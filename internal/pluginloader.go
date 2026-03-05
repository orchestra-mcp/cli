package internal

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	sdkplugin "github.com/orchestra-mcp/sdk-go/plugin"

	"github.com/orchestra-mcp/cli/internal/inprocess"
)

// ExternalProcess tracks a running plugin subprocess.
type ExternalProcess struct {
	Entry  *PluginEntry
	Cmd    *exec.Cmd
	Addr   string // QUIC address from "READY <addr>"
	Client *sdkplugin.OrchestratorClient
	Cancel context.CancelFunc
}

// loadExternalPlugins reads the plugin registry, spawns each enabled plugin
// as a child process, waits for READY, connects via QUIC, fetches tool defs,
// and registers as ExternalPlugin on the router.
func loadExternalPlugins(
	ctx context.Context,
	router *inprocess.Router,
	orchestratorAddr string,
	certsDir string,
) ([]*ExternalProcess, []func()) {

	reg, err := LoadRegistry()
	if err != nil {
		log.Printf("[pluginloader] warning: could not load registry: %v", err)
		return nil, nil
	}

	if len(reg.Plugins) == 0 {
		return nil, nil
	}

	sorted := topologicalSort(reg.Plugins)

	var processes []*ExternalProcess
	var cleanups []func()

	for _, entry := range sorted {
		if !entry.Enabled {
			log.Printf("[pluginloader] skipping disabled plugin: %s", entry.ID)
			continue
		}

		proc, err := spawnPlugin(ctx, entry, certsDir, orchestratorAddr)
		if err != nil {
			log.Printf("[pluginloader] failed to start %s: %v", entry.ID, err)
			continue
		}

		// Connect QUIC client to the plugin's READY address.
		clientTLS, err := inprocess.ClientTLSConfigForBridge(certsDir)
		if err != nil {
			log.Printf("[pluginloader] TLS config for %s: %v", entry.ID, err)
			proc.Cancel()
			continue
		}

		client, err := sdkplugin.NewOrchestratorClient(ctx, proc.Addr, clientTLS)
		if err != nil {
			log.Printf("[pluginloader] QUIC connect to %s at %s: %v", entry.ID, proc.Addr, err)
			proc.Cancel()
			continue
		}
		proc.Client = client

		// Fetch tool definitions via ListTools/ListPrompts RPCs.
		toolDefs, promptDefs, err := fetchPluginDefs(ctx, client)
		if err != nil {
			log.Printf("[pluginloader] fetch defs from %s: %v", entry.ID, err)
			client.Close()
			proc.Cancel()
			continue
		}

		// Register as ExternalPlugin on the router.
		ep := &inprocess.ExternalPlugin{
			ID:         entry.ID,
			ProvidesAI: entry.ProvidesAI,
			ToolDefs:   toolDefs,
			PromptDefs: promptDefs,
			Client:     client,
		}
		router.RegisterExternal(ep)

		processes = append(processes, proc)
		cleanups = append(cleanups, func() {
			client.Close()
			proc.Cancel()
		})

		log.Printf("[pluginloader] %s loaded (%d tools, addr: %s)",
			entry.ID, len(toolDefs), proc.Addr)
	}

	// Start background health checks.
	if len(processes) > 0 {
		go healthCheckLoop(ctx, processes)
	}

	return processes, cleanups
}

// spawnPlugin starts a plugin binary as a child process and waits for its
// "READY <addr>" line on stderr.
func spawnPlugin(
	ctx context.Context,
	entry *PluginEntry,
	certsDir string,
	orchestratorAddr string,
) (*ExternalProcess, error) {

	pluginCtx, cancel := context.WithCancel(ctx)

	args := []string{
		"--listen-addr", "localhost:0",
		"--certs-dir", certsDir,
	}

	// Storage-dependent plugins need the orchestrator address for storage RPCs.
	if orchestratorAddr != "" && (len(entry.NeedsStorage) > 0 || len(entry.NeedsTools) > 0) {
		args = append(args, "--orchestrator-addr", orchestratorAddr)
	}

	binaryPath := entry.Binary
	if runtime.GOOS == "windows" && !strings.HasSuffix(binaryPath, ".exe") {
		binaryPath += ".exe"
	}
	cmd := exec.CommandContext(pluginCtx, binaryPath, args...)

	// Propagate the orchestra workspace so child plugins know the project root.
	cmd.Env = append(os.Environ(), "ORCHESTRA_WORKSPACE="+os.Getenv("ORCHESTRA_WORKSPACE"))

	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start %s: %w", entry.Binary, err)
	}

	// Wait for "READY <addr>" with timeout.
	addrCh := make(chan string, 1)
	errCh := make(chan error, 1)

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "READY ") {
				addrCh <- strings.TrimPrefix(line, "READY ")
				return
			}
		}
		errCh <- fmt.Errorf("plugin %s exited without READY", entry.ID)
	}()

	// Monitor process exit.
	go func() {
		if err := cmd.Wait(); err != nil {
			select {
			case errCh <- fmt.Errorf("plugin %s process died: %w", entry.ID, err):
			default:
			}
		}
	}()

	select {
	case addr := <-addrCh:
		return &ExternalProcess{
			Entry:  entry,
			Cmd:    cmd,
			Addr:   addr,
			Cancel: cancel,
		}, nil

	case err := <-errCh:
		cancel()
		return nil, err

	case <-time.After(10 * time.Second):
		cancel()
		return nil, fmt.Errorf("timeout waiting for READY from %s", entry.ID)
	}
}

// fetchPluginDefs connects to a running plugin and fetches its tool and prompt
// definitions via the ListTools and ListPrompts RPCs.
func fetchPluginDefs(
	ctx context.Context,
	client *sdkplugin.OrchestratorClient,
) ([]*pluginv1.ToolDefinition, []*pluginv1.PromptDefinition, error) {

	toolResp, err := client.Send(ctx, &pluginv1.PluginRequest{
		Request: &pluginv1.PluginRequest_ListTools{
			ListTools: &pluginv1.ListToolsRequest{},
		},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("ListTools: %w", err)
	}
	toolDefs := toolResp.GetListTools().GetTools()

	promptResp, err := client.Send(ctx, &pluginv1.PluginRequest{
		Request: &pluginv1.PluginRequest_ListPrompts{
			ListPrompts: &pluginv1.ListPromptsRequest{},
		},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("ListPrompts: %w", err)
	}
	promptDefs := promptResp.GetListPrompts().GetPrompts()

	return toolDefs, promptDefs, nil
}

// topologicalSort orders plugins so dependencies start before dependants.
// Three tiers: AI bridges first (no deps), then standalone tools, then
// storage/tool-dependent plugins last (they need the orchestrator bridge ready).
func topologicalSort(plugins map[string]*PluginEntry) []*PluginEntry {
	var tier1, tier2, tier3 []*PluginEntry

	for _, entry := range plugins {
		switch {
		case len(entry.ProvidesAI) > 0:
			tier1 = append(tier1, entry) // AI bridges — standalone
		case len(entry.NeedsStorage) == 0 && len(entry.NeedsTools) == 0:
			tier2 = append(tier2, entry) // Standalone tools
		default:
			tier3 = append(tier3, entry) // Storage/tool-dependent
		}
	}

	// Stable sort within each tier by ID.
	sortByID := func(s []*PluginEntry) {
		sort.Slice(s, func(i, j int) bool { return s[i].ID < s[j].ID })
	}
	sortByID(tier1)
	sortByID(tier2)
	sortByID(tier3)

	result := make([]*PluginEntry, 0, len(plugins))
	result = append(result, tier1...)
	result = append(result, tier2...)
	result = append(result, tier3...)
	return result
}

// shutdownExternalPlugins gracefully shuts down all external plugin processes.
func shutdownExternalPlugins(processes []*ExternalProcess, cleanups []func()) {
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	var wg sync.WaitGroup
	for _, proc := range processes {
		wg.Add(1)
		go func(p *ExternalProcess) {
			defer wg.Done()
			if p.Client != nil {
				// Send shutdown RPC (best-effort).
				p.Client.Send(shutdownCtx, &pluginv1.PluginRequest{
					Request: &pluginv1.PluginRequest_Shutdown{
						Shutdown: &pluginv1.ShutdownRequest{},
					},
				})
			}
		}(proc)
	}
	wg.Wait()

	for _, cleanup := range cleanups {
		cleanup()
	}
}

// healthCheckLoop periodically pings external plugins and logs unhealthy ones.
func healthCheckLoop(ctx context.Context, processes []*ExternalProcess) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, proc := range processes {
				if proc.Client == nil {
					continue
				}
				checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				_, err := proc.Client.Send(checkCtx, &pluginv1.PluginRequest{
					Request: &pluginv1.PluginRequest_Health{
						Health: &pluginv1.HealthRequest{},
					},
				})
				cancel()
				if err != nil {
					log.Printf("[pluginloader] health check failed for %s: %v", proc.Entry.ID, err)
				}
			}
		}
	}
}

package internal

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/orchestra-mcp/cli/internal/inprocess"
	"github.com/orchestra-mcp/sdk-go/plugin"

	// ----------------------------------------------------------------
	// Core plugins (always bundled in-process, never removable)
	// ----------------------------------------------------------------
	storagemarkdown "github.com/orchestra-mcp/plugin-storage-markdown"
	toolsfeatures "github.com/orchestra-mcp/plugin-tools-features"
	toolsmarketplace "github.com/orchestra-mcp/plugin-tools-marketplace"
	transportstdio "github.com/orchestra-mcp/plugin-transport-stdio"
)

func RunServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	workspace := fs.String("workspace", ".", "Project workspace directory")
	tcpAddr := fs.String("tcp-addr", "localhost:50101", "TCP address for desktop app connections")
	logPath := fs.String("log", "", "Log file path (default: <workspace>/.orchestra-mcp.log)")
	noPlugins := fs.Bool("no-plugins", false, "Skip loading external plugins (core-only mode)")
	fs.Parse(args)

	// Resolve absolute workspace path.
	absWorkspace, err := filepath.Abs(*workspace)
	if err != nil {
		fatal("resolve workspace: %v", err)
	}

	// Set up log file.
	logFile := *logPath
	if logFile == "" {
		logFile = filepath.Join(absWorkspace, ".orchestra-mcp.log")
	}
	os.WriteFile(logFile, nil, 0644)
	lf, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fatal("open log: %v", err)
	}
	defer lf.Close()
	log.SetOutput(lf)

	// ----------------------------------------------------------------
	// Create the in-process router.
	// ----------------------------------------------------------------
	router := inprocess.NewRouter()

	// ================================================================
	// PHASE 1: CORE PLUGINS (always in-process)
	// ================================================================

	// 1a. Storage (must be first — other plugins depend on it)
	router.SetStorageHandler(storagemarkdown.NewStorage(absWorkspace))
	log.Printf("[serve] storage.markdown initialized (workspace: %s)", absWorkspace)

	// 1b. Core tools
	initStoragePlugin(router, "tools.features", func(b *plugin.PluginBuilder) {
		toolsfeatures.Register(b, router)
	})

	initStoragePlugin(router, "tools.marketplace", func(b *plugin.PluginBuilder) {
		toolsmarketplace.Register(b, router, absWorkspace)
	})

	// ================================================================
	// PHASE 2: EXTERNAL PLUGINS (from ~/.orchestra/plugins/registry.json)
	// ================================================================
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	certsDir := defaultCertsDir()
	var externalProcesses []*ExternalProcess
	var pluginCleanups []func()

	if !*noPlugins {
		// Start QUIC bridge so child plugins can make storage/cross-plugin RPCs.
		orchestratorAddr, err := router.ListenAndServeQUIC(ctx, "localhost:0", certsDir)
		if err != nil {
			log.Printf("[serve] QUIC bridge error: %v (external plugins disabled)", err)
		} else {
			externalProcesses, pluginCleanups = loadExternalPlugins(ctx, router, orchestratorAddr, certsDir)
		}
	}

	// ================================================================
	// PHASE 3: SIGNAL HANDLING
	// ================================================================
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("[serve] shutting down...")
		// Shutdown external plugins first.
		shutdownExternalPlugins(externalProcesses, pluginCleanups)
		cancel()
	}()

	// ================================================================
	// PHASE 4: TRANSPORT (TCP + stdio)
	// ================================================================

	// TCP server for desktop app connections (Swift, Windows, Linux).
	tcpServer := inprocess.NewTCPServer(*tcpAddr, router)
	go func() {
		if err := tcpServer.ListenAndServe(ctx); err != nil {
			log.Printf("[serve] TCP server error: %v", err)
		}
	}()

	// Stdio transport for this IDE session (stdin/stdout JSON-RPC).
	transport := transportstdio.NewTransport(router, os.Stdin, os.Stdout)

	toolCount := len(router.ListToolNames())
	coreCount := 3 // storage.markdown + tools.features + tools.marketplace
	extCount := len(externalProcesses)
	log.Printf("[serve] ready — %d tools from %d core + %d external plugins", toolCount, coreCount, extCount)

	if err := transport.Run(ctx); err != nil {
		if ctx.Err() == nil {
			log.Printf("[serve] transport error: %v", err)
		}
	}
}

// initStoragePlugin creates a plugin builder, calls the register function to
// add tools, exports the plugin, and registers it on the router.
func initStoragePlugin(router *inprocess.Router, id string, register func(b *plugin.PluginBuilder)) {
	builder := plugin.New(id).NeedsStorage("markdown")
	register(builder)
	ep := builder.Export()
	router.RegisterPlugin(ep)
	log.Printf("[serve] %s initialized (%d tools)", id, len(ep.Tools))
}

func defaultCertsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".orchestra", "certs")
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "orchestra: "+format+"\n", args...)
	os.Exit(1)
}

func resolveHome(path string) string {
	if strings.HasPrefix(path, "~") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[1:])
	}
	return path
}

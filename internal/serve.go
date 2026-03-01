package internal

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// pluginConfig mirrors the orchestrator's PluginConfig for YAML generation.
type pluginConfig struct {
	ID              string   `yaml:"id"`
	Binary          string   `yaml:"binary"`
	Enabled         bool     `yaml:"enabled"`
	ProvidesStorage []string `yaml:"provides_storage,omitempty"`
	Args            []string `yaml:"args,omitempty"`
}

type orchestratorConfig struct {
	ListenAddr string         `yaml:"listen_addr"`
	CertsDir   string         `yaml:"certs_dir"`
	Plugins    []pluginConfig `yaml:"plugins"`
}

// lockFile path — stores "pid:addr" of the running orchestrator process.
func orchLockFile(workspace string) string {
	return filepath.Join(workspace, ".orchestra-mcp.lock")
}

// readLock returns the orchestrator pid and listen addr if a live lock exists.
func readLock(workspace string) (pid int, addr string, ok bool) {
	data, err := os.ReadFile(orchLockFile(workspace))
	if err != nil {
		return 0, "", false
	}
	parts := strings.SplitN(strings.TrimSpace(string(data)), ":", 2)
	if len(parts) != 2 {
		return 0, "", false
	}
	p, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, "", false
	}
	// Check the process is alive.
	proc, err := os.FindProcess(p)
	if err != nil {
		return 0, "", false
	}
	if proc.Signal(syscall.Signal(0)) != nil {
		return 0, "", false
	}
	return p, parts[1], true
}

func writeLock(workspace string, pid int, addr string) {
	os.WriteFile(orchLockFile(workspace), fmt.Appendf(nil, "%d:%s", pid, addr), 0644)
}

func removeLock(workspace string) {
	os.Remove(orchLockFile(workspace))
}

func RunServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	workspace := fs.String("workspace", ".", "Project workspace directory")
	certsDir := fs.String("certs-dir", defaultCertsDir(), "mTLS certificates directory")
	logPath := fs.String("log", "", "Log file path (default: <workspace>/.orchestra-mcp.log)")
	fs.Parse(args)

	// Resolve absolute paths.
	absWorkspace, err := filepath.Abs(*workspace)
	if err != nil {
		fatal("resolve workspace: %v", err)
	}

	absCertsDir := *certsDir
	if strings.HasPrefix(absCertsDir, "~") {
		home, _ := os.UserHomeDir()
		absCertsDir = filepath.Join(home, absCertsDir[1:])
	}

	logFile := *logPath
	if logFile == "" {
		logFile = filepath.Join(absWorkspace, ".orchestra-mcp.log")
	}

	// Resolve sibling binaries.
	selfPath, err := os.Executable()
	if err != nil {
		fatal("resolve self path: %v", err)
	}
	selfPath, _ = filepath.EvalSymlinks(selfPath)
	binDir := filepath.Dir(selfPath)

	bins := map[string]string{
		"orchestrator":      filepath.Join(binDir, "orchestrator"),
		"storage-markdown":  filepath.Join(binDir, "storage-markdown"),
		"tools-features":    filepath.Join(binDir, "tools-features"),
		"tools-marketplace": filepath.Join(binDir, "tools-marketplace"),
		"transport-stdio":   filepath.Join(binDir, "transport-stdio"),
	}

	// Optional binaries — don't fail if missing.
	optionalBins := map[string]string{
		"engine-rag":            filepath.Join(binDir, "engine-rag"),
		"bridge-claude":         filepath.Join(binDir, "bridge-claude"),
		"bridge-openai":         filepath.Join(binDir, "bridge-openai"),
		"bridge-gemini":         filepath.Join(binDir, "bridge-gemini"),
		"bridge-ollama":         filepath.Join(binDir, "bridge-ollama"),
		"bridge-firecrawl":      filepath.Join(binDir, "bridge-firecrawl"),
		"tools-agentops":        filepath.Join(binDir, "tools-agentops"),
		"tools-sessions":        filepath.Join(binDir, "tools-sessions"),
		"agent-orchestrator":    filepath.Join(binDir, "agent-orchestrator"),
		"tools-workspace":       filepath.Join(binDir, "tools-workspace"),
		"transport-quic-bridge": filepath.Join(binDir, "transport-quic-bridge"),
	}
	available := map[string]bool{}
	for name, path := range optionalBins {
		if _, err := os.Stat(path); err == nil {
			available[name] = true
			bins[name] = path
		}
	}

	for name, path := range bins {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			fatal("missing binary %q at %s", name, path)
		}
	}

	// ----------------------------------------------------------------
	// Multi-session support: if a live orchestrator is already running
	// for this workspace, skip launching a new one and just attach a
	// new transport-stdio to it. Each Claude Code window gets its own
	// transport-stdio process — only one orchestrator and plugin set
	// runs at a time.
	// ----------------------------------------------------------------
	if _, orchAddr, ok := readLock(absWorkspace); ok {
		runTransportOnly(bins["transport-stdio"], orchAddr, absCertsDir, logFile)
		return
	}

	// No live orchestrator — we are the primary serve process.
	// Open log file (truncate for a fresh start).
	os.WriteFile(logFile, nil, 0644)
	lf, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fatal("open log: %v", err)
	}
	defer lf.Close()

	// Write temp config.
	cfg := orchestratorConfig{
		ListenAddr: "localhost:0",
		CertsDir:   absCertsDir,
		Plugins: []pluginConfig{
			{
				ID:              "storage.markdown",
				Binary:          bins["storage-markdown"],
				Enabled:         true,
				ProvidesStorage: []string{"markdown"},
				Args:            []string{fmt.Sprintf("--workspace=%s", absWorkspace)},
			},
			{
				ID:      "tools.features",
				Binary:  bins["tools-features"],
				Enabled: true,
			},
			{
				ID:      "tools.marketplace",
				Binary:  bins["tools-marketplace"],
				Enabled: true,
				Args:    []string{fmt.Sprintf("--workspace=%s", absWorkspace)},
			},
		},
	}

	if available["engine-rag"] {
		cfg.Plugins = append(cfg.Plugins, pluginConfig{
			ID:      "engine.rag",
			Binary:  bins["engine-rag"],
			Enabled: true,
			Args:    []string{fmt.Sprintf("--workspace=%s", absWorkspace)},
		})
	}

	if available["bridge-claude"] {
		cfg.Plugins = append(cfg.Plugins, pluginConfig{
			ID:      "bridge.claude",
			Binary:  bins["bridge-claude"],
			Enabled: true,
		})
	}

	if available["bridge-openai"] {
		cfg.Plugins = append(cfg.Plugins, pluginConfig{
			ID:      "bridge.openai",
			Binary:  bins["bridge-openai"],
			Enabled: true,
		})
	}

	if available["bridge-gemini"] {
		cfg.Plugins = append(cfg.Plugins, pluginConfig{
			ID:      "bridge.gemini",
			Binary:  bins["bridge-gemini"],
			Enabled: true,
		})
	}

	if available["bridge-ollama"] {
		cfg.Plugins = append(cfg.Plugins, pluginConfig{
			ID:      "bridge.ollama",
			Binary:  bins["bridge-ollama"],
			Enabled: true,
		})
	}

	if available["bridge-firecrawl"] {
		cfg.Plugins = append(cfg.Plugins, pluginConfig{
			ID:      "bridge.firecrawl",
			Binary:  bins["bridge-firecrawl"],
			Enabled: true,
		})
	}

	if available["tools-agentops"] {
		cfg.Plugins = append(cfg.Plugins, pluginConfig{
			ID:      "tools.agentops",
			Binary:  bins["tools-agentops"],
			Enabled: true,
		})
	}

	if available["tools-sessions"] {
		cfg.Plugins = append(cfg.Plugins, pluginConfig{
			ID:      "tools.sessions",
			Binary:  bins["tools-sessions"],
			Enabled: true,
		})
	}

	if available["agent-orchestrator"] {
		cfg.Plugins = append(cfg.Plugins, pluginConfig{
			ID:      "agent.orchestrator",
			Binary:  bins["agent-orchestrator"],
			Enabled: true,
		})
	}

	if available["tools-workspace"] {
		cfg.Plugins = append(cfg.Plugins, pluginConfig{
			ID:      "tools.workspace",
			Binary:  bins["tools-workspace"],
			Enabled: true,
		})
	}

	// Load third-party plugins from registry.
	registry, err := LoadRegistry()
	if err == nil && registry != nil {
		for _, p := range registry.Plugins {
			if _, err := os.Stat(p.Binary); err != nil {
				continue
			}
			cfg.Plugins = append(cfg.Plugins, pluginConfig{
				ID:              p.ID,
				Binary:          p.Binary,
				Enabled:         true,
				ProvidesStorage: p.ProvidesStorage,
				Args:            []string{fmt.Sprintf("--workspace=%s", absWorkspace)},
			})
		}
	}

	tmpFile, err := os.CreateTemp("", "orchestra-*.yaml")
	if err != nil {
		fatal("create temp config: %v", err)
	}
	tmpConfig := tmpFile.Name()
	data, _ := yaml.Marshal(&cfg)
	tmpFile.Write(data)
	tmpFile.Close()

	// Setup signal handling and cleanup.
	var orchCmd *exec.Cmd
	var quicBridgeCmd *exec.Cmd

	cleanup := func() {
		removeLock(absWorkspace)
		if quicBridgeCmd != nil && quicBridgeCmd.Process != nil {
			quicBridgeCmd.Process.Kill()
		}
		if orchCmd != nil && orchCmd.Process != nil {
			exec.Command("pkill", "-P", fmt.Sprintf("%d", orchCmd.Process.Pid)).Run()
			orchCmd.Process.Signal(syscall.SIGTERM)
			time.Sleep(300 * time.Millisecond)
			exec.Command("pkill", "-9", "-P", fmt.Sprintf("%d", orchCmd.Process.Pid)).Run()
			orchCmd.Process.Kill()
		}
		os.Remove(tmpConfig)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cleanup()
		os.Exit(0)
	}()
	defer cleanup()

	// Start orchestrator.
	orchCmd = exec.Command(bins["orchestrator"], "--config", tmpConfig)
	orchCmd.Stdout = lf
	orchCmd.Stderr = lf
	if err := orchCmd.Start(); err != nil {
		fatal("start orchestrator: %v", err)
	}

	// Wait for plugins to boot and extract the listen address.
	addrRe := regexp.MustCompile(`listening on (\S+)`)
	ready := false
	var orchAddr string
	for i := 0; i < 30; i++ {
		time.Sleep(500 * time.Millisecond)

		logData, _ := os.ReadFile(logFile)
		logStr := string(logData)

		booted := strings.Count(logStr, "registered and booted")
		expectedPlugins := len(cfg.Plugins)
		if booted >= expectedPlugins {
			if m := addrRe.FindStringSubmatch(logStr); len(m) >= 2 {
				orchAddr = m[1]
				ready = true
				break
			}
		}

		if orchCmd.ProcessState != nil {
			fatal("orchestrator exited unexpectedly. Check %s", logFile)
		}
	}

	if !ready {
		fatal("orchestrator did not become ready in 15 seconds. Check %s", logFile)
	}

	// Write lock so secondary serve processes can attach without launching a new orchestrator.
	writeLock(absWorkspace, orchCmd.Process.Pid, orchAddr)

	// Start QUIC bridge (optional, background).
	if available["transport-quic-bridge"] {
		quicBridgeCmd = exec.Command(bins["transport-quic-bridge"],
			fmt.Sprintf("--orchestrator-addr=%s", orchAddr),
			fmt.Sprintf("--certs-dir=%s", absCertsDir),
			"--listen-addr=:9200",
		)
		quicBridgeCmd.Stdout = lf
		quicBridgeCmd.Stderr = lf
		if err := quicBridgeCmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "orchestra: warning: failed to start quic-bridge: %v\n", err)
		}
	}

	// Run transport-stdio for this session (stdin/stdout passthrough).
	runTransportOnly(bins["transport-stdio"], orchAddr, absCertsDir, logFile)
}

// runTransportOnly starts a transport-stdio process and waits for it to exit.
// Used both by the primary serve (after launching the orchestrator) and by
// secondary serve processes that attach to an already-running orchestrator.
func runTransportOnly(transportBin, orchAddr, certsDir, logFile string) {
	lf, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		// Non-fatal — fall back to stderr.
		lf = os.Stderr
	} else {
		defer lf.Close()
	}

	cmd := exec.Command(transportBin,
		fmt.Sprintf("--orchestrator-addr=%s", orchAddr),
		fmt.Sprintf("--certs-dir=%s", certsDir),
	)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = lf

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
	}
}

func defaultCertsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".orchestra", "certs")
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "orchestra: "+format+"\n", args...)
	os.Exit(1)
}

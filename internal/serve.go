package internal

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
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
		"orchestrator":     filepath.Join(binDir, "orchestrator"),
		"storage-markdown": filepath.Join(binDir, "storage-markdown"),
		"tools-features":   filepath.Join(binDir, "tools-features"),
		"transport-stdio":  filepath.Join(binDir, "transport-stdio"),
	}
	for name, path := range bins {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			fatal("missing binary %q at %s", name, path)
		}
	}

	// Kill stale processes.
	for _, bin := range bins {
		exec.Command("pkill", "-9", "-f", bin).Run()
	}
	time.Sleep(500 * time.Millisecond)

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
		},
	}

	// Load third-party plugins from registry.
	registry, err := LoadRegistry()
	if err == nil && registry != nil {
		for _, p := range registry.Plugins {
			// Verify binary still exists.
			if _, err := os.Stat(p.Binary); err != nil {
				continue // skip missing binaries
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

	// Truncate log.
	os.WriteFile(logFile, nil, 0644)

	// Setup signal handling and cleanup.
	var orchCmd *exec.Cmd
	cleanup := func() {
		if orchCmd != nil && orchCmd.Process != nil {
			// Kill children first, then orchestrator.
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
	lf, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fatal("open log: %v", err)
	}
	defer lf.Close()

	orchCmd = exec.Command(bins["orchestrator"], "--config", tmpConfig)
	orchCmd.Stdout = lf
	orchCmd.Stderr = lf
	if err := orchCmd.Start(); err != nil {
		fatal("start orchestrator: %v", err)
	}

	// Write PID file.
	pidFile := filepath.Join(absWorkspace, ".orchestra-mcp.pid")
	os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", orchCmd.Process.Pid)), 0644)
	defer os.Remove(pidFile)

	// Wait for plugins to register.
	addrRe := regexp.MustCompile(`listening on (\S+)`)
	ready := false
	for i := 0; i < 30; i++ {
		time.Sleep(500 * time.Millisecond)

		logData, _ := os.ReadFile(logFile)
		logStr := string(logData)

		booted := strings.Count(logStr, "registered and booted")
		if booted >= 2 {
			ready = true
			break
		}

		// Check if orchestrator is still alive.
		if orchCmd.ProcessState != nil {
			fatal("orchestrator exited unexpectedly. Check %s", logFile)
		}
	}

	if !ready {
		fatal("orchestrator did not become ready in 15 seconds. Check %s", logFile)
	}

	// Extract listen address.
	logData, _ := os.ReadFile(logFile)
	matches := addrRe.FindStringSubmatch(string(logData))
	if len(matches) < 2 {
		fatal("could not determine orchestrator address. Check %s", logFile)
	}
	orchAddr := matches[1]

	// Run transport-stdio (stdin/stdout passthrough).
	transportCmd := exec.Command(bins["transport-stdio"],
		fmt.Sprintf("--orchestrator-addr=%s", orchAddr),
		fmt.Sprintf("--certs-dir=%s", absCertsDir),
	)
	transportCmd.Stdin = os.Stdin
	transportCmd.Stdout = os.Stdout
	transportCmd.Stderr = lf

	if err := transportCmd.Run(); err != nil {
		// Transport exited — this is normal when stdin closes.
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

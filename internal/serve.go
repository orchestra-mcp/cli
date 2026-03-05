package internal

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"time"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/orchestra-mcp/cli/internal/inprocess"
	"github.com/orchestra-mcp/sdk-go/globaldb"
	"github.com/orchestra-mcp/sdk-go/plugin"

	// ----------------------------------------------------------------
	// Core plugins (always bundled in-process, never removable)
	// ----------------------------------------------------------------
	storagemarkdown "github.com/orchestra-mcp/plugin-storage-markdown"
	storagesqlite "github.com/orchestra-mcp/plugin-storage-sqlite"
	toolsfeatures "github.com/orchestra-mcp/plugin-tools-features"
	toolsmarketplace "github.com/orchestra-mcp/plugin-tools-marketplace"
	transportstdio "github.com/orchestra-mcp/plugin-transport-stdio"
)

func RunServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	workspace := fs.String("workspace", ".", "Project workspace directory")
	tcpAddr := fs.String("tcp-addr", "localhost:50101", "TCP address for desktop app connections")
	logPath := fs.String("log", "", "Log file path (default: OS log directory)")
	noPlugins := fs.Bool("no-plugins", false, "Skip loading external plugins (core-only mode)")
	storageBackend := fs.String("storage", "sqlite", "Storage backend: sqlite (default) or markdown")
	fs.Parse(args)

	// Resolve absolute workspace path.
	absWorkspace, err := filepath.Abs(*workspace)
	if err != nil {
		fatal("resolve workspace: %v", err)
	}

	// Export workspace so child plugins can discover the project root.
	os.Setenv("ORCHESTRA_WORKSPACE", absWorkspace)

	// State directory for PID and log files — never in the project root.
	stateDir := orchestraStateDir()
	os.MkdirAll(stateDir, 0755)

	// Acquire PID lock file — only one orchestra serve per workspace.
	// PID file is keyed by workspace slug to allow multiple workspaces.
	pidFile := filepath.Join(stateDir, workspaceSlug(absWorkspace)+".pid")
	if err := acquirePIDLock(pidFile); err != nil {
		// Another instance is running — stop it and take over.
		// MCP clients (Claude Code, Cursor) expect to own the stdio process.
		// When they restart /mcp, the old process must yield.
		fmt.Fprintf(os.Stderr, "orchestra: %v — stopping it to take over\n", err)
		stopExistingInstance(pidFile)
		// Retry acquiring the lock after stopping.
		if err := acquirePIDLock(pidFile); err != nil {
			fmt.Fprintf(os.Stderr, "orchestra: still blocked: %v\n", err)
			os.Exit(1)
		}
	}
	defer os.Remove(pidFile)

	// Set up log file in the OS log directory.
	logFile := *logPath
	if logFile == "" {
		logDir := orchestraLogDir()
		os.MkdirAll(logDir, 0755)
		logFile = filepath.Join(logDir, workspaceSlug(absWorkspace)+".log")
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
	if *storageBackend == "markdown" {
		router.SetStorageHandler(storagemarkdown.NewStorage(absWorkspace))
		log.Printf("[serve] storage.markdown initialized (workspace: %s)", absWorkspace)
	} else {
		router.SetStorageHandler(storagesqlite.NewStorage(absWorkspace))
		log.Printf("[serve] storage.sqlite initialized (workspace: %s)", absWorkspace)
	}

	// 1a-2. Global database — migrate JSON configs (me.json, accounts.json, workspaces.json)
	// into globaldb on first run. These are machine-local and must NOT be in git.
	globaldb.MigrateMeJSON()
	globaldb.MigrateAccountsJSON()
	globaldb.MigrateWorkspacesJSON()
	defer globaldb.Close()

	// 1a-3. Self-update tools + background version check.
	registerUpdateTools(router)
	go backgroundVersionCheck()

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
	if runtime.GOOS == "windows" {
		signal.Notify(sigCh, os.Interrupt)
	} else {
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	}
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

	toolCount := len(router.ListToolNames())
	coreCount := 3 // storage.markdown + tools.features + tools.marketplace
	extCount := len(externalProcesses)
	log.Printf("[serve] ready — %d tools from %d core + %d external plugins", toolCount, coreCount, extCount)

	// Run stdio transport in a goroutine. If stdin closes (desktop app mode),
	// the transport returns but the process keeps running for TCP connections.
	// The process only exits on SIGINT/SIGTERM.
	go func() {
		transport := transportstdio.NewTransport(router, os.Stdin, os.Stdout)
		if err := transport.Run(ctx); err != nil {
			if ctx.Err() == nil {
				log.Printf("[serve] stdio transport ended: %v", err)
			}
		}
	}()

	// Block until signal (SIGINT/SIGTERM handled above).
	<-ctx.Done()
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

// acquirePIDLock writes our PID to the lock file. If the file already exists
// and the PID inside is still alive AND is actually an orchestra process, returns
// an error (another instance is running). Stale PIDs from crashed processes or
// PIDs reused by unrelated processes are ignored.
func acquirePIDLock(path string) error {
	data, err := os.ReadFile(path)
	if err == nil {
		var pid int
		if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); err == nil && pid > 0 && pid != os.Getpid() {
			// Check if the PID is still alive AND is actually an orchestra process.
			// Signal(0) alone is not enough — on macOS/Linux, any existing process
			// will pass, even if the PID was reused by an unrelated process.
			if isOrchestraProcess(pid) {
				return fmt.Errorf("another orchestra serve is running (pid %d)", pid)
			}
		}
	}
	return os.WriteFile(path, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0644)
}

// isOrchestraProcess checks if a PID belongs to a running orchestra process.
func isOrchestraProcess(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0: check if process exists at all.
	if proc.Signal(syscall.Signal(0)) != nil {
		return false
	}
	// Verify this is actually an orchestra process by checking /proc or ps.
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		out, err := exec.Command("ps", "-p", fmt.Sprintf("%d", pid), "-o", "comm=").Output()
		if err != nil {
			return false
		}
		return strings.Contains(strings.TrimSpace(string(out)), "orchestra")
	}
	return false // on unknown OS, assume stale
}

// stopExistingInstance reads the PID from the lock file and sends SIGTERM to
// gracefully stop the previous orchestra process, then waits for it to exit.
func stopExistingInstance(pidFile string) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); err != nil || pid <= 0 {
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	// Send SIGTERM for graceful shutdown.
	if runtime.GOOS == "windows" {
		proc.Kill()
	} else {
		proc.Signal(syscall.SIGTERM)
	}
	// Wait briefly for the process to exit.
	for i := 0; i < 20; i++ { // up to 2 seconds
		if proc.Signal(syscall.Signal(0)) != nil {
			break // process exited
		}
		time.Sleep(100 * time.Millisecond)
	}
	os.Remove(pidFile)
}

func resolveHome(path string) string {
	if strings.HasPrefix(path, "~") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[1:])
	}
	return path
}

// orchestraStateDir returns the OS-appropriate directory for runtime state
// (PID files). Located under ~/.orchestra/run/ on all platforms.
func orchestraStateDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".orchestra", "run")
}

// orchestraLogDir returns the OS-appropriate directory for log files.
//   - macOS:   ~/Library/Logs/Orchestra/
//   - Linux:   ~/.local/state/orchestra/
//   - Windows: %LOCALAPPDATA%\Orchestra\Logs\
func orchestraLogDir() string {
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Logs", "Orchestra")
	case "windows":
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			return filepath.Join(localAppData, "Orchestra", "Logs")
		}
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "AppData", "Local", "Orchestra", "Logs")
	default: // linux and others
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".local", "state", "orchestra")
	}
}

// workspaceSlug creates a filesystem-safe slug from a workspace path for use
// in PID and log filenames. E.g. "/Users/alice/Sites/my-project" → "my-project".
func workspaceSlug(absPath string) string {
	base := filepath.Base(absPath)
	// Replace any non-alphanumeric/hyphen/underscore chars with hyphens.
	var b strings.Builder
	for _, r := range strings.ToLower(base) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "default"
	}
	return slug
}

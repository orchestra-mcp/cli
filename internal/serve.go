package internal

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/orchestra-mcp/cli/internal/inprocess"
	"github.com/orchestra-mcp/sdk-go/globaldb"
	"github.com/orchestra-mcp/sdk-go/plugin"

	// ----------------------------------------------------------------
	// Core plugins (always bundled in-process, never removable)
	// ----------------------------------------------------------------
	storagemarkdown "github.com/orchestra-mcp/plugin-storage-markdown"
	storagesqlite "github.com/orchestra-mcp/plugin-storage-sqlite"
	synccloud "github.com/orchestra-mcp/plugin-sync-cloud"
	toolsdocs "github.com/orchestra-mcp/plugin-tools-docs"
	toolsfeatures "github.com/orchestra-mcp/plugin-tools-features"
	toolsmarketplace "github.com/orchestra-mcp/plugin-tools-marketplace"
	toolsnotes "github.com/orchestra-mcp/plugin-tools-notes"
	transportstdio "github.com/orchestra-mcp/plugin-transport-stdio"
)

// instanceInfo is written alongside the PID file so that new invocations can
// discover and connect to the already-running instance instead of killing it.
type instanceInfo struct {
	PID         int    `json:"pid"`
	TCPAddr     string `json:"tcp_addr"`
	WebGateAddr string `json:"web_gate_addr,omitempty"`
	Workspace   string `json:"workspace"`
}

func RunServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	workspace := fs.String("workspace", ".", "Project workspace directory")
	tcpAddr := fs.String("tcp-addr", "", "TCP address for desktop app connections (default: auto-assigned per workspace)")
	webGateAddr := fs.String("web-gate", envOrDefault("ORCHESTRA_WEB_GATE", ":9201"), "WebSocket gateway address for browser clients (default :9201)")
	webGateKey := fs.String("web-gate-key", envOrDefault("ORCHESTRA_WEB_GATE_KEY", ""), "API key for web-gate authentication (empty = no auth)")
	webGateCORS := fs.String("web-gate-cors", envOrDefault("ORCHESTRA_WEB_GATE_CORS", ""), "Comma-separated CORS origins for web-gate (empty = allow all)")
	logPath := fs.String("log", "", "Log file path (default: OS log directory)")
	noPlugins := fs.Bool("no-plugins", false, "Skip loading external plugins (core-only mode)")
	noPID := fs.Bool("no-pid", false, "Skip PID lock (for embedded/child process use)")
	storageBackend := fs.String("storage", "sqlite", "Storage backend: sqlite (default) or markdown")
	cloudURL := fs.String("cloud-url", envOrDefault("ORCHESTRA_CLOUD_URL", ""), "Cloud server URL for reverse tunnel (e.g., https://yourdomain.com)")
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
	// Skip when --no-pid is set (embedded/child process mode, e.g. Swift app).
	slug := workspaceSlug(absWorkspace)
	pidFile := filepath.Join(stateDir, slug+".pid")
	infoFile := filepath.Join(stateDir, slug+".info")
	if !*noPID {
		if err := acquirePIDLock(pidFile); err != nil {
			// Another instance is running. Behavior depends on caller:
			//
			// Interactive terminal (user typed `orchestra serve --web-gate`):
			//   → Reuse existing instance. Show web-gate token, block until ESC.
			//   → This avoids killing the MCP client's instance.
			//
			// Piped stdin (MCP client like Claude Code / Cursor):
			//   → Must take over because MCP clients need their own stdio transport.
			//   → Kill existing and start fresh (original behavior).
			info, rerr := readInstanceInfo(infoFile)
			if rerr != nil {
				info = &instanceInfo{
					PID:     readPIDFromFile(pidFile),
					TCPAddr: *tcpAddr,
				}
			}
			healthy := isInstanceHealthy(info.TCPAddr)
			stdinIsPipe := !isTerminal(os.Stdin)

			if healthy {
				if stdinIsPipe {
					// MCP client (piped stdin) → proxy stdin/stdout to existing instance via TCP.
					// No new server, no duplicate plugins — just a thin bridge.
					fmt.Fprintf(os.Stderr, "orchestra: proxying to existing instance (pid %d, tcp %s)\n", info.PID, info.TCPAddr)
					runProxyMode(info)
					return
				}
				// Interactive terminal: reuse existing instance (show web-gate token, block until ESC).
				fmt.Fprintf(os.Stderr, "orchestra: reusing existing instance (pid %d, tcp %s)\n", info.PID, info.TCPAddr)
				runAttachedMode(info, *webGateAddr, *webGateKey)
				return
			} else {
				// Unhealthy instance — stop it and take over.
				fmt.Fprintf(os.Stderr, "orchestra: %v — stopping it to take over\n", err)
				stopExistingInstance(pidFile)
				os.Remove(infoFile)
				if err := acquirePIDLock(pidFile); err != nil {
					fmt.Fprintf(os.Stderr, "orchestra: still blocked: %v\n", err)
					os.Exit(1)
				}
			}
		}
		defer os.Remove(pidFile)
		defer os.Remove(infoFile)
	}

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

	initStoragePlugin(router, "tools.notes", func(b *plugin.PluginBuilder) {
		toolsnotes.Register(b, router)
	})

	initStoragePlugin(router, "tools.docs", func(b *plugin.PluginBuilder) {
		toolsdocs.Register(b, router, absWorkspace)
	})

	// 1c. Sync-cloud plugin (data sync to cloud dashboard)
	var syncNow synccloud.SyncNowFunc
	var syncSetAuth synccloud.SetAuthFunc
	var syncCleanup synccloud.Cleanup
	initStoragePlugin(router, "sync.cloud", func(b *plugin.PluginBuilder) {
		syncCleanup, syncNow, syncSetAuth = synccloud.Register(b, router)
	})
	defer func() {
		if syncCleanup != nil {
			syncCleanup()
		}
	}()

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

	// Web Gate server for browser clients (WebSocket JSON-RPC gateway).
	if *webGateAddr != "" {
		var corsOrigins []string
		if *webGateCORS != "" {
			for _, o := range strings.Split(*webGateCORS, ",") {
				if trimmed := strings.TrimSpace(o); trimmed != "" {
					corsOrigins = append(corsOrigins, trimmed)
				}
			}
		}
		webGate := inprocess.NewWebGateServer(router, *webGateKey, *cloudURL, absWorkspace, corsOrigins)
		go func() {
			if err := webGate.ListenAndServe(ctx, *webGateAddr); err != nil {
				log.Printf("[serve] web-gate error: %v", err)
			}
		}()
		// Wait briefly for the listener to start, then display the token.
		// This must happen BEFORE WatchDoubleEsc puts the terminal into raw mode,
		// otherwise \n won't produce carriage returns and output gets garbled.
		time.Sleep(300 * time.Millisecond)
		raw, token, err := webGate.GenerateRegistrationToken()
		if err != nil {
			log.Printf("[serve] tunnel token error: %v", err)
		} else {
			fmt.Fprint(os.Stderr, inprocess.FormatTokenDisplay(raw, token))
			log.Printf("[serve] tunnel registration token generated for %s", token.GateAddress)
		}

		// If --cloud-url is set, start the reverse tunnel flow:
		// 1. Poll the cloud server to claim tunnel credentials after user pastes token
		// 2. On success, establish persistent reverse WebSocket to cloud
		if *cloudURL != "" && token != nil {
			go func() {
				inprocess.TunnelLog(33, "Waiting for registration at %s ...", *cloudURL)
				log.Printf("[serve] waiting for tunnel registration at %s ...", *cloudURL)
				tunnelID, connToken, teamID, authToken, err := inprocess.ClaimTunnel(ctx, *cloudURL, token.Nonce)
				if err != nil {
					inprocess.TunnelLog(31, "Claim failed: %v", err)
					log.Printf("[serve] tunnel claim failed: %v", err)
					return
				}
				inprocess.TunnelLog(32, "Claimed: %s", tunnelID)
				log.Printf("[serve] tunnel claimed: %s — starting reverse connection", tunnelID)

				// Configure sync engine with tunnel context credentials.
				if syncSetAuth != nil && authToken != "" {
					syncSetAuth(authToken, teamID, tunnelID, tunnelID)
					log.Printf("[serve] sync-cloud configured for tunnel %s (team %s)", tunnelID, teamID)
				}

				inprocess.TunnelLog(32, "Starting reverse connection to %s ...", *cloudURL)

				rt := inprocess.NewReverseTunnelClient(*cloudURL, tunnelID, connToken, router)
				rt.OnConnect = func(ctx context.Context) {
					if syncNow == nil {
						return
					}
					inprocess.TunnelLog(33, "Syncing data to cloud ...")
					applied, _, errs, err := syncNow(ctx)
					if err != nil {
						inprocess.TunnelLog(31, "Sync failed: %v", err)
					} else if applied > 0 || errs > 0 {
						inprocess.TunnelLog(32, "Synced: %d applied, %d errors", applied, errs)
					} else {
						inprocess.TunnelLog(32, "Sync: up to date")
					}
				}
				rt.ReconnectLoop(ctx)
			}()
		}
	}

	// Resolve TCP address: if not explicitly set, derive a deterministic port
	// from the workspace path so each workspace gets its own port.
	resolvedTCPAddr := *tcpAddr
	if resolvedTCPAddr == "" {
		resolvedTCPAddr = fmt.Sprintf("localhost:%d", workspaceTCPPort(absWorkspace))
	}

	// TCP server for desktop app connections (Swift, Windows, Linux).
	tcpServer := inprocess.NewTCPServer(resolvedTCPAddr, router)
	go func() {
		if err := tcpServer.ListenAndServe(ctx); err != nil {
			log.Printf("[serve] TCP server error: %v", err)
		}
	}()

	toolCount := len(router.ListToolNames())
	coreCount := 5 // storage + tools.features + tools.marketplace + tools.notes + tools.docs
	extCount := len(externalProcesses)
	log.Printf("[serve] ready — %d tools from %d core + %d external plugins", toolCount, coreCount, extCount)

	// Write instance info so new invocations can attach to this instance.
	if !*noPID {
		actualWebGate := ""
		if *webGateAddr != "" {
			// webGate variable is in the if-block above, use the addr flag directly.
			actualWebGate = *webGateAddr
		}
		writeInstanceInfo(infoFile, &instanceInfo{
			PID:         os.Getpid(),
			TCPAddr:     tcpServer.Addr(),
			WebGateAddr: actualWebGate,
			Workspace:   absWorkspace,
		})
	}

	// Background lock reaper — clean expired session locks every 5 minutes.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				globaldb.CleanExpiredLocks(30 * time.Minute)
			}
		}
	}()

	// Run stdio transport in a goroutine. If stdin closes (desktop app mode),
	// the transport returns but the process keeps running for TCP connections.
	// The process only exits on SIGINT/SIGTERM.
	go func() {
		transport := transportstdio.NewTransport(router, os.Stdin, os.Stdout,
			transportstdio.WithOnDisconnect(func(sessionID string) {
				log.Printf("[serve] session %s disconnected — releasing locks", sessionID)
				globaldb.ReleaseSessionLocks(sessionID)
			}),
		)
		if err := transport.Run(ctx); err != nil {
			if ctx.Err() == nil {
				log.Printf("[serve] stdio transport ended: %v", err)
			}
		}
	}()

	// ESC quit for interactive terminal sessions.
	// Opens /dev/tty directly so it works even when stdin is used by stdio transport.
	WatchEscQuit(func() {
		sigCh <- syscall.SIGTERM
	})

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
// It also kills any orphaned orchestra plugin child processes.
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

	// Kill orphaned orchestra processes (other serve instances and their child plugins).
	killOrphanedProcesses()
}

// killOrphanedProcesses finds and kills any leftover orchestra serve instances
// and their child plugin processes. This handles the case where multiple
// instances were spawned (e.g., by different MCP clients) and their child
// processes were not cleaned up properly.
func killOrphanedProcesses() {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return
	}
	myPID := fmt.Sprintf("%d", os.Getpid())

	// Kill orchestra serve processes (except ourselves).
	killMatchingProcesses("bin/orchestra", myPID)

	// Kill orphaned plugin child processes.
	killMatchingProcesses("plugin-bridge-", myPID)
	killMatchingProcesses("plugin-tools-", myPID)
	killMatchingProcesses("plugin-storage-", myPID)
	killMatchingProcesses("plugin-transport-", myPID)
	killMatchingProcesses("plugin-engine-", myPID)
	killMatchingProcesses("plugin-agent-", myPID)

	// Brief wait for cleanup.
	time.Sleep(500 * time.Millisecond)
}

// killMatchingProcesses sends SIGTERM to all processes whose command line
// contains the given pattern, except for the process with the given PID.
func killMatchingProcesses(pattern, excludePID string) {
	out, err := exec.Command("pgrep", "-f", pattern).Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == excludePID {
			continue
		}
		var pid int
		if _, err := fmt.Sscanf(line, "%d", &pid); err != nil || pid <= 0 {
			continue
		}
		if proc, err := os.FindProcess(pid); err == nil {
			proc.Signal(syscall.SIGTERM)
		}
	}
}

// envOrDefault returns the environment variable value if set, otherwise the fallback.
func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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

// writeInstanceInfo writes the instance metadata to a JSON file alongside the PID file.
func writeInstanceInfo(path string, info *instanceInfo) {
	data, err := json.Marshal(info)
	if err != nil {
		log.Printf("[serve] write instance info: %v", err)
		return
	}
	os.WriteFile(path, data, 0644)
}

// readInstanceInfo reads the instance metadata from the JSON info file.
func readInstanceInfo(path string) (*instanceInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var info instanceInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// readPIDFromFile reads just the PID integer from a PID file. Returns 0 on error.
func readPIDFromFile(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var pid int
	fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid)
	return pid
}

// isInstanceHealthy checks if an existing orchestra TCP server is responding.
func isInstanceHealthy(tcpAddr string) bool {
	conn, err := net.DialTimeout("tcp", tcpAddr, 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// isTerminal returns true if the given file is connected to a terminal (not a pipe).
func isTerminal(f *os.File) bool {
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

// runAttachedMode connects to an already-running orchestra instance from an
// interactive terminal. Shows the web-gate token (if applicable) and blocks
// until the user presses ESC or sends a signal.
func runAttachedMode(info *instanceInfo, _ string, webGateKey string) {
	// If the existing instance has a web-gate, show the token.
	if info.WebGateAddr != "" {
		gateAddr := info.WebGateAddr
		if strings.HasPrefix(gateAddr, ":") {
			gateAddr = "localhost" + gateAddr
		}

		// Generate a registration token from the running web-gate.
		raw, regToken, err := generateTokenFromGate(gateAddr, webGateKey)
		if err == nil {
			fmt.Fprint(os.Stderr, inprocess.FormatTokenDisplay(raw, regToken))
		} else {
			fmt.Fprintf(os.Stderr, "\norchestra: connected to existing instance (pid %d)\n", info.PID)
			fmt.Fprintf(os.Stderr, "  Web gate: %s\n\n", gateAddr)
		}
	} else {
		fmt.Fprintf(os.Stderr, "orchestra: connected to existing instance (pid %d)\n", info.PID)
		fmt.Fprintf(os.Stderr, "  Note: existing instance has no web-gate. Restart with --web-gate to enable.\n")
	}

	// Block until ESC or signal — don't start a new server.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	WatchEscQuit(func() {
		sigCh <- syscall.SIGTERM
	})
	<-sigCh
}

// runProxyMode bridges stdin/stdout (JSON-RPC) to an existing orchestra
// instance via TCP. The StdioTransport handles JSON-RPC ↔ Protobuf conversion;
// TCPSender forwards the Protobuf requests to the remote instance. When stdin
// closes (IDE disconnects), the proxy exits but the server stays running.
func runProxyMode(info *instanceInfo) {
	sender, err := inprocess.NewTCPSender(info.TCPAddr)
	if err != nil {
		fatal("connect to instance at %s: %v", info.TCPAddr, err)
	}
	defer sender.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals so proxy exits cleanly.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	transport := transportstdio.NewTransport(sender, os.Stdin, os.Stdout,
		transportstdio.WithOnDisconnect(func(sessionID string) {
			// Session locks live on the remote instance. The remote TCP server
			// tracks session IDs and releases locks when this connection closes.
		}),
	)
	if err := transport.Run(ctx); err != nil {
		if ctx.Err() == nil {
			log.Printf("[proxy] transport ended: %v", err)
		}
	}
}

// generateTokenFromGate asks the running web-gate to generate a registration
// token via POST /generate-token. This ensures the token is registered in the
// gate's own TunnelTokenManager so that POST /register will recognise it.
func generateTokenFromGate(gateAddr, apiKey string) (string, *inprocess.TunnelToken, error) {
	url := fmt.Sprintf("http://%s/generate-token", gateAddr)
	resp, err := http.Post(url, "application/json", nil)
	if err != nil {
		return "", nil, fmt.Errorf("call gate /generate-token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("gate returned status %d", resp.StatusCode)
	}

	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", nil, fmt.Errorf("decode response: %w", err)
	}

	token, err := inprocess.DecodeToken(result.Token)
	if err != nil {
		return "", nil, err
	}
	return result.Token, token, nil
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

// workspaceTCPPort derives a deterministic TCP port from the workspace path.
// Uses a hash of the absolute path mapped to the range 50101–50999, giving
// each workspace a stable port that won't collide with other workspaces.
func workspaceTCPPort(absWorkspace string) int {
	h := fnv.New32a()
	h.Write([]byte(absWorkspace))
	return 50101 + int(h.Sum32()%899) // range: 50101–50999
}

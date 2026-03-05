package internal

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RunRestart handles `orchestra restart [--workspace . --tcp-addr localhost:50101]`.
// It stops the running instance, then spawns a new `orchestra serve` as a
// detached process. The command returns immediately after the new process starts.
func RunRestart(args []string) {
	fs := flag.NewFlagSet("restart", flag.ExitOnError)
	workspace := fs.String("workspace", ".", "Project workspace directory")
	tcpAddr := fs.String("tcp-addr", "localhost:50101", "TCP address for desktop app connections")
	logPath := fs.String("log", "", "Log file path")
	noPlugins := fs.Bool("no-plugins", false, "Skip loading external plugins")
	fs.Parse(args)

	absWorkspace, err := filepath.Abs(*workspace)
	if err != nil {
		fatal("resolve workspace: %v", err)
	}

	pidFile := filepath.Join(absWorkspace, ".orchestra.pid")

	// ── Phase 1: Stop (if running) ──────────────────────────────────────
	pid, err := readPIDFile(pidFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Orchestra is not running, starting fresh...\n")
	} else {
		fmt.Fprintf(os.Stderr, "Stopping orchestra (pid %d)...\n", pid)
		if err := stopProcess(pid, 10*time.Second); err != nil {
			fatal("stop failed: %v", err)
		}
		if _, err := os.Stat(pidFile); err == nil {
			os.Remove(pidFile)
		}
		fmt.Fprintf(os.Stderr, "Orchestra stopped.\n")
	}

	// ── Phase 2: Start new process ──────────────────────────────────────
	fmt.Fprintf(os.Stderr, "Starting orchestra...\n")

	self, err := os.Executable()
	if err != nil {
		fatal("resolve executable: %v", err)
	}
	self, _ = filepath.EvalSymlinks(self)

	serveArgs := []string{
		"serve",
		"--workspace", absWorkspace,
		"--tcp-addr", *tcpAddr,
	}
	if *logPath != "" {
		serveArgs = append(serveArgs, "--log", *logPath)
	}
	if *noPlugins {
		serveArgs = append(serveArgs, "--no-plugins")
	}

	cmd := exec.Command(self, serveArgs...)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		fatal("start orchestra serve: %v", err)
	}

	newPID := cmd.Process.Pid

	// Wait briefly to catch immediate crashes.
	time.Sleep(500 * time.Millisecond)

	// Check if PID file was written (confirms successful startup).
	if data, err := os.ReadFile(pidFile); err == nil {
		fmt.Fprintf(os.Stderr, "Orchestra started (pid %s).\n",
			strings.TrimSpace(string(data)))
	} else {
		fmt.Fprintf(os.Stderr, "Orchestra started (pid %d).\n", newPID)
	}

	// Release — we don't wait for the serve process.
	cmd.Process.Release()
}

package internal

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// readPIDFile reads the PID from a .orchestra.pid file and validates the
// process is alive. Returns (pid, nil) if the process is running, or an
// error describing why it could not be read or is not alive.
func readPIDFile(pidFile string) (int, error) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, fmt.Errorf("no .orchestra.pid file found (orchestra is not running in this workspace)")
		}
		return 0, fmt.Errorf("read PID file: %w", err)
	}

	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); err != nil || pid <= 0 {
		return 0, fmt.Errorf("invalid PID in %s", pidFile)
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return 0, fmt.Errorf("process %d not found", pid)
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		os.Remove(pidFile)
		return 0, fmt.Errorf("stale PID file (process %d is not running) — removed", pid)
	}

	return pid, nil
}

// stopProcess sends SIGTERM to the given PID, waits up to timeout for it to
// exit, then sends SIGKILL if still alive.
func stopProcess(pid int, timeout time.Duration) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}

	// Send graceful shutdown signal.
	if runtime.GOOS == "windows" {
		if err := proc.Signal(os.Interrupt); err != nil {
			return fmt.Errorf("send interrupt to %d: %w", pid, err)
		}
	} else {
		if err := proc.Signal(syscall.SIGTERM); err != nil {
			return fmt.Errorf("send SIGTERM to %d: %w", pid, err)
		}
	}

	// Poll for process exit.
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if proc.Signal(syscall.Signal(0)) != nil {
			return nil // process exited
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Still alive — escalate.
	fmt.Fprintf(os.Stderr, "Process %d did not exit after %s, sending SIGKILL...\n", pid, timeout)
	if err := proc.Kill(); err != nil {
		if proc.Signal(syscall.Signal(0)) != nil {
			return nil // died between check and kill
		}
		return fmt.Errorf("kill process %d: %w", pid, err)
	}
	time.Sleep(500 * time.Millisecond)
	return nil
}

// RunStop handles `orchestra stop [--workspace .]`.
func RunStop(args []string) {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	workspace := fs.String("workspace", ".", "Project workspace directory")
	fs.Parse(args)

	absWorkspace, err := filepath.Abs(*workspace)
	if err != nil {
		fatal("resolve workspace: %v", err)
	}

	pidFile := filepath.Join(absWorkspace, ".orchestra.pid")
	pid, err := readPIDFile(pidFile)
	if err != nil {
		fatal("%v", err)
	}

	fmt.Fprintf(os.Stderr, "Stopping orchestra (pid %d)...\n", pid)

	if err := stopProcess(pid, 10*time.Second); err != nil {
		fatal("stop failed: %v", err)
	}

	// Clean up PID file if defer in serve didn't run (e.g., SIGKILL).
	if _, err := os.Stat(pidFile); err == nil {
		os.Remove(pidFile)
	}

	fmt.Fprintf(os.Stderr, "Orchestra stopped.\n")
}

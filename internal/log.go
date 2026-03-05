package internal

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
)

func RunLog(args []string) {
	fs := flag.NewFlagSet("log", flag.ExitOnError)
	lines := fs.Int("n", 50, "Number of lines to show (0 = all)")
	follow := fs.Bool("f", true, "Follow log output (tail -f)")
	workspace := fs.String("workspace", "", "Show log for specific workspace")
	fs.Parse(args)

	logDir := orchestraLogDir()

	var logFile string
	if *workspace != "" {
		// Specific workspace requested.
		absWs, err := filepath.Abs(*workspace)
		if err != nil {
			fatal("resolve workspace: %v", err)
		}
		logFile = filepath.Join(logDir, workspaceSlug(absWs)+".log")
	} else {
		// Find the most recently modified .log file in the log directory.
		logFile = findLatestLog(logDir)
	}

	if logFile == "" {
		fmt.Fprintf(os.Stderr, "orchestra: no log files found in %s\n", logDir)
		os.Exit(1)
	}

	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "orchestra: log file not found: %s\n", logFile)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "orchestra: %s\n", logFile)

	if runtime.GOOS == "windows" {
		// Windows: use PowerShell Get-Content -Tail -Wait.
		tailArgs := []string{"-Command", fmt.Sprintf("Get-Content '%s' -Tail %d", logFile, *lines)}
		if *follow {
			tailArgs = []string{"-Command", fmt.Sprintf("Get-Content '%s' -Tail %d -Wait", logFile, *lines)}
		}
		cmd := exec.Command("powershell", tailArgs...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Run()
	} else {
		// macOS / Linux: use tail.
		tailArgs := []string{fmt.Sprintf("-n%d", *lines)}
		if *follow {
			tailArgs = append(tailArgs, "-f")
		}
		tailArgs = append(tailArgs, logFile)
		cmd := exec.Command("tail", tailArgs...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Run()
	}
}

// findLatestLog returns the path to the most recently modified .log file in dir.
func findLatestLog(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	type logEntry struct {
		path    string
		modTime int64
	}

	var logs []logEntry
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".log" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		logs = append(logs, logEntry{
			path:    filepath.Join(dir, e.Name()),
			modTime: info.ModTime().UnixNano(),
		})
	}

	if len(logs) == 0 {
		return ""
	}

	sort.Slice(logs, func(i, j int) bool {
		return logs[i].modTime > logs[j].modTime
	})

	return logs[0].path
}

package internal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// detectProjectName tries to determine the project name from common config files.
func detectProjectName(root string) string {
	// 1. package.json
	if data, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		var pkg struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(data, &pkg) == nil && pkg.Name != "" {
			return pkg.Name
		}
	}

	// 2. go.mod — extract module name.
	if data, err := os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
		re := regexp.MustCompile(`module\s+(\S+)`)
		if m := re.FindSubmatch(data); len(m) > 1 {
			// Use last path segment as project name.
			parts := strings.Split(string(m[1]), "/")
			return parts[len(parts)-1]
		}
	}

	// 3. Cargo.toml — extract package name.
	if data, err := os.ReadFile(filepath.Join(root, "Cargo.toml")); err == nil {
		re := regexp.MustCompile(`name\s*=\s*"([^"]+)"`)
		if m := re.FindSubmatch(data); len(m) > 1 {
			return string(m[1])
		}
	}

	// 4. pyproject.toml
	if data, err := os.ReadFile(filepath.Join(root, "pyproject.toml")); err == nil {
		re := regexp.MustCompile(`name\s*=\s*"([^"]+)"`)
		if m := re.FindSubmatch(data); len(m) > 1 {
			return string(m[1])
		}
	}

	// 5. Fallback to directory name.
	return filepath.Base(root)
}

// detectIDEs checks for existing IDE configuration directories and returns
// matching IDE names. Falls back to ["claude"] if none detected.
func detectIDEs(workspace string) []string {
	var detected []string

	checks := []struct {
		name string
		path string // relative to workspace
	}{
		{"claude", ".mcp.json"},
		{"claude", ".claude"},
		{"cursor", ".cursor"},
		{"vscode", ".vscode"},
		{"zed", ".zed"},
		{"continue", ".continue"},
		{"codex", ".codex"},
		{"gemini", ".gemini"},
	}

	seen := make(map[string]bool)
	for _, c := range checks {
		if seen[c.name] {
			continue
		}
		checkPath := filepath.Join(workspace, c.path)
		if _, err := os.Stat(checkPath); err == nil {
			detected = append(detected, c.name)
			seen[c.name] = true
		}
	}

	// Check Windsurf (global config in home dir).
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(filepath.Join(home, ".codeium")); err == nil {
		detected = append(detected, "windsurf")
	}

	if len(detected) == 0 {
		detected = []string{"claude"}
	}

	return detected
}

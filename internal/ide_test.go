package internal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestClaudeDesktopConfigRegistered(t *testing.T) {
	cfg, ok := ideRegistry["claude-desktop"]
	if !ok {
		t.Fatal("claude-desktop not registered in ideRegistry")
	}
	if cfg.Display != "Claude Desktop" {
		t.Errorf("display: got %q, want %q", cfg.Display, "Claude Desktop")
	}
}

func TestClaudeDesktopInAllIDENames(t *testing.T) {
	names := allIDENames()
	found := false
	for _, n := range names {
		if n == "claude-desktop" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("claude-desktop not in allIDENames: %v", names)
	}
}

func TestClaudeDesktopConfigPath(t *testing.T) {
	path := claudeDesktopConfigPath()

	switch runtime.GOOS {
	case "darwin":
		if !strings.Contains(path, "Library/Application Support/Claude/claude_desktop_config.json") {
			t.Errorf("macOS path unexpected: %s", path)
		}
	case "windows":
		if !strings.Contains(path, "Claude") || !strings.HasSuffix(path, "claude_desktop_config.json") {
			t.Errorf("Windows path unexpected: %s", path)
		}
	default:
		if !strings.Contains(path, ".config/Claude/claude_desktop_config.json") {
			t.Errorf("Linux path unexpected: %s", path)
		}
	}
}

func TestClaudeDesktopConfigGenerate(t *testing.T) {
	cfg := ideRegistry["claude-desktop"]
	ws := t.TempDir()
	bin := "/usr/local/bin/orchestra"

	data, err := cfg.Generate(ws, bin)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("parse config: %v", err)
	}

	servers, ok := config["mcpServers"].(map[string]any)
	if !ok {
		t.Fatal("mcpServers not found in config")
	}
	orch, ok := servers["orchestra"].(map[string]any)
	if !ok {
		t.Fatal("orchestra not found in mcpServers")
	}
	if orch["command"] != bin {
		t.Errorf("command: got %v, want %v", orch["command"], bin)
	}
	args, ok := orch["args"].([]any)
	if !ok {
		t.Fatalf("args should be array, got %T", orch["args"])
	}
	if len(args) != 3 || args[0] != "serve" || args[1] != "--workspace" || args[2] != ws {
		t.Errorf("args: got %v", args)
	}
}

func TestClaudeDesktopConfigPreservesExisting(t *testing.T) {
	// Create a temp dir and write a fake existing config.
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, "Claude")
	os.MkdirAll(configDir, 0755)
	configPath := filepath.Join(configDir, "claude_desktop_config.json")

	existing := `{
  "mcpServers": {
    "other-server": {
      "command": "/usr/bin/other",
      "args": ["start"]
    }
  }
}
`
	os.WriteFile(configPath, []byte(existing), 0644)

	// Use mergeJSONMcpConfig directly since the config path is customized.
	bin := "/usr/local/bin/orchestra"
	ws := "/home/user/project"
	data, err := mergeJSONMcpConfig(configPath, "orchestra", orchestraServer(bin, ws))
	if err != nil {
		t.Fatalf("mergeJSONMcpConfig: %v", err)
	}

	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("parse config: %v", err)
	}

	servers := config["mcpServers"].(map[string]any)

	// Existing server should be preserved.
	if _, ok := servers["other-server"]; !ok {
		t.Error("existing other-server should be preserved")
	}
	// Orchestra should be added.
	if _, ok := servers["orchestra"]; !ok {
		t.Error("orchestra should be added")
	}
}

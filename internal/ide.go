package internal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// IDEConfig defines how to generate MCP config for a specific IDE.
type IDEConfig struct {
	Name       string
	Display    string
	ConfigPath func(workspace string) string
	Generate   func(workspace, binaryPath string) ([]byte, error)
}

// ideRegistry maps IDE names to their config generators.
var ideRegistry = map[string]*IDEConfig{
	"claude":   claudeConfig(),
	"cursor":   cursorConfig(),
	"vscode":   vscodeConfig(),
	"cline":    clineConfig(),
	"windsurf": windsurfConfig(),
	"codex":    codexConfig(),
	"gemini":   geminiConfig(),
	"zed":      zedConfig(),
	"continue": continueConfig(),
}

func allIDENames() []string {
	return []string{"claude", "cursor", "vscode", "cline", "windsurf", "codex", "gemini", "zed", "continue"}
}

// orchestraServer returns the standard server config map for MCP JSON configs.
func orchestraServer(binaryPath, workspace string) map[string]any {
	return map[string]any{
		"command": binaryPath,
		"args":    []string{"serve", "--workspace", workspace},
	}
}

// mergeJSONMcpConfig reads an existing JSON file, merges the orchestra server into
// mcpServers, and returns the updated JSON. Preserves other servers.
func mergeJSONMcpConfig(existingPath string, serverKey string, serverConfig map[string]any) ([]byte, error) {
	config := make(map[string]any)

	// Read existing file if it exists.
	if data, err := os.ReadFile(existingPath); err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &config); err != nil {
			// Existing file is invalid JSON — start fresh but warn.
			config = make(map[string]any)
		}
	}

	// Get or create the servers map.
	serversKey := "mcpServers"
	servers, ok := config[serversKey].(map[string]any)
	if !ok {
		servers = make(map[string]any)
	}

	// Set/update orchestra entry.
	servers[serverKey] = serverConfig
	config[serversKey] = servers

	result, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, err
	}
	// Trailing newline.
	result = append(result, '\n')
	return result, nil
}

// --- Claude Code ---

func claudeConfig() *IDEConfig {
	return &IDEConfig{
		Name:    "claude",
		Display: "Claude Code",
		ConfigPath: func(ws string) string {
			return filepath.Join(ws, ".mcp.json")
		},
		Generate: func(ws, bin string) ([]byte, error) {
			path := filepath.Join(ws, ".mcp.json")
			return mergeJSONMcpConfig(path, "orchestra", orchestraServer(bin, ws))
		},
	}
}

// --- Cursor ---

func cursorConfig() *IDEConfig {
	return &IDEConfig{
		Name:    "cursor",
		Display: "Cursor",
		ConfigPath: func(ws string) string {
			return filepath.Join(ws, ".cursor", "mcp.json")
		},
		Generate: func(ws, bin string) ([]byte, error) {
			path := filepath.Join(ws, ".cursor", "mcp.json")
			return mergeJSONMcpConfig(path, "orchestra", orchestraServer(bin, ws))
		},
	}
}

// --- VS Code / GitHub Copilot ---

func vscodeConfig() *IDEConfig {
	return &IDEConfig{
		Name:    "vscode",
		Display: "VS Code / Copilot",
		ConfigPath: func(ws string) string {
			return filepath.Join(ws, ".vscode", "mcp.json")
		},
		Generate: func(ws, bin string) ([]byte, error) {
			path := filepath.Join(ws, ".vscode", "mcp.json")
			return mergeJSONMcpConfig(path, "orchestra", orchestraServer(bin, ws))
		},
	}
}

// --- Cline (uses VS Code config) ---

func clineConfig() *IDEConfig {
	return &IDEConfig{
		Name:    "cline",
		Display: "Cline",
		ConfigPath: func(ws string) string {
			return filepath.Join(ws, ".vscode", "mcp.json")
		},
		Generate: func(ws, bin string) ([]byte, error) {
			path := filepath.Join(ws, ".vscode", "mcp.json")
			return mergeJSONMcpConfig(path, "orchestra", orchestraServer(bin, ws))
		},
	}
}

// --- Windsurf (global config) ---

func windsurfConfig() *IDEConfig {
	return &IDEConfig{
		Name:    "windsurf",
		Display: "Windsurf",
		ConfigPath: func(ws string) string {
			home, _ := os.UserHomeDir()
			return filepath.Join(home, ".codeium", "windsurf", "mcp_config.json")
		},
		Generate: func(ws, bin string) ([]byte, error) {
			home, _ := os.UserHomeDir()
			path := filepath.Join(home, ".codeium", "windsurf", "mcp_config.json")
			return mergeJSONMcpConfig(path, "orchestra", orchestraServer(bin, ws))
		},
	}
}

// --- Codex (OpenAI) — TOML format ---

func codexConfig() *IDEConfig {
	return &IDEConfig{
		Name:    "codex",
		Display: "Codex (OpenAI)",
		ConfigPath: func(ws string) string {
			return filepath.Join(ws, ".codex", "config.toml")
		},
		Generate: func(ws, bin string) ([]byte, error) {
			// Simple TOML generation via template (no toml library needed).
			toml := fmt.Sprintf(`[mcp_servers.orchestra]
command = %q
args = ["serve", "--workspace", %q]
`, bin, ws)
			return []byte(toml), nil
		},
	}
}

// --- Gemini Code Assist ---

func geminiConfig() *IDEConfig {
	return &IDEConfig{
		Name:    "gemini",
		Display: "Gemini Code Assist",
		ConfigPath: func(ws string) string {
			return filepath.Join(ws, ".gemini", "settings.json")
		},
		Generate: func(ws, bin string) ([]byte, error) {
			path := filepath.Join(ws, ".gemini", "settings.json")
			return mergeJSONMcpConfig(path, "orchestra", orchestraServer(bin, ws))
		},
	}
}

// --- Zed (different JSON structure) ---

func zedConfig() *IDEConfig {
	return &IDEConfig{
		Name:    "zed",
		Display: "Zed",
		ConfigPath: func(ws string) string {
			return filepath.Join(ws, ".zed", "settings.json")
		},
		Generate: func(ws, bin string) ([]byte, error) {
			path := filepath.Join(ws, ".zed", "settings.json")

			config := make(map[string]any)
			if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
				json.Unmarshal(data, &config)
			}

			servers, ok := config["context_servers"].(map[string]any)
			if !ok {
				servers = make(map[string]any)
			}

			servers["orchestra"] = map[string]any{
				"command": map[string]any{
					"path": bin,
					"args": []string{"serve", "--workspace", ws},
				},
			}
			config["context_servers"] = servers

			result, err := json.MarshalIndent(config, "", "  ")
			if err != nil {
				return nil, err
			}
			// Trailing newline.
			result = append(result, '\n')
			return result, nil
		},
	}
}

// --- Continue.dev (YAML format) ---

func continueConfig() *IDEConfig {
	return &IDEConfig{
		Name:    "continue",
		Display: "Continue.dev",
		ConfigPath: func(ws string) string {
			return filepath.Join(ws, ".continue", "mcpServers", "orchestra.yaml")
		},
		Generate: func(ws, bin string) ([]byte, error) {
			yaml := fmt.Sprintf(`name: orchestra
command: %s
args:
  - serve
  - --workspace
  - %s
`, bin, ws)
			return []byte(yaml), nil
		},
	}
}

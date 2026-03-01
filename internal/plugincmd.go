package internal

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// RunPlugin handles `orchestra plugin <subcommand>`.
func RunPlugin(args []string) {
	if len(args) < 1 {
		printPluginUsage()
		return
	}

	switch args[0] {
	case "install":
		runPluginInstall(args[1:])
	case "remove", "uninstall":
		RunUninstall(args[1:])
	case "list", "ls":
		runPluginList(args[1:])
	case "enable":
		runPluginEnable(args[1:])
	case "disable":
		runPluginDisable(args[1:])
	case "search":
		runPluginSearch(args[1:])
	case "update":
		runPluginUpdate(args[1:])
	case "info":
		runPluginInfo(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown plugin subcommand: %s\n\n", args[0])
		printPluginUsage()
		os.Exit(1)
	}
}

func printPluginUsage() {
	fmt.Fprintf(os.Stderr, `orchestra plugin — manage external plugins

Usage:
  orchestra plugin install <repo>[@version]   Install a plugin binary
  orchestra plugin remove <name>              Remove an installed plugin
  orchestra plugin list                       List installed plugins
  orchestra plugin enable <name>              Enable a disabled plugin
  orchestra plugin disable <name>             Disable a plugin (keep binary)
  orchestra plugin search <query>             Search available plugins
  orchestra plugin update [name]              Update one or all plugins
  orchestra plugin info <name>                Show plugin details

Examples:
  orchestra plugin install github.com/orchestra-mcp/plugin-devtools-git
  orchestra plugin install github.com/orchestra-mcp/plugin-bridge-openai@v0.1.0
  orchestra plugin disable bridge.openai
  orchestra plugin enable bridge.openai
  orchestra plugin search devtools
`)
}

// runPluginInstall delegates to the existing RunInstall.
func runPluginInstall(args []string) {
	RunInstall(args)
}

// runPluginList shows all installed plugins with enabled/disabled status.
func runPluginList(_ []string) {
	reg, err := LoadRegistry()
	if err != nil {
		fatal("load registry: %v", err)
	}

	if len(reg.Plugins) == 0 {
		fmt.Fprintf(os.Stderr, "No plugins installed.\n")
		fmt.Fprintf(os.Stderr, "  Install with: orchestra plugin install <repo>\n")
		return
	}

	fmt.Fprintf(os.Stderr, "Installed plugins:\n\n")
	fmt.Fprintf(os.Stderr, "  %-28s %-10s %-10s %s\n", "ID", "VERSION", "STATUS", "TOOLS")
	fmt.Fprintf(os.Stderr, "  %-28s %-10s %-10s %s\n",
		strings.Repeat("─", 28), strings.Repeat("─", 10),
		strings.Repeat("─", 10), strings.Repeat("─", 5))

	for _, p := range reg.Plugins {
		status := "enabled"
		if !p.Enabled {
			status = "disabled"
		}
		fmt.Fprintf(os.Stderr, "  %-28s %-10s %-10s %d\n",
			p.ID, p.Version, status, len(p.ProvidesTools))
	}
}

// runPluginEnable enables a disabled plugin.
func runPluginEnable(args []string) {
	if len(args) < 1 {
		fatal("usage: orchestra plugin enable <name>")
	}
	target := args[0]

	reg, err := LoadRegistry()
	if err != nil {
		fatal("load registry: %v", err)
	}

	entry := findPlugin(reg, target)
	if entry == nil {
		fatal("plugin not found: %s", target)
	}

	if entry.Enabled {
		fmt.Fprintf(os.Stderr, "%s is already enabled.\n", entry.ID)
		return
	}

	entry.Enabled = true
	entry.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := SaveRegistry(reg); err != nil {
		fatal("save registry: %v", err)
	}
	fmt.Fprintf(os.Stderr, "Enabled %s. Restart orchestra to activate.\n", entry.ID)
}

// runPluginDisable disables a plugin without removing it.
func runPluginDisable(args []string) {
	if len(args) < 1 {
		fatal("usage: orchestra plugin disable <name>")
	}
	target := args[0]

	reg, err := LoadRegistry()
	if err != nil {
		fatal("load registry: %v", err)
	}

	entry := findPlugin(reg, target)
	if entry == nil {
		fatal("plugin not found: %s", target)
	}

	if !entry.Enabled {
		fmt.Fprintf(os.Stderr, "%s is already disabled.\n", entry.ID)
		return
	}

	entry.Enabled = false
	entry.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := SaveRegistry(reg); err != nil {
		fatal("save registry: %v", err)
	}
	fmt.Fprintf(os.Stderr, "Disabled %s. Restart orchestra to deactivate.\n", entry.ID)
}

// runPluginSearch searches the known plugin catalog.
func runPluginSearch(args []string) {
	if len(args) < 1 {
		fatal("usage: orchestra plugin search <query>")
	}
	query := strings.ToLower(strings.Join(args, " "))

	type catalogEntry struct {
		Repo  string
		ID    string
		Desc  string
		Tools int
		Tags  []string
	}

	catalog := []catalogEntry{
		// Core tools (storage-dependent)
		{"github.com/orchestra-mcp/plugin-tools-notes", "tools.notes", "Note-taking tools", 8, []string{"notes", "docs"}},
		{"github.com/orchestra-mcp/plugin-tools-docs", "tools.docs", "Documentation tools", 10, []string{"docs", "documentation"}},
		{"github.com/orchestra-mcp/plugin-tools-sessions", "tools.sessions", "AI session management", 6, []string{"sessions", "ai"}},
		{"github.com/orchestra-mcp/plugin-agent-orchestrator", "agent.orchestrator", "Multi-agent orchestrator", 20, []string{"agents", "workflow", "orchestrator"}},
		{"github.com/orchestra-mcp/plugin-tools-agentops", "tools.agentops", "Agent operations & accounts", 8, []string{"agents", "accounts"}},
		{"github.com/orchestra-mcp/plugin-tools-workspace", "tools.workspace", "Workspace management", 8, []string{"workspace"}},
		{"github.com/orchestra-mcp/plugin-tools-markdown", "tools.markdown", "Markdown processing", 8, []string{"markdown"}},
		{"github.com/orchestra-mcp/plugin-tools-extension-generator", "tools.extension-generator", "Extension scaffolding", 8, []string{"extensions"}},
		// AI Bridges
		{"github.com/orchestra-mcp/plugin-bridge-claude", "bridge.claude", "Claude Code bridge", 5, []string{"ai", "claude"}},
		{"github.com/orchestra-mcp/plugin-bridge-openai", "bridge.openai", "OpenAI-compatible bridge", 5, []string{"ai", "openai", "grok", "deepseek"}},
		{"github.com/orchestra-mcp/plugin-bridge-gemini", "bridge.gemini", "Google Gemini bridge", 5, []string{"ai", "gemini"}},
		{"github.com/orchestra-mcp/plugin-bridge-ollama", "bridge.ollama", "Ollama local model bridge", 5, []string{"ai", "ollama", "local"}},
		{"github.com/orchestra-mcp/plugin-bridge-firecrawl", "bridge.firecrawl", "Firecrawl web scraping bridge", 5, []string{"web", "scraping"}},
		// DevTools
		{"github.com/orchestra-mcp/plugin-devtools-git", "devtools.git", "Git & GitHub integration", 20, []string{"git", "github", "devtools"}},
		{"github.com/orchestra-mcp/plugin-devtools-docker", "devtools.docker", "Docker container management", 10, []string{"docker", "devtools"}},
		{"github.com/orchestra-mcp/plugin-devtools-terminal", "devtools.terminal", "Terminal emulation", 6, []string{"terminal", "devtools"}},
		{"github.com/orchestra-mcp/plugin-devtools-ssh", "devtools.ssh", "SSH remote access", 7, []string{"ssh", "devtools"}},
		{"github.com/orchestra-mcp/plugin-devtools-database", "devtools.database", "Database management", 8, []string{"database", "sql", "devtools"}},
		{"github.com/orchestra-mcp/plugin-devtools-debugger", "devtools.debugger", "Debugger integration", 9, []string{"debugger", "devtools"}},
		{"github.com/orchestra-mcp/plugin-devtools-test-runner", "devtools.test-runner", "Test runner integration", 8, []string{"testing", "devtools"}},
		{"github.com/orchestra-mcp/plugin-devtools-file-explorer", "devtools.file-explorer", "File explorer", 17, []string{"files", "devtools"}},
		{"github.com/orchestra-mcp/plugin-devtools-components", "devtools.components", "Component inspector", 6, []string{"components", "devtools"}},
		{"github.com/orchestra-mcp/plugin-devtools-devops", "devtools.devops", "DevOps pipeline tools", 8, []string{"devops", "ci", "devtools"}},
		{"github.com/orchestra-mcp/plugin-devtools-services", "devtools.services", "Service management", 6, []string{"services", "devtools"}},
		{"github.com/orchestra-mcp/plugin-devtools-log-viewer", "devtools.log-viewer", "Log viewer", 5, []string{"logs", "devtools"}},
		// AI Awareness
		{"github.com/orchestra-mcp/plugin-ai-screenshot", "ai.screenshot", "Screenshot capture", 6, []string{"screenshot", "ai"}},
		{"github.com/orchestra-mcp/plugin-ai-vision", "ai.vision", "Vision analysis", 6, []string{"vision", "ai"}},
		{"github.com/orchestra-mcp/plugin-ai-browser-context", "ai.browser-context", "Browser context awareness", 7, []string{"browser", "ai"}},
		{"github.com/orchestra-mcp/plugin-ai-screen-reader", "ai.screen-reader", "Screen reader", 6, []string{"accessibility", "ai"}},
		// Integration
		{"github.com/orchestra-mcp/plugin-integration-figma", "integration.figma", "Figma design integration", 6, []string{"figma", "design"}},
		// Services
		{"github.com/orchestra-mcp/plugin-services-notifications", "services.notifications", "System notifications", 8, []string{"notifications"}},
		{"github.com/orchestra-mcp/plugin-services-voice", "services.voice", "Voice input/output", 8, []string{"voice", "tts", "stt"}},
		// Sync
		{"github.com/orchestra-mcp/plugin-sync-cloud", "sync.cloud", "Cloud sync to web dashboard", 5, []string{"sync", "cloud"}},
	}

	var matches []catalogEntry
	for _, p := range catalog {
		if strings.Contains(strings.ToLower(p.ID), query) ||
			strings.Contains(strings.ToLower(p.Desc), query) ||
			strings.Contains(strings.ToLower(p.Repo), query) {
			matches = append(matches, p)
			continue
		}
		for _, tag := range p.Tags {
			if strings.Contains(tag, query) {
				matches = append(matches, p)
				break
			}
		}
	}

	if len(matches) == 0 {
		fmt.Fprintf(os.Stderr, "No plugins found for: %s\n", query)
		return
	}

	fmt.Fprintf(os.Stderr, "Available plugins matching %q:\n\n", query)
	for _, p := range matches {
		fmt.Fprintf(os.Stderr, "  %-28s %s (%d tools)\n", p.ID, p.Desc, p.Tools)
		fmt.Fprintf(os.Stderr, "    %s\n\n", p.Repo)
	}
	fmt.Fprintf(os.Stderr, "Install with: orchestra plugin install <repo>\n")
}

// runPluginUpdate updates a specific plugin or all plugins.
func runPluginUpdate(args []string) {
	if len(args) > 0 {
		RunUpdate(args)
		return
	}

	// Update all plugins.
	reg, err := LoadRegistry()
	if err != nil {
		fatal("load registry: %v", err)
	}

	if len(reg.Plugins) == 0 {
		fmt.Fprintf(os.Stderr, "No plugins installed.\n")
		return
	}

	for _, entry := range reg.Plugins {
		fmt.Fprintf(os.Stderr, "Updating %s...\n", entry.ID)
		RunInstall([]string{entry.Repo})
	}
}

// runPluginInfo shows detailed information about a plugin.
func runPluginInfo(args []string) {
	if len(args) < 1 {
		fatal("usage: orchestra plugin info <name>")
	}
	target := args[0]

	reg, err := LoadRegistry()
	if err != nil {
		fatal("load registry: %v", err)
	}

	entry := findPlugin(reg, target)
	if entry == nil {
		fatal("plugin not found: %s", target)
	}

	fmt.Fprintf(os.Stderr, "Plugin: %s\n", entry.ID)
	fmt.Fprintf(os.Stderr, "  Version:     %s\n", entry.Version)
	fmt.Fprintf(os.Stderr, "  Repo:        %s\n", entry.Repo)
	fmt.Fprintf(os.Stderr, "  Binary:      %s\n", entry.Binary)
	fmt.Fprintf(os.Stderr, "  Enabled:     %v\n", entry.Enabled)
	fmt.Fprintf(os.Stderr, "  Platform:    %s\n", entry.Platform)
	fmt.Fprintf(os.Stderr, "  Installed:   %s\n", entry.InstalledAt)
	if entry.Description != "" {
		fmt.Fprintf(os.Stderr, "  Description: %s\n", entry.Description)
	}
	if entry.Author != "" {
		fmt.Fprintf(os.Stderr, "  Author:      %s\n", entry.Author)
	}
	if len(entry.ProvidesTools) > 0 {
		fmt.Fprintf(os.Stderr, "  Tools (%d):  %s\n",
			len(entry.ProvidesTools), strings.Join(entry.ProvidesTools, ", "))
	}
	if len(entry.ProvidesAI) > 0 {
		fmt.Fprintf(os.Stderr, "  AI:          %s\n", strings.Join(entry.ProvidesAI, ", "))
	}
	if len(entry.ProvidesPrompts) > 0 {
		fmt.Fprintf(os.Stderr, "  Prompts:     %s\n", strings.Join(entry.ProvidesPrompts, ", "))
	}
	if len(entry.NeedsStorage) > 0 {
		fmt.Fprintf(os.Stderr, "  Needs:       storage:%s\n", strings.Join(entry.NeedsStorage, ", "))
	}
	if len(entry.NeedsTools) > 0 {
		fmt.Fprintf(os.Stderr, "  Needs:       tools:%s\n", strings.Join(entry.NeedsTools, ", "))
	}
}

// findPlugin looks up a plugin by repo URL or plugin ID.
func findPlugin(reg *PluginRegistry, target string) *PluginEntry {
	if p, ok := reg.Plugins[target]; ok {
		return p
	}
	for _, p := range reg.Plugins {
		if p.ID == target {
			return p
		}
	}
	return nil
}

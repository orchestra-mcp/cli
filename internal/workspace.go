package internal

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// GenerateWorkspaceDocs creates or overwrites CLAUDE.md and AGENTS.md at the
// workspace root. It scans .claude/skills/, .claude/agents/, .claude/hooks/
// for installed content and reads the pack registry to produce accurate
// documentation files. Call this from orchestra init and after pack
// install/remove/update.
func GenerateWorkspaceDocs(workspace string) {
	// Ensure .claude/ directory exists.
	claudeDir := filepath.Join(workspace, ".claude")
	os.MkdirAll(claudeDir, 0755)

	// Scan installed content from the filesystem.
	skills := scanSkills(claudeDir)
	agents := scanAgents(claudeDir)
	hooks := scanHooks(claudeDir)

	// Load pack registry for the installed packs section.
	reg := loadPackRegistry(workspace)

	// Generate and write CLAUDE.md.
	claudeMD := buildClaudeMD(reg, skills, agents, hooks)
	claudeMDPath := filepath.Join(workspace, "CLAUDE.md")
	if err := os.WriteFile(claudeMDPath, []byte(claudeMD), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "  [FAIL] CLAUDE.md: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "  [OK] CLAUDE.md\n")
	}

	// Generate and write AGENTS.md.
	agentsMD := buildAgentsMD(agents)
	agentsMDPath := filepath.Join(workspace, "AGENTS.md")
	if err := os.WriteFile(agentsMDPath, []byte(agentsMD), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "  [FAIL] AGENTS.md: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "  [OK] AGENTS.md\n")
	}
}

// scanSkills returns sorted skill directory names found in .claude/skills/.
// Each skill is a directory containing at least a SKILL.md file.
func scanSkills(claudeDir string) []string {
	skillsDir := filepath.Join(claudeDir, "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil
	}

	var skills []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// Only include directories that contain a SKILL.md file.
		skillMD := filepath.Join(skillsDir, entry.Name(), "SKILL.md")
		if _, err := os.Stat(skillMD); err == nil {
			skills = append(skills, entry.Name())
		}
	}
	sort.Strings(skills)
	return skills
}

// scanAgents returns sorted agent names (without .md extension) found in
// .claude/agents/.
func scanAgents(claudeDir string) []string {
	agentsDir := filepath.Join(claudeDir, "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return nil
	}

	var agents []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".md") {
			agents = append(agents, strings.TrimSuffix(name, ".md"))
		}
	}
	sort.Strings(agents)
	return agents
}

// scanHooks returns sorted hook names (without .sh extension) found in
// .claude/hooks/.
func scanHooks(claudeDir string) []string {
	hooksDir := filepath.Join(claudeDir, "hooks")
	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		return nil
	}

	var hooks []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".sh") {
			hooks = append(hooks, strings.TrimSuffix(name, ".sh"))
		}
	}
	sort.Strings(hooks)
	return hooks
}

// buildClaudeMD generates the full CLAUDE.md content.
func buildClaudeMD(reg *packRegistry, skills, agents, hooks []string) string {
	var b strings.Builder

	b.WriteString("# CLAUDE.md\n\n")
	b.WriteString("This project uses [Orchestra MCP](https://github.com/orchestra-mcp/framework) for AI-powered project management.\n\n")

	// Available Tools section.
	b.WriteString("## Available Tools\n\n")
	b.WriteString("Orchestra provides **49 tools** via MCP (34 feature workflow + 15 marketplace) and **5 prompts**.\n\n")
	b.WriteString("Run `orchestra serve` to start the MCP server. IDE config is in `.mcp.json`.\n\n")

	// Installed Packs section.
	b.WriteString("## Installed Packs\n\n")
	if len(reg.Packs) == 0 {
		b.WriteString("No packs installed. Run `orchestra pack recommend` to get suggestions.\n\n")
	} else {
		packNames := sortedPackNames(reg)
		for _, name := range packNames {
			entry := reg.Packs[name]
			b.WriteString(fmt.Sprintf("- **%s** (v%s) — %d skills, %d agents, %d hooks\n",
				name, entry.Version,
				len(entry.Skills), len(entry.Agents), len(entry.Hooks)))
		}
		b.WriteString("\n")
	}

	// Skills section.
	b.WriteString("## Skills (Slash Commands)\n\n")
	if len(skills) == 0 {
		b.WriteString("No skills installed. Install a pack: `orchestra pack install github.com/orchestra-mcp/pack-essentials`\n\n")
	} else {
		b.WriteString("| Command | Source |\n")
		b.WriteString("|---------|--------|\n")
		for _, name := range skills {
			b.WriteString(fmt.Sprintf("| `/%s` | .claude/skills/%s/ |\n", name, name))
		}
		b.WriteString("\n")
	}

	// Agents section.
	b.WriteString("## Agents\n\n")
	if len(agents) == 0 {
		b.WriteString("No agents installed.\n\n")
	} else {
		b.WriteString("Specialized agents in `.claude/agents/` auto-delegate based on task context.\n\n")
		b.WriteString("| Agent | File |\n")
		b.WriteString("|-------|------|\n")
		for _, name := range agents {
			b.WriteString(fmt.Sprintf("| `%s` | .claude/agents/%s.md |\n", name, name))
		}
		b.WriteString("\n")
	}

	// Hooks section.
	b.WriteString("## Hooks\n\n")
	if len(hooks) == 0 {
		b.WriteString("No hooks installed.\n")
	} else {
		b.WriteString("| Hook | File |\n")
		b.WriteString("|------|------|\n")
		for _, name := range hooks {
			b.WriteString(fmt.Sprintf("| `%s` | .claude/hooks/%s.sh |\n", name, name))
		}
		b.WriteString("")
	}

	return b.String()
}

// buildAgentsMD generates the full AGENTS.md content.
func buildAgentsMD(agents []string) string {
	var b strings.Builder

	b.WriteString("# AGENTS.md\n\n")
	b.WriteString("Specialized agents installed via Orchestra packs. Each agent is a markdown file in `.claude/agents/` that provides domain-specific instructions.\n\n")

	if len(agents) == 0 {
		b.WriteString("No agents installed. Install a pack to add agents:\n")
		b.WriteString("```\n")
		b.WriteString("orchestra pack install github.com/orchestra-mcp/pack-essentials\n")
		b.WriteString("```\n")
	} else {
		for _, name := range agents {
			b.WriteString(fmt.Sprintf("## %s\n\n", name))
			b.WriteString(fmt.Sprintf("See [.claude/agents/%s.md](.claude/agents/%s.md)\n\n", name, name))
		}
	}

	return b.String()
}

// sortedPackNames returns pack names from the registry in alphabetical order.
func sortedPackNames(reg *packRegistry) []string {
	names := make([]string, 0, len(reg.Packs))
	for name := range reg.Packs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

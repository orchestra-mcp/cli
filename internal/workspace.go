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

	// Mandatory workflow rule.
	b.WriteString("## Mandatory Workflow Rule\n\n")
	b.WriteString("**ALL work MUST go through Orchestra MCP tools.** When the user asks you to do ANY task — build, fix, test, refactor, document, investigate, or change anything:\n\n")
	b.WriteString("1. `search_features` / `list_features` — check for existing feature\n")
	b.WriteString("2. `create_feature` — create one if needed (with `kind`: feature/bug/hotfix/chore)\n")
	b.WriteString("3. `set_current_feature` — start work (moves to in-progress)\n")
	b.WriteString("4. Do the work\n")
	b.WriteString("5. `advance_feature` — pass gates with structured evidence\n")
	b.WriteString("6. `request_review` + `AskUserQuestion` — get user approval\n")
	b.WriteString("7. `submit_review` — complete\n\n")
	b.WriteString("**Never do any work without an active feature.** This includes running tests, writing docs, investigating bugs, and refactoring. The MCP enforces gated transitions — you cannot advance without evidence.\n\n")

	// Feature kinds.
	b.WriteString("### Feature Kinds\n\n")
	b.WriteString("Every feature has a `kind` field: `feature` (default), `bug`, `hotfix`, or `chore`.\n\n")
	b.WriteString("- **feature** — New functionality or enhancement\n")
	b.WriteString("- **bug** — Defect report (Gate 3/docs skipped automatically)\n")
	b.WriteString("- **hotfix** — Urgent fix (Gate 3/docs skipped automatically)\n")
	b.WriteString("- **chore** — Maintenance, refactoring, CI work\n")
	b.WriteString("- **testcase** — QA test case linked to a parent feature (Gate 3/docs skipped automatically)\n\n")
	b.WriteString("Use `create_bug_report` as a shortcut for bugs. Use `create_test_case` or `bulk_create_test_cases` for QA test cases linked to a feature.\n\n")

	// Plan-first for large tasks.
	b.WriteString("### Plan-First for Large Tasks (MANDATORY)\n\n")
	b.WriteString("When a user request would result in **3 or more features**, you MUST create a plan before implementation:\n\n")
	b.WriteString("1. `create_plan` — Create the plan in `draft` status with title and description\n")
	b.WriteString("2. Present the plan to the user via `AskUserQuestion` for approval\n")
	b.WriteString("3. `approve_plan` — Move from draft → approved\n")
	b.WriteString("4. `breakdown_plan` — Break the plan into features with dependencies (pass a JSON array of feature definitions). This auto-creates all features with `plan:{plan_id}` labels and sets up dependency chains. Plan moves to `in-progress`.\n")
	b.WriteString("5. Work each feature through the full lifecycle (in order of dependencies)\n")
	b.WriteString("6. `complete_plan` — After all linked features are `done`, mark the plan as completed\n\n")
	b.WriteString("**Do NOT skip the plan step for large tasks.** The plan is stored via MCP and provides traceability.\n\n")

	// User request queue.
	b.WriteString("### User Request Queue\n\n")
	b.WriteString("When the user sends a new request while you are busy working on a feature:\n\n")
	b.WriteString("1. `create_request` — Save it to the queue with kind (feature/hotfix/bug) and priority\n")
	b.WriteString("2. Continue working on the current feature\n")
	b.WriteString("3. After the current feature reaches `done`, call `get_next_request` to pick up the next queued request\n")
	b.WriteString("4. `convert_request` — Convert it into a feature (auto-creates with correct kind/priority)\n")
	b.WriteString("5. Work the new feature through the full lifecycle\n\n")
	b.WriteString("Use `list_requests` to see the queue and `dismiss_request` to discard irrelevant requests.\n\n")

	// Bug reporting.
	b.WriteString("### Bug Reporting\n\n")
	b.WriteString("When a completed feature causes a regression or breakage:\n\n")
	b.WriteString("1. `create_bug_report` — Creates a feature with kind=bug, links to the original feature via `related_feature` param\n")
	b.WriteString("2. The bug follows the same workflow but **Gate 3 (docs) is auto-skipped** for bugs and hotfixes\n")
	b.WriteString("3. Work the bug through: backlog → todo → in-progress → testing → review → done\n\n")

	// Enforced gates.
	b.WriteString("### Enforced Gates (MCP validates evidence)\n\n")
	b.WriteString("The MCP **rejects** `advance_feature` if evidence is missing or malformed at gated transitions. Evidence must be markdown with `## Section` headers, each with at least 10 characters of content. **Sections marked with (files) must contain actual file paths** — not just prose.\n\n")
	b.WriteString("| Gate | Transition | Required Sections | Tool | Skippable |\n")
	b.WriteString("|------|-----------|-------------------|------|----------|\n")
	b.WriteString("| 1 | in-progress → ready-for-testing | `## Summary`, `## Changes` **(files)**, `## Verification` | `advance_feature` | No |\n")
	b.WriteString("| 2 | in-testing → ready-for-docs | `## Summary`, `## Results`, `## Coverage` | `advance_feature` | No |\n")
	b.WriteString("| 3 | in-docs → documented | `## Summary`, `## Location` **(files)** | `advance_feature` | **Yes** (bug, hotfix) |\n")
	b.WriteString("| 4 | documented → in-review | `## Summary`, `## Quality`, `## Checklist` **(files)** | `request_review` | No |\n")
	b.WriteString("| 5 | in-review → done | User approval via `AskUserQuestion` | `submit_review` | No |\n\n")
	b.WriteString("**Gate evidence format:**\n```\nevidence: \"## Summary\\n<what was done>\\n\\n## Changes\\n- libs/foo/bar.go (added validation)\\n- libs/baz/qux.go (new file)\\n\\n## Verification\\n<how to test>\"\n```\n\n")
	b.WriteString("Call `get_gate_requirements` to see what's needed for the next transition.\n\n")

	// Free transitions.
	b.WriteString("### Free Transitions (no gate)\n\n")
	b.WriteString("These transitions can be done without evidence:\n")
	b.WriteString("- backlog → todo, todo → in-progress, ready-for-testing → in-testing, ready-for-docs → in-docs, needs-edits → in-progress\n\n")

	// Review flow.
	b.WriteString("### Review Flow (Gate 4-5)\n\n")
	b.WriteString("1. Call `request_review` with self-review evidence (sections: `## Summary`, `## Quality`, `## Checklist`)\n")
	b.WriteString("2. MCP moves feature to `in-review` and instructs you to ask the user\n")
	b.WriteString("3. Use `AskUserQuestion` to present the review to the user with options: \"Approve\" / \"Needs Edits\"\n")
	b.WriteString("4. Call `submit_review` with the user's decision (`status: \"approved\"` or `status: \"needs-edits\"`)\n\n")
	b.WriteString("**Do NOT call `submit_review` without user approval.** `advance_feature` is blocked from `in-review` — you must use `submit_review`.\n\n")

	// Sub-agent rules.
	b.WriteString("### Sub-Agent Rules\n\n")
	b.WriteString("Sub-agents (Task tool) do **NOT** have MCP access. They cannot call `advance_feature` or any workflow tool.\n\n")
	b.WriteString("- Sub-agents = code only (use during in-progress for writing code)\n")
	b.WriteString("- Main agent owns lifecycle (YOU handle all gates: test, document, review)\n")
	b.WriteString("- One feature at a time per assignee (complete full lifecycle before picking next)\n")
	b.WriteString("- Summarize to user (tell user what sub-agent built before advancing)\n\n")

	// Anti-patterns.
	b.WriteString("### Anti-Patterns (NEVER DO)\n\n")
	b.WriteString("- Batch-advancing multiple features through gates in rapid succession\n")
	b.WriteString("- Writing fake/boilerplate evidence without doing actual work\n")
	b.WriteString("- Advancing through gates without providing evidence that references real file paths\n")
	b.WriteString("- Requesting review for one feature then starting another before review resolves\n")
	b.WriteString("- Calling `submit_review` without asking the user via `AskUserQuestion` first\n\n")

	// Programmatic guardrails.
	b.WriteString("### Programmatic Guardrails (MCP-Enforced)\n\n")
	b.WriteString("These rules are enforced at the MCP tool level — violation attempts return errors:\n\n")
	b.WriteString("1. **One feature at a time per assignee** — `set_current_feature` blocks if the same assignee already has an active feature (in-progress through in-review). Different assignees (parallel agents) can each work on their own feature. Returns `wip_violation` error.\n")
	b.WriteString("2. **Gate cooldown (30 seconds)** — Gated transitions require at least 30s since the last status change. Prevents instant batch-advancement. Returns `gate_cooldown` error.\n")
	b.WriteString("3. **File path evidence** — Gate 1 (Changes), Gate 3 (Location), and Gate 4 (Checklist) sections must reference actual file paths. Returns `gate_blocked` error.\n")
	b.WriteString("4. **Timestamped audit trail** — Every transition appends an ISO-8601 timestamp to the feature body for post-hoc review.\n")
	b.WriteString("5. **Model capability check** — `set_current_feature` accepts a `model` parameter. Validates the model can handle the feature's size estimate (Haiku→S, Sonnet→S/M, Opus→S/M/L/XL). Returns `model_capability` error.\n")
	b.WriteString("6. **Review requires user approval** — `advance_feature` is blocked from `in-review`. Only `submit_review` can move to `done`.\n\n")

	// Git & Sync section.
	b.WriteString("## Git & Sync (Natural Language Mapping)\n\n")
	b.WriteString("The MCP provides 6 git tools that use the current user's person profile for author identity. **Map natural language requests to these tools automatically:**\n\n")
	b.WriteString("| User says | Action |\n")
	b.WriteString("|-----------|--------|\n")
	b.WriteString("| \"sync my changes\", \"push my updates\", \"sync to cloud\" | `git_quick_commit` (stage all + commit) → `git_push` |\n")
	b.WriteString("| \"get latest\", \"pull updates\", \"sync from cloud\" | `git_pull` |\n")
	b.WriteString("| \"save my work\", \"commit this\" | `git_quick_commit` |\n")
	b.WriteString("| \"push\", \"push to remote\" | `git_push` |\n")
	b.WriteString("| \"create a branch for X\" | `git_create_branch` |\n")
	b.WriteString("| \"merge X\" | `git_merge_branch` |\n")
	b.WriteString("| \"what's the status\", \"git status\" | `git_status_summary` |\n")
	b.WriteString("| \"pull and rebase\" | `git_pull` with `rebase: true` |\n\n")
	b.WriteString("When the user says \"sync\" without a specific message, generate a meaningful commit message from the staged changes. All commits use the current user's person profile (name + github_email). No `Co-Authored-By` lines.\n\n")

	// Onboarding section.
	b.WriteString("## Onboarding (First Interaction)\n\n")
	b.WriteString("On the first interaction with a new user, check `get_current_user`. If not configured:\n\n")
	b.WriteString("1. Use `AskUserQuestion` to collect: name, role, email, github_email, bio, timezone\n")
	b.WriteString("2. `create_person` with the collected profile data\n")
	b.WriteString("3. `set_current_user` to link them to the project\n")
	b.WriteString("4. Confirm the setup — the profile persists in `~/.orchestra/me.json` across sessions\n\n")

	// Available Tools section.
	b.WriteString("## Available Tools\n\n")
	b.WriteString("Orchestra provides **85 tools** via MCP (70 feature workflow + 15 marketplace) and **5 prompts**.\n\n")
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

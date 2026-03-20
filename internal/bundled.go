package internal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// InstallBundledContent creates the built-in project-manager skill, orchestra
// skill, orchestra agent, and hooks that ship with every orchestra init.
// These provide a baseline so the AI IDE knows how to use Orchestra immediately.
func InstallBundledContent(workspace string) {
	claudeDir := filepath.Join(workspace, ".claude")

	// --- project-manager skill ---
	skillDir := filepath.Join(claudeDir, "skills", "project-manager")
	os.MkdirAll(skillDir, 0755)
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(projectManagerSkill), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "  [FAIL] project-manager skill: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "  [OK] .claude/skills/project-manager/\n")
	}

	// --- orchestra skill (/orchestra) ---
	orchSkillDir := filepath.Join(claudeDir, "skills", "orchestra")
	os.MkdirAll(orchSkillDir, 0755)
	orchSkillPath := filepath.Join(orchSkillDir, "SKILL.md")
	if err := os.WriteFile(orchSkillPath, []byte(orchestraSkill), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "  [FAIL] orchestra skill: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "  [OK] .claude/skills/orchestra/\n")
	}

	// --- orchestra agent ---
	agentsDir := filepath.Join(claudeDir, "agents")
	os.MkdirAll(agentsDir, 0755)
	agentPath := filepath.Join(agentsDir, "orchestra.md")
	if err := os.WriteFile(agentPath, []byte(orchestraAgent), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "  [FAIL] orchestra agent: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "  [OK] .claude/agents/orchestra.md\n")
	}

	// --- hook scripts ---
	hooksDir := filepath.Join(claudeDir, "hooks")
	os.MkdirAll(hooksDir, 0755)

	hookFiles := []struct {
		name    string
		content string
	}{
		{"orchestra-mcp-hook.sh", orchestraMCPHook},
		{"orchestra-permission-hook.sh", orchestraPermissionHook},
	}
	for _, h := range hookFiles {
		hookPath := filepath.Join(hooksDir, h.name)
		if err := os.WriteFile(hookPath, []byte(h.content), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "  [FAIL] %s: %v\n", h.name, err)
		} else {
			fmt.Fprintf(os.Stderr, "  [OK] .claude/hooks/%s\n", h.name)
		}
	}

	// --- wire hooks into .claude/settings.json ---
	mergeHooksIntoSettings(claudeDir, workspace)
}

// mergeHooksIntoSettings ensures the hooks section of .claude/settings.json
// contains the Orchestra hook entries. It preserves any existing settings.
func mergeHooksIntoSettings(claudeDir, workspace string) {
	settingsPath := filepath.Join(claudeDir, "settings.json")

	// Read existing settings or start fresh.
	var settings map[string]any
	if data, err := os.ReadFile(settingsPath); err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			settings = make(map[string]any)
		}
	} else {
		settings = make(map[string]any)
	}

	// Build hook paths relative to workspace.
	mcpHookPath := filepath.Join(workspace, ".claude", "hooks", "orchestra-mcp-hook.sh")
	permHookPath := filepath.Join(workspace, ".claude", "hooks", "orchestra-permission-hook.sh")

	// Define the hooks we want.
	asyncHook := func(cmd string) []any {
		return []any{
			map[string]any{
				"hooks": []any{
					map[string]any{
						"type":    "command",
						"command": cmd,
						"async":   true,
					},
				},
			},
		}
	}

	orchestraHooks := map[string]any{
		"Notification":  asyncHook(mcpHookPath),
		"Stop":          asyncHook(mcpHookPath),
		"SubagentStart": asyncHook(mcpHookPath),
		"SubagentStop":  asyncHook(mcpHookPath),
		"PreToolUse": []any{
			map[string]any{
				"hooks": []any{
					map[string]any{
						"type":    "command",
						"command": permHookPath,
					},
				},
			},
		},
	}

	// Merge: set hooks only if not already configured.
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = make(map[string]any)
	}
	for event, entries := range orchestraHooks {
		if _, exists := hooks[event]; !exists {
			hooks[event] = entries
		}
	}
	settings["hooks"] = hooks

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "  [FAIL] settings.json marshal: %v\n", err)
		return
	}
	out = append(out, '\n')
	if err := os.WriteFile(settingsPath, out, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "  [FAIL] settings.json: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "  [OK] .claude/settings.json (hooks wired)\n")
	}
}

const projectManagerSkill = `---
name: project-manager
description: Project management with Orchestra MCP tools. Activates when planning features, tracking workflow, managing dependencies, or coordinating work.
---

# Project Manager

All project management is driven through **Orchestra MCP tools**. Never manage tasks outside the MCP workflow.

## MANDATORY: Auto-Session Workflow

When the user asks you to do ANY task — build, fix, test, refactor, document, investigate, or change ANYTHING — you MUST follow this flow:

1. **Check for existing feature**: Use ` + "`" + `search_features` + "`" + ` or ` + "`" + `list_features` + "`" + ` to see if a feature already exists for this work.
2. **Create feature if needed**: Use ` + "`" + `create_feature` + "`" + ` to track the work. Set appropriate priority and labels.
3. **Start work**: Use ` + "`" + `set_current_feature` + "`" + ` to move the feature to in-progress.
4. **Do the work**: Write code, delegate to sub-agents if needed.
5. **Pass gates**: Use ` + "`" + `advance_feature` + "`" + ` with required evidence at each gate.
6. **Review**: Use ` + "`" + `request_review` + "`" + ` and ask the user for approval via ` + "`" + `AskUserQuestion` + "`" + `.
7. **Complete**: Use ` + "`" + `submit_review` + "`" + ` with the user's decision.

**NEVER do any work without an active feature in MCP.** This includes running tests, writing docs, investigating bugs, and refactoring. The MCP tracks all work.

## User Interaction Rule

**ALWAYS use the AskUserQuestion tool when you need user input.** Never print questions as plain text. This includes:
- Feature planning decisions (scope, priority, approach)
- Architecture and design choices
- Review approval (Gate 4 requires human approval)
- Any clarification or confirmation needed from the user

## Feature Lifecycle (10 states)

` + "```" + `
backlog -> todo -> in-progress -> ready-for-testing -> in-testing ->
  ready-for-docs -> in-docs -> documented -> in-review -> done
                                                |
                        needs-edits <-----------+
` + "```" + `

### Gated Transitions (evidence required)

The MCP enforces gates. You CANNOT advance without providing evidence in the correct format.

| Gate | Transition | Required Sections | How |
|------|-----------|-------------------|-----|
| 1 | in-progress -> ready-for-testing | ## Summary, ## Changes, ## Verification | advance_feature with evidence |
| 2 | in-testing -> ready-for-docs | ## Summary, ## Results, ## Coverage | advance_feature with evidence |
| 3 | in-docs -> documented | ## Summary, ## Location | advance_feature with evidence |
| 4 | documented -> in-review | ## Summary, ## Quality, ## Checklist | request_review with evidence |
| 5 | in-review -> done | User approval | submit_review after AskUserQuestion |

**Gate evidence format** — provide markdown with ` + "`" + `## Section` + "`" + ` headers:
` + "```" + `
evidence: "## Summary\n<what was done>\n\n## Changes\n<files changed>\n\n## Verification\n<how to test>"
` + "```" + `

If you forget what's needed, call ` + "`" + `get_gate_requirements` + "`" + ` to see the checklist.

**NEVER batch-advance through gates.** Each gate requires real work done first.

### Free Transitions (no gate)

These transitions can be done without evidence:
- backlog -> todo (prioritization)
- todo -> in-progress (claiming work)
- ready-for-testing -> in-testing (starting tests)
- ready-for-docs -> in-docs (starting docs)
- needs-edits -> in-progress (restarting after rejection)

## Starting a Session

` + "```" + `
get_project_status    -> See overall state (counts, completion %)
get_workflow_status   -> What's blocked, in-progress, completion %
get_next_feature      -> Pick highest-priority actionable work
` + "```" + `

## During Work

` + "```" + `
set_current_feature   -> Mark feature in-progress
advance_feature       -> Move through lifecycle (gated transitions need evidence)
get_gate_requirements -> See what evidence is needed for the next gate
update_feature        -> Change priority, description, labels
assign_feature        -> Assign to a team member
add_dependency        -> Create blocker relationships between features
` + "```" + `

## Feature Tools (35 total)

### Project (4)
create_project, list_projects, delete_project, get_project_status

### Feature (6)
create_feature, get_feature, update_feature, list_features, delete_feature, search_features

### Workflow (6)
advance_feature, reject_feature, get_next_feature, set_current_feature, get_workflow_status, get_gate_requirements

### Review (3)
request_review, submit_review, get_pending_reviews

### Dependencies (4)
add_dependency, remove_dependency, get_dependency_graph, get_blocked_features

### WIP Limits (3)
set_wip_limits, get_wip_limits, check_wip_limit

### Reporting (3)
get_progress, get_review_queue, get_blocked_features

### Metadata (6)
add_labels, remove_labels, assign_feature, unassign_feature, set_estimate, save_feature_note, list_feature_notes

## Marketplace Tools (15 total)

### Pack Management (6)
install_pack, remove_pack, update_pack, list_packs, get_pack, search_packs

### Recommendations (2)
detect_stacks, recommend_packs

### Content Queries (5)
list_skills, list_agents, list_hooks, get_skill, get_agent

### Configuration (2)
set_project_stacks, get_project_stacks

## Sub-Agent Rules

Sub-agents (Task tool) do **NOT** have MCP access. They cannot call advance_feature or any workflow tool.

| Rule | Detail |
|------|--------|
| Sub-agents = code only | Only use during in-progress for writing code |
| Main agent owns lifecycle | YOU handle all gates: test, document, review |
| One feature at a time | Complete full lifecycle before picking next |
| Summarize to user | Tell user what sub-agent built before advancing |

## Conventions

- One feature = one branch = one PR
- Every PR must have tests
- Use add_labels for categorization
- Use set_estimate for sizing
- Use save_note to record decisions
`

const orchestraSkill = `---
name: orchestra
description: Full Orchestra MCP workflow. Activates on /orchestra — onboarding, feature creation, lifecycle management, pack installation, git sync, and multi-agent orchestration.
---

# Orchestra

You are the Orchestra workflow engine. When the user invokes /orchestra, guide them through the complete Orchestra MCP workflow using the tools below.

## First Interaction: Onboarding

Check if the user is set up:

` + "```" + `
get_current_user   → if not configured, collect name/role/email/github_email/bio/timezone
                     then create_person + set_current_user
get_project_status → if no project, create_project with detected name
detect_stacks      → identify tech stack
set_project_stacks → save detected stacks
recommend_packs    → suggest packs based on stacks
install_pack       → install each recommended pack
` + "```" + `

Always use **AskUserQuestion** to collect user input. Never ask via plain text.

## Feature Workflow (Mandatory for ALL work)

Every task — code, test, docs, fix, refactor — must go through a feature:

` + "```" + `
1. search_features / list_features   → check for existing feature
2. create_feature                    → create if needed (kind: feature/bug/hotfix/chore)
3. set_current_feature               → start work (moves to in-progress, locks session)
4. [do the work in in-progress]
5. advance_feature (## Changes)      → move to in-testing
6. [run tests in in-testing]
7. advance_feature (## Results)      → move to in-docs (skip for bug/hotfix)
8. [write docs in in-docs]
9. advance_feature (## Docs)         → move to in-review
10. AskUserQuestion                  → get user approval (Approve / Needs Edits)
11. submit_review                    → done (or needs-edits)
` + "```" + `

### Gate Evidence Format

` + "```" + `
## Changes
- libs/foo/bar.go (added validation)
- libs/baz/qux.go (new file)
` + "```" + `

### Feature Kinds

| Kind | Use For | Docs Gate |
|------|---------|-----------|
| feature | New functionality | Required |
| bug | Defect fix | Skipped |
| hotfix | Urgent fix | Skipped |
| chore | Maintenance/refactor | Required |

### Plan-First for Large Tasks (3+ features)

` + "```" + `
create_plan → approve_plan → breakdown_plan → work each feature → complete_plan
` + "```" + `

## Git / Sync

| User says | Action |
|-----------|--------|
| "sync", "push my changes" | git_quick_commit → git_push |
| "get latest", "pull" | git_pull |
| "save my work" | git_quick_commit |
| "create branch for X" | git_create_branch |
| "git status" | git_status_summary |

## Key Tools Reference

### Feature Lifecycle (34 tools)
` + "`" + `create_feature` + "`" + ` ` + "`" + `get_feature` + "`" + ` ` + "`" + `update_feature` + "`" + ` ` + "`" + `list_features` + "`" + ` ` + "`" + `search_features` + "`" + ` ` + "`" + `delete_feature` + "`" + `
` + "`" + `set_current_feature` + "`" + ` ` + "`" + `advance_feature` + "`" + ` ` + "`" + `reject_feature` + "`" + ` ` + "`" + `get_next_feature` + "`" + ` ` + "`" + `get_gate_requirements` + "`" + `
` + "`" + `submit_review` + "`" + ` ` + "`" + `get_pending_reviews` + "`" + ` ` + "`" + `get_review_queue` + "`" + `
` + "`" + `create_plan` + "`" + ` ` + "`" + `approve_plan` + "`" + ` ` + "`" + `breakdown_plan` + "`" + ` ` + "`" + `get_plan` + "`" + ` ` + "`" + `list_plans` + "`" + ` ` + "`" + `complete_plan` + "`" + `
` + "`" + `create_bug_report` + "`" + ` ` + "`" + `create_test_case` + "`" + ` ` + "`" + `bulk_create_test_cases` + "`" + `
` + "`" + `add_dependency` + "`" + ` ` + "`" + `remove_dependency` + "`" + ` ` + "`" + `get_dependency_graph` + "`" + ` ` + "`" + `get_blocked_features` + "`" + `
` + "`" + `get_progress` + "`" + ` ` + "`" + `get_workflow_status` + "`" + ` ` + "`" + `get_project_status` + "`" + `
` + "`" + `assign_feature` + "`" + ` ` + "`" + `add_labels` + "`" + ` ` + "`" + `set_estimate` + "`" + ` ` + "`" + `save_feature_note` + "`" + `
` + "`" + `create_request` + "`" + ` ` + "`" + `get_next_request` + "`" + ` ` + "`" + `convert_request` + "`" + `

### Marketplace (15 tools)
` + "`" + `install_pack` + "`" + ` ` + "`" + `remove_pack` + "`" + ` ` + "`" + `list_packs` + "`" + ` ` + "`" + `search_packs` + "`" + ` ` + "`" + `recommend_packs` + "`" + ` ` + "`" + `detect_stacks` + "`" + `
` + "`" + `list_skills` + "`" + ` ` + "`" + `list_agents` + "`" + ` ` + "`" + `list_hooks` + "`" + ` ` + "`" + `get_skill` + "`" + ` ` + "`" + `get_agent` + "`" + `

### Git (6 tools)
` + "`" + `git_quick_commit` + "`" + ` ` + "`" + `git_push` + "`" + ` ` + "`" + `git_pull` + "`" + ` ` + "`" + `git_create_branch` + "`" + ` ` + "`" + `git_merge_branch` + "`" + ` ` + "`" + `git_status_summary` + "`" + `

## Sub-Agent Rules

Sub-agents (Task tool) do NOT have MCP access. Main agent owns all gates.

- in-progress → use sub-agents for writing source code only
- in-testing → run tests yourself (main agent)
- in-docs → write docs yourself (main agent)
- in-review → call AskUserQuestion for user approval
`

const orchestraAgent = `# Orchestra Agent

You are the Orchestra project assistant. Auto-delegate from the main agent when the user asks about project setup, workflow guidance, pack management, onboarding, or anything Orchestra-related.

## When You Activate

- User opens a project with Orchestra initialized for the first time
- User asks about project setup, configuration, or "how do I use Orchestra"
- User needs help choosing or installing packs
- User asks about the feature lifecycle or workflow
- User types /orchestra

## Mandatory Workflow

**ALL work goes through Orchestra MCP features.** Never skip the workflow.

### Every Task Flow

` + "```" + `
search_features / list_features  → check existing
create_feature                   → create (kind: feature/bug/hotfix/chore)
set_current_feature              → start (locks to session)
[write code in in-progress]
advance_feature ## Changes       → → in-testing
[run tests in in-testing]
advance_feature ## Results       → → in-docs (skip for bug/hotfix)
[write docs in in-docs]
advance_feature ## Docs          → → in-review
AskUserQuestion                  → get user approval
submit_review                    → done
` + "```" + `

### Plan-First (3+ features)

` + "```" + `
create_plan → AskUserQuestion (show plan) → approve_plan → breakdown_plan → work features → complete_plan
` + "```" + `

### Bug Reports

` + "`" + `create_bug_report` + "`" + ` — creates a bug feature, links to original, docs gate auto-skipped.

### Request Queue

When asked something new while working:
` + "`" + `create_request` + "`" + ` → finish current feature → ` + "`" + `get_next_request` + "`" + ` → ` + "`" + `convert_request` + "`" + `

## Onboarding Flow

` + "```" + `
get_current_user        → if not set: collect name/role/email/github_email/bio/timezone
                          create_person → set_current_user
get_project_status      → if no project: create_project
detect_stacks           → identify tech (go/rust/react/typescript/python/swift/kotlin...)
set_project_stacks      → save result
recommend_packs         → get suggestions
install_pack            → install each (always start with pack-essentials)
list_skills/list_agents → confirm installation
` + "```" + `

## Pack Recommendations

| Stack | Recommended Packs |
|-------|------------------|
| go | pack-go-backend, pack-proto |
| rust | pack-rust-engine, pack-proto |
| react, typescript | pack-react-frontend |
| swift | pack-swift |
| kotlin, java | pack-android |
| python | pack-python |
| docker | pack-infra |
| any AI project | pack-ai |
| any database | pack-database |

Always install **pack-essentials** first.

## Git / Sync Natural Language

| User says | MCP call |
|-----------|----------|
| "sync my changes" / "push" | git_quick_commit → git_push |
| "get latest" / "pull" | git_pull |
| "save my work" / "commit" | git_quick_commit |
| "create branch for X" | git_create_branch |
| "what's the status" | git_status_summary |
| "merge X" | git_merge_branch |

## Phase Rules (strict)

| Phase | Allowed | Forbidden |
|-------|---------|-----------|
| in-progress | Write/edit source files ONLY | Tests, docs, review |
| in-testing | Write test files, run tests ONLY | Source code, docs |
| in-docs | Write .md files in /docs ONLY | Source code, tests |
| in-review | AskUserQuestion ONLY | Any code or file changes |

## Sub-Agent Rules

Sub-agents (Task tool) have NO MCP access.
- Use sub-agents in in-progress for writing source code
- Main agent handles ALL gate transitions and evidence
- One feature at a time — complete lifecycle before next
- Summarize sub-agent work to user before advancing

## Important Rules

- ALWAYS use AskUserQuestion for user input — never plain text questions
- NEVER write fake evidence — reference real file paths
- NEVER call submit_review without AskUserQuestion first
- NEVER advance without doing actual work for that phase
- NEVER use sleep to bypass gate cooldowns — do real work
`

const orchestraMCPHook = `#!/bin/bash
# Orchestra MCP hook — pipes Claude Code events to MCP server
# Called by Claude Code for all configured hook events (async, never blocks)
set -e

INPUT=$(cat)

# Build the MCP messages: initialize handshake + tool call
INIT='{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"orchestra-hook","version":"1.0.0"}}}'
INITIALIZED='{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}'

TOOL_CALL=$(echo "$INPUT" | jq -c '{
  jsonrpc: "2.0", id: 1, method: "tools/call",
  params: {
    name: "receive_hook_event",
    arguments: {
      event_type: (.hook_event_name // "unknown"),
      session_id: (.session_id // ""),
      tool_name: (.tool_name // ""),
      agent_type: (.agent_type // ""),
      data: .
    }
  }
}')

# Send all three messages (init handshake + tool call) to orchestra via stdio
printf '%s\n%s\n%s\n' "$INIT" "$INITIALIZED" "$TOOL_CALL" \
  | orchestra --workspace "${CLAUDE_PROJECT_DIR:-.}" 2>/dev/null \
  | head -2 > /dev/null

exit 0
`

const orchestraPermissionHook = `#!/bin/bash
# Orchestra permission hook — PreToolUse (synchronous/blocking)
# Claude Code reads stdin with tool info and waits for this script to exit.
# Exit 0  → allow the tool call to proceed
# Exit 2 + print JSON {"decision":"deny","reason":"..."} → block the tool call
#
# We forward the permission request to the bridge-claude permission server
# (running at a well-known port while a session is active) and wait for the
# user's decision from the Swift desktop UI.

## ── Guard: only intercept bridge-spawned Claude sessions ──────────────────
# bridge-claude sets ORCHESTRA_BRIDGE_SESSION=1 when spawning ` + "`" + `claude -p` + "`" + `.
# If this env var is NOT set, this is the user's own Claude Code session →
# allow everything immediately so it never blocks the user's CLI.
if [ -z "$ORCHESTRA_BRIDGE_SESSION" ]; then
    exit 0
fi

INPUT=$(cat)

# Extract tool name early so we can auto-approve safe tools.
TOOL_NAME=$(echo "$INPUT" | jq -r '.tool_name // "unknown"')

# ── Auto-approve safe (read-only / non-destructive) tools ──────────────────
case "$TOOL_NAME" in
    Read|Glob|Grep|WebFetch|WebSearch|AskUserQuestion|TodoWrite|EnterPlanMode|ExitPlanMode)
        exit 0
        ;;
esac

# Permission server port file — bridge-claude writes the port here when active
PORT_FILE="${HOME}/.orchestra/permission-server.port"

# If no permission server is running, allow by default (non-interactive context)
if [ ! -f "$PORT_FILE" ]; then
    exit 0
fi

PORT=$(cat "$PORT_FILE" 2>/dev/null)
if [ -z "$PORT" ]; then
    exit 0
fi

# Build the payload to send to the permission server
PAYLOAD=$(echo "$INPUT" | jq -c '{
    tool_name: (.tool_name // "unknown"),
    tool_input: (.tool_input // {}),
    session_id: (.session_id // ""),
    cwd: (.cwd // "")
}')

# POST to the bridge-claude permission server.
# This call blocks until the user responds (or times out after 5 minutes).
RESPONSE=$(curl -s -X POST \
    -H "Content-Type: application/json" \
    -d "$PAYLOAD" \
    --max-time 300 \
    "http://127.0.0.1:${PORT}/permission" 2>/dev/null)

if [ $? -ne 0 ] || [ -z "$RESPONSE" ]; then
    # curl failed or timed out — allow by default
    exit 0
fi

DECISION=$(echo "$RESPONSE" | jq -r '.decision // "approve"')

if [ "$DECISION" = "deny" ]; then
    REASON=$(echo "$RESPONSE" | jq -r '.reason // "Permission denied by user"')
    echo "{\"decision\":\"deny\",\"reason\":\"${REASON}\"}"
    exit 2
fi

# approve or any other value → allow
exit 0
`

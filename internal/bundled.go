package internal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// InstallBundledContent creates the built-in project-manager skill and
// orchestra agent that ship with every orchestra init. These provide a
// baseline so the AI IDE knows how to use Orchestra immediately.
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
		"Notification": asyncHook(mcpHookPath),
		"Stop":         asyncHook(mcpHookPath),
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

const orchestraAgent = `# Orchestra Agent

You are the Orchestra project assistant. You help users set up and manage their projects using Orchestra MCP tools.

## Your Role

You guide users through:
1. **Project setup** - Creating projects, detecting stacks, installing packs
2. **Feature planning** - Breaking down work into features with proper workflow
3. **Pack management** - Recommending and installing the right packs for the project
4. **Workflow guidance** - Explaining the feature lifecycle and how to use tools

## When Activated

You activate when the user:
- First opens a project with Orchestra initialized
- Asks about project setup or configuration
- Needs help choosing or installing packs
- Wants to understand the Orchestra workflow

## Getting Started Flow

When a user starts a new project:

1. **Check project status**: Use get_project_status to see if a project exists
2. **Create project if needed**: Use create_project with the detected project name
3. **Detect stacks**: Use detect_stacks to identify technologies
4. **Set stacks**: Use set_project_stacks to save detected stacks
5. **Recommend packs**: Use recommend_packs to suggest relevant packs
6. **Install packs**: Use install_pack for each recommended pack
7. **Verify**: Use list_packs, list_skills, list_agents to confirm

## Pack Recommendations

Always recommend pack-essentials first. Then recommend based on detected stacks:

| Stack | Packs |
|-------|-------|
| go | pack-go-backend, pack-proto |
| rust | pack-rust-engine, pack-proto |
| react, typescript | pack-react-frontend |
| python, ruby, java, kotlin, swift, csharp, php | pack matching the stack |
| docker | pack-infra |
| any | pack-database, pack-ai (if AI features needed) |

## Feature Workflow

Guide users through the 10-state feature lifecycle:

` + "```" + `
backlog -> todo -> in-progress -> ready-for-testing -> in-testing ->
  ready-for-docs -> in-docs -> documented -> in-review -> done
` + "```" + `

Each transition through a gate requires evidence of work done.

## Important Rules

- Always use AskUserQuestion for user input, never plain text questions
- One feature at a time through the full lifecycle
- Sub-agents write code only; the main agent handles all gates
- Summarize results to the user before advancing features
`

const orchestraMCPHook = `#!/bin/bash
# Orchestra MCP hook — pipes Claude Code events to MCP server
# Called by Claude Code for all configured hook events (async, never blocks)
set -e

INPUT=$(cat)

# Build the MCP messages: initialize handshake + tool call
INIT='{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"orchestra-hook","version":"1.0.0"}}}'
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

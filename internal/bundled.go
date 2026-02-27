package internal

import (
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
}

const projectManagerSkill = `---
name: project-manager
description: Project management with Orchestra MCP tools. Activates when planning features, tracking workflow, managing dependencies, or coordinating work.
---

# Project Manager

All project management is driven through **Orchestra MCP tools**. Never manage tasks outside the MCP workflow.

## User Interaction Rule

**ALWAYS use the AskUserQuestion tool when you need user input.** Never print questions as plain text. This includes:
- Feature planning decisions (scope, priority, approach)
- Architecture and design choices
- Any clarification or confirmation needed from the user

## Feature Lifecycle (10 states)

` + "```" + `
backlog -> todo -> in-progress -> ready-for-testing -> in-testing ->
  ready-for-docs -> in-docs -> documented -> in-review -> done
                                                |
                        needs-edits <-----------+
` + "```" + `

### Gated Transitions (evidence required)

| Gate | From | Action Required | Evidence Example |
|------|------|----------------|-----------------|
| 1 | in-progress | Run tests, confirm pass | "go test ./... - all passed" |
| 2 | in-testing | Verify coverage, edge cases | "Coverage 85%, edge cases covered" |
| 3 | in-docs | Write/update documentation | "Added docs, updated README" |
| 4 | in-review | Review code quality | "No issues, error handling OK" |

**NEVER batch-advance through gates.** Each gate requires real work done first.

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
update_feature        -> Change priority, description, labels
assign_feature        -> Assign to a team member
add_dependency        -> Create blocker relationships between features
` + "```" + `

## Feature Tools (34 total)

### Project (4)
create_project, list_projects, delete_project, get_project_status

### Feature (6)
create_feature, get_feature, update_feature, list_features, delete_feature, search_features

### Workflow (5)
advance_feature, reject_feature, get_next_feature, set_current_feature, get_workflow_status

### Review (3)
request_review, submit_review, get_pending_reviews

### Dependencies (4)
add_dependency, remove_dependency, get_dependency_graph, get_blocked_features

### WIP Limits (3)
set_wip_limits, get_wip_limits, check_wip_limit

### Reporting (3)
get_progress, get_review_queue, get_blocked_features

### Metadata (6)
add_labels, remove_labels, assign_feature, unassign_feature, set_estimate, save_note, list_notes

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

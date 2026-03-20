package internal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallBundledContent_CreatesHooks(t *testing.T) {
	tmp := t.TempDir()

	InstallBundledContent(tmp)

	// Verify hook files exist and are executable.
	hooks := []string{"orchestra-mcp-hook.sh", "orchestra-permission-hook.sh"}
	for _, name := range hooks {
		hookPath := filepath.Join(tmp, ".claude", "hooks", name)
		info, err := os.Stat(hookPath)
		if err != nil {
			t.Fatalf("hook %s not found: %v", name, err)
		}
		if info.Size() == 0 {
			t.Errorf("hook %s is empty", name)
		}
		// Check executable bit (owner).
		if info.Mode()&0100 == 0 {
			t.Errorf("hook %s is not executable (mode %o)", name, info.Mode())
		}
	}
}

func TestInstallBundledContent_CreatesSkillAndAgent(t *testing.T) {
	tmp := t.TempDir()

	InstallBundledContent(tmp)

	skillPath := filepath.Join(tmp, ".claude", "skills", "project-manager", "SKILL.md")
	if _, err := os.Stat(skillPath); err != nil {
		t.Fatalf("project-manager skill not found: %v", err)
	}

	agentPath := filepath.Join(tmp, ".claude", "agents", "orchestra.md")
	if _, err := os.Stat(agentPath); err != nil {
		t.Fatalf("orchestra agent not found: %v", err)
	}
}

func TestInstallBundledContent_HookContainsReceiveHookEvent(t *testing.T) {
	tmp := t.TempDir()

	InstallBundledContent(tmp)

	hookPath := filepath.Join(tmp, ".claude", "hooks", "orchestra-mcp-hook.sh")
	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	content := string(data)

	if !contains(content, "receive_hook_event") {
		t.Error("hook should reference receive_hook_event tool")
	}
	if !contains(content, "#!/bin/bash") {
		t.Error("hook should have bash shebang")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

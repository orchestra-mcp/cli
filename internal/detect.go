package internal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// stackInfo describes a detected technology stack.
type stackInfo struct {
	name     string
	evidence string
}

// detectStacks detects technology stacks in the given workspace.
func detectStacks(root string) []stackInfo {
	var stacks []stackInfo

	type check struct {
		name  string
		check func(string) (bool, string)
	}

	checks := []check{
		{"go", checkAnyFile("go.mod", "go.work")},
		{"rust", checkFile("Cargo.toml")},
		{"react", checkPkgJSONDep("react")},
		{"typescript", checkFile("tsconfig.json")},
		{"python", checkAnyFile("pyproject.toml", "requirements.txt", "setup.py")},
		{"ruby", checkFile("Gemfile")},
		{"java", checkAnyFile("pom.xml", "build.gradle")},
		{"kotlin", checkFile("build.gradle.kts")},
		{"swift", checkSwiftStack},
		{"csharp", checkCSharpStack},
		{"php", checkFile("composer.json")},
		{"docker", checkAnyFile("Dockerfile", "docker-compose.yml", "docker-compose.yaml")},
	}

	for _, c := range checks {
		if ok, evidence := c.check(root); ok {
			stacks = append(stacks, stackInfo{name: c.name, evidence: evidence})
		}
	}

	return stacks
}

func checkFile(name string) func(string) (bool, string) {
	return func(root string) (bool, string) {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			return true, name + " found"
		}
		return false, ""
	}
}

func checkAnyFile(names ...string) func(string) (bool, string) {
	return func(root string) (bool, string) {
		for _, name := range names {
			if _, err := os.Stat(filepath.Join(root, name)); err == nil {
				return true, name + " found"
			}
		}
		return false, ""
	}
}

func checkPkgJSONDep(dep string) func(string) (bool, string) {
	return func(root string) (bool, string) {
		data, err := os.ReadFile(filepath.Join(root, "package.json"))
		if err != nil {
			return false, ""
		}
		var pkg struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if json.Unmarshal(data, &pkg) != nil {
			return false, ""
		}
		if _, ok := pkg.Dependencies[dep]; ok {
			return true, dep + " in dependencies"
		}
		if _, ok := pkg.DevDependencies[dep]; ok {
			return true, dep + " in devDependencies"
		}
		return false, ""
	}
}

func checkSwiftStack(root string) (bool, string) {
	if _, err := os.Stat(filepath.Join(root, "Package.swift")); err == nil {
		return true, "Package.swift found"
	}
	matches, _ := filepath.Glob(filepath.Join(root, "*.xcodeproj"))
	if len(matches) > 0 {
		return true, ".xcodeproj found"
	}
	return false, ""
}

func checkCSharpStack(root string) (bool, string) {
	matches, _ := filepath.Glob(filepath.Join(root, "*.csproj"))
	if len(matches) > 0 {
		return true, ".csproj found"
	}
	matches, _ = filepath.Glob(filepath.Join(root, "*.sln"))
	if len(matches) > 0 {
		return true, ".sln found"
	}
	return false, ""
}

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

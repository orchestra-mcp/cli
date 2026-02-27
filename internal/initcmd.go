package internal

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func RunInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	workspace := fs.String("workspace", ".", "Project directory to initialize")
	ide := fs.String("ide", "", "Target IDE: claude, cursor, vscode, windsurf, codex, gemini, zed, continue, cline")
	all := fs.Bool("all", false, "Generate configs for all supported IDEs")
	fs.Parse(args)

	// Resolve absolute workspace path.
	absWorkspace, err := filepath.Abs(*workspace)
	if err != nil {
		fatal("resolve workspace: %v", err)
	}

	// Resolve the orchestra binary path.
	binPath, err := resolveBinaryPath()
	if err != nil {
		fatal("resolve binary path: %v", err)
	}

	// Detect project name.
	projectName := detectProjectName(absWorkspace)

	// Determine target IDEs.
	var targets []string
	if *all {
		targets = allIDENames()
	} else if *ide != "" {
		// Support comma-separated IDE names.
		for _, name := range strings.Split(*ide, ",") {
			name = strings.TrimSpace(name)
			if _, ok := ideRegistry[name]; !ok {
				fatal("unknown IDE %q. Supported: %s", name, strings.Join(allIDENames(), ", "))
			}
			targets = append(targets, name)
		}
	} else {
		// Auto-detect from existing IDE config directories.
		targets = detectIDEs(absWorkspace)
	}

	// Generate IDE configs.
	fmt.Fprintf(os.Stderr, "Initializing Orchestra MCP for project %q\n", projectName)
	fmt.Fprintf(os.Stderr, "Workspace: %s\n", absWorkspace)
	fmt.Fprintf(os.Stderr, "Binary: %s\n\n", binPath)

	for _, name := range targets {
		ide := ideRegistry[name]
		configPath := ide.ConfigPath(absWorkspace)
		content, err := ide.Generate(absWorkspace, binPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  [SKIP] %s: %v\n", ide.Display, err)
			continue
		}

		// Create parent directory.
		if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "  [SKIP] %s: mkdir: %v\n", ide.Display, err)
			continue
		}

		if err := os.WriteFile(configPath, content, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "  [SKIP] %s: write: %v\n", ide.Display, err)
			continue
		}

		// Show relative path if inside workspace, else absolute.
		displayPath := configPath
		if rel, err := filepath.Rel(absWorkspace, configPath); err == nil && !strings.HasPrefix(rel, "..") {
			displayPath = rel
		}
		fmt.Fprintf(os.Stderr, "  [OK] %s → %s\n", ide.Display, displayPath)
	}

	// Create .projects/ directory.
	projectsDir := filepath.Join(absWorkspace, ".projects")
	if err := os.MkdirAll(projectsDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "\n  [WARN] Could not create .projects/: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "\n  [OK] .projects/ directory ready\n")
	}

	// Install bundled skill + agent (project-manager, orchestra).
	fmt.Fprintf(os.Stderr, "\n")
	InstallBundledContent(absWorkspace)

	// Generate CLAUDE.md and AGENTS.md from installed content.
	fmt.Fprintf(os.Stderr, "\n")
	GenerateWorkspaceDocs(absWorkspace)

	// Detect technology stacks and recommend packs.
	stacks := detectStacks(absWorkspace)
	if len(stacks) > 0 {
		var stackNames []string
		for _, s := range stacks {
			stackNames = append(stackNames, s.name)
		}
		fmt.Fprintf(os.Stderr, "\n  Detected stacks: %s\n", strings.Join(stackNames, ", "))
		fmt.Fprintf(os.Stderr, "  Run 'orchestra pack recommend' to see recommended packs\n")
	}

	fmt.Fprintf(os.Stderr, "\nDone! Orchestra MCP is ready.\n")

	// Check for newer version (non-blocking advisory).
	CheckAndPromptUpdate()
}

func resolveBinaryPath() (string, error) {
	// 1. Use own executable path (most reliable).
	self, err := os.Executable()
	if err == nil {
		self, _ = filepath.EvalSymlinks(self)
		return self, nil
	}

	// 2. Look up "orchestra" in PATH.
	path, err := exec.LookPath("orchestra")
	if err == nil {
		path, _ = filepath.Abs(path)
		return path, nil
	}

	return "", fmt.Errorf("could not find orchestra binary")
}

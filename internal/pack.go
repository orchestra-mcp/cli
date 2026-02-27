package internal

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// packManifest is the parsed pack.json from a pack repo.
type packManifest struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	Stacks      []string `json:"stacks"`
	Contents    struct {
		Skills []string `json:"skills"`
		Agents []string `json:"agents"`
		Hooks  []string `json:"hooks"`
	} `json:"contents"`
	Tags []string `json:"tags"`
}

// packEntry describes an installed pack in the local registry.
type packEntry struct {
	Version     string   `json:"version"`
	Repo        string   `json:"repo"`
	InstalledAt string   `json:"installed_at"`
	Stacks      []string `json:"stacks"`
	Skills      []string `json:"skills"`
	Agents      []string `json:"agents"`
	Hooks       []string `json:"hooks"`
}

// packRegistry holds the local pack registry.
type packRegistry struct {
	Packs map[string]*packEntry `json:"packs"`
}

// RunPack handles `orchestra pack <subcommand>`.
func RunPack(args []string) {
	if len(args) < 1 {
		printPackUsage()
		return
	}

	switch args[0] {
	case "install":
		runPackInstall(args[1:])
	case "remove", "uninstall":
		runPackRemove(args[1:])
	case "update":
		runPackUpdate(args[1:])
	case "list", "ls":
		runPackList(args[1:])
	case "search":
		runPackSearch(args[1:])
	case "recommend":
		runPackRecommend(args[1:])
	case "help", "--help", "-h":
		printPackUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown pack subcommand: %s\n\n", args[0])
		printPackUsage()
		os.Exit(1)
	}
}

func printPackUsage() {
	fmt.Fprintf(os.Stderr, `orchestra pack — manage content packs (skills, agents, hooks)

Usage:
  orchestra pack install <repo>[@version]   Install a pack from GitHub
  orchestra pack remove <name>              Remove an installed pack
  orchestra pack update [name]              Update one or all packs
  orchestra pack list                       List installed packs
  orchestra pack search <query>             Search available packs
  orchestra pack recommend                  Detect stacks & recommend packs

Examples:
  orchestra pack install github.com/orchestra-mcp/pack-go-backend
  orchestra pack install github.com/orchestra-mcp/pack-essentials@v0.1.0
  orchestra pack remove orchestra-mcp/pack-go-backend
  orchestra pack search go
  orchestra pack recommend
`)
}

// --- install ---

func runPackInstall(args []string) {
	fs := flag.NewFlagSet("pack install", flag.ExitOnError)
	workspace := fs.String("workspace", ".", "Project workspace directory")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fatal("usage: orchestra pack install <repo>[@version]")
	}

	rawArg := fs.Arg(0)
	repo, version := parsePackRepoVersion(rawArg)

	absWorkspace, _ := filepath.Abs(*workspace)

	fmt.Fprintf(os.Stderr, "Installing pack from %s...\n", repo)

	manifest, err := installPackFromGit(absWorkspace, repo, version)
	if err != nil {
		fatal("install failed: %v", err)
	}

	// Update local registry.
	reg := loadPackRegistry(absWorkspace)
	reg.Packs[manifest.Name] = &packEntry{
		Version:     manifest.Version,
		Repo:        repo,
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
		Stacks:      manifest.Stacks,
		Skills:      manifest.Contents.Skills,
		Agents:      manifest.Contents.Agents,
		Hooks:       manifest.Contents.Hooks,
	}
	savePackRegistry(absWorkspace, reg)

	fmt.Fprintf(os.Stderr, "  Installed: %s@%s\n", manifest.Name, manifest.Version)
	if len(manifest.Contents.Skills) > 0 {
		fmt.Fprintf(os.Stderr, "  Skills: %s\n", strings.Join(manifest.Contents.Skills, ", "))
	}
	if len(manifest.Contents.Agents) > 0 {
		fmt.Fprintf(os.Stderr, "  Agents: %s\n", strings.Join(manifest.Contents.Agents, ", "))
	}
	if len(manifest.Contents.Hooks) > 0 {
		fmt.Fprintf(os.Stderr, "  Hooks: %s\n", strings.Join(manifest.Contents.Hooks, ", "))
	}

	// Regenerate workspace docs to reflect new content.
	GenerateWorkspaceDocs(absWorkspace)
}

// --- remove ---

func runPackRemove(args []string) {
	fs := flag.NewFlagSet("pack remove", flag.ExitOnError)
	workspace := fs.String("workspace", ".", "Project workspace directory")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fatal("usage: orchestra pack remove <name>")
	}

	name := fs.Arg(0)
	absWorkspace, _ := filepath.Abs(*workspace)

	reg := loadPackRegistry(absWorkspace)
	entry, ok := reg.Packs[name]
	if !ok {
		fatal("pack %q is not installed", name)
	}

	removePackFiles(absWorkspace, entry.Skills, entry.Agents, entry.Hooks)
	delete(reg.Packs, name)
	savePackRegistry(absWorkspace, reg)

	fmt.Fprintf(os.Stderr, "Removed pack: %s\n", name)

	// Regenerate workspace docs to reflect removed content.
	GenerateWorkspaceDocs(absWorkspace)
}

// --- update ---

func runPackUpdate(args []string) {
	fs := flag.NewFlagSet("pack update", flag.ExitOnError)
	workspace := fs.String("workspace", ".", "Project workspace directory")
	fs.Parse(args)

	absWorkspace, _ := filepath.Abs(*workspace)
	reg := loadPackRegistry(absWorkspace)

	name := ""
	if fs.NArg() > 0 {
		name = fs.Arg(0)
	}

	var toUpdate map[string]*packEntry
	if name != "" {
		entry, ok := reg.Packs[name]
		if !ok {
			fatal("pack %q is not installed", name)
		}
		toUpdate = map[string]*packEntry{name: entry}
	} else {
		toUpdate = reg.Packs
	}

	if len(toUpdate) == 0 {
		fmt.Fprintf(os.Stderr, "No packs installed to update.\n")
		return
	}

	for packName, entry := range toUpdate {
		fmt.Fprintf(os.Stderr, "Updating %s...\n", packName)
		removePackFiles(absWorkspace, entry.Skills, entry.Agents, entry.Hooks)

		manifest, err := installPackFromGit(absWorkspace, entry.Repo, "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "  [FAIL] %s: %v\n", packName, err)
			continue
		}

		reg.Packs[packName] = &packEntry{
			Version:     manifest.Version,
			Repo:        entry.Repo,
			InstalledAt: time.Now().UTC().Format(time.RFC3339),
			Stacks:      manifest.Stacks,
			Skills:      manifest.Contents.Skills,
			Agents:      manifest.Contents.Agents,
			Hooks:       manifest.Contents.Hooks,
		}
		fmt.Fprintf(os.Stderr, "  [OK] %s → %s\n", packName, manifest.Version)
	}

	savePackRegistry(absWorkspace, reg)

	// Regenerate workspace docs to reflect updated packs.
	GenerateWorkspaceDocs(absWorkspace)
}

// --- list ---

func runPackList(args []string) {
	fs := flag.NewFlagSet("pack list", flag.ExitOnError)
	workspace := fs.String("workspace", ".", "Project workspace directory")
	fs.Parse(args)

	absWorkspace, _ := filepath.Abs(*workspace)
	reg := loadPackRegistry(absWorkspace)

	if len(reg.Packs) == 0 {
		fmt.Fprintf(os.Stderr, "No packs installed. Run: orchestra pack install <repo>\n")
		return
	}

	fmt.Fprintf(os.Stderr, "Installed packs:\n\n")
	for name, entry := range reg.Packs {
		fmt.Fprintf(os.Stderr, "  %-40s %s  (%d skills, %d agents, %d hooks)\n",
			name, entry.Version,
			len(entry.Skills), len(entry.Agents), len(entry.Hooks))
	}
}

// --- search ---

func runPackSearch(args []string) {
	fs := flag.NewFlagSet("pack search", flag.ExitOnError)
	fs.Parse(args)

	if fs.NArg() < 1 {
		fatal("usage: orchestra pack search <query>")
	}

	query := strings.ToLower(fs.Arg(0))

	type knownPack struct {
		Repo        string
		Stacks      []string
		Description string
		Tags        []string
	}

	// Same index as internal/packs/index.go — kept in sync.
	known := []knownPack{
		{Repo: "github.com/orchestra-mcp/pack-essentials", Stacks: []string{"*"}, Description: "Core project management skills and agents", Tags: []string{"core", "essential"}},
		{Repo: "github.com/orchestra-mcp/pack-go-backend", Stacks: []string{"go"}, Description: "Go backend skills (Fiber, GORM, REST)", Tags: []string{"go", "backend", "fiber"}},
		{Repo: "github.com/orchestra-mcp/pack-rust-engine", Stacks: []string{"rust"}, Description: "Rust engine skills", Tags: []string{"rust", "engine"}},
		{Repo: "github.com/orchestra-mcp/pack-react-frontend", Stacks: []string{"react", "typescript"}, Description: "React frontend skills", Tags: []string{"react", "typescript"}},
		{Repo: "github.com/orchestra-mcp/pack-database", Stacks: []string{"*"}, Description: "Database skills (PostgreSQL, SQLite, Redis)", Tags: []string{"database", "sql"}},
		{Repo: "github.com/orchestra-mcp/pack-ai", Stacks: []string{"*"}, Description: "AI/LLM integration skills", Tags: []string{"ai", "llm", "rag"}},
		{Repo: "github.com/orchestra-mcp/pack-mobile", Stacks: []string{"react-native"}, Description: "React Native mobile skills", Tags: []string{"mobile"}},
		{Repo: "github.com/orchestra-mcp/pack-desktop", Stacks: []string{"go"}, Description: "Desktop app skills", Tags: []string{"desktop", "wails"}},
		{Repo: "github.com/orchestra-mcp/pack-extensions", Stacks: []string{"*"}, Description: "Extension system skills", Tags: []string{"extensions"}},
		{Repo: "github.com/orchestra-mcp/pack-chrome", Stacks: []string{"typescript"}, Description: "Chrome extension skills", Tags: []string{"chrome", "browser"}},
		{Repo: "github.com/orchestra-mcp/pack-infra", Stacks: []string{"docker"}, Description: "Infrastructure and DevOps skills", Tags: []string{"docker", "devops"}},
		{Repo: "github.com/orchestra-mcp/pack-proto", Stacks: []string{"go", "rust"}, Description: "Protobuf/gRPC skills", Tags: []string{"proto", "grpc"}},
		{Repo: "github.com/orchestra-mcp/pack-native-swift", Stacks: []string{"swift"}, Description: "Swift/macOS/iOS plugin skills", Tags: []string{"swift", "macos"}},
		{Repo: "github.com/orchestra-mcp/pack-native-kotlin", Stacks: []string{"kotlin", "java"}, Description: "Kotlin/Android plugin skills", Tags: []string{"kotlin", "android"}},
		{Repo: "github.com/orchestra-mcp/pack-native-csharp", Stacks: []string{"csharp"}, Description: "C#/Windows plugin skills", Tags: []string{"csharp", "windows"}},
		{Repo: "github.com/orchestra-mcp/pack-native-gtk", Stacks: []string{"c"}, Description: "GTK4/Linux desktop skills", Tags: []string{"gtk", "linux"}},
		{Repo: "github.com/orchestra-mcp/pack-analytics", Stacks: []string{"*"}, Description: "ClickHouse analytics skills", Tags: []string{"analytics", "clickhouse"}},
	}

	var matches []knownPack
	for _, p := range known {
		if strings.Contains(strings.ToLower(p.Repo), query) ||
			strings.Contains(strings.ToLower(p.Description), query) {
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
		fmt.Fprintf(os.Stderr, "No packs found for: %s\n", query)
		return
	}

	fmt.Fprintf(os.Stderr, "Available packs matching %q:\n\n", query)
	for _, p := range matches {
		fmt.Fprintf(os.Stderr, "  %-50s %s\n", p.Repo, p.Description)
		fmt.Fprintf(os.Stderr, "  %s  stacks: %s\n\n",
			strings.Repeat(" ", 50), strings.Join(p.Stacks, ", "))
	}
	fmt.Fprintf(os.Stderr, "Install with: orchestra pack install <repo>\n")
}

// --- recommend ---

func runPackRecommend(args []string) {
	fs := flag.NewFlagSet("pack recommend", flag.ExitOnError)
	workspace := fs.String("workspace", ".", "Project workspace directory")
	fs.Parse(args)

	absWorkspace, _ := filepath.Abs(*workspace)

	stacks := detectStacks(absWorkspace)

	if len(stacks) == 0 {
		fmt.Fprintf(os.Stderr, "No technology stacks detected in %s\n", absWorkspace)
		return
	}

	fmt.Fprintf(os.Stderr, "Detected stacks: ")
	var stackNames []string
	for _, s := range stacks {
		stackNames = append(stackNames, s.name)
	}
	fmt.Fprintf(os.Stderr, "%s\n\n", strings.Join(stackNames, ", "))

	fmt.Fprintf(os.Stderr, "Recommended packs:\n")

	type knownPack struct {
		Repo   string
		Stacks []string
		Desc   string
	}
	known := []knownPack{
		{"github.com/orchestra-mcp/pack-essentials", []string{"*"}, "Core skills and agents"},
		{"github.com/orchestra-mcp/pack-go-backend", []string{"go"}, "Go backend skills"},
		{"github.com/orchestra-mcp/pack-rust-engine", []string{"rust"}, "Rust engine skills"},
		{"github.com/orchestra-mcp/pack-react-frontend", []string{"react", "typescript"}, "React frontend skills"},
		{"github.com/orchestra-mcp/pack-database", []string{"*"}, "Database skills"},
		{"github.com/orchestra-mcp/pack-ai", []string{"*"}, "AI/LLM skills"},
		{"github.com/orchestra-mcp/pack-mobile", []string{"react-native"}, "React Native skills"},
		{"github.com/orchestra-mcp/pack-desktop", []string{"go"}, "Desktop app skills"},
		{"github.com/orchestra-mcp/pack-infra", []string{"docker"}, "Infrastructure skills"},
		{"github.com/orchestra-mcp/pack-proto", []string{"go", "rust"}, "Protobuf/gRPC skills"},
		{"github.com/orchestra-mcp/pack-native-swift", []string{"swift"}, "Swift/iOS skills"},
		{"github.com/orchestra-mcp/pack-native-kotlin", []string{"kotlin", "java"}, "Kotlin/Android skills"},
		{"github.com/orchestra-mcp/pack-native-csharp", []string{"csharp"}, "C#/Windows skills"},
		{"github.com/orchestra-mcp/pack-native-gtk", []string{"c"}, "GTK4/Linux skills"},
		{"github.com/orchestra-mcp/pack-analytics", []string{"*"}, "ClickHouse analytics"},
	}

	stackSet := make(map[string]bool)
	for _, s := range stacks {
		stackSet[s.name] = true
	}

	for _, p := range known {
		for _, ps := range p.Stacks {
			if ps == "*" || stackSet[ps] {
				fmt.Fprintf(os.Stderr, "  %-50s (%s)\n", p.Repo, strings.Join(p.Stacks, ", "))
				break
			}
		}
	}

	fmt.Fprintf(os.Stderr, "\nInstall with: orchestra pack install <repo>\n")
}

// --- helpers ---

func parsePackRepoVersion(raw string) (string, string) {
	if idx := strings.LastIndex(raw, "@"); idx > 0 {
		return raw[:idx], raw[idx+1:]
	}
	return raw, ""
}

func installPackFromGit(workspace, repo, version string) (*packManifest, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git not found in PATH")
	}

	tmpDir, err := os.MkdirTemp("", "orchestra-pack-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	cloneURL := "https://" + repo + ".git"
	cloneArgs := []string{"clone", "--depth", "1"}
	if version != "" {
		cloneArgs = append(cloneArgs, "--branch", version)
	}
	cloneArgs = append(cloneArgs, cloneURL, tmpDir)

	cmd := exec.Command("git", cloneArgs...)
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git clone %s: %w", cloneURL, err)
	}

	packJSON, err := os.ReadFile(filepath.Join(tmpDir, "pack.json"))
	if err != nil {
		return nil, fmt.Errorf("read pack.json: %w (is this a valid pack repo?)", err)
	}

	var manifest packManifest
	if err := json.Unmarshal(packJSON, &manifest); err != nil {
		return nil, fmt.Errorf("parse pack.json: %w", err)
	}

	claudeDir := filepath.Join(workspace, ".claude")

	for _, name := range manifest.Contents.Skills {
		src := filepath.Join(tmpDir, "skills", name)
		dst := filepath.Join(claudeDir, "skills", name)
		if err := copyDirRecursive(src, dst); err != nil {
			return nil, fmt.Errorf("copy skill %s: %w", name, err)
		}
	}

	for _, name := range manifest.Contents.Agents {
		src := filepath.Join(tmpDir, "agents", name+".md")
		dst := filepath.Join(claudeDir, "agents", name+".md")
		if err := copySingleFile(src, dst); err != nil {
			return nil, fmt.Errorf("copy agent %s: %w", name, err)
		}
	}

	for _, name := range manifest.Contents.Hooks {
		src := filepath.Join(tmpDir, "hooks", name+".sh")
		dst := filepath.Join(claudeDir, "hooks", name+".sh")
		if err := copySingleFile(src, dst); err != nil {
			return nil, fmt.Errorf("copy hook %s: %w", name, err)
		}
		os.Chmod(dst, 0755)
	}

	return &manifest, nil
}

func removePackFiles(workspace string, skills, agents, hooks []string) {
	claudeDir := filepath.Join(workspace, ".claude")
	for _, name := range skills {
		os.RemoveAll(filepath.Join(claudeDir, "skills", name))
	}
	for _, name := range agents {
		os.Remove(filepath.Join(claudeDir, "agents", name+".md"))
	}
	for _, name := range hooks {
		os.Remove(filepath.Join(claudeDir, "hooks", name+".sh"))
	}
}

func loadPackRegistry(workspace string) *packRegistry {
	path := filepath.Join(workspace, ".projects", ".packs", "registry.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return &packRegistry{Packs: make(map[string]*packEntry)}
	}
	var reg packRegistry
	if err := json.Unmarshal(data, &reg); err != nil {
		return &packRegistry{Packs: make(map[string]*packEntry)}
	}
	if reg.Packs == nil {
		reg.Packs = make(map[string]*packEntry)
	}
	return &reg
}

func savePackRegistry(workspace string, reg *packRegistry) {
	dir := filepath.Join(workspace, ".projects", ".packs")
	os.MkdirAll(dir, 0755)
	data, _ := json.MarshalIndent(reg, "", "  ")
	os.WriteFile(filepath.Join(dir, "registry.json"), data, 0644)
}

func copyDirRecursive(src, dst string) error {
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDirRecursive(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copySingleFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func copySingleFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

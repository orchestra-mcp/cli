package internal

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// pluginManifest is the JSON structure returned by `<binary> --manifest`.
type pluginManifest struct {
	ID              string   `json:"id"`
	ProvidesTools   []string `json:"provides_tools"`
	ProvidesStorage []string `json:"provides_storage"`
	NeedsStorage    []string `json:"needs_storage"`
}

// RunInstall handles `orchestra install <repo> [flags]`.
func RunInstall(args []string) {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	forceSource := fs.Bool("source", false, "Force build from source (skip binary download)")
	forceBinary := fs.Bool("binary", false, "Force binary download (fail if unavailable)")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fatal("usage: orchestra install <repo> [--source] [--binary]\n  Example: orchestra install github.com/someone/my-plugin@v1.2.0")
	}

	// Parse repo and optional version tag.
	rawArg := fs.Arg(0)
	repo, version := parseRepoVersion(rawArg)

	// Derive binary name from last path segment.
	name := filepath.Base(repo)
	if name == "" || name == "." {
		fatal("invalid repo path: %s", repo)
	}

	binDir := pluginBinDir()
	if err := os.MkdirAll(binDir, 0755); err != nil {
		fatal("create plugin bin dir: %v", err)
	}
	binPath := filepath.Join(binDir, name)

	installed := false

	// Strategy 1: Pre-built binary download (unless --source).
	if !*forceSource {
		fmt.Fprintf(os.Stderr, "Attempting binary download for %s...\n", repo)
		if err := downloadRelease(repo, version, name, binPath); err == nil {
			installed = true
			fmt.Fprintf(os.Stderr, "  Downloaded pre-built binary.\n")
		} else {
			fmt.Fprintf(os.Stderr, "  Binary download failed: %v\n", err)
			if *forceBinary {
				fatal("binary download failed and --binary flag was set")
			}
		}
	}

	// Strategy 2: Build from source.
	if !installed {
		fmt.Fprintf(os.Stderr, "Building from source...\n")
		if err := buildFromSource(repo, version, name, binPath); err != nil {
			fatal("source build failed: %v", err)
		}
		fmt.Fprintf(os.Stderr, "  Built from source.\n")
	}

	// Make binary executable.
	if err := os.Chmod(binPath, 0755); err != nil {
		fatal("chmod binary: %v", err)
	}

	// Query plugin manifest.
	manifest, err := queryManifest(binPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Warning: could not read manifest: %v\n", err)
		// Use defaults derived from the repo name.
		manifest = &pluginManifest{ID: name}
	}

	// Determine display version.
	displayVersion := version
	if displayVersion == "" {
		displayVersion = "latest"
	}

	// Register in registry.
	reg, err := LoadRegistry()
	if err != nil {
		fatal("load registry: %v", err)
	}

	reg.Plugins[repo] = &PluginEntry{
		ID:              manifest.ID,
		Version:         displayVersion,
		Binary:          binPath,
		Repo:            repo,
		InstalledAt:     time.Now().UTC().Format(time.RFC3339),
		ProvidesTools:   manifest.ProvidesTools,
		ProvidesStorage: manifest.ProvidesStorage,
		NeedsStorage:    manifest.NeedsStorage,
	}

	if err := SaveRegistry(reg); err != nil {
		fatal("save registry: %v", err)
	}

	// Print summary.
	fmt.Fprintf(os.Stderr, "\nInstalled %s (%s)\n", manifest.ID, displayVersion)
	fmt.Fprintf(os.Stderr, "  Binary: %s\n", binPath)
	if len(manifest.ProvidesTools) > 0 {
		fmt.Fprintf(os.Stderr, "  Tools:  %s\n", strings.Join(manifest.ProvidesTools, ", "))
	}
	if len(manifest.ProvidesStorage) > 0 {
		fmt.Fprintf(os.Stderr, "  Storage: %s\n", strings.Join(manifest.ProvidesStorage, ", "))
	}
}

// parseRepoVersion splits "github.com/foo/bar@v1.0.0" into repo and version.
func parseRepoVersion(s string) (repo, version string) {
	if idx := strings.LastIndex(s, "@"); idx != -1 {
		return s[:idx], s[idx+1:]
	}
	return s, ""
}

// downloadRelease tries to download a pre-built binary from GitHub releases.
func downloadRelease(repo, version, name, destPath string) error {
	// Extract owner/repo from full path (e.g. "github.com/owner/repo" -> "owner/repo").
	parts := strings.SplitN(repo, "/", 3)
	if len(parts) < 3 || parts[0] != "github.com" {
		return fmt.Errorf("binary downloads only supported for github.com repos")
	}
	ownerRepo := parts[1] + "/" + parts[2]

	osName := runtime.GOOS
	archName := runtime.GOARCH
	tarName := fmt.Sprintf("%s-%s-%s.tar.gz", name, osName, archName)

	var url string
	if version != "" {
		url = fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", ownerRepo, version, tarName)
	} else {
		url = fmt.Sprintf("https://github.com/%s/releases/latest/download/%s", ownerRepo, tarName)
	}

	fmt.Fprintf(os.Stderr, "  GET %s\n", url)

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	// Extract binary from tar.gz.
	return extractTarGz(resp.Body, name, destPath)
}

// extractTarGz reads a tar.gz stream and extracts the named binary to destPath.
func extractTarGz(r io.Reader, binaryName, destPath string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}

		// Look for the binary: could be at root or in a subdirectory.
		baseName := filepath.Base(header.Name)
		if baseName == binaryName && header.Typeflag == tar.TypeReg {
			out, err := os.Create(destPath)
			if err != nil {
				return fmt.Errorf("create file: %w", err)
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return fmt.Errorf("write file: %w", err)
			}
			out.Close()
			return nil
		}
	}

	// If only one regular file in the archive, use it regardless of name.
	// Re-reading is not possible, so we accept the first regular file as a fallback
	// during the loop above. Instead, return an error here.
	return fmt.Errorf("binary %q not found in archive", binaryName)
}

// buildFromSource clones the repo and builds using `go build`.
func buildFromSource(repo, version, name, destPath string) error {
	// Check that git is available.
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("git not found in PATH: %w", err)
	}

	// Check that go is available.
	if _, err := exec.LookPath("go"); err != nil {
		return fmt.Errorf("go not found in PATH: %w", err)
	}

	// Create temp directory for the clone.
	tmpDir, err := os.MkdirTemp("", "orchestra-install-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Clone the repo.
	cloneURL := "https://" + repo + ".git"
	cloneArgs := []string{"clone", "--depth", "1"}
	if version != "" {
		cloneArgs = append(cloneArgs, "--branch", version)
	}
	cloneArgs = append(cloneArgs, cloneURL, tmpDir)

	fmt.Fprintf(os.Stderr, "  git clone %s\n", cloneURL)
	gitCmd := exec.Command("git", cloneArgs...)
	gitCmd.Stderr = os.Stderr
	if err := gitCmd.Run(); err != nil {
		return fmt.Errorf("git clone: %w", err)
	}

	// Determine the build target: prefer cmd/main.go, then cmd/, then root.
	buildTarget := "./"
	if _, err := os.Stat(filepath.Join(tmpDir, "cmd", "main.go")); err == nil {
		buildTarget = "./cmd/"
	} else if info, err := os.Stat(filepath.Join(tmpDir, "cmd")); err == nil && info.IsDir() {
		buildTarget = "./cmd/"
	}

	// Build the binary.
	fmt.Fprintf(os.Stderr, "  go build -o %s %s\n", destPath, buildTarget)
	buildCmd := exec.Command("go", "build", "-o", destPath, buildTarget)
	buildCmd.Dir = tmpDir
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("go build: %w", err)
	}

	return nil
}

// queryManifest runs the binary with --manifest and parses its JSON output.
func queryManifest(binPath string) (*pluginManifest, error) {
	cmd := exec.Command(binPath, "--manifest")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("run --manifest: %w", err)
	}

	var m pluginManifest
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, fmt.Errorf("parse manifest JSON: %w", err)
	}
	return &m, nil
}

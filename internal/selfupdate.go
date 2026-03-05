package internal

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/cli/internal/inprocess"
	"github.com/orchestra-mcp/sdk-go/plugin"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	githubRepo  = "orchestra-mcp/framework"
	releasesURL = "https://api.github.com/repos/" + githubRepo + "/releases"
)

// orchestraBinaries lists all binaries shipped in a release tarball.
var orchestraBinaries = []string{
	"orchestra",
	"orchestrator",
	"storage-markdown",
	"storage-sqlite",
	"tools-features",
	"transport-stdio",
	"tools-marketplace",
}

// checkLatestVersion queries the GitHub API for the latest release tag
// (including prereleases). Returns the tag string or "" on error.
func checkLatestVersion() string {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(releasesURL)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var releases []struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return ""
	}
	if len(releases) == 0 {
		return ""
	}
	return releases[0].TagName
}

// isNewerVersion returns true if latest is strictly newer than current.
// Handles semver with optional prerelease suffix (e.g. "v0.0.3-beta").
func isNewerVersion(current, latest string) bool {
	curBase, curPre := splitVersion(current)
	latBase, latPre := splitVersion(latest)

	curParts := parseSemver(curBase)
	latParts := parseSemver(latBase)

	// Compare major.minor.patch numerically.
	for i := 0; i < 3; i++ {
		if latParts[i] > curParts[i] {
			return true
		}
		if latParts[i] < curParts[i] {
			return false
		}
	}

	// Same base version: release > prerelease.
	if curPre != "" && latPre == "" {
		return true // "v0.0.3" > "v0.0.3-beta"
	}
	if curPre == "" && latPre != "" {
		return false // "v0.0.3-beta" is not > "v0.0.3"
	}

	// Both have prerelease: compare lexicographically.
	return latPre > curPre
}

// splitVersion strips the "v" prefix and splits "0.0.3-beta" into ("0.0.3", "beta").
func splitVersion(v string) (base, pre string) {
	v = strings.TrimPrefix(v, "v")
	if idx := strings.IndexByte(v, '-'); idx != -1 {
		return v[:idx], v[idx+1:]
	}
	return v, ""
}

// parseSemver splits "0.0.3" into [0, 0, 3]. Returns [0,0,0] on parse errors.
func parseSemver(base string) [3]int {
	var parts [3]int
	for i, s := range strings.SplitN(base, ".", 3) {
		if i >= 3 {
			break
		}
		n, _ := strconv.Atoi(s)
		parts[i] = n
	}
	return parts
}

// runSelfUpdate checks for a newer version and updates all Orchestra binaries.
func runSelfUpdate() {
	fmt.Fprintf(os.Stderr, "Checking for updates...\n")

	latest := checkLatestVersion()
	if latest == "" {
		fmt.Fprintf(os.Stderr, "Could not check for updates.\n")
		fmt.Fprintf(os.Stderr, "Download manually: https://github.com/%s/releases\n", githubRepo)
		return
	}

	if !isNewerVersion(Version, latest) {
		fmt.Fprintf(os.Stderr, "Orchestra is up to date (%s)\n", Version)
		return
	}

	fmt.Fprintf(os.Stderr, "Updating orchestra %s → %s...\n\n", Version, latest)

	if err := selfUpdate(latest); err != nil {
		fatal("update failed: %v", err)
	}

	fmt.Fprintf(os.Stderr, "\nUpdated to %s! Run 'orchestra version' to verify.\n", latest)
}

// selfUpdate downloads the release tarball and replaces all binaries.
func selfUpdate(targetVersion string) error {
	// Find where the current binary lives.
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find executable: %w", err)
	}
	self, _ = filepath.EvalSymlinks(self)
	installDir := filepath.Dir(self)

	// Check if we can write to the install directory.
	testFile := filepath.Join(installDir, ".orchestra-update-test")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		os.Remove(testFile)
		return fmt.Errorf("cannot write to %s (try: sudo orchestra update)", installDir)
	}
	os.Remove(testFile)

	// Build download URL.
	tarName := fmt.Sprintf("orchestra-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", githubRepo, targetVersion, tarName)

	fmt.Fprintf(os.Stderr, "  Downloading %s...\n", tarName)

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: HTTP %d from %s", resp.StatusCode, url)
	}

	// Extract all binaries to a temp directory.
	tmpDir, err := os.MkdirTemp(installDir, ".orchestra-update-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := extractTarGzAll(resp.Body, tmpDir); err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	// Replace each binary atomically.
	ext := binaryExt()
	for _, name := range orchestraBinaries {
		srcPath := filepath.Join(tmpDir, name+ext)
		if _, err := os.Stat(srcPath); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "  [SKIP] %s (not in release)\n", name)
			continue
		}

		destPath := filepath.Join(installDir, name+ext)

		// Atomic replace: rename is atomic on same filesystem.
		if err := os.Rename(srcPath, destPath); err != nil {
			return fmt.Errorf("replace %s: %w", name, err)
		}
		if runtime.GOOS != "windows" {
			if err := os.Chmod(destPath, 0755); err != nil {
				return fmt.Errorf("chmod %s: %w", name, err)
			}
		}

		fmt.Fprintf(os.Stderr, "  [OK] %s\n", name+ext)
	}

	return nil
}

// extractTarGzAll extracts all regular files from a tar.gz stream into destDir.
func extractTarGzAll(r io.Reader, destDir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	// Build a set of known binaries for safety (with platform extension).
	ext := binaryExt()
	known := make(map[string]bool, len(orchestraBinaries))
	for _, name := range orchestraBinaries {
		known[name+ext] = true
	}

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}

		if header.Typeflag != tar.TypeReg {
			continue
		}

		baseName := filepath.Base(header.Name)
		if !known[baseName] {
			continue
		}

		outPath := filepath.Join(destDir, baseName)
		out, err := os.Create(outPath)
		if err != nil {
			return fmt.Errorf("create %s: %w", baseName, err)
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return fmt.Errorf("write %s: %w", baseName, err)
		}
		out.Close()
	}

	return nil
}

// CheckAndPromptUpdate checks for a newer version and prints an advisory.
// Used by orchestra init to inform the user without blocking.
func CheckAndPromptUpdate() {
	latest := checkLatestVersion()
	if latest == "" {
		return
	}
	if !isNewerVersion(Version, latest) {
		return
	}
	fmt.Fprintf(os.Stderr, "\n  Update available: %s (current: %s)\n", latest, Version)
	fmt.Fprintf(os.Stderr, "  Run 'orchestra update' to upgrade\n")
}

// ---------- MCP tools for AI-agent-driven updates ----------

// registerUpdateTools registers check_update and update_orchestra on the router.
func registerUpdateTools(router *inprocess.Router) {
	builder := plugin.New("tools.update")

	builder.RegisterTool("check_update",
		"Check if a newer version of Orchestra is available. Returns current version, latest version, and whether an update is available.",
		checkUpdateSchema(), checkUpdateHandler())

	builder.RegisterTool("update_orchestra",
		"Download and install the latest Orchestra release. After a successful update the IDE must be restarted for the new binaries to take effect.",
		updateOrchestraSchema(), updateOrchestraHandler())

	ep := builder.Export()
	router.RegisterPlugin(ep)
	log.Printf("[serve] tools.update initialized (%d tools)", len(ep.Tools))
}

func checkUpdateSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	})
	return s
}

func updateOrchestraSchema() *structpb.Struct {
	s, _ := structpb.NewStruct(map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	})
	return s
}

func checkUpdateHandler() plugin.ToolHandler {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		latest := checkLatestVersion()
		if latest == "" {
			return &pluginv1.ToolResponse{
				Success: true,
				Result: mustStruct(map[string]any{
					"current_version":  Version,
					"latest_version":   "",
					"update_available": false,
					"error":            "Could not reach GitHub API to check for updates.",
				}),
			}, nil
		}

		updateAvailable := isNewerVersion(Version, latest)
		result := map[string]any{
			"current_version":  Version,
			"latest_version":   latest,
			"update_available": updateAvailable,
		}
		if updateAvailable {
			result["message"] = fmt.Sprintf("Orchestra %s is available (you have %s). Call update_orchestra to install it.", latest, Version)
		} else {
			result["message"] = fmt.Sprintf("Orchestra is up to date (%s).", Version)
		}

		return &pluginv1.ToolResponse{
			Success: true,
			Result:  mustStruct(result),
		}, nil
	}
}

func updateOrchestraHandler() plugin.ToolHandler {
	return func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
		latest := checkLatestVersion()
		if latest == "" {
			return &pluginv1.ToolResponse{
				Success:   false,
				ErrorCode: "update_check_failed",
				Result: mustStruct(map[string]any{
					"error": "Could not reach GitHub API. Download manually: https://github.com/" + githubRepo + "/releases",
				}),
			}, nil
		}

		if !isNewerVersion(Version, latest) {
			return &pluginv1.ToolResponse{
				Success: true,
				Result: mustStruct(map[string]any{
					"message": fmt.Sprintf("Orchestra is already up to date (%s). No update needed.", Version),
				}),
			}, nil
		}

		if err := selfUpdate(latest); err != nil {
			return &pluginv1.ToolResponse{
				Success:   false,
				ErrorCode: "update_failed",
				Result: mustStruct(map[string]any{
					"error": fmt.Sprintf("Update failed: %v", err),
				}),
			}, nil
		}

		return &pluginv1.ToolResponse{
			Success: true,
			Result: mustStruct(map[string]any{
				"previous_version": Version,
				"new_version":      latest,
				"message":          fmt.Sprintf("Orchestra updated from %s to %s. Please ask the user to restart their IDE for the new version to take effect.", Version, latest),
				"restart_required": true,
			}),
		}, nil
	}
}

// mustStruct creates a structpb.Struct from a map, panicking on error (safe for
// known-good maps of primitives).
func mustStruct(m map[string]any) *structpb.Struct {
	s, _ := structpb.NewStruct(m)
	return s
}

// backgroundVersionCheck runs a single GitHub API check on startup and logs
// the result. This makes the update advisory visible in the log file without
// blocking the stdio transport.
func backgroundVersionCheck() {
	latest := checkLatestVersion()
	if latest == "" {
		return
	}
	if isNewerVersion(Version, latest) {
		log.Printf("[update] new version available: %s (current: %s) — use check_update / update_orchestra MCP tools or run 'orchestra update'", latest, Version)
	}
}

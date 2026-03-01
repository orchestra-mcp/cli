package internal

import (
	"fmt"
	"os"
	"strings"
)

// RunPlugins handles `orchestra plugins` -- lists all installed third-party plugins.
func RunPlugins(args []string) {
	reg, err := LoadRegistry()
	if err != nil {
		fatal("load registry: %v", err)
	}

	if len(reg.Plugins) == 0 {
		fmt.Fprintf(os.Stderr, "No plugins installed.\n")
		fmt.Fprintf(os.Stderr, "  Install with: orchestra plugin install <repo>\n")
		return
	}

	fmt.Fprintf(os.Stderr, "Installed plugins:\n")
	for _, p := range reg.Plugins {
		status := "enabled"
		if !p.Enabled {
			status = "disabled"
		}

		var caps []string
		if n := len(p.ProvidesTools); n > 0 {
			caps = append(caps, fmt.Sprintf("%d tools", n))
		}
		if len(p.ProvidesAI) > 0 {
			caps = append(caps, fmt.Sprintf("ai:%s", strings.Join(p.ProvidesAI, ",")))
		}
		capStr := ""
		if len(caps) > 0 {
			capStr = "  (" + strings.Join(caps, ", ") + ")"
		}

		fmt.Fprintf(os.Stderr, "  %-28s %-10s %-10s %s%s\n", p.ID, p.Version, status, p.Repo, capStr)
	}
}

// RunUninstall handles `orchestra uninstall <plugin-id-or-repo>`.
func RunUninstall(args []string) {
	if len(args) < 1 {
		fatal("usage: orchestra uninstall <plugin-id-or-repo>")
	}
	target := args[0]

	reg, err := LoadRegistry()
	if err != nil {
		fatal("load registry: %v", err)
	}

	// Find by repo URL first, then by plugin ID.
	repoKey := ""
	var entry *PluginEntry
	if p, ok := reg.Plugins[target]; ok {
		repoKey = target
		entry = p
	} else {
		for k, p := range reg.Plugins {
			if p.ID == target {
				repoKey = k
				entry = p
				break
			}
		}
	}

	if entry == nil {
		fatal("plugin not found: %s", target)
	}

	// Delete binary.
	if err := os.Remove(entry.Binary); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "  Warning: could not remove binary %s: %v\n", entry.Binary, err)
	}

	// Remove from registry.
	delete(reg.Plugins, repoKey)
	if err := SaveRegistry(reg); err != nil {
		fatal("save registry: %v", err)
	}

	fmt.Fprintf(os.Stderr, "Uninstalled %s (%s)\n", entry.ID, entry.Repo)
}

// RunUpdate handles `orchestra update` (self-update) or `orchestra update <plugin>`.
func RunUpdate(args []string) {
	if len(args) < 1 {
		// No args = self-update Orchestra.
		runSelfUpdate()
		return
	}
	target := args[0]

	reg, err := LoadRegistry()
	if err != nil {
		fatal("load registry: %v", err)
	}

	// Find by repo URL first, then by plugin ID.
	var entry *PluginEntry
	if p, ok := reg.Plugins[target]; ok {
		entry = p
	} else {
		for _, p := range reg.Plugins {
			if p.ID == target {
				entry = p
				break
			}
		}
	}

	if entry == nil {
		fatal("plugin not found: %s", target)
	}

	fmt.Fprintf(os.Stderr, "Updating %s (%s)...\n", entry.ID, entry.Repo)

	// Re-run install with the same repo. This will overwrite the binary and
	// update the registry entry. Pass the repo without a version tag so it
	// fetches the latest.
	RunInstall([]string{entry.Repo})
}

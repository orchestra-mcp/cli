package internal

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// PluginEntry describes a single installed plugin.
type PluginEntry struct {
	ID              string   `json:"id"`
	Version         string   `json:"version"`
	Binary          string   `json:"binary"`
	Repo            string   `json:"repo"`
	InstalledAt     string   `json:"installed_at"`
	ProvidesTools   []string `json:"provides_tools"`
	ProvidesStorage []string `json:"provides_storage"`
	NeedsStorage    []string `json:"needs_storage"`

	// Extended fields for selective plugin loading.
	Enabled         bool     `json:"enabled"`
	ProvidesAI      []string `json:"provides_ai,omitempty"`
	NeedsAI         []string `json:"needs_ai,omitempty"`
	NeedsTools      []string `json:"needs_tools,omitempty"`
	ProvidesPrompts []string `json:"provides_prompts,omitempty"`
	Description     string   `json:"description,omitempty"`
	Author          string   `json:"author,omitempty"`
	Platform        string   `json:"platform,omitempty"`
	UpdatedAt       string   `json:"updated_at,omitempty"`
}

// PluginRegistry holds all installed third-party plugins, keyed by repo URL.
type PluginRegistry struct {
	Plugins map[string]*PluginEntry `json:"plugins"`
}

// registryDir returns the directory for plugin data: ~/.orchestra/plugins/
func registryDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".orchestra", "plugins")
}

// registryPath returns the path to the registry file: ~/.orchestra/plugins/registry.json
func registryPath() string {
	return filepath.Join(registryDir(), "registry.json")
}

// pluginBinDir returns the directory where plugin binaries are installed: ~/.orchestra/plugins/bin/
func pluginBinDir() string {
	return filepath.Join(registryDir(), "bin")
}

// LoadRegistry reads the registry from disk. Returns an empty registry if the file
// does not exist.
func LoadRegistry() (*PluginRegistry, error) {
	data, err := os.ReadFile(registryPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &PluginRegistry{Plugins: make(map[string]*PluginEntry)}, nil
		}
		return nil, err
	}

	var reg PluginRegistry
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, err
	}
	if reg.Plugins == nil {
		reg.Plugins = make(map[string]*PluginEntry)
	}
	return &reg, nil
}

// SaveRegistry writes the registry to disk, creating directories as needed.
func SaveRegistry(reg *PluginRegistry) error {
	if err := os.MkdirAll(registryDir(), 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(registryPath(), data, 0644)
}

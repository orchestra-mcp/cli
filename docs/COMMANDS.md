# Commands

## `orchestra serve`

Start the MCP stdio server. This is the default command -- running `orchestra` with no subcommand is equivalent to `orchestra serve`.

The serve command:
1. Locates sibling binaries (orchestrator, storage-markdown, tools-features, transport-stdio) next to the `orchestra` binary.
2. Generates a temporary `plugins.yaml` config.
3. Starts the orchestrator as a subprocess.
4. Waits for all plugins to register and boot (up to 15 seconds).
5. Starts transport-stdio with stdin/stdout passthrough.
6. On exit, kills all child processes and cleans up.

```bash
orchestra serve [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--workspace=DIR` | `.` (current directory) | Project workspace directory |
| `--certs-dir=DIR` | `~/.orchestra/certs` | mTLS certificates directory |
| `--log=FILE` | `<workspace>/.orchestra-mcp.log` | Log file path |

Third-party plugins from the registry (`~/.orchestra/plugins/registry.json`) are automatically included.

---

## `orchestra init`

Initialize MCP configuration files for your IDE(s). Generates the appropriate JSON/TOML/YAML config so the IDE knows how to start Orchestra as an MCP server.

```bash
orchestra init [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--workspace=DIR` | `.` (current directory) | Project directory to initialize |
| `--ide=NAME` | (auto-detect) | Target IDE (comma-separated for multiple) |
| `--all` | false | Generate configs for all 9 supported IDEs |

### Supported IDEs

| Name | Config File | Format |
|---|---|---|
| `claude` | `.mcp.json` | JSON (`mcpServers`) |
| `cursor` | `.cursor/mcp.json` | JSON (`mcpServers`) |
| `vscode` | `.vscode/mcp.json` | JSON (`mcpServers`) |
| `cline` | `.vscode/mcp.json` | JSON (`mcpServers`) |
| `windsurf` | `~/.codeium/windsurf/mcp_config.json` | JSON (`mcpServers`) |
| `codex` | `.codex/config.toml` | TOML |
| `gemini` | `.gemini/settings.json` | JSON (`mcpServers`) |
| `zed` | `.zed/settings.json` | JSON (`context_servers`) |
| `continue` | `.continue/mcpServers/orchestra.yaml` | YAML |

### Auto-detection

If `--ide` is not specified, init checks for existing IDE config directories (`.cursor/`, `.vscode/`, `.zed/`, etc.) and generates configs for detected IDEs. Falls back to `claude` if none detected.

### Examples

```bash
# Auto-detect IDEs in the current project
orchestra init

# Specific IDE
orchestra init --ide=cursor

# Multiple IDEs
orchestra init --ide=claude,cursor,vscode

# All IDEs
orchestra init --all

# Different workspace
orchestra init --workspace=/path/to/project --ide=claude
```

### Project Name Detection

The init command detects the project name from (in order):
1. `package.json` (`name` field)
2. `go.mod` (module path, last segment)
3. `Cargo.toml` (`name` field)
4. `pyproject.toml` (`name` field)
5. Directory name (fallback)

---

## `orchestra install`

Install a third-party plugin from a GitHub repository.

```bash
orchestra install <repo>[@version] [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--source` | false | Force build from source (skip binary download) |
| `--binary` | false | Force binary download (fail if unavailable) |

### Install Strategy

1. **Binary download** (default first attempt): Downloads a pre-built binary from GitHub Releases. Looks for `{name}-{os}-{arch}.tar.gz` (e.g., `my-plugin-darwin-arm64.tar.gz`).
2. **Source build** (fallback): Clones the repo, runs `go build`. Requires `git` and `go` in PATH.

### Manifest Query

After installation, the CLI runs `<binary> --manifest` to discover the plugin's ID, provided tools, and storage types. This information is stored in the registry.

### Examples

```bash
# Install latest version
orchestra install github.com/someone/my-plugin

# Install specific version
orchestra install github.com/someone/my-plugin@v1.2.0

# Force source build
orchestra install github.com/someone/my-plugin --source

# Force binary download (fail if no release)
orchestra install github.com/someone/my-plugin --binary
```

### Registry

Installed plugins are tracked in `~/.orchestra/plugins/registry.json`. Binaries are placed in `~/.orchestra/plugins/bin/`.

---

## `orchestra plugins`

List all installed third-party plugins.

```bash
orchestra plugins
```

Output shows plugin ID, version, repository URL, and capability summary.

---

## `orchestra uninstall`

Remove an installed plugin.

```bash
orchestra uninstall <plugin-id-or-repo>
```

Removes the binary from disk and the entry from the registry. Accepts either the plugin ID or the full repo URL.

### Examples

```bash
orchestra uninstall my-plugin
orchestra uninstall github.com/someone/my-plugin
```

---

## `orchestra update`

Update an installed plugin to the latest version.

```bash
orchestra update <plugin-id-or-repo>
```

Re-runs the install process for the plugin's repo without a version tag, fetching the latest release or source.

---

## `orchestra version`

Print version information.

```bash
orchestra version
```

Output: `orchestra <version> (<os>/<arch>, commit <hash>, built <date>)`

---

## `orchestra help`

Show usage help with all commands and flags.

```bash
orchestra help
```

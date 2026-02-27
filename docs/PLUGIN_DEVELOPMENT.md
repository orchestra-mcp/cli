# Plugin Development

This guide covers how to create plugins that are installable via `orchestra install`.

## Requirements

For a plugin to be installable, it must:

1. **Be a Go module** hosted on GitHub (e.g., `github.com/my-org/my-plugin`).
2. **Build to a single binary** with an entry point at `cmd/main.go` or the module root.
3. **Support the `--manifest` flag** that prints plugin metadata as JSON to stdout and exits.
4. **Print `READY <addr>` to stderr** when the QUIC server is listening.
5. **Accept standard plugin flags**: `--orchestrator-addr`, `--listen-addr`, `--certs-dir`.

All of these are handled automatically by the SDK if you use `plugin.New(...).BuildWithTools()` and call `p.ParseFlags()`.

## Plugin Structure

```
my-plugin/
  go.mod                        # module github.com/my-org/my-plugin
  cmd/
    main.go                     # Entry point
  internal/
    tools/
      my_tool.go                # Tool implementations
```

## Manifest Format

When `--manifest` is passed, the binary must print JSON to stdout and exit:

```json
{
  "id": "tools.greeting",
  "version": "0.1.0",
  "description": "A greeting plugin",
  "provides_tools": ["greet", "farewell"],
  "provides_storage": [],
  "needs_storage": ["markdown"]
}
```

The `orchestra install` command uses this to register the plugin's capabilities.

## Distribution

### Option A: Pre-built Binaries (Recommended)

Create GitHub Releases with platform-specific tarballs:

```
my-plugin-darwin-arm64.tar.gz
my-plugin-darwin-amd64.tar.gz
my-plugin-linux-amd64.tar.gz
my-plugin-linux-arm64.tar.gz
my-plugin-windows-amd64.tar.gz
```

Each tarball should contain the plugin binary at the root level. The binary name must match the repository name.

Use GitHub Actions or GoReleaser to automate this.

### Option B: Source Build

If no release binary is available, `orchestra install` falls back to cloning the repo and running `go build`. The build target is auto-detected:

1. `cmd/main.go` exists -> build `./cmd/`
2. `cmd/` directory exists -> build `./cmd/`
3. Otherwise -> build `./`

## Installation Flow

When a user runs `orchestra install github.com/my-org/my-plugin`:

1. Attempt to download `my-plugin-{os}-{arch}.tar.gz` from GitHub Releases.
2. If download fails (and `--binary` not set), clone the repo and `go build`.
3. Place the binary in `~/.orchestra/plugins/bin/my-plugin`.
4. Run `my-plugin --manifest` to discover capabilities.
5. Register in `~/.orchestra/plugins/registry.json`.

## Integration with `orchestra serve`

When `orchestra serve` starts, it:

1. Loads the plugin registry from `~/.orchestra/plugins/registry.json`.
2. Adds each registered plugin to the orchestrator's `plugins.yaml` config.
3. The orchestrator starts the plugin binary with standard flags.
4. The plugin's tools become available through MCP.

## Testing Your Plugin

Test that your plugin works with Orchestra end-to-end:

```bash
# 1. Build it
go build -o my-plugin ./cmd/

# 2. Verify manifest
./my-plugin --manifest

# 3. Install locally (from source)
orchestra install github.com/my-org/my-plugin --source

# 4. Verify it appears in the plugin list
orchestra plugins

# 5. Start Orchestra and test via MCP
orchestra serve
```

## Example: Minimal Plugin

`go.mod`:
```
module github.com/my-org/my-plugin

go 1.23

require (
    github.com/orchestra-mcp/sdk-go v0.1.0
    github.com/orchestra-mcp/gen-go v0.1.0
)
```

`cmd/main.go`:
```go
package main

import (
    "context"
    "log"
    "os"
    "os/signal"
    "syscall"

    pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
    "github.com/orchestra-mcp/sdk-go/helpers"
    "github.com/orchestra-mcp/sdk-go/plugin"
    "google.golang.org/protobuf/types/known/structpb"
)

func main() {
    schema, _ := structpb.NewStruct(map[string]any{
        "type": "object",
        "properties": map[string]any{
            "name": map[string]any{"type": "string", "description": "Who to greet"},
        },
        "required": []any{"name"},
    })

    greet := func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
        name := helpers.GetString(req.Arguments, "name")
        return helpers.TextResult("Hello, " + name + "!"), nil
    }

    p := plugin.New("tools.greet").
        Version("0.1.0").
        Description("Greeting plugin").
        RegisterTool("greet", "Greet someone", schema, greet).
        BuildWithTools()

    p.ParseFlags()

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
    go func() { <-sigCh; cancel() }()

    if err := p.Run(ctx); err != nil {
        log.Fatal(err)
    }
}
```

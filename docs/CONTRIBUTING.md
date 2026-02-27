# Contributing to cli

## Prerequisites

- Go 1.23+
- `gofmt`, `go vet`

## Development Setup

```bash
git clone https://github.com/orchestra-mcp/cli.git
cd cli
go mod download
go build -o orchestra .
```

## Building

```bash
# Development build
go build -o orchestra .

# Release build with version info
go build -ldflags "-X github.com/orchestra-mcp/cli/internal.Version=1.0.0 -X github.com/orchestra-mcp/cli/internal.Commit=$(git rev-parse --short HEAD) -X github.com/orchestra-mcp/cli/internal.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o orchestra .
```

## Testing

```bash
go test ./...
```

## Code Organization

```
cli/
  main.go                       # Subcommand dispatch
  internal/
    initcmd.go                  # orchestra init
    serve.go                    # orchestra serve
    install.go                  # orchestra install (binary download + source build)
    plugins.go                  # orchestra plugins, uninstall, update
    registry.go                 # Plugin registry (load/save ~/.orchestra/plugins/registry.json)
    ide.go                      # IDE config generators (9 IDEs)
    detect.go                   # Project name and IDE auto-detection
    version.go                  # Version info
```

## Adding a New IDE

1. Add a new function in `internal/ide.go` that returns an `*IDEConfig`.
2. Register it in the `ideRegistry` map.
3. Add it to the `allIDENames()` slice.
4. Update the `--ide` flag description in `initcmd.go` and `main.go`.
5. Update `docs/COMMANDS.md`.

## Adding a New Command

1. Create a new file in `internal/` (e.g., `mycommand.go`) with a `RunMyCommand(args []string)` function.
2. Add the case to the switch in `main.go`.
3. Update the `printUsage()` function.
4. Update `docs/COMMANDS.md`.

## Code Style

- Run `gofmt` on all files.
- Run `go vet ./...` before committing.
- All exported functions and types must have doc comments.
- Error output goes to stderr (`fmt.Fprintf(os.Stderr, ...)`).
- The `fatal()` helper prints to stderr and exits with code 1.
- Subcommands use `flag.NewFlagSet` for independent flag parsing.

## Pull Request Process

1. Fork the repository and create a feature branch from `main`.
2. Write or update tests for your changes.
3. Run `go test ./...` and `go vet ./...`.
4. Update `docs/COMMANDS.md` if adding or changing commands.
5. Update `docs/PLUGIN_DEVELOPMENT.md` if changing the plugin install flow.

## Related Repositories

- [orchestra-mcp/proto](https://github.com/orchestra-mcp/proto) -- Protobuf schema
- [orchestra-mcp/sdk-go](https://github.com/orchestra-mcp/sdk-go) -- Go Plugin SDK
- [orchestra-mcp/orchestrator](https://github.com/orchestra-mcp/orchestrator) -- Central hub
- [orchestra-mcp](https://github.com/orchestra-mcp) -- Organization home

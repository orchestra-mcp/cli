# Orchestra CLI

Command-line interface for initializing, configuring, and running Orchestra MCP.

## Install

```bash
go install github.com/orchestra-mcp/cli@latest
```

This installs the `orchestra-mcp` binary.

## Commands

```bash
orchestra-mcp init                  # Initialize Orchestra in current project
orchestra-mcp serve                 # Start MCP stdio server
orchestra-mcp install               # Download plugin binaries
orchestra-mcp plugins               # List installed plugins
orchestra-mcp update                # Update plugins to latest versions
orchestra-mcp uninstall             # Remove Orchestra from project
orchestra-mcp version               # Print version info
```

## Quick Start

```bash
# Initialize in your project directory
cd my-project
orchestra-mcp init

# Start the MCP server (used by IDE integrations)
orchestra-mcp serve --workspace .
```

## IDE Support

The `init` command detects and configures 9 IDEs automatically:

- Claude Code, Cursor, Windsurf, VS Code (Copilot), Zed
- Cline (VS Code), Roo Code (VS Code), Continue (VS Code/JetBrains)
- JetBrains IDEs (IntelliJ, GoLand, WebStorm, etc.)

## Architecture

The CLI is a standalone binary with no dependency on the SDK or Protobuf. It locates plugin binaries, starts the orchestrator, and connects the transport layer to your IDE.

```
IDE <-- stdin/stdout --> orchestra-mcp serve
                              |
                         orchestrator
                        /     |      \
                   storage  tools  transport
```

## Related Packages

| Package | Description |
|---------|-------------|
| [orchestrator](https://github.com/orchestra-mcp/orchestrator) | Central hub started by `serve` |
| [plugin-transport-stdio](https://github.com/orchestra-mcp/plugin-transport-stdio) | MCP JSON-RPC bridge |
| [plugin-tools-features](https://github.com/orchestra-mcp/plugin-tools-features) | Feature workflow tools |
| [plugin-storage-markdown](https://github.com/orchestra-mcp/plugin-storage-markdown) | File storage backend |

## License

[MIT](LICENSE)

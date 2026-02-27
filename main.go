package main

import (
	"fmt"
	"os"

	"github.com/orchestra-mcp/cli/internal"
)

func main() {
	if len(os.Args) < 2 {
		// No subcommand = default to serve (MCP clients call "command": "orchestra")
		internal.RunServe(os.Args[1:])
		return
	}

	switch os.Args[1] {
	case "init":
		internal.RunInit(os.Args[2:])
	case "serve", "start":
		internal.RunServe(os.Args[2:])
	case "install":
		internal.RunInstall(os.Args[2:])
	case "plugins":
		internal.RunPlugins(os.Args[2:])
	case "uninstall", "remove":
		internal.RunUninstall(os.Args[2:])
	case "update", "upgrade":
		internal.RunUpdate(os.Args[2:])
	case "version", "--version", "-v":
		internal.RunVersion()
	case "help", "--help", "-h":
		printUsage()
	default:
		// Unknown subcommand — treat all args as serve flags
		internal.RunServe(os.Args[1:])
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `orchestra — AI-agentic project management via MCP

Usage:
  orchestra serve        Start the MCP stdio server (default)
  orchestra init         Initialize MCP configs for your IDE(s)
  orchestra install      Install a plugin from a GitHub repo
  orchestra plugins      List installed plugins
  orchestra uninstall    Remove an installed plugin
  orchestra update       Update an installed plugin to latest
  orchestra version      Print version info
  orchestra help         Show this help

Serve flags:
  --workspace=DIR   Project workspace directory (default: current directory)
  --certs-dir=DIR   mTLS certificates directory (default: ~/.orchestra/certs)
  --log=FILE        Log file path (default: .orchestra-mcp.log)

Init flags:
  --workspace=DIR   Project directory to initialize (default: current directory)
  --ide=NAME        Target IDE: claude, cursor, vscode, windsurf, codex, gemini, zed, continue, cline
  --all             Generate configs for all supported IDEs

Install flags:
  --source          Force build from source (skip binary download)
  --binary          Force binary download (fail if unavailable)
  --dev             Clone full repo into libs/ for development

Examples:
  orchestra install github.com/someone/my-plugin
  orchestra install github.com/someone/my-plugin@v1.2.0
  orchestra install github.com/someone/my-plugin --source
  orchestra install github.com/orchestra-mcp/sdk-go --dev
  orchestra uninstall my-plugin
  orchestra update my-plugin
`)
}

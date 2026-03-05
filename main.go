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
	case "stop":
		internal.RunStop(os.Args[2:])
	case "restart":
		internal.RunRestart(os.Args[2:])
	case "plugin":
		internal.RunPlugin(os.Args[2:])
	case "install":
		internal.RunInstall(os.Args[2:])
	case "plugins":
		internal.RunPlugins(os.Args[2:])
	case "pack":
		internal.RunPack(os.Args[2:])
	case "uninstall", "remove":
		internal.RunUninstall(os.Args[2:])
	case "update", "upgrade":
		internal.RunUpdate(os.Args[2:])
	case "log", "logs":
		internal.RunLog(os.Args[2:])
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
  orchestra stop         Stop a running orchestra instance
  orchestra restart      Restart orchestra (stop + start)
  orchestra log          Tail the latest orchestra log file
  orchestra init         Initialize MCP configs for your IDE(s)
  orchestra plugin       Manage external plugins (install/remove/enable/disable)
  orchestra pack         Manage content packs (skills, agents, hooks)
  orchestra install      Install a plugin from a GitHub repo
  orchestra plugins      List installed plugins
  orchestra uninstall    Remove an installed plugin
  orchestra update       Update Orchestra to latest version
  orchestra version      Print version info
  orchestra help         Show this help

Plugin commands:
  orchestra plugin install <repo>[@version]   Install a plugin binary
  orchestra plugin remove <name>              Remove an installed plugin
  orchestra plugin list                       List installed plugins
  orchestra plugin enable <name>              Enable a disabled plugin
  orchestra plugin disable <name>             Disable a plugin (keep binary)
  orchestra plugin search <query>             Search available plugins
  orchestra plugin update [name]              Update one or all plugins
  orchestra plugin info <name>                Show plugin details

Serve flags:
  --workspace=DIR   Project workspace directory (default: current directory)
  --tcp-addr=ADDR   TCP address for desktop apps (default: localhost:50101)
  --log=FILE        Log file path (default: OS log directory)
  --no-plugins      Skip loading external plugins (core-only mode)

Stop/Restart flags:
  --workspace=DIR   Project workspace directory (default: current directory)
  --tcp-addr=ADDR   TCP address (restart only, default: localhost:50101)
  --log=FILE        Log file path (restart only)
  --no-plugins      Skip external plugins (restart only)

Log flags:
  -n=NUM            Number of lines to show (default: 50)
  -f                Follow log output, tail -f style (default: true)
  --workspace=DIR   Show log for specific workspace

Init flags:
  --workspace=DIR   Project directory to initialize (default: current directory)
  --ide=NAME        Target IDE: claude, cursor, vscode, windsurf, codex, gemini, zed, continue, cline
  --all             Generate configs for all supported IDEs

Examples:
  orchestra plugin install github.com/orchestra-mcp/plugin-devtools-git
  orchestra plugin search devtools
  orchestra plugin disable bridge.openai
  orchestra plugin enable bridge.openai
  orchestra serve --no-plugins
`)
}

#!/usr/bin/env node
import { Command } from 'commander';
import chalk from 'chalk';
import { welcomeCommand } from './commands/welcome.jsx';
import { pluginCommand } from './commands/plugin.jsx';
import { themeCommand } from './commands/theme.jsx';
import { storybookCommand } from './commands/storybook.js';

const program = new Command();

program
  .name('orchestra')
  .description(chalk.cyan('Orchestra MCP - Interactive CLI with rich TUI'))
  .version('0.1.0');

// Register commands
program
  .command('welcome')
  .description('Show interactive welcome screen with ASCII art')
  .action(welcomeCommand);

program
  .command('plugin')
  .description('Browse and manage plugins (TUI browser)')
  .action(pluginCommand);

program
  .command('theme')
  .description('Browse and switch themes (TUI switcher)')
  .action(themeCommand);

program
  .command('storybook')
  .description('Start Storybook development server')
  .option('-p, --port <port>', 'Port to run Storybook on', '6006')
  .option('--no-open', 'Do not open browser automatically')
  .action(storybookCommand);

// Default to welcome screen if no command
if (process.argv.length === 2) {
  welcomeCommand();
} else {
  program.parse();
}

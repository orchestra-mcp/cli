import { spawn } from 'child_process';
import chalk from 'chalk';
import ora from 'ora';

interface StorybookOptions {
  port: string;
  open: boolean;
}

export async function storybookCommand(options: StorybookOptions) {
  const spinner = ora({
    text: chalk.cyan('Starting Storybook...'),
    color: 'cyan',
  }).start();

  const args = [
    'storybook',
    '--port',
    options.port,
  ];

  if (!options.open) {
    args.push('--no-open');
  }

  const storybook = spawn('pnpm', args, {
    stdio: 'inherit',
    shell: true,
  });

  storybook.on('spawn', () => {
    spinner.succeed(chalk.green(`Storybook started on port ${options.port}`));
  });

  storybook.on('error', (err) => {
    spinner.fail(chalk.red('Failed to start Storybook'));
    console.error(err);
    process.exit(1);
  });

  storybook.on('exit', (code) => {
    if (code !== 0) {
      spinner.fail(chalk.red(`Storybook exited with code ${code}`));
      process.exit(code || 1);
    }
  });
}

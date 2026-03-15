"use client";

import React, { useState, useEffect } from 'react';
import { Box, Text, useInput, useApp } from 'ink';
import Spinner from 'ink-spinner';
import chalk from 'chalk';

const ASCII_ART = `
   ____           _               _
  / __ \\         | |             | |
 | |  | |_ __ ___| |__   ___  ___| |_ _ __ __ _
 | |  | | '__/ __| '_ \\ / _ \\/ __| __| '__/ _\` |
 | |__| | | | (__| | | |  __/\\__ \\ |_| | | (_| |
  \\____/|_|  \\___|_| |_|\\___||___/\\__|_|  \\__,_|

          MCP Framework - Interactive CLI
`;

export const WelcomeScreen: React.FC = () => {
  const { exit } = useApp();
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    const timer = setTimeout(() => setLoading(false), 1000);
    return () => clearTimeout(timer);
  }, []);

  useInput((input, key) => {
    if (input === 'q' || key.escape) {
      exit();
    }
  });

  return (
    <Box flexDirection="column" padding={1}>
      <Text color="cyan">{ASCII_ART}</Text>

      <Box marginTop={1}>
        {loading ? (
          <Box>
            <Text color="cyan">
              <Spinner type="dots" />
            </Text>
            <Text> Loading...</Text>
          </Box>
        ) : (
          <Box flexDirection="column">
            <Text bold color="green">✓ Welcome to Orchestra MCP!</Text>
            <Box marginTop={1} flexDirection="column">
              <Text dimColor>Available Commands:</Text>
              <Text>  {chalk.cyan('orchestra plugin')}   - Browse and manage plugins</Text>
              <Text>  {chalk.cyan('orchestra theme')}    - Switch color themes</Text>
              <Text>  {chalk.cyan('orchestra storybook')} - Start Storybook</Text>
            </Box>
            <Box marginTop={1}>
              <Text dimColor>Press {chalk.bold('q')} or {chalk.bold('ESC')} to exit</Text>
            </Box>
          </Box>
        )}
      </Box>
    </Box>
  );
};

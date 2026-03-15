"use client";

import React, { useState } from 'react';
import { Box, Text, useInput, useApp } from 'ink';
import SelectInput from 'ink-select-input';
import chalk from 'chalk';

interface Plugin {
  label: string;
  value: string;
  description: string;
  enabled: boolean;
}

const MOCK_PLUGINS: Plugin[] = [
  {
    label: '🔌 MCP Plugin',
    value: 'mcp',
    description: 'Orchestra MCP tools and workflow management',
    enabled: true,
  },
  {
    label: '🖥️  Desktop Plugin',
    value: 'desktop',
    description: 'Wails v3 desktop application',
    enabled: true,
  },
  {
    label: '📊 Tray Plugin',
    value: 'tray',
    description: 'System tray integration',
    enabled: true,
  },
  {
    label: '🪟  Panels Plugin',
    value: 'panels',
    description: 'Multi-window panel management',
    enabled: true,
  },
  {
    label: '💬 Discord Plugin',
    value: 'discord',
    description: 'Discord notifications and hooks',
    enabled: false,
  },
];

export const PluginBrowser: React.FC = () => {
  const { exit } = useApp();
  const [selectedPlugin, setSelectedPlugin] = useState<Plugin | null>(null);

  useInput((input, key) => {
    if (input === 'q' || key.escape) {
      exit();
    }
  });

  const handleSelect = (item: any) => {
    const plugin = MOCK_PLUGINS.find(p => p.value === item.value);
    if (plugin) {
      setSelectedPlugin(plugin);
    }
  };

  return (
    <Box flexDirection="column" padding={1}>
      <Text bold color="cyan">🔌 Plugin Browser</Text>
      <Text dimColor>Use ↑↓ arrows to navigate, Enter to select, q/ESC to exit</Text>

      <Box marginTop={1} flexDirection="column">
        {!selectedPlugin ? (
          <SelectInput
            items={MOCK_PLUGINS.map(p => ({
              label: `${p.label} ${p.enabled ? chalk.green('✓') : chalk.dim('○')}`,
              value: p.value,
            }))}
            onSelect={handleSelect}
          />
        ) : (
          <Box flexDirection="column">
            <Text bold>{selectedPlugin.label}</Text>
            <Box marginTop={1}>
              <Text>{selectedPlugin.description}</Text>
            </Box>
            <Box marginTop={1}>
              <Text>
                Status: {selectedPlugin.enabled ?
                  chalk.green('Enabled') :
                  chalk.yellow('Disabled')}
              </Text>
            </Box>
            <Box marginTop={1}>
              <Text dimColor>Press any key to go back...</Text>
            </Box>
          </Box>
        )}
      </Box>
    </Box>
  );
};

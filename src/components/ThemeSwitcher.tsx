"use client";

import React, { useState } from 'react';
import { Box, Text, useInput, useApp } from 'ink';
import SelectInput from 'ink-select-input';
import chalk from 'chalk';

interface Theme {
  label: string;
  value: string;
  preview: string;
}

const THEMES: Theme[] = [
  { label: 'Orchestra', value: 'orchestra', preview: '🎨 Default Orchestra theme' },
  { label: 'Material Dark', value: 'material-dark', preview: '🌙 Dark Material Design' },
  { label: 'Material Light', value: 'material-light', preview: '☀️  Light Material Design' },
  { label: 'Nord', value: 'nord', preview: '❄️  Arctic-inspired palette' },
  { label: 'Dracula', value: 'dracula', preview: '🧛 Dark purple theme' },
  { label: 'GitHub Dark', value: 'github-dark', preview: '🐙 GitHub dark mode' },
  { label: 'GitHub Light', value: 'github-light', preview: '🐙 GitHub light mode' },
  { label: 'Matrix', value: 'matrix', preview: '💚 Green monochrome' },
  { label: 'Monokai', value: 'monokai', preview: '🎨 Classic Monokai' },
  { label: 'Solarized Dark', value: 'solarized-dark', preview: '🌊 Solarized dark' },
];

export const ThemeSwitcher: React.FC = () => {
  const { exit } = useApp();
  const [currentTheme, setCurrentTheme] = useState('orchestra');
  const [preview, setPreview] = useState<string | null>(null);

  useInput((input, key) => {
    if (input === 'q' || key.escape) {
      exit();
    }
  });

  const handleSelect = (item: any) => {
    setCurrentTheme(item.value);
    const theme = THEMES.find(t => t.value === item.value);
    if (theme) {
      setPreview(theme.preview);
    }
  };

  const handleHighlight = (item: any) => {
    const theme = THEMES.find(t => t.value === item.value);
    if (theme) {
      setPreview(theme.preview);
    }
  };

  return (
    <Box flexDirection="column" padding={1}>
      <Text bold color="cyan">🎨 Theme Switcher</Text>
      <Text dimColor>Use ↑↓ arrows to navigate, Enter to select, q/ESC to exit</Text>

      <Box marginTop={1} flexDirection="row">
        <Box flexDirection="column" flexGrow={1}>
          <SelectInput
            items={THEMES.map(t => ({
              label: `${t.label} ${currentTheme === t.value ? chalk.green('●') : '○'}`,
              value: t.value,
            }))}
            onSelect={handleSelect}
            onHighlight={handleHighlight}
          />
        </Box>

        {preview && (
          <Box marginLeft={2} flexDirection="column" borderStyle="round" borderColor="cyan" padding={1}>
            <Text bold>Preview</Text>
            <Box marginTop={1}>
              <Text>{preview}</Text>
            </Box>
          </Box>
        )}
      </Box>

      <Box marginTop={1}>
        <Text dimColor>
          Current: {chalk.cyan(THEMES.find(t => t.value === currentTheme)?.label || 'Orchestra')}
        </Text>
      </Box>
    </Box>
  );
};

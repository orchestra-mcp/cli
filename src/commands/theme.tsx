import React from 'react';
import { render } from 'ink';
import { ThemeSwitcher } from '../components/ThemeSwitcher.jsx';

export async function themeCommand() {
  const { waitUntilExit } = render(<ThemeSwitcher />);
  await waitUntilExit();
}

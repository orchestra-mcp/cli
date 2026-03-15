import React from 'react';
import { render } from 'ink';
import { PluginBrowser } from '../components/PluginBrowser.jsx';

export async function pluginCommand() {
  const { waitUntilExit } = render(<PluginBrowser />);
  await waitUntilExit();
}

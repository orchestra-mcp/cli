import React from 'react';
import { render } from 'ink';
import { WelcomeScreen } from '../components/WelcomeScreen.jsx';

export async function welcomeCommand() {
  const { waitUntilExit } = render(<WelcomeScreen />);
  await waitUntilExit();
}

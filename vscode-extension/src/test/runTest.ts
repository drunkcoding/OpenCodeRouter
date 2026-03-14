import * as fs from 'node:fs/promises';
import * as os from 'node:os';
import * as path from 'node:path';

import { runTests } from '@vscode/test-electron';

async function main(): Promise<void> {
  const extensionDevelopmentPath = path.resolve(__dirname, '../../');
  const extensionTestsPath = path.resolve(__dirname, './suite/index');
  const workspaceDir = await fs.mkdtemp(path.join(os.tmpdir(), 'opencode-router-vscode-tests-'));

  try {
    await runTests({
      extensionDevelopmentPath,
      extensionTestsPath,
      launchArgs: [workspaceDir, '--disable-extensions', '--disable-workspace-trust']
    });
  } finally {
    await fs.rm(workspaceDir, { recursive: true, force: true });
  }
}

void main().catch((error) => {
  console.error('Failed to run VS Code UI tests', error);
  process.exit(1);
});

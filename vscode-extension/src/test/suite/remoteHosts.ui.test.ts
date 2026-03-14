import * as assert from 'node:assert/strict';
import * as vscode from 'vscode';
import { suite, test } from 'mocha';

suite('OpenCode Remote Hosts UI', () => {
  test('registers and runs remote hosts refresh command', async () => {
    const extension = vscode.extensions.getExtension('local.opencode-router');
    assert.ok(extension, 'expected local.opencode-router extension to be discoverable');

    if (!extension.isActive) {
      await extension.activate();
    }

    const activityBarViews = await vscode.commands.getCommands(true);
    assert.ok(
      activityBarViews.includes('opencode.refreshRemoteHosts'),
      'expected opencode.refreshRemoteHosts command to be registered'
    );

    await assert.doesNotReject(async () => {
      await vscode.commands.executeCommand('opencode.refreshRemoteHosts');
    }, 'expected remote hosts refresh command to execute without throwing');
  });
});

# OpenCode Control Plane VS Code Extension

This extension integrates VS Code with the OpenCodeRouter control plane.
It provides session management, chat, terminal bridging, and diff-apply flows.

## 1. Requirements

- VS Code `^1.90.0`
- Running OpenCodeRouter control plane (default `http://localhost:8080`)
- Optional auth token if control-plane auth is enabled

## 2. Installation

### Development install

```bash
cd vscode-extension
npm install
npm run compile
```

Then launch via VS Code **Extension Development Host**.

### Packaged install (optional)

```bash
cd vscode-extension
npm install
npm run compile
npx @vscode/vsce package
```

Install the generated `.vsix` using:

- VS Code command palette -> `Extensions: Install from VSIX...`

## 3. Configuration

Settings are under the `opencode` namespace.

| Setting | Type | Default | Description |
|---|---|---|---|
| `opencode.controlPlaneUrl` | string | `http://localhost:8080` | Base URL used for API, SSE, and terminal websocket connections |
| `opencode.authToken` | string | `""` | Optional bearer token added as `Authorization: Bearer <token>` |

## 4. Features

### 4.1 Session tree

- View ID, label, status, and workspace path.
- Run session lifecycle actions:
  - create
  - attach
  - stop
  - restart
  - delete
- Connection status is shown in a status bar item.

### 4.2 Resilient refresh and event handling

- Initial session fetch uses bounded retry backoff.
- SSE event stream drives incremental refresh scheduling.
- On control-plane failures with cached sessions:
  - sessions are marked stale
  - warning prompt offers explicit `Retry`

### 4.3 Agent chat view

- Session-targeted chat webview in the OpenCode activity container.
- Uses extension-host transport to avoid webview-side auth/cors concerns.

### 4.4 Terminal bridge

- `OpenCode Terminal` profile backed by extension PTY bridge.
- Session-selected websocket terminal connection.
- Reconnect/status behavior handled in bridge implementation.

### 4.5 Diff integration

- Stage and preview diffs via `vscode.diff`.
- Apply/reject staged diffs.
- Clear diff highlights explicitly.

## 5. Commands

Contributed commands:

- `opencode.attachSession`
- `opencode.createSession`
- `opencode.openChat`
- `opencode.openTerminal`
- `opencode.refreshSessions`
- `opencode.stopSession`
- `opencode.restartSession`
- `opencode.deleteSession`
- `opencode.applyDiffPreview`
- `opencode.applyLastDiff`
- `opencode.rejectLastDiff`
- `opencode.clearDiffHighlights`

## 6. Keybindings reference

This extension does **not** contribute default keyboard shortcuts in
`package.json`.

Use one of:

- Command palette (`Ctrl/Cmd+Shift+P`) with the command IDs above
- OpenCode activity view title actions and context menus
- User/workspace custom keybindings mapped to command IDs

Example custom mapping (`keybindings.json`):

```json
[
  {
    "key": "ctrl+alt+o",
    "command": "opencode.refreshSessions"
  },
  {
    "key": "ctrl+alt+t",
    "command": "opencode.openTerminal"
  }
]
```

## 7. Troubleshooting

### Session tree shows disconnected/error

- Verify control plane is running at `opencode.controlPlaneUrl`.
- Check token validity if auth is enabled.
- Use `OpenCode: Refresh Sessions` command.

### Sessions appear as stale

- The extension kept last successful data due to control-plane request failures.
- Use `Retry` in warning prompt or run refresh command.

### Terminal connection fails

- Confirm session is not `stopped` and daemon health is `healthy`.
- Ensure control plane can attach terminal for that session.
- In constrained environments, `/ws/terminal/{id}` may return 502/503 if terminal
  attach prerequisites are not available.

### Chat or diff actions fail

- Confirm selected session exists and daemon is reachable.
- Check control-plane logs for daemon passthrough errors.

### Extension compile issues

- Reinstall dependencies:

```bash
cd vscode-extension
npm install
npm run compile
```

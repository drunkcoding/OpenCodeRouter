import * as vscode from 'vscode';
import { ChatSessionTarget, ChatWebviewProvider } from './chat/ChatWebviewProvider';
import { DiffEditManager } from './edits/DiffEditManager';
import { OpenCodeTerminalBridge } from './terminal/OpenCodeTerminalBridge';

type ConnectionState = 'connecting' | 'connected' | 'disconnected' | 'error';

interface SessionRecord {
  id: string;
  label: string;
  status: string;
  workspacePath: string;
  stale?: boolean;
}

class SessionItem extends vscode.TreeItem {
  constructor(readonly session: SessionRecord) {
    super(session.stale ? `${session.label} (stale)` : session.label, vscode.TreeItemCollapsibleState.None);
    this.id = session.id;
    this.contextValue = 'opencodeSession';
    this.iconPath = this.statusToIcon(session.status);
    this.description = session.stale
      ? `${session.workspacePath || 'n/a'} · stale data`
      : session.workspacePath || undefined;
    this.tooltip = session.stale
      ? `${session.label}\nStatus: ${session.status}\nWorkspace: ${session.workspacePath || 'n/a'}\nData: stale (control plane unavailable)`
      : `${session.label}\nStatus: ${session.status}\nWorkspace: ${session.workspacePath || 'n/a'}`;
    this.command = {
      command: 'opencode.attachSession',
      title: 'Attach Session',
      arguments: [this]
    };
  }

  private statusToIcon(status: string): vscode.ThemeIcon {
    switch (status.toLowerCase()) {
      case 'active':
        return new vscode.ThemeIcon('play-circle');
      case 'idle':
        return new vscode.ThemeIcon('clock');
      case 'stopped':
        return new vscode.ThemeIcon('debug-stop');
      case 'error':
      case 'errored':
        return new vscode.ThemeIcon('error');
      default:
        return new vscode.ThemeIcon('question');
    }
  }
}

class SessionTreeProvider implements vscode.TreeDataProvider<SessionItem>, vscode.Disposable {
  private readonly changeEmitter = new vscode.EventEmitter<SessionItem | undefined | null | void>();
  readonly onDidChangeTreeData = this.changeEmitter.event;

  private sessions: SessionRecord[] = [];
  private connectionState: ConnectionState = 'disconnected';
  private sseAbort?: AbortController;
  private disposed = false;
  private reconnectDelayMs = 2000;
  private scheduledRefresh?: NodeJS.Timeout;
  private staleNoticeVisible = false;

  constructor(
    private readonly onConnectionStateChanged: (state: ConnectionState, detail?: string) => void
  ) {}

  dispose(): void {
    this.disposed = true;
    this.sseAbort?.abort();
    if (this.scheduledRefresh) {
      clearTimeout(this.scheduledRefresh);
      this.scheduledRefresh = undefined;
    }
    this.setConnectionState('disconnected', 'Extension deactivated');
  }

  getTreeItem(element: SessionItem): vscode.TreeItem {
    return element;
  }

  getChildren(): Thenable<SessionItem[]> {
    return Promise.resolve(this.sessions.map((session) => new SessionItem(session)));
  }

  async start(): Promise<void> {
    await this.refresh();
    void this.startEventLoop();
  }

  getSessions(): SessionRecord[] {
    return [...this.sessions];
  }

  async refresh(): Promise<void> {
    try {
      const response = await this.requestWithBackoff('/api/sessions', {
        method: 'GET',
        headers: { Accept: 'application/json' }
      }, 2);

      if (!response.ok) {
        throw new Error(`GET /api/sessions failed (${response.status})`);
      }

      const body = (await response.json()) as unknown;
      this.sessions = Array.isArray(body)
        ? body
            .map((entry) => this.normalizeSession(entry))
            .filter((value): value is SessionRecord => value !== null)
            .map((session) => ({ ...session, stale: false }))
        : [];

      this.staleNoticeVisible = false;
      if (this.connectionState !== 'connected') {
        this.setConnectionState('connected');
      }
      this.changeEmitter.fire();
    } catch (error) {
      const detail = this.formatError(error);
      if (this.sessions.length > 0) {
        this.sessions = this.sessions.map((session) => ({ ...session, stale: true }));
        this.setConnectionState('error', `Showing stale data: ${detail}`);
        this.changeEmitter.fire();
        this.showStaleDataWarning(detail);
        return;
      }

      this.setConnectionState('error', detail);
      throw error;
    }
  }

  async createSession(workspacePath: string, label?: string): Promise<void> {
    const payload = { workspacePath, ...(label ? { label } : {}) };
    const response = await this.request('/api/sessions', {
      method: 'POST',
      body: JSON.stringify(payload)
    });

    if (!response.ok) {
      throw new Error(`POST /api/sessions failed (${response.status})`);
    }
    await this.refresh();
  }

  async attachSession(item: SessionItem): Promise<void> {
    await this.sessionAction(item, 'attach', 'POST');
  }

  async stopSession(item: SessionItem): Promise<void> {
    await this.sessionAction(item, 'stop', 'POST');
  }

  async restartSession(item: SessionItem): Promise<void> {
    await this.sessionAction(item, 'restart', 'POST');
  }

  async deleteSession(item: SessionItem): Promise<void> {
    const response = await this.request(`/api/sessions/${encodeURIComponent(item.session.id)}`, {
      method: 'DELETE'
    });

    if (!response.ok && response.status !== 204) {
      throw new Error(`DELETE /api/sessions/{id} failed (${response.status})`);
    }
    await this.refresh();
  }

  private async startEventLoop(): Promise<void> {
    while (!this.disposed) {
      try {
        await this.connectEventStream();
      } catch (error) {
        if (!this.disposed) {
          this.setConnectionState('error', `SSE error: ${this.formatError(error)}`);
        }
      }

      if (this.disposed) {
        break;
      }

      this.setConnectionState('disconnected', `SSE disconnected; retrying in ${Math.floor(this.reconnectDelayMs / 1000)}s`);
      await this.delay(this.reconnectDelayMs);
      this.reconnectDelayMs = Math.min(this.reconnectDelayMs * 2, 15000);
    }
  }

  private async connectEventStream(): Promise<void> {
    this.setConnectionState('connecting', 'Connecting to /api/events');
    const controller = new AbortController();
    this.sseAbort = controller;

    const response = await this.request('/api/events', {
      method: 'GET',
      headers: { Accept: 'text/event-stream' },
      signal: controller.signal
    });

    if (!response.ok || !response.body) {
      throw new Error(`GET /api/events failed (${response.status})`);
    }

    this.reconnectDelayMs = 2000;
    this.setConnectionState('connected', 'SSE connected');

    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';

    while (!this.disposed) {
      const { done, value } = await reader.read();
      if (done) {
        break;
      }

      buffer += decoder.decode(value, { stream: true }).replace(/\r\n/g, '\n');

      let boundary = buffer.indexOf('\n\n');
      while (boundary >= 0) {
        const chunk = buffer.slice(0, boundary);
        buffer = buffer.slice(boundary + 2);
        this.handleSseChunk(chunk);
        boundary = buffer.indexOf('\n\n');
      }
    }
  }

  private handleSseChunk(chunk: string): void {
    const trimmed = chunk.trim();
    if (!trimmed || trimmed.startsWith(':')) {
      return;
    }

    const lines = trimmed.split('\n');
    let shouldRefresh = false;
    for (const line of lines) {
      if (line.startsWith('event:')) {
        const eventType = line.slice(6).trim();
        if (eventType.startsWith('session.') || eventType.startsWith('backend.') || eventType === 'message') {
          shouldRefresh = true;
        }
      }
      if (line.startsWith('data:')) {
        shouldRefresh = true;
      }
    }

    if (shouldRefresh) {
      this.scheduleRefresh();
    }
  }

  private scheduleRefresh(): void {
    if (this.scheduledRefresh) {
      return;
    }

    this.scheduledRefresh = setTimeout(() => {
      this.scheduledRefresh = undefined;
      void this.refresh().catch(() => undefined);
    }, 250);
  }

  private async sessionAction(item: SessionItem, action: 'attach' | 'stop' | 'restart', method: 'POST'): Promise<void> {
    const response = await this.request(`/api/sessions/${encodeURIComponent(item.session.id)}/${action}`, {
      method
    });

    if (!response.ok) {
      throw new Error(`${method} /api/sessions/{id}/${action} failed (${response.status})`);
    }
    await this.refresh();
  }

  private normalizeSession(value: unknown): SessionRecord | null {
    if (!value || typeof value !== 'object') {
      return null;
    }

    const candidate = value as Record<string, unknown>;
    const id = this.firstNonEmptyString(candidate.id, candidate.sessionId, candidate.session_id);
    if (!id) {
      return null;
    }

    const label =
      this.firstNonEmptyString(candidate.label, candidate.name, candidate.sessionName, candidate.session_name) || id;
    const status = this.firstNonEmptyString(candidate.status, candidate.state) || 'unknown';
    const workspacePath =
      this.firstNonEmptyString(candidate.workspacePath, candidate.workspace_path, candidate.path, candidate.projectPath) ||
      '';

    return {
      id,
      label,
      status,
      workspacePath
    };
  }

  private firstNonEmptyString(...values: unknown[]): string | undefined {
    for (const value of values) {
      if (typeof value === 'string' && value.trim()) {
        return value.trim();
      }
    }
    return undefined;
  }

  private setConnectionState(state: ConnectionState, detail?: string): void {
    this.connectionState = state;
    this.onConnectionStateChanged(state, detail);
  }

  private getControlPlaneUrl(): string {
    const cfg = vscode.workspace.getConfiguration('opencode');
    const configured = cfg.get<string>('controlPlaneUrl', 'http://localhost:8080');
    return configured.replace(/\/+$/, '');
  }

  private getAuthToken(): string {
    return vscode.workspace.getConfiguration('opencode').get<string>('authToken', '').trim();
  }

  private async request(path: string, init: RequestInit): Promise<Response> {
    const headers = new Headers(init.headers ?? undefined);
    if (!headers.has('Accept')) {
      headers.set('Accept', 'application/json');
    }
    if (init.body && !headers.has('Content-Type')) {
      headers.set('Content-Type', 'application/json');
    }

    const token = this.getAuthToken();
    if (token) {
      headers.set('Authorization', `Bearer ${token}`);
    }

    return fetch(`${this.getControlPlaneUrl()}${path}`, {
      ...init,
      headers
    });
  }

  private async requestWithBackoff(path: string, init: RequestInit, maxRetries: number): Promise<Response> {
    let attempt = 0;
    let lastError: unknown;

    while (attempt <= maxRetries) {
      try {
        const response = await this.request(path, init);
        if (!this.isRetryableStatus(response.status) || attempt >= maxRetries) {
          return response;
        }
        lastError = new Error(`retryable status ${response.status}`);
      } catch (error) {
        lastError = error;
        if (!this.isRetryableError(error) || attempt >= maxRetries) {
          throw error;
        }
      }

      const delayMs = Math.min(1000 * Math.pow(2, attempt), 4000);
      await this.delay(delayMs);
      attempt++;
    }

    if (lastError instanceof Error) {
      throw lastError;
    }
    throw new Error('request failed');
  }

  private isRetryableStatus(status: number): boolean {
    return status === 429 || status >= 500;
  }

  private isRetryableError(error: unknown): boolean {
    if (error instanceof Error) {
      const text = error.message.toLowerCase();
      return text.includes('fetch') || text.includes('network') || text.includes('timeout') || text.includes('econn');
    }
    return true;
  }

  private showStaleDataWarning(detail: string): void {
    if (this.staleNoticeVisible) {
      return;
    }
    this.staleNoticeVisible = true;
    void vscode.window
      .showWarningMessage(`OpenCode unavailable (${detail}). Showing stale sessions.`, 'Retry')
      .then(async (action) => {
        this.staleNoticeVisible = false;
        if (action === 'Retry') {
          await this.refresh().catch(() => undefined);
        }
      });
  }

  private formatError(error: unknown): string {
    if (error instanceof Error) {
      return error.message;
    }
    return String(error);
  }

  private delay(ms: number): Promise<void> {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }
}

class ConnectionStatusBar implements vscode.Disposable {
  private readonly item: vscode.StatusBarItem;

  constructor() {
    this.item = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 100);
    this.item.command = 'opencode.refreshSessions';
    this.item.tooltip = 'OpenCode control plane status. Click to refresh sessions.';
    this.update('disconnected', 'Not connected');
    this.item.show();
  }

  update(state: ConnectionState, detail?: string): void {
    switch (state) {
      case 'connected':
        this.item.text = '$(plug) OpenCode: Connected';
        this.item.color = undefined;
        break;
      case 'connecting':
        this.item.text = '$(sync~spin) OpenCode: Connecting';
        this.item.color = undefined;
        break;
      case 'error':
        this.item.text = '$(error) OpenCode: Error';
        this.item.color = new vscode.ThemeColor('statusBarItem.errorForeground');
        break;
      default:
        this.item.text = '$(debug-disconnect) OpenCode: Disconnected';
        this.item.color = undefined;
        break;
    }

    this.item.tooltip = detail
      ? `OpenCode control plane status: ${state}\n${detail}\n\nClick to refresh sessions.`
      : `OpenCode control plane status: ${state}\n\nClick to refresh sessions.`;
  }

  dispose(): void {
    this.item.dispose();
  }
}

export function activate(context: vscode.ExtensionContext): void {
  const statusBar = new ConnectionStatusBar();
  const treeProvider = new SessionTreeProvider((state, detail) => statusBar.update(state, detail));
  const chatProvider = new ChatWebviewProvider(context.extensionUri);
  const diffEditManager = new DiffEditManager((sessionId) =>
    treeProvider.getSessions().find((session) => session.id === sessionId)?.workspacePath
  );

  context.subscriptions.push(statusBar, treeProvider, chatProvider, diffEditManager);
  context.subscriptions.push(vscode.window.registerTreeDataProvider('opencodeSessions', treeProvider));
  context.subscriptions.push(
    vscode.window.registerWebviewViewProvider('opencodeChat', chatProvider, {
      webviewOptions: { retainContextWhenHidden: true }
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand('opencode.refreshSessions', async () => {
      try {
        await treeProvider.refresh();
      } catch (error) {
        vscode.window.showErrorMessage(`Failed to refresh sessions: ${error instanceof Error ? error.message : String(error)}`);
      }
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand('opencode.createSession', async () => {
      const workspaceDefault = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ?? '';
      const workspacePath = await vscode.window.showInputBox({
        prompt: 'Workspace path for new session',
        value: workspaceDefault,
        ignoreFocusOut: true
      });

      if (!workspacePath) {
        return;
      }

      const label = await vscode.window.showInputBox({
        prompt: 'Session label (optional)',
        ignoreFocusOut: true
      });

      try {
        await treeProvider.createSession(workspacePath, label?.trim() || undefined);
        vscode.window.showInformationMessage('OpenCode session created.');
      } catch (error) {
        vscode.window.showErrorMessage(
          `Failed to create session: ${error instanceof Error ? error.message : String(error)}`
        );
      }
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand('opencode.openChat', async (item?: SessionItem) => {
      let target: ChatSessionTarget | undefined;
      if (item?.session) {
        target = {
          id: item.session.id,
          label: item.session.label,
          workspacePath: item.session.workspacePath
        };
      } else {
        const picked = await pickSession(treeProvider, 'Select a session for OpenCode chat');

        if (!picked) {
          return;
        }

        target = {
          id: picked.id,
          label: picked.label,
          workspacePath: picked.workspacePath
        };
      }

      await chatProvider.openChat(target);
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand('opencode.openTerminal', async (item?: SessionItem) => {
      const session = item?.session ?? (await pickSession(treeProvider, 'Select a session for OpenCode terminal'));
      if (!session) {
        return;
      }

      const terminal = vscode.window.createTerminal(createTerminalOptions(session));
      terminal.show(true);
    })
  );

  context.subscriptions.push(
    vscode.window.registerTerminalProfileProvider('opencode.terminalProfile', {
      provideTerminalProfile: async () => {
        const session = await pickSession(treeProvider, 'Select a session for OpenCode terminal profile');
        if (!session) {
          return undefined;
        }
        return new vscode.TerminalProfile(createTerminalOptions(session));
      }
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand('opencode.applyDiffPreview', async (payload?: { sessionId?: string; diff?: string }) => {
      await diffEditManager.stageFromPayload({
        sessionId: payload?.sessionId,
        diff: payload?.diff,
        source: 'chat.applyDiffPreview'
      });
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand('opencode.applyLastDiff', async () => {
      await diffEditManager.applyLastDiff();
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand('opencode.rejectLastDiff', () => {
      diffEditManager.rejectLastDiff();
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand('opencode.clearDiffHighlights', () => {
      diffEditManager.clearDecorations();
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand('opencode.attachSession', async (item: SessionItem) => {
      if (!item) {
        return;
      }

      try {
        await treeProvider.attachSession(item);
        vscode.window.showInformationMessage(`Attached to session: ${item.session.label}`);
      } catch (error) {
        vscode.window.showErrorMessage(
          `Failed to attach session: ${error instanceof Error ? error.message : String(error)}`
        );
      }
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand('opencode.stopSession', async (item: SessionItem) => {
      if (!item) {
        return;
      }

      try {
        await treeProvider.stopSession(item);
        vscode.window.showInformationMessage(`Stopped session: ${item.session.label}`);
      } catch (error) {
        vscode.window.showErrorMessage(`Failed to stop session: ${error instanceof Error ? error.message : String(error)}`);
      }
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand('opencode.restartSession', async (item: SessionItem) => {
      if (!item) {
        return;
      }

      try {
        await treeProvider.restartSession(item);
        vscode.window.showInformationMessage(`Restarted session: ${item.session.label}`);
      } catch (error) {
        vscode.window.showErrorMessage(
          `Failed to restart session: ${error instanceof Error ? error.message : String(error)}`
        );
      }
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand('opencode.deleteSession', async (item: SessionItem) => {
      if (!item) {
        return;
      }

      const confirmed = await vscode.window.showWarningMessage(
        `Delete session \"${item.session.label}\"?`,
        { modal: true },
        'Delete'
      );

      if (confirmed !== 'Delete') {
        return;
      }

      try {
        await treeProvider.deleteSession(item);
        vscode.window.showInformationMessage(`Deleted session: ${item.session.label}`);
      } catch (error) {
        vscode.window.showErrorMessage(
          `Failed to delete session: ${error instanceof Error ? error.message : String(error)}`
        );
      }
    })
  );

  void treeProvider.start();
}

export function deactivate(): void {
}

async function pickSession(treeProvider: SessionTreeProvider, placeHolder: string): Promise<SessionRecord | undefined> {
  if (treeProvider.getSessions().length === 0) {
    await treeProvider.refresh().catch(() => undefined);
  }

  const sessions = treeProvider.getSessions();
  if (sessions.length === 0) {
    vscode.window.showWarningMessage('No sessions available. Refresh or create a session first.');
    return undefined;
  }

  const picked = await vscode.window.showQuickPick(
    sessions.map((session) => ({
      label: session.label,
      description: session.workspacePath || session.id,
      detail: `${session.status} · ${session.id}`,
      session
    })),
    { placeHolder }
  );

  return picked?.session;
}

function createTerminalOptions(session: SessionRecord): vscode.ExtensionTerminalOptions {
  const config = vscode.workspace.getConfiguration('opencode');
  const controlPlaneUrl = config.get<string>('controlPlaneUrl', 'http://localhost:8080').replace(/\/+$/, '');
  const authToken = config.get<string>('authToken', '').trim();

  return {
    name: `OpenCode: ${session.label}`,
    pty: new OpenCodeTerminalBridge({
      controlPlaneUrl,
      authToken,
      session: {
        id: session.id,
        label: session.label
      }
    })
  };
}

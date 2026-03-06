import { randomBytes } from 'crypto';
import * as path from 'path';
import * as vscode from 'vscode';

export interface ChatSessionTarget {
  id: string;
  label: string;
  workspacePath: string;
}

interface ChatMessage {
  id: string;
  role: 'user' | 'assistant' | 'system';
  content: string;
  toolCalls: unknown[];
}

type InboundMessage =
  | { type: 'ready' }
  | { type: 'requestHistory' }
  | { type: 'sendPrompt'; prompt: string }
  | { type: 'openFile'; path: string; line?: number }
  | { type: 'applyDiff'; diff: string };

type OutboundMessage =
  | { type: 'session'; session: ChatSessionTarget | null }
  | { type: 'chatHistory'; messages: ChatMessage[] }
  | { type: 'streamStarted' }
  | { type: 'streamEnded' }
  | { type: 'chatChunk'; chunk: Record<string, unknown> }
  | { type: 'error'; message: string };

export class ChatWebviewProvider implements vscode.WebviewViewProvider, vscode.Disposable {
  private view?: vscode.WebviewView;
  private currentSession: ChatSessionTarget | null = null;
  private streamAbort?: AbortController;

  constructor(private readonly extensionUri: vscode.Uri) {}

  dispose(): void {
    this.streamAbort?.abort();
  }

  resolveWebviewView(webviewView: vscode.WebviewView): void {
    this.view = webviewView;
    webviewView.webview.options = {
      enableScripts: true,
      localResourceRoots: [vscode.Uri.joinPath(this.extensionUri, 'media', 'chat')]
    };
    webviewView.webview.html = this.buildHtml(webviewView.webview);

    webviewView.webview.onDidReceiveMessage((msg: InboundMessage) => {
      void this.handleMessage(msg);
    });

    webviewView.onDidDispose(() => {
      this.streamAbort?.abort();
      this.streamAbort = undefined;
      this.view = undefined;
    });

    this.post({ type: 'session', session: this.currentSession });
  }

  async openChat(session?: ChatSessionTarget): Promise<void> {
    if (session) {
      this.currentSession = session;
    }

    await vscode.commands.executeCommand('workbench.view.extension.opencode');
    this.view?.show?.(true);
    this.post({ type: 'session', session: this.currentSession });
    if (this.currentSession) {
      await this.loadHistory();
    }
  }

  private async handleMessage(msg: InboundMessage): Promise<void> {
    switch (msg.type) {
      case 'ready':
        this.post({ type: 'session', session: this.currentSession });
        if (this.currentSession) {
          await this.loadHistory();
        }
        return;
      case 'requestHistory':
        await this.loadHistory();
        return;
      case 'sendPrompt':
        await this.streamPrompt(msg.prompt);
        return;
      case 'openFile':
        await this.openFile(msg.path, msg.line);
        return;
      case 'applyDiff':
        await this.applyDiff(msg.diff);
        return;
      default:
        return;
    }
  }

  private async loadHistory(): Promise<void> {
    if (!this.currentSession) {
      return;
    }

    try {
      const response = await this.request(`/api/sessions/${encodeURIComponent(this.currentSession.id)}/chat`, {
        method: 'GET',
        headers: { Accept: 'application/json' }
      });

      if (!response.ok) {
        throw new Error(`GET /api/sessions/{id}/chat failed (${response.status})`);
      }

      const payload = (await response.json()) as unknown;
      const messages = Array.isArray(payload)
        ? payload
            .map((entry) => this.normalizeHistoryMessage(entry))
            .filter((entry): entry is ChatMessage => entry !== null)
        : [];

      this.post({ type: 'chatHistory', messages });
    } catch (error) {
      this.post({ type: 'error', message: `Failed to load chat history: ${this.formatError(error)}` });
    }
  }

  private async streamPrompt(prompt: string): Promise<void> {
    if (!this.currentSession) {
      this.post({ type: 'error', message: 'No session selected for chat.' });
      return;
    }

    const trimmed = prompt.trim();
    if (!trimmed) {
      return;
    }

    this.streamAbort?.abort();
    const controller = new AbortController();
    this.streamAbort = controller;
    this.post({ type: 'streamStarted' });

    try {
      const response = await this.request(`/api/sessions/${encodeURIComponent(this.currentSession.id)}/chat`, {
        method: 'POST',
        headers: {
          Accept: 'text/event-stream'
        },
        body: JSON.stringify({ prompt: trimmed }),
        signal: controller.signal
      });

      if (!response.ok || !response.body) {
        throw new Error(`POST /api/sessions/{id}/chat failed (${response.status})`);
      }

      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      let buffer = '';

      while (true) {
        const { done, value } = await reader.read();
        if (done) {
          break;
        }

        buffer += decoder.decode(value, { stream: true }).replace(/\r\n/g, '\n');
        let boundary = buffer.indexOf('\n\n');
        while (boundary >= 0) {
          const frame = buffer.slice(0, boundary);
          buffer = buffer.slice(boundary + 2);
          const parsed = this.parseSSEFrame(frame);
          if (parsed) {
            this.post({ type: 'chatChunk', chunk: parsed });
          }
          boundary = buffer.indexOf('\n\n');
        }
      }
    } catch (error) {
      const aborted = error instanceof Error && error.name === 'AbortError';
      if (!aborted) {
        this.post({ type: 'error', message: `Chat streaming failed: ${this.formatError(error)}` });
      }
    } finally {
      if (this.streamAbort === controller) {
        this.streamAbort = undefined;
      }
      this.post({ type: 'streamEnded' });
    }
  }

  private parseSSEFrame(frame: string): Record<string, unknown> | null {
    const lines = frame.split('\n');
    const dataLines: string[] = [];
    for (const line of lines) {
      if (line.startsWith('data:')) {
        dataLines.push(line.slice(5).trimStart());
      }
    }

    if (dataLines.length === 0) {
      return null;
    }

    const raw = dataLines.join('\n').trim();
    if (!raw) {
      return null;
    }

    try {
      const parsed = JSON.parse(raw) as unknown;
      if (parsed && typeof parsed === 'object') {
        return this.normalizeChunk(parsed as Record<string, unknown>);
      }
      return { type: 'message', delta: String(parsed), done: false };
    } catch {
      return { type: 'message', delta: raw, done: false };
    }
  }

  private normalizeChunk(chunk: Record<string, unknown>): Record<string, unknown> {
    const type = this.firstNonEmptyString(chunk.type, chunk.event, 'message') ?? 'message';
    const delta =
      this.firstNonEmptyString(
        chunk.delta,
        this.nestedString(chunk, ['part', 'delta']),
        this.nestedString(chunk, ['message', 'part', 'delta']),
        this.nestedString(chunk, ['message', 'delta'])
      ) ?? '';
    const error = this.firstNonEmptyString(chunk.error, this.nestedString(chunk, ['payload', 'error'])) ?? '';
    const terminalType = ['session.idle', 'session.error', 'message.completed', 'message.done', 'message.error', 'stream.closed'];
    const done = Boolean(chunk.done) || terminalType.includes(type.toLowerCase());
    const payload = this.firstObject(chunk.payload, chunk.part, chunk.message) ?? chunk;

    return {
      type,
      delta,
      error,
      done,
      payload,
      raw: chunk
    };
  }

  private normalizeHistoryMessage(entry: unknown): ChatMessage | null {
    if (!entry || typeof entry !== 'object') {
      return null;
    }

    const message = entry as Record<string, unknown>;
    const roleRaw = this.firstNonEmptyString(message.role, message.type, 'assistant') ?? 'assistant';
    const role: 'user' | 'assistant' | 'system' = roleRaw === 'user' || roleRaw === 'assistant' || roleRaw === 'system' ? roleRaw : 'assistant';
    const content = this.extractHistoryText(message);
    const toolCalls = this.extractHistoryTools(message);

    return {
      id: this.firstNonEmptyString(message.id, message.messageId, message.message_id, `${Date.now()}-${Math.random()}`) ??
        `${Date.now()}-${Math.random()}`,
      role,
      content,
      toolCalls
    };
  }

  private extractHistoryText(message: Record<string, unknown>): string {
    const direct = this.firstNonEmptyString(message.content, message.text, message.delta);
    if (direct) {
      return direct;
    }

    const parts = Array.isArray(message.parts) ? message.parts : [];
    const textParts: string[] = [];
    for (const part of parts) {
      if (!part || typeof part !== 'object') {
        continue;
      }
      const partObject = part as Record<string, unknown>;
      const text = this.firstNonEmptyString(partObject.text, partObject.content, partObject.delta);
      if (text) {
        textParts.push(text);
      }
    }

    return textParts.join('');
  }

  private extractHistoryTools(message: Record<string, unknown>): unknown[] {
    const tools: unknown[] = [];
    const parts = Array.isArray(message.parts) ? message.parts : [];
    for (const part of parts) {
      if (!part || typeof part !== 'object') {
        continue;
      }
      const partObject = part as Record<string, unknown>;
      const type = this.firstNonEmptyString(partObject.type, partObject.kind) ?? '';
      if (type.toLowerCase().includes('tool')) {
        tools.push(partObject);
      }
    }
    return tools;
  }

  private async openFile(filePath: string, line?: number): Promise<void> {
    const trimmed = filePath.trim();
    if (!trimmed) {
      return;
    }

    const roots: string[] = [];
    if (this.currentSession?.workspacePath) {
      roots.push(this.currentSession.workspacePath);
    }
    const workspaceRoot = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
    if (workspaceRoot) {
      roots.push(workspaceRoot);
    }

    const targetPath = path.isAbsolute(trimmed) ? trimmed : roots.length > 0 ? path.join(roots[0], trimmed) : trimmed;

    try {
      const document = await vscode.workspace.openTextDocument(vscode.Uri.file(targetPath));
      const editor = await vscode.window.showTextDocument(document, { preview: false });
      const targetLine = Number.isFinite(line) && typeof line === 'number' && line > 0 ? line - 1 : 0;
      const position = new vscode.Position(Math.max(targetLine, 0), 0);
      editor.selection = new vscode.Selection(position, position);
      editor.revealRange(new vscode.Range(position, position), vscode.TextEditorRevealType.InCenter);
    } catch (error) {
      vscode.window.showErrorMessage(`Unable to open file reference ${trimmed}: ${this.formatError(error)}`);
    }
  }

  private async applyDiff(diff: string): Promise<void> {
    const content = diff.trim();
    if (!content) {
      return;
    }
    await vscode.commands.executeCommand('opencode.applyDiffPreview', {
      sessionId: this.currentSession?.id,
      diff: content
    });
  }

  private async request(pathname: string, init: RequestInit): Promise<Response> {
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

    return fetch(`${this.getControlPlaneUrl()}${pathname}`, {
      ...init,
      headers
    });
  }

  private getControlPlaneUrl(): string {
    const configured = vscode.workspace.getConfiguration('opencode').get<string>('controlPlaneUrl', 'http://localhost:8080');
    return configured.replace(/\/+$/, '');
  }

  private getAuthToken(): string {
    return vscode.workspace.getConfiguration('opencode').get<string>('authToken', '').trim();
  }

  private post(message: OutboundMessage): void {
    this.view?.webview.postMessage(message);
  }

  private firstNonEmptyString(...values: unknown[]): string | undefined {
    for (const value of values) {
      if (typeof value === 'string' && value.trim()) {
        return value.trim();
      }
    }
    return undefined;
  }

  private firstObject(...values: unknown[]): Record<string, unknown> | undefined {
    for (const value of values) {
      if (value && typeof value === 'object' && !Array.isArray(value)) {
        return value as Record<string, unknown>;
      }
    }
    return undefined;
  }

  private nestedString(value: Record<string, unknown>, keys: string[]): string | undefined {
    let cursor: unknown = value;
    for (const key of keys) {
      if (!cursor || typeof cursor !== 'object' || Array.isArray(cursor)) {
        return undefined;
      }
      cursor = (cursor as Record<string, unknown>)[key];
    }
    return this.firstNonEmptyString(cursor);
  }

  private formatError(error: unknown): string {
    if (error instanceof Error) {
      return error.message;
    }
    return String(error);
  }

  private buildHtml(webview: vscode.Webview): string {
    const nonce = randomBytes(16).toString('base64');
    const scriptUri = webview.asWebviewUri(vscode.Uri.joinPath(this.extensionUri, 'media', 'chat', 'chat.js'));
    const styleUri = webview.asWebviewUri(vscode.Uri.joinPath(this.extensionUri, 'media', 'chat', 'chat.css'));

    return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src ${webview.cspSource} data: https:; style-src ${webview.cspSource}; script-src 'nonce-${nonce}';" />
  <link rel="stylesheet" href="${styleUri}" />
  <title>OpenCode Chat</title>
</head>
<body>
  <div class="chat-root">
    <div class="chat-header">
      <div id="session-title" class="session-title">No session selected</div>
      <div id="stream-state" class="stream-state">idle</div>
    </div>
    <div id="messages" class="messages"></div>
    <form id="chat-form" class="chat-form">
      <textarea id="chat-input" class="chat-input" rows="3" placeholder="Ask the agent about this session"></textarea>
      <div class="chat-actions">
        <button id="chat-send" type="submit">Send</button>
      </div>
    </form>
  </div>
  <script nonce="${nonce}" src="${scriptUri}"></script>
</body>
</html>`;
  }
}

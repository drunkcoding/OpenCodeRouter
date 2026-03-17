import * as vscode from 'vscode';

type TerminalLogLevel = 'INFO' | 'WARN' | 'ERROR';

let terminalLogChannel: vscode.OutputChannel | undefined;

function getTerminalLogChannel(): vscode.OutputChannel {
  if (!terminalLogChannel) {
    terminalLogChannel = vscode.window.createOutputChannel('OpenCode Control Plane');
  }
  return terminalLogChannel;
}

function logTerminal(level: TerminalLogLevel, message: string, error?: unknown): void {
  const channel = getTerminalLogChannel();
  const detail = formatLogError(error);
  const text = detail ? `${message} | ${detail}` : message;
  channel.appendLine(`[${new Date().toISOString()}] [${level}] ${text}`);
}

function formatLogError(error: unknown): string {
  if (!error) {
    return '';
  }

  if (error instanceof Error) {
    return error.stack?.trim() || error.message;
  }

  if (typeof error === 'string') {
    return error;
  }

  try {
    return JSON.stringify(error);
  } catch {
    return String(error);
  }
}

export interface TerminalSessionTarget {
  id: string;
  label: string;
}

interface BridgeConfig {
  controlPlaneUrl: string;
  authToken: string;
  session: TerminalSessionTarget;
}

const encoder = new TextEncoder();

export class OpenCodeTerminalBridge implements vscode.Pseudoterminal {
  private readonly writeEmitter = new vscode.EventEmitter<string>();
  private readonly closeEmitter = new vscode.EventEmitter<number>();

  readonly onDidWrite: vscode.Event<string> = this.writeEmitter.event;
  readonly onDidClose?: vscode.Event<number> = this.closeEmitter.event;

  private socket?: WebSocket;
  private reconnectTimer?: NodeJS.Timeout;
  private reconnectAttempts = 0;
  private closed = false;
  private openCalled = false;
  private dimensions?: vscode.TerminalDimensions;
  private pendingInput: Uint8Array[] = [];

  constructor(private readonly config: BridgeConfig) {}

  open(initialDimensions: vscode.TerminalDimensions | undefined): void {
    this.openCalled = true;
    this.dimensions = initialDimensions;
    this.printStatus(`Opening terminal for session ${this.config.session.label} (${this.config.session.id})...`);
    this.connect();
  }

  close(): void {
    this.dispose(0);
  }

  handleInput(data: string): void {
    const payload = encoder.encode(data);
    if (this.socket && this.socket.readyState === WebSocket.OPEN) {
      this.socket.send(payload);
      return;
    }
    this.pendingInput.push(payload);
  }

  setDimensions(dimensions: vscode.TerminalDimensions): void {
    this.dimensions = dimensions;
    this.sendResize(dimensions);
  }

  private connect(): void {
    if (this.closed) {
      return;
    }

    logTerminal(
      'INFO',
      `terminal bridge connect | session_id=${this.config.session.id} | label=${this.config.session.label} | reconnect_attempt=${this.reconnectAttempts + 1}`
    );

    this.clearReconnectTimer();

    let socket: WebSocket;
    try {
      socket = this.createSocket();
    } catch (error) {
      logTerminal(
        'ERROR',
        `terminal bridge connect failed | session_id=${this.config.session.id} | label=${this.config.session.label}`,
        error
      );
      this.scheduleReconnect(`failed to construct websocket (${this.formatError(error)})`);
      return;
    }

    this.socket = socket;

    socket.binaryType = 'arraybuffer';
    socket.onopen = () => {
      this.reconnectAttempts = 0;
      logTerminal('INFO', `terminal bridge connected | session_id=${this.config.session.id} | label=${this.config.session.label}`);
      this.printStatus(`Connected to OpenCode terminal for ${this.config.session.label}.`);
      if (this.dimensions) {
        this.sendResize(this.dimensions);
      }
      this.flushPendingInput();
    };

    socket.onmessage = (event: MessageEvent) => {
      this.handleSocketMessage(event.data);
    };

    socket.onerror = (event: Event) => {
      const details =
        event instanceof ErrorEvent
          ? event.error ?? (event.message ? new Error(event.message) : event)
          : event;
      logTerminal(
        'ERROR',
        `terminal bridge error | session_id=${this.config.session.id} | label=${this.config.session.label}`,
        details
      );
      this.printStatus('Terminal websocket error detected.');
    };

    socket.onclose = (event: CloseEvent) => {
      const closeMessage =
        `terminal bridge disconnected | session_id=${this.config.session.id} | label=${this.config.session.label} ` +
        `| code=${event.code} | reason=${event.reason || 'none'} | was_clean=${event.wasClean}`;
      logTerminal('WARN', closeMessage);
      if (this.closed) {
        return;
      }
      this.socket = undefined;
      const reason = event.reason ? ` (${event.reason})` : '';
      this.scheduleReconnect(`connection closed${reason}`);
    };
  }

  private handleSocketMessage(data: unknown): void {
    if (typeof data === 'string') {
      this.writeEmitter.fire(data);
      return;
    }

    if (data instanceof ArrayBuffer) {
      this.writeEmitter.fire(new TextDecoder().decode(data));
      return;
    }

    if (ArrayBuffer.isView(data)) {
      const view = data as ArrayBufferView;
      this.writeEmitter.fire(new TextDecoder().decode(view.buffer.slice(view.byteOffset, view.byteOffset + view.byteLength)));
      return;
    }

    if (data instanceof Blob) {
      void data.arrayBuffer().then((buffer) => {
        if (!this.closed) {
          this.writeEmitter.fire(new TextDecoder().decode(buffer));
        }
      });
    }
  }

  private sendResize(dimensions: vscode.TerminalDimensions): void {
    if (!this.socket || this.socket.readyState !== WebSocket.OPEN) {
      return;
    }

    if (!dimensions.columns || !dimensions.rows) {
      return;
    }

    const control = {
      type: 'resize',
      cols: dimensions.columns,
      rows: dimensions.rows
    };
    this.socket.send(JSON.stringify(control));
  }

  private flushPendingInput(): void {
    if (!this.socket || this.socket.readyState !== WebSocket.OPEN) {
      return;
    }
    for (const payload of this.pendingInput) {
      this.socket.send(payload);
    }
    this.pendingInput = [];
  }

  private scheduleReconnect(reason: string): void {
    if (this.closed) {
      return;
    }

    const delays = [1000, 2000, 4000, 8000, 15000];
    const idx = Math.min(this.reconnectAttempts, delays.length - 1);
    const delayMs = delays[idx];
    const nextAttempt = this.reconnectAttempts + 1;
    this.reconnectAttempts += 1;

    logTerminal(
      'INFO',
      `terminal bridge reconnect scheduled | session_id=${this.config.session.id} | label=${this.config.session.label} | attempt=${nextAttempt} | delay_ms=${delayMs} | reason=${reason}`
    );

    this.printStatus(`Terminal ${reason}; reconnecting in ${Math.floor(delayMs / 1000)}s...`);
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = undefined;
      if (!this.closed && this.openCalled) {
        logTerminal(
          'INFO',
          `terminal bridge reconnecting | session_id=${this.config.session.id} | label=${this.config.session.label} | attempt=${nextAttempt}`
        );
        this.connect();
      }
    }, delayMs);
  }

  private createSocket(): WebSocket {
    const primary = this.buildWebSocketUrl(false);
    const authHeader = this.config.authToken ? { Authorization: `Bearer ${this.config.authToken}` } : undefined;
    const WSAny = WebSocket as unknown as {
      new (url: string, protocols?: string | string[], options?: { headers?: Record<string, string> }): WebSocket;
    };

    if (authHeader) {
      try {
        logTerminal('INFO', `terminal bridge ws auth mode=header | session_id=${this.config.session.id}`);
        return new WSAny(primary, undefined, { headers: authHeader });
      } catch (error) {
        logTerminal(
          'WARN',
          `terminal bridge ws auth header unsupported; using query-token fallback | session_id=${this.config.session.id}`,
          error
        );
      }
    }

    return new WebSocket(this.buildWebSocketUrl(Boolean(this.config.authToken)));
  }

  private buildWebSocketUrl(includeQueryToken: boolean): string {
    const base = new URL(this.config.controlPlaneUrl);
    base.protocol = base.protocol === 'https:' ? 'wss:' : 'ws:';
    const basePath = base.pathname.replace(/\/+$/, '');
    base.pathname = `${basePath}/ws/terminal/${encodeURIComponent(this.config.session.id)}`;
    if (includeQueryToken && this.config.authToken) {
      base.searchParams.set('access_token', this.config.authToken);
      base.searchParams.set('authorization', `Bearer ${this.config.authToken}`);
    }
    return base.toString();
  }

  private printStatus(message: string): void {
    this.writeEmitter.fire(`\r\n[OpenCode] ${message}\r\n`);
  }

  private clearReconnectTimer(): void {
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = undefined;
    }
  }

  private formatError(error: unknown): string {
    if (error instanceof Error) {
      return error.message;
    }
    return String(error);
  }

  private dispose(exitCode: number): void {
    if (this.closed) {
      return;
    }
    logTerminal(
      'INFO',
      `terminal bridge dispose | session_id=${this.config.session.id} | label=${this.config.session.label} | exit_code=${exitCode}`
    );
    this.closed = true;
    this.clearReconnectTimer();

    if (this.socket) {
      this.socket.onopen = null;
      this.socket.onmessage = null;
      this.socket.onclose = null;
      this.socket.onerror = null;
      if (this.socket.readyState === WebSocket.OPEN || this.socket.readyState === WebSocket.CONNECTING) {
        this.socket.close(1000, 'terminal disposed');
      }
      this.socket = undefined;
    }

    this.pendingInput = [];
    this.closeEmitter.fire(exitCode);
    this.writeEmitter.dispose();
    this.closeEmitter.dispose();
  }
}

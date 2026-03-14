import * as vscode from 'vscode';

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

    this.clearReconnectTimer();

    let socket: WebSocket;
    try {
      socket = this.createSocket();
    } catch (error) {
      this.scheduleReconnect(`failed to construct websocket (${this.formatError(error)})`);
      return;
    }

    this.socket = socket;

    socket.binaryType = 'arraybuffer';
    socket.onopen = () => {
      this.reconnectAttempts = 0;
      this.printStatus(`Connected to OpenCode terminal for ${this.config.session.label}.`);
      if (this.dimensions) {
        this.sendResize(this.dimensions);
      }
      this.flushPendingInput();
    };

    socket.onmessage = (event: MessageEvent) => {
      this.handleSocketMessage(event.data);
    };

    socket.onerror = () => {
      this.printStatus('Terminal websocket error detected.');
    };

    socket.onclose = (event: CloseEvent) => {
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
    this.reconnectAttempts += 1;

    this.printStatus(`Terminal ${reason}; reconnecting in ${Math.floor(delayMs / 1000)}s...`);
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = undefined;
      if (!this.closed && this.openCalled) {
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
        return new WSAny(primary, undefined, { headers: authHeader });
      } catch {
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

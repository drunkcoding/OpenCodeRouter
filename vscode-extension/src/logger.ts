import * as vscode from 'vscode';

type LogLevel = 'INFO' | 'WARN' | 'ERROR';

class Logger {
  constructor(private readonly channel: vscode.OutputChannel) {}

  info(message: string): void {
    this.log('INFO', message);
  }

  warn(message: string): void {
    this.log('WARN', message);
  }

  error(message: string, err?: unknown): void {
    const details = this.formatErrorDetails(err);
    this.log('ERROR', details ? `${message} | ${details}` : message);
  }

  show(preserveFocus = false): void {
    this.channel.show(preserveFocus);
  }

  logFetch(method: string, url: string, status: number | undefined, durationMs: number, error?: unknown): void {
    const base = `${method.toUpperCase()} ${url} | status=${status ?? 'n/a'} | duration=${durationMs}ms`;
    if (error) {
      this.error(`fetch failed | ${base}`, error);
      return;
    }

    if (typeof status === 'number' && status >= 400) {
      this.warn(`fetch response | ${base}`);
      return;
    }

    this.info(`fetch response | ${base}`);
  }

  private log(level: LogLevel, message: string): void {
    this.channel.appendLine(`[${new Date().toISOString()}] [${level}] ${message}`);
  }

  private formatErrorDetails(err: unknown): string {
    if (!err) {
      return '';
    }

    if (err instanceof Error) {
      if (err.stack && err.stack.trim()) {
        return err.stack;
      }
      return err.message;
    }

    if (typeof err === 'string') {
      return err;
    }

    try {
      return JSON.stringify(err);
    } catch {
      return String(err);
    }
  }
}

let loggerInstance: Logger | undefined;

export function getLogger(): Logger {
  if (!loggerInstance) {
    loggerInstance = new Logger(vscode.window.createOutputChannel('OpenCode Control Plane'));
  }
  return loggerInstance;
}

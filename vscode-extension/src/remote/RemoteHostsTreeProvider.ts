import * as vscode from 'vscode';

interface RemoteHostsResponse {
  hosts: RemoteHostRecord[];
  cached: boolean;
  stale: boolean;
  partial: boolean;
  lastScan?: string;
  warnings?: string[];
}

interface RemoteHostRecord {
  name: string;
  address: string;
  user: string;
  label: string;
  status: string;
  sessionCount: number;
  lastSeen?: string;
  lastError?: string;
  transport?: string;
  transportError?: string;
  projects: RemoteProjectRecord[];
}

interface RemoteProjectRecord {
  name: string;
  sessions: RemoteSessionRecord[];
}

interface RemoteSessionRecord {
  id: string;
  title: string;
  directory: string;
  status: string;
  activity: string;
  lastActivity?: string;
}

type RemoteNode = RemoteInfoItem | RemoteHostItem | RemoteProjectItem | RemoteSessionItem;

class RemoteInfoItem extends vscode.TreeItem {
  constructor(label: string, description?: string, severity: 'info' | 'warning' | 'error' = 'info') {
    super(label, vscode.TreeItemCollapsibleState.None);
    this.contextValue = 'opencodeRemoteInfo';
    this.description = description;
    this.tooltip = description ? `${label}\n${description}` : label;
    this.iconPath = severity === 'error'
      ? new vscode.ThemeIcon('error')
      : severity === 'warning'
        ? new vscode.ThemeIcon('warning')
        : new vscode.ThemeIcon('info');
  }
}

class RemoteHostItem extends vscode.TreeItem {
  constructor(readonly host: RemoteHostRecord) {
    super(host.label || host.name, host.projects.length > 0 ? vscode.TreeItemCollapsibleState.Collapsed : vscode.TreeItemCollapsibleState.None);
    this.id = `remote-host:${host.name}`;
    this.contextValue = 'opencodeRemoteHost';
    this.iconPath = this.statusToIcon(host.status);
    this.description = `${host.address || 'n/a'} · ${host.sessionCount} session${host.sessionCount === 1 ? '' : 's'}`;

    const details: string[] = [
      `Host: ${host.name}`,
      `Address: ${host.address || 'n/a'}`,
      `Status: ${host.status || 'unknown'}`
    ];
    if (host.user) {
      details.push(`User: ${host.user}`);
    }
    if (host.lastSeen) {
      details.push(`Last seen: ${host.lastSeen}`);
    }
    if (host.transport) {
      details.push(`Transport: ${host.transport}`);
    }
    if (host.lastError) {
      details.push(`Last error: ${host.lastError}`);
    }
    if (host.transportError) {
      details.push(`Transport error: ${host.transportError}`);
    }
    this.tooltip = details.join('\n');
  }

  private statusToIcon(status: string): vscode.ThemeIcon {
    switch (status.toLowerCase()) {
      case 'online':
        return new vscode.ThemeIcon('vm-active');
      case 'auth_required':
        return new vscode.ThemeIcon('key');
      case 'offline':
        return new vscode.ThemeIcon('debug-disconnect');
      case 'error':
        return new vscode.ThemeIcon('error');
      default:
        return new vscode.ThemeIcon('question');
    }
  }
}

class RemoteProjectItem extends vscode.TreeItem {
  constructor(readonly host: RemoteHostRecord, readonly project: RemoteProjectRecord) {
    super(project.name || '(unnamed project)', project.sessions.length > 0 ? vscode.TreeItemCollapsibleState.Collapsed : vscode.TreeItemCollapsibleState.None);
    this.id = `remote-project:${host.name}:${project.name}`;
    this.contextValue = 'opencodeRemoteProject';
    this.iconPath = new vscode.ThemeIcon('folder-library');
    this.description = `${project.sessions.length} session${project.sessions.length === 1 ? '' : 's'}`;
    this.tooltip = `${project.name}\nHost: ${host.name}\nSessions: ${project.sessions.length}`;
  }
}

class RemoteSessionItem extends vscode.TreeItem {
  constructor(readonly host: RemoteHostRecord, readonly project: RemoteProjectRecord, readonly session: RemoteSessionRecord) {
    super(session.title || session.id, vscode.TreeItemCollapsibleState.None);
    this.id = `remote-session:${host.name}:${project.name}:${session.id}`;
    this.contextValue = 'opencodeRemoteSession';
    this.iconPath = this.statusToIcon(session.status);

    const relative = formatRelativeTime(session.lastActivity);
    this.description = [session.status || 'unknown', relative].filter(Boolean).join(' · ');
    this.tooltip = [
      `Session: ${session.title || session.id}`,
      `ID: ${session.id}`,
      `Host: ${host.name}`,
      `Project: ${project.name}`,
      `Status: ${session.status || 'unknown'}`,
      session.directory ? `Directory: ${session.directory}` : '',
      session.activity ? `Activity: ${session.activity}` : '',
      session.lastActivity ? `Last activity: ${session.lastActivity}` : ''
    ]
      .filter(Boolean)
      .join('\n');
  }

  private statusToIcon(status: string): vscode.ThemeIcon {
    switch (status.toLowerCase()) {
      case 'active':
        return new vscode.ThemeIcon('play-circle');
      case 'idle':
        return new vscode.ThemeIcon('clock');
      case 'archived':
        return new vscode.ThemeIcon('archive');
      default:
        return new vscode.ThemeIcon('history');
    }
  }
}

export class RemoteHostsTreeProvider implements vscode.TreeDataProvider<RemoteNode>, vscode.Disposable {
  private readonly changeEmitter = new vscode.EventEmitter<RemoteNode | undefined | null | void>();
  readonly onDidChangeTreeData = this.changeEmitter.event;

  private hosts: RemoteHostRecord[] = [];
  private cached = false;
  private stale = false;
  private partial = false;
  private lastScan = '';
  private warnings: string[] = [];
  private lastError = '';
  private disposed = false;
  private refreshTimer?: NodeJS.Timeout;

  constructor(
    private readonly getControlPlaneUrl: () => string,
    private readonly getAuthToken: () => string
  ) {}

  dispose(): void {
    this.disposed = true;
    if (this.refreshTimer) {
      clearTimeout(this.refreshTimer);
      this.refreshTimer = undefined;
    }
  }

  getTreeItem(element: RemoteNode): vscode.TreeItem {
    return element;
  }

  getChildren(element?: RemoteNode): Thenable<RemoteNode[]> {
    if (!element) {
      const items: RemoteNode[] = [];

      if (this.lastError) {
        items.push(new RemoteInfoItem('Remote scan error', this.lastError, 'error'));
      } else if (this.stale || this.partial || this.cached || this.lastScan || this.warnings.length > 0) {
        const statusParts: string[] = [];
        if (this.stale) {
          statusParts.push('stale');
        }
        if (this.partial) {
          statusParts.push('partial');
        }
        if (this.cached) {
          statusParts.push('cached');
        }
        if (this.lastScan) {
          statusParts.push(`last scan: ${this.lastScan}`);
        }
        const warning = this.warnings[0] ?? '';
        items.push(new RemoteInfoItem('Remote scan status', [statusParts.join(' · '), warning].filter(Boolean).join('\n'), this.stale || this.partial ? 'warning' : 'info'));
      }

      if (this.hosts.length === 0) {
        items.push(new RemoteInfoItem('No remote hosts discovered', 'Run refresh after configuring SSH hosts.'));
      } else {
        items.push(...this.hosts.map((host) => new RemoteHostItem(host)));
      }

      return Promise.resolve(items);
    }

    if (element instanceof RemoteHostItem) {
      return Promise.resolve(element.host.projects.map((project) => new RemoteProjectItem(element.host, project)));
    }

    if (element instanceof RemoteProjectItem) {
      return Promise.resolve(element.project.sessions.map((session) => new RemoteSessionItem(element.host, element.project, session)));
    }

    return Promise.resolve([]);
  }

  async start(): Promise<void> {
    await this.refresh(false);
  }

  async refresh(force: boolean): Promise<void> {
    try {
      const response = await this.request(this.remoteHostsPath(force), {
        method: 'GET',
        headers: { Accept: 'application/json' }
      });
      if (!response.ok) {
        throw new Error(`GET /api/remote/hosts failed (${response.status})`);
      }

      const body = (await response.json()) as unknown;
      const normalized = this.normalizeResponse(body);
      this.hosts = normalized.hosts;
      this.cached = normalized.cached;
      this.stale = normalized.stale;
      this.partial = normalized.partial;
      this.lastScan = normalized.lastScan ?? '';
      this.warnings = normalized.warnings ?? [];
      this.lastError = '';
    } catch (error) {
      this.lastError = this.formatError(error);
      if (this.hosts.length === 0) {
        this.cached = false;
        this.stale = false;
        this.partial = false;
        this.lastScan = '';
        this.warnings = [];
      }
      throw error;
    } finally {
      this.changeEmitter.fire();
      this.scheduleAutoRefresh();
    }
  }

  private scheduleAutoRefresh(): void {
    if (this.disposed) {
      return;
    }

    if (this.refreshTimer) {
      clearTimeout(this.refreshTimer);
      this.refreshTimer = undefined;
    }

    const seconds = vscode.workspace.getConfiguration('opencode').get<number>('remoteHostsAutoRefreshSeconds', 30);
    if (!Number.isFinite(seconds) || seconds <= 0) {
      return;
    }

    this.refreshTimer = setTimeout(() => {
      void this.refresh(false).catch(() => undefined);
    }, Math.max(1, Math.floor(seconds)) * 1000);
  }

  private remoteHostsPath(force: boolean): string {
    const params = new URLSearchParams();
    if (force) {
      params.set('refresh', 'true');
    }
    const sshConfigPath = vscode.workspace
      .getConfiguration('opencode')
      .get<string>('remoteSshConfigPath', '')
      .trim();
    if (sshConfigPath) {
      params.set('sshConfigPath', sshConfigPath);
    }
    const query = params.toString();
    return query ? `/api/remote/hosts?${query}` : '/api/remote/hosts';
  }

  private async request(path: string, init: RequestInit): Promise<Response> {
    const headers = new Headers(init.headers ?? undefined);
    if (!headers.has('Accept')) {
      headers.set('Accept', 'application/json');
    }

    const token = this.getAuthToken().trim();
    if (token) {
      headers.set('Authorization', `Bearer ${token}`);
    }

    return fetch(`${this.getControlPlaneUrl().replace(/\/+$/, '')}${path}`, {
      ...init,
      headers
    });
  }

  private normalizeResponse(value: unknown): RemoteHostsResponse {
    const record = asRecord(value);
    if (!record) {
      return {
        hosts: [],
        cached: false,
        stale: false,
        partial: false,
        warnings: []
      };
    }

    const hosts = Array.isArray(record.hosts)
      ? record.hosts
          .map((host) => this.normalizeHost(host))
          .filter((host): host is RemoteHostRecord => host !== null)
      : [];

    return {
      hosts,
      cached: Boolean(record.cached),
      stale: Boolean(record.stale),
      partial: Boolean(record.partial),
      lastScan: readString(record.lastScan),
      warnings: readStringArray(record.warnings)
    };
  }

  private normalizeHost(value: unknown): RemoteHostRecord | null {
    const record = asRecord(value);
    if (!record) {
      return null;
    }

    const name = readString(record.name);
    if (!name) {
      return null;
    }

    const projects = Array.isArray(record.projects)
      ? record.projects
          .map((project) => this.normalizeProject(project))
          .filter((project): project is RemoteProjectRecord => project !== null)
      : [];

    return {
      name,
      address: readString(record.address),
      user: readString(record.user),
      label: readString(record.label) || name,
      status: readString(record.status) || 'unknown',
      sessionCount: readNumber(record.sessionCount),
      lastSeen: readString(record.lastSeen),
      lastError: readString(record.lastError),
      transport: readString(record.transport),
      transportError: readString(record.transportError),
      projects
    };
  }

  private normalizeProject(value: unknown): RemoteProjectRecord | null {
    const record = asRecord(value);
    if (!record) {
      return null;
    }

    const name = readString(record.name);
    if (!name) {
      return null;
    }

    const sessions = Array.isArray(record.sessions)
      ? record.sessions
          .map((session) => this.normalizeSession(session))
          .filter((session): session is RemoteSessionRecord => session !== null)
      : [];

    return {
      name,
      sessions
    };
  }

  private normalizeSession(value: unknown): RemoteSessionRecord | null {
    const record = asRecord(value);
    if (!record) {
      return null;
    }

    const id = readString(record.id);
    if (!id) {
      return null;
    }

    return {
      id,
      title: readString(record.title),
      directory: readString(record.directory),
      status: readString(record.status) || 'unknown',
      activity: readString(record.activity),
      lastActivity: readString(record.lastActivity)
    };
  }

  private formatError(error: unknown): string {
    if (error instanceof Error) {
      return error.message;
    }
    return String(error);
  }
}

function asRecord(value: unknown): Record<string, unknown> | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    return null;
  }
  return value as Record<string, unknown>;
}

function readString(value: unknown): string {
  if (typeof value !== 'string') {
    return '';
  }
  return value.trim();
}

function readNumber(value: unknown): number {
  if (typeof value === 'number' && Number.isFinite(value)) {
    return value;
  }
  if (typeof value === 'string' && value.trim()) {
    const parsed = Number(value);
    if (Number.isFinite(parsed)) {
      return parsed;
    }
  }
  return 0;
}

function readStringArray(value: unknown): string[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value
    .map((entry) => (typeof entry === 'string' ? entry.trim() : ''))
    .filter((entry): entry is string => Boolean(entry));
}

function formatRelativeTime(isoTimestamp: string | undefined): string {
  if (!isoTimestamp) {
    return '';
  }
  const ts = new Date(isoTimestamp);
  if (Number.isNaN(ts.getTime())) {
    return '';
  }

  const deltaMs = Date.now() - ts.getTime();
  if (!Number.isFinite(deltaMs)) {
    return '';
  }
  if (deltaMs < 60_000) {
    return 'just now';
  }
  const minutes = Math.floor(deltaMs / 60_000);
  if (minutes < 60) {
    return `${minutes}m ago`;
  }
  const hours = Math.floor(minutes / 60);
  if (hours < 24) {
    return `${hours}h ago`;
  }
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
}

const ANSI_REGEX = new RegExp('\\u001B\\[[0-9;]*[A-Za-z]', 'g');

const DEFAULT_SUCCESS_TTL_MS = 30_000;
const DEFAULT_ERROR_TTL_MS = 2_000;
const DEFAULT_MAX_LINES = 4;
const DEFAULT_MAX_RUNES = 280;

export interface LatestBlockSnapshot {
  block: string;
  error: string;
  fetchedAt: number;
}

interface LatestBlockClientOptions {
  successTtlMs?: number;
  errorTtlMs?: number;
}

type RequestFn = (path: string, init: RequestInit) => Promise<Response>;

export class LatestBlockClient {
  private readonly cache = new Map<string, LatestBlockSnapshot>();
  private readonly inFlight = new Map<string, Promise<LatestBlockSnapshot>>();
  private readonly successTtlMs: number;
  private readonly errorTtlMs: number;

  constructor(
    private readonly request: RequestFn,
    options?: LatestBlockClientOptions
  ) {
    this.successTtlMs = options?.successTtlMs ?? DEFAULT_SUCCESS_TTL_MS;
    this.errorTtlMs = options?.errorTtlMs ?? DEFAULT_ERROR_TTL_MS;
  }

  prune(activeSessionIds: readonly string[]): void {
    const active = new Set(activeSessionIds);
    for (const id of this.cache.keys()) {
      if (!active.has(id)) {
        this.cache.delete(id);
      }
    }
    for (const id of this.inFlight.keys()) {
      if (!active.has(id)) {
        this.inFlight.delete(id);
      }
    }
  }

  isInFlight(sessionId: string): boolean {
    return this.inFlight.has(sessionId);
  }

  getCached(sessionId: string): LatestBlockSnapshot | undefined {
    const entry = this.cache.get(sessionId);
    if (!entry) {
      return undefined;
    }

    const ttl = entry.error ? this.errorTtlMs : this.successTtlMs;
    if (Date.now() - entry.fetchedAt > ttl) {
      this.cache.delete(sessionId);
      return undefined;
    }

    return entry;
  }

  async getOrFetch(sessionId: string): Promise<LatestBlockSnapshot> {
    const cached = this.getCached(sessionId);
    if (cached) {
      return cached;
    }

    const active = this.inFlight.get(sessionId);
    if (active) {
      return active;
    }

    const task = this.fetchLatestBlock(sessionId).finally(() => {
      this.inFlight.delete(sessionId);
    });

    this.inFlight.set(sessionId, task);
    return task;
  }

  private async fetchLatestBlock(sessionId: string): Promise<LatestBlockSnapshot> {
    try {
      const response = await this.request(`/api/sessions/${encodeURIComponent(sessionId)}/chat`, {
        method: 'GET',
        headers: { Accept: 'application/json' }
      });

      if (!response.ok) {
        throw new Error(`GET /api/sessions/{id}/chat failed (${response.status})`);
      }

      const payload = await response.text();
      const block = extractLatestConversationBlock(payload);
      const snapshot: LatestBlockSnapshot = {
        block,
        error: '',
        fetchedAt: Date.now()
      };
      this.cache.set(sessionId, snapshot);
      return snapshot;
    } catch (error) {
      const snapshot: LatestBlockSnapshot = {
        block: '',
        error: sanitizeError(error),
        fetchedAt: Date.now()
      };
      this.cache.set(sessionId, snapshot);
      return snapshot;
    }
  }
}

function extractLatestConversationBlock(raw: string): string {
  const payload = parsePayload(raw);
  const messages = normalizeMessages(payload);

  for (let messageIndex = messages.length - 1; messageIndex >= 0; messageIndex--) {
    const message = asRecord(messages[messageIndex]);
    if (!message) {
      continue;
    }

    const parts = Array.isArray(message.parts) ? message.parts : [];
    let toolFallback = '';

    for (let partIndex = parts.length - 1; partIndex >= 0; partIndex--) {
      const part = asRecord(parts[partIndex]);
      if (!part) {
        continue;
      }

      const type = valueAsString(part.type).toLowerCase();
      const ignored = Boolean(part.ignored);

      if (type === 'text' || (!type && !ignored)) {
        const text = clampConversationBlock(
          firstNonEmptyString(part.text, part.content, part.delta)
        );
        if (text) {
          return text;
        }
      }

      if (!toolFallback && type.includes('tool')) {
        const state = asRecord(part.state);
        const status = valueAsString(state?.status).toLowerCase();
        if (!status || status === 'completed') {
          const output = clampConversationBlock(firstNonEmptyString(state?.output, part.output));
          if (output) {
            toolFallback = output;
          }
        }
      }
    }

    const direct = clampConversationBlock(firstNonEmptyString(message.content, message.text, message.delta));
    if (direct) {
      return direct;
    }

    if (toolFallback) {
      return toolFallback;
    }
  }

  return '';
}

function parsePayload(raw: string): unknown {
  const trimmed = raw.trim();
  if (!trimmed) {
    return [];
  }

  const parsed = safeJsonParse(trimmed);
  if (parsed !== undefined) {
    return parsed;
  }

  const recovered = recoverJSONEnvelope(trimmed);
  if (recovered === undefined) {
    return [];
  }

  return recovered;
}

function safeJsonParse(raw: string): unknown | undefined {
  try {
    return JSON.parse(raw) as unknown;
  } catch {
    return undefined;
  }
}

function recoverJSONEnvelope(raw: string): unknown | undefined {
  const arrayStart = raw.indexOf('[');
  const arrayEnd = raw.lastIndexOf(']');
  if (arrayStart >= 0 && arrayEnd > arrayStart) {
    const parsed = safeJsonParse(raw.slice(arrayStart, arrayEnd + 1));
    if (parsed !== undefined) {
      return parsed;
    }
  }

  const objectStart = raw.indexOf('{');
  const objectEnd = raw.lastIndexOf('}');
  if (objectStart >= 0 && objectEnd > objectStart) {
    return safeJsonParse(raw.slice(objectStart, objectEnd + 1));
  }

  return undefined;
}

function normalizeMessages(payload: unknown): unknown[] {
  if (Array.isArray(payload)) {
    return payload;
  }

  const object = asRecord(payload);
  const nestedMessages = object?.messages;
  return Array.isArray(nestedMessages) ? nestedMessages : [];
}

function clampConversationBlock(input: string): string {
  if (!input) {
    return '';
  }

  let normalized = input.replace(/\r\n/g, '\n').replace(/\r/g, '\n').replace(ANSI_REGEX, '').trim();
  if (!normalized) {
    return '';
  }

  const lines = normalized
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean);

  if (lines.length === 0) {
    return '';
  }

  if (lines.length > DEFAULT_MAX_LINES) {
    normalized = `${lines.slice(0, DEFAULT_MAX_LINES).join('\n')}\n…`;
  } else {
    normalized = lines.join('\n');
  }

  const runes = [...normalized];
  if (runes.length > DEFAULT_MAX_RUNES) {
    return `${runes.slice(0, DEFAULT_MAX_RUNES).join('')}…`;
  }

  return normalized;
}

function sanitizeError(error: unknown): string {
  const source = error instanceof Error ? error.message : String(error ?? 'unknown error');
  const compact = source.replace(/\s+/g, ' ').trim();
  if (!compact) {
    return 'unknown error';
  }
  return compact.length <= 120 ? compact : `${compact.slice(0, 120)}…`;
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    return undefined;
  }
  return value as Record<string, unknown>;
}

function valueAsString(value: unknown): string {
  return typeof value === 'string' ? value.trim() : '';
}

function firstNonEmptyString(...values: unknown[]): string {
  for (const value of values) {
    if (typeof value === 'string' && value.trim()) {
      return value.trim();
    }
  }
  return '';
}

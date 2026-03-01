# OpenClaw × OpenCodeRouter Autodispatch Research & Takeaways

Date: 2026-02-28
Scope: Exported research for programming task autodispatching (TickTick -> OpenClaw -> project-specific OpenCode backend via OpenCodeRouter).

## 1) Context Gathering Method (Analyze Mode)

Parallel context sources used:

- **Explore agent #1**: mapped router API, slug/path behavior, and caveats from code.
- **Explore agent #2**: mapped in-repo docs/examples and automation gaps.
- **Librarian agent**: compared OpenClaw plugin vs skill vs cron/memory for production autodispatch.
- **Direct tools**:
  - Grep over Go/Markdown for endpoint and mapping references.
  - AST-grep confirmation for `handleAPIResolve` implementation.
  - GitHub code search for OpenClaw extension mechanisms (`registerTool`, `registerService`).

Note: local LSP (`gopls`) is not installed in this environment, so symbol validation relied on direct file reads + AST/grep evidence.

## 2) Verified Router Contract (Code-Backed)

### API endpoints for external dispatch

From `internal/proxy/proxy.go`:

- `GET /api/backends` -> returns list entries with:
  - `slug`, `project_name`, `project_path`, `port`, `version`, `domain`, `path_prefix`, `url`, `last_seen`
- `GET /api/resolve?path=...` -> resolves a single project path to same routing info fields.
- Path routing remains `/{slug}/...` (prefix stripped when proxied).

Key references:

- `internal/proxy/proxy.go` lines ~161-197 (`handleAPIBackends`)
- `internal/proxy/proxy.go` lines ~213-249 (`handleAPIResolve`)
- `internal/proxy/proxy.go` lines ~39-71 (`ServeHTTP` route dispatch order)

### Slug and project mapping behavior

From `internal/registry/registry.go`:

- Slug source: `Slugify(projectPath)` (path-derived, not user-provided slug).
- Collision behavior: if two different paths produce same slug, router disambiguates with `"<slug>-<port>"`.
- `LookupByPath(path)` resolution order:
  1. exact `ProjectPath` match
  2. fallback slug lookup from `Slugify(path)`

Key references:

- `internal/registry/registry.go` lines ~48-91 (`Upsert`)
- `internal/registry/registry.go` lines ~138-159 (`LookupByPath`)
- `internal/registry/registry.go` lines ~191+ (`Slugify`)

### Project name display behavior

From `internal/scanner/scanner.go`:

- `project_name` is derived from `filepath.Base(projectPath)` so display name matches slug basis.

Key reference:

- `internal/scanner/scanner.go` lines ~139-146

### mDNS startup behavior

From `main.go`:

- mDNS sync loop now performs an initial sync after startup delay, then periodic sync at `scan-interval`.

Key reference:

- `main.go` lines ~96-118

## 3) Practical Caveats for External Agents

1. **Returned `url` uses localhost**
   - `url` is generated as `http://localhost:<listenPort>/<slug>/`.
   - For remote/cluster agents, replace host with reachable router host.
   - Evidence: `internal/proxy/proxy.go` lines ~190 and ~246.

2. **Reserved prefix collision risk (`/api/...`)**
   - `ServeHTTP` currently attempts path-based routing before API switch.
   - A project slug `api` could shadow API routes.
   - Evidence: `internal/proxy/proxy.go` lines ~49-67.

3. **Path matching is string-sensitive**
   - `LookupByPath` exact match first, slug fallback second.
   - Normalize the same canonical absolute path in dispatcher inputs.

4. **Freshness window matters**
   - Registry prunes stale backends by `stale-after` (default 30s).
   - Dispatcher should treat stale/404 resolve as retryable.

## 4) OpenClaw Integration Findings (Evidence-Driven)

Validated from OpenClaw repository patterns (GitHub code search):

- OpenClaw extensions register callable tools via `api.registerTool(...)`.
- OpenClaw extensions can run background logic via `api.registerService(...)`.
- This is suitable for an always-on dispatcher loop consuming TickTick tasks.

Representative evidence (openclaw/openclaw):

- `extensions/llm-task/index.ts` (`registerTool`)
- `extensions/memory-lancedb/index.ts` (`registerTool`, `registerService`)
- `extensions/diagnostics-otel/index.ts` (`registerService`)
- `src/plugins/registry.ts` (plugin API wiring includes `registerTool`, `registerService`, etc.)

`ticktick` was not found as a built-in extension name in quick repo search; assume your TickTick capability is custom/installed in your environment.

## 5) Architecture Decision: Plugin vs Skill vs Cron+Memory

### Decision matrix (for production-ish autodispatch)

| Option | Strengths | Weaknesses | Fit |
|---|---|---|---|
| **OpenClaw Plugin (+ service loop)** | Native tool/service lifecycle, can own retries/locks/state, integrates with OpenClaw runtime | More implementation effort than a plain skill | **Best primary engine** |
| **ClawHub Skill only** | Fast to author, good behavioral policy layer | Weak durability/state/retry semantics if used alone for autonomous queue processing | Good as policy layer, not core dispatcher |
| **Cron + memory only** | Very robust operationally, easy isolation | Bypasses OpenClaw orchestration/intelligence unless integrated | Good watchdog/reconciler |

### Recommended hybrid

Use:

- **Plugin as primary dispatcher** (poll/consume TickTick tasks, resolve router path, dispatch work)
- **Cron as watchdog/reconciler** (restart loop, recover stuck tasks, periodic sanity checks)
- **Memory for preferences only** (routing heuristics), **not** as queue state source of truth

## 6) Autodispatch Blueprint (Actionable)

State model (durable store keyed by `ticktick_task_id`):

- `ready -> dispatching -> in_progress -> done`
- failure branch: `-> retry_scheduled -> blocked`

Core dispatch algorithm:

1. Pull candidate tasks from TickTick (e.g., tag `autocode`, status `ready`).
2. Extract `project_path` from task metadata.
3. Resolve backend via:
   - `GET /api/resolve?path=<project_path>`
4. Use returned `url` to call project-specific OpenCode endpoint.
5. Persist attempt state + post status/comment back to TickTick.
6. On transient failure, retry with exponential backoff.
7. If unresolved/stale repeatedly, mark blocked + notify.

Operational guards:

- Idempotency lock per task ID.
- Max-attempt cap + dead-letter/blocked state.
- Host rewrite for non-local callers (`localhost` -> router host).

## 7) Takeaways (Executive Summary)

1. **Use router path resolution as the stable contract**: `project_path -> /api/resolve -> url`.
2. **Do not derive slug externally** for dispatch correctness; let router own collisions and mapping.
3. **Build autodispatch core in an OpenClaw plugin service**, not a prompt-only skill.
4. **Keep queue truth in durable task/state storage**, not ephemeral memory/context.
5. **Run cron as reliability backstop**, not as sole business logic engine.
6. **Handle `url` host rewriting** for remote workers since current API emits localhost URLs.

## 8) Source Pointers

In-repo:

- `internal/proxy/proxy.go`
- `internal/registry/registry.go`
- `internal/scanner/scanner.go`
- `main.go`
- `README.md`

OpenClaw evidence (GitHub):

- `openclaw/openclaw/extensions/llm-task/index.ts`
- `openclaw/openclaw/extensions/memory-lancedb/index.ts`
- `openclaw/openclaw/extensions/diagnostics-otel/index.ts`
- `openclaw/openclaw/src/plugins/registry.ts`

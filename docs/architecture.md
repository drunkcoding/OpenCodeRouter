# OpenCodeRouter Architecture Guide

This document describes the control-plane architecture implemented in this worktree.
It focuses on component boundaries, runtime data flow, configuration defaults,
security behavior, and failure/recovery semantics.

## 1. Scope

The codebase contains two major runtime surfaces:

1. **Control Plane server** (`main.go` + `internal/*`)  
   Hosts discovery, reverse proxy, session lifecycle APIs, SSE, terminal websocket
   bridge, browser dashboard assets, and session scrollback endpoints.

2. **VS Code extension** (`vscode-extension/`)  
   Uses control-plane HTTP/SSE/WebSocket APIs for session tree, chat, terminal,
   and diff workflow integration.

The architecture below applies to the control plane plus browser and extension
clients. This worktree does not include the historical remote TUI or SSH
fleet-aware control plane, all management happens against a single local
control-plane process that you can still expose remotely with standard SSH
port forwarding.

## 2. System Architecture (ASCII)

```text
                                        +-----------------------------+
                                        |         VS Code             |
                                        |  - Session Tree             |
                                        |  - Chat Webview             |
                                        |  - Terminal Profile (PTY)   |
                                        |  - Diff Edit Manager        |
                                        +--------------+--------------+
                                                       |
                                                       | HTTP / SSE / WS
                                                       v
+------------------------------+        +--------------+--------------+        +------------------------------+
|         Browser UI           |        |      OpenCodeRouter         |        |      OpenCode Daemon(s)      |
|  / (dashboard + terminal)    |<------>|  Control Plane Server       |<------>|  opencode serve per session  |
|  - sessions table            |  HTTP  |  - api router               |  HTTP  |  - /global/health            |
|  - SSE indicator             |  SSE   |  - sessions handler         |        |  - /session APIs             |
|  - terminal xterm            |  WS    |  - events handler (SSE)     |  WS    |  - terminal transport        |
|  - chat panel                |        |  - terminal ws bridge        |        |                              |
+------------------------------+        |  - proxy + scanner + registry|        +------------------------------+
                                        |  - scrollback cache (JSONL)  |
                                        +--------------+--------------+
                                                       |
                                                       | local process mgmt
                                                       v
                                        +-----------------------------+
                                        | Session Manager             |
                                        | - create/stop/restart       |
                                        | - health checks + circuit    |
                                        | - attach terminal            |
                                        | - event publication          |
                                        +-----------------------------+
```

## 3. Runtime Components and Boundaries

### 3.1 `main.go` (composition root)

Responsibilities:

- Parses CLI flags and builds `config.Config` (`internal/config/config.go` defaults).
- Loads auth/cors settings via `auth.LoadFromEnv()`.
- Creates and wires:
  - `registry.Registry`
  - `scanner.Scanner`
  - `proxy.Proxy`
  - `session.Manager`
  - `api.Router`
  - optional mDNS advertiser
- Starts HTTP server and graceful shutdown path.
- Performs startup orphan-process detection and optional cleanup offer
  (`--cleanup-orphans` or `OCR_CLEANUP_ORPHANS=1`).

Boundary notes:

- `main.go` owns object lifecycle and orchestration only; business behavior lives
  in internal packages.
- Startup cleanup is explicit-action only; no silent destructive default.

### 3.2 `internal/config`

Responsibilities:

- Defines static control-plane defaults and validation constraints.
- Provides domain naming helper and outbound IP helper.

Boundary notes:

- No HTTP, no process management, no storage logic.

### 3.3 `internal/auth`

Responsibilities:

- Defines auth/cors configuration model.
- Loads env-backed auth settings.
- Middleware integration occurs at API router boundary.

Boundary notes:

- Security policy is centralized by middleware; handlers do not duplicate auth
  checks.

### 3.4 `internal/scanner` + `internal/registry`

Responsibilities:

- Scanner probes configured local port range for daemon health/project/session info.
- Registry keeps thread-safe backend/session index and stale pruning.

Boundary notes:

- Scanner discovers runtime backends and refreshes registry snapshots.
- Registry is shared state for proxy routing and status views.

### 3.5 `internal/session` manager

Responsibilities:

- Session lifecycle API: create/get/list/stop/restart/delete/attach terminal/health.
- Process supervision (`opencode serve` child process start + wait handling).
- Health loop with circuit-breaker behavior:
  - threshold: 3 consecutive unhealthy probes by default
  - cooldown: 30s by default before next probe
  - reset on healthy probe and stop/restart paths
- Publishes session events to event bus.

Boundary notes:

- Manager is authoritative state for session lifecycle and health.
- Terminal WS handlers delegate terminal attachment via manager interface.

### 3.6 `internal/api` router and handlers

Responsibilities:

- Mounts REST and SSE endpoints:
  - `/api/sessions` and `/api/sessions/{id}/*`
  - `/api/events`
  - `/api/sessions/{id}/scrollback`
  - `/ws/terminal/{session-id}`
- Session handler translates manager errors into stable HTTP status/code payloads
  via `internal/errors` mapping.
- Event handler converts internal event types (including `session.health_changed`)
  into SSE event stream (`session.health`).

Boundary notes:

- API layer owns transport contracts (JSON + SSE + WS), not core lifecycle logic.
- Fallback routing delegates to proxy/static UI handler.

### 3.7 `internal/terminal` bridge

Responsibilities:

- Upgrades websocket connections and bridges client <-> daemon terminal streams.
- Validates session existence and health before attach.
- Appends terminal output to scrollback cache for reconnect hydration.

Boundary notes:

- Bridge is transport-level; terminal session ownership remains in session manager.

### 3.8 Browser dashboard (`web/`)

Responsibilities:

- Session table and action controls (attach/stop/restart/delete).
- SSE status indicator states (`STREAM_ACTIVE`, `RECONNECTING`, `DISCONNECTED`).
- Terminal reconnect UX with bounded exponential backoff.
- Scrollback hydration before terminal websocket attach.
- Chat panel rendering and streaming support.

Boundary notes:

- Browser is a thin API/SSE/WS client; no daemon-direct calls.

### 3.9 VS Code extension (`vscode-extension/`)

Responsibilities:

- Session tree provider with SSE-driven refresh and connection status bar.
- Resilient request path with bounded retry and stale-data fallback.
- Chat webview integration.
- PTY-backed terminal websocket bridge.
- Diff staging/preview/apply/reject workflow.

Boundary notes:

- Extension host performs control-plane communication; webview stays message-based.

## 4. API-Level Data Flow

### 4.1 Session lifecycle flow

1. Client `POST /api/sessions` (workspace + optional labels).
2. Sessions handler validates payload and calls manager `Create`.
3. Manager allocates port, launches process, stores session, emits
   `session.created` event.
4. Client receives normalized session view with health snapshot.
5. Subsequent operations (`stop`, `restart`, `delete`) map to manager methods
   and publish corresponding events.

Error mapping:

- `WORKSPACE_PATH_REQUIRED`, `WORKSPACE_PATH_INVALID` -> `400`
- `SESSION_ALREADY_EXISTS`, `SESSION_STOPPED` -> `409`
- `SESSION_NOT_FOUND` -> `404`
- `NO_AVAILABLE_SESSION_PORTS` -> `503`
- `TERMINAL_ATTACH_UNAVAILABLE`, `DAEMON_UNHEALTHY` -> `503`

### 4.2 Terminal data flow

1. Browser/extension requests websocket upgrade at `/ws/terminal/{session-id}`.
2. Handler checks method, upgrade headers, session existence, and health.
3. Handler calls `AttachTerminal` on manager and starts bridge.
4. Client input is forwarded to daemon terminal stream.
5. Daemon output is forwarded to client and persisted to scrollback cache.
6. On disconnect, client-side reconnect logic controls retry/backoff behavior.

### 4.3 Agent chat flow

1. Client `POST /api/sessions/{id}/chat` with prompt payload.
2. Sessions handler creates daemon client for session daemon port.
3. Handler proxies daemon message stream back as SSE-style response chunks.
4. Browser/extension incrementally renders assistant/tool output.

History path:

- `GET /api/sessions/{id}/chat` -> daemon message history passthrough.

### 4.4 Scrollback flow

1. Terminal output appends entries to JSONL scrollback cache.
2. Client reconnect path requests
   `GET /api/sessions/{id}/scrollback?type=terminal_output&limit=...`.
3. Handler applies filtering + offset/limit and returns entries.
4. Client hydrates terminal before opening live websocket.

## 5. Configuration Reference (Defaults + Toggles)

### 5.1 CLI/config defaults (`internal/config/config.go` + `main.go` flags)

| Setting | Default | Source | Notes |
|---|---:|---|---|
| listen port | `8080` | `Config.Defaults()` | `--port` |
| listen addr | `0.0.0.0:8080` | `Config.Defaults()` + hostname flag | host by `--hostname` |
| username | OS user | `user.Current()` | `--username` override |
| scan start | `30000` | `Config.Defaults()` | `--scan-start` |
| scan end | `31000` | `Config.Defaults()` | `--scan-end` |
| scan interval | `5s` | `Config.Defaults()` | `--scan-interval` |
| scan concurrency | `20` | `Config.Defaults()` | `--scan-concurrency` |
| probe timeout | `800ms` | `Config.Defaults()` | `--probe-timeout` |
| stale after | `30s` | `Config.Defaults()` | `--stale-after` |
| mDNS enabled | `true` | `Config.Defaults()` | `--mdns` |
| mDNS service type | `_opencode._tcp` | `Config.Defaults()` | static default |
| startup orphan cleanup | `false` | `main.go` | opt-in `--cleanup-orphans` |

Validation constraints:

- port ranges must be 1..65535
- `scan-end >= scan-start`
- `scan-interval >= 1s`
- username cannot be empty

### 5.2 Session manager defaults (`internal/session/manager.go`)

| Setting | Default | Notes |
|---|---:|---|
| session port range | `30100..31100` | derived from scan range + 100 via `config.Config.SessionPortStart/End` (manager falls back to `30000..31000` if no override provided) |
| health interval | `10s` | periodic health loop |
| health timeout | `2s` | per-probe context timeout |
| health fail threshold | `3` | opens circuit breaker |
| circuit cooldown | `30s` | next probe delay when circuit open |
| stop timeout | `5s` | graceful stop/kill fallback |
| opencode binary | `opencode` | default process starter command |

### 5.3 Auth/CORS environment (`internal/auth/config.go`)

| Env | Default | Meaning |
|---|---|---|
| `OCR_AUTH_ENABLED` | `false` | enables auth middleware gate |
| `OCR_AUTH_BEARER_TOKENS` | empty | CSV list of accepted bearer tokens |
| `OCR_AUTH_BASIC` | empty | CSV `user:pass` pairs |
| `OCR_CORS_ALLOW_ORIGINS` | `*` | CSV CORS allow-list |

Bypass paths default:

- `/api/health`
- `/api/backends`

### 5.4 Startup cleanup env toggle (`main.go`)

| Env | Default | Meaning |
|---|---|---|
| `OCR_CLEANUP_ORPHANS` | off | enables startup SIGTERM cleanup for detected orphan `opencode` listeners in scan range |

### 5.5 VS Code extension runtime settings

Defined in `vscode-extension/package.json`:

| Setting | Default | Meaning |
|---|---|---|
| `opencode.controlPlaneUrl` | `http://localhost:8080` | base URL for control-plane API/SSE/WS |
| `opencode.authToken` | empty | optional bearer token for authenticated control planes |

## 6. Security Model

### 6.1 Network binding

- Default bind is `0.0.0.0` (server reachable on network interfaces).
- For localhost-only operation, run with `--hostname 127.0.0.1`.

### 6.2 Authentication

- Optional middleware in front of all API/routes via `auth.Middleware`.
- Supports bearer token and basic auth based on environment configuration.
- Some health/backend endpoints can be bypassed by default path policy.

### 6.3 CORS

- Default CORS allow origins: `*`.
- Can be restricted by `OCR_CORS_ALLOW_ORIGINS` CSV values.

### 6.4 Trust boundaries

- Browser and extension are untrusted clients from server perspective; all
  operations go through HTTP API checks.
- Session manager and daemon process orchestration run server-side only.

### 6.5 Local process controls

- Orphan cleanup is opt-in; default behavior is warning-only.
- Cleanup scope is bounded to configured scan port range and `opencode` listener
  detection.

## 7. Failure Modes and Recovery Behavior

### 7.1 Session daemon unavailable

Symptoms:

- health probes fail
- terminal attach returns service unavailable

Behavior:

- manager marks health unhealthy and can transition session status to `error`
- SSE emits `session.health`
- dashboard row shows error state with start/restart affordance

Recovery:

- explicit `restart`/`start` action from UI/extension/API
- no automatic daemon restart behavior

### 7.2 Port exhaustion for new session

Symptoms:

- create session fails with `NO_AVAILABLE_SESSION_PORTS`

Behavior:

- API returns descriptive `503` with stable error code

Recovery:

- stop/delete existing sessions
- widen configured scan/session port ranges

### 7.3 SSE disruption

Symptoms:

- event stream disconnect/errors

Behavior:

- browser indicator transitions to reconnecting/disconnected states
- extension status bar transitions to disconnected/error with retry loop

Recovery:

- automatic reconnect loops with bounded delay logic

### 7.4 Terminal websocket disruption

Symptoms:

- terminal websocket close/error

Behavior:

- browser terminal prints reconnect message and retries with exponential backoff
- extension terminal bridge performs reconnect strategy per bridge implementation

Recovery:

- automatic reconnect path
- user can detach/reattach terminal

### 7.5 Control-plane API temporarily unavailable (extension)

Symptoms:

- session fetch failures / retryable statuses

Behavior:

- bounded backoff retries
- stale session data retained and marked as stale
- warning with Retry action

Recovery:

- retry from warning action or refresh command

### 7.6 Startup orphan listeners in scan range

Symptoms:

- pre-existing `opencode serve` listeners occupy scan ports

Behavior:

- startup warning logs orphan candidates and cleanup hint
- optional cleanup if explicitly enabled

Recovery:

- rerun with explicit cleanup toggle
- or manually terminate orphan listeners

## 8. Operational Notes

1. Browser dashboard and VS Code extension both depend on control-plane API/SSE.
2. Terminal attach requires session terminal connectivity to daemon; environment
   limitations can surface as 502/503 attach failures.
3. Scrollback hydration reduces terminal reconnect blind spots by loading cached
   output before live websocket starts.
4. mDNS is optional; path-based routing and direct API usage remain available when
   mDNS is disabled.

## 9. File-Level Reference Map

- Composition root: `main.go`
- Config defaults/validation: `internal/config/config.go`
- Auth/env config: `internal/auth/config.go`
- API router: `internal/api/router.go`
- Session lifecycle API: `internal/api/sessions.go`
- SSE stream: `internal/api/events.go`
- Scrollback API: `internal/api/scrollback.go`
- Session manager core: `internal/session/manager.go`
- Terminal websocket endpoint: `internal/terminal/handler.go`
- Browser client: `web/js/main.js`, `web/index.html`
- VS Code extension host: `vscode-extension/src/extension.ts`

## 10. Summary

OpenCodeRouter implements a control-plane architecture with clear layering:

- discovery/proxy plane (`scanner`, `registry`, `proxy`)
- session lifecycle and health supervision (`session.Manager`)
- transport adapters (`internal/api`, `internal/terminal`)
- clients (browser dashboard and VS Code extension)

Task 20–24 capabilities (terminal bridge, chat/diff integration, scrollback
hydration, resilience/error handling, circuit breaker) are represented in this
architecture and documented with concrete runtime defaults and failure behavior.

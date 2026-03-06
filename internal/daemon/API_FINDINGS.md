# OpenCode Daemon API Findings (Spike)

Date: 2026-03-05  
Task: 5 — Validate OpenCode daemon API assumptions (spike)  
Daemon binary: `opencode 1.2.17`

## Scope

This is an integration **spike** only. It validates endpoint assumptions for Task 14 planning and records deltas between assumed contracts and observed daemon behavior.

## Assumption Matrix

| # | Assumption | Status | Evidence |
|---|---|---|---|
| 1 | `GET /doc` serves parseable OpenAPI spec with required routes | **CONFIRMED** | `TestSpikeDocEndpointOpenAPI` passed. Log: `openapi=3.1.1 path_count=85`; required routes present (`/event`, `/project/current`, `/session`, `/session/{sessionID}`, `/session/{sessionID}/message`). |
| 2 | `POST /session` response shape matches future `SessionHandle` (`id`, `daemon port`, `workspace path`, `status`, `created`, `last activity`, `attached clients`) | **DENIED** | `TestSpikeCreateSessionShape` log: `map[attached_clients:false created_at:true daemon_port:false id:true last_activity:false status:false workspace_path:true]`. Response has `id`, `directory`, `time`; missing `daemon_port`, `status`, `last_activity`, `attached_clients`. |
| 3 | `GET /session/{id}/messages` lists messages and supports SSE streaming | **DENIED** | `TestSpikeSessionMessagesEndpoints` log: `/messages` returned `text/html;charset=UTF-8` (web shell), not message list/SSE. `GET /session/{id}/message` (singular) returned JSON list (`0 entries` in fresh session). |
| 4 | `POST /session/{id}/message` itself streams token SSE response | **DENIED** | `TestSpikePostMessageAndTokenEvents`: endpoint returned `Content-Type: application/json` (single JSON response), while token deltas were observed on `/event` (`message.part.delta`). |
| 5 | `GET /event` emits SSE events for session state changes | **CONFIRMED** | `TestSpikeEventEndpointReceivesSessionUpdates` patched session title and received matching `session.updated` event on `/event`. |
| 6 | Two clients can attach simultaneously and both receive same session SSE updates | **CONFIRMED** | `TestSpikeMultiClientEventStreams` opened two `/event` streams; both observed `session.updated` for same `sessionID`. |
| 7 | `GET /session/{id}` includes file list, working directory, and agent info | **DENIED** | `TestSpikeSessionDetailFields` keys: `[directory id projectID slug time title version]`; booleans: `working_directory=true files=false agent=false`. |

## Additional Endpoint Deltas (important for Task 14)

1. **Singular vs plural API paths differ from assumptions**
   - API routes are singular (`/session`, `/session/{id}/message`)
   - `/sessions` and `/session/{id}/messages` resolved to web UI HTML in observed environment.

2. **Token streaming source**
   - Streaming token deltas (`message.part.delta`) are seen on global SSE endpoint `/event`, not as SSE response body from `POST /session/{id}/message`.

3. **OpenAPI location**
   - `/doc` returns JSON OpenAPI.
   - `/openapi.json` returned web shell HTML in this environment.

4. **Scanner contract mismatch risk**
   - Existing scanner expects `/project/current` shape with `name` and `path`.
   - Observed payload uses fields such as `worktree` and `sandboxes`; this can cause fallback registration paths unless scanner parsing is updated.

## Verification Commands (exact)

```bash
go test -count=1 -v ./internal/daemon/...
go build ./internal/daemon/...
PATH="/usr/local/go/bin:/usr/bin:/bin" go test -count=1 -run TestSpikeDocEndpointOpenAPI -v ./internal/daemon/...
```

## Test Invocation Output

```text
=== RUN   TestSpikeDocEndpointOpenAPI
    spike_test.go:298: /doc openapi=3.1.1 path_count=85
--- PASS: TestSpikeDocEndpointOpenAPI (5.10s)
=== RUN   TestSpikeCreateSessionShape
    spike_test.go:325: create-session field coverage=map[attached_clients:false created_at:true daemon_port:false id:true last_activity:false status:false workspace_path:true]
--- PASS: TestSpikeCreateSessionShape (2.65s)
=== RUN   TestSpikeSessionMessagesEndpoints
    spike_test.go:347: plural messages endpoint status=200 content-type="text/html;charset=UTF-8" body-prefix="<!doctype html>..."
    spike_test.go:370: singular message endpoint returned 0 entries
--- PASS: TestSpikeSessionMessagesEndpoints (2.82s)
=== RUN   TestSpikePostMessageAndTokenEvents
    spike_test.go:433: observed message.part.delta events for session=ses_33fe55437ffezbvVy6J33bsFWv (total_event_data_lines=65)
--- PASS: TestSpikePostMessageAndTokenEvents (15.30s)
=== RUN   TestSpikeEventEndpointReceivesSessionUpdates
    spike_test.go:469: event stream delivered session.updated for session=ses_33fe5185fffes3dFUYysFCoHW0
--- PASS: TestSpikeEventEndpointReceivesSessionUpdates (3.17s)
=== RUN   TestSpikeMultiClientEventStreams
    spike_test.go:521: both event clients observed session.updated for session=ses_33fe50bf0ffeGySWMVQ3OSAKoz
--- PASS: TestSpikeMultiClientEventStreams (3.59s)
=== RUN   TestSpikeSessionDetailFields
    spike_test.go:563: session detail keys=[directory id projectID slug time title version]
    spike_test.go:564: session detail capability working_directory=true files=false agent=false
--- PASS: TestSpikeSessionDetailFields (2.62s)
PASS
ok   opencoderouter/internal/daemon  35.444s

=== RUN   TestSpikeDocEndpointOpenAPI
    spike_test.go:253: spike skipped: opencode binary not available in PATH: exec: "opencode": executable file not found in $PATH
--- SKIP: TestSpikeDocEndpointOpenAPI (0.00s)
PASS
ok   opencoderouter/internal/daemon  0.306s
```

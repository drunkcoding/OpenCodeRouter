# OpenCodeRouter

A long-running HTTP reverse proxy that auto-discovers [OpenCode](https://opencode.ai) `serve` / ACP instances on a shared server and routes traffic to them via per-project domains. Also ships **`ocr`** — a terminal UI for discovering and managing OpenCode sessions across remote SSH hosts.

Each discovered project gets its own hostname containing the owner's username (e.g. `myproject-alice.local`), and is advertised over mDNS so other machines on the LAN can find it.

The router can also **manage the lifecycle** of `opencode serve` instances — pass project directories as arguments and it will start them on launch and stop them on shutdown.

The **`ocr`** (OpenCode Remote) TUI reads your `~/.ssh/config`, probes each host for running OpenCode instances, and presents all sessions in a searchable, hierarchical terminal dashboard.

## How it works

```
                         LAN clients
                             │
                     ┌───────▼────────┐
                     │  OpenCodeRouter │  :8080
                     │  (reverse proxy)│
                     └──┬─────┬─────┬─┘
                        │     │     │
          ┌─────────────┘     │     └──────────────┐
          ▼                   ▼                    ▼
   localhost:30000     localhost:30001       localhost:30002
   opencode serve      opencode serve        opencode serve
   (project-a)         (project-b)           (project-c)
```

1. **Launcher** (optional) starts `opencode serve` in each project directory passed as a CLI argument, assigning ports automatically from the scan range. Child processes are stopped when the router shuts down.
2. **Scanner** probes a port range on `127.0.0.1` every few seconds, calling each port's `GET /global/health` and `GET /project/current` endpoints to identify running OpenCode instances.
3. **Registry** tracks discovered backends in a thread-safe map, keyed by a slug derived from the project path (the last folder name). Stale backends are pruned automatically.
4. **Proxy** routes incoming HTTP requests to the correct backend using either host-based or path-based matching.
5. **mDNS advertiser** registers each project as a `_opencode._tcp` service via [zeroconf](https://github.com/grandcat/zeroconf), making it discoverable on the local network.

## Install

Requires Go 1.23+.

```bash
go install opencoderouter@latest
```

Or build from source:

```bash
git clone https://github.com/your-org/OpenCodeRouter.git
cd OpenCodeRouter
go build -o opencoderouter .

# Build the remote session TUI
go build -o bin/ocr ./cmd/ocr

# Or use make for both
make build
```

## Usage

```bash
# Start with defaults (auto-detects username, scans ports 30000-31000, mDNS on)
./opencoderouter

# Start and manage opencode serve instances for specific projects
./opencoderouter ~/project-a ~/project-b ~/project-c

# Custom port and scan range
./opencoderouter --port 8080 --scan-start 4000 --scan-end 5000

# Override username (defaults to OS user)
./opencoderouter --username alice

# Disable mDNS advertisement
./opencoderouter --mdns=false

# Combine: custom port + managed projects
./opencoderouter --port 31000 ~/project-a ~/project-b
```

### Flags

| Flag | Default | Description |
|---|---|---|
| `--port` | `8080` | Port for the router to listen on |
| `--hostname` | `0.0.0.0` | Bind address |
| `--username` | OS user | Username embedded in domain names |
| `--scan-start` | `30000` | Start of port scan range (inclusive) |
| `--scan-end` | `31000` | End of port scan range (inclusive) |
| `--scan-interval` | `5s` | How often to scan for new instances |
| `--scan-concurrency` | `20` | Max concurrent port probes per scan |
| `--probe-timeout` | `800ms` | HTTP timeout for each health-check probe |
| `--stale-after` | `30s` | Remove backends not seen for this duration |
| `--mdns` | `true` | Enable mDNS service advertisement |

### Positional arguments

Any arguments after the flags are treated as **project directories**. The router will:

1. Start `opencode serve` in each directory, assigning ports from the scan range
2. Automatically discover them via the scanner
3. Stop all managed instances when the router shuts down (SIGINT / SIGTERM)

```bash
# Launch router + three managed projects
./opencoderouter --port 31000 ~/project-a ~/project-b ~/project-c
```

The project slug (used in HTTP paths and mDNS hostnames) is the **last folder name** of the project path. For example, `~/work/my-project` becomes the slug `my-project`, accessible at `/my-project/...`.

## Routing

The router supports two routing modes simultaneously. No client-side configuration is required for path-based routing.

### Host-based (mDNS)

When mDNS is enabled, each project is advertised as `{slug}-{username}.local`. Clients on the same LAN can reach a project directly:

```
http://myproject-alice.local:8080/session
  → proxied to http://127.0.0.1:30000/session
```

The router only advertises projects belonging to the server runner's username.

### Path-based

Any client can access a project by prefixing the request path with the project slug. The prefix is stripped before forwarding:

```
http://localhost:8080/myproject/session
  → proxied to http://127.0.0.1:30000/session
```

## Dashboard

Open `http://localhost:8080/` in a browser to see a live table of all discovered backends with their status, domains, and links.

## API

| Endpoint | Description |
|---|---|
| `GET /api/health` | Router health and backend count |
| `GET /api/backends` | JSON array of all discovered backends |
| `GET /api/resolve?path=...` | Resolve a project path to its routing info |
| `GET /api/resolve?name=...` | Resolve a project by folder basename |

### List backends

```bash
curl http://localhost:8080/api/backends | jq .
```

```json
[
  {
    "slug": "myproject",
    "project_name": "myproject",
    "project_path": "/home/alice/myproject",
    "port": 30000,
    "version": "1.2.9",
    "domain": "myproject-alice.local",
    "path_prefix": "/myproject/",
    "url": "http://localhost:8080/myproject/",
    "last_seen": "2026-02-28T18:10:00Z"
  }
]
```

### Resolve a project

External agents can look up a project by its filesystem path **or folder basename** to get the routing URL.

**By path** (full or partial — falls back to slug matching):

```bash
curl 'http://localhost:8080/api/resolve?path=/home/alice/myproject' | jq .
```

**By name** (just the folder basename — ideal for external automation):

```bash
curl 'http://localhost:8080/api/resolve?name=myproject' | jq .
```

Both return the same shape:

```json
{
  "slug": "myproject",
  "project_name": "myproject",
  "project_path": "/home/alice/myproject",
  "port": 30000,
  "version": "1.2.9",
  "domain": "myproject-alice.local",
  "path_prefix": "/myproject/",
  "url": "http://localhost:8080/myproject/",
  "last_seen": "2026-02-28T18:10:00Z"
}
```

The `url` field is the path-based URL through the router. External agents can use it directly to reach the project's OpenCode instance without needing to know slug derivation rules. The `?name=` parameter is particularly useful for automation tools like TickTick-based dispatchers that only know the project name, not its full path.

## mDNS

Each discovered project is registered as a DNS-SD service:

- **Service type**: `_opencode._tcp`
- **Hostname**: `{slug}-{username}.local` (A record pointing to the machine's IP)
- **Port**: the router's listen port
- **TXT records**: `project=...`, `path=...`, `backend=127.0.0.1:PORT`, `owner=USERNAME`, `version=...`

Multiple routers on different machines can coexist on the same LAN -- services are namespaced by username. Clients can browse all available projects:

```bash
# Using avahi
avahi-browse -r _opencode._tcp

# Using dns-sd (macOS)
dns-sd -B _opencode._tcp local.
```

## Remote access via SSH port forwarding

If the server running OpenCodeRouter is remote (e.g. a dev box, cloud VM, or shared lab machine), you can access the dashboard and all proxied OpenCode instances from your laptop over SSH — no VPN or public IP required.

### Quick start

```bash
# Forward the router port to your laptop
ssh -L 8080:localhost:8080 user@remote-server

# Then open in your local browser
open http://localhost:8080
```

All path-based routes work immediately:

```
http://localhost:8080/myproject/session    → remote opencode on :30000
http://localhost:8080/api/backends          → JSON list of all projects
```

### Forward a specific OpenCode backend directly

If you want to reach a single OpenCode instance without the router:

```bash
ssh -L 30000:localhost:30000 user@remote-server

# Now talk to that OpenCode instance directly
curl http://localhost:30000/global/health
```

### Forward multiple ports at once

```bash
# Router + two OpenCode instances
ssh -L 8080:localhost:8080 \
    -L 30000:localhost:30000 \
    -L 30001:localhost:30001 \
    user@remote-server
```

### Persistent tunnel with autossh

[autossh](https://www.harding.motd.ca/autossh/) reconnects automatically if the connection drops:

```bash
# Install once
sudo apt install autossh   # Debian/Ubuntu
brew install autossh       # macOS

# Run persistent tunnel
autossh -M 0 -f -N \
    -o "ServerAliveInterval 30" \
    -o "ServerAliveCountMax 3" \
    -L 8080:localhost:8080 \
    user@remote-server
```

### SSH config shortcut

Add to `~/.ssh/config` so you can just run `ssh devbox`:

```
Host devbox
    HostName remote-server.example.com
    User alice
    LocalForward 8080 localhost:8080
    # Add more OpenCode ports as needed:
    # LocalForward 30000 localhost:30000
    # LocalForward 30001 localhost:30001
    ServerAliveInterval 30
    ServerAliveCountMax 3
```

Then:

```bash
ssh devbox
# Router dashboard is now at http://localhost:8080
```

### Dynamic SOCKS proxy (forward all ports)

If you don't want to enumerate ports, use a SOCKS proxy:

```bash
ssh -D 1080 -f -N user@remote-server
```

Then configure your browser to use `localhost:1080` as a SOCKS5 proxy. All `localhost:*` URLs on the remote machine become accessible, including the router and every OpenCode instance.

### Tips

- **mDNS won't cross SSH tunnels.** Use path-based routing (`/slug/...`) when accessing remotely — it works without any DNS setup.
- **Only the router port is needed.** You don't need to forward individual OpenCode ports if you go through the router.
- The router binds to `0.0.0.0` by default. If you prefer it to only accept connections from SSH tunnels, start it with `--hostname 127.0.0.1`.
- To check which OpenCode instances are running before forwarding, SSH in and hit the API: `ssh user@remote-server 'curl -s localhost:8080/api/backends | jq .'`

## Helper scripts

Two standalone shell scripts are included for managing `opencode serve` instances **independently** of the router. These are useful when you want to start instances separately, or if you prefer not to use the router's built-in launcher.

> **Note:** If you pass project directories as arguments to the router, you don't need these scripts — the router manages the lifecycle automatically.

### `oc` — start instances

Launches `opencode serve` in each given directory, assigning ports from 30000-31000. Skips directories that already have a running instance.

```bash
# Start serving three projects
./oc ~/project-a ~/project-b ~/project-c

# Already-running directories are skipped automatically
./oc ~/project-a ~/project-d
```

### `oc-kill` — stop all instances

Sends SIGTERM to every running `opencode serve` process.

```bash
./oc-kill
```

## OpenCode Remote TUI (`ocr`)

A keyboard-driven terminal UI for managing OpenCode sessions across your entire fleet of SSH hosts. Think `k9s` for OpenCode.

### Features

- **Auto-discovery** — reads `~/.ssh/config`, resolves hosts via `ssh -G`, probes for `opencode` installations
- **Hierarchical view** — Host → Project → Sessions, collapsible tree with vim-style navigation
- **Live status** — 🟢 ACTIVE / 💤 IDLE / 🔴 ERRORED indicators, braille spinner (⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏) for thinking sessions
- **Fuzzy search** — filter across host names, project paths, session titles, and IDs
- **Session actions** — attach, create, inspect, kill/archive
- **Auto-refresh** — configurable polling with visual countdown
- **Parallel probing** — worker pool with connection multiplexing (SSH ControlMaster)
- **Themes** — Auto (adapts to terminal background, default), Night Ops (dark), Light, Minimal (ASCII-safe fallback)

### Quick start

```bash
# Build
make build

# Run
./bin/ocr

# With custom config
./bin/ocr --config ~/.opencode/remote-tui.yaml
```

### Keybindings

| Key | Action |
|---|---|
| `Enter` | If a session row is selected, attach to that session. On host or project rows, expand or collapse the node. |
| `Space` | Expand or collapse the selected host or project node, same as Enter on non-session rows. |
| `↑/↓` or `k/j` | Move selection up or down in the tree. |
| `←/→` or `h/l` | Collapse or expand tree nodes. |
| `/` | Focus search input. |
| `r` | Refresh all hosts and sessions. |
| `n` | Open "new session" prompt for the selected host or project. |
| `d` | Kill or archive the selected session. |
| `g` | Clone a git repository on the selected host and start OpenCode in the clone. |
| `i` | Show the inspect panel for the selected session. |
| `Tab` | Toggle the inspect panel on or off. |
| `a` | Show SSH auth bootstrap commands for an auth-required or blocked host. |
| `e` | Open details for the last error when an error toast is visible. |
| `Esc` | Close the active modal or error dialog. |
| `q` | Quit the TUI. |

### Configuration

Create `~/.opencode/remote-tui.yaml` (auto-generated with defaults on first run):

```yaml
polling:
  interval: 30s        # How often to re-probe hosts
  timeout: 10s         # SSH connect timeout per host
  max_parallel: 10     # Concurrent SSH connections

display:
  theme: auto          # auto, nightops, light, minimal
  unicode: true        # false for ASCII-only terminals
  animation: true      # Braille spinners, countdowns
  active_threshold: 10m  # Sessions active within this = ACTIVE
  idle_threshold: 24h    # Sessions older than this are dimmed

hosts:
  include: ["*"]       # Glob patterns to include from SSH config
  ignore: ["backup-*"] # Glob patterns to exclude
  groups:               # Logical grouping in the tree
    production: ["prod-*"]
    development: ["dev-*"]
  overrides:
    my-server:
      label: "Main API Server"
      priority: 1      # Lower = higher in the list
      opencode_path: /usr/local/bin/opencode

ssh:
  control_master: auto   # SSH ControlMaster for connection reuse
  control_persist: 60    # Keep control socket alive (seconds)
  batch_mode: true       # Non-interactive SSH (no password prompts)

sessions:
  sort_by: last_activity # last_activity, name, host
  show_archived: false
  max_display: 50
  enrich_from_db: true   # Query SQLite for message counts, agents

keybindings:
  attach: enter         # Attach to selected session
  search: /
  refresh: r
  quit: q
  new_session: n        # New session on selected host or project
  kill_session: d
  git_clone: g
  inspect: i
  cycle_view: tab       # Toggle inspect panel
  authenticate: a
  error_detail: e       # Open last error details when an error toast is shown
```

### How probing works

```
~/.ssh/config
      │
      ▼  parse Host entries
  ┌───────────────────┐
  │  DiscoveryService  │  ssh -G <host> → resolve real hostname/user/port
  └────────┬──────────┘
           ▼  for each host (parallel)
  ┌───────────────────┐
  │   ProbeWorkerPool  │  ssh -o BatchMode=yes <host> \
  │                    │    'command -v opencode && opencode session list --format json'
  └────────┬──────────┘
           ▼  TTL cache + stale-while-revalidate
  ┌───────────────────┐
  │    TUI TreeView    │  Host → Project → Sessions (ranked by last_activity)
  └───────────────────┘
```

Each probe is a single SSH round-trip. Unreachable hosts show as ○ offline and are retried on the next polling cycle.

## Project structure

```
├── main.go                        # Router entry point, CLI flags, orchestration
├── cmd/ocr/
│   └── main.go                    # Remote TUI entry point (cobra CLI)
├── oc                             # Batch-start opencode serve instances (standalone)
├── oc-kill                        # Kill all opencode serve instances (standalone)
├── internal/
│   ├── config/config.go           # Router configuration types, defaults, validation
│   ├── launcher/launcher.go       # Manages opencode serve child processes
│   ├── registry/registry.go       # Thread-safe backend registry
│   ├── scanner/scanner.go         # Parallel port scanner + OpenCode probing
│   ├── discovery/discovery.go     # mDNS advertisement via zeroconf
│   ├── proxy/proxy.go             # Reverse proxy, routing, dashboard
│   └── tui/                       # Remote session TUI (ocr)
│       ├── app.go                 # Top-level Bubble Tea model
│       ├── components/
│       │   ├── header.go          # Search bar, refresh countdown, fleet stats
│       │   ├── tree.go            # Collapsible Host→Project→Session tree
│       │   ├── inspect.go         # Session detail panel
│       │   ├── footer.go          # Context-sensitive keybinding hints
│       │   ├── modal.go           # Overlay dialogs
│       │   └── spinner.go         # Braille animation component
│       ├── config/                # TUI-specific config + YAML loader
│       ├── discovery/             # SSH config parser + host resolver
│       ├── probe/                 # SSH probe worker pool + TTL cache
│       ├── model/                 # Domain types + Bubble Tea messages
│       ├── theme/                 # Lipgloss style themes (auto, nightops, light, minimal)
│       └── keys/                  # Keybinding definitions
├── go.mod
└── go.sum
```

## Autodispatch (OpenClaw + TickTick)

OpenCodeRouter is designed to be the service-discovery layer in a **programming task autodispatch pipeline**. An external orchestrator (e.g. OpenClaw) polls a task source (e.g. TickTick), resolves the target project via the router, and dispatches the task to the correct OpenCode instance.

### Pipeline overview

```
TickTick task (tagged "autocode")          OpenClaw dispatcher
┌──────────────────────────┐           ┌──────────────────────┐
│ title: Fix auth bug      │  poll     │                      │
│ tags: [autocode]         │──────────▶│  1. Parse task YAML  │
│ content:                 │           │  2. Extract project  │
│   ---                    │           │  3. Resolve via      │
│   project: Archer        │           │     router API       │
│   model: claude-sonnet   │           │  4. Dispatch prompt  │
│   ---                    │           │  5. Monitor SSE      │
│   Fix the login timeout  │           │  6. Complete task    │
│   bug in auth module...  │           │                      │
└──────────────────────────┘           └───────┬──────────────┘
                                               │
                         ┌─────────────────────┘
                         ▼
               OpenCodeRouter (:31000)
        GET /api/resolve?name=Archer
                         │
                         ▼
            http://localhost:31000/archer/
         → opencode serve (Archer project)
```

### TickTick task convention

Tasks use YAML frontmatter in the `content` field for structured metadata (TickTick has no custom fields):

```markdown
---
project: Archer
model: claude-sonnet-4-5
agent: coder
---

Fix the login timeout bug in the auth module. The session expires
after 5 minutes instead of the configured 30 minutes.
```

| Field | Required | Description |
|---|---|---|
| `project` | Yes | Project folder basename (matched via `?name=`) |
| `model` | No | Model ID override (default: dispatcher's choice) |
| `agent` | No | Agent type: `coder`, `task` (default: `coder`) |

Tasks are filtered by the `autocode` tag and polled at ~5 minute intervals (TickTick rate limit: 100 req/min, 300 req/5min).

### Dispatch flow

1. **Poll** — Fetch active tasks tagged `autocode` from TickTick
2. **Parse** — Extract YAML frontmatter from `content`; the body after frontmatter becomes the prompt
3. **Resolve** — `GET /api/resolve?name={project}` → get the router URL for the target project
4. **Create session** — `POST {url}/session?directory={project_path}`
5. **Dispatch** — `POST {url}/session/{id}/prompt_async` with the task body as prompt
6. **Monitor** — `GET {url}/event?directory={project_path}` SSE stream; wait for `session.idle` (success) or `session.error` (failure)
7. **Complete** — Mark TickTick task as done (`status: 2`) on success, or add error comment on failure

### Example: resolve + dispatch

```bash
# 1. Resolve project to routing URL
URL=$(curl -s 'http://localhost:31000/api/resolve?name=Archer' | jq -r .url)
# URL = http://localhost:31000/archer/

# 2. Create a session
SESSION=$(curl -s -X POST "${URL}session" | jq -r .id)

# 3. Dispatch the task (async, returns 204 immediately)
curl -s -X POST "${URL}session/${SESSION}/prompt_async" \
  -H 'Content-Type: application/json' \
  -d '{
    "parts": [{"type": "text", "text": "Fix the login timeout bug in auth module"}],
    "model": {"providerID": "anthropic", "modelID": "claude-sonnet-4-5"},
    "agent": "coder"
  }'

# 4. Monitor via SSE (session.idle = done, session.error = failed)
curl -N "${URL}event?directory=/home/xly/Archer"
```


## License

See [LICENSE](LICENSE).

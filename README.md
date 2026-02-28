# OpenCodeRouter

A long-running HTTP reverse proxy that auto-discovers [OpenCode](https://opencode.ai) `serve` / ACP instances on a shared server and routes traffic to them via per-project domains.

Each discovered project gets its own hostname containing the owner's username (e.g. `myproject-alice.local`), and is advertised over mDNS so other machines on the LAN can find it.

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
   localhost:4096      localhost:4097        localhost:4098
   opencode serve      opencode serve        opencode serve
   (project-a)         (project-b)           (project-c)
```

1. **Scanner** probes a port range on `127.0.0.1` every few seconds, calling each port's `GET /global/health` and `GET /project/current` endpoints to identify running OpenCode instances.
2. **Registry** tracks discovered backends in a thread-safe map, keyed by a slug derived from the project path. Stale backends are pruned automatically.
3. **Proxy** routes incoming HTTP requests to the correct backend using either host-based or path-based matching.
4. **mDNS advertiser** registers each project as a `_opencode._tcp` service via [zeroconf](https://github.com/grandcat/zeroconf), making it discoverable on the local network.

## Install

Requires Go 1.22+.

```bash
go install opencoderouter@latest
```

Or build from source:

```bash
git clone https://github.com/your-org/OpenCodeRouter.git
cd OpenCodeRouter
go build -o opencoderouter .
```

## Usage

```bash
# Start with defaults (auto-detects username, scans ports 4096-4200, mDNS on)
./opencoderouter

# Custom port and scan range
./opencoderouter --port 8080 --scan-start 4000 --scan-end 5000

# Override username (defaults to OS user)
./opencoderouter --username alice

# Disable mDNS advertisement
./opencoderouter --mdns=false
```

### Flags

| Flag | Default | Description |
|---|---|---|
| `--port` | `8080` | Port for the router to listen on |
| `--hostname` | `0.0.0.0` | Bind address |
| `--username` | OS user | Username embedded in domain names |
| `--scan-start` | `4096` | Start of port scan range (inclusive) |
| `--scan-end` | `4200` | End of port scan range (inclusive) |
| `--scan-interval` | `5s` | How often to scan for new instances |
| `--scan-concurrency` | `20` | Max concurrent port probes per scan |
| `--probe-timeout` | `800ms` | HTTP timeout for each health-check probe |
| `--stale-after` | `30s` | Remove backends not seen for this duration |
| `--mdns` | `true` | Enable mDNS service advertisement |

## Routing

The router supports two routing modes simultaneously. No client-side configuration is required for path-based routing.

### Host-based (mDNS)

When mDNS is enabled, each project is advertised as `{slug}-{username}.local`. Clients on the same LAN can reach a project directly:

```
http://myproject-alice.local:8080/session
  → proxied to http://127.0.0.1:4096/session
```

The router only advertises projects belonging to the server runner's username.

### Path-based

Any client can access a project by prefixing the request path with the project slug. The prefix is stripped before forwarding:

```
http://localhost:8080/myproject/session
  → proxied to http://127.0.0.1:4096/session
```

## Dashboard

Open `http://localhost:8080/` in a browser to see a live table of all discovered backends with their status, domains, and links.

## API

| Endpoint | Description |
|---|---|
| `GET /api/health` | Router health and backend count |
| `GET /api/backends` | JSON array of all discovered backends |

Example:

```bash
curl http://localhost:8080/api/backends | jq .
```

```json
[
  {
    "slug": "myproject",
    "project_name": "myproject",
    "project_path": "/home/alice/myproject",
    "port": 4096,
    "version": "1.2.9",
    "domain": "myproject-alice.local",
    "path_prefix": "/myproject/",
    "last_seen": "2026-02-28T18:10:00Z"
  }
]
```

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
http://localhost:8080/myproject/session    → remote opencode on :4096
http://localhost:8080/api/backends          → JSON list of all projects
```

### Forward a specific OpenCode backend directly

If you want to reach a single OpenCode instance without the router:

```bash
ssh -L 4096:localhost:4096 user@remote-server

# Now talk to that OpenCode instance directly
curl http://localhost:4096/global/health
```

### Forward multiple ports at once

```bash
# Router + two OpenCode instances
ssh -L 8080:localhost:8080 \
    -L 4096:localhost:4096 \
    -L 4097:localhost:4097 \
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
    # LocalForward 4096 localhost:4096
    # LocalForward 4097 localhost:4097
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

## Project structure

```
├── main.go                        # Entry point, CLI flags, orchestration
├── internal/
│   ├── config/config.go           # Configuration types, defaults, validation
│   ├── registry/registry.go       # Thread-safe backend registry
│   ├── scanner/scanner.go         # Parallel port scanner + OpenCode probing
│   ├── discovery/discovery.go     # mDNS advertisement via zeroconf
│   └── proxy/proxy.go             # Reverse proxy, routing, dashboard
├── go.mod
└── go.sum
```

## License

See [LICENSE](LICENSE).

# GoBroom operations contract

GoBroom is local-first. The daemon owns state; CLI and TUI are replaceable
clients over the Unix IPC socket.

## Default paths

| Item | Default |
|---|---|
| SQLite | `$XDG_CONFIG_HOME/gobroom/gobroom.db` |
| Unix IPC | `$XDG_RUNTIME_DIR/gobroom.sock` |
| HTTP data plane | `127.0.0.1:2712` |
| HTTP control plane | disabled; when enabled, `127.0.0.1:2713` |
| user service | `gobroomd.service` |

The daemon flags are authoritative when a deployment needs different paths.
The request hot path reads immutable snapshots and does not query SQLite.

Startup reserves every enabled HTTP listener and acquires Unix IPC ownership
before initializing state. An occupied data/control port makes startup fail; any earlier
reservation is released. A later initialization or IPC error also releases the
reserved ports. After `Start` succeeds, the enabled listeners are already bound.
Embedders can request `127.0.0.1:0` and read the allocated ports from
`Daemon.Addresses()`. Addresses are cleared when the daemon stops.

IPC ownership uses a private `<socket>.lock` file and an OS advisory lock held
until daemon cleanup finishes. A second process using that IPC path fails
before opening its database. An existing active socket is preserved, including
listeners from another application. Crash residue is reclaimed only after a
socket probe reports no listener; regular files, directories and symlinks are
never replaced. Socket cleanup checks inode identity before unlinking.
The lock file intentionally remains after shutdown: its presence is not a held
lock, and deleting it while another process runs can break exclusivity.

Socket existence alone is not readiness: initialization reserves it before
starting the accept loop. `make install-all` waits for a successful CLI `status`
request before reporting the restarted daemon ready.

## Service and logs

The user service writes operational messages to stdout/stderr, so a service
manager can route them to journald:

```bash
systemctl --user enable --now gobroomd
journalctl --user -u gobroomd -f
```

Every `/v1/*` request also emits a structured access record with request ID,
method/path, status, duration, bytes, and (for model operations) public model
and operation. Bodies, query strings, authorization headers, API keys, and
provider response content are never included. Streaming requests are logged
when the stream finishes. The latest 256 records are available in the TUI Logs
pane or with `gobroom logs --limit 100`; this in-memory ring is not a replacement
for durable journal storage. Use `X-Request-ID` to supply a safe correlation ID;
otherwise the daemon generates one and returns it in the response header.

## Data-plane exposure

Loopback HTTP requires no token by default. When serving beyond a trusted
loopback boundary, start the daemon with an explicit token:

```bash
gobroomd --addr 127.0.0.1:2712 --http-token "$GOBROOM_HTTP_TOKEN"
```

Clients then send `Authorization: Bearer <token>`.

The control plane is independent and remains disabled unless
`--http-control` is explicitly enabled. Do not expose the control plane
through a public reverse proxy.

## TLS and reverse proxy

GoBroom's built-in listener is intended for local/loopback operation. For a
remote data plane, terminate TLS at a trusted reverse proxy and forward only
the data-plane routes. Keep the Unix IPC and control HTTP listener local.

Required proxy properties:

- TLS 1.2+ with certificate validation;
- preserve `Authorization` and streaming/flush behavior;
- disable buffering for SSE/streaming responses;
- enforce request-size, idle-timeout and upstream connection limits;
- add network-level access control in addition to the GoBroom bearer token;
- never log bearer tokens or request bodies by default.

An alternate storage backend is not currently claimed. SQLite remains the
only tested repository implementation until a named backend has transaction,
concurrency, migration and recovery tests.

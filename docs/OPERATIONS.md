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

## Service and logs

The user service writes operational messages to stdout/stderr, so a service
manager can route them to journald:

```bash
systemctl --user enable --now gobroomd
journalctl --user -u gobroomd -f
```

The daemon also exposes a bounded recent log ring through `logs.list` for TUI
and CLI diagnostics. It is not a replacement for durable journal storage.

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

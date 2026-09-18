# GoBroom daemon architecture

## 1. Core decision

`gobroomd` is not an HTTP server with a background loop. It is a daemon
process with a core runtime and optional gateways.

```text
gobroomd process
  ├── daemon lifecycle/supervisor
  ├── routing kernel
  ├── SQLite/control plane
  ├── provider workers
  ├── quota/usage/health workers
  ├── local IPC control endpoint       (default, always available)
  ├── local IPC data endpoint          (optional)
  └── HTTP data gateway                (default for provider clients)
```

The daemon must work when no HTTP port is exposed and no TUI is running.

## 2. Three deployment modes

### Mode A — local provider daemon

Default for a local machine:

```text
gobroomd
  └── Unix socket / Windows named pipe
        ├── status
        ├── start/stop/reload
        ├── provider/model/combo management
        ├── quota/usage/health queries
        └── optional local data-plane requests
```

The daemon also exposes the OpenAI-compatible data plane on loopback by
default, because ordinary provider clients expect HTTP. IPC remains the
full-control channel for CLI/TUI/admin clients.

### Mode B — local provider gateway

For OpenAI-compatible clients that only understand HTTP:

```text
gobroomd
  ├── IPC socket                 (full control)
  └── HTTP on 127.0.0.1:2712    (provider/data plane)
```

The HTTP listener is a gateway over the same kernel. It does not implement a
second router or second configuration path.

### Mode C — backend/server deployment

For a server or container:

```text
gobroomd
  ├── IPC socket (optional/restricted)
  └── HTTP/TLS data gateway on configured address
        └── /v1/* data plane

  HTTP /api/* control gateway remains optional.
```

The data gateway can be bound to loopback, a Unix-socket adapter, a private
interface or a public interface behind TLS/reverse proxy. The HTTP control API
is independently enabled or disabled. The daemon core is unchanged.

## 3. Why IPC must be a first-class contract

HTTP is useful for compatibility, but it is not the natural protocol for
daemon lifecycle operations:

```text
status
reload config
rotate credential
refresh models
shutdown
tail events
inspect route snapshot
```

The native control protocol should be a versioned framed RPC protocol over:

```text
Unix domain socket on Unix
Windows named pipe on Windows
```

The payload can initially be JSON frames with request ID, method, params,
response/error, event marker and protocol version. The socket/pipe is protected
by filesystem or pipe ACLs, so a local daemon does not need an unauthenticated
management TCP port.

## 4. Gateway layering

Both transports call the same application services:

```text
IPC command ───────┐
                   ├── application/control service
HTTP control API ──┘

IPC data request ──┐
                   ├── normalizer → kernel → adapter
HTTP /v1/* ────────┘
```

Neither gateway may read SQLite directly, resolve combos independently, select
accounts independently, implement a second fallback policy or mutate route
state outside the control service.

## 5. Process structure

```text
cmd/gobroomd
  -> internal/daemon
       -> internal/app
            -> controlplane/kernel/providers/usage/quota
       -> internal/ipc
       -> internal/gateway/http
       -> internal/gateway/ipc

cmd/gobroom
  -> IPC client

cmd/gobroom-tui
  -> IPC client
```

CLI/TUI must not import `internal/kernel` or `internal/store` to perform
operations. They are frontends to the daemon, not alternate application
services.

## 6. Lifecycle contract

The daemon supervisor owns configuration loading, migrations, snapshot build,
IPC/HTTP listeners, background workers, signals and graceful shutdown.

States:

```text
starting → ready → degraded → stopping → stopped
```

`ready` means SQLite is open, migrations completed, a valid route snapshot
exists and IPC accepts requests. Provider discovery or token refresh failures
must not prevent readiness when the existing snapshot is valid; they affect
individual routes or move the daemon to `degraded`.

## 7. Listener configuration

```yaml
daemon:
  ipc:
    enabled: true
    path: "${runtime_dir}/gobroom.sock"
  http:
    enabled: true
    address: "127.0.0.1:2712"
    tls: false
    data_plane: true
    control_plane: false
```

Useful commands:

```text
gobroom daemon start
gobroom daemon stop
gobroom daemon status
gobroom daemon reload
gobroom daemon http enable
gobroom daemon http disable
```

The command client talks to IPC even when HTTP is disabled.

## 8. Data plane and control plane are separate

1. Provider clients use the OpenAI-compatible HTTP data plane.
2. CLI/TUI/admin clients use IPC for full control.
3. The HTTP `/api/*` control surface is optional.
4. A Unix-socket HTTP adapter can serve local HTTP-only clients without TCP.

The daemon must expose a provider-compatible data surface, but it does not need
to expose a remotely reachable management API.

## 9. Relation to the current implementation

The current `cmd/gobroomd` starts `http.ListenAndServe` directly. That is a
temporary bootstrap, not the final daemon architecture:

```text
current: main → SQLite → HTTP server
target:  main → daemon supervisor → application services
                    ├── IPC always
                    ├── HTTP /v1 data gateway by default on loopback
                    └── HTTP /api control gateway optional
```

The current `internal/api` becomes `internal/gateway/http`; its handlers stay
thin adapters over control services and the kernel.

## 10. Failure and shutdown behavior

The daemon must keep IPC available while HTTP is disabled/degraded, keep the
last valid snapshot when reload fails, stop accepting new requests during
shutdown, cancel upstream requests, drain event queues within a bounded
timeout, close listeners before SQLite and report provider failures without
crashing the process.

## 11. Comparison with 9router

| Concern | 9router | GoBroom target |
|---|---|---|
| Default interaction | dashboard + HTTP | IPC daemon protocol |
| HTTP | central runtime surface | optional gateway |
| CLI/TUI | auxiliary UI | replaceable IPC client |
| State ownership | handlers/repositories/background modules | daemon application services |
| Local security | API/cookie auth | socket/pipe ACL plus optional auth |
| Headless operation | web-centric | primary mode |
| Backend deployment | same Next runtime | same daemon with HTTP enabled |
| Multiple frontends | coupled to dashboard APIs | one IPC contract |
| Provider routing | Next request lifecycle | daemon kernel |
| IDE integration | mixed into product runtime | separate sidecar/plugin |

## 12. Implementation order

1. Extract `internal/app` service ownership from API handlers.
2. Add daemon supervisor and readiness states.
3. Add Unix socket/named-pipe IPC framing and versioning.
4. Move CLI operations to IPC.
5. Make HTTP gateway optional over the same services.
6. Add local data-plane IPC or Unix-socket HTTP bridge.
7. Add TUI as a separate frontend module.

The kernel, provider adapters, quota and usage services remain independent of
all transports.

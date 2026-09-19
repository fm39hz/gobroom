# Daemon and transport architecture

Status: target contract with current implementation notes. The roadmap is the
source for completion status.

## One daemon, two planes

```text
gobroomd
  ├── routing kernel and provider runtime
  ├── SQLite control state + immutable route snapshot
  ├── Unix-domain-socket control IPC (CLI/TUI)
  ├── HTTP data plane (/v1/*, loopback by default)
  └── optional HTTP control plane (/api/*)
```

The daemon is both a normal HTTP provider endpoint and a Unix daemon. These are
not competing modes: HTTP serves tools that speak the OpenAI-compatible
provider protocol; IPC exposes lifecycle and full local control. The daemon
must remain operational with no frontend process running.

Default listener contract:

| Surface | Default | Responsibility |
|---|---|---|
| Control IPC | Unix socket | Status, reload, CRUD, discovery, route resolution, health/quota |
| HTTP data plane | `127.0.0.1:2712` | Published models and inference endpoints |
| HTTP control plane | Disabled; `127.0.0.1:2713` when enabled | Optional automation/control interface |

HTTP control and data listeners must be independently configurable. Exposing
the data plane does not require exposing a management API.

## Contracts

Both the IPC and HTTP boundaries delegate to daemon-owned services. They must
not implement their own combo traversal, route selection or credential logic.
Likewise, CLI/TUI are clients and do not access SQLite or perform routing.

The local IPC protocol currently uses request/response JSON over a Unix socket.
Keep the method and payload contract typed/versionable as it grows. Socket file
permissions provide the local access boundary; any non-loopback HTTP exposure
requires explicit operator configuration and appropriate network protection.

## Lifecycle expectations

The daemon owns database open/migration, snapshot construction, listeners,
background workers, signal handling and bounded shutdown. A bad configuration
reload must leave the last valid snapshot active. Provider discovery failures
should affect the relevant provider operation, not make an otherwise valid
daemon unusable.

Desired state sequence:

```text
starting -> ready <-> degraded -> stopping -> stopped
```

The current status endpoint is intentionally simpler than this full lifecycle
model; do not treat all readiness/degraded semantics as implemented.

## Deployment modes

1. **Local daemon (default):** IPC plus loopback HTTP provider endpoint.
2. **Headless backend:** same daemon, configured HTTP listener and optionally
   enabled control API; no TUI dependency.
3. **Local-only HTTP alternative:** a future Unix-socket HTTP listener/bridge
   may serve HTTP-only local clients without opening TCP. This is not required
   for the current default setup.

## Current implementation boundary

The repository currently wires the daemon, SQLite, kernel, IPC and HTTP
listeners in `internal/daemon`. `internal/api` contains both data handlers and
optional control handlers. This is a working bootstrap boundary, not yet a
fully extracted application-service package. The architecture can evolve
without changing the public provider API or making the TUI a daemon dependency.

## Safety and shutdown

- Stop accepting requests before closing SQLite.
- Cancel in-flight upstream requests on shutdown.
- Drain bounded usage/event work only for a bounded interval.
- Preserve the last valid snapshot if a reload fails.
- Do not print credentials in status, logs or normal control responses.
- Keep IPC available when the optional HTTP control API is disabled.

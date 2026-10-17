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

The local IPC protocol is HTTP/1.1 `POST /ipc/v1` over the Unix socket, carrying
the typed `IPCRequest`/`IPCResponse` JSON envelopes. `CallIPC` uses Go's HTTP
transport with a Unix dialer; the server uses Go's request contexts and shutdown
handling. Requests and client responses are capped at 32 MiB, and one envelope
is accepted per request. Socket file
permissions provide the local access boundary; any non-loopback HTTP exposure
requires explicit operator configuration and appropriate network protection.

## Lifecycle expectations

The daemon owns database open/migration, snapshot construction, listeners,
bounded event/persistence workers, signal handling and cancellation-aware shutdown. A bad configuration
reload must leave the last valid snapshot active. Provider discovery failures
should affect the relevant provider operation, not make an otherwise valid
daemon unusable.

Listener acquisition is part of startup. The daemon reserves all enabled TCP
listeners before opening SQLite or publishing IPC, then hands those listeners
to Go's HTTP server after runtime initialization. Any startup error releases
the reservations. `Start`/`Stop` are serialized, and a second `Start` on a running
instance is rejected. The immutable `Addresses()` projection reports actual
bound addresses, including OS-selected ports, and is cleared after shutdown.

Unix IPC is reserved before state initialization as well. `gofrs/flock` holds
an exclusive OS lock on the socket's persistent lock file through worker/store
cleanup. The server preserves active listeners and non-socket filesystem
objects, recovers refused stale sockets under that lock, and checks ownership
before pathname removal. Binding is separate from serving: the accept loop
starts after runtime initialization, and readiness is verified by a status
response. A standalone `IPCServer` releases ownership on Close or context
cancellation; an embedded daemon retains its reservation through final cleanup.

Each IPC generation owns a separate context and shared drain result. Client
cancellation closes the HTTP request and cancels the matching handler; it does
not stop the daemon. Closing IPC cancels accepted connections, including
partial request bodies, and waits for handlers to return before releasing the
standalone socket or permitting daemon store cleanup. Concurrent Close calls
wait for the same drain; an old generation cannot cancel a restart. Handlers
must cooperate with context cancellation. `gobroom shutdown` sends its complete
acknowledgement before initiating Stop, and daemon Wait observes that internal
shutdown as well as process signals. The shutdown result is retained so Wait
also reports a cleanup failure initiated by another caller.

OAuth authorization-code exchanges and device acquisition/polling are tracked
daemon workers. Shutdown closes authorization admission, cancels active work
and drains it before closing SQLite. The code exchange is bounded by caller
cancellation, daemon cancellation, session expiry and its exchange timeout;
device polling intentionally survives the initiating client disconnect. Both
flows use one credential commit boundary that checks the live session, expiry,
context and expected credential generation while serializing against explicit
session cancellation. A late successful driver response cannot revive a
cancelled session or persist a token after cancellation won that boundary.
Drivers must still cooperate with context cancellation; in-process code is not
forcibly terminated. Cancellation after a completed commit does not undo it.

No network healthcheck or daemon-wide quota poll runs by default. Real attempts
feed passive runtime state; provider quota enrichment is evidence-triggered and
deduplicated. See [Passive health](PASSIVE_HEALTH_ROUTING.md).

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

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
Config stdin commands (`config-validate`, `config-diff`, `config-apply`) accept
exactly one non-null JSON object, preserve numeric values without float64
rounding, and cap raw input at 32 MiB. IPC independently caps the entire encoded
envelope, so a near-limit document may exceed that transport limit after framing.
Validation remains daemon-owned; invalid refs/options do not change stored or
serving configuration. Post-commit activation failures follow their explicit
receipts rather than pretending storage was rolled back.

Route selection reads immutable snapshots. Credential issuance and the final
dispatch revision fence read current SQLite state; the latter selects binding
metadata without selecting secret material.

Startup reserves every enabled HTTP listener and acquires Unix IPC ownership
before initializing state. An occupied data/control/callback port makes startup fail; any earlier
reservation is released. A later initialization or IPC error also releases the
reserved ports. After `Start` succeeds, the enabled listeners are already bound.
Embedders can request `127.0.0.1:0` and read the allocated ports from
`Daemon.Addresses()`. Addresses are cleared when the daemon stops.

## HTTPS auth callbacks behind a local TLS proxy

Callback ingress is disabled by default and is separate from data/admin HTTP.
Opt in with both flags (address shown is an example, not an allocated default):

```sh
gobroomd --callback-origin https://auth.example.com --callback-addr 127.0.0.1:2714
```

Terminate TLS at a local reverse proxy and preserve the browser's public Host
when forwarding to that private HTTP address. Do not expose the private socket
as a general API; it serves registered callback grants only. No callback API key
is required, while the data-plane bearer policy remains independent. TLS keys,
certificate renewal and public DNS remain the deployment's responsibility.
Gobroom does not derive origins from `Forwarded`/`X-Forwarded-*`. Plain HTTP
non-loopback origins, wildcard binds and remote proxy peers are not accepted.

The provider's registered HTTPS callback must have the configured origin.
Different origins can fall back to manual delivery only if the callback is
optional; required callbacks fail before consent. DNS case/default-port
equivalence does not alter the URI spelling sent to the provider's token
exchange. Keep redirects and path rewrites out of the proxy route: escaped path,
fixed query, nonce and permitted response fields are matched by the grant.
`Daemon.Addresses().CallbackIngress` reports the actual private bound address;
port zero is useful for embeddings/tests, not a stable proxy upstream.

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

IPC uses versioned HTTP/JSON framing on the Unix socket (`POST /ipc/v1`). CLI
and TUI use `CallIPC`; its context applies to dialing, writing and reading the
reply. Canceling the client request cancels the daemon handler. IPC shutdown
closes partial connections and drains active handlers before closing SQLite.
Handlers are required to honor cancellation; the daemon waits for their return.
Cancellation does not undo an already committed mutation; clients can read
daemon state to reconcile an interrupted command. IPC calls are not replayed
or redirected automatically.

`gobroom shutdown` acknowledges the command and then drains/stops the daemon.
The bundled user service restarts only on failure, so this clean shutdown
leaves it stopped until `systemctl --user start gobroomd` or `make install-all`.

`connections authorize-cancel --session-id ID` can cancel an in-flight code
exchange, not only a session waiting for a callback. Code exchanges also stop
on caller disconnect, daemon shutdown or session expiry. Device polling remains
daemon-owned after its starting command exits; use `connections device-cancel`
to stop it explicitly. Both flows reject a late token from a cancelled driver
before credential commit. Cancellation cannot reverse an already committed
credential; inspect the connection/session when commands race completion.

Credential setup/refresh uses a durable internal revision, not token equality.
Replacing a token and later restoring it, resaving the same token, changing mode,
or disabling/re-enabling the connection supersedes any older exchange. An old
device flow cannot write into a disabled/deleted/recreated account. Revision
metadata is local state and is never included in portable config bundles.
Resolve/refresh and lock waits honor caller and daemon cancellation; exchanges
on unrelated connections remain independent. A token already committed is not
rolled back by a later route/health reconciliation failure.

Changing a provider's definition ID or contract version invalidates outstanding
credential work for its connections, even if the definition is changed back.
An old setup completion reports failure; start a new setup for the current
definition. Provider-node renames and unchanged definition values do not revoke
pending work. Existing secret bytes are retained, not converted into the new
driver's schema; that driver's `Resolve` validates them, and incompatible state
needs fresh credentials. Old pinned serving/quota bindings cannot reinterpret
state under a different current owner.

## Credential activation and reload receipts

Automatic recovery is opt-in on each provider auth mode: `"recovery":"refresh"`.
Omitting it or selecting `"none"` disables error-triggered refresh regardless of
credential-type name. The error classifier must also provide normalized auth
recovery evidence. Default HTTP/JSON 401 supplies that hint; default 403
permission/quota does not. A provider's custom token-expiry status can be
configured without a kernel change, for example in its operation binding:

```json
{"errorClassifierOptions":{"httpJson":{"authRecoverySignals":[{"httpStatuses":[498],"codes":["credential_expired"]}]}}}
```

Declared signals replace default 401 matching. Replay policy still controls
whether the business request can be resent; recovery never permits switching
issuer after an ambiguous keyed attempt. Module builds use host contract v6;
old auth/device/recovery contracts are not automatically adapted. Credential
renewal preserves configured-principal quota/operational cooldown, while old-token
authentication failures use their separate credential revision. Explicit
principal replacement discards the former account's evidence.

Saving an OAuth/device/refresh credential and applying its runtime configuration
are separate stages. A successful authorization can report `completed` with
`activation.status=pending`: its credential is committed, not rolled back.
The daemon reconciles locally without repeating the provider exchange, even
when no frontend remains connected. Refresh does not reload configuration on
the inference request's critical path.

```bash
gobroom connections activation-list --limit 128
gobroom connections activation-get --connection-id CONNECTION
gobroom connections activation-retry --connection-id CONNECTION
```

Jobs are retained once per configured connection, resume after restart, and
attempt activation up to five times one second apart before `exhausted`.
Explicit retry starts a new attempt budget for the current credential only;
superseded generations cannot reset a newer owner's state. The worker stops
its timer when idle. If storage prevents recording progress, bounded retry
failure is logged and an explicit retry or restart is required.

Receipts expose routing reset, control publication, persisted version and actual
serving publication/version. `status` now reports `controlSnapshotVersion`,
`servingSnapshotVersion` and `lastReload`, with no ambiguous `snapshotVersion`
alias. A failure after publication remains `degraded` even when versions match.
`/models` follows the actual reported serving snapshot, not an unapplied control
candidate. Activation views exclude secret bytes and private revision keys;
they are runtime state, not part of a portable config bundle. The TUI keeps
polling pending activation and provides a retry hint when attempts exhaust.

## Operation probes with local files

`gobroom connections test-operation` accepts `--payload-file payload.json`
and optional `--artifacts-file files.json`. The latter is a strict JSON array:

```json
[{"role":"audio","path":"/local/sample.wav","mediaType":"audio/wav"}]
```

Use the exact roles from the registered operation's artifact ports. In the
Connections TUI, `P` exposes the same manifest as the `artifactFiles` field.
Paths belong to the frontend machine: the daemon receives streamed bytes,
not local paths or base64. MIME can be inferred from the filename; specify it
explicitly when inference would be ambiguous. Either input may use stdin (`-`),
but not both at once.

The upload endpoint exists only on Unix IPC; neither public HTTP nor an HTTP
control listener is needed. Metadata and CLI payloads are capped at 128 KiB,
with at most 32 files and a 32 MiB total multipart body. Artifact type,
operation and store limits may impose tighter bounds. Exact provider/task
refs are checked before upload and again before execution; artifact
interpreters and recipient authorization run before upstream dispatch.
Probe leases are released on success or failure. Results contain bounded
response metadata, not provider response bodies. Probes do not update model
catalog, passive health, quota or usage; they can still incur provider cost
or create upstream resources. Cancellation cannot undo an upstream mutation.

## Transform bindings

Transforms are registered implementations, not scripts or arbitrary JSON
patches. Inspect the extension catalog for exact refs and option schemas, then
manage persisted bindings over the normal daemon IPC socket:

```sh
gobroom extensions catalog
gobroom transforms list
gobroom transforms upsert --file binding.json
gobroom transforms upsert --file - < binding.json
gobroom transforms delete --id my-binding
```

For a registered request transform, a binding has this shape (replace the
example ref and options with those declared by its module):

```json
{
  "id": "my-binding",
  "transformRef": {"kind": "request_transform", "id": "example.transform", "contractVersion": 1},
  "enabled": true,
  "scope": {"kind": "model", "id": "middle"},
  "order": 0,
  "failureMode": "fail_closed",
  "options": {}
}
```

The CLI accepts one typed JSON object, rejects unknown fields and input over
1 MiB, and preserves option numbers through IPC. The daemon remains authoritative
for catalog/schema/scope validation and activation. Registration alone does not
enable a transform; bindings survive restart. Canonical content and operation
inputs may change only under declared effects; artifact ownership, opaque
provenance and daemon-owned facts remain protected.

For streaming OpenAI Chat clients, `stream_options: {"include_usage": true}`
requests a final known-usage chunk before `[DONE]`, including when transforms
force semantic rendering. Opt-out omits that chunk; unavailable counters are
not invented. Typed binding rejects malformed/unsupported stream options.
This changes client visibility, not stored provider usage or billed accounting.

## Service and logs

Usage persistence runs independently of requests and provider health. Status
over IPC and HTTP includes `usagePersistence` counters: `received`, `persisted`,
`failed`, `abandoned`, and `queueDropped`. These are process-lifetime counters,
sampled independently while running. Failed/unacknowledged writes or admission
drops keep status degraded; a later success does not acknowledge earlier work.
No raw database error, request payload or credential is exposed in that view.

`gobroomd --usage-drain-timeout 5s` controls the cooperative usage-writer budget
after execution producers close. Zero selects the default five seconds;
negative values fail before startup. Caller cancellation also reaches the
writer. Storage receives context through its existing atomic event/daily
transaction. The owner joins the writer before closing SQLite, reports failed/
abandoned counts to the journal and returns an incomplete-persistence error.
The IPC shutdown command acknowledges scheduling, not successful later drain;
the daemon's exit/Stop/Wait result and journal carry that result.

This is not a hard wall-clock limit on uncooperative storage or the whole daemon:
other owned workers have separate lifecycles, and driver/filesystem calls must
cooperate with context cancellation. Failed writes and queue overflow are
observable, not automatically retried or durably spooled by this change.

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

## Credential recovery

Credential refresh is a `renewal`: it invalidates old-token authentication
failures without resetting configured-principal quota or active rate-limit
cooldowns. Interactive reauthorization is a `replacement`: it starts a new
principal epoch and discards the former connection's health/quota evidence.
Activation views report that transition separately from readiness. Health views
and the inspector display authentication evidence independently from operational
restrictions. A successful request or model discovery is not proof that an
existing quota/rate restriction has ended. Provider-wide auth failures are not
cleared by renewing a single account. Cross-connection shared identity is not
inferred. Explicit operator resets remain separate recovery actions.

## Action-based setup

For a provider mode advertising `actionAuthorization`, action-based setup is
available through the control client:

```bash
gobroom connections setup start --connection-id CONNECTION
gobroom connections setup get --session-id SESSION
gobroom connections setup submit --session-id SESSION --action-id ACTION --revision REVISION --input-file /secure/path/input.json
gobroom connections setup cancel --session-id SESSION
```

Submit only an offered input action at the returned revision. Input is one JSON
object, at most 64 KiB, validated against the action's exact registered schema.
Use a mode-0600 file or stdin; do not put secret values on the command line.
The daemon owns requested polling, so keeping a CLI/TUI open is unnecessary.
`completed` means the credential committed; inspect the separate activation
receipt before assuming a new routing snapshot is ready. Restart retains durable
credentials/activation, not private interactive sessions. Code and device flows
use this same action surface; their previous dedicated IPC/CLI commands are gone.
Configured factory/strategy fixtures do not establish live-provider acceptance.

Action setup may now report `callbackModes` per input action. `loopback` means
the daemon owns a matching fixed-loopback listener; `manual` means an optional
listener could not be acquired and no HTTP grant exists. Submit the provider's
callback input through `setup submit --input-file` when manual handoff is offered.
Do not put callback URLs/codes in argv or logs. The browser page acknowledges a
received response, not routing readiness: inspect setup status and activation.
Callbacks use the same input schema/revision checks as manual actions. Terminal
sessions release their grants and the last socket; restarting retains committed
credentials, not a pending callback session. A driver may request port zero:
the daemon owns the OS-assigned port before the driver registers its redirect
URI or returns consent actions. The CLI only sees the resolved final actions;
there is no frontend port probe or port-selection command. Generic TUI callback
input forms remain unfinished.
Device setup already uses `connections setup start/get/cancel` and the same
generic public actions in TUI. Its upstream await continues without a frontend;
the OAuth library owns pending/slow-down timing. `device-start/get/cancel` and
`auth.device.*`, `auth.authorization.*` and `authorize-*` no longer exist. Generic
setup deadlines are provider-declared within a one-hour host ceiling; the code
strategy defaults to ten minutes. For a definition supporting both OAuth methods,
submit `{"method":"code"}` or `{"method":"device"}` at its `select_method` input
action, or configure `authOptions.oauth.setupMode` to select a method directly.
Callback input can be `{"url":"..."}` or `{"code":"...","state":"..."}` in the
private submit file/stdin; never pass these values in argv. A rejected input leaves
its challenge available for correction, while exchange/cancellation failures do not.

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

## Local performance baseline

Context-aware persistence spot check (`463e47d`, Linux/amd64, CPU=1,
`make perf BENCHTIME=200ms BENCHCPU=1`): response/completion validation passed
for serial/parallel direct/proxy JSON/SSE. Serial proxy allocated 98,859 B/op,
1,254 allocs/op for JSON and 136,386 B/op, 1,785 allocs/op for SSE. This short
combined-process fixture is not an isolated-daemon RSS or tail-latency result,
nor a controlled speed comparison against the historical profile below.

Run `make perf BENCHTIME=1s BENCHCPU=1,4`. This uses Go's benchmark runner,
`ReportAllocs` and `RunParallel`, with a fake local upstream and fresh temporary
daemon DB/socket. It checks JSON and SSE response content/completion and drains
bodies. Setup is outside the child benchmark timer. Access-log output is
discarded for the benchmark; internal logs, health and usage persistence remain
active. No real provider or installed-service configuration is used.

This is a combined-process baseline: generator, fake upstream and daemon all
contribute to ns/op and allocated bytes. Parallel ns/op describes throughput,
not per-request tail latency. It does not measure isolated daemon RSS, provider
network latency, TTFT distribution or long-lived memory growth.

An initial 2026-10-21 run on Linux/amd64, i7-6820HQ, `-benchtime=300ms -cpu=1,4`
identified SQLite preparation, compatibility planning and health-key traversal
as costs. After fixed `database/sql` prepared statements, planner allocation
reduction and standard `iter.Seq` traversal, CPU1 measurements were:

| Fixture | Before µs/op | After µs/op | Before B/op | After B/op |
|---|---:|---:|---:|---:|
| JSON direct | 74 | 100 | 8,379 | 8,379 |
| JSON proxy | 936 | 1,041 | 112,355 | 98,372 |
| SSE direct | 81 | 193 | 10,307 | 10,307 |
| SSE proxy | 1,140 | 996 | 149,773 | 135,566 |

Allocated bytes fell about 12% for JSON and 9% for SSE in this fixture;
allocations/request fell 1,277→1,242 and 1,806→1,770 respectively. Direct timing
also varied substantially, so these short runs do not establish a latency
speedup. Health-key traversal separately measured 0 B/op and 0 allocs/op.

For a CPU/heap profile, run from `examples/external-provider-module`:

```sh
go test -run '^$' -bench '^BenchmarkOpenAIChatProxy/JSON/Proxy$' \
  -benchmem -benchtime=2s -cpu=1 -cpuprofile=/tmp/gobroom-proxy.cpu \
  -memprofile=/tmp/gobroom-proxy.mem -o /tmp/gobroom-proxy-profile.test .
go tool pprof -top /tmp/gobroom-proxy-profile.test /tmp/gobroom-proxy.cpu
go tool pprof -top -alloc_space /tmp/gobroom-proxy-profile.test /tmp/gobroom-proxy.mem
```

Profiles contain only fixture data, but heap profiles include startup and
calibration allocations too. Keep workload, build, CPU setting and logging
policy fixed when comparing. Do not call the product “nearly invisible” from
these numbers: isolated idle/loaded RSS, tail latency and longer load tests remain
separate acceptance work.

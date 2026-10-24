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

For a fresh deployment that needs portable definitions before its linked module
catalog can freeze, supply `gobroomd --config-bundle /absolute/path/config.json`
(embedders: `Config.ConfigBundlePath`). Startup reads bounded strict JSON,
registers portable definitions, validates exact code/schema pins and candidate
runtime configuration, then applies the bundle before serving. Required module
code must already be linked; external/non-portable definitions still require
their declared installed manifests. The bundle does not transfer credentials.

This is explicit file-managed desired configuration: it reapplies on every
Start while the option remains configured. Locally supplied credentials for
retained connection IDs are preserved. Remove the option after bootstrap to
use ordinary SQLite/IPC-managed configuration; otherwise later file reapplication
can overwrite graph changes made interactively. Conflicting exact definitions
fail rather than silently replacing installed code/config contracts. Parser
errors do not echo input; semantic validation diagnostics can include configured
identifiers. Do not put credentials or confidential identifiers in a bundle.

Run `make test-installed-portability GOBROOM_TEST_CLI=/absolute/path/to/gobroom`
for the separate-module fresh-target/restart and rejection fixtures. It uses
temporary daemons/fake upstreams, not the configured live provider accounts.

Route selection reads immutable snapshots. Credential issuance and the final
dispatch revision fence read current SQLite state; the latter selects binding
metadata without selecting secret material.

Startup reserves every enabled HTTP listener and acquires Unix IPC ownership
before initializing state. An occupied data/control/callback port makes startup fail; any earlier
reservation is released. A later initialization or IPC error also releases the
reserved ports. After `Start` succeeds, the enabled listeners are already bound.
Embedders can request `127.0.0.1:0` and read the allocated ports from
`Daemon.Addresses()`. Addresses are cleared when the daemon stops.

TCP data/control listeners require request headers within three seconds,
matching the callback receivers. This uses the standard HTTP header deadline;
it is not a three-second body, inference or streaming timeout. It also bounds
connections accepted without any request bytes, which otherwise can occupy
the standard server's shutdown grace period. Caller shutdown budgets still
apply; this is not a hard wall-clock guarantee for every cleanup worker.

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

## Usage capture and explicit detail access

Ordinary usage lists and summaries contain common counters and diagnostics, not
extension payloads or raw usage. Raw capture is off by default. Enable
`--usage-capture-raw` only when needed: built-in decoders retain a bounded,
validated usage object, never the complete provider response or headers.
Unknown or unsafe shapes are omitted with a diagnostic. This is not a sandbox
for arbitrary statically linked modules.

Host bounds default to 32 extension values, 65536 extension bytes per attempt,
and 262144 raw bytes. Override with `--usage-max-values`, `--usage-max-bytes`
and `--usage-max-raw-bytes`. All bounds must be positive. The positive
`--usage-retention` duration defaults to 720h (30 days). Local pruning runs at
startup and every 15 minutes in bounded batches; expired details are denied
even while physical cleanup catches up. This does not poll providers.

All detail access is denied by default. `--usage-details` permits explicit
management detail requests; `--usage-private-details` and
`--usage-raw-details` separately permit those classes and require the parent
grant. Capture does not grant access. These startup flags require a daemon
restart. For an authorized host:

```sh
gobroom usage detail 123
gobroom usage detail 123 --include-private --include-raw
```

In the TUI Usage view, `d` opens disclosure options; selecting an event alone
does not fetch details. Both frontends escape terminal controls and omit detail
output exceeding their 64 KiB display budget rather than truncating JSON.

Unix control uses the private IPC socket. HTTP control remains disabled by
default. Enabling `--http-control` requires an independent `--control-token`
or `GOBROOM_CONTROL_TOKEN`, different from the data-plane `--http-token`.
Every HTTP control route requires this bearer token. Explicit details use
`POST /api/usage/detail` and still require the host's class-specific grants.
Treat possession of the control token or IPC socket access as administrative
authority; keep them private and terminate TLS outside the daemon if remote.

After installation, run the explicit installed-client gate:

```sh
make test-installed-usage GOBROOM_TEST_CLI=/absolute/path/to/gobroom
```

It rejects a missing/non-executable path and runs the named CLI acceptance
fixtures three times with the race detector. These use temporary daemons and
fake upstreams, not configured live providers. Ordinary `make test` does not
prove installed-binary acceptance when the CLI environment variable is unset.

## Client operation failures

Registered operation routes use their ingress's failure encoder for auth,
decode, preparation and execution errors before response commitment. Ordinary
Go error text and upstream bodies are private. Only explicitly supplied bounded
public failure metadata and diagnostics reach the encoder. HTTP status and
dispatch effect remain host-owned; error encoding never grants automatic replay.
Invalid/failed encoders return an empty HTTP 500. Once committed, the existing
terminal-stream behavior applies instead. `/v1/models` retains its stock auth
response. Public provider modules must use host contract 14.

After installation, run:

```sh
make test-installed-client-failure GOBROOM_TEST_CLI=/absolute/path/to/gobroom
```

The gate requires an executable absolute CLI path and runs the independent
client-dialect and installed catalog/control fixtures three times under the
race detector. Fixtures use temporary databases, Unix sockets and fake HTTP
upstreams, not configured live providers.

## Service and logs

Status also exposes `sessionPersistence`, separately from usage accounting:
`received`, `persisted`, `failed`, `discarded`, `queueDropped`, `saveErrors`.
Writer counters cover the built-in session cache, including acknowledged
tombstones. `saveErrors` covers unexpected kernel Save errors, including custom
stores; it does not invent diagnostics for their hidden asynchronous workers.

Changed principal/definition/endpoint, removed/disabled authority and expired
queued state are deliberate safety fences. They use typed
`ErrSessionStateSuperseded` and count as `discarded`, not storage failure.
SQL/metadata read failures remain genuine faults. Custom stores can use the
public provider sentinel for the same expected result; no error-string matching
or provider-specific branch supplies that classification.

Failed writes, queue rejection and unexpected Save errors degrade daemon status
and produce one sanitized warning per daemon run. These counters retain fault
history until the next Start/Stop run; a later successful write does not erase
it. They do not assert that newer durable state is still inconsistent. A queue
drop can also be a kernel Save error: do not add the counters as unique losses.
At quiescence, received equals persisted plus failed plus discarded; live fields
are sampled independently. No session ID, response ID, opaque state, signature
or raw repository error is included in these new operational outputs.

Live continuity remains in RAM after rejected persistence. Current inference
success, provider health and accepted-effect replay fences stay unchanged.
There is no new retry, journal, per-key metric or route-selection SQLite read.
After joining the session worker, Stop/Wait returns the constant public daemon
`ErrSessionPersistenceFailureObserved` if fault history exists. Repeated Stop
retains that result. IPC shutdown still acknowledges scheduling, not successful
later drain. This adds no hard deadline for an uncooperative repository/logger.

Run `make test-installed-session GOBROOM_TEST_CLI=/absolute/path/to/gobroom`
after installation. This explicit gate validates the binary path and exercises
temporary hosts with injected SQLite failures and custom Save errors; ordinary
source tests may skip the installed-only fixture when its environment is unset.

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

The 2026-10-23 hybrid-usage comparison used runtime `d8d63fa` versus
`881f453`, Go `go1.27.3-X:nodwarf5`, i7-6820HQ, five 1s samples at CPU=1/4.
Both used the same instrumented harness, whose SHA256 is recorded with the
[baseline](performance/2026-10-23-usage-baseline.txt) and
[post-change samples](performance/2026-10-23-usage-post.txt). Proxy cases
reported zero usage drops and completed accounting drain outside the timer.

Selected medians, combined-process request windows:

| Fixture | Before µs/op | After µs/op | Before B/op | After B/op | Before/after allocs/op |
|---|---:|---:|---:|---:|---:|
| JSON serial, CPU1 | 774 | 1329 | 98,779 | 119,157 | 1255 / 1458 |
| JSON parallel, CPU4 | 596 | 425 | 97,721 | 117,124 | 1250 / 1444 |
| SSE serial, CPU1 | 929 | 1367 | 136,257 | 153,915 | 1783 / 1931 |
| SSE parallel, CPU4 | 519 | 498 | 135,850 | 152,721 | 1783 / 1924 |

The extra allocations are measurable: serial JSON +20,378 B/op (+20.6%) and
SSE +17,658 B/op (+13.0%). Do not describe this as negligible overhead. Timing
is noisy: direct JSON parallel CPU4 changed 29→59 µs/op, while direct SSE CPU1
changed 132→85 µs/op despite unchanged upstream code/allocation counts. These
ambient desktop samples cannot establish a causal latency regression/speedup.
Asynchronous storage work after the timer is not included in ns/op or timed
allocation totals; drain checks establish completion/loss, not full lifecycle
CPU accounting.

The [helper samples](performance/2026-10-23-usage-helpers.txt) separately
measure selected public APIs, not decoding, the attempt pipeline or SQLite.
CPU1 medians from five samples:

| Helper workload | µs/op | B/op | allocs/op |
|---|---:|---:|---:|
| Clone three known common counters | 0.198 | 24 | 3 |
| Validate/own/clone small registered extension | 9.318 | 2,432 | 47 |
| Same, 64 KiB encoded extension ceiling | 696.690 | 460,604 | 42 |
| Reject oversize extension | 0.341 | 80 | 3 |
| Raw selection disabled | 0.0057 | 0 | 0 |
| Raw selection, small object | 0.647 | 176 | 3 |
| Raw selection, 256 KiB ceiling | 371.892 | 262,285 | 3 |
| Reject oversize raw fragment | 0.058 | 64 | 1 |

Raw selection is the bounded ownership helper, not a claim that the synthetic
padding object is accepted by built-in decoder shape validation. Near-ceiling
extension processing has substantial temporary allocation despite finite
retained buffers. Helper CPU4 cases are still serial loops, not parallel
request throughput.

A qualitative [allocation profile](performance/2026-10-23-usage-clone-profile.txt)
places `UsageEvent.Clone` at 11.08% cumulative allocated space in that diagnostic
run, mainly compatibility mappings/path ownership copies. This identifies an
optimization candidate, not proof that it explains the entire measured delta.
Profiles include startup/calibration and have different request counts; their
totals are not a normalized before/after cost. Boundary ownership must remain
intact when reducing repeated internal clones. Full-path CPU attribution,
isolated RSS and sustained tail latency remain follow-up measurements, not
inferred results.

The [paired end-to-end samples](performance/2026-10-24-usage-paired.txt) add the
previously missing actual extension-bearing proxy workload: five 1s samples at
CPU1/4, JSON/SSE through decoding, attempt admission and SQLite. Both cases load
the same module, graph, upstream headers/body and disclosure settings; only the
usage-source binding changes. A non-benchmark fixture verifies that both actual
JSON/SSE attempts persist exactly one precise private value when enabled and
none when disabled, with no value leaked into client responses.

| Wire / CPU | Common µs/op | Extension µs/op | Common B/op | Extension B/op | Common/extension allocs/op |
|---|---:|---:|---:|---:|---:|
| JSON / 1 | 910 | 1438 | 120,529 | 151,204 | 1479 / 1986 |
| JSON / 4 | 1195 | 1032 | 121,247 | 150,318 | 1483 / 1972 |
| SSE / 1 | 1922 | 1669 | 155,271 | 185,906 | 1955 / 2462 |
| SSE / 4 | 1702 | 1372 | 156,515 | 186,627 | 1964 / 2461 |

All paired samples reported zero admission drops and drained accepted accounting
outside the timer. Small private extension processing adds about 30 KiB and
roughly 500 allocations per request in this workload, not merely the helper's
2.4 KiB. CPU4 cases here still have serial clients plus asynchronous daemon
workers, unlike the earlier `RunParallel` cases. Ambient timing remains noisy;
do not interpret an extension case's lower median as a speedup. The earlier
before/after comparison used the pre-header-budget runtime checkpoint; this
paired comparison includes the three-second HTTP header budget in both cases.

Reproduce five-sample runs with `make perf BENCHCOUNT=5` and
`make perf-usage BENCHCOUNT=5`; use `make perf-usage-proxy BENCHCOUNT=5` for the
paired end-to-end workload. All accept `BENCHTIME` and `BENCHCPU`.

Earlier measurements below describe their historical checkpoints, not the
current runtime's resource cost.

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

# Session persistence failure observability

Status: agent-selected design under the user's standing instruction to decide
implementation details and continue until a genuinely user-owned choice arises.
This is not a claim of a new explicit design approval.

## Problem and intent

`Cache.Save` preserves live continuity and queues an asynchronous write. It
returns queue rejection, but the kernel discards that error. The cache worker
also discards ordinary repository errors. A successful current response may
therefore hide failed persistence needed after restart. Invalidation already
has a durable acknowledgement and must keep that stronger contract.

Fix the missing operational evidence without replaying an accepted inference,
penalizing provider health or adding disk I/O to route selection. The daemon
must work without a TUI and report faults through existing control/log paths.

## Approaches and source evidence

- Logging only is small but insufficient for headless automation and status.
- Typed process counters, sanitized status/logs and a shutdown error reuse the
  existing usage-persistence observer pattern. Selected: different session
  semantics remain explicit rather than sharing the usage worker itself.
- Durable retry/spooling or per-key reconciliation changes persistence
  guarantees; outside this repair.

Local CLIProxyAPI `internal/cache/antigravity_interactions_session.go` separates
bounded process-local continuation from durable storage. Its
`internal/runtime/executor/helps/session_id_cache.go` distinguishes required
lookup errors from best-effort fallback. Neither is a drop-in replacement for
GoBroom's generation-fenced cache/acknowledged invalidation. Reuse this problem
separation, existing GoBroom observers/logs, and stdlib `sync/atomic`/
`sync.OnceFunc`; no new dependency or telemetry framework is justified.

## Contract

`internal/sessionpersistence.Status` is a data-only operational DTO with unsigned
daemon-run-lifetime counters (one Start/Stop run), lower-camel JSON keys:

- `received`: queued writes the built-in worker has begun, including tombstones.
- `persisted`, `failed`: repository write outcomes, including tombstones.
- `discarded`: built-in writes intentionally fenced by changed authority,
  endpoint or expiry, identified by `ErrSessionStateSuperseded`, not an I/O fault.
- `queueDropped`: Save/Invalidate write admissions rejected by the bounded queue.
- `saveErrors`: kernel SessionStore.Save calls returning an unexpected non-nil error,
  including custom stores and validation/cancellation errors.

These are independently sampled facts, not an atomic transaction snapshot.
At quiescence, received equals persisted plus failed plus discarded. A queue rejection can
also count as a kernel save error; do not sum these as unique lost records.
Repository counts describe the built-in writer, not hidden asynchronous work
inside arbitrary statically linked custom stores.

`Status.Faulted()` is true when failed, queueDropped or saveErrors is nonzero.
Evidence remains sticky until daemon restart; later successful writes do not
erase earlier observed faults. This does not claim the latest durable state is
still incomplete: newer writes can supersede older session versions.

Cache adds `PersistenceStatus()` and an initialization-only `OnFailure func()`.
Count outcomes on both ordinary and shutdown-drain write paths; acknowledge
invalidation with its original result. Notify outside the cache lock, with no
error strings, IDs, payloads or retained event history. Replace the old narrow
`DroppedWrites()` accessor and update its callers; no compatibility wrapper.

Kernel adds initialization-only `OnSessionSaveFailure func()`. Invoke it once
for each unexpected failed post-response Save, passing no private data. Typed
superseded-state results are normal fences, not failures. Do not alter
successful response/usage, health feedback, selected route or replay behavior.
SessionStore's public interface and provider-module host contract stay unchanged.

The repository emits `kernel.ErrSessionStateSuperseded` only for known absent/
disabled bindings, changed principal/definition/endpoint or expired valid state.
Missing expiry and malformed state remain errors. SQL/metadata read errors must
not be relabelled as superseded; binding absence gets a typed store error rather
than comparing human error text. `pkg/provider` exposes the session sentinel
for custom stores. Such synchronous expected results do not increment saveErrors;
discarded counts remain scoped to the built-in writer, not invented custom-store
asynchronous diagnostics.

Daemon initializes the hook and cache callback before serving, with shared
`sync.OnceFunc` notification and an atomic save-error counter. A focused helper
assembles `sessionPersistence` for the existing API/IPC status; existing control
degradation must remain degraded. CLI/TUI consume that same safe section.
Callbacks log one constant warning per daemon run, never repository error
text, session IDs, response IDs, opaque payloads or signatures.

After producers stop and the existing session worker joins, Stop/Wait joins a
constant `ErrSessionPersistenceFailureObserved` when the snapshot is faulted.
Repeated Stop retains that result. Export the sentinel through pkg/daemon.
Scheduling shutdown via IPC is not an acknowledgement of a successful drain.
No new hard shutdown deadline is claimed: the existing repository is not
context-aware, and this work does not detach a writer before closing SQLite.

## Boundaries and verification

No per-key counters, dynamic metric labels, retry, new journal, schema/storage
migration, provider switch, background health probe or new listener. Live cache
state/TTL/versioning, acknowledged invalidation and load-failure fences remain.

Tests must prove: queue rejection preserves live state; ordinary write failure
and later recovery are both counted; invalidation acknowledgement is unchanged;
callbacks are outside locks; a custom Save error produces one callback without
replay/health poisoning; status contains only six numeric fields and preserves
other degradation; real inference with injected SQLite session-write failure
still succeeds once, exposes safe status/logs and fails shutdown explicitly.
Installed CLI must exercise that temporary-host fixture with its environment
required for the explicit acceptance run. All actual config/credentials remain
untouched. Finish with root/external regression, vet, selected three-run race,
make install-all and installed CLI acceptance.

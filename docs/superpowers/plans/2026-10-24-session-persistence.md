# Session persistence failure observability implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Main alone runs Go/format/build/test/install and commits; disjoint workers never compile.

**Goal:** Make synchronous Save rejection and asynchronous session write failures
observable without changing successful inference or provider health.

**Architecture:** Data-only operational status, cache-local atomics, a payload-free
kernel callback, and existing daemon status/log wiring. Preserve live continuity,
version fences and durable invalidation acknowledgements.

**Tech Stack:** Existing Go 1.27, SQLite, HTTP/Unix IPC, stdlib atomics/OnceFunc.

**Spec:** `docs/superpowers/specs/2026-10-24-session-persistence-design.md`

## Global Constraints

- No new dependency or telemetry framework; no provider-specific kernel branch.
- Status counters are received, persisted, failed, discarded, queueDropped, saveErrors.
- Expected authority/endpoint/expiry fences use ErrSessionStateSuperseded, not string matching or suppression of infrastructure failures.
- Evidence is daemon-run-lifetime and sticky; not a current-disk consistency claim.
- A queue rejection can also count as a kernel save error; do not sum them as unique lost records.
- No raw error strings, IDs, payloads or signatures in new status/log/error outputs.
- Preserve response success, live continuity, provider health and replay fences.
- No retry, new journal, schema migration or compatibility wrapper.
- Preserve actual config/credentials; feature traffic uses temporary hosts only.
- Main observes RED before production permission; only main runs Go and commits.

## Review Focus

1. Cache callbacks may query cache state: never call them while holding its lock.
2. Failed tombstones still restore/acknowledge correctly and cannot roll back newer state.
3. A custom session-store Save failure must not replay an accepted inference or poison health.
4. Errors during shutdown drain must be reflected after joining, not lost in a pre-drain snapshot.
5. Later success must not hide fault history, but status must not claim latest disk state remains inconsistent.

## Task 1: Observe the bounded cache writer

**Files:** new `internal/sessionpersistence/status.go`; modify
`internal/runtime/sessionstore/cache.go`, `save_admission_test.go`; new
`internal/runtime/sessionstore/persistence_status_test.go`; typed repository
classification in `internal/store/store.go`, `credential_binding.go` and
`session_outcome_test.go`.

**Interfaces:** main supplies Status with the six uint64 JSON fields and
`Faulted() bool`. Cache produces `PersistenceStatus() sessionpersistence.Status`
and initialization-only `OnFailure func()`; saveErrors remains host-owned zero.

- [x] Add `TestCachePersistenceFailureRemainsVisibleAfterLaterSuccess`,
  `TestCachePersistenceOverflowPreservesLiveStateAndNotifiesOutsideLock`, and
  `TestCachePersistenceInvalidationAcknowledgementAndDrainCounters` against the
  real worker/controlled repository; assert exact counts, restored/newer state
  and callback lock safety. Update the removed DroppedWrites caller.
- [x] Main observes missing DTO/method/hook RED, then adds the DTO and authorizes production.
- [x] Add typed repository/controlled-worker tests separating expected
  supersession from SQL metadata failures; main observes RED before classification.
- [x] Add atomic writer outcomes on normal and drain paths, count queue drops
  for both admissions, keep existing acknowledgements and notify outside locks.
- [x] Main focused tests and three-run race; scoped spec/quality review; commit.

## Task 2: Observe failed kernel saves without semantic side effects

**Files:** modify `internal/kernel/kernel.go`; new
`internal/kernel/session_save_failure_test.go`, `session_save.go` for the shared
sentinel; expose that sentinel through `pkg/provider/provider.go`.

**Interfaces:** `Kernel.OnSessionSaveFailure func()` set before execution; no
SessionStore interface or module-contract change.

- [x] Add `TestKernelSessionSaveFailureIsObservedWithoutReplayOrHealthPenalty`
  using real Execute and a controlled failing Save implementation. Assert one
  upstream execution, one payload-free notification, successful completion,
  unchanged known usage and no failure feedback; retain load-failure behavior.
  Wrapped ErrSessionStateSuperseded and nil Save results must not notify failure.
- [x] Main observes missing hook RED, then authorizes the narrow Save error check.
- [x] Implement the hook; main runs kernel regression/three-run race, review and commit.

Tasks 1 and 2 have disjoint implementation files and may run in parallel under
the user's explicit multi-worker instruction; all Go remains sequential in main.

## Task 3: Connect safe status, logs and shutdown to installed clients

**Files:** new `internal/daemon/session_persistence.go`,
`session_persistence_test.go`, `internal/api/session_status_test.go`; modify
daemon/API initialization and status wiring, pkg/daemon error export, Makefile
installed-session gate and readiness path, and the
TUI status binding only if needed for exact numeric projection.

**Interfaces:** `Server.SetSessionPersistenceObserver(func() sessionpersistence.Status)`;
daemon helper binds snapshot callbacks before serving and exposes the constant
`ErrSessionPersistenceFailureObserved`, joined after the session worker exits.

- [x] Add healthy/faulted/other-degraded API tests with exact six-field projection.
  Add `TestDaemonSessionPersistenceFailureDoesNotReplayInference` and
  `TestInstalledCLISessionPersistenceFailureVisibility`: actual Responses
  inference with x-session-id, a SQLite trigger rejecting provider_sessions,
  safe CLI/IPC/HTTP status and logs, later successful write, sticky evidence,
  explicit Stop/Wait error and repeated Stop. Check no private marker escapes.
- [x] Main observes RED before API/daemon wiring; use shared OnceFunc warning,
  kernel save-error atomic and cache snapshot, not a new status subsystem.
- [x] Test TUI status displays the section and preserves large uint64 counters;
  use maintained JSON number binding if its existing generic decoder rounds.
- [x] Main runs full make test/vet/conformance, external vet and focused race3;
  install-all, explicitly run installed acceptance with GOBROOM_TEST_CLI set,
  check actual daemon/config fingerprints, and obtain final combined review.
- [x] Update operations/product status/C1–C6 evidence narrowly, commit; do not
  mark the whole product goal complete from this persistence slice.

## Verified checkpoint — 2026-10-24

Commits `eeec107` and `7eda7ed` implement the three tasks. Full root/external
regression, root/external vet and conformance passed. Focused core/host and the
existing independent principal/endpoint vertical passed three race runs.
A real SQLite first-failure-during-shutdown test passed three race runs; a Go
overlay freezing the snapshot before drain failed as intended, without changing
repository source. Full daemon regression/vet passed after that test addition.

`make install-all` with absolute temporary BIN/DAEMON paths passed; the explicit
installed-session CLI gate passed three race runs (6.894s). Main status=ok,
control=serving=513; both persistence sections report zero faults. Config export
piped to validation passed version 9, config/credential fingerprint stayed
unchanged and usage rows stayed 0/0. No live provider traffic or setup mutation.

Task reviews and final whole-feature review approved, with no Critical/Important
remaining. One nonblocking coverage minor is retained: a failure first dequeued
by the cancellation-drain branch is not independently exercised; the current
implementation shares `persist` for both branches, and shutdown in-flight failure
is directly tested. This slice does not close C1–C6, custom hidden async workers,
whole-daemon hard shutdown, or general logging backpressure. Those are not
silently inferred from these passes. The finite architecture audit identifies
registered client precommit failure projection as the next concrete generic gap.

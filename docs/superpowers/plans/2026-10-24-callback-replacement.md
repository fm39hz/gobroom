# Public callback replacement implementation plan

> **For agentic workers:** Main executes the coupled callback fixture inline; the installed/privacy sidecar has a disjoint test file. Only main formats, compiles, tests, installs and commits.

**Goal:** Prove public credential replacement fences late callback completion
without blocking replacement or corrupting serving/restart state.

**Architecture:** Existing generic callback authorization driver, CAS/activation,
Unix IPC, typed setup views, provider lease and real HTTP inference.

**Tech Stack:** Existing Go HTTP/JSON/channels, public provider/daemon APIs.

**Spec:** `docs/superpowers/specs/2026-10-24-callback-replacement-design.md`

Status: complete for this bounded proof at `53bf5e5`. No runtime defect or new
authorization mechanism was required. Full root/module tests and vet passed
after review assertion fixes; public race3 passed in 3.538s. Install-all and the
explicit installed gate passed (race3, 9.730s). Actual daemon healthy at matching
snapshot 518, bundle validation v9 valid, scoped config/credential hash unchanged.
Deferred fixture minor: shared restart has no separate timeout. M15 remains open.

## Global Constraints

- No direct SQL/store access, fake executor, new dependency or cancellation engine.
- Gate provider completion by channels, no arbitrary sleeps to create the race.
- Exact existing connection/owner/principal fences; no retry or replay grant.
- Only main Go, temporary hosts/upstreams, real setup unchanged.

## Review Focus

1. Public replacement must return while a driver ignores cancellation.
2. Callback delivery must not acknowledge a credential rejected by the commit fence.
3. Terminal setup removes actions/callback capability and releases the listener.
4. Serving must use the replacement before/after late work and after restart.
5. Ordinary controls/logs must not echo callback proof or private credentials.

## Task 1: Public race and serving

Files: new `examples/external-provider-module/callback_replacement_test.go`;
small shared schema constant in `authorization_callback_test.go` if needed.

- [x] Decorate existing external callback flow, hold a valid result, configure
  connection/Physical/Combo through public IPC and begin an actual HTTP callback.
- [x] Replace through public `connections.update` while held; real endpoint
  uses replacement. Release callback, require stale rejection and no live action.
- [x] Prove listener rebind/replay rejection and HTTP serving after fresh restart.
- [x] Main targeted tests; any product defect gets RED→GREEN; full root/module
  regression/vet and focused race3 before committing.

## Task 2: Installed controls and evidence

Files: new `callback_replacement_cli_test.go` (worker), Makefile, acceptance/audit/docs.

- [x] Explicit installed test uses public fixture plus CLI setup/status/activation/
  logs through temporary IPC, verifies privacy and healthy serving snapshot.
- [x] Dedicated installed gate requires check-installed-cli and race/count3.
- [x] Fresh whole-feature review, material fixes with reproductions, install-all,
  installed gate and live status/config fingerprint read-only checks.
- [x] Update product/use-case evidence, record remaining M15 boundaries and commit.

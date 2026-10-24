# Independent module portability implementation plan

> **For agentic workers:** Main executes the coupled fixture inline; independent sidecars may inspect source/product evidence in parallel. Only main compiles, formats, tests, installs and commits.

**Goal:** Prove a separately linked module survives fresh-target config import
and restart with exact pins and real HTTP serving.

**Architecture:** Existing secret-free bundle and registered modules, public
IPC controls and temporary source/target hosts; no second configuration path.

**Tech Stack:** Existing Go stdlib JSON/HTTP, public daemon/provider APIs.

**Spec:** `docs/superpowers/specs/2026-10-24-module-portability-design.md`

## Global Constraints

- Bundle v9; provider host14; no legacy, new dependency or real provider calls.
- No source manifest copied to target, no secrets transferred by bundle.
- Preserve exported bytes; JSON binding and typed assertions for pins/results.
- Only main Go; in-place master preserved per existing workflow.

## Review Focus

1. Empty target cannot accidentally inherit source definitions or credentials.
2. Stored definitions must activate after a fresh daemon instance starts.
3. Bad exact pins must reject before any configuration mutation.
4. Import must not make inference calls or invent reusable job handles.
5. Installed CLI must address the temporary target, never the live daemon.

## Task 1: Public fresh-target proof

Executed amendment: fresh linked job module cannot start before its required
provider-definition exists. Add `internal/daemon/startup_bundle.go` and tests,
`Config.ConfigBundlePath`, and `cmd/gobroomd --config-bundle` before final proof.
Strict JSON v2 max32MiB; definitions precede freeze; exact lock/transform
validation precedes transactional apply and first serving. File mode reapplies
on every Start; DB-only restart is also proven with path removed. Negative pins
reject during Start and leave prior export unchanged. No catalog safety bypass.

Files: new `examples/external-provider-module/module_portability_test.go`.
Interfaces: existing `newFailureClientFixture`, `JobModule`, failure module,
public `daemon.NewWithProviderModules` and `daemon.CallIPC`; helpers are test-only.

- [ ] Add source export and fresh-target import/restart tests. Require no source
  manifests, no credential leakage, exact modules/schema/descriptor pins,
  restart-required activation, explicit credential provisioning, actual HTTP
  result/failure wire and repeated restart serving.
- [ ] Add negative module version/host/descriptor/schema and missing module
  cases with unchanged target export and zero inference side effects.
- [ ] Main execute targeted acceptance; if behavior is already green retain
  proof without unnecessary production edits. Actual defects get RED→GREEN.
- [ ] Full root/module tests and vet; focused module race3; commit checkpoint.

## Task 2: Installed gate and product evidence

Files: Makefile, architecture audit, product progress, M15 conformance.

- [ ] Add explicit installed portability gate using check-installed-cli and
  selected module tests with race/count3; CLI export/apply/status use temporary IPC.
- [ ] Fresh whole-feature review; fix only material findings with reproductions.
- [ ] make install-all, installed gate, live status/fingerprint read-only check.
- [ ] Record which C6 proof is closed and which SG1/auth/provider/performance
  use cases remain open, then commit the verified checkpoint.

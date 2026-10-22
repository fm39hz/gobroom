# Typed Job Results Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove typed asynchronous-job results through existing registered artifacts without JSON-in-text or kernel changes.

**Architecture:** Decode the job DTO into an inline result artifact. The renderer reads its validated value; the projector validates a logical artifact-reference result. The separately replayable job handle keeps its current owner/expiry boundary.

**Tech Stack:** Existing public `pkg/provider`/`pkg/daemon`, JSON Schema catalog, `encoding/json`, fake HTTP upstream and standard Go tests. No new dependency.

**Spec:** [Approved usage design, typed-results section](../specs/2026-10-22-usage-contract-design.md#typed-results-separate-proof-not-a-kernel-redesign). This independent plan awaits review with the [usage plan](2026-10-22-hybrid-usage-implementation.md).

## Global Constraints

- No kernel/projector API change, out-of-band shared result state or JSON-in-text tunnel.
- No compatibility shim for replaced pre-release logical result contracts.
- The main agent alone runs Go/tests/build/install and commits. A Luna medium worker owns only the jobs module/test files below.
- Preserve the connection-scoped five-minute job handle, issuer/recipient validation, restart behavior and accepted-effect replay fences.
- Result artifacts are request-local output data, not a new replayable job handle.
- Keep fake upstreams and temporary daemon paths; no live job creation.

## Review Focus

1. Concurrent jobs must not exchange results through shared mutable state.
2. A schema-valid artifact does not prove the exact client envelope is valid.
3. The transient result artifact must not be returned as a replay handle.
4. Status/cancel need result output declarations as well as create.
5. A malformed result must not produce successful completion or replay an accepted create.

## Task 1: Artifact-based module result and conformance

**Files:** Modify `examples/external-provider-module/jobs.go`, `jobs_test.go`; create `job_result_test.go`. Update `docs/M15_CONFORMANCE.md` only after main verification; main owns any Makefile selector changes. Production code outside the example module is not in scope.

**Interfaces:** Add a registered `example.jobs.result` artifact type with its exact result-value schema. Every action declares exactly one `result` artifact output; create also retains exactly one `job` handle output. Logical result schema becomes a versioned DTO identifying the result artifact type/role/media/size from the existing projector metadata envelope. `jobEnvelope` stays `{result: jobResult, artifacts?: []ArtifactRef}`, where `artifacts` contains the replay handle only. Keep decoded artifact identity/provenance in the normal event path.

Test assertion form: `if textEvents != 0 || resultArtifacts != 1 || got.Result.ID != upstreamID { t.Fatal("result did not traverse typed artifact path") }`. Each concurrent invocation asserts its own expected upstream ID.

- [ ] Add `TestJobResultUsesArtifactWithoutTextEvents`: collect decoder events; assert no `EventTextDelta`, exactly one declared result artifact, typed value equals the upstream DTO and the projector finalizes a valid logical ref.
- [ ] Main runs `GOCACHE=/tmp/gobroom-go-cache go test ./... -run '^TestJobResult' -count=1` from `examples/external-provider-module`; observe RED before worker implements the change.
- [ ] Register exact result artifact schema/policy/operation ports. Revise logical operation/result contract versions and affected manifest/lock references consistently; retain unrelated provider identity where unchanged. Include result and handle schema dependencies in module locks.
- [ ] Decoder publishes the result with `PublishInlineResponseArtifact`; projector consumes only its declared metadata, rejects missing/duplicate result refs and enforces bounds. Renderer binds result by exact type/role and decodes `ArtifactRef.Value` into its own DTO, rejecting duplicates/missing results. Do not put full JSON in `ItemID`, `Opaque`, text or global state.
- [ ] Add tests for all create/status/cancel envelopes using a separately registered client-envelope schema. Assert the full outgoing JSON validates independently of logical-result validation and contains no result replay artifact. Add malformed/duplicate/absent result, wrong type, invalid issuer and concurrent-distinct-result cases.
- [ ] Main runs module tests and `-race -count=3`, including `TestExternalJobLifecycleKeepsEffectsAndHandlesScopedAcrossRestart`; ensure handle restart/expiry/connection isolation and accepted-create no-replay behavior survive.
- [ ] Main runs full root/external conformance, `make install-all`, then reruns job lifecycle with `GOBROOM_TEST_CLI=/home/fm39hz/go/bin/gobroom`. Confirm fixture actually executes installed controls rather than skipping them.
- [ ] Commit `refactor(example): carry typed job results through registered artifacts`. Record module-only acceptance and remaining direct-projector limitation without claiming a generic value-projection API exists.

If the existing artifact/renderer route cannot satisfy these named tests, stop
and report the concrete contract obstruction. Do not quietly expand this plan
into a kernel redesign or manufacture request-local sharing across factories.

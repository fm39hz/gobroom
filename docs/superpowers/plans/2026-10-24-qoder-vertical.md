# Source-backed provider vertical implementation plan

> **For agentic workers:** Main owns typed payload, public fixture and integration. A worker owns envelope decoder; no worker Go/gofmt/tests/build/vet/commits.

**Goal:** Execute one source-backed encoded/body-signed provider through public
setup, model graph, inference and fresh restart without provider kernel edits.

**Architecture:** Schema-configured external request codec delegates existing
COSY helpers/prepared bytes; envelope wrapper delegates registered OpenAI decoder.

**Tech Stack:** Existing Go crypto/Base64/JSON/HTTP, uuid and public provider/daemon APIs.

**Spec:** `docs/superpowers/specs/2026-10-24-qoder-vertical-design.md`

## Global Constraints

- No toy-payload/live-provider parity claim, copied SDK or provider kernel branch.
- Sign encoded transmitted bytes; exact source path excludes /algo and query.
- Native model config is explicit schema-bound options for selected custom model;
  do not smuggle metadata into auth context or capability constraints.
- Delegate OpenAI semantic events/usage/terminal framing; accepted failure never replays.
- Main-only Go; fake upstreams and temporary config, actual setup unchanged.

## Review Focus

1. Signature/hash/length must cover final encoded bytes, not source JSON.
2. Source-required body shape must be asserted independently at the upstream.
3. Forced upstream SSE must still serve normal JSON clients correctly.
4. Envelope error/EOF after acceptance must not emit success or grant replay.
5. Restart must use stored credentials/config, not retained private runtime objects.

## Task 1: Typed provider and delegated envelope

Files: new `qoder_vertical_test.go`, `qoder_envelope_test.go`; existing signing
mechanics fixture helper for library-based Base64 reuse.

- [x] Tests first for source payload/signing and delegated text/tool/usage/DONE
  envelope behavior; main executes RED before implementation changes.
- [x] Schema-bound codec options, typed source payload and generic public module.
- [x] Public connection/model/Physical/Combo setup, actual JSON/SSE client calls,
  actual signed-body verification and serving after fresh daemon restart.
- [x] Error/truncation/terminal cleanup tests; no accepted-effect replay.
- [x] Full root/module tests/vet, focused race3 and commit verified checkpoint.

## Task 2: Installed acceptance and evidence

Files: Makefile, acceptance map, architecture/M15/product docs.

- [x] Explicit installed gate validates CLI path and runs cases race/count3;
  catalog/setup metadata/status and graph delete/apply target temporary IPC only.
  Credential creation uses public IPC; this is not an installed custom daemon.
- [x] Fresh whole-feature review; material fixes need reproductions.
- [x] Install-all, installed gate and read-only actual health/hash checks.
- [x] Record bounded source coverage and remaining dynamic/catalog/media/live
  provider/product obligations; commit without declaring full M15 complete.

Evidence: source checkpoint `758b405`; full root/module tests and vet passed;
focused race3 27.934s, installed race3 36.275s. See
[acceptance](../../QODER_VERTICAL_ACCEPTANCE.md) for the exact boundaries and
review limits. No new runtime dependency or provider-name kernel branch.

# Registered client precommit failure boundary implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Only main runs Go/format/build/test/install/commits. Workers have disjoint source scopes and stop for main RED before production.

**Goal:** New client dialects encode bounded safe failures before commitment
through their registered ingress contract, without handler/provider switches.

**Architecture:** Existing safe kernel failure metadata plus owned optional
diagnostics feed a required ingress failure method. Encoder strategies return
immutable bounded body/media data; HTTP status and execution authority stay host-owned.

**Tech Stack:** Existing Go 1.27, stdlib JSON/mime/context, catalog/schema compiler.

**Spec:** `docs/superpowers/specs/2026-10-24-client-failure-design.md`

## Global Constraints

- No new dependency/parser/framework/provider-name branch or database migration.
- Unknown Go/InputError text is private; only valid explicit public carriers opt in.
- ClientIngress requires RenderFailure(context.Context, ClientFailureContext) (ClientFailureResponse, error).
- Host contract advances 13→14 atomically; no old-interface detection/compatibility path.
- Diagnostics: 32 paths × 256 UTF-8 bytes, 32 reason identifier codes × 128 bytes, encoded total ≤8192 bytes; owned copies.
- Failure context encoded ≤32768 bytes; request ID/model each ≤128 bytes, invalid/oversized omitted, never partially sliced.
- Encoded failure body immutable string ≤65536 bytes; valid ContentType ≤256 bytes without controls.
- No raw error/request/headers/leases/opaque state enters render input; host owns status/effect/commitment.
- Bad renderer/context/media/body returns host HTTP500 empty body, no recursive fallback or error text.
- Preserve committed-stream terminal/no-success/no-replay and provider health/replay classification.
- Tests first; main observes RED; actual config/credentials remain unchanged; temporary fake feature traffic only.

## Review Focus

1. Decode/auth failure happens before a decoded format/session exists: selection must come from the registered ingress.
2. Ordinary errors may implement dangerous/private Error methods or wrap InputError; do not call Error for fallback presentation.
3. ErrNoRoute may follow dispatched rejected candidates: default execution effect stays unknown unless authoritative metadata exists.
4. A renderer can fail, overrun or return header controls: validate data before any client write and do not recursively invoke it.
5. Once committed, this boundary must not add another body/terminal or turn accepted work into replay.

## Task 1: Safe kernel projection and optional public diagnostics

**Files:** `internal/kernel/client_failure.go`; new/updated kernel privacy and
diagnostics tests. Existing committed-event contracts stay comparable/unchanged.

**Interfaces:** ClientFailureDiagnostics {Paths []string; Reasons []string},
Clone(), Valid(), ClientFailureDiagnosticsForError(error). Carrier method of
the same name opts in, validates and owns data. Existing ClientFailure stays
three fields; explicit carriers/cause identity and known sentinel mappings remain.

- [ ] Preserve executed RED `TestOrdinaryClientFailureUsesSafeMessageWithoutChangingIdentity`.
  Add unknown-error Error-not-called, private InputError, invalid diagnostics,
  exact byte/count limits and module buffer mutation cases with literal expectations.
- [ ] Main runs RED before worker production permission.
- [ ] Replace unknown/InputError message text with constants; add bounded owned
  diagnostic projection, never generic raw error extraction.
- [ ] Main focused/kernel regression and race3; scoped review; commit checkpoint.

## Task 2: Required client-boundary encoding contract and strategies

**Files:** new `internal/ingress/failure.go`, `failure_test.go`; modify ingress.go.
Main owns host/public aliases/contract and migration of external fixtures.

**Interfaces:** ClientFailureContext carries Failure, Diagnostics, OperationRef,
RequestID, Model, Status, Effect, Retry. ClientFailureResponse {ContentType,
Body string}. FailureEncoder uses the required RenderFailure signature;
JSONOperationIngress/MultipartOperationIngress delegate to configurable encoder
with stock JSON as natural default. AnthropicFailureEncoder is explicitly bound
for the built-in Messages ingress at composition time.

- [ ] Add context validity/ownership/encoded-bound tests and actual stock JSON/
  Anthropic envelope tests, plus custom encoder delegation; main observes RED.
- [ ] Implement typed context/body validation and stock encoders with stdlib
  JSON/mime. No ResponseWriter exposed to modules; no arbitrary headers/status.
- [ ] Main adds required ClientIngress method after test RED, updates API/provider
  aliases, RuntimeModuleHostContractVersion14 and exact module locks atomically.
  Manual custom ingress types gain explicit method; embedded helpers inherit it.
- [ ] Main focused tests/registry and independent module build; scoped review.

Tasks 1/2 may use disjoint workers after shared interface names are frozen;
main alone coordinates every Go invocation after writers stop.

## Task 3: Host selection, safe context and commitment fence

**Files:** focused `internal/api/client_failure.go`, tests; wire server.go and
existing API error/auth tests. Main owns API integration.

**Interfaces:** host helper renders through known codec and validates context/
response before writes. executeNormalized receives the selected codec, including
preparation/dispatch failures; authentication on operation routes uses it too.
The fixed /v1/models route retains its stock safe JSON authorization envelope.

- [ ] Preserve executed ingress-private-error RED36175. Add actual custom failure
  envelope tests for auth, decode, operation mismatch, schema/no-route, executor
  error, accepted ReplaySuppressedError effect, and already committed output.
- [ ] Add bad encoder error/oversize/media controls/context overflow tests that
  require HTTP500 empty body and no second render; main observes RED.
- [ ] Implement metadata-only host projection: safe code/message and owned
  diagnostics, codec operation ref, safe logged request ID, bounded model.
  Decode/auth/mismatch EffectNotDispatched; execution EffectUnknown unless
  ReplaySuppressedError provides effect; RetryNever is not a replay grant.
- [ ] Remove fixed operation error branches and ordinary raw-error echo; preserve
  status categories and trusted explicit safe carriers without provider switches.
- [ ] Main full root/external regression/vet/conformance and selected race3;
  scoped review/commit, no intermediate broken-contract installation.

## Task 4: Independent client dialect and installed acceptance

**Files:** `examples/external-provider-module/client_failure.go`, `_test.go`;
Makefile installed gate; operations/status/M15/audit documentation.

**Interfaces:** only public provider/daemon APIs, one deliberately different
failure schema registered in the existing catalog and validated independently.
Wire error encoding contributes through ingress, not kernel/HTTP format branches.

- [ ] Main first runs missing public-hook/module RED; worker implements a real
  temporary-daemon client dialect covering decode/input-schema/no eligible route,
  auth, and accepted-provider pre-first-byte failure with exact status/effect,
  private marker absence, one dispatch/no replay and correct request correlation.
- [ ] Assert rendered wire schema separately from safe context; reuse existing
  stream terminal/replay tests rather than widening into every protocol pair.
- [ ] Main full tests/vet/conformance, root/external race3; install-all and explicit
  installed CLI catalog/control acceptance without env-skips; preserve real setup.
- [ ] Final whole-feature review, clause-to-test update and regular commits.
  Close F1 narrowly, not C1–C6/provider breadth/RSS/p99 by implication.

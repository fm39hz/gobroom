# SG1 composition implementation plan

> **For agentic workers:** Main implements the coupled public fixture; a worker owns the Anthropic acceptance file. Only main formats, compiles, tests, installs and commits. Tests before any production correction.

**Goal:** Execute the missing tools/budget/scoped-signature intersection and
named-loss/renderer boundaries through independent modules and real daemons.

**Architecture:** Existing Gemini/Anthropic codecs, scope-bound cache and
catalog-configured transforms, ordinary Physical/Combo setup and public HTTP.

**Tech Stack:** Existing Go JSON/HTTP, schema catalog and daemon/provider APIs.

**Spec:** `docs/superpowers/specs/2026-10-24-sg1-composition-design.md`

Status: complete for this bounded public-module intersection. Implementation
checkpoint `5b4c219`; full root/module tests and vet passed after review fixes,
and again after the installed fixture correction. Focused root/module race3
passed; installed gate race3 passed in 17.878s after correcting the fixture's
unsupported usage flag. Install-all succeeded. Actual daemon healthy at matching
snapshot 517, bundle validation v9 valid, scoped config/credential hash unchanged.
Deferred coverage: facet-specific negative reasons and a dedicated read-only
preflight load-failure/zero-version/cancellation spy matrix. See the
[acceptance map](../../SG1_COMPOSITION_ACCEPTANCE.md); broader M15 remains open.

## Global Constraints

- No preseeded signature proof, synthetic kernel execution, new dependency,
  fabricated wire protocol or relaxed signature/issuer checks.
- Budget 2048; Anthropic max-output adjustment 512→3072 requires named loss
  `anthropic.reasoning-budget-raises-max-output` plus model and ceiling grants.
- Stable configured prompt; declared transform effects; exact public refs.
- Main only Go and install; temporary databases/upstreams; preserve real setup.

## Review Focus

1. Hidden prompt changes can alter signed history; match the transformed prefix.
2. Client-echoed signatures are not proof outside the exact session/branch/call.
3. Incompatible earlier routes must not receive tools/budget requests.
4. Named loss must appear on actual wire and selected-attempt usage, not only explain.
5. A semantic mutation cannot preserve provider-signed Anthropic thinking by assertion.

## Task 1: Gemini public intersection

Executed implementation deltas: Gemini wire reasoning binding, common numeric
normalization for JSON-number IPC, and shared read-only candidate session
restoration in `internal/kernel/candidate_session.go`, used by execute/explain.
Each has an observed regression before correction; no provider-name branch.

Files: `examples/external-provider-module/sg1_fixture_test.go`, `sg1_gemini_test.go`.
Interfaces: new `newSG1Host`, `sg1ConfigureModel`, `sg1PublishRole`,
`sg1PutPrompt`, `sg1PutResponse`; existing public daemon/provider helpers.

- [x] Register a schema-bound stable prompt transform; ordinary public setup.
- [x] Real signed tool response then same-session follow-up with tools and
  budget2048, active request/response transforms; assert wire and client text.
- [x] Wrong session/branch/target and unsupported response-ID continuity reject
  before dispatch; incompatible first fallback member receives no calls.
- [x] Signed preflight agrees with execution for the correct session and
  rejects invalid scopes without dispatch or private state disclosure.
- [x] Main executes tests; actual defects get RED→GREEN; commit verified checkpoint.

## Task 2: Anthropic loss and signed renderer

Files: `examples/external-provider-module/sg1_anthropic_test.go` (worker only).
Consumes shared Task1 fixture interfaces; no writes to other files.

- [x] Tools/budget/max-output denial then explicit named-loss grant, actual wire
  and bounded usage evidence through ordinary controls.
- [x] Anthropic signed-thinking/tool output preserves issuer semantics with
  request transform; active semantic response mutation rejects before dispatch.
- [x] Worker stops for main execution, no Go or production edits. Main full
  root/module tests/vet, focused race3, fresh whole-feature review and fixes.

## Task 3: Installed acceptance and documentation

Files: Makefile, SG1 acceptance map, audit/M15/product status.

- [x] Dedicated installed SG1 gate requires check-installed-cli and race/count3;
  installed status/catalog/usage address temporary IPC only.
- [x] make install-all, explicit installed gate, actual status/hash read-only checks.
- [x] Record exactly which SG1 intersection is proven and remaining provider/
  expiry/reset/cross-codec breadth; commit without declaring all M15 complete.

# Behavior comparison and compatibility evidence

This is a behavior-by-behavior comparison, not a promise of drop-in parity.
The 9router column is based on source checked in the adjacent local checkout at
`/home/fm39hz/Workspace/Personal/Tools/AI/9router`; source paths below are
orientation, not a substitute for executable fixtures. GoBroom status reflects
the current repository and tests inspected on 2026-10-06. The roadmap is the
status source of truth; this matrix summarizes observable behavior and keeps
partial protocol semantics visible. The architecture success criterion is
defined separately in [`SOLUTION_ARCHITECTURE.md`](SOLUTION_ARCHITECTURE.md).

## Status legend

- **Implemented** — GoBroom has the stated behavior and focused tests.
- **Partial** — a useful slice exists; limits below remain.
- **Planned** — not complete.
- **Out of core** — intentionally belongs outside the daemon kernel.
- **Needs verification** — source behavior or edge semantics need fixtures; do
  not claim compatibility yet.

## Architecture catch-up criterion

Feature parity is not the same as architecture readiness. Architecture is
ready when implementing a missing 9router behavior only adds configuration or
a reusable/provider-specific module behind stable contracts; it does not
require a new kernel model category, schema concept, request lifecycle or
provider branch. `make test-conformance` verifies the current M15 composition
subset. Full semantic architecture closure is still pending SG1–SG7 in the
[conformance evidence](M15_CONFORMANCE.md); its decisions are fixed in
[Semantic extension contracts](EXTENSION_CONTRACTS.md). Provider/protocol
completeness and drop-in parity remain separate claims.

## Model and route configuration

| Behavior in 9router | GoBroom contract/status | Notes |
|---|---|---|
| Provider prefixes and provider aliases (`open-sse/services/model.js`, provider registry) | Partial | Prefix registry, provider definitions and collision handling exist; edge-case compatibility fixtures are incomplete. |
| Opaque `provider/model` names and model-ID rewriting | Partial | Typed model references and route mappings exist; verify first-slash, nested slash and marker behavior against source. |
| Models discovered from provider `/models` plus custom models | Partial | Definition-bound discovery, exact-connection test/review, subset import by exact upstream IDs, entitlements-only apply, completeness-aware per-connection availability and custom entries exist. Inference dry-run for operations without `/models` and connection-target editing for custom IDs remain. |
| Equivalent routes grouped into Physical models and composed into role Combos | Partial (Physical curation UX incomplete) | Typed persistence, IPC/CLI and separate Discovered/Physical/Combos operations exist. Physical identity retains route fidelity/evidence; slash-qualified picker search is regression-tested to match Physical source prefixes while rendering one canonical row. Combo editing has separate ordered-member/candidate panes, a strategy catalog, typed member weights and order-preserving K/J movement; a TUI-key-event → IPC → SQLite/snapshot → `/v1/models` fixture verifies save and exposure. Suggested equivalence grouping, bulk split/merge and its guided TUI workflow remain. |
| Only selected models exposed to clients | Implemented | Exposure is the `discoverable` property of routable Physical/Combo models; no separate publication object. `/v1/models` projects that graph. |
| Connection pools per provider/model | Partial | Discovered routes expand only over connections with positive model-list evidence; complete snapshots revoke previously known absent IDs, incomplete ones never do. Custom IDs carry explicit per-connection assignments and expand only to assigned, enabled accounts; they never inherit across the provider node. Broader real-provider completeness fixtures remain. |

## Routing and fallback

| Behavior in 9router | GoBroom contract/status | Notes |
|---|---|---|
| Combo order/fallback (`open-sse/services/combo.js`) | Implemented (core policies; management partial) | Typed execution preserves nested policy boundaries, fallback/rotation/weighted strategies, typed edge weights, retry and response-commit rules. TUI edits ordered members/weights, selects from the strategy catalog and validates JSON-bound options; a TUI-key-event → IPC → store/snapshot → `/v1/models` fixture verifies Combo exposure. Guided provider/Physical onboarding remains incomplete. Fusion panel/judge is intentionally outside the lightweight kernel. |
| Round-robin and sticky limits | Implemented (core policies) | Strategy state and concurrent scheduler behavior have tests; exact parity for all 9router precedence combinations remains unverified. |
| Account selection, exclusion and preferred account (`open-sse/services/auth.js`, `accountFallback.js`) | Partial | Connection affinity, health/quota gating and preferred-connection paths exist; provider-specific exclusions and precedence need fixtures. |
| Cooldown and `Retry-After` | Implemented (common evidence path; provider coverage partial) | Rich classifier outcomes now survive runtime registry wiring. Common Retry-After delta/date and rate headers affect health; provider-specific classifier precedence still needs fixture coverage. |
| Quota affects candidate eligibility | Implemented (common + manifest-configurable evidence; provider coverage partial) | Structured quota/reset evidence produces zero-remaining windows; manifests bind JSON pointers, quota signals, scope and named windows without kernel changes. A daemon restart fixture proves quota and rate windows persist and gate the route; usage-report operations and broader provider fixtures remain. |
| No switch after response bytes are sent | Implemented (kernel boundary) | Response commitment prevents retry/switch after output begins; broaden end-to-end coverage across adapters. |

## Protocol normalization and streaming

| Behavior in 9router | GoBroom contract/status | Notes |
|---|---|---|
| Shared request normalization and provider dispatch | Partial | GoBroom has operation/client-wire separation, provider response decoders, semantic events, registered renderers and request/response transform seams. The M15 composition subset passes; full semantic closure and protocol event-family parity remain incomplete. |
| OpenAI Chat endpoint | Implemented (core; provider matrix partial) | Streaming/JSON, cancellation, usage, tool events and retry boundary have focused tests; more upstream-specific fixtures are needed. |
| OpenAI Responses endpoint/continuity | Implemented (core; edge compatibility partial) | Responses adapter and client-session continuity are wired; complete item/tool/error compatibility still needs fixtures. |
| Anthropic Messages | Partial | Text/tool/thinking paths exist; broad content-block, cache, stop-reason and usage parity needs fixtures. |
| Gemini upstream through OpenAI Chat | Partial (typed translation path; kernel integration fixtures) | Typed Chat→Gemini request and Gemini→OpenAI JSON/SSE contracts cover messages/system instructions, function declarations/history, supported generation options/reasoning, base64 images, finish reasons and usage. Unrepresentable options are rejected. Kernel-level binding tests cover tool/signature streaming, non-stream text, cancellation, pre-commit 429/401 fallback, terminal 400 behavior and no fallback after committed truncated output; malformed streams and missing candidates are rejected. Provider-specific error-body/quota parsing, native Gemini/Antigravity requests and complete multimodal/tool-signature conformance are not claimed. |
| Cross-protocol canonical events | Implemented (core) | Kernel event contract and OpenAI/Anthropic/Gemini adapter observations exist; semantic parity across every event family remains incomplete. |
| Tool calls split across SSE chunks | Partial | OpenAI and Gemini tool deltas are translated; full interleaving/partial-argument/signature fixtures remain. |
| Multimodal messages and capability routing | Partial | Typed capability and eligibility policies exist. Gemini accepts base64 image data and explicitly rejects unsupported content instead of silently dropping it; modality coverage remains codec-specific. |
| Native passthrough and format inference | Partial | Inbound format detection and protocol matching exist; lossless passthrough is not general and must be proven per format/provider. |

## Credentials, quota and observability

| Behavior in 9router | GoBroom contract/status | Notes |
|---|---|---|
| Static API-key/Bearer credentials | Implemented (core) | Credentials resolve per connection through daemon/provider bindings; provider-specific auth schemes may need another reusable auth primitive. |
| OAuth lifecycle, proactive refresh and refresh-on-401 | Partial | Definition-bound refresh, per-connection locking and persistence exist. Interactive authorization-code/PKCE onboarding and live provider validation remain. |
| Per-connection proxy settings | Planned | Keep in transport/connection configuration, not kernel routing branches. |
| Usage history and request detail | Implemented (compact core) | Bounded asynchronous usage events are persisted and exposed through control/CLI/TUI with request class/session/TTFT/throughput. Rich request-detail parity and cost accounting are not claimed. |
| Provider quota APIs and reset-aware policy | Partial (generic runtime ready) | Generic quota operations, common structured error evidence, typed manifest JSON paths/signals and reset-aware policy exist; usage-report operations and broader provider fixtures remain. Polling is opt-in/opportunistic. |
| Quota/usage only as dashboard data | Not the target | Runtime policy should consume the same state; current coverage is partial. |

## Secondary product features

| 9router area | GoBroom decision |
|---|---|
| Embeddings, image/video, TTS/STT, search/fetch | Out of core; add separate services only when needed |
| Fusion panel/judge orchestration | Out of core; keep parallel fan-out/judge as an optional service so the kernel remains a lightweight route/model daemon |
| Dashboard | Replaceable frontend, not daemon authority |
| MITM/DNS, IDE interception, tunnels | Out of core; sidecar/integration if ever required |
| Cloud sync and prompt-mutating optimizers | Not in current core scope |

## Evidence still required

Before calling GoBroom a drop-in replacement, add deterministic fixtures for:

- exact model parsing/alias precedence and model-ID rewrites;
- combo and account strategy order under concurrency;
- error-class precedence across provider, connection, combo and model scopes;
- OAuth refresh races and one-retry behavior;
- Responses continuity, Anthropic content blocks, thinking and tool streams;
- cancellation, disconnect and errors before/after first response byte;
- quota window/reset parsing and its effect on route selection;
- every built-in manifest operation and declared capability.

Before calling the architecture implementation-ready for feature catch-up,
also exercise one change from each category—new API-key provider, OAuth flow,
wire dialect, capability, quota/error envelope, strategy, transformation and
operation—without changing the kernel model graph or provider-specific core
storage. These are architecture conformance tests, not feature-parity claims.

See [implementation roadmap](IMPLEMENTATION_PLAN.md) for sequencing and the
[normalization contract](NORMALIZATION.md) for protocol boundaries.

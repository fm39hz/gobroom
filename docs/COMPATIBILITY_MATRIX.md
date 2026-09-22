# Behavior comparison and compatibility evidence

This is a behavior-by-behavior comparison, not a promise of drop-in parity.
The 9router column is based on source checked in the adjacent local checkout at
`/home/fm39hz/Workspace/Personal/Tools/AI/9router`; source paths below are
orientation, not a substitute for executable fixtures. GoBroom status reflects
the current repository and tests inspected on 2026-09-22.

## Status legend

- **Implemented** — GoBroom has the stated behavior and focused tests.
- **Partial** — a useful slice exists; limits below remain.
- **Planned** — not complete.
- **Out of core** — intentionally belongs outside the daemon kernel.
- **Needs verification** — source behavior or edge semantics need fixtures; do
  not claim compatibility yet.

## Model and route configuration

| Behavior in 9router | GoBroom contract/status | Notes |
|---|---|---|
| Provider prefixes and provider aliases (`open-sse/services/model.js`, provider registry) | Partial | Prefix registry, provider definitions and collision handling exist; edge-case compatibility fixtures are incomplete. |
| Opaque `provider/model` names and model-ID rewriting | Partial | Typed model references and route mappings exist; verify first-slash, nested slash and marker behavior against source. |
| Models discovered from provider `/models` plus custom models | Partial | OpenAI-style discovery and custom catalog entries exist; UI import/review workflow is incomplete. |
| Model aliases, physical abstractions and role combos | Partial | Additive typed persistence, IPC/CLI and separate Discovered/Physical/Combos TUI tabs exist. Automatic legacy-to-typed migration and full editor polish remain. |
| Only selected models exposed to clients | Implemented | Explicit public-model projection drives `/v1/models`. |
| Connection pools per provider/model | Partial | Route expansion per connection and multiple selection strategies exist; full policy parity needs fixtures. |

## Routing and fallback

| Behavior in 9router | GoBroom contract/status | Notes |
|---|---|---|
| Combo order/fallback (`open-sse/services/combo.js`) | Partial | The execution path traverses typed nodes and preserves inner/outer strategy boundaries with cycle guards. Sticky semantics, fusion and exact retry precedence still need broader fixtures. |
| Round-robin and sticky limits | Partial | Round-robin scheduler exists; sticky-limit equivalence and concurrent ordering are not yet certified. |
| Account selection, exclusion and preferred account (`src/sse/services/auth.js`, `accountFallback.js`) | Partial | Fill-first, rotation, preferred connection and health cooldown paths exist; policy precedence remains. |
| Cooldown and `Retry-After` | Partial; redesign required | Basic cooldown plumbing exists. Target behavior uses real-attempt outcomes, scoped breakers, exact deadline provenance and half-open real trials rather than synthetic checks. |
| Quota affects candidate eligibility | Partial; redesign required | Persisted snapshots gate routes, but the target is passive limit extraction plus opportunistic enrichment; daemon-wide periodic polling is not the default contract. |
| No switch after response bytes are sent | Partial | HTTP tracks response commitment and kernel execution returns after stream dispatch; expand integration tests across adapters. |

## Protocol normalization and streaming

| Behavior in 9router | GoBroom contract/status | Notes |
|---|---|---|
| Shared `handleChatCore` normalization and provider dispatch | Partial | Typed inbound normalization, kernel and adapters exist; common semantic coverage is not complete. |
| OpenAI Chat endpoint | Partial | Adapter and live calls exist; broaden SSE, cancellation, tool and failure-boundary conformance. |
| OpenAI Responses endpoint/continuity | Partial | Adapter route exists; continuity and output-item compatibility need fixtures. |
| Anthropic Messages | Partial | Basic adapter and text/tool SSE conversion exist; content blocks, thinking, stop reasons and usage parity are incomplete. |
| Cross-protocol canonical events | Planned | Shared normalized response-event layer is M5. |
| Tool calls split across SSE chunks | Needs verification | Add fixture suite before claiming compatibility. |
| Multimodal messages and capability routing | Partial | Typed capability fields and route filtering exist; broad detection/degradation behavior is missing. |
| Native passthrough and format inference | Needs verification | Define and test when passthrough is lossless and how endpoint/header/body evidence is prioritized. |

## Credentials, quota and observability

| Behavior in 9router | GoBroom contract/status | Notes |
|---|---|---|
| Static API-key/Bearer credentials | Partial | Credentials resolve per connection through daemon control/runtime path; more auth aliases and redaction tests required. |
| OAuth lifecycle, proactive refresh and refresh-on-401 | Partial | OAuth library is selected; generic auth registry exists, but a complete end-to-end provider flow is not implemented. |
| Per-connection proxy settings | Planned | Keep in transport/connection configuration, not kernel routing branches. |
| Usage history and request detail | Partial | Compact events are durably stored and exposed through `usage.list`, CLI and TUI, including class/session/TTFT fields; aggregates, cost, retention and bounded diagnostics remain. |
| Provider quota APIs and reset-aware policy | Partial; redesign required | Generic HTTP/JSON polling exists as bootstrap code. The target uses passive response evidence and opportunistic provider enrichment with exact reset provenance; scheduled polling is explicit opt-in. |
| Quota/usage only as dashboard data | Not the target | Runtime policy should consume the same state; current coverage is partial. |

## Secondary product features

| 9router area | GoBroom decision |
|---|---|
| Embeddings, image/video, TTS/STT, search/fetch | Out of core; add separate services only when needed |
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

See [implementation roadmap](IMPLEMENTATION_PLAN.md) for sequencing and the
[normalization contract](NORMALIZATION.md) for protocol boundaries.

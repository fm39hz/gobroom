# GoBroom implementation plan

Status: planning only — no implementation work is authorized by this document.

## 0. Objective

Build a daemon-first replacement for the useful routing and normalization core
of 9router while preserving its provider breadth and compatibility behavior.

GoBroom must expose ordinary OpenAI-compatible endpoints:

```text
/v1/models
/v1/chat/completions
/v1/responses
/v1/messages
```

The daemon owns routing state, model normalization, provider execution,
quota/usage policy and SQLite persistence. CLI/TUI and any future dashboard are
control clients, not alternate routing implementations.

The target is not a minimal proxy. It is a feature-complete routing daemon with
a smaller and stricter runtime core.

## 1. 1:1 architecture mapping

| 9router subsystem | Current role | GoBroom target | Deliberate change |
|---|---|---|---|
| Next.js API routes | data plane + control plane mixed together | `internal/api` data/control surfaces | keep surfaces, separate ownership |
| `handleChat` | auth, combo entry and account fallback entry | gateway middleware + kernel entry | no provider logic in HTTP handler |
| `handleChatCore` | normalization, translation, execution and lifecycle | `normalize` + `kernel` + `adapter` pipeline | preserve behavior, split phases |
| provider registry | provider metadata, models, quirks, auth | provider presets + adapter registry | metadata and behavior separated |
| provider executors | upstream request execution | protocol/provider adapters | common execution contract |
| model parser | prefix/model string parsing | prefix registry + typed references | keep wire compatibility, remove stringly internals |
| model aliases | alias → provider/model | logical model → typed target | no ambiguous KV direction |
| provider nodes | custom endpoint + user prefix | `ProviderNode` | unique prefix validation |
| custom models | user-defined model IDs/capabilities | first-class catalog entries | discovery never overwrites custom entries |
| combos | ordered string list | typed combo graph | cycle detection, explicit strategy |
| `/v1/models` combo output | all combos/models mixed together | `published_models` projection | expose only selected public models |
| account selection | DB reads + process-wide mutex | in-memory scheduler + scoped locks | no global request serialization |
| account fallback | retry another account/provider | kernel route candidate scheduler | one unified fallback policy |
| token refresh | provider-specific refresh functions | credential manager + adapter refresh hooks | refresh outside global routing lock |
| model/account locks | fields in connection JSON | runtime health/quota state | durable state only where needed |
| `accountFallback` | status/error heuristic | adapter error classification + policy | typed error classes |
| capability detection | request scan + model metadata | normalization modality scan + capability gate | capability affects route selection |
| request translators | format-specific request conversion | semantic IR translators | direct path or canonical IR pivot |
| response translators | JSON/SSE conversion | normalized event stream encoders | explicit stream state machine |
| thinking concerns | capture/apply provider-native thinking | `ThinkingIntent` | semantic intent independent of wire format |
| tool concerns | repair IDs, tool result reconciliation, cloaking | tool normalization + adapter quirks | preserve mappings outside raw body |
| session manager | client/provider session metadata | `SessionContext` | not hidden in credential objects |
| continuity fields | Responses round-trip state | `ContinuityState` | never leak internal fields upstream |
| image prefetch/modality | content mutation and remote fetch | capability policy + cancellable media stage | explicit strip/reject/fallback policy |
| RTK/Headroom/PXPIPE | request mutation/token saving | optional post-normalization middleware | cannot change routing identity silently |
| usage DB | history, stats and dashboard feed | event bus + usage worker + aggregates | usage drives policy, not just UI |
| quota cache/auto-ping | provider quota visibility and cooldown | quota service + scheduler gate | quota is routing state |
| request details | raw payload observability | bounded opt-in diagnostic sink | never block stream or grow unbounded |
| dashboard | configuration and monitoring | CLI/TUI/control API | UI is a consumer of daemon contract |
| OAuth UI flows | browser onboarding | daemon auth plugins + CLI browser handoff | no routing logic in UI |
| tunnel/Tailscale | external access | separate optional service | outside kernel |
| MITM/DNS/IDE | IDE integration | sidecar/plugin boundary | outside daemon core |
| cloud sync | state replication | optional control-plane backend | never in hot path |

## 2. Target runtime architecture

```text
                         Clients
                            │
                 ┌──────────┴──────────┐
                 │                     │
          Data plane              Control plane
       OpenAI/Claude API        CLI/TUI/local API
                 │                     │
                 └──────────┬──────────┘
                            ▼
                      gobroomd
 ┌──────────────────────────────────────────────────────────────┐
 │ HTTP boundary                                               │
 │ auth → request body → normalized IR → kernel                │
 │                                                              │
 │ Route kernel                                                 │
 │ public model → snapshot → gates → scheduler → adapter       │
 │                                                              │
 │ Protocol layer                                               │
 │ OpenAI Chat / Responses / Anthropic / Gemini / custom       │
 │                                                              │
 │ Runtime services                                             │
 │ credentials · refresh · quota · usage · health · events     │
 └───────────────┬──────────────────────────────────────────────┘
                 │
       ┌─────────┴─────────┐
       │                   │
 SQLite control plane   upstream providers
```

### Control plane

Owns:

```text
provider nodes
connections
model catalog
custom models
logical models
combos
published models
settings
adapter configuration
```

It validates mutations, persists them transactionally and publishes a new
immutable route snapshot.

### Data plane

Owns:

```text
request normalization
public model resolution
capability/quota/health gates
route scheduling
provider translation
upstream execution
SSE/JSON response encoding
```

The common path must not query SQLite for static configuration.

### Event plane

Consumes compact events from the data plane:

```text
usage event
quota observation
route success/failure
stream lifecycle
credential refresh
```

Consumers update SQLite aggregates, quota state, route health and control API
views asynchronously.

## 3. Milestones

### M0 — Contract freeze and compatibility inventory

Purpose: prevent implementation from drifting into a generic proxy.

Deliverables:

- typed kernel contract;
- normalized semantic IR;
- provider adapter contract;
- route snapshot contract;
- public-model contract;
- quota/usage event contract;
- error taxonomy;
- compatibility matrix for all 9router formats/providers currently used.

9router coverage:

```text
handleChatCore inputs/outputs
model parser and prefix behavior
combo strategy behavior
provider transport selection
stream fallback boundary
thinking/tool/session/continuity semantics
```

Exit criteria:

- every retained 9router behavior maps to a typed contract;
- every intentionally removed behavior is documented;
- no handler is allowed to invent its own model resolution path.

### M1 — SQLite control plane and snapshot builder

Purpose: make configuration durable and data-plane-safe.

Deliverables:

- migrations for nodes, connections, catalog, custom models, aliases,
  logical models, combos, combo members, published models, settings;
- provider/node/model/combo CRUD repositories;
- import/export from the existing 9router database;
- prefix uniqueness validation;
- combo cycle validation;
- snapshot builder and atomic publication;
- retention/maintenance policy for usage and quota data.

9router mapping:

```text
localDb
providerNodes
aliasRepo
connectionsRepo
combosRepo
settingsRepo
```

Exit criteria:

- a 9router configuration can be imported without credentials being logged;
- `tech-lead` can resolve through nested helper combos;
- only explicitly published models appear in `/v1/models`;
- invalid prefixes/cycles are rejected transactionally.

### M2 — Public model and prefix compatibility

Purpose: preserve the external model syntax clients already use.

Deliverables:

- built-in provider aliases;
- custom provider prefixes;
- canonical provider/node IDs;
- first-slash parsing for opaque model IDs;
- logical aliases;
- custom model registration;
- public model publishing;
- explicit compatibility mode for bare model inference.

9router mapping:

```text
provider registry aliases
custom provider-node prefix
parseModel
resolveModelAliasFromMap
customModels
v1/models
```

Exit criteria:

- `g4f/srv_xxx:provider/model` round-trips without losing slashes or colons;
- built-in aliases cannot be shadowed by custom prefixes;
- duplicate custom prefixes are rejected;
- internal combos are hidden unless published.

### M3 — Normalization IR and request invariants

Purpose: reproduce the strongest part of `handleChatCore` before adding
provider breadth.

Deliverables:

- endpoint/body/header format detection;
- OpenAI Chat normalization;
- Responses normalization;
- Anthropic normalization;
- Gemini content normalization;
- tool-call ID repair;
- tool-result reconciliation;
- thinking intent capture;
- session and continuity capture;
- modality detection;
- extension preservation;
- cancellation-aware media preprocessing.

9router mapping:

```text
detectFormat
detectFormatByEndpoint
ensureToolCallIds
fixMissingToolResponses
captureThinking/applyThinking
captureSessionId
stripContinuityFields
stripUnsupportedModalities
prefetchRemoteImages
```

Exit criteria:

- equivalent OpenAI/Responses/Anthropic requests produce equivalent IR;
- provider-specific fields do not leak into the wrong protocol;
- missing tool-call IDs are repaired deterministically;
- normalization never silently deletes content without policy output.

### M4 — OpenAI-compatible data plane

Purpose: make GoBroom useful with generic providers first.

Deliverables:

- `/v1/models` public projection;
- `/v1/chat/completions`;
- OpenAI-compatible Chat adapter;
- passthrough SSE;
- JSON response handling;
- route selection and connection selection;
- fallback before first response byte;
- request cancellation;
- upstream timeout policy.

9router mapping:

```text
default executor
openai-compatible executor
streamController
handleStreamingResponse
handleNonStreamingResponse
```

Exit criteria:

- a published combo can route to a real OpenAI-compatible endpoint;
- SSE survives long-lived streams and client disconnects;
- fallback never happens after client bytes are emitted;
- route/account failures update runtime health.

### M5 — Canonical translation and response events

Purpose: implement the protocol-independent normalization/translation core.

Deliverables:

- direct translator registry;
- canonical IR translator path;
- normalized response event model;
- event-to-OpenAI Chat encoder;
- tool-call streaming state machine;
- usage extraction;
- finish reason normalization;
- provider error normalization.

9router mapping:

```text
translator/index.js
request translators
response translators
stream initState
streamHandler
```

Exit criteria:

- one provider stream can be emitted as OpenAI Chat;
- tool calls remain valid across chunk boundaries;
- reasoning/thinking events do not corrupt text events;
- usage arrives as a terminal event or explicit incomplete event.

### M6 — OpenAI Responses and Anthropic Messages

Purpose: support the two major non-Chat wire protocols.

Deliverables:

- `/v1/responses`;
- `/v1/messages`;
- Responses input/output item mapping;
- encrypted reasoning continuity handling;
- Anthropic content blocks;
- Claude tool schema policy;
- direct and pivot translation paths.

9router mapping:

```text
openai-responses translators
claude translators
thinkingUnified
toolCall concerns
Claude cache/continuity logic
```

Exit criteria:

- multi-turn Responses sessions preserve continuity;
- Claude tool use/result cycles remain valid;
- the same upstream route can serve Chat, Responses or Messages according to
  its transport matrix.

### M7 — Provider presets, discovery and credentials

Purpose: match 9router's provider breadth without contaminating the kernel.

Deliverables:

- generic OpenAI Chat preset;
- generic OpenAI Responses preset;
- generic Anthropic preset;
- `/models` discovery;
- custom model overrides;
- provider capability metadata;
- credential pool;
- API-key storage abstraction;
- OAuth/token refresh adapter interface;
- provider-specific refresh implementations by usage priority.

9router mapping:

```text
providers/registry
modelsFetcher
providerCustomModels
tokenRefresh
backgroundTokenRefresh
getProviderCredentials
```

Exit criteria:

- discovery failures do not erase custom models;
- credentials never appear in model/catalog responses;
- refresh is deduplicated per connection, not globally serialized;
- one provider adapter can have multiple connections/routes.

### M8 — Quota, usage, cost and health as routing services

Purpose: make operational state influence decisions, not merely dashboards.

Deliverables:

- quota snapshot repository;
- provider quota adapters;
- response-header/error-body quota observations;
- reset-aware route gate;
- usage event worker;
- daily/hourly aggregates;
- cost calculation;
- budget policy;
- route health score;
- usage/quota control API.

9router mapping:

```text
antigravity quota cache
accountFallback locks
usageRepo
usageDaily
pricing
quotaAutoPing
```

Exit criteria:

- exhausted routes are skipped until reset;
- usage persistence never blocks first byte or stream completion;
- cost/budget rules can reject or reroute requests;
- TUI and scheduler consume the same quota/usage state.

### M9 — Advanced request middleware

Purpose: preserve useful optimization features without mixing them into
semantic normalization.

Deliverables:

- optional tool-result compression;
- optional external compression proxy;
- image context compression;
- system prompt policies;
- cache anchoring;
- per-request opt-out;
- explicit transformation diagnostics.

9router mapping:

```text
RTK
Headroom
PXPIPE
Caveman
Ponytail
anchorClaudeCache
```

Exit criteria:

- middleware runs after IR/translation policy is established;
- every mutation is measurable and attributable;
- middleware can be disabled per request;
- no middleware bypasses cancellation or size limits.

### M10 — CLI/TUI control plane

Purpose: make the daemon usable without the 9router dashboard.

Deliverables:

- provider node management;
- connection management;
- model discovery and custom models;
- alias/logical model editor;
- combo graph editor;
- public-model publishing;
- route test;
- quota/usage/health views;
- daemon reload/status/logs.

9router mapping:

```text
dashboard providers
dashboard models
dashboard combos
usage stats
request logs
OAuth modals
```

Exit criteria:

- every control-plane action is available through typed local API;
- TUI never reads SQLite directly while daemon is running;
- config mutation produces a new snapshot without restarting daemon.

### M11 — Compatibility expansion

Purpose: add high-value provider/client ecosystems incrementally.

Provider order should be driven by actual usage and adapter reuse:

```text
OpenAI-compatible generic
Anthropic-compatible generic
OpenAI Responses
Gemini
Codex
OpenCode
Kiro
Cursor
Ollama
provider-specific OAuth adapters
```

Each provider must ship with:

- request fixtures;
- response fixtures;
- SSE fixtures;
- error classification tests;
- tool/thinking tests;
- quota/usage behavior;
- cancellation test.

### M12 — Optional integrations outside the kernel

Only after the daemon core is stable:

```text
cloud sync
tunnel/Tailscale
MITM sidecar
DNS integration
IDE credential spoofing
MCP bridge
```

These must communicate with the daemon through explicit APIs or plugin
contracts. They must not add provider logic to the kernel.

## 4. Test strategy

### Contract tests

Every provider adapter must pass common tests for:

```text
prepare
auth headers
model mapping
stream decode
tool calls
thinking
usage
error classes
cancel
```

### Golden normalization tests

Store fixtures for:

```text
OpenAI Chat
OpenAI Responses
Anthropic Messages
Gemini
Claude Code
Codex CLI
```

Each fixture is tested as:

```text
wire input → IR → target wire format → expected semantic equivalence
```

### Route tests

Cover:

```text
public model only
hidden internal combo
nested combo
cycle
duplicate route
prefix collision
quota exhausted
route cooldown
round-robin
fallback before first byte
no fallback after first byte
```

### Load tests

Measure separately:

```text
normalization CPU
translation CPU
SSE memory per stream
SQLite write queue behavior
quota refresh pressure
concurrent route selection
```

## 5. Data migration strategy

Import from 9router in this order:

```text
provider nodes
connections (secrets redacted in export logs)
custom models
model aliases
combos
settings
selected public models
usage aggregates (optional)
```

9router's mixed combo/model `/v1/models` behavior must not be imported as
publication automatically. The migration tool should offer:

```text
publish all combos
publish selected combos
publish none
```

## 6. Definition of done

GoBroom can be considered a practical 9router replacement when:

1. M0–M8 are complete.
2. OpenAI Chat, Responses and Anthropic compatibility pass golden tests.
3. A real 9router configuration imports successfully.
4. Public model exposure is explicit and stable.
5. Combo/fallback/round-robin behavior matches expected outcomes.
6. Quota and usage influence routing and survive daemon restart.
7. Long-lived SSE streams do not block or grow unbounded memory.
8. TUI can manage the complete control plane.
9. IDE/tunnel integrations remain optional and isolated.

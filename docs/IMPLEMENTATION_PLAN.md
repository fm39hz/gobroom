# GoBroom implementation plan

Status: active implementation plan.

This plan is based on the actual 9router source under
`/home/fm39hz/Workspace/Personal/Tools/AI/9router`, not on an idealized
router architecture. GoBroom keeps useful runtime behavior while changing
ownership boundaries:

```text
9router behavior                  GoBroom boundary
----------------------------------------------------------------
handleChat + handleChatCore       HTTP boundary + kernel pipeline
localDb model/combos              SQLite control plane + snapshot
getProviderCredentials            credential manager + candidates
accountFallback                  scheduler + health/quota policy
provider executors               protocol/provider adapters
request translators              normalization + translators
stream handlers                  normalized response events
usage/requestDetails             bounded event worker + diagnostics
dashboard/CLI/MITM/tunnel         frontend or sidecar, outside kernel
```

Automatic 9router database import is deliberately not part of the plan.
Migration is manual and must use the control plane or a documented data format.

## 1. Target and non-target

### Target

Build a daemon-first local gateway that can replace the useful chat-routing
core of the current 9router setup:

```text
OpenAI/Anthropic client
  -> gobroomd data plane
  -> public model
  -> combo/logical model
  -> route + connection candidates
  -> provider adapter
  -> normalized response stream
```

The daemon must work without CLI/TUI. CLI and TUI are control-plane clients.

### Not part of the first replacement

These remain separate or later:

```text
MITM/DNS interception
tunnel/Tailscale/Cloudflare exposure
IDE credential spoofing
cloud sync
full dashboard replacement
prompt/token saver experiments
all 9router media endpoints
```

They are not allowed to add provider-specific branches to the kernel.

## 2. Current implementation baseline

Already present:

- daemon process with Unix IPC;
- optional loopback HTTP data plane;
- SQLite control plane;
- provider nodes and connections;
- discovered and custom model catalog;
- logical models and nested combo graph;
- explicit public-model projection;
- snapshot validation and cycle detection;
- route expansion per connection;
- runtime credential resolution by connection ID;
- OpenAI Chat adapter;
- OpenAI Responses passthrough adapter;
- basic Anthropic Messages adapter;
- basic Anthropic SSE text/tool conversion;
- health cooldown gate;
- persisted quota snapshots and basic quota gate;
- asynchronous usage event persistence;
- CLI/TUI status and reload;
- provider `/models` discovery;
- prefix collision validation.

The current implementation is not yet a drop-in replacement. The largest
gaps are response event semantics, model/prefix compatibility, credential
refresh, provider breadth, and provider-specific quota/usage behavior.

## 3. Delivery rules

### Rule A — vertical slices before breadth

Every major phase must end with a real path:

```text
manual config
  -> /v1/models
  -> client request
  -> route/account selection
  -> upstream
  -> response stream
  -> usage/health update
```

### Rule B — kernel contracts before provider expansion

Provider-specific behavior may not leak into HTTP handlers, combo resolution,
or scheduler code.

### Rule C — one source of truth per concern

```text
model graph       snapshot
credentials       credential manager
health/quota      runtime policy
translation       adapter/event layer
usage             event worker
configuration     SQLite control plane
```

### Rule D — no automatic importer

The existing 9router configuration is migrated manually. GoBroom may provide
validation, export, and control-plane commands, but not a magic importer that
silently guesses semantics.

## 4. Milestones

### M0.1 — Source-grounded compatibility inventory

Freeze what 9router actually does before adding more abstractions.

Deliverables:

- fixtures for model parsing and aliases;
- combo fallback and round-robin fixtures;
- account selection and exclusion fixtures;
- tool-call repair fixtures;
- thinking and Responses continuity fixtures;
- Anthropic content-block fixtures;
- stream termination and usage fixtures;
- provider error classification fixtures;
- compatibility matrix labelled preserve, simplify, or out-of-kernel.

Exit criteria:

- every GoBroom contract maps to real source behavior or an explicit design
  decision;
- no design document describes intended behavior as existing 9router behavior;
- fixtures run without real provider credentials.

### M0.2 — Model, prefix and alias contract

Make external model syntax compatible before provider expansion.

Deliverables:

- first-slash parser for opaque model IDs;
- built-in provider aliases;
- custom prefix registry;
- logical alias resolution;
- upstream model ID mapping;
- optional bare-model compatibility mode;
- model-marker stripping where required;
- collision and round-trip tests.

Required cases:

```text
provider/model
alias/model
model/id/with/slashes
custom provider prefixes
bare model inference
logical alias -> combo
```

### M1 — SQLite control plane and immutable snapshot

Make configuration durable without putting SQLite on the hot path.

Deliverables:

- provider node and connection CRUD;
- discovered/custom catalog CRUD;
- logical model, combo, and public-model CRUD;
- transactional mutation validation;
- route expansion per connection;
- atomic snapshot reload;
- configuration versioning;
- usage/quota retention hooks.

Constraints:

- snapshots contain connection IDs, never secrets;
- route candidates are immutable per request;
- failed reloads do not replace the active snapshot;
- IPC/API/TUI use the same validation path.

### M2 — TUI/control-plane vertical slice

Use TUI as a real control-plane client and integration-test surface.

Deliverables:

- provider/node editor;
- masked connection editor;
- model discovery and custom-model editor;
- logical-model editor;
- ordered combo editor;
- public-model publishing;
- route resolution/test command;
- daemon reload/status/health view.

The TUI must not read SQLite directly. Every operation goes through IPC.

Exit criteria:

- the current manual configuration can be recreated without SQL;
- editing a combo publishes a new snapshot;
- `/v1/models` changes only after valid control-plane mutation;
- daemon remains usable with TUI stopped.

### M3 — OpenAI Chat complete vertical slice

Make one end-to-end path reliable before widening protocol coverage.

Deliverables:

- OpenAI Chat validation;
- generic OpenAI-compatible adapter;
- API-key credential resolution;
- route/account scheduler;
- fallback before first byte;
- timeout and cancellation policy;
- SSE passthrough;
- JSON response path;
- response commit tracking;
- compact usage event;
- health feedback.

Tests must cover one-route success, pre-header fallback, all-route failure,
post-first-byte failure, client disconnect, and usage/health completion.

### M4 — Account pool and fallback policy

Reproduce the useful behavior of 9router's account selection without a global
selection mutex.

Deliverables:

- per-route/per-connection candidates;
- fill-first selection;
- round-robin selection;
- sticky limit;
- preferred connection;
- per-request exclusion;
- account health/cooldown;
- account-level versus model-level failure scope;
- Retry-After handling;
- typed policy for terminal, retry, next-account, next-model, cooldown, and
  auth-refresh outcomes.

### M5 — Canonical response event layer

Prevent every adapter from inventing its own streaming state machine.

Canonical events:

```text
ResponseStart
TextDelta
ReasoningDelta
ToolCallStart
ToolCallDelta
ToolCallEnd
Usage
ResponseEnd
ResponseError
```

Deliverables:

- provider SSE decoders;
- event state machine;
- OpenAI Chat encoder;
- stream completion/error contract;
- tool-argument buffering;
- finish-reason normalization;
- usage aggregation;
- cancellation propagation.

### M6 — Responses and Anthropic semantic compatibility

Cover the two non-Chat formats already exposed by GoBroom.

Deliverables:

- Responses input normalization;
- Responses output-item encoding;
- response ID and previous-response continuity;
- Anthropic content blocks;
- Claude tool-schema policy;
- thinking/reasoning mapping;
- Chat/Responses/Anthropic conversions;
- usage and stop-reason mapping.

Tests must include multi-turn Responses, text plus tool call, tool result,
thinking-heavy output, stream/non-stream variants, and malformed events.

### M7 — Runtime credentials and token refresh

Add the credential lifecycle that 9router actually uses.

Deliverables:

- credential manager interface;
- expiry detection;
- per-connection refresh lock;
- proactive refresh;
- 401/403 refresh-once retry;
- rotating refresh-token persistence;
- credential update event;
- one end-to-end OAuth provider adapter;
- browser/CLI handoff contract.

Security constraints:

- secrets remain outside snapshots and normal logs;
- refresh never holds scheduler locks;
- refresh failure cannot corrupt the last good credential;
- persistence is atomic per connection.

### M8 — Usage, cost, quota and health services

Split this into independently testable parts.

#### M8a — Usage and aggregates

- bounded usage worker;
- daily/hourly aggregates;
- reasoning/cache token categories where available;
- cost calculator;
- retention/pruning;
- control API views.

#### M8b — Provider-neutral quota contract

- quota source and timestamp;
- used/limit/remaining/reset;
- account/model/window scope;
- authoritative versus estimated source;
- reset-aware gate.

#### M8c — Provider quota adapters

- response-header observation;
- error-body observation;
- provider usage endpoints;
- reset parsing;
- refresh throttling.

#### M8d — Routing policy

- exhausted-route exclusion;
- remaining-budget preference;
- budget limits;
- cost-aware fallback;
- quota notifications.

### M9 — Provider presets and discovery expansion

Add breadth through reusable adapters, not kernel branches.

Order:

1. generic OpenAI Chat;
2. generic OpenAI Responses;
3. generic Anthropic;
4. generic Gemini;
5. provider-specific presets based on actual use;
6. provider-specific OAuth only where needed.

Each adapter owns URL/path, headers/auth, request transform, response decode,
error classification, usage extraction, and optional quota integration.

### M10 — Capability and modality policies

Match 9router's practical capability behavior.

Deliverables:

- vision/audio/video/PDF detection;
- hard-capability rejection;
- soft-capability degradation;
- explicit strip/placeholder policy;
- cancellable remote image prefetch;
- capability-aware combo ordering;
- model-specific capability overrides;
- multimodal fixtures.

No modality may be silently deleted without a warning or policy result.

### M11 — Advanced middleware

Add optional optimizers only after semantic correctness is stable:

- tool-result compression;
- external compression proxy;
- image context compression;
- prompt policy modules;
- cache anchoring;
- per-request opt-out;
- bounded diagnostics.

Middleware runs after route identity and semantic normalization are fixed. It
cannot silently alter model selection or bypass cancellation.

### M12 — Secondary APIs and integrations

Only after the chat kernel is stable:

- embeddings;
- image generation;
- video generation;
- TTS/STT;
- search/fetch;
- provider usage APIs;
- tunnel/Tailscale/Cloudflare;
- MITM/DNS sidecar;
- IDE integrations;
- cloud sync.

Each belongs to a separate service or adapter package.

## 5. Testing strategy

### Contract tests

Every adapter covers:

```text
prepare
auth
model mapping
request cancellation
status/body error classification
stream decode
tool calls
thinking
usage
completion/error lifecycle
```

### Golden normalization tests

Fixtures cover OpenAI Chat, OpenAI Responses, Anthropic Messages, Gemini,
Claude Code, Codex-style continuity, tool-call repair, and multimodal content.

### Route tests

```text
public versus hidden model
nested combo
cycle
prefix collision
multiple connections
round-robin
sticky limit
weighted ordering
quota exhausted
auth refresh
fallback before first byte
no fallback after first byte
```

### Integration tests

Use local `httptest` upstreams to verify:

```text
config -> /v1/models -> request -> upstream -> stream -> usage
```

The base suite must not require real credentials.

### Load tests

Measure normalization CPU, translation CPU, SSE memory per stream, connection
selection contention, SQLite usage queue, quota refresh pressure, and config
reload under traffic.

## 6. Manual migration strategy

There is no automatic 9router importer.

Manual migration order:

```text
1. provider nodes
2. connections
3. custom models
4. model aliases as logical models
5. combos
6. selected public models
7. quota snapshots if useful
8. usage history only if explicitly needed
```

Migration notes must record semantic changes, especially:

- 9router flat combo names versus GoBroom nested combo graph;
- provider aliases versus provider-node prefixes;
- account/model locks versus runtime health/quota state;
- mixed 9router `/models` behavior versus explicit GoBroom publication.

## 7. Definition of done

### Core replacement

GoBroom is a practical replacement for the current chat use case when M0.2–M8
are complete and:

1. manual configuration recreates the active provider/account/model setup;
2. `/v1/models` exposes exactly the selected public models;
3. OpenAI Chat, Responses, and Anthropic fixtures pass;
4. combo and account fallback are deterministic;
5. OAuth refresh is per-connection and concurrency-safe;
6. quota/usage influence routing and survive restart;
7. long-lived SSE does not block control operations;
8. TUI manages the complete core control plane.

### Broader parity

M9–M12 are feature expansion, not prerequisites for the core replacement. They
must be added only through stable daemon contracts and must not re-couple TUI,
dashboard, provider quirks, or integrations to the hot path.

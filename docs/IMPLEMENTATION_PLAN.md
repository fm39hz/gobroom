# Implementation roadmap

Status is based on the repository implementation and tests inspected on
2026-10-03. This is the project roadmap and the source of truth for milestone
status. Architecture and compatibility documents describe contracts/evidence;
they do not override this status table.

## Status vocabulary

- **Done** — implemented in the repository and covered by tests for the stated
  scope. Does not imply parity beyond that scope.
- **Partial** — a usable slice exists, but listed gaps remain.
- **Planned** — not implemented yet.
- **Out of scope** — intentionally not part of the core daemon.

## Vision and product boundary

The governing product definition is [GoBroom vision](VISION.md): discovered
routes, physical models and combo models are separate management layers in one
typed graph, and exposure is a model property. Infrastructure options support
this workflow; they are not a mandate to implement every backend or
integration. The interaction contract is documented in [TUI UX](TUI_UX.md),
and model identity/capability/consumption semantics in
[Physical models](PHYSICAL_MODELS.md). End-to-end workflow scope, success
criteria and usability gaps are cataloged in [Product use cases](USE_CASES.md).

GoBroom is a daemon-first local AI gateway. It provides an HTTP-compatible data
plane for clients and a local IPC control plane for management. SQLite and
runtime routing state belong to the daemon; CLI/TUI are replaceable clients.

Core workflow:

```text
provider node + connections
  -> discover provider model sources
  -> map equivalent sources into physical models
  -> compose physical/combo models into role/use-case combos
  -> mark chosen models exposed
  -> /v1/models and request routing
```

No automatic 9router database importer is planned. Existing setup migration is
manual. Product and compatibility claims must be grounded in checked source,
tests or an explicitly labeled inference; see [compatibility matrix](COMPATIBILITY_MATRIX.md).

### Deliberately outside the core

MITM/DNS interception, IDE credential spoofing, tunnels, a hosted cloud-sync
service, dashboard, prompt-mutating token-saver experiments and media/search
endpoints are not required for the core provider-routing daemon. A portable
versioned config bundle is in scope; operating a hosted multi-writer sync
service is not. These features may be separate projects or future services,
but must not introduce provider-specific branches into the kernel.

## Milestone status

| Milestone | Scope | Status | Remaining work / exit condition |
|---|---|---|---|
| M0 | Source-grounded behavior inventory and model/prefix contracts | Done (core inventory) | Source-grounded architecture, typed model/prefix contracts and compatibility evidence are documented; unrelated 9router surfaces remain explicitly outside core. |
| M1 | SQLite control plane and immutable routing snapshot | Done | Typed config CRUD, validation/reload, route expansion, IPC and immutable snapshots are authoritative. Legacy publication/alias tables and APIs were removed; storage portability is separate. |
| M2 | Provider onboarding and model-management UX | Partial (typed control operations exist; end-to-end usability incomplete) | Basic LazyGit-style blocks, typed Discovered/Physical/Combos CRUD, exposure, usage/logs and route explanation exist. A selected connection can test its manifest-bound model-list endpoint; `i` now opens a review list with imported/new markers, filterable exact upstream IDs and explicit subset/entitlements-only apply. Connection model-list completeness is tracked; unknown accounts remain unroutable for discovered IDs. Slash-qualified source-prefix filtering matches through Physical source routes while showing one canonical Combo candidate. Combo editing uses separate ordered-member/candidate panes, typed weights and registry-backed strategy selection/options. A key-event acceptance fixture traverses Combo edit → save → exposure → `/v1/models`. Still missing: inference dry-run for definitions without a model-list operation, equivalence suggestions/bulk Physical grouping, per-connection assignment UX for custom IDs and fully joined provider→Physical→Combo onboarding. See [TUI UX](TUI_UX.md) and [Use cases](USE_CASES.md). |
| M3 | OpenAI Chat request vertical slice | Done (fake-upstream and OpenAI-compatible live acceptance) | Kernel/adapters and Bearer credential binding are covered by fixture; live OpenRouter connection tests `/models`, imports one currently listed free model, exposes it through Physical → Combo, and completes non-stream JSON plus SSE requests through the local endpoint (upstream reported cost 0). Direct OpenAI-vendor credentials have not been tested. |
| M4 | Hierarchical model execution, connection pool and policy primitives | Done (core) | Typed Physical/Combo boundaries, scoped passive feedback, adaptive ranking, session affinity, half-open admission, typed eligibility, sticky/weighted fallback, retry/commit boundaries, concurrent scheduler behavior and read-only snapshot resolution are implemented/tested. Panel/judge Fusion is explicitly an optional orchestration module outside the kernel core. See [Passive health](PASSIVE_HEALTH_ROUTING.md). |
| M5 | Canonical response-event contract | Done (core) | Kernel defines canonical lifecycle/content-block/text/thinking/tool/usage/completed/error events. Anthropic and OpenAI Chat/Responses SSE/JSON observers emit semantic events while preserving lossless client output; malformed JSON, continuity metadata and output-item boundaries are covered. |
| M6 | Responses/Anthropic semantic compatibility | Partial (Anthropic fake-upstream vertical verified; no live Anthropic credential) | Gemini/OpenAI request and response wire contracts are JSON-bound typed DTOs. OpenAI Chat requests translate into Gemini contents, system instructions, function declarations/calls, supported generation settings and reasoning; Gemini JSON and incremental SSE translate back, including tool calls, finish reasons and usage. Anthropic `/v1/messages` fake-upstream fixture proves connection-key auth, model rewrite and response; Anthropic model discovery uses its versioned API-key headers and cursor pagination. Remaining: live Anthropic account acceptance, broader SDK-shaped signatures/history, content-block/cache/stop/reasoning/tool conformance, full modality coverage and native Gemini/Antigravity envelopes. |
| M7 | Upstream credential lifecycle and OAuth | Partial (definition-bound refresh) | OAuth/no-auth/static factories bind from provider definitions through inference and discovery; connection type defaults from that binding and supplied credentials are validated on create/update. Typed access/refresh/client-secret state is parsed, refresh is fixture-tested against a token endpoint, refreshed state is persisted, and refresh locking is scoped per connection. Remaining: interactive authorization-code start/callback/state+PKCE handoff is not yet exposed through IPC/TUI, and no configured live OAuth provider has been verified. |
| M8 | Passive health, limits, performance and usage | Partial (runtime core; typed classifier options wired) | Typed outcomes, scoped feedback, reset-aware policy, bounded ranking, TTFT/throughput, EWMA, half-open trials, session affinity, durable health/usage IPC/TUI and retention/logging are implemented. Rich `ClassifyOutcome` survives registry wrapping; common typed HTTP error envelopes, quota reset/retry delay, Retry-After dates, rate-limit headers and manifest JSON pointers/signals/named windows feed route eligibility. The bounded coalescing writer persists asynchronously and flushes after producers stop; a daemon start → configured manifest HTTP 429 → two windows + connection scope → stop/reopen → restored route gating/cause fixture passes. Remaining: broader provider reset/error envelopes, scope precedence across simultaneous sources and state behavior on classifier/storage failure; add specialized extractors only when typed options cannot express the shape. |
| M9 | Provider presets and discovery expansion | Partial (executable shared composition) | Definitions now bind endpoint, transport, request/response codecs, static/OAuth/no-auth auth, model discovery, passive usage enrichment, session store, configurable error evidence and quota API operations. Quota polling resolves the dedicated quota operation's endpoint/transport/parser and remains opt-in/opportunistic. Session state is connection/definition/physical-model/client-session scoped, cached in memory and persisted asynchronously; OpenAI Responses restores stored continuity only with a client session key. Connection model-list snapshots now persist positive entitlement evidence and completeness; only positively observed discovered routes expand to that connection, while complete snapshots mark prior routes not listed and incomplete snapshots do not revoke evidence. Remaining: dedicated provider usage-report operations, custom per-connection availability editing and wider real-provider fixtures. |
| M10 | Physical identity, capability and modality policies | Done (physical core) | Typed support states, request-requirement compilation, profile persistence, typed TokenLimits, Physical identity/revision fields, source fidelity/evidence persistence, declared/guaranteed/available projections, projection source explanations and Physical IPC/TUI display are implemented and tested. Exact/alias sources are eligible by default; compatible/dynamic require opt-in and unknown fidelity is excluded. Conditional/emulated capability evidence does not satisfy hard requirements until an evaluator exists. Cross-protocol reasoning remains in M6. |
| M11 | Optional middleware | Planned/deferred | Not on the critical path. Revisit only for demonstrated need; keep opt-in, bounded and unable to mutate route identity or bypass cancellation. |
| M12 | Secondary APIs and integrations | Out of core | Embeddings/media/search, tunnels, MITM/DNS and IDE integrations remain separate services/sidecars, not kernel milestones. |
| M13 | Canonical configuration bundle and sync | Done (single-writer core) | Versioned secret-free typed bundle export, validation, diff/dry-run and atomic apply/import are implemented. Existing connection secrets are preserved by stable connection ID and never exported. Multi-writer conflict-free sync is intentionally not promised. |
| M14 | Portable operations and remote hosting | Done (local/remote boundary core) | Explicit paths/listeners, bounded logs/retention, optional bearer auth, journald guidance, TLS/reverse-proxy boundary and snapshot repository interface are documented/implemented. Alternate database backends remain an optional follow-up until a named backend is selected. |

## Milestone review and recommended order

The historical implementation sequence above is not the remaining-work queue.
M0–M1, M3–M5, M10 and M13–M14 have their stated core slices. M10's last gap found
in review—enforcing source fidelity at runtime and preventing unsupported
capability states from passing hard eligibility—has now been closed and tested.
The Gemini adapter now has a real OpenAI Chat translation path, so M6 is still
partial but no longer just a registered placeholder. A review also found and
closed a runtime wiring gap: rich HTTP error outcomes were being downgraded by
the registry adapter before reaching the kernel. Common quota/reset evidence
now feeds route eligibility; provider-specific extraction remains. The urgent
acceptance axis is OpenAI API-key and Anthropic Messages API-key readiness
(UC-40a/40b): verify provider definition → connection key → model route →
discoverable model → client request → upstream auth/response. Fake-upstream
vertical fixtures now cover both request paths and Anthropic model discovery;
live credentials are not required for deterministic acceptance.

1. **OpenAI/Anthropic API-key readiness:** fake-upstream fixtures now cover
   OpenAI Chat bearer-key routing, Anthropic `/v1/messages` key routing, and
   Anthropic model discovery. Broader SSE/auth-failure conformance and live
   provider validation remain optional; live keys require user authorization.
   Document unsupported count-tokens and protocol semantics rather than
   implying full harness parity.
2. **M2 workflow usability:** close the remaining provider → connection test →
   discover/review/custom model → suggested Physical grouping journey. The
   Combo edit/reorder/expose/API projection is now exercised through Tea key
   events and daemon IPC; preserve documented `h/l`, `j/k`, `Enter`, `Esc`,
   `Space`, `K/J`, `/` and `ctrl+s` semantics as the onboarding flow is joined
   to it.
3. **M8 provider evidence coverage:** test additional real-world error and
   reset envelopes, scope precedence when header/body/quota-API evidence
   conflict, and bounded-queue/storage failure behavior. Add specialized
   extractors only for shapes not expressible through current typed options.
4. **M7 interactive OAuth onboarding:** build on the definition-bound refresh
   flow with daemon-owned state/PKCE, authorization URL generation, a bounded
   callback exchange and connection-secret persistence. Keep the callback
   listener lifecycle independent of the TUI; verify cancellation, replay and
   concurrent connection isolation before live provider validation.
5. **M6 protocol conformance:** broaden Gemini end-to-end coverage to additional
   SDK-shaped signature/history cases and modality eligibility; validate
   reasoning option support against model families. Add Antigravity only from
   observed wire fixtures, not by assuming it is ordinary Gemini.
6. **M9 operation-surface completion:** wire dedicated provider usage-report
   APIs through the same composition contract; add real HTTP fixtures for
   quota/session behavior with representative providers. Keep unsupported
   operations absent rather than routing them to a default.

M11 remains deferred and M12 remains outside core. Alternate database and log
backends are portability options, not blockers for this local SQLite/journald
product. M13's bundle is the portability contract; do not sync database tables.

M13/M14 are architecture-enabling work, not reasons to delay usable local
SQLite, IPC and journald defaults. A second database or remote log sink should
be implemented only against a named need and testable contract.

## Architectural invariants

1. **No SQLite on the request hot path for static route configuration.** Build
   and validate a replacement snapshot before atomically publishing it.
2. **No global lock around provider execution.** Locks, if needed, are scoped to
   the specific mutable scheduling or credential state and never span upstream
   I/O.
3. **Provider additions are compositions first.** A provider manifest binds
   typed primitives. The kernel does not branch on provider names. A new
   primitive is shared infrastructure, not a provider-specific kernel patch.
4. **Management layers stay typed.** Discovered inventory, physical identity
   and combo policy are separate operations even though they form one graph.
   Node, connection and opaque upstream model ID remain distinct;
   prefixes/display labels are projections.
5. **Unknown remains unknown.** Missing capability or limit evidence is not
   replaced by a guessed global default; explicit negative evidence can
   override heuristics.
6. **Eligibility precedes strategy.** Compile request requirements and remove
   incompatible routes before fallback, rotation, weighting or adaptive
   scoring.
7. **Client intent is preserved.** Absent, auto, disabled, effort level and
   budget are distinct; lossy translation is explicit and observable.
8. **Runtime evidence is passive by default.** Real attempts update health,
   limits and performance; no synthetic network check or global quota poll runs
   unless explicitly configured.
9. **Priority stays explainable.** Runtime feedback never mutates durable user
   order. Positive preference is bounded, decayed, confidence-gated and applied
   only after hard eligibility.
10. **Exposure is explicit on the model.** Internal physical and combo models do
   not appear in `/v1/models` unless exposed; there is no separate publication
   workflow in the target UX.
11. **Policy boundaries survive resolution.** Resolving a role combo must not
   flatten away the strategy of its physical/combo members.
12. **No retry after response commitment.** Once response bytes are sent, errors
   are surfaced; the request cannot silently switch routes.
13. **Usage is best-effort and bounded.** Observability must not stall an SSE
   stream or first-byte delivery.
14. **Frontend independence.** Daemon operation does not depend on a running TUI
   or CLI; all frontend mutations use the same daemon control contract.
15. **One configuration truth.** UI edits, CLI operations and sync validate and
   apply the same versioned domain model; exports exclude secrets by default.
16. **Defaults stay simple.** SQLite, local IPC, loopback serving and
    stdout/journal logging work without external services.
17. **Typed-only model configuration.** Discovered routes, Physical models and
    Combo models are the only model-management primitives. Exposure is the
    `discoverable` field on those nodes; no separate alias/publication layer,
    compatibility table or fallback configuration path may be introduced.

## Delivery sequence

Use the user-workflow-first order in [Milestone review and recommended
order](#milestone-review-and-recommended-order), not the numeric order alone.
Every implementation slice is tested with `go test ./...`. For delivered CLI
features, install all binaries and exercise the installed CLI against the
running daemon (`make install-all`, followed by the relevant command). Live
provider tests are separate from credential-free contract tests.

## Drop-in replacement gate

Do not label GoBroom a drop-in replacement until all of the following are
verified against the user's intended workflows:

- manual configuration recreates selected provider nodes, connections,
  discovered routes, physical models, role/use-case combos and exposure flags;
- connection setup can test auth/endpoint, import `/models`, and add custom
  entries without SQL;
- `/v1/models` returns only models whose exposure flag is enabled;
- no model configuration is read from a second legacy or compatibility store;
- supported Chat, Responses and Anthropic formats have documented limits and
  passing fixtures, including streaming and tool calls;
- connection and combo ordering/fallback behavior is deterministic and
  concurrency-safe;
- credential refresh and quota state influence routing where configured and
  survive restart;
- real attempts drive scoped health/limit state without mandatory network
  healthchecks, and effective route order is explainable without mutating user
  priority;
- long-lived streams do not serialize or block control-plane actions;
- the TUI makes the workflow practical without direct database access;
- config can be validated/exported/imported without copying SQLite internals;
- remote data-plane access is authenticated independently of control-plane
  exposure.

Parity outside this gate—MITM, tunnels, media APIs, dashboards and similar
features—is not implied.

## Portability acceptance

Portability is additive to the local product, not a prerequisite for its
default install:

- documented state/log/config paths and listener/auth settings;
- clean operation under a user service manager with structured stdout/stderr;
- versioned config export/import with secret-safe diff and atomic validation;
- remotely hosted data plane protected separately from management APIs;
- a named alternate storage backend only after migration, transaction,
  concurrency and recovery tests.

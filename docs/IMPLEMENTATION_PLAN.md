# Implementation roadmap

Status is based on the repository implementation and tests inspected on
2026-10-08. This is the project roadmap and the source of truth for milestone
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
Reuse decisions from the local CLIProxyAPI source review are recorded in
[CLIProxyAPI reuse review](CLIPROXYAPI_REUSE_REVIEW.md); that source informs
provider fixtures but is not a Gobroom runtime dependency.

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
| M6 | Responses/Anthropic semantic compatibility | Partial (Anthropic fake-upstream vertical verified; no live Anthropic credential) | Gemini/OpenAI request and response wire contracts are JSON-bound typed DTOs. OpenAI Chat requests translate into Gemini contents, system instructions, function declarations/calls, supported generation settings and reasoning; Gemini JSON and incremental SSE translate back, including tool calls, finish reasons and usage. Anthropic `/v1/messages` fake-upstream fixture proves connection-key auth, model rewrite and response; Anthropic model discovery uses its versioned API-key headers and cursor pagination. Anthropic input binds system/content/tool history/choice/common generation settings into typed IR and rebuilds OpenAI Chat and Responses requests. Responses maps explicit disabled reasoning; directly representable effort levels can be encoded by the request codec, but Anthropic egress still requires signed thinking events and therefore rejects that cross-protocol route today. Anthropic token budgets/adaptive intent are excluded before dispatch when they cannot be faithfully projected, and malformed thinking fields remain explicit unsupported facets. Anthropic semantic egress maps canonical text/tool/usage and issuer-signed thinking; unsigned or transform-invalid thinking is rejected. OpenAI Chat and Responses decoders have cross-wire egress fixtures. Remaining: live Anthropic account acceptance, broader SDK-shaped signatures/history, content-block/cache/stop/tool conformance, request continuity and named-loss policy, full modality coverage and native Gemini/Antigravity envelopes. |
| M7 | Upstream credential lifecycle and OAuth | Partial (daemon-owned authorization-code and device flows) | OAuth/no-auth/static factories bind from provider definitions; OAuth options can declare an RFC 8628 device endpoint. Authorization-code IPC/CLI/TUI owns S256 PKCE and one-use state; HTTP loopback redirects bind only their configured local address, validate exact callback target, exchange in daemon and return a fixed non-secret completion page. Device IPC/CLI/TUI displays only verification URL/user code while the daemon retains the device code and polls through `x/oauth2` (including `authorization_pending`, `slow_down` and expiry); credentials use compare-and-swap before persistence/reload. Remaining: live provider OAuth acceptance, denial/expiry/shutdown lifecycle matrix, HTTPS/non-loopback callback integrations and deeper credential-generation/version tests. |
| M8 | Passive health, limits, performance and usage | Partial (runtime core; typed classifier options wired) | Typed outcomes, scoped feedback, reset-aware policy, bounded ranking, TTFT/throughput, EWMA, half-open trials, session affinity, durable health/usage IPC/TUI and retention/logging are implemented. Rich `ClassifyOutcome` survives registry wrapping; common typed HTTP error envelopes, quota reset/retry delay, Retry-After dates, rate-limit headers and manifest JSON pointers/signals/named windows feed route eligibility. The bounded coalescing writer persists asynchronously and flushes after producers stop; a daemon start → configured manifest HTTP 429 → two windows + connection scope → stop/reopen → restored route gating/cause fixture passes. Remaining: broader provider reset/error envelopes, scope precedence across simultaneous sources and state behavior on classifier/storage failure; add specialized extractors only when typed options cannot express the shape. |
| M9 | Provider presets and discovery expansion | Partial (executable shared composition) | Definitions now bind endpoint, transport, request/response codecs, static/OAuth/no-auth auth, model discovery, passive usage enrichment, session store, configurable error evidence and quota API operations. Quota polling resolves the dedicated quota operation's endpoint/transport/parser and remains opt-in/opportunistic. Session state is connection/definition/physical-model/client-session scoped, cached in memory and persisted asynchronously; OpenAI Responses restores stored continuity only with a client session key. Connection model-list snapshots now persist positive entitlement evidence and completeness; only positively observed discovered routes expand to that connection, while complete snapshots mark prior routes not listed and incomplete snapshots do not revoke evidence. Remaining: dedicated provider usage-report operations, custom per-connection availability editing and wider real-provider fixtures. |
| M10 | Physical identity, capability and modality policies | Done (physical core) | Typed support states, request-requirement compilation, profile persistence, typed TokenLimits, Physical identity/revision fields, source fidelity/evidence persistence, declared/guaranteed/available projections, projection source explanations and Physical IPC/TUI display are implemented and tested. Exact/alias sources are eligible by default; compatible/dynamic require opt-in and unknown fidelity is excluded. Conditional/emulated capability evidence does not satisfy hard requirements until an evaluator exists. Cross-protocol reasoning remains in M6. |
| M11 | Typed request/response transformation primitives | Partial (scoped config, schema and effect core) | Versioned transform refs and enabled bindings round-trip through SQLite, secret-free bundles and immutable request-pinned snapshots. Request transforms run at daemon, nested model, route, provider or connection scope with deterministic order and branch-local copies; response transforms use the same scopes and disable native passthrough when active. Registration alone does not activate a transform. Transform descriptors and option schemas now join the shared extension catalog; invalid options fail config validation. Undeclared request mutations fail transactionally, and response semantic fields are effect-checked. Operation payloads/events/output have basic byte and deadline bounds. Remaining: schema-driven TUI binding UX, complete effects/opaque-facet coverage, transactional safe-fail-open and transform-specific budgets. |
| M12 | Secondary operation implementations and integrations | Out of core | Operation-neutral routing is an architectural requirement; embeddings/media/search implementations, tunnels, MITM/DNS and IDE integrations may remain separate services/sidecars and are not initial core feature commitments. |
| M13 | Canonical configuration bundle and sync | Done (single-writer core) | Versioned secret-free typed bundle export, validation, diff/dry-run and atomic apply/import are implemented. Existing connection secrets are preserved by stable connection ID and never exported. Multi-writer conflict-free sync is intentionally not promised. |
| M14 | Portable operations and remote hosting | Done (local/remote boundary core) | Explicit paths/listeners, bounded logs/retention, optional bearer auth, journald guidance, TLS/reverse-proxy boundary and snapshot repository interface are documented/implemented. Alternate database backends remain an optional follow-up until a named backend is selected. |
| M15 | Architecture catch-up conformance | Partial (catalog, compatibility, transform and replay cores in progress) | `make test-conformance` covers registration/schema binding, exact provider/operation refs, typed non-chat schema admission, IPC/HTTP/CLI projection, request/response facet planning, transform binding persistence/scoping/options/effect rollback, transform-aware passthrough and first dispatch-effect replay rules. Feature requirements now carry exact evaluator refs; evaluator descriptors/factories and constraint schemas bind from the frozen shared catalog, including same-ID evaluator versions and fail-closed schema validation. Request/response transform implementations bind from that same catalog; exact transform refs/options schemas validate before execution. Strategy modules/options schemas share the catalog. Physical/Combo policies persist exact refs, validate options through the catalog at CRUD/bundle/reload boundaries, and pass contract-scoped config into the immutable model snapshot and scheduler. Provider primitive registry and runtime caches now key by typed exact ref; same-ID v1/v2 endpoint, transport, codecs and auth factories bind independently, while quota/usage/session/error consumers retain their typed refs end-to-end. Request codec options now validate against the selected codec's exact schema and bind into its factory. Operation input payloads, decoded response events and rendered output enforce declared bounds and per-operation deadlines. Operations with result schemas require incremental bounded projectors; canonical events are schema-validated and the projected result is validated before terminal completion. Artifact types declare owner/replay/sensitivity/recipient/media/size policy; operations declare named typed artifact input ports; a generic multipart ingress streams declared fields into bounded `BodyRef`s, and a reusable multipart provider codec streams those leases into manifest-selected form fields. Temporary spools are released after request completion and rolled back on decode failure. Remaining C1 work: built-in provider presets do not yet bind the generic multipart codec to real non-chat operations or native multimodal chat fields; extension dependency resolution in portable bundles and provider/client artifact transfer through response rendering remain open. Full compatibility planning, interactive auth, schema-driven transform UI/effect budgets/safe-fail-open, issuer-scoped idempotency and operation/job replay fixtures remain to implement and prove with SG1–SG7 in [M15 conformance evidence](M15_CONFORMANCE.md). |

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

0. **M15 semantic closure:** the existing `make test-conformance` passes its
   composition subset. The stronger SG1–SG7 gate remains pending. Follow the
   ordered slices below to implement the fixed contracts; do not infer full
   architecture readiness from auth factories, operation IDs or renderer
   registration alone.
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

M11's individual transformations remain deferred and M12 implementations
remain outside initial core. Alternate database and log
backends are portability options, not blockers for this local SQLite/journald
product. M13's bundle is the portability contract; do not sync database tables.

M13/M14 are architecture-enabling work, not reasons to delay usable local
SQLite, IPC and journald defaults. A second database or remote log sink should
be implemented only against a named need and testable contract.

## M15 semantic closure delivery

The design is fixed in [Semantic extension contracts](EXTENSION_CONTRACTS.md).
These slices implement that design; they do not reopen the model graph or
introduce provider-specific core workflows. C1 has started; C2–C6 remain
pending.

1. **C1 — catalog and semantic envelope (in progress):** the local-schema,
   versioned descriptor/factory catalog freezes with the provider runtime;
   provider, primitive and task refs pin exact contracts. Built-in endpoint,
   usage, classifier and OAuth options are schema-validated. Operation
   definitions own typed payload schemas, requirement compilers and replay
   declarations; ingress, provider task bindings, immutable routes and kernel
   execution resolve one exact operation version. Feature evaluator refs and
   constraint schemas now bind from that same frozen catalog. The
   descriptor/schema catalog is available through IPC/HTTP/CLI. Exact primitive
   versions, strategy/evaluator factories, operation/artifact schemas, bounded
   body refs, declarative bindings and atomic bundle dependency validation are
   now composed through that catalog. Bundle v5 embeds provider definitions
   only when manifest auth/default/endpoint/codec fields are provably
   secret-free and persists them for daemon-restart activation. Opaque or
   sensitive definitions remain exact external module dependencies; an
   unresolved definition is retained as inert setup only when no model route or
   Physical source uses it. Remaining: external primitive module distribution
   and resolution, side-by-side contract versions for provider definitions, and
   the integrated extension proof below.
2. **C2 — full compatibility planning (in progress):** the kernel now asks
   adapters for a request/route/operation-scoped `CompatibilityPlan` and
   recomputes admission from immutable facet declarations rather than trusting
   a boolean. Ordered facet composition, fail-closed required facets, named
   loss grants/denials and fidelity aggregation are implemented; the composed
   adapter now combines request-codec declarations with the response-wire
   facet and composes decoder event declarations with renderer event coverage.
   OpenAI Chat, OpenAI Responses, Anthropic Messages and Gemini declare bounded
   request facets; the response decoders enumerate event families and renderers
   must declare each request-required family. Missing text/completion/tool/
   thinking coverage now fails admission. Active semantic transforms disable
   native wire passthrough; OpenAI Chat re-encodes semantic events while raw
   passthrough renderers reject the plan. An Anthropic Messages semantic egress
   renderer now maps text/tool/usage and issuer-signed thinking; missing or
   transform-invalid signatures reject admission. Fake OpenAI Chat and
   Responses decoder→Anthropic renderer fixtures prove JSON/SSE cross-wire
   verticals. Physical/Combo nodes persist named loss grants/denials that flow
   down the model path; denial wins and admitted lossy plans are recorded in
   usage. Repeatable CLI `--allow-loss`/`--deny-loss` and portable bundle v5
   carry the policy. A server allow/deny ceiling is managed through the CLI and
   SQLite-backed snapshot. Loss records retain requested/effective values,
   semantic paths and granting model nodes. Candidate plans now include the
   ordered request/response transform chains and authorized operation-to-
   provider artifact handoffs. The selected payload-free plan summary is now
   persisted with successful usage, returned through `usage.list` and shown in
   the TUI Usage inspector. `routes.explain` now accepts a normalized request
   sample and evaluates every enabled model-path candidate through the same
   compatibility planner without strategy-state mutation, credential lookup
   or upstream dispatch; CLI accepts a JSON file or stdin. Remaining: close
   the full SG1/SG4 matrices, including response artifact rendering and
   transform fallback accounting.
3. **C3 — transform plan compilation (in progress):** transform modules and
   enabled bindings are distinct. Exact module refs, binding scope/order/options
   envelopes and enable state persist through the typed DB bundle and immutable
   serving snapshot. Daemon, nested model, route, provider and connection scopes
   are compiled in deterministic order; each mutating request scope gets a
   branch-local deep copy and recomputes operation requirements. Response chains
   use the chosen model/route scopes and preserve the native-passthrough boundary.
   Transform module descriptors/options schemas now register in the shared
   extension catalog; config import/publish rejects unknown refs and invalid
   options before applying state. Request transforms are transactional and
   checked against declared semantic effects; response event mutations are
   similarly checked. Remaining: schema-driven TUI binding UX, complete opaque
   artifact/effect coverage, scoped safe-fail-open/resource budgets, and full
   fallback, cancellation and original-versus-rendered accounting proofs.
4. **C4 — daemon auth coordinator (in progress):** authorization-code IPC owns
   state/S256 PKCE, bounded expiry/exchange, callback validation, cancellation
   and replay rejection. RFC 8628 uses `x/oauth2` DeviceAuth/DeviceAccessToken;
   a daemon-owned, cancellable worker handles pending/slow-down/expiry without
   a connected frontend. CLI and TUI expose only public actions; device codes
   remain daemon-private. Credential compare-and-swap fences delayed auth and
   refresh against newer secrets. The loopback callback listener is now
   exercised through HTTP and the TUI projects its status. Remaining: provider
   lifecycle fixtures for deny/expiry/slow-down and shutdown/cancel races,
   plus HTTPS/non-loopback callback integrations. Opaque leases and
   prepared request signing remain separate auth-driver work. Validate full
   lifecycle fixtures, not token-only stubs.
5. **C5 — attempt replay and operation execution (in progress):** operation
   replay declarations now reach the kernel attempt executor. Ambiguous transport
   failure and accepted-response decode/render failure stop instead of trying a
   second upstream route; `confirmed_rejection_only` may continue after an
   explicitly classified rejection such as 429. Missing effect evidence is
   unsafe. Remaining: actual issuer-scoped idempotency-key binding, provider
   operation replay overrides, async job effects, and a real non-chat
   payload/result through the shared graph executor while preserving the
   pre/post-client-commit boundary.
6. **C6 — integrated extension proof:** implement SG1–SG7 with a provider module
   that uses the completed contracts without changing kernel graph types,
   core schema or generic request/frontend workflow. Add all cases to
   `make test-conformance`; run ordinary integration checks, install delivered
   binaries and exercise the matching CLI/control operations.

Completion of C1–C6 proves runtime architecture closure for the declared
scope. It does not change M2/M6/M7/M8/M9 feature completeness automatically;
update those milestones only against their own exit criteria. Secondary
operation fixtures prove architecture without committing to ship every media
product feature. No legacy compatibility path is retained during these changes.

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
18. **Catch-up through extensions.** Provider, operation, client format,
    capability, auth, policy, transform and telemetry additions use the stable
    extension contracts in [`SOLUTION_ARCHITECTURE.md`](SOLUTION_ARCHITECTURE.md).
    Adding a provider-specific implementation must not require provider-name
    branches in kernel, schema or generic TUI/CLI workflows.

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

Drop-in replacement and architecture readiness are different claims. The
architecture catch-up gate is passed only after the M15 change simulations;
passing M15 does not imply that any omitted 9router feature has been
implemented or that GoBroom is a drop-in replacement.

## Portability acceptance

Portability is additive to the local product, not a prerequisite for its
default install:

- documented state/log/config paths and listener/auth settings;
- clean operation under a user service manager with structured stdout/stderr;
- versioned config export/import with secret-safe diff and atomic validation;
- remotely hosted data plane protected separately from management APIs;
- a named alternate storage backend only after migration, transaction,
  concurrency and recovery tests.

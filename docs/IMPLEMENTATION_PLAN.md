# Implementation roadmap

Status is based on the repository implementation and tests inspected on
2026-10-08. This is the project roadmap and the source of truth for milestone
status. Architecture and compatibility documents describe contracts/evidence;
they do not override this status table.

The [product progress snapshot](PRODUCT_STATUS.md) reviews the complete user
journey across these milestones. Update it at integrated milestones; do not
report only the feature currently being changed or infer product completeness
from a green architecture subset.

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
| M2 | Provider onboarding and model-management UX | Partial (typed control operations exist; end-to-end usability incomplete) | Basic LazyGit-style blocks, typed Discovered/Physical/Combos CRUD, exposure, usage/logs and route explanation exist. A selected connection can test its manifest-bound model-list endpoint; `i` opens a review list with imported/new markers, filterable exact upstream IDs and explicit subset/entitlements-only apply. Successful import refreshes Discovered and typed model views so routes are immediately available for curation. Static provider registries are distinct: preview lists candidates, selected IDs become explicit connection assignments, and the UI labels upstream access unverified instead of fabricating entitlement. `T` runs an explicit one-output-token chat probe; `P` and `connections test-operation` execute a user-supplied payload through an exact registered non-chat provider binding after operation-schema validation. These probes do not mutate catalog, usage or health; operation probes return only bounded response metadata and may incur cost or create resources. Connection model-list completeness is tracked; unknown accounts remain unroutable for discovered IDs. Custom upstream IDs carry explicit per-connection assignments; routes expand only to assigned, enabled accounts, and connection-context TUI/CLI workflows require that scope. Config bundle v9 preserves custom and static-catalog assignments with provenance. Slash-qualified source-prefix filtering matches through Physical source routes while showing one canonical Combo candidate. Combo editing uses separate ordered-member/candidate panes, typed weights and registry-backed strategy/options. The acceptance gate creates two providers and API-key connections through TUI forms, tests both model endpoints, previews/imports routes, reviews equivalent Physical sources, creates/exposes a Combo, then verifies `/v1/models` through fake providers and real TUI/IPC/SQLite/API boundaries. Creating a Physical uses conservative identity across the full Discovered catalog: equivalent routes are prefilled in stable order into a review draft, fidelity/evidence is recorded, existing policy/routes are preserved, and no draft is saved without user confirmation. CLI/TUI operation probes now accept a strict local-file JSON manifest and stream declared artifacts over Unix IPC; shared ingress/store limits, exact operation ports, recipient authorization and artifact interpreters apply before dispatch. Probe bodies are released on completion/failure. Whole-catalog Physical review/split UX and the full management acceptance matrix remain open. See [TUI UX](TUI_UX.md) and [Use cases](USE_CASES.md). |
| M3 | OpenAI Chat request vertical slice | Done (fake-upstream and OpenAI-compatible live acceptance) | Kernel/adapters and Bearer credential binding are covered by fixture; live OpenRouter connection tests `/models`, imports one currently listed free model, exposes it through Physical → Combo, and completes non-stream JSON plus SSE requests through the local endpoint (upstream reported cost 0). Direct OpenAI-vendor credentials have not been tested. |
| M4 | Hierarchical model execution, connection pool and policy primitives | Done (core) | Typed Physical/Combo boundaries, scoped passive feedback, adaptive ranking, session affinity, half-open admission, typed eligibility, sticky/weighted fallback, retry/commit boundaries, concurrent scheduler behavior and read-only snapshot resolution are implemented/tested. Panel/judge Fusion is explicitly an optional orchestration module outside the kernel core. See [Passive health](PASSIVE_HEALTH_ROUTING.md). |
| M5 | Canonical response-event contract | Done (core) | Kernel defines canonical lifecycle/content-block/text/thinking/tool/usage/completed/error events. Anthropic and OpenAI Chat/Responses SSE/JSON observers emit semantic events while preserving lossless client output; malformed JSON, continuity metadata and output-item boundaries are covered. |
| M6 | Responses/Anthropic semantic compatibility | Partial (Anthropic fake-upstream vertical verified; no live Anthropic credential) | OpenAI Chat and OpenAI Responses ingress translate from typed IR into Gemini or Anthropic provider request contracts; Gemini JSON/SSE and Anthropic JSON/SSE decode back to canonical events and semantic client egress. `TestGeminiRuntimeBindingMapsOpenAIResponsesThroughGeminiAndBack` proves typed Responses tool history/choice, generation, an explicit numeric reasoning budget and a base64 image→Gemini `inlineData` through the ordinary kernel/provider binding; `TestKernelMapsOpenAIResponsesThroughAnthropicMessagesAndBack` composes Responses prompt/tools/choice/generation through Anthropic Messages and back into Responses text/tool/usage output. Cross-provider `previous_response_id` and unsupported provider/envelope fields are excluded before dispatch. Reasoning effort/intent and numeric budget remain separate facets; Gemini maps explicit budget, and Anthropic output-limit adjustment requires a named loss grant that is applied on the wire. Anthropic `/v1/messages` acceptance and model discovery have fake-upstream coverage, but no live credential test. Anthropic token budgets/adaptive intent are excluded when they cannot be faithfully projected, and malformed thinking fields remain explicit unsupported facets. Anthropic semantic egress preserves issuer-signed thinking and rejects unsigned/transform-invalid signatures. Remaining: broader SDK-shaped signatures/history, content-block/cache/stop/tool conformance, request continuity, other modality envelopes and native Gemini/Antigravity integrations. |
| M7 | Upstream credential lifecycle, selectable auth modes and OAuth | Partial (per-connection auth modes, runtime credential context, shared header application and OAuth sessions implemented; live provider/lifecycle matrix remains) | Provider definitions bind typed auth modes and a default setup mode; that default no longer restricts connection types. Daemon validation, interactive authorization, discovery, inference probes, quota requests and immutable route construction select the exact flow by the connection's `credentialType`. Typed token header/prefix/static headers plus auth-flow-produced request headers and bounded, non-serialized string `AuthContext` are passed into shared adapters and provider request-codec/transport primitives; the kernel never branches on provider metadata. CLI/TUI catalogs expose mode setup schemas; TUI cycles modes with `ctrl+o` and rebuilds fields. Tests cover dual-mode routing, dynamic header application, external AuthFactory→AuthContext→codec flow, mode-specific onboarding, and clearing old secret material when changing credential types. OAuth authorization-code/device, PKCE, CAS persistence and pending-connection bootstrap are implemented. Resolve/refresh and per-connection semaphore waits honor caller/daemon cancellation; durable private revisions fence late setup/refresh across same-secret resaves, A→B→A, disabled/re-enabled and recreated accounts. Concurrent refresh callers share one replacement; public optional auth interfaces allow a separate module to implement device setup. Auth setup/refresh now return provider-owned bounded, redacted CredentialState; the daemon validates via Resolve before revision CAS instead of serializing runtime credentials as OAuth. OAuth-only durable fields were removed from kernel.Credential; public OAuth helpers support module composition. A custom opaque_ticket module proves setup, HTTP-401 refresh, metadata privacy and recovery after restart. Same-secret generation/header/context refresh retains replay restrictions; static-module host contract v2 rejects the old Go interface. Manual and loopback denial now reach retained terminal status; loopback expiry is observable and releases its listener. Remaining: the wider lifecycle/expiry/shutdown stress matrix, post-commit route/health reconciliation acceptance, HTTPS/non-loopback callback integrations and live provider OAuth acceptance. |
| M8 | Passive health, limits, performance and usage | Partial (scope-correct persistence, generic deadline precedence, observable async writes, permanent invalidation, config-generation fencing, quota evidence merge and durable failure spool implemented) | Typed outcomes, scoped feedback, reset-aware policy, bounded ranking, TTFT/throughput, EWMA, half-open trials, session affinity, durable health/usage IPC/TUI and retention/logging are implemented. Rich `ClassifyOutcome` survives registry wrapping; common typed HTTP error envelopes, quota reset/retry delay, Retry-After dates, rate-limit headers and manifest JSON pointers/signals/named windows feed route eligibility. Manifest `limitHeaders` binds independently named windows to provider-specific limit/used/remaining/reset headers and exact status-to-exhausted values; only exhausted or otherwise unqualified reset-only windows extend the retry deadline, so an allowed or positive-remaining window cannot lengthen another window's cooldown. Resets accept epoch/RFC3339/HTTP-date and Go duration forms, and independent windows survive as separate observations. Ordered scope signals match arbitrary typed JSON Pointer fields without provider-specific extractor code; bound classifier options deep-copy nested matchers so an immutable runtime catalog cannot be changed through caller-owned slices. Health persistence preserves route/model, connection and provider scope independently through SQLite and restart; every persisted breaker/quota observation carries an opaque fingerprint of its provider/credential/model routing configuration. Results, usage and quota enrichment from requests pinned to obsolete route generations are ignored; restored health/quota rows must match the current generation. Generationless health/quota rows are treated as unknown and not restored. Generic classifier precedence is explicit (error-body reset → Retry-After → X-RateLimit-Reset → structured body retry delay), and rate-limit reset headers drive cooldown even when remaining is omitted. Quota state resolves by observation time first, with response-header > success-body > error-body > quota-API > inferred authority on timestamp ties; SQLite atomically applies the same rule. Auth/permission/model-not-found rejections persist as invalid at classified scope; credential/provider configuration changes reset affected health and quota state, and positive discovery clears only model IDs confirmed by the endpoint. Health observations are coalesced by typed breaker identity in a bounded non-blocking writer; transient write failures retry, then spool durable bounded snapshots and replay after restart. Repeated failures dead-letter after 12 cycles; CLI exposes spool status/requeue. Queue/spool exhaustion or spool filesystem failure is logged as unrecoverable. Accepted events flush after kernel shutdown. A daemon start → configured manifest HTTP 429 → quota/header windows + connection scope → stop/reopen → restored route gating/cause fixture passes. Remaining: provider-specific reset/error envelope fixtures and specialized extractors only for shapes not representable by typed JSON Pointer/options contracts. |
| M9 | Provider presets and discovery expansion | Partial (executable shared composition) | Definitions now bind endpoint, transport, request/response codecs, static/OAuth/no-auth auth, model discovery, passive usage enrichment, session store, configurable error evidence and quota API operations. Model discovery now supports manifest-owned static catalogs with no fake endpoint/transport; OpenCode Go's model registry is represented as typed static data, while active connection-test remains unavailable for static catalogs. Static catalog import records only explicit per-connection assignments, never observed entitlement; assignments are shown distinctly from API-observed availability and travel in bundle v9. Built-in manifests are embedded in the daemon and checked byte-identical to their source files. The built-in `http-json-quota` source binds schema-validated per-provider options and maps multiple normalized windows from one JSON report via typed JSON Pointers, including object-map/array rows, used/remaining percentages, reset/status, explicitly unreported-window suppression and credential-header selection. `usedPercentPath` covers the observed OpenCode Go report semantics (percent used, not remaining) without provider-specific parsing. It composes up to eight report endpoints with independent endpoint options/windows and `fail_all` or `keep_available` semantics; `remainingFromLimitUsed` makes credit arithmetic explicit. It also maps configured response headers into quota windows, covering the observed Groq pattern of reading request/token limits from `/models` headers without provider-specific code. A typed model/connection quota scope controls both persistence identity and polling deduplication; the Groq preset binds connection scope so one `/models` quota read covers that account rather than repeating once per model. A generic `any_positive` aggregation plus root status gate models the observed DeepSeek rule: balance is available only when `is_available` is true and any reported currency balance is positive. Response and normalized-window counts are bounded. Quota-source instances with options receive definition/operation-scoped runtime refs; poller, opportunistic refresh, authority/freshness merge, generation fencing and persistence remain generic. OpenRouter-shaped `/key` + `/credits`, DeepSeek balance, Groq rate-header and OpenCode Go percent-used fixtures exercise the shared source without provider-name code. Quota polling remains opt-in/opportunistic. Session state is connection/definition/physical-model/client-session scoped, cached in memory and persisted asynchronously; OpenAI Responses restores stored continuity only with a client session key. Endpoint model-list snapshots persist positive entitlement evidence and completeness; only positively observed discovered routes expand automatically, while complete snapshots mark prior routes not listed and incomplete snapshots do not revoke evidence. Custom and static-catalog routes carry explicit per-connection assignments with distinct provenance. Remaining: broader provider-specific quota/report fixtures, custom availability overrides beyond assignment and wider real-provider fixtures. |
| M10 | Physical identity, capability and modality policies | Done (physical core) | Typed support states, request-requirement compilation, profile persistence, typed TokenLimits, Physical identity/revision fields, source fidelity/evidence persistence, declared/guaranteed/available projections, projection source explanations and Physical IPC/TUI display are implemented and tested. Exact/alias sources are eligible by default; compatible/dynamic require opt-in and unknown fidelity is excluded. Conditional/emulated capability evidence does not satisfy hard requirements until an evaluator exists. Cross-protocol reasoning remains in M6. |
| M11 | Typed request/response transformation primitives | Partial (scoped config, schema, effects, bounds, IPC and TUI management) | Versioned transform refs and enabled bindings round-trip through SQLite, secret-free bundles and immutable request-pinned snapshots. The local IPC control plane provides typed binding list/upsert/delete; each mutation validates the full candidate set against the frozen catalog before persistence and snapshot reload. A dedicated TUI block separates active bindings from the versioned transform catalog; descriptor options schemas generate typed option fields/defaults and structured JSON inputs, while the daemon remains the validation authority. Request transforms run at daemon, nested model, route, provider or connection scope with deterministic order and branch-local copies; response transforms use the same scopes and disable native passthrough when active. Registration alone does not activate a transform. Safe-fail-open modes are exact descriptor permissions and persist per binding. Request chains publish atomically only after every step validates; cancellation before/during transforms remains fail-closed even if a transform returns success after canceling its context, and a kernel fixture proves no upstream dispatch. `TestKernelResponseTransformCancellationStopsFallbackBeforeAndAfterCommit` covers direct kernel cancellation before/after client commitment. `TestClientDisconnectCancelsDataPlaneExecution` covers actual HTTP → kernel fallback → blocked response transform cancellation, with no cross-route fallback or successful usage completion. Response fail-open is allowed only before renderer output begins. Original provider usage remains separate from transformed client projection. Serialized payload bounds and cooperative transform deadlines are enforced, with their in-process limitations documented. The typed effect matrix covers prompt/input/tools/thinking/options/continuity and protected opaque evidence; response effects cover text/thinking/tool arguments/usage. Kernel-owned mutation markers drive native OpenAI Chat and Responses overlays; Responses patches prompt/input and function-call/function-call-output items while retaining source metadata, and rejects unsupported operation/modalities before dispatch. Native Anthropic overlays patch system blocks, text/image/tool-use/tool-result blocks, tools/choice, generation options and thinking while preserving source metadata; new canonical user/assistant/tool-result messages append as a suffix in IR order, and ambiguous middle insertions or unsupported content fail closed. Hierarchical fallback usage proves rejected-branch failure evidence does not leak into the selected branch. Remaining: unsupported Responses modality/tool-call edge cases, Anthropic opaque-envelope edge cases, concurrent client-disconnect and daemon-shutdown cancellation stress. |
| M12 | Secondary operation implementations and integrations | Out of core | Operation-neutral routing is an architectural requirement; embeddings/media/search implementations, tunnels, MITM/DNS and IDE integrations may remain separate services/sidecars and are not initial core feature commitments. |
| M13 | Canonical configuration bundle and sync | Done (single-writer core) | Versioned secret-free typed bundle export, validation, diff/dry-run and atomic apply/import are implemented. Existing connection secrets are preserved by stable connection ID and never exported. Multi-writer conflict-free sync is intentionally not promised. |
| M14 | Portable operations and remote hosting | Done (local/remote boundary core) | Explicit paths/listeners, bounded logs/retention, optional bearer auth, journald guidance, TLS/reverse-proxy boundary and snapshot repository interface are documented/implemented. Alternate database backends remain an optional follow-up until a named backend is selected. |
| M15 | Architecture catch-up conformance | Partial (catalog, compatibility, transform and replay cores in progress) | `make test-conformance` covers registration/schema binding, exact provider/operation refs, typed non-chat schema admission, IPC/HTTP/CLI projection, request/response facet planning, transform binding persistence/scoping/options/effect rollback, transform-aware passthrough and first dispatch-effect replay rules. Feature requirements now carry exact evaluator refs; evaluator descriptors/factories and constraint schemas bind from the frozen shared catalog, including same-ID evaluator versions and fail-closed schema validation. Request/response transform implementations bind from that same catalog; exact transform refs/options schemas validate before execution. Strategy modules/options schemas share the catalog. Physical/Combo policies persist exact refs, validate options through the catalog at CRUD/bundle/reload boundaries, and pass contract-scoped config into the immutable model snapshot and scheduler. Provider primitive registry and runtime caches now key by typed exact ref; same-ID v1/v2 endpoint, transport, codecs and auth factories bind independently, while quota/usage/session/error consumers retain their typed refs end-to-end. Request codec options now validate against the selected codec's exact schema and bind into its factory. Operation input payloads, decoded response events and rendered output enforce declared bounds and per-operation deadlines. Operations with result schemas require incremental bounded projectors; canonical events are schema-validated and the projected result is validated before terminal completion. Artifact types declare owner/replay/sensitivity/recipient/media/size policy; operations declare typed, bounded artifact input and output ports; a generic multipart ingress streams declared fields into bounded `BodyRef`s, and a reusable multipart provider codec streams those leases into manifest-selected form fields. Provider response artifacts now stream through an operation-authorized private spool and client renderer access scope, with response completion releasing the lease. Temporary spools are released after request completion and rolled back on decode failure. C1's module-resolution design is closed as statically linked Go dependencies; end-to-end non-chat/multimodal operation coverage is tracked as C6 conformance proof and product feature scope. Full compatibility planning, interactive auth, schema-driven transform UI/effect budgets/safe-fail-open, issuer-scoped idempotency and operation/job replay fixtures remain to implement and prove with SG1–SG7 in [M15 conformance evidence](M15_CONFORMANCE.md). |

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
   to it. Pressing `f` on a discovered route proposes a canonical Physical
   name and preloads all discovered routes with the same conservative
   normalized identity into a reviewable draft; routes, fidelity and evidence
   remain inspectable before saving.
3. **M8 provider evidence coverage:** test additional real-world error and
   reset envelopes and validate field-based scope mapping against captured
   provider fixtures. Generic body/header deadline precedence, quota
   freshness/authority conflict resolution, provider-configured quota HTTP
   statuses, and declarative scope mapping with class/cause consistency are
   tested. Bounded asynchronous health/quota write failure now retries, spools
   durably, dead-letters after the retry cap and exposes CLI status/requeue;
   unrecoverable spool-full/filesystem failures remain logged. Scope-correct route/model,
   connection and provider health persistence is implemented and tested.
   Add specialized
   extractors only for shapes not expressible through current typed options.
4. **M7 OAuth/provider acceptance:** the RFC 8628 `slow_down` interval and
   daemon shutdown during active polling now have integration evidence. Complete
   the remaining denial/expiry/shutdown race matrix, HTTPS/non-loopback callback
   integrations and post-commit route/health reconciliation acceptance. Device,
   code and refresh now share durable credential revision fences; deterministic
   fixtures cover replacement, disable/re-enable and recreated IDs. Add real
   provider OAuth definitions only from captured/source-verified contracts;
   live credential acceptance requires
   user authorization. Add generic signed-request `Apply` only for auth schemes
   that cannot be represented by the now-shared typed header presentation.
5. **M6 protocol conformance:** broaden Gemini end-to-end coverage to additional
   SDK-shaped signature/history cases and modality eligibility; validate
   reasoning option support against model families. Add Antigravity only from
   observed wire fixtures, not by assuming it is ordinary Gemini.
6. **M9 provider-report completion:** bind observed provider quota/plan reports
   through the quota operation and its shared JSON/header extraction contract;
   add representative fixtures for auth semantics and report authority. These
   are quota-report capabilities, not a separate generic `usage` operation.
   The shared window contract distinguishes enforced limits from observational
   billing balances; the Vercel `/credits` report is stored as `observe`, not
   mistaken for a hard cap. Provider-neutral `requestHeaders` binds public
   protocol metadata, covering the observed Anthropic OAuth usage headers while
   keeping credential headers in connection auth. Quota endpoint 401/403/429 failures now emit
   sanitized auth/permission/rate-limit codes to daemon logs without poisoning
   inference health; partial `keep_available` report failures are also visible.
   Keep unsupported operations absent rather than routing them to a default.

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
introduce provider-specific core workflows. C1's catalog, exact module pins
and static-link resolution contract are in place. The 2026-10-22 audit reopens
the semantic-envelope part of C1: registered usage dimensions are absent, and
typed operation-result data needs a proper projector path rather than the jobs
example's JSON-in-text transport. C2–C6 remain in progress.

1. **C1 — catalog complete; semantic envelope not closed:** the local-schema,
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
   now composed through that catalog. Bundle v9 embeds provider definitions
   only when manifest auth/default/endpoint/codec fields are provably
   secret-free and persists them for daemon-restart activation. Opaque or
   sensitive definitions remain exact external module dependencies; an
   unresolved definition is retained as inert setup only when no model route or
   Physical source uses it. The runtime registry and daemon now accept
   statically linked provider modules at the composition root. Dependency
   lock v2 pins each reachable module's exact opaque build and host-contract
   versions; executable code remains statically linked. Public
   `pkg/provider` and `pkg/daemon` APIs let a separate Go module register a
   non-chat task and client ingress, bind a JSON manifest to shared primitives,
   and host the daemon; `make test-external-provider-module` proves a
   schema-validated request through that boundary with no kernel or
   storage-schema change.
   Provider definitions resolve by exact ref and may run side-by-side under
   one ID; provider-node storage, route selection and bundle v9 preserve that
   exact ref. `TestProviderDefinitionVersionsBindSideBySideByExactRef` and
   `TestBundlePreservesSideBySideProviderDefinitionVersions` prove the v1/v2
   path. Module resolution is deliberately build-time: consumers pin the
   module as a normal Go dependency, statically link it through the composition
   root and deploy that host binary; config bundles never fetch or execute
   code. This avoids a second plugin loader/package manager and keeps missing
   implementations fail-closed. C6 still needs the full SG1–SG7 integrated
   proof.
2. **C2 — full compatibility planning (in progress):** the kernel now asks
   adapters for a request/route/operation-scoped `CompatibilityPlan` and
   recomputes admission from immutable facet declarations rather than trusting
   a boolean. Ordered facet composition, fail-closed required facets, named
   loss grants/denials and fidelity aggregation are implemented; the composed
   adapter now combines request-codec declarations with the response-wire
   facet and composes decoder event declarations with renderer event coverage.
   OpenAI Chat, OpenAI Responses, Anthropic Messages and Gemini declare bounded
   request facets; the response decoders enumerate event families and renderers
   must declare each request-required family. Reasoning intent and numeric token
   budget are separate required facets; Anthropic output-limit inflation is a
   named loss that needs an explicit model policy grant. Missing text/completion/tool/
   thinking coverage now fails admission. Active semantic transforms disable
   native wire passthrough; OpenAI Chat re-encodes semantic events while raw
   passthrough renderers reject the plan. An Anthropic Messages semantic egress
   renderer now maps text/tool/usage and issuer-signed thinking; missing or
   transform-invalid signatures reject admission. Fake OpenAI Chat and
   Responses decoder→Anthropic renderer fixtures prove JSON/SSE cross-wire
   verticals. Physical/Combo nodes persist named loss grants/denials that flow
   down the model path; denial wins and admitted lossy plans are recorded in
   usage. Repeatable CLI `--allow-loss`/`--deny-loss` and portable bundle v9
   carry the policy. A server allow/deny ceiling is managed through the CLI and
   SQLite-backed snapshot. Loss records retain requested/effective values,
   semantic paths and granting model nodes. Candidate plans now include the
   ordered request/response transform chains and authorized operation-to-
   provider artifact handoffs. The selected payload-free plan summary is now
   persisted with successful usage, returned through `usage.list` and shown in
   the TUI Usage inspector. `routes.explain` now accepts a normalized request
   sample including exact non-chat operation refs and payloads, then evaluates
   every enabled model-path candidate through the same
   compatibility planner without strategy-state mutation, credential lookup
   or upstream dispatch; CLI accepts a JSON file or stdin. Selected fallback
   transform accounting is now covered by
   `TestFallbackCompatibilityPlanAndUsageContainOnlySelectedTransforms`: exact
   selected-scope steps appear once, skipped/rejected scopes do not leak into
   the plan or usage, and attempt-local payloads remain independent. Remaining
   SG1/SG4 acceptance must be audited as concrete contract cases, not an
   unspecified universal provider matrix.

   Continuity scope checkpoint (2026-10-21): host-v7 session stores include
   stable principal and configured endpoint alongside the exact provider and
   operation refs, upstream model and client session. Existing decoder hooks
   stage private updates, propagate expiry and explicit reset, and reject failed
   decoder state. Transactional SQLite writes fence replaced authorities;
   unsupported cache namespaces are discarded, not adapted. The independent
   module Responses fixture covers failure/expiry/reset, renewal, account and
   endpoint replacement, and durable restart with three race runs. Real-provider
   expiry/reset mappings and classified continuity-error recovery remain
   implementation gates; this checkpoint does not close C2/SG1/M15.
   Installed `8165145` passed three further independent-module race runs with
   the real CLI account-update command; main daemon control=serving=484 and
   bundle-v9 validation remained valid. Config and credentials were preserved.

   Continuity rejection checkpoint (2026-10-21): host-v8 adds required
   compare-and-invalidate session stores and shared typed error selectors.
   Configured HTTP rejection uses request scope and cannot become auth recovery,
   quota suppression, fallback or replay. Acknowledged worker tombstones survive
   restart; CAS preserves newer success. The public daemon Responses vertical
   now injects source-backed parent-not-found JSON and verifies sanitized 409,
   no implicit retry, and durable reset. Automatic full-history reconstruction,
   provider-specific in-stream rejection and broader SG1 evidence remain open.
   Root regression/vet, three focused race runs and full conformance passed,
   including store-version protection for identical resaves. Installed
   `039e971` passed three independent-module race runs with real CLI status and
   account update; main control=serving=485 and bundle-v9 validation stayed valid.

   Accepted-response checkpoint (2026-10-21): host-v9 carries bounded private
   decoder errors to an operation-selected response classifier without fake
   HTTP statuses. Response reset signals are separate from HTTP 4xx rules and
   retain accepted effects/no replay. The compositor strips its host hook from
   decoder hooks and rejects local-error spoofing. OpenAI error envelopes are
   intercepted before raw data passthrough; committed protocol renderers emit
   one terminal error, while incomplete/failed responses cannot complete via
   `[DONE]`. The independent daemon vertical checks pre/post-commit failures,
   retained failed usage and durable reset after a new stream response ID.
   Root regression/vet and three focused race runs passed. Broader provider
   envelopes, full-history reconstruction and whole-product gates remain open.
   Full conformance also passed. Before closing SG1/SG6, audit typed client-error
   codes and stronger provider terminal-effect evidence separately from HTTP
   acceptance: this slice never turns a 2xx error into a replay grant and is not
   proof of automatic harness recovery.
   Installed `43c3d24` passed three further independent-module race runs with
   real CLI status/account update, committed error projection and durable failed
   usage/reset across restart. Main control=serving=486 and bundle-v9 validation
   remained valid; no live provider or real credential mutation was needed.

   Typed client-failure checkpoint (2026-10-21): host-v10 replaces the event
   error string with a bounded code/type/message value. HTTP and committed
   streaming projection preserve the descriptor; standard error wrapping,
   budgets, operation envelopes and transform guards retain its identity.
   Classifier/module metadata cannot grant replay, change effect or bypass
   invalidation validation. The independent daemon vertical compares descriptor
   identity across HTTP and SSE. Harness-specific automatic recovery remains
   unverified; no local standalone harness source was available.
   Root regression/vet, three focused core/API/renderer race runs and full
   conformance passed. SG6 follow-up is now a finite boundary/drain checklist in
   M15, not a demand for a universal provider matrix.
   Installed `25184ac` passed three additional independent-module race runs
   using the real CLI, including HTTP/SSE code/type/message identity. Main
   control=serving=487 and bundle-v9 validation remained valid; real config and
   credentials were preserved. Next: scoped-key cancellation/dispatch/drain
   proofs from the finite SG6 checklist.
   Non-OpenAI terminal-envelope checkpoint (2026-10-21): Anthropic and Gemini
   decoders now implement the existing bounded private-error contract for JSON
   and SSE, preserving known failed usage and rejecting false terminal success.
   Anthropic incomplete streams fail closed; Gemini no-candidate handling no
   longer hides explicit errors or erases earlier usage. A real daemon vertical
   drives both built-in codec families through configured test-only classifier
   reasons, HTTP 409/committed Chat errors, follow-up success and failed usage
   after restart. No kernel, host contract or dependency changed. Root
   regression/vet, three decoder race runs and full conformance passed; remaining
   SG1 breadth and whole-product gates stay open. Installed `bd007fa` passed
   three further module race runs with the real CLI for both protocols. Main
   control=serving=489 and bundle-v9 validation stayed valid; real configuration
   and credentials were preserved.
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
   similarly checked. Request safe-fail-open requires exact descriptor
   permission and records bounded reason codes; protected identity, artifacts
   and cancellation remain fail-closed. Response safe-fail-open is permitted
   only before renderer output starts. Response faults now carry typed origin
   evidence: local transforms/rendering and client delivery do not penalize a
   provider. Callback failures latch even when a decoder ignores them; original
   and transformed event budgets are separate. Caller cancellation is neutral,
   while an explicitly marked operation deadline in provider work records an
   operation timeout. Completion is staged through rendering and final result/
   artifact validation and cleanup before publishing success. Known provider
   usage survives local failure/cancellation with error/cancelled status; it
   does not become success/performance feedback or a replay grant. The actual
   concurrent HTTP disconnect fixture additionally checks neutral health.
   Remaining: complete opaque artifact/effect coverage, broader transform
   resource/commit/cancellation permutations and provider-specific wire cases;
   schema-driven TUI binding forms already have catalog-backed fixtures.
4. **C4 — daemon auth coordinator (in progress):** authorization-code IPC owns
   state/S256 PKCE, bounded expiry/exchange, callback validation, cancellation
   and replay rejection. RFC 8628 uses `x/oauth2` DeviceAuth/DeviceAccessToken;
   a daemon-owned, cancellable worker handles pending/slow-down/expiry without
   a connected frontend. CLI and TUI expose only public actions; device codes
   remain daemon-private. Credential compare-and-swap fences delayed auth and
   refresh against durable private revisions, including same-secret resaves and
   A→B→A changes; an in-flight authorization-code exchange is
   tested against a concurrent credential update. The loopback callback
   listener is exercised through HTTP and the TUI projects its status. Denial,
   expiry, device slow-down and shutdown during device polling have lifecycle
   evidence. `TestAuthorizationCancellationFencesLateSuccessfulToken` additionally
   covers late-success drivers after caller/session/daemon cancellation, code
   expiry, coordinator admission closure and worker draining for both flows.
   Credential commit checks live session/context/expiry and serializes against
   explicit cancellation as well as credential-generation changes. Device
   acquisition itself is tracked and cancelled on daemon shutdown, not only
   the subsequent poll. Per-connection waits use context-aware `x/sync/semaphore`;
   Resolve/Refresh propagate caller/daemon cancellation, and concurrent refresh
   callers share one persisted replacement. Public optional auth types have an
   external-module device-race fixture. Code/device orchestration now converges
   on the generic action engine. Kernel inference attempts use opaque scoped
   credential leases, with a live metadata fence after preparation/before each
   dispatch, refresh replacement and release. Quota polling/opportunistic reads
   now use exact consumer refs and guarded per-report transport dispatch;
   revision-fenced partial results are discarded. Detached enrichment is
   bounded by `x/sync/semaphore` and canceled/drained by daemon lifetime.
   Discovery now uses an exact model-source consumer ref and guards each HTTP
   page; connection probes use operation leases before dispatch. Import checks
   binding revision/owner and provider endpoint in the same immediate SQL
   transaction as catalog, evidence and invalid-model health updates. Shared
   transaction helpers preserve static assignment and incomplete-list semantics.
   Generic TUI input now uses exact registered schemas, masked local forms,
   asynchronous compilation, revision/action-bound submit and stale-reply/error
   fencing. Close clears the draft without ending daemon setup; explicit cancel
   and multi-step rejection/activation have real Unix IPC fixtures.
   Explicit `new`/`resume` admission now reattaches a fresh frontend by connection
   without another provider Begin. Context-cancellable observation waits for
   unpublished/submitted steps, not daemon-owned await, and validates the live
   credential binding before reuse. Resource→callback→replacement fault
   composition and daemon-restart/new-session distinctions have fixtures.
   An optional private loopback receiver now accepts callbacks for one explicitly
   configured public HTTPS origin behind local TLS termination. It uses the
   same grant/revision/CAS engine, is reserved before IPC startup and does not
   trust forwarded headers or expose data/admin routes. HTTPS code setup uses
   resource readiness before consent, or manual mode only when optional.
   Remaining: broader lifecycle/fault composition and live deployment/provider
   acceptance. Prepared request signing now
   has a public frozen-body/renewable-authorization
   seam with an external COSY fixture; provider-specific signer implementations
   remain auth-driver work. Validate full lifecycle fixtures, not token-only
   stubs. Installed checkpoint `a869763` covers the prepared-request revision
   fence through three independent-module race runs using the installed CLI;
   main control=serving=477 and bundle-v9 validation remain healthy.

   Quota checkpoint `8a76e34` adds consumer-ref authority, per-report dispatch
   fencing, stale partial-result rejection and bounded cancel/drain. Full tests,
   vet/conformance and repeated race fixtures passed; installed CLI connection
   replacement and `quota` reads passed against the independent module host.
   Main control=serving=478 and bundle-v9 validation passed after installation.

   Discovery/probe checkpoint `172a4a5` converges model-list and probe credential
   consumers on the daemon issuer and atomically fences catalog
   publication. Full tests/vet/conformance and repeated root/external race cases
   passed. Five public-module lease/renewal verticals passed three installed-CLI
   race runs after installation; main control=serving=479, bundle-v9 valid.

   TUI action-input checkpoint `b635d42` adds schema-bound masked editing,
   asynchronous compilation, option/action selection and stale-reply/error
   fencing. Regression/vet/conformance and five focused race runs passed.
   Installed CLI completed a session after TUI editor closure in three race
   runs; the default TUI passed PTY launch/exit smoke. Main control=serving=480
   and bundle-v9 validation passed. Full C4/SG2 fault composition remains open.

   Frontend reattach checkpoint `be0ea67` adds typed new/resume admission and
   observer-only cancellation without another Begin. Callback resource/manual-
   replacement and stale-owner/restart cases have composed fixtures. Full tests,
   vet/conformance and repeated race runs passed. Installed CLI `--resume` plus
   TUI fresh-model/handoff each passed three race runs; main control=serving=481,
   bundle-v9 valid. The broader C4/SG2 transport/fault gate remains open.

   HTTPS ingress checkpoint `17def5b` adds opt-in public-origin/private-loopback
   receiver policy on the same grant engine; host-v6 code drivers require
   readiness before consent. Full tests/vet/conformance and repeated root/TLS
   races passed. Installed CLI HTTPS, loopback and device verticals each passed
   three race runs; main control=serving=482, bundle-v9 valid. Main ingress was
   not enabled. Remaining C4/SG2 composition/fault and live-deployment gates are
   tracked separately from this deterministic receiver proof.

   Auth-owner fencing is implemented: state/revision/exact node-definition owner
   are read together, pinned route mode/auth bindings are checked before the
   driver, and final SQL CAS checks ownership. Definition ID/version changes
   transactionally rotate child revisions, including ABA; provider-node renames
   and unchanged definition values do not. Serving, quota, discovery and probes
   share one issuer; source services do not resolve durable credential state.
   Unit fixtures cover stale route/code/device/refresh rejection, forged owner,
   rollback and unrelated-node isolation; a public daemon fixture rejects old
   setup after version rebind/ABA, then accepts a fresh current-owner setup and
   executes an operation. This closes the identified rebind slice, not all
   credential leases, universal setup actions or post-commit reconciliation.

   Credential activation is implemented as a transactional per-connection outbox
   written with CAS. Refresh wakes the daemon worker and returns its replacement
   without routing reload on the inference critical path; code/device retain
   successful credential completion plus explicit pending/active activation.
   Typed reload receipts distinguish control publication, persisted version and
   actual serving snapshot. `/models` projects that serving snapshot, and status
   exposes separate control/serving versions. Fenced resets/progress, restart
   recovery, five-attempt budgets, idle-timer suppression, CLI list/get/retry and
   TUI pending/exhausted readiness are implemented. Fault fixtures cover reset
   failure, reload failure before/after publication, durable commit/status and
   HTTP 401 refresh followed by explicit retry or restart without another exchange.
   Session continuity is scoped by connection/exact definition/operation/model/
   client session, not token revision; preserve that intentional refresh behavior
   while retaining cross-owner isolation. The identified activation split is
   closed for refresh/code/device; universal setup actions, credential leases,
   broader fault composition and live-provider acceptance remain separate gates.

   Follow-up auth semantics: token renewal and configured principal replacement
   now have typed transitions, independent durable epochs and separate auth versus
   operational health lanes. CLIProxyAPI's `UpdateRefreshedAuth`/`updateInternal`
   in `sdk/cliproxy/auth/conductor_lifecycle.go` separates refresh/replacement,
   fences stale refresh and preserves unexpired `credential_quota` evidence.
   Gobroom reuses its own SQLite CAS/outbox and scoped persistence primitives
   for these semantics rather than importing that manager and its runtime graph.
   The recovery trigger slice is implemented:
   normalized auth recovery plus explicit mode opt-in replaces kernel 401/403
   checks, and replay/issuer safety remains independent. Generic structured
   classifier options plus a public-module HTTP498 fixture prove recovery without
   provider kernel branches; 403 permission/quota and recovery-disabled 401 do not
   refresh. Host contract v3 rejects old recovery semantics. Store/runtime/daemon
   fixtures and the separate-module renewal vertical preserve exhausted quota
   with unknown reset, active rate-limit cooldown and queued operational evidence
   through renewal and restart; replacement invalidates the former principal.
   New-credential auth failures survive delayed renewal activation. This closes
   that narrow continuity gate, not C4/SG2/M15: generic setup actions, credential
   leases, provider-reported identity and broader fault/live acceptance remain.

   The action-driver vertical is now implemented through public
   `AuthorizationFlow`/`AuthorizationTransition`, exact registered input schemas,
   redacted provider state and shared connection/session/CAS/activation ownership.
   Unix IPC and `connections setup start/get/submit/cancel` cover manual proof,
   OTP-style steps and daemon-owned polling without frontend activity. Revisions
   fence stale action replay; session expiry cannot extend, late driver success
   cannot commit after cancellation/shutdown, and intermediate state is never a
   public credential. A separate module starts from an empty credential, proves
   schema/stale rejection, polls without get calls, activates, restarts and then
   consumes the provider endpoint through Physical/Combo. Generic control JSON
   preserves exact numbers; control consumers bind typed limits rather than
   assuming float64. Remaining: migrate code/device/callback setup onto this same
   action engine and remove their special coordinators/views, generic TUI action
   forms, opaque credential leases and the complete SG2 composition/fault matrix.
   No compatibility action decoder or duplicate provider-specific setup kernel
   is a final-state solution. C4 remains in progress.

   Callback ownership now has a shared grant registry. Action drivers bind a
   private fixed-loopback target/correlation/response shape to a schema-backed
   input action; delivery pins the session revision and reuses the ordinary
   advance/CAS/activation path. The previous OAuth-specific listener runtime was
   removed, and its code-flow handler registers through the same registry. Socket
   acquisition is real ownership, owner replacement is atomic, required failures
   roll back, optional failures expose manual mode, and terminal/shutdown paths
   revoke grants. Fixtures cover strict/ambiguous dispatch, shared sockets, expiry
   cleanup, cancel versus an admitted callback and a channel-based wait for a
   published step. A separate module uses non-OAuth `ticket`/`proof` fields,
   automatic and manual callbacks, denial, restart and real HTTP inference.
   Remaining: migrating the code/device coordinators and clients to actions,
   TUI forms, opaque leases and
   full SG2 composition/live-provider evidence. This is not full auth closure.

   Daemon-assigned callback URLs now use a private two-phase preparation contract.
   A port-zero intent reserves an actual socket, then delivers typed
   `callbacks_ready` resources to the same admitted driver before consent actions
   are published. Activation preserves the exact granted target/action/correlation/
   response policy and transfers ownership without rebind. Preparation cannot
   extend expiry, is bounded to eight rounds, and releases reservations on failure,
   cancellation, terminal output or shutdown. The external module's issuer probes
   a matching callback during registration and receives rejection before activation;
   a blocked registration does not stall another account or status. Both assigned
   accounts activate and HTTP inference works through exposed Physical/Combo after
   restart. This closes the dynamic-URL ordering slice, not code/device convergence,
   TUI editing, credential leases or full C4/SG2.

   Device orchestration now converges on the generic setup engine. A reusable
   provider strategy composes device begin/await hooks, keeps the complete bounded
   response/private metadata opaque and emits public code/URL actions with typed
   `Await` intent. The daemon owns queued and admitted background work, root
   context values, expiry, single-flight advancement, CAS and activation. RFC 8628
   polling remains in pinned `x/oauth2`, including pending/slow-down. The old device
   coordinator, session fields, optional interface, capability metadata, IPC methods
   and three CLI commands are removed, not adapted. TUI `D` uses the same typed
   setup view as CLI, including activation readiness; code capability still works
   beside device mode. Host contract v4 rejects removed lifecycle contracts.
   Generic waits use their provider deadline under a one-hour host ceiling rather
   than the short RPC timeout. Migrated generation/owner/activation/cancel/drain
   fixtures and virtual-time long-wait tests retain the previous acceptance scope.
   Remaining: authorization-code coordinator convergence, inline TUI input editor,
   credential leases, live OAuth acceptance and full C4/SG2 composition.

   Code orchestration now also converges: the daemon has one generic session
   state and no OAuth state/verifier/redirect parser or code capability branch.
   The old code coordinator, IPC and CLI commands are removed without aliases.
   `NewAuthorizationCodeDriver` owns typed private state, PKCE/nonces through the
   pinned OAuth library, exact callback/code input validation and provider exchange
   hooks. Fixed and dynamic local callbacks prepare resources before consent;
   remote HTTPS remains manual. A typed recoverable input rejection preserves
   the challenge without exchanging or exposing input; fatal driver/owner/context
   errors terminate normally. The factory offers a registered-schema method
   choice when code/device coexist, or `setupMode` selects directly. Host v5
   declares removal of the specialized interfaces/metadata. All clients consume
   `auth.setup.*`; lifecycle assertions for state, replay, denial, expiry, owner/
   generation, activation and drain remain. Independent-module opaque credential
   exchange/restart inference is covered. Remaining C4/SG2 gates are inline TUI
   input, credential leases, live acceptance and complete fault composition;
   convergence alone does not complete the milestone or product.
5. **C5 — attempt replay and operation execution (in progress):** operation
   replay declarations now reach the kernel attempt executor. Ambiguous transport
   failure and accepted-response decode/render failure stop instead of trying a
   second upstream route; `confirmed_rejection_only` may continue after an
   explicitly classified rejection such as 429. Missing effect evidence is
   unsafe. `scoped_idempotency_key` now requires a validated client key plus a
   provider-operation header binding; the kernel derives a deterministic key
   scoped to provider definition, node/endpoint, credential, protocol, upstream
   model and operation, then permits one same-route replay for ambiguous effects
   only. Provider bindings now require a typed header plus guaranteed minimum
   retention; the window is pinned to the first dispatch and checked again
   after preparation/refresh. Virtual-time fixtures prove expiry after a slow
   response, transport error, retry preparation or credential refresh causes
   no redispatch or cross-issuer fallback. Cancellation after preparation also
   prevents dispatch when the codec ignored its context. It never grants
   cross-issuer fallback. A separate Go-module audio
   fixture now executes both JSON and bounded multipart non-chat operations
   through module-owned ingress, assigned model, Physical/Combo graph, shared
   multipart provider codec, decoder and client renderer. Remaining: real
   provider idempotency guarantees on future production opt-ins; the generic
   commit/cancellation boundaries now have finite SG6 evidence.
   Scoped retry now requires a prepared replay factory instead of rerunning
   the semantic codec. The byte helper freezes encoded payload/headers and
   renews authorization through an independent body reader. Multipart retains
   its boundary and frozen typed input while reopening authorized replayable
   leases; an end-to-end kernel fixture checks identical key/wire bytes after
   ambiguous transport failure. An external module re-signs identical encoded
   bytes through the resolved endpoint without changing kernel code. Missing
   factories and changed actions/framing fail closed. Factory invariants remain
   obligations of trusted module code, not an in-process security sandbox.
   Signers receive the derived dispatch header before authorization. The shared
   HTTP transport no longer follows redirects or implicitly rewinds requests
   behind the kernel's attempt policy; a reused-connection fixture covers even
   an empty-body POST carrying a standard idempotency key.
   The separate `JobModule` now registers create/status/cancel operations, their
   typed schemas, codecs, client ingress and renderer. A connection-scoped inline
   output handle is disclosed without widening its consumption scope; it retains
   the exact issuer/connection and TTL after response cleanup and daemon restart.
   Its real daemon/HTTP fixture proves keyed create survives a lost response
   without a second job, wrong role/expiry/issuer fail before upstream dispatch,
   cancellation with a lost response is not repeated, and explicit status reads
   reconcile cancelled or completed jobs. No daemon poller/job database is added.
   Operation-aware feedback now carries exact refs through typed breaker/quota
   identities, eligibility/ranking, half-open trials, SQLite, asynchronous writes
   and durable spools. The job fixture no longer waits for cancel cooldown before
   status: the two actions are independent across restart. Operation-qualified
   rate/quota windows can suppress create while status remains usable; an eligible
   different account can still be selected. Shared account/provider evidence
   remains broad. Comparable workload statistics and affinity are partitioned
   by operation/version, and CLI/TUI project the operation-level state.
   SG6 final-dispatch/drain checkpoint (2026-10-21): cancellation inside final
   live authority validation stops both initial and replay dispatch. Retention
   is now checked after that potentially blocking validation; a virtual-time
   regression reproduced duplicate dispatch before the fix. Accepted pre/post-
   client-commit cancellation tests prove no retry/fallback, cleanup and failed
   usage. An independent daemon module proves Stop cancels and drains a replay
   factory that returns success late, never redispatches, and releases resources
   for reopening. Root regression/vet and repeated core/module races passed;
   this closes the finite generic SG6 boundary checklist, not real provider
   retention guarantees or all of M15.
   Full conformance passed; the shutdown fixture also proves the actual old
   data listener can be rebound after cleanup. Installed `3b7b34d` passed three
   further module race runs using real CLI status/shutdown during held replay.
   Main control=serving=488 and bundle-v9 validation stayed valid; no real config
   or credential mutation was needed.
6. **C6 — integrated extension proof (in progress):** a separate Go module
   compiles against public provider/daemon APIs, adds an audio task/codec and
   versioned JSON/multipart ingress, loads its manifest, builds an assigned
   route plus Physical/Combo graph and executes schema-validated JSON and
   multipart artifact operations over ordinary IPC/daemon/HTTP boundaries. It
   also binds a custom AuthFactory, passes
   bounded credential AuthContext to an external request codec, and includes a
   source-derived Qoder COSY fixture that encodes then signs its body bytes and
   unwraps an SSE envelope through the shared runtime. This leaves kernel graph
   types, core schema and generic frontend workflow unchanged. Host lifecycle
   fixtures additionally prove synchronous HTTP port reservation, rollback on
   data/control/IPC startup failure, immediate use of allocated port-zero
   addresses, duplicate-Start rejection and listener release after Stop.
   Unix IPC now uses `gofrs/flock` for process ownership before database
   initialization, preserves active/non-socket paths, recovers crash residue
   and retains the lock through worker/store cleanup. Real subprocess fixtures
   verify contention and forced-crash recovery; status responses, rather than
   socket existence, gate installation readiness. IPC now uses Go's HTTP/JSON
   transport on the Unix socket: client cancellation reaches handlers, partial
   connections unblock on Stop and a shared generation drain completes before
   store/ownership cleanup. Acknowledged CLI shutdown also wakes daemon Wait.
   Complete the full SG1–SG7 compatibility, auth, artifact, transform, replay,
   reload and bundle cases through that module, then run installed CLI/control
   acceptance.

Completion of C1–C6 proves runtime architecture closure for the declared
scope. It does not change M2/M6/M7/M8/M9 feature completeness automatically;
update those milestones only against their own exit criteria. Secondary
operation fixtures prove architecture without committing to ship every media
product feature. No legacy compatibility path is retained during these changes.

## Configuration isolation and restart checkpoint

The SG7 finite checklist now joins catalog/schema validation, config apply,
persisted export, control/serving versions and active inference. Supported
invalid dependency/ref/options cases reject before mutation; accepted reload
pinning remains a separate verified boundary. A real-store privacy fixture
preserves configuration while excluding valid live continuity/signature state
and operational health/auth/quota/usage on fresh import.

CLI validate/diff/apply now preserve exact numbers and enforce one bounded JSON
object. Durable preload uses the standard heap, validates the selected state,
keeps newest unique scoped rows, and caps provider expiry at persisted last-use
plus TTL. The existing public-module expiry/reset fixture is reused rather
than treating generic expiry as a new missing mechanism.

Regression found and fixed a separate lifecycle defect: usage cancellation
could discard buffered accepted events and race repository close. The daemon
now closes the producer and joins the worker. Unit blocking-save and real
128-row shutdown/reopen fixtures cover drain ownership. Still open operational
work: whole-drain deadline under blocked storage and durable/reported handling
of failed writes or admission overflow. Do not infer those guarantees from
successful SQLite drain. Other semantic gates and C1–C6/M15 audit remain open.

Further verification found two concrete boundary defects: usage send/close
required a shared `sync.RWMutex`, not just an atomic closed flag; an expired
provider session update needed a valid queued clear marker, otherwise SQLite's
rejection preserved an older unexpired parent for restart. The cache regression
reproduced that resurrection before the fix; the public module fixture now
restarts at the expiry boundary. The activation fault injector also configures
SQLite's bounded busy timeout instead of racing live persistence with DDL.

Installed `cbbf00d`: final full regression/vet/conformance passed. Focused
config/preload/privacy/drain races and three installed-CLI repetitions passed;
three independent-module installed repetitions also covered activation recovery
and graceful-restart expiry. Main control=serving=495; bundle v9 remained valid.
Finite SG7 configuration/isolation boundaries are verified. Continue with the
explicit storage-failure/whole-drain obligations and a requirement-by-requirement
C1–C6 audit; do not substitute broad live-provider parity for core conformance.

## Transform ownership and lifecycle checkpoint

The public SDK and CLI now expose the existing transform contracts and typed
binding CRUD (`a10c29e`). Request operation-input/canonical-content fixtures
prove configured updates, restart and removal through the ordinary daemon.
SG5 verification now includes response JSON/SSE lifecycle and original provider
usage accounting, plus regression guards for deep artifact/usage ownership and
immutable operation versions. Semantic Chat streaming honors typed client usage
opt-in; it does not project modified client counters into provider accounting.
The finite core checklist in
[M15 conformance](M15_CONFORMANCE.md#sg5-finite-core-checklist) replaces an
unspecified demand for a universal transform/cancellation matrix.

These changes enforce existing contracts with standard typed copies and
existing clone helpers; they do not introduce a second transform engine,
provider-specific kernel branches, new dependencies or a compatibility path.
Full C1–C6/M15 acceptance and the product-level performance gate remain separate.

Verified `5aab43c`: full regression/vet/conformance, focused boundary target and
three full kernel/normalizer/renderer race runs passed. Three external transform
lifecycle race runs passed before install and three more after
`make install-all` with installed CLI CRUD. Main control=serving=493 and valid
bundle v9. The finite generic SG5 checklist is verified; continue with explicit
remaining SG1–SG4/SG7 requirements and the C1–C6 audit, rather than reopening
an undefined transform matrix. Live-provider feature breadth and isolated
RSS/tail-latency acceptance are not inferred from these deterministic fixtures.

## Measured performance checkpoint

`make perf` now runs the standard Go benchmark fixture against direct and daemon
JSON/SSE endpoints, serially and with `RunParallel`. The 2026-10-21 CPU/heap
profile identified SQLite prepare/parser work and allocations in health-key
traversal and compatibility composition. Fixed `database/sql.Stmt` instances,
standard `iter.Seq` traversal and owned planner preallocation reduce measured
fixture allocated bytes about 12% JSON / 9% SSE without caching credentials or
skipping schema, loss-policy or generation validation. Scope, numbers and
rerun/profile commands live in [Operations](OPERATIONS.md#local-performance-baseline).

Root regression/vet, three full store/runtime/kernel race runs and full
conformance passed. Installed `2438b7d` passed three further module race runs
with CLI status and activation retry/restart; main control=serving=491 and
bundle-v9 validation remained valid. Real configuration and credentials were
preserved; `make perf` was verified against fixture-only upstreams.

This is an initial baseline, not the near-zero resource acceptance gate. Isolated
idle/loaded RSS, sustained/streaming tail latency, larger catalogs and longer
load/reload/cancellation tests remain required before a product-level claim.

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

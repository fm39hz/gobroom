# Implementation roadmap

Status is based on the repository implementation and tests inspected on
2026-09-22. This is the project roadmap and the source of truth for milestone
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
[Physical models](PHYSICAL_MODELS.md).

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
| M0 | Source-grounded behavior inventory and model/prefix contracts | Partial | Keep compatibility claims evidence-based; finish fixtures that affect intended Chat/model workflows. Exhaustive recreation of unrelated behavior is not a prerequisite. |
| M1 | SQLite control plane and immutable routing snapshot | Done | Typed config CRUD, validation/reload, route expansion, IPC and immutable snapshots are authoritative. Legacy publication/alias tables and APIs were removed; storage portability is separate. |
| M2 | Provider onboarding and model-management UX | Partial | The TUI implements the LazyGit-style block/tab/context skeleton: `h/l` blocks, `j/k` items, `[/]` tabs, Enter/Esc depth, Space selection and context-local `/` filtering. Discovered, Physical and Combos are separately managed typed tabs with inline `discoverable` exposure. Usage, Logs, reverse references and Physical effective projections are now visible; split member/candidate editor, richer capability filters and inline effective-order explanation remain. See [TUI UX](TUI_UX.md). |
| M3 | OpenAI Chat request vertical slice | Partial | Kernel/adapters and live provider calls exist. Close cancellation, pre/post-commit error fixtures, load behavior and end-to-end usage/health certification. |
| M4 | Hierarchical model execution, connection pool and policy primitives | Partial; runtime core done | Typed Physical/Combo nodes preserve policy boundaries; scoped passive feedback, adaptive ranking, session affinity and half-open admission are implemented. Finish typed eligibility, sticky/fusion semantics, retry precedence and deterministic concurrency fixtures. See [Passive health](PASSIVE_HEALTH_ROUTING.md). |
| M5 | Canonical response-event contract | Partial; semantic stream foundation done | Kernel defines canonical start/content-block/text/thinking/tool/usage/completed/error events and stream hooks. Anthropic and OpenAI Chat/Responses SSE observers emit semantic events while preserving upstream output; malformed JSON, continuity metadata and output-item boundaries are covered. Finish richer renderer boundaries. |
| M6 | Responses/Anthropic semantic compatibility | Partial; reasoning foundation started | Normalized reasoning distinguishes absent `inherit` from explicit modes; layered precedence is defined and Physical/Combo defaults persist into kernel nodes. Anthropic uses real `thinking`/`output_config.effort`, OpenAI Responses uses native `reasoning`, and Gemini has a registered adapter/manifest with `thinkingConfig`. Finish richer persisted presets, continuity/output-item fixtures and full content-block/tool/thinking usage parity. |
| M7 | Upstream credential lifecycle and OAuth | Partial; generic lifecycle done | Static API-key/Bearer resolution, typed OAuth token parsing, refresh-token flow, persisted refreshed token state, serialized refresh-once after 401/403 and refresh locking are implemented. Bind provider-specific OAuth configs/flows and add end-to-end refresh-race fixtures. This is distinct from client auth to hosted GoBroom. |
| M8 | Passive health, limits, performance and usage | Done (runtime core) | Typed outcomes, scoped route/connection/provider evidence, reset-aware cooldowns, bounded feedback ranking, TTFT/throughput, request-class EWMA, half-open trials, session affinity, durable health/usage IPC/TUI, usage summaries, route-order explanation, explicit retention pruning, bounded daemon log ring and logs IPC/TUI are implemented. |
| M9 | Provider presets and discovery expansion | Partial | JSON manifests and reusable primitives support generic compositions. Add desired providers/operations; avoid provider-specific kernel code or breadth-only checklists. |
| M10 | Physical identity, capability and modality policies | Done (physical core) | Typed support states, request-requirement compilation, profile persistence, typed TokenLimits, Physical identity/revision fields, source fidelity/evidence persistence, declared/guaranteed/available projections, projection source explanations and Physical IPC/TUI display are implemented and tested. Cross-protocol reasoning translation remains intentionally in M6. |
| M11 | Optional middleware | Planned/deferred | Not on the critical path. Revisit only for demonstrated need; keep opt-in, bounded and unable to mutate route identity or bypass cancellation. |
| M12 | Secondary APIs and integrations | Out of core | Embeddings/media/search, tunnels, MITM/DNS and IDE integrations remain separate services/sidecars, not kernel milestones. |
| M13 | Canonical configuration bundle and sync | Done (single-writer core) | Versioned secret-free typed bundle export, validation, diff/dry-run and atomic apply/import are implemented. Existing connection secrets are preserved by stable connection ID and never exported. Multi-writer conflict-free sync is intentionally not promised. |
| M14 | Portable operations and remote hosting | Partial | Explicit paths/listeners exist, control HTTP remains disabled by default, bounded logs/retention are available, and optional bearer auth now protects the data plane. Finish structured journal guidance, TLS/reverse-proxy contract, storage interface and alternate backend tests. |

## Milestone review and recommended order

The existing milestones are grouped mainly by implementation layer. The vision
calls for a user-workflow-first sequence:

1. **M10 Physical contract first:** implement identity/fidelity, typed profiles,
   evidence, effective route profiles and request requirement compilation. Do
   not build adaptive routing on boolean capability maps.
2. **M5 canonical response events:** establish one lifecycle/content/usage
   event stream before adding protocol-specific semantics.
3. **M6 reasoning semantics:** parse and translate Codex/OpenCode/Antigravity/
   Claude Code intent without duplicating Physical models or silently clamping.
4. **M3 lifecycle boundaries:** make cancellation, first byte, stream
   completion and post-commit errors observable and deterministic.
5. **M4 policy completion:** apply explainable ranking and strategy only after
   eligibility; finish sticky/fusion/concurrency behavior.
6. **M8 projection/operations:** finish aggregate status, effective-order
   explanation, retention and logs on top of the runtime core.
7. **M2 management UX:** complete Physical comparison, runtime evidence,
   effective-order explanations, filters and split member/candidate editors.
8. **M7:** finish upstream OAuth for providers the user actually configures.
9. **M9:** grow provider manifests by demand. Keep M11 deferred and M12
   outside the core.
10. **M13:** establish the canonical versioned config contract before promising
   portability or sync. Do not copy database tables as the sync format.
11. **M14:** make local service operation and protected remote serving explicit.
   Structured logs to stdout/journal are the first path; remote log vendors
   should use a standard collector/export protocol.

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

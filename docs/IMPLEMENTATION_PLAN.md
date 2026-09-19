# Implementation roadmap

Status is based on the repository implementation and tests inspected on
2026-09-20. This is the project roadmap and the source of truth for milestone
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
integration. The interaction contract is documented in [TUI UX](TUI_UX.md).

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
| M1 | SQLite control plane and immutable routing snapshot | Done (core) | Config CRUD, validation/reload, route expansion, IPC and explicit publication exist. Harden schema/lifecycle; storage portability is separate. |
| M2 | Provider onboarding and model-management UX | Partial; redesign required | The current TUI has independent frames and basic CRUD/search, but its unified Models list is not the target. Implement the LazyGit-style block/tab/context contract: `h/l` blocks, `j/k` items, `[/]` tabs, Enter/Esc depth, Space selection and context-local `/` filtering. Discovered, Physical and Combos must be separately managed tabs in one Models workspace; exposure remains an inline property. See [TUI UX](TUI_UX.md). |
| M3 | OpenAI Chat request vertical slice | Partial | Kernel/adapters and live provider calls exist. Close cancellation, error/commit boundaries, load behavior and end-to-end usage/health tests. |
| M4 | Hierarchical model execution, connection pool and policy primitives | Partial; redesign required | Candidate expansion, connection selection, cooldown and quota gates exist, but nested combos are flattened and only the outer strategy survives. Introduce typed physical/combo references and strategy primitives that preserve policy boundaries; then finish sticky semantics, failure scope and deterministic concurrent behavior. |
| M5 | Canonical response-event contract | Planned | Introduce shared response events/fixtures where they remove duplicated stream logic; preserve lossless passthrough paths. Scope this to intended protocols. |
| M6 | Responses/Anthropic semantic compatibility | Partial | Basic adapters and Anthropic text/tool SSE conversion exist. Define and test supported continuity, content-block, tool, thinking, usage and malformed-stream behavior. |
| M7 | Upstream credential lifecycle and OAuth | Partial | Static API-key/Bearer resolution exists and OAuth dependency is selected. Add typed flow bindings, refresh/token persistence and refresh-once behavior for needed providers. This is distinct from client auth to hosted GoBroom. |
| M8 | Usage, cost, quota and health services | Partial | Compact usage events, health persistence, quota snapshots/policy and generic source/poller plumbing exist. Add useful aggregates/retention and connect quota/usage to routing; defer elaborate dashboards. |
| M9 | Provider presets and discovery expansion | Partial | JSON manifests and reusable primitives support generic compositions. Add desired providers/operations; avoid provider-specific kernel code or breadth-only checklists. |
| M10 | Capability and modality policies | Partial | Typed capability declarations and hard route filtering exist. Complete detection/degradation for supported modalities; never silently strip user input. |
| M11 | Optional middleware | Planned/deferred | Not on the critical path. Revisit only for demonstrated need; keep opt-in, bounded and unable to mutate route identity or bypass cancellation. |
| M12 | Secondary APIs and integrations | Out of core | Embeddings/media/search, tunnels, MITM/DNS and IDE integrations remain separate services/sidecars, not kernel milestones. |
| M13 | Canonical configuration bundle and sync | Planned | Define a versioned typed representation for provider nodes/connections, discovered routes, physical models, combo models and inline exposure. Add validate/diff/dry-run and atomic apply/export/import; keep secrets separate. Start single-writer; do not imply conflict-free multi-master sync. |
| M14 | Portable operations and remote hosting | Planned | Make paths/listeners/auth explicit; structured stdout/stderr should work with journald. Protect remotely exposed data plane with client auth and TLS/reverse-proxy guidance; keep control API disabled by default. Define a storage interface; SQLite remains default, and any alternate backend must be named and tested. |

## Milestone review and recommended order

The existing milestones are grouped mainly by implementation layer. The vision
calls for a user-workflow-first sequence:

1. **M2 contract first:** implement the panel/tab/context state machine and
   separate Discovered, Physical and Combo management with context-local
   filtering. Do not build more editors on the current unified-list facade.
2. **M4 graph/policy redesign:** preserve hierarchical policy boundaries and
   make fallback/rotation/weighting registered primitives before polishing the
   routing UI around misleading flattened behavior.
3. **M3 hardening:** make cancellation and response-commit behavior predictable
   under concurrency.
4. **M13:** establish the canonical versioned config contract before promising
   portability or sync. Do not copy database tables as the sync format.
5. **M14:** make local service operation and protected remote serving explicit.
   Structured logs to stdout/journal are the first path; remote log vendors
   should use a standard collector/export protocol.
6. **M5 + M6:** complete only protocol/stream semantics needed by intended
   clients, backed by fixtures.
7. **M7 + M8:** finish upstream OAuth and actionable quota/usage policies for
   providers the user actually configures.
8. **M9 + M10:** grow provider manifests and modality coverage by demand. Keep
   M11 deferred and M12 outside the core.

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
5. **Exposure is explicit on the model.** Internal physical and combo models do
   not appear in `/v1/models` unless exposed; there is no separate publication
   workflow in the target UX.
6. **Policy boundaries survive resolution.** Resolving a role combo must not
   flatten away the strategy of its physical/combo members.
7. **No retry after response commitment.** Once response bytes are sent, errors
   are surfaced; the request cannot silently switch routes.
8. **Usage is best-effort and bounded.** Observability must not stall an SSE
   stream or first-byte delivery.
9. **Frontend independence.** Daemon operation does not depend on a running TUI
   or CLI; all frontend mutations use the same daemon control contract.
10. **One configuration truth.** UI edits, CLI operations and sync validate and
   apply the same versioned domain model; exports exclude secrets by default.
11. **Defaults stay simple.** SQLite, local IPC, loopback serving and
    stdout/journal logging work without external services.

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
- supported Chat, Responses and Anthropic formats have documented limits and
  passing fixtures, including streaming and tool calls;
- connection and combo ordering/fallback behavior is deterministic and
  concurrency-safe;
- credential refresh and quota state influence routing where configured and
  survive restart;
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

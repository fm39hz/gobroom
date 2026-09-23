# GoBroom documentation

## Start here

- [Vision](VISION.md) — product intent, core workflow, portability boundaries
  and explicit non-goals.
- [Implementation roadmap](IMPLEMENTATION_PLAN.md) — milestone status, next
  work, invariants and replacement gate. This is the status source of truth.
- [Architecture](ARCHITECTURE.md) — runtime topology, model layers, concurrency
  and package responsibilities.
- [Physical model contract](PHYSICAL_MODELS.md) — identity and source fidelity,
  capability/limit/reasoning profiles, harness consumption and routing rules.
- [TUI interaction and model management](TUI_UX.md) — LazyGit-style context
  navigation, separate Discovered/Physical/Combos layers and filtering rules.
- [Behavior comparison](COMPATIBILITY_MATRIX.md) — source-grounded behavior
  comparison and known compatibility gaps.
- [Operations](OPERATIONS.md) — paths, journald, data-plane auth and
  reverse-proxy boundary.

## Contracts

- [Daemon and transports](DAEMON_ARCHITECTURE.md) — daemon lifecycle, IPC,
  data-plane/control-plane separation and deployment modes.
- [Frontend boundary](DAEMON_FRONTEND_BOUNDARY.md) — CLI/TUI ownership and
  daemon independence.
- [Kernel contract](KERNEL_CONTRACT.md) — snapshot, route selection, adapter,
  fallback and event responsibilities.
- [Normalization](NORMALIZATION.md) — inbound semantic request and protocol
  translation constraints.
- [Provider definitions](PROVIDERS.md) — typed manifest and primitive model.
- [Core policies](CORE_POLICIES.md) — concurrency, quota/usage, security and
  hot-path invariants.
- [Passive health and adaptive routing](PASSIVE_HEALTH_ROUTING.md) — real-call
  outcomes, limit windows, scoped breakers, status and route ranking.

## Engineering reference

- [Dependencies](DEPENDENCIES.md) — selected libraries, pinned versions and
  upgrade policy.

Documents labeled **Status** distinguish current implementation from target
contracts. Do not infer milestone completion from an architecture sketch; check
the roadmap and compatibility matrix.

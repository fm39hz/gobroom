# Architecture

Status: implementation-oriented overview. Milestone completion is tracked in
[the implementation roadmap](IMPLEMENTATION_PLAN.md).

## Goal

GoBroom is a small daemon-centered provider gateway, not a web application that
happens to run in the background. It separates the HTTP data plane from daemon
control and keeps the request hot path independent of SQLite and frontends.
“Small” means a narrow runtime and explicit contracts, not omitting useful
provider, model, combo, health or quota behavior.

## Runtime topology

```text
OpenAI-compatible client
  -> HTTP data plane (/v1/*)
  -> normalize request
  -> resolve exposed model against immutable snapshot
  -> execute hierarchical model policies to a physical route/connection
  -> provider adapter + HTTP transport
  -> translate/passthrough response

CLI / TUI / optional HTTP control API
  -> local IPC or control handler
  -> daemon services
  -> SQLite mutation + validate/rebuild snapshot
```

The daemon is authoritative for configuration and runtime state. CLI/TUI never
open SQLite. A running TUI is not required for daemon readiness or request
serving.

## Model and route layers

```text
provider node + connection
  -> discovered model route
  -> physical model
       -> ordered discovered route references + source-selection policy
  -> combo model
       -> ordered physical/combo references + execution strategy
  -> exposed projection (/v1/models)
```

- **Provider node**: endpoint, protocol/preset reference, model discovery path
  and provider-level defaults.
- **Connection**: credentials and connection-level routing/health state.
- **Discovered model**: discovered/custom upstream model entry owned by a
  provider node. It is separately reviewable and assignable inventory, not a
  physical model merely because it exists upstream.
- **Physical model**: stable user identity for one real model implemented by
  one or more discovered routes. It owns source order/policy and capabilities.
- **Combo model**: routable model with ordered physical/combo members and a
  typed execution strategy. Role names are ordinary combo names, not a special
  kernel type.
- **Model exposure**: an attribute on physical/combo models. Only exposed
  models appear in `/v1/models`; publication is not a separate user-managed
  catalog.
- **Physical route**: resolved tuple of provider node, opaque external model ID
  and connection. Prefixes are wire/display projections, not model identity.

For example, three discovered routes with different prefixes or upstream
spellings can implement the physical model `qwen-3.7-max`. The combo `junior`
can then reference that physical model alongside other physical/combo models.
`/models` discovery supplies source candidates; it does not dictate the model
graph.

## Request-path design

At daemon start or valid configuration change, the control plane builds a
snapshot containing typed model definitions, exposure flags and route data. It
validates references/cycles before atomic publication. Execution must preserve
model boundaries: a combo policy chooses a model member, then that physical or
nested combo applies its own policy. Pre-flattening the complete graph into one
route list destroys those semantics. Requests read the immutable snapshot and
do not query SQLite for static routing data.

The current implementation still represents logical models, combos and
published models in separate persistence/contracts and recursively flattens
nested combo members during resolution. That is an implementation gap, not the
target architecture. Migration must preserve existing data while introducing
typed Discovered, Physical and Combo management layers.

## Policy primitives

Resolution, eligibility, planning and failure transitions are separate:

```text
typed model graph
  -> resolve member references without erasing policy boundaries
  -> filter by capability, health, cooldown and quota
  -> strategy plans member order/concurrency
  -> execute member
  -> classify result
  -> strategy chooses return, retry, next member or aggregate
```

Strategies are registered implementations such as ordered fallback, rotating
fallback, weighted fallback, parallel race and fusion. Provider adapters supply
stable error classes; the kernel does not encode provider-specific status/body
rules. The same strategy contract can be applied at different graph layers
without conflating a combo member with a provider route.

The kernel owns exposed-name resolution, capability/quota/health eligibility,
candidate order, fallback boundaries and usage-event emission. Adapters own
provider protocol details: request preparation, execution, error classification
and response translation. No provider-name switch belongs in the kernel.

No lock is held across database I/O, token refresh or upstream I/O. Retry is
allowed only before the response is committed to the client.

## Provider extensibility

Provider definitions are JSON manifests. They bind typed primitive references
for endpoint construction, authentication, codecs, model source, error
classification and optional usage/quota/session behavior. Reusable runtime
implementations live in registries. A new provider should normally be a
manifest composition; adding a provider must not require editing kernel code or
copying another provider's implementation.

This is an architectural rule, not a claim that every possible OAuth flow or
provider operation is already represented. Current primitive coverage and
gaps are tracked in [provider documentation](PROVIDERS.md) and the roadmap.

## Concurrency and persistence

- Concurrent HTTP requests execute independently; routing snapshots are
  immutable and atomically replaced.
- Selection state is in memory and scoped to a policy key; persistence is
  asynchronous where it is not required to commit configuration.
- SQLite stores durable configuration and compact runtime/usage state.
- Usage events use a bounded queue; dropping an event under pressure must not
  block response delivery.
- Health and quota gates are runtime policy inputs, not dashboard-only data.

## Repository boundaries

```text
cmd/gobroomd          daemon wiring and lifecycle
cmd/gobroom           bundled CLI/TUI control entrypoint
internal/tui          Bubble Tea IPC frontend
internal/api          HTTP protocol boundary
internal/daemon       IPC, lifecycle, service wiring and background loops
internal/controlplane DB-to-snapshot construction and model resolution
internal/kernel       immutable routing contract, scheduler and execution
internal/normalize    typed inbound request representation
internal/adapter      protocol adapters
internal/provider     manifests, primitive registries and auth/discovery
internal/runtime      health/quota policy and polling
internal/store        SQLite schema and repositories
internal/usage        compact usage events and worker
```

## Relationship to 9router

The comparison is behavior-specific, not a claim that GoBroom reproduces the
entire application. The useful routing shape—provider connections, discovered
and custom models, aliases/combos, fallback, translation and runtime usage or
quota—is represented by explicit daemon contracts. IDE interception, dashboard
and other secondary products are outside the core. Verified behavior and
unknowns are recorded in the [compatibility matrix](COMPATIBILITY_MATRIX.md).

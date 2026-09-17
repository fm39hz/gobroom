# GoBroom Architecture

Status: initial design

GoBroom is a daemon-first replacement for the routing and configuration core
that is useful in 9router. It is not a line-by-line port. The target is a
feature-complete local gateway whose configuration is convenient to manage
from a CLI/TUI. “Small” applies to runtime boundaries and the request path,
not to the number of supported routing features.

## 1. What 9router currently does

The current 9router process combines several products in one Next.js runtime:

```text
client
  -> Next.js route
  -> request/auth checks
  -> model parser and alias/combo lookup
  -> account selection and token refresh
  -> request translation
  -> provider executor
  -> SSE/JSON response translation
  -> usage and request-detail persistence
```

Its effective model topology is more important than the dashboard:

```text
logical name
  -> combo or alias
      -> provider/model route
          -> provider node
              -> credential/account
```

The local setup confirms four distinct concepts:

1. Provider nodes: named OpenAI Chat, OpenAI Responses or compatible endpoints.
2. Discovered and custom models: catalog entries exposed by, or defined for,
   those nodes.
3. Logical models and aliases: convenient names resolving to concrete routes.
4. Combos: ordered sets of routes or other logical names.

Role-oriented combos such as `tech-lead`, `senior`, `middle`, `junior` and
`intern` sit above model-family combos such as `deepseek-v4-flash`, `glm-5.2`,
`free` and `mimo-v2.5`. A single logical model can therefore expand into many
physical routes.

## 2. Current request flow and its expensive parts

### 2.1 Inbound request

The chat handler reads JSON, authenticates the client, loads settings, detects
whether the requested name is a combo, scans capabilities and enters either a
single-model or combo path.

### 2.2 Resolution and account selection

The single-model path resolves the logical name to `{provider, model}`, then
selects credentials. Current selection is protected by a process-wide promise
mutex. It also reads connections/settings and may write round-robin state before
the upstream request has started.

GoBroom must preserve selection correctness but use short, scoped state
operations. No lock may be held while doing DB I/O, token refresh, proxy
resolution or an upstream request.

### 2.3 Translation and provider execution

9router determines source format, target format, model-specific quirks, tool
shape, thinking/reasoning shape and streaming mode. It then translates the body
and dispatches through a provider executor.

This is the main semantic complexity. It should become an adapter boundary in
GoBroom, not logic spread throughout the router:

```text
normalized request
  -> provider adapter Prepare
  -> upstream HTTP request
  -> provider adapter Decode/TranslateStream
  -> normalized response stream
```

### 2.4 Fallback

Fallback happens at two levels:

```text
combo member 1 -> route/account fallback
               -> combo member 2
               -> route/account fallback
               -> ...
```

The router classifies errors into retryable, account-cooldown, route-cooldown
and terminal errors. It must not retry after response bytes have already been
sent to the client.

### 2.5 Usage and observability

The current process writes usage history and request details alongside the hot
request path. Raw request details can be large and SQLite has accumulated a
large freelist in the current installation. GoBroom should keep usage writes
compact and asynchronous. Raw payload logging is an explicit debug mode, not a
normal request feature.

## 3. GoBroom target architecture

```text
                         +----------------+
OpenAI/Anthropic client ->|                |-> provider adapter -> upstream
                         |  gobroomd     |
CLI/TUI ---------------->|                |<-
                         +--------+-------+
                                  |
                         +--------v-------+
                         | SQLite state   |
                         | config/cache    |
                         | cooldown/usage |
                         +----------------+
```

The daemon is the only component that owns routing state. The CLI/TUI is a
control client and does not independently resolve or execute requests.

### Components

```text
cmd/gobroomd       daemon entrypoint
cmd/gobroom        CLI/TUI entrypoint
internal/api        public gateway and local control API
internal/router     request lifecycle and fallback
internal/resolve    aliases, combos and route expansion
internal/provider   provider/node adapters and discovery
internal/transport  HTTP, SSE and protocol primitives
internal/state      in-memory account/cooldown/rotation state
internal/store      SQLite schema and repositories
internal/usage      bounded asynchronous usage events
internal/auth       local API authentication and credential handling
```

## 4. Core domain model

The model must keep logical references separate from physical routes.

### Provider node

A provider node describes an endpoint and protocol:

```text
id
name
base_url
protocol: openai_chat | openai_responses | anthropic
models_path
auth_mode
request_defaults
enabled
```

### Connection

A connection contains credentials for a provider or node. Secrets are stored
in SQLite only as necessary and are never returned by the control API.

```text
id
provider_node_id
name
credential_type
secret_ref/encrypted_secret
enabled
priority
health state
```

### Model catalog entry

The catalog combines provider-discovered models and user-defined custom models.
Discovery is a source of entries, not the only source of entries:

```text
kind: discovered | custom | alias
provider_node_id (optional for custom models)
external_id (optional for custom models)
display_name
capabilities
request overrides
last_seen_at
raw_metadata
```

Custom models are first-class because a provider may expose an incomplete or
unstable `/models` endpoint, or the user may intentionally define a route that
is not advertised. A custom model can point to a provider node, override the
upstream model ID, declare capabilities and provide protocol/request options.

### Logical model

A logical model is a user-facing name. It may resolve to one route, an alias, a
custom model or a combo. Model discovery populates candidate names, while the
user can add, rename, override or disable entries independently of the provider
catalog.

### Combo

A combo is an ordered policy over references:

```text
name
strategy: fallback | round_robin | round_robin_fallback | weighted
sticky_limit
members[]
```

Each member may reference a logical model, a provider/model route or another
combo. Resolution must flatten the graph, preserve order and reject cycles.

### Published model

Publication is separate from combo definition. A combo may be an internal
helper and remain invisible to clients. A published model gives a client-visible
name to any valid target reference:

```text
published name: tech-lead
target: combo:tech-lead

published name: fast-free
target: combo:free
```

Only published models appear in `/v1/models`. The data plane accepts only
those public names and then resolves their targets through the internal graph.
This allows many internal model-family combos while exposing only the final
role/model combos that the user wants clients to see.

### Route candidate

The resolver produces an immutable candidate for a request:

```text
logical_ref
provider_node
external_model
connection candidates
protocol
capabilities
timeout
retry policy
```

The hot path consumes resolved candidates from memory and does not repeatedly
parse model strings or scan all SQLite rows.

## 5. Request lifecycle

```text
1. Accept request
2. Authenticate local client
3. Normalize source format
4. Resolve logical model/combo from in-memory snapshot
5. Filter candidates by capability and health
6. Select route and connection using scoped state
7. Prepare provider request
8. Execute upstream with context cancellation
9. If no bytes were sent and error is retryable:
     mark cooldown and try next candidate
10. Stream/translate response to client
11. Emit compact usage event asynchronously
```

The route snapshot is immutable per request. Configuration changes affect new
requests and do not mutate an in-flight request's candidate list.

### Streaming rule

Fallback is allowed only before the client receives the first meaningful byte.
After that point, the daemon propagates the error/termination and does not
silently switch providers, because a provider switch would corrupt the client
conversation.

## 6. Resolution strategy

Resolution happens in two phases:

### Control-plane phase

After config changes or model refresh:

```text
SQLite config
  -> validate references
  -> discover/merge models
  -> flatten combos
  -> detect cycles and duplicates
  -> build immutable RouteSnapshot
  -> atomically publish snapshot
```

### Data-plane phase

For every request:

```text
model name
  -> snapshot lookup
  -> candidate filtering
  -> scheduler selection
  -> provider execution
```

This separation is the central performance design. SQLite is for control-plane
state, not per-token routing decisions.

## 7. Scheduling and health

The scheduler keeps volatile state in memory:

```text
route/account -> consecutive failures
route/account -> cooldown until
combo -> round-robin cursor
account -> sticky usage count
```

SQLite may periodically persist durable health information, but a request must
not wait for a database write just to advance a round-robin cursor.

Error policy should be explicit:

```text
401/403  -> refresh once or cooldown credential
429      -> cooldown route/account using Retry-After when available
502/503/504 -> short retry/fallback according to policy
4xx      -> terminal by default
stream error after bytes -> terminate, no fallback
```

## 8. Protocol adapters

The first adapters should be deliberately small:

1. OpenAI Chat Completions.
2. OpenAI Responses.
3. Anthropic Messages.
4. Passthrough for compatible nodes where source and target format match.

An adapter owns:

- endpoint path;
- authentication headers;
- model field mapping;
- request translation;
- SSE event decoding/encoding;
- usage extraction;
- provider error classification.

The router owns none of those provider-specific details.

## 9. SQLite role

SQLite is the durable control plane:

```text
provider_nodes
connections
model_catalog
logical_models
custom_models
combos
combo_members
settings
cooldowns (optional durable subset)
usage_events
usage_daily
```

Recommended rules:

- use native SQLite, not an in-memory WASM fallback;
- use WAL mode;
- keep payloads out of normal usage rows;
- batch usage inserts in a bounded worker;
- periodically prune usage by retention policy;
- run incremental vacuum or explicit maintenance, not uncontrolled growth;
- never block a streaming response on usage persistence.

## 10. Daemon/control API boundary

The daemon should expose two separate surfaces:

### Data plane

```text
/v1/chat/completions
/v1/responses
/v1/messages
/v1/models
```

### Control plane

Prefer a Unix socket on Unix and a local TCP endpoint on Windows:

```text
/api/status
/api/providers
/api/providers/{id}/models/refresh
/api/connections
/api/models
/api/combos
/api/public-models
/api/combos/{name}/test
/api/usage
/api/config/reload
```

The CLI/TUI uses only the control plane. It should never open the SQLite file
directly while the daemon is running.

## 11. CLI/TUI responsibilities

The first useful TUI screens are:

```text
Providers      add/edit node, test endpoint, refresh models
Models         search discovered models and inspect capabilities
Combos         create/edit order and strategy
Connections    credentials, priority, health/cooldown
Requests       compact live status
Daemon         start/stop/status/config reload
```

The TUI should operate through typed control API calls. This keeps the daemon
usable without a terminal and avoids duplicating business rules.

## 12. Phased implementation

### Phase 1: skeleton

- Go module.
- `gobroomd` process.
- SQLite migrations.
- local control API.
- provider node CRUD.
- OpenAI Chat passthrough.

### Phase 2: discovery, custom models and combos

- `/models` discovery.
- discovered/custom model catalog.
- custom model create/edit/disable.
- logical references.
- nested combo resolver.
- cycle detection.
- fallback and round-robin scheduler.

### Phase 3: protocol coverage

- OpenAI Responses.
- Anthropic Messages.
- normalized SSE handling.
- usage extraction.

### Phase 4: CLI/TUI

- provider/model management.
- combo editor.
- connection health.
- daemon status and logs.

### Phase 5: hardening

- token refresh adapters where needed.
- route-level retry policies.
- compact metrics.
- config export/import.
- load tests with many concurrent SSE streams.

## 13. Optional feature modules

GoBroom should retain advanced routing features as independently enabled
modules rather than removing them because they are not needed in every
deployment:

```text
token refresh
provider quota/cooldown probes
capability adapters
request compression/token savers
parallel race/fusion strategies
cloud sync
```

IDE integration belongs outside the routing kernel:

```text
MITM proxy
DNS interception
IDE-specific credential spoofing
IDE auto-install/bootstrap
```

Those features may later become sidecars or plugins without changing the core
daemon.

## 14. Decisions for the first implementation

- Daemon owns all state and routing.
- SQLite remains the source of durable configuration.
- `/models` discovery populates the model catalog; it does not replace custom
  models or user overrides.
- Combos are graph references, not opaque strings.
- Route resolution is control-plane work and is cached atomically.
- Data-plane request handling is cancellation-aware and DB-free in the common path.
- Raw request/response persistence is disabled by default, while advanced
  routing and translation features remain available as configurable modules.
- Fallback never occurs after response bytes have been sent.
- The default branch is `master`.

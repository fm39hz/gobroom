# GoRouter core policies

GoRouter keeps useful 9router techniques in the routing core, but removes the
coupling and weak points that made the original process heavy.

## Retain and improve

```text
provider presets and adapters
model discovery and custom models
logical names and nested combos
public model publishing
capability-aware routing
account pools and token refresh
route/account cooldowns
provider quota tracking
usage and cost accounting
OpenAI/Responses/Anthropic compatibility
SSE translation and cancellation
round-robin, fallback and weighted policies
```

## Remove from the core request path

```text
global selection mutex
per-request SQLite reads for static config
synchronous raw payload persistence
dashboard-driven state
implicit prefix collisions
implicit unknown-model fallback to OpenAI
IDE MITM/DNS integration
```

## Quota is routing state

Quota snapshots are associated with provider node, connection, model and
window. A scheduler can use them to:

- exclude an exhausted route until its reset time;
- select the route with the most remaining budget;
- distinguish account-wide and model-specific limits;
- apply provider `Retry-After` or provider-specific reset timestamps;
- expose quota to CLI/TUI and the control API.

Quota sources may be:

```text
provider quota API
upstream response headers
429/402/409 error bodies
local usage estimation
```

Each snapshot records its source and timestamp. Local estimates never silently
override an authoritative provider snapshot.

## Usage is an event stream

The data plane emits compact usage events after a request. A bounded worker
consumes those events to update:

```text
usage history
daily/period aggregates
cost and budget policies
quota estimates
route health scores
control API metrics
```

Usage persistence must never delay the first response byte or block an SSE
stream. If the queue is full, the daemon applies an explicit policy: retain
aggregates and counters first, sample or drop verbose event details second.

## Retention and data safety

The normal database stores metadata and token counts, not full request/response
payloads. Raw payload capture is a bounded, opt-in diagnostic sink with a
retention limit. SQLite maintenance and usage retention are daemon jobs, not
dashboard side effects.

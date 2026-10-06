# Core runtime policies

Status: design constraints for implementation. Implemented portions are marked
in the [roadmap](IMPLEMENTATION_PLAN.md); this document does not imply that
every listed policy is already complete.

## Routing policy

Keep static routing configuration in a validated immutable snapshot. The
request path should avoid SQLite reads for model graphs and must never hold a
global selection lock over database, credential or network I/O. Candidate
selection is per request and may consult narrow mutable runtime gates such as
health and quota.

Useful policy inputs include:

- published-model target and combo order;
- connection selection strategy and per-request exclusions;
- model/connection capability compatibility;
- cooldown, quota/reset and explicit preference;
- upstream error classification and response commitment state.

## Combo strategy primitive contract

Combo strategy selection is a closed, versioned primitive catalog—not a free
string switch in TUI, CLI or snapshot construction. Each definition owns the
canonical ID, user-facing explanation, runtime implementation and a typed
option schema. Store writes and snapshot publication validate the same schema;
unknown aliases/options are rejected. The current built-ins are:

| Primitive | Execution meaning | Configuration |
|---|---|---|
| `ordered-fallback` | Preserve configured member order and continue according to failure policy | No options |
| `rotating-fallback` | Rotate the first member per request, then try remaining members in order | No options |
| `round-robin` | Attempt one rotating member; return failure to the parent Combo policy instead of trying siblings | No options |
| `round-robin-fallback` | Keep one start member for N request plans, then rotate while retaining ordered fallback | `stickyLimit`: integer 1–1000, default 1 |
| `weighted-fallback` | Weighted starting member, then ordered fallback | Weight is a typed field on each Combo member edge, not strategy JSON |

Strategy configuration is JSON-bound at persistence/control boundaries, but is
validated by the primitive schema. No option may be accepted and ignored.
Member order and member weight are independent: filtering/reordering cannot
rewrite a weight, and changing strategy cannot rewrite the ordered graph.

Kernel fixtures cover these node-level semantics; precedence with nested
strategies and parent failure policies remains an end-to-end test obligation.

## Provider extensibility

Provider names must not appear as branches in the kernel. Manifests compose
typed endpoint, transport, authentication, codec, model-source, classifier,
usage and quota primitives. Provider-specific behavior should be added as a reusable
primitive or configuration binding. Do not duplicate one provider's code for
another provider when their behavior is expressible by the same primitive.

## Health, quota and usage

Health and limits are passive by default: real inference attempts produce typed
observations and scoped outcomes. The daemon does not continuously ping routes.
Quota endpoints are queried opportunistically after relevant evidence unless a
provider opts into another explicit policy. See
[the passive health contract](PASSIVE_HEALTH_ROUTING.md).

Quota/usage state is useful only when it influences routing or policy, not
merely a dashboard. Limit windows identify scope, source, observation/reset
time, confidence and evidence. Authoritative reset times remain distinguishable
from estimates and are not capped. Unknown/stale data has explicit semantics.

Static user priority is never rewritten by runtime feedback. Adaptive ranking
uses bounded, decaying evidence only after capability/limit/health eligibility;
session affinity and global preference remain separate scopes.

The data plane emits compact usage events into a bounded asynchronous path.
Persistence, aggregation and display must not delay response delivery. Under
backpressure, preserve aggregate/counter correctness where possible and drop or
sample optional detail according to a documented policy. Raw payload logging is
opt-in, bounded and separate from normal operation.

## Security and failure policy

- credentials belong to connections, not provider manifests or snapshots;
- secrets must not appear in logs or ordinary list/status responses;
- refresh/update is atomic and must preserve the last good credential on error;
- retry/fallback is allowed only before response commitment;
- cancellation propagates through provider calls and stream translation;
- external HTTP exposure is explicit; control-plane exposure is independently
  configurable from the provider data plane.

## Keep out of the hot path

```text
dashboard/TUI availability
per-request SQLite reads for static model configuration
synchronous verbose request-detail writes
provider-name branching in kernel code
IDE interception, MITM/DNS or tunnel lifecycle
unbounded queues, retries or background polling
```

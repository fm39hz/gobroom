# Passive health, limits and adaptive routing contract

Status: target runtime contract. The first runtime slice is now implemented:
classified outcomes enter the kernel, route evidence is retained, exact
provider reset times are honored, eligible routes receive bounded feedback
ranking, completion TTFT/throughput is captured, and quota polling is opt-in.
Request-class aggregation and opportunistic quota enrichment remain follow-up
work.

## Principle

GoBroom does not continuously ping providers to decide whether they are
healthy. Real inference attempts are the primary evidence source:

```text
real request attempt
  -> observation
  -> typed outcome classification
  -> scoped breaker and limit updates
  -> performance observation
  -> eligibility and ranking for future requests
```

No network healthcheck runs by default. Explicit user tests remain available,
and provider quota APIs may be queried opportunistically after evidence of a
quota/rate-limit failure. Expiry cleanup and local state decay are allowed
background work because they do not contact providers.

## Separate projections

One attempt can update several projections, but they are not one field:

```text
health/status  can the target be attempted, and why?
limits         which request/token/quota windows apply, and when do they reset?
performance    how has this route/connection behaved for comparable requests?
priority       how should eligible candidates be ordered under the chosen policy?
```

Static user priority is configuration. Adaptive preference is derived runtime
state and never overwrites static configuration.

## Attempt observation

Every upstream attempt produces a structured observation across its lifecycle:

```text
AttemptObservation {
  requestId
  physicalModel
  routeId
  connectionId
  requestClass

  startedAt
  headersAt?
  firstByteAt?
  completedAt?

  phase: prepare | connect | headers | stream | complete
  statusCode?
  providerErrorCode?
  responseHeaders
  errorBodyExcerpt?

  inputTokens?
  outputTokens?
  reasoningTokens?

  clientCancelled
  responseCommitted
}
```

Raw credentials and unbounded bodies never enter this event. Provider parsers
receive only bounded evidence needed to classify outcomes and extract limits.

## Classified outcome

```text
ClassifiedOutcome {
  result: success | partial_success | failed | cancelled
  cause:
    request_invalid | capability_mismatch | model_not_found
    authentication | permission | billing
    quota_exhausted | rate_limited
    provider_capacity | provider_overloaded
    timeout | network | upstream_protocol | stream_failure | unknown
  scope
  retryAction
  deadline?
  limitWindows[]
  confidence
  evidence
}
```

Cause describes what happened. Retry action is a policy result:

```text
same_connection | another_connection | another_route | another_model | never
```

## Failure scope

Outcome scope determines which targets become ineligible or penalized:

```text
request
route
route + connection
route + connection + model
connection
provider node
provider
global
```

| Outcome | Scope |
|---|---|
| Invalid user payload shared by all routes | request; no health penalty |
| Upstream model ID not found | route/model; persistent invalidation |
| Expired or invalid API key | connection |
| Per-account per-model quota exhausted | connection + model |
| Account credit exhausted | connection |
| One regional endpoint unavailable | provider node/route |
| Provider-wide overload | provider |
| Client cancellation | neutral; no health penalty |

The kernel never assumes every HTTP 429 has the same scope or cause.

## Limit windows

Quota, rate limit, concurrency and credit state share a typed window contract:

```text
LimitWindow {
  kind:
    requests | tokens | credits | concurrency
    daily_quota | weekly_quota | billing_balance
  scope {
    providerNodeId?
    connectionId?
    modelRef?
  }
  name: burst | primary | secondary | session | daily | weekly | custom
  limit?
  used?
  remaining?
  resetsAt?
  retryAfter?
  source: response_header | success_body | error_body | quota_api | inferred
  confidence
  observedAt
}
```

Several windows can apply simultaneously. A route is blocked when an applicable
blocking window is exhausted; non-blocking low headroom may influence ranking.

## Passive limit collection

Evidence priority:

```text
response headers from real success/failure
  -> success body metadata
  -> error body metadata
  -> opportunistic quota enrichment after quota-like evidence
  -> estimated category backoff
```

Header extractors recognize `Retry-After` and provider-specific request or
token limit/reset fields. Error bodies can supply exact reset timestamps and
named quota windows. Each provider manifest binds reusable extractor and
classifier primitives; the kernel does not match provider error strings.

### Opportunistic quota enrichment

```text
real attempt returns quota-like 409/429
  -> classifier requests enrichment
  -> singleflight fetch for the relevant connection/model
  -> exact remaining/reset windows recorded
  -> inference fallback continues independently
```

Enrichment is deduplicated, bounded and has its own cooldown. Its failure never
breaks an inference response. Periodic quota polling is disabled by default;
providers that truly require scheduled refresh must opt into an explicit
policy rather than inheriting a daemon-wide ticker.

## Deadlines and provenance

```text
BlockDeadline {
  at?
  source:
    retry_after | rate_limit_header | error_body
    quota_api | estimated_backoff | permanent
  confidence: authoritative | strong | inferred
}
```

Deadline precedence:

```text
authoritative provider reset
  > Retry-After date/seconds
  > provider reset header
  > opportunistic quota API
  > category-specific estimate
```

Authoritative reset timestamps are not capped. Exponential backoff and maximum
delay apply only to estimates. Small jitter may be added when reopening to
avoid a reset-boundary thundering herd, but the reported reset remains visible
unchanged.

## Passive circuit breaker

```text
UNKNOWN
  ├─ real success -> CLOSED / available
  └─ real failure -> classify
                       ├─ transient -> OPEN until estimated deadline
                       ├─ limit     -> OPEN until exact reset
                       └─ config    -> INVALID until config/discovery changes

OPEN
  -> deadline expires
  -> HALF_OPEN
       ├─ one real request succeeds -> CLOSED
       └─ one real request fails    -> OPEN with new evidence/deadline
```

Half-open permits one in-flight trial for the breaker key. Other concurrent
requests use alternatives, preventing a stampede after reset. With no traffic,
no probe is sent; observations become stale rather than magically healthy.

## Success and neutral outcomes

HTTP 2xx headers alone are not success:

```text
headers accepted -> first byte -> stream/protocol completes -> success
```

| Event | Runtime effect |
|---|---|
| Complete valid response | success and performance sample |
| Stream fails after commitment | partial success plus reliability penalty |
| Client cancels after first byte | neutral cancellation |
| Timeout before headers | timeout/network failure |
| Empty or malformed upstream response | upstream protocol failure |
| Invalid client payload | request failure; route neutral |

Success reward is emitted only at the completion boundary defined by the
adapter/stream contract.

## Derived status

Status is a projection over observations, breakers, limits and freshness:

```text
unknown | available | degraded | blocked | misconfigured | disabled | stale
```

```text
DerivedStatus {
  state
  cause?
  scope?
  since?
  blockedUntil?
  confidence
  lastAttempt?
  lastSuccess?
  successRate?
  limits[]
  nextEligibleAt?
}
```

Physical and provider status are aggregates:

```text
Physical available  at least one compatible source is eligible
Physical degraded   only a subset is eligible or reliability regressed
Physical blocked    every compatible source is temporarily blocked
Physical invalid    no valid source binding remains
```

## Static priority and adaptive preference

```text
base priority       durable user intent/source order
adaptive evidence   ephemeral preference learned from real attempts
effective order     result of an explicit ranking policy
```

Runtime feedback never rewrites base priority. Candidate order is explainable
as a lexicographic policy:

```text
1. hard eligibility
2. session/cache affinity
3. health tier
4. user priority tier
5. adaptive score
6. original order
```

### Positive feedback

- **Session affinity:** a strong, short-lived preference for a route/connection
  that succeeded in the same conversation, preserving continuity, warmth and
  provider cache.
- **Global adaptive preference:** a smaller decaying preference based on
  completed success, TTFT, throughput, latency, reliability and quota headroom.

One success never permanently promotes a route. Failure penalties are stronger
than success rewards; both decay. Performance affects ranking only after
minimum sample and freshness requirements are met.

### Ranking modes

```text
strict_order          static order; failures only remove blocked candidates
adaptive_within_tier  adaptive ranking within user priority tier (default)
adaptive_global       adaptive ranking may cross tiers
lowest_latency
highest_throughput
lowest_cost
quota_preserving
```

### Exploration

Pure positive feedback creates a monopoly. Adaptive policies allow bounded
exploration of eligible stale/new routes:

- unknown routes are not unhealthy;
- blocked/incompatible routes are never exploration candidates;
- low-confidence routes receive limited safe traffic;
- high-risk requests may disable exploration;
- original order is the fallback when confidence is insufficient.

Bandit algorithms are deferred until deterministic bounded
boost/decay/exploration is measured and explainable.

## Request-class performance

Performance observations are bucketed where request shape materially changes
behavior:

```text
RequestClass {
  modality: text | vision | other
  context: small | medium | large | very_large
  effort: none_low | medium | high_plus
  tools: bool
  stream: bool
}
```

If a bucket lacks samples, ranking falls back to broader route/connection
aggregates and then static order.

```text
PerformanceSummary {
  sampleCount
  observedAt
  window
  ttftP50/P95
  outputTokensPerSecondP50/P95
  latencyP50/P95
  completionRate
  rateLimitRate
}
```

## Provider primitives

Provider manifests compose:

```text
outcomeClassifier
limitExtractor
quotaEnricher
deadlineParser
healthPolicy
```

```json
{
  "runtime": {
    "outcomeClassifier": "openai-json",
    "limitExtractor": "openai-rate-limit-headers",
    "quotaEnricher": "antigravity-on-limit",
    "healthPolicy": "passive-default"
  }
}
```

Adding provider-specific parsing does not add provider-name branches to the
kernel.

## Error examples

### 429 with Retry-After

```text
cause       rate_limited
scope       classifier-defined connection/model scope
deadline    exact Retry-After
action      try another eligible connection/route
```

### Quota exhausted with reset timestamp

```text
cause       quota_exhausted
scope       connection + model + named window
deadline    exact reset timestamp
limit       remaining=0
action      no retry before reset
```

### Provider overload

```text
cause       provider_capacity
scope       provider or provider node
deadline    short estimated backoff
action      fallback to another provider
```

It is not recorded as user quota merely because the status is 429.

### Authentication failure

```text
refreshable credential -> refresh once -> retry same connection once
refresh fails/static key invalid -> misconfigured until credential changes
```

### Model not found

```text
route/model invalid -> persistent invalid -> clear on discovery/config change
```

### Invalid request

If every conforming route would reject the same payload, the failure is
request-scoped and terminal; it does not penalize route health.

## Aggregate client errors

When no route remains:

```text
all compatible routes quota/rate-limited -> HTTP 429
all compatible routes transient/capacity -> HTTP 503
all compatible routes misconfigured      -> HTTP 503 + control diagnostic
no route satisfies request capabilities  -> HTTP 422
```

`Retry-After` and `next_eligible_at` use the earliest authoritative or
high-confidence time at which a compatible route can become eligible. Public
diagnostics are redacted; full details are available only through authorized
control IPC.

## Persistence and concurrency

Persist compact summaries:

```text
active breaker deadlines
limit windows
last classified outcome
EWMA/histogram summaries
sample counts
last attempt/success timestamps
```

Raw attempt history has bounded retention. On restart, unexpired exact resets
remain active, expired blocks disappear, performance scores decay and stale
routes become unknown/degraded rather than healthy.

Updates are scoped by breaker/observation key and never hold a global lock over
upstream I/O. Persistence is bounded/asynchronous and cannot delay first byte
or stream completion.

## Runtime interfaces

```text
AttemptObserver
  ObserveStart
  ObserveHeaders
  ObserveFirstByte
  ObserveComplete
  ObserveFailure

OutcomeClassifier
  Classify(AttemptObservation) -> ClassifiedOutcome

EligibilityPolicy
  Evaluate(route, request, runtime) -> Eligible/Reason

RankingPolicy
  Rank(eligibleRoutes, request, runtime) -> orderedRoutes

FailureTransitionPolicy
  Next(outcome, remainingCandidates) -> retry/fallback/return
```

The current `Gate`/`MarkFailure` API becomes a compatibility layer during
migration, not the final policy boundary.

## Management UX

Route status explains state and effective order:

```text
orca/deepseek-v4-flash
  state        blocked
  reason       weekly quota exhausted
  scope        account + model
  reset        2h 31m
  confidence   authoritative
  source       quota API after HTTP 429

  performance
    success    97.2% / 142 samples
    TTFT       820ms p50
    output     48 tok/s p50

  priority
    base       tier 1 / position 2
    effective  position 3
    reason     quota-blocked
```

Physical aggregate:

```text
deepseek-v4-flash
  27/33 available
  3 rate-limited
  2 quota exhausted
  1 invalid model
  next recovery 18s
```

The TUI supports manual test, disable, unblock and policy selection, but does
not hide evidence/provenance behind a generic “healthy” badge.

## Architectural invariants

1. No synthetic network healthcheck runs by default.
2. Client errors and cancellation do not degrade route health.
3. Every non-neutral health transition has cause, scope, evidence and confidence.
4. Authoritative reset times are not capped or replaced by local estimates.
5. Quota/rate-limit evidence does not implicitly block an entire provider.
6. A streamed attempt succeeds only at the adapter completion boundary.
7. Hard eligibility runs before adaptive ranking and strategy selection.
8. Runtime feedback never mutates durable user priority.
9. Positive feedback is bounded, decayed and confidence-gated.
10. New or stale routes are unknown, not unhealthy or healthy.
11. Half-open permits one real trial for the scoped breaker key.
12. Provider quirks live in classifier/extractor/enricher primitives.

## Delivery sequence

1. Introduce AttemptObservation and ClassifiedOutcome contracts.
2. Extract passive limit windows from real response headers and bodies.
3. Replace route-only cooldowns with scoped breakers and half-open trials.
4. Derive explainable route/connection/Physical/provider status projections.
5. Record passive performance summaries by request class.
6. Add session affinity and bounded adaptive-within-tier ranking.
7. Add opportunistic quota enrichers with singleflight/cooldown.
8. Disable daemon-wide periodic quota polling by default. The daemon exposes
   an explicit `--quota-poll` opt-in for providers that require it.
9. Expose evidence, limits, effective order and policy controls in IPC/TUI.

## Current implementation gaps

- Scoped route/connection/provider breaker state and one-real-request
  half-open trials are implemented.
- Request-class performance summaries and session affinity are not complete;
  the current in-memory book is route-level.
- Opportunistic quota enrichment/singleflight is not complete.
- Runtime ranking currently uses bounded success/failure feedback plus the
  in-memory route-level performance book; request-class aggregation is pending.
- User priority and runtime preference are not yet exposed in the control UI.
- Status does not explain aggregate cause, confidence or next eligibility.

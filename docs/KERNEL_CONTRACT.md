# Data-plane kernel contract

Status: hierarchical execution boundary implemented, with remaining policy
semantics tracked by M4 in the [roadmap](IMPLEMENTATION_PLAN.md). A flattened
resolver remains for legacy callers, but the request execution path traverses
typed model nodes and preserves nested policy boundaries.

## Ownership

The kernel owns public model resolution, route candidate ordering, request
eligibility, adapter dispatch, fallback boundary and compact usage emission.
HTTP handlers normalize inbound requests and translate kernel errors to HTTP;
they do not walk combos or choose provider credentials. Provider adapters own
wire protocol behavior.

## Snapshot and resolution

The target snapshot contains discovered routes, physical models, combo models
and exposure flags. A request resolves only through this snapshot. Internal
models remain valid graph members but are absent from `/v1/models` unless their
exposure property is enabled.

Request execution retains typed model nodes and their policy boundaries. A role
combo selects a model member; that physical or nested combo then applies its own
source/member policy. The legacy `resolveRef` route expansion remains a
compatibility/read API and must not be used as the hierarchical execution plan.

Snapshots carry connection identifiers and routing metadata, never connection
secrets. Credentials are resolved for the selected route at execution time.

## Execution sequence

```text
normalized request
  -> resolve exposed model name
  -> enter physical/combo model node
  -> apply eligibility filters
  -> ask the node's strategy for an execution plan
  -> recursively execute the selected member
  -> resolve route credential
  -> adapter Prepare
  -> adapter Execute
  -> classify upstream status
  -> translate response
  -> update runtime policy and emit usage event
```

The current kernel contract is represented by `ProviderAdapter` in
`internal/kernel/contract.go`; provider execution is wired by the daemon.

## Scheduling and fallback

Health and quota gates remove unavailable members before selection. Strategy is
a primitive contract, with implementations such as ordered fallback, rotating
fallback and weighted fallback. Resolution does not choose or erase strategy.
Exact policy semantics and concurrency behavior require contract tests. No
scheduler lock may span credential refresh or network I/O.

Fallback is permitted only before response commitment. If an adapter has
written/flushed response bytes, subsequent errors cannot transparently move to
another candidate. A terminal error stops candidate fallback; retryable and
cooldown outcomes may continue according to policy.

## Adapter boundary

```go
Prepare(ctx, normalizedRequest, route, credential) -> upstream request
Execute(ctx, upstream request) -> upstream response
ClassifyError(status, body) -> stable error class
TranslateStream(ctx, upstream response, writer, client format, hooks) -> error
```

The adapter interface currently combines several protocol responsibilities.
M5 tracks extracting a shared canonical response-event layer without removing
the ability to use a lossless passthrough path.

## Runtime events

Adapters report completion/error through hooks. The kernel enriches successful
usage with logical model, route, connection and elapsed time, then attempts a
non-blocking send to a bounded event channel. Runtime health updates and
persistent usage workers consume events independently of response delivery.

## Stable error classes

```text
terminal   do not retry another route
retryable  route may be retried/fallback may continue
cooldown   suppress route for a policy interval
auth       credential/authentication failure policy
```

Provider-specific status/body heuristics belong in classifier primitives, not
kernel branches. Exact precedence, 401 refresh and post-commit behavior remain
explicit test obligations.

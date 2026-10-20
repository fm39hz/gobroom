# Data-plane kernel contract

Status: hierarchical execution and semantic response composition implemented;
full semantic closure is pending M15 in the [roadmap](IMPLEMENTATION_PLAN.md). This is the
current kernel contract; the normative target for operation-neutral routing,
compatibility plans and adapter boundaries is the
[solution architecture](SOLUTION_ARCHITECTURE.md).
Detailed target contracts are fixed in
[Semantic extension contracts](EXTENSION_CONTRACTS.md).

## Ownership

The target kernel owns public model resolution, hard route eligibility,
candidate ordering, model-policy traversal, fallback boundaries and compact
usage/outcome emission. It consumes a semantic invocation and compatibility
decisions; it does not know provider names or client wire envelopes. Ingress
codecs normalize client requests, provider modules encode/decode upstream
protocols, and egress renderers translate semantic events to the client.
HTTP handlers map boundary errors to HTTP; they do not walk combos or choose
provider credentials.

## Snapshot and resolution

The target snapshot contains discovered routes, physical models, combo models
and exposure flags. A request resolves only through this snapshot. Internal
models remain valid graph members but are absent from `/v1/models` unless their
exposure property is enabled.

Request execution retains typed model nodes and their policy boundaries. A role
combo selects a model member; that physical or nested combo then applies its own
source/member policy. Route expansion is an internal validation/read helper and
execution always traverses the typed graph.
Physical route eligibility and reasoning translation follow the typed profile
and request-requirement rules in [the Physical model contract](PHYSICAL_MODELS.md).

Snapshots carry connection identifiers and routing metadata, never connection
secrets. Credentials are resolved for the selected route at execution time.

## Target execution sequence

This sequence implements the normative semantic contracts. Current execution
contains the graph/adapter core but does not yet implement every plan stage.

```text
semantic invocation
  -> resolve exposed model name
  -> enter physical/combo model node
  -> compose model-path defaults and enabled invocation transforms
  -> prepare candidate-local defaults/transforms and final requirements
  -> negotiate complete semantic compatibility and artifact transfer
  -> apply operation/capability/health/quota eligibility
  -> ask each node's strategy to order eligible members
  -> recursively retain the selected member's policy boundary
  -> acquire route credential lease
  -> provider encode + endpoint resolution + auth apply/sign
  -> validate lease scope/expiry and live credential revision
  -> transport dispatch
  -> provider decode into semantic response events
  -> optional response transform and client rendering
  -> classify attempt evidence
  -> update runtime policy and emit usage event
```

The current kernel contract is represented by `ProviderAdapter` in
`internal/kernel/contract.go`; provider execution is wired by the daemon.

## Scheduling and fallback

Typed eligibility removes incompatible or runtime-blocked members before
selection. Ranking combines static order with passive runtime evidence under an
explicit policy; strategy then plans fallback, rotation or fusion. A route
whose cooldown has expired admits one real half-open trial before concurrent
callers can reuse it. See the
[passive health contract](PASSIVE_HEALTH_ROUTING.md). No scheduler lock may
span credential refresh or network I/O.

Fallback is permitted only before response commitment. If an adapter has
written/flushed response bytes, subsequent errors cannot transparently move to
another candidate. A terminal error stops candidate fallback; retryable and
cooldown outcomes may continue according to policy.
Before commitment, replay additionally depends on dispatch/effect state and
the operation/binding's declared safety. An unknown unsafe effect cannot be
repeated merely because no client bytes were emitted. See
[Attempt replay and commitment](EXTENSION_CONTRACTS.md#6-attempt-replay-and-commitment).

## Adapter boundary

The provider extension is composed from separate contracts:

```text
RequestCodec.Prepare(invocation, route, credential) -> upstream request
Transport.Execute(upstream request) -> upstream response
ResponseDecoder.Decode(upstream response) -> semantic response events
ResponseRenderer.Begin(client contract) -> render session
render session.Emit(event) / Finish(error) -> client wire response
```

`ComposedAdapter.RenderResponse` coordinates decoder, registered transforms,
and the selected renderer. Individual provider codecs never write to the
client writer. The renderer controls response commitment; the kernel may retry
only while that renderer has not emitted client bytes. See the
[solution architecture](SOLUTION_ARCHITECTURE.md) for the full compatibility
and fidelity contract.

## Attempt credential authority

Each admitted attempt owns a bounded `CredentialLease`. Its exact scope binds
route, provider node/definition contract, connection/mode/flow, base URL, model,
protocol, stable principal and one exact operation or consumer contract. A
background consumer ref (`UseRef`) never carries inference `OperationRef`
authority; the composition root resolves it from the frozen catalog. The
kernel validates exact refs without a provider/consumer-kind whitelist. The
issuer's credential revision remains private. `Credential` is transient codec/signing material, not
authority to dispatch; borrowed headers/context are copied. An installed issuer
cannot be bypassed by supplying raw caller material.

The daemon resolves through its selected auth primitive and issues only if that
material still matches the live durable owner/revision. The kernel checks scope,
expiry, cancellation and the live revision fence after preparation and before
every dispatch, including replay. This is a last-boundary check, not an atomic
transaction with an external server. No scheduler/lease lock spans network I/O.
Local fence rejection does not become an upstream health failure or silently
fall back after preparation. Successful refresh replaces and releases the old
lease without relaxing effect/idempotency/replay gates. Attempt completion and
every normal error path release authority idempotently. Expiry prevents a new
dispatch; it does not abort a response already accepted by upstream.

Leases last at most one hour, capped by the caller deadline and credential
expiry. Expired renewable material can be refreshed before admission; auth
modes without refresh fail locally. Scope/lease formatting and JSON are
redacted. Dropping references does not claim to erase immutable Go strings or
copies retained by trusted in-process codecs.

Quota reads share lease issuance but use the exact quota-source ref. Polling
and opportunistic enrichment borrow transient material and pass the source a
lease-guarded transport: every report send is fenced independently after source
preparation. Final validation discards the whole result if authority changed,
even when a source returns partial data. Singleflight/cooldown identities bind
principal and credential revision; old work cannot suppress a new account's
read. Caller disconnect is detached, but daemon lifetime and explicit close
cancel/drain queued and admitted work before quota persistence or the DB closes.
An `x/sync/semaphore` bounds enrichment admission (default 16 workers); excess
signals are dropped without advancing their cooldown, rather than queuing more
goroutines. Polling remains opt-in. Trusted in-process sources must use the provided
transport; this is a lifecycle contract, not a plugin sandbox.

This integration covers inference and quota reads. Discovery, connection probes
and other daemon-owned outbound jobs still require convergence before the whole
credential-lease architecture gate is closed.

## Runtime events

Adapters report lifecycle completion/error through hooks. A 2xx header is not
success until the adapter's response/stream completion boundary, and client
cancellation is neutral health evidence. The kernel enriches successful usage
with logical model, route, connection and elapsed time, then attempts a
non-blocking send to a bounded event channel. Runtime health updates and
persistent usage workers consume events independently of response delivery.

Adapters may additionally emit the canonical `ResponseEvent` stream through
`StreamHooks.OnEvent`: response start, text/thinking delta, tool-call delta,
usage, completion and error. Semantic events are already the shared response
path consumed by registered transforms and client renderers. The full target
adds versioned operation events/artifacts, compiled opt-in chains and separate
original-versus-rendered observations.
Malformed SSE payloads are protocol errors: adapters emit a canonical error
event and return the error instead of silently discarding the payload.

## Stable error classes

The following compact execution actions coexist with the richer passive
outcome taxonomy:

```text
terminal   do not retry another route
retryable  route may be retried/fallback may continue
cooldown   suppress route for a policy interval
auth       credential/authentication failure policy
```

Provider-specific status/body heuristics belong in classifier primitives, not
kernel branches. Cause/scope/retry/deadline-rich outcomes are described in
[Passive health](PASSIVE_HEALTH_ROUTING.md). Exact
precedence, 401 refresh and post-commit behavior remain explicit test
obligations.

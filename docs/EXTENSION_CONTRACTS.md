# Semantic extension contracts

Status: normative design, decided on 2026-10-06. This document closes the
extension contracts required by [solution architecture](SOLUTION_ARCHITECTURE.md).
It specifies implementation obligations; it does not claim that the current
runtime implements them. Evidence is tracked in [M15 conformance](M15_CONFORMANCE.md).

## Ownership and design boundary

The model domain remains ProviderDefinition, ProviderNode, Connection,
DiscoveredRoute, PhysicalModel and ComboModel. New behavior contributes an
implementation, descriptor and bindings to the owners below. The kernel
consumes validated plans, requirements, events and outcomes.

| Owner | Contract |
|---|---|
| Extension catalog | Typed descriptors, schemas, dependency/version resolution and factories |
| Operation definition | Payload/event schemas, validation, requirement compilation and replay semantics |
| Ingress codec / client renderer | Client decoding, rendering, possible event support and boundary errors |
| Provider encoder / decoder | Upstream dialect mapping and compatibility declarations |
| Compatibility planner | Combine declarations and policy into a candidate-specific execution plan |
| Transform compiler | Resolve opt-in bindings into bounded, ordered pipelines |
| Auth driver / daemon auth coordinator | Credential application and interactive authorization lifecycle |
| Kernel / attempt executor | Graph traversal, admission, execution, commit and retry boundaries |
| Outcome / usage observers | Original upstream evidence and bounded operational feedback |

Static module registration belongs at the daemon composition root. A module
may register implementations and descriptors; adding one must not edit the
kernel's model types, provider dispatch, storage columns or generic frontend
workflow. Built-in and user definitions resolve through the same catalog.

Catalog compilation happens off the serving path. Schemas, factories,
dependency resolution and plan templates are prepared once per snapshot.
Per-request negotiation uses local declarations/evidence and performs no
provider probing or credential network I/O. Cheap rejection may precede
transforms only for immutable constraints. Request-local path/candidate caches
avoid repeated work; global locks never span provider execution.

## 1. Extension descriptor and configuration binding

Every configurable extension advertises a descriptor:

```text
ExtensionDescriptor {
  id, kind, contractVersion, implementationVersion
  displayName, description
  optionsSchemaRef, supportedOperationRefs, dependencies[]
  semanticContracts[], lifecycleCapabilities[]
  resourceBounds, setupView?
}
ExtensionRef { id, contractVersion }
ExtensionBinding { instanceId, extensionRef, enabled, options }
OperationTaskRef { kind: operation, id, contractVersion }
```

IDs are namespaced strings; contract versions are positive integers and
schema references carry the same ID/version identity plus an immutable digest.
References resolve an explicit contract version;
the catalog rejects unresolved references, duplicates and dependency cycles.
Implementation version and configuration digest form part of the resolved
catalog fingerprint. Schemas are registered, versioned JSON Schemas; trusted
Go implementations bind validated options into typed values. JSON is the
boundary representation, not permission for string/array rewriting or
unvalidated maps on the serving path.

Operation, feature, policy, codec, auth and transform option schemas are
first-class catalog entries. Options can be objects, arrays, strings, numbers,
booleans or tagged alternatives. Generic clients use the same schema as
daemon validation; unsupported editor widgets fall back to a schema-validated
structured editor. An optional setup view is a typed view descriptor, never
an executable shell command or a frontend provider-name case.

Registration makes an extension available. Only an enabled binding schedules
it. Configuration application validates every reference and option before
publishing an immutable snapshot and resolved execution-plan templates. An
in-flight request pins its snapshot/catalog version across all fallback
attempts. It does not observe partial reloads.

Portable bundles include declarative user provider definitions and extension
bindings, plus dependency references for compiled modules. They exclude code,
credentials, live auth sessions and runtime health. Import reports missing
modules/contracts before changing state; it does not silently substitute a
different implementation. Ordinary config/descriptor exports never include
connection secrets. Administrative endpoint/options exports are explicit;
secret-bearing fields use secret references rather than inline credentials.

Until release stability is declared, a changed contract replaces its previous
shape. No alias schema, legacy reader or compatibility path is retained.
Reset/rebuild of development state is explicit, not an automatic silent
deletion on daemon startup.

## 2. Operation payloads, content and artifacts

An operation is a semantic task, independent of both provider and client wire:

```text
OperationDefinition {
  ref: ExtensionRef
  inputSchemaRef, resultSchemaRef, eventSchemaRefs[]
  artifactInputs: [{role, exactTypeRef, minCount, maxCount}]
  Validate(payload) -> ValidationResult
  CompileRequirements(invocation) -> RequirementSet
  NewResultProjector(bounds) -> bounded incremental result accumulator
  semanticFacets[], replayContract
}
Invocation {
  operationRef, publicModel, clientContract
  payload: TypedPayload | Artifact
  promptPlan, tools, thinkingIntent, generationOptions
  content[], artifacts[], sessionContext
}
```

Shared chat semantics have typed values. An operation-specific payload uses
its own typed DTO bound to the registered schema. The envelope can carry that
payload without adding an operation field to every kernel layer. Multipart,
binary and JSON are ingress choices; the operation definition does not assume
that input is a messages array or a raw OpenAI body.

A result schema is only valid when the operation also supplies a request-local
result projector. The projector consumes canonical response-event envelopes
incrementally under the operation's buffer/output bounds and produces the
typed result checked against that exact schema before terminal completion is
accepted. Operations that expose only the event stream do not claim a typed
aggregate result schema.

Content references are tagged values: text, media/document reference,
tool invocation/result, refusal or extension artifact. New content kinds use
the artifact arm and a registered interpreter/requirement compiler. A custom
operation must derive real requirements from its payload; assigning a new
operation ID to chat fixtures does not prove this contract.

```text
Artifact {
  typeRef, role, schemaRef
  owner: { domain, issuerRef, providerDefinitionId?, connectionId?,
           modelIdentity?, clientContract?, sessionId? }
  applicability: { operationRefs[], recipientContracts[] }
  replayScope: request | session | connection | provider | portable
  sensitivity: public | private | secret
  expiresAt?, digest?, data: JSONValue | BodyRef
}
```

The extension catalog now registers artifact-type policies with exact refs,
owner-domain allowlists, replay scopes, sensitivity ceilings, media-type
allowlists, recipient contracts and a positive byte limit. `ArtifactRef` shape
validation distinguishes inline JSON from a body lease; the frozen catalog
validates inline schemas, expiry, digest form, declared byte sizes and any
cross-owner transfer. Portable transfer requires an explicit recipient
contract and can never carry secret-sensitivity artifacts. The leased body
store supports bounded request-scoped spool files and cancellable owner-scoped
leases. A generic multipart ingress binds file fields to exact artifact type
and role refs, streams them into the spool, and releases temporary bodies when
the request ends or decode fails. Operation definitions declare named artifact
input ports; request preparation validates role/type/count and the selected
provider boundary rechecks recipient policy before a codec can open the lease.
This is the generic binary path, not yet proof of provider-specific multimodal
JSON extraction, wire rendering, or a built-in non-chat operation running
end-to-end. A fake-provider HTTP→kernel→adapter fixture proves that the
registered operation can consume the leased bytes and return a response;
native provider rendering remains a separate adapter contract.

The artifact type descriptor declares legal scopes and transfer rules; the
issuer may narrow them. Ingress cannot grant portability merely by labeling
an unknown provider token portable. Changing owner or broadening scope is
forbidden. A registered translator creates a new artifact with provenance
and an explicit compatibility effect instead of rewriting the original.

Provider signatures, encrypted reasoning and continuation tokens default to
their issuing connection/model/session. The issuer definition may declare a
broader proven scope. Credential material never enters invocation artifacts;
it stays in secret storage and scoped auth leases.

Unknown artifacts are preserved only inside a proven native path. They cannot
cross issuers or codecs implicitly. A required artifact without a compatible
consumer rejects the candidate. Dropping a non-required artifact is a named
loss requiring explicit policy permission. Opaque bytes alone do not prove
that forwarding is safe.

`BodyRef` exposes a bounded, cancellable reader or leased spool reference with
media type, size bound and optional digest. Its owner defines release timing.
Retry requires a replayable source; large media is not copied into every
candidate or forced into base64 JSON. Provider framing is declared in the
binding (JSON, SSE, binary event stream or another registered frame codec),
not inferred solely from an HTTP content-type string.

Semantic events share response ID, sequence, item/content identity and a
versioned payload. Common text/thinking/tool/usage/completion/error events
remain typed; operation-specific progress/results use registered event
artifacts. The decoder validates order and tool-call identity before emission.
Renderers declare support for the operation's possible event families,
including errors and completion. Unexpected required events are protocol
errors, never silently dropped. Non-stream aggregation consumes this same
event contract with explicit memory/spool bounds.

## 3. Compatibility plan and policy algebra

Compatibility negotiation receives the effective invocation, node path,
candidate route/connection, operation definition, codec contracts, active
transforms and scoped artifacts. A `(format, streaming)` predicate is only a
wire constraint within this negotiation.

Each component reports its mapping for semantic facets such as tool history,
tool deltas, reasoning effort/budget/summary, signatures, media, output schema,
prompt ordering and continuity. New facets are namespaced catalog entries.

```text
FacetMapping {
  facetRef, semanticPaths[]
  disposition: preserved | translated | degraded | unsupported | unknown
  conditions[], lossIds[], provenance
}
CompatibilityPlan {
  operationRef, clientContract, candidateRef, catalogFingerprint
  eligible, fidelity: native | translated | lossy | unsupported
  mappings[], losses[], reasons[], artifactTransfers[]
  effectiveInvocationRef, requestPipelineRef, responsePipelineRef
  renderMode: semantic | native_passthrough
  replaySafety
}
ExecutionPlan {
  snapshotRef, catalogFingerprint, ingressInvocationRef, graphPolicyRefs
  candidatePlans: request-local cache
}
CandidatePlan {
  modelPathRef, routeConnectionRef, effectiveInvocationRef
  compatibilityPlan, requestPipelineRef, responsePipelineRef
  encoderRef, decoderRef, rendererRef, authBindingRef, transportBindingRef
  resourceBudget, replaySafety
}
```

These plans contain references to immutable graph/catalog state and compact
request-local decisions. They do not copy the entire model graph or prefetch
credentials. Runtime health/admission is rechecked before an attempt; changed
admission can skip a candidate without changing its pinned semantic contract.

The planner composes mappings in actual execution order. A semantic value is
preserved only if every stage accepts the previous stage's output and preserves
the required invariant. The result is the most restrictive disposition on
the complete path: any required unknown/unsupported mapping rejects it; any
permitted degradation makes it lossy; otherwise translation makes it
translated; native requires a proven native path. Conditions must resolve
against current capability evidence before eligibility is granted.

The semantic interfaces are:

```text
IngressContract.Describe(invocation, clientContract) -> MappingReport
EncoderContract.Negotiate(invocation, candidate, evidence) -> MappingReport
DecoderContract.Describe(operation, candidate, framing) -> EventContractSet
RendererContract.Negotiate(clientContract, events, artifacts) -> MappingReport
TransformContract.Describe(binding, semanticContract) -> MappingReport
CompatibilityPlanner.Build(reports, requirements, policy) -> Plan | Rejection
```

Feature requirements carry an exact, versioned evaluator reference; route
capability evidence remains keyed by semantic feature ID. Feature evaluator
descriptors live in the frozen shared extension catalog, bind through the
catalog factory, and may pin an input schema for requirement constraints.
Unknown evaluator versions and invalid constraints fail closed. Strategy and
ranker descriptors/factories now join the same catalog, and schemas are pinned
per strategy contract version; exact refs for the same strategy ID can coexist.
Physical and Combo policies persist the exact strategy ref with their options;
CRUD, bundle apply and snapshot compilation resolve that ref without ID-only
fallback. The frozen strategy module's options schema validates configuration,
and the selected strategy receives the validated options in its per-model
state. The common catalog does not restrict every policy to integer options.
Neither evaluator nor codec negotiator may mutate the snapshot or perform
remote health checks to prove support.

Default permission is no semantic loss. Explicit client intent or model
policy may grant named losses; grants are constrained by all configured
server/node ceilings. Explicit denials and required exact facets accumulate
and dominate grants. A child cannot weaken an ancestor's denial. Credentials,
issuer ownership, tool-call correlation and response commitment are hard
invariants and cannot be waived. Loss records include requested/effective
values, semantic paths and the policy source that permitted the change.

Physical and Combo nodes currently persist typed `LossPolicy {allow, deny}`
through SQLite, secret-free config bundles and the immutable serving snapshot.
Grants accumulate down the selected graph path; denials accumulate and win.
An optional server ceiling can further deny IDs or set `allowOnly` to constrain
the complete set of model grants; an enabled empty allowlist denies every loss.
The compatibility planner admits a lossy mapping only when every reported loss
ID is granted and not denied, and durable usage records retain the resulting
fidelity plus each loss's requested/effective values, semantic paths and
granting model-node sources. The server ceiling is exposed through the
`compatibility loss-ceiling` CLI and portable config bundle v5, and its changes
participate in bundle dry-run diff. Safe provider definitions are embedded in
v5; opaque or potentially sensitive definitions remain external exact module
dependencies and are never copied into the secret-free bundle.

Reasoning `exact`, `allow_clamp` and `best_effort` bind to this policy as specified
in [Physical models](PHYSICAL_MODELS.md#strictness-and-translation). Explicit
client intent and selected presets retain their precedence over Combo,
Physical and provider defaults. Administrative forcing is separately labeled.

Request and possible response semantics must both negotiate before dispatch.
An encoder cannot defer a known incompatibility to an upstream 400, and a
renderer cannot declare all providers compatible merely because it can emit
text. Route explanation and usage retain the selected plan's reasons/fidelity;
the planner does not modify durable user order.

Generic rejections carry operation, request ID, code, semantic paths, reasons
and retry/effect information. The ingress/client boundary renders these into
its wire error schema with secrets redacted. A new client dialect registers
its error renderer alongside its normal renderer; it does not add provider
error branches to HTTP handlers.

Native passthrough requires matching wire/framing contracts, a decoder that
can provide bounded semantic observations, and no active response mutation.
An observational extension does not invalidate it. A semantic mutation forces
semantic rendering; if the renderer cannot preserve the remaining facets,
reject the candidate. Raw frames are an optimization artifact, never a way to
ignore a transform. Unexpected upstream framing invalidates the planned mode
and must be handled before client commitment or reported as a stream error.

## 4. Transform bindings and execution lifecycle

```text
TransformDescriptor {
  descriptor: ExtensionDescriptor
  stage: invocation.request | attempt.request | response.semantic
  allowedScopes[], effects[], requiredFacets[]
  failureMode: fail_closed | safe_fail_open
}
TransformBinding {
  instanceId, extensionRef, enabled, stage, scopeRef, order, options
}
```

Request effects cover prompt, operation input, tools, thinking, generation
options and portable content. Response effects cover declared semantic event
payloads. Identity, auth leases, issuer-private artifacts, event correlation,
client contract and cancellation are immutable. Module options and effects
are validated at configuration publish; undeclared mutations fail explicitly.
Transform implementations are instantiated from the frozen extension
catalog independently of binding options. The catalog validates each exact
binding ref and its options schema; the same stateless implementation receives
the validated options at apply time rather than capturing one binding's
configuration in shared mutable state.

Invocation transforms bind to daemon/model policy and run before candidate
selection. Bindings compose in stable scope order: daemon, requested model,
nested model path, then Physical; within a scope use `(order, instanceId)`.
Each branch starts from the immutable ingress invocation. Shared path prefixes
may be memoized within that request so fallback does not compress or append
prompts twice. Existing prompt/default precedence is preserved, with origin
and binding provenance retained.

Prompt origin is evidence, not a guess from a `system` or `developer` role.
Ingress retains original order/roles and records the known source; harness
attribution needs a trusted configured source. Provider and administrative
layers come from explicit bindings. Compression must preserve the declared
instruction order/authority and report any permitted semantic degradation.

Attempt transforms bind to provider/connection and run on a candidate-local
copy after provider prompt/default composition, before final requirement
compilation and compatibility negotiation. They may adapt semantics but
cannot sign requests or select routes. Required features are recomputed from
the effective payload after each request stage. Explicit externally supplied
requirements remain constraints; a transform cannot erase a hard requirement
to make an incompatible route eligible.

Response chains bind to the chosen path and are pinned in its plan. Their
order is explicit and independent of registration order. Decoding first
produces original provider events for accounting/outcome evidence; transforms
then produce client-visible events. A client usage projection cannot rewrite
measured provider usage, quota evidence or billed accounting.

Transforms are transactional: apply to an isolated working copy and publish
only after validation. `safe_fail_open` is legal only when the descriptor
allows it, the binding enables it, the original input remains intact and
skipping preserves all hard requirements. Fail-open is observable. A response
transform may fail open only before any output for its affected atomic unit;
it cannot retract streamed deltas. Cancellation, secret-scope violations and
identity violations always fail closed.

Execution rejects invalid pipeline bounds at configuration time. A request
budget caps CPU work, buffering and extension deadlines. Implementations must
cooperate with cancellation; arbitrary in-process Go code is not a sandbox
and cannot be forcibly made safe by a timer. Blocking external work belongs
in bounded workers/processes with cancellable I/O. Observer queues remain
bounded and independent of the rendering path.

The serving sequence is:

```text
ingress decode/validate → immutable invocation
  → model-path defaults + invocation transforms → intrinsic requirements
  → candidate-local defaults + attempt transforms → final requirements
  → full compatibility plan + entitlement/health/limit eligibility
  → prune ineligible graph members; strategy orders eligible members
  → recheck runtime admission → auth lease → encode/resolve endpoint
  → auth apply/sign → transport → decode original events/outcomes
  → planned response transforms → planned renderer → client
```

Eligibility preparation may inspect candidates but preserves the graph's
policy boundaries. Plans never flatten nested Combo policies. Compact
provider attempt observations are emitted even when rendering/transforming
fails; client success and upstream success are distinct outcomes.

## 5. Auth driver and interactive session

Credential use and interactive setup are related contracts with separate
lifecycles:

```text
AuthDriver {
  descriptor, SetupSchema
  ValidateCredential(secretState) -> validation
  Acquire(connection, secretHandle) -> scoped CredentialLease
  Apply(preparedRequest, lease) -> authenticated/signed request
  Refresh(lease) -> replacement secret state
}
AuthorizationDriver { optional companion registered by the auth descriptor
  Start(context, setupInput) -> AuthorizationTransition
  Advance(context, privateState, AuthInputEvent) -> AuthorizationTransition
}
AuthorizationTransition {
  state, publicActions[], pollAfter?, expiresAt
  privateStateRef?, resultSecretState?, error?
}
```

`Apply` occurs after payload/endpoint preparation and before transport so
header auth, request signing and body-bound schemes share one execution
boundary. Credential leases are opaque, connection-scoped, expiring and
redacted. Providers with non-token credentials store their own typed state
behind the same secret handle; they do not add credential columns to the
kernel or force every auth method into an access-token field.

The daemon coordinator owns the state machine. The current authorization-code
slice exposes `auth.authorization.start`, `auth.authorization.complete` and
`auth.authorization.cancel` over local IPC (with headless CLI commands). Start
returns a consent URL and opaque one-use session ID; the frontend opens the
URL, then submits either the callback URL or code/state. The daemon holds the
verifier in memory, enforces S256 PKCE, a ten-minute session TTL, a cap of 64
pending sessions, one pending session per connection, bounded callback/code
sizes, redirect-target matching and a bounded exchange deadline, state
matching and replay rejection.

OAuth definitions may additionally declare `deviceAuthUrl`. The same auth
primitive then supports `auth.device.start/get/cancel`: the daemon starts the
RFC 8628 exchange, keeps `device_code` private, and runs a cancellable polling
worker using `golang.org/x/oauth2` device authorization support. The frontend
receives only user code, verification URLs, expiry and public status; polling
continues if the UI disconnects. Cancellation and daemon shutdown stop the
worker. A delayed authorization or refresh persists through a compare-and-swap
against the connection secret observed at start, so a newer credential is not
overwritten. On success the daemon reloads serving state.

The CLI and TUI expose both flows. For an HTTP loopback redirect URI, the
daemon binds only the configured loopback address, routes the callback by the
one-use state, verifies the exact redirect target, exchanges the code and
returns a fixed non-secret completion page. Listener references are shared
when multiple auth definitions use the same local port and released on session
completion, cancellation, expiry or daemon shutdown. HTTPS/non-loopback
redirects remain manual handoffs rather than causing the daemon to bind a
public interface. The TUI polls only the public authorization status; the
callback code, PKCE verifier and token exchange remain daemon-owned.

The daemon coordinator owns the state machine:

```text
created → awaiting_user | polling | exchanging
        → completed | failed | expired | cancelled
```

The driver reports transitions and protocol-specific errors. The coordinator
owns deadlines, bounded polling, backoff, callback dispatch, serialization and
atomic secret persistence. Poll intervals honor the driver's slow-down/backoff
result. A device flow can poll while no frontend is connected. Authorization
code flows use coordinator-owned, one-use state and PKCE with the real S256
challenge; callbacks reject mismatched, expired or already consumed sessions.

Public actions are tagged values: `open_url`, `show_user_code`,
`collect_fields`, `wait` and `complete`. Field values bind to a versioned
schema, including numeric/boolean/object values where declared. CLI/TUI/HTTP
clients render these actions; browser opening is optional. Public session
views contain no verifier, device token, refresh token or raw private state.

Generic control commands are start, get, submit and cancel by session ID.
Callbacks are routed internally through the same session advance path. There
is one active setup session per connection; a second start returns its ID and
a conflict until explicitly cancelled. Sessions carry a connection generation
to reject stale completion. Existing credentials remain usable until a
successful, validated replacement is committed atomically.

Pending interactive sessions are bounded, ephemeral and expire on daemon
restart by default. Durable connection credentials and refresh state survive
restart. Session resumption is an optional declared lifecycle capability with
its own fixtures; it is never inferred from having a refresh token. Terminal
sessions are retained only for a bounded diagnostic period.

Credential refresh is single-flight per connection. Refresh never overrides
attempt replay restrictions. A refreshed key is persisted only with the
expected credential generation, so concurrent setup/refresh cannot restore
superseded credentials. No global lock spans auth network I/O.

## 6. Attempt replay and commitment

Client commitment and upstream effects are separate boundaries. Each operation
declares one of `safe_before_dispatch`, `confirmed_rejection_only`,
`scoped_idempotency_key` or `unsafe_after_dispatch`; unknown is treated as
`unsafe_after_dispatch`. Runtime observations carry an explicit effect:
`not_dispatched`, `confirmed_rejection`, `accepted` or `unknown`.

Before dispatch, another eligible candidate can be tried. After dispatch,
`safe_before_dispatch` and `unsafe_after_dispatch` prohibit retry.
`confirmed_rejection_only` permits retry only when a classifier explicitly
establishes rejection (for example, a recognized 429); network errors, timeout,
5xx and unknown effects do not qualify. A 2xx response followed by decode or
render failure is still an accepted upstream effect, even if no client bytes
were committed, and does not authorize a duplicate call. `scoped_idempotency_key`
is usable only when ingress supplies a validated `Idempotency-Key` and the
selected provider-operation binding declares the upstream header that honors
it, and registration rejects that header unless the exact operation contract
declares `scoped_idempotency_key`. Gobroom hashes the client key with the exact provider definition,
node/endpoint, credential, protocol, upstream model and operation, then retries
at most once on that same route for an ambiguous transport or explicitly
retryable unknown-effect response. The derived header is redacted from logs and
is never stored in usage/config. Missing client proof or provider header
binding fails closed. An idempotency key does not make cross-provider fallback
idempotent. Replay permission is operation/binding specific and is never
inherited automatically by image/job/tool execution.
An ambiguous outcome reports replay suppression instead of launching a
potentially duplicate upstream job.

After client write/flush/commit, retry and fallback are forbidden regardless
of replay safety. Precommit failures are classified before sending a client
error; an error frame must not accidentally commit a failed attempt while a
safe fallback remains available. Postcommit errors are represented in the
selected client contract and terminate the stream.

Async upstream jobs can expose creation/status/cancellation as registered
operations linked by a scoped job artifact. Any long-running orchestration
worker is optional and uses those same operations. It introduces no second
model category or provider-specific loop in the kernel.

## Closure criterion

The design decisions above are fixed for the current product scope. Future
behavior implementation adds modules, descriptors, bindings and fixtures.
If a behavior violates this contract, it must expose the exact uncovered
semantic requirement rather than smuggling a branch into the kernel.

Runtime architecture closure requires the stronger M15 simulations: genuine
interactive authorization, a non-chat payload/result, facet compatibility,
issuer-scoped artifacts, native-transform interaction, config dependency
round trips and replay safety. Green registration/composition tests alone
do not establish that gate. Full vendor parity and performance benchmarks
remain separate acceptance axes.

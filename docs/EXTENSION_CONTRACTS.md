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

Client wire formats use the same extension boundary. A module may register a
versioned `ClientIngress` with exact routes and an exact operation ref; it
decodes client bytes into the shared operation request IR. The daemon installs
those routes into its existing API server, which retains authentication,
request bounds, executor, cancellation and error mapping. A client ingress
cannot call a provider directly or bypass compatibility planning. Its
descriptor depends on the operation contract it produces.
The public `pkg/provider` API exposes the reusable JSON-envelope and
schema-bound multipart artifact ingress codecs as data-driven implementations;
custom client dialects implement the same interface when those shapes do not
fit. Multipart files are streamed into bounded leases, never copied into the
canonical request IR.

Each linked module declares an opaque, exact build version and a positive
GoBroom host-contract version. GoBroom does not infer semver compatibility:
the pair is compared exactly. Descriptors registered during module setup carry
that owner, and a dependency lock pins only modules reachable from its exact
extension roots. Bundle validation therefore rejects a missing module build
or host contract before configuration is applied. Module code remains
statically linked; this pin records the required implementation, it does not
download, dynamically load or distribute executable code. Module registration
is synchronous startup composition, not a concurrent hot-plug API.

The public Go authoring surface is `github.com/fm39hz/gobroom/pkg/provider`;
custom binaries host statically linked modules with
`github.com/fm39hz/gobroom/pkg/daemon.NewWithProviderModules`. Modules register
implementation code once, while ordinary provider definitions remain separate
JSON manifests that reference exact primitive/task versions. This is a
compile-time extension model, not runtime `plugin.so` loading or a remote code
execution mechanism. The external-module example and its daemon-hosting test
are tracked in M15 conformance. Module resolution is intentionally ordinary
Go dependency resolution at build time; a host application declares the module
dependency, passes its implementation to the composition root and statically
links it. A config bundle cannot fetch, install, or execute module code.

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
input ports. An operation may bind a versioned artifact-interpreter ref and
options to an input role. The interpreter streams only that artifact through a
bounded, cancellable lease and returns facts checked against its registered
result schema; an artifact-requirement compiler maps those facts into exact
feature requirements before route eligibility. The daemon caches validated
facts in the request snapshot, not body bytes, so candidate fallback does not
rescan large inputs; facts are stripped before adapter compatibility planning
and provider request encoding. The selected provider boundary independently
rechecks recipient policy before a codec can open the lease.
This proves the generic binary path and manifest-selected multipart wire
framing end-to-end against a deterministic fake provider. It is not proof of
provider-native multimodal JSON extraction or real service-specific framing;
those remain adapter contracts.

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
Provider-signed response events carry the exact provider-definition issuer ref;
the kernel rejects missing or mismatched issuers before rendering, and response
transforms cannot rewrite that provenance. A renderer may preserve a signature
only when the response decoder declares the signature event family.

Response termination keeps fault origin explicit: provider, transform,
renderer, client, or unknown. `ResponseFailure` unwraps its inner error, so
standard `errors.As`/`errors.Is` checks still reach typed causes; wrapping an
already attributed failure preserves its inner origin. Only an evidenced
provider-origin failure can penalize operation health. Caller cancellation
and ordinary deadlines are neutral. An operation deadline uses
`ErrOperationDeadline` and counts as timeout only while the failure is still
attributed to provider work; a later local transform or render failure does
not become a provider timeout. Ambiguous validation after a transform remains
unknown rather than being guessed as provider fault. Renderer failures are
neutral unless their wrapped cause establishes provider or client origin.

The response pipeline latches the first callback failure and returns it even
if a decoder ignores the callback error. It checks cancellation at response
boundaries, budgets original provider events separately from transformed
client events, and prevents response-writer writes after cancellation. Raw
provider usage is reported through `OnProviderUsage` before transformation
and also from decoder completion when available. The kernel stages successful
completion until rendering returns, required artifact counts validate, scope
cleanup succeeds, and cancellation checks pass. It then publishes one terminal
success result; on failure it retains reported nonzero provider token/cost
counters in one `error` or `cancelled` usage event, without a success health or
performance observation. Unreported usage is not inferred. A 2xx accepted
upstream effect still forbids fallback after a response fault, regardless of
whether client bytes were committed.

Opaque tool signatures echoed by clients are untrusted until the selected
provider's session store contains the matching signature-digest, issuer,
conversation-context and target-call proof. Context is the normalized
prompt/tool contract and message prefix before the signed tool-call turn;
signatures in earlier messages are excluded from that fingerprint. The target
proof binds the tool-call ID, name and canonical arguments. The raw signature is
never persisted; a different provider, connection, model, operation, client
session, conversation branch or tool call cannot attest it. Provider and
operation contract versions are part of that isolation boundary.

Session-store implementations must derive their namespace with
`provider.SessionStoreKeyFor` (or the kernel equivalent). The exact isolation
key includes connection ID, stable principal generation, provider-definition
ref/version, operation ref/version, physical upstream model, configured endpoint
and client session ID. Renewal changes credential material, not principal
identity; replacing an account must not inherit its predecessor's response IDs,
provider data or signature proofs. An empty endpoint denotes the selected exact
definition's default endpoint, not a wildcard matching configured endpoints.
A store must not broaden this scope to a shared tool-call ID or client session
alone. The current data plane has one configured bearer/security domain, not a
multi-tenant identity system. Independently authenticated consumers will need
caller-scoped conversation ownership before sharing a session store.

Durable implementations use the typed `provider.SessionStoreNamespace` codec,
not a delimiter-built key. SQLite verifies the live principal, definition and
configured endpoint within the write transaction, so queued writes from a
replaced account cannot recreate usable continuity. Preload discards unsupported
or stale namespaces instead of adapting pre-release state. Credentials and
provider/model/Combo configuration are not part of this disposable cache.

Decoders publish private state using the existing `StreamHooks.OnSessionState`.
Updates are staged until decoding succeeds; invalid, failed or late decoder
updates cannot publish usable continuation. Nonempty fields merge; a completely
empty state explicitly clears all prior response IDs, opaque data and proofs.
`ExpiresAt` limits provider continuity and is capped by the store TTL. The
kernel persists only successful attempts after response rendering completes.
This does not authorize retrying an expired upstream response reference or
replaying a committed stream: classified continuity-error recovery remains a
separate policy/implementation gate. Module host contract v7 requires these
scope/reset semantics; no old-host adapter is retained.
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

The kernel passes the exact admitted candidate plan to provider preparation via
an optional `CompatibilityPlanPreparer` contract. A composed codec may consume
that plan through `PlannedRequestCodec` when an approved, explicitly named loss
changes the effective provider request. The kernel stays provider-agnostic:
the codec must validate that the planned requested/effective values match the
typed invocation, and the resulting usage event retains the same loss record.
Codecs that need no plan continue to implement the ordinary request codec.

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
`compatibility loss-ceiling` CLI and portable config bundle v9, and its changes
participate in bundle dry-run diff. Safe provider definitions are embedded in
v6; opaque or potentially sensitive definitions remain external exact module
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

The daemon's local IPC control plane owns transform-binding list/upsert/delete.
Each mutation validates the complete candidate binding set against the frozen
extension catalog before persistence, then publishes a new routing snapshot.
Frontends use this typed boundary; they do not edit transform tables or
rebuild the runtime catalog themselves.

Request effects cover prompt, operation input, tools, thinking, generation
options and portable content. Response effects cover declared semantic event
payloads. Identity, auth leases, issuer-private artifacts, event correlation,
client contract, ingress unsupported-facet evidence and opaque metadata are
immutable. Raw client request and source-extension maps cannot be mutated by
transforms; modules change canonical IR, then egress codecs overlay only typed
facets recorded as modified. Tool choice is governed by the tools effect and
normalized generation settings by options. Module options and effects are
validated at configuration publish; undeclared mutations fail explicitly.
New conversation messages may be appended only as canonical IR: message and
content-part provenance metadata, tool-call metadata and provider-private call
data must be absent. User/assistant/system/developer content must be text or
typed content parts; decoded `[]any` wire blocks and untyped objects cannot be
smuggled in as canonical content. Structured tool-result payloads remain a
separate role-specific value and each egress codec must encode or reject them
explicitly. This prevents adapters from mistaking an added item for a
source-backed wire item and silently retaining or dropping the wrong envelope.
The kernel owns this mutation set; transforms cannot set it themselves. A
codec preserves the source wire object, then replaces only marked facets using
the canonical request contract. This keeps unrelated provider extensions
untouched while preventing an active typed transform from being silently
ignored by a native-format adapter.
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

Transform chains are transactional: request transforms compose on a private
working copy and the chain publishes only after every enabled step validates;
response transforms likewise return no partial chain result on failure.
`safe_fail_open` is legal only when the descriptor
allows it, the binding enables it, the original input remains intact and
skipping preserves all hard requirements. Fail-open is observable. A response
transform may fail open only before any output for its affected atomic unit;
it cannot retract streamed deltas. Cancellation, secret-scope violations and
identity violations always fail closed.

Each transform descriptor carries input/output byte bounds, a maximum buffered
payload size and a deadline. The registry checks serialized input before
copying it, executes with a deadline context, and checks serialized output
before publishing it. Unset transform bounds use a 1 MiB buffer/input/output
ceiling and a 250 ms deadline. Resource-limit and deadline skips are distinct
safe-fail-open evidence; client cancellation always remains fail-closed.
These are payload and cooperative-time bounds, not a security sandbox: an
in-process Go transform that ignores context can still occupy its caller, and
an implementation can allocate temporary memory before returning an output
that is rejected. Untrusted or hard-CPU/memory-bounded extensions require an
isolated process/runtime with bounded I/O. Blocking external work belongs in
bounded workers/processes with cancellable I/O. Observer queues remain
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

Authentication is selected per connection, not fixed on the provider node. A
provider definition declares one or more named credential modes; each mode
binds an exact Auth primitive/options contract and an explicit credential
presentation contract. The definition also names a default mode for setup UX,
but that default is not a restriction on subsequently added connections:

```json
{
  "defaultCredentialType": "api_key",
  "authModes": [
    {
      "credentialType": "api_key",
      "auth": { "kind": "auth", "id": "static-secret", "contractVersion": 1 },
      "presentation": { "header": "Authorization", "prefix": "Bearer " }
    },
    {
      "credentialType": "oauth2",
      "auth": { "kind": "auth", "id": "oauth2", "contractVersion": 1 },
      "authOptions": { "oauth": { "clientId": "…", "authUrl": "…", "tokenUrl": "…" } },
      "presentation": {
        "header": "Authorization", "prefix": "Bearer ",
        "headers": { "anthropic-beta": "oauth-2025-04-20" }
      }
    }
  ]
}
```

These JSON field names are the version-1 provider-definition contract. A connection's
`credentialType` selects both the auth flow and how its resolved credential is
presented. The route snapshot binds that selection once, so refresh, usage,
quota, inference and health-generation fingerprints all refer to the same
connection mode. Validation rejects an undeclared mode. Empty credentials are
accepted only when that selected auth flow advertises the matching interactive
bootstrap capability. The provider-node default exists only to preselect a
mode in setup forms; it must never reject another declared mode.

Credential presentation is declarative and applied at a shared request
boundary after protocol encoding. Provider adapters must not branch on
`api_key` versus `oauth2`, nor hardcode an auth header when a route declares a
presentation. The presentation contract supports a token header/prefix and
non-secret static protocol headers; it cannot contain secret values. More
complex request signing remains a transport/auth execution extension, not
provider-specific kernel logic. An Auth flow may also return
`Credential.RequestHeaders`: runtime-only headers derived from that exact
connection's credential state (for example, a device ID). They are merged
after mode-level static protocol headers and before the declared token header;
reserved transport headers, invalid names and CR/LF values are rejected. These
dynamic values are never persisted or exposed in control-plane metadata. The
credential also carries a bounded string-valued `AuthContext` for request
codecs/transports that need connection-scoped identity such as `userId`,
`machineId`, or organization IDs to construct provider-specific wire requests.
The kernel does not inspect or route on those keys, and does not log or persist
them. A provider's auth primitive and request codec own their interpretation.
same resolved credential mode and dynamic header output are passed to
quota/model-discovery requests; an explicitly configured per-report
credential header may override the default token presentation where the
provider API requires it. Static presentation headers reject
credential-bearing/reserved names.

TUI/CLI catalogs expose the available modes, safe labels and per-mode setup
schemas/capabilities, never auth options or secrets. A connection form selects
one mode first, then renders only that mode's fields and authorization actions.
Discovered models remain attached to their actual connection, so two modes of
one provider can have different catalogs, entitlements, health and priority.

The current implementation binds each definition's `authModes` in the runtime
catalog, routes by the connection's `credentialType`, carries the selected
presentation into the immutable route, and applies it through shared helpers
for chat adapters, model discovery and quota requests. CLI/TUI catalogs expose
per-mode setup metadata; the TUI `ctrl+o` cycles the selected mode and rebuilds
the mode-specific fields. Empty credentials are accepted only for a selected
mode with an advertised interactive flow. Dual-mode snapshot and setup-form
fixtures cover selection. Remaining M7 work is the broader live-provider and
OAuth lifecycle acceptance matrix. Body-bound signatures are provider
request-codec/transport implementations over `AuthContext` and the encoded
request; the kernel does not need a provider branch, but each such implementation
still needs its own bounded/replay-safe fixture before claiming support.

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

The current executable auth boundary separates these values:

```text
AuthInput                         provider-owned persisted bytes + connection/mode
AuthFlow.Resolve(input)           -> ephemeral kernel.Credential
AuthFlow.Refresh(input, resolved) -> provider.CredentialState
Code/device completion            -> provider.CredentialState
daemon                           -> validate state -> Resolve -> revision CAS
```

`CredentialState` is a private encoded value constructed through
`NewCredentialState`; it requires 1..64 KiB and redacts formatting and JSON.
Only an explicit `Encoded()` crosses the secret-store boundary. The provider
owns its typed JSON/binary/text binding, validation and serialization. The
daemon never infers OAuth from runtime fields or rewrites the provider document.
Before CAS it resolves the replacement through the same selected auth primitive,
validates bounded runtime headers/context, and rejects late success after
cancellation. Invalid/zero/oversized/unresolvable state leaves the old revision
untouched. An unchanged refresh state does not cause a credential rewrite.

`kernel.Credential` has no refresh-token/client-ID/client-secret fields. It
contains the current wire secret, bounded runtime headers/context, generic
expiry and an opaque connection generation. A changed generation/context/header
can authorize the single allowed auth retry even when the bearer secret stays
the same; existing operation replay and issuer restrictions still apply.
OAuth alone owns `OAuthTokenState`, its encode/decode helpers and the existing
`x/oauth2` exchange; those helpers/factory are public for module composition.
The static-module host contract is version 6 for opaque-state, explicit
credential recovery, a single action-based interactive lifecycle and explicit
HTTPS callback receiver negotiation;
old module contracts are rejected, not adapted. Interactive sessions are not
durably resumed after restart.

### Action-based setup

An auth primitive can also implement `AuthorizationFlow.BeginAuthorization` and
`AdvanceAuthorization`. Its transition contains a completed opaque
`CredentialState`, a typed terminal denial/expiry/unavailability, or opaque
`AuthorizationState` plus bounded public actions
and/or a poll interval. The initial public action kinds are `open_url`,
`display_code` and `input`; input binds an exact, registered schema ref from the
existing frozen JSON Schema catalog. This is no provider-name switch, shell
execution, arbitrary URL fetch, filesystem access or frontend token exchange.
An import can submit a bounded JSON document; streamed binary setup artifacts
are not part of this initial action contract.

The daemon owns connection reservation, private state, step revision, expiry,
single-flight advancement and timers. `auth.setup.start/get/advance/cancel` is
available over control IPC; `connections setup start/get/submit/cancel` consumes
it. Submit requires the current revision and an offered input-action ID. Invalid
input is rejected by its registered schema before the driver and does not consume
the step. Schema diagnostics and driver error text are not echoed, because they
can contain input secrets. Input comes from a bounded JSON file/stdin, never a
secret-bearing argv option. Private state is redacted in formatting/JSON and is
not retained in public status or logs.

`auth.setup.start` accepts typed `mode: new | resume` (default `new`). `new`
rejects a busy connection. `resume` returns its existing active session and
current public revision, or starts one if no active session exists. Admission
and lookup share the same coordinator lock; no second Begin/consent/token call
is performed for a valid active session. CLI `connections setup start --resume`
and a fresh TUI's `D` use this mode, so reattachment needs only the connection ID.

Reattachment waits, without holding the lock, for an unpublished Begin or a
submitted step to finish. Its own bounded caller context cancels only that
observation, not the owner call. Daemon-owned poll/await work leaves its already
published instructions immediately available. Live credential revision,
principal, mode and definition ownership fence reuse; a changed owner retires
the old session/resources and requires an explicit retry rather than silently
starting a new flow on the same request. Public views never include retained
form values or private driver state.

This is frontend reattachment to daemon memory, not durable interactive-session
recovery. Daemon restart drops pending challenges and grants; `resume` then
creates a new session, while old handles are rejected. Saved credentials and
activation receipts retain their separate durable lifecycle.

There are at most 64 retained authorization sessions shared with code/device
setup, at most eight actions per transition and 64 KiB private/input documents.
A generic session cannot exceed one hour or extend its provider-declared deadline.
The reusable code strategy defaults to ten minutes. Poll intervals are
100 ms..one minute and only run for active sessions that request them; no poll
timer exists for ordinary input-only waiting. Provider network calls are bounded,
cancellable and never hold the coordinator mutex. Cancellation/shutdown fences
late completion; final credential validation, owner/revision CAS and activation
use the existing shared commit path, with principal replacement semantics.

This is not full C4/SG2 closure. Code and device setup share the action engine;
no old coordinator/schema/command compatibility adapter is retained.
Inline generic TUI editing and scoped credential leases are implemented. The
complete composition/fault matrix remains open. A flow can already parse a
cookie or service-account state through `AuthFlow.Resolve`; the new seam addresses
multi-step interaction and ownership rather than inventing a special credential
type for every setup method.

### Callback grants and receiver policy

A continuing transition may declare up to eight private `AuthorizationCallback`
grants, each bound to an offered input-action ID. A grant declares an HTTP
loopback or HTTPS URL/path/static query, a named opaque correlation value and permitted
response query names. The grant object is redacted in formatting/JSON; public
callback modes do not dump its target or correlation data. Drivers may publish
an authorization URL as a separate public action. Drivers generate a
fresh, unpredictable correlation value for each new callback challenge and retain
their own protocol validation, including manual handoff. No OAuth field name is
special in the generic dispatcher.

The shared listener registry acquires the actual loopback socket without a
probe/close/rebind window, shares a canonical bind address across owners, and
atomically replaces one owner's grants. A required bind failure rolls back
tentative reservations without dismantling the previous grants. An optional
failure exposes `manual` in `callbackModes` and grants no HTTP callback. Public
views contain only action IDs and `loopback`/`manual`/`proxied_https` modes, not
correlation data. Plain HTTP non-loopback and wildcard bind targets remain
forbidden. HTTPS intent is not listener authorization. An explicit HTTP port zero is
a resource request, never an active grant or a published callback URL.

For a port-zero transition, the daemon reserves actual sockets and sends a
private typed `callbacks_ready` event containing `AuthorizationCallbackResource`
values (action ID, resolved URL and mode). The driver can then register the
allocated redirect URI with its issuer and construct consent actions. These
calls share the admitted step's context/deadline and worker ownership; no
coordinator/registry lock spans the driver call. Intermediate states/actions are
not published as UI revisions. At most eight negotiation rounds are permitted,
and a round cannot extend the session deadline.

Reservation is not activation: matching HTTP callbacks are rejected until the
driver returns a final transition and the input schema/revision gate is active.
Activation must preserve the reserved action, exact resolved target, correlation,
response fields and required/optional policy. The socket is transferred into the
active grant without close/rebind; unused reservations are pruned. Existing fixed
grants and private resources use the same listener registry. Optional fixed-port
failure may produce manual mode; a dynamic allocation failure cannot produce a
manual port-zero URI and fails closed. Error, cancellation, terminal completion
and shutdown release both pending reservations and active grants.

`callbacks_ready` is an internal driver event, not an IPC/user action. Its
resource data is redacted in formatting/JSON and excluded from public status or
portable configuration. A driver may deliberately expose the completed consent
URL as a public action. Typed resource values and standard URL/form binding are
used instead of string placeholders or frontend port selection.

For HTTPS, an opt-in daemon `CallbackIngress` binds an explicit private loopback
HTTP socket before IPC publication. A configured HTTPS public origin is its
only receiver authority; an external TLS proxy must preserve public Host.
Forwarded headers do not select the origin or rewrite a grant. Unconfigured or
different-origin HTTPS intents produce manual resources only when optional;
required callbacks fail before consent. The ingress is a separate bounded HTTP
server, never the data/admin mux, and needs no API bearer token: the registered
nonce/revision grant is the callback capability. It is disabled by default.

HTTP delivery requires GET, a loopback peer, matching HTTP(S) origin/escaped path/static
query/correlation and only declared response fields. DNS hostname case and default
port spellings follow origin semantics; the driver receives its original registered
URI target spelling, not an origin reconstructed from forwarded headers.
Malformed, duplicate,
unknown and ambiguous query matches fail closed before driver invocation.
The callback is encoded as typed `AuthorizationCallbackInput{url}` and delivered
through the pinned action revision, registered input schema and normal auth-owner/
credential CAS checks. An already published step can still be returning to its
caller; the callback waits on that step's completion channel with a bounded
context, not a polling loop. Driver invocation never holds the registry lock.

Completion, denial, cancellation, expiry and shutdown revoke grants and close the
last actual socket reservation. An admitted callback still obeys cancellation
and the daemon's worker drain, so a late successful driver cannot persist a token.
The explicitly configured ingress listener belongs to the daemon lifetime, not
to the last grant; idle ingress has no active grant and releases its socket on
Stop. The default-disabled configuration creates no such listener.
The HTTP page acknowledges receipt; setup status and credential activation remain
separate outcomes. The previous OAuth-only listener runtime is removed: the code
flow uses the same registry and its driver owns OAuth interpretation. Code and
device orchestration already share the action engine.
There is no unknown-callback forwarding or fallback to whichever session is active.
If Start fails before producing a public session handle, its failed reservation
does not occupy the retained-session budget. Failures after a handle was issued
retain their public terminal status through the usual bounded retention period.

All interactive flows use `auth.setup.start/get/advance/cancel`. The specialized
code coordinator, code capability metadata/interfaces, daemon state/verifier/
redirect fields and `auth.authorization.*`/`authorize-*` controls are removed.
`NewAuthorizationCodeDriver` composes provider-owned authorize/exchange hooks.
It uses the pinned OAuth verifier generator, opaque typed private state, exact
registered input schema and typed denial/expiry outcomes. Provider code owns
state/S256/redirect/query interpretation; the daemon only owns generic resources,
revision admission, deadlines, cancellation, CAS and activation.

Code input is either a callback URL or an explicit code/state object. The reusable
strategy rejects incorrect targets, changed fixed query, unknown/duplicate fields,
wrong state and oversized values before exchange. `AuthorizationInputRejected`
is a safe recoverable input error: the current challenge survives and no exchange
has run. Driver/exchange errors, cancellation and owner changes remain terminal.
Local redirects request `PrepareCallbacks`, including fixed-port registration,
so the actual listener is held before any consent-building/issuer-registration
hook. The ready event carries its resolved URI; the final callback grant must
preserve that exact authority. Remote HTTPS redirects use manual input without a
daemon listener. Public actions never contain the verifier or resulting token.

OAuth definitions may declare `deviceAuthUrl` without an authorization-code URL.
The factory composes `NewDeviceAuthorizationDriver` over the existing OAuth
implementation. It produces public code/URL actions and private `Await` intent;
the same `auth.setup.*` coordinator owns reservation, expiry, worker lifetime,
CAS and activation. `auth.device.*`, its separate coordinator/session fields,
device capability metadata and the three device-specific CLI commands are removed,
not aliased. If both methods are configured, the OAuth factory exposes a typed
`select_method` input action. `authOptions.oauth.setupMode` can explicitly choose
`code` or `device` to avoid that selection step; `auto` selects the sole available
method or offers both. Invalid/unconfigured modes fail validation, not fallback.

`Await` schedules one cancellable background driver call with the session
deadline, rather than the short interactive-RPC timeout. It inherits daemon-owned
context values, not the initiating frontend's lifetime. Queued and admitted work
are both tracked before shutdown drain. Await cannot also poll or expose input/
callback actions; those would compete for the same single-flight session. Protocol
polling remains in the provider implementation: RFC 8628 uses the pinned
`x/oauth2.DeviceAccessToken` pending/slow-down/expiry logic, not a second daemon loop.
The reusable device strategy round-trips its complete bounded response/private
metadata as opaque state and returns provider-owned `CredentialState`. Denial and
expiry normalize to typed terminal failures. The generic CAS/activation and
cancellation fences apply unchanged; long waits cannot grant a late credential.

CLI/TUI render the same typed public setup view. TUI `D` starts generic setup;
its one-second get calls only observe progress. Input actions display their exact
schema ref and can be submitted through the CLI; the inline TUI editor remains
unfinished. No public status contains the API device code or private state.

The CLI/TUI expose only the common public setup view and actions. Provider hooks
run in the daemon and return opaque credential state; frontends never exchange
tokens. Callback listeners are shared by actual bind address and released on
completion, denial, cancellation, expiry or shutdown. Their HTTP page acknowledges
receipt; public terminal status and activation are separate outcomes.

The daemon coordinator owns the state machine:

```text
starting → awaiting_user (input | callback | poll | await)
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

Credential refresh is single-flight per connection. Waiting for that connection
is context-cancellable; another connection never waits on its network exchange.
Resolve and refresh receive the request context linked to daemon shutdown, and
late success after cancellation cannot grant a credential or commit a replacement.
Refresh never overrides attempt replay restrictions. A refreshed key is persisted
only with the expected durable credential revision and an enabled matching mode.
The internal revision changes atomically on credential/mode/enabled/provider-assignment
updates, including same-secret resaves and A → B → A changes; deleting and
recreating an ID does not restore its previous revision. It is not a bearer
credential, config field, portable generation or secret-equality heuristic.
Interactive setup and refresh use the same CAS fence. Revisions survive process
restart, but pending authorization remains ephemeral. No global lock spans auth
network I/O. Credential commit and runtime activation are separate outcomes;
an activation error after commit does not roll back the durable token.

Credential CAS and a per-connection activation outbox entry commit in one SQLite
transaction. The secret-free commit key pins node/definition/mode/revision.
Refresh returns the resolved replacement immediately and wakes the daemon worker;
it does not compile routing configuration on the inference latency path. Code
and device setup try local activation once and retain `completed` authorization
with an activation receipt when application is pending. Reconciliation never
repeats a token exchange or provider request.

The worker resumes pending entries after restart, uses indexed integer-nanosecond
deadlines and stops its timer when idle. One row per configured connection bounds
retained work; due batches are limited to 32. Five automatic attempts, one second
apart, lead to `exhausted`; explicit retry grants a new budget. Storage failures
that prevent recording progress also have a bounded in-memory retry budget and
emit a diagnostic before waiting for an explicit wake/restart. Fenced SQL reset
and progress writes cannot clear a newer owner's state or overwrite its job.
Snapshot generations separate the configured connection's principal epoch from
its credential revision. Operational health and quota use the principal epoch;
authentication health additionally uses the credential revision. Provider session
keys intentionally do not include token revision, preserving same-connection
refresh continuity. A pinned route cannot resolve a replacement principal under
its former operational generation.

Typed reload receipts distinguish control publication, persisted version and
actual serving publication/version, including errors after publication. A
published control snapshot may still reach serving when version metadata cannot
be written; that receipt remains degraded until reconciled. `/models` projects
the actual reported serving snapshot rather than an unapplied control candidate.
`status` exposes `controlSnapshotVersion`, `servingSnapshotVersion` and
`lastReload`; the ambiguous `snapshotVersion` alias is removed. CLI activation
list/get/retry and retained auth views expose safe progress, not secret bytes or
the private revision. TUI completion is ready only with activation evidence.

Automatic credential recovery requires two independent declarations: the
normalized outcome has `cause=auth` and `recovery=refresh`, and the selected
auth mode declares `recovery: refresh`. Omitted/`none` mode policy disables
automatic recovery. The kernel does not infer recovery from HTTP status or
credential-type names; it still requires operation replay/issuer/retention
safety, disallows accepted effects, and permits at most one credential retry.
Request-scoped auth errors do not trigger connection recovery even if a hint
is present; recovering them would turn a caller-local failure into a retry.
Unknown hints fail closed and unknown mode policies fail snapshot admission.

The HTTP/JSON classifier's default 401 auth rejection supplies a refresh hint;
default 403 permission and qualifying quota evidence do not. Optional
`errorClassifierOptions.httpJson.authRecoverySignals` replaces the default
401 matching with bounded structured evidence: HTTP statuses, codes, types,
provider statuses and JSON Pointer field matchers. Selector groups are AND;
values inside one group are OR. Quota classification takes precedence over
conflicting auth evidence. Successful responses never request recovery, and
ambiguous 5xx effects are not manufactured into confirmed rejection. Arbitrary
provider statuses can normalize to the same contract without a kernel branch.
One public classifier call decodes evidence once; existing typed JSON/schema
and standard slice helpers are reused.

Credential CAS requires a typed transition: `renewal` for refresh and
`replacement` for interactive authorization. Both rotate the private credential
revision; renewal retains the principal epoch, while replacement rotates it even
when the serialized state is identical. Activation receipts expose the transition
but neither private epoch. Renewal clears only authentication evidence for older
credential revisions; a failure already observed for the new credential survives
activation. Replacement clears the former connection's authentication and
operational health/quota. Definition or connection-owner changes rotate principal
identity; a same-value secret resave still revokes in-flight CAS without claiming
a new principal. An explicit operator reset remains a separate action.

Authentication health has a separate persisted lane, queue/spool identity and
runtime projection. It cannot overwrite operational rate-limit/quota evidence.
Successful in-flight calls do not erase an unexpired authoritative rate/quota
cooldown, and discovery success does not clear authentication rejection. Provider-
scoped authentication evidence is not cleared by renewal of one connection.
Queued operational observations coalesce by principal generation, not token
revision; authentication observations are fenced by the live credential revision.
The external-module fixture preserves zero remaining quota with unknown reset
and an active rate-limit cooldown across renewal and restart, then exercises
replacement recovery. This is configured-connection identity, not inferred shared
account identity across connections or provider-reported identity attestation.
Generic setup actions and credential leases remain separate unfinished contracts.

Credential ownership is the typed provider-node identity plus exact provider
definition ref, alongside the selected credential mode. Reads project state,
revision and owner in one SQL snapshot. Serving, quota polling and opportunistic
quota share the same resolver; a pinned route whose node/ref/mode/auth binding
no longer matches the current owner is rejected before invoking its auth driver.
Discovery and connection probes use the same typed owner/mode guard before
credential resolution, rejecting mixed reads across a concurrent rebind.
Setup captures the same owner, and final CAS checks it atomically in SQL.
Changing a provider definition ID/version rotates child connection revisions
in that transaction, including A → B → A and v1 → v2 → v1; provider-node renames
and unchanged definition values do not revoke work. Raw SQL and bundle writes
cannot bypass the SQLite revision trigger, and rollback restores it. The old
session may finish its bounded driver exchange, but cannot persist its state;
a newly started session for the current owner remains usable. No global lock
or network round trip is added to enforce ownership.

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
selected provider-operation binding declares an `idempotency` object with
`header` and a positive `minRetentionSeconds` backed by the provider's minimum
guaranteed retention. Missing/zero/overflowing retention and credential or
transport-control headers are rejected. The header-only field is removed;
there is no compatibility path. Registration rejects that policy unless the exact operation contract
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

The replay window starts immediately before the first upstream dispatch, not
at ingress or credential acquisition, and never resets on a retry. It is
checked when choosing retry and again after request preparation/credential
refresh before redispatch. At the retention boundary an ambiguous effect is
reported without issuing another request or crossing providers. Cancellation
is also checked after preparation, even if a codec returns success after
ignoring its cancelled context. This is an invocation-local retry guarantee,
not a durable cache of client keys across separate calls or daemon restarts.
Reusing a client key after upstream retention expires is not protected by it.
Do not activate this binding based merely on observing an accepted header.
For example, [Stripe's contract](https://docs.stripe.com/api/idempotent_requests)
explicitly permits pruning keys after its documented minimum; this motivates
an explicit bound, not a retention assumption for an AI provider.

Scoped retries also require `UpstreamRequest.Replay`, a codec-owned
`PreparedRequestReplay` factory. The kernel does not call the semantic codec's
`Prepare` again after an ambiguous dispatch. Missing factories suppress replay;
a factory that changes method, resolved URL or Content-Type is rejected before
dispatch. Endpoint composition pins the initially resolved URL while renewing
body handles and auth. The original request body is closed before replay so a
multipart producer cannot remain blocked on an abandoned consumer.

The public `PrepareBytesRequest` helper freezes encoded bytes and static headers
when replay is requested, then supplies independent readers to the transport
and `PreparedAuthorization`. Authorization can renew credentials, nonces or a
body-bound signature without changing the encoded business bytes. Requests
without replay intent do not need a second frozen payload copy. The shared
multipart codec freezes its typed invocation and boundary, reopens authorized
artifact leases and uses the standard multipart writer for each attempt;
non-replayable bodies do not acquire a factory, and expired/released bodies
remain unavailable. No fallback buffers an arbitrary stream in RAM.

Kernel-owned idempotency headers are available before authorization through
the typed preparation context; shared helpers apply them automatically, and
custom codecs can use `ApplyPreparedRequestHeaders` before signing. The shared
HTTP transport disables automatic redirects and body rewind (`GetBody`), and
keeps even empty actions non-replayable to Go's implicit idempotent retry
heuristic. Transport retries before any bytes were sent may still recover an
unused connection; after a possible upstream effect, the kernel alone grants
redispatch. Custom transport modules must uphold the same boundary.

Custom factories are in-process trusted extension code, just like codecs;
preserving business payload is their contract, not a sandbox guarantee. The
shared helpers and conformance fixtures prove byte identity for JSON, binary
multipart and an external body-signing codec. Specialized schemes supply a
factory/authorizer in their versioned codec rather than adding a kernel branch.

After client write/flush/commit, retry and fallback are forbidden regardless
of replay safety. Precommit failures are classified before sending a client
error; an error frame must not accidentally commit a failed attempt while a
safe fallback remains available. Postcommit errors are represented in the
selected client contract and terminate the stream.

Async upstream jobs can expose creation/status/cancellation as registered
operations linked by a scoped job artifact. Any long-running orchestration
worker is optional and uses those same operations. It introduces no second
model category or provider-specific loop in the kernel.

Artifact delivery and artifact consumption are different actions. A declared
provider output may be disclosed to its operation's harness renderer without
turning a connection-scoped handle into a request-scoped/portable value.
`ValidateArtifactDisclosure` preserves schema, byte, sensitivity, expiry and
exact recipient checks; the consumption path still enforces replay scope.
Provider connection handles require a non-empty connection and exact issuer
contract matching their provider definition. Consuming one at a different
connection or issuer contract is rejected before provider dispatch.

`PublishInlineResponseArtifact` mints bounded, schema-validated values using
the selected route's owner and an explicit positive TTL. Inline handles survive
response spool cleanup and can cross daemon restarts when the caller retains
them; ordinary response body leases still end with their response. The generic
JSON operation ingress binds inline artifacts directly and rejects external
daemon-body lease references. Operation ports and the route's artifact policy
remain the authority for admission. These are routing/ownership contracts, not
cryptographic capability tokens or a substitute for caller/upstream account
authorization; authenticated opaque references remain separate auth-driver work.

The external job fixture creates a job, loses its response and replays the same
prepared keyed request without creating another job. It restarts the daemon,
then reads/cancels the job through its retained scoped handle. An ambiguous
unsafe cancellation is not retried: the caller reads status to reconcile it.
Passive transport/stream evidence is operation-scoped: a lost cancel response
does not delay status reconciliation. Typed health and quota identities carry
the exact operation ref through eligibility, half-open trials, persistence and
restart. Classifier options can declare operation-specific HTTP limits, while
actual account/provider failures remain broader. Quota-source modules may
return operation-qualified snapshots; a zero operation ref deliberately means
a shared budget. Workload ranking and affinity keep operation versions separate
as well. This is a general dimension, not a job-status bypass.

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

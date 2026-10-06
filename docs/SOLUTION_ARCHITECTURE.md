# Solution architecture contract

Status: normative target design, with semantic extension decisions fixed in
[Semantic extension contracts](EXTENSION_CONTRACTS.md). This document defines
the topology required for feature catch-up to remain implementation work.
The detailed contract defines compatibility composition, operation/artifact
schemas, auth sessions, transform bindings and replay safety. Runtime closure
is still pending the stronger M15 gate; implementation status remains in
[`IMPLEMENTATION_PLAN.md`](IMPLEMENTATION_PLAN.md).

## Success criterion

The architecture is ready for feature catch-up when adding a provider,
wire-format dialect, operation, capability, policy or request transformation
uses an existing extension point. Work may add a reusable or provider-specific
implementation, fixtures and configuration data. It must not require a new
provider/model category, provider branch in the kernel, provider-specific
storage column, duplicate control-plane workflow, or a new request lifecycle.

The compatibility target is architectural, not that every provider or feature
already exists. A feature absent from the implementation may be added later;
the question is whether its semantics already have a stable place in the
system.

Architecture design closure means the owner, data contract, lifecycle,
configuration binding and failure behavior are specified for each extension.
Runtime closure additionally requires semantic integration fixtures proving
that those contracts compose. Registration/composition tests are necessary
evidence but do not by themselves establish runtime closure.

## Domain model

```text
ProviderDefinition
  ├── operation bindings and reusable primitive references
  └── model-source / authentication / capability defaults
       │
ProviderNode ── Connection(s)
       │            └── credentials, endpoint overrides, entitlement evidence
       └── DiscoveredModel / SourceRoute
                    │
                    ▼
              PhysicalModel
                    │ ordered typed members
                    ▼
                 ComboModel
                    │
                    └── discoverable property → public model projection
```

- **Provider definition** describes reusable behavior and binds operations to
  endpoint, transport, auth, request codec, response decoder, discovery,
  classifier and optional usage/quota/session primitives. It is not a model or
  credential.
- **Provider node** is the user's configured provider instance/preset choice.
- **Connection** is one credentialed or anonymous account/endpoint instance.
  Credential lifecycle and model entitlement belong here, not on a Physical.
- **Discovered model/source route** records the opaque upstream model ID and
  the connection(s) known to expose it. Prefixes are route identifiers or UI
  projections; they are not logical model identity.
- **Physical model** is the user's canonical identity and capability/limit
  policy for one model. It groups source routes with fidelity and evidence.
- **Combo model** is itself a routable model. Its ordered members reference
  Physical or Combo models; typed strategies and member policy define its role
  or use case.
- **Exposure** is a property of a routable model. The public model list is a
  projection of enabled, discoverable Physical/Combo nodes, not a second
  catalog or publication workflow.

The graph is validated for missing references and cycles before an immutable
snapshot is published. Runtime traversal preserves each node's policy
boundary; it must not flatten a role into an untyped list of provider strings.

## Operation-neutral data plane

The gateway's routing lifecycle is operation-neutral. An **operation** is an
exact versioned ref naming the semantic task (for example
`chat.generate@1`, `embeddings.create@1`, `audio.transcribe@1`,
`audio.synthesize@1`, `search.query@1`, or `fetch.read@1`). An ingress/egress
**wire contract** names how a client encodes
that operation (for example OpenAI Chat, OpenAI Responses, Anthropic Messages,
or Gemini). These are different dimensions: a wire format is not a provider,
and a provider protocol does not dictate the client's response format.

Operation and feature catalogs are namespaced registries. Each extension
declares its payload schema, requirement compiler, encoder/decoder support and
control-plane metadata. The kernel consumes generic operation IDs and typed
requirements; adding (for example) an audio operation or a new capability does
not add a kernel struct field or node kind. Common interactions remain
first-class typed values; novel payloads use the versioned artifact contract.

Operation definitions own input/result/event schemas, payload validation,
requirement compilation and replay semantics. Artifact ownership, scope,
sensitivity, expiration and binary-body lifetime are specified in
[operation and artifact contracts](EXTENSION_CONTRACTS.md#2-operation-payloads-content-and-artifacts).
New event/content kinds use registered artifacts rather than new kernel fields.

The canonical request envelope is `Invocation` (a concrete implementation may
call it `Request`):

```text
Invocation
├── operation                 namespaced operation ID
├── requested model           public model name, retained for telemetry
├── client contract           ingress format, stream/accept preferences
├── prompt plan               ordered layers with origin and role
├── operation payload         core typed shape or versioned namespaced artifact
├── tools                     definitions, choice and call/result state
├── thinking intent           absent/auto/off/effort/budget/provider opaque
├── generation options        common typed values + namespaced extensions
├── requirements              derived from actual operation/input/options
├── session/continuity        client identity + provider-scoped opaque state
└── extensions                versioned namespaced opaque artifacts
```

The envelope is typed for cross-provider semantics and extensible for semantics
that are provider-private or not yet shared. `map[string]any` is not the
canonical extension contract: extensions carry a namespace, version, declared
schema and opaque JSON/protocol payload, with ownership and forwarding rules.
Unknown data may be preserved, but preservation alone never proves cross-route
compatibility.

Content uses a tagged semantic union (text, image, audio, video, document,
refusal, tool call/result) plus a versioned extension artifact for new
semantics. Tool definitions, tool invocation and tool result are distinct
values. Partial tool arguments remain ordered deltas until complete. Thinking
intent is separate from emitted thinking artifacts; provider signatures,
encrypted reasoning and continuation tokens remain opaque and scoped to the
provider/model/session that issued them.

`PromptPlan` is an ordered list of layers carrying origin (`harness`,
`provider`, `user`, `inline` or a future namespace), role, content and policy
metadata. The plan does not assume that every provider has the same system
prompt field. Ingress records source and order; the provider codec maps those
layers to its dialect and reports any loss. User-, harness- and
provider-supplied instructions remain distinguishable for inspection and
future transforms.

## Translation, compatibility and fidelity

The routing decision is made against the complete translation path, not only
an upstream protocol or a boolean `supports format` flag:

```text
client ingress decoder
  ∩ operation semantics
  ∩ request requirements and source artifacts
  ∩ provider/model/connection encoder
  ∩ provider response decoder
  ∩ client egress renderer
  → CompatibilityPlan
```

`CompatibilityPlan` returns eligibility, fidelity (`native`, `translated`,
`lossy`, or `unsupported`), preserved/transformed/lost semantic paths,
constraints and a reason. A missing declaration is unsupported, not presumed
compatible. Material loss is rejected by default; a user may explicitly allow
a declared degradation policy at the relevant model/policy boundary. Secrets,
continuity artifacts and provider-private signatures are never silently
rewritten for a different provider.

The [compatibility policy algebra](EXTENSION_CONTRACTS.md#3-compatibility-plan-and-policy-algebra)
defines per-facet mapping composition, conditional support, loss grants and
denials, and native passthrough selection. The planner receives the effective
invocation and active transform chains; renderer format support alone cannot
admit a candidate.

Provider codec, client codec and renderer are separate contracts:

```text
IngressCodec.Decode(wire) -> Invocation
RequestTransform.Apply(Invocation) -> Invocation
ProviderEncoder.Encode(Invocation, Route) -> UpstreamRequest
Transport.Execute(UpstreamRequest) -> UpstreamResponse
ProviderDecoder.Decode(UpstreamResponse) -> SemanticEvent stream
ResponseTransform.Apply(SemanticEvent) -> SemanticEvent
EgressRenderer.Render(SemanticEvent, ClientContract) -> client wire stream
```

Non-stream responses are an aggregation of the same semantic event stream,
not a separate translation architecture. A truly lossless same-protocol path
may bypass semantic re-encoding only when the compatibility plan explicitly
proves pass-through safety and observability can still consume bounded events.
An active semantic response mutation forces semantic rendering. Signing/auth
application occurs after provider encoding and endpoint resolution, before
transport; auth is not embedded separately into every provider encoder.

## Extension stages and effects

The lifecycle exposes named stages; extensions declare stage, scope,
configuration schema, required capabilities, effects and failure behavior.

1. **Ingress decode** — client protocol modules produce `Invocation`.
2. **Model-path preparation** — compose defaults and enabled invocation
   transforms on immutable branch-local input, preserving prompt provenance.
3. **Candidate preparation** — compose provider/connection defaults and enabled
   attempt transforms; compile final requirements from the resulting payload.
4. **Candidate negotiation** — compose the complete compatibility plan and
   filter by operation, capabilities, fidelity, entitlement and health/quota.
5. **Model policy** — prune ineligible members, then apply each graph node's
   strategy without flattening nested policy boundaries.
6. **Provider encode/execute** — auth, endpoint and transport stay separate
   from semantic routing.
7. **Outcome classification** — convert status, headers, body and timing to a
   typed observation; update passive runtime policy independently of rendering.
8. **Response decode/transforms/render** — provider wire becomes semantic
   events, optional transformations run, then the ingress client contract is
   rendered.

Transformations must declare whether they change model requirements, prompt,
tools, modalities or continuity. Requirements are recomputed after any
pre-routing transform. A transform cannot change the requested public model,
select a route, bypass authentication, suppress cancellation, or hide a
material degradation. Extensions are bounded and cancellable. Observer hooks
cannot block first byte or stream delivery; transform errors fail explicitly
unless that transform declares a safe fail-open policy.

[Transform binding and lifecycle](EXTENSION_CONTRACTS.md#4-transform-bindings-and-execution-lifecycle)
fixes scope order, candidate-local stages, options, transactional fail-open,
resource budgets and original-versus-rendered accounting. Registration never
implicitly enables a transformation.

## Routing policy and runtime evidence

Combo member selection and Physical source selection are applications of the
same strategy contract at different graph boundaries. A strategy plans
candidates; it does not encode provider errors. An outcome classifier reports
cause, scope, retry action/deadline, confidence, evidence and limit windows.
The scheduler/ranker consumes those observations only after hard eligibility.

Static user order remains durable policy. Health, quota, latency, throughput,
session affinity and observed success are separate bounded runtime signals;
they may produce an explainable effective order but never rewrite user order.
Real calls are the default source of evidence. Synthetic checks and quota
polling require explicit policy. Retry/fallback is forbidden after client
response commitment.

Before client commitment, replay also requires the operation/provider's
declared safety after dispatch. An ambiguous upstream effect is not permission
to repeat an unsafe operation. The [attempt contract](EXTENSION_CONTRACTS.md#6-attempt-replay-and-commitment)
owns this distinction, including issuer-scoped idempotency keys and job artifacts.

Capabilities use a namespaced `FeatureID` with typed/versioned constraints and
an evaluator registered for the feature. Standard features (text, image,
audio, tools, reasoning, structured output, context limits and similar shared
semantics) use common definitions. A new feature contributes its schema,
evaluator and adapter support as an extension. Request requirements are
generic `(FeatureID, constraint)` values compiled from the operation payload;
the scheduler needs no feature-specific branch. Missing evidence remains
unknown and cannot satisfy a hard requirement.

## Provider and feature extension model

Three extension levels are supported by one system:

1. **Manifest composition:** choose existing primitives and options; no Go
   change for a provider whose behavior fits the catalog.
2. **Reusable primitive:** add an auth, endpoint, codec, discovery, classifier,
   transform, renderer or policy implementation once; bind it in any provider
   definition.
3. **Provider-specific module:** implement genuinely unique wire/auth quirks
   behind existing typed interfaces and register the module. This may require
   a Go package/build registration, but never a provider-name branch in kernel,
   storage schema or model-management UI.

Definitions advertise supported operations and option schemas. The same
metadata drives validation, control APIs and generic TUI forms; custom
provider-specific setup is allowed only as a declared extension view, not an
implicit frontend branch. Auth flows cover validation, request credentials,
refresh and optional interactive authorization lifecycle. Secrets remain in
connection-scoped secret storage and are resolved only for an attempt.

The [extension descriptor](EXTENSION_CONTRACTS.md#1-extension-descriptor-and-configuration-binding)
is the common schema/version/dependency contract for operations, features,
policies, transforms and provider primitives. Portable bundles include user
definitions and declarative bindings, with compiled-module dependencies
validated before apply. The [auth lifecycle](EXTENSION_CONTRACTS.md#5-auth-driver-and-interactive-session)
separates credential acquire/apply/refresh from daemon-owned authorization
sessions and generic frontend actions.

This design does not require dynamic Go plugins. Static registration is the
default so binaries remain portable and deployment predictable. Provider
manifests are data; they cannot execute arbitrary code.

## 9router behavior-family mapping

This mapping compares architectural responsibility, not implementation parity.

| 9router behavior family | GoBroom solution boundary |
|---|---|
| Provider registry/presets and provider-specific defaults | Provider definition + operation bindings + shared or provider-specific primitives |
| API key, OAuth, refresh and account selection | Auth-flow implementation bound to a Connection; credential resolution/refresh is independent from model policy |
| `/models`, static catalogs, live catalogs and custom IDs | Model-source primitive produces Discovered models/source routes with provenance and connection entitlement |
| Provider-prefixed upstream model IDs (the source strings users browse) | Opaque source-route ID plus provider/node prefix projection; never confuse it with Physical identity |
| 9router's explicit alias → target-name mapping | Optional ingress name-resolution behavior that targets a graph node; it is not a fourth model-management layer or a source route |
| `/v1/models` aggregation of combos and active provider catalogs | Explicit projection of only enabled, discoverable Physical/Combo nodes; source inventory is not automatically public |
| Combo/role model members and fallback | Combo graph node, typed ordered member edges and registered strategy/failure policy |
| Equivalent provider model variants | Physical identity with ordered source routes, fidelity, capability/limit evidence and source-selection policy |
| Client/provider protocol translation | Independent ingress codec, provider encoder/decoder, compatibility plan and client renderer |
| Thinking/reasoning, tools, modalities and continuity | Typed Invocation/semantic event facets plus namespaced opaque artifacts and capability negotiation |
| Account/model locks, quota and rate-limit response parsing | Typed outcome classifier/limit extractor feeding scoped passive scheduler state |
| Usage, latency and throughput | Bounded semantic observations and optional usage enrichers; never block request rendering |
| Prompt/tool compression or other request mutation | Explicit, opt-in stage-bound transform with declared effects and recomputed requirements |
| Embeddings/media/search/fetch operations | Same operation-neutral lifecycle with operation-specific codecs; runtime support can ship separately |
| MITM/DNS, IDE interception, tunnels and UI integrations | Sidecar/frontend boundary; not a kernel provider/model extension |

9router can place several of these behaviors inside one handler or
provider-specific branch. GoBroom assigns each behavior a stable owner and
extension contract so a new quirk is implemented at that owner rather than
spread through request handling.

## Architectural change simulations

Before calling the architecture ready for feature catch-up, verify that each
case below can be handled without changing kernel model categories, graph
semantics, core tables or request orchestration:

| Change request | Allowed extension work | Forbidden redesign |
|---|---|---|
| New API-key provider with `/models` | Manifest + existing auth/discovery/codec bindings | Provider branch in kernel or TUI |
| New OAuth/device-flow provider | Auth-flow implementation/binding + connection form metadata | New credential columns or provider-specific lifecycle in kernel |
| New request/response dialect | Ingress codec, provider codec and/or renderer + conformance fixtures | Replacing Combo/Physical graph or duplicating request lifecycle |
| New model capability/modality | Typed capability/content extension + requirement evaluator/codec support | New model node category or boolean added across every layer |
| New quota/error envelope | Reusable/configured classifier/extractor + fixtures | Provider-specific fallback condition in scheduler |
| New fallback/ranking policy | Registered strategy/ranker with typed options | Strategy-specific fields in Combo schema/kernel |
| New prompt/tool transform | Stage-bound transform with declared effects | Mutating raw JSON ad hoc in handler or bypassing compatibility checks |
| New operation family | Operation contract + codecs and policy bindings | New control-plane routing architecture |

The change is architectural only if the new implementation composes through
these contracts and existing generic control paths. A test that merely proves
the current examples still work is not sufficient.

The executable M15 simulation matrix and evidence map are maintained in
[`M15_CONFORMANCE.md`](M15_CONFORMANCE.md) and run with `make test-conformance`.

## Scope boundary

The architecture is operation-capable without promising that every operation
is in the initial product. MITM/DNS, IDE interception, tunnels, dashboard,
hosted sync and optional panel/judge orchestration remain external products or
services. They may consume the same gateway contracts but do not enlarge the
kernel. SQLite, Unix IPC and journald remain simple defaults behind replaceable
control/persistence/telemetry boundaries; arbitrary database compatibility is
not implied.

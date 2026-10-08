# Request normalization and protocol translation

Status: typed ingress, prompt separation, operation/format separation and
manifest-bound response decode → semantic events → renderer are implemented
for registered adapters. Semantic family coverage remains adapter-specific and
incomplete. The normative extension contract is in the
[solution architecture](SOLUTION_ARCHITECTURE.md).
The detailed target contracts for compatibility composition, artifacts,
transforms and replay are fixed in
[Semantic extension contracts](EXTENSION_CONTRACTS.md).

## Target pipeline

```text
client wire request
  -> ingress codec: wire format → Invocation IR
  -> prepare model-path defaults and opt-in invocation transforms
  -> candidate-local defaults and opt-in attempt transforms
  -> compile final operation/capability requirements and compatibility plans
  -> filter candidates, then apply hierarchical model strategies
  -> provider encoder → endpoint → auth apply/sign → transport → upstream
  -> provider decoder → semantic response events
  -> opt-in response transforms → client renderer
  -> client wire response
```

The target normalized request is an operation-neutral `Invocation`, not an
OpenAI wire body. It carries the requested public model, client wire contract,
prompt layers with origin, typed input/content, tools and tool state, thinking
intent, continuity, derived requirements, transport hints and namespaced opaque
extensions. Concrete Go names may differ; the complete field ownership and
extensibility rules are defined in the
[solution architecture contract](SOLUTION_ARCHITECTURE.md). The current Go type
is `normalize.Request` in `internal/normalize/types.go` and remains an initial
subset, not the full target contract.

Operation and wire format are separate. A versioned operation ref describes
the semantic task (`chat.generate@1`, `embeddings.create@1`, etc.); a wire
format describes the client envelope (OpenAI Chat, Responses, Anthropic
Messages, Gemini, etc.). Neither determines the selected provider by itself.
The ingress codec selects the exact operation version and route binding must
match that version.

Operation artifact ports also compile to named hard compatibility facets
(`operation.artifact.<role>`). A provider encoder must explicitly declare how
it carries each requested artifact role; an unbound binary input cannot slip
through merely because the outer request is multipart.

`PromptPlan` separates system/developer instructions from conversation and
records their origin and order. The contract provides harness-, provider-,
user- and inline-origin layers. HTTP ingress currently populates inline
layers; the other origins are architectural slots until their configuration
sources are wired.

The target reasoning intent distinguishes absent/inherit, explicit auto,
disabled, ordinal level and numeric budget. It also carries summary intent,
strictness and provenance. Harness wire shapes, route reasoning dialects and
lossy-mapping policy are specified in the
[Physical model contract](PHYSICAL_MODELS.md); an absent field must not be
silently normalized into an explicit provider default.

## Normalization responsibilities

- detect source format using endpoint and request evidence;
- require and preserve the requested public model name;
- normalize common message/tool structures without assuming all protocols are
  equivalent;
- capture continuity/session and modality hints before envelope conversion;
- preserve unknown data where the current typed contract supports it.

Unknown-field preservation is not a guarantee of lossless cross-protocol
translation. Adapters must not silently claim semantics they cannot represent.

## Translation policy

The target separates ingress decoding, provider encoding, provider response
decoding and client rendering. Candidate compatibility is the intersection of
operation support, request semantics, provider/model/connection capability,
response decoding and client rendering. The plan states fidelity and the
semantic paths it preserves, transforms or loses. Missing declarations mean
unsupported; material loss is rejected by default. Explicit degraded behavior
must be visible at a policy boundary. See the
[solution architecture](SOLUTION_ARCHITECTURE.md) for the contract.

An optimized lossless passthrough may bypass semantic re-encoding only when
the compatibility plan proves that it preserves the client's contract and
there is no active semantic response mutation. Enabling a text/tool transform
forces semantic rendering or rejects the candidate when that renderer is
unsupported. Observations consume original upstream events independently of
rendered projections. See the
[compatibility algebra](EXTENSION_CONTRACTS.md#3-compatibility-plan-and-policy-algebra).

Present adapters include OpenAI Chat, OpenAI Responses, Anthropic Messages and
Gemini paths. Their tested subsets differ. The composed runtime decodes
provider streams into `ResponseEvent`, applies registered response transforms,
then selects a client renderer. Native wire-frame passthrough is a renderer
mode, not a provider-specific writer path. Exact supported event families and
loss behavior belong in the [compatibility matrix](COMPATIBILITY_MATRIX.md)
and adapter fixtures. The Anthropic Messages renderer can semantically encode
text and tool events from other providers. Reasoning blocks are rendered only
when the decoder preserves the issuer signature and no semantic response
transform can invalidate it; otherwise the route is rejected before dispatch.
Anthropic adaptive effort and explicit token budgets are distinct canonical
intents: only an effort level with a target-dialect equivalent can be encoded
as OpenAI Responses reasoning effort. Thinking-token budgets are not silently
reinterpreted as a total response-output cap. A complete Anthropic-client
route additionally needs issuer-signed thinking egress; without that response
contract, compatibility planning excludes the candidate before dispatch.

## Streaming invariants

- client cancellation propagates to upstream context/body reads;
- a response is considered committed at first write/flush;
- no route fallback occurs after commitment;
- stream errors after commitment terminate/report the stream, not restart it;
- usage and health reporting cannot block first byte or stream writes;
- tool-call state must remain correct when arguments arrive across chunks.
- precommit replay after dispatch also requires operation/binding replay
  safety; ambiguous effects of unsafe upstream operations prohibit fallback.

These are architectural requirements. A completed implementation must use one
semantic event model for streaming and non-stream aggregation, retain partial
tool-call ordering/signatures, and prove the pre/post-commit retry boundary.
Milestone status and conformance evidence remain in the roadmap and
compatibility matrix.

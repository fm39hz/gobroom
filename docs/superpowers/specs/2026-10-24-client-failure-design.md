# Registered client precommit failure boundary

Status: agent-selected design under the user's standing instruction to settle
implementation decisions and proceed; not a new explicit approval claim.

## Evidence and purpose

At `6ff38a3`, executed regressions confirm both ordinary ingress Decode errors
and kernel unknown-error fallback expose private text. The decided extension
contract additionally requires a new client dialect to encode its own failures
without changing handler/provider branches. Current successful/committed-event
rendering cannot handle failures before a render session exists.

Local CLIProxyAPI separates status/classification from Claude reconstruction,
but its generic body builder preserves arbitrary valid JSON/error text and
its SDK HTTP adapter imports the Gin/upstream framework. Those are unsuitable
safe-boundary dependencies. Reuse the separation and existing GoBroom typed
ClientFailure, not raw JSON forwarding. stdlib encoding/json, mime, context and
existing schema/catalog machinery suffice; no new dependency or parser.

## Approach

Selected: make failure rendering a required part of ClientIngress, registered
as one exact client-boundary contract. An ingress already knows its dialect
before Decode succeeds. Reusable encoder strategies avoid duplicate formatting.

Separate registry lookup for an independently versioned error-renderer primitive
would add a second binding/activation path without a current independent owner.
Putting precommit handling only on ResponseRenderer cannot cover Decode/auth
errors before a render session/format exists. Do not add either extra mechanism.

ClientIngress requires:

`RenderFailure(context.Context, ClientFailureContext) (ClientFailureResponse, error)`.

The registry's existing ingress ref pins decoder and failure implementation
together. Bump the public provider host contract from 13 to 14 atomically and
update all codec implementations/public aliases/module locks. No optional
legacy interface detection or old-contract compatibility path.

## Safe metadata and ownership

Keep the existing comparable three-field kernel.ClientFailure. Unknown Go
errors and operations.InputError use constant public messages, never Error()
text. Existing explicit valid ClientFailure carriers keep safe messages and
private cause identity. Preserve typed continuity/model/route classifications.

Add kernel.ClientFailureDiagnostics {Paths []string; Reasons []string}. An
explicit `ClientFailureDiagnostics() ClientFailureDiagnostics` error carrier
can contribute public diagnostics without changing the cause. Paths: at most
32 absolute semantic paths, each at most 256 UTF-8 bytes without control runes.
Reasons: at most 32 identifier codes, each at most 128 bytes. Encoded diagnostics
must fit 8192 bytes. Validate with maintained JSON binding, return owned copies,
omit invalid optional diagnostics. No generic raw-error extraction or redactor.

ClientFailureContext is a value containing the safe failure, owned diagnostics,
exact operation ref, safe request ID/model, host-selected HTTP Status,
kernel.UpstreamEffect and RetryAction. No original error, HTTP request, headers,
credential lease, raw body, provider session or planner object enters rendering.
Request ID/model are bounded by 128 bytes; oversized/invalid values are omitted,
not partially sliced. Context encoded size must fit 32768 bytes; fail closed
if required exact refs/metadata cannot fit rather than inventing a shortened ref.

Decode/auth/operation-contract rejection is EffectNotDispatched, including
InputError from operation preparation. Other execution failures default to
EffectUnknown; ReplaySuppressedError takes precedence and supplies its actual
effect when present, including unknown. Never infer that ErrNoRoute implies no dispatch: candidates
may already have rejected attempts. RetryNever is conservative presentation,
not a new replay grant. Unknown paths/reasons stay empty rather than fabricated.

## Encoding and host authority

ClientFailureResponse has only ContentType string and immutable Body string.
It cannot override status, headers, retry policy or commitment. Body permits
non-JSON wire formats but is limited to 65536 bytes; ContentType must be a valid
media type at most 256 bytes without control runes. Strings avoid a retained
mutable module-owned byte buffer. Trusted static Go code is not sandboxed.

JSONOperationIngress and MultipartOperationIngress delegate to an optional
`FailureEncoding FailureEncoder` strategy; nil means their natural reusable JSON error encoder,
not a fallback to an old ClientIngress interface. Supply standard JSON and
Anthropic envelope strategies; the built-in Messages ingress selects Anthropic
at composition time. A custom ingress overrides RenderFailure or configures a
different encoder. No format/provider switch in the handler.

The generic JSON response keeps code/type/message/model and includes operation,
requestId, paths, reasons, effect and retry. Anthropic uses its declared error
envelope, including optional request_id and native error.type selected from the
host HTTP status, per the [official errors contract](https://platform.claude.com/docs/en/api/errors).
No arbitrary upstream JSON or error-derived header is forwarded.

Host encodes/validates before WriteHeader; an encoder error, invalid metadata,
media type or oversized body produces HTTP500 with an empty body and no error
text. No recursive fallback or pretend custom-dialect success. Auth failures on
registered operation routes use that same codec boundary; the fixed OpenAI
/v1/models metadata route keeps its ordinary safe JSON auth response.

After client commitment, the API never calls this boundary. Preserve the
existing typed terminal stream event and no-success/no-replay behavior.
Provider errors/health/replay classification remain kernel-owned; this is
presentation, not a second execution or translation pipeline.

## Acceptance

- Executed private Decode/unknown/InputError regressions become green without
  losing explicit safe metadata/cause identity.
- An independent module registers a visibly different client failure envelope
  and its own schema, covering auth/Decode/input-schema/no-route/accepted-provider
  failure before first client byte; status/effect remain host facts, no replay.
- Response bytes are independently schema-validated in the fixture, private
  markers stay absent, and invalid/oversized/failed encoders fail closed.
- Required hook/current host refs bind through ordinary registry/catalog/daemon
  APIs, with no provider-name branch or storage schema change.
- Existing committed-stream/error/continuity/replay tests remain green. Full
  root/external tests/vet/conformance, focused race3, actual install and explicit
  installed CLI catalog/control acceptance precede completion.

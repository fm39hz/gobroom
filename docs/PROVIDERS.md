# Provider definitions and runtime primitives

Status: executable manifest composition for endpoint, transport, request
encoders, provider response decoders, discovery/auth/error/quota binding and
client renderer registration is implemented. The M15 composition subset
passes; semantic closure remains pending under the fixed
[extension contracts](EXTENSION_CONTRACTS.md). See
the [implementation roadmap](IMPLEMENTATION_PLAN.md). The
normative extension architecture is the
[solution architecture contract](SOLUTION_ARCHITECTURE.md).

## Goal

Built-in and user-defined providers use the same configuration contract.
Provider identity is data, not a switch statement in the routing kernel. Adding
a provider should be a manifest composition of reusable primitives; if a new
behavior is required, add one reusable primitive implementation and bind it in
manifests rather than duplicating another provider.

## Provider definition

A JSON provider definition pins `contractVersion: 1` and binds operations to
primitive refs that each pin their own exact `contractVersion`. Missing or
unknown versions fail closed; runtime lookup never selects a default version.
Relevant contract types are in
`internal/provider/primitives.go`.

```json
{
  "chat": {
    "protocol": "openai_chat",
      "task": {"kind": "operation", "id": "chat.generate", "contractVersion": 1},
    "providerFormat": "openai",
    "endpoint": {"kind": "endpoint", "id": "http-json", "contractVersion": 1},
    "endpointOptions": {
      "path": "/chat/completions",
      "query": {"api-version": "v1"}
    },
    "transport": {"kind": "transport", "id": "http", "contractVersion": 1},
      "requestCodec": {"kind": "request_codec", "id": "openai-chat-json", "contractVersion": 1},
      "responseDecoder": {"kind": "response_decoder", "id": "openai-sse", "contractVersion": 1},
      "errorClassifier": {"kind": "error_classifier", "id": "http-json", "contractVersion": 1},
      "errorClassifierOptions": {
        "httpJson": {
          "codePath": "/failure/reason",
          "messagePath": "/failure/explanation",
          "resetAtPath": "/failure/reopens_at",
          "windowNamePath": "/failure/window",
          "windowLimitPath": "/failure/budget",
          "windowUsedPath": "/failure/spent",
          "windowRemainingPath": "/failure/left",
          "quotaWindowKind": "tokens",
          "quotaCodes": ["balance_empty"]
        }
      }
  }
}
```

The daemon loads user definitions from `$XDG_CONFIG_HOME/gobroom/providers`
(normally `~/.config/gobroom/providers`) by default. The directory is created
on daemon startup; `gobroomd --provider-manifests DIR` selects an alternate
manifest directory.

```text
provider definition
  ├── aliases and capabilities
  ├── auth/session references
  ├── defaults
  └── operation bindings
      ├── endpoint + typed path/query options
      ├── transport
      ├── request encoder / provider response decoder
      ├── model source
      ├── usage/quota source
      └── error classifier
```

An operation is optional when the provider does not support it. Capabilities
describe behavior; they are not inferred solely from a marketing model name.
Bindings are validated at load/startup, not by ad-hoc string edits while
serving a request.

### Generic multipart request codec

The shared `multipart-form` request codec streams declared operation artifacts
from their owner-scoped leases instead of copying file bytes into the JSON
operation payload. Its options are validated against the codec's versioned
schema. Artifact roles must match the operation's named `artifactInputs` ports;
the provider manifest only maps those roles to upstream form field names:

```json
{
  "requestCodec": {"kind": "request_codec", "id": "multipart-form", "contractVersion": 1},
  "requestCodecOptions": {
    "modelField": "model",
    "payloadField": "metadata",
    "streamField": "stream",
    "artifacts": [
      {"role": "source-audio", "field": "file", "fileName": "input.wav"}
    ]
  }
}
```

The selected operation must declare the same `source-audio` role and exact
artifact type. Unmapped roles, schema-invalid options and streaming requests
without a configured stream field fail closed. This codec is protocol-neutral;
provider-specific response decoding and client rendering remain separately
bound primitives. When an upstream expects individual text fields instead of
one JSON metadata field, `payloadFields` maps exact top-level operation
properties to form fields; every property must be mapped, so metadata is never
silently dropped. For example, `{"payloadFields":[{"property":"language","field":"language"}]}` maps the operation payload's `language` string to that form field.

## Primitive responsibilities

| Primitive | Responsibility |
|---|---|
| Endpoint | Resolve the configured API root with operation path and typed path/query overrides |
| Transport | Execute the prepared upstream request with cancellation and bounded timeout |
| Auth | Resolve static credentials or an auth-flow contract into request credentials |
| Provider encoder | Map normalized invocation semantics into upstream wire format |
| Provider response decoder | Decode upstream JSON/SSE into semantic response events |
| Client renderer | Emit the selected ingress client's wire format from semantic events |
| Model source | Discover and normalize the provider model catalog |
| Usage source | Extract bounded usage data from real responses |
| Outcome classifier | Convert attempt evidence into typed cause, scope and retry action; JSON classifiers may bind field paths and quota signals declaratively |
| Limit extractor | Parse request/token/quota windows from response headers and bodies |
| Quota enricher | Optionally fetch exact quota/reset state after relevant evidence |
| Deadline parser | Normalize provider reset/retry timestamps with provenance |
| Health policy | Bind passive breaker and ranking defaults without provider-name branches |
| Session store | Persist protocol continuity/session state when an operation needs it |
| Extension | Optional, typed behavior outside the common operation contract |

Providers may share any primitive. A provider definition composes them; it
does not copy their implementation.

The generic `http-json` classifier understands common status classes,
Retry-After delta/date, standard rate-limit headers and common JSON quota/reset
fields. Its rich outcome contract is preserved through runtime binding, so
quota/reset evidence can affect route eligibility. A provider manifest can
override JSON pointers and add exact quota codes/types or message markers plus
named-window fields without a kernel or classifier fork. Provider shapes not
expressible with these common typed options still need a reusable extractor
primitive. Polling and enrichment boundaries are defined in [the passive
health contract](PASSIVE_HEALTH_ROUTING.md).

## Resolution precedence

Explicit node/connection settings override provider-definition defaults.
Defaults provide convenience only and must not overwrite user configuration.
Credential material belongs to a connection, never to a provider manifest or
route snapshot.

## Current support and limits

The runtime registry composes OpenAI Chat, OpenAI Responses, Anthropic Messages
and Gemini request/response decoders with a generic HTTP endpoint and transport.
The selected definition also binds model discovery, auth flow, error classifier
and quota source. Provider bindings are resolved before the immutable routing
snapshot is published; the kernel receives the composed runtime adapter and
does not branch on provider identity. Built-in JSON definitions and externally
loaded definitions use the same builder.

`responseDecoder` and `providerFormat` describe the upstream response dialect.
The selected client renderer comes from the ingress contract and the daemon's
renderer registry; it is not copied into every provider definition. The
composed adapter decodes to semantic events, applies registered response
transforms, and renders the client contract. Native same-format responses use
an explicit wire-frame passthrough renderer. Other cross-format pairs need a
usable semantic decoder and registered renderer. The target full per-facet
compatibility negotiation is specified in
[Semantic extension contracts](EXTENSION_CONTRACTS.md); the current format-level
negotiation does not prove all semantic paths.

An operation binding independently selects `endpoint`, `transport`,
`requestCodec` and `responseDecoder`. `endpointOptions` can override the relative
operation path and add query parameters without editing or duplicating a codec.
The model-list operation uses the same endpoint resolver as inference.

A provider that exposes an account/model quota endpoint binds a separate
`quota` operation. Its endpoint, transport, quota parser and window label are
resolved into the route; the shared HTTP/JSON parser receives the resolved
URL rather than constructing a provider URL itself:

```json
{
  "quota": {
    "endpoint": {"kind": "endpoint", "id": "http-json"},
    "endpointOptions": {"path": "/account/quota"},
    "transport": {"kind": "transport", "id": "http"},
    "quotaSource": {"kind": "quota_source", "id": "http-json-quota"},
    "quotaWindowName": "account"
  }
}
```

Quota operations run only under the configured polling policy or after real
rate-limit/quota evidence; they do not trigger constant health polling.

The top-level `session` reference selects an optional per-provider session
store. Session keys are scoped by connection, provider definition, physical
upstream model and client session ID; provider state is opaque bounded JSON.
The daemon's default
`session` implementation reads from an in-memory bounded cache and persists
updates asynchronously to SQLite, so no database access occurs on the request
hot path. The built-in OpenAI Responses definition opts in and uses a stored
response ID as `previous_response_id` only when the client supplied a session
ID and did not provide its own continuity ID.

An optional `usageSource` runs on the completed real response and can enrich
the event before the asynchronous usage worker persists it. The shared
`http-header-usage` source reads `X-Usage-Input-Tokens`,
`X-Usage-Output-Tokens` and `X-Usage-Cost-USD` by default; a manifest can
override those names with typed `usageOptions`. Present configured headers
are authoritative over body-derived values; absent/unparseable headers leave
the codec's values intact. Usage sources must be bounded and passive—no
separate health/quota polling belongs in request completion.

OAuth is bound to a provider definition, while token material stays with the
connection:

```json
{
  "auth": {"kind": "auth", "id": "oauth2"},
  "authOptions": {
    "oauth": {
      "clientId": "public-client-id",
      "authUrl": "https://login.example/authorize",
      "tokenUrl": "https://login.example/token",
      "deviceAuthUrl": "https://login.example/device/code",
      "scopes": ["models.read"]
    }
  }
}
```

The connection's secret is a JSON token state, for example
`{"access_token":"…","refresh_token":"…","client_secret":"…"}`; client ID may
also be supplied there to override the definition. It is not part of the
provider manifest, routing snapshot, ordinary connection list or exported
config bundle.

`deviceAuthUrl` is optional. When the authorization server supports RFC 8628,
`gobroom connections device-start --connection-id <id>` starts the device
flow; the daemon retains and polls the private device code, while CLI/TUI show
the user code and verification URL. Polling honors the server interval,
`authorization_pending`, `slow_down` and expiry using the shared OAuth library.

For authorization-code flow, an HTTP redirect URI on `localhost` or a loopback
IP causes the daemon to bind that exact local address/port only while the
authorization session is active. The callback is checked against the registered
path/query and one-use state before token exchange. HTTPS and non-loopback
redirect URIs are not bound by the daemon; they use the explicit CLI callback
handoff instead.

Providers that require no secret still bind auth explicitly with
`{"kind":"auth","id":"none"}`; the connection can then exist without secret
material. Omitting auth is rejected so an unconfigured route cannot be mistaken
for anonymous access. Provider creation derives its connection credential-type
default from the bound auth primitive; connection setup validates the supplied
secret through the same flow before storing it.
Refresh is bound to that definition and serialized per connection, not behind
a process-wide network lock.

This is not a dynamic plugin system. Static registration is intentional;
manifests bind trusted primitive IDs and cannot execute arbitrary code. The
architecture supports three extension levels: manifest composition, reusable
primitive implementation, or provider-specific module behind an existing
contract. A module may add build-time registration but must not add a
provider-name branch to the kernel, a provider-specific storage schema, or a
provider-specific branch in generic management UI. See the architectural
[change simulations](SOLUTION_ARCHITECTURE.md#architectural-change-simulations).

The target auth contract uses connection-scoped opaque leases, a prepared
request apply/sign stage and an optional interactive authorization driver.
The daemon coordinator owns sessions, polling, callback/PKCE, generation-safe
secret replacement and generic frontend actions. Current `Resolve/Refresh`
factories and token-state forms implement only a subset; the complete contract
is fixed in [Auth driver and interactive session](EXTENSION_CONTRACTS.md#5-auth-driver-and-interactive-session).

All configurable primitives contribute the same versioned descriptor/schema
metadata. User definitions and enabled bindings are part of portable bundles;
secret-free provider definitions are embedded, while opaque/sensitive
definitions are exact external dependencies checked before application. Newly
imported portable definitions are stored and require a daemon restart to enter
the frozen runtime catalog. Credentials and live auth sessions remain excluded.
Implementation binaries/custom primitive code are never embedded. See
[configuration binding](EXTENSION_CONTRACTS.md#1-extension-descriptor-and-configuration-binding)
for the dependency-lock contract; operators install external primitive modules
on the target before applying bundles that depend on them.

Interactive authorization-code/device-flow start/callback handling, usage
extraction, provider sessions, quota APIs, embeddings/media operations and
many codecs are not all implemented. A genuinely new wire dialect adds an
ingress codec, provider encoder/decoder and/or client renderer as needed; it
does not combine provider decoding with a specific client's response writer.

## Adding a provider

1. Identify the operations and protocol semantics from authoritative provider
   docs or a captured fixture.
2. Compose existing endpoint, transport, codec, auth, discovery and policy
   primitives in a JSON manifest if they fit.
3. If the wire dialect is new, define one reusable codec primitive and tests;
   do not add a provider-name case to the kernel.
4. Add manifest fixtures for auth binding, model discovery, request/response,
   errors and optional quota/usage.
5. Test the provider through the same connection/configuration path as a custom
   provider.

Prebuilt definitions are convenience defaults, not a separate provider class.
Users must be able to change endpoint/auth/options and add custom models without
forking a built-in implementation.

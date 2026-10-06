# Provider definitions and runtime primitives

Status: executable manifest composition for endpoint, transport, request and
response codecs plus discovery/auth/error/quota binding is implemented; see M9
in the [implementation roadmap](IMPLEMENTATION_PLAN.md) for gaps.

## Goal

Built-in and user-defined providers use the same configuration contract.
Provider identity is data, not a switch statement in the routing kernel. Adding
a provider should be a manifest composition of reusable primitives; if a new
behavior is required, add one reusable primitive implementation and bind it in
manifests rather than duplicating another provider.

## Provider definition

A JSON provider definition identifies a provider and binds operations to typed
primitive references. Relevant contract types are in
`internal/provider/primitives.go`.

```json
{
  "chat": {
    "endpoint": {"kind": "endpoint", "id": "http-json"},
    "endpointOptions": {
      "path": "/chat/completions",
      "query": {"api-version": "v1"}
    },
    "transport": {"kind": "transport", "id": "http"},
      "requestCodec": {"kind": "request_codec", "id": "openai-chat-json"},
      "responseCodec": {"kind": "response_codec", "id": "openai-sse"},
      "errorClassifier": {"kind": "error_classifier", "id": "http-json"},
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
      ├── request/response codec
      ├── model source
      ├── usage/quota source
      └── error classifier
```

An operation is optional when the provider does not support it. Capabilities
describe behavior; they are not inferred solely from a marketing model name.
Bindings are validated at load/startup, not by ad-hoc string edits while
serving a request.

## Primitive responsibilities

| Primitive | Responsibility |
|---|---|
| Endpoint | Resolve the configured API root with operation path and typed path/query overrides |
| Transport | Execute the prepared upstream request with cancellation and bounded timeout |
| Auth | Resolve static credentials or an auth-flow contract into request credentials |
| Request codec | Map normalized semantic input into upstream wire format |
| Response codec | Decode JSON/SSE and emit the selected client format |
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
and Gemini request/response codecs with a generic HTTP endpoint and transport.
The selected definition also binds model discovery, auth flow, error classifier
and quota source. Provider bindings are resolved before the immutable routing
snapshot is published; the kernel receives the composed runtime adapter and
does not branch on provider identity. Built-in JSON definitions and externally
loaded definitions use the same builder.

An operation binding independently selects `endpoint`, `transport`,
`requestCodec` and `responseCodec`. `endpointOptions` can override the relative
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

Providers that require no secret still bind auth explicitly with
`{"kind":"auth","id":"none"}`; the connection can then exist without secret
material. Omitting auth is rejected so an unconfigured route cannot be mistaken
for anonymous access. Provider creation derives its connection credential-type
default from the bound auth primitive; connection setup validates the supplied
secret through the same flow before storing it.
Refresh is bound to that definition and serialized per connection, not behind
a process-wide network lock.

This is not yet a universal plugin system. Interactive authorization-code
start/callback handling, usage extraction, provider sessions, all quota APIs,
embeddings/media operations and every possible codec are not implemented.
Endpoint path/query can be configured in a definition, while a genuinely new
wire dialect still requires one reusable codec implementation registered in
Go. That implementation is then reusable by any definition; it must not add a
provider-specific kernel branch or duplicate another codec.

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

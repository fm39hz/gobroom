# Provider definitions and runtime primitives

Status: manifest/primitives foundation implemented; see M9 in the
[implementation roadmap](IMPLEMENTATION_PLAN.md) for gaps.

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

```text
provider definition
  ├── aliases and capabilities
  ├── auth/session references
  ├── defaults
  └── operation bindings
       ├── endpoint
       ├── runtime adapter and request/response codec
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
| Endpoint | Build operation URL, path and provider headers from typed configuration |
| Auth | Resolve static credentials or an auth-flow contract into request credentials |
| Request codec | Map normalized semantic input into upstream wire format |
| Response codec | Decode JSON/SSE and emit the selected client format |
| Model source | Discover and normalize the provider model catalog |
| Usage source | Extract bounded usage data from real responses |
| Outcome classifier | Convert attempt evidence into typed cause, scope and retry action |
| Limit extractor | Parse request/token/quota windows from response headers and bodies |
| Quota enricher | Optionally fetch exact quota/reset state after relevant evidence |
| Deadline parser | Normalize provider reset/retry timestamps with provenance |
| Health policy | Bind passive breaker and ranking defaults without provider-name branches |
| Session store | Persist protocol continuity/session state when an operation needs it |
| Extension | Optional, typed behavior outside the common operation contract |

Providers may share any primitive. A provider definition composes them; it
does not copy their implementation.

The existing generic error classifier and quota source are migration-era
primitives. Their target replacements and passive/no-polling rules are defined
in [the passive health contract](PASSIVE_HEALTH_ROUTING.md).

## Resolution precedence

Explicit node/connection settings override provider-definition defaults.
Defaults provide convenience only and must not overwrite user configuration.
Credential material belongs to a connection, never to a provider manifest or
route snapshot.

## Current support and limits

The runtime registry currently includes OpenAI Chat, OpenAI Responses and
Anthropic Messages adapters; OpenAI-style model discovery; static model source;
generic HTTP/JSON error classification; generic HTTP/JSON quota source; and
static-secret authentication aliases. Built-in JSON definitions compose these
primitives for compatible provider presets.

This is not yet a universal plugin system. OAuth flows, arbitrary provider
specific endpoint parsing, all quota APIs, embeddings/media operations and
every possible codec are not implemented. Adding a wholly new runtime behavior
may currently require registering a shared primitive in Go; the architectural
requirement is that this registration is generic and not a provider-specific
kernel branch. Manifest-only addition is the target for providers whose needs
fit the existing primitive vocabulary.

## Adding a provider

1. Identify the operations and protocol semantics from authoritative provider
   docs or a captured fixture.
2. Compose existing typed primitives in a JSON manifest if they fit.
3. If they do not, define a reusable primitive contract/implementation and
   tests; do not add a provider-name case to the kernel.
4. Add manifest fixtures for auth binding, model discovery, request/response,
   errors and optional quota/usage.
5. Test the provider through the same connection/configuration path as a custom
   provider.

Prebuilt definitions are convenience defaults, not a separate provider class.
Users must be able to change endpoint/auth/options and add custom models without
forking a built-in implementation.

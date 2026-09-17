# Provider presets and adapters

GoRouter will continue to support prebuilt providers like 9router does. The
difference is that a prebuilt provider is split into two pieces:

```text
Provider preset = defaults and metadata
Provider adapter = protocol/auth/translation behavior
```

## Preset

A preset provides convenient defaults:

```text
id
display name
default protocol
default models endpoint
auth mode
default headers
default timeout
capability hints
```

Examples:

```text
openai
anthropic
gemini
codex
openrouter
g4f
openai-compatible-chat
openai-compatible-responses
```

The preset must never own user credentials, discovered model state or combo
membership. Those remain in SQLite.

## Adapter

An adapter owns behavior that cannot be represented as configuration:

- token exchange or refresh;
- request body translation;
- SSE event translation;
- usage extraction;
- provider error classification;
- provider-specific project/session metadata.

Generic compatible providers use reusable adapters:

```text
OpenAI Chat adapter
OpenAI Responses adapter
Anthropic Messages adapter
Gemini-compatible adapter
passthrough adapter
```

Provider-specific adapters are added only where the generic adapter cannot
handle the service. This keeps the built-in provider list extensible without
making the router core aware of every provider.

## Resolution order

When adding a provider:

```text
1. User selects a preset or custom-compatible provider.
2. User supplies base URL and credentials.
3. Daemon selects the adapter from protocol/preset.
4. Daemon calls the configured models endpoint.
5. Discovered and custom models enter the shared catalog.
6. Combos reference catalog entries or explicit custom routes.
```

An explicit user setting always overrides a preset default:

```text
user protocol > preset protocol > generic inference
user models path > preset models path
user timeout > preset timeout
user capability declaration > discovered capability hint
```

## Why this is better than hard-coding providers into combos

Combos should refer to stable logical references, not provider implementation
details. A provider can be replaced or reconfigured without rewriting every
combo. Physical route expansion happens when the route snapshot is rebuilt.

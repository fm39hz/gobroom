# Normalization pipeline

GoRouter follows the useful design of 9router's `handleChatCore`, but makes the
semantic intermediate representation explicit and typed.

```text
HTTP body + endpoint + headers
  -> format detector
  -> semantic normalizer
  -> conversation invariant repair
  -> target transport selection
  -> protocol translator
  -> provider adapter
  -> upstream
```

The reverse path is an event pipeline:

```text
upstream JSON/SSE
  -> provider event decoder
  -> normalized response events
  -> client format encoder
  -> client
```

## Semantic IR

The IR in `internal/normalize` deliberately contains more than an OpenAI chat
body:

```text
Request
  model
  source format
  messages/content
  tools/tool calls
  thinking intent
  session context
  continuity state
  modalities
  transport hints
  provider-neutral extensions
  original raw map
```

The raw map is retained for compatibility and diagnostics, but provider
adapters must consume the typed semantic fields first.

## Invariants

The normalizer is responsible for invariants shared across providers:

- model is present;
- source format is explicit;
- tool calls have IDs;
- thinking intent is represented independently of provider fields;
- session/continuity metadata is captured before envelope conversion;
- modalities are detected before capability routing;
- unknown fields survive in extensions;
- endpoint-level format takes precedence over body heuristics.

## Translation policy

Adapters may use a direct source-to-target translation when it is more lossless.
Otherwise they may pivot through the normalized IR. GoRouter must not force an
OpenAI wire body to be the internal semantic model.

Optional optimizers such as compression or prompt injection run after semantic
translation and before dispatch. They cannot change routing identity or
silently delete content without an explicit policy.

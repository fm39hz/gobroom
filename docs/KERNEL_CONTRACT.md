# Daemon kernel contract

The kernel is the only owner of data-plane routing decisions. HTTP handlers,
CLI/TUI and background jobs call this contract; they do not parse model names,
walk combos or choose accounts themselves.

## Inputs

```text
NormalizedRequest
  model: public model ID
  messages/tools: normalized conversation
  stream: client stream preference
  extensions: provider-neutral extensions

Credential
  connection ID/type/secret supplied by the credential manager
```

The model field is a public model name. Internal combo names are not accepted
by the data plane unless explicitly published.

## Resolution

```text
Resolve(publicName) -> ResolvedModel

ResolvedModel
  public name
  target reference
  strategy
  ordered route candidates
```

Resolution is performed against an immutable `Snapshot`. A snapshot contains
public models, logical references, combos and physical routes. It is validated
before atomic publication, including nested-combo cycle detection.

## Scheduling

```text
Select(resolvedModel, now) -> Route
```

The scheduler applies capability, cooldown and quota gates before selecting a
route. Round-robin cursor changes are in-memory and scoped to a public model;
they never require a SQLite write in the request path.

## Provider adapter

```text
Prepare(normalizedRequest, route, credential) -> UpstreamRequest
Execute(ctx, upstreamRequest) -> UpstreamResponse
ClassifyError(status, body) -> ErrorClass
TranslateStream(ctx, response, writer, hooks) -> error
```

Adapters own protocol semantics. The kernel owns candidate selection,
retry/fallback boundaries and event emission.

## Fallback boundary

The kernel may try another candidate when:

- preparation fails before an upstream request is sent;
- the upstream request fails before bytes reach the client;
- the adapter classifies the response as retryable/cooldown/auth.

It must not silently switch candidates after the client has received the first
response byte.

## Events

The kernel emits compact usage events after a request. Consumers include:

```text
usage persistence
quota estimation
cost/budget policy
route health
control API metrics
```

Event delivery is bounded and non-blocking from the streaming path.

## Errors

The kernel exposes stable error classes rather than provider-specific error
strings:

```text
model_not_published
no_route
auth
cooldown
retryable
terminal
cancelled
```

Provider-specific status codes and message heuristics stay inside adapters.

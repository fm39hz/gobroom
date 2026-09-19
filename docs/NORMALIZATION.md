# Request normalization and protocol translation

Status: typed inbound normalization and initial adapters are implemented; the
canonical response-event layer and broad cross-protocol semantics remain
planned/partial (M5–M6 in the [roadmap](IMPLEMENTATION_PLAN.md)).

## Pipeline

```text
HTTP path + headers + JSON body
  -> source-format detection
  -> typed semantic request
  -> shared invariant repair/metadata capture
  -> route capability check
  -> provider request codec/adapter
  -> upstream
  -> provider response handling
  -> client-format response
```

The normalized request is not an OpenAI wire body. It carries model, source
format, messages/content, tools, thinking intent, modality flags, transport
hints, provider-neutral extensions and original fields needed for compatibility.
See `internal/normalize/types.go` for the implemented contract.

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

An adapter may translate directly between source and target formats where that
is more lossless, or use semantic fields as an intermediate representation.
The protocol contract must define how tools, reasoning, content blocks, finish
reasons, usage and stream lifecycle map. Information that cannot be represented
must be preserved as an extension, rejected clearly, or handled under an
explicit degradation policy—not silently discarded.

The present adapters include OpenAI Chat, OpenAI Responses and Anthropic
Messages paths. Their supported subsets differ. Anthropic text/tool SSE
conversion exists, but this is not full event parity. The exact tested subset
belongs in the compatibility matrix and adapter fixtures.

## Streaming invariants

- client cancellation propagates to upstream context/body reads;
- a response is considered committed at first write/flush;
- no route fallback occurs after commitment;
- stream errors after commitment terminate/report the stream, not restart it;
- usage and health reporting cannot block first byte or stream writes;
- tool-call state must remain correct when arguments arrive across chunks.

The first four are kernel/adapter contract requirements. Complete shared event
types, cross-adapter fixtures and conformance tests are M5 work.

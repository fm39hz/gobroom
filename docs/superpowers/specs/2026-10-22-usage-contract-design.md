# Hybrid usage accounting contract

Status: direction and written specification approved by the user on 2026-10-22.
Not implemented. The [usage implementation plan](../plans/2026-10-22-hybrid-usage-implementation.md)
and [independent typed-jobs plan](../plans/2026-10-22-typed-job-results-implementation.md)
are approved for execution on 2026-10-22. Supplements
[semantic extension contracts](../../EXTENSION_CONTRACTS.md).

## Purpose and success

Keep GoBroom a lightweight daemon whose modules can support unfamiliar provider
accounting without changing the kernel. Support client usage projection,
trustworthy statistics and investigation without building a general telemetry
or billing engine. Preservation, display and semantic decision-making are
different capabilities; schema validation does not make them equivalent.

Success: a separate public provider module registers, validates, persists and
optionally renders a new usage detail without a provider-specific kernel branch
or table. Common consumers keep working without discovering every extension.
This does not promise arbitrary new pricing or routing behavior from metadata.

## Alternatives and decision

- Fixed typed fields alone give comparable semantics and predictable consumers,
  but unfamiliar details require another shared contract change or remain raw.
- Common typed fields plus registered extensions retain the common path and
  preserve unfamiliar details. They add historical interpretation, disclosure
  and resource-bound obligations. This is the selected direction.
- Putting every measurement in a registry gives structural uniformity but
  adds indirection to common consumers and still requires semantic governance.
  Its wider scope is not justified by current product needs.

No performance advantage has been measured. Benchmark the selected design;
neither registry lookup nor typed fields alone establish product performance.

## Accounting scope and common semantics

Accounting belongs to an upstream attempt. Fallback attempts can each incur
usage; failed/cancelled attempts can have known usage. Frames and client
projections must not cause an attempt to be counted multiple times. This is not
a new crash-proof exactly-once delivery or durable spool guarantee.

Capture original accounting before client transformations. Client projection
cannot overwrite it. Usage, quota/reset reports, pricing, estimated charges
and provider-reported charges remain distinct. Token counts do not prove quota
remaining, and a reported cost does not prove a settled invoice.

Common typed accounting includes total input/output tokens and optional
cache-read, cache-write and reasoning token details, with existing timing,
status, estimated cost and provenance. Presence is explicit: unknown is not
zero. A known cache/reasoning detail is a subset, not an extra amount to add to
the canonical input/output total. Derive a total only when documented disjoint
components provide sufficient evidence; otherwise leave it unknown. Estimates
are not relabelled as observations.

Provider decoders own snapshot/delta/final-report conversion into a coherent
attempt-local accounting view. Cumulative frames are not summed. Corrections
replace the applicable snapshot; resets/decrements are not guessed as deltas.
This normalization is distinct from aggregation across separate attempts.

Common summaries retain known sums and completeness/coverage. A known subtotal
is not a complete total when some attempts did not report the quantity.
Throughput likewise requires appropriate known token and timing values.

Explicit zero and extension-only observations count as reported accounting,
including on failure. An absent field in a later partial report does not erase
an earlier known field; an explicitly reported correction to zero does replace
that field. Decoder completion without accounting must not overwrite the last
known accounting view. These rules require presence information, not a test
that any numeric value is nonzero.

## Extension contract

Reuse the frozen catalog, exact version/digest references, existing JSON Schema
compiler and public module boundary. Descriptors identify a value's exact
version, schema, description, disclosure classification and unit where relevant.
Modules bind typed DTOs through ordinary JSON encoding, not an unchecked shared
map or JSON encoded inside text. Compile descriptors/schemas off the serving path.

An attempt carries a bounded collection of exact-ref values alongside common
typed accounting. One identity occurs at most once in the finalized view.
Owned copies prevent mutable module buffers from changing immutable snapshots
or asynchronously persisted data. Registration grants no automatic aggregation,
pricing, policy execution or client protocol mapping.

Default limits: 32 extension values and 64 KiB of encoded extension data per
attempt, including reference/envelope overhead. Modules may impose smaller
limits; host overrides must remain finite and positive. Do not retain an
unbounded per-frame history. These are safety ceilings, not typical sizes or
performance claims.

Effective limits also obey the enclosing operation/event/output budgets;
extension ceilings cannot authorize a larger response than those budgets.

Unresolved required refs and invalid descriptors reject activation. Invalid
optional runtime values are rejected while valid common accounting is retained;
emit a sanitized accounting-incomplete diagnostic instead of silently writing
zero. A local accounting fault does not penalize provider availability or retry
inference. If an operation explicitly requires valid accounting, its completion
may fail, but an accepted upstream effect still prevents unsafe fallback.

## Persistence, history and disclosure

Persist validated values with exact schema/descriptor identity and a bounded,
immutable data-only description sufficient to inspect provenance and units
after module removal. Reuse schema storage rather than copying schemas into
every inference record. Never reinterpret history using the newest descriptor.
Without its interpreter a historical value is inspectable data, not executable
policy or a newly comparable metric.

Common summaries use typed semantics. Extensions are not automatically summed,
grouped, priced or compared across providers. Arbitrary properties, request IDs
and session IDs do not become aggregate dimensions. New semantic consumers
remain explicit implementation work behind appropriate consumer contracts.
No generic aggregation-expression interpreter is introduced in this scope.

Raw provider usage is separate, opt-in and disabled by default. Capture only
the usage fragment plus provider/API identity and attempt provenance, not
headers, credentials or the whole inference body. Default capture ceiling is
256 KiB per attempt; host limits remain finite. Omit unsafe/oversized fragments
with a diagnostic rather than producing truncated invalid JSON. Raw retention
shares explicit bounded host history retention policy, not indefinite storage.

Both extensions and raw fragments are internal by default. Management reads,
exports, logs and remote sinks require disclosure policy. Valid schemas do not
authorize disclosure. Generic logs and public inference responses receive no
automatic payload dump. Provider-local context is not automatically a global
metric. Preserve JSON numeric precision using existing JSON facilities; this
does not add a decimal dependency or make estimated cost billing-grade money.

## Projection and management

Adapters explicitly map common semantics or understood extensions to client
fields, preserving presence and subset semantics. Unrepresentable optional
details remain internal; do not invent equivalent fields. Required fidelity
uses existing compatibility/loss rules. Permitted native passthrough remains
byte-preserving and is not evidence of normalized cross-protocol support.

CLI/TUI show common statistics directly. Detail views can expose sanitized,
bounded values with identity, unit and provenance, marking incomplete accounting
or unavailable interpretation. A missing module must not break ordinary history
or daemon status views.

## Typed results: separate proof, not a kernel redesign

First replace the jobs example's JSON-through-text path using registered inline
result artifacts and its renderer. Current projectors see artifact metadata;
renderers receive schema-validated inline values. Logical result validation and
validation of the exact client wire envelope are separate obligations.

The fixture must validate both boundaries without a text tunnel or kernel edit.
The renderer consumes the registered result artifact's value; the projector may
validate a declared logical artifact-reference result instead of duplicating
the full client DTO. Revise that logical schema/version explicitly if needed.
Do not connect independently created factories through global result maps,
goroutine identity or an assumed shared request object. No such shared carrier
is established by the current factory contracts.
If a named operation later needs direct projector payload aggregation which
existing primitives cannot express cleanly, review that specific contract
separately. Source-level feasibility is not executed acceptance.

## Delivery and acceptance

Do not retain compatibility shims for replaced pre-release contracts. Revise
affected public host/module and persisted format versions together with their
consumers. Preserve credentials and connection configuration; permission to
reset disposable runtime data is not permission to erase setup. Do not add a
dynamic loader, new telemetry framework or provider-specific kernel switches.

The implementation plan must name tests for:

1. Unknown/zero, subset totals, partial coverage, snapshot/delta/final correction,
   separate fallback attempts, and known usage on failed/cancelled attempts.
2. External-module registration/persistence/rendering without kernel edits;
   exact refs, schemas, duplicates, bounds and buffer ownership rejection.
3. Visible optional accounting faults without false provider health penalties
   or retry; required completion failure respects upstream-effect fences.
4. Restart/module removal, historical descriptor identity, no automatic extension
   aggregation, and no reinterpretation by current schemas.
5. Provider/client accounting isolation and no accidental raw/extension leakage
   through logs, responses or secret-free configuration exports.
6. Disabled raw capture, finite retention, oversized/unsafe fragments, numeric
   precision, and bounded persistence backpressure/drain behavior.
7. Module-only typed job results and independently validated client envelopes.
8. Ordinary JSON/SSE versus extension-bearing CPU/allocation/bounded-memory
   measurements. Proxy microbenchmarks do not establish isolated daemon RSS
   or production tail latency.

Written-spec review precedes implementation-plan review and execution. Direction
approval does not close C1, M15 or the broader architecture acceptance gate.

## Existing-source impact review

The following source findings identify why a shared struct-only patch would
not implement this specification. They are not newly executed acceptance:

- `internal/kernel/response_failure.go:knownProviderUsage` recognizes only
  nonzero common counters/cost. `kernel.go:recordProviderUsage` replaces its
  view wholesale. Presence, partial reports and explicit zero need end-to-end
  treatment, not just nullable storage columns.
- `internal/kernel/pipeline.go` observes original provider usage before client
  transforms and stages completion accounting. Preserve that boundary and
  extend its ownership guarantees to new nested values. Existing
  `TestResponseTransformDoesNotRewriteProviderUsageAccounting` covers common
  counters, not unimplemented extension payloads.
- `internal/kernel/kernel.go` handles HTTP error statuses before response
  decoding. Its later render-failure path enriches known usage separately.
  Attempt accounting acceptance must include reported usage on error-status
  paths as well as accepted-response stream failures. Unknown accounting must
  remain unknown; this is not a mandate to invent a charge for every rejection.
- `internal/adapter/openai/events.go:openAIUsageFromMap` currently reduces
  usage to fixed token counters; Anthropic and Gemini have separate conversion
  paths. Each supported decoder and renderer needs semantic evidence rather
  than assuming a shared type change automatically fixes all protocols.
- `internal/store/store.go:SaveUsageEvent` and `UsageEvents` persist/read fixed
  counters, while `runtime_statements.go:usageDailySQL` unconditionally sums
  them. `internal/tui/rows.go` formats them as present numbers. Presence and
  coverage must survive the store/query/control/display boundary, not merely
  response decoding. Storing the authoritative provider view does not require
  a second durable copy of every client projection.

Existing drain/failure tests demonstrate persistence lifecycle behavior, not
the new accounting semantics. This read-only review ran no tests and does not
upgrade those tests into unknown/zero or extension acceptance evidence.

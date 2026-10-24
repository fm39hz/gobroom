# SG1 public-module composition proof

Agent-selected bounded acceptance under the standing implementation instruction.
The objective remains architecture closure, not a new protocol framework.

Prove tools, explicit reasoning budget and scoped signed-tool continuity on a
single real daemon path through configured request/response transforms. Use
the actual Gemini codec, session store and OpenAI Chat renderer. Bootstrap the
signature by consuming a real provider response, then echo it with the exact
tool history on the same session; never preseed kernel/private proof state.
Reject changed session/branch/target and unsupported cross-family continuation
before upstream dispatch. An ordered incompatible route must not receive the
budget request merely because it accepts OpenAI syntax.

Scoped signature history and opaque previous-response IDs are distinct
continuity mechanisms. This proof covers the former positively; unsupported
previous-response IDs must reject, not be discarded or called equivalent.
Existing provider expiry/reset proofs remain separate. No invented provider
wire format is needed to force an unsupported positive mapping.

Use a module-owned schema-configured prompt transform over an existing plain
system layer, retaining its provenance and role, so conversation context is
identical on both turns. Appending a hidden
user turn on every request would legitimately change the signed prefix; do not
weaken scope checks to admit that. Reuse the existing response-transform module.
Assert actual wire budget/tool/history/prompt and transformed client text.

In parallel, carry tools/budget through the real Anthropic binding: deny an
unapproved max-output adjustment, allow its exact named loss through both model
policy and server ceiling, assert actual wire and usage evidence. Exercise
Anthropic signed-thinking rendering with the correct issuer and without an
active semantic response mutation; enabling such mutation must reject before
dispatch rather than fabricate a signature. This is not a claim that all
thinking/signature formats translate across providers.

Use public module/daemon/IPC/HTTP APIs, typed JSON, existing codecs and JSON
schema compiler. No new dependency or provider-name kernel branch. Learn the
budget/signature constraints from adjacent CLIProxyAPI source; reuse Gobroom's
own primitives and attribution. Only main Go/format/test/install/commits;
workers own disjoint test files. Tests may initially prove existing behavior;
any production change requires a concrete executed RED first. Fake upstreams
and temporary configuration only; actual credentials/config remain unchanged.

Executed follow-ups: align the Gemini typed reasoning wire binding with its
declared budget mapping. Preserve `json.Number` values from management IPC in
the common typed scalar normalizer, rejecting fractional integer budgets instead
of silently rounding. Execution and route explanation must share the same
candidate-specific session restoration/attestation step. Preflight remains
read-only with respect to inference, response/session publication and health;
it may read/prune expiring cache entries. Neither opaque state nor raw signature
bytes become explanation output. Actual execution's invalidation/version/replay
contracts stay unchanged.

Review correction: HTTP/CLI/IPC preserve number tokens. Equivalent integer
spellings share an exact machine-integer policy; near-integer fractions never
round into valid budgets. Stdlib JSON and rational arithmetic provide binding,
with a 4096-byte integer-token bound. Strict wire validation remains enabled.
Output reservation consumes typed Generation, not provider-format Raw keys.

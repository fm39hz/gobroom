# SG1 tools, budget and scoped-history acceptance

Implementation checkpoint: `5b4c219`, following the
[design](superpowers/specs/2026-10-24-sg1-composition-design.md) and
[plan](superpowers/plans/2026-10-24-sg1-composition.md).
This closes the named public-module intersection, not all SG1 or M15 breadth.

Verified 2026-10-24: full root/module regression and vet passed after the
review-fix pass; focused root races and public SG1 races passed count3 (module
15.343s). `make install-all` succeeded. The installed gate initially exposed an
incorrect fixture assumption (`usage --limit`); the fixture now invokes the
actual `usage` command, and the explicit installed gate passed race3 in 17.878s.
Actual daemon status was healthy with control=serving=517, bundle-v9
export→validate valid and scoped configuration/credential fingerprint unchanged.
No configured live upstream was called.

| Requirement | Executed evidence |
|---|---|
| Tools + explicit budget + scoped continuation + configured transforms | `TestSG1GeminiToolsBudgetScopedHistoryAndTransforms`: real daemon/IPC/HTTP, a provider-issued tool signature, same-session follow-up, actual Gemini tools/history/budget 2048/system prompt and transformed OpenAI client text |
| Reject incompatible earlier members | The same fixture has an earlier OpenAI Chat member; it receives zero upstream requests while Gemini receives exactly the two valid requests |
| Reject wrong session/branch/tool and unsupported response IDs | `TestSG1GeminiRejectsInvalidSignedContinuationsBeforeDispatch`: real issued proof, preflight and execution denial, no extra upstream calls or private data; the original valid continuation succeeds afterward |
| Management agrees with consumption | `TestSG1SignedPreflightMatchesScopedExecution`; execute/explain share candidate session restoration and attestation, without exposing provider state |
| Named loss requires model and server permission | `TestSG1AnthropicToolsBudgetRequiresNamedLossGrants`: separate missing-model/missing-ceiling denials, approved 512→3072 wire adjustment, preserved tools/budget/prompt, selected usage reports exact named loss |
| Signed Anthropic rendering respects mutation boundaries | `TestSG1AnthropicSignedThinkingRejectsSemanticResponseMutation`: active semantic response transform rejects before dispatch; disabling it preserves thinking/signature/tool identity with the request transform still active |
| Admission and wire contract agree | `TestPrepareAcceptsTheChatBudgetDeclaredByCompatibility` reproduced missing Gemini budget binding; `TestPreparePreservesEquivalentBudgetSpellings` covers actual wire encoding |
| Exact numeric policy across HTTP and IPC | `TestHTTPAndIPCUseOneExactIntegerBudgetPolicy`, common scalar tests and strict-binding tests: integer/decimal/exponent forms agree; near-integer fractions and overflow do not round into valid budgets |
| CLI does not round before IPC | `TestRouteExplainPreservesRequestNumberTokens`, stdin and file; config and request input share one bounded token-preserving reader |
| IR owns reservation and explicit intent | `TestOutputReservationUsesCanonicalGenerationNotProviderRawFields`; `TestReasoningBudgetPreservesSufficientExplicitOutputLimit` |
| Installed controls | `make test-installed-sg1 GOBROOM_TEST_CLI=/absolute/path/to/gobroom`: CLI route-explain stdin, transform list, usage plan/mapping/privacy and healthy status through explicit temporary IPC, plus SG1 cases under race/count3 |

The prompt module edits an existing text system layer without changing its
origin/role. It does not invent harness authority or append a hidden turn that
would change the signed conversation prefix. Signature proof comes from actual
provider output; no private cache seeding or synthetic executor is used.

Signed tool history and opaque previous-response IDs remain different
continuity mechanisms. Gemini signature continuation is proven positively;
unsupported previous-response IDs are rejected, not silently dropped. These
tests do not claim cross-provider signature portability or every renderer pair.

The number policy reuses stdlib JSON and rational arithmetic, not a handwritten
number parser or a new dependency. Integer tokens are bounded to 4096 bytes.
Known wire fields retain strict member/duplicate/trailing validation. Kernel
output reservation uses canonical Generation rather than provider Raw keys.

Deferred coverage minors: negative cases do not assert a facet-specific
rejection reason; a dedicated preflight session/health spy matrix for load
failure, zero store version and cancellation remains unexecuted. The shared
helper is read-only on inspection, and ordinary execution retains its existing
version/invalidation/replay tests. No universal leak-freedom claim follows.

Provider-specific reset/expiry behavior, remaining cross-codec policy evidence,
public auth-race acceptance, difficult-provider onboarding, live harness/provider
coverage and whole-product performance remain separate obligations.

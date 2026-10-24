# M15 architecture conformance evidence

2026-10-24 SG1 follow-up: `5b4c219` executes the named tools/budget/scoped
signed-history intersection through configured public-module transforms and
real daemon/IPC/HTTP. It also covers independent Anthropic loss grants and
signed-renderer mutation boundaries. Regression exposed and corrected typed
Gemini wire mismatch, numeric HTTP/CLI/IPC disagreement, canonical output
reservation and session-aware preflight. The
[acceptance map](SG1_COMPOSITION_ACCEPTANCE.md) records verified/installed cases
and deferred coverage minors. This is not universal provider/harness parity.

2026-10-24 portability follow-up: `2f94fe8` adds explicit startup bundle import
before catalog activation and an independent-module fresh-target/restart proof.
It preserves artifact recipient/exact code pins, rejects invalid runtime config
before durable apply, and isolates candidate preview from other memory databases.
The [acceptance map](MODULE_PORTABILITY_ACCEPTANCE.md) names real serving,
credential preservation, rejection/reopen and installed CLI cases. This closes
the named export/import/restart delta, not SG1 or complete C6/M15.

2026-10-24 session follow-up: `eeec107` / `7eda7ed` closes ordinary session
persistence failure visibility, not the entire M15 gate. Safe six-field status,
constant warning/Stop sentinel, expected typed supersession versus SQL failure,
live-state preservation and provider-health/replay isolation passed core/host
race tests and the independent principal/endpoint regression. The first failure
during shutdown is tested through a real locked SQLite writer; a pre-drain
snapshot mutation fails. Full regression/vet/conformance and installed CLI
session gate passed; main healthy 513 and config fingerprint unchanged.
Final review retains one nonblocking queued-drain-specific coverage minor.
The [finite audit](ARCHITECTURE_AUDIT_2026-10-24.md) separates the subsequent
client-failure implementation/proof from remaining public-boundary acceptance.
The [F1 acceptance map](CLIENT_FAILURE_ACCEPTANCE.md) covers safe registered
precommit encoding, independent wire schemas, dispatch/replay and commitment
fences, staged-header privacy and the dedicated installed CLI gate. It closes
that bounded implementation gap, not SG1 or complete M15.

Status: composition simulations implemented; full semantic closure gate
pending. M15 asks whether new behaviors compose through stable contracts.
The [semantic extension design](EXTENSION_CONTRACTS.md) fixes the remaining
contracts. Run the existing composition subset with:

```sh
make test-conformance
```

The command currently exercises the following composition simulations. It
must not be described as the complete semantic closure suite until the
acceptance cases below are implemented.

## Architecture audit correction — 2026-10-22

Registration/catalog completeness is not semantic-envelope completeness.
The table records the audit baseline. Verified follow-ups below supersede its
source-state findings, but do not yet close the complete no-kernel-change
catch-up criterion; passing existing simulations alone does not remove gaps.

| Boundary | Inspected evidence | Current conclusion |
|---|---|---|
| Additional usage dimensions | `internal/kernel/contract.go:UsageEvent` has fixed common counters/cost/timing, no registered measurement/value collection. The Qoder usage normalizer in the adjacent source preserves cache-read/cache-creation/reasoning details. | A new first-class dimension currently needs a shared type change. Native raw preservation is not a normalized usage extension. |
| Typed operation results | `examples/external-provider-module/jobs.go` emits marshalled job DTOs as `EventTextDelta`; its projector reparses that text. `operationEventPayload` carries artifact metadata, but the renderer receives the full artifact including inline values. | The text tunnel is an example implementation choice, not proof that every typed result needs a kernel change. A module can declare a logical artifact-reference result and render its schema-validated value. Direct projector aggregation over values is a narrower, separate limitation. |
| Existing artifact extension arm | `extensions.Snapshot.validateArtifact` validates inline values against exact registered schemas and enforces owner/recipient/expiry policy; response scopes live in `internal/artifacts/response.go`. | Reuse this legitimate primitive. Do not infer that every custom event requires a new kernel enum merely because common fields are fixed: event kinds are string-backed and registered artifacts are supported. |
| Auth/replay | Public-module setup/resource/owner/activation fixtures and job/replay-shutdown fixtures exist. | No missing generic replay mechanism was established in this audit. Remaining proof must be a finite clause-to-test map, not a requirement for one giant universal matrix. |

Approved usage direction: retain efficient common typed values
and add catalog/schema-bound usage extensions. For results, first prove the
existing registered-artifact/renderer path without a text tunnel; do not add a
second payload system or change the projector interface merely because the
example currently uses text. A projector's logical result schema and the client
wire envelope are distinct contracts. The hybrid usage direction is now approved
in conversation; its
[written specification](superpowers/specs/2026-10-22-usage-contract-design.md)
is approved; the [implementation plan](superpowers/plans/2026-10-22-hybrid-usage-implementation.md)
and [typed-jobs proof plan](superpowers/plans/2026-10-22-typed-job-results-implementation.md)
are approved for execution. Follow-ups below record implementation evidence;
the original audit table is historical, not a claim that its missing usage
envelope remains absent. The overall semantic closure gate is still open.

### Source follow-up — typed results and usage models

Execution correction: the real typed-jobs fixture rejects an artifact-only
decoder before dispatch. `RequiredResponseEvents` currently mandates
`text_delta` even for non-chat operations. Thus the artifact/renderer payload
path exists, but the original unmodified-kernel admission claim was incomplete.
A separately tested generic correction was required; declaring unused text
support to evade admission would not satisfy this proof. `0d16e4c` fixes that
shared assumption, preserving chat/tool/thinking requirements and existing
artifact-output admission. It has independent clean review and root regression.

After that prerequisite, module changes `7796a04` / proof fix `e2ef9d5` use
registered result artifacts, logical metadata projection and independently
schema-validated client envelopes. The real-daemon lifecycle checks decoder
events and each concurrent caller's own upstream ID, not just aggregate sets.
Create/status/cancel, handle expiry/owner/restart and accepted-effect fences
passed full external tests, focused three-run race and explicit installed-CLI
three-run race acceptance after `make install-all`. Main status=ok,
control=serving=500. Full root/external regression, vet and existing conformance
passed. Both task reviews and scoped proof-fix review are clean. This does not
introduce direct projector access to artifact values or close all C1–C6.

Usage catalog Task 1 is verified in `314e45d` and `a15af67`: default64KiB
validation plus explicit host limits, bounded metadata, private classification,
factory-free exact schema bindings and compiler-derived schema dependencies.
Full root regression/vet, selected external public-module and installed CLI
catalog acceptance passed; independent review and bound-fix re-review passed.
Task 2 is now verified separately (`7a36239`, `baad314`, `dffc8f6`): the common
envelope has presence-aware cache/reasoning counters, registered values and
bounded diagnostics. Partial patches and full snapshots share one merge rule,
including retraction to unknown. `TestGeminiNativeSnapshotInvalidationReachesClientAndKernelTerminal`
proves that retraction reaches both client output and original accounting;
`TestAnthropicRendererMergesPartialUsageInJSONAndSSE` proves partial retention.
Store and summary tests prove NULL/zero and per-metric coverage; explicit
`TestInstalledCLIUsagePresence` passed three race runs after installation.
Full root/external tests and vet, existing conformance, and selected three-run
race suites passed. Main control=serving=501, status=ok. Per-dispatch error
accounting, historical extension metadata, capture/disclosure and their external
module proof were still later tasks at that checkpoint; this did not close C1
or all C1–C6.

Task 3 is verified in `bb16321` / `ccefbc3`: host invocation/dispatch IDs,
once-only finalization across failure/fallback/auth retry, bounded source error
evidence, exact operation usage requirements and optional-fault health neutrality.
Direct callbacks and aggregate snapshots obey host/operation/schema/envelope
bounds. `TestAttemptUsageCompletionDoesNotInventClientPayloadFields` separates
operational accounting from strict client event schemas. SQL IDs round-trip
and duplicate nonempty attempt IDs cannot increment daily totals twice.
Full root/external regression/vet and selected three-run race passed; existing
conformance passed on `bb16321`, followed by fresh full/race verification of
the small terminal-cancellation correction. Installed CLI attempt and presence
acceptance each passed three race runs after installation. Main status=ok,
control=serving=502; configuration/credential fingerprint unchanged. Historical
extension details, capture/disclosure and full public-module/performance proof
remained open at that checkpoint. No live upstream was called for acceptance.

Tasks 4–5 are now verified (`d2c587c`, `29ebd03`, `2e8e5ed`). Typed protocol
bindings preserve documented cache/thinking semantics, malformed corrections
and accounting on incomplete JSON responses. Owned callback snapshots prevent
client mutation of original charge bytes. Private reported-charge values retain
precision, explicit currency when available and provenance without becoming
estimates or aggregate charges. JSON/SSE real-daemon acceptance verifies each
invocation's own value, not merely an aggregate set.

Historical data-only descriptors and exact schema documents/pins survive
module removal. Full closure verification rejects missing or altered links,
dependency documents and coordinated digest rewrites; no current catalog,
module factory or schema reinterpretation is required. Atomicity, pruning,
metadata recreation and module rebuild tests passed. The internal repository
detail API is not yet a management disclosure grant.

Full root/external tests and vet, selected three-run race, independent scoped
reviews and installed CLI acceptance passed after `make install-all`.
Installed history/module-removal, charge JSON/SSE, attempt and presence tests
each ran three times with race detection. Main status=ok, control=serving=504,
configuration/credential fingerprint unchanged, no additional history lost.
The full conformance rerun passed after an unreproduced external-suite failure
whose initial output omitted the exact failing test. Tasks 6–8 still own raw/
authorized details, complete external usage-module inference proof and measured
resource costs at that checkpoint. No live upstream call or new dependency was used.

Tasks 6–7 are now verified (`0bcfe20`, `767bab1`, `9b0e8e9`, `71e66f4`,
`881f453`). Host 13 exposes typed bounded raw capture and independent detail
grants; raw capture remains off by default. HTTP control now requires its own
bearer token, separate from inference authentication. Exact numeric retention
and read-time expiry protect private/raw history while bounded pruning catches
up. TUI request epochs/resource identity prevent stale or cross-pane detail
disclosure; terminal output is escaped and capped.

`TestExternalUsageExtensionSurvivesRestartAndModuleRemoval` exercises a genuine
separate module, source/header evidence, typed precise private search units,
client-only usage transformation and historical reads after supported
active-config/module uninstall. No feature-specific kernel branch or dependency
was added. `TestDaemonAccountingQueueFailureDoesNotReplayInference` blocks the
actual SQLite writer and fills the real event queue, observes exactly one
successful inference/accounting drop, then drains accepted events. Existing
required-ref/options, schema, identity, bounds, ownership and cooperative-drain
tests provide the negative-case layer; the new vertical does not independently
re-prove every negative.

Fresh regression/vet/conformance and selected three-run race suites passed.
After installation, `make test-installed-usage` explicitly ran five root CLI
fixtures plus `TestInstalledCLIUsagePreservesPresenceAndPrivacy`, three race
runs each; invalid CLI paths fail before Go instead of silently skipping.
Main status=ok, control=serving=506, config/credential fingerprint unchanged,
usage rows 0/0. Task 8 measurements/final combined review remain open; this
narrows C1's usage gap without closing C1–C6 or M15. No live providers called.
Task 8 is now complete (`fd33fb0`, `1be757f`, `1a642b1`): five-sample paired
actual JSON/SSE proxy measurements close the helper-only gap, with zero usage
drops but measurable allocation costs. Full post-fix regression/vet/conformance
and installed acceptance passed, including the entire external module suite
with installed CLI and race count 3 (139.466s). Final combined/scoped review
approved both finite plans with no remaining findings. Main status=ok,
control=serving=512, config/credential fingerprint unchanged, usage rows 0/0.
This closes the finite usage/typed-results proof, not the broader C1–C6 audit,
isolated RSS, sustained p99 or provider/harness breadth.
The concise [usage acceptance map](USAGE_ACCEPTANCE.md) links the eight spec
items to named tests and records the remaining measurement/product boundaries.

The read-only follow-up narrows the earlier result claim. Registered inline
artifact values are validated by the catalog and output contract before the
renderer receives `ResponseEvent.Artifacts`. A custom renderer can decode that
validated value into its client DTO. A logical result schema may describe the
artifact reference/metadata; it need not equal the client wire response schema.
The current projector cannot inspect inline values through its metadata-only
event input. That limits direct payload aggregation, but does not eliminate the
existing typed client-result path. Arbitrary surrounding JSON emitted by a
renderer is not automatically validated by the artifact or logical result schema.
That was source-level feasibility evidence, not an executed replacement of the
jobs example. The later typed-jobs fixture described above supplies the executed
proof, including its separately identified generic admission prerequisite.

| Local reference | Observed representation | Implication, not a drop-in dependency |
|---|---|---|
| Bifrost `core/schemas/chatcompletions.go` and `responses.go` | Common totals plus typed cache/read/write/reasoning details and non-token units. Served-tier/geography/model hints include internal-only fields. Cache aliases are normalized explicitly; cache writes are not exported as cache reads. | A normalized measure needs inclusion/unit/projection semantics. Merely preserving extra JSON or adding numbers together is insufficient. Its growing provider-specific shared fields are not a schema registry. |
| TensorZero `crates/tensorzero-types/src/usage.rs` | Optional common/cache counters, decimal cost, and separate `RawUsageEntry` carrying provider/API identity plus raw JSON. `None` distinguishes unreported cache usage from a reported zero. | Separate normalized accounting from raw preservation, and preserve unknown-vs-zero. Raw usage does not by itself make reasoning/custom dimensions normalized. The Rust application types are not a Go library to import. |

Any approved usage extension must address per-response snapshot/delta handling,
cross-invocation aggregation, units, overlapping/subset token counts, optional
values, original-provider versus client-transformed data, and public projection
versus internal context. Existing catalog/schema validation should be reused;
the source comparison alone does not authorize implementation before plan review.

Session admission regression: `TestCacheSaveReportsOverflowWithoutDiscardingLiveState`
failed before the bounded fix because full queues returned nil. Save now preserves
the latest live state, counts the rejected write once and returns an error.
It does not roll back the useful live state or change acknowledged invalidation.
The kernel still ignores Save errors; this is not complete session persistence
observability or a new durability guarantee.

Bounded checkpoint `103dbbc`: red/green reproduction, full root/external
regression/vet/conformance, three complete session-store race runs and
independent review passed. After `make install-all`, the existing public-module
continuity fixture passed three race runs using installed CLI controls. Main
control=serving=498 and bundle-v9 validation were healthy. That installed test
checks ordinary continuity/restart regression, not queue rejection visibility
through CLI. Semantic-envelope design review and session error reporting remain
open; this does not complete C1–C6 or the user’s architectural success criterion.

The target begins with a focused transform-boundary group: opaque metadata on
appended IR is rejected transactionally, canonical message suffixes are
accepted, and Chat/Anthropic/Responses egress mappings plus their fail-closed
opaque/order cases are exercised before the broader M15 composition suite. It
also runs the concurrent HTTP disconnect-through-kernel test and daemon
shutdown-before-drain cancellation test.

OAuth cancellation evidence also includes
`TestAuthorizationCancellationFencesLateSuccessfulToken`: deliberately late
successful drivers cannot write credentials after explicit session cancellation,
coordinator shutdown or daemon context cancellation in either flow, nor after
caller cancellation or session expiry in a code exchange. The test holds each
driver open after cancellation and verifies the authorization worker drain has
not returned, then checks the original credential survives its late success.
Expiry remains visible rather than being overwritten by exchange completion.
`TestDeviceAuthorizationStartCancelsAndDrainsBeforeLateSuccess` additionally
holds the initial device acquisition after daemon cancellation and proves it is
drained without spawning a poller, even when its driver returns a valid challenge.

Credential lifecycle evidence now also covers context-cancellable per-connection
serialization, late successful Resolve/Refresh after cancellation, concurrent
refresh callers sharing one persisted replacement, and delayed device completion
after replacement/mode/disable/removal. `TestCredentialRevisionRejectsStaleCompareAndSwap`
uses durable revisions to reject A → B → A, same-secret resaves, disable/enable
and same-ID recreation; revision rotation/persistence and canceled SQL CAS have
separate tests. This strengthens SG2's setup/refresh generation contract, not
live OAuth-provider coverage. Post-commit reset/publication/version failures now
have the root and public-module acceptance described below, including installed
retry/restart recovery in `TestExternalCredentialRefreshActivationRetryAndRestartRecovery`.
A durable credential is not rolled back by a later reconciliation failure; this
evidence does not establish universal auth/provider coverage.

`TestOAuthAuthRefreshesAgainstTokenEndpoint` covers explicit refresh when the
rejected access token is expired, still within its recorded lifetime, or has no
expiry. Gobroom seeds `x/oauth2` with refresh state only: its caching TokenSource
must not hand back the rejected token just because its local expiry looks valid.

Installed acceptance (`2abe99c`): after `make install-all`, run
`GOBROOM_TEST_CLI=/absolute/path/to/gobroom GOWORK=off go test ./... -run '^TestExternalDeviceAuthorizationCannotOverwriteNewerConnectionCredential$' -count=1`
inside `examples/external-provider-module`. This passed device-start, update,
device-get (failed stale generation), and an operation probe authenticated with
the replacement credential through the installed CLI. The main daemon remained
`ok`; installed config export → validate passed. No live provider is called.

| Change simulation | Executable evidence |
|---|---|
| API-key provider, `/models`, operation bindings and reusable primitives | `TestGenericProviderManifestBindsProtocolsAndSemanticTasks`; daemon manifest/discovery path in `TestDaemonPersistsManifestClassifiedQuotaEvidenceAcrossRestart` |
| Side-by-side provider-definition contracts and pinned static-module ownership | `TestProviderDefinitionVersionsBindSideBySideByExactRef` loads the same provider ID at contract v1 and v2, builds two distinct operation bindings, selects each by exact ref, and rejects an unregistered v3; provider-node persistence and bundle v9 preserve those exact refs. `TestDependencyLockPinsOnlyReachableStaticModulesAndHostContracts` proves lock v2 records only modules reachable from exact roots and rejects a changed build or host-contract version; `TestStaticProviderModuleAddsOperationUsedByManifestWithoutKernelChanges` verifies module ownership propagates through operation registration. |
| Statically linked provider module contributes a non-chat operation and a separate manifest binds shared primitives | `TestStaticProviderModuleAddsOperationUsedByManifestWithoutKernelChanges` registers a versioned operation in a module, loads a provider manifest that selects the shared multipart/HTTP primitives, builds exact runtime bindings and prepares the operation request; `TestDaemonCompositionRootRegistersStaticProviderModule` proves the daemon accepts module registration at its composition root and projects its operation metadata through provider-catalog IPC. |
| Separate Go module imports the public provider/daemon API and hosts its module | `make test-external-provider-module` runs the independent `examples/external-provider-module` module; `TestExternalModuleImplementsQuotaSourceThroughPublicTypes` compiles and executes a custom `QuotaSource` using only exported `pkg/provider` quota request/snapshot types; `TestExternalModuleCanDeriveExactProviderSessionScope` compiles the session-key helper using only the public API and checks all five scope dimensions. `TestExternalModuleBindsManifestAndPreparesRequest` consumes exported `Credential.AuthContext` in a custom request codec and applies shared auth presentation through the public helper. `TestPublicDaemonHostsExternalProviderModuleAndManifest` creates a provider and connection, assigns a model, builds a Physical/Combo graph, checks `/v1/models`, then successfully sends both a JSON operation and a bounded multipart WAV artifact through module-owned ingress → daemon/kernel → provider codec/decoder → client renderer. The bounded interpreter derives a versioned codec requirement; its external feature evaluator gates the assigned model before dispatch; the shared multipart provider codec streams the same lease to a fake upstream, which verifies auth and exact parts before returning the typed transcript. `TestExternalRequestCodecEncodesThenSignsBodyWithoutKernelChanges` is a source-derived Qoder COSY fixture: an external module encodes body bytes, signs those exact bytes, sends the request through the shared endpoint/transport, verifies the signature at a fake upstream and decodes a Qoder-style SSE envelope, with no kernel modification. This proves the extension seam, not full Qoder provider parity. |
| Module-owned client wire ingress reaches the shared daemon execution boundary | `TestStaticProviderModuleAddsOperationUsedByManifestWithoutKernelChanges` proves an exact versioned ingress descriptor depends on its operation and its module is lockable; `TestDaemonCompositionRootRegistersStaticProviderModule` sends valid and invalid requests to a module-owned path and verifies decoding reaches the shared executor or common 400 boundary; `TestExternalModuleCanRegisterReusableMultipartClientIngress` verifies a separate Go module binds the public multipart contract; `TestPublicDaemonHostsExternalProviderModuleAndManifest` uses the real daemon HTTP listener to execute a JSON non-chat request and a genuine multipart WAV operation through the shared kernel and fake upstream. |
| Provider auth factory, per-connection auth-mode selection, shared credential presentation and daemon-owned OAuth lifecycles | `TestLoaderBuildsTypedPhysicalAndComboGraph` proves one provider's API-key and OAuth connections bind different auth flows/presentations into distinct immutable routes; `TestConnectionCanBeCreatedWithoutTokenOnlyForInteractiveOAuth` proves pending credentials are accepted only for a declared interactive mode; `TestCredentialPresentationAppliesTokenAndProtocolHeaders`, `TestCredentialPresentationRejectsSecretsInStaticHeaders`, `TestCredentialPresentationRejectsTransportControlHeaders` and adapter fixtures cover shared header application; `TestProviderSpecificDeviceOAuthFlowUsesGenericAuthExtensionContract` proves an external auth primitive can produce connection-scoped dynamic request headers without provider branches; `TestPublicDaemonHostsExternalProviderModuleAndManifest` proves an external AuthFactory produces bounded AuthContext consumed by a module codec through daemon HTTP; `TestConnectionFormCyclesAuthModesAndRebuildsFields` covers the TUI mode switch. Authorization-code/device evidence: `TestDaemonOwnsOAuthStatePKCEExchangeAndRejectsCallbackReplay`; `TestAuthorizationCodeExchangeCannotOverwriteNewerConnectionCredential` delays an authorization exchange while a user updates its connection, proving the credential-generation fence rejects stale results; `TestAuthorizationCodeLoopbackCallbackCompletesWithoutFrontendExchangeCall` covers target rejection, success, denied and expired terminal states plus listener release; `TestDaemonOwnsDeviceAuthorizationPollingAndKeepsDeviceCodePrivate` verifies pending, RFC 8628 `slow_down` (+5 seconds via the pinned x/oauth2 implementation), completion and credential persistence; `TestDaemonStopCancelsDeviceOAuthPollBeforeClosingStore` proves daemon shutdown waits for an active device poll to stop before store close; `TestDeviceAuthorizationReservesConnectionBeforeStartingProviderFlow`; `TestConnectionFormUsesProviderAuthSetupSchema`. Live provider behavior, HTTPS/non-loopback integrations and broader shutdown races remain open. |
| Safe provider configuration metadata for generic control clients and schema-driven TUI auth forms | `TestDefinitionCatalogExposesGenericSetupMetadataWithoutSecrets`; `TestProviderDefinitionMetadataIsAvailableThroughGenericControlAPI`; daemon IPC catalog assertion in `TestIPCControlCRUDUsesDaemonServices`; `TestConnectionFormUsesProviderAuthSetupSchema` |
| Versioned provider/primitive refs and schema catalog through IPC/HTTP/CLI | `TestPrimitiveReferencesRequireTheExactContractVersion`; `TestCatalogPinsSchemaAndModuleVersionsAndValidatesBeforeFactory`; `TestExtensionCatalogEndpointExposesContractVersionsAndOptionsSchemas`; `TestIPCControlCRUDUsesDaemonServices`; `TestRootHelpAndTypedCommandsDoNotRequireSeparateTUIBinary`; installed CLI smoke is part of the C1 check |
| Versioned operation definition, request payload schema and replay declaration | `TestChatDefinitionCompilesTypedInputAndPinsReplayContract`; `TestRegisteredNonChatOperationValidatesItsOwnPayload`; `TestRuntimeBindingAcceptsNewNamespacedOperationWithoutOperationSwitch`; `TestKernelRoutesNewOperationUsingGenericRouteTaskContract`; `TestOperationSchemaFailureUsesClientErrorBoundary`; custom ingress version/payload assertion in `TestNamespacedOperationIngressRegistersWithoutServerRouteBranch` |
| Operation input, semantic response event and rendered output resource bounds | `TestOperationPayloadBoundRejectsOversizedBodyBeforeSchemaDecode`; `TestComposedSemanticPipelineBoundsEachDecodedResponseEvent`; `TestResponseCommitWriterEnforcesOperationOutputBound` |
| Artifact owner/replay/sensitivity/recipient/media/size contract; named operation artifact ports; multipart-to-spool ingress; authorized lease reads | `TestArtifactContractEnforcesOwnerScopeSensitivityAndBoundedBodyRef`; required role/type/count, frozen descriptor projection and recipient validation in `TestOperationAcceptsOnlyRegisteredOwnedArtifacts`; exact field/role/type registration plus streamed multipart body in `TestMultipartOperationIngressMustMatchOperationArtifactPorts` and `TestMultipartOperationIngressSpoolsDeclaredArtifact`; partial-decode cleanup in `TestMultipartOperationIngressReleasesPartialSpoolsOnDecodeFailure`; `TestMultipartOperationExecutesThroughHTTPKernelAndAdapter` proves multipart HTTP → operation normalization → kernel → authorized adapter lease → response; bounded store/lease lifecycle and exact recipient context in `internal/artifacts` tests. Built-in provider codecs still do not render operation artifacts as native multimodal request parts. |
| Versioned ingress operation, provider task ref and route binding | `TestNamespacedOperationIngressRegistersWithoutServerRouteBranch`; `TestRuntimeBindingAcceptsNewNamespacedOperationWithoutOperationSwitch`; `TestKernelRoutesNewOperationUsingGenericRouteTaskContract`; these prove JSON payload schema admission and exact version selection, not a real media wire/result |
| Versioned feature evaluators, constraint schemas and route eligibility | `TestNewFeatureExtensionNegotiatesTypedConstraintsWithoutKernelBranch`; `TestFeatureEvaluatorsBindFromVersionedExtensionCatalog`; `TestKernelRunsOnlyRouteWhoseRegisteredFeatureEvaluatorAcceptsRequest`; `TestRuntimeRegistryContributesVersionedFeatureEvaluatorsToSharedCatalog` |
| Versioned provider primitive factories, including auth, and exact model-policy strategy resolution | `TestPrimitiveFactoriesWithSameIDBindOnlyTheirExactContractVersion`; `TestVersionedPrimitiveRefsSelectExactAdapterComponents`; `TestAuthPrimitiveVersionsBindTheirOwnFactoriesAndSchemas`; `TestRuntimeRegistryStoresStrategyImplementationsByExactVersion`; `TestExactStrategyRefOptionsReachTopLevelSchedulerPrimitive`; `TestControlPlaneRejectsUnregisteredStrategyVersionInsteadOfFallingBackByID`; `TestValidateBundleResolvesExactStrategyRefsAndOptions`; `TestStrategyStateIsScopedToExactContractRefAcrossReloads`; builtin metadata projection in `TestIPCControlCRUDUsesDaemonServices` |
| Typed quota-source options and multi-window provider reports | `TestManifestBindsTypedQuotaWindowOptionsAndCredentialHeader` proves per-definition options bind to an exact runtime quota-source ref, select credential header presentation, resolve the operation endpoint/transport, and parse the manifest-selected report; `TestHTTPJSONQuotaOptionsExpandNestedWindowsWithoutProviderBranches` covers window maps, percent normalization, reset evidence and explicitly unreported windows; `TestHTTPJSONQuotaSourceComposesMultipleReportsWithPartialSuccess` composes OpenRouter-shaped `/key` and `/credits` fixtures, derives remaining credits from total minus usage, and preserves `/key` windows when `/credits` is unavailable under the explicit best-effort mode; `TestDeepSeekQuotaManifestAggregatesCurrencyBalanceAvailability` covers the observed `is_available` plus any-positive-currency balance rule via manifest data. |
| Provider-specific quota request headers without provider branches | `TestHTTPJSONQuotaSourcePresentsOAuthCredentialWithTypedProviderHeaders` models the observed Anthropic OAuth usage endpoint beta/version headers while credential presentation remains Bearer; `TestQuotaRequestHeadersCannotEmbedCredentialSecrets` rejects authorization/API-key headers in static manifest options. |
| Quota-source auth failures stay visible without poisoning inference health | `TestQuotaHTTPFailuresExposeSafeAuthPermissionAndRateLimitCodes` distinguishes 401/403/429 without exposing response bodies; `TestHTTPJSONQuotaSourceComposesMultipleReportsWithPartialSuccess` reports the failed `/credits` leg while retaining successful `/key` windows; `TestQuotaPollerReportsSanitizedProviderFailureWithoutRouteFeedback` verifies the daemon-facing status hook reports permission failure without writing quota or health state. |
| Billing observations do not become hard routing entitlements | `TestVercelCreditsAreObservableWithoutHardQuotaEnforcement` binds the observed `/credits` response as connection-scoped balance/spend windows with `routingEffect=observe`; `TestObservedZeroBalanceDoesNotActAsRoutingEntitlement` and `TestPolicyGateRetainsObservedBalanceWithoutHardBlocking` prove zero observed balance is retained without excluding a route; `TestEnforcedAttemptEvidenceOverridesAdvisoryQuotaSource` proves real response-header exhaustion still overrides older advisory data; `TestNormalizedRuntimePrimitivesPersistSeparately` proves the effect survives SQLite persistence. |
| Request/response transform extension | `TestRequestTransformRegistryRunsBeforeKernelRequirementsAndProviderEncoding`; `TestRuntimeBindingExecutesSelectedEndpointAndCodecs` |
| Provider decoder → semantic events → client renderer, including commit boundary | `TestRuntimeBindingExecutesSelectedEndpointAndCodecs`; `TestComposedSemanticPipelineDoesNotReplayAcceptedUpstreamResponse`; `TestKernelDoesNotReplayAcceptedResponseAfterSemanticFailure` |
| Semantic Anthropic client egress | `TestOpenAIChatDecoderRendersAnthropicMessagesFromSemanticEvents` exercises OpenAI Chat→canonical events→Anthropic Messages JSON; `TestOpenAIResponsesDecoderPreservesToolIdentityForAnthropicEgress` exercises OpenAI Responses tool identity and argument deltas through Anthropic SSE egress. Renderer tests cover streaming/non-stream text/tools, usage, issuer-signed thinking preservation, and rejection without signatures or when a transform could invalidate them. This is partial SG1 evidence, not the complete reasoning/continuity/loss-policy matrix. |
| Reasoning effort and token budget are separate required compatibility facets | `TestRequiredRequestFacetsSeparateReasoningBudgetFromEffort` checks task normalization; Gemini translates only an explicit positive exact budget in `TestGeminiReasonsAboutBudgetSeparatelyFromEffort`; OpenAI Chat and Responses preserve only matching native raw budget evidence, admit only explicitly mapped transformed reasoning intents, and reject unsupported transformed modes in `TestOpenAIChatBudgetRequiresExactNativeSourceEvidence`, `TestNativeResponsesBudgetRequiresExactUnmodifiedSourceEvidence`, `TestNativeChatReasoningMutationRequiresDeclaredModeMapping` and `TestNativeResponsesReasoningMutationRequiresDeclaredModeMapping`; Anthropic records the named `anthropic.reasoning-budget-raises-max-output` loss and admits it only with a model policy grant in `TestReasoningBudgetRaisesAnthropicOutputLimitOnlyWithNamedLossApproval`. |
| Stored provider continuity participates in route admission | `TestKernelLoadsAndSavesProviderSessionContinuity` verifies the provider-scoped previous response is loaded before adapter compatibility planning, the exact operation ref reaches the adapter, and the ref is derived from the selected route binding; `TestMemorySessionStoreExpiresStateAndCopiesOpaqueData` proves memory state cannot cross model, operation, operation-version or provider-definition-version boundaries and preserves expiry; `TestCachePersistsSessionStateOffTheReadPath` proves durable state remains operation-isolated across restart and both cache paths preserve shorter provider expiry while capping longer expiry at their TTL; `TestKernelFailsClosedWhenSessionStateCannotBeLoaded` prevents storage errors from silently degrading continuation into a fresh request and hides backend details. |
| Anthropic client request → OpenAI Chat request codec | `TestAnthropicMessagesNormalizeToTypedConversationToolsAndOptions` verifies system, multimodal content, tool-use/result history, tool choice and common generation options become typed IR; `TestAnthropicMessagesRequestMapsToOpenAIChatFromTypedIR` verifies the rebuilt OpenAI wire request and admission plan; `TestAnthropicThinkingIntentIsRejectedByOpenAIChatCompatibilityBeforeEncoding` and `TestKernelExcludesOpenAIChatRouteForAnthropicThinkingBeforeDispatch` prove unsupported thinking budget is rejected before upstream dispatch; `TestAnthropicOpaqueThinkingAndToolResultErrorsBecomeExplicitFacets` checks fail-closed history semantics. This is partial SG1 evidence; continuity/signature translation and named loss policy remain open. |
| Anthropic client request → OpenAI Responses request codec | `TestAnthropicAdaptiveEffortBecomesTypedReasoningIntent` and `TestAnthropicMalformedThinkingIntentIsExplicitlyUnsupported` verify protocol-specific reasoning is bound into typed intent or retained as unsupported; `TestAnthropicMessagesRequestMapsToOpenAIResponsesFromTypedIR` verifies system/image/tool-history/tool-choice/generation encoding and the complete request plan; `TestAnthropicThinkingBudgetIsRejectedByResponsesBeforeEncoding` and `TestKernelExcludesOpenAIResponsesRouteForAnthropicThinkingBudgetBeforeDispatch` prove non-equivalent thinking budgets are rejected before dispatch; `TestAnthropicEffortRouteRequiresSignedThinkingEgress` proves request-side effort encoding alone does not admit a route whose renderer cannot preserve required Anthropic signatures. This is partial SG1 evidence; signed thinking egress, continuity and named-loss policy remain open. |
| Operation event and projected-result schemas are enforced at the kernel response-event boundary | `TestKernelEnforcesExactOperationEventSchemaAtAdapterBoundary` rejects a tool-call event that violates the operation's exact event schema; `TestKernelValidatesProjectedOperationResultAtCompleteEvent` rejects a malformed bounded result before terminal completion is accepted |
| Versioned manifest options configure a reusable streaming multipart request codec | `TestRuntimeBindingBindsManifestConfiguredMultipartCodecOptions` binds typed role/field options from the provider definition; `TestMultipartRequestCodecStreamsConfiguredArtifactFields` proves binary bytes are streamed into the mapped form part; `TestManifestMultipartCodecFramesBinaryBodyThroughKernelAndTypedResult` composes the exact manifest binding, kernel artifact lease, multipart HTTP transport frame, provider JSON response decoder and typed result projector; fail-closed mapping and stream-mode checks are in `TestMultipartRequestCodecFailsClosedForUnmappedArtifactsAndStreamMode`; exact artifact-role compatibility declarations are in `TestMultipartRequestCodecDeclaresExactArtifactRoleCompatibility` |
| New quota/error envelope and restart-persistent routing effect | `TestDaemonPersistsManifestClassifiedQuotaEvidenceAcrossRestart` binds the observed Anthropic 5h `Status=rejected`/`Reset` header pair, persists that window independently from JSON-body quota and standard rate-limit windows, restores it after daemon restart, proves that only the configured exhausted header window blocks an otherwise non-exhausted body quota, and verifies automatic eligibility after its reset deadline. |
| Compatibility facet composition and fail-closed route admission | `TestCompatibilityPlanComposesFacetMappingsInStageOrder`; `TestCompatibilityPlanFailsClosedForRequiredUnknownOrUnsupportedFacet`; `TestCompatibilityPlanRequiresNamedAndGrantedLosses`; `TestCompatibilityPlanIgnoresUnrequestedUnsupportedFacet`; `TestCompatibilityPlanRejectsUnscopedDegradationAndInvalidDeclarations`; `TestKernelRejectsAdapterWithoutRequiredFacetDeclarationBeforeDispatch` |
| Model-path/server loss ceilings and usage evidence | `TestModelLossPoliciesRoundTripThroughConfigBundle` proves Physical/Combo and server ceiling portability in bundle v9; `TestDiffBundleDetectsServerLossCeilingChanges` proves ceiling updates appear in dry-run; `TestLoaderBuildsTypedPhysicalAndComboGraph` proves both policies reach the immutable graph; `TestNodeLossPoliciesFlowDownAndPermittedLossIsRecordedInUsage` proves model grants are constrained by the server allowlist, denials dominate, and requested/effective values, semantic path, granting model source, fidelity and loss ID appear in usage; `TestIPCControlCRUDUsesDaemonServices` exercises the CLI backing operation. |
| Compatibility plan composition, persistence, request preflight and explanations | `TestTransformRegistriesProjectScopedCompatibilitySteps` proves exact active request/response transform refs, scope, order and declared effects are compiled; `TestRequestTransformRegistryRunsBeforeKernelRequirementsAndProviderEncoding` proves the kernel passes the post-transform invocation and chain into admission; `TestKernelGrantsMultipartBodyOnlyToDeclaredOperationRecipient` proves the kernel supplies only the operation-authorized artifact transfer; `TestComposedCompatibilityPlanRetainsTransformAndArtifactStages` verifies those chains/transfers survive into the composed plan; `TestNodeLossPoliciesFlowDownAndPermittedLossIsRecordedInUsage` verifies the selected candidate plan is attached to its completion event; `TestNormalizedRuntimePrimitivesPersistSeparately` proves the payload-free summary round-trips through SQLite with usage; `TestExplainCompatibilityUsesModelPathAndDoesNotDispatch` and `TestRouteExplainIPCPreflightsRequestCompatibility` prove request-specific admission supports exact non-chat operation refs/payloads, uses the model path and does not dispatch or leak prompt content. `usage.list`, the TUI Usage inspector and `gobroom route-explain --request-file` expose the selected plan. This is partial C2 evidence; transform fallback accounting and complete SG1/SG4 matrices remain open. |
| Portable provider dependencies and side-by-side definition contracts | `TestBundleEmbedsOnlySecretFreeProviderDefinitionsAndPersistsThem` proves safe typed manifests travel in bundle v9 and load after restart while opaque auth options stay external; `TestBundlePreservesSideBySideProviderDefinitionVersions` round-trips v1/v2 nodes with the same ID and exact embedded definitions; `TestProviderDefinitionVersionsBindSideBySideByExactRef` proves runtime binding isolation; `TestBundleRejectsProviderDefinitionPayloadDifferentFromPinnedDescriptor` binds payload to the exact catalog descriptor; `TestBundleExportsUnusedUnresolvedProviderWithoutInventingBehavior` retains a missing definition as unresolved only if no model route or Physical source uses it; lock v2 pins exact statically linked module build and host-contract refs for the reachable extension closure, and runtime composition rejects unsupported host-contract versions; `TestConfigApplyPersistsPortableProviderDefinitionAndRequiresRestart` verifies restart activation for a newly imported definition. |
| Connection-scoped route assignment and evidence separation | `TestCustomRouteExpandsOnlyToExplicitlyAssignedConnections` proves a custom upstream ID becomes a route only for its assigned enabled account; `TestStaticCatalogAssignmentRoutesWithoutCreatingEntitlementEvidence` proves registry candidates create explicit assignments, never endpoint entitlement, and do not leak to another account; `TestStaticCatalogAssignmentsRoundTripWithoutEntitlementEvidence` proves provenance survives bundle v9; `TestCustomRouteAssignmentsMigrateToTypedModelAssignments` verifies the table migration preserves user assignments; `TestStaticCatalogImportPreselectsOnlyExistingConnectionAssignments` verifies TUI candidates are not preselected as account evidence. |
| Request-codec facet declaration and candidate preflight | `TestGeminiCompatibilityPlanRejectsUnsupportedRequestFacetsBeforeDispatch`; `TestKernelRejectsGeminiContinuityBeforePreparingUpstreamRequest`; OpenAI Chat, OpenAI Responses and Anthropic Messages now publish request-facet declarations through the same interface |
| Response decoder → event IR → renderer contract and passthrough boundary | `TestComposedAdapterRejectsRendererEventMissingFromDecoderContract`; `TestOpenAIChatRendererPlansRequiredSemanticEvents`; `TestActiveSemanticTransformDisablesOpenAIWirePassthrough`; `TestWirePassthroughRejectsSemanticResponseTransform` |
| Operation replay safety at dispatch/effect boundary | `TestReplaySafetyAllowsPostDispatchReplayOnlyForConfirmedRejection`; `TestKernelDoesNotFallbackAfterAmbiguousDispatchedRequest`; explicit 429 fallback remains covered by Gemini quota/rate-limit integration fixtures |
| Safe request-level logging and headless log retrieval | `TestDataPlaneAccessLogRecordsSafeRequestSummaryAndPreservesStreaming`; `TestLogsListReturnsRecentStructuredRecordsWithBoundedLimit`; `TestRootHelpAndTypedCommandsDoNotRequireSeparateTUIBinary` checks the `logs` command |
| Versioned scoped transform binding, persistence, snapshot and branch isolation | `TestRegisteringTransformDoesNotActivateItWithoutEnabledBinding`; `TestResponseTransformRegistrationNeedsAnEnabledMatchingScope`; `TestModelScopedTransformUsesBranchLocalInputBeforeFallback`; `TestConnectionScopedTransformIsCandidateLocal`; `TestCloneRequestIsolatesNestedTransformMutation`; `TestTransformBindingsRoundTripThroughTypedConfigBundle`; `TestDiffBundleDetectsTransformBindingUpdates`; daemon-scoped response binding is applied through the semantic response pipeline fixture |
| Transform implementation, schema and exact-ref binding from the shared catalog; mutation-effect rollback | `TestTransformOptionsUseVersionedSharedExtensionSchema`; `TestRequestTransformRejectsUndeclaredMutationTransactionally` |

The provider definition catalog contains only display/operation metadata,
primitive references, capabilities and the auth flow's declared setup schema.
It excludes auth options, connection secrets, endpoint options and arbitrary
provider defaults. Both IPC and the optional HTTP control API serve this same
projection; `gobroom providers catalog` exposes it for headless clients.

The current primitive catalog additionally contains exact versioned endpoint,
transport, codec, auth, discovery, usage, classifier and client-renderer
descriptors. Same-ID implementation versions bind independently; reachable
statically linked module build/host-contract refs are included in dependency
lock v2. Built-in endpoint/usage/error/OAuth option schemas and compiled
fingerprints are available from IPC `extensions.catalog`, HTTP control
`/api/extensions` and CLI `gobroom extensions catalog`. Operations, feature
evaluators, strategies and transforms also register through the catalog.

The implementation adds no provider-name branch to the kernel and no
provider-specific persistence field. The existing physical/combo graph,
snapshot lifecycle and request executor are reused by these simulations.
These checks establish the composition subset. They do not prove complete
semantic closure, provider parity, TUI onboarding or drop-in replacement.

The compatibility planner evaluates immutable declarations in request/route/
operation context and recomputes admission from facet mappings. It composes
ordered mappings, requires named losses and explicit grants, gives denials
precedence, and rejects undeclared required facets before dispatch. Production
request codecs now report the request facets they preserve, translate or
reject; a Gemini end-to-end fixture proves continuity rejection happens before
request preparation. Response decoders now enumerate possible canonical event
families, renderers negotiate request-required families, and missing decoder or
renderer declarations fail closed. A registered semantic response transform
forces the OpenAI Chat renderer's semantic path and makes raw-only passthrough
ineligible. Operation/artifact-specific event schemas, transform effect reports,
artifact transfer and node-bound loss policy remain unimplemented. Therefore
this slice is not SG1 evidence.

The attempt executor now consumes the operation's replay-safety declaration.
An ambiguous transport failure is treated as an unknown upstream effect; a
successful upstream response followed by a pre-client-commit decoder failure
is still an accepted upstream effect. Neither case triggers a second provider
attempt under the current contracts. Only an explicitly classified rejection
can satisfy `confirmed_rejection_only`; 5xx, timeout, network failure and a
successful response are not treated as rejection. Scoped idempotency replay
now requires an explicit minimum retention window as well as a valid header
and client key. An external async-job lifecycle now executes through the real
daemon; provider-specific guarantees and broader commit/cancellation stress remain open,
so SG6 is still pending.

`TestReplayWithKeyExpiresBeforeAnotherDispatch` uses Go's virtual-time
`testing/synctest` to cover expiry at a slow response, transport failure,
second preparation and credential refresh. Each permits only the original
dispatch and no cross-issuer fallback. `TestIdempotencyPolicyRequiresExplicitBoundedRetention`
covers missing/zero/overflowing bounds and credential/transport headers;
`TestProviderOperationBindsOnlyValidIdempotencyPolicies` checks manifest JSON,
registration and runtime binding; the typed loader fixture preserves both
header and retention in an immutable route. `TestReplayWithKeyCancellationDuringPreparePreventsDispatch`
checks cancellation during first or retry preparation cannot dispatch even
when a preparation hook returns success. Scoped replay now uses a prepared
factory rather than invoking the semantic codec again.

`TestPreparedBytesReplayFreezesPayloadAndRenewsBodyBoundAuthorization` mutates
caller bytes/header maps after preparation and verifies a replay signs and
sends the original bytes with a new credential. `TestReplayWithKeyRequiresPreparedFactoryAndStableAction`
rejects missing factories and changed method/endpoint/framing before dispatch;
the ordinary issuer replay test asserts exactly one semantic preparation.
`TestMultipartRequestCodecStreamsConfiguredArtifactFields` checks byte-identical
multipart framing, renewed credentials, released-lease rejection and refusal
to grant replay to a non-replayable body. Its kernel-level companion
`TestManifestMultipartCodecFramesBinaryBodyThroughKernelAndTypedResult` now
runs both ordinary and ambiguous-replay cases: the fake upstream consumes the
first multipart request before a transport failure and checks the second has
identical bytes and key before returning a schema-validated transcript.
`TestExternalRequestCodecEncodesThenSignsBodyWithoutKernelChanges` also replays
the external COSY codec through the resolved fake HTTP endpoint, checking
encoded-body identity and renewed signature/auth.

`TestExternalJobLifecycleKeepsEffectsAndHandlesScopedAcrossRestart` runs an
independent `JobModule` through manifest loading, ordinary control IPC, an
assigned model, Physical/Combo publication, module-owned HTTP ingress, shared
prepared/HTTP primitives, typed result projection and a module-owned renderer.
The fake upstream loses the first create response but creates only one job
for its two keyed requests. Its inline handle remains connection/exact-issuer
scoped after response cleanup and daemon restart. Wrong role, expiry and
wrong/missing issuer are rejected before upstream access. An unsafe cancel's
lost response results in one cancel only; explicit status reads reconcile the
cancelled state. A second job traverses pending → running → completed. No
background job polling occurs. Operation health now keeps cancel cooling down
across restart while status reconciles immediately; status success cannot erase
cancel's evidence. A configured HTTP 429 produces operation-qualified quota
windows that survive another restart. The exhausted account is not called
again; an independently eligible account may still be selected. The existing
job's status remains usable. Set `GOBROOM_TEST_CLI` to an installed CLI path to
additionally verify status/catalog/health/quota projection through that binary.

Installed acceptance for operation feedback (`9492f6d`): after
`make install-all`, run
`GOBROOM_TEST_CLI=/absolute/path/to/gobroom GOWORK=off go test ./... -run '^TestExternalJobLifecycleKeepsEffectsAndHandlesScopedAcrossRestart$' -count=1`
inside `examples/external-provider-module`. This passed with CLI status,
extension metadata, operation health and quota evidence through real Unix IPC.
The main service remained `ok`; installed CLI config export → validate passed.

The `TestOperation*` runtime/store/daemon groups cover exact-version and
credential-generation fences, action-local failure and success, half-open
single-flight, recovery of an actually acquired expired shared trial, broad
account auth failure, typed quota identities, time/authority merge with partial
fields, reset/reopen, queue coalescing and durable spool reconstruction.
`TestWorkloadClassIncludesExactOperationWithoutAStringDSL` and operation ranking/
affinity tests prevent status telemetry or session stickiness from biasing an
unrelated action/version. TUI evidence includes independent operation health
details and collision-free selection identities for operation quota windows.

`TestInlineResponseDisclosurePreservesConnectionReplayScope` covers exact
recipient/connection/issuer, TTL, schema, byte/count bounds, cloned value bytes,
closed-scope rejection and handle survival after response cleanup.
`TestJSONOperationIngressBindsInlineArtifactsAndRejectsBodyLeases` proves the
shared typed JSON ingress accepts inline handles but not private spool IDs.

The issuer replay fixture also checks the authorizer saw the actual derived
idempotency header before signing. `TestHTTPTransportDoesNotRedirectPreparedActions`
uses two real loopback servers to prevent hidden cross-issuer redirect;
`TestHTTPTransportDoesNotImplicitlyReplayAfterAmbiguousEmptyBodyDispatch`
warms a persistent connection, consumes a keyed empty POST, then loses its
response. The shared transport returns the ambiguous error without opening a
second connection or issuing a second request outside kernel replay policy.

## Public transform binding lifecycle evidence

`TestExternalAudioRequestTransformBindingLifecycle` and
`TestExternalContentTransformBindingLifecycle` compile in a separate module
using only public provider/daemon APIs. The SDK exports the existing transform
contracts directly; no second transform framework or kernel execution path is
introduced. CLI binding commands use Cobra and standard typed JSON decoding,
with the existing daemon IPC validation and persistence path.

Audio evidence covers inactive registration, schema-invalid option rejection
without persistence, daemon/model ordering, option updates and restart,
post-transform operation schema rejection before upstream, disable and delete.
Content evidence covers canonical message append exactly once, option update,
protected opaque provenance rejection before upstream, restart and removal.
`GOBROOM_TEST_CLI` exercises installed binding CRUD in the content fixture.
`TestExternalResponseTransformBindingLifecycle` adds actual JSON/SSE response
projection, schema-invalid replacement without changing active options,
restart and disable/delete. With a binding, client completion tokens change
from 3 to 103; all 14 original provider usage rows remain 3 after draining and
restart. The SSE assertion requires a real final usage chunk, not just absence
of incorrect counters. It failed when semantic rendering dropped opted-in
usage; typed client transport hints now preserve that preference through the
compositor. Independent renderer tests cover opt-out, unavailable usage,
failed Finish and byte-exact passthrough. The installed CLI switch also drives
response binding CRUD.
These fixtures establish configured input/content/response lifecycle. Combine
them with the finite SG5 core checklist below; they do not authorize artifact
ownership changes or prove every module's transform algorithm.

Installed checkpoint `a10c29e`: regression/vet/full conformance and three focused
CLI/external-module race runs passed. `make install-all` completed; the two
external lifecycle fixtures then passed three installed-CLI race runs. Main
control=serving=492 and bundle-v9 validation were healthy. All provider traffic
was to temporary fake upstreams, not live accounts.

## Full semantic closure gate

Recovery-trigger evidence: `TestCredentialRecoveryRequiresAuthClassificationAndRoutePolicy`
composes normalized cause/action with explicit mode policy; unknown hints and
invalid route policies fail closed, and an unannotated HTTP status cannot supply
a recovery hint. Accepted effects and unsafe replay are vetoed, while a bounded
same-issuer prepared key permits ambiguous-effect recovery. HTTP/JSON fixtures
cover default 401, 403 permission, structured custom-status matching, quota
precedence, success veto, strict option schema and immutable options. Binding
fixtures carry policy through manifests, safe metadata and control-plane routes.
The separate public-module opaque-state fixture normalizes HTTP498 and persists
its replacement, rejects 403 permission/quota and mode-disabled 401 without new
activation records, then consumes the recovered state after restart. This is
host-contract-v3 recovery conformance, not principal-scoped quota continuity or
full C4/SG2/M15 closure.

Installed recovery acceptance (`565279b`): after `make install-all`, run
`GOBROOM_TEST_CLI=/absolute/path/to/gobroom GOWORK=off go test ./... -run '^TestExternalOpaqueCredentialStateDeviceSetupPersistsAndStaysPrivate$' -count=1`
inside `examples/external-provider-module`. It passed typed HTTP498 recovery,
permission/quota and disabled-policy vetoes, CLI setup/get/activation/status and
post-restart inference. The main service remained `ok` with matching control/
serving versions; installed config export → validate passed. No live provider
is called, and principal/quota continuity remains a separate architecture gate.

Activation evidence: credential CAS and its secret-free outbox commit atomically;
store fixtures cover rollback, restart, deadline ordering, evidence requirements
for `active`, superseding and conditional reset/progress writes. Root daemon
fixtures inject reset, version-persistence and serving-hook errors before/after
publication: code/device stay committed/completed with explicit activation
progress and retry without re-exchanging tokens. Worker exhaustion and shutdown
drain are exercised. Typed control/API receipts preserve actual publication
outcomes; `TestModelsProjectionFollowsActualServingPublication` keeps discovery
on the serving snapshot across a rejected publication. CLI command and TUI
readiness fixtures cover active/pending/exhausted and missing evidence. The
public-module `TestExternalCredentialRefreshActivationRetryAndRestartRecovery`
executes real HTTP 401 → refresh → successful response while activation cannot
persist its version, then repairs the durable job through retry or restart.
This closes the identified refresh/code/device activation slice, not universal
auth actions, leases, full fault composition or live-provider acceptance.

Installed acceptance (`a09102b`): after `make install-all`, run
`GOBROOM_TEST_CLI=/absolute/path/to/gobroom GOWORK=off go test ./... -run '^TestExternalCredentialRefreshActivationRetryAndRestartRecovery$' -count=1`
inside `examples/external-provider-module`. It passed activation list/get/retry,
published-but-unpersisted degraded status, explicit retry and pending-job restart
recovery through the installed client. The main service reported equal control/
serving versions and `ok`; config export → validate passed. No live provider
is called, and complete C4/SG2/M15 remains pending.

Auth-owner rebinding evidence: `TestCredentialOwnerProjectionAndCAS` covers
typed node/definition projection, privacy, forged-owner rejection and current-owner
CAS. Store fixtures exercise raw-SQL and repository definition/version ABA,
unrelated nodes, rename/no-op preservation and transaction rollback.
`TestCredentialOwnerRejectsPinnedRoutesAfterDefinitionRebind` and incorrect-binding
fixtures reject before driver invocation; in-flight refresh and code/device
completion fixtures reject old revision/owner commits. The independent-module
`TestPublicDaemonRejectsDeviceAuthorizationAfterDefinitionOwnerChanges` performs
version rebind and v1 → v2 → v1 through the daemon, rejects the old device token,
then completes a new current-owner setup and authenticates an operation with it.
Serving and both quota paths share that same owner-checked resolver. This is the
identified owner-rebind slice, not universal auth-driver/lease lifecycle proof.
`TestDiscoveryCredentialBindingRequiresExactOwnerAndMode` covers the pure guard
used before model-discovery and connection-probe auth resolution; their existing
vertical fixtures continue exercising the valid-owner paths.

Installed acceptance (`e402a0f`): after `make install-all`, run
`GOBROOM_TEST_CLI=/absolute/path/to/gobroom GOWORK=off go test ./... -run '^TestPublicDaemonRejectsDeviceAuthorizationAfterDefinitionOwnerChanges$' -count=1`
inside `examples/external-provider-module`. Both v1 → v2 and v1 → v2 → v1
passed provider updates, rejected old completion, retained-credential probe,
fresh device setup and replacement-credential probe through the installed CLI.
The main service remained `ok`; installed config export → validate passed.
No live provider is called, and post-commit activation remains a separate gate.

Auth-state isolation evidence: `TestExternalOpaqueCredentialStateDeviceSetupPersistsAndStaysPrivate`
uses a provider-defined `opaque_ticket` mode with typed ticket/subject/workspace
metadata. Successful device setup and a real HTTP-401 refresh persist its exact
state; a restarted daemon resolves the refreshed state and completes another
chat request. IPC and configuration exports omit state. `TestCredentialStateBoundsAndRedacts`
checks bounded construction, exact bytes and redacted formatting/JSON;
`TestRefreshCredentialRejectsInvalidProviderStateBeforeCAS`, replacement Resolve
failure/cancellation and malformed runtime-context fixtures leave the old revision
untouched. `TestUnauthorizedRefreshRetriesWhenRuntimeContextChanges` proves
same-secret generation/header/context changes reach the permitted same-issuer
retry. These are public extension/daemon/kernel fixtures, not universal auth
setup actions, cross-provider credential leases or full SG2 closure.

Installed acceptance (`30a066e`): after `make install-all`, run
`GOBROOM_TEST_CLI=/absolute/path/to/gobroom GOWORK=off go test ./... -run '^TestExternalOpaqueCredentialStateDeviceSetupPersistsAndStaysPrivate$' -count=1`
inside `examples/external-provider-module`. Device-start/get and post-restart
inference passed through the installed CLI, alongside the fixture's real HTTP
401-refresh/restart path and private config/IPC assertions. Main daemon status
remained `ok`; installed config export → validate passed. The provider is fake
and definition-fixed; this does not cover auth-owner rebinding or every driver.

Response-terminal accounting slice: the typed origin/first-failure latch is
covered by `TestComposedAdapterResponseFailurePreservesTransformOriginThroughDecoderWrap`,
`TestComposedAdapterLatchesIgnoredTransformCallbackFailure`, renderer/provider
fault tests and caller-cancellation tests. Kernel fixtures exercise cancellation
before/after client bytes, ignored cancellation with a false completion callback,
typed local versus provider failure, exactly-one feedback and admission release.
Standard `testing/synctest` distinguishes a neutral caller deadline from an
operation-budget timeout during dispatch or accepted provider response work.

`TestProviderUsageSurvivesTransformFaultWithoutSuccessObservation`,
`TestProviderUsageSurvivesCancellationAfterAdapterCompletion` and
`TestProviderCompletionIsStagedUntilTerminalReturn` retain actual provider
token/cost counters without publishing success or starting another route.
Happy completion still emits once; unreported usage is not fabricated. The
response-artifact fixture now has a false-completion/missing-required-artifact
case proving late validation cannot leak success. Client write/flush errors
latch through the standard ResponseController wrapper chain, and writes after
cancellation stop. The real eight-request HTTP disconnect fixture now asserts
no provider-health or success/performance observation after disconnect.

These cases close demonstrated attribution/terminal holes; they are not the
full SG1–SG7 matrix or evidence of live-provider/harness parity.

Checkpoint `8c83070`: full tests/vet, the focused response/kernel/API race group
(`-count=3`) and `make test-conformance` passed. After `make install-all`, the
installed CLI external-job control/HTTP acceptance and main config export →
validate passed; the main daemon remained `ok`. Cancellation/flush/fault-origin
details are proved by the targeted deterministic/real-loopback fixtures above,
not by assuming a successful status command covers them.

The following cases are required in addition to the existing subset. They use
deterministic upstream/callback fixtures; live credentials are not required.
Each assertion checks observable behavior through the ordinary control and
serving boundaries, not merely registry acceptance.

| Case | Required proof |
|---|---|
| IPC request cancellation and drain | `TestCallIPCCancellationReachesHandler` verifies client cancellation returns `context.Canceled`, cancels the matching handler and leaves IPC usable; `TestIPCCloseDrainsHandlersBeforeReleasingOwnership` holds a handler after cancellation and verifies concurrent Close/restart/ownership ordering and suppression of a success reply after cancellation; `TestIPCCloseUnblocksPartialRequestBody` covers blocked body reads; `TestIPCRejectsInvalidOrOversizedEnvelopeBeforeDispatch` guards framing/bounds; `TestDaemonStopDrainsIPCBeforeClosingStore` verifies store lifetime; `TestDaemonIPCShutdownAcknowledgesAndWaitReturns` checks acknowledged shutdown, Wait completion and ownership release; `TestDaemonWaitPreservesCleanupError` retains a failed ownership cleanup without deleting a replacement path. |
| HTTP listener binding and startup rollback | `TestDaemonHTTPBindFailureDoesNotPublishOrInitialize` proves an occupied data port fails before DB/IPC initialization; `TestDaemonControlBindFailureRollsBackDataPort` proves a later control bind releases the data reservation; `TestDaemonIPCFailureRollsBackHTTPAndAllowsRetry` proves IPC acquisition failure releases the HTTP reservations and permits retry; `TestDaemonInitializationFailureReleasesReservedSockets` verifies module initialization failure releases both IPC ownership and HTTP ports. `TestDaemonPublishesBoundListenersAndReleasesThemOnStop` verifies immediate HTTP/IPC usability, data/control separation, actual port-zero addresses, duplicate-Start rejection on the same daemon instance and port release after Stop. The external-module HTTP fixture uses `Addresses()` directly without reserving/closing ports or readiness polling. |
| Unix IPC ownership and crash recovery | `TestIPCSocketOwnershipSurvivesContentionAndOwnerCrash` runs an owner subprocess, rejects a contender while keeping the original socket usable, kills the owner without cleanup and verifies successor recovery. `TestIPCServerPreservesNonSocketPaths` protects files, symlinks and directories; `TestIPCServerPreservesActiveUnmanagedSocket` protects listeners without a Gobroom lock; `TestIPCServerClosePreservesReplacementPath` checks identity-aware cleanup; `TestIPCServerCancellationReleasesOwnership` checks context cancellation/restart. `TestDaemonIPCConflictDoesNotInitializeAnotherDatabase` proves acquisition precedes database open, and `TestDaemonRetainsIPCOwnershipUntilCleanupCompletes` keeps a successor excluded while workers/store are draining. |
| SG1: full compatibility composition | A request with tools, reasoning budget and scoped continuity is evaluated across encoder, decoder, renderer and transforms. Exact intent excludes incompatible candidates before dispatch; permitted clamping records a named loss. Undeclared support fails closed. Both OpenAI and Anthropic client rendering are exercised from semantic events. `TestKernelRunsAnthropicMessagesThroughOpenAIChatAndBack` composes Anthropic `/v1/messages` normalization, tool-capable route eligibility, OpenAI Chat encoding, provider response decoding and Anthropic message rendering through a real kernel execution; it checks model remapping, tool identity/arguments and provider usage. `TestKernelMapsOpenAIChatRequestThroughResponsesAndBack` proves OpenAI Chat tool history, schemas, choice, generation options and reasoning effort become the Responses request contract, then maps Responses JSON text/tools/usage back through the kernel. `TestKernelMapsResponsesIngressThroughOpenAIChatAndBack` and `TestKernelStreamsResponsesIngressThroughOpenAIChatAndBack` cover JSON and SSE paths through Chat; `TestKernelMapsOpenAIResponsesThroughAnthropicMessagesAndBack` and `TestGeminiRuntimeBindingMapsOpenAIResponsesThroughGeminiAndBack` prove the same Responses ingress/output contract through Anthropic and Gemini provider codecs. `TestKernelRestoresResponsesContinuityFromClientSession` proves native Responses provider response IDs persist and restore on the next session-scoped request when the client omits `previous_response_id`. Typed prompt/image detail, tools, choice, generation and reasoning are mapped; unsupported cross-provider continuity, unknown provider options and unsupported content are rejected before dispatch. The optional `CompatibilityPlanPreparer` contract carries the admitted candidate-specific plan to a codec only when wire preparation needs approved-loss details; `TestKernelAppliesOnlyApprovedAnthropicReasoningBudgetLossToWire` proves denial prevents dispatch and an approved 512→3072 Anthropic reasoning-budget adjustment is applied to the actual upstream JSON and recorded in usage. Gemini tool signatures echoed through OpenAI Chat are accepted only when the exact signature digest is attested by the selected route's provider/connection/model/session state; the integration test proves missing proof and wrong-issuer proof are rejected before dispatch, then a provider-emitted signature is persisted and accepted on the same-session follow-up. Durable session caching preserves provider-requested expiry and caps longer state at its cache TTL in `TestCachePersistsSessionStateOffTheReadPath`. Remaining SG1 cases include provider-specific continuity reset semantics and end-to-end provider expiry behavior, plus cross-codec policy/evidence cases. |
| SG2: interactive auth lifecycle | Device authorization displays code/URL, polls, honors slow-down and completes without a frontend. Authorization-code fixtures exercise state/PKCE, callback replay rejection, cancellation/expiry and concurrent connection isolation. Credential application includes a request-bound signing fixture. Atomic setup/refresh generation checks preserve newer credentials. |
| SG3: operation/artifact semantics | A non-chat operation consumes a genuine multipart/binary input and emits its typed result plus registered semantic events. Its compiler derives requirements from that input. Required unknown events, expired artifacts and wrong-issuer signatures are rejected; portable artifacts survive an allowed transfer. `TestManifestMultipartCodecFramesBinaryBodyThroughKernelAndTypedResult` covers manifest-bound media framing through HTTP transport, operation event/result projection and client response. `TestOperationArtifactOutputsAreTypedBoundedAndRecipientScoped` proves exact output type/role/count and recipient contract; `TestOperationCompilesRequirementsFromTypedArtifactMetadata` covers metadata-only compilation; `TestKernelGrantsMultipartBodyOnlyToDeclaredOperationRecipient` proves a registered bounded interpreter reads the real spool lease, derives a route feature requirement from PDF magic bytes, and caches facts outside the provider-visible request. It also covers request-scope/portable transfer and rejects expired input before inspection/planning/dispatch. `TestExternalModuleInterpretsBoundedArtifactAndCompilesRequirement` proves a separate public-API module can register and bind its interpreter without kernel changes. `TestResponseArtifactScopeStreamsAuthorizesAndReleasesLease`, `TestResponseArtifactScopeRejectsUnauthorizedRecipientWithoutSpooling`, `TestResponseArtifactScopeEnforcesAggregateOperationOutputBound` and `TestKernelTransfersProviderResponseArtifactThroughAuthorizedRendererLease` cover response spooling, transfer and lease cleanup. `TestGeminiRuntimeBindingMapsOpenAIResponsesThroughGeminiAndBack` proves Responses base64 image input becomes Gemini `inlineData` through the kernel. Signed response issuer provenance is checked by `TestComposedAdapterBindsSignatureEventToProviderIssuer`, `TestKernelRejectsResponseSignatureFromWrongIssuer` and `TestResponseTransformCannotRewriteSignatureIssuer`. Still missing: other provider-native multimodal JSON/media envelopes and real service framing fixtures. |
| SG4: native wire versus transforms | `TestKernelPreservesNativeChatSSEWithoutTransforms` and `TestKernelPreservesNativeResponsesJSONWithoutTransforms` prove exact Chat SSE and Responses JSON bytes survive ordinary kernel routes when wire/framing and streaming agree. `TestKernelMapsOpenAIChatRequestThroughResponsesAndBack` composes a translated Chat request, Responses JSON output items, and Chat response projection through the kernel; `TestKernelMapsResponsesIngressThroughOpenAIChatAndBack` and `TestKernelStreamsResponsesIngressThroughOpenAIChatAndBack` exercise both JSON and SSE cross-wire directions through the kernel, including Responses event framing and client-scoped response IDs. `TestKernelRendersResponsesJSONItemsAsAnthropicToolUseBlocks` composes a non-stream Responses JSON result through the kernel decoder into Anthropic text, independent tool-use blocks and usage; `TestResponsesJSONSemanticDecoderRejectsIncompleteStatus` prevents incomplete Responses envelopes from becoming successful semantic output. `OpenAIResponses` is now a semantic renderer as well as native passthrough; `TestOpenAIResponsesRendererPreservesExactNativeWire`, `TestOpenAIResponsesRendererProjectsSemanticJSON`, `TestOpenAIResponsesRendererStreamsItemLifecycleAndFinalEnvelope` and `TestOpenAIResponsesRendererDoesNotCompleteInterruptedStream` cover its renderer contract. `TestOpenAIChatRendererSeparatesResponsesItemsAndFinishesOnce` proves stable Responses item IDs become distinct Chat tool indexes, per-item completion is not mistaken for overall finish, and one terminal frame is emitted; `TestOpenAIChatRendererRejectsIncompleteOrDuplicateToolCalls` verifies malformed identities do not receive `[DONE]`. `TestActiveSemanticTransformDisablesOpenAIWirePassthrough` and `TestWirePassthroughRejectsSemanticResponseTransform` prove transforms select semantic rendering or reject incompatible raw passthrough; `TestChatTransformRegistryToWireEncodesCanonicalMessageSuffix` and `TestOpenAIChatRendererReencodesTransformedToolCallWithStableCorrelation` cover typed re-encoding and stable tool-call identity. `TestKernelRejectsMissingClientRendererBeforeDispatch` proves a route without the requested Anthropic client renderer is excluded before provider dispatch. `TestKernelStreamsInterleavedToolLifecycleToAnthropicClient` covers sparse stable provider tool indices, metadata split across deltas and argument fragments arriving before tool name through the kernel stream. `TestKernelRejectsIncompleteToolIdentityWithoutSuccessfulStreamEnd` and `TestAnthropicMessagesRendererRejectsIncompleteOrDuplicateToolIdentity` prove incomplete identities fail closed without a success terminal, and duplicate call IDs cannot bind multiple response blocks. `TestKernelInterruptedCrossProtocolStreamStaysIncompleteAndDoesNotFallback` proves partial OpenAI SSE translated to Anthropic remains visibly incomplete on truncation and the kernel does not replace the accepted stream with a fallback. Remaining: Responses ingress through additional provider codec families, other stream/JSON cross-format pairs and additional tool lifecycle variants. |
| SG5: configured transform lifecycle | Catalog registration alone leaves a transform inactive. Bindings/options/failure mode survive SQLite/config-bundle round trip; `TestTransformBindingFailureModeMigrationDefaultsClosed` proves the new persisted mode defaults safely to fail-closed. Model-path and attempt chains have deterministic order, recompute requirements and do not run twice on fallback. `TestRequestTransformSafeFailOpenRollsBackAndReportsWithoutErrorText`, `TestTransformCannotFailOpenProtectedIdentityOrUndeclaredDescriptorMode`, `TestRuntimeCatalogPinsTransformFailureModes` and `TestKernelRecordsRequestTransformSafeFailOpenInUsagePlan` prove transactional request fail-open, exact module permission, protected identity and observable usage evidence. `TestResponseTransformSafeFailOpenOnlyBeforeOutput` and `TestResponseSafeFailOpenStopsAfterFirstRenderedEvent` prove response fail-open is disallowed after renderer output begins. `TestResponseTransformDoesNotRewriteProviderUsageAccounting` proves transformed client usage remains separate from the decoder's original provider accounting. `TestTransformsEnforcePayloadAndDeadlineBounds` proves request payload rejection and cooperative response deadline reporting. `TestTransformCancellationIsFailClosedAndAtomicAcrossChains` and `TestKernelDoesNotDispatchWhenRequestTransformCancels` prove ignored cancellation cannot publish request/response mutations or reach upstream. `TestIPCControlCRUDUsesDaemonServices` proves local IPC binding upsert/list/delete and rejection of missing transform refs without changing storage. `TestTransformCatalogDrivesTypedBindingForm` proves catalog/schema-derived TUI form generation and typed option submission. `TestRequestTransformEffectsAndOpaqueFacetsAreExhaustivelyGuarded` covers declared/undeclared generation and tool-choice effects, unsupported-facet/provenance evidence, opaque raw extensions, prompt/message metadata and session state; `TestResponseTransformEffectMatrixAcceptsOnlyDeclaredSemanticPayload` and `TestResponseTransformCannotMutateOpaquePayloadEvenWithFailOpen` cover semantic event effects and opaque response bytes. `TestFallbackUsageDoesNotIncludeTransformFailureFromRejectedModelBranch` proves failure evidence from a rejected model branch does not leak into the selected fallback's compatibility report. Transform bounds cap serialized input/output and apply a cooperative deadline; they do not sandbox code that ignores context or temporary allocations. Use the finite SG5 core checklist below for ownership, configured lifecycle and cancellation acceptance; provider-specific transform behavior remains module conformance. |
| SG6: replay safety | Safe precommit failures may fallback according to the exact operation policy. Keyed replay requires client proof, a declared header and minimum retention, remains issuer-scoped and happens at most once within the original window. Binding/snapshot, immutable bytes/auth, absent proof, retention expiry and cancellation have named fixtures. The finite evidence below adds final-dispatch cancellation/expiry, accepted pre/post-commit cancellation and a real daemon replay-preparation drain. The generic core gate is verified for these declared phases; production opt-ins still require provider-specific guarantee evidence and none currently claim it. This is not proof for arbitrary providers or indefinitely uncooperative module code. |
| SG7: extension configuration and isolation | Versioned feature/policy/auth/operation schemas drive validation and generic control clients. A bundle includes user definitions/bindings and excludes secrets/session state. Missing compiled dependencies fail atomically. `TestSnapshotReloadDoesNotBlockStreamsAndPinsActiveRequests` holds two requests at their old upstream, atomically publishes a new snapshot, proves a new request sees the new route before either old request is released, then verifies both active requests complete on the old route. `TestDaemonIPCReloadAndDeviceAuthRemainResponsiveDuringHTTPStream` runs a discovered model through the real daemon/kernel/API path, blocks the upstream SSE, then proves Unix IPC status, snapshot reload and OAuth device start/get/cancel all return while the client stream remains open; the stream completes only after the upstream is released. |

`TestGeminiRuntimeBindingExecutesOpenAIChatToolRoundTrip` now also verifies
signature proofs are tied to the prompt/tool contract, conversation prefix and
exact target call: a signature copied into a changed branch or moved to a
different call is rejected before dispatch, while the same signature on the
original continuation is accepted. Only the signature digest, issuer, context
digest and target digest are persisted; the opaque signature remains in the
client transcript.

`TestKernelMapsOpenAIResponsesThroughAnthropicMessagesAndBack` also proves a
Responses base64 image is converted to Anthropic's typed `image.source` block
through the serving kernel; image detail and file-ID inputs that the target
contract cannot represent are rejected before dispatch.

`TestKernelInterruptedChatProviderResponsesStreamDoesNotCompleteOrFallback`
complements the Anthropic cross-wire interruption fixture: after Chat SSE has
committed a partial response to a Responses client, the kernel preserves an
incomplete Responses stream and does not switch to fallback.

`TestKernelInterruptedChatProviderResponsesStreamDoesNotCompleteOrFallback`
proves a truncated Chat-provider SSE translated into Responses stays visibly
incomplete and cannot switch to a fallback after client output has started.

The 2026-10-09 M11 follow-up adds an exhaustive request-effect/opaque-facet
fixture, response-effect acceptance/rejection coverage, and a hierarchical
fallback assertion that failed transform evidence from a rejected branch does
not appear in the selected route's usage compatibility plan. These tests
complement SG5's existing scoped, persistence, bounds, fail-open and cancellation
fixtures; the broader kernel cancellation matrix remains open.

`TestRequestTransformCannotAppendOpaqueMessageMetadata` proves that appended
messages, content parts and tool calls cannot smuggle opaque provenance/provider
data past the immutable-prefix checks. Rejection is transactional: none of the
partially added message reaches the invocation consumed by routing or egress.

`TestChatPrepareEncodesAddedCanonicalTypedMessage` verifies the native Chat
codec projects canonical text/image parts into the Chat wire content schema;
`TestNativeChatMessageRejectsUnmappedCanonicalContentParts` proves unsupported
parts fail closed instead of serializing Go IR fields as provider JSON.
`TestChatTransformRegistryToWireEncodesCanonicalMessageSuffix` composes the
kernel transform registry's mutation marker with `Chat.Prepare` and proves the
canonical append reaches wire JSON in the expected order and image shape.
Added-message fields without an exact native mapping (such as unsupported role,
name/correlation placement, invalid tool-call role, or an empty semantic item)
are rejected by Chat, Anthropic and Responses codec boundary tests rather than
silently omitted.

`TestKernelResponseTransformCancellationStopsFallbackBeforeAndAfterCommit`
adds serving-boundary cancellation evidence: client cancellation during a
response transform fails closed both before the first write and after a
committed event, never switches to another route, and never emits successful
usage for the cancelled request.

`TestNativeAnthropicOverlayRejectsReorderedSourceMessages` records the current
safe boundary for Anthropic native overlays: a transform that changes source
message order is rejected explicitly because the overlay preserves opaque
provider blocks by patching their original wire positions. It does not silently
send the original order as if the transform succeeded.

`TestNativeAnthropicOverlayAppendsTypedMessagesAndToolResults` proves newly
added canonical assistant/tool-result messages serialize as a suffix while
opaque source blocks remain in place. Additions inserted between source
messages and content without a declared native mapping fail closed in
`TestNativeAnthropicOverlayRejectsAdditionsInsertedBetweenSourceMessages` and
`TestNativeAnthropicOverlayRejectsUnrepresentableAddedContent`.

`TestClientDisconnectCancelsDataPlaneExecution` runs an actual streamed HTTP
request through the API, fallback kernel, and a blocked response transform;
closing the client after the first flushed bytes cancels the transform, prevents
fallback to the second route, and suppresses successful usage completion across
eight simultaneous requests. Higher-volume/repeated disconnect soak remains
open.
`TestDaemonStopCancelsActiveHTTPRequestBeforeDraining` verifies the daemon's
HTTP server binds request contexts to its run context and cancels them before
graceful drain, so active streams cannot prevent shutdown from reaching cleanup.
Concurrent-disconnect stress remains open.

The typed-mutation follow-up records which canonical request facets changed in
kernel-owned, non-serialized markers. `TestNativeChatEgressOverlaysOnlyTransformedTypedFacets`
proves the OpenAI Chat native-format codec replaces stale tools/tool-choice/
generation fields while retaining unrelated raw extensions. `TestOpenAIGenerationAndParallelToolChoiceNormalizeIntoTypedIR` covers the typed source contract consumed by those overlays. `TestNativeResponsesEgressOverlaysTypedMutationsAndPreservesExtensions` proves native Responses overlays tools, choice, generation, reasoning and continuity while retaining extensions. `TestNativeResponsesOverlaysTransformedInputAndPromptPreservingItems` patches native system/user content using retained source metadata while preserving opaque function-call input; `TestNativeResponsesCompatibilityRejectsUnsupportedOperationMutation` proves unsupported operation/modalities and stop-sequence changes fail admission. `TestNativeResponsesOverlaysTransformedToolCallAndResultItems` covers function-call argument and output updates with stable correlation and opaque item metadata. `TestNativeResponsesRejectsReorderedFunctionCallsWhenMessagesChange` proves message-only transforms cannot silently leave native function-call items in stale order. `TestNativeAnthropicEgressOverlaysTypedFacetsAndPreservesBlocks` verifies transformed system/message/tool-use/tool-result blocks, tool-choice, generation and thinking are written back while cache-control/vendor metadata, block order and unsupported provider blocks remain intact. Remaining: additional Anthropic opaque-envelope fixtures, opaque nested Chat-style tool-call metadata that has no Responses mapping, and the full cancellation matrix.

`TestNativeAnthropicOverlayRejectsSourceMessageRoleChange` and
`TestNativeResponsesOverlayRejectsSourceMessageRoleChange` verify native
overlays fail closed if a direct caller bypasses the kernel's immutable
message-role identity contract. Tool-use and function-call role checks remain
additional structural guards.

`TestNativeResponsesConvertsCanonicalNestedChatToolCallMetadata` converts the
standard nested Chat `id/type/function` wrapper into typed Responses call fields;
`TestNativeResponsesRejectsOpaqueNestedChatToolCallMetadata` ensures unknown
vendor extensions are not dropped when that wire shape has no lossless mapping.

`TestNativeResponsesOverlaysTransformedToolCallAndResultItems` additionally
proves tool-call arguments and tool results are updated in native Responses
items while call IDs and opaque per-item metadata remain intact. Calls removed
from typed IR are omitted; newly added canonical calls are encoded as
`function_call` items. Message-only transforms now also validate ordering of
retained native function-call items.

After the generic contracts are implemented, the synthetic/captured provider
used by these cases must be introduced using module implementations, static
composition-root registration, descriptors, manifests and fixtures only. Its
addition must leave kernel graph types, core storage schema, generic request
orchestration and frontend workflow unchanged. Implementing the generic
contracts themselves is planned core work; passing their unit tests alone is
not this change simulation.

Completion requires every SG case plus the existing composition subset,
ordinary integration checks and installed CLI verification for delivered
control surfaces. The full cases must join `make test-conformance` when they
exist. Never replace an absent case with a test name implying stronger
coverage than its assertions provide.

See the [implementation roadmap](IMPLEMENTATION_PLAN.md) for ordered slices.
Vendor completeness and measured resource/latency performance remain separate
gates even after semantic closure passes.

### Performance-preserving correctness evidence

`TestPreparedRuntimeStatementsKeepLiveCredentialAuthority` proves prepared SQL
still observes replacement/disable and caller cancellation. The companion usage
test keeps event/daily aggregation atomic on trigger failure, works with one
pooled connection and rejects writes after Close. Health-key fixtures retain
all scope/lane combinations, order and early stop; their standard benchmark
reports zero allocations. `TestComposeCompatibilityPlanOwnsNestedLossEvidence`
mutates input/output nested losses to prove preallocation/read-only intermediate
views do not alias the owned plan. The independent benchmark fixture validates
JSON/SSE content/completion and bounded concurrency through the public daemon.
Root regression/vet and three full store/runtime/kernel race runs passed.
Full conformance passed. Installed `2438b7d` then passed three independent-module
race runs with the real CLI for the benchmark fixture and activation retry/
restart vertical; main status was `ok`, control=serving=491, and bundle-v9
validation passed. `make perf` was exercised too. These are correctness and
fixture measurements, not isolated RSS or latency acceptance.

### Non-OpenAI terminal-envelope evidence

`TestAnthropicTerminalErrorEvidence` and
`TestAnthropicJSONTerminalErrorAndStreamCompletion` intercept error objects
before raw data passthrough, retain usage and reject incomplete streams or a
later message stop. `TestGeminiTerminalErrorsStopJSONAndSSE` checks errors before
candidates, numeric codes/details, retained usage across content frames and no
completion after error; the healthy case stays explicit.
`TestNonOpenAIProviderFailuresReachConfiguredClassifier` uses public daemon and
provider APIs with both built-in codec families, not a new kernel branch. It
proves configured classification into matching HTTP/SSE client failure values,
preserved partial text, no raw private-message disclosure, no implicit retry,
follow-up availability and durable failed usage after restart.

The reason selectors in the integration fixture are explicitly synthetic:
`FAILED_PRECONDITION` and `invalid_request_error` alone are not builtin
continuity-reset rules. Local source provides the wire envelopes, not a real
provider continuity guarantee. Root regression/vet, three decoder race runs and
full conformance passed. Installed `bd007fa` then passed three additional module
race runs with `GOBROOM_TEST_CLI=/absolute/path/to/gobroom` after
`make install-all`. Both protocol verticals retained typed error identity,
follow-up availability and failed usage through restart. Main status was `ok`,
control=serving=489, and bundle-v9 validation passed. No real provider was called.
This closes
the missing non-OpenAI terminal-evidence seam, not all SG1/harness/provider cases.

### Typed client-failure evidence

Typed client failures (2026-10-21, host contract v10):
`TestClientFailureStableContinuityDescriptorAndPrivatePrefix` and explicit
metadata cases prove safe known codes and cause preservation through wrapping.
Validation, budget, operation-object projection and transform tests guard the
typed value. HTTP tests check stable code/type/message without private prefixes,
custom metadata and no overwrite after commit. Renderer fixtures compare
canonical descriptors in Chat/Responses and the Anthropic details extension.
The independent scope vertical compares the same public descriptor in a 409
and a committed SSE terminal frame. These are executable interface proofs,
not evidence of automatic harness request reconstruction.

Verification: full root regression/vet, three focused core/API/renderer race
runs and full conformance passed, including the separate-module HTTP/SSE
descriptor comparison. Installed `25184ac` passed three additional independent
scope race runs with `GOBROOM_TEST_CLI=/absolute/path/to/gobroom` after
`make install-all`. CLI status/account update remained usable while HTTP/SSE
checks preserved the same typed descriptor, failed usage and reset through
restart. The main daemon was `ok`, control=serving=487; bundle-v9 validation
passed. No real provider was contacted.

### SG5 finite core checklist

The generic transform contract is bounded by the phases below, not an
unspecified universal provider or cancellation matrix. Each module still owns
its algorithm and must cooperate with context deadlines; static Go code is
not sandboxed by these checks.

| Boundary | Required executable evidence |
|---|---|
| Activation and persistence | Public-module audio/content/response lifecycle fixtures: unbound is inactive, options validate before replacement, scope/order updates take effect, restart retains bindings, disable/delete remove effects. Installed CLI exercises the ordinary IPC path. |
| Request identity and private working copy | `TestRequestTransformCannotChangeOperationContractVersion`, existing model/operation identity and transactional effect tests. A transform cannot change the selected operation version or daemon-owned provenance. |
| Response ownership | `TestResponseTransformPreservesAndIsolatesArtifactReferences` and `TestResponseTransformOwnsNestedUsageEvidence`: unchanged artifacts survive, mutations are isolated and rejected, and nested usage evidence cannot alias original provider events. |
| Effects and candidate isolation | Existing declared/undeclared effect, opaque metadata, signature issuer, branch-local fallback and requirements-recomputation tests. A rejected branch cannot leak its transform failures into another branch's plan. |
| Fail-open and bounds | Existing payload/deadline, descriptor permission, transactional safe-fail-open and post-output fail-closed tests. Usage projection cannot change original provider accounting. |
| Cancellation and committed output | `TestTransformCancellationIsFailClosedAndAtomicAcrossChains`, `TestKernelDoesNotDispatchWhenRequestTransformCancels`, `TestKernelResponseTransformCancellationStopsFallbackBeforeAndAfterCommit` and real HTTP disconnect tests. Cancellation cannot publish partial request mutations, dispatch new work, retry another route or report successful completion. |

The ownership and version guards fix defects in enforcing the existing contract;
they do not authorize artifact rewriting or introduce another extension API.
Request and response module fixtures exercise the standard daemon execution
path, not an alternate transform test engine. Passing this checklist establishes
the stated generic SG5 boundaries, not live-provider or full M15 parity.

Verified checkpoint `5aab43c`: full regression/vet/conformance, updated focused
boundary target, three full kernel/normalizer/renderer race runs and three
external request/content/response lifecycle race runs passed. After
`make install-all`, those external fixtures passed three further race runs
with installed CLI CRUD. Main control=serving=493; bundle-v9 validation passed.
This closes the finite generic SG5 checklist above. Other semantic gates and
the full C1–C6/M15 completion audit remain independent.

### SG7 finite configuration and isolation checklist

| Boundary | Executable evidence |
|---|---|
| Exact dependency and option validation | Catalog exact-ref/schema/factory, reachable-module build/host and closure tests; `TestConfigApplyRejectionsPreserveServingAndInflightRequests` adds actual IPC apply rejection for a missing module build, wrong host, unavailable transform version and schema-invalid transform options. |
| Validation rejection is non-mutating | That daemon fixture compares independently exported persisted config and both control/serving versions after every rejection, while a real upstream SSE request remains open. The held request completes and new inference still uses the prior route. A subsequent valid update publishes normally. `GOBROOM_TEST_CLI` selects installed config export/apply. |
| Accepted publication pins old requests | `TestSnapshotReloadDoesNotBlockStreamsAndPinsActiveRequests` retains old snapshots for both held requests while new requests use the replacement. Manager reload receipt tests distinguish load failure, successful publication and post-publication version persistence failure. |
| Runtime privacy and portability | `TestConfigBundleExcludesLiveRuntimeState` seeds a valid, scoped provider session with opaque data/signature proof plus health/auth/quota/usage evidence. Typed config entities survive export/import, private source markers and runtime fields do not, and fresh target runtime tables remain empty. Interactive setup coordinator state is not an input to `ExportBundle`; this fixture does not pretend auth-health rows are interactive sessions. |
| CLI JSON fidelity | `TestConfigCommandsPreserveLargeNumbers` checks validate/diff/apply keep exact option numbers and uint64-sized contract values through Unix IPC; malformed, null, array, trailing and oversized inputs are rejected before IPC. With `GOBROOM_TEST_CLI`, numeric round-trip uses the installed binary. |
| Restart does not renew session authority | `TestCachePreload*` independently validates state, count, newest unique-key selection, earlier provider expiry and persisted-time TTL. Standard `testing/synctest` advances two hours without wall-clock waits and proves repeated preload cannot resurrect expired continuity. SQLite remains responsible for current principal/endpoint checks; this does not claim another database is shipped. |

Validation rejection is distinct from a post-commit activation/publication
failure. The latter follows the existing reload/activation receipt contract;
these tests do not claim an impossible rollback of an already published
snapshot or a multi-writer transaction across storage and active requests.
Module algorithms, remote backends and full C1–C6 completion remain separate
from these finite generic configuration boundaries.

Expiry regression: `TestCachePreloadDoesNotResurrectProviderExpiredUpdate`
failed with an old unexpired parent after the repository rejected its expired
replacement. The cache now writes a typed clear marker and returns markers as
unknown. The existing independent-module session-scope fixture now restarts
immediately after provider expiry and verifies the next actual upstream call
has no parent. This is graceful-restart evidence after ordered persistence,
not crash durability or a new provider-specific core branch.

Shutdown retention evidence: `TestWorkerDrainsAcceptedEventsInOrderAfterClose`
and `TestWorkerWaitsForInFlightSaveBeforeReturning` prove channel close/drain
ownership. `TestDaemonStopDrainsAcceptedUsageBeforeClosingStore` accepts 128
failed-usage events, stops the daemon and reopens SQLite to verify every row and
its counters in order. The installed CLI switch uses actual shutdown/Wait.
The existing public-module continuation fixture previously lost failed usage
intermittently during restart because worker cancellation preceded drain and
the daemon never joined it. The worker now consumes until producer close and
the daemon waits before closing storage. This is not a durable usage spool,
whole-drain deadline, or proof against storage write failure.

`TestKernelUsageCloseSerializesConcurrentProducers` additionally races producers
with close, compares accepted/drained counts and rejects post-close admission.
Installed CLI shutdown race testing exposed that an atomic closed flag alone
did not synchronize channel send with close. A standard `sync.RWMutex` now
protects that short non-blocking admission/close boundary; it does not hold a
lock across persistence or network work.

Verified checkpoint `cbbf00d`: final root/external regression, vet and full
conformance passed. Config/preload/privacy/drain focused races passed. After
`make install-all`, config numeric fidelity, rejection during SSE and shutdown
retention passed three installed-CLI race runs; independent activation recovery
and continuity expiry/restart passed three more. Main control=serving=495 and
bundle-v9 validation were healthy. This verifies the stated finite SG7
configuration/isolation boundaries, not whole-drain deadlines, storage-failure
durability, every backend or full M15 closure.

### Usage persistence fault and cooperative drain boundary

The daemon's persistence context is independent of request cancellation. After
execution producers close, `--usage-drain-timeout` (default five seconds) or
caller cancellation reaches context-aware atomic event/daily writes. The owner
still joins the worker before closing the repository; it does not detach a
possibly active writer. Errors/counters are safe operational evidence, never
provider health failures or instructions to retry upstream work.

`TestWorkerContinuesAfterPersistenceFailureWithoutLeakingError` proves later
writes continue, failures remain visible, notification occurs once, and private
repository errors do not escape. `TestWorkerDeadlineAbandonsUnwrittenEventsAndReturnsDeadline`
counts a canceled in-flight write and queued unacknowledged work.
`TestDrainUsage*` uses virtual time to prove budget/caller cancellation and join;
`TestUsageDrainTimeoutRejectsNegativeBeforeStateInitialization` checks startup.
`TestSaveUsageEventCanceledBeforeAdmissionDoesNotWrite` and prepared statement
rollback tests preserve context rejection and event/daily atomicity.

`TestDaemonUsageWriteFailureIsVisibleWithoutFailingInference` injects an actual
SQLite write failure. Two real upstream inferences still succeed with no extra
dispatch; IPC and HTTP status show sticky, typed counters, logs exclude the
private error, recovery permits a new persisted row, and Stop/repeated Stop
retain the redacted incomplete-persistence error. `GOBROOM_TEST_CLI` drives the
status projection using the installed client. `TestKernelUsageOverflowIsCountedSeparatelyFromClose`
separates admission loss from ordinary post-close rejection.

These cases do not establish a hard wall-clock deadline for uncooperative
backends, whole-daemon shutdown, automatic retry or durable spool recovery.
Counters report acknowledgement outcomes, not a billing or power-loss guarantee.
Ordinary session-save overflow/error visibility remains a separate open contract.

### Selected transform accounting through fallback

`TestFallbackCompatibilityPlanAndUsageContainOnlySelectedTransforms` covers both
compatibility exclusion and actual confirmed upstream rejection before selecting
the next route. It checks candidate-local inputs, invocation/model/attempt/
provider/connection step order exactly once, skipped scope exclusion, rejected
branch failure isolation, selected dispatch identity and persisted-plan usage
summary. Existing routing logic satisfied this proof without a kernel change.
This closes that concrete C2 accounting case, not every provider semantic mapping.

Installed checkpoint `463e47d` / fallback proof `f8f5f09`: full root/external
regression, vet, conformance and three focused race runs passed. The final
fallback fixture also passed three race runs in both rejection phases. After
`make install-all`, three installed-CLI race runs covered actual write-failure
status, shutdown retention and config rejection while SSE remained open.
The installed daemon rejected a negative budget before state initialization;
main control=serving=496, status=ok and bundle-v9 validation were healthy.
The short performance smoke passed; no new resource-target or live-provider
acceptance is inferred. Session-save overflow/failure and full C1–C6 audit remain.

### SG6 finite boundary evidence

The local production built-ins/manifests currently contain no scoped-key replay
opt-in. `examples/external-provider-module/jobs.go` declares keyed replay for an
illustrative operation, not a production provider retention guarantee. A future
production opt-in still needs provider-source evidence and a deterministic
retention/deduplication fixture. The current absence does not establish that
arbitrary provider APIs support idempotency.

| Phase | Current proof | Latest focused verification |
|---|---|---|
| Prepare and retained bytes | `TestReplayWithKeyCancellationDuringPreparePreventsDispatch`, `TestPreparedBytesReplayFreezesPayloadAndRenewsBodyBoundAuthorization` | `TestReplayWithKeyCancellationDuringFinalDispatchValidation` covers initial/retry checks and validators returning success after cancellation |
| Ambiguous transport | `TestReplayWithKeyRetriesOnceWithinIssuerAndDerivesProviderScopedHeader`, cross-issuer/missing-key cases and HTTP no-implicit-replay tests | No new architecture rule identified |
| Accepted before client commit | `TestKernelDoesNotReplayAcceptedResponseAfterSemanticFailure`, composed pipeline tests | `TestReplayWithKeyCancellationAfterAcceptedResponseBeforeCommit`: one call, no replay/fallback or successful usage |
| After client commit | `TestKernelDoesNotSwitchAfterResponseCommitment`, execution cancellation tests | `TestReplayWithKeyCancellationAfterClientCommit`: visible bytes preserved, response resources closed and late known usage marked cancelled |
| Retention expiry | `TestReplayWithKeyExpiresBeforeAnotherDispatch`, retention policy validation | `TestReplayWithKeyRetentionExpiresDuringFinalDispatchValidation`: slow live validation cannot move the original 60-second boundary |
| Daemon stop | `TestDaemonStopCancelsActiveHTTPRequestBeforeDraining` | `TestPublicDaemonStopCancelsAndDrainsScopedReplayPreparation`: cancel real HTTP replay preparation, wait for a late-returning factory, forbid redispatch, release listeners and reopen DB/socket |

These checks refine the existing commit/cancellation requirement. They do not
create a universal provider matrix or authorize replay of accepted operations.

The final-validation expiry fixture failed before the fix with two upstream
dispatches and now suppresses the second. Core cases use the standard
`testing/synctest`, context and channels. Root regression/vet, three kernel replay
race runs, three independent-module shutdown race runs and full conformance
passed. The strengthened fixture also binds the actual old data port after Stop;
installed `3b7b34d` then passed three further independent-module race runs with
`GOBROOM_TEST_CLI=/absolute/path/to/gobroom`, exercising real status/shutdown
commands during held replay preparation. The main daemon was `ok`,
control=serving=488, and bundle-v9 validation passed. The daemon fixture proves
graceful drain, not an in-process sandbox for arbitrary extension code.

### Renewal/principal continuity evidence

Accepted-response failure (2026-10-21, host contract v9):
`TestNewProviderResponseErrorCopiesAndBoundsData` and its rejection cases guard
the immutable private envelope. Compositor fixtures prove decoder-hook isolation,
single normalization, failed-update suppression, local-error spoof prevention,
cancellation and event bounds. `TestHTTPJSONResponseContinuityClassification`
and schema/clone/adapter tests prove response signals are independent of HTTP
statuses and preserve accepted effects. Decoder tests intercept explicit errors
before raw payload disclosure, reject incomplete completion, preserve healthy
bytes and retain usage. Renderer terminal-error fixtures check single projection,
escaped JSON, no premature commit and no success after failure. The independent
scope vertical adds errors before and after partial SSE text, no implicit replay,
private-message redaction, store-version reset across restart and durable failed
usage. Root regression/vet, three focused race runs and full conformance passed.
This is not full SG1/M15
closure or broad live-provider/harness evidence.

Installed acceptance (`43c3d24`): after `make install-all`, the independent
scope vertical passed three further `-race` runs with
`GOBROOM_TEST_CLI=/absolute/path/to/gobroom`. It exercised installed CLI status
and fixture-account replacement alongside the HTTP pre/post-commit error,
failed-usage and durable-reset workflow. The main daemon stayed `ok`,
control=serving=486, and bundle-v9 validation passed. No real provider was called.

Classified continuity rejection (2026-10-21, host contract v8):
`TestHTTPJSONContinuityResetForcesInvalidationOutcome` and
`TestHTTPJSONContinuityResetCloneAndSchema` prove bounded structured matching,
shared immutable options and registered schema/factory admission.
`TestBuiltinResponsesDeclaresStructuredContinuationReset` proves the built-in
operation declares parent-not-found behavior. Kernel invalidation tests cover
single dispatch/no fallback, malformed evidence, unrelated explicit parents and
CAS protection for newer success. Cache tests cover ordered durable tombstones,
queue saturation, cancelled waits and persistence failure.
`TestContinuityRestartErrorIsSafeAndDoesNotOverwriteCommittedResponse` checks
sanitized 409 projection and committed-output protection. The independent
`TestExternalResponsesSessionScopeFollowsPrincipalAndEndpoint` adds a real HTTP
409 parent-not-found rejection and restart to its existing scope vertical.
This proves HTTP rejection invalidation, not automatic full-history replay or
provider-specific streaming-error support; SG1/M15 remains open.

Verification: full root regression/vet, three focused kernel/provider/cache/API
race runs and the full conformance target passed. Installed acceptance
`039e971` ran the independent scope vertical three more times with `-race` and
`GOBROOM_TEST_CLI=/absolute/path/to/gobroom`, including CLI status after the
409 and subsequent fixture-account replacement. Main daemon status remained
`ok` and bundle-v9 validation passed. The cache/kernel fixtures also cover a
newer save with identical data, using private store versions rather than value
equality alone. This remains deterministic local-provider evidence.

Provider continuation scope (2026-10-21, host contract v7):
`TestExternalResponsesSessionScopeFollowsPrincipalAndEndpoint` wraps the
registered Responses decoder using only the public catalog API, then drives
real ingress, credential recovery and durable restart. It proves failed-decode
rollback, expiry, explicit empty reset, renewal retention and principal/endpoint
isolation. `TestProviderSessionScopeAndAuthority` rejects queued stale-principal
writes while permitting renewal; stale endpoint and unsupported namespaces are
purged by separate store tests. Namespace and private collector tests cover
strict JSON binding, copying, cancellation and late callbacks. This closes the
generic end-to-end expiry/reset extension seam, not the provider-specific SG1
mapping/recovery cases listed above. Fixture headers are not a newly claimed
OpenAI wire contract. The external vertical passed three race runs.

Installed acceptance (`8165145`): after `make install-all`, execute
`GOBROOM_TEST_CLI=/absolute/path/to/gobroom GOWORK=off go test -race ./... -run '^TestExternalResponsesSessionScopeFollowsPrincipalAndEndpoint$' -count=3`
in `examples/external-provider-module`. This passed all three runs, including
account replacement through the installed client; main daemon status and
bundle-v9 validation remained healthy. No live provider was contacted.

`TestExternalPrincipalRenewalPreservesOperationalHealthAndQuota` exercises an
opaque custom credential through real HTTP rejection, refresh, activation and
restart, retaining independent rate cooldown and exhausted quota without reset.
Replacement then restores auth, rate-limited and quota-limited roles. The optional
installed CLI path covers safe views and connection replacement. Lower-level
fixtures include `TestCredentialRenewalPreservesPrincipalAndOperationalState`,
`TestPrincipalReplacementRotatesAndClearsRoutingState`,
`TestRenewalResetDoesNotClearFailureOfCurrentCredential`,
`TestHealthLanesKeepAuthenticationAndOperationalBlocksIndependent`,
`TestDiscoveryRecoveryDoesNotClearAuthenticationFailure` and
`TestAuthHealthPersistenceCannotOverwriteOperationalCooldown`. These close the
configured-principal renewal slice only; generic setup actions/leases and the
full SG composition gate remain open.

`TestSQLiteWriteTransactionsReserveBeforeReading` verifies the driver-native WAL
write reservation prevents another writer from invalidating validation reads,
while concurrent reads remain available. It failed with deferred transactions;
the pinned driver's `_txlock=immediate` option fixes the reproduced model-edit/
activation collision without a custom transaction retry layer. After installation
of `08d662c`, the optional CLI renewal/restart fixture passed with health/quota/
activation/status views and explicit connection replacement. Main daemon versions
were control=serving=469; bundle-v9 export/validate passed. This is not a throughput
benchmark or closure of the remaining SG composition cases.

### Generic action setup vertical

`TestExternalGenericAuthorizationSetupPersistsAndActivatesCredential` registers
an independent auth driver and exact input schemas, creates an empty-credential
account and consumes typed URL/code/input transitions. It rejects stale revisions
and invalid schemas, submits proof/OTP and waits for a provider poll signal before
making another get call. The daemon completes without a connected frontend; the
test waits for actual activation, restarts and performs HTTP inference through
custom → Physical → Combo. The optional installed CLI path uses `connections
setup start/get/submit/cancel` with mode-0600 input files, never secret argv.

`TestGenericAuthorizationSetupReusesActionIDAtCurrentRevision`,
`TestGenericAuthorizationSetupRejectsInvalidSchemaWithoutEndingSession`,
`TestGenericAuthorizationSetupRejectsUnsafeURLAndUnknownSchema`,
`TestGenericAuthorizationSetupConcurrentInputIsSingleFlight`,
`TestGenericAuthorizationSetupTimerExpiresWithoutFrontend`,
`TestGenericAuthorizationSetupCancellationFencesLateCredential`,
`TestGenericAuthorizationSetupRetainsTypedProviderDenial`,
`TestGenericAuthorizationSetupShutdownDrainsLateBegin` and
`TestGenericAuthorizationSetupCanceledCallNeverInvokesDriver` cover cancellation
before dispatch as well as draining admitted calls. The pre-canceled-call
regression failed before the explicit context guard was added.
`TestGenericAuthorizationSetupCredentialReplacementFencesStaleCommit` exercises
the final revision fence.
`TestIPCEnvelopePreservesJSONNumbers` and
`TestConnectionSetupCommandsInvokeTypedRPCs` cover exact numeric values across
typed CLI/IPC boundaries. These do not prove migrated OAuth callback actions,
generic TUI editing, credential leases or full SG2 composition.

Installed checkpoint `11a6f65`: `make install-all` and the external action setup
fixture with `GOBROOM_TEST_CLI` passed. CLI actions, daemon-only polling, activation,
restart, HTTP inference and cancellation were exercised; the main daemon reported
control=serving=471 and valid bundle-v9 export/validate. Full conformance preceded
the final pre-dispatch cancellation guard; that guard was followed by full
tests/vet and repeated generic-coordinator race checks. No live provider, migrated
OAuth callback action or performance baseline is implied.

### Shared callback grant runtime

`TestExternalModuleCallbackGrantsCredentialAndSurvivesRestart` registers a
separate auth driver with private fixed-loopback grants. Its callback uses
non-OAuth `ticket`/`proof` fields, rejects malformed/mismatched requests,
persists a credential through the normal schema/revision/CAS/activation path,
restarts and performs actual HTTP inference through Physical/Combo. The same
fixture covers port-busy manual handoff, typed denial and cancellation; the
optional installed CLI path submits callback input through a private file.

Registry evidence includes `TestAuthorizationCallbackRegistrySharesListenerAndReleasesByOwner`,
`TestAuthorizationCallbackReplacementRollbackAndOptionalBindFailure`,
`TestAuthorizationCallbackRegistryCloseRejectsAcquire`,
`TestAuthorizationCallbackHandlerRequiresExactLocalGrant`,
`TestAuthorizationCallbackHandlerFailsClosedOnAmbiguousGrants` and
`TestAuthorizationCallbackOwnerReplacementChangesToken`.
`TestAuthorizationCallbackCancellationFencesAdmittedCredential` exercises an
actual admitted HTTP callback whose driver returns late after cancellation.
`TestAuthorizationCallbackExpiryReleasesGrantAndListener` checks the expiration
finisher's resource cleanup; the generic setup timer has separate virtual-time
coverage. `TestAuthorizationCallbackWaitsForPublishedStepWithoutPolling` uses
`testing/synctest` to prove the receiver waits for the admitted step rather than
polling/bypassing it. `TestGenericAuthorizationSetupRequiredBindFailuresDoNotFillRetention`
prevents unissued failed starts from exhausting the session budget. Existing
OAuth success/denial/cancellation fixtures exercise the same listener runtime;
the previous OAuth-only listener implementation is removed.

These cases do not establish daemon-assigned ephemeral callback URL negotiation,
fully migrated code/device orchestration, generic TUI forms, credential leases
or complete SG2 composition.

Installed checkpoint `6ce9490`: `make install-all` and the separate-module callback
fixture with `GOBROOM_TEST_CLI` passed. The installed client observed loopback and
manual modes, submitted a private callback input file, canceled setup and read
denial/completion status; automatic HTTP callback, activation, restart and provider
inference also passed. The main daemon reported control=serving=472, an empty
activation queue and valid bundle-v9 export/validate. Full tests/vet/conformance
and repeated root/external callback race cases passed. These do not close the
remaining C4/SG2 gates or prove resource-performance targets.

### Daemon-assigned callback resources

`TestExternalModuleDaemonAssignedCallbackResources` registers an independent
driver that requests a port-zero callback. During its private ready event the
driver registers the allocated URI with a fake issuer; that issuer probes a
fully correlated callback before activation and receives rejection. One blocked
registration does not block another account or control status. Two held sockets
yield distinct nonzero URIs; completing one does not close the other. Activation,
restart, exposed Physical/Combo HTTP inference and cancel cleanup are included.
The optional installed CLI path reads the final consent actions and cancels a
pending dynamically allocated grant. No test port discovery or rebind is used.

`TestAuthorizationCallbackReservationAllocatesAndActivatesExactGrant`,
`TestAuthorizationCallbackReservationReuseAndOwnerIsolation`,
`TestAuthorizationCallbackReservationRollbackCancellationAndRelease`,
`TestAuthorizationCallbackActivationRejectsChangedReservation` and
`TestAuthorizationCallbackActivationPrunesUnusedReservations` cover pending socket
ownership, atomic failure and exact activation scope. The preparation cases
`TestGenericAuthorizationPreparationPublishesOnlyResolvedActions`,
`TestGenericAuthorizationPreparationFailuresReleaseReservations` and
`TestGenericAuthorizationPreparationCancellationFencesLateCredential` cover
non-public intermediate state, driver/scope/round-limit failure and late canceled
completion. `TestGenericAuthorizationSetupRejectsPublicCallbacksReadyEvent`
prevents spoofing the internal event. Resource formatting/JSON redaction has
provider-contract coverage. These close dynamic-URI ordering, not the remaining
code/device coordinator migration, TUI action forms, leases or full SG2.

Installed checkpoint `4ccecb2`: full tests/vet/conformance, three focused root race
runs and five independent-module race runs passed. After `make install-all`, the
assigned-URI fixture with `GOBROOM_TEST_CLI` verified resolved consent actions,
pending-grant cancellation, callback activation, restart and exposed Physical/
Combo inference. Main control=serving=473, activation queue empty, bundle-v9
export/validate valid. The callback-ready event remained private and registration
probes could not advance a not-yet-active grant. Remaining auth convergence,
leases, TUI and performance gates are not implied by this vertical.

### Device/action coordinator convergence

The previous device-specific coordinator/IPC/CLI/interface/metadata are removed.
`TestDeviceAuthorizationDriverKeepsSecretsOpaqueAndAwaitsOnDemand` and
`TestDeviceAuthorizationDriverMapsKnownFailuresAndSanitizesErrors` cover the
reusable strategy. `TestOAuthFactoryPreservesCodeCapabilityAlongsideDeviceActions`
retains code capability for dual-mode OAuth, while device-only options also bind.
`TestGenericAuthorizationAwaitOutlivesStepTimeoutWithoutFrontend` uses virtual
time to prove the wait is driven once with the session deadline and daemon-owned
context values. `TestGenericAuthorizationAwaitCancellationFencesLateResult` and
`TestGenericAuthorizationRemovedDeviceRPCsAreNotAliased` enforce cancellation and
the absence of old control paths. Queued await work is registered before Start
returns, and actual provider work is tracked through shared worker drain.

The previous device generation/owner ABA, delayed success, start reservation,
pending/slow-down, token privacy, shutdown, activation failure and control-during-
stream fixtures were migrated to generic setup rather than removed. Separate
module setup/refresh/restart and installed CLI fixtures now use typed actions.
`TestTUISetupShowsGenericPublicActionsAndPollsDaemon` and
`TestTUISetupCancellationUsesGenericSession` exercise the common public contract.
Module host contract v4 rejects prior lifecycle contracts; no device compatibility
adapter is retained. Authorization-code coordinator migration, inline TUI input,
opaque leases and full SG2 composition remain unclosed.

Installed checkpoint `734ace6`: full tests/vet/conformance and repeated focused
root/external race tests passed. Installed generic setup CLI completed opaque
device setup, generation fencing, refresh and restart inference; the exact
`TestPublicDaemonRejectsAuthorizationSetupAfterDefinitionOwnerChanges` fixture
passed three installed-CLI race runs, including ABA. Main control=serving=474,
activation queue empty, bundle-v9 export/validate valid. Removed device commands
are absent from installed help, old device RPCs are rejected and module host v3
is rejected. No live provider or complete C4/SG2 claim follows from these cases.

### Single interactive coordinator

Both specialized code/device coordinators, interfaces, capability fields and
control commands are removed; host contract v5 admits only the current contract.
`TestAuthorizationCodeFixedLoopbackPreparesBeforeConsent`,
`TestAuthorizationCodeAllocatedLoopbackBindsResolvedURIBeforeConsent` and
`TestAuthorizationCodeRemoteHTTPSBeginsConsentWithoutCallbackGrant` cover
resource ordering and manual delivery. Input/PKCE/redaction tests use exact
typed state and exercise the real driver after preparation. The factory choice/
explicit-mode tests retain both OAuth methods through the single interface.
Root state/PKCE/replay, wrong input/target, denial, late result, owner/generation,
expiry, shutdown and post-commit activation assertions now traverse setup actions.
`AuthorizationInputRejected` is a typed safe rejection before exchange, not a
request to kill the current challenge.

`TestExternalGenericAuthorizationCodePersistsOpaqueCredential` links an
independent provider: it registers the held URI with a fake issuer, rejects an
early fully correlated callback, verifies the S256 challenge and exact verifier/
URI at token exchange, rejects bad callbacks/replay, and restarts before HTTP
inference with the unchanged opaque credential state. This is not a live provider
or the complete fault-composition matrix. TUI input editing, leases, full SG2
and performance/whole-product gates remain unproven.

Installed checkpoint `cb50006`: full tests/vet/conformance and repeated focused
root/provider/client race cases passed. The external code strategy passed five
race runs and three final runs with context-bound registration and actual
activation readiness. After `make install-all`, `GOBROOM_TEST_CLI` exercised both
code and device through generic setup and verified opaque credential restart
inference. Main control=serving=476, queue empty, bundle-v9 export/validate valid;
old specialized command families are absent from installed help. The single-
coordinator gate is proved for these cases; C4/SG2 as a whole remains open.

### Inference-attempt credential leases

`TestCredentialLease*` covers exact scope (including operation/definition
version), copied runtime material, expiry, live revocation, release concurrency
and redaction. Kernel fixtures prove a post-prepare fence stops dispatch/fallback
without upstream health feedback, replacement releases old authority, and an
accepted response finishes after expiry in virtual time.
`TestConnectionCredentialBinding*` checks secret-free live metadata against
durable credential revision/principal/owner changes. Daemon lease fixtures cover
same-secret resave, owner rebind, disable/delete, stale issuance, renewal and
expired credential refresh. Existing refresh policy/effect/idempotency fixtures
now use lease issuance, not a compatibility callback.

`TestExternalCredentialLeaseFencesPreparedCredentialBeforeUpstreamDispatch`
links an independently registered codec and manifest into the public daemon.
With preparation blocked, public connection updates remain responsive; the old
request is rejected before either account dispatches. A fresh request uses the
replacement credential. `GOBROOM_TEST_CLI` selects the installed CLI for those
updates. This is deterministic fake-provider evidence, not a live provider or
atomic network-revocation guarantee. Discovery/quota/job lease convergence and
the remaining SG2/TUI/lifecycle matrix are still open.

Installed checkpoint `a869763`: full regression/vet/conformance and repeated
focused race tests passed. After `make install-all`, the dispatch-fence and
principal-renewal external fixtures passed three installed-CLI race runs. Main
daemon control=serving=477 and bundle-v9 export/validate remained valid. These
checks use deterministic fake issuers/upstreams, not new live-provider coverage.

### Quota consumer authority and worker lifetime

`TestCredentialLeaseQuotaScopeIsDistinctAndExact` validates exclusive operation
vs exact consumer scope without adding a provider-kind switch to the kernel.
`TestCredentialLeaseGuard*` exercises per-send validation, nil/released authority,
body cleanup, response preservation and expiry without holding locks across the
transport. Quota issuer fixtures use live SQLite metadata to reject resaves.
`TestQuotaLease*` checks stale multi-report/partial-result rejection, release on
success/error and virtual expiry. `TestQuotaRequestKey*` isolates generations
while retaining connection-scoped deduplication. `TestOpportunisticQuota*`
retains detached client behavior, tests lifetime cancellation/Close drain, and
bounds admitted workers without cooldowning dropped signals.

The external quota module reuses the HTTP JSON quota primitive, triggers work
from an actual upstream 429, and blocks before the host transport. It replaces
the selected credential through public control, verifies zero quota HTTP calls,
then stops/restarts the daemon to inspect durable absence of its obsolete
quota window. Fresh inference succeeds with the replacement. The fixture can
use the installed CLI via `GOBROOM_TEST_CLI`. Sources remain trusted code and
must use the host-provided transport; no plugin sandbox or atomic network
revocation is claimed. Discovery/probes and remaining C4/SG2 gates remain open.

Installed checkpoint `8a76e34`: full regression/vet/conformance passed; focused
root and external quota/inference/renewal cases passed three race runs, with ten
virtual-time/concurrency runs for quota runtime. After `make install-all`, all
three external verticals passed three installed-CLI race runs; the quota case
passed three more with CLI `quota` reading state after restart. The main daemon
reported control=serving=478 and bundle-v9 validation succeeded. No new live
provider or whole-product performance acceptance is implied.

### Discovery, probe and catalog commit authority

`TestCredentialLeaseHTTPGuard*` covers independent client settings, per-page
authority checks, rejected-body cleanup, cancellation and redirect prevention.
`TestDiscoveryMissingLeaseIssuerFailsClosedBeforeHTTP` removes the raw resolver
alternative; revision/pagination and wrong-scope fixtures exercise rejection and
lease release. Existing static-catalog and onboarding fixtures now use the
mandatory issuer contract without introducing network work for local catalogs.
`TestOperationProbeValidatesExactNonChatPayloadAndDoesNotRecordUsage` also
rotates material during preparation and checks no dispatch plus body cleanup.
`TestDiscoveryCredentialLeaseRenewalCanCommitCatalog` proves the new revision
after proactive renewal is the one used for publication.

`TestImportConnectionCatalog*` exercises complete vs incomplete evidence,
explicit static assignment, stale owner/principal/revision/endpoint rejection,
mid-import rollback and cancellation after a prior model write. The existing
immediate-writer transaction fixture supplies the SQL reservation invariant;
all catalog/evidence/invalid-health operations reuse transaction helpers.

`TestExternalDiscoveryLeaseFencesPaginationAndCatalogImport` uses the existing
Anthropic pagination primitive through a manifest, replaces credentials after
the first page is accepted, checks no second send/no stale import, imports a
fresh catalog and verifies it after restart. Redirect targets remain untouched.
`TestExternalInferenceProbeLeaseFencesPreparedCredential` stops a probe between
prepare and dispatch, then succeeds with new material. Both support installed
CLI control via `GOBROOM_TEST_CLI`; they are fake-provider tests, not live OAuth
or full C4/SG2/M15 closure.

Installed checkpoint `172a4a5`: full regression/vet/conformance and focused
root race cases passed. Both new external verticals passed three race runs;
after `make install-all`, all five lease/renewal verticals passed three more
using installed CLI commands. Discovery request counts distinguish explicit
refresh and preview from forbidden extra pagination; failure cleanup releases
the held handler before closing its fake upstream. Main control=serving=479
and bundle-v9 validation succeeded. No live-provider, universal auth-composition
or whole-product performance acceptance is implied.

### Generic TUI auth input

`TestSetupForm*` binds input to the exact offered session/action/revision/schema,
uses the frozen local schema validator, preserves JSON numbers and whitespace,
and checks masking, bounded JSON fallback, conditional fields, enum choices and
input cleanup. `TestSetupResult*`, same-revision/change fixtures and late-old-step
failure coverage exercise async correlation, draft preservation and invalidation.
Compiler/poll work is coalesced; cancel-in-flight and start cancellation prevent
reopening a retired editor. Terminal setup updates do not replace unrelated
resource forms. Existing activation fixtures now use the typed setup reply path;
no old generic action-result route is retained for setup RPCs.

`TestSetupAuthorizationE2EOverUnixIPC` drives real TUI commands through a daemon
with a registered auth module. It submits multiple steps with exact large-number
payloads, handles recoverable rejection without replay, waits for activation and
uses the saved credential for fake-provider inference. The companion Esc/resume/
Ctrl+x fixture proves editor closure and daemon cancellation are separate. With
`GOBROOM_TEST_CLI`, the installed CLI can submit the final step after the editor
closes, then TUI status catches up through the same public session. Inputs never
appear in retained preview/status/error data. This is command/model integration,
not a claim of exhaustive terminal UX or live OAuth acceptance. Full C4/SG2/M15
composition/fault and whole-product performance gates remain open.

Installed checkpoint `b635d42`: full regression/vet/conformance passed, and the
editor/lifecycle/IPC fixtures passed five race runs. After `make install-all`,
the TUI/installed-CLI session handoff and cancel/resume fixtures passed three
more race runs, including activation and fake inference. The default installed
TUI entered/exited alt-screen in a real PTY; the main daemon stayed healthy
afterward (control=serving=480), and bundle-v9 validation passed. Terminal form
coverage remains model/render tests plus that launch smoke check, not an
exhaustive manual UX or live-provider auth claim.

### Frontend reattachment and composed auth faults

`TestAuthorizationSetupResume*` covers same-session observation without another
Begin, strict-new busy rejection, invalid mode, cancellation of a waiting caller,
latest submitted-step revision, published background-await instructions,
replacement/owner rebind, terminal-session replacement and resource release.
Waiting uses the existing completion channel and does not cancel its owner;
the submitted-step test uses virtual-time synchronization rather than an
ordering sleep. The TUI Unix IPC fixture now drops its original model and
reattaches by connection at the same revision with no retained private input.

`TestAuthorizationCompositionResourceCallbackManualReplacement` crosses actual
reservation, ready-event, active-grant HTTP delivery and credential-CAS
boundaries: a late valid driver result cannot replace a manually updated
credential, retain callback authority or acknowledge success. Its companion
resource-failure case leaves no public session/private credential mutation.

`TestExternalAuthorizationSetupResumeAcrossFrontends` links an independent auth
module, advances to OTP, resumes from a separate frontend without the old handle,
rejects a strict-new start, then completes polling/activation and public-model
inference. `TestExternalAuthorizationResumeAfterDaemonRestartStartsFreshSession`
separates in-memory reattachment from durable credential persistence: new
challenge/handle, old handle rejected. `GOBROOM_TEST_CLI` selects the installed
CLI for the first scenario. Full auth composition and HTTPS/non-loopback
callback integration remain separate open gates; live credentials are not
required for the architecture simulations.

Installed checkpoint `be0ea67`: full regression/vet/conformance and three focused
root/external race runs passed. After `make install-all`, independent-module
resume/restart and TUI fresh-model/CLI-handoff cases each passed three more
race runs using the installed CLI. Main control=serving=481 and bundle-v9
validation remained healthy. The new flag appears in installed help. These
observations establish the stated deterministic composition subset, not full
C4/SG2 or live-provider OAuth acceptance.

### Proxied HTTPS callback receiver

`TestAuthorizationCallbackValidation*` admits bounded HTTPS intents without
granting listener authority. Code-driver fixtures require readiness before
consent and reject incompatible receipt modes/targets or required-manual
downgrades. Host contract v6 rejects v5 and earlier lifecycle hosts.
`TestCallbackIngress*` covers default-disabled behavior, explicit origin/private
bind validation, startup bind rollback before storage/IPC, and actual port-zero
address publication. The ingress uses its own bounded server and lifecycle.

`TestProxiedHTTPS*` checks rejection before final grant activation, public mode
privacy, owner/correlation isolation, wrong Host/path/escaped path/static query,
duplicate/unexpected fields, method and non-loopback peers. Forged forwarded
headers cannot create origin authority. DNS case/default-port origin matching
preserves the issuer-registered target spelling delivered to the driver.

`TestExternalAuthorizationCodeThroughPublicHTTPSProxy` links an independent
module and a real certificate-verifying `httptest` TLS proxy. A fully correlated
early callback is rejected before exchange; a valid callback completes PKCE and
activation, replay is rejected, and API-key-protected public-model inference
uses the new credential before and after restart. Callback delivery needs no
data/admin bearer token and does not expose those muxes. This proves the
declared local TLS-termination deployment boundary, not native certificate
management, arbitrary remote proxy trust or full C4/SG2/M15 closure.

Installed checkpoint `17def5b`: full regression/vet/conformance and three focused
root/external race runs passed after the origin comparison correction. After
`make install-all`, HTTPS-proxy, loopback-code and device setup each passed three
installed-CLI race runs. Main control=serving=482 and bundle-v9 validation passed.
The binary exposes `--callback-origin`/`--callback-addr`; service ExecStart omits
them, preserving default-disabled ingress. This is deterministic TLS-termination
composition evidence, not certificate deployment or broad live OAuth acceptance.

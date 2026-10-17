# M15 architecture conformance evidence

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
daemon; operation-aware feedback and provider-specific guarantees remain open,
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
background job polling occurs. The test respects the actual passive health
cooldown from `health.list`; operation-aware health/quota remains an open C5
dimension. Set `GOBROOM_TEST_CLI` to an installed CLI path to additionally verify
status/catalog projection and read the cooldown through that binary.

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

## Full semantic closure gate

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
| SG5: configured transform lifecycle | Catalog registration alone leaves a transform inactive. Bindings/options/failure mode survive SQLite/config-bundle round trip; `TestTransformBindingFailureModeMigrationDefaultsClosed` proves the new persisted mode defaults safely to fail-closed. Model-path and attempt chains have deterministic order, recompute requirements and do not run twice on fallback. `TestRequestTransformSafeFailOpenRollsBackAndReportsWithoutErrorText`, `TestTransformCannotFailOpenProtectedIdentityOrUndeclaredDescriptorMode`, `TestRuntimeCatalogPinsTransformFailureModes` and `TestKernelRecordsRequestTransformSafeFailOpenInUsagePlan` prove transactional request fail-open, exact module permission, protected identity and observable usage evidence. `TestResponseTransformSafeFailOpenOnlyBeforeOutput` and `TestResponseSafeFailOpenStopsAfterFirstRenderedEvent` prove response fail-open is disallowed after renderer output begins. `TestResponseTransformDoesNotRewriteProviderUsageAccounting` proves transformed client usage remains separate from the decoder's original provider accounting. `TestTransformsEnforcePayloadAndDeadlineBounds` proves request payload rejection and cooperative response deadline reporting. `TestTransformCancellationIsFailClosedAndAtomicAcrossChains` and `TestKernelDoesNotDispatchWhenRequestTransformCancels` prove ignored cancellation cannot publish request/response mutations or reach upstream. `TestIPCControlCRUDUsesDaemonServices` proves local IPC binding upsert/list/delete and rejection of missing transform refs without changing storage. `TestTransformCatalogDrivesTypedBindingForm` proves catalog/schema-derived TUI form generation and typed option submission. `TestRequestTransformEffectsAndOpaqueFacetsAreExhaustivelyGuarded` covers declared/undeclared generation and tool-choice effects, unsupported-facet/provenance evidence, opaque raw extensions, prompt/message metadata and session state; `TestResponseTransformEffectMatrixAcceptsOnlyDeclaredSemanticPayload` and `TestResponseTransformCannotMutateOpaquePayloadEvenWithFailOpen` cover semantic event effects and opaque response bytes. `TestFallbackUsageDoesNotIncludeTransformFailureFromRejectedModelBranch` proves failure evidence from a rejected model branch does not leak into the selected fallback's compatibility report. Transform bounds cap serialized input/output and apply a cooperative deadline; they do not sandbox code that ignores context or temporary allocations. Still missing: remaining transform effect permutations and full kernel-level cancellation matrix. |
| SG6: replay safety | A safe precommit failure can fallback. `Idempotency-Key` is validated at ingress; provider operation definitions bind a header and guaranteed minimum retention only when the exact operation declares `scoped_idempotency_key`. `ReplayWithKey` derives an issuer-scoped hash, permits at most one same-route retry within the original dispatch window, and refuses cross-issuer fallback. The replay tests above cover normal retry, suppression, retention expiry and cancellation. `TestProviderOperationBindsOnlyValidIdempotencyPolicies` and `TestLoaderBuildsTypedPhysicalAndComboGraph` cover manifest→snapshot propagation. `TestProviderDefinitionRejectsRemovedHeaderOnlyFieldInEveryJSONBoundary` rejects the removed field even inside a bundle. No operation switches routes after client commitment. Still missing: provider-specific guarantees and operation-aware health/quota feedback. |
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

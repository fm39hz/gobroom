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

| Change simulation | Executable evidence |
|---|---|
| API-key provider, `/models`, operation bindings and reusable primitives | `TestGenericProviderManifestBindsProtocolsAndSemanticTasks`; daemon manifest/discovery path in `TestDaemonPersistsManifestClassifiedQuotaEvidenceAcrossRestart` |
| Provider auth factory and daemon-owned authorization-code/device lifecycles | `TestProviderSpecificDeviceOAuthFlowUsesGenericAuthExtensionContract`; `TestDefinitionBuildsConfiguredOAuthAuthFlow` binds the configured device endpoint; `TestDaemonOwnsOAuthStatePKCEExchangeAndRejectsCallbackReplay` covers S256, manual callback validation, exchange, persistence, cancellation and replay; `TestAuthorizationCodeLoopbackCallbackCompletesWithoutFrontendExchangeCall` exercises the actual loopback HTTP callback and secret-free response; `TestDaemonOwnsDeviceAuthorizationPollingAndKeepsDeviceCodePrivate` covers RFC 8628 pending→approval, private device-code retention, public action projection, CAS persistence and cancellation; `TestDeviceAuthorizationReservesConnectionBeforeStartingProviderFlow` proves concurrent starts cannot issue duplicate device grants; TUI tests cover public device instructions and loopback authorization status polling. Live provider behavior, denial/slow-down/expiry and shutdown stress fixtures remain open. |
| Safe provider configuration metadata for generic control clients and schema-driven TUI auth forms | `TestDefinitionCatalogExposesGenericSetupMetadataWithoutSecrets`; `TestProviderDefinitionMetadataIsAvailableThroughGenericControlAPI`; daemon IPC catalog assertion in `TestIPCControlCRUDUsesDaemonServices`; `TestConnectionFormUsesProviderAuthSetupSchema` |
| Versioned provider/primitive refs and schema catalog through IPC/HTTP/CLI | `TestPrimitiveReferencesRequireTheExactContractVersion`; `TestCatalogPinsSchemaAndModuleVersionsAndValidatesBeforeFactory`; `TestExtensionCatalogEndpointExposesContractVersionsAndOptionsSchemas`; `TestIPCControlCRUDUsesDaemonServices`; `TestRootHelpAndTypedCommandsDoNotRequireSeparateTUIBinary`; installed CLI smoke is part of the C1 check |
| Versioned operation definition, request payload schema and replay declaration | `TestChatDefinitionCompilesTypedInputAndPinsReplayContract`; `TestRegisteredNonChatOperationValidatesItsOwnPayload`; `TestRuntimeBindingAcceptsNewNamespacedOperationWithoutOperationSwitch`; `TestKernelRoutesNewOperationUsingGenericRouteTaskContract`; `TestOperationSchemaFailureUsesClientErrorBoundary`; custom ingress version/payload assertion in `TestNamespacedOperationIngressRegistersWithoutServerRouteBranch` |
| Operation input, semantic response event and rendered output resource bounds | `TestOperationPayloadBoundRejectsOversizedBodyBeforeSchemaDecode`; `TestComposedSemanticPipelineBoundsEachDecodedResponseEvent`; `TestResponseCommitWriterEnforcesOperationOutputBound` |
| Artifact owner/replay/sensitivity/recipient/media/size contract; named operation artifact ports; multipart-to-spool ingress; authorized lease reads | `TestArtifactContractEnforcesOwnerScopeSensitivityAndBoundedBodyRef`; required role/type/count, frozen descriptor projection and recipient validation in `TestOperationAcceptsOnlyRegisteredOwnedArtifacts`; exact field/role/type registration plus streamed multipart body in `TestMultipartOperationIngressMustMatchOperationArtifactPorts` and `TestMultipartOperationIngressSpoolsDeclaredArtifact`; partial-decode cleanup in `TestMultipartOperationIngressReleasesPartialSpoolsOnDecodeFailure`; `TestMultipartOperationExecutesThroughHTTPKernelAndAdapter` proves multipart HTTP → operation normalization → kernel → authorized adapter lease → response; bounded store/lease lifecycle and exact recipient context in `internal/artifacts` tests. Built-in provider codecs still do not render operation artifacts as native multimodal request parts. |
| Versioned ingress operation, provider task ref and route binding | `TestNamespacedOperationIngressRegistersWithoutServerRouteBranch`; `TestRuntimeBindingAcceptsNewNamespacedOperationWithoutOperationSwitch`; `TestKernelRoutesNewOperationUsingGenericRouteTaskContract`; these prove JSON payload schema admission and exact version selection, not a real media wire/result |
| Versioned feature evaluators, constraint schemas and route eligibility | `TestNewFeatureExtensionNegotiatesTypedConstraintsWithoutKernelBranch`; `TestFeatureEvaluatorsBindFromVersionedExtensionCatalog`; `TestKernelRunsOnlyRouteWhoseRegisteredFeatureEvaluatorAcceptsRequest`; `TestRuntimeRegistryContributesVersionedFeatureEvaluatorsToSharedCatalog` |
| Versioned provider primitive factories, including auth, and exact model-policy strategy resolution | `TestPrimitiveFactoriesWithSameIDBindOnlyTheirExactContractVersion`; `TestVersionedPrimitiveRefsSelectExactAdapterComponents`; `TestAuthPrimitiveVersionsBindTheirOwnFactoriesAndSchemas`; `TestRuntimeRegistryStoresStrategyImplementationsByExactVersion`; `TestExactStrategyRefOptionsReachTopLevelSchedulerPrimitive`; `TestControlPlaneRejectsUnregisteredStrategyVersionInsteadOfFallingBackByID`; `TestValidateBundleResolvesExactStrategyRefsAndOptions`; `TestStrategyStateIsScopedToExactContractRefAcrossReloads`; builtin metadata projection in `TestIPCControlCRUDUsesDaemonServices` |
| Request/response transform extension | `TestRequestTransformRegistryRunsBeforeKernelRequirementsAndProviderEncoding`; `TestRuntimeBindingExecutesSelectedEndpointAndCodecs` |
| Provider decoder → semantic events → client renderer, including commit boundary | `TestRuntimeBindingExecutesSelectedEndpointAndCodecs`; `TestComposedSemanticPipelineDoesNotReplayAcceptedUpstreamResponse`; `TestKernelDoesNotReplayAcceptedResponseAfterSemanticFailure` |
| Semantic Anthropic client egress | `TestOpenAIChatDecoderRendersAnthropicMessagesFromSemanticEvents` exercises OpenAI Chat→canonical events→Anthropic Messages JSON; `TestOpenAIResponsesDecoderPreservesToolIdentityForAnthropicEgress` exercises OpenAI Responses tool identity and argument deltas through Anthropic SSE egress. Renderer tests cover streaming/non-stream text/tools, usage, issuer-signed thinking preservation, and rejection without signatures or when a transform could invalidate them. This is partial SG1 evidence, not the complete reasoning/continuity/loss-policy matrix. |
| Anthropic client request → OpenAI Chat request codec | `TestAnthropicMessagesNormalizeToTypedConversationToolsAndOptions` verifies system, multimodal content, tool-use/result history, tool choice and common generation options become typed IR; `TestAnthropicMessagesRequestMapsToOpenAIChatFromTypedIR` verifies the rebuilt OpenAI wire request and admission plan; `TestAnthropicThinkingIntentIsRejectedByOpenAIChatCompatibilityBeforeEncoding` and `TestKernelExcludesOpenAIChatRouteForAnthropicThinkingBeforeDispatch` prove unsupported thinking budget is rejected before upstream dispatch; `TestAnthropicOpaqueThinkingAndToolResultErrorsBecomeExplicitFacets` checks fail-closed history semantics. This is partial SG1 evidence; continuity/signature translation and named loss policy remain open. |
| Anthropic client request → OpenAI Responses request codec | `TestAnthropicAdaptiveEffortBecomesTypedReasoningIntent` and `TestAnthropicMalformedThinkingIntentIsExplicitlyUnsupported` verify protocol-specific reasoning is bound into typed intent or retained as unsupported; `TestAnthropicMessagesRequestMapsToOpenAIResponsesFromTypedIR` verifies system/image/tool-history/tool-choice/generation encoding and the complete request plan; `TestAnthropicThinkingBudgetIsRejectedByResponsesBeforeEncoding` and `TestKernelExcludesOpenAIResponsesRouteForAnthropicThinkingBudgetBeforeDispatch` prove non-equivalent thinking budgets are rejected before dispatch; `TestAnthropicEffortRouteRequiresSignedThinkingEgress` proves request-side effort encoding alone does not admit a route whose renderer cannot preserve required Anthropic signatures. This is partial SG1 evidence; signed thinking egress, continuity and named-loss policy remain open. |
| Operation event and projected-result schemas are enforced at the kernel response-event boundary | `TestKernelEnforcesExactOperationEventSchemaAtAdapterBoundary` rejects a tool-call event that violates the operation's exact event schema; `TestKernelValidatesProjectedOperationResultAtCompleteEvent` rejects a malformed bounded result before terminal completion is accepted |
| Versioned manifest options configure a reusable streaming multipart request codec | `TestRuntimeBindingBindsManifestConfiguredMultipartCodecOptions` binds typed role/field options from the provider definition; `TestMultipartRequestCodecStreamsConfiguredArtifactFields` proves binary bytes are streamed into the mapped form part; fail-closed mapping and stream-mode checks are in `TestMultipartRequestCodecFailsClosedForUnmappedArtifactsAndStreamMode`; exact artifact-role compatibility declarations are in `TestMultipartRequestCodecDeclaresExactArtifactRoleCompatibility` |
| New quota/error envelope and restart-persistent routing effect | `TestDaemonPersistsManifestClassifiedQuotaEvidenceAcrossRestart` |
| Compatibility facet composition and fail-closed route admission | `TestCompatibilityPlanComposesFacetMappingsInStageOrder`; `TestCompatibilityPlanFailsClosedForRequiredUnknownOrUnsupportedFacet`; `TestCompatibilityPlanRequiresNamedAndGrantedLosses`; `TestCompatibilityPlanIgnoresUnrequestedUnsupportedFacet`; `TestCompatibilityPlanRejectsUnscopedDegradationAndInvalidDeclarations`; `TestKernelRejectsAdapterWithoutRequiredFacetDeclarationBeforeDispatch` |
| Model-path/server loss ceilings and usage evidence | `TestModelLossPoliciesRoundTripThroughConfigBundle` proves Physical/Combo and server ceiling portability in bundle v5; `TestDiffBundleDetectsServerLossCeilingChanges` proves ceiling updates appear in dry-run; `TestLoaderBuildsTypedPhysicalAndComboGraph` proves both policies reach the immutable graph; `TestNodeLossPoliciesFlowDownAndPermittedLossIsRecordedInUsage` proves model grants are constrained by the server allowlist, denials dominate, and requested/effective values, semantic path, granting model source, fidelity and loss ID appear in usage; `TestIPCControlCRUDUsesDaemonServices` exercises the CLI backing operation. |
| Compatibility plan composition, persistence, request preflight and explanations | `TestTransformRegistriesProjectScopedCompatibilitySteps` proves exact active request/response transform refs, scope, order and declared effects are compiled; `TestRequestTransformRegistryRunsBeforeKernelRequirementsAndProviderEncoding` proves the kernel passes the post-transform invocation and chain into admission; `TestKernelGrantsMultipartBodyOnlyToDeclaredOperationRecipient` proves the kernel supplies only the operation-authorized artifact transfer; `TestComposedCompatibilityPlanRetainsTransformAndArtifactStages` verifies those chains/transfers survive into the composed plan; `TestNodeLossPoliciesFlowDownAndPermittedLossIsRecordedInUsage` verifies the selected candidate plan is attached to its completion event; `TestNormalizedRuntimePrimitivesPersistSeparately` proves the payload-free summary round-trips through SQLite with usage; `TestExplainCompatibilityUsesModelPathAndDoesNotDispatch` and `TestRouteExplainIPCPreflightsRequestCompatibility` prove request-specific admission uses the model path and does not dispatch or leak prompt content. `usage.list`, the TUI Usage inspector and `gobroom route-explain --request-file` expose the selected plan. This is partial C2 evidence; transform fallback accounting and complete SG1/SG4 matrices remain open. |
| Portable provider dependencies | `TestBundleEmbedsOnlySecretFreeProviderDefinitionsAndPersistsThem` proves safe typed manifests travel in bundle v5 and load after restart while opaque auth options stay external; `TestBundleRejectsProviderDefinitionPayloadDifferentFromPinnedDescriptor` binds payload to the exact catalog descriptor; `TestBundleExportsUnusedUnresolvedProviderWithoutInventingBehavior` retains a missing definition as unresolved only if no model route or Physical source uses it; active dependencies fail closed unless the safe definition payload is embedded or its exact external module is installed; `TestConfigApplyPersistsPortableProviderDefinitionAndRequiresRestart` verifies restart activation for a newly imported definition. |
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
transport, codec, auth, discovery, usage, classifier, quota and client renderer
descriptors. Built-in endpoint/usage/error/OAuth option schemas and compiled
fingerprints are available from IPC `extensions.catalog`, HTTP control
`/api/extensions` and CLI `gobroom extensions catalog`.
The provider runtime currently registers one implementation version per
primitive ID; side-by-side module versions and catalog-backed operation,
feature and strategy descriptors remain in C1.

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
and real non-chat job execution remain unimplemented, so SG6 is still pending.

## Full semantic closure gate

The following cases are required in addition to the existing subset. They use
deterministic upstream/callback fixtures; live credentials are not required.
Each assertion checks observable behavior through the ordinary control and
serving boundaries, not merely registry acceptance.

| Case | Required proof |
|---|---|
| SG1: full compatibility composition | A request with tools, reasoning budget and scoped continuity is evaluated across encoder, decoder, renderer and transforms. Exact intent excludes incompatible candidates before dispatch; permitted clamping records a named loss. Undeclared support fails closed. Both OpenAI and Anthropic client rendering are exercised from semantic events. |
| SG2: interactive auth lifecycle | Device authorization displays code/URL, polls, honors slow-down and completes without a frontend. Authorization-code fixtures exercise state/PKCE, callback replay rejection, cancellation/expiry and concurrent connection isolation. Credential application includes a request-bound signing fixture. Atomic setup/refresh generation checks preserve newer credentials. |
| SG3: operation/artifact semantics | A non-chat operation consumes a genuine multipart/binary input and emits its typed result plus registered semantic events. Its compiler derives requirements from that input. Required unknown events, expired artifacts and wrong-issuer signatures are rejected; portable artifacts survive an allowed transfer. Multipart HTTP → kernel → adapter lease → bounded result projection is covered by a synthetic operation fixture. `TestOperationArtifactOutputsAreTypedBoundedAndRecipientScoped` proves exact output type/role/count and recipient contract; `TestResponseArtifactScopeStreamsAuthorizesAndReleasesLease`, `TestResponseArtifactScopeRejectsUnauthorizedRecipientWithoutSpooling` and `TestResponseArtifactScopeEnforcesAggregateOperationOutputBound` cover private bounded provider-owned spooling, transfer authorization and cleanup; `TestKernelTransfersProviderResponseArtifactThroughAuthorizedRendererLease` proves output refs are validated, the client renderer receives a scoped lease, and bytes are inaccessible after response completion. Still missing: requirement derivation from artifact contents, real media framing/provider behavior, expired-body and issuer-signature fixtures, and portable artifact transfer. |
| SG4: native wire versus transforms | Unmodified native JSON/SSE output is preserved with bounded observations. Enabling a text/tool semantic transform forces re-encoding, changes the client output and preserves tool correlation. Missing semantic renderer support rejects the route before dispatch. |
| SG5: configured transform lifecycle | Catalog registration alone leaves a transform inactive. Bindings/options/failure mode survive SQLite/config-bundle round trip; `TestTransformBindingFailureModeMigrationDefaultsClosed` proves the new persisted mode defaults safely to fail-closed. Model-path and attempt chains have deterministic order, recompute requirements and do not run twice on fallback. `TestRequestTransformSafeFailOpenRollsBackAndReportsWithoutErrorText`, `TestTransformCannotFailOpenProtectedIdentityOrUndeclaredDescriptorMode`, `TestRuntimeCatalogPinsTransformFailureModes` and `TestKernelRecordsRequestTransformSafeFailOpenInUsagePlan` prove transactional request fail-open, exact module permission, protected identity and observable usage evidence. `TestResponseTransformSafeFailOpenOnlyBeforeOutput` and `TestResponseSafeFailOpenStopsAfterFirstRenderedEvent` prove response fail-open is disallowed after renderer output begins. `TestResponseTransformDoesNotRewriteProviderUsageAccounting` proves transformed client usage remains separate from the decoder's original provider accounting. `TestTransformsEnforcePayloadAndDeadlineBounds` proves request payload rejection and cooperative response deadline reporting. `TestTransformCancellationIsFailClosedAndAtomicAcrossChains` and `TestKernelDoesNotDispatchWhenRequestTransformCancels` prove ignored cancellation cannot publish request/response mutations or reach upstream. `TestIPCControlCRUDUsesDaemonServices` proves local IPC binding upsert/list/delete and rejection of missing transform refs without changing storage. `TestTransformCatalogDrivesTypedBindingForm` proves catalog/schema-derived TUI form generation and typed option submission. `TestRequestTransformEffectsAndOpaqueFacetsAreExhaustivelyGuarded` covers declared/undeclared generation and tool-choice effects, unsupported-facet/provenance evidence, opaque raw extensions, prompt/message metadata and session state; `TestResponseTransformEffectMatrixAcceptsOnlyDeclaredSemanticPayload` and `TestResponseTransformCannotMutateOpaquePayloadEvenWithFailOpen` cover semantic event effects and opaque response bytes. `TestFallbackUsageDoesNotIncludeTransformFailureFromRejectedModelBranch` proves failure evidence from a rejected model branch does not leak into the selected fallback's compatibility report. Transform bounds cap serialized input/output and apply a cooperative deadline; they do not sandbox code that ignores context or temporary allocations. Still missing: remaining transform effect permutations and full kernel-level cancellation matrix. |
| SG6: replay safety | A safe precommit failure can fallback. `Idempotency-Key` is validated at ingress; provider operation definitions may bind a valid upstream header only when the exact operation declares `scoped_idempotency_key`. `ReplayWithKey` derives a deterministic hash scoped to the exact provider definition, node/endpoint, credential, protocol, model and operation, retries at most once on the same route after ambiguous transport/eligible unknown-effect responses, and refuses cross-issuer fallback. `TestReplayWithKeyRetriesOnceWithinIssuerAndDerivesProviderScopedHeader`, `TestReplayWithKeyNeverFallsBackAcrossIssuerAfterAmbiguousRetry`, `TestReplayWithKeyWithoutClientProofDoesNotRetryAmbiguousDispatch`, `TestProviderOperationBindsOnlyValidIdempotencyHeaderNames` and `TestLoaderBuildsTypedPhysicalAndComboGraph` cover the contract and manifest→snapshot path. An unsafe dispatched operation with ambiguous timeout cannot duplicate an upstream job; no operation switches routes after client commitment. Still missing: real provider idempotency semantics/expiry and async job effect fixtures. |
| SG7: extension configuration and isolation | Versioned feature/policy/auth/operation schemas drive validation and generic control clients. A bundle includes user definitions/bindings and excludes secrets/session state. Missing compiled dependencies fail atomically. Reload pins existing streams to their old catalog/snapshot. Multiple long streams do not block control/auth actions. |

The 2026-10-09 M11 follow-up adds an exhaustive request-effect/opaque-facet
fixture, response-effect acceptance/rejection coverage, and a hierarchical
fallback assertion that failed transform evidence from a rejected branch does
not appear in the selected route's usage compatibility plan. These tests
complement SG5's existing scoped, persistence, bounds, fail-open and cancellation
fixtures; the broader kernel cancellation matrix remains open.

`TestKernelResponseTransformCancellationStopsFallbackBeforeAndAfterCommit`
adds serving-boundary cancellation evidence: client cancellation during a
response transform fails closed both before the first write and after a
committed event, never switches to another route, and never emits successful
usage for the cancelled request.

The typed-mutation follow-up records which canonical request facets changed in
kernel-owned, non-serialized markers. `TestNativeChatEgressOverlaysOnlyTransformedTypedFacets`
proves the OpenAI Chat native-format codec replaces stale tools/tool-choice/
generation fields while retaining unrelated raw extensions. `TestOpenAIGenerationAndParallelToolChoiceNormalizeIntoTypedIR` covers the typed source contract consumed by those overlays. `TestNativeResponsesEgressOverlaysTypedMutationsAndPreservesExtensions` proves native Responses overlays tools, choice, generation, reasoning and continuity while retaining extensions. `TestNativeResponsesOverlaysTransformedInputAndPromptPreservingItems` patches native system/user content using retained source metadata while preserving opaque function-call input; `TestNativeResponsesCompatibilityRejectsUnsupportedOperationMutation` proves unsupported operation/modalities and stop-sequence changes fail admission. `TestNativeResponsesOverlaysTransformedToolCallAndResultItems` covers function-call argument and output updates with stable correlation and opaque item metadata. `TestNativeAnthropicEgressOverlaysTypedFacetsAndPreservesBlocks` verifies transformed system/message/tool-use/tool-result blocks, tool-choice, generation and thinking are written back while cache-control/vendor metadata, block order and unsupported provider blocks remain intact. Remaining: additional Anthropic additions/reorders and nested opaque fixtures, Responses nested Chat-style tool-call conversion and the full cancellation matrix.

`TestNativeResponsesOverlaysTransformedToolCallAndResultItems` additionally
proves tool-call arguments and tool results are updated in native Responses
items while call IDs and opaque per-item metadata remain intact. Calls removed
from typed IR are omitted; newly added canonical calls are encoded as
`function_call` items.

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

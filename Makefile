BIN      := gobroom
DAEMON   := gobroomd
VERSION  := $(shell git describe --tags --long --dirty --match 'v*' 2>/dev/null | sed -E 's/^v//; s/-([0-9]+)-g/.r\1.g/; s/-/./g')
LDFLAGS  := -s -w $(if $(VERSION),-X main.version=$(VERSION))
REMOTE   := origin
BRANCH   := master

.PHONY: help build build-daemon build-all run run-daemon tui test test-conformance test-v race bench fmt vet install install-all clean reload status

help: ## list targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}; {printf "  %-16s %s\n", $$1, $$2}'

build: ## build CLI
	go build -ldflags='$(LDFLAGS)' -o $(BIN) ./cmd/gobroom

build-daemon: ## build daemon
	go build -ldflags='$(LDFLAGS)' -o $(DAEMON) ./cmd/gobroomd

build-all: build build-daemon ## build client and daemon

run: ## run CLI (ARGS='status')
	go run -ldflags='$(LDFLAGS)' ./cmd/gobroom $(ARGS)

run-daemon: ## run daemon in foreground
	go run -ldflags='$(LDFLAGS)' ./cmd/gobroomd $(ARGS)

tui: ## run bundled TUI
	go run -ldflags='$(LDFLAGS)' ./cmd/gobroom

status: ## query daemon status over IPC
	go run ./cmd/gobroom status

reload: ## reload daemon snapshot over IPC
	go run ./cmd/gobroom reload

test: ## unit and integration tests
	go test ./...

test-conformance: ## architectural extension change simulations (M15)
	go test ./internal/extensions ./internal/normalize ./internal/store ./internal/operations ./internal/provider ./internal/api ./internal/kernel ./internal/adapter/openai ./internal/adapter/anthropic ./internal/adapter/gemini ./internal/adapter/renderers ./internal/controlplane ./internal/daemon ./internal/tui ./cmd/gobroom -run '^(TestCatalogPinsSchemaAndModuleVersionsAndValidatesBeforeFactory|TestCatalogRejectsRemoteSchemaReferencesDuringFreeze|TestCatalogRequiresCompleteDependencyGraphAndRejectsCycles|TestCatalogOptionsWithoutSchemaAreRejectedAndSnapshotIsImmutable|TestChatDefinitionCompilesTypedInputAndPinsReplayContract|TestRegisteredNonChatOperationValidatesItsOwnPayload|TestPrimitiveReferencesRequireTheExactContractVersion|TestGenericProviderManifestBindsProtocolsAndSemanticTasks|TestDefinitionBuildsConfiguredOAuthAuthFlow|TestDefinitionCatalogExposesGenericSetupMetadataWithoutSecrets|TestProviderSpecificDeviceOAuthFlowUsesGenericAuthExtensionContract|TestNamespacedOperationIngressRegistersWithoutServerRouteBranch|TestOperationSchemaFailureUsesClientErrorBoundary|TestProviderDefinitionMetadataIsAvailableThroughGenericControlAPI|TestExtensionCatalogEndpointExposesContractVersionsAndOptionsSchemas|TestNewFeatureExtensionNegotiatesTypedConstraintsWithoutKernelBranch|TestFeatureEvaluatorsBindFromVersionedExtensionCatalog|TestRuntimeRegistryContributesVersionedFeatureEvaluatorsToSharedCatalog|TestKernelRunsOnlyRouteWhoseRegisteredFeatureEvaluatorAcceptsRequest|TestKernelRoutesNewOperationUsingGenericRouteTaskContract|TestNewStrategyExtensionAppearsInCatalogAndSchedulerWithoutKernelBranch|TestRuntimeRegistryStoresStrategyImplementationsByExactVersion|TestRequestTransformRegistryRunsBeforeKernelRequirementsAndProviderEncoding|TestRuntimeBindingAcceptsNewNamespacedOperationWithoutOperationSwitch|TestRuntimeBindingExecutesSelectedEndpointAndCodecs|TestComposedSemanticPipelineDoesNotReplayAcceptedUpstreamResponse|TestDaemonPersistsManifestClassifiedQuotaEvidenceAcrossRestart|TestConnectionFormUsesProviderAuthSetupSchema|TestRootHelpAndTypedCommandsDoNotRequireSeparateTUIBinary|TestMultipartOperationIngressSpoolsDeclaredArtifact|TestMultipartOperationIngressReleasesPartialSpoolsOnDecodeFailure|TestMultipartIngressBindingsMustMatchOperationArtifactPorts|TestMultipartOperationExecutesThroughHTTPKernelAndAdapter|TestKernelGrantsMultipartBodyOnlyToDeclaredOperationRecipient|TestCompatibilityPlanComposesFacetMappingsInStageOrder|TestCompatibilityPlanFailsClosedForRequiredUnknownOrUnsupportedFacet|TestCompatibilityPlanRequiresNamedAndGrantedLosses|TestCompatibilityPlanIgnoresUnrequestedUnsupportedFacet|TestCompatibilityPlanRejectsUnscopedDegradationAndInvalidDeclarations|TestKernelRejectsAdapterWithoutRequiredFacetDeclarationBeforeDispatch|TestComposedAdapterRejectsRendererEventMissingFromDecoderContract|TestGeminiCompatibilityPlanRejectsUnsupportedRequestFacetsBeforeDispatch|TestKernelRejectsGeminiContinuityBeforePreparingUpstreamRequest|TestOpenAIChatRendererPlansRequiredSemanticEvents|TestActiveSemanticTransformDisablesOpenAIWirePassthrough|TestWirePassthroughRejectsSemanticResponseTransform|TestKernelDoesNotReplayAcceptedResponseAfterSemanticFailure|TestComposedSemanticPipelineDoesNotReplayAcceptedUpstreamResponse|TestKernelDoesNotFallbackAfterAmbiguousDispatchedRequest|TestReplaySafetyAllowsPostDispatchReplayOnlyForConfirmedRejection|TestDataPlaneAccessLogRecordsSafeRequestSummaryAndPreservesStreaming|TestLogsListReturnsRecentStructuredRecordsWithBoundedLimit|TestRegisteringTransformDoesNotActivateItWithoutEnabledBinding|TestResponseTransformRegistrationNeedsAnEnabledMatchingScope|TestSQLitePragmasApplyToEveryPooledConnection|TestCloneRequestIsolatesNestedTransformMutation|TestModelScopedTransformUsesBranchLocalInputBeforeFallback|TestConnectionScopedTransformIsCandidateLocal|TestTransformBindingsRoundTripThroughTypedConfigBundle|TestDiffBundleDetectsTransformBindingUpdates|TestTransformOptionsUseVersionedSharedExtensionSchema|TestRequestTransformRejectsUndeclaredMutationTransactionally)$$' -count=1
	go test ./internal/kernel ./internal/provider ./internal/controlplane -run '^(TestExactStrategyRefOptionsReachTopLevelSchedulerPrimitive|TestRuntimeRegistryStoresStrategyImplementationsByExactVersion|TestControlPlaneRejectsUnregisteredStrategyVersionInsteadOfFallingBackByID|TestValidateBundleResolvesExactStrategyRefsAndOptions|TestStrategyStateIsScopedToExactContractRefAcrossReloads|TestPrimitiveFactoriesWithSameIDBindOnlyTheirExactContractVersion|TestVersionedPrimitiveRefsSelectExactAdapterComponents|TestAuthPrimitiveVersionsBindTheirOwnFactoriesAndSchemas)$$' -count=1
	go test ./internal/operations ./internal/kernel -run '^(TestOperationPayloadBoundRejectsOversizedBodyBeforeSchemaDecode|TestComposedSemanticPipelineBoundsEachDecodedResponseEvent|TestResponseCommitWriterEnforcesOperationOutputBound)$$' -count=1
	go test ./internal/extensions ./internal/operations -run '^(TestArtifactContractEnforcesOwnerScopeSensitivityAndBoundedBodyRef|TestOperationAcceptsOnlyRegisteredOwnedArtifacts)$$' -count=1
	go test ./internal/artifacts -run '^(TestFileStoreStreamsBoundedOwnerScopedLeases|TestFileStoreEnforcesPerBodyTotalAndContextBounds|TestFileStorePrunesExpiredBodyReferences|TestContextArtifactAccessRequiresExactRecipientAndPolicy)$$' -count=1
	go test ./internal/kernel -run '^(TestKernelEnforcesExactOperationEventSchemaAtAdapterBoundary|TestKernelValidatesProjectedOperationResultAtCompleteEvent)$$' -count=1
	go test ./internal/operations -run '^TestOperationResultSchemaRequiresBoundedProjectorAtRegistration$$' -count=1
	go test ./internal/provider -run '^(TestMultipartRequestCodecStreamsConfiguredArtifactFields|TestRuntimeBindingBindsManifestConfiguredMultipartCodecOptions|TestMultipartRequestCodecFailsClosedForUnmappedArtifactsAndStreamMode)$$' -count=1
	go test ./internal/provider -run '^TestMultipartRequestCodecDeclaresExactArtifactRoleCompatibility$$' -count=1
	go test ./internal/tui -run '^TestTUIDeviceAuthorizationShowsPublicInstructionsAndPollsDaemon$$' -count=1
	go test ./internal/tui -run '^TestTUIAuthorizationCodeFlowShowsLoopbackModeAndPollsTerminalStatus$$' -count=1
	go test ./internal/adapter/renderers -run '^(TestAnthropicMessagesRendererProjectsCanonicalTextAndTools|TestAnthropicMessagesRendererBuildsNonStreamingMessage|TestAnthropicSemanticThinkingRequiresAndPreservesIssuerSignature)$$' -count=1
	go test ./internal/adapter/openai -run '^TestOpenAIChatDecoderRendersAnthropicMessagesFromSemanticEvents$$' -count=1
	go test ./internal/adapter/openai -run '^TestOpenAIResponsesDecoderPreservesToolIdentityForAnthropicEgress$$' -count=1
	go test ./internal/adapter/anthropic -run '^TestAnthropicDecoderEmitsThinkingSignatureAsCanonicalEvent$$' -count=1
	go test ./internal/normalize -run '^(TestAnthropicMessagesNormalizeToTypedConversationToolsAndOptions|TestAnthropicOpaqueThinkingAndToolResultErrorsBecomeExplicitFacets)$$' -count=1
	go test ./internal/adapter/openai -run '^(TestAnthropicMessagesRequestMapsToOpenAIChatFromTypedIR|TestAnthropicThinkingIntentIsRejectedByOpenAIChatCompatibilityBeforeEncoding|TestKernelExcludesOpenAIChatRouteForAnthropicThinkingBeforeDispatch)$$' -count=1
	go test ./internal/store -run '^TestCredentialCompareAndSwapDoesNotOverwriteNewerCredential$$' -count=1

	go test ./internal/daemon -run '^(TestDaemonOwnsOAuthStatePKCEExchangeAndRejectsCallbackReplay|TestAuthorizationCodeLoopbackCallbackCompletesWithoutFrontendExchangeCall|TestDaemonOwnsDeviceAuthorizationPollingAndKeepsDeviceCodePrivate)$$' -count=1
	go test ./internal/daemon -run '^(TestLoopbackCallbackAddressRejectsNonLocalAndInvalidRedirects|TestOAuthCallbackTargetMatchesExactRegisteredPathAndQuery)$$' -count=1
	go test ./internal/daemon -run '^TestDeviceAuthorizationReservesConnectionBeforeStartingProviderFlow$$' -count=1

test-v: ## verbose tests
	go test ./... -count=1 -v

race: ## race detector
	go test -race ./...

bench: ## benchmarks
	go test ./... -bench=. -benchmem -run=^$$

fmt: ## gofmt all Go files
	gofmt -w $$(find . -name '*.go' -not -path './tools/*')

vet: ## go vet
	go vet ./...

install: build build-daemon ## install client and daemon into GOPATH/bin
	go install -ldflags='$(LDFLAGS)' ./cmd/gobroom
	go install -ldflags='$(LDFLAGS)' ./cmd/gobroomd

install-all: install ## install binaries and user systemd unit
	mkdir -p ~/.config/systemd/user
	cp dist/gobroomd.service ~/.config/systemd/user/gobroomd.service
	systemctl --user daemon-reload
	systemctl --user enable gobroomd 2>/dev/null || true
	systemctl --user restart gobroomd
	@for attempt in $$(seq 1 50); do \
		if test -S /run/user/$$(id -u)/gobroom.sock; then exit 0; fi; \
		sleep 0.1; \
	done; \
	systemctl --user status gobroomd --no-pager -l; \
	echo "gobroomd IPC socket did not become ready" >&2; exit 1

clean: ## remove local binaries
	rm -f $(BIN) $(DAEMON)

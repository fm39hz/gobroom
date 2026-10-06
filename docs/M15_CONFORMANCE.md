# M15 architecture conformance evidence

M15 asks whether representative new behaviors compose through stable
extensions—not whether every upstream has already been implemented. Run the
executable gate with:

```sh
make test-conformance
```

The gate exercises the following change simulations:

| Change simulation | Executable evidence |
|---|---|
| API-key provider, `/models`, operation bindings and reusable primitives | `TestGenericProviderManifestBindsProtocolsAndSemanticTasks`; daemon manifest/discovery path in `TestDaemonPersistsManifestClassifiedQuotaEvidenceAcrossRestart` |
| Provider-specific OAuth/device auth flow | `TestProviderSpecificDeviceOAuthFlowUsesGenericAuthExtensionContract`; `TestDefinitionBuildsConfiguredOAuthAuthFlow` |
| Safe provider configuration metadata for generic control clients and schema-driven TUI auth forms | `TestDefinitionCatalogExposesGenericSetupMetadataWithoutSecrets`; `TestProviderDefinitionMetadataIsAvailableThroughGenericControlAPI`; daemon IPC catalog assertion in `TestIPCControlCRUDUsesDaemonServices`; `TestConnectionFormUsesProviderAuthSetupSchema` |
| New ingress operation and provider operation binding | `TestNamespacedOperationIngressRegistersWithoutServerRouteBranch`; `TestRuntimeBindingAcceptsNewNamespacedOperationWithoutOperationSwitch` |
| New capability evaluator and route eligibility | `TestNewFeatureExtensionNegotiatesTypedConstraintsWithoutKernelBranch`; `TestKernelRunsOnlyRouteWhoseRegisteredFeatureEvaluatorAcceptsRequest` |
| New fallback strategy | `TestNewStrategyExtensionAppearsInCatalogAndSchedulerWithoutKernelBranch` |
| Request/response transform extension | `TestRequestTransformRegistryRunsBeforeKernelRequirementsAndProviderEncoding`; `TestRuntimeBindingExecutesSelectedEndpointAndCodecs` |
| Provider decoder → semantic events → client renderer, including commit boundary | `TestRuntimeBindingExecutesSelectedEndpointAndCodecs`; `TestComposedSemanticResponsePipelineRetriesOnlyBeforeRendererCommit` |
| New quota/error envelope and restart-persistent routing effect | `TestDaemonPersistsManifestClassifiedQuotaEvidenceAcrossRestart` |

The provider definition catalog contains only display/operation metadata,
primitive references, capabilities and the auth flow's declared setup schema.
It excludes auth options, connection secrets, endpoint options and arbitrary
provider defaults. Both IPC and the optional HTTP control API serve this same
projection; `gobroom providers catalog` exposes it for headless clients.

The implementation adds no provider-name branch to the kernel and no
provider-specific persistence field. The existing physical/combo graph,
snapshot lifecycle and request executor are reused by these simulations.
These checks establish the architecture extension gate only. They do not
establish full provider/protocol parity, complete TUI onboarding, or drop-in
replacement status; see the [implementation roadmap](IMPLEMENTATION_PLAN.md)
and [compatibility evidence](COMPATIBILITY_MATRIX.md).

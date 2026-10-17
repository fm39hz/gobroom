# Product progress

Snapshot: 2026-10-17. This is a use-case-level review, not a completion
percentage or a parity claim. The [roadmap](IMPLEMENTATION_PLAN.md) owns
milestone status, [use cases](USE_CASES.md) own product acceptance, and
[M15 conformance](M15_CONFORMANCE.md) records architecture evidence.

## Current product shape

| User journey | What is demonstrated | What is not yet closed |
|---|---|---|
| Set up provider/accounts and obtain models | API-key onboarding, selected-connection endpoint tests, discovery/import review, custom assignments and entitlement fences through TUI/IPC/SQLite/API fixtures. OAuth code/device lifecycle fixtures exist. | Broad live-provider auth acceptance, non-loopback/HTTPS callback integrations and the full credential lifecycle race matrix. |
| Turn sources into Physical models | Typed identity/revision/fidelity, source evidence, capability/limit projections, conservative grouping and model policies. | Whole-catalog review/split UX, stronger identity evidence and convenient capability comparison across sources. |
| Build role Combos and expose selected names | Ordered typed members, registry-backed strategy/options, source-prefix filtering, exposure and `/v1/models` have onboarding and editor acceptance fixtures. | Complete interactive/headless acceptance for every management action, including Physical exposure through the UI. |
| Consume the provider endpoint | Chat has fake verticals and a limited live acceptance history. Responses/Anthropic/Gemini, tools, JSON/SSE and committed-stream fallback have many deterministic IR fixtures. | The complete cross-protocol, reasoning/signature/history/continuity/modality matrix and live harness coverage. A supported vertical is not universal wire compatibility. |
| Run headless and operate through CLI/TUI | Daemon owns state, listeners and workers; Unix IPC lifecycle/ownership, stream-independent control, reload, logs, usage and installed-client smoke tests exist. | Some advanced probes still lack artifact upload from CLI/TUI. Lazygit-like keyboard contracts and automated workflows do not alone establish polished UX for every screen. |
| Route from real health/quota/performance evidence | Passive feedback, reset-aware gates, usage, EWMA, bounded asynchronous persistence/spool and restart fixtures. Exact operation health/quota, workload ranking and affinity have deterministic integration evidence. | More provider-specific evidence shapes and whole-path latency/RSS/load measurements. Go/concurrency tests are not proof of the “nearly invisible” resource target. |
| Configure, move and extend the product | Secret-free bundle export/validate/diff/apply, exact refs, single-writer portability, independently configurable control/data planes, and a separate static module executing JSON/multipart/signing/job fixtures. | Full C1–C6/SG1–SG7 closure; alternate databases, hosted multi-writer sync and direct remote log backends are not shipped features. |

Useful acceptance anchors include
`TestConnectionModelListToPublicComboOnboardingEndToEnd`,
`TestDiscoveredPhysicalComboOnboardingPublishesThroughModelsAPI`,
`TestComboEditorSaveAndExposureReachOpenAIModelsProjection`,
`TestKernelStreamsInterleavedToolLifecycleToAnthropicClient`,
`TestDaemonIPCReloadAndDeviceAuthRemainResponsiveDuringHTTPStream`, and
`TestPublicDaemonHostsExternalProviderModuleAndManifest`.

## Current integration milestone

The operation-aware feedback slice has deterministic end-to-end verification. It adds an
exact operation dimension to health, quota, persistence, workload ranking and
affinity. The external job fixture proves that a cancelled action's
cooldown survives restart without delaying status, and that exhausted create
quota suppresses the affected account without disabling status or legitimate
selection of another eligible account. Full tests, race tests and the existing
conformance subset pass for this slice. Installed-client acceptance is recorded
separately with the milestone's CLI run. This does not close the full M15 gate
or establish every provider's limits/reset semantics.

## Product-level exit gaps

1. Close the full semantic extension gate, rather than only the existing
   composition subset: compatibility, transforms, auth, replay/artifacts,
   reload and portable configuration must compose through the public module.
2. Broaden real provider/harness acceptance with explicit unsupported behavior.
   Provider catalog size and a generic codec do not prove every provider works.
3. Finish everyday management gaps and establish a reproducible performance
   baseline before claiming the core workflow is both polished and ultra-light.

Update this snapshot at integrated milestones and report these journeys
together. State whether evidence is source-level, a deterministic fixture,
an installed-client smoke test or a live provider run. Keep design contracts
separate from demonstrated product behavior.

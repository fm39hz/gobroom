# Hybrid usage acceptance

The approved [spec](superpowers/specs/2026-10-22-usage-contract-design.md) and
[plan](superpowers/plans/2026-10-22-hybrid-usage-implementation.md) separate
common accounting, registered provider detail and opt-in raw fragments.
Tasks 1–7 are verified. Task 8 has retained-bound tests and five-sample scoped
measurements; final integrated gates and combined review are in progress.
This document does not close C1–C6/M15 or claim provider parity.

## Spec-to-test map

| Spec item | Named evidence |
|---|---|
| 1. Presence, subsets, partial/full corrections and attempts | `TestUsagePresenceRoundTripAndDailyCoverage`, `TestUsageSnapshotPartialRetainsAbsentValuesAndFullClearsUnknown`, `TestGeminiNativeSnapshotInvalidationReachesClientAndKernelTerminal`, `TestDecodeAnthropicUsageMapsDocumentedThinkingSubset`, `TestAttemptUsageFinalizesOnceAcrossFailureAndFallback`, `TestAttemptUsageTransportFailureAndCancellationRemainUnknown` |
| 2. Public modules, exact schema/ref, bounds and ownership | `TestExternalUsageExtensionSurvivesRestartAndModuleRemoval`, `TestUsageRequirementsRequireExactRegisteredUsageRefs`, `TestUsageDescriptorRejectsInvalidRegistrationAndEnforcesPayloadBound`, `TestAttemptUsageBoundsIncludeSerializedEnvelopeAndRejectInvalidSiblings`, `TestKernelUsageAdmissionOwnsReportedValues` |
| 3. Optional/required faults and effects | `TestOptionalUsageIssueDoesNotPoisonHealth`, `TestRequiredUsageFailureCannotReplayAcceptedEffect`, `TestDaemonUsageWriteFailureIsVisibleWithoutFailingInference` |
| 4. History without current module/schema interpretation | `TestUsageHistoryRetainsExactDescriptorWithoutModule`, `TestUsageHistoryRejectsIncompleteRecordedSchemaClosure`, `TestUsageExtensionDescriptorAndSchemaConflictsFailClosed`, `TestExternalUsageExtensionSurvivesRestartAndModuleRemoval` |
| 5. Original/client isolation and disclosure | `TestRawUsageStaysOriginalAndNeverReachesClientTransforms`, `TestExternalUsageExtensionSurvivesRestartAndModuleRemoval`, `TestUsageDetailControlAuthAndIndependentPayloadClasses`, `TestUsageDetailReplyIsRejectedAfterSelectionOrPaneChange`, `TestUsageDetailOutputRejectsAllTerminalControls` |
| 6. Opt-in raw, retention, precision and backpressure | `TestUsageRawCaptureIsOptInBoundedAndPrivate`, `TestUsageRetentionPreservesExactAndNewerNanosecondTimestamps`, `TestUsageDetailNeverServesExpiredRawWhileCleanupLags`, `TestDaemonAccountingQueueFailureDoesNotReplayInference`, `TestWorkerDeadlineAbandonsUnwrittenEventsAndReturnsDeadline`, `TestDaemonStopDrainsAcceptedUsageBeforeClosingStore` |
| 7. Typed jobs and independent client-envelope validation | `TestExternalJobLifecycleKeepsEffectsAndHandlesScopedAcrossRestart` and the [typed-jobs evidence](superpowers/plans/2026-10-22-typed-job-results-implementation.md) |
| 8. Retained bounds and scoped resource measurements | `TestAttemptUsageRetainedStateRemainsBoundedAcrossManyFrames`, `TestUsageAccountingBenchmarkBounds`, shared five-sample proxy harness and selected helper benchmarks; see [scope/results](OPERATIONS.md#local-performance-baseline). Final combined review remains pending. |

These are representative named anchors, not a claim that the new module
fixture independently repeats every schema/activation/fault test. Protocol
specific shapes remain decoder implementation work. Unknown future measurements
need a registered schema/interpreter where semantic decisions are required;
registration alone grants no aggregation, pricing, routing or disclosure.

The unfamiliar `search-units` module uses only public APIs and stdlib. Common
zero/unknown and coverage survive IPC/CLI, original output 3 stays distinct from
client output 103, and a precise private decimal survives active-config/module
uninstall and restart. Removing a required module does not make its active
provider/transform configuration runnable: the fixture uninstalls those active
bindings first, leaving historical data untouched.

## Executed gates through Task 7

Main ran root/external regression, root/external vet and architectural
conformance, plus selected three-run race suites. Independent scoped reviews
and fixes are recorded in Git and the plan. The final whole-change review is
not yet complete.

After `make install-all`, the explicit installed gate was:

```sh
GOCACHE=/tmp/gobroom-go-cache make test-installed-usage \
  GOBROOM_TEST_CLI=/home/fm39hz/go/bin/gobroom
```

It passed five root installed CLI tests and the separate-module
`TestInstalledCLIUsagePreservesPresenceAndPrivacy`, each three race runs.
An unset, relative, missing or non-executable binary path fails before Go;
ordinary `make test` alone does not prove installed acceptance. Main daemon
status was `ok`, control/serving snapshot 506; configuration and credentials
had the same pre/post fingerprint, and real usage rows stayed 0/0. All feature
traffic used temporary hosts and fake upstreams, with no live provider calls.

The initial nullable-contract cutover discarded 4 ambiguous events and 3 daily
rows under the approved pre-release reset policy. Later checkpoints discarded
no additional history. There is no compatibility reader for replaced formats.

## Whole-product boundaries

- Provider/accounts/discovery: existing deterministic onboarding and lease
  fixtures remain; these changes do not add live-provider auth breadth.
- Physical models/role Combos: existing graph, filtering, exposure and editor
  evidence remains; whole-catalog split/review and polished UI breadth are open.
- Client consumption: documented protocol usage normalization is covered, not
  universal reasoning/signature/history/modality or live-harness compatibility.
- Passive routing/accounting: each real dispatch has bounded original usage;
  optional telemetry faults do not retry inference or poison provider health.
- Operations/privacy: IPC ownership, independent HTTP control auth, detail
  grants, terminal sanitization, retention and explicit queue-loss visibility
  are implemented. Ordinary session-save failure visibility remains open.
- Extension portability: unfamiliar usage and typed jobs have independent
  module proofs. Alternate databases, hosted sync and direct remote log sinks
  are not shipped. This is not a claim that every remaining C1–C6 clause is closed.

Five-sample ordinary JSON/SSE and extension/raw-helper measurements now exist,
with the exact source/harness, zero proxy admission loss, and an allocation
increase that is not called negligible. The raw samples and qualitative clone
profile are linked from [operations](OPERATIONS.md#local-performance-baseline).
These do not establish extension-bearing full-proxy resource costs, isolated
daemon RSS, sustained p99 or a comparative 9router load result. Those remain
product-performance work; helper tests are not evidence of a universal target.

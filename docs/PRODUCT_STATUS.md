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
| Run headless and operate through CLI/TUI | Daemon owns state, listeners and workers; Unix IPC lifecycle/ownership, stream-independent control, reload, logs, usage and installed-client smoke tests exist. | CLI/TUI operation probes now stream declared file artifacts through Unix IPC with bounded storage, interpreter validation and cleanup fixtures. Lazygit-like keyboard contracts and automated workflows do not alone establish polished UX for every screen. |
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

The M7/SG2 credential lifecycle slice replaces secret-equality guards with a
durable, non-exported credential revision. Setup/device completion and refresh
share a context-aware CAS; same-secret resave, A → B → A, disabled/re-enabled
and recreated accounts cannot accept stale tokens. Resolve/refresh use caller
and daemon cancellation; waiting is per connection through the existing
`x/sync/semaphore`, with idle lock entries reclaimed. Focused repeated race
tests cover cancellation, late success, generation changes and eight callers
sharing one provider refresh. A separate-module daemon fixture implements its
own device auth via the public API, rejects a token arriving after a control
update, then successfully probes with the newer credential. This closes a
public auth-type export gap without provider-name branches in the kernel.
Explicit OAuth refresh also replaces a rejected token regardless of recorded
expiry, using the pinned `x/oauth2` grant implementation rather than cached-token
reuse or a handwritten refresh exchange.
It does not close live OAuth acceptance, every lifecycle race, or post-commit
route/health reconciliation failure acceptance.
Implementation checkpoint: `2abe99c`. Full tests/vet, the conformance composition
suite and repeated focused race tests passed; the explicit-refresh fix was
additionally followed by full tests/vet and provider/external-module race runs.
After `make install-all`, the installed CLI passed device-start → connection
update → failed stale-device completion → successful operation probe with the
new credential. The main service reported `ok` at snapshot 464; installed
config export → validate passed (bundle v9). These checks use fake providers,
not live accounts, and do not close full M7 or M15.

Previous integrated milestone:

The M2 operation-probe slice now carries local file artifacts from CLI/TUI
over Unix IPC without requiring a public listener. A separate provider-module
fixture verifies exact bytes/MIME, interpreter rejection before dispatch,
empty spool after success/failure and no catalog/health/quota/usage writes.
Transport fixtures cover metadata-first streaming, local-path isolation,
JSON-only RPC and cancellation. This closes the missing upload mechanism,
not a file-picker UX, broad live-provider acceptance or the full M2/M15 gate.
Implementation checkpoint: `af75d2e`. Full tests/vet, current conformance and
repeated focused race tests passed, including the separate-module upload
fixture. After `make install-all`, the installed CLI uploaded the WAV through
the Unix-only fixture daemon and received HTTP-200 metadata; rejected inputs
left no spool or operational state. The main daemon reported `ok` at snapshot
463 and installed config export → validate passed (bundle v9). These are
local fake-provider checks, not live-provider artifact acceptance.

Previous integrated milestone:

The response-terminal slice has deterministic verification: ordinary chat, cross-protocol
streaming and extension operations now share explicit fault origin and a staged
completion boundary. Client/transform/render faults must not poison provider
health or manufacture a successful usage record; provider token/cost evidence
must remain available when client delivery fails. This improves consume-API
correctness and operational accounting, not provider catalog breadth or model
management UX. Full tests/vet, repeated race tests and the current conformance
subset pass. The concurrent HTTP fixture checks that actual client disconnect
does not create provider-health or success/performance observations. Standard
context causes distinguish caller deadlines from operation execution budgets.
Implementation checkpoint: `8c83070`. After `make install-all`, the installed
CLI status/catalog/health/quota fixture against the external module passed,
the main service reported `ok`, and config export → validate passed. These
installed checks complement the targeted HTTP disconnect/fault fixtures;
they do not turn every fault case into a live-provider run. Full M15 remains pending.

Previous integrated milestone:

The operation-aware feedback slice has deterministic end-to-end verification. It adds an
exact operation dimension to health, quota, persistence, workload ranking and
affinity. The external job fixture proves that a cancelled action's
cooldown survives restart without delaying status, and that exhausted create
quota suppresses the affected account without disabling status or legitimate
selection of another eligible account. Full tests, race tests and the existing
conformance subset pass for this slice (`9492f6d`). After `make install-all`,
the installed CLI also passed status/catalog/health/quota acceptance against
the external module's restarted daemon; the main daemon reported `ok` and its
configuration export → validate round trip passed. This does not close the full M15 gate
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

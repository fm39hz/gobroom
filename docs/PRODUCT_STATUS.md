# Product progress

Snapshot: 2026-10-22. This is a use-case-level review, not a completion
percentage or a parity claim. The [roadmap](IMPLEMENTATION_PLAN.md) owns
milestone status, [use cases](USE_CASES.md) own product acceptance, and
[M15 conformance](M15_CONFORMANCE.md) records architecture evidence.

## Current product shape

| User journey | What is demonstrated | What is not yet closed |
|---|---|---|
| Set up provider/accounts and obtain models | API-key onboarding, selected-connection endpoint tests, discovery/import review, custom assignments and entitlement fences through TUI/IPC/SQLite/API fixtures. Code/device setup shares the action coordinator; CLI and TUI can submit registered schema actions. Inference, quota, discovery and probes share scoped leases. Discovery import atomically fences catalog/evidence/health writes. | Broad live-provider auth acceptance, complete lifecycle/fault composition and polished acceptance across all provider schemas. |
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

The core architecture audit found a real extension gap, not merely another
provider implementation to copy: `UsageEvent` has no schema-bound dimension
arm for cache/reasoning/other measurements. Native passthrough can preserve raw
wire fields, but that is not normalized accounting or cross-protocol support.
The jobs example transports JSON through text, but that does not prove a kernel
gap: renderers already receive schema-validated artifact values. Projectors see
metadata only and constrain a logical result, not the exact client wire schema.
A module-only typed-result fixture should be attempted before changing that
interface. The catalog part of C1 is implemented; usage-envelope closure remains
reopened and public-contract design approval is pending.

The bounded session fix now returns an error on persistence queue overflow,
while preserving live memory continuity and the drop counter. Kernel handling
of that error and asynchronous session-write visibility remain open.

Usage persistence now receives context through the existing atomic event/daily
transaction. It is independent of request cancellation and provider health.
The daemon owns producer close, a configurable cooperative drain budget, worker
join, and the retained Stop/Wait result. IPC and HTTP status share typed
received/persisted/failed/abandoned/queue-dropped counters. Lost acknowledgement
evidence remains degraded after a later successful write; raw database errors
are not exposed. Failed writes are not automatically retried.

The real-daemon fault fixture rejects a usage write while inference succeeds,
checks IPC/HTTP status and private-error-safe logs, restores storage and verifies
the next inference succeeds without provider retry. Worker/deadline fixtures use
standard contexts, channels and virtual time; cancellation-before-admission and
event/daily rollback have store tests. A cooperative budget is not a hard
wall-clock guarantee for arbitrary storage or all daemon workers.

C2 fallback accounting now has an exact selected-plan/usage assertion for both
compatibility exclusion and confirmed upstream rejection. Invocation, model,
route, provider and connection transforms run at their declared scopes; selected
steps occur once, rejected/skipped state does not leak. This required no new
routing logic or provider-specific core branch.

## Verified foundation

- Provider/account setup, discovery/custom assignment, Physical grouping and
  ordered role Combos reach selected public model names through normal control
  and serving boundaries. The table above owns remaining user-journey gaps.
- Exact compiled module/contract refs, schemas, compatibility/loss policy,
  operations/artifacts and auth actions share the frozen extension catalog.
  Independent modules execute through public provider/daemon APIs.
- Finite SG5 transform and SG6 replay boundaries have named conformance tests.
  Finite SG7 config isolation includes rejected apply during held SSE,
  secret/runtime-state-free export/import and exact CLI JSON numbers.
- Session preload independently bounds newest scoped state and persisted-time
  TTL. Provider-expired updates use ordered clear markers; graceful restart
  does not revive an older parent. Usage admission and close are synchronized,
  and accepted event drain completes before repository close.
- The measured performance fixture is reproducible with `make perf`.
  [Operations](OPERATIONS.md#local-performance-baseline) records its measured
  source checkpoint and limits; it does not establish current isolated RSS,
  sustained tail latency or the whole-product resource target.

Detailed named evidence lives in [M15 conformance](M15_CONFORMANCE.md).
Historical installation/version records remain in Git and that evidence file;
this overview is current state, not an append-only installation log.

## Latest acceptance

Installed checkpoint `103dbbc`, audit correction `2b9dc90`: the new overflow
regression was observed red before the fix and green afterward. Full root/
external regression, vet, conformance and three session-store race runs passed;
independent review found no critical/important issue in the bounded change.
After `make install-all`, three installed-CLI continuity/restart race runs passed.
The queue-overflow error itself is unit/race evidence, not CLI observability:
the kernel still ignores save errors. Main control=serving=498, status=ok;
secret-free bundle v9 validation passed. Real configuration/credentials were
preserved, and tests used fake upstreams. The audit gaps above remain open.

## Next core work

1. Review schema-bound usage extensions before claiming provider catch-up needs
   no shared contract change. Keep normalized measures distinct from raw usage,
   including unit/subset, unknown-vs-zero and snapshot/delta semantics.
2. Prove the module-only artifact/renderer path for typed job results, replacing
   the example's text tunnel before assuming a new kernel payload arm is needed.
3. Finish ordinary session persistence overflow/failure observability:
   `Cache.Save` now reports queue rejection, but the kernel ignores save errors
   and asynchronous write failures are not surfaced. Do not confuse
   reliable invalidation acknowledgement with ordinary asynchronous saves.
4. Complete a clause-by-clause C1–C6 audit against ordinary public-module/control/
   serving boundaries. Provider-specific codecs, envelopes and live-harness
   breadth are implementation/product acceptance, not an undefined universal
   core matrix.
5. Finish explicit persistence-failure durability and whole-daemon shutdown
   obligations. Usage counters/cooperative cancellation are not a durable spool
   or a sandbox for uncooperative module code.

## Product-level exit gaps

1. Close the full semantic extension gate, rather than only the existing
   composition subset: compatibility, transforms, auth, replay/artifacts,
   reload and portable configuration must compose through the public module.
2. Broaden real provider/harness acceptance with explicit unsupported behavior.
   Provider catalog size and a generic codec do not prove every provider works.
3. Finish everyday management gaps and extend reproducible measurements to
   isolated idle/loaded RSS and sustained tail latency before claiming the
   core workflow is both polished and ultra-light.

Update this snapshot at integrated milestones and report these journeys
together. State whether evidence is source-level, a deterministic fixture,
an installed-client smoke test or a live provider run. Keep design contracts
separate from demonstrated product behavior.

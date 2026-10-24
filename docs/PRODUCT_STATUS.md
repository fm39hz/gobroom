# Product progress

Snapshot: 2026-10-24. This is a use-case-level review, not a completion
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

The approved [hybrid usage plan](superpowers/plans/2026-10-22-hybrid-usage-implementation.md)
is implemented and verified against its [specification](superpowers/specs/2026-10-22-usage-contract-design.md).
The frozen catalog now supports bounded, private-by-default, exact-schema usage
values. The common envelope carries nullable input/output/cache/reasoning
counters, cost and throughput, plus registered values and bounded diagnostics.
Partial observations preserve absent fields; full snapshots can retract a
previously known quantity. Native normalization and client rendering share
those semantics. SQLite and daily summaries preserve unknown versus reported
zero and expose reporting coverage; TUI uses that distinction too.

Each actual upstream dispatch now has host-owned invocation/attempt identity
and one final accounting record, including rejected, failed and cancelled
dispatches. Sources receive bounded response/error evidence before release;
required accounting gates completion without granting replay or poisoning
health. Direct callbacks and aggregate partial updates share admission bounds.
Protocol bindings now preserve documented cache/thinking semantics and private
reported charges without inventing currency or treating them as estimates.
Historical extension values retain their immutable data descriptor, exact
schema documents and dependency pins after module removal. Ordinary lists
stay payload-free. Bounded opt-in raw retention, independently authorized detail
access and separate public-module inference/history/CLI acceptance are verified.
Scoped resource measurement and final combined review passed; C1 is not closed.
The [usage acceptance map](USAGE_ACCEPTANCE.md) records the spec's eight items
and distinguishes deterministic, installed-client and unmeasured boundaries.
Registered values do not automatically become aggregation, pricing or routing
inputs. Native passthrough alone is not normalized accounting.

The [typed-jobs plan](superpowers/plans/2026-10-22-typed-job-results-implementation.md)
has verified module-owned result artifacts and independently validated client
envelopes. It required a generic correction to remove the unintended text-event
requirement for non-chat operations. Projectors still consume artifact metadata,
while renderers receive schema-validated values; no second payload system or
direct projector access was introduced.

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

Task 8 checkpoint `fd33fb0` / `1be757f` / `1a642b1` completes both approved
usage and typed-jobs plans. All eight spec items have named evidence in the
[acceptance map](USAGE_ACCEPTANCE.md). Paired actual JSON/SSE extension proxy
samples now supplement helper costs; a small private extension adds about
30 KiB and 500 allocations/request in this workload. This is not negligible
overhead or proof of the ultra-light target. Standard three-second header
deadlines fix the reproduced idle-connection shutdown mechanism without a
body/stream deadline; the exact cause of all old intermittent failures is not
claimed. Full regression, root/external vet, conformance and final installed
gates passed. The entire external suite with installed CLI and race count 3
passed in 139.466s, including typed jobs. Final combined/scoped review has no
remaining findings. Main status=ok, control=serving=512, config validates,
fingerprint unchanged and usage rows 0/0. No live provider calls.

Earlier integration checkpoints:

Public usage-module checkpoint `71e66f4` / `881f453`: a separate module adds
private search units through existing codecs and public registration, persists
exact data contracts, and reads history after active-config/module removal.
Provider output 3 remains distinct from client-transformed output 103. Real
SQLite blocking/full-queue acceptance proves one inference with an explicitly
observed accounting drop, followed by a complete accepted-record drain.
Root/external regression, vet, conformance and selected three-run race passed.
After installation, `make test-installed-usage` passed six named CLI fixtures
across root/external modules, three race runs each. Main status=ok,
control=serving=506; config/credential fingerprint unchanged, usage rows 0/0.
Task 8 and final combined review remained open at that checkpoint. No live upstream traffic.

Capture/disclosure checkpoint `0bcfe20` / `767bab1` / `9b0e8e9`: bounded
opt-in raw usage, independently granted detail classes, authenticated HTTP
control, explicit CLI/TUI detail views and exact local retention are verified.
Full root/external regression, vet and conformance passed, with scoped review
fixes and selected three-run race suites. After `make install-all`, installed
CLI raw/detail, presence, attempt identity, historical reads and reported-charge
acceptance each passed three race runs. Main status=ok, control=serving=505,
zero persistence failures; configuration/credential fingerprint unchanged and
usage rows remained 0/0. Capture and detail grants remain off by default.
No live provider traffic or additional history loss occurred.

One initial full external run timed out at the existing HTTPS callback test's
Stop deadline. Its untouched test passed 33 subsequent focused repetitions and
later full regression/conformance passed. The cause is not established; no
shutdown fix is claimed. Task 7 public-module inference proof and Task 8
resource measurements remained open at that checkpoint.

Earlier verified protocol/history acceptance:

Protocol/history checkpoint `d2c587c` / `29ebd03`, inference acceptance
`2e8e5ed`: full root/external regression, vet, selected three-run race suites,
independent review and scoped fixes passed. Full conformance rerun passed after
one external-suite failure that did not reproduce in a direct run; its first
output was truncated, so no exact cause is claimed.

After `make install-all`, explicit installed CLI tests each passed three race
runs for charge JSON/SSE inference, history after module removal, per-dispatch
identity/fallback and unknown versus zero. Each JSON/SSE invocation retained
its own precise amount or explicit zero; ordinary controls exposed neither
extension values nor credentials. Installed public-module regressions for typed
jobs, catalog, concurrent JSON/SSE and response transforms also passed three
race runs. Main status=ok, control=serving=504, persistence failures=0;
configuration/credential fingerprint unchanged and usage rows 0/0 across the
upgrade. No live provider traffic or additional history loss occurred.

The initial nullable-usage cutover discarded 4 ambiguous events and 3 daily
rows under the pre-release reset policy, preserving configuration/credentials.
Exact earlier catalog, presence, typed-jobs and session-store acceptance records
are retained in [M15 conformance](M15_CONFORMANCE.md) and the linked task plans.
Typed jobs is an extension proof, not a hosted job service in the standard
daemon. Ordinary session-save failure visibility remains open despite verified
queue rejection and invalidation behavior.

## Next core work

The [finite C1–C6 audit](ARCHITECTURE_AUDIT_2026-10-24.md) identifies a concrete
next architectural implementation gap: registered client-specific precommit
failure projection and safe handling of ordinary ingress error text. It also
separates the remaining named public-boundary proofs from provider breadth.

1. Finish ordinary session persistence overflow/failure observability:
   `Cache.Save` now reports queue rejection, but the kernel ignores save errors
   and asynchronous write failures are not surfaced. Do not confuse
   reliable invalidation acknowledgement with ordinary asynchronous saves.
2. Complete a clause-by-clause C1–C6 audit against ordinary public-module/control/
   serving boundaries. Provider-specific codecs, envelopes and live-harness
   breadth are implementation/product acceptance, not an undefined universal
   core matrix. Usage and typed jobs now have finite combined acceptance, not
   a universal no-kernel-change catch-up proof.
3. Investigate repeated internal compatibility/usage copies while preserving
   ownership fences, then measure isolated RSS and sustained tail latency.
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

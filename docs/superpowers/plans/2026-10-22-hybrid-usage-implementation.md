# Hybrid Usage Accounting Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Carry trustworthy, presence-aware attempt accounting and bounded provider extensions through inference, persistence and management without provider-specific kernel changes.

**Architecture:** Keep common semantics typed in the existing `kernel.UsageEvent`; register unfamiliar values in the frozen extension catalog. Decode original accounting before client transforms, finalize once per actual dispatch, persist with immutable historical descriptors, and project only explicitly understood values. Do not build a telemetry-expression engine.

**Tech Stack:** Existing Go 1.27, `encoding/json`, `database/sql`, contexts/channels, `github.com/santhosh-tekuri/jsonschema/v6 v6.0.3`, `modernc.org/sqlite v1.59.0`, Cobra and the existing Charm TUI. No new dependency is required by this plan; existing versions are repository pins, not a claim about latest upstream releases.

**Spec:** [Approved hybrid usage design](../specs/2026-10-22-usage-contract-design.md). Spec and implementation plan approved by the user on 2026-10-22; execution started. Only verified checkboxes are complete.

## Global Constraints

- Default limits: 32 extension values and 64 KiB of encoded extension data per attempt, including reference/envelope overhead.
- Default capture ceiling is 256 KiB per attempt; raw provider usage is opt-in and disabled by default.
- Host overrides remain finite and positive; enclosing operation/event/output limits still apply.
- Unknown is not zero; cache/reasoning details are subsets of canonical totals.
- Descriptor registration grants no automatic aggregation, pricing, routing or public disclosure.
- Preserve credentials and connection configuration. Replace affected pre-release contracts without compatibility shims; never recreate the whole database.
- Only the main agent runs Go, gofmt, tests, builds, vet, installation and service actions. Luna medium workers may edit only assigned disjoint files; they may not commit.
- No real provider calls, credential changes or migration of live setup for acceptance. Use fake upstreams and temporary DB/IPC paths.
- After each integrated runtime checkpoint: main runs `make install-all`, then tests the corresponding feature with the installed CLI against a temporary daemon.

## Review Focus

1. An explicit zero after a positive snapshot must replace that field; an absent field must not erase it (Task 2).
2. HTTP rejection/auth retry and fallback must not lose a billable observation or count one dispatch twice (Task 3).
3. Uninstalling a module must leave bounded historical records readable, without loading its code (Task 5).
4. Valid schema payloads may still contain private strings; ordinary control/log/response paths must not disclose them (Task 6).
5. A full accounting queue or cancellation during drain must remain observable without retrying the inference (Task 7).

## Boundaries and ownership

Create focused files instead of growing `kernel.go`, `store.go` or `primitives.go` further. Existing files get wiring or replacement of their old usage definitions, not unrelated refactors.

| Unit | Main files | Responsibility |
|---|---|---|
| Registered value contract | new `internal/extensions/usage.go`, existing `catalog.go` | Descriptors, exact schema binding, validation and immutable history metadata |
| Common accounting | new `internal/kernel/usage.go`, existing `contract.go` | Presence, clone, merge, validation and safe diagnostics |
| Attempt lifecycle | new `internal/kernel/usage_attempt.go`, existing `kernel.go`, `pipeline.go` | Actual-dispatch identity, evidence, finalization and transform isolation |
| Wire conversion | new usage files in existing adapter packages | Provider normalization and client projection, not kernel switches |
| Durable records | new `internal/store/usage.go`, `usage_schema.go`, existing `runtime_statements.go` | Event/daily atomicity, descriptors, coverage and retention |
| Host policy/control | new `internal/daemon/usage_policy.go`, existing daemon/CLI/TUI | Bounds, retention, authorized detail views and status |
| Independent typed jobs | [separate plan](2026-10-22-typed-job-results-implementation.md) | Registered result artifacts; no usage dependency |

Use `pkg/provider` aliases for module-facing types. Do not create a second public usage model or a duplicate `internal/usage.Event`; remove that unused model if no production caller exists after tracing references. `internal/usage` remains the worker/repository lifecycle package.

## Task 1: Registered, bounded usage details

**Files:** Create `internal/extensions/usage.go`, `usage_test.go`; modify `catalog.go`, `catalog_test.go`, `internal/provider/primitives.go`, `pkg/provider/provider.go`.

**Interfaces:** `UsageKind = "usage-value"`; `UsageValue{TypeRef Ref, Value json.RawMessage}`; `UsagePolicy{Unit string, Disclosure UsageDisclosure, MaxValueBytes int64}` attached as `Descriptor.UsagePolicy`. `UsageDisclosure` has private and explicitly management-visible modes, default private. `Snapshot.ValidateUsageValue(UsageValue) error` and `Snapshot.UsageDescriptor(Ref) (UsageDescriptorSnapshot, bool)` reuse registered schemas. `UsageDescriptorSnapshot` holds the exact descriptor identity, schema identity/digest, label, description, unit and classification; no factory or executable payload. Export aliases through `pkg/provider`.

Test assertion form: `if err := snapshot.ValidateUsageValue(wrongVersion); err == nil { t.Fatal("accepted unregistered version") }`.

- [ ] Add `TestUsageValueBindsExactSchemaAndOwnsMetadata`: register two versions; wrong/missing ref, wrong schema, invalid policy and mutated caller buffers must not pass. Expected assertion: `ValidateUsageValue(wrongVersion) != nil`, while the frozen original remains unchanged.
- [ ] Main runs `GOCACHE=/tmp/gobroom-go-cache go test ./internal/extensions ./internal/provider -run 'UsageValue|UsageDescriptor' -count=1`; observe missing-contract failure before implementation.
- [ ] Implement descriptor validation/clone/fingerprint/dependency handling and public aliases using the existing schema compiler. Include schema dependencies in locks and immutable descriptor snapshots; no new registry parallel to the catalog. Bound descriptor strings/documents under existing catalog resource validation.
- [ ] Main reruns the focused tests plus catalog/dependency-lock regressions; expected PASS. Confirm registering a value does not run inference, aggregation or policy code.
- [ ] Main installs and uses `gobroom --ipc <temporary-socket> extensions catalog` against a temporary module-enabled host; assert the exact usage descriptor/schema appears and contains no runtime value or secret.
- [ ] Commit `feat(extensions): register schema-bound usage values`.

## Task 2: Atomic presence-aware common-contract cutover

**Files:** Create `internal/kernel/usage.go`, `usage_test.go`, `internal/store/usage_schema.go`, `usage_presence_test.go`; modify usage definitions/call sites in `contract.go`, `pipeline.go`, `response_failure.go`, `operationEventPayload`, adapters/renderers, `internal/provider/operation_primitives.go`, store statements/records, `internal/runtime/performance.go`, `internal/tui/rows.go`, their tests and external examples. Main inventories all `UsageEvent` and `normalize.Usage` callers with `rg` before assigning files.

**Interfaces:** Keep existing common names but use `*int64` for input/output/cache-read/cache-write/reasoning counters and `*float64` for estimated cost and output-tokens/sec. Add `UsageEvent.Extensions []extensions.UsageValue`, `AccountingIssues []UsageIssue`, where `UsageIssue{Code string, TypeRef extensions.Ref}` contains no raw error text. Methods: `UsageEvent.Clone() UsageEvent`, `UsageEvent.HasReportedUsage() bool`, `UsageEvent.ValidateCommon() error`; `MergeProviderUsage(current, update UsageEvent) UsageEvent` accepts already-normalized partial snapshots, never raw deltas. No generic delta inference.

Define `kernel.UsageLimits{MaxValues int, MaxExtensionBytes int64}` with defaults 32 and 65536; Task 3 uses it and Task 6 binds host overrides. Coalesce issue codes/type refs with a ceiling of 32 retained issues plus one overflow marker; no repeated-frame diagnostic slice may grow without bound.

Test assertion form: `if got.InputTokens == nil || *got.InputTokens != 0 || got.OutputTokens == nil || *got.OutputTokens != 9 { t.Fatal("lost zero correction or prior output") }`.

- [ ] Add `TestUsagePresenceMergeAndClone` with explicit assertions: nil stays unknown, `ptr(0)` is reported, update `ptr(0)` replaces `ptr(7)`, absent output preserves earlier output, extension-only is reported, mutation after merge/clone cannot affect the original, negative/nonfinite known values fail validation. `ptr` is a test helper, not a new optional-value framework.
- [ ] Main runs `go test ./internal/kernel -run '^TestUsagePresenceMergeAndClone$' -count=1` with the task GOCACHE; observe RED.
- [ ] Perform one atomic source cutover: pointers throughout JSON decode/render/store/query/TUI; SQL NULL represents unknown. Bind pointers from actual wire presence, not `ptr(oldZero)` indiscriminately. Remove old numeric/zero-based accounting readers. Use `sql.NullInt64`/`sql.NullFloat64` when reading; preserve zero on write. Throughput records absence unless its required quantities are known.
- [ ] Move usage schema changes into a targeted transaction: detect the old usage-table shape, replace only disposable usage event/daily tables, then record the new schema shape. Never coerce old ambiguous zeros into known values or retain an old reader. Test exact credentials, provider/connection/model config and unrelated tables survive. Report discarded usage history when this transition actually occurs.
- [ ] Bump static module host contract 10 to 11 atomically with affected API consumers, lock fixtures and schemas. Update affected event/result contracts deliberately rather than accepting old wire aliases. Do not bump config bundle v9 solely because runtime usage changed; change bundle version only if its actual format changes. All built-in/external code must compile in this commit.
- [ ] Main runs full `make test` and `make vet`; then `make install-all` and installed CLI `status`, `usage`, `usage-summary` against a temporary host containing fake reported-zero and unknown usage. Both cases must remain distinguishable. Commit `feat(usage): replace ambiguous counters with explicit presence`.

This is the shared cutover checkpoint: workers can prepare disjoint patches concurrently, but no intermediate broken contract is committed or installed. Main resolves shared files and runs verification only after writers stop.

## Task 3: Per-dispatch lifecycle, error evidence and completion policy

**Files:** Create `internal/kernel/usage_attempt.go`, `usage_attempt_test.go`; modify `kernel.go`, `pipeline.go`, `contract.go`, `internal/operations/registry.go`, `internal/extensions/catalog.go`, `internal/provider/operation_primitives.go`, daemon runtime wiring and `pkg/provider/provider.go`.

**Interfaces:** Add host-owned `InvocationID` and `AttemptID` to usage records; generate invocation identity once and attempt identity at every actual transport dispatch, including auth/idempotency retries. Use existing stdlib random identity facilities, not route/session strings. Introduce `UsageEvidence{StatusCode int, Headers http.Header, ErrorBody json.RawMessage}` and replace `UsageEnricher.EnrichUsage(context.Context, Route, UsageEvidence, UsageEvent) (UsageEvent, error)`. Error body input stays bounded by the current 64 KiB HTTP-error read, is not automatically persisted, and retains the existing error-classification path. Add `extensions.UsageRequirements{RequireReported bool, RequiredExtensions []Ref}` to operation definition/descriptor and exact dependency validation; default requires no accounting.

Bind `Kernel.UsageLimits` and the frozen usage catalog before execution; no per-request schema compilation. This breaking public interface checkpoint advances host contract 11 to 12 and updates built-in/external consumers together.

Test assertion form: `if len(events) != 2 || events[0].AttemptID == events[1].AttemptID || upstreamCalls != 2 { t.Fatal("lost or duplicated dispatch accounting") }`.

- [ ] Add `TestAttemptUsageFinalizesOnceAcrossFailureAndFallback`: distinct dispatches have distinct IDs; callbacks/frame updates do not duplicate records; HTTP-error usage and transport failure remain attributed to the correct attempt; unknown is not synthesized as zero. Add `TestRequiredUsageFailureCannotReplayAcceptedEffect` and `TestOptionalUsageIssueDoesNotPoisonHealth`.
- [ ] Main runs `go test ./internal/kernel ./internal/provider ./internal/operations -run 'AttemptUsage|RequiredUsage|OptionalUsage' -count=1`; observe failures for missing attempt/rejection behavior.
- [ ] Centralize attempt-local merge/finalization in `usage_attempt.go`; include successful, rejected, failed and cancelled actual dispatches, even if counters are unknown. Skipped/ineligible candidates are not attempts. Reuse staged completion and accepted-effect fences. Feed header/error-body evidence to the selected source before its response is released, including before retry/fallback. Do not let parsing an error usage fragment grant replay.
- [ ] Keep accounting validation and diagnostics separate from health. Sanitize invalid optional values, retain valid common counters, mark accounting incomplete. A required-accounting failure fails completion with local/unknown attribution and never retries an accepted effect. Preserve ordinary persisted failed-attempt evidence and distinguish semantic incompleteness from persistence queue failure.
- [ ] Validate and bound all observation entrances before copying/retaining, including direct `OnProviderUsage` callbacks; they must not bypass accounting bounds because canonical event budgeting occurs elsewhere. Keep repeated snapshot storage bounded and respect caller/operation cancellation.
- [ ] Extend clone/transform/budget handling and `operationEventPayload` to explicitly allowed common accounting only; extension/raw disclosure is never implicit. Main runs focused tests, response failure/replay regressions and race coverage. Install and run a new fake-upstream CLI acceptance with one rejected attempt then one success; assert `usage` contains two attempt IDs and no duplicate. Commit `feat(kernel): finalize accounting once per upstream dispatch`.

## Task 4: Protocol normalization and faithful projections

**Files:** Create `internal/adapter/openai/usage.go`, `usage_test.go`, `internal/adapter/anthropic/usage.go`, `usage_test.go`, `internal/adapter/gemini/usage.go`, `usage_test.go`, `internal/adapter/renderers/usage.go`, `usage_test.go`; modify the existing decoder/renderer files and `internal/provider/operation_primitives.go` only where wiring changes.

**Interfaces:** Package-local `decodeOpenAIUsage`, `decodeAnthropicUsage`, `decodeGeminiUsage` each take `json.RawMessage` and return `(kernel.UsageEvent, error)`. Their exact wire DTOs remain package-private. Renderers consume the presence-aware `UsageEvent` from Task 2. Typed native field structs use `json.RawMessage`/`json.Number` when precision must be retained; no string splitting or schema-by-string manipulation.

Test assertion form: `if usage.InputTokens == nil || *usage.InputTokens != 1000 || usage.CachedReadTokens == nil || *usage.CachedReadTokens != 900 { t.Fatal("wrong cache normalization") }`. Use `CachedReadTokens`, `CachedWriteTokens` and `ReasoningTokens` consistently as the new common field names.

- [ ] Add table tests for each JSON/SSE dialect and header source: missing versus zero; cache inclusion/exclusion differences; reasoning as output subset; partial/final reports; corrected snapshots; malformed numeric fields; absent usage at completion. Assert canonical total is 1000 with a cache subset of 900, not 1900, only where the provider fixture establishes the relationship.
- [ ] Main observes RED with `go test ./internal/adapter/... ./internal/provider -run 'Usage' -count=1` before wiring the richer semantics.
- [ ] Verify the meaning of each fixture from local reference source and checked wire samples. Normalize only evidenced disjoint components. Do not infer a Gemini reasoning relationship or provider alias from field names alone. Map known details into canonical counters; unfamiliar registered values remain module-owned.
- [ ] Implement explicit Chat/Responses/Anthropic/Gemini output mappings. Unknown optional details stay absent; required unmappable facets obey existing fidelity/loss policy. Test native passthrough bytes remain unchanged where permitted and transformed accounting cannot alter original accounting.
- [ ] Keep a provider-reported monetary amount distinct from `EstimatedCost`: represent it through a registered reported-charge value with precision-preserving amount, explicitly reported currency when available, and provenance. Never infer currency, settled status or a common daily charge sum from that value.
- [ ] Main runs protocol tests plus kernel cross-protocol suites, installs and tests installed-CLI usage through temporary fake JSON/SSE upstreams. Commit each independently green protocol slice; workers own distinct adapter directories, and main alone owns shared renderer/kernel edits.

## Task 5: Immutable historical descriptors and complete summaries

**Files:** Create `internal/store/usage.go`, `usage_extensions_test.go`, `usage_summary_test.go`; move usage functions from `store.go`; modify `runtime_statements.go`, schema from Task 2 and `internal/usage/worker.go` only for owned-record handling.

**Interfaces:** Preserve `SaveUsageEvent(context.Context, kernel.UsageEvent) error`, `UsageEvents(limit int) ([]UsageRecord, error)` and `UsageSummary(limit int) ([]UsageSummary, error)` for common bounded queries. Add `UsageDetail(ctx context.Context, id int64) (UsageDetailRecord, error)` for gated details. `UsageSummary.Attempts` replaces the misleading request count; each common metric returns its known sum and reported-attempt count. No client-projection table is added.

`Store.SetUsageCatalog(*extensions.Snapshot) error` binds one frozen catalog before the worker starts. Writes obtain exact descriptor/schema metadata there and insert it transactionally when first referenced. Reads require no live catalog, and pruning unreferenced metadata cannot prevent a later valid write from inserting it again. Conflicting identity/digest pairs fail closed.

Test assertion form: `if summary.Attempts != 2 || summary.Input.Reported != 1 || summary.Input.Sum != 0 { t.Fatal("unknown counted as reported zero") }`. Use `UsageMetricSummary{Sum int64, Reported int64}` for token quantities and a separate float-valued estimated-cost summary; extensions have no automatic summary field.

- [ ] Add `TestUsageHistoryRetainsExactDescriptorWithoutModule`, `TestUsageSummaryReportsPartialCoverage` and `TestUsageTransactionRollsBackDetailsAndSummary`. Assertions: one known-zero and one unknown input yield known sum zero with coverage 1/2; cache is not added again; arbitrary extension values do not alter common sums.
- [ ] Main runs `go test ./internal/store -run 'UsageHistory|UsageSummaryReports|UsageTransaction' -count=1`; observe RED.
- [ ] Store descriptor/schema snapshots once by exact identity/digest in data-only tables and usage values by event/descriptor identity. Persist event, details, issue metadata and daily updates in one context-aware transaction. Verify digest collision/mismatched descriptor identity fails rather than reinterpreting history. Do not invoke module factories when reading old records.
- [ ] Keep precision-preserving raw bytes separate from normalized numerics. Prune dependent details and orphaned metadata safely under the same retention transaction; no unbounded metadata accumulation after history pruning. Overflow/cancellation retains existing persistence failure counters.
- [ ] Main runs store/worker cancellation, atomicity and restart tests; install and query CLI common/history summaries after module removal in a temporary daemon. Commit `feat(store): persist versioned usage details and coverage`.

## Task 6: Opt-in raw retention and authorized management views

**Files:** Create `internal/daemon/usage_policy.go`, `usage_policy_test.go`, `internal/kernel/usage_capture.go`, `usage_capture_test.go`, `internal/tui/usage_test.go`, `cmd/gobroom/usage_test.go`; modify host Config/flags, decode hooks, usage IPC handling, API control wiring and TUI rows.

**Interfaces:** `UsagePolicy{MaxValues int, MaxExtensionBytes int64, CaptureRaw bool, MaxRawBytes int64, Retention time.Duration, AllowManagementDetails bool, AllowPrivateDetails bool, AllowRawDetails bool}` on daemon config; defaults 32, 65536, false, 262144, 30 days, false, false, false. Positive finite validation applies before startup. `UsageCapturePolicy` on `StreamHooks` passes enabled capture/bounds to decoders. `RawUsageFragment{ProviderDefinition Ref, API string, Value json.RawMessage}` is original bounded usage JSON only; attempt provenance comes from its event, not payload-controlled IDs. `UsageDetailOptions{IncludeExtensions bool, IncludePrivate bool, IncludeRaw bool}` is the typed control request; each requested class requires its corresponding host grant as well as control authorization. The additive public hook contract advances host 12 to 13 with exact module locks updated in the same checkpoint.

Add `UsageEvent.RawUsage *RawUsageFragment` at this checkpoint, with clone/retention rules identical to owned extension buffers. The repository's separate raw table is queried only by `UsageDetail`; `UsageEvents` never returns its bytes. Declare this payload through the existing public alias boundary, not an untyped `StreamHooks` options map.

Test assertion form: `if bytes.Contains(ordinaryOutput, []byte(privateMarker)) || detailDenied == nil { t.Fatal("usage detail disclosure bypass") }`.

- [ ] Add `TestUsageRawCaptureIsOptInBoundedAndPrivate`: disabled capture never retains raw bytes; enabled capture preserves a number greater than 2^53; oversized/unsafe input yields a sanitized issue, not truncated JSON. Secret-bearing fixture strings must not occur in logs, ordinary usage lists, status, public responses or config exports. Test a valid schema with a private string, not merely an invalid payload.
- [ ] Main runs focused capture/policy/control tests and observes RED before implementing the policy.
- [ ] Implement host-gated capture of a decoder-selected usage fragment, never headers or the whole response. Bound before retaining/copying; use `json.RawMessage`/`json.Number` and decoder-owned typed selection of allowed usage shapes. Unsupported or sensitive raw shapes are omitted with a diagnostic. Do not claim arbitrary JSON can be made safe by generic key-name redaction or sandbox arbitrary statically linked module code.
- [ ] Add an explicit `usage detail <id>` CLI operation and IPC `usage.detail`; authorize before fetching payloads. HTTP control details require the existing control authentication plus the host disclosure grant; ordinary reads exclude details. TUI detail fetch is an explicit user action and renders bounded sanitized text, never terminal escape sequences. Private descriptors stay private unless a separate explicit grant permits that class; enabling raw capture alone never permits disclosure.
- [ ] Run bounded retention pruning at startup and every 15 minutes under daemon cancellation, using the store transaction; retention cannot be disabled while raw capture is enabled. Add equivalent positive host flags and document restart semantics. Verify with virtual time and temporary stores, not sleeps.
- [ ] Main runs policy/privacy/TUI/CLI tests, installs and exercises denied and granted detail operations against temporary daemons. Commit `feat(usage): add bounded private capture and explicit detail access`.

## Task 7: External-module and installed-client acceptance

**Files:** Create `examples/external-provider-module/usage.go`, `usage_test.go`, `internal/daemon/usage_semantics_test.go`; extend `usage_failure_test.go`, `usage_drain_test.go`, `internal/controlplane/bundle_runtime_privacy_test.go`, `Makefile` selectors.

**Interfaces:** A separate statically linked example module uses only `pkg/provider` and `pkg/daemon` to register a typed unfamiliar `search-units` detail, declare dependencies and complete a fake operation. It must not import `internal/` or modify the kernel for that feature.

Test assertion form: `if upstreamCalls != 1 || status.UsagePersistence.QueueDropped == 0 { t.Fatal("queue failure hidden or inference retried") }` for the queue-rejection subcase; storage-failure and deadline subcases assert their own exact counters.

- [ ] Add `TestExternalUsageExtensionSurvivesRestartAndModuleRemoval`, `TestDaemonAccountingQueueFailureDoesNotReplayInference`, and `TestInstalledCLIUsagePreservesPresenceAndPrivacy`. Assert original versus client-transformed quantities, coverage, exact metadata and no automatic sum/pricing of search units.
- [ ] Main runs the focused root and separate-module tests; observe the missing end-to-end behavior before completing the fixtures/wiring.
- [ ] Test invalid required refs/options at activation, runtime optional schema violations, limits just below/at/above bounds, duplicate identities, module buffer mutation, blocked storage, full queue, cancellation/drain and readonly historical access after removing the module. Reuse existing fault-injection and context coordination patterns.
- [ ] Run `GOCACHE=/tmp/gobroom-go-cache make test`, `make vet`, `make test-conformance`, then selected root/external tests with `-race -count=3`. Main runs them sequentially, never concurrently with workers editing.
- [ ] Run `make install-all`; set `GOBROOM_TEST_CLI=/home/fm39hz/go/bin/gobroom` for installed fixtures. Explicitly require the new CLI fixture so optional env-skips cannot masquerade as installed acceptance. Check main daemon status and secret-free config validation without exposing credentials; all feature traffic goes to temporary hosts/fake upstreams.
- [ ] Commit `test(usage): prove external modules and installed control boundaries`.

## Task 8: Performance and whole-product handoff

**Files:** Extend `examples/external-provider-module/proxy_benchmark_test.go`, `Makefile` focused selectors and `docs/OPERATIONS.md`; update `docs/PRODUCT_STATUS.md`, `docs/IMPLEMENTATION_PLAN.md`, `docs/M15_CONFORMANCE.md`, `docs/EXTENSION_CONTRACTS.md`.

**Interfaces:** New benchmarks `BenchmarkUsageCommon`, `BenchmarkUsageExtensions`, `BenchmarkUsageRawCapture` separate pure accounting from existing proxy JSON/SSE benchmarks. Dimensions: disabled capture, small extension, configured ceiling, rejected oversize; all use bounded fixtures.

Bound assertion form: `if retainedBytes > policy.MaxExtensionBytes || admittedOversize { t.Fatal("accounting retention exceeded policy") }`.

- [ ] Add assertions in non-benchmark tests that oversized payloads are rejected before durable admission and that retained state stays within configured bounds. Main runs these first; a benchmark alone does not prove bounded memory.
- [ ] Main captures baseline and post-change samples using the same benchmark source/Go version/settings, `-benchmem -count=5 -cpu=1,4`; compare ordinary proxy paths and extension-bearing paths. Use the existing `make perf BENCHTIME=1s BENCHCPU=1,4` target for the proxy fixture. No fabricated numeric performance threshold; report regressions and investigate them before claiming negligible overhead.
- [ ] Record CPU/allocation results and measurement scope. Do not infer isolated daemon RSS or sustained p99 from combined-process proxy measurements; those product targets remain open unless independently measured.
- [ ] Run full regression/install/installed acceptance after all work, including the independently reviewed typed-jobs plan. Map every spec acceptance item to named tests and actual commands; record unexecuted/live gaps explicitly.
- [ ] Report across onboarding/discovery, Physical/Combo management, client consumption, passive routing/accounting, operations/privacy and extension portability. Update C1 narrowly; do not declare C1–C6/M15 closed from usage tests alone. Commit `docs(usage): record acceptance and measured resource costs`.

## Execution waves and gates

1. Task 1 prepares the catalog contract. Task 2 is one main-owned atomic cutover, with disjoint worker edits coordinated against the same interface definitions.
2. After Task 2, Task 3 (main kernel), Task 4 (one Luna per adapter directory) and Task 5 (one Luna store worker) may proceed concurrently. Main owns cross-boundary integration; no worker runs Go commands.
3. Task 6 follows Tasks 3–5. CLI/TUI test/edit work can be split into disjoint files once daemon DTOs are fixed. Typed jobs is independent except for Task 2's mechanical edits to its existing usage call sites; schedule its writer outside that overlap.
4. Tasks 7–8 follow stopped writers, integration review, full validation and installation. Review correctness and spec conformance per integrated checkpoint; final review checks the whole change, not just the last patch.

Only main commits, and only after fresh validation. Keep task checkboxes honest:
writing a test is not running it, source feasibility is not acceptance, and a
green subset is not whole-product completion. The user already selected
Luna-medium parallel workers with centralized verification; preserve that
execution method after this plan is reviewed.

## Spec-to-task review

| Spec acceptance | Owning tasks |
|---|---|
| Presence, subsets, coverage, stream updates, failed attempts | 2, 3, 4, 5 |
| Public module, exact refs, schema/bounds/ownership | 1, 2, 3, 7 |
| Optional/required accounting faults and replay fences | 3, 7 |
| Restart/module removal, immutable history, no arbitrary aggregation | 5, 7 |
| Provider/client isolation and disclosure | 3, 4, 6, 7 |
| Opt-in raw retention, precision, backpressure/drain | 5, 6, 7 |
| Module-only typed jobs and wire validation | Separate typed-jobs plan |
| CPU/allocations/bounded memory and honest measurement scope | 8 |

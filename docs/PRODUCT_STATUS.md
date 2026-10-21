# Product progress

Snapshot: 2026-10-21. This is a use-case-level review, not a completion
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

The finite SG6 dispatch/cancellation/drain checklist is now exercised. Tests
cover final live authority validation, accepted response before client commit,
already committed output and daemon shutdown during frozen replay preparation.
The retention test found a real gap: time could expire during live validation
after the old retention check. Checking after validation now prevents the second
dispatch. Cancellation never becomes a replay grant or successful usage; known
provider usage is retained as cancelled. Shutdown drains late-returning replay
work before cleanup completes and permits reopening the same DB/socket.

Root regression/vet, three core replay race runs and three independent-module
shutdown race runs passed, including rebinding the actual old data port.
Full conformance passed. Current
production built-ins claim no keyed-idempotency guarantee;
future opt-ins need provider evidence. Provider/harness breadth, catalog UX and
measured latency/RSS remain open; generic SG6 evidence does not close M15.

Installed checkpoint `3b7b34d`: `make install-all` completed and restarted the
user service. Three further independent-module race runs passed with the
installed CLI executing status and shutdown while replay preparation was held.
Shutdown acknowledged, cancelled and drained that work; late success did not
redispatch. The actual data port was rebound and the DB/IPC reopened. Main
daemon status was `ok`, control=serving=488; export → validate returned valid
bundle v9. Real config/credentials were preserved; no live provider was called.

Previous integrated milestone:

Client-visible error identity is now a bounded typed code/type/message value,
separate from private provider evidence and replay policy. It survives standard
Go error wrapping, HTTP executor projection and native/semantic streaming
renderers. Event budgets and immutable transform checks include all fields;
the event no longer accepts a string error shape. Extensions can supply explicit
public metadata without a provider branch in the kernel. Host-v10 requires it.

The independent module daemon vertical compares descriptor identity between
HTTP 409 and committed SSE. HTTP/renderer/core fixtures cover custom metadata,
private wrapper filtering, bounds and no postcommit overwrite. Full root
regression/vet, three focused race runs and conformance (including the complete
independent module suite) passed.
Replay proof still needs the finite
cancellation/dispatch/shutdown cases listed in M15; provider/harness breadth,
catalog UX and measured latency/RSS remain open. This does not close M15.

Installed checkpoint `25184ac`: `make install-all` completed and restarted the
user service. The independent scope vertical passed three further race runs
with the installed CLI, comparing the typed descriptor in HTTP 409 and committed
SSE alongside status, account update, failed usage and durable reset/restart.
Main daemon status was `ok`, control=serving=487; export → validate returned
valid bundle v9. Real config/credentials were preserved; no live provider call
or new dependency was needed.

Previous integrated milestone:

Accepted-response failures now use bounded private decoder evidence and a
response classifier separate from HTTP rejection. Accepted upstream effects do
not become replay grants. OpenAI error payloads are intercepted before raw data
passthrough, failed/incomplete streams never become success through `[DONE]`,
and known usage survives as failed usage. If client output has committed, its
status/content remain and one protocol error is emitted; otherwise a continuity
failure can become a sanitized 409. Invalidation uses the original loaded state,
not IDs or proofs from the failed stream. Host-v9 requires this boundary.

Root regression/vet and three focused kernel/provider/decoder/renderer race runs
passed. The independent module daemon vertical also passed pre/post-commit
errors, partial text, no replay, durable reset and failed usage checks.
Full conformance, including the new response-failure group and complete external
module suite, passed. Provider/harness
breadth, automatic full-history reconstruction and measured latency/RSS remain
open; this does not close C2/SG1/M15.

Installed checkpoint `43c3d24`: `make install-all` completed and restarted the
user service. Three additional independent-module race runs passed with the
installed CLI, including status during the scope workflow, pre/post-commit
accepted-response errors, preserved partial text, no fake completion/replay,
durable invalidation and failed usage through restart. Main daemon status was
`ok`, control=serving=486; config export → validate returned valid bundle v9.
Real configuration/credentials were preserved and no live provider was called.

Previous integrated milestone:

Structured continuity rejection can now invalidate only the state observed by
its failed attempt, preserving concurrent newer success. It has request scope
and cannot trigger auth refresh, quota suppression, model fallback or blind
replay. The API returns a sanitized 409 requesting complete input. Cache reset
uses acknowledged tombstones on the existing ordered worker; SQLite never runs
under the cache lock. Saturation/persistence failure is not reported as success.
Shared typed selectors and schema binding configure provider-specific signals;
the built-in Responses operation declares parent-not-found behavior. Host-v8
requires the new store contract rather than adapting old modules.

The independent module/daemon vertical verifies HTTP 409, no implicit retry,
private-reference redaction and durable reset across restart. Automatic complete
history reconstruction, in-stream provider errors, broader harness/provider
acceptance and measured latency/RSS remain open. Root regression, vet and three
focused kernel/provider/cache/API race runs passed, including identical-state
resaves protected by store versions. Full conformance, including the independent
module suite and the newly added invalidation group, passed. This checkpoint
does not close C2/SG1/M15.

Installed checkpoint `039e971`: `make install-all` completed and restarted the
user service. Three independent-module race runs passed with
`GOBROOM_TEST_CLI=/home/fm39hz/go/bin/gobroom`, including CLI status following
the normalized rejection and later fixture-account replacement. HTTP checks
proved no implicit retry, private-reference redaction and durable reset through
restart. Main daemon status was `ok`, control=serving=485; config export →
validate returned valid bundle v9. No real provider was contacted and real
configuration/credentials were preserved.

Previous integrated milestone:

Provider continuation now uses stable principal and configured-endpoint scope
in addition to exact provider/operation refs, upstream model, connection and
client session. Refresh preserves that scope; account or endpoint replacement
does not inherit response IDs or private proofs. Decoder updates use the
existing hook with private staging, explicit empty-state reset and provider
expiry capped by the cache TTL. SQLite rejects stale queued writes in its write
transaction and discards unsupported cache rows on preload; user configuration
and credentials are preserved. Host contract v7 requires the new semantics.

`TestExternalResponsesSessionScopeFollowsPrincipalAndEndpoint` exercises the
public module/daemon API and Responses endpoint through decode failure,
expiry/reset, auth recovery, principal/endpoint replacement and durable restart.
It passed three race runs. These expiry/reset signals belong to a fixture
decoder wrapping the registered Responses codec: they prove the extension seam,
not undocumented real-provider wire behavior. Classified upstream continuity
error recovery, provider-specific mappings, harness breadth and measured
latency/RSS remain open. The single configured data-plane bearer is not a
multi-tenant identity boundary. Full root regression, `go vet`, architecture
conformance (including the full independent-module suite), and three focused
kernel/cache/SQLite race runs passed. C2/SG1/M15 is not declared complete.

Installed checkpoint `8165145`: `make install-all` completed and restarted the
user service. The independent continuity vertical passed three additional race
runs with `GOBROOM_TEST_CLI=/home/fm39hz/go/bin/gobroom`, using the installed
`connections update` to replace the fixture account and observing isolation at
the upstream endpoint. Main daemon status was `ok`, control=serving=484;
config export → validate returned valid bundle v9. Real provider credentials
and configuration were preserved. Unsupported disposable session cache rows
are dropped rather than converted; no real-provider request was needed.

Previous integrated milestone:

Auth callbacks can now use an explicitly configured public HTTPS origin through
a private loopback receiver and an external local TLS proxy. It is off by
default. Provider HTTPS intent alone never opens a listener; optional unavailable
receivers become manual, required ones fail before consent. The existing grant
engine checks receiver/origin/path/query/nonce and revision, ignores forwarded
origin headers, rejects early/replayed delivery and preserves registered URI
spelling for the provider. The ingress has no data/admin mux and no authority
to bypass their bearer policy. Startup reserves/rolls back its socket and Stop
drains it before storage cleanup. Host contract v6 rejects older callback-resource
semantics rather than retaining an adapter.

This adds a deterministic remote-browser/TLS deployment seam, not certificate
management or broad live OAuth support. Full auth fault composition, deployment
acceptance, catalog/harness breadth, alternate stores and measured latency/RSS
remain separate open gates. C4/SG2/M15 is not declared complete.

Verification: full regression/vet and architecture conformance passed. Receiver,
grant, driver/host-contract and lifecycle cases passed three race runs after
origin comparison was fixed to preserve the registered URI. The independent
TLS-proxy/PKCE/activation/protected-inference/restart case passed three race
runs with certificate verification enabled. Installed CLI acceptance follows
installation; this is fake-provider evidence, not public deployment or broad
live OAuth acceptance.

Installed checkpoint `17def5b`: `make install-all` completed and restarted the
user service. HTTPS-proxy, loopback-code and device-setup public-module verticals
passed three additional race runs with the installed CLI. The TLS fixture used
certificate verification, PKCE exchange, activation, replay rejection and
API-key-protected inference after restart. Main daemon status was `ok`,
control=serving=482; bundle-v9 export/validate was valid. Installed daemon help
contains both callback flags, but the running service ExecStart supplies neither,
so its callback ingress remains disabled. No real-provider requests or user
configuration reset were needed.

Previous integrated milestone:

Fresh frontends can reattach by connection using explicit setup `resume` mode.
The daemon atomically chooses reuse or new admission, without repeating provider
Begin for an active session. Initial/submitted steps can be observed with a
cancellable wait; published daemon-await instructions remain immediately
available. Live owner/principal/revision checks retire stale sessions/resources.
TUI `D` uses this mode and CLI exposes `connections setup start --resume`.

This is in-memory reattachment across frontend lifetimes, not pending-session
recovery across daemon restart. New independent-module cases resume an OTP
challenge without exposing prior proof, complete activation and inference, then
separately verify restart creates a new challenge and rejects an old handle.
Resource-to-HTTP-callback-to-manual-replacement composition checks late success
cannot overwrite the new credential and releases the listener. Broader auth
fault/transport composition, live coverage, catalog/protocol breadth and measured
latency/RSS/load remain open; C4/SG2/M15 is not declared complete.

Verification: full regression/vet and architecture conformance passed. Focused
daemon/TUI/CLI admission and fault cases passed three race runs, including a
new frontend that does not remember the previous session handle. Independent
module resume/restart cases also passed three race runs. Installed CLI acceptance
follows installation; these are deterministic fake-provider proofs, not new
live OAuth coverage.

Installed checkpoint `be0ea67`: `make install-all` completed and restarted the
user service. The independent resume/restart cases passed three race runs with
the installed CLI, including `start --resume`, OTP/daemon polling, activation
and public-model inference. The TUI fresh-model reattach/CLI-handoff cases also
passed three installed-client race runs. Main daemon status was `ok`,
control=serving=481; bundle-v9 export → validate was valid, and installed help
documents `--resume`. User configuration was preserved; no live-provider calls
or durable interactive-session recovery are claimed.

Previous integrated milestone:

The TUI can edit and submit generic auth actions through registered schemas.
It reuses masked Bubbles inputs and the existing local-only schema validator,
with named fields/enum choices plus lossless JSON fallback. Compilation is
asynchronous; session/epoch/revision/action/schema fences prevent stale results
or old RPC errors from overwriting a new draft. Close/submit/cancel/transition
clear owned input, public state never retains it, and unrelated resource forms
are not replaced by setup polling. The daemon remains the session/commit/
activation owner. Real Unix IPC fixtures exercise multi-step input, recoverable
rejection, explicit retry, activation and authenticated fake inference.

This closes the basic generic input-editor gap, not the entire auth product.
Full fault/composition and live-provider acceptance, all-schema UX polish,
catalog curation, protocol/harness breadth, alternate stores and measured
RSS/latency/load remain open. C4/SG2/M15 is not declared complete.

Verification: full regression/vet and architecture conformance passed. Focused
auth editor/lifecycle/model-to-daemon IPC cases passed five race runs after
adding stale old-step error fencing. These are registered fake-provider and
model/command integration fixtures; installed-client/terminal acceptance follows
installation and does not imply broad live OAuth or all-schema UX coverage.

Installed checkpoint `b635d42`: `make install-all` completed and restarted the
user service. Three race runs used `GOBROOM_TEST_CLI` to close the TUI editor,
submit the final auth step with the installed CLI, observe activation from the
TUI model and authenticate against a fake upstream. The default installed
`gobroom` also opened its alt-screen in a real PTY and exited cleanly with `q`;
this is a launch/exit smoke check, not a visual audit of every auth form. Main
daemon status remained `ok`, control=serving=480, with bundle-v9 export/validate
valid. No real-provider auth or user configuration reset was performed.

Previous integrated milestone:

Discovery and connection probes now use the daemon's issuer without a raw auth
resolver fallback. Model sources carry exact runtime refs; a standard guarded
HTTP client checks every page and blocks redirects. Local static registries do
not acquire a network lease. Catalog/entitlement/assignment/health writes reuse
one transaction with a live revision/owner and provider-endpoint fence; failure
or cancellation cannot leave a partial import. Probes check authority after
preparation and never replay accepted work. A proactive credential renewal can
still import its catalog using the new material revision. Existing onboarding
and custom/static/discovered distinctions remain in the management workflow.

This improves setup/import and test-connection safety, not catalog grouping UX,
inline TUI setup forms, the full cross-protocol/harness matrix, alternate storage
backends or measured RSS/latency/load. C4/SG2/M15 as a whole remains open.

Verification: full regression and vet passed, including the issuer-backed TUI
onboarding path. Architecture conformance passed with the new import/discovery
gate. Focused HTTP guard, store/discovery/probe/renewal and onboarding race cases
passed three runs; the independent pagination/redirect/restart and probe
verticals also passed three race runs. Installed-CLI acceptance is recorded
after installation; no live-provider or performance claim follows from these
fake-upstream checks.

Installed checkpoint `172a4a5`: `make install-all` completed and restarted the
user service. Five external verticals (discovery, probe, quota, inference lease
and renewal) passed three race runs with the installed CLI. Discovery explicitly
checks one stale first-page request, one fresh import and one user-requested
preview; the redirected target receives no call. The independently linked test
daemon verifies catalog persistence after restart, while installed CLI commands
perform refresh/preview/test-inference and credential updates. Main daemon
status was `ok`, control=serving=479; bundle-v9 export → validate was valid.
User configuration was preserved and no real-provider requests were made.

Previous integrated milestone:

Quota polling and opportunistic enrichment now share the inference issuer but
use exact consumer refs rather than inference operation authority. Sources
receive borrowed transient material and a guarded transport; every report send
is fenced, and final validation rejects obsolete partial results. Typed exact
source/principal/revision keys isolate old cooldown/singleflight work. The
existing `x/sync/singleflight` and `semaphore` provide deduplication and bounded
admission; standard contexts and WaitGroup cancel/drain admitted work on daemon
shutdown before persistence/DB close. Client disconnect remains detached, and
polling is still opt-in. The default enrichment limit is 16; a dropped signal
does not acquire a cooldown. Existing HTTP JSON quota logic is exposed as a
reusable public primitive rather than rewritten by external modules.

This improves headless quota/control safety and resource bounds. Discovery and
connection probes still resolve credentials outside the lease issuer; inline
TUI setup editing, catalog curation, harness/protocol/modality coverage and
measured RSS/latency/load remain open. This is not whole C4/SG2/M15 closure.

Verification: full regression, vet and architecture conformance passed. Focused
kernel/runtime/daemon quota/lease/shutdown cases and the independent quota,
inference-lease and principal-renewal module fixtures passed three race runs.
The strengthened virtual-time singleflight/drain and quota runtime cases also
passed ten race runs. Installed-CLI acceptance follows the implementation
checkpoint; these remain fake-provider tests, not latency/RSS or live-provider
acceptance.

Installed checkpoint `8a76e34`: `make install-all` completed and restarted the
user service. Three further external-module race runs used the installed CLI
for connection replacement across quota/inference-lease and renewal fixtures.
The quota fixture then passed three additional installed-CLI race runs reading
durable quota through `gobroom quota` after restart. The module host is the
independently compiled test daemon, not a dynamically loaded plugin in the
installed daemon. Main daemon status was `ok`, control=serving=478, and bundle-v9
export → validate was valid. User configuration was not reset and no live
provider calls were made.

Previous integrated milestone:

Inference now owns an opaque credential lease for each attempt. Exact
owner/principal/route/operation scope and bounded expiry are checked locally;
the final dispatch fence reads live binding metadata without reading secrets.
Refresh issues a new revision lease and releases the old one, while the existing
effect/replay/idempotency gates remain authoritative. Release is idempotent on
normal terminal paths; expiry does not interrupt accepted responses. An external
module fixture replaces a credential over the control plane while its codec is
blocked in preparation, verifies zero upstream requests, then succeeds with the
new credential. Local admission/fence errors do not become upstream health
evidence. No custom OAuth implementation or third-party lease service was added.

This closes the inference-attempt slice, not all C4/SG2. Discovery, quota and
other outbound jobs still need lease convergence; inline TUI auth action editing,
broader catalog UX, protocol/harness coverage and RSS/latency/load remain open.

Verification: full root regression and vet passed; architectural conformance
including the independent module passed. Focused kernel/store/daemon lease and
concurrent-refresh race tests passed three runs; external dispatch-fence and
principal-renewal continuity fixtures also passed three race runs.

Installed checkpoint `a869763`: `make install-all` completed and restarted the
user service. Both external fixtures passed three further race runs with
`GOBROOM_TEST_CLI=/home/fm39hz/go/bin/gobroom`, so connection updates traversed
the installed CLI while request preparation was held. Main daemon status was
`ok`, control=serving=477; secret-free bundle-v9 export → validate was valid.
No real-provider requests or config reset were needed. This is not performance
acceptance or discovery/quota lease closure.

Previous integrated milestone:

Interactive code and device setup now share one daemon coordinator, session,
resource/revision/CAS/activation path and CLI/TUI public action view. The old
code coordinator/interfaces/metadata/IPC/commands and daemon PKCE/state/redirect
fields are removed; host contract v5 rejects them. Provider strategies compose
the pinned OAuth helpers and own protocol input/exchange. Code/device selection
is schema-backed, with an explicit config option to avoid a selection roundtrip.
Recoverable bad input leaves its challenge available; fatal exchange, owner and
context errors do not. Existing code lifecycle and device/library fixtures were
migrated, while an external module proves S256, resource-before-consent, rejected
callback/replay and opaque credential use after restart. This closes specialized
orchestration divergence, not all auth safety or provider acceptance. Inline TUI
input, leases and full C4/SG2/M15 remain open, as do broader catalog UX, harness/
modality coverage and measured latency/RSS/load gates across the product.

Implementation checkpoint: `cb50006`. Full tests/vet/conformance passed. Focused
provider/daemon/TUI/CLI race cases passed twice; the independent code module
passed five race runs, then three more after its registration request was bound
to the caller context and activation readiness was made explicit. Existing wrong-
input tests now bind actual JSON objects and retain the same challenge before
a valid exchange, rather than silently bypassing driver validation.

After `make install-all`, installed generic setup CLI exercised both the new code
strategy and the migrated opaque device fixture, with callback/PKCE exchange,
activation, refresh, restart and HTTP inference. Main daemon status was `ok`,
control=serving=476, activation queue empty and bundle-v9 export → validate valid.
Installed help lists only `setup` for interactive auth: no `authorize-*` or
`device-*` commands remain. These are fake-provider acceptances, not new live
OAuth, inline TUI or performance coverage.

Previous integrated milestone:

Device authorization now uses the generic setup coordinator and typed action
view throughout daemon, CLI and TUI. A reusable provider strategy owns device
response serde and composes the existing OAuth library's polling; the daemon
owns one background await, queued/admitted worker drain, expiry, credential CAS
and activation. The former device coordinator/IPC/CLI/interface/capability fields
are removed without compatibility aliases; host contract v4 makes that break
explicit. Virtual-time coverage proves a wait can exceed the interactive timeout
without a frontend, while cancellation fences late success. Existing SDK slow-
down, opaque-state privacy, generation/owner ABA, post-commit activation and
stream-independent control fixtures now traverse this engine. TUI displays public
code/URL/input/schema/callback-mode instructions and status; inline input editing
remains unfinished. This reduces onboarding/control divergence, not provider
catalog or harness gaps. Authorization-code convergence, leases, full C4/SG2/M15,
catalog curation and measured latency/RSS/load remain open across the product.

Implementation checkpoint: `734ace6`. Full tests/vet/conformance passed; focused
provider/daemon/TUI/CLI race cases passed twice, including SDK slow-down, expiry,
generation/activation and cancellation/drain. Migrated external setup/privacy/
generation fixtures passed three race runs. Stronger old-host rejection and exact
UI schema-ref assertions then passed focused tests.

After `make install-all`, installed generic setup commands exercised opaque-state
setup, generation fencing, refresh and inference after restart. The exact public-
module owner-rebind and ABA fixture also passed three race runs with the installed
CLI. Main daemon status was `ok`, control=serving=474, activation queue empty,
bundle-v9 export/validate valid; installed help no longer lists device-specific
commands. This is deterministic fake-provider acceptance, not new live OAuth,
inline TUI input or performance coverage.

Previous integrated milestone:

Auth drivers can now request an OS-assigned callback URI without frontend port
selection or a probe/rebind race. Private reservations are allocated before a
typed ready event lets the driver register its redirect and return consent
actions; only the final transition reaches the public revision. Exact grant
activation, expiry/round bounds and cancellation/failure cleanup have focused
fixtures. A separate module proves listener ownership before issuer registration,
no early callback dispatch, independent accounts, responsive control while one
issuer is blocked, activation and HTTP inference through explicitly exposed
Physical/Combo models after restart. This extends onboarding and headless
extensibility; it does not add live provider coverage, generic TUI forms or broader
harness compatibility. Code/device action-engine convergence, credential leases,
full C4/SG2/M15, catalog curation UX and measured latency/RSS/load remain open.

Implementation checkpoint: `4ccecb2`. Full tests/vet and architecture conformance
passed; focused root/provider race checks passed three times and the independent
assigned-URI module passed five race runs. Exact activation scope includes action,
target, correlation, response fields and required/optional policy; the additional
action/policy rejection assertions passed their focused test afterward.

After `make install-all`, the installed CLI observed resolved consent actions and
canceled a pending allocated grant. The isolated fixture also proved concurrent
account setup while one issuer is blocked, rejection before callback activation,
credential activation, restart and HTTP 200 from both explicitly exposed Physical
and Combo names. The main daemon reported `ok`, control=serving=473, an empty
activation queue and a valid bundle-v9 export → validate round trip. This does not
establish new live-provider, generic TUI or resource-performance acceptance.

Previous integrated milestone:

Loopback callback ownership now uses one shared registry for action-based setup
and the existing code flow. Private grants bind exact targets/correlation/query
shapes to schema-backed input actions; no callback protocol is special in the
dispatcher. Required listener failures roll back, optional failures expose manual
handoff, and terminal/cancel/expiry paths release the actual socket. A separate
module proves non-OAuth `ticket`/`proof` callback input, manual fallback, denial,
restart and HTTP inference through Physical/Combo. Scope/ownership and admitted-
callback cancellation have focused fixtures. This improves provider onboarding and
headless lifecycle safety, without adding a live provider or broadening harness
compatibility. The code/device coordinators and frontends still need action-engine
convergence; daemon-assigned callback URLs, TUI action forms, leases and full
C4/SG2/M15 remain open. Catalog curation UX and latency/RSS/load evidence remain
separate unfinished product gates.

Implementation checkpoint: `6ce9490`. Full tests/vet and conformance passed.
Repeated focused provider/daemon race tests passed three times, including
canonical bind sharing, admitted-callback cancellation, deterministic publication
wait and failed-start retention cleanup. The external callback vertical passed
five race runs; its final independent unknown-query rejection assertion was also
followed by three passing race runs.

After `make install-all`, `GOBROOM_TEST_CLI` exercised setup start/get/cancel and
manual URL submit from a mode-0600 file, alongside automatic callback delivery,
typed denial and HTTP inference after restart. The real main daemon reported
`ok`, control=serving=472, no retained activation jobs and valid bundle-v9 export
→ validate. This is an isolated fake-provider acceptance, not new live-provider
coverage or a latency/RSS/load measurement.

Previous integrated milestone:

Provider modules now have an action-based setup path independent of OAuth code/
device shape. Bounded public URL/code/input actions, exact registered schemas,
private state and revisioned transitions feed the shared daemon session ownership,
credential CAS and activation. CLI/IPC can advance manual proof/OTP steps; daemon
polling continues without a frontend. The separate-module fixture starts from an
empty credential, rejects stale/schema-invalid submissions, completes polling,
waits for activation, restarts and invokes HTTP through a Physical/Combo model.
Exact JSON numbers survive setup CLI/IPC; error summaries do not echo submitted
secrets. Timer expiry, single-flight input, late-cancelled completion and shutdown
drain have focused fixtures. This advances provider onboarding extensibility and
headless use; it adds no shipped provider credentials or live acceptance. Existing
code/device/callback coordination still needs to converge onto the action engine,
and generic TUI forms, credential leases and full C4/SG2/M15 remain unfinished.
Physical catalog curation UX, broad harness/protocol acceptance and measured
latency/RSS/load targets also remain open across the product.

Implementation checkpoint: `11a6f65`. Full tests/vet and conformance passed;
focused provider/daemon/CLI race fixtures passed twice and the external module
vertical passed three times under race detection. A final pre-cancelled-call
regression failed before its context guard; after that guard, full tests/vet and
repeated generic-coordinator race checks passed. The evidence-order queue test
also independently verifies older reset observations cannot replace newer attempts.

After `make install-all`, the installed CLI performed setup start/get/submit/
cancel using mode-0600 JSON input, while the fake-provider fixture completed its
poll without frontend get calls, waited for activation, restarted and returned
HTTP 200 through a Physical/Combo model. The main daemon reported `ok`, control=
serving=471, no retained activation jobs and valid bundle-v9 export → validate.
These are temporary-database/fake-upstream checks; no new live account or
production-provider acceptance is claimed.

Previous integrated milestone:

Credential renewal now preserves configured-principal operational evidence.
Typed renewal/replacement transitions separate private credential revisions from
durable principal epochs. Authentication failures have a separate persistence,
queue/spool and inspector lane; neither refresh activation nor an in-flight
success erases active rate/quota restrictions. A separate-module fake provider
proves auth recovery while zero-remaining quota with unknown reset and a rate
cooldown survive renewal and restart. Explicit principal replacement makes those
roles usable again. Store/runtime fixtures cover stale auth writes, current-token
failure during activation, discovery/auth isolation and queued evidence identity.
This improves safe consumption and headless recovery, not provider catalog
breadth, universal harness compatibility or measured performance. Generic setup
actions/leases, provider-attested/shared identity, catalog UX and full C4/SG2/M15
closure remain open.

Implementation checkpoint: `08d662c`. Full tests/vet and conformance passed;
focused store/runtime/daemon race checks passed twice, and external opaque setup
and renewal fixtures passed five times under race detection. Conformance exposed
a real `SQLITE_BUSY_SNAPSHOT` during model editing alongside activation: the
existing driver's `_txlock=immediate` option now reserves write transactions
before validation reads. Its regression failed on the old DSN and passes on the
new one, including concurrent WAL reader access. The final activation receipt
fallback was followed by full tests/vet and a repeated focused race check.

After `make install-all`, the installed CLI verified health/quota/activation/status
views and connection replacement in the local fake-provider renewal/restart
fixture. Auth recovered after renewal; quota/rate-limited roles stayed blocked
through restart; explicit replacement made all three roles callable. The main
daemon reported `ok`, with control/serving versions both 469, an empty activation
list and a valid bundle-v9 export → validate round trip. These are deterministic
fixture acceptances, not new live-provider or latency/RSS/load measurements.

Previous integrated milestone:

Credential recovery is now driven by normalized auth evidence and an explicit
per-mode policy rather than a kernel HTTP-status shortcut. Mode/binding/metadata/
route projections preserve that policy; the generic HTTP/JSON classifier accepts
bounded structured recovery signals, with quota precedence and no recovery on
successful responses. Kernel fixtures retain replay/issuer constraints and
reject unknown actions/policies. A separate-module fixture performs HTTP498
credential-expired recovery, rejects 403 permission/quota and recovery-disabled
401 without another upstream attempt or credential activation, then consumes
persisted state after restart. Host contract v3 makes the semantic change explicit.
This improves provider extension depth and safe API consumption, not catalog
breadth or complete harness compatibility. Quota continuity still requires
principal and credential generations plus auth/operational health separation;
generic setup actions/leases, UX catalog work and measured resource performance
also remain open. Full C4/SG2/M15 is not yet closed.
Implementation checkpoint: `565279b`. Full tests/vet, conformance and repeated
focused root/external race tests passed; final request-scope recovery veto was
also followed by full tests/vet and kernel race checks. After `make install-all`,
the installed CLI exercised device setup/status/activation views and post-restart
inference in the fake-provider HTTP498 recovery fixture. The same fixture
verified permission/quota 403 and mode-disabled 401 do not refresh. Main daemon
status was `ok` with control/serving versions both 468; installed bundle-v9
config export → validate passed. These checks are not live-provider or quota-
continuity acceptance.

Previous integrated milestone:

Credential commit and activation now have distinct observable outcomes. A
transactional, secret-free per-connection outbox resumes after restart; its
daemon worker applies owner/revision-fenced reset/reload with bounded retries
and no idle polling. Refresh returns the new credential without compiling the
routing graph on the inference latency path. Code/device completion remains
successful after commit even if activation is pending. Typed receipts distinguish
control publication, version persistence and actual serving publication;
`/models` follows serving, and status exposes both versions instead of a single
ambiguous counter. CLI list/get/retry and TUI pending/exhausted/readiness states
are implemented. The external fake-provider fixture completes an HTTP 401 refresh
despite a version-write failure, then recovers through retry or daemon restart
without repeating provider calls. These changes improve consume latency shape,
headless operations and configuration correctness; they do not establish a
resource baseline, finish model curation UX, widen provider catalog coverage or
close the full harness/protocol matrix. Generic setup actions/leases and full
C4/SG2/M15 composition remain open.
Implementation checkpoint: `a09102b`. Full tests/vet, current conformance and
repeated focused root/external race tests passed. After `make install-all`, the
installed CLI verified activation list/get/retry, degraded status while control
and serving were already published but version persistence failed, and recovery
by explicit retry or restart. The real main service reported `ok` with control
and serving versions both 467, an empty activation list and a valid bundle-v9
config export → validate round trip. These are deterministic fake-provider
acceptances; renewal-preserved quota, recovery triggers and generic leases/actions
remain architecture work rather than implied completion.

Previous integrated milestone:

The auth-owner rebind slice now guards credential state by exact provider node,
definition ref, selected mode and auth binding. One SQL read returns state with
its revision/owner; final CAS validates both. Definition ID/version changes
rotate child revisions transactionally, including ABA, without revoking work
for provider-node renames/no-op definitions. Serving, polling and opportunistic
quota use the same resolver. Stale pinned routes are rejected before driver
work; old code/device/refresh completion cannot write after a rebind. The
public daemon fixture rejects the old session, retains the previous credential,
then accepts a fresh setup and executes an operation for the current owner.
This strengthens account/configuration correctness and extension isolation.
It does not add provider catalog breadth, finish TUI curation, establish full
harness compatibility, or provide the still-missing resource/load baseline.
Full C4/SG2/M15 remains open for generic setup actions, leases, reconciliation
and the remaining composition matrix.
Implementation checkpoint: `e402a0f`. Full tests/vet, the conformance composition
suite and repeated focused root/external race tests passed. After
`make install-all`, the installed CLI passed device-start → definition update
(including ABA) → failed old completion → retained-credential probe → fresh
current-owner setup → new-credential probe. The main daemon reported `ok` at
snapshot 466 and installed config export → validate passed (bundle v9).
These are fake-provider checks; activation after durable commit is the next
identified architecture slice, not a completed part of this milestone.

Previous integrated milestone:

The auth-state boundary now separates provider-owned persisted state from the
ephemeral kernel credential. Code/device setup and refresh return a bounded,
redacted `CredentialState`; the daemon validates through the selected `Resolve`
before revision CAS. OAuth alone owns token serialization; refresh token/client
ID/client secret no longer live in `kernel.Credential`. A provider-defined
`opaque_ticket` mode has a public-module setup → HTTP 401 → refresh → restart
fixture preserving typed ticket/subject/workspace metadata without exposing
it in config/IPC. The kernel accepts same-secret header/context/generation
refresh without relaxing operation replay rules. Static module host contract
v2 makes the interface break explicit rather than adding a compatibility path.
This improves provider extensibility and account operation, not catalog breadth,
TUI model curation, full harness compatibility or resource-performance evidence.
Generic action-based setup, opaque lease isolation and post-commit reconciliation
remain open; this slice does not close full C4/SG2 or M15.
At that checkpoint, provider-definition rebind fencing was still missing; the
subsequent owner milestone above closes that identified slice. The earlier
opaque-state preservation fixture itself keeps its definition fixed.
Implementation checkpoint: `30a066e`. Full tests/vet, conformance composition
and repeated focused root/external race tests passed. After `make install-all`,
the installed CLI completed custom device setup and post-restart inference;
the fixture's real HTTP path also performed one 401-triggered refresh and
verified state privacy. The main daemon reported `ok` at snapshot 465 and
installed config export → validate passed (bundle v9). No live provider was
called, and the remaining ownership/reconciliation/lease gates stay open.

Previous integrated milestone:

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

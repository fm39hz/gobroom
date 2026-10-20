# CLIProxyAPI implementation and reuse review

Reviewed local source snapshot: `../CLIProxyAPI`, tag `v8.0.17`, commit
`57bde351` (2026-10-08). This is a source review, not a claim of behavioral
parity or a benchmark. The purpose is to identify implementation patterns and
code that Gobroom can safely reuse without making its kernel depend on another
gateway's internal architecture.

## Short conclusion

CLIProxyAPI is useful as a source of provider edge-case knowledge and operational
fixtures. It is not a suitable runtime dependency for Gobroom. Its provider
executors and protocol translators are intentionally coupled to CLIProxyAPI's
auth records, model registry, HTTP clients, logging, plugin runtime and
configuration. Several `sdk/...` packages also import the module's `internal/...`
packages, which Go forbids Gobroom from importing as a separate module. Its
module declares Go 1.26.0; importing the whole module would also make Gobroom
inherit an unrelated application dependency graph and toolchain floor.

No CLIProxyAPI module dependency or copied implementation is warranted by this
review. Keep the Gobroom canonical IR, extension contracts and runtime
composition. Reuse the upstream source as a design/test corpus for concrete
provider quirks, and port only a narrowly scoped algorithm when a Gobroom
contract has a demonstrated gap. Any such port must preserve the upstream MIT
notice and have an independent Gobroom fixture.

## Implementation map

Response-terminal lesson: `sdk/api/handlers/handlers_interceptors.go`
(`completeError`) and `handlers_stream.go` (`streamDeliveryCompletion`)
separate cancelled request lifecycles from stream delivery failure. Gobroom
uses that problem statement to keep caller cancellation/local projection
faults out of provider health, retain known provider usage, and publish one
validated terminal result. It uses standard Go context causes, `errors.As`/
`errors.Is`, and `http.ResponseController` plus its own IR contracts; no
interceptor or stream-delivery implementation is copied or imported.

Scope-isolation lesson for the operation feedback slice: the auth scheduler's
targeted model updates (`sdk/cliproxy/auth/scheduler.go` and the targeted-update/
cooldown-rebuild fixtures) keep one suppressed model from starving another.
Gobroom extends that problem statement to independent operations on one model
route. It uses exact typed operation refs in its own breaker/quota identities,
immutable snapshot generation fences and persistence queue/spool contracts;
it does not copy the scheduler, import the auth conductor or add a job branch.

### Request path and execution

Dispatch-authority follow-up: `sdk/cliproxy/auth/conductor_execution.go` selects
an auth snapshot before executor preparation; the reviewed dispatch path does
not reread its revision immediately before sending. `conductor_refresh.go`
serializes refresh by account and checks registration epochs, while
`conductor_cooldown.go` fences stale execution results. These solve publication
and feedback problems, not authorization of an already prepared request.
Gobroom uses that distinction for attempt-scoped leases with a final secret-free
SQLite metadata read. Its existing CAS, standard `sync.Mutex`, context and time
primitives cover the mechanism; OAuth remains delegated to `x/oauth2`. No new
token cache, conductor or custom OAuth implementation is introduced. This does
not assert atomic revocation against a network dispatch already in progress.

Quota follow-up: management `plugin_quota.go:FetchCredentialQuota` explicitly
selects one credential independently of registry-backed model listing;
`conductor_cooldown.go:isStaleExecutionResult` fences older material/registration
results, and `quota_signals.go` merges observations without treating them as
cooldown state. Gobroom retains those separate concerns: exact quota-consumer
lease scope, a guard at every host transport call, and final revision validation
before recording partial/multi-report results. Existing `x/sync/singleflight`
still owns deduplication; typed principal/revision identities isolate old work.
Standard contexts and WaitGroup track queued/admitted workers through daemon
shutdown. No management handler, conductor or custom singleflight is copied.

Discovery follow-up: the reference registry (`internal/registry/model_registry.go`)
keeps client-specific model metadata separately from request execution. Gobroom
keeps its own model-source/import workflow and extends the already observed
stale-owner problem to catalog publication: every HTTP page uses a standard
guarded client and catalog/entitlement/health updates validate the observed
binding inside one SQL transaction. Existing provider pagination/decoding is
reused, and native `database/sql` plus SQLite writer reservation enforce the
commit boundary. No reference registry, model alias state or custom HTTP stack
is imported. Probes use the same issuer rather than a second auth resolver.

Callback ownership follow-up: `internal/auth/claude/oauth_server.go` and the
Codex equivalent probe a port before starting a listener, while Devin's
`internal/auth/devin/devin_auth.go` retains a directly bound loopback listener.
SDK callers separately validate returned state. The problem is ownership of a
real socket and correlation to one pending setup, not an OAuth-specific server
to import. Gobroom's action grant registry uses its existing Go HTTP/URL/context
primitives, exact target/query matching and session/action revisions. The code
flow registers with the same listener runtime; no reference listener, forwarder,
store or broad-address binding is copied. Fixed and daemon-assigned URLs use
the same registry. Devin's `sdk/auth/devin.go` obtains `actualPort` from
`OAuthServer.Start` before constructing redirect/consent URLs; Trae/Windsurf
likewise await the actual bound port before authorization. Gobroom models that
ordering as private callback reservations and a typed `callbacks_ready` event,
keeping listener readiness separate from callback activation. No reference
callback server is imported and setup ownership stays in the daemon.

Auth lifecycle follow-up: `sdk/cliproxy/auth/conductor_refresh.go` rereads an
account after acquiring its per-auth lock, reuses a newer access token and fences
late refresh merges by registration epoch/generation. This identifies the
superseded-result problem; Gobroom solves it using its own durable SQLite
credential revision CAS shared by interactive setup and refresh. Waiting uses
the existing `x/sync/semaphore` context contract, not a copied auth conductor.
Unlike a cancellation-detached persistence policy, Gobroom rejects late success
before its commit boundary when the request or daemon context is canceled.
Post-commit reconciliation errors remain observable and do not roll back tokens.

Provider-state lesson: `sdk/cliproxy/auth/types.go` keeps arbitrary mutable
account metadata (tokens/cookies/provider fields), rather than assuming all
refresh results are a bearer token. Gobroom solves that preservation problem
with bounded, redacted, provider-owned `CredentialState` plus typed serde inside
the auth primitive. Its daemon validates through `Resolve` before revision CAS;
its kernel sees only an ephemeral credential/generation. No metadata bag or
CLIProxyAPI persistence/conductor implementation is copied into the kernel.

Auth-owner follow-up: `conductor_lifecycle.go` fences refresh/prepare merges
against `RegistrationEpoch`, including re-registration under an existing auth
ID. Gobroom applies that stale-owner problem to a provider definition/version
rebind: typed node/definition ownership is read with credential state, child
revisions rotate in SQLite, and owner/revision CAS rejects late old-driver
results. Existing SQL transactions and exact refs do the work; no auth conductor
or process-local epoch registry is imported or duplicated.

Activation follow-up: `conductor_lifecycle.go:updateInternal` saves credential
changes before runtime publication, and `conductor_refresh.go` explicitly warns
when persisting a rotated token fails. Gobroom treats that split as a business
boundary: credential CAS and activation intent commit together in SQLite;
local reconciliation has typed publication receipts and never repeats the
provider exchange. This is not a port of the conductor or a remote queue.

The same source also preserves an unexpired `credential_quota` observation
across auth refresh. That is a separate semantic lesson still to close in
Gobroom: credential renewal, account replacement and principal-scoped quota
evidence must not be conflated. Activation correctness alone does not prove
quota continuity across token renewal.

Recovery-trigger follow-up: `conductor_refresh.go:tryRefreshAfterUnauthorized`
rejects request-scoped failures before trying auth recovery. Gobroom preserves
that distinction with a normalized recovery action plus a declared per-mode
policy and generic replay/effect guards, rather than testing provider names or
HTTP status in the recovery branch. Structured HTTP/JSON options can normalize
custom expiry envelopes into that action; permission/quota evidence is not a
credential recovery request. Its matcher reuses existing typed JSON evidence,
schema validation and standard Go helpers; no conductor implementation is copied.

CLIProxyAPI separates API handlers, a translator pipeline, auth/conductor
selection and provider executors. `sdk/cliproxy/auth/conductor_execution.go`
owns attempt orchestration (`Execute`, `ExecuteCount`, `ExecuteStream`), while
`sdk/cliproxy/auth/scheduler.go`, `selector.go`, and the `conductor_*` files
implement credential choice, cooldown, refresh, session affinity and retry
behavior. Provider wire behavior is in `internal/runtime/executor/*`; each
executor can prepare auth, build its own request, call a provider endpoint,
decode its response and report usage/errors.

This design is effective for rapidly adding provider-specific behavior, but the
provider executor is a large behavioral unit rather than a manifest-selected
composition of endpoint, transport, auth, codecs and error evidence. Provider
quirks therefore tend to be implemented in executor code and coupled to
CLIProxyAPI's auth/model/config types. It is a good reference when Gobroom needs
to understand a provider's actual wire deviations; it is not a reusable generic
provider kernel.

Gobroom's intended boundary remains:

```text
client protocol -> typed normalization -> model graph/scheduler
                 -> compatibility plan -> provider primitives
                 -> canonical response events -> client renderer
```

The reusable lesson is to make the attempt lifecycle explicit and test failure
boundaries: before dispatch, after dispatch, after upstream acceptance, and
after downstream commit. Gobroom already has its own replay-safety contract;
CLIProxyAPI should inform additional provider fixtures, not replace that
contract or its independent scheduler.

The 2026-10-17 lifecycle follow-up against the same source snapshot examined
`internal/api/server.go:Start`: it acquires a TCP listener and returns bind
failures before entering `Serve`. Gobroom had started `ListenAndServe` in a
background goroutine, allowing `Start` to report success while a requested
port was unavailable. Gobroom now reserves both enabled HTTP listeners before
publishing IPC and uses standard-library `net.ListenConfig`/`http.Server.Serve`
with startup rollback. The lesson is listener ownership and readiness; no
CLIProxyAPI server or protocol-multiplexer code is imported or copied.

### Protocol translation

`sdk/translator.Registry` maps pairs of wire `Format` values to request and
response functions. `sdk/translator.Pipeline` adds ordered request and response
middleware around that registry; `internal/translator/*` contains the actual
provider/protocol-specific translations. The pair registry is convenient for a
small number of formats, but adding formats tends toward a source/target
matrix, and transforms operate on raw JSON envelopes rather than a
provider-neutral semantic IR. Middleware hooks also carry CLIProxyAPI-specific
model registry metadata and thinking behavior.

Gobroom should keep the stronger IR boundary already established: normalization
extracts request intent/facets, request codecs encode to a selected provider,
response decoders emit canonical events, and renderers project those events to
the client protocol. The part worth carrying forward is the explicit,
deterministically ordered middleware/transform-chain concept. Gobroom now
represents configured request/response transform steps in its compatibility
plan; it should continue to attach declared effects, scope and failure policy
to those steps rather than importing the raw-JSON translator registry.

### Credential and passive health behavior

The conductor/scheduler code is a rich source for fixtures around quota
cooldowns, reset times, transport retry, credential refresh races, session
affinity, concurrency and error priority. Those behaviors are interdependent
with CLIProxyAPI's account/auth object and model registry. Copying the conductor
would duplicate Gobroom's model graph and feedback-based scheduler, while
introducing a second authority for health and route order.

Use these as questions for Gobroom's contract tests: which failures are safe to
retry; which evidence marks quota exhaustion versus transient rate limiting;
how are reset/cooldown windows scoped; what happens when refresh races with a
new credential; and when may session affinity yield to availability? Do not
copy the implementation without a specific uncovered behavior and a narrow
interface boundary.

### SDK packages and direct dependency feasibility

The apparently reusable paths have different problems:

| Source package | Reuse assessment |
|---|---|
| `sdk/translator` | Not importable from Gobroom as-is: its production files import `internal/registry` and `internal/thinking`; the API is also raw-JSON format-pair translation rather than Gobroom IR. |
| `sdk/translator/builtin` | Imports `internal/translator`, so it is not an external consumer API. |
| `sdk/cliproxy` | Builder/service package imports CLIProxyAPI `internal/api`, watcher, plugin host and other internals; it would embed the application runtime. |
| `sdk/cliproxy/auth` | Despite its SDK path, production files couple to internal model registry, logging, thinking, utilities and—in some paths—Gin. Not a standalone scheduler library. |
| `sdk/cliproxy/session` | Uses internal utilities and executor/translator contracts; its session identity and LCP rules are product-specific. |
| `sdk/cliproxy/usage` | Couples to internal logging and CLIProxyAPI accounting types; Gobroom already owns usage semantics and persistence. |
| `sdk/cliproxy/executionregistry` | Closest to a standalone library (mostly stdlib plus logrus), but it tracks Home-dispatched execution resource lifetimes, not generic model routing. No current Gobroom use case justifies adding it. |
| `sdk/pluginapi` | A plugin ABI/control surface, not an implementation of Gobroom's typed primitive catalog or isolation boundary. |

The source module is `github.com/router-for-me/CLIProxyAPI/v8` and requires
`go 1.26.0`. The upstream repository is MIT-licensed, so narrowly copied code
is legally possible with its notice retained; legality does not resolve the
internal-package, API coupling, dependency, compatibility, or maintenance
problems above.

## What to adopt in Gobroom

1. Continue using CLIProxyAPI's provider executors and tests as evidence when
   implementing a particular provider definition, especially auth refresh,
   model aliases, streaming termination, thinking/reasoning, tool identity and
   quota reset edge cases.
2. Convert a discovered behavior into a typed Gobroom primitive contract and a
   fake-upstream conformance test. Keep provider variation in manifests,
   codecs, auth modules or scoped extensions; do not add provider branches to
   the kernel.
3. Keep request and response transformations ordered and inspectable. For each
   step, preserve exact version, scope, effects and error/fallback accounting.
4. Import a third-party package only when it is a real standalone module/API,
   does not depend on CLIProxyAPI internals, matches a Gobroom contract, and
   measurably removes maintenance without adding an unnecessary runtime.
5. Do not add CLIProxyAPI as a `require`, `replace`, `go.work` module, vendored
   tree or runtime service. Do not duplicate its full provider executor set.

## Consequence for the roadmap

This review does not create a new M15 dependency or an excuse to pause its
closure. It sharpens C1/C2/C3 boundaries: extension dependencies must resolve
through Gobroom's own exact-ref catalog; compatibility plans must describe the
typed IR transformations; and provider-specific knowledge is incorporated as
manifest/codec/auth behavior plus conformance fixtures. The next M15 work stays
on route-level compatibility explanation and remaining SG1–SG7 evidence, not
on importing CLIProxyAPI's conductor or translator.

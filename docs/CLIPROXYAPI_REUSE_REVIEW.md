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

### Request path and execution

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

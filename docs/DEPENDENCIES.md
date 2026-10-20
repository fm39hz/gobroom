# GoBroom dependency policy

Snapshot checked: 2026-09-19.
The JSON Schema row below was added after a targeted upstream release check on
2026-10-06. The IPC ownership row was checked on 2026-10-17; the existing rows
retain their original check date.
The HTTP syntax row was checked through the Go module proxy on 2026-10-17.

The goal is to reduce handwritten infrastructure code without turning
GoBroom into a dependency-heavy framework. The standard library remains the
default for HTTP, JSON, context, concurrency, crypto, testing and logging.

Before adding infrastructure, check the standard library, existing dependencies
and reusable project primitives first. Inspect relevant upstream source and tests
for provider/protocol behavior instead of guessing from names or wire examples.
Record the source, reuse boundary and license when borrowing implementation;
keep Gobroom-specific policy separate from reusable mechanics. Check current
upstream versions when selecting or upgrading a dependency, then pin and test it.

The operation-probe upload uses `mime/multipart`, `io.Pipe` and the existing
Unix HTTP transport, multipart ingress and artifact store. CLI/TUI share one
strict JSON manifest decoder; no second upload/storage framework is introduced.

Per-connection auth serialization uses `semaphore.Weighted.Acquire(ctx, 1)`
from the already-pinned `golang.org/x/sync v0.24.0`. The library owns cancellable
waiter coordination; Gobroom only owns connection-key scoping and entry lifetime.
Durable credential revision CAS and revision rotation use SQLite transactions
and triggers, covering every credential writer rather than duplicating counters
in setup, refresh and configuration paths.

Auth state uses provider-owned typed serde and standard `fmt`/JSON redaction,
not a kernel metadata-bag parser. Public OAuth encode/decode/factory helpers let
modules compose the pinned `x/oauth2` flow without copying its exchange logic.

Credential activation reuses SQLite transactions/indexes for its durable outbox,
`time.Timer` for due work (disabled when idle), existing per-connection
`x/sync/semaphore` ownership, and atomic immutable snapshot publication. Its
fixed bounded local retry policy is application behavior, not a new provider
retry framework. No queue service, scheduler dependency or periodic provider
health check is introduced.

Structured auth recovery reuses the existing HTTP/JSON error-evidence decoder,
JSON Pointer matching and frozen JSON Schema binding. Evidence is decoded once
per classifier call; status membership uses standard `slices.Contains`. No
provider-specific regex parser, status switch in the recovery kernel or new
dependency is needed for configured token-expiry envelopes.

## Adopt

| Area | Library/tool | Version checked | Decision | Why |
|---|---|---:|---|---|
| SQLite driver | `modernc.org/sqlite` | `v1.59.0` | adopt | pure Go SQLite, no CGO requirement; replaces the current ad-hoc fallback risk |
| SQL generation | `github.com/sqlc-dev/sqlc` | `v1.31.1` | adopt as separate build tool | generates typed repositories from SQL and removes repetitive scan/row mapping code |
| HTTP routing | `github.com/go-chi/chi/v5` | `v5.3.2` | adopt when API surface grows | small middleware/router layer on top of `net/http`; avoids a custom route framework |
| OAuth | `golang.org/x/oauth2` | `v0.37.0` | adopt | standard OAuth2/token transport, PKCE and device/auth helpers |
| IPC process ownership | `github.com/gofrs/flock` | `v0.13.1` (checked 2026-10-17) | adopted | OS-backed non-blocking exclusive file locks with descriptor cleanup; avoids maintaining platform lock syscalls or PID-file heuristics |
| HTTP syntax validation | `golang.org/x/net` | `v0.61.0` (checked 2026-10-17) | adopted | `httpguts.ValidHeaderFieldName` replaces duplicated handwritten token parsers at provider/snapshot admission; replay timing tests use standard `testing/synctest` rather than a clock framework |
| Extension option schemas | `github.com/santhosh-tekuri/jsonschema/v6` | `v6.0.3` (checked 2026-10-06) | adopted | compile JSON Schema 2020-12 at catalog freeze and validate typed module options at bind time; custom refs resolve only from the local catalog |
| CLI | `github.com/spf13/cobra` | `v1.10.2` | adopt for `gobroom` | command tree, flags, help and completion without custom CLI plumbing |
| TUI runtime | `charm.land/bubbletea/v2` | `v2.0.9` | adopted | Elm-style state/update/view model; bundled into the `gobroom` control client |
| TUI components | `charm.land/bubbles/v2` | `v2.2.1` | adopted | Official fuzzy list, text input, viewport and help components; avoids custom terminal input/filter machinery |
| TUI layout | `charm.land/lipgloss/v2` | `v2.0.5` | adopted | Terminal-cell-aware panel/layout styling; palette stays deliberately restrained |

The official repositories report the versions above for the checked date:
[chi](https://github.com/go-chi/chi/releases),
[sqlc](https://github.com/sqlc-dev/sqlc/releases),
[Cobra](https://github.com/spf13/cobra/releases),
[Bubble Tea](https://github.com/charmbracelet/bubbletea/releases),
[Bubbles](https://github.com/charmbracelet/bubbles/releases), and
[Lip Gloss](https://github.com/charmbracelet/lipgloss/releases).

The Go module proxy currently lists `modernc.org/sqlite` v1.59.0 and
`golang.org/x/oauth2` v0.37.0 as the newest versions available at the check
date. These should be rechecked immediately before pinning a release.
The validator pin is verified from the maintainer's
[v6.0.3 release](https://github.com/santhosh-tekuri/jsonschema/releases/tag/v6.0.3).
The IPC ownership pin was checked against `go list -m ...@latest` and the
[v0.13.1 release](https://github.com/gofrs/flock/releases/tag/v0.13.1). Its runtime
dependency on `x/sys` is already present in Gobroom's module graph.
The `x/net` pin was resolved by `go list -m -json golang.org/x/net@latest`
(upstream timestamp 2026-10-09). Its minimum dependency graph also selects
`x/sync v0.24.0`, `x/sys v0.49.0` and `x/text v0.43.0`; both the root module and
the independent external-provider fixture are tidied against that graph.

## Keep optional

Credential leases use standard `context`, `sync`, `time`, `net/http.Header.Clone`
and the existing SQLite/CAS boundary. No third-party lease service or token
cache is needed for an in-process attempt authority. OAuth protocol exchanges,
PKCE and polling remain the pinned `x/oauth2` implementation; provider-specific
durable state continues to use typed serialization, not kernel token parsing.

| Area | Library/tool | Version checked | Decision |
|---|---|---:|---|
| Structured logging | `log/slog` | stdlib | prefer stdlib; no zerolog dependency initially |
| Metrics | `github.com/prometheus/client_golang` | `v1.24.1` | defer until metrics consumers exist |
| Retry/backoff | `github.com/cenkalti/backoff/v4` | `v4.3.0` | do not adopt initially; provider retry policy is domain-specific |
| UUID | `github.com/google/uuid` | `v1.6.0` | avoid initially; use `crypto/rand` IDs or database IDs |
| Assertions | `github.com/stretchr/testify` | `v1.12.1` | optional; standard `testing` is sufficient for core contracts |

Zerolog is technically suitable for high-volume structured logs, but GoBroom
should first use `log/slog` to avoid maintaining two logging APIs. The zerolog
repository documents low-allocation structured logging and slog integration,
but that is not enough benefit to justify it in the first runtime.

## Migration tool decision

Use one migration strategy, not a mixture. Build tools live in `tools/go.mod`,
not in the daemon runtime module:

```text
golang-migrate/migrate v4.20.1 + SQL migration files
```

The project supports SQLite and versioned `v4` imports. [Official migrate repository](https://github.com/golang-migrate/migrate)

`sqlc` generates query code; `golang-migrate` owns schema versioning. This
removes the current inline `CREATE TABLE`/best-effort `ALTER TABLE` pattern and
makes database changes reviewable and reversible.

The TUI is bundled into the `gobroom` control client through `internal/tui`;
running `gobroom` without a subcommand opens it. Bubble Tea, Bubbles and Lip
Gloss must never become dependencies of `gobroomd`'s runtime package graph.
Huh was not added: the TUI needs inline, domain-aware editors (including
ordered combo membership), not standalone full-screen prompt flows.

Migration and SQL generation tools are pinned in `tools/go.mod`, but the
current store still owns its schema setup/repository SQL directly. The pinned
tools are not evidence that generated repositories or versioned migrations are
already wired into the build; that integration remains engineering work.

## Explicit non-adoptions

Code setup convergence uses the pinned `x/oauth2.GenerateVerifier`, S256 helper
and verifier exchange option, existing JSON Schema binding and generic callback
resources/CAS. The daemon no longer contains an OAuth-specific coordinator or
parser. Standard URL/JSON binding replaces provider-wire guessing; a private
typed method-choice wrapper composes the existing code/device strategies. No
new OAuth implementation, template engine or workflow dependency is introduced.
Host contract v5 deliberately removes the old code interfaces and control paths.
Device lifecycle convergence reuses the already-pinned `x/oauth2` device flow.
Its `deviceauth.go` owns server interval, `authorization_pending`, `slow_down`,
expiry and token retrieval; Gobroom does not implement another RFC poll loop.
The generic coordinator uses standard context deadlines, `sync.WaitGroup` and
existing CAS/activation, while provider hooks own opaque response/credential
binding. No new dependency, workflow service or frontend-owned token wait is
introduced. The public lifecycle break is module host contract v4.

Dynamic callback allocation uses the standard `net.ListenConfig`/listener
address, URL/form binding and existing session context/deadline ownership.
Pending reservations are separate from active grants in the existing registry;
the private typed ready event introduces no resource service, template engine,
workflow framework or dependency. The ordering was checked against Devin's
actual-port handoff and the reference Trae/Windsurf dynamic listeners.

Callback grants reuse `net.Listen`, `net/http`, `net/url`, constant-time byte
comparison and the existing action/schema/CAS lifecycle. One listener registry
replaces the OAuth-only runtime; no callback web framework, provider redirect
forwarder or new dependency is introduced. Reference source showed both the
probe-then-bind race and broad-address listeners, as well as a direct loopback
listener held by the flow. Gobroom retains actual socket ownership and binds
delivery to a typed grant rather than copying those callback servers.

Action-based authorization uses existing frozen JSON Schema validation for input
actions, the existing CAS/activation and coordinator reservations, and standard
context/timers for bounded calls and requested polling. Opaque state follows the
existing typed redaction pattern. No workflow/state-machine framework, JSON
validator, browser automation library or polling service is added.
The standard JSON decoder's `UseNumber` preserves numeric identities/revisions
across generic control envelopes; handlers bind typed structs instead of
converting through floating point or manipulating JSON strings.
Reference problems were checked in CLIProxyAPI `internal/cmd/vertex_import.go`
(`DoVertexImport`: validate/import a service-account document),
`internal/auth/meta/meta.go` (`MintAPIKey`: intermediate token to final key), and
the adjacent reference's cookie-auth UI (manual input followed by an exchange).
The contract expresses those interaction needs; it does not copy their stores,
UI handlers or provider auth implementation.

Credential renewal/replacement uses existing SQLite transactions/triggers,
typed CAS/outbox and health persistence, with Go's `testing/synctest` for virtual
cooldown time. No new runtime dependency is needed. The behavior was checked
against the adjacent CLIProxyAPI source (`sdk/cliproxy/auth/conductor_lifecycle.go`,
`UpdateRefreshedAuth`/`updateInternal`): stale refresh fencing and preserving active
credential-quota evidence are useful precedents; importing its auth manager would
also import a second ownership, persistence and scheduler model. This is reuse of
existing Gobroom primitives with source-backed semantics, not a copied manager.

Explicit store transactions reserve the SQLite WAL writer using the existing
modernc driver option `_txlock=immediate` (verified in the pinned v1.59.0 source,
`sqlite.go` connection options). This prevents deferred validation-read/write
upgrade failures when activation and model editing overlap, without an application
retry loop. WAL readers remain concurrent; writes still obey SQLite's single-writer
and configured busy-timeout limits. The reservation regression fails with the old
DSN and passes with the driver option.

Do not add:

```text
ORM                         SQL schema is already the domain contract
dependency injection       small daemon graph is explicit
generic event framework    typed channels are enough
generic retry framework    retry policy depends on provider/error semantics
Web framework               net/http + chi is enough
config framework            SQLite + typed control API is enough
```

## Version policy

- Pin runtime dependencies in `go.mod`.
- Pin `sqlc` and migration tools in a toolchain file or CI image.
- Do not use `go get @latest` in builds.
- Recheck versions and changelogs at release time.
- Run `go mod tidy`, `go test ./...`, race tests and a dependency vulnerability
  scan before upgrading.
- Upgrade one infrastructure dependency at a time.

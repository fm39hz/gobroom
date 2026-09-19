# GoBroom dependency policy

Snapshot checked: 2026-09-19.

The goal is to reduce handwritten infrastructure code without turning
GoBroom into a dependency-heavy framework. The standard library remains the
default for HTTP, JSON, context, concurrency, crypto, testing and logging.

## Adopt

| Area | Library/tool | Version checked | Decision | Why |
|---|---|---:|---|---|
| SQLite driver | `modernc.org/sqlite` | `v1.59.0` | adopt | pure Go SQLite, no CGO requirement; replaces the current ad-hoc fallback risk |
| SQL generation | `github.com/sqlc-dev/sqlc` | `v1.31.1` | adopt as separate build tool | generates typed repositories from SQL and removes repetitive scan/row mapping code |
| HTTP routing | `github.com/go-chi/chi/v5` | `v5.3.2` | adopt when API surface grows | small middleware/router layer on top of `net/http`; avoids a custom route framework |
| OAuth | `golang.org/x/oauth2` | `v0.37.0` | adopt | standard OAuth2/token transport, PKCE and device/auth helpers |
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

## Keep optional

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

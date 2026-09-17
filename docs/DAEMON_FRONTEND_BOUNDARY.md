# Daemon/frontend boundary

`gorouterd` is the backend. CLI and TUI are replaceable frontend clients.

```text
gorouterd
  ├── data plane: /v1/*
  ├── control plane: /api/*
  ├── SQLite
  ├── route snapshot
  ├── provider adapters
  ├── quota/usage/health workers
  └── no terminal/UI dependency

gorouter CLI/TUI
  ├── call local control API
  ├── render state
  ├── submit mutations
  └── no direct SQLite access
```

The daemon must remain fully usable when:

- no CLI is installed;
- no TUI is running;
- a different frontend is used;
- configuration is edited through HTTP/API automation;
- the daemon runs headless under a service manager.

The frontend contract is the control API, not Go package imports. This keeps
the backend deployable as a standalone binary and lets the TUI evolve or be
replaced without changing routing logic.

## Dependency rule

```text
gorouterd → runtime dependencies only
gorouter CLI → CLI client dependencies
gorouter TUI → TUI dependencies, later separate module
tools/ → sqlc/migration tooling only
```

No UI package may be imported by `internal/*` or `cmd/gorouterd`.

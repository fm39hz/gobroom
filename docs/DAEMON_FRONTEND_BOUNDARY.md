# Daemon and frontend boundary

Status: enforced architectural boundary; see [roadmap](IMPLEMENTATION_PLAN.md)
for completeness of individual frontend workflows.

```text
gobroomd
  ├── owns SQLite, routing snapshots, provider execution and runtime workers
  ├── exposes local control IPC
  ├── optionally exposes HTTP control
  └── exposes the configured HTTP provider data plane

gobroom / gobroom-tui / future frontend
  ├── sends typed control requests to the daemon
  ├── renders returned state
  └── never owns routing state or accesses SQLite directly
```

The daemon must serve requests when no CLI or TUI is installed or running.
Frontends can be replaced independently; their control contract is IPC (and,
when explicitly enabled, the HTTP control API), not direct Go imports into
kernel/store packages.

The current CLI already exposes management operations over IPC. The current
Bubble Tea program is a basic control UI, not the final keyboard-first
management experience. UX completion is tracked under M2 in the roadmap.

Dependency direction:

```text
daemon -> runtime/control dependencies
CLI    -> IPC client + Cobra
TUI    -> IPC client + Bubble Tea
```

No UI package may be imported by `internal/*` or `cmd/gobroomd`. Shared request
and response payloads should live in a UI-independent control contract as that
surface matures.

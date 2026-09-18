# GoBroom

GoBroom is a local AI provider daemon with an OpenAI-compatible data plane.

It manages provider nodes, model routes, fallback combos and public models. The
CLI and TUI are frontend clients of the daemon; they do not read SQLite
directly.

## Current capabilities

- OpenAI-compatible /v1/models, /v1/chat/completions, /v1/responses and /v1/messages;
- OpenAI Chat, OpenAI Responses and Anthropic Messages adapters;
- basic Anthropic-to-OpenAI Chat text/tool SSE conversion;
- provider prefixes and /models discovery;
- custom model catalog entries with capability filtering;
- logical models and nested combos;
- explicit public-model publishing;
- snapshot validation and cycle detection;
- SQLite control plane and Unix IPC;
- per-connection route candidates, health cooldowns and basic persisted quota gates;
- optional HTTP control API;
- status TUI;
- Makefile build/test/install targets.

## Status

GoBroom is under active development. The daemon, control plane, normalization
IR, connection-aware fallback and initial provider adapters are implemented.
Provider-specific OAuth, advanced quota accounting, full cross-protocol event
translation and the complete TUI are not finished yet.

## Build

Requirements: Go 1.27 or newer and Unix domain sockets on Unix-like systems.

    make build-all
    make test

## Run

    make run-daemon

By default the daemon stores SQLite state under the user config directory,
creates a Unix IPC socket under the runtime directory, exposes the provider data
plane on 127.0.0.1:2712, and keeps HTTP control disabled. If enabled, the
separate HTTP control plane listens on 127.0.0.1:2713.

## Provider API

    http://127.0.0.1:2712/v1/models
    http://127.0.0.1:2712/v1/chat/completions
    http://127.0.0.1:2712/v1/responses
    http://127.0.0.1:2712/v1/messages

## IPC control plane

    gobroom status
    gobroom reload

The IPC layer supports daemon lifecycle, provider/model/combo/public-model
operations, route resolution and model refresh. The TUI currently displays
daemon status and supports reload.

## Optional HTTP control API

    gobroomd -http-control=true

## Project layout

    cmd/gobroomd       daemon entrypoint
    cmd/gobroom        CLI IPC client
    cmd/gobroom-tui    TUI IPC client
    internal/api        HTTP gateway
    internal/daemon     lifecycle and IPC server
    internal/kernel     route snapshot and adapter contract
    internal/normalize  semantic request normalization
    internal/adapter    provider protocol adapters
    internal/controlplane SQLite-to-snapshot loading
    internal/discovery  provider /models discovery
    internal/store      SQLite repositories

Architecture docs: daemon (docs/DAEMON_ARCHITECTURE.md),
kernel (docs/KERNEL_CONTRACT.md),
normalization (docs/NORMALIZATION.md),
plan (docs/IMPLEMENTATION_PLAN.md).

## Development

    make fmt
    make test
    make race
    make vet
    make build-all

The daemon remains usable without any frontend running.

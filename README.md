# GoBroom

GoBroom is a local AI gateway daemon with an OpenAI-compatible HTTP data plane
and a separate local control plane. The daemon owns configuration, routing,
provider execution and runtime state; the CLI and TUI are clients and are not
required while serving requests.

The product vision is a lightweight, portable gateway that makes provider
connections, discovered routes, physical models and combo models easy to
manage without conflating those layers.
SQLite, stdout/journal logging and loopback-only serving are simple defaults,
not assumptions the domain is built around. See [the vision](docs/VISION.md)
and [roadmap](docs/IMPLEMENTATION_PLAN.md) for boundaries and current gaps.

The project focuses on the useful provider/model-routing workflow: configure a
provider node and its connections, discover provider routes, group equivalent
routes into physical models, compose role/use-case combos, then expose the
model names clients may discover and use.

## Implemented today

- Go daemon, SQLite state, Unix-domain-socket IPC and optional HTTP control API;
- loopback HTTP data plane, default `127.0.0.1:2712`;
- `/v1/models`, `/v1/chat/completions`, `/v1/responses` and `/v1/messages` routes;
- provider nodes, multiple credential connections, model discovery and custom
  catalog entries;
- logical model references, nested combos and explicit public-model publishing;
- JSON provider manifests composed from registered endpoint, auth, codec,
  discovery, quota and error-classification primitives;
- OpenAI Chat, OpenAI Responses and Anthropic Messages adapters, with the
  protocol/streaming limits described in the compatibility matrix;
- immutable route snapshots, connection-aware scheduling, health cooldowns,
  persisted quota snapshots, and asynchronous compact usage events;
- Cobra CLI and an alternate-screen Bubble Tea dashboard with independently
  framed panes, basic filtering/editors and daemon-only IPC access. This is an
  early UX slice: the target Models workspace will manage Discovered, Physical
  and Combos as separate tabs, with LazyGit-style block/item/tab/depth
  navigation. See [the TUI UX contract](docs/TUI_UX.md).

The existence of an endpoint or an adapter does not imply complete protocol
parity. In particular, OAuth refresh flows, comprehensive cross-protocol SSE
semantics, provider-specific quota integrations and a finished interactive TUI
remain unfinished; see [current status](docs/IMPLEMENTATION_PLAN.md).

## Build and run

Requirements: Go 1.27+ and Unix domain sockets on Unix-like systems.

```sh
make test
make build-all
make run-daemon
```

The daemon stores state in the user configuration directory, serves the data
plane on loopback port `2712`, and uses a Unix socket for control IPC. The HTTP
control API is optional and defaults to loopback port `2713` when enabled.

```sh
gobroom status
gobroom providers list
gobroom models list
gobroom-tui
```

The HTTP control API and provider data plane are separate surfaces. A frontend
is never responsible for keeping the daemon alive or maintaining its SQLite
state.

## Project map

```text
cmd/gobroomd        daemon entry point
cmd/gobroom         CLI control client
cmd/gobroom-tui     TUI control client
internal/api        HTTP data/control handlers
internal/daemon     lifecycle, IPC and service wiring
internal/kernel     immutable route snapshot, scheduler and execution contract
internal/normalize  inbound semantic request normalization
internal/adapter    protocol adapters
internal/provider   provider primitives, manifests and registries
internal/controlplane snapshot construction and resolution
internal/store      SQLite persistence
internal/runtime    health and quota runtime policy
internal/usage      bounded asynchronous usage events
manifests/builtin   built-in provider definitions
docs/               architecture, contracts, status and decisions
```

## Documentation

Start at [the documentation index](docs/README.md). The implementation plan is
the status source of truth; design documents define intended contracts and
must not be read as a claim of complete feature parity.

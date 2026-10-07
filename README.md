# GoBroom

**Many providers. One handle.**

GoBroom brings provider accounts, model sources and routing policies under one
daemon. Manage each upstream connection, organize models around stable
identities, compose role-based models, and serve the names you choose through
one gateway.

```text
providers and connections
          ↓
discovered routes → Physical models → optional Combos
                           │                  │
                           └──── expose ──────┘
                                    ↓
                          one client endpoint
```

## Manage providers and connections

A provider describes how to reach an upstream service. A connection represents
an account or credential for that provider. Keep multiple connections separate
so each can have its own credentials, priority and model availability.

Discover model IDs from a connection or add a route manually. GoBroom preserves
the exact upstream ID used to call the provider and tracks which connection
reported it. Discovery gives you source inventory to review; it does not decide
that two routes are the same model or expose them to clients automatically.

## Organize models around identity

Provider prefixes and model IDs often vary even when routes refer to the same
underlying model. GoBroom keeps those route details while letting you manage a
stable Physical model identity:

```text
xkiro/qwen/qwen3.7-max:free ─┐
ocg/qwen3.7-max              ├── qwen-3.7-max
g4f/Qwen:qwen3.7-max        ─┘
```

Each Physical model groups routes you consider equivalent and retains the
identity evidence, capabilities and limits used to evaluate them. Unknown
evidence stays unknown: similar names alone do not make routes interchangeable.
Availability also belongs to a connection, so one account is not assumed to
access every route discovered through another.

Build **Combos** from ordered Physical or Combo members to represent a role or
use case, such as `junior`, `senior` or `tech-lead`. Choose a routing strategy
for each Combo and keep the policy at that layer. A nested Combo retains its own
strategy when used as a member; the model graph is not flattened into a list of
provider strings.

Physical models and Combos are both routable models. Choose which ones clients
can see by exposing them; `/v1/models` is the projection of that choice, not a
separate alias or publishing catalog.

## Route requests with policy

GoBroom first checks whether a route can satisfy the request, including its
operation, tools, modalities, context and reasoning requirements. It then
applies the strategy at each model layer to order eligible members and sources.
Fallback, rotation and weighting are explicit policies, so a role model can
combine models without losing their individual routing rules.

Real requests provide health, limit and performance evidence. Authentication
failures, unavailable models, rate limits, quota windows and transient errors
have different effects on future selection. Runtime feedback can guide route
ordering without changing the priority you configured. Synthetic health checks
and quota polling are opt-in by default.

Fallback ends once a response is committed to the client; a stream never
switches models halfway through. Client and provider protocols may differ, but
GoBroom makes translation compatibility explicit and does not silently accept
unsupported or materially lossy request paths.

## One gateway, independent control

The daemon owns configuration, routing and request serving. CLI and TUI clients
manage the same state through local IPC; neither needs to stay open for the
gateway to serve requests. The HTTP data plane is separate from the optional
HTTP control API, so serving inference does not require exposing management
access.

The data plane provides OpenAI Chat Completions, OpenAI Responses, Anthropic
Messages and `/v1/models` endpoints. Requests are normalized into a shared
invocation, routed through provider-specific codecs, and rendered for the
client protocol. This boundary lets clients use one gateway while provider
connections retain their own wire formats and credentials.

- **TUI:** manage providers, connections, discovered routes, Physical models
  and Combos with keyboard-driven navigation and search.
- **CLI:** inspect, automate and administer the same daemon over IPC.
- **Configuration bundles:** export and review a versioned, secret-free
  configuration diff, then apply it atomically. Credentials remain separate.
- **Local-first operations:** SQLite, loopback HTTP, Unix-domain control IPC
  and structured stdout/stderr logging work without external services. Under
  systemd, logs are available through `journalctl`.

## Get started

Requirements: Go 1.27+ and Unix-domain socket support on Unix-like systems.

```sh
make build-all
./gobroomd
```

In another terminal:

```sh
./gobroom                         # open the TUI
./gobroom status                  # query the daemon
./gobroom providers catalog       # inspect provider setup metadata
./gobroom extensions catalog     # inspect extension schemas
./gobroom --help
```

The default data-plane address is `http://127.0.0.1:2712`. The optional HTTP
control API uses `127.0.0.1:2713` when enabled. Install the CLI and daemon with
`make install-all`.

```sh
make test
make build-all
```

## Further reading

- [Product vision](docs/VISION.md)
- [Product use cases](docs/USE_CASES.md)
- [Model management in the TUI](docs/TUI_UX.md)
- [Routing policies](docs/CORE_POLICIES.md)
- [Compatibility matrix](docs/COMPATIBILITY_MATRIX.md)
- [Operations](docs/OPERATIONS.md)

## Acknowledgements

GoBroom draws inspiration from [9router](https://github.com/decolua/9router)
for its provider routing and model-management concepts.

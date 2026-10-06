# GoBroom

**One model lineup for every provider you use.**

GoBroom brings provider connections, model catalogs and routing policies into a
single lightweight gateway. Give equivalent upstream routes one stable identity,
compose those models into roles such as `junior` or `tech-lead`, and expose the
names your tools should use. The daemon serves requests independently; the
bundled CLI and keyboard-first TUI are simply ways to operate it.

```text
provider connections → discovered routes → physical models → role combos
                                                        └→ /v1/models
```

## Models that match how you think

Provider model IDs often encode account, gateway and product-specific prefixes.
Those strings matter when calling an upstream, but they are not the model you
want to manage:

```text
xkiro/qwen/qwen3.7-max:free ─┐
ocg/qwen3.7-max              ├── qwen-3.7-max
g4f/Qwen:qwen3.7-max        ─┘       │
                                     ├── junior
                                     ├── senior
                                     └── tech-lead
```

GoBroom keeps the layers distinct and connected:

- **Discovered routes** retain the exact upstream model ID and the connection
  that reported it. Discovery is evidence, not an assumption that every account
  can use every model.
- **Physical models** give equivalent routes one canonical identity. Their
  sources carry explicit identity/fidelity evidence and capability profiles,
  including context limits, modalities and reasoning support.
- **Combos** are real, routable models made from ordered Physical or Combo
  members. Their strategy, fallback behavior and member options are typed
  policy—not hidden strings or a second publishing system.

Manage each layer directly, while moving through the model graph in one
workflow. Search source prefixes when curating a Combo, see the canonical model
identity, and decide which Physical models and Combos are discoverable. The
public model list is a projection of that choice; there is no separate alias
catalog to keep synchronized.

## Route by what a request needs

Before fallback or ranking, GoBroom evaluates whether a route can satisfy the
request: operation, tools, modalities, context, reasoning intent and continuity.
Unknown capability evidence stays unknown; it is not silently treated as
support. Strategy then selects among eligible routes using the policy configured
at each model layer.

Health and priority come from actual traffic. Typed outcomes distinguish
authentication failures, unavailable models, throttling, quota exhaustion and
transient upstream errors. Reset windows, latency and throughput can inform
eligibility and bounded adaptive ordering. Synthetic health checks and quota
polling are opt-in, not background noise on the default path.

Retries and fallback stop when a response is committed to the client. A stream
does not switch models halfway through. Translation compatibility is explicit:
unsupported or materially lossy paths are rejected unless the model policy
allows them.

## A semantic boundary between clients and providers

Clients and upstreams do not have to share a wire protocol. GoBroom decodes
requests into a typed invocation, routes that semantic request, and converts
the response through canonical events:

```text
client wire → ingress codec → typed invocation → routing policy
            → provider composition → semantic response events
            → client renderer → client wire
```

The request contract keeps prompt layers, tools, thinking effort/budget,
continuity state, generation options and hard requirements distinct. Provider
signatures and opaque continuation artifacts remain scoped to the provider and
session that issued them. This gives provider codecs room to translate only what
they understand and report what cannot be preserved.

The data plane includes OpenAI Chat Completions, OpenAI Responses,
Anthropic Messages and `/v1/models` endpoints. Provider definitions compose
reusable primitives for endpoint resolution, transport, authentication,
request encoding, response decoding, model discovery, error classification,
usage and quota evidence. A provider-specific quirk belongs behind one of those
contracts; it does not require a provider-name branch in the routing kernel.

## A daemon you can operate your way

- **Daemon-first:** `gobroomd` owns configuration and runtime state. Serving
  requests does not depend on an open TUI or CLI.
- **Keyboard-first TUI:** move between named management panes, search and edit
  models, connections and combos without treating the screen as a dashboard.
- **CLI:** inspect, automate and administer the same domain through local IPC.
- **HTTP gateway:** connect existing clients to the data plane; optionally
  protect remote access with a bearer token and TLS at the deployment boundary.
- **Portable configuration:** export a versioned, secret-free bundle, review a
  diff, then apply it atomically. Credentials remain separate from the bundle.
- **Lightweight operations:** SQLite, loopback HTTP, Unix-domain control IPC and
  structured stdout/stderr logging work without external services. Under
  systemd, logs are available through `journalctl`.

## Get started

Requirements: Go 1.27+ and Unix-domain socket support on Unix-like systems.

```sh
make build-all
./gobroomd
```

In another terminal:

```sh
./gobroom           # open the bundled TUI
./gobroom status    # query the daemon
./gobroom --help
```

The default data-plane address is `http://127.0.0.1:2712`. The optional HTTP
control API uses `127.0.0.1:2713` when enabled. The CLI and daemon can also be
installed with `make install-all`.

```sh
make test
make build-all
```

## Project status

GoBroom's product and extension contracts are documented independently from
feature maturity. The roadmap and compatibility matrix identify what is
implemented, partial or still being validated; this README describes the
intended product, not a claim that every provider or protocol edge case is
already interchangeable.

- [Implementation roadmap](docs/IMPLEMENTATION_PLAN.md)
- [Compatibility and verification status](docs/COMPATIBILITY_MATRIX.md)
- [Product vision](docs/VISION.md)
- [Solution architecture](docs/SOLUTION_ARCHITECTURE.md)
- [Physical model contract](docs/PHYSICAL_MODELS.md)
- [Passive health and adaptive routing](docs/PASSIVE_HEALTH_ROUTING.md)
- [Provider definitions](docs/PROVIDERS.md)
- [Normalization contract](docs/NORMALIZATION.md)
- [Operations and remote hosting](docs/OPERATIONS.md)
- [TUI interaction model](docs/TUI_UX.md)

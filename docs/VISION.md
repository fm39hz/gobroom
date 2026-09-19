# GoBroom vision

GoBroom is a lightweight, portable AI gateway daemon for people who use many
providers but want one clean way to configure, compose and serve models.

> Bring provider connections together, turn physical models into useful
> abstractions, and expose only the names you choose through a fast,
> OpenAI-compatible endpoint.

GoBroom takes inspiration from the useful routing and model-management
capabilities of 9router. It is not a goal to reproduce every adjacent product
feature or implementation choice. The product should make the core workflow
clearer, keep the serving process small, and remain manageable without a UI
process running.

## The user workflow

```text
configure provider and connection
  -> test endpoint/auth and discover provider model sources
  -> map equivalent source routes into a physical model identity
  -> compose physical/combo models into role/use-case combos
  -> expose a model directly when clients should see it
  -> use one compatible endpoint
```

For example, provider sources such as `xkiro/qwen/qwen3.7-max:free`,
`ocg/qwen3.7-max` and `g4f/Qwen:qwen3.7-max` can be variants of the single
physical model `qwen-3.7-max`. Prefixes and upstream spelling identify source
routes; they do not define separate user-managed models. Discovery is a starting
point, not an authority: matching may suggest canonical identities, but users
must be able to review and correct it. Reordering and search should be
keyboard-first and practical in the TUI; the CLI must also support automation
and headless administration.

## One model domain, separate management layers

The model workflow has three distinct layers:

```text
discovered provider routes
  -> physical model identity
  -> combo model with an execution policy
```

They belong to one model graph but require separate management operations. A
physical model such as `qwen-3.7-max` gathers equivalent provider routes. A
combo such as `junior` is a routable model whose ordered members and strategy
encode a role or use case. The TUI therefore presents Discovered, Physical and
Combos as separate tabs in one Models workspace instead of merging their rows.

Exposure remains a property of a physical/combo model. `/v1/models` is a
projection of exposed models, not a second publication object users must create
and synchronize. A combo is a real model and is discoverable through
`/v1/models` when exposed.

Provider `/models` results remain source catalog data. Users reach them while
adding or reviewing model routes; the raw catalog is not the physical-model
list. A physical model's detail view shows its routes, while a combo detail
shows its members; both use stable names without provider prefixes or database
IDs.

## Product principles

1. **Daemon first, UI optional.** The daemon owns configuration and runtime
   state. HTTP serves provider clients; local IPC serves control clients. No
   frontend must be running for inference or background service operation.
2. **Small serving core, complete core workflow.** Keep routing, provider
   execution and policy on the hot path lean. Put editors, export/import,
   diagnostics and integrations at control-plane or optional-worker
   boundaries.
3. **Useful defaults, replaceable boundaries.** SQLite remains the simple
   zero-setup default. Logging to stdout/stderr works naturally with
   `journalctl`. Storage, log export, listener auth and deployment settings
   must not be conceptually tied to those defaults.
4. **Portable by explicit support, not by slogan.** Keep domain contracts
   independent of SQLite and systemd, but only claim a backend/platform after
   it has implementation and tests. Do not promise arbitrary database
   compatibility.
5. **One graph, typed layers.** Discovered routes, physical models and combos
   are distinct node/reference types in one validated graph. They are managed
   separately, while exposure stays on routable model nodes. Import/export/sync
   uses that graph rather than copying persistence tables or creating a second
   publication catalog.
6. **Secrets are a separate concern.** Configuration bundles and diffs should
   not leak credentials. Secret transport/storage is explicit and can differ
   between local and hosted deployments.
7. **Providers are compositions.** Built-in and custom providers use the same
   typed primitive vocabulary. The kernel does not branch by provider name;
   new behavior is implemented once as a reusable primitive.
8. **Operational data has a job.** Health, quota and usage should inform routing,
   budgets or diagnosis—not exist only to fill a dashboard. They are bounded
   and must not stall response delivery.
9. **Secure remote use is a supported deployment shape.** The data-plane
   listener can be hosted remotely with explicit authentication and transport
   protection. The control plane stays independently configurable and is not
   exposed merely because the inference endpoint is.

## Configuration and portability boundaries

### Configuration

There is one logical configuration contract regardless of whether it is
edited through the TUI, CLI, HTTP control API or a future declarative file.
Changes should have a previewable diff, schema/version validation and atomic
application. First support deterministic export/import and explicit
single-writer sync; multi-writer conflict resolution is a separate design
problem, not an implied property of “sync”.

Provider definitions are reusable defaults; user nodes, connections and model
graphs are user configuration. Connection secrets are not copied into a
portable config export by default.

### State storage

SQLite is the initial local backend and must remain easy to install and back up.
The domain/control contracts should not expose SQLite-specific types. A
different SQL backend (for example, PostgreSQL for a hosted deployment) may be
added when a real deployment requires it. Each supported backend needs
transaction, migration, concurrency and recovery tests. “Any database” is not
a compatibility promise.

Configuration, ephemeral health/cooldown state, quota snapshots and usage
history may have different durability/retention requirements. A storage
boundary should preserve those distinctions instead of forcing every kind of
state into one generic blob or one synchronization policy.

### Logs and remote operations

Structured logs go to stdout/stderr by default; systemd captures them for
`journalctl`. Remote log delivery should use a standard exporter/protocol or
external collector where possible, rather than making an Elasticsearch client
a mandatory daemon dependency. Remote API keys authenticate clients to
GoBroom; provider credentials authenticate GoBroom to upstreams. Those are
separate identities and policies.

## Explicit non-goals for the core

- reproducing the dashboard or every feature of 9router or its forks;
- MITM/DNS interception, IDE credential spoofing, tunnels or a hosted cloud-sync service;
- implementing every database and log vendor before there is a concrete need;
- multi-master configuration editing without a conflict model;
- making the TUI a runtime dependency;
- adding middleware or model transformations that silently alter user input.

These features may be separate integrations if demand appears, but they must
not enlarge or serialize the inference kernel.

## Product acceptance test

GoBroom is succeeding when a user can configure and test a provider, discover
and curate its physical models, compose and publish useful abstractions, and
serve them through a protected compatible endpoint—while the daemon stays
responsive, the TUI remains optional, configuration can be moved deliberately,
and operational output integrates with the host environment.

Milestone sequencing and current completion state are in the
[implementation roadmap](IMPLEMENTATION_PLAN.md). This vision defines why that
work matters; the roadmap defines what exists and what remains.

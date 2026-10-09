# Product use cases and acceptance

This document records the user journeys GoBroom is meant to solve, the
observable success condition for each journey, and what the repository can
actually do today. It is normative for product scope; [the implementation
roadmap](IMPLEMENTATION_PLAN.md) remains authoritative for milestone status.
An endpoint, schema field, or architectural abstraction alone does not count as
a complete use case.

## Product boundary

GoBroom's core user is someone who has several upstream provider accounts and
wants one local daemon and one OpenAI-compatible endpoint. The daemon must be
useful without the TUI running. The TUI is the preferred interactive control
surface; the CLI is the automation/headless surface. Both are clients of the
same daemon-owned state.

### Terms used in these journeys

- **Provider definition:** reusable manifest composition of endpoint,
  transport, authentication, codecs, discovery and classifiers.
- **Provider node:** one configured provider identity/prefix/base endpoint in
  this installation.
- **Connection:** one account/credential attached to a provider node; multiple
  connections may expose the same upstream model.
- **Discovered route:** one opaque upstream model ID, executable through a
  connection only when its entitlement evidence allows it. Prefixes are source
  labels/search keys, not model identity.
- **Connection entitlement:** evidence that one enabled connection returned a
  route from its model-list operation. A complete snapshot may record
  `not_listed`; an incomplete snapshot only adds positive `available` evidence.
  No record is `unknown`, not an implicit entitlement.
- **Physical model:** canonical identity that groups source routes known or
  declared to be equivalent, together with evidence and capability projections.
- **Combo:** a named model assembled from ordered Physical/Combo members and a
  strategy primitive (fallback, rotation, weighting, etc.).
- **Exposure:** `discoverable` on a Physical/Combo record; `/v1/models` is the
  projection of exposed routable models, not a separate publish catalog.

The central model journey is:

```text
provider definition
  -> one or more credential connections
  -> discovered/custom upstream route IDs
  -> Physical identity grouping equivalent routes
  -> ordered role/use-case Combo graph
  -> discoverable model names exposed by /v1/models
  -> inference over eligible routes and nested fallback policies
```

Example:

```text
discovered routes:
  xkiro/qwen/qwen3.7-max:free
  ocg/qwen3.7-max
  g4f/Qwen:qwen3.7-max
             │ user groups equivalent upstream models
             ▼
Physical: qwen-3.7-max
             │ used as one ordered member in a role model
             ▼
Combo: junior
             │ discoverable=true
             ▼
GET /v1/models  -> junior
```

Prefixes identify routes and support search; they are not part of the Physical
identity. A Combo is a routable model in its own right, not a separate
publication record. Physical models and Combos can each be managed and exposed
independently.

## Status vocabulary

- **Supported** — the whole described journey has an implementation path and
  relevant tests; this does not imply all provider variants or edge cases.
- **Partial** — some steps work, but at least one user-visible step or semantic
  guarantee is missing.
- **Not yet supported** — there is no complete path in the current product.
- **Outside core** — deliberately not required of the daemon/model-routing
  product; a sidecar may serve it later.

## Use-case catalog

### A. Run and connect

| ID | User intent | Successful outcome | Current status |
|---|---|---|---|
| UC-01 | Install and run GoBroom as a background service without opening the TUI | `gobroomd` owns SQLite, snapshots, request execution and runtime observations; CLI/TUI may exit without interrupting serving | **Supported** — local daemon/service path is implemented; verify platform-specific service integration separately |
| UC-02 | Point an OpenAI-compatible client at GoBroom | Client discovers only exposed models and sends requests to the configured data-plane listener | **Supported for core API** — loopback `/v1/models`, `/v1/chat/completions`, `/v1/responses`, `/v1/messages`; each protocol's semantic coverage differs |
| UC-03 | Host the data plane remotely without exposing management controls | Protect remote inference separately from local IPC/control access; preserve SSE flush and cancellation | **Partial** — bearer token and listener boundary exist; TLS/reverse-proxy deployment is a documented configuration, not a built-in hosted service |
| UC-04 | Replace the current UI or automate administration | Any control client can query/change the same daemon state without opening SQLite | **Supported at the contract level** — CLI/TUI use IPC; full third-party client SDK/stable public schema is not yet promised |

### B. Add providers and accounts

| ID | User intent | Successful outcome | Current status |
|---|---|---|---|
| UC-10 | Pick a prebuilt provider or add a custom OpenAI-compatible provider | Provider definition chooses endpoint, transport, auth, codecs and discovery; ordinary endpoint/auth changes need no kernel edit | **Partial** — typed executable manifests and built-ins exist; not every built-in provider has complete operations/fixtures |
| UC-11 | Create multiple accounts/connections for one provider | Each account owns its secret, priority, enablement and independent discovered-model availability | **Implemented in route/storage core** — discovered routes require positive per-connection evidence; unknown connections are not assumed entitled |
| UC-12 | Check that a specific connection can reach its provider model-list API | See a bounded model preview, safe endpoint summary and success/failure for the chosen account | **Partial** — `connections.test --connection-id` and TUI `t` on a connection query the manifest's active model-list source with that exact enabled credential and do not mutate catalog state. This is not an inference dry-run; static catalogs cannot claim a successful endpoint test |
| UC-13 | Review and import model IDs from one connection's `/models` catalog | Preview, select a subset, and retain the selected account's complete/partial availability evidence independently of which IDs were added to inventory | **Implemented in service/CLI/TUI path** — exact model IDs are validated against that connection's fresh response; an empty selection can apply entitlement evidence without importing route IDs |
| UC-14 | Connect an OAuth-backed account | Daemon owns authorization state, callback/PKCE, token exchange, refresh and secret persistence; TUI is not required during refresh | **Partial** — definition-bound refresh/persistence/locking exist; interactive authorization start/callback and live OAuth onboarding are missing |
| UC-15 | Handle accounts that expose different model catalogs or entitlements | Discovered routes are executable only through accounts with positive evidence; complete snapshots may gate absent models, while incomplete snapshots never create false negatives | **Implemented in persistence and route expansion** — no observation is unknown/excluded; complete scans record `not_listed`, partial scans add positive evidence only; the Discovered inspector shows per-connection status. Existing development DB discoveries remain unknown until each connection is reviewed/refreshed; no automatic entitlement backfill is claimed |

Provider definitions describe reusable behavior; credentials belong to
connections. A provider definition is not a second user-maintained provider
implementation. If a dialect requires new behavior, implement one reusable
primitive and bind it in the definition.

The provider-row `t` shortcut is distinct from the account-specific workflow:
it refreshes/imports using the highest-priority enabled connection. For a
chosen account, test that endpoint first, inspect the read-only preview, then
import with the same connection. Discovery is inventory only; it does not
auto-group routes into Physical models or expose them to clients.

### C. Curate discovered routes into Physical models

| ID | User intent | Successful outcome | Current status |
|---|---|---|---|
| UC-20 | See only model routes discovered for a provider/account | Rows retain opaque upstream model IDs and connection identity; provider prefix is available for search/source attribution | **Supported in the domain/control layer**; presentation and review flow are still partial |
| UC-21 | Add a model that `/models` does not return | A custom route can be entered with exact upstream ID and appears in Discovered management | **Supported** — CLI/store path exists |
| UC-22 | Treat several provider routes as one actual model | Discovery suggests equivalent IDs, user confirms/corrects the grouping once, and one canonical Physical name such as `qwen-3.7-max` owns `xkiro/...`, `ocg/...`, `g4f/...` source routes | **Partial** — selecting multiple routes now suggests a conservative canonical name only when all selected IDs agree and opens a review editor; a matching existing Physical is merged without overwriting its policy. Broader equivalence suggestions across unselected routes and a split/review workflow remain |
| UC-23 | Inspect Physical capabilities/limits before using it | See which sources support vision/tools/reasoning, context/input/output limits and reasoning controls; guaranteed versus merely available capabilities stay distinct and unknown values are not guessed | **Partial** — typed profile/projection and IPC data exist; broader provider evidence and ergonomic comparison remain |
| UC-24 | Search/select sources by a known prefix while editing models | Typing `orca/`, `ocg/` or `g4f/` filters provider route candidates; filtered display never changes membership or order | **Partial** — local view filtering exists; the polished member/candidate workflow and its acceptance tests remain |

Physical identity is independent from Combo membership. One Physical may gather
many source routes; a Combo may contain Physical models, other Combos, or both.
Unknown source equivalence/capability remains unknown unless the user or
upstream provides evidence.

### D. Compose and expose role/use-case Combos

| ID | User intent | Successful outcome | Current status |
|---|---|---|---|
| UC-30 | Build role models such as `intern`, `junior`, `senior`, `tech-lead` | Combo contains an explicit ordered list of Physical/Combo members and has an execution strategy | **Supported in the typed model/CLI/control layers**; full TUI composition usability is partial |
| UC-31 | Search available Physical models while editing a Combo | Filter unique Physical candidates by canonical name, capability or underlying source prefix/upstream ID; matching `orca/` still renders one canonical row such as `deepseek-v4-flash`, never a provider-route duplicate. Nested Combo candidates remain searchable by their Combo/member names | **Physical prefix behavior implemented and unit tested** — the complete live key sequence through edit, reorder, save and exposure still needs end-to-end acceptance |
| UC-31a | Edit Combo membership without accidentally changing fallback order | Existing member order survives filtering and save; adding appends, removing removes, and explicit move keys reorder only the selected member | **Implemented and integration-tested** — `K/J` reorders the explicit sequence, typed order reaches daemon, and the separate UC-33 exposure step verifies `/v1/models` projection |
| UC-31b | Manage Combo candidates and fallback order without leaving the editor | Separate ordered-member and candidate panes preserve each cursor/filter; inspect a model, add/remove it, reorder explicitly, then save | **Implemented and exercised through Tea key events → IPC → SQLite/control snapshot → `/v1/models`**; picker and order/filter behavior also have focused tests |
| UC-32 | Choose fallback/rotation/weighting behavior for a Combo | Search registered strategies by name/description, inspect semantics and option schema, select one, and edit only valid options; nested member policy stays intact | **Implemented for built-ins** — one canonical registry supplies runtime primitive, display metadata, supported options/defaults and validation; stickyLimit now reaches hierarchical round-robin scheduling |
| UC-32a | Set weight for one Combo member | A member's weight is a typed property of that model-reference edge; weighted strategy consumes it, while other strategies ignore it | **Implemented in typed storage/snapshot/TUI path** — persisted in member `options_json`, shown and changed with `+/-` in the ordered-members pane, covered through IPC and kernel snapshot tests |
| UC-32b | Edit a Combo strategy without losing its options | Change the strategy primitive or its JSON-bound options while preserving all member and exposure state | **Implemented for built-ins** — selecting another strategy applies its declared defaults, options round-trip, and typed schema errors reject save without discarding form state |
| UC-32c | Know what a strategy option actually does before enabling it | Only registered, effective options are shown; invalid/unknown keys fail before snapshot publication, and configured state affects the matching primitive | **Implemented for built-ins** — registry catalog is available via IPC/CLI/TUI; option types, bounds and defaults are validated on edit/store and snapshot construction, with unknown IDs/keys rejected |
| UC-33 | Expose `junior` but keep internal Physical models hidden | Set `discoverable` on the Combo; no separate publish/alias object needs synchronization | **Supported and tested end to end** — TUI `p` updates Combo through IPC, and only exposed names appear in the OpenAI model list |
| UC-34 | Also expose a Physical model directly when useful | Set discoverability on that Physical independently of any Combo | **Supported in the domain/API projection**; verify in the interactive UI workflow |

Filtering in the Combo editor is a navigation aid. It must not silently mutate
the ordered membership list, become an implicit execution predicate, or hide
selected-but-filtered members. Execution fallback/selection remains explicit
strategy configuration.

### E. Consume models and route requests

| ID | User intent | Successful outcome | Current status |
|---|---|---|---|
| UC-40 | Call the exposed Combo name from an OpenAI-compatible client | `/v1/models` returns the selected name; a chat request resolves that model graph and executes an eligible source route | **Supported for tested OpenAI Chat path**; cross-provider/protocol parity is partial |
| UC-40a | Use an OpenAI API key through an exposed GoBroom model | A physical/combo model reaches OpenAI Chat using the connection's Bearer key; non-stream JSON, SSE and usage/fallback behavior follow the documented contract | **OpenAI-compatible path live-tested through OpenRouter** — connection `/models` discovery, one current free-model import, exposed Combo, JSON and SSE inference passed; provider reported cost 0. Direct OpenAI-vendor account acceptance remains unverified. |
| UC-40b | Use an Anthropic API key from an Anthropic Messages client | `POST /v1/messages` preserves Anthropic request/response semantics and reaches the Anthropic Messages operation using `x-api-key` and `anthropic-version` | **Fake-upstream vertical fixture passes** — verifies `/v1/messages`, connection key, Anthropic version header, model rewrite and response. Anthropic `/v1/models` discovery uses the same API key and cursor contract. No live Anthropic credential is configured yet. |
| UC-41 | Send an image/tool/thinking request to a Combo | Candidate eligibility accounts for requested capabilities and the chosen upstream preserves semantics or rejects unsupported input clearly | **Partial** — typed eligibility and Gemini/OpenAI/Anthropic slices exist; modality and reasoning coverage is not complete |
| UC-42 | Fall back when an upstream fails before output begins | Strategy tries the next eligible member/connection for retryable/auth/rate conditions according to the explicit policy | **Supported for core policies** — fixtures cover 429/401 fallback and terminal 400 handling |
| UC-43 | Avoid switching providers after a stream has started | A malformed/truncated stream returns an error; no second provider is allowed to splice a different response into committed output | **Supported and tested**, including Gemini runtime binding |
| UC-44 | Reuse affinity/continuity for a client session | Session affinity helps keep a request on its last successful route; provider protocol state persists only when an explicit session key exists | **Partial** — runtime affinity and OpenAI Responses continuity exist; provider/harness-specific coverage remains |

### F. Learn from real attempts, not constant probes

| ID | User intent | Successful outcome | Current status |
|---|---|---|---|
| UC-50 | Distinguish invalid credentials, model-not-found, rate limit, quota exhaustion and provider capacity | Real response headers/body become a typed cause, scope, confidence, retry deadline and optional limit window | **Partial** — generic JSON/header evidence and typed manifest field paths work; provider-specific error forms and precedence are incomplete |
| UC-51 | Stop selecting an exhausted route until its reset | Remaining/reset evidence gates route selection; reset expiry reopens it without a background healthcheck | **Supported for evidence represented by current generic/manifest options**, including daemon restart fixture |
| UC-52 | Improve order for routes that actually succeed and respond quickly | User order remains durable; bounded runtime feedback may improve candidates only after eligibility | **Supported in core policy**, with broader real-provider/load characterization remaining |
| UC-53 | Inspect why a route is blocked or ordered below another | Show cause, evidence source, scope, deadline and effective ordering | **Partial** — health/quota/route explanation IPC exists; TUI diagnostic presentation is incomplete |
| UC-54 | See usage as more than a dashboard | Usage/latency evidence is persisted and informs diagnosis/performance; quota/limit evidence gates routing | **Partial** — compact usage pipeline and core quota gating exist; detailed cost/request accounting/provider usage reports remain |

The daemon must not run provider pings to manufacture health. Periodic quota
polling is opt-in; quota APIs may be queried opportunistically after an actual
quota/rate-limit outcome. Observability and persistence must not hold an SSE
response open.

### G. Move, inspect and operate the installation

| ID | User intent | Successful outcome | Current status |
|---|---|---|---|
| UC-60 | Operate entirely from keyboard in a focused, LazyGit-like TUI | `h/l` changes block, `j/k` moves only inside it, `Enter` enters a context, `Esc` returns, `Space` selects, `/` filters; help is contextual | **Partial** — top-level grammar/layout exists, final editor/picker usability is not complete |
| UC-61 | Automate setup or inspect a headless daemon | CLI uses daemon IPC for provider, connection, model, config, health, quota, usage and log operations | **Partial** — many commands exist; interactive provider testing and some model-composition workflows are missing |
| UC-62 | Review a risky configuration change before applying it | Validate a versioned bundle, inspect a typed diff, then apply atomically so invalid routes never replace the active snapshot | **Supported for single-writer bundles** |
| UC-63 | Diagnose a service under systemd or remote deployment | Journal receives stdout/stderr logs; data plane can be protected independently from the local control plane | **Supported/documented for the current Linux service shape**; other platforms and remote deployments need their own verification |

### H. Portability and deployment

| ID | User intent | Successful outcome | Current status |
|---|---|---|---|
| UC-70 | Move a setup to another machine without copying SQLite internals | Use the UC-62 bundle workflow on the destination; secrets remain local and stable connection IDs preserve their association | **Supported for single-writer bundles** |
| UC-71 | Use another database backend for a hosted deployment | Select a named repository backend with tested transactions, migrations, concurrency and recovery | **Not yet supported** — SQLite is the only tested implementation; portability is an architectural boundary, not a claim that any database can be plugged in today |
| UC-72 | Send daemon output to journald or an external log platform | Keep logs structured on stdout/stderr so service manager or external collector owns delivery | **Supported for host journal capture**; direct Elasticsearch/hosted log-sink integration is not a core feature |
| UC-73 | Host inference remotely while keeping management local | TLS/access policy is enforced at a reverse proxy, GoBroom bearer auth protects data plane, IPC/control API stays local | **Partial/documented** — remote boundary exists; production proxy/TLS deployment must be verified by the operator |
| UC-74 | Synchronize edits from multiple writers | Concurrent changes merge without silently losing model/order/secret updates | **Not supported** — bundle import/export is explicit single-writer workflow; no multi-master sync protocol is promised |

## Worked core workflow

This is the target flow using the example names from the model-management
mental model. It intentionally distinguishes catalog route IDs, Physical
identity and Combo identity:

```text
1. Add connections:
   OpenRouter/account-1, Orca/account-1, OCG/account-1, g4f/account-1

2. Discover route IDs or enter missing IDs:
   openrouter/deepseek/deepseek-v4-flash-0731:free
   orca/deepseek/deepseek-v4-flash-free
   bai/deepseek-v4-flash
   ...

3. Curate one Physical model:
   deepseek-v4-flash
     ├─ openrouter/deepseek/deepseek-v4-flash-0731:free
     ├─ orca/deepseek/deepseek-v4-flash-free
     └─ bai/deepseek-v4-flash

4. Compose role Combos:
   junior    = [deepseek-v4-flash, glm-5.3-flash, ...]
   middle    = [claude-opus-4.8, gpt-5.6-luna, ...]
   tech-lead = [qwen-3.8-max, gpt-5.6-sol, ...]

5. Expose chosen names on their own model records:
   /v1/models -> junior, middle, tech-lead
```

In a Physical editor, the source picker shows the provider routes it groups.
In the Combo editor, the candidate pane contains unique Physical/Combo models.
After pressing `/` to filter, typing `orca/` may match a Physical model through
one of its source routes, but the candidate remains one prefix-free canonical
row; the route is a match reason/detail, not a separate Combo member. `Space`
selects, `j/k` moves within
a list, `h/l` changes pane/block focus, and explicit move actions set member
order. Filtering never changes membership or order. Inference uses the Combo's
strategy primitive and each nested member's own strategy. Only failures before
the first client byte may advance to another route; a committed stream is
never spliced with a second provider's output.

The current store/kernel support the typed graph and `/v1/models` projection.
The end-to-end TUI editor still needs validation; the walkthrough is an
acceptance scenario, not a statement that this entire interaction is shipped.

## End-to-end usability gates

These are product acceptance tests, not just implementation checkboxes:

1. **Onboard one provider:** create/select definition → add connection → test
   that exact connection → review the full `/models` list → import selected IDs
   or apply entitlement evidence only → add a missing custom ID. Current gaps:
   inference dry-run and a guided custom-ID editor with explicit connection
   scope. Unknown account entitlement must remain non-routable.
2. **Curate one Physical:** filter routes by prefix → select equivalent upstream
   IDs → assign canonical name → inspect evidence/capability/limits → verify
   only exact/allowed sources are eligible. Current gap: polished guided UX and
   broad provider metadata coverage.
3. **Compose one role:** search candidates while preserving selected items →
   preserve existing member order → add/remove candidates → explicitly reorder
   members → set strategy/config → expose the Combo → verify the Combo, and
   only independently exposed models, appear in `/v1/models`. Current gap:
   complete end-to-end TUI acceptance for filter + membership edits + reorder +
   save + `/v1/models` projection.
4. **Exercise one failure:** real 429/quota body + reset → fallback according
   to policy → persist evidence → restart daemon → same route remains blocked
   until reset → route becomes eligible without a probe. Current gap: broad
   provider-specific fixtures and storage failure policy.
5. **Use the service without a UI:** stop/never launch `gobroom`; daemon
   continues serving while CLI or another IPC client reads/changes config.
   Current gap: third-party control client contract/versioning, not daemon
   independence itself.

## Explicitly outside these use cases

MITM/DNS interception, IDE credential spoofing, tunnels, embeddings and
media/search APIs, Fusion panel/judge orchestration, a web dashboard, hosted
multi-user cloud sync, and arbitrary database/log-vendor plugins are not
required for the core provider/model-routing daemon. They are separate services
or future contracts and must not be used to inflate core completion status.

## Evidence map

- Current status: [implementation roadmap](IMPLEMENTATION_PLAN.md)
- Product boundary: [vision](VISION.md)
- TUI context grammar: [TUI UX](TUI_UX.md)
- Provider setup contract: [providers](PROVIDERS.md)
- Model identity and capabilities: [physical models](PHYSICAL_MODELS.md)
- Request/runtime semantics: [kernel contract](KERNEL_CONTRACT.md),
  [normalization](NORMALIZATION.md), [passive health](PASSIVE_HEALTH_ROUTING.md)
- Relative feature parity and non-goals: [compatibility matrix](COMPATIBILITY_MATRIX.md)

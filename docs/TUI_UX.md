# TUI interaction and model-management design

Status: interaction contract and implementation guide. LazyGit-style top-level
blocks/tabs, keyboard navigation, the separate Combo member/candidate editor,
and its expose-to-`/v1/models` flow are implemented/tested. Provider onboarding
and guided Physical curation remain partial and are tracked in the roadmap.

## Mental model

The TUI follows the user's workflow and keeps its management layers distinct:

```text
provider
  -> connection
  -> discovered model route
  -> physical model
  -> combo model
  -> optional discovery through /v1/models
```

- **Discovered** is provider inventory: upstream IDs returned by `/models` or
  entered manually.
- **Physical** is a user-managed identity for one real model implemented by one
  or more discovered routes. `qwen-3.7-max` can contain XKiro, OCG and g4f
  routes without adopting any provider prefix as its identity.
- **Combo** is a real model with ordered model members and an execution policy.
  Role names such as `junior`, `senior` and `tech-lead` are combos.
- **Exposure** is a property on a physical/combo model. It is not a fourth
  management catalog. Exposed models form the `/v1/models` projection.

These layers belong to one Models workspace, but they are not merged into one
list. A shared domain does not imply a shared management operation.

## LazyGit interaction grammar

GoBroom adopts LazyGit's separation between blocks, items, tabs and nested
contexts:

```text
h / l       previous / next block
j / k       previous / next item in the focused block
[ / ]       previous / next tab in the focused block
Enter       go into the selected context/main view
Esc         return to the parent context
Space       select or toggle the focused item
/           filter the focused view
n / N       next / previous filter match
1..6        jump directly to a side block
0           focus the main view
?           context-sensitive actions and keybindings
+ / _       next / previous screen mode
H / L       horizontal scroll
```

`j/k` never changes blocks. Moving the cursor to the last row does not activate
the next frame. `h/l` traverses blocks in layout order even when side blocks are
stacked vertically. Each block preserves its own active tab, cursor, filter,
selection and scroll state when focus moves away.

The key map is a context grammar, not a bag of global shortcuts. `Enter`,
`Space`, `d`, `e` and other actions resolve against the focused context; modal
or input modes guard unrelated bindings.

Reference behavior:

- LazyGit universal navigation: <https://github.com/jesseduffield/lazygit/blob/master/docs/Config.md>
- LazyGit generated keybinding reference: <https://github.com/jesseduffield/lazygit/blob/master/docs/keybindings/Keybindings_en.md>

## Top-level layout

```text
┌─ [1] Status ─────────────────┐ ┌─ Main view ──────────────────────────────┐
│ daemon / endpoint           │ │                                          │
└──────────────────────────────┘ │ content for the focused block, tab and   │
┌─ [2] Providers·Connections ─┐ │ selected item                            │
│ provider and account state │ │                                          │
└──────────────────────────────┘ │                                          │
┌─ [3] Discovered·Physical·Combos ─┐                                      │
│ model-management layer      │ │                                          │
└──────────────────────────────┘ │                                          │
┌─ [4] Usage·Quota ────────────┐ │                                          │
│ operational accounting     │ │                                          │
└──────────────────────────────┘ │                                          │
┌─ [5] Runtime·Logs ───────────┐ │                                          │
│ health / cooldown / events │ │                                          │
└──────────────────────────────┘ ┌─ [6] Transforms·Bindings·Catalog ───┐   │
                                 │ scoped extension policies           │   │
                                 └──────────────────────────────────────┘   │
                                 └──────────────────────────────────────────┘
```

The dot-separated names in a frame are tabs in one panel, analogous to
LazyGit's Files/Worktrees/Submodules or Branches/Remotes/Tags groups. A layer
may later be promoted to its own block without changing its context contract.

The Transforms block separates active bindings from the versioned transform
catalog. From Catalog, `n` opens a binding form generated from the selected
descriptor's options schema; from Bindings, `e` edits and `d` deletes. The
form derives top-level option fields, defaults, primitive types and required
fields from the schema; structured values are entered as JSON. The daemon
validates exact refs/options and the full binding set before publish.

## Provider and connection onboarding

The intended setup is account-specific and inspectable:

```text
Providers: create provider + endpoint
  -> Connections: create account/credential
  -> select that connection, press t
  -> read-only /models test and bounded model preview
  -> press i to review the connection catalog; select IDs and import a subset
  -> add a custom ID if the provider omitted it
```

The selected-account flow keeps test, review and mutation separate. A complete
catalog snapshot can establish both available and not-listed evidence for that
connection; an incomplete snapshot can establish only positive availability.
Unknown entitlement is not the same as absent. Custom upstream IDs are assigned
to explicit connection IDs; they are not provider-wide routes. Press `m` on a
connection to create a custom ID for that account, then tab to Assigned
connections and press `a` to review/select additional same-provider accounts.
The custom route becomes executable only through assigned, enabled connections.
Returned IDs start checked and existing catalog IDs are labeled; deselect
unwanted route imports. Clearing all and
applying records connection availability without importing route IDs. The
existing development catalog is not backfilled to accounts automatically;
review/refresh each connection to establish evidence. The provider-row `t`
shortcut remains a
convenience operation using the highest-priority enabled connection and imports
all returned IDs; it is not a substitute for reviewing a selected account.
CLI equivalents: `gobroom connections test --connection-id ID`,
`gobroom connections preview-models --connection-id ID`, and
`gobroom connections refresh-models --node-id NODE --connection-id ID` with
repeated `--model-id UPSTREAM_ID` flags to import a reviewed subset; omit
`--model-id` to import all IDs returned by that connection, or use
`--entitlements-only` to update evidence without adding IDs to the catalog.
Custom IDs can be bound explicitly with repeated
`gobroom models upsert --connection-id ID` flags; they never inherit access
from every connection on the provider.
Catalog review/import only adds Discovered routes; it does not create Physical
models, Combos or expose models. The UI must identify the selected connection,
show entitlement certainty and preserve model IDs exactly.

For direct OpenAI and Anthropic API-key setup, the provider form starts with
the OpenAI preset; press `Ctrl+P` to cycle OpenAI → Anthropic → Custom. The
presets fill name, prefix, base URL, protocol, models path and API-key auth
mode. Leave the provider definition blank so the daemon derives it from the
protocol:

| Field | OpenAI | Anthropic |
|---|---|---|
| Name / prefix | `OpenAI` / `openai` | `Anthropic` / `anthropic` |
| Base URL | `https://api.openai.com/v1` | `https://api.anthropic.com/v1` |
| Protocol | `openai_chat` | `anthropic` |
| Models path | `/models` | `/models` |
| Auth mode | `api_key` (daemon default) | `api_key` (daemon default) |

Create the connection from the selected provider row; the TUI masks the key.
OpenAI discovery uses Bearer auth. Anthropic discovery uses `x-api-key` and
`anthropic-version`. These settings cover the provider/model route, not full
protocol parity; see the [compatibility matrix](COMPATIBILITY_MATRIX.md).

## Models workspace

### Discovered tab

Shows provider routes, grouped and searchable by provider/prefix. Its main view
supports rediscovery, custom upstream IDs, source inspection and assignment to
a physical model. Discovery never silently creates the final user model graph.

### Physical tab

Shows physical model identities independently from combos. Selecting
`deepseek-v4-flash` opens its ordered provider routes, capabilities, source
policy, reverse references and exposure state. Adding a source opens a filtered
Discovered picker; it does not turn the top-level view into raw provider rows.
When creating a Physical from selected Discovered routes, the editor proposes a
canonical name only if every selected upstream ID yields the same conservative
token (for example `qwen/qwen3.7-max:free`, `qwen3.7-max` and
`Qwen:qwen3.7-max` suggest `qwen-3.7-max`). The suggestion remains editable and
is never saved automatically. If that name already exists, the review editor
preloads its current policy and routes, then appends only newly selected source
routes so equivalence grouping does not overwrite existing configuration.
Different normalized IDs receive no merge suggestion.
The focused route-grouping and Combo exposure key-event scenarios run with
`make test-model-workflow`.
The profile, evidence, source-fidelity and aggregate views follow the
[Physical model contract](PHYSICAL_MODELS.md).

### Combos tab

Shows role/use-case models independently from physical models. Selecting
`junior` opens its ordered physical/combo members, execution strategy, reverse
references and exposure state. A combo is a routable model and may be returned
by `/v1/models` when exposed.

Runtime and source rows display derived cause, scope, deadline, confidence and
the explanation for effective order; they do not reduce passive evidence to a
generic health badge. See
[the passive health contract](PASSIVE_HEALTH_ROUTING.md).

## Filtering

`/` filters only the focused view. It must search the fields meaningful to that
context:

```text
Discovered    provider name, prefix, upstream ID, capabilities
Physical      physical name, source routes/prefixes, capabilities, exposure
Combos        combo name, member names, strategy, exposure
Members       current ordered members
Picker        eligible canonical Physical/Combo members; Physical sources are prefix-search keys, not duplicate rows
```

Typing a known prefix such as `g4f/`, `orca/` or `ocg/` immediately narrows
provider routes in Discovered/source selection. In a Combo candidate picker,
those prefixes match through each Physical model's discovered routes, while the
result remains one prefix-free Physical row. Filtering is a projection: it
does not mutate membership or order. Selected items stay selected when hidden
by a filter, and the UI states the total and matching counts. A matched source
may appear as secondary context, never as a separate Combo candidate.

The Combo editor splits the main view into ordered members and candidates:

```text
┌─ Combo members ──────────────┬─ Physical/combo picker ─────────┐
│ 1 glm-5.3-flash             │ / qwen                          │
│ 2 deepseek-v4-flash         │ [ ] qwen-3.8-max                │
│ 3 hy3                        │ [x] qwen-3.7-max                │
└──────────────────────────────┴──────────────────────────────────┘
```

Within this subcontext, the Combo editor renders the two independently
focusable lists shown above: `h/l` changes member/candidate pane and `j/k` moves
only within the focused list. `Space` removes the focused member or toggles a
candidate into/out of membership. `K/J` reorders only the focused member in the
ordered-members pane. `Enter` opens its details without losing editor state;
`Esc` closes details first, then cancels the unsaved editor. `ctrl+s` saves.
Filtering is scoped to the focused list and never mutates membership/order.
Existing order survives opening the editor, filtering, adding/removing
candidates and saving. New candidates append in selection order; the saved
Combo order is exactly the visible member sequence, not candidate sort order.

Strategy is an execution primitive on the Combo. Primitive-level options are a
JSON object and must round-trip unchanged when editing another field. Member
weight is a typed property of each ordered member edge, not a synthetic key in
the strategy-options object; weighted fallback reads that property, and other
strategies ignore it. The member pane makes weight visible and `+/-` changes
the focused member's weight without changing identity/order. The Combo form
binds strategy options as a JSON object. Focusing the strategy field and
pressing `Enter` opens the searchable registered-strategy catalog. `j/k`
navigates, `/` filters, `Enter` selects, and the inspector shows semantics plus
option schema/defaults. Selecting a primitive replaces incompatible options
with that primitive's defaults; validation errors keep the form open and point
to the offending option.

## Acceptance rules

- Discovered, Physical and Combos are separately manageable tabs in the Models
  workspace.
- No provider route is presented as a physical model identity merely because
  it was discovered.
- No separate Published Models panel is required; exposure is edited on the
  model.
- A combo appears as an OpenAI-compatible model when exposed.
- Testing a selected connection is read-only; importing is a separate action
  and uses that same connection.
- Combo candidates are canonical Physical/Combo identities. Prefix search may
  match a Physical model's source routes, but it never creates duplicate
  provider-route rows in the Combo member list.
- Combo membership edits use separate ordered-member and candidate panes;
  filtering cannot reorder or remove members, and only `ctrl+s` persists the
  edited order.
- Cursor, filter and selection state are local to a context.
- Help and footer actions are generated from the focused context.
- Narrow layouts preserve the same context graph even when blocks are stacked
  or the main view becomes fullscreen.

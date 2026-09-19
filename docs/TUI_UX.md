# TUI interaction and model-management design

Status: target UX contract. The current Bubble Tea implementation is an early
slice and does not yet implement this complete context hierarchy.

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
1..5        jump directly to a side block
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
└──────────────────────────────┘ └──────────────────────────────────────────┘
```

The dot-separated names in a frame are tabs in one panel, analogous to
LazyGit's Files/Worktrees/Submodules or Branches/Remotes/Tags groups. A layer
may later be promoted to its own block without changing its context contract.

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

### Combos tab

Shows role/use-case models independently from physical models. Selecting
`junior` opens its ordered physical/combo members, execution strategy, reverse
references and exposure state. A combo is a routable model and may be returned
by `/v1/models` when exposed.

## Filtering

`/` filters only the focused view. It must search the fields meaningful to that
context:

```text
Discovered    provider name, prefix, upstream ID, capabilities
Physical      physical name, source routes, capabilities, exposure
Combos        combo name, member names, strategy, exposure
Members       current ordered members
Picker        eligible candidate members
```

Typing a known prefix such as `g4f/`, `orca/` or `ocg/` immediately narrows
provider routes. Filtering is a projection: it does not mutate membership or
order. Selected items stay selected when hidden by a filter, and the UI states
the total and matching counts.

An editor may split the main view into ordered members and candidates:

```text
┌─ Combo members ──────────────┬─ Physical/combo picker ─────────┐
│ 1 glm-5.3-flash             │ / qwen                          │
│ 2 deepseek-v4-flash         │ [ ] qwen-3.8-max                │
│ 3 hy3                        │ [x] qwen-3.7-max                │
└──────────────────────────────┴──────────────────────────────────┘
```

Within this subcontext, `h/l` changes member/picker focus, `j/k` moves within
the focused list, `Space` toggles membership, `Enter` drills into a model and
`Esc` returns while preserving editor state. Reordering is explicit and never a
side effect of filtering.

## Acceptance rules

- Discovered, Physical and Combos are separately manageable tabs in the Models
  workspace.
- No provider route is presented as a physical model identity merely because
  it was discovered.
- No separate Published Models panel is required; exposure is edited on the
  model.
- A combo appears as an OpenAI-compatible model when exposed.
- Cursor, filter and selection state are local to a context.
- Help and footer actions are generated from the focused context.
- Narrow layouts preserve the same context graph even when blocks are stacked
  or the main view becomes fullscreen.

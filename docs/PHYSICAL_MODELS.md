# Physical model contract

Status: target domain, management and consumption contract. Typed Physical and
Combo persistence exists. The kernel now has typed support states,
request-requirement compilation, TokenLimits, Physical identity and source
fidelity/evidence persistence through catalog, discovered route, Physical and
snapshot boundaries. Route-effective projections and cross-protocol reasoning
translation remain incomplete. Declared/guaranteed/available projections are
available in the Physical control/TUI view; richer route-effective evidence
and policy explanation remain. Track
implementation status in [the roadmap](IMPLEMENTATION_PLAN.md).

## Purpose

A Physical Model is the stable semantic identity between provider inventory
and policy-bearing Combo models:

```text
provider + connection
  -> discovered route
  -> physical model
  -> combo / role model
  -> exposed client model
```

For example, these may be source bindings of one Physical Model:

```text
xkiro/qwen/qwen3.7-max:free
ocg/qwen3.7-max
g4f/Qwen:qwen3.7-max
                 │
                 └──> Physical: qwen-3.7-max
```

The Physical Model answers “which model is this and what is it intrinsically
capable of?” A source route answers “how does this provider expose it now?” A
runtime profile answers “how is this route/connection behaving now?” Those
truths must not be collapsed into one metadata map.

## Layer ownership

| Fact | Physical identity/profile | Route/provider profile | Runtime profile |
|---|---:|---:|---:|
| Canonical family and revision | yes | | |
| Knowledge cutoff | yes | may override | |
| Intrinsic modalities/reasoning behavior | yes | may restrict | |
| Effective input/output limits | reference only | yes | |
| Accepted parameter and reasoning dialect | | yes | |
| Price, retention, region and moderation | | yes | |
| RPM/TPM/account quota | | advertised | current state |
| TTFT, output tokens/s and reliability | | | yes |
| Health and cooldown | | | yes |

Physical metadata must never claim a route-specific or transient fact as an
immutable model property.

## Identity and source fidelity

Name similarity is not proof that two routes serve the same weights or model
revision. Each source edge therefore records fidelity:

```text
exact       same model and revision
alias       different upstream name with evidence of identity
compatible  same family, but revision/preset/quantization may differ
dynamic     upstream route chooses a model dynamically
unknown     inferred only from incomplete evidence
```

```text
PhysicalSource {
  routeId
  fidelity
  enabled
  profileOverrides
  evidence
}
```

Default Physical fallback uses `exact` and `alias`. `compatible` is opt-in.
Dynamic routes such as `auto`, `free` or an upstream router are not exact
Physical implementations; they belong in a Combo or an explicitly dynamic
model node.

## Typed support state

Capabilities are not booleans. Every feature uses a support state:

```text
unknown
unsupported
native
emulated
conditional
```

`unknown` is not false, `emulated` is not native, and `conditional` requires
machine-readable constraints. Explicit negative evidence must be able to
override a prior positive heuristic.

```text
ModelProfile
├── input modalities
│   ├── text
│   ├── image
│   ├── audio
│   ├── video
│   └── document / PDF
├── output modalities
│   ├── text
│   ├── image
│   ├── audio
│   └── embeddings
├── features
│   ├── function calling
│   ├── parallel tool calls
│   ├── structured output
│   ├── strict JSON schema
│   └── log probabilities
├── token limits
├── reasoning behavior
└── lifecycle
```

Modality details include accepted formats and route constraints where known:

```text
image input:
  support: native
  formats: [png, jpeg, webp]
  maxItems: 20

PDF input:
  support: emulated
  mode: extracted_text
```

Built-in web search, file search, code execution and computer use are normally
provider/endpoint services, not intrinsic Physical capabilities.

## Token and context limits

Do not use one ambiguous `contextWindow` field:

```text
TokenLimits {
  maxInputTokens?
  maxOutputTokens?
  maxTotalTokens?
  tokenizer?
  countingMode?
  reasoningAccounting?
}
```

Unknown limits remain unknown; they are not replaced with a guessed global
default. A route-specific token budget evaluator checks the provider's actual
constraint model:

```text
estimated input
  + reserved output
  + protocol overhead
  + reasoning reserve when applicable
  <= effective route limits
```

Some providers publish independent input/output limits, while others enforce a
shared total window or context tiers. The route/adapter owns that evaluation.
Gemini, for example, exposes separate input and output limits and documents
that thought tokens consume output capacity:
<https://ai.google.dev/gemini-api/docs/tokens> and
<https://ai.google.dev/gemini-api/docs/thinking>.

Physical projections show both safety and opportunity:

```text
Context
  guaranteed   200K   all active sources
  available      1M   at least one source
  unknown       1/6   insufficient evidence
```

The kernel gates on the effective source limit, never on the largest Physical
aggregate.

## Declared, guaranteed and available projections

A Physical Model exposes three views:

```text
declared    intrinsic profile supported by evidence/user override
guaranteed  intersection across eligible active sources
available   union across eligible active sources
```

Example:

```text
qwen-3.7-max

Vision
  declared     native
  guaranteed   no
  available    3/6 sources

Reasoning levels
  guaranteed   [high]
  available    [none, low, medium, high, xhigh]
```

Request-time filtering produces a fourth, transient projection: the routes
that satisfy the current request.

## Reasoning contract

Reasoning has three independent layers.

### Physical behavior

```text
unsupported
optional
required
adaptive
```

### Route control surface

```text
ReasoningControlProfile {
  dialect:
    openai_responses
    openai_chat
    anthropic_adaptive
    anthropic_budget
    gemini_level
    gemini_budget
    fixed_variant

  supportedLevels[]
  defaultLevel?
  canDisable
  minBudget?
  maxBudget?
  supportsSummary
  supportsEncryptedContinuity
}
```

### Normalized client intent

```text
ReasoningIntent {
  mode: inherit | auto | disabled | level | budget
  level?: string
  budgetTokens?: int
  summary: inherit | none | auto | concise | detailed
  strictness: exact | allow_clamp | best_effort
  source: request_field | model_preset | combo_default | physical_default
}
```

The distinction among `inherit`, `auto` and `disabled` is mandatory:

- `inherit`: the client sent no opinion; do not insert or remove fields.
- `auto`: the client explicitly chose provider/model default behavior.
- `disabled`: the client explicitly requested no reasoning.

Effort levels are open strings with known common values, not a closed enum;
model catalogs can add future levels. Level labels are ordinal intent, not
guaranteed cross-vendor equivalence.

### Harness consumption

| Harness | User-facing selection | Harness abstraction | Typical wire form |
|---|---|---|---|
| Codex | model picker, config/profile, Plan override | model plus independent thread/turn effort | Responses `reasoning.effort` |
| OpenCode | `provider/model#variant`, variant cycle, agent override | named request patch | provider-specific expansion |
| Antigravity | compound model/effort selector, `/effort`, `--effort` | displayed model variant | Gemini `thinkingLevel` or backend-specific mapping |
| Claude Code | `/effort`, `effortLevel`, environment override | session adaptive-effort setting | `thinking` plus `output_config.effort` |

References:

- Codex configuration: <https://developers.openai.com/docs/config-file/config-sample>
- OpenAI reasoning: <https://developers.openai.com/api/docs/guides/reasoning>
- OpenCode variants: <https://opencode.ai/v2/docs/models>
- Antigravity model selection: <https://antigravity.google/docs/models>
- Antigravity `/effort`: <https://www.antigravity.google/changelog>
- Claude Code `/effort`: <https://code.claude.com/docs/commands>
- Claude Code environment precedence: <https://code.claude.com/docs/env-vars>
- Anthropic adaptive thinking: <https://docs.anthropic.com/en/docs/build-with-claude/prompt-engineering/prompt-templates-and-variables>

Gobroom accepts native wire shapes rather than guessing from user-agent:

```text
OpenAI Responses      reasoning.effort
OpenAI Chat           reasoning_effort
Anthropic             thinking + output_config.effort
Gemini                generationConfig.thinkingConfig
Antigravity envelope  request.generationConfig.thinkingConfig
```

Provider-manifest-declared variant syntax may additionally map model selectors
such as `#high` or a fixed upstream suffix. It must not be guessed globally.

### Presets are not Physical Models

Effort variants do not create duplicate Physical identities:

```text
gpt-5.6-sol-low     invalid Physical duplication
gpt-5.6-sol-high    invalid Physical duplication
```

Use a request preset:

```text
ModelPreset {
  name: deep
  target: PhysicalRef("gpt-5.6-sol")
  requestDefaults.reasoning:
    mode: level
    level: high
}
```

Precedence is explicit:

```text
explicit client intent
  > selected preset
  > combo default
  > physical default
  > provider/model default
```

An administrative force is a separate policy mode; it must not masquerade as
a default.

### Strictness and translation

`exact` excludes any route that cannot represent the requested intent. If no
route remains, return a descriptive client error rather than silently changing
effort. `allow_clamp` may select the nearest supported level, but usage records
the requested value, effective value and mapping loss. `best_effort` may fall
back to provider defaults.

Cross-protocol adapters own dialect translation. Metadata fields do not control
reasoning and cannot substitute for real Anthropic/Gemini thinking fields.

## Performance and capacity

Physical Models do not have fixed throughput. Runtime observations are scoped
to route and connection, and bucketed where material by context size, effort,
modality and streaming mode:

```text
RuntimeProfile {
  routeId
  connectionId
  sampleCount
  observedAt
  window
  ttftP50/P95
  outputTokensPerSecondP50/P95
  latencyP50/P95
  successRate
  rateLimitRate
  concurrency
  quota
}
```

Selection uses runtime performance only after minimum sample and freshness
requirements are met. Low-confidence observations fall back to static order.
OpenRouter similarly separates model properties, pricing, supported parameters
and recent throughput/latency sorting:
<https://openrouter.ai/docs/api/api-reference/models/get-models>.

## Quality and evaluation

Coding, mathematics, agentic ability, long-context recall, tool reliability,
JSON adherence and language quality are not hard boolean capabilities. They
belong in versioned evaluation records or user preferences:

```text
EvaluationProfile {
  modelRevision
  task
  source
  score
  sampleCount
  observedAt
}
```

Hard eligibility uses capability/limit contracts. Soft ranking may consume
evaluation scores when their provenance is known.

## Economics, governance and lifecycle

These are route/provider facts unless evidence says otherwise:

```text
Economics
  input/output/reasoning/cache/media prices
  per-request fee and free tier

Governance
  retention, ZDR, region, moderation and training policy

Lifecycle
  stable/preview/deprecated
  release/deprecation/EOL dates
  pinned revision versus floating alias
```

Floating aliases are not equivalent to pinned model revisions.

## Evidence and overrides

Profile values and evidence are stored separately so typed contracts remain
usable:

```text
ProfileEvidence {
  "/limits/maxInputTokens": [Evidence...]
  "/modalities/input/image": [Evidence...]
}
```

Evidence records source, confidence, observation time and optional expiry.
Field-level resolution generally considers:

```text
user override
authoritative live provider metadata
provider manifest / official documentation
trusted external catalog
cross-provider consensus
name heuristic
unknown
```

There is no universal precedence for every field: provider metadata is usually
best for effective gateway limits, official model evidence for intrinsic
modalities, and observations for performance.

## Durable model shape

```text
PhysicalModel {
  id
  displayName
  identity {
    family
    revision
    aliases[]
    knowledgeCutoff?
  }
  declaredProfile ModelProfile
  userOverrides ProfilePatch
  sources[] PhysicalSource
  sourcePolicy StrategySpec
  discoverable
  enabled
}
```

```text
DiscoveredRoute {
  providerNodeId
  upstreamModelId
  rawMetadata
  advertisedProfile
}
```

RouteProfile contains effective limits, accepted controls, reasoning dialect,
economics and governance. RuntimeProfile is separate ephemeral/operational
state. The immutable kernel snapshot contains resolved effective profiles; the
request path does not query storage or external catalogs.

Runtime health, limit and adaptive-order semantics are defined in
[Passive health and adaptive routing](PASSIVE_HEALTH_ROUTING.md).

## Routing pipeline

```text
normalized request
  -> compile modality/token/tool/reasoning/governance requirements
  -> expand Physical source bindings
  -> hard-filter protocol, capability, limits, reasoning and policy
  -> hard-filter health, cooldown and quota
  -> soft-rank order, affinity, latency, throughput, cost and reliability
  -> apply source strategy
  -> translate intent through the selected route dialect
  -> execute
  -> record requested/effective controls and runtime observations
```

Hard filtering always runs before a source strategy. A strategy never chooses
an ineligible route and asks the adapter to repair the mismatch afterward.

## Management UX

The Discovered tab manages provider inventory, assignment suggestions and
source fidelity. The Physical tab manages stable identity, profile evidence,
ordered sources and source policy. The Combo tab sees Physical/Combo members,
not provider routes unless the user drills down.

Physical list row:

```text
qwen-3.7-max
6 sources · ctx 200K–1M · vision 3/6 · reasoning
```

Physical Main view:

```text
Identity and evidence
Declared / guaranteed / available capabilities
Token limits
Reasoning behavior and available controls
Ordered source comparison
Observed performance and quota
Reverse Combo references
```

Useful filters include:

```text
provider:g4f
vision
ctx:>=200k
reasoning:high
fidelity:exact
unknown
slow
```

## Consumption contract

`/v1/models` returns exposed IDs without multiplying entries by effort level.
It may attach an ignorable `gobroom` extension containing safe aggregate
metadata. Full profiles and evidence are available through typed control IPC,
not reconstructed from the OpenAI-compatible catalog.

Example extension:

```json
{
  "id": "junior",
  "object": "model",
  "owned_by": "gobroom",
  "gobroom": {
    "reasoning": {
      "available": ["low", "medium", "high"],
      "default": "inherit"
    },
    "context": {
      "guaranteed": 200000,
      "available": 1000000
    }
  }
}
```

## Architectural invariants

1. Physical identity is independent of provider and connection.
2. Discovered, Physical and Combo remain separate management layers.
3. Effort variants and request presets do not duplicate Physical identities.
4. Effective route profiles determine eligibility.
5. Unknown facts remain unknown; there is no fake global capability floor.
6. Hard capability filtering happens before strategy selection.
7. Explicit client intent wins over defaults unless an explicit force policy
   is configured.
8. Lossy reasoning mapping is never silent.
9. Runtime performance is not persisted as an intrinsic model fact.
10. `/v1/models` is an exposure projection, not the internal metadata source.

## Delivery sequence

1. Replace boolean capability maps with versioned typed profiles and evidence.
2. Add identity/revision and source fidelity.
3. Build route-effective profiles and aggregate projections.
4. Compile normalized request requirements and hard-filter routes.
5. Complete OpenAI, Anthropic, Gemini and Antigravity reasoning
   parsing/translation.
6. Record requested/effective reasoning and mapping loss in usage events.
7. Add runtime performance observations with confidence/freshness gates.
8. Add adaptive source policies only after eligibility is correct.
9. Complete Physical profile, comparison, filter and reverse-reference UX.

## Current implementation gaps

- Physical capability storage is still `map[string]bool`.
- Identity revision, source fidelity, evidence and route profile patches are missing.
- Token limits are not compiled into request eligibility.
- Reasoning normalization does not yet cover Anthropic `output_config` or
  Gemini/Antigravity shapes.
- Cross-protocol Anthropic reasoning currently lacks a real adaptive/budget translator.
- Runtime usage records do not distinguish requested and effective effort.
- Performance observation and confidence-aware ranking are not implemented.
- `/v1/models` does not yet publish aggregate profile extensions.

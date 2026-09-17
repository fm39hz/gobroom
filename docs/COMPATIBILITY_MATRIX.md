# 9router compatibility matrix

This document records behavior verified from the checked-out 9router source at
`/home/fm39hz/Workspace/Personal/Tools/AI/9router`.

Labels:

- `preserve`: required for the GoBroom core replacement;
- `simplify`: keep the useful behavior with a smaller contract;
- `out-of-kernel`: not part of `gobroomd` request routing;
- `pending`: important behavior not implemented yet.

## Request entry and model resolution

| 9router behavior | Source evidence | GoBroom decision | Status |
|---|---|---|---|
| OpenAI Chat, Responses, Anthropic and Gemini-family detection | `open-sse/services/provider.js`, `open-sse/translator/formats.js`, `open-sse/handlers/chatCore.js` | Keep endpoint/header/body detection as a typed normalizer | pending |
| `provider/model` parsing using the first slash | `open-sse/services/model.js:parseModel` | Preserve opaque model IDs after the first slash | pending |
| Provider aliases to canonical IDs | `open-sse/services/model.js`, `open-sse/providers/registry` | Preserve through one prefix registry | partial |
| Bare model inference | `open-sse/services/model.js:inferProviderFromModelName` | Keep as explicit compatibility mode | pending |
| User model alias to `provider/model` | `open-sse/services/model.js:resolveModelAliasFromMap` | Preserve as a logical model reference | partial |
| Custom model entries | `src/lib/db/migrate.js`, `open-sse/config/providerModels.js` | Preserve as first-class catalog entries | partial |
| Upstream model ID rewrite | `open-sse/config/providerModels.js`, `chatCore.js` | Preserve as route/model override | pending |

## Combos and fallback

| 9router behavior | Source evidence | GoBroom decision | Status |
|---|---|---|---|
| Combo is an ordered JSON list of model strings | `src/lib/db/repos/combosRepo.js` | Accept old flat semantics through manual migration | partial |
| Fallback through combo members | `open-sse/services/combo.js:handleComboChat` | Preserve in kernel scheduler | partial |
| Round-robin combo strategy | `open-sse/services/combo.js:getRotatedModels` | Preserve with in-memory scheduler state | partial |
| Sticky round-robin limit | `open-sse/services/combo.js:normalizeStickyLimit` | Preserve as combo policy | partial |
| Fusion strategy | `open-sse/handlers/chatCore.js`, `open-sse/services/combo.js` | Keep outside first core replacement | out-of-kernel |
| Capability-based combo reordering | `open-sse/services/combo.js:reorderByCapabilities` | Preserve after capability policy | pending |
| Recursive combo handling | `src/sse/handlers/chat.js` | Represent explicitly as validated graph | simplify |
| Status/error-text fallback rules | `open-sse/services/accountFallback.js`, `combo.js` | Preserve as typed error policy | partial |
| Retry-After aggregation | `open-sse/services/combo.js` | Preserve in response policy | pending |

## Account and credential behavior

| 9router behavior | Source evidence | GoBroom decision | Status |
|---|---|---|---|
| Active connections per provider | `src/sse/services/auth.js:getProviderCredentials` | Preserve through connection candidates | partial |
| Fill-first account strategy | `src/sse/services/auth.js` | Preserve | partial |
| Round-robin account strategy | `src/sse/services/auth.js` | Preserve without global mutex | partial |
| Sticky account usage count | `src/sse/services/auth.js` | Preserve as scoped scheduler state | pending |
| Preferred/pinned connection | `src/sse/services/auth.js` | Preserve | pending |
| Per-model account lock | `open-sse/services/accountFallback.js` | Preserve as runtime policy state | pending |
| Account exclusion after failure | `src/sse/handlers/chat.js` | Preserve | partial |
| Proactive token refresh | `src/sse/services/tokenRefresh.js`, `open-sse/services/oauthCredentialManager.js` | Preserve through credential manager | pending |
| Refresh-on-401/403 and retry | `open-sse/handlers/chatCore.js` | Preserve once per request | pending |
| Provider-specific OAuth | `src/sse/services/tokenRefresh.js`, `src/lib/oauth` | Add by usage priority, not in kernel | pending |
| Per-connection proxy settings | `open-sse/handlers/chatCore.js`, `src/lib/network/connectionProxy.js` | Keep in transport/credential layer | pending |

## Normalization and translation

| 9router behavior | Source evidence | GoBroom decision | Status |
|---|---|---|---|
| Source/target format selection | `open-sse/handlers/chatCore.js`, `open-sse/services/provider.js` | Preserve as normalizer + adapter selection | partial |
| Native passthrough | `chatCore.js`, `open-sse/utils/clientDetector.js` | Preserve as explicit fast path | pending |
| Tool-call ID repair | `open-sse/translator/concerns/toolCall.js` | Preserve deterministically | partial |
| Missing tool-result reconciliation | translator concerns/tests | Preserve in normalization invariants | pending |
| Thinking/reasoning mapping | `open-sse/translator/concerns/thinkingUnified.js` | Semantic IR, wire mapping in adapter | partial |
| Responses continuity fields | `chatCore.js:stripContinuityFields` | Preserve without leaking internals | partial |
| Content blocks and multimodal input | `open-sse/translator`, `combo.js` | Preserve after capability policy | partial |
| Remote image prefetch | `open-sse/translator/concerns/prefetch.js` | Optional cancellable middleware | pending |
| Tool deduplication | `chatCore.js`, `open-sse/utils/toolDeduper.js` | Optional middleware | simplify |
| RTK/Headroom/Caveman/Ponytail/PXPIPE | `chatCore.js`, `open-sse/rtk` | Post-normalization middleware | out-of-kernel |

## Streaming and response lifecycle

| 9router behavior | Source evidence | GoBroom decision | Status |
|---|---|---|---|
| Passthrough streaming | `open-sse/utils/stream.js` | Preserve | partial |
| Provider SSE to client format | `open-sse/handlers/chatCore/streamingHandler.js` | Canonical response events | pending |
| Tool-call streaming across chunks | stream handler and translator tests | Preserve | pending |
| Usage extraction/estimation | `open-sse/utils/usageTracking.js` | Compact usage events | partial |
| Client disconnect propagation | `open-sse/utils/streamHandler.js` | Preserve | partial |
| No fallback after response bytes | chat/combo stream flow | Kernel invariant | partial |
| Non-streaming conversion | `open-sse/handlers/chatCore/nonStreamingHandler.js` | Preserve for core formats | partial |

## Quota, usage and health

| 9router behavior | Source evidence | GoBroom decision | Status |
|---|---|---|---|
| Persistent usage history | `src/lib/db`, `open-sse/utils/usageTracking.js` | Preserve compactly | partial |
| Request detail logging | `open-sse/handlers/chatCore/requestDetail.js`, `usageDb` | Bounded opt-in diagnostics | simplify |
| Daily usage aggregates | `src/lib/db/migrations`, usage repositories | Preserve | pending |
| Cost/pricing data | `open-sse/providers/pricing.js` | Preserve after usage contract | pending |
| Account/model cooldown locks | `open-sse/services/accountFallback.js` | Runtime policy | partial |
| Provider-specific quota endpoints | `open-sse/services/usage`, `src/shared/services/quotaAutoPing.js` | Quota adapters | pending |
| Antigravity quota cache/strikes | `src/sse/services/antigravityQuota.js` | Provider-specific service | pending |
| Quota affects routing | `src/sse/services/auth.js`, combo fallback | Preserve | partial |

## Secondary APIs and integrations

| 9router behavior | Source evidence | GoBroom decision | Status |
|---|---|---|---|
| Embeddings | `open-sse/handlers/embeddings.js` | Later service | out-of-kernel |
| Image/video generation | `open-sse/handlers/imageGeneration.js`, `src/sse/handlers/videoGeneration.js` | Later service | out-of-kernel |
| TTS/STT | `src/sse/handlers/tts.js`, `stt.js` | Later service | out-of-kernel |
| Search/fetch | `src/sse/handlers/search.js`, `fetch.js` | Later service | out-of-kernel |
| Dashboard | `src/app` | TUI/control clients | out-of-kernel |
| CLI/tray | `cli/src/cli` | TUI/CLI frontend | simplify |
| MITM/DNS | `src/mitm` | Sidecar/plugin | out-of-kernel |
| Tunnels | `src/lib/tunnel` | Optional sidecar | out-of-kernel |

## Deliberately not copied into the kernel

```text
global account-selection mutex
per-request SQLite reads for static model configuration
synchronous raw request-detail persistence
dashboard-driven routing state
provider-specific branches in the core kernel
MITM/DNS interception
tunnel lifecycle
token saver prompt mutations
fusion/panel orchestration
media endpoints
```

## Evidence gaps

The following require runtime fixtures before being called compatible:

- exact round-robin behavior under concurrency;
- precedence between provider, combo, model, and account cooldowns;
- all provider-specific error rules;
- every `supportedFormats`/transport combination;
- whether every dashboard quota control affects request routing;
- stream behavior for every specialized executor.

These are `pending`, not assumed to be implemented by either project.

# Source-backed encoded/signing provider acceptance

This follows the [design](superpowers/specs/2026-10-24-qoder-vertical-design.md)
and [plan](superpowers/plans/2026-10-24-qoder-vertical.md). The external-module
fixture exercises a source-derived Qoder text-chat payload through public
connection/custom-model/Physical/Combo controls, real HTTP and persisted restart.
It is not a shipped Qoder provider or live-provider compatibility claim.

Verified 2026-10-24 at `758b405`: full root/external-module tests and both vet
runs passed; focused race/count3 passed in 27.934s. After `make install-all`,
the dedicated installed gate passed race/count3 in 36.275s. The actual daemon
reported healthy control=serving=519, zero persistence failures, and valid
bundle version 9. Scoped configuration/credential fingerprint was unchanged;
no configured live upstream was called.

The adjacent executor and `open-sse/shared/qoder/{constants,encoding,cosy,sse}.js`
were inspected directly. The older three-field signing test remains a mechanics
test; it is not relabelled as a native request. The new codec binds selected
native model configuration through its options schema and uses typed payloads.
The outer options object rejects unknown keys; native model objects retain
vendor fields with required key and output bounds. Credentials use the existing
schema-driven import/auth primitive. No provider-name kernel branch is added.

| Boundary | Evidence |
|---|---|
| Native request shape | Fake upstream independently checks hoisted system, messages/tools, task/session/business fields, forced upstream streaming, output limit and mirrored native model config |
| Final-byte signing | Existing COSY signer is reused; fake upstream independently checks encoded-byte MD5, length and signature input/path. Literal Base64 vectors exercise the stdlib custom alphabet and permutation |
| Honest admission | Preflight and preparation share the same supported text subset; unknown generation options, named messages and unsupported multipart text reject before dispatch |
| Signing URL scope | Origin-only base and fixed module endpoint reject path/query overrides; dispatch and signature path agree |
| Semantic response reuse | Bounded envelope reader delegates OpenAI text/tool/usage events; typed normalization preserves nested/top-level finish reasons and late usage |
| Actual client serving | JSON and SSE requests traverse real daemon HTTP, with usage and terminal framing checked; fresh daemon instances serve from persisted setup |
| Failure/resource boundary | Error/malformed/truncated envelopes fail; accepted-response errors dispatch once. DONE/cancellation closes upstream, including blocked reads |
| Installed controls | Installed CLI reads catalog/export/status, deletes the published role, verifies dispatch is unavailable, reapplies the exported graph, then checks serving and healthy snapshots before/after restart |

The installed gate deliberately uses an independently linked **in-process test
daemon**, fake credentials/upstreams and temporary SQLite/Unix sockets. It does
not claim installation of a custom-module daemon binary. Run after install:

```sh
make test-installed-qoder-vertical GOBROOM_TEST_CLI=/absolute/path/to/gobroom
```

The gate runs the bounded native/envelope/encoding/installed cases three times
under the race detector. Source files are in `examples/external-provider-module/`
(`qoder_native_module_test.go`, `qoder_vertical_test.go`, `qoder_envelope_test.go`
and the shared `qoder_auth_test.go` signing helpers).

## Limits

Native model configuration is explicitly supplied for a selected custom model.
Dynamic catalog acquisition/persistence, interactive login, media, complete tool
history/reasoning and real remote acceptance remain separate implementation and
acceptance work. RSA/ciphertext shape plus independent signature checks are not
a proof that a real server accepts the credential envelope. Accepted-failure
tests use one candidate and no idempotency key; other replay guarantees have
their own kernel tests. Restart is a new daemon object, not a subprocess proof.
The shared restart helper still lacks a dedicated local restart deadline.
This closes only the named source-backed vertical, not all C6/M15, product UX,
isolated RSS or sustained tail-latency obligations.

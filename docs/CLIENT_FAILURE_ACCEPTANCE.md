# Client failure boundary acceptance

The bounded F1 change follows the [design](superpowers/specs/2026-10-24-client-failure-design.md)
and [implementation plan](superpowers/plans/2026-10-24-client-failure.md).
Core contract checkpoint: `8269f21`. This is not a whole-provider parity claim.

Verified 2026-10-24: final root/separate-module regression and vet passed;
focused root race3 passed (API 10.696s, ingress 1.088s, kernel 1.359s).
Full conformance passed before the final staged-header fix; full regression and
focused commitment/privacy race tests passed afterward. `make install-all`
succeeded on retry after its first IPC readiness timeout, with the service
healthy on inspection. The explicit installed gate passed race3 in 22.082s.
The real daemon reports healthy matching snapshot 515 and zero persistence
fault counters; export→validate returns valid/version 9. The scoped
config/credential fingerprint is unchanged. No live upstream was called.

| Requirement | Executed test or acceptance gate |
|---|---|
| Ordinary error text stays private; explicit public metadata preserves cause identity | Kernel `TestOrdinaryClientFailureUsesSafeMessageWithoutChangingIdentity` and client-failure privacy/diagnostics tests |
| Owned, bounded public context and immutable response | Ingress validity, clone, exact encoded-budget, response/media and reusable-encoder tests |
| Auth/decode/schema failures select the registered client dialect | API `TestRegisteredClientOwnsPrecommitFailureWireAndHostAuthority`; external `TestIndependentClientFailureBeforeDispatch` |
| Status, dispatch effect and retry remain host facts | External `TestIndependentClientFailureEncoderPreservesHostFactsAndDiagnostics`; API wrapper-precedence test |
| Accepted upstream work is not replayed after pre-first-byte failure | External `TestIndependentClientFailureAcceptedBeforeFirstByteDoesNotReplay`, for HTTP 200 and 202 |
| Quota response creates real cooldown without fabricating no-dispatch for the first attempt | External `TestIndependentClientFailureNoEligibleRouteAfterRealCooldown`: first request 502/confirmed rejection, second 503/unknown with no additional dispatch |
| Failure wire shape is independently verified | External acceptance schema, registered module schema, and test-owned decoded wire structure; no shared encoder DTO assertions |
| Private staged headers do not survive | API `TestPrecommitFailureRestoresHostHeaders`, for valid and failed encoders, including cookies, encoding, length and correlation override |
| Invalid encoders/context fail closed before output | API bad-encoder and metadata/overflow tests: empty 500, no recursive render |
| No precommit encoder after commitment | API `TestCommittedExecutionNeverInvokesPrecommitFailureRenderer`: body, header-only and flush commitment |
| Public module contract and installed controls | `make test-installed-client-failure GOBROOM_TEST_CLI=/absolute/path/to/gobroom` |

The independent module adds its ingress/error encoder and wrapper decoder beside
the existing job module. Job operation, codecs, typed result artifacts and normal
rendering are reused. Its temporary daemon is configured through public IPC with
a connection, custom route, Physical model and published Combo. Private markers
never enter the failure renderer's input. Validation now composes the same
statically linked modules as serving; otherwise the fixture's own exported config
failed validation despite being active.

The explicit installed gate validates an executable absolute binary path, checks
host-14 ingress/decoder/module refs and `client.failure` lifecycle, schema refs,
status, reload and the unchanged export→validate round trip. It runs these and the
independent wire fixtures three times with the race detector. Ordinary tests may
skip the installed-only fixture when the binary environment is absent; they do
not by themselves prove the installed gate.

No new dependency, provider-name branch, database migration, fallback interface
or live-provider call is introduced. Standard-library JSON/MIME validation and
the existing schema catalog are reused. Static Go modules remain trusted code,
not a security sandbox. This boundary handles registered operation failures
before commitment; it is not a claim that all management API errors have been
redesigned, that every native provider dialect is implemented, or that upstream
SDK retry behavior is controlled by the router.

Remaining nonblocking hardening: check operation-ref length before the shared
reference validator's temporary concatenation, and extend inclusive media/body
edge-pair and full Anthropic status-table tests. Exact context budget and model
omission are directly covered. SG1, linked-module restart/import, callback-race
and difficult-provider vertical proofs remain separate architecture obligations.

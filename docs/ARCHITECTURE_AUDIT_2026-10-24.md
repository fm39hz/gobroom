# Finite architecture audit — 2026-10-24

This is source/test inspection, not a new execution of every cited test or a
parity claim. The governing clauses remain in [the roadmap](IMPLEMENTATION_PLAN.md#m15-semantic-closure-delivery)
and [extension contracts](EXTENSION_CONTRACTS.md). Usage/typed-jobs acceptance
is recorded separately; session observability is at `eeec107` / `7eda7ed`,
installed with focused acceptance and final whole-feature review approved (one
nonblocking queued-drain coverage minor). This does not change the audit's
other source-only classifications or execute the missing public-boundary proofs.
The F1 section below records its subsequent implementation and focused proof.

| Clause | Current evidence | Concrete remaining delta |
|---|---|---|
| C1 catalog/envelopes | Exact refs/build/host pins, independent operations/artifacts, typed usage and job results have finite named proofs; F1 now supplies a required registered client-failure boundary. | Additional provider/client codecs need their own implementation and bounded acceptance, not a new generic framework by default. |
| C2 compatibility/client boundary | Required facets, named losses, signature/continuity scope and fallback accounting have named proofs. F1 and the public SG1 tools/budget/scoped-history intersection are implemented and executed, including consistent preflight. | Provider-specific reset/expiry and cross-codec policy breadth remain bounded module acceptance; see SG1 coverage minors rather than inventing an unlimited generic matrix. |
| C3 transforms | Configured registration/lifecycle, ownership/effects, bounds/cancellation and committed-output fences have finite SG5 evidence. | More provider wire overlays are implementation/acceptance work, not a newly established generic primitive gap. |
| C4 auth/leases | Shared code/device/action coordinator, owner/CAS/activation and scoped leases have named proofs. `53bf5e5` carries late callback completion versus credential replacement through public IPC, serving and restart. | Broader live-provider/auth acceptance remains module/product work; this bounded proof does not replace other resource/cancellation/activation-fault cases. |
| C5 operations/artifacts/replay | Binary interpretation, recipient leases, typed jobs, issuer-scoped prepared bytes and accepted-effect fences have finite SG3/SG6 proofs. | Real provider idempotency/retention guarantees and additional wire codecs require bounded module acceptance. |
| C6 change simulation | Independent modules execute ordinary controls and Unix daemon lifecycle/stream-independent IPC has evidence. F1 supplies an independent failure dialect; `2f94fe8` proves import/restart; `5b4c219` adds SG1; `53bf5e5` adds the public callback replacement race. | Source-backed difficult-provider onboarding remains below; no infinite provider-pair matrix. Export→validate alone is still not import/restart proof. |

## F1: registered client failure boundary follow-up

The initial inspection found raw decode/unknown-error reflection and no
registered precommit failure encoder. Executed regressions reproduced both.
`8269f21` adds required `ClientIngress.RenderFailure`, public provider host
contract 14, bounded safe metadata/optional diagnostics and reusable encoders.
The host retains HTTP status, dispatch effect and commitment authority.

An independently linked module now proves a different wire envelope through a
temporary daemon and public connection→Physical→Combo setup. Its HTTP bodies
are validated against independent and module schemas, with auth/decode/schema,
real cooldown/no-route and accepted-provider pre-first-byte cases. A final review
also reproduced staged-header leakage; the fix restores host headers before
either a safe failure body or empty fail-closed 500. Existing committed-stream
contracts remain separate and unchanged.

The [acceptance map](CLIENT_FAILURE_ACCEPTANCE.md) names the tests and installed
gate. This closes the generic F1 implementation gap only; it does not supply
the remaining C1–C6/public-boundary or live-provider proofs.

## Next order and limits

The [module portability proof](MODULE_PORTABILITY_ACCEPTANCE.md) executes export
to a separate database, explicit startup import without copying source manifests,
file-managed and DB-only restart, ordinary IPC apply, published model listing
and actual HTTP job/failure responses. It exposed and fixed bootstrap ordering
and precommit runtime validation, preserving exact pins and artifact recipient
checks. It does not complete all C6 semantic intersections or M15.

1. Keep the bounded F1 contract/proof as a regression gate; do not reopen it as
   another generic protocol framework.
2. Add only the named public-boundary acceptance deltas above, reusing accepted
   SG5/SG6/SG7 proofs rather than redoing every negative case per provider.
3. The [source-backed encoded/signing vertical](QODER_VERTICAL_ACCEPTANCE.md)
   now extends the older COSY mechanics fixture through normal credential/custom
   model setup, Physical/Combo and public JSON/SSE serving/restart. Its native
   model config is explicitly schema-configured: dynamic acquisition/persistence,
   live login/media and remote acceptance remain separate, unproved boundaries.

Onboarding/Physical UX breadth, real provider/harness compatibility, isolated
RSS and sustained tail latency remain product obligations. None is silently
closed by catalog size, a generic codec or these source-level observations.

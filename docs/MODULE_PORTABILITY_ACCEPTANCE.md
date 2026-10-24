# Independent module deployment acceptance

This follows the [design](superpowers/specs/2026-10-24-module-portability-design.md)
and [plan](superpowers/plans/2026-10-24-module-portability.md). It closes the
named fresh-target export/import/restart proof, not all of C6 or M15.

Verified 2026-10-24 at implementation checkpoint `2f94fe8`: full root/module
tests and vet passed after the final unique-memory isolation fix. Root bundle/
loader/startup and module portability races passed three times; the final
isolation regression passed race3 in 2.312s. After `make install-all`, the
explicit installed portability gate passed race3 in 40.924s. Actual daemon
status was healthy with control=serving=516 and no persistence fault counters;
bundle export→validate returned valid/version 9. Scoped configuration and
credential fingerprint was unchanged. No configured live upstream was called.

The initial executable proof found a genuine bootstrap cycle: a linked module's
artifact recipient requires a provider-definition, but IPC import is unavailable
until its complete catalog freezes. `Config.ConfigBundlePath` and daemon
`--config-bundle` load the same secret-free bundle before freeze. Required module
code remains statically linked; no source manifest is copied to the target.
Exact dependency and artifact recipient checks remain strict.

| Boundary | Evidence |
|---|---|
| Fresh-target deployment | `TestIndependentModuleBundleFreshTargetRestart`: separate empty SQLite path, no manifest directory, startup import, ordinary IPC apply, repeated fresh daemon instances |
| Actual consumption | Target `/v1/models` publishes only the role; real HTTP job requests use the imported provider/connection/codec; schema-validated failure after upstream acceptance dispatches once |
| Exact code/config closure | Literal operation/codec/decoder/schema/module requirements plus unchanged full dependency lock across import/restart |
| Credentials remain separate | Bundle excludes fake credential; locally provisioned credential survives file reapplication and subsequent DB-only restart |
| Rejected imports do not persist | `TestIndependentModuleBundleRejectsChangedPinsWithoutMutation`: module build/host, descriptor/schema digest, absent module, unsupported credential/protocol; reopen stock target and compare configuration exports |
| Strict startup file | `TestStartupBundleInputIsStrictAndBounded`: duplicate/unknown JSON members, trailing documents, null/array, oversized/missing file; parser error does not echo input |
| Preview isolation | `TestBundleRuntimePreviewDoesNotMutateOtherMemoryStores`: a real live memory-store sentinel survives preview; reproduced failure before unique memory-URI fix |
| Installed control plane | `make test-installed-portability GOBROOM_TEST_CLI=/absolute/path/to/gobroom`: installed CLI export/apply/status through explicit temporary IPC, plus the independent acceptance cases, race/count3 |

Review reproduced an additional ordering bug: invalid runtime values could be
committed before the first serving snapshot rejected them. Candidate validation
now shares route binding rules with the loader and compiles the candidate using
the real store/loader in uniquely named in-memory SQLite before durable apply.
This also guards live validate/diff/apply. It is cold-path configuration work,
not request-path persistence. It avoids maintaining a parallel graph compiler;
it does not claim negligible startup/admin cost or measured inference overhead.

The file is explicit desired configuration reapplied on every Start while its
option remains set. Remove the option for SQLite/IPC-managed configuration after
bootstrap. No credential/runtime-state migration, module-code installer or
cross-version compatibility is added. External/non-portable provider manifests
remain separately installed exact dependencies.

Deferred minor: semantic startup errors can include configured identifiers;
only parser input is deliberately hidden. No stored-credential disclosure was
observed. General runtime leak-freedom, SG1 tools/budget/scoped-continuity
composition, callback-versus-credential-replacement acceptance, difficult-provider
onboarding and whole-product performance remain separate obligations.

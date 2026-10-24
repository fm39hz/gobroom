# Independent module portability proof

Agent-selected acceptance design under the standing instruction to proceed
autonomously. This closes the named C6 export/import/restart proof, not M15.

Use the existing public daemon IPC, bundle v9, exact dependency locks and
statically linked job/failure modules. Export a configured source fixture;
apply its unchanged secret-free bytes to a separate empty database without
installing the source manifest directory. Restart with the same linked modules
so stored portable definitions bind normally. Supply credentials through the
existing connection action, never through the bundle. Verify published models,
actual HTTP job results, failure dialect, dependency pins and a second restart.

Also reject changed module build/host, descriptor/schema pins and absent linked
dependencies before config mutation. Re-export the target before/after rejected
apply and require unchanged data; no inference side effect is allowed.

Chosen over a same-database export→validate test because that cannot prove
portability. Rebuilding a standalone deployment executable is unnecessary: this
already separate Go module links only public packages and starts real daemon
instances. No new dependency, importer, storage backend or compatibility path.
Existing JSON binding retains full exported bytes and typed assertions inspect
exact refs; do not manipulate strings or invent secret-transfer semantics.

An initially green acceptance test documents existing behavior, not a reason
to change the kernel. Any discovered production defect requires an observed
failing regression before correction. Only temporary hosts/fake upstreams;
main owns all Go/format/test/install/commit. Live setup stays unchanged.

## Executed bootstrap finding and design amendment

Actual fresh-target tests fail before IPC: the job module artifact policy
requires the exact provider-definition recipient, absent until import. Keep
catalog completeness and artifact recipient validation unchanged. IPC-only
bootstrap is circular for this legitimate module shape.

Add explicit `Config.ConfigBundlePath` / daemon `--config-bundle`. After normal
socket ownership acquisition, read at most 32 MiB using strict standard-library
JSON v2 binding; reject duplicate/unknown members and trailing data. Register
its portable definitions before catalog freeze, validate exact dependency locks
and transform bindings, then transactionally apply through existing bundle
logic before publishing a serving snapshot. Missing linked code still fails.
The configured file is desired configuration applied on every Start, not an
implicit one-time seed; omit the option after bootstrap to return to ordinary
DB-managed configuration. Existing connection secrets are retained by ordinary
ApplyBundle; bundles never supply credentials. Conflicting installed definitions
still fail instead of silently replacing the same exact contract.

Chosen over copying a source manifest or relaxing recipient checks, which would
hide the missing deployment path. A separate offline importer would duplicate
ownership/validation/activation machinery. This amendment is agent-selected,
not an additional user approval claim, and requires failing tests before code.

Review correction: exact lock/schema validation alone does not prove serving
configuration is valid. Resolve protocol/auth-mode compatibility through the
same helper as the serving loader, and compile the candidate with the existing
store and loader in a uniquely named in-memory SQLite database before applying
to the real store. The same preview guards live validate/diff/apply. Unique
names isolate simultaneous previews and live in-memory embedders, as documented
by [SQLite](https://www.sqlite.org/inmemorydb.html). This adds cold-path work,
not inference-path storage. Reusing the real compiler avoids maintaining a
parallel graph validator or weakening catalog/recipient checks.

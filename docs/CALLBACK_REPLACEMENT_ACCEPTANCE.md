# Public callback replacement acceptance

This follows the [design](superpowers/specs/2026-10-24-callback-replacement-design.md)
and [plan](superpowers/plans/2026-10-24-callback-replacement.md). It extends the
existing direct-store composition test to actual public control and serving
boundaries. No new authorization engine, provider branch or dependency is added.

Verified 2026-10-24 at `53bf5e5`: full root/module tests and vet passed after
review assertion fixes; public race3 passed in 3.538s. Following install-all,
the explicit installed gate passed race3 in 9.730s. Actual daemon healthy with
control=serving=518 and zero persistence fault counters; export→validate valid
at version 9. Scoped config/credential fingerprint unchanged; no configured
live upstream was called.

| Boundary | Evidence |
|---|---|
| Admitted callback genuinely races replacement | Existing external callback validation is delegated, then a valid credential transition is held by channels, deliberately ignoring cancellation |
| Public replacement is not blocked by provider work | `connections.update` returns while the callback barrier remains closed |
| Replacement is immediately serving | A real HTTP request through the assigned route→Physical→published Combo reaches the fake upstream with the exact replacement bearer |
| Late completion cannot win | Releasing the held transition yields callback HTTP 400, setup `failed`, no actions or callback modes; serving still uses the replacement |
| Callback capability is released | The actual callback listener can be rebound; replay does not enter the driver again |
| Replacement is durable | A fresh daemon instance serves through the same public model using the replacement credential |
| Ordinary outputs remain private | Public setup/status/log outputs exclude callback proof, correlation and replacement credential markers |
| Installed clients exercise the same workflow | `TestInstalledCLICallbackReplacementRace`: installed CLI performs replacement, inspects setup/status/logs and checks healthy serving snapshots before/after restart |

The main case is `TestPublicCallbackReplacementCannotCommitStaleCredential`.
All setup/model operations use public Unix IPC, callback and inference use real
HTTP listeners, and persistence is observed through serving after restart. No
SQL/store mutation, synthetic executor or private credential read is used.
The driver decorator reuses existing callback/opaque-auth semantics rather than
duplicating the provider protocol. Timed waits bound failures; sleeps do not
create the race.

Run the explicit installed gate after installation:

```sh
make test-installed-callback-race GOBROOM_TEST_CLI=/absolute/path/to/gobroom
```

It validates the executable path and runs public/installed cases three times
under the race detector. Temporary databases, sockets and fake credentials are
used; configured live provider accounts are not touched.

This is one finite C4 boundary, not universal auth/provider acceptance or a
proof against indefinitely uncooperative module code. Other owner/CAS, callback
resource negotiation, device/code setup and activation-fault tests remain
separate evidence; this case does not replace them or close all M15.

Deferred fixture minor: the shared restart helper uses Background Stop/Start
and the global test deadline, not a dedicated per-restart timeout. All race
entry, callback delivery and ordinary IPC/HTTP waits are otherwise bounded.

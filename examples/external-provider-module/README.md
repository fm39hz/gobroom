# External provider module example

This directory is a separate Go module that imports only GoBroom's public
`pkg/provider` and `pkg/daemon` APIs. It demonstrates the static-module path:

1. `AudioModule` registers an operation contract, wire codecs, and a custom
   `QuotaSource` using only the public normalized quota request/snapshot types.
2. A separate provider manifest binds the audio task and quota source to the
   shared endpoint/transport primitives.
3. A custom binary embeds the module through `daemon.NewWithProviderModules`;
   the normal daemon then loads the provider manifest and exposes its metadata.

The example deliberately does not import `internal/*`. Its local `replace`
directive lets this checkout compile the example before GoBroom has a published
module version; downstream modules should depend on the released GoBroom module
version instead.

Run from this directory with `GOWORK=off go test ./...`. The tests compile the
module as an independent importer, bind the manifest, verify the custom quota
source through the registry, then start the public daemon API with the module
and exercise the operation through local IPC and a fake HTTP provider.

The artifact-probe fixture starts a Unix-only daemon and streams a local WAV
through `CallOperationProbe`. It checks upstream bytes/MIME, interpreter rejection,
spool cleanup and unchanged catalog/health/quota/usage. Set
`GOBROOM_TEST_CLI=/absolute/path/to/gobroom` to exercise the installed
`connections test-operation --artifacts-file` command against that daemon too.

The auth-generation fixture supplies its own device auth primitive using public
`DeviceAuthorizationFlow`/`DeviceAuthorization` types. A late token races a
connection update through Unix IPC; stale setup must fail, and the next operation
must authenticate with the newer credential. The same `GOBROOM_TEST_CLI` switch
exercises installed CLI device-start/get, connection update and operation probe.

The opaque-state fixture registers its own `opaque_ticket` mode with typed
`ticket`/`subject`/`workspace` metadata. Device setup and a typed custom-status
refresh return bounded `CredentialState`; `Resolve` derives the wire ticket
and runtime context. After daemon restart the next chat call verifies the
refreshed metadata survived, while config/IPC omit it. With `GOBROOM_TEST_CLI`,
successful device setup and post-restart inference also use the installed CLI.
The module host contract is v3; old auth/recovery contracts are rejected.
The auth mode explicitly opts into `recovery: refresh`. The fixture normalizes
HTTP498/`credential_expired` through typed classifier options, while separate
403 permission/quota roles and a recovery-disabled 401 do not refresh. The
shared ordinary manifest still uses default HTTP401 semantics for activation
tests; no protocol status recovery fallback remains in the kernel.

The auth-owner fixture holds device setup across provider-definition v1 → v2
and v1 → v2 → v1 updates. The old token cannot commit; an operation still uses
the retained credential. A fresh setup for the current owner then completes
and authenticates another operation. `GOBROOM_TEST_CLI` covers these provider
updates, device commands and probes through the installed control client.

The activation fixture injects failure in persisted snapshot-version writes
after credential CAS. A real 401 refresh still serves the replacement ticket;
the durable activation receipt records which stages applied. Explicit retry
or daemon restart repairs activation without another provider exchange. With
`GOBROOM_TEST_CLI`, it also checks activation list/get/retry and separate
control/serving status versions through the installed CLI. Public
`daemon.CredentialActivationView` is used instead of a second response schema.

The COSY signing fixture additionally uses the public `PrepareBytesRequest`
and `PreparedAuthorization` contracts. It encodes the business payload once,
then renews body-bound authorization for replay without changing those bytes
or the resolved endpoint. Request codecs without a prepared replay factory
cannot receive an automatic scoped retry after ambiguous dispatch.

`TestExternalPrincipalRenewalPreservesOperationalHealthAndQuota` holds an actual
upstream request across another role's auth rejection, refreshes its opaque ticket,
and waits for activation. Authentication recovers, while independent rate-limit
and unknown-reset exhausted-quota evidence survive renewal and daemon restart.
Explicit credential replacement then makes all three blocked roles usable.
`GOBROOM_TEST_CLI=/absolute/path/to/gobroom` also verifies installed health/quota/
activation/status views and performs replacement through the control client.
This fixture uses only a local fake provider and temporary database.

`JobModule` demonstrates async create/status/cancel as independent operations,
with schema-validated results and a client-held, connection-scoped inline handle.
The lifecycle test loses create/cancel responses, proves no duplicate job or
unsafe cancel replay, restarts the daemon, and rejects handles on another
account's role or after expiry/issuer changes. Operation-scoped cancel cooldown
survives restart without delaying status. A create-only HTTP rate limit remains
scoped to its operation/account across restart; other eligible accounts may
still be selected and status remains usable. No background job worker is needed.
To verify the installed control client too, run the lifecycle test with
`GOBROOM_TEST_CLI=/absolute/path/to/gobroom`.

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

The COSY signing fixture additionally uses the public `PrepareBytesRequest`
and `PreparedAuthorization` contracts. It encodes the business payload once,
then renews body-bound authorization for replay without changing those bytes
or the resolved endpoint. Request codecs without a prepared replay factory
cannot receive an automatic scoped retry after ambiguous dispatch.

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

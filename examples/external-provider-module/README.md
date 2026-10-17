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

The COSY signing fixture additionally uses the public `PrepareBytesRequest`
and `PreparedAuthorization` contracts. It encodes the business payload once,
then renews body-bound authorization for replay without changing those bytes
or the resolved endpoint. Request codecs without a prepared replay factory
cannot receive an automatic scoped retry after ambiguous dispatch.

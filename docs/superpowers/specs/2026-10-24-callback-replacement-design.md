# Callback completion versus credential replacement

Agent-selected bounded C4 proof under the standing implementation instruction.
The previous SG1 milestone changed authoritative code and passed installed gates;
this step continues the remaining public authorization boundary.

Use the existing external callback AuthorizationFlow and opaque credential
primitive. A small test decorator delegates callback validation, then holds a
successful credential transition on a channel, deliberately ignoring cancellation.
Through real Unix IPC, replace that connection's credential while the callback
is held. Replacement must complete without waiting for provider work. Serve
through the ordinary Physical/Combo HTTP endpoint before releasing the callback,
after its stale completion, and after a fresh daemon instance starts.

The replacement must remain authoritative; late callback work cannot overwrite
it, acknowledge successful activation, retain a live action/callback or grant
replay. Verify callback listener release and sanitized public setup/status/log
views. No direct store mutation, SQL inspection or synthetic kernel executor.
Credential contents are tested only through the actual upstream authorization
header. The provider's callback protocol is reused, not reimplemented.

Choose a channel-controlled public fixture over sleep-based timing or another
direct-daemon fault test. Existing auth/CAS/activation primitives should suffice;
do not add a new cancellation subsystem or dependency absent an executed defect.
Use ordinary JSON binding and callback URL types, not string manipulation.

Only main Go/gofmt/tests/install/commits. A worker may add the separate installed
CLI/privacy test against the shared fixture; other source checks are read-only.
Fake upstreams and temporary databases only. No real credential/config mutation.
Any production fix requires an observed failing regression first. This closes
the named race boundary, not all auth providers or complete M15.

# Owner-bound model core

The separate `cmd/xfriend-model-core` executable exposes Magpie's existing gateway,
provider catalog, account routing and OpenCode plugin runtime to an embedding host.
It does not call the standalone daemon entry point or import the CLI/GUI. The
normal Magpie executable keeps its existing discovery, storage and lifecycle.

## Sources of truth and ownership

The embedding application owns account IDs, authorization, the encrypted vault,
connection configuration and deployment locks. Each positive owner ID gets one
process initialized once with explicit absolute data/cache paths. Credentials are
not imported from the computer's existing CLI profiles. Built-in providers are
custom instances under opaque connection IDs; an installed plugin's selected
account is bound to exactly one such ID, rather than its whole vendor pool.

The complete control protocol is [pkg/modelcore/protocol.md](../../pkg/modelcore/protocol.md).
`internal/managedstate` stores provider, binding and plugin-auth JSON documents only
through parent callbacks; a successful `secret.set` requires durable acknowledgment
before publishing the new value. The Bun host serializes auth updates and waits
for the same acknowledgment. A failed write fails the authorization/refresh and
keeps the error sticky in that host; restart the owner process after repairing the
vault. Token refresh and inference still use the original plugin implementations.

Google Vertex AI accepts `vertex.credentialsJSON` as an explicit JSON string.
Managed connections reject credential paths and host ADC/gcloud/environment
fallback. Existing authorized-user, service-account and impersonation exchanges
mint tokens directly from that memory value; a credential digest keys the cache.

## Runtime and request flow

Initialization creates one authenticated IPv4 loopback HTTP listener. Its random
bearer travels only over parent stdio. Model references are `connection-id/model`
and `group/group-id`. The actual handler is `gateway.Server.Handler`; the standalone
listener's CLI synchronization, update, check-in and warm-up loops never run.

Every inference requires a parent-issued invocation ticket. Each actual upstream
attempt, including classifier, retry and fallback, requests admission over stdio.
The parent checks allowed connections and reserves/accountably reuses budget. A
rejection cancels only that invocation. Streaming headers are held until the first
admission succeeds, so an invalid ticket receives explicit HTTP 403 rather than an
empty 200. Every admitted response ends with one synchronous `attempt.finished`
event carrying usage; `barrier` after HTTP EOF provides a control-stream barrier.
Gemini URL models and multipart audio models are admitted too. Unidentifiable
inference formats fail closed. Usage is collected across a bounded streaming
buffer, retaining early Anthropic input usage alongside later output usage.

Existing Xfriend-native connections use an in-process transport emitting bounded,
credit-controlled `native.invoke` frames to the parent registry. No request reaches
the synthetic native endpoint and no OAuth state is duplicated. Canceling the HTTP
request emits `native.cancel` and releases the corresponding stream.

## Plugins and isolation

The parent supplies an exact installed plugin list and fixed Bun executable. Core
never installs/updates packages or downloads another Bun in managed mode. Directory
specs use the host's existing entry selection: package main before exports, so
OpenCode v1 `opencode.mjs` remains preferred over v2 `opencode2.mjs`. Provider
source listing explicitly materializes installed descriptors and reports auth
method indices plus disabled policy flags.

Bun receives an owner-specific HOME/XDG/APPDATA and a small environment allowlist;
model/API secrets in the application's environment are not inherited. Local CLI,
desktop and current-app sign-in methods are blocked. This is trusted bundled
plugin isolation, not an OS sandbox for arbitrary user code. Plugin stderr is
drained without persisting plugin-defined messages that could contain credentials.
Control catalogs omit credentials; route traces omit bodies and vendor error text.

EOF and shutdown stop the listener and owned plugin hosts. Graceful shutdown keeps
stdio available until auth writes, outstanding refreshes and host hooks drain.
No daemon, host account or unrelated process is killed.

## Verification

Run `go test -tags nogui ./pkg/modelcore` with Bun 1.4.2 on PATH to exercise the
real gateway, ordered fallback, native streaming, plugin signing, vault ACK and
vault failure. Without Bun the same integration still checks key-based routing and
native transport. `go test -race -tags nogui ./pkg/modelcore ./internal/provider
-run 'TestManaged|TestVertexImported'` covers the managed path, usage accumulation,
wire-model admission and in-memory Vertex credential rotation. Existing provider,
plugin cancellation/auth-read and classifier/decision tests remain applicable.

## Packaged standalone Magpie defaults

The ordinary Magpie executable also recognizes a read-only `runtime.json` v1
resource directory at `Contents/Resources/model-core` on macOS, or `model-core`
and `resources/model-core` beside the Windows/Linux executable. Its Bun version
and each package's exact name/version/path must match the manifest. A missing or
invalid bundle leaves the original standalone behavior unchanged.

A valid bundle supplies fixed plugins as runtime list defaults, without writing
`plugins.json` on discovery. An explicitly configured package, including a disabled
one, takes precedence. Existing user-installed packages and `MAGPIE_BUN` keep
precedence; a missing user installation can use the matching bundled version.
Bundled defaults are not auto-updated or reinstalled by Update All. Removing an
active bundled default records it as disabled rather than deleting app resources.
The package's Bun is never auto-updated or replaced by a crash fallback. This seam
does not start the managed core process from standalone Magpie.

## Native subscriptions

Antigravity uses the existing Google OAuth/Code Assist conversion under an
owner-bound account, with PKCE sessions, validated callbacks, selected-account
model/allowance reads and acknowledged refresh writes. It does not aggregate
accounts from host CLI profiles.

Claude uses a fixed, unmodified official Claude Code binary supplied at init.
Authorization runs its native `setup-token` flow or accepts the user's existing
setup-token. The long-lived inference credential goes to the parent's vault and
only into `CLAUDE_CODE_OAUTH_TOKEN` for that account's private official CLI run;
no third-party Claude OAuth client is implemented. Inference reuses the existing
subscription bridge, with core-owned MCP callbacks and invocation admission/usage
settlement. Its setup-token scope cannot report subscription quotas or verified
identity, and those are explicitly unknown. Renew it through the official flow.

Existing Xfriend Codex/Copilot/Gemini accounts keep their existing registry and
refresh owner and participate through the native callback transport. Factory uses
its real bundled provider plugin. Native reauthorization is an interactive user
step, not an export or an automatic CLI credential takeover.

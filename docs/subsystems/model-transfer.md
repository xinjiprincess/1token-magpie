# Model transfer to Xfriend

Model transfer is selective export, not whole-application backup restore.
`pkg/modeltransfer` owns the version-1 `xfriend-model-transfer` manifest, source
fingerprint, safe preview, selection, and encrypted file encoding. CLI and GUI use
the same collector. It reads Magpie's saved providers, groups, plugin descriptors
and saved account identities only. It never discovers another application's
accounts, refreshes tokens, starts the gateway/plugin host, installs a plugin, or
rewrites client configuration.

A preview contains provider names, whether an API key exists, model counts,
group names and masked account labels. Credentials remain on the source backend.
The fingerprint covers the actual API keys and configuration (creation time is
excluded); `export` rereads the source and refuses a stale fingerprint. Explicit
provider and group IDs are required, unknown/duplicate selections are rejected.
API keys, per-protocol endpoints and routing settings travel only when selected.
Keyless export removes keys, credential-bearing headers/proxies, extra body and
URL passwords. Vertex's source credential-file path is never exported.

Subscription identities always say `needs_reauth`. This implementation does not
claim an OAuth account has been transferred: login transfer capability is false,
and the target must authorize again. Raw OAuth/CLI authentication objects are
never copied. Plugin descriptors contain no options or executable files; local
plugins and credential-bearing source URLs are not portable. Installed version
and git commit, when known locally, are carried without resolving the network.
The target must approve/resolve a compatible fixed plugin dependency before use.
Missing route members must be reported by the target, not silently broadened.

`magpie model-transfer --stdio` bypasses the ordinary CLI `startUp` migration and
client-discovery flow. It receives newline-delimited `{id,method,params}` and
returns `{id,result}` or `{id,error:{code,message}}`. Methods are `describe`,
`list` (redacted preview), `export` (selected manifest), `seal`/`open` (encrypted
file), and `validate` (normalized manifest plus preview). Passwords go only over
this trusted pipe or the existing authenticated GUI API, never arguments,
environment variables or a deep link. The managed core may delegate `transfer.*`
to `modeltransfer.Call`.

Encrypted `.xfriend-models` files use the existing AES-256-GCM/PBKDF2-SHA256
600,000-iteration envelope from `backup.SealData`, with strict inner purpose and
version validation. General backup/library/agent data is rejected. Input is
bounded to 32 MiB; wrong password or tampering does not create any target record.
The GUI adds a provider-page export dialog and fixed, credential-free
`xfriend://models/import?source=magpie` launch action. Export creates a private
file in Downloads without overwriting an existing file. Xfriend owns import
preview/commit, owner isolation, conflict mapping and encrypted target storage.

Verification: `go test -tags nogui ./pkg/modeltransfer ./internal/gui -run
'TestCollectPreview|TestSelectedKeys|TestEncryptedFile|TestMalformedSource|TestStdioList|TestModelTransfer'`
and `node --test internal/gui/tests/model-transfer.test.cjs` in Chromium/WebKit.
Tests use temporary homes, source-shaped saved JSON and local API fixtures.

Explicit client configuration imports use `apps.list` / `apps.export` with a
`source_id` of `alma`, `cc-switch`, `claude-code`, `codex` or `opencode`. Only
the selected client's stock model configuration reader runs. API keys stay
behind a redacted preview and source fingerprint; OAuth entries are skipped
without refreshing or taking ownership. The managed core's positive transfer
allowlist excludes these source-reading methods.

`import.parse` in the managed core accepts a user-pasted `magpie://import` link
in the control request body, never a URL query or launch argument. It parses
the stock format into the same review manifest and preview without fetching
its icon or endpoint. Missing keys are rejected unless the preset permits
keyless use. Vertex ADC files and inline OAuth credentials never travel in
the transfer format; Vertex requires owner reauthorization.

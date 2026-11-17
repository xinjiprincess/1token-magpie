# Xfriend managed model core protocol 1

One process belongs to one owner. stdin/stdout carry newline JSON; stdout never contains logs.
Requests: `{ "id": 1, "method": "sources", "params": {} }`.
Responses: `{ "id": 1, "result": ... }` or `{ "id": 1, "error": { "code": "...", "message": "..." } }`.
The first request is `init` with `{protocol:1, owner_id:<positive integer>, data_dir:<absolute>, cache_dir:<absolute>, bun_path:<absolute bundled executable>, providers?:[], groups?:[]}`.
The response and a `ready` event contain `{protocol:1,owner_id,base_url,token}`. Inference uses the unmodified Magpie gateway HTTP APIs on base_url, and requires `Authorization: Bearer <token>`.
The owner cannot be changed after init. Caller-supplied owner fields on later requests are rejected.

The core asks its parent to persist owner-bound secret documents:
`{event:"secret",id:"secret-N",method:"secret.get",params:{ref:"modelcore/providers"}}`
`{event:"secret",id:"secret-N",method:"secret.set",params:{ref:"modelcore/providers",payload:<JSON>}}`.
Parent replies `{id:"secret-N",result:{payload:<JSON or null>}}` to get and `{id:"secret-N",result:{ok:true}}` to set. Errors use the same error envelope. `secret.set` must only succeed once the owner's vault commit is durable. No secret documents are written to core data/cache.

Methods:

- `sources` => `{presets:[PresetDef],plugins:[plugin.Provider],protocols:["chat","responses","anthropic","gemini"],strategies:[...],version:1}`.
- `providers.list` => `[Provider]` with every secret field removed and `credential_present` boolean.
- `provider.save` params `{provider:<Provider>,credential_ref?:"connection:<id>"}` => sanitized Provider. IDs are Xfriend opaque connection IDs; `preset` is vendor source ID. If credential_ref is supplied, `secret.get` returns `{api_key:string}` or `{key:string}`; the key is never returned.
- `provider.delete` params `{id}` => `{ok:true}`.
- `provider.models` params `{id,refresh?:bool}` => catalog model array.
- `provider.quota` params `{id}` => live allowance/usage result.
- `groups.list` => `[Group]`.
- `group.save` params `{group:<Group>}` => Group; `group.delete` params `{id}` => `{ok:true}`. Members use `<connection_id>/<upstream_model_id>` or `group/<groupid>`.
- `trace` => `{seq:number,routes:[...]}`; no prompt/response bodies or credentials.
- `plugin.list` => installed supported plugin provider descriptors.
- `auth.methods` params `{source_id}` => plugin auth methods with original `index` and policy `disabled` flags. Local CLI/desktop account reading is unavailable.
- `auth.start` params `{source_id,method:0,inputs:{},account_id?:string}` => plugin Authorization.
- `auth.finish` params `{session,code?:string}` => plugin Saved.
- `auth.key` params `{source_id,method:0,inputs:{},key,account_id?:string}` => plugin Saved.
- `auth.cancel` params `{session}` => `{ok:true}`.
- `provider.bind` params `{id,source_id,account_id,name?}` => sanitized bound Provider using the selected plugin account, not a shared source-wide account pool.
- `import.parse` params `{link:<magpie://import...>}` => `{manifest,preview}` using only pure link parsing; no discovery or network request.
- `transfer.open` params `{data:<base64>,password}` => Manifest.
- `transfer.validate` params `{manifest}` => `{manifest,preview}`.
- `transfer.seal` params `{manifest,password}` => `{data:<base64>}`.
- `barrier` => `{ok:true}`; after an inference HTTP EOF, every preceding `attempt.finished` has already been synchronously emitted before this reply.
- `shutdown` => `{ok:true}` then process exits after owned requests and credential writes drain.

Methods may execute concurrently. `cancel` params `{id:<request id>}` cancels that management operation. HTTP disconnect cancels its inference. Parent EOF cancels all operations and closes the gateway/plugin host. No external CLI settings, account discovery, LAN listener, auto update, check-in or warm-up are started.

## Per-invocation admission and existing native transports

Every inference POST requires `X-Xfriend-Invocation: <opaque parent-issued ticket>`.
The ticket stays in the request context, not any vendor's headers.
Immediately before each actual model HTTP attempt (including classifier and retry):

`{event:"admission",id:"admit-N",method:"call.admit",params:{invocation:<ticket>,provider_id:<connection_id>,model_id:<raw upstream id>,payload:<JSON request>}}`

Parent response: `{id:"admit-N",result:{ok:true,call_id:<unique attempt id>}}`.
On denial return an error envelope. Denial cancels only this inference's context,
so the underlying router cannot try another account to evade the decision.
When the response body ends/closes, one event reports actual usage:

`{event:"attempt.finished",call_id,invocation,provider_id,model_id,status,usage:<vendor usage object or null>,failed?:true}`.

A previously configured Xfriend-native connection is represented in core by
`provider.save` with `{provider:{id:<connection_id>,preset:"xfriend-native",models:[<raw ids>],...}}`.
It gets an internal synthetic endpoint and an in-process transport; no DNS or
network request to that endpoint occurs. A real request emits:

`{event:"native.invoke",id:"invoke-N",params:{invocation,provider_id,model_id,payload:<OpenAI Chat request>,window:1048576}}`.

The parent sends `{id:"invoke-N",result:{status:200,headers:{"Content-Type":"text/event-stream"}}}`,
then `{id:"invoke-N",event:"data",data:<base64 bytes>}` (at most 65536 decoded
bytes per frame), then `{id:"invoke-N",event:"end"}`. A bridge failure is an
error envelope. The initial decoded byte budget is 1048576; when consumed, core
emits `{event:"native.credit",id:"invoke-N",bytes:<n>}` to replenish it.
Cancellation emits `{event:"native.cancel",id:"invoke-N"}`. The parent cancels
the corresponding existing registry stream and releases its request resources.

Models exposed to Magpie gateway use `<connection_id>/<raw upstream model id>`;
groups use `group/<group_id>`. Public Xfriend refs remain
`native-<connection_id>/<raw model id>` (route connections map to `group/<id>`).

Additional auth helpers: `auth.prompt` `{source_id,method,inputs}` returns the
next plugin prompt or null; `auth.validate` `{source_id,method,key,value}` returns
`{error:string}`; `auth.cancel` `{session}` removes the session and makes late
callbacks unable to save an account.

Managed Google Vertex AI accepts `vertex.credentialsJSON` as a JSON **string**.
It reuses the credential minting implementation in memory. `vertex.credentials`
paths, ADC environment variables and gcloud fallback are rejected in managed
mode. The document is kept only in the parent's encrypted provider vault.

## Native managed subscriptions

`init.claude_path` names an optional absolute, fixed, unmodified official Claude
Code binary. It is never discovered from PATH or downloaded by core. Sources
`claude` and `antigravity` use the same source/auth/bind DTOs as plugin sources.
Claude method 0 runs official `setup-token` browser authorization; method 1
accepts an existing long-lived setup-token. Tokens are captured to the parent
vault, then injected only into the owned official CLI run. Its inference-only
scope cannot report account identity or subscription allowance; quota is unknown.
Antigravity method 0 uses the existing Google PKCE/token/project implementation;
method 1 accepts explicit OAuth JSON with snake_case token fields. Refresh must
receive a durable vault acknowledgment before the new access token is usable.
Neither source reads or rewrites another application's credential file or
keychain. `auth.cancel` and owner shutdown terminate their owned login flows.
The native Claude bridge uses the existing private, per-run-token MCP callback
on the core listener; no public management route is opened.

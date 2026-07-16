# CPA Account Config Manager

[Chinese documentation](README_CN.md)

`cpa-account-config-manager` is a standalone native plugin for
[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI). It provides a
dense account list, in-plugin account creation/details/editing/deletion, and
safe previewed batch edits that would otherwise require switching pages or
repetitive Auth JSON changes.

The plugin is modeled on the native plugin and background-job architecture of
[`ywddd/grok-inspection`](https://github.com/ywddd/grok-inspection), while its
selection and opt-in patch workflow follows the useful account-management
behavior in sub2api.

## Features

- Redacted account list with a visible account/plan type, plus search and
  provider, type, status, disabled, and editability filters. Imported sub2api
  `plan_type` metadata is preserved and shown ahead of the OAuth/API-key type.
- Passive per-account activity and usage from CPA native data: cumulative and
  recent request counts, cumulative Token counters, and Codex 5-hour/7-day
  quota progress when the upstream response headers are available.
- First-class row actions for redacted account details, fixed-scope
  single-account editing, and filename-confirmed deletion of eligible physical
  Auth files; the visible **Add account** action reuses the secure converter.
- Page selection, explicit selected-account scope, selected-account credential
  downloads, a remembered 20/50/100/200 page-size preference, and a fixed
  snapshot of all accounts matching the current filters.
- Quick bulk enable/disable plus opt-in edits for `priority`, `note`, `prefix`,
  `proxy_url`, `websockets`, and custom headers.
- Persistent default rules for `priority` and `websockets`, stored in CPA's
  host-owned plugin config so they survive restarts and plugin upgrades, with
  missing-only reconciliation for existing and newly uploaded Auth files.
- Explicit, preview-confirmed force sync when an operator deliberately wants
  to overwrite the managed default fields across editable Auth files.
- Server-side preview with editable, read-only, missing, and physical-file
  counts before any write starts.
- Bounded asynchronous jobs, per-account results, revision-conflict detection,
  best-effort continuation, and failed-only retry.
- Explicit target-system credential downloads for filtered or selected
  accounts, including CPA, sub2api, Cockpit, 9router, Codex, AxonHub, and
  Codex-Manager formats.
- Sanitized JSON, CSV, and JSON Lines reports for batch results.
- Preview-confirmed account import from pasted textual JSON or mixed multi-file
  JSON, JSON Lines, text, and ZIP selections, with recursive format conversion
  into CPA Codex Auth JSON and no overwrite of existing Auth files.
- Embedded React UI aligned with the Management Center page hierarchy,
  controls, dense tables, dialogs, and light/white/dark themes, with
  remembered-auth integration.

The plugin intentionally does not expose token refresh, OAuth reauthorization,
unrestricted credential editing, active provider quota probing, or scheduling.

## Compatibility

The plugin uses CLIProxyAPI native plugin ABI/schema version 1 and requires a
CLIProxyAPI build that provides:

- native plugin discovery and Management/resource routes;
- `host.auth.list`, `host.auth.get`, and `host.auth.save` callbacks;
- `PATCH /v0/management/auth-files/status`;
- `PATCH /v0/management/auth-files/fields`;
- authenticated `DELETE /v0/management/auth-files?name=<file.json>` for
  confirmed account deletion.

Token totals and Codex quota bars additionally use the native Usage Plugin
`usage.handle` callback. On a host that does not dispatch usage records, the
account list and CPA request counters still work, while Token and quota fields
remain unavailable.

It is self-contained and does not import CLIProxyAPI Go packages or require a
CLIProxyAPI executable patch.

Published releases target:

| Platform | Architecture | Library | Release archive |
| --- | --- | --- | --- |
| Linux | amd64 | `cpa-account-config-manager-v<version>.so` | `cpa-account-config-manager_*_linux_amd64.zip` |
| Linux | arm64 | `cpa-account-config-manager-v<version>.so` | `cpa-account-config-manager_*_linux_arm64.zip` |
| macOS | arm64 | `cpa-account-config-manager-v<version>.dylib` | `cpa-account-config-manager_*_darwin_arm64.zip` |
| Windows | amd64 | `cpa-account-config-manager-v<version>.dll` | `cpa-account-config-manager_*_windows_amd64.zip` |

Dynamic libraries are platform- and architecture-specific. Do not copy a
library built for a different target.

## Installation

### 1. Download and verify

Download the archive for the CLIProxyAPI host platform from
[Releases](../../releases), together with `checksums.txt` or the matching
`.zip.sha256` file.

Linux verification with a per-archive checksum file:

```bash
sha256sum -c cpa-account-config-manager_0.2.0_linux_amd64.zip.sha256
```

macOS verification:

```bash
shasum -a 256 -c cpa-account-config-manager_0.2.0_darwin_arm64.zip.sha256
```

Windows PowerShell verification:

```powershell
Get-FileHash .\cpa-account-config-manager_0.2.0_windows_amd64.zip -Algorithm SHA256
Get-Content .\cpa-account-config-manager_0.2.0_windows_amd64.zip.sha256
```

### 2. Install the library

Extract the archive and place the library in CLIProxyAPI's plugin directory.
The host checks the platform-specific directory first and then the plugin root:

```text
plugins/linux/amd64/cpa-account-config-manager-v0.2.0.so
plugins/linux/arm64/cpa-account-config-manager-v0.2.0.so
plugins/darwin/arm64/cpa-account-config-manager-v0.2.0.dylib
plugins/windows/amd64/cpa-account-config-manager-v0.2.0.dll
```

On Linux and macOS, make the library readable and executable by the
CLIProxyAPI service account:

```bash
chmod 755 plugins/linux/amd64/cpa-account-config-manager-v0.2.0.so
```

### 3. Enable the plugin

Add the plugin to CLIProxyAPI's `config.yaml`:

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    cpa-account-config-manager:
      enabled: true
      priority: 20
```

Restart CLIProxyAPI. The Management Center should then show **账号管理** in the
plugin menu.

The minimal configuration above is sufficient for the standard CLIProxyAPI
layout. `workers`, `data_dir`, and `management_base_url` are optional
overrides. If CLIProxyAPI is not listening on port `8317`, set
`management_base_url` to the loopback URL used inside the CLIProxyAPI process
environment. Use `http://127.0.0.1:<port>` from Docker as well; a Compose
service name such as `http://cli-proxy-api:8317` is not loopback and is
intentionally rejected. Only `http://` or `https://` loopback hosts
(`localhost`, `127.0.0.0/8`, or `::1`) are accepted. Credentials, paths, query
parameters, fragments, and remote hosts are rejected.

## Configuration

| Field | Default | Meaning |
| --- | --- | --- |
| `workers` | `6` | Concurrent account mutations. Values below 1 use 6; values above 16 are clamped to 16. |
| `data_dir` | `data/cpa-account-config-manager` | Optional directory for sanitized terminal job state, the backward-compatible `default-policy.json` policy/scan cache, and `usage-snapshots.json`. Override it when terminal jobs must be retained or the default path is not writable. `CPA_ACCOUNT_CONFIG_MANAGER_DATA_DIR` is used when this field is empty. |
| `management_base_url` | `http://127.0.0.1:8317` | Optional loopback CLIProxyAPI Management API base used by ordinary batch edits and confirmed account deletion. Default-policy reconciliation and force sync use host Auth callbacks instead. Environment fallbacks are `CPA_MANAGEMENT_BASE_URL`, a loopback-only `CPA_BASE_URL`, `PORT`, and `CPA_PORT`. |

The `enabled` and `priority` values in the same YAML object are owned by the
CLIProxyAPI plugin host. Account `priority` is a separate field edited through
the operator UI.

The operator UI automatically shallow-patches a non-secret `default_policy`
object into `plugins.configs.cpa-account-config-manager`. CPA persists that
object in its own `config.yaml`, so the enabled state, managed fields, values,
and scan interval survive process restarts and plugin-version replacement.
Manual YAML editing is not required. The private `default-policy.json` remains
a backward-compatible fallback and scan-summary cache; configured policy is
authoritative when both copies exist.

## Permissions

The CLIProxyAPI process needs:

- read and execute access to the platform library;
- read/write access to the CLIProxyAPI Auth directory, because CLIProxyAPI's
  canonical Management endpoints persist the requested account fields;
- read/write access to the effective `data_dir` when terminal job state must be
  retained. Policy scan-cache and sanitized usage snapshots use the same path
  on a best-effort basis; the durable policy itself lives in CPA config.

The plugin creates `data_dir` with mode `0700` where supported and writes
`results.json`, `default-policy.json`, and `usage-snapshots.json` through
temporary files with mode `0600`. The usage file contains only cumulative Token
counters, timestamps, and normalized Codex window percentages/reset times; it
never stores raw Auth JSON, API keys, failure bodies, cookies, or raw response
headers. Run CLIProxyAPI and the plugin under the same non-root service account
where practical.

## Operator Workflow

1. Open **账号管理** in the CLIProxyAPI Management Center.
2. If the official panel already remembered its Management Key, the embedded
   same-origin page reuses that session. Otherwise enter the CPA address and
   Management Key; the manually entered key stays in browser memory only.
3. Filter the account list and select either explicit editable rows or
   **All filtered**.
4. Use **Export selected** to download only the checked account IDs, or use the
   header download action for all current filter matches.
5. Choose bulk enable, bulk disable, or batch edit. Batch-edit fields are
   omitted unless their checkbox is enabled.
6. Review the server-created target snapshot, read-only/missing counts, patch
   field names, and warnings.
7. Start the job and inspect per-account progress. After a mixed result, use
   **Retry failed only** to target only failed or conflicting accounts.

The preview expires after five minutes. Starting a preview consumes that fixed
snapshot, so accounts that begin matching the filter later are not silently
added.

## Account CRUD

- **Add:** use the visible **Add account** toolbar action. It accepts pasted
  text JSON, mixed files, and ZIP archives through the conversion/import flow
  described below.
- **View:** the eye action opens an allow-listed detail view containing account
  identity, filename, provider/type, status, routing configuration, redacted
  proxy address, header names, request/usage counters, timestamps, and
  editability. Raw Auth JSON and credential values are never requested by the
  browser.
- **Edit:** the pencil action opens the existing opt-in editor with a fixed
  `selected` scope containing only that row. It uses the same server preview,
  physical revision check, shared writer slot, job results, and conflict
  handling as batch editing without changing the table's bulk selection.
- **Delete:** the trash action first creates a five-minute server preview for
  one editable file-backed account. The operator must type the exact `.json`
  filename. Start then acquires the shared writer slot, re-reads and compares
  the physical revision, and calls CPA's authenticated loopback Auth-file
  delete endpoint. A changed, missing, duplicated, runtime-only, or otherwise
  read-only target is not deleted.

Deletion is intentionally single-account only. There is no bulk
"delete filtered accounts" action, and a successful delete cannot be undone by
the plugin; back up the Auth directory or export the account before deleting it.

## Account Import

Open **Add account** from the operator toolbar. The file picker accepts up
to 64 JSON, JSON Lines, NDJSON, text JSON, and ZIP files in one mixed selection;
the textual JSON mode submits pasted content as one in-memory text source. A
source may contain one JSON value, multiple top-level JSON values, or JSON Lines;
each value may be one account, an array, or arbitrarily nested objects and
arrays. The converter recursively recognizes the common ChatGPT session,
sub2api, 9router, Codex, Codex-manager, and already-CPA credential aliases used by
[`GPTSession2CPAandSub2API`](https://github.com/Mxucc/GPTSession2CPAandSub2API).

Each ZIP may contain multiple directories and `.json`, `.jsonl`, `.ndjson`, or
`.txt` JSON sources. Directories and unrelated entries are not extracted and are
reported as skipped. A malformed individual source is also skipped when other
selected files remain usable.
Unsafe paths, symbolic links, encrypted entries, unsupported compression,
excessive expansion, and archive-limit violations reject the request before
any Auth file is written.

The server returns only account identity, source location, generated CPA
filename, and warnings. Converted credential values remain only in a bounded
five-minute in-memory preview; uploaded and pasted raw JSON is not retained by
the preview store. The browser clears pasted text and selected `File` references
after the preview is accepted. Confirming the preview writes each canonical
`type: codex` document through `host.auth.save` while holding the shared mutation
slot.

Import limits apply to the complete mixed request, not independently to every
archive:

| Limit | Value |
| --- | --- |
| Top-level uploaded files | 64 |
| ZIP entries across all archives | 256 |
| Multipart/raw request body | 12 MiB |
| One expanded JSON entry | 2 MiB |
| Expanded JSON across all ZIP files | 32 MiB |
| JSON nesting / visited nodes | 32 levels / 50,000 nodes |
| Converted accounts | 10,000 |

Generated filenames are reserved against the current Auth list during preview.
The plugin rechecks names immediately before writes and skips a collision rather
than calling the overwrite-capable host save operation. The host ABI does not
provide create-only compare-and-swap, so a narrow external race remains between
the final name check and save.

## Account Credential and Result Export

Account downloads use either the active filters, including the type filter, or
a fixed set of checked account IDs. Both paths require an explicit target
format in the download dialog. Selected IDs are sent in an authenticated POST
body rather than a potentially oversized URL. CPA exports preserve each
matching file-backed Auth JSON object. One account downloads as `email.json`;
multiple accounts download as a ZIP with one unique, path-safe `email.json`
entry per account.

| Account format | Shape |
| --- | --- |
| CPA | Original CPA Auth JSON; multiple accounts are packaged as ZIP. |
| sub2api | One `exported_at/proxies/accounts` import document. |
| Cockpit | Flat Codex token object, or an array for multiple accounts. |
| 9router | Codex OAuth object, or an array for multiple accounts. |
| Codex | Native `auth.json` object, or an array for multiple accounts. |
| AxonHub | AxonHub Codex Auth object, or an array for multiple accounts. |
| Codex-Manager | `tokens/meta` object, or an array for multiple accounts. |

Non-CPA targets accept compatible Codex OAuth Auth files. Unsupported,
runtime-only, invalid, or unreadable matches are skipped; the download response
reports exported and skipped counts in `X-Exported-Accounts` and
`X-Skipped-Accounts`.

These files contain credentials, including tokens and, for CPA output, any
stored proxy or header secrets. Credential downloads are authenticated exact
Management routes, require explicit format selection, set `Cache-Control:
no-store` and `Content-Disposition: attachment`, and are never persisted or
logged by the plugin.

Batch-result exports remain sanitized operational reports. They support JSON,
formula-safe CSV, and JSON Lines, and use only the existing allow-listed result
snapshot.

## Default Auth-File Policy

Open **Default policy** in the operator toolbar to manage `priority` and
`websockets` independently. An unchecked field is unmanaged; `priority: 0`
and `websockets: false` are valid managed values and are not treated as empty.
The policy cannot be enabled unless at least one field is managed.

When enabled, the plugin scans immediately and then polls every 15 seconds by
default. The operator may choose an interval from 5 through 300 seconds or
request an immediate scan. Each automatic scan uses `host.auth.list/get/save`
and fills only a managed key that is absent from the JSON object. Existing
values, including values supplied by an upload or a later manual edit, remain
authoritative. New Auth files become eligible on the next bounded scan; no
CLIProxyAPI core patch or browser Management Key is required.

**Force sync** is a separate destructive operation. It creates a five-minute
preview, shows the exact managed values and read-only skips, and requires an
explicit confirmation before starting. The job re-reads every eligible file,
rejects revision conflicts, and overwrites only `priority` and/or `websockets`
selected by the policy. It never changes `disabled`, proxy settings, headers,
notes, prefixes, tokens, cookies, credentials, or other unknown fields.

Saving through the operator UI first writes the complete non-secret policy to
CPA's host-owned plugin config and then applies it immediately in the plugin.
The local policy/scan cache is best-effort and does not block saving when
`data_dir` is unavailable. `management_base_url` is not used by automatic
scans or force sync; it remains an optional override for ordinary batch edits.

## Editable Fields

| Field | Behavior |
| --- | --- |
| `disabled` | Uses CLIProxyAPI's canonical auth-file status endpoint. |
| `priority` | Replaces account priority when explicitly enabled. |
| `note` | Replaces the note; leading/trailing whitespace is normalized. |
| `prefix` | Replaces or clears the routing prefix. |
| `proxy_url` | Accepts empty, `direct`, `none`, or `http`, `https`, `socks5`, and `socks5h` URLs. Current credentials are never returned to the browser. |
| `websockets` | Explicitly enables or disables WebSocket mode. |
| `headers` | Case-insensitive merge/remove operations. Existing values are never loaded into the browser. Hop-by-hop headers are rejected. |

Runtime-only, config-derived, virtual-child, stale, non-JSON, duplicated-source,
and otherwise unsupported records remain visible but read-only.

## Concurrency and Failure Semantics

- Only one Auth-file mutation path owns the writer slot at a time. Ordinary
  batch jobs, single-account deletion, imports, missing-only default-policy
  reconciliation, and force sync are serialized with each other.
- Every target is preflighted. Invalid, missing, duplicated, or read-only
  targets are skipped while eligible targets continue.
- The plugin records a SHA-256 revision during preview and re-reads the physical
  Auth JSON immediately before each write. A changed revision becomes a
  conflict and is not overwritten.
- The current ABI does not provide host-side compare-and-swap, so a narrow race
  remains between the final revision check and CLIProxyAPI's Management API
  write. The plugin does not claim strict cross-file atomicity.
- Successful writes are retained when other targets fail. There is no automatic
  cross-file rollback.
- Background policy reconciliation is always missing-only, uses the shared
  writer slot, retries shortly when that slot is occupied, and performs a final
  Auth re-read before `host.auth.save`; it does not overwrite a managed key that
  is already present.
- A process restart cannot resume a running job because the Management Key and
  patch values are not persisted. Persisted running state is marked
  `interrupted`; exact failed-only retry is available only while the in-memory
  patch intent still exists.

## Security Model

- Account lists, previews, errors, and batch-result exports are allow-listed and
  redacted. They never include raw Auth JSON, tokens, cookies, API keys, proxy
  credentials, or header values.
- Usage collection is passive. It listens to CPA native `usage.handle` records,
  does not call provider quota endpoints, and does not consume CPA's destructive
  `/usage-queue`. Only Token counters and an allow-list of Codex percentage,
  reset, and window headers are normalized into public and persisted snapshots.
- Credential export is a separate, explicitly selected Management download.
  Its attachment body intentionally contains target-system credentials, is
  marked `no-store`, is size/count bounded, and is never written to plugin
  state or logs.
- Every account route is a CLIProxyAPI authenticated Management route. The
  unauthenticated resource route serves static HTML only.
- A manually entered Management Key is held only in JavaScript memory. Reloading
  the page clears it. If the official panel remembered a key, the plugin reads
  that existing same-origin panel state but never writes its own credential to
  storage.
- The Management Key exists only in memory for an active mutation and is
  explicitly cleared when that operation ends. Full patch values remain in process memory only
  while needed by a pending preview, an active job, or failed-only retry; they
  are never persisted. Persisted job state contains sanitized status, field
  names, counters, and generic errors, not secret values.
- Batch start/retry and delete start use the request bearer key first. For non-browser callers,
  `MANAGEMENT_PASSWORD` or `CPA_MANAGEMENT_KEY` may provide an in-process
  fallback; the plugin never writes those environment values to disk.
- Nested Management calls accept only a loopback base URL and bound response
  reads to 64 KiB. Upstream response bodies are not copied into public errors.
- Delete preview/start are authenticated Management routes. A preview stores
  the private path/revision only in bounded process memory, exposes a safe
  identity summary, and is consumed only after CPA confirms deletion. The
  Management Key is cleared from the loopback client after every attempt.
- Default-policy scans and force sync call `host.auth.list/get/save` directly,
  so they neither request nor persist a browser Management Key. Raw Auth JSON
  is transformed only in process and is never returned by policy routes or
  written into plugin state.
- Import preview/start are authenticated Management routes. Multipart files,
  converted credentials, and raw JSON are never written to `data_dir`; public
  preview/result models are allow-listed, and preview memory is cleared on
  consumption, expiry, eviction, or plugin shutdown.

Do not expose the CLIProxyAPI Management API to untrusted networks. Protect the
Management Key independently of this plugin.

## Backup and Rollback

Before a large batch, back up CLIProxyAPI's `config.yaml` and Auth directory.
CPA credential export can provide a portable snapshot of matching file-backed
Auth JSON, but it does not preserve the complete directory layout, original
filenames, runtime-only records, or non-Auth configuration. The full directory
backup remains the authoritative rollback source.

To reverse an ordinary metadata edit, create a new batch with the previous
known values. To restore exact secret-bearing fields, restore the backed-up
Auth files and let CLIProxyAPI reload them.

To disable or remove the plugin itself:

1. Set `plugins.configs.cpa-account-config-manager.enabled` to `false`.
2. Restart CLIProxyAPI.
3. Remove the dynamic library after the process has stopped. Windows must stop
   the process before replacing or deleting a loaded DLL.
4. Optionally remove `data/cpa-account-config-manager/results.json` and
   `default-policy.json`. The first contains sanitized job history; deleting
   the second resets the saved policy and sanitized scan summary.

## Docker

Mount the platform library and a writable data directory into the CLIProxyAPI
container, then enable the plugin in the mounted configuration:

```yaml
services:
  cpa:
    volumes:
      - ./plugins/linux/amd64/cpa-account-config-manager-v0.2.0.so:/app/plugins/linux/amd64/cpa-account-config-manager-v0.2.0.so:ro
      - ./plugin-data:/app/data/cpa-account-config-manager
```

Use the actual image paths for your deployment. Because the plugin and
CLIProxyAPI run in the same container, `http://127.0.0.1:8317` normally remains
the correct internal Management URL. Restart the container after installing or
upgrading the native library.

## Management Routes

All 18 privileged routes are exact paths below `/v0/management/plugins/cpa-account-config-manager`:

- `GET /accounts`
- `POST /accounts/delete/preview`
- `POST /accounts/delete/start`
- `POST /batch/preview`
- `POST /batch/start`
- `GET /batch/status`
- `POST /batch/retry`
- `GET /export/accounts`
- `POST /export/accounts`
- `GET /export/results`
- `POST /import/preview`
- `POST /import/start`
- `GET /defaults`
- `PUT /defaults`
- `POST /defaults/scan`
- `POST /defaults/force/preview`
- `POST /defaults/force/start`
- `GET /defaults/force/status`

The static UI is served from
`/v0/resource/plugins/cpa-account-config-manager/index.html`.

## Development

Prerequisites:

- Go 1.24 or newer;
- Node.js 22 and npm;
- a native C toolchain supported by `go build -buildmode=c-shared`.

```bash
cd web
npm ci
cd ..
make verify
make package VERSION=0.2.0
```

For a local build that should publish a repository link in plugin metadata,
pass `REPOSITORY=https://github.com/<owner>/cpa-account-config-manager` to
`make build` or `make package`. GitHub Actions injects the actual repository
automatically.

`make package` writes the build-stage native library to `dist/` and a
plugin-store-compatible ZIP plus `.sha256` file to `dist/release/`. The ZIP
contains one installable library named `<id>-v<version>.<ext>` at its root.

### Local UI demo

Run the fake Management API and Vite in separate terminals:

```bash
cd web
MOCK_CPA_PORT=8318 npm run mock
```

```bash
cd web
VITE_CPA_BASE=http://127.0.0.1:8318 npm run dev
```

Open `http://127.0.0.1:5175`, leave the CPA address on the same origin, and use
the demo key `demo-key`. The mock contains synthetic accounts and simulates
row details/edit/delete, batch/default-policy progress, and mixed JSON/ZIP
imports; it does not edit real credentials.

## Release Process

The tag workflow accepts `vX.Y.Z`, runs the full verification gate, builds on
native runners for all supported targets, injects `X.Y.Z` into plugin metadata,
and publishes:

- one `<id>_<version>_<goos>_<goarch>.zip` per platform;
- one versioned `<id>-v<version>.<ext>` library at each ZIP root;
- one matching `.zip.sha256` file per archive;
- aggregate `checksums.txt` required by the CLIProxyAPI plugin store.

## License

MIT. See [LICENSE](LICENSE) and [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

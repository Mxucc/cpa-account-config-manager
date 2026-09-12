# CPA Account Config Manager

[中文文档](README.md)

`cpa-account-config-manager` is a native
[CLIProxyAPI (CPA)](https://github.com/router-for-me/CLIProxyAPI) plugin for managing accounts, AI providers, usage, routing policy, and automation from the CPA Management Center. It brings batch configuration, format conversion, model probes, quota and cost accounting, request risk control, inspection, remediation, proxies, notifications, and audit logs into one CPA-authenticated workspace while keeping raw credentials out of browser-visible data and logs.

## Core Capabilities

### Dashboard And Account Pool

- Summarizes account and AI-provider totals, enabled/disabled state, health, active requests, tokens, cost, inspection results, pending actions, and model cost rankings.
- Account lists support search, filters, persistent sorting, and page sizes of 20, 50, 100, 200, 500, or 1,000.
- View, add, edit, enable, disable, delete, and batch-edit accounts. Single-account editing loads the current configuration; batch workflows include previews, revision conflict checks, bounded concurrency, per-account results, and failed-item retries.
- Deduplicate by email first and account ID second, with options to ignore IDs or exclude Team/K12 accounts whose members may share an ID.
- Refresh tokens through CPA's native endpoint, or use the compatible full refresh flow when an account has a valid Refresh Token but the current CPA does not expose a refresh endpoint.
- Display creation and disable times, Priority, WebSockets, notes, routing prefixes, headers, proxies, model policy, concurrency, and quota policy.

### Import, Export, And Format Conversion

Imports accept pasted JSON and mixed JSON, JSON Lines, TXT, or ZIP uploads. A job can process up to 10,000 accounts and provides a preview, duplicate checks, background progress, and cancellation. ZIP input is guarded against path traversal and abnormal expansion, and existing Auth files are not overwritten.

Commonly recognized sources include:

- Native CPA Auth, Sub2API collections, Codex OAuth, Codex PAT, and Agent Identity.
- Claude/Anthropic, Kimi, Qwen, xAI/Grok, Gemini, Gemini CLI, and Vertex service accounts.
- Cockpit, 9Router, AxonHub, Codex Manager, and other common JSON shapes that can be normalized into CPA Auth files.

Exports support CPA, Sub2API, Cockpit, 9Router, Codex, AxonHub, and Codex Manager. A ZIP is produced automatically when a target cannot represent multiple accounts in one file. Batch and operation reports are also available as JSON, CSV, or JSON Lines without exposing credentials.

### Usage, Cost, Concurrency, And Quotas

- Persist successful and failed requests, tokens, live concurrency, rolling request-window events, and accumulated cost for both accounts and AI providers. Provider updates and CPA restarts retain historical usage through redacted credential fingerprints and aliases, so a regenerated auth index does not reset the counters.
- Display Codex 5-hour/7-day official quota windows, reset times, plans, and proactive reset counts. Plan detection prefers data inside `id_token`, then CPA info, then outer type fields.
- New Codex accounts collect plan and reset metadata automatically. Manual refresh updates both values, and accounts with remaining resets can consume one after explicit confirmation. Non-Codex accounts display `-`.
- Sub2API-compatible credit accounting is a permanent, default-on capability. Model prices synchronize in the background, successful requests are estimated in USD while raw token totals remain available, and very small costs retain useful precision.
- Accounts and supported AI providers can show current concurrency and enforce independent 15-second and 60-second rolling-window limits. `0` or an empty value means unlimited; both windows are enforced together.
- Account quota policies use the official 5h/7d percentages returned by CPA. AI providers can define 5h/7d USD budget amounts and then apply percentage thresholds to those budgets.
- AI provider model editors include “Fetch models”: the current channel credential is used to read the provider catalog, merge new models, and preserve existing aliases, display names, and mapping options.
- Plugin-owned quota, concurrency, routing, proxy, risk-control, audit, inspection, update, and experimental settings use private atomic stores. When `data_dir` is implicit, sanitized state follows the CPA Auth directory; API keys, OAuth tokens, Auth JSON, cookies, headers, request bodies, prompts, and proxy credentials are never written.

### Risk Control Center

- Inspired by Sub2API's content-risk workflow, the plugin runs a native check at the front of CPA's request-transformer chain. It supports disabled, observe-only (`observe`), and pre-routing block (`pre_block`) modes.
- Configure local blocked keywords plus all/include/exclude model filters, the public block status and message, event retention, and the maximum event count.
- Optional SHA-256 input-hash memory reuses a confirmed risk result so an identical normalized input can still be recognized after keyword changes. Both runtime insertion and persisted-state loading are capped at 4,096 hashes.
- The workspace reports observed, blocked, keyword-hit, and hash-hit totals with sanitized events, and can clear events or remembered hashes independently.
- Risk-control storage and Management APIs never retain prompt text, excerpts, request headers, tokens, cookies, API keys, or proxy credentials. Account identifiers are SHA-256-pseudonymized and matched rules are stored only as irreversible `kw:` references.
- The risk center contains content moderation, prompt auditing, and custom auditing modules; external audits persist only the endpoint, model, scanners, queue/timeout settings, and credential environment-variable name, with fail-open/fail-closed policies.
- Account quota limits use only CPA-collected official Codex 5-hour and 7-day usage percentages; each window has an independent threshold, blank means unlimited, and missing official usage is passed through rather than fabricated.

### Model Tests, Routing, And Codex Identity

- Load model catalogs from an account or AI provider and run a real probe through the selected account or the provider's own Base URL.
- Results include model, HTTP status, latency, and a sanitized upstream response. Primary, fallback, and compatibility models are supported, and completed `200` responses are recognized as success.
- The UI persists the last manually selected model and tested-model history; allowlisted accounts load an allowed model first.
- Model routing supports all models, allowlists, and blocklists. Manual tests, automatic probes, and inspection honor the policy. New Codex accounts can detect restricted compatibility and receive an automatic allowlist. Compatibility allowlisting is permanent and no longer requires an experimental toggle.
- Codex client identity policy is configured in the **Codex** view. It supports outbound identity convergence, an official-client ingress gate, App Server allowance, minimum/maximum versions, allowlists, blocklists, engine-fingerprint signals, and pass-through, device, session, or fully converged modes; the two Codex experiments (quota-overdraft continuation and Agent Identity / PAT) remain experimental toggles under Other settings → Experimental features.
- Only that global switch can enable the official-client ingress gate: an account or AI-provider policy can exempt a single target but never enable the gate on its own, so requests are not rejected after the master switch is turned off. A rejection reports its provenance (`source`, `reason`) so a plugin block can be told apart from an upstream restriction.
- Convergence and the ingress gate are independent: enabling convergence never rejects a request. Codex OAuth, `codex-api-key` health checks, and internal model, quota, token, PAT, and Agent Identity probes use a consistent compatible identity.

### Inspection, Automated Remediation, And Policies

- Inspection combines native CPA state, recent requests, Usage data, active model probes, and passive failures observed during service. It supports native fast scans, full scans, incremental scans, selected-account rechecks, review retries, live progress, and cancellation.
- Results distinguish healthy, abnormal, authentication-failed, quota-limited, and review states. HTTP 401 is recorded as invalid-credential evidence, recommends re-login or deletion, and can trigger immediate automatic disablement.
- Accounts can be disabled from evidence, re-enabled after quota refresh or reset time, and optionally receive a Priority boost after fresh quota becomes available. Automatic enablement only manages accounts that inspection disabled; it does not take ownership of manually disabled accounts.
- Automatic deletion has a separate risk confirmation, grace period, strong-evidence requirement, and file-backed-account restriction. Every automatic disable, enable, or delete action records its reason.
- Policy order is global policy, new-account default policy, then conditional policy. Default policy processes only new or changed accounts; processed fingerprints persist so stable accounts are not rescanned when the page opens or CPA restarts.
- Conditional rules support priorities and nested `all`/`any` groups matching provider, account type/plan, and email suffix.
- Actions include enable/disable state, Priority, 15-second/60-second concurrency, 5h/7d quota policy, notes, prefix, headers, WebSockets, separate account/AI-provider proxy profiles, model probing, all/allowlist/blocklist model policy, and Codex identity policy. Long-running work is asynchronous and does not block settings saves.

### Proxy Profiles And External Notifications

- Maintain multiple proxy profiles with add, edit, delete, enable, and disable operations. Credentials are shown only in masked form and existing secrets are never refilled into the browser.
- Accounts and AI providers reference proxy profiles separately, with overrides available in global policy, default policy, conditional policy, and batch editing. This capability resolves [issue #3](https://github.com/Mxucc/cpa-account-config-manager/issues/3).
- External notifications support multiple HTTPS GET targets, including generic Bark and ntfy endpoints. Template values can be previewed and test-sent; diagnostics show the final URL, HTTP status, attempt count, and concrete variable values. Percentage variables include `%`.
- General notifications and policy notifications are independent. A policy notification has a unique name, display order, one or more URLs, nested `all`/`any` match conditions, and available-count or availability-rate thresholds. Once attached to a policy, it is not controlled by the general trigger settings.

### AI Providers

The dedicated AI Providers workspace manages:

- OpenAI-compatible, Gemini API Key, Interactions API Key, Claude API Key, Codex API Key, xAI API Key, Vertex API Key, and CPA generic API-key channels.
- OpenCode Go.
- OpenCode Zen and self-hosted `opencode-cc` through a custom Base URL. OpenCode Zen defaults to `https://opencode.ai/zen` when no Base URL is provided.

Provider fields include type, name, state, model count, concurrency, Base URL, API Key, model mappings, Priority, Weight, prefix, headers, proxy, and channel-specific options. CPA channel entries have no name field of their own, so the plugin stores a channel record (the operator label and the usage identities observed for that channel) keyed by a salted, irreversible digest of the channel base URL and API key. The record is revalidated against the live channel list and rewritten on every read, so channels that share only a URL or only a key stay apart. Changing the API key re-reads the channel under the new digest; when that base URL is unique among channels of the same kind, the previous record (label and historical usage identities) is adopted so usage history still lines up. A duplicated base URL is never adopted, so two channels cannot claim each other's history. API keys are always masked, and an empty key during editing preserves the stored value. Each row's More menu groups reset local usage and delete channel (delete last). Supported operations include view, test, edit, enable, disable, delete, model catalogs, real model probes, token/cost accounting, 15-second/60-second concurrency, 5h/7d custom budgets, proxy profiles, and Codex identity policy. Capabilities that the current CPA cannot edit are shown as compatibility-limited instead of pretending to work.

OpenCode Go additionally supports Workspace ID plus auth Cookie, 5h/7d/30d quotas, reset times, manual refresh, and deletion. Configuration and refresh live in the authenticated UI: the standalone OpenCode status page is served without authentication, so it only renders cached state, masks workspace identifiers, and never accepts credentials.

### Codex

A dedicated **Codex** workspace is added to the side menu directly before **OpenCode**:

- The workspace is organized as three tabs, in this order: Overview, Models and prices, and Fingerprint profiles.
- Overview shows the counts (Codex accounts, AI-provider channels, disabled models, account-level overrides, provider-level overrides, and overridden fingerprint fields), the effective convergence mode, and an indicator for whether the global model control is active.
- The Codex identity policy editor lives on Overview. The two Codex experiments — the 5h/7d quota overdraft continuation and Agent Identity / PAT — remain experimental toggles in **Other settings → Experimental features**; the Codex view echoes their current values when it saves and the experimental panel echoes the identity policy, so neither view can clear the other.
- Fingerprint profiles turn every value that used to be compiled into the Codex fingerprint into an editable field with its built-in default shown next to the current value, an "overridden" marker, a per-field restore, a per-group restore, and a restore-everything action.
- The editable fields are: convergence mode; the client identity strings (User-Agent, Originator, Version, OpenAI-Beta) and the turn-metadata header name; explicit installation/session/thread ids (empty means derive) and the window suffix; the derivation prefixes for installation, session and thread ids plus the seed strategy (per account or one fixed seed); and the body toggles (turn timestamp, relationship fields, and prompt-cache-key rewrite).
- Clearing a field, or restoring it, returns it to the default; an invalid value is rejected and changes nothing. The profile applies to every Codex account and every Codex AI-provider channel, and the per-account and per-provider convergence overrides on the AI providers page still take precedence over the profile default.
- Models and prices provides one global switch list for Codex model ids showing how many Codex accounts and AI-provider channels reference each model, with a selection column, per-row **Test** and **Disable**/**Enable** actions, and bulk **Disable selected** / **Enable selected** alongside the existing enable-all action; a Codex model probe runs through a stored Codex account credential. Each model also shows the rate the plugin's own accounting resolves for it: the input, output and cache-read price in USD per million tokens, taken from the same Sub2API / Wei-Shaw price table that rates Codex usage, with the model's long-context multiplier noted and the table's source and last sync time shown above the list. A model the table does not price is marked as unpriced instead of being presented as free. Disabling a model applies to every Codex account and AI-provider channel at once and is enforced in the request path: a disabled model is answered immediately with a rejection instead of being forwarded upstream, so the change takes effect on the next request without waiting for host-side policy application. Only Codex traffic is affected; the same model id on another provider family is untouched.
- Both settings are stored in the plugin private data directory (0600) and exposed through management routes that require the Management Key, under `/v0/management/plugins/cpa-account-config-manager`: `GET /codex/overview`, `GET|PUT /codex/fingerprint`, `POST /codex/fingerprint/reset`, and `GET|PUT /codex/models`. Responses never include a credential.

### OpenCode

A dedicated **OpenCode** workspace is added to the side menu directly after **AI Providers**:

- The workspace is organized as tabs for easier management, in this order: Overview (billing summary, conversation-session status, and a counts strip), Go accounts, Zen accounts, Channels, and Models and prices (the price catalog plus the model test). The quick links and the refresh action stay available on every tab, and opening a model test from an account row switches to the Models and prices tab.
- OpenCode Go is bound by signing in to opencode.ai, opening the Go workspace page, and entering the Workspace ID plus the auth Cookie (the value of `auth`, used for quota scraping), plus an optional OpenCode Go API Key; each workspace shows the stored 5-hour/7-day/30-day quota.
- OpenCode Zen credentials are added with a name, an API Key, and a Base URL that is either the Zen gateway `https://opencode.ai/zen` (the default) or a self-hosted `opencode-cc` bridge such as `http://localhost:8787`.
- The Channels tab reads the CPA AI-provider channels that already belong to OpenCode and lists them with kind, name, Base URL, model count, key status, and whether the workspace already manages them. One click imports a channel credential instead of asking for it twice: a Zen channel, including a self-hosted `opencode-cc` bridge, becomes a Zen account, and a Go channel attaches its API key to the workspace account that already holds the Workspace ID and auth Cookie. A Go channel without such an account answers with a distinct state that sends the operator to the Go accounts tab to add the Workspace ID and auth Cookie first. The credential is read and stored server-side and never reaches the browser.
- “Load models” fetches the model list from the OpenAI-compatible endpoint `GET {base}/v1/models` with the `x-opencode-client: cli` header and an `opencode/<version>` User-Agent, and caches it per account.
- Model testing sends a real `POST {base}/v1/chat/completions` probe with the selected model and reports the status, reason code, HTTP status, and latency; reason codes are `authentication_failed`, `model_not_found`, `quota_limited`, and `upstream_unavailable`.
- One-click binding (“publish models to CPA routing”) upserts an `openai-compatibility` CPA channel with Base URL `{base}/v1`, the stored API Key as the key entry, and the OpenCode headers, and publishes the verified model catalog into the channel model list, so CPA routes OpenCode models natively; binding the same account again updates the existing channel instead of creating a duplicate and keeps operator-defined aliases. A stored Go credential can be completed or corrected from the Go accounts table ("Edit credentials"): the Workspace ID, auth cookie and API key are updated in place, an empty field keeps its stored value, and a missing cookie or API key is flagged in the row — so a legacy credential that only stored an API key no longer has to be deleted and re-added.
- Quick links open `https://opencode.ai/auth`, `https://opencode.ai/workspace`, `https://opencode.ai/zen`, and the read-only OpenCode status page.
- The auth Cookie and API Key are written once over the authenticated Management connection and stored in the plugin private data directory; they are never returned to the browser (only a `key_set` boolean) and never written to logs.
- Billing follows the official OpenCode documentation: OpenCode Go per https://opencode.ai/docs/go/ and OpenCode Zen per https://opencode.ai/docs/zen/. Prices, allowances, and billing parameters are re-parsed from those pages on every sync; models.dev is kept only as the machine-readable mirror that fills gaps. Prices published in the official tables take precedence and are marked as official, so an official price change applies without upgrading the plugin. The plugin revalidates the catalog every 24 hours with an ETag conditional request, keeps a cached copy in the plugin private data directory, and ships an embedded official snapshot, so prices are available offline and before the first sync. The OpenCode workspace shows the catalog (model, input, output, cache read, cache write, context window, and context-length price tiers) with the source and last sync time and a “sync prices” action.
- The Models and prices tab provides a per-model control table: every row has a selection column, per-row **Test** and **Disable**/**Enable** actions, and the bulk **Disable selected** / **Enable selected** / **Enable all**. Disabling applies to every OpenCode account and channel and is enforced in the request path — a matching request is rejected immediately with `opencode_model_disabled` instead of being forwarded upstream — and it is the intended way to stop GPT-class models from consuming OpenCode Go resources. Testing an OpenCode model runs the probe through the OpenCode credential that references it. Writes and tests show progress on the row that was clicked and confirm the result globally, and a disable/enable never waits for the CPA management API channel scan: the click answers immediately while the scan refreshes in the background (a read waits at most five seconds and then falls back to the previous scan), so a slow or unreachable management API can no longer make a click look like it did nothing.
- OpenCode-routed usage is priced with OpenCode's own prices instead of a generic vendor table: when a request can be attributed to an OpenCode channel by its recorded channel base URL, the official Zen or Go prices are used. Prices are public data, but the routes still require the Management Key.
- OpenCode Go billing is a $10/month subscription where each model has its own monthly USD allowance (examples from the docs: GLM-5.3 is $15 and GLM-5.3-Flash is $60 per month), split 20% for the 5-hour window, 50% for the weekly window, and 100% for the month. The workspace shows each model's monthly allowance with the derived 5-hour and weekly budgets, plus the official per-window estimated request counts, and flags officially deprecated models with their deprecation date.
- OpenCode Zen billing is pay-as-you-go per 1M tokens. The balance auto-reloads when it drops below $5 by default with $20, and a monthly usage limit can be set for the workspace and for each member. The workspace shows the metered mode together with these parameters.
- The subscription price, the window split, and the auto-reload threshold and amount are parsed from the documentation text with built-in defaults as a fallback, so a change on the OpenCode side is adopted by the next sync. The official-docs sync time and the mirror sync time are shown separately, and a temporarily unavailable source keeps the last usable data instead of clearing prices.
- OpenCode Go routing always derives a session id for a request attributed to OpenCode: OpenCode Go asks clients to “Send a stable session ID in `x-opencode-session` for each conversation so we can optimize routing and prompt caching” (from https://opencode.ai/docs/go/#where-can-i-use-it). The value is resolved in order: an inbound `x-opencode-session` is preserved; otherwise a native client session header is reused (Claude Code, Codex, ZCode, and Pi style headers are recognized); then a session id found in the request body (`prompt_cache_key`, `session_id`, `conversation_id`); otherwise a keyed digest of the system/first-user seed; when the body cannot be parsed into such a seed, a keyed digest of a bounded prefix of the raw body; and finally a stable credential-and-model seed, so a request attributed to OpenCode is never sent without the header while the same conversation keeps the same id across turns and no message text is ever sent. Attribution is layered: when the CPA auth index matches a recorded OpenCode channel it is exact; otherwise the model decides — a model published by OpenCode on a request that is not Codex traffic is attributed to OpenCode, and the model match is prefix- and separator-insensitive, so a channel prefix such as `opencode-go/` cannot hide a model. That model fallback is required because OpenCode Go rejects a request that carries no `x-opencode-session`, so a channel list that is momentarily unknown must not stop the header from being sent. Injection is limited to model ids published by OpenCode (the Zen and Go catalogs plus each account's loaded catalog); no other model is touched. The channel created by one-click binding also carries a baseline `x-opencode-session` value, so a host without request interception stays routable instead of failing upstream.
- The workspace shows the session status: active/inactive, covered model count, requests given a session id, and distinct conversations observed, plus the attribution breakdown (how many requests were recognized by channel and by model) and the skip reasons (Codex traffic, another channel, an untargeted model), so an operator can see why a specific request did or did not receive the header. Session ids are never logged, and the per-installation salt is stored 0600 in the plugin data directory.
- Session stickiness mirrors the Codex behaviour: the scheduler account pick keeps one conversation on the same upstream account. The sticky key is read from the request's session headers (`x-opencode-session`, `session-id`, `session_id`, `x-session-id`, `x-codex-session-id`, `x-claude-session-id`, `conversation-id`, `x-conversation-id`) or, when none of them carries a usable value, from the session-related request metadata (`session_id`, `conversation_id`, `prompt_cache_key`, `x-opencode-session`, `thread_id`). A mapped account is reused while it has capacity; if it saturates, disappears from the candidate list, or the mapping ages out (30 minutes), the pick falls back to the normal least-loaded selection and re-maps the conversation. The mapping is memory-only and bounded (4,096 conversations), is never persisted, and a request without any session identifier keeps its previous behaviour exactly.
- All routes are exact paths that require the Management Key, under `/v0/management/plugins/cpa-account-config-manager`: `POST /opencode/models` with `{kind: "go"|"zen", account_id}` returns the redacted account view with models; `POST /opencode/model-test` with `{kind, account_id, model, timeout_seconds?}` returns the status, reason code, HTTP status, latency, tested time, and detail; `POST /opencode/bind` with `{kind, account_id}` returns the binding (kind, Base URL, index, created, channel key); `POST /opencode/accounts` also accepts `{account_id, api_key}` for a key-only update, where an empty `api_key` preserves the stored key and a new key invalidates the cached model catalog; `GET /opencode/pricing` returns the catalog with its sync provenance; `POST /opencode/pricing/refresh` revalidates it and reports whether it changed; `GET /opencode/session` returns the session routing status; `GET /opencode/channels` returns the OpenCode channels with their import state; `POST /opencode/import` with `{base_url}` imports one channel credential, returning 200 on success or a 409 marked `needs_workspace` for a Go channel that needs the workspace credentials first; `GET|PUT /opencode/model-control` returns the model rows plus the price-table provenance, with `PUT` taking `{"disabled": [...]}`; both require the Management Key.

- The self-update never goes through the Plugin Store: `GET /self-update` returns the current version, the resolved version and source, the archive and checksum state, the located plugin library file, and `restart_required`; `POST /self-update/check` resolves the latest release immediately (GitHub API, then the `releases/latest` redirect, then `releases.atom`); `POST /self-update/install` downloads the archive for the running platform, verifies its SHA-256 against the release `checksums.txt`, and atomically replaces the plugin library while keeping `<library>.previous` as a backup, so a failed verification never replaces anything; `PUT /self-update/settings` with `{"plugin_file": "..."}` records the library path when the host cannot detect it. All four routes require the Management Key and answer with versions, checksums, file paths, and state only. A release also carries `ui/index.html`: the plugin serves that copy straight from its private state, so an interface change takes effect on a page refresh without a CPA restart, and the panel reports "interface updated" separately from the restart the library still needs (`ui_updated` / `interface_refresh_only`).

### Audit Log, UI, And Updates

- The persistent audit log covers import, export, batch changes, model tests, policy scans, inspection, automatic remediation, notifications, and plugin updates. It records success, failure, partial completion, failure basis, counts, sanitized samples, source, and time.
- The UI supports Simplified Chinese, Traditional Chinese, English, and Russian, and follows CPA language and theme. Optional neutral, indigo, forest, and rose themes, comfortable/compact density, small/medium/large fonts, and separate title/description sizing are available.
- Table sorting, page size, filters, and manual model selections persist.
- The plugin can check and install updates through the CPA Plugin Store and display current/latest CPA versions. It only detects CPA program updates and never replaces the CPA executable. When the Plugin Store request cannot return data, Other Settings → Updates also updates the plugin directly from its own GitHub releases: it resolves the latest tag, selects the archive for the running platform, verifies its SHA-256 against the release `checksums.txt`, and only then atomically replaces the plugin library while keeping the previous file as a backup. Downloads are restricted to GitHub hosts and bounded in size.

## Experimental Features

The remaining opt-in experiments are:

- **Codex 5h/7d quota overdraft continuation** (edited here, in **Other settings → Experimental features**): after a quota is exhausted, run up to five probes; any successful probe keeps the account enabled, while five failures allow automatic disablement. The first ordinary-request failure freezes the quota-window baseline, overdraft tokens and costs are tracked separately, and the cycle ends when quota resets. This modifies the Codex tool-call chain and may increase time-to-first-token on slower servers.
- **Agent Identity and PAT** (edited here, in **Other settings → Experimental features**): import, conversion, login, and native-plugin authentication paths for these formats, including common Sub2API-compatible structures.

Sub2API-compatible cost accounting, automatic model compatibility allowlists, and the Codex client identity policy are permanent features and are not experimental toggles. The Codex identity policy editor is edited in the Codex view; the two experiments above remain experimental toggles on this panel, and each view echoes the values it does not own.

## Installation

Installing from the CPA Plugin Store is recommended. CPA selects the platform archive, verifies checksums, and reports whether a host restart is required. Releases are also provided for:

| Platform | Architecture | Library |
| --- | --- | --- |
| Linux | amd64 | `.so` |
| Linux | arm64 | `.so` |
| macOS | arm64 | `.dylib` |
| Windows | amd64 | `.dll` |

For a manual installation, verify the matching `.sha256`, extract the library into CPA's plugin directory, and enable it in `config.yaml`:

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    cpa-account-config-manager:
      enabled: true
      priority: 20
```

After CPA loads the plugin, open **CPA-A Manager** in the Management Center. Most Plugin Store updates need only a page refresh; restart CPA only when the host reports `restart_required: true` or the loaded library is locked. The direct GitHub self-update replaces the plugin library file on disk while the running CPA keeps the previous library mapped, so it always requires a CPA restart to take effect (the UI reports `restart_required`).

## Configuration And Persistence

UI settings are written back to CPA's plugin configuration. Deployment-level fields remain available:

| Field | Default | Purpose |
| --- | --- | --- |
| `workers` | `6` | Concurrent account mutations, clamped to 1-16. |
| `data_dir` | `data/cpa-account-config-manager` | Private usage, cost, inspection, policy, notification, update, job, and log state. |
| `management_base_url` | `http://127.0.0.1:8317` | Loopback CPA Management API address used by the plugin. |

Persist `data_dir` when CPA runs in a replaceable container. Without an explicit directory, the plugin can store sanitized usage state beside a common Auth directory, but an explicit persistent mount is more predictable. The CPA process needs read/write access to the Auth directory and the effective data directory.

## Security Model

- Privileged operations use fixed, CPA-authenticated Management routes; the public Resource route serves only the embedded static UI.
- The Management Key remains in the current browser/CPA request chain and is never persisted by the plugin.
- Raw Auth JSON, tokens, cookies, API keys, proxy credentials, header values, and upstream responses are excluded from public models, logs, and persisted state.
- Imports and exports are bounded by count and size. ZIP input is checked for path traversal and abnormal expansion.
- Account writes use previews, physical revisions, a shared writer lock, and conflict checks. Destructive operations such as deletion and quota reset require explicit confirmation.
- Private directories and files use restrictive permissions where supported.

## Compatibility

- Baseline features require CLIProxyAPI native plugin ABI/schema v1, Auth list/get/save callbacks, the Usage Plugin callback, and current authenticated Management APIs.
- Live concurrency and 15-second/60-second enforcement require a newer CPA request-lifecycle hook/native plugin schema v2. Older CPA versions show unsupported/unavailable state instead of claiming that limits are active.
- AI-provider runtime and quota policy degrade safely when older CPA builds lack the corresponding Management routes; editable capabilities follow what the connected CPA actually exposes.
- The plugin does not import CPA Go packages and does not patch the CPA binary.

## Development

Prerequisites are Go 1.24+, Node.js 20+, npm, `make`, and a C toolchain suitable for CGO.

```bash
make verify
make build
make package VERSION=X.Y.Z
```

`make verify` checks Go formatting and tests, React tests and build output, embedded assets, and release metadata. Release tags use `vX.Y.Z`; the workflow produces archives for four platforms, matching `.sha256` files, and `checksums.txt`.

## Acknowledgements

- Inspection design and remediation workflow: [seakee/CPA-Manager-Plus](https://github.com/seakee/CPA-Manager-Plus)
- Native inspection and job patterns: [ywddd/grok-inspection](https://github.com/ywddd/grok-inspection)
- Codex failure and quota presentation: [ysxk/codex-429-autoban](https://github.com/ysxk/codex-429-autoban), [zhumengling/codex-token-usage](https://github.com/zhumengling/codex-token-usage)
- Agent Identity import and login concepts: [catoncat/codex-agent-identity-web](https://github.com/catoncat/codex-agent-identity-web)
- OpenCode Go quota monitor: [zcyoop/opencode-go-quota-cpa-plugin](https://cnb.cool/zcyoop/opencode-go-quota-cpa-plugin)
- OpenCode Zen and multi-protocol bridging: [Kiowx/opencode-cc](https://github.com/Kiowx/opencode-cc)
- Community link: [LINUX DO](https://linux.do/)

These projects informed product behavior. Their code is not copied into this plugin unless separately identified by the repository license history.

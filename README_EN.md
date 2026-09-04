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

- Persist successful and failed requests, tokens, live concurrency, and accumulated cost for both accounts and AI providers across CPA restarts.
- Display Codex 5-hour/7-day official quota windows, reset times, plans, and proactive reset counts. Plan detection prefers data inside `id_token`, then CPA info, then outer type fields.
- New Codex accounts collect plan and reset metadata automatically. Manual refresh updates both values, and accounts with remaining resets can consume one after explicit confirmation. Non-Codex accounts display `-`.
- Sub2API-compatible credit accounting is a permanent, default-on capability. Model prices synchronize in the background, successful requests are estimated in USD while raw token totals remain available, and very small costs retain useful precision.
- Accounts and supported AI providers can show current concurrency and enforce independent 15-second and 60-second rolling-window limits. `0` or an empty value means unlimited; both windows are enforced together.
- Account quota policies use the official 5h/7d percentages returned by CPA. AI providers can define 5h/7d token budgets and then apply percentage thresholds to those budgets.

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
- Codex client identity policy is a standard configuration surface. It supports outbound identity convergence, an official-client ingress gate, App Server allowance, minimum/maximum versions, allowlists, blocklists, engine-fingerprint signals, and pass-through, device, session, or fully converged modes.
- Identity settings can be inherited or overridden at global, default-policy, conditional-policy, account, and AI-provider levels. Codex OAuth, `codex-api-key` health checks, and internal model, quota, token, PAT, and Agent Identity probes use a consistent compatible identity.

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

Provider fields include type, name, state, model count, concurrency, Base URL, API Key, model mappings, Priority, Weight, prefix, headers, proxy, and channel-specific options. API keys are always masked, and an empty key during editing preserves the stored value. Supported operations include view, test, edit, enable, disable, delete, model catalogs, real model probes, token/cost accounting, 15-second/60-second concurrency, 5h/7d custom budgets, proxy profiles, and Codex identity policy. Capabilities that the current CPA cannot edit are shown as compatibility-limited instead of pretending to work.

OpenCode Go additionally supports Workspace ID plus auth Cookie, 5h/7d/30d quotas, reset times, manual refresh, and deletion.

### Audit Log, UI, And Updates

- The persistent audit log covers import, export, batch changes, model tests, policy scans, inspection, automatic remediation, notifications, and plugin updates. It records success, failure, partial completion, failure basis, counts, sanitized samples, source, and time.
- The UI supports Simplified Chinese, Traditional Chinese, English, and Russian, and follows CPA language and theme. Optional neutral, indigo, forest, and rose themes, comfortable/compact density, small/medium/large fonts, and separate title/description sizing are available.
- Table sorting, page size, filters, and manual model selections persist.
- The plugin can check and install updates through the CPA Plugin Store and display current/latest CPA versions. It only detects CPA program updates and never replaces the CPA executable.

## Experimental Features

The remaining opt-in experiments are:

- **Codex 5h/7d quota overdraft continuation**: after a quota is exhausted, run up to five probes; any successful probe keeps the account enabled, while five failures allow automatic disablement. The first ordinary-request failure freezes the quota-window baseline, overdraft tokens and costs are tracked separately, and the cycle ends when quota resets. This modifies the Codex tool-call chain and may increase time-to-first-token on slower servers.
- **Agent Identity and PAT**: import, conversion, login, and native-plugin authentication paths for these formats, including common Sub2API-compatible structures.

Sub2API-compatible cost accounting, automatic model compatibility allowlists, and Codex client identity policy are permanent features and are not experimental toggles.

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

After CPA loads the plugin, open **CPA-A Manager** in the Management Center. Most Plugin Store updates need only a page refresh; restart CPA only when the host reports `restart_required: true` or the loaded library is locked.

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

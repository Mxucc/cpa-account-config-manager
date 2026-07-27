# CPA Account Config Manager

[Chinese documentation](README_CN.md)

`cpa-account-config-manager` is a native
[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) plugin for managing
CPA accounts from one authenticated workspace. It adds account CRUD, batch
operations, import/export conversion, quota visibility, model tests,
inspection automation, conditional policies, notifications, and an operation
journal without exposing raw credentials to the browser.

## Highlights

- List, search, filter, view, add, edit, enable, disable, deduplicate, and
  delete accounts. Page sizes include 20, 50, 100, 200, 500, and 1000.
- Run selected or filtered batch edits with a server-side preview, revision
  checks, bounded workers, per-account results, and failed-only retry.
- Display CPA request counters, token totals, Codex 5-hour and 7-day quota
  windows, plan type, and active reset count when CPA or the upstream account
  provides that data.
- Test provider models through CPA for Codex/OpenAI, Claude, Gemini/AI Studio,
  and xAI. Account allow-lists and deny-lists are respected by manual tests,
  automatic probes, and inspection.
- Detect restricted Codex model sets and apply a compatible account allow-list
  when policy-driven new-account probing is enabled. This capability is built
  in; it is not controlled by an experimental switch.
- Inspect all accounts with CPA-native evidence and optional active model
  probes. Results are published while a scan is running and include health,
  evidence, recovery time, and an executable recommendation.
- Optionally auto-disable failed or exhausted accounts, auto-enable only
  inspection-owned disables after recovery, and auto-delete narrowly eligible
  accounts after explicit confirmation and a grace period.
- Keep a persistent, redacted operation journal. Disable and enable entries
  include the actual normalized reason, such as quota exhaustion, invalid
  credentials, quota reset, health recovery, or credential refresh.
- Follow the CPA Management Center language and theme. The UI supports English,
  Simplified Chinese, Traditional Chinese, and Russian.

## Account Transfer

Import accepts pasted JSON text or up to 10,000 accounts from mixed JSON,
JSON Lines, text, and ZIP files. A ZIP may contain multiple supported files.
Every import is previewed, bounded, duplicate-checked, and written only through
CPA Auth callbacks; existing Auth files are not overwritten.

Supported inputs include native CPA files, common sub2api account collections,
Codex OAuth/PAT and Agent Identity variants, Claude, Gemini, and other formats
that can be converted into a CPA-supported Auth document. Agent Identity
conversion remains opt-in under Experimental Features because it depends on
upstream authentication behavior.

Exports can target CPA, sub2api, Cockpit, 9router, Codex, AxonHub, and Codex
Manager formats. Formats that cannot represent multiple accounts as one file
are downloaded as a ZIP containing separate account files. Operational reports
are available as JSON, CSV, or JSON Lines and never contain credentials.

## Inspection And Automation

Inspection combines three evidence sources:

1. CPA account state, recent requests, and usage records.
2. Scheduled or manual active model probes through CPA.
3. Passive failures observed while CPA serves traffic.

Automatic actions are disabled by default. Auto-enable never claims an account
disabled by an operator or another system. Automatic deletion requires a
separate risk confirmation and remains limited to file-backed accounts with
strong, current evidence. Raw upstream bodies are not persisted in inspection
state or operation logs.

The default policy is incremental. Each stable account is processed once and
its processed identity is persisted across plugin and CPA restarts. Periodic
scans still discover new accounts, but already processed accounts are skipped.
Codex plan and active-reset metadata is refreshed before conditional rules are
evaluated. A quota endpoint HTTP 401 becomes sanitized invalid-credential
evidence for inspection instead of inflating the policy write-failure count.

Conditional policies can match nested `all`/`any` conditions using provider,
account type, plan type, and email suffix. Rules have explicit priorities and
can set account priority, WebSockets, new-account probing, and model policy.
The base policy runs first; conditional rules then override only the actions
they own.

External notifications support multiple operator-defined HTTPS GET templates.
Templates can combine allow-listed account and health variables, can be
previewed with current values, and have a test action. Delivery outcomes and
bounded HTTP metadata are recorded in the operation journal.

## Installation

Installing from the CPA Plugin Store is recommended because CPA selects the
platform archive, verifies checksums, and reports whether a host restart is
required. Manual release archives are also available for:

| Platform | Architecture | Library |
| --- | --- | --- |
| Linux | amd64 | `.so` |
| Linux | arm64 | `.so` |
| macOS | arm64 | `.dylib` |
| Windows | amd64 | `.dll` |

For a manual installation, verify the matching `.sha256` file, extract the
library into CPA's platform plugin directory, and enable it in `config.yaml`:

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    cpa-account-config-manager:
      enabled: true
      priority: 20
```

After CPA loads the plugin, open **CPA-A Manager** in the Management Center.
Most CPA plugin-store updates need only a page refresh; restart CPA only when
the host reports `restart_required: true` or the loaded library is locked.

## Configuration And Persistence

The UI persists supported settings back to CPA's plugin configuration. These
optional plugin fields remain available for deployment-level configuration:

| Field | Default | Purpose |
| --- | --- | --- |
| `workers` | `6` | Concurrent account mutations, clamped to 1-16. |
| `data_dir` | `data/cpa-account-config-manager` | Private state for usage, inspection, policies, updates, runtime ownership, jobs, and logs. |
| `management_base_url` | `http://127.0.0.1:8317` | Loopback CPA Management API base for authenticated operations. |

Persist `data_dir` when CPA runs in a replaceable container. If no explicit
data directory is configured, the plugin can mirror sanitized usage state
under a common local Auth directory, but an explicit persistent mount is more
predictable. The CPA process needs read/write access to the Auth directory and
the effective plugin data directory.

Experimental Features currently contains:

- Codex weekly-quota overdraft probing, which relies on upstream tool-call
  continuation behavior.
- Codex Agent Identity/PAT conversion and authentication hooks.

Both are off by default and isolated behind stable hooks so they can be removed
without changing the standard account-management paths.

## Security Model

- All privileged endpoints are fixed, authenticated CPA Management routes.
- The public resource route serves only the embedded static UI.
- Management keys remain in the current browser/CPA request path and are never
  persisted by the plugin.
- Raw Auth JSON, tokens, cookies, proxy credentials, header values, and raw
  upstream responses are excluded from public models, logs, and saved state.
- Imports and exports are count- and size-bounded. ZIP entries are validated
  against path traversal and archive expansion limits.
- Account mutations use previews, physical revisions, a shared writer lock,
  and conflict checks. Destructive actions require explicit confirmation.
- Private directories and files use restrictive permissions where supported.

## Compatibility

The plugin uses CLIProxyAPI native plugin ABI/schema version 1 and requires a
CPA build with native plugin discovery, Auth list/get/save callbacks, the Usage
Plugin callback, and current authenticated Management APIs for Auth status,
field edits, account-selected API calls, deletion, and plugin-store updates.
It does not import CLIProxyAPI Go packages and does not patch the CPA binary.

## Development

Prerequisites: Go 1.24+, Node.js 20+, npm, `make`, and a C toolchain suitable
for CGO.

```bash
make verify
make build
make package VERSION=X.Y.Z
```

`make verify` formats and tests Go code, tests and builds the React UI, checks
embedded assets, and validates release metadata. Release tags are annotated as
`vX.Y.Z`; the release workflow builds four platform archives, four matching
`.sha256` files, and `checksums.txt`.

## Acknowledgements

- Inspection design and remediation workflow:
  [seakee/CPA-Manager-Plus](https://github.com/seakee/CPA-Manager-Plus)
- Native inspection and job patterns:
  [ywddd/grok-inspection](https://github.com/ywddd/grok-inspection)
- Codex failure and quota presentation:
  [ysxk/codex-429-autoban](https://github.com/ysxk/codex-429-autoban) and
  [zhumengling/codex-token-usage](https://github.com/zhumengling/codex-token-usage)
- Agent Identity import and login concepts:
  [catoncat/codex-agent-identity-web](https://github.com/catoncat/codex-agent-identity-web)
- Community link: [LINUX DO](https://linux.do/)

These projects informed product behavior. Their code is not copied into this
plugin unless separately identified by the repository license history.

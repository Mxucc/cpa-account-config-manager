# Evidence — provider v2 (S1–S7 收尾)

Collected 2026-11-08 in the workspace root. Every claim below maps to a command that
was run or a file that exists; nothing here is inferred.

## 1. Real-browser capture

Scripts (in-repo, reusable):

- `web/scripts/mock-provider-ui.mjs` — same-origin static host for the built
  single-file UI plus a fixture provider API (`/v0/management/plugins/…/providers*`).
  Fixtures include an **unreported** quota window (`used_percent: null`), a wallet,
  a repairing credential and an unpublished channel.
- `web/scripts/capture-evidence.py` — Playwright 1.58 Chromium, logs in through the
  real form, walks 7 views at 1440×900 and 390×844, opens the account drawer, and
  asserts: no horizontal overflow, no console/page error, the unreported window
  says “未上报”, the wallet balance is visible, exactly 8 meters are drawn, and no
  screenshot is blank (<10 kB).

Commands:

```
cd web && npm run build                 # dist/index.html 1,430.61 kB, gzip 350.71 kB
node web/scripts/mock-provider-ui.mjs &
python3 web/scripts/capture-evidence.py
```

Output (verbatim):

```
shots: 15
overflow: 0/14
script_exit=0
```

`visual-report.json`:

```json
{"ok": true, "failures": [], "quota_text": {"says_not_reported": true, "mentions_balance": true, "meters": 8},
 "shots": 15, "console": []}
```

Artifacts in `.web-builder/evidence/sprint-5/`:

| screen | views |
| --- | --- |
| desktop 1440×900 | overview, accounts, providers, provider-codex, provider-compat, quota, usage + account-drawer |
| mobile 390×844 | overview, accounts, providers, provider-codex, provider-compat, quota, usage |

A real defect was found and fixed by this capture: `QuotaBar` drew an empty
`Progress` for an unreported window (meters 9 instead of 8), which reads as 0%.
`web/src/v2/shared.tsx` now renders no meter when the share is unknown.

## 2. Magpie hook execution (opt-in, default off)

- `internal/magpiecompat/client.go` — JSON-line sidecar client: one process per
  probe, 20s timeout that kills it, 4 MB stdout cap, concurrency 2, executable
  allow-list (`bun` only), crash isolation, absolute-directory check.
- `internal/magpiecompat/host.mjs` — embedded sidecar; imports the plugin, reads
  the login declaration, model catalog and allowance hook. It never calls `fetch`,
  writes nothing, and throws if a plugin requests a shell.
- `internal/provider/magpie_adapter.go` — maps a probe into the same
  `Account/QuotaWindow` rules (used/limit → percent, balance stays a balance).
- `internal/manager/provider_settings.go` + `provider_hooks.go` — the switch
  `magpie_hook_execution` (default `false`), persistence in
  `<data>/provider-settings.json`, and the degradation paths.
- `internal/provider/import.go` — a manifest may name a `directory`, which must
  live under `<data>/magpie-plugins`; anything else is refused.

Tests (`go test ./internal/magpiecompat ./internal/manager -run '…'`):

- success against a real Bun + fixture plugin (models deduplicated, methods read,
  nameless window dropped, wallet window keeps balance and no share)
- timeout → `ErrTimeout`, returns in <3s
- crash → `ErrPluginInvalid`, and the next probe still works
- oversized output → `ErrOutputTooLarge`
- no Bun (empty PATH) → `ErrBunUnavailable`
- relative directory refused; plugin noise before the JSON answer ignored
- switch off → no probe runs, `/providers/accounts` unchanged, catalog
  `hook_execution_enabled=false`, entry `hook_execution=false`
- switch on + fixture → `/providers/accounts` carries the plugin account
  (`models.count=2`, `source=plugin`, first window `used_percent=15` from 1.5/10,
  second window a wallet with `balance=4.25` and no `used_percent`, billing `wallet`)
- switch on + timeout → no account, catalog entry gets `hook_error`
- switch on + no runtime → no probe attempted, `bun_available=false`,
  `hook_error` explains why
- settings persist to disk, invalid body → 400
- import refuses a directory outside the plugin folder

## 3. Legacy shell removal

- `git status` shows 46 deleted front-end files; `web/src/App.tsx` no longer exists.
- Kept as the compatibility layer: `CodexWorkspace`, `OpenCodeWorkspace`,
  `ClinePassWorkspace` (plus the settings/policy panels the new pages use).
- `web/src/styles.css` pruned 3317 → 2137 lines by removing rules whose class
  names no longer appear in any remaining source; rules still referenced by the
  legacy consoles were kept (a textual audit of every `className` token in
  `src/components` found no class left without either a rule or an unused-status).

Runtime proof the compatibility tab still works (Playwright against the built page):

```
has_tab_label: True
has_legacy_codex_ui: True
… 兼容（旧界面） | CODEX | Codex 工作区 | … | 总览 | 模型与价格 | 指纹配置 …
```

## 4. Full verification

`make verify` → `EXIT=0`, log at `.web-builder/evidence/verify.log`, which contains
`go test ./...`, `go test -race ./...` (all packages ok), `go vet`, TypeScript
`--noEmit`, the frontend suite and the hosted build.

Counts observed:

- Go: root, `internal/manager`, `internal/provider`, `internal/magpiecompat`,
  `internal/releasepack` all `ok` (with `-race` too).
- Frontend: 34 files / 321 tests passed (the legacy shell's own tests went with the
  files they covered; every kept component keeps its own suite).
- Build: `internal/web/dist/index.html` 1,430.61 kB, **gzip 350.71 kB**.

Packaging:

```
make package VERSION=0.2.0     → exit 0
dist/release/cpa-account-config-manager_0.2.0_darwin_arm64.zip        5,431,237 bytes
dist/release/…zip.sha256      e67b84e55463a908866d646fa7527fb8790cf872081542c13cfb2c415047e714
dist/release/checksums.txt    same digest, verified by `shasum -a 256 -c`
```

## Not verified

- No live CLIProxyAPI v8 + Management Center instance was available, so the plugin
  was not installed into a running host. v8 behaviour is covered by contract tests
  against an HTTP fixture (`internal/manager/management_api_version_test.go`).
- The Bun sidecar was exercised with a local fixture plugin only; no community
  plugin was installed from the network.

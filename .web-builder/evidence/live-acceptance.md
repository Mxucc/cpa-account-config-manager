# Live acceptance — v0.3.1478 on https://cpaous.256.ltd

Date: 2026-11-08. Host: CPA `v8.0.60`, plugin `0.3.1478`
(`plugins/linux/amd64/cpa-account-config-manager-v0.3.1478.so`, `supports_quota: true`).
Reproduce with the scripts noted per section; all of them live in the session scratch
directory and drive the real host with a real management key.

## 1. No sign-in inside the v8 Management Center

`live_accept.py` plants the center's own session entries (`isLoggedIn`, `managementKey`,
`apiBase`, written with the center's `enc::v2::` obfuscation) on the plugin page's origin and
then opens the page.

- `no-sign-in path: SIGNED IN` — the shell renders 总览 without any key being typed.
- The login form is still the answer when the center is not logged in: covered by
  `web/src/store/panelAuth.test.ts` (`does not sign in from a stored key when the center is
  not logged in`).
- Evidence: `.web-builder/evidence/live-accept/00-no-signin.jpg`.

## 2. One-click community import

- `GET .../providers/market?locale=zh-CN` → `available: true`, 96 plugins, 26 with a version
  from npm; the rest report no version instead of a wrong one.
- `POST .../providers/import {"id":"@magpie-community/opencode-zcode-auth"}` → 200, provider
  `zcode` registered (version `0.1.57`, `source: magpie`, `hook_execution: false`).
- `GET .../providers` now lists `zcode`; the market marks that entry `imported: true`.
- The list renders with a 导入 button per row and 已导入 for the registered one.
- Evidence: `.web-builder/evidence/live-accept/providers.jpg`, `market.jpg`.
- Offline/unreachable behaviour and the id validation are covered by
  `internal/provider/market_test.go` and `internal/manager/provider_market_test.go`.

## 3. Inspection data no longer breaks the page

- `GET .../inspection/results?page=1&page_size=50` → 21 rows, **no row with an empty
  `health` / `reason_code` / `confidence` / `recommendation`**.
- The automation tab renders its table with 0 console errors (`live_accept.py`).
- `internal/manager/inspection_publish_test.go` covers both the placeholder skip and the
  non-empty enum contract; `web/src/api/inspectionResultRows.test.ts` covers the client's
  tolerance for a legacy row.

## 4. Automation and settings rebuilt

`live_accept.py` walks every page and every sub-tab on the live host:

| surface | result |
|---|---|
| 总览 / 账号 / 供应商 / 额度与计划 / 用量与计费 / 路由与模型 | rendered, no problem lines |
| 兼容（旧界面） | rendered (legacy workspaces intact) |
| 自动化: 巡检 / 自动策略 / 外部通知 / 自动重试 / 代理档案 | all five rendered |
| 日志与设置: 通用设置 / 操作日志 / 风险控制 / 实验性功能 / 插件配置 / 更新 | all six rendered |

- `console=0 http=0` for the whole walk (no console error, no 4xx/5xx on `/v0/`).
- 390×844: `doc === win === 390` (no horizontal overflow).
- `grep -rn "components/" web/src/v2/pages/` → no matches. The three legacy workspaces are
  reached through one named bridge, `web/src/v2/legacy/LegacyConsole.tsx`, which is outside
  `v2/pages` and exists only because the contract keeps them.
- Evidence: `.web-builder/evidence/live-accept/*.jpg`.

## 5. Account management

`live_account_actions.py` and `live_catalog.py` drive the dialogs on the live host. Only
read-only calls are made (preview / read), so the host state is unchanged.

| action | live result |
|---|---|
| 编辑配置 → 变更预览 | `POST /accounts/config` 200, `POST /batch/preview` 200, preview rendered |
| 删除账号 → 预览 | `POST /accounts/delete/preview` 200, preview rendered |
| 模型目录 | `POST /accounts/models` 200, **20 models rendered** |
| 重复账号 → 开始扫描 | `POST /accounts/deduplicate/preview` 200, groups/missing rendered |
| 导出账号 | dialog opens with format + scope (7 formats) |
| 批量编辑 / 批量删除 | enabled with the count of CPA-managed rows |

The first attempt passed the provider-scoped id and the host answered
`400 scope contains no editable accounts`; the page now resolves the CPA account id from
`GET /accounts?editability=editable` (joined on `auth_id` / `name` / `label`) and hides the
CPA-side actions for a plugin-owned workspace, saying so in one line.

## 6. Release and deploy

- Tags `v0.3.1476`, `v0.3.1477`, `v0.3.1478`, each with a green `CI` and `Release Plugin` run
  and a release carrying 4 zips + 4 sha256 + `checksums.txt`.
- Deployed through the host's plugin store (`POST /v0/management/plugin-store/…/install`).
  Cloudflare reports 502/524 for that call while CPA finishes the install server side, so the
  version was verified afterwards from `GET /v0/management/plugins`.

## 7. Regression

- `make verify` EXIT=0 (Go vet, tests, web tsc, vitest, style contract, build).
- `gofmt -l .` empty.
- Legacy routes still answer: `GET .../opencode/accounts` 200.
- `GET .../providers/catalog`, `.../providers/settings`, `.../accounts` 200.

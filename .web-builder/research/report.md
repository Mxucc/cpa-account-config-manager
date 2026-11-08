# S0 研究：CLIProxyAPI v8 / Management Center / magpie / 现有三域

采集时间：2026-11-08。所有结论均来自本机仓库文件或公开仓库源码（已克隆到会话 scratch，只读）。

## 1. CLIProxyAPI 上游

- 最新 Release：`v8.0.60`（2026-11-07，tag commit `91e0dee`）。
- v0 Management API 仍支持；新增 `/v8/management`（`docs/management-api-v8.md`）。
- 配置树：`/v8/management/config/api-keys/<kind>`；`PUT/PATCH` 支持 `If-Match`，冲突返回 **412**（`internal/api/handlers/management/config_v8.go`）。
- 插件 RPC schema：`sdk/pluginabi/types.go` 中 `SchemaVersion = 10`。版本节点：
  - 3 流式块省略请求体；4 WebSocket 观察；5 省略历史块；6 管理响应不转义；
  - 7 拦截器可省略请求体；8 原始字节补丁；9 多供应商模型 `registrations`；10 多 auth 入口 `identifiers`/`entries`。
- 插件管理路由前缀固定：`/v0/management` 与 `/v0/resource/plugins/<id>`（`internal/pluginhost/management.go`）。
- Quota 能力：`sdk/pluginapi/types.go` 的 `QuotaProvider` 接口 + `quota.identifier|describe|fetch|reset`；
  响应结构 `QuotaFetchResponse{subscription, summary, groups[{displayName,buckets[{window,remainingFraction,resetTime,description}]}], serverTimeOffsetMs}`。
  宿主按 `auth_index` 解析凭据并把 `storage_json/metadata/attributes/host` 交给插件（`internal/pluginhost/quota_provider.go`）。
- 插件列表对外字段（`internal/pluginhost/snapshot.go`）：`supports_oauth`、`oauth_providers`、`oauth_provider_entries`、`supports_quota`、`quota_provider`。

## 2. Management Center 前端

仓库：`router-for-me/Cli-Proxy-API-Management-Center`（main，2026-11-05）。

- `src/types/plugin.ts`：`PluginListEntry` 含 `supportsQuota`、`quotaProvider`、`oauthProviders`、`oauthProviderEntries`。
- `src/features/quota/providers/plugin/data.ts`：
  发 `POST /plugins/<id>/quota {auth_index}`，期望 `{subscription, summary, groups}`；
  `summary` 项为 `{key,label,value,unit?,format?:'number'|'currency',currency?}`。
- `src/features/quota/providers/plugin/PluginQuotaBody.tsx`：`groups[].buckets[]` 按 `remainingFraction` 画进度条，`resetTime` 直接展示；无 plan/summary/group 时显示空态。
- `src/features/plugins/PluginResourcePage.tsx`：插件页面以 **iframe** 挂在 `/plugin-pages/<pluginId>/<menuIndex>`，`src` 为插件声明的资源路径。
- `tests/pluginOAuthEntries.test.ts`：入口名称/图标会被后端 `html.EscapeString`，前端解一次实体；我们返回的 `entries[].name/logo` 需是纯文本。
- `tests/pluginQuota.test.ts`：内置供应商额度卡优先；插件额度只用于没有内置卡的 provider（`quota_provider` 不匹配内置）。

结论：本插件要出现在新前端，必须
1) 返回 `supports_quota/quota_provider`；
2) 实现 `quota.fetch` 且按 `auth_index` 归属；
3) 有真实登录流程的 provider 才声明 `supports_oauth`，避免空卡片；
4) 资源页仍是 iframe，页面需自包含（单文件构建）并跟随主题背景。

## 3. magpie（MIT）

仓库：`yetone/magpie`（MIT，2026）。关键文件：

- `internal/provider/provider.go`：供应商 = 本地配置文件里的记录（键、多个 KeyAccount、每个协议的 Base URL、Routing/Affinity 等）。
- `internal/provider/account.go`：订阅账号（Codex/Copilot/Claude）从 agent 自己的凭据读取；`Account` 带 `sign/renew/models/fetch/retry/unusable` 等钩子。
- `internal/provider/plugins.go` + `internal/plugin/providers.go`：插件型供应商 `Provider{ID,Spec,Name,NPM,API,Methods,Accounts,...}`。
- `internal/plugin/usage.go`/`plugin_usage.go`：额度窗口 `UsageWindow` 可为 `used` 缺省（null≠0）、可带 `balance`（钱包）、`aside`（不参与路由）。
- `internal/plugin/host.js`（3546 行）：Bun 下运行 OpenCode 风格 npm 插件，stdin/stdout JSON-line RPC；插件得到 `PluginInput`（auth.set、config.get、fetch、Bun.$）。
- 社区插件样例 `magpie-community/plugins/packages/cline`：`package.json` 声明 `main`/`exports`，`auth` 钩子做 WorkOS 设备码登录，`provider.models` 出模型，`auth.usage` 出余额与 5h/周/月窗口。
- 额度语义（`docs/subsystems/provider-plugins.md`）：`used` 缺省不是 0；有 `amount/limit` 才换算比例；钱包只给 `balance`；`aside` 不参与路由。

结论（本计划采用的可借鉴点，不复制代码）：
- 统一 `QuotaWindow{used?, limit?, balance?, reset, aside}` 语义，未知保持 null。
- 供应商 = 定义 + 账号 + 每账号的额度/模型/登录钩子；与我们的 `provider → account` 模型同构。
- 插件兼容按“清单导入（必做）+ 钩子执行（可选，Bun sidecar）”分层，`fetch` 不接管路由。

## 4. 现有仓库基线

- Go：101 个测试文件 / 约 1105 个测试点；`internal/manager` 239 个 Go 文件。
- 前端：50 个测试文件 / 约 450 个测试点；`App.tsx` 1799 行、`types.ts` 1951 行、`styles.css` 3317 行。
- 三个供应商页面：`CodexWorkspace.tsx` 800 行、`OpenCodeWorkspace.tsx` 1266 行、`ClinePassWorkspace.tsx` 1149 行，各自维护加载/错误/操作/额度渲染。
- 供应商数据现状（互不一致）：
  - Codex：`AccountUsageSnapshot.codex{five_hour,seven_day}`（百分比 + reset + 透支字段），plan 在 `QuotaMetadataResponse.plan_type`。
  - OpenCode Go：`OpenCodeQuotaResult{rolling,weekly,monthly}`，每窗口 `usage_percent/percent_remaining/reset_at`；Go 计费 `$10/月` + 20%/50%/100% 三窗（`OpenCodeBillingMode`）。
  - OpenCode Zen：余额制（`OpenCodePricingSnapshot.zen` + 钱包），无窗口。
  - Cline Pass：`QuotaUsage`（三窗参考价）+ 余额 + `quota_limited_until` + 自动修复状态。
  - AI Provider 渠道：`AIProviderRuntimeSnapshot` 用量/成本，无账号级额度窗口。
- 管理 API 写入现状：`provider_channel_bind.go` 直接 `PUT /v0/management/<kind>`，无 ETag、无 v8。

## 5. 差距清单（S1/S2 要消灭的）

| # | 差距 | 影响 | 解决 sprint |
| --- | --- | --- | --- |
| G1 | 插件 schema 只声明到 2 | 新前端拿不到 quota 能力；多入口/多供应商模型不可用 | S1 |
| G2 | 未实现 `quota.*` | 新前端额度页插件卡为空 | S1/S2 |
| G3 | 渠道写入只有 v0 | v8 的 If-Match/配置树未用，易覆盖他人字段 | S1 |
| G4 | 三套账号/额度模型 | 页面重复、逻辑分叉、未知值显示 0 | S2 |
| G5 | 无统一账号动作契约 | Cline/OpenCode/Codex 操作不一致 | S2 |
| G6 | 12 个顶层视图、3317 行全局 CSS | 信息架构混乱、风格不统一 | S3–S5 |
| G7 | 无 magpie 兼容 | 无法接入社区供应商 | S6 |

## 6. 复现命令

```bash
git clone --depth 1 https://github.com/router-for-me/CLIProxyAPI.git
git clone --depth 1 https://github.com/yetone/magpie.git
curl -s https://raw.githubusercontent.com/router-for-me/Cli-Proxy-API-Management-Center/main/src/types/plugin.ts
curl -s https://raw.githubusercontent.com/magpie-community/plugins/main/packages/cline/package.json
```

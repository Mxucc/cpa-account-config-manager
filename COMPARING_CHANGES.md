# Comparing changes

## 合并目标

- 目标主线：`main`
- 当前主线提交：`5df76d6`（`v0.3.1372`）
- 共同基线：`5e3c826`（`v0.3.1352`）
- 检查日期：2026-09-02

当前 `main` 已包含 CPA 图标、`file://` 本地入口修复、代理策略、AI 提供商、用量统计、并发限制等后续发布内容。`session-0827-01` 和 `session-0827-02` 都从共同基线分叉，尚未合并回 `main`。

## 分支比较

| 分支 | 相对 `main` 的变化 | 独有提交 | 说明 |
| --- | --- | --- | --- |
| `session-0827-01` | 26 个文件，新增 7,900 行、删除 2,599 行 | `2105340`、`698ad9e` | 可配置用量限制与第一版可观测性控制台 |
| `session-0827-02` | 32 个文件，新增 8,026 行、删除 2,659 行 | `2105340`、`698ad9e`、`567757a`、`7e44dc2` | 在 `session-0827-01` 基础上修复可观测性 UI，并区分账号与供应商用量限制 |

### `session-0827-01` 的主要改动

- 新增持久化的账号百分比限制、美元额度限制和按模型限制。
- 增加账号与 AI 供应商的运行时请求限制。
- 增加 `UsageLimitsSettings` 配置界面和对应测试。
- 重做仪表盘和用量指标展示。
- 扩展 API 客户端、类型定义、国际化文案和样式。

### `session-0827-02` 的额外改动

- 修复并恢复可观测性控制台 UI。
- 将账号用量限制与 AI 供应商用量限制分离处理。
- 补充批量编辑、账号配置、任务、预览和请求拦截链路中的限制字段。
- 增加 AI 供应商页面、批量编辑和仪表盘相关测试覆盖。
- 相比 `session-0827-01`，该分支包含更完整的最终修复，建议优先评估该分支。

## 推荐合并策略

### 推荐方案：先合并 `session-0827-02`

```bash
git fetch origin
git switch main
git pull --ff-only origin main
git merge --no-ff origin/session-0827-02
```

由于目标主线在共同基线之后又增加了大量功能，预计以下区域可能产生冲突，需要逐项人工确认：

- `internal/manager/app.go`
- `internal/manager/request_hook.go`
- `internal/manager/account_config.go`
- `internal/manager/accounts.go`
- `internal/manager/jobs.go`
- `internal/manager/patch.go`
- `internal/manager/preview.go`
- `web/src/App.tsx`
- `web/src/api/client.ts`
- `web/src/components/OtherSettingsWorkspace.tsx`
- `web/src/components/DashboardWorkspace.tsx`
- `web/src/styles.css`
- `web/src/types.ts`
- `web/src/i18n/uiCatalog*.ts`
- `internal/web/dist/index.html`

解决冲突时应保留 `main` 已有的后续功能，尤其是：

- `v0.3.1372` 的 `cpama.svg` 图标注册和页面品牌图标。
- `web/index.html` 的 `file://` 源码入口回退逻辑。
- 15 秒与 60 秒双窗口并发限制。
- Sub2API 兼容计费默认开启及 AI 供应商用量持久化。
- 代理档案、自动策略、Codex 身份策略和现有测试修复。

## 不建议直接合并的方案

不建议先合并 `session-0827-01`，再合并 `session-0827-02`。两个分支共享大量同一批文件，且 `session-0827-02` 已包含 `session-0827-01` 的核心提交及后续修复，这样做会增加重复冲突和重复提交。

也不建议直接使用 `git rebase main` 改写共享分支历史。若需要将改动拆分到多个 PR，应从 `session-0827-02` 建立新的临时分支，再按功能拆分提交。

## 合并后的验证清单

```bash
make verify VERSION=<next-version>
git diff --check
git status --short
```

前端至少应确认以下测试通过：

- `UsageLimitsSettings`
- `AIProvidersSettings`
- `BatchEditor`
- `AccountUsageCell`
- `dashboardMetrics`
- `OtherSettingsWorkspace`

合并后还应确认：

- 账号和 AI 供应商页面仍能加载。
- 账号编辑不会强制刷新整个列表。
- 自动策略和巡检不会因页面访问重复启动。
- 并发限制在 15 秒和 60 秒窗口同时生效。
- 现有 Logo、构建产物和 `file://` 入口没有回归。

## 结论

如果目标是把旧会话中的“可观测性控制台 + 用量限制”带回当前主线，优先比较并合并 `session-0827-02`。它是两个候选分支中较新的修复版本，但不应直接覆盖 `main`；应以当前 `main` 为基础逐项解决冲突，并在合并后重新运行完整验证。

# CPA Account Config Manager

[English documentation](README_EN.md)

`cpa-account-config-manager` 是一个
[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 原生插件，用于在同一个经过鉴权的工作区中管理 CPA 账号。插件提供账号增删改查、批量操作、导入导出转换、额度展示、模型测试、巡检自动化、条件策略、外部通知和操作日志，同时避免把原始凭据暴露给浏览器。

## 功能说明

| 功能 | 说明 |
| --- | --- |
| 批量账号管理 | 集中完成账号搜索、筛选、查看、添加、编辑、启用、禁用、删除和去重；可对已选账号或全部筛选结果批量修改，支持预览、Revision 冲突检查、有界并发、逐账号结果和仅重试失败项。 |
| 全格式兼容导入 | 支持粘贴 JSON 文本，以及混合上传 JSON、JSON Lines、TXT 和 ZIP；ZIP 可包含多个文件，单次最多处理 10,000 个账号。可识别 CPA、sub2api、Codex Auth、Agent Identity、PAT、Claude、Gemini，以及 Cockpit、9router、AxonHub、Codex Manager 等常见账号 JSON 结构。 |
| 全格式输出 | 可导出 CPA、sub2api、Cockpit、9router、Codex、AxonHub 和 Codex Manager；无法用单文件容纳多账号的目标格式会自动打包为 ZIP。批量结果和操作报告可另行导出为 JSON、CSV 或 JSON Lines。 |
| 配置预设 | 基础策略可为以后导入或新发现的账号设置默认 Priority、WebSockets、模型探测和模型策略；条件策略可按提供方、套餐、账号类型和邮箱后缀进一步覆盖。 |
| 账号池用量 | 提供接近 sub2api 的账号利用率视图，集中展示请求成功/失败次数、Token 总量、Codex 5 小时和 7 天额度、恢复时间、套餐及主动重置次数，便于判断号池容量。 |
| 模型测试 | 通过 CPA 对指定账号执行真实模型测试，展示 HTTP 状态、延迟、回退模型和脱敏后的上游响应；账号白名单与黑名单会自动约束手动测试、自动探测和巡检模型。 |
| 账号巡检 | 结合 CPA 原生状态、Usage 记录、主动模型测试和被动失败进行全量巡检，实时展示进度、健康证据、恢复时间和建议操作，并支持批量执行建议。 |
| 自动处置 | 可选自动禁用失败或额度耗尽账号、在恢复后启用由巡检自身禁用的账号，并对严格符合条件且经过风险确认和宽限期的账号执行自动删除。 |
| 插件与 CPA 版本检测 | 从 CPA 插件商店检查并安装插件更新，同时展示当前 CPA 服务端版本和可用的新版本。CPA 主程序只做版本检测，插件不会自行替换 CPA 可执行文件。 |
| Codex 5h / 7d 额度透支 | 实验性额度透支功能会利用最后一条消息为 tool call 时可继续生成的行为；5 小时或 7 天额度耗尽后，自动禁用前最多进行 5 次可用性验证，任意一次成功就保留账号。 |
| Agent Identity 与 PAT | 实验性支持 Codex Agent Identity 和 Personal Access Token 的导入、转换、登录和 CPA 原生插件鉴权路径，并兼容常见 sub2api 结构。 |
| 用量与异常通知 | 使用可预览、可测试的 HTTPS GET URL 模板发送通知，可对接 Bark、ntfy 等服务；变量可组合账号总数、可用账号数、可用率、异常占比、额度受限数量和触发时间。 |
| 额度重置 | 自动读取 Codex 套餐和主动重置次数；账号存在重置次数时，可在二次确认后消耗一次重置并立即刷新套餐、额度和剩余次数。 |
| 条件策略 | 支持多个带优先级的规则，并使用嵌套 `all`/`any` 条件匹配提供方、账号类型、套餐类型和邮箱后缀；可配置 Priority、WebSockets、新账号模型探测及全部/白名单/黑名单模型策略。 |
| 代理档案与策略代理 | 在自动策略中为账号和 AI 供应商设置默认代理，并可在条件策略中按提供方、套餐或邮箱后缀覆盖；代理档案支持启用/停用、批量编辑引用、脱敏展示和安全持久化。该功能解决 [issue #3](https://github.com/Mxucc/cpa-account-config-manager/issues/3)。 |
| 条件通知 | 每个通知策略拥有独立名称、嵌套匹配条件、全部满足/任意满足逻辑、低可用账号数和低可用率阈值，以及一个或多个有序通知地址。 |
| 模型路由策略 | 单账号和批量编辑均支持全部模型、白名单和黑名单模式；新账号探测可识别只支持部分 Codex 模型的账号并应用兼容白名单。 |
| 操作日志 | 对账号修改、导入导出、模型测试、策略扫描、巡检处置、通知和更新操作进行持久化脱敏审计，记录真实原因、结果、失败分类和安全账号样本。 |
| OpenCode Go 额度监控 | 参考社区插件整合 OpenCode Go 多账号额度监控，入口并入「AI 提供商」视图：绑定 Workspace ID 与 auth Cookie 后，抓取 opencode.ai 工作区仪表盘，展示 5 小时 / 7 天 / 30 天使用率与重置时间，支持配额测试、手动刷新与单账号删除。 |
| OpenCode Zen 与 opencode-cc 桥接 | 参照 [Kiowx/opencode-cc](https://github.com/Kiowx/opencode-cc) 的多协议桥接方案，在「AI 提供商」视图新增 OpenCode Zen 渠道：统一管理 Zen API Key（密钥脱敏、留空保留），指向 Zen 网关（https://opencode.ai/zen）或自建 opencode-cc 桥接地址，入库即探测连通性，支持单账号重新测试、编辑与删除。 |
| OpenCode Zen 与 opencode-cc 桥接 | 参照 [Kiowx/opencode-cc](https://github.com/Kiowx/opencode-cc) 的多协议桥接方案，在「AI 提供商」视图新增 OpenCode Zen 渠道：统一管理 Zen API Key（密钥脱敏、留空保留）、指向 Zen 网关（https://opencode.ai/zen）或自建 opencode-cc 桥接地址，入库即探测连通性，支持单账号重新测试、编辑与删除。 |
| 多语言与主题 | 跟随 CPA Management Center 的语言和主题，支持 English、简体中文、繁体中文和俄语，不维护容易与宿主脱节的独立语言设置。 |

## 账号导入与导出

导入支持粘贴 JSON 文本，也支持从混合的 JSON、JSON Lines、文本和 ZIP 文件中一次导入最多 10,000 个账号。ZIP 中可以包含多个受支持文件。每次导入都会先生成预览，执行数量和体积限制及重复检查，然后仅通过 CPA Auth 回调在可取消的后台任务中写入；不会覆盖现有 Auth 文件。

支持的输入包括 CPA 原生文件、常见 sub2api 账号集合、Codex OAuth/PAT 和 Agent Identity 变体、Claude、Gemini，以及其他能够转换为 CPA Auth 文档的格式。Agent Identity 转换仍需在“实验性功能”中手动开启，因为它依赖上游鉴权行为。

导出目标支持 CPA、sub2api、Cockpit、9router、Codex、AxonHub 和 Codex Manager。无法用单个文件表示多账号的格式会下载为 ZIP，其中每个账号使用独立文件。操作报告可导出为 JSON、CSV 或 JSON Lines，且绝不包含凭据。

## 巡检与自动化

巡检结合三类证据：

1. CPA 账号状态、近期请求和 Usage 记录。
2. 通过 CPA 执行的定时或手动主动模型探测。
3. CPA 提供服务时观察到的被动失败。

自动操作默认关闭。自动启用不会接管由操作员或其他系统禁用的账号。自动删除需要单独确认风险，并且只适用于具有当前强证据的文件型账号。巡检状态和操作日志不会持久化原始上游响应正文。

默认策略采用增量处理。每个稳定账号只处理一次，处理身份会跨插件更新和 CPA 重启持久化。定时扫描仍会发现新账号，但会跳过已经处理过的账号。Codex 套餐和主动重置次数会在条件规则判定前刷新。额度接口返回 HTTP 401 时，会转换为脱敏的凭据失效巡检证据，而不会错误增加策略写入失败数量。

条件策略支持使用提供方、账号类型、套餐类型和邮箱后缀组成嵌套的 `all`/`any` 条件。规则具有明确优先级，可以设置账号 Priority、WebSockets、新账号模型探测和模型策略。基础策略先执行，条件规则随后只覆盖自身管理的操作。

外部通知支持多个由操作员配置的 HTTPS GET 模板。模板可以组合白名单内的账号和健康变量，支持使用当前值预览及发送测试。通知结果和受限的 HTTP 元数据会写入操作日志。每个通知地址既可以使用通用触发，也可以绑定一个有序的通知策略。策略通知复用嵌套的提供方、账号类型和邮箱后缀条件，并应用独立的低账号数量与低可用率阈值。

## 安装

推荐从 CPA 插件商店安装，由 CPA 选择对应平台压缩包、校验 Checksum，并明确报告是否需要重启宿主。GitHub Release 也提供以下平台的手动安装包：

| 平台 | 架构 | 动态库 |
| --- | --- | --- |
| Linux | amd64 | `.so` |
| Linux | arm64 | `.so` |
| macOS | arm64 | `.dylib` |
| Windows | amd64 | `.dll` |

手动安装时，请先校验对应的 `.sha256` 文件，再将动态库解压到 CPA 的平台插件目录，并在 `config.yaml` 中启用：

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    cpa-account-config-manager:
      enabled: true
      priority: 20
```

CPA 加载插件后，在 Management Center 中打开 **CPA-A Manager**。大多数通过 CPA 插件商店完成的更新只需刷新页面；仅当宿主返回 `restart_required: true`，或已加载的动态库被系统锁定时，才需要重启 CPA。

## 配置与持久化

界面会把支持的设置持久化到 CPA 插件配置中。部署层仍可使用以下可选配置字段：

| 字段 | 默认值 | 用途 |
| --- | --- | --- |
| `workers` | `6` | 账号修改并发数，限制在 1-16。 |
| `data_dir` | `data/cpa-account-config-manager` | Usage、巡检、策略、更新、运行时所有权、任务和日志等私有状态目录。 |
| `management_base_url` | `http://127.0.0.1:8317` | 插件执行鉴权操作时使用的 CPA Management API 回环地址。 |

如果 CPA 运行在会被替换的容器中，请持久化挂载 `data_dir`。没有显式配置数据目录时，插件可以把脱敏 Usage 状态镜像到常见的本地 Auth 目录，但显式的持久化挂载更可预测。CPA 进程需要对 Auth 目录和实际插件数据目录具有读写权限。

“实验性功能”目前包括：

- Codex 5 小时与 7 天额度透支探测，依赖上游的 tool-call 续写行为。
- Codex Agent Identity/PAT 转换和鉴权 Hook。

两项功能默认关闭，并通过稳定 Hook 与标准账号管理流程隔离，因此以后可以单独移除实验实现，而无需修改常规路径。

## 安全模型

- 所有特权接口都是固定路径且经过鉴权的 CPA Management 路由。
- 公共 Resource 路由只提供内嵌静态界面。
- Management Key 仅存在于当前浏览器与 CPA 请求链路中，插件不会持久化。
- 原始 Auth JSON、Token、Cookie、代理凭据、Header 值和原始上游响应不会进入公开模型、日志或持久化状态。
- 导入和导出都限制账号数量和数据体积；ZIP 条目会检查路径穿越和解压膨胀。
- 账号修改使用预览、物理 Revision、共享写入锁和冲突检查；破坏性操作需要明确确认。
- 在平台支持的情况下，私有目录和文件使用限制性权限。

## 兼容性

插件使用 CLIProxyAPI 原生插件 ABI/Schema v1，需要 CPA 支持原生插件发现、Auth list/get/save 回调、Usage Plugin 回调，以及用于 Auth 状态、字段编辑、指定账号 API 调用、删除和插件商店更新的当前鉴权 Management API。

插件不导入 CLIProxyAPI Go 包，也不会修改 CPA 可执行文件。

## 开发

环境要求：Go 1.24+、Node.js 20+、npm、`make`，以及适用于 CGO 的 C 工具链。

```bash
make verify
make build
make package VERSION=X.Y.Z
```

`make verify` 会格式化并测试 Go 代码、测试和构建 React 界面、检查内嵌资源并验证发行元数据。Release 使用 `vX.Y.Z` annotated tag；发布工作流会构建四个平台压缩包、四个对应的 `.sha256` 文件和汇总的 `checksums.txt`。

## 致谢

- 巡检设计与处置流程：
  [seakee/CPA-Manager-Plus](https://github.com/seakee/CPA-Manager-Plus)
- 原生巡检与任务模式：
  [ywddd/grok-inspection](https://github.com/ywddd/grok-inspection)
- Codex 失败和额度展示：
  [ysxk/codex-429-autoban](https://github.com/ysxk/codex-429-autoban) 与
  [zhumengling/codex-token-usage](https://github.com/zhumengling/codex-token-usage)
- Agent Identity 导入与登录思路：
  [catoncat/codex-agent-identity-web](https://github.com/catoncat/codex-agent-identity-web)
- OpenCode Go 额度监控：
  [zcyoop/opencode-go-quota-cpa-plugin](https://cnb.cool/zcyoop/opencode-go-quota-cpa-plugin)
- OpenCode Zen 与多协议桥接：
  [Kiowx/opencode-cc](https://github.com/Kiowx/opencode-cc)
- 社区友链：[LINUX DO](https://linux.do/)

以上项目为产品行为提供了参考。除非仓库许可证历史中另有明确说明，本插件没有复制这些项目的代码。

# CPA Account Config Manager

[English documentation](README_EN.md)

`cpa-account-config-manager` 是一个
[CLIProxyAPI（CPA）](https://github.com/router-for-me/CLIProxyAPI) 原生插件，用于在 CPA Management Center 中统一管理账号、AI 提供商、用量、路由策略和自动化任务。插件把批量配置、格式转换、模型探测、额度与成本统计、请求风控、巡检处置、代理、通知和审计日志集中到一个经过 CPA 鉴权的界面中，并对浏览器和日志隐藏原始凭据。

## 核心能力

### 仪表盘与账号池

- 汇总账号与 AI 提供商数量、启用/禁用状态、健康与异常账号、活跃请求、Token、成本、巡检结果、待处理操作和模型成本排行。
- 账号列表支持搜索、筛选、持久化排序，以及 20、50、100、200、500、1000 条分页。
- 支持查看、添加、编辑、启用、禁用、删除、批量修改和结果重试；单账号编辑会加载当前配置，批处理支持预览、Revision 冲突检查、有界并发和逐账号结果。
- 支持账号去重：邮箱优先、账号 ID 辅助，并可忽略 ID 或排除 Team/K12 团队账号，避免团队共享 ID 被误删。
- 支持 CPA 原生 Token 刷新；CPA 未提供刷新接口时，可对持有有效 Refresh Token 的兼容账号执行完整刷新流程。
- 展示账号初始时间、禁用时间、Priority、WebSockets、备注、路由前缀、Header、代理、模型、并发和额度策略。

### 导入、导出与格式转换

导入入口支持粘贴 JSON，以及混合上传 JSON、JSON Lines、TXT 和 ZIP；单次最多处理 10,000 个账号，并提供预览、重复检查、后台进度和取消任务。ZIP 会检查路径穿越和异常解压膨胀，导入不会覆盖已有 Auth 文件。

可识别的常见来源包括：

- CPA 原生 Auth、Sub2API 集合、Codex OAuth、Codex PAT、Agent Identity。
- Claude/Anthropic、Kimi、Qwen、xAI/Grok、Gemini、Gemini CLI、Vertex 服务账号。
- Cockpit、9Router、AxonHub、Codex Manager，以及其他可归一化为 CPA Auth 的常见 JSON 结构。

可导出为 CPA、Sub2API、Cockpit、9Router、Codex、AxonHub 和 Codex Manager。目标格式无法用一个文件表达多账号时会自动生成 ZIP；批量任务和操作结果还可导出为 JSON、CSV 或 JSON Lines，公开结果不包含凭据。

### 用量、成本、并发与额度

- 持久化账号和 AI 提供商的成功/失败请求、Token、活跃并发、滚动请求窗口和累计成本；AI 提供商更新或 CPA 重启后仍保留历史统计，并通过脱敏凭据指纹和别名避免 auth-index 变化造成用量归零。
- 插件自有的额度、并发、路由、代理、风控、审计、巡检、更新和实验配置均使用私有原子存储；隐式数据目录会跟随 CPA Auth 目录保存，且不会写入 API Key、OAuth Token、Auth JSON、Cookie、Header、请求体或代理凭据。
- 展示 Codex 5 小时/7 天官方额度、恢复时间、套餐和主动重置次数。套餐识别优先读取 `id_token` 内部信息，再降级到 CPA info 和外层类型字段。
- 新 Codex 账号会采集套餐与主动重置次数；手动刷新会同时更新套餐和次数，有剩余次数时可在二次确认后执行额度重置，非 Codex 账号显示 `-`。
- Sub2API 兼容额度计费已作为常驻能力默认启用：后台异步同步模型价格并按成功请求估算 USD 成本，同时保留原始 Token 统计，对极小金额使用足够精度展示。
- 账号和支持的 AI 提供商均可显示实时并发，并独立配置 15 秒与 60 秒滚动窗口限制；`0` 或留空表示不限制，两个窗口同时生效。
- 账号额度策略使用 CPA 返回的 5h/7d 官方百分比；AI 提供商可先配置 5h/7d USD 预算额度，再使用百分比阈值限制。
- AI 提供商模型编辑支持“快速获取模型”：使用当前频道凭据读取供应商模型目录，自动合并新模型并保留已有别名、显示名和映射选项。

### 风控中心

- 参考 Sub2API 的内容风控工作流，在 CPA 请求转换链最前端提供插件原生检查；支持关闭、仅观察（`observe`）和路由前阻断（`pre_block`）模式。
- 支持本地阻断关键词，以及全部模型、指定模型和排除模型三种过滤方式；可配置阻断 HTTP 状态、公开错误消息、事件保留天数和最大事件数。
- 可选的 SHA-256 输入 hash 记忆会复用已确认的风险结果，从而在关键词被调整后继续识别完全相同的规范化输入；内存与持久化加载均限制为最多 4,096 个 hash。
- 风控中心展示观察、阻断、关键词命中、hash 命中和脱敏事件，可独立清空事件或 hash 记忆。
- 风控存储和管理 API 不保存 prompt 原文、摘录、请求头、Token、Cookie、API Key 或代理凭据。账号标识经过 SHA-256 伪名化，命中规则仅保存不可逆的 `kw:` 引用。
- 风控中心内置内容审核、提示词审计和自定义审核三个模块；外部审核仅保存 endpoint、模型、扫描器、队列/超时和凭据环境变量名，支持 fail-open/fail-closed。
- 账号额度限制严格读取 CPA 已采集的 Codex 5 小时/7 天官方用量百分比；每个窗口可单独设置阈值，留空表示不限制，未采集官方用量时不会伪造额度或拦截请求。

### 模型测试、路由与 Codex 身份

- 从账号或 AI 提供商读取模型目录，并通过对应账号或 Provider Base URL 发起真实测试。
- 测试结果展示模型、HTTP 状态、延迟和脱敏后的上游响应；支持主模型、回退模型和兼容模型，成功的 `200` 完成响应会被正确识别。
- 手动测试会持久化最后一次模型和历史测试模型；白名单账号会优先加载白名单模型。
- 模型策略支持全部模型、白名单和黑名单。手动测试、自动探测与巡检都会遵守策略；新 Codex 账号可自动识别受限兼容模型并应用白名单。自动兼容白名单属于常驻能力，不需要实验开关。
- Codex 客户端身份策略是「其他配置 → 实验性功能」中的唯一全局配置，支持出站身份收敛、官方客户端入口门、App Server 放行、最低/最高版本、白名单/黑名单、引擎指纹信号，以及关闭、设备级、会话级和完全收敛模式。
- 官方客户端入口门只能由该全局开关开启：账号或 AI 提供商的策略只能豁免单个对象，不能单独开启，因此不会在关闭总开关后继续拦截请求；被拦截时会返回来源标记（`source`、`reason`）便于区分插件拦截与上游限制。
- 出站收敛与入口门是相互独立的：只开启收敛不会拒绝任何请求。Codex OAuth、`codex-api-key` 健康检查及内部模型、额度、Token、PAT、Agent Identity 探测使用一致的兼容身份。

### 巡检、自动处置与策略

- 巡检结合 CPA 原生状态、近期请求、Usage、主动模型探测和运行期间观察到的被动失败，支持原生快速扫描、完整巡检、增量巡检、指定账号复检、待复核重试、实时进度和停止任务。
- 结果区分健康、异常、认证失败、额度受限和待复核状态。HTTP 401 会被记录为凭据失效证据，建议重新登录或删除，并可直接自动禁用。
- 可按证据自动禁用、在额度刷新或恢复时间到达后自动启用，并可提高刚刷新额度账号的 Priority。自动启用只接管由巡检自身禁用的账号，不修改人工禁用状态。
- 自动删除具有独立风险确认、宽限期、强证据和文件型账号限制；所有自动禁用、启用和删除都会在操作日志中记录原因。
- 策略执行顺序为全局策略、新账号默认策略、条件策略。默认策略只处理新账号或内容已变化账号，已处理指纹会持久化，稳定账号不会因打开页面或重启而重复扫描。
- 条件策略支持多规则、优先级和嵌套 `all`/`any`，可按提供方、账号类型/套餐和邮箱后缀匹配。
- 策略动作包括启用/禁用、Priority、15 秒/60 秒并发、5h/7d 额度策略、备注、前缀、Header、WebSockets、账号/AI 提供商代理档案、模型探测、全部/白名单/黑名单模型策略和 Codex 身份策略。长任务在后台异步运行，不阻塞保存界面。

### 代理与外部通知

- 代理档案支持添加、编辑、删除、启用/停用和多个档案管理；代理凭据只脱敏展示，不会把已保存密钥回填浏览器。
- 账号与 AI 提供商可分别引用代理档案，并能在全局策略、默认策略、条件策略和批量编辑中覆盖。此能力解决了 [issue #3](https://github.com/Mxucc/cpa-account-config-manager/issues/3)。
- 外部通知支持多个 HTTPS GET 地址，可对接 Bark、ntfy 等通用接口；模板变量可预览并发送测试，测试结果会展示实际 URL、HTTP 状态、尝试次数和具体变量值，百分比变量自带 `%`。
- 通用通知和策略通知相互独立。策略通知具有唯一名称、顺序、一个或多个通知地址、嵌套 `all`/`any` 匹配条件，以及可用账号数和可用率阈值；指定策略后不再受通用通知触发规则控制。

### AI 提供商

AI 提供商是独立工作区，当前可管理：

- OpenAI-compatible、Gemini API Key、Interactions API Key、Claude API Key、Codex API Key、xAI API Key、Vertex API Key 和 CPA 通用 API Key 渠道。
- OpenCode Go。
- OpenCode Zen，以及通过自定义 Base URL 接入的自建 `opencode-cc`；OpenCode Zen 未填写 Base URL 时默认使用 `https://opencode.ai/zen`。

支持提供商名称、状态、模型数量、并发用量、Base URL、API Key、模型映射、Priority、Weight、前缀、Header、代理和渠道专用选项。CPA 渠道条目本身没有名称字段，因此名称、该渠道的用量身份等由插件保存为「渠道记录」：键为渠道 Base URL 与 API Key 的加盐不可逆摘要，每次读取都会用实时渠道数据重新校验并回写；仅 Base URL 或仅 Key 相同的渠道不会被认错。换 Key 后会按新摘要重新读取，若该 Base URL 在同类渠道中唯一，则自动继承旧记录（名称与历史用量身份），因此历史用量仍能对上；Base URL 重复时不做继承，避免两个渠道互相认领。API Key 始终脱敏，编辑时留空会保留原值。每行的「更多」菜单集中提供重置本地用量与删除渠道（删除为最后一项）；支持查看、测试、编辑、启用、禁用、删除、模型目录、真实模型测试、Token/成本统计、15 秒/60 秒并发、5h/7d 自定义预算、代理档案和 Codex 身份策略；无法由当前 CPA 修改的能力会显示兼容提示，而不是伪造可用状态。

OpenCode Go 还支持 Workspace ID 与 auth Cookie、5h/7d/30d 配额、重置时间、手动刷新和删除（配置与刷新在鉴权界面中进行；独立的 OpenCode 状态页无需鉴权，因此只读取缓存状态、掩码工作区 ID，且不接收任何凭据）。

### OpenCode

侧边菜单在「AI 提供商」之后新增独立的 **OpenCode** 工作区：

- 以 Workspace ID 和 auth Cookie（用于抓取额度）添加 OpenCode Go 工作区，并可选择保存 OpenCode Go API Key；每个工作区展示已保存的 5h/7d/30d 配额。
- 以名称、Base URL（默认 `https://opencode.ai/zen`）和 API Key 添加 OpenCode Zen 凭据。
- “加载模型”会从 OpenAI 兼容端点 `GET {base}/v1/models` 获取模型列表，请求携带 `x-opencode-client: cli` 请求头和 `opencode/<version>` User-Agent，并按账号缓存结果。
- 模型测试会使用所选模型发起真实 `POST {base}/v1/chat/completions` 探测，返回状态、原因码、HTTP 状态和延迟；原因码包括 `authentication_failed`、`model_not_found`、`quota_limited` 和 `upstream_unavailable`。
- 一键绑定会 upsert 一个 `openai-compatibility` CPA 渠道，把模型发布到 CPA 路由：Base URL 为 `{base}/v1`，以已保存的 API Key 作为 key 条目并携带 OpenCode 请求头，同时把已验证的模型目录写入渠道的模型列表；重复绑定同一账号只会更新已有渠道、保留既有别名，不会创建重复渠道。
- 快捷入口提供 `https://opencode.ai/auth`、`https://opencode.ai/workspace`、`https://opencode.ai/zen` 和只读的 OpenCode 状态页。
- auth Cookie 和 API Key 只在经过鉴权的 Management 连接中写入一次，保存在插件私有数据目录；不会返回给浏览器（只有 `key_set` 布尔标记），也不会写入日志。
- 已内置 OpenCode 官方价格并自动同步：来源为 models.dev（为 OpenCode 维护的模型数据库），其中发布 `opencode`（Zen）与 `opencode-go`（Go）两个提供商，逐模型给出以「美元/百万 Token」计的输入、输出与缓存价格。插件每 24 小时用 ETag 条件请求重新校验该目录，在插件私有数据目录保留缓存副本，并内置官方快照，因此离线或首次同步前价格即可用；OpenCode 工作区展示目录（模型、输入、输出、缓存读取、缓存写入、上下文窗口与按上下文长度的价格档位），并给出来源、最后同步时间和“同步价格”操作。
- OpenCode 路由的用量改为按 OpenCode 自身价格计费，不再套用通用厂商价目表：当插件能按已记录的渠道 Base URL 将请求归属到某个 OpenCode 渠道时，就使用 Zen 或 Go 的目录。Go 是每月 $10 的订阅，因此其金额为所用量的标价价值。价格为公开数据，但相关路由仍需 Management Key。
- OpenCode Go 路由按会话派生会话 ID：OpenCode Go 要求客户端“Send a stable session ID in `x-opencode-session` for each conversation so we can optimize routing and prompt caching”（引自 https://opencode.ai/docs/go/#where-can-i-use-it）；插件为 OpenCode 模型按以下顺序取值：入站 `x-opencode-session` 原样保留；否则复用原生客户端会话请求头（可识别 Claude Code、Codex、ZCode、Pi 风格的请求头）；再取请求体中的会话 ID（`prompt_cache_key`、`session_id`、`conversation_id`）；最后回退为对会话前缀的加盐摘要，使同一会话在各轮次保持同一 ID，且任何消息正文都不会被发送。注入仅限 OpenCode 发布的模型 ID（Zen 与 Go 目录，以及各账号已加载的目录），不触碰其他模型。
- 工作区展示会话状态：启用/停用、覆盖的模型数量、已分配会话 ID 的请求数、观察到的不同会话数。会话 ID 永不写入日志，每个安装的盐以 0600 权限保存在插件数据目录中。
- 所有路由均为固定路径并要求 Management Key，位于 `/v0/management/plugins/cpa-account-config-manager` 下：`POST /opencode/models` 携带 `{kind: "go"|"zen", account_id}`，返回带模型列表的脱敏账号视图；`POST /opencode/model-test` 携带 `{kind, account_id, model, timeout_seconds?}`，返回状态、原因码、HTTP 状态、延迟、测试时间和详情；`POST /opencode/bind` 携带 `{kind, account_id}`，返回绑定信息（类型、Base URL、索引、是否新建、渠道 key）；`POST /opencode/accounts` 还接受 `{account_id, api_key}` 做仅更新密钥操作，`api_key` 为空时保留已保存的密钥，传入新密钥会使缓存的模型目录失效；`GET /opencode/pricing` 返回目录及其同步来源信息；`POST /opencode/pricing/refresh` 重新校验该目录并报告是否发生变化；`GET /opencode/session` 返回会话路由状态。

### 操作日志、界面与更新

- 操作日志覆盖导入、导出、批量修改、模型测试、策略扫描、巡检、自动处置、通知和插件更新，记录成功/失败/部分完成、失败依据、数量、脱敏样本、来源和时间。
- 界面支持简体中文、繁体中文、English 和 Русский，并跟随 CPA 语言与主题；另提供中性、靛蓝、森林、玫瑰主题，舒适/紧凑密度，小/中/大字号，以及主标题与描述字号区分。
- 表格排序、分页大小、筛选条件和手动测试模型会持久化。
- 可检查并从 CPA 插件商店安装插件更新，也会展示 CPA 当前版本和最新版本。插件只检测 CPA 主程序更新，不替换 CPA 可执行文件。

## 实验性功能

当前仍需手动开启的实验能力为：

- **Codex 5h / 7d 额度透支续用**：额度耗尽后最多探测 5 次，任意一次成功则保持启用，全部失败才自动禁用；以普通请求首次不可用时间冻结窗口基线，单独统计透支 Token 和成本，并在额度恢复后结束当前透支周期。该功能会修改 Codex 工具调用链，可能增加性能较低服务器的首字延迟。
- **Agent Identity 与 PAT**：提供相关格式的导入、转换、登录和 CPA 原生插件鉴权路径，并兼容常见 Sub2API 结构。

Sub2API 兼容成本计费、自动模型兼容白名单和 Codex 客户端身份策略均已是常驻功能，不在实验性开关中。

## 安装

推荐从 CPA 插件商店安装，CPA 会选择对应平台包、校验 checksum，并明确返回是否需要重启宿主。Release 同时提供：

| 平台 | 架构 | 动态库 |
| --- | --- | --- |
| Linux | amd64 | `.so` |
| Linux | arm64 | `.so` |
| macOS | arm64 | `.dylib` |
| Windows | amd64 | `.dll` |

手动安装时，请校验同名 `.sha256`，解压动态库到 CPA 插件目录，并在 `config.yaml` 中启用：

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    cpa-account-config-manager:
      enabled: true
      priority: 20
```

CPA 加载插件后，可从 Management Center 打开 **CPA-A Manager**。大多数插件商店更新只需刷新页面；只有 CPA 明确返回 `restart_required: true` 或动态库被宿主锁定时才需要完整重启。

## 配置与持久化

界面设置会写回 CPA 插件配置。部署层还可使用以下字段：

| 字段 | 默认值 | 用途 |
| --- | --- | --- |
| `workers` | `6` | 账号并发写入数，限制在 1-16。 |
| `data_dir` | `data/cpa-account-config-manager` | 用量、成本、巡检、策略、通知、更新、任务和日志的私有状态目录。 |
| `management_base_url` | `http://127.0.0.1:8317` | 插件访问 CPA Management API 的回环地址。 |

容器会被替换时应持久化 `data_dir`。未显式配置时，插件可在常见 Auth 目录旁保存脱敏用量状态，但显式挂载最可靠；CPA 进程需要对 Auth 目录和实际数据目录具有读写权限。

## 安全模型

- 所有特权操作都使用固定且经过 CPA 鉴权的 Management 路由；公共 Resource 路由只提供嵌入式静态 UI。
- Management Key 只存在于当前浏览器/CPA 请求链路，插件不会持久化。
- 原始 Auth JSON、Token、Cookie、API Key、代理凭据、Header 值和上游响应不会进入公开模型、日志或持久化状态。
- 导入导出具有数量和体积限制；ZIP 会检查路径穿越和异常展开。
- 账号写入使用预览、物理 Revision、共享写锁和冲突检查；删除、额度重置等破坏性操作需要明确确认。
- 私有目录和文件在平台支持时使用限制性权限。

## 兼容性

- 基础功能依赖 CLIProxyAPI native plugin ABI/schema v1，以及 Auth list/get/save、Usage Plugin 回调和当前鉴权 Management API。
- 实时并发与 15 秒/60 秒限制需要较新的 CPA 请求生命周期 Hook/native plugin schema v2；旧版 CPA 会显示“不支持/暂不可用”，不会假装执行限制。
- AI 提供商运行时和额度策略会对旧 CPA 缺少的 Management 路由做兼容降级，具体可编辑能力以当前 CPA 返回结果为准。
- 插件不导入 CPA 的 Go 包，不修改或补丁 CPA 二进制。

## 开发

需要 Go 1.24+、Node.js 20+、npm、`make` 和可用于 CGO 的 C 工具链。

```bash
make verify
make build
make package VERSION=X.Y.Z
```

`make verify` 会检查 Go 格式与测试、React 测试与构建、嵌入资源和发布元数据。Release tag 使用 `vX.Y.Z`；工作流为四个平台生成压缩包、对应 `.sha256` 和 `checksums.txt`。

## 致谢

- 巡检设计与处置流程：[seakee/CPA-Manager-Plus](https://github.com/seakee/CPA-Manager-Plus)
- 原生巡检与任务模式：[ywddd/grok-inspection](https://github.com/ywddd/grok-inspection)
- Codex 错误与额度展示：[ysxk/codex-429-autoban](https://github.com/ysxk/codex-429-autoban)、[zhumengling/codex-token-usage](https://github.com/zhumengling/codex-token-usage)
- Agent Identity 导入与登录思路：[catoncat/codex-agent-identity-web](https://github.com/catoncat/codex-agent-identity-web)
- OpenCode Go 额度监控：[zcyoop/opencode-go-quota-cpa-plugin](https://cnb.cool/zcyoop/opencode-go-quota-cpa-plugin)
- OpenCode Zen 与多协议桥接：[Kiowx/opencode-cc](https://github.com/Kiowx/opencode-cc)
- 社区链接：[LINUX DO](https://linux.do/)

这些项目提供了产品行为参考；除非仓库许可历史另有说明，本插件没有直接复制其代码。

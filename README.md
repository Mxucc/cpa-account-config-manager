# CPA Account Config Manager

[English documentation](README_EN.md)

`cpa-account-config-manager` 是一个
[CLIProxyAPI（CPA）](https://github.com/router-for-me/CLIProxyAPI) 原生插件，用于在 CPA Management Center 中统一管理账号、AI 提供商、用量、路由策略和自动化任务。插件把批量配置、格式转换、模型探测、额度与成本统计、请求风控、巡检处置、代理、通知和审计日志集中到一个经过 CPA 鉴权的界面中，并对浏览器和日志隐藏原始凭据。

## 核心能力

### 仪表盘与账号池

- 汇总账号与 AI 提供商数量、启用/禁用状态、健康与异常账号、活跃请求、Token、成本、巡检结果、待处理操作和模型成本排行。
- 「概览」新增**产品用量**拆分：按产品列出可归属的 Token、请求数与参考价金额。OpenCode 只计本插件自己的 OpenCode 渠道，Codex 汇总 Codex 凭据自身的用量与 codex-api-key 渠道，其余账号与渠道归入「其他提供商」。拆分只使用上方合计所用的那两个存储，因此各行相加恒等于总量；归属不了的流量仍留在总量里，并体现在「其他提供商」一行，而不是从拆分里消失。
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
- 自动重试（「其他配置」里的一项设置）：为 Codex 账号、OpenCode 渠道、Cline Pass 渠道的**每个凭据**写入宿主自己的重试预算——默认 5 次，`0` 表示不重试，最高 10 次。重试发生在宿主的请求循环内，客户端只会看到最终成功的那次响应，插件不新增任何按尝试的报错或日志。开启时还会把宿主的 `max-retry-interval` 提升到至少覆盖瞬时错误冷却（默认 60 秒，否则 5xx 无法被重试），已配置的非零宿主开关不会被调低。

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
- Codex 客户端身份策略在「Codex」页面中配置，支持出站身份收敛、官方客户端入口门、App Server 放行、最低/最高版本、白名单/黑名单、引擎指纹信号，以及关闭、设备级、会话级和完全收敛模式；两个 Codex 实验项（额度透支续用、Agent Identity / PAT）仍保留在「其他配置 → 实验性功能」中。
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
- 策略通知还支持**状态触发条件**，用于在账号掉授权、被禁用或持续报错时告警：按 `health`、`reason_code`、`status_code`、`disabled`、`disable_reason` 匹配巡检记录，并以「任一账号」「全部账号」「至少 N 个账号」控制触发范围，例如「全部账号被禁用」或「错误码 = 429」。状态条件与可用性阈值任一命中即触发通知，并新增 `${state_rule}`、`${matched_accounts}` 模板变量。此能力解决了 [issue #5](https://github.com/Mxucc/cpa-account-config-manager/issues/5)。

### AI 提供商

AI 提供商是独立工作区，当前可管理：

- OpenAI-compatible、Gemini API Key、Interactions API Key、Claude API Key、Codex API Key、xAI API Key、Vertex API Key 和 CPA 通用 API Key 渠道。
- OpenCode Go。
- OpenCode Zen，以及通过自定义 Base URL 接入的自建 `opencode-cc`；OpenCode Zen 未填写 Base URL 时默认使用 `https://opencode.ai/zen`。

支持提供商名称、状态、模型数量、并发用量、Base URL、API Key、模型映射、Priority、Weight、前缀、Header、代理和渠道专用选项。CPA 渠道条目本身没有名称字段，因此名称、该渠道的用量身份等由插件保存为「渠道记录」：键为渠道 Base URL 与 API Key 的加盐不可逆摘要，每次读取都会用实时渠道数据重新校验并回写；仅 Base URL 或仅 Key 相同的渠道不会被认错。换 Key 后会按新摘要重新读取，若该 Base URL 在同类渠道中唯一，则自动继承旧记录（名称与历史用量身份），因此历史用量仍能对上；Base URL 重复时不做继承，避免两个渠道互相认领。API Key 始终脱敏，编辑时留空会保留原值。每行的「更多」菜单集中提供重置本地用量与删除渠道（删除为最后一项）；支持查看、测试、编辑、启用、禁用、删除、模型目录、真实模型测试、Token/成本统计、15 秒/60 秒并发、5h/7d 自定义预算、代理档案和 Codex 身份策略；无法由当前 CPA 修改的能力会显示兼容提示，而不是伪造可用状态。

OpenCode Go 还支持 Workspace ID 与 auth Cookie、5h/7d/30d 配额、重置时间、手动刷新和删除（配置与刷新在鉴权界面中进行；独立的 OpenCode 状态页无需鉴权，因此只读取缓存状态、掩码工作区 ID，且不接收任何凭据）。

### Codex

侧边菜单在「OpenCode」之前新增独立的 **Codex** 工作区：

- 工作区按标签页组织，依次为：「总览」、「模型与价格」和「指纹配置」。
- 「总览」展示 Codex 账号数、AI 提供商渠道数、已禁用模型数、账号级覆盖数、提供商级覆盖数、已覆盖指纹字段数、有效收敛模式，以及一个表示全局模型管控是否启用的指示器。
- 「总览」新增「额度窗口」：直接读取各 Codex 凭据自身的 5 小时 / 7 天用量快照，给出用量最高的那个账号的占比与重置时间（运行时的百分比是插件自己的预算策略，因此不用于这里）；没有任何凭据上报该窗口时整块不显示，而不是显示 0%。
- 「总览」的 Codex 账号数与「模型与价格」的每行账号数来自宿主列出的 Codex 凭据（含原生 OAuth 账号）及其实际模型目录，而不是运行时的提供商身份：只配置了原生 Codex 账号的部署不会再把账号数显示为 0、模型列表显示为空。
- Codex 身份兼容策略编辑器位于「总览」。两个 Codex 实验项（Codex 5h / 7d 额度透支续用、Agent Identity / PAT）仍作为实验性开关保留在「其他配置 → 实验性功能」；Codex 页面保存时会回显这两个开关的当前值，实验性功能保存时会回显身份策略，两个页面不会互相清空配置。
- 「指纹配置」把原先直接编译进 Codex 指纹的每个取值都变成可编辑字段，字段旁同时展示内置默认值与当前生效值，并提供「已覆盖」标记、单字段恢复、分组恢复和全部恢复操作。
- 可编辑字段为：收敛模式；客户端身份字符串（User-Agent、Originator、Version、OpenAI-Beta）和 turn 元数据请求头名称；显式 installation/session/thread id（留空表示自动派生）和窗口后缀；installation、session 和 thread id 的派生前缀以及种子策略（按账号或固定种子）；请求体开关（turn 时间戳、关联字段和 prompt-cache-key 重写）。
- 清空字段或执行恢复都会回到默认值；无效取值会被拒绝，且不会产生任何改动。指纹配置作用于每个 Codex 账号和每条 Codex AI 提供商渠道，AI 提供商页面上的账号级与提供商级收敛覆盖仍优先于档案默认值。
- 「模型与价格」提供 Codex 模型 id 的全局开关列表，展示每个模型被多少 Codex 账号和 AI 提供商渠道引用；列表带选择列，每行提供「测试」和「禁用」/「启用」操作，并支持批量「禁用所选」「启用所选」以及既有的「全部启用」；模型测试通过已保存的 Codex 账号凭据发起探测。每个模型同时显示插件计费所采用的价格——输入、输出与缓存读取的「美元 / 百万 token」单价，取自与 Codex 用量计费相同的 Sub2API / Wei-Shaw 价格表，并标注该模型的长上下文倍率；列表上方展示价格来源与同步时间。价格表未收录的模型会标记为「暂无价格」，而不是显示为免费。禁用会同时作用于所有 Codex 账号和 AI 提供商渠道，并在请求路径上强制执行：被禁用的模型会立即收到拒绝响应，而不是被转发到上游，因此改动在下一次请求即生效，无需等待宿主侧策略应用。此能力只影响 Codex 流量，同一模型 id 在其他提供商系列上不受影响。
- 「Codex 自动模型白名单」是「其他配置 → 实验性功能」中的可视化实验开关，默认关闭。开启后仅在某个账号的默认探测模型被上游以已知的 ChatGPT 账号不兼容响应拒绝、且回退模型可用时才触发：插件会逐一探测该账号模型目录中的全部模型（上限 32 个、并发 3 个、总预算 60 秒），只有每个模型都给出明确结论时才写入白名单；否则不写入任何策略，并在操作日志中记为 `insufficient_compatibility_evidence`。写入的策略会标记为自动探测（`auto_detected`、`detected_at`、`verified_models`），实验性功能面板会显示当前被自动限制的账号数量与最近探测记录；已有的人工白名单或黑名单不会被覆盖。
- 两项设置都保存在插件私有数据目录（0600），并通过要求 Management Key 的管理路由开放，位于 `/v0/management/plugins/cpa-account-config-manager` 下：`GET /codex/overview`、`GET|PUT /codex/fingerprint`、`POST /codex/fingerprint/reset`、`GET|PUT /codex/models` 和 `GET /experiments/auto-model-whitelist`。响应中不包含任何凭据。

### OpenCode

侧边菜单在「AI 提供商」之后新增独立的 **OpenCode** 工作区：

- 工作区按标签页组织，便于管理，依次为：「总览」（OpenCode 累计用量与额度窗口、计费摘要、对话会话状态和计数条）、「Go 账号」、「Zen 账号」、「渠道」，以及「模型与价格」（价格目录与模型测试）。快捷入口和刷新操作在每个标签页都保持可用，从账号行发起模型测试会切换到「模型与价格」标签页。
- 「总览」把 OpenCode 自己的用量放在最前面：总 Token（输入 / 输出 / 缓存拆分）、请求数，以及按 OpenCode 官方价格折算的**参考价**金额。金额只累计能证明属于本插件 OpenCode 渠道的运行时数据——Go 与 Zen 凭据按账号 ID 或工作区 ID 归因，无法归属的流量不会被算进来，也不会因为归属不了就显示成 0。下方按 5 小时 / 7 天 / 30 天给出额度窗口，每个窗口取用量最高的那个账号并给出重置倒计时，另外把运行时归到 5 小时 / 7 天窗口的参考价金额一并显示；完全没有记录到 OpenCode 流量时，这里如实显示「尚未记录到 OpenCode 供应商流量」，而不是一排 0。
- 绑定 OpenCode Go 时，先登录 opencode.ai 并打开 Go 工作区页面，再以 Workspace ID 和 auth Cookie（即 `auth` 的值，用于抓取额度）添加工作区，并可选择保存 OpenCode Go API Key；每个工作区展示已保存的 5h/7d/30d 配额。
- 以名称、API Key 和 Base URL 添加 OpenCode Zen 凭据，Base URL 可以是 Zen 网关 `https://opencode.ai/zen`（默认值），也可以是自建 `opencode-cc` 桥接地址（例如 `http://localhost:8787`）。
- 「渠道」标签页会读取已归属 OpenCode 的 CPA AI 提供商渠道并列出类型、名称、Base URL、模型数量、密钥状态以及工作区是否已管理该渠道。一次点击即可导入渠道凭据，无需重复填写：Zen 渠道（包括自建 `opencode-cc` 桥接）会成为 Zen 账号，Go 渠道会把其 API Key 附加到已保存 Workspace ID 和 auth Cookie 的工作区账号。若没有这样的账号，Go 渠道会返回一个明确状态，引导操作者先到「Go 账号」标签页填写 Workspace ID 和 auth Cookie。凭据在服务端读取并保存，绝不会到达浏览器。
- “加载模型”会从 OpenAI 兼容端点 `GET {base}/v1/models` 获取模型列表，请求携带 `x-opencode-client: cli` 请求头和 `opencode/<version>` User-Agent，并按账号缓存结果。
- 模型测试会使用所选模型发起真实 `POST {base}/v1/chat/completions` 探测，返回状态、原因码、HTTP 状态和延迟；原因码包括 `authentication_failed`、`model_not_found`、`quota_limited` 和 `upstream_unavailable`。
- 一键绑定会 upsert 一个 `openai-compatibility` CPA 渠道，把模型发布到 CPA 路由：Base URL 为 `{base}/v1`，以已保存的 API Key 作为 key 条目并携带 OpenCode 请求头，同时把已验证的模型目录写入渠道的模型列表；重复绑定同一账号只会更新已有渠道、保留既有别名，不会创建重复渠道。已保存的 Go 凭据可在「Go 账号」表格里用「编辑凭据」补全或修改 Workspace ID、auth Cookie 与 API Key（留空的字段保持原值），账号缺少 Cookie 或 API Key 时会直接在表格里给出提示——旧凭据只存了 API Key、缺 workspace 时不再需要删除重建。
- 快捷入口提供 `https://opencode.ai/auth`、`https://opencode.ai/workspace`、`https://opencode.ai/zen` 和只读的 OpenCode 状态页。
- auth Cookie 和 API Key 只在经过鉴权的 Management 连接中写入一次，保存在插件私有数据目录；不会返回给浏览器（只有 `key_set` 布尔标记），也不会写入日志。
- 计费以 OpenCode 官方文档为准：OpenCode Go 见 https://opencode.ai/docs/go/，OpenCode Zen 见 https://opencode.ai/docs/zen/。每次同步都会重新解析这些页面中的价格、额度与计费参数；models.dev 现在只作为机器可读镜像，用于补齐缺项。官方表格发布的价格优先，并会被标记为官方来源，因此官方调价无需升级插件即可生效。插件每 24 小时用 ETag 条件请求重新校验该目录，在插件私有数据目录保留缓存副本，并内置官方快照，因此离线或首次同步前价格即可用；OpenCode 工作区展示目录（模型、输入、输出、缓存读取、缓存写入、上下文窗口与按上下文长度的价格档位），并给出来源、最后同步时间和“同步价格”操作。
- 状态目录默认跟随 CPA 账号目录：未显式配置 `data_dir` 时，插件状态（OpenCode Go/Zen 账号、模型管控、指纹、自更新等）保存在 CPA 账号目录下的 `.cpa-account-config-manager/`，与已有的用量快照同一处，因此**重启 CPA 时的工作目录变化不再影响它**；旧的相对路径 `data/cpa-account-config-manager`、插件库目录旁与 CPA 可执行文件目录旁仍作为回退目录被检索并采纳。显式配置的 `data_dir` 依旧优先，插件不会覆盖它。`GET /opencode/storage` 会一并给出当前目录与全部回退目录。
- 凭据与状态位置可见、可恢复：`GET /opencode/storage` 返回插件实际使用的数据目录、账号状态文件路径、是否存在与账号数；当隐式数据目录里没有状态文件（例如 CPA 换了工作目录重启），插件会先在同名的已知位置（插件库目录旁、CPA 可执行文件目录旁）寻找已有状态文件并**采纳**它，而不是从空开始；读不出来的状态文件会先另存一份 `.unreadable` 备份，绝不因一次读取失败而丢掉凭据。
- 「模型与价格」提供逐模型管控表：每行带选择列和「测试」「禁用」/「启用」操作，并提供批量「禁用所选」「启用所选」和「全部启用」。禁用会同时作用于所有 OpenCode 账号和渠道，并在请求路径上强制执行：匹配的请求会立即以 `opencode_model_disabled` 被拒绝，而不是被转发到上游；这也是阻止 GPT 级模型消耗 OpenCode Go 资源的推荐做法。测试 OpenCode 模型时，探测通过引用该模型的 OpenCode 凭据发起。写入与测试都在被点击的行上显示进度，并在完成后给出全局提示；禁用/启用不等待 CPA 管理 API 的渠道扫描，因此点击会立即返回，扫描在后台刷新（读取则最多等待 5 秒后退回到上一次扫描结果），慢或不可达的管理 API 不会再让一次点击看起来「没有反应」。
- OpenCode 路由的用量改为按 OpenCode 自身价格计费，不再套用通用厂商价目表：当插件能按已记录的渠道 Base URL 将请求归属到某个 OpenCode 渠道时，就使用 Zen 或 Go 的官方价格。价格为公开数据，但相关路由仍需 Management Key。
- OpenCode Go 计费语义：每月 10 美元订阅，每个模型有自己的每月美元额度（文档示例：GLM-5.3 为每月 $15，GLM-5.3-Flash 为每月 $60），并按 5 小时窗口 20%、每周窗口 50%、整月 100% 拆分。工作区展示每个模型的每月额度及其推导出的 5 小时与每周预算，并给出官方的各窗口预计请求数，同时标记官方已弃用模型及其弃用日期。
- OpenCode Zen 计费语义：按百万 Token 计费的即用即付（pay-as-you-go）。余额低于 5 美元时默认自动充值 20 美元，并可为工作区和每个成员设置每月用量上限。工作区会一并展示该计量模式及其参数。
- 订阅价格、窗口拆分以及自动充值阈值与金额都会从文档正文解析，内置默认值仅作兜底，因此 OpenCode 侧的改动会在下一次同步时被采用。官方文档同步时间与镜像同步时间分开显示；某个来源暂时不可用时保留最近一次可用数据，而不会清空价格。
- OpenCode Go 路由始终为归属 OpenCode 的请求生成会话 ID：OpenCode Go 要求客户端“Send a stable session ID in `x-opencode-session` for each conversation so we can optimize routing and prompt caching”（引自 https://opencode.ai/docs/go/#where-can-i-use-it），插件按以下顺序取值：入站 `x-opencode-session` 原样保留；否则复用原生客户端会话请求头（可识别 Claude Code、Codex、ZCode、Pi 风格的请求头）；再取请求体中的会话 ID（`prompt_cache_key`、`session_id`、`conversation_id`）；否则对系统提示与首条用户消息构成的种子取加盐摘要；请求体解析不出这样的种子时，退化为对请求体有界前缀的加盐摘要；最后退化为由凭据与模型构成的稳定种子，因此归属 OpenCode 的请求绝不会不带该请求头，同一会话在各轮次保持同一 ID，且任何消息正文都不会被发送。归属采用分层判定：CPA 记录的鉴权索引能对应到已记录的 OpenCode 渠道时，按该索引精确归属；否则由模型判定——模型由 OpenCode 发布且请求不属于 Codex 流量，即归属 OpenCode，模型匹配忽略前缀与分隔符，因此 `opencode-go/` 这样的渠道前缀无法把模型藏起来。该模型回退是必要的：OpenCode Go 会拒绝未携带 `x-opencode-session` 的请求，渠道列表一时未知时不能因此不发该请求头。注入仅限 OpenCode 发布的模型 ID（Zen 与 Go 目录，以及各账号已加载的目录），不触碰其他模型。一键绑定创建的渠道也自带一个基线 `x-opencode-session` 值，因此不进行请求拦截的宿主也能保持可路由，而不会在上游失败。
- 工作区展示会话状态：启用/停用、覆盖的模型数量、已分配会话 ID 的请求数、观察到的不同会话数，以及归因构成（按渠道、按模型各识别了多少请求）和跳过原因（Codex 流量、其他渠道、非目标模型），因此可以看清某个请求为什么收到或没有收到该请求头。会话 ID 永不写入日志，每个安装的盐以 0600 权限保存在插件数据目录中。
- 会话粘性参照 Codex 的行为：调度选号会把同一会话固定到同一上游账号。粘性键优先取请求的会话请求头（`x-opencode-session`、`session-id`、`session_id`、`x-session-id`、`x-codex-session-id`、`x-claude-session-id`、`conversation-id`、`x-conversation-id`），都没有时再取与会话相关的请求元数据（`session_id`、`conversation_id`、`prompt_cache_key`、`x-opencode-session`、`thread_id`）。映射到的账号仍有容量时继续复用；账号饱和、从候选列表消失或映射过期（30 分钟）时，选号回退到常规的最低负载选择并重新建立映射。映射只驻留内存、有界（4096 个会话），从不持久化；没有任何会话标识的请求保持原有行为不变。
- 所有路由均为固定路径并要求 Management Key，位于 `/v0/management/plugins/cpa-account-config-manager` 下：`POST /opencode/models` 携带 `{kind: "go"|"zen", account_id}`，返回带模型列表的脱敏账号视图；`POST /opencode/model-test` 携带 `{kind, account_id, model, timeout_seconds?}`，返回状态、原因码、HTTP 状态、延迟、测试时间和详情；`POST /opencode/bind` 携带 `{kind, account_id}`，返回绑定信息（类型、Base URL、索引、是否新建、渠道 key）；`POST /opencode/accounts` 还接受 `{account_id, api_key}` 做仅更新密钥操作，`api_key` 为空时保留已保存的密钥，传入新密钥会使缓存的模型目录失效；`GET /opencode/pricing` 返回目录及其同步来源信息；`POST /opencode/pricing/refresh` 重新校验该目录并报告是否发生变化；`GET /opencode/session` 返回会话路由状态；`GET /opencode/channels` 返回 OpenCode 渠道及其导入状态；`POST /opencode/import` 携带 `{base_url}` 导入一条渠道凭据，成功返回 200，需要工作区凭据的 Go 渠道返回标记为 `needs_workspace` 的 409；`GET|PUT /opencode/model-control` 返回模型行与价格表来源信息，其中 `PUT` 携带 `{"disabled": [...]}`，两者都要求 Management Key。

- **不重启热重载**：CPA 只会在「插件市场安装」之后重新加载原生插件，所以新增 `POST /self-update/reload`：插件自己去读市场、确认市场版本不低于已写入版本（绝不降级），再请求 CPA 重装本插件；成功即热重载（刷新页面即可），失败会给出明确原因（市场不可用/已禁用/未收录/版本更旧/重装失败/仍要求重启），「其他配置 → 更新」里有「不重启热重载」按钮。若市场确实不可用，重启 CPA 仍是加载新动态库的唯一办法。
- 自更新不经过插件商店：`GET /self-update` 返回当前版本、已解析版本、解析来源、压缩包与校验和状态、插件库文件定位结果和 `restart_required`；`POST /self-update/check` 立即解析最新 Release（依次尝试 GitHub API、`releases/latest` 跳转、`releases.atom`）；`POST /self-update/install` 下载当前平台压缩包，用 Release 的 `checksums.txt` 校验 SHA-256 后原子替换插件库文件并保留 `<插件库>.previous` 备份，校验失败时不替换；`PUT /self-update/settings` 携带 `{"plugin_file": "..."}`，在宿主无法自动定位插件库时记录其路径。四个路由都要求 Management Key，响应只包含版本号、校验和、文件路径与状态。发布包同时携带 `ui/index.html`：安装后插件立即提供新界面（刷新页面即生效，无需重启），因此纯界面改动可以做到「不重启更新」；只有动态库本身仍需重启 CPA 才会加载，界面会分别显示「界面已更新」与「需要重启」，并给出 `ui_updated` / `interface_refresh_only` 字段。

### Cline Pass

侧边菜单的 **OpenCode** 工作区新增「Cline Pass 账号」标签页，用于绑定 Cline Pass 订阅并通过 OAuth 登录：

- 三种登录方式：浏览器设备码登录（由 WorkOS 设备授权下发用户码，轮询完成后通过 `POST {base}/auth/register` 换取 Cline 令牌）、复用本机已有的 Cline CLI 登录（读取 `~/.cline/data/settings/providers.json`），以及直接粘贴 Cline Pass API Key。
- OAuth 访问令牌带 `workos:` 前缀；令牌接近过期时插件用 `POST {base}/auth/refresh`（`granttype=refresh_token`）主动轮换，并立即把轮换后的 refresh token 落盘，因为网关会让上一个 refresh token 失效。
- 访问令牌与 refresh token 只保存在插件私有数据目录的 `cline-pass.json`；管理接口只返回 `access_token_set` / `refresh_token_set` 布尔值和过期时间，绝不返回令牌本身。
- 模型目录是显式白名单（Cline Pass 付费模型与免费档模型）。上游 `GET {base}/models` 只用于校验凭据，永远不会扩大插件发布或路由的模型集合；「加载模型」会重新校验并按账号记录 `models_error`。
- 「模型测试」向 `POST {base}/chat/completions` 发起一次最小真实请求，返回状态、原因码、HTTP 状态、延迟、测试时间与脱敏后的上游响应。
- 「发布到 CPA 路由」会 upsert 一个 `openai-compatibility` CPA 渠道：Base URL 为 `https://api.cline.bot/api/v1`，以访问令牌作为密钥，携带 Cline 产品面识别请求头（`x-client-type: cli`、`x-client-version`、`x-core-version`，以及 `Cline/<version>` User-Agent，版本取自 npm registry 并缓存 24 小时），并把白名单模型写入渠道模型列表。重复绑定同一账号只会更新已有渠道。
- 令牌轮换会让渠道里保存的旧密钥失效，因此「刷新登录」通过 `POST /opencode/cline-pass/refresh`（`rebind: true`）同时完成令牌轮换与渠道密钥重写。
- Cline Pass 的令牌会自己过期，而 CPA 只按渠道行里保存的那份密钥路由，于是过期或轮换过的令牌会表现为「AI 供应商账号授权错误、需要重新登录」，而账号页仍显示已绑定。现在每次读取 Cline Pass 账号页或模型页时会自动修复：先轮换落在刷新窗口内的令牌，再比对账号当前令牌与其渠道行里保存的密钥——若账号自己的渠道行仍持有旧密钥（按账号标签唯一识别），就自动重写该行，无需手动刷新登录，也不必删除并重新添加 AI 供应商账号。修复期间该账号会如实显示为「未绑定」而不是假装可用；自动修复受既有 30 秒冷却限制，成功与否都记录在操作历史里（`channel_bound` / `channel_bind_failed`）。标签重复（多个账号共用默认标签）时不猜测归属，保持原状。
- 令牌还可能在**记录的过期时间之前**就被网关拒绝（上游回 `Unauthorized: ... re-authenticate your Cline account`，CPA 随即冷却该凭据并回 `auth_unavailable: no auth available`，状态码 503）。这种情况现在也会自动修复：插件从请求完成回调里识别这次拒绝（状态码 401/403，或 503 且报文含未授权特征），按记录过的 auth-index／auth-id 归因到具体账号（归因不明时不猜测），随后自动轮换该账号令牌并重写它自己的渠道行；账号页在该账号修复完成前会如实显示「凭据已被网关拒绝，正在自动修复」并标记为不可路由，问题也不会被静默吞掉——操作历史里会留下 `cline_pass_credential_rejected`。整个自动修复有 15 秒合并退避（一批失败只触发一次轮换＋一次渠道写入）与 1 分钟每账号轮换节流，需要 Management Key 才能写渠道行（插件会记住最近一次管理请求带来的密钥，仅存内存、不落盘、不记日志）；若始终写不成功或令牌本身需要重新登录，页面会照实显示，此时再重新登录一次该账号即可。
- 所有路由均为固定路径并要求 Management Key，位于 `/v0/management/plugins/cpa-account-config-manager` 下：`GET|POST|DELETE /opencode/cline-pass/accounts`、`GET /opencode/cline-pass/catalog`、`POST /opencode/cline-pass/login/start|poll|cancel`、`POST /opencode/cline-pass/refresh`、`POST /opencode/cline-pass/models`、`POST /opencode/cline-pass/model-test`、`POST /opencode/cline-pass/bind`。
- 设备码登录在每次管理请求内只完成一次轮询而不阻塞，由页面按 `interval_seconds` 轮询；上游返回 `slow_down` 时把间隔加 5 秒，会话 15 分钟后过期。

### 操作日志、界面与更新

- 操作日志覆盖导入、导出、批量修改、模型测试、策略扫描、巡检、自动处置、通知和插件更新，记录成功/失败/部分完成、失败依据、数量、脱敏样本、来源和时间。
- 「大模型错误」是日志里的独立分类：每个最终失败的模型请求都会写入一条，含**上游原始错误信息**（已剥离 `Bearer`/`sk-…`/`at-…`/`api_key=…` 等凭据形态、长度有上限，永远不会落盘凭据）、上游状态码、模型、本次允许的重试次数与请求 ID，可按该分类筛选排查。重试次数用尽后，插件用 **503** 结束请求（`code=retry_exhausted`、`retry_limit`、`request_id`），而不是把上游状态码直接透传；客户端主动取消记为「已取消」。宿主要求下，插件无法改写**已经失败**的响应，因此 503 通过拦截「预算之外的最后一次尝试」实现：宿主每凭据预算比设定值多留一次，正好由插件用它返回 503，操作者设定的重试次数一次不少。
- 界面支持简体中文、繁体中文、English 和 Русский，并跟随 CPA 语言与主题；另提供中性、靛蓝、森林、玫瑰主题，舒适/紧凑密度，小/中/大字号，以及主标题与描述字号区分。
- 表格排序、分页大小、筛选条件和手动测试模型会持久化。
- 可检查并从 CPA 插件商店安装插件更新，也会展示 CPA 当前版本和最新版本。插件只检测 CPA 主程序更新，不替换 CPA 可执行文件。插件商店读取不到数据时，还可在「其他配置 → 更新」里直接使用本插件的 GitHub Release 更新自身：解析最新版本、按当前平台选择压缩包、用 Release 的 `checksums.txt` 校验 SHA-256，校验通过才原子替换插件库文件并保留上一份备份；下载仅限 GitHub 域名，且下载体积有上限。


### Cline Pass

侧边菜单中的 **Cline Pass** 是与 Codex、OpenCode 并列的独立工作区。Cline Pass 账号不出现在 OpenCode 页面里。

- 「账号」页签用 OAuth 设备码、复用已有的 Cline CLI 登录或直接粘贴 API Key 添加账号，并显示每个账号的凭据状态与**路由**状态：已绑定（并显示该渠道发布了多少个模型）、有 N 个模型未发布，或未绑定（并提示客户端此时会收到 `unknown provider for model`）。登录成功或保存账号时会自动绑定到 CPA 渠道，绑定失败只作为提示，不影响账号保存。
- 「模型」页签列出 CPA 渠道为 Cline Pass 发布的模型映射：模型名称、**客户端模型 ID**（客户端应调用的 ID）、上游 ID、是否已发布，以及逐行「测试」按钮（测试始终用上游 ID 直接探测 Cline 网关）。
- 「模型」页签顶部的开关控制发布映射是否去掉 `cline-pass/` 前缀，默认开启。开启后客户端可直接调用短模型名（例如 `deepseek-v4.1-flash`），关闭后必须使用带前缀的 ID；切换后会立即重新绑定所有账号。只去掉 `cline-pass/` 这一前缀，`cline-free/`、`z-ai/` 等其它模型 ID 保持不变。
- 同一个网关可以有**多个账号同时绑定**：每个账号发布自己的一行 `openai-compatibility` 渠道行（标签为账号名称，未命名时用账号 ID），凭据与模型列表互不影响。网关也按行选择凭据，因此一个账号被限流时，另一个账号的行仍在服务同样的模型。
- **额度受限时临时停用**：当 Cline 网关回 `429`（`INFERENCE_CAP_ERROR`，如「The limit resets in 2d 22h」）时，插件记录网关自己给出的重置窗口（解析不出时保守按 15 分钟，并钳制上下限），并把**该账号自己的渠道行**临时置为 `disabled`，让 CPA 停止向这个已耗尽额度的凭据路由，请求改由同网关的其它账号承接；窗口结束，或一次模型测试／真实请求证明该账号已恢复时自动重新启用。渠道行与已发布模型列表都保留，因此停用期间无需重新绑定，用量归因也不会断。停用期间账号页会如实显示「已临时停用（额度受限）」与恢复时间。
  - 两条防误伤规则：**只有本插件停用过的行才会被插件重新启用**——手动禁用的行永远不会被插件打开，包含额度记录已过期的情形；以及**每次看到 429 都会重新确认该行处于停用状态**，即便网关给的是同一个窗口（记录没变化、甚至记录写盘失败，也照样把该行移出路由），因此不会出现「测试说不可用、渠道却还在被调度」。写盘失败会记入操作历史，模型测试的返回里也带有这次处置的结果（`quota.row` / `quota.row_error`）。
  - 触发点：模型可用性测试的 `quota_limited` 结果（也就是报告里用的那个测试）、请求完成回调里的额度报文，以及由此触发的后台维护流程——它与凭据修复共用同一把维护节流，避免两个写入者互相覆盖渠道列表。被插件停用的行不会被一次「成功」的回调重新启用：行已停用时那次成功不可能来自该凭据，只可能是归因错误。
  - 记录（窗口与「本插件停用过该行」）都会持久化，重启后不会被自动解除；重复的限流答复只在窗口明显延长时才重新写盘。
- 「总览」是默认首屏：把三个官方窗口（滚动 5 小时、自然周、自然月）的请求数、Token 与**参考价**金额汇总展示，并给出本月**已用额度**与 9.99 美元/月订阅的对照。插件不再推算「剩余余额」：按参考价折算出的余额并不等于真实可用的订阅余量，而已用额度是可测量的那一个。金额按 Cline 官方公布的参考价折算，只是「按标准 API 价折算」的对照值，不是账单。Cline 网关把 DeepSeek 订阅模型发布为 `cline-pass/deepseek-v4.1-flash`，而官方参考价表按「DeepSeek V4 Flash」计价——DeepSeek 已下线 `deepseek-v4-flash` 并由 DeepSeek-V4.1-Flash 以 Flash 价格承接——插件把两种写法归一到同一行参考价，因此该模型的用量不会再显示成 0。官方未公布参考价的模型（例如 `cline-pass/glm-5.3-flash`）仍按「未定价」显示并给出未定价请求数，避免把 0 当成免费。
- 历史用量补价：费率表学会某个模型之前记录的用量过去会永久停留在「未定价」。现在每次读取 CPA 渠道列表、以及每次打开用量接口时，插件都会对**能精确套用到多条请求合计**的历史行补算金额，并把计数从「未定价」移到「已定价」；长上下文、优先级这类分档费率无法套用到合计值，因此保持未定价（不估算、不猜价）。补价只累加总额，不会写进滚动窗口，重启后依然有效。
- 窗口事件会随账号状态写入插件私有存储，重启或热更新后三个窗口不再归零；只保留仍落在窗口内的事件，键数与每键条数都有上限，已过期的事件不会被恢复。

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

CPA 加载插件后，可从 Management Center 打开 **CPA-A Manager**。大多数插件商店更新只需刷新页面；只有 CPA 明确返回 `restart_required: true` 或动态库被宿主锁定时才需要完整重启。通过插件自身 GitHub 直连更新替换的是磁盘上的插件库文件，运行中的 CPA 仍映射旧库，因此始终需要重启 CPA 才会生效（界面会显示 `restart_required`）。

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
- Cline Pass OAuth 登录与模型目录：[fifidayone/pi-clinepass](https://github.com/fifidayone/pi-clinepass)
- 社区链接：[LINUX DO](https://linux.do/)

这些项目提供了产品行为参考；除非仓库许可历史另有说明，本插件没有直接复制其代码。

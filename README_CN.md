# CPA Account Config Manager

[English](README.md)

`cpa-account-config-manager` 是一个独立发布的 CLIProxyAPI 原生插件，用于在同一界面添加、查看、编辑、删除、筛选、导出并批量修改账号配置，避免来回切换 CPA 页面或逐个编辑 Auth JSON。

插件架构参考 [`ywddd/grok-inspection`](https://github.com/ywddd/grok-inspection)，账号选择和字段按需启用的交互参考 sub2api。巡检功能是本项目重点参考的部分：双巡检路径、账号健康判定、“仅恢复自己禁用账号”的归属边界和保守自动处置设计主要参考 [`seakee/CPA-Manager-Plus`](https://github.com/seakee/CPA-Manager-Plus)；429 窗口判断和实时额度/账号运营界面同时参考 [`ysxk/codex-429-autoban`](https://github.com/ysxk/codex-429-autoban) 与 [`zhumengling/codex-token-usage`](https://github.com/zhumengling/codex-token-usage)，并按 CPA 原生插件回调与 Management 鉴权边界独立重新实现。

## 功能

- 账号列表独立显示账号/套餐类型，并支持搜索以及提供方、类型、状态、启用状态、可编辑性筛选；筛选条件经过校验并持久化，刷新后会自动恢复。sub2api 导入的 `plan_type` 会保留并优先于 OAuth/API Key 类型显示。
- 使用 CPA 原生数据被动展示每个账号的累计/近期请求、累计 Token，以及可用时的 Codex 5 小时和 7 天额度进度。
- 提供快速 CPA 原生巡检与全量服务器巡检：前者同步全部原生状态但不请求模型，后者固定目标账号快照并在服务器端按有界批次完成模型可用性测试；页面展示来源、状态、完成数、总数和剩余数。
- 主动巡检每完成一个账号就立即写入鉴权且禁止缓存的实时快照；页面约每 0.7 秒更新当前进度、逐账号结果、额度窗口与 Token 用量，无需等待整批结束，并始终提供模型重测、启用/禁用、复核与安全删除入口。
- Codex 429 同时识别相对 `reset-after` 和绝对 `reset-at`，多个窗口耗尽时采用较晚恢复时间；Codex 缺少窗口头时使用有界的 5 小时安全恢复窗口。
- 账号列表直接显示与当前策略一致的处置标签：是否由巡检自动禁用、规范化禁用原因、明确的额度恢复时间、等待恢复证据、建议删除、删除宽限期和删除重试；策略未开启时不会伪装成正在自动执行。
- 可配置自动禁用、仅限巡检归属账号的自动恢复，以及单独确认、超过宽限期后才执行的停用账号自动删除；所有破坏性选项默认关闭。
- 检查公开 GitHub Release、显示更新提示，并通过 CPA 鉴权插件商店按需安装；GitHub 信息不可用时使用插件商店中的稳定版本作为提示回退，可选择仅在管理页面打开期间自动更新。
- 每行提供脱敏详情、固定单账号范围编辑和确认弹窗删除；顶部“添加账号”复用安全的多格式转换导入流程。
- 每行可对 Codex/OpenAI、Claude、Gemini/AI Studio 和 xAI 账号做模型可用性测试；请求由 CPA 按指定 `auth_index` 执行，浏览器不能提供上游地址、请求头或原始请求体。
- 本页多选、明确的“已选账号”范围、选中账号凭据下载、可持久化的 20/50/100/200 每页数量，以及固定快照的“全部筛选结果”范围。
- 批量启用、批量禁用，以及 `priority`、`note`、`prefix`、`proxy_url`、`websockets`、自定义 Header 的按字段批量编辑。
- 为 `priority` 和 `websockets` 保存默认策略，并写入 CPA 宿主管理的插件配置，重启或更新插件后仍会自动恢复；后台只补齐已有及后续上传 Auth 文件中缺失的受管字段。
- 提供独立的预览确认式强制同步，在操作员明确选择时覆盖所有可编辑 Auth 文件中的受管默认字段。
- 写入前由服务端生成预览，显示目标数、可执行数、只读数、缺失数和物理文件数。
- 后台异步执行、受限并发、逐账号结果、Revision 冲突检测、部分成功继续和仅重试失败项。
- 把当前筛选或当前勾选账号直接下载为 CPA、sub2api、Cockpit、9router、Codex、AxonHub 或 Codex-Manager 凭据文件。
- 把批量任务结果导出为脱敏 JSON、CSV 或 JSON Lines 报表。
- 账号删除、批处理、导入导出、默认策略、巡检自动化和插件更新统一进入持久化“操作日志”，支持筛选、详情弹窗、关联任务控制、导出和二次确认清理。
- 支持粘贴文本 JSON，或一次混合选择多份 JSON、JSON Lines、TXT、ZIP 文件；服务端递归识别多种账号结构，转换成 CPA Codex Auth JSON，预览确认后导入且不覆盖现有 Auth 文件。
- React 单文件内嵌界面，页面层级、控件、密集表格、弹窗以及浅色、纯白、深色主题均与 Management Center 保持一致，并支持同源认证状态。插件读取 CPA Management Center 同源的 `cli-proxy-language` 选项，实时跟随简体中文、繁体中文、英文和俄语切换，不保存可能与 CPA 脱节的独立语言偏好。四套类型化语言目录和统一插值函数保证组件不再包含针对某种语言的条件分支。

插件仍不提供刷新 Token、OAuth 重新授权、无限制凭据编辑或直接调用供应商额度接口。只有操作员明确执行全量服务器巡检，或显式开启定时主动模型探测时，才会通过 CPA 固定 Management 路由发送有界模型测试请求，并可能消耗少量上游额度；其余巡检证据均来自 CPA 已持有的原生状态与 Usage 记录。

## 兼容性

插件使用 CLIProxyAPI 原生插件 ABI/Schema v1，需要宿主具备：

- 原生插件发现、Management 路由和 Resource 路由；
- `host.auth.list`、`host.auth.get` 与 `host.auth.save` 回调；
- 用于公开 GitHub Release 元数据检查的 `host.http.do`；
- `PATCH /v0/management/auth-files/status`；
- `PATCH /v0/management/auth-files/fields`；
- 用于确认删除的鉴权 `DELETE /v0/management/auth-files?name=<file.json>`；
- 用于指定一个账号执行白名单模型探测的鉴权 `POST /v0/management/api-call`；
- 用于插件更新的鉴权 `GET /v0/management/plugin-store` 与
  `POST /v0/management/plugin-store/cpa-account-config-manager/install`。

Token 累计和 Codex 额度进度还会使用原生 Usage Plugin 的 `usage.handle` 回调。宿主没有分发 Usage 记录时，账号列表和 CPA 请求计数仍可使用，Token 与额度位置显示为暂无数据。

插件不导入 CLIProxyAPI Go 包，也不要求修改 CLIProxyAPI 可执行文件。

发布矩阵：

| 平台 | 架构 | 动态库 | 发布压缩包 |
| --- | --- | --- | --- |
| Linux | amd64 | `cpa-account-config-manager-v<version>.so` | `cpa-account-config-manager_*_linux_amd64.zip` |
| Linux | arm64 | `cpa-account-config-manager-v<version>.so` | `cpa-account-config-manager_*_linux_arm64.zip` |
| macOS | arm64 | `cpa-account-config-manager-v<version>.dylib` | `cpa-account-config-manager_*_darwin_arm64.zip` |
| Windows | amd64 | `cpa-account-config-manager-v<version>.dll` | `cpa-account-config-manager_*_windows_amd64.zip` |

动态库与平台、架构强绑定，不能混用。

## 安装

### 1. 下载并校验

从 [Releases](../../releases) 下载与 CLIProxyAPI 宿主匹配的 ZIP，同时下载 `checksums.txt` 或对应的 `.zip.sha256`。

Linux：

```bash
sha256sum -c cpa-account-config-manager_0.2.7_linux_amd64.zip.sha256
```

macOS：

```bash
shasum -a 256 -c cpa-account-config-manager_0.2.7_darwin_arm64.zip.sha256
```

Windows PowerShell：

```powershell
Get-FileHash .\cpa-account-config-manager_0.2.7_windows_amd64.zip -Algorithm SHA256
Get-Content .\cpa-account-config-manager_0.2.7_windows_amd64.zip.sha256
```

### 2. 放置动态库

解压后，将动态库放进 CLIProxyAPI 插件目录。推荐使用宿主优先扫描的平台子目录：

```text
plugins/linux/amd64/cpa-account-config-manager-v0.2.7.so
plugins/linux/arm64/cpa-account-config-manager-v0.2.7.so
plugins/darwin/arm64/cpa-account-config-manager-v0.2.7.dylib
plugins/windows/amd64/cpa-account-config-manager-v0.2.7.dll
```

Linux/macOS 上确保 CLIProxyAPI 服务账号可读、可执行：

```bash
chmod 755 plugins/linux/amd64/cpa-account-config-manager-v0.2.7.so
```

### 3. 启用插件

在 CLIProxyAPI 的 `config.yaml` 中加入：

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    cpa-account-config-manager:
      enabled: true
      priority: 20
```

重启 CLIProxyAPI 后，Management Center 的插件菜单中应出现 **账号管理**。

标准 CLIProxyAPI 布局只需要上面的最小配置；`workers`、`data_dir` 和 `management_base_url` 都是可选覆盖项。如果 CLIProxyAPI 不是监听 `8317`，请把 `management_base_url` 改成 CLIProxyAPI 进程内部可访问的回环地址。Docker 中也应使用 `http://127.0.0.1:<端口>`；`http://cli-proxy-api:8317` 这类 Compose 服务名不是回环地址，会被有意拒绝。插件只接受 `http://` 或 `https://` 的 `localhost`、`127.0.0.0/8`、`::1`，拒绝远程主机、凭据、路径、查询参数和 Fragment，避免 Management Key 被转发到外部地址。

## 配置项

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `workers` | `6` | 同时执行的账号修改数。小于 1 时恢复为 6，大于 16 时限制为 16。 |
| `data_dir` | `data/cpa-account-config-manager` | 脱敏终态任务、向后兼容的 `default-policy.json`、`usage-snapshots.json`、`inspection-state.json`、`update-state.json` 与有界 `operation-log.json` 的目录。要让巡检/动作/更新策略和审计记录跨 CPA 重启与插件替换保留，必须持久化该目录；字段为空时读取 `CPA_ACCOUNT_CONFIG_MANAGER_DATA_DIR`。 |
| `management_base_url` | `http://127.0.0.1:8317` | 普通批量编辑和确认删除使用的可选 CLIProxyAPI 原生写入接口回环地址；默认策略补齐和强制同步改用宿主 Auth 回调。还支持 `CPA_MANAGEMENT_BASE_URL`、仅限回环地址的 `CPA_BASE_URL`、`PORT`、`CPA_PORT`。 |

同一对象里的 `enabled` 和 `priority` 由 CLIProxyAPI 插件宿主管理；界面中编辑的账号 `priority` 是另一项账号字段。

操作界面会自动把不含秘密值的 `default_policy` 浅合并到 `plugins.configs.cpa-account-config-manager`。CPA 将它写回自己的 `config.yaml`，因此策略启用状态、受管字段、字段值和扫描间隔可以跨进程重启及插件版本替换恢复，无需手工编辑 YAML。私有 `default-policy.json` 继续作为旧版本兼容回退和扫描摘要缓存；两份数据同时存在时以宿主配置为准。

## 权限要求

CLIProxyAPI 进程需要：

- 对动态库有读取和执行权限；
- 对 Auth 目录有读写权限，因为真正的字段持久化由 CLIProxyAPI 原生 Management API 完成；
- 对实际生效的 `data_dir` 有读写权限。巡检与更新策略、脱敏巡检/动作状态、终态任务、策略扫描缓存和脱敏使用量快照都使用该目录；默认 Auth 策略还会在 CPA 配置中保留一份耐久副本。

支持权限位的平台上，插件以 `0700` 创建数据目录，通过临时文件原子替换 JSON 状态，文件权限为 `0600`。巡检和更新状态只包含白名单身份、原因码、计数、策略、版本和时间戳；使用量状态只包含累计 Token 与规范化后的 Codex 百分比/重置时间。所有状态文件都不保存原始 Auth JSON、API Key、失败正文、Cookie、原始响应 Header 或 Management Key。建议让 CLIProxyAPI 和插件使用同一个非 root 服务账号运行。

`operation-log.json` 最多保留 2,000 条，只含固定类别、动作、状态、来源、范围、公开关联 ID、有界计数、时间、白名单原因码、版本和导出格式。日志落盘是尽力行为：存储失败会单独显示为日志健康异常，但不会把已经完成的账号操作改判为失败。

## 使用流程

1. 在 CLIProxyAPI Management Center 打开 **账号管理**。
2. 如果官方面板已经选择“记住密码”，同源 iframe 会复用面板认证；否则输入 CPA 地址和 Management Key。手动输入的 Key 只保存在浏览器内存中，刷新即清除。
3. 筛选账号，并明确选择“已选账号”或“全部筛选”。
4. 点击“导出选中”只下载勾选 ID，或用顶部下载按钮导出全部当前筛选结果。
5. 点击批量启用、批量禁用或批量编辑。批量编辑中只有勾选的字段才会进入 Patch。
6. 检查服务端预览中的目标数、只读/缺失项、变更字段和告警。
7. 启动任务，查看逐账号进度。出现部分失败后，可用“仅重试失败项”重新处理失败或冲突账号。

预览有效期为 5 分钟。执行时消费的是固定目标快照，后续新匹配筛选条件的账号不会被静默加入。

## 账号增删改查

- **添加：** 顶部“添加账号”支持粘贴文本 JSON、混合多文件和 ZIP，继续使用下文的安全转换与预览导入流程。
- **查看：** 每行眼睛按钮显示白名单详情，包括账号身份、文件名、Provider/Type、状态、路由配置、脱敏代理地址、Header 名称、请求/用量、时间和可编辑性。浏览器不会请求原始 Auth JSON 或凭据值。
- **编辑：** 每行铅笔按钮打开现有按字段启用的编辑器，范围固定为该账号的 `selected` ID，不会改变表格批量勾选。后续仍走服务端预览、物理 Revision 检查、共享写入槽位、任务结果与冲突处理。
- **删除：** 每行垃圾桶按钮先为一个可编辑文件账号创建 5 分钟删除预览，确认弹窗会显示账号与文件身份，不需要输入用户名或文件名。二次确认后插件占用共享写入槽位，再次读取并比较物理 Revision，然后调用 CPA 鉴权后的回环 Auth 文件删除接口。文件已变化、目标缺失、重复来源、运行时账号或其他只读记录都不会删除。

删除有意只支持逐账号确认，没有“删除全部筛选账号”。成功删除无法由插件撤销，操作前应备份 Auth 目录或先导出该账号。

## 模型可用性测试

每行的活动图标会打开模型测试弹窗，并按提供方预选模型。确认后，插件通过 CPA 鉴权后的 `/v0/management/api-call` 发送一次最小 `hi` 生成请求，用 `auth_index` 固定目标账号，因此 Token 刷新和账号代理选择仍由 CPA 负责。只读或运行时账号只要 CPA 能解析其运行时索引也可以测试；测试不会修改账号配置或状态。

浏览器只能提交有长度和字符限制的账号 ID 与模型 ID，不能提交 URL、Header、Prompt、Payload、凭据或代理。插件只会构造 Codex/OpenAI、Claude、Gemini/AI Studio 和 xAI 的固定 HTTPS 请求，设置 20 秒超时和响应体上限；对外仅返回规范化可用性、固定原因码、提供方/模型 ID、延迟和时间。模型输出和上游原始响应正文不会返回、持久化或写入日志。不支持的提供方会直接返回结构化“暂不支持”，不会发起网络请求。

每次结果都会作为 `model_test` 写入统一操作日志，只记录公开账号/模型 ID 与规范化原因。测试失败、额度受限或认证失败只供操作员判断，不会触发自动禁用、启用或删除。

## 账号导入

在操作栏打开“添加账号”。文件模式一次最多混合选择 64 份 JSON、JSON Lines、NDJSON、文本 JSON 和 ZIP；“文本 JSON”模式会把粘贴内容作为一份仅存在于浏览器内存中的文本来源提交。每份来源可以包含一个 JSON 值、多个顶层 JSON 值或逐行 JSON；每个值又可以是单账号、数组或任意层级的对象与数组。转换器会递归识别 [`GPTSession2CPAandSub2API`](https://github.com/Mxucc/GPTSession2CPAandSub2API) 使用的 ChatGPT session、sub2api、9router、Codex、Codex-manager 和已有 CPA 字段别名。

每个 ZIP 可以包含多个目录以及 `.json`、`.jsonl`、`.ndjson`、`.txt` JSON 来源。目录和无关条目不会解压到磁盘，只会在预览中列为跳过；多文件请求中，单份来源损坏时也会跳过该文件并继续处理其他有效来源。ZIP 路径穿越、符号链接、加密条目、不支持的压缩方法、异常压缩比或累计上限超标会在写入任何 Auth 文件前拒绝整批请求。

服务端预览只返回账号身份、来源位置、生成的 CPA 文件名和警告，不返回 Access Token、Refresh Token、ID Token、Session Token 或原始 JSON。转换后的凭据只保存在有界的 5 分钟进程内预览中，上传或粘贴的原始 JSON 不进入预览存储；服务端接受预览后，浏览器会清除粘贴文本和已选 `File` 引用。确认后，插件占用共享写入槽位并通过 `host.auth.save` 保存规范化的 `type: codex` 文档。

所有限制按整次混合请求累计，不能通过拆成多个 ZIP 绕过：

| 限制 | 数值 |
| --- | --- |
| 顶层上传文件 | 64 |
| 所有 ZIP 的条目总数 | 256 |
| Multipart/原始请求体 | 12 MiB |
| 单个解压 JSON 条目 | 2 MiB |
| 所有 ZIP 的 JSON 解压总量 | 32 MiB |
| JSON 层级 / 访问节点 | 32 层 / 50,000 个节点 |
| 转换账号数 | 10,000 |

预览时会结合当前 Auth 列表预留目标文件名；正式保存前再次检查。同名文件只会跳过，不会调用宿主可覆盖保存接口。宿主 ABI 暂无 create-only Compare-and-swap，因此最终名称检查与保存之间仍存在很窄的外部竞态窗口。

## 账号凭据与结果导出

账号下载可以沿用包括 Type 在内的当前筛选条件，也可以固定为当前勾选的账号 ID；两种方式都会在下载对话框中要求明确选择目标格式。选中 ID 通过鉴权 POST body 发送，不放进可能过长的 URL。CPA 会保留每个匹配的文件型 Auth JSON：单账号直接下载 `邮箱.json`，多账号下载 ZIP，压缩包内每个账号对应一个唯一且路径安全的 `邮箱.json`。

| 账号格式 | 结构 |
| --- | --- |
| CPA | 原始 CPA Auth JSON；多账号自动打包 ZIP。 |
| sub2api | 一个 `exported_at/proxies/accounts` 批量导入文档。 |
| Cockpit | 扁平 Codex Token 对象；多账号为数组。 |
| 9router | Codex OAuth 对象；多账号为数组。 |
| Codex | 原生 `auth.json` 对象；多账号为数组。 |
| AxonHub | AxonHub Codex Auth 对象；多账号为数组。 |
| Codex-Manager | `tokens/meta` 对象；多账号为数组。 |

除 CPA 外的目标只转换兼容的 Codex OAuth Auth 文件。运行时账号、无效文件、读取失败或不兼容记录会跳过；响应通过 `X-Exported-Accounts` 与 `X-Skipped-Accounts` 返回实际导出和跳过数量。

这些下载包含 Token 等凭据；CPA 输出还可能包含原有代理凭据和 Header 值。凭据下载只走鉴权后的精确 Management 路由，必须显式选择格式，并设置 `Cache-Control: no-store` 与附件响应头；插件不会把生成文件写入状态目录或日志。

批量任务结果仍是脱敏运维报表，支持 JSON、防公式注入的 CSV 和 JSON Lines，只包含现有结果白名单字段。

## Auth 文件默认策略

在操作栏打开“默认策略”，可以分别管理 `priority` 与 `websockets`。未勾选的字段不受策略管理；`priority: 0` 和 `websockets: false` 都是有效的明确值，不会被当成空值。启用策略时至少要管理一个字段。

策略启用后会立即扫描，随后默认每 15 秒轮询一次；界面允许设置 5 到 300 秒，也可以手动“立即扫描”。自动扫描通过 `host.auth.list/get/save` 工作，只在 JSON 对象中完全没有对应 Key 时补齐。上传文件已带的值以及之后的手动修改始终优先，不会被后台覆盖。新上传的 Auth 文件会在下一次有界扫描中被发现，不需要修改 CLIProxyAPI 核心，也不需要浏览器 Management Key。

“强制同步”是单独的覆盖操作。它先生成有效期 5 分钟的预览，明确展示受管字段的精确值和只读跳过项，确认后才启动。任务会逐文件重读并拒绝 Revision 冲突，只覆盖策略勾选的 `priority` 和/或 `websockets`；不会修改 `disabled`、代理、Header、备注、前缀、Token、Cookie、凭据或任何其他未知字段。

在界面保存时，会先把完整的非敏感策略写入 CPA 宿主管理的插件配置，再立即应用到当前插件。`data_dir` 中的策略/扫描缓存改为尽力写入，即使目录不可用也不会阻止宿主配置中的策略保存。自动扫描和强制同步都不使用 `management_base_url`；它仍只是普通批量编辑的可选覆盖项。

## 账号巡检与自动处置

打开“巡检与自动化”可以执行两种明确的手动巡检、按健康状态筛选、查看判定证据与自动化记录，并配置定时任务：

- **快速巡检**完整读取 CPA 原生账号、运行状态、请求计数、规范化 Codex 额度窗口和 `usage.handle` 语义化证据，不发送模型请求。
- **全量服务器巡检**先刷新上述原生证据，再固定本轮全部符合策略的账号 ID，在服务器端按有界批次持续执行模型可用性测试，直到目标快照完成。刷新页面或切换标签不会中断；总数、完成数、剩余数、来源和状态会持久化。CPA 重启后 Management Key 仍不会落盘，下一次已鉴权访问会重新激活等待认证的未完成巡检。

全量巡检期间新增账号进入下一轮，不会改变当前目标；已删除或不再符合策略的人工禁用账号会安全跳过并计入已处理。是否把人工禁用账号纳入初始目标由“巡检人工禁用账号”控制。原始失败正文只在内存中参与判定，对外返回或持久化前会缩减为白名单原因码。

如果已知额度恢复时间且距离当前不超过 24 小时，账号列表会显示按当前语言格式化的具体自动启用时间；超过 24 小时则按向上取整后的“天 + 小时”显示倒计时。凭据刷新和成功请求证据驱动的恢复没有可靠时间戳，界面不会虚构具体恢复时间。

裸 `401`、`403`、`unauthorized`、`payment_required`、地区限制和模型权限失败只会进入人工复核。只有 `invalid_grant`、明确无效/撤销的 Token、账号或 Workspace 停用、明确额度耗尽，以及严格匹配的 xAI 凭据权限响应，才具备永久凭据处置的自动禁用资格。被动 Usage 的失败和恢复按真实事件计数；反复扫描同一条旧事件不会凑满连续阈值。可选的被动临时熔断可以在连续低/中置信失败达到阈值时执行有界临时禁用，但它不升级为高置信凭据失效，也不会进入自动删除。

| 设置 | 默认值 | 范围 / 行为 |
| --- | --- | --- |
| 定时巡检 | 关闭 | 关闭后仍可手动立即巡检。 |
| 巡检间隔 | 30 分钟 | 5-1,440 分钟。 |
| 连续异常 | 3 | 2-10 次符合条件的连续观察。 |
| 连续恢复 | 2 | 1-10 次连续恢复观察。 |
| 被动临时熔断 | 关闭 | 依赖自动禁用和自动启用；默认不处理含糊失败。 |
| 被动失败阈值 | 5 | 2-100 次同原因连续失败。 |
| 被动失败窗口 | 180 分钟 | 1-1,440 分钟；超过窗口或原因改变时重新计数。 |
| 临时禁用时长 | 15 分钟 | 1-1,440 分钟；新成功证据也可以提前恢复。 |
| 自动禁用 | 关闭 | 只通过宿主回调修改物理 Auth JSON 的 `disabled` 字段。 |
| 自动启用 | 关闭 | 只恢复仍由本巡检引擎持有禁用归属的账号。 |
| 自动删除 | 关闭 | 依赖自动禁用，首次开启必须单独确认风险。 |
| 删除宽限 | 168 小时 | 巡检禁用后 24-8,760 小时。 |
| 单次删除 | 10 | 每次鉴权执行最多 1-100 个到期候选。 |

归属式恢复保证插件不会启用操作员或其他系统禁用的账号。额度原因可以在已知重置时间后恢复；凭据问题需要禁用后的成功或刷新证据。被动临时熔断在精确阈值触发后立即排队巡检，并持久化归属、原因与恢复时间；到时仍存在强失败则不会错误启用，`model_not_found` 也不会触发账号熔断。操作员手动启用会撤销巡检归属，不会被下次扫描重新覆盖。

账号列表会复用同一份有界巡检证据。只有巡检持有禁用归属、自动启用已开启且存在明确额度重置时间时，列表才显示具体“预计自动启用”时间；凭据类问题显示等待重新授权/刷新或成功请求证据。停用账号会根据自动删除是否开启、宽限期、待执行状态和失败重试时间显示“建议删除”“等待删除宽限期”“等待自动删除”或“等待自动删除重试”。人工禁用不会显示为巡检自动禁用。列表摘要只含白名单原因/动作枚举、计数、策略开关和时间，不包含原始 Usage 失败正文或 Auth 来源细节。

自动删除比逐行手动删除更严格。只有明确的账号/Workspace 停用原因能创建候选；候选必须仍由巡检持有、仍禁用、唯一可编辑、指向同一份物理 `.json`、超过宽限期且仍判定停用。真正删除前，插件会再次列出账号、用最新信号重算健康状态、重读物理 JSON 并确认 `disabled: true`，再进入已有的 Revision 校验删除服务。账号恢复或来源变化会撤销候选；临时删除失败会保留候选，并至少延迟 5 分钟重试。

插件绝不持久化 Management Key，因此到期删除不能由无人值守后台协程执行。它只在已经鉴权的“巡检与自动化”页面打开时运行：进入页面执行一次，随后按有界间隔检查。非敏感策略、脱敏结果、归属元数据和最多 500 条动作记录保存在 `inspection-state.json`。

## 插件更新

Release 检查默认开启，每 24 小时一次，可配置 1-168 小时。后端只通过 `host.http.do` 请求固定的公开地址 `https://api.github.com/repos/Mxucc/cpa-account-config-manager/releases/latest`，不发送 Authorization Header 或账号凭据，拒绝 Draft、Prerelease 和无效语义版本；`update-state.json` 只保存策略、规范化最新版本、时间戳和稳定错误码。

已鉴权页面还会读取 CPA 插件商店 Registry 中的目标插件元数据。直接 GitHub 查询不受支持、被阻断或限流时，合法的插件商店稳定版本会成为更新提示来源，同时保留独立 GitHub 错误供排查；缺失或非法版本不会被采用。

发现新版本后页面会显示提示。真正安装委托给 CPA 鉴权插件商店，由宿主负责 Registry 来源、平台匹配、归档限制、Checksum 校验和最终落盘。自动安装默认关闭，首次开启必须确认，并且只在已鉴权巡检页面打开期间运行。原生动态库可能需要重启 CPA 才能启用新版本；安装失败或文件被占用时仍可手动重试。插件不会自行下载或替换自己的动态库，也不会保存浏览器中的 Management Key。

## 统一操作日志

打开“操作日志”可以统一查看账号管理器活动：单账号删除和模型可用性测试、批量修改与失败项重试、导入导出、默认策略保存/扫描/强制同步、巡检扫描及自动处置、更新检查和插件商店安装结果。进行中的批处理与强制同步按稳定 Job ID 原地更新；已持久化的巡检 Action ID 和扫描时间会被去重对账，轮询或重启后不会重复生成日志行。

工作台提供类别、状态、来源和文本筛选，20/50/100/200 条分页，逐字段详情弹窗，以及关联的内存任务仍存在时可用的“打开关联任务”控制。当前筛选结果可下载为 JSON、防公式注入 CSV 或 JSON Lines。清理属于需要确认的破坏操作，完成后会有意保留一条 `journal_clear`，使清理行为本身仍可审计。

操作日志不是凭据日志。它绝不保存 Management Key、原始 Auth JSON、Token、Cookie、API Key、代理或 Header 值、Patch 值、导入文档、凭据导出正文、原始请求/响应正文或浏览器任意文本。浏览器补记接口只接受固定 `update_install` 动作、三种固定结果和可选的规范化语义版本。

## 可编辑字段

| 字段 | 行为 |
| --- | --- |
| `disabled` | 调用 CLIProxyAPI 原生账号状态接口。 |
| `priority` | 勾选后替换账号优先级。 |
| `note` | 替换备注，并规范首尾空白。 |
| `prefix` | 替换或清空路由前缀。 |
| `proxy_url` | 支持空值、`direct`、`none`、`http`、`https`、`socks5`、`socks5h`。当前代理凭据不会返回浏览器。 |
| `websockets` | 明确开启或关闭 WebSocket 模式。 |
| `headers` | Header 名称大小写不敏感，可设置或移除。旧值不会加载到浏览器，Hop-by-hop Header 会被拒绝。 |

运行时账号、配置派生账号、虚拟子账号、失效记录、非 JSON 文件、重复物理来源和其他不支持的记录仍会显示，但标记为只读。

## 并发、冲突与部分失败

- 同一时间只有一个 Auth 文件写入路径占用写入槽位；普通批量任务、单账号删除、账号导入、后台缺失字段补齐和默认策略强制同步彼此串行。
- 所有目标先做 Preflight。无效、缺失、重复或只读目标跳过，其余可执行目标继续。
- 预览时记录物理 Auth JSON 的 SHA-256 Revision；每次写入前立即重读。Revision 改变时返回冲突，不覆盖新内容。
- 当前 ABI 没有宿主级 Compare-and-swap，因此最终 Revision 检查与 Management API 写入之间仍存在很窄的竞态窗口。插件不宣称跨文件严格事务。
- 某些账号失败不会回滚已经成功的账号。
- 后台默认策略始终只补缺失字段，使用共享写入槽位；槽位被占用时会短延迟重试，并在 `host.auth.save` 前再次读取 Auth。已存在的受管 Key 不会被覆盖。
- 进程重启后无法继续正在运行的任务，因为 Management Key 和 Patch 值不会落盘。保存为 running 的任务会标记为 `interrupted`；只有内存中的 Patch Intent 仍存在时才能精确“仅重试失败项”。

## 安全边界

- 账号列表、预览、错误和批量结果导出使用显式白名单字段并脱敏，不包含原始 Auth JSON、Token、Cookie、API Key、代理凭据或 Header 值。
- 使用量采集是被动的：只监听 CPA 原生 `usage.handle` 记录，不调用供应商额度接口，也不消费 CPA 的破坏性 `/usage-queue`；公开和落盘快照仅保留 Token 计数以及白名单内的 Codex 百分比、重置和窗口字段。
- 巡检只持久化规范化健康状态、目标账号 ID、原因码、计数、时间、来源、进度、归属元数据和脱敏动作记录。原始失败正文与响应 Header 只短暂存在于内存；含糊的鉴权或权限失败不能进入永久凭据处置或自动删除，只能在独立开启、达到连续阈值且受计时器约束时进入被动临时熔断。
- 自动启用受禁用归属约束；自动删除必须单独确认，只接受停用原因，经过宽限期并重读物理 Auth 状态，且只能使用当前已鉴权的浏览器请求执行。
- 更新检查只访问固定公开 GitHub API 且不携带凭据；安装始终由 CPA 鉴权插件商店完成。插件不接受任意 Release URL，也不自行写入动态库。
- 统一操作日志最多 2,000 条，只持久化显式公开字段；日志写入失败不阻断账号操作，界面会单独显示存储异常。清理必须确认并保留一条脱敏清理记录。
- 凭据导出是单独且必须显式选择的 Management 下载；附件正文有意包含目标系统凭据，带 `no-store`，受账号数和体积限制，并且不会写入插件状态或日志。
- 所有账号数据和写入接口都是 CLIProxyAPI 鉴权后的 Management 路由；未鉴权 Resource 路由只提供静态 HTML。
- 手动输入的 Management Key 只在 JavaScript 内存中保存。插件可以读取官方面板已经保存的同源状态，但不会自行把 Key 写入 Local Storage。
- Management Key 只在活动写操作内存中存在，操作结束时会显式清空。完整 Patch 值仅在待确认预览、活动任务或“仅重试失败项”仍需要时保留在进程内存中，绝不落盘。落盘内容只有脱敏状态、字段名、计数和通用错误。
- 批量启动、重试和删除确认优先使用请求携带的 Bearer Key。非浏览器调用也可以通过 `MANAGEMENT_PASSWORD` 或 `CPA_MANAGEMENT_KEY` 提供进程内回退；插件不会把这些环境变量写入磁盘。
- 插件内部调用只允许回环地址，响应读取上限为 64 KiB，且不会把上游响应正文拼进公开错误。
- 删除预览和确认都是鉴权 Management 路由。预览只在有界进程内存中保存私有路径/Revision，对外仅返回安全账号摘要；只有 CPA 确认成功后才消费预览，每次请求结束都会清空回环客户端中的 Management Key。
- 默认策略扫描和强制同步直接调用 `host.auth.list/get/save`，不读取也不保存浏览器 Management Key。原始 Auth JSON 只在进程内转换，不会由策略接口返回，也不会写入插件状态文件。
- 导入预览和确认都是鉴权 Management 路由。Multipart 文件、转换凭据和原始 JSON 不写入 `data_dir`；公开预览/结果使用显式白名单，预览在消费、过期、淘汰或插件关闭时清除内存。

不要把 CLIProxyAPI Management API 暴露到不可信网络，Management Key 仍需单独妥善保护。

## 备份与回滚

大批量修改前，请备份 CLIProxyAPI 的 `config.yaml` 和整个 Auth 目录。CPA 凭据导出可以作为当前筛选文件型 Auth JSON 的便携快照，但不会保留完整目录布局、原文件名、运行时账号或非 Auth 配置；完整目录备份仍是权威回滚来源。

普通元数据可通过再次批量填写旧值来反向修改。涉及秘密值时，应恢复备份的 Auth 文件并让 CLIProxyAPI 重新加载。

禁用或移除插件：

1. 把 `plugins.configs.cpa-account-config-manager.enabled` 改为 `false`。
2. 重启 CLIProxyAPI。
3. 进程停止后再删除动态库。Windows 加载中的 DLL 不能直接覆盖或删除。
4. 如不再需要，可删除 `data/cpa-account-config-manager` 下的脱敏状态文件。删除 `inspection-state.json` 会重置巡检策略、禁用归属和动作历史；删除 `update-state.json` 会重置更新偏好和上次检查；删除 `default-policy.json` 会重置回退策略/扫描缓存；删除 `operation-log.json` 会重置统一操作日志。

## Docker

把对应平台动态库和可写数据目录挂载进 CLIProxyAPI 容器：

```yaml
services:
  cpa:
    volumes:
      - ./plugins/linux/amd64/cpa-account-config-manager-v0.2.7.so:/app/plugins/linux/amd64/cpa-account-config-manager-v0.2.7.so:ro
      - ./plugin-data:/app/data/cpa-account-config-manager
```

路径以实际镜像为准。插件和 CLIProxyAPI 在同一容器内运行时，`http://127.0.0.1:8317` 通常仍是正确的内部 Management 地址。安装或升级动态库后需重启容器。

## Management 路由

以下 38 条鉴权路由都是 `/v0/management/plugins/cpa-account-config-manager` 下的固定精确路径：

- `GET /accounts`
- `POST /accounts/model-test`
- `POST /accounts/delete/preview`
- `POST /accounts/delete/start`
- `POST /batch/preview`
- `POST /batch/start`
- `GET /batch/status`
- `POST /batch/retry`
- `GET /export/accounts`
- `POST /export/accounts`
- `GET /export/results`
- `POST /import/preview`
- `POST /import/start`
- `GET /defaults`
- `PUT /defaults`
- `POST /defaults/scan`
- `POST /defaults/force/preview`
- `POST /defaults/force/start`
- `GET /defaults/force/status`
- `GET /inspection`
- `GET /inspection/live`
- `PUT /inspection`
- `POST /inspection/scan`
- `POST /inspection/scan/native`
- `POST /inspection/run`
- `POST /inspection/stop`
- `GET /inspection/results`
- `GET /inspection/export`
- `POST /inspection/review`
- `GET /inspection/actions`
- `POST /inspection/auto-delete`
- `GET /updates`
- `PUT /updates`
- `POST /updates/check`
- `GET /operations`
- `GET /operations/export`
- `DELETE /operations`
- `POST /operations/record`

静态界面由 `/v0/resource/plugins/cpa-account-config-manager/index.html` 提供。

## 开发与本地演示

构建要求：Go 1.24+、Node.js 22、npm，以及支持 `go build -buildmode=c-shared` 的本机 C 工具链。

项目协作和稳定后端输出统一以英文为源语言。后端元数据、错误、原因码、动作名和状态值都必须使用英文；前端代码使用类型化的英文语义消息 ID，简体中文、繁体中文、英文和俄语展示文案只放在各自语言目录中。不要再用翻译后的展示文本作为键，也不要在组件内按语言写条件分支。类型检查和源语言契约测试会持续校验这些约束。

```bash
cd web
npm ci
cd ..
make verify
make package VERSION=0.2.7
```

如果本地构建需要在插件元数据中显示仓库链接，可给 `make build` 或 `make package` 传入 `REPOSITORY=https://github.com/<owner>/cpa-account-config-manager`。GitHub Actions 会自动注入实际仓库地址。

`make package` 在 `dist/` 生成构建阶段动态库，在 `dist/release/` 生成 CLIProxyAPI 插件商店兼容的 ZIP 和 `.sha256`。ZIP 根目录只包含一个可直接安装的 `<id>-v<version>.<ext>` 动态库。

本地 UI 演示需要两个终端：

```bash
cd web
MOCK_CPA_PORT=8318 npm run mock
```

```bash
cd web
VITE_CPA_BASE=http://127.0.0.1:8318 npm run dev
```

打开 `http://127.0.0.1:5175`，CPA 地址保持页面同源，Management Key 使用 `demo-key`。Mock 仅包含合成账号，并模拟行详情/编辑/删除、模型可用性测试、批量任务、默认策略进度、巡检与更新流程及混合 JSON/ZIP 导入，不会修改真实凭据。

## 发布

推送 `vX.Y.Z` Tag 后，GitHub Actions 会执行完整验证，并在原生 Runner 上构建四个平台，注入 `X.Y.Z` 插件版本。每个平台 ZIP 根目录包含一个 `<id>-v<version>.<ext>` 动态库，同时发布对应 `.zip.sha256` 和插件商店需要的聚合 `checksums.txt`。

## 友链

- [LINUX DO](https://linux.do/) - 我们认可并感谢的社区。

## 致谢

- 本插件的巡检功能，尤其是快速/全量巡检划分、账号健康判定、归属式自动恢复，以及自动禁用、自动启用和自动删除的保守安全边界，主要参考 [`seakee/CPA-Manager-Plus`](https://github.com/seakee/CPA-Manager-Plus) 的产品设计，Copyright 2026 Seakee。本项目针对 CLIProxyAPI 原生插件运行时和 Management 鉴权边界独立重新实现这些思路。CPA Manager Plus 使用 MIT License，完整声明见 [第三方声明](THIRD_PARTY_NOTICES.md)。
- 全量、增量、分类范围和失败重试巡检、停止控制、进度持久化、行级/批量处置及脱敏结果导出同时参考 [`ywddd/grok-inspection`](https://github.com/ywddd/grok-inspection)。本插件保留更严格的 CPA 鉴权、禁用归属和修订版本删除校验。Grok Inspection 使用 MIT License，完整声明见 [第三方声明](THIRD_PARTY_NOTICES.md)。
- Codex 429 恢复窗口判定与展示参考 [`ysxk/codex-429-autoban`](https://github.com/ysxk/codex-429-autoban)，实时额度/Token 可视化和账号池操作入口参考 [`zhumengling/codex-token-usage`](https://github.com/zhumengling/codex-token-usage)。本插件在既有的脱敏、Management 鉴权和归属式恢复边界内独立适配这些思路。两个项目均使用 MIT License，完整声明见 [第三方声明](THIRD_PARTY_NOTICES.md)。

## License

MIT。参见 [LICENSE](LICENSE) 和 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。

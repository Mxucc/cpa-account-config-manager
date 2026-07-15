# CPA Account Config Manager

[English](README.md)

`cpa-account-config-manager` 是一个独立发布的 CLIProxyAPI 原生插件，用于集中查看、筛选、导出并批量修改账号配置，减少逐个编辑 Auth JSON 的重复操作。

插件架构参考 [`ywddd/grok-inspection`](https://github.com/ywddd/grok-inspection)，账号选择和字段按需启用的交互参考 sub2api，但首个版本不修改 CLIProxyAPI 核心程序。

## 功能

- 账号列表、搜索以及 Provider、状态、启用状态、可编辑性筛选。
- 本页多选、明确的“已选账号”范围，以及固定快照的“全部筛选结果”范围。
- 批量启用、批量禁用，以及 `priority`、`note`、`prefix`、`proxy_url`、`websockets`、自定义 Header 的按字段批量编辑。
- 写入前由服务端生成预览，显示目标数、可执行数、只读数、缺失数和物理文件数。
- 后台异步执行、受限并发、逐账号结果、Revision 冲突检测、部分成功继续和仅重试失败项。
- 导出脱敏后的筛选账号和任务结果。
- React 单文件内嵌界面，支持官方 Management Center 的主题和同源认证状态。

MVP 不提供删除账号、上传或下载原始 Auth JSON、刷新 Token、OAuth 重新授权、直接编辑凭据、额度巡检和调度功能。

## 兼容性

插件使用 CLIProxyAPI 原生插件 ABI/Schema v1，需要宿主具备：

- 原生插件发现、Management 路由和 Resource 路由；
- `host.auth.list` 与 `host.auth.get` 回调；
- `PATCH /v0/management/auth-files/status`；
- `PATCH /v0/management/auth-files/fields`。

插件不导入 CLIProxyAPI Go 包，也不要求修改 CLIProxyAPI 可执行文件。

发布矩阵：

| 平台 | 架构 | 动态库 | 发布压缩包 |
| --- | --- | --- | --- |
| Linux | amd64 | `cpa-account-config-manager.so` | `cpa-account-config-manager_*_linux_amd64.zip` |
| Linux | arm64 | `cpa-account-config-manager.so` | `cpa-account-config-manager_*_linux_arm64.zip` |
| macOS | arm64 | `cpa-account-config-manager.dylib` | `cpa-account-config-manager_*_darwin_arm64.zip` |
| Windows | amd64 | `cpa-account-config-manager.dll` | `cpa-account-config-manager_*_windows_amd64.zip` |

动态库与平台、架构强绑定，不能混用。

## 安装

### 1. 下载并校验

从 [Releases](../../releases) 下载与 CLIProxyAPI 宿主匹配的 ZIP，同时下载 `checksums.txt` 或对应的 `.zip.sha256`。

Linux：

```bash
sha256sum -c cpa-account-config-manager_0.1.0_linux_amd64.zip.sha256
```

macOS：

```bash
shasum -a 256 -c cpa-account-config-manager_0.1.0_darwin_arm64.zip.sha256
```

Windows PowerShell：

```powershell
Get-FileHash .\cpa-account-config-manager_0.1.0_windows_amd64.zip -Algorithm SHA256
Get-Content .\cpa-account-config-manager_0.1.0_windows_amd64.zip.sha256
```

### 2. 放置动态库

解压后，将动态库放进 CLIProxyAPI 插件目录。推荐使用宿主优先扫描的平台子目录：

```text
plugins/linux/amd64/cpa-account-config-manager.so
plugins/linux/arm64/cpa-account-config-manager.so
plugins/darwin/arm64/cpa-account-config-manager.dylib
plugins/windows/amd64/cpa-account-config-manager.dll
```

Linux/macOS 上确保 CLIProxyAPI 服务账号可读、可执行：

```bash
chmod 755 plugins/linux/amd64/cpa-account-config-manager.so
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
      workers: 6
      data_dir: data/cpa-account-config-manager
      management_base_url: http://127.0.0.1:8317
```

重启 CLIProxyAPI 后，Management Center 的插件菜单中应出现 **Account Config Manager**。

如果 CLIProxyAPI 不是监听 `8317`，请把 `management_base_url` 改成 CLIProxyAPI 进程内部可访问的回环地址。插件只接受 `http://` 或 `https://` 的 `localhost`、`127.0.0.0/8`、`::1`，拒绝远程主机、凭据、路径、查询参数和 Fragment，避免 Management Key 被转发到外部地址。

## 配置项

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `workers` | `6` | 同时执行的账号修改数。小于 1 时恢复为 6，大于 16 时限制为 16。 |
| `data_dir` | `data/cpa-account-config-manager` | 保存脱敏终态任务结果的目录。字段为空时读取 `CPA_ACCOUNT_CONFIG_MANAGER_DATA_DIR`。 |
| `management_base_url` | `http://127.0.0.1:8317` | 插件调用 CLIProxyAPI 原生写入接口时使用的回环地址。还支持 `CPA_MANAGEMENT_BASE_URL`、`CPA_BASE_URL`、`PORT`、`CPA_PORT`。 |

同一对象里的 `enabled` 和 `priority` 由 CLIProxyAPI 插件宿主管理；界面中编辑的账号 `priority` 是另一项账号字段。

## 权限要求

CLIProxyAPI 进程需要：

- 对动态库有读取和执行权限；
- 对 Auth 目录有读写权限，因为真正的字段持久化由 CLIProxyAPI 原生 Management API 完成；
- 对 `data_dir` 有读写权限。

支持权限位的平台上，插件以 `0700` 创建数据目录，通过临时文件原子替换 `results.json`，结果文件权限为 `0600`。建议让 CLIProxyAPI 和插件使用同一个非 root 服务账号运行。

## 使用流程

1. 在 CLIProxyAPI Management Center 打开 **Account Config Manager**。
2. 如果官方面板已经选择“记住密码”，同源 iframe 会复用面板认证；否则输入 CPA 地址和 Management Key。手动输入的 Key 只保存在浏览器内存中，刷新即清除。
3. 筛选账号，并明确选择“已选账号”或“全部筛选”。
4. 点击批量启用、批量禁用或批量编辑。批量编辑中只有勾选的字段才会进入 Patch。
5. 检查服务端预览中的目标数、只读/缺失项、变更字段和告警。
6. 启动任务，查看逐账号进度。出现部分失败后，可用“仅重试失败项”重新处理失败或冲突账号。

预览有效期为 5 分钟。执行时消费的是固定目标快照，后续新匹配筛选条件的账号不会被静默加入。

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

- 同一时间只运行一个写入任务。
- 所有目标先做 Preflight。无效、缺失、重复或只读目标跳过，其余可执行目标继续。
- 预览时记录物理 Auth JSON 的 SHA-256 Revision；每次写入前立即重读。Revision 改变时返回冲突，不覆盖新内容。
- 当前 ABI 没有宿主级 Compare-and-swap，因此最终 Revision 检查与 Management API 写入之间仍存在很窄的竞态窗口。插件不宣称跨文件严格事务。
- 某些账号失败不会回滚已经成功的账号。
- 进程重启后无法继续正在运行的任务，因为 Management Key 和 Patch 值不会落盘。保存为 running 的任务会标记为 `interrupted`；只有内存中的 Patch Intent 仍存在时才能精确“仅重试失败项”。

## 安全边界

- 账号列表和导出使用显式白名单字段并脱敏。原始 Auth JSON、Token、Cookie、API Key、代理凭据、Header 值不会越过插件 API 边界。
- 所有账号数据和写入接口都是 CLIProxyAPI 鉴权后的 Management 路由；未鉴权 Resource 路由只提供静态 HTML。
- 手动输入的 Management Key 只在 JavaScript 内存中保存。插件可以读取官方面板已经保存的同源状态，但不会自行把 Key 写入 Local Storage。
- Management Key 只在活动任务内存中存在，任务结束时会显式清空。完整 Patch 值仅在待确认预览、活动任务或“仅重试失败项”仍需要时保留在进程内存中，绝不落盘。落盘内容只有脱敏状态、字段名、计数和通用错误。
- 批量启动和重试优先使用请求携带的 Bearer Key。非浏览器调用也可以通过 `MANAGEMENT_PASSWORD` 或 `CPA_MANAGEMENT_KEY` 提供进程内回退；插件不会把这些环境变量写入磁盘。
- 插件内部调用只允许回环地址，响应读取上限为 64 KiB，且不会把上游响应正文拼进公开错误。

不要把 CLIProxyAPI Management API 暴露到不可信网络，Management Key 仍需单独妥善保护。

## 备份与回滚

大批量修改前，请备份 CLIProxyAPI 的 `config.yaml` 和整个 Auth 目录。插件导出是脱敏审计数据，不是完整恢复备份，尤其无法还原旧代理凭据和旧 Header 值。

普通元数据可通过再次批量填写旧值来反向修改。涉及秘密值时，应恢复备份的 Auth 文件并让 CLIProxyAPI 重新加载。

禁用或移除插件：

1. 把 `plugins.configs.cpa-account-config-manager.enabled` 改为 `false`。
2. 重启 CLIProxyAPI。
3. 进程停止后再删除动态库。Windows 加载中的 DLL 不能直接覆盖或删除。
4. 如不需要历史，可删除 `data/cpa-account-config-manager/results.json`；该文件只包含脱敏任务结果。

## Docker

把对应平台动态库和可写数据目录挂载进 CLIProxyAPI 容器：

```yaml
services:
  cpa:
    volumes:
      - ./plugins/linux/amd64/cpa-account-config-manager.so:/app/plugins/linux/amd64/cpa-account-config-manager.so:ro
      - ./plugin-data:/app/data/cpa-account-config-manager
```

路径以实际镜像为准。插件和 CLIProxyAPI 在同一容器内运行时，`http://127.0.0.1:8317` 通常仍是正确的内部 Management 地址。安装或升级动态库后需重启容器。

## 开发与本地演示

构建要求：Go 1.24+、Node.js 22、npm，以及支持 `go build -buildmode=c-shared` 的本机 C 工具链。

```bash
cd web
npm ci
cd ..
make verify
make package VERSION=0.1.0
```

如果本地构建需要在插件元数据中显示仓库链接，可给 `make build` 或 `make package` 传入 `REPOSITORY=https://github.com/<owner>/cpa-account-config-manager`。GitHub Actions 会自动注入实际仓库地址。

`make package` 在 `dist/` 生成动态库，在 `dist/release/` 生成 CLIProxyAPI 插件商店兼容的 ZIP 和 `.sha256`。

本地 UI 演示需要两个终端：

```bash
cd web
MOCK_CPA_PORT=8318 npm run mock
```

```bash
cd web
VITE_CPA_BASE=http://127.0.0.1:8318 npm run dev
```

打开 `http://127.0.0.1:5175`，CPA 地址保持页面同源，Management Key 使用 `demo-key`。Mock 仅包含合成账号并模拟任务进度，不会修改真实凭据。

## 发布

推送 `vX.Y.Z` Tag 后，GitHub Actions 会执行完整验证，并在原生 Runner 上构建四个平台，注入 `X.Y.Z` 插件版本，发布每个平台的 ZIP、对应 `.zip.sha256` 和插件商店需要的聚合 `checksums.txt`。

## License

MIT。参见 [LICENSE](LICENSE) 和 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。

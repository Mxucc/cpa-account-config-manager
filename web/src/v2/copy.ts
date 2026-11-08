import type { Locale } from "../i18n";

// The new shell keeps its words next to the screens it owns instead of growing
// the shared catalog. Both languages are complete, and a page reads only this
// object, so a missing translation is a type error rather than a silent fallback.
export interface Copy {
  nav: { overview: string; accounts: string; providers: string; quota: string; usage: string; routing: string; automation: string; settings: string };
  common: {
    loading: string; error: string; retry: string; empty: string; refresh: string; close: string;
    unknown: string; notReported: string; save: string; cancel: string; delete: string; details: string;
    all: string; search: string; updated: string; viewAll: string; source: string;
  };
  overview: { title: string; subtitle: string; accounts: string; ready: string; limited: string; invalid: string; spend: string; providers: string; hotspots: string; recent: string };
  accounts: { title: string; subtitle: string; provider: string; status: string; plan: string; quota: string; billing: string; models: string; actions: string; refresh: string; test: string; bind: string; empty: string; filterProvider: string; filterStatus: string };
  providers: { title: string; subtitle: string; accounts: string; capabilities: string; open: string; import: string; importHint: string; importDone: string; hooks: string; hooksHint: string; hooksNeedsBun: string; hooksOff: string; tabs: { accounts: string; quota: string; models: string; routing: string; advanced: string } };
  // The community market: one click imports a Magpie plugin by id, without the
  // operator handling a package.json.
  market: {
    title: string; hint: string; search: string; import: string; importing: string; imported: string;
    empty: string; unreachable: string; noVersion: string; advanced: string; advancedHint: string;
    providersOnly: string; hidden: string;
    reasons: { unreachable: string; timeout: string; unreadable: string; unavailable: string };
  };
  quota: { title: string; subtitle: string; window: string; used: string; remaining: string; reset: string; limit: string; wallet: string; aside: string; notes: { referencePricedUsage: string } };
  usage: { title: string; subtitle: string; tokens: string; cost: string; mode: string; priceSource: string; metered: string; subscription: string; wallet: string };
  // A subscription cycle is a wire enum; the page owns its words.
  cycle: { monthly: string; weekly: string; rolling: string; none: string };
  routing: { title: string; subtitle: string; published: string; unpublished: string; gaps: string; models: string; channel: string; advanced: string };
  automation: { title: string; subtitle: string; inspection: string; policies: string; notifications: string; retry: string; proxy: string };
  settings: { title: string; subtitle: string; general: string; operations: string; risk: string; update: string };
  status: { ready: string; quota_limited: string; credential_invalid: string; repairing: string; disabled: string; unknown: string };
  statusNotes: { quotaLimited: string; credentialRejected: string };
  // A provider kind is a wire enum; the page owns its words.
  kinds: { oauth: string; subscription: string; wallet: string; channel: string };
  // Capability keys are the same identifiers the API uses.
  capabilities: { login: string; quota: string; models: string; binding: string; billing: string; refresh: string; test: string; host_auth: string };
  // The server sends English provider copy and English metric names; the shell
  // owns its language, so both are looked up here by a stable key and fall back
  // to whatever the server said.
  providerDescriptions: Record<string, string>;
  metricLabels: Record<string, string>;
  // Quota window labels also come from the provider; known ones are translated
  // and an unknown label is shown as the provider wrote it.
  windowLabels: Record<string, string>;
  billing_allowance: string;
  billing_used: string;
  billing_remaining: string;
  auth: { title: string; baseURL: string; key: string; signIn: string; signOut: string; rejected: string };
}

const zh: Copy = {
  nav: { overview: "总览", accounts: "账号", providers: "供应商", quota: "额度与计划", usage: "用量与计费", routing: "路由与模型", automation: "自动化", settings: "日志与设置" },
  common: {
    loading: "加载中…", error: "加载失败", retry: "重试", empty: "暂无数据", refresh: "刷新", close: "关闭",
    unknown: "未知", notReported: "未上报", save: "保存", cancel: "取消", delete: "删除", details: "详情",
    all: "全部", search: "搜索", updated: "更新于", viewAll: "查看全部", source: "来源",
  },
  overview: { title: "总览", subtitle: "所有供应商的账号健康、订阅支出与额度热点", accounts: "账号总数", ready: "可用", limited: "额度受限", invalid: "凭据异常", spend: "月度订阅支出", providers: "供应商", hotspots: "额度热点", recent: "供应商分布" },
  accounts: { title: "账号", subtitle: "同一套字段与操作覆盖所有供应商", provider: "供应商", status: "状态", plan: "计划", quota: "额度", billing: "计费", models: "模型", actions: "操作", refresh: "刷新", test: "测试", bind: "发布渠道", empty: "没有符合条件的账号", filterProvider: "按供应商筛选", filterStatus: "按状态筛选" },
  providers: { title: "供应商", subtitle: "内置供应商、Magpie/OpenCode 插件导入与能力对照", accounts: "账号", capabilities: "能力", open: "打开", import: "导入插件", importHint: "粘贴 Magpie/OpenCode 供应商插件的 package.json。导入只登记定义，不执行插件代码。", importDone: "已导入供应商", hooks: "执行插件钩子", hooksHint: "开启后会运行已导入插件的模型目录与额度钩子；不接管请求路由。", hooksNeedsBun: "未检测到 Bun，钩子不会执行。", hooksOff: "钩子执行已关闭，导入项只登记定义。", tabs: { accounts: "账号", quota: "额度与计划", models: "模型与价格", routing: "路由", advanced: "兼容（旧界面）" } },
  market: {
    title: "Magpie 社区插件", hint: "从社区插件仓库直接导入，不需要手填 package.json。清单由插件服务端读取，导入只登记定义、不执行插件代码。",
    search: "搜索插件", import: "导入", importing: "导入中…", imported: "已导入",
    empty: "没有匹配的插件", unreachable: "无法读取社区插件列表", noVersion: "版本未读取",
    advanced: "高级：粘贴 package.json", advancedHint: "离线或自建插件才需要手填清单。",
    providersOnly: "仅列出供应商插件", hidden: "已隐藏非供应商条目",
    reasons: { unreachable: "无法连接社区插件仓库，请检查服务器出网。", timeout: "读取社区插件仓库超时。", unreadable: "社区插件仓库返回的内容无法解析。", unavailable: "社区插件列表当前不可用。" },
  },
  quota: { title: "额度与计划", subtitle: "统一窗口、重置时间与订阅周期", window: "窗口", used: "已用", remaining: "剩余", reset: "重置", limit: "上限", wallet: "钱包余额", aside: "不参与路由", notes: { referencePricedUsage: "参考价折算用量" } },
  usage: { title: "用量与计费", subtitle: "订阅、按量与钱包三种计费口径分开呈现", tokens: "Token", cost: "成本", mode: "计费模式", priceSource: "价格来源", metered: "按量", subscription: "订阅", wallet: "钱包" },
  cycle: { monthly: "按月", weekly: "按周", rolling: "滚动", none: "无周期" },
  routing: { title: "路由与模型", subtitle: "账号发布到 CPA 渠道的状态与模型缺口", published: "已发布", unpublished: "未发布", gaps: "模型缺口", models: "模型数", channel: "渠道", advanced: "路由高级设置" },
  automation: { title: "自动化", subtitle: "巡检、策略、通知、重试与代理", inspection: "巡检", policies: "策略", notifications: "通知", retry: "自动重试", proxy: "代理档案" },
  settings: { title: "日志与设置", subtitle: "操作日志、风险控制、插件更新与实验性功能", general: "通用设置", operations: "操作日志", risk: "风险控制", update: "更新" },
  status: { ready: "可用", quota_limited: "额度受限", credential_invalid: "凭据异常", repairing: "自动修复中", disabled: "已停用", unknown: "未知" },
  statusNotes: { quotaLimited: "额度窗口已用尽，窗口重置后自动恢复", credentialRejected: "渠道拒绝了该凭据，正在自动修复" },
  kinds: { oauth: "登录授权", subscription: "订阅", wallet: "钱包", channel: "渠道" },
  capabilities: { login: "登录", quota: "额度", models: "模型", binding: "发布渠道", billing: "计费", refresh: "刷新", test: "测试", host_auth: "宿主鉴权" },
  providerDescriptions: {
    codex: "通过 CPA 管理的 OpenAI Codex 凭据，含 5 小时与 7 天额度窗口、计划信息与客户端身份策略。",
    "opencode-go": "OpenCode Go 工作区：每月额度按滚动、每周与每月三个窗口拆分。",
    "opencode-zen": "OpenCode Zen 凭据：按量计费并抵扣预付余额，模型目录按账号读取。",
    "cline-pass": "Cline 账号：ClinePass 的 5 小时、每周与每月上限，加上钱包余额，并自动修复凭据。",
    "ai-provider": "不归属于上述产品的 OpenAI 兼容 CPA 渠道：只统计模型、运行时用量与成本。",
  },
  metricLabels: {
    active_reset_count: "可用重置次数",
    balance: "余额",
  },
  windowLabels: {
    "5 hours": "5 小时",
    "5 hour": "5 小时",
    "5-hour": "5 小时",
    "7 days": "7 天",
    "7 day": "7 天",
    "7-day": "7 天",
    weekly: "每周",
    monthly: "按月",
    month: "按月",
    rolling: "滚动",
    balance: "余额",
    wallet: "钱包",
  },
  billing_allowance: "额度上限",
  billing_used: "已用额度",
  billing_remaining: "剩余额度",
  auth: { title: "管理认证", baseURL: "管理接口地址", key: "管理密钥", signIn: "登录", signOut: "退出", rejected: "管理密钥被拒绝。" },
};

const en: Copy = {
  nav: { overview: "Overview", accounts: "Accounts", providers: "Providers", quota: "Quota & plans", usage: "Usage & billing", routing: "Routing & models", automation: "Automation", settings: "Logs & settings" },
  common: {
    loading: "Loading…", error: "Failed to load", retry: "Retry", empty: "Nothing here", refresh: "Refresh", close: "Close",
    unknown: "Unknown", notReported: "Not reported", save: "Save", cancel: "Cancel", delete: "Delete", details: "Details",
    all: "All", search: "Search", updated: "Updated", viewAll: "View all", source: "Source",
  },
  overview: { title: "Overview", subtitle: "Account health, subscription spend and quota hotspots across every provider", accounts: "Accounts", ready: "Ready", limited: "Quota limited", invalid: "Credential issues", spend: "Monthly subscription spend", providers: "Providers", hotspots: "Quota hotspots", recent: "By provider" },
  accounts: { title: "Accounts", subtitle: "One set of fields and actions for every provider", provider: "Provider", status: "Status", plan: "Plan", quota: "Quota", billing: "Billing", models: "Models", actions: "Actions", refresh: "Refresh", test: "Test", bind: "Publish channel", empty: "No account matches these filters", filterProvider: "Filter by provider", filterStatus: "Filter by status" },
  providers: { title: "Providers", subtitle: "Built-in vendors, imported Magpie/OpenCode plugins and their capabilities", accounts: "accounts", capabilities: "Capabilities", open: "Open", import: "Import plugin", importHint: "Paste a Magpie/OpenCode provider plugin package.json. Import only records the definition; no plugin code runs.", importDone: "Imported provider", hooks: "Run plugin hooks", hooksHint: "Runs the model and allowance hooks of imported plugins. Request routing is never taken over.", hooksNeedsBun: "Bun was not found; hooks will not run.", hooksOff: "Hook execution is off; imports only record a definition.", tabs: { accounts: "Accounts", quota: "Quota & plans", models: "Models & pricing", routing: "Routing", advanced: "Legacy console" } },
  market: {
    title: "Magpie community plugins", hint: "Import straight from the community registry — no package.json to paste. The plugin reads the registry server side, and an import only records a definition; no plugin code runs.",
    search: "Search plugins", import: "Import", importing: "Importing…", imported: "Imported",
    empty: "No plugin matches", unreachable: "The community plugin list is unavailable", noVersion: "version not read",
    advanced: "Advanced: paste a package.json", advancedHint: "Only an offline or in-house plugin needs a pasted manifest.",
    providersOnly: "Provider plugins only", hidden: "other registry entries hidden",
    reasons: { unreachable: "The community plugin registry is unreachable; check the server's outbound network.", timeout: "Reading the community plugin registry timed out.", unreadable: "The community plugin registry returned something unreadable.", unavailable: "The community plugin list is currently unavailable." },
  },
  quota: { title: "Quota & plans", subtitle: "Unified windows, reset times and subscription cycles", window: "Window", used: "Used", remaining: "Remaining", reset: "Resets", limit: "Limit", wallet: "Wallet balance", aside: "Excluded from routing", notes: { referencePricedUsage: "Reference-priced usage" } },
  usage: { title: "Usage & billing", subtitle: "Subscription, metered and wallet billing kept apart", tokens: "Tokens", cost: "Cost", mode: "Billing", priceSource: "Price source", metered: "Metered", subscription: "Subscription", wallet: "Wallet" },
  cycle: { monthly: "Monthly", weekly: "Weekly", rolling: "Rolling", none: "No cycle" },
  routing: { title: "Routing & models", subtitle: "Channel publication state and model gaps", published: "Published", unpublished: "Not published", gaps: "Model gaps", models: "Models", channel: "Channel", advanced: "Routing advanced settings" },
  automation: { title: "Automation", subtitle: "Inspection, policies, notifications, retries and proxies", inspection: "Inspection", policies: "Policies", notifications: "Notifications", retry: "Auto retry", proxy: "Proxy profiles" },
  settings: { title: "Logs & settings", subtitle: "Operation journal, risk control, plugin updates and experiments", general: "General", operations: "Operation journal", risk: "Risk control", update: "Updates" },
  status: { ready: "Ready", quota_limited: "Quota limited", credential_invalid: "Credential invalid", repairing: "Repairing", disabled: "Disabled", unknown: "Unknown" },
  statusNotes: { quotaLimited: "The gateway limited this account; it recovers after the window ends", credentialRejected: "The gateway rejected this credential; automatic repair is running" },
  kinds: { oauth: "OAuth", subscription: "Subscription", wallet: "Wallet", channel: "Channel" },
  capabilities: { login: "Login", quota: "Quota", models: "Models", binding: "Channel binding", billing: "Billing", refresh: "Refresh", test: "Test", host_auth: "Host auth" },
  providerDescriptions: {
    codex: "OpenAI Codex credentials managed through CPA, with five-hour and seven-day quota windows, plan metadata and client identity policy.",
    "opencode-go": "OpenCode Go workspaces: a monthly allowance split across rolling, weekly and monthly windows.",
    "opencode-zen": "OpenCode Zen credentials: metered usage against a prepaid balance, with a per-account model catalog.",
    "cline-pass": "Cline accounts: ClinePass's five-hour, weekly and monthly limits plus the wallet balance, with automatic credential repair.",
    "ai-provider": "OpenAI-compatible CPA channels that are not one of the managed products: models, runtime usage and cost only.",
  },
  metricLabels: {
    active_reset_count: "Active reset credits",
    balance: "Balance",
  },
  windowLabels: {},
  billing_allowance: "Allowance",
  billing_used: "Used",
  billing_remaining: "Remaining",
  auth: { title: "Management authentication", baseURL: "Management base URL", key: "Management key", signIn: "Sign in", signOut: "Sign out", rejected: "The management key was rejected." },
};

const catalogs: Record<string, Copy> = { en, "zh-CN": zh };

// A catalog lookup, not a locale comparison: adding a catalog needs no branch.
export const copyFor = (locale: Locale): Copy => catalogs[locale] ?? zh;

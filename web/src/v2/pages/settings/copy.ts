import { useMemo } from "react";
import { useI18n } from "../../../i18n";
import type { Locale } from "../../../i18n";

// Settings display text, isolated so no runtime file holds localized strings and
// no runtime file branches on a locale.
export interface SettingsCopy {
  general: string;
  operations: string;
  risk: string;
  experiments: string;
  plugins: string;
  updates: string;
  save: string;
  metrics: {
    retained: string;
    segments: string;
    page: string;
    observed: string;
    blocked: string;
    bun: string;
    bunReady: string;
    bunMissing: string;
  };
  saved: string;
  dirty: string;
  enabled: string;
  scanInterval: string;
  priority: string;
  concurrency: string;
  proxyURL: string;
  prefix: string;
  note: string;
  websockets: string;
  providerNames: string;
  channel: string;
  name: string;
  baseURL: string;
  none: string;
  filterCategory: string;
  filterStatus: string;
  search: string;
  retention: string;
  retentionHint: string;
  export: string;
  clear: string;
  confirmClear: string;
  category: string;
  action: string;
  status: string;
  startedAt: string;
  targets: string;
  blockedKeywords: string;
  blockedKeywordsHint: string;
  blockStatus: string;
  blockMessage: string;
  retentionDays: string;
  maxEvents: string;
  events: string;
  hashes: string;
  clearEvents: string;
  clearHashes: string;
  weeklyOverdraft: string;
  agentIdentity: string;
  autoModelWhitelist: string;
  sub2apiCredits: string;
  whitelist: string;
  accounts: string;
  limited: string;
  hookExecution: string;
  hookExecutionHint: string;
  registry: string;
  registryHint: string;
  imported: string;
  pluginFile: string;
  pluginFileHint: string;
  currentVersion: string;
  latestVersion: string;
  cpaVersion: string;
  checkUpdates: string;
  install: string;
  reloadThroughStore: string;
  restartRequired: string;
  canInstall: string;
  appliedVersion: string;
  upgradeHint: string;
  cancel: string;
  invalidResponse: string;
  confirm: string;
}

const zh: SettingsCopy = {
  general: "通用设置",
  operations: "操作日志",
  risk: "风险控制",
  experiments: "实验性功能",
  plugins: "插件配置",
  updates: "更新",
  save: "保存",
  metrics: {
    retained: "保留条数",
    segments: "归档分段",
    page: "页码",
    observed: "已观察",
    blocked: "已拦截",
    bun: "Bun 运行时",
    bunReady: "已就绪",
    bunMissing: "未安装",
  },
  saved: "已保存",
  dirty: "有未保存的改动",
  enabled: "启用",
  scanInterval: "扫描间隔（秒）",
  priority: "优先级",
  concurrency: "并发上限",
  proxyURL: "代理 URL",
  prefix: "前缀",
  note: "备注",
  websockets: "WebSockets",
  providerNames: "AI 供应商命名",
  channel: "渠道",
  name: "名称",
  baseURL: "Base URL",
  none: "暂无数据",
  filterCategory: "分类",
  filterStatus: "状态",
  search: "搜索",
  retention: "扩展历史",
  retentionHint: "开启后保留更多归档分段，磁盘占用随之增加。",
  export: "导出",
  clear: "清空日志",
  confirmClear: "确认清空",
  category: "分类",
  action: "动作",
  status: "状态",
  startedAt: "开始时间",
  targets: "目标",
  blockedKeywords: "拦截关键词",
  blockedKeywordsHint: "每行一个关键词。",
  blockStatus: "拦截状态码",
  blockMessage: "拦截提示",
  retentionDays: "事件保留天数",
  maxEvents: "事件上限",
  events: "事件",
  hashes: "指纹",
  clearEvents: "清空事件",
  clearHashes: "清空指纹",
  weeklyOverdraft: "7 天上限透支",
  agentIdentity: "Agent 身份",
  autoModelWhitelist: "自动模型白名单",
  sub2apiCredits: "Sub2API 额度计费",
  whitelist: "白名单事件",
  accounts: "账号",
  limited: "受限",
  hookExecution: "执行插件钩子",
  hookExecutionHint: "开启后会运行已导入插件的模型目录与额度钩子；不接管请求路由。",
  registry: "社区插件来源",
  registryHint: "插件服务端读取该仓库的清单；浏览器不直连外网。",
  imported: "已导入供应商",
  pluginFile: "插件库文件",
  pluginFileHint: "自更新写入的目标文件；留空表示由宿主探测。",
  currentVersion: "当前版本",
  latestVersion: "最新版本",
  cpaVersion: "CPA 版本",
  checkUpdates: "检查更新",
  install: "安装更新",
  reloadThroughStore: "通过插件商店重载",
  restartRequired: "需要重启",
  canInstall: "可安装",
  appliedVersion: "已应用版本",
  upgradeHint: "安装会替换插件库文件；宿主可能需要重启或重载后才能生效。",
  cancel: "取消",
  invalidResponse: "服务端返回的数据结构无效",
  confirm: "确认",
};

const en: SettingsCopy = {
  general: "General",
  operations: "Operation journal",
  risk: "Risk control",
  experiments: "Experiments",
  plugins: "Plugin configuration",
  updates: "Updates",
  save: "Save",
  metrics: {
    retained: "Retained",
    segments: "Archived segments",
    page: "Page",
    observed: "Observed",
    blocked: "Blocked",
    bun: "Bun runtime",
    bunReady: "ready",
    bunMissing: "missing",
  },
  saved: "Saved",
  dirty: "Unsaved changes",
  enabled: "Enabled",
  scanInterval: "Scan interval (seconds)",
  priority: "Priority",
  concurrency: "Concurrency limit",
  proxyURL: "Proxy URL",
  prefix: "Prefix",
  note: "Note",
  websockets: "WebSockets",
  providerNames: "AI provider names",
  channel: "Channel",
  name: "Name",
  baseURL: "Base URL",
  none: "Nothing here",
  filterCategory: "Category",
  filterStatus: "Status",
  search: "Search",
  retention: "Extended history",
  retentionHint: "Keeps more archived segments; disk usage grows with it.",
  export: "Export",
  clear: "Clear journal",
  confirmClear: "Confirm clear",
  category: "Category",
  action: "Action",
  status: "Status",
  startedAt: "Started",
  targets: "Targets",
  blockedKeywords: "Blocked keywords",
  blockedKeywordsHint: "One keyword per line.",
  blockStatus: "Block status code",
  blockMessage: "Block message",
  retentionDays: "Event retention (days)",
  maxEvents: "Maximum events",
  events: "Events",
  hashes: "Hashes",
  clearEvents: "Clear events",
  clearHashes: "Clear hashes",
  weeklyOverdraft: "Weekly overdraft",
  agentIdentity: "Agent identity",
  autoModelWhitelist: "Automatic model allow-list",
  sub2apiCredits: "Sub2API credit billing",
  whitelist: "Allow-list events",
  accounts: "Accounts",
  limited: "Limited",
  hookExecution: "Run plugin hooks",
  hookExecutionHint: "Runs the model and allowance hooks of imported plugins. Request routing is never taken over.",
  registry: "Community plugin source",
  registryHint: "The plugin reads this repository's list itself; the browser never reaches the public internet.",
  imported: "Imported providers",
  pluginFile: "Plugin library file",
  pluginFileHint: "The file self-update writes. Empty means the host detects it.",
  currentVersion: "Current version",
  latestVersion: "Latest version",
  cpaVersion: "CPA version",
  checkUpdates: "Check for updates",
  install: "Install update",
  reloadThroughStore: "Reload through the plugin store",
  restartRequired: "Restart required",
  canInstall: "Can install",
  appliedVersion: "Applied version",
  upgradeHint: "Installing replaces the plugin library; the host may need a restart or reload to pick it up.",
  cancel: "Cancel",
  invalidResponse: "The host answered with something this page cannot read",
  confirm: "Confirm",
};

const catalogs: Record<string, SettingsCopy> = { en, "zh-CN": zh };

// A catalog lookup, not a locale comparison: adding a catalog needs no branch.
export const settingsCopyFor = (locale: Locale): SettingsCopy => catalogs[locale] ?? zh;

export function useSettingsCopy(): SettingsCopy {
  const { locale } = useI18n();
  return useMemo(() => settingsCopyFor(locale), [locale]);
}

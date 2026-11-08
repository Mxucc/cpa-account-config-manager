import { useMemo } from "react";
import { useI18n } from "../../../i18n";
import type { Locale } from "../../../i18n";

// Automation display text, isolated so no runtime file holds localized strings
// and no runtime file branches on a locale.
export interface AutomationCopy {
  inspection: string;
  policies: string;
  notifications: string;
  retry: string;
  proxy: string;
  run: string;
  running: string;
  stop: string;
  nativeScan: string;
  fullScan: string;
  enabled: string;
  interval: string;
  failureThreshold: string;
  recoveryThreshold: string;
  autoDisable: string;
  autoEnable: string;
  autoDelete: string;
  deleteGrace: string;
  deleteBatch: string;
  save: string;
  saved: string;
  dirty: string;
  health: string;
  reason: string;
  recommendation: string;
  account: string;
  provider: string;
  actions: string;
  review: string;
  resolve: string;
  ignore: string;
  reopen: string;
  deleteSelected: string;
  confirmDelete: string;
  targetCount: string;
  export: string;
  results: string;
  summary: string;
  recentActions: string;
  page: string;
  next: string;
  previous: string;
  filterHealth: string;
  search: string;
  defaults: string;
  defaultsHint: string;
  globalPolicy: string;
  globalPolicyHint: string;
  applyMode: string;
  scanNow: string;
  scanning: string;
  priority: string;
  concurrency: string;
  proxyURL: string;
  note: string;
  websockets: string;
  prefix: string;
  endpoints: string;
  addEndpoint: string;
  endpointName: string;
  endpointURL: string;
  preview: string;
  testSend: string;
  expandedURL: string;
  variables: string;
  retryAttempts: string;
  retryHint: string;
  retryHost: string;
  profiles: string;
  addProfile: string;
  profileName: string;
  enabledProfiles: string;
  accounts: string;
  remove: string;
  none: string;
  deleting: string;
  deleted: string;
  confirm: string;
  cancel: string;
  invalidResponse: string;
}

const zh: AutomationCopy = {
  inspection: "巡检",
  policies: "自动策略",
  notifications: "外部通知",
  retry: "自动重试",
  proxy: "代理档案",
  run: "开始巡检",
  running: "巡检中…",
  stop: "停止",
  nativeScan: "立即扫描（原生）",
  fullScan: "全量扫描",
  enabled: "启用",
  interval: "扫描间隔（分钟）",
  failureThreshold: "失败阈值",
  recoveryThreshold: "恢复阈值",
  autoDisable: "自动停用",
  autoEnable: "自动启用",
  autoDelete: "自动删除",
  deleteGrace: "删除宽限（小时）",
  deleteBatch: "删除批次大小",
  save: "保存",
  saved: "已保存",
  dirty: "有未保存的改动",
  health: "健康",
  reason: "原因",
  recommendation: "建议",
  account: "账号",
  provider: "供应商",
  actions: "操作",
  review: "复核",
  resolve: "已处理",
  ignore: "忽略",
  reopen: "重新打开",
  deleteSelected: "删除建议账号",
  confirmDelete: "确认删除",
  targetCount: "目标数量",
  export: "导出",
  results: "巡检结果",
  summary: "汇总",
  recentActions: "最近动作",
  page: "页",
  next: "下一页",
  previous: "上一页",
  filterHealth: "按健康筛选",
  search: "搜索",
  defaults: "默认策略",
  defaultsHint: "新账号的默认配置：只在缺失时补齐，不覆盖已有值。",
  globalPolicy: "全局策略",
  globalPolicyHint: "对所有账号强制生效的值；留空表示不修改。",
  applyMode: "应用方式",
  scanNow: "立即扫描",
  scanning: "扫描中…",
  priority: "优先级",
  concurrency: "并发上限",
  proxyURL: "代理 URL",
  note: "备注",
  websockets: "WebSockets",
  prefix: "前缀",
  endpoints: "通知端点",
  addEndpoint: "添加端点",
  endpointName: "名称",
  endpointURL: "URL 模板",
  preview: "预览",
  testSend: "测试发送",
  expandedURL: "展开后的 URL",
  variables: "变量",
  retryAttempts: "最大重试次数",
  retryHint: "CPAx 的自动重试次数。0 表示不重试。",
  retryHost: "宿主状态",
  profiles: "代理档案",
  addProfile: "新建档案",
  profileName: "名称",
  enabledProfiles: "已启用",
  accounts: "账号数",
  remove: "删除",
  none: "暂无数据",
  deleting: "删除中…",
  deleted: "已删除",
  confirm: "确认",
  cancel: "取消",
  invalidResponse: "服务端返回的数据结构无效",
};

const en: AutomationCopy = {
  inspection: "Inspection",
  policies: "Policies",
  notifications: "Notifications",
  retry: "Auto retry",
  proxy: "Proxy profiles",
  run: "Start inspection",
  running: "Running…",
  stop: "Stop",
  nativeScan: "Scan now (native)",
  fullScan: "Full sweep",
  enabled: "Enabled",
  interval: "Scan interval (minutes)",
  failureThreshold: "Failure threshold",
  recoveryThreshold: "Recovery threshold",
  autoDisable: "Auto disable",
  autoEnable: "Auto enable",
  autoDelete: "Auto delete",
  deleteGrace: "Delete grace (hours)",
  deleteBatch: "Delete batch size",
  save: "Save",
  saved: "Saved",
  dirty: "Unsaved changes",
  health: "Health",
  reason: "Reason",
  recommendation: "Recommendation",
  account: "Account",
  provider: "Provider",
  actions: "Actions",
  review: "Review",
  resolve: "Resolved",
  ignore: "Ignore",
  reopen: "Reopen",
  deleteSelected: "Delete recommended accounts",
  confirmDelete: "Confirm deletion",
  targetCount: "Targets",
  export: "Export",
  results: "Inspection results",
  summary: "Summary",
  recentActions: "Recent actions",
  page: "Page",
  next: "Next",
  previous: "Previous",
  filterHealth: "Filter by health",
  search: "Search",
  defaults: "Default policy",
  defaultsHint: "What a new account gets: filled in only when a value is missing.",
  globalPolicy: "Global policy",
  globalPolicyHint: "Values forced onto every account; empty means unchanged.",
  applyMode: "Apply mode",
  scanNow: "Scan now",
  scanning: "Scanning…",
  priority: "Priority",
  concurrency: "Concurrency limit",
  proxyURL: "Proxy URL",
  note: "Note",
  websockets: "WebSockets",
  prefix: "Prefix",
  endpoints: "Notification endpoints",
  addEndpoint: "Add endpoint",
  endpointName: "Name",
  endpointURL: "URL template",
  preview: "Preview",
  testSend: "Send test",
  expandedURL: "Expanded URL",
  variables: "Variables",
  retryAttempts: "Maximum attempts",
  retryHint: "CPA's automatic retry count. 0 disables retries.",
  retryHost: "Host state",
  profiles: "Proxy profiles",
  addProfile: "New profile",
  profileName: "Name",
  enabledProfiles: "Enabled",
  accounts: "Accounts",
  remove: "Delete",
  none: "Nothing here",
  deleting: "Deleting…",
  deleted: "Deleted",
  confirm: "Confirm",
  cancel: "Cancel",
  invalidResponse: "The host answered with something this page cannot read",
};

const catalogs: Record<string, AutomationCopy> = { en, "zh-CN": zh };

// A catalog lookup, not a locale comparison: adding a catalog needs no branch.
export const automationCopyFor = (locale: Locale): AutomationCopy => catalogs[locale] ?? zh;

export function useAutomationCopy(): AutomationCopy {
  const { locale } = useI18n();
  return useMemo(() => automationCopyFor(locale), [locale]);
}


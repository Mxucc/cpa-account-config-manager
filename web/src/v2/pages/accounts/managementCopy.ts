import { useMemo } from "react";
import { useI18n } from "../../../i18n";
import type { Locale } from "../../../i18n";

// The account-management strings, kept in one catalog module so no runtime file
// carries display text and no runtime file branches on a locale.
export interface ManagementCopy {
  title: string;
  actions: string;
  edit: string;
  editHint: string;
  models: string;
  modelsHint: string;
  test: string;
  testHint: string;
  model: string;
  token: string;
  tokenHint: string;
  delete: string;
  deleteHint: string;
  batchEdit: string;
  batchDelete: string;
  dedupe: string;
  importAccounts: string;
  exportAccounts: string;
  format: string;
  scope: string;
  scopeSelected: string;
  scopeFiltered: string;
  preview: string;
  apply: string;
  applying: string;
  start: string;
  targets: string;
  eligible: string;
  readOnly: string;
  missing: string;
  physicalFiles: string;
  changedFields: string;
  jobProgress: string;
  noJob: string;
  pickFile: string;
  pickedFiles: string;
  duplicateGroups: string;
  proposedDeletions: string;
  confirmDelete: string;
  keep: string;
  removed: string;
  download: string;
  downloaded: string;
  note: string;
  prefix: string;
  priority: string;
  disabled: string;
  websockets: string;
  proxyURL: string;
  concurrency: string;
  concurrency15s: string;
  windowSeconds: string;
  leaveEmpty: string;
  testResult: string;
  status: string;
  reason: string;
  latency: string;
  testedAt: string;
  ignoreAccountID: string;
  excludeTeams: string;
  scan: string;
  scanning: string;
  none: string;
  hookNote: string;
  close: string;
  unmanaged: string;
  addAccount: string;
  addAccountHint: string;
  chooseProvider: string;
  flowBrowser: string;
  flowDevice: string;
  flowPlugin: string;
  flowExternal: string;
  openLoginPage: string;
  userCode: string;
  loginWaiting: string;
  loginCheck: string;
  loginDone: string;
  loginFailed: string;
  communityTitle: string;
  communityNote: string;
}

const zh: ManagementCopy = {
  title: "账号管理",
  actions: "操作",
  edit: "编辑配置",
  editHint: "只提交你改动的字段；提交前先给出变更预览。",
  models: "模型目录",
  modelsHint: "读取该账号可见的模型清单（只读）。",
  test: "测试模型",
  testHint: "通过该账号凭据探测一次模型可用性。",
  model: "模型",
  token: "刷新令牌",
  tokenHint: "调用 CPA 原生凭据刷新。",
  delete: "删除账号",
  deleteHint: "删除前先校验文件未变化，并生成预览。",
  batchEdit: "批量编辑",
  batchDelete: "批量删除",
  dedupe: "重复账号",
  importAccounts: "导入账号",
  exportAccounts: "导出账号",
  format: "格式",
  scope: "范围",
  scopeSelected: "当前筛选结果",
  scopeFiltered: "当前筛选条件",
  preview: "变更预览",
  apply: "应用到账号",
  applying: "应用中…",
  start: "开始",
  targets: "目标",
  eligible: "可执行",
  readOnly: "只读",
  missing: "缺失",
  physicalFiles: "物理文件",
  changedFields: "改动字段",
  jobProgress: "执行进度",
  noJob: "尚未执行",
  pickFile: "选择文件",
  pickedFiles: "已选择文件",
  duplicateGroups: "重复组",
  proposedDeletions: "建议删除",
  confirmDelete: "确认删除",
  keep: "保留",
  removed: "删除",
  download: "下载",
  downloaded: "已开始下载",
  note: "备注",
  prefix: "前缀",
  priority: "优先级",
  disabled: "停用",
  websockets: "WebSockets",
  proxyURL: "代理 URL",
  concurrency: "并发上限",
  concurrency15s: "15 秒并发上限",
  windowSeconds: "窗口秒数",
  leaveEmpty: "留空表示不修改",
  testResult: "测试结果",
  status: "状态",
  reason: "原因",
  latency: "耗时",
  testedAt: "测试时间",
  ignoreAccountID: "忽略账号 ID 差异",
  excludeTeams: "排除团队账号",
  scan: "开始扫描",
  scanning: "扫描中…",
  none: "暂无",
  hookNote: "全部改动都先预览再执行，预览过期需要重新生成。",
  close: "关闭",
  unmanaged: "该账号不由 CPA 管理（插件自建工作区），只能用刷新、测试与渠道绑定操作。",
  addAccount: "添加账号",
  addAccountHint: "选择一个供应商，用 CPA 的登录流程新增账号；登录在 CPA 侧完成，插件只转发。",
  chooseProvider: "供应商",
  flowBrowser: "浏览器登录",
  flowDevice: "设备码登录",
  flowPlugin: "插件登录",
  flowExternal: "原插件内登录",
  openLoginPage: "打开登录页",
  userCode: "设备码",
  loginWaiting: "等待授权…",
  loginCheck: "刷新状态",
  loginDone: "授权完成，账号已新增",
  loginFailed: "登录未完成",
  communityTitle: "社区供应商",
  communityNote: "这些供应商要在 Magpie/OpenCode 里登录，CPA 没有它们的登录实现，所以这里不能直接添加；请到对应插件完成登录后，把凭据以 CPA 渠道方式接入。",
};

const en: ManagementCopy = {
  title: "Account management",
  actions: "Actions",
  edit: "Edit configuration",
  editHint: "Only the fields you change are submitted, and only after a preview.",
  models: "Model catalog",
  modelsHint: "Read the models this account can see (read-only).",
  test: "Test model",
  testHint: "Probe one model's availability through this account's credential.",
  model: "Model",
  token: "Refresh token",
  tokenHint: "Call CPA's native credential refresh.",
  delete: "Delete account",
  deleteHint: "Verify the file is unchanged and preview the deletion first.",
  batchEdit: "Batch edit",
  batchDelete: "Batch delete",
  dedupe: "Duplicate accounts",
  importAccounts: "Import accounts",
  exportAccounts: "Export accounts",
  format: "Format",
  scope: "Scope",
  scopeSelected: "Current selection",
  scopeFiltered: "Current filters",
  preview: "Change preview",
  apply: "Apply to accounts",
  applying: "Applying…",
  start: "Start",
  targets: "Targets",
  eligible: "Eligible",
  readOnly: "Read-only",
  missing: "Missing",
  physicalFiles: "Physical files",
  changedFields: "Changed fields",
  jobProgress: "Progress",
  noJob: "Not started",
  pickFile: "Choose files",
  pickedFiles: "Selected files",
  duplicateGroups: "Duplicate groups",
  proposedDeletions: "Proposed deletions",
  confirmDelete: "Confirm deletion",
  keep: "Keep",
  removed: "Delete",
  download: "Download",
  downloaded: "Download started",
  note: "Note",
  prefix: "Prefix",
  priority: "Priority",
  disabled: "Disabled",
  websockets: "WebSockets",
  proxyURL: "Proxy URL",
  concurrency: "Concurrency limit",
  concurrency15s: "15s concurrency limit",
  windowSeconds: "Window seconds",
  leaveEmpty: "Leave empty to keep unchanged",
  testResult: "Test result",
  status: "Status",
  reason: "Reason",
  latency: "Latency",
  testedAt: "Tested at",
  ignoreAccountID: "Ignore account id differences",
  excludeTeams: "Exclude team accounts",
  scan: "Scan",
  scanning: "Scanning…",
  none: "Nothing here",
  hookNote: "Every change is previewed before it is applied; an expired preview must be regenerated.",
  close: "Close",
  unmanaged: "CPA does not manage this account (a plugin-owned workspace), so only refresh, test and channel binding apply.",
  addAccount: "Add account",
  addAccountHint: "Pick a provider and let CPA run its own sign-in; the plugin only relays it.",
  chooseProvider: "Provider",
  flowBrowser: "Browser sign-in",
  flowDevice: "Device code",
  flowPlugin: "Plugin sign-in",
  flowExternal: "Signed in by the original plugin",
  openLoginPage: "Open sign-in page",
  userCode: "Device code",
  loginWaiting: "Waiting for approval…",
  loginCheck: "Refresh status",
  loginDone: "Signed in; the account was added",
  loginFailed: "The sign-in did not finish",
  communityTitle: "Community providers",
  communityNote: "These providers sign in inside Magpie or OpenCode, and CPA has no login implementation for them; finish the sign-in there and add the credential as a CPA channel.",
};

const catalogs: Record<string, ManagementCopy> = { en, "zh-CN": zh };

// A catalog lookup, not a locale comparison: adding a catalog needs no branch.
export const managementCopyFor = (locale: Locale): ManagementCopy => catalogs[locale] ?? zh;

// The accounts page labels its own actions from here, so the strings live in one
// place instead of being duplicated per dialog.
export function useAccountManagementCopy(): ManagementCopy {
  const { locale } = useI18n();
  return useMemo(() => managementCopyFor(locale), [locale]);
}
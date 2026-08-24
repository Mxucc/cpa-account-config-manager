import { getSession } from "../store/session";
import type {
  AccountDeletePreview,
  AccountDeleteResult,
	AccountTokenRefreshResult,
  AccountDeduplicationPreview,
  AccountDeduplicationOptions,
	AccountEditableConfig,
	AccountConcurrencyAvailability,
  AccountFilters,
  AccountExportFormat,
  AccountListResponse,
  AccountSort,
	AccountModelCatalogResponse,
  BatchPatch,
  BatchPreview,
	CPAServerVersionSnapshot,
	DefaultPolicy,
	ExperimentalSettings,
	ExperimentalSettingsSnapshot,
	AgentIdentitySessionLoginResponse,
	OpenCodeAccountSaveResponse,
	OpenCodeAccountsResponse,
	OpenCodeProbeResponse,
	OpenCodeZenAccountSaveResponse,
	OpenCodeZenAccountsResponse,
	OpenCodeZenProbeAccountResponse,
	OpenCodeZenProbeResponse,
	AIProviderChannelKind,
	AIProviderChannelSnapshot,
	AIProviderChannelEntry,
	AIProviderAPIKeyEntry,
	AIProviderChannelModel,
	AIProviderRuntimeResponse,
	ForceSyncJobSnapshot,
	ForceSyncPreview,
  ExportFormat,
  ImportPreview,
  ImportResult,
  InspectionAction,
  InspectionDeleteRun,
	InspectionNotificationPreview,
	InspectionNotificationRequest,
	InspectionNotificationTestResult,
  InspectionPolicy,
  InspectionRemediationSummary,
  InspectionResultList,
  InspectionResult,
  InspectionRunRequest,
  InspectionSnapshot,
  JobSnapshot,
  ModelTestResult,
  OperationEntry,
  OperationExportFormat,
  OperationFilters,
  OperationListResponse,
  OperationRetentionSettings,
  PluginInstallResult,
  PluginStoreResponse,
  PolicySnapshot,
	QuotaMetadataResponse,
  ResultExportFormat,
  TargetScope,
  UpdatePolicy,
  UpdateSnapshot,
} from "../types";

const API_ROOT = "/v0/management/plugins/cpa-account-config-manager";

export class APIError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

function buildURL(path: string, query?: URLSearchParams): string {
  const session = getSession();
  if (!session) throw new APIError(401, "ui.management_key_is_not_set");
  const suffix = query && query.size > 0 ? `?${query.toString()}` : "";
  return `${session.baseUrl}${API_ROOT}${path}${suffix}`;
}

async function request<T>(path: string, init: RequestInit = {}, query?: URLSearchParams): Promise<T> {
  const session = getSession();
  if (!session) throw new APIError(401, "ui.management_key_is_not_set");
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  headers.set("Authorization", `Bearer ${session.managementKey}`);
  const isFormData = typeof FormData !== "undefined" && init.body instanceof FormData;
  if (init.body && !isFormData && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  const response = await fetch(buildURL(path, query), {
    ...init,
    headers,
  });
  if (!response.ok) {
    let message = `Request failed (${response.status})`;
    try {
      const body = (await response.json()) as { error?: string; message?: string };
      if (body.message || body.error) message = body.message || body.error || message;
    } catch {
      // Keep the status-only error when the response is not JSON.
    }
    throw new APIError(response.status, message);
  }
  return (await response.json()) as T;
}

function buildManagementURL(path: string): string {
  const session = getSession();
  if (!session) throw new APIError(401, "ui.management_key_is_not_set");
  return `${session.baseUrl}/v0/management${path}`;
}

async function managementRequest<T>(path: string, init: RequestInit = {}): Promise<T> {
  const session = getSession();
  if (!session) throw new APIError(401, "ui.management_key_is_not_set");
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  headers.set("Authorization", `Bearer ${session.managementKey}`);
  if (init.body && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  const response = await fetch(buildManagementURL(path), { ...init, headers });
  if (!response.ok) {
    let message = `Request failed (${response.status})`;
    try {
      const body = (await response.json()) as { error?: string; message?: string };
      message = body.error === "plugin_update_requires_restart" ? body.error : body.message || body.error || message;
    } catch {
      // Keep the status-only error when the response is not JSON.
    }
    throw new APIError(response.status, message);
  }
  return (await response.json()) as T;
}

interface ParsedCPAServerVersion {
  core: [number, number, number];
  prerelease: string[];
}

function safeCPAVersionLabel(value: unknown): string {
  if (typeof value !== "string") return "";
  const trimmed = value.trim();
  if (!trimmed || trimmed.length > 64 || !/^[A-Za-z0-9][A-Za-z0-9._+-]*$/.test(trimmed)) return "";
  return /^v?\d+\.\d+\.\d+(?:[-+].+)?$/i.test(trimmed)
    ? `v${trimmed.replace(/^v/i, "")}`
    : trimmed;
}

function safeCPAHeaderText(value: unknown): string {
  if (typeof value !== "string") return "";
  const trimmed = value.trim();
  return trimmed && trimmed.length <= 96 && !/[\u0000-\u001f\u007f]/.test(trimmed) ? trimmed : "";
}

function parseCPAServerVersion(value: string): ParsedCPAServerVersion | null {
  const match = /^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$/i.exec(value.trim());
  if (!match) return null;
  const core = [Number(match[1]), Number(match[2]), Number(match[3])] as [number, number, number];
  if (core.some((part) => !Number.isSafeInteger(part) || part < 0 || part > 1_000_000)) return null;
  const prerelease = match[4] ? match[4].split(".") : [];
  if (prerelease.some((part) => !part)) return null;
  return { core, prerelease };
}

function comparePrereleaseIdentifiers(left: string[], right: string[]): number {
  if (left.length === 0 || right.length === 0) {
    if (left.length === right.length) return 0;
    return left.length === 0 ? 1 : -1;
  }
  for (let index = 0; index < Math.max(left.length, right.length); index += 1) {
    const leftPart = left[index];
    const rightPart = right[index];
    if (leftPart === undefined || rightPart === undefined) return leftPart === undefined ? -1 : 1;
    if (leftPart === rightPart) continue;
    const leftNumeric = /^\d+$/.test(leftPart);
    const rightNumeric = /^\d+$/.test(rightPart);
    if (leftNumeric && rightNumeric) return Number(leftPart) < Number(rightPart) ? -1 : 1;
    if (leftNumeric !== rightNumeric) return leftNumeric ? -1 : 1;
    return leftPart < rightPart ? -1 : 1;
  }
  return 0;
}

export function compareCPAServerVersions(left: string, right: string): number | null {
  const parsedLeft = parseCPAServerVersion(left);
  const parsedRight = parseCPAServerVersion(right);
  if (!parsedLeft || !parsedRight) return null;
  for (let index = 0; index < parsedLeft.core.length; index += 1) {
    if (parsedLeft.core[index] !== parsedRight.core[index]) {
      return parsedLeft.core[index] < parsedRight.core[index] ? -1 : 1;
    }
  }
  return comparePrereleaseIdentifiers(parsedLeft.prerelease, parsedRight.prerelease);
}

export async function getCPAServerVersionStatus(): Promise<CPAServerVersionSnapshot> {
  const session = getSession();
  if (!session) throw new APIError(401, "ui.management_key_is_not_set");
  const headers = new Headers({ Accept: "application/json", Authorization: `Bearer ${session.managementKey}` });
  const controller = new AbortController();
  const timeout = window.setTimeout(() => controller.abort(), 15_000);
  let response: Response;
  try {
    response = await fetch(buildManagementURL("/latest-version"), { headers, signal: controller.signal });
  } finally {
    window.clearTimeout(timeout);
  }
  if (response.status === 401) throw new APIError(401, "ui.authentication_failed");

  const currentVersion = safeCPAVersionLabel(response.headers.get("X-CPA-Version") || response.headers.get("X-Server-Version"));
  const currentBuildDate = safeCPAHeaderText(response.headers.get("X-CPA-Build-Date") || response.headers.get("X-Server-Build-Date"));
  let latestVersion = "";
  if (response.ok) {
    try {
      const payload = await response.json() as Record<string, unknown>;
      latestVersion = safeCPAVersionLabel(payload["latest-version"] ?? payload.latest_version ?? payload.latest);
    } catch {
      latestVersion = "";
    }
  }

  const comparison = currentVersion && latestVersion ? compareCPAServerVersions(currentVersion, latestVersion) : null;
  const error = !currentVersion
    ? "current_version_unavailable" as const
    : !latestVersion
      ? "latest_version_unavailable" as const
      : comparison === null
        ? "version_comparison_unavailable" as const
        : undefined;
  return {
    current_version: currentVersion || undefined,
    latest_version: latestVersion || undefined,
    current_build_date: currentBuildDate || undefined,
    update_available: comparison !== null && comparison < 0,
    checked_at: new Date().toISOString(),
    release_url: latestVersion && parseCPAServerVersion(latestVersion)
      ? `https://github.com/router-for-me/CLIProxyAPI/releases/tag/${encodeURIComponent(latestVersion)}`
      : undefined,
    error,
  };
}

function arrayOrEmpty<T>(value: T[] | null | undefined): T[] {
  return Array.isArray(value) ? value : [];
}

const legacyAccountConcurrency: AccountConcurrencyAvailability = {
	supported: false,
	host_schema_version: 1,
	required_schema_version: 2,
	reason: "host_schema_v2_required",
};

function filtersQuery(filters: AccountFilters): URLSearchParams {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(filters)) {
    if (value === undefined || value === "") continue;
    query.set(key, String(value));
  }
  return query;
}

export async function verifySession(): Promise<void> {
  const query = new URLSearchParams({ page: "1", page_size: "1" });
  await request<AccountListResponse>("/accounts", {}, query);
}

export async function listAccounts(
  page: number,
  pageSize: number,
  filters: AccountFilters,
  sort: AccountSort = { field: "account", order: "asc" },
): Promise<AccountListResponse> {
  const query = filtersQuery(filters);
  query.set("page", String(page));
  query.set("page_size", String(pageSize));
  query.set("sort_by", sort.field);
  query.set("sort_order", sort.order);
  const response = await request<AccountListResponse>("/accounts", {}, query);
	const availability = response.account_concurrency ?? legacyAccountConcurrency;
	return {
		...response,
		account_concurrency: availability,
		accounts: arrayOrEmpty(response.accounts).map((account) => ({
			...account,
			concurrency: account.concurrency ?? { supported: availability.supported, active: 0, limit: 0 },
		})),
	};
}

export async function loadAccountConfig(accountID: string): Promise<AccountEditableConfig> {
	const response = await request<AccountEditableConfig>("/accounts/config", {
		method: "POST",
		body: JSON.stringify({ account_id: accountID }),
	});
	const availability = response.account_concurrency ?? legacyAccountConcurrency;
	return {
		...response,
		header_names: arrayOrEmpty(response.header_names),
		account_concurrency: availability,
		concurrency: response.concurrency ?? { supported: availability.supported, active: 0, limit: 0 },
	};
}

export async function refreshAccountQuotaMetadata(accountID: string): Promise<QuotaMetadataResponse> {
	return request<QuotaMetadataResponse>("/accounts/quota-metadata/refresh", {
		method: "POST",
		body: JSON.stringify({ account_id: accountID }),
	});
}

export async function useAccountActiveReset(accountID: string): Promise<QuotaMetadataResponse> {
	return request<QuotaMetadataResponse>("/accounts/quota-metadata/reset", {
		method: "POST",
		body: JSON.stringify({ account_id: accountID, confirm: true }),
	});
}

export async function testAccountModel(accountID: string, model: string, experimentalWeeklyOverdraft = false): Promise<ModelTestResult> {
  return request<ModelTestResult>("/accounts/model-test", {
    method: "POST",
    body: JSON.stringify({
      account_id: accountID,
      model: model.trim(),
      ...(experimentalWeeklyOverdraft ? { experimental_weekly_overdraft: true } : {}),
    }),
  });
}

export async function refreshAccountToken(accountID: string): Promise<AccountTokenRefreshResult> {
	return request<AccountTokenRefreshResult>("/accounts/token/refresh", {
		method: "POST",
		body: JSON.stringify({ account_id: accountID }),
	});
}

export async function loadAccountModels(scope: TargetScope): Promise<AccountModelCatalogResponse> {
	const response = await request<AccountModelCatalogResponse>("/accounts/models", {
		method: "POST",
		body: JSON.stringify({ scope }),
	});
	return { ...response, models: arrayOrEmpty(response.models), warnings: arrayOrEmpty(response.warnings) };
}

export async function createAccountDeletePreview(accountID: string): Promise<AccountDeletePreview> {
  return request<AccountDeletePreview>("/accounts/delete/preview", {
    method: "POST",
    body: JSON.stringify({ id: accountID }),
  });
}

export async function deleteAccount(previewID: string): Promise<AccountDeleteResult> {
  return request<AccountDeleteResult>("/accounts/delete/start", {
    method: "POST",
    body: JSON.stringify({ preview_id: previewID }),
  });
}

export async function createPreview(scope: TargetScope, patch: BatchPatch): Promise<BatchPreview> {
  return request<BatchPreview>("/batch/preview", {
    method: "POST",
    body: JSON.stringify({ scope, patch }),
  });
}

export async function createBatchDeletePreview(scope: TargetScope): Promise<BatchPreview> {
  return request<BatchPreview>("/batch/delete/preview", {
    method: "POST",
    body: JSON.stringify({ scope }),
  });
}

export async function scanAccountDuplicates(options: AccountDeduplicationOptions): Promise<AccountDeduplicationPreview> {
  return request<AccountDeduplicationPreview>("/accounts/deduplicate/preview", {
    method: "POST",
    body: JSON.stringify(options),
  });
}

export async function startBatch(previewID: string): Promise<JobSnapshot> {
  return request<JobSnapshot>("/batch/start", {
    method: "POST",
    body: JSON.stringify({ preview_id: previewID }),
  });
}

export async function startBatchDelete(previewID: string): Promise<JobSnapshot> {
  return request<JobSnapshot>("/batch/delete/start", {
    method: "POST",
    body: JSON.stringify({ preview_id: previewID, confirm: true }),
  });
}

export async function getJobStatus(includeResults = true): Promise<JobSnapshot> {
  const query = new URLSearchParams();
  if (!includeResults) query.set("light", "1");
  return request<JobSnapshot>("/batch/status", {}, query);
}

export async function retryBatch(): Promise<JobSnapshot> {
  return request<JobSnapshot>("/batch/retry", { method: "POST" });
}

export async function getDefaultPolicy(): Promise<PolicySnapshot> {
	return request<PolicySnapshot>("/defaults");
}

interface PersistentPluginSettings {
	default_policy?: DefaultPolicy;
	inspection_policy?: InspectionPolicy;
	update_policy?: UpdatePolicy;
	operation_settings?: Pick<OperationRetentionSettings, "extended_history">;
	experimental_settings?: ExperimentalSettings;
}

async function persistPluginSettings(settings: PersistentPluginSettings): Promise<void> {
	try {
		await request<unknown>("/config", {
			method: "PATCH",
			body: JSON.stringify(settings),
		});
	} catch (error) {
		if (error instanceof APIError) throw new APIError(error.status, "ui.settings_persistence_failed");
		throw error;
	}
}

export async function saveDefaultPolicy(policy: DefaultPolicy): Promise<PolicySnapshot> {
	await persistPluginSettings({ default_policy: policy });
	return request<PolicySnapshot>("/defaults", {
		method: "PUT",
		body: JSON.stringify(policy),
	});
}

export async function scanDefaultPolicy(): Promise<PolicySnapshot> {
	return request<PolicySnapshot>("/defaults/scan", { method: "POST" });
}

export async function getInspection(): Promise<InspectionSnapshot> {
  return request<InspectionSnapshot>("/inspection");
}

export async function getLiveInspection(): Promise<InspectionSnapshot> {
  const response = await request<InspectionSnapshot>("/inspection/live");
  return { ...response, live_results: arrayOrEmpty(response.live_results) };
}

export async function saveInspectionPolicy(policy: InspectionPolicy, confirmAutoDelete = false, confirmDeleteInvalidCredentials = false): Promise<InspectionSnapshot> {
	await persistPluginSettings({ inspection_policy: policy });
  return request<InspectionSnapshot>("/inspection", {
    method: "PUT",
    body: JSON.stringify({ ...policy, confirm_auto_delete: confirmAutoDelete, confirm_delete_invalid_credentials: confirmDeleteInvalidCredentials }),
  });
}

export async function scanFullInspection(): Promise<InspectionSnapshot> {
  return request<InspectionSnapshot>("/inspection/scan", { method: "POST" });
}

export async function scanNativeInspection(): Promise<InspectionSnapshot> {
  return request<InspectionSnapshot>("/inspection/scan/native", { method: "POST" });
}

export async function previewInspectionNotification(notification: InspectionNotificationRequest): Promise<InspectionNotificationPreview> {
  return request<InspectionNotificationPreview>("/inspection/notification/preview", {
    method: "POST",
    body: JSON.stringify(notification),
  });
}

export async function testInspectionNotification(notification: InspectionNotificationRequest): Promise<InspectionNotificationTestResult> {
  return request<InspectionNotificationTestResult>("/inspection/notification/test", {
    method: "POST",
    body: JSON.stringify(notification),
  });
}

export async function startInspectionRun(run: InspectionRunRequest): Promise<InspectionSnapshot> {
  return request<InspectionSnapshot>("/inspection/run", {
    method: "POST",
    body: JSON.stringify(run),
  });
}

export async function stopInspectionRun(): Promise<InspectionSnapshot> {
  return request<InspectionSnapshot>("/inspection/stop", { method: "POST" });
}

export async function updateInspectionReview(accountID: string, action: "resolve" | "ignore" | "reopen"): Promise<InspectionResult> {
  return request<InspectionResult>("/inspection/review", {
    method: "POST",
    body: JSON.stringify({ account_id: accountID, action }),
  });
}

export async function listInspectionResults(page: number, pageSize: number, health = "", search = ""): Promise<InspectionResultList> {
  const query = new URLSearchParams({ page: String(page), page_size: String(pageSize) });
  if (health) query.set("health", health);
  if (search) query.set("search", search);
  const response = await request<InspectionResultList>("/inspection/results", {}, query);
	const summary = response.summary as Partial<InspectionRemediationSummary> | null | undefined;
  return {
    ...response,
    results: arrayOrEmpty(response.results),
		summary: {
			actionable: summary?.actionable ?? 0,
			suggested_delete: summary?.suggested_delete ?? 0,
			suggested_disable: summary?.suggested_disable ?? 0,
			suggested_enable: summary?.suggested_enable ?? 0,
			reauth: summary?.reauth ?? 0,
			deletable_reauth: summary?.deletable_reauth ?? 0,
			review: summary?.review ?? 0,
			keep: summary?.keep ?? 0,
			handled: summary?.handled ?? 0,
			editable_enabled: summary?.editable_enabled ?? 0,
			editable_disabled: summary?.editable_disabled ?? 0,
		},
  };
}

export async function listInspectionActions(limit = 50): Promise<InspectionAction[]> {
  const query = new URLSearchParams({ limit: String(limit) });
  const response = await request<{ actions: InspectionAction[] }>("/inspection/actions", {}, query);
  return arrayOrEmpty(response.actions);
}

export async function deleteInspectionRecommendations(accountIDs: string[]): Promise<InspectionDeleteRun> {
  return request<InspectionDeleteRun>("/inspection/delete", {
    method: "POST",
    body: JSON.stringify({ account_ids: accountIDs, confirm: true }),
  });
}

export async function downloadInspectionExport(format: "json" | "csv" | "jsonl", health = "", search = ""): Promise<{ filename: string; exported?: number }> {
  const session = getSession();
  if (!session) throw new APIError(401, "ui.management_key_is_not_set");
  const query = new URLSearchParams({ format });
  if (health) query.set("health", health);
  if (search) query.set("search", search);
  const response = await fetch(buildURL("/inspection/export", query), {
    headers: { Authorization: `Bearer ${session.managementKey}` },
  });
  if (!response.ok) {
    let message = `Export failed (${response.status})`;
    try {
      const body = (await response.json()) as { error?: string };
      if (body.error) message = body.error;
    } catch {
      // Keep the status-only error when the response is not JSON.
    }
    throw new APIError(response.status, message);
  }
  const disposition = response.headers.get("Content-Disposition") ?? "";
  const match = disposition.match(/filename="?([^";]+)"?/i);
  const filename = match?.[1] ?? `cpa-account-inspection.${format}`;
  const href = URL.createObjectURL(await response.blob());
  const anchor = document.createElement("a");
  anchor.href = href;
  anchor.download = filename;
  anchor.click();
  URL.revokeObjectURL(href);
  return { filename, exported: numericHeader(response.headers.get("X-Exported-Inspection-Results")) };
}

export async function executeInspectionAutoDelete(): Promise<InspectionDeleteRun> {
  return request<InspectionDeleteRun>("/inspection/auto-delete", { method: "POST" });
}

export async function getUpdateStatus(): Promise<UpdateSnapshot> {
  return request<UpdateSnapshot>("/updates");
}

export async function getExperimentalSettings(): Promise<ExperimentalSettingsSnapshot> {
	return request<ExperimentalSettingsSnapshot>("/experiments");
}

export async function saveExperimentalSettings(settings: ExperimentalSettings): Promise<ExperimentalSettingsSnapshot> {
	await persistPluginSettings({ experimental_settings: settings });
	return request<ExperimentalSettingsSnapshot>("/experiments", {
		method: "PUT",
		body: JSON.stringify(settings),
	});
}

export async function completeAgentIdentitySessionLogin(state: string, sessionJSON: string): Promise<AgentIdentitySessionLoginResponse> {
	return request<AgentIdentitySessionLoginResponse>("/experiments/agent-identity/session-login", {
		method: "POST",
		body: JSON.stringify({ state, session_json: sessionJSON }),
	});
}

export async function listOpenCodeAccounts(): Promise<OpenCodeAccountsResponse> {
	return request<OpenCodeAccountsResponse>("/opencode/accounts");
}

export async function saveOpenCodeAccount(workspaceID: string, authCookie: string): Promise<OpenCodeAccountSaveResponse> {
	return request<OpenCodeAccountSaveResponse>("/opencode/accounts", {
		method: "POST",
		body: JSON.stringify({ workspace_id: workspaceID, auth_cookie: authCookie }),
	});
}

export async function removeOpenCodeAccount(accountID: string): Promise<void> {
	await request<{ removed: boolean }>("/opencode/accounts?account_id=" + encodeURIComponent(accountID), {
		method: "DELETE",
	});
}

export async function refreshOpenCodeQuota(): Promise<{ results: Record<string, import("../types").OpenCodeQuotaResult> }> {
	return request<{ results: Record<string, import("../types").OpenCodeQuotaResult> }>("/opencode/refresh", {
		method: "POST",
	});
}

export async function probeOpenCodeQuota(workspaceID: string, authCookie: string, timeoutSeconds = 30): Promise<OpenCodeProbeResponse> {
	return request<OpenCodeProbeResponse>("/opencode/probe", {
		method: "POST",
		body: JSON.stringify({ workspace_id: workspaceID, auth_cookie: authCookie, timeout_seconds: timeoutSeconds }),
	});
}

export async function listOpenCodeZenAccounts(): Promise<OpenCodeZenAccountsResponse> {
	return request<OpenCodeZenAccountsResponse>("/opencode/zen/accounts");
}

export async function saveOpenCodeZenAccount(options: {
	account_id?: string;
	name?: string;
	base_url: string;
	zen_api_key?: string;
	timeout_seconds?: number;
}): Promise<OpenCodeZenAccountSaveResponse> {
	return request<OpenCodeZenAccountSaveResponse>("/opencode/zen/accounts", {
		method: "POST",
		body: JSON.stringify(options),
	});
}

export async function removeOpenCodeZenAccount(accountID: string): Promise<void> {
	await request<{ removed: boolean }>("/opencode/zen/accounts?account_id=" + encodeURIComponent(accountID), {
		method: "DELETE",
	});
}

export async function probeOpenCodeZen(options: {
	base_url: string;
	zen_api_key: string;
	timeout_seconds?: number;
}): Promise<OpenCodeZenProbeResponse> {
	return request<OpenCodeZenProbeResponse>("/opencode/zen/probe", {
		method: "POST",
		body: JSON.stringify(options),
	});
}

export async function probeOpenCodeZenAccount(accountID: string): Promise<OpenCodeZenProbeAccountResponse> {
	return request<OpenCodeZenProbeAccountResponse>("/opencode/zen/probe-account?account_id=" + encodeURIComponent(accountID), {
		method: "POST",
	});
}

export async function saveUpdatePolicy(policy: UpdatePolicy, confirmAutoUpdate = false): Promise<UpdateSnapshot> {
	await persistPluginSettings({ update_policy: policy });
  const status = await request<UpdateSnapshot>("/updates", {
    method: "PUT",
    body: JSON.stringify({ policy, confirm_auto_update: confirmAutoUpdate }),
  });
  const store = await loadPluginStore();
  return reconcileUpdateStatus(status, store.response, store.error);
}

export async function checkForUpdates(): Promise<UpdateSnapshot> {
  return request<UpdateSnapshot>("/updates/check", { method: "POST" });
}

export async function getPluginStore(): Promise<PluginStoreResponse> {
  const response = await managementRequest<PluginStoreResponse>("/plugin-store");
  return { ...response, plugins: arrayOrEmpty(response.plugins) };
}

const pluginID = "cpa-account-config-manager";
const pluginReleaseBaseURL = "https://github.com/Mxucc/cpa-account-config-manager/releases/tag/v";

function normalizedStableVersion(value: string | undefined): { value: string; parts: [number, number, number] } | null {
  const match = /^v?(\d+)\.(\d+)\.(\d+)$/.exec((value ?? "").trim());
  if (!match) return null;
  const parts = [Number(match[1]), Number(match[2]), Number(match[3])] as [number, number, number];
  if (parts.some((part) => !Number.isSafeInteger(part))) return null;
  return { value: parts.join("."), parts };
}

function compareStableVersions(left: [number, number, number], right: [number, number, number]): number {
  for (let index = 0; index < left.length; index += 1) {
    if (left[index] !== right[index]) return left[index] - right[index];
  }
  return 0;
}

export function reconcileUpdateStatus(status: UpdateSnapshot, store: PluginStoreResponse | null, storeError = ""): UpdateSnapshot {
  const obsoleteDirectCheckErrors = new Set([
    "release metadata request failed",
    "release metadata response was invalid",
    "repository metadata is invalid",
    "update check is unavailable",
  ]);
  const statusError = status.error?.trim() || "";
  const retainedError = obsoleteDirectCheckErrors.has(statusError) ? "" : statusError;
  const currentVersion = normalizedStableVersion(status.current_version);
  const plugin = store?.plugins_enabled ? arrayOrEmpty(store.plugins).find((entry) => entry?.id === pluginID) : undefined;
  const storeVersion = normalizedStableVersion(plugin?.version);
  const base: UpdateSnapshot = {
    policy: status.policy,
    current_version: status.current_version,
    update_available: false,
    checking: status.checking,
    pending: status.pending,
    checked_at: status.checked_at,
    release_source: "none",
    store_error: storeError ? "plugin store metadata is unavailable" : undefined,
    error: retainedError || undefined,
    runtime: status.runtime,
  };

  if (!storeVersion || !currentVersion) {
    return {
      ...base,
      error: retainedError || "plugin store metadata is unavailable",
    };
  }
  const storeIsNewer = compareStableVersions(storeVersion.parts, currentVersion.parts) > 0;

  return {
    ...base,
    latest_version: storeVersion.value,
    update_available: storeIsNewer,
    release_url: `${pluginReleaseBaseURL}${storeVersion.value}`,
    release_source: "plugin_store",
    error: retainedError || undefined,
  };
}

async function loadPluginStore(): Promise<{ response: PluginStoreResponse | null; error: string }> {
  return getPluginStore().then(
    (response) => ({ response, error: "" }),
    () => ({ response: null, error: "plugin store metadata is unavailable" }),
  );
}

export async function getEffectiveUpdateStatus(checkNow = false): Promise<UpdateSnapshot> {
  const [status, store] = await Promise.all([
    checkNow ? checkForUpdates() : getUpdateStatus(),
    loadPluginStore(),
  ]);
  return reconcileUpdateStatus(status, store.response, store.error);
}

export async function installPluginUpdate(version: string): Promise<PluginInstallResult> {
  try {
    const requestedVersion = normalizedStableVersion(version);
    if (!requestedVersion) {
      throw new APIError(400, "plugin store install response was invalid");
    }
    const store = await getPluginStore();
    const plugin = store.plugins_enabled ? arrayOrEmpty(store.plugins).find((entry) => entry.id === pluginID) : undefined;
    const storeVersion = normalizedStableVersion(plugin?.version);
    if (!plugin || !storeVersion || compareStableVersions(storeVersion.parts, requestedVersion.parts) !== 0) {
      throw new APIError(404, "ui.the_account_manager_plugin_was_not_found_in_the_plugin_store");
    }
    const installed = await managementRequest<PluginInstallResult>("/plugin-store/cpa-account-config-manager/install", {
      method: "POST",
      body: JSON.stringify({ version: requestedVersion.value }),
    });
    const installedVersion = normalizedStableVersion(installed.version);
    if (installed.status !== "installed" || installed.id !== pluginID || !installedVersion ||
      compareStableVersions(installedVersion.parts, requestedVersion.parts) !== 0) {
      throw new APIError(502, "plugin store install response was invalid");
    }
    const result: PluginInstallResult = {
      status: "installed",
      id: pluginID,
      version: installedVersion.value,
      restart_required: installed.restart_required === true,
    };
    await recordBrowserOperation("update_install", result.restart_required ? "warning" : "succeeded", result.version).catch(() => undefined);
    return result;
  } catch (error) {
    await recordBrowserOperation("update_install", "failed", version).catch(() => undefined);
    throw error;
  }
}

export async function recordBrowserOperation(action: "update_install", status: "succeeded" | "failed" | "warning", version?: string): Promise<void> {
  const controller = new AbortController();
  const timeout = globalThis.setTimeout(() => controller.abort(), 3_000);
  try {
    await request("/operations/record", {
      method: "POST",
      body: JSON.stringify({ action, status, version }),
      signal: controller.signal,
    });
  } finally {
    globalThis.clearTimeout(timeout);
  }
}

export async function listOperations(page: number, filters: OperationFilters = {}, signal?: AbortSignal): Promise<OperationListResponse> {
  const query = new URLSearchParams({ page: String(page), page_size: "500" });
  if (filters.category) query.set("category", filters.category);
  if (filters.status) query.set("status", filters.status);
  if (filters.source) query.set("source", filters.source);
  if (filters.search) query.set("search", filters.search);
  const controller = new AbortController();
  const abortFromCaller = () => controller.abort();
  if (signal?.aborted) controller.abort();
  else signal?.addEventListener("abort", abortFromCaller, { once: true });
  const timeout = globalThis.setTimeout(() => controller.abort(), 15_000);
  let response: OperationListResponse;
  try {
    response = await request<OperationListResponse>("/operations", { signal: controller.signal }, query);
  } finally {
    globalThis.clearTimeout(timeout);
    signal?.removeEventListener("abort", abortFromCaller);
  }
  const total = Number.isFinite(response.total) ? Math.max(0, response.total) : 0;
  return {
    ...response,
    operations: arrayOrEmpty(response.operations),
    total,
    page_size: 500,
    extended_history: response.extended_history === true,
    archived_segments: Number.isFinite(response.archived_segments) ? Math.max(0, response.archived_segments) : 0,
    retention_limit: 500,
    retained: Number.isFinite(response.retained) ? Math.max(0, response.retained) : total,
  };
}

export async function saveOperationRetentionSettings(extendedHistory: boolean): Promise<OperationRetentionSettings> {
	await persistPluginSettings({ operation_settings: { extended_history: extendedHistory } });
  return request<OperationRetentionSettings>("/operations/settings", {
    method: "PUT",
    body: JSON.stringify({ extended_history: extendedHistory }),
  });
}

export async function getOperationRetentionSettings(): Promise<OperationRetentionSettings> {
	return request<OperationRetentionSettings>("/operations/settings");
}

export async function persistCurrentSettings(): Promise<ExperimentalSettings> {
	const [defaults, inspection, updates, operations, experiments] = await Promise.all([
		getDefaultPolicy(),
		getInspection(),
		getUpdateStatus(),
		getOperationRetentionSettings(),
		getExperimentalSettings(),
	]);
	await persistPluginSettings({
		default_policy: defaults.policy,
		inspection_policy: inspection.policy,
		update_policy: updates.policy,
		operation_settings: { extended_history: operations.extended_history === true },
		experimental_settings: experiments.settings,
	});
	return experiments.settings;
}

export async function clearOperations(): Promise<{ operation: OperationEntry; retained: number }> {
  return request<{ operation: OperationEntry; retained: number }>("/operations", { method: "DELETE" });
}

export async function downloadOperationExport(format: OperationExportFormat, filters: OperationFilters = {}): Promise<{ filename: string; exported?: number }> {
  const session = getSession();
  if (!session) throw new APIError(401, "ui.management_key_is_not_set");
  const query = new URLSearchParams({ format });
  if (filters.category) query.set("category", filters.category);
  if (filters.status) query.set("status", filters.status);
  if (filters.source) query.set("source", filters.source);
  if (filters.search) query.set("search", filters.search);
  const response = await fetch(buildURL("/operations/export", query), {
    headers: { Authorization: `Bearer ${session.managementKey}` },
  });
  if (!response.ok) {
    let message = `Export failed (${response.status})`;
    try {
      const body = (await response.json()) as { error?: string };
      if (body.error) message = body.error;
    } catch {
      // Keep the status-only error when the response is not JSON.
    }
    throw new APIError(response.status, message);
  }
  const disposition = response.headers.get("Content-Disposition") ?? "";
  const match = disposition.match(/filename="?([^";]+)"?/i);
  const filename = match?.[1] ?? `cpa-account-operations.${format}`;
  const href = URL.createObjectURL(await response.blob());
  const anchor = document.createElement("a");
  anchor.href = href;
  anchor.download = filename;
  anchor.click();
  URL.revokeObjectURL(href);
  return { filename, exported: numericHeader(response.headers.get("X-Exported-Operations")) };
}

export async function createForceSyncPreview(): Promise<ForceSyncPreview> {
	return request<ForceSyncPreview>("/defaults/force/preview", { method: "POST" });
}

export async function startForceSync(previewID: string): Promise<ForceSyncJobSnapshot> {
	return request<ForceSyncJobSnapshot>("/defaults/force/start", {
		method: "POST",
		body: JSON.stringify({ preview_id: previewID }),
	});
}

export async function getForceSyncStatus(includeResults = true): Promise<ForceSyncJobSnapshot> {
	const query = new URLSearchParams();
	if (!includeResults) query.set("light", "1");
	return request<ForceSyncJobSnapshot>("/defaults/force/status", {}, query);
}

export async function createImportPreview(files: File[]): Promise<ImportPreview> {
  const body = new FormData();
  files.forEach((file) => body.append("files", file, file.name));
  return request<ImportPreview>("/import/preview", {
    method: "POST",
    body,
  });
}

export async function startImport(previewID: string): Promise<ImportResult> {
  return request<ImportResult>("/import/start", {
    method: "POST",
    body: JSON.stringify({ preview_id: previewID }),
  });
}

export async function getImportStatus(): Promise<ImportResult> {
  const result = await request<ImportResult>("/import/status");
  return { ...result, results: arrayOrEmpty(result.results) };
}

export interface ExportDownloadResult {
  filename: string;
  exported?: number;
  skipped?: number;
}

export async function downloadExport(kind: "accounts", format: AccountExportFormat, scope?: TargetScope): Promise<ExportDownloadResult>;
export async function downloadExport(kind: "results", format: ResultExportFormat, filters?: undefined): Promise<ExportDownloadResult>;
export async function downloadExport(kind: "accounts" | "results", format: ExportFormat, scope?: TargetScope): Promise<ExportDownloadResult> {
  const session = getSession();
  if (!session) throw new APIError(401, "ui.management_key_is_not_set");
  const query = kind === "accounts" && scope?.mode === "filtered" ? filtersQuery(scope.filters ?? {}) : new URLSearchParams();
  query.set("format", format);
  const headers = new Headers({ Authorization: `Bearer ${session.managementKey}` });
  const selected = kind === "accounts" && scope?.mode === "selected";
  if (selected) headers.set("Content-Type", "application/json");
  const response = await fetch(buildURL(`/export/${kind}`, query), {
    method: selected ? "POST" : "GET",
    headers,
    ...(selected ? { body: JSON.stringify({ scope }) } : {}),
  });
  if (!response.ok) {
    let message = `Export failed (${response.status})`;
    try {
      const body = (await response.json()) as { error?: string };
      if (body.error) message = body.error;
    } catch {
      // Keep the status-only error when the response is not JSON.
    }
    throw new APIError(response.status, message);
  }
  const disposition = response.headers.get("Content-Disposition") ?? "";
  const match = disposition.match(/filename="?([^";]+)"?/i);
  const filename = match?.[1] ?? `cpa-account-config-${kind}.${format}`;
  const href = URL.createObjectURL(await response.blob());
  const anchor = document.createElement("a");
  anchor.href = href;
  anchor.download = filename;
  anchor.click();
  URL.revokeObjectURL(href);
  const exported = numericHeader(response.headers.get("X-Exported-Accounts"));
  const skipped = numericHeader(response.headers.get("X-Skipped-Accounts"));
  return { filename, exported, skipped };
}

function numericHeader(value: string | null): number | undefined {
  if (value === null || value.trim() === "") return undefined;
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed >= 0 ? parsed : undefined;
}

export const AI_PROVIDER_CHANNELS: Array<{ kind: AIProviderChannelKind; labelKey: string; apiPath: string }> = [
  { kind: "openai-compatibility", labelKey: "ui.ai_provider_channel_openai_compatibility", apiPath: "/openai-compatibility" },
  { kind: "gemini-api-key", labelKey: "ui.ai_provider_channel_gemini", apiPath: "/gemini-api-key" },
  { kind: "interactions-api-key", labelKey: "ui.ai_provider_channel_interactions", apiPath: "/interactions-api-key" },
  { kind: "claude-api-key", labelKey: "ui.ai_provider_channel_claude", apiPath: "/claude-api-key" },
  { kind: "codex-api-key", labelKey: "ui.ai_provider_channel_codex", apiPath: "/codex-api-key" },
  { kind: "xai-api-key", labelKey: "ui.ai_provider_channel_xai", apiPath: "/xai-api-key" },
  { kind: "vertex-api-key", labelKey: "ui.ai_provider_channel_vertex", apiPath: "/vertex-api-key" },
  { kind: "api-keys", labelKey: "ui.ai_provider_channel_api_keys", apiPath: "/api-keys" },
  { kind: "opencode-go", labelKey: "ui.ai_provider_channel_opencode", apiPath: "" },
  { kind: "opencode-zen", labelKey: "ui.ai_provider_channel_opencode_zen", apiPath: "" },
];

function channelModelFromJSON(source: Record<string, unknown>): AIProviderChannelModel {
  const model: AIProviderChannelModel = { name: typeof source["name"] === "string" ? source["name"] : "" };
  if (typeof source["alias"] === "string") model.alias = source["alias"];
  if (typeof source["display-name"] === "string") model.display_name = source["display-name"];
  if (typeof source["max-context-length"] === "number") model.max_context_length = source["max-context-length"];
  if (typeof source["force-mapping"] === "boolean") model.force_mapping = source["force-mapping"];
  if (typeof source["is-compat"] === "boolean") model.is_compat = source["is-compat"];
  if (typeof source["image"] === "boolean") model.image = source["image"];
  if (Array.isArray(source["input-modalities"])) model.input_modalities = source["input-modalities"] as string[];
  if (Array.isArray(source["output-modalities"])) model.output_modalities = source["output-modalities"] as string[];
  if (source["thinking"] !== undefined && source["thinking"] !== null) model.thinking = source["thinking"];
  return model;
}

function mapAPIKeyEntryFromJSON(source: Record<string, unknown>): AIProviderAPIKeyEntry {
  const entry: AIProviderAPIKeyEntry = {};
  if (typeof source["api-key"] === "string") entry.api_key = source["api-key"];
  if (source["weight"] !== undefined && source["weight"] !== null) entry.weight = Number(source["weight"]);
  if (typeof source["proxy-url"] === "string") entry.proxy_url = source["proxy-url"];
  return entry;
}

function channelEntriesFromResponse(kind: AIProviderChannelKind, payload: unknown): AIProviderChannelEntry[] {
  const record = (payload ?? {}) as Record<string, unknown>;
  const raw = record[kind];
  if (!Array.isArray(raw)) return [];
  return raw.map((item, index) => {
    if (typeof item === "string") {
      return { index, api_key: item };
    }
    const source = (item ?? {}) as Record<string, unknown>;
    const entry: AIProviderChannelEntry = { index };
    if (typeof source["name"] === "string") entry.name = source["name"];
    if (typeof source["api-key"] === "string") entry.api_key = source["api-key"];
    if (Array.isArray(source["api-key-entries"])) {
      entry.api_key_entries = (source["api-key-entries"] as Array<Record<string, unknown>>).map(mapAPIKeyEntryFromJSON);
      const firstKey = entry.api_key_entries.find((keyEntry) => keyEntry.api_key);
      if (firstKey?.api_key) entry.api_key = firstKey.api_key;
    }
    if (typeof source["base-url"] === "string") entry.base_url = source["base-url"];
    if (typeof source["proxy-url"] === "string") entry.proxy_url = source["proxy-url"];
    if (typeof source["prefix"] === "string") entry.prefix = source["prefix"];
    if (typeof source["priority"] === "number") entry.priority = source["priority"];
    if (typeof source["disabled"] === "boolean") entry.disabled = source["disabled"];
    if (source["weight"] !== undefined && source["weight"] !== null) entry.weight = Number(source["weight"]);
    if (source["headers"] !== undefined && source["headers"] !== null) entry.headers = source["headers"] as Record<string, string>;
    if (Array.isArray(source["models"])) entry.models = (source["models"] as Array<Record<string, unknown>>).map(channelModelFromJSON);
    if (Array.isArray(source["excluded-models"])) entry.excluded_models = source["excluded-models"] as string[];
    if (typeof source["support-prompt-cache-key"] === "boolean") entry.support_prompt_cache_key = source["support-prompt-cache-key"];
    if (typeof source["disable-cooling"] === "boolean") entry.disable_cooling = source["disable-cooling"];
    if (typeof source["alpha-search"] === "boolean") entry.alpha_search = source["alpha-search"];
    if (typeof source["websockets"] === "boolean") entry.websockets = source["websockets"];
    if (typeof source["rebuild-mid-system-message"] === "boolean") entry.rebuild_mid_system_message = source["rebuild-mid-system-message"];
    if (typeof source["auth-index"] === "string") entry.auth_index = source["auth-index"];
    return entry;
  });
}

/**
 * Fetch every provider channel, sequentially. Failures on individual channels
 * degrade to an empty entry list (opencode-go / opencode-zen / missing host
 * channels), but an authentication or throttling failure (401/403) aborts the
 * whole refresh immediately instead of hammering the host with more requests:
 * the CPA management API bans an IP after 5 failed attempts, so a wrong key
 * must surface once rather than as a burst.
 */
export async function getAIProviderRuntime(): Promise<AIProviderRuntimeResponse> {
  return managementRequest<AIProviderRuntimeResponse>("/ai-providers/runtime");
}

export async function listAIProviderChannels(): Promise<AIProviderChannelSnapshot[]> {
  const channels: AIProviderChannelSnapshot[] = [];
  for (const channel of AI_PROVIDER_CHANNELS) {
    try {
      if (channel.kind === "opencode-go") {
        const listed = await listOpenCodeAccounts();
        channels.push({
          kind: channel.kind,
          count: listed.accounts?.length ?? 0,
          entries: (listed.accounts ?? []).map((account, index) => ({
            index,
            account_id: account.id,
            workspace_id: account.workspace_id,
            name: account.workspace_id,
          })),
        });
        continue;
      }
      if (channel.kind === "opencode-zen") {
        const listed = await listOpenCodeZenAccounts();
        channels.push({
          kind: channel.kind,
          count: listed.accounts?.length ?? 0,
          entries: (listed.accounts ?? []).map((account, index) => ({
            index,
            account_id: account.id,
            name: account.name ?? account.base_url,
            base_url: account.base_url,
            key_set: account.key_set,
          })),
        });
        continue;
      }
      if (channel.apiPath) {
        const payload = await managementRequest<unknown>(channel.apiPath);
        const entries = channelEntriesFromResponse(channel.kind, payload);
        channels.push({ kind: channel.kind, count: entries.length, entries });
        continue;
      }
      channels.push({ kind: channel.kind, count: 0, entries: [] as AIProviderChannelEntry[] });
    } catch (caught) {
      if (caught instanceof APIError && (caught.status === 401 || caught.status === 403)) {
        throw caught;
      }
      channels.push({ kind: channel.kind, count: 0, entries: [] as AIProviderChannelEntry[] });
    }
  }
  return channels;
}

export async function putAIProviderChannel(kind: AIProviderChannelKind, items: unknown[]): Promise<void> {
  await managementRequest<void>(`/${kind}`, {
    method: "PUT",
    body: JSON.stringify(items),
  });
}

async function getRawAIProviderChannelItems(kind: AIProviderChannelKind): Promise<unknown[]> {
  const payload = (await managementRequest<unknown>(`/${kind}`)) as Record<string, unknown>;
  const raw = payload[kind];
  if (!Array.isArray(raw)) throw new Error(kind + " channel is not available");
  return raw.map((item) => {
    if (typeof item === "string") return item;
    return stripAuthIndex(item);
  });
}

export async function deleteAIProviderChannelEntry(kind: AIProviderChannelKind, index: number, accountID?: string): Promise<void> {
  if (kind === "opencode-go") {
    if (!accountID) throw new Error("OpenCode account id is required");
    await removeOpenCodeAccount(accountID);
    return;
  }
  if (kind === "opencode-zen") {
    if (!accountID) throw new Error("OpenCode Zen account id is required");
    await removeOpenCodeZenAccount(accountID);
    return;
  }
  await managementRequest<void>(`/${kind}?index=${index}`, { method: "DELETE" });
}

export async function patchAIProviderChannelEntry(kind: AIProviderChannelKind, index: number, value: Record<string, unknown> | string): Promise<void> {
  await managementRequest<void>(`/${kind}`, {
    method: "PATCH",
    body: JSON.stringify({ index, value }),
  });
}

/** Strip auth-index metadata so a channel object can be written back to the host. */
function stripAuthIndex(item: unknown): Record<string, unknown> {
  const copy = { ...((item ?? {}) as Record<string, unknown>) };
  delete copy["auth-index"];
  return copy;
}

/** Serialize one AIProviderChannelModel into the host JSON shape (kebab-case tags). */
function modelToJSON(model: AIProviderChannelModel): Record<string, unknown> {
  const out: Record<string, unknown> = { name: model.name };
  if (model.alias !== undefined && model.alias !== "") out["alias"] = model.alias;
  if (model.display_name !== undefined && model.display_name !== "") out["display-name"] = model.display_name;
  if (model.max_context_length !== undefined) out["max-context-length"] = model.max_context_length;
  if (model.force_mapping === true) out["force-mapping"] = true;
  if (model.is_compat === true) out["is-compat"] = true;
  if (model.image === true) out["image"] = true;
  if (model.input_modalities && model.input_modalities.length > 0) out["input-modalities"] = model.input_modalities;
  if (model.output_modalities && model.output_modalities.length > 0) out["output-modalities"] = model.output_modalities;
  if (model.thinking !== undefined) out["thinking"] = model.thinking;
  return out;
}

function normalizedNumber(value: number | string | null | undefined): number | null | undefined {
  if (value === null) return null;
  if (typeof value === "number") return value;
  if (value === undefined || value === "") return undefined;
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : undefined;
}

function keyEntriesToJSON(entries: Array<{ api_key?: string; weight?: number | string | null; proxy_url?: string }> | undefined): Record<string, unknown>[] | undefined {
  if (!entries || entries.length === 0) return undefined;
  return entries.map((entry) => {
    const out: Record<string, unknown> = {};
    if (entry.api_key) out["api-key"] = entry.api_key;
    if (entry.weight !== undefined && entry.weight !== null) {
      const parsed = Number(entry.weight);
      if (Number.isFinite(parsed)) out["weight"] = parsed;
    }
    if (entry.proxy_url) out["proxy-url"] = entry.proxy_url;
    return out;
  });
}

export interface AIProviderChannelEntryPatch {
  name?: string;
  api_key?: string;
  base_url?: string;
  proxy_url?: string;
  prefix?: string;
  priority?: number | string | null;
  weight?: number | string | null;
  disabled?: boolean;
  headers?: Record<string, string>;
  excluded_models?: string[];
  models?: AIProviderChannelModel[];
  api_key_entries?: Array<{ api_key?: string; weight?: number | string | null; proxy_url?: string }>;
  support_prompt_cache_key?: boolean;
  disable_cooling?: boolean;
  alpha_search?: boolean;
  websockets?: boolean;
  rebuild_mid_system_message?: boolean;
}

/**
 * Update one host-managed channel entry by rewriting the whole channel list
 * (PUT). The host GET returns full plaintext objects, so untouched fields are
 * preserved verbatim, and the documented PATCH gap (priority, websockets,
 * disable-cooling, model alias rows, ...) is covered by the full rewrite.
 */
export async function saveAIProviderChannelEntry(
  kind: AIProviderChannelKind,
  index: number,
  patch: AIProviderChannelEntryPatch,
): Promise<void> {
  if (kind === "opencode-go" || kind === "opencode-zen") {
    throw new Error("saveAIProviderChannelEntry only supports host-managed channels");
  }
  const raw = await getRawAIProviderChannelItems(kind);
  if (index < 0 || index >= raw.length) throw new Error(kind + " entry #" + (index + 1) + " was not found");

  const items = raw.map((item) => typeof item === "string" ? { "api-key": item } : { ...(item as Record<string, unknown>) });
  const target = items[index];
  const patched: Record<string, unknown> = { ...target };

  const replacementAPIKey = patch.api_key?.trim() ?? "";
  if (kind === "openai-compatibility") {
    const keyEntries = patch.api_key_entries !== undefined
      ? (keyEntriesToJSON(patch.api_key_entries) ?? [])
      : Array.isArray(patched["api-key-entries"])
        ? (patched["api-key-entries"] as Record<string, unknown>[]).map((entry) => ({ ...entry }))
        : [];
    if (replacementAPIKey) {
      if (keyEntries.length === 0) keyEntries.push({ "api-key": replacementAPIKey });
      else keyEntries[0] = { ...keyEntries[0], "api-key": replacementAPIKey };
    }
    if (patch.api_key_entries !== undefined || replacementAPIKey) patched["api-key-entries"] = keyEntries;
    // OpenAI-compatible credentials only exist inside api-key-entries. A
    // top-level api-key is accepted by JSON but ignored by CPA.
    delete patched["api-key"];
  } else if (replacementAPIKey) {
    patched["api-key"] = replacementAPIKey;
  }
  if (patch.name !== undefined) patched["name"] = patch.name.trim();
  if (patch.base_url !== undefined) {
    const baseURL = patch.base_url.trim();
    if (baseURL) patched["base-url"] = baseURL;
    else delete patched["base-url"];
  }
  if (patch.proxy_url !== undefined) {
    const proxyURL = patch.proxy_url.trim();
    if (proxyURL) patched["proxy-url"] = proxyURL;
    else delete patched["proxy-url"];
  }
  if (patch.prefix !== undefined) {
    const prefix = patch.prefix.trim();
    if (prefix) patched["prefix"] = prefix;
    else delete patched["prefix"];
  }
  const priority = normalizedNumber(patch.priority);
  if (priority !== undefined) {
    if (priority === null) delete patched["priority"];
    else patched["priority"] = priority;
  }
  const weight = normalizedNumber(patch.weight);
  if (weight !== undefined) {
    if (weight === null) delete patched["weight"];
    else patched["weight"] = weight;
  }
  if (patch.disabled !== undefined && kind === "openai-compatibility") patched["disabled"] = patch.disabled;
  if (patch.support_prompt_cache_key !== undefined) patched["support-prompt-cache-key"] = patch.support_prompt_cache_key;
  if (patch.disable_cooling !== undefined) patched["disable-cooling"] = patch.disable_cooling;
  if (patch.alpha_search !== undefined) patched["alpha-search"] = patch.alpha_search;
  if (patch.websockets !== undefined) patched["websockets"] = patch.websockets;
  if (patch.rebuild_mid_system_message !== undefined) patched["rebuild-mid-system-message"] = patch.rebuild_mid_system_message;
  if (patch.headers !== undefined) {
    if (Object.keys(patch.headers).length > 0) patched["headers"] = patch.headers;
    else delete patched["headers"];
  }
  if (patch.excluded_models !== undefined) {
    if (patch.excluded_models.length > 0) patched["excluded-models"] = patch.excluded_models;
    else delete patched["excluded-models"];
  }
  if (patch.models !== undefined) patched["models"] = patch.models.map(modelToJSON);
  if (patch.api_key_entries !== undefined && kind !== "openai-compatibility") {
    patched["api-key-entries"] = keyEntriesToJSON(patch.api_key_entries);
  }

  items[index] = patched;
  await putAIProviderChannel(kind, items);
}

export interface AIProviderProbeResult {
  reachable: boolean;
  status_code?: number;
  detail?: string;
}

export async function testAIProviderChannel(baseURL: string, apiKey: string, timeoutSeconds = 15, headers?: Record<string, string>): Promise<AIProviderProbeResult> {
  return testAIProviderChannelForKind("openai-compatibility", baseURL, apiKey, timeoutSeconds, headers);
}

export async function testAIProviderChannelForKind(kind: AIProviderChannelKind, baseURL: string, apiKey: string, timeoutSeconds = 15, headers?: Record<string, string>): Promise<AIProviderProbeResult> {
  const response = await request<AIProviderProbeResult>("/ai-providers/test", {
    method: "POST",
    body: JSON.stringify({ kind, base_url: baseURL, api_key: apiKey, timeout_seconds: timeoutSeconds, ...(headers && Object.keys(headers).length > 0 ? { headers } : {}) }),
  });
  return response;
}

/** Toggle a channel's enabled/disabled state through the host PATCH semantics. */
export async function setAIProviderChannelEnabled(kind: AIProviderChannelKind, index: number, enabled: boolean): Promise<void> {
  if (kind === "opencode-go" || kind === "opencode-zen") return;
  if (kind === "openai-compatibility") {
    await patchAIProviderChannelEntry(kind, index, { disabled: !enabled });
    return;
  }
  // API-key channels gate credentials through weight (0 excludes the credential).
  // Preserve the configured non-zero weight when re-enabling; blindly writing 1
  // used to turn a weighted key (for example 100) into a different route.
  const items = await getRawAIProviderChannelItems(kind);
  const raw = items[index];
  const current = typeof raw === "string" ? undefined : Number((raw as Record<string, unknown>)?.weight);
  const restoredWeight = typeof current === "number" && Number.isFinite(current) && current > 0 ? current : 1;
  await patchAIProviderChannelEntry(kind, index, { weight: enabled ? restoredWeight : 0 });
}

export interface NewOpenAICompatibilityProvider {
  name: string;
  base_url: string;
  api_key: string;
}

export interface NewOpenCodeGoProvider {
  workspace_id: string;
  auth_cookie: string;
}

export interface NewOpenCodeZenProvider {
  name?: string;
  base_url: string;
  api_key: string;
}

export interface NewAPIKeyProvider {
  api_key: string;
  base_url?: string;
}

export type NewAIProvider = NewOpenAICompatibilityProvider | NewOpenCodeGoProvider | NewOpenCodeZenProvider | NewAPIKeyProvider;

const apiKeyChannelKinds: AIProviderChannelKind[] = [
  "gemini-api-key",
  "interactions-api-key",
  "claude-api-key",
  "codex-api-key",
  "xai-api-key",
  "vertex-api-key",
];

export async function addAIProviderChannel(kind: AIProviderChannelKind, provider: NewAIProvider): Promise<void> {
  if (kind === "opencode-go") {
    const opencode = provider as NewOpenCodeGoProvider;
    await saveOpenCodeAccount(opencode.workspace_id, opencode.auth_cookie);
    return;
  }
  if (kind === "opencode-zen") {
    const zen = provider as NewOpenCodeZenProvider;
    await saveOpenCodeZenAccount({
      name: zen.name || undefined,
      base_url: zen.base_url,
      zen_api_key: zen.api_key,
    });
    return;
  }
  if (kind === "api-keys") {
    const apiKey = (provider as NewAPIKeyProvider).api_key.trim();
    const items = await getRawAIProviderChannelItems(kind);
    items.push(apiKey);
    await putAIProviderChannel(kind, items);
    return;
  }
  const openai = provider as NewOpenAICompatibilityProvider;
  const items = await getRawAIProviderChannelItems(kind);
  if (kind === "openai-compatibility") {
    items.push({
      name: openai.name.trim(),
      "base-url": openai.base_url.trim(),
      "api-key-entries": [{ "api-key": openai.api_key.trim() }],
    });
    await putAIProviderChannel(kind, items);
    return;
  }
  // Plain API-key channels (gemini / interactions / claude / codex / xai / vertex).
  const apiKeyProvider = provider as NewAPIKeyProvider;
  items.push({
    "api-key": apiKeyProvider.api_key.trim(),
    ...(apiKeyProvider.base_url?.trim() ? { "base-url": apiKeyProvider.base_url.trim() } : {}),
  });
  await putAIProviderChannel(kind, items);
}

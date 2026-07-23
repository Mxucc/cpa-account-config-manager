import { getSession } from "../store/session";
import type {
  AccountDeletePreview,
  AccountDeleteResult,
  AccountFilters,
  AccountExportFormat,
  AccountListResponse,
  BatchPatch,
  BatchPreview,
	CPAServerVersionSnapshot,
	DefaultPolicy,
	ExperimentalSettings,
	ExperimentalSettingsSnapshot,
	AgentIdentitySessionLoginResponse,
	ForceSyncJobSnapshot,
	ForceSyncPreview,
  ExportFormat,
  ImportPreview,
  ImportResult,
  InspectionAction,
  InspectionDeleteRun,
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
): Promise<AccountListResponse> {
  const query = filtersQuery(filters);
  query.set("page", String(page));
  query.set("page_size", String(pageSize));
  const response = await request<AccountListResponse>("/accounts", {}, query);
  return { ...response, accounts: arrayOrEmpty(response.accounts) };
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
    const store = await getPluginStore();
    if (!arrayOrEmpty(store.plugins).some((plugin) => plugin.id === pluginID)) {
      throw new APIError(404, "ui.the_account_manager_plugin_was_not_found_in_the_plugin_store");
    }
    const result = await managementRequest<PluginInstallResult>("/plugin-store/cpa-account-config-manager/install", {
      method: "POST",
      body: JSON.stringify({ version }),
    });
    void recordBrowserOperation("update_install", result.restart_required ? "warning" : "succeeded", result.version).catch(() => undefined);
    return result;
  } catch (error) {
    void recordBrowserOperation("update_install", "failed", version).catch(() => undefined);
    throw error;
  }
}

export async function recordBrowserOperation(action: "update_install", status: "succeeded" | "failed" | "warning", version?: string): Promise<void> {
  await request("/operations/record", {
    method: "POST",
    body: JSON.stringify({ action, status, version }),
  });
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

export async function persistCurrentSettings(): Promise<void> {
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

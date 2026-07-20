import { getSession } from "../store/session";
import type {
  AccountDeletePreview,
  AccountDeleteResult,
  AccountFilters,
  AccountExportFormat,
  AccountListResponse,
  BatchPatch,
  BatchPreview,
	DefaultPolicy,
	ForceSyncJobSnapshot,
	ForceSyncPreview,
  ExportFormat,
  ImportPreview,
  ImportResult,
  InspectionAction,
  InspectionDeleteRun,
  InspectionPolicy,
  InspectionResultList,
  InspectionSnapshot,
  JobSnapshot,
  ModelTestResult,
  OperationEntry,
  OperationExportFormat,
  OperationFilters,
  OperationListResponse,
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

export async function testAccountModel(accountID: string, model: string): Promise<ModelTestResult> {
  return request<ModelTestResult>("/accounts/model-test", {
    method: "POST",
    body: JSON.stringify({ account_id: accountID, model: model.trim() }),
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

export async function startBatch(previewID: string): Promise<JobSnapshot> {
  return request<JobSnapshot>("/batch/start", {
    method: "POST",
    body: JSON.stringify({ preview_id: previewID }),
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

export async function saveDefaultPolicy(policy: DefaultPolicy): Promise<PolicySnapshot> {
	await request<{ status: string }>("/config", {
		method: "PATCH",
		body: JSON.stringify({ default_policy: policy }),
	});
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

export async function saveInspectionPolicy(policy: InspectionPolicy, confirmAutoDelete = false): Promise<InspectionSnapshot> {
  return request<InspectionSnapshot>("/inspection", {
    method: "PUT",
    body: JSON.stringify({ ...policy, confirm_auto_delete: confirmAutoDelete }),
  });
}

export async function scanInspection(): Promise<InspectionSnapshot> {
  return request<InspectionSnapshot>("/inspection/scan", { method: "POST" });
}

export async function listInspectionResults(page: number, pageSize: number, health = "", search = ""): Promise<InspectionResultList> {
  const query = new URLSearchParams({ page: String(page), page_size: String(pageSize) });
  if (health) query.set("health", health);
  if (search) query.set("search", search);
  const response = await request<InspectionResultList>("/inspection/results", {}, query);
  return { ...response, results: arrayOrEmpty(response.results) };
}

export async function listInspectionActions(limit = 50): Promise<InspectionAction[]> {
  const query = new URLSearchParams({ limit: String(limit) });
  const response = await request<{ actions: InspectionAction[] }>("/inspection/actions", {}, query);
  return arrayOrEmpty(response.actions);
}

export async function executeInspectionAutoDelete(): Promise<InspectionDeleteRun> {
  return request<InspectionDeleteRun>("/inspection/auto-delete", { method: "POST" });
}

export async function getUpdateStatus(): Promise<UpdateSnapshot> {
  return request<UpdateSnapshot>("/updates");
}

export async function saveUpdatePolicy(policy: UpdatePolicy, confirmAutoUpdate = false): Promise<UpdateSnapshot> {
  return request<UpdateSnapshot>("/updates", {
    method: "PUT",
    body: JSON.stringify({ policy, confirm_auto_update: confirmAutoUpdate }),
  });
}

export async function checkForUpdates(): Promise<UpdateSnapshot> {
  return request<UpdateSnapshot>("/updates/check", { method: "POST" });
}

export async function getPluginStore(): Promise<PluginStoreResponse> {
  return managementRequest<PluginStoreResponse>("/plugin-store");
}

export async function installPluginUpdate(version: string): Promise<PluginInstallResult> {
  try {
    const store = await getPluginStore();
    if (!store.plugins.some((plugin) => plugin.id === "cpa-account-config-manager")) {
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

export async function listOperations(page: number, pageSize: number, filters: OperationFilters = {}): Promise<OperationListResponse> {
  const query = new URLSearchParams({ page: String(page), page_size: String(pageSize) });
  if (filters.category) query.set("category", filters.category);
  if (filters.status) query.set("status", filters.status);
  if (filters.source) query.set("source", filters.source);
  if (filters.search) query.set("search", filters.search);
  const response = await request<OperationListResponse>("/operations", {}, query);
  return { ...response, operations: arrayOrEmpty(response.operations) };
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

import { getSession } from "../store/session";
import type {
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
  JobSnapshot,
  PolicySnapshot,
  ResultExportFormat,
  TargetScope,
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
  if (!session) throw new APIError(401, "Management Key 未设置");
  const suffix = query && query.size > 0 ? `?${query.toString()}` : "";
  return `${session.baseUrl}${API_ROOT}${path}${suffix}`;
}

async function request<T>(path: string, init: RequestInit = {}, query?: URLSearchParams): Promise<T> {
  const session = getSession();
  if (!session) throw new APIError(401, "Management Key 未设置");
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
    let message = `请求失败 (${response.status})`;
    try {
      const body = (await response.json()) as { error?: string };
      if (body.error) message = body.error;
    } catch {
      // Keep the status-only error when the response is not JSON.
    }
    throw new APIError(response.status, message);
  }
  return (await response.json()) as T;
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
  return request<AccountListResponse>("/accounts", {}, query);
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
	return request<PolicySnapshot>("/defaults", {
		method: "PUT",
		body: JSON.stringify(policy),
	});
}

export async function scanDefaultPolicy(): Promise<PolicySnapshot> {
	return request<PolicySnapshot>("/defaults/scan", { method: "POST" });
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

export async function downloadExport(kind: "accounts", format: AccountExportFormat, filters?: AccountFilters): Promise<ExportDownloadResult>;
export async function downloadExport(kind: "results", format: ResultExportFormat, filters?: undefined): Promise<ExportDownloadResult>;
export async function downloadExport(kind: "accounts" | "results", format: ExportFormat, filters?: AccountFilters): Promise<ExportDownloadResult> {
  const session = getSession();
  if (!session) throw new APIError(401, "Management Key 未设置");
  const query = kind === "accounts" && filters ? filtersQuery(filters) : new URLSearchParams();
  query.set("format", format);
  const response = await fetch(buildURL(`/export/${kind}`, query), {
    headers: { Authorization: `Bearer ${session.managementKey}` },
  });
  if (!response.ok) {
    let message = `导出失败 (${response.status})`;
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

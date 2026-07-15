import { getSession } from "../store/session";
import type {
  AccountFilters,
  AccountListResponse,
  BatchPatch,
  BatchPreview,
  JobSnapshot,
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
  const response = await fetch(buildURL(path, query), {
    ...init,
    headers: {
      Accept: "application/json",
      Authorization: `Bearer ${session.managementKey}`,
      ...(init.body ? { "Content-Type": "application/json" } : {}),
      ...init.headers,
    },
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

export async function downloadExport(kind: "accounts" | "results", filters?: AccountFilters): Promise<void> {
  const session = getSession();
  if (!session) throw new APIError(401, "Management Key 未设置");
  const query = kind === "accounts" && filters ? filtersQuery(filters) : undefined;
  const response = await fetch(buildURL(`/export/${kind}`, query), {
    headers: { Authorization: `Bearer ${session.managementKey}` },
  });
  if (!response.ok) throw new APIError(response.status, `导出失败 (${response.status})`);
  const disposition = response.headers.get("Content-Disposition") ?? "";
  const match = disposition.match(/filename="?([^";]+)"?/i);
  const filename = match?.[1] ?? `cpa-account-config-${kind}.json`;
  const href = URL.createObjectURL(await response.blob());
  const anchor = document.createElement("a");
  anchor.href = href;
  anchor.download = filename;
  anchor.click();
  URL.revokeObjectURL(href);
}

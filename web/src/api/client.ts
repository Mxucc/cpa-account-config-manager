import { getSession } from "../store/session";
import type {
  Account,
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
	AccountQuotaPolicy,
	ProviderQuotaPolicy,
	QuotaPolicySnapshot,
	ForceSyncJobSnapshot,
	ProxyProfileInput,
	ProxyProfileListResponse,
	ProxyProfileView,
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
const REQUEST_TIMEOUT_MS = 30_000;

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

async function parseJSONResponse<T>(response: Response): Promise<T> {
  if (response.status === 204) return {} as T;
  const text = await response.text();
  if (!text.trim()) return {} as T;
  try {
    return JSON.parse(text) as T;
  } catch {
    throw new APIError(response.status, "ui.invalid_json_response");
  }
}

async function responseErrorMessage(response: Response, fallback: string, preferError = false): Promise<string> {
  try {
    const text = await response.text();
    if (!text.trim()) return fallback;
    const body = JSON.parse(text) as { error?: unknown; message?: unknown };
    if (preferError && typeof body.error === "string" && body.error.trim()) return body.error;
    if (typeof body.message === "string" && body.message.trim()) return body.message;
    if (typeof body.error === "string" && body.error.trim()) return body.error;
  } catch {
    // Keep the status-only error when the response is not JSON.
  }
  return fallback;
}

async function fetchWithTimeout(input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> {
  const controller = new AbortController();
  let timedOut = false;
  const abortFromCaller = () => controller.abort();
  if (init.signal?.aborted) controller.abort();
  else init.signal?.addEventListener("abort", abortFromCaller, { once: true });
  const timeout = globalThis.setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, REQUEST_TIMEOUT_MS);
  try {
    return await fetch(input, { ...init, signal: controller.signal });
  } catch (error) {
    if (timedOut && !init.signal?.aborted) throw new APIError(504, "ui.request_timeout");
    throw error;
  } finally {
    globalThis.clearTimeout(timeout);
    init.signal?.removeEventListener("abort", abortFromCaller);
  }
}

async function request<T>(path: string, init: RequestInit = {}, query?: URLSearchParams): Promise<T> {
  const session = getSession();
  if (!session) throw new APIError(401, "ui.management_key_is_not_set");
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  headers.set("Authorization", `Bearer ${session.managementKey}`);
  const isFormData = typeof FormData !== "undefined" && init.body instanceof FormData;
  if (init.body && !isFormData && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  const response = await fetchWithTimeout(buildURL(path, query), { ...init, headers });
  if (!response.ok) {
    const message = await responseErrorMessage(response, `Request failed (${response.status})`);
    throw new APIError(response.status, message);
  }
  return parseJSONResponse<T>(response);
}

async function requestRecord<T>(path: string, init: RequestInit = {}, query?: URLSearchParams): Promise<T> {
  const response = await request<unknown>(path, init, query);
  if (!isRecord(response)) {
    throw new APIError(502, "ui.invalid_json_response");
  }
  return response as T;
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
  const isFormData = typeof FormData !== "undefined" && init.body instanceof FormData;
  if (init.body && !isFormData && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  const response = await fetchWithTimeout(buildManagementURL(path), { ...init, headers });
  if (!response.ok) {
    const message = await responseErrorMessage(response, `Request failed (${response.status})`, true);
    throw new APIError(response.status, message);
  }
  return parseJSONResponse<T>(response);
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

export async function getCPAServerVersionStatus(signal?: AbortSignal): Promise<CPAServerVersionSnapshot> {
  const session = getSession();
  if (!session) throw new APIError(401, "ui.management_key_is_not_set");
  const headers = new Headers({ Accept: "application/json", Authorization: `Bearer ${session.managementKey}` });
  const controller = new AbortController();
  const abortFromCaller = () => controller.abort();
  if (signal?.aborted) controller.abort();
  else signal?.addEventListener("abort", abortFromCaller, { once: true });
  const timeout = window.setTimeout(() => controller.abort(), 15_000);
  let response: Response;
  try {
    response = await fetch(buildManagementURL("/latest-version"), { headers, signal: controller.signal });
  } finally {
    window.clearTimeout(timeout);
    signal?.removeEventListener("abort", abortFromCaller);
  }
  if (response.status === 401) throw new APIError(401, "ui.authentication_failed");

  const currentVersion = safeCPAVersionLabel(response.headers.get("X-CPA-Version") || response.headers.get("X-Server-Version"));
  const currentBuildDate = safeCPAHeaderText(response.headers.get("X-CPA-Build-Date") || response.headers.get("X-Server-Build-Date"));
  let latestVersion = "";
  if (response.ok) {
    try {
      const payload = await parseJSONResponse<Record<string, unknown>>(response);
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

function finiteNonNegativeInteger(value: unknown, fallback: number, max: number): number {
  const number = Number(value);
  if (!Number.isFinite(number)) return fallback;
  return Math.min(max, Math.max(0, Math.floor(number)));
}

function isFiniteNonNegativeNumber(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value) && value >= 0;
}

function isFiniteNonNegativeInteger(value: unknown): value is number {
  return isFiniteNonNegativeNumber(value) && Number.isInteger(value);
}

function hasRequiredNonNegativeIntegers(source: Record<string, unknown>, keys: readonly string[]): boolean {
  return keys.every((key) => Object.prototype.hasOwnProperty.call(source, key) && isFiniteNonNegativeInteger(source[key]));
}

function isValidAccountConcurrencyAvailability(value: unknown): value is AccountConcurrencyAvailability {
  return isRecord(value)
    && typeof value.supported === "boolean"
    && hasRequiredNonNegativeIntegers(value, ["host_schema_version", "required_schema_version"])
    && (value.reason === undefined || value.reason === "host_schema_v2_required")
    && (value.storage_error === undefined || typeof value.storage_error === "string");
}

function isValidAccountConcurrencySummary(value: unknown): value is NonNullable<AccountEditableConfig["concurrency"]> {
  return isRecord(value)
    && typeof value.supported === "boolean"
    && hasRequiredNonNegativeIntegers(value, ["active", "limit"]);
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value.trim().length > 0;
}

/**
 * Go may encode an empty nil slice as null on older plugin/runtime pairs, so
 * null remains a compatible empty list. A missing field or a list containing
 * malformed rows is different: callers must surface that as a broken API
 * response instead of replacing valid UI state with an apparently empty list.
 */
function nullableRecordArray(value: unknown): Record<string, unknown>[] | undefined {
  if (value === null) return [];
  if (!Array.isArray(value) || value.some((item) => !isRecord(item))) return undefined;
  return value as Record<string, unknown>[];
}

function normalizeAccountListResponse(response: unknown, requestedPage: number, requestedPageSize: number): AccountListResponse {
  if (!isRecord(response) || !Array.isArray(response.accounts) || response.accounts.some((account) => !isRecord(account))) {
    // Never turn a malformed account payload into an apparently healthy empty
    // pool: callers must be able to distinguish "no accounts" from a broken
    // CPA response and keep the last known-good UI state.
    throw new APIError(502, "ui.invalid_accounts_response");
  }
  const source = response;
  if (!hasRequiredNonNegativeIntegers(source, ["total", "page", "page_size", "pages"])) {
    throw new APIError(502, "ui.invalid_accounts_response");
  }
  const pageSize = Math.max(1, finiteNonNegativeInteger(source.page_size, Math.max(1, requestedPageSize), 1000));
  const page = Math.max(1, finiteNonNegativeInteger(source.page, Math.max(1, requestedPage), 10_000));
  const total = finiteNonNegativeInteger(source.total, 0, 10_000_000);
  const pages = Math.max(1, finiteNonNegativeInteger(source.pages, total > 0 ? Math.ceil(total / pageSize) : 1, 10_000));
  let availability = legacyAccountConcurrency;
  if (source.account_concurrency !== undefined) {
    if (!isValidAccountConcurrencyAvailability(source.account_concurrency)) {
      throw new APIError(502, "ui.invalid_accounts_response");
    }
    availability = source.account_concurrency;
  }
  const accounts = (source.accounts as Account[]).map((account) => {
    if (account.concurrency !== undefined && !isValidAccountConcurrencySummary(account.concurrency)) {
      throw new APIError(502, "ui.invalid_accounts_response");
    }
    return {
      ...account,
      concurrency: account.concurrency ?? { supported: availability.supported, active: 0, limit: 0 },
    };
  });
  return {
    ...(source as Partial<AccountListResponse>),
    accounts,
    total,
    page,
    page_size: pageSize,
    pages,
    account_concurrency: availability,
  };
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

export async function verifySession(signal?: AbortSignal): Promise<void> {
  const query = new URLSearchParams({ page: "1", page_size: "1" });
  await requestRecord<AccountListResponse>("/accounts", { signal }, query);
}

export async function listAccounts(
  page: number,
  pageSize: number,
  filters: AccountFilters,
  sort: AccountSort = { field: "account", order: "asc" },
  signal?: AbortSignal,
): Promise<AccountListResponse> {
  const query = filtersQuery(filters);
  query.set("page", String(page));
  query.set("page_size", String(pageSize));
  query.set("sort_by", sort.field);
  query.set("sort_order", sort.order);
  const response = await requestRecord<AccountListResponse>("/accounts", { signal }, query);
	return normalizeAccountListResponse(response, page, pageSize);
}

export async function loadAccountConfig(accountID: string): Promise<AccountEditableConfig> {
	const response = await requestRecord<unknown>("/accounts/config", {
		method: "POST",
		body: JSON.stringify({ account_id: accountID }),
	});
	const source = response as Record<string, unknown>;
	const validPriority = source.priority === null || (typeof source.priority === "number" && Number.isFinite(source.priority));
	const validWebsockets = source.websockets === null || typeof source.websockets === "boolean";
	const validHeaders = source.header_names === null || (Array.isArray(source.header_names) && source.header_names.every((item) => typeof item === "string"));
	const validModelPolicy = source.model_policy === null || isRecord(source.model_policy);
	if (
		typeof source.account_id !== "string" || !source.account_id.trim()
		|| typeof source.disabled !== "boolean"
		|| !validPriority
		|| typeof source.note !== "string"
		|| typeof source.prefix !== "string"
		|| typeof source.proxy !== "string"
		|| typeof source.proxy_configured !== "boolean"
		|| !validWebsockets
		|| !validHeaders
		|| !validModelPolicy
	) {
		throw new APIError(502, "ui.invalid_api_response");
	}
	let availability = legacyAccountConcurrency;
	if (source.account_concurrency !== undefined) {
		if (!isValidAccountConcurrencyAvailability(source.account_concurrency)) {
			throw new APIError(502, "ui.invalid_api_response");
		}
		availability = source.account_concurrency;
	}
	let concurrency: AccountEditableConfig["concurrency"];
	if (source.concurrency !== undefined) {
		if (!isValidAccountConcurrencySummary(source.concurrency)) {
			throw new APIError(502, "ui.invalid_api_response");
		}
		concurrency = source.concurrency;
	} else {
		concurrency = { supported: availability.supported, active: 0, limit: 0 };
	}
	return {
		...(source as Partial<AccountEditableConfig>),
		account_id: typeof source.account_id === "string" ? source.account_id : accountID,
		header_names: Array.isArray(source.header_names) ? source.header_names.filter((item): item is string => typeof item === "string") : [],
		account_concurrency: availability,
		concurrency,
	} as AccountEditableConfig;
}

export async function refreshAccountQuotaMetadata(accountID: string): Promise<QuotaMetadataResponse> {
	return requestRecord<QuotaMetadataResponse>("/accounts/quota-metadata/refresh", {
		method: "POST",
		body: JSON.stringify({ account_id: accountID }),
	});
}

export async function useAccountActiveReset(accountID: string): Promise<QuotaMetadataResponse> {
	return requestRecord<QuotaMetadataResponse>("/accounts/quota-metadata/reset", {
		method: "POST",
		body: JSON.stringify({ account_id: accountID, confirm: true }),
	});
}

export async function testAccountModel(accountID: string, model: string, experimentalWeeklyOverdraft = false): Promise<ModelTestResult> {
  return requestRecord<ModelTestResult>("/accounts/model-test", {
    method: "POST",
    body: JSON.stringify({
      account_id: accountID,
      model: model.trim(),
      ...(experimentalWeeklyOverdraft ? { experimental_weekly_overdraft: true } : {}),
    }),
  });
}

export async function refreshAccountToken(accountID: string): Promise<AccountTokenRefreshResult> {
	return requestRecord<AccountTokenRefreshResult>("/accounts/token/refresh", {
		method: "POST",
		body: JSON.stringify({ account_id: accountID }),
	});
}

export async function loadAccountModels(scope: TargetScope): Promise<AccountModelCatalogResponse> {
	const response = await requestRecord<unknown>("/accounts/models", {
		method: "POST",
		body: JSON.stringify({ scope }),
	});
	const source = response as Record<string, unknown>;
	const models = nullableRecordArray(source.models);
	if (models !== undefined && models.some((model) => !isNonEmptyString(model.id))) {
		throw new APIError(502, "ui.invalid_api_response");
	}
	if (
		models === undefined
		|| !isFiniteNonNegativeInteger(source.total)
		|| !isFiniteNonNegativeInteger(source.eligible)
		|| !isFiniteNonNegativeInteger(source.loaded)
		|| !isFiniteNonNegativeInteger(source.failed)
		|| !isFiniteNonNegativeInteger(source.read_only)
		|| !isFiniteNonNegativeInteger(source.missing)
		|| (source.warnings !== undefined && (!Array.isArray(source.warnings) || source.warnings.some((item) => typeof item !== "string")))
	) {
		throw new APIError(502, "ui.invalid_api_response");
	}
	return {
		...(source as Partial<AccountModelCatalogResponse>),
		models: models as unknown as AccountModelCatalogResponse["models"],
		warnings: Array.isArray(source.warnings) ? source.warnings.filter((item): item is string => typeof item === "string") : [],
		total: finiteNonNegativeInteger(source.total, 0, 10_000_000),
		eligible: finiteNonNegativeInteger(source.eligible, 0, 10_000_000),
		loaded: finiteNonNegativeInteger(source.loaded, 0, 10_000_000),
		failed: finiteNonNegativeInteger(source.failed, 0, 10_000_000),
		read_only: finiteNonNegativeInteger(source.read_only, 0, 10_000_000),
		missing: finiteNonNegativeInteger(source.missing, 0, 10_000_000),
	} as AccountModelCatalogResponse;
}

export async function createAccountDeletePreview(accountID: string): Promise<AccountDeletePreview> {
  return requestRecord<AccountDeletePreview>("/accounts/delete/preview", {
    method: "POST",
    body: JSON.stringify({ id: accountID }),
  });
}

export async function deleteAccount(previewID: string): Promise<AccountDeleteResult> {
  return requestRecord<AccountDeleteResult>("/accounts/delete/start", {
    method: "POST",
    body: JSON.stringify({ preview_id: previewID }),
  });
}

export async function createPreview(scope: TargetScope, patch: BatchPatch): Promise<BatchPreview> {
  return requestRecord<BatchPreview>("/batch/preview", {
    method: "POST",
    body: JSON.stringify({ scope, patch }),
  });
}

export async function createBatchDeletePreview(scope: TargetScope): Promise<BatchPreview> {
  return requestRecord<BatchPreview>("/batch/delete/preview", {
    method: "POST",
    body: JSON.stringify({ scope }),
  });
}

export async function scanAccountDuplicates(options: AccountDeduplicationOptions): Promise<AccountDeduplicationPreview> {
  return requestRecord<AccountDeduplicationPreview>("/accounts/deduplicate/preview", {
    method: "POST",
    body: JSON.stringify(options),
  });
}

export async function startBatch(previewID: string): Promise<JobSnapshot> {
  return requestRecord<JobSnapshot>("/batch/start", {
    method: "POST",
    body: JSON.stringify({ preview_id: previewID }),
  });
}

export async function startBatchDelete(previewID: string): Promise<JobSnapshot> {
  return requestRecord<JobSnapshot>("/batch/delete/start", {
    method: "POST",
    body: JSON.stringify({ preview_id: previewID, confirm: true }),
  });
}

export async function getJobStatus(includeResults = true, signal?: AbortSignal): Promise<JobSnapshot> {
  const query = new URLSearchParams();
  if (!includeResults) query.set("light", "1");
  return requestRecord<JobSnapshot>("/batch/status", { signal }, query);
}

export async function retryBatch(): Promise<JobSnapshot> {
  return requestRecord<JobSnapshot>("/batch/retry", { method: "POST" });
}

export async function getDefaultPolicy(signal?: AbortSignal): Promise<PolicySnapshot> {
	return requestRecord<PolicySnapshot>("/defaults", { signal });
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
    // Older CPA hosts do not expose the generic plugin-config PATCH route.
    // Each feature endpoint persists its own state, so an unsupported mirror
    // must not prevent the actual save from running.
    if (error instanceof APIError && [404, 405, 501].includes(error.status)) return;
    if (error instanceof APIError) throw new APIError(error.status, "ui.settings_persistence_failed");
    throw error;
  }
}

export async function saveDefaultPolicy(policy: DefaultPolicy): Promise<PolicySnapshot> {
	await persistPluginSettings({ default_policy: policy });
	return requestRecord<PolicySnapshot>("/defaults", {
		method: "PUT",
		body: JSON.stringify(policy),
	});
}

export async function scanDefaultPolicy(): Promise<PolicySnapshot> {
	return requestRecord<PolicySnapshot>("/defaults/scan", { method: "POST" });
}

const INSPECTION_CONFIDENCE_VALUES = new Set(["high", "medium", "low"]);
const INSPECTION_ACTION_VALUES = new Set(["disable", "enable", "delete", "delete_candidate", "review_resolve", "review_ignore", "review_reopen"]);
const INSPECTION_ACTION_STATUS_VALUES = new Set(["pending", "succeeded", "failed", "skipped"]);

function optionalString(item: Record<string, unknown>, key: string, nonEmpty = false): boolean {
  if (!(key in item) || item[key] === undefined || item[key] === null) return true;
  return nonEmpty ? isNonEmptyString(item[key]) : typeof item[key] === "string";
}

function optionalBoolean(item: Record<string, unknown>, key: string): boolean {
  return !(key in item) || item[key] === undefined || item[key] === null || typeof item[key] === "boolean";
}

function optionalNonNegativeInteger(item: Record<string, unknown>, key: string): boolean {
  return !(key in item) || item[key] === undefined || item[key] === null || isFiniteNonNegativeInteger(item[key]);
}

function isValidInspectionResultRecord(item: Record<string, unknown>): boolean {
  // Older CPA builds omit fields introduced by newer inspection/remediation versions.
  // Validate every field that is present, but do not turn a compatible legacy row into an empty result set.
  return isNonEmptyString(item.id)
    && (!("health" in item) || isNonEmptyString(item.health))
    && (!("reason_code" in item) || isNonEmptyString(item.reason_code))
    && (!("confidence" in item) || isNonEmptyString(item.confidence))
    && (!("recommendation" in item) || isNonEmptyString(item.recommendation))
    && optionalBoolean(item, "disabled")
    && optionalBoolean(item, "editable")
    && optionalBoolean(item, "auto_disable_eligible")
    && optionalBoolean(item, "owned_disable")
    && optionalNonNegativeInteger(item, "failure_streak")
    && optionalNonNegativeInteger(item, "healthy_streak")
    && optionalString(item, "last_checked_at")
    && optionalBoolean(item, "manual_delete_eligible")
    && optionalString(item, "name")
    && optionalString(item, "provider")
    && optionalString(item, "type")
    && optionalString(item, "plan_type")
    && optionalString(item, "first_unhealthy_at")
    && optionalString(item, "last_failure_at")
    && optionalString(item, "last_success_at")
    && optionalString(item, "recover_after")
    && optionalString(item, "delete_eligible_at")
    && optionalString(item, "auto_action")
    && optionalString(item, "probe_status")
    && optionalString(item, "probe_kind")
    && optionalString(item, "probe_reason_code")
    && optionalString(item, "probe_model")
    && optionalString(item, "probe_tested_at")
    && optionalNonNegativeInteger(item, "probe_latency_ms")
    && optionalString(item, "auto_action_status")
    && optionalString(item, "signal_source")
    && optionalNonNegativeInteger(item, "status_code")
    && optionalString(item, "review_status")
    && optionalString(item, "reviewed_at")
    && optionalBoolean(item, "circuit_open")
    && optionalString(item, "circuit_reason_code")
    && optionalString(item, "quota_window")
    && optionalNonNegativeInteger(item, "usage_total_tokens")
    && optionalString(item, "usage_last_request_at")
    && optionalString(item, "run_id")
    && optionalString(item, "run_phase")
    && optionalString(item, "run_observed_at")
    && optionalString(item, "auto_disable_probe_name")
    && optionalString(item, "auto_disable_probe_status")
    && optionalNonNegativeInteger(item, "auto_disable_probe_attempts")
    && optionalNonNegativeInteger(item, "auto_disable_probe_limit")
    && optionalString(item, "auto_disable_probe_reason_code")
    && optionalString(item, "auto_disable_probe_model")
    && optionalString(item, "auto_disable_probe_tested_at");
}

function isValidInspectionActionRecord(item: Record<string, unknown>): boolean {
  return isNonEmptyString(item.id)
    && isNonEmptyString(item.account_id)
    && (!("action" in item) || (typeof item.action === "string" && INSPECTION_ACTION_VALUES.has(item.action)))
    && (!("status" in item) || (typeof item.status === "string" && INSPECTION_ACTION_STATUS_VALUES.has(item.status)))
    && (!("reason_code" in item) || isNonEmptyString(item.reason_code))
    && optionalString(item, "created_at")
    && optionalString(item, "name")
    && optionalString(item, "provider")
    && optionalString(item, "source");
}

export async function getInspection(signal?: AbortSignal): Promise<InspectionSnapshot> {
  return requestRecord<InspectionSnapshot>("/inspection", { signal });
}

export async function getLiveInspection(signal?: AbortSignal): Promise<InspectionSnapshot> {
  const response = await requestRecord<unknown>("/inspection/live", { signal });
  const source = response as Record<string, unknown>;
  const liveResults = nullableRecordArray(source.live_results);
  if (!isRecord(source.policy) || !isRecord(source.last_run) || liveResults === undefined
    || liveResults.some((item) => !isValidInspectionResultRecord(item))) {
    throw new APIError(502, "ui.invalid_api_response");
  }
  return {
    ...(source as Partial<InspectionSnapshot>),
    live_results: liveResults as unknown as InspectionSnapshot["live_results"],
  } as unknown as InspectionSnapshot;
}

export async function saveInspectionPolicy(policy: InspectionPolicy, confirmAutoDelete = false, confirmDeleteInvalidCredentials = false): Promise<InspectionSnapshot> {
  await persistPluginSettings({ inspection_policy: policy });
  return requestRecord<InspectionSnapshot>("/inspection", {
    method: "PUT",
    body: JSON.stringify({ ...policy, confirm_auto_delete: confirmAutoDelete, confirm_delete_invalid_credentials: confirmDeleteInvalidCredentials }),
  });
}

export async function scanFullInspection(): Promise<InspectionSnapshot> {
  return requestRecord<InspectionSnapshot>("/inspection/scan", { method: "POST" });
}

export async function scanNativeInspection(): Promise<InspectionSnapshot> {
  return requestRecord<InspectionSnapshot>("/inspection/scan/native", { method: "POST" });
}

export async function previewInspectionNotification(notification: InspectionNotificationRequest): Promise<InspectionNotificationPreview> {
  return requestRecord<InspectionNotificationPreview>("/inspection/notification/preview", {
    method: "POST",
    body: JSON.stringify(notification),
  });
}

export async function testInspectionNotification(notification: InspectionNotificationRequest): Promise<InspectionNotificationTestResult> {
  return requestRecord<InspectionNotificationTestResult>("/inspection/notification/test", {
    method: "POST",
    body: JSON.stringify(notification),
  });
}

export async function startInspectionRun(run: InspectionRunRequest): Promise<InspectionSnapshot> {
  return requestRecord<InspectionSnapshot>("/inspection/run", {
    method: "POST",
    body: JSON.stringify(run),
  });
}

export async function stopInspectionRun(): Promise<InspectionSnapshot> {
  return requestRecord<InspectionSnapshot>("/inspection/stop", { method: "POST" });
}

export async function updateInspectionReview(accountID: string, action: "resolve" | "ignore" | "reopen"): Promise<InspectionResult> {
  return requestRecord<InspectionResult>("/inspection/review", {
    method: "POST",
    body: JSON.stringify({ account_id: accountID, action }),
  });
}

export async function listInspectionResults(page: number, pageSize: number, health = "", search = "", signal?: AbortSignal): Promise<InspectionResultList> {
  const query = new URLSearchParams({ page: String(page), page_size: String(pageSize) });
  if (health) query.set("health", health);
  if (search) query.set("search", search);
  const response = await requestRecord<unknown>("/inspection/results", { signal }, query);
  const source = response as Record<string, unknown>;
  const results = nullableRecordArray(source.results);
  if (results === undefined || results.some((item) => !isValidInspectionResultRecord(item)) || !isRecord(source.summary)) {
    throw new APIError(502, "ui.invalid_api_response");
  }
  const summary = source.summary as Partial<InspectionRemediationSummary>;
  const summaryKeys: (keyof InspectionRemediationSummary)[] = [
    "actionable", "suggested_delete", "suggested_disable", "suggested_enable",
    "reauth", "deletable_reauth", "review", "keep", "handled",
    "editable_enabled", "editable_disabled",
  ];
  if (!summaryKeys.every((key) => isFiniteNonNegativeInteger(summary[key]))) {
    throw new APIError(502, "ui.invalid_api_response");
  }
  if (!hasRequiredNonNegativeIntegers(source, ["total", "page", "page_size", "pages"])) {
    throw new APIError(502, "ui.invalid_api_response");
  }
  return {
    ...(source as Partial<InspectionResultList>),
    results: results as unknown as InspectionResultList["results"],
    summary: {
      actionable: finiteNonNegativeInteger(summary.actionable, 0, 10_000_000),
      suggested_delete: finiteNonNegativeInteger(summary.suggested_delete, 0, 10_000_000),
      suggested_disable: finiteNonNegativeInteger(summary.suggested_disable, 0, 10_000_000),
      suggested_enable: finiteNonNegativeInteger(summary.suggested_enable, 0, 10_000_000),
      reauth: finiteNonNegativeInteger(summary.reauth, 0, 10_000_000),
      deletable_reauth: finiteNonNegativeInteger(summary.deletable_reauth, 0, 10_000_000),
      review: finiteNonNegativeInteger(summary.review, 0, 10_000_000),
      keep: finiteNonNegativeInteger(summary.keep, 0, 10_000_000),
      handled: finiteNonNegativeInteger(summary.handled, 0, 10_000_000),
      editable_enabled: finiteNonNegativeInteger(summary.editable_enabled, 0, 10_000_000),
      editable_disabled: finiteNonNegativeInteger(summary.editable_disabled, 0, 10_000_000),
    },
    total: finiteNonNegativeInteger(source.total, 0, 10_000_000),
    page: Math.max(1, finiteNonNegativeInteger(source.page, page, 10_000_000)),
    page_size: Math.max(1, finiteNonNegativeInteger(source.page_size, pageSize, 1000)),
    pages: Math.max(1, finiteNonNegativeInteger(source.pages, 1, 10_000_000)),
  } as InspectionResultList;
}

export async function listInspectionActions(limit = 50, signal?: AbortSignal): Promise<InspectionAction[]> {
  const query = new URLSearchParams({ limit: String(limit) });
  const response = await requestRecord<unknown>("/inspection/actions", { signal }, query);
  const source = response as Record<string, unknown>;
  const actions = nullableRecordArray(source.actions);
  if (actions === undefined || actions.some((item) => !isValidInspectionActionRecord(item))) throw new APIError(502, "ui.invalid_api_response");
  return actions as unknown as InspectionAction[];
}

export async function deleteInspectionRecommendations(accountIDs: string[]): Promise<InspectionDeleteRun> {
  return requestRecord<InspectionDeleteRun>("/inspection/delete", {
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
  const response = await fetchWithTimeout(buildURL("/inspection/export", query), {
    headers: { Authorization: `Bearer ${session.managementKey}` },
  });
  if (!response.ok) {
    const message = await responseErrorMessage(response, `Export failed (${response.status})`);
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
  return requestRecord<InspectionDeleteRun>("/inspection/auto-delete", { method: "POST" });
}

export async function getUpdateStatus(signal?: AbortSignal): Promise<UpdateSnapshot> {
  return requestRecord<UpdateSnapshot>("/updates", { signal });
}

export async function getExperimentalSettings(signal?: AbortSignal): Promise<ExperimentalSettingsSnapshot> {
	return requestRecord<ExperimentalSettingsSnapshot>("/experiments", { signal });
}

export async function saveExperimentalSettings(settings: ExperimentalSettings): Promise<ExperimentalSettingsSnapshot> {
	await persistPluginSettings({ experimental_settings: settings });
	return requestRecord<ExperimentalSettingsSnapshot>("/experiments", {
		method: "PUT",
		body: JSON.stringify(settings),
	});
}

export async function completeAgentIdentitySessionLogin(state: string, sessionJSON: string): Promise<AgentIdentitySessionLoginResponse> {
	return requestRecord<AgentIdentitySessionLoginResponse>("/experiments/agent-identity/session-login", {
		method: "POST",
		body: JSON.stringify({ state, session_json: sessionJSON }),
	});
}

function normalizeOpenCodeAccountsResponse(response: unknown): OpenCodeAccountsResponse {
	if (!isRecord(response)) throw new APIError(502, "ui.invalid_api_response");
	const accounts = nullableRecordArray(response.accounts);
	if (accounts === undefined || accounts.some((account) => !isNonEmptyString(account.id) || !isNonEmptyString(account.workspace_id))) {
		throw new APIError(502, "ui.invalid_api_response");
	}
	if (response.storage_error !== undefined && typeof response.storage_error !== "string") {
		throw new APIError(502, "ui.invalid_api_response");
	}
	return {
		accounts: accounts.map((account) => ({ id: (account.id as string).trim(), workspace_id: (account.workspace_id as string).trim() })),
		...(typeof response.storage_error === "string" ? { storage_error: response.storage_error } : {}),
	};
}

export async function listOpenCodeAccounts(signal?: AbortSignal): Promise<OpenCodeAccountsResponse> {
	return normalizeOpenCodeAccountsResponse(await requestRecord<unknown>("/opencode/accounts", { signal }));
}

export async function saveOpenCodeAccount(workspaceID: string, authCookie: string): Promise<OpenCodeAccountSaveResponse> {
	return requestRecord<OpenCodeAccountSaveResponse>("/opencode/accounts", {
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
	return requestRecord<{ results: Record<string, import("../types").OpenCodeQuotaResult> }>("/opencode/refresh", {
		method: "POST",
	});
}

export async function probeOpenCodeQuota(workspaceID: string, authCookie: string, timeoutSeconds = 30): Promise<OpenCodeProbeResponse> {
	return requestRecord<OpenCodeProbeResponse>("/opencode/probe", {
		method: "POST",
		body: JSON.stringify({ workspace_id: workspaceID, auth_cookie: authCookie, timeout_seconds: timeoutSeconds }),
	});
}

function normalizeOpenCodeZenAccountsResponse(response: unknown): OpenCodeZenAccountsResponse {
	if (!isRecord(response)) throw new APIError(502, "ui.invalid_api_response");
	const accounts = nullableRecordArray(response.accounts);
	if (accounts === undefined || accounts.some((account) =>
		!isNonEmptyString(account.id)
		|| !isNonEmptyString(account.base_url)
		|| typeof account.key_set !== "boolean"
		|| (account.name !== undefined && typeof account.name !== "string")
	)) {
		throw new APIError(502, "ui.invalid_api_response");
	}
	if (response.storage_error !== undefined && typeof response.storage_error !== "string") {
		throw new APIError(502, "ui.invalid_api_response");
	}
	return {
		accounts: accounts.map((account) => ({
			id: (account.id as string).trim(),
			base_url: (account.base_url as string).trim(),
			key_set: account.key_set as boolean,
			...(typeof account.name === "string" ? { name: account.name } : {}),
		})),
		...(typeof response.storage_error === "string" ? { storage_error: response.storage_error } : {}),
	};
}

export async function listOpenCodeZenAccounts(signal?: AbortSignal): Promise<OpenCodeZenAccountsResponse> {
	return normalizeOpenCodeZenAccountsResponse(await requestRecord<unknown>("/opencode/zen/accounts", { signal }));
}

export async function saveOpenCodeZenAccount(options: {
	account_id?: string;
	name?: string;
	base_url: string;
	zen_api_key?: string;
	timeout_seconds?: number;
}): Promise<OpenCodeZenAccountSaveResponse> {
	return requestRecord<OpenCodeZenAccountSaveResponse>("/opencode/zen/accounts", {
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
	return requestRecord<OpenCodeZenProbeResponse>("/opencode/zen/probe", {
		method: "POST",
		body: JSON.stringify(options),
	});
}

export async function probeOpenCodeZenAccount(accountID: string): Promise<OpenCodeZenProbeAccountResponse> {
	return requestRecord<OpenCodeZenProbeAccountResponse>("/opencode/zen/probe-account?account_id=" + encodeURIComponent(accountID), {
		method: "POST",
	});
}

export async function saveUpdatePolicy(policy: UpdatePolicy, confirmAutoUpdate = false): Promise<UpdateSnapshot> {
	await persistPluginSettings({ update_policy: policy });
  const status = await requestRecord<UpdateSnapshot>("/updates", {
    method: "PUT",
    body: JSON.stringify({ policy, confirm_auto_update: confirmAutoUpdate }),
  });
  const store = await loadPluginStore();
  return reconcileUpdateStatus(status, store.response, store.error);
}

export async function checkForUpdates(signal?: AbortSignal): Promise<UpdateSnapshot> {
	return requestRecord<UpdateSnapshot>("/updates/check", { method: "POST", signal });
}

export async function getPluginStore(signal?: AbortSignal): Promise<PluginStoreResponse> {
  const response = await managementRequest<unknown>("/plugin-store", { signal });
  if (!isRecord(response)) throw new APIError(502, "ui.invalid_json_response");
  const source = response;
  if (typeof source.plugins_enabled !== "boolean") {
    throw new APIError(502, "ui.invalid_api_response");
  }
  if (!Object.prototype.hasOwnProperty.call(source, "plugins")) {
    throw new APIError(502, "ui.invalid_api_response");
  }
  const pluginValues = Array.isArray(source.plugins) ? source.plugins : null;
  if (source.plugins !== null && pluginValues === null) {
    throw new APIError(502, "ui.invalid_api_response");
  }
  // Never silently drop malformed rows: the update checker could otherwise
  // conclude that this plugin is absent and report a false "no update" state.
  if (pluginValues !== null && pluginValues.some((plugin) => {
    if (!isRecord(plugin)) return true;
    return typeof plugin.id !== "string" || plugin.id.trim() === ""
      || typeof plugin.version !== "string" || plugin.version.trim() === "";
  })) {
    throw new APIError(502, "ui.invalid_api_response");
  }
  return {
    plugins_enabled: source.plugins_enabled,
    plugins: (pluginValues ?? []) as unknown as NonNullable<PluginStoreResponse["plugins"]>,
  };
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

  if (!storeVersion) {
    return {
      ...base,
      error: retainedError || "plugin store metadata is unavailable",
    };
  }
  if (!currentVersion) {
    return {
      ...base,
      latest_version: storeVersion.value,
      release_url: `${pluginReleaseBaseURL}${storeVersion.value}`,
      release_source: "plugin_store",
      // Do not claim an update when the installed version is unknown.
      update_available: false,
      error: retainedError || "current plugin version is unavailable",
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

async function loadPluginStore(signal?: AbortSignal): Promise<{ response: PluginStoreResponse | null; error: string }> {
  return getPluginStore(signal).then(
    (response) => ({ response, error: "" }),
    (error) => {
      if (error instanceof APIError && error.status === 401) throw error;
      return { response: null, error: "plugin store metadata is unavailable" };
    },
  );
}

export async function getEffectiveUpdateStatus(checkNow = false, signal?: AbortSignal): Promise<UpdateSnapshot> {
  const key = checkNow ? "check" : "read";
  const pending = effectiveUpdateStatusInFlight[key];
  if (pending) return withAbortSignal(pending, signal);
  const operation = getEffectiveUpdateStatusUnshared(checkNow);
  effectiveUpdateStatusInFlight[key] = operation;
  void operation.then(() => {
    if (effectiveUpdateStatusInFlight[key] === operation) delete effectiveUpdateStatusInFlight[key];
  }, () => {
    if (effectiveUpdateStatusInFlight[key] === operation) delete effectiveUpdateStatusInFlight[key];
  });
  // Keep the underlying request independent from any one component's
  // lifecycle. A page can unmount while another mounted page still needs the
  // same status; aborting the shared fetch in that case caused duplicate
  // checks and transient "update metadata unavailable" states.
  return withAbortSignal(operation, signal);
}

const effectiveUpdateStatusInFlight: Partial<Record<"read" | "check", Promise<UpdateSnapshot>>> = {};

function withAbortSignal<T>(promise: Promise<T>, signal?: AbortSignal): Promise<T> {
  if (!signal) return promise;
  if (signal.aborted) return Promise.reject(new DOMException("Aborted", "AbortError"));
  return new Promise<T>((resolve, reject) => {
    const onAbort = () => reject(new DOMException("Aborted", "AbortError"));
    signal.addEventListener("abort", onAbort, { once: true });
    promise.then(
      (value) => {
        signal.removeEventListener("abort", onAbort);
        resolve(value);
      },
      (error) => {
        signal.removeEventListener("abort", onAbort);
        reject(error);
      },
    );
  });
}

async function getEffectiveUpdateStatusUnshared(checkNow: boolean, signal?: AbortSignal): Promise<UpdateSnapshot> {
  let status: UpdateSnapshot;
  try {
    status = await (checkNow ? checkForUpdates(signal) : getUpdateStatus(signal));
  } catch (error) {
    // A direct check is best-effort. Keep the last persisted status so a
    // transient CPA/plugin-store failure does not hide the authenticated
    // plugin-store version that can still be displayed to the user.
    if (error instanceof APIError && error.status === 401) throw error;
    try {
      status = await getUpdateStatus(signal);
    } catch (fallbackError) {
      if (fallbackError instanceof APIError && fallbackError.status === 401) throw fallbackError;
      status = {
        policy: { check_enabled: false, check_interval_hours: 24, auto_update: false },
        current_version: "",
        update_available: false,
        checking: false,
        pending: false,
        error: error instanceof Error ? error.message : "update status unavailable",
      };
    }
  }
  const store = await loadPluginStore(signal);
  return reconcileUpdateStatus(status, store.response, store.error);
}

const pluginInstallInFlight = new Map<string, Promise<PluginInstallResult>>();

export function installPluginUpdate(version: string): Promise<PluginInstallResult> {
  const normalized = normalizedStableVersion(version);
  const key = normalized?.value ?? `invalid:${version.trim()}`;
  const pending = pluginInstallInFlight.get(key);
  if (pending) return pending;
  const install = installPluginUpdateOnce(version).finally(() => {
    if (pluginInstallInFlight.get(key) === install) pluginInstallInFlight.delete(key);
  });
  pluginInstallInFlight.set(key, install);
  return install;
}

async function installPluginUpdateOnce(version: string): Promise<PluginInstallResult> {
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

export async function listOperations(requestedPage: number, filters: OperationFilters = {}, signal?: AbortSignal): Promise<OperationListResponse> {
  const query = new URLSearchParams({ page: String(requestedPage), page_size: "500" });
  if (filters.category) query.set("category", filters.category);
  if (filters.status) query.set("status", filters.status);
  if (filters.source) query.set("source", filters.source);
  if (filters.search) query.set("search", filters.search);
  const controller = new AbortController();
  const abortFromCaller = () => controller.abort();
  if (signal?.aborted) controller.abort();
  else signal?.addEventListener("abort", abortFromCaller, { once: true });
  const timeout = globalThis.setTimeout(() => controller.abort(), 15_000);
  let response: unknown;
  try {
    response = await requestRecord<unknown>("/operations", { signal: controller.signal }, query);
  } finally {
    globalThis.clearTimeout(timeout);
    signal?.removeEventListener("abort", abortFromCaller);
  }
  const source = response as Record<string, unknown>;
  const operations = nullableRecordArray(source.operations);
  if (operations === undefined || !isRecord(source.summary)) {
    throw new APIError(502, "ui.invalid_api_response");
  }
  const summary = source.summary as Partial<OperationListResponse["summary"]>;
  const summaryKeys: (keyof OperationListResponse["summary"])[] = [
    "total", "running", "succeeded", "failed", "attention", "interrupted",
  ];
  if (!summaryKeys.every((key) => isFiniteNonNegativeInteger(summary[key]))
    || !hasRequiredNonNegativeIntegers(source, ["total", "page", "page_size", "pages"])) {
    throw new APIError(502, "ui.invalid_api_response");
  }
  if (source.extended_history !== undefined && typeof source.extended_history !== "boolean") {
    throw new APIError(502, "ui.invalid_api_response");
  }
  for (const key of ["archived_segments", "retention_limit", "retained"] as const) {
    if (source[key] !== undefined && !isFiniteNonNegativeInteger(source[key])) {
      throw new APIError(502, "ui.invalid_api_response");
    }
  }
  const total = finiteNonNegativeInteger(source.total, 0, 10_000_000);
  const page = Math.max(1, finiteNonNegativeInteger(source.page, requestedPage, 10_000_000));
  const pages = Math.max(1, finiteNonNegativeInteger(source.pages, total > 0 ? Math.ceil(total / 500) : 1, 10_000_000));
  return {
    ...(source as Partial<OperationListResponse>),
    operations: operations as unknown as OperationEntry[],
    summary: source.summary as unknown as OperationListResponse["summary"],
    total,
    page,
    pages,
    page_size: 500,
    extended_history: source.extended_history === true,
    archived_segments: finiteNonNegativeInteger(source.archived_segments, 0, 10_000_000),
    retention_limit: finiteNonNegativeInteger(source.retention_limit, 500, 10_000_000),
    retained: finiteNonNegativeInteger(source.retained, total, 10_000_000),
  };
}

export async function saveOperationRetentionSettings(extendedHistory: boolean): Promise<OperationRetentionSettings> {
	await persistPluginSettings({ operation_settings: { extended_history: extendedHistory } });
  return requestRecord<OperationRetentionSettings>("/operations/settings", {
    method: "PUT",
    body: JSON.stringify({ extended_history: extendedHistory }),
  });
}

export async function getOperationRetentionSettings(): Promise<OperationRetentionSettings> {
	const response = await requestRecord<unknown>("/operations/settings");
	if (!isRecord(response) || typeof response.extended_history !== "boolean") {
		throw new APIError(502, "ui.invalid_api_response");
	}
	const hasDetails = ["page_size", "retained", "archived_segments"].some((key) => Object.prototype.hasOwnProperty.call(response, key));
	if (!hasDetails) {
		// Older CPA hosts only returned the toggle. Keep that wire format
		// compatible while avoiding partial/malformed detail objects.
		return { extended_history: response.extended_history, page_size: 500, retained: 0, archived_segments: 0 };
	}
	if (!isFiniteNonNegativeInteger(response.page_size)
		|| !isFiniteNonNegativeInteger(response.retained)
		|| !isFiniteNonNegativeInteger(response.archived_segments)) {
		throw new APIError(502, "ui.invalid_api_response");
	}
	return response as unknown as OperationRetentionSettings;
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

const SETTINGS_MIGRATION_PREFIX = "cpa-account-config-manager:settings-migrated:";

function settingsMigrationKey(): string {
	const session = getSession();
	const base = session?.baseUrl || (typeof window !== "undefined" ? window.location.origin : "default");
	return `${SETTINGS_MIGRATION_PREFIX}${encodeURIComponent(base)}`;
}

/**
 * Backward-compatible one-time settings migration helper. Current screens
 * persist their own settings directly; keep this export for older callers.
 */
export async function persistCurrentSettingsOnce(): Promise<ExperimentalSettings> {
	const key = settingsMigrationKey();
	if (typeof sessionStorage !== "undefined" && sessionStorage.getItem(key) === "done") {
		return (await getExperimentalSettings()).settings;
	}
	try {
		const settings = await persistCurrentSettings();
		if (typeof sessionStorage !== "undefined") sessionStorage.setItem(key, "done");
		return settings;
	} catch (error) {
		if (typeof sessionStorage !== "undefined") sessionStorage.removeItem(key);
		throw error;
	}
}

export async function clearOperations(): Promise<{ operation: OperationEntry; retained: number }> {
  const response = await requestRecord<unknown>("/operations", { method: "DELETE" });
  const operationStatuses = new Set(["running", "succeeded", "partial", "failed", "interrupted", "warning", "skipped"]);
  if (!isRecord(response) || !isRecord(response.operation) || !isNonEmptyString(response.operation.id)
    || typeof response.operation.status !== "string" || !operationStatuses.has(response.operation.status)
    || !isFiniteNonNegativeInteger(response.retained)) {
    throw new APIError(502, "ui.invalid_api_response");
  }
  return response as { operation: OperationEntry; retained: number };
}

export async function downloadOperationExport(format: OperationExportFormat, filters: OperationFilters = {}): Promise<{ filename: string; exported?: number }> {
  const session = getSession();
  if (!session) throw new APIError(401, "ui.management_key_is_not_set");
  const query = new URLSearchParams({ format });
  if (filters.category) query.set("category", filters.category);
  if (filters.status) query.set("status", filters.status);
  if (filters.source) query.set("source", filters.source);
  if (filters.search) query.set("search", filters.search);
  const response = await fetchWithTimeout(buildURL("/operations/export", query), {
    headers: { Authorization: `Bearer ${session.managementKey}` },
  });
  if (!response.ok) {
    const message = await responseErrorMessage(response, `Export failed (${response.status})`);
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
	return requestRecord<ForceSyncPreview>("/defaults/force/preview", { method: "POST" });
}

export async function startForceSync(previewID: string): Promise<ForceSyncJobSnapshot> {
	return requestRecord<ForceSyncJobSnapshot>("/defaults/force/start", {
		method: "POST",
		body: JSON.stringify({ preview_id: previewID }),
	});
}

export async function getForceSyncStatus(includeResults = true, signal?: AbortSignal): Promise<ForceSyncJobSnapshot> {
	const query = new URLSearchParams();
	if (!includeResults) query.set("light", "1");
	return requestRecord<ForceSyncJobSnapshot>("/defaults/force/status", { signal }, query);
}

export async function createImportPreview(files: File[]): Promise<ImportPreview> {
  const body = new FormData();
  files.forEach((file) => body.append("files", file, file.name));
  return requestRecord<ImportPreview>("/import/preview", {
    method: "POST",
    body,
  });
}

export async function startImport(previewID: string): Promise<ImportResult> {
  return requestRecord<ImportResult>("/import/start", {
    method: "POST",
    body: JSON.stringify({ preview_id: previewID }),
  });
}

export async function getImportStatus(signal?: AbortSignal): Promise<ImportResult> {
  const result = await requestRecord<unknown>("/import/status", { signal });
  const source = result as Record<string, unknown>;
  const results = nullableRecordArray(source.results);
  const validStates = new Set(["idle", "running", "completed", "partial", "failed"]);
  const validResultStates = new Set(["imported", "skipped", "failed"]);
  const malformedResult = results?.some((item) =>
    !isFiniteNonNegativeInteger(item.index)
    || !isNonEmptyString(item.source_name)
    || !isNonEmptyString(item.target_name)
    || !isNonEmptyString(item.label)
    || typeof item.status !== "string"
    || !validResultStates.has(item.status)
    || (item.source_path !== undefined && typeof item.source_path !== "string")
    || (item.email !== undefined && typeof item.email !== "string")
    || (item.project_id !== undefined && typeof item.project_id !== "string")
    || (item.account_id !== undefined && typeof item.account_id !== "string")
    || (item.error !== undefined && typeof item.error !== "string")
  ) ?? true;
  if (
    !isNonEmptyString(source.id)
    || typeof source.state !== "string" || !validStates.has(source.state)
    || typeof source.running !== "boolean"
    || typeof source.started_at !== "string"
    || typeof source.finished_at !== "string"
    || results === undefined
    || malformedResult
    || !isFiniteNonNegativeInteger(source.total)
    || !isFiniteNonNegativeInteger(source.imported)
    || !isFiniteNonNegativeInteger(source.skipped)
    || !isFiniteNonNegativeInteger(source.failed)
    || (source.error !== undefined && typeof source.error !== "string")
    || (source.usage_collection_started !== undefined && typeof source.usage_collection_started !== "boolean")
    || (source.usage_collection_targets !== undefined && !isFiniteNonNegativeInteger(source.usage_collection_targets))
  ) {
    throw new APIError(502, "ui.invalid_api_response");
  }
  return {
    ...(source as Partial<ImportResult>),
    results: results as unknown as ImportResult["results"],
  } as ImportResult;
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
  const response = await fetchWithTimeout(buildURL(`/export/${kind}`, query), {
    method: selected ? "POST" : "GET",
    headers,
    ...(selected ? { body: JSON.stringify({ scope }) } : {}),
  });
  if (!response.ok) {
    const message = await responseErrorMessage(response, `Export failed (${response.status})`);
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

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function channelModelFromJSON(source: Record<string, unknown>): AIProviderChannelModel {
  const model: AIProviderChannelModel = {
    name: typeof source["name"] === "string" ? source["name"] : "",
    raw: { ...source },
  };
  if (typeof source["alias"] === "string") model.alias = source["alias"];
  if (typeof source["display-name"] === "string") model.display_name = source["display-name"];
  if (typeof source["max-context-length"] === "number") model.max_context_length = source["max-context-length"];
  if (typeof source["force-mapping"] === "boolean") model.force_mapping = source["force-mapping"];
  if (typeof source["is-compat"] === "boolean") model.is_compat = source["is-compat"];
  if (typeof source["image"] === "boolean") model.image = source["image"];
  if (Array.isArray(source["input-modalities"])) model.input_modalities = source["input-modalities"].filter((item): item is string => typeof item === "string");
  if (Array.isArray(source["output-modalities"])) model.output_modalities = source["output-modalities"].filter((item): item is string => typeof item === "string");
  if (source["thinking"] !== undefined && source["thinking"] !== null) model.thinking = source["thinking"];
  return model;
}

function mapAPIKeyEntryFromJSON(source: Record<string, unknown>): AIProviderAPIKeyEntry {
  // Keep the complete host object so a credential row that was not edited can
  // be written back verbatim, including auth-index and future CPA fields.
  const entry: AIProviderAPIKeyEntry = { raw: { ...source } };
  if (typeof source["api-key"] === "string") entry.api_key = source["api-key"];
  if (typeof source["auth-index"] === "string") entry.auth_index = source["auth-index"];
  else if (typeof source["auth_index"] === "string") entry.auth_index = source["auth_index"];
  if (source["weight"] !== undefined && source["weight"] !== null) {
    const weight = Number(source["weight"]);
    if (Number.isFinite(weight)) entry.weight = weight;
  }
  if (typeof source["proxy-url"] === "string") entry.proxy_url = source["proxy-url"];
  return entry;
}

function channelEntriesFromResponse(kind: AIProviderChannelKind, payload: unknown): AIProviderChannelEntry[] {
  // Management endpoints are shared with multiple CPA versions. A malformed
  // entry must remain visible as a channel error; dropping it would make a
  // provider look empty and a later save could overwrite real credentials.
  const record = isRecord(payload) ? payload : {};
  const raw = record[kind];
  if (!Array.isArray(raw)) return [];
  return raw.map((item, index) => {
    if (typeof item === "string") {
      return { index, api_key: item };
    }
    if (!isRecord(item)) {
      throw new APIError(502, "ui.invalid_api_response");
    }
    const source = item as Record<string, unknown>;
    const entry: AIProviderChannelEntry = { index };
    if (typeof source["name"] === "string") entry.name = source["name"];
    if (typeof source["api-key"] === "string") entry.api_key = source["api-key"];
    if (source["api-key-entries"] !== undefined) {
      if (!Array.isArray(source["api-key-entries"]) || source["api-key-entries"].some((item) => !isRecord(item))) {
        throw new APIError(502, "ui.invalid_api_response");
      }
      entry.api_key_entries = source["api-key-entries"].map((item) => mapAPIKeyEntryFromJSON(item as Record<string, unknown>));
      const firstKey = entry.api_key_entries.find((keyEntry) => keyEntry.api_key);
      if (firstKey?.api_key) entry.api_key = firstKey.api_key;
    }
    if (typeof source["base-url"] === "string") entry.base_url = source["base-url"];
    if (typeof source["proxy-url"] === "string") entry.proxy_url = source["proxy-url"];
    if (typeof source["prefix"] === "string") entry.prefix = source["prefix"];
    if (typeof source["priority"] === "number") entry.priority = source["priority"];
    if (typeof source["disabled"] === "boolean") entry.disabled = source["disabled"];
    if (source["weight"] !== undefined && source["weight"] !== null) {
      const weight = Number(source["weight"]);
      if (Number.isFinite(weight)) entry.weight = weight;
    }
    if (source["headers"] !== undefined) {
      if (!isRecord(source["headers"]) || Object.values(source["headers"]).some((value) => typeof value !== "string")) {
        throw new APIError(502, "ui.invalid_api_response");
      }
      entry.headers = source["headers"] as Record<string, string>;
    }
    if (source["models"] !== undefined) {
      if (!Array.isArray(source["models"]) || source["models"].some((item) => !isRecord(item))) {
        throw new APIError(502, "ui.invalid_api_response");
      }
      entry.models = source["models"].map((item) => channelModelFromJSON(item as Record<string, unknown>));
    }
    if (source["excluded-models"] !== undefined) {
      if (!Array.isArray(source["excluded-models"]) || source["excluded-models"].some((item) => typeof item !== "string")) {
        throw new APIError(502, "ui.invalid_api_response");
      }
      entry.excluded_models = source["excluded-models"] as string[];
    }
    if (typeof source["support-prompt-cache-key"] === "boolean") entry.support_prompt_cache_key = source["support-prompt-cache-key"];
    if (typeof source["disable-cooling"] === "boolean") entry.disable_cooling = source["disable-cooling"];
    if (source["request-retry"] !== undefined) {
      if (source["request-retry"] === null) entry.request_retry = null;
      else if (typeof source["request-retry"] === "number" && Number.isInteger(source["request-retry"])) entry.request_retry = source["request-retry"];
      else throw new APIError(502, "ui.invalid_api_response");
    }
    if (source["request-scoped-errors"] !== undefined) {
      if (!Array.isArray(source["request-scoped-errors"]) || source["request-scoped-errors"].some((item) => !isRecord(item))) {
        throw new APIError(502, "ui.invalid_api_response");
      }
      // Keep the complete host rule objects so a later save preserves fields
      // this UI does not yet edit.
      entry.request_scoped_errors = source["request-scoped-errors"].map((item) => ({ ...(item as Record<string, unknown>) }));
    }
    if (typeof source["alpha-search"] === "boolean") entry.alpha_search = source["alpha-search"];
    if (typeof source["websockets"] === "boolean") entry.websockets = source["websockets"];
    if (typeof source["rebuild-mid-system-message"] === "boolean") entry.rebuild_mid_system_message = source["rebuild-mid-system-message"];
    if (typeof source["fingerprint-profile"] === "string") entry.fingerprint_profile = source["fingerprint-profile"];
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
function normalizeAIProviderRuntimeResponse(response: unknown): AIProviderRuntimeResponse {
  if (!isRecord(response) || !isNonEmptyString(response.updated_at) || !Array.isArray(response.snapshots)) {
    throw new APIError(502, "ui.invalid_api_response");
  }
  const snapshots = response.snapshots.map((raw) => {
    if (!isRecord(raw)
      || !isNonEmptyString(raw.provider)
      || !isNonEmptyString(raw.identity)
      || typeof raw.supported !== "boolean"
      || (raw.concurrency_configurable !== undefined && typeof raw.concurrency_configurable !== "boolean")
      || !isFiniteNonNegativeInteger(raw.active)
      || !isFiniteNonNegativeInteger(raw.limit)
      || !isFiniteNonNegativeNumber(raw.input_tokens)
      || !isFiniteNonNegativeNumber(raw.output_tokens)
      || !isFiniteNonNegativeNumber(raw.reasoning_tokens)
      || !isFiniteNonNegativeNumber(raw.cached_tokens)
      || !isFiniteNonNegativeNumber(raw.total_tokens)
      || !isFiniteNonNegativeNumber(raw.amount_usd)
      || !isFiniteNonNegativeInteger(raw.rated_requests)
      || !isFiniteNonNegativeInteger(raw.unrated_requests)
      || !isNonEmptyString(raw.updated_at)
      || (raw.auth_index !== undefined && typeof raw.auth_index !== "string")
      || (raw.reason !== undefined && typeof raw.reason !== "string")) {
      throw new APIError(502, "ui.invalid_api_response");
    }
    if (raw.models !== undefined && (!Array.isArray(raw.models) || raw.models.some((model) => {
      return !isRecord(model)
        || !isNonEmptyString(model.model)
        || !isFiniteNonNegativeNumber(model.input_tokens)
        || !isFiniteNonNegativeNumber(model.output_tokens)
        || !isFiniteNonNegativeNumber(model.reasoning_tokens)
        || !isFiniteNonNegativeNumber(model.cached_tokens)
        || !isFiniteNonNegativeNumber(model.total_tokens)
        || !isFiniteNonNegativeNumber(model.amount_usd)
        || typeof model.rated !== "boolean"
        || !isFiniteNonNegativeInteger(model.rated_requests)
        || !isFiniteNonNegativeInteger(model.unrated_requests);
    }))) {
      throw new APIError(502, "ui.invalid_api_response");
    }
    return raw as unknown as AIProviderRuntimeResponse["snapshots"][number];
  });
  return { snapshots, updated_at: response.updated_at };
}

export async function getAIProviderRuntime(signal?: AbortSignal): Promise<AIProviderRuntimeResponse> {
  // Runtime metrics are exposed by this plugin, not by CPA's native
  // management API.  Use the plugin API root so requests include
  // `/v0/management/plugins/cpa-account-config-manager`.
  return normalizeAIProviderRuntimeResponse(await requestRecord<unknown>("/ai-providers/runtime", { signal }));
}

export async function getQuotaPolicies(signal?: AbortSignal): Promise<QuotaPolicySnapshot> {
	// Quota policies are plugin-owned persisted settings.  Calling the host
	// management root here produced a misleading 404 on CPA because that path
	// is not a native CPA endpoint.
	const raw = await requestRecord<unknown>("/quota-policies", { signal });
	if (!isRecord(raw)) throw new APIError(502, "ui.invalid_api_response");
	const accounts = isRecord(raw.accounts) ? raw.accounts as QuotaPolicySnapshot["accounts"] : {};
	const providers = Array.isArray(raw.providers) ? raw.providers.filter(isRecord).map((item) => item as unknown as ProviderQuotaPolicy) : [];
	const storageError = typeof raw.storage_error === "string" ? raw.storage_error : undefined;
	return { accounts, providers, ...(storageError ? { storage_error: storageError } : {}) };
}

export async function saveAccountQuotaPolicy(accountID: string, policy: AccountQuotaPolicy): Promise<void> {
	await request("/quota-policies/account", { method: "PUT", body: JSON.stringify({ account_id: accountID, policy }) });
}

export async function saveAIProviderQuotaPolicy(policy: ProviderQuotaPolicy): Promise<void> {
	await request("/quota-policies/provider", { method: "PUT", body: JSON.stringify({ policy }) });
}

/** Fetch one provider channel, degrading non-auth failures to a channel error. */
async function loadAIProviderChannel(
  channel: (typeof AI_PROVIDER_CHANNELS)[number],
  signal?: AbortSignal,
): Promise<AIProviderChannelSnapshot> {
  try {
    if (channel.kind === "opencode-go") {
      const listed = await listOpenCodeAccounts(signal);
      return {
        kind: channel.kind,
        count: listed.accounts?.length ?? 0,
        entries: (listed.accounts ?? []).map((account, index) => ({
          index,
          account_id: account.id,
          workspace_id: account.workspace_id,
          name: account.workspace_id,
        })),
        ...(listed.storage_error ? { storage_error: "provider_storage_unavailable" } : {}),
      };
    }
    if (channel.kind === "opencode-zen") {
      const listed = await listOpenCodeZenAccounts(signal);
      return {
        kind: channel.kind,
        count: listed.accounts?.length ?? 0,
        entries: (listed.accounts ?? []).map((account, index) => ({
          index,
          account_id: account.id,
          name: account.name ?? account.base_url,
          base_url: account.base_url,
          key_set: account.key_set,
        })),
        ...(listed.storage_error ? { storage_error: "provider_storage_unavailable" } : {}),
      };
    }
    if (channel.apiPath) {
      const payload = await managementRequest<unknown>(channel.apiPath, { signal });
      const payloadRecord = isRecord(payload) ? payload : undefined;
      const rawEntries = payloadRecord?.[channel.kind];
      if (!Array.isArray(rawEntries)) {
        return { kind: channel.kind, count: 0, entries: [], error: "provider_channel_response_invalid" };
      }
      const entries = channelEntriesFromResponse(channel.kind, payload);
      return { kind: channel.kind, count: entries.length, entries };
    }
    return { kind: channel.kind, count: 0, entries: [] as AIProviderChannelEntry[] };
  } catch (caught) {
    if (signal?.aborted || (caught instanceof DOMException && caught.name === "AbortError")) throw caught;
    if (caught instanceof APIError && (caught.status === 401 || caught.status === 403)) throw caught;
    return {
      kind: channel.kind,
      count: 0,
      entries: [],
      error: caught instanceof APIError && caught.status === 502
        ? "provider_channel_response_invalid"
        : "provider_channel_unavailable",
    };
  }
}

/**
 * Fetch one channel as an authentication gate, then load the independent
 * provider channels in parallel to avoid serial page-load latency.
 */
export async function listAIProviderChannels(signal?: AbortSignal): Promise<AIProviderChannelSnapshot[]> {
  const [first, ...rest] = AI_PROVIDER_CHANNELS;
  if (!first) return [];
  const firstSnapshot = await loadAIProviderChannel(first, signal);
  const remaining = await Promise.all(rest.map((channel) => loadAIProviderChannel(channel, signal)));
  return [firstSnapshot, ...remaining];
}

export async function putAIProviderChannel(kind: AIProviderChannelKind, items: unknown[]): Promise<void> {
  await managementRequest<void>(`/${kind}`, {
    method: "PUT",
    body: JSON.stringify(items),
  });
}

async function getRawAIProviderChannelItems(kind: AIProviderChannelKind): Promise<unknown[]> {
  const payload = await managementRequest<unknown>(`/${kind}`);
  const raw = isRecord(payload) ? payload[kind] : undefined;
  if (!Array.isArray(raw)) throw new Error(kind + " channel is not available");
  return raw.map((item, index) => {
    if (typeof item === "string") return item;
    if (!isRecord(item)) {
      throw new Error(`${kind} channel entry #${index + 1} is malformed`);
    }
    // CPA annotates runtime credentials with auth-index metadata in GET
    // responses, while PUT structs ignore unknown JSON fields. Keep the
    // metadata so untouched credential rows survive a full-list rewrite;
    // stripping it early made OpenAI-compatible keys lose their live identity
    // after saving unrelated provider fields.
    return { ...item };
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

/** Serialize one AIProviderChannelModel into the host JSON shape (kebab-case tags). */
function modelToJSON(model: AIProviderChannelModel): Record<string, unknown> {
  // Start from the exact host model object when available. CPA adds fields over
  // time (for example `thinking` and provider-specific options); rebuilding a
  // model from only the visible editor fields silently discarded those fields.
  const out: Record<string, unknown> = { ...(model.raw ?? {}) };
  out["name"] = model.name;

  const setString = (key: string, value: string | undefined) => {
    if (value === undefined) return;
    if (value === "") delete out[key];
    else out[key] = value;
  };
  const setNumber = (key: string, value: number | undefined) => {
    if (value !== undefined) out[key] = value;
  };
  const setBoolean = (key: string, value: boolean | undefined) => {
    if (value !== undefined) out[key] = value;
  };
  const setList = (key: string, value: string[] | undefined) => {
    if (value === undefined) return;
    if (value.length === 0) delete out[key];
    else out[key] = value;
  };

  setString("alias", model.alias);
  setString("display-name", model.display_name);
  setNumber("max-context-length", model.max_context_length);
  setBoolean("force-mapping", model.force_mapping);
  setBoolean("is-compat", model.is_compat);
  setBoolean("image", model.image);
  setList("input-modalities", model.input_modalities);
  setList("output-modalities", model.output_modalities);
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

function keyEntriesToJSON(entries: Array<{ api_key?: string; weight?: number | string | null; proxy_url?: string; raw?: Record<string, unknown> }> | undefined): Record<string, unknown>[] | undefined {
  if (!entries || entries.length === 0) return undefined;
  return entries.flatMap((entry) => {
    const apiKey = typeof entry.api_key === "string" ? entry.api_key.trim() : "";
    // CPA treats an api-key-entries row without a key as a credential. Never
    // write editor-only blank rows (or rows containing only weight/proxy),
    // otherwise a harmless empty form row can shadow valid credentials.
    if (!apiKey) return [];
    // Preserve the original host row (including auth-index and future fields),
    // then apply only the credential fields exposed by the editor.
    const out: Record<string, unknown> = { ...(entry.raw ?? {}) };
    out["api-key"] = apiKey;
    // Empty editor fields mean "clear this override"; retaining the previous
    // host value would make the form look cleared while the saved channel kept
    // the old weight or proxy.
    if (entry.weight !== undefined && entry.weight !== null && !(typeof entry.weight === "string" && entry.weight.trim() === "")) {
      const parsed = Number(entry.weight);
      if (Number.isFinite(parsed) && parsed >= 0) out["weight"] = parsed;
      else delete out["weight"];
    } else {
      delete out["weight"];
    }
    if (typeof entry.proxy_url === "string" && entry.proxy_url.trim()) out["proxy-url"] = entry.proxy_url.trim();
    else delete out["proxy-url"];
    return [out];
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
  api_key_entries?: Array<{ api_key?: string; weight?: number | string | null; proxy_url?: string; raw?: Record<string, unknown> }>;
  support_prompt_cache_key?: boolean;
  disable_cooling?: boolean;
  request_retry?: number | null;
  request_scoped_errors?: unknown[];
  alpha_search?: boolean;
  websockets?: boolean;
  rebuild_mid_system_message?: boolean;
  fingerprint_profile?: string;
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

  const items = raw.map((item) => {
    if (typeof item === "string") return { "api-key": item };
    return isRecord(item) ? { ...item } : {};
  });
  const target = items[index];
  const patched: Record<string, unknown> = { ...target };

  const replacementAPIKey = patch.api_key?.trim() ?? "";
  if (kind === "openai-compatibility") {
    let keyEntries: Record<string, unknown>[];
    if (patch.api_key_entries !== undefined) {
      keyEntries = keyEntriesToJSON(patch.api_key_entries) ?? [];
    } else {
      keyEntries = Array.isArray(patched["api-key-entries"])
        ? patched["api-key-entries"].filter(isRecord)
        : [];
      patched["api-key-entries"] = keyEntries;
    }
    // CPA annotates each credential row with runtime auth-index metadata.
    // Preserve those rows verbatim when the editor did not touch credentials;
    // rebuilding them from visible fields would otherwise strip metadata and
    // make the saved channel disappear from AI provider runtime views.
    const legacyAPIKey = typeof patched["api-key"] === "string" ? patched["api-key"].trim() : "";
    const originalHasKeyEntries = Array.isArray((raw[index] as Record<string, unknown>)["api-key-entries"]);
    if (replacementAPIKey && originalHasKeyEntries) {
      // The editor only exposes the first credential as a simple replacement
      // field. Update that row in place so auth-index and other host metadata
      // survive instead of replacing the whole weighted key list.
      if (keyEntries.length === 0) keyEntries.push({ "api-key": replacementAPIKey });
      else keyEntries[0] = { ...keyEntries[0], "api-key": replacementAPIKey };
    } else if (patch.api_key_entries === undefined && keyEntries.length === 0 && legacyAPIKey) {
      // Older CPA releases stored this credential in the legacy top-level
      // field. Migrate it while rewriting the entry instead of silently
      // deleting it when the user only changes unrelated settings.
      keyEntries.push({ "api-key": legacyAPIKey });
    }
    if (patch.api_key_entries !== undefined || replacementAPIKey || (legacyAPIKey && !originalHasKeyEntries)) {
      patched["api-key-entries"] = keyEntries;
    }
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
  if (patch.request_retry !== undefined) {
    if (patch.request_retry === null) delete patched["request-retry"];
    else patched["request-retry"] = patch.request_retry;
  }
  if (patch.request_scoped_errors !== undefined) {
    if (patch.request_scoped_errors.length === 0) delete patched["request-scoped-errors"];
    else patched["request-scoped-errors"] = patch.request_scoped_errors;
  }
  if (patch.alpha_search !== undefined) patched["alpha-search"] = patch.alpha_search;
  if (patch.websockets !== undefined) patched["websockets"] = patch.websockets;
  if (patch.rebuild_mid_system_message !== undefined) patched["rebuild-mid-system-message"] = patch.rebuild_mid_system_message;
  if (patch.fingerprint_profile !== undefined) {
    const profile = patch.fingerprint_profile.trim();
    if (profile) patched["fingerprint-profile"] = profile;
    else delete patched["fingerprint-profile"];
  }
  if (patch.headers !== undefined) {
    if (Object.keys(patch.headers).length > 0) patched["headers"] = patch.headers;
    else delete patched["headers"];
  }
  if (patch.excluded_models !== undefined) {
    if (patch.excluded_models.length > 0) patched["excluded-models"] = patch.excluded_models;
    else delete patched["excluded-models"];
  }
  if (patch.models !== undefined) {
    const existingModels = Array.isArray(patched["models"])
      ? (patched["models"] as Array<Record<string, unknown>>)
      : [];
    const existingByName = new Map<string, Record<string, unknown>>();
    for (const existing of existingModels) {
      const name = typeof existing["name"] === "string" ? existing["name"].trim() : "";
      if (name && !existingByName.has(name)) existingByName.set(name, existing);
    }
    patched["models"] = patch.models.map((model) => {
      // Callers that loaded the channel through listAIProviderChannels carry a
      // raw model snapshot. For other callers, merge by model name so a partial
      // edit still keeps CPA fields unknown to this UI.
      const existing = existingByName.get(model.name.trim());
      return modelToJSON(existing ? { ...model, raw: { ...existing, ...(model.raw ?? {}) } } : model);
    });
  }
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
  model?: string;
  status?: "available" | "unavailable" | "unsupported" | "review";
  probe_kind?: string;
  reason_code?: string;
  latency_ms?: number;
  tested_at?: string;
  response?: import("../types").ModelTestResponsePreview;
  models?: import("../types").AccountModelOption[];
}

export async function testAIProviderChannel(baseURL: string, apiKey: string, timeoutSeconds = 15, headers?: Record<string, string>): Promise<AIProviderProbeResult> {
  return testAIProviderChannelForKind("openai-compatibility", baseURL, apiKey, timeoutSeconds, headers);
}

export async function testAIProviderChannelForKind(
  kind: AIProviderChannelKind,
  baseURL: string,
  apiKey: string,
  timeoutSeconds = 15,
  headers?: Record<string, string>,
  authID?: string,
  model?: string,
): Promise<AIProviderProbeResult> {
  const response = await requestRecord<AIProviderProbeResult>("/ai-providers/test", {
    method: "POST",
    body: JSON.stringify({
      kind,
      base_url: baseURL,
      api_key: apiKey,
      timeout_seconds: timeoutSeconds,
      ...(headers && Object.keys(headers).length > 0 ? { headers } : {}),
      ...(authID ? { auth_id: authID } : {}),
      ...(model ? { model: model.trim() } : {}),
    }),
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

function normalizeProxyProfilesResponse(response: unknown): ProxyProfileListResponse {
	if (!isRecord(response)) throw new APIError(502, "ui.invalid_api_response");
	const profiles = nullableRecordArray(response.profiles);
	if (profiles === undefined || profiles.some((item) =>
		!isNonEmptyString(item.id)
		|| !isNonEmptyString(item.name)
		|| !isNonEmptyString(item.proxy_url_masked)
		|| typeof item.enabled !== "boolean"
		|| !isFiniteNonNegativeInteger(item.account_count)
	)) {
		throw new APIError(502, "ui.invalid_api_response");
	}
	return {
		profiles: profiles.map((item) => ({
			id: item.id as string,
			name: item.name as string,
			proxy_url_masked: item.proxy_url_masked as string,
			note: typeof item.note === "string" ? item.note : undefined,
			providers: Array.isArray(item.providers) ? item.providers.filter((value): value is string => typeof value === "string") : [],
			enabled: item.enabled as boolean,
			account_count: Number(item.account_count),
			created_at: typeof item.created_at === "string" ? item.created_at : "",
			updated_at: typeof item.updated_at === "string" ? item.updated_at : "",
		})),
		...(typeof response.storage_error === "string" ? { storage_error: response.storage_error } : {}),
	};
}

export async function listProxyProfiles(signal?: AbortSignal): Promise<ProxyProfileListResponse> {
	return normalizeProxyProfilesResponse(await requestRecord<unknown>("/proxy-profiles", { signal }));
}

export async function createProxyProfile(input: ProxyProfileInput): Promise<ProxyProfileView> {
	const response = await requestRecord<{ profile?: unknown }>("/proxy-profiles", { method: "POST", body: JSON.stringify(input) });
	return parseProxyProfile(response.profile);
}

export async function updateProxyProfile(input: ProxyProfileInput & { id: string }): Promise<ProxyProfileView> {
	const response = await requestRecord<{ profile?: unknown }>("/proxy-profiles", { method: "PUT", body: JSON.stringify(input) });
	return parseProxyProfile(response.profile);
}

export async function deleteProxyProfile(id: string, force = false): Promise<void> {
	await request("/proxy-profiles?id=" + encodeURIComponent(id) + (force ? "&force=true" : ""), { method: "DELETE" });
}

function parseProxyProfile(value: unknown): ProxyProfileView {
	const normalized = normalizeProxyProfilesResponse({ profiles: [value] });
	if (normalized.profiles.length !== 1) throw new APIError(502, "ui.invalid_api_response");
	return normalized.profiles[0];
}

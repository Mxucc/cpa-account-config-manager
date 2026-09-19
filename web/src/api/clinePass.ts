import {
	APIError,
	isFiniteNonNegativeInteger,
	isFiniteNonNegativeNumber,
	isNonEmptyString,
	isRecord,
	nullableRecordArray,
	request,
	requestRecord,
	stringArrayOrUndefined,
} from "./client";
import type {
	ClinePassAccountResponse,
	ClinePassAccountSaveResponse,
	ClinePassAccountView,
	ClinePassAccountsResponse,
	ClinePassBindResponse,
	ClinePassCatalogResponse,
	ClinePassLoginCancelResponse,
	ClinePassLoginView,
	ClinePassModelView,
	ClinePassModelsResponse,
	ClinePassQuotaUsage,
	ClinePassQuotaWindow,
	ClinePassRefreshResponse,
	ClinePassSettings,
	ClinePassSettingsPatch,
	ClinePassSettingsResponse,
	ClinePassSettingsSaveResponse,
} from "./clinePassTypes";
import type { OpenCodeModelTestResult } from "../types";

/** The shared transport error, re-exported so Cline Pass callers need one import. */
export { APIError } from "./client";

/**
 * Cline Pass API client. Cline Pass is its own product (its own gateway and credential),
 * so it keeps its own module; the transport and the response guards are imported from the
 * shared client instead of being duplicated.
 */
/**
 * Cline Pass reference rates are USD per 1M tokens and are only meaningful when the handler priced
 * the model, so an unpriced model carries no rate at all. A partially numeric payload is treated as
 * unpriced rather than as a free model.
 */
function clinePassReferenceRate(model: Record<string, unknown>): Partial<ClinePassModelView> {
	if (model.priced !== true) return {};
	const rate: Partial<ClinePassModelView> = {};
	for (const field of ["input_usd_per_million", "output_usd_per_million", "cache_read_usd_per_million", "cache_write_usd_per_million"] as const) {
		if (isFiniteNonNegativeNumber(model[field])) rate[field] = model[field] as number;
	}
	return rate;
}

/** One documented usage window. Every field degrades to 0 so a partial payload cannot fail a list. */
function normalizeClinePassQuotaWindow(value: unknown): ClinePassQuotaWindow {
	const source = isRecord(value) ? value : {};
	return {
		usd: isFiniteNonNegativeNumber(source.usd) ? source.usd : 0,
		input_tokens: isFiniteNonNegativeInteger(source.input_tokens) ? source.input_tokens : 0,
		output_tokens: isFiniteNonNegativeInteger(source.output_tokens) ? source.output_tokens : 0,
		requests: isFiniteNonNegativeInteger(source.requests) ? source.requests : 0,
		// The cached halves are part of the documented windows and the backend reports them
		// separately, so dropping them here would show every cached total as 0.
		cache_read_tokens: isFiniteNonNegativeInteger(source.cache_read_tokens) ? source.cache_read_tokens : 0,
		cache_write_tokens: isFiniteNonNegativeInteger(source.cache_write_tokens) ? source.cache_write_tokens : 0,
		// Requests Cline could not reference price must survive normalization, or the UI would
		// render their window as if it were simply free.
		unpriced_requests: isFiniteNonNegativeInteger(source.unpriced_requests) ? source.unpriced_requests : 0,
	};
}

/**
 * The three windows Cline documents for ClinePass. A backend that predates them omits the field, so
 * an absent payload returns undefined and the account simply renders without a usage line.
 */
function normalizeClinePassQuotaUsage(value: unknown): ClinePassQuotaUsage | undefined {
	if (!isRecord(value)) return undefined;
	return {
		five_hour: normalizeClinePassQuotaWindow(value.five_hour),
		weekly: normalizeClinePassQuotaWindow(value.weekly),
		monthly: normalizeClinePassQuotaWindow(value.monthly),
		monthly_subscription_usd: isFiniteNonNegativeNumber(value.monthly_subscription_usd) ? value.monthly_subscription_usd : 0,
		reference: value.reference === true,
	};
}

function normalizeClinePassAccountsResponse(response: unknown): ClinePassAccountsResponse {
	if (!isRecord(response)) throw new APIError(502, "ui.invalid_api_response");
	const accounts = nullableRecordArray(response.accounts);
	if (accounts === undefined || accounts.some((account) =>
		!isNonEmptyString(account.id)
		|| !isNonEmptyString(account.base_url)
		|| typeof account.access_token_set !== "boolean"
		|| typeof account.refresh_token_set !== "boolean"
		|| typeof account.expired !== "boolean"
		|| (account.auth_method !== "oauth" && account.auth_method !== "api_key" && account.auth_method !== "cli")
		|| (account.name !== undefined && typeof account.name !== "string")
		|| (account.expires_at !== undefined && typeof account.expires_at !== "string")
	)) {
		throw new APIError(502, "ui.invalid_api_response");
	}
	if (response.storage_error !== undefined && typeof response.storage_error !== "string") {
		throw new APIError(502, "ui.invalid_api_response");
	}
	return {
		accounts: accounts.map((account) => {
			const view: ClinePassAccountView = {
				id: (account.id as string).trim(),
				base_url: (account.base_url as string).trim(),
				auth_method: account.auth_method as ClinePassAccountView["auth_method"],
				access_token_set: account.access_token_set as boolean,
				refresh_token_set: account.refresh_token_set as boolean,
				expired: account.expired as boolean,
				// A pre-binding backend omits the routing fields, so an absent value degrades to
				// "unbound" instead of failing the whole account list.
				channel_bound: account.channel_bound === true,
				channel_state_unreadable: account.channel_state_unreadable === true,
				channel_models: isFiniteNonNegativeNumber(account.channel_models) ? account.channel_models : 0,
				channel_model_gaps: isFiniteNonNegativeNumber(account.channel_model_gaps) ? account.channel_model_gaps : 0,
				channel_credential_rejected: account.channel_credential_rejected === true,
			};
			if (typeof account.name === "string" && account.name.trim()) view.name = account.name;
			if (typeof account.expires_at === "string" && account.expires_at) view.expires_at = account.expires_at;
			const models = stringArrayOrUndefined(account.models);
			if (models) view.models = models;
			if (typeof account.models_error === "string" && account.models_error.trim()) view.models_error = account.models_error.trim();
			if (typeof account.models_fetched_at === "string" && account.models_fetched_at) view.models_fetched_at = account.models_fetched_at;
			if (typeof account.created_at === "string" && account.created_at) view.created_at = account.created_at;
			const quotaUsage = normalizeClinePassQuotaUsage(account.quota_usage);
			if (quotaUsage) view.quota_usage = quotaUsage;
			return view;
		}),
		...(typeof response.storage_error === "string" ? { storage_error: response.storage_error } : {}),
	};
}

/** The published Cline Pass settings, with the additive upstream-consistency switch. */
function normalizeClinePassSettings(value: unknown): ClinePassSettings {
	if (!isRecord(value) || typeof value.strip_model_prefix !== "boolean") {
		throw new APIError(502, "ui.invalid_api_response");
	}
	// The switch is additive, so a response from a build that predates it reads as
	// the documented default (off) instead of failing the page.
	return {
		strip_model_prefix: value.strip_model_prefix,
		deepseek_upstream_consistency: value.deepseek_upstream_consistency === true,
	};
}

/**
 * The model mapping the Cline Pass channel publishes. The rows drive the table, so a row
 * without the ids the probe or the client needs is a broken payload; the binding summary
 * degrades to "unbound" for a pre-binding backend, exactly like the account list does.
 */
function normalizeClinePassModelsResponse(response: unknown): ClinePassModelsResponse {
	if (!isRecord(response) || typeof response.strip_model_prefix !== "boolean") {
		throw new APIError(502, "ui.invalid_api_response");
	}
	const models = nullableRecordArray(response.models);
	if (models === undefined || models.some((model) =>
		!isNonEmptyString(model.id)
		|| !isNonEmptyString(model.upstream_id)
		|| !isNonEmptyString(model.client_id)
		|| typeof model.published !== "boolean"
	)) {
		throw new APIError(502, "ui.invalid_api_response");
	}
	return {
		models: models.map((model): ClinePassModelView => ({
			id: (model.id as string).trim(),
			name: isNonEmptyString(model.name) ? model.name.trim() : (model.client_id as string).trim(),
			free: model.free === true,
			upstream_id: (model.upstream_id as string).trim(),
			client_id: (model.client_id as string).trim(),
			published: model.published === true,
			priced: model.priced === true,
			...clinePassReferenceRate(model),
		})),
		strip_model_prefix: response.strip_model_prefix,
		deepseek_upstream_consistency: response.deepseek_upstream_consistency === true,
		accounts: isFiniteNonNegativeInteger(response.accounts) ? response.accounts : 0,
		channel_bound: response.channel_bound === true,
		channel_state_unreadable: response.channel_state_unreadable === true,
		channel_models: isFiniteNonNegativeInteger(response.channel_models) ? response.channel_models : 0,
		default_base_url: typeof response.default_base_url === "string" ? response.default_base_url : "",
	};
}

export async function listClinePassAccounts(signal?: AbortSignal): Promise<ClinePassAccountsResponse> {
	return normalizeClinePassAccountsResponse(await requestRecord<unknown>("/opencode/cline-pass/accounts", { signal }));
}

/** Save or correct one Cline Pass credential; an API key is what creates a new account. */
export async function saveClinePassAccount(options: {
	account_id?: string;
	name?: string;
	base_url?: string;
	api_key?: string;
	timeout_seconds?: number;
}): Promise<ClinePassAccountSaveResponse> {
	return requestRecord<ClinePassAccountSaveResponse>("/opencode/cline-pass/accounts", {
		method: "POST",
		body: JSON.stringify(options),
	});
}

export async function removeClinePassAccount(accountID: string): Promise<void> {
	await request<{ removed: boolean }>("/opencode/cline-pass/accounts?account_id=" + encodeURIComponent(accountID), {
		method: "DELETE",
	});
}

/** Read the allow-listed Cline Pass model catalog and the default gateway base URL. */
export async function getClinePassCatalog(signal?: AbortSignal): Promise<ClinePassCatalogResponse> {
	return requestRecord<ClinePassCatalogResponse>("/opencode/cline-pass/catalog", { signal });
}

/** Read the Cline Pass publishing settings; currently only the model-id prefix rule. */
export async function getClinePassSettings(signal?: AbortSignal): Promise<ClinePassSettingsResponse> {
	const response = await requestRecord<Record<string, unknown>>("/opencode/cline-pass/settings", { signal });
	return { settings: normalizeClinePassSettings(response.settings) };
}

/**
 * Persist the prefix setting. Saving re-binds the Cline Pass channel, so the published
 * mapping changes immediately; `rebound`/`rebind_errors` report that re-bind. A re-bind
 * error does not fail the save: the setting itself is stored either way.
 */
export async function saveClinePassSettings(patch: ClinePassSettingsPatch): Promise<ClinePassSettingsSaveResponse> {
	const body: Record<string, boolean> = {};
	if (patch.stripModelPrefix !== undefined) body.strip_model_prefix = patch.stripModelPrefix;
	if (patch.deepseekUpstreamConsistency !== undefined) body.deepseek_upstream_consistency = patch.deepseekUpstreamConsistency;
	const response = await requestRecord<Record<string, unknown>>("/opencode/cline-pass/settings", {
		method: "PUT",
		body: JSON.stringify(body),
	});
	return {
		settings: normalizeClinePassSettings(response.settings),
		rebound: isFiniteNonNegativeInteger(response.rebound) ? response.rebound : 0,
		rebind_errors: isFiniteNonNegativeInteger(response.rebind_errors) ? response.rebind_errors : 0,
	};
}

/** Read the model mapping the Cline Pass channel publishes, with its binding summary. */
export async function listClinePassModels(signal?: AbortSignal): Promise<ClinePassModelsResponse> {
	return normalizeClinePassModelsResponse(await requestRecord<unknown>("/opencode/cline-pass/models", { signal }));
}

/** Start a sign-in: the browser device flow, an existing Cline CLI sign-in or an API key. */
export async function startClinePassLogin(options: {
	method?: "oauth" | "cli" | "api_key";
	name?: string;
	api_key?: string;
	base_url?: string;
}): Promise<ClinePassLoginView> {
	return requestRecord<ClinePassLoginView>("/opencode/cline-pass/login/start", {
		method: "POST",
		body: JSON.stringify(options),
	});
}

/** One non-blocking poll of a pending device sign-in. An unknown session answers 404. */
export async function pollClinePassLogin(sessionID: string): Promise<ClinePassLoginView> {
	return requestRecord<ClinePassLoginView>("/opencode/cline-pass/login/poll", {
		method: "POST",
		body: JSON.stringify({ session_id: sessionID }),
	});
}

export async function cancelClinePassLogin(sessionID: string): Promise<ClinePassLoginCancelResponse> {
	return requestRecord<ClinePassLoginCancelResponse>("/opencode/cline-pass/login/cancel", {
		method: "POST",
		body: JSON.stringify({ session_id: sessionID }),
	});
}

/** Rotate one stored token and, with `rebind`, republish its CPA channel key. */
export async function refreshClinePassAccount(accountID: string, rebind = false): Promise<ClinePassRefreshResponse> {
	return requestRecord<ClinePassRefreshResponse>("/opencode/cline-pass/refresh", {
		method: "POST",
		body: JSON.stringify({ account_id: accountID, ...(rebind ? { rebind: true } : {}) }),
	});
}

/** Validate one stored credential against the gateway catalog. A catalog failure stays on the account. */
export async function refreshClinePassModels(accountID: string): Promise<ClinePassAccountResponse> {
	return requestRecord<ClinePassAccountResponse>("/opencode/cline-pass/models", {
		method: "POST",
		body: JSON.stringify({ account_id: accountID }),
	});
}

/** Probe one Cline Pass model through the stored credential. */
export async function testClinePassModel(accountID: string, model: string, timeoutSeconds = 30): Promise<{ result: OpenCodeModelTestResult }> {
	return requestRecord<{ result: OpenCodeModelTestResult }>("/opencode/cline-pass/model-test", {
		method: "POST",
		body: JSON.stringify({ account_id: accountID, model, timeout_seconds: timeoutSeconds }),
	});
}

/** Publish the models of one account to CPA routing as one OpenAI-compatible channel. */
export async function bindClinePassChannel(accountID: string): Promise<ClinePassBindResponse> {
	return requestRecord<ClinePassBindResponse>("/opencode/cline-pass/bind", {
		method: "POST",
		body: JSON.stringify({ account_id: accountID }),
	});
}

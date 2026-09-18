/**
 * Cline Pass response models. Cline Pass is its own product (its own gateway and
 * credential), so its payload shapes live here instead of in the shared `types.ts`.
 * Tokens are replaced by booleans, exactly like the Zen view: the plugin never
 * returns a stored secret to the browser.
 */

import type { ProviderChannelBindingResult } from "../types";

export interface ClinePassAccountView {
  id: string;
  name?: string;
  base_url: string;
  auth_method: "oauth" | "api_key" | "cli";
  access_token_set: boolean;
  refresh_token_set: boolean;
  expires_at?: string;
  expired: boolean;
  models?: string[];
  models_error?: string;
  models_fetched_at?: string;
  created_at?: string;
  /**
   * CPA routing state. The credential is only routable once a CPA channel carries this
   * account's base URL; `channel_model_gaps` counts the account models that channel omits
   * and equals the whole model list while the account is unbound.
   */
  channel_bound: boolean;
  /** The live channel list could not be read, so channel_bound is unknown rather than false. */
  channel_state_unreadable?: boolean;
  channel_models: number;
  channel_model_gaps: number;
  /** Usage the Cline gateway attributes to this account; absent until Cline reports it. */
  quota_usage?: ClinePassQuotaUsage;
}

/**
 * One window of Cline Pass usage. `usd` is the reference price the documented standard API
 * rates give for exactly these tokens: never an amount owed.
 */
export interface ClinePassQuotaWindow {
  usd: number;
  input_tokens: number;
  output_tokens: number;
  requests: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
  unpriced_requests?: number;
}

/**
 * The three windows Cline documents for ClinePass: a rolling 5-hour window, the calendar
 * week and the calendar month. Every USD figure is REFERENCE-priced at the documented
 * standard API rates (`reference: true`), not a charge: the subscription is a flat
 * monthly fee, reported separately as `monthly_subscription_usd`.
 */
export interface ClinePassQuotaUsage {
  five_hour?: ClinePassQuotaWindow;
  weekly?: ClinePassQuotaWindow;
  monthly?: ClinePassQuotaWindow;
  monthly_subscription_usd?: number;
  /** True while `usd` values are reference prices from the documented rates, not charges. */
  reference?: boolean;
}

export interface ClinePassAccountsResponse {
  accounts: ClinePassAccountView[];
  storage_error?: string;
}

/** One allow-listed Cline Pass catalog model. */
export interface ClinePassCatalogModel {
  id: string;
  name: string;
  free: boolean;
}

export interface ClinePassCatalogResponse {
  models: ClinePassCatalogModel[];
  default_base_url: string;
}
/** One row of the published Cline Pass mapping: what a client calls and what the gateway receives. */
export interface ClinePassModelView {
  id: string;
  name: string;
  free: boolean;
  /** The model id the Cline gateway accepts; the probe must send this one. */
  upstream_id: string;
  /** The model id a client calls once the channel is published. */
  client_id: string;
  published: boolean;
  /**
   * The documented reference rates for this model, in USD per million tokens. They are
   * omitted when the documentation publishes no rate, and `priced` is then false.
   */
  priced?: boolean;
  input_usd_per_million?: number;
  output_usd_per_million?: number;
  cache_read_usd_per_million?: number;
  cache_write_usd_per_million?: number;
}

export interface ClinePassModelsResponse {
  models: ClinePassModelView[];
  strip_model_prefix: boolean;
  /**
   * The stored upstream-consistency switch: Cline Pass DeepSeek requests are pinned to
   * DeepSeek's own upstream so one conversation keeps its prompt cache.
   */
  deepseek_upstream_consistency: boolean;
  accounts: number;
  channel_bound: boolean;
  channel_models: number;
  /** The live channel list could not be read, so channel_bound is unknown rather than false. */
  channel_state_unreadable?: boolean;
  default_base_url: string;
}

/** Publishing settings for the Cline Pass channel. */
export interface ClinePassSettings {
  strip_model_prefix: boolean;
  deepseek_upstream_consistency: boolean;
}

/** One saved control: an omitted field leaves the stored switch alone. */
export interface ClinePassSettingsPatch {
  stripModelPrefix?: boolean;
  deepseekUpstreamConsistency?: boolean;
}

export interface ClinePassSettingsResponse {
  settings: ClinePassSettings;
}

/** Saving settings re-binds the channel; the counts report that re-bind. */
export interface ClinePassSettingsSaveResponse extends ClinePassSettingsResponse {
  rebound: number;
  rebind_errors: number;
}

/** One in-flight Cline Pass sign-in: the device flow, the CLI reuse or an API key. */
export interface ClinePassLoginView {
  session_id?: string;
  method?: string;
  status: "pending" | "completed" | "failed" | "expired" | "cancelled";
  user_code?: string;
  verification_uri?: string;
  verification_uri_complete?: string;
  interval_seconds?: number;
  expires_in_seconds?: number;
  error?: string;
  account?: ClinePassAccountView;
  /**
   * The CPA channel the automatic bind wrote on a completed sign-in, and why that bind
   * failed. The sign-in itself still succeeds when only `binding_error` is set.
   */
  binding?: ProviderChannelBindingResult;
  binding_error?: string;
}

export interface ClinePassProbeResult {
  reachable: boolean;
  status_code?: number;
  detail?: string;
}

export interface ClinePassAccountSaveResponse {
  account: ClinePassAccountView;
  result: ClinePassProbeResult;
  /** The CPA channel the automatic bind wrote, and why that bind failed; the save still succeeded. */
  binding?: ProviderChannelBindingResult;
  binding_error?: string;
}

export interface ClinePassBindResponse {
  binding: ProviderChannelBindingResult;
}

export interface ClinePassRefreshResponse {
  account: ClinePassAccountView;
  /** Present when the caller asked for the CPA channel to be republished. */
  binding?: ProviderChannelBindingResult;
}

export interface ClinePassAccountResponse {
  account: ClinePassAccountView;
}

export interface ClinePassLoginCancelResponse {
  cancelled: boolean;
}

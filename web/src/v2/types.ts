// Wire types for the unified provider API. They mirror internal/provider's JSON
// exactly; a rename on either side is meant to break the build or a test.

export type ProviderStatus = "ready" | "quota_limited" | "credential_invalid" | "repairing" | "disabled" | "unknown";

export interface ProviderCapabilities {
  login: boolean;
  quota: boolean;
  models: boolean;
  binding: boolean;
  billing: boolean;
  refresh: boolean;
  test: boolean;
  host_auth: boolean;
}

export interface ProviderDefinition {
  id: string;
  display_name: string;
  kind: string;
  description?: string;
  docs_url?: string;
  auth_modes?: string[];
  capabilities: ProviderCapabilities;
}

export interface ProviderView extends ProviderDefinition {
  accounts: number;
  by_status: Record<string, number>;
}

export interface CredentialState {
  configured: boolean;
  method?: string;
  expires_at?: string;
  expired?: boolean;
  refreshable?: boolean;
  repairing?: boolean;
  rejected?: boolean;
  reason?: string;
}

export interface PlanCycle {
  kind: "none" | "rolling" | "weekly" | "monthly" | "annual";
  starts_at?: string;
  renews_at?: string;
  resets_at?: string;
}

export interface PlanPrice {
  amount: number;
  currency: string;
  period?: string;
  source?: string;
}

export interface PlanState {
  id?: string;
  name?: string;
  tier?: string;
  cycle: PlanCycle;
  price?: PlanPrice;
}

export interface QuotaWindow {
  id: string;
  label: string;
  kind: string;
  source_window?: string;
  used_percent?: number | null;
  remaining_percent?: number | null;
  limit?: number | null;
  unit?: string;
  balance?: number | null;
  reset_at?: string;
  aside?: boolean;
  source?: string;
  note?: string;
}

export interface BillingState {
  mode: "subscription" | "metered" | "wallet" | "unknown";
  currency?: string;
  allowance?: number | null;
  used?: number | null;
  remaining?: number | null;
  period?: string;
  rate_source?: string;
}

export interface UsageState {
  input_tokens: number;
  output_tokens: number;
  total_tokens: number;
  cost_usd: number;
  last_request_at?: string;
  updated_at?: string;
}

export interface ModelsState {
  count: number;
  source?: string;
  error?: string;
  fetched_at?: string;
}

export interface BindingState {
  kind?: string;
  channel?: string;
  published: boolean;
  models?: number;
  gaps?: number;
  readable?: boolean;
  error?: string;
}

export interface ProviderMetric {
  key: string;
  label: string;
  value: number;
  unit?: string;
  format?: string;
  currency?: string;
}

export interface ProviderAccount {
  id: string;
  provider: { id: string; kind?: string; display_name?: string };
  display_name: string;
  status: ProviderStatus;
  status_note?: string;
  credential: CredentialState;
  plan: PlanState;
  quota: QuotaWindow[];
  metrics?: ProviderMetric[];
  billing: BillingState;
  usage: UsageState;
  models: ModelsState;
  binding?: BindingState;
  source: { kind: string; ref?: string };
  updated_at: string;
}

export interface ProviderOverview {
  accounts: number;
  by_status: Record<string, number>;
  providers: number;
  publication_usd: number;
  updated_at: string;
}

export interface ProviderAccountsResponse {
  accounts: ProviderAccount[];
  total: number;
}

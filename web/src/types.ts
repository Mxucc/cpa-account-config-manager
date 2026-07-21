export interface Account {
  id: string;
  auth_id?: string;
  name: string;
  provider?: string;
  type?: string;
  label?: string;
  email?: string;
  project_id?: string;
  account_type?: string;
  plan_type?: string;
  status?: string;
  status_message?: string;
  disabled: boolean;
  unavailable: boolean;
  runtime_only: boolean;
  source?: string;
  priority?: number;
  note?: string;
  prefix?: string;
  proxy?: string;
  proxy_configured: boolean;
  websockets?: boolean;
  header_names?: string[];
  header_count: number;
  editable: boolean;
  read_only_reason?: string;
  success: number;
  failed: number;
  recent_requests?: RecentRequestEntry[];
  next_retry_after?: string;
  usage?: AccountUsageSnapshot;
  updated_at?: string;
  last_refresh?: string;
  automation?: AccountAutomationSummary;
}

export interface AccountAutomationSummary {
  health: InspectionHealth;
  reason_code: string;
  recommendation: "keep" | "reauth" | "review" | "disable" | "enable" | "delete";
  last_checked_at: string;
  owned_disable: boolean;
  disable_reason?: string;
  disabled_at?: string;
  recover_after?: string;
  delete_eligible_at?: string;
  delete_retry_after?: string;
  auto_action?: "disable" | "enable" | "delete" | "delete_candidate";
  auto_action_status?: "pending" | "succeeded" | "failed" | "skipped";
  auto_disable_eligible: boolean;
  inspection_enabled: boolean;
  auto_disable_enabled: boolean;
  auto_enable_enabled: boolean;
  auto_delete_enabled: boolean;
  failure_threshold: number;
  failure_streak: number;
  recovery_threshold: number;
  healthy_streak: number;
  passive_circuit_enabled?: boolean;
  passive_failure_threshold?: number;
  passive_failure_streak?: number;
  circuit_open?: boolean;
  circuit_reason_code?: string;
}

export interface RecentRequestEntry {
  time: string;
  success: number;
  failed: number;
}

export interface UsageWindowSnapshot {
  used_percent: number;
  reset_at?: string;
  window_minutes?: number;
}

export interface CodexUsageSnapshot {
  five_hour?: UsageWindowSnapshot;
  seven_day?: UsageWindowSnapshot;
  observed_at: string;
}

export interface AccountUsageSnapshot {
  input_tokens: number;
  output_tokens: number;
  reasoning_tokens: number;
  cached_tokens: number;
  cache_read_tokens: number;
  cache_creation_tokens: number;
  total_tokens: number;
  last_request_at?: string;
  updated_at?: string;
  codex?: CodexUsageSnapshot;
}

export interface AccountFilters {
  provider?: string;
  type?: string;
  status?: string;
  disabled?: boolean;
  editability?: string;
  source?: string;
  search?: string;
}

export interface AccountListResponse {
  accounts: Account[];
  total: number;
  page: number;
  page_size: number;
  pages: number;
}

export type ModelTestStatus = "available" | "unavailable" | "unsupported" | "review";

export interface ModelTestResult {
  account_id: string;
  provider: string;
  model: string;
  status: ModelTestStatus;
  reason_code: string;
  latency_ms: number;
  tested_at: string;
}

export interface AccountDeleteTarget {
  id: string;
  name: string;
  provider?: string;
  type?: string;
  plan_type?: string;
  label?: string;
  email?: string;
  status?: string;
  source?: string;
}

export interface AccountDeletePreview {
  id: string;
  created_at: string;
  expires_at: string;
  account: AccountDeleteTarget;
}

export interface AccountDeleteResult {
  status: "deleted";
  deleted_at: string;
  account: AccountDeleteTarget;
}

export interface HeaderPatch {
  set?: Record<string, string>;
  remove?: string[];
}

export interface BatchPatch {
  disabled?: boolean;
  priority?: number;
  note?: string;
  prefix?: string;
  proxy_url?: string;
  websockets?: boolean;
  headers?: HeaderPatch;
}

export interface TargetScope {
  mode: "selected" | "filtered";
  ids?: string[];
  filters?: AccountFilters;
}

export interface PatchSummary {
  fields: string[];
  header_set?: string[];
  header_remove?: string[];
  proxy_mutation: boolean;
}

export interface PreviewTarget {
  id: string;
  name?: string;
  provider?: string;
  label?: string;
  eligible: boolean;
  read_only_reason?: string;
}

export interface BatchPreview {
  id: string;
  created_at: string;
  expires_at: string;
  scope_mode: string;
  total: number;
  eligible: number;
  read_only: number;
  missing: number;
  physical_files: number;
  providers: Record<string, number>;
  patch: PatchSummary;
  warnings?: string[];
  targets: PreviewTarget[];
}

export type JobState = "idle" | "running" | "completed" | "partial" | "failed" | "interrupted";

export interface JobResult {
  id: string;
  name?: string;
  provider?: string;
  label?: string;
  status: "pending" | "running" | "succeeded" | "failed" | "conflict" | "skipped" | "interrupted";
  error?: string;
  applied_fields?: string[];
  retryable: boolean;
}

export interface JobSnapshot {
  id?: string;
  parent_job_id?: string;
  state: JobState;
  running: boolean;
  total: number;
  eligible: number;
  done: number;
  succeeded: number;
  failed: number;
  conflicts: number;
  skipped: number;
  workers: number;
  patch: PatchSummary;
  started_at?: string;
  finished_at?: string;
  retry_available: boolean;
  persisted: boolean;
  results?: JobResult[];
}

export interface DefaultPolicy {
  enabled: boolean;
  apply_mode: "missing";
  scan_interval_seconds: number;
  priority: number | null;
  websockets: boolean | null;
}

export interface PolicyScanSummary {
  started_at?: string;
  finished_at?: string;
  scanned: number;
  eligible: number;
  changed: number;
  skipped: number;
  failed: number;
  error?: string;
}

export interface PolicySnapshot {
  policy: DefaultPolicy;
  running: boolean;
  scan_started_at?: string;
  last_scan: PolicyScanSummary;
}

export interface ForcePolicySummary {
  fields: string[];
  priority: number | null;
  websockets: boolean | null;
}

export interface ForceSyncPreview {
  id: string;
  created_at: string;
  expires_at: string;
  total: number;
  eligible: number;
  read_only: number;
  physical_files: number;
  policy: ForcePolicySummary;
  warnings?: string[];
  targets: PreviewTarget[];
}

export interface ForceSyncJobSnapshot {
  id?: string;
  state: JobState;
  running: boolean;
  total: number;
  eligible: number;
  done: number;
  succeeded: number;
  failed: number;
  conflicts: number;
  skipped: number;
  workers: number;
  policy: ForcePolicySummary;
  started_at?: string;
  finished_at?: string;
  results?: JobResult[];
}

export interface ImportSkippedItem {
  source_name: string;
  source_path?: string;
  reason: string;
}

export interface ImportPreviewItem {
  index: number;
  source_name: string;
  source_path?: string;
  target_name: string;
  email?: string;
  account_id?: string;
  label: string;
  synthetic_id_token: boolean;
  warnings?: string[];
}

export interface ImportPreview {
  id: string;
  created_at: string;
  expires_at: string;
  input_type: "json" | "text" | "zip" | "mixed";
  source_files: number;
  total: number;
  skipped: number;
  warnings?: string[];
  items: ImportPreviewItem[];
  skipped_items?: ImportSkippedItem[];
}

export interface ImportResultItem {
  index: number;
  source_name: string;
  source_path?: string;
  target_name: string;
  email?: string;
  account_id?: string;
  label: string;
  status: "imported" | "skipped" | "failed";
  error?: string;
}

export interface ImportResult {
  id: string;
  state: "completed" | "partial" | "failed";
  total: number;
  imported: number;
  skipped: number;
  failed: number;
  started_at: string;
  finished_at: string;
  results: ImportResultItem[];
}

export type AccountExportFormat = "cpa" | "sub2api" | "cockpit" | "9router" | "codex" | "axonhub" | "codexmanager";

export type ResultExportFormat = "json" | "csv" | "jsonl";

export type ExportFormat = AccountExportFormat | ResultExportFormat;

export type OperationCategory = "account" | "batch" | "import" | "export" | "default_policy" | "inspection" | "update" | "journal";
export type OperationStatus = "running" | "succeeded" | "partial" | "failed" | "interrupted" | "warning" | "skipped";
export type OperationSource = "manual" | "background" | "default_policy" | "inspection" | "import" | "plugin_store";
export type OperationExportFormat = "json" | "csv" | "jsonl";

export interface OperationEntry {
  id: string;
  event_id?: string;
  category: OperationCategory;
  action: string;
  status: OperationStatus;
  source: OperationSource;
  scope?: string;
  target_id?: string;
  target_count: number;
  succeeded: number;
  failed: number;
  skipped: number;
  started_at: string;
  finished_at?: string;
  reason_code?: string;
  related_job_id?: string;
  related_action_id?: string;
  version?: string;
  format?: string;
  model?: string;
}

export interface OperationSummary {
  total: number;
  running: number;
  succeeded: number;
  failed: number;
  attention: number;
  interrupted: number;
}

export interface OperationListResponse {
  operations: OperationEntry[];
  summary: OperationSummary;
  total: number;
  page: number;
  page_size: number;
  pages: number;
  storage_error?: string;
}

export interface OperationFilters {
  category?: OperationCategory | "";
  status?: OperationStatus | "";
  source?: OperationSource | "";
  search?: string;
}

export interface Session {
  baseUrl: string;
  managementKey: string;
}

export type InspectionHealth = "healthy" | "quota_limited" | "invalid_credentials" | "deactivated" | "review" | "unavailable" | "disabled" | "unknown";

export interface InspectionPolicy {
  enabled: boolean;
  scan_interval_minutes: number;
  model_probe_enabled: boolean;
  model_probe_full_sweep: boolean;
  scan_manually_disabled: boolean;
  model_probe_interval_minutes: number;
  model_probe_batch_size: number;
  model_probe_models: {
    codex: string;
    openai: string;
    claude: string;
    gemini: string;
    xai: string;
  };
  failure_threshold: number;
  recovery_threshold: number;
  passive_circuit_enabled?: boolean;
  passive_failure_threshold?: number;
  passive_failure_window_minutes?: number;
  passive_circuit_minutes?: number;
  auto_disable: boolean;
  auto_enable: boolean;
  auto_delete: boolean;
  auto_delete_invalid_credentials: boolean;
  delete_grace_hours: number;
  delete_batch_size: number;
  anomaly_trigger_enabled: boolean;
  anomaly_threshold_percent: number;
  anomaly_minimum_accounts: number;
  anomaly_cooldown_minutes: number;
}

export interface InspectionRunSummary {
  started_at?: string;
  finished_at?: string;
  scanned: number;
  healthy: number;
  quota_limited: number;
  invalid_credentials: number;
  deactivated: number;
  review: number;
  unavailable: number;
  disabled: number;
  unknown: number;
  auto_disabled: number;
  auto_enabled: number;
  delete_pending: number;
  failed: number;
  truncated: number;
  error?: string;
}

export interface InspectionRunRecord {
  id: string;
  mode: "native" | "full" | "incremental" | "scoped" | "retry";
  source: "manual" | "scheduled" | "anomaly";
  status: "running" | "completed" | "failed" | "waiting_for_auth" | "stopped";
  phase?: "listing" | "primary" | "retry" | "stopped" | "completed";
  started_at: string;
  finished_at?: string;
  primary_total: number;
  primary_completed: number;
  retry_total: number;
  retry_completed: number;
  summary: InspectionRunSummary;
}

export interface InspectionSnapshot {
  policy: InspectionPolicy;
  running: boolean;
  pending: boolean;
  scan_started_at?: string;
  last_run: InspectionRunSummary;
  total: number;
  action_count: number;
  active_probe_armed: boolean;
  last_native_run_at?: string;
  last_probe_run_at?: string;
  probe_sweep_remaining: number;
  probe_sweep_total?: number;
  probe_sweep_completed?: number;
  probe_sweep_source?: "manual" | "scheduled" | "anomaly";
  probe_sweep_status?: "running" | "completed" | "failed" | "waiting_for_auth" | "stopped";
  probe_sweep_started_at?: string;
  anomaly_eligible: number;
  anomaly_count: number;
  anomaly_percent: number;
  anomaly_trigger_pending: boolean;
  last_anomaly_trigger_at?: string;
  storage_error?: string;
  run_mode?: "native" | "full" | "incremental" | "scoped" | "retry";
  probe_phase?: "listing" | "primary" | "retry" | "stopped" | "completed";
  retry_total?: number;
  retry_completed?: number;
  stop_requested?: boolean;
  recent_runs?: InspectionRunRecord[];
  revision?: number;
  active_run?: InspectionRunRecord;
  live_results?: InspectionResult[];
}

export interface InspectionRunRequest {
  mode: "full" | "incremental" | "scoped" | "retry";
  health?: InspectionHealth[];
  selected?: string[];
}

export interface InspectionResult {
  id: string;
  name?: string;
  provider?: string;
  type?: string;
  plan_type?: string;
  health: InspectionHealth;
  reason_code: string;
  confidence: "high" | "medium" | "low";
  recommendation: "keep" | "reauth" | "review" | "disable" | "enable" | "delete";
  disabled: boolean;
  editable: boolean;
  auto_disable_eligible: boolean;
  owned_disable: boolean;
  failure_streak: number;
  healthy_streak: number;
  last_checked_at: string;
  first_unhealthy_at?: string;
  last_failure_at?: string;
  last_success_at?: string;
  recover_after?: string;
  delete_eligible_at?: string;
  auto_action?: "disable" | "enable" | "delete" | "delete_candidate";
  probe_status?: "available" | "unavailable" | "review" | "unsupported";
  probe_reason_code?: string;
  probe_model?: string;
  probe_tested_at?: string;
  probe_latency_ms?: number;
  auto_action_status?: "pending" | "succeeded" | "failed" | "skipped";
  signal_source?: "native" | "passive" | "active_probe";
  status_code?: number;
  review_status?: "pending" | "resolved" | "ignored";
  reviewed_at?: string;
  circuit_open?: boolean;
  circuit_reason_code?: string;
  quota_window?: "five_hour" | "seven_day" | "multiple" | "five_hour_fallback";
  usage_total_tokens?: number;
  usage_last_request_at?: string;
  codex_usage?: CodexUsageSnapshot;
  run_id?: string;
  run_phase?: "listing" | "primary" | "retry" | "stopped" | "completed";
  run_observed_at?: string;
}

export interface InspectionResultList {
  results: InspectionResult[];
  summary: InspectionRemediationSummary;
  total: number;
  page: number;
  page_size: number;
  pages: number;
}

export interface InspectionRemediationSummary {
  actionable: number;
  suggested_delete: number;
  suggested_disable: number;
  suggested_enable: number;
  reauth: number;
  review: number;
  keep: number;
  editable_enabled: number;
  editable_disabled: number;
}

export interface InspectionAction {
  id: string;
  account_id: string;
  name?: string;
  provider?: string;
  action: "disable" | "enable" | "delete" | "delete_candidate" | "review_resolve" | "review_ignore" | "review_reopen";
  status: "pending" | "succeeded" | "failed" | "skipped";
  reason_code: string;
  created_at: string;
}

export interface InspectionDeleteRun {
  attempted: number;
  succeeded: number;
  failed: number;
  skipped: number;
  results?: Array<{ account_id: string; status: string; reason?: string }>;
}

export interface UpdatePolicy {
  check_enabled: boolean;
  check_interval_hours: number;
  auto_update: boolean;
}

export interface UpdateSnapshot {
  policy: UpdatePolicy;
  current_version: string;
  latest_version?: string;
  update_available: boolean;
  release_url?: string;
  checking: boolean;
  pending: boolean;
  checked_at?: string;
  error?: string;
  release_source?: "github" | "plugin_store" | "none";
  github_error?: string;
  store_error?: string;
}

export interface PluginStoreEntry {
  id: string;
  version: string;
  installed: boolean;
  installed_version: string;
  update_available: boolean;
}

export interface PluginStoreResponse {
  plugins_enabled: boolean;
  plugins: PluginStoreEntry[] | null;
}

export interface PluginInstallResult {
  status: "installed";
  id: string;
  version: string;
  restart_required: boolean;
}

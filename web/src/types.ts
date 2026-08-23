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
  created_at?: string;
  disabled_at?: string;
  updated_at?: string;
  last_refresh?: string;
  automation?: AccountAutomationSummary;
	model_policy?: AccountModelPolicySummary;
	concurrency?: AccountConcurrencySummary;
}

export interface AccountConcurrencyAvailability {
	supported: boolean;
	host_schema_version: number;
	required_schema_version: number;
	reason?: "host_schema_v2_required";
}

export interface AccountConcurrencySummary {
	supported: boolean;
	limit: number;
	active: number;
}

export type ModelPolicyMode = "all" | "allow_only" | "deny_only";

export interface AccountModelPolicySummary {
	mode: ModelPolicyMode;
	models?: string[];
	excluded_count: number;
}

export interface AccountEditableConfig {
	account_id: string;
	disabled: boolean;
	priority: number | null;
	note: string;
	prefix: string;
	proxy: string;
	proxy_configured: boolean;
	websockets: boolean | null;
	header_names: string[];
	model_policy: AccountModelPolicySummary | null;
	concurrency?: AccountConcurrencySummary;
	account_concurrency?: AccountConcurrencyAvailability;
}

export interface ModelPolicyPatch {
	mode: ModelPolicyMode;
	models?: string[];
}

export interface AccountModelOption {
	id: string;
	display_name?: string;
	type?: string;
	owned_by?: string;
}

export interface AccountModelCatalogResponse {
	models: AccountModelOption[];
	current_policy?: AccountModelPolicySummary;
	total: number;
	eligible: number;
	loaded: number;
	failed: number;
	read_only: number;
	missing: number;
	warnings?: string[];
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
  auto_disable_probe_name?: string;
  auto_disable_probe_status?: "pending" | "passed" | "failed" | "inconclusive";
  auto_disable_probe_attempts?: number;
  auto_disable_probe_limit?: number;
  auto_disable_probe_reason_code?: string;
  auto_disable_probe_model?: string;
  auto_disable_probe_tested_at?: string;
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
	overdraft_active?: boolean;
	overdraft_tokens?: number;
	overdraft_requests?: number;
	overdraft_amount_usd?: number;
	overdraft_rated_requests?: number;
	overdraft_unrated_requests?: number;
	overdraft_started_at?: string;
	overdraft_recover_at?: string;
}

export interface CodexUsageSnapshot {
  five_hour?: UsageWindowSnapshot;
  seven_day?: UsageWindowSnapshot;
	plan_type?: string;
	active_reset_count?: number;
	metadata_observed_at?: string;
  observed_at: string;
}

export interface QuotaMetadataResponse {
	account_id: string;
	plan_type?: string;
	active_reset_count?: number;
	observed_at: string;
	warning?: "active_reset_count_unavailable" | "quota_metadata_refresh_after_reset_unavailable";
	reset_credit_used?: boolean;
}

export interface AccountTokenRefreshResult {
	account_id: string;
	provider?: string;
	refresh_source: "cpa_native" | "plugin_codex";
	refreshed_at: string;
	expires_at?: string;
	refresh_token_rotated: boolean;
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
  credit?: CreditUsageSnapshot;
}

export interface CreditUsageSnapshot {
  amount_usd: number;
  rated_requests: number;
  unrated_requests: number;
  started_at?: string;
  pricing_updated_at?: string;
  pricing_source?: string;
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

export type AccountSortField = "account" | "provider" | "type" | "usage" | "active_reset_count" | "concurrency" | "created_at" | "disabled_at" | "access" | "status" | "priority" | "routing";
export type AccountSortOrder = "asc" | "desc";

export interface AccountSort {
  field: AccountSortField;
  order: AccountSortOrder;
}

export interface AccountListResponse {
  accounts: Account[];
  total: number;
  page: number;
  page_size: number;
  pages: number;
	account_concurrency?: AccountConcurrencyAvailability;
}

export type AccountDeduplicationMatch = "account_id" | "email" | "multiple";
export type AccountDeduplicationAction = "keep" | "delete" | "skip";

export interface AccountDeduplicationOptions {
  ignore_account_id: boolean;
  exclude_team_accounts: boolean;
}

export interface AccountDeduplicationMember {
  id: string;
  name?: string;
  email?: string;
  provider?: string;
  type?: string;
  plan_type?: string;
  status?: string;
  disabled: boolean;
  unavailable: boolean;
  editable: boolean;
  read_only_reason?: string;
  updated_at?: string;
  last_refresh?: string;
  recommended_action: AccountDeduplicationAction;
}

export interface AccountDeduplicationGroup {
  id: string;
  provider: string;
  matched_by: AccountDeduplicationMatch;
  identity_label: string;
  keep_id: string;
  keep_reason: "editable_physical_file" | "enabled_account" | "healthier_account" | "newer_evidence" | "more_complete_credential" | "deterministic_order";
  members: AccountDeduplicationMember[];
}

export interface AccountDeduplicationPreview {
  scanned_credentials: number;
  identified_credentials: number;
  excluded_credentials: number;
  duplicate_groups: number;
  duplicate_credentials: number;
  proposed_deletions: number;
  read_only_skipped: number;
  missing_identity: number;
  options: AccountDeduplicationOptions;
  groups: AccountDeduplicationGroup[];
}

export type ModelTestStatus = "available" | "unavailable" | "unsupported" | "review";

export interface ModelTestResult {
  account_id: string;
  provider: string;
  model: string;
  primary_model?: string;
  fallback_model?: string;
  selected_model?: string;
  fallback_used?: boolean;
  status: ModelTestStatus;
  probe_kind?: "model" | "credential";
  reason_code: string;
  status_code?: number;
  quota_window?: "five_hour" | "seven_day" | "multiple" | "five_hour_fallback";
  latency_ms: number;
  tested_at: string;
  response?: ModelTestResponsePreview;
  experiment?: ModelTestExperiment;
  attempts?: ModelTestAttempt[];
  compatible_models?: string[];
  model_policy?: {
    mode: "allow_only";
    models: string[];
    status: "applied" | "skipped" | "failed";
    reason_code: string;
  };
}

export interface ModelTestAttempt {
  model: string;
  role: "primary" | "fallback" | "compatibility";
  status: ModelTestStatus;
  probe_kind?: "model" | "credential";
  reason_code: string;
  status_code?: number;
  quota_window?: ModelTestResult["quota_window"];
  latency_ms: number;
  tested_at: string;
  response?: ModelTestResponsePreview;
  experiment?: ModelTestExperiment;
}

export interface ModelTestExperiment {
  name: "weekly_overdraft";
  applied: boolean;
  call_id?: string;
}

export interface ModelTestResponsePreview {
  format: "json" | "sse" | "text" | "empty";
  body: string;
  headers: ModelTestResponseHeader[];
  truncated: boolean;
}

export interface ModelTestResponseHeader {
  name: string;
  value: string;
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
	model_policy?: ModelPolicyPatch;
	concurrency_limit?: number;
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
  operation: "patch" | "delete";
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
  operation?: "patch" | "delete";
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
  new_account_model_probe_enabled: boolean;
  codex_quota_metadata_probe_enabled: boolean;
  apply_mode: "missing";
  scan_interval_seconds: number;
  priority: number | null;
  websockets: boolean | null;
  conditional_rules?: ConditionalPolicyRule[];
}

export type PolicyConditionField = "provider" | "account_type" | "email_suffix";
export type PolicyConditionOperator = "all" | "any";

export interface PolicyCondition {
  field: PolicyConditionField;
  value: string;
}

export interface PolicyConditionGroup {
  operator: PolicyConditionOperator;
  conditions?: PolicyCondition[];
  groups?: PolicyConditionGroup[];
}

export interface ConditionalPolicyActions {
  new_account_model_probe?: boolean;
  priority?: number;
  websockets?: boolean;
  model_policy?: ModelPolicyPatch;
}

export interface ConditionalPolicyRule {
  id: string;
  name: string;
  enabled: boolean;
  priority: number;
  conditions: PolicyConditionGroup;
  actions: ConditionalPolicyActions;
}

export interface PolicyScanSummary {
  started_at?: string;
  finished_at?: string;
  scanned: number;
  eligible: number;
  changed: number;
  skipped: number;
  failed: number;
  quota_metadata_probed?: number;
  quota_metadata_updated?: number;
  quota_metadata_failed?: number;
  error?: string;
}

export interface PolicySnapshot {
  policy: DefaultPolicy;
  running: boolean;
  scan_started_at?: string;
  last_scan: PolicyScanSummary;
  new_account_model_probe_storage_error?: string;
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
  credential_type?: "agent_identity" | "personal_access_token" | string;
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
  state: "idle" | "running" | "completed" | "partial" | "failed";
  running: boolean;
  total: number;
  imported: number;
  skipped: number;
  failed: number;
  started_at: string;
  finished_at: string;
  results: ImportResultItem[];
  error?: string;
  usage_collection_started?: boolean;
  usage_collection_targets?: number;
}

export type AccountExportFormat = "cpa" | "sub2api" | "cockpit" | "9router" | "codex" | "axonhub" | "codexmanager";

export type ResultExportFormat = "json" | "csv" | "jsonl";

export type ExportFormat = AccountExportFormat | ResultExportFormat;

export type OperationCategory = "account" | "batch" | "import" | "export" | "default_policy" | "inspection" | "update" | "journal" | "opencode";
export type OperationStatus = "running" | "succeeded" | "partial" | "failed" | "interrupted" | "warning" | "skipped";
export type OperationSource = "manual" | "background" | "default_policy" | "inspection" | "import" | "plugin_store";
export type OperationExportFormat = "json" | "csv" | "jsonl";

export interface OperationFailureDetail {
  reason_code: string;
  count: number;
  sample_account_ids?: string[];
}

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
  http_status?: number;
  attempts?: number;
  failure_details?: OperationFailureDetail[];
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
  extended_history: boolean;
  archived_segments: number;
  retention_limit: number;
  retained: number;
  storage_error?: string;
}

export interface OperationRetentionSettings {
  extended_history: boolean;
  page_size: number;
  retained: number;
  archived_segments: number;
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
  quota_recovery_priority_enabled?: boolean;
  auto_delete: boolean;
  auto_delete_invalid_credentials: boolean;
  delete_grace_hours: number;
  delete_batch_size: number;
  anomaly_trigger_enabled: boolean;
  anomaly_threshold_percent: number;
  anomaly_minimum_accounts: number;
  anomaly_cooldown_minutes: number;
  anomaly_notification_enabled: boolean;
  anomaly_notification_only: boolean;
  anomaly_notification_url: string;
  notification_endpoints?: InspectionNotificationEndpoint[];
  notification_policies?: InspectionNotificationPolicy[];
  notification_available_accounts_enabled: boolean;
  notification_available_accounts_threshold: number;
  notification_availability_percent_enabled: boolean;
  notification_availability_percent_threshold: number;
  notification_cooldown_minutes: number;
}

export interface InspectionNotificationEndpoint {
  id: string;
  name?: string;
  url: string;
  enabled: boolean;
  notification_policy_id?: string;
}

export interface InspectionNotificationPolicy {
  id: string;
  name: string;
  enabled: boolean;
  conditions: PolicyConditionGroup;
  threshold_operator: "all" | "any";
  available_accounts_enabled: boolean;
  available_accounts_below: number;
  availability_percent_enabled: boolean;
  availability_percent_below: number;
}

export type InspectionNotificationScenario = "manual_test" | "anomaly_threshold" | "available_accounts_low" | "availability_percent_low" | "combined";

export interface InspectionNotificationRequest {
  endpoint_id?: string;
  endpoint_name?: string;
  url_template: string;
  scenario: InspectionNotificationScenario;
  threshold_percent: number;
  available_accounts_threshold: number;
  availability_percent_threshold: number;
  notification_policy_id?: string;
}

export interface InspectionNotificationPreview {
  endpoint_id?: string;
  endpoint_name?: string;
  scenario: InspectionNotificationScenario;
  event: string;
  expanded_url: string;
  variables: Record<string, string>;
  triggered_at: string;
}

export interface InspectionNotificationTestResult {
  preview: InspectionNotificationPreview;
  delivered: boolean;
  status_code?: number;
  attempts: number;
  reason_code: string;
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
  last_notification_at?: string;
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
  probe_kind?: "model" | "credential";
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
  manual_delete_eligible: boolean;
  auto_disable_probe_name?: string;
  auto_disable_probe_status?: "pending" | "passed" | "failed" | "inconclusive";
  auto_disable_probe_attempts?: number;
  auto_disable_probe_limit?: number;
  auto_disable_probe_reason_code?: string;
  auto_disable_probe_model?: string;
  auto_disable_probe_tested_at?: string;
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
  deletable_reauth: number;
  review: number;
  keep: number;
  handled: number;
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
  source?: OperationSource;
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
  release_source?: "plugin_store" | "none";
  store_error?: string;
  runtime?: {
    active: boolean;
    superseded: boolean;
    instance_version: string;
    owner_version?: string;
    process_scope?: string;
    storage_error?: string;
    restart_required: boolean;
    restart_recommended: boolean;
  };
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

export interface CPAServerVersionSnapshot {
  current_version?: string;
  latest_version?: string;
  current_build_date?: string;
  update_available: boolean;
  checked_at: string;
  release_url?: string;
  error?: "current_version_unavailable" | "latest_version_unavailable" | "version_comparison_unavailable";
}

export interface ExperimentalSettings {
  weekly_overdraft_enabled: boolean;
  agent_identity_enabled: boolean;
  auto_model_whitelist_enabled: boolean;
  sub2api_credit_usage_enabled: boolean;
}

export interface ExperimentalSettingsSnapshot {
  settings: ExperimentalSettings;
  storage_error?: string;
}

export interface AgentIdentitySessionLoginResponse {
  status: "completed";
  account: {
    email?: string;
    plan_type: string;
    provider: string;
    login_state: string;
  };
}

export interface OpenCodeAccountView {
  id: string;
  workspace_id: string;
}

export interface OpenCodeWindowUsage {
  usage_percent: number;
  percent_remaining: number;
  reset_in_sec: number;
  reset_at: string;
}

export interface OpenCodeQuotaResult {
  success: boolean;
  account_id?: string;
  workspace?: string;
  rolling?: OpenCodeWindowUsage;
  weekly?: OpenCodeWindowUsage;
  monthly?: OpenCodeWindowUsage;
  source?: string;
  fetched_at?: string;
  error?: string;
}

export interface OpenCodeAccountSaveResponse {
  account: OpenCodeAccountView;
  result: OpenCodeQuotaResult;
}

export interface OpenCodeProbeResponse {
  result: OpenCodeQuotaResult;
}

export interface OpenCodeAccountsResponse {
  accounts: OpenCodeAccountView[];
}

export type AIProviderChannelKind =
  | "openai-compatibility"
  | "gemini-api-key"
  | "interactions-api-key"
  | "claude-api-key"
  | "codex-api-key"
  | "xai-api-key"
  | "vertex-api-key"
  | "api-keys";

export interface AIProviderChannelEntry {
  index: number;
  name?: string;
  api_key?: string;
  base_url?: string;
  proxy_url?: string;
  prefix?: string;
  priority?: number;
  disabled?: boolean;
  weight?: number | null;
  headers?: Record<string, string>;
  models?: Array<Record<string, unknown>>;
  excluded_models?: string[];
  auth_index?: string;
}

export interface AIProviderChannelSnapshot {
  kind: AIProviderChannelKind;
  count: number;
  entries: AIProviderChannelEntry[];
}


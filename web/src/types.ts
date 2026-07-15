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

export interface Session {
  baseUrl: string;
  managementKey: string;
}

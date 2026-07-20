import {
  AlertTriangle,
  CheckCircle2,
  ChevronLeft,
  ChevronRight,
  CircleDot,
  Download,
  Eye,
  FileJson2,
  Link2,
  LoaderCircle,
  RefreshCw,
  Search,
  ScrollText,
  Trash2,
  X,
  XCircle,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import * as api from "../api/client";
import { operatorMessage } from "../format/operatorMessage";
import type {
  OperationCategory,
  OperationEntry,
  OperationExportFormat,
  OperationFilters,
  OperationListResponse,
  OperationSource,
  OperationStatus,
} from "../types";
import { IconButton } from "./IconButton";
import { Modal } from "./Modal";
import { useI18n } from "../i18n";
import type { Locale } from "../i18n";
import { translateUI, type UIMessageKey } from "../i18n/uiText";

interface OperationLogWorkspaceProps {
  activeJobIDs: string[];
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
  onOpenRelatedJob: (operation: OperationEntry) => void;
}

const emptyResponse: OperationListResponse = {
  operations: [],
  summary: { total: 0, running: 0, succeeded: 0, failed: 0, attention: 0, interrupted: 0 },
  total: 0,
  page: 1,
  page_size: 50,
  pages: 0,
};

const pageSizeOptions = [20, 50, 100, 200] as const;

const categoryLabels: Record<OperationCategory, UIMessageKey> = {
  account: "ui.accounts",
  batch: "ui.batch",
  import: "ui.import",
  export: "ui.export",
  default_policy: "ui.default_policy",
  inspection: "ui.inspection_automation",
  update: "ui.plugin_updates",
  journal: "ui.log_management",
};

const statusLabels: Record<OperationStatus, UIMessageKey> = {
  running: "ui.running_2",
  succeeded: "ui.succeeded",
  partial: "ui.partially_completed",
  failed: "ui.failed",
  interrupted: "ui.interrupted_2",
  warning: "ui.attention",
  skipped: "ui.skipped_2",
};

const sourceLabels: Record<OperationSource, UIMessageKey> = {
  manual: "ui.manual_2",
  background: "ui.background",
  default_policy: "ui.default_policy",
  inspection: "ui.inspection",
  import: "ui.import",
  plugin_store: "ui.plugin_store",
};

const actionLabels: Record<string, UIMessageKey> = {
  delete: "ui.delete_account",
  model_test: "ui.model_availability_test",
  batch_edit: "ui.batch_edit_2",
  batch_retry: "ui.retry_failures",
  import: "ui.import_accounts",
  export_accounts: "ui.export_accounts",
  export_results: "ui.export_job_results",
  policy_save: "ui.save_default_policy",
  policy_scan: "ui.default_policy_scan",
  force_sync: "ui.force_sync_policy",
  inspection_policy_save: "ui.save_inspection_policy",
  inspection_scan: "ui.account_inspection",
  auto_disable: "ui.auto_disable",
  auto_enable: "ui.auto_enable",
  delete_candidate: "ui.add_deletion_candidate",
  auto_delete: "ui.auto_delete",
  update_policy_save: "ui.save_update_policy",
  update_check: "ui.check_plugin_updates",
  update_install: "ui.install_plugin_update",
  journal_clear: "ui.clear_operation_log",
};

const reasonLabels: Record<string, UIMessageKey> = {
  completed: "ui.completed_2",
  partial_failure: "ui.some_operations_failed",
  operation_failed: "ui.operation_failed",
  interrupted: "ui.operation_interrupted",
  conflict: "ui.account_state_conflict",
  restart_required: "ui.cpa_restart_required",
  install_failed: "ui.update_installation_failed",
  update_available: "ui.update_available_2",
  up_to_date: "ui.up_to_date_2",
  check_failed: "ui.update_check_failed",
  healthy_recent_success: "ui.recent_requests_are_healthy",
  quota_exhausted: "ui.account_quota_exhausted",
  token_revoked: "ui.access_credential_revoked",
  invalid_credentials: "ui.invalid_credentials_2",
  account_deactivated: "ui.account_deactivated",
  workspace_deactivated: "ui.workspace_deactivated_2",
  authentication_review: "ui.authentication_needs_review_2",
  billing_review: "ui.billing_needs_review",
  credential_permission_denied: "ui.credential_permission_denied_2",
  native_unavailable: "ui.cpa_native_status_unavailable",
  manual_disabled: "ui.account_manually_disabled",
  transient_failure: "ui.temporary_upstream_failure_2",
  no_recent_evidence: "ui.no_recent_evidence",
  mutation_busy: "ui.another_account_change_is_running",
  account_changed: "ui.account_state_changed",
  account_missing: "ui.account_missing",
  account_read_only: "ui.account_is_read_only",
  management_unavailable: "ui.cpa_management_api_unavailable",
  delete_failed: "ui.account_deletion_failed",
  model_response_ok: "ui.model_response_is_healthy",
  model_not_found: "ui.model_unavailable_or_missing",
  account_unavailable: "ui.account_is_currently_unavailable",
  authentication_failed: "ui.authentication_failed",
  quota_limited: "ui.upstream_quota_or_rate_limited_2",
  request_timeout: "ui.model_test_timed_out",
  upstream_unavailable: "ui.upstream_service_unavailable",
  invalid_response: "ui.could_not_validate_upstream_response",
  unsupported_provider: "ui.provider_unsupported",
};

const scopeLabels: Record<string, UIMessageKey> = {
  single: "ui.single_account",
  selected: "ui.selected_accounts_2",
  filtered: "ui.filtered_results",
  all: "ui.all_accounts",
  scheduled: "ui.scheduled_job",
  system: "ui.system",
};

const operationExportOptions: Array<{ format: OperationExportFormat; label: UIMessageKey; extension: string }> = [
  { format: "json", label: "ui.json_structured", extension: ".json" },
  { format: "csv", label: "ui.csv_table", extension: ".csv" },
  { format: "jsonl", label: "ui.json_lines_line_delimited", extension: ".jsonl" },
];

export function OperationLogWorkspace({ activeJobIDs, onAPIError, onNotice, onOpenRelatedJob }: OperationLogWorkspaceProps) {
  const { locale, tx, formatDateTime } = useI18n();
  const [data, setData] = useState<OperationListResponse>(emptyResponse);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(50);
  const [category, setCategory] = useState<OperationCategory | "">("");
  const [status, setStatus] = useState<OperationStatus | "">("");
  const [source, setSource] = useState<OperationSource | "">("");
  const [searchDraft, setSearchDraft] = useState("");
  const [search, setSearch] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [detail, setDetail] = useState<OperationEntry | null>(null);
  const [exportOpen, setExportOpen] = useState(false);
  const [exportFormat, setExportFormat] = useState<OperationExportFormat>("json");
  const [exporting, setExporting] = useState(false);
  const [clearOpen, setClearOpen] = useState(false);
  const [clearConfirmed, setClearConfirmed] = useState(false);
  const [clearing, setClearing] = useState(false);

  const filters = useMemo<OperationFilters>(() => ({ category, status, source, search }), [category, search, source, status]);

  const handleError = useCallback((caught: unknown) => {
    if (caught instanceof api.APIError && caught.status === 401) {
      onAPIError(caught);
      return;
    }
    setError(operatorMessage(caught instanceof Error ? caught.message : tx("ui.operation_log_request_failed"), locale));
  }, [locale, onAPIError]);

  const refresh = useCallback(async (quiet = false) => {
    if (!quiet) setLoading(true);
    try {
      const next = await api.listOperations(page, pageSize, filters);
      setData(next);
      setError("");
      if (next.pages > 0 && page > next.pages) setPage(next.pages);
    } catch (caught) {
      handleError(caught);
    } finally {
      if (!quiet) setLoading(false);
    }
  }, [filters, handleError, page, pageSize]);

  useEffect(() => {
    const timer = window.setTimeout(() => {
      setSearch(searchDraft.trim());
      setPage(1);
    }, 250);
    return () => window.clearTimeout(timer);
  }, [searchDraft]);

  useEffect(() => {
    void refresh();
    const timer = window.setInterval(() => void refresh(true), 5_000);
    return () => window.clearInterval(timer);
  }, [refresh]);

  const download = async () => {
    setExporting(true);
    setError("");
    try {
      const result = await api.downloadOperationExport(exportFormat, filters);
      setExportOpen(false);
      onNotice(tx("ui.operation_log_exported_as_file", { file: result.filename }));
    } catch (caught) {
      handleError(caught);
    } finally {
      setExporting(false);
    }
  };

  const clear = async () => {
    if (!clearConfirmed) return;
    setClearing(true);
    setError("");
    try {
      await api.clearOperations();
      setClearOpen(false);
      setClearConfirmed(false);
      setPage(1);
      await refresh();
      onNotice(tx("ui.operation_log_cleared_this_cleanup_event_was_retained"));
    } catch (caught) {
      handleError(caught);
    } finally {
      setClearing(false);
    }
  };

  const hasFilters = Boolean(category || status || source || searchDraft || search);
  return (
    <section className="operation-panel" aria-label={tx("ui.operation_log")}>
      <header className="operation-toolbar">
        <div className="operation-title">
          <span className="operation-title-icon"><ScrollText size={18} /></span>
          <div><strong>{tx("ui.account_manager_operation_log")}</strong><span>{data.storage_error ? tx("ui.storage_state_error") : tx("ui.retaining_the_latest_count_audit_records", { count: Math.max(data.summary.total, data.total) })}</span></div>
        </div>
        <div className="operation-toolbar-actions">
          <button className="button" type="button" onClick={() => setExportOpen(true)}><Download size={16} />{tx("ui.export")}</button>
          <IconButton label={tx("ui.refresh_operation_log")} disabled={loading} onClick={() => void refresh()}><RefreshCw className={loading ? "spin" : ""} size={17} /></IconButton>
          <IconButton className="operation-clear-button" label={tx("ui.clear_operation_log")} onClick={() => { setClearConfirmed(false); setClearOpen(true); }}><Trash2 size={17} /></IconButton>
        </div>
      </header>

      {error || data.storage_error ? (
        <div className="operation-error" role="alert"><AlertTriangle size={16} /><span>{error || operatorMessage(data.storage_error, locale)}</span><IconButton label={tx("ui.dismiss_log_message")} onClick={() => setError("")}><X size={14} /></IconButton></div>
      ) : null}

      <div className="operation-metrics" aria-label={tx("ui.operation_log_metrics")}>
        <OperationMetric label={tx("ui.current_results")} value={data.summary.total} icon={<ScrollText size={14} />} />
        <OperationMetric label={tx("ui.running_2")} value={data.summary.running} tone="running" icon={<CircleDot size={14} />} />
        <OperationMetric label={tx("ui.succeeded")} value={data.summary.succeeded} tone="success" icon={<CheckCircle2 size={14} />} />
        <OperationMetric label={tx("ui.failed")} value={data.summary.failed} tone="danger" icon={<XCircle size={14} />} />
        <OperationMetric label={tx("ui.attention")} value={data.summary.attention + data.summary.interrupted} tone="warning" icon={<AlertTriangle size={14} />} />
      </div>

      <div className="operation-filters">
        <label className="search-box operation-search">
          <Search size={16} />
          <input aria-label={tx("ui.search_operation_log")} value={searchDraft} onChange={(event) => setSearchDraft(event.target.value)} placeholder={tx("ui.action_account_job_id_or_reason")} />
          {searchDraft ? <button type="button" aria-label={tx("ui.clear_log_search")} onClick={() => setSearchDraft("")}><X size={14} /></button> : null}
        </label>
        <select aria-label={tx("ui.operation_category")} value={category} onChange={(event) => { setCategory(event.target.value as OperationCategory | ""); setPage(1); }}>
          <option value="">{tx("ui.all_categories")}</option>
          {Object.keys(categoryLabels).map((value) => <option key={value} value={value}>{categoryLabel(value as OperationCategory, locale)}</option>)}
        </select>
        <select aria-label={tx("ui.operation_status")} value={status} onChange={(event) => { setStatus(event.target.value as OperationStatus | ""); setPage(1); }}>
          <option value="">{tx("ui.all_statuses")}</option>
          {Object.keys(statusLabels).map((value) => <option key={value} value={value}>{statusLabel(value as OperationStatus, locale)}</option>)}
        </select>
        <select aria-label={tx("ui.operation_source")} value={source} onChange={(event) => { setSource(event.target.value as OperationSource | ""); setPage(1); }}>
          <option value="">{tx("ui.all_sources")}</option>
          {Object.keys(sourceLabels).map((value) => <option key={value} value={value}>{sourceLabel(value as OperationSource, locale)}</option>)}
        </select>
        <button className="button button-quiet" type="button" disabled={!hasFilters} onClick={() => { setCategory(""); setStatus(""); setSource(""); setSearchDraft(""); setSearch(""); setPage(1); }}>{tx("ui.reset")}</button>
      </div>

      <div className="operation-table-scroll">
        <table className="operation-table">
          <thead><tr><th>{tx("ui.status")}</th><th>{tx("ui.actions")}</th><th>{tx("ui.source")}</th><th>{tx("ui.results")}</th><th>{tx("ui.related_object")}</th><th>{tx("ui.time")}</th><th aria-label={tx("ui.operation_details")} /></tr></thead>
          <tbody>
            {loading ? <OperationLoadingRows /> : data.operations.map((operation) => {
              const canOpenJob = Boolean(operation.related_job_id && activeJobIDs.includes(operation.related_job_id));
              return (
                <tr key={operation.id}>
                  <td><OperationStatusBadge status={operation.status} /></td>
                  <td><div className="operation-name"><strong>{actionLabelForOperation(operation.action, locale)}</strong><span>{categoryLabel(operation.category, locale)}{operation.model ? ` · ${operation.model}` : ""}{operation.format ? ` · ${operation.format.toUpperCase()}` : ""}{operation.version ? ` · v${operation.version}` : ""}</span></div></td>
                  <td><span className={`operation-source source-${operation.source}`}>{sourceLabel(operation.source, locale)}</span></td>
                  <td><OperationCounts operation={operation} /></td>
                  <td><div className="operation-target"><code>{operation.target_id || operation.related_job_id || "-"}</code><span>{scopeLabel(operation.scope, locale)}</span></div></td>
                  <td><time>{formatDateTime(operation.finished_at || operation.started_at)}</time></td>
                  <td><div className="operation-row-actions">{canOpenJob ? <IconButton label={tx("ui.open_related_job")} onClick={() => onOpenRelatedJob(operation)}><Link2 size={15} /></IconButton> : null}<IconButton label={tx("ui.view_operation_details")} onClick={() => setDetail(operation)}><Eye size={15} /></IconButton></div></td>
                </tr>
              );
            })}
          </tbody>
        </table>
        {!loading && data.operations.length === 0 ? <div className="empty-state">{tx("ui.no_matching_operation_records")}</div> : null}
      </div>

      <div className="pagination operation-pagination">
        <label className="page-size-control"><span>{tx("ui.per_page")}</span><select aria-label={tx("ui.operation_logs_per_page")} value={pageSize} onChange={(event) => { setPageSize(Number(event.target.value)); setPage(1); }}>{pageSizeOptions.map((value) => <option key={value} value={value}>{value}</option>)}</select></label>
        <span>{tx("ui.page_page_slash_pages_count_records", { page: data.page || 1, pages: data.pages || 1, count: data.total })}</span>
        <IconButton label={tx("ui.previous_log_page")} disabled={page <= 1} onClick={() => setPage((current) => Math.max(1, current - 1))}><ChevronLeft size={17} /></IconButton>
        <strong>{page}</strong>
        <IconButton label={tx("ui.next_log_page")} disabled={data.pages === 0 || page >= data.pages} onClick={() => setPage((current) => current + 1)}><ChevronRight size={17} /></IconButton>
      </div>

      {detail ? <OperationDetailsDialog operation={detail} canOpenJob={Boolean(detail.related_job_id && activeJobIDs.includes(detail.related_job_id))} onClose={() => setDetail(null)} onOpenJob={() => { onOpenRelatedJob(detail); setDetail(null); }} /> : null}
      {exportOpen ? <OperationExportDialog format={exportFormat} count={data.total} exporting={exporting} onChange={setExportFormat} onClose={() => setExportOpen(false)} onExport={() => void download()} /> : null}
      {clearOpen ? <Modal title={tx("ui.clear_operation_log")} onClose={() => setClearOpen(false)} footer={<><button className="button" type="button" onClick={() => setClearOpen(false)}>{tx("ui.cancel")}</button><button className="button button-danger" type="button" disabled={!clearConfirmed || clearing} onClick={() => void clear()}>{clearing ? <LoaderCircle className="spin" size={15} /> : <Trash2 size={15} />}{tx("ui.confirm_clear")}</button></>}><label className="operation-clear-confirm"><input type="checkbox" checked={clearConfirmed} onChange={(event) => setClearConfirmed(event.target.checked)} aria-label={tx("ui.clear_the_current_count_operation_records", { count: data.total })} /><AlertTriangle size={18} /><span><strong>{tx("ui.clear_the_current_count_operation_records", { count: data.total })}</strong><small>{tx("ui.only_this_cleanup_event_will_remain_afterward")}</small></span></label></Modal> : null}
    </section>
  );
}

function OperationMetric({ label, value, tone = "", icon }: { label: string; value: number; tone?: string; icon: ReactNode }) {
  return <div className={tone ? `tone-${tone}` : ""}><span>{icon}{label}</span><strong>{value}</strong></div>;
}

function OperationStatusBadge({ status }: { status: OperationStatus }) {
  const { locale } = useI18n();
  return <span className={`operation-status status-${status}`}>{status === "running" ? <LoaderCircle className="spin" size={12} /> : null}{statusLabel(status, locale)}</span>;
}

function OperationCounts({ operation }: { operation: OperationEntry }) {
  const { tx } = useI18n();
  return <div className="operation-counts"><strong>{operation.succeeded}</strong><span>/</span><b>{operation.failed}</b><span>/</span><em>{operation.skipped}</em><small>{tx("ui.succeeded_slash_failed_slash_skipped")}</small></div>;
}

function OperationDetailsDialog({ operation, canOpenJob, onClose, onOpenJob }: { operation: OperationEntry; canOpenJob: boolean; onClose: () => void; onOpenJob: () => void }) {
  const { locale, tx, formatDateTime } = useI18n();
  const fields: Array<[string, string | number | undefined]> = [
    [tx("ui.operation_id"), operation.id],
    [tx("ui.category"), categoryLabel(operation.category, locale)],
    [tx("ui.action"), actionLabelForOperation(operation.action, locale)],
    [tx("ui.status"), statusLabel(operation.status, locale)],
    [tx("ui.source"), sourceLabel(operation.source, locale)],
    [tx("ui.scope"), scopeLabel(operation.scope, locale)],
    [tx("ui.target_id"), operation.target_id],
    [tx("ui.target_count"), operation.target_count],
    [tx("ui.succeeded_slash_failed_slash_skipped"), `${operation.succeeded} / ${operation.failed} / ${operation.skipped}`],
    [tx("ui.result"), operation.reason_code ? reasonLabelForOperation(operation.reason_code, locale) : undefined],
    [tx("ui.related_job"), operation.related_job_id],
    [tx("ui.related_automation_action"), operation.related_action_id],
    [tx("ui.version"), operation.version ? `v${operation.version}` : undefined],
    [tx("ui.format"), operation.format?.toUpperCase()],
    [tx("ui.model"), operation.model],
    [tx("ui.started"), formatDateTime(operation.started_at)],
    [tx("ui.completed_3"), formatDateTime(operation.finished_at)],
  ];
  return <Modal title={tx("ui.operation_details")} wide onClose={onClose} footer={<>{canOpenJob ? <button className="button" type="button" onClick={onOpenJob}><Link2 size={15} />{tx("ui.open_related_job")}</button> : null}<button className="button button-primary" type="button" onClick={onClose}>{tx("ui.completed")}</button></>}><div className="operation-detail-grid">{fields.filter(([, value]) => value !== undefined && value !== "").map(([label, value]) => <div key={label}><span>{label}</span><code>{value}</code></div>)}</div></Modal>;
}

function OperationExportDialog({ format, count, exporting, onChange, onClose, onExport }: { format: OperationExportFormat; count: number; exporting: boolean; onChange: (format: OperationExportFormat) => void; onClose: () => void; onExport: () => void }) {
  const { tx } = useI18n();
  return <Modal title={tx("ui.export_operation_log")} onClose={onClose} footer={<><button className="button" type="button" onClick={onClose}>{tx("ui.cancel")}</button><button className="button button-primary" type="button" disabled={exporting} onClick={onExport}>{exporting ? <LoaderCircle className="spin" size={15} /> : <Download size={15} />}{tx("ui.export")} {format === "jsonl" ? "JSON Lines" : format.toUpperCase()}</button></>}><div className="operation-export"><div className="operation-export-summary"><ScrollText size={20} /><div><strong>{count}</strong><span>{tx("ui.filtered_redacted_records")}</span></div></div><div className="operation-export-options">{operationExportOptions.map((option) => {
    const label = tx(option.label);
    return <label key={option.format} className={format === option.format ? "is-selected" : ""}><input type="radio" name="operation-export-format" checked={format === option.format} onChange={() => onChange(option.format)} aria-label={`${label} ${option.extension}`} /><FileJson2 size={18} /><span><strong>{label}</strong><small>{option.extension}</small></span></label>;
  })}</div></div></Modal>;
}

function categoryLabel(value: OperationCategory, locale: Locale): string {
  return translateUI(locale, categoryLabels[value]);
}

function statusLabel(value: OperationStatus, locale: Locale): string {
  return translateUI(locale, statusLabels[value]);
}

function sourceLabel(value: OperationSource, locale: Locale): string {
  return translateUI(locale, sourceLabels[value]);
}

function actionLabelForOperation(value: string, locale: Locale): string {
  return translateUI(locale, actionLabels[value] || "ui.unknown_action");
}

function scopeLabel(value: string | undefined, locale: Locale): string {
  const key = value || "";
  return translateUI(locale, scopeLabels[key] || "ui.other_scope");
}

function reasonLabelForOperation(value: string, locale: Locale): string {
  return translateUI(locale, reasonLabels[value] || "ui.other_reason");
}

function OperationLoadingRows() {
  return <>{Array.from({ length: 7 }, (_, index) => <tr className="operation-loading-row" key={index}><td colSpan={7}><span /></td></tr>)}</>;
}

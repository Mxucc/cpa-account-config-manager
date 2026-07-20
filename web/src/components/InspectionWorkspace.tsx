import {
  Activity,
  AlertTriangle,
  CheckCircle2,
  ChevronLeft,
  ChevronRight,
  Clock3,
  ExternalLink,
  LoaderCircle,
  RefreshCw,
  Search,
  Settings2,
  ShieldAlert,
  ShieldCheck,
  Trash2,
  UploadCloud,
  X,
} from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import * as api from "../api/client";
import { operatorMessage } from "../format/operatorMessage";
import type {
  InspectionAction,
  InspectionHealth,
  InspectionPolicy,
  InspectionResult,
  InspectionResultList,
  InspectionSnapshot,
  UpdatePolicy,
  UpdateSnapshot,
} from "../types";
import { AutomationSettingsDialog } from "./AutomationSettingsDialog";
import { IconButton } from "./IconButton";
import { useI18n } from "../i18n";
import type { Locale } from "../i18n";
import { translateUI, type UIMessageKey } from "../i18n/uiText";

interface InspectionWorkspaceProps {
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
}

const emptyResults: InspectionResultList = { results: [], total: 0, page: 1, page_size: 50, pages: 0 };

const healthOptions: Array<{ value: "" | InspectionHealth; label: UIMessageKey }> = [
  { value: "", label: "ui.all_health_states" },
  { value: "deactivated", label: "ui.deactivated" },
  { value: "invalid_credentials", label: "ui.invalid_credentials" },
  { value: "quota_limited", label: "ui.quota_limited" },
  { value: "review", label: "ui.needs_review" },
  { value: "unavailable", label: "ui.temporarily_unavailable" },
  { value: "disabled", label: "ui.manually_disabled" },
  { value: "unknown", label: "ui.insufficient_evidence" },
  { value: "healthy", label: "ui.healthy" },
];

export function InspectionWorkspace({ onAPIError, onNotice }: InspectionWorkspaceProps) {
  const { locale, tx, formatDateTime } = useI18n();
  const [snapshot, setSnapshot] = useState<InspectionSnapshot | null>(null);
  const [results, setResults] = useState<InspectionResultList>(emptyResults);
  const [actions, setActions] = useState<InspectionAction[]>([]);
  const [updates, setUpdates] = useState<UpdateSnapshot | null>(null);
  const [page, setPage] = useState(1);
  const [health, setHealth] = useState<"" | InspectionHealth>("");
  const [searchDraft, setSearchDraft] = useState("");
  const [search, setSearch] = useState("");
  const [loading, setLoading] = useState(true);
  const [scanning, setScanning] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [settingsSaving, setSettingsSaving] = useState(false);
  const [settingsError, setSettingsError] = useState("");
  const [updateChecking, setUpdateChecking] = useState(false);
  const [installing, setInstalling] = useState(false);
  const [autoDeleting, setAutoDeleting] = useState(false);
  const [error, setError] = useState("");
  const attemptedUpdate = useRef("");
  const autoDeleteBusy = useRef(false);

  const handleError = useCallback((caught: unknown, target: "page" | "settings" = "page") => {
    if (caught instanceof api.APIError && caught.status === 401) {
      onAPIError(caught);
      return;
    }
    const message = operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale);
    if (target === "settings") setSettingsError(message);
    else setError(message);
  }, [locale, onAPIError]);

  const refreshOverview = useCallback(async () => {
    try {
      const [nextSnapshot, nextActions, nextUpdates] = await Promise.all([
        api.getInspection(),
        api.listInspectionActions(50),
        api.getUpdateStatus(),
      ]);
      setSnapshot(nextSnapshot);
      setActions(nextActions);
      setUpdates(nextUpdates);
      if (nextUpdates.policy.check_enabled && !nextUpdates.checked_at && !nextUpdates.checking && !nextUpdates.pending) {
        setUpdates(await api.checkForUpdates());
      }
    } catch (caught) {
      handleError(caught);
    }
  }, [handleError]);

  const refreshResults = useCallback(async () => {
    try {
      const next = await api.listInspectionResults(page, 50, health, search);
      setResults(next);
      if (next.pages > 0 && page > next.pages) setPage(next.pages);
    } catch (caught) {
      handleError(caught);
    }
  }, [handleError, health, page, search]);

  useEffect(() => {
    const timer = window.setTimeout(() => {
      setSearch(searchDraft.trim());
      setPage(1);
    }, 250);
    return () => window.clearTimeout(timer);
  }, [searchDraft]);

  useEffect(() => {
    let active = true;
    const load = async () => {
      setLoading(true);
      await Promise.all([refreshOverview(), refreshResults()]);
      if (active) setLoading(false);
    };
    void load();
    return () => { active = false; };
  }, [refreshOverview, refreshResults]);

  useEffect(() => {
    if (!snapshot?.running && !snapshot?.pending && !updates?.checking && !updates?.pending) return;
    const timer = window.setTimeout(async () => {
      await refreshOverview();
      if (snapshot?.running || snapshot?.pending) await refreshResults();
    }, 1200);
    return () => window.clearTimeout(timer);
  }, [refreshOverview, refreshResults, snapshot?.pending, snapshot?.running, updates?.checking, updates?.pending]);

  const runAutoDelete = useCallback(async () => {
    if (!snapshot?.policy.auto_delete || autoDeleteBusy.current) return;
    autoDeleteBusy.current = true;
    setAutoDeleting(true);
    try {
      const run = await api.executeInspectionAutoDelete();
      if (run.succeeded > 0) {
        onNotice(tx("ui.inspection_auto_deleted_count_expired_accounts", { count: run.succeeded }));
        await Promise.all([refreshOverview(), refreshResults()]);
      } else if (run.failed > 0) {
        setError(tx("ui.count_auto_delete_operations_failed_and_will_retry_later", { count: run.failed }));
      }
    } catch (caught) {
      handleError(caught);
    } finally {
      autoDeleteBusy.current = false;
      setAutoDeleting(false);
    }
  }, [handleError, locale, onNotice, refreshOverview, refreshResults, snapshot?.policy.auto_delete]);

  useEffect(() => {
    if (!snapshot?.policy.auto_delete) return;
    void runAutoDelete();
    const timer = window.setInterval(() => void runAutoDelete(), 60_000);
    return () => window.clearInterval(timer);
  }, [runAutoDelete, snapshot?.policy.auto_delete]);

  const installUpdate = useCallback(async (automatic = false) => {
    const version = updates?.latest_version;
    if (!version || installing) return;
    setInstalling(true);
    setError("");
    try {
      const result = await api.installPluginUpdate(version);
      attemptedUpdate.current = version;
      setUpdates((current) => current ? { ...current, current_version: result.version, update_available: false } : current);
      onNotice(result.restart_required
        ? tx("ui.plugin_version_installed_restart_cpa_to_activate_it", { version: result.version })
        : tx("ui.plugin_version_installed_refresh_to_use_the_new_version", { version: result.version }));
    } catch (caught) {
      attemptedUpdate.current = version;
      handleError(caught);
      if (automatic) setError(tx("ui.auto_update_did_not_complete_retry_it_from_update_status"));
    } finally {
      setInstalling(false);
    }
  }, [handleError, installing, locale, onNotice, updates?.latest_version]);

  useEffect(() => {
    if (!updates?.policy.auto_update || !updates.update_available || !updates.latest_version || attemptedUpdate.current === updates.latest_version) return;
    attemptedUpdate.current = updates.latest_version;
    void installUpdate(true);
  }, [installUpdate, updates]);

  const runScan = async () => {
    setScanning(true);
    setError("");
    try {
      setSnapshot(await api.scanInspection());
    } catch (caught) {
      handleError(caught);
    } finally {
      setScanning(false);
    }
  };

  const checkUpdates = async () => {
    setUpdateChecking(true);
    setError("");
    try {
      setUpdates(await api.checkForUpdates());
    } catch (caught) {
      handleError(caught);
    } finally {
      setUpdateChecking(false);
    }
  };

  const saveSettings = async (inspection: InspectionPolicy, updatePolicy: UpdatePolicy, confirmDelete: boolean, confirmDeleteInvalid: boolean, confirmUpdate: boolean) => {
    setSettingsSaving(true);
    setSettingsError("");
    try {
      const nextInspection = await api.saveInspectionPolicy(inspection, confirmDelete, confirmDeleteInvalid);
      const nextUpdates = await api.saveUpdatePolicy(updatePolicy, confirmUpdate);
      setSnapshot(nextInspection);
      setUpdates(nextUpdates);
      setSettingsOpen(false);
      onNotice(tx("ui.inspection_and_update_settings_saved"));
    } catch (caught) {
      handleError(caught, "settings");
    } finally {
      setSettingsSaving(false);
    }
  };

  const lastRun = snapshot?.last_run;
  return (
    <section className="automation-panel" aria-label={tx("ui.inspection_and_automation")}>
      <header className="automation-toolbar">
        <div className="automation-title">
          <div className="automation-sources">
            <span className={`automation-live ${snapshot?.policy.enabled ? "is-on" : ""}`}><span />{tx(snapshot?.policy.enabled ? "ui.scheduled_inspection" : "ui.manual")}</span>
            {snapshot?.policy.model_probe_enabled ? <span className={`automation-live ${snapshot.active_probe_armed ? "is-on" : "is-waiting"}`}><span />{tx(snapshot.active_probe_armed ? "ui.active_probe_ready" : "ui.active_probe_waiting_for_auth")}</span> : null}
            {snapshot?.anomaly_trigger_pending || (snapshot?.probe_sweep_remaining ?? 0) > 0 ? <span className="automation-live is-waiting"><span />{tx("ui.full_active_inspection_queued")}</span> : null}
          </div>
          <div><strong>{tx("ui.account_health_inspection")}</strong><span>{snapshot?.running || snapshot?.pending ? tx("ui.reading_cpa_status") : tx("ui.last_completed_time", { time: formatDateTime(lastRun?.finished_at) })}</span></div>
        </div>
        <div className="automation-toolbar-actions">
          <button className="button" type="button" disabled={scanning || snapshot?.running || snapshot?.pending} onClick={() => void runScan()}>
            {scanning || snapshot?.running || snapshot?.pending ? <LoaderCircle className="spin" size={16} /> : <Activity size={16} />}{tx("ui.inspect_now")}
          </button>
          <IconButton label={tx("ui.refresh_inspection")} disabled={loading} onClick={() => void Promise.all([refreshOverview(), refreshResults()])}><RefreshCw className={loading ? "spin" : ""} size={17} /></IconButton>
          <IconButton label={tx("ui.inspection_and_automation_settings")} disabled={!snapshot || !updates} onClick={() => { setSettingsError(""); setSettingsOpen(true); }}><Settings2 size={17} /></IconButton>
        </div>
      </header>

      {updates?.update_available ? (
        <div className="update-banner" role="status">
          <UploadCloud size={19} />
          <div><strong>{tx("ui.version_version_available", { version: updates.latest_version || "-" })}</strong><span>{tx("ui.current_version_verified_and_installed_through_the_cpa_plugin_store", { version: updates.current_version })}</span></div>
          {updates.release_url ? <a href={updates.release_url} target="_blank" rel="noopener noreferrer">{tx("ui.release_notes")}<ExternalLink size={13} /></a> : null}
          <button className="button button-primary" type="button" disabled={installing} onClick={() => void installUpdate()}>{installing ? <LoaderCircle className="spin" size={15} /> : <UploadCloud size={15} />}{tx("ui.updated_2")}</button>
        </div>
      ) : null}

      {error || snapshot?.storage_error || lastRun?.error ? (
        <div className="automation-error" role="alert"><AlertTriangle size={16} /><span>{error || operatorMessage(snapshot?.storage_error || lastRun?.error, locale)}</span><IconButton label={tx("ui.dismiss_inspection_message")} onClick={() => setError("")}><X size={14} /></IconButton></div>
      ) : null}

      <div className="inspection-metrics" aria-label={tx("ui.inspection_metrics")}>
        <InspectionMetric label={tx("ui.accounts")} value={lastRun?.scanned ?? snapshot?.total ?? 0} icon={<ShieldCheck size={14} />} />
        <InspectionMetric label={tx("ui.healthy")} value={lastRun?.healthy ?? 0} tone="healthy" />
        <InspectionMetric label={tx("ui.invalid_credentials")} value={(lastRun?.invalid_credentials ?? 0) + (lastRun?.deactivated ?? 0)} tone="danger" />
        <InspectionMetric label={tx("ui.quota_limited")} value={lastRun?.quota_limited ?? 0} tone="warning" />
        <InspectionMetric label={tx("ui.needs_review")} value={(lastRun?.review ?? 0) + (lastRun?.unavailable ?? 0)} tone="review" />
        <InspectionMetric label={tx("ui.auto_disable")} value={lastRun?.auto_disabled ?? 0} />
        <InspectionMetric label={tx("ui.auto_enable")} value={lastRun?.auto_enabled ?? 0} tone="healthy" />
        <InspectionMetric label={tx("ui.pending_deletion")} value={lastRun?.delete_pending ?? 0} tone="danger" />
        <InspectionMetric
          label={tx("ui.anomaly_ratio")}
          value={`${snapshot?.anomaly_percent ?? 0}%`}
          detail={snapshot?.last_anomaly_trigger_at ? tx("ui.last_triggered_time", { time: formatDateTime(snapshot.last_anomaly_trigger_at) }) : tx("ui.not_triggered_yet")}
          tone={(snapshot?.anomaly_percent ?? 0) >= (snapshot?.policy.anomaly_threshold_percent ?? 101) ? "warning" : ""}
        />
        <InspectionMetric label={tx("ui.abnormal_sample")} value={`${snapshot?.anomaly_count ?? 0}/${snapshot?.anomaly_eligible ?? 0}`} />
        <InspectionMetric label={tx("ui.active_sweep_remaining")} value={snapshot?.probe_sweep_remaining ?? 0} tone={(snapshot?.probe_sweep_remaining ?? 0) > 0 ? "warning" : ""} />
      </div>

      <section className="inspection-results">
        <div className="inspection-filter-bar">
          <label className="search-box inspection-search">
            <Search size={16} /><input aria-label={tx("ui.search_inspected_accounts")} value={searchDraft} onChange={(event) => setSearchDraft(event.target.value)} placeholder={tx("ui.account_file_or_reason")} />
            {searchDraft ? <button type="button" aria-label={tx("ui.clear_inspection_search")} onClick={() => setSearchDraft("")}><X size={14} /></button> : null}
          </label>
          <select aria-label={tx("ui.inspection_health")} value={health} onChange={(event) => { setHealth(event.target.value as "" | InspectionHealth); setPage(1); }}>
            {healthOptions.map((option) => <option key={option.value || "all"} value={option.value}>{option.value ? healthLabel(option.value, locale) : tx(option.label)}</option>)}
          </select>
          <span>{tx("ui.count_results", { count: results.total })}</span>
        </div>
        <div className="inspection-table-scroll">
          <table className="inspection-table">
            <thead><tr><th>{tx("ui.healthy")}</th><th>{tx("ui.accounts")}</th><th>{tx("ui.type")}</th><th>{tx("ui.decision")}</th><th>{tx("ui.model_probe")}</th><th>{tx("ui.streak")}</th><th>{tx("ui.recommendation")}</th><th>{tx("ui.automation")}</th><th>{tx("ui.checked")}</th></tr></thead>
            <tbody>
              {loading ? <InspectionLoadingRows /> : results.results.map((result) => <InspectionRow key={result.id} result={result} />)}
            </tbody>
          </table>
          {!loading && results.results.length === 0 ? <div className="empty-state">{tx("ui.no_matching_inspection_results")}</div> : null}
        </div>
        <div className="pagination inspection-pagination">
          <span>{tx("ui.page_page_slash_pages", { page: results.page || 1, pages: results.pages || 1 })}</span>
          <IconButton label={tx("ui.previous_inspection_page")} disabled={page <= 1} onClick={() => setPage((current) => Math.max(1, current - 1))}><ChevronLeft size={17} /></IconButton>
          <strong>{page}</strong>
          <IconButton label={tx("ui.next_inspection_page")} disabled={results.pages === 0 || page >= results.pages} onClick={() => setPage((current) => current + 1)}><ChevronRight size={17} /></IconButton>
        </div>
      </section>

      <div className="automation-lower-grid">
        <section className="action-history">
          <header><div><strong>{tx("ui.automation_history")}</strong><span>{tx("ui.latest_count", { count: Math.min(actions.length, 8) })}</span></div>{autoDeleting ? <LoaderCircle className="spin" size={15} /> : <Clock3 size={15} />}</header>
          <div className="action-history-list">
            {actions.slice(0, 8).map((action) => <ActionHistoryRow key={action.id} action={action} />)}
            {actions.length === 0 ? <div className="automation-empty">{tx("ui.no_automation_actions")}</div> : null}
          </div>
        </section>
        <section className="update-status">
          <header><div><strong>{tx("ui.plugin_updates")}</strong><span>{tx(updates?.policy.auto_update ? "ui.automatic_installation_on" : "ui.manual_installation")}</span></div><ShieldCheck size={15} /></header>
          <div className="update-status-body">
            <div><span>{tx("ui.current_version")}</span><code>{updates?.current_version || "-"}</code></div>
            <div><span>{tx("ui.latest_version")}</span><code>{updates?.latest_version || "-"}</code></div>
            <div><span>{tx("ui.last_checked")}</span><time>{formatDateTime(updates?.checked_at)}</time></div>
            <div><span>{tx("ui.check_status")}</span><strong>{updates?.error ? operatorMessage(updates.error, locale) : tx(updates?.checking || updates?.pending ? "ui.checking" : updates?.update_available ? "ui.update_available" : "ui.up_to_date")}</strong></div>
          </div>
          <button className="button button-quiet" type="button" disabled={updateChecking || updates?.checking || updates?.pending} onClick={() => void checkUpdates()}>
            {updateChecking || updates?.checking || updates?.pending ? <LoaderCircle className="spin" size={15} /> : <RefreshCw size={15} />}{tx("ui.check_for_updates")}
          </button>
        </section>
      </div>

      {settingsOpen && snapshot && updates ? (
        <AutomationSettingsDialog
          inspection={snapshot.policy}
          updates={updates.policy}
          saving={settingsSaving}
          error={settingsError}
          onClose={() => setSettingsOpen(false)}
          onSave={(inspection, updatePolicy, confirmDelete, confirmDeleteInvalid, confirmUpdate) => void saveSettings(inspection, updatePolicy, confirmDelete, confirmDeleteInvalid, confirmUpdate)}
        />
      ) : null}
    </section>
  );
}

function InspectionMetric({ label, value, detail, tone = "", icon }: { label: string; value: string | number; detail?: string; tone?: string; icon?: ReactNode }) {
  return <div className={tone}><span>{icon}{label}</span><strong>{value}</strong>{detail ? <small>{detail}</small> : null}</div>;
}

function InspectionRow({ result }: { result: InspectionResult }) {
  const { locale, formatDateTime, tx } = useI18n();
  return (
    <tr>
      <td><span className={`health-badge health-${result.health}`}><span />{healthLabel(result.health, locale)}</span></td>
      <td><div className="inspection-account"><strong>{result.name || result.id}</strong><code>{result.id}</code></div></td>
      <td><div className="inspection-type"><strong>{result.provider || tx("ui.unknown")}</strong><span>{result.plan_type || result.type || "-"}</span></div></td>
      <td><div className="inspection-reason"><strong>{reasonLabel(result.reason_code, locale)}</strong><span>{confidenceLabel(result.confidence, locale)}</span></div></td>
      <td><div className={`inspection-probe probe-${result.probe_status || "none"}`}><strong>{result.probe_reason_code ? reasonLabel(result.probe_reason_code, locale) : tx("ui.no_probe_result")}</strong><span>{result.probe_model || "-"}{result.probe_latency_ms ? ` · ${result.probe_latency_ms} ms` : ""}</span>{result.probe_tested_at ? <time title={tx("ui.last_model_probe_time", { time: formatDateTime(result.probe_tested_at) })}>{formatDateTime(result.probe_tested_at)}</time> : null}</div></td>
      <td><div className="inspection-streak"><span className="danger">{tx("ui.failures_count", { count: result.failure_streak })}</span><span className="success">{tx("ui.recovery_count", { count: result.healthy_streak })}</span></div></td>
      <td><span className={`recommendation recommendation-${result.recommendation}`}>{recommendationLabel(result.recommendation, locale)}</span></td>
      <td><div className="inspection-action-state"><strong>{actionLabel(result.auto_action, locale)}</strong><span>{actionStatusLabel(result.auto_action_status, result.owned_disable, locale)}</span></div></td>
      <td><time>{formatDateTime(result.last_checked_at)}</time></td>
    </tr>
  );
}

function ActionHistoryRow({ action }: { action: InspectionAction }) {
  const { locale, formatDateTime } = useI18n();
  const Icon = action.action === "delete" || action.action === "delete_candidate" ? Trash2 : action.status === "succeeded" ? CheckCircle2 : ShieldAlert;
  return (
    <div className={`action-history-row action-${action.status}`}>
      <Icon size={14} />
      <div><strong>{actionLabel(action.action, locale)}</strong><span>{action.name || action.account_id}</span></div>
      <time>{formatDateTime(action.created_at)}</time>
    </div>
  );
}

function InspectionLoadingRows() {
  return <>{Array.from({ length: 5 }, (_, index) => <tr className="inspection-loading-row" key={index}><td colSpan={9}><span /></td></tr>)}</>;
}

function healthLabel(value: InspectionHealth, locale: Locale): string {
  const source = ({ healthy: "ui.healthy", quota_limited: "ui.quota_limited", invalid_credentials: "ui.invalid_credentials", deactivated: "ui.deactivated", review: "ui.needs_review", unavailable: "ui.unavailable", disabled: "ui.disabled", unknown: "ui.insufficient_evidence" } satisfies Record<InspectionHealth, UIMessageKey>)[value];
  return translateUI(locale, source);
}

function reasonLabel(value: string, locale: Locale): string {
  const source = ({
    healthy_recent_success: "ui.recent_request_succeeded", quota_exhausted: "ui.quota_exhausted", token_revoked: "ui.token_revoked", invalid_credentials: "ui.credentials_invalid_or_expired",
    account_deactivated: "ui.account_deactivated", workspace_deactivated: "ui.workspace_deactivated", authentication_review: "ui.authentication_needs_review",
    billing_review: "ui.billing_or_quota_needs_review", credential_permission_denied: "ui.credential_permission_denied", native_unavailable: "ui.cpa_marked_unavailable", manual_disabled: "ui.manually_disabled_2",
    transient_failure: "ui.temporary_upstream_failure", no_recent_evidence: "ui.no_recent_evidence",
    model_response_ok: "ui.model_response_is_healthy", authentication_failed: "ui.credentials_invalid_or_expired",
    quota_limited: "ui.upstream_quota_or_rate_limited_2", model_not_found: "ui.model_unavailable_or_missing",
    request_timeout: "ui.model_test_timed_out", upstream_unavailable: "ui.upstream_service_unavailable",
    invalid_response: "ui.could_not_validate_upstream_response", unsupported_provider: "ui.provider_unsupported",
  } satisfies Record<string, UIMessageKey>)[value];
  return source ? translateUI(locale, source) : value;
}

function confidenceLabel(value: string, locale: Locale): string {
  return translateUI(locale, value === "high" ? "ui.high_confidence" : value === "medium" ? "ui.medium_confidence" : "ui.low_confidence");
}

function recommendationLabel(value: InspectionResult["recommendation"], locale: Locale): string {
  return translateUI(locale, ({ keep: "ui.keep", reauth: "ui.reauthorize", review: "ui.manual_review", disable: "ui.disable", enable: "ui.enable", delete: "ui.delete" } satisfies Record<InspectionResult["recommendation"], UIMessageKey>)[value]);
}

function actionLabel(value: string | undefined, locale: Locale): string {
  const source = ({ disable: "ui.auto_disable", enable: "ui.auto_enable", delete: "ui.auto_delete", delete_candidate: "ui.pending_deletion_2" } satisfies Record<string, UIMessageKey>)[value || ""] || "ui.not_run";
  return translateUI(locale, source);
}

function actionStatusLabel(value: string | undefined, owned: boolean, locale: Locale): string {
  const source = ({ pending: "ui.pending_2", succeeded: "ui.completed_2", failed: "ui.waiting_to_retry", skipped: "ui.skipped_2" } satisfies Record<string, UIMessageKey>)[value || ""] || (owned ? "ui.inspection_owns_disable" : "ui.record_only");
  return translateUI(locale, source);
}

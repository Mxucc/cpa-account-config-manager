import {
  Activity,
  AlertTriangle,
  CheckCircle2,
  CheckSquare2,
  ChevronLeft,
  ChevronRight,
  Clock3,
  Download,
  LoaderCircle,
  RefreshCw,
  ScanSearch,
  Search,
  Settings2,
  ShieldAlert,
  ShieldCheck,
  Trash2,
  Wrench,
  X,
  XSquare,
} from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import * as api from "../api/client";
import { operatorMessage } from "../format/operatorMessage";
import type {
  Account,
  AccountDeletePreview,
  BatchPreview,
  InspectionAction,
  InspectionHealth,
  InspectionPolicy,
  InspectionResult,
  InspectionResultList,
  InspectionRemediationSummary,
  InspectionSnapshot,
  JobSnapshot,
  ModelTestResult,
} from "../types";
import { AutomationSettingsDialog } from "./AutomationSettingsDialog";
import { IconButton } from "./IconButton";
import { DeleteAccountDialog } from "./DeleteAccountDialog";
import { Modal } from "./Modal";
import { ModelTestDialog } from "./ModelTestDialog";
import { PreviewDialog } from "./PreviewDialog";
import { useI18n } from "../i18n";
import type { Locale } from "../i18n";
import { translateUI, type UIMessageKey } from "../i18n/uiText";
import {
  INSPECTION_PAGE_SIZE_OPTIONS,
  readInspectionPageSize,
  type InspectionPageSize,
  writeInspectionPageSize,
} from "../store/inspectionPageSize";

interface InspectionWorkspaceProps {
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
}

const emptyRemediationSummary: InspectionRemediationSummary = {
  actionable: 0,
  suggested_delete: 0,
  suggested_disable: 0,
  suggested_enable: 0,
  reauth: 0,
  deletable_reauth: 0,
  review: 0,
  keep: 0,
  handled: 0,
  editable_enabled: 0,
  editable_disabled: 0,
};

const emptyResults: InspectionResultList = { results: [], summary: emptyRemediationSummary, total: 0, page: 1, page_size: 50, pages: 0 };

interface RemediationPlan {
  mode: "recommended" | "reauth" | "selected";
  deleteIDs: string[];
  disableIDs: string[];
  enableIDs: string[];
}

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
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState<InspectionPageSize>(() => readInspectionPageSize());
  const [health, setHealth] = useState<"" | InspectionHealth>("");
  const [searchDraft, setSearchDraft] = useState("");
  const [search, setSearch] = useState("");
  const [loading, setLoading] = useState(true);
  const [scanningMode, setScanningMode] = useState<"native" | "active" | "stop" | "">("");
  const [runMode, setRunMode] = useState<"full" | "incremental" | "retry" | "filtered" | "selected">("full");
  const [selected, setSelected] = useState<Map<string, boolean>>(() => new Map());
  const [actionTarget, setActionTarget] = useState<InspectionResult | null>(null);
  const [reviewing, setReviewing] = useState(false);
  const [modelAccount, setModelAccount] = useState<Account | null>(null);
  const [modelResult, setModelResult] = useState<ModelTestResult | null>(null);
  const [modelTesting, setModelTesting] = useState(false);
  const [modelError, setModelError] = useState("");
  const [batchPreview, setBatchPreview] = useState<BatchPreview | null>(null);
  const [batchStarting, setBatchStarting] = useState(false);
  const [batchError, setBatchError] = useState("");
  const [deleteTarget, setDeleteTarget] = useState<Account | null>(null);
  const [deletePreview, setDeletePreview] = useState<AccountDeletePreview | null>(null);
  const [deletePreviewing, setDeletePreviewing] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState("");
  const [exportFormat, setExportFormat] = useState<"json" | "csv" | "jsonl">("json");
  const [exporting, setExporting] = useState(false);
  const [resolvingFiltered, setResolvingFiltered] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [settingsSaving, setSettingsSaving] = useState(false);
  const [settingsError, setSettingsError] = useState("");
  const [autoDeleting, setAutoDeleting] = useState(false);
  const [remediationPlan, setRemediationPlan] = useState<RemediationPlan | null>(null);
  const [remediating, setRemediating] = useState(false);
  const [remediationError, setRemediationError] = useState("");
  const [error, setError] = useState("");
  const autoDeleteBusy = useRef(false);
  const livePollCount = useRef(0);

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
      const [nextSnapshot, nextActions] = await Promise.all([
        api.getInspection(),
        api.listInspectionActions(50),
      ]);
      setSnapshot(nextSnapshot);
      setActions(nextActions);
    } catch (caught) {
      handleError(caught);
    }
  }, [handleError]);

  const refreshResults = useCallback(async () => {
    try {
      const next = await api.listInspectionResults(page, pageSize, health, search);
      setResults(next);
      if (next.pages > 0 && page > next.pages) setPage(next.pages);
    } catch (caught) {
      handleError(caught);
    }
  }, [handleError, health, page, pageSize, search]);

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
    const active = Boolean(snapshot?.running || snapshot?.pending || snapshot?.probe_sweep_status === "running");
    if (!active) return;
    let polling = false;
    const poll = async () => {
      if (polling) return;
      polling = true;
      try {
        const next = await api.getLiveInspection();
        setSnapshot(next);
        livePollCount.current += 1;
        if (livePollCount.current % 2 === 0) {
          const [nextResults, nextActions] = await Promise.all([
            api.listInspectionResults(page, pageSize, health, search),
            api.listInspectionActions(50),
          ]);
          setResults(nextResults);
          setActions(nextActions);
          if (nextResults.pages > 0 && page > nextResults.pages) setPage(nextResults.pages);
        }
      } catch (caught) {
        handleError(caught);
      } finally {
        polling = false;
      }
    };
    const timer = window.setInterval(() => void poll(), 700);
    return () => window.clearInterval(timer);
  }, [handleError, health, page, pageSize, search, snapshot?.pending, snapshot?.probe_sweep_status, snapshot?.running]);

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

  const runNativeScan = async () => {
    setScanningMode("native");
    setError("");
    try {
      setSnapshot(await api.scanNativeInspection());
    } catch (caught) {
      handleError(caught);
    } finally {
      setScanningMode("");
    }
  };

  const runActiveInspection = async (requestedMode = runMode) => {
    setScanningMode("active");
    setError("");
    try {
      if (requestedMode === "filtered" && !health) throw new Error(tx("ui.select_health_filter_before_scoped_inspection"));
      if (requestedMode === "selected" && selected.size === 0) throw new Error(tx("ui.select_accounts_before_scoped_inspection"));
      const request = requestedMode === "filtered"
        ? { mode: "scoped" as const, health: [health as InspectionHealth] }
        : requestedMode === "selected"
          ? { mode: "scoped" as const, selected: [...selected.keys()] }
          : { mode: requestedMode };
      setSnapshot(await api.startInspectionRun(request));
    } catch (caught) {
      handleError(caught);
    } finally {
      setScanningMode("");
    }
  };

  const stopActiveInspection = async () => {
    setScanningMode("stop");
    setError("");
    try {
      setSnapshot(await api.stopInspectionRun());
    } catch (caught) {
      handleError(caught);
    } finally {
      setScanningMode("");
    }
  };

  const updatePageSize = (next: InspectionPageSize) => {
    writeInspectionPageSize(next);
    setPageSize(next);
    setPage(1);
  };

  const toggleSelected = (result: InspectionResult) => {
    setSelected((current) => {
      const next = new Map(current);
      if (next.has(result.id)) next.delete(result.id);
      else next.set(result.id, result.disabled);
      return next;
    });
  };

  const toggleCurrentPage = () => {
    const editableIDs = results.results.filter((result) => result.editable).map((result) => result.id);
    const allSelected = editableIDs.length > 0 && editableIDs.every((id) => selected.has(id));
    setSelected((current) => {
      const next = new Map(current);
      for (const result of results.results.filter((entry) => entry.editable)) {
        if (allSelected) next.delete(result.id);
        else next.set(result.id, result.disabled);
      }
      return next;
    });
  };

  const previewAccountChange = async (ids: string[], disabled: boolean) => {
    setBatchError("");
    try {
      setBatchPreview(await api.createPreview({ mode: "selected", ids }, { disabled }));
      setActionTarget(null);
    } catch (caught) {
      handleError(caught);
    }
  };

  const collectFilteredResults = async (targetHealth = health, targetSearch = search): Promise<InspectionResult[]> => {
    const collected: InspectionResult[] = [];
    let currentPage = 1;
    let pages = 1;
    do {
      const batch = await api.listInspectionResults(currentPage, 200, targetHealth, targetSearch);
      collected.push(...batch.results);
      pages = Math.min(50, batch.pages);
      currentPage++;
    } while (currentPage <= pages && collected.length < 10_000);
    return collected.slice(0, 10_000);
  };

  const previewFilteredAccountChange = async (disabled: boolean) => {
    setResolvingFiltered(true);
    setError("");
    try {
      const ids = (await collectFilteredResults())
        .filter((result) => result.editable && result.disabled !== disabled)
        .map((result) => result.id);
      if (ids.length === 0) throw new Error(tx("ui.no_editable_inspection_results"));
      await previewAccountChange(ids, disabled);
    } catch (caught) {
      handleError(caught);
    } finally {
      setResolvingFiltered(false);
    }
  };

  const waitForBatch = async (jobID: string): Promise<JobSnapshot> => {
    for (let attempt = 0; attempt < 240; attempt++) {
      const job = await api.getJobStatus(false);
      if (job.id !== jobID) throw new Error(tx("ui.batch_job_was_replaced"));
      if (!job.running) return job;
      await new Promise((resolve) => window.setTimeout(resolve, 500));
    }
    throw new Error(tx("ui.batch_job_did_not_finish_in_time"));
  };

  const startAccountChange = async () => {
    if (!batchPreview) return;
    setBatchStarting(true);
    setBatchError("");
    try {
      const started = await api.startBatch(batchPreview.id);
      setBatchPreview(null);
      setSelected(new Map());
      onNotice(tx("ui.change_started"));
      const finished = await waitForBatch(started.id || "");
      if (finished.failed > 0 || finished.conflicts > 0) {
        setError(tx("ui.account_change_finished_with_failures", { succeeded: finished.succeeded, failed: finished.failed + finished.conflicts }));
      } else {
        onNotice(tx("ui.account_change_completed", { count: finished.succeeded }));
      }
      await Promise.all([refreshOverview(), refreshResults()]);
    } catch (caught) {
      setBatchError(operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale));
    } finally {
      setBatchStarting(false);
    }
  };

  const openRemediation = async (mode: RemediationPlan["mode"]) => {
    setResolvingFiltered(true);
    setRemediationError("");
    try {
      const source = await collectFilteredResults(mode === "selected" ? "" : health, mode === "selected" ? "" : search);
      const candidates = mode === "selected" ? source.filter((result) => selected.has(result.id)) : source;
      const deleteIDs = candidates
        .filter((result) => result.manual_delete_eligible && (mode === "reauth"
          ? result.recommendation === "reauth"
          : result.recommendation === "delete" || (mode === "selected" && result.recommendation === "reauth")))
        .map((result) => result.id);
      const disableIDs = mode === "recommended"
        ? candidates.filter((result) => result.editable && !result.disabled && result.recommendation === "disable").map((result) => result.id)
        : [];
      const enableIDs = mode === "recommended"
        ? candidates.filter((result) => result.editable && result.disabled && result.recommendation === "enable").map((result) => result.id)
        : [];
      if (deleteIDs.length + disableIDs.length + enableIDs.length === 0) {
        throw new Error(tx("ui.no_recommended_actions_available"));
      }
      setRemediationPlan({ mode, deleteIDs, disableIDs, enableIDs });
    } catch (caught) {
      handleError(caught);
    } finally {
      setResolvingFiltered(false);
    }
  };

  const executeRemediation = async () => {
    if (!remediationPlan) return;
    setRemediating(true);
    setRemediationError("");
    let succeeded = 0;
    let failed = 0;
    let skipped = 0;
    try {
      for (let offset = 0; offset < remediationPlan.deleteIDs.length; offset += 100) {
        const run = await api.deleteInspectionRecommendations(remediationPlan.deleteIDs.slice(offset, offset + 100));
        succeeded += run.succeeded;
        failed += run.failed;
        skipped += run.skipped;
      }
      for (const target of [
        { ids: remediationPlan.disableIDs, disabled: true },
        { ids: remediationPlan.enableIDs, disabled: false },
      ]) {
        if (target.ids.length === 0) continue;
        const preview = await api.createPreview({ mode: "selected", ids: target.ids }, { disabled: target.disabled });
        if (preview.eligible === 0) {
          skipped += preview.total;
          continue;
        }
        const started = await api.startBatch(preview.id);
        const finished = await waitForBatch(started.id || "");
        succeeded += finished.succeeded;
        failed += finished.failed + finished.conflicts;
        skipped += finished.skipped;
      }
      setSelected(new Map());
      await Promise.all([refreshOverview(), refreshResults()]);
      if (failed > 0 || skipped > 0) {
        setRemediationError(tx("ui.remediation_finished_with_result", { succeeded, failed, skipped }));
      } else {
        setRemediationPlan(null);
        onNotice(tx("ui.remediation_completed_count", { count: succeeded }));
      }
    } catch (caught) {
      setRemediationError(operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale));
      await Promise.all([refreshOverview(), refreshResults()]);
    } finally {
      setRemediating(false);
    }
  };

  const openModelTest = (result: InspectionResult) => {
    setActionTarget(null);
    setModelResult(null);
    setModelError("");
    setModelAccount(inspectionResultAccount(result));
  };

  const testModel = async (model: string) => {
    if (!modelAccount) return;
    setModelTesting(true);
    setModelError("");
    try {
      setModelResult(await api.testAccountModel(modelAccount.id, model));
      await Promise.all([refreshOverview(), refreshResults()]);
    } catch (caught) {
      setModelError(operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale));
    } finally {
      setModelTesting(false);
    }
  };

  const updateReview = async (result: InspectionResult, action: "resolve" | "ignore" | "reopen") => {
    setReviewing(true);
    setError("");
    try {
      await api.updateInspectionReview(result.id, action);
      setActionTarget(null);
      onNotice(tx("ui.review_updated"));
      await Promise.all([refreshOverview(), refreshResults()]);
    } catch (caught) {
      handleError(caught);
    } finally {
      setReviewing(false);
    }
  };

  const openDelete = async (result: InspectionResult) => {
    const account = inspectionResultAccount(result);
    setActionTarget(null);
    setDeleteTarget(account);
    setDeletePreview(null);
    setDeleteError("");
    setDeletePreviewing(true);
    try {
      setDeletePreview(await api.createAccountDeletePreview(result.id));
    } catch (caught) {
      setDeleteError(operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale));
    } finally {
      setDeletePreviewing(false);
    }
  };

  const confirmDelete = async () => {
    if (!deletePreview) return;
    setDeleting(true);
    setDeleteError("");
    try {
      const deleted = await api.deleteAccount(deletePreview.id);
      onNotice(tx("ui.deleted_account_account", { account: deleted.account.label || deleted.account.email || deleted.account.name }));
      setDeleteTarget(null);
      setDeletePreview(null);
      await Promise.all([refreshOverview(), refreshResults()]);
    } catch (caught) {
      setDeleteError(operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale));
    } finally {
      setDeleting(false);
    }
  };

  const exportInspectionResults = async () => {
    setExporting(true);
    setError("");
    try {
      const download = await api.downloadInspectionExport(exportFormat, health, search);
      onNotice(tx("ui.inspection_results_exported_as_file", { file: download.filename }));
    } catch (caught) {
      handleError(caught);
    } finally {
      setExporting(false);
    }
  };

  const saveSettings = async (inspection: InspectionPolicy, confirmDelete: boolean, confirmDeleteInvalid: boolean) => {
    setSettingsSaving(true);
    setSettingsError("");
    try {
      setSnapshot(await api.saveInspectionPolicy(inspection, confirmDelete, confirmDeleteInvalid));
      setSettingsOpen(false);
      onNotice(tx("ui.inspection_settings_saved"));
    } catch (caught) {
      handleError(caught, "settings");
    } finally {
      setSettingsSaving(false);
    }
  };

  const lastRun = snapshot?.last_run;
  const sweepTotal = snapshot?.probe_sweep_total ?? 0;
  const sweepCompleted = snapshot?.probe_sweep_completed ?? 0;
  const sweepRemaining = snapshot?.probe_sweep_remaining ?? 0;
  const sweepStatus = snapshot?.probe_sweep_status;
  const inspectionBusy = Boolean(snapshot?.running || snapshot?.pending || scanningMode);
  const remediation = results.summary ?? emptyRemediationSummary;
  const selectedDisabledIDs = [...selected.entries()].filter(([, disabled]) => disabled).map(([id]) => id);
  const selectedEnabledIDs = [...selected.entries()].filter(([, disabled]) => !disabled).map(([id]) => id);
  const lastAnomalyTriggerAt = meaningfulTimestamp(snapshot?.last_anomaly_trigger_at) ? snapshot?.last_anomaly_trigger_at : undefined;
  return (
    <section className="automation-panel" aria-label={tx("ui.inspection_and_automation")}>
      <header className="automation-toolbar">
        <div className="automation-title">
          <div className="automation-sources">
            <span className={`automation-live ${snapshot?.policy.enabled ? "is-on" : ""}`}><span />{tx(snapshot?.policy.enabled ? "ui.scheduled_inspection" : "ui.manual")}</span>
            {snapshot?.policy.model_probe_enabled ? <span className={`automation-live ${snapshot.active_probe_armed ? "is-on" : "is-waiting"}`}><span />{tx(snapshot.active_probe_armed ? "ui.active_probe_ready" : "ui.active_probe_waiting_for_auth")}</span> : null}
            <span className={`automation-live ${snapshot?.policy.auto_disable ? "is-on" : ""}`}><span />{tx("ui.auto_disable_status", { status: tx(snapshot?.policy.auto_disable ? "ui.on" : "ui.off") })}</span>
            <span className={`automation-live ${snapshot?.policy.auto_enable ? "is-on" : ""}`}><span />{tx("ui.auto_enable_status", { status: tx(snapshot?.policy.auto_enable ? "ui.on" : "ui.off") })}</span>
            {snapshot?.anomaly_trigger_pending || (snapshot?.probe_sweep_remaining ?? 0) > 0 ? <span className="automation-live is-waiting"><span />{tx("ui.full_active_inspection_queued")}</span> : null}
          </div>
          <div><strong>{tx("ui.account_health_inspection")}</strong><span>{snapshot?.running || snapshot?.pending ? tx("ui.reading_cpa_status") : tx("ui.last_completed_time", { time: formatDateTime(lastRun?.finished_at) })}</span></div>
        </div>
        <div className="automation-toolbar-actions">
          <button className="button inspection-mode-button" type="button" disabled={inspectionBusy} onClick={() => void runNativeScan()}>
            {scanningMode === "native" ? <LoaderCircle className="spin" size={16} /> : <Activity size={16} />}{tx("ui.quick_native_inspection")}
          </button>
          <label className="inspection-run-mode">
            <span>{tx("ui.inspection_run_mode")}</span>
            <select value={runMode} disabled={inspectionBusy} onChange={(event) => setRunMode(event.target.value as typeof runMode)}>
              <option value="full">{tx("ui.full_inspection")}</option>
              <option value="incremental">{tx("ui.incremental_inspection")}</option>
              <option value="retry">{tx("ui.retry_review_accounts")}</option>
              <option value="filtered" disabled={!health}>{tx("ui.reinspect_current_health")}</option>
              <option value="selected" disabled={selected.size === 0}>{tx("ui.reinspect_selected")}</option>
            </select>
          </label>
          <button className="button button-primary inspection-mode-button" type="button" disabled={inspectionBusy || (runMode === "filtered" && !health) || (runMode === "selected" && selected.size === 0)} onClick={() => void runActiveInspection()}>
            {scanningMode === "active" ? <LoaderCircle className="spin" size={16} /> : <ScanSearch size={16} />}{tx("ui.start_inspection")}
          </button>
          {snapshot?.running || snapshot?.pending ? <IconButton label={tx("ui.stop_inspection")} disabled={scanningMode === "stop"} onClick={() => void stopActiveInspection()}>{scanningMode === "stop" ? <LoaderCircle className="spin" size={17} /> : <XSquare size={17} />}</IconButton> : null}
          <IconButton label={tx("ui.refresh_inspection")} disabled={loading} onClick={() => void Promise.all([refreshOverview(), refreshResults()])}><RefreshCw className={loading ? "spin" : ""} size={17} /></IconButton>
          <IconButton label={tx("ui.inspection_and_automation_settings")} disabled={!snapshot} onClick={() => { setSettingsError(""); setSettingsOpen(true); }}><Settings2 size={17} /></IconButton>
        </div>
      </header>

      {error || snapshot?.storage_error || lastRun?.error ? (
        <div className="automation-error" role="alert"><AlertTriangle size={16} /><span>{error || operatorMessage(snapshot?.storage_error || lastRun?.error, locale)}</span><IconButton label={tx("ui.dismiss_inspection_message")} onClick={() => setError("")}><X size={14} /></IconButton></div>
      ) : null}

      {snapshot?.probe_sweep_source && sweepStatus ? (
        <div className={`inspection-sweep-progress sweep-${sweepStatus}`} role="status">
          <div>
            <strong>{runModeLabel(snapshot.run_mode, locale)}</strong>
            <span>{sweepSourceLabel(snapshot.probe_sweep_source, locale)} · {sweepStatusLabel(sweepStatus, locale)} · {probePhaseLabel(snapshot.probe_phase, locale)}</span>
          </div>
          <progress max={Math.max(1, sweepTotal)} value={Math.min(sweepCompleted, Math.max(1, sweepTotal))} aria-label={tx("ui.full_server_inspection_progress")} />
          <code>{tx("ui.completed_count_of_total_remaining_remaining", { completed: sweepCompleted, total: sweepTotal, remaining: sweepRemaining })}</code>
          <small>{tx("ui.retry_progress", { completed: snapshot.retry_completed ?? 0, total: snapshot.retry_total ?? 0 })}</small>
        </div>
      ) : null}

      {snapshot?.active_run ? (
        <section className="inspection-live-panel" aria-label={tx("ui.live_inspection")}>
          <header>
            <div><Activity size={17} /><strong>{tx("ui.live_inspection")}</strong><span>{runModeLabel(snapshot.active_run.mode, locale)} · {probePhaseLabel(snapshot.active_run.phase, locale)}</span></div>
            <div className="inspection-live-status"><span className="live-pulse" />{tx("ui.inspecting_now")}<code>{tx("ui.live_results_count", { count: snapshot.live_results?.length ?? 0 })}</code></div>
          </header>
          <div className="inspection-live-results">
            {(snapshot.live_results ?? []).map((result) => (
              <LiveInspectionResult
                key={`${result.run_id}:${result.id}`}
                result={result}
                onModelTest={() => openModelTest(result)}
                onToggle={() => void previewAccountChange([result.id], !result.disabled)}
                onAction={() => setActionTarget(result)}
              />
            ))}
            {(snapshot.live_results?.length ?? 0) === 0 ? <div className="inspection-live-empty"><LoaderCircle className="spin" size={17} />{tx("ui.waiting_for_first_live_result")}</div> : null}
          </div>
        </section>
      ) : null}

      <div className="inspection-metrics" aria-label={tx("ui.inspection_metrics")}>
        <InspectionMetric label={tx("ui.accounts")} value={lastRun?.scanned ?? snapshot?.total ?? 0} icon={<ShieldCheck size={14} />} />
        <InspectionMetric label={tx("ui.healthy")} value={lastRun?.healthy ?? 0} tone="healthy" />
        <InspectionMetric label={tx("ui.invalid_credentials")} value={(lastRun?.invalid_credentials ?? 0) + (lastRun?.deactivated ?? 0)} tone="danger" />
        <InspectionMetric label={tx("ui.quota_limited")} value={lastRun?.quota_limited ?? 0} tone="warning" />
        <InspectionMetric label={tx("ui.needs_review")} value={lastRun?.review ?? 0} tone="review" />
        <InspectionMetric label={tx("ui.unavailable")} value={lastRun?.unavailable ?? 0} tone="review" />
        <InspectionMetric label={tx("ui.auto_disable")} value={lastRun?.auto_disabled ?? 0} />
        <InspectionMetric label={tx("ui.auto_enable")} value={lastRun?.auto_enabled ?? 0} tone="healthy" />
        <InspectionMetric label={tx("ui.pending_deletion")} value={lastRun?.delete_pending ?? 0} tone="danger" />
        <InspectionMetric
          label={tx("ui.anomaly_ratio")}
          value={`${snapshot?.anomaly_percent ?? 0}%`}
          detail={lastAnomalyTriggerAt ? tx("ui.last_triggered_time", { time: formatDateTime(lastAnomalyTriggerAt) }) : tx("ui.not_triggered_yet")}
          tone={(snapshot?.anomaly_percent ?? 0) >= (snapshot?.policy.anomaly_threshold_percent ?? 101) ? "warning" : ""}
        />
        <InspectionMetric label={tx("ui.abnormal_sample")} value={`${snapshot?.anomaly_count ?? 0}/${snapshot?.anomaly_eligible ?? 0}`} />
        <InspectionMetric label={tx("ui.full_server_inspection_progress")} value={sweepTotal > 0 ? `${sweepCompleted}/${sweepTotal}` : "-"} detail={sweepTotal > 0 ? tx("ui.remaining_count", { count: sweepRemaining }) : undefined} tone={sweepRemaining > 0 ? "warning" : ""} />
      </div>

      <section className="inspection-remediation" aria-label={tx("ui.inspection_remediation_queue")}>
        <header>
          <div><strong>{tx("ui.inspection_remediation_queue")}</strong><span>{tx("ui.inspection_remediation_description")}</span></div>
          <b>{tx("ui.recommended_action_count", { count: remediation.actionable })}</b>
        </header>
        <div className="inspection-remediation-counts">
          <span className="danger"><small>{tx("ui.suggested_delete")}</small><strong>{remediation.suggested_delete}</strong></span>
          <span className="warning"><small>{tx("ui.suggested_disable")}</small><strong>{remediation.suggested_disable}</strong></span>
          <span className="healthy"><small>{tx("ui.suggested_enable")}</small><strong>{remediation.suggested_enable}</strong></span>
          <span className="review"><small>{tx("ui.relogin_required")}</small><strong>{remediation.reauth}</strong></span>
          <span><small>{tx("ui.keep")}</small><strong>{remediation.keep}</strong></span>
          <span className="healthy"><small>{tx("ui.handled")}</small><strong>{remediation.handled}</strong></span>
          <span><small>{tx("ui.manual_review")}</small><strong>{remediation.review}</strong></span>
        </div>
        <div className="inspection-remediation-actions">
          <button className="button button-primary" type="button" disabled={resolvingFiltered || remediating || remediation.suggested_delete + remediation.suggested_disable + remediation.suggested_enable === 0} onClick={() => void openRemediation("recommended")}>
            {resolvingFiltered || remediating ? <LoaderCircle className="spin" size={15} /> : <Wrench size={15} />}{tx("ui.execute_recommended_actions")}
          </button>
          <button className="button button-danger" type="button" disabled={resolvingFiltered || remediating || remediation.deletable_reauth === 0} onClick={() => void openRemediation("reauth")}>
            <Trash2 size={15} />{tx("ui.delete_relogin_accounts", { count: remediation.deletable_reauth })}
          </button>
        </div>
      </section>

      <section className="inspection-results">
        <div className="inspection-filter-bar">
          <label className="search-box inspection-search">
            <Search size={16} /><input aria-label={tx("ui.search_inspected_accounts")} value={searchDraft} onChange={(event) => setSearchDraft(event.target.value)} placeholder={tx("ui.account_file_or_reason")} />
            {searchDraft ? <button type="button" aria-label={tx("ui.clear_inspection_search")} onClick={() => setSearchDraft("")}><X size={14} /></button> : null}
          </label>
          <select aria-label={tx("ui.inspection_health")} value={health} onChange={(event) => { setHealth(event.target.value as "" | InspectionHealth); setPage(1); }}>
            {healthOptions.map((option) => <option key={option.value || "all"} value={option.value}>{option.value ? healthLabel(option.value, locale) : tx(option.label)}</option>)}
          </select>
          <label className="inspection-page-size">
            <span>{tx("ui.per_page")}</span>
            <select aria-label={tx("ui.inspection_results_per_page")} value={pageSize} onChange={(event) => updatePageSize(Number(event.target.value) as InspectionPageSize)}>
              {INSPECTION_PAGE_SIZE_OPTIONS.map((option) => <option key={option} value={option}>{option}</option>)}
            </select>
          </label>
          <label className="inspection-export-format">
            <span>{tx("ui.format")}</span>
            <select aria-label={tx("ui.export_inspection_results")} value={exportFormat} onChange={(event) => setExportFormat(event.target.value as typeof exportFormat)}>
              <option value="json">JSON</option><option value="csv">CSV</option><option value="jsonl">JSONL</option>
            </select>
          </label>
          <IconButton label={tx("ui.export_inspection_results")} disabled={exporting} onClick={() => void exportInspectionResults()}>{exporting ? <LoaderCircle className="spin" size={16} /> : <Download size={16} />}</IconButton>
          <span>{tx("ui.count_results", { count: results.total })}</span>
        </div>
        {results.total > 0 ? (
          <div className="inspection-filter-actions" role="toolbar" aria-label={tx("ui.filtered_results")}>
            <span>{tx("ui.filtered_results")} · {tx("ui.count_results", { count: results.total })}</span>
            <button className="button button-quiet" type="button" disabled={resolvingFiltered || remediation.editable_disabled === 0} onClick={() => void previewFilteredAccountChange(false)}>{resolvingFiltered ? <LoaderCircle className="spin" size={15} /> : <CheckSquare2 size={15} />}{tx("ui.enable_filtered_disabled_accounts", { count: remediation.editable_disabled })}</button>
            <button className="button button-quiet" type="button" disabled={resolvingFiltered || remediation.editable_enabled === 0} onClick={() => void previewFilteredAccountChange(true)}>{resolvingFiltered ? <LoaderCircle className="spin" size={15} /> : <XSquare size={15} />}{tx("ui.disable_filtered_enabled_accounts", { count: remediation.editable_enabled })}</button>
          </div>
        ) : null}
        {selected.size > 0 ? (
          <div className="inspection-selection-bar" role="toolbar" aria-label={tx("ui.selected_accounts") }>
            <strong>{tx("ui.selected_count", { count: selected.size })}</strong>
            <button className="button button-quiet" type="button" disabled={selectedDisabledIDs.length === 0} onClick={() => void previewAccountChange(selectedDisabledIDs, false)}><CheckSquare2 size={15} />{tx("ui.enable_selected_disabled_accounts", { count: selectedDisabledIDs.length })}</button>
            <button className="button button-quiet" type="button" disabled={selectedEnabledIDs.length === 0} onClick={() => void previewAccountChange(selectedEnabledIDs, true)}><XSquare size={15} />{tx("ui.disable_selected_enabled_accounts", { count: selectedEnabledIDs.length })}</button>
            <button className="button button-danger" type="button" disabled={resolvingFiltered} onClick={() => void openRemediation("selected")}><Trash2 size={15} />{tx("ui.delete_selected_recommendations")}</button>
            <button className="button button-quiet" type="button" disabled={inspectionBusy} onClick={() => { setRunMode("selected"); void runActiveInspection("selected"); }}><ScanSearch size={15} />{tx("ui.inspect_selected")}</button>
            <button className="button button-quiet" type="button" onClick={() => setSelected(new Map())}>{tx("ui.clear_selection")}</button>
          </div>
        ) : null}
        <div className="inspection-table-scroll">
          <table className="inspection-table">
            <thead><tr><th className="inspection-select-cell"><input type="checkbox" aria-label={tx("ui.select_current_page")} checked={results.results.some((result) => result.editable) && results.results.filter((result) => result.editable).every((result) => selected.has(result.id))} onChange={toggleCurrentPage} /></th><th>{tx("ui.healthy")}</th><th>{tx("ui.accounts")}</th><th>{tx("ui.type")}</th><th>{tx("ui.quota_and_usage")}</th><th>{tx("ui.decision")}</th><th>{tx("ui.model_probe")}</th><th>{tx("ui.streak")}</th><th>{tx("ui.recommendation")}</th><th>{tx("ui.automation")}</th><th>{tx("ui.checked")}</th><th>{tx("ui.actions")}</th></tr></thead>
            <tbody>
              {loading ? <InspectionLoadingRows /> : results.results.map((result) => <InspectionRow key={result.id} result={result} selected={selected.has(result.id)} onSelect={() => toggleSelected(result)} onModelTest={() => openModelTest(result)} onToggle={() => void previewAccountChange([result.id], !result.disabled)} onAction={() => setActionTarget(result)} />)}
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

      {snapshot?.recent_runs?.length ? (
        <section className="inspection-run-history" aria-label={tx("ui.inspection_run_history")}>
          <header><strong>{tx("ui.inspection_run_history")}</strong><span>{tx("ui.latest_count", { count: snapshot.recent_runs.length })}</span></header>
          <div>
            {snapshot.recent_runs.slice(0, 6).map((run) => (
              <div className={`inspection-run-row run-${run.status}`} key={run.id}>
                <span className="inspection-run-dot" />
                <strong>{runModeLabel(run.mode, locale)}</strong>
                <span>{sweepStatusLabel(run.status, locale)} · {probePhaseLabel(run.phase, locale)}</span>
                <code>{run.primary_completed}/{run.primary_total}</code>
                <code>{tx("ui.retry_progress", { completed: run.retry_completed, total: run.retry_total })}</code>
                <time>{formatDateTime(run.finished_at || run.started_at)}</time>
              </div>
            ))}
          </div>
        </section>
      ) : null}

      <div className="automation-lower-grid automation-history-only">
        <section className="action-history">
          <header><div><strong>{tx("ui.automation_history")}</strong><span>{tx("ui.latest_count", { count: Math.min(actions.length, 8) })}</span></div>{autoDeleting ? <LoaderCircle className="spin" size={15} /> : <Clock3 size={15} />}</header>
          <div className="action-history-list">
            {actions.slice(0, 8).map((action) => <ActionHistoryRow key={action.id} action={action} />)}
            {actions.length === 0 ? <div className="automation-empty">{tx("ui.no_automation_actions")}</div> : null}
          </div>
        </section>
      </div>

      {settingsOpen && snapshot ? (
        <AutomationSettingsDialog
          inspection={snapshot.policy}
          saving={settingsSaving}
          error={settingsError}
          onClose={() => setSettingsOpen(false)}
          onSave={(inspection, confirmDelete, confirmDeleteInvalid) => void saveSettings(inspection, confirmDelete, confirmDeleteInvalid)}
        />
      ) : null}
      {actionTarget ? (
        <InspectionActionDialog
          result={actionTarget}
          reviewing={reviewing}
          onClose={() => setActionTarget(null)}
          onModelTest={() => openModelTest(actionTarget)}
          onDisable={() => void previewAccountChange([actionTarget.id], true)}
          onEnable={() => void previewAccountChange([actionTarget.id], false)}
          onDelete={() => void openDelete(actionTarget)}
          onReview={(action) => void updateReview(actionTarget, action)}
        />
      ) : null}
      {modelAccount ? <ModelTestDialog account={modelAccount} result={modelResult} error={modelError} testing={modelTesting} onClose={() => setModelAccount(null)} onTest={(model) => void testModel(model)} /> : null}
      {batchPreview ? <PreviewDialog preview={batchPreview} starting={batchStarting} error={batchError} onClose={() => setBatchPreview(null)} onConfirm={() => void startAccountChange()} /> : null}
      {deleteTarget ? <DeleteAccountDialog account={deleteTarget} preview={deletePreview} previewing={deletePreviewing} deleting={deleting} error={deleteError} onClose={() => { setDeleteTarget(null); setDeletePreview(null); }} onConfirm={() => void confirmDelete()} /> : null}
      {remediationPlan ? <InspectionRemediationDialog plan={remediationPlan} running={remediating} error={remediationError} onClose={() => !remediating && setRemediationPlan(null)} onConfirm={() => void executeRemediation()} /> : null}
    </section>
  );
}

function InspectionRemediationDialog({ plan, running, error, onClose, onConfirm }: {
  plan: RemediationPlan;
  running: boolean;
  error: string;
  onClose: () => void;
  onConfirm: () => void;
}) {
  const { tx } = useI18n();
  const deleteCount = plan.deleteIDs.length;
  const total = deleteCount + plan.disableIDs.length + plan.enableIDs.length;
  return (
    <Modal
      title={tx(plan.mode === "reauth" ? "ui.confirm_delete_relogin_accounts" : plan.mode === "selected" ? "ui.confirm_selected_remediation" : "ui.confirm_recommended_actions")}
      onClose={onClose}
      footer={<>
        <span className="modal-scope">{tx("ui.count_accounts", { count: total })}</span>
        <button className="button" type="button" disabled={running} onClick={onClose}>{tx("ui.cancel")}</button>
        <button className={`button ${deleteCount > 0 ? "button-danger" : "button-primary"}`} type="button" disabled={running || total === 0} onClick={onConfirm}>
          {running ? <LoaderCircle className="spin" size={15} /> : deleteCount > 0 ? <Trash2 size={15} /> : <Wrench size={15} />}
          {running ? tx("ui.executing_remediation") : tx("ui.confirm_and_execute")}
        </button>
      </>}
    >
      <div className="inspection-remediation-confirm">
        <div className="preview-metrics">
          <div className="preview-metric danger"><span>{tx("ui.delete")}</span><strong>{deleteCount}</strong></div>
          <div className="preview-metric warning"><span>{tx("ui.disable")}</span><strong>{plan.disableIDs.length}</strong></div>
          <div className="preview-metric success"><span>{tx("ui.enable")}</span><strong>{plan.enableIDs.length}</strong></div>
        </div>
        {deleteCount > 0 ? <div className="delete-warning"><AlertTriangle size={18} /><div><strong>{tx("ui.deletion_cannot_be_undone")}</strong><span>{tx("ui.bulk_inspection_delete_revalidates_each_file")}</span></div></div> : null}
        {error ? <div className="preview-start-error" role="alert"><AlertTriangle size={17} /><div><strong>{tx("ui.remediation_result")}</strong><span>{error}</span></div></div> : null}
      </div>
    </Modal>
  );
}

function meaningfulTimestamp(value: string | undefined): value is string {
  if (!value) return false;
  const parsed = new Date(value);
  return Number.isFinite(parsed.getTime()) && parsed.getUTCFullYear() > 1;
}

function InspectionMetric({ label, value, detail, tone = "", icon }: { label: string; value: string | number; detail?: string; tone?: string; icon?: ReactNode }) {
  return <div className={tone}><span>{icon}{label}</span><strong>{value}</strong>{detail ? <small>{detail}</small> : null}</div>;
}

function InspectionRow({ result, selected, onSelect, onModelTest, onToggle, onAction }: { result: InspectionResult; selected: boolean; onSelect: () => void; onModelTest: () => void; onToggle: () => void; onAction: () => void }) {
  const { locale, formatDateTime, tx } = useI18n();
  return (
    <tr className={result.run_id ? "inspection-result-observed" : ""}>
      <td className="inspection-select-cell"><input type="checkbox" aria-label={tx("ui.select_account", { account: result.name || result.id })} checked={selected} disabled={!result.editable} onChange={onSelect} /></td>
      <td><span className={`health-badge health-${result.health}`}><span />{healthLabel(result.health, locale)}</span></td>
      <td><div className="inspection-account"><strong>{result.name || result.id}</strong><code>{result.id}</code></div></td>
      <td><div className="inspection-type"><strong>{result.provider || tx("ui.unknown")}</strong><span>{result.plan_type || result.type || "-"}</span></div></td>
      <td><InspectionQuotaUsage result={result} /></td>
      <td><div className="inspection-reason"><strong>{reasonLabel(result.reason_code, locale)}</strong><span>{result.status_code ? `HTTP ${result.status_code} · ` : ""}{confidenceLabel(result.confidence, locale)}{result.signal_source ? ` · ${signalSourceLabel(result.signal_source, locale)}` : ""}</span>{result.review_status ? <small className={`review-state review-${result.review_status}`}>{reviewStatusLabel(result.review_status, locale)}</small> : null}</div></td>
      <td><div className={`inspection-probe probe-${result.probe_status || "none"}`}><strong>{result.probe_reason_code ? reasonLabel(result.probe_reason_code, locale) : tx("ui.no_probe_result")}</strong><span>{result.probe_kind === "credential" ? tx("ui.credential_preflight") : result.probe_model || "-"}{result.probe_latency_ms ? ` · ${result.probe_latency_ms} ms` : ""}</span>{result.probe_tested_at ? <time title={tx("ui.last_model_probe_time", { time: formatDateTime(result.probe_tested_at) })}>{formatDateTime(result.probe_tested_at)}</time> : null}</div></td>
      <td><div className="inspection-streak"><span className="danger">{tx("ui.failures_count", { count: result.failure_streak })}</span><span className="success">{tx("ui.recovery_count", { count: result.healthy_streak })}</span></div></td>
      <td><span className={`recommendation recommendation-${result.recommendation}`}>{recommendationLabel(result.recommendation, locale)}</span></td>
      <td><div className="inspection-action-state"><strong>{result.circuit_open ? tx("ui.passive_temporary_circuit") : actionLabel(result.auto_action, locale)}</strong><span>{result.circuit_open && result.recover_after ? tx("ui.circuit_recovers_at_time", { time: formatDateTime(result.recover_after) }) : actionStatusLabel(result.auto_action_status, result.owned_disable, locale)}</span></div></td>
      <td><time>{formatDateTime(result.last_checked_at)}</time></td>
      <td><div className="inspection-inline-actions"><IconButton label={tx("ui.model_retest")} onClick={onModelTest}><Activity size={15} /></IconButton><IconButton label={tx(result.disabled ? "ui.enable" : "ui.disable")} disabled={!result.editable} onClick={onToggle}>{result.disabled ? <CheckSquare2 size={15} /> : <XSquare size={15} />}</IconButton><IconButton label={tx("ui.account_action")} onClick={onAction}><Wrench size={15} /></IconButton></div></td>
    </tr>
  );
}

function LiveInspectionResult({ result, onModelTest, onToggle, onAction }: { result: InspectionResult; onModelTest: () => void; onToggle: () => void; onAction: () => void }) {
  const { locale, formatDateTime, tx } = useI18n();
  return (
    <article className={`inspection-live-result health-${result.health}`}>
      <span className="inspection-live-health" />
      <div className="inspection-live-identity"><strong>{result.name || result.id}</strong><span>{result.provider || tx("ui.unknown")} · {result.plan_type || result.type || "-"}</span></div>
      <div className="inspection-live-decision"><strong>{reasonLabel(result.reason_code, locale)}</strong><span>{result.status_code ? `HTTP ${result.status_code} · ` : ""}{recommendationLabel(result.recommendation, locale)}</span></div>
      <InspectionQuotaUsage result={result} compact />
      <time>{result.run_observed_at ? tx("ui.live_updated_at", { time: formatDateTime(result.run_observed_at) }) : formatDateTime(result.last_checked_at)}</time>
      <div className="inspection-inline-actions"><IconButton label={tx("ui.model_retest")} onClick={onModelTest}><Activity size={15} /></IconButton><IconButton label={tx(result.disabled ? "ui.enable" : "ui.disable")} disabled={!result.editable} onClick={onToggle}>{result.disabled ? <CheckSquare2 size={15} /> : <XSquare size={15} />}</IconButton><IconButton label={tx("ui.account_action")} onClick={onAction}><Wrench size={15} /></IconButton></div>
    </article>
  );
}

function InspectionQuotaUsage({ result, compact = false }: { result: InspectionResult; compact?: boolean }) {
  const { locale, formatDateTime, tx } = useI18n();
  const windows = [
    { key: "five_hour", label: tx("ui.5_hour_usage"), value: result.codex_usage?.five_hour },
    { key: "seven_day", label: tx("ui.7_day_usage"), value: result.codex_usage?.seven_day },
  ].filter((entry) => entry.value);
  const quotaLabel = result.quota_window ? quotaWindowLabel(result.quota_window, locale) : "";
  return (
    <div className={`inspection-quota${compact ? " is-compact" : ""}`}>
      <div><strong>{Number(result.usage_total_tokens ?? 0).toLocaleString(locale)}</strong><span>{tx("ui.total_tokens")}</span></div>
      {windows.map(({ key, label, value }) => value ? <div className="inspection-quota-window" key={key}><span>{label}<b>{Math.min(100, Math.max(0, value.used_percent)).toFixed(0)}%</b></span><progress max={100} value={Math.min(100, Math.max(0, value.used_percent))} />{value.reset_at ? <small>{tx("ui.quota_reset_at", { time: formatDateTime(value.reset_at) })}</small> : null}</div> : null)}
      {windows.length === 0 && quotaLabel ? <span className="inspection-quota-label">{quotaLabel}{result.recover_after ? ` · ${tx("ui.quota_reset_at", { time: formatDateTime(result.recover_after) })}` : ""}</span> : null}
      {windows.length === 0 && !quotaLabel ? <span className="inspection-quota-empty">-</span> : null}
    </div>
  );
}

function InspectionActionDialog({ result, reviewing, onClose, onModelTest, onDisable, onEnable, onDelete, onReview }: {
  result: InspectionResult;
  reviewing: boolean;
  onClose: () => void;
  onModelTest: () => void;
  onDisable: () => void;
  onEnable: () => void;
  onDelete: () => void;
  onReview: (action: "resolve" | "ignore" | "reopen") => void;
}) {
  const { locale, tx, formatDateTime } = useI18n();
  const reviewStatus = result.review_status || "pending";
  return (
    <Modal title={tx("ui.account_action")} onClose={onClose} footer={<button className="button" type="button" disabled={reviewing} onClick={onClose}>{tx("ui.close")}</button>}>
      <div className="inspection-action-dialog">
        <header><div><strong>{result.name || result.id}</strong><code>{result.id}</code></div><span className={`health-badge health-${result.health}`}><span />{healthLabel(result.health, locale)}</span></header>
        <dl>
          <div><dt>{tx("ui.http_status")}</dt><dd>{result.status_code ? `HTTP ${result.status_code}` : "-"}</dd></div>
          <div><dt>{tx("ui.decision")}</dt><dd>{reasonLabel(result.reason_code, locale)}</dd></div>
          <div><dt>{tx("ui.recommendation")}</dt><dd>{recommendationLabel(result.recommendation, locale)}</dd></div>
          <div><dt>{tx("ui.review_state")}</dt><dd>{reviewStatusLabel(reviewStatus, locale)}{result.reviewed_at ? ` · ${formatDateTime(result.reviewed_at)}` : ""}</dd></div>
          {result.quota_window ? <div><dt>{tx("ui.quota_and_usage")}</dt><dd>{quotaWindowLabel(result.quota_window, locale)}{result.recover_after ? ` · ${tx("ui.quota_reset_at", { time: formatDateTime(result.recover_after) })}` : ""}</dd></div> : null}
        </dl>
        {(result.status_code === 401 || result.status_code === 402 || result.health === "review") ? <p className="inspection-safety-note"><ShieldAlert size={17} />{tx("ui.review_safety_note")}</p> : null}
        <div className="inspection-action-grid">
          <button className="button button-primary" type="button" onClick={onModelTest}><Activity size={15} />{tx("ui.model_retest")}</button>
          <button className="button" type="button" disabled={!result.editable || result.disabled} onClick={onDisable}><XSquare size={15} />{tx("ui.disable")}</button>
          <button className="button" type="button" disabled={!result.editable || !result.disabled} onClick={onEnable}><CheckSquare2 size={15} />{tx("ui.enable")}</button>
          <button className="button button-danger" type="button" disabled={!result.editable} onClick={onDelete}><Trash2 size={15} />{tx("ui.delete")}</button>
        </div>
        {result.health === "review" ? <div className="inspection-review-actions">
          {reviewStatus === "pending" ? <>
            <button className="button button-quiet" type="button" disabled={reviewing} onClick={() => onReview("resolve")}>{tx("ui.mark_resolved")}</button>
            <button className="button button-quiet" type="button" disabled={reviewing} onClick={() => onReview("ignore")}>{tx("ui.ignore_result")}</button>
          </> : <button className="button button-quiet" type="button" disabled={reviewing} onClick={() => onReview("reopen")}>{tx("ui.reopen_review")}</button>}
        </div> : null}
      </div>
    </Modal>
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
  return <>{Array.from({ length: 5 }, (_, index) => <tr className="inspection-loading-row" key={index}><td colSpan={12}><span /></td></tr>)}</>;
}

function inspectionResultAccount(result: InspectionResult): Account {
  return {
    id: result.id,
    name: result.name || result.id,
    provider: result.provider,
    type: result.type,
    plan_type: result.plan_type,
    disabled: result.disabled,
    unavailable: result.health === "unavailable",
    runtime_only: !result.editable,
    proxy_configured: false,
    header_count: 0,
    editable: result.editable,
    success: 0,
    failed: result.failure_streak,
  };
}

function healthLabel(value: InspectionHealth | string | undefined, locale: Locale): string {
  const sources: Partial<Record<string, UIMessageKey>> = { healthy: "ui.healthy", quota_limited: "ui.quota_limited", invalid_credentials: "ui.invalid_credentials", deactivated: "ui.deactivated", review: "ui.needs_review", unavailable: "ui.unavailable", disabled: "ui.disabled", unknown: "ui.insufficient_evidence" };
  const source = value ? sources[value] : undefined;
  return translateUI(locale, source ?? "ui.insufficient_evidence");
}

function reasonLabel(value: string, locale: Locale): string {
  const source = ({
    healthy_recent_success: "ui.recent_request_succeeded", quota_exhausted: "ui.quota_exhausted", token_revoked: "ui.token_revoked", invalid_credentials: "ui.credentials_invalid_or_expired",
    account_deactivated: "ui.account_deactivated", workspace_deactivated: "ui.workspace_deactivated", authentication_review: "ui.authentication_needs_review",
    billing_review: "ui.billing_or_quota_needs_review", credential_permission_denied: "ui.credential_permission_denied", native_unavailable: "ui.cpa_marked_unavailable", manual_disabled: "ui.manually_disabled_2",
    transient_failure: "ui.temporary_upstream_failure", no_recent_evidence: "ui.no_recent_evidence",
    model_response_ok: "ui.model_response_is_healthy", authentication_failed: "ui.authentication_failed",
    quota_limited: "ui.upstream_quota_or_rate_limited_2", model_not_found: "ui.model_unavailable_or_missing",
    request_timeout: "ui.model_test_timed_out", upstream_unavailable: "ui.upstream_service_unavailable",
    invalid_response: "ui.could_not_validate_upstream_response", unsupported_provider: "ui.provider_unsupported",
    unconfirmed_upstream_response: "ui.could_not_validate_upstream_response", passive_circuit_open: "ui.passive_temporary_circuit",
  } satisfies Record<string, UIMessageKey>)[value];
  return source ? translateUI(locale, source) : value;
}

function signalSourceLabel(value: NonNullable<InspectionResult["signal_source"]>, locale: Locale): string {
  return translateUI(locale, ({ native: "ui.cpa_native_evidence", passive: "ui.passive_request_evidence", active_probe: "ui.active_model_probe_evidence" } satisfies Record<NonNullable<InspectionResult["signal_source"]>, UIMessageKey>)[value]);
}

function sweepSourceLabel(value: NonNullable<InspectionSnapshot["probe_sweep_source"]>, locale: Locale): string {
  return translateUI(locale, ({ manual: "ui.manual_full_inspection", scheduled: "ui.scheduled_full_inspection", anomaly: "ui.anomaly_triggered_inspection" } satisfies Record<NonNullable<InspectionSnapshot["probe_sweep_source"]>, UIMessageKey>)[value]);
}

function sweepStatusLabel(value: NonNullable<InspectionSnapshot["probe_sweep_status"]>, locale: Locale): string {
  return translateUI(locale, ({ running: "ui.inspection_running", completed: "ui.inspection_completed", failed: "ui.inspection_failed", waiting_for_auth: "ui.inspection_waiting_for_auth", stopped: "ui.stopped" } satisfies Record<NonNullable<InspectionSnapshot["probe_sweep_status"]>, UIMessageKey>)[value]);
}

function runModeLabel(value: InspectionSnapshot["run_mode"], locale: Locale): string {
  const key = ({ native: "ui.quick_native_inspection", full: "ui.full_inspection", incremental: "ui.incremental_inspection", scoped: "ui.reinspect_selected", retry: "ui.retry_review_accounts" } satisfies Record<NonNullable<InspectionSnapshot["run_mode"]>, UIMessageKey>)[value || "full"];
  return translateUI(locale, key);
}

function probePhaseLabel(value: InspectionSnapshot["probe_phase"], locale: Locale): string {
  const key = ({ listing: "ui.inspection_phase_listing", primary: "ui.inspection_phase_primary", retry: "ui.inspection_phase_retry", stopped: "ui.inspection_phase_stopped", completed: "ui.inspection_phase_completed" } satisfies Record<NonNullable<InspectionSnapshot["probe_phase"]>, UIMessageKey>)[value || "listing"];
  return translateUI(locale, key);
}

function reviewStatusLabel(value: NonNullable<InspectionResult["review_status"]>, locale: Locale): string {
  return translateUI(locale, ({ pending: "ui.pending_review", resolved: "ui.review_resolved", ignored: "ui.review_ignored" } satisfies Record<NonNullable<InspectionResult["review_status"]>, UIMessageKey>)[value]);
}

function confidenceLabel(value: string, locale: Locale): string {
  return translateUI(locale, value === "high" ? "ui.high_confidence" : value === "medium" ? "ui.medium_confidence" : "ui.low_confidence");
}

function quotaWindowLabel(value: NonNullable<InspectionResult["quota_window"]>, locale: Locale): string {
  return translateUI(locale, ({
    five_hour: "ui.quota_window_five_hour",
    seven_day: "ui.quota_window_seven_day",
    multiple: "ui.quota_window_multiple",
    five_hour_fallback: "ui.quota_window_five_hour_fallback",
  } satisfies Record<NonNullable<InspectionResult["quota_window"]>, UIMessageKey>)[value]);
}

function recommendationLabel(value: InspectionResult["recommendation"], locale: Locale): string {
  return translateUI(locale, ({ keep: "ui.keep", reauth: "ui.reauthorize", review: "ui.manual_review", disable: "ui.disable", enable: "ui.enable", delete: "ui.delete" } satisfies Record<InspectionResult["recommendation"], UIMessageKey>)[value]);
}

function actionLabel(value: string | undefined, locale: Locale): string {
  const source = ({ disable: "ui.auto_disable", enable: "ui.auto_enable", delete: "ui.auto_delete", delete_candidate: "ui.pending_deletion_2", review_resolve: "ui.mark_resolved", review_ignore: "ui.ignore_result", review_reopen: "ui.reopen_review" } satisfies Record<string, UIMessageKey>)[value || ""] || "ui.not_run";
  return translateUI(locale, source);
}

function actionStatusLabel(value: string | undefined, owned: boolean, locale: Locale): string {
  const source = ({ pending: "ui.pending_2", succeeded: "ui.completed_2", failed: "ui.waiting_to_retry", skipped: "ui.skipped_2" } satisfies Record<string, UIMessageKey>)[value || ""] || (owned ? "ui.inspection_owns_disable" : "ui.record_only");
  return translateUI(locale, source);
}

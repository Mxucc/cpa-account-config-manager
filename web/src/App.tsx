import {
  Activity,
  ChevronLeft,
  ChevronRight,
  Download,
  Eye,
  FileCog,
  Github,
  LoaderCircle,
  LockKeyhole,
  Power,
  PowerOff,
  Pencil,
  RefreshCw,
  Search,
  ScrollText,
  Settings2,
  ShieldCheck,
  SlidersHorizontal,
  Trash2,
  UserPlus,
  Wifi,
  WifiOff,
  X,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import * as api from "./api/client";
import { AccountDetailsDialog } from "./components/AccountDetailsDialog";
import { BatchEditor } from "./components/BatchEditor";
import { AccountUsageCell } from "./components/AccountUsageCell";
import { ExportDialog } from "./components/ExportDialog";
import { ForceSyncPreviewDialog } from "./components/ForceSyncPreviewDialog";
import { IconButton } from "./components/IconButton";
import { ImportDialog } from "./components/ImportDialog";
import { InspectionWorkspace } from "./components/InspectionWorkspace";
import { OperationLogWorkspace } from "./components/OperationLogWorkspace";
import { OtherSettingsWorkspace } from "./components/OtherSettingsWorkspace";
import { JobPanel, jobStateLabel } from "./components/JobPanel";
import { LoginDialog } from "./components/LoginDialog";
import { ModelTestDialog } from "./components/ModelTestDialog";
import { Modal } from "./components/Modal";
import { PolicyDialog } from "./components/PolicyDialog";
import { PreviewDialog } from "./components/PreviewDialog";
import { DeleteAccountDialog } from "./components/DeleteAccountDialog";
import { operatorMessage } from "./format/operatorMessage";
import { accountState, accountStateLabel, technicalLabel } from "./format/accountDisplay";
import { accountAutomationPresentation } from "./format/accountAutomation";
import { useI18n, type Locale } from "./i18n";
import { translateUI, type UIMessageKey } from "./i18n/uiText";
import {
  ACCOUNT_PAGE_SIZE_OPTIONS,
  DEFAULT_ACCOUNT_PAGE_SIZE,
  isAccountPageSize,
  readAccountPageSize,
  writeAccountPageSize,
} from "./store/accountPageSize";
import {
  EMPTY_ACCOUNT_FILTERS,
  readAccountFilters,
  writeAccountFilters,
  type PersistedAccountFilters,
} from "./store/accountFilters";
import { readPanelAuth } from "./store/panelAuth";
import { clearSession, setSession } from "./store/session";
import type {
  Account,
  AccountDeletePreview,
  AccountExportFormat,
  AccountFilters,
  AccountListResponse,
  BatchPatch,
  BatchPreview,
  DefaultPolicy,
  ForceSyncJobSnapshot,
  ForceSyncPreview,
  ExportFormat,
  ImportPreview,
  ImportResult,
  JobSnapshot,
  ModelTestResult,
  PolicySnapshot,
  ResultExportFormat,
  TargetScope,
} from "./types";

const exportFormatLabels: Record<ExportFormat, string> = {
  cpa: "CPA",
  sub2api: "sub2api",
  cockpit: "Cockpit",
  "9router": "9router",
  codex: "Codex",
  axonhub: "AxonHub",
  codexmanager: "Codex-Manager",
  json: "JSON",
  csv: "CSV",
  jsonl: "JSON Lines",
};

function formatLabel(format: ExportFormat): string {
  return exportFormatLabels[format];
}

const providerOptions = [
  "antigravity",
  "aistudio",
  "claude",
  "codex",
  "gemini",
  "gemini-cli",
  "gemini-interactions",
  "kimi",
  "openai",
  "openai-compatible-kimi",
  "qwen",
  "vertex",
  "xai",
];

const typeOptions = [
  "free",
  "plus",
  "pro",
  "team",
  "business",
  "enterprise",
  "edu",
  "k12",
  "oauth",
  "api_key",
];

type FilterState = PersistedAccountFilters;

interface EditorContext {
  title: UIMessageKey;
  scopeLabel: string;
  scope?: TargetScope;
}

export default function App() {
  const { locale, tx, formatDateTime } = useI18n();
  const [authState, setAuthState] = useState<"booting" | "login" | "ready">("booting");
  const [activeView, setActiveView] = useState<"accounts" | "inspection" | "operations" | "settings">("accounts");
  const [authLoading, setAuthLoading] = useState(false);
  const [authError, setAuthError] = useState("");
  const [filters, setFilters] = useState<FilterState>(readAccountFilters);
  const [searchDraft, setSearchDraft] = useState(() => readAccountFilters().search);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(readAccountPageSize);
  const [data, setData] = useState<AccountListResponse>({ accounts: [], total: 0, page: 1, page_size: DEFAULT_ACCOUNT_PAGE_SIZE, pages: 0 });
  const [loading, setLoading] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [scopeMode, setScopeMode] = useState<"selected" | "filtered">("filtered");
  const [editorContext, setEditorContext] = useState<EditorContext | null>(null);
  const [detailAccount, setDetailAccount] = useState<Account | null>(null);
  const [modelTestTarget, setModelTestTarget] = useState<Account | null>(null);
  const [modelTestResult, setModelTestResult] = useState<ModelTestResult | null>(null);
  const [modelTesting, setModelTesting] = useState(false);
  const [modelTestError, setModelTestError] = useState("");
  const [deleteTarget, setDeleteTarget] = useState<Account | null>(null);
  const [deletePreview, setDeletePreview] = useState<AccountDeletePreview | null>(null);
  const [deletePreviewing, setDeletePreviewing] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState("");
  const [preview, setPreview] = useState<BatchPreview | null>(null);
  const [previewError, setPreviewError] = useState("");
  const [previewLoading, setPreviewLoading] = useState(false);
  const [starting, setStarting] = useState(false);
  const [job, setJob] = useState<JobSnapshot | null>(null);
  const [jobOpen, setJobOpen] = useState(false);
  const [retrying, setRetrying] = useState(false);
  const [policyOpen, setPolicyOpen] = useState(false);
  const [policySnapshot, setPolicySnapshot] = useState<PolicySnapshot | null>(null);
  const [policyLoading, setPolicyLoading] = useState(false);
  const [policySaving, setPolicySaving] = useState(false);
  const [policyScanning, setPolicyScanning] = useState(false);
  const [policyError, setPolicyError] = useState("");
  const [forcePreview, setForcePreview] = useState<ForceSyncPreview | null>(null);
  const [forcePreviewLoading, setForcePreviewLoading] = useState(false);
  const [forcePreviewError, setForcePreviewError] = useState("");
  const [forceStarting, setForceStarting] = useState(false);
  const [forceJob, setForceJob] = useState<ForceSyncJobSnapshot | null>(null);
  const [forceJobOpen, setForceJobOpen] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [importPreview, setImportPreview] = useState<ImportPreview | null>(null);
  const [importResult, setImportResult] = useState<ImportResult | null>(null);
  const [importPreviewing, setImportPreviewing] = useState(false);
  const [importStarting, setImportStarting] = useState(false);
  const [importError, setImportError] = useState("");
  const [importUsageCollectionActive, setImportUsageCollectionActive] = useState(false);
  const [inspectionAccountSyncActive, setInspectionAccountSyncActive] = useState(false);
  const [exportTarget, setExportTarget] = useState<"accounts" | "results" | null>(null);
  const [accountExportScope, setAccountExportScope] = useState<TargetScope | null>(null);
  const [exporting, setExporting] = useState(false);
  const [exportError, setExportError] = useState("");
  const [notice, setNotice] = useState("");
  const accountRequest = useRef(0);
  const deleteRequest = useRef(0);

  const apiFilters = useMemo<AccountFilters>(() => ({
    ...(filters.search ? { search: filters.search } : {}),
    ...(filters.provider ? { provider: filters.provider } : {}),
    ...(filters.type ? { type: filters.type } : {}),
    ...(filters.status ? { status: filters.status } : {}),
    ...(filters.disabled ? { disabled: filters.disabled === "true" } : {}),
    ...(filters.editability ? { editability: filters.editability } : {}),
  }), [filters]);

  useEffect(() => {
    if (filters.search === searchDraft) return;
    const timer = window.setTimeout(() => {
      setFilters((current) => current.search === searchDraft ? current : { ...current, search: searchDraft });
      setPage(1);
      setSelected(new Set());
    }, 250);
    return () => window.clearTimeout(timer);
  }, [filters.search, searchDraft]);

  useEffect(() => {
    writeAccountFilters({ ...filters, search: searchDraft });
  }, [filters, searchDraft]);

  useEffect(() => {
    let active = true;
    const bootstrap = async () => {
      const panelAuth = readPanelAuth();
      if (!panelAuth) {
        if (active) setAuthState("login");
        return;
      }
      setSession(panelAuth.apiBase, panelAuth.managementKey);
      try {
        await api.verifySession();
        try {
          await api.persistCurrentSettings();
        } catch (error) {
          if (error instanceof api.APIError && error.status === 401) throw error;
          if (active) setNotice(operatorMessage(error instanceof Error ? error.message : "ui.settings_persistence_failed", locale));
        }
        if (active) setAuthState("ready");
      } catch {
        clearSession();
        if (active) setAuthState("login");
      }
    };
    void bootstrap();
    return () => { active = false; };
  }, []);

  const handleAPIError = useCallback((error: unknown) => {
    if (error instanceof api.APIError && error.status === 401) {
      clearSession();
      setAuthState("login");
      setAuthError(operatorMessage(error.message, locale));
      return;
    }
    setNotice(errorText(error, locale));
  }, [locale]);

  const refreshAccounts = useCallback(async (silent = false, requestedPage = page, requestedFilters: AccountFilters = apiFilters) => {
    if (authState !== "ready") return;
    const requestID = accountRequest.current + 1;
    accountRequest.current = requestID;
    if (!silent) setLoading(true);
    try {
      const response = await api.listAccounts(requestedPage, pageSize, requestedFilters);
      if (requestID !== accountRequest.current) return;
      setData(response);
      if (response.pages > 0 && requestedPage > response.pages) setPage(response.pages);
    } catch (error) {
      if (requestID !== accountRequest.current) return;
      handleAPIError(error);
    } finally {
      if (requestID === accountRequest.current) setLoading(false);
    }
  }, [apiFilters, authState, handleAPIError, page, pageSize]);

  const requestInspectionAccountSync = useCallback(() => {
    setInspectionAccountSyncActive(true);
    void refreshAccounts(true);
  }, [refreshAccounts]);

  useEffect(() => {
    if (activeView === "accounts") void refreshAccounts();
  }, [activeView, refreshAccounts]);

  useEffect(() => {
    if (!importUsageCollectionActive || authState !== "ready") return;
    let cancelled = false;
    let timer = 0;
    const poll = async () => {
      try {
        const snapshot = await api.getInspection();
        if (cancelled) return;
        await refreshAccounts(true);
        if (cancelled) return;
        if (snapshot.running || snapshot.pending || snapshot.probe_sweep_remaining > 0) {
          timer = window.setTimeout(poll, 1500);
        } else {
          setImportUsageCollectionActive(false);
        }
      } catch (error) {
        if (!cancelled) {
          setImportUsageCollectionActive(false);
          handleAPIError(error);
        }
      }
    };
    timer = window.setTimeout(poll, 700);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [authState, handleAPIError, importUsageCollectionActive, refreshAccounts]);

  useEffect(() => {
    if (!inspectionAccountSyncActive || authState !== "ready") return;
    let cancelled = false;
    let timer = 0;
    const poll = async () => {
      try {
        const snapshot = await api.getInspection();
        if (cancelled) return;
        await refreshAccounts(true);
        if (cancelled) return;
        if (snapshot.running || snapshot.pending || snapshot.probe_sweep_status === "running") {
          timer = window.setTimeout(poll, 1200);
        } else {
          setInspectionAccountSyncActive(false);
        }
      } catch (error) {
        if (!cancelled) {
          setInspectionAccountSyncActive(false);
          handleAPIError(error);
        }
      }
    };
    timer = window.setTimeout(poll, 400);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [authState, handleAPIError, inspectionAccountSyncActive, refreshAccounts]);

  useEffect(() => {
    if (authState !== "ready") return;
    void api.getJobStatus(true).then((snapshot) => {
      if (snapshot.id) setJob(snapshot);
    }).catch(() => undefined);
  }, [authState]);

  useEffect(() => {
    if (!job?.running) return;
    let cancelled = false;
    let timer = 0;
    const poll = async () => {
      try {
        const light = await api.getJobStatus(false);
        if (cancelled) return;
        if (light.running) {
          setJob((current) => ({ ...light, results: current?.results }));
          timer = window.setTimeout(poll, 850);
          return;
        }
        const full = await api.getJobStatus(true);
        if (!cancelled) {
          setJob(full);
          void refreshAccounts();
        }
      } catch (error) {
        if (!cancelled) handleAPIError(error);
      }
    };
    timer = window.setTimeout(poll, 500);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [handleAPIError, job?.id, job?.running, refreshAccounts]);

  useEffect(() => {
    if (!forceJob?.running) return;
    let cancelled = false;
    let timer = 0;
    const poll = async () => {
      try {
        const light = await api.getForceSyncStatus(false);
        if (cancelled) return;
        if (light.running) {
          setForceJob((current) => ({ ...light, results: current?.results }));
          timer = window.setTimeout(poll, 850);
          return;
        }
        const full = await api.getForceSyncStatus(true);
        if (!cancelled) {
          setForceJob(full);
          void refreshAccounts();
          void api.getDefaultPolicy().then(setPolicySnapshot).catch(() => undefined);
        }
      } catch (error) {
        if (!cancelled) handleAPIError(error);
      }
    };
    timer = window.setTimeout(poll, 500);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [forceJob?.id, forceJob?.running, handleAPIError, refreshAccounts]);

  useEffect(() => {
    if (!policyOpen || authState !== "ready" || !policySnapshot) return;
    let cancelled = false;
    let polling = false;
    const refresh = async () => {
      if (polling) return;
      polling = true;
      try {
        const snapshot = await api.getDefaultPolicy();
        if (!cancelled) setPolicySnapshot(snapshot);
      } catch (error) {
        if (!cancelled && error instanceof api.APIError && error.status === 401) handleAPIError(error);
      } finally {
        polling = false;
      }
    };
    const timer = window.setInterval(() => void refresh(), 2000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [authState, handleAPIError, policyOpen, policySnapshot !== null]);

  const login = async (baseURL: string, managementKey: string) => {
    setAuthLoading(true);
    setAuthError("");
    setSession(baseURL, managementKey);
    try {
      await api.verifySession();
      try {
        await api.persistCurrentSettings();
      } catch (error) {
        if (error instanceof api.APIError && error.status === 401) throw error;
        setNotice(operatorMessage(error instanceof Error ? error.message : "ui.settings_persistence_failed", locale));
      }
      setAuthState("ready");
    } catch (error) {
      clearSession();
      setAuthError(error instanceof Error ? operatorMessage(error.message, locale) : tx("ui.authentication_failed"));
    } finally {
      setAuthLoading(false);
    }
  };

  const updateFilter = (key: keyof FilterState, value: string) => {
    setFilters((current) => ({ ...current, [key]: value }));
    setPage(1);
    setSelected(new Set());
  };

  const updatePageSize = (value: string) => {
    const nextPageSize = Number(value);
    if (!isAccountPageSize(nextPageSize) || nextPageSize === pageSize) return;
    writeAccountPageSize(nextPageSize);
    setPageSize(nextPageSize);
    setPage(1);
  };

  const toggleAccount = (account: Account) => {
    if (!account.editable) return;
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(account.id)) next.delete(account.id);
      else next.add(account.id);
      return next;
    });
    setScopeMode("selected");
  };

  const editablePageIDs = data.accounts.filter((account) => account.editable).map((account) => account.id);
  const allPageSelected = editablePageIDs.length > 0 && editablePageIDs.every((id) => selected.has(id));
  const togglePage = () => {
    setSelected((current) => {
      const next = new Set(current);
      if (allPageSelected) editablePageIDs.forEach((id) => next.delete(id));
      else editablePageIDs.forEach((id) => next.add(id));
      return next;
    });
    setScopeMode("selected");
  };

  const targetScope = () => {
    if (scopeMode === "selected" && selected.size > 0) {
      return { mode: "selected" as const, ids: Array.from(selected) };
    }
    return { mode: "filtered" as const, filters: apiFilters };
  };

  const beginPreview = async (patch: BatchPatch, explicitScope?: TargetScope) => {
    setPreviewLoading(true);
    setPreviewError("");
    try {
      const response = await api.createPreview(explicitScope ?? targetScope(), patch);
      setPreview(response);
    } catch (error) {
      handleAPIError(error);
    } finally {
      setPreviewLoading(false);
    }
  };

  const openAccountEditor = (account: Account) => {
    if (!account.editable) return;
    setDetailAccount(null);
    setEditorContext({
      title: "ui.edit_account",
      scopeLabel: account.label || account.email || account.name || account.id,
      scope: { mode: "selected", ids: [account.id] },
    });
  };

  const openModelTest = (account: Account) => {
    setModelTestTarget(account);
    setModelTestResult(null);
    setModelTestError("");
  };

  const closeModelTest = () => {
    if (modelTesting) return;
    setModelTestTarget(null);
    setModelTestResult(null);
    setModelTestError("");
  };

  const runModelTest = async (model: string) => {
    if (!modelTestTarget) return;
    setModelTesting(true);
    setModelTestError("");
    setModelTestResult(null);
    try {
      setModelTestResult(await api.testAccountModel(modelTestTarget.id, model));
    } catch (error) {
      if (error instanceof api.APIError && error.status === 401) {
        setModelTestTarget(null);
        handleAPIError(error);
      } else {
        setModelTestError(errorText(error, locale));
      }
    } finally {
      setModelTesting(false);
    }
  };

  const closeDelete = () => {
    deleteRequest.current += 1;
    setDeleteTarget(null);
    setDeletePreview(null);
    setDeletePreviewing(false);
    setDeleting(false);
    setDeleteError("");
  };

  const openDelete = async (account: Account) => {
    if (!account.editable) return;
    const requestID = deleteRequest.current + 1;
    deleteRequest.current = requestID;
    setDeleteTarget(account);
    setDeletePreview(null);
    setDeleteError("");
    setDeletePreviewing(true);
    try {
      const response = await api.createAccountDeletePreview(account.id);
      if (requestID === deleteRequest.current) setDeletePreview(response);
    } catch (error) {
      if (requestID !== deleteRequest.current) return;
      if (error instanceof api.APIError && error.status === 401) {
        closeDelete();
        handleAPIError(error);
      } else {
        setDeleteError(errorText(error, locale));
      }
    } finally {
      if (requestID === deleteRequest.current) setDeletePreviewing(false);
    }
  };

  const confirmDelete = async () => {
    if (!deletePreview) return;
    const deletedID = deletePreview.account.id;
    setDeleting(true);
    setDeleteError("");
    try {
      const result = await api.deleteAccount(deletePreview.id);
      closeDelete();
      setSelected((current) => {
        const next = new Set(current);
        next.delete(deletedID);
        return next;
      });
      setNotice(tx("ui.deleted_account_account", { account: result.account.label || result.account.email || result.account.name }));
      await refreshAccounts();
    } catch (error) {
      if (error instanceof api.APIError && error.status === 401) {
        closeDelete();
        handleAPIError(error);
      } else {
        setDeleteError(errorText(error, locale));
      }
    } finally {
      setDeleting(false);
    }
  };

  const confirmPreview = async () => {
    if (!preview) return;
    setStarting(true);
    setPreviewError("");
    try {
      const snapshot = await api.startBatch(preview.id);
      setPreview(null);
      setJob(snapshot);
      setForceJobOpen(false);
      setJobOpen(true);
    } catch (error) {
      if (error instanceof api.APIError && error.status === 401) {
        handleAPIError(error);
      } else {
        setPreviewError(errorText(error, locale));
      }
    } finally {
      setStarting(false);
    }
  };

  const retryJob = async () => {
    setRetrying(true);
    try {
      const snapshot = await api.retryBatch();
      setJob(snapshot);
      setForceJobOpen(false);
      setJobOpen(true);
    } catch (error) {
      handleAPIError(error);
    } finally {
      setRetrying(false);
    }
  };

  const refreshJob = async () => {
    try {
      setJob(await api.getJobStatus(true));
    } catch (error) {
      handleAPIError(error);
    }
  };

  const openPolicy = async () => {
    setPolicyOpen(true);
    setPolicyLoading(true);
    setPolicyError("");
    try {
      const [snapshot, lastForceJob] = await Promise.all([
        api.getDefaultPolicy(),
        api.getForceSyncStatus(true),
      ]);
      setPolicySnapshot(snapshot);
      if (lastForceJob.id) setForceJob(lastForceJob);
    } catch (error) {
      if (error instanceof api.APIError && error.status === 401) {
        setPolicyOpen(false);
        handleAPIError(error);
      } else {
        setPolicyError(errorText(error, locale));
      }
    } finally {
      setPolicyLoading(false);
    }
  };

  const savePolicy = async (policy: DefaultPolicy) => {
    setPolicySaving(true);
    setPolicyError("");
    try {
      setPolicySnapshot(await api.saveDefaultPolicy(policy));
    } catch (error) {
      if (error instanceof api.APIError && error.status === 401) handleAPIError(error);
      else setPolicyError(errorText(error, locale));
    } finally {
      setPolicySaving(false);
    }
  };

  const scanPolicy = async () => {
    setPolicyScanning(true);
    setPolicyError("");
    try {
      const accepted = await api.scanDefaultPolicy();
      setPolicySnapshot({ ...accepted, running: true });
    } catch (error) {
      if (error instanceof api.APIError && error.status === 401) handleAPIError(error);
      else setPolicyError(errorText(error, locale));
    } finally {
      setPolicyScanning(false);
    }
  };

  const previewForceSync = async () => {
    setForcePreviewLoading(true);
    setPolicyError("");
    setForcePreviewError("");
    try {
      setForcePreview(await api.createForceSyncPreview());
      setPolicyOpen(false);
    } catch (error) {
      if (error instanceof api.APIError && error.status === 401) handleAPIError(error);
      else setPolicyError(errorText(error, locale));
    } finally {
      setForcePreviewLoading(false);
    }
  };

  const confirmForceSync = async () => {
    if (!forcePreview) return;
    setForceStarting(true);
    setForcePreviewError("");
    try {
      const snapshot = await api.startForceSync(forcePreview.id);
      setForcePreview(null);
      setForceJob(snapshot);
      setJobOpen(false);
      setForceJobOpen(true);
    } catch (error) {
      if (error instanceof api.APIError && error.status === 401) handleAPIError(error);
      else setForcePreviewError(errorText(error, locale));
    } finally {
      setForceStarting(false);
    }
  };

  const refreshForceJob = async () => {
    try {
      setForceJob(await api.getForceSyncStatus(true));
    } catch (error) {
      handleAPIError(error);
    }
  };

  const openImport = () => {
    setImportPreview(null);
    setImportResult(null);
    setImportError("");
    setImportOpen(true);
  };

  const closeImport = () => {
    setImportOpen(false);
    setImportPreview(null);
    setImportResult(null);
    setImportError("");
  };

  const resetImport = () => {
    setImportPreview(null);
    setImportResult(null);
    setImportError("");
  };

  const previewImport = async (files: File[]) => {
    setImportPreviewing(true);
    setImportError("");
    setImportResult(null);
    try {
      setImportPreview(await api.createImportPreview(files));
    } catch (error) {
      if (error instanceof api.APIError && error.status === 401) {
        closeImport();
        handleAPIError(error);
      } else {
        setImportError(errorText(error, locale));
      }
    } finally {
      setImportPreviewing(false);
    }
  };

  const confirmImport = async () => {
    if (!importPreview) return;
    setImportStarting(true);
    setImportError("");
    try {
      const result = await api.startImport(importPreview.id);
      setImportPreview(null);
      setImportResult(result);
      setImportUsageCollectionActive(Boolean(result.usage_collection_started));
      setNotice(result.failed || result.skipped
        ? tx("ui.added_count_accounts_failed_not_written", { count: result.imported, failed: result.failed + result.skipped })
        : tx("ui.added_count_accounts", { count: result.imported }));
      void refreshAccounts();
    } catch (error) {
      if (error instanceof api.APIError && error.status === 401) {
        closeImport();
        handleAPIError(error);
      } else {
        setImportError(errorText(error, locale));
      }
    } finally {
      setImportStarting(false);
    }
  };

  const openExport = (target: "accounts" | "results", scope?: TargetScope) => {
    setExportTarget(target);
    setAccountExportScope(target === "accounts" ? scope ?? { mode: "filtered", filters: apiFilters } : null);
    setExportError("");
  };

  const closeExport = () => {
    setExportTarget(null);
    setAccountExportScope(null);
    setExportError("");
  };

  const confirmExport = async (format: ExportFormat) => {
    if (!exportTarget) return;
    setExporting(true);
    setExportError("");
    try {
      const result = exportTarget === "accounts"
        ? await api.downloadExport("accounts", format as AccountExportFormat, accountExportScope ?? { mode: "filtered", filters: apiFilters })
        : await api.downloadExport("results", format as ResultExportFormat);
      if (exportTarget === "accounts") {
        setNotice(tx("ui.downloaded_format_credentials_for_count_accounts_skipped_skipped", {
          format: formatLabel(format),
          count: result.exported ?? 0,
          skipped: result.skipped ?? 0,
        }));
      } else {
        setNotice(tx("ui.exported_results_as_format", { format: formatLabel(format) }));
      }
      closeExport();
    } catch (error) {
      if (error instanceof api.APIError && error.status === 401) {
        closeExport();
        handleAPIError(error);
      } else {
        setExportError(errorText(error, locale));
      }
    } finally {
      setExporting(false);
    }
  };

  const scopeLabel = scopeMode === "selected" && selected.size > 0
    ? tx("ui.count_selected_accounts", { count: selected.size })
    : tx("ui.count_filtered_accounts", { count: data.total });
  const exportCount = exportTarget === "accounts"
    ? accountExportScope?.mode === "selected" ? accountExportScope.ids?.length ?? 0 : data.total
    : job?.results?.length ?? job?.done ?? 0;
  const exportScopeLabel = exportTarget === "accounts" && accountExportScope?.mode === "selected"
    ? tx("ui.selected_accounts")
    : undefined;
  const hasActiveFilters = Object.values(filters).some(Boolean) || Boolean(searchDraft);
  const panelOpen = Boolean(jobOpen && job || forceJobOpen && forceJob);

  return (
    <div className={`app-shell ${panelOpen ? "with-job-panel" : ""}`}>
      <div className="page-frame">
        <header className="app-header">
          <div className="brand-block">
            <span className="brand-icon"><FileCog size={21} /></span>
            <div><h1>{tx("ui.account_management")}</h1><span>CPA Account Config Manager</span></div>
          </div>
        </header>

        <div className="workspace-bar">
          <nav className="workspace-tabs" aria-label={tx("ui.account_management_views")}>
            <button type="button" className={activeView === "accounts" ? "active" : ""} aria-current={activeView === "accounts" ? "page" : undefined} onClick={() => setActiveView("accounts")}><FileCog size={16} />{tx("ui.accounts")}</button>
            <button type="button" className={activeView === "inspection" ? "active" : ""} aria-current={activeView === "inspection" ? "page" : undefined} onClick={() => setActiveView("inspection")}><Activity size={16} />{tx("ui.inspection_and_automation")}</button>
            <button type="button" className={activeView === "operations" ? "active" : ""} aria-current={activeView === "operations" ? "page" : undefined} onClick={() => setActiveView("operations")}><ScrollText size={16} />{tx("ui.operation_log")}</button>
            <button type="button" className={activeView === "settings" ? "active" : ""} aria-current={activeView === "settings" ? "page" : undefined} onClick={() => setActiveView("settings")}><Settings2 size={16} />{tx("ui.other_settings")}</button>
          </nav>
          <div className="workspace-controls">
            <div className="header-status">
              <span><ShieldCheck size={15} />{tx("ui.count_accounts", { count: data.total })}</span>
              {job?.id ? <button type="button" onClick={() => { setForceJobOpen(false); setJobOpen(true); }}><Activity size={15} />{job.running ? `${job.done}/${job.total}` : jobStateLabel(job.state)}</button> : null}
              {forceJob?.id ? <button type="button" onClick={() => { setJobOpen(false); setForceJobOpen(true); }}><RefreshCw size={15} />{forceJob.running ? `${forceJob.done}/${forceJob.total}` : jobStateLabel(forceJob.state)}</button> : null}
            </div>
            <div className="header-actions">
              {job?.id ? <IconButton className="mobile-job-action" label={tx("ui.open_batch_job")} onClick={() => { setForceJobOpen(false); setJobOpen(true); }}><Activity size={17} /></IconButton> : null}
              {forceJob?.id ? <IconButton className="mobile-job-action" label={tx("ui.open_force_sync_job")} onClick={() => { setJobOpen(false); setForceJobOpen(true); }}><RefreshCw size={17} /></IconButton> : null}
              {activeView === "accounts" ? <>
                <button className="button button-primary header-add-account" type="button" title={tx("ui.add_accounts")} aria-label={tx("ui.add_accounts")} onClick={openImport}><UserPlus size={16} /><span>{tx("ui.add_accounts")}</span></button>
                <IconButton label={tx("ui.default_policy")} onClick={() => void openPolicy()}><Settings2 size={17} /></IconButton>
                <IconButton className="export-action" label={tx("ui.download_filtered_credentials")} onClick={() => openExport("accounts")}><Download size={17} /></IconButton>
                <IconButton label={tx("ui.refresh_accounts")} onClick={() => void refreshAccounts()} disabled={loading}><RefreshCw className={loading ? "spin" : ""} size={17} /></IconButton>
              </> : null}
              <a className="icon-button" href="https://github.com/Mxucc/cpa-account-config-manager/" target="_blank" rel="noopener noreferrer" aria-label={tx("ui.open_project_on_github")} title={tx("ui.open_project_on_github")}><Github size={17} /></a>
            </div>
          </div>
        </div>

        {notice ? <div className="notice-bar global-notice" role="alert"><span>{notice}</span><IconButton label={tx("ui.dismiss_notification")} onClick={() => setNotice("")}><X size={15} /></IconButton></div> : null}

        {activeView === "accounts" ? (
        <section className="account-panel">
          <section className="filter-bar" aria-label={tx("ui.account_filters")}>
            <div className="filter-heading">
              <div><strong>{tx("ui.filter_accounts")}</strong><span>{tx(hasActiveFilters ? "ui.filters_applied" : "ui.all_accounts")}</span></div>
              <button className="button button-quiet reset-filters" type="button" disabled={!hasActiveFilters} onClick={() => { setFilters({ ...EMPTY_ACCOUNT_FILTERS }); setSearchDraft(""); setPage(1); setSelected(new Set()); void refreshAccounts(false, 1, {}); }}>
                {tx("ui.reset")}
              </button>
            </div>
            <div className="filter-grid">
              <div className="filter-control filter-search-control">
                <span>{tx("ui.search")}</span>
                <label className="search-box">
                  <Search size={16} />
                  <input value={searchDraft} onChange={(event) => setSearchDraft(event.target.value)} placeholder={tx("ui.account_email_filename_or_type")} aria-label={tx("ui.search_accounts")} />
                  {searchDraft ? <button type="button" aria-label={tx("ui.clear_search")} onClick={() => setSearchDraft("")}><X size={14} /></button> : null}
                </label>
              </div>
              <label className="filter-control">
                <span>{tx("ui.provider")}</span>
                <input list="provider-options" value={filters.provider} onChange={(event) => updateFilter("provider", event.target.value)} placeholder={tx("ui.all_providers")} aria-label={tx("ui.provider")} />
              </label>
              <datalist id="provider-options">
                {providerOptions.map((provider) => <option key={provider} value={provider} />)}
              </datalist>
              <label className="filter-control">
                <span>{tx("ui.type")}</span>
                <input list="type-options" value={filters.type} onChange={(event) => updateFilter("type", event.target.value)} placeholder={tx("ui.all_types")} aria-label={tx("ui.type")} />
              </label>
              <datalist id="type-options">
                {typeOptions.map((type) => <option key={type} value={type} />)}
              </datalist>
              <label className="filter-control">
                <span>{tx("ui.status")}</span>
                <select value={filters.status} onChange={(event) => updateFilter("status", event.target.value)} aria-label={tx("ui.status")}>
                  <option value="">{tx("ui.all_statuses")}</option>
                  <option value="active">{tx("ui.active")}</option>
                  <option value="disabled">{tx("ui.disabled")}</option>
                  <option value="error">{tx("ui.error")}</option>
                  <option value="unavailable">{tx("ui.temporarily_unavailable")}</option>
                </select>
              </label>
              <label className="filter-control">
                <span>{tx("ui.enabled_state")}</span>
                <select value={filters.disabled} onChange={(event) => updateFilter("disabled", event.target.value)} aria-label={tx("ui.enabled_state")}>
                  <option value="">{tx("ui.enabled_and_disabled")}</option>
                  <option value="false">{tx("ui.enabled_only")}</option>
                  <option value="true">{tx("ui.disabled_only")}</option>
                </select>
              </label>
              <label className="filter-control">
                <span>{tx("ui.editability")}</span>
                <select value={filters.editability} onChange={(event) => updateFilter("editability", event.target.value)} aria-label={tx("ui.editability")}>
                  <option value="">{tx("ui.all_sources")}</option>
                  <option value="editable">{tx("ui.editable")}</option>
                  <option value="read_only">{tx("ui.read_only")}</option>
                </select>
              </label>
            </div>
          </section>

          <main className="account-workspace">
        <div className="table-meta">
          <div className="table-title"><span>{tx("ui.account_list")}</span><strong>{data.total}</strong></div>
          <span>{tx("ui.count_records_page_page_slash_pages", { count: data.total, page: data.page || 1, pages: data.pages || 1 })}</span>
        </div>
        <div className="table-scroll">
          <table className="account-table">
            <colgroup>
              <col className="col-select" /><col className="col-identity" /><col className="col-provider" />
              <col className="col-type" /><col className="col-activity" /><col className="col-updated" /><col className="col-access" />
              <col className="col-state" /><col className="col-priority" /><col className="col-routing" /><col className="col-actions" />
            </colgroup>
            <thead>
              <tr>
                <th className="select-header"><input type="checkbox" checked={allPageSelected} onChange={togglePage} aria-label={tx("ui.select_editable_accounts_on_this_page")} /></th>
                <th className="identity-header">{tx("ui.accounts")}</th><th>{tx("ui.provider")}</th><th>{tx("ui.type")}</th><th>{tx("ui.usage")}</th><th>{tx("ui.updated")}</th><th>{tx("ui.access")}</th><th>{tx("ui.status")}</th><th>{tx("ui.priority")}</th><th>{tx("ui.routing")}</th><th className="actions-header">{tx("ui.actions")}</th>
              </tr>
            </thead>
            <tbody>
              {loading ? <LoadingRows /> : data.accounts.map((account) => {
                const identity = account.label || account.email || account.name || account.id;
                const readOnlyReason = operatorMessage(account.read_only_reason, locale) || tx("ui.this_account_is_read_only");
                return (
                <tr key={account.id} className={`${selected.has(account.id) ? "is-selected" : ""} ${!account.editable ? "is-readonly" : ""}`}>
                  <td className="select-cell"><input type="checkbox" checked={selected.has(account.id)} disabled={!account.editable} onChange={() => toggleAccount(account)} aria-label={tx("ui.select_account", { account: account.label || account.name || account.id })} title={operatorMessage(account.read_only_reason, locale)} /></td>
                  <td className="identity-column-cell">
                    <div className="identity-cell">
                      <strong>{account.label || account.email || account.name || account.id}</strong>
                      <span>{account.email && account.label !== account.email ? account.email : account.name}</span>
                      {account.note ? <small>{account.note}</small> : null}
                    </div>
                  </td>
                  <td><span className="provider-tag">{technicalLabel(account.provider || account.type)}</span></td>
                  <td><AccountTypeCell account={account} /></td>
                  <td><AccountUsageCell account={account} /></td>
                  <td><time>{formatDateTime(account.updated_at || account.last_refresh)}</time></td>
                  <td>{account.editable ? <span className="access-tag editable"><Settings2 size={13} />{tx("ui.editable")}</span> : <span className="access-tag readonly" title={operatorMessage(account.read_only_reason, locale)}><LockKeyhole size={13} />{tx("ui.read_only")}</span>}</td>
                  <td><StateCell account={account} /></td>
                  <td><code className="priority-value">{account.priority ?? "-"}</code></td>
                  <td><RoutingCell account={account} /></td>
                  <td className="actions-cell">
                    <div className="row-actions">
                      <IconButton label={tx("ui.view_account", { account: identity })} onClick={() => setDetailAccount(account)}><Eye size={15} /></IconButton>
                      <IconButton label={tx("ui.test_model_for_account", { account: identity })} onClick={() => openModelTest(account)}><Activity size={15} /></IconButton>
                      <IconButton label={account.editable ? tx("ui.edit_account_2", { account: identity }) : readOnlyReason} disabled={!account.editable} onClick={() => openAccountEditor(account)}><Pencil size={15} /></IconButton>
                      <IconButton className="row-delete-action" label={account.editable ? tx("ui.delete_account_2", { account: identity }) : readOnlyReason} disabled={!account.editable} onClick={() => void openDelete(account)}><Trash2 size={15} /></IconButton>
                    </div>
                  </td>
                </tr>
                );
              })}
            </tbody>
          </table>
          {!loading && data.accounts.length === 0 ? <div className="empty-state" role="status">{tx("ui.no_matching_accounts")}</div> : null}
        </div>
        <div className="pagination">
          <label className="page-size-control">
            <span>{tx("ui.per_page")}</span>
            <select aria-label={tx("ui.accounts_per_page")} value={pageSize} onChange={(event) => updatePageSize(event.target.value)}>
              {ACCOUNT_PAGE_SIZE_OPTIONS.map((option) => <option key={option} value={option}>{option}</option>)}
            </select>
          </label>
          <IconButton label={tx("ui.previous_page")} disabled={page <= 1} onClick={() => setPage((value) => Math.max(1, value - 1))}><ChevronLeft size={17} /></IconButton>
          <strong>{page}</strong>
          <IconButton label={tx("ui.next_page")} disabled={data.pages === 0 || page >= data.pages} onClick={() => setPage((value) => value + 1)}><ChevronRight size={17} /></IconButton>
        </div>
          </main>
        </section>
        ) : activeView === "inspection" ? (
          <InspectionWorkspace onAPIError={handleAPIError} onNotice={setNotice} onAccountsChanged={requestInspectionAccountSync} />
        ) : activeView === "operations" ? (
          <OperationLogWorkspace
            activeJobIDs={[job?.id, forceJob?.id].filter((id): id is string => Boolean(id))}
            onAPIError={handleAPIError}
            onNotice={setNotice}
            onOpenRelatedJob={(operation) => {
              if (operation.related_job_id === forceJob?.id) {
                setJobOpen(false);
                setForceJobOpen(true);
              } else if (operation.related_job_id === job?.id) {
                setForceJobOpen(false);
                setJobOpen(true);
              }
            }}
          />
        ) : (
          <OtherSettingsWorkspace onAPIError={handleAPIError} onNotice={setNotice} />
        )}
      </div>

      {activeView === "accounts" && data.total > 0 ? (
        <footer className="bulk-bar">
          <div className="bulk-scope-line">
            <div className="scope-segment" aria-label={tx("ui.batch_scope")}>
              <button type="button" className={scopeMode === "selected" ? "active" : ""} disabled={selected.size === 0} onClick={() => setScopeMode("selected")}>{tx("ui.selected")} <strong>{selected.size}</strong></button>
              <button type="button" className={scopeMode === "filtered" ? "active" : ""} onClick={() => setScopeMode("filtered")}>{tx("ui.all_filtered")} <strong>{data.total}</strong></button>
            </div>
            {selected.size > 0 ? (
              <div className="selection-actions">
                <button className="button button-quiet selection-export" type="button" title={tx("ui.export_selected_accounts")} aria-label={tx("ui.export_selected_accounts")} onClick={() => openExport("accounts", { mode: "selected", ids: Array.from(selected) })}>
                  <Download size={16} /><span>{tx("ui.export_selected")}</span>
                </button>
                <IconButton className="clear-selection" label={tx("ui.clear_selection")} onClick={() => { setSelected(new Set()); setScopeMode("filtered"); }}><X size={16} /></IconButton>
              </div>
            ) : null}
          </div>
          <div className="bulk-actions">
            <button className="button button-success" type="button" disabled={previewLoading} onClick={() => void beginPreview({ disabled: false })}><Power size={16} />{tx("ui.enable_selected")}</button>
            <button className="button button-danger" type="button" disabled={previewLoading} onClick={() => void beginPreview({ disabled: true })}><PowerOff size={16} />{tx("ui.disable_selected")}</button>
            <button className="button button-primary" type="button" disabled={previewLoading} onClick={() => setEditorContext({ title: "ui.batch_edit", scopeLabel })}>
              {previewLoading ? <LoaderCircle className="spin" size={16} /> : <SlidersHorizontal size={16} />}{tx("ui.batch_edit")}
            </button>
          </div>
        </footer>
      ) : null}

      {authState === "booting" ? <div className="auth-loading"><LoaderCircle className="spin" size={24} /></div> : null}
      {authState === "login" ? <LoginDialog loading={authLoading} error={authError} onSubmit={login} /> : null}
      {editorContext ? <BatchEditor title={editorContext.title} scopeLabel={editorContext.scopeLabel} onClose={() => setEditorContext(null)} onSubmit={(patch) => { const scope = editorContext.scope; setEditorContext(null); void beginPreview(patch, scope); }} /> : null}
      {detailAccount ? <AccountDetailsDialog account={detailAccount} onClose={() => setDetailAccount(null)} onEdit={() => openAccountEditor(detailAccount)} /> : null}
      {modelTestTarget ? <ModelTestDialog key={modelTestTarget.id} account={modelTestTarget} result={modelTestResult} error={modelTestError} testing={modelTesting} onClose={closeModelTest} onTest={(model) => void runModelTest(model)} /> : null}
      {deleteTarget ? <DeleteAccountDialog key={deleteTarget.id} account={deleteTarget} preview={deletePreview} previewing={deletePreviewing} deleting={deleting} error={deleteError} onClose={closeDelete} onConfirm={() => void confirmDelete()} /> : null}
      {preview ? <PreviewDialog preview={preview} starting={starting} error={previewError} onClose={() => { setPreview(null); setPreviewError(""); }} onConfirm={() => void confirmPreview()} /> : null}
      {jobOpen && job ? <JobPanel job={job} retrying={retrying} onClose={() => setJobOpen(false)} onRetry={() => void retryJob()} onExport={() => openExport("results")} onRefresh={() => void refreshJob()} /> : null}
      {importOpen ? <ImportDialog preview={importPreview} result={importResult} previewing={importPreviewing} importing={importStarting} error={importError} onClose={closeImport} onPreview={(files) => void previewImport(files)} onImport={() => void confirmImport()} onReset={resetImport} /> : null}
      {exportTarget ? <ExportDialog kind={exportTarget} count={exportCount} scopeLabel={exportScopeLabel} exporting={exporting} error={exportError} onClose={closeExport} onExport={(format) => void confirmExport(format)} /> : null}
      {policyOpen && policySnapshot ? <PolicyDialog key={`${policySnapshot.policy.enabled}:${policySnapshot.policy.priority}:${policySnapshot.policy.websockets}:${policySnapshot.policy.scan_interval_seconds}`} snapshot={policySnapshot} saving={policySaving} scanning={policyScanning} forceLoading={forcePreviewLoading} error={policyError} onClose={() => setPolicyOpen(false)} onSave={(policy) => void savePolicy(policy)} onScan={() => void scanPolicy()} onForcePreview={() => void previewForceSync()} /> : null}
      {policyOpen && !policySnapshot ? (
        <Modal title={tx("ui.default_policy")} onClose={() => setPolicyOpen(false)} footer={<button className="button" type="button" onClick={() => setPolicyOpen(false)}>{tx("ui.close")}</button>}>
          <div className="policy-loading">{policyLoading ? <><LoaderCircle className="spin" size={22} /><span>{tx("ui.loading_policy")}</span></> : <><span>{policyError || tx("ui.policy_unavailable")}</span><button className="button" type="button" onClick={() => void openPolicy()}>{tx("ui.retry")}</button></>}</div>
        </Modal>
      ) : null}
      {forcePreview ? <ForceSyncPreviewDialog preview={forcePreview} starting={forceStarting} error={forcePreviewError} onClose={() => { setForcePreview(null); setForcePreviewError(""); setPolicyOpen(true); }} onConfirm={() => void confirmForceSync()} /> : null}
      {forceJobOpen && forceJob ? <JobPanel job={forceJob} title="ui.default_policy_sync" ariaLabel="ui.default_policy_force_sync" fields={forceJob.policy.fields} onClose={() => setForceJobOpen(false)} onRefresh={() => void refreshForceJob()} /> : null}
    </div>
  );
}

function AccountTypeCell({ account }: { account: Account }) {
  const primary = account.plan_type || account.account_type || account.type || "-";
  const secondary = account.plan_type ? account.account_type || account.type : "";
  return (
    <div className="type-cell" title={secondary ? `${primary} / ${secondary}` : primary}>
      <strong className="account-plan-type">{primary}</strong>
      {secondary && secondary !== primary ? <span>{secondary}</span> : null}
    </div>
  );
}

function StateCell({ account }: { account: Account }) {
  const { locale, tx } = useI18n();
  const status = accountState(account);
  const automation = accountAutomationPresentation(account, locale);
  return (
    <div className="state-cell" title={automation?.detail || operatorMessage(account.status_message, locale)}>
      <span className={`state-dot state-${status}`} />
      <strong>{accountStateLabel(account, locale)}</strong>
      <span className="state-message">{account.disabled ? tx("ui.account_disabled") : operatorMessage(account.status_message, locale)}</span>
      {automation ? (
        <div className="automation-disposition">
          <span className={`automation-disposition-badge is-${automation.tone}`}>{automation.badge}</span>
          <small>{automation.detail}</small>
        </div>
      ) : null}
    </div>
  );
}

function RoutingCell({ account }: { account: Account }) {
  const { locale, tx } = useI18n();
  return (
    <div className="routing-cell">
      <code>{account.prefix || tx("ui.default")}</code>
      <span title={account.proxy || (account.proxy_configured ? tx("ui.proxy_configured") : tx("ui.no_proxy"))}>{account.proxy_configured ? <Wifi size={14} /> : <WifiOff size={14} />}</span>
      <span className={account.websockets ? "ws-on" : ""}>WS {account.websockets ? tx("ui.on_2") : tx("ui.off_2")}</span>
      {account.header_count > 0 ? <span>H {account.header_count}</span> : null}
    </div>
  );
}

function LoadingRows() {
  return <>{Array.from({ length: 8 }, (_, index) => <tr className="loading-row" key={index}><td colSpan={11}><span /></td></tr>)}</>;
}

function errorText(error: unknown, locale: Locale = "zh-CN"): string {
  return error instanceof Error ? operatorMessage(error.message, locale) : translateUI(locale, "ui.request_failed");
}

import {
  Activity,
  ChevronLeft,
  ChevronRight,
  Download,
  Eye,
  FileCog,
  KeyRound,
  LoaderCircle,
  LockKeyhole,
  Power,
  PowerOff,
  Pencil,
  RefreshCw,
  Search,
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
import { JobPanel, jobStateLabel } from "./components/JobPanel";
import { LoginDialog } from "./components/LoginDialog";
import { Modal } from "./components/Modal";
import { PolicyDialog } from "./components/PolicyDialog";
import { PreviewDialog } from "./components/PreviewDialog";
import { DeleteAccountDialog } from "./components/DeleteAccountDialog";
import { operatorMessage } from "./format/operatorMessage";
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
  PolicySnapshot,
  ResultExportFormat,
  TargetScope,
} from "./types";

const PAGE_SIZE = 50;
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

interface FilterState {
  search: string;
  provider: string;
  type: string;
  status: string;
  disabled: string;
  editability: string;
}

const emptyFilters: FilterState = { search: "", provider: "", type: "", status: "", disabled: "", editability: "" };

interface EditorContext {
  title: string;
  scopeLabel: string;
  scope?: TargetScope;
}

export default function App() {
  const [authState, setAuthState] = useState<"booting" | "login" | "ready">("booting");
  const [authLoading, setAuthLoading] = useState(false);
  const [authError, setAuthError] = useState("");
  const [filters, setFilters] = useState<FilterState>(emptyFilters);
  const [searchDraft, setSearchDraft] = useState("");
  const [page, setPage] = useState(1);
  const [data, setData] = useState<AccountListResponse>({ accounts: [], total: 0, page: 1, page_size: PAGE_SIZE, pages: 0 });
  const [loading, setLoading] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [scopeMode, setScopeMode] = useState<"selected" | "filtered">("filtered");
  const [editorContext, setEditorContext] = useState<EditorContext | null>(null);
  const [detailAccount, setDetailAccount] = useState<Account | null>(null);
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
  const [exportTarget, setExportTarget] = useState<"accounts" | "results" | null>(null);
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
      setAuthError(operatorMessage(error.message));
      return;
    }
    setNotice(errorText(error));
  }, []);

  const refreshAccounts = useCallback(async () => {
    if (authState !== "ready") return;
    const requestID = accountRequest.current + 1;
    accountRequest.current = requestID;
    setLoading(true);
    try {
      const response = await api.listAccounts(page, PAGE_SIZE, apiFilters);
      if (requestID !== accountRequest.current) return;
      setData(response);
      if (response.pages > 0 && page > response.pages) setPage(response.pages);
    } catch (error) {
      if (requestID !== accountRequest.current) return;
      handleAPIError(error);
    } finally {
      if (requestID === accountRequest.current) setLoading(false);
    }
  }, [apiFilters, authState, handleAPIError, page]);

  useEffect(() => {
    void refreshAccounts();
  }, [refreshAccounts]);

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
    const refresh = async () => {
      try {
        const snapshot = await api.getDefaultPolicy();
        if (!cancelled) setPolicySnapshot(snapshot);
      } catch (error) {
        if (!cancelled && error instanceof api.APIError && error.status === 401) handleAPIError(error);
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
      setAuthState("ready");
    } catch (error) {
      clearSession();
      setAuthError(error instanceof Error ? operatorMessage(error.message) : "认证失败");
    } finally {
      setAuthLoading(false);
    }
  };

  const updateFilter = (key: keyof FilterState, value: string) => {
    setFilters((current) => ({ ...current, [key]: value }));
    setPage(1);
    setSelected(new Set());
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
      title: "编辑账号",
      scopeLabel: account.label || account.email || account.name || account.id,
      scope: { mode: "selected", ids: [account.id] },
    });
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
        setDeleteError(errorText(error));
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
      setNotice(`已删除账号 ${result.account.label || result.account.email || result.account.name}`);
      await refreshAccounts();
    } catch (error) {
      if (error instanceof api.APIError && error.status === 401) {
        closeDelete();
        handleAPIError(error);
      } else {
        setDeleteError(errorText(error));
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
        setPreviewError(errorText(error));
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
        setPolicyError(errorText(error));
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
      else setPolicyError(errorText(error));
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
      else setPolicyError(errorText(error));
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
      else setPolicyError(errorText(error));
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
      else setForcePreviewError(errorText(error));
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
        setImportError(errorText(error));
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
      setNotice(`已添加 ${result.imported} 个账号${result.failed || result.skipped ? `，${result.failed + result.skipped} 个未写入` : ""}`);
      void refreshAccounts();
    } catch (error) {
      if (error instanceof api.APIError && error.status === 401) {
        closeImport();
        handleAPIError(error);
      } else {
        setImportError(errorText(error));
      }
    } finally {
      setImportStarting(false);
    }
  };

  const openExport = (target: "accounts" | "results") => {
    setExportTarget(target);
    setExportError("");
  };

  const closeExport = () => {
    setExportTarget(null);
    setExportError("");
  };

  const confirmExport = async (format: ExportFormat) => {
    if (!exportTarget) return;
    setExporting(true);
    setExportError("");
    try {
      const result = exportTarget === "accounts"
        ? await api.downloadExport("accounts", format as AccountExportFormat, apiFilters)
        : await api.downloadExport("results", format as ResultExportFormat);
      if (exportTarget === "accounts") {
        const count = result.exported === undefined ? "" : ` ${result.exported} 个账号`;
        const skipped = result.skipped ? `，跳过 ${result.skipped} 个` : "";
        setNotice(`已下载 ${formatLabel(format)} 凭据${count}${skipped}`);
      } else {
        setNotice(`已导出结果 ${formatLabel(format)}`);
      }
      closeExport();
    } catch (error) {
      if (error instanceof api.APIError && error.status === 401) {
        closeExport();
        handleAPIError(error);
      } else {
        setExportError(errorText(error));
      }
    } finally {
      setExporting(false);
    }
  };

  const scopeLabel = scopeMode === "selected" && selected.size > 0
    ? `已选 ${selected.size} 个账号`
    : `当前筛选 ${data.total} 个账号`;
  const hasActiveFilters = Object.values(filters).some(Boolean) || Boolean(searchDraft);
  const panelOpen = Boolean(jobOpen && job || forceJobOpen && forceJob);

  return (
    <div className={`app-shell ${panelOpen ? "with-job-panel" : ""}`}>
      <header className="app-header">
        <div className="brand-block">
          <span className="brand-icon"><FileCog size={21} /></span>
          <div><h1>CPA Account Config Manager</h1><span>ACCOUNT CONFIGURATION</span></div>
        </div>
        <div className="header-status">
          <span><ShieldCheck size={15} />{data.total} 个账号</span>
          {job?.id ? <button type="button" onClick={() => { setForceJobOpen(false); setJobOpen(true); }}><Activity size={15} />{job.running ? `${job.done}/${job.total}` : jobStateLabel(job.state)}</button> : null}
          {forceJob?.id ? <button type="button" onClick={() => { setJobOpen(false); setForceJobOpen(true); }}><RefreshCw size={15} />{forceJob.running ? `${forceJob.done}/${forceJob.total}` : jobStateLabel(forceJob.state)}</button> : null}
        </div>
        <div className="header-actions">
          {job?.id ? <IconButton className="mobile-job-action" label="打开批量任务" onClick={() => { setForceJobOpen(false); setJobOpen(true); }}><Activity size={17} /></IconButton> : null}
          {forceJob?.id ? <IconButton className="mobile-job-action" label="打开强制同步任务" onClick={() => { setJobOpen(false); setForceJobOpen(true); }}><RefreshCw size={17} /></IconButton> : null}
          <button className="button button-primary header-add-account" type="button" title="添加账号" aria-label="添加账号" onClick={openImport}><UserPlus size={16} /><span>添加账号</span></button>
          <IconButton label="默认策略" onClick={() => void openPolicy()}><Settings2 size={17} /></IconButton>
          <IconButton className="export-action" label="下载筛选账号凭据" onClick={() => openExport("accounts")}><Download size={17} /></IconButton>
          <IconButton label="刷新账号" onClick={() => void refreshAccounts()} disabled={loading}><RefreshCw className={loading ? "spin" : ""} size={17} /></IconButton>
          <IconButton label="退出管理认证" onClick={() => { clearSession(); setAuthState("login"); }}><KeyRound size={17} /></IconButton>
        </div>
      </header>

      <section className="filter-bar" aria-label="账号筛选">
        <label className="search-box">
          <Search size={16} />
          <input value={searchDraft} onChange={(event) => setSearchDraft(event.target.value)} placeholder="搜索账号、邮箱、文件名、类型" aria-label="搜索账号" />
          {searchDraft ? <button type="button" aria-label="清空搜索" onClick={() => setSearchDraft("")}><X size={14} /></button> : null}
        </label>
        <input list="provider-options" value={filters.provider} onChange={(event) => updateFilter("provider", event.target.value)} placeholder="全部 Provider" aria-label="Provider" />
        <datalist id="provider-options">
          {providerOptions.map((provider) => <option key={provider} value={provider} />)}
        </datalist>
        <input list="type-options" value={filters.type} onChange={(event) => updateFilter("type", event.target.value)} placeholder="全部 Type" aria-label="Type" />
        <datalist id="type-options">
          {typeOptions.map((type) => <option key={type} value={type} />)}
        </datalist>
        <select value={filters.status} onChange={(event) => updateFilter("status", event.target.value)} aria-label="状态">
          <option value="">全部状态</option>
          <option value="active">active</option>
          <option value="disabled">disabled</option>
          <option value="error">error</option>
          <option value="unavailable">unavailable</option>
        </select>
        <select value={filters.disabled} onChange={(event) => updateFilter("disabled", event.target.value)} aria-label="启用状态">
          <option value="">启用与禁用</option>
          <option value="false">仅启用</option>
          <option value="true">仅禁用</option>
        </select>
        <select value={filters.editability} onChange={(event) => updateFilter("editability", event.target.value)} aria-label="可编辑性">
          <option value="">全部来源</option>
          <option value="editable">可编辑</option>
          <option value="read_only">只读</option>
        </select>
        <button className="button button-quiet reset-filters" type="button" disabled={!hasActiveFilters} onClick={() => { setFilters(emptyFilters); setSearchDraft(""); setPage(1); setSelected(new Set()); }}>
          重置
        </button>
      </section>

      {notice ? <div className="notice-bar" role="alert"><span>{notice}</span><IconButton label="关闭提示" onClick={() => setNotice("")}><X size={15} /></IconButton></div> : null}

      <main className="account-workspace">
        <div className="table-meta">
          <span>账号列表</span>
          <span>{data.total} 条记录 · 第 {data.page || 1}/{data.pages || 1} 页</span>
        </div>
        <div className="table-scroll">
          <table className="account-table">
            <colgroup>
              <col className="col-select" /><col className="col-identity" /><col className="col-provider" />
              <col className="col-type" /><col className="col-state" /><col className="col-priority" /><col className="col-routing" />
              <col className="col-activity" /><col className="col-updated" /><col className="col-access" /><col className="col-actions" />
            </colgroup>
            <thead>
              <tr>
                <th className="select-header"><input type="checkbox" checked={allPageSelected} onChange={togglePage} aria-label="选择本页可编辑账号" /></th>
                <th className="identity-header">账号</th><th>Provider</th><th>Type</th><th>状态</th><th>Priority</th><th>路由配置</th><th>用量</th><th>更新时间</th><th>权限</th><th className="actions-header">操作</th>
              </tr>
            </thead>
            <tbody>
              {loading ? <LoadingRows /> : data.accounts.map((account) => {
                const identity = account.label || account.email || account.name || account.id;
                const readOnlyReason = operatorMessage(account.read_only_reason) || "该账号为只读";
                return (
                <tr key={account.id} className={`${selected.has(account.id) ? "is-selected" : ""} ${!account.editable ? "is-readonly" : ""}`}>
                  <td className="select-cell"><input type="checkbox" checked={selected.has(account.id)} disabled={!account.editable} onChange={() => toggleAccount(account)} aria-label={`选择 ${account.label || account.name || account.id}`} title={operatorMessage(account.read_only_reason)} /></td>
                  <td className="identity-column-cell">
                    <div className="identity-cell">
                      <strong>{account.label || account.email || account.name || account.id}</strong>
                      <span>{account.email && account.label !== account.email ? account.email : account.name}</span>
                      {account.note ? <small>{account.note}</small> : null}
                    </div>
                  </td>
                  <td><span className="provider-tag">{account.provider || account.type || "unknown"}</span></td>
                  <td><AccountTypeCell account={account} /></td>
                  <td><StateCell account={account} /></td>
                  <td><code className="priority-value">{account.priority ?? "-"}</code></td>
                  <td><RoutingCell account={account} /></td>
                  <td><AccountUsageCell account={account} /></td>
                  <td><time>{formatDate(account.updated_at || account.last_refresh)}</time></td>
                  <td>{account.editable ? <span className="access-tag editable"><Settings2 size={13} />可编辑</span> : <span className="access-tag readonly" title={operatorMessage(account.read_only_reason)}><LockKeyhole size={13} />只读</span>}</td>
                  <td className="actions-cell">
                    <div className="row-actions">
                      <IconButton label={`查看 ${identity}`} onClick={() => setDetailAccount(account)}><Eye size={15} /></IconButton>
                      <IconButton label={account.editable ? `编辑 ${identity}` : readOnlyReason} disabled={!account.editable} onClick={() => openAccountEditor(account)}><Pencil size={15} /></IconButton>
                      <IconButton className="row-delete-action" label={account.editable ? `删除 ${identity}` : readOnlyReason} disabled={!account.editable} onClick={() => void openDelete(account)}><Trash2 size={15} /></IconButton>
                    </div>
                  </td>
                </tr>
                );
              })}
            </tbody>
          </table>
          {!loading && data.accounts.length === 0 ? <div className="empty-state" role="status">没有匹配账号</div> : null}
        </div>
        <div className="pagination">
          <span>每页 {PAGE_SIZE}</span>
          <IconButton label="上一页" disabled={page <= 1} onClick={() => setPage((value) => Math.max(1, value - 1))}><ChevronLeft size={17} /></IconButton>
          <strong>{page}</strong>
          <IconButton label="下一页" disabled={data.pages === 0 || page >= data.pages} onClick={() => setPage((value) => value + 1)}><ChevronRight size={17} /></IconButton>
        </div>
      </main>

      {data.total > 0 ? (
        <footer className="bulk-bar">
          <div className="bulk-scope-line">
            <div className="scope-segment" aria-label="批量范围">
              <button type="button" className={scopeMode === "selected" ? "active" : ""} disabled={selected.size === 0} onClick={() => setScopeMode("selected")}>已选 <strong>{selected.size}</strong></button>
              <button type="button" className={scopeMode === "filtered" ? "active" : ""} onClick={() => setScopeMode("filtered")}>全部筛选 <strong>{data.total}</strong></button>
            </div>
            {selected.size > 0 ? <IconButton className="clear-selection" label="清空选择" onClick={() => { setSelected(new Set()); setScopeMode("filtered"); }}><X size={16} /></IconButton> : null}
          </div>
          <div className="bulk-actions">
            <button className="button button-success" type="button" disabled={previewLoading} onClick={() => void beginPreview({ disabled: false })}><Power size={16} />批量启用</button>
            <button className="button button-danger" type="button" disabled={previewLoading} onClick={() => void beginPreview({ disabled: true })}><PowerOff size={16} />批量禁用</button>
            <button className="button button-primary" type="button" disabled={previewLoading} onClick={() => setEditorContext({ title: "批量编辑", scopeLabel })}>
              {previewLoading ? <LoaderCircle className="spin" size={16} /> : <SlidersHorizontal size={16} />}批量编辑
            </button>
          </div>
        </footer>
      ) : null}

      {authState === "booting" ? <div className="auth-loading"><LoaderCircle className="spin" size={24} /></div> : null}
      {authState === "login" ? <LoginDialog loading={authLoading} error={authError} onSubmit={login} /> : null}
      {editorContext ? <BatchEditor title={editorContext.title} scopeLabel={editorContext.scopeLabel} onClose={() => setEditorContext(null)} onSubmit={(patch) => { const scope = editorContext.scope; setEditorContext(null); void beginPreview(patch, scope); }} /> : null}
      {detailAccount ? <AccountDetailsDialog account={detailAccount} onClose={() => setDetailAccount(null)} onEdit={() => openAccountEditor(detailAccount)} /> : null}
      {deleteTarget ? <DeleteAccountDialog key={deleteTarget.id} account={deleteTarget} preview={deletePreview} previewing={deletePreviewing} deleting={deleting} error={deleteError} onClose={closeDelete} onConfirm={() => void confirmDelete()} /> : null}
      {preview ? <PreviewDialog preview={preview} starting={starting} error={previewError} onClose={() => { setPreview(null); setPreviewError(""); }} onConfirm={() => void confirmPreview()} /> : null}
      {jobOpen && job ? <JobPanel job={job} retrying={retrying} onClose={() => setJobOpen(false)} onRetry={() => void retryJob()} onExport={() => openExport("results")} onRefresh={() => void refreshJob()} /> : null}
      {importOpen ? <ImportDialog preview={importPreview} result={importResult} previewing={importPreviewing} importing={importStarting} error={importError} onClose={closeImport} onPreview={(files) => void previewImport(files)} onImport={() => void confirmImport()} onReset={resetImport} /> : null}
      {exportTarget ? <ExportDialog kind={exportTarget} count={exportTarget === "accounts" ? data.total : job?.results?.length ?? job?.done ?? 0} exporting={exporting} error={exportError} onClose={closeExport} onExport={(format) => void confirmExport(format)} /> : null}
      {policyOpen && policySnapshot ? <PolicyDialog key={`${policySnapshot.policy.enabled}:${policySnapshot.policy.priority}:${policySnapshot.policy.websockets}:${policySnapshot.policy.scan_interval_seconds}`} snapshot={policySnapshot} saving={policySaving} scanning={policyScanning} forceLoading={forcePreviewLoading} error={policyError} onClose={() => setPolicyOpen(false)} onSave={(policy) => void savePolicy(policy)} onScan={() => void scanPolicy()} onForcePreview={() => void previewForceSync()} /> : null}
      {policyOpen && !policySnapshot ? (
        <Modal title="默认策略" onClose={() => setPolicyOpen(false)} footer={<button className="button" type="button" onClick={() => setPolicyOpen(false)}>关闭</button>}>
          <div className="policy-loading">{policyLoading ? <><LoaderCircle className="spin" size={22} /><span>正在读取策略</span></> : <><span>{policyError || "策略不可用"}</span><button className="button" type="button" onClick={() => void openPolicy()}>重试</button></>}</div>
        </Modal>
      ) : null}
      {forcePreview ? <ForceSyncPreviewDialog preview={forcePreview} starting={forceStarting} error={forcePreviewError} onClose={() => { setForcePreview(null); setForcePreviewError(""); setPolicyOpen(true); }} onConfirm={() => void confirmForceSync()} /> : null}
      {forceJobOpen && forceJob ? <JobPanel job={forceJob} title="默认策略同步" ariaLabel="默认策略强制同步" fields={forceJob.policy.fields} onClose={() => setForceJobOpen(false)} onRefresh={() => void refreshForceJob()} /> : null}
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
  const status = account.disabled ? "disabled" : account.unavailable ? "unavailable" : account.status || "unknown";
  return (
    <div className="state-cell" title={operatorMessage(account.status_message)}>
      <span className={`state-dot state-${status}`} />
      <strong>{status}</strong>
      <span>{account.disabled ? "已禁用" : operatorMessage(account.status_message)}</span>
    </div>
  );
}

function RoutingCell({ account }: { account: Account }) {
  return (
    <div className="routing-cell">
      <code>{account.prefix || "default"}</code>
      <span title={account.proxy || (account.proxy_configured ? "已配置代理" : "未配置代理")}>{account.proxy_configured ? <Wifi size={14} /> : <WifiOff size={14} />}</span>
      <span className={account.websockets ? "ws-on" : ""}>WS {account.websockets ? "ON" : "OFF"}</span>
      {account.header_count > 0 ? <span>H {account.header_count}</span> : null}
    </div>
  );
}

function LoadingRows() {
  return <>{Array.from({ length: 8 }, (_, index) => <tr className="loading-row" key={index}><td colSpan={11}><span /></td></tr>)}</>;
}

function formatDate(value?: string): string {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  return new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }).format(date);
}

function errorText(error: unknown): string {
  return error instanceof Error ? operatorMessage(error.message) : "请求失败";
}

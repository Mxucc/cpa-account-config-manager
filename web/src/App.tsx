import {
  Activity,
  ChevronLeft,
  ChevronRight,
  Download,
  FileCog,
  KeyRound,
  LoaderCircle,
  LockKeyhole,
  Power,
  PowerOff,
  RefreshCw,
  Search,
  Settings2,
  ShieldCheck,
  SlidersHorizontal,
  Wifi,
  WifiOff,
  X,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import * as api from "./api/client";
import { BatchEditor } from "./components/BatchEditor";
import { IconButton } from "./components/IconButton";
import { JobPanel } from "./components/JobPanel";
import { LoginDialog } from "./components/LoginDialog";
import { PreviewDialog } from "./components/PreviewDialog";
import { operatorMessage } from "./format/operatorMessage";
import { readPanelAuth } from "./store/panelAuth";
import { clearSession, setSession } from "./store/session";
import type { Account, AccountFilters, AccountListResponse, BatchPatch, BatchPreview, JobSnapshot } from "./types";

const PAGE_SIZE = 50;
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

interface FilterState {
  search: string;
  provider: string;
  status: string;
  disabled: string;
  editability: string;
}

const emptyFilters: FilterState = { search: "", provider: "", status: "", disabled: "", editability: "" };

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
  const [editorOpen, setEditorOpen] = useState(false);
  const [preview, setPreview] = useState<BatchPreview | null>(null);
  const [previewLoading, setPreviewLoading] = useState(false);
  const [starting, setStarting] = useState(false);
  const [job, setJob] = useState<JobSnapshot | null>(null);
  const [jobOpen, setJobOpen] = useState(false);
  const [retrying, setRetrying] = useState(false);
  const [notice, setNotice] = useState("");
  const accountRequest = useRef(0);

  const apiFilters = useMemo<AccountFilters>(() => ({
    ...(filters.search ? { search: filters.search } : {}),
    ...(filters.provider ? { provider: filters.provider } : {}),
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
    setNotice(error instanceof Error ? operatorMessage(error.message) : "请求失败");
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

  const beginPreview = async (patch: BatchPatch) => {
    setPreviewLoading(true);
    try {
      const response = await api.createPreview(targetScope(), patch);
      setPreview(response);
    } catch (error) {
      handleAPIError(error);
    } finally {
      setPreviewLoading(false);
    }
  };

  const confirmPreview = async () => {
    if (!preview) return;
    setStarting(true);
    try {
      const snapshot = await api.startBatch(preview.id);
      setPreview(null);
      setJob(snapshot);
      setJobOpen(true);
    } catch (error) {
      handleAPIError(error);
    } finally {
      setStarting(false);
    }
  };

  const retryJob = async () => {
    setRetrying(true);
    try {
      const snapshot = await api.retryBatch();
      setJob(snapshot);
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

  const scopeLabel = scopeMode === "selected" && selected.size > 0
    ? `已选 ${selected.size} 个账号`
    : `当前筛选 ${data.total} 个账号`;
  const hasActiveFilters = Object.values(filters).some(Boolean) || Boolean(searchDraft);

  return (
    <div className={`app-shell ${jobOpen && job ? "with-job-panel" : ""}`}>
      <header className="app-header">
        <div className="brand-block">
          <span className="brand-icon"><FileCog size={21} /></span>
          <div><h1>CPA Account Config Manager</h1><span>ACCOUNT CONTROL SURFACE</span></div>
        </div>
        <div className="header-status">
          <span><ShieldCheck size={15} />{data.total} accounts</span>
          {job?.id ? <button type="button" onClick={() => setJobOpen(true)}><Activity size={15} />{job.running ? `${job.done}/${job.total}` : job.state}</button> : null}
        </div>
        <div className="header-actions">
          {job?.id ? <IconButton className="mobile-job-action" label="打开批量任务" onClick={() => setJobOpen(true)}><Activity size={17} /></IconButton> : null}
          <IconButton className="export-action" label="导出筛选账号" onClick={() => void api.downloadExport("accounts", apiFilters).catch(handleAPIError)}><Download size={17} /></IconButton>
          <IconButton label="刷新账号" onClick={() => void refreshAccounts()} disabled={loading}><RefreshCw className={loading ? "spin" : ""} size={17} /></IconButton>
          <IconButton label="退出管理认证" onClick={() => { clearSession(); setAuthState("login"); }}><KeyRound size={17} /></IconButton>
        </div>
      </header>

      <section className="filter-bar" aria-label="账号筛选">
        <label className="search-box">
          <Search size={16} />
          <input value={searchDraft} onChange={(event) => setSearchDraft(event.target.value)} placeholder="搜索账号、邮箱、文件名" aria-label="搜索账号" />
          {searchDraft ? <button type="button" aria-label="清空搜索" onClick={() => setSearchDraft("")}><X size={14} /></button> : null}
        </label>
        <input list="provider-options" value={filters.provider} onChange={(event) => updateFilter("provider", event.target.value)} placeholder="全部 Provider" aria-label="Provider" />
        <datalist id="provider-options">
          {providerOptions.map((provider) => <option key={provider} value={provider} />)}
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
          <span>{data.total} records · page {data.page || 1}/{data.pages || 1}</span>
        </div>
        <div className="table-scroll">
          <table className="account-table">
            <colgroup>
              <col className="col-select" /><col className="col-identity" /><col className="col-provider" />
              <col className="col-state" /><col className="col-priority" /><col className="col-routing" />
              <col className="col-activity" /><col className="col-updated" /><col className="col-access" />
            </colgroup>
            <thead>
              <tr>
                <th><input type="checkbox" checked={allPageSelected} onChange={togglePage} aria-label="选择本页可编辑账号" /></th>
                <th>账号</th><th>Provider</th><th>状态</th><th>Priority</th><th>路由配置</th><th>请求</th><th>更新时间</th><th>权限</th>
              </tr>
            </thead>
            <tbody>
              {loading ? <LoadingRows /> : data.accounts.map((account) => (
                <tr key={account.id} className={`${selected.has(account.id) ? "is-selected" : ""} ${!account.editable ? "is-readonly" : ""}`}>
                  <td><input type="checkbox" checked={selected.has(account.id)} disabled={!account.editable} onChange={() => toggleAccount(account)} aria-label={`选择 ${account.label || account.name || account.id}`} title={operatorMessage(account.read_only_reason)} /></td>
                  <td>
                    <div className="identity-cell">
                      <strong>{account.label || account.email || account.name || account.id}</strong>
                      <span>{account.email && account.label !== account.email ? account.email : account.name}</span>
                      {account.note ? <small>{account.note}</small> : null}
                    </div>
                  </td>
                  <td><span className="provider-tag">{account.provider || account.type || "unknown"}</span><small className="account-type">{account.account_type || account.type}</small></td>
                  <td><StateCell account={account} /></td>
                  <td><code className="priority-value">{account.priority ?? "-"}</code></td>
                  <td><RoutingCell account={account} /></td>
                  <td><div className="activity-cell"><span className="success">{account.success}</span><span className="danger">{account.failed}</span></div></td>
                  <td><time>{formatDate(account.updated_at || account.last_refresh)}</time></td>
                  <td>{account.editable ? <span className="access-tag editable"><Settings2 size={13} />可编辑</span> : <span className="access-tag readonly" title={operatorMessage(account.read_only_reason)}><LockKeyhole size={13} />只读</span>}</td>
                </tr>
              ))}
              {!loading && data.accounts.length === 0 ? <tr><td colSpan={9}><div className="empty-state">没有匹配账号</div></td></tr> : null}
            </tbody>
          </table>
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
          <div className="scope-segment" aria-label="批量范围">
            <button type="button" className={scopeMode === "selected" ? "active" : ""} disabled={selected.size === 0} onClick={() => setScopeMode("selected")}>已选 <strong>{selected.size}</strong></button>
            <button type="button" className={scopeMode === "filtered" ? "active" : ""} onClick={() => setScopeMode("filtered")}>全部筛选 <strong>{data.total}</strong></button>
          </div>
          <span className="bulk-divider" />
          <button className="button button-success" type="button" disabled={previewLoading} onClick={() => void beginPreview({ disabled: false })}><Power size={16} />批量启用</button>
          <button className="button button-danger" type="button" disabled={previewLoading} onClick={() => void beginPreview({ disabled: true })}><PowerOff size={16} />批量禁用</button>
          <button className="button button-primary" type="button" disabled={previewLoading} onClick={() => setEditorOpen(true)}>
            {previewLoading ? <LoaderCircle className="spin" size={16} /> : <SlidersHorizontal size={16} />}批量编辑
          </button>
          {selected.size > 0 ? <button className="button button-quiet" type="button" onClick={() => { setSelected(new Set()); setScopeMode("filtered"); }}>清空选择</button> : null}
        </footer>
      ) : null}

      {authState === "booting" ? <div className="auth-loading"><LoaderCircle className="spin" size={24} /></div> : null}
      {authState === "login" ? <LoginDialog loading={authLoading} error={authError} onSubmit={login} /> : null}
      {editorOpen ? <BatchEditor scopeLabel={scopeLabel} onClose={() => setEditorOpen(false)} onSubmit={(patch) => { setEditorOpen(false); void beginPreview(patch); }} /> : null}
      {preview ? <PreviewDialog preview={preview} starting={starting} onClose={() => setPreview(null)} onConfirm={() => void confirmPreview()} /> : null}
      {jobOpen && job ? <JobPanel job={job} retrying={retrying} onClose={() => setJobOpen(false)} onRetry={() => void retryJob()} onExport={() => void api.downloadExport("results").catch(handleAPIError)} onRefresh={() => void refreshJob()} /> : null}
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
  return <>{Array.from({ length: 8 }, (_, index) => <tr className="loading-row" key={index}><td colSpan={9}><span /></td></tr>)}</>;
}

function formatDate(value?: string): string {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  return new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }).format(date);
}

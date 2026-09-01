import {
  Activity,
  ArrowDown,
  ArrowUp,
  ArrowUpDown,
  Boxes,
  ChevronLeft,
  ChevronRight,
  CopyCheck,
  Download,
  Eye,
  FileCog,
  Github,
  Gauge,
  CircleHelp,
  LoaderCircle,
  LockKeyhole,
  Power,
  PowerOff,
  Pencil,
  RefreshCw,
	RotateCcw,
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
import { AccountActionsMenu } from "./components/AccountActionsMenu";
import { AccountDeduplicationDialog } from "./components/AccountDeduplicationDialog";
import { BatchEditor } from "./components/BatchEditor";
import { AccountUsageCell } from "./components/AccountUsageCell";
import { AgentIdentitySessionLogin } from "./components/AgentIdentitySessionLogin";
import { ExportDialog } from "./components/ExportDialog";
import { ForceSyncPreviewDialog } from "./components/ForceSyncPreviewDialog";
import { IconButton } from "./components/IconButton";
import { ImportDialog } from "./components/ImportDialog";
import { InspectionWorkspace } from "./components/InspectionWorkspace";
import { AIProvidersSettings } from "./components/AIProvidersSettings";
import { OperationLogWorkspace } from "./components/OperationLogWorkspace";
import { OtherSettingsWorkspace } from "./components/OtherSettingsWorkspace";
import { PluginUpdateAutomation } from "./components/PluginUpdateAutomation";
import { JobPanel, jobStateLabel } from "./components/JobPanel";
import { LoginDialog } from "./components/LoginDialog";
import { ModelTestDialog } from "./components/ModelTestDialog";
import { Modal } from "./components/Modal";
import { PreviewDialog } from "./components/PreviewDialog";
import { DeleteAccountDialog } from "./components/DeleteAccountDialog";
import { DashboardWorkspace } from "./components/DashboardWorkspace";
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
import {
  DEFAULT_ACCOUNT_SORT,
  isDefaultAccountSort,
  readAccountSort,
  writeAccountSort,
  type AccountSort,
  type AccountSortField,
} from "./store/accountSort";
import { readPanelAuth } from "./store/panelAuth";
import { clearSession, setSession } from "./store/session";
import type {
  Account,
  AccountDeletePreview,
  AccountDeduplicationOptions,
  AccountDeduplicationPreview,
  AccountExportFormat,
  AccountFilters,
  AccountListResponse,
  BatchPatch,
  BatchPreview,
  ForceSyncJobSnapshot,
  ForceSyncPreview,
  ExportFormat,
  ExperimentalSettings,
  ImportPreview,
  ImportResult,
  JobSnapshot,
  ModelTestResult,
  ResultExportFormat,
  TargetScope,
} from "./types";
import { accountConcurrencyLimitLabel, accountConcurrencySaturated } from "./accountConcurrency";

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

function applyCompletedAccountPatch(current: AccountListResponse, job: JobSnapshot, patch: BatchPatch | null): AccountListResponse {
  if (!patch || job.operation === "delete") return current;
  const succeeded = new Set((job.results ?? []).filter((result) => result.status === "succeeded").map((result) => result.id));
  if (succeeded.size === 0) return current;
  return {
    ...current,
    accounts: current.accounts.map((account) => {
      if (!succeeded.has(account.id)) return account;
      return {
        ...account,
        ...(patch.disabled !== undefined ? { disabled: patch.disabled } : {}),
        ...(patch.priority !== undefined ? { priority: patch.priority } : {}),
				...((patch.concurrency_limit !== undefined || patch.concurrency_15s_limit !== undefined) ? {
					concurrency: {
						supported: account.concurrency?.supported ?? current.account_concurrency?.supported ?? false,
						active: account.concurrency?.active ?? 0,
						limit: patch.concurrency_limit ?? account.concurrency?.limit ?? 0,
						limit_15s: patch.concurrency_15s_limit ?? account.concurrency?.limit_15s ?? 0,
						used_60s: account.concurrency?.used_60s ?? 0,
						used_15s: account.concurrency?.used_15s ?? 0,
					},
				} : {}),
        ...(patch.note !== undefined ? { note: patch.note } : {}),
        ...(patch.prefix !== undefined ? { prefix: patch.prefix } : {}),
        ...(patch.proxy_url !== undefined ? { proxy_configured: patch.proxy_url.trim() !== "" } : {}),
        ...(patch.websockets !== undefined ? { websockets: patch.websockets } : {}),
        ...(patch.model_policy !== undefined ? {
          model_policy: {
            mode: patch.model_policy.mode,
            models: patch.model_policy.models,
            excluded_count: account.model_policy?.excluded_count ?? 0,
          },
        } : {}),
      };
    }),
  };
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

const defaultDeduplicationOptions: AccountDeduplicationOptions = {
  ignore_account_id: false,
  exclude_team_accounts: false,
};

type FilterState = PersistedAccountFilters;

interface EditorContext {
  title: UIMessageKey;
  scopeLabel: string;
	scope: TargetScope;
	accountID?: string;
}

const agentIdentityLoginStatePattern = /^[A-Za-z0-9._-]{1,256}$/;

function queryAgentIdentityLoginState(): string | null | undefined {
  const parameters = new URLSearchParams(window.location.search);
  if (!parameters.has("agent_identity_login")) return undefined;
  const state = parameters.get("agent_identity_login")?.trim() ?? "";
  return agentIdentityLoginStatePattern.test(state) ? state : null;
}

export default function App() {
  const agentIdentityLoginState = queryAgentIdentityLoginState();
  if (agentIdentityLoginState !== undefined) return <AgentIdentitySessionLogin loginState={agentIdentityLoginState} />;
  return <AccountManagerApp />;
}

function AccountManagerApp() {
  const { locale, tx, formatDateTime } = useI18n();
  const [authState, setAuthState] = useState<"booting" | "login" | "ready">("booting");
  const [activeView, setActiveView] = useState<"dashboard" | "accounts" | "inspection" | "providers" | "operations" | "settings">("accounts");
  const [authLoading, setAuthLoading] = useState(false);
  const [authError, setAuthError] = useState("");
  const [filters, setFilters] = useState<FilterState>(readAccountFilters);
  const [searchDraft, setSearchDraft] = useState(() => readAccountFilters().search);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(readAccountPageSize);
  const [accountSort, setAccountSort] = useState<AccountSort>(readAccountSort);
  const [data, setData] = useState<AccountListResponse>({ accounts: [], total: 0, page: 1, page_size: DEFAULT_ACCOUNT_PAGE_SIZE, pages: 0, account_concurrency: { supported: false, host_schema_version: 1, required_schema_version: 2, reason: "host_schema_v2_required" } });
  const [loading, setLoading] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [scopeMode, setScopeMode] = useState<"selected" | "filtered">("filtered");
  const [editorContext, setEditorContext] = useState<EditorContext | null>(null);
  const [detailAccount, setDetailAccount] = useState<Account | null>(null);
	const [quotaMetadataBusy, setQuotaMetadataBusy] = useState<Record<string, "refresh" | "reset">>({});
	const [tokenRefreshBusy, setTokenRefreshBusy] = useState<Record<string, boolean>>({});
	const [quotaResetTarget, setQuotaResetTarget] = useState<Account | null>(null);
  const [modelTestTarget, setModelTestTarget] = useState<Account | null>(null);
  const [modelTestResult, setModelTestResult] = useState<ModelTestResult | null>(null);
  const [modelTesting, setModelTesting] = useState(false);
  const [modelTestError, setModelTestError] = useState("");
  const [modelTestExperimentalAvailable, setModelTestExperimentalAvailable] = useState(false);
  const [weeklyOverdraftEnabled, setWeeklyOverdraftEnabled] = useState(false);
  const modelTestExperimentRequest = useRef(0);
  const [deleteTarget, setDeleteTarget] = useState<Account | null>(null);
  const [deletePreview, setDeletePreview] = useState<AccountDeletePreview | null>(null);
  const [deletePreviewing, setDeletePreviewing] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState("");
  const [deduplicationPreview, setDeduplicationPreview] = useState<AccountDeduplicationPreview | null>(null);
  const [deduplicationLoading, setDeduplicationLoading] = useState(false);
  const [deduplicationReviewing, setDeduplicationReviewing] = useState(false);
  const [deduplicationError, setDeduplicationError] = useState("");
  const deduplicationRequest = useRef(0);
  const [preview, setPreview] = useState<BatchPreview | null>(null);
  const [previewError, setPreviewError] = useState("");
  const [previewLoading, setPreviewLoading] = useState(false);
  const [starting, setStarting] = useState(false);
  const [job, setJob] = useState<JobSnapshot | null>(null);
  const [jobOpen, setJobOpen] = useState(false);
  const [retrying, setRetrying] = useState(false);
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
  const skipInitialAccountRefresh = useRef(false);
  const previewPatches = useRef(new Map<string, BatchPatch>());
  const activeJobPatch = useRef<BatchPatch | null>(null);
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
    writeAccountSort(accountSort);
  }, [accountSort]);

  useEffect(() => {
    let active = true;
    const controller = new AbortController();
    const bootstrap = async () => {
      const panelAuth = readPanelAuth();
      if (!panelAuth) {
        if (active) setAuthState("login");
        return;
      }
      setSession(panelAuth.apiBase, panelAuth.managementKey);
      try {
        const response = await api.listAccounts(1, pageSize, apiFilters, accountSort, controller.signal);
        if (!active) return;
        setData(response);
        skipInitialAccountRefresh.current = true;
        setAuthState("ready");
      } catch (error) {
        if (!active || controller.signal.aborted) return;
        // A transient CPA/plugin failure must not be mistaken for an expired
        // management key. Only an explicit 401 invalidates the session;
        // other failures stay in the authenticated shell so the operator can
        // retry instead of being silently sent back to login.
        if (error instanceof api.APIError && error.status === 401) {
          clearSession();
          setAuthState("login");
          setAuthError(operatorMessage(error.message, locale));
          return;
        }
        setAuthState("ready");
        setNotice(errorText(error, locale));
      }
    };
    void bootstrap();
    return () => {
      active = false;
      controller.abort();
    };
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

  const handleExperimentalSettingsChange = useCallback((settings: ExperimentalSettings) => {
    setWeeklyOverdraftEnabled(settings.weekly_overdraft_enabled === true);
  }, []);

  useEffect(() => {
    if (authState !== "ready") {
      setWeeklyOverdraftEnabled(false);
    }
  }, [authState]);

  const refreshAccounts = useCallback(async (silent = false, requestedPage = page, requestedFilters: AccountFilters = apiFilters, requestedSort: AccountSort = accountSort, signal?: AbortSignal) => {
    if (authState !== "ready") return;
    const requestID = accountRequest.current + 1;
    accountRequest.current = requestID;
    if (!silent) setLoading(true);
    try {
      const response = await api.listAccounts(requestedPage, pageSize, requestedFilters, requestedSort, signal);
      if (requestID !== accountRequest.current) return;
      setData(response);
      if (response.pages > 0 && requestedPage > response.pages) setPage(response.pages);
    } catch (error) {
      if (signal?.aborted) return;
      if (requestID !== accountRequest.current) return;
      handleAPIError(error);
    } finally {
      if (requestID === accountRequest.current) setLoading(false);
    }
  }, [accountSort, apiFilters, authState, handleAPIError, page, pageSize]);

	const setQuotaBusy = (accountID: string, action?: "refresh" | "reset") => {
		setQuotaMetadataBusy((current) => {
			const next = { ...current };
			if (action) next[accountID] = action;
			else delete next[accountID];
			return next;
		});
	};

	const refreshAccountToken = async (account: Account) => {
		if (!account.editable || tokenRefreshBusy[account.id]) return;
		setTokenRefreshBusy((current) => ({ ...current, [account.id]: true }));
		try {
			const result = await api.refreshAccountToken(account.id);
			setData((current) => ({
				...current,
				accounts: current.accounts.map((entry) => entry.id === account.id
					? { ...entry, last_refresh: result.refreshed_at, updated_at: result.refreshed_at }
					: entry),
			}));
			setNotice(tx("ui.token_refreshed_for_account", { account: account.label || account.email || account.name || account.id }));
		} catch (error) {
			handleAPIError(error);
		} finally {
			setTokenRefreshBusy((current) => {
				const next = { ...current };
				delete next[account.id];
				return next;
			});
		}
	};

	const refreshQuotaMetadata = async (account: Account) => {
		setQuotaBusy(account.id, "refresh");
		try {
			const result = await api.refreshAccountQuotaMetadata(account.id);
			await refreshAccounts(true);
			setNotice(tx(result.warning ? "ui.quota_metadata_refreshed_with_warning" : "ui.quota_metadata_refreshed", { account: account.label || account.email || account.name || account.id }));
		} catch (error) {
			handleAPIError(error);
		} finally {
			setQuotaBusy(account.id);
		}
	};

	const confirmQuotaReset = async () => {
		if (!quotaResetTarget) return;
		const account = quotaResetTarget;
		setQuotaBusy(account.id, "reset");
		try {
			const result = await api.useAccountActiveReset(account.id);
			await refreshAccounts(true);
			setQuotaResetTarget(null);
			setNotice(tx(result.warning ? "ui.active_reset_succeeded_with_warning" : "ui.active_reset_succeeded", { account: account.label || account.email || account.name || account.id }));
		} catch (error) {
			handleAPIError(error);
		} finally {
			setQuotaBusy(account.id);
		}
	};

  const requestInspectionAccountSync = useCallback(() => {
    // The polling owner reads inspection state first and then refreshes accounts.
    // Keeping both operations in one sequence avoids a stale eager refresh plus
    // a duplicate refresh when the first poll starts.
    setInspectionAccountSyncActive(true);
  }, []);

  useEffect(() => {
    if (activeView !== "accounts") return;
    if (skipInitialAccountRefresh.current) {
      skipInitialAccountRefresh.current = false;
      return;
    }
    void refreshAccounts();
  }, [activeView, refreshAccounts]);

  useEffect(() => {
    if (!importUsageCollectionActive || authState !== "ready") return;
    let cancelled = false;
    let timer = 0;
    const controller = new AbortController();
    const poll = async () => {
      try {
        const snapshot = await api.getInspection(controller.signal);
        if (cancelled) return;
        await refreshAccounts(true, page, apiFilters, accountSort, controller.signal);
        if (cancelled) return;
        if (snapshot.running || snapshot.pending || snapshot.probe_sweep_remaining > 0) {
          timer = window.setTimeout(poll, 1500);
        } else {
          setImportUsageCollectionActive(false);
        }
      } catch (error) {
        if (!cancelled && !controller.signal.aborted) {
          setImportUsageCollectionActive(false);
          handleAPIError(error);
        }
      }
    };
    timer = window.setTimeout(poll, 700);
    return () => {
      cancelled = true;
      controller.abort();
      window.clearTimeout(timer);
    };
  }, [accountSort, apiFilters, authState, handleAPIError, importUsageCollectionActive, page, refreshAccounts]);

  useEffect(() => {
    if (authState !== "ready" || importResult?.state !== "running") return;
    let cancelled = false;
    let timer = 0;
    const controller = new AbortController();
    const poll = async () => {
      try {
        const result = await api.getImportStatus(controller.signal);
        if (cancelled) return;
        setImportResult(result);
        if (result.state === "running") {
          timer = window.setTimeout(poll, 1000);
          return;
        }
        setImportUsageCollectionActive(Boolean(result.usage_collection_started));
        setNotice(result.failed || result.skipped
          ? tx("ui.added_count_accounts_failed_not_written", { count: result.imported, failed: result.failed + result.skipped })
          : tx("ui.added_count_accounts", { count: result.imported }));
        void refreshAccounts(false, page, apiFilters, accountSort, controller.signal);
      } catch (error) {
        if (!cancelled && !controller.signal.aborted) {
          setImportError(errorText(error, locale));
          if (error instanceof api.APIError && error.status === 401) handleAPIError(error);
        }
      }
    };
    timer = window.setTimeout(poll, 500);
    return () => {
      cancelled = true;
      controller.abort();
      window.clearTimeout(timer);
    };
  }, [authState, handleAPIError, importResult?.state, locale, refreshAccounts, tx]);

  useEffect(() => {
    if (!inspectionAccountSyncActive || authState !== "ready") return;
    let cancelled = false;
    let timer = 0;
    const controller = new AbortController();
    const poll = async () => {
      try {
        const snapshot = await api.getInspection(controller.signal);
        if (cancelled) return;
        await refreshAccounts(true, page, apiFilters, accountSort, controller.signal);
        if (cancelled) return;
        if (snapshot.running || snapshot.pending || snapshot.probe_sweep_status === "running") {
          timer = window.setTimeout(poll, 1200);
        } else {
          setInspectionAccountSyncActive(false);
        }
      } catch (error) {
        if (!cancelled && !controller.signal.aborted) {
          setInspectionAccountSyncActive(false);
          handleAPIError(error);
        }
      }
    };
    timer = window.setTimeout(poll, 400);
    return () => {
      cancelled = true;
      controller.abort();
      window.clearTimeout(timer);
    };
  }, [accountSort, apiFilters, authState, handleAPIError, inspectionAccountSyncActive, page, refreshAccounts]);

  useEffect(() => {
    if (authState !== "ready") return;
    const controller = new AbortController();
    void api.getJobStatus(true, controller.signal).then((snapshot) => {
      if (!controller.signal.aborted && snapshot.id) setJob(snapshot);
    }).catch((error) => {
      if (!controller.signal.aborted) setNotice(errorText(error, locale));
    });
    return () => controller.abort();
  }, [authState, locale]);

  useEffect(() => {
    if (!job?.running) return;
    let cancelled = false;
    let timer = 0;
    const controller = new AbortController();
    const poll = async () => {
      try {
        const light = await api.getJobStatus(false, controller.signal);
        if (cancelled) return;
        if (light.running) {
          setJob((current) => ({ ...light, results: current?.results }));
          timer = window.setTimeout(poll, 850);
          return;
        }
        const full = await api.getJobStatus(true, controller.signal);
        if (!cancelled) {
          setJob(full);
          setData((current) => applyCompletedAccountPatch(current, full, activeJobPatch.current));
        }
      } catch (error) {
        if (!cancelled && !controller.signal.aborted) handleAPIError(error);
      }
    };
    timer = window.setTimeout(poll, 500);
    return () => {
      cancelled = true;
      controller.abort();
      window.clearTimeout(timer);
    };
  }, [handleAPIError, job?.id, job?.running, refreshAccounts]);

  useEffect(() => {
    if (!forceJob?.running) return;
    let cancelled = false;
    let timer = 0;
    const controller = new AbortController();
    const poll = async () => {
      try {
        const light = await api.getForceSyncStatus(false, controller.signal);
        if (cancelled) return;
        if (light.running) {
          setForceJob((current) => ({ ...light, results: current?.results }));
          timer = window.setTimeout(poll, 850);
          return;
        }
        const full = await api.getForceSyncStatus(true, controller.signal);
        if (!cancelled) {
          setForceJob(full);
          void refreshAccounts(false, page, apiFilters, accountSort, controller.signal);
        }
      } catch (error) {
        if (!cancelled && !controller.signal.aborted) handleAPIError(error);
      }
    };
    timer = window.setTimeout(poll, 500);
    return () => {
      cancelled = true;
      controller.abort();
      window.clearTimeout(timer);
    };
  }, [accountSort, apiFilters, forceJob?.id, forceJob?.running, handleAPIError, page, refreshAccounts]);

  const login = async (baseURL: string, managementKey: string) => {
    setAuthLoading(true);
    setAuthError("");
    setSession(baseURL, managementKey);
    try {
      const response = await api.listAccounts(1, pageSize, apiFilters, accountSort);
      setData(response);
      skipInitialAccountRefresh.current = true;
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

  const updateAccountSort = (field: AccountSortField) => {
    setAccountSort((current) => ({
      field,
      order: current.field === field && current.order === "asc" ? "desc" : "asc",
    }));
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
      previewPatches.current.set(response.id, patch);
      setPreview(response);
    } catch (error) {
      handleAPIError(error);
    } finally {
      setPreviewLoading(false);
    }
  };

  const beginAccountStatePreview = (account: Account, disabled: boolean) => {
    if (!account.editable || account.disabled === disabled) return;
    void beginPreview({ disabled }, { mode: "selected", ids: [account.id] });
  };

  const beginBatchDelete = async () => {
    setPreviewLoading(true);
    setPreviewError("");
    try {
      setPreview(await api.createBatchDeletePreview(targetScope()));
    } catch (error) {
      handleAPIError(error);
    } finally {
      setPreviewLoading(false);
    }
  };

  const loadAccountDeduplication = async (options: AccountDeduplicationOptions, keepDialogOpen: boolean) => {
    const requestID = ++deduplicationRequest.current;
    setDeduplicationLoading(true);
    setDeduplicationError("");
    try {
      const nextPreview = await api.scanAccountDuplicates(options);
      if (deduplicationRequest.current === requestID) setDeduplicationPreview(nextPreview);
    } catch (error) {
      if (deduplicationRequest.current !== requestID) return;
      if (keepDialogOpen && !(error instanceof api.APIError && error.status === 401)) {
        setDeduplicationError(errorText(error, locale));
      } else {
        handleAPIError(error);
      }
    } finally {
      if (deduplicationRequest.current === requestID) setDeduplicationLoading(false);
    }
  };

  const openAccountDeduplication = async () => {
    await loadAccountDeduplication(defaultDeduplicationOptions, false);
  };

  const reviewDuplicateDeletions = async (accountIDs: string[]) => {
    if (accountIDs.length === 0) return;
    setDeduplicationReviewing(true);
    setDeduplicationError("");
    try {
      const deletePreview = await api.createBatchDeletePreview({ mode: "selected", ids: accountIDs });
      setDeduplicationPreview(null);
      setPreview(deletePreview);
    } catch (error) {
      if (error instanceof api.APIError && error.status === 401) {
        setDeduplicationPreview(null);
        handleAPIError(error);
      } else {
        setDeduplicationError(errorText(error, locale));
      }
    } finally {
      setDeduplicationReviewing(false);
    }
  };

  const openAccountEditor = (account: Account) => {
    if (!account.editable) return;
    setDetailAccount(null);
    setEditorContext({
      title: "ui.edit_account",
      scopeLabel: account.label || account.email || account.name || account.id,
      scope: { mode: "selected", ids: [account.id] },
		accountID: account.id,
    });
  };

  const openModelTest = (account: Account) => {
    const requestID = modelTestExperimentRequest.current + 1;
    modelTestExperimentRequest.current = requestID;
    setModelTestTarget(account);
    setModelTestResult(null);
    setModelTestError("");
    setModelTestExperimentalAvailable(weeklyOverdraftEnabled);
    void api.getExperimentalSettings().then((snapshot) => {
      if (modelTestExperimentRequest.current !== requestID) return;
      setModelTestExperimentalAvailable(snapshot.settings.weekly_overdraft_enabled === true);
    }).catch((error) => {
      if (modelTestExperimentRequest.current !== requestID) return;
      if (error instanceof api.APIError && error.status === 401) {
        setModelTestTarget(null);
        handleAPIError(error);
      } else {
        setModelTestError(errorText(error, locale));
      }
    });
  };

  const closeModelTest = () => {
    if (modelTesting) return;
    modelTestExperimentRequest.current += 1;
    setModelTestTarget(null);
    setModelTestResult(null);
    setModelTestError("");
    setModelTestExperimentalAvailable(false);
  };

  const runModelTest = async (model: string, experimentalWeeklyOverdraft = false) => {
    if (!modelTestTarget) return;
    setModelTesting(true);
    setModelTestError("");
    setModelTestResult(null);
    try {
      setModelTestResult(await api.testAccountModel(modelTestTarget.id, model, experimentalWeeklyOverdraft));
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
      const deletingBatch = preview.operation === "delete";
      const submittedPatch = deletingBatch ? null : previewPatches.current.get(preview.id) ?? null;
      const snapshot = deletingBatch
        ? await api.startBatchDelete(preview.id)
        : await api.startBatch(preview.id);
      previewPatches.current.delete(preview.id);
      activeJobPatch.current = submittedPatch;
      setPreview(null);
      if (deletingBatch) {
        setSelected(new Set());
        setScopeMode("filtered");
      }
      setJob(snapshot);
      if (!snapshot.running) setData((current) => applyCompletedAccountPatch(current, snapshot, activeJobPatch.current));
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
      if (!snapshot.running) setData((current) => applyCompletedAccountPatch(current, snapshot, activeJobPatch.current));
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

  const previewForceSync = async () => {
    setForcePreviewLoading(true);
    setForcePreviewError("");
    try {
      setForcePreview(await api.createForceSyncPreview());
    } catch (error) {
      if (error instanceof api.APIError && error.status === 401) handleAPIError(error);
      else setNotice(errorText(error, locale));
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
    void api.getImportStatus().then((result) => {
      if (result.state === "running") setImportResult(result);
    }).catch((error) => {
      if (error instanceof api.APIError && error.status === 401) handleAPIError(error);
    });
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
      setNotice(tx("ui.import_started_in_background"));
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
  const hasCustomAccountSort = !isDefaultAccountSort(accountSort);
  const hasAccountViewPreferences = hasActiveFilters || hasCustomAccountSort;
  const panelOpen = Boolean(jobOpen && job || forceJobOpen && forceJob);

  return (
    <div className={`app-shell ${panelOpen ? "with-job-panel" : ""}`}>
      {authState === "ready" ? <PluginUpdateAutomation onAPIError={handleAPIError} onNotice={setNotice} /> : null}
      <aside className="app-sidebar">
        <div className="sidebar-brand">
          <span className="brand-icon"><FileCog size={21} /></span>
          <div>
            <strong>CPA</strong>
            <span>Account Config Manager</span>
          </div>
        </div>
        <div className="sidebar-version"><span>{tx("ui.observability_console")}</span><code>native plugin</code></div>
        <nav className="workspace-tabs sidebar-nav" aria-label={tx("ui.account_management_views")}>
          <button type="button" className={activeView === "dashboard" ? "active" : ""} aria-current={activeView === "dashboard" ? "page" : undefined} onClick={() => setActiveView("dashboard")}><Gauge size={16} /><span>{tx("ui.dashboard")}</span></button>
          <button type="button" className={activeView === "accounts" ? "active" : ""} aria-current={activeView === "accounts" ? "page" : undefined} onClick={() => setActiveView("accounts")}><FileCog size={16} /><span>{tx("ui.accounts")}</span></button>
          <button type="button" className={activeView === "inspection" ? "active" : ""} aria-current={activeView === "inspection" ? "page" : undefined} onClick={() => setActiveView("inspection")}><Activity size={16} /><span>{tx("ui.inspection_and_automation")}</span></button>
          <button type="button" className={activeView === "providers" ? "active" : ""} aria-current={activeView === "providers" ? "page" : undefined} onClick={() => setActiveView("providers")}><Boxes size={16} /><span>{tx("ui.ai_providers")}</span></button>
          <button type="button" className={activeView === "operations" ? "active" : ""} aria-current={activeView === "operations" ? "page" : undefined} onClick={() => setActiveView("operations")}><ScrollText size={16} /><span>{tx("ui.operation_log")}</span></button>
          <button type="button" className={activeView === "settings" ? "active" : ""} aria-current={activeView === "settings" ? "page" : undefined} onClick={() => setActiveView("settings")}><Settings2 size={16} /><span>{tx("ui.other_settings")}</span></button>
        </nav>
        <div className="sidebar-telemetry" aria-live="polite">
          <div className="sidebar-telemetry-row"><span><ShieldCheck size={14} />{tx("ui.accounts")}</span><strong>{data.total}</strong></div>
          <div className="sidebar-telemetry-row"><span><Wifi size={14} />{tx("ui.system_status")}</span><strong>{job?.running || forceJob?.running ? tx("ui.running") : tx("ui.ready")}</strong></div>
          {job?.id || forceJob?.id ? <div className="sidebar-job-summary"><span>{tx("ui.current_job")}</span><strong>{job?.running ? `${job.done}/${job.total}` : forceJob?.running ? `${forceJob.done}/${forceJob.total}` : jobStateLabel((job ?? forceJob)!.state)}</strong></div> : null}
        </div>
      </aside>
      <div className="page-frame app-content">
        <header className="app-header">
          <div className="page-context">
            <span className="page-eyebrow">{tx("ui.observability_console")}</span>
            <h1>{tx("ui.account_management")}</h1>
            <p>{activeView === "dashboard"
              ? tx("ui.dashboard")
              : activeView === "accounts"
                ? tx("ui.accounts")
                : activeView === "inspection"
                  ? tx("ui.inspection_and_automation")
                  : activeView === "providers"
                    ? tx("ui.ai_providers")
                    : activeView === "operations"
                      ? tx("ui.operation_log")
                      : tx("ui.other_settings")} · CPA Account Config Manager</p>
          </div>
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
                <button className="button header-deduplicate-account" type="button" title={tx("ui.deduplicate_accounts")} aria-label={tx("ui.deduplicate_accounts")} disabled={deduplicationLoading} onClick={() => void openAccountDeduplication()}>{deduplicationLoading ? <LoaderCircle className="spin" size={16} /> : <CopyCheck size={16} />}<span>{tx("ui.deduplicate_accounts")}</span></button>
                <IconButton className="export-action" label={tx("ui.download_filtered_credentials")} onClick={() => openExport("accounts")}><Download size={17} /></IconButton>
                <IconButton label={tx("ui.refresh_accounts")} onClick={() => void refreshAccounts()} disabled={loading}><RefreshCw className={loading ? "spin" : ""} size={17} /></IconButton>
              </> : null}
              <a className="icon-button" href="https://github.com/Mxucc/cpa-account-config-manager/" target="_blank" rel="noopener noreferrer" aria-label={tx("ui.open_project_on_github")} title={tx("ui.open_project_on_github")}><Github size={17} /></a>
            </div>
          </div>
        </header>
        {data.usage_storage_error ? <div className="notice-bar global-notice warning-notice" role="alert"><span>{tx("ui.usage_storage_error")}</span></div> : null}
        {data.account_concurrency?.storage_error ? <div className="notice-bar global-notice warning-notice" role="alert"><span>{tx("ui.account_concurrency_storage_error")}</span></div> : null}
        {notice ? <div className="notice-bar global-notice" role="alert"><span>{notice}</span><IconButton label={tx("ui.dismiss_notification")} onClick={() => setNotice("")}><X size={15} /></IconButton></div> : null}

        {activeView === "dashboard" ? (
          <DashboardWorkspace onAPIError={handleAPIError} />
        ) : activeView === "accounts" ? (
        <section className="account-panel">
          <section className="filter-bar" aria-label={tx("ui.account_filters")}>
            <div className="filter-heading">
              <div><strong>{tx("ui.filter_accounts")}</strong><span>{tx(hasAccountViewPreferences ? "ui.filters_or_sort_applied" : "ui.all_accounts")}</span></div>
              <button className="button button-quiet reset-filters" type="button" disabled={!hasAccountViewPreferences} onClick={() => { setFilters({ ...EMPTY_ACCOUNT_FILTERS }); setSearchDraft(""); setAccountSort({ ...DEFAULT_ACCOUNT_SORT }); setPage(1); setSelected(new Set()); }}>
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
								<col className="col-type" /><col className="col-activity" /><col className="col-active-reset" /><col className="col-concurrency" /><col className="col-created" /><col className="col-disabled-at" /><col className="col-access" />
              <col className="col-state" /><col className="col-priority" /><col className="col-routing" /><col className="col-actions" />
            </colgroup>
            <thead>
              <tr>
                <th className="select-header"><input type="checkbox" checked={allPageSelected} onChange={togglePage} aria-label={tx("ui.select_editable_accounts_on_this_page")} /></th>
								<SortableAccountHeader className="identity-header" field="account" label={tx("ui.accounts")} sort={accountSort} onSort={updateAccountSort} />
								<SortableAccountHeader field="provider" label={tx("ui.provider")} sort={accountSort} onSort={updateAccountSort} />
								<SortableAccountHeader field="type" label={tx("ui.type")} sort={accountSort} onSort={updateAccountSort} />
								<SortableAccountHeader field="usage" label={tx("ui.usage")} sort={accountSort} onSort={updateAccountSort} />
								<SortableAccountHeader field="active_reset_count" label={tx("ui.active_reset_count")} sort={accountSort} onSort={updateAccountSort} />
								<SortableAccountHeader field="concurrency" label={tx("ui.account_concurrency")} sort={accountSort} onSort={updateAccountSort} />
								<SortableAccountHeader field="created_at" label={tx("ui.initial_time")} sort={accountSort} onSort={updateAccountSort} />
								<SortableAccountHeader field="disabled_at" label={tx("ui.disabled_time")} sort={accountSort} onSort={updateAccountSort} />
								<SortableAccountHeader field="access" label={tx("ui.access")} sort={accountSort} onSort={updateAccountSort} />
								<SortableAccountHeader field="status" label={tx("ui.status")} sort={accountSort} onSort={updateAccountSort} />
								<SortableAccountHeader field="priority" label={tx("ui.priority")} sort={accountSort} onSort={updateAccountSort} />
								<SortableAccountHeader field="routing" label={tx("ui.routing")} sort={accountSort} onSort={updateAccountSort} />
								<th className="actions-header">{tx("ui.actions")}</th>
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
                  <td><AccountUsageCell account={account} weeklyOverdraftEnabled={weeklyOverdraftEnabled} creditUsageEnabled /></td>
									<td><AccountQuotaMetadataCell account={account} busy={quotaMetadataBusy[account.id]} onRefresh={() => void refreshQuotaMetadata(account)} onReset={() => setQuotaResetTarget(account)} /></td>
									<td><AccountConcurrencyCell account={account} /></td>
									<td><AccountLifecycleTime value={account.created_at} /></td>
									<td><AccountLifecycleTime value={account.disabled_at} /></td>
                  <td>{account.editable ? <span className="access-tag editable"><Settings2 size={13} />{tx("ui.editable")}</span> : <span className="access-tag readonly" title={operatorMessage(account.read_only_reason, locale)}><LockKeyhole size={13} />{tx("ui.read_only")}</span>}</td>
                  <td><StateCell account={account} /></td>
                  <td><code className="priority-value">{account.priority ?? "-"}</code></td>
                  <td><RoutingCell account={account} /></td>
                  <td className="actions-cell">
                    <div className="row-actions">
                      <IconButton label={tx("ui.view_account", { account: identity })} onClick={() => setDetailAccount(account)}><Eye size={15} /></IconButton>
                      <IconButton label={tx("ui.test_model_for_account", { account: identity })} onClick={() => openModelTest(account)}><Activity size={15} /></IconButton>
                      <IconButton label={account.editable ? tx("ui.edit_account_2", { account: identity }) : readOnlyReason} disabled={!account.editable} onClick={() => openAccountEditor(account)}><Pencil size={15} /></IconButton>
                      <IconButton
                        className="row-enable-action"
                        label={tx("ui.enable_account_2", { account: identity })}
                        title={!account.editable ? readOnlyReason : undefined}
                        disabled={previewLoading || !account.editable || !account.disabled}
                        onClick={() => beginAccountStatePreview(account, false)}
                      ><Power size={15} /></IconButton>
                      <IconButton
                        className="row-disable-action"
                        label={tx("ui.disable_account_2", { account: identity })}
                        title={!account.editable ? readOnlyReason : undefined}
                        disabled={previewLoading || !account.editable || account.disabled}
                        onClick={() => beginAccountStatePreview(account, true)}
                      ><PowerOff size={15} /></IconButton>
											<AccountActionsMenu
												label={tx("ui.more_actions_for_account", { account: identity })}
												menuLabel={tx("ui.account_more_actions")}
												refreshLabel={tx("ui.refresh_token")}
												deleteLabel={tx("ui.delete_account")}
												disabled={!account.editable}
												disabledReason={readOnlyReason}
												refreshing={Boolean(tokenRefreshBusy[account.id])}
												onRefresh={() => void refreshAccountToken(account)}
												onDelete={() => void openDelete(account)}
											/>
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
        ) : activeView === "providers" ? (
          <AIProvidersSettings refreshRevision={0} onAPIError={handleAPIError} onNotice={setNotice} />
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
          <OtherSettingsWorkspace
            onAPIError={handleAPIError}
            onNotice={setNotice}
            forceLoading={forcePreviewLoading}
            onForcePreview={() => void previewForceSync()}
            onExperimentalSettingsChange={handleExperimentalSettingsChange}
          />
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
            <button className="button button-primary" type="button" disabled={previewLoading} onClick={() => setEditorContext({ title: "ui.batch_edit", scopeLabel, scope: targetScope() })}>
              {previewLoading ? <LoaderCircle className="spin" size={16} /> : <SlidersHorizontal size={16} />}{tx("ui.batch_edit")}
            </button>
            <button className="button button-danger" type="button" disabled={previewLoading} onClick={() => void beginBatchDelete()}>
              <Trash2 size={16} />{tx("ui.batch_delete")}
            </button>
          </div>
        </footer>
      ) : null}

      {authState === "booting" ? <div className="auth-loading"><LoaderCircle className="spin" size={24} /></div> : null}
      {authState === "login" ? <LoginDialog loading={authLoading} error={authError} onSubmit={login} /> : null}
      {editorContext ? <BatchEditor title={editorContext.title} scopeLabel={editorContext.scopeLabel} accountConcurrency={data.account_concurrency} loadModels={() => api.loadAccountModels(editorContext.scope)} loadCurrentConfig={editorContext.accountID ? () => api.loadAccountConfig(editorContext.accountID || "") : undefined} onLoadError={(error) => { if (error instanceof api.APIError && error.status === 401) { setEditorContext(null); handleAPIError(error); } }} onClose={() => setEditorContext(null)} onSubmit={(patch) => { const scope = editorContext.scope; setEditorContext(null); void beginPreview(patch, scope); }} /> : null}
      {detailAccount ? <AccountDetailsDialog account={detailAccount} creditUsageEnabled weeklyOverdraftEnabled={weeklyOverdraftEnabled} onClose={() => setDetailAccount(null)} onEdit={() => openAccountEditor(detailAccount)} /> : null}
			{quotaResetTarget ? (
				<Modal
					title={tx("ui.confirm_active_reset")}
					onClose={() => { if (!quotaMetadataBusy[quotaResetTarget.id]) setQuotaResetTarget(null); }}
					footer={(
						<>
							<button className="button" type="button" disabled={Boolean(quotaMetadataBusy[quotaResetTarget.id])} onClick={() => setQuotaResetTarget(null)}>{tx("ui.cancel")}</button>
							<button className="button button-primary" type="button" disabled={Boolean(quotaMetadataBusy[quotaResetTarget.id])} onClick={() => void confirmQuotaReset()}>
								{quotaMetadataBusy[quotaResetTarget.id] ? <LoaderCircle className="spin" size={15} /> : <RotateCcw size={15} />}{tx("ui.use_active_reset")}
							</button>
						</>
					)}
				>
					<p className="confirmation-copy">{tx("ui.confirm_active_reset_description", { account: quotaResetTarget.label || quotaResetTarget.email || quotaResetTarget.name || quotaResetTarget.id })}</p>
				</Modal>
			) : null}
      {modelTestTarget ? <ModelTestDialog key={modelTestTarget.id} account={modelTestTarget} result={modelTestResult} error={modelTestError} testing={modelTesting} experimentalAvailable={modelTestExperimentalAvailable} onClose={closeModelTest} onTest={(model, experimental) => void runModelTest(model, experimental)} /> : null}
      {deleteTarget ? <DeleteAccountDialog key={deleteTarget.id} account={deleteTarget} preview={deletePreview} previewing={deletePreviewing} deleting={deleting} error={deleteError} onClose={closeDelete} onConfirm={() => void confirmDelete()} /> : null}
      {deduplicationPreview ? <AccountDeduplicationDialog preview={deduplicationPreview} loading={deduplicationLoading} reviewing={deduplicationReviewing} error={deduplicationError} onClose={() => { deduplicationRequest.current++; setDeduplicationPreview(null); setDeduplicationError(""); setDeduplicationLoading(false); }} onOptionsChange={(options) => void loadAccountDeduplication(options, true)} onReview={(ids) => void reviewDuplicateDeletions(ids)} /> : null}
      {preview ? <PreviewDialog preview={preview} starting={starting} error={previewError} onClose={() => { previewPatches.current.delete(preview.id); setPreview(null); setPreviewError(""); }} onConfirm={() => void confirmPreview()} /> : null}
      {jobOpen && job ? <JobPanel job={job} title={job.operation === "delete" ? "ui.batch_delete_job" : "ui.batch_job"} ariaLabel={job.operation === "delete" ? "ui.batch_delete_job" : "ui.batch_job"} retrying={retrying} onClose={() => setJobOpen(false)} onRetry={() => void retryJob()} onExport={() => openExport("results")} onRefresh={() => void refreshJob()} /> : null}
      {importOpen ? <ImportDialog preview={importPreview} result={importResult} previewing={importPreviewing} importing={importStarting} error={importError} onClose={closeImport} onPreview={(files) => void previewImport(files)} onImport={() => void confirmImport()} onReset={resetImport} /> : null}
      {exportTarget ? <ExportDialog kind={exportTarget} count={exportCount} scopeLabel={exportScopeLabel} exporting={exporting} error={exportError} onClose={closeExport} onExport={(format) => void confirmExport(format)} /> : null}
      {forcePreview ? <ForceSyncPreviewDialog preview={forcePreview} starting={forceStarting} error={forcePreviewError} onClose={() => { setForcePreview(null); setForcePreviewError(""); }} onConfirm={() => void confirmForceSync()} /> : null}
      {forceJobOpen && forceJob ? <JobPanel job={forceJob} title="ui.default_policy_sync" ariaLabel="ui.default_policy_force_sync" fields={forceJob.policy.fields} onClose={() => setForceJobOpen(false)} onRefresh={() => void refreshForceJob()} /> : null}
    </div>
  );
}

function AccountTypeCell({ account }: { account: Account }) {
	const { tx } = useI18n();
  const primary = account.plan_type || account.account_type || account.type || "-";
  const secondary = account.plan_type ? account.account_type || account.type : "";
	const primaryLabel = primary === "agent_identity" ? tx("ui.agent_identity") : primary === "personal_access_token" ? tx("ui.codex_personal_access_token") : primary;
	const secondaryLabel = secondary === "agent_identity" ? tx("ui.agent_identity") : secondary === "personal_access_token" ? tx("ui.codex_personal_access_token") : secondary;
  return (
    <div className="type-cell" title={secondaryLabel ? `${primaryLabel} / ${secondaryLabel}` : primaryLabel}>
      <strong className="account-plan-type">{primaryLabel}</strong>
      {secondaryLabel && secondaryLabel !== primaryLabel ? <span>{secondaryLabel}</span> : null}
    </div>
  );
}

function SortableAccountHeader({ className, field, label, sort, onSort }: {
	className?: string;
	field: AccountSortField;
	label: string;
	sort: AccountSort;
	onSort: (field: AccountSortField) => void;
}) {
	const { tx } = useI18n();
	const active = sort.field === field;
	const nextOrder = active && sort.order === "asc" ? "desc" : "asc";
	const nextOrderLabel = tx(nextOrder === "asc" ? "ui.ascending" : "ui.descending");
	const currentOrderLabel = tx(sort.order === "asc" ? "ui.ascending" : "ui.descending");
	const buttonLabel = active
		? tx("ui.sort_column_current_next", { column: label, current: currentOrderLabel, next: nextOrderLabel })
		: tx("ui.sort_column_next", { column: label, next: nextOrderLabel });
	return (
		<th className={className} aria-sort={active ? sort.order === "asc" ? "ascending" : "descending" : "none"}>
			<button className={`table-sort-button ${active ? "is-active" : ""}`} type="button" title={buttonLabel} aria-label={buttonLabel} onClick={() => onSort(field)}>
				<span>{label}</span>
				{active ? sort.order === "asc" ? <ArrowUp size={13} aria-hidden="true" /> : <ArrowDown size={13} aria-hidden="true" /> : <ArrowUpDown size={13} aria-hidden="true" />}
			</button>
		</th>
	);
}

function AccountLifecycleTime({ value }: { value?: string }) {
	const { formatDateTime } = useI18n();
	if (!value) return <span className="account-time-empty">-</span>;
	const formatted = formatDateTime(value);
	return <time className="account-lifecycle-time" dateTime={value} title={formatted}>{formatted}</time>;
}

function AccountQuotaMetadataCell({ account, busy, onRefresh, onReset }: { account: Account; busy?: "refresh" | "reset"; onRefresh: () => void; onReset: () => void }) {
	const { tx, formatNumber, formatDateTime } = useI18n();
	const provider = String(account.provider || account.type).trim().toLowerCase();
	const supported = provider === "codex" || provider === "codex-agent-identity";
	const count = account.usage?.codex?.active_reset_count;
	const known = typeof count === "number" && Number.isFinite(count) && count >= 0;
	const observedAt = account.usage?.codex?.metadata_observed_at;
	if (!supported) return <span className="quota-metadata-unsupported">-</span>;
	return (
		<div className="quota-metadata-cell">
			<div className="quota-metadata-value"><strong>{known ? formatNumber(count) : "-"}</strong><IconButton label={tx("ui.refresh_plan_and_active_reset", { account: account.label || account.email || account.name || account.id })} disabled={Boolean(busy)} onClick={onRefresh}>{busy === "refresh" ? <LoaderCircle className="spin" size={14} /> : <RefreshCw size={14} />}</IconButton></div>
			{known && count > 0 ? <button className="quota-reset-button" type="button" disabled={Boolean(busy)} onClick={onReset}>{busy === "reset" ? <LoaderCircle className="spin" size={12} /> : <RotateCcw size={12} />}{tx("ui.use_active_reset")}</button> : null}
			<small>{observedAt ? tx("ui.quota_metadata_collected_at", { time: formatDateTime(observedAt) }) : tx("ui.not_collected")}</small>
		</div>
	);
}

function AccountConcurrencyCell({ account }: { account: Account }) {
	const { tx } = useI18n();
	if (!account.concurrency?.supported) {
		const explanation = tx("ui.account_concurrency_unavailable_old_cpa");
		return <span className="concurrency-unavailable" title={explanation} aria-label={`${tx("ui.unavailable")}: ${explanation}`} tabIndex={0}>{tx("ui.unavailable")}<CircleHelp size={12} aria-hidden="true" /></span>;
	}
	const minuteLimit = accountConcurrencyLimitLabel(account.concurrency, "60s");
	const fifteenSecondLimit = accountConcurrencyLimitLabel(account.concurrency, "15s");
	const saturated = accountConcurrencySaturated(account.concurrency);
	return (
		<div className={`concurrency-cell ${saturated ? "is-saturated" : ""}`} title={tx("ui.account_concurrency_dual_window_title", { active: account.concurrency.active, used15s: account.concurrency.used_15s, limit15s: fifteenSecondLimit, used60s: account.concurrency.used_60s, limit60s: minuteLimit })}>
			<span>{tx("ui.account_concurrency_active_short")} <strong>{account.concurrency.active}</strong></span>
			<span>{tx("ui.account_concurrency_15s_short")} <strong>{account.concurrency.used_15s}</strong>/<strong>{fifteenSecondLimit}</strong></span>
			<span>{tx("ui.account_concurrency_60s_short")} <strong>{account.concurrency.used_60s}</strong>/<strong>{minuteLimit}</strong></span>
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
	return <>{Array.from({ length: 8 }, (_, index) => <tr className="loading-row" key={index}><td colSpan={14}><span /></td></tr>)}</>;
}

function errorText(error: unknown, locale: Locale = "zh-CN"): string {
  return error instanceof Error ? operatorMessage(error.message, locale) : translateUI(locale, "ui.request_failed");
}

import { Fragment, useCallback, useEffect, useRef, useState } from "react";
import { Activity, AlertTriangle, Coins, Download, ExternalLink, KeyRound, Link2, LoaderCircle, Plus, Power, Radio, RefreshCw, RotateCcw, Save, Search, Trash2, Wrench } from "lucide-react";
import * as api from "../api/client";
import { operatorMessage } from "../format/operatorMessage";
import { useI18n } from "../i18n";
import type { OpenCodeAccountView, OpenCodeChannelView, OpenCodeStorageInfo, OpenCodeModelControlSnapshot, OpenCodeModelPrice, OpenCodeModelTestResult, OpenCodePricingSnapshot, OpenCodeQuotaResult, OpenCodeSessionSnapshot, OpenCodeZenAccountView } from "../types";
import { IconButton } from "./IconButton";
import { ModelProbeDialog } from "./ModelProbeDialog";

interface OpenCodeWorkspaceProps {
  refreshRevision: number;
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
}

type OpenCodeKind = "go" | "zen";

/** The workspace is split into tabs so overview, accounts, imports and prices stay reachable. */
type OpenCodeTab = "overview" | "go" | "zen" | "channels" | "models";

interface ModelTarget {
  kind: OpenCodeKind;
  accountID: string;
  label: string;
  models: string[];
}

interface OpenCodeTestCandidate {
  kind: OpenCodeKind;
  accountID: string;
  label: string;
}

/** Prices are USD per million tokens; tiny values keep four decimals. */
function formatPriceUSD(value: number | undefined): string {
  if (typeof value !== "number" || Number.isNaN(value)) return "-";
  if (value === 0) return "$0";
  if (value < 0.01) return `$${value.toFixed(4)}`;
  return `$${value.toFixed(2)}`;
}

/**
 * Allowances and subscription amounts are USD figures that are normally whole
 * dollars, so they reuse the locale number formatter instead of formatPriceUSD.
 */
function formatAllowanceUSD(value: number | undefined, formatNumber: (value: number) => string): string {
  if (typeof value !== "number" || Number.isNaN(value)) return "-";
  return `$${formatNumber(value)}`;
}

/** Compact 5-hour / weekly / monthly request estimate; undefined when the docs have none. */
function formatEstimatedRequests(requests: OpenCodeModelPrice["estimated_requests"], formatNumber: (value: number) => string): string | undefined {
  const windows = [requests?.five_hour, requests?.weekly, requests?.monthly];
  if (!windows.some((value) => typeof value === "number" && !Number.isNaN(value))) return undefined;
  return windows.map((value) => (typeof value === "number" && !Number.isNaN(value) ? formatNumber(value) : "-")).join(" / ");
}

/** Mirrors the backend lookup: vendor prefixes and "_-\" drift are tolerated. */
function normalizePriceKey(model: string): string {
  const trimmed = model.trim().toLowerCase();
  const tail = trimmed.includes("/") ? trimmed.slice(trimmed.lastIndexOf("/") + 1) : trimmed;
  return tail.replace(/_/g, "-");
}

function priceFor(prices: OpenCodeModelPrice[] | undefined, model: string): OpenCodeModelPrice | undefined {
  const key = normalizePriceKey(model);
  if (!key) return undefined;
  return (prices ?? []).find((price) => normalizePriceKey(price.id) === key);
}

const PRICE_ROWS_LIMIT = 40;

function formatWindow(window: { usage_percent: number; reset_in_sec: number } | undefined, tx: ReturnType<typeof useI18n>["tx"]): string {
  if (!window) return tx("ui.no_data");
  const resets = window.reset_in_sec > 0 ? `${Math.ceil(window.reset_in_sec / 60)} ${tx("ui.minutes_short")}` : "-";
  return `${window.usage_percent.toFixed(1)}% · ${resets}`;
}

/**
 * OpenCode workspace: the single place that manages OpenCode Go workspaces and
 * Zen credentials, including the model catalog, a real model test, and the action
 * that makes the models routable through CPA.
 */
export function OpenCodeWorkspace({ refreshRevision, onAPIError, onNotice }: OpenCodeWorkspaceProps) {
  const { locale, tx, formatDateTime, formatNumber } = useI18n();
  const [goAccounts, setGoAccounts] = useState<OpenCodeAccountView[]>([]);
  const [zenAccounts, setZenAccounts] = useState<OpenCodeZenAccountView[]>([]);
  const [channels, setChannels] = useState<OpenCodeChannelView[]>([]);
  const [quota, setQuota] = useState<Record<string, OpenCodeQuotaResult>>({});
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [storageError, setStorageError] = useState("");
  const [importNotice, setImportNotice] = useState("");
  const [activeTab, setActiveTab] = useState<OpenCodeTab>("overview");
  const [adding, setAdding] = useState(false);
  const [newWorkspace, setNewWorkspace] = useState("");
  const [newCookie, setNewCookie] = useState("");
  const [newKey, setNewKey] = useState("");
  const [newZenName, setNewZenName] = useState("");
  const [newZenBase, setNewZenBase] = useState("");
  const [newZenKey, setNewZenKey] = useState("");
  const [keyDraft, setKeyDraft] = useState<Record<string, string>>({});
  const [target, setTarget] = useState<ModelTarget | null>(null);
  const [testModel, setTestModel] = useState("");
  const [testResult, setTestResult] = useState<OpenCodeModelTestResult | null>(null);
  const [pricing, setPricing] = useState<OpenCodePricingSnapshot>({});
  const [session, setSession] = useState<OpenCodeSessionSnapshot | null>(null);
  const [priceKind, setPriceKind] = useState<OpenCodeKind>("go");
  const [priceQuery, setPriceQuery] = useState("");
  const [modelControl, setModelControl] = useState<OpenCodeModelControlSnapshot | null>(null);
  const [storage, setStorage] = useState<OpenCodeStorageInfo | null>(null);
  const [selectedModels, setSelectedModels] = useState<string[]>([]);
  const [controlTestModel, setControlTestModel] = useState("");
  const [controlTestTarget, setControlTestTarget] = useState("");
  const [controlTestResult, setControlTestResult] = useState<OpenCodeModelTestResult | null>(null);
  const [controlTestError, setControlTestError] = useState("");
  // The row whose write is in flight, so the clicked button can show its own progress.
  const [pendingModel, setPendingModel] = useState("");
  // The account whose credential editor is open, so a workspace or cookie can be completed
  // in place instead of deleting and re-adding the account.
  const [editingAccount, setEditingAccount] = useState("");
  const [credentialDraft, setCredentialDraft] = useState({ workspace: "", cookie: "", key: "" });
  const request = useRef(0);
  const handleError = useCallback((caught: unknown) => {
    if (caught instanceof api.APIError && caught.status === 401) {
      onAPIError(caught);
      return;
    }
    setError(operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale));
  }, [locale, onAPIError, tx]);

  const refresh = useCallback(async (signal?: AbortSignal) => {
    const requestID = request.current + 1;
    request.current = requestID;
    setLoading(true);
    setError("");
    try {
      const [go, zen, quotaSnapshot, pricingSnapshot, sessionSnapshot, channelSnapshot, controlSnapshot, storageSnapshot] = await Promise.all([
        api.listOpenCodeAccounts(signal),
        api.listOpenCodeZenAccounts(signal),
        api.getOpenCodeQuota(signal),
        api.getOpenCodePricing(signal),
        api.getOpenCodeSession(signal),
        api.getOpenCodeChannels(signal),
        api.getOpenCodeModelControl(signal),
        api.getOpenCodeStorage(signal),
      ]);
      if (requestID !== request.current) return;
      setGoAccounts(go.accounts);
      setZenAccounts(zen.accounts);
      setChannels(channelSnapshot.channels ?? []);
      setQuota(quotaSnapshot.results ?? {});
      setPricing(pricingSnapshot.pricing ?? {});
      setSession(sessionSnapshot.session ?? null);
      setModelControl(controlSnapshot);
      setStorage(storageSnapshot.storage ?? null);
      setStorageError(go.storage_error || zen.storage_error || quotaSnapshot.storage_error || pricingSnapshot.pricing?.storage_error || controlSnapshot.storage_error || "");
    } catch (caught) {
      if (signal?.aborted || (caught instanceof DOMException && caught.name === "AbortError")) return;
      if (requestID === request.current) handleError(caught);
    } finally {
      if (requestID === request.current) setLoading(false);
    }
  }, [handleError]);

  useEffect(() => {
    const controller = new AbortController();
    void refresh(controller.signal);
    return () => {
      controller.abort();
      request.current += 1;
    };
  }, [refresh, refreshRevision]);

  // Selection is keyed by model id; ids that disappear from the snapshot are dropped.
  useEffect(() => {
    const available = new Set((modelControl?.models ?? []).map((row) => row.id));
    setSelectedModels((current) => current.filter((id) => available.has(id)));
  }, [modelControl]);

  const withBusy = async (key: string, action: () => Promise<void>) => {
    setBusy(key);
    setError("");
    try {
      await action();
    } catch (caught) {
      handleError(caught);
    } finally {
      setBusy("");
    }
  };

  const addGoAccount = () => void withBusy("add-go", async () => {
    if (!newWorkspace.trim() || !newCookie.trim()) return;
    await api.saveOpenCodeAccount(newWorkspace.trim(), newCookie.trim(), newKey.trim() || undefined);
    setNewWorkspace("");
    setNewCookie("");
    setNewKey("");
    setAdding(false);
    setGoAccounts((await api.listOpenCodeAccounts()).accounts);
    onNotice(tx("ui.opencode_account_saved"));
  });

  /**
   * Complete or correct a stored credential. The workspace id is the upstream identity and
   * the API key drives the catalog, tests and routing, so an account created before either
   * value was known must be repairable without deleting it and losing its CPA binding.
   */
  const saveCredentials = (accountID: string) => void withBusy(`cred-${accountID}`, async () => {
    const response = await api.updateOpenCodeAccountCredentials(accountID, {
      workspaceID: credentialDraft.workspace.trim(),
      authCookie: credentialDraft.cookie.trim(),
      apiKey: credentialDraft.key.trim(),
    });
    setGoAccounts((current) => current.map((account) => (account.id === accountID ? { ...account, ...response.account } : account)));
    setEditingAccount("");
    setCredentialDraft({ workspace: "", cookie: "", key: "" });
    onNotice(tx("ui.opencode_credentials_saved"));
  });

  const openCredentials = (accountID: string) => {
    setEditingAccount((current) => (current === accountID ? "" : accountID));
    setCredentialDraft({ workspace: "", cookie: "", key: "" });
  };

  const addZenAccount = () => void withBusy("add-zen", async () => {
    if (!newZenKey.trim()) return;
    await api.saveOpenCodeZenAccount({ name: newZenName.trim(), base_url: newZenBase.trim(), zen_api_key: newZenKey.trim() });
    setNewZenName("");
    setNewZenBase("");
    setNewZenKey("");
    setZenAccounts((await api.listOpenCodeZenAccounts()).accounts);
    onNotice(tx("ui.opencode_account_saved"));
  });

  const syncPrices = () => void withBusy("pricing", async () => {
    const response = await api.refreshOpenCodePricing();
    setPricing(response.pricing ?? {});
    onNotice(tx(response.changed ? "ui.opencode_pricing_changed" : "ui.opencode_pricing_unchanged"));
  });

  const refreshCatalog = (kind: OpenCodeKind, accountID: string) => void withBusy(`models-${accountID}`, async () => {
    const response = await api.refreshOpenCodeModels(kind, accountID);
    if (kind === "go") {
      setGoAccounts((current) => current.map((account) => (account.id === accountID ? { ...account, ...(response.account as OpenCodeAccountView) } : account)));
    } else {
      setZenAccounts((current) => current.map((account) => (account.id === accountID ? { ...account, ...(response.account as OpenCodeZenAccountView) } : account)));
    }
    const models = response.account.models ?? [];
    onNotice(tx("ui.opencode_models_loaded", { count: String(models.length) }));
  });

  const refreshQuota = (accountID: string) => void withBusy(`quota-${accountID}`, async () => {
    const response = await api.refreshOpenCodeAccountQuota(accountID);
    setQuota((current) => ({ ...current, [accountID]: response.result }));
  });

  const bind = (kind: OpenCodeKind, accountID: string) => void withBusy(`bind-${accountID}`, async () => {
    const response = await api.bindOpenCodeChannel(kind, accountID);
    const binding = response.binding;
    onNotice(`${tx(binding.created ? "ui.opencode_channel_created" : "ui.opencode_channel_updated", { url: binding.base_url })} · ${tx("ui.opencode_channel_models", { count: String(binding.models ?? 0) })}`);
  });

  const saveKey = (accountID: string) => void withBusy(`key-${accountID}`, async () => {
    await api.saveOpenCodeAccountKey(accountID, (keyDraft[accountID] ?? "").trim());
    setKeyDraft((current) => ({ ...current, [accountID]: "" }));
    setGoAccounts((await api.listOpenCodeAccounts()).accounts);
    onNotice(tx("ui.opencode_key_saved"));
  });

  const openModels = (kind: OpenCodeKind, accountID: string, label: string, models: string[]) => {
    setTarget({ kind, accountID, label, models });
    setTestModel(models[0] ?? "");
    setTestResult(null);
    setActiveTab("models");
  };

  /**
   * Import the credential of one AI-provider channel. The backend returns 409 when
   * a Go channel still needs the Workspace ID and auth Cookie the operator owns;
   * APIError only exposes the status and message, so 409 alone marks that case.
   */
  const importChannel = (baseURL: string) => {
    void (async () => {
      setBusy(`import-${baseURL}`);
      setError("");
      setImportNotice("");
      try {
        const response = await api.importOpenCodeChannel(baseURL);
        const kindLabel = response.import.kind === "go" ? tx("ui.opencode_channel_kind_go") : tx("ui.opencode_channel_kind_zen");
        onNotice(tx("ui.opencode_import_done", { kind: kindLabel }));
        const [go, zen, channelSnapshot] = await Promise.all([
          api.listOpenCodeAccounts(),
          api.listOpenCodeZenAccounts(),
          api.getOpenCodeChannels(),
        ]);
        setGoAccounts(go.accounts);
        setZenAccounts(zen.accounts);
        setChannels(channelSnapshot.channels ?? []);
      } catch (caught) {
        if (caught instanceof api.APIError && caught.status === 409) {
          setImportNotice(tx("ui.opencode_import_needs_workspace"));
          setActiveTab("go");
          return;
        }
        handleError(caught);
      } finally {
        setBusy("");
      }
    })();
  };

  const runModelTest = () => void withBusy("model-test", async () => {
    if (!target || !testModel.trim()) return;
    const response = await api.testOpenCodeModel(target.kind, target.accountID, testModel.trim());
    setTestResult(response.result);
  });

  const toggleModelSelection = (id: string) => setSelectedModels((current) => (
    current.includes(id) ? current.filter((item) => item !== id) : [...current, id]
  ));

  /**
   * Apply the global disabled set and confirm it. The request itself can wait on the host,
   * so the clicked row shows its own progress and the result is announced: with no feedback
   * at all, a slow write is indistinguishable from a dead button.
   */
  const applyControlDisabled = (next: string[], changed: number, pending: string) => void withBusy("model-control", async () => {
    setPendingModel(pending);
    try {
      const before = modelControl?.disabled.length ?? 0;
      const snapshot = await api.saveOpenCodeModelControl(next);
      setModelControl(snapshot);
      setSelectedModels([]);
      const after = snapshot.disabled?.length ?? 0;
      onNotice(tx(after >= before ? "ui.models_updated_disabled_notice" : "ui.models_updated_enabled_notice", { count: String(changed) }));
    } finally {
      setPendingModel("");
    }
  });

  /** Bulk disable joins the current list; bulk enable removes the selection. */
  const applyControlSelection = (enable: boolean) => {
    const disabled = modelControl?.disabled ?? [];
    const selected = new Set(selectedModels);
    applyControlDisabled(
      enable ? disabled.filter((id) => !selected.has(id)) : Array.from(new Set([...disabled, ...selectedModels])),
      selectedModels.length,
      "",
    );
  };

  /**
   * Accounts that reference the model, matched by normalised id. When no cached catalog lists
   * the model, every credential of the family is offered instead of leaving the dialog empty:
   * a probe is exactly how an operator finds out whether a credential can run the model.
   */
  const controlTestCandidates = (model: string): OpenCodeTestCandidate[] => {
    const key = normalizePriceKey(model);
    const matching: OpenCodeTestCandidate[] = [];
    const every: OpenCodeTestCandidate[] = [];
    for (const account of goAccounts) {
      const candidate = { kind: "go" as OpenCodeKind, accountID: account.id, label: account.workspace_id || account.id };
      every.push(candidate);
      if ((account.models ?? []).some((entry) => normalizePriceKey(entry) === key)) matching.push(candidate);
    }
    for (const account of zenAccounts) {
      const candidate = { kind: "zen" as OpenCodeKind, accountID: account.id, label: account.name || account.id };
      every.push(candidate);
      if ((account.models ?? []).some((entry) => normalizePriceKey(entry) === key)) matching.push(candidate);
    }
    return matching.length > 0 ? matching : every;
  };

  const runControlTest = (model: string, candidate: OpenCodeTestCandidate) => void (async () => {
    setBusy("model-control-test");
    setControlTestError("");
    setControlTestResult(null);
    try {
      const response = await api.testOpenCodeModel(candidate.kind, candidate.accountID, model);
      setControlTestResult(response.result);
    } catch (caught) {
      if (caught instanceof api.APIError && caught.status === 401) {
        onAPIError(caught);
        return;
      }
      setControlTestError(operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale));
    } finally {
      setBusy("");
    }
  })();

  const openControlTest = (model: string) => {
    setControlTestModel(model);
    setControlTestResult(null);
    setControlTestError("");
    const candidates = controlTestCandidates(model);
    const first = candidates[0];
    setControlTestTarget(first ? `${first.kind}:${first.accountID}` : "");
    if (candidates.length === 1) runControlTest(model, first);
  };

  const closeControlTest = () => {
    setControlTestModel("");
    setControlTestTarget("");
    setControlTestResult(null);
    setControlTestError("");
  };

  const controlRows = modelControl?.models ?? [];
  const controlDisabled = modelControl?.disabled ?? [];
  const visibleControlIds = controlRows.map((row) => row.id);
  const allVisibleSelected = visibleControlIds.length > 0 && visibleControlIds.every((id) => selectedModels.includes(id));
  const toggleVisibleSelection = () => setSelectedModels((current) => (
    allVisibleSelected ? current.filter((id) => !visibleControlIds.includes(id)) : Array.from(new Set([...current, ...visibleControlIds]))
  ));
  const controlTestCandidatesFor = controlTestModel ? controlTestCandidates(controlTestModel) : [];
  // True when the dialog had to fall back to every credential, so the operator is told why.
  const controlTestIsFallback = controlTestModel !== "" && controlTestCandidatesFor.length > 0
    && !controlTestCandidatesFor.some((candidate) => (candidate.kind === "go" ? goAccounts : zenAccounts)
      .some((account) => account.id === candidate.accountID && (account.models ?? []).some((entry) => normalizePriceKey(entry) === normalizePriceKey(controlTestModel))));

  const catalogPrices = (priceKind === "go" ? pricing.go : pricing.zen) ?? [];
  const filteredPrices = catalogPrices.filter((price) => {
    const query = priceQuery.trim().toLowerCase();
    if (!query) return true;
    return price.id.toLowerCase().includes(query) || (price.name ?? "").toLowerCase().includes(query);
  });
  const visiblePrices = filteredPrices.slice(0, PRICE_ROWS_LIMIT);
  const selectedPrice = target ? priceFor(target.kind === "go" ? pricing.go : pricing.zen, testModel) : undefined;
  const billingModes = pricing.billing ?? [];
  const goBilling = billingModes.find((mode) => mode.kind === "go");
  // Documented Go split: the 5-hour window is 20% and the week is 50% of the
  // monthly allowance. The API carries the fractions; the constants are the fallback.
  const goFiveHourFraction = goBilling?.five_hour_fraction ?? 0.2;
  const goWeeklyFraction = goBilling?.weekly_fraction ?? 0.5;
  const modelCount = [...goAccounts, ...zenAccounts].reduce((total, account) => total + (account.models?.length ?? 0), 0);
  const tabs: Array<{ id: OpenCodeTab; label: string }> = [
    { id: "overview", label: tx("ui.opencode_tab_overview") },
    { id: "go", label: tx("ui.opencode_tab_go") },
    { id: "zen", label: tx("ui.opencode_tab_zen") },
    { id: "channels", label: tx("ui.opencode_tab_channels") },
    { id: "models", label: tx("ui.opencode_tab_models") },
  ];
  const tabLabel = (id: OpenCodeTab) => tabs.find((tab) => tab.id === id)?.label ?? "";

  const statusLabel = (status: OpenCodeModelTestResult["status"]): string => {
    switch (status) {
      case "available": return tx("ui.model_available");
      case "unavailable": return tx("ui.model_unavailable");
      case "unsupported": return tx("ui.testing_unsupported");
      default: return tx("ui.manual_confirmation_required");
    }
  };

  return (
    <section className="opencode-workspace" role="tabpanel" aria-label={tx("ui.opencode_menu")}>
      <header className="opencode-header">
        <div>
          <div className="eyebrow"><Link2 size={15} />{tx("ui.opencode")}</div>
          <h2>{tx("ui.opencode_title")}</h2>
          <p>{tx("ui.opencode_description")}</p>
        </div>
        <div className="opencode-header-actions">
          <a className="button button-quiet" href="https://opencode.ai/auth" target="_blank" rel="noopener noreferrer">
            <ExternalLink size={15} />{tx("ui.opencode_open_auth")}
          </a>
          <button className="button button-quiet" type="button" disabled={loading} onClick={() => void refresh()}>
            <RefreshCw className={loading ? "spin" : ""} size={15} />{tx("ui.refresh")}
          </button>
        </div>
      </header>

      {storageError ? <div className="notice-bar warning-notice" role="status"><AlertTriangle size={16} />{storageError}</div> : null}
      {error ? <div className="notice-bar" role="alert"><AlertTriangle size={16} />{error}</div> : null}
      {importNotice ? <div className="notice-bar warning-notice" role="status"><AlertTriangle size={16} />{importNotice}</div> : null}

      <div className="opencode-links">
        <a href="https://opencode.ai/workspace" target="_blank" rel="noopener noreferrer">OpenCode Go · {tx("ui.opencode_open_workspace")}</a>
        <a href="https://opencode.ai/zen" target="_blank" rel="noopener noreferrer">OpenCode Zen · {tx("ui.opencode_open_zen")}</a>
        <a href="/v0/resource/plugins/cpa-account-config-manager/opencode-status" target="_blank" rel="noopener noreferrer">{tx("ui.opencode_open_status_page")}</a>
      </div>

      <div className="opencode-tabs" role="tablist" aria-label={tx("ui.opencode_menu")}>
        {tabs.map((tab) => (
          <button
            key={tab.id}
            type="button"
            role="tab"
            className={activeTab === tab.id ? "active" : ""}
            aria-selected={activeTab === tab.id}
            onClick={() => setActiveTab(tab.id)}
          >
            {tab.label}
          </button>
        ))}
      </div>

      {activeTab === "overview" ? (
        <section className="opencode-tab-panel" role="tabpanel" aria-label={tabLabel("overview")}>
          {billingModes.length ? (
            <div className="opencode-billing" role="group" aria-label={tx("ui.opencode_billing")}>
              {billingModes.map((mode) => {
                const label = mode.kind === "go"
                  ? tx("ui.opencode_billing_go")
                  : mode.kind === "zen"
                    ? tx("ui.opencode_billing_zen")
                    : mode.kind;
                const split = !mode.metered && (typeof mode.five_hour_fraction === "number" || typeof mode.weekly_fraction === "number")
                  ? tx("ui.opencode_billing_split", {
                    five_hour: `${formatNumber((mode.five_hour_fraction ?? 0) * 100)}%`,
                    weekly: `${formatNumber((mode.weekly_fraction ?? 0) * 100)}%`,
                    monthly: "100%",
                  })
                  : "";
                return (
                  <div className="opencode-billing-row" key={mode.kind}>
                    <span className="opencode-billing-kind">{label}</span>
                    {mode.metered ? <span className="opencode-billing-badge">{tx("ui.opencode_billing_metered")}</span> : null}
                    {!mode.metered && typeof mode.subscription_usd_per_month === "number" ? (
                      <span className="opencode-billing-amount">
                        {tx("ui.opencode_billing_subscription", { amount: formatAllowanceUSD(mode.subscription_usd_per_month, formatNumber) })}
                      </span>
                    ) : null}
                    {split ? <span className="opencode-billing-split">{split}</span> : null}
                    {mode.summary ? <span className="opencode-billing-summary">{mode.summary}</span> : null}
                    {mode.docs_url ? (
                      <a className="opencode-billing-docs" href={mode.docs_url} target="_blank" rel="noopener noreferrer">
                        <ExternalLink size={13} />{tx("ui.opencode_billing_docs")}
                      </a>
                    ) : null}
                  </div>
                );
              })}
            </div>
          ) : null}

          <section className="opencode-section opencode-session" aria-label={tx("ui.opencode_session")}>
            <div className="opencode-section-heading">
              <div>
                <strong><Radio size={14} /> {tx("ui.opencode_session")}</strong>
                <span>{tx("ui.opencode_session_description")}</span>
              </div>
              <a className="button button-quiet" href="https://opencode.ai/docs/go/#where-can-i-use-it" target="_blank" rel="noopener noreferrer">
                <ExternalLink size={15} />{tx("ui.opencode_session_docs")}
              </a>
            </div>
            <dl className="opencode-test-result">
              <div><dt>{tx("ui.status")}</dt><dd>{session?.enabled && session?.salt_ready ? tx("ui.opencode_session_active") : tx("ui.opencode_session_inactive")}</dd></div>
              <div><dt>{tx("ui.opencode_session_targets")}</dt><dd>{session?.target_models?.length ?? 0} · {session?.target_auth_indexes ?? 0} {tx("ui.opencode_session_channels")}</dd></div>
              <div><dt>{tx("ui.opencode_session_injected")}</dt><dd>{session?.injected_requests ?? 0}</dd></div>
              <div><dt>{tx("ui.opencode_session_distinct")}</dt><dd>{session?.distinct_sessions ?? 0}</dd></div>
          <div>
            <dt>{tx("ui.opencode_session_attribution")}</dt>
            <dd>{tx("ui.opencode_session_attribution_value", {
              channel: String(session?.attributed_by_auth_index ?? 0),
              model: String(session?.attributed_by_model ?? 0),
            })}</dd>
          </div>
          <div>
            <dt>{tx("ui.opencode_session_skipped")}</dt>
            <dd>{tx("ui.opencode_session_skipped_value", {
              codex: String(session?.skipped_codex_requests ?? 0),
              other: String(session?.skipped_other_channel ?? 0),
              untargeted: String(session?.skipped_untargeted_model ?? 0),
            })}</dd>
          </div>
            </dl>
          </section>

          <dl className="opencode-counts">
            <div><dt>{tx("ui.opencode_counts_go")}</dt><dd>{goAccounts.length}</dd></div>
            <div><dt>{tx("ui.opencode_counts_zen")}</dt><dd>{zenAccounts.length}</dd></div>
            <div><dt>{tx("ui.opencode_counts_channels")}</dt><dd>{channels.length}</dd></div>
            <div><dt>{tx("ui.opencode_counts_models")}</dt><dd>{modelCount}</dd></div>
          </dl>
        </section>
      ) : null}

      {activeTab === "go" ? (
        <section className="opencode-tab-panel" role="tabpanel" aria-label={tabLabel("go")}>
          <section className="opencode-section" aria-label={tx("ui.opencode_go_accounts")}>
            <div className="opencode-section-heading">
              <div>
                <strong>{tx("ui.opencode_go_accounts")}</strong>
                <span>{tx("ui.opencode_go_accounts_description")}</span>
              </div>
              <button className="button button-quiet" type="button" onClick={() => setAdding((value) => !value)}>
                <Plus size={15} />{tx("ui.opencode_add_go")}
              </button>
            </div>
            {storage && (storage.hint === "missing" || storage.hint === "adopted") ? (
              <p className={storage.hint === "missing" ? "opencode-credential-warning" : "opencode-note"} role="status">
                <AlertTriangle size={14} />
                {storage.hint === "missing"
                  ? tx("ui.opencode_storage_missing", { path: storage.store_path || storage.data_dir })
                  : tx("ui.opencode_storage_adopted", { path: storage.adopted_from || "" })}
              </p>
            ) : null}
            {adding ? (
              <div className="opencode-form">
                <label className="field-block"><span>{tx("ui.opencode_workspace_id")}</span><input value={newWorkspace} placeholder={tx("ui.opencode_workspace_placeholder")} onChange={(event) => setNewWorkspace(event.target.value)} autoComplete="off" /></label>
                <label className="field-block"><span>{tx("ui.opencode_auth_cookie")}</span><input type="password" value={newCookie} placeholder={tx("ui.opencode_auth_cookie_placeholder")} onChange={(event) => setNewCookie(event.target.value)} autoComplete="off" /></label>
                <label className="field-block"><span>{tx("ui.opencode_api_key")}</span><input type="password" value={newKey} placeholder={tx("ui.opencode_key_placeholder")} onChange={(event) => setNewKey(event.target.value)} autoComplete="off" /></label>
                <div className="opencode-form-actions">
                  <button className="button button-primary" type="button" disabled={busy === "add-go" || !newWorkspace.trim() || !newCookie.trim()} onClick={addGoAccount}>
                    {busy === "add-go" ? <LoaderCircle className="spin" size={15} /> : <Plus size={15} />}{tx("ui.save")}
                  </button>
                </div>
                <p className="opencode-note">{tx("ui.opencode_credentials_note")}</p>
              </div>
            ) : null}
            <div className="opencode-table-wrap">
              <table className="account-table opencode-table">
                <thead>
                  <tr>
                    <th>{tx("ui.opencode_workspace_id")}</th>
                    <th>{tx("ui.opencode_api_key")}</th>
                    <th>{tx("ui.opencode_quota")}</th>
                    <th>{tx("ui.models")}</th>
                    <th className="actions-header">{tx("ui.actions")}</th>
                  </tr>
                </thead>
                <tbody>
                  {goAccounts.map((account) => {
                    const result = quota[account.id];
                    return (
                      <Fragment key={account.id}>
                        <tr>
                        <td><strong>{account.workspace_id}</strong></td>
                        <td>
                          <div className="opencode-key-cell">
                            <span>{account.key_set ? tx("ui.opencode_key_stored") : tx("ui.opencode_key_missing")}</span>
                            <input
                              type="password"
                              aria-label={tx("ui.opencode_api_key_for", { account: account.workspace_id })}
                              value={keyDraft[account.id] ?? ""}
                              placeholder={tx("ui.opencode_key_placeholder")}
                              autoComplete="off"
                              onChange={(event) => setKeyDraft((current) => ({ ...current, [account.id]: event.target.value }))}
                            />
                            <button className="button button-quiet button-small" type="button" disabled={busy === `key-${account.id}` || !(keyDraft[account.id] ?? "").trim()} onClick={() => saveKey(account.id)}>
                              {busy === `key-${account.id}` ? <LoaderCircle className="spin" size={14} /> : <KeyRound size={14} />}{tx("ui.save")}
                            </button>
                          </div>
                        </td>
                        <td>
                          <div className="opencode-quota-cell">
                            {result?.success ? (
                              <>
                                <small>{tx("ui.opencode_rolling")}: {formatWindow(result.rolling, tx)}</small>
                                <small>{tx("ui.opencode_weekly")}: {formatWindow(result.weekly, tx)}</small>
                                <small>{tx("ui.opencode_monthly")}: {formatWindow(result.monthly, tx)}</small>
                              </>
                            ) : (
                              <small>{result?.error ? operatorMessage(result.error, locale) : tx("ui.no_data")}</small>
                            )}
                          </div>
                        </td>
                        <td>
                          {account.models?.length ? (
                            <div className="opencode-models-cell">
                              <strong>{account.models.length}</strong>
                              <small>{account.models.slice(0, 3).join(", ")}{account.models.length > 3 ? " …" : ""}</small>
                              {account.models_error ? <small className="opencode-model-error">{account.models_error}</small> : null}
                            </div>
                          ) : (
                            <div className="opencode-models-cell">
                              <strong>-</strong>
                              <small>{account.models_error ? account.models_error : tx("ui.opencode_models_not_loaded")}</small>
                            </div>
                          )}
                        </td>
                        <td className="actions-cell">
                          <div className="row-actions">
                            <IconButton label={tx("ui.opencode_load_models_for", { account: account.workspace_id })} disabled={busy === `models-${account.id}`} onClick={() => refreshCatalog("go", account.id)}>
                              {busy === `models-${account.id}` ? <LoaderCircle className="spin" size={15} /> : <RefreshCw size={15} />}
                            </IconButton>
                            <IconButton label={tx("ui.opencode_refresh_action")} disabled={busy === `quota-${account.id}`} onClick={() => refreshQuota(account.id)}>
                              {busy === `quota-${account.id}` ? <LoaderCircle className="spin" size={15} /> : <RotateCcw size={15} />}
                            </IconButton>
                            <IconButton label={tx("ui.opencode_test_models_for", { account: account.workspace_id })} disabled={!account.models?.length} onClick={() => openModels("go", account.id, account.workspace_id, account.models ?? [])}>
                              <Activity size={15} />
                            </IconButton>
                            <IconButton label={tx("ui.opencode_bind_for", { account: account.workspace_id })} disabled={busy === `bind-${account.id}` || !account.key_set} onClick={() => bind("go", account.id)}>
                              {busy === `bind-${account.id}` ? <LoaderCircle className="spin" size={15} /> : <Link2 size={15} />}
                            </IconButton>
                            <IconButton label={tx("ui.opencode_edit_credentials_for", { account: account.workspace_id })} disabled={busy === `cred-${account.id}`} onClick={() => openCredentials(account.id)}>
                              <Wrench size={15} />
                            </IconButton>
                            <IconButton className="button-danger" label={tx("ui.opencode_remove_for", { account: account.workspace_id })} onClick={() => void withBusy(`remove-${account.id}`, async () => {
                              await api.removeOpenCodeAccount(account.id);
                              setGoAccounts((await api.listOpenCodeAccounts()).accounts);
                              onNotice(tx("ui.opencode_account_removed"));
                            })}><Trash2 size={15} /></IconButton>
                          </div>
                        </td>
                      </tr>
                      {editingAccount === account.id ? (
                        <tr className="opencode-credential-row">
                          <td colSpan={5}>
                            <div className="opencode-form">
                              <label className="field-block">
                                <span>{tx("ui.opencode_workspace_id")}</span>
                                <input value={credentialDraft.workspace} placeholder={account.workspace_id} autoComplete="off" onChange={(event) => setCredentialDraft((current) => ({ ...current, workspace: event.target.value }))} />
                              </label>
                              <label className="field-block">
                                <span>{tx("ui.opencode_auth_cookie")}</span>
                                <input type="password" value={credentialDraft.cookie} placeholder={account.cookie_set ? tx("ui.opencode_credentials_keep") : tx("ui.opencode_auth_cookie_placeholder")} autoComplete="off" onChange={(event) => setCredentialDraft((current) => ({ ...current, cookie: event.target.value }))} />
                              </label>
                              <label className="field-block">
                                <span>{tx("ui.opencode_api_key")}</span>
                                <input type="password" value={credentialDraft.key} placeholder={account.key_set ? tx("ui.opencode_credentials_keep") : tx("ui.opencode_key_placeholder")} autoComplete="off" onChange={(event) => setCredentialDraft((current) => ({ ...current, key: event.target.value }))} />
                              </label>
                              <div className="opencode-form-actions">
                                <button className="button button-primary" type="button" disabled={busy === `cred-${account.id}` || !credentialDraft.workspace.trim() && !credentialDraft.cookie.trim() && !credentialDraft.key.trim()} onClick={() => saveCredentials(account.id)}>
                                  {busy === `cred-${account.id}` ? <LoaderCircle className="spin" size={15} /> : <Save size={15} />}{tx("ui.save")}
                                </button>
                                <button className="button button-quiet" type="button" onClick={() => setEditingAccount("")}>{tx("ui.cancel")}</button>
                              </div>
                              <p className="opencode-note">{tx("ui.opencode_credentials_keep_hint")}</p>
                            </div>
                          </td>
                        </tr>
                      ) : null}
                      {!account.key_set || !account.cookie_set ? (
                        <tr className="opencode-credential-row">
                          <td colSpan={5}>
                            <p className="opencode-credential-warning" role="status">
                              <AlertTriangle size={14} />
                              {!account.cookie_set ? tx("ui.opencode_cookie_missing") : ""}
                              {!account.cookie_set && !account.key_set ? " · " : ""}
                              {!account.key_set ? tx("ui.opencode_incomplete_credentials") : ""}
                            </p>
                          </td>
                        </tr>
                      ) : null}
                      </Fragment>
                    );
                  })}
                  {!loading && goAccounts.length === 0 ? <tr><td colSpan={5}>{tx("ui.opencode_no_accounts")}</td></tr> : null}
                </tbody>
              </table>
            </div>
          </section>
        </section>
      ) : null}

      {activeTab === "zen" ? (
        <section className="opencode-tab-panel" role="tabpanel" aria-label={tabLabel("zen")}>
          <section className="opencode-section" aria-label={tx("ui.opencode_zen_accounts")}>
            <div className="opencode-section-heading">
              <div>
                <strong>{tx("ui.opencode_zen_accounts")}</strong>
                <span>{tx("ui.opencode_zen_accounts_description")}</span>
              </div>
            </div>
            <div className="opencode-form">
              <label className="field-block"><span>{tx("ui.name")}</span><input value={newZenName} onChange={(event) => setNewZenName(event.target.value)} autoComplete="off" /></label>
              <label className="field-block"><span>{tx("ui.ai_provider_base_url")}</span><input value={newZenBase} onChange={(event) => setNewZenBase(event.target.value)} placeholder="https://opencode.ai/zen" autoComplete="off" /></label>
              <label className="field-block"><span>{tx("ui.opencode_api_key")}</span><input type="password" value={newZenKey} onChange={(event) => setNewZenKey(event.target.value)} autoComplete="off" /></label>
              <div className="opencode-form-actions">
                <button className="button button-primary" type="button" disabled={busy === "add-zen" || !newZenKey.trim()} onClick={addZenAccount}>
                  {busy === "add-zen" ? <LoaderCircle className="spin" size={15} /> : <Plus size={15} />}{tx("ui.save")}
                </button>
              </div>
              <p className="opencode-note">{tx("ui.opencode_zen_base_hint")}</p>
            </div>
            <div className="opencode-table-wrap">
              <table className="account-table opencode-table">
                <thead>
                  <tr>
                    <th>{tx("ui.name")}</th>
                    <th>{tx("ui.ai_provider_base_url")}</th>
                    <th>{tx("ui.models")}</th>
                    <th className="actions-header">{tx("ui.actions")}</th>
                  </tr>
                </thead>
                <tbody>
                  {zenAccounts.map((account) => (
                    <tr key={account.id}>
                      <td><strong>{account.name || account.id}</strong></td>
                      <td className="opencode-table-url">{account.base_url}</td>
                      <td>
                        {account.models?.length ? (
                          <div className="opencode-models-cell">
                            <strong>{account.models.length}</strong>
                            <small>{account.models.slice(0, 3).join(", ")}{account.models.length > 3 ? " …" : ""}</small>
                            {account.models_error ? <small className="opencode-model-error">{account.models_error}</small> : null}
                          </div>
                        ) : (
                          <div className="opencode-models-cell">
                            <strong>-</strong>
                            <small>{account.models_error ? account.models_error : tx("ui.opencode_models_not_loaded")}</small>
                          </div>
                        )}
                      </td>
                      <td className="actions-cell">
                        <div className="row-actions">
                          <IconButton label={tx("ui.opencode_load_models_for", { account: account.name || account.id })} disabled={busy === `models-${account.id}`} onClick={() => refreshCatalog("zen", account.id)}>
                            {busy === `models-${account.id}` ? <LoaderCircle className="spin" size={15} /> : <RefreshCw size={15} />}
                          </IconButton>
                          <IconButton label={tx("ui.opencode_test_models_for", { account: account.name || account.id })} disabled={!account.models?.length} onClick={() => openModels("zen", account.id, account.name || account.id, account.models ?? [])}>
                            <Activity size={15} />
                          </IconButton>
                          <IconButton label={tx("ui.opencode_bind_for", { account: account.name || account.id })} disabled={busy === `bind-${account.id}` || !account.key_set} onClick={() => bind("zen", account.id)}>
                            {busy === `bind-${account.id}` ? <LoaderCircle className="spin" size={15} /> : <Link2 size={15} />}
                          </IconButton>
                          <IconButton className="button-danger" label={tx("ui.opencode_remove_for", { account: account.name || account.id })} onClick={() => void withBusy(`remove-${account.id}`, async () => {
                            await api.removeOpenCodeZenAccount(account.id);
                            setZenAccounts((await api.listOpenCodeZenAccounts()).accounts);
                            onNotice(tx("ui.opencode_account_removed"));
                          })}><Trash2 size={15} /></IconButton>
                        </div>
                      </td>
                    </tr>
                  ))}
                  {!loading && zenAccounts.length === 0 ? <tr><td colSpan={4}>{tx("ui.opencode_no_accounts")}</td></tr> : null}
                </tbody>
              </table>
            </div>
          </section>
        </section>
      ) : null}

      {activeTab === "channels" ? (
        <section className="opencode-tab-panel" role="tabpanel" aria-label={tabLabel("channels")}>
          <section className="opencode-section opencode-channels" aria-label={tx("ui.opencode_channels")}>
            <div className="opencode-section-heading">
              <div>
                <strong>{tx("ui.opencode_channels")}</strong>
                <span>{tx("ui.opencode_channels_description")}</span>
                <span>{tx("ui.opencode_channel_source")}</span>
              </div>
            </div>
            {channels.length === 0 ? (
              !loading ? <p className="opencode-note">{tx("ui.opencode_no_channels")}</p> : null
            ) : (
              <div className="opencode-table-wrap">
                <table className="account-table opencode-table">
                  <thead>
                    <tr>
                      <th>{tx("ui.opencode_channel_kind")}</th>
                      <th>{tx("ui.name")}</th>
                      <th>{tx("ui.ai_provider_base_url")}</th>
                      <th>{tx("ui.models")}</th>
                      <th>{tx("ui.opencode_channel_key")}</th>
                      <th>{tx("ui.opencode_channel_state")}</th>
                      <th className="actions-header">{tx("ui.actions")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {channels.map((channel) => (
                      <tr key={`${channel.kind}-${channel.base_url}`}>
                        <td>
                          <span className="opencode-channel-kind">
                            {channel.kind === "go" ? tx("ui.opencode_channel_kind_go") : tx("ui.opencode_channel_kind_zen")}
                          </span>
                        </td>
                        <td>
                          <div className="opencode-channel-name">
                            <strong>{channel.name || "-"}</strong>
                            {channel.workspace_id ? <small>{channel.workspace_id}</small> : null}
                          </div>
                        </td>
                        <td className="opencode-table-url">{channel.base_url}</td>
                        <td>{channel.models}</td>
                        <td>{channel.key_set ? tx("ui.opencode_key_stored") : tx("ui.opencode_key_missing")}</td>
                        <td>
                          <span className={channel.imported ? "opencode-channel-state imported" : "opencode-channel-state not-imported"}>
                            {channel.imported ? tx("ui.opencode_imported") : tx("ui.opencode_not_imported")}
                          </span>
                        </td>
                        <td className="actions-cell">
                          <button
                            className="button button-quiet button-small"
                            type="button"
                            disabled={busy === `import-${channel.base_url}` || !channel.key_set}
                            onClick={() => importChannel(channel.base_url)}
                          >
                            {busy === `import-${channel.base_url}` ? <LoaderCircle className="spin" size={14} /> : <Download size={14} />}{tx("ui.opencode_import")}
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>
        </section>
      ) : null}

      {activeTab === "models" ? (
        <section className="opencode-tab-panel" role="tabpanel" aria-label={tabLabel("models")}>
          <section className="opencode-section opencode-model-control" aria-label={tx("ui.opencode_model_control")}>
            <div className="opencode-section-heading">
              <div>
                <strong><Power size={14} /> {tx("ui.opencode_model_control")}</strong>
                <span>{tx("ui.opencode_model_control_description")}</span>
              </div>
              <div className="opencode-section-actions">
                <span className="opencode-model-count">{tx("ui.opencode_models_disabled_count")}: <strong>{controlDisabled.length}</strong></span>
                <button className="button button-quiet" type="button" disabled={busy === "model-control" || controlDisabled.length === 0} onClick={() => applyControlDisabled([], controlDisabled.length, "")}>
                  <RotateCcw size={15} />{tx("ui.opencode_models_enable_all")}
                </button>
              </div>
            </div>
            <div className="opencode-model-bulk" role="group" aria-label={tx("ui.opencode_model_control")}>
              <span className="opencode-model-bulk-count">{tx("ui.models_selected_count", { count: String(selectedModels.length) })}</span>
              <button className="button button-quiet" type="button" disabled={busy === "model-control" || selectedModels.length === 0} onClick={() => applyControlSelection(false)}>
                <Power size={15} />{tx("ui.models_disable_selected")}
              </button>
              <button className="button button-quiet" type="button" disabled={busy === "model-control" || selectedModels.length === 0} onClick={() => applyControlSelection(true)}>
                <RotateCcw size={15} />{tx("ui.models_enable_selected")}
              </button>
            </div>
            <div className="opencode-table-wrap">
              <table className="account-table opencode-table opencode-control-table">
                <thead>
                  <tr>
                    <th className="selection-header">
                      <input
                        type="checkbox"
                        aria-label={tx("ui.select_all")}
                        checked={allVisibleSelected}
                        disabled={controlRows.length === 0}
                        onChange={toggleVisibleSelection}
                      />
                    </th>
                    <th>{tx("ui.model")}</th>
                    <th>{tx("ui.opencode_models_input_price")}</th>
                    <th>{tx("ui.opencode_models_output_price")}</th>
                    <th>{tx("ui.opencode_models_accounts_count")}</th>
                    <th>{tx("ui.status")}</th>
                    <th className="actions-header">{tx("ui.actions")}</th>
                  </tr>
                </thead>
                <tbody>
                  {controlRows.map((row) => (
                    <tr key={row.id}>
                      <td className="selection-cell">
                        <input
                          type="checkbox"
                          aria-label={tx("ui.select_account", { account: row.id })}
                          checked={selectedModels.includes(row.id)}
                          onChange={() => toggleModelSelection(row.id)}
                        />
                      </td>
                      <td><strong>{row.id}</strong></td>
                      <td>{row.priced ? formatPriceUSD(row.input_usd_per_million) : tx("ui.opencode_models_unpriced")}</td>
                      <td>{formatPriceUSD(row.output_usd_per_million)}</td>
                      <td>{row.accounts}</td>
                      <td><span className={row.disabled ? "opencode-model-state disabled" : "opencode-model-state enabled"}>{tx(row.disabled ? "ui.disabled" : "ui.enabled")}</span></td>
                      <td className="actions-cell">
                        <div className="row-actions" role="group" aria-label={tx("ui.model_actions", { model: row.id })}>
                          <IconButton label={tx("ui.model_test_action", { model: row.id })} disabled={busy === "model-control-test"} onClick={() => openControlTest(row.id)}>
                            {busy === "model-control-test" && controlTestModel === row.id ? <LoaderCircle className="spin" size={15} /> : <Activity size={15} />}
                          </IconButton>
                          <IconButton
                            label={row.disabled ? tx("ui.model_enable_action", { model: row.id }) : tx("ui.model_disable_action", { model: row.id })}
                            disabled={busy === "model-control"}
                            onClick={() => applyControlDisabled(
                              row.disabled ? controlDisabled.filter((id) => id !== row.id) : [...controlDisabled, row.id],
                              1,
                              row.id,
                            )}
                          >
                            {busy === "model-control" && pendingModel === row.id ? <LoaderCircle className="spin" size={15} /> : <Power size={15} />}
                          </IconButton>
                        </div>
                      </td>
                    </tr>
                  ))}
                  {!loading && controlRows.length === 0 ? <tr><td colSpan={7}>{tx("ui.opencode_models_empty")}</td></tr> : null}
                </tbody>
              </table>
            </div>
            {controlTestModel ? (
              <ModelProbeDialog
                model={controlTestModel}
                targets={controlTestCandidatesFor.map((candidate) => ({ id: `${candidate.kind}:${candidate.accountID}`, label: candidate.label }))}
                targetID={controlTestTarget}
                onSelectTarget={setControlTestTarget}
                onRun={() => {
                  const candidate = controlTestCandidatesFor.find((entry) => `${entry.kind}:${entry.accountID}` === controlTestTarget) ?? controlTestCandidatesFor[0];
                  if (candidate) runControlTest(controlTestModel, candidate);
                }}
                onClose={closeControlTest}
                testing={busy === "model-control-test"}
                fallbackHint={controlTestIsFallback}
                error={controlTestError}
              >
                {controlTestResult ? (
                  <dl className="codex-model-test-result">
                    <div><dt>{tx("ui.model_test_result_status")}</dt><dd>{controlTestResult.status}</dd></div>
                    <div><dt>{tx("ui.model_test_result_reason")}</dt><dd>{controlTestResult.reason_code || "-"}</dd></div>
                    <div><dt>{tx("ui.model_test_result_http")}</dt><dd>{controlTestResult.status_code || "-"}</dd></div>
                    <div><dt>{tx("ui.model_test_result_latency")}</dt><dd>{typeof controlTestResult.latency_ms === "number" ? `${controlTestResult.latency_ms} ms` : "-"}</dd></div>
                  </dl>
                ) : null}
              </ModelProbeDialog>
            ) : null}
          </section>

          <section className="opencode-section opencode-pricing" aria-label={tx("ui.opencode_pricing")}>
            <div className="opencode-section-heading">
              <div>
                <strong><Coins size={14} /> {tx("ui.opencode_pricing")}</strong>
                <span>{tx("ui.opencode_pricing_description")}</span>
              </div>
              <button className="button button-quiet" type="button" disabled={busy === "pricing"} onClick={syncPrices}>
                {busy === "pricing" ? <LoaderCircle className="spin" size={15} /> : <RefreshCw size={15} />}{tx("ui.opencode_pricing_sync")}
              </button>
            </div>
            <div className="opencode-price-meta">
              <span>{tx("ui.opencode_pricing_source")}: {pricing.source || "-"}</span>
              <span>{tx("ui.opencode_pricing_updated")}: {pricing.updated_at ? formatDateTime(pricing.updated_at) : "-"}</span>
              <span>{tx("ui.opencode_docs_synced")}: {pricing.docs_updated_at ? formatDateTime(pricing.docs_updated_at) : "-"}</span>
              <span>{tx("ui.opencode_price_per_million")}</span>
            </div>
            <div className="opencode-price-controls">
              <div className="scope-segment" aria-label={tx("ui.opencode_pricing")}>
                <button type="button" className={priceKind === "go" ? "active" : ""} onClick={() => setPriceKind("go")}>{tx("ui.ai_provider_channel_opencode")}</button>
                <button type="button" className={priceKind === "zen" ? "active" : ""} onClick={() => setPriceKind("zen")}>{tx("ui.ai_provider_channel_opencode_zen")}</button>
              </div>
              <label className="opencode-price-search">
                <Search size={14} />
                <input value={priceQuery} onChange={(event) => setPriceQuery(event.target.value)} placeholder={tx("ui.opencode_prices_filter")} aria-label={tx("ui.opencode_prices_filter")} />
              </label>
              <span className="opencode-price-count">{tx("ui.opencode_prices_shown", { shown: String(visiblePrices.length), total: String(filteredPrices.length) })}</span>
            </div>
            <div className="opencode-table-wrap">
              <table className="account-table opencode-table opencode-price-table">
                <thead>
                  <tr>
                    <th>{tx("ui.model")}</th>
                    <th>{tx("ui.opencode_price_input")}</th>
                    <th>{tx("ui.opencode_price_output")}</th>
                    <th>{tx("ui.opencode_price_cache_read")}</th>
                    <th>{tx("ui.opencode_price_cache_write")}</th>
                    {priceKind === "go" ? <th>{tx("ui.opencode_monthly_allowance")}</th> : null}
                    {priceKind === "go" ? <th>{tx("ui.opencode_estimated_requests")}</th> : null}
                    <th>{tx("ui.opencode_context")}</th>
                  </tr>
                </thead>
                <tbody>
                  {visiblePrices.map((price) => {
                    const estimated = formatEstimatedRequests(price.estimated_requests, formatNumber);
                    const estimatedLabel = `${tx("ui.opencode_estimated_requests")}: ${tx("ui.opencode_rolling")} / ${tx("ui.opencode_weekly")} / ${tx("ui.opencode_monthly")}`;
                    const monthlyLimit = typeof price.monthly_limit_usd === "number" && !Number.isNaN(price.monthly_limit_usd) ? price.monthly_limit_usd : undefined;
                    return (
                      <tr key={price.id}>
                        <td>
                          <div className="opencode-models-cell">
                            <strong>{price.name || price.id}</strong>
                            <small>{price.id}</small>
                            {price.tiers?.length ? <small className="opencode-model-error">{tx("ui.opencode_price_tier_note", { tokens: formatNumber(price.tiers[0].min_context_tokens) })}</small> : null}
                            {price.deprecated_at ? <small className="opencode-model-error">{tx("ui.opencode_deprecated_at", { date: price.deprecated_at })}</small> : null}
                          </div>
                        </td>
                        <td>{formatPriceUSD(price.input_usd_per_million)}</td>
                        <td>{formatPriceUSD(price.output_usd_per_million)}</td>
                        <td>{formatPriceUSD(price.cache_read_usd_per_million)}</td>
                        <td>{formatPriceUSD(price.cache_write_usd_per_million)}</td>
                        {priceKind === "go" ? (
                          <td>
                            <div className="opencode-allowance-cell">
                              <strong>{formatAllowanceUSD(monthlyLimit, formatNumber)}</strong>
                              {monthlyLimit === undefined ? null : (
                                <small>
                                  {tx("ui.opencode_window_budget", {
                                    five_hour: formatAllowanceUSD(monthlyLimit * goFiveHourFraction, formatNumber),
                                    weekly: formatAllowanceUSD(monthlyLimit * goWeeklyFraction, formatNumber),
                                  })}
                                </small>
                              )}
                            </div>
                          </td>
                        ) : null}
                        {priceKind === "go" ? (
                          <td>
                            {estimated ? <span className="opencode-estimates" title={estimatedLabel} aria-label={estimatedLabel}>{estimated}</span> : "-"}
                          </td>
                        ) : null}
                        <td>{price.context_tokens ? formatNumber(price.context_tokens) : "-"}</td>
                      </tr>
                    );
                  })}
                  {!loading && visiblePrices.length === 0 ? <tr><td colSpan={priceKind === "go" ? 8 : 6}>{tx("ui.no_data")}</td></tr> : null}
                </tbody>
              </table>
            </div>
          </section>

          {target ? (
            <section className="opencode-section opencode-model-tester" aria-label={tx("ui.opencode_model_test")}>
              <div className="opencode-section-heading">
                <div>
                  <strong>{tx("ui.opencode_model_test")}</strong>
                  <span>{tx("ui.opencode_model_test_description", { account: target.label })}</span>
                </div>
                <button className="button button-quiet" type="button" onClick={() => { setTarget(null); setTestResult(null); }}>{tx("ui.close")}</button>
              </div>
              <div className="opencode-form">
                <label className="field-block">
                  <span>{tx("ui.model")}</span>
                  <select value={testModel} onChange={(event) => setTestModel(event.target.value)}>
                    {target.models.map((model) => <option key={model} value={model}>{model}</option>)}
                  </select>
                </label>
                <div className="opencode-form-actions">
                  <button className="button button-primary" type="button" disabled={busy === "model-test" || !testModel} onClick={runModelTest}>
                    {busy === "model-test" ? <LoaderCircle className="spin" size={15} /> : <Activity size={15} />}{tx("ui.test")}
                  </button>
                </div>
              </div>
              <p className="opencode-note">
                {tx("ui.opencode_model_price")}: {selectedPrice
                  ? `${formatPriceUSD(selectedPrice.input_usd_per_million)} / ${formatPriceUSD(selectedPrice.output_usd_per_million)} · ${tx("ui.opencode_price_per_million")}`
                  : tx("ui.opencode_no_price")}
              </p>
              {testResult ? (
                <dl className="opencode-test-result">
                  <div><dt>{tx("ui.status")}</dt><dd>{statusLabel(testResult.status)}</dd></div>
                  <div><dt>{tx("ui.reason")}</dt><dd>{testResult.reason_code || "-"}</dd></div>
                  <div><dt>{tx("ui.http_status")}</dt><dd>{testResult.status_code || "-"}</dd></div>
                  {typeof testResult.latency_ms === "number" ? <div><dt>{tx("ui.latency")}</dt><dd>{testResult.latency_ms} ms</dd></div> : null}
                  {testResult.tested_at ? <div><dt>{tx("ui.tested_at")}</dt><dd>{formatDateTime(testResult.tested_at)}</dd></div> : null}
                  {testResult.detail ? <div><dt>{tx("ui.detail")}</dt><dd>{operatorMessage(testResult.detail, locale)}</dd></div> : null}
                </dl>
              ) : null}
            </section>
          ) : null}
        </section>
      ) : null}
    </section>
  );
}

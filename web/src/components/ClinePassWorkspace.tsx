import { useCallback, useEffect, useRef, useState } from "react";
import { Activity, AlertTriangle, CircleDollarSign, ExternalLink, Gauge, KeyRound, Link2, LoaderCircle, RefreshCw, RotateCcw, Save, Trash2, Wallet } from "lucide-react";
import * as api from "../api/clinePass";
import type { ClinePassAccountView, ClinePassCatalogModel, ClinePassLoginView, ClinePassModelsResponse, ClinePassModelView } from "../api/clinePassTypes";
import { operatorMessage } from "../format/operatorMessage";
import { useI18n } from "../i18n";
import type { OpenCodeModelTestResult } from "../types";
import { IconButton } from "./IconButton";
import { ModelProbeDialog, ModelProbeOutcome } from "./ModelProbeDialog";
import { UsageMetricCards } from "./UsageMetricCards";

interface ClinePassWorkspaceProps {
  refreshRevision: number;
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
}

/** One Cline Pass model probe: the account, its label and the model being tested. */
interface ClinePassProbeTarget {
  accountID: string;
  label: string;
  model: string;
  models: string[];
}

/** The Cline Pass surface splits into the credential list and the published model mapping. */
type ClinePassTab = "overview" | "accounts" | "models";

/** One mapping row waiting in the shared model-test dialog, with its finished result. */
interface ClinePassRowProbe {
  /** The published client id of the clicked row, so its button knows it owns the open dialog. */
  clientID: string;
  /** The row as the table labels it: what the dialog header shows. */
  label: string;
  /** The id the Cline gateway accepts, which is what the probe sends. */
  model: string;
  result: OpenCodeModelTestResult | null;
  error: string;
}

/** Device-flow cadence: the gateway interval, clamped so a bad value cannot hammer it. */
const CLINE_PASS_POLL_DEFAULT_SECONDS = 5;
const CLINE_PASS_POLL_MIN_SECONDS = 2;
const CLINE_PASS_POLL_MAX_SECONDS = 30;

/** Milliseconds before the next device-flow poll: the gateway interval, clamped. */
function clinePassPollDelayMS(intervalSeconds?: number): number {
  const value = typeof intervalSeconds === "number" && Number.isFinite(intervalSeconds) && intervalSeconds > 0
    ? Math.round(intervalSeconds)
    : CLINE_PASS_POLL_DEFAULT_SECONDS;
  return Math.min(CLINE_PASS_POLL_MAX_SECONDS, Math.max(CLINE_PASS_POLL_MIN_SECONDS, value)) * 1000;
}

/**
 * A poll reply repeats the fields of its own session; anything it omits keeps the value
 * from the reply that started the sign-in, so the device code stays on screen while polling.
 */
function mergeClinePassLogin(current: ClinePassLoginView | null, next: ClinePassLoginView): ClinePassLoginView {
  const fallback = current ?? { status: next.status };
  const merged: ClinePassLoginView = { ...fallback, ...next };
  if (!merged.session_id) merged.session_id = current?.session_id;
  return merged;
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

/**
 * Cline Pass workspace: the credentials of the Cline Pass gateway and the model mapping the
 * plugin publishes to CPA routing. Cline Pass is its own product menu, so this component owns
 * its own state, requests and surface instead of living inside the OpenCode workspace.
 */
export function ClinePassWorkspace({ refreshRevision, onAPIError, onNotice }: ClinePassWorkspaceProps) {
  const { locale, tx, formatDateTime, formatNumber } = useI18n();
  const [clinePassAccounts, setClinePassAccounts] = useState<ClinePassAccountView[]>([]);
  const [clinePassCatalog, setClinePassCatalog] = useState<ClinePassCatalogModel[]>([]);
  const [clinePassDefaultBase, setClinePassDefaultBase] = useState("");
  const [clinePassLogin, setClinePassLogin] = useState<ClinePassLoginView | null>(null);
  const [clinePassError, setClinePassError] = useState("");
  const [newClinePassName, setNewClinePassName] = useState("");
  const [newClinePassBase, setNewClinePassBase] = useState("");
  const [newClinePassKey, setNewClinePassKey] = useState("");
  const [clinePassProbe, setClinePassProbe] = useState<ClinePassProbeTarget | null>(null);
  const [clinePassProbeResult, setClinePassProbeResult] = useState<OpenCodeModelTestResult | null>(null);
  const [clinePassProbeError, setClinePassProbeError] = useState("");
  // The published mapping is its own surface: it loads when its tab opens and never blocks
  // the accounts tab, so a broken models endpoint only shows its own error state.
  const [clinePassTab, setClinePassTab] = useState<ClinePassTab>("overview");
  const [clinePassModels, setClinePassModels] = useState<ClinePassModelsResponse | null>(null);
  const [clinePassModelsLoading, setClinePassModelsLoading] = useState(false);
  const [clinePassModelsError, setClinePassModelsError] = useState("");
  // A failed initial load is reported on the page, never hidden behind an empty account list.
  const [clinePassLoadError, setClinePassLoadError] = useState("");
  const [clinePassRowProbe, setClinePassRowProbe] = useState<ClinePassRowProbe | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const request = useRef(0);
  // The device-flow poll runs on timers, so it reads its state from refs: a re-render
  // must never restart, duplicate or leak the polling chain.
  const clinePassLoginRef = useRef<ClinePassLoginView | null>(null);
  const clinePassPollTimer = useRef(0);
  const clinePassPollInFlight = useRef(false);
  const clinePassPollMounted = useRef(true);
  const clinePassCatalogLoaded = useRef(false);
  // The models tab loads its payload the first time it is opened; refresh re-reads on demand.
  const clinePassModelsLoaded = useRef(false);
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
    setClinePassLoadError("");
    try {
      const [accountSnapshot, catalogSnapshot] = await Promise.all([
        // Cline Pass is its own service, but a failed read must not look like an empty account
        // list: the failure is reported on this page instead of degrading to "no accounts yet".
        api.listClinePassAccounts(signal),
        clinePassCatalogLoaded.current ? Promise.resolve(null) : api.getClinePassCatalog(signal),
      ]);
      if (requestID !== request.current) return;
      setClinePassAccounts(accountSnapshot.accounts);
      // The catalog is a static allow-list: it is read once and then reused by the form.
      if (catalogSnapshot) {
        clinePassCatalogLoaded.current = true;
        setClinePassCatalog(catalogSnapshot.models ?? []);
        setClinePassDefaultBase(catalogSnapshot.default_base_url || "");
      }
    } catch (caught) {
      if (signal?.aborted || (caught instanceof DOMException && caught.name === "AbortError")) return;
      if (requestID !== request.current) return;
      // A 401 belongs to the session and goes through the shared handler; every other failure
      // (an unreachable Cline Pass service included) stays visible on this page.
      if (caught instanceof api.APIError && caught.status === 401) {
        onAPIError(caught);
        return;
      }
      setClinePassLoadError(operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale));
    } finally {
      if (requestID === request.current) setLoading(false);
    }
  }, [locale, onAPIError, tx]);

  useEffect(() => {
    const controller = new AbortController();
    void refresh(controller.signal);
    return () => {
      controller.abort();
      request.current += 1;
    };
  }, [refresh, refreshRevision]);

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

  /**
   * Cline Pass sign-in. The device flow answers with a pending session that has to be
   * polled, while the CLI reuse finishes in one round trip, so both paths share the
   * handler and only the device flow arms the poll.
   */
  const applyClinePassLogin = useCallback((view: ClinePassLoginView | null) => {
    clinePassLoginRef.current = view;
    setClinePassLogin(view);
  }, []);

  const stopClinePassPoll = useCallback(() => {
    if (clinePassPollTimer.current) {
      window.clearTimeout(clinePassPollTimer.current);
      clinePassPollTimer.current = 0;
    }
  }, []);

  // The poll chain must die with the panel even when a sign-in is still pending.
  useEffect(() => {
    clinePassPollMounted.current = true;
    return () => {
      clinePassPollMounted.current = false;
      stopClinePassPoll();
    };
  }, [stopClinePassPoll]);

  const applyClinePassAccount = (account: ClinePassAccountView) => setClinePassAccounts((current) => (
    current.some((entry) => entry.id === account.id)
      ? current.map((entry) => (entry.id === account.id ? { ...entry, ...account } : entry))
      : [...current, account]
  ));

  /**
   * One non-blocking device-flow poll. It refuses to overlap a request still in flight,
   * ignores a reply for a session the operator already replaced, and only reschedules
   * itself while that session is still pending.
   */
  const runClinePassPoll = useCallback((sessionID: string) => {
    const active = clinePassLoginRef.current;
    if (clinePassPollInFlight.current || !clinePassPollMounted.current) return;
    if (!active || active.session_id !== sessionID || active.status !== "pending") return;
    clinePassPollInFlight.current = true;
    void (async () => {
      try {
        const view = await api.pollClinePassLogin(sessionID);
        const current = clinePassLoginRef.current;
        if (!clinePassPollMounted.current || !current || current.session_id !== sessionID) return;
        applyClinePassLogin(mergeClinePassLogin(current, view));
        if (view.status === "pending") {
          // Only a still-pending session reschedules itself: any other status stops the chain.
          const intervalSeconds = view.interval_seconds;
          clinePassPollTimer.current = window.setTimeout(() => runClinePassPoll(sessionID), clinePassPollDelayMS(intervalSeconds));
          return;
        }
        if (view.status === "completed") {
          // The completed sign-in binds the account to a CPA channel: report that channel
          // instead of a bare success, or warn about it without failing the sign-in.
          if (view.binding) {
            onNotice(`${tx("ui.cline_pass_bound")} · ${tx("ui.opencode_channel_models", { count: String(view.binding.models ?? 0) })}`);
          } else {
            onNotice(tx("ui.cline_pass_login_ok"));
            if (view.binding_error) setClinePassError(tx("ui.cline_pass_binding_error", { error: operatorMessage(view.binding_error, locale) }));
          }
          const listed = await api.listClinePassAccounts();
          if (clinePassPollMounted.current) setClinePassAccounts(listed.accounts);
        }
        return;
      } catch (caught) {
        const current = clinePassLoginRef.current;
        if (!clinePassPollMounted.current || !current || current.session_id !== sessionID) return;
        if (caught instanceof api.APIError && caught.status === 401) {
          onAPIError(caught);
          applyClinePassLogin(null);
          return;
        }
        // An unknown or already finished session cannot be polled again: report it once.
        applyClinePassLogin({
          ...current,
          status: "failed",
          error: operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale),
        });
      } finally {
        clinePassPollInFlight.current = false;
      }
    })();
  }, [applyClinePassLogin, locale, onAPIError, onNotice, tx]);

  const startClinePassSignIn = (method: "oauth" | "cli") => void withBusy(`cline-pass-login-${method}`, async () => {
    stopClinePassPoll();
    setClinePassError("");
    const view = await api.startClinePassLogin({ method });
    applyClinePassLogin(view);
    if (view.status === "pending" && view.session_id) {
      runClinePassPoll(view.session_id);
      return;
    }
    setClinePassAccounts((await api.listClinePassAccounts()).accounts);
    if (view.status === "completed") {
      // The CLI reuse finishes in one round trip and may already have bound the account.
      if (view.binding) {
        onNotice(`${tx("ui.cline_pass_bound")} · ${tx("ui.opencode_channel_models", { count: String(view.binding.models ?? 0) })}`);
      } else {
        onNotice(tx("ui.cline_pass_login_ok"));
        if (view.binding_error) setClinePassError(tx("ui.cline_pass_binding_error", { error: operatorMessage(view.binding_error, locale) }));
      }
    }
  });

  const cancelClinePassSignIn = () => void withBusy("cline-pass-cancel", async () => {
    const sessionID = clinePassLoginRef.current?.session_id ?? "";
    stopClinePassPoll();
    applyClinePassLogin(null);
    if (!sessionID) return;
    try {
      await api.cancelClinePassLogin(sessionID);
    } catch (caught) {
      // A session the backend already dropped is the state the cancel asked for.
      if (caught instanceof api.APIError && caught.status === 404) return;
      throw caught;
    }
  });

  const saveClinePassKey = () => void withBusy("cline-pass-api-key", async () => {
    if (!newClinePassKey.trim()) return;
    setClinePassError("");
    const response = await api.saveClinePassAccount({
      name: newClinePassName.trim() || undefined,
      base_url: newClinePassBase.trim() || undefined,
      api_key: newClinePassKey.trim(),
    });
    setNewClinePassKey("");
    applyClinePassAccount(response.account);
    onNotice(response.binding
      ? `${tx("ui.cline_pass_bound")} · ${tx("ui.opencode_channel_models", { count: String(response.binding.models ?? 0) })}`
      : tx("ui.cline_pass_account_saved"));
    // A failed bind is a warning: the credential itself was saved and stays usable for a retry.
    if (response.binding_error) setClinePassError(tx("ui.cline_pass_binding_error", { error: operatorMessage(response.binding_error, locale) }));
    if (!response.binding_error && !response.result.reachable && response.result.detail) setClinePassError(operatorMessage(response.result.detail, locale));
  });

  const refreshClinePassCatalogFor = (accountID: string) => void withBusy(`cline-pass-models-${accountID}`, async () => {
    const response = await api.refreshClinePassModels(accountID);
    applyClinePassAccount(response.account);
    onNotice(tx("ui.opencode_models_loaded", { count: String(response.account.models?.length ?? 0) }));
  });

  /** Rotating the token also republishes the CPA channel key, so routed traffic keeps working. */
  const refreshClinePassSignIn = (accountID: string) => void withBusy(`cline-pass-refresh-${accountID}`, async () => {
    const response = await api.refreshClinePassAccount(accountID, true);
    applyClinePassAccount(response.account);
    const binding = response.binding;
    onNotice(binding
      ? `${tx("ui.cline_pass_refreshed")} · ${tx(binding.created ? "ui.opencode_channel_created" : "ui.opencode_channel_updated", { url: binding.base_url })}`
      : tx("ui.cline_pass_refreshed"));
  });

  const removeClinePass = (accountID: string) => void withBusy(`cline-pass-remove-${accountID}`, async () => {
    await api.removeClinePassAccount(accountID);
    setClinePassAccounts((current) => current.filter((account) => account.id !== accountID));
    if (clinePassLoginRef.current?.account?.id === accountID) applyClinePassLogin(null);
    onNotice(tx("ui.cline_pass_account_removed"));
  });

  /**
   * The Cline Pass probe uses the shared dialog, but the model has to be chosen here: the
   * models tab resolves its own targets from the Go and Zen families only.
   */
  const openClinePassProbe = (account: ClinePassAccountView) => {
    const models = account.models ?? [];
    setClinePassProbe({ accountID: account.id, label: account.name || account.id, models, model: models[0] ?? "" });
    setClinePassProbeResult(null);
    setClinePassProbeError("");
  };

  const closeClinePassProbe = () => {
    setClinePassProbe(null);
    setClinePassProbeResult(null);
    setClinePassProbeError("");
  };

  const runClinePassProbe = () => void (async () => {
    const probe = clinePassProbe;
    if (!probe || !probe.model.trim()) return;
    setBusy("cline-pass-model-test");
    setClinePassProbeError("");
    setClinePassProbeResult(null);
    try {
      const response = await api.testClinePassModel(probe.accountID, probe.model.trim());
      setClinePassProbeResult(response.result);
    } catch (caught) {
      if (caught instanceof api.APIError && caught.status === 401) {
        onAPIError(caught);
        return;
      }
      setClinePassProbeError(operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale));
    } finally {
      setBusy("");
    }
  })();

  /** The mapping test uses one credential, preferring an account whose channel is already bound. */
  const clinePassProbeAccount = clinePassAccounts.find((account) => account.channel_bound) ?? clinePassAccounts[0];

  /**
   * The published mapping is read from its own endpoint. A failure only sets this panel's
   * error state: the accounts tab and the rest of the surface keep working.
   */
  const loadClinePassModels = useCallback(async () => {
    setClinePassModelsLoading(true);
    setClinePassModelsError("");
    try {
      setClinePassModels(await api.listClinePassModels());
    } catch (caught) {
      if (caught instanceof api.APIError && caught.status === 401) {
        onAPIError(caught);
        return;
      }
      setClinePassModelsError(tx("ui.cline_pass_models_error", {
        error: operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale),
      }));
    } finally {
      setClinePassModelsLoading(false);
    }
  }, [locale, onAPIError, tx]);

  // Opening the models tab is what reads the mapping; the ref keeps a rerender from re-reading.
  useEffect(() => {
    if (clinePassTab !== "models" || clinePassModelsLoaded.current) return;
    clinePassModelsLoaded.current = true;
    void loadClinePassModels();
  }, [clinePassTab, loadClinePassModels]);

  /**
   * Saving the prefix rule re-binds the channel, so the reply reports the rebound accounts and
   * a failed re-bind is only a warning: the setting is stored and the mapping still reloads.
   */
  const saveClinePassPrefixSetting = (stripModelPrefix: boolean) => void (async () => {
    setBusy("cline-pass-settings");
    setClinePassModelsError("");
    try {
      const response = await api.saveClinePassSettings(stripModelPrefix);
      onNotice(tx("ui.cline_pass_prefix_saved", { count: String(response.rebound) }));
      await loadClinePassModels();
      if (response.rebind_errors > 0) {
        setClinePassModelsError(tx("ui.cline_pass_prefix_rebind_errors", { count: String(response.rebind_errors) }));
      }
    } catch (caught) {
      if (caught instanceof api.APIError && caught.status === 401) {
        onAPIError(caught);
        return;
      }
      setClinePassModelsError(tx("ui.cline_pass_settings_error", {
        error: operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale),
      }));
    } finally {
      setBusy("");
    }
  })();

  /**
   * One row of the mapping is tested through the same dialog every other model page uses, so the
   * outcome never needs a row of its own in the table. The probe sends the row's upstream id: the
   * published client id is only what CPA exposes, while the gateway knows the upstream one.
   */
  const openClinePassRowProbe = (row: ClinePassModelView) => {
    setClinePassRowProbe({ clientID: row.client_id, label: row.name || row.client_id, model: row.upstream_id, result: null, error: "" });
  };

  const closeClinePassRowProbe = () => setClinePassRowProbe(null);

  const runClinePassRowProbe = () => void (async () => {
    const probe = clinePassRowProbe;
    const account = clinePassProbeAccount;
    if (!probe || !account) return;
    setBusy("cline-pass-model-row-test");
    setClinePassRowProbe((current) => (current && current.clientID === probe.clientID ? { ...current, result: null, error: "" } : current));
    try {
      const response = await api.testClinePassModel(account.id, probe.model);
      setClinePassRowProbe((current) => (current && current.clientID === probe.clientID ? { ...current, result: response.result } : current));
    } catch (caught) {
      if (caught instanceof api.APIError && caught.status === 401) {
        onAPIError(caught);
        return;
      }
      setClinePassRowProbe((current) => (current && current.clientID === probe.clientID
        ? { ...current, error: operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale) }
        : current));
    } finally {
      setBusy("");
    }
  })();

  /** The auth method of an account is reported as a label, never as a raw enum. */
  const clinePassAuthLabel = (method: ClinePassAccountView["auth_method"]): string => {
    switch (method) {
      case "oauth": return tx("ui.cline_pass_auth_oauth");
      case "cli": return tx("ui.cline_pass_auth_cli");
      default: return tx("ui.cline_pass_auth_api_key");
    }
  };

  /** The device flow completes the sign-in in the browser, so the complete URI is preferred. */
  const clinePassVerificationURI = clinePassLogin?.verification_uri_complete || clinePassLogin?.verification_uri || "";
  /** The mapping summary counts the rows the channel actually publishes. */
  const clinePassPublishedModels = (clinePassModels?.models ?? []).filter((model) => model.published).length;

  /**
   * One Cline Pass usage window summed over the accounts that reported it. The USD figure is
   * Cline's documented reference price, so it is labelled as one everywhere it is shown.
   */
  const clinePassWindowTotals = (key: "five_hour" | "weekly" | "monthly") => clinePassAccounts.reduce(
    (total, account) => {
      const window = account.quota_usage?.[key];
      if (!window) return total;
      total.reported += 1;
      total.usd += window.usd ?? 0;
      total.requests += window.requests ?? 0;
      total.inputTokens += window.input_tokens ?? 0;
      total.outputTokens += window.output_tokens ?? 0;
      total.cachedTokens += (window.cache_read_tokens ?? 0) + (window.cache_write_tokens ?? 0);
      total.unpricedRequests += window.unpriced_requests ?? 0;
      return total;
    },
    { reported: 0, usd: 0, requests: 0, inputTokens: 0, outputTokens: 0, cachedTokens: 0, unpricedRequests: 0 },
  );
  const clinePassMonthlyUsage = clinePassWindowTotals("monthly");
  const clinePassWindowKeys = ([
    ["5h", "five_hour"],
    ["7d", "weekly"],
    ["30d", "monthly"],
  ] as const);
  /** Cline documents one flat monthly fee, so any account that reports it names the plan. */
  const clinePassSubscriptionUSD = clinePassAccounts.reduce<number | undefined>(
    (value, account) => value ?? account.quota_usage?.monthly_subscription_usd,
    undefined,
  );
  // The balance is the documented subscription minus the reference-priced month. It stays
  // undefined until Cline reports a month, because a missing window is not zero usage.
  const clinePassRemainingUSD = clinePassMonthlyUsage.reported > 0 && typeof clinePassSubscriptionUSD === "number"
    ? clinePassSubscriptionUSD - clinePassMonthlyUsage.usd
    : undefined;
  const clinePassBoundChannels = clinePassAccounts.filter((account) => account.channel_bound === true).length;
  const clinePassModelCount = clinePassAccounts.reduce((total, account) => total + (account.models?.length ?? 0), 0);

  return (
    <section className="opencode-workspace" role="tabpanel" aria-label={tx("ui.cline_pass_menu")}>
      <header className="opencode-header">
        <div>
          <div className="eyebrow"><Link2 size={15} />{tx("ui.cline_pass_menu")}</div>
          <h2>{tx("ui.cline_pass_accounts")}</h2>
          <p>{tx("ui.cline_pass_description")}</p>
        </div>
        <div className="opencode-header-actions">
          <button className="button button-quiet" type="button" disabled={loading} onClick={() => void refresh()}>
            <RefreshCw className={loading ? "spin" : ""} size={15} />{tx("ui.refresh")}
          </button>
        </div>
      </header>

      {error ? <div className="notice-bar" role="alert"><AlertTriangle size={16} />{error}</div> : null}
      {clinePassLoadError ? (
        <p className="opencode-credential-warning" role="alert"><AlertTriangle size={14} />{clinePassLoadError}</p>
      ) : null}

      {/* A layout wrapper only: the tab panels below carry the accessibility names, and a second
          tabpanel here would duplicate the workspace's own name. */}
      <section className="opencode-tab-panel">
        {/* Cline Pass owns its own two-tab surface: the credential list and the published mapping. */}
        <div className="codex-tabs cline-pass-tabs" role="tablist" aria-label={tx("ui.cline_pass_menu")}>
          <button
            type="button"
            role="tab"
            className={clinePassTab === "overview" ? "active" : ""}
            aria-selected={clinePassTab === "overview"}
            onClick={() => setClinePassTab("overview")}
          >
            {tx("ui.opencode_tab_overview")}
          </button>
          <button
            type="button"
            role="tab"
            className={clinePassTab === "accounts" ? "active" : ""}
            aria-selected={clinePassTab === "accounts"}
            onClick={() => setClinePassTab("accounts")}
          >
            {tx("ui.cline_pass_tab_accounts")}
          </button>
          <button
            type="button"
            role="tab"
            className={clinePassTab === "models" ? "active" : ""}
            aria-selected={clinePassTab === "models"}
            onClick={() => setClinePassTab("models")}
          >
            {tx("ui.cline_pass_tab_models")}
          </button>
        </div>
        {clinePassTab === "overview" ? (
        <section className="codex-tab-panel" role="tabpanel" aria-label={tx("ui.cline_pass_tab_overview")}>
          <dl className="opencode-counts">
            <div><dt>{tx("ui.cline_pass_accounts")}</dt><dd>{clinePassAccounts.length}</dd></div>
            <div><dt>{tx("ui.cline_pass_counts_bound")}</dt><dd>{clinePassBoundChannels}</dd></div>
            <div><dt>{tx("ui.cline_pass_counts_models")}</dt><dd>{clinePassModelCount}</dd></div>
          </dl>

          <section className="opencode-section" aria-label={tx("ui.cline_pass_usage")}>
            <div className="opencode-section-heading"><div><strong>{tx("ui.cline_pass_usage")}</strong><span>{tx("ui.cline_pass_usage_reference_note")}</span></div></div>
            <UsageMetricCards
              label={tx("ui.cline_pass_usage")}
              metrics={[
                {
                  key: "tokens",
                  icon: <Gauge size={18} />,
                  label: tx("ui.total_tokens"),
                  value: formatNumber(clinePassMonthlyUsage.inputTokens + clinePassMonthlyUsage.outputTokens + clinePassMonthlyUsage.cachedTokens),
                  note: tx("ui.overview_usage_tokens", {
                    input: formatNumber(clinePassMonthlyUsage.inputTokens),
                    output: formatNumber(clinePassMonthlyUsage.outputTokens),
                    cached: formatNumber(clinePassMonthlyUsage.cachedTokens),
                  }),
                },
                {
                  key: "requests",
                  icon: <Activity size={18} />,
                  label: tx("ui.overview_requests"),
                  value: formatNumber(clinePassMonthlyUsage.requests),
                  // Requests Cline could not price stay visible instead of looking free.
                  note: clinePassMonthlyUsage.unpricedRequests > 0
                    ? tx("ui.unrated_requests_count", { count: formatNumber(clinePassMonthlyUsage.unpricedRequests) })
                    : undefined,
                  title: clinePassMonthlyUsage.unpricedRequests > 0
                    ? tx("ui.some_requests_could_not_be_priced", { count: formatNumber(clinePassMonthlyUsage.unpricedRequests) })
                    : undefined,
                },
                {
                  key: "amount",
                  tone: "accent",
                  icon: <CircleDollarSign size={18} />,
                  label: tx("ui.overview_priced_amount"),
                  value: formatAllowanceUSD(clinePassMonthlyUsage.usd, formatNumber),
                  note: typeof clinePassSubscriptionUSD === "number"
                    ? tx("ui.opencode_billing_subscription", { amount: formatAllowanceUSD(clinePassSubscriptionUSD, formatNumber) })
                    : undefined,
                  title: tx("ui.cline_pass_usage_reference_note"),
                },
                {
                  key: "balance",
                  icon: <Wallet size={18} />,
                  label: tx("ui.overview_balance"),
                  value: clinePassRemainingUSD === undefined ? "-" : formatAllowanceUSD(clinePassRemainingUSD, formatNumber),
                  note: clinePassRemainingUSD === undefined
                    ? tx("ui.overview_balance_unavailable")
                    : tx("ui.cline_pass_balance", { subscription: formatAllowanceUSD(clinePassSubscriptionUSD, formatNumber) }),
                  title: clinePassRemainingUSD === undefined
                    ? tx("ui.overview_balance_unavailable")
                    : tx("ui.cline_pass_usage_reference_note"),
                },
              ]}
            />
            <div className="opencode-price-grid">
              {clinePassWindowKeys.map(([window, key]) => {
                const usage = clinePassWindowTotals(key);
                return (
                <div className="opencode-price-card" key={window}>
                  <strong>{window}</strong><span>{formatAllowanceUSD(usage.usd, formatNumber)}</span>
                  <small>
                    {tx("ui.overview_requests")}: {formatNumber(usage.requests)} · {tx("ui.overview_usage_tokens", {
                      input: formatNumber(usage.inputTokens),
                      output: formatNumber(usage.outputTokens),
                      cached: formatNumber(usage.cachedTokens),
                    })}
                  </small>
                </div>
                );
              })}
            </div>
            <p className="opencode-note">{tx("ui.cline_pass_usage_reference_note")}</p>
          </section>
        </section>
        ) : null}

        {clinePassTab === "accounts" ? (
        <section className="codex-tab-panel" role="tabpanel" aria-label={tx("ui.cline_pass_tab_accounts")}>
        <section className="opencode-section" aria-label={tx("ui.cline_pass_accounts")}>
          <div className="opencode-section-heading">
            <div>
              <strong>{tx("ui.cline_pass_accounts")}</strong>
              <span>{tx("ui.cline_pass_description")}</span>
            </div>
          </div>
          {clinePassError ? (
            <p className="opencode-credential-warning" role="alert"><AlertTriangle size={14} />{clinePassError}</p>
          ) : null}
          <div className="opencode-form-actions">
            <button className="button button-quiet" type="button" disabled={busy === "cline-pass-login-oauth"} onClick={() => startClinePassSignIn("oauth")}>
              {busy === "cline-pass-login-oauth" ? <LoaderCircle className="spin" size={15} /> : <ExternalLink size={15} />}{tx("ui.cline_pass_login_device")}
            </button>
            <button className="button button-quiet" type="button" disabled={busy === "cline-pass-login-cli"} onClick={() => startClinePassSignIn("cli")}>
              {busy === "cline-pass-login-cli" ? <LoaderCircle className="spin" size={15} /> : <KeyRound size={15} />}{tx("ui.cline_pass_login_cli")}
            </button>
          </div>
          {clinePassLogin ? (
            <div className="opencode-key-cell" role="status">
              {clinePassLogin.status === "pending" ? (
                <>
                  {clinePassLogin.user_code ? <strong>{clinePassLogin.user_code}</strong> : null}
                  <small>{tx("ui.cline_pass_waiting")}</small>
                  {clinePassVerificationURI ? (
                    <>
                      <small>{tx("ui.cline_pass_code", { url: clinePassVerificationURI })}</small>
                      <button
                        className="button button-quiet button-small"
                        type="button"
                        onClick={() => window.open(clinePassVerificationURI, "_blank", "noopener,noreferrer")}
                      >
                        <ExternalLink size={14} />{tx("ui.cline_pass_open_browser")}
                      </button>
                    </>
                  ) : null}
                  <button className="button button-quiet button-small" type="button" disabled={busy === "cline-pass-cancel"} onClick={cancelClinePassSignIn}>
                    {busy === "cline-pass-cancel" ? <LoaderCircle className="spin" size={14} /> : null}{tx("ui.cline_pass_cancel")}
                  </button>
                </>
              ) : null}
              {clinePassLogin.status === "completed" ? <small>{tx("ui.cline_pass_login_ok")}</small> : null}
              {clinePassLogin.status === "expired" ? <small className="opencode-model-error">{tx("ui.cline_pass_login_expired")}</small> : null}
              {clinePassLogin.status === "failed" ? (
                <small className="opencode-model-error">
                  {tx("ui.cline_pass_login_failed", { error: clinePassLogin.error || tx("ui.request_failed") })}
                </small>
              ) : null}
            </div>
          ) : null}
          <div className="opencode-form">
            <label className="field-block"><span>{tx("ui.cline_pass_name_label")}</span><input value={newClinePassName} onChange={(event) => setNewClinePassName(event.target.value)} autoComplete="off" /></label>
            <label className="field-block">
              <span>{tx("ui.ai_provider_base_url")}</span>
              <input value={newClinePassBase} placeholder={clinePassDefaultBase} onChange={(event) => setNewClinePassBase(event.target.value)} autoComplete="off" />
            </label>
            <label className="field-block">
              <span>{tx("ui.cline_pass_api_key_label")}</span>
              <input type="password" value={newClinePassKey} placeholder={tx("ui.opencode_key_placeholder")} onChange={(event) => setNewClinePassKey(event.target.value)} autoComplete="off" />
            </label>
            <div className="opencode-form-actions">
              <button className="button button-primary" type="button" disabled={busy === "cline-pass-api-key" || !newClinePassKey.trim()} onClick={saveClinePassKey}>
                {busy === "cline-pass-api-key" ? <LoaderCircle className="spin" size={15} /> : <Save size={15} />}{tx("ui.cline_pass_login_api_key")}
              </button>
            </div>
            <p className="opencode-note">{tx("ui.opencode_credentials_note")}</p>
          </div>
          <div className="opencode-table-wrap">
            <table className="account-table opencode-table">
              <thead>
                <tr>
                  <th>{tx("ui.name")}</th>
                  <th>{tx("ui.opencode_channel_kind")}</th>
                  <th>{tx("ui.cline_pass_token_state")}</th>
                  <th>{tx("ui.models")}</th>
                  <th>{tx("ui.cline_pass_routing")}</th>
                  <th className="actions-header">{tx("ui.actions")}</th>
                </tr>
              </thead>
              <tbody>
                {clinePassAccounts.map((account) => {
                  const label = account.name || account.id;
                  const credentialed = account.access_token_set || account.refresh_token_set;
                  // CPA routing: the account is reachable only once a channel carries its base
                  // URL, and a partially published channel leaves some models unroutable.
                  const bound = account.channel_bound === true;
                  const publishedModels = account.channel_models ?? 0;
                  const modelGaps = account.channel_model_gaps ?? 0;
                  // Cline documents exactly these three ClinePass windows; the USD figures the
                  // backend attributes to them are reference prices, never an amount owed.
                  const usage = account.quota_usage;
                  const usageMonthly = usage?.monthly;
                  const usageWindows = [
                    { key: "five_hour", label: tx("ui.opencode_rolling"), window: usage?.five_hour },
                    { key: "weekly", label: tx("ui.opencode_weekly"), window: usage?.weekly },
                    { key: "monthly", label: tx("ui.opencode_monthly"), window: usageMonthly },
                  ];
                  // The catalog turns the stored ids into the names the operator knows; the ids
                  // stay visible because they are what the gateway accepts.
                  const modelNames = (account.models ?? [])
                    .map((id) => clinePassCatalog.find((model) => model.id === id)?.name ?? id)
                    .join(", ");
                  return (
                    <tr key={account.id}>
                      <td data-label={tx("ui.name")}>
                        <div className="opencode-channel-name">
                          <strong>{label}</strong>
                          <small>{account.base_url}</small>
                        </div>
                      </td>
                      <td data-label={tx("ui.opencode_channel_kind")}><span className="opencode-channel-kind">{clinePassAuthLabel(account.auth_method)}</span></td>
                      <td data-label={tx("ui.cline_pass_token_state")}>
                        <div className="opencode-models-cell">
                          <span className={account.access_token_set && account.refresh_token_set ? "opencode-channel-state imported" : "opencode-channel-state not-imported"}>
                            {account.access_token_set && account.refresh_token_set ? tx("ui.cline_pass_token_ready") : tx("ui.cline_pass_token_missing")}
                          </span>
                          <small>{account.expires_at ? tx("ui.cline_pass_expires", { time: formatDateTime(account.expires_at) }) : "-"}</small>
                          {account.expired ? <small className="opencode-model-error">{tx("ui.cline_pass_expired")}</small> : null}
                        </div>
                      </td>
                      <td data-label={tx("ui.models")}>
                        {account.models?.length ? (
                          <div className="opencode-models-cell">
                            <strong>{account.models.length}</strong>
                            <small title={modelNames}>{account.models.slice(0, 3).join(", ")}{account.models.length > 3 ? " …" : ""}</small>
                            {account.models_error ? <small className="opencode-model-error">{account.models_error}</small> : null}
                          </div>
                        ) : (
                          <div className="opencode-models-cell">
                            <strong>-</strong>
                            <small>{account.models_error ? account.models_error : tx("ui.opencode_models_not_loaded")}</small>
                          </div>
                        )}
                      </td>
                      <td data-label={tx("ui.cline_pass_routing")}>
                        <div className="opencode-routing-cell">
                          {bound && modelGaps > 0 ? (
                            <span className="opencode-routing-badge is-warning">{tx("ui.cline_pass_routing_gaps", { count: String(modelGaps) })}</span>
                          ) : bound ? (
                            <span className="opencode-routing-badge is-bound">{tx("ui.cline_pass_routing_bound", { count: String(publishedModels) })}</span>
                          ) : (
                            <>
                              <span className="opencode-routing-badge is-unbound">{tx("ui.cline_pass_routing_unbound")}</span>
                              <small>{tx("ui.cline_pass_routing_hint")}</small>
                            </>
                          )}
                          {usage ? (
                            <div className="cline-pass-usage-cell" title={tx("ui.cline_pass_usage_reference_note")}>
                              <small className="cline-pass-usage-title">{tx("ui.cline_pass_usage")}</small>
                              <span className="cline-pass-usage-windows">
                                {usageWindows.map((entry) => (
                                  <span
                                    key={entry.key}
                                    className="cline-pass-usage-window"
                                    title={entry.window ? tx("ui.cline_pass_usage_window_title", {
                                      window: entry.label,
                                      usd: formatPriceUSD(entry.window.usd),
                                      input: formatNumber(entry.window.input_tokens),
                                      output: formatNumber(entry.window.output_tokens),
                                      requests: formatNumber(entry.window.requests),
                                    }) : undefined}
                                  >
                                    <small>{entry.label}</small>
                                    <b>{formatPriceUSD(entry.window?.usd)}</b>
                                  </span>
                                ))}
                              </span>
                              {usageMonthly ? (
                                <small className="cline-pass-usage-tokens">
                                  {tx("ui.cline_pass_usage_tokens", {
                                    input: formatNumber(usageMonthly.input_tokens),
                                    output: formatNumber(usageMonthly.output_tokens),
                                    requests: formatNumber(usageMonthly.requests),
                                  })}
                                </small>
                              ) : null}
                              <small className="cline-pass-usage-reference">
                                {tx("ui.cline_pass_usage_reference", {
                                  reference: formatPriceUSD(usageMonthly?.usd),
                                  subscription: formatAllowanceUSD(usage.monthly_subscription_usd, formatNumber),
                                })}
                              </small>
                            </div>
                          ) : null}
                        </div>
                      </td>
                      <td className="actions-cell">
                        <div className="row-actions">
                          <IconButton label={tx("ui.opencode_load_models_for", { account: label })} disabled={busy === `cline-pass-models-${account.id}`} onClick={() => refreshClinePassCatalogFor(account.id)}>
                            {busy === `cline-pass-models-${account.id}` ? <LoaderCircle className="spin" size={15} /> : <RefreshCw size={15} />}
                          </IconButton>
                          {credentialed ? (
                            <>
                              <IconButton label={tx("ui.opencode_test_models_for", { account: label })} disabled={!account.models?.length} onClick={() => openClinePassProbe(account)}>
                                <Activity size={15} />
                              </IconButton>
                              <IconButton label={tx("ui.cline_pass_refresh")} disabled={busy === `cline-pass-refresh-${account.id}`} onClick={() => refreshClinePassSignIn(account.id)}>
                                {busy === `cline-pass-refresh-${account.id}` ? <LoaderCircle className="spin" size={15} /> : <RotateCcw size={15} />}
                              </IconButton>
                            </>
                          ) : (
                            <small className="opencode-model-error">{tx("ui.opencode_models_not_loaded")}</small>
                          )}
                          <IconButton className="button-danger" label={tx("ui.opencode_remove_for", { account: label })} disabled={busy === `cline-pass-remove-${account.id}`} onClick={() => removeClinePass(account.id)}>
                            {busy === `cline-pass-remove-${account.id}` ? <LoaderCircle className="spin" size={15} /> : <Trash2 size={15} />}
                          </IconButton>
                        </div>
                      </td>
                    </tr>
                  );
                })}
                {/* A failed read is reported above; an empty-list row next to that error would
                    claim the account list is empty when it was never read. */}
                {!loading && !clinePassLoadError && clinePassAccounts.length === 0 ? <tr><td colSpan={6}>{tx("ui.cline_pass_no_accounts")}</td></tr> : null}
              </tbody>
            </table>
          </div>
          {clinePassProbe ? (
            <ModelProbeDialog
              model={clinePassProbe.model}
              targets={[{ id: clinePassProbe.accountID, label: clinePassProbe.label }]}
              targetID={clinePassProbe.accountID}
              onSelectTarget={() => undefined}
              onRun={runClinePassProbe}
              onClose={closeClinePassProbe}
              testing={busy === "cline-pass-model-test"}
              error={clinePassProbeError}
            >
              <label className="model-test-field">
                <span>{tx("ui.model")}</span>
                <select
                  aria-label={tx("ui.model")}
                  value={clinePassProbe.model}
                  onChange={(event) => setClinePassProbe((current) => (current ? { ...current, model: event.target.value } : current))}
                >
                  {clinePassProbe.models.map((model) => <option key={model} value={model}>{model}</option>)}
                </select>
              </label>
              {clinePassProbeResult ? (
                <ModelProbeOutcome
                  status={clinePassProbeResult.status}
                  model={clinePassProbeResult.model || clinePassProbe.model}
                  reasonCode={clinePassProbeResult.reason_code}
                  statusCode={clinePassProbeResult.status_code}
                  latencyMs={clinePassProbeResult.latency_ms}
                  testedAt={clinePassProbeResult.tested_at}
                  endpoint={clinePassProbeResult.endpoint}
                  triedEndpoints={clinePassProbeResult.tried_endpoints}
                  probeKind={clinePassProbeResult.probe_kind ?? "model"}
                  response={clinePassProbeResult.response}
                  detail={clinePassProbeResult.detail}
                />
              ) : null}
            </ModelProbeDialog>
          ) : null}
        </section>
        </section>
        ) : null}

        {clinePassTab === "models" ? (
        <section className="codex-tab-panel" role="tabpanel" aria-label={tx("ui.cline_pass_tab_models")}>
          <section className="opencode-section opencode-cline-pass-models" aria-label={tx("ui.cline_pass_models_title")}>
            <div className="opencode-section-heading">
              <div>
                <strong>{tx("ui.cline_pass_models_title")}</strong>
                <span>{tx("ui.cline_pass_models_description")}</span>
              </div>
              <button className="button button-quiet" type="button" disabled={clinePassModelsLoading} onClick={() => void loadClinePassModels()}>
                <RefreshCw className={clinePassModelsLoading ? "spin" : ""} size={15} />{tx("ui.refresh")}
              </button>
            </div>

            {clinePassModelsError ? (
              <p className="opencode-credential-warning" role="alert"><AlertTriangle size={14} />{clinePassModelsError}</p>
            ) : null}

            {clinePassModels ? (
              <>
                <div className="opencode-section-heading">
                  <div>
                    <strong>{tx("ui.cline_pass_strip_prefix")}</strong>
                    <span>{tx("ui.cline_pass_strip_prefix_hint")}</span>
                  </div>
                  <label className="switch-control">
                    <input
                      type="checkbox"
                      checked={clinePassModels.strip_model_prefix}
                      disabled={busy === "cline-pass-settings"}
                      aria-label={tx("ui.cline_pass_strip_prefix")}
                      onChange={(event) => saveClinePassPrefixSetting(event.target.checked)}
                    />
                    <span><b>{tx(clinePassModels.strip_model_prefix ? "ui.on_2" : "ui.off_2")}</b></span>
                  </label>
                </div>
                <p className="opencode-price-meta">
                  <span>{tx("ui.cline_pass_models_summary", { published: String(clinePassPublishedModels), total: String(clinePassModels.models.length) })}</span>
                  <span>{clinePassModels.channel_bound
                    ? tx("ui.cline_pass_routing_bound", { count: String(clinePassModels.channel_models) })
                    : tx("ui.cline_pass_routing_unbound")}</span>
                  <span>{tx("ui.opencode_price_per_million")}</span>
                </p>
                <p className="opencode-note">{tx("ui.cline_pass_models_prices_note")}</p>
                {clinePassModels.accounts === 0 ? (
                  <p className="opencode-note">{tx("ui.cline_pass_models_no_account")}</p>
                ) : clinePassModels.models.length === 0 ? (
                  <p className="opencode-note">{tx("ui.cline_pass_models_empty")}</p>
                ) : (
                  <div className="opencode-table-wrap">
                    <table className="account-table opencode-table">
                      <thead>
                        <tr>
                          <th>{tx("ui.model")}</th>
                          <th>{tx("ui.cline_pass_client_model_id")}</th>
                          <th>{tx("ui.status")}</th>
                          <th>{tx("ui.opencode_price_input")}</th>
                          <th>{tx("ui.opencode_price_output")}</th>
                          <th>{tx("ui.opencode_price_cache_read")}</th>
                          <th className="actions-header">{tx("ui.actions")}</th>
                        </tr>
                      </thead>
                      <tbody>
                        {clinePassModels.models.map((row) => (
                          <tr key={row.id}>
                            <td data-label={tx("ui.model")}><strong>{row.name || row.client_id}</strong></td>
                            <td data-label={tx("ui.cline_pass_client_model_id")}>
                              <div className="opencode-models-cell">
                                <code className="cline-pass-model-id">{row.client_id}</code>
                                {row.client_id !== row.upstream_id ? (
                                  <small>
                                    {tx("ui.cline_pass_upstream_id")}: <code className="cline-pass-model-id">{row.upstream_id}</code>
                                  </small>
                                ) : null}
                              </div>
                            </td>
                            <td data-label={tx("ui.status")}>
                              <span className={row.published ? "opencode-model-state enabled" : "opencode-model-state disabled"}>
                                {tx(row.published ? "ui.cline_pass_published" : "ui.cline_pass_unpublished")}
                              </span>
                            </td>
                            <td data-label={tx("ui.opencode_price_input")}>{row.priced ? formatPriceUSD(row.input_usd_per_million) : tx("ui.opencode_models_unpriced")}</td>
                            <td data-label={tx("ui.opencode_price_output")}>{formatPriceUSD(row.output_usd_per_million)}</td>
                            <td data-label={tx("ui.opencode_price_cache_read")}>{formatPriceUSD(row.cache_read_usd_per_million)}</td>
                            <td className="actions-cell">
                              <div className="row-actions">
                                <IconButton
                                  label={tx("ui.model_test_action", { model: row.name || row.client_id })}
                                  disabled={!clinePassProbeAccount || busy === "cline-pass-model-row-test"}
                                  onClick={() => openClinePassRowProbe(row)}
                                >
                                  {busy === "cline-pass-model-row-test" && clinePassRowProbe?.clientID === row.client_id
                                    ? <LoaderCircle className="spin" size={15} />
                                    : <Activity size={15} />}
                                </IconButton>
                              </div>
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
                {clinePassRowProbe ? (
                  <ModelProbeDialog
                    model={clinePassRowProbe.label}
                    targets={clinePassProbeAccount ? [{ id: clinePassProbeAccount.id, label: clinePassProbeAccount.name || clinePassProbeAccount.id }] : []}
                    targetID={clinePassProbeAccount?.id ?? ""}
                    onSelectTarget={() => undefined}
                    onRun={runClinePassRowProbe}
                    onClose={closeClinePassRowProbe}
                    testing={busy === "cline-pass-model-row-test"}
                    error={clinePassRowProbe.error}
                  >
                    {clinePassRowProbe.result ? (
                      <ModelProbeOutcome
                        status={clinePassRowProbe.result.status}
                        model={clinePassRowProbe.result.model || clinePassRowProbe.model}
                        reasonCode={clinePassRowProbe.result.reason_code}
                        statusCode={clinePassRowProbe.result.status_code}
                        latencyMs={clinePassRowProbe.result.latency_ms}
                        testedAt={clinePassRowProbe.result.tested_at}
                        endpoint={clinePassRowProbe.result.endpoint}
                        triedEndpoints={clinePassRowProbe.result.tried_endpoints}
                        probeKind={clinePassRowProbe.result.probe_kind ?? "model"}
                        response={clinePassRowProbe.result.response}
                        detail={clinePassRowProbe.result.detail}
                      />
                    ) : null}
                  </ModelProbeDialog>
                ) : null}
              </>
            ) : clinePassModelsLoading ? (
              <p className="opencode-note" role="status"><LoaderCircle className="spin" size={14} />{tx("ui.loading_models")}</p>
            ) : null}
          </section>
        </section>
        ) : null}
      </section>
    </section>
  );
}

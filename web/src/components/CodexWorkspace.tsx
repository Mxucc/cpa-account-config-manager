import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Activity, AlertTriangle, Fingerprint, LoaderCircle, Power, RefreshCw, RotateCcw, Save, Search } from "lucide-react";
import * as api from "../api/client";
import { operatorMessage } from "../format/operatorMessage";
import { useI18n } from "../i18n";
import type { UIMessageKey } from "../i18n/uiText";
import type { Account, AIProviderRuntimeSnapshot, CodexFingerprintField, CodexFingerprintMode, CodexModelProbeResult, CodexTestTargetOption, CodexFingerprintProfile, CodexModelControlSnapshot, CodexOverview, ExperimentalCodexIdentitySettings, ExperimentalSettings, ModelTestResult, ModelTestStatus } from "../types";
import { CodexIdentityPolicyEditor } from "./CodexIdentityPolicyEditor";
import { ModelProbeDialog, ModelProbeOutcome } from "./ModelProbeDialog";
import { IconButton } from "./IconButton";

import { ProductQuotaWindowRow } from "./ProductQuotaWindowRow";
import { UsageMetricCards } from "./UsageMetricCards";
import { formatReferenceUSD } from "../format/currency";
import { accountProductUsage } from "../format/productUsage";
interface CodexWorkspaceProps {
  refreshRevision: number;
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
}

/** The workspace is split into tabs so identity, model control and fingerprints stay reachable. */
type CodexTab = "overview" | "models" | "fingerprint";

/** Mirrors the backend price-table label so the source is shown before the first load. */
const creditPricingSourceFallback = "Sub2API / Wei-Shaw model-price-repo";

const EMPTY_CODEX_IDENTITY: ExperimentalCodexIdentitySettings = {
  outbound_convergence_enabled: false,
  ingress_gate_enabled: false,
  allow_app_server_clients: false,
};

/** Prices come from the plugin price table as USD per million tokens. */
function formatCodexPrice(value: number | undefined): string {
  if (typeof value !== "number" || Number.isNaN(value)) return "-";
  if (value === 0) return "$0";
  if (value < 0.01) return `$${value.toFixed(4)}`;
  return `$${value.toFixed(2)}`;
}

const GROUP_ORDER = ["client", "identity", "derivation", "body"];

const FINGERPRINT_GROUP_LABELS: Record<string, UIMessageKey> = {
  client: "ui.codex_fingerprint_group_client",
  identity: "ui.codex_fingerprint_group_identity",
  derivation: "ui.codex_fingerprint_group_derivation",
  body: "ui.codex_fingerprint_group_body",
};

const FINGERPRINT_FIELD_LABELS: Record<string, UIMessageKey> = {
  mode: "ui.codex_fingerprint_mode",
  user_agent: "ui.codex_fingerprint_user_agent",
  originator: "ui.codex_fingerprint_originator",
  version: "ui.codex_fingerprint_version",
  openai_beta: "ui.codex_fingerprint_openai_beta",
  turn_metadata_header: "ui.codex_fingerprint_turn_metadata_header",
  installation_id: "ui.codex_fingerprint_installation_id",
  session_id: "ui.codex_fingerprint_session_id",
  thread_id: "ui.codex_fingerprint_thread_id",
  window_suffix: "ui.codex_fingerprint_window_suffix",
  install_prefix: "ui.codex_fingerprint_install_prefix",
  session_prefix: "ui.codex_fingerprint_session_prefix",
  thread_prefix: "ui.codex_fingerprint_thread_prefix",
  seed_strategy: "ui.codex_fingerprint_seed_strategy",
  fixed_seed: "ui.codex_fingerprint_fixed_seed",
  include_turn_started_at: "ui.codex_fingerprint_include_turn_started_at",
  include_relationship_fields: "ui.codex_fingerprint_include_relationship_fields",
  rewrite_prompt_cache_key: "ui.codex_fingerprint_rewrite_prompt_cache_key",
};

/**
 * The four fixed convergence modes and the localized label each raw value shows. The API
 * stores and receives the raw value; only the displayed text is translated.
 */
const CONVERGENCE_MODE_LABELS: Record<CodexFingerprintMode, UIMessageKey> = {
  off: "ui.codex_convergence_off",
  device: "ui.codex_convergence_device",
  session: "ui.codex_convergence_session",
  full: "ui.codex_convergence_full",
};

/**
 * Returns the localized label for a raw convergence-mode value, or "" for an empty or
 * unrecognized value so each caller picks its own fallback: the fingerprint select keeps
 * the raw option text, the overview shows a dash.
 */
function convergenceModeLabel(value: string, tx: (key: UIMessageKey) => string): string {
  switch (value) {
    case "off":
    case "device":
    case "session":
    case "full":
      return tx(CONVERGENCE_MODE_LABELS[value]);
    default:
      return "";
  }
}

function draftFor(field: CodexFingerprintField, drafts: Record<string, string>): string {
  return drafts[field.key] ?? field.value;
}

/**
 * Codex workspace: the single place that owns the global Codex client identity,
 * the global model control, and the request fingerprint profile.
 */
export function CodexWorkspace({ refreshRevision, onAPIError, onNotice }: CodexWorkspaceProps) {
  const { locale, tx, formatDateTime, formatNumber } = useI18n();
  const [activeTab, setActiveTab] = useState<CodexTab>("overview");
  const [overview, setOverview] = useState<CodexOverview | null>(null);
  const [modelControl, setModelControl] = useState<CodexModelControlSnapshot | null>(null);
  const [profile, setProfile] = useState<CodexFingerprintProfile | null>(null);
  const [experiments, setExperiments] = useState<ExperimentalSettings | null>(null);
  const [codexIdentity, setCodexIdentity] = useState<ExperimentalCodexIdentitySettings>(EMPTY_CODEX_IDENTITY);
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const [selectedModels, setSelectedModels] = useState<string[]>([]);
  // The row whose write is in flight, so the clicked button can show its own progress.
  const [pendingModel, setPendingModel] = useState("");
  const [testModel, setTestModel] = useState("");
  const [testAccounts, setTestAccounts] = useState<CodexTestTargetOption[] | null>(null);
  const [testAccountID, setTestAccountID] = useState("");
  const [testResult, setTestResult] = useState<ModelTestResult | null>(null);
  const [channelTestResult, setChannelTestResult] = useState<CodexModelProbeResult | null>(null);
  const [testError, setTestError] = useState("");
  const [modelQuery, setModelQuery] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [storageError, setStorageError] = useState("");
  /** Provider runtime snapshots, used for the Codex usage totals on the overview. */
  const [providerRuntime, setProviderRuntime] = useState<AIProviderRuntimeSnapshot[]>([]);
  /** The Codex credentials themselves: their usage snapshot carries the upstream 5h/7d windows. */
  const [codexAccounts, setCodexAccounts] = useState<Account[]>([]);
  const [fingerprintSaved, setFingerprintSaved] = useState(false);
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
      const [overviewSnapshot, modelsSnapshot, fingerprintSnapshot, experimentsSnapshot, runtimeSnapshot, accountsSnapshot] = await Promise.all([
        api.getCodexOverview(signal),
        api.getCodexModels(signal),
        api.getCodexFingerprint(signal),
        api.getExperimentalSettings(signal),
        // The Codex usage totals live in the provider runtime: CPA reports provider traffic
        // there, keyed by the "codex" provider name. A failure must not blank the workspace,
        // so it degrades to "no totals" instead of failing the load.
        api.getAIProviderRuntime(signal).catch(() => ({ snapshots: [], updated_at: "" })),
        // The upstream 5h/7d allowance windows are per credential, so the overview needs the
        // account list; like the runtime totals it stays optional for the rest of the page.
        api.listAccounts(1, 100, {}, undefined, signal).catch(() => ({ accounts: [], total: 0, page: 1, page_size: 100, pages: 0 })),
      ]);
      if (requestID !== request.current) return;
      setOverview(overviewSnapshot.overview ?? null);
      setModelControl(modelsSnapshot);
      setProfile(fingerprintSnapshot.profile ?? null);
      setExperiments(experimentsSnapshot.settings ?? null);
      setStorageError(fingerprintSnapshot.profile?.storage_error || modelsSnapshot.storage_error || "");
      setProviderRuntime(runtimeSnapshot.snapshots ?? []);
      setCodexAccounts(accountsSnapshot.accounts ?? []);
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

  useEffect(() => {
    if (!experiments) return;
    setCodexIdentity(experiments.codex_identity ?? EMPTY_CODEX_IDENTITY);
  }, [experiments]);

  useEffect(() => {
    if (!profile) return;
    setDrafts(Object.fromEntries(profile.fields.map((field) => [field.key, field.value])));
  }, [profile]);

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

  const saveCodexSettings = () => void withBusy("codex-settings", async () => {
    const next = await api.saveExperimentalSettings({
      // The two Codex experiments are owned by the experimental settings panel.
      // Their current values are echoed back so saving here never clears them.
      weekly_overdraft_enabled: experiments?.weekly_overdraft_enabled ?? false,
      agent_identity_enabled: experiments?.agent_identity_enabled ?? false,
      auto_model_whitelist_enabled: experiments?.auto_model_whitelist_enabled ?? false,
      sub2api_credit_usage_enabled: experiments?.sub2api_credit_usage_enabled ?? true,
      codex_identity: codexIdentity,
    });
    setExperiments(next.settings);
    onNotice(tx("ui.experimental_settings_saved"));
  });

  /**
   * Apply the global disabled set and confirm it. The request can wait on the host, so the
   * clicked row shows its own progress and the result is announced: with no feedback at all,
   * a slow write is indistinguishable from a dead button.
   */
  const applyDisabled = (nextDisabled: string[], changed: number, pending: string) => void withBusy("models", async () => {
    setPendingModel(pending);
    try {
      const before = modelControl?.disabled.length ?? 0;
      const snapshot = await api.saveCodexModels(nextDisabled);
      setModelControl(snapshot);
      const after = snapshot.disabled?.length ?? 0;
      onNotice(tx(after >= before ? "ui.models_updated_disabled_notice" : "ui.models_updated_enabled_notice", { count: String(changed) }));
    } finally {
      setPendingModel("");
    }
  });

  /** Bulk disable/enable: disable joins the current list, enable removes the selection. */
  const applySelection = (enable: boolean) => {
    const disabled = modelControl?.disabled ?? [];
    const selected = new Set(selectedModels);
    applyDisabled(
      enable ? disabled.filter((id) => !selected.has(id)) : Array.from(new Set([...disabled, ...selectedModels])),
      selectedModels.length,
      "",
    );
    setSelectedModels([]);
  };

  const toggleModelSelection = (id: string) => setSelectedModels((current) => (
    current.includes(id) ? current.filter((item) => item !== id) : [...current, id]
  ));

  const closeModelTest = () => {
    setTestModel("");
    setTestAccounts(null);
    setTestAccountID("");
    setTestResult(null);
    setChannelTestResult(null);
    setTestError("");
  };

  const runModelTest = (model: string, target: string) => void (async () => {
    setBusy("model-test");
    setTestError("");
    setTestResult(null);
    try {
      // A channel target is probed through its own base URL and credential; an account target goes
      // through CPA's account probe, which is what the accounts page uses.
      if (target.startsWith("channel:")) {
        const index = Number.parseInt(target.slice("channel:".length), 10);
        const response = await api.testCodexChannelModel(index, model);
        setChannelTestResult(response.result);
        return;
      }
      setTestResult(await api.testAccountModel(target.replace(/^account:/, ""), model));
    } catch (caught) {
      if (caught instanceof api.APIError && caught.status === 401) {
        onAPIError(caught);
        return;
      }
      setTestError(operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale));
    } finally {
      setBusy("");
    }
  })();

  /** Candidate accounts are loaded lazily so opening the tab stays cheap. */
  /**
   * The Codex credential can live as a CPA account or as an AI-provider channel, and an
   * installation that only configured channels has no account at all, so both are offered.
   */
  const openModelTest = (model: string) => void (async () => {
    setTestModel(model);
    setTestAccounts(null);
    setTestAccountID("");
    setTestResult(null);
    setChannelTestResult(null);
    setTestError("");
    try {
      const [accounts, targets] = await Promise.all([
        api.listAccounts(1, 100, {}),
        api.getCodexTestTargets(),
      ]);
      const accountTargets = accounts.accounts
        .filter((account) => `${account.provider ?? ""} ${account.type ?? ""}`.toLowerCase().includes("codex"))
        .map((account) => ({ id: `account:${account.id}`, label: account.label || account.email || account.name || account.id }));
      const channelTargets = targets.targets
        .filter((target) => target.kind === "channel" && target.key_set)
        .map((target) => ({ id: target.id, label: `${target.label} · ${tx("ui.codex_test_channel_suffix")}` }));
      const candidates = [...accountTargets, ...channelTargets];
      setTestAccounts(candidates);
      // The target list is a picker, so it must start on a real credential: leaving the selection
      // empty showed the first option's label while the start button stayed disabled, and the
      // operator had to re-pick the very target the dialog already displayed. With a single
      // credential the probe also runs immediately, which is what the operator asked for.
      const [preferred] = candidates;
      if (preferred) {
        setTestAccountID(preferred.id);
        if (candidates.length === 1) runModelTest(model, preferred.id);
      }
    } catch (caught) {
      setTestAccounts([]);
      if (caught instanceof api.APIError && caught.status === 401) {
        onAPIError(caught);
        return;
      }
      setTestError(operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale));
    }
  })();

  const updateDraft = (field: CodexFingerprintField, value: string) => {
    setFingerprintSaved(false);
    setDrafts((current) => ({ ...current, [field.key]: value }));
  };

  const saveFingerprint = () => void withBusy("fingerprint", async () => {
    const fields = profile?.fields ?? [];
    const values: Record<string, string> = {};
    for (const field of fields) {
      const draft = draftFor(field, drafts);
      if (draft !== field.value) values[field.key] = draft;
    }
    const snapshot = await api.saveCodexFingerprint(values);
    setProfile(snapshot.profile ?? null);
    setFingerprintSaved(true);
    onNotice(tx("ui.codex_fingerprint_saved"));
  });

  const restoreFingerprintKeys = (keys: string[], busyKey: string) => void withBusy(busyKey, async () => {
    const snapshot = await api.resetCodexFingerprint(keys);
    setProfile(snapshot.profile ?? null);
    setFingerprintSaved(false);
  });

  const fields = profile?.fields ?? [];
  const modelRows = modelControl?.models ?? [];
  const disabledModels = modelControl?.disabled ?? [];
  const disabledCount = disabledModels.length;
  const filteredRows = modelRows.filter((row) => {
    const query = modelQuery.trim().toLowerCase();
    if (!query) return true;
    return row.id.toLowerCase().includes(query);
  });
  const visibleSelectedCount = filteredRows.filter((row) => selectedModels.includes(row.id)).length;
  const allVisibleSelected = filteredRows.length > 0 && visibleSelectedCount === filteredRows.length;
  const toggleVisibleSelection = () => setSelectedModels((current) => (
    allVisibleSelected
      ? current.filter((id) => !filteredRows.some((row) => row.id === id))
      : Array.from(new Set([...current, ...filteredRows.map((row) => row.id)]))
  ));
  const testStatusLabel = (status: ModelTestStatus): string => {
    switch (status) {
      case "available": return tx("ui.model_available");
      case "unavailable": return tx("ui.model_unavailable");
      case "unsupported": return tx("ui.testing_unsupported");
      default: return tx("ui.manual_confirmation_required");
    }
  };

  /**
   * The Codex usage totals shown on the overview. CPA records provider traffic under the
   * "codex" provider name (and it is the same name for every Codex channel row), so the totals
   * are the sum of those snapshots: tokens, the reference-priced amount and the request split.
   * A missing snapshot means "no Codex traffic recorded yet", which the panel states instead of
   * printing zeroes that would read as measured emptiness.
   */
  const codexUsage = useMemo(() => {
    const snapshots = providerRuntime.filter((snapshot) => (snapshot.provider ?? "").trim().toLowerCase() === "codex");
    return snapshots.reduce((totals, snapshot) => ({
      available: true,
      input: totals.input + (snapshot.input_tokens ?? 0),
      output: totals.output + (snapshot.output_tokens ?? 0),
      cached: totals.cached + (snapshot.cached_tokens ?? 0),
      total: totals.total + (snapshot.total_tokens ?? 0),
      amountUSD: totals.amountUSD + (snapshot.amount_usd ?? 0),
      requests: totals.requests + (snapshot.rated_requests ?? 0) + (snapshot.unrated_requests ?? 0),
      unpriced: totals.unpriced + (snapshot.unrated_requests ?? 0),
    }), { available: snapshots.length > 0, input: 0, output: 0, cached: 0, total: 0, amountUSD: 0, requests: 0, unpriced: 0 });
  }, [providerRuntime]);

  /**
   * The Codex allowance of the overview: the 5h/7d windows come from the credentials' own usage
   * snapshots (the runtime aggregates carry the plugin's budget policy instead), so the tightest
   * reporting credential is what limits the pool. An installation without a Codex credential
   * reports no window at all, which is why the section renders nothing rather than 0%.
   */
  const codexAccountUsage = useMemo(
    () => accountProductUsage(codexAccounts.filter((account) => `${account.provider ?? ""}`.trim().toLowerCase() === "codex" || `${account.type ?? ""}`.trim().toLowerCase() === "codex")),
    [codexAccounts],
  );
  const groups = useMemo(() => {
    const known = GROUP_ORDER.filter((group) => fields.some((field) => field.group === group));
    const rest = fields.map((field) => field.group).filter((group) => !known.includes(group));
    return [...known, ...rest.filter((group, index) => rest.indexOf(group) === index)];
  }, [fields]);
  const tabs: Array<{ id: CodexTab; label: string }> = [
    { id: "overview", label: tx("ui.codex_tab_overview") },
    { id: "models", label: tx("ui.codex_tab_models") },
    { id: "fingerprint", label: tx("ui.codex_tab_fingerprint") },
  ];
  const tabLabel = (id: CodexTab) => tabs.find((tab) => tab.id === id)?.label ?? "";
  const fieldLabel = (field: CodexFingerprintField) => {
    const key = FINGERPRINT_FIELD_LABELS[field.key];
    return key ? tx(key) : field.key;
  };
  const groupLabel = (group: string) => {
    const key = FINGERPRINT_GROUP_LABELS[group];
    return key ? tx(key) : group;
  };
  const settingsDisabled = loading || busy === "codex-settings" || !experiments;

  return (
    <section className="codex-workspace" role="tabpanel" aria-label={tx("ui.codex_menu")}>
      <header className="codex-header">
        <div>
          <div className="eyebrow"><Fingerprint size={15} />{tx("ui.codex_menu")}</div>
          <h2>{tx("ui.codex_title")}</h2>
          <p>{tx("ui.codex_description")}</p>
        </div>
        <div className="codex-header-actions">
          <button className="button button-quiet" type="button" disabled={loading} onClick={() => void refresh()}>
            <RefreshCw className={loading ? "spin" : ""} size={15} />{tx("ui.refresh")}
          </button>
        </div>
      </header>

      {storageError ? <div className="notice-bar warning-notice" role="status"><AlertTriangle size={16} />{storageError}</div> : null}
      {error ? <div className="notice-bar" role="alert"><AlertTriangle size={16} />{error}</div> : null}

      <div className="codex-tabs" role="tablist" aria-label={tx("ui.codex_menu")}>
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
        <section className="codex-tab-panel" role="tabpanel" aria-label={tabLabel("overview")}>
          {/* Codex usage totals: CPA reports this traffic as the "codex" provider, so the
              overview sums those runtime snapshots instead of leaving the page with counts
              only. When nothing has been recorded the panel says so rather than showing 0. */}
          <UsageMetricCards
            label={tx("ui.codex_usage_totals")}
            metrics={codexUsage.available ? [
              { key: "tokens", icon: <Activity size={18} />, label: tx("ui.total_tokens"), value: formatNumber(codexUsage.total), note: tx("ui.overview_usage_tokens", { input: formatNumber(codexUsage.input), output: formatNumber(codexUsage.output), cached: formatNumber(codexUsage.cached) }) },
              { key: "requests", icon: <Activity size={18} />, label: tx("ui.overview_requests"), value: formatNumber(codexUsage.requests), note: codexUsage.unpriced > 0 ? tx("ui.unrated_requests_count", { count: String(codexUsage.unpriced) }) : "" },
              { key: "amount", icon: <Activity size={18} />, label: tx("ui.codex_usage_reference_amount"), value: formatReferenceUSD(codexUsage.amountUSD, formatNumber), note: tx("ui.codex_usage_reference_note"), tone: "accent" as const },
            ] : [
              { key: "empty", icon: <Activity size={18} />, label: tx("ui.total_tokens"), value: "-", note: tx("ui.codex_usage_unavailable") },
            ]}
          />
          <dl className="codex-counts">
            <div><dt>{tx("ui.codex_counts_accounts")}</dt><dd>{overview?.accounts ?? "-"}</dd></div>
            <div><dt>{tx("ui.codex_counts_channels")}</dt><dd>{overview?.channels ?? "-"}</dd></div>
            <div><dt>{tx("ui.codex_counts_disabled_models")}</dt><dd>{overview?.disabled_models ?? "-"}</dd></div>
            <div><dt>{tx("ui.codex_counts_account_overrides")}</dt><dd>{overview?.account_overrides ?? "-"}</dd></div>
            <div><dt>{tx("ui.codex_counts_provider_overrides")}</dt><dd>{overview?.provider_overrides ?? "-"}</dd></div>
            <div><dt>{tx("ui.codex_counts_fingerprint_fields")}</dt><dd>{overview?.fingerprint_overridden_fields ?? "-"}</dd></div>
          </dl>
          {/* The Codex allowance the operator is actually burning: the upstream 5h/7d windows live
              in each credential's own usage snapshot, so this reports the tightest credential that
              has one, not a sum of percentages. */}
          {codexAccountUsage.fiveHour || codexAccountUsage.sevenDay ? (
            <section className="codex-section" aria-label={tx("ui.quota_window")}>
              <div className="codex-section-heading">
                <div>
                  <strong>{tx("ui.quota_window")}</strong>
                  <span>{tx("ui.codex_quota_window_description")}</span>
                </div>
              </div>
              <ProductQuotaWindowRow label={tx("ui.5_hour_usage")} window={codexAccountUsage.fiveHour} />
              <ProductQuotaWindowRow label={tx("ui.7_day_usage")} window={codexAccountUsage.sevenDay} />
            </section>
          ) : null}

          <section className="codex-section codex-runtime" aria-label={tx("ui.codex_model_control_active")}>
            <div className="codex-section-heading">
              <div>
                <strong>{tx("ui.codex_model_control_active")}</strong>
                <span>{tx("ui.codex_model_control_description")}</span>
              </div>
              <label className="switch-control codex-model-control-switch">
                <input type="checkbox" checked={overview?.model_control_active === true} readOnly disabled aria-label={tx("ui.codex_model_control_active")} />
                <span><b>{tx(overview?.model_control_active ? "ui.on_2" : "ui.off_2")}</b></span>
              </label>
            </div>
            <dl className="codex-test-result">
              <div><dt>{tx("ui.codex_convergence_mode_effective")}</dt><dd>{convergenceModeLabel(overview?.convergence_mode ?? "", tx) || "-"}</dd></div>
            </dl>
          </section>

          <section className="codex-section" aria-label={tx("ui.codex_identity_group")}>
            <div className="codex-section-heading">
              <div>
                <strong>{tx("ui.codex_identity_group")}</strong>
                <span>{tx("ui.codex_identity_group_help")}</span>
              </div>
              <button className="button button-primary" type="button" disabled={settingsDisabled} onClick={saveCodexSettings}>
                {busy === "codex-settings" ? <LoaderCircle className="spin" size={15} /> : <Save size={15} />}{tx("ui.save_settings")}
              </button>
            </div>
            <CodexIdentityPolicyEditor
              value={codexIdentity}
              disabled={settingsDisabled}
              onChange={(patch) => setCodexIdentity((current) => ({ ...current, ...patch }))}
            />
          </section>
        </section>
      ) : null}

      {activeTab === "models" ? (
        <section className="codex-tab-panel" role="tabpanel" aria-label={tabLabel("models")}>
          <div className="codex-section-heading">
            <div>
              <strong>{tx("ui.codex_tab_models")}</strong>
              <span className="codex-models-count">{tx("ui.codex_models_disabled_count")}: <strong>{disabledCount}</strong></span>
            </div>
            <button className="button button-quiet" type="button" disabled={busy === "models" || disabledCount === 0} onClick={() => applyDisabled([], disabledCount, "")}>
              <RotateCcw size={15} />{tx("ui.codex_models_enable_all")}
            </button>
          </div>
          <p className="codex-models-note" role="note"><AlertTriangle size={15} />{tx("ui.codex_model_control_description")}</p>
          <p className="codex-price-source">
            {tx("ui.codex_models_price_source")}: <strong>{modelControl?.pricing_source || creditPricingSourceFallback}</strong>
            {modelControl?.pricing_updated_at ? ` · ${tx("ui.codex_pricing_updated")}: ${formatDateTime(modelControl.pricing_updated_at)}` : ""}
            {" · "}{tx("ui.codex_models_price_unit")}
          </p>
          <label className="codex-model-search">
            <Search size={15} />
            <input value={modelQuery} onChange={(event) => setModelQuery(event.target.value)} placeholder={tx("ui.search")} aria-label={tx("ui.search")} />
          </label>
          <div className="codex-model-bulk" role="group" aria-label={tx("ui.codex_model_control_active")}>
            <span className="codex-model-bulk-count">{tx("ui.models_selected_count", { count: String(selectedModels.length) })}</span>
            <button className="button button-quiet" type="button" disabled={busy === "models" || selectedModels.length === 0} onClick={() => applySelection(false)}>
              <Power size={15} />{tx("ui.models_disable_selected")}
            </button>
            <button className="button button-quiet" type="button" disabled={busy === "models" || selectedModels.length === 0} onClick={() => applySelection(true)}>
              <RotateCcw size={15} />{tx("ui.models_enable_selected")}
            </button>
          </div>
          <div className="codex-table-wrap">
            <table className="account-table codex-table">
              <thead>
                <tr>
                  <th className="selection-header">
                    <input
                      type="checkbox"
                      aria-label={tx("ui.select_all")}
                      checked={allVisibleSelected}
                      disabled={filteredRows.length === 0}
                      onChange={toggleVisibleSelection}
                    />
                  </th>
                  <th>{tx("ui.model")}</th>
                  <th>{tx("ui.codex_models_input_price")}</th>
                  <th>{tx("ui.codex_models_output_price")}</th>
                  <th>{tx("ui.codex_models_cache_read_price")}</th>
                  <th>{tx("ui.codex_models_accounts_count")}</th>
                  <th>{tx("ui.codex_models_channels_count")}</th>
                  <th>{tx("ui.status")}</th>
                  <th className="actions-header">{tx("ui.actions")}</th>
                </tr>
              </thead>
              <tbody>
                {filteredRows.map((row) => (
                  <tr key={row.id}>
                    <td className="selection-cell">
                      <input
                        type="checkbox"
                        aria-label={tx("ui.select_account", { account: row.id })}
                        checked={selectedModels.includes(row.id)}
                        onChange={() => toggleModelSelection(row.id)}
                      />
                    </td>
                    <td data-label={tx("ui.model")}>
                      <div className="codex-model-cell">
                        <strong>{row.id}</strong>
                        {row.long_context_threshold_tokens && (row.long_context_input_multiplier ?? 0) > 1 ? (
                          <small className="codex-model-long-context">{tx("ui.codex_models_long_context_note", {
                            tokens: String(row.long_context_threshold_tokens),
                            multiplier: String(row.long_context_input_multiplier),
                          })}</small>
                        ) : null}
                      </div>
                    </td>
                    <td data-label={tx("ui.codex_models_input_price")}>{row.priced ? formatCodexPrice(row.input_usd_per_million) : tx("ui.codex_models_unpriced")}</td>
                    <td data-label={tx("ui.codex_models_output_price")}>{formatCodexPrice(row.output_usd_per_million)}</td>
                    <td data-label={tx("ui.codex_models_cache_read_price")}>{formatCodexPrice(row.cache_read_usd_per_million)}</td>
                    <td data-label={tx("ui.codex_models_accounts_count")}>{row.accounts}</td>
                    <td data-label={tx("ui.codex_models_channels_count")}>{row.channels}</td>
                    <td data-label={tx("ui.status")}><span className={row.disabled ? "codex-model-state disabled" : "codex-model-state enabled"}>{tx(row.disabled ? "ui.disabled" : "ui.enabled")}</span></td>
                    <td className="actions-cell">
                      <div className="row-actions" role="group" aria-label={tx("ui.model_actions", { model: row.id })}>
                        <IconButton label={tx("ui.model_test_action", { model: row.id })} disabled={busy === "model-test"} onClick={() => openModelTest(row.id)}>
                          {busy === "model-test" && testModel === row.id ? <LoaderCircle className="spin" size={15} /> : <Activity size={15} />}
                        </IconButton>
                        <IconButton
                          label={row.disabled ? tx("ui.model_enable_action", { model: row.id }) : tx("ui.model_disable_action", { model: row.id })}
                          disabled={busy === "models"}
                          onClick={() => applyDisabled(
                            row.disabled ? disabledModels.filter((id) => id !== row.id) : [...disabledModels, row.id],
                            1,
                            row.id,
                          )}
                        >
                          {busy === "models" && pendingModel === row.id ? <LoaderCircle className="spin" size={15} /> : <Power size={15} />}
                        </IconButton>
                      </div>
                    </td>
                  </tr>
                ))}
                {!loading && filteredRows.length === 0 ? <tr><td colSpan={8}>{tx("ui.codex_models_empty")}</td></tr> : null}
              </tbody>
            </table>
          </div>
          {testModel ? (
            <ModelProbeDialog
              model={testModel}
              targets={testAccounts ?? []}
              targetID={testAccountID}
              onSelectTarget={setTestAccountID}
              onRun={() => runModelTest(testModel, testAccountID)}
              onClose={closeModelTest}
              testing={busy === "model-test" || testAccounts === null}
              error={testError}
            >
              {testResult ? (
                <ModelProbeOutcome
                  status={testResult.status}
                  model={testResult.model}
                  reasonCode={testResult.reason_code}
                  statusCode={testResult.status_code}
                  latencyMs={testResult.latency_ms}
                  testedAt={testResult.tested_at}
                  detail={testResult.response?.body}
                />
              ) : null}
              {channelTestResult ? (
                <ModelProbeOutcome
                  status={channelTestResult.status}
                  model={channelTestResult.model || testModel}
                  reasonCode={channelTestResult.reason_code}
                  statusCode={channelTestResult.status_code}
                  latencyMs={channelTestResult.latency_ms}
                  testedAt={channelTestResult.tested_at}
                  endpoint={channelTestResult.endpoint}
                  probeKind={channelTestResult.probe_kind}
                  response={channelTestResult.response}
                  detail={channelTestResult.detail}
                />
              ) : null}
            </ModelProbeDialog>
          ) : null}
        </section>
      ) : null}

      {activeTab === "fingerprint" ? (
        <section className="codex-tab-panel" role="tabpanel" aria-label={tabLabel("fingerprint")}>
          <div className="codex-section-heading">
            <div>
              <strong>{tx("ui.codex_tab_fingerprint")}</strong>
              <span>{tx("ui.codex_fingerprint_description")}</span>
            </div>
            <div className="codex-section-actions">
              <button className="button button-quiet" type="button" disabled={!profile || busy === "fingerprint-restore-all"} onClick={() => restoreFingerprintKeys([], "fingerprint-restore-all")}>
                <RotateCcw size={15} />{tx("ui.codex_fingerprint_restore_all")}
              </button>
              <button className="button button-primary" type="button" disabled={!profile || busy === "fingerprint"} onClick={saveFingerprint}>
                {busy === "fingerprint" ? <LoaderCircle className="spin" size={15} /> : <Save size={15} />}{tx("ui.save")}
              </button>
            </div>
          </div>
          {fingerprintSaved ? <div className="notice-bar codex-saved-notice" role="status">{tx("ui.codex_fingerprint_saved")}</div> : null}

          {groups.map((group) => (
            <section className="codex-fingerprint-group" key={group} aria-label={groupLabel(group)}>
              <div className="codex-fingerprint-group-heading">
                <strong>{groupLabel(group)}</strong>
                <button
                  className="button button-quiet button-small"
                  type="button"
                  disabled={busy === `fingerprint-restore-${group}`}
                  onClick={() => restoreFingerprintKeys(fields.filter((field) => field.group === group).map((field) => field.key), `fingerprint-restore-${group}`)}
                >
                  <RotateCcw size={14} />{tx("ui.codex_fingerprint_restore_group")}
                </button>
              </div>
              {fields.filter((field) => field.group === group).map((field) => {
                const label = fieldLabel(field);
                const draft = draftFor(field, drafts);
                return (
                  <div className="codex-fingerprint-row" key={field.key}>
                    <div className="codex-fingerprint-field">
                      <span className="codex-fingerprint-label">
                        {label}
                        {field.overridden ? <em className="codex-fingerprint-badge">{tx("ui.codex_fingerprint_overridden")}</em> : null}
                      </span>
                      {field.kind === "select" ? (
                        <select aria-label={label} value={draft} disabled={busy.startsWith("fingerprint")} onChange={(event) => updateDraft(field, event.target.value)}>
                          {!(field.options ?? []).includes(draft) ? <option value={draft}>{draft || "-"}</option> : null}
                          {(field.options ?? []).map((option) => (
                            // Only the mode enum has localized option labels; every other select keeps its raw values.
                            <option key={option} value={option}>{field.key === "mode" ? convergenceModeLabel(option, tx) || option : option}</option>
                          ))}
                        </select>
                      ) : field.kind === "bool" ? (
                        <input
                          type="checkbox"
                          aria-label={label}
                          checked={draft === "true"}
                          disabled={busy.startsWith("fingerprint")}
                          onChange={(event) => updateDraft(field, event.target.checked ? "true" : "false")}
                        />
                      ) : (
                        <input
                          type={field.kind === "number" ? "number" : "text"}
                          aria-label={label}
                          value={draft}
                          disabled={busy.startsWith("fingerprint")}
                          onChange={(event) => updateDraft(field, event.target.value)}
                        />
                      )}
                    </div>
                    <span className="codex-fingerprint-default">
                      <em>{tx("ui.codex_fingerprint_default")}</em>
                      <code>{field.default === "" ? "-" : field.default}</code>
                    </span>
                    <button
                      className="button button-quiet button-small"
                      type="button"
                      disabled={busy.startsWith("fingerprint")}
                      onClick={() => restoreFingerprintKeys([field.key], `fingerprint-restore-${field.key}`)}
                    >
                      <RotateCcw size={14} />{tx("ui.codex_fingerprint_restore")}
                    </button>
                  </div>
                );
              })}
            </section>
          ))}
        </section>
      ) : null}
    </section>
  );
}

/** The channel probe reports its own status vocabulary; map it onto the account-test labels. */
function accountTestStatus(status: string): ModelTestStatus {
  switch (status) {
    case "available":
      return "available";
    case "unavailable":
      return "unavailable";
    case "unsupported":
      return "unsupported";
    default:
      return "review";
  }
}

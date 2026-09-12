import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { AlertTriangle, Fingerprint, LoaderCircle, RefreshCw, RotateCcw, Save, Search } from "lucide-react";
import * as api from "../api/client";
import { operatorMessage } from "../format/operatorMessage";
import { useI18n } from "../i18n";
import type { UIMessageKey } from "../i18n/uiText";
import type { CodexFingerprintField, CodexFingerprintProfile, CodexModelControlSnapshot, CodexOverview, ExperimentalCodexIdentitySettings, ExperimentalSettings } from "../types";
import { CodexIdentityPolicyEditor } from "./CodexIdentityPolicyEditor";

interface CodexWorkspaceProps {
  refreshRevision: number;
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
}

/** The workspace is split into tabs so identity, model control and fingerprints stay reachable. */
type CodexTab = "overview" | "models" | "fingerprint";

const EMPTY_CODEX_IDENTITY: ExperimentalCodexIdentitySettings = {
  outbound_convergence_enabled: false,
  ingress_gate_enabled: false,
  allow_app_server_clients: false,
};

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

function draftFor(field: CodexFingerprintField, drafts: Record<string, string>): string {
  return drafts[field.key] ?? field.value;
}

/**
 * Codex workspace: the single place that owns the global Codex client identity,
 * the global model control, and the request fingerprint profile.
 */
export function CodexWorkspace({ refreshRevision, onAPIError, onNotice }: CodexWorkspaceProps) {
  const { locale, tx } = useI18n();
  const [activeTab, setActiveTab] = useState<CodexTab>("overview");
  const [overview, setOverview] = useState<CodexOverview | null>(null);
  const [modelControl, setModelControl] = useState<CodexModelControlSnapshot | null>(null);
  const [profile, setProfile] = useState<CodexFingerprintProfile | null>(null);
  const [experiments, setExperiments] = useState<ExperimentalSettings | null>(null);
  const [codexIdentity, setCodexIdentity] = useState<ExperimentalCodexIdentitySettings>(EMPTY_CODEX_IDENTITY);
  const [weeklyOverdraftEnabled, setWeeklyOverdraftEnabled] = useState(false);
  const [agentIdentityEnabled, setAgentIdentityEnabled] = useState(false);
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const [modelQuery, setModelQuery] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [storageError, setStorageError] = useState("");
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
      const [overviewSnapshot, modelsSnapshot, fingerprintSnapshot, experimentsSnapshot] = await Promise.all([
        api.getCodexOverview(signal),
        api.getCodexModels(signal),
        api.getCodexFingerprint(signal),
        api.getExperimentalSettings(signal),
      ]);
      if (requestID !== request.current) return;
      setOverview(overviewSnapshot.overview ?? null);
      setModelControl(modelsSnapshot);
      setProfile(fingerprintSnapshot.profile ?? null);
      setExperiments(experimentsSnapshot.settings ?? null);
      setStorageError(fingerprintSnapshot.profile?.storage_error || modelsSnapshot.storage_error || "");
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

  useEffect(() => {
    if (!experiments) return;
    setWeeklyOverdraftEnabled(experiments.weekly_overdraft_enabled === true);
    setAgentIdentityEnabled(experiments.agent_identity_enabled === true);
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
      weekly_overdraft_enabled: weeklyOverdraftEnabled,
      agent_identity_enabled: agentIdentityEnabled,
      auto_model_whitelist_enabled: experiments?.auto_model_whitelist_enabled ?? true,
      sub2api_credit_usage_enabled: experiments?.sub2api_credit_usage_enabled ?? true,
      codex_identity: codexIdentity,
    });
    setExperiments(next.settings);
    onNotice(tx("ui.experimental_settings_saved"));
  });

  const applyDisabled = (nextDisabled: string[]) => void withBusy("models", async () => {
    const snapshot = await api.saveCodexModels(nextDisabled);
    setModelControl(snapshot);
  });

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
          <dl className="codex-counts">
            <div><dt>{tx("ui.codex_counts_accounts")}</dt><dd>{overview?.accounts ?? "-"}</dd></div>
            <div><dt>{tx("ui.codex_counts_channels")}</dt><dd>{overview?.channels ?? "-"}</dd></div>
            <div><dt>{tx("ui.codex_counts_disabled_models")}</dt><dd>{overview?.disabled_models ?? "-"}</dd></div>
            <div><dt>{tx("ui.codex_counts_account_overrides")}</dt><dd>{overview?.account_overrides ?? "-"}</dd></div>
            <div><dt>{tx("ui.codex_counts_provider_overrides")}</dt><dd>{overview?.provider_overrides ?? "-"}</dd></div>
            <div><dt>{tx("ui.codex_counts_fingerprint_fields")}</dt><dd>{overview?.fingerprint_overridden_fields ?? "-"}</dd></div>
          </dl>

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
              <div><dt>{tx("ui.codex_convergence_mode_effective")}</dt><dd>{overview?.convergence_mode || "-"}</dd></div>
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
            <div className="experimental-feature-block">
              <div className="experimental-feature-row">
                <div className="experimental-feature-copy">
                  <div>
                    <strong>{tx("ui.codex_weekly_quota_overdraft")}</strong>
                    <span>{tx("ui.codex_weekly_quota_overdraft_description")}</span>
                  </div>
                </div>
                <label className="switch-control experimental-feature-switch">
                  <input
                    type="checkbox"
                    checked={weeklyOverdraftEnabled}
                    disabled={settingsDisabled}
                    onChange={(event) => setWeeklyOverdraftEnabled(event.target.checked)}
                    aria-label={tx("ui.codex_weekly_quota_overdraft")}
                  />
                  <b>{tx(weeklyOverdraftEnabled ? "ui.on_2" : "ui.off_2")}</b>
                </label>
              </div>
            </div>
            <div className="experimental-feature-block">
              <div className="experimental-feature-row">
                <div className="experimental-feature-copy">
                  <div>
                    <strong>{tx("ui.codex_agent_identity")}</strong>
                    <span>{tx("ui.codex_agent_identity_description")}</span>
                  </div>
                </div>
                <label className="switch-control experimental-feature-switch">
                  <input
                    type="checkbox"
                    checked={agentIdentityEnabled}
                    disabled={settingsDisabled}
                    onChange={(event) => setAgentIdentityEnabled(event.target.checked)}
                    aria-label={tx("ui.codex_agent_identity")}
                  />
                  <b>{tx(agentIdentityEnabled ? "ui.on_2" : "ui.off_2")}</b>
                </label>
              </div>
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
            <button className="button button-quiet" type="button" disabled={busy === "models" || disabledCount === 0} onClick={() => applyDisabled([])}>
              <RotateCcw size={15} />{tx("ui.codex_models_enable_all")}
            </button>
          </div>
          <p className="codex-models-note" role="note"><AlertTriangle size={15} />{tx("ui.codex_model_control_description")}</p>
          <label className="codex-model-search">
            <Search size={15} />
            <input value={modelQuery} onChange={(event) => setModelQuery(event.target.value)} placeholder={tx("ui.search")} aria-label={tx("ui.search")} />
          </label>
          <div className="codex-table-wrap">
            <table className="account-table codex-table">
              <thead>
                <tr>
                  <th>{tx("ui.model")}</th>
                  <th>{tx("ui.codex_models_accounts_count")}</th>
                  <th>{tx("ui.codex_models_channels_count")}</th>
                  <th>{tx("ui.status")}</th>
                  <th className="actions-header">{tx("ui.actions")}</th>
                </tr>
              </thead>
              <tbody>
                {filteredRows.map((row) => (
                  <tr key={row.id}>
                    <td><strong>{row.id}</strong></td>
                    <td>{row.accounts}</td>
                    <td>{row.channels}</td>
                    <td><span className={row.disabled ? "codex-model-state disabled" : "codex-model-state enabled"}>{tx(row.disabled ? "ui.disabled" : "ui.enabled")}</span></td>
                    <td className="actions-cell">
                      <button
                        className="button button-quiet button-small"
                        type="button"
                        disabled={busy === "models"}
                        onClick={() => applyDisabled(row.disabled ? disabledModels.filter((id) => id !== row.id) : [...disabledModels, row.id])}
                      >
                        {row.disabled ? tx("ui.codex_models_enable") : tx("ui.codex_models_disable")}
                      </button>
                    </td>
                  </tr>
                ))}
                {!loading && filteredRows.length === 0 ? <tr><td colSpan={5}>{tx("ui.codex_models_empty")}</td></tr> : null}
              </tbody>
            </table>
          </div>
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
                          {(field.options ?? []).map((option) => <option key={option} value={option}>{option}</option>)}
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

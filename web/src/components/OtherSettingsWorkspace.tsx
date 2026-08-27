import {
  AlertTriangle,
  BellRing,
  DoorClosed,
  ExternalLink,
  FlaskConical,
  KeyRound,
  LoaderCircle,
  PackageCheck,
  RefreshCw,
  RotateCcw,
  Save,
  Server,
  ShieldCheck,
  Type,
  UploadCloud,
  Workflow,
} from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import * as api from "../api/client";
import { operatorMessage } from "../format/operatorMessage";
import { useI18n } from "../i18n";
import type {
  CPAServerVersionSnapshot,
  ExperimentalSettings,
  ExperimentalSettingsSnapshot,
  UpdateSnapshot,
} from "../types";
import {
  readFontSize,
  readTypographyDistinction,
  writeFontSize,
  writeTypographyDistinction,
  type FontSizePreset,
} from "../store/fontSize";
import { ExternalNotificationSettings } from "./ExternalNotificationSettings";
import { ProxyProfilesSettings } from "./ProxyProfilesSettings";
import { AutomationPolicySettings } from "./AutomationPolicySettings";
import {
  announcePluginUpdateStatus,
  subscribePluginUpdateStatus,
} from "./PluginUpdateAutomation";
import {
  readPluginDensity,
  readPluginTheme,
  readPluginThemeEnabled,
  resetPluginTheme,
  setPluginDensity,
  setPluginTheme,
  setPluginThemeEnabled,
  type PluginDensity,
  type PluginThemePreset,
} from "../store/pluginTheme";

interface OtherSettingsWorkspaceProps {
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
  forceLoading?: boolean;
  onForcePreview?: () => void;
  onExperimentalSettingsChange?: (settings: ExperimentalSettings) => void;
}

const ignoreExperimentalSettingsChange = (_settings: ExperimentalSettings) =>
  undefined;

export function OtherSettingsWorkspace({
  onAPIError,
  onNotice,
  forceLoading = false,
  onForcePreview = () => undefined,
  onExperimentalSettingsChange = ignoreExperimentalSettingsChange,
}: OtherSettingsWorkspaceProps) {
  const { locale, tx, formatDateTime } = useI18n();
  const [updates, setUpdates] = useState<UpdateSnapshot | null>(null);
  const [server, setServer] = useState<CPAServerVersionSnapshot | null>(null);
  const [experiments, setExperiments] =
    useState<ExperimentalSettingsSnapshot | null>(null);
  const [activeSection, setActiveSection] = useState<
    "automation" | "notifications" | "updates" | "experimental"
  >("automation");
  const [fontSize, setFontSize] = useState<FontSizePreset>(readFontSize);
  const [typographyDistinction, setTypographyDistinction] = useState(
    readTypographyDistinction,
  );
  const [pluginTheme, setPluginThemeState] =
    useState<PluginThemePreset>(readPluginTheme);
  const [pluginDensity, setPluginDensityState] =
    useState<PluginDensity>(readPluginDensity);
  const [pluginThemeEnabled, setPluginThemeEnabledState] = useState(
    readPluginThemeEnabled,
  );
  const [notificationRefreshRevision, setNotificationRefreshRevision] =
    useState(0);
  const [automationRefreshRevision, setAutomationRefreshRevision] = useState(0);
  const [proxyRefreshRevision, setProxyRefreshRevision] = useState(0);
  const [loading, setLoading] = useState(true);
  const [checkingPlugin, setCheckingPlugin] = useState(false);
  const [checkingServer, setCheckingServer] = useState(false);
  const [installing, setInstalling] = useState(false);
  const [saving, setSaving] = useState(false);
  const [savingExperiment, setSavingExperiment] = useState(false);
  const [checkEnabled, setCheckEnabled] = useState(true);
  const [checkInterval, setCheckInterval] = useState("24");
  const [autoUpdate, setAutoUpdate] = useState(false);
  const [confirmAutoUpdate, setConfirmAutoUpdate] = useState(false);
  const [weeklyOverdraftEnabled, setWeeklyOverdraftEnabled] = useState(false);
  const [agentIdentityEnabled, setAgentIdentityEnabled] = useState(false);
  const [sub2APICreditUsageEnabled, setSub2APICreditUsageEnabled] =
    useState(false);
  const [codexOutboundConvergenceEnabled, setCodexOutboundConvergenceEnabled] =
    useState(false);
  const [codexIngressGateEnabled, setCodexIngressGateEnabled] = useState(false);
  const [codexAllowAppServerClients, setCodexAllowAppServerClients] =
    useState(false);
  const [codexConvergenceMode, setCodexConvergenceMode] = useState("");
  const [codexMinVersion, setCodexMinVersion] = useState("");
  const [codexMaxVersion, setCodexMaxVersion] = useState("");
  const [codexWhitelist, setCodexWhitelist] = useState("");
  const [codexBlacklist, setCodexBlacklist] = useState("");
  const [codexFingerprintSignals, setCodexFingerprintSignals] = useState("");
  const [error, setError] = useState("");
  const refreshSequence = useRef(0);
  const handleError = useCallback(
    (caught: unknown) => {
      if (caught instanceof api.APIError && caught.status === 401) {
        onAPIError(caught);
        return;
      }
      setError(
        operatorMessage(
          caught instanceof Error ? caught.message : tx("ui.request_failed"),
          locale,
        ),
      );
    },
    [locale, onAPIError, tx],
  );

  const refreshPlugin = useCallback(
    async (checkNow = false, signal?: AbortSignal) => {
      const next = await api.getEffectiveUpdateStatus(checkNow, signal);
      if (!signal?.aborted) setUpdates(next);
      return next;
    },
    [],
  );

  const refreshServer = useCallback(async (signal?: AbortSignal) => {
    const next = await api.getCPAServerVersionStatus(signal);
    if (!signal?.aborted) setServer(next);
    return next;
  }, []);

  const refreshExperiments = useCallback(
    async (signal?: AbortSignal) => {
      const next = await api.getExperimentalSettings(signal);
      if (!signal?.aborted) {
        setExperiments(next);
        onExperimentalSettingsChange(next.settings);
      }
      return next;
    },
    [onExperimentalSettingsChange],
  );

  const refreshAll = useCallback(
    async (signal?: AbortSignal) => {
      const sequence = refreshSequence.current + 1;
      refreshSequence.current = sequence;
      setLoading(true);
      setError("");
      try {
        await Promise.all([
          refreshPlugin(false, signal),
          refreshServer(signal),
          refreshExperiments(signal),
        ]);
      } catch (caught) {
        if (!signal?.aborted && refreshSequence.current === sequence)
          handleError(caught);
      } finally {
        if (!signal?.aborted && refreshSequence.current === sequence)
          setLoading(false);
      }
    },
    [handleError, refreshExperiments, refreshPlugin, refreshServer],
  );

  useEffect(() => {
    const controller = new AbortController();
    void refreshAll(controller.signal);
    return () => {
      refreshSequence.current += 1;
      controller.abort();
    };
  }, [refreshAll]);

  useEffect(() => subscribePluginUpdateStatus(setUpdates), []);

  useEffect(() => {
    if (!updates?.policy) return;
    setCheckEnabled(updates.policy.check_enabled);
    setCheckInterval(String(updates.policy.check_interval_hours || 24));
    setAutoUpdate(updates.policy.auto_update);
    if (updates.policy.auto_update) setConfirmAutoUpdate(false);
  }, [
    updates?.policy?.auto_update,
    updates?.policy?.check_enabled,
    updates?.policy?.check_interval_hours,
  ]);

  useEffect(() => {
    if (!experiments?.settings) return;
    setWeeklyOverdraftEnabled(
      experiments.settings.weekly_overdraft_enabled === true,
    );
    setAgentIdentityEnabled(
      experiments.settings.agent_identity_enabled === true,
    );
    setSub2APICreditUsageEnabled(
      experiments.settings.sub2api_credit_usage_enabled === true,
    );
    const codexIdentity = experiments.settings.codex_identity ?? {
      outbound_convergence_enabled: false,
      ingress_gate_enabled: false,
      allow_app_server_clients: false,
    };
    setCodexOutboundConvergenceEnabled(
      codexIdentity.outbound_convergence_enabled === true,
    );
    setCodexIngressGateEnabled(codexIdentity.ingress_gate_enabled === true);
    setCodexAllowAppServerClients(
      codexIdentity.allow_app_server_clients === true,
    );
    setCodexConvergenceMode(codexIdentity.convergence_mode || "");
    setCodexMinVersion(codexIdentity.min_version || "");
    setCodexMaxVersion(codexIdentity.max_version || "");
    setCodexWhitelist(codexIdentity.whitelist || "");
    setCodexBlacklist(codexIdentity.blacklist || "");
    setCodexFingerprintSignals(codexIdentity.fingerprint_signals || "");
  }, [experiments]);

  const installUpdate = useCallback(async () => {
    const version = updates?.latest_version;
    if (!version || installing) return;
    setInstalling(true);
    setError("");
    try {
      const result = await api.installPluginUpdate(version);
      if (updates) {
        const next = {
          ...updates,
          current_version: result.version,
          update_available: false,
        };
        setUpdates(next);
        announcePluginUpdateStatus(next);
      }
      onNotice(
        tx(
          result.restart_required
            ? "ui.plugin_version_installed_restart_cpa_to_activate_it"
            : "ui.plugin_version_installed_refresh_to_use_the_new_version",
          { version: result.version },
        ),
      );
    } catch (caught) {
      handleError(caught);
    } finally {
      setInstalling(false);
    }
  }, [handleError, installing, onNotice, tx, updates]);

  const checkPluginUpdates = async () => {
    setCheckingPlugin(true);
    setError("");
    try {
      const next = await refreshPlugin(true);
      announcePluginUpdateStatus(next);
    } catch (caught) {
      handleError(caught);
    } finally {
      setCheckingPlugin(false);
    }
  };

  const checkServerVersion = async () => {
    setCheckingServer(true);
    setError("");
    try {
      await refreshServer();
    } catch (caught) {
      handleError(caught);
    } finally {
      setCheckingServer(false);
    }
  };

  const saveUpdateSettings = async () => {
    const intervalHours = Number(checkInterval);
    setError("");
    if (
      !Number.isInteger(intervalHours) ||
      intervalHours < 1 ||
      intervalHours > 168
    ) {
      setError(tx("ui.update_check_interval_must_be_between_1_and_168_hours"));
      return;
    }
    if (autoUpdate && !checkEnabled) {
      setError(tx("ui.auto_update_requires_update_checks"));
      return;
    }
    if (autoUpdate && !updates?.policy?.auto_update && !confirmAutoUpdate) {
      setError(tx("ui.confirm_the_risk_before_enabling_auto_update"));
      return;
    }
    setSaving(true);
    try {
      const next = await api.saveUpdatePolicy(
        {
          check_enabled: checkEnabled,
          check_interval_hours: intervalHours,
          auto_update: autoUpdate,
        },
        confirmAutoUpdate,
      );
      setUpdates(next);
      announcePluginUpdateStatus(next);
      setConfirmAutoUpdate(false);
      onNotice(tx("ui.update_settings_saved"));
    } catch (caught) {
      handleError(caught);
    } finally {
      setSaving(false);
    }
  };

  const saveExperimentalSettings = async () => {
    setSavingExperiment(true);
    setError("");
    try {
      const next = await api.saveExperimentalSettings({
        weekly_overdraft_enabled: weeklyOverdraftEnabled,
        agent_identity_enabled: agentIdentityEnabled,
        auto_model_whitelist_enabled: true,
        sub2api_credit_usage_enabled: sub2APICreditUsageEnabled,
        codex_identity: {
          outbound_convergence_enabled: codexOutboundConvergenceEnabled,
          ingress_gate_enabled: codexIngressGateEnabled,
          allow_app_server_clients: codexAllowAppServerClients,
          convergence_mode: codexConvergenceMode,
          min_version: codexMinVersion,
          max_version: codexMaxVersion,
          whitelist: codexWhitelist,
          blacklist: codexBlacklist,
          fingerprint_signals: codexFingerprintSignals,
        },
      });
      setExperiments(next);
      onExperimentalSettingsChange(next.settings);
      onNotice(tx("ui.experimental_settings_saved"));
    } catch (caught) {
      handleError(caught);
    } finally {
      setSavingExperiment(false);
    }
  };

  const pluginBusy =
    checkingPlugin || Boolean(updates?.checking || updates?.pending);
  const updateFontSize = (next: FontSizePreset) => {
    setFontSize(next);
    writeFontSize(next);
  };
  const updateTypographyDistinction = (enabled: boolean) => {
    setTypographyDistinction(enabled);
    writeTypographyDistinction(enabled);
  };
  const updatePluginTheme = (next: PluginThemePreset) => {
    setPluginThemeState(next);
    setPluginTheme(next);
  };
  const updatePluginDensity = (next: PluginDensity) => {
    setPluginDensityState(next);
    setPluginDensity(next);
  };
  const updatePluginThemeEnabled = (enabled: boolean) => {
    setPluginThemeEnabledState(enabled);
    setPluginThemeEnabled(enabled);
  };
  const resetPluginAppearance = () => {
    resetPluginTheme();
    setPluginThemeState(readPluginTheme());
    setPluginDensityState(readPluginDensity());
    setPluginThemeEnabledState(readPluginThemeEnabled());
  };
  return (
    <section
      className="other-settings-panel"
      aria-label={tx("ui.other_settings")}
    >
      <header className="other-settings-toolbar">
        <div>
          <strong>{tx("ui.other_settings")}</strong>
          <span>{tx("ui.other_settings_description")}</span>
        </div>
        <button
          className="button button-quiet"
          type="button"
          disabled={loading}
          onClick={() => {
            setNotificationRefreshRevision((current) => current + 1);
            setAutomationRefreshRevision((current) => current + 1);
            setProxyRefreshRevision((current) => current + 1);
            void refreshAll();
          }}
        >
          <RefreshCw className={loading ? "spin" : ""} size={16} />
          {tx("ui.refresh")}
        </button>
      </header>

      <div
        className="other-settings-tabs"
        role="tablist"
        aria-label={tx("ui.other_settings_sections")}
      >
        <button
          type="button"
          role="tab"
          aria-selected={activeSection === "automation"}
          className={activeSection === "automation" ? "active" : ""}
          onClick={() => setActiveSection("automation")}
        >
          <Workflow size={15} />
          {tx("ui.automation_policy")}
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={activeSection === "notifications"}
          className={activeSection === "notifications" ? "active" : ""}
          onClick={() => setActiveSection("notifications")}
        >
          <BellRing size={15} />
          {tx("ui.external_notifications")}
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={activeSection === "updates"}
          className={activeSection === "updates" ? "active" : ""}
          onClick={() => setActiveSection("updates")}
        >
          <Server size={15} />
          {tx("ui.plugin_configuration_and_version")}
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={activeSection === "experimental"}
          className={activeSection === "experimental" ? "active" : ""}
          onClick={() => setActiveSection("experimental")}
        >
          <FlaskConical size={15} />
          {tx("ui.experimental_features")}
        </button>
      </div>

      {error ? (
        <div className="automation-error" role="alert">
          <AlertTriangle size={16} />
          <span>{error}</span>
          <button type="button" onClick={() => setError("")}>
            {tx("ui.close")}
          </button>
        </div>
      ) : null}

      {activeSection === "automation" ? (
        <>
          <AutomationPolicySettings
            refreshRevision={automationRefreshRevision}
            forceLoading={forceLoading}
            onAPIError={onAPIError}
            onNotice={onNotice}
            onForcePreview={onForcePreview}
          />
          <ProxyProfilesSettings
            refreshRevision={proxyRefreshRevision}
            onAPIError={onAPIError}
            onNotice={onNotice}
          />
        </>
      ) : activeSection === "notifications" ? (
        <ExternalNotificationSettings
          refreshRevision={notificationRefreshRevision}
          onAPIError={onAPIError}
          onNotice={onNotice}
        />
      ) : activeSection === "updates" ? (
        <div
          className="plugin-configuration-version-panel"
          role="tabpanel"
          aria-label={tx("ui.plugin_configuration_and_version")}
        >
          <section
            className="plugin-appearance-settings settings-section"
            aria-label={tx("ui.plugin_appearance")}
          >
            <div className="settings-section-heading">
              <div>
                <strong>{tx("ui.plugin_appearance")}</strong>
                <span>{tx("ui.plugin_appearance_description")}</span>
              </div>
            </div>
            <label className="switch-control">
              <input
                type="checkbox"
                checked={pluginThemeEnabled}
                onChange={(event) =>
                  updatePluginThemeEnabled(event.target.checked)
                }
              />
              <b>{tx(pluginThemeEnabled ? "ui.enabled" : "ui.disabled")}</b>
            </label>
            <div className="settings-inline-grid">
              <label className="filter-control">
                <span>{tx("ui.plugin_theme_preset")}</span>
                <select
                  value={pluginTheme}
                  disabled={!pluginThemeEnabled}
                  onChange={(event) =>
                    updatePluginTheme(event.target.value as PluginThemePreset)
                  }
                >
                  <option value="neutral">
                    {tx("ui.plugin_theme_neutral")}
                  </option>
                  <option value="indigo">{tx("ui.plugin_theme_indigo")}</option>
                  <option value="forest">{tx("ui.plugin_theme_forest")}</option>
                  <option value="rose">{tx("ui.plugin_theme_rose")}</option>
                </select>
              </label>
              <label className="filter-control">
                <span>{tx("ui.plugin_density")}</span>
                <select
                  value={pluginDensity}
                  onChange={(event) =>
                    updatePluginDensity(event.target.value as PluginDensity)
                  }
                >
                  <option value="comfortable">
                    {tx("ui.plugin_density_comfortable")}
                  </option>
                  <option value="compact">
                    {tx("ui.plugin_density_compact")}
                  </option>
                </select>
              </label>
            </div>
            <div className="settings-section-actions">
              <button
                className="button button-quiet"
                type="button"
                onClick={resetPluginAppearance}
              >
                <RotateCcw size={15} />
                {tx("ui.reset_plugin_appearance")}
              </button>
            </div>
          </section>
          <section
            className="font-size-settings settings-section"
            aria-label={tx("ui.font_size")}
          >
            <header>
              <Type size={18} />
              <div>
                <strong>{tx("ui.font_size")}</strong>
                <span>{tx("ui.font_size_description")}</span>
              </div>
            </header>
            <div className="font-size-settings-body">
              <div
                className="font-size-options"
                role="group"
                aria-label={tx("ui.font_size")}
              >
                {(["small", "medium", "large"] as const).map((preset) => (
                  <button
                    key={preset}
                    type="button"
                    className={fontSize === preset ? "active" : ""}
                    aria-pressed={fontSize === preset}
                    onClick={() => updateFontSize(preset)}
                  >
                    {tx(`ui.font_size_${preset}`)}
                  </button>
                ))}
              </div>
              <span className="font-size-current">
                {tx("ui.font_size_current", {
                  size: tx(`ui.font_size_${fontSize}`),
                })}
              </span>
            </div>
            <label className="font-distinction-setting">
              <span>
                <strong>{tx("ui.typography_distinction")}</strong>
                <small>{tx("ui.typography_distinction_description")}</small>
              </span>
              <input
                type="checkbox"
                checked={typographyDistinction}
                onChange={(event) =>
                  updateTypographyDistinction(event.target.checked)
                }
              />
              <b>{tx(typographyDistinction ? "ui.enabled" : "ui.disabled")}</b>
            </label>
          </section>
          <div className="other-settings-grid">
            <section
              className="settings-section server-version-section"
              aria-label={tx("ui.cpa_server_version")}
            >
              <header>
                <Server size={18} />
                <div>
                  <strong>{tx("ui.cpa_server_version")}</strong>
                  <span>{tx("ui.cpa_server_version_description")}</span>
                </div>
              </header>
              <div className="settings-version-grid">
                <div>
                  <span>{tx("ui.current_version")}</span>
                  <code>{server?.current_version || "-"}</code>
                </div>
                <div>
                  <span>{tx("ui.latest_version")}</span>
                  <code>{server?.latest_version || "-"}</code>
                </div>
                <div>
                  <span>{tx("ui.server_build_date")}</span>
                  <time>{formatDateTime(server?.current_build_date)}</time>
                </div>
                <div>
                  <span>{tx("ui.check_status")}</span>
                  <strong
                    className={server?.update_available ? "status-warning" : ""}
                  >
                    {serverStatusLabel(server, tx)}
                  </strong>
                </div>
              </div>
              {server?.update_available ? (
                <div className="settings-update-callout" role="status">
                  <UploadCloud size={18} />
                  <strong>
                    {tx("ui.new_server_version_available", {
                      version: server.latest_version || "-",
                    })}
                  </strong>
                </div>
              ) : null}
              <div className="settings-section-actions">
                {server?.release_url ? (
                  <a
                    className="button button-quiet"
                    href={server.release_url}
                    target="_blank"
                    rel="noopener noreferrer"
                  >
                    {tx("ui.release_notes")}
                    <ExternalLink size={13} />
                  </a>
                ) : null}
                <button
                  className="button button-primary"
                  type="button"
                  disabled={checkingServer}
                  onClick={() => void checkServerVersion()}
                >
                  {checkingServer ? (
                    <LoaderCircle className="spin" size={15} />
                  ) : (
                    <RefreshCw size={15} />
                  )}
                  {tx("ui.check_server_version")}
                </button>
              </div>
            </section>

            <section
              className="settings-section plugin-update-section"
              aria-label={tx("ui.plugin_updates")}
            >
              <header>
                <PackageCheck size={18} />
                <div>
                  <strong>{tx("ui.plugin_updates")}</strong>
                  <span>{tx("ui.cpa_plugin_store_updates")}</span>
                </div>
              </header>
              <div className="settings-version-grid">
                <div>
                  <span>{tx("ui.current_version")}</span>
                  <code>{updates?.current_version || "-"}</code>
                </div>
                <div>
                  <span>{tx("ui.latest_version")}</span>
                  <code>{updates?.latest_version || "-"}</code>
                </div>
                <div>
                  <span>{tx("ui.last_checked")}</span>
                  <time>{formatDateTime(updates?.checked_at)}</time>
                </div>
                <div>
                  <span>{tx("ui.check_status")}</span>
                  <strong
                    className={
                      updates?.update_available ? "status-warning" : ""
                    }
                  >
                    {pluginStatusLabel(updates, locale, tx)}
                  </strong>
                </div>
              </div>
              {updates?.update_available ? (
                <div className="settings-update-callout" role="status">
                  <UploadCloud size={18} />
                  <strong>
                    {tx("ui.version_version_available", {
                      version: updates.latest_version || "-",
                    })}
                  </strong>
                </div>
              ) : null}
              {updates?.runtime?.storage_error ? (
                <div className="experimental-storage-error" role="alert">
                  <AlertTriangle size={16} />
                  <span>
                    {tx("ui.runtime_ownership_storage_is_unavailable")}
                  </span>
                </div>
              ) : null}
              {updates?.runtime?.restart_recommended ? (
                <div className="experimental-storage-warning" role="status">
                  <AlertTriangle size={16} />
                  <span>{tx("ui.runtime_hot_reload_restart_recommended")}</span>
                </div>
              ) : null}
              <div className="update-policy-controls">
                <label>
                  <span>{tx("ui.check_for_updates")}</span>
                  <input
                    type="checkbox"
                    checked={checkEnabled}
                    disabled={saving}
                    onChange={(event) => {
                      setCheckEnabled(event.target.checked);
                      if (!event.target.checked) setAutoUpdate(false);
                    }}
                  />
                </label>
                <label>
                  <span>{tx("ui.check_interval")}</span>
                  <span className="number-suffix">
                    <input
                      type="number"
                      min="1"
                      max="168"
                      value={checkInterval}
                      disabled={!checkEnabled || saving}
                      onChange={(event) => setCheckInterval(event.target.value)}
                    />
                    <b>{tx("ui.hours")}</b>
                  </span>
                </label>
                <label>
                  <span>{tx("ui.auto_update")}</span>
                  <input
                    type="checkbox"
                    checked={autoUpdate}
                    disabled={saving}
                    onChange={(event) => {
                      setAutoUpdate(event.target.checked);
                      if (event.target.checked) setCheckEnabled(true);
                    }}
                  />
                </label>
              </div>
              {autoUpdate && !updates?.policy?.auto_update ? (
                <label className="destructive-confirmation update-confirmation other-settings-confirmation">
                  <input
                    type="checkbox"
                    checked={confirmAutoUpdate}
                    disabled={saving}
                    onChange={(event) =>
                      setConfirmAutoUpdate(event.target.checked)
                    }
                    aria-label={tx("ui.confirm_auto_update")}
                  />
                  <ShieldCheck size={15} />
                  <span>
                    {tx(
                      "ui.confirm_automatic_installation_of_versions_verified_by_the_cpa_plugin_store_while_authenticated_plugin_management_is_active",
                    )}
                  </span>
                </label>
              ) : null}
              <div className="settings-section-actions">
                <button
                  className="button button-quiet"
                  type="button"
                  disabled={pluginBusy}
                  onClick={() => void checkPluginUpdates()}
                >
                  {pluginBusy ? (
                    <LoaderCircle className="spin" size={15} />
                  ) : (
                    <RefreshCw size={15} />
                  )}
                  {tx("ui.check_for_updates")}
                </button>
                {updates?.release_url ? (
                  <a
                    className="button button-quiet"
                    href={updates.release_url}
                    target="_blank"
                    rel="noopener noreferrer"
                  >
                    {tx("ui.release_notes")}
                    <ExternalLink size={13} />
                  </a>
                ) : null}
                {updates?.update_available ? (
                  <button
                    className="button button-primary"
                    type="button"
                    disabled={installing}
                    onClick={() => void installUpdate()}
                  >
                    {installing ? (
                      <LoaderCircle className="spin" size={15} />
                    ) : (
                      <UploadCloud size={15} />
                    )}
                    {tx("ui.updated_2")}
                  </button>
                ) : null}
                <button
                  className="button button-primary"
                  type="button"
                  disabled={saving || !updates}
                  onClick={() => void saveUpdateSettings()}
                >
                  {saving ? (
                    <LoaderCircle className="spin" size={15} />
                  ) : (
                    <Save size={15} />
                  )}
                  {tx("ui.save_settings")}
                </button>
              </div>
            </section>
          </div>
        </div>
      ) : (
      ) : (
        <section
          className="experimental-settings-section"
          role="tabpanel"
          aria-label={tx("ui.experimental_features")}
        >
          <div className="experimental-warning" role="note">
            <AlertTriangle size={20} />
            <div>
              <strong>{tx("ui.experimental_features_warning")}</strong>
              <span>
                {tx("ui.experimental_features_may_change_or_stop_working")}
              </span>
            </div>
          </div>
          {experiments?.storage_error ? (
            <div className="experimental-storage-error" role="alert">
              <AlertTriangle size={16} />
              <span>{tx("ui.experimental_settings_storage_error")}</span>
            </div>
          ) : null}
          <div className="experimental-feature-block">
            <div className="experimental-feature-row">
              <div className="experimental-feature-copy">
                <span className="experimental-feature-icon">
                  <FlaskConical size={18} />
                </span>
                <div>
                  <strong>{tx("ui.codex_weekly_quota_overdraft")}</strong>
                  <span>
                    {tx("ui.codex_weekly_quota_overdraft_description")}
                  </span>
                </div>
              </div>
              <label className="switch-control experimental-feature-switch">
                <input
                  type="checkbox"
                  checked={weeklyOverdraftEnabled}
                  disabled={loading || savingExperiment || !experiments}
                  onChange={(event) =>
                    setWeeklyOverdraftEnabled(event.target.checked)
                  }
                  aria-label={tx("ui.codex_weekly_quota_overdraft")}
                />
                <b>{tx(weeklyOverdraftEnabled ? "ui.on_2" : "ui.off_2")}</b>
              </label>
            </div>
            <div className="experimental-behavior-list">
              <div>
                <strong>{tx("ui.request_behavior")}</strong>
                <span>{tx("ui.weekly_overdraft_request_behavior")}</span>
              </div>
              <div>
                <strong>{tx("ui.automation_behavior")}</strong>
                <span>{tx("ui.weekly_overdraft_automation_behavior")}</span>
              </div>
              <div>
                <strong>{tx("ui.availability_notice")}</strong>
                <span>{tx("ui.weekly_overdraft_availability_notice")}</span>
              </div>
            </div>
          </div>
          <div className="experimental-feature-block">
            <div className="experimental-feature-row">
              <div className="experimental-feature-copy">
                <span className="experimental-feature-icon">
                  <ShieldCheck size={18} />
                </span>
                <div>
                  <strong>{tx("ui.codex_identity_convergence")}</strong>
                  <span>{tx("ui.codex_identity_convergence_description")}</span>
                </div>
              </div>
              <label className="switch-control experimental-feature-switch">
                <input
                  type="checkbox"
                  checked={codexOutboundConvergenceEnabled}
                  disabled={loading || savingExperiment || !experiments}
                  onChange={(event) =>
                    setCodexOutboundConvergenceEnabled(event.target.checked)
                  }
                  aria-label={tx("ui.codex_outbound_convergence")}
                />
                <b>
                  {tx(codexOutboundConvergenceEnabled ? "ui.on_2" : "ui.off_2")}
                </b>
              </label>
            </div>
            <div className="experimental-behavior-list">
              <div>
                <strong>{tx("ui.codex_outbound_convergence")}</strong>
                <span>{tx("ui.codex_outbound_convergence_behavior")}</span>
              </div>
              <div>
                <strong>{tx("ui.codex_api_key_probe")}</strong>
                <span>{tx("ui.codex_api_key_probe_behavior")}</span>
              </div>
              <div>
                <strong>{tx("ui.internal_probe_requests")}</strong>
                <span>{tx("ui.codex_internal_probe_behavior")}</span>
              </div>
            </div>
            <div className="experimental-feature-row">
              <div className="experimental-feature-copy">
                <span className="experimental-feature-icon">
                  <DoorClosed size={18} />
                </span>
                <div>
                  <strong>{tx("ui.codex_ingress_gate")}</strong>
                  <span>{tx("ui.codex_ingress_gate_description")}</span>
                </div>
              </div>
              <label className="switch-control experimental-feature-switch">
                <input
                  type="checkbox"
                  checked={codexIngressGateEnabled}
                  disabled={loading || savingExperiment || !experiments}
                  onChange={(event) =>
                    setCodexIngressGateEnabled(event.target.checked)
                  }
                  aria-label={tx("ui.codex_ingress_gate")}
                />
                <b>{tx(codexIngressGateEnabled ? "ui.on_2" : "ui.off_2")}</b>
              </label>
            </div>
            <div className="experimental-behavior-list">
              <div>
                <strong>{tx("ui.codex_app_server_clients")}</strong>
                <span>{tx("ui.codex_app_server_clients_behavior")}</span>
              </div>
              <div>
                <strong>{tx("ui.codex_version_bounds")}</strong>
                <span>{tx("ui.codex_version_bounds_behavior")}</span>
              </div>
              <div>
                <strong>{tx("ui.codex_fingerprint_signals")}</strong>
                <span>{tx("ui.codex_fingerprint_signals_behavior")}</span>
              </div>
            </div>
            <details className="codex-identity-advanced">
              <summary>{tx("ui.codex_advanced_policy_json")}</summary>
              <div className="settings-inline-grid codex-policy-grid">
                <label className="filter-control">
                  <span>{tx("ui.codex_min_version")}</span>
                  <input
                    value={codexMinVersion}
                    placeholder="0.144.0"
                    onChange={(event) => setCodexMinVersion(event.target.value)}
                  />
                </label>
                <label className="filter-control">
                  <span>{tx("ui.codex_max_version")}</span>
                  <input
                    value={codexMaxVersion}
                    placeholder="0.144.9"
                    onChange={(event) => setCodexMaxVersion(event.target.value)}
                  />
                </label>
                <label className="filter-control">
                  <span>{tx("ui.codex_convergence_mode")}</span>
                  <select
                    value={codexConvergenceMode}
                    onChange={(event) =>
                      setCodexConvergenceMode(event.target.value)
                    }
                  >
                    <option value="">
                      {tx("ui.codex_convergence_legacy_full")}
                    </option>
                    <option value="off">
                      {tx("ui.codex_convergence_off")}
                    </option>
                    <option value="device">
                      {tx("ui.codex_convergence_device")}
                    </option>
                    <option value="session">
                      {tx("ui.codex_convergence_session")}
                    </option>
                    <option value="full">
                      {tx("ui.codex_convergence_full")}
                    </option>
                  </select>
                </label>
                <label className="switch-control">
                  <input
                    type="checkbox"
                    checked={codexAllowAppServerClients}
                    onChange={(event) =>
                      setCodexAllowAppServerClients(event.target.checked)
                    }
                  />
                  <b>
                    {tx(codexAllowAppServerClients ? "ui.on_2" : "ui.off_2")} ·{" "}
                    {tx("ui.codex_allow_app_server")}
                  </b>
                </label>
              </div>
              <label className="codex-policy-field">
                <span>{tx("ui.codex_whitelist_json")}</span>
                <textarea
                  rows={3}
                  spellCheck={false}
                  value={codexWhitelist}
                  onChange={(event) => setCodexWhitelist(event.target.value)}
                />
              </label>
              <label className="codex-policy-field">
                <span>{tx("ui.codex_blacklist_json")}</span>
                <textarea
                  rows={3}
                  spellCheck={false}
                  value={codexBlacklist}
                  onChange={(event) => setCodexBlacklist(event.target.value)}
                />
              </label>
              <label className="codex-policy-field">
                <span>{tx("ui.codex_fingerprint_json")}</span>
                <textarea
                  rows={4}
                  spellCheck={false}
                  value={codexFingerprintSignals}
                  onChange={(event) =>
                    setCodexFingerprintSignals(event.target.value)
                  }
                />
              </label>
            </details>
          </div>
          <div className="experimental-feature-block">
            <div className="experimental-feature-row">
              <div className="experimental-feature-copy">
                <span className="experimental-feature-icon">
                  <CircleDollarSign size={18} />
                </span>
                <div>
                  <strong>{tx("ui.sub2api_credit_usage")}</strong>
                  <span>{tx("ui.sub2api_credit_usage_description")}</span>
                </div>
              </div>
              <label className="switch-control experimental-feature-switch">
                <input
                  type="checkbox"
                  checked={sub2APICreditUsageEnabled}
                  disabled={loading || savingExperiment || !experiments}
                  onChange={(event) =>
                    setSub2APICreditUsageEnabled(event.target.checked)
                  }
                  aria-label={tx("ui.sub2api_credit_usage")}
                />
                <b>{tx(sub2APICreditUsageEnabled ? "ui.on_2" : "ui.off_2")}</b>
              </label>
            </div>
            <div className="experimental-behavior-list">
              <div>
                <strong>{tx("ui.credit_pricing_source")}</strong>
                <span>{tx("ui.credit_pricing_source_description")}</span>
              </div>
              <div>
                <strong>{tx("ui.credit_pricing_sync_behavior")}</strong>
                <span>{tx("ui.credit_pricing_sync_behavior_description")}</span>
              </div>
              <div>
                <strong>{tx("ui.credit_usage_history_boundary")}</strong>
                <span>
                  {tx("ui.credit_usage_history_boundary_description")}
                </span>
              </div>
            </div>
          </div>
          <div className="experimental-feature-block">
            <div className="experimental-feature-row">
              <div className="experimental-feature-copy">
                <span className="experimental-feature-icon">
                  <KeyRound size={18} />
                </span>
                <div>
                  <strong>{tx("ui.codex_agent_identity")}</strong>
                  <span>{tx("ui.codex_agent_identity_description")}</span>
                </div>
              </div>
              <label className="switch-control experimental-feature-switch">
                <input
                  type="checkbox"
                  checked={agentIdentityEnabled}
                  disabled={loading || savingExperiment || !experiments}
                  onChange={(event) =>
                    setAgentIdentityEnabled(event.target.checked)
                  }
                  aria-label={tx("ui.codex_agent_identity")}
                />
                <b>{tx(agentIdentityEnabled ? "ui.on_2" : "ui.off_2")}</b>
              </label>
            </div>
            <div className="experimental-behavior-list">
              <div>
                <strong>{tx("ui.authentication_path")}</strong>
                <span>{tx("ui.agent_identity_authentication_behavior")}</span>
              </div>
              <div>
                <strong>{tx("ui.supported_imports")}</strong>
                <span>{tx("ui.agent_identity_import_formats")}</span>
              </div>
              <div>
                <strong>{tx("ui.security_notice")}</strong>
                <span>{tx("ui.agent_identity_security_notice")}</span>
              </div>
            </div>
          </div>
          <div className="settings-section-actions experimental-actions">
            <button
              className="button button-primary"
              type="button"
              disabled={loading || savingExperiment || !experiments}
              onClick={() => void saveExperimentalSettings()}
            >
              {savingExperiment ? (
                <LoaderCircle className="spin" size={15} />
              ) : (
                <Save size={15} />
              )}
              {tx("ui.save_settings")}
            </button>
          </div>
        </section>
      )}
    </section>
  );
}

function serverStatusLabel(
  snapshot: CPAServerVersionSnapshot | null,
  tx: ReturnType<typeof useI18n>["tx"],
): string {
  if (!snapshot) return tx("ui.checking");
  if (snapshot.error === "current_version_unavailable")
    return tx("ui.current_server_version_unavailable");
  if (snapshot.error === "latest_version_unavailable")
    return tx("ui.server_version_check_failed");
  if (snapshot.error === "version_comparison_unavailable")
    return tx("ui.server_version_comparison_unavailable");
  return tx(
    snapshot.update_available ? "ui.update_available" : "ui.up_to_date",
  );
}

function pluginStatusLabel(
  snapshot: UpdateSnapshot | null,
  locale: Parameters<typeof operatorMessage>[1],
  tx: ReturnType<typeof useI18n>["tx"],
): string {
  if (!snapshot) return tx("ui.checking");
  if (snapshot.error) return operatorMessage(snapshot.error, locale);
  return tx(
    snapshot.checking || snapshot.pending
      ? "ui.checking"
      : snapshot.update_available
        ? "ui.update_available"
        : "ui.up_to_date",
  );
}

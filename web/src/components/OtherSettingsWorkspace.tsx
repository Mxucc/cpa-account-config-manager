import {
  AlertTriangle,
  ExternalLink,
  FlaskConical,
  LoaderCircle,
  PackageCheck,
  RefreshCw,
  Save,
  Server,
  ShieldCheck,
  UploadCloud,
} from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import * as api from "../api/client";
import { operatorMessage } from "../format/operatorMessage";
import { useI18n } from "../i18n";
import type { CPAServerVersionSnapshot, ExperimentalSettingsSnapshot, UpdateSnapshot } from "../types";

interface OtherSettingsWorkspaceProps {
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
}

export function OtherSettingsWorkspace({ onAPIError, onNotice }: OtherSettingsWorkspaceProps) {
  const { locale, tx, formatDateTime } = useI18n();
  const [updates, setUpdates] = useState<UpdateSnapshot | null>(null);
  const [server, setServer] = useState<CPAServerVersionSnapshot | null>(null);
  const [experiments, setExperiments] = useState<ExperimentalSettingsSnapshot | null>(null);
  const [activeSection, setActiveSection] = useState<"general" | "experimental">("general");
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
  const [error, setError] = useState("");
  const attemptedUpdate = useRef("");

  const handleError = useCallback((caught: unknown) => {
    if (caught instanceof api.APIError && caught.status === 401) {
      onAPIError(caught);
      return;
    }
    setError(operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale));
  }, [locale, onAPIError, tx]);

  const refreshPlugin = useCallback(async (checkNow = false) => {
    const next = await api.getEffectiveUpdateStatus(checkNow);
    setUpdates(next);
    return next;
  }, []);

  const refreshServer = useCallback(async () => {
    const next = await api.getCPAServerVersionStatus();
    setServer(next);
    return next;
  }, []);

  const refreshExperiments = useCallback(async () => {
    const next = await api.getExperimentalSettings();
    setExperiments(next);
    return next;
  }, []);

  const refreshAll = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const [nextUpdates] = await Promise.all([refreshPlugin(), refreshServer(), refreshExperiments()]);
      if (nextUpdates.policy.check_enabled && !nextUpdates.checked_at && !nextUpdates.checking && !nextUpdates.pending) {
        await refreshPlugin(true);
      }
    } catch (caught) {
      handleError(caught);
    } finally {
      setLoading(false);
    }
  }, [handleError, refreshExperiments, refreshPlugin, refreshServer]);

  useEffect(() => { void refreshAll(); }, [refreshAll]);

  useEffect(() => {
    if (!updates) return;
    setCheckEnabled(updates.policy.check_enabled);
    setCheckInterval(String(updates.policy.check_interval_hours || 24));
    setAutoUpdate(updates.policy.auto_update);
    if (updates.policy.auto_update) setConfirmAutoUpdate(false);
  }, [updates?.policy.auto_update, updates?.policy.check_enabled, updates?.policy.check_interval_hours]);

  useEffect(() => {
    if (!experiments) return;
    setWeeklyOverdraftEnabled(experiments.settings.weekly_overdraft_enabled === true);
  }, [experiments]);

  useEffect(() => {
    if (!updates?.checking && !updates?.pending) return;
    let polling = false;
    let cancelled = false;
    const poll = async () => {
      if (polling) return;
      polling = true;
      try {
        await refreshPlugin();
      } catch (caught) {
        if (!cancelled) handleError(caught);
      } finally {
        polling = false;
      }
    };
    const timer = window.setInterval(() => void poll(), 1200);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [handleError, refreshPlugin, updates?.checking, updates?.pending]);

  const installUpdate = useCallback(async (automatic = false) => {
    const version = updates?.latest_version;
    if (!version || installing) return;
    setInstalling(true);
    setError("");
    try {
      const result = await api.installPluginUpdate(version);
      attemptedUpdate.current = version;
      setUpdates((current) => current ? { ...current, current_version: result.version, update_available: false } : current);
      onNotice(result.restart_required
        ? tx("ui.plugin_version_installed_restart_cpa_to_activate_it", { version: result.version })
        : tx("ui.plugin_version_installed_refresh_to_use_the_new_version", { version: result.version }));
    } catch (caught) {
      attemptedUpdate.current = version;
      handleError(caught);
      if (automatic) setError(tx("ui.auto_update_did_not_complete_retry_it_from_update_status"));
    } finally {
      setInstalling(false);
    }
  }, [handleError, installing, onNotice, tx, updates?.latest_version]);

  useEffect(() => {
    if (!updates?.policy.auto_update || !updates.update_available || !updates.latest_version || attemptedUpdate.current === updates.latest_version) return;
    attemptedUpdate.current = updates.latest_version;
    void installUpdate(true);
  }, [installUpdate, updates]);

  useEffect(() => {
    if (!updates?.policy.check_enabled || !updates.checked_at) return;
    const checkedAt = Date.parse(updates.checked_at);
    if (!Number.isFinite(checkedAt)) return;
    const intervalHours = Math.min(168, Math.max(1, updates.policy.check_interval_hours || 24));
    const dueAt = checkedAt + intervalHours * 60 * 60 * 1000;
    const timer = window.setTimeout(() => void checkPluginUpdates(), Math.max(1_000, dueAt - Date.now()));
    return () => window.clearTimeout(timer);
  }, [updates?.checked_at, updates?.policy.check_enabled, updates?.policy.check_interval_hours]);

  const checkPluginUpdates = async () => {
    setCheckingPlugin(true);
    setError("");
    try {
      await refreshPlugin(true);
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
    if (!Number.isInteger(intervalHours) || intervalHours < 1 || intervalHours > 168) {
      setError(tx("ui.update_check_interval_must_be_between_1_and_168_hours"));
      return;
    }
    if (autoUpdate && !checkEnabled) {
      setError(tx("ui.auto_update_requires_update_checks"));
      return;
    }
    if (autoUpdate && !updates?.policy.auto_update && !confirmAutoUpdate) {
      setError(tx("ui.confirm_the_risk_before_enabling_auto_update"));
      return;
    }
    setSaving(true);
    try {
      setUpdates(await api.saveUpdatePolicy({ check_enabled: checkEnabled, check_interval_hours: intervalHours, auto_update: autoUpdate }, confirmAutoUpdate));
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
      setExperiments(await api.saveExperimentalSettings({ weekly_overdraft_enabled: weeklyOverdraftEnabled }));
      onNotice(tx("ui.experimental_settings_saved"));
    } catch (caught) {
      handleError(caught);
    } finally {
      setSavingExperiment(false);
    }
  };

  const pluginBusy = checkingPlugin || Boolean(updates?.checking || updates?.pending);
  return (
    <section className="other-settings-panel" aria-label={tx("ui.other_settings")}>
      <header className="other-settings-toolbar">
        <div><strong>{tx("ui.other_settings")}</strong><span>{tx("ui.other_settings_description")}</span></div>
        <button className="button button-quiet" type="button" disabled={loading} onClick={() => void refreshAll()}>
          <RefreshCw className={loading ? "spin" : ""} size={16} />{tx("ui.refresh")}
        </button>
      </header>

      <div className="other-settings-tabs" role="tablist" aria-label={tx("ui.other_settings_sections")}>
        <button type="button" role="tab" aria-selected={activeSection === "general"} className={activeSection === "general" ? "active" : ""} onClick={() => setActiveSection("general")}>
          <Server size={15} />{tx("ui.general_settings")}
        </button>
        <button type="button" role="tab" aria-selected={activeSection === "experimental"} className={activeSection === "experimental" ? "active" : ""} onClick={() => setActiveSection("experimental")}>
          <FlaskConical size={15} />{tx("ui.experimental_features")}
        </button>
      </div>

      {error ? <div className="automation-error" role="alert"><AlertTriangle size={16} /><span>{error}</span><button type="button" onClick={() => setError("")}>{tx("ui.close")}</button></div> : null}

      {activeSection === "general" ? <div className="other-settings-grid" role="tabpanel">
        <section className="settings-section server-version-section" aria-label={tx("ui.cpa_server_version")}>
          <header><Server size={18} /><div><strong>{tx("ui.cpa_server_version")}</strong><span>{tx("ui.cpa_server_version_description")}</span></div></header>
          <div className="settings-version-grid">
            <div><span>{tx("ui.current_version")}</span><code>{server?.current_version || "-"}</code></div>
            <div><span>{tx("ui.latest_version")}</span><code>{server?.latest_version || "-"}</code></div>
            <div><span>{tx("ui.server_build_date")}</span><time>{formatDateTime(server?.current_build_date)}</time></div>
            <div><span>{tx("ui.check_status")}</span><strong className={server?.update_available ? "status-warning" : ""}>{serverStatusLabel(server, tx)}</strong></div>
          </div>
          {server?.update_available ? (
            <div className="settings-update-callout" role="status"><UploadCloud size={18} /><strong>{tx("ui.new_server_version_available", { version: server.latest_version || "-" })}</strong></div>
          ) : null}
          <div className="settings-section-actions">
            {server?.release_url ? <a className="button button-quiet" href={server.release_url} target="_blank" rel="noopener noreferrer">{tx("ui.release_notes")}<ExternalLink size={13} /></a> : null}
            <button className="button button-primary" type="button" disabled={checkingServer} onClick={() => void checkServerVersion()}>
              {checkingServer ? <LoaderCircle className="spin" size={15} /> : <RefreshCw size={15} />}{tx("ui.check_server_version")}
            </button>
          </div>
        </section>

        <section className="settings-section plugin-update-section" aria-label={tx("ui.plugin_updates")}>
          <header><PackageCheck size={18} /><div><strong>{tx("ui.plugin_updates")}</strong><span>{tx("ui.cpa_plugin_store_updates")}</span></div></header>
          <div className="settings-version-grid">
            <div><span>{tx("ui.current_version")}</span><code>{updates?.current_version || "-"}</code></div>
            <div><span>{tx("ui.latest_version")}</span><code>{updates?.latest_version || "-"}</code></div>
            <div><span>{tx("ui.last_checked")}</span><time>{formatDateTime(updates?.checked_at)}</time></div>
            <div><span>{tx("ui.check_status")}</span><strong className={updates?.update_available ? "status-warning" : ""}>{pluginStatusLabel(updates, locale, tx)}</strong></div>
          </div>
          {updates?.update_available ? (
            <div className="settings-update-callout" role="status"><UploadCloud size={18} /><strong>{tx("ui.version_version_available", { version: updates.latest_version || "-" })}</strong></div>
          ) : null}
          <div className="update-policy-controls">
            <label><span>{tx("ui.check_for_updates")}</span><input type="checkbox" checked={checkEnabled} disabled={saving} onChange={(event) => { setCheckEnabled(event.target.checked); if (!event.target.checked) setAutoUpdate(false); }} /></label>
            <label><span>{tx("ui.check_interval")}</span><span className="number-suffix"><input type="number" min="1" max="168" value={checkInterval} disabled={!checkEnabled || saving} onChange={(event) => setCheckInterval(event.target.value)} /><b>{tx("ui.hours")}</b></span></label>
            <label><span>{tx("ui.auto_update")}</span><input type="checkbox" checked={autoUpdate} disabled={saving} onChange={(event) => { setAutoUpdate(event.target.checked); if (event.target.checked) setCheckEnabled(true); }} /></label>
          </div>
          {autoUpdate && !updates?.policy.auto_update ? (
            <label className="destructive-confirmation update-confirmation other-settings-confirmation">
              <input type="checkbox" checked={confirmAutoUpdate} disabled={saving} onChange={(event) => setConfirmAutoUpdate(event.target.checked)} aria-label={tx("ui.confirm_auto_update")} />
              <ShieldCheck size={15} /><span>{tx("ui.confirm_automatic_installation_of_versions_verified_by_the_cpa_plugin_store_while_this_page_is_open")}</span>
            </label>
          ) : null}
          <div className="settings-section-actions">
            <button className="button button-quiet" type="button" disabled={pluginBusy} onClick={() => void checkPluginUpdates()}>{pluginBusy ? <LoaderCircle className="spin" size={15} /> : <RefreshCw size={15} />}{tx("ui.check_for_updates")}</button>
            {updates?.release_url ? <a className="button button-quiet" href={updates.release_url} target="_blank" rel="noopener noreferrer">{tx("ui.release_notes")}<ExternalLink size={13} /></a> : null}
            {updates?.update_available ? <button className="button button-primary" type="button" disabled={installing} onClick={() => void installUpdate()}>{installing ? <LoaderCircle className="spin" size={15} /> : <UploadCloud size={15} />}{tx("ui.updated_2")}</button> : null}
            <button className="button button-primary" type="button" disabled={saving || !updates} onClick={() => void saveUpdateSettings()}>{saving ? <LoaderCircle className="spin" size={15} /> : <Save size={15} />}{tx("ui.save_settings")}</button>
          </div>
        </section>
      </div> : (
        <section className="experimental-settings-section" role="tabpanel" aria-label={tx("ui.experimental_features")}>
          <div className="experimental-warning" role="note">
            <AlertTriangle size={20} />
            <div><strong>{tx("ui.experimental_features_warning")}</strong><span>{tx("ui.experimental_features_may_change_or_stop_working")}</span></div>
          </div>
          {experiments?.storage_error ? <div className="experimental-storage-error" role="alert"><AlertTriangle size={16} /><span>{tx("ui.experimental_settings_storage_error")}</span></div> : null}
          <div className="experimental-feature-row">
            <div className="experimental-feature-copy">
              <span className="experimental-feature-icon"><FlaskConical size={18} /></span>
              <div>
                <strong>{tx("ui.codex_weekly_quota_overdraft")}</strong>
                <span>{tx("ui.codex_weekly_quota_overdraft_description")}</span>
              </div>
            </div>
            <label className="switch-control experimental-feature-switch">
              <input
                type="checkbox"
                checked={weeklyOverdraftEnabled}
                disabled={loading || savingExperiment || !experiments}
                onChange={(event) => setWeeklyOverdraftEnabled(event.target.checked)}
                aria-label={tx("ui.codex_weekly_quota_overdraft")}
              />
              <b>{tx(weeklyOverdraftEnabled ? "ui.on_2" : "ui.off_2")}</b>
            </label>
          </div>
          <div className="experimental-behavior-list">
            <div><strong>{tx("ui.request_behavior")}</strong><span>{tx("ui.weekly_overdraft_request_behavior")}</span></div>
            <div><strong>{tx("ui.automation_behavior")}</strong><span>{tx("ui.weekly_overdraft_automation_behavior")}</span></div>
            <div><strong>{tx("ui.availability_notice")}</strong><span>{tx("ui.weekly_overdraft_availability_notice")}</span></div>
          </div>
          <div className="settings-section-actions experimental-actions">
            <button className="button button-primary" type="button" disabled={loading || savingExperiment || !experiments} onClick={() => void saveExperimentalSettings()}>
              {savingExperiment ? <LoaderCircle className="spin" size={15} /> : <Save size={15} />}{tx("ui.save_settings")}
            </button>
          </div>
        </section>
      )}
    </section>
  );
}

function serverStatusLabel(snapshot: CPAServerVersionSnapshot | null, tx: ReturnType<typeof useI18n>["tx"]): string {
  if (!snapshot) return tx("ui.checking");
  if (snapshot.error === "current_version_unavailable") return tx("ui.current_server_version_unavailable");
  if (snapshot.error === "latest_version_unavailable") return tx("ui.server_version_check_failed");
  if (snapshot.error === "version_comparison_unavailable") return tx("ui.server_version_comparison_unavailable");
  return tx(snapshot.update_available ? "ui.update_available" : "ui.up_to_date");
}

function pluginStatusLabel(snapshot: UpdateSnapshot | null, locale: Parameters<typeof operatorMessage>[1], tx: ReturnType<typeof useI18n>["tx"]): string {
  if (!snapshot) return tx("ui.checking");
  if (snapshot.error) return operatorMessage(snapshot.error, locale);
  return tx(snapshot.checking || snapshot.pending ? "ui.checking" : snapshot.update_available ? "ui.update_available" : "ui.up_to_date");
}

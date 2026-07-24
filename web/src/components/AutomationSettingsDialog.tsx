import { AlertTriangle, BellRing, Eye, LoaderCircle, Save, Send, ShieldCheck, Trash2 } from "lucide-react";
import { useRef, useState } from "react";
import type {
  InspectionNotificationPreview,
  InspectionNotificationRequest,
  InspectionNotificationScenario,
  InspectionNotificationTestResult,
  InspectionPolicy,
} from "../types";
import { Modal } from "./Modal";
import { useI18n } from "../i18n";
import type { UIMessageKey } from "../i18n/uiText";

interface AutomationSettingsDialogProps {
  inspection: InspectionPolicy;
  saving: boolean;
  error?: string;
  onClose: () => void;
  onSave: (inspection: InspectionPolicy, confirmDelete: boolean, confirmDeleteInvalid: boolean) => void;
  onNotificationPreview?: (request: InspectionNotificationRequest) => Promise<InspectionNotificationPreview>;
  onNotificationTest?: (request: InspectionNotificationRequest) => Promise<InspectionNotificationTestResult>;
}

const notificationVariables: Array<{ name: string; label: UIMessageKey; suffix?: string }> = [
  { name: "event", label: "ui.notification_parameter_event" },
  { name: "total_accounts", label: "ui.notification_parameter_total_accounts" },
  { name: "eligible_accounts", label: "ui.notification_parameter_eligible_accounts" },
  { name: "available_accounts", label: "ui.notification_parameter_available_accounts" },
  { name: "available_percent", label: "ui.notification_parameter_available_percent", suffix: "%" },
  { name: "abnormal_accounts", label: "ui.notification_parameter_abnormal_accounts" },
  { name: "abnormal_percent", label: "ui.notification_parameter_abnormal_percent", suffix: "%" },
  { name: "quota_limited_accounts", label: "ui.notification_parameter_quota_limited_accounts" },
  { name: "invalid_credential_accounts", label: "ui.notification_parameter_invalid_credential_accounts" },
  { name: "deactivated_accounts", label: "ui.notification_parameter_deactivated_accounts" },
  { name: "unavailable_accounts", label: "ui.notification_parameter_unavailable_accounts" },
  { name: "disabled_accounts", label: "ui.notification_parameter_disabled_accounts" },
  { name: "threshold_percent", label: "ui.notification_parameter_threshold_percent", suffix: "%" },
  { name: "available_accounts_threshold", label: "ui.notification_parameter_available_accounts_threshold" },
  { name: "availability_percent_threshold", label: "ui.notification_parameter_availability_percent_threshold", suffix: "%" },
  { name: "triggered_at", label: "ui.notification_parameter_triggered_at" },
];

const notificationDetailsPreset = "__notification_details__";

const notificationScenarios: Array<{ value: InspectionNotificationScenario; label: UIMessageKey }> = [
  { value: "manual_test", label: "ui.notification_scenario_manual" },
  { value: "anomaly_threshold", label: "ui.notification_scenario_anomaly" },
  { value: "available_accounts_low", label: "ui.notification_scenario_available_accounts" },
  { value: "availability_percent_low", label: "ui.notification_scenario_availability_percent" },
  { value: "combined", label: "ui.notification_scenario_combined" },
];

type NotificationScenarioResult = {
  preview: InspectionNotificationPreview;
  test?: InspectionNotificationTestResult;
};

export function AutomationSettingsDialog({
  inspection,
  saving,
  error = "",
  onClose,
  onSave,
  onNotificationPreview,
  onNotificationTest,
}: AutomationSettingsDialogProps) {
  const { tx, formatDateTime } = useI18n();
  const [scheduleEnabled, setScheduleEnabled] = useState(inspection.enabled);
  const [scanInterval, setScanInterval] = useState(String(inspection.scan_interval_minutes));
  const [probeEnabled, setProbeEnabled] = useState(inspection.model_probe_enabled);
  const [fullProbeSweep, setFullProbeSweep] = useState(inspection.model_probe_full_sweep);
  const [scanManuallyDisabled, setScanManuallyDisabled] = useState(inspection.scan_manually_disabled);
  const [probeInterval, setProbeInterval] = useState(String(inspection.model_probe_interval_minutes));
  const [probeBatchSize, setProbeBatchSize] = useState(String(inspection.model_probe_batch_size));
  const [probeModels, setProbeModels] = useState({ ...inspection.model_probe_models });
  const [failureThreshold, setFailureThreshold] = useState(String(inspection.failure_threshold));
  const [recoveryThreshold, setRecoveryThreshold] = useState(String(inspection.recovery_threshold));
  const [passiveCircuit, setPassiveCircuit] = useState(inspection.passive_circuit_enabled ?? false);
  const [passiveThreshold, setPassiveThreshold] = useState(String(inspection.passive_failure_threshold ?? 5));
  const [passiveWindow, setPassiveWindow] = useState(String(inspection.passive_failure_window_minutes ?? 180));
  const [passiveDuration, setPassiveDuration] = useState(String(inspection.passive_circuit_minutes ?? 15));
  const [autoDisable, setAutoDisable] = useState(inspection.auto_disable);
  const [autoEnable, setAutoEnable] = useState(inspection.auto_enable);
  const [autoDelete, setAutoDelete] = useState(inspection.auto_delete);
  const [autoDeleteInvalid, setAutoDeleteInvalid] = useState(inspection.auto_delete_invalid_credentials);
  const [deleteGrace, setDeleteGrace] = useState(String(inspection.delete_grace_hours));
  const [deleteBatch, setDeleteBatch] = useState(String(inspection.delete_batch_size));
  const [anomalyEnabled, setAnomalyEnabled] = useState(inspection.anomaly_trigger_enabled);
  const [anomalyThreshold, setAnomalyThreshold] = useState(String(inspection.anomaly_threshold_percent));
  const [anomalyMinimum, setAnomalyMinimum] = useState(String(inspection.anomaly_minimum_accounts));
  const [anomalyCooldown, setAnomalyCooldown] = useState(String(inspection.anomaly_cooldown_minutes));
  const [anomalyNotificationEnabled, setAnomalyNotificationEnabled] = useState(inspection.anomaly_notification_enabled ?? false);
  const [notificationOnly, setNotificationOnly] = useState(inspection.anomaly_notification_only ?? false);
  const [availableNotificationEnabled, setAvailableNotificationEnabled] = useState(inspection.notification_available_accounts_enabled ?? false);
  const [availableNotificationThreshold, setAvailableNotificationThreshold] = useState(String(inspection.notification_available_accounts_threshold ?? 10));
  const [availabilityNotificationEnabled, setAvailabilityNotificationEnabled] = useState(inspection.notification_availability_percent_enabled ?? false);
  const [availabilityNotificationThreshold, setAvailabilityNotificationThreshold] = useState(String(inspection.notification_availability_percent_threshold ?? 20));
  const [notificationCooldown, setNotificationCooldown] = useState(String(inspection.notification_cooldown_minutes ?? 60));
  const [notificationURL, setNotificationURL] = useState(inspection.anomaly_notification_url ?? "");
  const [confirmDelete, setConfirmDelete] = useState(inspection.auto_delete);
  const [confirmDeleteInvalid, setConfirmDeleteInvalid] = useState(inspection.auto_delete_invalid_credentials);
  const [formError, setFormError] = useState("");
  const [notificationScenario, setNotificationScenario] = useState<InspectionNotificationScenario>("manual_test");
  const [notificationAction, setNotificationAction] = useState<"preview" | "test" | "">("");
  const [notificationResults, setNotificationResults] = useState<Partial<Record<InspectionNotificationScenario, NotificationScenarioResult>>>({});
  const notificationURLRef = useRef<HTMLInputElement>(null);

  const save = () => {
    setFormError("");
    const scanMinutes = Number(scanInterval);
    const failures = Number(failureThreshold);
    const probeMinutes = Number(probeInterval);
    const probeBatch = Number(probeBatchSize);
    const recoveries = Number(recoveryThreshold);
    const passiveFailures = Number(passiveThreshold);
    const passiveWindowMinutes = Number(passiveWindow);
    const passiveCircuitMinutes = Number(passiveDuration);
    const graceHours = Number(deleteGrace);
    const batchSize = Number(deleteBatch);
    const anomalyPct = Number(anomalyThreshold);
    const anomalyMin = Number(anomalyMinimum);
    const anomalyCooldownMinutes = Number(anomalyCooldown);
    const availableThreshold = Number(availableNotificationThreshold);
    const availabilityThreshold = Number(availabilityNotificationThreshold);
    const notificationCooldownMinutes = Number(notificationCooldown);
    if (!Number.isInteger(scanMinutes) || scanMinutes < 5 || scanMinutes > 1440) return setFormError(tx("ui.inspection_interval_must_be_between_5_and_1440_minutes"));
    if (!Number.isInteger(probeMinutes) || probeMinutes < 5 || probeMinutes > 1440) return setFormError(tx("ui.model_probe_interval_must_be_between_5_and_1440_minutes"));
    if (!Number.isInteger(probeBatch) || probeBatch < 1 || probeBatch > 200) return setFormError(tx("ui.model_probe_batch_must_be_between_1_and_200_accounts"));
    if (Object.values(probeModels).some((model) => !model.trim())) return setFormError(tx("ui.model_probe_models_are_required"));
    if (!Number.isInteger(failures) || failures < 2 || failures > 10) return setFormError(tx("ui.failure_threshold_must_be_between_2_and_10_events"));
    if (!Number.isInteger(recoveries) || recoveries < 1 || recoveries > 10) return setFormError(tx("ui.recovery_threshold_must_be_between_1_and_10_events"));
    if (!Number.isInteger(passiveFailures) || passiveFailures < 2 || passiveFailures > 100) return setFormError(tx("ui.passive_failure_threshold_must_be_between_2_and_100_events"));
    if (!Number.isInteger(passiveWindowMinutes) || passiveWindowMinutes < 1 || passiveWindowMinutes > 1440) return setFormError(tx("ui.passive_failure_window_must_be_between_1_and_1440_minutes"));
    if (!Number.isInteger(passiveCircuitMinutes) || passiveCircuitMinutes < 1 || passiveCircuitMinutes > 1440) return setFormError(tx("ui.passive_circuit_duration_must_be_between_1_and_1440_minutes"));
    if (!Number.isInteger(graceHours) || graceHours < 24 || graceHours > 8760) return setFormError(tx("ui.deletion_grace_must_be_between_24_and_8760_hours"));
    if (!Number.isInteger(batchSize) || batchSize < 1 || batchSize > 100) return setFormError(tx("ui.deletes_per_run_must_be_between_1_and_100"));
    if (!Number.isInteger(anomalyPct) || anomalyPct < 1 || anomalyPct > 100) return setFormError(tx("ui.anomaly_threshold_must_be_between_1_and_100_percent"));
    if (!Number.isInteger(anomalyMin) || anomalyMin < 1 || anomalyMin > 10000) return setFormError(tx("ui.anomaly_minimum_must_be_between_1_and_10000_accounts"));
    if (!Number.isInteger(anomalyCooldownMinutes) || anomalyCooldownMinutes < 5 || anomalyCooldownMinutes > 1440) return setFormError(tx("ui.anomaly_cooldown_must_be_between_5_and_1440_minutes"));
    if (!Number.isInteger(availableThreshold) || availableThreshold < 1 || availableThreshold > 10000) return setFormError(tx("ui.notification_available_accounts_must_be_between_1_and_10000"));
    if (!Number.isInteger(availabilityThreshold) || availabilityThreshold < 1 || availabilityThreshold > 100) return setFormError(tx("ui.notification_availability_percent_must_be_between_1_and_100"));
    if (!Number.isInteger(notificationCooldownMinutes) || notificationCooldownMinutes < 5 || notificationCooldownMinutes > 1440) return setFormError(tx("ui.notification_cooldown_must_be_between_5_and_1440_minutes"));
    const notificationEnabled = anomalyNotificationEnabled || availableNotificationEnabled || availabilityNotificationEnabled;
    if (notificationEnabled && !notificationURL.trim()) return setFormError(tx("ui.notification_url_is_required"));
    if (notificationURL.trim() && !notificationURL.trim().toLowerCase().startsWith("https://")) return setFormError(tx("ui.notification_url_must_use_https"));
    if (autoDelete && !autoDisable) return setFormError(tx("ui.auto_delete_requires_auto_disable"));
    if (passiveCircuit && (!autoDisable || !autoEnable)) return setFormError(tx("ui.passive_circuit_requires_auto_disable_and_auto_enable"));
    if (autoDelete && !inspection.auto_delete && !confirmDelete) return setFormError(tx("ui.confirm_the_risk_before_enabling_auto_delete"));
    if (autoDeleteInvalid && !inspection.auto_delete_invalid_credentials && !confirmDeleteInvalid) return setFormError(tx("ui.confirm_the_risk_before_deleting_invalid_credentials"));
    onSave({
      enabled: scheduleEnabled,
      scan_interval_minutes: scanMinutes,
      model_probe_enabled: probeEnabled,
      model_probe_full_sweep: fullProbeSweep,
      scan_manually_disabled: scanManuallyDisabled,
      model_probe_interval_minutes: probeMinutes,
      model_probe_batch_size: probeBatch,
      model_probe_models: Object.fromEntries(Object.entries(probeModels).map(([provider, model]) => [provider, model.trim()])) as InspectionPolicy["model_probe_models"],
      failure_threshold: failures,
      recovery_threshold: recoveries,
      passive_circuit_enabled: passiveCircuit,
      passive_failure_threshold: passiveFailures,
      passive_failure_window_minutes: passiveWindowMinutes,
      passive_circuit_minutes: passiveCircuitMinutes,
      auto_disable: autoDisable,
      auto_enable: autoEnable,
      auto_delete: autoDelete,
      auto_delete_invalid_credentials: autoDeleteInvalid,
      delete_grace_hours: graceHours,
      delete_batch_size: batchSize,
      anomaly_trigger_enabled: anomalyEnabled,
      anomaly_threshold_percent: anomalyPct,
      anomaly_minimum_accounts: anomalyMin,
      anomaly_cooldown_minutes: anomalyCooldownMinutes,
      anomaly_notification_enabled: anomalyNotificationEnabled,
      anomaly_notification_only: notificationOnly,
      anomaly_notification_url: notificationURL.trim(),
      notification_available_accounts_enabled: availableNotificationEnabled,
      notification_available_accounts_threshold: availableThreshold,
      notification_availability_percent_enabled: availabilityNotificationEnabled,
      notification_availability_percent_threshold: availabilityThreshold,
      notification_cooldown_minutes: notificationCooldownMinutes,
    }, confirmDelete, confirmDeleteInvalid);
  };

  const insertNotificationVariable = (name: string) => {
    if (!name) return;
    const insertion = name === notificationDetailsPreset
      ? tx("ui.notification_full_message_template")
      : `\${${name}}`;
    const input = notificationURLRef.current;
    const start = input?.selectionStart ?? notificationURL.length;
    const end = input?.selectionEnd ?? start;
    const next = `${notificationURL.slice(0, start)}${insertion}${notificationURL.slice(end)}`;
    setNotificationURL(next);
    window.setTimeout(() => {
      const cursor = start + insertion.length;
      notificationURLRef.current?.focus();
      notificationURLRef.current?.setSelectionRange(cursor, cursor);
    }, 0);
  };

  const buildNotificationRequest = (): InspectionNotificationRequest | null => {
    setFormError("");
    const urlTemplate = notificationURL.trim();
    const thresholdPercent = Number(anomalyThreshold);
    const availableAccountsThreshold = Number(availableNotificationThreshold);
    const availabilityPercentThreshold = Number(availabilityNotificationThreshold);
    if (!urlTemplate) {
      setFormError(tx("ui.notification_url_is_required"));
      return null;
    }
    if (!urlTemplate.toLowerCase().startsWith("https://")) {
      setFormError(tx("ui.notification_url_must_use_https"));
      return null;
    }
    if (!Number.isInteger(thresholdPercent) || thresholdPercent < 1 || thresholdPercent > 100) {
      setFormError(tx("ui.anomaly_threshold_must_be_between_1_and_100_percent"));
      return null;
    }
    if (!Number.isInteger(availableAccountsThreshold) || availableAccountsThreshold < 1 || availableAccountsThreshold > 10000) {
      setFormError(tx("ui.notification_available_accounts_must_be_between_1_and_10000"));
      return null;
    }
    if (!Number.isInteger(availabilityPercentThreshold) || availabilityPercentThreshold < 1 || availabilityPercentThreshold > 100) {
      setFormError(tx("ui.notification_availability_percent_must_be_between_1_and_100"));
      return null;
    }
    return {
      url_template: urlTemplate,
      scenario: notificationScenario,
      threshold_percent: thresholdPercent,
      available_accounts_threshold: availableAccountsThreshold,
      availability_percent_threshold: availabilityPercentThreshold,
    };
  };

  const previewNotification = async () => {
    if (!onNotificationPreview) return;
    const request = buildNotificationRequest();
    if (!request) return;
    setNotificationAction("preview");
    try {
      const preview = await onNotificationPreview(request);
      setNotificationResults((current) => ({
        ...current,
        [request.scenario]: { ...current[request.scenario], preview },
      }));
    } catch {
      // The workspace owns authenticated API error presentation.
    } finally {
      setNotificationAction("");
    }
  };

  const testNotification = async () => {
    if (!onNotificationTest) return;
    const request = buildNotificationRequest();
    if (!request) return;
    setNotificationAction("test");
    try {
      const test = await onNotificationTest(request);
      setNotificationResults((current) => ({
        ...current,
        [request.scenario]: { preview: test.preview, test },
      }));
    } catch {
      // The workspace owns authenticated API error presentation.
    } finally {
      setNotificationAction("");
    }
  };

  const notificationResult = notificationResults[notificationScenario];

  return (
    <Modal
      title={tx("ui.inspection_and_automation_settings")}
      wide
      onClose={onClose}
      footer={(
        <>
          <span className="modal-scope">{tx("ui.all_writes_require_management_authentication")}</span>
          <button className="button button-primary" type="button" disabled={saving} onClick={save}>
            {saving ? <LoaderCircle className="spin" size={15} /> : <Save size={15} />}{tx("ui.save_settings")}
          </button>
        </>
      )}
    >
      <div className="automation-settings">
        <section className="automation-settings-section">
          <header><ShieldCheck size={17} /><div><strong>{tx("ui.inspection_schedule")}</strong><span>{tx("ui.cpa_native_status_and_usage_evidence")}</span></div></header>
          <div className="automation-setting-grid">
            <SettingToggle label="ui.scheduled_inspection" checked={scheduleEnabled} disabled={saving} onChange={setScheduleEnabled} />
            <SettingNumber label="ui.inspection_interval" suffix="ui.minutes" value={scanInterval} min={5} max={1440} disabled={saving} onChange={setScanInterval} />
            <SettingNumber label="ui.failure_threshold" suffix="ui.events" value={failureThreshold} min={2} max={10} disabled={saving} onChange={setFailureThreshold} />
            <SettingNumber label="ui.recovery_threshold" suffix="ui.events" value={recoveryThreshold} min={1} max={10} disabled={saving} onChange={setRecoveryThreshold} />
          </div>
        </section>

        <section className="automation-settings-section">
          <header><ShieldCheck size={17} /><div><strong>{tx("ui.server_model_inspection")}</strong><span>{tx("ui.server_model_inspection_description")}</span></div></header>
          <div className="automation-setting-grid">
            <SettingToggle label="ui.scheduled_model_probes" checked={probeEnabled} disabled={saving} onChange={setProbeEnabled} />
            <SettingToggle label="ui.full_scheduled_active_inspection" checked={fullProbeSweep} disabled={!probeEnabled || saving} onChange={setFullProbeSweep} />
            <SettingToggle label="ui.probe_manually_disabled_accounts" checked={scanManuallyDisabled} disabled={!probeEnabled || saving} onChange={setScanManuallyDisabled} />
            <SettingNumber label="ui.model_probe_interval" suffix="ui.minutes" value={probeInterval} min={5} max={1440} disabled={!probeEnabled || saving} onChange={setProbeInterval} />
            <SettingNumber label="ui.accounts_per_probe_run" suffix="ui.accounts_2" value={probeBatchSize} min={1} max={200} disabled={!probeEnabled || saving} onChange={setProbeBatchSize} />
          </div>
          <div className="probe-model-grid">
            <ProbeModel label="ui.codex_model" value={probeModels.codex} disabled={saving} onChange={(value) => setProbeModels((current) => ({ ...current, codex: value }))} />
            <ProbeModel label="ui.openai_model" value={probeModels.openai} disabled={saving} onChange={(value) => setProbeModels((current) => ({ ...current, openai: value }))} />
            <ProbeModel label="ui.claude_model" value={probeModels.claude} disabled={saving} onChange={(value) => setProbeModels((current) => ({ ...current, claude: value }))} />
            <ProbeModel label="ui.gemini_model" value={probeModels.gemini} disabled={saving} onChange={(value) => setProbeModels((current) => ({ ...current, gemini: value }))} />
            <ProbeModel label="ui.grok_xai_model" value={probeModels.xai} disabled={saving} onChange={(value) => setProbeModels((current) => ({ ...current, xai: value }))} />
          </div>
          <p className="automation-setting-note">{tx("ui.active_probe_key_memory_note")}</p>
        </section>

        <section className="automation-settings-section">
          <header><AlertTriangle size={17} /><div><strong>{tx("ui.anomaly_trigger")}</strong><span>{tx("ui.anomaly_trigger_description")}</span></div></header>
          <div className="automation-setting-grid">
            <SettingToggle label="ui.enable_anomaly_trigger" checked={anomalyEnabled} disabled={saving} onChange={(checked) => { setAnomalyEnabled(checked); if (checked) setScheduleEnabled(true); else { setAnomalyNotificationEnabled(false); setNotificationOnly(false); } }} />
            <SettingNumber label="ui.anomaly_threshold" suffix="ui.percent" value={anomalyThreshold} min={1} max={100} disabled={!anomalyEnabled || saving} onChange={setAnomalyThreshold} />
            <SettingNumber label="ui.minimum_sample" suffix="ui.accounts_2" value={anomalyMinimum} min={1} max={10000} disabled={!anomalyEnabled || saving} onChange={setAnomalyMinimum} />
            <SettingNumber label="ui.trigger_cooldown" suffix="ui.minutes" value={anomalyCooldown} min={5} max={1440} disabled={!anomalyEnabled || saving} onChange={setAnomalyCooldown} />
          </div>
        </section>

        <section className="automation-settings-section">
          <header><BellRing size={17} /><div><strong>{tx("ui.external_notifications")}</strong><span>{tx("ui.external_notifications_description")}</span></div></header>
          <div className="automation-setting-grid">
            <SettingToggle label="ui.notify_on_anomaly_ratio" checked={anomalyNotificationEnabled} disabled={!anomalyEnabled || saving} onChange={(checked) => { setAnomalyNotificationEnabled(checked); if (checked) { setAnomalyEnabled(true); setScheduleEnabled(true); } else setNotificationOnly(false); }} />
            <SettingToggle label="ui.notification_only_mode" checked={notificationOnly} disabled={!anomalyNotificationEnabled || saving} onChange={setNotificationOnly} />
            <SettingToggle label="ui.notify_when_available_accounts_low" checked={availableNotificationEnabled} disabled={saving} onChange={(checked) => { setAvailableNotificationEnabled(checked); if (checked) setScheduleEnabled(true); }} />
            <SettingNumber label="ui.available_accounts_threshold" suffix="ui.accounts_2" value={availableNotificationThreshold} min={1} max={10000} disabled={!availableNotificationEnabled || saving} onChange={setAvailableNotificationThreshold} />
            <SettingToggle label="ui.notify_when_availability_low" checked={availabilityNotificationEnabled} disabled={saving} onChange={(checked) => { setAvailabilityNotificationEnabled(checked); if (checked) setScheduleEnabled(true); }} />
            <SettingNumber label="ui.availability_percent_threshold" suffix="ui.percent" value={availabilityNotificationThreshold} min={1} max={100} disabled={!availabilityNotificationEnabled || saving} onChange={setAvailabilityNotificationThreshold} />
            <SettingNumber label="ui.notification_cooldown" suffix="ui.minutes" value={notificationCooldown} min={5} max={1440} disabled={!(anomalyNotificationEnabled || availableNotificationEnabled || availabilityNotificationEnabled) || saving} onChange={setNotificationCooldown} />
          </div>
          <div className="notification-template-editor">
            <label className="notification-template-field">
              <span>{tx("ui.notification_url_template")}</span>
              <input
                ref={notificationURLRef}
                type="text"
                maxLength={4096}
                value={notificationURL}
                disabled={!(anomalyNotificationEnabled || availableNotificationEnabled || availabilityNotificationEnabled) || saving}
                onChange={(event) => setNotificationURL(event.target.value)}
                placeholder="https://notify.example/hook?available=${available_accounts}"
                aria-label={tx("ui.notification_url_template")}
                autoComplete="off"
                spellCheck={false}
              />
            </label>
            <label className="notification-variable-field">
              <span>{tx("ui.insert_notification_parameter")}</span>
              <select
                value=""
                disabled={!(anomalyNotificationEnabled || availableNotificationEnabled || availabilityNotificationEnabled) || !notificationURL.trim() || saving}
                onChange={(event) => insertNotificationVariable(event.target.value)}
                aria-label={tx("ui.insert_notification_parameter")}
              >
                <option value="">{tx("ui.select_parameter")}</option>
                <option value={notificationDetailsPreset}>{tx("ui.notification_parameter_full_details")}</option>
                {notificationVariables.map((variable) => (
                  <option key={variable.name} value={variable.name}>{tx(variable.label)} · ${"{"}{variable.name}{"}"}</option>
                ))}
              </select>
            </label>
          </div>
          {onNotificationPreview && onNotificationTest ? (
            <div className="notification-test-panel">
              <div className="notification-test-heading">
                <strong>{tx("ui.notification_preview_and_test")}</strong>
                <div className="notification-test-actions">
                  <button className="button button-quiet" type="button" disabled={saving || Boolean(notificationAction) || !notificationURL.trim()} onClick={() => void previewNotification()}>
                    {notificationAction === "preview" ? <LoaderCircle className="spin" size={14} /> : <Eye size={14} />}{tx("ui.preview_notification")}
                  </button>
                  <button className="button button-primary" type="button" disabled={saving || Boolean(notificationAction) || !notificationURL.trim()} onClick={() => void testNotification()}>
                    {notificationAction === "test" ? <LoaderCircle className="spin" size={14} /> : <Send size={14} />}{tx("ui.send_test_notification")}
                  </button>
                </div>
              </div>
              <div className="notification-scenario-tabs" role="tablist" aria-label={tx("ui.notification_test_scenario")}>
                {notificationScenarios.map((scenario) => (
                  <button
                    key={scenario.value}
                    type="button"
                    role="tab"
                    aria-selected={notificationScenario === scenario.value}
                    className={notificationScenario === scenario.value ? "is-active" : ""}
                    onClick={() => setNotificationScenario(scenario.value)}
                  >
                    {tx(scenario.label)}{notificationResults[scenario.value] ? <span aria-label={tx("ui.preview_ready")}>✓</span> : null}
                  </button>
                ))}
              </div>
              {notificationResult ? (
                <div className="notification-preview-result" role="tabpanel">
                  <div className="notification-preview-meta">
                    <span><b>{tx("ui.notification_event")}</b><code>{notificationResult.preview.event}</code></span>
                    <span><b>{tx("ui.generated_at")}</b><time>{formatDateTime(notificationResult.preview.triggered_at)}</time></span>
                    {notificationResult.test ? (
                      <>
                        <span className={notificationResult.test.delivered ? "is-success" : "is-failed"}><b>{tx("ui.delivery_result")}</b>{tx(notificationResult.test.delivered ? "ui.notification_delivered" : "ui.notification_failed")}</span>
                        <span><b>{tx("ui.http_status")}</b><code>{notificationResult.test.status_code || "-"}</code></span>
                        <span><b>{tx("ui.attempts")}</b><code>{notificationResult.test.attempts}</code></span>
                      </>
                    ) : null}
                  </div>
                  <label className="notification-expanded-url">
                    <span>{tx("ui.exact_get_url")}</span>
                    <code>{notificationResult.preview.expanded_url}</code>
                  </label>
                  <div className="notification-variable-values">
                    <strong>{tx("ui.current_variable_values")}</strong>
                    <dl>
                      {notificationVariables.map((variable) => (
                        <div key={variable.name}><dt>{tx(variable.label)}<code>${"{"}{variable.name}{"}"}</code></dt><dd>{notificationResult.preview.variables[variable.name] === undefined ? "-" : `${notificationResult.preview.variables[variable.name]}${variable.suffix ?? ""}`}</dd></div>
                      ))}
                    </dl>
                  </div>
                </div>
              ) : null}
            </div>
          ) : null}
        </section>

        <section className="automation-settings-section">
          <header><AlertTriangle size={17} /><div><strong>{tx("ui.account_disposition")}</strong><span>{tx("ui.only_accounts_disabled_by_inspection_can_be_restored")}</span></div></header>
          <div className="automation-setting-grid">
            <SettingToggle label="ui.auto_disable" checked={autoDisable} disabled={saving} onChange={(checked) => { setAutoDisable(checked); if (checked) setScheduleEnabled(true); else { setAutoDelete(false); setPassiveCircuit(false); } }} />
            <SettingToggle label="ui.auto_enable" checked={autoEnable} disabled={saving} onChange={(checked) => { setAutoEnable(checked); if (checked) setScheduleEnabled(true); else setPassiveCircuit(false); }} />
            <SettingToggle label="ui.passive_temporary_circuit" checked={passiveCircuit} disabled={saving} onChange={(checked) => { setPassiveCircuit(checked); if (checked) { setAutoDisable(true); setAutoEnable(true); setScheduleEnabled(true); } }} />
            <SettingNumber label="ui.passive_failure_threshold" suffix="ui.events" value={passiveThreshold} min={2} max={100} disabled={!passiveCircuit || saving} onChange={setPassiveThreshold} />
            <SettingNumber label="ui.passive_failure_window" suffix="ui.minutes" value={passiveWindow} min={1} max={1440} disabled={!passiveCircuit || saving} onChange={setPassiveWindow} />
            <SettingNumber label="ui.passive_circuit_duration" suffix="ui.minutes" value={passiveDuration} min={1} max={1440} disabled={!passiveCircuit || saving} onChange={setPassiveDuration} />
            <SettingToggle label="ui.auto_delete" checked={autoDelete} disabled={saving} danger onChange={(checked) => { setAutoDelete(checked); if (checked) { setAutoDisable(true); setScheduleEnabled(true); } else setAutoDeleteInvalid(false); }} />
            <SettingToggle label="ui.delete_persistent_invalid_credentials" checked={autoDeleteInvalid} disabled={saving} danger onChange={(checked) => { setAutoDeleteInvalid(checked); if (checked) { setAutoDelete(true); setAutoDisable(true); setScheduleEnabled(true); } }} />
            <SettingNumber label="ui.deletion_grace" suffix="ui.hours" value={deleteGrace} min={24} max={8760} disabled={!autoDelete || saving} onChange={setDeleteGrace} />
            <SettingNumber label="ui.deletes_per_run" suffix="ui.accounts_2" value={deleteBatch} min={1} max={100} disabled={!autoDelete || saving} onChange={setDeleteBatch} />
          </div>
          <p className="automation-setting-note">{tx("ui.passive_circuit_description")}</p>
          {autoDelete && !inspection.auto_delete ? (
            <label className="destructive-confirmation">
              <input type="checkbox" checked={confirmDelete} disabled={saving} onChange={(event) => setConfirmDelete(event.target.checked)} aria-label={tx("ui.confirm_auto_delete")} />
              <Trash2 size={15} /><span>{tx("ui.confirm_auto_delete_only_for_explicitly_deactivated_accounts_disabled_by_inspection_after_the_grace_period")}</span>
            </label>
          ) : null}
          {autoDeleteInvalid && !inspection.auto_delete_invalid_credentials ? (
            <label className="destructive-confirmation">
              <input type="checkbox" checked={confirmDeleteInvalid} disabled={saving} onChange={(event) => setConfirmDeleteInvalid(event.target.checked)} aria-label={tx("ui.confirm_invalid_credential_deletion")} />
              <Trash2 size={15} /><span>{tx("ui.confirm_delete_only_after_persistent_high_confidence_auth_failure_inspection_disable_grace_and_revalidation")}</span>
            </label>
          ) : null}
        </section>

        {formError || error ? <div className="form-error" role="alert">{formError || error}</div> : null}
      </div>
    </Modal>
  );
}

function ProbeModel({ label, value, disabled, onChange }: { label: UIMessageKey; value: string; disabled: boolean; onChange: (value: string) => void }) {
  const { tx } = useI18n();
  return <label className="probe-model-field"><span>{tx(label)}</span><input type="text" maxLength={128} value={value} disabled={disabled} onChange={(event) => onChange(event.target.value)} aria-label={tx(label)} /></label>;
}

function SettingToggle({ label, checked, disabled, danger = false, onChange }: { label: UIMessageKey; checked: boolean; disabled: boolean; danger?: boolean; onChange: (checked: boolean) => void }) {
  const { tx } = useI18n();
  return (
    <label className={`automation-setting ${checked ? "is-enabled" : ""} ${danger ? "is-danger" : ""}`}>
      <span>{tx(label)}</span>
      <span className="switch-control"><input type="checkbox" checked={checked} disabled={disabled} onChange={(event) => onChange(event.target.checked)} aria-label={tx(label)} /><b>{tx(checked ? "ui.on_2" : "ui.off_2")}</b></span>
    </label>
  );
}

function SettingNumber({ label, suffix, value, min, max, disabled, onChange }: { label: UIMessageKey; suffix: UIMessageKey; value: string; min: number; max: number; disabled: boolean; onChange: (value: string) => void }) {
  const { tx } = useI18n();
  return (
    <label className="automation-setting automation-setting-number">
      <span>{tx(label)}</span>
      <span className="number-suffix"><input type="number" min={min} max={max} step="1" value={value} disabled={disabled} onChange={(event) => onChange(event.target.value)} aria-label={tx(label)} /><b>{tx(suffix)}</b></span>
    </label>
  );
}

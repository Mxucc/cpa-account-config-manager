import { AlertTriangle, LoaderCircle, Save, ShieldCheck, Trash2 } from "lucide-react";
import { useState } from "react";
import type { InspectionPolicy } from "../types";
import { Modal } from "./Modal";
import { useI18n } from "../i18n";
import type { UIMessageKey } from "../i18n/uiText";

interface AutomationSettingsDialogProps {
  inspection: InspectionPolicy;
  saving: boolean;
  error?: string;
  onClose: () => void;
  onSave: (inspection: InspectionPolicy, confirmDelete: boolean, confirmDeleteInvalid: boolean) => void;
}

export function AutomationSettingsDialog({ inspection, saving, error = "", onClose, onSave }: AutomationSettingsDialogProps) {
  const { tx } = useI18n();
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
  const [confirmDelete, setConfirmDelete] = useState(inspection.auto_delete);
  const [confirmDeleteInvalid, setConfirmDeleteInvalid] = useState(inspection.auto_delete_invalid_credentials);
  const [formError, setFormError] = useState("");

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
    }, confirmDelete, confirmDeleteInvalid);
  };

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
            <SettingToggle label="ui.enable_anomaly_trigger" checked={anomalyEnabled} disabled={saving} onChange={(checked) => { setAnomalyEnabled(checked); if (checked) setScheduleEnabled(true); }} />
            <SettingNumber label="ui.anomaly_threshold" suffix="ui.percent" value={anomalyThreshold} min={1} max={100} disabled={!anomalyEnabled || saving} onChange={setAnomalyThreshold} />
            <SettingNumber label="ui.minimum_sample" suffix="ui.accounts_2" value={anomalyMinimum} min={1} max={10000} disabled={!anomalyEnabled || saving} onChange={setAnomalyMinimum} />
            <SettingNumber label="ui.trigger_cooldown" suffix="ui.minutes" value={anomalyCooldown} min={5} max={1440} disabled={!anomalyEnabled || saving} onChange={setAnomalyCooldown} />
          </div>
        </section>

        <section className="automation-settings-section">
          <header><AlertTriangle size={17} /><div><strong>{tx("ui.account_disposition")}</strong><span>{tx("ui.only_accounts_disabled_by_inspection_can_be_restored")}</span></div></header>
          <div className="automation-setting-grid">
            <SettingToggle label="ui.auto_disable" checked={autoDisable} disabled={saving} onChange={(checked) => { setAutoDisable(checked); if (!checked) { setAutoDelete(false); setPassiveCircuit(false); } }} />
            <SettingToggle label="ui.auto_enable" checked={autoEnable} disabled={saving} onChange={(checked) => { setAutoEnable(checked); if (!checked) setPassiveCircuit(false); }} />
            <SettingToggle label="ui.passive_temporary_circuit" checked={passiveCircuit} disabled={saving} onChange={(checked) => { setPassiveCircuit(checked); if (checked) { setAutoDisable(true); setAutoEnable(true); } }} />
            <SettingNumber label="ui.passive_failure_threshold" suffix="ui.events" value={passiveThreshold} min={2} max={100} disabled={!passiveCircuit || saving} onChange={setPassiveThreshold} />
            <SettingNumber label="ui.passive_failure_window" suffix="ui.minutes" value={passiveWindow} min={1} max={1440} disabled={!passiveCircuit || saving} onChange={setPassiveWindow} />
            <SettingNumber label="ui.passive_circuit_duration" suffix="ui.minutes" value={passiveDuration} min={1} max={1440} disabled={!passiveCircuit || saving} onChange={setPassiveDuration} />
            <SettingToggle label="ui.auto_delete" checked={autoDelete} disabled={saving} danger onChange={(checked) => { setAutoDelete(checked); if (checked) setAutoDisable(true); else setAutoDeleteInvalid(false); }} />
            <SettingToggle label="ui.delete_persistent_invalid_credentials" checked={autoDeleteInvalid} disabled={saving} danger onChange={(checked) => { setAutoDeleteInvalid(checked); if (checked) { setAutoDelete(true); setAutoDisable(true); } }} />
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

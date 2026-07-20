import { AlertTriangle, LoaderCircle, Save, ShieldCheck, Trash2 } from "lucide-react";
import { useState } from "react";
import type { InspectionPolicy, UpdatePolicy } from "../types";
import { Modal } from "./Modal";
import { useI18n } from "../i18n";
import type { UIMessageKey } from "../i18n/uiText";

interface AutomationSettingsDialogProps {
  inspection: InspectionPolicy;
  updates: UpdatePolicy;
  saving: boolean;
  error?: string;
  onClose: () => void;
  onSave: (inspection: InspectionPolicy, updates: UpdatePolicy, confirmDelete: boolean, confirmUpdate: boolean) => void;
}

export function AutomationSettingsDialog({ inspection, updates, saving, error = "", onClose, onSave }: AutomationSettingsDialogProps) {
  const { tx } = useI18n();
  const [scheduleEnabled, setScheduleEnabled] = useState(inspection.enabled);
  const [scanInterval, setScanInterval] = useState(String(inspection.scan_interval_minutes));
  const [failureThreshold, setFailureThreshold] = useState(String(inspection.failure_threshold));
  const [recoveryThreshold, setRecoveryThreshold] = useState(String(inspection.recovery_threshold));
  const [autoDisable, setAutoDisable] = useState(inspection.auto_disable);
  const [autoEnable, setAutoEnable] = useState(inspection.auto_enable);
  const [autoDelete, setAutoDelete] = useState(inspection.auto_delete);
  const [deleteGrace, setDeleteGrace] = useState(String(inspection.delete_grace_hours));
  const [deleteBatch, setDeleteBatch] = useState(String(inspection.delete_batch_size));
  const [checkEnabled, setCheckEnabled] = useState(updates.check_enabled);
  const [checkInterval, setCheckInterval] = useState(String(updates.check_interval_hours));
  const [autoUpdate, setAutoUpdate] = useState(updates.auto_update);
  const [confirmDelete, setConfirmDelete] = useState(inspection.auto_delete);
  const [confirmUpdate, setConfirmUpdate] = useState(updates.auto_update);
  const [formError, setFormError] = useState("");

  const save = () => {
    setFormError("");
    const scanMinutes = Number(scanInterval);
    const failures = Number(failureThreshold);
    const recoveries = Number(recoveryThreshold);
    const graceHours = Number(deleteGrace);
    const batchSize = Number(deleteBatch);
    const updateHours = Number(checkInterval);
    if (!Number.isInteger(scanMinutes) || scanMinutes < 5 || scanMinutes > 1440) return setFormError(tx("ui.inspection_interval_must_be_between_5_and_1440_minutes"));
    if (!Number.isInteger(failures) || failures < 2 || failures > 10) return setFormError(tx("ui.failure_threshold_must_be_between_2_and_10_events"));
    if (!Number.isInteger(recoveries) || recoveries < 1 || recoveries > 10) return setFormError(tx("ui.recovery_threshold_must_be_between_1_and_10_events"));
    if (!Number.isInteger(graceHours) || graceHours < 24 || graceHours > 8760) return setFormError(tx("ui.deletion_grace_must_be_between_24_and_8760_hours"));
    if (!Number.isInteger(batchSize) || batchSize < 1 || batchSize > 100) return setFormError(tx("ui.deletes_per_run_must_be_between_1_and_100"));
    if (!Number.isInteger(updateHours) || updateHours < 1 || updateHours > 168) return setFormError(tx("ui.update_check_interval_must_be_between_1_and_168_hours"));
    if (autoDelete && !autoDisable) return setFormError(tx("ui.auto_delete_requires_auto_disable"));
    if (autoDelete && !inspection.auto_delete && !confirmDelete) return setFormError(tx("ui.confirm_the_risk_before_enabling_auto_delete"));
    if (autoUpdate && !checkEnabled) return setFormError(tx("ui.auto_update_requires_update_checks"));
    if (autoUpdate && !updates.auto_update && !confirmUpdate) return setFormError(tx("ui.confirm_the_risk_before_enabling_auto_update"));
    onSave({
      enabled: scheduleEnabled,
      scan_interval_minutes: scanMinutes,
      failure_threshold: failures,
      recovery_threshold: recoveries,
      auto_disable: autoDisable,
      auto_enable: autoEnable,
      auto_delete: autoDelete,
      delete_grace_hours: graceHours,
      delete_batch_size: batchSize,
    }, {
      check_enabled: checkEnabled,
      check_interval_hours: updateHours,
      auto_update: autoUpdate,
    }, confirmDelete, confirmUpdate);
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
          <header><AlertTriangle size={17} /><div><strong>{tx("ui.account_disposition")}</strong><span>{tx("ui.only_accounts_disabled_by_inspection_can_be_restored")}</span></div></header>
          <div className="automation-setting-grid">
            <SettingToggle label="ui.auto_disable" checked={autoDisable} disabled={saving} onChange={(checked) => { setAutoDisable(checked); if (!checked) setAutoDelete(false); }} />
            <SettingToggle label="ui.auto_enable" checked={autoEnable} disabled={saving} onChange={setAutoEnable} />
            <SettingToggle label="ui.auto_delete" checked={autoDelete} disabled={saving} danger onChange={(checked) => { setAutoDelete(checked); if (checked) setAutoDisable(true); }} />
            <SettingNumber label="ui.deletion_grace" suffix="ui.hours" value={deleteGrace} min={24} max={8760} disabled={!autoDelete || saving} onChange={setDeleteGrace} />
            <SettingNumber label="ui.deletes_per_run" suffix="ui.accounts_2" value={deleteBatch} min={1} max={100} disabled={!autoDelete || saving} onChange={setDeleteBatch} />
          </div>
          {autoDelete && !inspection.auto_delete ? (
            <label className="destructive-confirmation">
              <input type="checkbox" checked={confirmDelete} disabled={saving} onChange={(event) => setConfirmDelete(event.target.checked)} aria-label={tx("ui.confirm_auto_delete")} />
              <Trash2 size={15} /><span>{tx("ui.confirm_auto_delete_only_for_explicitly_deactivated_accounts_disabled_by_inspection_after_the_grace_period")}</span>
            </label>
          ) : null}
        </section>

        <section className="automation-settings-section">
          <header><ShieldCheck size={17} /><div><strong>{tx("ui.plugin_updates")}</strong><span>{tx("ui.github_releases_and_the_cpa_plugin_store")}</span></div></header>
          <div className="automation-setting-grid">
            <SettingToggle label="ui.check_for_updates" checked={checkEnabled} disabled={saving} onChange={(checked) => { setCheckEnabled(checked); if (!checked) setAutoUpdate(false); }} />
            <SettingNumber label="ui.check_interval" suffix="ui.hours" value={checkInterval} min={1} max={168} disabled={!checkEnabled || saving} onChange={setCheckInterval} />
            <SettingToggle label="ui.auto_update" checked={autoUpdate} disabled={saving} onChange={(checked) => { setAutoUpdate(checked); if (checked) setCheckEnabled(true); }} />
          </div>
          {autoUpdate && !updates.auto_update ? (
            <label className="destructive-confirmation update-confirmation">
              <input type="checkbox" checked={confirmUpdate} disabled={saving} onChange={(event) => setConfirmUpdate(event.target.checked)} aria-label={tx("ui.confirm_auto_update")} />
              <ShieldCheck size={15} /><span>{tx("ui.confirm_automatic_installation_of_versions_verified_by_the_cpa_plugin_store_while_this_page_is_open")}</span>
            </label>
          ) : null}
        </section>

        {formError || error ? <div className="form-error" role="alert">{formError || error}</div> : null}
      </div>
    </Modal>
  );
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

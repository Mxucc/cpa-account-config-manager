import { AlertCircle, LoaderCircle, RefreshCw, Save, ScanLine, ShieldAlert } from "lucide-react";
import { useState } from "react";
import { operatorMessage } from "../format/operatorMessage";
import type { DefaultPolicy, PolicySnapshot } from "../types";
import { Modal } from "./Modal";
import { useI18n } from "../i18n";

interface PolicyDialogProps {
  snapshot: PolicySnapshot;
  saving: boolean;
  scanning: boolean;
  forceLoading: boolean;
  error?: string;
  onClose: () => void;
  onSave: (policy: DefaultPolicy) => void;
  onScan: () => void;
  onForcePreview: () => void;
}

export function PolicyDialog({
  snapshot,
  saving,
  scanning,
  forceLoading,
  error = "",
  onClose,
  onSave,
  onScan,
  onForcePreview,
}: PolicyDialogProps) {
  const { locale, tx, formatDateTime } = useI18n();
  const initial = snapshot.policy;
  const [enabled, setEnabled] = useState(initial.enabled);
  const [managePriority, setManagePriority] = useState(initial.priority !== null);
  const [priority, setPriority] = useState(String(initial.priority ?? 0));
  const [manageWebsockets, setManageWebsockets] = useState(initial.websockets !== null);
  const [websockets, setWebsockets] = useState(initial.websockets ?? false);
  const [interval, setInterval] = useState(String(initial.scan_interval_seconds));
  const [formError, setFormError] = useState("");
  const lastScan = snapshot.last_scan;
  const hasPersistedFields = initial.priority !== null || initial.websockets !== null;
  const dirty = enabled !== initial.enabled ||
    managePriority !== (initial.priority !== null) ||
    managePriority && priority.trim() !== String(initial.priority ?? 0) ||
    manageWebsockets !== (initial.websockets !== null) ||
    manageWebsockets && websockets !== initial.websockets ||
    interval.trim() !== String(initial.scan_interval_seconds);
  const controlsLocked = saving || forceLoading;
  const policyError = formError || error || operatorMessage(lastScan.error);

  const save = () => {
    setFormError("");
    if (enabled && !managePriority && !manageWebsockets) {
      setFormError(tx("ui.select_at_least_one_default_field_before_enabling_the_policy"));
      return;
    }
    if (managePriority && !/^-?\d+$/.test(priority.trim())) {
      setFormError(tx("ui.priority_must_be_an_integer"));
      return;
    }
    if (!/^\d+$/.test(interval.trim())) {
      setFormError(tx("ui.scan_interval_must_be_an_integer"));
      return;
    }
    const scanInterval = Number(interval);
    if (scanInterval < 5 || scanInterval > 300) {
      setFormError(tx("ui.scan_interval_must_be_between_5_and_300_seconds"));
      return;
    }
    onSave({
      enabled,
      apply_mode: "missing",
      scan_interval_seconds: scanInterval,
      priority: managePriority ? Number(priority) : null,
      websockets: manageWebsockets ? websockets : null,
    });
  };

  return (
    <Modal
      title={tx("ui.default_policy")}
      wide
      onClose={onClose}
      footer={(
        <>
          <span className="modal-scope">{tx("ui.mode_fill_missing_fields")}</span>
          <button className="button button-warning" type="button" disabled={forceLoading || saving || snapshot.running || dirty || !hasPersistedFields} onClick={onForcePreview}>
            {forceLoading ? <LoaderCircle className="spin" size={15} /> : <ShieldAlert size={15} />}{tx("ui.force_sync")}
          </button>
          <button className="button" type="button" disabled={scanning || saving || snapshot.running || dirty || !initial.enabled || !hasPersistedFields} onClick={onScan}>
            {scanning || snapshot.running ? <LoaderCircle className="spin" size={15} /> : <ScanLine size={15} />}{tx("ui.scan_now")}
          </button>
          <button className="button button-primary" type="button" disabled={saving} onClick={save}>
            {saving ? <LoaderCircle className="spin" size={15} /> : <Save size={15} />}{tx("ui.save_policy")}
          </button>
        </>
      )}
    >
      <div className="policy-status-line">
        <span className={`policy-state ${initial.enabled ? "is-enabled" : ""}`}><span />{tx(initial.enabled ? "ui.auto_apply" : "ui.stopped")}</span>
        <span><RefreshCw className={snapshot.running ? "spin" : ""} size={14} />{snapshot.running ? tx("ui.scanning") : tx("ui.last_scan_time", { time: formatDateTime(lastScan.finished_at) })}</span>
      </div>

      <div className="policy-metrics" aria-label={tx("ui.latest_scan_metrics")}>
        <PolicyMetric label={tx("ui.scanned")} value={lastScan.scanned} />
        <PolicyMetric label={tx("ui.updated_2")} value={lastScan.changed} tone="success" />
        <PolicyMetric label={tx("ui.skipped")} value={lastScan.skipped} />
        <PolicyMetric label={tx("ui.failed")} value={lastScan.failed} tone={lastScan.failed ? "danger" : ""} />
      </div>

      <div className="policy-form">
        <label className={`policy-row policy-master ${enabled ? "is-enabled" : ""}`}>
          <span><strong>{tx("ui.auto_apply")}</strong><small>{tx("ui.auth_files")}</small></span>
          <span className="switch-control"><input type="checkbox" checked={enabled} disabled={controlsLocked} onChange={(event) => setEnabled(event.target.checked)} aria-label={tx("ui.enable_default_policy")} /><b>{tx(enabled ? "ui.on_2" : "ui.off_2")}</b></span>
        </label>

        <div className={`policy-row ${managePriority ? "is-enabled" : ""}`}>
          <label className="edit-optin"><input type="checkbox" checked={managePriority} disabled={controlsLocked} onChange={(event) => setManagePriority(event.target.checked)} />Priority</label>
          <input type="number" step="1" value={priority} onChange={(event) => setPriority(event.target.value)} disabled={!managePriority || controlsLocked} aria-label={tx("ui.default_priority")} />
        </div>

        <div className={`policy-row ${manageWebsockets ? "is-enabled" : ""}`}>
          <label className="edit-optin"><input type="checkbox" checked={manageWebsockets} disabled={controlsLocked} onChange={(event) => setManageWebsockets(event.target.checked)} />WebSockets</label>
          <label className="switch-control"><input type="checkbox" checked={websockets} onChange={(event) => setWebsockets(event.target.checked)} disabled={!manageWebsockets || controlsLocked} aria-label={tx("ui.default_websockets")} /><b>{tx(websockets ? "ui.on_2" : "ui.off_2")}</b></label>
        </div>

        <label className="policy-row policy-interval">
          <span className="edit-optin">{tx("ui.scan_interval")}</span>
          <span className="number-suffix"><input type="number" min="5" max="300" step="1" value={interval} disabled={controlsLocked} onChange={(event) => setInterval(event.target.value)} aria-label={tx("ui.scan_interval")} /><b>{tx("ui.seconds")}</b></span>
        </label>
      </div>

      {policyError ? (
        <div className="policy-error" role="alert"><AlertCircle size={16} /><span>{policyError}</span></div>
      ) : null}
    </Modal>
  );
}

function PolicyMetric({ label, value, tone = "" }: { label: string; value: number; tone?: string }) {
  return <div className={tone}><span>{label}</span><strong>{value}</strong></div>;
}

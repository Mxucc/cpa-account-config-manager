import { AlertCircle, AlertTriangle, CheckCircle2, FileJson2, LoaderCircle, ShieldAlert } from "lucide-react";
import { operatorMessage } from "../format/operatorMessage";
import type { ForceSyncPreview } from "../types";
import { Modal } from "./Modal";
import { useI18n } from "../i18n";

interface ForceSyncPreviewDialogProps {
  preview: ForceSyncPreview;
  starting: boolean;
  error?: string;
  onClose: () => void;
  onConfirm: () => void;
}

export function ForceSyncPreviewDialog({ preview, starting, error = "", onClose, onConfirm }: ForceSyncPreviewDialogProps) {
  const { locale, tx } = useI18n();
  return (
    <Modal
      title={tx("ui.force_sync_preview")}
      wide
      onClose={onClose}
      footer={(
        <>
          <span className="modal-scope">{tx("ui.snapshot_id", { id: preview.id.slice(0, 8) })}</span>
          <button className="button" type="button" onClick={onClose}>{tx("ui.cancel")}</button>
          <button className="button button-danger" type="button" disabled={starting || preview.eligible === 0} onClick={onConfirm}>
            {starting ? <LoaderCircle className="spin" size={15} /> : <ShieldAlert size={15} />}{tx("ui.overwrite_count_files", { count: preview.eligible })}
          </button>
        </>
      )}
    >
      <div className="preview-metrics force-preview-metrics">
        <Metric label={tx("ui.targets")} value={preview.total} />
        <Metric label={tx("ui.eligible")} value={preview.eligible} tone="success" />
        <Metric label={tx("ui.read_only")} value={preview.read_only} tone={preview.read_only ? "warning" : ""} />
        <Metric label={tx("ui.physical_files")} value={preview.physical_files} />
      </div>

      <div className="force-policy-values" aria-label={tx("ui.managed_fields")}>
        {preview.policy.priority !== null ? <span><b>Priority</b><code>{preview.policy.priority}</code></span> : null}
        {preview.policy.websockets !== null ? <span><b>WebSockets</b><code>{preview.policy.websockets ? "ON" : "OFF"}</code></span> : null}
      </div>

      <div className="force-warning"><AlertTriangle size={17} /><strong>{tx("ui.existing_field_values_will_be_overwritten")}</strong><span>{tx("ui.only_the_managed_fields_above")}</span></div>
      {error ? <div className="preview-start-error" role="alert"><AlertCircle size={18} /><div><strong>{tx("ui.job_not_started")}</strong><span>{error}</span></div></div> : null}
      {preview.read_only > 0 ? <div className="warning-list"><div><AlertTriangle size={15} />{tx("ui.count_read_only_targets_will_be_skipped", { count: preview.read_only })}</div></div> : null}

      <div className="preview-targets">
        <div className="preview-target-header"><span>{tx("ui.account_snapshot")}</span><span>{preview.targets.length}</span></div>
        <div className="preview-target-list">
          {preview.targets.slice(0, 12).map((target) => (
            <div className="preview-target" key={target.id}>
              {target.eligible ? <CheckCircle2 className="tone-success" size={16} /> : <FileJson2 className="tone-muted" size={16} />}
              <span className="preview-target-name">{target.label || target.name || target.id}</span>
              <span className="provider-tag">{target.provider || tx("ui.unknown")}</span>
              <span className={target.eligible ? "eligibility success" : "eligibility muted"}>{target.eligible ? tx("ui.will_overwrite") : operatorMessage(target.read_only_reason, locale) || tx("ui.skipped")}</span>
            </div>
          ))}
          {preview.targets.length > 12 ? <div className="preview-more">{tx("ui.count_more_targets", { count: preview.targets.length - 12 })}</div> : null}
        </div>
      </div>
    </Modal>
  );
}

function Metric({ label, value, tone = "" }: { label: string; value: number; tone?: string }) {
  return <div className={`preview-metric ${tone}`}><span>{label}</span><strong>{value}</strong></div>;
}

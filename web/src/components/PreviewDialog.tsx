import { AlertCircle, AlertTriangle, CheckCircle2, FileJson2 } from "lucide-react";
import { operatorMessage } from "../format/operatorMessage";
import type { BatchPreview } from "../types";
import { Modal } from "./Modal";
import { useI18n } from "../i18n";
import type { Locale } from "../i18n";
import { translateUI, type UIMessageKey } from "../i18n/uiText";

interface PreviewDialogProps {
  preview: BatchPreview;
  starting: boolean;
  error?: string;
  onClose: () => void;
  onConfirm: () => void;
}

const fieldLabels: Record<string, UIMessageKey> = {
  disabled: "ui.enabled_state",
  priority: "ui.priority",
  note: "ui.note",
  prefix: "ui.prefix",
  proxy_url: "ui.proxy_url",
  websockets: "ui.websockets",
  headers: "ui.headers",
};

export function PreviewDialog({ preview, starting, error = "", onClose, onConfirm }: PreviewDialogProps) {
  const { locale, tx } = useI18n();
  const warnings = previewWarnings(preview, locale);
  return (
    <Modal
      title={tx("ui.change_preview")}
      wide
      onClose={onClose}
      footer={(
        <>
          <span className="modal-scope">{tx("ui.snapshot_id", { id: preview.id.slice(0, 8) })}</span>
          <button className="button" type="button" onClick={onClose}>{tx("ui.cancel")}</button>
          <button className="button button-primary" type="button" disabled={starting || preview.eligible === 0} onClick={onConfirm}>
            {starting ? tx("ui.starting") : error ? tx("ui.restart_count_accounts", { count: preview.eligible }) : tx("ui.apply_to_count_accounts", { count: preview.eligible })}
          </button>
        </>
      )}
    >
      <div className="preview-metrics">
        <Metric label={tx("ui.targets")} value={preview.total} />
        <Metric label={tx("ui.eligible")} value={preview.eligible} tone="success" />
        <Metric label={tx("ui.read_only")} value={preview.read_only} tone={preview.read_only > 0 ? "warning" : ""} />
        <Metric label={tx("ui.missing")} value={preview.missing} tone={preview.missing > 0 ? "danger" : ""} />
        <Metric label={tx("ui.physical_files")} value={preview.physical_files} />
      </div>
      <div className="preview-fields" aria-label={tx("ui.changed_fields")}>
        {preview.patch.fields.map((field) => <span className="field-chip" key={field}>{tx(fieldLabels[field] ?? "ui.unknown")}</span>)}
      </div>
      {error ? (
        <div className="preview-start-error" role="alert">
          <AlertCircle size={18} />
          <div><strong>{tx("ui.job_not_started")}</strong><span>{error}</span></div>
        </div>
      ) : null}
      {warnings.length ? (
        <div className="warning-list">
          {warnings.map((warning) => <div key={warning}><AlertTriangle size={15} />{warning}</div>)}
        </div>
      ) : null}
      <div className="preview-targets">
        <div className="preview-target-header">
          <span>{tx("ui.account_snapshot")}</span>
          <span>{preview.targets.length}</span>
        </div>
        <div className="preview-target-list">
          {preview.targets.slice(0, 12).map((target) => (
            <div className="preview-target" key={target.id}>
              {target.eligible ? <CheckCircle2 className="tone-success" size={16} /> : <FileJson2 className="tone-muted" size={16} />}
              <span className="preview-target-name">{target.label || target.name || target.id}</span>
              <span className="provider-tag">{target.provider || tx("ui.unknown")}</span>
              <span className={target.eligible ? "eligibility success" : "eligibility muted"}>{target.eligible ? tx("ui.eligible") : operatorMessage(target.read_only_reason, locale) || tx("ui.skipped")}</span>
            </div>
          ))}
          {preview.targets.length > 12 ? <div className="preview-more">{tx("ui.count_more_targets", { count: preview.targets.length - 12 })}</div> : null}
        </div>
      </div>
    </Modal>
  );
}

function previewWarnings(preview: BatchPreview, locale: Locale): string[] {
  const warnings: string[] = [];
  if (preview.read_only > 0) warnings.push(translateUI(locale, "ui.count_targets_are_read_only_and_will_be_skipped", { count: preview.read_only }));
  if (preview.missing > 0) warnings.push(translateUI(locale, "ui.count_selected_targets_no_longer_exist_and_will_be_skipped", { count: preview.missing }));
  if (Object.keys(preview.providers).length > 1) warnings.push(translateUI(locale, "ui.targets_include_multiple_providers_confirm_that_the_fields_apply_to_all_accounts"));
  return warnings;
}

function Metric({ label, value, tone = "" }: { label: string; value: number; tone?: string }) {
  return <div className={`preview-metric ${tone}`}><span>{label}</span><strong>{value}</strong></div>;
}

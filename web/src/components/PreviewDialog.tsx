import { AlertCircle, AlertTriangle, CheckCircle2, FileJson2 } from "lucide-react";
import { operatorMessage } from "../format/operatorMessage";
import type { BatchPreview } from "../types";
import { Modal } from "./Modal";

interface PreviewDialogProps {
  preview: BatchPreview;
  starting: boolean;
  error?: string;
  onClose: () => void;
  onConfirm: () => void;
}

const fieldLabels: Record<string, string> = {
  disabled: "启用状态",
  priority: "Priority",
  note: "Note",
  prefix: "Prefix",
  proxy_url: "Proxy URL",
  websockets: "WebSockets",
  headers: "Headers",
};

export function PreviewDialog({ preview, starting, error = "", onClose, onConfirm }: PreviewDialogProps) {
  const warnings = previewWarnings(preview);
  return (
    <Modal
      title="变更预览"
      wide
      onClose={onClose}
      footer={(
        <>
          <span className="modal-scope">快照 {preview.id.slice(0, 8)}</span>
          <button className="button" type="button" onClick={onClose}>取消</button>
          <button className="button button-primary" type="button" disabled={starting || preview.eligible === 0} onClick={onConfirm}>
            {starting ? "正在启动" : error ? `重新启动 ${preview.eligible} 个账号` : `执行 ${preview.eligible} 个账号`}
          </button>
        </>
      )}
    >
      <div className="preview-metrics">
        <Metric label="目标" value={preview.total} />
        <Metric label="可执行" value={preview.eligible} tone="success" />
        <Metric label="只读" value={preview.read_only} tone={preview.read_only > 0 ? "warning" : ""} />
        <Metric label="缺失" value={preview.missing} tone={preview.missing > 0 ? "danger" : ""} />
        <Metric label="物理文件" value={preview.physical_files} />
      </div>
      <div className="preview-fields" aria-label="变更字段">
        {preview.patch.fields.map((field) => <span className="field-chip" key={field}>{fieldLabels[field] ?? field}</span>)}
      </div>
      {error ? (
        <div className="preview-start-error" role="alert">
          <AlertCircle size={18} />
          <div><strong>任务未启动</strong><span>{error}</span></div>
        </div>
      ) : null}
      {warnings.length ? (
        <div className="warning-list">
          {warnings.map((warning) => <div key={warning}><AlertTriangle size={15} />{warning}</div>)}
        </div>
      ) : null}
      <div className="preview-targets">
        <div className="preview-target-header">
          <span>账号快照</span>
          <span>{preview.targets.length}</span>
        </div>
        <div className="preview-target-list">
          {preview.targets.slice(0, 12).map((target) => (
            <div className="preview-target" key={target.id}>
              {target.eligible ? <CheckCircle2 className="tone-success" size={16} /> : <FileJson2 className="tone-muted" size={16} />}
              <span className="preview-target-name">{target.label || target.name || target.id}</span>
              <span className="provider-tag">{target.provider || "unknown"}</span>
              <span className={target.eligible ? "eligibility success" : "eligibility muted"}>{target.eligible ? "可执行" : operatorMessage(target.read_only_reason) || "跳过"}</span>
            </div>
          ))}
          {preview.targets.length > 12 ? <div className="preview-more">另有 {preview.targets.length - 12} 个目标</div> : null}
        </div>
      </div>
    </Modal>
  );
}

function previewWarnings(preview: BatchPreview): string[] {
  const warnings: string[] = [];
  if (preview.read_only > 0) warnings.push(`${preview.read_only} 个目标为只读，将自动跳过`);
  if (preview.missing > 0) warnings.push(`${preview.missing} 个已选目标已不存在，将自动跳过`);
  if (Object.keys(preview.providers).length > 1) warnings.push("目标包含多个 Provider，请确认字段适用于全部账号");
  return warnings;
}

function Metric({ label, value, tone = "" }: { label: string; value: number; tone?: string }) {
  return <div className={`preview-metric ${tone}`}><span>{label}</span><strong>{value}</strong></div>;
}

import { AlertCircle, AlertTriangle, CheckCircle2, FileJson2, LoaderCircle, ShieldAlert } from "lucide-react";
import { operatorMessage } from "../format/operatorMessage";
import type { ForceSyncPreview } from "../types";
import { Modal } from "./Modal";

interface ForceSyncPreviewDialogProps {
  preview: ForceSyncPreview;
  starting: boolean;
  error?: string;
  onClose: () => void;
  onConfirm: () => void;
}

export function ForceSyncPreviewDialog({ preview, starting, error = "", onClose, onConfirm }: ForceSyncPreviewDialogProps) {
  return (
    <Modal
      title="强制同步预览"
      wide
      onClose={onClose}
      footer={(
        <>
          <span className="modal-scope">快照 {preview.id.slice(0, 8)}</span>
          <button className="button" type="button" onClick={onClose}>取消</button>
          <button className="button button-danger" type="button" disabled={starting || preview.eligible === 0} onClick={onConfirm}>
            {starting ? <LoaderCircle className="spin" size={15} /> : <ShieldAlert size={15} />}覆盖 {preview.eligible} 个文件
          </button>
        </>
      )}
    >
      <div className="preview-metrics force-preview-metrics">
        <Metric label="目标" value={preview.total} />
        <Metric label="可执行" value={preview.eligible} tone="success" />
        <Metric label="只读" value={preview.read_only} tone={preview.read_only ? "warning" : ""} />
        <Metric label="物理文件" value={preview.physical_files} />
      </div>

      <div className="force-policy-values" aria-label="覆盖字段">
        {preview.policy.priority !== null ? <span><b>Priority</b><code>{preview.policy.priority}</code></span> : null}
        {preview.policy.websockets !== null ? <span><b>WebSockets</b><code>{preview.policy.websockets ? "ON" : "OFF"}</code></span> : null}
      </div>

      <div className="force-warning"><AlertTriangle size={17} /><strong>将覆盖现有字段值</strong><span>仅限上方受管字段</span></div>
      {error ? <div className="preview-start-error" role="alert"><AlertCircle size={18} /><div><strong>任务未启动</strong><span>{error}</span></div></div> : null}
      {preview.read_only > 0 ? <div className="warning-list"><div><AlertTriangle size={15} />{preview.read_only} 个只读目标将跳过</div></div> : null}

      <div className="preview-targets">
        <div className="preview-target-header"><span>账号快照</span><span>{preview.targets.length}</span></div>
        <div className="preview-target-list">
          {preview.targets.slice(0, 12).map((target) => (
            <div className="preview-target" key={target.id}>
              {target.eligible ? <CheckCircle2 className="tone-success" size={16} /> : <FileJson2 className="tone-muted" size={16} />}
              <span className="preview-target-name">{target.label || target.name || target.id}</span>
              <span className="provider-tag">{target.provider || "unknown"}</span>
              <span className={target.eligible ? "eligibility success" : "eligibility muted"}>{target.eligible ? "将覆盖" : operatorMessage(target.read_only_reason) || "跳过"}</span>
            </div>
          ))}
          {preview.targets.length > 12 ? <div className="preview-more">另有 {preview.targets.length - 12} 个目标</div> : null}
        </div>
      </div>
    </Modal>
  );
}

function Metric({ label, value, tone = "" }: { label: string; value: number; tone?: string }) {
  return <div className={`preview-metric ${tone}`}><span>{label}</span><strong>{value}</strong></div>;
}

import { AlertCircle, LoaderCircle, RefreshCw, Save, ScanLine, ShieldAlert } from "lucide-react";
import { useState } from "react";
import { operatorMessage } from "../format/operatorMessage";
import type { DefaultPolicy, PolicySnapshot } from "../types";
import { Modal } from "./Modal";

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
      setFormError("启用自动策略时至少选择一个默认字段");
      return;
    }
    if (managePriority && !/^-?\d+$/.test(priority.trim())) {
      setFormError("Priority 必须是整数");
      return;
    }
    if (!/^\d+$/.test(interval.trim())) {
      setFormError("扫描间隔必须是整数");
      return;
    }
    const scanInterval = Number(interval);
    if (scanInterval < 5 || scanInterval > 300) {
      setFormError("扫描间隔必须在 5 到 300 秒之间");
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
      title="默认策略"
      wide
      onClose={onClose}
      footer={(
        <>
          <span className="modal-scope">模式：补齐缺失字段</span>
          <button className="button button-warning" type="button" disabled={forceLoading || saving || snapshot.running || dirty || !hasPersistedFields} onClick={onForcePreview}>
            {forceLoading ? <LoaderCircle className="spin" size={15} /> : <ShieldAlert size={15} />}强制同步
          </button>
          <button className="button" type="button" disabled={scanning || saving || snapshot.running || dirty || !initial.enabled || !hasPersistedFields} onClick={onScan}>
            {scanning || snapshot.running ? <LoaderCircle className="spin" size={15} /> : <ScanLine size={15} />}立即扫描
          </button>
          <button className="button button-primary" type="button" disabled={saving} onClick={save}>
            {saving ? <LoaderCircle className="spin" size={15} /> : <Save size={15} />}保存策略
          </button>
        </>
      )}
    >
      <div className="policy-status-line">
        <span className={`policy-state ${initial.enabled ? "is-enabled" : ""}`}><span />{initial.enabled ? "自动应用" : "已停用"}</span>
        <span><RefreshCw className={snapshot.running ? "spin" : ""} size={14} />{snapshot.running ? "扫描中" : `最近扫描 ${formatPolicyTime(lastScan.finished_at)}`}</span>
      </div>

      <div className="policy-metrics" aria-label="最近扫描统计">
        <PolicyMetric label="扫描" value={lastScan.scanned} />
        <PolicyMetric label="更新" value={lastScan.changed} tone="success" />
        <PolicyMetric label="跳过" value={lastScan.skipped} />
        <PolicyMetric label="失败" value={lastScan.failed} tone={lastScan.failed ? "danger" : ""} />
      </div>

      <div className="policy-form">
        <label className={`policy-row policy-master ${enabled ? "is-enabled" : ""}`}>
          <span><strong>自动应用</strong><small>Auth 文件</small></span>
          <span className="switch-control"><input type="checkbox" checked={enabled} disabled={controlsLocked} onChange={(event) => setEnabled(event.target.checked)} aria-label="启用默认策略" /><b>{enabled ? "开启" : "关闭"}</b></span>
        </label>

        <div className={`policy-row ${managePriority ? "is-enabled" : ""}`}>
          <label className="edit-optin"><input type="checkbox" checked={managePriority} disabled={controlsLocked} onChange={(event) => setManagePriority(event.target.checked)} />Priority</label>
          <input type="number" step="1" value={priority} onChange={(event) => setPriority(event.target.value)} disabled={!managePriority || controlsLocked} aria-label="默认 Priority" />
        </div>

        <div className={`policy-row ${manageWebsockets ? "is-enabled" : ""}`}>
          <label className="edit-optin"><input type="checkbox" checked={manageWebsockets} disabled={controlsLocked} onChange={(event) => setManageWebsockets(event.target.checked)} />WebSockets</label>
          <label className="switch-control"><input type="checkbox" checked={websockets} onChange={(event) => setWebsockets(event.target.checked)} disabled={!manageWebsockets || controlsLocked} aria-label="默认 WebSockets" /><b>{websockets ? "开启" : "关闭"}</b></label>
        </div>

        <label className="policy-row policy-interval">
          <span className="edit-optin">扫描间隔</span>
          <span className="number-suffix"><input type="number" min="5" max="300" step="1" value={interval} disabled={controlsLocked} onChange={(event) => setInterval(event.target.value)} aria-label="扫描间隔" /><b>秒</b></span>
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

function formatPolicyTime(value?: string): string {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  return new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }).format(date);
}

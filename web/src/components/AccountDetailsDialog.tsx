import { LockKeyhole, Pencil, Settings2 } from "lucide-react";
import type { Account } from "../types";
import { operatorMessage } from "../format/operatorMessage";
import { Modal } from "./Modal";

interface AccountDetailsDialogProps {
  account: Account;
  onClose: () => void;
  onEdit: () => void;
}

export function AccountDetailsDialog({ account, onClose, onEdit }: AccountDetailsDialogProps) {
  const usage = account.usage;
  const identity = account.label || account.email || account.name || account.id;
  const state = account.disabled ? "disabled" : account.unavailable ? "unavailable" : account.status || "unknown";

  return (
    <Modal
      title="账号详情"
      wide
      onClose={onClose}
      footer={(
        <>
          <span className="modal-scope">{account.name || account.id}</span>
          <button className="button" type="button" onClick={onClose}>关闭</button>
          {account.editable ? <button className="button button-primary" type="button" onClick={onEdit}><Pencil size={15} />编辑账号</button> : null}
        </>
      )}
    >
      <div className="account-details">
        <div className="account-details-heading">
          <div>
            <strong>{identity}</strong>
            <span>{account.email && account.email !== identity ? account.email : account.name}</span>
          </div>
          {account.editable
            ? <span className="access-tag editable"><Settings2 size={13} />可编辑</span>
            : <span className="access-tag readonly" title={operatorMessage(account.read_only_reason)}><LockKeyhole size={13} />只读</span>}
        </div>

        <DetailSection title="身份与来源">
          <DetailItem label="文件名" value={account.name} mono />
          <DetailItem label="账号索引" value={account.id} mono />
          <DetailItem label="Auth ID" value={account.auth_id} mono />
          <DetailItem label="Provider" value={account.provider} />
          <DetailItem label="Type" value={account.type} />
          <DetailItem label="账号类型" value={account.account_type} />
          <DetailItem label="套餐类型" value={account.plan_type} />
          <DetailItem label="来源" value={account.source} />
          <DetailItem label="状态" value={state} />
          <DetailItem label="状态说明" value={operatorMessage(account.status_message)} />
          {!account.editable ? <DetailItem label="只读原因" value={operatorMessage(account.read_only_reason)} wide /> : null}
        </DetailSection>

        <DetailSection title="路由配置">
          <DetailItem label="Priority" value={account.priority} mono />
          <DetailItem label="Prefix" value={account.prefix || "default"} mono />
          <DetailItem label="Proxy" value={account.proxy || (account.proxy_configured ? "已配置（地址已隐藏）" : "未配置")} mono />
          <DetailItem label="WebSockets" value={account.websockets === undefined ? "未设置" : account.websockets ? "开启" : "关闭"} />
          <DetailItem label="Headers" value={`${account.header_count || 0} 个`} />
          <DetailItem label="Note" value={account.note} wide />
          {account.header_names?.length ? (
            <div className="detail-item detail-item-wide">
              <span>Header 名称</span>
              <div className="detail-chips">{account.header_names.map((name) => <code key={name}>{name}</code>)}</div>
            </div>
          ) : null}
        </DetailSection>

        <DetailSection title="用量与活动">
          <DetailItem label="成功请求" value={formatNumber(account.success)} mono />
          <DetailItem label="失败请求" value={formatNumber(account.failed)} mono />
          <DetailItem label="总 Tokens" value={usage ? formatNumber(usage.total_tokens) : "暂无数据"} mono />
          <DetailItem label="Input" value={usage ? formatNumber(usage.input_tokens) : "暂无数据"} mono />
          <DetailItem label="Output" value={usage ? formatNumber(usage.output_tokens) : "暂无数据"} mono />
          <DetailItem label="Reasoning" value={usage ? formatNumber(usage.reasoning_tokens) : "暂无数据"} mono />
          <DetailItem label="Cached" value={usage ? formatNumber(usage.cached_tokens + usage.cache_read_tokens) : "暂无数据"} mono />
          <DetailItem label="最后请求" value={formatTimestamp(usage?.last_request_at)} />
          <DetailItem label="5 小时用量" value={usage?.codex?.five_hour ? `${formatPercent(usage.codex.five_hour.used_percent)} · ${formatTimestamp(usage.codex.five_hour.reset_at)}` : "暂无数据"} />
          <DetailItem label="7 天用量" value={usage?.codex?.seven_day ? `${formatPercent(usage.codex.seven_day.used_percent)} · ${formatTimestamp(usage.codex.seven_day.reset_at)}` : "暂无数据"} />
        </DetailSection>

        <DetailSection title="时间">
          <DetailItem label="更新时间" value={formatTimestamp(account.updated_at)} />
          <DetailItem label="最后刷新" value={formatTimestamp(account.last_refresh)} />
          <DetailItem label="下次重试" value={formatTimestamp(account.next_retry_after)} />
        </DetailSection>
      </div>
    </Modal>
  );
}

function DetailSection({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="detail-section">
      <h3>{title}</h3>
      <div className="detail-grid">{children}</div>
    </section>
  );
}

function DetailItem({ label, value, mono = false, wide = false }: { label: string; value: string | number | undefined; mono?: boolean; wide?: boolean }) {
  const shown = value === undefined || value === "" ? "-" : String(value);
  return (
    <div className={`detail-item ${wide ? "detail-item-wide" : ""}`}>
      <span>{label}</span>
      {mono ? <code title={shown}>{shown}</code> : <strong title={shown}>{shown}</strong>}
    </div>
  );
}

function formatNumber(value: number): string {
  return new Intl.NumberFormat("zh-CN", { notation: value >= 100000 ? "compact" : "standard", maximumFractionDigits: 1 }).format(value);
}

function formatPercent(value: number): string {
  const normalized = value <= 1 ? value * 100 : value;
  return `${Math.max(0, Math.min(100, normalized)).toFixed(1).replace(/\.0$/, "")}%`;
}

function formatTimestamp(value?: string): string {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(date);
}

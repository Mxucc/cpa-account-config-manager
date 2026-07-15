import { Activity } from "lucide-react";
import type { Account, UsageWindowSnapshot } from "../types";

export function AccountUsageCell({ account }: { account: Account }) {
  const usage = account.usage;
  const recentTotal = (account.recent_requests ?? []).reduce(
    (total, bucket) => total + safeCount(bucket.success) + safeCount(bucket.failed),
    0,
  );
  const tokenValue = usage ? formatCompactNumber(usage.total_tokens) : "--";
  const tokenTitle = usage
    ? `累计 Token：${formatExactNumber(usage.total_tokens)}`
    : "尚未收到 CPA Usage 数据";
  const requestTitle = `累计请求：成功 ${formatExactNumber(account.success)}，失败 ${formatExactNumber(account.failed)}`;
  const recentTitle = account.recent_requests?.length
    ? `CPA 近期请求：${formatExactNumber(recentTotal)}（${account.recent_requests.length} 个时段）${usage?.last_request_at ? `；最近请求 ${formatDateTime(usage.last_request_at)}` : ""}`
    : usage?.last_request_at
      ? `最近请求 ${formatDateTime(usage.last_request_at)}`
      : "CPA 暂无近期请求时段";
  const codex = usage?.codex;
  const hasQuota = Boolean(codex?.five_hour || codex?.seven_day);

  return (
    <div className="account-usage-cell">
      <div className="usage-overview">
        <span className="usage-token-total" title={tokenTitle}>
          <strong>{tokenValue}</strong><small>tok</small>
        </span>
        <span className="usage-request-total" title={requestTitle}>
          <b className="success">{formatCompactNumber(account.success)}</b>
          <i>/</i>
          <b className="danger">{formatCompactNumber(account.failed)}</b>
        </span>
        <span className="usage-recent-total" title={recentTitle}>
          <Activity size={11} aria-hidden="true" />
          <b>{account.recent_requests?.length ? formatCompactNumber(recentTotal) : "--"}</b>
        </span>
      </div>
      {hasQuota ? (
        <div className="usage-quota-list">
          {codex?.five_hour ? <UsageQuota label="5h" window={codex.five_hour} /> : null}
          {codex?.seven_day ? <UsageQuota label="7d" window={codex.seven_day} /> : null}
        </div>
      ) : (
        <div className="usage-quota-empty" title="Codex 配额会在 CPA 捕获到对应上游响应头后显示">
          <span>配额</span><b>--</b>
        </div>
      )}
    </div>
  );
}

function UsageQuota({ label, window }: { label: "5h" | "7d"; window: UsageWindowSnapshot }) {
  const percent = safePercent(window.used_percent);
  const percentLabel = formatPercent(percent);
  const width = Math.min(100, percent);
  const tone = percent >= 90 ? "danger" : percent >= 70 ? "warning" : "normal";
  const reset = window.reset_at ? formatDateTime(window.reset_at) : "未知";
  const title = `${label} 已用 ${percentLabel}%，重置 ${reset}${window.window_minutes ? `，窗口 ${window.window_minutes} 分钟` : ""}`;

  return (
    <div className={`usage-quota-row quota-${tone}`} title={title}>
      <span>{label}</span>
      <span
        className="usage-quota-track"
        role="meter"
        aria-label={`${label} 用量 ${percentLabel}%`}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={width}
      >
        <span style={{ width: `${width}%` }} />
      </span>
      <b>{percentLabel}%</b>
    </div>
  );
}

function safeCount(value: number): number {
  return Number.isFinite(value) ? Math.max(0, value) : 0;
}

function safePercent(value: number): number {
  return Number.isFinite(value) ? Math.max(0, value) : 0;
}

function formatCompactNumber(value: number): string {
  const normalized = safeCount(value);
  const units: Array<[number, string]> = [[1_000_000_000_000, "T"], [1_000_000_000, "B"], [1_000_000, "M"], [1_000, "K"]];
  for (const [threshold, suffix] of units) {
    if (normalized < threshold) continue;
    return `${(normalized / threshold).toFixed(1).replace(/\.0$/, "")}${suffix}`;
  }
  return Math.round(normalized).toLocaleString("en-US");
}

function formatExactNumber(value: number): string {
  return Math.round(safeCount(value)).toLocaleString("en-US");
}

function formatPercent(value: number): string {
  const rounded = Math.round(value * 10) / 10;
  return Number.isInteger(rounded) ? rounded.toFixed(0) : rounded.toFixed(1);
}

function formatDateTime(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "未知";
  return new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(date);
}

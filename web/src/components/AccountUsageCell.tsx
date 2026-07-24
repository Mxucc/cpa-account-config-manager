import { Activity, AlertTriangle } from "lucide-react";
import type { Account, UsageWindowSnapshot } from "../types";
import { localeFormats, useI18n, type Locale } from "../i18n";

export function AccountUsageCell({ account }: { account: Account }) {
  const { locale, tx, formatDateTime, formatNumber } = useI18n();
  const usage = account.usage;
  const agentIdentity = String(account.provider || account.type).trim().toLowerCase() === "codex-agent-identity";
  const recentTotal = (account.recent_requests ?? []).reduce(
    (total, bucket) => total + safeCount(bucket.success) + safeCount(bucket.failed),
    0,
  );
  const tokenValue = usage ? formatCompactNumber(usage.total_tokens, locale) : agentIdentity ? tx("ui.unknown") : formatCompactNumber(0, locale);
  const tokenTitle = usage
    ? tx("ui.total_tokens_count", { count: formatNumber(usage.total_tokens) })
    : tx("ui.no_cpa_usage_data_received");
  const requestTitle = tx("ui.total_requests_success_succeeded_failed_failed", { success: formatNumber(account.success), failed: formatNumber(account.failed) });
  const recentTitle = account.recent_requests?.length
    ? usage?.last_request_at
      ? tx("ui.recent_cpa_requests_count_across_windows_windows_last_request_time", { count: formatNumber(recentTotal), windows: account.recent_requests.length, time: formatDateTime(usage.last_request_at) })
      : tx("ui.recent_cpa_requests_count_across_windows_windows", { count: formatNumber(recentTotal), windows: account.recent_requests.length })
    : usage?.last_request_at
      ? tx("ui.last_request_time", { time: formatDateTime(usage.last_request_at) })
      : tx("ui.no_recent_cpa_request_windows");
  const codex = usage?.codex;
  const hasQuota = Boolean(codex?.five_hour || codex?.seven_day);
  const fiveHourExhausted = safePercent(codex?.five_hour?.used_percent ?? 0) >= 100;
  const longWindowExhausted = safePercent(codex?.seven_day?.used_percent ?? 0) >= 100;
  const quotaExhausted = fiveHourExhausted || longWindowExhausted;
  const quotaPlaceholderTitle = agentIdentity
    ? tx("ui.cpa_does_not_currently_provide_agent_identity_quota")
    : String(account.provider || account.type).toLowerCase() === "codex"
      ? tx("ui.codex_quota_appears_after_cpa_captures_the_relevant_upstream_response_headers")
      : tx("ui.no_cpa_usage_data_received");
  const exhaustedAction = quotaExhausted && !account.disabled
    ? tx("ui.suggested_disable")
    : tx("ui.waiting_for_quota_recovery");

  return (
    <div className="account-usage-cell">
      <div className="usage-overview">
        <span className="usage-token-total" title={tokenTitle}>
          <strong>{tokenValue}</strong><small>tok</small>
        </span>
        <span className="usage-request-total" title={requestTitle}>
          <b className="success">{formatCompactNumber(account.success, locale)}</b>
          <i>/</i>
          <b className="danger">{formatCompactNumber(account.failed, locale)}</b>
        </span>
        <span className="usage-recent-total" title={recentTitle}>
          <Activity size={11} aria-hidden="true" />
          <b>{account.recent_requests?.length ? formatCompactNumber(recentTotal, locale) : "0"}</b>
        </span>
      </div>
      {hasQuota ? (
        <div className="usage-quota-list">
          {codex?.five_hour ? <UsageQuota label="5h" window={codex.five_hour} /> : null}
          {codex?.seven_day ? <UsageQuota label="7d" window={codex.seven_day} /> : null}
          {quotaExhausted ? <div className="usage-quota-alert" role="status"><AlertTriangle size={10} /><span>{tx("ui.quota_exhausted")}</span><b>{exhaustedAction}</b></div> : null}
        </div>
      ) : (
        <div className="usage-quota-empty" title={quotaPlaceholderTitle}>
          <Activity size={10} aria-hidden="true" /><b>{agentIdentity ? tx("ui.cpa_does_not_currently_provide_agent_identity_quota") : tx("ui.awaiting_usage_collection")}</b>
        </div>
      )}
    </div>
  );
}

function UsageQuota({ label, window }: { label: "5h" | "7d"; window: UsageWindowSnapshot }) {
  const { tx, formatDateTime } = useI18n();
  const percent = safePercent(window.used_percent);
  const percentLabel = formatPercent(percent);
  const width = Math.min(100, percent);
  const tone = percent >= 90 ? "danger" : percent >= 70 ? "warning" : "normal";
  const reset = window.reset_at ? formatDateTime(window.reset_at) : tx("ui.unknown");
  const title = window.window_minutes
    ? tx("ui.label_percent_percent_used_resets_reset_minutes_minute_window", { label, percent: percentLabel, reset, minutes: window.window_minutes })
    : tx("ui.label_percent_percent_used_resets_reset", { label, percent: percentLabel, reset });

  return (
    <div className={`usage-quota-row quota-${tone}`} title={title}>
      <span>{label}</span>
      <span
        className="usage-quota-track"
        role="meter"
        aria-label={tx("ui.label_usage_percent_percent", { label, percent: percentLabel })}
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

function formatCompactNumber(value: number, locale: Locale): string {
  const normalized = safeCount(value);
  return new Intl.NumberFormat(localeFormats[locale].dateTimeLocale, {
    notation: normalized >= 1000 ? "compact" : "standard",
    maximumFractionDigits: 1,
  }).format(normalized);
}

function formatPercent(value: number): string {
  const rounded = Math.round(value * 10) / 10;
  return Number.isInteger(rounded) ? rounded.toFixed(0) : rounded.toFixed(1);
}

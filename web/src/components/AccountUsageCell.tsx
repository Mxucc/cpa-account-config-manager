import { Activity, AlertTriangle, Gauge } from "lucide-react";
import type { Account, UsageWindowSnapshot } from "../types";
import { localeFormats, useI18n, type Locale } from "../i18n";
import type { UIMessageKey } from "../i18n/uiText";

export function AccountUsageCell({ account, weeklyOverdraftEnabled = false, creditUsageEnabled = false }: { account: Account; weeklyOverdraftEnabled?: boolean; creditUsageEnabled?: boolean }) {
  const { locale, t, tx, formatDateTime, formatNumber } = useI18n();
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
  const credit = usage?.credit;
  const hasCreditSamples = Boolean(credit && (credit.rated_requests > 0 || credit.unrated_requests > 0));
  const primaryUsageValue = creditUsageEnabled && hasCreditSamples
    ? formatUSD(credit?.amount_usd ?? 0, locale)
    : creditUsageEnabled
      ? tx("ui.awaiting_credit_usage_collection")
      : tokenValue;
  const primaryUsageUnit = creditUsageEnabled && hasCreditSamples ? "USD" : creditUsageEnabled ? "" : "tok";
  const primaryUsageTitle = creditUsageEnabled
    ? hasCreditSamples
      ? tx("ui.estimated_credit_usage_detail", {
          amount: formatUSD(credit?.amount_usd ?? 0, locale),
          rated: formatNumber(credit?.rated_requests ?? 0),
          unrated: formatNumber(credit?.unrated_requests ?? 0),
          tokens: formatNumber(usage?.total_tokens ?? 0),
        })
      : tx("ui.awaiting_credit_usage_collection")
    : tokenTitle;
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
  const overdraftWindows = weeklyOverdraftEnabled ? [
    codex?.five_hour?.overdraft_active
      ? { label: "5h" as const, window: codex.five_hour }
      : null,
    codex?.seven_day?.overdraft_active
      ? { label: "7d" as const, window: codex.seven_day }
      : null,
  ].filter((window): window is { label: "5h" | "7d"; window: UsageWindowSnapshot } => window !== null) : [];
  const quotaPlaceholderTitle = agentIdentity
    ? tx("ui.cpa_does_not_currently_provide_agent_identity_quota")
    : String(account.provider || account.type).toLowerCase() === "codex"
      ? tx("ui.codex_quota_appears_after_cpa_captures_the_relevant_upstream_response_headers")
      : tx("ui.no_cpa_usage_data_received");
  let exhaustedAction = tx("ui.suggested_disable");
  const gateStatus = account.automation?.auto_disable_probe_status;
  if (account.disabled) {
    exhaustedAction = tx("ui.waiting_for_quota_recovery");
  } else if (account.automation?.auto_action === "disable" && account.automation.auto_action_status === "failed") {
    exhaustedAction = t("automation.disable_failed");
  } else if (quotaExhausted && gateStatus === "passed") {
    exhaustedAction = tx("ui.weekly_overdraft_probe_passed");
  } else if (quotaExhausted && gateStatus === "pending") {
    exhaustedAction = tx("ui.weekly_overdraft_probe_running", {
      current: account.automation?.auto_disable_probe_attempts ?? 0,
      total: account.automation?.auto_disable_probe_limit ?? 5,
    });
  } else if (quotaExhausted && gateStatus === "inconclusive") {
    exhaustedAction = `${tx("ui.weekly_overdraft_probe_inconclusive")} · ${probeReasonLabel(account.automation?.auto_disable_probe_reason_code, tx)}`;
  } else if (quotaExhausted && gateStatus === "failed") {
    exhaustedAction = tx("ui.weekly_overdraft_probe_failed");
  } else if (account.automation?.auto_disable_enabled) {
    exhaustedAction = t("automation.waiting_disable");
  } else if (account.automation) {
    exhaustedAction = t("automation.disable_off");
  }

  return (
    <div className="account-usage-cell">
      <div className="usage-overview">
        <span className="usage-token-total" title={primaryUsageTitle}>
          <strong>{primaryUsageValue}</strong>{primaryUsageUnit ? <small>{primaryUsageUnit}</small> : null}
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
      {creditUsageEnabled && (credit?.unrated_requests ?? 0) > 0 ? (
        <div className="usage-quota-empty" title={tx("ui.some_requests_could_not_be_priced", { count: formatNumber(credit?.unrated_requests ?? 0) })}>
          <AlertTriangle size={10} aria-hidden="true" /><b>{tx("ui.unrated_requests_count", { count: formatNumber(credit?.unrated_requests ?? 0) })}</b>
        </div>
      ) : null}
      {hasQuota ? (
        <div className="usage-quota-list">
          {codex?.five_hour ? <UsageQuota label="5h" window={codex.five_hour} /> : null}
          {codex?.seven_day ? <UsageQuota label="7d" window={codex.seven_day} /> : null}
          {overdraftWindows.length > 0 ? (
            <div
              className="usage-overdraft-panel"
              role="group"
              aria-label={tx("ui.overdraft_usage")}
              title={tx("ui.overdraft_usage_included_in_total")}
            >
              <span className="usage-overdraft-title"><Gauge size={10} aria-hidden="true" />{tx("ui.overdraft_usage")}</span>
              <span className="usage-overdraft-values">
                {overdraftWindows.map((window) => {
                  const total = safePercent(window.window.used_percent);
                  const officialOverdraft = Math.max(0, total - 100);
                  const measuredTokens = safeCount(window.window.overdraft_tokens ?? 0);
                  const measuredRequests = safeCount(window.window.overdraft_requests ?? 0);
                  const totalLabel = formatPercent(total);
                  const content = officialOverdraft > 0
                    ? `${formatPercent(officialOverdraft)}%`
                    : measuredTokens > 0
                      ? tx("ui.overdraft_tokens_value", { count: formatCompactNumber(measuredTokens, locale) })
                      : measuredRequests > 0
                        ? tx("ui.overdraft_requests_value", { count: formatCompactNumber(measuredRequests, locale) })
                        : tx("ui.overdraft_tokens_value", { count: "0" });
                  const title = officialOverdraft > 0
                    ? tx("ui.overdraft_usage_window", { label: window.label, percent: formatPercent(officialOverdraft), total: totalLabel })
                    : tx("ui.overdraft_usage_window_observed", {
                        label: window.label,
                        tokens: formatNumber(measuredTokens),
                        requests: formatNumber(measuredRequests),
                        total: totalLabel,
                      });
                  return (
                    <span key={window.label} title={title}>
                      <small>{window.label}</small><b>+{content}</b>
                    </span>
                  );
                })}
              </span>
            </div>
          ) : null}
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

function formatUSD(value: number, locale: Locale): string {
  const normalized = Number.isFinite(value) ? Math.max(0, value) : 0;
  const maximumFractionDigits = normalized > 0 && normalized < 0.01 ? 6 : 4;
  return new Intl.NumberFormat(localeFormats[locale].dateTimeLocale, {
    style: "currency",
    currency: "USD",
    currencyDisplay: "narrowSymbol",
    minimumFractionDigits: 0,
    maximumFractionDigits,
  }).format(normalized);
}

function probeReasonLabel(reasonCode: string | undefined, tx: (key: UIMessageKey) => string): string {
  switch (reasonCode) {
    case "management_auth_unavailable":
      return tx("ui.weekly_overdraft_probe_management_auth_unavailable");
    case "experimental_probe_unavailable":
      return tx("ui.weekly_overdraft_probe_experiment_unavailable");
    case "request_timeout":
      return tx("ui.weekly_overdraft_probe_timeout");
    default:
      return tx("ui.weekly_overdraft_probe_upstream_unavailable");
  }
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

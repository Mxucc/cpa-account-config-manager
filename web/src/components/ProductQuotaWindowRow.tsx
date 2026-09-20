import { useI18n } from "../i18n";
import { formatReferenceUSD } from "../format/currency";
import type { ProductQuotaWindow } from "../format/productUsage";
import { formatResetDuration, quotaPercent } from "../format/quotaWindow";

/**
 * One aggregated quota window of a product overview: the tightest credential that reported it, its
 * share of that window and when the window resets. A product-wide aggregate must not read like a
 * single credential's window, so the number of credentials behind it travels in the row's title.
 * The reference-priced usage the runtime attributes to the same window is printed next to the
 * reset, and a window that only knows its reset time (an upstream snapshot instead of the runtime
 * tracker) prints that time rather than dropping the countdown.
 */
export function ProductQuotaWindowRow({ label, window, amountUSD }: {
  label: string;
  window: ProductQuotaWindow | undefined;
  /** Reference-priced usage of the same window; omitted when nothing is attributed to it. */
  amountUSD?: number;
}) {
  const { tx, formatDateTime, formatNumber } = useI18n();
  if (!window) return null;
  const percent = quotaPercent(window.usagePercent);
  const reset = window.resetInSeconds
    ? tx("ui.resets_in", { duration: formatResetDuration(window.resetInSeconds, tx) })
    : window.resetAt
      ? tx("ui.resets_at", { time: formatDateTime(window.resetAt) })
      : "";
  const detail = [
    reset,
    typeof amountUSD === "number" && amountUSD > 0
      ? tx("ui.reference_price_amount", { amount: formatReferenceUSD(amountUSD, formatNumber) })
      : "",
  ].filter(Boolean).join(" · ");
  return (
    <div className="quota-window-row" title={tx("ui.overview_quota_window_note", { count: formatNumber(window.credentials) })}>
      <div className={`usage-quota-row${percent >= 90 ? " quota-danger" : percent >= 75 ? " quota-warning" : ""}`}>
        <span>{label}</span>
        <span className="usage-quota-track" role="img" aria-label={`${label} ${percent.toFixed(0)}%`}>
          <span style={{ width: `${percent}%` }} />
        </span>
        <b>{percent.toFixed(1)}%</b>
      </div>
      {detail ? <small>{detail}</small> : null}
    </div>
  );
}

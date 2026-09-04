import type { AccountConcurrencySummary } from "./types";

export function accountConcurrencyRequestLimit(summary: AccountConcurrencySummary): number {
  return summary.request_limit ?? summary.limit_15s ?? 0;
}

export function accountConcurrencyUsedRequests(summary: AccountConcurrencySummary): number {
  return summary.used_requests ?? summary.used_15s ?? summary.used_60s ?? 0;
}

export function accountConcurrencyWindowSeconds(summary: AccountConcurrencySummary): number {
  const value = summary.request_window_seconds;
  return typeof value === "number" && Number.isFinite(value) && value > 0 ? Math.floor(value) : 15;
}

export function accountConcurrencyLimitLabel(summary: AccountConcurrencySummary): string {
  return summary.limit > 0 ? String(summary.limit) : "∞";
}

export function accountConcurrencyRequestLimitLabel(summary: AccountConcurrencySummary): string {
  const limit = accountConcurrencyRequestLimit(summary);
  return limit > 0 ? String(limit) : "∞";
}

export function accountConcurrencySaturated(summary: AccountConcurrencySummary): boolean {
  return (summary.limit > 0 && summary.active >= summary.limit)
    || (accountConcurrencyRequestLimit(summary) > 0 && accountConcurrencyUsedRequests(summary) >= accountConcurrencyRequestLimit(summary));
}

export function formatAccountConcurrency(
  summary: AccountConcurrencySummary,
  labels: { active: string; request: string; queue: string } = { active: "active", request: "request", queue: "queue" },
): string {
  return `${labels.active} ${summary.active}/${accountConcurrencyLimitLabel(summary)} · ${accountConcurrencyWindowSeconds(summary)}s ${labels.request} ${accountConcurrencyUsedRequests(summary)}/${accountConcurrencyRequestLimitLabel(summary)} · ${labels.queue} ${summary.waiting ?? 0}`;
}

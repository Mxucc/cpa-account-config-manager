import type { AccountConcurrencySummary } from "./types";

export function accountConcurrencyLimitLabel(summary: AccountConcurrencySummary): string {
	return summary.limit > 0 ? String(summary.limit) : "∞";
}

export function formatAccountConcurrency(summary: AccountConcurrencySummary): string {
	return `${summary.active}/${accountConcurrencyLimitLabel(summary)}`;
}

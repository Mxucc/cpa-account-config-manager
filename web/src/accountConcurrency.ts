import type { AccountConcurrencySummary } from "./types";

export type AccountConcurrencyWindow = "15s" | "60s";

export function accountConcurrencyLimitLabel(summary: AccountConcurrencySummary, window: AccountConcurrencyWindow = "60s"): string {
	const limit = window === "15s" ? summary.limit_15s : summary.limit;
	return limit > 0 ? String(limit) : "∞";
}

export function accountConcurrencySaturated(summary: AccountConcurrencySummary): boolean {
	return (summary.limit_15s > 0 && summary.used_15s >= summary.limit_15s)
		|| (summary.limit > 0 && summary.used_60s >= summary.limit);
}

export function formatAccountConcurrency(
	summary: AccountConcurrencySummary,
	labels: { active: string; fifteenSeconds: string; minute: string } = { active: "active", fifteenSeconds: "15s", minute: "60s" },
): string {
	return `${labels.active} ${summary.active} · ${labels.fifteenSeconds} ${summary.used_15s}/${accountConcurrencyLimitLabel(summary, "15s")} · ${labels.minute} ${summary.used_60s}/${accountConcurrencyLimitLabel(summary, "60s")}`;
}

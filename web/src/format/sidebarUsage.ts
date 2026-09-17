import type { Account, AIProviderRuntimeSnapshot } from "../types";

/** The two combined figures the sidebar footer reports. */
export interface SidebarUsageTotals {
  /** Enabled CPA accounts plus AI-provider runtime quota windows, in USD. */
  costUSD: number;
  /** Enabled CPA accounts plus AI-provider runtime counters, in tokens. */
  totalTokens: number;
}

/**
 * A missing, unusable or negative counter contributes nothing. A snapshot can be assembled from
 * several API reads, so a field that never arrived must not turn a whole footer total into NaN.
 */
function safeAmount(value: unknown): number {
  const number = Number(value);
  return Number.isFinite(number) && number > 0 ? number : 0;
}

/**
 * One spend figure and one token figure instead of a breakdown per credential family: an operator
 * reads the footer as a single running total, and the six separate counters it used to print
 * (enabled, active, spend for each family) added up to exactly these two numbers while taking the
 * whole block.
 *
 * A disabled account contributes nothing, which is the scope that block always used. Provider cost
 * keeps the five-hour quota window the previous provider row reported, so the footer total stays on
 * the same footing as before.
 *
 * Neither figure is a daily window, which is why the footer labels them as accumulated: an account
 * credit accumulates from the moment its accounting started and the provider amount is a rolling
 * five-hour quota window, while both token counters are aggregates of their own snapshot.
 */
export function sidebarUsageTotals(accounts: Account[], runtime: AIProviderRuntimeSnapshot[]): SidebarUsageTotals {
  let costUSD = 0;
  let totalTokens = 0;
  for (const account of accounts) {
    if (account.disabled) continue;
    costUSD += safeAmount(account.usage?.credit?.amount_usd);
    totalTokens += safeAmount(account.usage?.total_tokens);
  }
  for (const snapshot of runtime) {
    costUSD += safeAmount(snapshot.quota?.five_hour_amount_usd);
    totalTokens += safeAmount(snapshot.total_tokens);
  }
  return { costUSD, totalTokens };
}

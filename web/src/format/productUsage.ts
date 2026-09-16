import type { Account, AIProviderChannelKind, AIProviderChannelSnapshot, AIProviderRuntimeSnapshot, OpenCodeQuotaResult } from "../types";
import { providerRuntimeSnapshotsForChannels } from "./providerRuntime";

/**
 * The headline usage numbers one product overview reports. Each menu aggregates its own
 * product only: Codex sums its CPA accounts, OpenCode sums the runtime aggregates of its own
 * channels, and Cline Pass sums the usage its gateway attributes to its own accounts.
 */
export interface ProductUsage {
  inputTokens: number;
  outputTokens: number;
  cachedTokens: number;
  totalTokens: number;
  requests: number;
  ratedRequests: number;
  unratedRequests: number;
  amountUSD: number;
}

/** One reported quota window: the tightest usage share and when it resets. */
export interface ProductQuotaWindow {
  usagePercent: number;
  percentRemaining?: number;
  resetAt?: string;
  resetInSeconds?: number;
  /** How many credentials reported this window, so an aggregate stays honest. */
  credentials: number;
}

export interface ProductUsageSummary extends ProductUsage {
  /** True once any counter, priced amount or quota window was observed. */
  observed: boolean;
  /** How many credentials contributed numbers, so the UI can label a sum. */
  reportingCredentials: number;
  fiveHour?: ProductQuotaWindow;
  sevenDay?: ProductQuotaWindow;
  /** Quota amounts the runtime attributes to a window: usage, never an allowance. */
  fiveHourAmountUSD?: number;
  sevenDayAmountUSD?: number;
}

const EMPTY_USAGE: ProductUsage = {
  inputTokens: 0,
  outputTokens: 0,
  cachedTokens: 0,
  totalTokens: 0,
  requests: 0,
  ratedRequests: 0,
  unratedRequests: 0,
  amountUSD: 0,
};

function safeCount(value: unknown): number {
  const number = Number(value);
  return Number.isFinite(number) && number > 0 ? number : 0;
}

function safePercent(value: unknown): number | undefined {
  const number = Number(value);
  if (!Number.isFinite(number)) return undefined;
  return Math.min(100, Math.max(0, number));
}

function emptySummary(): ProductUsageSummary {
  return { ...EMPTY_USAGE, observed: false, reportingCredentials: 0 };
}

/**
 * The tightest window wins an aggregate: the 5h/7d percentages belong to one credential at a
 * time, so the highest usage is what limits the product, and its reset time travels with it.
 */
function tightest(current: ProductQuotaWindow | undefined, next: ProductQuotaWindow): ProductQuotaWindow {
  if (!current) return next;
  if (next.usagePercent > current.usagePercent) return { ...next, credentials: current.credentials + next.credentials };
  return { ...current, credentials: current.credentials + next.credentials };
}

function hasUsage(usage: ProductUsage): boolean {
  return usage.totalTokens > 0 || usage.inputTokens > 0 || usage.outputTokens > 0 || usage.cachedTokens > 0
    || usage.requests > 0 || usage.amountUSD > 0;
}

/**
 * Sum the usage of one product's own CPA accounts. Codex accounts carry their own usage
 * snapshot (`usage.codex`) including the upstream 5h/7d windows, so the overview can show the
 * product's usage without depending on any provider-side attribution.
 */
export function accountProductUsage(accounts: Account[]): ProductUsageSummary {
  const summary = emptySummary();
  for (const account of accounts) {
    const usage = account.usage;
    const credit = usage?.credit;
    const requests = safeCount(account.success) + safeCount(account.failed);
    summary.inputTokens += safeCount(usage?.input_tokens);
    summary.outputTokens += safeCount(usage?.output_tokens);
    summary.cachedTokens += safeCount(usage?.cached_tokens) + safeCount(usage?.cache_read_tokens) + safeCount(usage?.cache_creation_tokens);
    summary.totalTokens += safeCount(usage?.total_tokens);
    summary.requests += requests;
    summary.ratedRequests += safeCount(credit?.rated_requests);
    summary.unratedRequests += safeCount(credit?.unrated_requests);
    summary.amountUSD += safeCount(credit?.amount_usd);
    if (usage || requests > 0) summary.reportingCredentials += 1;
    const fiveHour = usage?.codex?.five_hour;
    if (fiveHour) summary.fiveHour = tightest(summary.fiveHour, {
      usagePercent: safePercent(fiveHour.used_percent) ?? 0,
      resetAt: fiveHour.reset_at,
      credentials: 1,
    });
    const sevenDay = usage?.codex?.seven_day;
    if (sevenDay) summary.sevenDay = tightest(summary.sevenDay, {
      usagePercent: safePercent(sevenDay.used_percent) ?? 0,
      resetAt: sevenDay.reset_at,
      credentials: 1,
    });
  }
  summary.observed = hasUsage(summary) || summary.fiveHour !== undefined || summary.sevenDay !== undefined;
  return summary;
}

/**
 * Sum the AI-providers runtime aggregates that can be proven to belong to one product's own
 * channels. `providerRuntimeSnapshotsForChannels` keeps the account-traffic and auth-index
 * collision guards, so an unattributable snapshot contributes nothing instead of a wrong number.
 */
export function channelProductUsage(
  channels: AIProviderChannelSnapshot[],
  snapshots: AIProviderRuntimeSnapshot[],
  kinds: AIProviderChannelKind[],
  accounts: Account[] = [],
): ProductUsageSummary {
  const summary = emptySummary();
  const productChannels = channels.filter((channel) => kinds.includes(channel.kind));
  for (const snapshot of providerRuntimeSnapshotsForChannels(productChannels, snapshots, accounts)) {
    const rated = safeCount(snapshot.rated_requests);
    const unrated = safeCount(snapshot.unrated_requests);
    summary.inputTokens += safeCount(snapshot.input_tokens);
    summary.outputTokens += safeCount(snapshot.output_tokens);
    summary.cachedTokens += safeCount(snapshot.cached_tokens);
    summary.totalTokens += safeCount(snapshot.total_tokens);
    summary.requests += rated + unrated;
    summary.ratedRequests += rated;
    summary.unratedRequests += unrated;
    summary.amountUSD += safeCount(snapshot.amount_usd);
    if (safeCount(snapshot.total_tokens) > 0 || rated + unrated > 0 || safeCount(snapshot.amount_usd) > 0) {
      summary.reportingCredentials += 1;
    }
    const fiveHourPercent = safePercent(snapshot.quota?.five_hour_percent);
    if (fiveHourPercent !== undefined) summary.fiveHour = tightest(summary.fiveHour, { usagePercent: fiveHourPercent, credentials: 1 });
    const sevenDayPercent = safePercent(snapshot.quota?.seven_day_percent);
    if (sevenDayPercent !== undefined) summary.sevenDay = tightest(summary.sevenDay, { usagePercent: sevenDayPercent, credentials: 1 });
    summary.fiveHourAmountUSD = (summary.fiveHourAmountUSD ?? 0) + safeCount(snapshot.quota?.five_hour_amount_usd);
    summary.sevenDayAmountUSD = (summary.sevenDayAmountUSD ?? 0) + safeCount(snapshot.quota?.seven_day_amount_usd);
  }
  summary.observed = hasUsage(summary) || summary.fiveHour !== undefined || summary.sevenDay !== undefined;
  return summary;
}

/**
 * Aggregate the OpenCode quota API. It reports shares of each window and their reset, never a
 * USD allowance, so the only balance OpenCode can honestly show is the remaining share.
 */
export function openCodeQuotaWindows(results: Record<string, OpenCodeQuotaResult>): {
  fiveHour?: ProductQuotaWindow;
  weekly?: ProductQuotaWindow;
  monthly?: ProductQuotaWindow;
} {
  const aggregate = (
    pick: (result: OpenCodeQuotaResult) => OpenCodeQuotaResult["rolling"],
  ): ProductQuotaWindow | undefined => {
    let window: ProductQuotaWindow | undefined;
    for (const result of Object.values(results)) {
      if (!result?.success) continue;
      const reported = pick(result);
      if (!reported) continue;
      window = tightest(window, {
        usagePercent: safePercent(reported.usage_percent) ?? 0,
        percentRemaining: safePercent(reported.percent_remaining),
        resetAt: reported.reset_at,
        resetInSeconds: safeCount(reported.reset_in_sec),
        credentials: 1,
      });
    }
    return window;
  };
  return {
    fiveHour: aggregate((result) => result.rolling),
    weekly: aggregate((result) => result.weekly),
    monthly: aggregate((result) => result.monthly),
  };
}

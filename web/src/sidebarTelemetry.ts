import type { Account, AIProviderChannelSnapshot, AIProviderRuntimeSnapshot } from "./types";

/**
 * The sidebar shows one combined spend total and one combined token total, so it reads the account
 * AI-provider channel on a schedule. A fixed ten-second timer hammered the plugin and CPA's
 * Management API around the clock: a background tab kept polling a page nobody could see, and an
 * installation with no traffic kept re-reading rows that had not changed for hours. The schedule
 * therefore follows the operator instead of the clock: it stops while the page is hidden, reads
 * again as soon as the page is visible, and only keeps the fast cadence while the totals it shows
 * are actually moving.
 */

export const sidebarTelemetryTiming = {
  // Cadence after a fresh visit, and while the totals the sidebar shows keep changing.
  intervalMS: 10_000,
  // Cadence once consecutive reads returned the same totals: nothing visible changed, so there is
  // no reason to keep asking every ten seconds.
  idleIntervalMS: 60_000,
  // Identical reads in a row before the schedule drops to the idle cadence.
  idleAfterPolls: 3,
};

export interface SidebarTelemetryRead {
  /** False while the document is hidden: the sidebar cannot be seen, so nothing is requested. */
  visible: boolean;
  /** Consecutive reads that returned exactly the totals the sidebar already shows. */
  unchangedPolls: number;
}

/**
 * nextSidebarTelemetryDelay answers how long to wait before the next read. A hidden document
 * answers null, which tells the caller to wait for the next visibility change instead of polling
 * a page the operator is not looking at.
 */
export function nextSidebarTelemetryDelay(read: SidebarTelemetryRead): number | null {
  if (!read.visible) return null;
  return read.unchangedPolls >= sidebarTelemetryTiming.idleAfterPolls
    ? sidebarTelemetryTiming.idleIntervalMS
    : sidebarTelemetryTiming.intervalMS;
}

/** isSidebarTelemetryVisible reports whether the sidebar behind the totals can be seen. */
export function isSidebarTelemetryVisible(document?: Pick<Document, "hidden">): boolean {
  const current = document ?? (typeof window !== "undefined" ? window.document : undefined);
  return current?.hidden !== true;
}

/**
 * sidebarTelemetrySignature summarizes everything the sidebar totals are derived from, including
 * the account and provider identities that decide which runtime aggregate a channel may show.
 * Two reads with the same signature render the same numbers, which is what lets the schedule back
 * off without ever showing a total that moved in the meantime. Busy-window and concurrency
 * counters such as `used_requests`, `active` or `limit` are deliberately absent: they move with
 * traffic without changing a single number on screen, so counting them would keep a quiet
 * installation on the fast cadence forever.
 * Token counters are the opposite case: the footer prints their combined total, so a change there
 * has to count as a change.
 */
export function sidebarTelemetrySignature(
  accounts: Account[],
  channels: AIProviderChannelSnapshot[],
  runtime: AIProviderRuntimeSnapshot[],
): string {
  const accountParts = accounts.map((account) => [
    account.id,
    account.auth_id ?? "",
    account.credential?.id ?? "",
    account.credential?.auth_id ?? "",
    account.disabled ? "1" : "0",
    account.usage?.credit?.amount_usd ?? 0,
    account.usage?.total_tokens ?? 0,
  ].join(":"));
  const channelParts = channels.map((channel) => [
    channel.kind,
    channel.error ?? "",
    channel.storage_error ?? "",
    (channel.entries ?? []).map((entry) => [
      entry.disabled ? "1" : "0",
      entry.auth_index ?? "",
      entry.account_id ?? "",
      entry.workspace_id ?? "",
      (entry.api_key_entries ?? []).map((keyEntry) => keyEntry.auth_index ?? "").join("+"),
    ].join(":")).join(";"),
  ].join(":"));
  const runtimeParts = runtime.map((snapshot) => [
    snapshot.provider,
    snapshot.auth_index ?? "",
    snapshot.identity,
    snapshot.quota?.five_hour_amount_usd ?? 0,
    snapshot.total_tokens ?? 0,
  ].join(":"));
  return `${accountParts.join(",")}|${channelParts.join(",")}|${runtimeParts.join(",")}`;
}

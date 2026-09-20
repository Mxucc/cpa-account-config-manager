import { describe, expect, it, vi } from "vitest";
import { collectDashboardAccounts, countUnhealthyAccounts, normalizeDashboardPageCount, normalizeDashboardPageSize, summarizeProductUsage } from "./DashboardWorkspace";
import type { Account, AccountListResponse, AIProviderChannelKind, AIProviderChannelSnapshot, AIProviderRuntimeSnapshot } from "../types";

const account = (id: string) => ({ id, name: `${id}.json`, email: `${id}@example.com` });

function page(total: number, pages: number): AccountListResponse {
  return { accounts: [account("one")], total, page: 1, page_size: 1000, pages } as AccountListResponse;
}

describe("dashboard account collection", () => {
  it("normalizes malformed pagination metadata", () => {
    expect(normalizeDashboardPageCount(Number.NaN)).toBe(1);
    expect(normalizeDashboardPageCount(-2)).toBe(1);
    expect(normalizeDashboardPageCount(100000)).toBe(100);
    expect(normalizeDashboardPageSize(Number.NaN)).toBe(1000);
    expect(normalizeDashboardPageSize(0)).toBe(1);
  });

  it("classifies every non-healthy inspection state as unhealthy", () => {
    const base = account("health");
    const runtimeUnavailable = base;
    (runtimeUnavailable as Account).unavailable = true;
    const accounts: Account[] = [
      { ...base, automation: { health: "healthy" } },
      { ...base, id: "quota", automation: { health: "quota_limited" } },
      { ...base, id: "review", automation: { health: "review" } },
      { ...base, id: "unavailable-health", automation: { health: "unavailable" } },
      { ...base, id: "unknown-health", automation: { health: "unknown" } },
      runtimeUnavailable,
      { ...base, id: "status-error", status: "error" },
    ] as Account[];

    expect(countUnhealthyAccounts(accounts)).toBe(7);
  });

  it("caps malformed page fan-out and deduplicates results", async () => {
    const fetchPage = vi.fn(async (requestedPage: number) => ({
      accounts: [account(requestedPage === 2 ? "one" : `account-${requestedPage}`)],
      total: 50_000_000,
      page: requestedPage,
      page_size: 1000,
      pages: 50_000,
    }) as AccountListResponse);

    const accounts = await collectDashboardAccounts(page(50_000_000, 50_000), fetchPage);

    expect(fetchPage).toHaveBeenCalledTimes(99);
    expect(accounts).toHaveLength(99);
  });

  it("stops page collection when the caller aborts", async () => {
    const controller = new AbortController();
    const fetchPage = vi.fn(async () => {
      controller.abort();
      return page(2000, 2);
    });

    const accounts = await collectDashboardAccounts(page(2000, 2), fetchPage, controller.signal);

    expect(accounts).toHaveLength(1);
    expect(fetchPage).toHaveBeenCalledTimes(1);
  });
});

describe("dashboard product usage", () => {
  const usageAccount = (overrides: Record<string, unknown>): Account => ({
    id: "account",
    name: "account.json",
    provider: "claude",
    type: "claude",
    usage: { total_tokens: 0, input_tokens: 0, output_tokens: 0, cached_tokens: 0, credit: { amount_usd: 0, rated_requests: 0, unrated_requests: 0 } },
    ...overrides,
  } as unknown as Account);

  const channel = (kind: AIProviderChannelKind, entry: Record<string, unknown>): AIProviderChannelSnapshot => ({
    kind,
    count: 1,
    entries: [{ index: 0, name: "channel-entry", key_set: true, ...entry }],
  } as unknown as AIProviderChannelSnapshot);

  it("splits the totals by product and keeps an unattributed remainder", () => {
    const accounts = [
      usageAccount({ id: "codex-1", provider: "codex", type: "codex", usage: { total_tokens: 1_000, credit: { amount_usd: 1.5 } } }),
      usageAccount({ id: "claude-1", usage: { total_tokens: 2_000, credit: { amount_usd: 2.5 } } }),
    ];
    const providers = [
      channel("opencode-go", { account_id: "go-1" }),
      channel("openai-compatibility", { auth_index: "cred-x" }),
    ];
    const snapshots: AIProviderRuntimeSnapshot[] = [
      { provider: "opencode go", identity: "account:go-1", auth_index: "go-1", total_tokens: 300, amount_usd: 0.3, rated_requests: 3, unrated_requests: 0 },
      { provider: "openai-compatibility", identity: "credential:cred-x", auth_index: "cred-x", total_tokens: 400, amount_usd: 0.4, rated_requests: 4, unrated_requests: 0 },
    ] as unknown as AIProviderRuntimeSnapshot[];

    const rows = summarizeProductUsage(accounts, providers, snapshots);

    expect(rows.map((row) => row.key)).toEqual(["opencode", "codex", "other"]);
    // OpenCode is only its own channels.
    expect(rows[0].usage).toMatchObject({ totalTokens: 300, amountUSD: 0.3, requests: 3 });
    // Codex adds its credential usage to its own API-key channel.
    expect(rows[1].usage).toMatchObject({ totalTokens: 1_000, amountUSD: 1.5 });
    // A product the plugin cannot name keeps its traffic visible instead of dropping it.
    expect(rows[2].usage).toMatchObject({ totalTokens: 2_400, amountUSD: 2.9 });
    // The rows add back up to the two stores the dashboard totals are made of.
    expect(rows.reduce((total, row) => total + row.usage.totalTokens, 0)).toBe(1_000 + 2_000 + 300 + 400);
    expect(rows.reduce((total, row) => total + row.usage.amountUSD, 0)).toBeCloseTo(1.5 + 2.5 + 0.3 + 0.4, 6);
  });
});

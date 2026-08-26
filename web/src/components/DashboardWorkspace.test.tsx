import { describe, expect, it, vi } from "vitest";
import { collectDashboardAccounts, countUnhealthyAccounts, normalizeDashboardPageCount, normalizeDashboardPageSize } from "./DashboardWorkspace";
import type { Account, AccountListResponse } from "../types";

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

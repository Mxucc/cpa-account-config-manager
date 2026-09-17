import { describe, expect, it } from "vitest";
import type { Account, AIProviderRuntimeSnapshot } from "../types";
import { sidebarUsageTotals } from "./sidebarUsage";

const account = (overrides: Record<string, unknown> = {}): Account => ({
  id: "account-1",
  disabled: false,
  usage: { credit: { amount_usd: 1.25 }, total_tokens: 2_000 },
  ...overrides,
} as unknown as Account);

const snapshot = (overrides: Record<string, unknown> = {}): AIProviderRuntimeSnapshot => ({
  provider: "openai-compatibility",
  identity: "openai-compatibility:0",
  total_tokens: 4_000,
  quota: { five_hour_amount_usd: 16 },
  ...overrides,
} as unknown as AIProviderRuntimeSnapshot);

describe("sidebarUsageTotals", () => {
  it("adds the enabled accounts and the provider runtime into two figures", () => {
    const accounts = [account(), account({ id: "account-2", usage: { credit: { amount_usd: 0.75 }, total_tokens: 1_000 } })];
    const runtime = [snapshot(), snapshot({ identity: "openai-compatibility:1", total_tokens: 500, quota: { five_hour_amount_usd: 0.25 } })];
    expect(sidebarUsageTotals(accounts, runtime)).toEqual({ costUSD: 18.25, totalTokens: 7_500 });
  });

  it("leaves a disabled account out of both figures", () => {
    expect(sidebarUsageTotals([account({ disabled: true })], [])).toEqual({ costUSD: 0, totalTokens: 0 });
  });

  it("treats missing and unusable counters as zero", () => {
    expect(sidebarUsageTotals([account({ usage: undefined }), account({ usage: {} })], []))
      .toEqual({ costUSD: 0, totalTokens: 0 });
    expect(sidebarUsageTotals([], [snapshot({ total_tokens: -5, quota: { five_hour_amount_usd: Number.NaN } })]))
      .toEqual({ costUSD: 0, totalTokens: 0 });
  });
});

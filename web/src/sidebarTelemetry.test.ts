import { describe, expect, it } from "vitest";
import { isSidebarTelemetryVisible, nextSidebarTelemetryDelay, sidebarTelemetrySignature, sidebarTelemetryTiming } from "./sidebarTelemetry";
import type { Account, AIProviderChannelSnapshot, AIProviderRuntimeSnapshot } from "./types";

const accounts = (overrides: Record<string, unknown> = {}): Account[] => [{
  id: "auth-1",
  name: "operator.json",
  note: "",
  disabled: false,
  concurrency: { supported: true, active: 2, limit: 10 },
  usage: { credit: { amount_usd: 1.5 } },
  ...overrides,
}] as unknown as Account[];

const channels = (overrides: Record<string, unknown> = {}): AIProviderChannelSnapshot[] => [{
  kind: "openai-compatibility",
  count: 3,
  entries: [{}, {}, {}],
  ...overrides,
}] as unknown as AIProviderChannelSnapshot[];

const runtime = (overrides: Record<string, unknown> = {}): AIProviderRuntimeSnapshot[] => [{
  provider: "openai-compatibility",
  identity: "openai-compatibility:0",
  active: 1,
  waiting: 0,
  limit: 20,
  used_requests: 5,
  quota: { five_hour_amount_usd: 0.25 },
  ...overrides,
}] as unknown as AIProviderRuntimeSnapshot[];

describe("nextSidebarTelemetryDelay", () => {
  it("never schedules while the page is hidden", () => {
    expect(nextSidebarTelemetryDelay({ visible: false, unchangedPolls: 0 })).toBeNull();
    expect(nextSidebarTelemetryDelay({ visible: false, unchangedPolls: 12 })).toBeNull();
  });

  it("keeps the ten-second cadence while the totals are still moving", () => {
    expect(nextSidebarTelemetryDelay({ visible: true, unchangedPolls: 0 })).toBe(sidebarTelemetryTiming.intervalMS);
    expect(nextSidebarTelemetryDelay({ visible: true, unchangedPolls: sidebarTelemetryTiming.idleAfterPolls - 1 }))
      .toBe(sidebarTelemetryTiming.intervalMS);
  });

  it("drops to the idle cadence once repeated reads returned the same totals", () => {
    expect(nextSidebarTelemetryDelay({ visible: true, unchangedPolls: sidebarTelemetryTiming.idleAfterPolls }))
      .toBe(sidebarTelemetryTiming.idleIntervalMS);
    expect(nextSidebarTelemetryDelay({ visible: true, unchangedPolls: 40 })).toBe(sidebarTelemetryTiming.idleIntervalMS);
  });
});

describe("isSidebarTelemetryVisible", () => {
  it("reads the document visibility", () => {
    expect(isSidebarTelemetryVisible({ hidden: true })).toBe(false);
    expect(isSidebarTelemetryVisible({ hidden: false })).toBe(true);
  });

  it("treats an unknown document as visible", () => {
    expect(isSidebarTelemetryVisible(undefined)).toBe(true);
  });
});

describe("sidebarTelemetrySignature", () => {
  it("is stable for data that would render the same totals", () => {
    expect(sidebarTelemetrySignature(accounts(), channels(), runtime()))
      .toBe(sidebarTelemetrySignature(accounts(), channels(), runtime()));
  });

  it("ignores fields the sidebar totals never show", () => {
    expect(sidebarTelemetrySignature(accounts({ name: "renamed.json", note: "operator note" }), channels(), runtime()))
      .toBe(sidebarTelemetrySignature(accounts(), channels(), runtime()));
  });

  it("changes when an account total or the identity behind it moves", () => {
    const base = sidebarTelemetrySignature(accounts(), channels(), runtime());
    expect(sidebarTelemetrySignature(accounts({ concurrency: { supported: true, active: 3, limit: 10 } }), channels(), runtime())).not.toBe(base);
    expect(sidebarTelemetrySignature(accounts({ disabled: true }), channels(), runtime())).not.toBe(base);
    expect(sidebarTelemetrySignature(accounts({ usage: { credit: { amount_usd: 2 } } }), channels(), runtime())).not.toBe(base);
    // The credential identities decide which runtime aggregate may be shown as provider usage.
    expect(sidebarTelemetrySignature(accounts({ auth_id: "auth-2" }), channels(), runtime())).not.toBe(base);
    expect(sidebarTelemetrySignature(accounts({ credential: { id: "cred-2" } }), channels(), runtime())).not.toBe(base);
    expect(sidebarTelemetrySignature([], channels(), runtime())).not.toBe(base);
  });

  it("changes when a provider entry or runtime total moves", () => {
    const base = sidebarTelemetrySignature(accounts(), channels(), runtime());
    expect(sidebarTelemetrySignature(accounts(), channels({ count: 4, entries: [{}, {}, {}, {}] }), runtime())).not.toBe(base);
    expect(sidebarTelemetrySignature(accounts(), channels({ error: "channel_unavailable" }), runtime())).not.toBe(base);
    // Disabling one entry keeps the entry count identical, so only the entry fields can catch it.
    expect(sidebarTelemetrySignature(accounts(), channels({ entries: [{ disabled: true }, {}, {}] }), runtime())).not.toBe(base);
    expect(sidebarTelemetrySignature(accounts(), channels({ entries: [{ auth_index: "idx-9" }, {}, {}] }), runtime())).not.toBe(base);
    expect(sidebarTelemetrySignature(accounts(), channels(), runtime({ active: 7 }))).not.toBe(base);
    expect(sidebarTelemetrySignature(accounts(), channels(), runtime({ auth_index: "idx-9" }))).not.toBe(base);
    expect(sidebarTelemetrySignature(accounts(), channels(), runtime({ quota: { five_hour_amount_usd: 0.5 } }))).not.toBe(base);
    expect(sidebarTelemetrySignature(accounts(), channels(), [])).not.toBe(base);
  });

  it("ignores busy-window counters that never change a displayed total", () => {
    // Traffic keeps moving these counters. Counting them would keep a busy installation on the
    // fast cadence, while every number the sidebar renders stayed the same.
    const base = sidebarTelemetrySignature(accounts(), channels(), runtime());
    expect(sidebarTelemetrySignature(accounts(), channels(), runtime({ used_requests: 4_242, waiting: 8 }))).toBe(base);
  });
});

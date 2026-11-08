import { describe, expect, test } from "vitest";
import { accountWindows, formatAmount, metricLabel, windowLabel } from "./shared";
import { copyFor } from "./copy";
import type { ProviderAccount } from "./types";

describe("quota display helpers", () => {
  test("a currency amount is formatted instead of showing float noise", () => {
    // A live host reports 1.9980000000000002 as a five-hour allowance share.
    expect(formatAmount(1.9980000000000002, "USD")).toBe("$2.00");
    expect(formatAmount(9.99, "USD")).toBe("$9.99");
  });

  test("a non-currency unit keeps a bounded number of decimals", () => {
    expect(formatAmount(1234.5678)).toBe("1,234.57");
    expect(formatAmount(42)).toBe("42");
  });

  test("a null quota list reads as no windows", () => {
    expect(accountWindows({ quota: null } as unknown as ProviderAccount)).toEqual([]);
    expect(accountWindows({ quota: [] } as unknown as ProviderAccount)).toEqual([]);
  });
});

describe("provider copy lookups", () => {
  const zh = copyFor("zh-CN");
  const en = copyFor("en");

  test("a known window label is translated and an unknown one is kept", () => {
    expect(windowLabel("5 hours", zh)).toBe("5 小时");
    expect(windowLabel("Monthly", zh)).toBe("按月");
    // A provider label the catalog has never seen must not be swallowed.
    expect(windowLabel("Per 3 hours", zh)).toBe("Per 3 hours");
  });

  test("a known metric name is translated and an unknown one is kept", () => {
    expect(metricLabel("active_reset_count", "Active reset credits", zh)).toBe("可用重置次数");
    expect(metricLabel("something_new", "Something new", zh)).toBe("Something new");
  });

  test("the English catalog keeps the provider wording", () => {
    expect(windowLabel("5 hours", en)).toBe("5 hours");
    expect(metricLabel("active_reset_count", "Active reset credits", en)).toBe("Active reset credits");
  });
});

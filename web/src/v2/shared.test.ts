import { describe, expect, test } from "vitest";
import { accountWindows, formatAmount } from "./shared";
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

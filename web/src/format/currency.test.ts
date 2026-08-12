import { describe, expect, it } from "vitest";
import { formatCreditUSD } from "./currency";

describe("formatCreditUSD", () => {
  it("keeps low-cost model charges visible down to backend nano-USD precision", () => {
    expect(formatCreditUSD(0.000345, "zh-CN")).toContain("0.000345");
    expect(formatCreditUSD(0.000000345, "zh-CN")).toContain("0.000000345");
    expect(formatCreditUSD(0.000000001, "zh-CN")).toContain("0.000000001");
  });

  it("keeps normal totals compact and handles invalid values safely", () => {
    expect(formatCreditUSD(0.1544321, "zh-CN")).toContain("0.1544");
    expect(formatCreditUSD(Number.NaN, "zh-CN")).toContain("0");
    expect(formatCreditUSD(-1, "zh-CN")).toContain("0");
  });
});

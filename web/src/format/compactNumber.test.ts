import { describe, expect, it } from "vitest";
import { formatCompactNumber } from "./compactNumber";

describe("formatCompactNumber", () => {
  it("keeps counts below a thousand exact", () => {
    expect(formatCompactNumber(0, "en")).toBe("0");
    expect(formatCompactNumber(999, "en")).toBe("999");
  });

  it("compacts thousands and millions", () => {
    expect(formatCompactNumber(1_500, "en")).toBe("1.5K");
    expect(formatCompactNumber(2_000_000, "en")).toBe("2M");
  });

  it("treats an unusable count as zero instead of printing NaN", () => {
    expect(formatCompactNumber(Number.NaN, "en")).toBe("0");
    expect(formatCompactNumber(-5, "en")).toBe("0");
  });

  it("formats a small count the same way in every locale", () => {
    expect(formatCompactNumber(999, "zh-CN")).toBe("999");
    expect(formatCompactNumber(999, "zh-TW")).toBe("999");
    expect(formatCompactNumber(999, "ru")).toBe("999");
  });
});

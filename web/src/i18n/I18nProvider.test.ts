import { describe, expect, it } from "vitest";
import { formatDateTimeForLocale } from "./I18nProvider";

describe("formatDateTimeForLocale", () => {
  it("treats Go zero-value timestamps as missing", () => {
    expect(formatDateTimeForLocale("zh-CN", "0001-01-01T00:00:00Z")).toBe("-");
  });

  it("still formats valid application timestamps", () => {
    expect(formatDateTimeForLocale("zh-CN", "2026-09-04T01:47:00Z")).not.toBe("-");
  });
});

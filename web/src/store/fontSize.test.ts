import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  DEFAULT_FONT_SIZE,
  FONT_SIZE_STORAGE_KEY,
  applyFontSize,
  initFontSize,
  readFontSize,
  writeFontSize,
} from "./fontSize";

describe("font-size preference", () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.removeAttribute("data-font-size");
    vi.restoreAllMocks();
  });

  it("keeps the existing typography as small and persists custom presets", () => {
    expect(readFontSize()).toBe(DEFAULT_FONT_SIZE);
    initFontSize();
    expect(document.documentElement).not.toHaveAttribute("data-font-size");

    writeFontSize("medium");
    expect(localStorage.getItem(FONT_SIZE_STORAGE_KEY)).toBe("medium");
    expect(document.documentElement).toHaveAttribute("data-font-size", "medium");

    writeFontSize("large");
    expect(readFontSize()).toBe("large");
    expect(document.documentElement).toHaveAttribute("data-font-size", "large");
  });

  it("clears the custom preference when small is selected", () => {
    localStorage.setItem(FONT_SIZE_STORAGE_KEY, "large");
    applyFontSize("large");
    writeFontSize("small");
    expect(localStorage.getItem(FONT_SIZE_STORAGE_KEY)).toBeNull();
    expect(document.documentElement).not.toHaveAttribute("data-font-size");
  });

  it("falls back safely for malformed or unavailable browser storage", () => {
    localStorage.setItem(FONT_SIZE_STORAGE_KEY, "extra-large");
    expect(readFontSize()).toBe("small");

    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new Error("denied"); });
    expect(readFontSize()).toBe("small");
    vi.restoreAllMocks();
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("denied"); });
    expect(() => writeFontSize("medium")).not.toThrow();
    expect(document.documentElement).toHaveAttribute("data-font-size", "medium");
  });

  it("routes every explicit stylesheet font size through the global offset", () => {
    const css = readFileSync(resolve(process.cwd(), "src/styles.css"), "utf8");
    expect(css).not.toMatch(/font-size:\s*\d+(?:\.\d+)?px\s*[;}]/);
    expect(css).toContain("--font-size-scale: 1");
    expect(css).toContain('html[data-font-size="medium"]');
    expect(css).toContain('html[data-font-size="large"]');
  });
});

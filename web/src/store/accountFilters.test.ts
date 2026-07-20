import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  ACCOUNT_FILTERS_STORAGE_KEY,
  EMPTY_ACCOUNT_FILTERS,
  readAccountFilters,
  writeAccountFilters,
} from "./accountFilters";

describe("account filter preferences", () => {
  beforeEach(() => {
    localStorage.clear();
    vi.restoreAllMocks();
  });

  it("persists and restores only normalized filter fields", () => {
    writeAccountFilters({
      search: " operator@example.com ",
      provider: " codex ",
      type: " k12 ",
      status: "ACTIVE",
      disabled: "false",
      editability: "editable",
    });

    expect(readAccountFilters()).toEqual({
      search: "operator@example.com",
      provider: "codex",
      type: "k12",
      status: "active",
      disabled: "false",
      editability: "editable",
    });
    expect(JSON.parse(localStorage.getItem(ACCOUNT_FILTERS_STORAGE_KEY) || "{}")).not.toHaveProperty("selected");
  });

  it("rejects malformed versions and normalizes invalid fields", () => {
    for (const raw of ["not-json", "[]", '{"version":2,"search":"secret"}']) {
      localStorage.setItem(ACCOUNT_FILTERS_STORAGE_KEY, raw);
      expect(readAccountFilters()).toEqual(EMPTY_ACCOUNT_FILTERS);
    }

    localStorage.setItem(ACCOUNT_FILTERS_STORAGE_KEY, JSON.stringify({
      version: 1,
      search: "abc\u0000def",
      provider: 42,
      type: "k12",
      status: "deleted",
      disabled: "yes",
      editability: "owner",
      selected: ["auth-1"],
    }));
    expect(readAccountFilters()).toEqual({ ...EMPTY_ACCOUNT_FILTERS, search: "abcdef", type: "k12" });
  });

  it("removes storage for an empty state and tolerates unavailable storage", () => {
    localStorage.setItem(ACCOUNT_FILTERS_STORAGE_KEY, "stale");
    writeAccountFilters(EMPTY_ACCOUNT_FILTERS);
    expect(localStorage.getItem(ACCOUNT_FILTERS_STORAGE_KEY)).toBeNull();

    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new Error("denied"); });
    expect(readAccountFilters()).toEqual(EMPTY_ACCOUNT_FILTERS);
    vi.restoreAllMocks();
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("denied"); });
    expect(() => writeAccountFilters({ ...EMPTY_ACCOUNT_FILTERS, search: "operator" })).not.toThrow();
  });
});

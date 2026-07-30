import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  ACCOUNT_SORT_STORAGE_KEY,
  DEFAULT_ACCOUNT_SORT,
  isDefaultAccountSort,
  readAccountSort,
  writeAccountSort,
} from "./accountSort";

describe("account sort preference", () => {
  beforeEach(() => {
    localStorage.clear();
    vi.restoreAllMocks();
  });

  it("defaults to account ascending and persists a validated preference", () => {
    expect(readAccountSort()).toEqual(DEFAULT_ACCOUNT_SORT);
    expect(isDefaultAccountSort(DEFAULT_ACCOUNT_SORT)).toBe(true);

    writeAccountSort({ field: "disabled_at", order: "desc" });
    expect(readAccountSort()).toEqual({ field: "disabled_at", order: "desc" });
    expect(isDefaultAccountSort(readAccountSort())).toBe(false);
  });

  it("rejects malformed versions and unsupported sort values", () => {
    for (const raw of ["not-json", "[]", '{"version":2,"field":"usage","order":"desc"}']) {
      localStorage.setItem(ACCOUNT_SORT_STORAGE_KEY, raw);
      expect(readAccountSort()).toEqual(DEFAULT_ACCOUNT_SORT);
    }

    localStorage.setItem(ACCOUNT_SORT_STORAGE_KEY, JSON.stringify({ version: 1, field: "credential", order: "sideways" }));
    expect(readAccountSort()).toEqual(DEFAULT_ACCOUNT_SORT);
  });

  it("removes default storage and tolerates unavailable browser storage", () => {
    localStorage.setItem(ACCOUNT_SORT_STORAGE_KEY, "stale");
    writeAccountSort(DEFAULT_ACCOUNT_SORT);
    expect(localStorage.getItem(ACCOUNT_SORT_STORAGE_KEY)).toBeNull();

    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new Error("denied"); });
    expect(readAccountSort()).toEqual(DEFAULT_ACCOUNT_SORT);
    vi.restoreAllMocks();
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("denied"); });
    expect(() => writeAccountSort({ field: "priority", order: "desc" })).not.toThrow();
  });
});

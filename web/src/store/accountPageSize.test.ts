import { beforeEach, describe, expect, it } from "vitest";
import {
  ACCOUNT_PAGE_SIZE_STORAGE_KEY,
  DEFAULT_ACCOUNT_PAGE_SIZE,
  readAccountPageSize,
  writeAccountPageSize,
} from "./accountPageSize";

describe("account page-size preference", () => {
  beforeEach(() => localStorage.clear());

  it("defaults to 50 and restores each supported value", () => {
    expect(readAccountPageSize()).toBe(DEFAULT_ACCOUNT_PAGE_SIZE);
    for (const pageSize of [20, 50, 100, 200] as const) {
      writeAccountPageSize(pageSize);
      expect(readAccountPageSize()).toBe(pageSize);
    }
  });

  it("ignores malformed and unsupported stored values", () => {
    for (const stored of ["", "0", "75", "201", "not-a-number"]) {
      localStorage.setItem(ACCOUNT_PAGE_SIZE_STORAGE_KEY, stored);
      expect(readAccountPageSize()).toBe(DEFAULT_ACCOUNT_PAGE_SIZE);
    }
  });
});

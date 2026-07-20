import { beforeEach, describe, expect, it } from "vitest";
import {
  DEFAULT_INSPECTION_PAGE_SIZE,
  INSPECTION_PAGE_SIZE_STORAGE_KEY,
  readInspectionPageSize,
  writeInspectionPageSize,
} from "./inspectionPageSize";

describe("inspectionPageSize", () => {
  beforeEach(() => window.localStorage.clear());

  it("defaults to 50 and persists supported values", () => {
    expect(readInspectionPageSize()).toBe(DEFAULT_INSPECTION_PAGE_SIZE);
    writeInspectionPageSize(200);
    expect(readInspectionPageSize()).toBe(200);
  });

  it("rejects unsupported or malformed stored values", () => {
    window.localStorage.setItem(INSPECTION_PAGE_SIZE_STORAGE_KEY, "10000");
    expect(readInspectionPageSize()).toBe(DEFAULT_INSPECTION_PAGE_SIZE);
    window.localStorage.setItem(INSPECTION_PAGE_SIZE_STORAGE_KEY, "invalid");
    expect(readInspectionPageSize()).toBe(DEFAULT_INSPECTION_PAGE_SIZE);
  });
});

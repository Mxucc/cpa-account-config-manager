import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  DEFAULT_MANUAL_MODEL_TEST_MODEL,
  MANUAL_MODEL_TEST_STORAGE_KEY,
  normalizeManualModelTestModel,
  readManualModelTestPreference,
  recordManualModelTestModel,
} from "./manualModelTestModel";

describe("manual model-test preference", () => {
  beforeEach(() => localStorage.clear());
  afterEach(() => vi.restoreAllMocks());

  it("defaults to gpt-5.6-sol and restores the most recently tested model", () => {
    expect(readManualModelTestPreference("codex")).toEqual({
      model: DEFAULT_MANUAL_MODEL_TEST_MODEL,
      testedModels: [],
    });

    recordManualModelTestModel("codex", "gpt-5.5");
    recordManualModelTestModel("codex", "gpt-5.4");
    expect(readManualModelTestPreference("codex")).toEqual({
      model: "gpt-5.4",
      testedModels: ["gpt-5.4", "gpt-5.5"],
    });
  });

  it("normalizes, de-duplicates, and reorders tested models", () => {
    recordManualModelTestModel("codex-agent-identity", "  gpt-5.5\n");
    recordManualModelTestModel("codex", "gpt-5.4");
    recordManualModelTestModel("codex", "gpt-5.5");
    expect(readManualModelTestPreference("codex")).toEqual({
      model: "gpt-5.5",
      testedModels: ["gpt-5.5", "gpt-5.4"],
    });
  });

  it("keeps model histories isolated by provider", () => {
    recordManualModelTestModel("codex", "gpt-5.5");
    recordManualModelTestModel("gemini", "gemini-2.5-pro");
    expect(readManualModelTestPreference("codex").model).toBe("gpt-5.5");
    expect(readManualModelTestPreference("gemini").model).toBe("gemini-2.5-pro");
    expect(readManualModelTestPreference("claude", "claude-sonnet-4-5-20250929").model).toBe("claude-sonnet-4-5-20250929");
  });

  it("strips controls and rejects empty or excessive model IDs", () => {
    expect(normalizeManualModelTestModel("  gpt-5.5\u0000\n  ")).toBe("gpt-5.5");
    expect(normalizeManualModelTestModel("\u0000\n\t")).toBe("");
    expect(normalizeManualModelTestModel("x".repeat(129))).toBe("");
    recordManualModelTestModel("codex", "\u0000\n");
    expect(readManualModelTestPreference("codex").model).toBe(DEFAULT_MANUAL_MODEL_TEST_MODEL);
  });

  it("ignores malformed stored data and tolerates unavailable storage", () => {
    localStorage.setItem(MANUAL_MODEL_TEST_STORAGE_KEY, "not-json");
    expect(readManualModelTestPreference("codex").model).toBe(DEFAULT_MANUAL_MODEL_TEST_MODEL);

    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new Error("denied"); });
    expect(readManualModelTestPreference("codex").model).toBe(DEFAULT_MANUAL_MODEL_TEST_MODEL);
    vi.restoreAllMocks();

    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("denied"); });
    expect(recordManualModelTestModel("codex", "gpt-5.5")).toEqual({ model: "gpt-5.5", testedModels: ["gpt-5.5"] });
  });
});

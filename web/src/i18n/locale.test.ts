import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  CPA_LANGUAGE_STORAGE_KEY,
  detectHostLocale,
  normalizeHostLocale,
  parseStoredHostLocale,
  subscribeHostLocale,
} from "./locale";

describe("CPA host locale adapter", () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.lang = "zh-CN";
  });

  it("parses the official Zustand persistence shape and supported fallbacks", () => {
    expect(parseStoredHostLocale('{"state":{"language":"en"},"version":0}')).toBe("en");
    expect(parseStoredHostLocale('{"state":{"language":"zh-TW"},"version":0}')).toBe("zh-TW");
    expect(parseStoredHostLocale('{"language":"zh-CN"}')).toBe("zh-CN");
    expect(parseStoredHostLocale("en")).toBe("en");
    expect(parseStoredHostLocale("not-a-locale")).toBeNull();
    expect(normalizeHostLocale("ru")).toBe("ru");
  });

  it("prefers the CPA language key over document and browser language", () => {
    document.documentElement.lang = "zh-CN";
    localStorage.setItem(CPA_LANGUAGE_STORAGE_KEY, '{"state":{"language":"en"},"version":0}');
    expect(detectHostLocale()).toBe("en");
    localStorage.removeItem(CPA_LANGUAGE_STORAGE_KEY);
    expect(detectHostLocale()).toBe("zh-CN");
  });

  it("subscribes to live language changes from the same-origin CPA parent", () => {
    const callback = vi.fn();
    const unsubscribe = subscribeHostLocale(callback);
    window.dispatchEvent(new StorageEvent("storage", {
      key: CPA_LANGUAGE_STORAGE_KEY,
      newValue: '{"state":{"language":"en"},"version":0}',
    }));
    expect(callback).toHaveBeenCalledWith("en");
    unsubscribe();
    window.dispatchEvent(new StorageEvent("storage", {
      key: CPA_LANGUAGE_STORAGE_KEY,
      newValue: '{"state":{"language":"zh-CN"},"version":0}',
    }));
    expect(callback).toHaveBeenCalledTimes(1);
  });
});

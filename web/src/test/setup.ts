import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

afterEach(() => cleanup());

// Node 26 exposes an experimental process-level localStorage getter. It masks
// JSDOM's origin-bound storage in test modules and may be unavailable without a
// Node --localstorage-file flag. Use a deterministic browser-storage shim.
const storageItems = new Map<string, string>();
const testStorage: Storage = {
  get length() { return storageItems.size; },
  clear: () => storageItems.clear(),
  getItem: (key) => storageItems.get(String(key)) ?? null,
  key: (index) => [...storageItems.keys()][index] ?? null,
  removeItem: (key) => storageItems.delete(String(key)),
  setItem: (key, value) => { storageItems.set(String(key), String(value)); },
};
Object.defineProperty(window, "localStorage", { configurable: true, value: testStorage });
Object.defineProperty(globalThis, "localStorage", { configurable: true, value: testStorage });

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => undefined,
    removeListener: () => undefined,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
    dispatchEvent: () => false,
  }),
});

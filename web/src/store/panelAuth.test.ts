import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { readPanelAuth } from "./panelAuth";

const storageKey = "cli-proxy-auth";
const prefix = "enc::v1::";
const salt = "cli-proxy-api-webui::secure-storage";

function encodePanelState(apiBase: string, managementKey: string): string {
  const raw = JSON.stringify({ state: { apiBase, managementKey, rememberPassword: true }, version: 0 });
  const data = new TextEncoder().encode(raw);
  const key = new TextEncoder().encode(`${salt}|${window.location.host}|${navigator.userAgent}`);
  const encrypted = new Uint8Array(data.length);
  for (let index = 0; index < data.length; index += 1) {
    encrypted[index] = data[index] ^ key[index % key.length];
  }
  const binary = Array.from(encrypted, (value) => String.fromCharCode(value)).join("");
  return `${prefix}${btoa(binary)}`;
}

describe("official panel auth recovery", () => {
  const realSelf = window.self;
  const realTop = window.top;

  beforeEach(() => localStorage.clear());

  afterEach(() => {
    localStorage.clear();
    Object.defineProperty(window, "self", { value: realSelf, configurable: true });
    Object.defineProperty(window, "top", { value: realTop, configurable: true });
  });

  function setEmbedded(embedded: boolean) {
    Object.defineProperty(window, "self", { value: window, configurable: true });
    Object.defineProperty(window, "top", { value: embedded ? ({} as Window) : window, configurable: true });
  }

  it("does not read remembered credentials when opened directly", () => {
    setEmbedded(false);
    localStorage.setItem(storageKey, encodePanelState("http://127.0.0.1:8317", "test-key"));
    expect(readPanelAuth()).toBeNull();
  });

  it("reads same-origin credentials in an explicitly authorized standalone OAuth window", () => {
    setEmbedded(false);
    localStorage.setItem(storageKey, encodePanelState("http://127.0.0.1:8317", "test-key"));
    expect(readPanelAuth({ allowStandalone: true })).toEqual({ apiBase: "http://127.0.0.1:8317", managementKey: "test-key" });
    expect(readPanelAuth()).toBeNull();
  });

  it("recovers the official panel session only in a same-origin iframe", () => {
    setEmbedded(true);
    localStorage.setItem(storageKey, encodePanelState("http://127.0.0.1:8317", "test-key"));
    expect(readPanelAuth()).toEqual({ apiBase: "http://127.0.0.1:8317", managementKey: "test-key" });
  });

  it("fails closed for malformed or incomplete remembered state", () => {
    setEmbedded(true);
    localStorage.setItem(storageKey, "not-json");
    expect(readPanelAuth()).toBeNull();
    localStorage.setItem(storageKey, encodePanelState("http://127.0.0.1:8317", ""));
    expect(readPanelAuth()).toBeNull();
  });
});

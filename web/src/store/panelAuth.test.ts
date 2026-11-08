import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { readCenterAuth, readLegacyPanelAuth, readPanelAuth } from "./panelAuth";

const legacyStorageKey = "cli-proxy-auth";
const prefixV1 = "enc::v1::";
const prefixV2 = "enc::v2::";
const salt = "cli-proxy-api-webui::secure-storage";

function obfuscate(plain: string, prefix: string, withUserAgent: boolean): string {
  const data = new TextEncoder().encode(plain);
  const key = new TextEncoder().encode(
    withUserAgent ? `${salt}|${window.location.host}|${navigator.userAgent}` : `${salt}|${window.location.host}`,
  );
  const bytes = new Uint8Array(data.length);
  for (let index = 0; index < data.length; index += 1) bytes[index] = data[index] ^ key[index % key.length];
  return `${prefix}${btoa(Array.from(bytes, (value) => String.fromCharCode(value)).join(""))}`;
}

// The v8 Management Center writes each session field on its own key, as JSON,
// through its obfuscating storage helper.
function writeCenterSession(apiBase: string, managementKey: string, prefix = prefixV2) {
  localStorage.setItem("isLoggedIn", "true");
  localStorage.setItem("managementKey", obfuscate(JSON.stringify(managementKey), prefix, prefix === prefixV1));
  localStorage.setItem("apiBase", obfuscate(JSON.stringify(apiBase), prefix, prefix === prefixV1));
}

function encodeLegacyState(apiBase: string, managementKey: string): string {
  return obfuscate(
    JSON.stringify({ state: { apiBase, managementKey, rememberPassword: true }, version: 0 }),
    prefixV1,
    true,
  );
}

describe("panel session recovery", () => {
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

  it("signs in from the Management Center session without asking for a key", () => {
    setEmbedded(true);
    writeCenterSession("http://127.0.0.1:8317", "center-key");
    expect(readCenterAuth()).toEqual({ apiBase: "http://127.0.0.1:8317", managementKey: "center-key" });
    expect(readPanelAuth()).toEqual({ apiBase: "http://127.0.0.1:8317", managementKey: "center-key" });
  });

  it("reads a center session stored by the older v1 obfuscation", () => {
    setEmbedded(true);
    writeCenterSession("", "legacy-shaped-key", prefixV1);
    // An empty base means the center talks to its own origin, which serves the
    // plugin resource page too.
    expect(readCenterAuth()).toEqual({ apiBase: window.location.origin, managementKey: "legacy-shaped-key" });
  });

  it("reads an un-obfuscated center session", () => {
    setEmbedded(true);
    localStorage.setItem("isLoggedIn", "true");
    localStorage.setItem("managementKey", JSON.stringify("plain-key"));
    localStorage.setItem("apiBase", JSON.stringify("https://cpa.example"));
    expect(readCenterAuth()).toEqual({ apiBase: "https://cpa.example", managementKey: "plain-key" });
  });

  it("does not sign in from a stored key when the center is not logged in", () => {
    setEmbedded(true);
    localStorage.setItem("managementKey", obfuscate(JSON.stringify("stale-key"), prefixV2, false));
    localStorage.setItem("apiBase", obfuscate(JSON.stringify("https://old.example"), prefixV2, false));
    expect(readCenterAuth()).toBeNull();
    expect(readPanelAuth()).toBeNull();
  });

  it("still recovers the old web UI session in an embedded frame", () => {
    setEmbedded(true);
    localStorage.setItem(legacyStorageKey, encodeLegacyState("http://127.0.0.1:8317", "test-key"));
    expect(readPanelAuth()).toEqual({ apiBase: "http://127.0.0.1:8317", managementKey: "test-key" });
  });

  it("prefers the live center session over a stale legacy one", () => {
    setEmbedded(true);
    localStorage.setItem(legacyStorageKey, encodeLegacyState("http://old.example:8317", "old-key"));
    writeCenterSession("https://cpa.example", "center-key");
    expect(readPanelAuth()).toEqual({ apiBase: "https://cpa.example", managementKey: "center-key" });
  });

  it("does not read a remembered legacy credential when opened directly", () => {
    setEmbedded(false);
    localStorage.setItem(legacyStorageKey, encodeLegacyState("http://127.0.0.1:8317", "test-key"));
    expect(readLegacyPanelAuth()).toBeNull();
    // A standalone tab with no center session must still ask for the key.
    expect(readPanelAuth()).toBeNull();
  });

  it("reads a same-origin legacy credential only in an explicitly authorized window", () => {
    setEmbedded(false);
    localStorage.setItem(legacyStorageKey, encodeLegacyState("http://127.0.0.1:8317", "test-key"));
    expect(readLegacyPanelAuth({ allowStandalone: true })).toEqual({
      apiBase: "http://127.0.0.1:8317",
      managementKey: "test-key",
    });
  });

  it("fails closed for malformed or incomplete remembered state", () => {
    setEmbedded(true);
    localStorage.setItem(legacyStorageKey, "not-json");
    expect(readLegacyPanelAuth()).toBeNull();
    localStorage.setItem(legacyStorageKey, encodeLegacyState("http://127.0.0.1:8317", ""));
    expect(readLegacyPanelAuth()).toBeNull();
    localStorage.setItem("isLoggedIn", "true");
    localStorage.setItem("managementKey", "%%%not-base64%%%");
    expect(readCenterAuth()).toBeNull();
  });
});

const ENC_PREFIX_V1 = "enc::v1::";
const ENC_PREFIX_V2 = "enc::v2::";
const SECRET_SALT = "cli-proxy-api-webui::secure-storage";
// The old CLIProxyAPI web UI kept one remembered session object here.
const LEGACY_STORAGE_KEY = "cli-proxy-auth";
// The v8 Management Center stores its session as separate entries, written with
// its own obfuscation helper. The plugin resource page is served from the center
// itself, so those entries are readable from here.
const CENTER_KEY_STORAGE_KEY = "managementKey";
const CENTER_API_BASE_STORAGE_KEY = "apiBase";
const CENTER_API_URL_STORAGE_KEY = "apiUrl";
const CENTER_LOGGED_IN_STORAGE_KEY = "isLoggedIn";

export interface PanelAuth {
  apiBase: string;
  managementKey: string;
}

function isEmbedded(): boolean {
  try {
    return window.self !== window.top;
  } catch {
    return false;
  }
}

function xorBytes(data: Uint8Array, key: Uint8Array): Uint8Array {
  const output = new Uint8Array(data.length);
  for (let index = 0; index < data.length; index += 1) {
    output[index] = data[index] ^ key[index % key.length];
  }
  return output;
}

function latin1ToBytes(binary: string): Uint8Array {
  return Uint8Array.from(binary, (character) => character.charCodeAt(0));
}

// The center obfuscates stored values with the same marker scheme the older web
// UI used. v2 keys off the host only; v1 also keyed off the user agent, so both
// shapes must decode or a session stored before an upgrade would read as absent.
function decodeStoredValue(value: string): string {
  try {
    if (value.startsWith(ENC_PREFIX_V2)) {
      const key = new TextEncoder().encode(`${SECRET_SALT}|${window.location.host}`);
      return new TextDecoder().decode(xorBytes(latin1ToBytes(atob(value.slice(ENC_PREFIX_V2.length))), key));
    }
    if (value.startsWith(ENC_PREFIX_V1)) {
      const key = new TextEncoder().encode(`${SECRET_SALT}|${window.location.host}|${navigator.userAgent}`);
      return new TextDecoder().decode(xorBytes(latin1ToBytes(atob(value.slice(ENC_PREFIX_V1.length))), key));
    }
  } catch {
    return value;
  }
  return value;
}

// readStoredValue returns the string the center's own reader would return: the
// stored value is JSON, then obfuscated, so both steps are undone here.
function readStoredValue(key: string): string {
  let raw: string | null = null;
  try {
    raw = localStorage.getItem(key);
  } catch {
    return "";
  }
  if (raw === null || raw === "") return "";
  // The center always writes JSON, so a value that does not parse is not a
  // session field: returning it would let unrelated garbage act as a key.
  try {
    const parsed = JSON.parse(decodeStoredValue(raw)) as unknown;
    return typeof parsed === "string" ? parsed : "";
  } catch {
    return "";
  }
}

function readStoredFlag(key: string): boolean {
  try {
    return localStorage.getItem(key) === "true";
  } catch {
    return false;
  }
}

function extractPanelAuth(value: unknown): PanelAuth | null {
  if (!value || typeof value !== "object") return null;
  const root = value as Record<string, unknown>;
  const state = (root.state as Record<string, unknown> | undefined) ?? root;
  const apiBase = typeof state.apiBase === "string" ? state.apiBase.trim() : "";
  const managementKey = typeof state.managementKey === "string" ? state.managementKey.trim() : "";
  if (apiBase === "" || managementKey === "") return null;
  return { apiBase, managementKey };
}

// readCenterAuth recovers the session the v8 Management Center itself is using.
// The plugin page is opened inside that center, so asking the operator to type
// the same management key again would be a regression, not a security boundary.
// The logged-in marker is required: a stale key from a previous host must not
// sign the page in on its own.
export function readCenterAuth(): PanelAuth | null {
  if (!readStoredFlag(CENTER_LOGGED_IN_STORAGE_KEY)) return null;
  const managementKey = readStoredValue(CENTER_KEY_STORAGE_KEY).trim();
  if (managementKey === "") return null;
  // An empty base means the center talks to its own origin, which is exactly how
  // the plugin resource page is served.
  const apiBase =
    readStoredValue(CENTER_API_BASE_STORAGE_KEY).trim() || readStoredValue(CENTER_API_URL_STORAGE_KEY).trim();
  return { apiBase: apiBase === "" ? window.location.origin : apiBase, managementKey };
}

// readLegacyPanelAuth recovers the remembered session of the old web UI, which
// only ever trusted an embedded frame (or an explicitly authorized window).
export function readLegacyPanelAuth(options: { allowStandalone?: boolean } = {}): PanelAuth | null {
  if (!isEmbedded() && options.allowStandalone !== true) return null;
  try {
    const raw = localStorage.getItem(LEGACY_STORAGE_KEY);
    if (!raw) return null;
    return extractPanelAuth(JSON.parse(decodeStoredValue(raw)));
  } catch {
    return null;
  }
}

export function readPanelAuth(options: { allowStandalone?: boolean } = {}): PanelAuth | null {
  // The center's own session wins: it is the live one, while the legacy key can
  // be weeks old and point at another host.
  return readCenterAuth() ?? readLegacyPanelAuth(options);
}

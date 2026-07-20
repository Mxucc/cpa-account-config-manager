export interface PersistedAccountFilters {
  search: string;
  provider: string;
  type: string;
  status: string;
  disabled: string;
  editability: string;
}

interface StoredAccountFilters extends PersistedAccountFilters {
  version: 1;
}

export const ACCOUNT_FILTERS_STORAGE_KEY = "cpa-account-config-manager:account-filters";

export const EMPTY_ACCOUNT_FILTERS: PersistedAccountFilters = {
  search: "",
  provider: "",
  type: "",
  status: "",
  disabled: "",
  editability: "",
};

const statusValues = new Set(["", "active", "disabled", "error", "unavailable"]);
const disabledValues = new Set(["", "true", "false"]);
const editabilityValues = new Set(["", "editable", "read_only"]);

export function readAccountFilters(): PersistedAccountFilters {
  if (typeof window === "undefined") return { ...EMPTY_ACCOUNT_FILTERS };
  try {
    const raw = window.localStorage.getItem(ACCOUNT_FILTERS_STORAGE_KEY);
    if (!raw) return { ...EMPTY_ACCOUNT_FILTERS };
    const parsed: unknown = JSON.parse(raw);
    if (!isRecord(parsed) || parsed.version !== 1) return { ...EMPTY_ACCOUNT_FILTERS };
    return normalizeAccountFilters(parsed);
  } catch {
    return { ...EMPTY_ACCOUNT_FILTERS };
  }
}

export function writeAccountFilters(filters: PersistedAccountFilters): void {
  if (typeof window === "undefined") return;
  try {
    const normalized = normalizeAccountFilters(filters);
    if (Object.values(normalized).every((value) => value === "")) {
      window.localStorage.removeItem(ACCOUNT_FILTERS_STORAGE_KEY);
      return;
    }
    const stored: StoredAccountFilters = { version: 1, ...normalized };
    window.localStorage.setItem(ACCOUNT_FILTERS_STORAGE_KEY, JSON.stringify(stored));
  } catch {
    // Active filters still work when browser storage is unavailable.
  }
}

function normalizeAccountFilters(value: Record<string, unknown> | PersistedAccountFilters): PersistedAccountFilters {
  const status = safeString(value.status, 32).toLowerCase();
  const disabled = safeString(value.disabled, 8).toLowerCase();
  const editability = safeString(value.editability, 16).toLowerCase();
  return {
    search: safeString(value.search, 256),
    provider: safeString(value.provider, 80),
    type: safeString(value.type, 80),
    status: statusValues.has(status) ? status : "",
    disabled: disabledValues.has(disabled) ? disabled : "",
    editability: editabilityValues.has(editability) ? editability : "",
  };
}

function safeString(value: unknown, maxLength: number): string {
  if (typeof value !== "string") return "";
  const normalized = value.replace(/[\u0000-\u001f\u007f]/g, "").trim();
  return Array.from(normalized).slice(0, maxLength).join("");
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

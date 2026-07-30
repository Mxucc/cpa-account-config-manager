export const ACCOUNT_SORT_FIELDS = [
  "account",
  "provider",
  "type",
  "usage",
  "active_reset_count",
  "concurrency",
  "created_at",
  "disabled_at",
  "access",
  "status",
  "priority",
  "routing",
] as const;

export type AccountSortField = typeof ACCOUNT_SORT_FIELDS[number];
export type AccountSortOrder = "asc" | "desc";

export interface AccountSort {
  field: AccountSortField;
  order: AccountSortOrder;
}

interface StoredAccountSort extends AccountSort {
  version: 1;
}

export const DEFAULT_ACCOUNT_SORT: AccountSort = { field: "account", order: "asc" };
export const ACCOUNT_SORT_STORAGE_KEY = "cpa-account-config-manager:account-sort";

const accountSortFields = new Set<string>(ACCOUNT_SORT_FIELDS);

export function readAccountSort(): AccountSort {
  if (typeof window === "undefined") return { ...DEFAULT_ACCOUNT_SORT };
  try {
    const raw = window.localStorage.getItem(ACCOUNT_SORT_STORAGE_KEY);
    if (!raw) return { ...DEFAULT_ACCOUNT_SORT };
    const parsed: unknown = JSON.parse(raw);
    if (!isRecord(parsed) || parsed.version !== 1) return { ...DEFAULT_ACCOUNT_SORT };
    return normalizeAccountSort(parsed);
  } catch {
    return { ...DEFAULT_ACCOUNT_SORT };
  }
}

export function writeAccountSort(sort: AccountSort): void {
  if (typeof window === "undefined") return;
  try {
    const normalized = normalizeAccountSort(sort);
    if (isDefaultAccountSort(normalized)) {
      window.localStorage.removeItem(ACCOUNT_SORT_STORAGE_KEY);
      return;
    }
    const stored: StoredAccountSort = { version: 1, ...normalized };
    window.localStorage.setItem(ACCOUNT_SORT_STORAGE_KEY, JSON.stringify(stored));
  } catch {
    // The active sort still applies when browser storage is unavailable.
  }
}

export function isDefaultAccountSort(sort: AccountSort): boolean {
  return sort.field === DEFAULT_ACCOUNT_SORT.field && sort.order === DEFAULT_ACCOUNT_SORT.order;
}

function normalizeAccountSort(value: Record<string, unknown> | AccountSort): AccountSort {
  const field = typeof value.field === "string" && accountSortFields.has(value.field)
    ? value.field as AccountSortField
    : DEFAULT_ACCOUNT_SORT.field;
  const order = value.order === "desc" ? "desc" : "asc";
  return { field, order };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

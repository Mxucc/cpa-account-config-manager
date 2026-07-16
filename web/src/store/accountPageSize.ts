export const ACCOUNT_PAGE_SIZE_OPTIONS = [20, 50, 100, 200] as const;
export type AccountPageSize = typeof ACCOUNT_PAGE_SIZE_OPTIONS[number];

export const DEFAULT_ACCOUNT_PAGE_SIZE: AccountPageSize = 50;
export const ACCOUNT_PAGE_SIZE_STORAGE_KEY = "cpa-account-config-manager:account-page-size";

export function isAccountPageSize(value: number): value is AccountPageSize {
  return ACCOUNT_PAGE_SIZE_OPTIONS.some((option) => option === value);
}

export function readAccountPageSize(): AccountPageSize {
  if (typeof window === "undefined") return DEFAULT_ACCOUNT_PAGE_SIZE;
  try {
    const stored = window.localStorage.getItem(ACCOUNT_PAGE_SIZE_STORAGE_KEY);
    if (stored === null) return DEFAULT_ACCOUNT_PAGE_SIZE;
    const parsed = Number(stored);
    return isAccountPageSize(parsed) ? parsed : DEFAULT_ACCOUNT_PAGE_SIZE;
  } catch {
    return DEFAULT_ACCOUNT_PAGE_SIZE;
  }
}

export function writeAccountPageSize(pageSize: AccountPageSize): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(ACCOUNT_PAGE_SIZE_STORAGE_KEY, String(pageSize));
  } catch {
    // The active page size still applies when browser storage is unavailable.
  }
}

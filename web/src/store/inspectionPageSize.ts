export const INSPECTION_PAGE_SIZE_OPTIONS = [20, 50, 100, 200] as const;
export type InspectionPageSize = typeof INSPECTION_PAGE_SIZE_OPTIONS[number];

export const DEFAULT_INSPECTION_PAGE_SIZE: InspectionPageSize = 50;
export const INSPECTION_PAGE_SIZE_STORAGE_KEY = "cpa-account-config-manager:inspection-page-size";

export function isInspectionPageSize(value: number): value is InspectionPageSize {
  return INSPECTION_PAGE_SIZE_OPTIONS.some((option) => option === value);
}

export function readInspectionPageSize(): InspectionPageSize {
  if (typeof window === "undefined") return DEFAULT_INSPECTION_PAGE_SIZE;
  try {
    const stored = window.localStorage.getItem(INSPECTION_PAGE_SIZE_STORAGE_KEY);
    if (stored === null) return DEFAULT_INSPECTION_PAGE_SIZE;
    const parsed = Number(stored);
    return isInspectionPageSize(parsed) ? parsed : DEFAULT_INSPECTION_PAGE_SIZE;
  } catch {
    return DEFAULT_INSPECTION_PAGE_SIZE;
  }
}

export function writeInspectionPageSize(pageSize: InspectionPageSize): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(INSPECTION_PAGE_SIZE_STORAGE_KEY, String(pageSize));
  } catch {
    // The active page size still applies when browser storage is unavailable.
  }
}

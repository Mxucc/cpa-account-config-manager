export const FONT_SIZE_PRESETS = ["small", "medium", "large"] as const;
export type FontSizePreset = typeof FONT_SIZE_PRESETS[number];

export const DEFAULT_FONT_SIZE: FontSizePreset = "small";
export const FONT_SIZE_STORAGE_KEY = "cpa-account-config-manager:font-size";

const fontSizePresets = new Set<string>(FONT_SIZE_PRESETS);

export function isFontSizePreset(value: unknown): value is FontSizePreset {
  return typeof value === "string" && fontSizePresets.has(value);
}

export function readFontSize(): FontSizePreset {
  if (typeof window === "undefined") return DEFAULT_FONT_SIZE;
  try {
    const stored = window.localStorage.getItem(FONT_SIZE_STORAGE_KEY);
    return isFontSizePreset(stored) ? stored : DEFAULT_FONT_SIZE;
  } catch {
    return DEFAULT_FONT_SIZE;
  }
}

export function applyFontSize(preset: FontSizePreset): void {
  if (typeof document === "undefined") return;
  if (preset === DEFAULT_FONT_SIZE) document.documentElement.removeAttribute("data-font-size");
  else document.documentElement.setAttribute("data-font-size", preset);
}

export function writeFontSize(preset: FontSizePreset): void {
  applyFontSize(preset);
  if (typeof window === "undefined") return;
  try {
    if (preset === DEFAULT_FONT_SIZE) window.localStorage.removeItem(FONT_SIZE_STORAGE_KEY);
    else window.localStorage.setItem(FONT_SIZE_STORAGE_KEY, preset);
  } catch {
    // The selected typography remains active when browser storage is unavailable.
  }
}

export function initFontSize(): void {
  applyFontSize(readFontSize());
}

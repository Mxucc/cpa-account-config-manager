export type PluginThemePreset = "neutral" | "indigo" | "forest" | "rose";
export type PluginDensity = "comfortable" | "compact";

const THEME_KEY = "cpa_account_config_manager.plugin_theme";
const DENSITY_KEY = "cpa_account_config_manager.plugin_density";
const ENABLED_KEY = "cpa_account_config_manager.plugin_theme_enabled";

function readString(key: string, fallback: string): string {
  try { return localStorage.getItem(key) || fallback; } catch { return fallback; }
}

export function readPluginTheme(): PluginThemePreset {
  const value = readString(THEME_KEY, "neutral");
  return value === "indigo" || value === "forest" || value === "rose" ? value : "neutral";
}

export function readPluginDensity(): PluginDensity {
  return readString(DENSITY_KEY, "comfortable") === "compact" ? "compact" : "comfortable";
}

export function readPluginThemeEnabled(): boolean {
  return readString(ENABLED_KEY, "true") !== "false";
}

function apply(): void {
  const root = document.documentElement;
  const enabled = readPluginThemeEnabled();
  root.toggleAttribute("data-plugin-theme-enabled", enabled);
  root.setAttribute("data-plugin-theme", enabled ? readPluginTheme() : "neutral");
  root.setAttribute("data-plugin-density", readPluginDensity());
}

export function setPluginTheme(value: PluginThemePreset): void {
  try { localStorage.setItem(THEME_KEY, value); } catch { /* best effort */ }
  apply();
}

export function setPluginDensity(value: PluginDensity): void {
  try { localStorage.setItem(DENSITY_KEY, value); } catch { /* best effort */ }
  apply();
}

export function setPluginThemeEnabled(value: boolean): void {
  try { localStorage.setItem(ENABLED_KEY, String(value)); } catch { /* best effort */ }
  apply();
}

export function resetPluginTheme(): void {
  try { localStorage.removeItem(THEME_KEY); localStorage.removeItem(DENSITY_KEY); localStorage.removeItem(ENABLED_KEY); } catch { /* best effort */ }
  apply();
}

export function initPluginTheme(): void { apply(); }

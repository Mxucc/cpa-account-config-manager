import type { Locale } from "./locale";
import type { TranslationValues } from "./messages";
import { enUI } from "./uiCatalogEn";
import { ruUI } from "./uiCatalogRu";
import { zhCNUI } from "./uiCatalogZhCN";
import { zhTWUI } from "./uiCatalogZhTW";

export type UIMessageKey = keyof typeof enUI;

const uiCatalogs: Record<Locale, Record<UIMessageKey, string>> = {
  "zh-CN": zhCNUI,
  "zh-TW": zhTWUI,
  en: enUI,
  ru: ruUI,
};

function interpolate(template: string, values: TranslationValues): string {
  return template.replace(/\{([a-z_]+)\}/gi, (_, name: string) => String(values[name] ?? `{${name}}`));
}

export function translateUI(locale: Locale, key: UIMessageKey, values: TranslationValues = {}): string {
  return interpolate(uiCatalogs[locale][key], values);
}

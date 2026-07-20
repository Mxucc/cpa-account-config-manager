export const CPA_LANGUAGE_STORAGE_KEY = "cli-proxy-language";

export const supportedLocales = ["zh-CN", "zh-TW", "en", "ru"] as const;

export type Locale = (typeof supportedLocales)[number];

export interface LocaleFormat {
  dateTimeLocale: string;
  hour12: boolean;
}

export const localeFormats: Record<Locale, LocaleFormat> = {
  "zh-CN": { dateTimeLocale: "zh-CN", hour12: false },
  "zh-TW": { dateTimeLocale: "zh-TW", hour12: false },
  en: { dateTimeLocale: "en", hour12: true },
  ru: { dateTimeLocale: "ru", hour12: false },
};

export function normalizeHostLocale(value: unknown): Locale | null {
  if (typeof value !== "string") return null;
  const normalized = value.trim().toLowerCase();
  if (normalized === "zh-tw" || normalized === "zh-hk" || normalized === "zh-hant") return "zh-TW";
  if (normalized === "zh-cn" || normalized === "zh-sg" || normalized === "zh-hans" || normalized === "zh") return "zh-CN";
  if (normalized === "en" || normalized.startsWith("en-")) return "en";
  if (normalized === "ru" || normalized.startsWith("ru-")) return "ru";
  return null;
}

export function parseStoredHostLocale(value: string | null): Locale | null {
  if (!value) return null;
  try {
    const parsed = JSON.parse(value) as unknown;
    if (typeof parsed === "string") return normalizeHostLocale(parsed);
    if (parsed && typeof parsed === "object") {
      const record = parsed as { language?: unknown; state?: { language?: unknown } };
      return normalizeHostLocale(record.state?.language ?? record.language);
    }
  } catch {
    return normalizeHostLocale(value);
  }
  return null;
}

export function detectHostLocale(target: Window = window): Locale {
  try {
    const stored = parseStoredHostLocale(target.localStorage.getItem(CPA_LANGUAGE_STORAGE_KEY));
    if (stored) return stored;
  } catch {
    // Storage can be unavailable in hardened or cross-origin deployments.
  }
  const documentLocale = normalizeHostLocale(target.document.documentElement.lang);
  if (documentLocale) return documentLocale;
  const browserLocale = normalizeHostLocale(target.navigator.languages?.[0] || target.navigator.language);
  return browserLocale ?? "zh-CN";
}

export function subscribeHostLocale(callback: (locale: Locale) => void, target: Window = window): () => void {
  const onStorage = (event: StorageEvent) => {
    if (event.key !== CPA_LANGUAGE_STORAGE_KEY) return;
    callback(parseStoredHostLocale(event.newValue) ?? detectHostLocale(target));
  };
  const onLanguageChange = () => callback(detectHostLocale(target));
  target.addEventListener("storage", onStorage);
  target.addEventListener("languagechange", onLanguageChange);
  return () => {
    target.removeEventListener("storage", onStorage);
    target.removeEventListener("languagechange", onLanguageChange);
  };
}

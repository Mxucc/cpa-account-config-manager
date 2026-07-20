import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { detectHostLocale, localeFormats, subscribeHostLocale, type Locale } from "./locale";
import { translate, type MessageKey, type TranslationValues } from "./messages";
import { translateUI, type UIMessageKey } from "./uiText";

interface I18nValue {
  locale: Locale;
  t: (key: MessageKey, values?: TranslationValues) => string;
  tx: (key: UIMessageKey, values?: TranslationValues) => string;
  formatDateTime: (value?: string | Date) => string;
  formatNumber: (value: number) => string;
}

function formatDateTimeForLocale(locale: Locale, value?: string | Date): string {
  if (!value) return "-";
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  const format = localeFormats[locale];
  return new Intl.DateTimeFormat(format.dateTimeLocale, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: format.hour12,
  }).format(date);
}

function formatDaysHoursForLocale(locale: Locale, days: number, hours: number): string {
  const targetLocale = localeFormats[locale].dateTimeLocale;
  const dayPart = new Intl.NumberFormat(targetLocale, { style: "unit", unit: "day", unitDisplay: "long" }).format(days);
  const hourPart = new Intl.NumberFormat(targetLocale, { style: "unit", unit: "hour", unitDisplay: "long" }).format(hours);
  return `${dayPart} ${hourPart}`;
}

const fallback: I18nValue = {
  locale: "zh-CN",
  t: (key, values) => translate("zh-CN", key, values),
  tx: (source, values) => translateUI("zh-CN", source, values),
  formatDateTime: (value) => formatDateTimeForLocale("zh-CN", value),
  formatNumber: (value) => new Intl.NumberFormat(localeFormats["zh-CN"].dateTimeLocale).format(value),
};

const I18nContext = createContext<I18nValue>(fallback);

export function I18nProvider({ children }: { children: ReactNode }) {
  const [locale, setLocale] = useState<Locale>(detectHostLocale);

  useEffect(() => subscribeHostLocale(setLocale), []);
  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);

  const value = useMemo<I18nValue>(() => ({
    locale,
    t: (key, values) => translate(locale, key, values),
    tx: (source, values) => translateUI(locale, source, values),
    formatDateTime: (input) => formatDateTimeForLocale(locale, input),
    formatNumber: (input) => new Intl.NumberFormat(localeFormats[locale].dateTimeLocale).format(input),
  }), [locale]);

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n(): I18nValue {
  return useContext(I18nContext);
}

export { formatDateTimeForLocale, formatDaysHoursForLocale };

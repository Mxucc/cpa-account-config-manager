import { localeFormats, type Locale } from "../i18n";

/**
 * A count an operator reads at a glance: thousands collapse into a compact form ("12.3K"), while
 * anything smaller stays exact so a small total is never rounded into a number that looks wrong.
 * A missing or unusable value counts as zero rather than printing "NaN".
 */
export function formatCompactNumber(value: number, locale: Locale): string {
  const number = Number(value);
  const normalized = Number.isFinite(number) && number > 0 ? number : 0;
  return new Intl.NumberFormat(localeFormats[locale].dateTimeLocale, {
    notation: normalized >= 1000 ? "compact" : "standard",
    maximumFractionDigits: 1,
  }).format(normalized);
}

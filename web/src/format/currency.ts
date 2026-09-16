import { localeFormats, type Locale } from "../i18n";

/**
 * Formats accumulated USD charges without rounding a positive nano-dollar amount to $0.
 * Credit accounting is persisted in nano-USD, so nine decimal places preserve all
 * precision available from the backend while larger totals stay compact.
 */
export function formatCreditUSD(value: number, locale: Locale): string {
  const normalized = Number.isFinite(value) ? Math.max(0, value) : 0;
  const maximumFractionDigits = normalized === 0
    ? 0
    : normalized < 0.000001
      ? 9
      : normalized < 0.01
        ? 8
        : 4;
  return new Intl.NumberFormat(localeFormats[locale].dateTimeLocale, {
    style: "currency",
    currency: "USD",
    currencyDisplay: "narrowSymbol",
    minimumFractionDigits: 0,
    maximumFractionDigits,
  }).format(normalized);
}

/**
 * A reference-priced amount with the locale's number formatting. These figures are list prices in
 * US dollars, not charges, so they reuse the number formatter instead of the currency one.
 */
export function formatReferenceUSD(value: number | undefined, formatNumber: (value: number) => string): string {
  if (typeof value !== "number" || Number.isNaN(value)) return "-";
  return `$${formatNumber(value)}`;
}

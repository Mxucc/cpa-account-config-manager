import type { useI18n } from "../i18n";

type Translate = ReturnType<typeof useI18n>["tx"];

/**
 * A quota reset countdown in words instead of a raw minute count: "6256 min" cannot be read at a
 * glance, while the same value as "4 d 8 h" can. The largest two units are enough, so a window
 * under an hour shows minutes, under a day hours and minutes, and beyond that days and hours.
 */
export function formatResetDuration(seconds: number, tx: Translate): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return "-";
  const totalMinutes = Math.ceil(seconds / 60);
  if (totalMinutes < 60) {
    return tx("ui.duration_minutes", { minutes: String(totalMinutes) });
  }
  const totalHours = Math.floor(totalMinutes / 60);
  if (totalHours < 24) {
    const minutes = totalMinutes % 60;
    return minutes > 0
      ? tx("ui.duration_hours_minutes", { hours: String(totalHours), minutes: String(minutes) })
      : tx("ui.duration_hours", { hours: String(totalHours) });
  }
  const days = Math.floor(totalHours / 24);
  const hours = totalHours % 24;
  return hours > 0
    ? tx("ui.duration_days_hours", { days: String(days), hours: String(hours) })
    : tx("ui.duration_days", { days: String(days) });
}

/** The percentage of a quota window, clamped so a bad upstream value cannot break a bar. */
export function quotaPercent(value: number | undefined): number {
  if (typeof value !== "number" || !Number.isFinite(value)) return 0;
  return Math.min(100, Math.max(0, value));
}

import { useCallback, useEffect, useMemo, useState } from "react";
import { Badge, Progress, type Tone } from "../ui/kit";
import type { ProviderAccount, ProviderStatus, QuotaWindow } from "./types";
import { copyFor } from "./copy";
import { useI18n } from "../i18n";

export function useCopy() {
  const { locale } = useI18n();
  return useMemo(() => copyFor(locale), [locale]);
}

export interface AsyncState<T> {
  data: T | null;
  loading: boolean;
  error: string;
  reload: () => void;
}

// One loader per page, with an explicit error string and a manual reload. Pages
// never render a spinner forever: a failed request leaves the error visible.
export function useAsync<T>(loader: () => Promise<T>, deps: unknown[]): AsyncState<T> {
  const [data, setData] = useState<T | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);

  const reload = useCallback(() => setRevision((value) => value + 1), []);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError("");
    loader()
      .then((value) => {
        if (!cancelled) setData(value);
      })
      .catch((caught: unknown) => {
        if (!cancelled) setError(caught instanceof Error ? caught.message : String(caught));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, revision]);

  return { data, loading, error, reload };
}

export function statusTone(status: ProviderStatus | string): Tone {
  switch (status) {
    case "ready":
      return "green";
    case "quota_limited":
      return "amber";
    case "credential_invalid":
      return "red";
    case "repairing":
      return "blue";
    default:
      return "gray";
  }
}

export function StatusBadge({ status, note }: { status: ProviderStatus | string; note?: string }) {
  const copy = useCopy();
  const label = (copy.status as Record<string, string>)[status] ?? status;
  return (
    <span className="inline-flex items-center gap-2">
      <Badge tone={statusTone(status)}>{label}</Badge>
      {note ? <span className="text-xs text-gray-500">{note}</span> : null}
    </span>
  );
}

// usedPercent is authoritative when present. A wallet window has no share, so it
// renders its balance instead of a meter — never a 0% bar.
export function quotaUsedPercent(window: QuotaWindow): number | null {
  if (typeof window.used_percent === "number") return window.used_percent;
  if (typeof window.remaining_percent === "number") return 100 - window.remaining_percent;
  return null;
}

export function quotaTone(window: QuotaWindow): Tone {
  const used = quotaUsedPercent(window);
  if (used === null) return "gray";
  if (used >= 95) return "red";
  if (used >= 80) return "amber";
  return "blue";
}

export function QuotaBar({ window }: { window: QuotaWindow }) {
  const copy = useCopy();
  const used = quotaUsedPercent(window);
  const { formatDateTime } = useI18n();
  if (window.balance != null && used === null) {
    return (
      <div className="flex items-center justify-between gap-3 text-sm">
        <span className="text-gray-600">{window.label}</span>
        <span className="font-medium text-gray-900">
          {window.balance} {window.unit ?? ""}
        </span>
      </div>
    );
  }
  return (
    <div className="space-y-1">
      <div className="flex items-center justify-between gap-3 text-xs">
        <span className="text-gray-600">{window.label}</span>
        <span className={used === null ? "text-gray-400" : "font-medium text-gray-900"}>
          {used === null ? copy.common.notReported : `${Math.round(used)}%`}
        </span>
      </div>
      {/* An unreported share draws no meter at all: an empty bar would read as 0%. */}
      {used === null ? null : <Progress value={used} tone={quotaTone(window)} />}
      <div className="flex items-center justify-between gap-3 text-xs text-gray-500">
        <span>
          {typeof window.limit === "number" ? `${copy.quota.limit} ${window.limit} ${window.unit ?? ""}` : ""}
        </span>
        <span>{window.reset_at ? `${copy.quota.reset} ${formatDateTime(window.reset_at)}` : ""}</span>
      </div>
      {window.note ? <p className="text-xs text-gray-400">{window.note}</p> : null}
    </div>
  );
}

export function AccountQuota({ account, compact = false }: { account: ProviderAccount; compact?: boolean }) {
  const copy = useCopy();
  if (account.quota.length === 0) {
    return <span className="text-xs text-gray-400">{copy.common.notReported}</span>;
  }
  const windows = compact ? account.quota.slice(0, 2) : account.quota;
  return (
    <div className={compact ? "w-44 space-y-2" : "space-y-4"}>
      {windows.map((window) => (
        <QuotaBar key={window.id} window={window} />
      ))}
    </div>
  );
}

export function formatMoney(value: number | null | undefined, currency = "USD"): string {
  if (value === null || value === undefined) return "-";
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency, maximumFractionDigits: 2 }).format(value);
  } catch {
    return `${value} ${currency}`;
  }
}

export function billingLabel(mode: string): string {
  switch (mode) {
    case "subscription":
      return "subscription";
    case "metered":
      return "metered";
    case "wallet":
      return "wallet";
    default:
      return "unknown";
  }
}

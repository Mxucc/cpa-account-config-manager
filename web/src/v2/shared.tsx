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


// A capability key is a wire identifier; the page shows its own word for it.
export function capabilityLabel(key: string, copy: ReturnType<typeof useCopy>): string {
  const labels = copy.capabilities as Record<string, string>;
  return labels[key] ?? key;
}

// A provider kind is a wire enum, so the page supplies its label.
export function kindLabel(kind: string, copy: ReturnType<typeof useCopy>): string {
  switch (kind) {
    case "oauth":
      return copy.kinds.oauth;
    case "subscription":
      return copy.kinds.subscription;
    case "wallet":
      return copy.kinds.wallet;
    case "channel":
      return copy.kinds.channel;
    default:
      return copy.common.unknown;
  }
}

// Status notes are fixed English sentences from the wire. The page owns the ones
// it knows and shows any other note (an imported plugin warning) unchanged.
export function statusNoteLabel(note: string, copy: ReturnType<typeof useCopy>): string {
  switch (note) {
    case "The gateway limited this account; it recovers after the window ends":
      return copy.statusNotes.quotaLimited;
    case "The gateway rejected this credential; automatic repair is running":
      return copy.statusNotes.credentialRejected;
    default:
      return note;
  }
}
export function StatusBadge({ status, note }: { status: ProviderStatus | string; note?: string }) {
  const copy = useCopy();
  const label = (copy.status as Record<string, string>)[status] ?? status;
  return (
    <span className="inline-flex items-center gap-2">
      <Badge tone={statusTone(status)}>{label}</Badge>
      {note ? <span className="text-xs text-gray-500">{statusNoteLabel(note, copy)}</span> : null}
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

// A limit or a balance is a display value. The vendor's float noise
// (1.9980000000000002 USD) must never reach the page, and a currency unit
// formats as money rather than a bare number.
export function formatAmount(value: number, unit?: string): string {
  if (unit && /^[A-Z]{3}$/.test(unit)) return formatMoney(value, unit);
  return new Intl.NumberFormat(undefined, { maximumFractionDigits: 2 }).format(value);
}

// Known vendor notes carry a caveat the page must not hide. The wire value is a
// fixed English sentence, so the page maps the ones it knows to its own language
// and shows anything else unchanged.
export function noteLabel(note: string, copy: ReturnType<typeof useCopy>): string {
  if (note === "Reference-priced usage") return copy.quota.notes.referencePricedUsage;
  return note;
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
          {formatAmount(window.balance, window.unit)}
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
          {typeof window.limit === "number" ? `${copy.quota.limit} ${formatAmount(window.limit, window.unit)}` : ""}
        </span>
        <span>{window.reset_at ? `${copy.quota.reset} ${formatDateTime(window.reset_at)}` : ""}</span>
      </div>
      {window.note ? <p className="text-xs text-gray-400">{noteLabel(window.note, copy)}</p> : null}
    </div>
  );
}

// accountWindows is the single read of the quota list. The API answers [] when a
// provider reported nothing, and a null from an older or third-party source must
// not crash the page.
export function accountWindows(account: ProviderAccount): QuotaWindow[] {
  return account.quota ?? [];
}

export function AccountQuota({ account, compact = false }: { account: ProviderAccount; compact?: boolean }) {
  const copy = useCopy();
  const all = accountWindows(account);
  if (all.length === 0) {
    return <span className="text-xs text-gray-400">{copy.common.notReported}</span>;
  }
  const windows = compact ? all.slice(0, 2) : all;
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

// Billing mode is a wire enum, so the page owns its words. A badge that reads
// "subscription" in a Chinese shell is a missing label, not a vendor value.
export function billingLabel(mode: string, copy: ReturnType<typeof useCopy>): string {
  switch (mode) {
    case "subscription":
      return copy.usage.subscription;
    case "metered":
      return copy.usage.metered;
    case "wallet":
      return copy.usage.wallet;
    default:
      return copy.common.unknown;
  }
}

// A cycle kind is normalized by the server, so the page supplies its label.
export function cycleLabel(kind: string, copy: ReturnType<typeof useCopy>): string {
  switch (kind) {
    case "monthly":
      return copy.cycle.monthly;
    case "weekly":
      return copy.cycle.weekly;
    case "rolling":
      return copy.cycle.rolling;
    case "none":
      return copy.cycle.none;
    default:
      return copy.common.unknown;
  }
}

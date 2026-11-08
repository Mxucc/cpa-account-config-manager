import { useEffect, useRef } from "react";
import { Badge, Button, Card, CardTitle, Dialog, EmptyState, ErrorState, Input, Progress, Select, Skeleton } from "../../../ui/kit";
import type { ReactNode } from "react";
import type { Tone } from "../../../ui/kit";

// Shared building blocks for the rebuilt automation and settings tabs. They are
// deliberately small: the design system already owns colour and spacing, so a tab
// only composes these and never restates a class.

export function Metric({ label, value }: { label: string; value: number | string }) {
  return (
    <div className="rounded-lg border border-gray-200 bg-gray-50 px-3 py-2">
      <p className="text-xs text-gray-500">{label}</p>
      <p className="mt-0.5 font-semibold text-gray-900">{value}</p>
    </div>
  );
}

export function LabeledField({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label className="block text-sm font-medium text-gray-700">
      {label}
      <div className="mt-1">{children}</div>
      {hint ? <span className="mt-1 block text-xs text-gray-400">{hint}</span> : null}
    </label>
  );
}

export function Toggle({
  label,
  checked,
  onChange,
  disabled,
}: {
  label: string;
  checked: boolean;
  onChange: (value: boolean) => void;
  disabled?: boolean;
}) {
  return (
    <label className="flex items-center gap-2 text-sm text-gray-700">
      <input
        type="checkbox"
        checked={checked}
        disabled={disabled}
        onChange={(event) => onChange(event.target.checked)}
        className="h-4 w-4 rounded border-gray-300 text-blue-600 focus:ring-2 focus:ring-blue-500 focus:ring-offset-2"
      />
      {label}
    </label>
  );
}

// usePolling runs one refresh loop at a time and clears it on unmount, so a
// long-running job cannot leave a timer hitting the host after its tab closes.
export function usePolling(active: boolean, run: () => Promise<void>, intervalMs = 2000) {
  const runRef = useRef(run);
  runRef.current = run;
  useEffect(() => {
    if (!active) return;
    let cancelled = false;
    const timer = window.setInterval(() => {
      if (cancelled) return;
      void runRef.current().catch(() => {
        // A failed poll leaves the last known state on screen.
      });
    }, intervalMs);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [active, intervalMs]);
}

// A host answer this page cannot read is shown as an error, never as a white
// screen: the pages below check the field they depend on before rendering it.
export function hasFields(value: unknown, fields: string[]): boolean {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const record = value as Record<string, unknown>;
  return fields.every((field) => record[field] !== undefined && record[field] !== null);
}

export { Badge, Button, Card, CardTitle, Dialog, EmptyState, ErrorState, Input, Progress, Select, Skeleton };
export type { Tone };

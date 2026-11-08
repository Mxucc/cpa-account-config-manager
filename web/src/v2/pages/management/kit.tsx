import { useEffect, useRef } from "react";
import { Badge, Button, Card, CardTitle, Dialog, EmptyState, ErrorState, Field, Input, Progress, Select, Skeleton } from "../../../ui/kit";
import type { ReactNode } from "react";
import type { Tone } from "../../../ui/kit";

// Shared building blocks for the rebuilt automation and settings tabs. They are
// deliberately small: the design system already owns colour and spacing, so a tab
// only composes these and never restates a class.

// The metric tile is the shared one: same border, padding and type scale on
// every tab, so a number never sits in a box that looks like another page's.
export { Metric } from "../../../ui/kit";

// One labelled field shape for every form on the automation and settings pages.
export function LabeledField({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return <Field label={label} hint={hint}>{children}</Field>;
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
        className="h-4 w-4 rounded border-gray-300 text-blue-600 focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2"
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

export { Badge, Button, Card, CardTitle, Dialog, EmptyState, ErrorState, Field, Input, Metric as MetricTile, Notice, Progress, Select, Skeleton } from "../../../ui/kit";
export type { Tone };

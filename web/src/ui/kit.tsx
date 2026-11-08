import { useEffect } from "react";
import { clsx } from "clsx";
import type { ButtonHTMLAttributes, InputHTMLAttributes, ReactNode, SelectHTMLAttributes } from "react";
import { Loader2, X } from "lucide-react";
import { useI18n } from "../i18n";

export const cn = (...values: Array<string | false | null | undefined>) => clsx(values);

// Corporate Clean primitives.
//
// Every page composes these, so a card, a field, a table and a metric look the
// same on every screen. The spacing and colour values live in v2.css as tokens;
// this file only names them. Anything that would otherwise be re-typed in a page
// belongs here, which is what keeps the pages from drifting apart.
const buttonBase =
  "inline-flex items-center justify-center gap-2 rounded-lg font-medium transition-all duration-150 ease-out shadow-sm " +
  "hover:shadow active:scale-[0.98] active:translate-y-0 active:shadow-sm " +
  "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-blue-500 focus-visible:ring-offset-2 " +
  // A disabled control reads as unavailable, not as a washed-out primary action:
  // the pale-blue save button looked like a rendering bug.
  "disabled:pointer-events-none disabled:bg-gray-100 disabled:text-gray-400 disabled:border-gray-200 disabled:shadow-none";

const variants = {
  primary: "bg-blue-600 text-white hover:bg-blue-700 border border-blue-600 hover:border-blue-700",
  secondary: "bg-white text-gray-700 border border-gray-300 hover:bg-gray-50",
  ghost: "bg-transparent text-gray-600 shadow-none border border-transparent hover:bg-gray-100 hover:shadow-none",
  danger: "bg-red-600 text-white hover:bg-red-700 border border-red-600 hover:border-red-700",
} as const;

const sizes = {
  sm: "h-8 px-3 text-xs",
  md: "h-9 px-3.5 text-sm",
} as const;

export function Button({
  variant = "primary",
  size = "md",
  loading = false,
  className,
  children,
  disabled,
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: keyof typeof variants;
  size?: keyof typeof sizes;
  loading?: boolean;
}) {
  return (
    <button
      {...rest}
      disabled={disabled || loading}
      className={cn(buttonBase, variants[variant], sizes[size], className)}
    >
      {loading ? <Loader2 size={14} className="animate-spin" aria-hidden="true" /> : null}
      {children}
    </button>
  );
}

export function Card({
  className,
  children,
  interactive = false,
  flush = false,
}: {
  className?: string;
  children: ReactNode;
  interactive?: boolean;
  flush?: boolean;
}) {
  return (
    <div
      className={cn(
        "v2-card",
        flush && "v2-card-flush",
        interactive && "transition-all duration-200 ease-out hover:-translate-y-0.5 hover:shadow-md",
        className,
      )}
    >
      {children}
    </div>
  );
}

export function CardTitle({ children, className }: { children: ReactNode; className?: string }) {
  return <h2 className={cn("v2-section-title", className)}>{children}</h2>;
}

// Section is the one card-with-a-heading shape: title, optional subtitle, an
// actions slot aligned to the right, and a consistent vertical rhythm.
export function Section({
  title,
  description,
  actions,
  children,
  className,
}: {
  title?: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <Card className={cn("v2-section", className)}>
      {title || actions ? (
        <div className="v2-section-head">
          <div className="min-w-0">
            {title ? <h2 className="v2-section-title">{title}</h2> : null}
            {description ? <p className="v2-section-sub">{description}</p> : null}
          </div>
          {actions ? <div className="flex flex-wrap items-center gap-2">{actions}</div> : null}
        </div>
      ) : null}
      {children}
    </Card>
  );
}

export type Tone = "blue" | "green" | "amber" | "red" | "gray";

const tones: Record<Tone, string> = {
  blue: "bg-blue-50 text-blue-700 border-blue-200",
  green: "bg-green-50 text-green-700 border-green-200",
  amber: "bg-amber-50 text-amber-700 border-amber-200",
  red: "bg-red-50 text-red-700 border-red-200",
  gray: "bg-gray-50 text-gray-600 border-gray-200",
};

export function Badge({ tone = "gray", children, className }: { tone?: Tone; children: ReactNode; className?: string }) {
  return (
    <span className={cn("inline-flex items-center gap-1 whitespace-nowrap rounded-md border px-2 py-0.5 text-xs font-medium", tones[tone], className)}>
      {children}
    </span>
  );
}

// A capability chip is not a control: it never takes a button's weight.
export function Chip({ children }: { children: ReactNode }) {
  return <span className="v2-chip">{children}</span>;
}

export function Input({ className, ...rest }: InputHTMLAttributes<HTMLInputElement>) {
  return (
    <input
      {...rest}
      className={cn(
        "h-9 w-full rounded-lg border border-gray-300 bg-white px-3 text-sm text-gray-900 placeholder:text-gray-400",
        "focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 focus:border-blue-500",
        className,
      )}
    />
  );
}

export function Select({ className, children, ...rest }: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select
      {...rest}
      className={cn(
        "h-9 rounded-lg border border-gray-300 bg-white px-3 text-sm text-gray-900",
        "focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 focus:border-blue-500",
        className,
      )}
    >
      {children}
    </select>
  );
}

export function Field({
  label,
  hint,
  htmlFor,
  children,
  className,
}: {
  label: ReactNode;
  hint?: ReactNode;
  htmlFor?: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("v2-field", className)}>
      <label className="v2-label" htmlFor={htmlFor}>
        {label}
      </label>
      {children}
      {hint ? <p className="v2-hint">{hint}</p> : null}
    </div>
  );
}

export function FieldGrid({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn("v2-grid-fields", className)}>{children}</div>;
}

// Fields that need the whole row (a URL, a long note) say so once.
export function FieldWide({ children }: { children: ReactNode }) {
  return <div className="sm:col-span-2 xl:col-span-3">{children}</div>;
}

export function Metric({ label, value, hint }: { label: ReactNode; value: ReactNode; hint?: ReactNode }) {
  return (
    <div className="v2-metric">
      <p className="v2-metric-label">{label}</p>
      <p className="v2-metric-value">{value}</p>
      {hint ? <p className="v2-hint mt-1">{hint}</p> : null}
    </div>
  );
}

export function useErrorMessage(message: string): string {
  const { tx } = useI18n();
  if (!message.startsWith("ui.")) return message;
  return tx(message as Parameters<typeof tx>[0]);
}

export function Notice({ tone = "info", children }: { tone?: "info" | "ok" | "warn" | "error"; children: ReactNode }) {
  return (
    <p className="v2-notice" data-tone={tone} role={tone === "error" ? "alert" : undefined}>
      {children}
    </p>
  );
}

export function Progress({ value, tone = "blue" }: { value: number | null; tone?: Tone }) {
  const width = value === null ? 0 : Math.max(0, Math.min(100, value));
  const bar = tone === "amber" ? "bg-amber-500" : tone === "red" ? "bg-red-500" : "bg-blue-600";
  return (
    <div className="h-1.5 w-full rounded-full bg-gray-100" role="progressbar" aria-valuenow={value ?? undefined} aria-valuemin={0} aria-valuemax={100}>
      <div className={cn("h-1.5 rounded-full transition-all duration-200 ease-out", bar)} style={{ width: `${width}%` }} />
    </div>
  );
}

export function PageHeader({ title, subtitle, actions }: { title: string; subtitle?: string; actions?: ReactNode }) {
  return (
    <header className="flex flex-wrap items-start justify-between gap-3">
      <div className="min-w-0">
        <h1 className="v2-page-title">{title}</h1>
        {subtitle ? <p className="v2-page-sub">{subtitle}</p> : null}
      </div>
      {actions ? <div className="flex flex-wrap items-center gap-2">{actions}</div> : null}
    </header>
  );
}

export function Toolbar({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn("flex flex-wrap items-center gap-2", className)}>{children}</div>;
}

export function EmptyState({ title, description, action }: { title: string; description?: string; action?: ReactNode }) {
  return (
    <div className="v2-empty">
      <p className="v2-empty-title">{title}</p>
      {description ? <p className="v2-hint max-w-md">{description}</p> : null}
      {action}
    </div>
  );
}

export function ErrorState({ message, retry, retryLabel }: { message: string; retry?: () => void; retryLabel?: string }) {
  // A client-side failure carries an i18n key ("ui.invalid_api_response"); the
  // page must never show the key itself.
  const text = useErrorMessage(message);
  return (
    <div className="v2-notice flex-col items-start gap-2" data-tone="error">
      <p className="font-medium">{text}</p>
      {retry ? (
        <Button variant="secondary" size="sm" onClick={retry}>
          {retryLabel}
        </Button>
      ) : null}
    </div>
  );
}

export function Skeleton({ className }: { className?: string }) {
  return <div className={cn("animate-pulse rounded-lg bg-gray-100", className)} />;
}

export function Tabs<T extends string>({
  tabs,
  active,
  onChange,
}: {
  tabs: Array<{ id: T; label: string; count?: number }>;
  active: T;
  onChange: (id: T) => void;
}) {
  return (
    <div className="v2-tabs" role="tablist">
      {tabs.map((tab) => (
        <button
          key={tab.id}
          type="button"
          role="tab"
          aria-selected={active === tab.id}
          onClick={() => onChange(tab.id)}
          className={cn(
            "v2-tab",
            "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-blue-500 focus-visible:ring-offset-2",
          )}
        >
          {tab.label}
          {typeof tab.count === "number" ? <span className="ml-1.5 text-xs text-gray-400">{tab.count}</span> : null}
        </button>
      ))}
    </div>
  );
}

export function Dialog({ open, title, onClose, children, footer }: { open: boolean; title: string; onClose: () => void; children: ReactNode; footer?: ReactNode }) {
  // Escape closes the dialog. A modal that can only be dismissed by its buttons
  // traps a keyboard user, and the key is the convention every form expects.
  useEffect(() => {
    if (!open) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [open, onClose]);
  if (!open) return null;
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-gray-900/30 p-4" role="dialog" aria-modal="true" aria-label={title}>
      <div className="flex max-h-[calc(100vh-2rem)] w-full max-w-lg flex-col rounded-xl border border-gray-200 bg-white shadow-md">
        <div className="flex items-center justify-between border-b border-gray-200 px-5 py-3.5">
          <h2 className="v2-section-title">{title}</h2>
          <Button variant="ghost" size="sm" onClick={onClose} aria-label="close">
            <X size={16} />
          </Button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">{children}</div>
        {footer ? <div className="flex justify-end gap-2 border-t border-gray-200 px-5 py-3">{footer}</div> : null}
      </div>
    </div>
  );
}

export function Drawer({ open, title, onClose, children }: { open: boolean; title: string; onClose: () => void; children: ReactNode }) {
  // The same Escape contract as the dialog, so both surfaces dismiss alike.
  useEffect(() => {
    if (!open) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [open, onClose]);
  if (!open) return null;
  return (
    <div className="fixed inset-0 z-50 flex justify-end bg-gray-900/30" role="dialog" aria-modal="true" aria-label={title}>
      <aside className="h-full w-full max-w-xl overflow-y-auto border-l border-gray-200 bg-white shadow-md">
        <div className="sticky top-0 z-10 flex items-center justify-between border-b border-gray-200 bg-white px-5 py-3.5">
          <h2 className="v2-section-title">{title}</h2>
          <Button variant="ghost" size="sm" onClick={onClose} aria-label="close">
            <X size={16} />
          </Button>
        </div>
        <div className="px-5 py-4">{children}</div>
      </aside>
    </div>
  );
}

export function KeyValue({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-4 border-b border-gray-100 py-1.5 last:border-0">
      <span className="text-xs text-gray-500">{label}</span>
      <span className="text-right text-xs font-medium text-gray-900">{children}</span>
    </div>
  );
}

// Table primitives: the same header, cell padding and action alignment on every
// screen that shows rows.
export function Table({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div className="overflow-x-auto">
      <table className={cn("v2-table", className)}>{children}</table>
    </div>
  );
}

export function Th({ children, align = "left" }: { children?: ReactNode; align?: "left" | "right" }) {
  return <th style={align === "right" ? { textAlign: "right" } : undefined}>{children}</th>;
}

export function Td({ children, className, colSpan }: { children?: ReactNode; className?: string; colSpan?: number }) {
  return (
    <td className={className} colSpan={colSpan}>
      {children}
    </td>
  );
}

import { clsx } from "clsx";
import type { ButtonHTMLAttributes, InputHTMLAttributes, ReactNode, SelectHTMLAttributes } from "react";
import { Loader2, X } from "lucide-react";

export const cn = (...values: Array<string | false | null | undefined>) => clsx(values);

const buttonBase =
  "inline-flex items-center justify-center gap-2 rounded-lg font-medium transition-all duration-150 ease-out shadow-sm " +
  "hover:-translate-y-0.5 hover:shadow active:scale-[0.98] active:translate-y-0 active:shadow-sm " +
  "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-blue-500 focus-visible:ring-offset-2 " +
  "disabled:pointer-events-none disabled:opacity-50";

const variants = {
  primary: "bg-blue-600 text-white hover:bg-blue-700",
  secondary: "bg-white text-gray-700 border border-gray-300 hover:bg-gray-50",
  ghost: "bg-transparent text-gray-600 shadow-none hover:bg-gray-100 hover:shadow-none",
  danger: "bg-red-600 text-white hover:bg-red-700",
} as const;

const sizes = {
  sm: "h-8 px-3 text-xs",
  md: "h-10 px-4 text-sm",
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

export function Card({ className, children }: { className?: string; children: ReactNode }) {
  return (
    <div className={cn("bg-white rounded-xl shadow-sm border border-gray-200 p-6 transition-all duration-200 ease-out hover:-translate-y-0.5 hover:shadow-md", className)}>
      {children}
    </div>
  );
}

export function CardTitle({ children, className }: { children: ReactNode; className?: string }) {
  return <h2 className={cn("font-semibold tracking-tight text-gray-900 text-base", className)}>{children}</h2>;
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
    <span className={cn("inline-flex items-center gap-1 rounded-lg border px-2 py-0.5 text-xs font-medium", tones[tone], className)}>
      {children}
    </span>
  );
}

export function Input({ className, ...rest }: InputHTMLAttributes<HTMLInputElement>) {
  return (
    <input
      {...rest}
      className={cn(
        "h-10 w-full rounded-lg border border-gray-300 bg-white px-3 text-sm text-gray-900 placeholder:text-gray-400",
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
        "h-10 rounded-lg border border-gray-300 bg-white px-3 text-sm text-gray-900",
        "focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 focus:border-blue-500",
        className,
      )}
    >
      {children}
    </select>
  );
}

export function Progress({ value, tone = "blue" }: { value: number | null; tone?: Tone }) {
  const width = value === null ? 0 : Math.max(0, Math.min(100, value));
  const bar = tone === "amber" ? "bg-amber-500" : tone === "red" ? "bg-red-500" : "bg-blue-600";
  return (
    <div className="h-2 w-full rounded-lg bg-gray-100" role="progressbar" aria-valuenow={value ?? undefined} aria-valuemin={0} aria-valuemax={100}>
      <div className={cn("h-2 rounded-lg transition-all duration-200 ease-out", bar)} style={{ width: `${width}%` }} />
    </div>
  );
}

export function PageHeader({ title, subtitle, actions }: { title: string; subtitle?: string; actions?: ReactNode }) {
  return (
    <header className="flex flex-wrap items-start justify-between gap-4">
      <div>
        <h1 className="font-semibold tracking-tight text-2xl md:text-3xl text-gray-900">{title}</h1>
        {subtitle ? <p className="mt-1 text-sm text-gray-500">{subtitle}</p> : null}
      </div>
      {actions ? <div className="flex flex-wrap items-center gap-3">{actions}</div> : null}
    </header>
  );
}

export function Toolbar({ children }: { children: ReactNode }) {
  return <div className="flex flex-wrap items-center gap-3">{children}</div>;
}

export function EmptyState({ title, description, action }: { title: string; description?: string; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center gap-3 rounded-xl border border-dashed border-gray-300 bg-white px-6 py-16 text-center">
      <p className="font-medium text-gray-900">{title}</p>
      {description ? <p className="max-w-md text-sm text-gray-500">{description}</p> : null}
      {action}
    </div>
  );
}

export function ErrorState({ message, retry, retryLabel }: { message: string; retry?: () => void; retryLabel?: string }) {
  return (
    <div className="flex flex-col items-center justify-center gap-3 rounded-xl border border-red-200 bg-red-50 px-6 py-12 text-center">
      <p className="font-medium text-red-700">{message}</p>
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
    <div className="flex flex-wrap gap-2 border-b border-gray-200 pb-2" role="tablist">
      {tabs.map((tab) => (
        <button
          key={tab.id}
          type="button"
          role="tab"
          aria-selected={active === tab.id}
          onClick={() => onChange(tab.id)}
          className={cn(
            "rounded-lg px-3 py-2 text-sm font-medium transition-all duration-150 ease-out",
            "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-blue-500 focus-visible:ring-offset-2",
            "active:scale-[0.98] active:translate-y-0",
            active === tab.id ? "bg-blue-50 text-blue-700 shadow-sm" : "text-gray-600 hover:bg-gray-100",
          )}
        >
          {tab.label}
          {typeof tab.count === "number" ? <span className="ml-2 text-xs text-gray-500">{tab.count}</span> : null}
        </button>
      ))}
    </div>
  );
}

export function Dialog({ open, title, onClose, children, footer }: { open: boolean; title: string; onClose: () => void; children: ReactNode; footer?: ReactNode }) {
  if (!open) return null;
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-gray-900/30 p-4" role="dialog" aria-modal="true" aria-label={title}>
      <div className="w-full max-w-lg rounded-xl border border-gray-200 bg-white shadow-md">
        <div className="flex items-center justify-between border-b border-gray-200 px-6 py-4">
          <h2 className="font-semibold tracking-tight text-gray-900">{title}</h2>
          <Button variant="ghost" size="sm" onClick={onClose} aria-label="close">
            <X size={16} />
          </Button>
        </div>
        <div className="px-6 py-4">{children}</div>
        {footer ? <div className="flex justify-end gap-3 border-t border-gray-200 px-6 py-4">{footer}</div> : null}
      </div>
    </div>
  );
}

export function Drawer({ open, title, onClose, children }: { open: boolean; title: string; onClose: () => void; children: ReactNode }) {
  if (!open) return null;
  return (
    <div className="fixed inset-0 z-50 flex justify-end bg-gray-900/30" role="dialog" aria-modal="true" aria-label={title}>
      <aside className="h-full w-full max-w-xl overflow-y-auto border-l border-gray-200 bg-white shadow-md">
        <div className="sticky top-0 z-10 flex items-center justify-between border-b border-gray-200 bg-white px-6 py-4">
          <h2 className="font-semibold tracking-tight text-gray-900">{title}</h2>
          <Button variant="ghost" size="sm" onClick={onClose} aria-label="close">
            <X size={16} />
          </Button>
        </div>
        <div className="px-6 py-4">{children}</div>
      </aside>
    </div>
  );
}

export function KeyValue({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-4 border-b border-gray-100 py-2 last:border-0">
      <span className="text-sm text-gray-500">{label}</span>
      <span className="text-right text-sm font-medium text-gray-900">{children}</span>
    </div>
  );
}

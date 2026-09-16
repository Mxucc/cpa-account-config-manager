import type { ReactNode } from "react";

/**
 * One headline metric of a product overview: what it counts, its value and the honest
 * note the value needs (how it was derived, or why it is unavailable).
 */
export interface UsageMetric {
  key: string;
  label: string;
  value: string;
  note?: string;
  /** Full-text explanation for hover, used when the note is too terse on its own. */
  title?: string;
  tone?: "default" | "success" | "warning" | "accent";
  icon: ReactNode;
}

/**
 * The three product overviews share the dashboard's stat cards, so every menu opens on a
 * surface the operator already knows instead of a third card design.
 */
export function UsageMetricCards({ label, metrics }: { label: string; metrics: UsageMetric[] }) {
  return (
    <div className="dashboard-grid" role="group" aria-label={label}>
      {metrics.map((metric) => (
        <article className="dashboard-card" key={metric.key} title={metric.title}>
          <div className={metric.tone && metric.tone !== "default" ? `dashboard-card-icon ${metric.tone}` : "dashboard-card-icon"}>{metric.icon}</div>
          <span>{metric.label}</span>
          <strong>{metric.value}</strong>
          <small>{metric.note ?? ""}</small>
        </article>
      ))}
    </div>
  );
}

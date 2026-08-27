import type {
  Account,
  InspectionHealth,
  InspectionSnapshot,
  OperationEntry,
} from "../types";

export const HEALTH_STATES: InspectionHealth[] = [
  "healthy",
  "quota_limited",
  "invalid_credentials",
  "deactivated",
  "review",
  "unavailable",
  "disabled",
  "unknown",
];

export interface StatusDistribution {
  health: InspectionHealth;
  count: number;
  percent: number;
}

export interface TrendPoint {
  label: string;
  timestamp?: string;
  success: number;
  failed: number;
}

export interface TimelineEvent {
  id: string;
  kind: "inspection" | "operation" | "account";
  tone: "success" | "warning" | "danger" | "info" | "neutral";
  title: string;
  detail: string;
  timestamp?: string;
}

export interface AttentionItem {
  id: string;
  tone: "warning" | "danger" | "info";
  title: string;
  detail: string;
}

const isHealth = (value: unknown): value is InspectionHealth =>
  typeof value === "string" && HEALTH_STATES.includes(value as InspectionHealth);

export function safeMetric(value: unknown): number {
  const number = Number(value);
  return Number.isFinite(number) && number >= 0 ? number : 0;
}

export function accountHealth(account: Account): InspectionHealth {
  if (account.disabled) return "disabled";
  if (account.unavailable) return "unavailable";
  if (account.automation?.health && isHealth(account.automation.health)) return account.automation.health;
  if (account.status === "error") return "invalid_credentials";
  return "unknown";
}

export function buildHealthDistribution(accounts: Account[]): StatusDistribution[] {
  const counts = new Map<InspectionHealth, number>(HEALTH_STATES.map((health) => [health, 0]));
  for (const account of accounts) counts.set(accountHealth(account), (counts.get(accountHealth(account)) ?? 0) + 1);
  const total = accounts.length;
  return HEALTH_STATES.map((health) => ({
    health,
    count: counts.get(health) ?? 0,
    percent: total > 0 ? ((counts.get(health) ?? 0) / total) * 100 : 0,
  }));
}

function validTimestamp(value: unknown): string | undefined {
  if (typeof value !== "string" || !value.trim()) return undefined;
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? undefined : value;
}

export function buildRequestTrend(accounts: Account[], limit = 12): TrendPoint[] {
  const points = new Map<string, TrendPoint>();
  for (const account of accounts) {
    for (const request of account.recent_requests ?? []) {
      const timestamp = validTimestamp(request.time);
      const label = timestamp ? new Date(timestamp).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : "—";
      const key = timestamp ?? `unknown-${points.size}`;
      const current = points.get(key) ?? { label, timestamp, success: 0, failed: 0 };
      current.success += safeMetric(request.success);
      current.failed += safeMetric(request.failed);
      points.set(key, current);
    }
  }
  return [...points.values()]
    .sort((left, right) => (left.timestamp ? new Date(left.timestamp).getTime() : 0) - (right.timestamp ? new Date(right.timestamp).getTime() : 0))
    .slice(-Math.max(1, limit));
}

function eventTimestamp(event: TimelineEvent): number {
  return event.timestamp ? new Date(event.timestamp).getTime() : 0;
}

export function buildDashboardTimeline(
  inspection: InspectionSnapshot | null,
  operations: OperationEntry[] = [],
  accounts: Account[] = [],
  limit = 8,
): TimelineEvent[] {
  const events: TimelineEvent[] = [];
  for (const run of inspection?.recent_runs ?? []) {
    events.push({
      id: `inspection-${run.id}`,
      kind: "inspection",
      tone: run.status === "completed" ? "success" : run.status === "failed" ? "danger" : run.status === "waiting_for_auth" ? "warning" : "info",
      title: `Inspection · ${run.mode}`,
      detail: `${run.summary.scanned} scanned · ${run.summary.failed} failed`,
      timestamp: run.finished_at ?? run.started_at,
    });
  }
  for (const operation of operations) {
    events.push({
      id: `operation-${operation.id}`,
      kind: "operation",
      tone: operation.status === "succeeded" ? "success" : operation.status === "failed" ? "danger" : operation.status === "warning" || operation.status === "partial" ? "warning" : "info",
      title: operation.action || operation.category,
      detail: `${operation.succeeded} succeeded · ${operation.failed} failed · ${operation.skipped} skipped`,
      timestamp: operation.finished_at ?? operation.started_at,
    });
  }
  for (const account of accounts) {
    const timestamp = validTimestamp(account.usage?.last_request_at);
    if (!timestamp) continue;
    events.push({
      id: `account-${account.id}`,
      kind: "account",
      tone: accountHealth(account) === "healthy" ? "success" : "warning",
      title: account.label || account.email || account.name,
      detail: `${safeMetric(account.usage?.total_tokens).toLocaleString()} tokens observed`,
      timestamp,
    });
  }
  return events.sort((left, right) => eventTimestamp(right) - eventTimestamp(left)).slice(0, Math.max(1, limit));
}

export function buildAttentionItems(accounts: Account[], inspection: InspectionSnapshot | null, operations: OperationEntry[] = [], limit = 6): AttentionItem[] {
  const items: AttentionItem[] = [];
  for (const account of accounts) {
    const health = accountHealth(account);
    if (health === "healthy") continue;
    const label = account.label || account.email || account.name;
    const detail = account.automation?.recover_after
      ? `Recovery ${new Date(account.automation.recover_after).toLocaleString()}`
      : account.status_message || health.replaceAll("_", " ");
    items.push({ id: account.id, tone: health === "invalid_credentials" || health === "unavailable" ? "danger" : "warning", title: label, detail });
  }
  if (inspection?.action_count) items.unshift({ id: "inspection-actions", tone: "warning", title: "Inspection actions pending", detail: `${safeMetric(inspection.action_count)} actions need review` });
  for (const operation of operations.filter((entry) => ["failed", "partial", "warning", "interrupted"].includes(entry.status)).slice(0, 3)) {
    items.push({ id: `operation-${operation.id}`, tone: operation.status === "failed" ? "danger" : "warning", title: operation.action || operation.category, detail: `${operation.failed} failed · ${operation.skipped} skipped` });
  }
  return items.slice(0, Math.max(1, limit));
}

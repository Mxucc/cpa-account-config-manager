import { describe, expect, it } from "vitest";
import type { Account, InspectionSnapshot, OperationEntry } from "../types";
import {
  HEALTH_STATES,
  accountHealth,
  buildAttentionItems,
  buildDashboardTimeline,
  buildHealthDistribution,
  buildRequestTrend,
  safeMetric,
} from "./dashboardMetrics";

function account(overrides: Partial<Account> = {}): Account {
  return {
    id: "account-1",
    name: "Account 1",
    disabled: false,
    unavailable: false,
    runtime_only: false,
    proxy_configured: false,
    header_count: 0,
    editable: true,
    success: 0,
    failed: 0,
    ...overrides,
  };
}

function inspection(overrides: Partial<InspectionSnapshot> = {}): InspectionSnapshot {
  return {
    policy: {} as InspectionSnapshot["policy"],
    running: false,
    pending: false,
    last_run: {
      scanned: 0,
      healthy: 0,
      quota_limited: 0,
      invalid_credentials: 0,
      deactivated: 0,
      review: 0,
      unavailable: 0,
      disabled: 0,
      unknown: 0,
      auto_disabled: 0,
      auto_enabled: 0,
      delete_pending: 0,
      failed: 0,
      truncated: 0,
    },
    total: 0,
    action_count: 0,
    active_probe_armed: false,
    probe_sweep_remaining: 0,
    anomaly_eligible: 0,
    anomaly_count: 0,
    anomaly_percent: 0,
    anomaly_trigger_pending: false,
    ...overrides,
  };
}

function operation(overrides: Partial<OperationEntry> = {}): OperationEntry {
  return {
    id: "operation-1",
    category: "batch",
    action: "Batch edit",
    status: "succeeded",
    source: "manual",
    target_count: 1,
    succeeded: 1,
    failed: 0,
    skipped: 0,
    started_at: "2026-08-25T10:00:00Z",
    ...overrides,
  };
}

describe("dashboardMetrics", () => {
  it("protects malformed and negative metric values", () => {
    expect(safeMetric(undefined)).toBe(0);
    expect(safeMetric("not-a-number")).toBe(0);
    expect(safeMetric(-3)).toBe(0);
    expect(safeMetric("4.5")).toBe(4.5);
  });

  it("prioritizes explicit account health and disabled/unavailable flags", () => {
    expect(accountHealth(account({ disabled: true, automation: { health: "healthy" } as Account["automation"] }))).toBe("disabled");
    expect(accountHealth(account({ unavailable: true }))).toBe("unavailable");
    expect(accountHealth(account({ automation: { health: "quota_limited" } as Account["automation"] }))).toBe("quota_limited");
    expect(accountHealth(account({ status: "error" }))).toBe("invalid_credentials");
    expect(accountHealth(account())).toBe("unknown");
  });

  it("returns every health state with safe percentages", () => {
    const accounts = HEALTH_STATES.map((health, index) => account({ id: `account-${index}`, automation: { health } as Account["automation"] }));
    const distribution = buildHealthDistribution(accounts);
    expect(distribution.map((item) => item.health)).toEqual(HEALTH_STATES);
    expect(distribution.every((item) => item.count === 1 && item.percent === 12.5)).toBe(true);
    expect(buildHealthDistribution([]).every((item) => item.count === 0 && item.percent === 0)).toBe(true);
  });

  it("sorts request trend points, aggregates duplicate timestamps, and bounds the result", () => {
    const accounts = [account({
      recent_requests: [
        { time: "2026-08-25T12:00:00Z", success: 2, failed: 1 },
        { time: "2026-08-25T10:00:00Z", success: 1, failed: 0 },
        { time: "invalid", success: 100, failed: 100 },
      ],
    }), account({ id: "account-2", recent_requests: [{ time: "2026-08-25T12:00:00Z", success: 3, failed: 2 }] })];
    const trend = buildRequestTrend(accounts, 2);
    expect(trend).toHaveLength(2);
    expect(trend[0]).toMatchObject({ success: 1, failed: 0, timestamp: "2026-08-25T10:00:00Z" });
    expect(trend[1]).toMatchObject({ success: 5, failed: 3, timestamp: "2026-08-25T12:00:00Z" });
  });

  it("orders timeline events newest first and keeps missing timestamps last", () => {
    const events = buildDashboardTimeline(
      inspection({ recent_runs: [{ id: "run-1", mode: "full", source: "manual", status: "completed", started_at: "2026-08-25T08:00:00Z", finished_at: "2026-08-25T09:00:00Z", primary_total: 1, primary_completed: 1, retry_total: 0, retry_completed: 0, summary: { ...inspection().last_run, scanned: 1 } }] }),
      [operation({ id: "operation-new", started_at: "2026-08-25T11:00:00Z" }), operation({ id: "operation-no-time", started_at: "invalid" })],
      [account({ usage: { total_tokens: 12, last_request_at: "2026-08-25T10:00:00Z" } as Account["usage"] })],
    );
    expect(events.map((event) => event.id)).toEqual(["operation-operation-new", "account-account-1", "inspection-run-1", "operation-operation-no-time"]);
  });

  it("limits attention items and prioritizes inspection actions and failed operations", () => {
    const accounts = Array.from({ length: 8 }, (_, index) => account({ id: `account-${index}`, label: `Account ${index}`, status: "error" }));
    const items = buildAttentionItems(accounts, inspection({ action_count: 2 }), [operation({ status: "failed", failed: 1, skipped: 1 })], 4);
    expect(items).toHaveLength(4);
    expect(items[0]).toMatchObject({ id: "inspection-actions", tone: "warning" });
    expect(items.some((item) => item.id === "operation-operation-1")).toBe(false);
  });
});

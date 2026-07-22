import { describe, expect, it } from "vitest";
import { formatDateTimeForLocale, formatDaysHoursForLocale } from "../i18n";
import type { Account, AccountAutomationSummary } from "../types";
import { accountAutomationPresentation } from "./accountAutomation";

const now = new Date("2026-07-20T08:00:00Z");

function automation(overrides: Partial<AccountAutomationSummary> = {}): AccountAutomationSummary {
  return {
    health: "quota_limited",
    reason_code: "quota_exhausted",
    recommendation: "enable",
    last_checked_at: "2026-07-20T07:59:00Z",
    owned_disable: true,
    disable_reason: "quota_exhausted",
    disabled_at: "2026-07-20T07:00:00Z",
    auto_disable_eligible: true,
    inspection_enabled: true,
    auto_disable_enabled: true,
    auto_enable_enabled: true,
    auto_delete_enabled: false,
    failure_threshold: 3,
    failure_streak: 3,
    recovery_threshold: 2,
    healthy_streak: 0,
    ...overrides,
  };
}

function account(summary?: AccountAutomationSummary, disabled = true): Account {
  return {
    id: "account-1",
    name: "account.json",
    disabled,
    unavailable: false,
    runtime_only: false,
    proxy_configured: false,
    header_count: 0,
    editable: true,
    success: 0,
    failed: 0,
    automation: summary,
  };
}

describe("accountAutomationPresentation", () => {
  it("shows passive circuit evidence and its exact recovery time", () => {
    const target = account(automation({
        owned_disable: true,
        disable_reason: "passive_circuit_open",
        circuit_open: true,
        circuit_reason_code: "invalid_response",
        recover_after: "2026-07-21T12:30:00Z",
    }), true);

    const result = accountAutomationPresentation(target, "zh-CN", new Date("2026-07-21T12:00:00Z"));

    expect(result?.badge).toBe("被动临时熔断");
    expect(result?.detail).toContain("无法确认上游响应");
    expect(result?.detail).toContain("2026");
  });

  it.each([
    ["zh-CN", "自动禁用", "额度已耗尽", "预计"],
    ["zh-TW", "自動停用", "額度已用盡", "預計"],
    ["en", "Auto-disabled", "Quota exhausted", "Expected auto-enable"],
    ["ru", "Отключено автоматически", "Квота исчерпана", "Автовключение ожидается"],
  ] as const)("shows an owned quota disable and its concrete auto-enable time in %s", (locale, badge, reason, prefix) => {
    const recoverAfter = "2026-07-20T10:30:00Z";
    const result = accountAutomationPresentation(account(automation({ recover_after: recoverAfter })), locale, now);
    expect(result?.badge).toBe(badge);
    expect(result?.detail).toContain(reason);
    expect(result?.detail).toContain(prefix);
    expect(result?.detail).toContain(formatDateTimeForLocale(locale, recoverAfter));
  });

  it.each([
    ["zh-CN", "后自动启用"],
    ["zh-TW", "後自動啟用"],
    ["en", "Auto-enable in"],
    ["ru", "Автовключение через"],
  ] as const)("shows a days-and-hours countdown beyond 24 hours in %s", (locale, expected) => {
    const result = accountAutomationPresentation(account(automation({
      recover_after: "2026-07-22T09:01:00Z",
    })), locale, now);
    expect(result?.detail).toContain(expected);
    expect(result?.detail).toContain(formatDaysHoursForLocale(locale, 2, 2));
    expect(result?.detail).not.toContain(formatDateTimeForLocale(locale, "2026-07-22T09:01:00Z"));
  });

  it("uses locale plural rules for a one-day one-hour recovery", () => {
    const recoverAfter = "2026-07-21T09:00:00Z";
    const english = accountAutomationPresentation(account(automation({ recover_after: recoverAfter })), "en", now);
    const russian = accountAutomationPresentation(account(automation({ recover_after: recoverAfter })), "ru", now);
    expect(english?.detail).toContain("1 day 1 hour");
    expect(russian?.detail).toContain("1 день 1 час");
  });

  it("keeps the exact-time presentation at the inclusive 24-hour boundary", () => {
    const recoverAfter = "2026-07-21T08:00:00Z";
    const result = accountAutomationPresentation(account(automation({ recover_after: recoverAfter })), "zh-CN", now);
    expect(result?.detail).toContain(formatDateTimeForLocale("zh-CN", recoverAfter));
    expect(result?.detail).not.toContain("天 0 小时后");
  });

  it("does not invent a recovery time for invalid credentials", () => {
    const result = accountAutomationPresentation(account(automation({
      health: "invalid_credentials",
      reason_code: "invalid_credentials",
      disable_reason: "invalid_credentials",
      recommendation: "reauth",
      recover_after: undefined,
    })), "zh-CN", now);
    expect(result).toMatchObject({ badge: "自动禁用" });
    expect(result?.detail).toContain("等待重新授权或凭据刷新");
    expect(result?.detail).not.toContain("预计");
  });

  it("distinguishes a deletion recommendation from enabled auto-delete", () => {
    const result = accountAutomationPresentation(account(automation({
      health: "deactivated",
      reason_code: "workspace_deactivated",
      disable_reason: "workspace_deactivated",
      recommendation: "delete",
      auto_delete_enabled: false,
    })), "zh-CN", now);
    expect(result).toMatchObject({ badge: "建议删除", tone: "danger" });
    expect(result?.detail).toContain("自动删除未开启");
  });

  it("shows deletion grace and failed retry times", () => {
    const eligibleAt = "2026-07-21T08:00:00Z";
    const grace = accountAutomationPresentation(account(automation({
      health: "deactivated",
      reason_code: "account_deactivated",
      disable_reason: "account_deactivated",
      recommendation: "delete",
      auto_delete_enabled: true,
      delete_eligible_at: eligibleAt,
    })), "zh-CN", now);
    expect(grace?.badge).toBe("等待删除宽限期");
    expect(grace?.detail).toContain(formatDateTimeForLocale("zh-CN", eligibleAt));

    const retryAfter = "2026-07-20T08:05:00Z";
    const retry = accountAutomationPresentation(account(automation({
      health: "deactivated",
      reason_code: "account_deactivated",
      disable_reason: "account_deactivated",
      recommendation: "delete",
      auto_delete_enabled: true,
      delete_eligible_at: "2026-07-20T07:00:00Z",
      delete_retry_after: retryAfter,
      auto_action: "delete_candidate",
      auto_action_status: "failed",
    })), "zh-CN", now);
    expect(retry?.badge).toBe("等待自动删除重试");
    expect(retry?.detail).toContain(formatDateTimeForLocale("zh-CN", retryAfter));
  });

  it("keeps manual disables and pre-threshold auto-disable evidence distinct", () => {
    const manual = accountAutomationPresentation(account(automation({
      owned_disable: false,
      health: "disabled",
      reason_code: "manual_disabled",
      disable_reason: undefined,
      recommendation: "review",
    })), "zh-CN", now);
    expect(manual?.badge).toBe("人工禁用");

    const waiting = accountAutomationPresentation(account(automation({
      owned_disable: false,
      recommendation: "disable",
      failure_streak: 2,
      failure_threshold: 3,
    }), false), "zh-CN", now);
    expect(waiting?.badge).toBe("等待自动禁用");
    expect(waiting?.detail).toContain("2/3");
  });

  it("shows failed automatic disable actions instead of an indefinite waiting state", () => {
    const failed = accountAutomationPresentation(account(automation({
      owned_disable: false,
      health: "quota_limited",
      reason_code: "quota_exhausted",
      recommendation: "disable",
      auto_action: "disable",
      auto_action_status: "failed",
      auto_disable_enabled: true,
      failure_streak: 1,
    }), false), "zh-CN", now);

    expect(failed?.badge).toBe("自动禁用失败");
    expect(failed?.detail).toContain("额度已耗尽");
    expect(failed?.detail).toContain("下次巡检将重试");
  });

  it("returns no disposition when no inspection summary exists", () => {
    expect(accountAutomationPresentation(account(undefined, false), "zh-CN", now)).toBeNull();
  });
});

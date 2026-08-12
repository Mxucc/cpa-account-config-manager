import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Account } from "../types";
import { AccountUsageCell } from "./AccountUsageCell";

const baseAccount: Account = {
  id: "auth-usage",
  name: "usage.json",
  provider: "codex",
  disabled: false,
  unavailable: false,
  runtime_only: false,
  proxy_configured: false,
  header_count: 0,
  editable: true,
  success: 23,
  failed: 2,
};

const quotaAutomation: NonNullable<Account["automation"]> = {
  health: "quota_limited",
  reason_code: "quota_exhausted",
  recommendation: "disable",
  last_checked_at: "2026-07-21T12:00:00Z",
  owned_disable: false,
  auto_disable_eligible: true,
  inspection_enabled: true,
  auto_disable_enabled: true,
  auto_enable_enabled: true,
  auto_delete_enabled: false,
  failure_threshold: 3,
  failure_streak: 1,
  recovery_threshold: 2,
  healthy_streak: 0,
};

describe("AccountUsageCell", () => {
  it("switches the primary usage display to estimated USD cost without hiding token details", () => {
    const account: Account = {
      ...baseAccount,
      usage: {
        input_tokens: 1000,
        output_tokens: 100,
        reasoning_tokens: 0,
        cached_tokens: 0,
        cache_read_tokens: 200,
        cache_creation_tokens: 0,
        total_tokens: 1100,
        credit: {
          amount_usd: 0.00345,
          rated_requests: 2,
          unrated_requests: 1,
          started_at: "2026-08-12T08:00:00Z",
          pricing_updated_at: "2026-08-12T07:00:00Z",
          pricing_source: "Sub2API / Wei-Shaw model-price-repo",
        },
      },
    };
    const { rerender } = render(<AccountUsageCell account={account} creditUsageEnabled />);

    expect(screen.getByText("USD")).toBeInTheDocument();
    expect(screen.getByTitle(/预估成本：.*已计价请求：2.*未计价请求：1.*原始 Token：1,100/)).toBeInTheDocument();
    expect(screen.getByText("未计价 1")).toBeInTheDocument();

    rerender(<AccountUsageCell account={account} creditUsageEnabled={false} />);
    expect(screen.getByText("tok")).toBeInTheDocument();
    expect(screen.getByText("1100")).toBeInTheDocument();
    expect(screen.queryByText("未计价 1")).not.toBeInTheDocument();
  });

  it("shows a collection placeholder before the first credit-rated request", () => {
    render(<AccountUsageCell account={{ ...baseAccount, usage: {
      input_tokens: 0,
      output_tokens: 0,
      reasoning_tokens: 0,
      cached_tokens: 0,
      cache_read_tokens: 0,
      cache_creation_tokens: 0,
      total_tokens: 0,
    } }} creditUsageEnabled />);

    expect(screen.getByText("等待额度计费采集")).toBeInTheDocument();
    expect(screen.queryByText("tok")).not.toBeInTheDocument();
  });

  it("shows per-window overdraft only while the quota-continuation experiment is enabled", () => {
    const account: Account = {
      ...baseAccount,
      usage: {
        input_tokens: 0,
        output_tokens: 0,
        reasoning_tokens: 0,
        cached_tokens: 0,
        cache_read_tokens: 0,
        cache_creation_tokens: 0,
        total_tokens: 0,
        codex: {
          observed_at: "2026-07-29T06:00:00Z",
          five_hour: { used_percent: 118.5, window_minutes: 300, overdraft_active: true },
          seven_day: { used_percent: 142, window_minutes: 10_080, overdraft_active: true },
        },
      },
    };
    const { rerender } = render(<AccountUsageCell account={account} weeklyOverdraftEnabled />);

    expect(screen.getByRole("group", { name: "透支用量" })).toHaveTextContent("5h+18.5%7d+42%");
    expect(screen.getByRole("meter", { name: "5h 用量 118.5%" })).toBeInTheDocument();
    expect(screen.getByRole("meter", { name: "7d 用量 142%" })).toBeInTheDocument();

    rerender(<AccountUsageCell account={account} weeklyOverdraftEnabled={false} />);
    expect(screen.queryByRole("group", { name: "透支用量" })).not.toBeInTheDocument();

    rerender(<AccountUsageCell account={{
      ...account,
      usage: {
        ...account.usage!,
        codex: { observed_at: "2026-07-29T06:00:00Z", five_hour: { used_percent: 100, window_minutes: 300 } },
      },
    }} weeklyOverdraftEnabled />);
    expect(screen.queryByRole("group", { name: "透支用量" })).not.toBeInTheDocument();
  });

  it("shows measured post-exhaustion tokens when upstream caps quota at 100 percent", () => {
    render(<AccountUsageCell account={{
      ...baseAccount,
      usage: {
        input_tokens: 12_000,
        output_tokens: 3_000,
        reasoning_tokens: 0,
        cached_tokens: 0,
        cache_read_tokens: 0,
        cache_creation_tokens: 0,
        total_tokens: 15_000,
        codex: {
          observed_at: "2026-07-30T01:00:00Z",
          five_hour: {
            used_percent: 100,
            window_minutes: 300,
            overdraft_active: true,
            overdraft_tokens: 12_345,
            overdraft_requests: 4,
          },
        },
      },
    }} weeklyOverdraftEnabled />);

    const panel = screen.getByRole("group", { name: "透支用量" });
    expect(panel).toHaveTextContent("5h+1.2万 tok");
    expect(screen.getByTitle("5h 从普通探测失败时的冻结基线起，已观测透支 12,345 Tokens，共 4 个成功请求（官方总用量 100%）")).toBeInTheDocument();
  });

  it("uses credit pricing for overdraft when both experiments are enabled", () => {
    const account: Account = {
      ...baseAccount,
      usage: {
        input_tokens: 3000,
        output_tokens: 300,
        reasoning_tokens: 0,
        cached_tokens: 0,
        cache_read_tokens: 0,
        cache_creation_tokens: 0,
        total_tokens: 3300,
        credit: { amount_usd: 0.0125, rated_requests: 3, unrated_requests: 1 },
        codex: {
          observed_at: "2026-08-12T08:00:00Z",
          five_hour: {
            used_percent: 100,
            window_minutes: 300,
            overdraft_active: true,
            overdraft_tokens: 2200,
            overdraft_requests: 2,
            overdraft_amount_usd: 0.0065,
            overdraft_rated_requests: 1,
            overdraft_unrated_requests: 1,
          },
        },
      },
    };

    const { rerender } = render(<AccountUsageCell account={account} weeklyOverdraftEnabled creditUsageEnabled />);
    expect(screen.getByRole("group", { name: "透支用量" })).toHaveTextContent("5h+$0.0065");
    expect(screen.getByTitle("5h 透支预估费用 $0.0065；已计价 1 次，未计价 1 次；该费用已包含在账号总预估费用中。")).toBeInTheDocument();

    rerender(<AccountUsageCell account={account} weeklyOverdraftEnabled creditUsageEnabled={false} />);
    expect(screen.getByRole("group", { name: "透支用量" })).toHaveTextContent("5h+2200 tok");

    rerender(<AccountUsageCell account={account} weeklyOverdraftEnabled={false} creditUsageEnabled />);
    expect(screen.queryByRole("group", { name: "透支用量" })).not.toBeInTheDocument();
  });

  it("falls back to measured successful requests when a stream omits token usage", () => {
    render(<AccountUsageCell account={{
      ...baseAccount,
      usage: {
        input_tokens: 0,
        output_tokens: 0,
        reasoning_tokens: 0,
        cached_tokens: 0,
        cache_read_tokens: 0,
        cache_creation_tokens: 0,
        total_tokens: 0,
        codex: {
          observed_at: "2026-07-30T01:00:00Z",
          five_hour: { used_percent: 100, window_minutes: 300, overdraft_active: true, overdraft_requests: 3 },
        },
      },
    }} weeklyOverdraftEnabled />);

    expect(screen.getByRole("group", { name: "透支用量" })).toHaveTextContent("5h+3 次");
    expect(screen.getByTitle("5h 从普通探测失败时的冻结基线起，已观测透支 0 Tokens，共 3 个成功请求（官方总用量 100%）")).toBeInTheDocument();
  });

  it("renders token and request activity with clamped Codex quota tracks", () => {
    render(<AccountUsageCell account={{
      ...baseAccount,
      recent_requests: [
        { time: "2026-07-15T11:00:00Z", success: 3, failed: 1 },
        { time: "2026-07-15T12:00:00Z", success: 4, failed: 0 },
      ],
      usage: {
        input_tokens: 10_000,
        output_tokens: 2_000,
        reasoning_tokens: 345,
        cached_tokens: 0,
        cache_read_tokens: 0,
        cache_creation_tokens: 0,
        total_tokens: 12_345,
        last_request_at: "2026-07-15T12:00:00Z",
        updated_at: "2026-07-15T12:00:00Z",
        codex: {
          observed_at: "2026-07-15T12:00:00Z",
          five_hour: { used_percent: 18.5, reset_at: "2026-07-15T12:30:00Z", window_minutes: 300 },
          seven_day: { used_percent: 142, reset_at: "2026-07-20T12:00:00Z", window_minutes: 10_080 },
        },
      },
    }} />);

    expect(screen.getByTitle("累计 Token：12,345")).toHaveTextContent("1.2万tok");
    expect(screen.getByTitle("累计请求：成功 23，失败 2")).toHaveTextContent("23/2");
    expect(screen.getByTitle(/CPA 近期请求：8/)).toHaveTextContent("8");
    expect(screen.getByRole("meter", { name: "5h 用量 18.5%" }).firstElementChild).toHaveStyle({ width: "18.5%" });
    expect(screen.getByRole("meter", { name: "7d 用量 142%" }).firstElementChild).toHaveStyle({ width: "100%" });
    expect(screen.getByText("142%")).toBeInTheDocument();
  });

  it("shows a populated collection state instead of blank usage placeholders", () => {
    const { rerender } = render(<AccountUsageCell account={baseAccount} />);
    expect(screen.getByText("等待用量采集")).toBeInTheDocument();
    expect(screen.getByText("0", { selector: ".usage-token-total strong" })).toBeInTheDocument();
    expect(screen.queryByText("--")).not.toBeInTheDocument();

    rerender(<AccountUsageCell account={{
      ...baseAccount,
      usage: {
        input_tokens: 40,
        output_tokens: 2,
        reasoning_tokens: 0,
        cached_tokens: 0,
        cache_read_tokens: 0,
        cache_creation_tokens: 0,
        total_tokens: 42,
        updated_at: "2026-07-15T10:00:00Z",
      },
    }} />);
    expect(screen.getByText("42")).toBeInTheDocument();
    expect(screen.queryByRole("meter")).not.toBeInTheDocument();
    expect(screen.getByText("等待用量采集")).toBeInTheDocument();
  });

  it("distinguishes unsupported Agent Identity quota from zero usage", () => {
    render(<AccountUsageCell account={{
      ...baseAccount,
      provider: "codex-agent-identity",
      plan_type: "k12",
      success: 32,
      failed: 5,
      recent_requests: [{ time: "2026-07-23T07:09:00Z", success: 32, failed: 5 }],
    }} />);

    expect(screen.getByText("未知", { selector: ".usage-token-total strong" })).toBeInTheDocument();
    expect(screen.getByTitle("累计请求：成功 32，失败 5")).toHaveTextContent("32/5");
    expect(screen.getByTitle(/CPA 近期请求：37/)).toHaveTextContent("37");
    expect(screen.getByText("CPA 暂未提供 Agent Identity 配额")).toBeInTheDocument();
    expect(screen.queryByText("等待用量采集")).not.toBeInTheDocument();
  });

  it("makes exhausted quota and the next action visible", () => {
    const { rerender } = render(<AccountUsageCell account={{
      ...baseAccount,
      automation: quotaAutomation,
      usage: {
        input_tokens: 0, output_tokens: 0, reasoning_tokens: 0, cached_tokens: 0,
        cache_read_tokens: 0, cache_creation_tokens: 0, total_tokens: 0,
        codex: { observed_at: "2026-07-21T12:00:00Z", five_hour: { used_percent: 100, window_minutes: 300 } },
      },
    }} />);
    expect(screen.getByRole("status")).toHaveTextContent("额度已用尽等待自动禁用");

    rerender(<AccountUsageCell account={{
      ...baseAccount,
      automation: { ...quotaAutomation, auto_disable_enabled: false },
      usage: {
        input_tokens: 0, output_tokens: 0, reasoning_tokens: 0, cached_tokens: 0,
        cache_read_tokens: 0, cache_creation_tokens: 0, total_tokens: 0,
        codex: { observed_at: "2026-07-21T12:00:00Z", five_hour: { used_percent: 100, window_minutes: 300 } },
      },
    }} />);
    expect(screen.getByRole("status")).toHaveTextContent("额度已用尽自动禁用未开启");

    rerender(<AccountUsageCell account={{
      ...baseAccount,
      disabled: true,
      automation: { ...quotaAutomation, owned_disable: true },
      usage: {
        input_tokens: 0, output_tokens: 0, reasoning_tokens: 0, cached_tokens: 0,
        cache_read_tokens: 0, cache_creation_tokens: 0, total_tokens: 0,
        codex: { observed_at: "2026-07-21T12:00:00Z", five_hour: { used_percent: 100, window_minutes: 300 } },
      },
    }} />);
    expect(screen.getByRole("status")).toHaveTextContent("额度已用尽等待额度恢复");
  });

  it("shows five-hour and weekly overdraft probe decisions instead of a generic disable action", () => {
    const weeklyUsage = {
      input_tokens: 0, output_tokens: 0, reasoning_tokens: 0, cached_tokens: 0,
      cache_read_tokens: 0, cache_creation_tokens: 0, total_tokens: 0,
      codex: { observed_at: "2026-07-25T12:00:00Z", seven_day: { used_percent: 100, window_minutes: 10_080 } },
    };
    const { rerender } = render(<AccountUsageCell account={{
      ...baseAccount,
      automation: {
        ...quotaAutomation,
        auto_disable_probe_name: "weekly_overdraft",
        auto_disable_probe_status: "passed",
        auto_disable_probe_attempts: 3,
        auto_disable_probe_limit: 5,
      },
      usage: weeklyUsage,
    }} />);
    expect(screen.getByRole("status")).toHaveTextContent("额度已用尽透支探测可用，保持启用");

    rerender(<AccountUsageCell account={{
      ...baseAccount,
      automation: {
        ...quotaAutomation,
        auto_disable_probe_name: "weekly_overdraft",
        auto_disable_probe_status: "inconclusive",
        auto_disable_probe_attempts: 0,
        auto_disable_probe_limit: 5,
      },
      usage: weeklyUsage,
    }} />);
    expect(screen.getByRole("status")).toHaveTextContent("额度已用尽透支探测未完成，等待下次巡检");

    rerender(<AccountUsageCell account={{
      ...baseAccount,
      automation: {
        ...quotaAutomation,
        auto_disable_probe_name: "weekly_overdraft",
        auto_disable_probe_status: "passed",
        auto_disable_probe_attempts: 1,
        auto_disable_probe_limit: 5,
      },
      usage: {
        ...weeklyUsage,
        codex: { observed_at: "2026-07-29T01:00:00Z", five_hour: { used_percent: 100, window_minutes: 300 }, seven_day: { used_percent: 17, window_minutes: 10_080 } },
      },
    }} />);
    expect(screen.getByRole("status")).toHaveTextContent("额度已用尽透支探测可用，保持启用");

    rerender(<AccountUsageCell account={{
      ...baseAccount,
      automation: {
        ...quotaAutomation,
        auto_disable_probe_name: "weekly_overdraft",
        auto_disable_probe_status: "inconclusive",
        auto_disable_probe_attempts: 0,
        auto_disable_probe_limit: 5,
        auto_disable_probe_reason_code: "management_auth_unavailable",
      },
      usage: {
        ...weeklyUsage,
        codex: { observed_at: "2026-07-29T01:00:00Z", five_hour: { used_percent: 100, window_minutes: 300 }, seven_day: { used_percent: 17, window_minutes: 10_080 } },
      },
    }} />);
    expect(screen.getByRole("status")).toHaveTextContent("额度已用尽透支探测未完成，等待下次巡检 · 等待已认证的巡检请求");
  });
});

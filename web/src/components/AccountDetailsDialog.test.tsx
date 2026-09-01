import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import type { Account } from "../types";
import { AccountDetailsDialog } from "./AccountDetailsDialog";

const account: Account = {
  id: "auth-1",
  auth_id: "file-1",
  name: "operator.json",
  provider: "codex",
  type: "codex",
  label: "operator@example.com",
  email: "operator@example.com",
  account_type: "oauth",
  plan_type: "plus",
  status: "active",
  disabled: false,
  unavailable: false,
  runtime_only: false,
  source: "file",
  priority: 8,
  prefix: "team-a",
  proxy: "http://127.0.0.1:7890",
  proxy_configured: true,
  websockets: true,
  header_names: ["Authorization", "X-Team"],
  header_count: 2,
	concurrency: { supported: true, active: 10, limit: 0, limit_15s: 0, used_60s: 12, used_15s: 3 },
	model_policy: {
		mode: "allow_only",
		models: ["gpt-5.5", "gpt-5.4-mini"],
		excluded_count: 2,
	},
  editable: true,
  success: 15,
  failed: 2,
  usage: {
    input_tokens: 100,
    output_tokens: 40,
    reasoning_tokens: 10,
    cached_tokens: 5,
    cache_read_tokens: 3,
    cache_creation_tokens: 0,
    total_tokens: 158,
    last_request_at: "2026-07-15T10:00:00Z",
  },
  updated_at: "2026-07-15T10:05:00Z",
};

it("shows only the safe account detail model and opens single-account editing", async () => {
  const user = userEvent.setup();
  const onEdit = vi.fn();
  render(<AccountDetailsDialog account={account} onClose={() => undefined} onEdit={onEdit} />);

  expect(screen.getByRole("dialog", { name: "账号详情" })).toBeInTheDocument();
  expect(screen.getAllByText("operator.json").length).toBeGreaterThan(0);
  expect(screen.getByText("plus")).toBeInTheDocument();
  expect(screen.getByText("http://127.0.0.1:7890")).toBeInTheDocument();
  expect(screen.getByText("Authorization")).toBeInTheDocument();
	expect(screen.getByText("插件配置")).toBeInTheDocument();
	expect(screen.getByText("由插件管理")).toBeInTheDocument();
	expect(screen.getByText("白名单模式")).toBeInTheDocument();
	expect(screen.getByText("gpt-5.5")).toBeInTheDocument();
	expect(screen.getByText("gpt-5.4-mini")).toBeInTheDocument();
	expect(screen.getByText("插件管理的排除模型").parentElement).toHaveTextContent("2");
	expect(screen.getByText("当前传输数 10 · 15 秒窗口 3/∞ · 每分钟窗口 12/∞")).toBeInTheDocument();
  expect(screen.getByText("158")).toBeInTheDocument();
  expect(screen.queryByText(/access_token|Bearer secret/i)).not.toBeInTheDocument();

  await user.click(screen.getByRole("button", { name: "编辑账号" }));
  expect(onEdit).toHaveBeenCalledTimes(1);
});

it("shows live active transfers and both configured request windows", () => {
	render(<AccountDetailsDialog account={{ ...account, concurrency: { supported: true, active: 10, limit: 100, limit_15s: 3, used_60s: 7, used_15s: 2 } }} onClose={() => undefined} onEdit={() => undefined} />);
	expect(screen.getByText("当前传输数 10 · 15 秒窗口 2/3 · 每分钟窗口 7/100")).toBeInTheDocument();
});

it("localizes the Agent Identity account type", () => {
  render(<AccountDetailsDialog account={{ ...account, provider: "codex-agent-identity", account_type: "agent_identity" }} onClose={() => undefined} onEdit={() => undefined} />);
  expect(screen.getByText("Agent Identity")).toBeInTheDocument();
});

it("localizes the Codex PAT account type", () => {
  render(<AccountDetailsDialog account={{ ...account, provider: "codex-agent-identity", account_type: "personal_access_token" }} onClose={() => undefined} onEdit={() => undefined} />);
  expect(screen.getByText("Codex PAT")).toBeInTheDocument();
});

it("shows credit accounting metadata while preserving the token breakdown", () => {
  render(<AccountDetailsDialog
    account={{
      ...account,
      usage: {
        ...account.usage!,
        credit: {
          amount_usd: 0.00345,
          rated_requests: 2,
          unrated_requests: 1,
          started_at: "2026-08-12T08:00:00Z",
          pricing_updated_at: "2026-08-12T07:00:00Z",
          pricing_source: "Sub2API / Wei-Shaw model-price-repo",
        },
      },
    }}
    creditUsageEnabled
    onClose={() => undefined}
    onEdit={() => undefined}
  />);

  expect(screen.getByText("预估额度用量").parentElement).toHaveTextContent("$0.00345");
  expect(screen.getByText("已计价请求").parentElement).toHaveTextContent("2");
  expect(screen.getByText("未计价请求").parentElement).toHaveTextContent("1");
  expect(screen.getByText("价格来源").parentElement).toHaveTextContent("Sub2API / Wei-Shaw model-price-repo");
  expect(screen.getByText("Input").parentElement).toHaveTextContent("100");
  expect(screen.getByText("Output").parentElement).toHaveTextContent("40");
});

it("shows nano-USD credit totals in account details without rounding to zero", () => {
  render(<AccountDetailsDialog
    account={{
      ...account,
      usage: {
        ...account.usage!,
        credit: { amount_usd: 0.000000345, rated_requests: 1, unrated_requests: 0 },
      },
    }}
    creditUsageEnabled
    onClose={() => undefined}
    onEdit={() => undefined}
  />);

  expect(screen.getByText("预估额度用量").parentElement).toHaveTextContent("$0.000000345");
});

it("shows per-window overdraft credit only when both experiments are enabled", () => {
  const usage = {
    ...account.usage!,
    credit: { amount_usd: 0.0125, rated_requests: 3, unrated_requests: 1 },
    codex: {
      observed_at: "2026-08-12T08:00:00Z",
      five_hour: { used_percent: 100, overdraft_active: true, overdraft_amount_usd: 0.0065, overdraft_rated_requests: 1, overdraft_unrated_requests: 1 },
      seven_day: { used_percent: 100, overdraft_active: true, overdraft_amount_usd: 0.004, overdraft_rated_requests: 1, overdraft_unrated_requests: 0 },
    },
  };
  const { rerender } = render(<AccountDetailsDialog account={{ ...account, usage }} creditUsageEnabled weeklyOverdraftEnabled onClose={() => undefined} onEdit={() => undefined} />);

  expect(screen.getByText("5 小时透支费用").parentElement).toHaveTextContent("$0.0065");
  expect(screen.getByText("5 小时透支费用").parentElement).toHaveTextContent("已计入账号总费用");
  expect(screen.getByText("7 天透支费用").parentElement).toHaveTextContent("$0.004");

  rerender(<AccountDetailsDialog account={{ ...account, usage }} creditUsageEnabled weeklyOverdraftEnabled={false} onClose={() => undefined} onEdit={() => undefined} />);
  expect(screen.queryByText("5 小时透支费用")).not.toBeInTheDocument();
});

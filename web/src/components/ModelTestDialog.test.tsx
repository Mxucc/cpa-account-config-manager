import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { Account, ModelTestResult } from "../types";
import { ModelTestDialog } from "./ModelTestDialog";

const account: Account = {
  id: "account-1",
  name: "EdwardGreen7768Nyx@outlook.com",
  provider: "codex",
  type: "oauth",
  disabled: false,
  unavailable: false,
  runtime_only: false,
  proxy_configured: false,
  header_count: 0,
  editable: true,
  success: 0,
  failed: 1,
};

const result: ModelTestResult = {
  account_id: account.id,
  provider: "codex",
  model: "gpt-5.4",
  status: "review",
  probe_kind: "credential",
  reason_code: "quota_limited",
  status_code: 429,
  quota_window: "five_hour",
  latency_ms: 765,
  tested_at: "2026-07-22T05:54:00Z",
  response: {
    format: "json",
    body: "{\n  \"error\": {\n    \"message\": \"Rate limit reached; retry later\",\n    \"type\": \"rate_limit_error\",\n    \"code\": \"rate_limit_exceeded\"\n  }\n}",
    headers: [
      { name: "retry-after", value: "30" },
      { name: "x-request-id", value: "req-safe-123" },
    ],
    truncated: true,
  },
};

describe("ModelTestDialog", () => {
  it("shows the diagnostic upstream response instead of only a generic classification", () => {
    render(<ModelTestDialog account={account} result={result} error="" testing={false} onClose={vi.fn()} onTest={vi.fn()} />);

    const dialog = screen.getByRole("dialog", { name: "模型可用性测试" });
    expect(within(dialog).getByText("429")).toBeInTheDocument();
    expect(within(dialog).getByText("凭据探测")).toBeInTheDocument();
    expect(within(dialog).getByText("5 小时窗口")).toBeInTheDocument();
    expect(within(dialog).getByText("上游实际响应")).toBeInTheDocument();
    expect(within(dialog).getByText("已脱敏的诊断响应")).toBeInTheDocument();
    expect(within(dialog).getByText("JSON · 已截断")).toBeInTheDocument();
    expect(within(dialog).getByText("retry-after")).toBeInTheDocument();
    expect(within(dialog).getByText("30")).toBeInTheDocument();
    expect(within(dialog).getByText("x-request-id")).toBeInTheDocument();
    expect(within(dialog).getByText("req-safe-123")).toBeInTheDocument();

    const responseBody = within(dialog).getByLabelText("响应正文");
    expect(responseBody).toHaveTextContent("Rate limit reached; retry later");
    expect(responseBody).toHaveTextContent("rate_limit_error");
    expect(responseBody).toHaveTextContent("rate_limit_exceeded");
  });

  it("makes an empty upstream response explicit", () => {
    render(<ModelTestDialog
      account={account}
      result={{ ...result, status_code: 204, response: { format: "empty", body: "", headers: [], truncated: false } }}
      error=""
      testing={false}
      onClose={vi.fn()}
      onTest={vi.fn()}
    />);

    const dialog = screen.getByRole("dialog", { name: "模型可用性测试" });
    expect(within(dialog).getByText("EMPTY")).toBeInTheDocument();
    expect(within(dialog).getByLabelText("响应正文")).toHaveTextContent("响应正文为空");
  });

  it("runs an enabled experimental probe and shows its fresh correlation ID", async () => {
    const user = userEvent.setup();
    const onTest = vi.fn();
    render(<ModelTestDialog
      account={account}
      result={{ ...result, experiment: { name: "weekly_overdraft", applied: true, call_id: "call_cpa_overdraft_fresh123" } }}
      error=""
      testing={false}
      experimentalAvailable
      onClose={vi.fn()}
      onTest={onTest}
    />);

    const dialog = screen.getByRole("dialog", { name: "模型可用性测试" });
    expect(within(dialog).getByText("已加载实验请求")).toBeInTheDocument();
    expect(within(dialog).getByText("call_cpa_overdraft_fresh123")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "加载实验性功能" }));
    expect(onTest).toHaveBeenCalledWith("gpt-5.4", true);
  });
});

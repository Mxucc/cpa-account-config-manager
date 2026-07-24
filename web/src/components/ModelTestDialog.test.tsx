import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Account, ModelTestResult } from "../types";
import { recordManualModelTestModel } from "../store/manualModelTestModel";
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
  beforeEach(() => localStorage.clear());

  it("shows the primary failure and successful gpt-5.5 fallback as one test flow", async () => {
    const user = userEvent.setup();
    const fallbackResult: ModelTestResult = {
      account_id: account.id,
      provider: "codex",
      model: "gpt-5.5",
      primary_model: "gpt-5.6-sol",
      fallback_model: "gpt-5.5",
      selected_model: "gpt-5.5",
      fallback_used: true,
      status: "available",
      probe_kind: "model",
      reason_code: "model_response_ok",
      status_code: 200,
      latency_ms: 1240,
      tested_at: "2026-07-23T08:05:00Z",
      attempts: [
        {
          model: "gpt-5.6-sol",
          role: "primary",
          status: "unavailable",
          probe_kind: "model",
          reason_code: "model_not_found",
          status_code: 400,
          latency_ms: 1005,
          tested_at: "2026-07-23T08:05:00Z",
          response: {
            format: "json",
            body: "{\n  \"detail\": \"The 'gpt-5.6-sol' model is not supported when using Codex with a ChatGPT account.\"\n}",
            headers: [{ name: "cf-ray", value: "safe-ray-id" }],
            truncated: false,
          },
        },
        {
          model: "gpt-5.5",
          role: "fallback",
          status: "available",
          probe_kind: "model",
          reason_code: "model_response_ok",
          status_code: 200,
          latency_ms: 235,
          tested_at: "2026-07-23T08:05:01Z",
          response: {
            format: "sse",
            body: "event: response.completed\ndata:\n{\n  \"type\": \"response.completed\",\n  \"response\": {\n    \"output\": [{\"content\": [{\"text\": \"OK\"}]}]\n  }\n}",
            headers: [],
            truncated: false,
          },
        },
      ],
    };
    render(<ModelTestDialog account={account} result={fallbackResult} error="" testing={false} onClose={vi.fn()} onTest={vi.fn()} />);

    const dialog = screen.getByRole("dialog", { name: "模型可用性测试" });
    expect(within(dialog).getByText("主模型不受支持，账号已通过 gpt-5.5 验证可用")).toBeInTheDocument();
    expect(within(dialog).getByText("主模型")).toBeInTheDocument();
    expect(within(dialog).getByText("回退模型")).toBeInTheDocument();
    expect(within(dialog).getByText("可用模型")).toBeInTheDocument();
    const attempts = within(dialog).getByRole("list", { name: "模型探测过程" });
    const rows = within(attempts).getAllByRole("listitem");
    expect(rows).toHaveLength(2);
    expect(within(rows[0]).getByText("主模型探测")).toBeInTheDocument();
    expect(within(rows[0]).getByText("gpt-5.6-sol")).toBeInTheDocument();
    expect(within(rows[0]).getByText("HTTP 400")).toBeInTheDocument();
    expect(within(rows[1]).getByText("回退模型探测")).toBeInTheDocument();
    expect(within(rows[1]).getByText("gpt-5.5")).toBeInTheDocument();
    expect(within(rows[1]).getByText("HTTP 200")).toBeInTheDocument();
    await user.click(within(rows[0]).getByText("查看脱敏后的上游响应"));
    await user.click(within(rows[1]).getByText("查看脱敏后的上游响应"));
    expect(within(rows[0]).getByLabelText("响应正文")).toHaveTextContent("not supported");
    expect(within(rows[1]).getByText("SSE")).toBeInTheDocument();
    expect(within(rows[1]).getByLabelText("响应正文")).toHaveTextContent('"type": "response.completed"');
    expect(within(rows[1]).getByLabelText("响应正文")).toHaveTextContent('"text": "OK"');
  });

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

  it("decodes CPA-host character references as inert response text", () => {
    render(<ModelTestDialog
      account={{ ...account, provider: "codex-agent-identity", plan_type: "k12" }}
      result={{
        ...result,
        model: "gpt-5.6-sol",
        probe_kind: "model",
        experiment: { name: "weekly_overdraft", applied: true, call_id: "call_cpa_overdraft_2f026e0867cc9a9400c58a07" },
        response: {
          format: "json",
          body: "{\n  &#34;error&#34;: {\n    &#34;_omitted_fields&#34;: 4,\n    &#34;message&#34;: &#34;The usage limit has been reached &lt;img src=x onerror=alert(1)&gt;&#34;,\n    &#34;type&#34;: &#34;usage_limit_reached&#34;\n  }\n}",
          headers: [
            { name: "cf-ray", value: "a1f9ebf56c42e3c4-IAD" },
            { name: "content-type", value: "application/json" },
          ],
          truncated: false,
        },
      }}
      error=""
      testing={false}
      experimentalAvailable
      onClose={vi.fn()}
      onTest={vi.fn()}
    />);

    const responseBody = within(screen.getByRole("dialog", { name: "模型可用性测试" })).getByLabelText("响应正文");
    expect(responseBody).toHaveTextContent('"error":');
    expect(responseBody).toHaveTextContent('"message": "The usage limit has been reached <img src=x onerror=alert(1)>"');
    expect(responseBody).toHaveTextContent('"type": "usage_limit_reached"');
    expect(responseBody).not.toHaveTextContent("&#34;");
    expect(responseBody.querySelector("img")).toBeNull();
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
    expect(onTest).toHaveBeenCalledWith("gpt-5.6-sol", true);
  });

  it("uses Codex defaults and experimental controls for Agent Identity accounts", async () => {
    const user = userEvent.setup();
    const onTest = vi.fn();
    render(<ModelTestDialog
      account={{ ...account, provider: "codex-agent-identity", account_type: "agent_identity", plan_type: "k12" }}
      result={null}
      error=""
      testing={false}
      experimentalAvailable
      onClose={vi.fn()}
      onTest={onTest}
    />);

    const dialog = screen.getByRole("dialog", { name: "模型可用性测试" });
    expect(within(dialog).getByLabelText("测试模型")).toHaveValue("gpt-5.6-sol");
    expect(within(dialog).getByText("codex-agent-identity", { exact: false })).toBeInTheDocument();
    expect(within(dialog).getByText("实验测试会使用新的关联工具调用编号发起真实 Codex 模型探测，并显示脱敏后的上游响应。")).toBeInTheDocument();

    await user.click(within(dialog).getByRole("button", { name: "开始测试" }));
    expect(onTest).toHaveBeenCalledWith("gpt-5.6-sol", false);
    await user.click(within(dialog).getByRole("button", { name: "加载实验性功能" }));
    expect(onTest).toHaveBeenCalledWith("gpt-5.6-sol", true);
  });

  it("restores the latest tested model and keeps tested models in the dropdown", async () => {
    const user = userEvent.setup();
    const onTest = vi.fn();
    recordManualModelTestModel("codex", "gpt-5.4");
    recordManualModelTestModel("codex", "gpt-5.5");

    const first = render(<ModelTestDialog
      account={account}
      result={null}
      error=""
      testing={false}
      onClose={vi.fn()}
      onTest={onTest}
    />);
    const dialog = screen.getByRole("dialog", { name: "模型可用性测试" });
    const modelInput = within(dialog).getByLabelText("测试模型");
    expect(modelInput).toHaveValue("gpt-5.5");
    expect(dialog.querySelector('option[value="gpt-5.5"]')).not.toBeNull();
    expect(dialog.querySelector('option[value="gpt-5.4"]')).not.toBeNull();

    await user.clear(modelInput);
    await user.type(modelInput, "  gpt-5.3-codex  ");
    await user.click(within(dialog).getByRole("button", { name: "开始测试" }));
    expect(onTest).toHaveBeenCalledWith("gpt-5.3-codex", false);
    first.unmount();

    render(<ModelTestDialog account={account} result={null} error="" testing={false} onClose={vi.fn()} onTest={vi.fn()} />);
    const reopened = screen.getByRole("dialog", { name: "模型可用性测试" });
    expect(within(reopened).getByLabelText("测试模型")).toHaveValue("gpt-5.3-codex");
    expect(reopened.querySelector('option[value="gpt-5.3-codex"]')).not.toBeNull();
    expect(reopened.querySelector('option[value="gpt-5.5"]')).not.toBeNull();
    expect(reopened.querySelector('option[value="gpt-5.4"]')).not.toBeNull();
  });
});

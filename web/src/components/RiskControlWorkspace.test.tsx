import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { RiskControlSnapshot } from "../types";
import { _resetSessionForTest, setSession } from "../store/session";
import { RiskControlWorkspace } from "./RiskControlWorkspace";

const defaultPrompt = { id: "default-security-audit", name: "Default security audit", system_prompt: "Default security prompt", builtin: true };
const snapshot: RiskControlSnapshot = {
  config: {
    enabled: false,
    mode: "off",
    blocked_keywords: [],
    model_filter: { mode: "all", models: [] },
    pre_hash_check_enabled: true,
    block_status: 403,
    block_message: "blocked",
    event_retention_days: 30,
    max_events: 500,
    audit: { enabled: false, mode: "off", endpoint: "", model: "", api_key: "", api_key_set: false, scanners: [], latest_turn_only: true, store_pass_events: false, timeout_ms: 3000, input_limit: 32768, worker_count: 2, queue_capacity: 128, failure_policy: "fail_open", block_status: 403, block_message: "audit blocked", confidence_threshold: 0.8, prompt_id: defaultPrompt.id },
    system_prompts: [defaultPrompt],
  },
  status: { active: false, mode: "off", total_events: 0, observed: 0, blocked: 0, keyword_hits: 0, hash_hits: 0, remembered_hashes: 0, audit: { active: false, mode: "off", queue_length: 0, queue_capacity: 128, worker_count: 2, processed: 0, blocked: 0, errors: 0, dropped: 0, api_key_configured: false, api_key_available: false } },
  events: [],
};

beforeEach(() => {
  _resetSessionForTest();
  localStorage.clear();
  setSession("", "management-secret");
  vi.restoreAllMocks();
});

function respond(body: RiskControlSnapshot = snapshot) {
  return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
}

describe("RiskControlWorkspace", () => {
  it("normalizes nullable persisted lists without crashing", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(respond({
      ...snapshot,
      config: { ...snapshot.config, blocked_keywords: null as unknown as string[], model_filter: { mode: "all", models: null as unknown as string[] }, audit: { ...snapshot.config.audit, scanners: null as unknown as string[] }, system_prompts: null as unknown as typeof snapshot.config.system_prompts },
      events: null as unknown as typeof snapshot.events,
    }));

    render(<RiskControlWorkspace onAPIError={vi.fn()} onNotice={vi.fn()} />);
    const workspace = await screen.findByRole("region", { name: "风控中心" });
    expect(within(workspace).getByLabelText("阻断关键词")).toHaveValue("");
    expect(within(workspace).getByText("暂无风控事件。")).toBeInTheDocument();
  });

  it("saves normalized content settings and audit api key without exposing it", async () => {
    const user = userEvent.setup();
    const requests: Array<{ url: string; init?: RequestInit }> = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      requests.push({ url, init });
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body));
        return respond({ ...snapshot, config: { ...snapshot.config, ...body, audit: { ...snapshot.config.audit, ...body.audit, api_key: "", api_key_set: true } }, status: { ...snapshot.status, audit: { ...snapshot.status.audit, api_key_configured: true, api_key_available: true } } });
      }
      return respond();
    });

    render(<RiskControlWorkspace onAPIError={vi.fn()} onNotice={vi.fn()} />);
    const workspace = await screen.findByRole("region", { name: "风控中心" });
    await user.click(within(workspace).getByRole("switch", { name: "启用风控" }));
    await user.selectOptions(within(workspace).getByLabelText("运行模式"), "observe");
    await user.type(within(workspace).getByLabelText("阻断关键词"), "danger");
    await user.click(within(workspace).getByRole("tab", { name: "提示词审计" }));
    const apiKey = within(workspace).getByLabelText("API Key");
    await user.type(apiKey, "private-audit-key");
    await user.click(within(workspace).getByRole("button", { name: "保存" }));

    await waitFor(() => expect(requests.some(({ init }) => init?.method === "PUT")).toBe(true));
    const putRequest = requests.find(({ init }) => init?.method === "PUT");
    const body = JSON.parse(String(putRequest?.init?.body));
    expect(body).toMatchObject({ enabled: true, mode: "observe", blocked_keywords: ["danger"], audit: { api_key: "private-audit-key" } });
    expect(screen.queryByText("private-audit-key")).not.toBeInTheDocument();
  });

  it("supports prompt catalog add, edit, delete, and protects the default prompt", async () => {
    const user = userEvent.setup();
    vi.spyOn(globalThis, "fetch").mockResolvedValue(respond());
    render(<RiskControlWorkspace onAPIError={vi.fn()} onNotice={vi.fn()} />);
    const workspace = await screen.findByRole("region", { name: "风控中心" });
    await user.click(within(workspace).getByRole("tab", { name: "提示词审计" }));
    const list = within(workspace).getByLabelText("系统提示词列表");
    expect(within(workspace).getByRole("button", { name: "删除提示词" })).toBeDisabled();
    expect(within(workspace).getAllByDisplayValue("Default security audit")[1]).toBeDisabled();
    await user.click(within(workspace).getByRole("button", { name: "新增提示词" }));
    expect(within(workspace).getAllByDisplayValue("新提示词")[1]).toBeInTheDocument();
    await user.type(within(workspace).getAllByDisplayValue("新提示词")[1], " custom");
    await user.type(within(workspace).getByLabelText("系统提示词"), "custom body");
    expect(list.value).toMatch(/^custom-/);
    expect(within(workspace).getByRole("button", { name: "删除提示词" })).not.toBeDisabled();
  });
});

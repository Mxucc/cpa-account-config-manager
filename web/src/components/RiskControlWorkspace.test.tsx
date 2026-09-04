import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api/client";
import { _resetSessionForTest, setSession } from "../store/session";
import type { RiskControlSnapshot } from "../types";
import { RiskControlWorkspace } from "./RiskControlWorkspace";

const snapshot: RiskControlSnapshot = {
  config: { enabled: false, mode: "off", blocked_keywords: [], model_filter: { mode: "all", models: [] }, pre_hash_check_enabled: true, block_status: 403, block_message: "blocked", event_retention_days: 30, max_events: 500, prompt_audit: { enabled: false, mode: "off", endpoint: "", model: "", credential_env: "", scanners: [], latest_turn_only: true, store_pass_events: false, timeout_ms: 3000, input_limit: 32768, worker_count: 2, queue_capacity: 128, failure_policy: "fail_open", block_status: 403, block_message: "prompt blocked" }, custom_audit: { enabled: false, mode: "off", endpoint: "", model: "", credential_env: "", scanners: [], latest_turn_only: true, store_pass_events: false, timeout_ms: 3000, input_limit: 32768, worker_count: 2, queue_capacity: 128, failure_policy: "fail_open", block_status: 403, block_message: "custom blocked", confidence_threshold: 0.8, system_prompt: "" } },
  status: { active: false, mode: "off", total_events: 0, observed: 0, blocked: 0, keyword_hits: 0, hash_hits: 0, remembered_hashes: 0, prompt_audit: { active: false, mode: "off", queue_length: 0, queue_capacity: 128, worker_count: 2, processed: 0, blocked: 0, errors: 0, dropped: 0, credential_configured: false, credential_available: false }, custom_audit: { active: false, mode: "off", queue_length: 0, queue_capacity: 128, worker_count: 2, processed: 0, blocked: 0, errors: 0, dropped: 0, credential_configured: false, credential_available: false } },
  events: [],
};

beforeEach(() => {
  _resetSessionForTest();
  localStorage.clear();
  setSession("", "management-secret");
  vi.restoreAllMocks();
});

describe("RiskControlWorkspace", () => {
  it("loads, saves normalized lists, and clears events and hashes", async () => {
    const user = userEvent.setup();
    const requests: Array<{ url: string; init?: RequestInit }> = [];
    const getSnapshot = (): RiskControlSnapshot => ({ ...snapshot, config: { ...snapshot.config }, status: { ...snapshot.status }, events: [] });
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      requests.push({ url, init });
      if (init?.method === "PUT") return new Response(JSON.stringify({ ...getSnapshot(), config: { ...snapshot.config, enabled: true, mode: "observe", blocked_keywords: ["danger", "exfiltrate"], model_filter: { mode: "include", models: ["gpt-5"] } } }), { status: 200, headers: { "Content-Type": "application/json" } });
      if (url.endsWith("/events")) return new Response(JSON.stringify(getSnapshot()), { status: 200, headers: { "Content-Type": "application/json" } });
      if (url.endsWith("/hashes")) return new Response(JSON.stringify(getSnapshot()), { status: 200, headers: { "Content-Type": "application/json" } });
      return new Response(JSON.stringify({ ...getSnapshot(), config: { ...snapshot.config, blocked_keywords: ["danger"] } }), { status: 200, headers: { "Content-Type": "application/json" } });
    });

    render(<RiskControlWorkspace onAPIError={vi.fn()} onNotice={vi.fn()} />);
    const workspace = await screen.findByRole("region", { name: "风控中心" });
    expect(within(workspace).getByText("策略未启用")).toBeInTheDocument();

    await user.click(within(workspace).getByRole("switch", { name: "启用风控" }));
    await user.selectOptions(within(workspace).getByLabelText("运行模式"), "observe");
    await user.type(within(workspace).getByLabelText("阻断关键词"), "\nexfiltrate");
    await user.click(within(workspace).getByRole("button", { name: "保存" }));
    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/risk-control") && init?.method === "PUT")).toBe(true));
    const putRequest = requests.find(({ init }) => init?.method === "PUT");
    expect(JSON.parse(String(putRequest?.init?.body))).toMatchObject({ enabled: true, mode: "observe", blocked_keywords: ["danger", "exfiltrate"] });

    await user.click(within(workspace).getByRole("button", { name: "清空哈希" }));
    await user.click(within(workspace).getByRole("button", { name: "清空事件" }));
    await waitFor(() => expect(requests.filter(({ init }) => init?.method === "DELETE")).toHaveLength(2));
    expect(requests.some(({ url, init }) => url.endsWith("/risk-control/hashes") && init?.method === "DELETE")).toBe(true);
    expect(requests.some(({ url, init }) => url.endsWith("/risk-control/events") && init?.method === "DELETE")).toBe(true);
  });
});

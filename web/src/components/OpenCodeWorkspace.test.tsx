import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { _resetSessionForTest, setSession } from "../store/session";
import { OpenCodeWorkspace } from "./OpenCodeWorkspace";

const GO_ACCOUNT_ID = "acc_go_1";
const GO_WORKSPACE = "wrk_test";
const ZEN_ACCOUNT_ID = "zen_1";
// Canary that must never be rendered: responses only ever expose `key_set`.
const UNRENDERED_SECRET = "sk-opencode-canary-secret-1234";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function goAccountView(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    id: GO_ACCOUNT_ID,
    workspace_id: GO_WORKSPACE,
    key_set: true,
    models: ["gpt-5.1", "claude-sonnet-4", "gemini-2.5-pro", "o4-mini"],
    models_error: "",
    models_fetched_at: "2026-09-01T00:00:00Z",
    created_at: "2026-09-01T00:00:00Z",
    updated_at: "2026-09-01T00:00:00Z",
    ...overrides,
  };
}

function zenAccountView(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    id: ZEN_ACCOUNT_ID,
    name: "Zen mirror",
    base_url: "https://opencode.ai/zen",
    key_set: true,
    models: ["zen-model-a"],
    models_error: "",
    models_fetched_at: "2026-09-01T00:00:00Z",
    ...overrides,
  };
}

interface OpenCodeFetchMockOptions {
  accounts?: Array<Record<string, unknown>>;
  accountsStatus?: number;
  accountsErrorBody?: Record<string, unknown>;
  zenAccounts?: Array<Record<string, unknown>>;
  quota?: Record<string, Record<string, unknown>>;
  quotaRefresh?: Record<string, unknown>;
  modelsResponse?: Record<string, unknown>;
  modelTestResponse?: Record<string, unknown>;
  bindResponse?: Record<string, unknown>;
  pricing?: Record<string, unknown>;
  pricingRefresh?: Record<string, unknown>;
  session?: Record<string, unknown>;
}

describe("OpenCodeWorkspace", () => {
  beforeEach(() => {
    _resetSessionForTest();
    localStorage.clear();
    setSession("", "management-secret");
    vi.restoreAllMocks();
  });

  function openCodeFetchMock(options: OpenCodeFetchMockOptions = {}) {
    const requests: Array<{ url: string; init: RequestInit }> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      requests.push({ url, init });
      if (url.endsWith("/opencode/zen/accounts") && init.method === "POST") {
        return jsonResponse({ account: zenAccountView(), result: { success: true } });
      }
      if (url.endsWith("/opencode/zen/accounts")) return jsonResponse({ accounts: options.zenAccounts ?? [] });
      if (url.endsWith("/opencode/accounts") && init.method === "POST") {
        return jsonResponse({ account: goAccountView(), result: { success: true } });
      }
      if (url.includes("/opencode/accounts?") && init.method === "DELETE") return jsonResponse({ removed: true });
      if (url.endsWith("/opencode/accounts")) {
        if (options.accountsStatus) {
          return jsonResponse(options.accountsErrorBody ?? { error: "opencode accounts failed" }, options.accountsStatus);
        }
        return jsonResponse({ accounts: options.accounts ?? [goAccountView()] });
      }
      if (url.includes("/opencode/refresh-account?") && init.method === "POST") {
        return jsonResponse({ result: options.quotaRefresh ?? {} });
      }
      if (url.endsWith("/opencode/quota")) return jsonResponse({ results: options.quota ?? {}, storage_error: "" });
      if (url.endsWith("/opencode/models")) return jsonResponse(options.modelsResponse ?? { account: goAccountView() });
      if (url.endsWith("/opencode/model-test")) return jsonResponse(options.modelTestResponse ?? {});
      if (url.endsWith("/opencode/bind")) return jsonResponse(options.bindResponse ?? {});
      if (url.endsWith("/opencode/pricing/refresh") && init.method === "POST") {
        return jsonResponse({ changed: true, pricing: options.pricingRefresh ?? options.pricing ?? {} });
      }
      if (url.endsWith("/opencode/pricing")) return jsonResponse({ pricing: options.pricing ?? {} });
      if (url.endsWith("/opencode/session")) return jsonResponse({ session: options.session ?? {} });
      return jsonResponse({});
    });
    vi.stubGlobal("fetch", fetchMock);
    return requests;
  }

  async function findGoRow(section: HTMLElement): Promise<HTMLElement> {
    return waitFor(() => {
      const found = Array.from(section.querySelectorAll(".opencode-table tbody tr"))
        .find((row) => row.textContent?.includes(GO_WORKSPACE));
      expect(found).toBeDefined();
      return found as HTMLElement;
    });
  }

  it("renders a Go workspace row with its quota windows and model count from the initial load", async () => {
    const requests = openCodeFetchMock({
      quota: {
        [GO_ACCOUNT_ID]: {
          success: true,
          rolling: { usage_percent: 42.5, reset_in_sec: 3600 },
          weekly: { usage_percent: 10, reset_in_sec: 7200 },
          monthly: { usage_percent: 3.25, reset_in_sec: 0 },
        },
      },
    });

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const section = await screen.findByRole("region", { name: "OpenCode Go 工作区" });
    const row = await findGoRow(section);
    expect(within(row).getByText(GO_WORKSPACE)).toBeInTheDocument();
    expect(within(row).getByText(/5 小时额度: 42\.5% · 60 分钟/)).toBeInTheDocument();
    expect(within(row).getByText(/7 天额度: 10\.0% · 120 分钟/)).toBeInTheDocument();
    expect(within(row).getByText(/30 天额度: 3\.3% · -/)).toBeInTheDocument();
    expect(within(row).getByText("4")).toBeInTheDocument();
    expect(row.textContent).toContain("gpt-5.1, claude-sonnet-4, gemini-2.5-pro …");

    expect(requests.some(({ url }) => url.endsWith("/opencode/accounts"))).toBe(true);
    expect(requests.some(({ url }) => url.endsWith("/opencode/zen/accounts"))).toBe(true);
    expect(requests.some(({ url }) => url.endsWith("/opencode/quota"))).toBe(true);
  });

  it("never renders a stored API key value and only shows the key status", async () => {
    openCodeFetchMock({ accounts: [goAccountView({ key_set: true })] });

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const section = await screen.findByRole("region", { name: "OpenCode Go 工作区" });
    const row = await findGoRow(section);
    expect(within(row).getByText("已保存密钥")).toBeInTheDocument();
    expect(within(row).queryByText("未设置密钥")).not.toBeInTheDocument();
    const keyInput = within(row).getByLabelText(`${GO_WORKSPACE} 的 OpenCode API 密钥`);
    expect(keyInput).toHaveValue("");
    for (const input of Array.from(document.querySelectorAll<HTMLInputElement>('input[type="password"]'))) {
      expect(input.value).toBe("");
    }
    expect(document.body.textContent).not.toContain(UNRENDERED_SECRET);
  });

  it("saves a Go API key through the accounts route and reports the saved notice", async () => {
    const user = userEvent.setup();
    const requests = openCodeFetchMock({ accounts: [goAccountView({ key_set: false })] });
    const onNotice = vi.fn();

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={onNotice} />);

    const section = await screen.findByRole("region", { name: "OpenCode Go 工作区" });
    const row = await findGoRow(section);
    expect(within(row).getByText("未设置密钥")).toBeInTheDocument();

    const keyInput = within(row).getByLabelText(`${GO_WORKSPACE} 的 OpenCode API 密钥`);
    await user.type(keyInput, "sk-go-replacement-4321");
    await user.click(within(row).getByRole("button", { name: "保存" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/opencode/accounts") && init.method === "POST")).toBe(true));
    const request = requests.find(({ url, init }) => url.endsWith("/opencode/accounts") && init.method === "POST");
    expect(JSON.parse(String(request?.init.body))).toEqual({ account_id: GO_ACCOUNT_ID, api_key: "sk-go-replacement-4321" });
    await waitFor(() => expect(onNotice).toHaveBeenCalledWith("OpenCode API 密钥已保存"));
    await waitFor(() => expect(keyInput).toHaveValue(""));
  });

  it("loads models through the models route and updates the model count with the loaded notice", async () => {
    const user = userEvent.setup();
    const requests = openCodeFetchMock({
      accounts: [goAccountView({ models: [] })],
      modelsResponse: { account: goAccountView({ models: ["gpt-5.1", "claude-sonnet-4", "gemini-2.5-pro"] }) },
    });
    const onNotice = vi.fn();

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={onNotice} />);

    const section = await screen.findByRole("region", { name: "OpenCode Go 工作区" });
    const row = await findGoRow(section);
    expect(row.textContent).toContain("先拉取模型目录，才能进行模型测试与 CPA 绑定。");

    await user.click(within(row).getByRole("button", { name: `拉取 ${GO_WORKSPACE} 的模型列表` }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/opencode/models") && init.method === "POST")).toBe(true));
    const request = requests.find(({ url, init }) => url.endsWith("/opencode/models") && init.method === "POST");
    expect(JSON.parse(String(request?.init.body))).toEqual({ kind: "go", account_id: GO_ACCOUNT_ID });
    await waitFor(() => expect(within(row).getByText("3")).toBeInTheDocument());
    expect(row.textContent).toContain("gpt-5.1, claude-sonnet-4, gemini-2.5-pro");
    await waitFor(() => expect(onNotice).toHaveBeenCalledWith("已加载 3 个模型"));
  });

  it("runs a real model test after loading models and renders the status and reason code", async () => {
    const user = userEvent.setup();
    const requests = openCodeFetchMock({
      accounts: [goAccountView({ models: [] })],
      modelsResponse: { account: goAccountView({ models: ["gpt-5.1", "claude-sonnet-4"] }) },
      modelTestResponse: {
        result: {
          status: "unavailable",
          reason_code: "model_not_found",
          status_code: 404,
          latency_ms: 123,
          tested_at: "2026-09-01T00:00:00Z",
          detail: "",
        },
      },
    });

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const section = await screen.findByRole("region", { name: "OpenCode Go 工作区" });
    const row = await findGoRow(section);
    await user.click(within(row).getByRole("button", { name: `拉取 ${GO_WORKSPACE} 的模型列表` }));
    await waitFor(() => expect(within(row).getByText("2")).toBeInTheDocument());

    await user.click(within(row).getByRole("button", { name: `测试 ${GO_WORKSPACE} 的模型` }));
    const tester = await screen.findByRole("region", { name: "模型测试" });
    expect(within(tester).getByRole("combobox")).toHaveValue("gpt-5.1");
    await user.click(within(tester).getByRole("button", { name: "测试" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/opencode/model-test") && init.method === "POST")).toBe(true));
    const request = requests.find(({ url, init }) => url.endsWith("/opencode/model-test") && init.method === "POST");
    expect(JSON.parse(String(request?.init.body))).toEqual({
      kind: "go",
      account_id: GO_ACCOUNT_ID,
      model: "gpt-5.1",
      timeout_seconds: 30,
    });
    expect(await within(tester).findByText("模型不可用")).toBeInTheDocument();
    expect(within(tester).getByText("model_not_found")).toBeInTheDocument();
  });

  it("binds a Go workspace through the bind route and reports the channel base URL", async () => {
    const user = userEvent.setup();
    const requests = openCodeFetchMock({
      bindResponse: {
        binding: {
          kind: "go",
          base_url: "https://opencode.ai/zen/go/v1",
          index: 0,
          created: true,
          channel_key: "sk-cpa-channel-1",
        },
      },
    });
    const onNotice = vi.fn();

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={onNotice} />);

    const section = await screen.findByRole("region", { name: "OpenCode Go 工作区" });
    const row = await findGoRow(section);
    await user.click(within(row).getByRole("button", { name: `将 ${GO_WORKSPACE} 的模型发布到 CPA 路由` }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/opencode/bind") && init.method === "POST")).toBe(true));
    const request = requests.find(({ url, init }) => url.endsWith("/opencode/bind") && init.method === "POST");
    expect(JSON.parse(String(request?.init.body))).toEqual({ kind: "go", account_id: GO_ACCOUNT_ID });
    await waitFor(() => expect(onNotice).toHaveBeenCalledWith(expect.stringContaining("https://opencode.ai/zen/go/v1")));
  });

  it("surfaces an inline error for a failed initial load instead of crashing", async () => {
    openCodeFetchMock({
      accountsStatus: 500,
      accountsErrorBody: { error: "opencode accounts storage unavailable" },
    });

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("opencode accounts storage unavailable");
    expect(screen.getByText("OpenCode Go 与 Zen 控制器")).toBeInTheDocument();
  });

  it("refreshes one workspace quota through the per-account route", async () => {
    const user = userEvent.setup();
    const requests = openCodeFetchMock({
      quotaRefresh: {
        success: true,
        rolling: { usage_percent: 5, reset_in_sec: 300 },
        weekly: { usage_percent: 6, reset_in_sec: 600 },
        monthly: { usage_percent: 7, reset_in_sec: 900 },
      },
    });

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const section = await screen.findByRole("region", { name: "OpenCode Go 工作区" });
    const row = await findGoRow(section);
    await user.click(within(row).getByRole("button", { name: "刷新 OpenCode 额度" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.includes(`/opencode/refresh-account?account_id=${GO_ACCOUNT_ID}`) && init.method === "POST")).toBe(true));
    expect(await within(row).findByText(/5 小时额度: 5\.0% · 5 分钟/)).toBeInTheDocument();
  });

  it("renders the official OpenCode price catalog and syncs it on demand", async () => {
    const user = userEvent.setup();
    const requests = openCodeFetchMock({
      pricing: {
        source: "models.dev (OpenCode Zen and OpenCode Go)",
        updated_at: "2026-09-11T00:00:00Z",
        go: [{ id: "longcat-2.0", name: "LongCat-2.0", input_usd_per_million: 0.3, output_usd_per_million: 1.2, context_tokens: 1000000 }],
        zen: [{ id: "gemini-3.1-pro", name: "Gemini 3.1 Pro", input_usd_per_million: 2, output_usd_per_million: 12, cache_read_usd_per_million: 0.2, tiers: [{ min_context_tokens: 200000, input_usd_per_million: 4 }] }],
      },
    });
    const onNotice = vi.fn();

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={onNotice} />);

    const section = await screen.findByRole("region", { name: "OpenCode 官方价格" });
    expect(within(section).getByText(/^来源: models\.dev/)).toBeInTheDocument();
    expect(await within(section).findByText("LongCat-2.0")).toBeInTheDocument();
    expect(within(section).getByText("$0.30")).toBeInTheDocument();
    expect(within(section).getByText("$1.20")).toBeInTheDocument();

    // Switch to the Zen catalog: tiered pricing is announced, not hidden.
    await user.click(within(section).getByRole("button", { name: "OpenCode Zen" }));
    expect(await within(section).findByText("Gemini 3.1 Pro")).toBeInTheDocument();
    expect(within(section).getByText(/超过 200,000 token/)).toBeInTheDocument();

    await user.click(within(section).getByRole("button", { name: "同步价格" }));
    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/opencode/pricing/refresh") && init.method === "POST")).toBe(true));
    await waitFor(() => expect(onNotice).toHaveBeenCalledWith("价格已更新"));
  });

  it("shows the per-conversation session routing status", async () => {
    openCodeFetchMock({
      session: { enabled: true, salt_ready: true, target_models: ["a", "b", "c"], injected_requests: 7, distinct_sessions: 2 },
    });

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const section = await screen.findByRole("region", { name: "对话会话" });
    expect(await within(section).findByText("已启用")).toBeInTheDocument();
    expect(within(section).getByText("7")).toBeInTheDocument();
    expect(within(section).getByText("2")).toBeInTheDocument();
    expect(within(section).getByRole("link", { name: "OpenCode Go 客户端要求" })).toHaveAttribute("href", "https://opencode.ai/docs/go/#where-can-i-use-it");
  });
});

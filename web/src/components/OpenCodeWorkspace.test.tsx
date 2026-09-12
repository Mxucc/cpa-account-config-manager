import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { _resetSessionForTest, setSession } from "../store/session";
import { OpenCodeWorkspace } from "./OpenCodeWorkspace";

const GO_ACCOUNT_ID = "acc_go_1";
const GO_WORKSPACE = "wrk_test";
const ZEN_ACCOUNT_ID = "zen_1";
const CHANNEL_ZEN_BASE = "https://opencode.ai/zen/v1";
const CHANNEL_GO_BASE = "https://opencode.ai/zen/go/v1";
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
    cookie_set: true,
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

/** One row of `GET /opencode/channels`: an AI-provider channel that belongs to OpenCode. */
function channelView(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    kind: "zen",
    name: "Zen channel",
    base_url: CHANNEL_ZEN_BASE,
    key_set: true,
    models: 3,
    source: "ai_provider",
    imported: false,
    ...overrides,
  };
}

/** Pricing snapshot with the official-docs billing block and per-model Go allowances. */
function billingPricingFixture(): Record<string, unknown> {
  return {
    source: "models.dev (OpenCode Zen and OpenCode Go)",
    updated_at: "2026-09-11T00:00:00Z",
    docs_updated_at: "2026-09-12T00:00:00Z",
    billing: [
      {
        kind: "zen",
        metered: true,
        docs_url: "https://opencode.ai/docs/zen/",
        summary: "Pay-as-you-go per 1M tokens; balance auto-reloads below $5 (default $20) and a monthly workspace limit can cap spend.",
      },
      {
        kind: "go",
        metered: false,
        subscription_usd_per_month: 10,
        five_hour_fraction: 0.2,
        weekly_fraction: 0.5,
        docs_url: "https://opencode.ai/docs/go/",
        summary: "$10/month subscription; each model has a monthly USD allowance split 20% / 50% / 100%.",
      },
    ],
    go: [
      {
        id: "longcat-2.0",
        name: "LongCat-2.0",
        input_usd_per_million: 0.3,
        output_usd_per_million: 1.2,
        context_tokens: 1000000,
        monthly_limit_usd: 60,
        estimated_requests: { five_hour: 1830, weekly: 4580, monthly: 9150 },
      },
      {
        id: "legacy-mini",
        name: "Legacy Mini",
        input_usd_per_million: 1,
        output_usd_per_million: 2,
        context_tokens: 200000,
        monthly_limit_usd: 20,
        deprecated_at: "January 2026",
      },
    ],
    zen: [
      {
        id: "gemini-3.1-pro",
        name: "Gemini 3.1 Pro",
        input_usd_per_million: 2,
        output_usd_per_million: 12,
        cache_read_usd_per_million: 0.2,
        tiers: [{ min_context_tokens: 200000, input_usd_per_million: 4 }],
      },
    ],
  };
}

interface OpenCodeFetchMockOptions {
  accounts?: Array<Record<string, unknown>>;
  accountsStatus?: number;
  accountsErrorBody?: Record<string, unknown>;
  zenAccounts?: Array<Record<string, unknown>>;
  channels?: Array<Record<string, unknown>>;
  importResponse?: Record<string, unknown>;
  importStatus?: number;
  importErrorBody?: Record<string, unknown>;
  quota?: Record<string, Record<string, unknown>>;
  quotaRefresh?: Record<string, unknown>;
  modelsResponse?: Record<string, unknown>;
  modelTestResponse?: Record<string, unknown>;
  bindResponse?: Record<string, unknown>;
  pricing?: Record<string, unknown>;
  modelControl?: Record<string, unknown>;
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
    let disabledModels = [...((options.modelControl?.disabled as string[] | undefined) ?? [])];
    const controlRows = (disabled: string[]) => [
      { id: "qwen3.7-max", disabled: disabled.includes("qwen3.7-max"), accounts: 1, channels: 1, priced: true, input_usd_per_million: 2.5, output_usd_per_million: 7.5 },
      { id: "gpt-5.6-luna", disabled: disabled.includes("gpt-5.6-luna"), accounts: 1, channels: 0, priced: true, input_usd_per_million: 0.2, output_usd_per_million: 1.2 },
      { id: "unpriced-opencode", disabled: disabled.includes("unpriced-opencode"), accounts: 0, channels: 0, priced: false },
    ];
    const modelControlBody = (opts: OpenCodeFetchMockOptions) => ({
      storage_error: "",
      pricing_source: "Sub2API / Wei-Shaw model-price-repo",
      ...(opts.modelControl ?? {}),
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      requests.push({ url, init });
      if (url.endsWith("/opencode/zen/accounts") && init.method === "POST") {
        return jsonResponse({ account: zenAccountView(), result: { success: true } });
      }
      if (url.endsWith("/opencode/zen/accounts")) return jsonResponse({ accounts: options.zenAccounts ?? [] });
      if (url.endsWith("/opencode/channels")) return jsonResponse({ channels: options.channels ?? [] });
      if (url.endsWith("/opencode/import") && init.method === "POST") {
        if (options.importStatus) {
          return jsonResponse(options.importErrorBody ?? { error: "opencode import failed" }, options.importStatus);
        }
        return jsonResponse({
          import: options.importResponse ?? {
            kind: "zen",
            action: "create_zen",
            account_id: ZEN_ACCOUNT_ID,
            name: "Zen mirror",
            base_url: CHANNEL_ZEN_BASE,
          },
        });
      }
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
      if (url.endsWith("/opencode/model-control") && init.method === "PUT") {
        const body = JSON.parse(String(init.body ?? "{}")) as { disabled?: string[] };
        disabledModels = body.disabled ?? [];
        return jsonResponse({ ...modelControlBody(options), disabled: disabledModels, models: controlRows(disabledModels) });
      }
      if (url.endsWith("/opencode/model-control")) {
        return jsonResponse({ ...modelControlBody(options), disabled: disabledModels, models: controlRows(disabledModels) });
      }
      if (url.endsWith("/opencode/session")) return jsonResponse({ session: options.session ?? {} });
      return jsonResponse({});
    });
    vi.stubGlobal("fetch", fetchMock);
    return requests;
  }

  async function selectTab(user: ReturnType<typeof userEvent.setup>, name: string) {
    await user.click(screen.getByRole("tab", { name }));
  }

  async function findGoRow(section: HTMLElement): Promise<HTMLElement> {
    return waitFor(() => {
      const found = Array.from(section.querySelectorAll(".opencode-table tbody tr"))
        .find((row) => row.textContent?.includes(GO_WORKSPACE));
      expect(found).toBeDefined();
      return found as HTMLElement;
    });
  }

  function findChannelRow(panel: HTMLElement, name: string): HTMLElement {
    const cell = within(panel).getByText(name);
    const row = cell.closest("tr");
    expect(row).not.toBeNull();
    return row as HTMLElement;
  }

  it("renders the five workspace tabs and switches to the matching panel", async () => {
    const user = userEvent.setup();
    openCodeFetchMock();

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const tablist = await screen.findByRole("tablist", { name: "OpenCode" });
    expect(within(tablist).getAllByRole("tab").map((tab) => tab.textContent)).toEqual([
      "总览",
      "Go 账号",
      "Zen 账号",
      "渠道",
      "模型与价格",
    ]);
    expect(within(tablist).getByRole("tab", { name: "总览" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tabpanel", { name: "总览" })).toBeInTheDocument();
    expect(screen.queryByRole("tabpanel", { name: "Go 账号" })).not.toBeInTheDocument();

    await user.click(within(tablist).getByRole("tab", { name: "渠道" }));

    expect(within(tablist).getByRole("tab", { name: "渠道" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tabpanel", { name: "渠道" })).toBeInTheDocument();
    expect(screen.queryByRole("tabpanel", { name: "总览" })).not.toBeInTheDocument();
  });

  it("renders a Go workspace row with its quota windows and model count from the initial load", async () => {
    const user = userEvent.setup();
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

    await selectTab(user, "Go 账号");
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
    expect(requests.some(({ url }) => url.endsWith("/opencode/channels"))).toBe(true);
  });

  it("never renders a stored API key value and only shows the key status", async () => {
    const user = userEvent.setup();
    openCodeFetchMock({ accounts: [goAccountView({ key_set: true })] });

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    await selectTab(user, "Go 账号");
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

    await selectTab(user, "Go 账号");
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

  it("completes an incomplete credential in place and warns while it is incomplete", async () => {
    const user = userEvent.setup();
    // The workspace and cookie are stored, but the API key never was: the account cannot
    // reach the catalog, the model test, or the CPA route until it is completed.
    const requests = openCodeFetchMock({ accounts: [goAccountView({ key_set: false })] });
    const onNotice = vi.fn();

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={onNotice} />);

    await selectTab(user, "Go 账号");
    const section = await screen.findByRole("region", { name: "OpenCode Go 工作区" });
    expect(await within(section).findByText(/凭据不完整/)).toBeInTheDocument();

    await user.click(within(section).getByRole("button", { name: `编辑 ${GO_WORKSPACE} 的凭据` }));
    // Scope to the editor row: the key column also has a save button.
    const editorRow = within(section).getByLabelText("Auth Cookie（auth 的值）").closest("tr") as HTMLElement;
    const editor = within(editorRow).getByRole("button", { name: "保存" });
    const cookieField = within(editorRow).getByLabelText("Auth Cookie（auth 的值）");
    const keyField = within(editorRow).getByLabelText("OpenCode API 密钥");
    await user.type(cookieField, "Fe26.2*rotated-cookie");
    await user.type(keyField, "sk-completed-key");
    await user.click(editor);

    await waitFor(() => {
      const writes = requests.filter(({ url, init }) => url.endsWith("/opencode/accounts") && init.method === "POST");
      const body = JSON.parse(String(writes.at(-1)?.init.body));
      // Only the fields the operator filled are sent: the workspace stays as recorded.
      expect(body).toEqual({
        account_id: GO_ACCOUNT_ID,
        auth_cookie: "Fe26.2*rotated-cookie",
        api_key: "sk-completed-key",
      });
    });
    await waitFor(() => expect(onNotice).toHaveBeenCalledWith("凭据已更新"));
  });

  it("only warns about an incomplete credential when the stored cookie is really missing", async () => {
    const user = userEvent.setup();
    // The account was saved through the add form, so the backend reports cookie_set: true and
    // the row must stay quiet: losing cookie_set in the response normalizer used to warn about
    // every account even though the cookie was stored.
    openCodeFetchMock({ accounts: [goAccountView()] });

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    await selectTab(user, "Go 账号");
    const section = await screen.findByRole("region", { name: "OpenCode Go 工作区" });
    expect(await findGoRow(section)).toBeInTheDocument();
    expect(within(section).queryByText(/缺少 Auth Cookie/)).not.toBeInTheDocument();
  });

  it("explains a rejected probe with the upstream detail and a Go-specific hint", async () => {
    const user = userEvent.setup();
    // A 401 from the Go gateway means the credential was refused, not that the model is broken:
    // the dialog must say so instead of leaving an opaque reason code.
    openCodeFetchMock({
      accounts: [
        { id: "acc_go_1", workspace_id: "wrk_test", key_set: true, cookie_set: true, models: ["gpt-5.6-luna"], models_error: "", models_fetched_at: "2026-09-01T00:00:00Z" },
      ],
      modelTestResponse: {
        result: {
          reachable: true,
          status: "unavailable",
          reason_code: "authentication_failed",
          status_code: 401,
          latency_ms: 101,
          detail: "{\"error\":{\"message\":\"Invalid API key\"}}",
          tested_at: "2026-09-01T00:00:00Z",
        },
      },
    });

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);
    await user.click(await screen.findByRole("tab", { name: "模型与价格" }));
    const panel = await screen.findByRole("tabpanel", { name: "模型与价格" });
    await user.click(within(panel).getByRole("button", { name: "测试 gpt-5.6-luna" }));
    const dialog = await screen.findByRole("dialog", { name: "模型可用性测试" });
    await user.click(within(dialog).getByRole("button", { name: "开始测试" }));

    // The localized reason and the sanitized upstream message are both shown.
    await waitFor(() => expect(within(dialog).getByText(/authentication_failed/)).toBeInTheDocument());
    expect(within(dialog).getByText(/Invalid API key/)).toBeInTheDocument();
    expect(within(dialog).getByText(/Go 网关拒绝了这把 API Key/)).toBeInTheDocument();
  });

  it("shows the sanitized upstream response, exactly like the accounts model test", async () => {
    const user = userEvent.setup();
    // The accounts test shows the response headers and the redacted JSON body; the model pages
    // must render the same block for the probe they run.
    openCodeFetchMock({
      accounts: [
        { id: "acc_go_1", workspace_id: "wrk_test", key_set: true, cookie_set: true, models: ["qwen3.7-max"], models_error: "", models_fetched_at: "2026-09-01T00:00:00Z" },
      ],
      modelTestResponse: {
        result: {
          reachable: true,
          status: "available",
          reason_code: "model_response_ok",
          status_code: 200,
          latency_ms: 357,
          model: "qwen3.7-max",
          probe_kind: "model",
          endpoint: "chat",
          response: {
            format: "json",
            truncated: false,
            headers: [{ name: "cf-ray", value: "a39f5031edc19898-LAX" }],
            body: '{\n  "_omitted_fields": 4,\n  "model": "qwen3.7-max",\n  "object": "chat.completion"\n}',
          },
          tested_at: "2026-09-12T21:38:00Z",
        },
      },
    });

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);
    await user.click(await screen.findByRole("tab", { name: "模型与价格" }));
    const panel = await screen.findByRole("tabpanel", { name: "模型与价格" });
    await user.click(within(panel).getByRole("button", { name: "测试 qwen3.7-max" }));
    const dialog = await screen.findByRole("dialog", { name: "模型可用性测试" });
    await user.click(within(dialog).getByRole("button", { name: "开始测试" }));

    // Outcome banner and fields match the accounts dialog.
    await waitFor(() => expect(within(dialog).getByText("模型可用")).toBeInTheDocument());
    expect(within(dialog).getByText("模型测试")).toBeInTheDocument();
    expect(within(dialog).getByText("上游实际响应")).toBeInTheDocument();
    expect(within(dialog).getByText("已脱敏的诊断响应")).toBeInTheDocument();
    // Headers and the redacted body are both shown.
    expect(within(dialog).getByText("cf-ray")).toBeInTheDocument();
    expect(within(dialog).getByText(/a39f5031edc19898-LAX/)).toBeInTheDocument();
    expect(within(dialog).getByText(/_omitted_fields/)).toBeInTheDocument();
  });

  it("blames the model, not the key, when the gateway answers ModelError with 401", async () => {
    const user = userEvent.setup();
    // Real answer from the Go gateway: 401 plus a ModelError body. Reporting that as an
    // authentication failure would send the operator to rotate a key that was accepted.
    openCodeFetchMock({
      accounts: [
        { id: "acc_go_1", workspace_id: "wrk_test", key_set: true, cookie_set: true, models: ["gpt-5.6-luna"], models_error: "", models_fetched_at: "2026-09-01T00:00:00Z" },
      ],
      modelTestResponse: {
        result: {
          reachable: true,
          status: "unavailable",
          reason_code: "model_not_supported",
          status_code: 401,
          latency_ms: 84,
          detail: "{\"type\":\"error\",\"error\":{\"type\":\"ModelError\",\"message\":\"Model gemini-3.1-pro is not supported\"}}",
          tested_at: "2026-09-01T00:00:00Z",
        },
      },
    });

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);
    await user.click(await screen.findByRole("tab", { name: "模型与价格" }));
    const panel = await screen.findByRole("tabpanel", { name: "模型与价格" });
    await user.click(within(panel).getByRole("button", { name: "测试 gpt-5.6-luna" }));
    const dialog = await screen.findByRole("dialog", { name: "模型可用性测试" });
    await user.click(within(dialog).getByRole("button", { name: "开始测试" }));

    await waitFor(() => expect(within(dialog).getByText(/model_not_supported/)).toBeInTheDocument());
    // Both the localized reason and the upstream message mention it.
    expect(within(dialog).getAllByText(/not supported/).length).toBeGreaterThan(0);
    // The model-specific hint replaces the credential hint.
    expect(within(dialog).getByText(/密钥是被接受的/)).toBeInTheDocument();
    expect(within(dialog).queryByText(/Go 网关拒绝了这把 API Key/)).not.toBeInTheDocument();
  });

  it("renders the test dialog into the document body so it centres in the viewport", async () => {
    const user = userEvent.setup();
    openCodeFetchMock({
      accounts: [
        { id: "acc_go_1", workspace_id: "wrk_test", key_set: true, cookie_set: true, models: ["gpt-5.6-luna"], models_error: "", models_fetched_at: "2026-09-01T00:00:00Z" },
      ],
      modelTestResponse: { result: { reachable: true, status: "available", reason_code: "model_response_ok", status_code: 200, latency_ms: 33, tested_at: "2026-09-01T00:00:00Z" } },
    });

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);
    await user.click(await screen.findByRole("tab", { name: "模型与价格" }));
    const panel = await screen.findByRole("tabpanel", { name: "模型与价格" });
    await user.click(within(panel).getByRole("button", { name: "测试 gpt-5.6-luna" }));

    const dialog = await screen.findByRole("dialog", { name: "模型可用性测试" });
    // Portalled to <body>: an ancestor with a transform would otherwise become the containing
    // block of the fixed backdrop and the dialog would centre inside the scrolled workspace.
    expect(dialog.closest(".modal-backdrop")?.parentElement).toBe(document.body);
  });

  it("loads models through the models route and updates the model count with the loaded notice", async () => {
    const user = userEvent.setup();
    const requests = openCodeFetchMock({
      accounts: [goAccountView({ models: [] })],
      modelsResponse: { account: goAccountView({ models: ["gpt-5.1", "claude-sonnet-4", "gemini-2.5-pro"] }) },
    });
    const onNotice = vi.fn();

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={onNotice} />);

    await selectTab(user, "Go 账号");
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

    await selectTab(user, "Go 账号");
    const section = await screen.findByRole("region", { name: "OpenCode Go 工作区" });
    const row = await findGoRow(section);
    await user.click(within(row).getByRole("button", { name: `拉取 ${GO_WORKSPACE} 的模型列表` }));
    await waitFor(() => expect(within(row).getByText("2")).toBeInTheDocument());

    await user.click(within(row).getByRole("button", { name: `测试 ${GO_WORKSPACE} 的模型` }));

    // The tester lives on the Models tab, so opening it switches the active tab.
    expect(screen.getByRole("tab", { name: "模型与价格" })).toHaveAttribute("aria-selected", "true");
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
    expect(within(tester).getByText(/model_not_found/)).toBeInTheDocument();
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

    await selectTab(user, "Go 账号");
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
    expect(screen.getByRole("tablist", { name: "OpenCode" })).toBeInTheDocument();
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

    await selectTab(user, "Go 账号");
    const section = await screen.findByRole("region", { name: "OpenCode Go 工作区" });
    const row = await findGoRow(section);
    await user.click(within(row).getByRole("button", { name: "刷新 OpenCode 额度" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.includes(`/opencode/refresh-account?account_id=${GO_ACCOUNT_ID}`) && init.method === "POST")).toBe(true));
    expect(await within(row).findByText(/5 小时额度: 5\.0% · 5 分钟/)).toBeInTheDocument();
  });

  it("renders the detected channels and imports one through the import route", async () => {
    const user = userEvent.setup();
    const requests = openCodeFetchMock({
      channels: [
        channelView(),
        channelView({ kind: "go", name: "OpenCode Go wrk_9", base_url: CHANNEL_GO_BASE, key_set: false }),
      ],
      importResponse: { kind: "zen", action: "create_zen", account_id: "zen_2", name: "Zen channel", base_url: CHANNEL_ZEN_BASE },
    });
    const onNotice = vi.fn();

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={onNotice} />);

    await selectTab(user, "渠道");
    const panel = await screen.findByRole("tabpanel", { name: "渠道" });
    const zenRow = findChannelRow(panel, "Zen channel");
    expect(zenRow.textContent).toContain(CHANNEL_ZEN_BASE);
    expect(zenRow.textContent).toContain("已保存密钥");
    expect(zenRow.textContent).toContain("未导入");
    expect(within(zenRow).getAllByRole("cell")[3]).toHaveTextContent("3");

    // A channel without a stored key has nothing to import.
    const goRow = findChannelRow(panel, "OpenCode Go wrk_9");
    expect(within(goRow).getByRole("button", { name: "导入" })).toBeDisabled();

    await user.click(within(zenRow).getByRole("button", { name: "导入" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/opencode/import") && init.method === "POST")).toBe(true));
    const request = requests.find(({ url, init }) => url.endsWith("/opencode/import") && init.method === "POST");
    expect(JSON.parse(String(request?.init.body))).toEqual({ base_url: CHANNEL_ZEN_BASE });
    await waitFor(() => expect(onNotice).toHaveBeenCalledWith("已将 Zen 渠道导入 OpenCode 工作区"));
    await waitFor(() => expect(requests.filter(({ url }) => url.endsWith("/opencode/channels")).length).toBeGreaterThan(1));
    await waitFor(() => expect(requests.filter(({ url }) => url.endsWith("/opencode/accounts")).length).toBeGreaterThan(1));
    await waitFor(() => expect(requests.filter(({ url }) => url.endsWith("/opencode/zen/accounts")).length).toBeGreaterThan(1));
  });

  it("explains a 409 import that needs the Go workspace credentials and switches to the Go tab", async () => {
    const user = userEvent.setup();
    openCodeFetchMock({
      channels: [channelView({ kind: "go", name: "OpenCode Go wrk_test", base_url: CHANNEL_GO_BASE })],
      importStatus: 409,
      importErrorBody: {
        error: "add the Workspace ID and auth Cookie for this Go workspace, then import the API key",
        needs_workspace: true,
      },
    });

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    await selectTab(user, "渠道");
    const panel = await screen.findByRole("tabpanel", { name: "渠道" });
    await user.click(within(findChannelRow(panel, "OpenCode Go wrk_test")).getByRole("button", { name: "导入" }));

    expect(await screen.findByText("该 Go 渠道需要先填写 Workspace ID 与 auth Cookie：请先在 Go 账号页添加，再导入 API 密钥。")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("tab", { name: "Go 账号" })).toHaveAttribute("aria-selected", "true"));
    expect(screen.getByRole("tabpanel", { name: "Go 账号" })).toBeInTheDocument();
  });

  it("marks an already imported channel as imported", async () => {
    const user = userEvent.setup();
    openCodeFetchMock({ channels: [channelView({ imported: true })] });

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    await selectTab(user, "渠道");
    const panel = await screen.findByRole("tabpanel", { name: "渠道" });
    const row = findChannelRow(panel, "Zen channel");
    expect(within(row).getByText("已导入")).toBeInTheDocument();
    expect(within(row).queryByText("未导入")).not.toBeInTheDocument();
  });

  it("renders the official OpenCode price catalog and syncs it on demand", async () => {
    const user = userEvent.setup();
    const requests = openCodeFetchMock({
      pricing: billingPricingFixture(),
    });
    const onNotice = vi.fn();

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={onNotice} />);

    await selectTab(user, "模型与价格");
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

  it("disables and enables the selected OpenCode models in bulk", async () => {
    const user = userEvent.setup();
    const requests = openCodeFetchMock({});
    const onNotice = vi.fn();

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={onNotice} />);
    await user.click(await screen.findByRole("tab", { name: "模型与价格" }));
    const panel = await screen.findByRole("tabpanel", { name: "模型与价格" });

    // The control states that it affects every account and channel.
    expect(within(panel).getByText(/所有 OpenCode 账号与渠道/)).toBeInTheDocument();

    // Select two models and disable the selection.
    const gptRow = within(panel).getByText("gpt-5.6-luna").closest("tr") as HTMLElement;
    await user.click(within(gptRow).getByRole("checkbox", { name: "选择 gpt-5.6-luna" }));
    const qwenRow = within(panel).getByText("qwen3.7-max").closest("tr") as HTMLElement;
    await user.click(within(qwenRow).getByRole("checkbox", { name: "选择 qwen3.7-max" }));
    expect(within(panel).getByText("已选择 2 个")).toBeInTheDocument();
    await user.click(within(panel).getByRole("button", { name: "禁用所选" }));

    await waitFor(() => {
      const writes = requests.filter(({ url, init }) => url.endsWith("/opencode/model-control") && init.method === "PUT");
      expect(writes.length).toBeGreaterThan(0);
      expect(JSON.parse(String(writes.at(-1)?.init.body)).disabled.sort()).toEqual(["gpt-5.6-luna", "qwen3.7-max"]);
    });
    await waitFor(() => expect(within(panel).getByText("已选择 0 个")).toBeInTheDocument());
    // The write is confirmed: without a notice a slow request looks like a dead button.
    await waitFor(() => expect(onNotice).toHaveBeenCalledWith("已全局禁用 2 个模型"));

    // Enable one of them again: the other stays disabled.
    await user.click(within(panel).getByRole("checkbox", { name: "选择 gpt-5.6-luna" }));
    await user.click(within(panel).getByRole("button", { name: "启用所选" }));
    await waitFor(() => {
      const writes = requests.filter(({ url, init }) => url.endsWith("/opencode/model-control") && init.method === "PUT");
      expect(JSON.parse(String(writes.at(-1)?.init.body)).disabled).toEqual(["qwen3.7-max"]);
    });

    // Enable-all clears the whole list.
    await user.click(within(panel).getByRole("button", { name: "全部启用" }));
    await waitFor(() => {
      const writes = requests.filter(({ url, init }) => url.endsWith("/opencode/model-control") && init.method === "PUT");
      expect(JSON.parse(String(writes.at(-1)?.init.body)).disabled).toEqual([]);
    });
  });

  it("tests one OpenCode model through a stored account credential", async () => {
    const user = userEvent.setup();
    const requests = openCodeFetchMock({
      accounts: [
        { id: "acc_go_1", workspace_id: "wrk_test", key_set: true, models: ["gpt-5.6-luna"], models_error: "", models_fetched_at: "2026-09-01T00:00:00Z" },
      ],
      modelTestResponse: { result: { reachable: true, status: "available", reason_code: "model_response_ok", status_code: 200, latency_ms: 33, tested_at: "2026-09-01T00:00:00Z" } },
    });

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);
    await user.click(await screen.findByRole("tab", { name: "模型与价格" }));
    const panel = await screen.findByRole("tabpanel", { name: "模型与价格" });

    await user.click(within(panel).getByRole("button", { name: "测试 gpt-5.6-luna" }));
    // The test opens the same dialog the accounts page uses, instead of an inline block.
    const dialog = await screen.findByRole("dialog", { name: "模型可用性测试" });
    // The dialog shows the model in its header and in the outcome list, like the accounts test.
    expect(within(dialog).getAllByText("gpt-5.6-luna").length).toBeGreaterThan(0);
    // The only credential that references the model is selected automatically.
    expect(within(dialog).getByLabelText("测试目标")).toHaveValue("wrk_test");
    await user.click(within(dialog).getByRole("button", { name: "开始测试" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/opencode/model-test") && init.method === "POST")).toBe(true));
    const probe = requests.find(({ url, init }) => url.endsWith("/opencode/model-test") && init.method === "POST");
    expect(JSON.parse(String(probe?.init.body))).toMatchObject({ kind: "go", account_id: "acc_go_1", model: "gpt-5.6-luna" });
    await waitFor(() => expect(within(dialog).getByText(/model_response_ok/)).toBeInTheDocument());
  });

  it("shows the per-conversation session routing status", async () => {
    const user = userEvent.setup();
    openCodeFetchMock({
      session: {
        enabled: true, salt_ready: true, target_models: ["a", "b", "c"], injected_requests: 7, distinct_sessions: 2,
        attributed_by_auth_index: 5, attributed_by_model: 2, skipped_codex_requests: 1, skipped_other_channel: 3, skipped_untargeted_model: 4,
      },
    });

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    await selectTab(user, "总览");
    const section = await screen.findByRole("region", { name: "对话会话" });
    expect(await within(section).findByText("已启用")).toBeInTheDocument();
    expect(within(section).getByText("7")).toBeInTheDocument();
    expect(within(section).getByText("2")).toBeInTheDocument();
    // The attribution breakdown explains how the header was applied.
    expect(within(section).getByText("按渠道 5，按模型 2")).toBeInTheDocument();
    expect(within(section).getByText("Codex 1，其它渠道 3，非目标模型 4")).toBeInTheDocument();
    expect(within(section).getByRole("link", { name: "OpenCode Go 客户端要求" })).toHaveAttribute("href", "https://opencode.ai/docs/go/#where-can-i-use-it");
  });

  it("renders the Go monthly allowance with its derived 5-hour and weekly budgets", async () => {
    const user = userEvent.setup();
    openCodeFetchMock({ pricing: billingPricingFixture() });

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    await selectTab(user, "模型与价格");
    const section = await screen.findByRole("region", { name: "OpenCode 官方价格" });
    expect(await within(section).findByText("LongCat-2.0")).toBeInTheDocument();
    expect(within(section).getByText("每月额度")).toBeInTheDocument();
    expect(within(section).getByText("$60")).toBeInTheDocument();
    expect(within(section).getByText("5 小时 $12 · 每周 $30")).toBeInTheDocument();
    expect(within(section).getByText("$20")).toBeInTheDocument();
    expect(within(section).getByText("5 小时 $4 · 每周 $10")).toBeInTheDocument();
  });

  it("renders the documented request estimates with the three window labels", async () => {
    const user = userEvent.setup();
    openCodeFetchMock({ pricing: billingPricingFixture() });

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    await selectTab(user, "模型与价格");
    const section = await screen.findByRole("region", { name: "OpenCode 官方价格" });
    const estimates = await within(section).findByText("1,830 / 4,580 / 9,150");
    expect(estimates.getAttribute("title")).toContain("预估请求数");
    expect(estimates.getAttribute("title")).toContain("5 小时额度");
    expect(estimates.getAttribute("title")).toContain("7 天额度");
    expect(estimates.getAttribute("title")).toContain("30 天额度");
    expect(estimates).toHaveAttribute("aria-label", expect.stringContaining("30 天额度"));

    // A model without documented estimates keeps the dash fallback instead of NaN.
    const legacyRow = within(section).getByText("Legacy Mini").closest("tr");
    expect(legacyRow).not.toBeNull();
    expect(within(legacyRow as HTMLElement).getAllByText("-").length).toBeGreaterThan(0);
  });

  it("warns about Go models the official docs mark as deprecated", async () => {
    const user = userEvent.setup();
    openCodeFetchMock({ pricing: billingPricingFixture() });

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    await selectTab(user, "模型与价格");
    const section = await screen.findByRole("region", { name: "OpenCode 官方价格" });
    const warning = await within(section).findByText("已弃用 January 2026");
    expect(warning).toHaveClass("opencode-model-error");
  });

  it("hides the Go allowance columns when the Zen catalog is selected", async () => {
    const user = userEvent.setup();
    openCodeFetchMock({ pricing: billingPricingFixture() });

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    await selectTab(user, "模型与价格");
    const section = await screen.findByRole("region", { name: "OpenCode 官方价格" });
    expect(await within(section).findByText("每月额度")).toBeInTheDocument();

    await user.click(within(section).getByRole("button", { name: "OpenCode Zen" }));

    expect(await within(section).findByText("Gemini 3.1 Pro")).toBeInTheDocument();
    expect(within(section).queryByText("每月额度")).not.toBeInTheDocument();
    expect(within(section).queryByText("预估请求数")).not.toBeInTheDocument();
    expect(within(section).queryByText("$60")).not.toBeInTheDocument();
  });

  it("summarizes metered Zen and subscription Go billing with the docs links and sync time", async () => {
    const user = userEvent.setup();
    openCodeFetchMock({ pricing: billingPricingFixture() });

    render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    // The billing summary lives on Overview; the sync metadata stays on Models.
    await selectTab(user, "总览");
    const billing = await screen.findByRole("group", { name: "计费" });
    expect(billing).toHaveTextContent("OpenCode Zen");
    expect(billing).toHaveTextContent("按量计费");
    expect(billing).toHaveTextContent("Pay-as-you-go per 1M tokens");
    expect(billing).toHaveTextContent("$10/月订阅");
    expect(billing).toHaveTextContent("5 小时 20% · 每周 50% · 每月 100%");

    const docsLinks = within(billing).getAllByRole("link", { name: "价格文档" });
    expect(docsLinks.map((link) => link.getAttribute("href"))).toEqual([
      "https://opencode.ai/docs/zen/",
      "https://opencode.ai/docs/go/",
    ]);

    await selectTab(user, "模型与价格");
    const section = await screen.findByRole("region", { name: "OpenCode 官方价格" });
    expect(within(section).getByText(/官方文档同步/)).toBeInTheDocument();
  });
});

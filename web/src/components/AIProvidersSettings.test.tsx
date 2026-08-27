import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { _resetSessionForTest, setSession } from "../store/session";
import { AIProvidersSettings } from "./AIProvidersSettings";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function authIndexedOpenAIEntry(entry: Record<string, unknown>): Record<string, unknown> {
  const keyEntries = (Array.isArray(entry["api-key-entries"])
    ? entry["api-key-entries"]
    : []) as Array<Record<string, unknown>>;
  if (keyEntries.length > 0) {
    return { ...entry, "api-key-entries": keyEntries.map((keyEntry) => ({ ...keyEntry, "auth-index": `openai-${keyEntry["api-key"]}` })) };
  }
  return { ...entry, "auth-index": "openai-entry" };
}

describe("AIProvidersSettings", () => {
  beforeEach(() => {
    _resetSessionForTest();
    localStorage.clear();
    setSession("", "management-secret");
    vi.restoreAllMocks();
  });

  function providerFetchMock(overrides: Record<string, unknown> = {}) {
    const requests: Array<{ url: string; init: RequestInit }> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      requests.push({ url, init });
      const openai = (overrides["openai-compatibility"] as Array<Record<string, unknown>> | undefined) ?? [
        { name: "OpenRouter", "base-url": "https://openrouter.ai/api/v1", "api-key-entries": [{ "api-key": "sk-or-live-1234abcd", "proxy-url": "" }], models: [{ name: "deepseek-chat", alias: "deepseek-chat" }] },
      ];
      const gemini = overrides["gemini-api-key"] ?? [
        { "api-key": "AIza-live-abcdef", "base-url": "https://generativelanguage.googleapis.com/v1beta" },
      ];
      const opencode = overrides["opencode-go"] ?? [
        { id: "wrk_1", workspace_id: "wrk_test" },
      ];
      if (url.endsWith("/opencode/accounts") && init.method === "DELETE") return jsonResponse({ removed: true });
      if (url.endsWith("/opencode/accounts") && init.method === "POST") {
        return jsonResponse({ account: { id: "wrk_new_1", workspace_id: "wrk_new" }, result: { success: true, workspace: "wrk_new" } });
      }
      if (url.endsWith("/opencode/accounts")) return jsonResponse({ accounts: opencode });
      if (url.endsWith("/opencode/refresh")) return jsonResponse({ results: {} });
      if (url.endsWith("/openai-compatibility")) {
        return jsonResponse({ "openai-compatibility": openai.map(authIndexedOpenAIEntry) });
      }
      if (url.endsWith("/gemini-api-key")) return jsonResponse({ "gemini-api-key": gemini });
      if (url.endsWith("/interactions-api-key")) return jsonResponse({ "interactions-api-key": [] });
      if (url.endsWith("/claude-api-key")) return jsonResponse({ "claude-api-key": [] });
      if (url.endsWith("/codex-api-key")) return jsonResponse({ "codex-api-key": [] });
      if (url.endsWith("/xai-api-key")) return jsonResponse({ "xai-api-key": [] });
      if (url.endsWith("/vertex-api-key")) return jsonResponse({ "vertex-api-key": [] });
      if (url.endsWith("/api-keys")) return jsonResponse({ "api-keys": [] });
      if (url.endsWith("/ai-providers/usage-limits")) {
        const requestBody = init.body ? JSON.parse(String(init.body)) as { provider?: string } : {};
        return jsonResponse({
          scope: { kind: "provider", id: requestBody.provider ?? "openrouter" },
          config: { enabled: false, total: { enabled: false, basis: "account", window: "five_hour", percent: 80, amount_usd: 0 }, models: [] },
          credit_used_usd: 0,
          credit_model_used_usd: {},
          updated_at: new Date().toISOString(),
        });
      }
      if (url.includes("/ai-providers/runtime")) {
        const runtime = overrides["ai-providers-runtime"] ?? { snapshots: [], updated_at: new Date().toISOString() };
        return jsonResponse(runtime);
      }
      return jsonResponse({});
    });
    vi.stubGlobal("fetch", fetchMock);
    return requests;
  }

  it("opens provider-scoped usage limits for the selected provider", async () => {
    const user = userEvent.setup();
    const requests = providerFetchMock();

    render(<AIProvidersSettings refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const section = await screen.findByRole("tabpanel", { name: "AI 提供商" });
    const row = Array.from(section.querySelectorAll(".ai-provider-table tbody tr")).find((candidate) => candidate.textContent?.includes("OpenRouter"));
    expect(row).toBeDefined();
    await user.click(within(row as HTMLElement).getByRole("button", { name: "用量限额" }));

    const dialog = await screen.findByRole("dialog", { name: "用量限额 · OpenRouter" });
    expect(within(dialog).getByText("限额仅作用于 AI 供应商 OpenRouter。")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "保存设置" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/ai-providers/usage-limits") && init.method === "PUT")).toBe(true));
    const putRequest = requests.find(({ url, init }) => url.endsWith("/ai-providers/usage-limits") && init.method === "PUT");
    expect(JSON.parse(String(putRequest?.init.body))).toMatchObject({ provider: "OpenRouter" });
  });

  it("lists provider channels with redacted keys and adds an OpenAI-compatible provider", async () => {
    const user = userEvent.setup();
    const requests = providerFetchMock({
      "openai-compatibility": [
        {
          name: "OpenRouter",
          "base-url": "https://openrouter.ai/api/v1",
          "api-key-entries": [
            { "api-key": "sk-or-live-1234abcd", weight: 2, "proxy-url": "https://key-proxy.example" },
            { "api-key": "sk-or-backup-5678", weight: 1 },
          ],
          models: [{ name: "deepseek-chat", alias: "deepseek-chat" }],
          "support-prompt-cache-key": true,
          "disable-cooling": true,
          "request-retry": 3,
          "request-scoped-errors": [{ match: "rate limited" }],
        },
      ],
    });
    const onNotice = vi.fn();

    render(<AIProvidersSettings refreshRevision={0} onAPIError={() => undefined} onNotice={onNotice} />);

    const section = await screen.findByRole("tabpanel", { name: "AI 提供商" });
    expect(within(section).getByText("OpenAI 兼容")).toBeInTheDocument();
    expect(within(section).getByText("Gemini")).toBeInTheDocument();
    expect(within(section).getByText("OpenCode Go")).toBeInTheDocument();

    const openai = within(section).getByText("OpenRouter");
    expect(openai).toBeInTheDocument();
    expect(within(section).getByText("https://openrouter.ai/api/v1")).toBeInTheDocument();
    // API keys must be masked in the DOM, never the full secret.
    expect(within(section).queryByText("sk-or-live-1234abcd")).not.toBeInTheDocument();
    expect(within(section).getByText("sk-o••••abcd")).toBeInTheDocument();
    expect(within(section).queryByText("AIza-live-abcdef")).not.toBeInTheDocument();
    // OpenCode workspace is listed in the table.
    expect(within(section).getAllByText("wrk_test").length).toBeGreaterThanOrEqual(1);

    await user.click(within(section).getByRole("button", { name: "添加 AI 提供商" }));
    const dialog = await screen.findByRole("dialog", { name: "添加 AI 提供商" });
    expect(within(dialog).getByLabelText("选择提供商类型")).toBeInTheDocument();
    await user.type(within(dialog).getByLabelText("提供商名称"), "MyProvider");
    await user.type(within(dialog).getByLabelText("Base URL"), "https://my.example.com/v1");
    await user.type(within(dialog).getByLabelText("API Key"), "sk-new-secret-1234");
    await user.click(within(dialog).getByRole("button", { name: "添加提供商" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/openai-compatibility") && init.method === "PUT")).toBe(true));
    const putRequest = requests.find(({ url, init }) => url.endsWith("/openai-compatibility") && init.method === "PUT");
    const body = JSON.parse(String(putRequest?.init.body)) as Array<Record<string, unknown>>;
    expect(body).toHaveLength(2);
    expect(body[0]).toMatchObject({ name: "OpenRouter", "base-url": "https://openrouter.ai/api/v1" });
    expect(body[0]["api-key-entries"]).toEqual([
      { "api-key": "sk-or-live-1234abcd", weight: 2, "proxy-url": "https://key-proxy.example", "auth-index": "openai-sk-or-live-1234abcd" },
      { "api-key": "sk-or-backup-5678", weight: 1, "auth-index": "openai-sk-or-backup-5678" },
    ]);
    expect(body[0]).toMatchObject({ "support-prompt-cache-key": true, "disable-cooling": true });
    expect(body[0]).toMatchObject({ "request-retry": 3, "request-scoped-errors": [{ match: "rate limited" }] });
    expect(body[1]).toMatchObject({ name: "MyProvider", "base-url": "https://my.example.com/v1" });
    expect(JSON.stringify(body)).toContain("sk-new-secret-1234");
    expect(onNotice).toHaveBeenCalledWith("AI 提供商已添加");
  });

  it("hides provider concurrency controls while CPA supports observation only", async () => {
    providerFetchMock();

    render(<AIProvidersSettings refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const section = await screen.findByRole("tabpanel", { name: "AI 提供商" });
    await waitFor(() => expect(section.querySelector(".ai-provider-table tbody tr")).not.toBeNull());
    const headers = Array.from(section.querySelectorAll(".ai-provider-table thead th")).map((item) => item.textContent);
    expect(headers).not.toContain("并发");
  });

  it("keeps provider settings free of provider-level concurrency while retaining usage metrics", async () => {
    providerFetchMock({
      "ai-providers-runtime": {
        snapshots: [{
          provider: "openai",
          auth_index: "openai-sk-or-live-1234abcd",
          identity: "provider-key",
          supported: true,
          concurrency_configurable: true,
          active: 2,
          limit: 100,
          input_tokens: 1000,
          output_tokens: 200,
          reasoning_tokens: 20,
          cached_tokens: 14,
          total_tokens: 1234,
          amount_usd: 0.0123,
          rated_requests: 1,
          unrated_requests: 0,
          updated_at: new Date().toISOString(),
        }],
        updated_at: new Date().toISOString(),
      },
    });

    render(<AIProvidersSettings refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const section = await screen.findByRole("tabpanel", { name: "AI 提供商" });
    const row = await waitFor(() => {
      const found = Array.from(section.querySelectorAll(".ai-provider-table tbody tr"))
        .find((item) => item.textContent?.includes("OpenRouter"));
      expect(found).toBeDefined();
      return found as HTMLElement;
    });
    const headers = Array.from(section.querySelectorAll(".ai-provider-table thead th")).map((item) => item.textContent);
    expect(headers).not.toContain("并发");
    expect(row.textContent).not.toContain("2/100");
    expect(row.textContent).toContain("1,234");
    expect(row.textContent).toContain("$0.0123");
  });

  it("tests a configured model without requiring an upstream model catalog", async () => {
    const user = userEvent.setup();
    const requests = providerFetchMock();

    render(<AIProvidersSettings refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const section = await screen.findByRole("tabpanel", { name: "AI 提供商" });
    const rows = await waitFor(() => section.querySelectorAll(".ai-provider-table tbody tr"));
    const openaiRow = Array.from(rows).find((row) => row.textContent?.includes("OpenRouter"));
    expect(openaiRow).toBeDefined();
    await user.click(within(openaiRow as HTMLElement).getByRole("button", { name: "测试 OpenRouter" }));

    const dialog = await screen.findByRole("dialog", { name: "测试渠道：OpenRouter" });
    await waitFor(() => expect(within(dialog).getByLabelText("测试模型")).toHaveValue("deepseek-chat"));
    const probeRequests = requests.filter(({ url }) => url.endsWith("/ai-providers/test"));
    expect(probeRequests).toHaveLength(2);
    expect(JSON.parse(String(probeRequests[1].init.body)).model).toBe("deepseek-chat");
  });

  it("saves a replacement OpenAI-compatible API key inside api-key-entries", async () => {
    const user = userEvent.setup();
    const requests = providerFetchMock({
      "openai-compatibility": [
        {
          name: "OpenRouter",
          "base-url": "https://openrouter.ai/api/v1",
          "api-key-entries": [
            { "api-key": "sk-or-live-1234abcd", weight: 2, "proxy-url": "https://first-proxy.example" },
            { "api-key": "sk-or-backup-5678", weight: 1, "proxy-url": "https://backup-proxy.example" },
          ],
        },
      ],
    });

    render(<AIProvidersSettings refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const section = await screen.findByRole("tabpanel", { name: "AI 提供商" });
    const rows = section.querySelectorAll(".ai-provider-table tbody tr");
    const openaiRow = Array.from(rows).find((row) => row.textContent?.includes("OpenRouter"));
    expect(openaiRow).toBeDefined();
    await user.click(within(openaiRow as HTMLElement).getByRole("button", { name: "编辑渠道" }));

    const dialog = await screen.findByRole("dialog", { name: "编辑渠道" });
    const passwordInputs = dialog.querySelectorAll<HTMLInputElement>('input[type="password"]');
    expect(passwordInputs.length).toBeGreaterThanOrEqual(1);
    await user.type(passwordInputs[0], "sk-replacement-5678");
    await user.click(within(dialog).getByRole("button", { name: "保存" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/openai-compatibility") && init.method === "PUT")).toBe(true));
    const putRequest = requests.find(({ url, init }) => url.endsWith("/openai-compatibility") && init.method === "PUT");
    const body = JSON.parse(String(putRequest?.init.body)) as Array<Record<string, unknown>>;
    expect(body[0]).toMatchObject({
      name: "OpenRouter",
      "api-key-entries": [
        { "api-key": "sk-replacement-5678", weight: 2, "proxy-url": "https://first-proxy.example", "auth-index": "openai-sk-or-live-1234abcd" },
        { "api-key": "sk-or-backup-5678", weight: 1, "proxy-url": "https://backup-proxy.example", "auth-index": "openai-sk-or-backup-5678" },
      ],
    });
    expect(body[0]["api-key"]).toBeUndefined();
  });

  it("keeps unrelated weighted OpenAI-compatible keys when editing the first credential row", async () => {
    const user = userEvent.setup();
    const requests = providerFetchMock({
      "openai-compatibility": [
        {
          name: "OpenRouter",
          "base-url": "https://openrouter.ai/api/v1",
          "api-key-entries": [
            { "api-key": "sk-first-1234abcd" },
            { "api-key": "sk-second-5678efgh", weight: 3, "proxy-url": "https://second-proxy.example" },
          ],
        },
      ],
    });

    render(<AIProvidersSettings refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const section = await screen.findByRole("tabpanel", { name: "AI 提供商" });
    const rows = section.querySelectorAll(".ai-provider-table tbody tr");
    const openaiRow = Array.from(rows).find((row) => row.textContent?.includes("OpenRouter"));
    expect(openaiRow).toBeDefined();
    await user.click(within(openaiRow as HTMLElement).getByRole("button", { name: "编辑渠道" }));

    const dialog = await screen.findByRole("dialog", { name: "编辑渠道" });
    const keyInput = dialog.querySelector<HTMLInputElement>(".secret-input input") as HTMLInputElement;
    expect(keyInput.placeholder).toBe("输入新 Key 替换当前凭据");
    await user.type(keyInput, "sk-replacement-9012ijklmn");
    await user.click(within(dialog).getByRole("button", { name: "保存" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/openai-compatibility") && init.method === "PUT")).toBe(true));
    const putRequest = requests.find(({ url, init }) => url.endsWith("/openai-compatibility") && init.method === "PUT");
    const body = JSON.parse(String(putRequest?.init.body)) as Array<Record<string, unknown>>;
    expect(body[0]["api-key-entries"]).toEqual([
      { "api-key": "sk-replacement-9012ijklmn", "auth-index": "openai-sk-first-1234abcd" },
      { "api-key": "sk-second-5678efgh", weight: 3, "proxy-url": "https://second-proxy.example", "auth-index": "openai-sk-second-5678efgh" },
    ]);
  });

  it("keeps the existing first OpenAI-compatible key when only unrelated fields change", async () => {
    const user = userEvent.setup();
    const requests = providerFetchMock({
      "openai-compatibility": [
        {
          name: "OpenRouter",
          "base-url": "https://openrouter.ai/api/v1",
          "api-key-entries": [
            { "api-key": "sk-first-1234abcd" },
            { "api-key": "sk-second-5678efgh", weight: 3, "proxy-url": "https://second-proxy.example" },
          ],
        },
      ],
    });

    render(<AIProvidersSettings refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const section = await screen.findByRole("tabpanel", { name: "AI 提供商" });
    const rows = section.querySelectorAll(".ai-provider-table tbody tr");
    const openaiRow = Array.from(rows).find((row) => row.textContent?.includes("OpenRouter"));
    expect(openaiRow).toBeDefined();
    await user.click(within(openaiRow as HTMLElement).getByRole("button", { name: "编辑渠道" }));

    const dialog = await screen.findByRole("dialog", { name: "编辑渠道" });
    await user.clear(within(dialog).getByLabelText("提供商名称"));
    await user.type(within(dialog).getByLabelText("提供商名称"), "OpenRouter Updated");
    await user.click(within(dialog).getByRole("button", { name: "保存" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/openai-compatibility") && init.method === "PUT")).toBe(true));
    const putRequest = requests.find(({ url, init }) => url.endsWith("/openai-compatibility") && init.method === "PUT");
    const body = JSON.parse(String(putRequest?.init.body)) as Array<Record<string, unknown>>;
    expect(body[0].name).toBe("OpenRouter Updated");
    expect(body[0]["api-key-entries"]).toEqual([
      { "api-key": "sk-first-1234abcd", "auth-index": "openai-sk-first-1234abcd" },
      { "api-key": "sk-second-5678efgh", weight: 3, "proxy-url": "https://second-proxy.example", "auth-index": "openai-sk-second-5678efgh" },
    ]);
  });

  it("preserves a legacy top-level OpenAI-compatible API key when editing other fields", async () => {
    const user = userEvent.setup();
    const requests = providerFetchMock({
      "openai-compatibility": [
        {
          name: "LegacyProvider",
          "base-url": "https://legacy.example.com/v1",
          "api-key": "sk-legacy-secret-1234",
        },
      ],
    });

    render(<AIProvidersSettings refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const section = await screen.findByRole("tabpanel", { name: "AI 提供商" });
    const row = Array.from(section.querySelectorAll(".ai-provider-table tbody tr")).find((candidate) => candidate.textContent?.includes("LegacyProvider"));
    expect(row).toBeDefined();
    await user.click(within(row as HTMLElement).getByRole("button", { name: "编辑渠道" }));
    const dialog = await screen.findByRole("dialog", { name: "编辑渠道" });
    await user.click(within(dialog).getByRole("button", { name: "保存" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/openai-compatibility") && init.method === "PUT")).toBe(true));
    const putRequest = requests.find(({ url, init }) => url.endsWith("/openai-compatibility") && init.method === "PUT");
    const body = JSON.parse(String(putRequest?.init.body)) as Array<Record<string, unknown>>;
    expect(body[0]["api-key"]).toBeUndefined();
    expect(body[0]["api-key-entries"]).toEqual([{ "api-key": "sk-legacy-secret-1234" }]);
  });

  it("loads and updates CPA request retry and scoped error rules", async () => {
    const user = userEvent.setup();
    const requests = providerFetchMock({
      "openai-compatibility": [
        {
          name: "RetryProvider",
          "base-url": "https://retry.example.com/v1",
          "api-key-entries": [{ "api-key": "sk-retry-secret-1234" }],
          "request-retry": 2,
          "request-scoped-errors": [{ match: "old rule" }],
        },
      ],
    });

    render(<AIProvidersSettings refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const section = await screen.findByRole("tabpanel", { name: "AI 提供商" });
    const row = Array.from(section.querySelectorAll(".ai-provider-table tbody tr")).find((candidate) => candidate.textContent?.includes("RetryProvider"));
    expect(row).toBeDefined();
    await user.click(within(row as HTMLElement).getByRole("button", { name: "编辑渠道" }));

    const dialog = await screen.findByRole("dialog", { name: "编辑渠道" });
    expect(within(dialog).getByLabelText("请求重试覆盖（留空使用全局，0 禁用）")).toHaveValue(2);
    expect(within(dialog).getByLabelText("请求范围错误规则（每行一个 JSON 对象）")).toHaveValue('{"match":"old rule"}');

    const retryField = within(dialog).getByLabelText("请求重试覆盖（留空使用全局，0 禁用）");
    await user.clear(retryField);
    await user.type(retryField, "5");
    const errorsField = within(dialog).getByLabelText("请求范围错误规则（每行一个 JSON 对象）");
    await user.clear(errorsField);
    await user.clear(errorsField);
    fireEvent.change(errorsField, { target: { value: '{"match":"new rule"}' } });
    await user.click(within(dialog).getByRole("button", { name: "保存" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/openai-compatibility") && init.method === "PUT")).toBe(true));
    const putRequest = requests.find(({ url, init }) => url.endsWith("/openai-compatibility") && init.method === "PUT");
    const body = JSON.parse(String(putRequest?.init.body)) as Array<Record<string, unknown>>;
    expect(body[0]).toMatchObject({
      "request-retry": 5,
      "request-scoped-errors": [{ match: "new rule" }],
    });
  });

  it("offers every provider type when adding a new provider", async () => {
    const user = userEvent.setup();
    providerFetchMock();

    render(<AIProvidersSettings refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const section = await screen.findByRole("tabpanel", { name: "AI 提供商" });
    await user.click(within(section).getByRole("button", { name: "添加 AI 提供商" }));
    const dialog = await screen.findByRole("dialog", { name: "添加 AI 提供商" });
    const radios = within(dialog).getAllByRole("radio");
    const labels = radios.map((radio) => (radio.closest("label") as HTMLElement).textContent ?? "");
    for (const expected of ["OpenAI 兼容", "Gemini", "Interactions", "Claude", "Codex", "xAI", "Vertex", "API Keys", "OpenCode Go"]) {
      expect(labels.some((label) => label.includes(expected))).toBe(true);
    }
  });

  it("adds a Gemini API key channel through the host management API", async () => {
    const user = userEvent.setup();
    const requests = providerFetchMock();
    const onNotice = vi.fn();

    render(<AIProvidersSettings refreshRevision={0} onAPIError={() => undefined} onNotice={onNotice} />);

    const section = await screen.findByRole("tabpanel", { name: "AI 提供商" });
    await user.click(within(section).getByRole("button", { name: "添加 AI 提供商" }));
    const dialog = await screen.findByRole("dialog", { name: "添加 AI 提供商" });
    await user.click(within(dialog).getByRole("radio", { name: /Gemini/ }));
    await user.type(within(dialog).getByLabelText("API Key"), "AIza-new-secret-1234");
    await user.type(within(dialog).getByLabelText("Base URL"), "https://generativelanguage.googleapis.com/v1beta");
    await user.click(within(dialog).getByRole("button", { name: "添加提供商" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/gemini-api-key") && init.method === "PUT")).toBe(true));
    const putRequest = requests.find(({ url, init }) => url.endsWith("/gemini-api-key") && init.method === "PUT");
    const body = JSON.parse(String(putRequest?.init.body)) as Array<Record<string, unknown>>;
    expect(body).toHaveLength(2);
    expect(body[1]).toMatchObject({ "api-key": "AIza-new-secret-1234", "base-url": "https://generativelanguage.googleapis.com/v1beta" });
    expect(onNotice).toHaveBeenCalledWith("AI 提供商已添加");
  });

  it("adds an OpenCode Go provider through the plugin API", async () => {
    const user = userEvent.setup();
    const requests = providerFetchMock();
    const onNotice = vi.fn();

    render(<AIProvidersSettings refreshRevision={0} onAPIError={() => undefined} onNotice={onNotice} />);

    const section = await screen.findByRole("tabpanel", { name: "AI 提供商" });
    await user.click(within(section).getByRole("button", { name: "添加 AI 提供商" }));
    const dialog = await screen.findByRole("dialog", { name: "添加 AI 提供商" });
    await user.click(within(dialog).getByRole("radio", { name: /OpenCode Go/ }));
    await user.type(within(dialog).getByLabelText("Workspace ID"), "wrk_new");
    await user.type(within(dialog).getByLabelText("Auth Cookie（auth 的值）"), "opencode-cookie-secret");
    await user.click(within(dialog).getByRole("button", { name: "添加提供商" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/opencode/accounts") && init.method === "POST")).toBe(true));
    const request = requests.find(({ url, init }) => url.endsWith("/opencode/accounts") && init.method === "POST");
    expect(JSON.parse(String(request?.init.body))).toEqual({ workspace_id: "wrk_new", auth_cookie: "opencode-cookie-secret" });
    expect(onNotice).toHaveBeenCalledWith("AI 提供商已添加");
  });

  it("deletes an OpenAI-compatible channel entry through the host management API", async () => {
    const user = userEvent.setup();
    const requests = providerFetchMock();
    const onNotice = vi.fn();

    render(<AIProvidersSettings refreshRevision={0} onAPIError={() => undefined} onNotice={onNotice} />);

    const section = await screen.findByRole("tabpanel", { name: "AI 提供商" });
    const rows = section.querySelectorAll(".ai-provider-table tbody tr");
    expect(rows.length).toBeGreaterThanOrEqual(2);

    // Delete the OpenAI-compatible (OpenRouter) row.
    const openaiRow = Array.from(rows).find((row) => row.textContent?.includes("OpenRouter"));
    expect(openaiRow).toBeDefined();
    await user.click(within(openaiRow as HTMLElement).getByRole("button", { name: "删除该渠道" }));
    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/openai-compatibility?index=0") && init.method === "DELETE")).toBe(true));
    await waitFor(() => expect(requests.filter(({ url, init }) => url.endsWith("/openai-compatibility") && !init.method)).toHaveLength(2));
    expect(onNotice).toHaveBeenCalledWith("渠道已删除");
  });

  it("views a provider channel in a detail dialog with masked secrets", async () => {
    const user = userEvent.setup();
    providerFetchMock();

    render(<AIProvidersSettings refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const section = await screen.findByRole("tabpanel", { name: "AI 提供商" });
    const rows = section.querySelectorAll(".ai-provider-table tbody tr");
    const openaiRow = Array.from(rows).find((row) => row.textContent?.includes("OpenRouter"));
    expect(openaiRow).toBeDefined();
    await user.click(within(openaiRow as HTMLElement).getByRole("button", { name: "查看 OpenRouter" }));

    const dialog = await screen.findByRole("dialog", { name: "查看 OpenRouter" });
    expect(within(dialog).getByText("OpenAI 兼容")).toBeInTheDocument();
    expect(within(dialog).getByText("https://openrouter.ai/api/v1")).toBeInTheDocument();
    expect(within(dialog).queryByText("sk-or-live-1234abcd")).not.toBeInTheDocument();
    expect(within(dialog).getByText("sk-o••••abcd")).toBeInTheDocument();
  });

  it("tests a provider channel endpoint through the probe API", async () => {
    const user = userEvent.setup();
    const requests: Array<{ url: string; init: RequestInit }> = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      requests.push({ url, init });
      if (url.endsWith("/ai-providers/test")) {
        const body = JSON.parse(String(init.body ?? "{}")) as Record<string, unknown>;
        if (body.model) {
          return jsonResponse({ reachable: true, status: "available", status_code: 200, model: body.model, probe_kind: "model", reason_code: "model_response_ok", detail: "model response ok", models: [{ id: "gpt-5.5" }, { id: "gpt-5.4-mini" }] });
        }
        return jsonResponse({ reachable: true, status_code: 200, detail: "reachable", models: [{ id: "gpt-5.5" }, { id: "gpt-5.4-mini" }] });
      }
      if (url.endsWith("/openai-compatibility")) {
        return jsonResponse({ "openai-compatibility": [{ name: "OpenRouter", "base-url": "https://openrouter.ai/api/v1", "api-key-entries": [{ "api-key": "sk-or-live-1234abcd" }] }] });
      }
      if (url.endsWith("/opencode/accounts")) return jsonResponse({ accounts: [] });
      return jsonResponse({ "gemini-api-key": [], "interactions-api-key": [], "claude-api-key": [], "codex-api-key": [], "xai-api-key": [], "vertex-api-key": [], "api-keys": [] });
    }));

    render(<AIProvidersSettings refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const section = await screen.findByRole("tabpanel", { name: "AI 提供商" });
    const rows = section.querySelectorAll(".ai-provider-table tbody tr");
    const openaiRow = Array.from(rows).find((row) => row.textContent?.includes("OpenRouter"));
    expect(openaiRow).toBeDefined();
    await user.click(within(openaiRow as HTMLElement).getByRole("button", { name: "测试 OpenRouter" }));

    const dialog = await screen.findByRole("dialog", { name: "测试渠道：OpenRouter" });
    expect(await within(dialog).findByText("模型可用")).toBeInTheDocument();
    expect(within(dialog).getByRole("combobox", { name: "测试模型" })).toHaveValue("gpt-5.5");
    await user.selectOptions(within(dialog).getByRole("combobox", { name: "测试模型" }), "gpt-5.4-mini");
    await user.click(within(dialog).getByRole("button", { name: "重新测试" }));
    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/ai-providers/test") && JSON.parse(String(init.body)).model === "gpt-5.4-mini")).toBe(true));
    const probeRequest = requests.find(({ url }) => url.endsWith("/ai-providers/test"));
    expect(probeRequest).toBeDefined();
    const body = JSON.parse(String(probeRequest?.init.body)) as Record<string, unknown>;
    expect(body).toMatchObject({ base_url: "https://openrouter.ai/api/v1" });
  });

  it("disables an OpenAI-compatible channel through the host PATCH API", async () => {
    const user = userEvent.setup();
    const requests = providerFetchMock();
    const onNotice = vi.fn();

    render(<AIProvidersSettings refreshRevision={0} onAPIError={() => undefined} onNotice={onNotice} />);

    const section = await screen.findByRole("tabpanel", { name: "AI 提供商" });
    const rows = section.querySelectorAll(".ai-provider-table tbody tr");
    const openaiRow = Array.from(rows).find((row) => row.textContent?.includes("OpenRouter"));
    expect(openaiRow).toBeDefined();
    await user.click(within(openaiRow as HTMLElement).getByRole("button", { name: "禁用该渠道" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/openai-compatibility") && init.method === "PATCH")).toBe(true));
    const patchRequest = requests.find(({ url, init }) => url.endsWith("/openai-compatibility") && init.method === "PATCH");
    expect(JSON.parse(String(patchRequest?.init.body))).toEqual({ index: 0, value: { disabled: true } });
    expect(onNotice).toHaveBeenCalledWith("渠道已禁用");
  });
});

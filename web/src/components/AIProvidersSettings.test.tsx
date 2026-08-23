import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { _resetSessionForTest, setSession } from "../store/session";
import { AIProvidersSettings } from "./AIProvidersSettings";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
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
      const openai = overrides["openai-compatibility"] ?? [
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
      if (url.endsWith("/openai-compatibility")) return jsonResponse({ "openai-compatibility": openai });
      if (url.endsWith("/gemini-api-key")) return jsonResponse({ "gemini-api-key": gemini });
      if (url.endsWith("/interactions-api-key")) return jsonResponse({ "interactions-api-key": [] });
      if (url.endsWith("/claude-api-key")) return jsonResponse({ "claude-api-key": [] });
      if (url.endsWith("/codex-api-key")) return jsonResponse({ "codex-api-key": [] });
      if (url.endsWith("/xai-api-key")) return jsonResponse({ "xai-api-key": [] });
      if (url.endsWith("/vertex-api-key")) return jsonResponse({ "vertex-api-key": [] });
      if (url.endsWith("/api-keys")) return jsonResponse({ "api-keys": [] });
      return jsonResponse({});
    });
    vi.stubGlobal("fetch", fetchMock);
    return requests;
  }

  it("lists provider channels with redacted keys and adds an OpenAI-compatible provider", async () => {
    const user = userEvent.setup();
    const requests = providerFetchMock();
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
    expect(body[1]).toMatchObject({ name: "MyProvider", "base-url": "https://my.example.com/v1" });
    expect(JSON.stringify(body)).toContain("sk-new-secret-1234");
    expect(onNotice).toHaveBeenCalledWith("AI 提供商已添加");
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
    await user.click(within(openaiRow as HTMLElement).getByRole("button", { name: "删除" }));
    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/openai-compatibility?index=0") && init.method === "DELETE")).toBe(true));
    expect(onNotice).toHaveBeenCalledWith("渠道已删除");
  });
});

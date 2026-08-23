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
      const empty = overrides["api-keys"] ?? [];
      if (url.endsWith("/openai-compatibility")) return jsonResponse({ "openai-compatibility": openai });
      if (url.endsWith("/gemini-api-key")) return jsonResponse({ "gemini-api-key": gemini });
      if (url.endsWith("/interactions-api-key")) return jsonResponse({ "interactions-api-key": [] });
      if (url.endsWith("/claude-api-key")) return jsonResponse({ "claude-api-key": [] });
      if (url.endsWith("/codex-api-key")) return jsonResponse({ "codex-api-key": [] });
      if (url.endsWith("/xai-api-key")) return jsonResponse({ "xai-api-key": [] });
      if (url.endsWith("/vertex-api-key")) return jsonResponse({ "vertex-api-key": [] });
      if (url.endsWith("/api-keys")) return jsonResponse({ "api-keys": empty });
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

    const openai = within(section).getByText("OpenRouter");
    expect(openai).toBeInTheDocument();
    expect(within(section).getByText("https://openrouter.ai/api/v1")).toBeInTheDocument();
    // API keys must be masked in the DOM, never the full secret.
    expect(within(section).queryByText("sk-or-live-1234abcd")).not.toBeInTheDocument();
    expect(within(section).getByText("sk-o••••abcd")).toBeInTheDocument();
    expect(within(section).queryByText("AIza-live-abcdef")).not.toBeInTheDocument();

    await user.click(within(section).getByRole("button", { name: "添加 AI 提供商" }));
    await user.type(within(section).getByLabelText("提供商名称"), "MyProvider");
    await user.type(within(section).getByLabelText("Base URL"), "https://my.example.com/v1");
    await user.type(within(section).getByLabelText("API Key"), "sk-new-secret-1234");
    await user.click(within(section).getByRole("button", { name: "添加提供商" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/openai-compatibility") && init.method === "PUT")).toBe(true));
    const putRequest = requests.find(({ url, init }) => url.endsWith("/openai-compatibility") && init.method === "PUT");
    const body = JSON.parse(String(putRequest?.init.body)) as Array<Record<string, unknown>>;
    expect(body).toHaveLength(2);
    expect(body[0]).toMatchObject({ name: "OpenRouter", "base-url": "https://openrouter.ai/api/v1" });
    expect(body[1]).toMatchObject({ name: "MyProvider", "base-url": "https://my.example.com/v1" });
    expect(JSON.stringify(body)).toContain("sk-new-secret-1234");
    expect(onNotice).toHaveBeenCalledWith("AI 提供商已添加");
  });

  it("deletes a channel entry through the host management API", async () => {
    const user = userEvent.setup();
    const requests = providerFetchMock();
    const onNotice = vi.fn();

    render(<AIProvidersSettings refreshRevision={0} onAPIError={() => undefined} onNotice={onNotice} />);

    const section = await screen.findByRole("tabpanel", { name: "AI 提供商" });
    const openaiChannel = section.querySelector(".ai-provider-channel");
    expect(openaiChannel).not.toBeNull();
    const deleteButton = within(openaiChannel as HTMLElement).getByRole("button", { name: "删除" });
    await user.click(deleteButton);

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/openai-compatibility?index=0") && init.method === "DELETE")).toBe(true));
    expect(onNotice).toHaveBeenCalledWith("渠道已删除");
  });
});

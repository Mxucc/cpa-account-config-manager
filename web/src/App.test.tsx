import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import App from "./App";
import { _resetSessionForTest } from "./store/session";

const account = {
  id: "auth-1",
  name: "operator.json",
  provider: "codex",
  type: "codex",
  label: "operator@example.com",
  email: "operator@example.com",
  status: "active",
  disabled: false,
  unavailable: false,
  runtime_only: false,
  source: "file",
  priority: 5,
  note: "",
  prefix: "team-a",
  proxy_configured: false,
  websockets: false,
  header_count: 0,
  editable: true,
  success: 12,
  failed: 1,
  updated_at: "2026-07-15T10:00:00Z",
};

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

describe("primary account batch flow", () => {
  beforeEach(() => {
    _resetSessionForTest();
    localStorage.clear();
    vi.restoreAllMocks();
  });

  it("logs in, selects an account, previews an opted-in edit, and opens completed results", async () => {
    const user = userEvent.setup();
    const requests: Array<{ url: string; init: RequestInit }> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      requests.push({ url, init });
      if (url.includes("/batch/preview")) {
        return jsonResponse({
          id: "preview-1",
          created_at: "2026-07-15T10:00:00Z",
          expires_at: "2026-07-15T10:05:00Z",
          scope_mode: "selected",
          total: 1,
          eligible: 1,
          read_only: 0,
          missing: 0,
          physical_files: 1,
          providers: { codex: 1 },
          patch: { fields: ["note"], proxy_mutation: false },
          targets: [{ id: "auth-1", name: "operator.json", provider: "codex", label: "operator@example.com", eligible: true }],
        });
      }
      if (url.includes("/batch/start")) {
        return jsonResponse({
          id: "job-1",
          state: "completed",
          running: false,
          total: 1,
          eligible: 1,
          done: 1,
          succeeded: 1,
          failed: 0,
          conflicts: 0,
          skipped: 0,
          workers: 1,
          patch: { fields: ["note"], proxy_mutation: false },
          retry_available: false,
          persisted: true,
          results: [{ id: "auth-1", name: "operator.json", provider: "codex", label: "operator@example.com", status: "succeeded", applied_fields: ["note"], retryable: false }],
        });
      }
      if (url.includes("/batch/status")) {
        return jsonResponse({
          state: "idle", running: false, total: 0, eligible: 0, done: 0, succeeded: 0,
          failed: 0, conflicts: 0, skipped: 0, workers: 0,
          patch: { fields: [], proxy_mutation: false }, retry_available: false, persisted: false,
        });
      }
      return jsonResponse({ accounts: [account], total: 1, page: 1, page_size: url.includes("page_size=1") ? 1 : 50, pages: 1 });
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<App />);
    expect(await screen.findByRole("form", { name: "管理认证" })).toBeInTheDocument();
    await user.type(screen.getByLabelText("Management Key"), "management-secret");
    await user.click(screen.getByRole("button", { name: "验证并进入" }));

    expect(await screen.findByText("operator@example.com")).toBeInTheDocument();
    const resetFilters = screen.getByRole("button", { name: "重置" });
    const providerFilter = screen.getByLabelText("Provider");
    expect(resetFilters).toBeDisabled();
    await user.type(providerFilter, "custom-provider");
    expect(resetFilters).toBeEnabled();
    await user.clear(providerFilter);
    expect(resetFilters).toBeDisabled();

    await user.click(screen.getByLabelText("选择 operator@example.com"));
    await user.click(screen.getByRole("button", { name: "批量编辑" }));
    await user.click(screen.getByLabelText("Note"));
    await user.type(screen.getByLabelText("Note 值"), "rotated pool");
    await user.click(screen.getByRole("button", { name: "生成预览" }));

    expect(await screen.findByRole("dialog", { name: "变更预览" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "执行 1 个账号" }));
    expect(await screen.findByRole("complementary", { name: "批量任务" })).toBeInTheDocument();
    expect(screen.getAllByText("成功").length).toBeGreaterThan(0);

    const previewRequest = requests.find((request) => request.url.includes("/batch/preview"));
    expect(previewRequest).toBeDefined();
    const body = JSON.parse(String(previewRequest?.init.body));
    expect(body.scope).toEqual({ mode: "selected", ids: ["auth-1"] });
    expect(body.patch).toEqual({ note: "rotated pool" });
    await waitFor(() => expect(new Headers(previewRequest?.init.headers).get("Authorization")).toBe("Bearer management-secret"));
    expect(localStorage.getItem("management-secret")).toBeNull();
  });

  it("keeps the newest account result when an older filter request finishes later", async () => {
    const user = userEvent.setup();
    let resolveCodex: ((response: Response) => void) | undefined;
    const codexResponse = new Promise<Response>((resolve) => {
      resolveCodex = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/batch/status")) {
        return jsonResponse({
          state: "idle", running: false, total: 0, eligible: 0, done: 0, succeeded: 0,
          failed: 0, conflicts: 0, skipped: 0, workers: 0,
          patch: { fields: [], proxy_mutation: false }, retry_available: false, persisted: false,
        });
      }
      if (url.includes("provider=codex")) return codexResponse;
      if (url.includes("provider=gemini")) {
        return jsonResponse({
          accounts: [{ ...account, id: "gemini-1", name: "gemini.json", provider: "gemini", label: "gemini@example.com", email: "gemini@example.com" }],
          total: 1, page: 1, page_size: 50, pages: 1,
        });
      }
      return jsonResponse({ accounts: [account], total: 1, page: 1, page_size: url.includes("page_size=1") ? 1 : 50, pages: 1 });
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<App />);
    await user.type(await screen.findByLabelText("Management Key"), "management-secret");
    await user.click(screen.getByRole("button", { name: "验证并进入" }));
    expect(await screen.findByText("operator@example.com")).toBeInTheDocument();

    const providerFilter = screen.getByLabelText("Provider");
    fireEvent.change(providerFilter, { target: { value: "codex" } });
    await waitFor(() => expect(fetchMock.mock.calls.some(([input]) => String(input).includes("provider=codex"))).toBe(true));
    fireEvent.change(providerFilter, { target: { value: "gemini" } });
    expect(await screen.findByText("gemini@example.com")).toBeInTheDocument();

    resolveCodex?.(jsonResponse({
      accounts: [{ ...account, id: "codex-old", name: "codex-old.json", label: "old-codex@example.com", email: "old-codex@example.com" }],
      total: 1, page: 1, page_size: 50, pages: 1,
    }));
    await waitFor(() => expect(screen.queryByText("old-codex@example.com")).not.toBeInTheDocument());
    expect(screen.getByText("gemini@example.com")).toBeInTheDocument();
  });

  it("replaces running row results with the final completed snapshot", async () => {
    const user = userEvent.setup();
    let statusCalls = 0;
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/batch/status")) {
        statusCalls += 1;
        if (statusCalls === 1) {
          return jsonResponse({
            id: "job-poll", state: "running", running: true, total: 1, eligible: 1, done: 0,
            succeeded: 0, failed: 0, conflicts: 0, skipped: 0, workers: 1,
            patch: { fields: ["note"], proxy_mutation: false }, retry_available: false, persisted: true,
            results: [{ id: "auth-1", name: "operator.json", provider: "codex", label: "operator@example.com", status: "running", retryable: false }],
          });
        }
        if (url.includes("light=1")) {
          return jsonResponse({
            id: "job-poll", state: "completed", running: false, total: 1, eligible: 1, done: 1,
            succeeded: 1, failed: 0, conflicts: 0, skipped: 0, workers: 1,
            patch: { fields: ["note"], proxy_mutation: false }, retry_available: false, persisted: true,
          });
        }
        return jsonResponse({
          id: "job-poll", state: "completed", running: false, total: 1, eligible: 1, done: 1,
          succeeded: 1, failed: 0, conflicts: 0, skipped: 0, workers: 1,
          patch: { fields: ["note"], proxy_mutation: false }, retry_available: false, persisted: true,
          results: [{ id: "auth-1", name: "operator.json", provider: "codex", label: "operator@example.com", status: "succeeded", applied_fields: ["note"], retryable: false }],
        });
      }
      return jsonResponse({ accounts: [account], total: 1, page: 1, page_size: url.includes("page_size=1") ? 1 : 50, pages: 1 });
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<App />);
    await user.type(await screen.findByLabelText("Management Key"), "management-secret");
    await user.click(screen.getByRole("button", { name: "验证并进入" }));
    const jobButton = await screen.findByRole("button", { name: "0/1" });
    await user.click(jobButton);

    expect(await screen.findByText("成功", { selector: ".result-status" }, { timeout: 2000 })).toBeInTheDocument();
    expect(screen.queryByText("执行中", { selector: ".result-status" })).not.toBeInTheDocument();
    expect(statusCalls).toBeGreaterThanOrEqual(3);

    await user.click(screen.getByRole("button", { name: "关闭任务面板" }));
    await user.click(screen.getByRole("button", { name: "打开批量任务" }));
    expect(screen.getByRole("complementary", { name: "批量任务" })).toBeInTheDocument();
  });
});

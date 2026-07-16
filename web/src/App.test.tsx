import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import App from "./App";
import { ACCOUNT_PAGE_SIZE_STORAGE_KEY, writeAccountPageSize } from "./store/accountPageSize";
import { _resetSessionForTest } from "./store/session";

const account = {
  id: "auth-1",
  name: "operator.json",
  provider: "codex",
  type: "codex",
  label: "operator@example.com",
  email: "operator@example.com",
  account_type: "oauth",
  plan_type: "k12",
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
    expect(screen.getByRole("heading", { name: "账号管理" })).toBeInTheDocument();
    const githubLink = screen.getByRole("link", { name: "打开项目 GitHub" });
    expect(githubLink).toHaveAttribute("href", "https://github.com/Mxucc/cpa-account-config-manager/");
    expect(githubLink).toHaveAttribute("rel", "noopener noreferrer");
    expect(screen.queryByRole("button", { name: "退出管理认证" })).not.toBeInTheDocument();
    expect(screen.getByRole("region", { name: "账号筛选" })).toBeInTheDocument();
    expect(screen.getByText("账号列表")).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "Type" })).toBeInTheDocument();
    expect(screen.getByText("k12", { selector: ".account-plan-type" })).toBeInTheDocument();
    expect(screen.getByLabelText("每页账号数")).toHaveValue("50");
    const resetFilters = screen.getByRole("button", { name: "重置" });
    const providerFilter = screen.getByLabelText("Provider");
    expect(resetFilters).toBeDisabled();
    await user.type(providerFilter, "custom-provider");
    expect(resetFilters).toBeEnabled();
    await user.clear(providerFilter);
    expect(resetFilters).toBeDisabled();

    const typeFilter = screen.getByLabelText("Type");
    await user.type(typeFilter, "k12");
    await waitFor(() => expect(fetchMock.mock.calls.some(([input]) => String(input).includes("type=k12"))).toBe(true));
    expect(resetFilters).toBeEnabled();
    await user.clear(typeFilter);
    expect(resetFilters).toBeDisabled();

    await user.click(screen.getByLabelText("选择 operator@example.com"));
    await user.click(screen.getByRole("button", { name: "导出选中账号" }));
    expect(await screen.findByRole("dialog", { name: "下载账号凭据" })).toBeInTheDocument();
    expect(screen.getByText("已选账号")).toBeInTheDocument();
    await user.click(screen.getByLabelText("关闭"));
    await user.click(screen.getByRole("button", { name: "批量编辑" }));
    await user.click(screen.getByLabelText("Note"));
    await user.type(screen.getByLabelText("Note 值"), "rotated pool");
    await user.click(screen.getByRole("button", { name: "生成预览" }));

    expect(await screen.findByRole("dialog", { name: "变更预览" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "执行 1 个账号" }));
    expect(await screen.findByRole("complementary", { name: "批量任务" })).toBeInTheDocument();
    expect(screen.getAllByText("成功").length).toBeGreaterThan(0);

    await user.click(screen.getByRole("button", { name: "导出结果" }));
    expect(await screen.findByRole("dialog", { name: "导出结果" })).toBeInTheDocument();
    expect(screen.getByRole("radiogroup", { name: "导出格式" })).toBeInTheDocument();
    await user.click(screen.getByLabelText("关闭"));
    await user.click(screen.getByLabelText("下载筛选账号凭据"));
    expect(await screen.findByRole("dialog", { name: "下载账号凭据" })).toBeInTheDocument();
    await user.click(screen.getByLabelText("关闭"));

    const previewRequest = requests.find((request) => request.url.includes("/batch/preview"));
    expect(previewRequest).toBeDefined();
    const body = JSON.parse(String(previewRequest?.init.body));
    expect(body.scope).toEqual({ mode: "selected", ids: ["auth-1"] });
    expect(body.patch).toEqual({ note: "rotated pool" });
    await waitFor(() => expect(new Headers(previewRequest?.init.headers).get("Authorization")).toBe("Bearer management-secret"));
    expect(localStorage.getItem("management-secret")).toBeNull();
  });

  it("restores and persists the selected account page size", async () => {
    const user = userEvent.setup();
    const requests: string[] = [];
    writeAccountPageSize(100);
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      requests.push(url);
      if (url.includes("/batch/status")) {
        return jsonResponse({
          state: "idle", running: false, total: 0, eligible: 0, done: 0, succeeded: 0,
          failed: 0, conflicts: 0, skipped: 0, workers: 0,
          patch: { fields: [], proxy_mutation: false }, retry_available: false, persisted: false,
        });
      }
      const parsed = new URL(url, "http://localhost");
      const page = Number(parsed.searchParams.get("page") || 1);
      const pageSize = Number(parsed.searchParams.get("page_size") || 50);
      return jsonResponse({ accounts: [account], total: 400, page, page_size: pageSize, pages: Math.ceil(400 / pageSize) });
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<App />);
    await user.type(await screen.findByLabelText("Management Key"), "management-secret");
    await user.click(screen.getByRole("button", { name: "验证并进入" }));
    expect(await screen.findByText("operator@example.com")).toBeInTheDocument();

    const pageSizeSelect = screen.getByLabelText("每页账号数");
    expect(pageSizeSelect).toHaveValue("100");
    await waitFor(() => expect(requests.some((url) => url.includes("page=1") && url.includes("page_size=100"))).toBe(true));

    await user.click(screen.getByRole("button", { name: "下一页" }));
    await waitFor(() => expect(requests.some((url) => url.includes("page=2") && url.includes("page_size=100"))).toBe(true));
    await user.selectOptions(pageSizeSelect, "200");

    await waitFor(() => expect(requests.some((url) => url.includes("page=1") && url.includes("page_size=200"))).toBe(true));
    expect(pageSizeSelect).toHaveValue("200");
    expect(localStorage.getItem(ACCOUNT_PAGE_SIZE_STORAGE_KEY)).toBe("200");
  });

  it("views, edits, and deletes one account from its row without changing bulk selection", async () => {
    const user = userEvent.setup();
    const requests: Array<{ url: string; init: RequestInit }> = [];
    let deleted = false;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      requests.push({ url, init });
      if (url.includes("/batch/status")) {
        return jsonResponse({
          state: "idle", running: false, total: 0, eligible: 0, done: 0, succeeded: 0,
          failed: 0, conflicts: 0, skipped: 0, workers: 0,
          patch: { fields: [], proxy_mutation: false }, retry_available: false, persisted: false,
        });
      }
      if (url.includes("/batch/preview")) {
        return jsonResponse({
          id: "row-preview-1",
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
      if (url.includes("/accounts/delete/preview")) {
        return jsonResponse({
          id: "delete-preview-1",
          created_at: "2026-07-15T10:00:00Z",
          expires_at: "2026-07-15T10:05:00Z",
          account: { id: "auth-1", name: "operator.json", provider: "codex", label: "operator@example.com", email: "operator@example.com", source: "file" },
        });
      }
      if (url.includes("/accounts/delete/start")) {
        deleted = true;
        return jsonResponse({
          status: "deleted",
          deleted_at: "2026-07-15T10:01:00Z",
          account: { id: "auth-1", name: "operator.json", provider: "codex", label: "operator@example.com", email: "operator@example.com" },
        });
      }
      return jsonResponse({
        accounts: deleted ? [] : [{ ...account, header_names: ["X-Team"], header_count: 1 }],
        total: deleted ? 0 : 1,
        page: 1,
        page_size: url.includes("page_size=1") ? 1 : 50,
        pages: deleted ? 0 : 1,
      });
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<App />);
    await user.type(await screen.findByLabelText("Management Key"), "management-secret");
    await user.click(screen.getByRole("button", { name: "验证并进入" }));
    expect(await screen.findByText("operator@example.com")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "查看 operator@example.com" }));
    const details = await screen.findByRole("dialog", { name: "账号详情" });
    expect(within(details).getAllByText("operator.json").length).toBeGreaterThan(0);
    expect(within(details).getByText("k12")).toBeInTheDocument();
    expect(within(details).getByText("X-Team")).toBeInTheDocument();
    await user.click(within(details).getByLabelText("关闭"));

    expect(screen.getByLabelText("选择 operator@example.com")).not.toBeChecked();
    await user.click(screen.getByRole("button", { name: "编辑 operator@example.com" }));
    const editor = await screen.findByRole("dialog", { name: "编辑账号" });
    await user.click(within(editor).getByLabelText("Note"));
    await user.type(within(editor).getByLabelText("Note 值"), "single edit");
    await user.click(within(editor).getByRole("button", { name: "生成预览" }));
    const rowPreview = await screen.findByRole("dialog", { name: "变更预览" });
    const rowPreviewRequest = requests.find(({ url }) => url.includes("/batch/preview"));
    expect(JSON.parse(String(rowPreviewRequest?.init.body))).toEqual({
      scope: { mode: "selected", ids: ["auth-1"] },
      patch: { note: "single edit" },
    });
    expect(screen.getByLabelText("选择 operator@example.com")).not.toBeChecked();
    await user.click(within(rowPreview).getByLabelText("关闭"));

    await user.click(screen.getByRole("button", { name: "删除 operator@example.com" }));
    const deleteDialog = await screen.findByRole("dialog", { name: "删除账号" });
    const confirmation = await within(deleteDialog).findByLabelText("确认删除文件名");
    const deleteButton = within(deleteDialog).getByRole("button", { name: "删除账号" });
    expect(deleteButton).toBeDisabled();
    await user.type(confirmation, "operator.json");
    expect(deleteButton).toBeEnabled();
    await user.click(deleteButton);

    expect(await screen.findByText("没有匹配账号")).toBeInTheDocument();
    expect(screen.getByText("已删除账号 operator@example.com")).toBeInTheDocument();
    const deletePreviewRequest = requests.find(({ url }) => url.includes("/accounts/delete/preview"));
    const deleteStartRequest = requests.find(({ url }) => url.includes("/accounts/delete/start"));
    expect(JSON.parse(String(deletePreviewRequest?.init.body))).toEqual({ id: "auth-1" });
    expect(JSON.parse(String(deleteStartRequest?.init.body))).toEqual({ preview_id: "delete-preview-1" });
    expect(new Headers(deleteStartRequest?.init.headers).get("Authorization")).toBe("Bearer management-secret");
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

  it("keeps the preview open and shows an actionable inline error when start fails", async () => {
    const user = userEvent.setup();
    let startCalls = 0;
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/batch/preview")) {
        return jsonResponse({
          id: "preview-storage",
          created_at: "2026-07-15T10:00:00Z",
          expires_at: "2026-07-15T10:05:00Z",
          scope_mode: "filtered",
          total: 1,
          eligible: 1,
          read_only: 0,
          missing: 0,
          physical_files: 1,
          providers: { codex: 1 },
          patch: { fields: ["disabled"], proxy_mutation: false },
          targets: [{ id: "auth-1", name: "operator.json", provider: "codex", label: "operator@example.com", eligible: true }],
        });
      }
      if (url.includes("/batch/start")) {
        startCalls += 1;
        if (startCalls === 1) {
          return jsonResponse({ error: "job result storage is unavailable; configure data_dir to a writable directory" }, 503);
        }
        return jsonResponse({
          id: "job-storage-retry",
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
          patch: { fields: ["disabled"], proxy_mutation: false },
          retry_available: false,
          persisted: true,
          results: [{ id: "auth-1", name: "operator.json", provider: "codex", label: "operator@example.com", status: "succeeded", applied_fields: ["disabled"], retryable: false }],
        }, 202);
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
    await user.type(await screen.findByLabelText("Management Key"), "management-secret");
    await user.click(screen.getByRole("button", { name: "验证并进入" }));
    await user.click(await screen.findByRole("button", { name: "批量启用" }));
    await user.click(await screen.findByRole("button", { name: "执行 1 个账号" }));

    expect(await screen.findByText("任务未启动")).toBeInTheDocument();
    expect(screen.getByText(/data_dir/)).toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "变更预览" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "重新启动 1 个账号" }));

    expect(await screen.findByRole("complementary", { name: "批量任务" })).toBeInTheDocument();
    expect(startCalls).toBe(2);
  });

  it("edits the default policy, confirms force sync, and publishes terminal force results", async () => {
    const user = userEvent.setup();
    const requests: Array<{ url: string; init: RequestInit }> = [];
    let forceStarted = false;
    let forceStatusCalls = 0;
    const policySnapshot = {
      policy: { enabled: true, apply_mode: "missing", scan_interval_seconds: 15, priority: null, websockets: false },
      running: false,
      last_scan: { scanned: 1, eligible: 1, changed: 0, skipped: 1, failed: 0, finished_at: "2026-07-15T10:00:00Z" },
    };
    const forceJob = (running: boolean, includeResults: boolean) => ({
      id: "force-job-1",
      state: running ? "running" : "completed",
      running,
      total: 1,
      eligible: 1,
      done: running ? 0 : 1,
      succeeded: running ? 0 : 1,
      failed: 0,
      conflicts: 0,
      skipped: 0,
      workers: 1,
      policy: { fields: ["priority", "websockets"], priority: 0, websockets: false },
      ...(includeResults ? { results: [{ id: "auth-1", name: "operator.json", provider: "codex", label: "operator@example.com", status: running ? "running" : "succeeded", applied_fields: running ? [] : ["priority", "websockets"], retryable: false }] } : {}),
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      requests.push({ url, init });
      if (url.includes("/defaults/force/preview")) {
        return jsonResponse({
          id: "force-preview-1",
          created_at: "2026-07-15T10:00:00Z",
          expires_at: "2026-07-15T10:05:00Z",
          total: 1,
          eligible: 1,
          read_only: 0,
          physical_files: 1,
          policy: { fields: ["priority", "websockets"], priority: 0, websockets: false },
          targets: [{ id: "auth-1", name: "operator.json", provider: "codex", label: "operator@example.com", eligible: true }],
        });
      }
      if (url.includes("/defaults/force/start")) {
        forceStarted = true;
        return jsonResponse(forceJob(true, true), 202);
      }
      if (url.includes("/defaults/force/status")) {
        if (!forceStarted) return jsonResponse({ state: "idle", running: false, total: 0, eligible: 0, done: 0, succeeded: 0, failed: 0, conflicts: 0, skipped: 0, workers: 0, policy: { fields: [], priority: null, websockets: null } });
        forceStatusCalls += 1;
        return jsonResponse(forceJob(false, !url.includes("light=1")));
      }
      if (url.endsWith("/config") && init.method === "PATCH") {
        return jsonResponse({ status: "ok" });
      }
      if (url.endsWith("/defaults") && init.method === "PUT") {
        const policy = JSON.parse(String(init.body));
        return jsonResponse({ ...policySnapshot, policy });
      }
      if (url.endsWith("/defaults")) return jsonResponse(policySnapshot);
      if (url.includes("/batch/status")) {
        return jsonResponse({ state: "idle", running: false, total: 0, eligible: 0, done: 0, succeeded: 0, failed: 0, conflicts: 0, skipped: 0, workers: 0, patch: { fields: [], proxy_mutation: false }, retry_available: false, persisted: false });
      }
      return jsonResponse({ accounts: [account], total: 1, page: 1, page_size: url.includes("page_size=1") ? 1 : 50, pages: 1 });
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<App />);
    await user.type(await screen.findByLabelText("Management Key"), "management-secret");
    await user.click(screen.getByRole("button", { name: "验证并进入" }));
    await user.click(await screen.findByRole("button", { name: "默认策略" }));
    expect(await screen.findByRole("dialog", { name: "默认策略" })).toBeInTheDocument();

    await user.click(screen.getByRole("checkbox", { name: "Priority" }));
    const priority = screen.getByLabelText("默认 Priority");
    await user.clear(priority);
    await user.type(priority, "0");
    await user.click(screen.getByRole("button", { name: "保存策略" }));
    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/defaults") && init.method === "PUT")).toBe(true));

    await user.click(screen.getByRole("button", { name: "强制同步" }));
    expect(await screen.findByRole("dialog", { name: "强制同步预览" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "覆盖 1 个文件" }));
    expect(await screen.findByRole("complementary", { name: "默认策略强制同步" })).toBeInTheDocument();
    expect(await screen.findByText("成功", { selector: ".result-status" }, { timeout: 2000 })).toBeInTheDocument();
    expect(screen.getByText("priority, websockets")).toBeInTheDocument();
    expect(forceStatusCalls).toBeGreaterThanOrEqual(2);

    const putRequest = requests.find(({ url, init }) => url.endsWith("/defaults") && init.method === "PUT");
    expect(JSON.parse(String(putRequest?.init.body))).toEqual({ enabled: true, apply_mode: "missing", scan_interval_seconds: 15, priority: 0, websockets: false });
    const configRequest = requests.find(({ url, init }) => url.endsWith("/config") && init.method === "PATCH");
    expect(JSON.parse(String(configRequest?.init.body))).toEqual({ default_policy: { enabled: true, apply_mode: "missing", scan_interval_seconds: 15, priority: 0, websockets: false } });
    expect(requests.indexOf(configRequest!)).toBeLessThan(requests.indexOf(putRequest!));
    expect(localStorage.length).toBe(0);
  });

  it("previews arbitrary JSON and confirms a redacted account import", async () => {
    const user = userEvent.setup();
    const requests: Array<{ url: string; init: RequestInit }> = [];
    const rawJSON = `{"wrapper":{"accounts":[{"email":"import@example.com","account_id":"import-account","access_token":"browser-access-secret","refresh_token":"browser-refresh-secret"}]}}`;
    let imported = false;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      requests.push({ url, init });
      if (url.includes("/import/preview")) {
        return jsonResponse({
          id: "import-preview-ui",
          created_at: "2026-07-15T10:00:00Z",
          expires_at: "2026-07-15T10:05:00Z",
          input_type: "json",
          source_files: 1,
          total: 1,
          skipped: 0,
          warnings: ["existing Auth files will not be overwritten"],
          items: [{
            index: 1,
            source_name: "pasted-import.json",
            source_path: "$.wrapper.accounts[0]",
            target_name: "codex-import_example_com.json",
            email: "import@example.com",
            account_id: "import-account",
            label: "import@example.com",
            synthetic_id_token: false,
          }],
        });
      }
      if (url.includes("/import/start")) {
        imported = true;
        return jsonResponse({
          id: "import-preview-ui",
          state: "completed",
          total: 1,
          imported: 1,
          skipped: 0,
          failed: 0,
          started_at: "2026-07-15T10:00:01Z",
          finished_at: "2026-07-15T10:00:02Z",
          results: [{
            index: 1,
            source_name: "pasted-import.json",
            source_path: "$.wrapper.accounts[0]",
            target_name: "codex-import_example_com.json",
            email: "import@example.com",
            account_id: "import-account",
            label: "import@example.com",
            status: "imported",
          }],
        });
      }
      if (url.includes("/batch/status")) {
        return jsonResponse({ state: "idle", running: false, total: 0, eligible: 0, done: 0, succeeded: 0, failed: 0, conflicts: 0, skipped: 0, workers: 0, patch: { fields: [], proxy_mutation: false }, retry_available: false, persisted: false });
      }
      return jsonResponse({
        accounts: imported ? [{ ...account, id: "imported-1", name: "codex-import_example_com.json", label: "import@example.com", email: "import@example.com" }] : [account],
        total: 1,
        page: 1,
        page_size: url.includes("page_size=1") ? 1 : 50,
        pages: 1,
      });
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<App />);
    await user.type(await screen.findByLabelText("Management Key"), "management-secret");
    await user.click(screen.getByRole("button", { name: "验证并进入" }));
    await user.click(await screen.findByRole("button", { name: "添加账号" }));
    await user.click(screen.getByRole("button", { name: "文本 JSON" }));
    fireEvent.change(screen.getByLabelText("JSON 文本"), { target: { value: rawJSON } });
    await user.click(screen.getByRole("button", { name: "生成预览" }));

    expect(await screen.findByText("codex-import_example_com.json")).toBeInTheDocument();
    expect(screen.queryByDisplayValue(rawJSON)).not.toBeInTheDocument();
    expect(screen.queryByText("browser-access-secret")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "添加 1 个账号" }));

    expect(await screen.findByText("导入完成")).toBeInTheDocument();
    expect(screen.getAllByText("codex-import_example_com.json").length).toBeGreaterThan(0);
    await waitFor(() => expect(requests.some(({ url }) => url.includes("/import/start"))).toBe(true));
    const previewRequest = requests.find(({ url }) => url.includes("/import/preview"));
    expect(previewRequest?.init.body).toBeInstanceOf(FormData);
    const pastedFile = (previewRequest?.init.body as FormData).get("files") as File;
    expect(pastedFile.name).toBe("pasted-import.txt");
    expect(pastedFile.type).toBe("text/plain");
    expect(pastedFile.size).toBe(new Blob([rawJSON]).size);
    expect(new Headers(previewRequest?.init.headers).get("Content-Type")).toBeNull();
    expect(localStorage.length).toBe(0);
  });
});

import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import App from "./App";
import { ACCOUNT_FILTERS_STORAGE_KEY, writeAccountFilters } from "./store/accountFilters";
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

  it("renders automatic disable reason and expected recovery in the account row", async () => {
    const user = userEvent.setup();
    const recoverAfter = new Date(Date.now() + 6 * 60 * 60 * 1000).toISOString();
    const automatedAccount = {
      ...account,
      disabled: true,
      automation: {
        health: "quota_limited",
        reason_code: "quota_exhausted",
        recommendation: "enable",
        last_checked_at: "2026-07-20T10:00:00Z",
        owned_disable: true,
        disable_reason: "quota_exhausted",
        disabled_at: "2026-07-20T09:00:00Z",
        recover_after: recoverAfter,
        auto_action: "disable",
        auto_action_status: "succeeded",
        auto_disable_eligible: true,
        inspection_enabled: true,
        auto_disable_enabled: true,
        auto_enable_enabled: true,
        auto_delete_enabled: false,
        failure_threshold: 3,
        failure_streak: 3,
        recovery_threshold: 2,
        healthy_streak: 0,
      },
    };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/batch/status")) {
        return jsonResponse({ state: "idle", running: false, total: 0, eligible: 0, done: 0, succeeded: 0, failed: 0, conflicts: 0, skipped: 0, workers: 0, patch: { fields: [], proxy_mutation: false }, retry_available: false, persisted: false });
      }
      return jsonResponse({ accounts: [automatedAccount], total: 1, page: 1, page_size: 50, pages: 1 });
    }));

    render(<App />);
    await user.type(await screen.findByLabelText("Management Key"), "management-secret");
    await user.click(screen.getByRole("button", { name: "验证并进入" }));

    expect(await screen.findByText("自动禁用", { selector: ".automation-disposition-badge" })).toBeInTheDocument();
    expect(screen.getByText(/额度已耗尽.*自动启用/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "查看 operator@example.com" }));
    const details = await screen.findByRole("dialog", { name: "账号详情" });
    expect(within(details).getByText("自动处置")).toBeInTheDocument();
    expect(within(details).getAllByText("自动禁用").length).toBeGreaterThan(0);
    expect(within(details).getByText("额度已耗尽")).toBeInTheDocument();
  });

  it("refreshes the account state when returning to the accounts view", async () => {
    const user = userEvent.setup();
    let disabled = true;
    const accountRequests: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/batch/status")) {
        return jsonResponse({
          state: "idle", running: false, total: 0, eligible: 0, done: 0, succeeded: 0,
          failed: 0, conflicts: 0, skipped: 0, workers: 0,
          patch: { fields: [], proxy_mutation: false }, retry_available: false, persisted: false,
        });
      }
      if (url.includes("/operations")) {
        return jsonResponse({
          operations: [], total: 0, page: 1, page_size: 500, pages: 0,
          summary: { total: 0, running: 0, succeeded: 0, failed: 0, attention: 0, interrupted: 0 },
          retained: 0, retention_limit: 500, extended_history: false, archived_segments: 0,
        });
      }
      if (url.includes("/accounts")) {
        accountRequests.push(url);
        return jsonResponse({ accounts: [{ ...account, disabled }], total: 1, page: 1, page_size: 50, pages: 1 });
      }
      return jsonResponse({});
    }));

    render(<App />);
    await user.type(await screen.findByLabelText("Management Key"), "management-secret");
    await user.click(screen.getByRole("button", { name: "验证并进入" }));
    expect(await screen.findByText("账号已禁用")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "操作日志" }));
    disabled = false;
    const requestsBeforeReturn = accountRequests.length;
    await user.click(screen.getByRole("button", { name: "账号" }));

    await waitFor(() => expect(accountRequests.length).toBeGreaterThan(requestsBeforeReturn));
    await waitFor(() => expect(screen.queryByText("账号已禁用")).not.toBeInTheDocument());
  });

  it("keeps synchronizing account state after the inspection workspace unmounts", async () => {
    const user = userEvent.setup();
    let inspectionStarted = false;
    let disabled = false;
    let accountRequests = 0;
    const snapshot = {
      policy: { enabled: true, scan_interval_minutes: 30, failure_threshold: 3, recovery_threshold: 2, auto_disable: true, auto_enable: true, auto_delete: false, delete_grace_hours: 168, delete_batch_size: 10 },
      running: false, pending: false, total: 1, action_count: 0,
      probe_sweep_remaining: 0, probe_sweep_total: 0, probe_sweep_completed: 0, probe_sweep_status: "completed",
      last_run: { scanned: 1, healthy: 1, quota_limited: 0, invalid_credentials: 0, deactivated: 0, review: 0, unavailable: 0, disabled: 0, unknown: 0, auto_disabled: 0, auto_enabled: 0, delete_pending: 0, failed: 0, truncated: 0 },
    };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      if (url.includes("/batch/status")) {
        return jsonResponse({ state: "idle", running: false, total: 0, eligible: 0, done: 0, succeeded: 0, failed: 0, conflicts: 0, skipped: 0, workers: 0, patch: { fields: [], proxy_mutation: false }, retry_available: false, persisted: false });
      }
      if (url.includes("/inspection/results")) return jsonResponse({ results: [], total: 0, page: 1, page_size: 50, pages: 0 });
      if (url.includes("/inspection/actions")) return jsonResponse({ actions: [] });
      if (url.endsWith("/inspection/run")) {
        inspectionStarted = true;
        return jsonResponse({ ...snapshot, pending: true, run_mode: "full", probe_phase: "listing" }, 202);
      }
      if (url.endsWith("/inspection")) {
        if (inspectionStarted) disabled = true;
        return jsonResponse(snapshot);
      }
      if (url.includes("/accounts")) {
        accountRequests += 1;
        return jsonResponse({ accounts: [{ ...account, disabled }], total: 1, page: 1, page_size: 50, pages: 1 });
      }
      return jsonResponse({});
    }));

    render(<App />);
    await user.type(await screen.findByLabelText("Management Key"), "management-secret");
    await user.click(screen.getByRole("button", { name: "验证并进入" }));
    expect(await screen.findByText("operator@example.com")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "巡检与自动化" }));
    await user.click(await screen.findByRole("button", { name: "开始巡检" }));
    await user.click(screen.getByRole("button", { name: "账号" }));
    const requestsBeforeCompletion = accountRequests;

    await waitFor(() => expect(accountRequests).toBeGreaterThan(requestsBeforeCompletion), { timeout: 2500 });
    expect(await screen.findByText("账号已禁用")).toBeInTheDocument();
  });

  it("logs in, selects an account, previews an opted-in edit, and opens completed results", async () => {
    const user = userEvent.setup();
    const requests: Array<{ url: string; init: RequestInit }> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      requests.push({ url, init });
      if (url.includes("/accounts/model-test")) {
        return jsonResponse({
          account_id: "auth-1",
          provider: "codex",
          model: "gpt-5.6-sol",
          status: "available",
          reason_code: "model_response_ok",
          latency_ms: 286,
          tested_at: "2026-07-20T08:00:00Z",
        });
      }
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
    expect(screen.getByRole("button", { name: "其他配置" })).toBeInTheDocument();
    const githubLink = screen.getByRole("link", { name: "打开项目 GitHub" });
    expect(githubLink).toHaveAttribute("href", "https://github.com/Mxucc/cpa-account-config-manager/");
    expect(githubLink).toHaveAttribute("rel", "noopener noreferrer");
    expect(screen.queryByRole("button", { name: "退出管理认证" })).not.toBeInTheDocument();
    expect(screen.getByRole("region", { name: "账号筛选" })).toBeInTheDocument();
    expect(screen.getByText("账号列表")).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "类型" })).toBeInTheDocument();
    expect(screen.getAllByRole("columnheader").map((header) => header.textContent)).toEqual([
      "", "账号", "提供方", "类型", "用量", "更新时间", "权限", "状态", "优先级", "路由配置", "操作",
    ]);
    expect(screen.getByText("k12", { selector: ".account-plan-type" })).toBeInTheDocument();
    expect(screen.getByLabelText("每页账号数")).toHaveValue("50");
    const resetFilters = screen.getByRole("button", { name: "重置" });
    const providerFilter = screen.getByLabelText("提供方");
    expect(resetFilters).toBeDisabled();
    await user.type(providerFilter, "custom-provider");
    expect(resetFilters).toBeEnabled();
    await user.clear(providerFilter);
    expect(resetFilters).toBeDisabled();

    const typeFilter = screen.getByLabelText("类型");
    await user.type(typeFilter, "k12");
    await waitFor(() => expect(fetchMock.mock.calls.some(([input]) => String(input).includes("type=k12"))).toBe(true));
    expect(resetFilters).toBeEnabled();
    await user.clear(typeFilter);
    expect(resetFilters).toBeDisabled();

    await user.click(screen.getByRole("button", { name: "测试模型 operator@example.com" }));
    const modelTestDialog = await screen.findByRole("dialog", { name: "模型可用性测试" });
    expect(within(modelTestDialog).getByLabelText("测试模型")).toHaveValue("gpt-5.6-sol");
    await user.click(within(modelTestDialog).getByRole("button", { name: "开始测试" }));
    expect(await within(modelTestDialog).findByText("模型可用")).toBeInTheDocument();
    expect(within(modelTestDialog).getByText("已收到符合预期的模型响应")).toBeInTheDocument();
    expect(within(modelTestDialog).getByText("286 ms")).toBeInTheDocument();
    const modelTestRequest = requests.find((request) => request.url.includes("/accounts/model-test"));
    expect(JSON.parse(String(modelTestRequest?.init.body))).toEqual({ account_id: "auth-1", model: "gpt-5.6-sol" });
    await user.click(within(modelTestDialog).getAllByRole("button", { name: "关闭" })[1]);

    await user.click(screen.getByLabelText("选择 operator@example.com"));
    await user.click(screen.getByRole("button", { name: "导出选中账号" }));
    expect(await screen.findByRole("dialog", { name: "下载账号凭据" })).toBeInTheDocument();
    expect(screen.getByText("已选账号")).toBeInTheDocument();
    await user.click(screen.getByLabelText("关闭"));
    await user.click(screen.getByRole("button", { name: "批量编辑" }));
    await user.click(screen.getByLabelText("备注"));
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

  it("previews filtered and selected batch deletion, confirms it, and clears the selection", async () => {
    const user = userEvent.setup();
    const requests: Array<{ url: string; init: RequestInit }> = [];
    let deleteJobStarted = false;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      requests.push({ url, init });
      if (url.includes("/batch/delete/preview")) {
        const body = JSON.parse(String(init.body)) as { scope: { mode: string } };
        return jsonResponse({
          operation: "delete",
          id: `delete-preview-${body.scope.mode}`,
          created_at: "2026-07-23T10:00:00Z",
          expires_at: "2026-07-23T10:05:00Z",
          scope_mode: body.scope.mode,
          total: 1,
          eligible: 1,
          read_only: 0,
          missing: 0,
          physical_files: 1,
          providers: { codex: 1 },
          patch: { fields: [], proxy_mutation: false },
          targets: [{ id: "auth-1", name: "operator.json", provider: "codex", label: "operator@example.com", eligible: true }],
        });
      }
      if (url.includes("/batch/delete/start")) {
        deleteJobStarted = true;
        return jsonResponse({
          operation: "delete",
          id: "delete-job-1",
          state: "running",
          running: true,
          total: 1,
          eligible: 1,
          done: 0,
          succeeded: 0,
          failed: 0,
          conflicts: 0,
          skipped: 0,
          workers: 1,
          patch: { fields: [], proxy_mutation: false },
          retry_available: false,
          persisted: false,
          results: [{ id: "auth-1", label: "operator@example.com", provider: "codex", status: "pending", applied_fields: [], retryable: false }],
        }, 202);
      }
      if (url.includes("/batch/status")) {
        return jsonResponse(deleteJobStarted ? {
          operation: "delete", id: "delete-job-1", state: "running", running: true,
          total: 1, eligible: 1, done: 0, succeeded: 0, failed: 0, conflicts: 0, skipped: 0,
          workers: 1, patch: { fields: [], proxy_mutation: false }, retry_available: false, persisted: false,
        } : {
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
    expect(await screen.findByRole("button", { name: "批量删除" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "批量删除" }));
    const filteredPreview = await screen.findByRole("dialog", { name: "批量删除预览" });
    expect(JSON.parse(String(requests.find(({ url }) => url.includes("/batch/delete/preview"))?.init.body)).scope).toEqual({ mode: "filtered", filters: {} });
    await user.click(within(filteredPreview).getByLabelText("关闭"));

    await user.click(screen.getByLabelText("选择 operator@example.com"));
    await user.click(screen.getByRole("button", { name: "批量删除" }));
    const selectedPreview = await screen.findByRole("dialog", { name: "批量删除预览" });
    expect(within(selectedPreview).getByText("删除后无法通过插件恢复这些 Auth 文件，请确认目标范围后再继续。")).toBeInTheDocument();
    expect(within(selectedPreview).queryByRole("textbox")).not.toBeInTheDocument();
    await user.click(within(selectedPreview).getByRole("button", { name: "删除 1 个账号" }));

    expect(await screen.findByRole("complementary", { name: "批量删除任务" })).toBeInTheDocument();
    expect(screen.getByLabelText("选择 operator@example.com")).not.toBeChecked();
    const previews = requests.filter(({ url }) => url.includes("/batch/delete/preview"));
    expect(JSON.parse(String(previews[1].init.body)).scope).toEqual({ mode: "selected", ids: ["auth-1"] });
    const start = requests.find(({ url }) => url.includes("/batch/delete/start"));
    expect(JSON.parse(String(start?.init.body))).toEqual({ preview_id: "delete-preview-selected", confirm: true });
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

  it("restores account search and filters and clears the persisted state on reset", async () => {
    const user = userEvent.setup();
    const requests: string[] = [];
    writeAccountFilters({
      search: "operator@example.com",
      provider: "codex",
      type: "k12",
      status: "active",
      disabled: "false",
      editability: "editable",
    });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      requests.push(url);
      if (url.includes("/batch/status")) {
        return jsonResponse({
          state: "idle", running: false, total: 0, eligible: 0, done: 0, succeeded: 0,
          failed: 0, conflicts: 0, skipped: 0, workers: 0,
          patch: { fields: [], proxy_mutation: false }, retry_available: false, persisted: false,
        });
      }
      return jsonResponse({ accounts: [account], total: 1, page: 1, page_size: url.includes("page_size=1") ? 1 : 50, pages: 1 });
    }));

    render(<App />);
    await user.type(await screen.findByLabelText("Management Key"), "management-secret");
    await user.click(screen.getByRole("button", { name: "验证并进入" }));
    expect(await screen.findByText("operator@example.com")).toBeInTheDocument();

    expect(screen.getByLabelText("搜索账号")).toHaveValue("operator@example.com");
    expect(screen.getByLabelText("提供方")).toHaveValue("codex");
    expect(screen.getByLabelText("类型")).toHaveValue("k12");
    expect(screen.getByLabelText("状态")).toHaveValue("active");
    expect(screen.getByLabelText("启用状态")).toHaveValue("false");
    expect(screen.getByLabelText("可编辑性")).toHaveValue("editable");
    await waitFor(() => expect(requests.some((url) => (
      url.includes("search=operator%40example.com")
      && url.includes("provider=codex")
      && url.includes("type=k12")
      && url.includes("status=active")
      && url.includes("disabled=false")
      && url.includes("editability=editable")
    ))).toBe(true));

    await user.click(screen.getByRole("button", { name: "重置" }));
    expect(screen.getByLabelText("搜索账号")).toHaveValue("");
    expect(screen.getByLabelText("状态")).toHaveValue("");
    await waitFor(() => expect(requests.some((url) => {
      const parsed = new URL(url, "http://localhost");
      return parsed.pathname.endsWith("/accounts")
        && parsed.searchParams.get("page") === "1"
        && parsed.searchParams.get("search") === null
        && parsed.searchParams.get("provider") === null
        && parsed.searchParams.get("type") === null
        && parsed.searchParams.get("status") === null
        && parsed.searchParams.get("disabled") === null
        && parsed.searchParams.get("editability") === null;
    })).toBe(true));
    await waitFor(() => expect(localStorage.getItem(ACCOUNT_FILTERS_STORAGE_KEY)).toBeNull());
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
    await user.click(within(editor).getByLabelText("备注"));
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
    const deleteButton = within(deleteDialog).getByRole("button", { name: "删除账号" });
    await waitFor(() => expect(deleteButton).toBeEnabled());
    expect(within(deleteDialog).queryByRole("textbox")).not.toBeInTheDocument();
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

    const providerFilter = screen.getByLabelText("提供方");
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
		const configRequest = requests.find(({ url, init }) => {
			if (!url.endsWith("/config") || init.method !== "PATCH") return false;
			const body = JSON.parse(String(init.body)) as { default_policy?: { priority?: number | null } };
			return body.default_policy?.priority === 0;
		});
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

describe("Agent Identity Session login mode", () => {
  beforeEach(() => {
    _resetSessionForTest();
    localStorage.clear();
    window.history.replaceState({}, "", "/");
    vi.restoreAllMocks();
  });

  afterEach(() => {
    window.history.replaceState({}, "", "/");
  });

  it("fails closed for an invalid OAuth state without rendering account management", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    window.history.replaceState({}, "", "/?agent_identity_login=%3Cinvalid%3E");

    render(<App />);

    expect(await screen.findByText("该 Agent Identity 登录请求已过期，请返回 CPA 重新发起登录。")).toBeInTheDocument();
    expect(screen.queryByText("筛选账号")).not.toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("clears the Session input and shows only redacted account metadata after conversion", async () => {
    const user = userEvent.setup();
    const requests: Array<{ url: string; init: RequestInit }> = [];
    let finishConversion: ((response: Response) => void) | undefined;
    const conversion = new Promise<Response>((resolve) => { finishConversion = resolve; });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      requests.push({ url, init });
      if (url.endsWith("/experiments/agent-identity/session-login")) return conversion;
      return jsonResponse({ accounts: [], total: 0, page: 1, page_size: 1, pages: 0 });
    }));
    window.history.replaceState({}, "", "/?agent_identity_login=login-state_123");

    render(<App />);
    await user.type(await screen.findByLabelText("Management Key"), "management-secret");
    await user.click(screen.getByRole("button", { name: "验证并进入" }));

    const sessionLink = await screen.findByRole("link", { name: "打开 ChatGPT Session" });
    expect(sessionLink).toHaveAttribute("href", "https://chatgpt.com/api/auth/session");
    expect(screen.queryByText("筛选账号")).not.toBeInTheDocument();
    const input = screen.getByLabelText("ChatGPT Session JSON");
    const sessionJSON = "{\"accessToken\":\"browser-session-secret\"}";
    fireEvent.change(input, { target: { value: sessionJSON } });
    await user.click(screen.getByRole("button", { name: "转换并登录" }));

    expect(input).toHaveValue("");
    expect(await screen.findByRole("button", { name: "正在创建 Agent Identity" })).toBeDisabled();
    finishConversion?.(jsonResponse({
      status: "completed",
      account: { email: "agent@example.com", plan_type: "team", provider: "codex-agent-identity", login_state: "login-state_123" },
    }));

    expect(await screen.findByText("Agent Identity 已就绪")).toBeInTheDocument();
    expect(screen.getByText("agent@example.com")).toBeInTheDocument();
    expect(screen.getByText("team")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "关闭登录窗口" })).toBeInTheDocument();
    expect(document.body.textContent).not.toContain("browser-session-secret");
    expect(document.body.textContent).not.toContain("agent_private_key");
    expect(document.body.textContent).not.toContain("task_id");

    const request = requests.find(({ url }) => url.endsWith("/experiments/agent-identity/session-login"));
    expect(request).toBeDefined();
    expect(JSON.parse(String(request?.init.body))).toEqual({ state: "login-state_123", session_json: sessionJSON });
    expect(new Headers(request?.init.headers).get("Authorization")).toBe("Bearer management-secret");
    expect(localStorage.length).toBe(0);
  });
});

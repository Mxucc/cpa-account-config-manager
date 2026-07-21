import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { _resetSessionForTest, setSession } from "../store/session";
import { InspectionWorkspace } from "./InspectionWorkspace";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

const inspectionSnapshot = {
  policy: { enabled: true, scan_interval_minutes: 30, failure_threshold: 3, recovery_threshold: 2, auto_disable: false, auto_enable: false, auto_delete: false, delete_grace_hours: 168, delete_batch_size: 10 },
  running: false,
  pending: false,
  last_run: { scanned: 1, healthy: 0, quota_limited: 0, invalid_credentials: 1, deactivated: 0, review: 0, unavailable: 0, disabled: 0, unknown: 0, auto_disabled: 0, auto_enabled: 0, delete_pending: 0, failed: 0, truncated: 0, finished_at: "2026-07-20T08:00:00Z" },
  total: 1,
  action_count: 1,
  probe_sweep_remaining: 3,
  probe_sweep_total: 5,
  probe_sweep_completed: 2,
  probe_sweep_source: "manual",
  probe_sweep_status: "running",
  probe_sweep_started_at: "2026-07-20T07:59:00Z",
};

describe("InspectionWorkspace", () => {
  beforeEach(() => {
    _resetSessionForTest();
    localStorage.clear();
    setSession("", "management-secret");
    vi.restoreAllMocks();
  });

  it("shows inspection evidence and installs an available update through the host store", async () => {
    const user = userEvent.setup();
    const onNotice = vi.fn();
    const requests: Array<{ url: string; init: RequestInit }> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      requests.push({ url, init });
      if (url.includes("/inspection/results")) return jsonResponse({ results: [{ id: "auth-1", name: "operator.json", provider: "codex", type: "codex", plan_type: "k12", health: "invalid_credentials", reason_code: "invalid_credentials", confidence: "high", recommendation: "reauth", disabled: false, editable: true, auto_disable_eligible: true, owned_disable: false, failure_streak: 2, healthy_streak: 0, last_checked_at: "2026-07-20T08:00:00Z" }], total: 1, page: 1, page_size: 50, pages: 1 });
      if (url.includes("/inspection/actions")) return jsonResponse({ actions: [{ id: "action-1", account_id: "auth-1", name: "operator.json", provider: "codex", action: "disable", status: "pending", reason_code: "invalid_credentials", created_at: "2026-07-20T08:00:00Z" }] });
      if (url.endsWith("/inspection/run")) return jsonResponse({ ...inspectionSnapshot, pending: true, run_mode: "full", probe_phase: "listing" }, 202);
      if (url.endsWith("/inspection")) return jsonResponse(inspectionSnapshot);
      if (url.endsWith("/updates")) return jsonResponse({ policy: { check_enabled: true, check_interval_hours: 24, auto_update: false }, current_version: "0.2.0", latest_version: "0.3.0", update_available: true, release_url: "https://github.com/Mxucc/cpa-account-config-manager/releases/tag/v0.3.0", checking: false, pending: false, checked_at: "2026-07-20T08:00:00Z" });
      if (url === "/v0/management/plugin-store") return jsonResponse({ plugins_enabled: true, plugins: [{ id: "cpa-account-config-manager", version: "0.3.0", installed: true, installed_version: "0.2.0", update_available: true }] });
      if (url.endsWith("/plugin-store/cpa-account-config-manager/install")) return jsonResponse({ status: "installed", id: "cpa-account-config-manager", version: "0.3.0", restart_required: false });
      return jsonResponse({});
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<InspectionWorkspace onAPIError={() => undefined} onNotice={onNotice} />);
    expect(await screen.findByRole("region", { name: "巡检与自动化" })).toBeInTheDocument();
    expect(await screen.findByText("凭据无效或过期")).toBeInTheDocument();
    expect(screen.getByText("重新授权")).toBeInTheDocument();
    expect(screen.queryByText("等待删除", { exact: false })).not.toBeInTheDocument();
    expect(await screen.findByText("发现版本 0.3.0")).toBeInTheDocument();
    expect(screen.getByText("已完成 2/5 · 剩余 3")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "快速巡检" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "开始巡检" })).toBeEnabled();

    await user.click(screen.getByRole("button", { name: "更新" }));
    await waitFor(() => expect(onNotice).toHaveBeenCalledWith(expect.stringContaining("0.3.0")));
    const installRequest = requests.find(({ url }) => url.endsWith("/plugin-store/cpa-account-config-manager/install"));
    expect(installRequest).toBeDefined();
    expect(JSON.parse(String(installRequest?.init.body))).toEqual({ version: "0.3.0" });
    expect(new Headers(installRequest?.init.headers).get("Authorization")).toBe("Bearer management-secret");

    await user.click(screen.getByRole("button", { name: "开始巡检" }));
    const runRequest = requests.find(({ url }) => url.endsWith("/inspection/run"));
    expect(runRequest).toBeDefined();
    expect(JSON.parse(String(runRequest?.init.body))).toEqual({ mode: "full" });
  });

  it("uses the plugin store version when direct GitHub release discovery fails", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/inspection/results")) return jsonResponse({ results: [], total: 0, page: 1, page_size: 50, pages: 0 });
      if (url.includes("/inspection/actions")) return jsonResponse({ actions: [] });
      if (url.endsWith("/inspection")) return jsonResponse({ ...inspectionSnapshot, total: 0, action_count: 0 });
      if (url.endsWith("/updates")) return jsonResponse({ policy: { check_enabled: true, check_interval_hours: 24, auto_update: false }, current_version: "0.2.3", update_available: false, checking: false, pending: false, checked_at: "2026-07-21T08:00:00Z", error: "release metadata request failed" });
      if (url === "/v0/management/plugin-store") return jsonResponse({ plugins_enabled: true, plugins: [{ id: "cpa-account-config-manager", version: "0.2.4", installed: true, installed_version: "0.2.3", update_available: true }] });
      return jsonResponse({});
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<InspectionWorkspace onAPIError={() => undefined} onNotice={() => undefined} />);

    expect(await screen.findByText("发现版本 0.2.4")).toBeInTheDocument();
    expect(screen.getByText("GitHub 元数据不可用，已使用 CPA 插件商店版本")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "更新" })).toBeEnabled();
  });

  it("renders unknown runtime health values without crashing", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/inspection/results")) return jsonResponse({ results: [{ id: "future-1", name: "future.json", provider: "codex", type: "oauth", health: "provider_unhealthy", reason_code: "native_unavailable", confidence: "medium", recommendation: "review", disabled: false, editable: true, auto_disable_eligible: false, owned_disable: false, failure_streak: 1, healthy_streak: 0, last_checked_at: "2026-07-21T10:00:00Z" }], total: 1, page: 1, page_size: 50, pages: 1 });
      if (url.includes("/inspection/actions")) return jsonResponse({ actions: [] });
      if (url.endsWith("/inspection")) return jsonResponse(inspectionSnapshot);
      if (url.endsWith("/updates")) return jsonResponse({ policy: { check_enabled: false, check_interval_hours: 24, auto_update: false }, current_version: "0.2.6", update_available: false, checking: false, pending: false });
      if (url === "/v0/management/plugin-store") return jsonResponse({ plugins_enabled: true, plugins: [] });
      return jsonResponse({});
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<InspectionWorkspace onAPIError={() => undefined} onNotice={() => undefined} />);

    expect(await screen.findByText("future.json")).toBeInTheDocument();
    expect(screen.getAllByText("证据不足").length).toBeGreaterThan(0);
    expect(screen.getByText("自动禁用 未开启")).toBeInTheDocument();
    expect(screen.getByText("自动启用 未开启")).toBeInTheDocument();
  });

  it("exposes safe operator actions for bare 401 review results and persists page size", async () => {
    const user = userEvent.setup();
    const requests: Array<{ url: string; init: RequestInit }> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      requests.push({ url, init });
      if (url.includes("/inspection/results")) return jsonResponse({ results: [{ id: "review-1", name: "review.json", provider: "codex", type: "codex", plan_type: "k12", health: "review", reason_code: "authentication_review", confidence: "low", recommendation: "review", disabled: false, editable: true, auto_disable_eligible: false, owned_disable: false, failure_streak: 1, healthy_streak: 0, last_checked_at: "2026-07-21T08:00:00Z", status_code: 401, review_status: "pending", signal_source: "passive" }], total: 1, page: 1, page_size: url.includes("page_size=100") ? 100 : 50, pages: 1 });
      if (url.includes("/inspection/actions")) return jsonResponse({ actions: [] });
      if (url.endsWith("/inspection/review")) return jsonResponse({ id: "review-1", health: "review", review_status: "resolved" });
      if (url.endsWith("/inspection")) return jsonResponse({ ...inspectionSnapshot, probe_sweep_remaining: 0, probe_sweep_total: 0, probe_sweep_completed: 0, probe_sweep_status: "completed" });
      if (url.endsWith("/updates")) return jsonResponse({ policy: { check_enabled: false, check_interval_hours: 24, auto_update: false }, current_version: "0.2.4", update_available: false, checking: false, pending: false });
      if (url === "/v0/management/plugin-store") return jsonResponse({ plugins_enabled: true, plugins: [] });
      return jsonResponse({});
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<InspectionWorkspace onAPIError={() => undefined} onNotice={() => undefined} />);
    expect(await screen.findByText("HTTP 401", { exact: false })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "账号处置" }));
    const actionDialog = screen.getByRole("dialog", { name: "账号处置" });
    expect(within(actionDialog).getByText("仅凭 HTTP 状态不足以执行破坏性操作，请重新测试或明确选择人工处置。")).toBeInTheDocument();
    expect(within(actionDialog).getByRole("button", { name: "重新测试模型" })).toBeEnabled();
    expect(within(actionDialog).getByRole("button", { name: "禁用" })).toBeEnabled();
    expect(within(actionDialog).getByRole("button", { name: "删除" })).toBeEnabled();
    await user.click(within(actionDialog).getByRole("button", { name: "标记已解决" }));
    await waitFor(() => expect(requests.some(({ url }) => url.endsWith("/inspection/review"))).toBe(true));

    await user.selectOptions(screen.getByRole("combobox", { name: "每页巡检结果数" }), "100");
    await waitFor(() => expect(requests.some(({ url }) => url.includes("page_size=100"))).toBe(true));
    expect(localStorage.getItem("cpa-account-config-manager:inspection-page-size")).toBe("100");
  });

  it("streams completed account results into visible inline operations while the run is active", async () => {
    const user = userEvent.setup();
    const requests: Array<{ url: string; init: RequestInit }> = [];
    let livePolls = 0;
    const liveResult = {
      id: "live-1", name: "live.json", provider: "codex", type: "codex", plan_type: "plus",
      health: "quota_limited", reason_code: "quota_exhausted", confidence: "high", recommendation: "disable",
      disabled: false, editable: true, auto_disable_eligible: true, owned_disable: false, failure_streak: 1, healthy_streak: 0,
      last_checked_at: "2026-07-21T10:00:01Z", recover_after: "2026-07-21T15:00:00Z", quota_window: "five_hour",
      usage_total_tokens: 12345, codex_usage: { observed_at: "2026-07-21T10:00:01Z", five_hour: { used_percent: 75, reset_at: "2026-07-21T15:00:00Z", window_minutes: 300 } },
      run_id: "inspection-live", run_phase: "primary", run_observed_at: "2026-07-21T10:00:01Z",
    };
    const activeSnapshot = {
      ...inspectionSnapshot,
      running: true,
      pending: false,
      probe_sweep_remaining: 1,
      probe_sweep_total: 2,
      probe_sweep_completed: 1,
      probe_sweep_status: "running",
      run_mode: "full",
      probe_phase: "primary",
      active_run: { id: "inspection-live", mode: "full", source: "manual", status: "running", phase: "primary", started_at: "2026-07-21T10:00:00Z", primary_total: 2, primary_completed: 1, retry_total: 0, retry_completed: 0, summary: { ...inspectionSnapshot.last_run, scanned: 1 } },
      live_results: [],
    };
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      requests.push({ url, init });
      if (url.includes("/inspection/live")) {
        livePolls += 1;
        return jsonResponse({ ...activeSnapshot, revision: livePolls + 1, live_results: livePolls >= 1 ? [liveResult] : [] });
      }
      if (url.includes("/inspection/results")) return jsonResponse({ results: [liveResult], total: 1, page: 1, page_size: 50, pages: 1 });
      if (url.includes("/inspection/actions")) return jsonResponse({ actions: [] });
      if (url.includes("/batch/preview")) return jsonResponse({ id: "disable-preview", created_at: "2026-07-21T10:00:02Z", expires_at: "2026-07-21T10:05:02Z", scope_mode: "selected", total: 1, eligible: 1, read_only: 0, missing: 0, physical_files: 1, providers: { codex: 1 }, patch: { fields: ["disabled"], proxy_mutation: false }, targets: [{ id: "live-1", name: "live.json", provider: "codex", eligible: true }] });
      if (url.endsWith("/inspection")) return jsonResponse(activeSnapshot);
      if (url.endsWith("/updates")) return jsonResponse({ policy: { check_enabled: false, check_interval_hours: 24, auto_update: false }, current_version: "0.2.5", update_available: false, checking: false, pending: false });
      if (url === "/v0/management/plugin-store") return jsonResponse({ plugins_enabled: true, plugins: [] });
      return jsonResponse({});
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<InspectionWorkspace onAPIError={() => undefined} onNotice={() => undefined} />);
    const liveRegion = await screen.findByRole("region", { name: "实时巡检" });
    expect(within(liveRegion).getByText("等待首条巡检结果")).toBeInTheDocument();
    await waitFor(() => expect(within(liveRegion).getByText("live.json")).toBeInTheDocument(), { timeout: 2500 });
    expect(within(liveRegion).getByText("12,345")).toBeInTheDocument();
    expect(within(liveRegion).getByText("75%")).toBeInTheDocument();
    expect(within(liveRegion).getByRole("button", { name: "重新测试模型" })).toBeEnabled();
    const disable = within(liveRegion).getByRole("button", { name: "禁用" });
    expect(disable).toBeEnabled();
    await user.click(disable);
    await waitFor(() => expect(requests.some(({ url }) => url.includes("/batch/preview"))).toBe(true));
    const previewRequest = requests.find(({ url }) => url.includes("/batch/preview"));
    expect(JSON.parse(String(previewRequest?.init.body))).toEqual({ scope: { mode: "selected", ids: ["live-1"] }, patch: { disabled: true } });
  });
});

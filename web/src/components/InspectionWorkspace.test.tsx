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

  it("shows inspection evidence and starts a full active inspection", async () => {
    const user = userEvent.setup();
    const onNotice = vi.fn();
    const onAccountsChanged = vi.fn();
    const requests: Array<{ url: string; init: RequestInit }> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      requests.push({ url, init });
      if (url.includes("/inspection/results")) return jsonResponse({ results: [{ id: "auth-1", name: "operator.json", provider: "codex", type: "codex", plan_type: "k12", health: "invalid_credentials", reason_code: "invalid_credentials", confidence: "high", recommendation: "reauth", disabled: false, editable: true, auto_disable_eligible: true, owned_disable: false, failure_streak: 2, healthy_streak: 0, last_checked_at: "2026-07-20T08:00:00Z" }], total: 1, page: 1, page_size: 50, pages: 1 });
      if (url.includes("/inspection/actions")) return jsonResponse({ actions: [{ id: "action-1", account_id: "auth-1", name: "operator.json", provider: "codex", action: "disable", status: "pending", reason_code: "invalid_credentials", created_at: "2026-07-20T08:00:00Z" }] });
      if (url.endsWith("/inspection/run")) return jsonResponse({ ...inspectionSnapshot, pending: true, run_mode: "full", probe_phase: "listing" }, 202);
      if (url.endsWith("/inspection")) return jsonResponse(inspectionSnapshot);
      return jsonResponse({});
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<InspectionWorkspace onAPIError={() => undefined} onNotice={onNotice} onAccountsChanged={onAccountsChanged} />);
    expect(await screen.findByRole("region", { name: "巡检与自动化" })).toBeInTheDocument();
    expect(await screen.findByText("凭据无效或过期")).toBeInTheDocument();
    expect(screen.getByText("重新授权")).toBeInTheDocument();
    expect(screen.queryByText("等待删除", { exact: false })).not.toBeInTheDocument();
    expect(screen.getByText("已完成 2/5 · 剩余 3")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "快速巡检" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "开始巡检" })).toBeEnabled();

    await user.click(screen.getByRole("button", { name: "开始巡检" }));
    const runRequest = requests.find(({ url }) => url.endsWith("/inspection/run"));
    expect(runRequest).toBeDefined();
    expect(JSON.parse(String(runRequest?.init.body))).toEqual({ mode: "full" });
    expect(onAccountsChanged).toHaveBeenCalledTimes(1);
  });

  it("does not request or render plugin update controls inside inspection", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/inspection/results")) return jsonResponse({ results: [], total: 0, page: 1, page_size: 50, pages: 0 });
      if (url.includes("/inspection/actions")) return jsonResponse({ actions: [] });
      if (url.endsWith("/inspection")) return jsonResponse({ ...inspectionSnapshot, total: 0, action_count: 0 });
      return jsonResponse({});
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<InspectionWorkspace onAPIError={() => undefined} onNotice={() => undefined} />);

    expect(await screen.findByRole("region", { name: "巡检与自动化" })).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "插件更新" })).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes("/updates"))).toBe(false);
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes("/plugin-store"))).toBe(false);
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

  it("shows the remediation queue and separates enabled from disabled filter targets", async () => {
    const user = userEvent.setup();
    const previewBodies: Array<{ scope: { ids: string[] }; patch: { disabled: boolean } }> = [];
    const deleteBodies: Array<{ account_ids: string[]; confirm: boolean }> = [];
    let currentJobID = "";
    let jobCount = 0;
    const inspected = [
      { id: "delete-1", name: "delete.json", provider: "codex", health: "deactivated", reason_code: "workspace_deactivated", confidence: "high", recommendation: "delete", disabled: false, editable: true, auto_disable_eligible: true, owned_disable: false, failure_streak: 3, healthy_streak: 0, last_checked_at: "2026-07-21T10:00:00Z", signal_source: "active_probe", probe_kind: "credential", status_code: 402, manual_delete_eligible: true },
      { id: "disable-1", name: "disable.json", provider: "codex", health: "quota_limited", reason_code: "quota_exhausted", confidence: "high", recommendation: "disable", disabled: false, editable: true, auto_disable_eligible: true, owned_disable: false, failure_streak: 1, healthy_streak: 0, last_checked_at: "2026-07-21T10:00:00Z" },
      { id: "enable-1", name: "enable.json", provider: "codex", health: "healthy", reason_code: "healthy_recent_success", confidence: "high", recommendation: "enable", disabled: true, editable: true, auto_disable_eligible: false, owned_disable: false, failure_streak: 0, healthy_streak: 2, last_checked_at: "2026-07-21T10:00:00Z" },
      { id: "reauth-credential", name: "reauth-credential.json", provider: "codex", health: "invalid_credentials", reason_code: "invalid_credentials", confidence: "high", recommendation: "reauth", disabled: false, editable: true, auto_disable_eligible: true, owned_disable: false, failure_streak: 3, healthy_streak: 0, last_checked_at: "2026-07-21T10:00:00Z", signal_source: "active_probe", probe_kind: "credential", status_code: 401, manual_delete_eligible: true },
      { id: "reauth-model", name: "reauth-model.json", provider: "codex", health: "unavailable", reason_code: "authentication_failed", confidence: "medium", recommendation: "disable", disabled: false, editable: true, auto_disable_eligible: true, owned_disable: false, failure_streak: 3, healthy_streak: 0, last_checked_at: "2026-07-21T10:00:00Z", signal_source: "active_probe", probe_kind: "model", status_code: 401, manual_delete_eligible: false },
    ];
    const summary = { actionable: 5, suggested_delete: 1, suggested_disable: 2, suggested_enable: 1, reauth: 1, deletable_reauth: 1, review: 0, keep: 0, handled: 0, editable_enabled: 4, editable_disabled: 1 };
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      if (url.includes("/inspection/results")) return jsonResponse({ results: inspected, summary, total: 5, page: 1, page_size: url.includes("page_size=200") ? 200 : 50, pages: 1 });
      if (url.includes("/inspection/actions")) return jsonResponse({ actions: [] });
      if (url.endsWith("/batch/preview")) {
        const body = JSON.parse(String(init.body)) as { scope: { ids: string[] }; patch: { disabled: boolean } };
        previewBodies.push(body);
        return jsonResponse({ id: `preview-${previewBodies.length}`, created_at: "2026-07-21T10:00:00Z", expires_at: "2026-07-21T10:05:00Z", scope_mode: "selected", total: body.scope.ids.length, eligible: body.scope.ids.length, read_only: 0, missing: 0, physical_files: body.scope.ids.length, providers: { codex: body.scope.ids.length }, patch: { fields: ["disabled"], proxy_mutation: false }, targets: body.scope.ids.map((id) => ({ id, name: `${id}.json`, provider: "codex", eligible: true })) });
      }
      if (url.endsWith("/inspection/delete")) {
        const body = JSON.parse(String(init.body)) as { account_ids: string[]; confirm: boolean };
        deleteBodies.push(body);
        return jsonResponse({ attempted: body.account_ids.length, succeeded: body.account_ids.length, failed: 0, skipped: 0 });
      }
      if (url.endsWith("/batch/start")) {
        currentJobID = `job-${++jobCount}`;
        return jsonResponse({ id: currentJobID, state: "running", running: true, total: 1, eligible: 1, done: 0, succeeded: 0, failed: 0, conflicts: 0, skipped: 0, workers: 1, patch: { fields: ["disabled"] }, retry_available: false, persisted: true }, 202);
      }
      if (url.includes("/batch/status")) return jsonResponse({ id: currentJobID, state: "completed", running: false, total: 1, eligible: 1, done: 1, succeeded: 1, failed: 0, conflicts: 0, skipped: 0, workers: 1, patch: { fields: ["disabled"] }, retry_available: false, persisted: true });
      if (url.endsWith("/inspection")) return jsonResponse({ ...inspectionSnapshot, probe_sweep_remaining: 0, probe_sweep_total: 5, probe_sweep_completed: 5, probe_sweep_status: "completed", anomaly_eligible: 5, anomaly_count: 4, anomaly_percent: 80 });
      if (url.endsWith("/updates")) return jsonResponse({ policy: { check_enabled: false, check_interval_hours: 24, auto_update: false }, current_version: "0.2.7", update_available: false, checking: false, pending: false });
      if (url === "/v0/management/plugin-store") return jsonResponse({ plugins_enabled: true, plugins: [] });
      return jsonResponse({});
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<InspectionWorkspace onAPIError={() => undefined} onNotice={() => undefined} />);

    expect(await screen.findByRole("region", { name: "巡检处置队列" })).toBeInTheDocument();
    expect(screen.getByText("建议处理 5 项")).toBeInTheDocument();
    expect(screen.getByText("已处置")).toBeInTheDocument();
    const metrics = screen.getByLabelText("巡检统计");
    expect(within(metrics).getByText("需要复核")).toBeInTheDocument();
    expect(within(metrics).getByText("暂不可用")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "执行建议操作" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "删除需重新登录账号（1）" })).toBeEnabled();

    await user.click(screen.getByRole("button", { name: "启用筛选中的已禁用账号（1）" }));
    await waitFor(() => expect(previewBodies).toHaveLength(1));
    expect(previewBodies[0]).toEqual({ scope: { mode: "selected", ids: ["enable-1"] }, patch: { disabled: false } });
    await user.click(within(screen.getByRole("dialog", { name: "变更预览" })).getByRole("button", { name: "取消" }));

    await user.click(screen.getByRole("button", { name: "禁用筛选中的已启用账号（4）" }));
    await waitFor(() => expect(previewBodies).toHaveLength(2));
    expect(previewBodies[1]).toEqual({ scope: { mode: "selected", ids: ["delete-1", "disable-1", "reauth-credential", "reauth-model"] }, patch: { disabled: true } });
    await user.click(within(screen.getByRole("dialog", { name: "变更预览" })).getByRole("button", { name: "取消" }));

    await user.click(screen.getByRole("button", { name: "删除需重新登录账号（1）" }));
    const reauthRemediation = await screen.findByRole("dialog", { name: "确认删除需重新登录账号" });
    await user.click(within(reauthRemediation).getByRole("button", { name: "确认并执行" }));
    await waitFor(() => expect(deleteBodies).toEqual([{ account_ids: ["reauth-credential"], confirm: true }]));

    await user.click(screen.getByRole("button", { name: "执行建议操作" }));
    const remediation = await screen.findByRole("dialog", { name: "确认执行建议操作" });
    expect(within(remediation).getByText("删除后无法恢复")).toBeInTheDocument();
    await user.click(within(remediation).getByRole("button", { name: "确认并执行" }));
    await waitFor(() => expect(deleteBodies).toEqual([
      { account_ids: ["reauth-credential"], confirm: true },
      { account_ids: ["delete-1"], confirm: true },
    ]));
    await waitFor(() => expect(previewBodies).toHaveLength(4));
    expect(previewBodies[2]).toEqual({ scope: { mode: "selected", ids: ["disable-1", "reauth-model"] }, patch: { disabled: true } });
    expect(previewBodies[3]).toEqual({ scope: { mode: "selected", ids: ["enable-1"] }, patch: { disabled: false } });
    expect(jobCount).toBe(2);
  });

  it("renders the corrected 151-account remediation distribution", async () => {
    const summary = {
      actionable: 68,
      suggested_delete: 43,
      suggested_disable: 0,
      suggested_enable: 24,
      reauth: 1,
      deletable_reauth: 1,
      review: 0,
      keep: 83,
      handled: 0,
      editable_enabled: 7,
      editable_disabled: 144,
    };
    const result = {
      id: "healthy-disabled",
      name: "healthy-disabled.json",
      provider: "codex",
      health: "healthy",
      reason_code: "credential_response_ok",
      confidence: "high",
      recommendation: "enable",
      disabled: true,
      editable: true,
      auto_disable_eligible: false,
      owned_disable: false,
      failure_streak: 0,
      healthy_streak: 1,
      last_checked_at: "2026-07-21T18:00:00Z",
      signal_source: "active_probe",
      probe_kind: "credential",
      status_code: 200,
    };
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/inspection/results")) return jsonResponse({ results: [result], summary, total: 151, page: 1, page_size: 50, pages: 4 });
      if (url.includes("/inspection/actions")) return jsonResponse({ actions: [] });
      if (url.endsWith("/inspection")) return jsonResponse({
        ...inspectionSnapshot,
        total: 151,
        probe_sweep_remaining: 0,
        probe_sweep_total: 151,
        probe_sweep_completed: 151,
        probe_sweep_status: "completed",
        last_run: { ...inspectionSnapshot.last_run, scanned: 151, healthy: 31, quota_limited: 48, invalid_credentials: 1, deactivated: 43, unavailable: 28 },
      });
      return jsonResponse({});
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<InspectionWorkspace onAPIError={() => undefined} onNotice={() => undefined} />);

    const queue = await screen.findByRole("region", { name: "巡检处置队列" });
    expect(within(queue).getByText("建议处理 68 项")).toBeInTheDocument();
    expect(within(queue).getByText("43")).toBeInTheDocument();
    expect(within(queue).getByText("24")).toBeInTheDocument();
    expect(within(queue).getByText("1")).toBeInTheDocument();
    expect(within(queue).getByText("83")).toBeInTheDocument();
    expect(within(queue).getAllByText("0").length).toBeGreaterThanOrEqual(3);
    expect(screen.getByText("凭据用量响应正常")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "执行建议操作" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "删除需重新登录账号（1）" })).toBeEnabled();
  });
});

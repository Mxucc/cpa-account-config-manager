import { render, screen, waitFor } from "@testing-library/react";
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
      if (url.endsWith("/inspection/scan")) return jsonResponse({ ...inspectionSnapshot, pending: true }, 202);
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
    expect(screen.getByRole("button", { name: "全量服务器巡检" })).toBeEnabled();

    await user.click(screen.getByRole("button", { name: "更新" }));
    await waitFor(() => expect(onNotice).toHaveBeenCalledWith(expect.stringContaining("0.3.0")));
    const installRequest = requests.find(({ url }) => url.endsWith("/plugin-store/cpa-account-config-manager/install"));
    expect(installRequest).toBeDefined();
    expect(JSON.parse(String(installRequest?.init.body))).toEqual({ version: "0.3.0" });
    expect(new Headers(installRequest?.init.headers).get("Authorization")).toBe("Bearer management-secret");

    await user.click(screen.getByRole("button", { name: "全量服务器巡检" }));
    expect(requests.some(({ url }) => url.endsWith("/inspection/scan"))).toBe(true);
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
});

import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { _resetSessionForTest, setSession } from "../store/session";
import { OtherSettingsWorkspace } from "./OtherSettingsWorkspace";

function jsonResponse(body: unknown, status = 200, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json", ...headers } });
}

describe("OtherSettingsWorkspace", () => {
  beforeEach(() => {
    _resetSessionForTest();
    localStorage.clear();
    setSession("", "management-secret");
    vi.restoreAllMocks();
  });

  it("shows CPA server and plugin versions, installs the plugin, and saves update policy", async () => {
    const user = userEvent.setup();
    const onNotice = vi.fn();
    const requests: Array<{ url: string; init: RequestInit }> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      requests.push({ url, init });
      if (url.endsWith("/v0/management/latest-version")) {
        return jsonResponse({ "latest-version": "v7.2.93" }, 200, {
          "X-CPA-Version": "v7.2.92",
          "X-CPA-Build-Date": "2026-07-20T08:00:00Z",
        });
      }
      if (url.endsWith("/updates") && init.method === "PUT") {
        return jsonResponse({ policy: { check_enabled: true, check_interval_hours: 24, auto_update: true }, current_version: "0.2.91", update_available: false, checking: false, pending: false, checked_at: "2026-07-21T08:00:00Z" });
      }
      if (url.endsWith("/updates")) {
        return jsonResponse({ policy: { check_enabled: false, check_interval_hours: 24, auto_update: false }, current_version: "0.2.91", update_available: false, checking: false, pending: false, checked_at: "2026-07-21T08:00:00Z" });
      }
      if (url === "/v0/management/plugin-store") {
        return jsonResponse({ plugins_enabled: true, plugins: [{ id: "cpa-account-config-manager", version: "0.3.0", installed: true, installed_version: "0.2.91", update_available: true }] });
      }
      if (url.endsWith("/plugin-store/cpa-account-config-manager/install")) {
        return jsonResponse({ status: "installed", id: "cpa-account-config-manager", version: "0.3.0", restart_required: false });
      }
      if (url.endsWith("/operations/record")) return jsonResponse({});
      return jsonResponse({});
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<OtherSettingsWorkspace onAPIError={() => undefined} onNotice={onNotice} />);

    const workspace = await screen.findByRole("region", { name: "其他配置" });
    const server = within(workspace).getByRole("region", { name: "CPA 服务端版本" });
    expect(within(server).getByText("v7.2.92")).toBeInTheDocument();
    expect(within(server).getAllByText("v7.2.93").length).toBeGreaterThan(0);
    expect(within(server).getByText("有新版本 v7.2.93")).toBeInTheDocument();

    const plugin = within(workspace).getByRole("region", { name: "插件更新" });
    expect(within(plugin).getByText("0.2.91")).toBeInTheDocument();
    expect(within(plugin).getAllByText("0.3.0").length).toBeGreaterThan(0);
    await user.click(within(plugin).getByRole("button", { name: "更新" }));
    await waitFor(() => expect(requests.some(({ url }) => url.endsWith("/plugin-store/cpa-account-config-manager/install"))).toBe(true));
    expect(onNotice).toHaveBeenCalledWith(expect.stringContaining("0.3.0"));

    await user.click(within(plugin).getByLabelText("自动更新"));
    await user.click(within(plugin).getByRole("button", { name: "保存设置" }));
    expect(within(workspace).getByRole("alert")).toHaveTextContent("确认风险");
    await user.click(within(plugin).getByLabelText("确认开启自动更新"));
    await user.click(within(plugin).getByRole("button", { name: "保存设置" }));
    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/updates") && init.method === "PUT")).toBe(true));
    const saveRequest = requests.find(({ url, init }) => url.endsWith("/updates") && init.method === "PUT");
    expect(JSON.parse(String(saveRequest?.init.body))).toEqual({
      policy: { check_enabled: true, check_interval_hours: 24, auto_update: true },
      confirm_auto_update: true,
    });
  });
});

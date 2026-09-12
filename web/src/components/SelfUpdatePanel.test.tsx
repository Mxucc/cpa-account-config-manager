import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api/client";
import { _resetSessionForTest, setSession } from "../store/session";
import type { SelfUpdateSnapshot } from "../types";
import { SelfUpdatePanel, reloadPollTiming } from "./SelfUpdatePanel";

function snapshot(overrides: Partial<SelfUpdateSnapshot> = {}): SelfUpdateSnapshot {
  return {
    current_version: "0.3.1415",
    latest_version: "0.3.1416",
    update_available: true,
    source: "github_api",
    checked_at: "2026-09-12T10:00:00Z",
    asset_name: "cpa-account-config-manager_0.3.1416_darwin_arm64.zip",
    asset_url: "https://github.com/Mxucc/cpa-account-config-manager/releases/download/v0.3.1416/cpa-account-config-manager_0.3.1416_darwin_arm64.zip",
    asset_bytes: 4194304,
    checksum_ok: false,
    plugin_file: "/opt/cpa/plugins/cpa-account-config-manager.dylib",
    plugin_file_source: "setting",
    plugin_file_exists: true,
    restart_required: false,
    ui_updated: false,
    interface_refresh_only: false,
    can_install: true,
    ...overrides,
  };
}

describe("SelfUpdatePanel", () => {
  beforeEach(() => {
    _resetSessionForTest();
    localStorage.clear();
    setSession("", "management-secret");
    vi.restoreAllMocks();
  });

  it("resolves the release from GitHub and installs a checksum-verified update", async () => {
    const user = userEvent.setup();
    const onNotice = vi.fn();
    vi.spyOn(api, "getSelfUpdate").mockResolvedValue(snapshot({ update_available: false, latest_version: undefined, checked_at: undefined }));
    const checkSpy = vi.spyOn(api, "checkSelfUpdate").mockResolvedValue(snapshot());
    const installSpy = vi.spyOn(api, "installSelfUpdate").mockResolvedValue(snapshot({
      checksum_ok: true,
      archive_sha256: "a".repeat(64),
      applied_version: "0.3.1416",
      restart_required: true,
      staged_path: "/opt/cpa/plugins/cpa-account-config-manager.dylib.update-staged",
      backup_path: "/opt/cpa/plugins/cpa-account-config-manager.dylib.previous",
    }));

    render(<SelfUpdatePanel onAPIError={() => undefined} onNotice={onNotice} />);

    // The panel states its own update channel and never claims the plugin store is used.
    const panel = await screen.findByRole("region", { name: "GitHub 直连自更新" });
    expect(await screen.findByText(/不依赖 CPA 插件市场/)).toBeInTheDocument();
    expect(screen.getByText("/opt/cpa/plugins/cpa-account-config-manager.dylib")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "检查 GitHub 发布" }));
    await waitFor(() => expect(checkSpy).toHaveBeenCalledTimes(1));
    expect(onNotice).toHaveBeenCalledWith("GitHub 上有新版本 0.3.1416。");
    // The resolution channel is surfaced so an operator can tell the API apart from a fallback.
    expect(await screen.findByText("GitHub API")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "下载并安装" }));
    await waitFor(() => expect(installSpy).toHaveBeenCalledTimes(1));
    expect(onNotice).toHaveBeenLastCalledWith("版本 0.3.1416 已校验并写入，重启 CPA 后生效。");
    // A replaced library cannot be swapped in-process, so the restart requirement is explicit.
    expect(screen.getByText("请重启 CPA 以加载更新后的插件库。")).toBeInTheDocument();
    expect(screen.getByText("旧版本库文件保留在 /opt/cpa/plugins/cpa-account-config-manager.dylib.previous")).toBeInTheDocument();
    expect(panel).toBeInTheDocument();
  });

  it("asks CPA to reload in place instead of waiting for a restart", async () => {
    const user = userEvent.setup();
    const onNotice = vi.fn();
    // The library is already replaced, so the panel offers the one action CPA watches for a
    // native plugin reload.
    vi.spyOn(api, "getSelfUpdate").mockResolvedValue(snapshot({
      update_available: false,
      applied_version: "0.3.1420",
      restart_required: true,
    }));
    const reloadSpy = vi.spyOn(api, "reloadSelfUpdateThroughStore").mockResolvedValue({
      reloaded: true,
      store_version: "0.3.1420",
      applied_version: "0.3.1420",
      restart_required: false,
    });

    render(<SelfUpdatePanel onAPIError={() => undefined} onNotice={onNotice} />);

    const panel = await screen.findByRole("region", { name: "GitHub 直连自更新" });
    await user.click(within(panel).getByRole("button", { name: "不重启热重载" }));

    await waitFor(() => expect(reloadSpy).toHaveBeenCalledTimes(1));
    expect(onNotice).toHaveBeenCalledWith("CPA 已重新加载插件，刷新本页即可使用新版本。");
  });

  it("treats a dropped reload connection as success once the new version answers", async () => {
    const user = userEvent.setup();
    const onNotice = vi.fn();
    // The plugin is swapped while the call is open, so the gateway reports an invalid response
    // (Cloudflare 502). The reloaded instance answers with its own version, which is the only
    // reliable signal, so the panel polls instead of reporting a failure.
    const getSpy = vi.spyOn(api, "getSelfUpdate")
      .mockResolvedValueOnce(snapshot({ update_available: false, applied_version: "0.3.1422", restart_required: true }))
      .mockResolvedValue(snapshot({ current_version: "0.3.1422", update_available: false, restart_required: false }));
    vi.spyOn(api, "reloadSelfUpdateThroughStore").mockRejectedValue(new api.APIError(502, "origin_bad_gateway"));

    render(<SelfUpdatePanel onAPIError={() => undefined} onNotice={onNotice} />);

    const panel = await screen.findByRole("region", { name: "GitHub 直连自更新" });
    await user.click(within(panel).getByRole("button", { name: "不重启热重载" }));

    await waitFor(() => expect(onNotice).toHaveBeenCalledWith("CPA 已重新加载插件，刷新本页即可使用新版本。"), { timeout: 15_000 });
    expect(getSpy.mock.calls.length).toBeGreaterThan(1);
  }, 20_000);

  it("reports an unconfirmed reload when the version never changes", async () => {
    const user = userEvent.setup();
    // Keep the case fast: the real window is 90 seconds.
    const originalWindow = reloadPollTiming.windowMS;
    reloadPollTiming.windowMS = 1500;
    try {
    const onNotice = vi.fn();
    vi.spyOn(api, "getSelfUpdate").mockResolvedValue(snapshot({ update_available: false, applied_version: "0.3.1422", restart_required: true }));
    vi.spyOn(api, "reloadSelfUpdateThroughStore").mockRejectedValue(new api.APIError(502, "origin_bad_gateway"));

    render(<SelfUpdatePanel onAPIError={() => undefined} onNotice={onNotice} />);

    const panel = await screen.findByRole("region", { name: "GitHub 直连自更新" });
    await user.click(within(panel).getByRole("button", { name: "不重启热重载" }));

      await waitFor(() => expect(onNotice).toHaveBeenCalledWith(expect.stringContaining("没有确认到热重载")), { timeout: 15_000 });
    } finally {
      reloadPollTiming.windowMS = originalWindow;
    }
  }, 20_000);

  it("names the reason when CPA refuses the reload", async () => {
    const user = userEvent.setup();
    const onNotice = vi.fn();
    vi.spyOn(api, "getSelfUpdate").mockResolvedValue(snapshot({ update_available: false, applied_version: "0.3.1420", restart_required: true }));
    vi.spyOn(api, "reloadSelfUpdateThroughStore").mockResolvedValue({
      reloaded: false,
      restart_required: true,
      reason: "plugin_store_unavailable",
    });

    render(<SelfUpdatePanel onAPIError={() => undefined} onNotice={onNotice} />);

    const panel = await screen.findByRole("region", { name: "GitHub 直连自更新" });
    await user.click(within(panel).getByRole("button", { name: "不重启热重载" }));

    // A refusal explains itself instead of leaving the operator guessing why nothing happened.
    await waitFor(() => expect(onNotice).toHaveBeenCalledWith("读不到 CPA 插件市场，因此没有发起热重载。"));
    expect(await screen.findByText(/上次热重载结果：plugin_store_unavailable/)).toBeInTheDocument();
  });

  it("blocks installation until the plugin library is located and saves a corrected path", async () => {
    const user = userEvent.setup();
    const onNotice = vi.fn();
    vi.spyOn(api, "getSelfUpdate").mockResolvedValue(snapshot({ plugin_file: "/opt/cpa/plugins/missing.dylib", plugin_file_exists: false, can_install: false }));
    const saveSpy = vi.spyOn(api, "saveSelfUpdateSettings").mockResolvedValue(snapshot({ plugin_file: "/srv/cpa/plugins/cpa-account-config-manager.dylib", plugin_file_source: "setting" }));

    render(<SelfUpdatePanel onAPIError={() => undefined} onNotice={onNotice} />);

    // A stale recorded path is reported instead of offering an update that cannot apply.
    expect(await screen.findByText(/记录的插件库文件不存在/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "下载并安装" })).toBeDisabled();

    const field = screen.getByRole("textbox", { name: "插件库文件" });
    await user.clear(field);
    await user.type(field, "/srv/cpa/plugins/cpa-account-config-manager.dylib");
    await user.click(screen.getByRole("button", { name: "保存路径" }));

    await waitFor(() => expect(saveSpy).toHaveBeenCalledWith("/srv/cpa/plugins/cpa-account-config-manager.dylib"));
    expect(onNotice).toHaveBeenCalledWith("插件库路径已保存");
  });

  it("explains an unresolvable check instead of reporting the plugin as current", async () => {
    vi.spyOn(api, "getSelfUpdate").mockResolvedValue(snapshot({ latest_version: undefined, update_available: false }));
    vi.spyOn(api, "checkSelfUpdate").mockRejectedValue(new api.APIError(502, "the latest release could not be resolved"));

    render(<SelfUpdatePanel onAPIError={() => undefined} onNotice={() => undefined} />);
    const panel = await screen.findByRole("region", { name: "GitHub 直连自更新" });

    await userEvent.setup().click(screen.getByRole("button", { name: "检查 GitHub 发布" }));
    expect(await screen.findByText("the latest release could not be resolved")).toBeInTheDocument();
    expect(panel).toBeInTheDocument();
  });
});

import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api/client";
import { _resetSessionForTest, setSession } from "../store/session";
import type { PolicySnapshot } from "../types";
import { AutomationPolicySettings } from "./AutomationPolicySettings";

const globalPolicy = {
  policy: {
    enabled: true,
    disabled: null,
    priority: null,
    concurrency_limit: null,
    quota_policy: null,
    note: null,
    prefix: null,
    proxy_url: null,
    proxy_profile_id: null,
    ai_provider_proxy_profile_id: null,
    websockets: null,
    headers: null,
    model_policy: null,
    codex_identity: {
      outbound_convergence_enabled: false,
      ingress_gate_enabled: false,
      allow_app_server_clients: false,
    },
  },
};

const snapshot: PolicySnapshot = {
  policy: {
    enabled: true,
    new_account_model_probe_enabled: false,
    codex_quota_metadata_probe_enabled: false,
    apply_mode: "missing",
    scan_interval_seconds: 15,
    priority: null,
    websockets: null,
    conditional_rules: [],
  },
  running: false,
  last_scan: { scanned: 12, eligible: 12, changed: 1, skipped: 11, failed: 0, quota_metadata_probed: 10, quota_metadata_updated: 9, quota_metadata_failed: 1 },
};

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

describe("AutomationPolicySettings", () => {
  beforeEach(() => {
    _resetSessionForTest();
    localStorage.clear();
    setSession("", "management-secret");
    vi.restoreAllMocks();
    vi.spyOn(api, "listProxyProfiles").mockResolvedValue({ profiles: [] });
    vi.spyOn(api, "getGlobalPolicy").mockResolvedValue(globalPolicy);
  });

  it("ignores stale policy responses after a refresh revision changes", async () => {
    const first = deferred<PolicySnapshot>();
    const second = deferred<PolicySnapshot>();
    vi.spyOn(api, "getDefaultPolicy")
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise);

    const view = render(<AutomationPolicySettings refreshRevision={0} forceLoading={false} onAPIError={vi.fn()} onNotice={vi.fn()} onForcePreview={vi.fn()} />);
    view.rerender(<AutomationPolicySettings refreshRevision={1} forceLoading={false} onAPIError={vi.fn()} onNotice={vi.fn()} onForcePreview={vi.fn()} />);

    second.resolve({ ...snapshot, last_scan: { ...snapshot.last_scan, scanned: 99 } });
    const metrics = await screen.findByLabelText("最近扫描统计");
    const scannedMetric = metrics.querySelector("div")!;
    expect(within(scannedMetric).getByText("99")).toBeInTheDocument();

    first.resolve({ ...snapshot, last_scan: { ...snapshot.last_scan, scanned: 1 } });
    await waitFor(() => expect(within(scannedMetric).getByText("99")).toBeInTheDocument());
    expect(within(scannedMetric).queryByText("1")).not.toBeInTheDocument();
  });

  it("keeps global configuration and automation defaults in separate, labeled sections", async () => {
    vi.spyOn(api, "getDefaultPolicy").mockResolvedValue(snapshot);

    render(<AutomationPolicySettings refreshRevision={0} forceLoading={false} onAPIError={vi.fn()} onNotice={vi.fn()} onForcePreview={vi.fn()} />);

    expect(await screen.findByRole("region", { name: "全局配置覆盖" })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "全局配置覆盖" })).toHaveTextContent("账号基础配置");
    expect(screen.getByRole("region", { name: "全局配置覆盖" })).toHaveTextContent("路由与传输");
    expect(screen.getByRole("region", { name: "全局配置覆盖" })).toHaveTextContent("额度限制");
    expect(screen.getByRole("region", { name: "全局配置覆盖" })).toHaveTextContent("请求与模型策略");
    expect(screen.getByRole("region", { name: "全局配置覆盖" })).toHaveTextContent("Codex 身份兼容");
    expect(screen.getByRole("region", { name: "自动策略默认设置" })).toHaveTextContent("自动化运行");
    expect(screen.getByRole("button", { name: "保存全局配置" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "保存默认策略" })).toBeInTheDocument();
  });

  it("builds multiple prioritized policies with nested conditions and model routing", async () => {
    const user = userEvent.setup();
    const save = vi.spyOn(api, "saveDefaultPolicy").mockImplementation(async (policy) => ({ ...snapshot, policy }));
    vi.spyOn(api, "getDefaultPolicy").mockResolvedValue(snapshot);
    vi.spyOn(api, "scanDefaultPolicy").mockResolvedValue(snapshot);

    render(<AutomationPolicySettings refreshRevision={0} forceLoading={false} onAPIError={vi.fn()} onNotice={vi.fn()} onForcePreview={vi.fn()} />);
    expect(await screen.findByText("12")).toBeInTheDocument();
    const panel = screen.getByRole("tabpanel", { name: "自动策略" });

    await user.click(within(panel).getByRole("button", { name: "添加策略" }));
    await user.click(within(panel).getByRole("button", { name: "添加策略" }));
    const names = within(panel).getAllByLabelText("策略名称");
    expect(names).toHaveLength(2);
    await user.type(names[0], "Codex 默认");
    await user.type(names[1], "Codex Free");
    await user.click(within(panel).getAllByRole("button", { name: "上移策略" })[1]);

    const rules = within(panel).getAllByRole("article");
    const specificRule = rules[0];
    await user.click(within(specificRule).getByRole("button", { name: "添加嵌套条件组" }));
    const values = within(specificRule).getAllByLabelText("条件值");
    await user.clear(values[0]);
    await user.type(values[0], "codex");
    await user.clear(values[1]);
    await user.type(values[1], "free");
    await user.click(within(specificRule).getByRole("checkbox", { name: "模型路由" }));
    await user.click(within(specificRule).getByRole("button", { name: "白名单" }));
    const modelIDs = within(specificRule).getByLabelText("模型 ID");
    await user.clear(modelIDs);
    await user.type(modelIDs, "gpt-5.5{enter}gpt-5.4-mini");

    await user.click(within(panel).getByRole("button", { name: "保存策略" }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    const saved = save.mock.calls[0][0];
    expect(saved.conditional_rules).toHaveLength(2);
    expect(saved.conditional_rules?.[0]).toMatchObject({
      name: "Codex Free",
      enabled: true,
      conditions: {
        operator: "all",
        conditions: [{ field: "provider", value: "codex" }],
        groups: [{ operator: "all", conditions: [{ field: "account_type", value: "free" }] }],
      },
      actions: {
        websockets: true,
        model_policy: { mode: "allow_only", models: ["gpt-5.5", "gpt-5.4-mini"] },
      },
    });
  }, 15_000);

  it("keeps the Codex quota metadata probe as a hidden always-on capability", async () => {
    const user = userEvent.setup();
    const save = vi.spyOn(api, "saveDefaultPolicy").mockImplementation(async (policy) => ({ ...snapshot, policy }));
    vi.spyOn(api, "getDefaultPolicy").mockResolvedValue(snapshot);
    vi.spyOn(api, "scanDefaultPolicy").mockResolvedValue(snapshot);

    render(<AutomationPolicySettings refreshRevision={0} forceLoading={false} onAPIError={vi.fn()} onNotice={vi.fn()} onForcePreview={vi.fn()} />);
    await screen.findByRole("checkbox", { name: "启用新账号模型探测" });
    const panel = screen.getByRole("tabpanel", { name: "自动策略" });
    expect(within(panel).queryByText("Codex 套餐与重置次数探测")).not.toBeInTheDocument();
    expect(within(panel).queryByText("最近探测：更新 9/10 · 失败 1")).not.toBeInTheDocument();
    expect(within(panel).queryByText("始终启用")).not.toBeInTheDocument();
    expect(within(panel).queryByRole("checkbox", { name: "启用 Codex 套餐与重置次数探测" })).not.toBeInTheDocument();

    await user.click(within(panel).getByRole("checkbox", { name: "启用新账号模型探测" }));
    await user.click(within(panel).getByRole("button", { name: "保存策略" }));

    await waitFor(() => expect(save).toHaveBeenCalledWith(expect.objectContaining({ codex_quota_metadata_probe_enabled: true })));
  });

  it("saves enabled proxy profile references for default and conditional account/provider policies", async () => {
    const user = userEvent.setup();
    const save = vi.spyOn(api, "saveDefaultPolicy").mockImplementation(async (policy) => ({ ...snapshot, policy }));
    vi.spyOn(api, "getDefaultPolicy").mockResolvedValue(snapshot);
    vi.mocked(api.listProxyProfiles).mockResolvedValue({
      profiles: [
        { id: "proxy-account", name: "账号出口", proxy_url_masked: "socks5://user:***@proxy.example:1080", enabled: true, account_count: 2, created_at: "", updated_at: "" },
        { id: "proxy-provider", name: "供应商出口", proxy_url_masked: "https://***@provider.example", enabled: true, account_count: 0, created_at: "", updated_at: "" },
        { id: "proxy-disabled", name: "停用出口", proxy_url_masked: "https://***@disabled.example", enabled: false, account_count: 0, created_at: "", updated_at: "" },
      ],
    });

    render(<AutomationPolicySettings refreshRevision={0} forceLoading={false} onAPIError={vi.fn()} onNotice={vi.fn()} onForcePreview={vi.fn()} />);
    await screen.findByRole("checkbox", { name: "启用新账号模型探测" });
    const panel = screen.getByRole("tabpanel", { name: "自动策略" });
    await waitFor(() =>
      expect(
        within(panel).getAllByRole("option", {
          name: "账号出口 · socks5://user:***@proxy.example:1080",
        }),
      ).toHaveLength(4),
    );
    expect(within(panel).queryByText(/disabled\.example/)).not.toBeInTheDocument();

    await user.selectOptions(within(panel).getAllByLabelText("默认账号代理")[1], "proxy-account");
    await user.selectOptions(within(panel).getAllByLabelText("默认 AI 供应商代理")[1], "proxy-provider");
    await user.click(within(panel).getByRole("button", { name: "添加策略" }));
    const rule = within(panel).getByRole("article");
    await user.click(within(rule).getByRole("checkbox", { name: "账号代理档案" }));
    await user.click(within(rule).getByRole("checkbox", { name: "AI 供应商代理档案" }));
    const conditionalSelects = within(rule).getAllByRole("combobox");
    await user.selectOptions(conditionalSelects.at(-2)!, "proxy-provider");
    await user.selectOptions(conditionalSelects.at(-1)!, "proxy-account");

    await user.click(within(panel).getByRole("button", { name: "保存策略" }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0][0]).toMatchObject({
      proxy_profile_id: "proxy-account",
      ai_provider_proxy_profile_id: "proxy-provider",
      conditional_rules: [{ actions: { proxy_profile_id: "proxy-provider", ai_provider_proxy_profile_id: "proxy-account" } }],
    });
  });

  it("saves without running until the user explicitly starts the asynchronous scan", async () => {
    const user = userEvent.setup();
    const changed = { ...snapshot.policy, new_account_model_probe_enabled: true };
    const save = vi.spyOn(api, "saveDefaultPolicy").mockResolvedValue({ ...snapshot, policy: changed });
    const scan = vi.spyOn(api, "scanDefaultPolicy").mockResolvedValue({ ...snapshot, policy: changed });
    vi.spyOn(api, "getDefaultPolicy").mockResolvedValue(snapshot);

    render(<AutomationPolicySettings refreshRevision={0} forceLoading={false} onAPIError={vi.fn()} onNotice={vi.fn()} onForcePreview={vi.fn()} />);
    const modelProbe = await screen.findByRole("checkbox", { name: "启用新账号模型探测" });
    const panel = screen.getByRole("tabpanel", { name: "自动策略" });
    await user.click(modelProbe);
    await user.click(within(panel).getByRole("button", { name: "保存策略" }));

    expect(await screen.findByRole("dialog", { name: "保存后执行自动策略？" })).toBeInTheDocument();
    expect(save).toHaveBeenCalledTimes(1);
    expect(scan).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "仅保存" }));
    expect(screen.queryByRole("dialog", { name: "保存后执行自动策略？" })).not.toBeInTheDocument();
    expect(scan).not.toHaveBeenCalled();
  });

  it("closes the save confirmation and starts execution through the background scan endpoint", async () => {
    const user = userEvent.setup();
    const changed = { ...snapshot.policy, new_account_model_probe_enabled: true };
    vi.spyOn(api, "saveDefaultPolicy").mockResolvedValue({ ...snapshot, policy: changed });
    const scan = vi.spyOn(api, "scanDefaultPolicy").mockResolvedValue({ ...snapshot, policy: changed });
    vi.spyOn(api, "getDefaultPolicy").mockResolvedValue(snapshot);

    render(<AutomationPolicySettings refreshRevision={0} forceLoading={false} onAPIError={vi.fn()} onNotice={vi.fn()} onForcePreview={vi.fn()} />);
    const modelProbe = await screen.findByRole("checkbox", { name: "启用新账号模型探测" });
    const panel = screen.getByRole("tabpanel", { name: "自动策略" });
    await user.click(modelProbe);
    await user.click(within(panel).getByRole("button", { name: "保存策略" }));
    await user.click(await screen.findByRole("button", { name: "后台执行" }));

    expect(screen.queryByRole("dialog", { name: "保存后执行自动策略？" })).not.toBeInTheDocument();
    await waitFor(() => expect(scan).toHaveBeenCalledTimes(1));
  });
});

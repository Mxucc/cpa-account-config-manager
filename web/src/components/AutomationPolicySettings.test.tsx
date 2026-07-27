import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api/client";
import { _resetSessionForTest, setSession } from "../store/session";
import type { PolicySnapshot } from "../types";
import { AutomationPolicySettings } from "./AutomationPolicySettings";

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

describe("AutomationPolicySettings", () => {
  beforeEach(() => {
    _resetSessionForTest();
    localStorage.clear();
    setSession("", "management-secret");
    vi.restoreAllMocks();
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
  });

  it("persists the Codex quota metadata probe independently and shows its last result", async () => {
    const user = userEvent.setup();
    const save = vi.spyOn(api, "saveDefaultPolicy").mockImplementation(async (policy) => ({ ...snapshot, policy }));
    vi.spyOn(api, "getDefaultPolicy").mockResolvedValue(snapshot);
    vi.spyOn(api, "scanDefaultPolicy").mockResolvedValue(snapshot);

    render(<AutomationPolicySettings refreshRevision={0} forceLoading={false} onAPIError={vi.fn()} onNotice={vi.fn()} onForcePreview={vi.fn()} />);
    const probeSwitch = await screen.findByRole("checkbox", { name: "启用 Codex 套餐与重置次数探测" });
    const panel = screen.getByRole("tabpanel", { name: "自动策略" });
    expect(within(panel).getByText("最近探测：更新 9/10 · 失败 1")).toBeInTheDocument();

    await user.click(probeSwitch);
    await user.click(within(panel).getByRole("button", { name: "保存策略" }));

    await waitFor(() => expect(save).toHaveBeenCalledWith(expect.objectContaining({ codex_quota_metadata_probe_enabled: true })));
  });
});

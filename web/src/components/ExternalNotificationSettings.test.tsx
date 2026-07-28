import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api/client";
import type { InspectionPolicy } from "../types";
import { ExternalNotificationSettings } from "./ExternalNotificationSettings";

function inspectionPolicy(): InspectionPolicy {
  return {
    enabled: true, scan_interval_minutes: 30,
    model_probe_enabled: false, model_probe_full_sweep: false, scan_manually_disabled: false, model_probe_interval_minutes: 60, model_probe_batch_size: 20,
    model_probe_models: { codex: "gpt-5.6-sol", openai: "gpt-5.6-sol", claude: "claude-sonnet-4-5-20250929", gemini: "gemini-2.0-flash", xai: "grok-4" },
    failure_threshold: 3, recovery_threshold: 2,
    passive_circuit_enabled: false, passive_failure_threshold: 5, passive_failure_window_minutes: 180, passive_circuit_minutes: 15,
    auto_disable: false, auto_enable: false, auto_delete: false, auto_delete_invalid_credentials: false, delete_grace_hours: 168, delete_batch_size: 10,
    anomaly_trigger_enabled: true, anomaly_threshold_percent: 50, anomaly_minimum_accounts: 10, anomaly_cooldown_minutes: 60,
    anomaly_notification_enabled: true, anomaly_notification_only: true,
    anomaly_notification_url: "https://legacy.example/hook?available=${available_accounts}",
    notification_available_accounts_enabled: true, notification_available_accounts_threshold: 10,
    notification_availability_percent_enabled: true, notification_availability_percent_threshold: 20, notification_cooldown_minutes: 60,
  };
}

describe("ExternalNotificationSettings", () => {
  beforeEach(() => vi.restoreAllMocks());

  it("loads a legacy URL, adds another endpoint, tests it independently, and persists the endpoint list", async () => {
    const user = userEvent.setup();
    const initial = inspectionPolicy();
    vi.spyOn(api, "getInspection").mockResolvedValue({ policy: initial } as Awaited<ReturnType<typeof api.getInspection>>);
    const preview = vi.spyOn(api, "previewInspectionNotification").mockImplementation(async (request) => ({
      endpoint_id: request.endpoint_id,
      endpoint_name: request.endpoint_name,
      scenario: request.scenario,
      event: request.scenario,
      expanded_url: request.url_template.replace("${available_percent}", "8%"),
      variables: { available_percent: "8%", available_accounts: "2", event: request.scenario },
      triggered_at: "2026-07-26T08:00:00Z",
    }));
    const test = vi.spyOn(api, "testInspectionNotification").mockImplementation(async (request) => ({
      preview: await preview(request), delivered: true, status_code: 204, attempts: 1, reason_code: "notification_delivered",
    }));
    const save = vi.spyOn(api, "saveInspectionPolicy").mockImplementation(async (policy) => ({ policy } as Awaited<ReturnType<typeof api.saveInspectionPolicy>>));
    const onNotice = vi.fn();

    render(<ExternalNotificationSettings refreshRevision={0} onAPIError={() => undefined} onNotice={onNotice} />);

    const first = await screen.findByRole("region", { name: "通知链接 1" });
    expect(within(first).getByLabelText("通知链接 1 URL 模板")).toHaveValue("https://legacy.example/hook?available=${available_accounts}");
    await user.click(screen.getByRole("button", { name: "添加通知链接" }));
    const second = screen.getByRole("region", { name: "通知链接 2" });
    await user.type(within(second).getByLabelText("通知链接 2 名称"), "备用通知");
    await user.type(within(second).getByLabelText("通知链接 2 URL 模板"), "https://backup.example/hook?rate=");
    await user.selectOptions(within(second).getByLabelText("向通知链接 2 插入参数"), "available_percent");

    await user.click(within(second).getByRole("button", { name: "预览" }));
    await waitFor(() => expect(preview).toHaveBeenCalledWith(expect.objectContaining({
      endpoint_name: "备用通知", url_template: "https://backup.example/hook?rate=${available_percent}", scenario: "manual_test",
    })));
    expect(within(second).getByText("https://backup.example/hook?rate=8%")).toBeInTheDocument();
    await user.click(within(second).getByRole("button", { name: "发送测试" }));
    await waitFor(() => expect(test).toHaveBeenCalledTimes(1));
    expect(within(second).getByText("外部通知发送成功")).toBeInTheDocument();
    expect(within(second).getByText("204")).toBeInTheDocument();
    expect(screen.queryByRole("tablist", { name: "通知测试场景" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "手动测试" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "异常占比" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "可用账号数" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "账号可用率" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "组合场景" })).not.toBeInTheDocument();

    await user.click(within(second).getByRole("button", { name: "上移通知链接" }));

    await user.click(screen.getByRole("button", { name: "保存设置" }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    const saved = save.mock.calls[0][0];
    expect(saved.anomaly_notification_url).toBe("");
    expect(saved.notification_endpoints).toEqual([
      expect.objectContaining({ name: "备用通知", url: "https://backup.example/hook?rate=${available_percent}", enabled: true }),
      { id: "legacy", name: "", url: "https://legacy.example/hook?available=${available_accounts}", enabled: true },
    ]);
    expect(onNotice).toHaveBeenCalledWith("外部通知设置已保存");
  });

  it("rejects duplicate URLs and requires an enabled endpoint for active triggers", async () => {
    const user = userEvent.setup();
    const policy = inspectionPolicy();
    policy.notification_endpoints = [
      { id: "first", url: "https://notify.example/hook", enabled: false },
      { id: "second", url: "https://notify.example/hook", enabled: false },
    ];
    policy.anomaly_notification_url = "";
    vi.spyOn(api, "getInspection").mockResolvedValue({ policy } as Awaited<ReturnType<typeof api.getInspection>>);
    const save = vi.spyOn(api, "saveInspectionPolicy");

    render(<ExternalNotificationSettings refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);
    await screen.findByRole("region", { name: "通知链接 1" });
    await user.click(screen.getByRole("button", { name: "保存设置" }));
    expect(screen.getByRole("alert")).toHaveTextContent("URL 不能重复");
    expect(save).not.toHaveBeenCalled();

    const second = screen.getByRole("region", { name: "通知链接 2" });
    await user.clear(within(second).getByLabelText("通知链接 2 URL 模板"));
    await user.type(within(second).getByLabelText("通知链接 2 URL 模板"), "https://backup.example/hook");
    await user.click(screen.getByRole("button", { name: "保存设置" }));
    expect(screen.getByRole("alert")).toHaveTextContent("至少启用一条通用通知链接");
    expect(save).not.toHaveBeenCalled();
  });

  it("creates a complete ordered policy notification with its endpoints and persists all/any operators", async () => {
    const user = userEvent.setup();
    const initial = inspectionPolicy();
    vi.spyOn(api, "getInspection").mockResolvedValue({ policy: initial } as Awaited<ReturnType<typeof api.getInspection>>);
    const save = vi.spyOn(api, "saveInspectionPolicy").mockImplementation(async (policy) => ({ policy } as Awaited<ReturnType<typeof api.saveInspectionPolicy>>));

    render(<ExternalNotificationSettings refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);
    await screen.findByRole("region", { name: "通知链接 1" });
    expect(screen.getByRole("region", { name: "通用通知" })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "策略通知" })).toBeInTheDocument();

    const policyWorkspace = screen.getByRole("region", { name: "策略通知" });
    await user.click(within(policyWorkspace).getAllByRole("button", { name: "添加策略通知" })[0]);
    const firstPolicy = screen.getByRole("article", { name: "通知策略 1" });
    expect(within(firstPolicy).getByLabelText("通知策略 1 名称")).toHaveValue("策略通知 1");
    await user.clear(within(firstPolicy).getByLabelText("通知策略 1 名称"));
    await user.type(within(firstPolicy).getByLabelText("通知策略 1 名称"), "Free Codex");
    const accountConditions = within(firstPolicy).getByRole("group", { name: "条件关系" });
    await user.click(within(accountConditions).getByRole("button", { name: "任一满足" }));
    expect(within(accountConditions).getByRole("button", { name: "任一满足" })).toHaveAttribute("aria-pressed", "true");
    expect(within(firstPolicy).getByText("符合组内任意一个条件即可匹配账号")).toBeInTheDocument();
    await user.click(within(firstPolicy).getByRole("button", { name: "添加条件" }));
    const conditionFields = within(firstPolicy).getAllByLabelText("条件字段");
    await user.selectOptions(conditionFields[1], "account_type");
    const conditionValues = within(firstPolicy).getAllByLabelText("条件值");
    await user.type(conditionValues[1], "free");

    const thresholds = within(firstPolicy).getByRole("group", { name: "触发条件组合方式" });
    await user.click(within(thresholds).getByRole("button", { name: "任一满足" }));
    expect(within(thresholds).getByRole("button", { name: "任一满足" })).toHaveAttribute("aria-pressed", "true");
    expect(within(firstPolicy).getByText("任意一个已启用的阈值达到时触发通知")).toBeInTheDocument();

    const firstPolicyEndpoints = within(firstPolicy).getByRole("region", { name: "Free Codex 的策略通知链接" });
    const firstPolicyEndpoint = within(firstPolicyEndpoints).getByRole("region", { name: "通知链接 1" });
    expect(within(firstPolicyEndpoint).queryByLabelText("通知链接 1 名称")).not.toBeInTheDocument();
    await user.type(within(firstPolicyEndpoint).getByLabelText("通知链接 1 URL 模板"), "https://free.example/hook?available=${available_accounts}");
    expect(within(firstPolicyEndpoint).queryByLabelText("通知链接 1 的触发策略")).not.toBeInTheDocument();

    await user.click(within(policyWorkspace).getByRole("button", { name: "添加策略通知" }));
    const secondPolicy = screen.getByRole("article", { name: "通知策略 2" });
    expect(within(secondPolicy).getByLabelText("通知策略 2 名称")).toHaveValue("策略通知 2");
    await user.clear(within(secondPolicy).getByLabelText("通知策略 2 名称"));
    await user.type(within(secondPolicy).getByLabelText("通知策略 2 名称"), "Team Codex");
    await user.click(within(secondPolicy).getByRole("button", { name: "上移通知策略" }));
    await user.click(within(secondPolicy).getAllByRole("checkbox")[0]);
    const secondPolicyEndpoints = within(secondPolicy).getByRole("region", { name: "Team Codex 的策略通知链接" });
    const secondPolicyEndpoint = within(secondPolicyEndpoints).getByRole("region", { name: "通知链接 1" });
    await user.type(within(secondPolicyEndpoint).getByLabelText("通知链接 1 URL 模板"), "https://team.example/hook");

    await user.click(screen.getByRole("button", { name: "保存设置" }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    const saved = save.mock.calls[0][0];
    expect(saved.notification_policies?.map((item) => [item.name, item.enabled])).toEqual([["Team Codex", false], ["Free Codex", true]]);
    expect(saved.notification_policies?.[1].conditions).toEqual({
      operator: "any",
      conditions: [{ field: "provider", value: "codex" }, { field: "account_type", value: "free" }],
      groups: [],
    });
    expect(saved.notification_policies?.[1].threshold_operator).toBe("any");
    expect(saved.notification_endpoints?.[0].notification_policy_id).toBeFalsy();
    expect(saved.notification_endpoints?.find((endpoint) => endpoint.url.includes("free.example"))?.notification_policy_id).toBe(saved.notification_policies?.[1].id);
    expect(saved.notification_endpoints?.find((endpoint) => endpoint.url.includes("team.example"))?.notification_policy_id).toBe(saved.notification_policies?.[0].id);
    const reloadedFreePolicy = screen.getByRole("article", { name: "通知策略 2" });
    expect(within(within(reloadedFreePolicy).getByRole("group", { name: "条件关系" })).getByRole("button", { name: "任一满足" })).toHaveAttribute("aria-pressed", "true");
    expect(within(within(reloadedFreePolicy).getByRole("group", { name: "触发条件组合方式" })).getByRole("button", { name: "任一满足" })).toHaveAttribute("aria-pressed", "true");
  });

  it("loads existing bound endpoints inside their policies and sorts only within one policy", async () => {
    const user = userEvent.setup();
    const initial = inspectionPolicy();
    initial.notification_policies = [
      {
        id: "free", name: "Free", enabled: true,
        conditions: { operator: "any", conditions: [{ field: "account_type", value: "free" }, { field: "email_suffix", value: "outlook.com" }], groups: [] },
        threshold_operator: "all", available_accounts_enabled: true, available_accounts_below: 2,
        availability_percent_enabled: false, availability_percent_below: 20,
      },
      {
        id: "team", name: "Team", enabled: true,
        conditions: { operator: "all", conditions: [{ field: "account_type", value: "team" }], groups: [] },
        threshold_operator: "all", available_accounts_enabled: true, available_accounts_below: 1,
        availability_percent_enabled: false, availability_percent_below: 20,
      },
    ];
    initial.notification_endpoints = [
      { id: "generic", url: "https://generic.example/hook", enabled: true },
      { id: "free-a", name: "旧策略链接名称", url: "https://free-a.example/hook", enabled: true, notification_policy_id: "free" },
      { id: "team-a", url: "https://team.example/hook", enabled: true, notification_policy_id: "team" },
      { id: "free-b", url: "https://free-b.example/hook", enabled: true, notification_policy_id: "free" },
    ];
    initial.anomaly_notification_url = "";
    vi.spyOn(api, "getInspection").mockResolvedValue({ policy: initial } as Awaited<ReturnType<typeof api.getInspection>>);
    const save = vi.spyOn(api, "saveInspectionPolicy").mockImplementation(async (policy) => ({ policy } as Awaited<ReturnType<typeof api.saveInspectionPolicy>>));

    render(<ExternalNotificationSettings refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const freePolicy = await screen.findByRole("article", { name: "通知策略 1" });
    const teamPolicy = screen.getByRole("article", { name: "通知策略 2" });
    const freeEndpoints = within(freePolicy).getByRole("region", { name: "Free 的策略通知链接" });
    const teamEndpoints = within(teamPolicy).getByRole("region", { name: "Team 的策略通知链接" });
    expect(within(freeEndpoints).getByDisplayValue("https://free-a.example/hook")).toBeInTheDocument();
    expect(within(freeEndpoints).getByDisplayValue("https://free-b.example/hook")).toBeInTheDocument();
    expect(within(freeEndpoints).queryByLabelText("通知链接 1 名称")).not.toBeInTheDocument();
    expect(within(teamEndpoints).getByDisplayValue("https://team.example/hook")).toBeInTheDocument();

    const secondFreeEndpoint = within(freeEndpoints).getByRole("region", { name: "通知链接 2" });
    await user.click(within(secondFreeEndpoint).getByRole("button", { name: "上移通知链接" }));
    await user.click(screen.getByRole("button", { name: "保存设置" }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0][0].notification_endpoints?.map((endpoint) => endpoint.id)).toEqual(["generic", "free-b", "team-a", "free-a"]);
    expect(save.mock.calls[0][0].notification_endpoints?.find((endpoint) => endpoint.id === "free-a")?.name).toBe("");
  });

  it("normalizes unnamed policies, rejects duplicate names, and never exposes internal IDs", async () => {
    const user = userEvent.setup();
    const initial = inspectionPolicy();
    initial.notification_policies = [
      {
        id: "notify-policy-internal-1", name: "", enabled: true,
        conditions: { operator: "all", conditions: [{ field: "provider", value: "codex" }], groups: [] },
        threshold_operator: "all", available_accounts_enabled: true, available_accounts_below: 2,
        availability_percent_enabled: false, availability_percent_below: 20,
      },
      {
        id: "notify-policy-internal-2", name: "Operations", enabled: true,
        conditions: { operator: "all", conditions: [{ field: "provider", value: "claude" }], groups: [] },
        threshold_operator: "all", available_accounts_enabled: true, available_accounts_below: 1,
        availability_percent_enabled: false, availability_percent_below: 20,
      },
    ];
    initial.notification_endpoints = [
      { id: "endpoint-internal-1", url: "https://codex.example/hook", enabled: true, notification_policy_id: "notify-policy-internal-1" },
      { id: "endpoint-internal-2", url: "https://claude.example/hook", enabled: true, notification_policy_id: "notify-policy-internal-2" },
    ];
    initial.anomaly_notification_url = "";
    initial.anomaly_notification_enabled = false;
    initial.notification_available_accounts_enabled = false;
    initial.notification_availability_percent_enabled = false;
    vi.spyOn(api, "getInspection").mockResolvedValue({ policy: initial } as Awaited<ReturnType<typeof api.getInspection>>);
    const save = vi.spyOn(api, "saveInspectionPolicy");
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);

    render(<ExternalNotificationSettings refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const firstPolicy = await screen.findByRole("article", { name: "通知策略 1" });
    const secondPolicy = screen.getByRole("article", { name: "通知策略 2" });
    expect(within(firstPolicy).getByLabelText("通知策略 1 名称")).toHaveValue("策略通知 1");
    expect(screen.queryByText(/notify-policy-internal/)).not.toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /通知策略 ID/ })).not.toBeInTheDocument();

    await user.click(within(firstPolicy).getByRole("button", { name: "删除通知策略" }));
    expect(confirm).toHaveBeenLastCalledWith("确定删除策略通知 策略通知 1 及其全部通知链接？");
    const firstEndpoint = within(firstPolicy).getByRole("region", { name: "通知链接 1" });
    await user.click(within(firstEndpoint).getByRole("button", { name: "删除通知链接 1" }));
    expect(confirm).toHaveBeenLastCalledWith("确定删除第 1 条通知链接？");

    const secondName = within(secondPolicy).getByLabelText("通知策略 2 名称");
    await user.clear(secondName);
    await user.type(secondName, "策略通知 1");
    await user.click(screen.getByRole("button", { name: "保存设置" }));
    expect(screen.getByRole("alert")).toHaveTextContent("策略名称“策略通知 1”已存在，请使用唯一名称");
    expect(save).not.toHaveBeenCalled();
  });
});

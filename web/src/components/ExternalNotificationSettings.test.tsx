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

  it("keeps policy notifications in a separate ordered card and binds endpoints explicitly", async () => {
    const user = userEvent.setup();
    const initial = inspectionPolicy();
    vi.spyOn(api, "getInspection").mockResolvedValue({ policy: initial } as Awaited<ReturnType<typeof api.getInspection>>);
    const save = vi.spyOn(api, "saveInspectionPolicy").mockImplementation(async (policy) => ({ policy } as Awaited<ReturnType<typeof api.saveInspectionPolicy>>));

    render(<ExternalNotificationSettings refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);
    await screen.findByRole("region", { name: "通知链接 1" });
    expect(screen.getByRole("region", { name: "通用通知" })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "策略通知" })).toBeInTheDocument();

    await user.click(screen.getAllByRole("button", { name: "添加通知策略" })[0]);
    const firstPolicy = screen.getByRole("article", { name: "通知策略 1" });
    await user.type(within(firstPolicy).getByLabelText("通知策略 1 名称"), "Free Codex");
    await user.click(within(firstPolicy).getByRole("button", { name: "添加嵌套条件组" }));
    const conditionValues = within(firstPolicy).getAllByLabelText("条件值");
    await user.type(conditionValues[1], "free");

    await user.click(screen.getByRole("button", { name: "添加通知策略" }));
    const secondPolicy = screen.getByRole("article", { name: "通知策略 2" });
    await user.type(within(secondPolicy).getByLabelText("通知策略 2 名称"), "Team Codex");
    await user.click(within(secondPolicy).getByRole("button", { name: "上移通知策略" }));
    await user.click(within(secondPolicy).getAllByRole("checkbox")[0]);

    await user.click(screen.getByRole("button", { name: "添加通知链接" }));
    const policyEndpoint = screen.getByRole("region", { name: "通知链接 2" });
    await user.type(within(policyEndpoint).getByLabelText("通知链接 2 URL 模板"), "https://policy.example/hook?available=${available_accounts}");
    await user.selectOptions(within(policyEndpoint).getByLabelText("通知链接 2 的触发策略"), "Free Codex");

    await user.click(screen.getByRole("button", { name: "保存设置" }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    const saved = save.mock.calls[0][0];
    expect(saved.notification_policies?.map((item) => [item.name, item.enabled])).toEqual([["Team Codex", false], ["Free Codex", true]]);
    expect(saved.notification_policies?.[1].conditions.groups?.[0].conditions).toEqual([{ field: "account_type", value: "free" }]);
    expect(saved.notification_endpoints?.[0].notification_policy_id).toBeFalsy();
    expect(saved.notification_endpoints?.[1].notification_policy_id).toBe(saved.notification_policies?.[1].id);
  });
});

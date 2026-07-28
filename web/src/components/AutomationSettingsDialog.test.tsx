import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { InspectionPolicy } from "../types";
import { AutomationSettingsDialog } from "./AutomationSettingsDialog";

function policy(overrides: Partial<InspectionPolicy> = {}): InspectionPolicy {
  return {
    enabled: false, scan_interval_minutes: 30,
    model_probe_enabled: true, model_probe_full_sweep: false, scan_manually_disabled: false, model_probe_interval_minutes: 60, model_probe_batch_size: 20,
    model_probe_models: { codex: "gpt-5.4", openai: "gpt-5.4", claude: "claude-sonnet-4-5-20250929", gemini: "gemini-2.0-flash", xai: "grok-4" },
    failure_threshold: 3, recovery_threshold: 2,
    passive_circuit_enabled: false, passive_failure_threshold: 5, passive_failure_window_minutes: 180, passive_circuit_minutes: 15,
    auto_disable: false, auto_enable: false, auto_delete: false, auto_delete_invalid_credentials: false, delete_grace_hours: 168, delete_batch_size: 10,
    anomaly_trigger_enabled: false, anomaly_threshold_percent: 50, anomaly_minimum_accounts: 10, anomaly_cooldown_minutes: 60,
    anomaly_notification_enabled: false, anomaly_notification_only: false, anomaly_notification_url: "", notification_endpoints: [],
    notification_available_accounts_enabled: false, notification_available_accounts_threshold: 10,
    notification_availability_percent_enabled: false, notification_availability_percent_threshold: 20, notification_cooldown_minutes: 60,
    ...overrides,
  };
}

describe("AutomationSettingsDialog", () => {
  it("keeps notification settings outside the inspection dialog while preserving them on save", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn();
    const notificationEndpoints = [{ id: "primary", name: "Primary", url: "https://notify.example/hook", enabled: true }];
    render(<AutomationSettingsDialog inspection={policy({ notification_available_accounts_enabled: true, notification_endpoints: notificationEndpoints })} saving={false} onClose={() => undefined} onSave={onSave} />);

    expect(screen.queryByText("外部通知")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "保存设置" }));
    expect(onSave).toHaveBeenCalledWith(expect.objectContaining({
      notification_available_accounts_enabled: true,
      notification_endpoints: notificationEndpoints,
    }), false, false);
  });

  it("requires explicit confirmation before enabling destructive inspection automation", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn();
    render(<AutomationSettingsDialog inspection={policy()} saving={false} onClose={() => undefined} onSave={onSave} />);

    await user.click(screen.getByLabelText("自动删除"));
    expect(screen.getByLabelText("自动禁用")).toBeChecked();
    await user.click(screen.getByLabelText("删除持续失效的凭据"));
    await user.click(screen.getByLabelText("定时巡检人工禁用账号"));
    await user.click(screen.getByLabelText("全量定时主动巡检"));
    await user.click(screen.getByLabelText("启用异常占比触发"));
    await user.click(screen.getByLabelText("被动临时熔断"));
    await user.click(screen.getByRole("button", { name: "保存设置" }));
    expect(screen.getByRole("alert")).toHaveTextContent("确认风险");
    expect(onSave).not.toHaveBeenCalled();

    await user.click(screen.getByLabelText("确认开启自动删除"));
    await user.click(screen.getByRole("button", { name: "保存设置" }));
    expect(screen.getByRole("alert")).toHaveTextContent("确认风险");
    await user.click(screen.getByLabelText("确认删除失效凭据"));
    await user.click(screen.getByRole("button", { name: "保存设置" }));

    expect(onSave).toHaveBeenCalledTimes(1);
    const [inspection, confirmDelete, confirmDeleteInvalid] = onSave.mock.calls[0];
    expect(inspection).toMatchObject({
      enabled: true,
      model_probe_full_sweep: true,
      scan_manually_disabled: true,
      auto_disable: true,
      auto_enable: true,
      passive_circuit_enabled: true,
      auto_delete: true,
      auto_delete_invalid_credentials: true,
      anomaly_trigger_enabled: true,
    });
    expect(confirmDelete).toBe(true);
    expect(confirmDeleteInvalid).toBe(true);
  });

  it("persists recovered Codex priority scheduling and enables its required automation", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn();
    render(<AutomationSettingsDialog inspection={policy()} saving={false} onClose={() => undefined} onSave={onSave} />);

    await user.click(screen.getByLabelText("Codex 额度恢复优先调度"));
    expect(screen.getByLabelText("自动启用")).toBeChecked();
    await user.click(screen.getByRole("button", { name: "保存设置" }));

    expect(onSave).toHaveBeenCalledWith(expect.objectContaining({
      enabled: true,
      auto_enable: true,
      quota_recovery_priority_enabled: true,
    }), false, false);
  });
});

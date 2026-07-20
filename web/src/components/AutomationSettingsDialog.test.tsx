import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { AutomationSettingsDialog } from "./AutomationSettingsDialog";

describe("AutomationSettingsDialog", () => {
  it("requires explicit confirmation before enabling delete and update automation", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn();
    render(
      <AutomationSettingsDialog
        inspection={{
          enabled: false, scan_interval_minutes: 30,
          model_probe_enabled: true, model_probe_full_sweep: false, scan_manually_disabled: false, model_probe_interval_minutes: 60, model_probe_batch_size: 20,
          model_probe_models: { codex: "gpt-5.4", openai: "gpt-5.4", claude: "claude-sonnet-4-5-20250929", gemini: "gemini-2.0-flash", xai: "grok-4" },
          failure_threshold: 3, recovery_threshold: 2, auto_disable: false, auto_enable: false,
          passive_circuit_enabled: false, passive_failure_threshold: 5, passive_failure_window_minutes: 180, passive_circuit_minutes: 15,
          auto_delete: false, auto_delete_invalid_credentials: false, delete_grace_hours: 168, delete_batch_size: 10,
          anomaly_trigger_enabled: false, anomaly_threshold_percent: 50, anomaly_minimum_accounts: 10, anomaly_cooldown_minutes: 60,
        }}
        updates={{ check_enabled: true, check_interval_hours: 24, auto_update: false }}
        saving={false}
        onClose={() => undefined}
        onSave={onSave}
      />,
    );

    await user.click(screen.getByLabelText("自动删除"));
    expect(screen.getByLabelText("自动禁用")).toBeChecked();
    await user.click(screen.getByLabelText("删除持续失效的凭据"));
    await user.click(screen.getByLabelText("巡检人工禁用账号"));
    await user.click(screen.getByLabelText("全量定时主动巡检"));
    await user.click(screen.getByLabelText("启用异常占比触发"));
    await user.click(screen.getByLabelText("自动更新"));
    await user.click(screen.getByLabelText("被动临时熔断"));
    await user.click(screen.getByRole("button", { name: "保存设置" }));
    expect(screen.getByRole("alert")).toHaveTextContent("确认风险");
    expect(onSave).not.toHaveBeenCalled();

    await user.click(screen.getByLabelText("确认开启自动删除"));
    await user.click(screen.getByRole("button", { name: "保存设置" }));
    expect(screen.getByRole("alert")).toHaveTextContent("确认风险");
    await user.click(screen.getByLabelText("确认删除失效凭据"));
    await user.click(screen.getByRole("button", { name: "保存设置" }));
    expect(screen.getByRole("alert")).toHaveTextContent("确认风险");
    await user.click(screen.getByLabelText("确认开启自动更新"));
    await user.click(screen.getByRole("button", { name: "保存设置" }));

    expect(onSave).toHaveBeenCalledTimes(1);
    const [inspection, updates, confirmDelete, confirmDeleteInvalid, confirmUpdate] = onSave.mock.calls[0];
    expect(inspection).toMatchObject({
      enabled: true,
      model_probe_full_sweep: true,
      auto_disable: true,
      auto_enable: true,
      passive_circuit_enabled: true,
      passive_failure_threshold: 5,
      auto_delete: true,
      auto_delete_invalid_credentials: true,
      anomaly_trigger_enabled: true,
      anomaly_threshold_percent: 50,
      anomaly_minimum_accounts: 10,
      anomaly_cooldown_minutes: 60,
    });
    expect(inspection.scan_manually_disabled).toBe(true);
    expect(inspection.model_probe_models).toEqual({ codex: "gpt-5.4", openai: "gpt-5.4", claude: "claude-sonnet-4-5-20250929", gemini: "gemini-2.0-flash", xai: "grok-4" });
    expect(updates).toMatchObject({ check_enabled: true, auto_update: true });
    expect(confirmDelete).toBe(true);
    expect(confirmDeleteInvalid).toBe(true);
    expect(confirmUpdate).toBe(true);
  });
});

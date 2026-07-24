import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { AutomationSettingsDialog } from "./AutomationSettingsDialog";

describe("AutomationSettingsDialog", () => {
  it("requires explicit confirmation before enabling destructive inspection automation", async () => {
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
          anomaly_notification_enabled: false, anomaly_notification_only: false, anomaly_notification_url: "",
          notification_available_accounts_enabled: false, notification_available_accounts_threshold: 10,
          notification_availability_percent_enabled: false, notification_availability_percent_threshold: 20, notification_cooldown_minutes: 60,
        }}
        saving={false}
        onClose={() => undefined}
        onSave={onSave}
      />,
    );

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
    expect(confirmDelete).toBe(true);
    expect(confirmDeleteInvalid).toBe(true);
  });

  it("inserts placeholder-only notification content at the cursor without adding query fields", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn();
    render(
      <AutomationSettingsDialog
        inspection={{
          enabled: true, scan_interval_minutes: 30,
          model_probe_enabled: true, model_probe_full_sweep: false, scan_manually_disabled: false, model_probe_interval_minutes: 60, model_probe_batch_size: 20,
          model_probe_models: { codex: "gpt-5.4", openai: "gpt-5.4", claude: "claude-sonnet-4-5-20250929", gemini: "gemini-2.0-flash", xai: "grok-4" },
          failure_threshold: 3, recovery_threshold: 2, auto_disable: false, auto_enable: false,
          passive_circuit_enabled: false, passive_failure_threshold: 5, passive_failure_window_minutes: 180, passive_circuit_minutes: 15,
          auto_delete: false, auto_delete_invalid_credentials: false, delete_grace_hours: 168, delete_batch_size: 10,
          anomaly_trigger_enabled: true, anomaly_threshold_percent: 50, anomaly_minimum_accounts: 10, anomaly_cooldown_minutes: 60,
          anomaly_notification_enabled: false, anomaly_notification_only: false, anomaly_notification_url: "",
          notification_available_accounts_enabled: false, notification_available_accounts_threshold: 10,
          notification_availability_percent_enabled: false, notification_availability_percent_threshold: 20, notification_cooldown_minutes: 60,
        }}
        saving={false}
        onClose={() => undefined}
        onSave={onSave}
      />,
    );

    await user.click(screen.getByLabelText("异常占比达到阈值时通知"));
    await user.click(screen.getByLabelText("仅发送通知，不触发二次巡检"));
    await user.click(screen.getByLabelText("可用账号数不足时通知"));
    await user.click(screen.getByLabelText("总可用率不足时通知"));
    const urlInput = screen.getByLabelText("通知 URL 模板");
    await user.type(urlInput, "https://notify.example/hook?message=可用账号剩余");
    const parameterSelect = screen.getByLabelText("插入通知参数");
    await user.selectOptions(parameterSelect, "available_accounts");
    await user.selectOptions(parameterSelect, "__notification_details__");
    const template = String((urlInput as HTMLInputElement).value);
    expect(template).toContain("可用账号剩余${available_accounts}");
    expect(template).toContain("事件 ${event}");
    expect(template).toContain("可用率 ${available_percent}%");
    expect(template).not.toContain("&event=");
    expect(template).not.toContain("available_accounts=${available_accounts}");

    await user.click(screen.getByRole("button", { name: "保存设置" }));
    expect(onSave).toHaveBeenCalledWith(expect.objectContaining({
      anomaly_notification_enabled: true,
      anomaly_notification_only: true,
      anomaly_notification_url: template,
      notification_available_accounts_enabled: true,
      notification_available_accounts_threshold: 10,
      notification_availability_percent_enabled: true,
      notification_availability_percent_threshold: 20,
      notification_cooldown_minutes: 60,
    }), false, false);
  });

  it("previews and sends current unsaved values while caching multiple scenarios", async () => {
    const user = userEvent.setup();
    const onPreview = vi.fn(async (request) => ({
      scenario: request.scenario,
      event: request.scenario,
      expanded_url: `https://notify.example/publish?message=${request.scenario}:${request.available_accounts_threshold}`,
      variables: {
        event: request.scenario, total_accounts: "122", eligible_accounts: "71", available_accounts: "10", available_percent: "8",
        abnormal_accounts: "60", abnormal_percent: "84", quota_limited_accounts: "60", invalid_credential_accounts: "0",
        deactivated_accounts: "0", unavailable_accounts: "0", disabled_accounts: "0", threshold_percent: String(request.threshold_percent),
        available_accounts_threshold: String(request.available_accounts_threshold), availability_percent_threshold: String(request.availability_percent_threshold),
        triggered_at: "2026-07-24T08:00:00Z",
      },
      triggered_at: "2026-07-24T08:00:00Z",
    }));
    const onTest = vi.fn(async (request) => ({
      preview: await onPreview(request), delivered: true, status_code: 204, attempts: 1, reason_code: "notification_delivered",
    }));
    render(
      <AutomationSettingsDialog
        inspection={{
          enabled: true, scan_interval_minutes: 30,
          model_probe_enabled: false, model_probe_full_sweep: false, scan_manually_disabled: false, model_probe_interval_minutes: 60, model_probe_batch_size: 20,
          model_probe_models: { codex: "gpt-5.6-sol", openai: "gpt-5.6-sol", claude: "claude-sonnet-4-5-20250929", gemini: "gemini-2.0-flash", xai: "grok-4" },
          failure_threshold: 3, recovery_threshold: 2, auto_disable: true, auto_enable: false,
          passive_circuit_enabled: false, passive_failure_threshold: 5, passive_failure_window_minutes: 180, passive_circuit_minutes: 15,
          auto_delete: false, auto_delete_invalid_credentials: false, delete_grace_hours: 168, delete_batch_size: 10,
          anomaly_trigger_enabled: true, anomaly_threshold_percent: 50, anomaly_minimum_accounts: 10, anomaly_cooldown_minutes: 60,
          anomaly_notification_enabled: true, anomaly_notification_only: true,
          anomaly_notification_url: "https://notify.example/publish?message=可用${available_accounts}，可用率${available_percent}",
          notification_available_accounts_enabled: true, notification_available_accounts_threshold: 10,
          notification_availability_percent_enabled: true, notification_availability_percent_threshold: 20, notification_cooldown_minutes: 60,
        }}
        saving={false}
        onClose={() => undefined}
        onSave={() => undefined}
        onNotificationPreview={onPreview}
        onNotificationTest={onTest}
      />,
    );

    await user.clear(screen.getByLabelText("可用账号数低于"));
    await user.type(screen.getByLabelText("可用账号数低于"), "3");
    await user.click(screen.getByRole("button", { name: "预览" }));
    expect(onPreview).toHaveBeenCalledWith(expect.objectContaining({
      scenario: "manual_test", available_accounts_threshold: 3,
      url_template: "https://notify.example/publish?message=可用${available_accounts}，可用率${available_percent}",
    }));
    expect(screen.getByText("https://notify.example/publish?message=manual_test:3")).toBeInTheDocument();
    expect(screen.getByText("8%", { selector: "dd" })).toBeInTheDocument();
    expect(screen.getByText("84%", { selector: "dd" })).toBeInTheDocument();
    expect(screen.getByText("50%", { selector: "dd" })).toBeInTheDocument();
    expect(screen.getByText("20%", { selector: "dd" })).toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: "可用账号数" }));
    await user.click(screen.getByRole("button", { name: "发送测试" }));
    expect(onTest).toHaveBeenCalledWith(expect.objectContaining({ scenario: "available_accounts_low", available_accounts_threshold: 3 }));
    expect(await screen.findByText("外部通知发送成功")).toBeInTheDocument();
    expect(screen.getByText("204")).toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: /手动测试/ }));
    expect(screen.getByText("https://notify.example/publish?message=manual_test:3")).toBeInTheDocument();
    expect(onPreview).toHaveBeenCalledTimes(2);
  });

  it("enables scheduled inspection when low-availability notification is enabled independently", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn();
    render(
      <AutomationSettingsDialog
        inspection={{
          enabled: false, scan_interval_minutes: 30,
          model_probe_enabled: false, model_probe_full_sweep: false, scan_manually_disabled: false, model_probe_interval_minutes: 60, model_probe_batch_size: 20,
          model_probe_models: { codex: "gpt-5.4", openai: "gpt-5.4", claude: "claude-sonnet-4-5-20250929", gemini: "gemini-2.0-flash", xai: "grok-4" },
          failure_threshold: 3, recovery_threshold: 2, auto_disable: false, auto_enable: false,
          passive_circuit_enabled: false, passive_failure_threshold: 5, passive_failure_window_minutes: 180, passive_circuit_minutes: 15,
          auto_delete: false, auto_delete_invalid_credentials: false, delete_grace_hours: 168, delete_batch_size: 10,
          anomaly_trigger_enabled: false, anomaly_threshold_percent: 50, anomaly_minimum_accounts: 10, anomaly_cooldown_minutes: 60,
          anomaly_notification_enabled: false, anomaly_notification_only: false, anomaly_notification_url: "",
          notification_available_accounts_enabled: false, notification_available_accounts_threshold: 10,
          notification_availability_percent_enabled: false, notification_availability_percent_threshold: 20, notification_cooldown_minutes: 60,
        }}
        saving={false}
        onClose={() => undefined}
        onSave={onSave}
      />,
    );

    await user.click(screen.getByLabelText("可用账号数不足时通知"));
    await user.type(screen.getByLabelText("通知 URL 模板"), "https://notify.example/hook?available=${available_accounts}");
    await user.click(screen.getByRole("button", { name: "保存设置" }));

    expect(onSave).toHaveBeenCalledWith(expect.objectContaining({
      enabled: true,
      anomaly_notification_enabled: false,
      notification_available_accounts_enabled: true,
      notification_availability_percent_enabled: false,
    }), false, false);
  });

  it("enables scheduled inspection when automatic account disposition is enabled", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn();
    render(
      <AutomationSettingsDialog
        inspection={{
          enabled: false, scan_interval_minutes: 30,
          model_probe_enabled: false, model_probe_full_sweep: false, scan_manually_disabled: false, model_probe_interval_minutes: 60, model_probe_batch_size: 20,
          model_probe_models: { codex: "gpt-5.4", openai: "gpt-5.4", claude: "claude-sonnet-4-5-20250929", gemini: "gemini-2.0-flash", xai: "grok-4" },
          failure_threshold: 3, recovery_threshold: 2, auto_disable: false, auto_enable: false,
          passive_circuit_enabled: false, passive_failure_threshold: 5, passive_failure_window_minutes: 180, passive_circuit_minutes: 15,
          auto_delete: false, auto_delete_invalid_credentials: false, delete_grace_hours: 168, delete_batch_size: 10,
          anomaly_trigger_enabled: false, anomaly_threshold_percent: 50, anomaly_minimum_accounts: 10, anomaly_cooldown_minutes: 60,
          anomaly_notification_enabled: false, anomaly_notification_only: false, anomaly_notification_url: "",
          notification_available_accounts_enabled: false, notification_available_accounts_threshold: 10,
          notification_availability_percent_enabled: false, notification_availability_percent_threshold: 20, notification_cooldown_minutes: 60,
        }}
        saving={false}
        onClose={() => undefined}
        onSave={onSave}
      />,
    );

    await user.click(screen.getByLabelText("自动禁用"));
    await user.click(screen.getByRole("button", { name: "保存设置" }));

    expect(onSave).toHaveBeenCalledWith(expect.objectContaining({
      enabled: true,
      auto_disable: true,
    }), false, false);
  });
});

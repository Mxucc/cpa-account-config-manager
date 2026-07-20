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
        inspection={{ enabled: false, scan_interval_minutes: 30, failure_threshold: 3, recovery_threshold: 2, auto_disable: false, auto_enable: false, auto_delete: false, delete_grace_hours: 168, delete_batch_size: 10 }}
        updates={{ check_enabled: true, check_interval_hours: 24, auto_update: false }}
        saving={false}
        onClose={() => undefined}
        onSave={onSave}
      />,
    );

    await user.click(screen.getByLabelText("自动删除"));
    expect(screen.getByLabelText("自动禁用")).toBeChecked();
    await user.click(screen.getByLabelText("自动更新"));
    await user.click(screen.getByRole("button", { name: "保存设置" }));
    expect(screen.getByRole("alert")).toHaveTextContent("确认风险");
    expect(onSave).not.toHaveBeenCalled();

    await user.click(screen.getByLabelText("确认开启自动删除"));
    await user.click(screen.getByRole("button", { name: "保存设置" }));
    expect(screen.getByRole("alert")).toHaveTextContent("确认风险");
    await user.click(screen.getByLabelText("确认开启自动更新"));
    await user.click(screen.getByRole("button", { name: "保存设置" }));

    expect(onSave).toHaveBeenCalledTimes(1);
    const [inspection, updates, confirmDelete, confirmUpdate] = onSave.mock.calls[0];
    expect(inspection).toMatchObject({ auto_disable: true, auto_delete: true });
    expect(updates).toMatchObject({ check_enabled: true, auto_update: true });
    expect(confirmDelete).toBe(true);
    expect(confirmUpdate).toBe(true);
  });
});


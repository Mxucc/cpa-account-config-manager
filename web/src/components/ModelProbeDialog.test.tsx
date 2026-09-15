import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ModelProbeDialog } from "./ModelProbeDialog";

const targets = [
  { id: "account:one", label: "codex-one.json" },
  { id: "channel:0", label: "https://example.test/v1 · AI 供应商渠道" },
];

function renderDialog(overrides: Partial<Parameters<typeof ModelProbeDialog>[0]> = {}) {
  const onRun = vi.fn();
  const onSelectTarget = vi.fn();
  render(
    <ModelProbeDialog
      model="gpt-5.4-codex"
      targets={targets}
      targetID="account:one"
      onSelectTarget={onSelectTarget}
      onRun={onRun}
      onClose={() => undefined}
      testing={false}
      {...overrides}
    />,
  );
  return { onRun, onSelectTarget };
}

describe("ModelProbeDialog", () => {
  beforeEach(() => cleanup());

  it("opens on the target the caller selected and can be run without touching the picker", async () => {
    const user = userEvent.setup();
    const { onRun } = renderDialog();

    expect(screen.getByRole("combobox", { name: "测试目标" })).toHaveValue("account:one");
    const start = screen.getByRole("button", { name: "开始测试" });
    expect(start).toBeEnabled();
    await user.click(start);
    expect(onRun).toHaveBeenCalledTimes(1);
  });

  // The reported case: a select whose value matches no option still displays the first one, so the
  // dialog looked ready while the start button stayed disabled until the target was re-picked.
  it("never claims a target the caller did not select", async () => {
    const user = userEvent.setup();
    const { onRun, onSelectTarget } = renderDialog({ targetID: "" });

    const picker = screen.getByRole("combobox", { name: "测试目标" });
    expect(picker).toHaveValue("");
    expect(within(picker).getByRole("option", { name: "请选择凭据" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "开始测试" })).toBeDisabled();

    // Picking a target is what makes the dialog runnable, and it runs the picked one.
    await user.selectOptions(picker, "channel:0");
    expect(onSelectTarget).toHaveBeenCalledWith("channel:0");
    expect(onRun).not.toHaveBeenCalled();
  });

  // The caller owns targetID, so a list that changed under the dialog can leave that id dangling.
  it("refuses a target that is no longer offered instead of running another one", () => {
    const { onRun } = renderDialog({ targetID: "account:gone" });

    const picker = screen.getByRole("combobox", { name: "测试目标" });
    expect(picker).toHaveValue("");
    expect(within(picker).getByRole("option", { name: "请选择凭据" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "开始测试" })).toBeDisabled();
    expect(onRun).not.toHaveBeenCalled();
  });

  it("does not claim there is no credential while the targets are still loading", () => {
    renderDialog({ targets: [], targetID: "", testing: true });
    expect(screen.queryByText(/没有可用于测试的凭据/)).not.toBeInTheDocument();
  });

  it("reports a finished load that resolved no credential at all", () => {
    renderDialog({ targets: [], targetID: "", testing: false });
    expect(screen.getByText(/没有可用于测试的凭据/)).toBeInTheDocument();
  });
});

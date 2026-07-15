import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { PolicySnapshot } from "../types";
import { PolicyDialog } from "./PolicyDialog";

const snapshot: PolicySnapshot = {
  policy: {
    enabled: true,
    apply_mode: "missing",
    scan_interval_seconds: 15,
    priority: null,
    websockets: false,
  },
  running: false,
  last_scan: {
    finished_at: "2026-07-15T10:00:00Z",
    scanned: 8,
    eligible: 7,
    changed: 2,
    skipped: 5,
    failed: 1,
  },
};

describe("PolicyDialog", () => {
  it("preserves unmanaged null and an explicit false value", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn();
    render(<PolicyDialog snapshot={snapshot} saving={false} scanning={false} forceLoading={false} onClose={vi.fn()} onSave={onSave} onScan={vi.fn()} onForcePreview={vi.fn()} />);

    expect(screen.getByText("8")).toBeInTheDocument();
    expect(screen.getByLabelText("默认 WebSockets")).not.toBeChecked();
    await user.click(screen.getByRole("button", { name: "保存策略" }));

    expect(onSave).toHaveBeenCalledWith({
      enabled: true,
      apply_mode: "missing",
      scan_interval_seconds: 15,
      priority: null,
      websockets: false,
    });
  });

  it("submits Priority zero and validates the bounded scan interval", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn();
    render(<PolicyDialog snapshot={snapshot} saving={false} scanning={false} forceLoading={false} onClose={vi.fn()} onSave={onSave} onScan={vi.fn()} onForcePreview={vi.fn()} />);

    await user.click(screen.getByRole("checkbox", { name: "Priority" }));
    const priority = screen.getByLabelText("默认 Priority");
    await user.clear(priority);
    await user.type(priority, "0");
    const interval = screen.getByLabelText("扫描间隔");
    await user.clear(interval);
    await user.type(interval, "2");
    await user.click(screen.getByRole("button", { name: "保存策略" }));
    expect(screen.getByRole("alert")).toHaveTextContent("5 到 300 秒");
    expect(onSave).not.toHaveBeenCalled();

    await user.clear(interval);
    await user.type(interval, "5");
    await user.click(screen.getByRole("button", { name: "保存策略" }));
    expect(onSave).toHaveBeenCalledWith(expect.objectContaining({ priority: 0, websockets: false, scan_interval_seconds: 5 }));
  });

  it("requires unsaved edits to be persisted before scan or force actions", async () => {
    const user = userEvent.setup();
    render(<PolicyDialog snapshot={snapshot} saving={false} scanning={false} forceLoading={false} onClose={vi.fn()} onSave={vi.fn()} onScan={vi.fn()} onForcePreview={vi.fn()} />);

    expect(screen.getByRole("button", { name: "立即扫描" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "强制同步" })).toBeEnabled();
    await user.click(screen.getByRole("checkbox", { name: "Priority" }));
    expect(screen.getByRole("button", { name: "立即扫描" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "强制同步" })).toBeDisabled();
  });

  it("translates persisted scan errors for operators", () => {
    render(<PolicyDialog snapshot={{ ...snapshot, last_scan: { ...snapshot.last_scan, error: "stored default policy could not be loaded" } }} saving={false} scanning={false} forceLoading={false} onClose={vi.fn()} onSave={vi.fn()} onScan={vi.fn()} onForcePreview={vi.fn()} />);

    expect(screen.getByRole("alert")).toHaveTextContent("已保存的默认策略无法读取");
  });
});

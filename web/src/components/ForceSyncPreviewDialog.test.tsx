import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { ForceSyncPreview } from "../types";
import { ForceSyncPreviewDialog } from "./ForceSyncPreviewDialog";

describe("ForceSyncPreviewDialog", () => {
  it("shows exact managed values and requires the explicit overwrite command", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    const preview: ForceSyncPreview = {
      id: "force-preview-1",
      created_at: "2026-07-15T10:00:00Z",
      expires_at: "2026-07-15T10:05:00Z",
      total: 2,
      eligible: 1,
      read_only: 1,
      physical_files: 1,
      policy: { fields: ["priority", "websockets"], priority: 0, websockets: false },
      targets: [
        { id: "a", name: "a.json", provider: "codex", label: "a@example.com", eligible: true },
        { id: "runtime", name: "runtime.json", provider: "codex", eligible: false, read_only_reason: "runtime-only account has no physical auth file" },
      ],
    };
    render(<ForceSyncPreviewDialog preview={preview} starting={false} onClose={vi.fn()} onConfirm={onConfirm} />);

    expect(screen.getByText("将覆盖现有字段值")).toBeInTheDocument();
    expect(screen.getByText("0")).toBeInTheDocument();
    expect(screen.getByText("OFF")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "覆盖 1 个文件" }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });
});

import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { PreviewDialog } from "./PreviewDialog";

describe("PreviewDialog", () => {
  it("renders structured preview warnings in Chinese", () => {
    render(
      <PreviewDialog
        preview={{
          operation: "patch",
          id: "preview-1",
          created_at: "2026-07-15T00:00:00Z",
          expires_at: "2026-07-15T00:05:00Z",
          scope_mode: "filtered",
          total: 3,
          eligible: 1,
          read_only: 1,
          missing: 1,
          physical_files: 1,
          providers: { codex: 2, claude: 1 },
          patch: { fields: ["note"], proxy_mutation: false },
          warnings: ["server warning text"],
          targets: [],
        }}
        starting={false}
        onClose={vi.fn()}
        onConfirm={vi.fn()}
      />,
    );

    expect(screen.getByText("1 个目标为只读，将自动跳过")).toBeInTheDocument();
    expect(screen.getByText("1 个已选目标已不存在，将自动跳过")).toBeInTheDocument();
    expect(screen.getByText("目标包含多个 Provider，请确认字段适用于全部账号")).toBeInTheDocument();
    expect(screen.queryByText("server warning text")).not.toBeInTheDocument();
  });

  it("renders an explicit destructive confirmation without requiring a username", () => {
    const onConfirm = vi.fn();
    render(
      <PreviewDialog
        preview={{
          operation: "delete",
          id: "delete-preview-1",
          created_at: "2026-07-23T00:00:00Z",
          expires_at: "2026-07-23T00:05:00Z",
          scope_mode: "selected",
          total: 3,
          eligible: 2,
          read_only: 1,
          missing: 0,
          physical_files: 2,
          providers: { codex: 3 },
          patch: { fields: [], proxy_mutation: false },
          targets: [
            { id: "auth-1", label: "first@example.com", provider: "codex", eligible: true },
            { id: "auth-2", label: "second@example.com", provider: "codex", eligible: true },
            { id: "auth-3", label: "readonly@example.com", provider: "codex", eligible: false, read_only_reason: "read-only" },
          ],
        }}
        starting={false}
        onClose={vi.fn()}
        onConfirm={onConfirm}
      />,
    );

    expect(screen.getByRole("dialog", { name: "批量删除预览" })).toBeInTheDocument();
    expect(screen.getByText("删除后无法通过插件恢复这些 Auth 文件，请确认目标范围后再继续。")).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    screen.getByRole("button", { name: "删除 2 个账号" }).click();
    expect(onConfirm).toHaveBeenCalledOnce();
  });
});

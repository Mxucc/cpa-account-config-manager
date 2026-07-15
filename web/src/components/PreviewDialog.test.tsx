import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { PreviewDialog } from "./PreviewDialog";

describe("PreviewDialog", () => {
  it("renders structured preview warnings in Chinese", () => {
    render(
      <PreviewDialog
        preview={{
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
});

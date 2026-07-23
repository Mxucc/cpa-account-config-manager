import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { JobPanel } from "./JobPanel";

describe("JobPanel", () => {
  it("keeps refresh and close controls in the result action toolbar", () => {
    const { container } = render(
      <JobPanel
        job={{
          id: "batch-job-1",
          state: "completed",
          running: false,
          total: 11,
          done: 11,
          succeeded: 11,
          failed: 0,
          conflicts: 0,
          skipped: 0,
          retry_available: false,
          results: [],
        }}
        onClose={vi.fn()}
        onRetry={vi.fn()}
        onExport={vi.fn()}
        onRefresh={vi.fn()}
      />,
    );

    const toolbar = screen.getByRole("button", { name: "导出结果" }).closest(".job-toolbar");
    expect(toolbar).not.toBeNull();
    expect(toolbar).toContainElement(screen.getByRole("button", { name: "仅重试失败项" }));
    expect(toolbar).toContainElement(screen.getByRole("button", { name: "刷新任务" }));
    expect(toolbar).toContainElement(screen.getByRole("button", { name: "关闭任务面板" }));
    expect(container.querySelector(".job-header")).not.toContainElement(screen.getByRole("button", { name: "关闭任务面板" }));
  });

  it("labels successful batch-delete results as deleted", () => {
    render(
      <JobPanel
        job={{
          id: "delete-job-1",
          operation: "delete",
          state: "completed",
          running: false,
          total: 1,
          done: 1,
          succeeded: 1,
          failed: 0,
          conflicts: 0,
          skipped: 0,
          retry_available: false,
          results: [{ id: "auth-1", label: "operator@example.com", status: "succeeded", applied_fields: [], retryable: false }],
        }}
        title="ui.batch_delete_job"
        ariaLabel="ui.batch_delete_job"
        onClose={vi.fn()}
        onRetry={vi.fn()}
        onExport={vi.fn()}
        onRefresh={vi.fn()}
      />,
    );

    expect(screen.getByRole("complementary", { name: "批量删除任务" })).toBeInTheDocument();
    expect(screen.getByText("已删除")).toBeInTheDocument();
  });
});

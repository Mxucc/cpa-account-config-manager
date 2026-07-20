import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api/client";
import type { OperationListResponse } from "../types";
import { OperationLogWorkspace } from "./OperationLogWorkspace";

const operationResponse: OperationListResponse = {
  operations: [{
    id: "operation-1",
    category: "batch",
    action: "batch_edit",
    status: "partial",
    source: "manual",
    scope: "selected",
    target_id: "<img src=x onerror=alert(1)>",
    target_count: 3,
    succeeded: 2,
    failed: 1,
    skipped: 0,
    started_at: "2026-07-20T08:00:00Z",
    finished_at: "2026-07-20T08:01:00Z",
    reason_code: "partial_failure",
    related_job_id: "job-1",
  }],
  summary: { total: 1, running: 0, succeeded: 0, failed: 0, attention: 1, interrupted: 0 },
  total: 1,
  page: 1,
  page_size: 50,
  pages: 1,
};

describe("OperationLogWorkspace", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.spyOn(api, "listOperations").mockResolvedValue(operationResponse);
    vi.spyOn(api, "downloadOperationExport").mockResolvedValue({ filename: "operations.csv", exported: 1 });
    vi.spyOn(api, "clearOperations").mockResolvedValue({ operation: { ...operationResponse.operations[0], id: "clear-1", category: "journal", action: "journal_clear", status: "succeeded" }, retained: 1 });
  });

  it("filters, inspects, and opens a currently available related job", async () => {
    const user = userEvent.setup();
    const onOpenRelatedJob = vi.fn();
    render(<OperationLogWorkspace activeJobIDs={["job-1"]} onAPIError={() => undefined} onNotice={() => undefined} onOpenRelatedJob={onOpenRelatedJob} />);

    expect(await screen.findByText("批量修改")).toBeInTheDocument();
    expect(screen.getByText("<img src=x onerror=alert(1)>")).toBeInTheDocument();
    expect(screen.queryByRole("img")).not.toBeInTheDocument();

    await user.selectOptions(screen.getByRole("combobox", { name: "操作类别" }), "batch");
    await waitFor(() => expect(api.listOperations).toHaveBeenLastCalledWith(1, 50, expect.objectContaining({ category: "batch" })));

    await user.click(screen.getByRole("button", { name: "查看操作详情" }));
    const details = screen.getByRole("dialog", { name: "操作详情" });
    expect(details).toBeInTheDocument();
    expect(screen.getByText("部分操作失败")).toBeInTheDocument();
    expect(screen.queryByText("partial_failure")).not.toBeInTheDocument();
    await user.click(within(details).getByRole("button", { name: "打开关联任务" }));
    expect(onOpenRelatedJob).toHaveBeenCalledWith(operationResponse.operations[0]);
  });

  it("shows model-test actions and reasons in Chinese while preserving the technical model ID", async () => {
    vi.mocked(api.listOperations).mockResolvedValue({
      ...operationResponse,
      operations: [{
        ...operationResponse.operations[0],
        id: "model-test-1",
        category: "account",
        action: "model_test",
        status: "succeeded",
        scope: "single",
        model: "gpt-5.4",
        reason_code: "model_response_ok",
      }],
      summary: { total: 1, running: 0, succeeded: 1, failed: 0, attention: 0, interrupted: 0 },
    });
    const user = userEvent.setup();
    render(<OperationLogWorkspace activeJobIDs={[]} onAPIError={() => undefined} onNotice={() => undefined} onOpenRelatedJob={() => undefined} />);

    expect(await screen.findByText("模型可用性测试")).toBeInTheDocument();
    expect(screen.getByText(/gpt-5\.4/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "查看操作详情" }));
    const details = screen.getByRole("dialog", { name: "操作详情" });
    expect(within(details).getByText("模型响应正常")).toBeInTheDocument();
    expect(within(details).queryByText("model_response_ok")).not.toBeInTheDocument();
  });

  it("exports the filtered journal and requires confirmation before clearing", async () => {
    const user = userEvent.setup();
    const onNotice = vi.fn();
    render(<OperationLogWorkspace activeJobIDs={[]} onAPIError={() => undefined} onNotice={onNotice} onOpenRelatedJob={() => undefined} />);
    await screen.findByText("批量修改");

    await user.click(screen.getByRole("button", { name: "导出" }));
    expect(screen.getByRole("dialog", { name: "导出操作日志" })).toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: "CSV 表格 .csv" }));
    await user.click(screen.getByRole("button", { name: "导出 CSV" }));
    await waitFor(() => expect(api.downloadOperationExport).toHaveBeenCalledWith("csv", expect.any(Object)));
    expect(onNotice).toHaveBeenCalledWith(expect.stringContaining("operations.csv"));

    await user.click(screen.getByRole("button", { name: "清理操作日志" }));
    const confirm = screen.getByRole("button", { name: "确认清理" });
    expect(confirm).toBeDisabled();
    await user.click(screen.getByRole("checkbox", { name: /清除当前 1 条操作记录/ }));
    expect(confirm).toBeEnabled();
    await user.click(confirm);
    await waitFor(() => expect(api.clearOperations).toHaveBeenCalledTimes(1));
    expect(onNotice).toHaveBeenCalledWith(expect.stringContaining("保留本次清理记录"));
  });
});

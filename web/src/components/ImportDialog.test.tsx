import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { ImportPreview, ImportResult } from "../types";
import { ImportDialog } from "./ImportDialog";

describe("ImportDialog", () => {
  it("selects mixed JSON and ZIP files together and previews them as one batch", async () => {
    const user = userEvent.setup();
    const onPreview = vi.fn();
    render(
      <ImportDialog
        preview={null}
        result={null}
        previewing={false}
        importing={false}
        onClose={() => undefined}
        onPreview={onPreview}
        onImport={() => undefined}
        onReset={() => undefined}
      />,
    );
    const jsonFile = new File([`{"email":"one@example.com"}`], "one.json", { type: "application/json" });
    const zipFile = new File(["PK\u0003\u0004archive"], "accounts.zip", { type: "application/zip" });

    const textFile = new File([`{"email":"text@example.com"}\n{"email":"next@example.com"}`], "accounts.jsonl", { type: "application/x-ndjson" });

    await user.upload(screen.getByLabelText("选择 JSON、文本 JSON 或 ZIP 文件"), [jsonFile, textFile, zipFile]);
    expect(screen.getByText("one.json")).toBeInTheDocument();
    expect(screen.getByText("accounts.jsonl")).toBeInTheDocument();
    expect(screen.getByText(/JSONL/)).toBeInTheDocument();
    expect(screen.getByText("accounts.zip")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "生成预览" }));

    expect(onPreview).toHaveBeenCalledTimes(1);
    const selected = onPreview.mock.calls[0][0] as File[];
    expect(selected.map((file) => file.name)).toEqual(["one.json", "accounts.jsonl", "accounts.zip"]);
  });

  it("bounds rendered preview rows while preserving the full account total", () => {
    const preview: ImportPreview = {
      id: "preview-large",
      created_at: "2026-07-15T00:00:00Z",
      expires_at: "2026-07-15T00:05:00Z",
      input_type: "json",
      source_files: 1,
      total: 251,
      skipped: 0,
      items: Array.from({ length: 251 }, (_, index) => ({
        index: index + 1,
        source_name: "large.json",
        source_path: `$.accounts[${index}]`,
        target_name: `codex-user-${index + 1}.json`,
        email: `user-${index + 1}@example.com`,
        account_id: `account-${index + 1}`,
        label: `user-${index + 1}@example.com`,
        synthetic_id_token: false,
		credential_type: index === 0 ? "agent_identity" as const : undefined,
      })),
    };
    render(
      <ImportDialog
        preview={preview}
        result={null}
        previewing={false}
        importing={false}
        onClose={() => undefined}
        onPreview={() => undefined}
        onImport={() => undefined}
        onReset={() => undefined}
      />,
    );
    expect(screen.getByText("user-250@example.com")).toBeInTheDocument();
    expect(screen.getByText("Agent Identity")).toBeInTheDocument();
    expect(screen.queryByText("user-251@example.com")).not.toBeInTheDocument();
    expect(screen.getByText("另有 1 个账号未展开")).toBeInTheDocument();
  });

  it("bounds rendered result rows while preserving the full import total", () => {
    const result: ImportResult = {
      id: "result-large",
      state: "completed",
      total: 251,
      imported: 251,
      skipped: 0,
      failed: 0,
      started_at: "2026-07-15T00:00:00Z",
      finished_at: "2026-07-15T00:01:00Z",
      results: Array.from({ length: 251 }, (_, index) => ({
        index: index + 1,
        source_name: "large.json",
        target_name: `codex-user-${index + 1}.json`,
        email: `user-${index + 1}@example.com`,
        account_id: `account-${index + 1}`,
        label: `user-${index + 1}@example.com`,
        status: "imported",
      })),
    };
    render(
      <ImportDialog
        preview={null}
        result={result}
        previewing={false}
        importing={false}
        onClose={() => undefined}
        onPreview={() => undefined}
        onImport={() => undefined}
        onReset={() => undefined}
      />,
    );
    expect(screen.getByText("codex-user-250.json")).toBeInTheDocument();
    expect(screen.queryByText("codex-user-251.json")).not.toBeInTheDocument();
    expect(screen.getByText("另有 1 个账号未展开")).toBeInTheDocument();
  });
});

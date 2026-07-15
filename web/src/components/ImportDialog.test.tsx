import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
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
});

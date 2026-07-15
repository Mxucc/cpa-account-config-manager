import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { ExportDialog } from "./ExportDialog";

it("selects a target credential format for account downloads", async () => {
  const user = userEvent.setup();
  const onExport = vi.fn();
  render(<ExportDialog kind="accounts" count={36} exporting={false} onClose={() => undefined} onExport={onExport} />);

  expect(screen.getByRole("dialog", { name: "下载账号凭据" })).toBeInTheDocument();
  expect(screen.getByText("包含凭据")).toBeInTheDocument();
  expect(screen.getByRole("radio", { name: "CPA 多账号 ZIP .json / .zip" })).toBeChecked();
  expect(screen.queryByRole("radio", { name: /CSV/ })).not.toBeInTheDocument();

  const formats = [
    ["CPA 多账号 ZIP .json / .zip", "下载 CPA", "cpa"],
    ["sub2api 批量账号 .json", "下载 sub2api", "sub2api"],
    ["Cockpit Codex 凭据 .json", "下载 Cockpit", "cockpit"],
    ["9router OAuth 账号 .json", "下载 9router", "9router"],
    ["Codex 原生格式 auth.json", "下载 Codex", "codex"],
    ["AxonHub Codex Auth .json", "下载 AxonHub", "axonhub"],
    ["Codex-Manager 批量导入 .json", "下载 Codex-Manager", "codexmanager"],
  ] as const;
  for (const [radioName, buttonName] of formats) {
    const radio = screen.getByRole("radio", { name: radioName });
    expect(radio).toHaveAttribute("type", "radio");
    await user.click(radio);
    expect(radio).toBeChecked();
    await user.click(screen.getByRole("button", { name: buttonName }));
  }

  expect(onExport.mock.calls.map(([format]) => format)).toEqual(formats.map(([, , format]) => format));
});

it("keeps sanitized report formats for result downloads", async () => {
  const user = userEvent.setup();
  const onExport = vi.fn();
  render(<ExportDialog kind="results" count={12} exporting={false} onClose={() => undefined} onExport={onExport} />);

  expect(screen.getByText("脱敏")).toBeInTheDocument();
  expect(screen.queryByRole("radio", { name: /CPA/ })).not.toBeInTheDocument();
  const formats = [
    ["JSON 结构化 .json", "下载 JSON", "json"],
    ["CSV 表格 .csv", "下载 CSV", "csv"],
    ["JSON Lines 逐行 .jsonl", "下载 JSON Lines", "jsonl"],
  ] as const;
  for (const [radioName, buttonName] of formats) {
    const radio = screen.getByRole("radio", { name: radioName });
    await user.click(radio);
    expect(radio).toBeChecked();
    await user.click(screen.getByRole("button", { name: buttonName }));
  }

  expect(onExport.mock.calls.map(([format]) => format)).toEqual(formats.map(([, , format]) => format));
});

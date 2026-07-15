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
  expect(screen.getByRole("radio", { name: /CPA/ })).toBeChecked();
  expect(screen.queryByRole("radio", { name: /CSV/ })).not.toBeInTheDocument();
  await user.click(screen.getByRole("radio", { name: /Codex-Manager/ }));
  await user.click(screen.getByRole("button", { name: "下载 Codex-Manager" }));

  expect(onExport).toHaveBeenCalledWith("codexmanager");
});

it("keeps sanitized report formats for result downloads", async () => {
  const user = userEvent.setup();
  const onExport = vi.fn();
  render(<ExportDialog kind="results" count={12} exporting={false} onClose={() => undefined} onExport={onExport} />);

  expect(screen.getByText("脱敏")).toBeInTheDocument();
  expect(screen.queryByRole("radio", { name: /CPA/ })).not.toBeInTheDocument();
  await user.click(screen.getByRole("radio", { name: /CSV/ }));
  await user.click(screen.getByRole("button", { name: "下载 CSV" }));

  expect(onExport).toHaveBeenCalledWith("csv");
});

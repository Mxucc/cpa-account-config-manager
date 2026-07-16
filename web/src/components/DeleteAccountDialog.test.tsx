import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import type { Account, AccountDeletePreview } from "../types";
import { DeleteAccountDialog } from "./DeleteAccountDialog";

const account: Account = {
  id: "auth-1",
  name: "operator.json",
  provider: "codex",
  label: "operator@example.com",
  status: "active",
  disabled: false,
  unavailable: false,
  runtime_only: false,
  source: "file",
  proxy_configured: false,
  header_count: 0,
  editable: true,
  success: 0,
  failed: 0,
};

const preview: AccountDeletePreview = {
  id: "delete-preview-1",
  created_at: "2026-07-15T10:00:00Z",
  expires_at: "2026-07-15T10:05:00Z",
  account: {
    id: "auth-1",
    name: "operator.json",
    provider: "codex",
    label: "operator@example.com",
  },
};

it("requires the exact file name before confirming deletion", async () => {
  const user = userEvent.setup();
  const onConfirm = vi.fn();
  render(
    <DeleteAccountDialog
      account={account}
      preview={preview}
      previewing={false}
      deleting={false}
      error=""
      onClose={() => undefined}
      onConfirm={onConfirm}
    />,
  );

  const deleteButton = screen.getByRole("button", { name: "删除账号" });
  expect(deleteButton).toBeDisabled();
  await user.type(screen.getByLabelText("确认删除文件名"), "operator");
  expect(deleteButton).toBeDisabled();
  await user.clear(screen.getByLabelText("确认删除文件名"));
  await user.type(screen.getByLabelText("确认删除文件名"), "operator.json");
  expect(deleteButton).toBeEnabled();
  await user.click(deleteButton);
  expect(onConfirm).toHaveBeenCalledTimes(1);
});

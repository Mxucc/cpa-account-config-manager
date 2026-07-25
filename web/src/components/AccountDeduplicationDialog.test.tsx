import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { I18nProvider } from "../i18n";
import { CPA_LANGUAGE_STORAGE_KEY } from "../i18n/locale";
import type { AccountDeduplicationPreview } from "../types";
import { AccountDeduplicationDialog } from "./AccountDeduplicationDialog";

const preview: AccountDeduplicationPreview = {
  scanned_credentials: 6,
  identified_credentials: 5,
  excluded_credentials: 0,
  duplicate_groups: 2,
  duplicate_credentials: 3,
  proposed_deletions: 2,
  read_only_skipped: 1,
  missing_identity: 1,
  options: { ignore_account_id: false, exclude_team_accounts: false },
  groups: [
    {
      id: "group-one",
      provider: "codex",
      matched_by: "multiple",
      identity_label: "duplicate@example.com",
      keep_id: "one",
      keep_reason: "healthier_account",
      members: [
        { id: "one", name: "one.json", email: "duplicate@example.com", provider: "codex", type: "codex", status: "ready", disabled: false, unavailable: false, editable: true, recommended_action: "keep" },
        { id: "two", name: "two.json", email: "duplicate@example.com", provider: "codex-agent-identity", type: "codex", status: "ready", disabled: false, unavailable: false, editable: true, recommended_action: "delete" },
        { id: "runtime", name: "runtime.json", email: "duplicate@example.com", provider: "codex", disabled: false, unavailable: false, editable: false, read_only_reason: "runtime-only account has no physical auth file", recommended_action: "skip" },
      ],
    },
    {
      id: "group-two",
      provider: "gemini",
      matched_by: "email",
      identity_label: "gemini@example.com",
      keep_id: "four",
      keep_reason: "newer_evidence",
      members: [
        { id: "four", name: "four.json", email: "gemini@example.com", provider: "gemini", disabled: false, unavailable: false, editable: true, recommended_action: "keep" },
        { id: "five", name: "five.json", email: "gemini@example.com", provider: "gemini", disabled: true, unavailable: false, editable: true, recommended_action: "delete" },
      ],
    },
  ],
};

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem(CPA_LANGUAGE_STORAGE_KEY, '{"state":{"language":"zh-CN"},"version":0}');
});

it("lets the operator override retained credentials and sends only reviewed deletions", async () => {
  const user = userEvent.setup();
  const onReview = vi.fn();
  render(<I18nProvider><AccountDeduplicationDialog preview={preview} loading={false} reviewing={false} onClose={vi.fn()} onOptionsChange={vi.fn()} onReview={onReview} /></I18nProvider>);

  const dialog = screen.getByRole("dialog", { name: "账号去重" });
  expect(within(dialog).getByText("只读跳过")).toBeInTheDocument();
  expect(within(dialog).getByRole("button", { name: "复核删除（2）" })).toBeEnabled();

  const radios = within(dialog).getAllByRole("radio");
  expect(radios[2]).toBeDisabled();
  await user.click(radios[1]);
  const deletionCheckboxes = within(dialog).getAllByRole("checkbox", { name: "删除重复凭证" });
  await user.click(deletionCheckboxes[1]);

  await user.click(within(dialog).getByRole("button", { name: "复核删除（1）" }));
  expect(onReview).toHaveBeenCalledWith(["one"]);
});

it("does not offer a destructive next step when no duplicates are found", () => {
  render(<I18nProvider><AccountDeduplicationDialog preview={{ ...preview, duplicate_groups: 0, duplicate_credentials: 0, proposed_deletions: 0, groups: [] }} loading={false} reviewing={false} onClose={vi.fn()} onOptionsChange={vi.fn()} onReview={vi.fn()} /></I18nProvider>);
  expect(screen.getByText("未发现重复账号")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "复核删除（0）" })).toBeDisabled();
});

it("requests authoritative rescans with combinable identity exclusions", async () => {
  const user = userEvent.setup();
  const onOptionsChange = vi.fn();
  const { rerender } = render(<I18nProvider><AccountDeduplicationDialog preview={preview} loading={false} reviewing={false} onClose={vi.fn()} onOptionsChange={onOptionsChange} onReview={vi.fn()} /></I18nProvider>);

  await user.click(screen.getByRole("checkbox", { name: /忽略账号 ID 判重/ }));
  expect(onOptionsChange).toHaveBeenLastCalledWith({ ignore_account_id: true, exclude_team_accounts: false });

  rerender(<I18nProvider><AccountDeduplicationDialog preview={{ ...preview, excluded_credentials: 2, options: { ignore_account_id: true, exclude_team_accounts: false } }} loading={false} reviewing={false} onClose={vi.fn()} onOptionsChange={onOptionsChange} onReview={vi.fn()} /></I18nProvider>);
  await user.click(screen.getByRole("checkbox", { name: /排除 k12\/team 套餐账号/ }));
  expect(onOptionsChange).toHaveBeenLastCalledWith({ ignore_account_id: true, exclude_team_accounts: true });
  expect(screen.getByText("已排除凭证").nextSibling).toHaveTextContent("2");
});

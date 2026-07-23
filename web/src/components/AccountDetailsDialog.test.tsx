import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import type { Account } from "../types";
import { AccountDetailsDialog } from "./AccountDetailsDialog";

const account: Account = {
  id: "auth-1",
  auth_id: "file-1",
  name: "operator.json",
  provider: "codex",
  type: "codex",
  label: "operator@example.com",
  email: "operator@example.com",
  account_type: "oauth",
  plan_type: "plus",
  status: "active",
  disabled: false,
  unavailable: false,
  runtime_only: false,
  source: "file",
  priority: 8,
  prefix: "team-a",
  proxy: "http://127.0.0.1:7890",
  proxy_configured: true,
  websockets: true,
  header_names: ["Authorization", "X-Team"],
  header_count: 2,
  editable: true,
  success: 15,
  failed: 2,
  usage: {
    input_tokens: 100,
    output_tokens: 40,
    reasoning_tokens: 10,
    cached_tokens: 5,
    cache_read_tokens: 3,
    cache_creation_tokens: 0,
    total_tokens: 158,
    last_request_at: "2026-07-15T10:00:00Z",
  },
  updated_at: "2026-07-15T10:05:00Z",
};

it("shows only the safe account detail model and opens single-account editing", async () => {
  const user = userEvent.setup();
  const onEdit = vi.fn();
  render(<AccountDetailsDialog account={account} onClose={() => undefined} onEdit={onEdit} />);

  expect(screen.getByRole("dialog", { name: "账号详情" })).toBeInTheDocument();
  expect(screen.getAllByText("operator.json").length).toBeGreaterThan(0);
  expect(screen.getByText("plus")).toBeInTheDocument();
  expect(screen.getByText("http://127.0.0.1:7890")).toBeInTheDocument();
  expect(screen.getByText("Authorization")).toBeInTheDocument();
  expect(screen.getByText("158")).toBeInTheDocument();
  expect(screen.queryByText(/access_token|Bearer secret/i)).not.toBeInTheDocument();

  await user.click(screen.getByRole("button", { name: "编辑账号" }));
  expect(onEdit).toHaveBeenCalledTimes(1);
});

it("localizes the Agent Identity account type", () => {
  render(<AccountDetailsDialog account={{ ...account, provider: "codex-agent-identity", account_type: "agent_identity" }} onClose={() => undefined} onEdit={() => undefined} />);
  expect(screen.getByText("Agent Identity")).toBeInTheDocument();
});

it("localizes the Codex PAT account type", () => {
  render(<AccountDetailsDialog account={{ ...account, provider: "codex-agent-identity", account_type: "personal_access_token" }} onClose={() => undefined} onEdit={() => undefined} />);
  expect(screen.getByText("Codex PAT")).toBeInTheDocument();
});

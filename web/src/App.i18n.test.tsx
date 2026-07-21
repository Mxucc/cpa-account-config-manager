import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import App from "./App";
import { I18nProvider } from "./i18n";
import { CPA_LANGUAGE_STORAGE_KEY } from "./i18n/locale";
import { _resetSessionForTest } from "./store/session";

const account = {
  id: "i18n-account",
  name: "i18n.json",
  provider: "codex",
  type: "codex",
  label: "i18n@example.com",
  email: "i18n@example.com",
  plan_type: "plus",
  status: "active",
  disabled: true,
  unavailable: false,
  runtime_only: false,
  source: "file",
  priority: 0,
  proxy_configured: false,
  header_count: 0,
  editable: true,
  success: 10,
  failed: 1,
  automation: {
    health: "quota_limited",
    reason_code: "quota_exhausted",
    recommendation: "enable",
    last_checked_at: "2026-07-20T08:00:00Z",
    owned_disable: true,
    disable_reason: "quota_exhausted",
    disabled_at: "2026-07-20T07:00:00Z",
    recover_after: "2026-07-21T10:30:00Z",
    auto_disable_eligible: true,
    inspection_enabled: true,
    auto_disable_enabled: true,
    auto_enable_enabled: true,
    auto_delete_enabled: false,
    failure_threshold: 3,
    failure_streak: 3,
    recovery_threshold: 2,
    healthy_streak: 0,
  },
};

beforeEach(() => {
  _resetSessionForTest();
  localStorage.clear();
  document.documentElement.lang = "zh-CN";
  vi.restoreAllMocks();
});

it("renders major account surfaces in English and follows a live CPA switch to Chinese", async () => {
  const user = userEvent.setup();
  const english = '{"state":{"language":"en"},"version":0}';
  const chinese = '{"state":{"language":"zh-CN"},"version":0}';
  localStorage.setItem(CPA_LANGUAGE_STORAGE_KEY, english);
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes("/batch/status")) {
      return new Response(JSON.stringify({ state: "idle", running: false, total: 0, eligible: 0, done: 0, succeeded: 0, failed: 0, conflicts: 0, skipped: 0, workers: 0, patch: { fields: [], proxy_mutation: false }, retry_available: false, persisted: false }), { status: 200 });
    }
    return new Response(JSON.stringify({ accounts: [account], total: 1, page: 1, page_size: 50, pages: 1 }), { status: 200 });
  }));

  render(<I18nProvider><App /></I18nProvider>);
  expect(await screen.findByRole("form", { name: "Management Authentication" })).toBeInTheDocument();
  await user.type(screen.getByLabelText("Management Key"), "management-secret");
  await user.click(screen.getByRole("button", { name: "Verify and continue" }));

  expect(await screen.findByRole("heading", { name: "Account Management" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Other Settings" })).toBeInTheDocument();
  expect(screen.getByRole("region", { name: "Account filters" })).toBeInTheDocument();
  expect(screen.getByRole("columnheader", { name: "Provider" })).toBeInTheDocument();
  expect(screen.getByText("Auto-disabled", { selector: ".automation-disposition-badge" })).toBeInTheDocument();
  expect(screen.getByText(/Quota exhausted.*Expected auto-enable/)).toBeInTheDocument();

  act(() => {
    localStorage.setItem(CPA_LANGUAGE_STORAGE_KEY, chinese);
    window.dispatchEvent(new StorageEvent("storage", { key: CPA_LANGUAGE_STORAGE_KEY, oldValue: english, newValue: chinese }));
  });
  expect(await screen.findByRole("heading", { name: "账号管理" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "其他配置" })).toBeInTheDocument();
  expect(screen.getByText("自动禁用", { selector: ".automation-disposition-badge" })).toBeInTheDocument();
});

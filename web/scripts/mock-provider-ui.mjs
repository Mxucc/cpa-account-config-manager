import http from "node:http";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

// Serves the built single-file interface plus the v2 provider API on one origin,
// so the browser can log in and load data exactly as it does inside the
// Management Center. Fixtures deliberately include the awkward cases: an
// unreported quota window, a wallet, a repairing credential and an unpublished
// channel.
const here = dirname(fileURLToPath(import.meta.url));
const dist = process.env.DIST_HTML || resolve(here, "../../internal/web/dist/index.html");
const html = readFileSync(dist, "utf8");
const key = process.env.MOCK_KEY || "evidence-key";
const port = Number(process.env.PORT || 8399);

const providers = [
  { id: "codex", display_name: "Codex", kind: "oauth", description: "OpenAI Codex credentials managed through CPA.", capabilities: { login: true, quota: true, models: true, binding: false, billing: true, refresh: true, test: true, host_auth: true }, accounts: 2, by_status: { ready: 1, quota_limited: 1 } },
  { id: "opencode-go", display_name: "OpenCode Go", kind: "subscription", description: "A monthly allowance split across three windows.", capabilities: { login: false, quota: true, models: true, binding: true, billing: true, refresh: true, test: true, host_auth: false }, accounts: 1, by_status: { ready: 1 } },
  { id: "opencode-zen", display_name: "OpenCode Zen", kind: "wallet", description: "Metered usage against a prepaid balance.", capabilities: { login: false, quota: true, models: true, binding: true, billing: true, refresh: true, test: true, host_auth: false }, accounts: 1, by_status: { ready: 1 } },
  { id: "cline-pass", display_name: "Cline Pass", kind: "subscription", description: "ClinePass limits with automatic credential repair.", capabilities: { login: true, quota: true, models: true, binding: true, billing: true, refresh: true, test: true, host_auth: false }, accounts: 1, by_status: { credential_invalid: 1 } },
  { id: "ai-provider", display_name: "AI Providers", kind: "channel", description: "Plain OpenAI-compatible channels.", capabilities: { login: false, quota: false, models: true, binding: false, billing: true, refresh: false, test: true, host_auth: false }, accounts: 0, by_status: {} },
];

const accounts = [
  {
    id: "provider:codex:acct-1", provider: { id: "codex", kind: "oauth", display_name: "Codex" },
    display_name: "alice@example.com", status: "ready",
    credential: { configured: true, method: "oauth", expires_at: "2026-12-01T00:00:00Z", refreshable: true },
    plan: { id: "plus", name: "ChatGPT Plus", tier: "plus", cycle: { kind: "monthly", renews_at: "2026-12-01T00:00:00Z" }, price: { amount: 20, currency: "USD", period: "month", source: "catalog" } },
    quota: [
      { id: "five_hour", label: "5 hours", kind: "rolling", source_window: "five_hour", used_percent: 42, remaining_percent: 58, unit: "%", reset_at: "2026-11-08T15:00:00Z" },
      { id: "seven_day", label: "7 days", kind: "weekly", source_window: "seven_day", used_percent: 71, remaining_percent: 29, unit: "%", reset_at: "2026-11-12T00:00:00Z" },
    ],
    metrics: [{ key: "active_reset_count", label: "Active reset credits", value: 2 }],
    billing: { mode: "subscription", currency: "USD", period: "month" },
    usage: { input_tokens: 120000, output_tokens: 34000, total_tokens: 154000, cost_usd: 1.82 },
    models: { count: 12, source: "cpa" }, source: { kind: "cpa-auth", ref: "acct-1" }, updated_at: "2026-11-08T10:12:00Z",
  },
  {
    id: "provider:codex:acct-2", provider: { id: "codex", kind: "oauth", display_name: "Codex" },
    display_name: "bob@example.com", status: "quota_limited", status_note: "The gateway limited this account; it recovers after the window ends",
    credential: { configured: true, method: "oauth", refreshable: true },
    plan: { id: "pro", name: "ChatGPT Pro", tier: "pro", cycle: { kind: "monthly" } },
    quota: [
      { id: "five_hour", label: "5 hours", kind: "rolling", used_percent: 100, remaining_percent: 0, unit: "%", reset_at: "2026-11-08T13:40:00Z" },
      { id: "seven_day", label: "7 days", kind: "weekly", used_percent: null, remaining_percent: null, unit: "%" },
    ],
    billing: { mode: "subscription", currency: "USD", period: "month" },
    usage: { input_tokens: 0, output_tokens: 0, total_tokens: 0, cost_usd: 0 },
    models: { count: 9, source: "cpa" }, source: { kind: "cpa-auth", ref: "acct-2" }, updated_at: "2026-11-08T10:05:00Z",
  },
  {
    id: "provider:opencode-go:go-1", provider: { id: "opencode-go", kind: "subscription", display_name: "OpenCode Go" },
    display_name: "team-alpha", status: "ready",
    credential: { configured: true, method: "cookie", refreshable: true },
    plan: { id: "go", name: "OpenCode Go", tier: "go", cycle: { kind: "monthly" }, price: { amount: 10, currency: "USD", period: "month", source: "official-pricing" } },
    quota: [
      { id: "rolling", label: "5 hours", kind: "rolling", used_percent: 20, remaining_percent: 80, limit: 2, unit: "USD", reset_at: "2026-11-08T14:00:00Z" },
      { id: "weekly", label: "7 days", kind: "weekly", used_percent: 55, remaining_percent: 45, limit: 5, unit: "USD", reset_at: "2026-11-12T00:00:00Z" },
      { id: "monthly", label: "Monthly", kind: "monthly", used_percent: 32, remaining_percent: 68, limit: 10, unit: "USD", reset_at: "2026-12-01T00:00:00Z" },
    ],
    billing: { mode: "subscription", currency: "USD", period: "month", allowance: 10, used: 3.2, remaining: 6.8 },
    usage: { input_tokens: 88000, output_tokens: 21000, total_tokens: 109000, cost_usd: 3.2 },
    models: { count: 6, source: "upstream" }, source: { kind: "plugin-store", ref: "go-1" }, updated_at: "2026-11-08T09:58:00Z",
  },
  {
    id: "provider:opencode-zen:zen-1", provider: { id: "opencode-zen", kind: "wallet", display_name: "OpenCode Zen" },
    display_name: "personal", status: "ready",
    credential: { configured: true, method: "api_key", refreshable: true },
    plan: { cycle: { kind: "none" } },
    quota: [{ id: "balance", label: "Balance", kind: "none", balance: 4.25, unit: "USD", source: "upstream" }],
    billing: { mode: "wallet", currency: "USD", remaining: 4.25 },
    usage: { input_tokens: 5200, output_tokens: 900, total_tokens: 6100, cost_usd: 0.75 },
    models: { count: 18, source: "upstream" }, source: { kind: "plugin-store", ref: "zen-1" }, updated_at: "2026-11-08T09:40:00Z",
  },
  {
    id: "provider:cline-pass:cline-1", provider: { id: "cline-pass", kind: "subscription", display_name: "Cline Pass" },
    display_name: "work-cline", status: "credential_invalid", status_note: "The gateway rejected this credential; automatic repair is running",
    credential: { configured: true, method: "device_code", refreshable: true, rejected: true, repairing: true },
    plan: { id: "pro", name: "pro", tier: "pro", cycle: { kind: "monthly" } },
    quota: [
      { id: "five_hour", label: "5 hours", kind: "rolling", used_percent: 70, remaining_percent: 30, unit: "%", note: "Reference-priced usage" },
      { id: "weekly", label: "7 days", kind: "weekly", used_percent: 33, remaining_percent: 67, unit: "%", note: "Reference-priced usage" },
    ],
    metrics: [{ key: "balance", label: "Balance", value: 1.5, unit: "USD", format: "currency", currency: "USD" }],
    billing: { mode: "subscription", currency: "USD", period: "month", allowance: 20, used: 6.6, remaining: 13.4 },
    usage: { input_tokens: 40000, output_tokens: 9000, total_tokens: 49000, cost_usd: 6.6 },
    models: { count: 7, source: "upstream" },
    binding: { kind: "openai-compatibility", published: false, models: 5, gaps: 2, readable: true },
    source: { kind: "plugin-store", ref: "cline-1" }, updated_at: "2026-11-08T09:30:00Z",
  },
];

const catalog = {
  presets: [
    { id: "openrouter", name: "OpenRouter", kind: "channel", protocol: "openai", base_url: "https://openrouter.ai/api/v1", auth_mode: "api_key", tags: ["aggregator"] },
    { id: "deepseek", name: "DeepSeek", kind: "channel", protocol: "openai", base_url: "https://api.deepseek.com/v1", auth_mode: "api_key", tags: ["openai-compatible"] },
    { id: "ollama", name: "Ollama", kind: "channel", protocol: "openai", base_url: "http://127.0.0.1:11434/v1", auth_mode: "none", tags: ["local"] },
  ],
  imported: [
    { id: "cline", name: "Cline", package: "@magpie-community/opencode-cline-auth", version: "0.1.14", source: "magpie", hook_execution: false, notes: ["Provider hooks (auth, models, usage) are recorded but not executed."] },
  ],
  hook_execution_enabled: false,
  bun_available: true,
};

const json = (res, status, body) => {
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify(body));
};

// The legacy console (providers/<id>/advanced) still calls the v0 account routes;
// an empty but well-shaped answer keeps it rendering without console errors.
const legacy = {
  accounts: [], total: 0, page: 1, page_size: 100, pages: 0,
  account_concurrency: { supported: true, host_schema_version: 10, required_schema_version: 2 },
};

http
  .createServer((req, res) => {
    const url = new URL(req.url, "http://127.0.0.1");
    if (url.pathname === "/" || url.pathname === "/index.html") {
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
      res.end(html);
      return;
    }
    if (req.headers.authorization !== `Bearer ${key}`) {
      json(res, 401, { error: "unauthorized" });
      return;
    }
    const base = "/v0/management/plugins/cpa-account-config-manager";
    if (url.pathname === `${base}/providers`) return json(res, 200, { providers });
    if (url.pathname === `${base}/providers/overview`) {
      return json(res, 200, { accounts: accounts.length, by_status: { ready: 3, quota_limited: 1, credential_invalid: 1 }, providers: providers.length, publication_usd: 30, updated_at: "2026-11-08T10:15:00Z" });
    }
    if (url.pathname === `${base}/providers/accounts`) {
      const filter = url.searchParams.get("provider");
      const rows = filter ? accounts.filter((row) => row.provider.id === filter) : accounts;
      return json(res, 200, { accounts: rows, total: rows.length });
    }
    if (url.pathname === `${base}/providers/catalog`) return json(res, 200, catalog);
    if (url.pathname === `${base}/providers/settings`) return json(res, 200, { magpie_hook_execution: false });
    if (url.pathname === `${base}/providers/pricing`) return json(res, 200, { provider: "opencode-go", available: true, snapshot: { source: "official" } });
    if (url.pathname.startsWith(base)) return json(res, 200, { status: "ok" });
    if (url.pathname.startsWith("/v0/management")) return json(res, 200, legacy);
    json(res, 404, { error: "not found", path: url.pathname });
  })
  .listen(port, "127.0.0.1", () => {
    console.log(`mock provider UI on http://127.0.0.1:${port}`);
  });

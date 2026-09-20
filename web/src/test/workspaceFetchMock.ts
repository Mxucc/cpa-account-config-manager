import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import { _resetSessionForTest, setSession } from "../store/session";

export const GO_ACCOUNT_ID = "acc_go_1";
export const GO_WORKSPACE = "wrk_test";
export const ZEN_ACCOUNT_ID = "zen_1";
export const CHANNEL_ZEN_BASE = "https://opencode.ai/zen/v1";
export const CHANNEL_GO_BASE = "https://opencode.ai/zen/go/v1";
export const CLINE_PASS_ACCOUNT_ID = "cline_1";
// Canary that must never be rendered: responses only ever expose `key_set`.
export const UNRENDERED_SECRET = "sk-opencode-canary-secret-1234";

export function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

/** The state every workspace test starts from: no stored session and no restored mocks. */
export function resetWorkspaceTestSession(): void {
  _resetSessionForTest();
  localStorage.clear();
  setSession("", "management-secret");
  vi.restoreAllMocks();
}

/** Click one tab of a tab strip by its accessible name. */
export async function selectTab(user: ReturnType<typeof userEvent.setup>, name: string): Promise<void> {
  await user.click(screen.getByRole("tab", { name }));
}

export function goAccountView(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    id: GO_ACCOUNT_ID,
    workspace_id: GO_WORKSPACE,
    key_set: true,
    cookie_set: true,
    models: ["gpt-5.1", "claude-sonnet-4", "gemini-2.5-pro", "o4-mini"],
    models_error: "",
    models_fetched_at: "2026-09-01T00:00:00Z",
    created_at: "2026-09-01T00:00:00Z",
    updated_at: "2026-09-01T00:00:00Z",
    ...overrides,
  };
}

/**
 * One runtime aggregate CPA recorded for this plugin's own OpenCode Go channel. The auth index is
 * the account or workspace id the channel entry carries, which is how the workspace attributes a
 * snapshot to OpenCode instead of to an OAuth account.
 */
export function openCodeRuntimeSnapshot(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    provider: "openai-compatible-opencode go",
    identity: `account:${GO_ACCOUNT_ID}`,
    auth_index: GO_ACCOUNT_ID,
    supported: true,
    active: 0,
    limit: 0,
    input_tokens: 1_200,
    output_tokens: 800,
    reasoning_tokens: 0,
    cached_tokens: 300,
    total_tokens: 2_000,
    amount_usd: 0.42,
    rated_requests: 9,
    unrated_requests: 1,
    updated_at: "2026-09-16T00:00:00Z",
    ...overrides,
  };
}

export function zenAccountView(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    id: ZEN_ACCOUNT_ID,
    name: "Zen mirror",
    base_url: "https://opencode.ai/zen",
    key_set: true,
    models: ["zen-model-a"],
    models_error: "",
    models_fetched_at: "2026-09-01T00:00:00Z",
    ...overrides,
  };
}

export function clinePassAccountView(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    id: CLINE_PASS_ACCOUNT_ID,
    name: "Work laptop",
    base_url: "https://api.cline.bot/api/v1",
    auth_method: "oauth",
    access_token_set: true,
    refresh_token_set: true,
    expires_at: "2026-09-15T02:00:00Z",
    expired: false,
    models: ["cline-pass/glm-5.3", "cline-pass/kimi-k2.6"],
    models_error: "",
    models_fetched_at: "2026-09-15T01:00:00Z",
    created_at: "2026-09-15T01:00:00Z",
    // CPA routing state attached by the backend once the account is listed.
    channel_bound: true,
    channel_models: 2,
    channel_model_gaps: 0,
    ...overrides,
  };
}

/**
 * The three windows Cline documents for ClinePass and the USD the documented standard API
 * rates attribute to them. Those USD figures are reference prices, not an amount owed.
 */
export function clinePassQuotaUsage(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    five_hour: { usd: 0.42, input_tokens: 120000, output_tokens: 8000, requests: 12 },
    weekly: { usd: 3.1, input_tokens: 900000, output_tokens: 60000, requests: 88 },
    monthly: { usd: 7.85, input_tokens: 2100000, output_tokens: 150000, requests: 210 },
    monthly_subscription_usd: 9.99,
    reference: true,
    ...overrides,
  };
}

/** The allow-listed catalog the backend publishes for the Cline Pass gateway. */
export function clinePassCatalogModels(): Array<Record<string, unknown>> {
  return [
    { id: "cline-pass/glm-5.3", name: "GLM-5.3", free: false },
    { id: "cline-pass/kimi-k2.6", name: "Kimi K2.6", free: false },
    { id: "cline-free/longcat-2.0", name: "LongCat 2.0", free: true },
  ];
}

/** One row of `GET /opencode/cline-pass/models`: the mapping the CPA channel publishes. */
export function clinePassModelsPayload(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    models: [
      {
        id: "cline-pass/deepseek-v4.1-flash",
        name: "DeepSeek V4.1 Flash",
        free: false,
        upstream_id: "cline-pass/deepseek-v4.1-flash",
        client_id: "deepseek-v4.1-flash",
        published: true,
        // The documented reference rates in USD per million tokens.
        priced: true,
        input_usd_per_million: 1.4,
        output_usd_per_million: 4.4,
        cache_read_usd_per_million: 0.26,
        cache_write_usd_per_million: 2.5,
      },
      {
        id: "cline-pass/kimi-k2.6",
        name: "Kimi K2.6",
        free: false,
        upstream_id: "cline-pass/kimi-k2.6",
        client_id: "kimi-k2.6",
        published: false,
        // Cline publishes no rate for this model, so the row has no price fields at all.
        priced: false,
      },
    ],
    strip_model_prefix: true,
    deepseek_upstream_consistency: false,
    accounts: 1,
    channel_bound: true,
    channel_models: 2,
    default_base_url: "https://api.cline.bot/api/v1",
    ...overrides,
  };
}

export function clinePassLoginView(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    session_id: "clinelogin_test",
    method: "oauth",
    status: "pending",
    user_code: "CODE-1234",
    verification_uri: "https://cline.bot/device",
    verification_uri_complete: "https://cline.bot/device?code=CODE-1234",
    interval_seconds: 5,
    expires_in_seconds: 600,
    ...overrides,
  };
}

/** One row of `GET /opencode/channels`: an AI-provider channel that belongs to OpenCode. */
export function channelView(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    kind: "zen",
    name: "Zen channel",
    base_url: CHANNEL_ZEN_BASE,
    key_set: true,
    models: 3,
    source: "ai_provider",
    imported: false,
    ...overrides,
  };
}

/** Pricing snapshot with the official-docs billing block and per-model Go allowances. */
export function billingPricingFixture(): Record<string, unknown> {
  return {
    source: "models.dev (OpenCode Zen and OpenCode Go)",
    updated_at: "2026-09-11T00:00:00Z",
    docs_updated_at: "2026-09-12T00:00:00Z",
    billing: [
      {
        kind: "zen",
        metered: true,
        docs_url: "https://opencode.ai/docs/zen/",
        summary: "Pay-as-you-go per 1M tokens; balance auto-reloads below $5 (default $20) and a monthly workspace limit can cap spend.",
      },
      {
        kind: "go",
        metered: false,
        subscription_usd_per_month: 10,
        five_hour_fraction: 0.2,
        weekly_fraction: 0.5,
        docs_url: "https://opencode.ai/docs/go/",
        summary: "$10/month subscription; each model has a monthly USD allowance split 20% / 50% / 100%.",
      },
    ],
    go: [
      {
        id: "longcat-2.0",
        name: "LongCat-2.0",
        input_usd_per_million: 0.3,
        output_usd_per_million: 1.2,
        context_tokens: 1000000,
        monthly_limit_usd: 60,
        estimated_requests: { five_hour: 1830, weekly: 4580, monthly: 9150 },
      },
      {
        id: "legacy-mini",
        name: "Legacy Mini",
        input_usd_per_million: 1,
        output_usd_per_million: 2,
        context_tokens: 200000,
        monthly_limit_usd: 20,
        deprecated_at: "January 2026",
      },
    ],
    zen: [
      {
        id: "gemini-3.1-pro",
        name: "Gemini 3.1 Pro",
        input_usd_per_million: 2,
        output_usd_per_million: 12,
        cache_read_usd_per_million: 0.2,
        tiers: [{ min_context_tokens: 200000, input_usd_per_million: 4 }],
      },
    ],
  };
}

export interface WorkspaceFetchMockOptions {
  accounts?: Array<Record<string, unknown>>;
  accountsStatus?: number;
  accountsErrorBody?: Record<string, unknown>;
  zenAccounts?: Array<Record<string, unknown>>;
  channels?: Array<Record<string, unknown>>;
  importResponse?: Record<string, unknown>;
  importStatus?: number;
  importErrorBody?: Record<string, unknown>;
  quota?: Record<string, Record<string, unknown>>;
  quotaRefresh?: Record<string, unknown>;
  modelsResponse?: Record<string, unknown>;
  modelTestResponse?: Record<string, unknown>;
  bindResponse?: Record<string, unknown>;
  pricing?: Record<string, unknown>;
  modelControl?: Record<string, unknown>;
  pricingRefresh?: Record<string, unknown>;
  session?: Record<string, unknown>;
  clinePassAccounts?: Array<Record<string, unknown>>;
  clinePassLoginStart?: Record<string, unknown>;
  clinePassLoginPoll?: Record<string, unknown>;
  /** The initial `strip_model_prefix` the mock serves; a PUT flips it for later GETs. */
  clinePassStripPrefix?: boolean;
  /** The initial `deepseek_upstream_consistency` the mock serves; a PUT flips it. */
  clinePassDeepseekConsistency?: boolean;
  clinePassModels?: Record<string, unknown>;
  /** Status the accounts read answers with, to exercise a failed page load. */
  clinePassAccountsStatus?: number;
  clinePassModelsStatus?: number;
  clinePassSettingsStatus?: number;
  clinePassSettingsRebound?: number;
  clinePassModelTest?: Record<string, unknown>;
  /** Runtime aggregates CPA recorded for the OpenCode channels, for the overview usage cards. */
  runtimeSnapshots?: Array<Record<string, unknown>>;
}

/**
 * One fetch mock for both product surfaces: the OpenCode workspace and the Cline Pass
 * workspace read different sets of routes, so the same fixture server serves them both.
 * Returns the recorded requests so a test can assert the route and body that was used.
 */
export function stubWorkspaceFetch(options: WorkspaceFetchMockOptions = {}): Array<{ url: string; init: RequestInit }> {
  const requests: Array<{ url: string; init: RequestInit }> = [];
  let disabledModels = [...((options.modelControl?.disabled as string[] | undefined) ?? [])];
  let stripModelPrefix = options.clinePassStripPrefix ?? true;
  let deepseekConsistency = options.clinePassDeepseekConsistency ?? false;
  const controlRows = (disabled: string[]) => [
    { id: "qwen3.7-max", disabled: disabled.includes("qwen3.7-max"), accounts: 1, channels: 1, priced: true, input_usd_per_million: 2.5, output_usd_per_million: 7.5 },
    { id: "gpt-5.6-luna", disabled: disabled.includes("gpt-5.6-luna"), accounts: 1, channels: 0, priced: true, input_usd_per_million: 0.2, output_usd_per_million: 1.2 },
    { id: "unpriced-opencode", disabled: disabled.includes("unpriced-opencode"), accounts: 0, channels: 0, priced: false },
  ];
  const modelControlBody = (opts: WorkspaceFetchMockOptions) => ({
    storage_error: "",
    pricing_source: "Sub2API / Wei-Shaw model-price-repo",
    ...(opts.modelControl ?? {}),
  });
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    requests.push({ url, init });
    if (url.endsWith("/opencode/zen/accounts") && init.method === "POST") {
      return jsonResponse({ account: zenAccountView(), result: { success: true } });
    }
    if (url.endsWith("/opencode/zen/accounts")) return jsonResponse({ accounts: options.zenAccounts ?? [] });
    if (url.endsWith("/opencode/cline-pass/catalog")) {
      return jsonResponse({ models: clinePassCatalogModels(), default_base_url: "https://api.cline.bot/api/v1" });
    }
    if (url.endsWith("/opencode/cline-pass/login/start")) {
      return jsonResponse(options.clinePassLoginStart ?? clinePassLoginView());
    }
    if (url.endsWith("/opencode/cline-pass/login/poll")) {
      return jsonResponse(
        options.clinePassLoginPoll
          ?? clinePassLoginView({ status: "completed", account: clinePassAccountView() }),
      );
    }
    if (url.endsWith("/opencode/cline-pass/login/cancel")) return jsonResponse({ cancelled: true });
    if (url.endsWith("/opencode/cline-pass/accounts") && init.method === "POST") {
      return jsonResponse({ account: clinePassAccountView(), result: { reachable: true } });
    }
    if (url.includes("/opencode/cline-pass/accounts?") && init.method === "DELETE") return jsonResponse({ removed: true });
    if (url.endsWith("/opencode/cline-pass/accounts")) {
      if (options.clinePassAccountsStatus) {
        return jsonResponse({ error: "cline pass accounts failed" }, options.clinePassAccountsStatus);
      }
      return jsonResponse({ accounts: options.clinePassAccounts ?? [] });
    }
    if (url.endsWith("/opencode/cline-pass/refresh")) return jsonResponse({ account: clinePassAccountView() });
    if (url.endsWith("/opencode/cline-pass/models") && init.method === "POST") {
      return jsonResponse({ account: clinePassAccountView() });
    }
    if (url.endsWith("/opencode/cline-pass/models")) {
      if (options.clinePassModelsStatus) {
        return jsonResponse({ error: "cline pass models failed" }, options.clinePassModelsStatus);
      }
      // The served mapping follows the last PUT so a save-and-reload is observable.
      return jsonResponse({ ...clinePassModelsPayload(), ...(options.clinePassModels ?? {}), strip_model_prefix: stripModelPrefix, deepseek_upstream_consistency: deepseekConsistency });
    }
    if (url.endsWith("/opencode/cline-pass/settings") && init.method === "PUT") {
      if (options.clinePassSettingsStatus) {
        return jsonResponse({ error: "cline pass settings failed" }, options.clinePassSettingsStatus);
      }
      const body = JSON.parse(String(init.body ?? "{}")) as { strip_model_prefix?: boolean; deepseek_upstream_consistency?: boolean };
      // A PUT carries one control at a time, so the other keeps its stored value.
      if (body.strip_model_prefix !== undefined) stripModelPrefix = body.strip_model_prefix === true;
      if (body.deepseek_upstream_consistency !== undefined) deepseekConsistency = body.deepseek_upstream_consistency === true;
      return jsonResponse({
        settings: { strip_model_prefix: stripModelPrefix, deepseek_upstream_consistency: deepseekConsistency },
        rebound: options.clinePassSettingsRebound ?? 0,
        rebind_errors: 0,
      });
    }
    if (url.endsWith("/opencode/cline-pass/settings")) {
      return jsonResponse({ settings: { strip_model_prefix: stripModelPrefix, deepseek_upstream_consistency: deepseekConsistency } });
    }
    if (url.endsWith("/opencode/cline-pass/model-test")) return jsonResponse(options.clinePassModelTest ?? { result: {} });
    if (url.endsWith("/opencode/cline-pass/bind")) {
      return jsonResponse({
        binding: {
          kind: "openai-compatibility",
          base_url: "https://api.cline.bot/api/v1",
          index: 0,
          created: true,
          channel_key: "openai-compatibility:0",
          models: 3,
        },
      });
    }
    if (url.endsWith("/opencode/channels")) return jsonResponse({ channels: options.channels ?? [] });
    if (url.endsWith("/opencode/import") && init.method === "POST") {
      if (options.importStatus) {
        return jsonResponse(options.importErrorBody ?? { error: "opencode import failed" }, options.importStatus);
      }
      return jsonResponse({
        import: options.importResponse ?? {
          kind: "zen",
          action: "create_zen",
          account_id: ZEN_ACCOUNT_ID,
          name: "Zen mirror",
          base_url: CHANNEL_ZEN_BASE,
        },
      });
    }
    if (url.endsWith("/opencode/accounts") && init.method === "POST") {
      return jsonResponse({ account: goAccountView(), result: { success: true } });
    }
    if (url.includes("/opencode/accounts?") && init.method === "DELETE") return jsonResponse({ removed: true });
    if (url.endsWith("/opencode/accounts")) {
      if (options.accountsStatus) {
        return jsonResponse(options.accountsErrorBody ?? { error: "opencode accounts failed" }, options.accountsStatus);
      }
      return jsonResponse({ accounts: options.accounts ?? [goAccountView()] });
    }
    if (url.includes("/opencode/refresh-account?") && init.method === "POST") {
      return jsonResponse({ result: options.quotaRefresh ?? {} });
    }
    if (url.endsWith("/opencode/quota")) return jsonResponse({ results: options.quota ?? {}, storage_error: "" });
    if (url.endsWith("/opencode/models")) return jsonResponse(options.modelsResponse ?? { account: goAccountView() });
    if (url.endsWith("/opencode/model-test")) return jsonResponse(options.modelTestResponse ?? {});
    if (url.endsWith("/opencode/bind")) return jsonResponse(options.bindResponse ?? {});
    if (url.endsWith("/opencode/pricing/refresh") && init.method === "POST") {
      return jsonResponse({ changed: true, pricing: options.pricingRefresh ?? options.pricing ?? {} });
    }
    if (url.endsWith("/opencode/pricing")) return jsonResponse({ pricing: options.pricing ?? {} });
    if (url.endsWith("/opencode/model-control") && init.method === "PUT") {
      const body = JSON.parse(String(init.body ?? "{}")) as { disabled?: string[] };
      disabledModels = body.disabled ?? [];
      return jsonResponse({ ...modelControlBody(options), disabled: disabledModels, models: controlRows(disabledModels) });
    }
    if (url.endsWith("/opencode/model-control")) {
      return jsonResponse({ ...modelControlBody(options), disabled: disabledModels, models: controlRows(disabledModels) });
    }
    if (url.endsWith("/opencode/session")) return jsonResponse({ session: options.session ?? {} });
    // The plugin's own runtime route. The workspace reads it for the usage of its own channels,
    // so a test states the aggregates CPA recorded (none by default).
    if (url.endsWith("/ai-providers/runtime")) {
      return jsonResponse({ updated_at: "2026-09-16T00:00:00Z", snapshots: options.runtimeSnapshots ?? [] });
    }
    return jsonResponse({});
  });
  vi.stubGlobal("fetch", fetchMock);
  return requests;
}

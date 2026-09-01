import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  createAccountDeletePreview,
  createBatchDeletePreview,
  createImportPreview,
  createPreview,
  clearOperations,
  compareCPAServerVersions,
  completeAgentIdentitySessionLogin,
  deleteAccount,
  deleteAIProviderChannelEntry,
  downloadExport,
  executeInspectionAutoDelete,
  getEffectiveUpdateStatus,
  getImportStatus,
  getOperationRetentionSettings,
  getAIProviderRuntime,
  getLiveInspection,
  getPluginStore,
  getCPAServerVersionStatus,
  installPluginUpdate,
  listAccounts,
  listAIProviderChannels,
  listOpenCodeAccounts,
  listOpenCodeZenAccounts,
  loadAccountConfig,
  listInspectionActions,
  listInspectionResults,
  listOperations,
	loadAccountModels,
	persistCurrentSettings,
	persistCurrentSettingsOnce,
  previewInspectionNotification,
  reconcileUpdateStatus,
  retryBatch,
  saveDefaultPolicy,
  saveInspectionPolicy,
  saveOperationRetentionSettings,
  saveUpdatePolicy,
  scanAccountDuplicates,
  scanFullInspection,
  scanNativeInspection,
  startImport,
  startBatchDelete,
  testInspectionNotification,
  testAccountModel,
  saveOpenCodeAccount,
  removeOpenCodeAccount,
  refreshOpenCodeQuota,
  probeOpenCodeQuota,
  patchAIProviderChannelEntry,
  saveAIProviderChannelEntry,
  setAIProviderChannelEnabled,
  testAIProviderChannelForKind,
} from "./client";
import { _resetSessionForTest, setSession } from "../store/session";

describe("management API client", () => {
  beforeEach(() => {
    _resetSessionForTest();
    localStorage.clear();
    sessionStorage.clear();
    vi.restoreAllMocks();
  });


  it("rejects malformed nested provider credentials instead of silently dropping them", async () => {
    setSession("https://cpa.example", "management-secret");
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/openai-compatibility")) {
        return new Response(JSON.stringify({ "openai-compatibility": [{ name: "broken", "api-key-entries": [null] }] }), { status: 200 });
      }
      if (url.endsWith("/opencode/accounts") || url.endsWith("/opencode/zen/accounts")) {
        return new Response(JSON.stringify({ accounts: [] }), { status: 200 });
      }
      const kind = url.slice(url.lastIndexOf("/") + 1);
      return new Response(JSON.stringify({ [kind]: [] }), { status: 200 });
    });
    vi.stubGlobal("fetch", fetchMock);

    const channels = await listAIProviderChannels();
    expect(channels.find((channel) => channel.kind === "openai-compatibility")).toMatchObject({ error: "provider_channel_response_invalid" });
  });

  it("keeps successful provider channels while surfacing a failed channel", async () => {
    setSession("https://cpa.example", "management-secret");
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/openai-compatibility")) {
        return new Response(JSON.stringify({ "openai-compatibility": [{ name: "working" }] }), { status: 200 });
      }
      if (url.endsWith("/gemini-api-key")) {
        return new Response(JSON.stringify({ error: "internal details must not escape" }), { status: 500 });
      }
      if (url.endsWith("/opencode/accounts") || url.endsWith("/opencode/zen/accounts")) {
        return new Response(JSON.stringify({ accounts: [] }), { status: 200 });
      }
      const kind = url.slice(url.lastIndexOf("/") + 1);
      return new Response(JSON.stringify({ [kind]: [] }), { status: 200 });
    });
    vi.stubGlobal("fetch", fetchMock);

    const channels = await listAIProviderChannels();

    expect(channels.find((channel) => channel.kind === "openai-compatibility")?.entries).toHaveLength(1);
    expect(channels.find((channel) => channel.kind === "gemini-api-key")).toMatchObject({
      count: 0,
      entries: [],
      error: "provider_channel_unavailable",
    });
    expect(JSON.stringify(channels)).not.toContain("internal details must not escape");
  });

  it("surfaces malformed provider channel responses instead of treating them as empty", async () => {
    setSession("https://cpa.example", "management-secret");
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/opencode/accounts") || url.endsWith("/opencode/zen/accounts")) {
        return new Response(JSON.stringify({ accounts: [] }), { status: 200 });
      }
      if (url.endsWith("/openai-compatibility")) {
        return new Response(JSON.stringify({ unexpected: [] }), { status: 200 });
      }
      const kind = url.slice(url.lastIndexOf("/") + 1);
      return new Response(JSON.stringify({ [kind]: [] }), { status: 200 });
    });
    vi.stubGlobal("fetch", fetchMock);

    const channels = await listAIProviderChannels();

    expect(channels.find((channel) => channel.kind === "openai-compatibility")?.error).toBe("provider_channel_response_invalid");
  });

  it("surfaces malformed provider rows instead of silently dropping credentials", async () => {
    setSession("https://cpa.example", "management-secret");
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/opencode/accounts") || url.endsWith("/opencode/zen/accounts")) {
        return new Response(JSON.stringify({ accounts: [] }), { status: 200 });
      }
      if (url.endsWith("/openai-compatibility")) {
        return new Response(JSON.stringify({ "openai-compatibility": [{ name: "valid" }, null] }), { status: 200 });
      }
      const kind = url.slice(url.lastIndexOf("/") + 1);
      return new Response(JSON.stringify({ [kind]: [] }), { status: 200 });
    });
    vi.stubGlobal("fetch", fetchMock);

    const channels = await listAIProviderChannels();

    expect(channels.find((channel) => channel.kind === "openai-compatibility")).toMatchObject({
      count: 0,
      entries: [],
      error: "provider_channel_response_invalid",
    });
  });

  it("aborts provider enumeration immediately on management authentication failure", async () => {
    setSession("https://cpa.example", "management-secret");
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: "unauthorized" }), { status: 401 }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(listAIProviderChannels()).rejects.toMatchObject({ status: 401 });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("surfaces OpenCode storage failures without exposing raw storage details", async () => {
    setSession("https://cpa.example", "management-secret");
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/opencode/accounts")) {
        return new Response(JSON.stringify({ accounts: [{ id: "safe-id", workspace_id: "safe-workspace" }], storage_error: "/secret/path failed" }), { status: 200 });
      }
      if (url.endsWith("/opencode/zen/accounts")) {
        return new Response(JSON.stringify({ accounts: [] }), { status: 200 });
      }
      const kind = url.slice(url.lastIndexOf("/") + 1);
      return new Response(JSON.stringify({ [kind]: [] }), { status: 200 });
    });
    vi.stubGlobal("fetch", fetchMock);

    const channels = await listAIProviderChannels();
    const openCode = channels.find((channel) => channel.kind === "opencode-go");

    expect(openCode).toMatchObject({ count: 1, storage_error: "provider_storage_unavailable" });
    expect(JSON.stringify(openCode)).not.toContain("/secret/path");
  });
  it("uses the host string-list PATCH shape for api keys", async () => {
    setSession("https://cpa.example", "management-secret");
    const fetchMock = vi.fn().mockResolvedValue(new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await patchAIProviderChannelEntry("api-keys", 2, "sk-replacement");
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("https://cpa.example/v0/management/api-keys");
    expect(init.method).toBe("PATCH");
    expect(JSON.parse(String(init.body))).toEqual({ index: 2, value: "sk-replacement" });
  });

  it("deletes an AI provider entry exactly once", async () => {
    setSession("https://cpa.example", "management-secret");
    const fetchMock = vi.fn().mockResolvedValue(new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await deleteAIProviderChannelEntry("openai-compatibility", 2);

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("https://cpa.example/v0/management/openai-compatibility?index=2");
    expect(init.method).toBe("DELETE");
  });

  it("sends provider kind for provider probes", async () => {
    setSession("https://cpa.example", "management-secret");
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ reachable: true }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await testAIProviderChannelForKind("claude-api-key", "", "sk-claude");
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toMatchObject({ kind: "claude-api-key", api_key: "sk-claude" });
  });

  it("sends credential identity for codex provider probes", async () => {
    setSession("https://cpa.example", "management-secret");
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ reachable: true }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

		await testAIProviderChannelForKind("codex-api-key", "https://gptpro.live/v1", "sk-codex", 15, undefined, "provider-auth", "gpt-5.6-sol", "codex-api-key:provider-auth");
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toMatchObject({
      kind: "codex-api-key",
      base_url: "https://gptpro.live/v1",
      api_key: "sk-codex",
			auth_id: "provider-auth",
			model: "gpt-5.6-sol",
			provider_key: "codex-api-key:provider-auth",
		});
  });

  it("preserves weighted API-key channels when toggled back on", async () => {
    setSession("https://cpa.example", "management-secret");
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ "gemini-api-key": [{ "api-key": "AIza-test", weight: 100 }] }), { status: 200 }))
      .mockResolvedValueOnce(new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await setAIProviderChannelEnabled("gemini-api-key", 0, true);
    const [, init] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toEqual({ index: 0, value: { weight: 100 } });
  });

	it("clears optional provider fields without dropping unknown host configuration", async () => {
    setSession("https://cpa.example", "management-secret");
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        "openai-compatibility": [{
          name: "OpenRouter",
          "base-url": "https://openrouter.example/v1",
          prefix: "old-prefix",
          "proxy-url": "https://proxy.example",
          headers: { "X-Test": "old" },
          priority: 8,
          weight: 100,
          models: [{ name: "old-model" }],
          "api-key-entries": [{ "api-key": "sk-existing", "auth-index": "openai-live" }],
          "future-host-field": { enabled: true },
        }],
      }), { status: 200 }))
      .mockResolvedValueOnce(new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await saveAIProviderChannelEntry("openai-compatibility", 0, {
      name: "OpenRouter",
      base_url: "https://openrouter.example/v1",
      prefix: "",
      proxy_url: "",
      headers: {},
      priority: null,
      weight: null,
      models: [],
      api_key_entries: [{ api_key: "sk-existing" }],
      disabled: false,
    });

    const [, init] = fetchMock.mock.calls[1] as [string, RequestInit];
    const body = JSON.parse(String(init.body)) as Array<Record<string, unknown>>;
    expect(body[0]).toMatchObject({
      name: "OpenRouter",
      "base-url": "https://openrouter.example/v1",
      models: [],
      "api-key-entries": [{ "api-key": "sk-existing" }],
      disabled: false,
      "future-host-field": { enabled: true },
    });
		expect(body[0]["api-key-entries"]).toEqual([{ "api-key": "sk-existing" }]);
		for (const removed of ["prefix", "proxy-url", "headers", "priority", "weight"]) {
			expect(body[0][removed]).toBeUndefined();
		}
	});

	it("preserves CPA auth-index metadata when only provider fields change", async () => {
		setSession("https://cpa.example", "management-secret");
		const fetchMock = vi.fn()
			.mockResolvedValueOnce(new Response(JSON.stringify({
				"openai-compatibility": [{
					name: "OpenRouter",
					"base-url": "https://openrouter.example/v1",
					"api-key-entries": [{ "api-key": "sk-existing", "auth-index": "openai-live" }],
				}],
			}), { status: 200 }))
			.mockResolvedValueOnce(new Response("{}", { status: 200 }));
		vi.stubGlobal("fetch", fetchMock);

		await saveAIProviderChannelEntry("openai-compatibility", 0, {
			name: "OpenRouter renamed",
		});

		const [, init] = fetchMock.mock.calls[1] as [string, RequestInit];
		const body = JSON.parse(String(init.body)) as Array<Record<string, unknown>>;
		expect(body[0]["api-key-entries"]).toEqual([{ "api-key": "sk-existing", "auth-index": "openai-live" }]);
	});

  it("preserves CPA request retry and scoped error metadata on provider channels", async () => {
    setSession("https://cpa.example", "management-secret");
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/openai-compatibility")) {
        return new Response(JSON.stringify({
          "openai-compatibility": [{
            name: "retryable",
            "request-retry": 3,
            "request-scoped-errors": [{ match: "rate limited", scope: "model" }],
          }],
        }), { status: 200 });
      }
      const kind = url.slice(url.lastIndexOf("/") + 1);
      return new Response(JSON.stringify({ [kind]: [] }), { status: 200 });
    });
    vi.stubGlobal("fetch", fetchMock);

    const channels = await listAIProviderChannels();
    const entry = channels.find((channel) => channel.kind === "openai-compatibility")?.entries[0];

    expect(entry).toMatchObject({
      request_retry: 3,
      request_scoped_errors: [{ match: "rate limited", scope: "model" }],
    });
  });

  it("migrates a legacy top-level OpenAI-compatible API key when rewriting an entry", async () => {
    setSession("https://cpa.example", "management-secret");
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        "openai-compatibility": [{ "api-key": "sk-legacy" }],
      }), { status: 200 }))
      .mockResolvedValueOnce(new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await saveAIProviderChannelEntry("openai-compatibility", 0, { name: "Legacy" });

    const [, init] = fetchMock.mock.calls[1] as [string, RequestInit];
    const body = JSON.parse(String(init.body)) as Array<Record<string, unknown>>;
    expect(body[0]["api-key"]).toBeUndefined();
    expect(body[0]["api-key-entries"]).toEqual([{ "api-key": "sk-legacy" }]);
  });

  it("rejects malformed provider payload items instead of rewriting them as credentials", async () => {
    setSession("https://cpa.example", "management-secret");
    const fetchMock = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({
      "openai-compatibility": [null, 42, { "api-key-entries": [null, { "api-key": "sk-existing" }] }],
    }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(saveAIProviderChannelEntry("openai-compatibility", 2, { api_key: "sk-updated" }))
      .rejects.toThrow("openai-compatibility channel entry #1 is malformed");
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

	it("does not persist blank OpenAI-compatible key rows", async () => {
    setSession("https://cpa.example", "management-secret");
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        "openai-compatibility": [{ "api-key-entries": [{ "api-key": "sk-existing" }] }],
      }), { status: 200 }))
      .mockResolvedValueOnce(new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await saveAIProviderChannelEntry("openai-compatibility", 0, {
      api_key_entries: [{ api_key: "   ", weight: 100 }, { api_key: "sk-valid", weight: 2 }],
    });
    const [, init] = fetchMock.mock.calls[1] as [string, RequestInit];
    const body = JSON.parse(String(init.body)) as Array<Record<string, unknown>>;
    expect(body[0]["api-key-entries"]).toEqual([{ "api-key": "sk-valid", weight: 2 }]);
  });

  it("replaces an edited OpenAI-compatible key while preserving other credentials", async () => {
    setSession("https://cpa.example", "management-secret");
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        "openai-compatibility": [{
          name: "OpenRouter",
          "api-key-entries": [
            { "api-key": "sk-old", weight: 3, "auth-index": "old-index" },
            { "api-key": "sk-keep", weight: 1, "auth-index": "keep-index" },
          ],
        }],
      }), { status: 200 }))
      .mockResolvedValueOnce(new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await saveAIProviderChannelEntry("openai-compatibility", 0, {
      api_key_entries: [
        { api_key: "sk-new", weight: 3, raw: { "api-key": "sk-old", weight: 3, "auth-index": "old-index" } },
        { api_key: "sk-keep", weight: 1, raw: { "api-key": "sk-keep", weight: 1, "auth-index": "keep-index" } },
      ],
    });

    const [, init] = fetchMock.mock.calls[1] as [string, RequestInit];
    const body = JSON.parse(String(init.body)) as Array<Record<string, unknown>>;
    expect(body[0]["api-key-entries"]).toEqual([
      { "api-key": "sk-new", weight: 3, "auth-index": "old-index" },
      { "api-key": "sk-keep", weight: 1, "auth-index": "keep-index" },
    ]);
  });

  it("preserves CPA model fields that the editor does not expose", async () => {
    setSession("https://cpa.example", "management-secret");
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        "claude-api-key": [{
          name: "claude",
          models: [{
            name: "claude-3-7-sonnet",
            thinking: { type: "enabled", budget: 4096 },
            "future-model-field": { enabled: true },
          }],
        }],
      }), { status: 200 }))
      .mockResolvedValueOnce(new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await saveAIProviderChannelEntry("claude-api-key", 0, {
      models: [{ name: "claude-3-7-sonnet" }],
    });

    const [, init] = fetchMock.mock.calls[1] as [string, RequestInit];
    const body = JSON.parse(String(init.body)) as Array<Record<string, unknown>>;
    expect(body[0].models).toEqual([{
      name: "claude-3-7-sonnet",
      thinking: { type: "enabled", budget: 4096 },
      "future-model-field": { enabled: true },
    }]);
  });

  it("preserves Claude fingerprint profile and updates the exposed profile field", async () => {
    setSession("https://cpa.example", "management-secret");
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        "claude-api-key": [{
          "api-key": "sk-claude",
          "fingerprint-profile": "claude-code-cli",
          weight: 100,
          "future-host-field": { enabled: true },
        }],
      }), { status: 200 }))
      .mockResolvedValueOnce(new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await saveAIProviderChannelEntry("claude-api-key", 0, { fingerprint_profile: "" });

    const [, init] = fetchMock.mock.calls[1] as [string, RequestInit];
    const body = JSON.parse(String(init.body)) as Array<Record<string, unknown>>;
    expect(body[0]).toEqual({
      "api-key": "sk-claude",
      weight: 100,
      "future-host-field": { enabled: true },
    });
  });

  it("adds the in-memory management key and serializes filters", async () => {
    setSession("", "management-secret");
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      accounts: [], total: 0, page: 1, page_size: 50, pages: 0,
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);

    await listAccounts(2, 50, { provider: "codex", type: "k12", disabled: false, search: "operator" });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toContain("/accounts?");
    expect(url).toContain("provider=codex");
    expect(url).toContain("type=k12");
    expect(url).toContain("disabled=false");
    expect(url).toContain("page=2");
    expect(url).toContain("sort_by=account");
    expect(url).toContain("sort_order=asc");
    expect(new Headers(init.headers).get("Authorization")).toBe("Bearer management-secret");
    expect(localStorage.length).toBe(0);
  });

  it("serializes an explicit account sort for full-set server ordering", async () => {
    setSession("", "management-secret");
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({
      accounts: [], total: 0, page: 1, page_size: 50, pages: 0,
    }));
    vi.stubGlobal("fetch", fetchMock);

    await listAccounts(1, 50, {}, { field: "usage", order: "desc" });

    const [rawURL] = fetchMock.mock.calls[0] as [string, RequestInit];
    const url = new URL(rawURL, "http://localhost");
    expect(url.searchParams.get("sort_by")).toBe("usage");
    expect(url.searchParams.get("sort_order")).toBe("desc");
  });

	it("loads one safe editable account configuration from the fixed authenticated route", async () => {
		setSession("https://cpa.example", "management-secret");
		const response = {
			account_id: "auth-1", disabled: false, priority: 8, note: "primary pool", prefix: "team-a",
			proxy: "http://proxy.example", proxy_configured: true, websockets: false,
			header_names: null,
			model_policy: { mode: "allow_only", models: ["gpt-5.5"], excluded_count: 2 },
		};
		const fetchMock = vi.fn().mockResolvedValue(jsonResponse(response));
		vi.stubGlobal("fetch", fetchMock);

		await expect(loadAccountConfig("auth-1")).resolves.toMatchObject({ account_id: "auth-1", header_names: [] });
		const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
		expect(url).toBe("https://cpa.example/v0/management/plugins/cpa-account-config-manager/accounts/config");
		expect(init.method).toBe("POST");
		expect(JSON.parse(String(init.body))).toEqual({ account_id: "auth-1" });
		expect(new Headers(init.headers).get("Authorization")).toBe("Bearer management-secret");
	});

  it("submits Session JSON only to the authenticated Agent Identity login route", async () => {
    setSession("https://cpa.example", "management-secret");
    const response = {
      status: "completed",
      account: { email: "agent@example.com", plan_type: "team", provider: "codex-agent-identity", login_state: "login-state" },
    };
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(response));
    vi.stubGlobal("fetch", fetchMock);

    await expect(completeAgentIdentitySessionLogin("login-state", "{\"accessToken\":\"secret\"}")).resolves.toEqual(response);

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("https://cpa.example/v0/management/plugins/cpa-account-config-manager/experiments/agent-identity/session-login");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({ state: "login-state", session_json: "{\"accessToken\":\"secret\"}" });
    expect(new Headers(init.headers).get("Authorization")).toBe("Bearer management-secret");
    expect(localStorage.length).toBe(0);
  });

  it("saves an OpenCode workspace credential through the authenticated route without localStorage persistence", async () => {
    setSession("https://cpa.example", "management-secret");
    const response = {
      account: { id: "wrk_x_123", workspace_id: "wrk_x" },
      result: { success: true, workspace: "wrk_x", rolling: { usage_percent: 12, percent_remaining: 88, reset_in_sec: 3600, reset_at: "2026-08-07T00:00:00Z" } },
    };
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(response));
    vi.stubGlobal("fetch", fetchMock);

    await expect(saveOpenCodeAccount("wrk_x", "auth-cookie-value")).resolves.toEqual(response);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("https://cpa.example/v0/management/plugins/cpa-account-config-manager/opencode/accounts");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({ workspace_id: "wrk_x", auth_cookie: "auth-cookie-value" });
    expect(new Headers(init.headers).get("Authorization")).toBe("Bearer management-secret");
    expect(localStorage.length).toBe(0);
  });

  it("removes an OpenCode account by its unique account ID", async () => {
    setSession("https://cpa.example", "management-secret");
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ removed: true }));
    vi.stubGlobal("fetch", fetchMock);

    await removeOpenCodeAccount("wrk_x_123");
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("https://cpa.example/v0/management/plugins/cpa-account-config-manager/opencode/accounts?account_id=wrk_x_123");
    expect(init.method).toBe("DELETE");
  });

  it("forces an OpenCode quota refresh and probes one workspace without saving", async () => {
    setSession("https://cpa.example", "management-secret");
    const refreshMock = vi.fn().mockResolvedValue(jsonResponse({ results: {} }));
    vi.stubGlobal("fetch", refreshMock);
    await refreshOpenCodeQuota();
    const [refreshURL, refreshInit] = refreshMock.mock.calls[0] as [string, RequestInit];
    expect(refreshURL).toBe("https://cpa.example/v0/management/plugins/cpa-account-config-manager/opencode/refresh");
    expect(refreshInit.method).toBe("POST");

    const probeMock = vi.fn().mockResolvedValue(jsonResponse({ result: { success: true, workspace: "wrk_y" } }));
    vi.stubGlobal("fetch", probeMock);
    await probeOpenCodeQuota("wrk_y", "cookie", 15);
    const [probeURL, probeInit] = probeMock.mock.calls[0] as [string, RequestInit];
    expect(probeURL).toBe("https://cpa.example/v0/management/plugins/cpa-account-config-manager/opencode/probe");
    expect(probeInit.method).toBe("POST");
    expect(JSON.parse(String(probeInit.body))).toEqual({ workspace_id: "wrk_y", auth_cookie: "cookie", timeout_seconds: 15 });
  });

  it("reads the connected and latest CPA server versions from the authenticated CPA endpoint", async () => {
    setSession("https://cpa.example", "management-secret");
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ "latest-version": "v7.2.93" }), {
      status: 200,
      headers: {
        "Content-Type": "application/json",
        "X-CPA-Version": "7.2.92",
        "X-CPA-Build-Date": "2026-07-20T08:00:00Z",
      },
    }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(getCPAServerVersionStatus()).resolves.toMatchObject({
      current_version: "v7.2.92",
      latest_version: "v7.2.93",
      current_build_date: "2026-07-20T08:00:00Z",
      update_available: true,
      release_url: "https://github.com/router-for-me/CLIProxyAPI/releases/tag/v7.2.93",
    });
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("https://cpa.example/v0/management/latest-version");
    expect(new Headers(init.headers).get("Authorization")).toBe("Bearer management-secret");
  });

  it("compares CPA semantic versions including prereleases without treating build metadata as newer", () => {
    expect(compareCPAServerVersions("v7.2.92", "v7.2.93")).toBe(-1);
    expect(compareCPAServerVersions("v7.2.93-rc.1", "v7.2.93")).toBe(-1);
    expect(compareCPAServerVersions("v7.2.93", "v7.2.93-rc.2")).toBe(1);
    expect(compareCPAServerVersions("v7.2.93+build.1", "7.2.93+build.2")).toBe(0);
    expect(compareCPAServerVersions("dev", "v7.2.93")).toBeNull();
  });

  it("keeps the current CPA version but never exposes a failed latest-version response body", async () => {
    setSession("", "management-secret");
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ message: "upstream token secret-value" }), {
      status: 502,
      headers: { "Content-Type": "application/json", "X-CPA-Version": "v7.2.92" },
    })));

    const status = await getCPAServerVersionStatus();
    expect(status).toMatchObject({ current_version: "v7.2.92", update_available: false, error: "latest_version_unavailable" });
    expect(JSON.stringify(status)).not.toContain("secret-value");
  });

  it("uses separate fixed routes for quick native and full server inspection", async () => {
    setSession("", "management-secret");
    const snapshot = { policy: {}, running: false, pending: true, last_run: {}, total: 0, action_count: 0 };
    const fetchMock = vi.fn().mockImplementation(async () => jsonResponse(snapshot));
    vi.stubGlobal("fetch", fetchMock);

    await scanNativeInspection();
    await scanFullInspection();

    expect(String(fetchMock.mock.calls[0][0])).toContain("/inspection/scan/native");
    expect(String(fetchMock.mock.calls[1][0])).toMatch(/\/inspection\/scan$/);
    expect((fetchMock.mock.calls[0][1] as RequestInit).method).toBe("POST");
    expect((fetchMock.mock.calls[1][1] as RequestInit).method).toBe("POST");
  });

  it("posts current notification form values to the fixed authenticated preview and test routes", async () => {
    setSession("https://cpa.example", "management-secret");
    const preview = {
      scenario: "available_accounts_low", event: "available_accounts_low",
      expanded_url: "https://notify.example/publish?message=0", variables: { available_accounts: "0" }, triggered_at: "2026-07-24T08:00:00Z",
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(preview))
      .mockResolvedValueOnce(jsonResponse({ preview, delivered: true, status_code: 204, attempts: 1, reason_code: "notification_delivered" }));
    vi.stubGlobal("fetch", fetchMock);
    const request = {
      url_template: "https://notify.example/publish?message=${available_accounts}",
      scenario: "available_accounts_low" as const,
      threshold_percent: 55,
      available_accounts_threshold: 3,
      availability_percent_threshold: 25,
    };

    await expect(previewInspectionNotification(request)).resolves.toEqual(preview);
    await expect(testInspectionNotification(request)).resolves.toMatchObject({ delivered: true, status_code: 204 });

    expect(fetchMock).toHaveBeenCalledTimes(2);
    const [previewURL, previewInit] = fetchMock.mock.calls[0] as [string, RequestInit];
    const [testURL, testInit] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(previewURL).toBe("https://cpa.example/v0/management/plugins/cpa-account-config-manager/inspection/notification/preview");
    expect(testURL).toBe("https://cpa.example/v0/management/plugins/cpa-account-config-manager/inspection/notification/test");
    expect(previewInit.method).toBe("POST");
    expect(testInit.method).toBe("POST");
    expect(JSON.parse(String(previewInit.body))).toEqual(request);
    expect(JSON.parse(String(testInit.body))).toEqual(request);
    expect(new Headers(previewInit.headers).get("Authorization")).toBe("Bearer management-secret");
    expect(new Headers(testInit.headers).get("Authorization")).toBe("Bearer management-secret");
  });

  it("rejects a malformed account list instead of presenting it as an empty pool", async () => {
    setSession("", "management-secret");
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ accounts: null, total: 0, page: 1, page_size: 50, pages: 0 }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(listAccounts(1, 50, {})).rejects.toMatchObject({ status: 502, message: "ui.invalid_accounts_response" });
  });

  it("rejects missing pagination and summary fields instead of inventing zero values", async () => {
    setSession("", "management-secret");
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ accounts: [] }))
      .mockResolvedValueOnce(jsonResponse({ results: [], summary: {} }))
      .mockResolvedValueOnce(jsonResponse({ operations: [], summary: {} }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(listAccounts(1, 50, {})).rejects.toMatchObject({ status: 502, message: "ui.invalid_accounts_response" });
    await expect(listInspectionResults(1, 50)).rejects.toMatchObject({ status: 502, message: "ui.invalid_api_response" });
    await expect(listOperations(1)).rejects.toMatchObject({ status: 502, message: "ui.invalid_api_response" });
  });

  it("accepts legacy inspection rows and runtime health values without silently dropping them", async () => {
    setSession("", "management-secret");
    const summary = {
      actionable: 0, suggested_delete: 0, suggested_disable: 0, suggested_enable: 0,
      reauth: 0, deletable_reauth: 0, review: 0, keep: 1, handled: 0,
      editable_enabled: 1, editable_disabled: 0,
    };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({
      results: [{ id: "legacy-1", health: "provider_unhealthy" }],
      summary,
      total: 1,
      page: 1,
      page_size: 50,
      pages: 1,
    })));

    await expect(listInspectionResults(1, 50)).resolves.toMatchObject({
      total: 1,
      results: [{ id: "legacy-1", health: "provider_unhealthy" }],
    });
  });

  it("normalizes nullable inspection and operation lists from older backends", async () => {
    setSession("", "management-secret");
    const inspectionSummary = {
      actionable: 0, suggested_delete: 0, suggested_disable: 0, suggested_enable: 0,
      reauth: 0, deletable_reauth: 0, review: 0, keep: 0, handled: 0,
      editable_enabled: 0, editable_disabled: 0,
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ results: null, summary: inspectionSummary, total: 0, page: 1, page_size: 50, pages: 0 }))
      .mockResolvedValueOnce(jsonResponse({ actions: null }))
      .mockResolvedValueOnce(jsonResponse({
        operations: null,
        summary: { total: 0, running: 0, succeeded: 0, failed: 0, attention: 0, interrupted: 0 },
        total: 0,
        page: 1,
        page_size: 500,
        pages: 0,
        extended_history: false,
        archived_segments: 0,
        retention_limit: 500,
        retained: 0,
      }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(listInspectionResults(1, 50)).resolves.toMatchObject({ results: [] });
    await expect(listInspectionActions()).resolves.toEqual([]);
    await expect(listOperations(1)).resolves.toMatchObject({
      operations: [], page_size: 500, extended_history: false, archived_segments: 0, retention_limit: 500, retained: 0,
    });
  });

  it("rejects malformed feature responses instead of presenting false empty states", async () => {
    setSession("", "management-secret");
    const operationSummary = { total: 0, running: 0, succeeded: 0, failed: 0, attention: 0, interrupted: 0 };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({}))
      .mockResolvedValueOnce(jsonResponse({ total: 1, eligible: 1, loaded: 1, failed: 0, read_only: 0, missing: 0 }))
      .mockResolvedValueOnce(jsonResponse({ policy: {}, last_run: {} }))
      .mockResolvedValueOnce(jsonResponse({ summary: {}, total: 0, page: 1, page_size: 50, pages: 0 }))
      .mockResolvedValueOnce(jsonResponse({}))
      .mockResolvedValueOnce(jsonResponse({ summary: operationSummary, total: 0, page: 1, pages: 0 }))
      .mockResolvedValueOnce(jsonResponse({ state: "running", running: true, total: 1, imported: 0, skipped: 0, failed: 0 }));
    vi.stubGlobal("fetch", fetchMock);

    const invalid = { status: 502, message: "ui.invalid_api_response" };
    await expect(loadAccountConfig("auth-1")).rejects.toMatchObject(invalid);
    await expect(loadAccountModels({ mode: "selected", ids: ["auth-1"] })).rejects.toMatchObject(invalid);
    await expect(getLiveInspection()).rejects.toMatchObject(invalid);
    await expect(listInspectionResults(1, 50)).rejects.toMatchObject(invalid);
    await expect(listInspectionActions()).rejects.toMatchObject(invalid);
    await expect(listOperations(1)).rejects.toMatchObject(invalid);
    await expect(getImportStatus()).rejects.toMatchObject(invalid);
  });

  it("uses fixed operation pages and persists extended-history settings", async () => {
    setSession("", "management-secret");
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({
        operations: [], summary: { total: 0, running: 0, succeeded: 0, failed: 0, attention: 0, interrupted: 0 },
        total: 0, page: 2, page_size: 500, pages: 0, extended_history: false, archived_segments: 0, retention_limit: 500, retained: 0,
      }))
			.mockResolvedValueOnce(jsonResponse({ status: "ok" }))
      .mockResolvedValueOnce(jsonResponse({ extended_history: true, page_size: 500, retained: 500, archived_segments: 0 }));
    vi.stubGlobal("fetch", fetchMock);

    await listOperations(2, { category: "inspection" });
    const [listURL] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(listURL).toContain("page=2");
    expect(listURL).toContain("page_size=500");
    expect(listURL).toContain("category=inspection");

    await expect(saveOperationRetentionSettings(true)).resolves.toMatchObject({ extended_history: true, page_size: 500 });
		const [configURL, configInit] = fetchMock.mock.calls[1] as [string, RequestInit];
		expect(configURL).toContain("/plugins/cpa-account-config-manager/config");
		expect(configInit.method).toBe("PATCH");
		expect(JSON.parse(String(configInit.body))).toEqual({ operation_settings: { extended_history: true } });

    const [settingsURL, settingsInit] = fetchMock.mock.calls[2] as [string, RequestInit];
    expect(settingsURL).toContain("/operations/settings");
    expect(settingsInit.method).toBe("PUT");
    expect(JSON.parse(String(settingsInit.body))).toEqual({ extended_history: true });
  });

  it("cancels an in-flight operation-log request from its caller", async () => {
    setSession("", "management-secret");
    const fetchMock = vi.fn((_input: RequestInfo | URL, init: RequestInit = {}) => new Promise<Response>((_resolve, reject) => {
      init.signal?.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), { once: true });
    }));
    vi.stubGlobal("fetch", fetchMock);
    const controller = new AbortController();
    const pending = listOperations(1, {}, controller.signal);
    controller.abort();
    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    const [, init] = fetchMock.mock.calls[0] as [RequestInfo | URL, RequestInit];
    expect(init.signal?.aborted).toBe(true);
  });

  it("sends selected scope and patch values only in the authenticated request", async () => {
    setSession("", "management-secret");
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      id: "preview-1",
      created_at: "2026-07-15T00:00:00Z",
      expires_at: "2026-07-15T00:05:00Z",
      scope_mode: "selected",
      total: 1,
      eligible: 1,
      read_only: 0,
      missing: 0,
      physical_files: 1,
      providers: { codex: 1 },
      patch: { fields: ["headers"], header_set: ["Authorization"], proxy_mutation: false },
      targets: [],
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);

    await createPreview(
      { mode: "selected", ids: ["auth-1"] },
      { headers: { set: { Authorization: "Bearer upstream-secret" } } },
    );

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    const body = JSON.parse(String(init.body));
    expect(body.scope).toEqual({ mode: "selected", ids: ["auth-1"] });
    expect(body.patch.headers.set.Authorization).toBe("Bearer upstream-secret");
  });

  it("creates and starts an authenticated single-account delete preview", async () => {
    setSession("", "management-secret");
    const previewBody = {
      id: "delete-preview-1",
      created_at: "2026-07-15T00:00:00Z",
      expires_at: "2026-07-15T00:05:00Z",
      account: { id: "auth-1", name: "operator.json", provider: "codex" },
    };
    const resultBody = {
      status: "deleted",
      deleted_at: "2026-07-15T00:00:01Z",
      account: previewBody.account,
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(previewBody))
      .mockResolvedValueOnce(jsonResponse(resultBody));
    vi.stubGlobal("fetch", fetchMock);

    await createAccountDeletePreview("auth-1");
    await deleteAccount("delete-preview-1");

    const [previewURL, previewInit] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(previewURL).toContain("/accounts/delete/preview");
    expect(JSON.parse(String(previewInit.body))).toEqual({ id: "auth-1" });
    expect(new Headers(previewInit.headers).get("Authorization")).toBe("Bearer management-secret");

    const [startURL, startInit] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(startURL).toContain("/accounts/delete/start");
    expect(JSON.parse(String(startInit.body))).toEqual({ preview_id: "delete-preview-1" });
    expect(new Headers(startInit.headers).get("Authorization")).toBe("Bearer management-secret");
  });

  it("creates selected and filtered batch-delete previews and starts only with explicit confirmation", async () => {
    setSession("", "management-secret");
    const response = {
      operation: "delete",
      id: "batch-delete-preview-1",
      created_at: "2026-07-23T00:00:00Z",
      expires_at: "2026-07-23T00:05:00Z",
      scope_mode: "selected",
      total: 2,
      eligible: 2,
      read_only: 0,
      missing: 0,
      physical_files: 2,
      providers: { codex: 2 },
      patch: { fields: [], proxy_mutation: false },
      targets: [],
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(response))
      .mockResolvedValueOnce(jsonResponse({ ...response, id: "batch-delete-preview-2", scope_mode: "filtered" }))
      .mockResolvedValueOnce(jsonResponse({
        id: "delete-job-1", operation: "delete", state: "running", running: true,
        total: 2, eligible: 2, done: 0, succeeded: 0, failed: 0, conflicts: 0, skipped: 0,
        workers: 2, patch: { fields: [], proxy_mutation: false }, retry_available: false, persisted: false,
      }, 202));
    vi.stubGlobal("fetch", fetchMock);

    await createBatchDeletePreview({ mode: "selected", ids: ["auth-1", "auth-2"] });
    await createBatchDeletePreview({ mode: "filtered", filters: { provider: "codex", disabled: true } });
    await startBatchDelete("batch-delete-preview-1");

    expect(String(fetchMock.mock.calls[0][0])).toContain("/batch/delete/preview");
    expect(JSON.parse(String((fetchMock.mock.calls[0][1] as RequestInit).body))).toEqual({ scope: { mode: "selected", ids: ["auth-1", "auth-2"] } });
    expect(JSON.parse(String((fetchMock.mock.calls[1][1] as RequestInit).body))).toEqual({ scope: { mode: "filtered", filters: { provider: "codex", disabled: true } } });
    expect(String(fetchMock.mock.calls[2][0])).toContain("/batch/delete/start");
    expect(JSON.parse(String((fetchMock.mock.calls[2][1] as RequestInit).body))).toEqual({ preview_id: "batch-delete-preview-1", confirm: true });
  });

  it("requests a redacted account-deduplication preview through the authenticated fixed route", async () => {
    setSession("https://cpa.example", "management-secret");
    const preview = {
      scanned_credentials: 2, identified_credentials: 2, duplicate_groups: 1,
      duplicate_credentials: 1, proposed_deletions: 1, read_only_skipped: 0,
      excluded_credentials: 0, missing_identity: 0,
      options: { ignore_account_id: true, exclude_team_accounts: false }, groups: [],
    };
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(preview));
    vi.stubGlobal("fetch", fetchMock);

    await expect(scanAccountDuplicates({ ignore_account_id: true, exclude_team_accounts: false })).resolves.toEqual(preview);

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("https://cpa.example/v0/management/plugins/cpa-account-config-manager/accounts/deduplicate/preview");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({ ignore_account_id: true, exclude_team_accounts: false });
    expect(new Headers(init.headers).get("Authorization")).toBe("Bearer management-secret");
  });

  it("retries failed batch-delete items through the shared batch retry route", async () => {
    setSession("", "management-secret");
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({
      id: "delete-retry-job-1", parent_job_id: "delete-job-1", operation: "delete",
      state: "running", running: true, total: 1, eligible: 1, done: 0, succeeded: 0,
      failed: 0, conflicts: 0, skipped: 0, workers: 1,
      patch: { fields: [], proxy_mutation: false }, retry_available: false, persisted: false,
    }, 202));
    vi.stubGlobal("fetch", fetchMock);

    await retryBatch();

    expect(String(fetchMock.mock.calls[0][0])).toContain("/batch/retry");
    expect((fetchMock.mock.calls[0][1] as RequestInit).method).toBe("POST");
  });

  it("submits only the account ID and model for a model availability test", async () => {
    setSession("", "management-secret");
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({
      account_id: "auth-1", provider: "codex", model: "gpt-5.4", status: "available",
      reason_code: "model_response_ok", latency_ms: 286, tested_at: "2026-07-20T08:00:00Z",
    }));
    vi.stubGlobal("fetch", fetchMock);

    await testAccountModel("auth-1", " gpt-5.4 ");

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toContain("/accounts/model-test");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({ account_id: "auth-1", model: "gpt-5.4" });
    expect(new Headers(init.headers).get("Authorization")).toBe("Bearer management-secret");
  });

  it("adds the weekly-overdraft flag only for an explicit experimental model test", async () => {
    setSession("", "management-secret");
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({
      account_id: "auth-1", provider: "codex", model: "gpt-5.4", status: "available",
      reason_code: "model_response_ok", latency_ms: 286, tested_at: "2026-07-22T08:00:00Z",
      experiment: { name: "weekly_overdraft", applied: true, call_id: "call_cpa_overdraft_test" },
    }));
    vi.stubGlobal("fetch", fetchMock);

    await testAccountModel("auth-1", "gpt-5.4", true);

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toEqual({
      account_id: "auth-1",
      model: "gpt-5.4",
      experimental_weekly_overdraft: true,
    });
  });

	it("preserves zero, false, and unmanaged null values in a default policy", async () => {
		setSession("", "management-secret");
		const responseBody = {
			policy: {
				enabled: true,
				new_account_model_probe_enabled: true,
				codex_quota_metadata_probe_enabled: true,
				apply_mode: "missing",
				scan_interval_seconds: 15,
				priority: 0,
				websockets: false,
			},
			running: false,
			last_scan: { scanned: 0, eligible: 0, changed: 0, skipped: 0, failed: 0 },
		};
		const fetchMock = vi.fn(async (input: RequestInfo | URL, _init: RequestInit = {}) => String(input).endsWith("/config")
			? jsonResponse({ status: "ok" })
			: jsonResponse(responseBody));
		vi.stubGlobal("fetch", fetchMock);

		await saveDefaultPolicy({
			enabled: true,
			new_account_model_probe_enabled: true,
			codex_quota_metadata_probe_enabled: true,
			apply_mode: "missing",
			scan_interval_seconds: 15,
			priority: 0,
			websockets: false,
		});

		const [configURL, configInit] = fetchMock.mock.calls[0] as [string, RequestInit];
		expect(configURL).toContain("/plugins/cpa-account-config-manager/config");
		expect(configInit.method).toBe("PATCH");
			expect(JSON.parse(String(configInit.body))).toEqual({ default_policy: {
				enabled: true,
				new_account_model_probe_enabled: true,
				codex_quota_metadata_probe_enabled: true,
			apply_mode: "missing",
			scan_interval_seconds: 15,
			priority: 0,
			websockets: false,
		} });

		const [policyURL, policyInit] = fetchMock.mock.calls[1] as [string, RequestInit];
		expect(policyURL).toContain("/defaults");
		expect(policyInit.method).toBe("PUT");
		expect(JSON.parse(String(policyInit.body))).toEqual({
			enabled: true,
			new_account_model_probe_enabled: true,
			codex_quota_metadata_probe_enabled: true,
			apply_mode: "missing",
			scan_interval_seconds: 15,
			priority: 0,
			websockets: false,
		});
	});

  it("uploads import bytes directly with authenticated metadata and confirms by preview id", async () => {
    setSession("", "management-secret");
    const previewBody = {
      id: "import-preview-1",
      created_at: "2026-07-15T00:00:00Z",
      expires_at: "2026-07-15T00:05:00Z",
      input_type: "zip",
      source_files: 1,
      total: 1,
      skipped: 0,
      items: [],
    };
    const resultBody = {
      id: "import-preview-1",
      state: "completed",
      total: 1,
      imported: 1,
      skipped: 0,
      failed: 0,
      started_at: "2026-07-15T00:00:01Z",
      finished_at: "2026-07-15T00:00:02Z",
      results: [],
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(previewBody))
      .mockResolvedValueOnce(jsonResponse(resultBody));
    vi.stubGlobal("fetch", fetchMock);
    const jsonFile = new File([`{"access_token":"json-secret"}`], "first.json", { type: "application/json" });
    const archive = new File(["PK\u0003\u0004raw-secret-bytes"], "账号 bundle.zip", { type: "application/zip" });

    await createImportPreview([jsonFile, archive]);
    await startImport("import-preview-1");

    const [previewURL, previewInit] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(previewURL).toContain("/import/preview");
    expect(previewInit.body).toBeInstanceOf(FormData);
    const files = (previewInit.body as FormData).getAll("files") as File[];
    expect(files.map((file) => file.name)).toEqual(["first.json", "账号 bundle.zip"]);
    const previewHeaders = new Headers(previewInit.headers);
    expect(previewHeaders.get("Authorization")).toBe("Bearer management-secret");
    expect(previewHeaders.get("Content-Type")).toBeNull();

    const [startURL, startInit] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(startURL).toContain("/import/start");
    expect(JSON.parse(String(startInit.body))).toEqual({ preview_id: "import-preview-1" });
  });

  it("downloads the selected credential target with current filters and account counts", async () => {
    setSession("", "management-secret");
    const fetchMock = vi.fn().mockResolvedValue(new Response("PK\u0003\u0004credential-archive", {
      status: 200,
      headers: {
        "Content-Type": "application/zip",
        "Content-Disposition": 'attachment; filename="cpa-accounts.zip"',
        "X-Exported-Accounts": "8",
        "X-Skipped-Accounts": "1",
      },
    }));
    vi.stubGlobal("fetch", fetchMock);
    const createObjectURL = vi.fn(() => "blob:export");
    const revokeObjectURL = vi.fn();
    vi.stubGlobal("URL", { ...URL, createObjectURL, revokeObjectURL });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);

    const result = await downloadExport("accounts", "cpa", { mode: "filtered", filters: { provider: "codex", type: "k12", disabled: false } });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toContain("/export/accounts?");
    expect(url).toContain("format=cpa");
    expect(url).toContain("provider=codex");
    expect(url).toContain("type=k12");
    expect(url).toContain("disabled=false");
    expect(new Headers(init.headers).get("Authorization")).toBe("Bearer management-secret");
    expect(createObjectURL).toHaveBeenCalledTimes(1);
    expect(click).toHaveBeenCalledTimes(1);
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:export");
    expect(result).toEqual({ filename: "cpa-accounts.zip", exported: 8, skipped: 1 });
  });

  it("posts selected account ids without placing them in the download URL", async () => {
    setSession("", "management-secret");
    const fetchMock = vi.fn().mockResolvedValue(new Response("{}", {
      status: 200,
      headers: {
        "Content-Type": "application/json",
        "Content-Disposition": 'attachment; filename="selected.json"',
        "X-Exported-Accounts": "1",
      },
    }));
    vi.stubGlobal("fetch", fetchMock);
    vi.stubGlobal("URL", { ...URL, createObjectURL: vi.fn(() => "blob:selected"), revokeObjectURL: vi.fn() });
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);

    await downloadExport("accounts", "cpa", { mode: "selected", ids: ["auth-2", "auth-1"] });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toContain("format=cpa");
    expect(url).not.toContain("auth-1");
    expect(init.method).toBe("POST");
    expect(new Headers(init.headers).get("Content-Type")).toBe("application/json");
    expect(JSON.parse(String(init.body))).toEqual({ scope: { mode: "selected", ids: ["auth-2", "auth-1"] } });
  });

  it("persists confirmed automation settings and installs an exact plugin-store version", async () => {
    setSession("", "management-secret");
    const inspectionSnapshot = {
      policy: {
        enabled: true, scan_interval_minutes: 30,
        model_probe_enabled: true, model_probe_full_sweep: true, scan_manually_disabled: true, model_probe_interval_minutes: 60, model_probe_batch_size: 20,
        model_probe_models: { codex: "gpt-5.4", openai: "gpt-5.4", claude: "claude-sonnet-4-5-20250929", gemini: "gemini-2.0-flash", xai: "grok-4" },
        failure_threshold: 3, recovery_threshold: 2, auto_disable: true, auto_enable: true,
        auto_delete: true, auto_delete_invalid_credentials: true, delete_grace_hours: 168, delete_batch_size: 10,
        anomaly_trigger_enabled: true, anomaly_threshold_percent: 50, anomaly_minimum_accounts: 10, anomaly_cooldown_minutes: 60,
        anomaly_notification_enabled: true, anomaly_notification_only: true, anomaly_notification_url: "https://notify.example/hook?available=${available_accounts}",
        notification_available_accounts_enabled: true, notification_available_accounts_threshold: 8,
        notification_availability_percent_enabled: true, notification_availability_percent_threshold: 35, notification_cooldown_minutes: 45,
      },
      running: false, pending: false, last_run: {}, total: 0, action_count: 0,
    };
    const updateSnapshot = {
      policy: { check_enabled: true, check_interval_hours: 24, auto_update: true },
      current_version: "0.2.0", latest_version: "0.3.0", update_available: true, checking: false, pending: false,
    };
    const fetchMock = vi.fn()
			.mockResolvedValueOnce(jsonResponse({ status: "ok" }))
      .mockResolvedValueOnce(jsonResponse(inspectionSnapshot))
			.mockResolvedValueOnce(jsonResponse({ status: "ok" }))
      .mockResolvedValueOnce(jsonResponse(updateSnapshot))
      .mockResolvedValueOnce(jsonResponse({ plugins_enabled: true, plugins: [{ id: "cpa-account-config-manager", version: "0.3.0", installed: true, installed_version: "0.2.0", update_available: true }] }))
      .mockResolvedValueOnce(jsonResponse({ attempted: 0, succeeded: 0, failed: 0, skipped: 0 }))
      .mockResolvedValueOnce(jsonResponse({ plugins_enabled: true, plugins: [{ id: "cpa-account-config-manager", version: "0.3.0", installed: true, installed_version: "0.2.0", update_available: true }] }))
      .mockResolvedValueOnce(jsonResponse({ status: "installed", id: "cpa-account-config-manager", version: "0.3.0", restart_required: false }));
    vi.stubGlobal("fetch", fetchMock);

    await saveInspectionPolicy(inspectionSnapshot.policy, true, true);
    await saveUpdatePolicy(updateSnapshot.policy, true);
    await executeInspectionAutoDelete();
		const installResult = await installPluginUpdate("0.3.0");
		expect(installResult.restart_required).toBe(false);

		const [inspectionConfigURL, inspectionConfigInit] = fetchMock.mock.calls[0] as [string, RequestInit];
		expect(inspectionConfigURL).toContain("/plugins/cpa-account-config-manager/config");
		expect(JSON.parse(String(inspectionConfigInit.body))).toEqual({ inspection_policy: inspectionSnapshot.policy });

    const [inspectionURL, inspectionInit] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(inspectionURL).toContain("/inspection");
    expect(JSON.parse(String(inspectionInit.body))).toEqual({ ...inspectionSnapshot.policy, confirm_auto_delete: true, confirm_delete_invalid_credentials: true });

		const [updateConfigURL, updateConfigInit] = fetchMock.mock.calls[2] as [string, RequestInit];
		expect(updateConfigURL).toContain("/plugins/cpa-account-config-manager/config");
		expect(JSON.parse(String(updateConfigInit.body))).toEqual({ update_policy: updateSnapshot.policy });

    const [updateURL, updateInit] = fetchMock.mock.calls[3] as [string, RequestInit];
    expect(updateURL).toContain("/updates");
    expect(JSON.parse(String(updateInit.body))).toEqual({ policy: updateSnapshot.policy, confirm_auto_update: true });

		const [policyStoreURL, policyStoreInit] = fetchMock.mock.calls[4] as [string, RequestInit];
    expect(policyStoreURL).toBe("/v0/management/plugin-store");
    expect(new Headers(policyStoreInit.headers).get("Authorization")).toBe("Bearer management-secret");

		const [deleteURL, deleteInit] = fetchMock.mock.calls[5] as [string, RequestInit];
    expect(deleteURL).toContain("/inspection/auto-delete");
    expect(deleteInit.body).toBeUndefined();

		const [storeURL, storeInit] = fetchMock.mock.calls[6] as [string, RequestInit];
    expect(storeURL).toBe("/v0/management/plugin-store");
    expect(new Headers(storeInit.headers).get("Authorization")).toBe("Bearer management-secret");

		const [installURL, installInit] = fetchMock.mock.calls[7] as [string, RequestInit];
    expect(installURL).toBe("/v0/management/plugin-store/cpa-account-config-manager/install");
    expect(JSON.parse(String(installInit.body))).toEqual({ version: "0.3.0" });
    expect(new Headers(installInit.headers).get("Authorization")).toBe("Bearer management-secret");
		await vi.waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThan(8));
		const [, operationInit] = fetchMock.mock.calls[8] as [string, RequestInit];
		expect(JSON.parse(String(operationInit.body))).toMatchObject({ action: "update_install", status: "succeeded", version: "0.3.0" });
    expect(localStorage.length).toBe(0);
  });

	it("migrates all current server settings into one CPA plugin-config patch", async () => {
		setSession("", "management-secret");
		const defaultPolicy = { enabled: true, apply_mode: "missing" as const, scan_interval_seconds: 15, priority: 0, websockets: false };
		const inspectionPolicy = {
			enabled: true, scan_interval_minutes: 30,
			model_probe_enabled: true, model_probe_full_sweep: true, scan_manually_disabled: true, model_probe_interval_minutes: 60, model_probe_batch_size: 20,
			model_probe_models: { codex: "gpt-5.4", openai: "gpt-5.4", claude: "claude-sonnet-4-5-20250929", gemini: "gemini-2.0-flash", xai: "grok-4" },
			failure_threshold: 3, recovery_threshold: 2, passive_circuit_enabled: true, passive_failure_threshold: 5,
			passive_failure_window_minutes: 180, passive_circuit_minutes: 15, auto_disable: true, auto_enable: true,
			auto_delete: false, auto_delete_invalid_credentials: false, delete_grace_hours: 168, delete_batch_size: 10,
			anomaly_trigger_enabled: true, anomaly_threshold_percent: 50, anomaly_minimum_accounts: 10, anomaly_cooldown_minutes: 60,
			anomaly_notification_enabled: true, anomaly_notification_only: true, anomaly_notification_url: "https://notify.example/hook?available=${available_accounts}",
			notification_available_accounts_enabled: true, notification_available_accounts_threshold: 8,
			notification_availability_percent_enabled: true, notification_availability_percent_threshold: 35, notification_cooldown_minutes: 45,
		};
		const updatePolicy = { check_enabled: true, check_interval_hours: 12, auto_update: true };
		const fetchMock = vi.fn(async (input: RequestInfo | URL, _init: RequestInit = {}) => {
			const url = String(input);
			if (url.endsWith("/defaults")) return jsonResponse({ policy: defaultPolicy });
			if (url.endsWith("/inspection")) return jsonResponse({ policy: inspectionPolicy });
			if (url.endsWith("/updates")) return jsonResponse({ policy: updatePolicy });
			if (url.endsWith("/operations/settings")) return jsonResponse({ extended_history: true, page_size: 500, retained: 500, archived_segments: 0 });
			if (url.endsWith("/experiments")) return jsonResponse({ settings: { weekly_overdraft_enabled: true, agent_identity_enabled: true, auto_model_whitelist_enabled: true, sub2api_credit_usage_enabled: true, codex_identity: { outbound_convergence_enabled: false, ingress_gate_enabled: false, allow_app_server_clients: false } } });
			if (url.endsWith("/config")) return jsonResponse({ status: "ok" });
			return jsonResponse({}, 404);
		});
		vi.stubGlobal("fetch", fetchMock);

		await persistCurrentSettings();

		const configCall = fetchMock.mock.calls.find(([input]) => String(input).endsWith("/config"));
		expect(configCall).toBeDefined();
		const [, configInit] = configCall as [RequestInfo | URL, RequestInit];
		expect(configInit.method).toBe("PATCH");
		expect(JSON.parse(String(configInit.body))).toEqual({
			default_policy: defaultPolicy,
			inspection_policy: inspectionPolicy,
			update_policy: updatePolicy,
			operation_settings: { extended_history: true },
			experimental_settings: { weekly_overdraft_enabled: true, agent_identity_enabled: true, auto_model_whitelist_enabled: true, sub2api_credit_usage_enabled: true, codex_identity: { outbound_convergence_enabled: false, ingress_gate_enabled: false, allow_app_server_clients: false } },
		});
		expect(String(configInit.body)).not.toContain("management-secret");
	});

	it("migrates current settings only once per CPA base URL in a browser session", async () => {
		setSession("https://cpa.example", "management-secret");
		const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
			const url = String(input);
			if (url.endsWith("/defaults")) return jsonResponse({ policy: {} });
			if (url.endsWith("/inspection")) return jsonResponse({ policy: {} });
			if (url.endsWith("/updates")) return jsonResponse({ policy: {} });
			if (url.endsWith("/operations/settings")) return jsonResponse({ extended_history: false });
			if (url.endsWith("/experiments")) return jsonResponse({ settings: { weekly_overdraft_enabled: false } });
			if (url.endsWith("/config")) return jsonResponse({ status: "ok" });
			return jsonResponse({}, 404);
		});
		vi.stubGlobal("fetch", fetchMock);

		await persistCurrentSettingsOnce();
		await persistCurrentSettingsOnce();

		expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith("/config"))).toHaveLength(1);
		expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith("/experiments"))).toHaveLength(2);
	});

	it("stops a settings save when CPA plugin-config persistence fails", async () => {
		setSession("", "management-secret");
		const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ error: "save failed" }, 500));
		vi.stubGlobal("fetch", fetchMock);

		await expect(saveOperationRetentionSettings(true)).rejects.toMatchObject({ message: "ui.settings_persistence_failed" });
		expect(fetchMock).toHaveBeenCalledTimes(1);
	});

	it("continues feature-specific settings saves when an older CPA host lacks /config", async () => {
		setSession("", "management-secret");
		const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
			const url = String(input);
			if (url.endsWith("/config")) return jsonResponse({ error: "not found" }, 404);
			if (url.endsWith("/operations/settings")) {
				if (init.method === "PUT") return jsonResponse({ extended_history: true });
				return jsonResponse({ extended_history: false });
			}
			return jsonResponse({});
		});
		vi.stubGlobal("fetch", fetchMock);

		await expect(saveOperationRetentionSettings(true)).resolves.toEqual({ extended_history: true });
		expect(fetchMock.mock.calls.map(([input]) => String(input))).toEqual([
			"/v0/management/plugins/cpa-account-config-manager/config",
			"/v0/management/plugins/cpa-account-config-manager/operations/settings",
		]);
	});

  it("deduplicates concurrent installs of the same plugin version", async () => {
    setSession("", "management-secret");
    let releaseInstall: ((response: Response) => void) | undefined;
    const installResponse = new Promise<Response>((resolve) => { releaseInstall = resolve; });
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url === "/v0/management/plugin-store") {
        return jsonResponse({ plugins_enabled: true, plugins: [{ id: "cpa-account-config-manager", version: "0.3.0", installed: true }] });
      }
      if (url.endsWith("/plugin-store/cpa-account-config-manager/install")) return installResponse;
      return jsonResponse({ recorded: true });
    });
    vi.stubGlobal("fetch", fetchMock);

    const first = installPluginUpdate("0.3.0");
    const second = installPluginUpdate("v0.3.0");
    await vi.waitFor(() => expect(fetchMock.mock.calls.some(([input]) => String(input).endsWith("/install"))).toBe(true));
    releaseInstall?.(jsonResponse({ status: "installed", id: "cpa-account-config-manager", version: "0.3.0", restart_required: false }));

    await expect(Promise.all([first, second])).resolves.toEqual([
      expect.objectContaining({ version: "0.3.0" }),
      expect.objectContaining({ version: "0.3.0" }),
    ]);
    expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith("/install"))).toHaveLength(1);
  });

  it("preserves the stable restart-required plugin-store error code", async () => {
    setSession("", "management-secret");
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ plugins_enabled: true, plugins: [{ id: "cpa-account-config-manager", version: "0.3.0", installed: true, installed_version: "0.2.0", update_available: true }] }))
      .mockResolvedValueOnce(jsonResponse({ error: "plugin_update_requires_restart", message: "loaded plugin cannot be overwritten while running" }, 409));
    vi.stubGlobal("fetch", fetchMock);

    await expect(installPluginUpdate("0.3.0")).rejects.toMatchObject({
      status: 409,
      message: "plugin_update_requires_restart",
    });
  });

  it("normalizes restart outcomes and records the completed install before returning", async () => {
    setSession("", "management-secret");
    for (const test of [
      { name: "restart required", response: { status: "installed", id: "cpa-account-config-manager", version: "v0.3.0", restart_required: true }, wantRestart: true, wantStatus: "warning" },
      { name: "legacy response without restart flag", response: { status: "installed", id: "cpa-account-config-manager", version: "0.3.0" }, wantRestart: false, wantStatus: "succeeded" },
    ]) {
      const fetchMock = vi.fn()
        .mockResolvedValueOnce(jsonResponse({ plugins_enabled: true, plugins: [{ id: "cpa-account-config-manager", version: "0.3.0", installed: true, installed_version: "0.2.0", update_available: true }] }))
        .mockResolvedValueOnce(jsonResponse(test.response))
        .mockResolvedValueOnce(jsonResponse({}));
      vi.stubGlobal("fetch", fetchMock);

      const result = await installPluginUpdate("v0.3.0");

      expect(result, test.name).toEqual({ status: "installed", id: "cpa-account-config-manager", version: "0.3.0", restart_required: test.wantRestart });
      expect(fetchMock, test.name).toHaveBeenCalledTimes(3);
      expect(JSON.parse(String((fetchMock.mock.calls[1] as [string, RequestInit])[1].body))).toEqual({ version: "0.3.0" });
      expect(JSON.parse(String((fetchMock.mock.calls[2] as [string, RequestInit])[1].body))).toMatchObject({
        action: "update_install", status: test.wantStatus, version: "0.3.0",
      });
    }
  });

  it("rejects a plugin-store response without the enabled flag", async () => {
    setSession("", "management-secret");
    vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce(jsonResponse({ plugins: [{ id: "cpa-account-config-manager", version: "0.3.0" }] })));

    await expect(getPluginStore()).rejects.toMatchObject({ status: 502, message: "ui.invalid_api_response" });
  });

  it("rejects malformed plugin-store rows instead of treating the plugin as absent", async () => {
    setSession("", "management-secret");
    const fetchMock = vi.fn().mockResolvedValueOnce(jsonResponse({
      plugins: [
        { id: "cpa-account-config-manager", version: "0.3.0" },
        "invalid-entry",
        null,
      ],
    }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(getPluginStore()).rejects.toMatchObject({ status: 502, message: "ui.invalid_api_response" });
  });

  it("rejects unverified versions and malformed plugin-store install responses", async () => {
    setSession("", "management-secret");
    const mismatchedStore = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ plugins_enabled: true, plugins: [{ id: "cpa-account-config-manager", version: "0.3.1", installed: true, installed_version: "0.2.0", update_available: true }] }))
      .mockResolvedValueOnce(jsonResponse({}));
    vi.stubGlobal("fetch", mismatchedStore);
    await expect(installPluginUpdate("0.3.0")).rejects.toMatchObject({ status: 404 });
    expect(mismatchedStore).toHaveBeenCalledTimes(2);
    expect(String(mismatchedStore.mock.calls[1][0])).toContain("/operations/record");

    const malformedInstall = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ plugins_enabled: true, plugins: [{ id: "cpa-account-config-manager", version: "0.3.0", installed: true, installed_version: "0.2.0", update_available: true }] }))
      .mockResolvedValueOnce(jsonResponse({ status: "installed", id: "another-plugin", version: "0.3.0", restart_required: false }))
      .mockResolvedValueOnce(jsonResponse({}));
    vi.stubGlobal("fetch", malformedInstall);
    await expect(installPluginUpdate("0.3.0")).rejects.toMatchObject({ status: 502, message: "plugin store install response was invalid" });
    expect(JSON.parse(String((malformedInstall.mock.calls[2] as [string, RequestInit])[1].body))).toMatchObject({
      action: "update_install", status: "failed", version: "0.3.0",
    });
  });

  it("uses authenticated plugin-store metadata as the sole update source", async () => {
    setSession("", "management-secret");
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ policy: { check_enabled: true, check_interval_hours: 24, auto_update: false }, current_version: "0.2.3", update_available: false, checking: false, pending: false, error: "release metadata request failed" }))
      .mockResolvedValueOnce(jsonResponse({ plugins_enabled: true, plugins: [{ id: "cpa-account-config-manager", version: "0.2.4", installed: true, installed_version: "0.2.3", update_available: true }] }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await getEffectiveUpdateStatus();

    expect(result).toMatchObject({
      current_version: "0.2.3",
      latest_version: "0.2.4",
      update_available: true,
      release_source: "plugin_store",
    });
    expect(result.error).toBeUndefined();
    expect(result).not.toHaveProperty("github_error");
    expect(new Headers((fetchMock.mock.calls[1] as [string, RequestInit])[1].headers).get("Authorization")).toBe("Bearer management-secret");
    expect(localStorage.length).toBe(0);
  });

  it("shares update status requests across lifecycle-bound callers without sharing aborts", async () => {
    setSession("", "management-secret");
    let releaseStatus!: () => void;
    const statusReady = new Promise<void>((resolve) => { releaseStatus = resolve; });
    const fetchMock = vi.fn()
      .mockImplementationOnce(() => statusReady.then(() => jsonResponse({
        policy: { check_enabled: true, check_interval_hours: 24, auto_update: false },
        current_version: "0.3.0", update_available: false, checking: false, pending: false,
      })))
      .mockResolvedValueOnce(jsonResponse({ plugins_enabled: true, plugins: [{
        id: "cpa-account-config-manager", version: "0.3.0", installed: true, installed_version: "0.3.0",
      }] }));
    vi.stubGlobal("fetch", fetchMock);

    const firstController = new AbortController();
    const first = getEffectiveUpdateStatus(false, firstController.signal);
    const second = getEffectiveUpdateStatus(false, new AbortController().signal);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    firstController.abort();
    releaseStatus();

    await expect(first).rejects.toMatchObject({ name: "AbortError" });
    await expect(second).resolves.toMatchObject({ current_version: "0.3.0", release_source: "plugin_store" });
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("uses the store as the source without inventing an update when versions match", () => {
    const result = reconcileUpdateStatus({
      policy: { check_enabled: true, check_interval_hours: 24, auto_update: false },
      current_version: "0.2.3", update_available: false, checking: false, pending: false,
      error: "release metadata request failed",
    }, { plugins_enabled: true, plugins: [{ id: "cpa-account-config-manager", version: "v0.2.3", installed: true, installed_version: "0.2.3", update_available: false }] });

    expect(result).toMatchObject({ latest_version: "0.2.3", update_available: false, release_source: "plugin_store" });
    expect(result.error).toBeUndefined();
  });

  it("reports a stable plugin-store error when store metadata is missing or invalid", () => {
    const status = {
      policy: { check_enabled: true, check_interval_hours: 24, auto_update: false },
      current_version: "0.2.3", update_available: false, checking: false, pending: false,
      error: "release metadata request failed",
    };
    for (const store of [null, { plugins_enabled: true, plugins: null }, { plugins_enabled: true, plugins: [{ id: "cpa-account-config-manager", version: "latest", installed: true, installed_version: "0.2.3", update_available: true }] }]) {
      const result = reconcileUpdateStatus(status, store);
      expect(result.release_source).toBe("none");
      expect(result.error).toBe("plugin store metadata is unavailable");
      expect(result.update_available).toBe(false);
    }
  });

  it("ignores stale direct-release metadata when the plugin store has an older stable version", () => {
    const result = reconcileUpdateStatus({
      policy: { check_enabled: true, check_interval_hours: 24, auto_update: false },
      current_version: "0.2.3", latest_version: "9.9.9", update_available: true,
      release_url: "https://example.invalid/release", checking: false, pending: false,
    }, { plugins_enabled: true, plugins: [{ id: "cpa-account-config-manager", version: "0.2.4", installed: true, installed_version: "0.2.3", update_available: true }] });

    expect(result).toMatchObject({ latest_version: "0.2.4", update_available: true, release_source: "plugin_store" });
    expect(result.release_url).toBe("https://github.com/Mxucc/cpa-account-config-manager/releases/tag/v0.2.4");
  });

  it("rejects malformed account concurrency payloads instead of showing zero", async () => {
    setSession("https://cpa.example", "management-secret");
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({
      total: 1, page: 1, page_size: 50, pages: 1,
      account_concurrency: { supported: true, host_schema_version: "bad", required_schema_version: 2 },
      accounts: [],
    })));
    await expect(listAccounts(1, 50, {})).rejects.toMatchObject({ message: "ui.invalid_accounts_response" });
  });

  it("rejects malformed per-account concurrency and config payloads", async () => {
    setSession("https://cpa.example", "management-secret");
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ total: 1, page: 1, page_size: 50, pages: 1, accounts: [{ id: "a", concurrency: { supported: true, active: "bad", limit: 0 } }] }))
      .mockResolvedValueOnce(jsonResponse({
        account_id: "a", disabled: false, priority: null, note: "", prefix: "", proxy: "", proxy_configured: false,
        websockets: null, header_names: [], model_policy: null,
        account_concurrency: { supported: true, host_schema_version: 2, required_schema_version: 2 },
        concurrency: { supported: true, active: "bad", limit: 0 },
      }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(listAccounts(1, 50, {})).rejects.toMatchObject({ message: "ui.invalid_accounts_response" });
    await expect(loadAccountConfig("a")).rejects.toMatchObject({ message: "ui.invalid_api_response" });
  });

  it("defaults newly introduced rolling-window counters for legacy account payloads", async () => {
    setSession("https://cpa.example", "management-secret");
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({
      total: 1, page: 1, page_size: 50, pages: 1,
      accounts: [{ id: "a", concurrency: { supported: true, active: 2, limit: 10 } }],
    })));

    const response = await listAccounts(1, 50, {});
    expect(response.accounts[0].concurrency).toEqual({
      supported: true,
      active: 2,
      limit: 10,
      limit_15s: 0,
      used_60s: 0,
      used_15s: 0,
    });
  });

  it("defaults legacy provider windows and rejects malformed new provider counters", async () => {
    setSession("https://cpa.example", "management-secret");
    const runtime = {
      provider: "openai",
      identity: "provider-key",
      supported: true,
      active: 2,
      limit: 10,
      input_tokens: 100,
      output_tokens: 20,
      reasoning_tokens: 0,
      cached_tokens: 0,
      total_tokens: 120,
      amount_usd: 0.01,
      rated_requests: 1,
      unrated_requests: 0,
      updated_at: "2026-09-01T00:00:00Z",
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ snapshots: [runtime], updated_at: "2026-09-01T00:00:00Z" }))
      .mockResolvedValueOnce(jsonResponse({ snapshots: [{ ...runtime, used_15s: "bad" }], updated_at: "2026-09-01T00:00:00Z" }));
    vi.stubGlobal("fetch", fetchMock);

    const response = await getAIProviderRuntime();
    expect(response.snapshots[0]).toMatchObject({ limit_15s: 0, used_60s: 0, used_15s: 0 });
    await expect(getAIProviderRuntime()).rejects.toMatchObject({ message: "ui.invalid_api_response" });
  });

  it("rejects fractional model catalog counters and model rows without ids", async () => {
    setSession("https://cpa.example", "management-secret");
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ total: 1.5, eligible: 0, loaded: 0, failed: 0, read_only: 0, missing: 0, models: [] }))
      .mockResolvedValueOnce(jsonResponse({ total: 1, eligible: 1, loaded: 1, failed: 0, read_only: 0, missing: 0, models: [{ display_name: "missing id" }] }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(loadAccountModels({ mode: "selected", ids: ["a"] })).rejects.toMatchObject({ message: "ui.invalid_api_response" });
    await expect(loadAccountModels({ mode: "selected", ids: ["a"] })).rejects.toMatchObject({ message: "ui.invalid_api_response" });
  });

  it("rejects missing plugin-store lists and malformed OpenCode accounts", async () => {
    setSession("https://cpa.example", "management-secret");
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ plugins_enabled: true }))
      .mockResolvedValueOnce(jsonResponse({ accounts: [{ id: "a", workspace_id: "" }] }))
      .mockResolvedValueOnce(jsonResponse({ accounts: [{ id: "a", base_url: "https://example", key_set: "yes" }] }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(getPluginStore()).rejects.toMatchObject({ message: "ui.invalid_api_response" });
    await expect(listOpenCodeAccounts()).rejects.toMatchObject({ message: "ui.invalid_api_response" });
    await expect(listOpenCodeZenAccounts()).rejects.toMatchObject({ message: "ui.invalid_api_response" });
  });

  it("rejects malformed import status, runtime, and operation responses", async () => {
    setSession("https://cpa.example", "management-secret");
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ id: "i", state: "completed", running: false, started_at: "", finished_at: "", total: 1.2, imported: 0, skipped: 0, failed: 0, results: [] }))
      .mockResolvedValueOnce(jsonResponse({ snapshots: [{ provider: "p" }], updated_at: "now" }))
      .mockResolvedValueOnce(jsonResponse({ extended_history: true, page_size: 500, retained: 1.2, archived_segments: 0 }))
      .mockResolvedValueOnce(jsonResponse({ operation: { id: "x", status: "unknown" }, retained: 1 }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(getImportStatus()).rejects.toMatchObject({ message: "ui.invalid_api_response" });
    await expect(getAIProviderRuntime()).rejects.toMatchObject({ message: "ui.invalid_api_response" });
    await expect(getOperationRetentionSettings()).rejects.toMatchObject({ message: "ui.invalid_api_response" });
    await expect(clearOperations()).rejects.toMatchObject({ message: "ui.invalid_api_response" });
  });

});

function jsonResponse(body: unknown, status = 200): Response {
	return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

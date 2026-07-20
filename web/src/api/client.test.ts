import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  createAccountDeletePreview,
  createImportPreview,
  createPreview,
  deleteAccount,
  downloadExport,
  executeInspectionAutoDelete,
  installPluginUpdate,
  listAccounts,
  listInspectionActions,
  listInspectionResults,
  listOperations,
  saveDefaultPolicy,
  saveInspectionPolicy,
  saveUpdatePolicy,
  startImport,
  testAccountModel,
} from "./client";
import { _resetSessionForTest, setSession } from "../store/session";

describe("management API client", () => {
  beforeEach(() => {
    _resetSessionForTest();
    localStorage.clear();
    vi.restoreAllMocks();
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
    expect(new Headers(init.headers).get("Authorization")).toBe("Bearer management-secret");
    expect(localStorage.length).toBe(0);
  });

  it("normalizes nullable list payloads from older or malformed backends", async () => {
    setSession("", "management-secret");
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ accounts: null, total: 0, page: 1, page_size: 50, pages: 0 }))
      .mockResolvedValueOnce(jsonResponse({ results: null, total: 0, page: 1, page_size: 50, pages: 0 }))
      .mockResolvedValueOnce(jsonResponse({ actions: null }))
      .mockResolvedValueOnce(jsonResponse({
        operations: null,
        summary: { total: 0, running: 0, succeeded: 0, failed: 0, attention: 0, interrupted: 0 },
        total: 0,
        page: 1,
        page_size: 50,
        pages: 0,
      }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(listAccounts(1, 50, {})).resolves.toMatchObject({ accounts: [] });
    await expect(listInspectionResults(1, 50)).resolves.toMatchObject({ results: [] });
    await expect(listInspectionActions()).resolves.toEqual([]);
    await expect(listOperations(1, 50)).resolves.toMatchObject({ operations: [] });
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

	it("preserves zero, false, and unmanaged null values in a default policy", async () => {
		setSession("", "management-secret");
		const responseBody = {
			policy: {
				enabled: true,
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
      },
      running: false, pending: false, last_run: {}, total: 0, action_count: 0,
    };
    const updateSnapshot = {
      policy: { check_enabled: true, check_interval_hours: 24, auto_update: true },
      current_version: "0.2.0", latest_version: "0.3.0", update_available: true, checking: false, pending: false,
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(inspectionSnapshot))
      .mockResolvedValueOnce(jsonResponse(updateSnapshot))
      .mockResolvedValueOnce(jsonResponse({ attempted: 0, succeeded: 0, failed: 0, skipped: 0 }))
      .mockResolvedValueOnce(jsonResponse({ plugins_enabled: true, plugins: [{ id: "cpa-account-config-manager", version: "0.3.0", installed: true, installed_version: "0.2.0", update_available: true }] }))
      .mockResolvedValueOnce(jsonResponse({ status: "installed", id: "cpa-account-config-manager", version: "0.3.0", restart_required: false }));
    vi.stubGlobal("fetch", fetchMock);

    await saveInspectionPolicy(inspectionSnapshot.policy, true, true);
    await saveUpdatePolicy(updateSnapshot.policy, true);
    await executeInspectionAutoDelete();
    await installPluginUpdate("0.3.0");

    const [inspectionURL, inspectionInit] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(inspectionURL).toContain("/inspection");
    expect(JSON.parse(String(inspectionInit.body))).toEqual({ ...inspectionSnapshot.policy, confirm_auto_delete: true, confirm_delete_invalid_credentials: true });

    const [updateURL, updateInit] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(updateURL).toContain("/updates");
    expect(JSON.parse(String(updateInit.body))).toEqual({ policy: updateSnapshot.policy, confirm_auto_update: true });

    const [deleteURL, deleteInit] = fetchMock.mock.calls[2] as [string, RequestInit];
    expect(deleteURL).toContain("/inspection/auto-delete");
    expect(deleteInit.body).toBeUndefined();

    const [storeURL, storeInit] = fetchMock.mock.calls[3] as [string, RequestInit];
    expect(storeURL).toBe("/v0/management/plugin-store");
    expect(new Headers(storeInit.headers).get("Authorization")).toBe("Bearer management-secret");

    const [installURL, installInit] = fetchMock.mock.calls[4] as [string, RequestInit];
    expect(installURL).toBe("/v0/management/plugin-store/cpa-account-config-manager/install");
    expect(JSON.parse(String(installInit.body))).toEqual({ version: "0.3.0" });
    expect(new Headers(installInit.headers).get("Authorization")).toBe("Bearer management-secret");
    expect(localStorage.length).toBe(0);
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
});

function jsonResponse(body: unknown, status = 200): Response {
	return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

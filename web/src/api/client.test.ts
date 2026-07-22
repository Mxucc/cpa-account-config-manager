import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  createAccountDeletePreview,
  createImportPreview,
  createPreview,
  compareCPAServerVersions,
  deleteAccount,
  downloadExport,
  executeInspectionAutoDelete,
  getEffectiveUpdateStatus,
  getCPAServerVersionStatus,
  installPluginUpdate,
  listAccounts,
  listInspectionActions,
  listInspectionResults,
  listOperations,
	persistCurrentSettings,
  reconcileUpdateStatus,
  saveDefaultPolicy,
  saveInspectionPolicy,
  saveOperationRetentionSettings,
  saveUpdatePolicy,
  scanFullInspection,
  scanNativeInspection,
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
        page_size: 500,
        pages: 0,
        extended_history: false,
        archived_segments: 0,
        retention_limit: 500,
        retained: 0,
      }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(listAccounts(1, 50, {})).resolves.toMatchObject({ accounts: [] });
    await expect(listInspectionResults(1, 50)).resolves.toMatchObject({ results: [] });
    await expect(listInspectionActions()).resolves.toEqual([]);
    await expect(listOperations(1)).resolves.toMatchObject({
      operations: [], page_size: 500, extended_history: false, archived_segments: 0, retention_limit: 500, retained: 0,
    });
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
    await installPluginUpdate("0.3.0");

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
			if (url.endsWith("/experiments")) return jsonResponse({ settings: { weekly_overdraft_enabled: true } });
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
			experimental_settings: { weekly_overdraft_enabled: true },
		});
		expect(String(configInit.body)).not.toContain("management-secret");
	});

	it("stops a settings save when CPA plugin-config persistence fails", async () => {
		setSession("", "management-secret");
		const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ error: "save failed" }, 500));
		vi.stubGlobal("fetch", fetchMock);

		await expect(saveOperationRetentionSettings(true)).rejects.toMatchObject({ message: "ui.settings_persistence_failed" });
		expect(fetchMock).toHaveBeenCalledTimes(1);
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
});

function jsonResponse(body: unknown, status = 200): Response {
	return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

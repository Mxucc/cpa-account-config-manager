import { beforeEach, describe, expect, it, vi } from "vitest";
import { createImportPreview, createPreview, listAccounts, saveDefaultPolicy, startImport } from "./client";
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

    await listAccounts(2, 50, { provider: "codex", disabled: false, search: "operator" });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toContain("/accounts?");
    expect(url).toContain("provider=codex");
    expect(url).toContain("disabled=false");
    expect(url).toContain("page=2");
    expect(new Headers(init.headers).get("Authorization")).toBe("Bearer management-secret");
    expect(localStorage.length).toBe(0);
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
		const fetchMock = vi.fn().mockResolvedValue(jsonResponse(responseBody));
		vi.stubGlobal("fetch", fetchMock);

		await saveDefaultPolicy({
			enabled: true,
			apply_mode: "missing",
			scan_interval_seconds: 15,
			priority: 0,
			websockets: false,
		});

		const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
		expect(url).toContain("/defaults");
		expect(init.method).toBe("PUT");
		expect(JSON.parse(String(init.body))).toEqual({
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
});

function jsonResponse(body: unknown, status = 200): Response {
	return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

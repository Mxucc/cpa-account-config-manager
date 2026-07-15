import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPreview, listAccounts, saveDefaultPolicy } from "./client";
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
});

function jsonResponse(body: unknown, status = 200): Response {
	return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

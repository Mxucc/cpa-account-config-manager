import { afterEach, describe, expect, it, vi } from "vitest";
import { getLiveInspection, inspectionResultRows, listInspectionResults } from "./client";
import { _resetSessionForTest, setSession } from "../store/session";

const summary = {
  actionable: 0, suggested_delete: 0, suggested_disable: 0, suggested_enable: 0,
  reauth: 0, deletable_reauth: 0, review: 0, keep: 1, handled: 0,
  editable_enabled: 1, editable_disabled: 0,
};

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
}

describe("inspection rows a legacy server still sends", () => {
  afterEach(() => {
    _resetSessionForTest();
    vi.unstubAllGlobals();
  });

  it("keeps inspected rows and drops the unclassified placeholder", () => {
    const rows = inspectionResultRows([
      { id: "placeholder", health: "", reason_code: "", confidence: "", recommendation: "" },
      { id: "checked", health: "healthy" },
    ]);
    expect(rows).toEqual([{ id: "checked", health: "healthy" }]);
  });

  it("renders the results page instead of failing on an empty enum", async () => {
    setSession("", "management-secret");
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse({
          results: [
            { id: "placeholder", health: "", reason_code: "", confidence: "", recommendation: "", last_checked_at: "0001-01-01T00:00:00Z" },
            { id: "checked", health: "quota_limited", reason_code: "quota_exhausted" },
          ],
          summary,
          total: 2,
          page: 1,
          page_size: 50,
          pages: 1,
        }),
      ),
    );

    const list = await listInspectionResults(1, 50);
    expect(list.results).toEqual([{ id: "checked", health: "quota_limited", reason_code: "quota_exhausted" }]);
  });

  it("drops an unclassified row from the live view too", async () => {
    setSession("", "management-secret");
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse({
          policy: {},
          last_run: {},
          live_results: [
            { id: "placeholder", health: "" },
            { id: "mid-run", health: "review" },
          ],
        }),
      ),
    );

    const snapshot = await getLiveInspection();
    expect(snapshot.live_results).toEqual([{ id: "mid-run", health: "review" }]);
  });
});

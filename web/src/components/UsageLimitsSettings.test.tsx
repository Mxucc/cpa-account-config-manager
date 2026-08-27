import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { _resetSessionForTest, setSession } from "../store/session";
import { UsageLimitsSettings } from "./UsageLimitsSettings";

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
}

const loaded = {
  config: {
    enabled: true,
    total: { enabled: true, basis: "credit", amount_usd: 500 },
    models: [{ model: "gpt-5.5", within_total: false, rule: { enabled: true, basis: "credit", amount_usd: 100 } }],
  },
  credit_used_usd: 42.5,
  credit_model_used_usd: { "gpt-5.5": 12.25 },
  updated_at: "2026-08-27T08:00:00Z",
};

describe("UsageLimitsSettings", () => {
  beforeEach(() => {
    _resetSessionForTest();
    setSession("", "management-secret");
    vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init: RequestInit = {}) => {
      if (init.method === "PUT") return jsonResponse(loaded);
      return jsonResponse(loaded);
    }));
  });

  it("loads total and model credit limits, then saves edits", async () => {
    const onNotice = vi.fn();
    render(<UsageLimitsSettings onAPIError={() => undefined} onNotice={onNotice} />);

    expect(await screen.findByText("42.5000", { exact: false })).toBeInTheDocument();
    expect(screen.getByDisplayValue("gpt-5.5")).toBeInTheDocument();
    expect(screen.getAllByText("额度限额").length).toBeGreaterThan(0);

    fireEvent.click(screen.getByRole("button", { name: "保存设置" }));
    await waitFor(() => expect(onNotice).toHaveBeenCalledWith("用量限额已保存"));
  });
});

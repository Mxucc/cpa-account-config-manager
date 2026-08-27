import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { _resetSessionForTest, setSession } from "../store/session";
import { UsageLimitsSettings } from "./UsageLimitsSettings";

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
}

const loaded = {
  scope: { kind: "provider", id: "default" },
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
    expect(screen.getByText("总限额")).toBeInTheDocument();
    expect(screen.getByText("模型限额")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "保存设置" }));
    await waitFor(() => expect(onNotice).toHaveBeenCalledWith("用量限额已保存"));
  });


  it("shows percentage and quota windows for account limits", () => {
    const onChange = vi.fn();
    render(<UsageLimitsSettings
      scope={{ kind: "account", id: "auth-1" }}
      value={{
        enabled: true,
        total: { enabled: true, basis: "account", window: "five_hour", percent: 80 },
        models: [{ model: "gpt-5.4", within_total: true, rule: { enabled: true, basis: "account", window: "seven_day", percent: 90 } }],
      }}
      onChange={onChange}
      onAPIError={() => undefined}
      onNotice={() => undefined}
    />);

    expect(screen.getAllByText("账号额度百分比")).toHaveLength(2);
    expect(screen.getAllByText("额度窗口")).toHaveLength(2);
    expect(screen.getAllByText("限额百分比")).toHaveLength(2);
    fireEvent.change(screen.getAllByRole("combobox")[0], { target: { value: "credit" } });
    expect(onChange.mock.lastCall?.[0].total?.basis).toBe("credit");
  });

  it("hides percentage controls and normalizes provider rules to credit amounts", async () => {
    const invalidProvider = {
      ...loaded,
      config: {
        enabled: true,
        total: { enabled: true, basis: "account", window: "five_hour", percent: 80 },
        models: [{ model: "gpt-5.5", within_total: false, rule: { enabled: true, basis: "account", window: "seven_day", percent: 90 } }],
      },
    };
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, init: RequestInit = {}) => {
      if (init.method === "PUT") return jsonResponse(invalidProvider);
      return jsonResponse(invalidProvider);
    });
    vi.stubGlobal("fetch", fetchMock);
    const onNotice = vi.fn();
    render(<UsageLimitsSettings scope={{ kind: "provider", id: "openai" }} onAPIError={() => undefined} onNotice={onNotice} />);

    expect(await screen.findByText("42.5000", { exact: false })).toBeInTheDocument();
    expect(screen.queryByText("账号额度百分比")).not.toBeInTheDocument();
    expect(screen.queryByText("额度窗口")).not.toBeInTheDocument();
    expect(screen.getAllByText("限额金额")).toHaveLength(2);
    expect(screen.getAllByDisplayValue("0")).toHaveLength(2);

    fireEvent.click(screen.getByRole("button", { name: "保存设置" }));
    await waitFor(() => expect(onNotice).toHaveBeenCalledWith("用量限额已保存"));
    const putCall = fetchMock.mock.calls.find((call) => call[1]?.method === "PUT");
    expect(putCall).toBeDefined();
    if (!putCall) throw new Error("PUT request was not sent");
    const body = JSON.parse(String(putCall[1]?.body));
    expect(body.config.total.basis).toBe("credit");
    expect(body.config.models[0].rule.basis).toBe("credit");
  });
});

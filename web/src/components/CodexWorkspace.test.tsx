import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { _resetSessionForTest, setSession } from "../store/session";
import { CodexWorkspace } from "./CodexWorkspace";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

const fingerprintFields = [
  { key: "mode", group: "client", kind: "select", default: "off", value: "off", overridden: false, options: ["off", "device", "session", "full"] },
  { key: "user_agent", group: "client", kind: "text", default: "codex-tui/0.153.3 (Linux Unknown; x86_64) xterm-256color (codex-tui; 0.153.3)", value: "codex-tui/0.153.3 (Linux Unknown; x86_64) xterm-256color (codex-tui; 0.153.3)", overridden: false },
  // One field is already overridden so the badge path is exercised.
  { key: "originator", group: "client", kind: "text", default: "codex-tui", value: "custom-originator", overridden: true },
  { key: "install_prefix", group: "derivation", kind: "text", default: "sub2api:codex-install-id:v2:", value: "sub2api:codex-install-id:v2:", overridden: false },
  { key: "include_relationship_fields", group: "body", kind: "bool", default: "true", value: "true", overridden: false },
];

const modelRows = [
  { id: "gpt-5.4-codex", disabled: false, accounts: 2, channels: 1 },
  { id: "gpt-5.4", disabled: false, accounts: 1, channels: 1 },
];

interface CodexFetchMockOptions {
  disabled?: string[];
  fields?: Array<Record<string, unknown>>;
  overview?: Record<string, unknown>;
}

describe("CodexWorkspace", () => {
  beforeEach(() => {
    _resetSessionForTest();
    localStorage.clear();
    setSession("", "management-secret");
    vi.restoreAllMocks();
  });

  function codexFetchMock(options: CodexFetchMockOptions = {}) {
    const requests: Array<{ url: string; init: RequestInit }> = [];
    let disabled = [...(options.disabled ?? [])];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      requests.push({ url, init });
      if (url.endsWith("/codex/overview")) {
        return jsonResponse({
          overview: {
            accounts: 3,
            channels: 2,
            disabled_models: disabled.length,
            convergence_mode: "session",
            account_overrides: 1,
            provider_overrides: 2,
            fingerprint_overridden_fields: 1,
            model_control_active: disabled.length > 0,
            ...options.overview,
          },
        });
      }
      if (url.endsWith("/codex/models") && init.method === "PUT") {
        const body = JSON.parse(String(init.body ?? "{}")) as { disabled?: string[] };
        disabled = body.disabled ?? [];
        return jsonResponse({ models: modelRows.map((row) => ({ ...row, disabled: disabled.includes(row.id) })), disabled });
      }
      if (url.endsWith("/codex/models")) {
        return jsonResponse({ models: modelRows.map((row) => ({ ...row, disabled: disabled.includes(row.id) })), disabled });
      }
      if (url.endsWith("/codex/fingerprint") && init.method === "PUT") {
        const body = JSON.parse(String(init.body ?? "{}")) as { values?: Record<string, string> };
        const values = body.values ?? {};
        return jsonResponse({ profile: {
          overridden_fields: Object.keys(values).length,
          fields: fingerprintFields.map((field) => field.key in values
            ? { ...field, value: values[field.key] === "" ? field.default : values[field.key], overridden: values[field.key] !== "" }
            : field),
        } });
      }
      if (url.endsWith("/codex/fingerprint/reset")) {
        return jsonResponse({ profile: { overridden_fields: 0, fields: fingerprintFields.map((field) => ({ ...field, value: field.default, overridden: false })) } });
      }
      if (url.endsWith("/codex/fingerprint")) {
        return jsonResponse({ profile: { overridden_fields: 1, fields: options.fields ?? fingerprintFields } });
      }
      if (url.endsWith("/experiments") && init.method === "PUT") {
        return jsonResponse({ settings: {
          weekly_overdraft_enabled: true, agent_identity_enabled: false, auto_model_whitelist_enabled: true,
          sub2api_credit_usage_enabled: true,
          codex_identity: { outbound_convergence_enabled: true, convergence_mode: "session", ingress_gate_enabled: false, allow_app_server_clients: false },
        } });
      }
      if (url.endsWith("/experiments")) {
        return jsonResponse({ settings: {
          weekly_overdraft_enabled: false, agent_identity_enabled: false, auto_model_whitelist_enabled: true,
          sub2api_credit_usage_enabled: true,
          codex_identity: { outbound_convergence_enabled: false, ingress_gate_enabled: false, allow_app_server_clients: false },
        } });
      }
      return jsonResponse({});
    });
    vi.stubGlobal("fetch", fetchMock);
    return requests;
  }

  it("renders the three Codex tabs and switches to the matching panel", async () => {
    const user = userEvent.setup();
    codexFetchMock();

    render(<CodexWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const tablist = await screen.findByRole("tablist", { name: "Codex" });
    expect(within(tablist).getAllByRole("tab").map((tab) => tab.textContent)).toEqual(["总览", "模型与价格", "指纹配置"]);
    expect(within(tablist).getAllByRole("tab")[0]).toHaveAttribute("aria-selected", "true");

    await user.click(within(tablist).getByRole("tab", { name: "模型与价格" }));
    expect(await screen.findByRole("tabpanel", { name: "模型与价格" })).toBeInTheDocument();
    await user.click(within(tablist).getByRole("tab", { name: "指纹配置" }));
    expect(await screen.findByRole("tabpanel", { name: "指纹配置" })).toBeInTheDocument();
  });

  it("renders the overview counts and the effective convergence mode", async () => {
    codexFetchMock();

    render(<CodexWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const panel = await screen.findByRole("tabpanel", { name: "总览" });
    expect(within(panel).getByText("3")).toBeInTheDocument();
    expect(within(panel).getByText("session")).toBeInTheDocument();
    expect(within(panel).getByText("AI 提供商渠道")).toBeInTheDocument();
  });

  // The fingerprint editor must show every value with its default, allow an edit,
  // and offer both a per-field and a global restore.
  it("shows each fingerprint field with its default and saves an edit", async () => {
    const user = userEvent.setup();
    const requests = codexFetchMock();

    render(<CodexWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);
    await user.click(await screen.findByRole("tab", { name: "指纹配置" }));

    const panel = await screen.findByRole("tabpanel", { name: "指纹配置" });
    // The default is rendered next to the value for every field.
    const userAgentRow = within(panel).getByLabelText("User-Agent").closest(".codex-fingerprint-row") as HTMLElement;
    expect(within(userAgentRow).getByText(/codex-tui\/0\.153\.3/)).toBeInTheDocument();
    // An already-overridden field carries its badge.
    expect(within(panel).getAllByText("已覆盖").length).toBeGreaterThan(0);

    const originator = within(panel).getByLabelText("Originator");
    await user.clear(originator);
    await user.type(originator, "operator-originator");
    await user.click(within(panel).getByRole("button", { name: "保存" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/codex/fingerprint") && init.method === "PUT")).toBe(true));
    const saveRequest = requests.find(({ url, init }) => url.endsWith("/codex/fingerprint") && init.method === "PUT");
    expect(JSON.parse(String(saveRequest?.init.body))).toEqual({ values: { originator: "operator-originator" } });

    // A per-field restore clears that field only.
    await user.click(within(originatorRow(panel)).getByRole("button", { name: "恢复默认值" }));
    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/codex/fingerprint/reset"))).toBe(true));
    const resetRequest = requests.find(({ url, init }) => url.endsWith("/codex/fingerprint/reset"));
    expect(JSON.parse(String(resetRequest?.init.body))).toEqual({ keys: ["originator"] });

    // The global restore sends an empty key list, which means "everything".
    await user.click(within(panel).getByRole("button", { name: "恢复全部默认值" }));
    await waitFor(() => {
      const resets = requests.filter(({ url }) => url.endsWith("/codex/fingerprint/reset"));
      expect(JSON.parse(String(resets.at(-1)?.init.body))).toEqual({ keys: [] });
    });
  });

  it("disables a Codex model globally and can enable every model again", async () => {
    const user = userEvent.setup();
    const requests = codexFetchMock();

    render(<CodexWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);
    await user.click(await screen.findByRole("tab", { name: "模型与价格" }));

    const panel = await screen.findByRole("tabpanel", { name: "模型与价格" });
    // The global effect is stated explicitly before the table.
    expect(within(panel).getByText(/在此禁用模型会作用于所有 Codex 账号与 AI 提供商渠道/)).toBeInTheDocument();

    const codexRow = within(panel).getByText("gpt-5.4-codex").closest("tr") as HTMLElement;
    await user.click(within(codexRow).getByRole("button", { name: "禁用" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/codex/models") && init.method === "PUT")).toBe(true));
    const disableRequest = requests.find(({ url, init }) => url.endsWith("/codex/models") && init.method === "PUT");
    expect(JSON.parse(String(disableRequest?.init.body))).toEqual({ disabled: ["gpt-5.4-codex"] });
    expect(await within(within(panel).getByText("gpt-5.4-codex").closest("tr") as HTMLElement).findByText("已禁用")).toBeInTheDocument();

    await user.click(within(panel).getByRole("button", { name: "全部启用" }));
    await waitFor(() => {
      const writes = requests.filter(({ url, init }) => url.endsWith("/codex/models") && init.method === "PUT");
      expect(JSON.parse(String(writes.at(-1)?.init.body))).toEqual({ disabled: [] });
    });
  });

  it("saves the moved Codex experimental settings from the overview", async () => {
    const user = userEvent.setup();
    const requests = codexFetchMock();
    const onNotice = vi.fn();

    render(<CodexWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={onNotice} />);

    const panel = await screen.findByRole("tabpanel", { name: "总览" });
    await user.click(within(panel).getByRole("checkbox", { name: "Codex Agent Identity / PAT" }));
    await user.click(within(panel).getByRole("button", { name: "保存设置" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/experiments") && init.method === "PUT")).toBe(true));
    const saveRequest = requests.find(({ url, init }) => url.endsWith("/experiments") && init.method === "PUT");
    const body = JSON.parse(String(saveRequest?.init.body)) as Record<string, unknown>;
    expect(body).toMatchObject({ agent_identity_enabled: true, auto_model_whitelist_enabled: true });
    expect((body.codex_identity as Record<string, unknown>).outbound_convergence_enabled).toBe(false);
    await waitFor(() => expect(onNotice).toHaveBeenCalledWith("实验性设置已保存"));
  });
});

function originatorRow(panel: HTMLElement): HTMLElement {
  return within(panel).getByLabelText("Originator").closest(".codex-fingerprint-row") as HTMLElement;
}

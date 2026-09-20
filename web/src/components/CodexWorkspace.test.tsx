import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { _resetSessionForTest, setSession } from "../store/session";
import { formatDateTimeForLocale } from "../i18n";
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

// Rows carry the price the plugin's own accounting table resolves for the model.
const modelRows = [
  { id: "gpt-5.4-codex", disabled: false, accounts: 2, channels: 1, priced: true, input_usd_per_million: 1.25, output_usd_per_million: 10, cache_read_usd_per_million: 0.125 },
  { id: "unpriced-model", disabled: false, accounts: 0, channels: 1, priced: false },
];

interface CodexFetchMockOptions {
  /** Extra AI-provider channels that answer the target list, used when no Codex account exists. */
  channels?: string[];
  /** Answer the account list with nothing, like an installation that only uses channels. */
  noAccounts?: boolean;
  disabled?: string[];
  fields?: Array<Record<string, unknown>>;
  overview?: Record<string, unknown>;
  /** Answer the experiments snapshot with nothing, like an unreadable payload. */
  noExperiments?: boolean;
  /** Provider runtime snapshots, which carry the Codex usage totals on the overview. */
  runtime?: unknown;
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
    // A Codex credential may exist only as an AI-provider channel, which is the case this option
    // covers: no Codex account is listed, so the channel has to be offered as a target.
    const channelTargets = (options.channels ?? []).map((label, index) => ({ id: `channel:${index}`, label, kind: "channel", key_set: true }));
    let disabled = [...(options.disabled ?? [])];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input);
      requests.push({ url, init });
      if (url.endsWith("/accounts/model-test") && init.method === "POST") {
        return jsonResponse({ account_id: "acct-codex-1", provider: "codex", model: "gpt-5.4-codex", status: "available", probe_kind: "model", reason_code: "model_response_ok", status_code: 200, latency_ms: 42 });
      }
      if (url.endsWith("/codex/test-targets")) {
        return jsonResponse({ targets: channelTargets });
      }
      if (url.endsWith("/codex/model-test") && init.method === "POST") {
        return jsonResponse({
          result: { reachable: true, status: "available", reason_code: "model_response_ok", status_code: 200, latency_ms: 12, model: "gpt-5.4-codex", endpoint: "responses", tested_at: "2026-09-12T00:00:00Z" },
        });
      }
      if (url.includes("/accounts?")) {
        if (options.noAccounts) {
          return jsonResponse({ accounts: [], total: 0, page: 1, page_size: 100, pages: 0 });
        }
        return jsonResponse({ accounts: [
          { id: "acct-codex-1", name: "codex-one.json", provider: "codex", type: "codex", status: "active", disabled: false, unavailable: false, editable: true, recommended_action: "keep", usage: { total_tokens: 12_000, input_tokens: 9_000, output_tokens: 3_000, cached_tokens: 2_000, credit: { amount_usd: 0.5, rated_requests: 12, unrated_requests: 0 }, codex: { five_hour: { used_percent: 42.5, reset_at: "2026-09-12T05:00:00Z" }, seven_day: { used_percent: 12, reset_at: "2026-09-15T00:00:00Z" } } } },
        ], total: 1, page: 1, page_size: 100, pages: 1 });
      }
      if (url.endsWith("/codex/overview")) {
        return jsonResponse({
          overview: {
            accounts: 3,
            channels: 2,
            disabled_models: disabled.length,
            convergence_mode: "device",
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
        return jsonResponse({
          models: modelRows.map((row) => ({ ...row, disabled: disabled.includes(row.id) })),
          disabled,
          pricing_source: "Sub2API / Wei-Shaw model-price-repo",
          pricing_updated_at: "2026-09-12T00:00:00Z",
        });
      }
      if (url.endsWith("/codex/models")) {
        return jsonResponse({
          models: modelRows.map((row) => ({ ...row, disabled: disabled.includes(row.id) })),
          disabled,
          pricing_source: "Sub2API / Wei-Shaw model-price-repo",
          pricing_updated_at: "2026-09-12T00:00:00Z",
        });
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
      if (url.endsWith("/ai-providers/runtime")) return jsonResponse(options.runtime ?? { snapshots: [], updated_at: new Date().toISOString() });
      if (options.noExperiments && url.endsWith("/experiments")) return jsonResponse({});
      if (url.endsWith("/experiments") && init.method === "PUT") {
        return jsonResponse({ settings: {
          weekly_overdraft_enabled: true, agent_identity_enabled: false, auto_model_whitelist_enabled: false,
          sub2api_credit_usage_enabled: true,
          codex_identity: { outbound_convergence_enabled: true, convergence_mode: "session", ingress_gate_enabled: false, allow_app_server_clients: false },
        } });
      }
      if (url.endsWith("/experiments")) {
        return jsonResponse({ settings: {
          // The automatic allow-list is off in the served snapshot; the view must echo it unchanged.
          weekly_overdraft_enabled: false, agent_identity_enabled: false, auto_model_whitelist_enabled: false,
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
    // The effective mode shows its localized label, never the raw enum value.
    const effectiveMode = within(panel).getByText("有效收敛模式").nextElementSibling as HTMLElement;
    expect(effectiveMode.textContent).toBe("设备级（固定 installation_id）");
    expect(within(panel).queryByText("device")).not.toBeInTheDocument();
    expect(within(panel).getByText("AI 提供商渠道")).toBeInTheDocument();
  });

  // The upstream 5h/7d allowance is what a Codex operator watches, and it exists only on the
  // credentials themselves, so the overview reports the tightest one that has a snapshot.
  it("reports the Codex allowance windows of the credentials that have one", async () => {
    codexFetchMock();

    render(<CodexWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const panel = await screen.findByRole("tabpanel", { name: "总览" });
    const windows = await within(panel).findByRole("region", { name: "额度窗口" });
    expect(within(windows).getByText("5 小时用量")).toBeInTheDocument();
    expect(within(windows).getByText("42.5%")).toBeInTheDocument();
    expect(within(windows).getByText(`${formatDateTimeForLocale("zh-CN", "2026-09-12T05:00:00Z")} 重置`)).toBeInTheDocument();
    expect(within(windows).getByText("7 天用量")).toBeInTheDocument();
    expect(within(windows).getByText("12.0%")).toBeInTheDocument();
  });

  it.each([
    { name: "empty", mode: "" },
    { name: "unrecognized", mode: "experimental_mode" },
  ])("shows a dash for the effective convergence mode when it is $name", async ({ mode }) => {
    codexFetchMock({ overview: { convergence_mode: mode } });

    render(<CodexWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const panel = await screen.findByRole("tabpanel", { name: "总览" });
    const effectiveMode = within(panel).getByText("有效收敛模式").nextElementSibling as HTMLElement;
    expect(effectiveMode.textContent).toBe("-");
  });

  // The unset enum value follows the converging default now, so the inherit option has to say so.
  it("names the unset convergence mode after the converging default", async () => {
    codexFetchMock();

    render(<CodexWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const panel = await screen.findByRole("tabpanel", { name: "总览" });
    const mode = within(panel).getByLabelText("收敛模式") as HTMLSelectElement;
    expect(mode).toHaveValue("");
    expect(mode.selectedOptions[0]?.textContent).toBe("默认：设备级（未显式配置）");
    expect(within(panel).queryByText("默认关闭（未显式配置）")).not.toBeInTheDocument();
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

  // The fingerprint mode is a fixed enum: the select must show the localized labels while
  // the value it submits stays the raw enum value the API stores.
  it("localizes the fingerprint mode options and still submits the raw value", async () => {
    const user = userEvent.setup();
    const requests = codexFetchMock();

    render(<CodexWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);
    await user.click(await screen.findByRole("tab", { name: "指纹配置" }));
    const panel = await screen.findByRole("tabpanel", { name: "指纹配置" });

    const mode = within(panel).getByRole("combobox", { name: "模式" }) as HTMLSelectElement;
    expect(mode).toHaveValue("off");
    expect(Array.from(mode.options).map((option) => [option.value, option.textContent])).toEqual([
      ["off", "关闭（透传原始标识）"],
      ["device", "设备级（固定 installation_id）"],
      ["session", "会话级（固定会话并派生 thread）"],
      ["full", "完全收敛（单设备单会话）"],
    ]);

    await user.selectOptions(mode, "device");
    await user.click(within(panel).getByRole("button", { name: "保存" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/codex/fingerprint") && init.method === "PUT")).toBe(true));
    const saveRequest = requests.find(({ url, init }) => url.endsWith("/codex/fingerprint") && init.method === "PUT");
    expect(JSON.parse(String(saveRequest?.init.body))).toEqual({ values: { mode: "device" } });
  });

  it("disables a Codex model globally and can enable every model again", async () => {
    const user = userEvent.setup();
    const requests = codexFetchMock();
    const onNotice = vi.fn();

    render(<CodexWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={onNotice} />);
    await user.click(await screen.findByRole("tab", { name: "模型与价格" }));

    const panel = await screen.findByRole("tabpanel", { name: "模型与价格" });
    // The global effect is stated explicitly before the table.
    expect(within(panel).getByText(/在此禁用模型会作用于所有 Codex 账号与 AI 提供商渠道/)).toBeInTheDocument();

    const codexRow = within(panel).getByText("gpt-5.4-codex").closest("tr") as HTMLElement;
    await user.click(within(codexRow).getByRole("button", { name: "禁用 gpt-5.4-codex" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/codex/models") && init.method === "PUT")).toBe(true));
    const disableRequest = requests.find(({ url, init }) => url.endsWith("/codex/models") && init.method === "PUT");
    expect(JSON.parse(String(disableRequest?.init.body))).toEqual({ disabled: ["gpt-5.4-codex"] });
    expect(await within(within(panel).getByText("gpt-5.4-codex").closest("tr") as HTMLElement).findByText("已禁用")).toBeInTheDocument();
    // The result is announced: a silent write is indistinguishable from a dead button.
    await waitFor(() => expect(onNotice).toHaveBeenCalledWith("已全局禁用 1 个模型"));

    await user.click(within(panel).getByRole("button", { name: "全部启用" }));
    await waitFor(() => {
      const writes = requests.filter(({ url, init }) => url.endsWith("/codex/models") && init.method === "PUT");
      expect(JSON.parse(String(writes.at(-1)?.init.body))).toEqual({ disabled: [] });
    });
    await waitFor(() => expect(onNotice).toHaveBeenLastCalledWith("已重新启用 1 个模型"));
  });

  it("offers an AI-provider channel when no Codex account exists", async () => {
    const user = userEvent.setup();
    // The reported case: the Codex credential exists only as an AI-provider channel, so the
    // account list is empty and the channel has to be offered instead.
    const requests = codexFetchMock({ channels: ["my-codex-channel"], noAccounts: true });

    render(<CodexWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);
    await user.click(await screen.findByRole("tab", { name: "模型与价格" }));
    const panel = await screen.findByRole("tabpanel", { name: "模型与价格" });
    await user.click(within(panel).getByRole("button", { name: "测试 gpt-5.4-codex" }));

    const dialog = await screen.findByRole("dialog", { name: "模型可用性测试" });
    expect(within(dialog).queryByText("没有可用于测试的凭据")).not.toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "开始测试" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/codex/model-test") && init.method === "POST")).toBe(true));
    const probe = requests.find(({ url, init }) => url.endsWith("/codex/model-test") && init.method === "POST");
    expect(JSON.parse(String(probe?.init.body))).toEqual({ channel_index: 0, model: "gpt-5.4-codex" });
    expect(await within(dialog).findByText("模型可用")).toBeInTheDocument();
  });

  // The reported case: an installation with a Codex account AND a Codex channel offers two
  // targets, and the dialog opened with no target selected while the picker displayed the first
  // one, so the start button stayed disabled until the operator re-picked the target already shown.
  it("starts on the first target and keeps the start button usable", async () => {
    const user = userEvent.setup();
    const requests = codexFetchMock({ channels: ["my-codex-channel"] });

    render(<CodexWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);
    await user.click(await screen.findByRole("tab", { name: "模型与价格" }));
    const panel = await screen.findByRole("tabpanel", { name: "模型与价格" });
    await user.click(within(panel).getByRole("button", { name: "测试 gpt-5.4-codex" }));

    const dialog = await screen.findByRole("dialog", { name: "模型可用性测试" });
    // Two credentials are offered, so the picker is a select and it starts on a real selection.
    const picker = await within(dialog).findByRole("combobox", { name: "测试目标" });
    expect(picker).toHaveValue("account:acct-codex-1");
    expect((picker as HTMLSelectElement).selectedOptions[0].textContent).toContain("codex-one.json");

    // The button must already be usable without touching the picker.
    const start = within(dialog).getByRole("button", { name: "开始测试" });
    expect(start).toBeEnabled();
    await user.click(start);

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/accounts/model-test") && init.method === "POST")).toBe(true));
    const probe = requests.find(({ url, init }) => url.endsWith("/accounts/model-test") && init.method === "POST");
    expect(JSON.parse(String(probe?.init.body))).toMatchObject({ account_id: "acct-codex-1", model: "gpt-5.4-codex" });
  });

  // Switching the picker still probes the credential the operator chose.
  it("probes the target the operator picks", async () => {
    const user = userEvent.setup();
    const requests = codexFetchMock({ channels: ["my-codex-channel"] });

    render(<CodexWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);
    await user.click(await screen.findByRole("tab", { name: "模型与价格" }));
    const panel = await screen.findByRole("tabpanel", { name: "模型与价格" });
    await user.click(within(panel).getByRole("button", { name: "测试 gpt-5.4-codex" }));

    const dialog = await screen.findByRole("dialog", { name: "模型可用性测试" });
    const picker = await within(dialog).findByRole("combobox", { name: "测试目标" });
    await user.selectOptions(picker, "channel:0");
    await user.click(within(dialog).getByRole("button", { name: "开始测试" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/codex/model-test") && init.method === "POST")).toBe(true));
    const probe = requests.find(({ url, init }) => url.endsWith("/codex/model-test") && init.method === "POST");
    expect(JSON.parse(String(probe?.init.body))).toEqual({ channel_index: 0, model: "gpt-5.4-codex" });
  });

  it("shows the plugin price table rates and marks an unpriced model", async () => {
    const user = userEvent.setup();
    codexFetchMock();

    render(<CodexWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);
    await user.click(await screen.findByRole("tab", { name: "模型与价格" }));

    const panel = await screen.findByRole("tabpanel", { name: "模型与价格" });
    // The source of the displayed prices is stated explicitly.
    expect(within(panel).getByText(/Sub2API \/ Wei-Shaw model-price-repo/)).toBeInTheDocument();

    const pricedRow = within(panel).getByText("gpt-5.4-codex").closest("tr") as HTMLElement;
    const pricedCells = within(pricedRow).getAllByRole("cell").map((cell) => cell.textContent);
    expect(pricedCells).toContain("$1.25");
    expect(pricedCells).toContain("$10.00");
    expect(pricedCells).toContain("$0.13");

    const unpricedRow = within(panel).getByText("unpriced-model").closest("tr") as HTMLElement;
    expect(within(unpricedRow).getByText("暂无价格")).toBeInTheDocument();
  });

  it("disables and enables the selected models in bulk", async () => {
    const user = userEvent.setup();
    const requests = codexFetchMock();

    render(<CodexWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);
    await user.click(await screen.findByRole("tab", { name: "模型与价格" }));
    const panel = await screen.findByRole("tabpanel", { name: "模型与价格" });

    // Select both rows through the header checkbox, then disable the selection.
    await user.click(within(panel).getByRole("checkbox", { name: "全选" }));
    expect(within(panel).getByText("已选择 2 个")).toBeInTheDocument();
    await user.click(within(panel).getByRole("button", { name: "禁用所选" }));

    await waitFor(() => {
      const writes = requests.filter(({ url, init }) => url.endsWith("/codex/models") && init.method === "PUT");
      expect(writes.length).toBeGreaterThan(0);
      expect(JSON.parse(String(writes.at(-1)?.init.body))).toEqual({ disabled: ["gpt-5.4-codex", "unpriced-model"] });
    });
    // The selection is cleared after a successful write.
    await waitFor(() => expect(within(panel).getByText("已选择 0 个")).toBeInTheDocument());

    // Re-select the same rows and enable them again.
    await user.click(within(panel).getByRole("checkbox", { name: "全选" }));
    await user.click(within(panel).getByRole("button", { name: "启用所选" }));
    await waitFor(() => {
      const writes = requests.filter(({ url, init }) => url.endsWith("/codex/models") && init.method === "PUT");
      expect(JSON.parse(String(writes.at(-1)?.init.body))).toEqual({ disabled: [] });
    });
  });

  it("tests one Codex model through a stored account credential", async () => {
    const user = userEvent.setup();
    const requests = codexFetchMock();

    render(<CodexWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);
    await user.click(await screen.findByRole("tab", { name: "模型与价格" }));
    const panel = await screen.findByRole("tabpanel", { name: "模型与价格" });

    await user.click(within(panel).getByRole("button", { name: "测试 gpt-5.4-codex" }));
    // The test opens the same dialog the accounts page uses, with the model and target.
    const dialog = await screen.findByRole("dialog", { name: "模型可用性测试" });
    // The dialog shows the model in its header and in the outcome list, like the accounts test.
    expect(within(dialog).getAllByText("gpt-5.4-codex").length).toBeGreaterThan(0);
    // A single candidate is shown by name, so the dialog never needs a second click to run it.
    expect(await within(dialog).findByLabelText("测试目标")).toHaveValue("codex-one.json");
    await user.click(within(dialog).getByRole("button", { name: "开始测试" }));
    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/accounts/model-test") && init.method === "POST")).toBe(true));
    const probe = requests.find(({ url, init }) => url.endsWith("/accounts/model-test") && init.method === "POST");
    expect(JSON.parse(String(probe?.init.body))).toMatchObject({ account_id: "acct-codex-1", model: "gpt-5.4-codex" });
    // The probe result is rendered inside the dialog with the localized status label.
    expect(await within(dialog).findByText("模型可用")).toBeInTheDocument();
    expect(within(dialog).getByText("model_response_ok")).toBeInTheDocument();
    expect(within(dialog).getByText("200")).toBeInTheDocument();
  });

  it("keeps the Codex identity policy here and echoes the two experiments", async () => {
    const user = userEvent.setup();
    const requests = codexFetchMock();
    const onNotice = vi.fn();

    render(<CodexWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={onNotice} />);

    const panel = await screen.findByRole("tabpanel", { name: "总览" });
    // The identity policy editor lives here; the two experiments stay experimental
    // toggles in the experimental settings panel and must not be edited here.
    expect(within(panel).getByRole("region", { name: "Codex 身份兼容" })).toBeInTheDocument();
    expect(within(panel).queryByText("Codex 5h / 7d 额度透支续用")).not.toBeInTheDocument();
    expect(within(panel).queryByText("Codex Agent Identity / PAT")).not.toBeInTheDocument();

    await user.click(within(panel).getByRole("button", { name: "保存设置" }));

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/experiments") && init.method === "PUT")).toBe(true));
    const saveRequest = requests.find(({ url, init }) => url.endsWith("/experiments") && init.method === "PUT");
    const body = JSON.parse(String(saveRequest?.init.body)) as Record<string, unknown>;
    // The experiment values are echoed from the snapshot so saving here cannot
    // clear (or enable) what the experimental settings panel configured; the
    // served snapshot leaves the automatic allow-list off.
    expect(body).toMatchObject({ weekly_overdraft_enabled: false, agent_identity_enabled: false, auto_model_whitelist_enabled: false });
    expect((body.codex_identity as Record<string, unknown>).outbound_convergence_enabled).toBe(false);
    await waitFor(() => expect(onNotice).toHaveBeenCalledWith("实验性设置已保存"));
  });

  it("never enables the automatic allow-list experiment from an unread snapshot", async () => {
    const user = userEvent.setup();
    // The experiments payload is unreadable, so the view has no value to echo and
    // cannot save the identity policy at all.
    const requests = codexFetchMock({ noExperiments: true });

    render(<CodexWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const panel = await screen.findByRole("tabpanel", { name: "总览" });
    const save = within(panel).getByRole("button", { name: "保存设置" });
    await waitFor(() => expect(save).toBeDisabled());
    await user.click(save);

    // Without a save request there is no payload that could switch the experiment on.
    expect(requests.some(({ url, init }) => url.endsWith("/experiments") && init.method === "PUT")).toBe(false);
  });

  // "codex" provider, so the panel sums those runtime snapshots. Without them it must say so
  // rather than showing zeroes that read like measured emptiness.
  it("shows the Codex usage totals on the overview and says when none are recorded", async () => {
    codexFetchMock({
      runtime: {
        snapshots: [
          { provider: "codex", auth_index: "a", identity: "credential:one", credential_backed: true, supported: true, active: 0, waiting: 0, limit: 0, request_limit: 0, request_window_seconds: 0, used_requests: 0, limit_15s: 0, used_60s: 0, used_15s: 0, input_tokens: 300_000_000, output_tokens: 17_000_000, reasoning_tokens: 0, cached_tokens: 1_000_000, total_tokens: 317_000_000, amount_usd: 386.5, rated_requests: 2760, unrated_requests: 4, quota: { five_hour_amount_usd: 0, seven_day_amount_usd: 0 }, models: [], updated_at: "2026-09-16T00:00:00Z" },
          { provider: "openai-compatible-other", auth_index: "b", identity: "credential:two", credential_backed: true, supported: true, active: 0, waiting: 0, limit: 0, request_limit: 0, request_window_seconds: 0, used_requests: 0, limit_15s: 0, used_60s: 0, used_15s: 0, input_tokens: 1, output_tokens: 1, reasoning_tokens: 0, cached_tokens: 0, total_tokens: 2, amount_usd: 9, rated_requests: 1, unrated_requests: 0, quota: { five_hour_amount_usd: 0, seven_day_amount_usd: 0 }, models: [], updated_at: "2026-09-16T00:00:00Z" },
        ],
        updated_at: "2026-09-16T00:00:00Z",
      },
    });

    render(<CodexWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const panel = await screen.findByRole("tabpanel", { name: "总览" });
    const totals = await within(panel).findByRole("group", { name: "Codex 累计用量" });
    // Only the codex provider counts: the other provider's 2 tokens and $9 stay out.
    expect(within(totals).getByText("317,000,000")).toBeInTheDocument();
    expect(within(totals).getByText("$386.5")).toBeInTheDocument();
    expect(within(totals).getByText("2,764")).toBeInTheDocument();
    expect(within(totals).getByText("未计价 4")).toBeInTheDocument();
  });

  it("states that no Codex usage is recorded instead of showing zeroes", async () => {
    codexFetchMock();

    render(<CodexWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const panel = await screen.findByRole("tabpanel", { name: "总览" });
    const totals = await within(panel).findByRole("group", { name: "Codex 累计用量" });
    expect(within(totals).getByText("尚未记录到 Codex 供应商流量")).toBeInTheDocument();
  });
});

function originatorRow(panel: HTMLElement): HTMLElement {
  return within(panel).getByLabelText("Originator").closest(".codex-fingerprint-row") as HTMLElement;
}


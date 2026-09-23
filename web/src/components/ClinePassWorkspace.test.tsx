import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { stubWorkspaceFetch, WorkspaceFetchMockOptions, clinePassAccountView, clinePassLoginView, clinePassQuotaUsage, resetWorkspaceTestSession, UNRENDERED_SECRET } from "../test/workspaceFetchMock";
import { formatDateTimeForLocale } from "../i18n";
import { ClinePassWorkspace } from "./ClinePassWorkspace";
import { OpenCodeWorkspace } from "./OpenCodeWorkspace";

describe("ClinePassWorkspace", () => {
  beforeEach(resetWorkspaceTestSession);

  function clinePassFetchMock(options: WorkspaceFetchMockOptions = {}) {
    return stubWorkspaceFetch(options);
  }

  /**
   * The workspace opens on 总览 now, so a test that exercises the credential list opens that tab
   * first, exactly like the operator does to reach it.
   */
  async function renderClinePass(onNotice: (message: string) => void = () => undefined) {
    const result = render(<ClinePassWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={onNotice} />);
    const tabs = await screen.findByRole("tablist", { name: "Cline Pass" });
    await userEvent.setup().click(within(tabs).getByRole("tab", { name: "账号" }));
    return result;
  }

  it("renders the Cline Pass surface without the OpenCode tab strip", async () => {
    const requests = clinePassFetchMock({ clinePassAccounts: [clinePassAccountView()] });

    await renderClinePass();

    // Cline Pass is its own product menu, so this view must not offer the OpenCode tab strip
    // (nor its OpenCode-only links and heading).
    expect(await screen.findByRole("tabpanel", { name: "Cline Pass" })).toBeInTheDocument();
    // The OpenCode strip must not appear; Cline Pass offers its own overview/accounts/models tabs.
    expect(screen.queryByRole("tablist", { name: "OpenCode" })).not.toBeInTheDocument();
    const clinePassTabs = screen.getByRole("tablist", { name: "Cline Pass" });
    expect(within(clinePassTabs).getAllByRole("tab")).toHaveLength(3);
    expect(within(clinePassTabs).getByRole("tab", { name: "总览" })).toBeInTheDocument();
    expect(within(clinePassTabs).getByRole("tab", { name: "模型" })).toBeInTheDocument();
    expect(screen.queryByText("OpenCode Go 与 Zen 控制器")).not.toBeInTheDocument();
    expect(screen.queryByText(/OpenCode Go · /)).not.toBeInTheDocument();

    const panel = screen.getByRole("tabpanel", { name: "账号" });
    expect(within(panel).getByText("Work laptop")).toBeInTheDocument();
    expect(within(panel).getByText("cline-pass/glm-5.3, cline-pass/kimi-k2.6")).toBeInTheDocument();
    expect(within(panel).getByRole("button", { name: "使用浏览器登录" })).toBeInTheDocument();
    expect(requests.some(({ url }) => url.endsWith("/opencode/cline-pass/accounts"))).toBe(true);
  });

  // The operator reported the unbound hint appearing on every refresh while the server reports the
  // channel bound. This pins the contract with the payload the server actually serves (captured
  // live: channel_bound true, 21 published models, no gaps), so a row may only show the unbound
  // hint when the payload really says unbound.
  it("shows the bound badge for a payload that reports the channel bound", async () => {
    clinePassFetchMock({
      clinePassAccounts: [clinePassAccountView({
        channel_bound: true,
        channel_models: 21,
        channel_model_gaps: 0,
        models: Array.from({ length: 21 }, (_, index) => `cline-pass/model-${index}`),
      })],
    });

    await renderClinePass();

    const panel = await screen.findByRole("tabpanel", { name: "账号" });
    const row = (await within(panel).findByText("Work laptop")).closest("tr") as HTMLElement;
    expect(within(row).getByText("已绑定 · 已发布 21 个模型")).toBeInTheDocument();
    expect(within(row).queryByText("尚未绑定 CPA 渠道")).not.toBeInTheDocument();
  });
  // A failed load must not look like an empty account list: the page the operator is looking at
  // has to say what went wrong, and a 401 belongs to the shared session handler.
  it("reports a failed account load instead of showing an empty list", async () => {
    clinePassFetchMock({ clinePassAccountsStatus: 502 });
    const onAPIError = vi.fn();
    render(<ClinePassWorkspace refreshRevision={0} onAPIError={onAPIError} onNotice={() => undefined} />);

    // The failure is reported on the page itself, above the tabs, so it is visible whichever
    // tab is open.
    expect(await screen.findByRole("alert")).toHaveTextContent("cline pass accounts failed");
    // The empty state must not be what the operator sees when the read failed.
    expect(screen.queryByText(/暂无 Cline Pass 账号/)).not.toBeInTheDocument();
    expect(onAPIError).not.toHaveBeenCalled();
  });

  it("routes a 401 on the account load into the shared handler", async () => {
    clinePassFetchMock({ clinePassAccountsStatus: 401 });
    const onAPIError = vi.fn();
    render(<ClinePassWorkspace refreshRevision={0} onAPIError={onAPIError} onNotice={() => undefined} />);

    await waitFor(() => expect(onAPIError).toHaveBeenCalled());
  });

  // The sidebar menus swap one product component for the other, so React drops the whole
  // surface on the switch. That used to leave the OpenCode overview (billing cards, session
  // router, its counters) on screen under the Cline Pass heading.
  it("shows only the Cline Pass surface when the menu switches from OpenCode", async () => {
    clinePassFetchMock({ clinePassAccounts: [clinePassAccountView()] });

    const { rerender } = render(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);
    expect(await screen.findByRole("tabpanel", { name: "总览" })).toBeInTheDocument();
    expect(screen.getByText("对话会话")).toBeInTheDocument();

    rerender(<ClinePassWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    // The Cline Pass menu opens on its own overview, so the credential list needs its tab.
    const tabs = await screen.findByRole("tablist", { name: "Cline Pass" });
    const user = userEvent.setup();
    await user.click(within(tabs).getByRole("tab", { name: "账号" }));
    const panel = await screen.findByRole("tabpanel", { name: "账号" });
    expect(within(panel).getByText("Work laptop")).toBeInTheDocument();
    expect(screen.queryByText("对话会话")).not.toBeInTheDocument();
    expect(screen.queryByText("OpenCode Zen")).not.toBeInTheDocument();
    expect(screen.queryByText("OpenCode 模型")).not.toBeInTheDocument();
  });

  // The other direction used to leak too: opening Cline Pass first and then switching to
  // OpenCode rendered the Cline Pass accounts/models surface inside the OpenCode menu.
  it("never shows the Cline Pass surface inside the OpenCode menu", async () => {
    clinePassFetchMock({ clinePassAccounts: [clinePassAccountView()] });

    const user = userEvent.setup();
    const { rerender } = render(<ClinePassWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);
    // The Cline Pass workspace owns its own overview panel inside its OpenCode-free menu.
    expect(await screen.findByRole("tabpanel", { name: "Cline Pass" })).toBeInTheDocument();

    rerender(<OpenCodeWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    expect(await screen.findByRole("tabpanel", { name: "总览" })).toBeInTheDocument();
    expect(screen.queryByRole("tabpanel", { name: "Cline Pass" })).not.toBeInTheDocument();
    const tablist = screen.getByRole("tablist", { name: "OpenCode" });
    expect(within(tablist).queryByRole("tab", { name: "Cline Pass 账号" })).not.toBeInTheDocument();
    expect(within(tablist).getAllByRole("tab")).toHaveLength(5);
  });

  it("lists the published Cline Pass mapping with the client-facing model id", async () => {
    const user = userEvent.setup();
    const requests = clinePassFetchMock({ clinePassAccounts: [clinePassAccountView()] });

    await renderClinePass();

    const tabs = await screen.findByRole("tablist", { name: "Cline Pass" });
    await user.click(within(tabs).getByRole("tab", { name: "模型" }));

    const panel = await screen.findByRole("tabpanel", { name: "模型" });
    // The client calls the short id; the upstream id stays visible for reference.
    expect(within(panel).getByText("deepseek-v4.1-flash")).toBeInTheDocument();
    expect(within(panel).getByText("cline-pass/deepseek-v4.1-flash")).toBeInTheDocument();
    expect(within(panel).getByText("已发布")).toBeInTheDocument();
    expect(within(panel).getByText("未发布")).toBeInTheDocument();
    expect(within(panel).getByText("已发布 1 / 2 个模型")).toBeInTheDocument();
    expect(requests.some(({ url }) => url.endsWith("/opencode/cline-pass/models"))).toBe(true);
  });

  it("saves the Cline Pass prefix setting and reloads the mapping", async () => {
    const user = userEvent.setup();
    const requests = clinePassFetchMock({ clinePassAccounts: [clinePassAccountView()], clinePassSettingsRebound: 1 });
    const onNotice = vi.fn();

    await renderClinePass(onNotice);

    const tabs = await screen.findByRole("tablist", { name: "Cline Pass" });
    await user.click(within(tabs).getByRole("tab", { name: "模型" }));

    const switcher = await screen.findByRole("checkbox", { name: "发布模型 ID 时不带 cline-pass/ 前缀" });
    expect(switcher).toBeChecked();
    await user.click(switcher);

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/opencode/cline-pass/settings") && init.method === "PUT")).toBe(true));
    const save = requests.find(({ url, init }) => url.endsWith("/opencode/cline-pass/settings") && init.method === "PUT");
    expect(JSON.parse(String(save?.init.body))).toEqual({ strip_model_prefix: false });
    await waitFor(() => expect(onNotice).toHaveBeenCalledWith("Cline Pass 前缀设置已保存 · 已重新绑定 1 个账号"));
    // The mapping is re-read so the table reflects the new client-facing ids.
    await waitFor(() => expect(requests.filter(({ url }) => url.endsWith("/opencode/cline-pass/models")).length).toBeGreaterThan(1));
  });

  it("saves the DeepSeek upstream-consistency switch without rebinding the channel", async () => {
    const user = userEvent.setup();
    const requests = clinePassFetchMock({ clinePassAccounts: [clinePassAccountView()], clinePassSettingsRebound: 1 });
    const onNotice = vi.fn();

    await renderClinePass(onNotice);

    const tabs = await screen.findByRole("tablist", { name: "Cline Pass" });
    await user.click(within(tabs).getByRole("tab", { name: "模型" }));

    const switcher = await screen.findByRole("checkbox", { name: "DeepSeek 上游一致性" });
    expect(switcher).not.toBeChecked();
    await user.click(switcher);

    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/opencode/cline-pass/settings") && init.method === "PUT")).toBe(true));
    const save = requests.find(({ url, init }) => url.endsWith("/opencode/cline-pass/settings") && init.method === "PUT");
    // Only the control that changed is sent, so the prefix rule keeps its stored value
    // and the channel is not re-bound for a switch that changes no mapping.
    expect(JSON.parse(String(save?.init.body))).toEqual({ deepseek_upstream_consistency: true });
    await waitFor(() => expect(onNotice).toHaveBeenCalledWith("DeepSeek 上游一致性设置已保存"));
    // It is stored state, so the reloaded page reports it back.
    await waitFor(() => expect(screen.getByRole("checkbox", { name: "DeepSeek 上游一致性" })).toBeChecked());
    expect(screen.getByRole("checkbox", { name: "发布模型 ID 时不带 cline-pass/ 前缀" })).toBeChecked();
  });

  it("probes a Cline Pass mapping row with its upstream id in the shared dialog", async () => {
    const user = userEvent.setup();
    const requests = clinePassFetchMock({
      clinePassAccounts: [clinePassAccountView()],
      clinePassModelTest: {
        result: {
          reachable: true,
          status: "available",
          reason_code: "model_response_ok",
          status_code: 200,
          latency_ms: 18,
          model: "cline-pass/deepseek-v4.1-flash",
          endpoint: "chat",
          probe_kind: "model",
          tested_at: "2026-09-12T00:00:00Z",
        },
      },
    });

    await renderClinePass();

    const tabs = await screen.findByRole("tablist", { name: "Cline Pass" });
    await user.click(within(tabs).getByRole("tab", { name: "模型" }));

    const panel = await screen.findByRole("tabpanel", { name: "模型" });
    const row = (await within(panel).findByText("DeepSeek V4.1 Flash")).closest("tr") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: "测试 DeepSeek V4.1 Flash" }));

    // The result belongs to the dialog every other model page uses, never to a row of the mapping.
    const dialog = await screen.findByRole("dialog", { name: "模型可用性测试" });
    expect(within(panel).queryByRole("region", { name: "模型测试" })).not.toBeInTheDocument();
    expect(within(dialog).getByLabelText("测试目标")).toHaveValue("Work laptop");

    await user.click(within(dialog).getByRole("button", { name: "开始测试" }));
    await waitFor(() => expect(requests.some(({ url, init }) => url.endsWith("/opencode/cline-pass/model-test") && init.method === "POST")).toBe(true));
    const probe = requests.find(({ url, init }) => url.endsWith("/opencode/cline-pass/model-test") && init.method === "POST");
    // The probe talks to the Cline gateway, so it must send the id the gateway accepts.
    expect(JSON.parse(String(probe?.init.body))).toMatchObject({ model: "cline-pass/deepseek-v4.1-flash" });
    expect(await within(dialog).findByText("模型可用")).toBeInTheDocument();
  });

  it("keeps the accounts tab usable when the Cline Pass mapping cannot be read", async () => {
    const user = userEvent.setup();
    clinePassFetchMock({ clinePassAccounts: [clinePassAccountView()], clinePassModelsStatus: 502 });

    await renderClinePass();

    const tabs = await screen.findByRole("tablist", { name: "Cline Pass" });
    await user.click(within(tabs).getByRole("tab", { name: "模型" }));
    expect(await screen.findByRole("alert")).toBeInTheDocument();

    // Switching back must still show the unchanged accounts surface.
    await user.click(within(tabs).getByRole("tab", { name: "账号" }));
    const accounts = await screen.findByRole("tabpanel", { name: "账号" });
    expect(within(accounts).getByText("Work laptop")).toBeInTheDocument();
  });

  it("starts a Cline Pass browser sign-in and shows the device code while polling", async () => {
    const user = userEvent.setup();
    const requests = clinePassFetchMock({
      clinePassAccounts: [clinePassAccountView()],
      clinePassLoginStart: clinePassLoginView(),
      // The poll stays pending so the assertion observes the code the operator must enter.
      clinePassLoginPoll: clinePassLoginView({ status: "pending" }),
    });

    await renderClinePass();

    const panel = await screen.findByRole("tabpanel", { name: "账号" });
    expect(within(panel).getByText("Work laptop")).toBeInTheDocument();
    expect(within(panel).getByText("cline-pass/glm-5.3, cline-pass/kimi-k2.6")).toBeInTheDocument();

    await user.click(within(panel).getByRole("button", { name: "使用浏览器登录" }));

    const status = await within(panel).findByRole("status");
    expect(status).toHaveTextContent("CODE-1234");
    expect(status).toHaveTextContent("正在等待浏览器完成登录…");
    await waitFor(() => {
      expect(requests.some((entry) => entry.url.endsWith("/opencode/cline-pass/login/start"))).toBe(true);
      expect(requests.some((entry) => entry.url.endsWith("/opencode/cline-pass/login/poll"))).toBe(true);
    });
    // The sign-in code the gateway issued is rendered, and the secret is never returned.
    expect(requests.some((entry) => entry.url.endsWith("/opencode/cline-pass/login/cancel"))).toBe(false);
    expect(panel.textContent).not.toContain(UNRENDERED_SECRET);
  });

  it("saves a pasted Cline Pass API key through the account endpoint", async () => {
    const user = userEvent.setup();
    const requests = clinePassFetchMock();
    const onNotice = vi.fn();

    await renderClinePass(onNotice);

    const panel = await screen.findByRole("tabpanel", { name: "账号" });
    const keyInput = panel.querySelector('input[type="password"]') as HTMLInputElement;
    await user.type(keyInput, "sk-cline-pasted");
    await user.click(within(panel).getByRole("button", { name: "保存 API 密钥" }));

    await waitFor(() => {
      const saved = requests.find((entry) => entry.url.endsWith("/opencode/cline-pass/accounts") && entry.init.method === "POST");
      expect(saved).toBeDefined();
      expect(String(saved?.init.body)).toContain("sk-cline-pasted");
    });
    expect(onNotice).toHaveBeenCalled();
  });

  it("reports a failed Cline Pass sign-in instead of leaving the dialog pending", async () => {
    const user = userEvent.setup();
    clinePassFetchMock({
      clinePassLoginStart: clinePassLoginView({ status: "failed", error: "the gateway rejected the device code" }),
    });

    await renderClinePass();

    const panel = await screen.findByRole("tabpanel", { name: "账号" });
    await user.click(within(panel).getByRole("button", { name: "使用浏览器登录" }));

    expect(await within(panel).findByText(/Cline Pass 登录失败/)).toBeInTheDocument();
  });

  it("warns that an unbound Cline Pass account is unroutable and points at the bind action", async () => {
    const user = userEvent.setup();
    // A working credential is not routable until a CPA channel carries its base URL: the row
    // must say so instead of looking merely incomplete.
    clinePassFetchMock({
      clinePassAccounts: [clinePassAccountView({ channel_bound: false, channel_models: 0, channel_model_gaps: 2 })],
    });

    await renderClinePass();
    const panel = await screen.findByRole("tabpanel", { name: "账号" });
    const row = (await within(panel).findByText("Work laptop")).closest("tr") as HTMLElement;

    expect(within(row).getByText("尚未绑定 CPA 渠道")).toBeInTheDocument();
    // The row states the consequence clients see, because that is the only symptom an
    // operator gets from an unbound channel.
    expect(within(row).getByText(/unknown provider for model/)).toBeInTheDocument();
    // Binding is automatic, so the hint names the repair instead of an action to click.
    expect(within(row).getByText(/插件在打开本页时会自动发布渠道/)).toBeInTheDocument();
    expect(within(row).queryByRole("button", { name: "将 Work laptop 的模型发布到 CPA 路由" })).not.toBeInTheDocument();
  });

  it("names the model gap when the CPA channel publishes only part of an account", async () => {
    clinePassFetchMock({
      clinePassAccounts: [clinePassAccountView({ channel_bound: true, channel_models: 1, channel_model_gaps: 1 })],
    });

    await renderClinePass();
    const panel = await screen.findByRole("tabpanel", { name: "账号" });
    const row = (await within(panel).findByText("Work laptop")).closest("tr") as HTMLElement;

    expect(within(row).getByText("还有 1 个模型未发布")).toBeInTheDocument();
    expect(within(row).queryByText("尚未绑定 CPA 渠道")).not.toBeInTheDocument();
  });

  it("explains a credential the gateway rejected and says the repair is automatic", async () => {
    // The failure the operator sees: Cline refuses the stored token while CPA keeps routing through
    // the key on the channel row. The row must name the cause and the automatic repair, because the
    // account stays unroutable until that republish succeeds.
    clinePassFetchMock({
      clinePassAccounts: [clinePassAccountView({ channel_bound: false, channel_models: 0, channel_model_gaps: 2, channel_credential_rejected: true })],
    });

    await renderClinePass();
    const panel = await screen.findByRole("tabpanel", { name: "账号" });
    const row = (await within(panel).findByText("Work laptop")).closest("tr") as HTMLElement;

    expect(within(row).getByText("凭据已被网关拒绝，正在自动修复")).toBeInTheDocument();
    expect(within(row).getByText(/插件已自动轮换令牌并重写该渠道行/)).toBeInTheDocument();
    // The rejected state takes precedence over the plain unbound wording, which would send the
    // operator looking for a missing channel instead of a refused credential.
    expect(within(row).queryByText("尚未绑定 CPA 渠道")).not.toBeInTheDocument();
  });

  // The gateway answered 429 for this account, so the plugin disabled its channel row until the
  // window it named passes. The operator must read that apart from "not bound" (the row is still
  // published) and from a rejected credential (nothing needs repairing by hand).
  it("shows a quota-limited account as temporarily disabled while its published row stays listed", async () => {
    const until = "2026-10-03T12:00:00Z";
    clinePassFetchMock({
      clinePassAccounts: [clinePassAccountView({
        channel_bound: true,
        channel_models: 2,
        channel_model_gaps: 0,
        quota_limited: true,
        quota_limited_until: until,
      })],
    });

    await renderClinePass();
    const panel = await screen.findByRole("tabpanel", { name: "账号" });
    const row = (await within(panel).findByText("Work laptop")).closest("tr") as HTMLElement;

    expect(within(row).getByText("已临时停用（额度受限）")).toBeInTheDocument();
    expect(within(row).getByText(/网关以 429 拒绝了本账号/)).toBeInTheDocument();
    // The moment the gateway named is on screen, not just the fact that the account is held back.
    expect(row.textContent).toContain(formatDateTimeForLocale("zh-CN", until));
    // The row is still bound, so the cell must not read as a missing channel.
    expect(within(row).getByText("已绑定 · 已发布 2 个模型")).toBeInTheDocument();
    expect(within(row).queryByText("尚未绑定 CPA 渠道")).not.toBeInTheDocument();
    expect(within(row).queryByText("凭据已被网关拒绝，正在自动修复")).not.toBeInTheDocument();
  });

  // The bind failure the operator cannot read apart from "never published": the stored token could
  // not be rotated (a refused refresh), so the channel row was never written and the account needs a
  // new sign-in. The reason has to survive normalization and reach the cell, and the unbound badge
  // has to stay because the channel really is missing.
  it("names the bind reason when the account's channel row could not be published", async () => {
    const reason = "token refresh was refused (invalid_grant): the account needs a new sign-in";
    clinePassFetchMock({
      clinePassAccounts: [clinePassAccountView({
        channel_bound: false,
        channel_models: 0,
        channel_model_gaps: 2,
        channel_binding_error: reason,
      })],
    });

    await renderClinePass();
    const panel = await screen.findByRole("tabpanel", { name: "账号" });
    const row = (await within(panel).findByText("Work laptop")).closest("tr") as HTMLElement;

    expect(within(row).getByText("尚未绑定 CPA 渠道")).toBeInTheDocument();
    expect(row.textContent).toContain(reason);
    expect(within(row).queryByText(/插件在打开本页时会自动发布渠道/)).not.toBeInTheDocument();
  });

  // A quota hold is the state the operator acts on first, and an account can be held by the gateway
  // and still carry an older bind failure, so the hold keeps the cell and the stale reason stays out.
  it("keeps the quota hold in front of a stale bind reason", async () => {
    const until = "2026-10-03T12:00:00Z";
    const reason = "token refresh was refused (invalid_grant): the account needs a new sign-in";
    clinePassFetchMock({
      clinePassAccounts: [clinePassAccountView({
        channel_bound: true,
        channel_models: 2,
        channel_model_gaps: 0,
        quota_limited: true,
        quota_limited_until: until,
        channel_binding_error: reason,
      })],
    });

    await renderClinePass();
    const panel = await screen.findByRole("tabpanel", { name: "账号" });
    const row = (await within(panel).findByText("Work laptop")).closest("tr") as HTMLElement;

    expect(within(row).getByText("已临时停用（额度受限）")).toBeInTheDocument();
    expect(within(row).getByText(/网关以 429 拒绝了本账号/)).toBeInTheDocument();
    expect(row.textContent).toContain(formatDateTimeForLocale("zh-CN", until));
    expect(row.textContent).not.toContain(reason);
  });

  // The rejected-credential and unreadable-channel branches each explain themselves, and the bind
  // reason joins them instead of replacing them: it is what says why the automatic repair did not
  // help, while the branch hint still names the states the operator must not confuse with unbound.
  it("adds the bind reason to a rejected credential instead of replacing the repair hint", async () => {
    const reason = "token refresh was refused (invalid_grant): the account needs a new sign-in";
    clinePassFetchMock({
      clinePassAccounts: [clinePassAccountView({
        channel_bound: false,
        channel_models: 0,
        channel_model_gaps: 2,
        channel_credential_rejected: true,
        channel_binding_error: reason,
      })],
    });

    await renderClinePass();
    const panel = await screen.findByRole("tabpanel", { name: "账号" });
    const row = (await within(panel).findByText("Work laptop")).closest("tr") as HTMLElement;

    expect(within(row).getByText("凭据已被网关拒绝，正在自动修复")).toBeInTheDocument();
    expect(within(row).getByText(/插件已自动轮换令牌并重写该渠道行/)).toBeInTheDocument();
    expect(row.textContent).toContain(reason);
  });

  // Every other routing state keeps its own wording: a served account must not carry the quota hold.
  it("omits the quota hold from an account the gateway still serves", async () => {
    clinePassFetchMock({
      clinePassAccounts: [clinePassAccountView({ channel_bound: true, channel_models: 2, channel_model_gaps: 0 })],
    });

    await renderClinePass();
    const panel = await screen.findByRole("tabpanel", { name: "账号" });
    const row = (await within(panel).findByText("Work laptop")).closest("tr") as HTMLElement;

    expect(within(row).getByText("已绑定 · 已发布 2 个模型")).toBeInTheDocument();
    expect(within(row).queryByText("已临时停用（额度受限）")).not.toBeInTheDocument();
    expect(within(row).queryByText(/网关以 429 拒绝了本账号/)).not.toBeInTheDocument();
  });

  it("announces the CPA channel a completed Cline Pass sign-in bound", async () => {
    const user = userEvent.setup();
    clinePassFetchMock({
      clinePassLoginStart: clinePassLoginView({
        status: "completed",
        account: clinePassAccountView(),
        binding: { kind: "openai-compatibility", base_url: "https://api.cline.bot/api/v1", index: 0, created: true, channel_key: "openai-compatibility:0", models: 4 },
      }),
    });
    const onNotice = vi.fn();

    await renderClinePass(onNotice);
    const panel = await screen.findByRole("tabpanel", { name: "账号" });
    await user.click(within(panel).getByRole("button", { name: "复用已有的 Cline CLI 登录" }));

    await waitFor(() => expect(onNotice).toHaveBeenCalledWith(expect.stringContaining("已发布 4 个模型")));
  });

  it("warns about a failed bind from a completed sign-in without failing the sign-in", async () => {
    const user = userEvent.setup();
    clinePassFetchMock({
      clinePassLoginStart: clinePassLoginView({
        status: "completed",
        account: clinePassAccountView(),
        binding_error: "the CPA management key rejected the channel write",
      }),
    });

    await renderClinePass();
    const panel = await screen.findByRole("tabpanel", { name: "账号" });
    await user.click(within(panel).getByRole("button", { name: "复用已有的 Cline CLI 登录" }));

    const warning = await within(panel).findByRole("alert");
    expect(warning).toHaveTextContent("Cline Pass 渠道绑定失败：the CPA management key rejected the channel write");
    // The sign-in itself still succeeded, so the completed status stays on screen.
    expect(within(panel).getByText("Cline Pass 账号已登录")).toBeInTheDocument();
  });

  // The operator asked for all three product menus to open on an overview, so this workspace must
  // land on 总览 and show this product's own counters and usage without a click.
  it("opens on the overview with this product's counters and usage", async () => {
    clinePassFetchMock({
      clinePassAccounts: [clinePassAccountView({ quota_usage: clinePassQuotaUsage() })],
    });

    render(<ClinePassWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const overview = await screen.findByRole("tabpanel", { name: "总览" });
    const tabs = screen.getByRole("tablist", { name: "Cline Pass" });
    expect(within(tabs).getByRole("tab", { name: "总览" })).toHaveAttribute("aria-selected", "true");
    // The counts belong to this product, not to the OpenCode families.
    expect(within(overview).getByText("已绑定 CPA 渠道的账号")).toBeInTheDocument();
    expect(within(overview).getByText("Cline Pass 模型")).toBeInTheDocument();
    // The usage cards reuse the dashboard stat cards the operator already knows.
    expect(within(overview).getByText("总 Tokens")).toBeInTheDocument();
    expect(within(overview).getByRole("group", { name: "用量与参考价" })).toBeInTheDocument();
    // The money card reports the used allowance. The plugin no longer derives a remaining balance:
    // a balance priced at reference rates is not the allowance the subscription actually has left.
    const usageCards = within(overview).getByRole("group", { name: "用量与参考价" });
    expect(within(usageCards).getByText("已用额度")).toBeInTheDocument();
    expect(within(usageCards).getByText("$7.85")).toBeInTheDocument();
    expect(within(usageCards).getByText("相对于 $9.99 订阅的已用额度（按文档参考价折算）")).toBeInTheDocument();
    expect(within(overview).queryByText("余额")).not.toBeInTheDocument();
    expect(within(overview).queryByText("$2.14")).not.toBeInTheDocument();
    expect(screen.queryByText("对话会话")).not.toBeInTheDocument();
  });

  it("renders the three Cline Pass usage windows and the subscription context of an account", async () => {
    clinePassFetchMock({
      clinePassAccounts: [clinePassAccountView({ quota_usage: clinePassQuotaUsage() })],
    });

    await renderClinePass();

    const panel = await screen.findByRole("tabpanel", { name: "账号" });
    const row = (await within(panel).findByText("Work laptop")).closest("tr") as HTMLElement;

    // The three windows Cline documents for ClinePass, each with its reference-priced USD.
    expect(within(row).getByText("5 小时额度")).toBeInTheDocument();
    expect(within(row).getByText("$0.42")).toBeInTheDocument();
    expect(within(row).getByText("7 天额度")).toBeInTheDocument();
    expect(within(row).getByText("$3.10")).toBeInTheDocument();
    expect(within(row).getByText("30 天额度")).toBeInTheDocument();
    expect(within(row).getByText("$7.85")).toBeInTheDocument();
    // The calendar-month window carries the token totals; the flat subscription sits next to it.
    expect(within(row).getByText("本月 token：输入 2,100,000 / 输出 150,000 · 210 次请求")).toBeInTheDocument();
    expect(within(row).getByText("参考价 $7.85 / 订阅 $9.99")).toBeInTheDocument();
    // A window keeps its own token and request detail on hover instead of widening the cell.
    const fiveHourWindow = within(row).getByText("5 小时额度").closest("span") as HTMLElement;
    expect(fiveHourWindow.getAttribute("title")).toContain("120,000");
    expect(fiveHourWindow.getAttribute("title")).toContain("8,000");
    expect(fiveHourWindow.getAttribute("title")).toContain("12");
  });

  it("renders the Cline Pass reference rates and marks a model without a published rate", async () => {
    const user = userEvent.setup();
    clinePassFetchMock({ clinePassAccounts: [clinePassAccountView()] });

    await renderClinePass();

    const tabs = await screen.findByRole("tablist", { name: "Cline Pass" });
    await user.click(within(tabs).getByRole("tab", { name: "模型" }));
    const panel = await screen.findByRole("tabpanel", { name: "模型" });

    const pricedRow = (await within(panel).findByText("DeepSeek V4.1 Flash")).closest("tr") as HTMLElement;
    const pricedCells = within(pricedRow).getAllByRole("cell").map((cell) => cell.textContent);
    expect(pricedCells).toContain("$1.40");
    expect(pricedCells).toContain("$4.40");
    expect(pricedCells).toContain("$0.26");
    // The unit is stated once for the columns, exactly like the OpenCode price table.
    expect(within(panel).getByText("美元 / 百万 token")).toBeInTheDocument();
    expect(within(panel).getByText(/并非实际计费金额/)).toBeInTheDocument();

    // Cline publishes no rate for this model: the row must say so instead of showing zeros.
    const unpricedRow = within(panel).getByText("Kimi K2.6").closest("tr") as HTMLElement;
    expect(within(unpricedRow).getByText("暂无价格")).toBeInTheDocument();
  });

  it("keeps the Cline Pass usage USD labelled as a reference price, never as an amount owed", async () => {
    clinePassFetchMock({
      clinePassAccounts: [clinePassAccountView({ quota_usage: clinePassQuotaUsage() })],
    });

    await renderClinePass();

    const panel = await screen.findByRole("tabpanel", { name: "账号" });
    const row = (await within(panel).findByText("Work laptop")).closest("tr") as HTMLElement;

    // Both words stay on screen: a change that presents the reference USD as a charge must fail here.
    expect(within(row).getByText("参考价 $7.85 / 订阅 $9.99")).toBeInTheDocument();
    const usageCell = row.querySelector(".cline-pass-usage-cell") as HTMLElement;
    expect(usageCell).not.toBeNull();
    expect(usageCell.getAttribute("title")).toContain("并非实际计费金额");
  });
  // A window Cline could not reference price must show its unpriced count beside the USD, so a
  // $0 reference amount can never be read as "no usage". Both surfaces carry the marker: the
  // overview card and the account's window entry.
  it("marks Cline Pass windows whose requests could not be reference priced", async () => {
    clinePassFetchMock({
      clinePassAccounts: [clinePassAccountView({
        quota_usage: clinePassQuotaUsage({
          five_hour: { usd: 0, input_tokens: 120000, output_tokens: 8000, requests: 12, unpriced_requests: 3 },
        }),
      })],
    });

    render(<ClinePassWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const overview = await screen.findByRole("tabpanel", { name: "总览" });
    expect(within(overview).getByText("未计价 3")).toBeInTheDocument();
    expect(within(overview).getByText("未计价 3").getAttribute("title")).toContain("3");

    const tabs = screen.getByRole("tablist", { name: "Cline Pass" });
    await userEvent.setup().click(within(tabs).getByRole("tab", { name: "账号" }));
    const panel = await screen.findByRole("tabpanel", { name: "账号" });
    const row = (await within(panel).findByText("Work laptop")).closest("tr") as HTMLElement;

    // The 5-hour window keeps its "$0" reference price, with the unpriced count beside it.
    expect(within(row).getByText("$0")).toBeInTheDocument();
    expect(within(row).getByText("未计价 3")).toBeInTheDocument();
  });

  it("omits the unpriced marker when every Cline Pass window was priced", async () => {
    clinePassFetchMock({
      clinePassAccounts: [clinePassAccountView({
        quota_usage: clinePassQuotaUsage({
          five_hour: { usd: 0.42, input_tokens: 120000, output_tokens: 8000, requests: 12, unpriced_requests: 0 },
          weekly: { usd: 3.1, input_tokens: 900000, output_tokens: 60000, requests: 88, unpriced_requests: 0 },
          monthly: { usd: 7.85, input_tokens: 2100000, output_tokens: 150000, requests: 210, unpriced_requests: 0 },
        }),
      })],
    });

    render(<ClinePassWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const overview = await screen.findByRole("tabpanel", { name: "总览" });
    expect(within(overview).queryByText(/未计价/)).not.toBeInTheDocument();

    const tabs = screen.getByRole("tablist", { name: "Cline Pass" });
    await userEvent.setup().click(within(tabs).getByRole("tab", { name: "账号" }));
    const panel = await screen.findByRole("tabpanel", { name: "账号" });
    const row = (await within(panel).findByText("Work laptop")).closest("tr") as HTMLElement;
    expect(within(row).queryByText(/未计价/)).not.toBeInTheDocument();
  });

  // The cached halves of a window are usage the backend reports separately, so the totals must
  // survive the API normalizer instead of rendering as zero.
  it("shows the cached tokens a Cline Pass window reports", async () => {
    clinePassFetchMock({
      clinePassAccounts: [clinePassAccountView({
        quota_usage: clinePassQuotaUsage({
          monthly: {
            usd: 7.85, input_tokens: 2100000, output_tokens: 150000, requests: 210,
            cache_read_tokens: 4000, cache_write_tokens: 500,
          },
        }),
      })],
    });

    render(<ClinePassWorkspace refreshRevision={0} onAPIError={() => undefined} onNotice={() => undefined} />);

    const overview = await screen.findByRole("tabpanel", { name: "总览" });
    // The total card and the monthly window card both report the cached halves.
    expect(within(overview).getAllByText("缓存 4,500 token", { exact: false }).length).toBeGreaterThan(0);
  });
});

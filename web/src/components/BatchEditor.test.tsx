import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { BatchEditor } from "./BatchEditor";

describe("BatchEditor", () => {
  beforeEach(() => cleanup());
	const loadModels = async () => ({ models: [], total: 1, eligible: 1, loaded: 1, failed: 0, read_only: 0, missing: 0 });

  it("submits only opted-in fields", async () => {
    const user = userEvent.setup();
    const submit = vi.fn();
    render(<BatchEditor scopeLabel="已选 2 个账号" loadModels={loadModels} onClose={() => undefined} onSubmit={submit} />);

    expect(screen.getByRole("button", { name: "生成预览" })).toBeDisabled();
    await user.click(screen.getByLabelText("备注"));
    await user.type(screen.getByLabelText("Note 值"), "batch-note");
    await user.click(screen.getByRole("button", { name: "生成预览" }));

    expect(submit).toHaveBeenCalledWith({ note: "batch-note" });
  });

	it("submits both account request-window limits independently", async () => {
		const user = userEvent.setup();
		const submit = vi.fn();
		render(<BatchEditor scopeLabel="已选 2 个账号" loadModels={loadModels} accountConcurrency={{ supported: true, host_schema_version: 2, required_schema_version: 2 }} onClose={() => undefined} onSubmit={submit} />);

		await user.click(screen.getByRole("checkbox", { name: "请求窗口限制" }));
		await user.clear(screen.getByLabelText("请求窗口限制值"));
		await user.type(screen.getByLabelText("请求窗口限制值"), "3");
		await user.click(screen.getByRole("checkbox", { name: "同时在途并发限制" }));
		await user.clear(screen.getByLabelText("同时在途并发限制值"));
		await user.type(screen.getByLabelText("同时在途并发限制值"), "10");
		await user.click(screen.getByRole("button", { name: "生成预览" }));

		expect(submit).toHaveBeenCalledWith({ concurrency_15s_limit: 3, concurrency_limit: 10 });
	});

	it("explains and disables account concurrency on legacy CPA hosts", () => {
		render(<BatchEditor scopeLabel="已选 2 个账号" loadModels={loadModels} accountConcurrency={{ supported: false, host_schema_version: 1, required_schema_version: 2, reason: "host_schema_v2_required" }} onClose={() => undefined} onSubmit={() => undefined} />);

		expect(screen.getByRole("checkbox", { name: "请求窗口限制" })).toBeDisabled();
		expect(screen.getByRole("checkbox", { name: "同时在途并发限制" })).toBeDisabled();
		expect(screen.getAllByText(/当前 CPA 版本不支持账号并发控制/)).toHaveLength(2);
		expect(screen.getByLabelText("请求窗口限制值")).toBeDisabled();
		expect(screen.getByLabelText("同时在途并发限制值")).toBeDisabled();
	});

	it("loads current single-account values while keeping the patch explicitly opted in", async () => {
		const user = userEvent.setup();
		const submit = vi.fn();
		let resolveConfig: ((value: {
			account_id: string; disabled: boolean; priority: number; note: string; prefix: string; proxy: string;
			proxy_configured: boolean; websockets: boolean; header_names: string[];
			concurrency: { supported: true; active: number; limit: number; request_limit: number; request_window_seconds: number; used_requests: number; waiting: number };
			model_policy: { mode: "allow_only"; models: string[]; excluded_count: number };
		}) => void) | undefined;
		const loadCurrentConfig = vi.fn(() => new Promise<{
			account_id: string; disabled: boolean; priority: number; note: string; prefix: string; proxy: string;
			proxy_configured: boolean; websockets: boolean; header_names: string[];
			concurrency: { supported: true; active: number; limit: number; request_limit: number; request_window_seconds: number; used_requests: number; waiting: number };
			model_policy: { mode: "allow_only"; models: string[]; excluded_count: number };
		}>((resolve) => { resolveConfig = resolve; }));
		render(<BatchEditor scopeLabel="operator@example.com" loadModels={loadModels} loadCurrentConfig={loadCurrentConfig} onClose={() => undefined} onSubmit={submit} />);

		expect(screen.getByRole("status")).toHaveTextContent("正在加载当前账号配置");
		resolveConfig?.({
			account_id: "auth-1", disabled: true, priority: 8, note: "primary pool", prefix: "team-a",
			proxy: "http://proxy.example", proxy_configured: true, websockets: false,
			header_names: ["Authorization", "X-Team"],
			concurrency: { supported: true, active: 4, limit: 10, request_limit: 3, request_window_seconds: 15, used_requests: 2, waiting: 0 },
			model_policy: { mode: "allow_only", models: ["gpt-5.5"], excluded_count: 2 },
		});

		expect(await screen.findByText("当前账号配置")).toBeInTheDocument();
		expect(screen.getByText("并发 4/10 · 15s 请求 2/3 · 队列 0")).toBeInTheDocument();
		expect(screen.getByLabelText("请求窗口限制值")).toHaveValue(3);
		expect(screen.getByLabelText("同时在途并发限制值")).toHaveValue(10);
		expect(screen.getByText("http://proxy.example")).toBeInTheDocument();
		expect(screen.getByText("Authorization, X-Team")).toBeInTheDocument();
		expect(screen.getByLabelText("Priority 值")).toHaveValue("8");
		expect(screen.getByLabelText("Note 值")).toHaveValue("primary pool");
		expect(screen.getByRole("button", { name: "生成预览" })).toBeDisabled();

		await user.click(screen.getByLabelText("备注"));
		await user.clear(screen.getByLabelText("Note 值"));
		await user.type(screen.getByLabelText("Note 值"), "updated note");
		await user.click(screen.getByRole("button", { name: "生成预览" }));
		expect(submit).toHaveBeenCalledWith({ note: "updated note" });
		expect(loadCurrentConfig).toHaveBeenCalledTimes(1);
	});

	it("loads, updates, and clears the single-account Codex identity override", async () => {
		const user = userEvent.setup();
		const submit = vi.fn();
		const loadCurrentConfig = vi.fn(async () => ({
			account_id: "auth-codex-1",
			disabled: false,
			priority: 0,
			note: "",
			prefix: "",
			proxy: "",
			proxy_configured: false,
			websockets: true,
			header_names: [],
			model_policy: null,
			codex_identity: {
				convergence_mode: "session" as const,
				ingress_gate_enabled: true,
				allow_app_server_clients: false,
			},
		}));

		render(<BatchEditor scopeLabel="codex@example.com" loadModels={loadModels} loadCurrentConfig={loadCurrentConfig} onClose={() => undefined} onSubmit={submit} />);

		expect(await screen.findByLabelText("收敛模式")).toHaveValue("session");
		expect(screen.getByLabelText("官方客户端入口门")).toHaveValue("true");
		expect(screen.getByLabelText("App Server 客户端")).toHaveValue("false");

		await user.click(screen.getByRole("checkbox", { name: "Codex 客户端身份策略" }));
		await user.selectOptions(screen.getByLabelText("收敛模式"), "device");
		await user.selectOptions(screen.getByLabelText("官方客户端入口门"), "false");
		await user.selectOptions(screen.getByLabelText("App Server 客户端"), "true");
		await user.click(screen.getByRole("button", { name: "生成预览" }));

		expect(submit).toHaveBeenLastCalledWith({
			codex_identity: {
				convergence_mode: "device",
				ingress_gate_enabled: false,
				allow_app_server_clients: true,
			},
		});

		await user.selectOptions(screen.getByLabelText("收敛模式"), "");
		await user.selectOptions(screen.getByLabelText("官方客户端入口门"), "");
		await user.selectOptions(screen.getByLabelText("App Server 客户端"), "");
		await user.click(screen.getByRole("button", { name: "生成预览" }));

		expect(submit).toHaveBeenLastCalledWith({ codex_identity: {} });
	});

  it("keeps header values in password inputs and validates duplicate names", async () => {
    const user = userEvent.setup();
    render(<BatchEditor scopeLabel="当前筛选 3 个账号" loadModels={loadModels} onClose={() => undefined} onSubmit={() => undefined} />);

    await user.click(screen.getByLabelText("请求头"));
    expect(screen.getByLabelText("Header 值")).toHaveAttribute("type", "password");
    await user.type(screen.getByLabelText("Header 名称"), "Authorization");
    await user.type(screen.getByLabelText("Header 值"), "Bearer secret");
    await user.click(screen.getByRole("button", { name: /Header$/ }));
    const names = screen.getAllByLabelText("Header 名称");
    await user.type(names[1], "authorization");
    const values = screen.getAllByLabelText("Header 值");
    await user.type(values[1], "other");
    await user.click(screen.getByRole("button", { name: "生成预览" }));
    expect(screen.getByRole("alert")).toHaveTextContent("重复");
  });

	it("loads the effective catalog and submits allowlist or blocklist policies", async () => {
		const user = userEvent.setup();
		const submit = vi.fn();
		const load = vi.fn(async () => ({
			models: [
				{ id: "gpt-5.5", display_name: "GPT 5.5" },
				{ id: "gpt-5.6-sol", display_name: "GPT 5.6 SOL" },
			],
			total: 2,
			eligible: 2,
			loaded: 2,
			failed: 0,
			read_only: 0,
			missing: 0,
		}));
		render(<BatchEditor scopeLabel="已选 2 个账号" loadModels={load} onClose={() => undefined} onSubmit={submit} />);

		await user.click(screen.getByRole("checkbox", { name: "模型策略" }));
		expect(await screen.findByText("2 个公共模型")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "白名单模式" }));
		await user.click(screen.getByRole("checkbox", { name: /GPT 5.6 SOL/ }));
		await user.click(screen.getByRole("button", { name: "生成预览" }));

		expect(load).toHaveBeenCalledTimes(1);
		expect(submit).toHaveBeenCalledWith({ model_policy: { mode: "allow_only", models: ["gpt-5.6-sol"] } });
	});
});

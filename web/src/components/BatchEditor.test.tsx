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

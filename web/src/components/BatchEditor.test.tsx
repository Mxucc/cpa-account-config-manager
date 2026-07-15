import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { BatchEditor } from "./BatchEditor";

describe("BatchEditor", () => {
  beforeEach(() => cleanup());

  it("submits only opted-in fields", async () => {
    const user = userEvent.setup();
    const submit = vi.fn();
    render(<BatchEditor scopeLabel="已选 2 个账号" onClose={() => undefined} onSubmit={submit} />);

    expect(screen.getByRole("button", { name: "生成预览" })).toBeDisabled();
    await user.click(screen.getByLabelText("Note"));
    await user.type(screen.getByLabelText("Note 值"), "batch-note");
    await user.click(screen.getByRole("button", { name: "生成预览" }));

    expect(submit).toHaveBeenCalledWith({ note: "batch-note" });
  });

  it("keeps header values in password inputs and validates duplicate names", async () => {
    const user = userEvent.setup();
    render(<BatchEditor scopeLabel="当前筛选 3 个账号" onClose={() => undefined} onSubmit={() => undefined} />);

    await user.click(screen.getByLabelText("Headers"));
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
});

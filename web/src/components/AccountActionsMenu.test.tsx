import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { AccountActionsMenu } from "./AccountActionsMenu";

function renderMenu(overrides: Partial<React.ComponentProps<typeof AccountActionsMenu>> = {}) {
  const props: React.ComponentProps<typeof AccountActionsMenu> = {
    label: "More actions for account@example.com",
    menuLabel: "Account actions",
    refreshLabel: "Refresh token",
    deleteLabel: "Delete account",
    onRefresh: vi.fn(),
    onDelete: vi.fn(),
    ...overrides,
  };
  render(<><AccountActionsMenu {...props} /><button type="button">Outside</button></>);
  return props;
}

describe("AccountActionsMenu", () => {
  it("runs refresh and delete from one accessible menu", async () => {
    const user = userEvent.setup();
    const props = renderMenu();
    const trigger = screen.getByRole("button", { name: "More actions for account@example.com" });

    await user.click(trigger);
    let menu = await screen.findByRole("menu", { name: "Account actions" });
    await user.click(within(menu).getByRole("menuitem", { name: "Refresh token" }));
    expect(props.onRefresh).toHaveBeenCalledOnce();
    expect(screen.queryByRole("menu", { name: "Account actions" })).not.toBeInTheDocument();

    await user.click(trigger);
    menu = await screen.findByRole("menu", { name: "Account actions" });
    await user.click(within(menu).getByRole("menuitem", { name: "Delete account" }));
    expect(props.onDelete).toHaveBeenCalledOnce();
  });

  it("closes on Escape and outside interaction", async () => {
    const user = userEvent.setup();
    renderMenu();
    const trigger = screen.getByRole("button", { name: "More actions for account@example.com" });

    await user.click(trigger);
    expect(await screen.findByRole("menu", { name: "Account actions" })).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu", { name: "Account actions" })).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();

    await user.click(trigger);
    await user.click(screen.getByRole("button", { name: "Outside" }));
    expect(screen.queryByRole("menu", { name: "Account actions" })).not.toBeInTheDocument();
  });

  it("disables mutations while refresh is running", async () => {
    const user = userEvent.setup();
    renderMenu({ refreshing: true });
    await user.click(screen.getByRole("button", { name: "More actions for account@example.com" }));
    const menu = await screen.findByRole("menu", { name: "Account actions" });
    expect(within(menu).getByRole("menuitem", { name: "Refresh token" })).toBeDisabled();
    expect(within(menu).getByRole("menuitem", { name: "Delete account" })).toBeDisabled();
  });
});

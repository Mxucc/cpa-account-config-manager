import { afterEach, describe, expect, it } from "vitest";
import { initThemeSync } from "./theme";

const originalTop = window.top;

function setEmbedded(value: boolean): void {
  Object.defineProperty(window, "top", {
    value: value ? ({} as Window) : window,
    configurable: true,
  });
}

afterEach(() => {
  Object.defineProperty(window, "top", { value: originalTop, configurable: true });
  document.documentElement.removeAttribute("data-plugin-host");
  document.documentElement.removeAttribute("data-theme");
});

describe("initThemeSync", () => {
  it("marks standalone pages without reserving CPA host controls", () => {
    setEmbedded(false);

    const dispose = initThemeSync();

    expect(document.documentElement).toHaveAttribute("data-plugin-host", "standalone");
    dispose();
  });

  it("marks embedded pages so the layout reserves the CPA toolbar safe area", () => {
    setEmbedded(true);

    const dispose = initThemeSync();

    expect(document.documentElement).toHaveAttribute("data-plugin-host", "cpa");
    dispose();
  });
});

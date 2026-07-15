import { describe, expect, it } from "vitest";
import { operatorMessage } from "./operatorMessage";

describe("operatorMessage", () => {
  it("localizes stable backend reasons and preserves unknown diagnostics", () => {
    expect(operatorMessage("physical auth file changed after preview")).toBe("物理 Auth 文件在预览后已变化");
    expect(operatorMessage("provider-specific detail")).toBe("provider-specific detail");
    expect(operatorMessage()).toBe("");
  });
});

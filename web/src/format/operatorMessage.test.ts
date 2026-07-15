import { describe, expect, it } from "vitest";
import { operatorMessage } from "./operatorMessage";

describe("operatorMessage", () => {
  it("localizes stable backend reasons and preserves unknown diagnostics", () => {
    expect(operatorMessage("physical auth file changed after preview")).toBe("物理 Auth 文件在预览后已变化");
    expect(operatorMessage("job result storage is unavailable; configure data_dir to a writable directory")).toContain("data_dir");
    expect(operatorMessage("default policy changed; create a new force-sync preview")).toContain("默认策略已变化");
    expect(operatorMessage("default policy update failed")).toBe("默认策略字段更新失败");
    expect(operatorMessage("import contains more than 10000 accounts")).toBe("一次最多导入 10000 个账号");
    expect(operatorMessage("provider-specific detail")).toBe("provider-specific detail");
    expect(operatorMessage()).toBe("");
  });
});

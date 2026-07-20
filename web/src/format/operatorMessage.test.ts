import { describe, expect, it } from "vitest";
import { operatorMessage } from "./operatorMessage";

describe("operatorMessage", () => {
  it("localizes stable backend reasons and preserves unknown diagnostics", () => {
    expect(operatorMessage("physical auth file changed after preview")).toBe("物理 Auth 文件在预览后已变化");
    expect(operatorMessage("job result storage is unavailable; configure data_dir to a writable directory")).toContain("data_dir");
    expect(operatorMessage("default policy changed; create a new force-sync preview")).toContain("默认策略已变化");
    expect(operatorMessage("default policy update failed")).toBe("默认策略字段更新失败");
    expect(operatorMessage("enabling auto_delete requires explicit confirmation")).toContain("明确确认");
    expect(operatorMessage("release metadata request failed")).toContain("GitHub Release");
    expect(operatorMessage("update state could not be persisted")).toContain("data_dir");
    expect(operatorMessage("plugin_update_requires_restart")).toContain("无法覆盖");
    expect(operatorMessage("too many model tests are running")).toContain("模型测试");
    expect(operatorMessage("model contains unsupported characters or exceeds 128 characters")).toContain("模型 ID");
    expect(operatorMessage("import contains more than 10000 accounts")).toBe("一次最多导入 10000 个账号");
    expect(operatorMessage("provider-specific detail")).toBe("provider-specific detail");
    expect(operatorMessage()).toBe("");
  });

  it.each([
    ["zh-CN", "一次最多导入 10000 个账号", "Management Key 无效"],
    ["zh-TW", "一次最多匯入 10000 個帳號", "Management Key 無效"],
    ["en", "A single import supports at most 10000 accounts", "invalid management key"],
    ["ru", "За один импорт можно добавить не более 10000 учётных записей", "Недействительный Management Key"],
  ] as const)("uses the %s operator catalog", (locale, limit, invalidKey) => {
    expect(operatorMessage("import contains more than 10000 accounts", locale)).toBe(limit);
    expect(operatorMessage("invalid management key", locale)).toBe(invalidKey);
  });
});

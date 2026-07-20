import { describe, expect, it } from "vitest";
import { accountStateLabel, sourceLabel, stateLabel, technicalLabel } from "./accountDisplay";

const baseAccount = { disabled: false, unavailable: false, status: "active" } as const;

describe("account display labels", () => {
  it("presents machine states in Chinese", () => {
    expect(accountStateLabel(baseAccount as never)).toBe("正常");
    expect(accountStateLabel({ ...baseAccount, disabled: true } as never)).toBe("已禁用");
    expect(accountStateLabel({ ...baseAccount, unavailable: true } as never)).toBe("暂时不可用");
    expect(stateLabel("error")).toBe("异常");
    expect(stateLabel("unexpected")).toBe("状态未知");
  });

  it("localizes known sources while preserving useful technical identifiers", () => {
    expect(sourceLabel("file")).toBe("认证文件");
    expect(sourceLabel("custom-source")).toBe("custom-source");
    expect(technicalLabel()).toBe("未知");
    expect(technicalLabel("codex")).toBe("codex");
  });
});

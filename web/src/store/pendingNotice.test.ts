import { beforeEach, describe, expect, it } from "vitest";
import { takePendingNotice, writePendingNotice } from "./pendingNotice";

describe("pending notice", () => {
  beforeEach(() => { sessionStorage.clear(); });

  it("keeps a notice for the page that reloads after a self-update", () => {
    writePendingNotice("CPA 已重新加载插件");
    expect(takePendingNotice()).toBe("CPA 已重新加载插件");
  });

  it("shows a stored notice exactly once", () => {
    writePendingNotice("done");
    expect(takePendingNotice()).toBe("done");
    expect(takePendingNotice()).toBe("");
  });

  it("ignores an empty or whitespace-only message", () => {
    writePendingNotice("   ");
    expect(takePendingNotice()).toBe("");
    expect(sessionStorage.getItem("cpa-account-config-manager:pending-notice")).toBeNull();
  });

  it("trims the stored message", () => {
    writePendingNotice("  reloaded  ");
    expect(takePendingNotice()).toBe("reloaded");
  });
});

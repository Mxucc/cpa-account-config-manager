import { render, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../api/client";
import type { UpdateSnapshot } from "../types";
import { PluginUpdateAutomation } from "./PluginUpdateAutomation";

const snapshot: UpdateSnapshot = {
  policy: { check_enabled: true, check_interval_hours: 24, auto_update: false },
  current_version: "0.3.0",
  update_available: false,
  checking: false,
  pending: false,
};

describe("PluginUpdateAutomation", () => {
  beforeEach(() => vi.restoreAllMocks());

  it("reads status on mount without triggering a write check", async () => {
    const getStatus = vi.spyOn(api, "getEffectiveUpdateStatus").mockResolvedValue(snapshot);

    render(<PluginUpdateAutomation onAPIError={() => undefined} onNotice={() => undefined} />);

    await waitFor(() => expect(getStatus).toHaveBeenCalled());
    expect(getStatus.mock.calls.some(([checkNow]) => checkNow === true)).toBe(false);
    expect(getStatus.mock.calls[0]?.[0]).toBe(false);
    expect(getStatus.mock.calls[0]?.[1]).toBeInstanceOf(AbortSignal);
  });
});

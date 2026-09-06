import { describe, expect, it } from "vitest";
import type { Account, AIProviderChannelSnapshot, AIProviderRuntimeSnapshot } from "../types";
import { providerRuntimeSnapshotsForChannels } from "./providerRuntime";

function account(id: string): Account {
  return {
    id,
    name: id,
    disabled: false,
    unavailable: false,
    runtime_only: false,
    proxy_configured: false,
    header_count: 0,
    editable: true,
    success: 0,
    failed: 0,
  };
}

function snapshot(authIndex: string, amount = 0, active = 0, credentialBacked = false): AIProviderRuntimeSnapshot {
  return {
    provider: "openai",
    auth_index: authIndex,
    identity: credentialBacked ? `credential:provider-${authIndex}` : `auth-index:${authIndex}`,
    credential_backed: credentialBacked,
    supported: true,
    active,
    waiting: 0,
    limit: 0,
    request_limit: 0,
    request_window_seconds: 15,
    used_requests: 0,
    limit_15s: 0,
    used_60s: 0,
    used_15s: 0,
    input_tokens: 0,
    output_tokens: 0,
    reasoning_tokens: 0,
    cached_tokens: 0,
    total_tokens: 0,
    amount_usd: amount,
    rated_requests: 0,
    unrated_requests: 0,
    updated_at: "2026-09-06T00:00:00Z",
  };
}

function channel(
  kind: AIProviderChannelSnapshot["kind"],
  entry: Omit<AIProviderChannelSnapshot["entries"][number], "index">,
): AIProviderChannelSnapshot {
  return { kind, count: 1, entries: [{ ...entry, index: 0 }] };
}

describe("providerRuntimeSnapshotsForChannels", () => {
  it("does not copy an OAuth account snapshot into provider totals when identities collide", () => {
    const result = providerRuntimeSnapshotsForChannels(
      [channel("openai-compatibility", { auth_index: "shared-auth" })],
      [snapshot("shared-auth", 672.46, 2)],
      [account("shared-auth")],
    );
    expect(result).toEqual([]);
  });

  it("includes an explicitly credential-backed provider even when CPA reuses an account auth index", () => {
    const result = providerRuntimeSnapshotsForChannels(
      [channel("openai-compatibility", { auth_index: "shared-auth" })],
      [snapshot("shared-auth", 4, 1, true)],
      [account("shared-auth")],
    );
    expect(result).toHaveLength(1);
    expect(result[0]?.amount_usd).toBe(4);
  });

  it("includes only explicitly indexed generic API-key provider entries", () => {
    const result = providerRuntimeSnapshotsForChannels(
      [channel("openai-compatibility", {
        account_id: "oauth-account-id",
        api_key_entries: [{ auth_index: "provider-key" }],
      })],
      [snapshot("oauth-account-id", 99), snapshot("provider-key", 0, 1, true), snapshot("unknown", 88)],
      [account("oauth-account-id")],
    );
    expect(result).toHaveLength(1);
    expect(result[0]?.auth_index).toBe("provider-key");
    expect(result[0]?.amount_usd).toBe(0);
  });

  it("supports OpenCode account identities while ignoring disabled entries", () => {
    const result = providerRuntimeSnapshotsForChannels(
      [
        channel("opencode-go", { account_id: "go-account" }),
        { kind: "opencode-zen", count: 1, entries: [{ index: 0, account_id: "disabled-zen", disabled: true }] },
      ],
      [snapshot("go-account", 1), snapshot("disabled-zen", 2)],
    );
    expect(result.map((item) => item.auth_index)).toEqual(["go-account"]);
  });

  it("deduplicates the same runtime identity matched by multiple provider fields", () => {
    const result = providerRuntimeSnapshotsForChannels(
      [channel("openai-compatibility", {
        auth_index: "provider-key",
        api_key_entries: [{ auth_index: "provider-key" }],
      })],
      [snapshot("provider-key", 4, 0, true), snapshot("provider-key", 4, 0, true)],
    );
    expect(result).toHaveLength(1);
  });

  it("hides legacy auth-index-only snapshots from generic API-key providers", () => {
    const legacy = snapshot("provider-key", 672.46, 2);
    legacy.credential_backed = false;
    const result = providerRuntimeSnapshotsForChannels(
      [channel("openai-compatibility", { auth_index: "provider-key" })],
      [legacy],
    );
    expect(result).toEqual([]);
  });
});

import type { Account, AIProviderChannelKind, AIProviderChannelSnapshot, AIProviderRuntimeSnapshot } from "../types";

function normalized(value?: string): string {
  return (value ?? "").trim();
}

function accountIdentitySet(accounts: Account[]): Set<string> {
  const identities = new Set<string>();
  for (const account of accounts) {
    for (const value of [
      account.id,
      account.auth_id,
      account.credential?.id,
      account.credential?.auth_id,
    ]) {
      const identity = normalized(value);
      if (identity) identities.add(identity);
    }
  }
  return identities;
}

function providerEntryAuthIndexes(
  kind: AIProviderChannelKind,
  entry: AIProviderChannelSnapshot["entries"][number],
): Set<string> {
  const indexes = new Set<string>();
  const add = (value?: string) => {
    const identity = normalized(value);
    if (identity) indexes.add(identity);
  };

  // Generic API-key channels must use CPA's explicit auth-index metadata. Do
  // not treat arbitrary account_id/workspace_id fields as provider identities:
  // some CPA responses reuse those fields for OAuth account records.
  add(entry.auth_index);
  for (const keyEntry of entry.api_key_entries ?? []) add(keyEntry.auth_index);

  // OpenCode account stores do not expose api-key-entries, so their stable
  // account/workspace IDs are the only available runtime identity.
  if (kind === "opencode-go" || kind === "opencode-zen") {
    add(entry.account_id);
    add(entry.workspace_id);
  }
  return indexes;
}

function isOpenCodeKind(kind: AIProviderChannelKind): boolean {
  return kind === "opencode-go" || kind === "opencode-zen";
}

/**
 * Select only provider runtime aggregates that can be proven to belong to an
 * enabled provider entry. Ambiguous auth indexes shared with OAuth accounts
 * are excluded rather than displaying account usage as provider usage.
 */
export function providerRuntimeSnapshotsForChannels(
  channels: AIProviderChannelSnapshot[],
  snapshots: AIProviderRuntimeSnapshot[],
  accounts: Account[] = [],
): AIProviderRuntimeSnapshot[] {
  const providerAuthKinds = new Map<string, Set<AIProviderChannelKind>>();
  for (const channel of channels) {
    for (const entry of channel.entries ?? []) {
      if (entry.disabled) continue;
      for (const authIndex of providerEntryAuthIndexes(channel.kind, entry)) {
        const kinds = providerAuthKinds.get(authIndex) ?? new Set<AIProviderChannelKind>();
        kinds.add(channel.kind);
        providerAuthKinds.set(authIndex, kinds);
      }
    }
  }
  if (providerAuthKinds.size === 0) return [];

  const accountAuthIndexes = accountIdentitySet(accounts);
  const seen = new Set<string>();
  return snapshots.filter((snapshot) => {
    const authIndex = normalized(snapshot.auth_index);
    const kinds = providerAuthKinds.get(authIndex);
    if (!authIndex || !kinds) return false;
    // A real API-key provider may reuse CPA's volatile auth index with an
    // OAuth account. Only unproven historical aggregates are rejected on that
    // collision; credential-backed provider usage remains visible.
    if (accountAuthIndexes.has(authIndex) && !String(snapshot.identity || "").startsWith("credential:")) return false;
    // Auth-index-only snapshots can be historical OAuth/account telemetry from
    // before provider traffic was separated. Generic API-key channels must
    // only display credential-backed aggregates; OpenCode has a separate
    // account identity model and remains supported by its stable IDs.
    if (!String(snapshot.identity || "").startsWith("credential:") && ![...kinds].some(isOpenCodeKind)) return false;
    const identity = `${normalized(snapshot.provider)}:${authIndex}:${normalized(snapshot.identity)}`;
    if (seen.has(identity)) return false;
    seen.add(identity);
    return true;
  });
}

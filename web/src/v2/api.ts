import { getSession } from "../store/session";
import type {
  MarketSnapshot,
  ProviderAccount,
  ProviderAccountsResponse,
  ProviderOverview,
  ProviderView,
} from "./types";

const API_ROOT = "/v0/management/plugins/cpa-account-config-manager";

export class ProviderAPIError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const session = getSession();
  if (!session) throw new ProviderAPIError(401, "management key is not set");
  const response = await fetch(`${session.baseUrl}${API_ROOT}${path}`, {
    ...init,
    headers: {
      Authorization: `Bearer ${session.managementKey}`,
      Accept: "application/json",
      ...(init?.body ? { "Content-Type": "application/json" } : {}),
      ...(init?.headers ?? {}),
    },
  });
  if (response.status === 401) throw new ProviderAPIError(401, "management key was rejected");
  if (!response.ok) {
    let message = `request failed with HTTP ${response.status}`;
    try {
      const body = (await response.json()) as { error?: string; reason?: string };
      if (body.error) message = body.reason ? `${body.error}: ${body.reason}` : body.error;
    } catch {
      // A non-JSON body carries nothing an operator can act on.
    }
    throw new ProviderAPIError(response.status, message);
  }
  if (response.status === 204) return undefined as T;
  return (await response.json()) as T;
}

export const providerApi = {
  providers: () => request<{ providers: ProviderView[] }>("/providers"),
  overview: () => request<ProviderOverview>("/providers/overview"),
  accounts: (provider?: string) =>
    request<ProviderAccountsResponse>(provider ? `/providers/accounts?provider=${encodeURIComponent(provider)}` : "/providers/accounts"),
  account: (id: string) => request<ProviderAccount>(`/providers/account?id=${encodeURIComponent(id)}`),
  pricing: (provider: string) =>
    request<{ provider: string; available: boolean; reason?: string; snapshot?: unknown }>(
      `/providers/pricing?provider=${encodeURIComponent(provider)}`,
    ),
  refresh: (provider: string, id: string) =>
    request<unknown>(`/providers/account/refresh?provider=${encodeURIComponent(provider)}&id=${encodeURIComponent(id)}`, { method: "POST" }),
  test: (provider: string, body: unknown) =>
    request<unknown>(`/providers/account/test?provider=${encodeURIComponent(provider)}`, {
      method: "POST",
      body: JSON.stringify(body),
    }),
  // removeChannel deletes the CPA channel this plugin published for a community
  // provider. It is the only removable object such an account has, because it owns
  // no CPA auth file.
  removeChannel: (provider: string) =>
    request<{ status?: string; error?: string; reason?: string }>(
      `/providers/account/channel?provider=${encodeURIComponent(provider)}`,
      { method: "DELETE" },
    ),
  bind: (provider: string, body: unknown) =>
    request<unknown>(`/providers/account/bind?provider=${encodeURIComponent(provider)}`, {
      method: "POST",
      body: JSON.stringify(body),
    }),
  importManifest: (manifest: string, source: string) =>
    request<{ provider: unknown }>("/providers/import", {
      method: "POST",
      body: JSON.stringify({ manifest, source }),
    }),
  // The market listing is what turns "paste a package.json" into one click: the
  // plugin fetches the entry's published manifest itself.
  market: (locale: string) =>
    request<MarketSnapshot>(`/providers/market?locale=${encodeURIComponent(locale)}`),
  importMarketPlugin: (id: string) =>
    request<{ provider: Record<string, unknown> }>("/providers/import", {
      method: "POST",
      body: JSON.stringify({ id }),
    }),
  removeImport: (id: string) =>
    request<{ status: string; id: string }>(`/providers/import?id=${encodeURIComponent(id)}`, { method: "DELETE" }),
  catalog: () =>
    request<{
      presets: Array<Record<string, unknown>>;
      imported: Array<Record<string, unknown>>;
      hook_execution_enabled: boolean;
      bun_available: boolean;
      plugins_dir?: string;
    }>("/providers/catalog"),
  settings: () => request<{ magpie_hook_execution: boolean }>("/providers/settings"),
  saveSettings: (settings: { magpie_hook_execution: boolean }) =>
    request<{ magpie_hook_execution: boolean }>("/providers/settings", {
      method: "PUT",
      body: JSON.stringify(settings),
    }),

};


// A local-only connection is what the plugin page normally has; the login dialog
// still lets an operator point at another loopback proxy.
export const hasSession = () => getSession() !== null;

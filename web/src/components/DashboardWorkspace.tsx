import { Activity, AlertTriangle, Boxes, CircleDollarSign, HeartPulse, RefreshCw, ShieldCheck, Users } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import * as api from "../api/client";
import { useI18n } from "../i18n";
import type { Account, AccountListResponse, AIProviderChannelSnapshot, AIProviderRuntimeResponse, InspectionSnapshot } from "../types";
import { IconButton } from "./IconButton";

interface DashboardWorkspaceProps {
  onAPIError: (error: unknown) => void;
}

async function optional<T>(request: Promise<T>): Promise<T | null> {
  try { return await request; }
  catch (error) {
    if (error instanceof api.APIError && error.status === 401) throw error;
    return null;
  }
}

interface DashboardData {
  accounts: Account[];
  inspection: InspectionSnapshot | null;
  runtime: AIProviderRuntimeResponse | null;
  providers: AIProviderChannelSnapshot[];
  updatedAt: string;
}

// The dashboard is a convenience summary, not an unbounded account export.
// Cap fan-out so a malformed/stale total cannot turn opening the dashboard
// into thousands of concurrent requests.
const DASHBOARD_MAX_PAGES = 100;
const DASHBOARD_PAGE_CONCURRENCY = 6;

export function normalizeDashboardPageCount(value: unknown): number {
  if (!Number.isFinite(Number(value))) return 1;
  return Math.min(DASHBOARD_MAX_PAGES, Math.max(1, Math.floor(Number(value))));
}

export function normalizeDashboardPageSize(value: unknown): number {
  if (!Number.isFinite(Number(value))) return 1000;
  return Math.min(1000, Math.max(1, Math.floor(Number(value))));
}

function accountKey(account: Account): string {
  return String(account.id || account.auth_id || account.name || account.email || "").trim();
}

function uniqueAccounts(accounts: Account[]): Account[] {
  const seen = new Set<string>();
  return accounts.filter((account) => {
    const key = accountKey(account);
    if (!key) return true;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

export async function collectDashboardAccounts(first: AccountListResponse, fetchPage = api.listAccounts, signal?: AbortSignal): Promise<Account[]> {
  const firstAccounts = Array.isArray(first?.accounts) ? first.accounts : [];
  const pageSize = normalizeDashboardPageSize(first?.page_size);
  const total = Number(first?.total);
  // Prefer the server's total when it is sane. If it is absent or malformed,
  // do not trust an attacker-controlled/buggy `pages` value at all; the
  // dashboard can still render the first page without issuing a request storm.
  const derivedPages = Number.isFinite(total) && total > 0
    ? Math.ceil(total / pageSize)
    : 1;
  const pagesCount = Math.min(DASHBOARD_MAX_PAGES, Math.max(1, normalizeDashboardPageCount(derivedPages)));
  if (pagesCount <= 1) return uniqueAccounts(firstAccounts);

  const pages = Array.from({ length: pagesCount - 1 }, (_, index) => index + 2);
  const responses: AccountListResponse[] = [];
  for (let offset = 0; offset < pages.length; offset += DASHBOARD_PAGE_CONCURRENCY) {
    const batch = pages.slice(offset, offset + DASHBOARD_PAGE_CONCURRENCY);
    if (signal?.aborted) return uniqueAccounts(firstAccounts);
    const settled = await Promise.allSettled(batch.map((page) =>
      fetchPage(page, pageSize, {}, { field: "account", order: "asc" }, signal),
    ));
    for (const result of settled) {
      if (result.status === "fulfilled" && result.value) {
        responses.push(result.value);
      } else if (result.status === "rejected" && result.reason instanceof api.APIError && result.reason.status === 401) {
        throw result.reason;
      }
    }
  }
  return uniqueAccounts([firstAccounts, ...responses.map((response) =>
    Array.isArray(response?.accounts) ? response.accounts : [],
  )].flat());
}

function formatUSD(value: number): string {
  const normalized = Number.isFinite(value) ? Math.max(0, value) : 0;
  return new Intl.NumberFormat(undefined, { style: "currency", currency: "USD", maximumFractionDigits: 4 }).format(normalized);
}

function safeDashboardNumber(value: unknown): number {
  const number = Number(value);
  return Number.isFinite(number) ? number : 0;
}

function safeDashboardPercent(value: unknown): number {
  return Math.min(100, Math.max(0, safeDashboardNumber(value)));
}

export function countUnhealthyAccounts(accounts: Account[]): number {
  return accounts.filter((account) =>
    account.unavailable ||
    account.status === "error" ||
    (account.automation?.health !== undefined && account.automation?.health !== "healthy")
  ).length;
}

export function DashboardWorkspace({ onAPIError }: DashboardWorkspaceProps) {
  const { tx, formatDateTime, formatNumber } = useI18n();
  const [data, setData] = useState<DashboardData>({ accounts: [], inspection: null, runtime: null, providers: [], updatedAt: "" });
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [refreshToken, setRefreshToken] = useState(0);

  const refresh = useCallback(async (signal?: AbortSignal) => {
    setLoading(true);
    setError("");
    try {
      const [accountPage, inspection, runtime, providers] = await Promise.all([
        api.listAccounts(1, 1000, {}, { field: "account", order: "asc" }, signal),
        optional(api.getInspection(signal)),
        optional(api.getAIProviderRuntime(signal)),
        optional(api.listAIProviderChannels(signal)),
      ]);
      if (signal?.aborted) return;
      const accounts = await collectDashboardAccounts(accountPage, api.listAccounts, signal);
      if (signal?.aborted) return;
      setData({ accounts, inspection, runtime, providers: providers ?? [], updatedAt: new Date().toISOString() });
    } catch (caught) {
      if (signal?.aborted) return;
      if (caught instanceof api.APIError && caught.status === 401) onAPIError(caught);
      setError(caught instanceof Error ? caught.message : tx("ui.request_failed"));
    } finally {
      if (!signal?.aborted) setLoading(false);
    }
  }, [onAPIError, tx]);

  useEffect(() => {
    const controller = new AbortController();
    void refresh(controller.signal);
    return () => controller.abort();
  }, [refresh, refreshToken]);

  const summary = useMemo(() => {
    const accounts = data.accounts;
    const enabled = accounts.filter((account) => !account.disabled).length;
    const disabled = accounts.filter((account) => account.disabled).length;
    const unhealthy = countUnhealthyAccounts(accounts);
    const healthy = Math.max(0, accounts.length - unhealthy);
    const providerCount = data.providers.reduce((total, channel) => total + (Array.isArray(channel.entries) ? channel.entries.length : 0), 0);
    const providerCredentialCount = data.providers.reduce((total, channel) => total + Math.max(0, safeDashboardNumber(channel.count ?? channel.entries?.length ?? 0)), 0);
    const providerEnabled = data.providers.reduce((total, channel) => total + (channel.entries ?? []).filter((entry) => !entry.disabled).length, 0);
    const providerDisabled = data.providers.reduce((total, channel) => total + (channel.entries ?? []).filter((entry) => entry.disabled === true).length, 0);
    const providerIndexes = new Set<string>();
    for (const channel of data.providers) {
      for (const entry of channel.entries ?? []) {
        const topLevel = String(entry.auth_index ?? "").trim();
        if (topLevel) providerIndexes.add(topLevel);
        for (const keyEntry of entry.api_key_entries ?? []) {
          const authIndex = String(keyEntry.auth_index ?? "").trim();
          if (authIndex) providerIndexes.add(authIndex);
        }
      }
    }
    // Runtime callbacks may include both auth files and API-key channels. Only
    // snapshots explicitly identified as a configured provider are added here;
    // account usage is already persisted and counted above.
    const providerSnapshots = (data.runtime?.snapshots ?? []).filter((snapshot) => {
      const authIndex = String(snapshot.auth_index ?? "").trim();
      return authIndex !== "" && providerIndexes.has(authIndex);
    });
    const providerTokens = providerSnapshots.reduce((total, snapshot) => total + Math.max(0, safeDashboardNumber(snapshot.total_tokens)), 0);
    const providerCost = providerSnapshots.reduce((total, snapshot) => total + Math.max(0, safeDashboardNumber(snapshot.amount_usd)), 0);
    const tokens = accounts.reduce((total, account) => total + Math.max(0, safeDashboardNumber(account.usage?.total_tokens)), 0) + providerTokens;
    const cost = accounts.reduce((total, account) => total + Math.max(0, safeDashboardNumber(account.usage?.credit?.amount_usd)), 0) + providerCost;
    const active = (data.runtime?.snapshots ?? []).reduce((total, snapshot) => total + Math.max(0, safeDashboardNumber(snapshot.active)), 0);
    const providers = providerCount;
    const modelCosts = new Map<string, number>();
    for (const snapshot of providerSnapshots) {
      for (const model of snapshot.models ?? []) {
        const name = String(model.model || "").trim();
        const amount = safeDashboardNumber(model.amount_usd);
        if (!name || amount < 0) continue;
        modelCosts.set(name, (modelCosts.get(name) ?? 0) + amount);
      }
    }
    const topModels = [...modelCosts.entries()].sort((a, b) => b[1] - a[1]).slice(0, 5);
    return { enabled, disabled, unhealthy, healthy, tokens, cost, active, providers, topModels, providerCount, providerCredentialCount, providerEnabled, providerDisabled };
  }, [data.accounts, data.providers, data.runtime]);

  return (
    <section className="dashboard-workspace" aria-label={tx("ui.dashboard")}>
      <div className="workspace-heading dashboard-heading">
        <div><h2>{tx("ui.dashboard")}</h2><p>{tx("ui.dashboard_description")}</p></div>
        <div className="workspace-heading-actions">
          {data.updatedAt ? <span className="muted-text">{tx("ui.last_updated_time", { time: formatDateTime(data.updatedAt) })}</span> : null}
          <button className="button" type="button" disabled={loading} onClick={() => setRefreshToken((value) => value + 1)}>{loading ? <RefreshCw className="spin" size={16} /> : <RefreshCw size={16} />}{tx("ui.refresh")}</button>
        </div>
      </div>
      {error ? <div className="notice-bar warning" role="alert"><AlertTriangle size={16} />{error}</div> : null}
      <div className="dashboard-grid">
        <article className="dashboard-card"><div className="dashboard-card-icon"><Users size={18} /></div><span>{tx("ui.total_accounts_and_providers")}</span><strong>{formatNumber(data.accounts.length + summary.providerCredentialCount)}</strong><small>{tx("ui.enabled_disabled_summary", { enabled: summary.enabled, disabled: summary.disabled })} · {tx("ui.provider_credentials_enabled_disabled", { enabled: summary.providerEnabled, disabled: summary.providerDisabled })}</small></article>
        <article className="dashboard-card"><div className="dashboard-card-icon success"><HeartPulse size={18} /></div><span>{tx("ui.healthy_accounts")}</span><strong>{formatNumber(summary.healthy)}</strong><small>{tx("ui.unhealthy_count", { count: summary.unhealthy })}</small></article>
        <article className="dashboard-card"><div className="dashboard-card-icon warning"><ShieldCheck size={18} /></div><span>{tx("ui.total_tokens")}</span><strong>{formatNumber(summary.tokens)}</strong><small>{tx("ui.provider_count", { count: summary.providers })}</small></article>
        <article className="dashboard-card"><div className="dashboard-card-icon accent"><CircleDollarSign size={18} /></div><span>{tx("ui.total_cost")}</span><strong>{formatUSD(summary.cost)}</strong><small>{tx("ui.active_requests_count", { count: summary.active })}</small></article>
      </div>
      <div className="dashboard-panels">
        <section className="dashboard-panel"><div className="panel-title"><span><Activity size={16} />{tx("ui.inspection_summary")}</span>{data.inspection?.running ? <span className="status-pill running">{tx("ui.running")}</span> : null}</div>
          {data.inspection ? <div className="dashboard-list"><div><span>{tx("ui.last_run")}</span><strong>{data.inspection.last_run?.finished_at ? formatDateTime(data.inspection.last_run.finished_at) : "-"}</strong></div><div><span>{tx("ui.anomaly_accounts")}</span><strong>{formatNumber(Math.max(0, safeDashboardNumber(data.inspection.anomaly_count)))} ({safeDashboardPercent(data.inspection.anomaly_percent).toFixed(1)}%)</strong></div><div><span>{tx("ui.pending_actions")}</span><strong>{formatNumber(Math.max(0, safeDashboardNumber(data.inspection.action_count)))}</strong></div></div> : <p className="muted-text">{tx("ui.dashboard_data_unavailable")}</p>}
        </section>
        <section className="dashboard-panel"><div className="panel-title"><span><Boxes size={16} />{tx("ui.top_model_costs")}</span></div>
          {summary.topModels.length ? <div className="dashboard-list">{summary.topModels.map(([model, amount]) => <div key={model}><span className="model-name">{model}</span><strong>{formatUSD(amount)}</strong></div>)}</div> : <p className="muted-text">{tx("ui.no_rated_usage")}</p>}
        </section>
      </div>
    </section>
  );
}

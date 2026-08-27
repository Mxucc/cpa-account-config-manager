import { Activity, AlertTriangle, ArrowUpRight, Boxes, CircleDollarSign, Clock3, HeartPulse, RefreshCw, ShieldCheck, Sparkles, Users, Zap } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import * as api from "../api/client";
import { useI18n } from "../i18n";
import type { Account, AccountListResponse, AIProviderChannelSnapshot, AIProviderRuntimeResponse, InspectionHealth, InspectionSnapshot, OperationEntry, UsageWindowSnapshot } from "../types";
import { accountHealth, buildAttentionItems, buildDashboardTimeline, buildHealthDistribution, buildRequestTrend, HEALTH_STATES, safeMetric } from "./dashboardMetrics";

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
  operations: OperationEntry[];
  updatedAt: string;
}

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
  const derivedPages = Number.isFinite(total) && total > 0 ? Math.ceil(total / pageSize) : 1;
  const pagesCount = Math.min(DASHBOARD_MAX_PAGES, Math.max(1, normalizeDashboardPageCount(derivedPages)));
  if (pagesCount <= 1) return uniqueAccounts(firstAccounts);
  const pages = Array.from({ length: pagesCount - 1 }, (_, index) => index + 2);
  const responses: AccountListResponse[] = [];
  for (let offset = 0; offset < pages.length; offset += DASHBOARD_PAGE_CONCURRENCY) {
    const batch = pages.slice(offset, offset + DASHBOARD_PAGE_CONCURRENCY);
    if (signal?.aborted) return uniqueAccounts(firstAccounts);
    const settled = await Promise.allSettled(batch.map((page) => fetchPage(page, pageSize, {}, { field: "account", order: "asc" }, signal)));
    for (const result of settled) {
      if (result.status === "fulfilled" && result.value) responses.push(result.value);
      else if (result.status === "rejected" && result.reason instanceof api.APIError && result.reason.status === 401) throw result.reason;
    }
  }
  return uniqueAccounts([firstAccounts, ...responses.map((response) => Array.isArray(response?.accounts) ? response.accounts : [])].flat());
}

function safeDashboardNumber(value: unknown): number {
  const number = Number(value);
  return Number.isFinite(number) ? number : 0;
}

function safeDashboardPercent(value: unknown): number {
  return Math.min(100, Math.max(0, safeDashboardNumber(value)));
}

export function countUnhealthyAccounts(accounts: Account[]): number {
  return accounts.filter((account) => accountHealth(account) !== "healthy").length;
}

function formatUSD(value: number): string {
  const normalized = Number.isFinite(value) ? Math.max(0, value) : 0;
  return new Intl.NumberFormat(undefined, { style: "currency", currency: "USD", maximumFractionDigits: 4 }).format(normalized);
}

function healthKey(health: InspectionHealth): "ui.healthy" | "ui.quota_limited" | "ui.invalid_credentials" | "ui.deactivated" | "ui.needs_review" | "ui.unavailable" | "ui.disabled" | "ui.unknown" {
  const keys: Record<InspectionHealth, "ui.healthy" | "ui.quota_limited" | "ui.invalid_credentials" | "ui.deactivated" | "ui.needs_review" | "ui.unavailable" | "ui.disabled" | "ui.unknown"> = {
    healthy: "ui.healthy",
    quota_limited: "ui.quota_limited",
    invalid_credentials: "ui.invalid_credentials",
    deactivated: "ui.deactivated",
    review: "ui.needs_review",
    unavailable: "ui.unavailable",
    disabled: "ui.disabled",
    unknown: "ui.unknown",
  };
  return keys[health];
}

function donutGradient(distribution: ReturnType<typeof buildHealthDistribution>): string {
  const colors = ["#2dd4bf", "#f59e0b", "#fb7185", "#a78bfa", "#fbbf24", "#60a5fa", "#94a3b8", "#64748b"];
  let offset = 0;
  return distribution.map((item, index) => {
    const start = offset;
    offset += item.percent;
    return `${colors[index]} ${start}% ${offset}%`;
  }).join(", ");
}

function HealthRing({ distribution, tx }: { distribution: ReturnType<typeof buildHealthDistribution>; tx: ReturnType<typeof useI18n>["tx"] }) {
  const total = distribution.reduce((sum, item) => sum + item.count, 0);
  const label = distribution.map((item) => `${tx(healthKey(item.health))}: ${item.count}`).join(", ");
  return (
    <div className="health-ring-wrap">
      <div className="health-ring" style={{ background: `conic-gradient(${donutGradient(distribution) || "var(--surface-3) 0 100%"})` }} role="img" aria-label={`${tx("ui.accounts")}: ${total}. ${label}`}>
        <div className="health-ring-hole"><strong>{total}</strong><span>{tx("ui.accounts")}</span></div>
      </div>
      <div className="health-legend">
        {distribution.map((item) => <div key={item.health} className={`health-legend-item health-legend-${item.health}`}><span className="health-legend-dot" /><span>{tx(healthKey(item.health))}</span><strong>{item.count}</strong><small>{item.percent.toFixed(0)}%</small></div>)}
      </div>
    </div>
  );
}

function TrendChart({ accounts, formatNumber, tx }: { accounts: Account[]; formatNumber: (value: number) => string; tx: ReturnType<typeof useI18n>["tx"] }) {
  const trend = buildRequestTrend(accounts);
  const max = Math.max(1, ...trend.map((point) => point.success + point.failed));
  const width = 620;
  const height = 180;
  const points = trend.length > 1 ? trend : [{ label: "—", success: 0, failed: 0 }];
  const successPath = points.map((point, index) => `${(index / Math.max(1, points.length - 1)) * width},${height - ((point.success / max) * (height - 24) + 12)}`).join(" ");
  const failedPath = points.map((point, index) => `${(index / Math.max(1, points.length - 1)) * width},${height - (((point.success + point.failed) / max) * (height - 24) + 12)}`).join(" ");
  const totals = trend.reduce((summary, point) => ({ success: summary.success + point.success, failed: summary.failed + point.failed }), { success: 0, failed: 0 });
  return (
    <div className="trend-chart-wrap">
      <div className="trend-summary" aria-label={tx("ui.request_activity_summary", { success: formatNumber(totals.success), failed: formatNumber(totals.failed) })}><span><i className="trend-key success" />{tx("ui.success_requests")} <strong>{formatNumber(totals.success)}</strong></span><span><i className="trend-key failed" />{tx("ui.failed_requests")} <strong>{formatNumber(totals.failed)}</strong></span></div>
      {trend.length ? <svg className="trend-chart" viewBox={`0 0 ${width} ${height}`} role="img" aria-label={tx("ui.request_activity_summary", { success: formatNumber(totals.success), failed: formatNumber(totals.failed) })}><path className="trend-area" d={`M ${successPath} L ${width},${height} L 0,${height} Z`} /><polyline className="trend-line trend-line-success" points={successPath} /><polyline className="trend-line trend-line-failed" points={failedPath} />{points.map((point, index) => <circle key={`${point.label}-${index}`} cx={(index / Math.max(1, points.length - 1)) * width} cy={height - ((point.success / max) * (height - 24) + 12)} r="3" className="trend-dot" />)}</svg> : <div className="chart-empty">{tx("ui.no_data_collected")}</div>}
      <div className="trend-labels">{trend.slice(-6).map((point) => <span key={`${point.label}-${point.timestamp}`}>{point.label}</span>)}</div>
    </div>
  );
}

function UsageWindow({ label, window, emptyLabel }: { label: string; window?: UsageWindowSnapshot; emptyLabel: string }) {
  const percent = safeDashboardPercent(window?.used_percent);
  return <div className="usage-window"><div className="usage-window-heading"><span>{label}</span><strong>{window ? `${percent.toFixed(0)}%` : "—"}</strong></div><div className="progress-track"><span style={{ width: `${percent}%` }} /></div><small>{window?.reset_at ? new Date(window.reset_at).toLocaleString() : emptyLabel}</small></div>;
}

export function DashboardWorkspace({ onAPIError }: DashboardWorkspaceProps) {
  const { tx, formatDateTime, formatNumber } = useI18n();
  const [data, setData] = useState<DashboardData>({ accounts: [], inspection: null, runtime: null, providers: [], operations: [], updatedAt: "" });
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [refreshToken, setRefreshToken] = useState(0);

  const refresh = useCallback(async (signal?: AbortSignal) => {
    setLoading(true);
    setError("");
    try {
      const [accountPage, inspection, runtime, providers, operations] = await Promise.all([
        api.listAccounts(1, 1000, {}, { field: "account", order: "asc" }, signal),
        optional(api.getInspection(signal)),
        optional(api.getAIProviderRuntime(signal)),
        optional(api.listAIProviderChannels(signal)),
        optional(api.listOperations(1, {}, signal)),
      ]);
      if (signal?.aborted) return;
      const accounts = await collectDashboardAccounts(accountPage, api.listAccounts, signal);
      if (signal?.aborted) return;
      setData({ accounts, inspection, runtime, providers: providers ?? [], operations: operations?.operations ?? [], updatedAt: new Date().toISOString() });
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
    const providerCount = data.providers.reduce((total, channel) => total + (channel.entries?.length ?? 0), 0);
    const providerCredentialCount = data.providers.reduce((total, channel) => total + Math.max(0, safeDashboardNumber(channel.count ?? channel.entries?.length ?? 0)), 0);
    const providerEnabled = data.providers.reduce((total, channel) => total + (channel.entries ?? []).filter((entry) => !entry.disabled).length, 0);
    const providerDisabled = data.providers.reduce((total, channel) => total + (channel.entries ?? []).filter((entry) => entry.disabled === true).length, 0);
    const providerIndexes = new Set<string>();
    for (const channel of data.providers) for (const entry of channel.entries ?? []) {
      if (entry.auth_index) providerIndexes.add(entry.auth_index);
      for (const keyEntry of entry.api_key_entries ?? []) if (keyEntry.auth_index) providerIndexes.add(keyEntry.auth_index);
    }
    const providerSnapshots = (data.runtime?.snapshots ?? []).filter((snapshot) => snapshot.auth_index && providerIndexes.has(snapshot.auth_index));
    const providerTokens = providerSnapshots.reduce((total, snapshot) => total + safeMetric(snapshot.total_tokens), 0);
    const providerCost = providerSnapshots.reduce((total, snapshot) => total + safeMetric(snapshot.amount_usd), 0);
    const tokens = accounts.reduce((total, account) => total + safeMetric(account.usage?.total_tokens), 0) + providerTokens;
    const cost = accounts.reduce((total, account) => total + safeMetric(account.usage?.credit?.amount_usd), 0) + providerCost;
    const active = (data.runtime?.snapshots ?? []).reduce((total, snapshot) => total + safeMetric(snapshot.active), 0);
    const topModels = new Map<string, number>();
    for (const snapshot of providerSnapshots) for (const model of snapshot.models ?? []) if (model.model) topModels.set(model.model, (topModels.get(model.model) ?? 0) + safeMetric(model.amount_usd));
    return { enabled, disabled, unhealthy, healthy: Math.max(0, accounts.length - unhealthy), providerCount, providerCredentialCount, providerEnabled, providerDisabled, tokens, cost, active, topModels: [...topModels.entries()].sort((a, b) => b[1] - a[1]).slice(0, 5) };
  }, [data.accounts, data.providers, data.runtime]);

  const distribution = useMemo(() => buildHealthDistribution(data.accounts), [data.accounts]);
  const attention = useMemo(() => buildAttentionItems(data.accounts, data.inspection, data.operations), [data.accounts, data.inspection, data.operations]);
  const timeline = useMemo(() => buildDashboardTimeline(data.inspection, data.operations, data.accounts), [data.accounts, data.inspection, data.operations]);
  const codexUsage = data.accounts.find((account) => account.usage?.codex)?.usage?.codex;

  return (
    <section className="dashboard-workspace" aria-label={tx("ui.dashboard")}>
      <div className="workspace-heading dashboard-heading"><div><div className="eyebrow"><Sparkles size={13} />{tx("ui.observability_console")}</div><h2>{tx("ui.dashboard")}</h2><p>{tx("ui.dashboard_description")}</p></div><div className="workspace-heading-actions">{data.updatedAt ? <span className="muted-text">{tx("ui.observed_at", { time: formatDateTime(data.updatedAt) })}</span> : null}<button className="button" type="button" disabled={loading} onClick={() => setRefreshToken((value) => value + 1)}>{loading ? <RefreshCw className="spin" size={16} /> : <RefreshCw size={16} />}{loading ? tx("ui.refreshing_data") : tx("ui.refresh")}</button></div></div>
      {error ? <div className="notice-bar warning" role="alert"><AlertTriangle size={16} />{error}</div> : null}
      <div className="dashboard-kpi-grid">
        <MetricCard icon={<Users size={17} />} label={tx("ui.total_accounts")} value={formatNumber(data.accounts.length)} detail={tx("ui.enabled_disabled_summary", { enabled: summary.enabled, disabled: summary.disabled })} tone="info" />
        <MetricCard icon={<HeartPulse size={17} />} label={tx("ui.healthy_accounts")} value={formatNumber(summary.healthy)} detail={tx("ui.unhealthy_count", { count: summary.unhealthy })} tone="success" />
        <MetricCard icon={<AlertTriangle size={17} />} label={tx("ui.unhealthy_accounts")} value={formatNumber(summary.unhealthy)} detail={tx("ui.observed_at", { time: data.updatedAt ? formatDateTime(data.updatedAt) : "—" })} tone="danger" />
        <MetricCard icon={<ShieldCheck size={17} />} label={tx("ui.total_tokens")} value={formatNumber(summary.tokens)} detail={tx("ui.provider_count", { count: summary.providerCount })} tone="warning" />
        <MetricCard icon={<CircleDollarSign size={17} />} label={tx("ui.total_cost")} value={formatUSD(summary.cost)} detail={tx("ui.observed_at", { time: data.updatedAt ? formatDateTime(data.updatedAt) : "—" })} tone="accent" />
        <MetricCard icon={<Zap size={17} />} label={tx("ui.active_requests")} value={formatNumber(summary.active)} detail={summary.active ? tx("ui.system_status") : tx("ui.no_active_requests")} tone="live" />
      </div>
      <div className="dashboard-main-grid">
        <section className="dashboard-panel dashboard-health-panel"><PanelHeader icon={<HeartPulse size={16} />} title={tx("ui.health_distribution")} detail={tx("ui.observed_at", { time: data.updatedAt ? formatDateTime(data.updatedAt) : "—" })} /><HealthRing distribution={distribution} tx={tx} /></section>
        <section className="dashboard-panel dashboard-trend-panel"><PanelHeader icon={<Activity size={16} />} title={tx("ui.request_activity")} detail={tx("ui.success_requests")} /><TrendChart accounts={data.accounts} formatNumber={formatNumber} tx={tx} /></section>
        <section className="dashboard-panel dashboard-usage-panel"><PanelHeader icon={<Clock3 size={16} />} title={tx("ui.usage_windows")} detail={codexUsage?.observed_at ? tx("ui.observed_at", { time: formatDateTime(codexUsage.observed_at) }) : tx("ui.no_data_collected")} /><div className="usage-window-grid"><UsageWindow label={tx("ui.codex_five_hour")} window={codexUsage?.five_hour} emptyLabel={tx("ui.no_data_collected")} /><UsageWindow label={tx("ui.codex_seven_day")} window={codexUsage?.seven_day} emptyLabel={tx("ui.no_data_collected")} /></div><div className="usage-stat-row"><span>{tx("ui.total_tokens")} <strong>{formatNumber(summary.tokens)}</strong></span><span>{tx("ui.active_requests")} <strong>{formatNumber(summary.active)}</strong></span></div></section>
        <section className="dashboard-panel dashboard-attention-panel"><PanelHeader icon={<AlertTriangle size={16} />} title={tx("ui.needs_attention")} detail={`${attention.length}`} />{attention.length ? <ul className="attention-list">{attention.map((item) => <li key={item.id} className={`attention-item ${item.tone}`}><span className="attention-dot" /><div><strong>{item.title}</strong><small>{item.detail}</small></div><ArrowUpRight size={15} /></li>)}</ul> : <EmptyState text={tx("ui.no_attention_items")} />}</section>
        <section className="dashboard-panel dashboard-timeline-panel"><PanelHeader icon={<Clock3 size={16} />} title={tx("ui.inspection_timeline")} detail={timeline.length ? `${timeline.length}` : tx("ui.no_data_collected")} />{timeline.length ? <ol className="activity-timeline">{timeline.map((event) => <li key={event.id} className={`timeline-event ${event.tone}`}><span className="timeline-marker" /><div><strong>{event.title}</strong><span>{event.detail}</span></div>{event.timestamp ? <time dateTime={event.timestamp}>{formatDateTime(event.timestamp)}</time> : null}</li>)}</ol> : <EmptyState text={tx("ui.no_data_collected")} />}</section>
        <section className="dashboard-panel dashboard-model-panel"><PanelHeader icon={<Boxes size={16} />} title={tx("ui.top_model_costs")} detail={tx("ui.total_cost")} />{summary.topModels.length ? <div className="model-cost-list">{summary.topModels.map(([model, amount]) => <div key={model}><span className="model-name">{model}</span><div className="model-cost-bar"><span style={{ width: `${summary.topModels[0]?.[1] ? (amount / summary.topModels[0][1]) * 100 : 0}%` }} /></div><strong>{formatUSD(amount)}</strong></div>)}</div> : <EmptyState text={tx("ui.no_rated_usage")} />}</section>
      </div>
      <div className="dashboard-footer-strip"><span><ShieldCheck size={14} />{tx("ui.provider_credentials_enabled_disabled", { enabled: summary.providerEnabled, disabled: summary.providerDisabled })}</span><span><Boxes size={14} />{tx("ui.total_accounts_and_providers")} · {formatNumber(data.accounts.length + summary.providerCredentialCount)}</span><span>{tx("ui.last_updated_time", { time: data.updatedAt ? formatDateTime(data.updatedAt) : "—" })}</span></div>
    </section>
  );
}

function MetricCard({ icon, label, value, detail, tone }: { icon: React.ReactNode; label: string; value: string; detail: string; tone: string }) {
  return <article className={`metric-card metric-card-${tone}`}><div className="metric-card-top"><span className="metric-icon">{icon}</span><span>{label}</span></div><strong>{value}</strong><small>{detail}</small></article>;
}

function PanelHeader({ icon, title, detail }: { icon: React.ReactNode; title: string; detail: string }) {
  return <header className="panel-header"><div><span className="panel-icon">{icon}</span><strong>{title}</strong></div><small>{detail}</small></header>;
}

function EmptyState({ text }: { text: string }) {
  return <div className="dashboard-empty"><span>—</span><p>{text}</p></div>;
}

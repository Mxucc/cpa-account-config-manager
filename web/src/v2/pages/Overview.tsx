import { providerApi } from "../api";
import { Card, CardTitle, EmptyState, ErrorState, PageHeader, Skeleton } from "../../ui/kit";
import { AccountQuota, StatusBadge, accountWindows, formatMoney, kindLabel, quotaUsedPercent, useAsync, useCopy } from "../shared";
import { useI18n } from "../../i18n";
import type { ProviderAccount } from "../types";

// The headline figures are the shared metric tiles: the same border, label size
// and value weight the automation and settings pages use.
function Stat({ label, value, tone }: { label: string; value: string; tone?: string }) {
  return (
    <div className="v2-metric">
      <p className="v2-metric-label">{label}</p>
      <p className={`v2-metric-value ${tone ?? ""}`}>{value}</p>
    </div>
  );
}

// The hotspot list answers "which accounts are closest to their limit" with the
// same normalized percentage every provider reports, never with a raw vendor field.
// An account that reported no share is not a hotspot: it is unknown, not 0%.
function worstReportedPercent(account: ProviderAccount): number | null {
  let worst: number | null = null;
  for (const window of accountWindows(account)) {
    const used = quotaUsedPercent(window);
    if (used === null) continue;
    worst = worst === null ? used : Math.max(worst, used);
  }
  return worst;
}

function hotspots(accounts: ProviderAccount[], limit = 5): ProviderAccount[] {
  const scored: Array<{ account: ProviderAccount; used: number }> = [];
  for (const account of accounts) {
    const used = worstReportedPercent(account);
    if (used === null) continue;
    scored.push({ account, used });
  }
  scored.sort((left, right) => right.used - left.used);
  return scored.slice(0, limit).map((entry) => entry.account);
}

export function OverviewPage({ onOpenAccount }: { onOpenAccount: (id: string) => void }) {
  const copy = useCopy();
  const { formatDateTime } = useI18n();
  const overview = useAsync(() => providerApi.overview(), []);
  const providers = useAsync(() => providerApi.providers(), []);
  const accounts = useAsync(() => providerApi.accounts(), []);
  const loading = overview.loading || providers.loading || accounts.loading;
  const error = overview.error || providers.error || accounts.error;

  if (loading && !overview.data) {
    return (
      <div className="v2-page">
        <PageHeader title={copy.overview.title} subtitle={copy.overview.subtitle} />
        <div className="v2-metrics">
          {[0, 1, 2, 3].map((index) => (
            <Skeleton key={index} className="h-28" />
          ))}
        </div>
      </div>
    );
  }
  if (error && !overview.data) {
    return <ErrorState message={`${copy.common.error}: ${error}`} retry={overview.reload} retryLabel={copy.common.retry} />;
  }

  const data = overview.data!;
  const byStatus = data.by_status ?? {};
  const accountList = accounts.data?.accounts ?? [];
  const hotspotsList = hotspots(accountList);

  return (
    <div className="v2-page">
      <PageHeader
        title={copy.overview.title}
        subtitle={copy.overview.subtitle}
        actions={<span className="text-xs text-gray-500">{copy.common.updated} {formatDateTime(data.updated_at)}</span>}
      />
      <div className="v2-metrics">
        <Stat label={copy.overview.accounts} value={String(data.accounts)} />
        <Stat label={copy.overview.ready} value={String(byStatus.ready ?? 0)} tone="text-green-600" />
        <Stat label={copy.overview.limited} value={String(byStatus.quota_limited ?? 0)} tone="text-amber-600" />
        <Stat label={copy.overview.invalid} value={String((byStatus.credential_invalid ?? 0) + (byStatus.repairing ?? 0))} tone="text-red-600" />
      </div>
      <div className="grid gap-4 lg:grid-cols-3">
        <Card className="v2-section lg:col-span-2">
          <CardTitle>{copy.overview.recent}</CardTitle>
          <ul className="divide-y divide-gray-100 rounded-lg border border-gray-200">
            {(providers.data?.providers ?? []).map((provider) => (
              <li key={provider.id} className="flex items-center justify-between gap-3 px-3 py-2">
                <div className="min-w-0">
                  <p className="truncate text-sm font-medium text-gray-900">{provider.display_name}</p>
                  <p className="v2-hint">{kindLabel(provider.kind, copy)}</p>
                </div>
                <div className="flex shrink-0 items-center gap-2 text-xs text-gray-600">
                  <span>
                    {provider.accounts} {copy.providers.accounts}
                  </span>
                  <StatusBadge status={byStatusFor(provider.by_status)} />
                </div>
              </li>
            ))}
          </ul>
        </Card>
        <Card className="v2-section">
          <CardTitle>{copy.overview.spend}</CardTitle>
          <p className="v2-metric-value">{formatMoney(data.publication_usd)}</p>
          <p className="v2-hint">
            {copy.overview.providers}: {data.providers}
          </p>
        </Card>
      </div>
      <Card className="v2-section">
        <CardTitle>{copy.overview.hotspots}</CardTitle>
        {hotspotsList.length === 0 ? (
          <EmptyState title={copy.common.empty} />
        ) : (
          <div className="space-y-2">
            {hotspotsList.map((account) => (
              <button
                key={account.id}
                type="button"
                onClick={() => onOpenAccount(account.id)}
                className="flex w-full items-center justify-between gap-4 rounded-lg px-2 py-2 text-left transition-all duration-150 ease-out hover:bg-gray-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-blue-500 focus-visible:ring-offset-2 active:scale-[0.99]"
              >
                <span className="min-w-0">
                  <span className="block truncate text-sm font-medium text-gray-900">{account.display_name}</span>
                  <span className="block text-xs text-gray-500">{account.provider.display_name}</span>
                </span>
                <AccountQuota account={account} compact />
              </button>
            ))}
          </div>
        )}
      </Card>
    </div>
  );
}

function byStatusFor(byStatus: Record<string, number>): string {
  if ((byStatus.credential_invalid ?? 0) > 0) return "credential_invalid";
  if ((byStatus.quota_limited ?? 0) > 0) return "quota_limited";
  if ((byStatus.ready ?? 0) > 0) return "ready";
  return "unknown";
}

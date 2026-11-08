import { providerApi } from "../api";
import { Card, CardTitle, ErrorState, PageHeader, Skeleton } from "../../ui/kit";
import { AccountQuota, StatusBadge, accountWindows, formatMoney, kindLabel, quotaUsedPercent, useAsync, useCopy } from "../shared";
import { useI18n } from "../../i18n";
import type { ProviderAccount } from "../types";

function Stat({ label, value, tone }: { label: string; value: string; tone?: string }) {
  return (
    <Card>
      <p className="text-sm text-gray-500">{label}</p>
      <p className={`mt-2 font-semibold tracking-tight text-2xl md:text-3xl ${tone ?? "text-gray-900"}`}>{value}</p>
    </Card>
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
      <div className="space-y-6">
        <PageHeader title={copy.overview.title} subtitle={copy.overview.subtitle} />
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
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
    <div className="space-y-6">
      <PageHeader
        title={copy.overview.title}
        subtitle={copy.overview.subtitle}
        actions={<span className="text-xs text-gray-500">{copy.common.updated} {formatDateTime(data.updated_at)}</span>}
      />
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Stat label={copy.overview.accounts} value={String(data.accounts)} />
        <Stat label={copy.overview.ready} value={String(byStatus.ready ?? 0)} tone="text-green-600" />
        <Stat label={copy.overview.limited} value={String(byStatus.quota_limited ?? 0)} tone="text-amber-600" />
        <Stat label={copy.overview.invalid} value={String((byStatus.credential_invalid ?? 0) + (byStatus.repairing ?? 0))} tone="text-red-600" />
      </div>
      <div className="grid gap-4 lg:grid-cols-3">
        <Card className="lg:col-span-2">
          <CardTitle>{copy.overview.recent}</CardTitle>
          <div className="mt-4 space-y-3">
            {(providers.data?.providers ?? []).map((provider) => (
              <div key={provider.id} className="flex items-center justify-between gap-4 border-b border-gray-100 pb-3 last:border-0 last:pb-0">
                <div>
                  <p className="text-sm font-medium text-gray-900">{provider.display_name}</p>
                  <p className="text-xs text-gray-500">{kindLabel(provider.kind, copy)}</p>
                </div>
                <div className="flex items-center gap-2 text-xs text-gray-600">
                  <span>{provider.accounts} {copy.providers.accounts}</span>
                  <StatusBadge status={byStatusFor(provider.by_status)} />
                </div>
              </div>
            ))}
          </div>
        </Card>
        <Card>
          <CardTitle>{copy.overview.spend}</CardTitle>
          <p className="mt-2 font-semibold tracking-tight text-2xl text-gray-900">{formatMoney(data.publication_usd)}</p>
          <p className="mt-1 text-xs text-gray-500">{copy.overview.providers}: {data.providers}</p>
        </Card>
      </div>
      <Card>
        <CardTitle>{copy.overview.hotspots}</CardTitle>
        {hotspotsList.length === 0 ? (
          <p className="mt-3 text-sm text-gray-500">{copy.common.empty}</p>
        ) : (
          <div className="mt-4 space-y-4">
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

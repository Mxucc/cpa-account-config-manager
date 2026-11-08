import { providerApi } from "../api";
import { Card, EmptyState, ErrorState, PageHeader, Skeleton } from "../../ui/kit";
import { QuotaBar, StatusBadge, useAsync, useCopy } from "../shared";
import { useI18n } from "../../i18n";

// Every provider's windows are already normalized, so this page is a flat
// projection rather than three provider-specific tables. A window with no
// reported share renders "not reported" and no meter.
export function QuotaPage({ providerFilter }: { providerFilter?: string }) {
  const copy = useCopy();
  const { formatDateTime } = useI18n();
  const accounts = useAsync(() => providerApi.accounts(providerFilter), [providerFilter]);
  if (accounts.loading && !accounts.data) {
    return (
      <div className="space-y-4">
        <PageHeader title={copy.quota.title} subtitle={copy.quota.subtitle} />
        <Skeleton className="h-80" />
      </div>
    );
  }
  if (accounts.error && !accounts.data) {
    return <ErrorState message={`${copy.common.error}: ${accounts.error}`} retry={accounts.reload} retryLabel={copy.common.retry} />;
  }
  const rows = (accounts.data?.accounts ?? []).filter((account) => account.quota.length > 0);
  return (
    <div className="space-y-4">
      <PageHeader title={copy.quota.title} subtitle={copy.quota.subtitle} />
      {rows.length === 0 ? (
        <EmptyState title={copy.common.empty} description={copy.common.notReported} />
      ) : (
        <Card className="overflow-x-auto p-0">
          <table className="w-full border-collapse text-sm">
            <thead>
              <tr className="border-b border-gray-200 text-left text-xs font-medium text-gray-500">
                <th className="px-4 py-3">{copy.accounts.provider}</th>
                <th className="px-4 py-3">{copy.accounts.plan}</th>
                <th className="px-4 py-3">{copy.quota.window}</th>
                <th className="px-4 py-3">{copy.quota.reset}</th>
                <th className="px-4 py-3">{copy.accounts.status}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((account) => (
                <tr key={account.id} className="border-b border-gray-100 align-top last:border-0 transition-colors duration-150 ease-out hover:bg-gray-50">
                  <td className="px-4 py-4">
                    <span className="block font-medium text-gray-900">{account.display_name}</span>
                    <span className="block text-xs text-gray-500">{account.provider.display_name}</span>
                  </td>
                  <td className="px-4 py-4 text-gray-700">
                    {account.plan.name || copy.common.unknown}
                    <span className="block text-xs text-gray-500">
                      {account.plan.cycle.kind === "none"
                        ? "—"
                        : account.plan.cycle.renews_at
                          ? `${account.plan.cycle.kind} · ${formatDateTime(account.plan.cycle.renews_at)}`
                          : account.plan.cycle.kind}
                    </span>
                  </td>
                  <td className="min-w-[18rem] px-4 py-4">
                    <div className="space-y-4">
                      {account.quota.map((window) => (
                        <div key={window.id}>
                          <QuotaBar window={window} />
                          {window.aside ? <span className="text-xs text-gray-400">{copy.quota.aside}</span> : null}
                        </div>
                      ))}
                    </div>
                  </td>
                  <td className="px-4 py-4 text-xs text-gray-600">
                    {account.quota
                      .map((window) => (window.reset_at ? `${window.label}: ${formatDateTime(window.reset_at)}` : `${window.label}: -`))
                      .join("\n")
                      .split("\n")
                      .map((line) => (
                        <span key={line} className="block">
                          {line}
                        </span>
                      ))}
                  </td>
                  <td className="px-4 py-4">
                    <StatusBadge status={account.status} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </Card>
      )}
    </div>
  );
}

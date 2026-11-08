import { providerApi } from "../api";
import { Card, EmptyState, ErrorState, PageHeader, Skeleton, Table, Td, Th } from "../../ui/kit";
import { QuotaBar, StatusBadge, accountWindows, cycleLabel, useAsync, useCopy } from "../shared";
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
      <div className="v2-page">
        <PageHeader title={copy.quota.title} subtitle={copy.quota.subtitle} />
        <Skeleton className="h-80" />
      </div>
    );
  }
  if (accounts.error && !accounts.data) {
    return <ErrorState message={`${copy.common.error}: ${accounts.error}`} retry={accounts.reload} retryLabel={copy.common.retry} />;
  }
  // An account that reported no window is left out: the page is about windows,
  // and a null list is "not reported" rather than an empty row.
  const rows = (accounts.data?.accounts ?? [])
    .map((account) => ({ account, windows: accountWindows(account) }))
    .filter((row) => row.windows.length > 0);
  return (
    <div className="v2-page">
      <PageHeader title={copy.quota.title} subtitle={copy.quota.subtitle} />
      {rows.length === 0 ? (
        <EmptyState title={copy.common.empty} description={copy.common.notReported} />
      ) : (
        <Card flush>
          <Table>
            <thead>
              <tr>
                <Th>{copy.accounts.provider}</Th>
                <Th>{copy.accounts.plan}</Th>
                <Th>{copy.quota.window}</Th>
                <Th>{copy.accounts.status}</Th>
              </tr>
            </thead>
            <tbody>
              {rows.map(({ account, windows }) => (
                <tr key={account.id}>
                  <Td className="max-w-[15rem]">
                    <span className="v2-truncate font-medium text-gray-900" title={account.display_name}>
                      {account.display_name}
                    </span>
                    <span className="mt-0.5 block text-xs text-gray-500">{account.provider.display_name}</span>
                  </Td>
                  <Td className="whitespace-nowrap">
                    {account.plan.name || copy.common.unknown}
                    <span className="mt-0.5 block text-xs text-gray-500">
                      {account.plan.cycle.kind === "none"
                        ? "—"
                        : account.plan.cycle.renews_at
                          ? `${cycleLabel(account.plan.cycle.kind, copy)} · ${formatDateTime(account.plan.cycle.renews_at)}`
                          : cycleLabel(account.plan.cycle.kind, copy)}
                    </span>
                  </Td>
                  <Td className="min-w-[20rem]">
                    {/* The reset time belongs to its own window, so it stays under
                        the meter instead of being repeated in a separate column. */}
                    <div className="space-y-2">
                      {windows.map((window) => (
                        <div key={window.id}>
                          <QuotaBar window={window} />
                          {window.aside ? <span className="text-xs text-gray-400">{copy.quota.aside}</span> : null}
                        </div>
                      ))}
                    </div>
                  </Td>
                  <Td>
                    <StatusBadge status={account.status} />
                  </Td>
                </tr>
              ))}
            </tbody>
          </Table>
        </Card>
      )}
    </div>
  );
}

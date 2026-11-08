import { useMemo } from "react";
import { providerApi } from "../api";
import { Badge, Card, CardTitle, EmptyState, ErrorState, PageHeader, Skeleton } from "../../ui/kit";
import { billingLabel, formatMoney, useAsync, useCopy } from "../shared";

// Billing modes are kept apart on purpose: a subscription's allowance, a
// metered channel's spend and a wallet's balance answer different questions and
// must not be summed into one number.
export function UsagePage() {
  const copy = useCopy();
  const accounts = useAsync(() => providerApi.accounts(), []);
  const rows = accounts.data?.accounts ?? [];
  const byProvider = useMemo(() => {
    const map = new Map<string, { name: string; accounts: number; tokens: number; cost: number; modes: Set<string> }>();
    for (const account of rows) {
      const entry = map.get(account.provider.id) ?? {
        name: account.provider.display_name ?? account.provider.id,
        accounts: 0,
        tokens: 0,
        cost: 0,
        modes: new Set<string>(),
      };
      entry.accounts += 1;
      entry.tokens += account.usage.total_tokens;
      entry.cost += account.usage.cost_usd;
      entry.modes.add(account.billing.mode);
      map.set(account.provider.id, entry);
    }
    return [...map.values()];
  }, [rows]);

  if (accounts.loading && !accounts.data) {
    return (
      <div className="v2-page">
        <PageHeader title={copy.usage.title} subtitle={copy.usage.subtitle} />
        <Skeleton className="h-64" />
      </div>
    );
  }
  if (accounts.error && !accounts.data) {
    return <ErrorState message={`${copy.common.error}: ${accounts.error}`} retry={accounts.reload} retryLabel={copy.common.retry} />;
  }
  if (rows.length === 0) {
    return (
      <div className="v2-page">
        <PageHeader title={copy.usage.title} subtitle={copy.usage.subtitle} />
        <EmptyState title={copy.common.empty} />
      </div>
    );
  }

  return (
    <div className="v2-page">
      <PageHeader title={copy.usage.title} subtitle={copy.usage.subtitle} />
      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        {byProvider.map((provider) => (
          <Card key={provider.name}>
            <CardTitle>{provider.name}</CardTitle>
            <p className="mt-2 text-sm text-gray-500">{provider.accounts} {copy.providers.accounts}</p>
            <div className="mt-3 flex flex-wrap gap-2">
              {[...provider.modes].map((mode) => (
                <Badge key={mode} tone={mode === "unknown" ? "gray" : "blue"}>
                  {billingLabel(mode, copy)}
                </Badge>
              ))}
            </div>
            <dl className="mt-4 grid grid-cols-2 gap-3 text-sm">
              <div>
                <dt className="text-gray-500">{copy.usage.tokens}</dt>
                <dd className="font-medium text-gray-900">{provider.tokens}</dd>
              </div>
              <div>
                <dt className="text-gray-500">{copy.usage.cost}</dt>
                <dd className="font-medium text-gray-900">{formatMoney(provider.cost)}</dd>
              </div>
            </dl>
          </Card>
        ))}
      </div>
      <Card className="overflow-x-auto p-0">
        <table className="w-full border-collapse text-sm">
          <thead>
            <tr className="border-b border-gray-200 text-left text-xs font-medium text-gray-500 whitespace-nowrap">
              <th className="px-4 py-3">{copy.accounts.provider}</th>
              <th className="px-4 py-3">{copy.usage.mode}</th>
              <th className="px-4 py-3">{copy.usage.tokens}</th>
              <th className="px-4 py-3">{copy.usage.cost}</th>
              <th className="px-4 py-3">{copy.billing_remaining}</th>
              <th className="px-4 py-3">{copy.usage.priceSource}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((account) => (
              <tr key={account.id} className="border-b border-gray-100 last:border-0 transition-colors duration-150 ease-out hover:bg-gray-50">
                <td className="px-4 py-3">
                  <span className="block font-medium text-gray-900">{account.display_name}</span>
                  <span className="block text-xs text-gray-500">{account.provider.display_name}</span>
                </td>
                <td className="px-4 py-3">
                  <Badge tone={account.billing.mode === "unknown" ? "gray" : "blue"}>{billingLabel(account.billing.mode, copy)}</Badge>
                </td>
                <td className="px-4 py-3 text-gray-900">{account.usage.total_tokens}</td>
                <td className="px-4 py-3 text-gray-900">{formatMoney(account.usage.cost_usd)}</td>
                <td className="px-4 py-3 text-gray-900">{formatMoney(account.billing.remaining, account.billing.currency)}</td>
                <td className="px-4 py-3 text-gray-600">{account.billing.rate_source ?? "-"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Card>
    </div>
  );
}

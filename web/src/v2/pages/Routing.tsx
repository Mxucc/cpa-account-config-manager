import { providerApi } from "../api";
import { Badge, Card, EmptyState, ErrorState, PageHeader, Skeleton } from "../../ui/kit";
import { useAsync, useCopy } from "../shared";

// Routing state is read from the account's channel binding. A provider whose
// binding was never readable says so instead of claiming "not published".
export function RoutingPage({ providerFilter }: { providerFilter?: string }) {
  const copy = useCopy();
  const accounts = useAsync(() => providerApi.accounts(providerFilter), [providerFilter]);
  if (accounts.loading && !accounts.data) {
    return (
      <div className="space-y-4">
        <PageHeader title={copy.routing.title} subtitle={copy.routing.subtitle} />
        <Skeleton className="h-64" />
      </div>
    );
  }
  if (accounts.error && !accounts.data) {
    return <ErrorState message={`${copy.common.error}: ${accounts.error}`} retry={accounts.reload} retryLabel={copy.common.retry} />;
  }
  const rows = (accounts.data?.accounts ?? []).filter((account) => account.binding);
  return (
    <div className="space-y-4">
      <PageHeader title={copy.routing.title} subtitle={copy.routing.subtitle} />
      {rows.length === 0 ? (
        <EmptyState title={copy.common.empty} description={copy.routing.subtitle} />
      ) : (
        <Card className="overflow-x-auto p-0">
          <table className="w-full border-collapse text-sm">
            <thead>
              <tr className="border-b border-gray-200 text-left text-xs font-medium text-gray-500">
                <th className="px-4 py-3">{copy.accounts.provider}</th>
                <th className="px-4 py-3">{copy.routing.channel}</th>
                <th className="px-4 py-3">{copy.routing.published}</th>
                <th className="px-4 py-3">{copy.routing.models}</th>
                <th className="px-4 py-3">{copy.routing.gaps}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((account) => (
                <tr key={account.id} className="border-b border-gray-100 last:border-0 transition-colors duration-150 ease-out hover:bg-gray-50">
                  <td className="px-4 py-3">
                    <span className="block font-medium text-gray-900">{account.display_name}</span>
                    <span className="block text-xs text-gray-500">{account.provider.display_name}</span>
                  </td>
                  <td className="px-4 py-3 text-gray-700">{account.binding?.kind ?? "-"}</td>
                  <td className="px-4 py-3">
                    {account.binding?.readable === false ? (
                      <Badge tone="gray">{copy.common.unknown}</Badge>
                    ) : account.binding?.published ? (
                      <Badge tone="green">{copy.routing.published}</Badge>
                    ) : (
                      <Badge tone="amber">{copy.routing.unpublished}</Badge>
                    )}
                    {account.binding?.error ? <span className="mt-1 block text-xs text-red-600">{account.binding.error}</span> : null}
                  </td>
                  <td className="px-4 py-3 text-gray-900">{account.binding?.models ?? 0}</td>
                  <td className="px-4 py-3 text-gray-900">{account.binding?.gaps ?? 0}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Card>
      )}
    </div>
  );
}

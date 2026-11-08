import { providerApi } from "../api";
import { Badge, Card, EmptyState, ErrorState, PageHeader, Skeleton, Table, Td, Th } from "../../ui/kit";
import { useAsync, useCopy } from "../shared";

// Routing state is read from the account's channel binding. A provider whose
// binding was never readable says so instead of claiming "not published".
export function RoutingPage({ providerFilter }: { providerFilter?: string }) {
  const copy = useCopy();
  const accounts = useAsync(() => providerApi.accounts(providerFilter), [providerFilter]);
  if (accounts.loading && !accounts.data) {
    return (
      <div className="v2-page">
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
    <div className="v2-page">
      <PageHeader title={copy.routing.title} subtitle={copy.routing.subtitle} />
      {rows.length === 0 ? (
        <EmptyState title={copy.common.empty} description={copy.routing.subtitle} />
      ) : (
        <Card flush>
          <Table>
            <thead>
              <tr>
                <Th>{copy.accounts.provider}</Th>
                <Th>{copy.routing.channel}</Th>
                <Th>{copy.routing.published}</Th>
                <Th>{copy.routing.models}</Th>
                <Th>{copy.routing.gaps}</Th>
              </tr>
            </thead>
            <tbody>
              {rows.map((account) => (
                <tr key={account.id}>
                  <Td>
                    <span className="block font-medium text-gray-900">{account.display_name}</span>
                    <span className="block text-xs text-gray-500">{account.provider.display_name}</span>
                  </Td>
                  <Td className="text-gray-700">{account.binding?.kind ?? "-"}</Td>
                  <Td>
                    {account.binding?.readable === false ? (
                      <Badge tone="gray">{copy.common.unknown}</Badge>
                    ) : account.binding?.published ? (
                      <Badge tone="green">{copy.routing.published}</Badge>
                    ) : (
                      <Badge tone="amber">{copy.routing.unpublished}</Badge>
                    )}
                    {account.binding?.error ? <span className="mt-1 block text-xs text-red-600">{account.binding.error}</span> : null}
                  </Td>
                  <Td className="text-gray-900">{account.binding?.models ?? 0}</Td>
                  <Td className="text-gray-900">{account.binding?.gaps ?? 0}</Td>
                </tr>
              ))}
            </tbody>
          </Table>
        </Card>
      )}
    </div>
  );
}

import { useState } from "react";
import { providerApi } from "../api";
import { Badge, Button, Card, EmptyState, ErrorState, Notice, PageHeader, Skeleton, Table, Td, Th } from "../../ui/kit";
import { useAsync, useCopy } from "../shared";
import type { BindingState } from "../types";

// Routing state is read from the account's channel binding. Every account is
// listed, including one whose channel has no model list yet: CPA routes nothing
// for such a channel, so hiding the row is what made "routing is empty" look like
// a page with no data instead of a channel that needs its catalog published.
export function RoutingPage({ providerFilter }: { providerFilter?: string }) {
  const copy = useCopy();
  const accounts = useAsync(() => providerApi.accounts(providerFilter), [providerFilter]);
  const [publishing, setPublishing] = useState("");
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");

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

  const rows = accounts.data?.accounts ?? [];

  // Publishing the provider's own catalog is the repair for a channel that was
  // written before its models were known, so the page offers it where the gap is
  // visible instead of making the operator re-sign-in.
  const publish = async (providerID: string) => {
    setPublishing(providerID);
    setError("");
    setNotice("");
    try {
      const answer = (await providerApi.bind(providerID, {})) as { error?: string; reason?: string; channels?: number };
      if (answer?.error) {
        setError(answer.reason ? `${answer.error}: ${answer.reason}` : answer.error);
        return;
      }
      setNotice(`${copy.routing.published}: ${answer?.channels ?? 0}`);
      accounts.reload();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setPublishing("");
    }
  };

  return (
    <div className="v2-page">
      <PageHeader title={copy.routing.title} subtitle={copy.routing.subtitle} />
      {notice ? <Notice tone="ok">{notice}</Notice> : null}
      {error ? <Notice tone="error">{error}</Notice> : null}
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
                <Th align="right">{copy.accounts.actions}</Th>
              </tr>
            </thead>
            <tbody>
              {rows.map((account) => {
                const binding: BindingState | undefined = account.binding;
                // A channel with no models cannot serve a request, which is the
                // state that needs an action; anything else is reported as read.
                const needsModels = binding ? binding.published === true && (binding.models ?? 0) === 0 : false;
                return (
                  <tr key={account.id}>
                    <Td>
                      <span className="v2-truncate block font-medium text-gray-900">{account.display_name}</span>
                      <span className="block text-xs text-gray-500">{account.provider.display_name}</span>
                    </Td>
                    <Td className="text-gray-700">{binding?.kind ?? "-"}</Td>
                    <Td>
                      {!binding ? (
                        <Badge tone="gray">{copy.routing.unpublished}</Badge>
                      ) : binding.readable === false ? (
                        <Badge tone="gray">{copy.common.unknown}</Badge>
                      ) : binding.published ? (
                        <Badge tone="green">{copy.routing.published}</Badge>
                      ) : (
                        <Badge tone="amber">{copy.routing.unpublished}</Badge>
                      )}
                      {binding?.error ? <span className="mt-1 block text-xs text-red-600">{binding.error}</span> : null}
                      {!binding && account.provider.kind === "plugin" ? (
                        <span className="mt-1 block text-xs text-gray-500">{copy.routing.noBinding}</span>
                      ) : null}
                    </Td>
                    <Td className="text-gray-900">{binding?.models ?? 0}</Td>
                    <Td className="text-gray-900">{binding?.gaps ?? 0}</Td>
                    <Td className="v2-cell-actions">
                      {needsModels || (!binding && account.provider.kind === "plugin") ? (
                        <Button
                          size="sm"
                          loading={publishing === account.provider.id}
                          title={copy.routing.publishModelsHint}
                          onClick={() => void publish(account.provider.id)}
                        >
                          {copy.routing.publishModels}
                        </Button>
                      ) : (
                        <span className="text-xs text-gray-400">—</span>
                      )}
                    </Td>
                  </tr>
                );
              })}
            </tbody>
          </Table>
        </Card>
      )}
      <p className="v2-hint">{copy.routing.publishModelsHint}</p>
    </div>
  );
}

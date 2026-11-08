import { useMemo, useState } from "react";
import { RefreshCw, Search } from "lucide-react";
import { providerApi } from "../api";
import {
  Badge,
  Button,
  Card,
  Drawer,
  EmptyState,
  ErrorState,
  Input,
  KeyValue,
  PageHeader,
  Select,
  Skeleton,
  Toolbar,
} from "../../ui/kit";
import { AccountQuota, StatusBadge, billingLabel, formatMoney, useAsync, useCopy } from "../shared";
import { useI18n } from "../../i18n";
import type { ProviderAccount } from "../types";

const rawAccountID = (account: ProviderAccount) =>
  account.id.startsWith(`provider:${account.provider.id}:`) ? account.id.slice(`provider:${account.provider.id}:`.length) : account.id;

export function AccountsPage({
  providerFilter,
  selectedID,
  onSelect,
  onRefreshNeeded,
  onNotice,
}: {
  providerFilter?: string;
  selectedID?: string;
  onSelect: (id: string | undefined) => void;
  onRefreshNeeded: () => void;
  onNotice: (message: string) => void;
}) {
  const copy = useCopy();
  const { formatDateTime } = useI18n();
  const providers = useAsync(() => providerApi.providers(), []);
  const accounts = useAsync(() => providerApi.accounts(), []);
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("");
  const [provider, setProvider] = useState(providerFilter ?? "");
  const [busy, setBusy] = useState("");
  const [actionError, setActionError] = useState("");

  const filtered = useMemo(() => {
    const list = accounts.data?.accounts ?? [];
    const needle = query.trim().toLowerCase();
    return list.filter((account) => {
      if (provider && account.provider.id !== provider) return false;
      if (status && account.status !== status) return false;
      if (needle && !`${account.display_name} ${account.plan.name ?? ""}`.toLowerCase().includes(needle)) return false;
      return true;
    });
  }, [accounts.data, provider, status, query]);

  const selected = filtered.find((account) => account.id === selectedID) ?? null;

  const refresh = async (account: ProviderAccount) => {
    const key = `${account.id}:refresh`;
    setBusy(key);
    setActionError("");
    try {
      await providerApi.refresh(account.provider.id, rawAccountID(account));
      onNotice(`${copy.accounts.refresh}: ${account.display_name}`);
      accounts.reload();
      onRefreshNeeded();
    } catch (caught) {
      setActionError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setBusy("");
    }
  };

  if (accounts.loading && !accounts.data) {
    return (
      <div className="space-y-4">
        <PageHeader title={copy.accounts.title} subtitle={copy.accounts.subtitle} />
        <Skeleton className="h-12" />
        <Skeleton className="h-96" />
      </div>
    );
  }
  if (accounts.error && !accounts.data) {
    return <ErrorState message={`${copy.common.error}: ${accounts.error}`} retry={accounts.reload} retryLabel={copy.common.retry} />;
  }

  return (
    <div className="space-y-4">
      <PageHeader title={copy.accounts.title} subtitle={copy.accounts.subtitle} />
      <Card className="p-4">
        <Toolbar>
          <div className="relative">
            <Search size={16} className="pointer-events-none absolute left-3 top-3 text-gray-400" aria-hidden="true" />
            <Input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={copy.common.search}
              aria-label={copy.common.search}
              className="w-56 pl-9"
            />
          </div>
          <Select value={provider} onChange={(event) => setProvider(event.target.value)} aria-label={copy.accounts.filterProvider}>
            <option value="">{copy.common.all} · {copy.accounts.provider}</option>
            {(providers.data?.providers ?? []).map((item) => (
              <option key={item.id} value={item.id}>
                {item.display_name}
              </option>
            ))}
          </Select>
          <Select value={status} onChange={(event) => setStatus(event.target.value)} aria-label={copy.accounts.filterStatus}>
            <option value="">{copy.common.all} · {copy.accounts.status}</option>
            {Object.entries(copy.status).map(([value, label]) => (
              <option key={value} value={value}>
                {label}
              </option>
            ))}
          </Select>
          <Button variant="secondary" size="sm" onClick={accounts.reload} loading={accounts.loading}>
            <RefreshCw size={14} /> {copy.common.refresh}
          </Button>
          <span className="text-xs text-gray-500">{filtered.length} / {accounts.data?.total ?? 0}</span>
        </Toolbar>
        {actionError ? <p className="mt-3 text-sm text-red-600">{actionError}</p> : null}
      </Card>

      {filtered.length === 0 ? (
        <EmptyState title={copy.accounts.empty} description={copy.accounts.subtitle} />
      ) : (
        <Card className="overflow-x-auto p-0">
          <table className="w-full border-collapse text-sm">
            <thead>
              <tr className="border-b border-gray-200 text-left text-xs font-medium text-gray-500">
                <th className="px-4 py-3">{copy.accounts.provider}</th>
                <th className="px-4 py-3">{copy.accounts.status}</th>
                <th className="px-4 py-3">{copy.accounts.plan}</th>
                <th className="px-4 py-3">{copy.accounts.quota}</th>
                <th className="px-4 py-3">{copy.accounts.billing}</th>
                <th className="px-4 py-3">{copy.accounts.models}</th>
                <th className="px-4 py-3 text-right">{copy.accounts.actions}</th>
              </tr>
            </thead>
            <tbody>
              {filtered.map((account) => (
                <tr
                  key={account.id}
                  className="border-b border-gray-100 last:border-0 transition-colors duration-150 ease-out hover:bg-gray-50"
                >
                  <td className="max-w-[16rem] px-4 py-3">
                    <button
                      type="button"
                      onClick={() => onSelect(account.id)}
                      className="rounded-lg text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-blue-500 focus-visible:ring-offset-2 active:scale-[0.99]"
                    >
                      <span className="block truncate font-medium text-gray-900">{account.display_name}</span>
                      <span className="block text-xs text-gray-500">{account.provider.display_name}</span>
                    </button>
                  </td>
                  <td className="px-4 py-3">
                    <StatusBadge status={account.status} note={account.status_note} />
                  </td>
                  <td className="px-4 py-3">
                    <span className="text-gray-900">{account.plan.name || copy.common.unknown}</span>
                    <span className="block text-xs text-gray-500">
                      {account.plan.cycle.kind === "none" ? "—" : account.plan.cycle.kind}
                    </span>
                  </td>
                  <td className="px-4 py-3">
                    <AccountQuota account={account} compact />
                  </td>
                  <td className="px-4 py-3">
                    <Badge tone={account.billing.mode === "unknown" ? "gray" : "blue"}>{billingLabel(account.billing.mode)}</Badge>
                    <span className="mt-1 block text-xs text-gray-500">
                      {formatMoney(account.billing.remaining, account.billing.currency)}
                    </span>
                  </td>
                  <td className="px-4 py-3 text-gray-900">{account.models.count}</td>
                  <td className="px-4 py-3 text-right">
                    <div className="flex justify-end gap-2">
                      <Button variant="secondary" size="sm" onClick={() => onSelect(account.id)}>
                        {copy.common.details}
                      </Button>
                      <Button
                        variant="primary"
                        size="sm"
                        loading={busy === `${account.id}:refresh`}
                        onClick={() => refresh(account)}
                        disabled={account.provider.id === "codex" && !account.credential.refreshable}
                      >
                        {copy.accounts.refresh}
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </Card>
      )}

      <AccountDrawer account={selected} open={Boolean(selected)} onClose={() => onSelect(undefined)} />
      <span className="sr-only">{formatDateTime(selected?.updated_at)}</span>
    </div>
  );
}

export function AccountDrawer({ account, open, onClose }: { account: ProviderAccount | null; open: boolean; onClose: () => void }) {
  const copy = useCopy();
  const { formatDateTime } = useI18n();
  if (!account) return null;
  return (
    <Drawer open={open} title={account.display_name} onClose={onClose}>
      <div className="space-y-6">
        <section>
          <h3 className="mb-2 text-sm font-semibold text-gray-900">{copy.accounts.status}</h3>
          <KeyValue label={copy.accounts.provider}>{account.provider.display_name ?? account.provider.id}</KeyValue>
          <KeyValue label={copy.accounts.status}>
            <StatusBadge status={account.status} />
          </KeyValue>
          <KeyValue label={copy.accounts.plan}>{account.plan.name || copy.common.unknown}</KeyValue>
          <KeyValue label={copy.quota.window}>
            {account.plan.cycle.kind === "none"
              ? copy.common.unknown
              : account.plan.cycle.renews_at
                ? `${account.plan.cycle.kind} · ${formatDateTime(account.plan.cycle.renews_at)}`
                : account.plan.cycle.kind}
          </KeyValue>
          <KeyValue label={copy.common.updated}>{formatDateTime(account.updated_at)}</KeyValue>
        </section>

        <section>
          <h3 className="mb-2 text-sm font-semibold text-gray-900">{copy.accounts.quota}</h3>
          <AccountQuota account={account} />
          {account.metrics?.length ? (
            <div className="mt-3 space-y-1">
              {account.metrics.map((metric) => (
                <KeyValue key={metric.key} label={metric.label}>
                  {metric.format === "currency" ? formatMoney(metric.value, metric.currency ?? "USD") : `${metric.value} ${metric.unit ?? ""}`}
                </KeyValue>
              ))}
            </div>
          ) : null}
        </section>

        <section>
          <h3 className="mb-2 text-sm font-semibold text-gray-900">{copy.accounts.billing}</h3>
          <KeyValue label={copy.usage.mode}>{billingLabel(account.billing.mode)}</KeyValue>
          <KeyValue label={copy.billing_allowance}>{formatMoney(account.billing.allowance, account.billing.currency)}</KeyValue>
          <KeyValue label={copy.billing_used}>{formatMoney(account.billing.used, account.billing.currency)}</KeyValue>
          <KeyValue label={copy.billing_remaining}>{formatMoney(account.billing.remaining, account.billing.currency)}</KeyValue>
          <KeyValue label={copy.usage.tokens}>{account.usage.total_tokens}</KeyValue>
          <KeyValue label={copy.usage.cost}>{formatMoney(account.usage.cost_usd)}</KeyValue>
        </section>

        <section>
          <h3 className="mb-2 text-sm font-semibold text-gray-900">{copy.accounts.models}</h3>
          <KeyValue label={copy.accounts.models}>{account.models.count}</KeyValue>
          {account.models.error ? <p className="text-xs text-red-600">{account.models.error}</p> : null}
        </section>

        {account.binding ? (
          <section>
            <h3 className="mb-2 text-sm font-semibold text-gray-900">{copy.routing.title}</h3>
            <KeyValue label={copy.routing.channel}>{account.binding.kind ?? "-"}</KeyValue>
            <KeyValue label={copy.routing.published}>
              {account.binding.published ? copy.routing.published : copy.routing.unpublished}
            </KeyValue>
            <KeyValue label={copy.routing.models}>{account.binding.models ?? 0}</KeyValue>
            <KeyValue label={copy.routing.gaps}>{account.binding.gaps ?? 0}</KeyValue>
            {account.binding.error ? <p className="text-xs text-red-600">{account.binding.error}</p> : null}
          </section>
        ) : null}

        <section>
          <h3 className="mb-2 text-sm font-semibold text-gray-900">{copy.accounts.provider}</h3>
          <KeyValue label="Source">{account.source.kind}</KeyValue>
          <KeyValue label={copy.common.details}>{account.source.ref ?? "-"}</KeyValue>
        </section>
      </div>
    </Drawer>
  );
}

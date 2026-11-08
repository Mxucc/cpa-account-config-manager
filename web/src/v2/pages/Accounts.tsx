import { useMemo, useState } from "react";
import { RefreshCw, Search } from "lucide-react";
import { providerApi } from "../api";
import { listAccounts } from "../../api/client";
import {
  Badge,
  Button,
  Card,
  Drawer,
  EmptyState,
  ErrorState,
  Input,
  KeyValue,
  Notice,
  PageHeader,
  Select,
  Skeleton,
  useErrorMessage,
  Table,
  Td,
  Th,
  Toolbar,
} from "../../ui/kit";
import { AccountQuota, accountWindows, billingLabel, cycleLabel, formatMoney, metricLabel, StatusBadge, useAsync, useCopy } from "../shared";
import { useI18n } from "../../i18n";
import type { ProviderAccount } from "../types";
import type { AccountEditableConfig, AccountListResponse, TargetScope } from "../../types";
import {
  AddAccountDialog,
  BatchDeleteDialog,
  DedupeDialog,
  DeleteAccountDialog,
  EditAccountsDialog,
  ExportAccountsDialog,
  ImportAccountsDialog,
  ModelsDialog,
  TestModelDialog,
  useAccountManagementCopy,
} from "./accounts/management";

// Every account action opens one of these dialogs. The list is the page's whole
// management surface, so the table and the drawer cannot drift apart.
type DialogKind = "edit" | "models" | "test" | "delete" | "batchEdit" | "batchDelete" | "dedupe" | "import" | "export" | "addAccount";

const rawAccountID = (account: ProviderAccount) =>
  account.id.startsWith(`provider:${account.provider.id}:`) ? account.id.slice(`provider:${account.provider.id}:`.length) : account.id;


// ManagedAccount is a normalized account plus the CPA-side id it maps to. CPA only
// edits, deletes and exports its own auth files, so an account without that id
// offers the provider-level actions alone.
type ManagedAccount = ProviderAccount & { cpaID: string | null };

interface EditableIndex {
  byAuthID: Map<string, string>;
  byName: Map<string, string>;
  byLabel: Map<string, string>;
}

// buildEditableIndex maps every join key CPA publishes to the account id its own
// endpoints take: `id` is that id, while `auth_id` / `name` / `label` are what a
// normalized account can be matched on.
function buildEditableIndex(list: AccountListResponse | null): EditableIndex {
  const index: EditableIndex = { byAuthID: new Map(), byName: new Map(), byLabel: new Map() };
  for (const account of list?.accounts ?? []) {
    const id = account.id?.trim();
    if (!id) continue;
    for (const [map, key] of [
      [index.byAuthID, account.auth_id],
      [index.byName, account.name],
      [index.byLabel, account.label],
    ] as const) {
      const value = typeof key === "string" ? key.trim() : "";
      // First writer wins: an earlier account is the one CPA listed first, which
      // keeps a duplicate label from stealing another account's id.
      if (value && !map.has(value)) map.set(value, id);
    }
  }
  return index;
}

function resolveCPAAccountID(account: ProviderAccount, index: EditableIndex): string | null {
  const candidates = [account.source?.ref, account.display_name, account.plan?.name].filter(
    (value): value is string => typeof value === "string" && value.trim() !== "",
  );
  for (const candidate of candidates) {
    const direct = index.byAuthID.get(candidate) ?? index.byName.get(candidate) ?? index.byLabel.get(candidate);
    if (direct) return direct;
  }
  return null;
}

export function AccountsPage({
  providerFilter,
  selectedID,
  onSelect,
  onRefreshNeeded,
  onNotice,
  // The provider page renders this list inside its own header and tabs, so the
  // page header and the provider filter would only repeat what is above.
  embedded = false,
}: {
  providerFilter?: string;
  selectedID?: string;
  onSelect: (id: string | undefined) => void;
  onRefreshNeeded: () => void;
  onNotice: (message: string) => void;
  embedded?: boolean;
}) {
  const copy = useCopy();
  const managementCopy = useAccountManagementCopy();
  const { formatDateTime } = useI18n();
  const providers = useAsync(() => providerApi.providers(), []);
  const accounts = useAsync(() => providerApi.accounts(), []);
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("");
  const [provider, setProvider] = useState(providerFilter ?? "");
  const [busy, setBusy] = useState("");
  const [actionError, setActionError] = useState("");
  const actionErrorText = useErrorMessage(actionError);
  // Which management dialog is open, and for which account. One slot keeps the
  // page from stacking unrelated dialogs on top of each other.
  const [dialog, setDialog] = useState<DialogKind | null>(null);
  const [dialogAccount, setDialogAccount] = useState<ManagedAccount | null>(null);
  // CPA manages its own auth files. Only those accounts can be edited, deleted,
  // model-tested or exported through CPA's endpoints, so the page resolves each
  // normalized account to the CPA-side id first and offers the rest of the
  // actions only where that id exists.
  const cpaAccounts = useAsync(() => listAccounts(1, 200, { editability: "editable" }), []);

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

  // The scope every batch action works on: what the operator currently sees. A
  // scope built from the visible rows cannot surprise anyone with accounts the
  // filters hid.
  const editableIndex = useMemo(() => buildEditableIndex(cpaAccounts.data), [cpaAccounts.data]);
  const managed = useMemo(
    () => filtered.map((account) => ({ ...account, cpaID: resolveCPAAccountID(account, editableIndex) })),
    [filtered, editableIndex],
  );
  const selectedCPAID = selected ? resolveCPAAccountID(selected, editableIndex) : null;
  const managedIDs = useMemo(
    () => managed.map((account) => account.cpaID).filter((id): id is string => Boolean(id)),
    [managed],
  );
  const filteredScope: TargetScope = useMemo(() => ({ mode: "selected", ids: managedIDs }), [managedIDs]);

  const openDialog = (kind: DialogKind, account?: ManagedAccount) => {
    setActionError("");
    setDialogAccount(account ?? null);
    setDialog(kind);
  };

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
      <div className="v2-page">
        {embedded ? null : <PageHeader title={copy.accounts.title} subtitle={copy.accounts.subtitle} />}
        <Skeleton className="h-12" />
        <Skeleton className="h-96" />
      </div>
    );
  }
  if (accounts.error && !accounts.data) {
    return <ErrorState message={`${copy.common.error}: ${accounts.error}`} retry={accounts.reload} retryLabel={copy.common.retry} />;
  }

  return (
    <div className="v2-page">
      {embedded ? null : <PageHeader title={copy.accounts.title} subtitle={copy.accounts.subtitle} />}
      <Card className="space-y-3 p-4">
        {/* Filters and the actions they scope live in one card, separated by a
            hairline: the page has one control area, not two stacked ones. */}
        <Toolbar>
          <div className="relative">
            <Search size={15} className="pointer-events-none absolute left-2.5 top-2.5 text-gray-400" aria-hidden="true" />
            <Input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={copy.common.search}
              aria-label={copy.common.search}
              className="w-52 pl-8"
            />
          </div>
          {embedded ? null : (
            <Select value={provider} onChange={(event) => setProvider(event.target.value)} aria-label={copy.accounts.filterProvider}>
              <option value="">{copy.common.all} · {copy.accounts.provider}</option>
              {(providers.data?.providers ?? []).map((item) => (
                <option key={item.id} value={item.id}>
                  {item.display_name}
                </option>
              ))}
            </Select>
          )}
          <Select value={status} onChange={(event) => setStatus(event.target.value)} aria-label={copy.accounts.filterStatus}>
            <option value="">{copy.common.all} · {copy.accounts.status}</option>
            {Object.entries(copy.status).map(([value, label]) => (
              <option key={value} value={value}>
                {label}
              </option>
            ))}
          </Select>
          <span className="ml-auto text-xs text-gray-500">
            {filtered.length} / {accounts.data?.total ?? 0}
          </span>
        </Toolbar>

        <div className="border-t border-gray-100" />

        <Toolbar>
          <Button size="sm" onClick={() => openDialog("addAccount")}>
            {managementCopy.addAccount}
          </Button>
          <Button variant="secondary" size="sm" onClick={() => accounts.reload()} loading={accounts.loading}>
            <RefreshCw size={13} /> {copy.common.refresh}
          </Button>
          <span className="mx-1 h-4 w-px bg-gray-200" aria-hidden="true" />
          <Button variant="secondary" size="sm" onClick={() => openDialog("batchEdit")} disabled={managedIDs.length === 0}>
            {managementCopy.batchEdit} ({managedIDs.length})
          </Button>
          <Button variant="secondary" size="sm" onClick={() => openDialog("batchDelete")} disabled={managedIDs.length === 0}>
            {managementCopy.batchDelete} ({managedIDs.length})
          </Button>
          <Button variant="secondary" size="sm" onClick={() => openDialog("dedupe")}>
            {managementCopy.dedupe}
          </Button>
          <span className="mx-1 h-4 w-px bg-gray-200" aria-hidden="true" />
          <Button variant="secondary" size="sm" onClick={() => openDialog("import")}>
            {managementCopy.importAccounts}
          </Button>
          <Button variant="secondary" size="sm" onClick={() => openDialog("export")} disabled={filtered.length === 0}>
            {managementCopy.exportAccounts}
          </Button>
        </Toolbar>
        {actionError ? <Notice tone="error">{actionErrorText}</Notice> : null}
      </Card>

      {filtered.length === 0 ? (
        <EmptyState title={copy.accounts.empty} description={copy.accounts.subtitle} />
      ) : (
        <Card flush>
          <Table>
            <thead>
              <tr>
                <Th>{copy.accounts.provider}</Th>
                <Th>{copy.accounts.status}</Th>
                <Th>{copy.accounts.plan}</Th>
                <Th>{copy.accounts.quota}</Th>
                <Th>{copy.accounts.billing}</Th>
                <Th>{copy.accounts.models}</Th>
                <Th align="right">{copy.accounts.actions}</Th>
              </tr>
            </thead>
            <tbody>
              {managed.map((account) => (
                <tr key={account.id}>
                  <Td className="max-w-[16rem]">
                    <button
                      type="button"
                      onClick={() => onSelect(account.id)}
                      title={account.display_name}
                      className="block w-full rounded-md text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-blue-500 focus-visible:ring-offset-2"
                    >
                      <span className="v2-truncate font-medium text-gray-900">{account.display_name}</span>
                      <span className="mt-0.5 block text-xs text-gray-500">{account.provider.display_name}</span>
                    </button>
                  </Td>
                  <Td>
                    <StatusBadge status={account.status} note={account.status_note} />
                  </Td>
                  <Td>
                    <span className="text-gray-900">{account.plan.name || copy.common.unknown}</span>
                    <span className="mt-0.5 block text-xs text-gray-500">
                      {account.plan.cycle.kind === "none" ? "—" : cycleLabel(account.plan.cycle.kind, copy)}
                    </span>
                  </Td>
                  <Td className="min-w-[13rem]">
                    <AccountQuota account={account} compact />
                  </Td>
                  <Td>
                    <Badge tone={account.billing.mode === "unknown" ? "gray" : "blue"}>{billingLabel(account.billing.mode, copy)}</Badge>
                    {/* A mode with no remaining amount renders the badge alone rather than a stray dash. */}
                    {account.billing.remaining == null ? null : (
                      <span className="mt-1 block text-xs text-gray-500">
                        {formatMoney(account.billing.remaining, account.billing.currency)}
                      </span>
                    )}
                  </Td>
                  <Td>{account.models.count}</Td>
                  {/* Two actions keep the row inside the viewport; edit and
                      delete live in the drawer, one click from the name. */}
                  <Td className="v2-cell-actions">
                    <Button variant="ghost" size="sm" onClick={() => onSelect(account.id)}>
                      {copy.common.details}
                    </Button>
                    <Button
                      variant="secondary"
                      size="sm"
                      loading={busy === `${account.id}:refresh`}
                      onClick={() => refresh(account)}
                      disabled={account.provider.id === "codex" && !account.credential.refreshable}
                    >
                      {copy.accounts.refresh}
                    </Button>
                  </Td>
                </tr>
              ))}
            </tbody>
          </Table>
        </Card>
      )}

      <AccountDrawer
        account={selected}
        cpaID={selectedCPAID}
        open={Boolean(selected)}
        onClose={() => onSelect(undefined)}
        onAction={(kind) => {
          if (!selected) return;
          const target = { ...selected, cpaID: selectedCPAID };
          onSelect(undefined);
          openDialog(kind, target);
        }}
      />
      <span className="sr-only">{formatDateTime(selected?.updated_at)}</span>

      {/* One dialog at a time; each one previews before it applies. */}
      {dialogAccount ? (
        <>
          <EditAccountsDialog
            scope={{ mode: "selected", ids: [dialogAccount.cpaID ?? ""] }}
            account={{ id: dialogAccount.cpaID ?? "", display_name: dialogAccount.display_name }}
            open={dialog === "edit"}
            onClose={() => setDialog(null)}
            onError={setActionError}
            onDone={() => {
              setDialog(null);
              accounts.reload();
              onRefreshNeeded();
            }}
          />
          <ModelsDialog
            account={{ id: dialogAccount.cpaID ?? "", display_name: dialogAccount.display_name }}
            open={dialog === "models"}
            onClose={() => setDialog(null)}
            onError={setActionError}
          />
          <TestModelDialog
            account={{ id: dialogAccount.cpaID ?? "", display_name: dialogAccount.display_name }}
            open={dialog === "test"}
            onClose={() => setDialog(null)}
            onError={setActionError}
          />
          <DeleteAccountDialog
            account={{ id: dialogAccount.cpaID ?? "", display_name: dialogAccount.display_name }}
            open={dialog === "delete"}
            onClose={() => setDialog(null)}
            onError={setActionError}
            onDone={() => {
              setDialog(null);
              accounts.reload();
              onRefreshNeeded();
            }}
          />
        </>
      ) : null}
      <EditAccountsDialog
        scope={filteredScope}
        open={dialog === "batchEdit"}
        onClose={() => setDialog(null)}
        onError={setActionError}
        onDone={() => {
          setDialog(null);
          accounts.reload();
          onRefreshNeeded();
        }}
      />
      <BatchDeleteDialog
        scope={filteredScope}
        open={dialog === "batchDelete"}
        onClose={() => setDialog(null)}
        onError={setActionError}
        onDone={() => {
          setDialog(null);
          accounts.reload();
          onRefreshNeeded();
        }}
      />
      <DedupeDialog
        open={dialog === "dedupe"}
        onClose={() => setDialog(null)}
        onError={setActionError}
        onDone={() => accounts.reload()}
      />
      <AddAccountDialog
        open={dialog === "addAccount"}
        onClose={() => setDialog(null)}
        onError={setActionError}
        onDone={() => {
          accounts.reload();
          cpaAccounts.reload();
          onRefreshNeeded();
          onNotice(managementCopy.loginDone);
        }}
      />
      <ImportAccountsDialog
        open={dialog === "import"}
        onClose={() => setDialog(null)}
        onError={setActionError}
        onDone={() => {
          setDialog(null);
          accounts.reload();
          onRefreshNeeded();
        }}
      />
      <ExportAccountsDialog
        scope={filteredScope}
        open={dialog === "export"}
        onClose={() => setDialog(null)}
        onError={setActionError}
        onNotice={onNotice}
      />
    </div>
  );
}

export function AccountDrawer({
  account,
  cpaID,
  open,
  onClose,
  onAction,
}: {
  account: ProviderAccount | null;
  cpaID: string | null;
  open: boolean;
  onClose: () => void;
  // The drawer offers the same management actions as the table row, so an
  // operator who opened the detail view does not have to go back to find them.
  onAction?: (kind: "edit" | "models" | "test" | "delete") => void;
}) {
  const copy = useCopy();
  const managementCopy = useAccountManagementCopy();
  const { formatDateTime } = useI18n();
  if (!account) return null;
  return (
    <Drawer open={open} title={account.display_name} onClose={onClose}>
      <div className="space-y-6">
        <section>
          <h3 className="v2-section-title mb-2">{copy.accounts.status}</h3>
          <KeyValue label={copy.accounts.provider}>{account.provider.display_name ?? account.provider.id}</KeyValue>
          <KeyValue label={copy.accounts.status}>
            <StatusBadge status={account.status} />
          </KeyValue>
          <KeyValue label={copy.accounts.plan}>{account.plan.name || copy.common.unknown}</KeyValue>
          <KeyValue label={copy.quota.window}>
            {account.plan.cycle.kind === "none"
              ? copy.common.unknown
              : account.plan.cycle.renews_at
                ? `${cycleLabel(account.plan.cycle.kind, copy)} · ${formatDateTime(account.plan.cycle.renews_at)}`
                : cycleLabel(account.plan.cycle.kind, copy)}
          </KeyValue>
          <KeyValue label={copy.common.updated}>{formatDateTime(account.updated_at)}</KeyValue>
        </section>

        <section>
          <h3 className="v2-section-title mb-2">{copy.accounts.quota}</h3>
          <AccountQuota account={account} />
          {account.metrics?.length ? (
            <div className="mt-3 space-y-1">
              {account.metrics.map((metric) => (
                <KeyValue key={metric.key} label={metricLabel(metric.key, metric.label, copy)}>
                  {metric.format === "currency" ? formatMoney(metric.value, metric.currency ?? "USD") : `${metric.value} ${metric.unit ?? ""}`}
                </KeyValue>
              ))}
            </div>
          ) : null}
        </section>

        <section>
          <h3 className="v2-section-title mb-2">{copy.accounts.billing}</h3>
          <KeyValue label={copy.usage.mode}>{billingLabel(account.billing.mode, copy)}</KeyValue>
          <KeyValue label={copy.billing_allowance}>{formatMoney(account.billing.allowance, account.billing.currency)}</KeyValue>
          <KeyValue label={copy.billing_used}>{formatMoney(account.billing.used, account.billing.currency)}</KeyValue>
          <KeyValue label={copy.billing_remaining}>{formatMoney(account.billing.remaining, account.billing.currency)}</KeyValue>
          <KeyValue label={copy.usage.tokens}>{account.usage.total_tokens}</KeyValue>
          <KeyValue label={copy.usage.cost}>{formatMoney(account.usage.cost_usd)}</KeyValue>
        </section>

        <section>
          <h3 className="v2-section-title mb-2">{copy.accounts.models}</h3>
          <KeyValue label={copy.accounts.models}>{account.models.count}</KeyValue>
          {account.models.error ? <p className="text-xs text-red-600">{account.models.error}</p> : null}
        </section>

        {account.binding ? (
          <section>
            <h3 className="v2-section-title mb-2">{copy.routing.title}</h3>
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
          <h3 className="v2-section-title mb-2">{copy.accounts.provider}</h3>
          <KeyValue label={copy.common.source}>{account.source.kind}</KeyValue>
          <KeyValue label={copy.common.details}>{account.source.ref ?? "-"}</KeyValue>
        </section>

        {onAction ? (
          <section>
            <h3 className="v2-section-title mb-2">{managementCopy.title}</h3>
            {cpaID ? (
              <div className="flex flex-wrap gap-2">
                <Button variant="secondary" size="sm" onClick={() => onAction("edit")}>
                  {managementCopy.edit}
                </Button>
                <Button variant="secondary" size="sm" onClick={() => onAction("models")}>
                  {managementCopy.models}
                </Button>
                <Button variant="secondary" size="sm" onClick={() => onAction("test")}>
                  {managementCopy.test}
                </Button>
                <Button variant="ghost" size="sm" onClick={() => onAction("delete")}>
                  {managementCopy.delete}
                </Button>
              </div>
            ) : (
              <p className="text-xs text-gray-500">{managementCopy.unmanaged}</p>
            )}
          </section>
        ) : null}
      </div>
    </Drawer>
  );
}

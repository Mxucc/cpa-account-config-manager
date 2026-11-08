import { useMemo, useState } from "react";
import { ArrowRight } from "lucide-react";
import { providerApi } from "../api";
import { Badge, Button, Card, CardTitle, Dialog, EmptyState, ErrorState, PageHeader, Select, Skeleton, Tabs } from "../../ui/kit";
import { StatusBadge, useAsync, useCopy } from "../shared";
import { AccountsPage } from "./Accounts";
import { QuotaPage } from "./Quota";
import { RoutingPage } from "./Routing";
import { CodexWorkspace } from "../../components/CodexWorkspace";
import { OpenCodeWorkspace } from "../../components/OpenCodeWorkspace";
import { ClinePassWorkspace } from "../../components/ClinePassWorkspace";

export type ProviderTab = "accounts" | "quota" | "models" | "routing" | "advanced";

export function ProvidersPage({
  providerID,
  tab,
  onNavigate,
  onAPIError,
  onNotice,
  refreshRevision,
}: {
  providerID?: string;
  tab?: ProviderTab;
  onNavigate: (hash: string) => void;
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
  refreshRevision: number;
}) {
  const copy = useCopy();
  const providers = useAsync(() => providerApi.providers(), []);
  const accounts = useAsync(() => providerApi.accounts(), []);
  const current = providers.data?.providers.find((provider) => provider.id === providerID);

  if (providers.loading && !providers.data) {
    return (
      <div className="space-y-4">
        <PageHeader title={copy.providers.title} subtitle={copy.providers.subtitle} />
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {[0, 1, 2].map((index) => (
            <Skeleton key={index} className="h-40" />
          ))}
        </div>
      </div>
    );
  }
  if (providers.error && !providers.data) {
    return <ErrorState message={`${copy.common.error}: ${providers.error}`} retry={providers.reload} retryLabel={copy.common.retry} />;
  }

  if (!current) {
    return (
      <div className="space-y-4">
        <PageHeader title={copy.providers.title} subtitle={copy.providers.subtitle} />
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {(providers.data?.providers ?? []).map((provider) => (
            <Card key={provider.id} className="flex flex-col justify-between gap-4">
              <div>
                <div className="flex items-start justify-between gap-3">
                  <CardTitle>{provider.display_name}</CardTitle>
                  <Badge tone="gray">{provider.kind}</Badge>
                </div>
                <p className="mt-2 text-sm text-gray-500">{provider.description}</p>
                <div className="mt-3 flex flex-wrap gap-2">
                  {Object.entries(provider.capabilities)
                    .filter(([, enabled]) => enabled)
                    .map(([capability]) => (
                      <Badge key={capability} tone="blue">
                        {capability}
                      </Badge>
                    ))}
                </div>
              </div>
              <div className="flex items-center justify-between gap-3">
                <span className="text-sm text-gray-600">
                  {provider.accounts} {copy.providers.accounts}
                </span>
                <Button size="sm" onClick={() => onNavigate(`/providers/${provider.id}`)}>
                  {copy.providers.open} <ArrowRight size={14} />
                </Button>
              </div>
            </Card>
          ))}
        </div>
        <ImportedProviders onChanged={providers.reload} onNotice={onNotice} />
      </div>
    );
  }

  const activeTab: ProviderTab = tab ?? "accounts";
  const providerAccounts = (accounts.data?.accounts ?? []).filter((account) => account.provider.id === current.id);
  const summary = summarize(providerAccounts);

  return (
    <div className="space-y-4">
      <PageHeader
        title={current.display_name}
        subtitle={current.description}
        actions={
          <Button variant="secondary" size="sm" onClick={() => onNavigate("/providers")}>
            {copy.providers.title}
          </Button>
        }
      />
      <Card className="p-4">
        <div className="flex flex-wrap items-center gap-3 text-sm text-gray-600">
          <Badge tone="gray">{current.kind}</Badge>
          <span>{current.accounts} {copy.providers.accounts}</span>
          {Object.entries(summary).map(([status, count]) => (
            <StatusBadge key={status} status={status} />
          ))}
          {Object.entries(summary).map(([status, count]) => (
            <span key={`count-${status}`} className="text-xs text-gray-500">{count}</span>
          ))}
        </div>
      </Card>
      <Tabs
        active={activeTab}
        onChange={(id) => onNavigate(`/providers/${current.id}/${id}`)}
        tabs={[
          { id: "accounts", label: copy.providers.tabs.accounts, count: current.accounts },
          { id: "quota", label: copy.providers.tabs.quota },
          { id: "models", label: copy.providers.tabs.models },
          { id: "routing", label: copy.providers.tabs.routing },
          { id: "advanced", label: copy.providers.tabs.advanced },
        ]}
      />
      {activeTab === "accounts" ? (
        <AccountsPage providerFilter={current.id} onSelect={() => undefined} onRefreshNeeded={accounts.reload} onNotice={onNotice} />
      ) : null}
      {activeTab === "quota" ? <QuotaPage providerFilter={current.id} /> : null}
      {activeTab === "models" ? <ModelsPanel providerID={current.id} /> : null}
      {activeTab === "routing" ? <RoutingPage providerFilter={current.id} /> : null}
      {activeTab === "advanced" ? (
        <AdvancedPanel providerID={current.id} refreshRevision={refreshRevision} onAPIError={onAPIError} onNotice={onNotice} />
      ) : null}
    </div>
  );
}

function ModelsPanel({ providerID }: { providerID: string }) {
  const copy = useCopy();
  const accounts = useAsync(() => providerApi.accounts(providerID), [providerID]);
  const pricing = useAsync(() => providerApi.pricing(providerID), [providerID]);
  const rows = accounts.data?.accounts ?? [];
  if (accounts.loading && !accounts.data) return <Skeleton className="h-64" />;
  if (rows.length === 0) return <EmptyState title={copy.common.empty} description={copy.accounts.empty} />;
  return (
    <div className="space-y-4">
      <Card className="p-0">
        <table className="w-full border-collapse text-sm">
          <thead>
            <tr className="border-b border-gray-200 text-left text-xs font-medium text-gray-500">
              <th className="px-4 py-3">{copy.accounts.provider}</th>
              <th className="px-4 py-3">{copy.accounts.models}</th>
              <th className="px-4 py-3">{copy.usage.priceSource}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((account) => (
              <tr key={account.id} className="border-b border-gray-100 last:border-0 transition-colors duration-150 ease-out hover:bg-gray-50">
                <td className="px-4 py-3 font-medium text-gray-900">{account.display_name}</td>
                <td className="px-4 py-3 text-gray-900">
                  {account.models.count}
                  {account.models.error ? <span className="block text-xs text-red-600">{account.models.error}</span> : null}
                </td>
                <td className="px-4 py-3 text-gray-600">
                  {account.billing.rate_source ?? (pricing.data?.available ? "plugin price catalog" : copy.common.unknown)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Card>
      {pricing.data && !pricing.data.available ? (
        <p className="text-xs text-gray-500">{pricing.data.reason}</p>
      ) : null}
    </div>
  );
}

function AdvancedPanel({
  providerID,
  refreshRevision,
  onAPIError,
  onNotice,
}: {
  providerID: string;
  refreshRevision: number;
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
}) {
  const copy = useCopy();
  const workspace = useMemo(() => {
    switch (providerID) {
      case "codex":
        return <CodexWorkspace refreshRevision={refreshRevision} onAPIError={onAPIError} onNotice={onNotice} />;
      case "opencode-go":
      case "opencode-zen":
        return <OpenCodeWorkspace refreshRevision={refreshRevision} onAPIError={onAPIError} onNotice={onNotice} />;
      case "cline-pass":
        return <ClinePassWorkspace refreshRevision={refreshRevision} onAPIError={onAPIError} onNotice={onNotice} />;
      default:
        return null;
    }
  }, [providerID, refreshRevision, onAPIError, onNotice]);
  if (!workspace) return <EmptyState title={copy.common.empty} description={copy.providers.tabs.advanced} />;
  // The legacy workspaces are embedded unchanged so the deep controls (Codex
  // identity policy, OpenCode session routing, Cline Pass repair) keep their
  // existing behavior while the new IA owns navigation and the shared account view.
  return <div className="rounded-xl border border-gray-200 bg-white p-4">{workspace}</div>;
}

function summarize(accounts: Array<{ status: string }>): Record<string, number> {
  return accounts.reduce<Record<string, number>>((counts, account) => {
    counts[account.status] = (counts[account.status] ?? 0) + 1;
    return counts;
  }, {});
}

// ImportedProviders is the Magpie-compatible half of the provider page: one
// manifest becomes a provider definition without executing any plugin code.
function ImportedProviders({ onChanged, onNotice }: { onChanged: () => void; onNotice: (message: string) => void }) {
  const copy = useCopy();
  const [open, setOpen] = useState(false);
  const [manifest, setManifest] = useState("");
  const [source, setSource] = useState("magpie");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const catalog = useAsync(() => providerApi.catalog(), []);
  const imported = catalog.data?.imported ?? [];
  const hooksOn = catalog.data?.hook_execution_enabled === true;
  const bunAvailable = catalog.data?.bun_available === true;

  const toggleHooks = async () => {
    setBusy(true);
    setError("");
    try {
      await providerApi.saveSettings({ magpie_hook_execution: !hooksOn });
      catalog.reload();
      onChanged();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setBusy(false);
    }
  };

  const submit = async () => {
    setBusy(true);
    setError("");
    try {
      await providerApi.importManifest(manifest, source);
      setOpen(false);
      setManifest("");
      onNotice(copy.providers.importDone);
      catalog.reload();
      onChanged();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setBusy(false);
    }
  };

  const remove = async (id: string) => {
    setBusy(true);
    try {
      await providerApi.removeImport(id);
      catalog.reload();
      onChanged();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="font-semibold tracking-tight text-gray-900">{copy.providers.import}</h2>
          <p className="text-xs text-gray-500">{copy.providers.importHint}</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            size="sm"
            variant={hooksOn ? "primary" : "secondary"}
            onClick={toggleHooks}
            loading={busy}
            disabled={!bunAvailable && !hooksOn}
          >
            {copy.providers.hooks}: {hooksOn ? "on" : "off"}
          </Button>
          <Button size="sm" variant="secondary" onClick={() => setOpen(true)}>
            {copy.providers.import}
          </Button>
        </div>
      </div>
      <p className="text-xs text-gray-500">
        {bunAvailable ? copy.providers.hooksHint : copy.providers.hooksNeedsBun}
        {hooksOn ? "" : ` ${copy.providers.hooksOff}`}
      </p>
      {imported.length === 0 ? (
        <p className="text-sm text-gray-500">{copy.common.empty}</p>
      ) : (
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
          {imported.map((entry) => {
            const id = String(entry.id ?? "");
            const notes = (entry.notes as string[] | undefined) ?? [];
            return (
              <Card key={id} className="space-y-2">
                <div className="flex items-start justify-between gap-2">
                  <CardTitle>{String(entry.name ?? id)}</CardTitle>
                  <Badge tone="amber">{String(entry.source ?? "manifest")}</Badge>
                </div>
                <p className="text-xs text-gray-500">
                  {String(entry.package ?? "")} {entry.version ? `· ${String(entry.version)}` : ""}
                </p>
                <p className="text-xs text-gray-500">{notes[0]}</p>
                {entry.hook_error ? <p className="text-xs text-amber-700">{String(entry.hook_error)}</p> : null}
                <Button size="sm" variant="ghost" onClick={() => remove(id)} loading={busy}>
                  {copy.common.delete}
                </Button>
              </Card>
            );
          })}
        </div>
      )}
      <Dialog
        open={open}
        title={copy.providers.import}
        onClose={() => setOpen(false)}
        footer={
          <>
            <Button variant="ghost" onClick={() => setOpen(false)}>
              {copy.common.cancel}
            </Button>
            <Button onClick={submit} loading={busy} disabled={!manifest.trim()}>
              {copy.providers.import}
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          <label className="block text-sm font-medium text-gray-700">
            Source
            <Select className="mt-1 w-full" value={source} onChange={(event) => setSource(event.target.value)}>
              <option value="magpie">magpie</option>
              <option value="opencode">opencode</option>
              <option value="manifest">manifest</option>
            </Select>
          </label>
          <label className="block text-sm font-medium text-gray-700">
            package.json
            <textarea
              value={manifest}
              onChange={(event) => setManifest(event.target.value)}
              rows={10}
              spellCheck={false}
              className="mt-1 w-full rounded-lg border border-gray-300 bg-white p-3 font-mono text-xs text-gray-900 focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 focus:border-blue-500"
              placeholder={'{"name":"@magpie-community/opencode-cline-auth","version":"0.1.14","main":"./opencode.mjs"}'}
            />
          </label>
          {error ? <p className="text-sm text-red-600">{error}</p> : null}
        </div>
      </Dialog>
    </section>
  );
}

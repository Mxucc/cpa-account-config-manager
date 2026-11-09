import { useMemo, useState } from "react";
import { ArrowRight } from "lucide-react";
import { providerApi } from "../api";
import { Badge, Button, Card, CardTitle, Chip, Dialog, EmptyState, ErrorState, Input, Notice, PageHeader, Select, Skeleton, Table, Tabs, Td, Th } from "../../ui/kit";
import { capabilityLabel, describeProvider, kindLabel, StatusBadge, useAsync, useCopy } from "../shared";
import { useI18n } from "../../i18n";
import { AccountsPage } from "./Accounts";
import { QuotaPage } from "./Quota";
import { RoutingPage } from "./Routing";
import { LegacyConsole } from "../legacy/LegacyConsole";
import { AddAccountDialog } from "./accounts/management";

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
  // The provider whose sign-in dialog is open. The dialog is the same one the
  // accounts page uses; it is only preselected here.
  const [signInProvider, setSignInProvider] = useState("");

  if (providers.loading && !providers.data) {
    return (
      <div className="v2-page">
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
      <div className="v2-page">
        <PageHeader title={copy.providers.title} subtitle={copy.providers.subtitle} />
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {(providers.data?.providers ?? []).map((provider) => (
            <Card key={provider.id} className="flex h-full flex-col justify-between gap-3">
              <div>
                <div className="flex items-start justify-between gap-3">
                  <CardTitle>{provider.display_name}</CardTitle>
                  <Chip>{kindLabel(provider.kind, copy)}</Chip>
                </div>
                <p className="mt-1.5 text-xs leading-relaxed text-gray-500">{describeProvider(provider.id, provider.description ?? "", copy)}</p>
                <div className="mt-3 flex flex-wrap gap-2">
                  {Object.entries(provider.capabilities)
                    .filter(([, enabled]) => enabled)
                    .map(([capability]) => (
                      <Chip key={capability}>{capabilityLabel(capability, copy)}</Chip>
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
        <ImportedProviders onChanged={providers.reload} onNotice={onNotice} onSignIn={setSignInProvider} />
        <AddAccountDialog
          open={signInProvider !== ""}
          preselect={signInProvider}
          onClose={() => setSignInProvider("")}
          onError={onAPIError}
          onDone={() => {
            setSignInProvider("");
            providers.reload();
            accounts.reload();
            onNotice(copy.market.signInDone);
          }}
        />
      </div>
    );
  }

  const activeTab: ProviderTab = tab ?? "accounts";
  const providerAccounts = (accounts.data?.accounts ?? []).filter((account) => account.provider.id === current.id);
  const summary = summarize(providerAccounts);

  return (
    <div className="v2-page">
      <PageHeader
        title={current.display_name}
        subtitle={describeProvider(current.id, current.description ?? "", copy)}
        actions={
          <div className="flex flex-wrap gap-2">
            {/* A provider that can sign in offers it here, next to its own
                accounts, instead of only on the accounts page. */}
            {current.capabilities.login ? (
              <Button size="sm" onClick={() => setSignInProvider(current.id)}>
                {copy.market.signIn}
              </Button>
            ) : null}
            <Button variant="secondary" size="sm" onClick={() => onNavigate("/providers")}>
              {copy.providers.title}
            </Button>
          </div>
        }
      />
      {/* One summary line: kind, account count, and each status with its own
          count, so no bare number is left without a label. */}
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 rounded-lg border border-gray-200 bg-white px-4 py-2.5 text-sm text-gray-600">
        <Chip>{kindLabel(current.kind, copy)}</Chip>
        <span className="font-medium text-gray-900">
          {current.accounts} {copy.providers.accounts}
        </span>
        {Object.entries(summary).map(([status, count]) => (
          <span key={status} className="inline-flex items-center gap-1.5">
            <StatusBadge status={status} />
            <span className="text-xs tabular-nums text-gray-500">{count}</span>
          </span>
        ))}
      </div>
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
        <AccountsPage providerFilter={current.id} embedded onSelect={() => undefined} onRefreshNeeded={accounts.reload} onNotice={onNotice} />
      ) : null}
      {activeTab === "quota" ? <QuotaPage providerFilter={current.id} /> : null}
      {activeTab === "models" ? <ModelsPanel providerID={current.id} /> : null}
      {activeTab === "routing" ? <RoutingPage providerFilter={current.id} /> : null}
      {activeTab === "advanced" ? (
        <AdvancedPanel providerID={current.id} refreshRevision={refreshRevision} onAPIError={onAPIError} onNotice={onNotice} />
      ) : null}
      <AddAccountDialog
        open={signInProvider !== ""}
        preselect={signInProvider}
        onClose={() => setSignInProvider("")}
        onError={onAPIError}
        onDone={() => {
          setSignInProvider("");
          providers.reload();
          accounts.reload();
          onNotice(copy.market.signInDone);
        }}
      />
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
    <div className="v2-page">
      <Card flush>
        <Table>
          <thead>
            <tr>
              <Th>{copy.accounts.provider}</Th>
              <Th>{copy.accounts.models}</Th>
              <Th>{copy.usage.priceSource}</Th>
            </tr>
          </thead>
          <tbody>
            {rows.map((account) => (
              <tr key={account.id}>
                <Td className="font-medium text-gray-900">{account.display_name}</Td>
                <Td>
                  {account.models.count}
                  {account.models.error ? <span className="mt-0.5 block text-xs text-red-600">{account.models.error}</span> : null}
                </Td>
                <Td className="text-gray-600">
                  {account.billing.rate_source ?? (pricing.data?.available ? copy.usage.priceSource : copy.common.unknown)}
                </Td>
              </tr>
            ))}
          </tbody>
        </Table>
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
  const supported = providerID === "codex" || providerID === "opencode-go" || providerID === "opencode-zen" || providerID === "cline-pass";
  if (!supported) return <EmptyState title={copy.common.empty} description={copy.providers.tabs.advanced} />;
  // The compatibility tab renders the old workspaces through one named bridge
  // module, so the new pages never reach into the legacy components themselves.
  return (
    <LegacyConsole
      providerID={providerID}
      refreshRevision={refreshRevision}
      onAPIError={onAPIError}
      onNotice={onNotice}
    />
  );
}

function summarize(accounts: Array<{ status: string }>): Record<string, number> {
  return accounts.reduce<Record<string, number>>((counts, account) => {
    counts[account.status] = (counts[account.status] ?? 0) + 1;
    return counts;
  }, {});
}

// ProviderMarketSection is the community half of the provider page. It lists the
// Magpie community plugins this host can import, and one click imports an entry:
// the plugin fetches that entry's published manifest itself, so the operator
// never handles a package.json. Pasting a manifest stays available for an
// offline or in-house plugin.
//
// An imported provider signs in here too: its package is already installed by the
// import, so the button opens the same add-account dialog the accounts page uses,
// already pointed at that provider. Sending the operator elsewhere would be a
// dead end, because no other application knows about this plugin's install.
function ImportedProviders({
  onChanged,
  onNotice,
  onSignIn,
}: {
  onChanged: () => void;
  onNotice: (message: string) => void;
  onSignIn: (providerID: string) => void;
}) {
  const copy = useCopy();
  const { locale } = useI18n();
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [manifest, setManifest] = useState("");
  const [source, setSource] = useState("magpie");
  const [pendingID, setPendingID] = useState("");
  const [busy, setBusy] = useState(false);
  const [search, setSearch] = useState("");
  const [error, setError] = useState("");
  const catalog = useAsync(() => providerApi.catalog(), []);
  const market = useAsync(() => providerApi.market(locale), [locale]);
  const imported = catalog.data?.imported ?? [];
  const hooksOn = catalog.data?.hook_execution_enabled === true;
  const bunAvailable = catalog.data?.bun_available === true;
  const marketPlugins = market.data?.plugins ?? [];

  const filtered = useMemo(() => {
    const needle = search.trim().toLowerCase();
    if (needle === "") return marketPlugins;
    return marketPlugins.filter((plugin) =>
      [plugin.name, plugin.id, plugin.kind ?? "", ...(plugin.providers ?? [])].join("\n").toLowerCase().includes(needle),
    );
  }, [marketPlugins, search]);

  const reloadBoth = () => {
    catalog.reload();
    market.reload();
    onChanged();
  };

  const toggleHooks = async () => {
    setBusy(true);
    setError("");
    try {
      await providerApi.saveSettings({ magpie_hook_execution: !hooksOn });
      reloadBoth();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setBusy(false);
    }
  };

  // One click: no dialog and no pasted JSON. The id is the npm package name, and
  // the plugin fetches the manifest server side.
  const importFromMarket = async (id: string, name: string) => {
    setPendingID(id);
    setError("");
    try {
      await providerApi.importMarketPlugin(id);
      onNotice(`${copy.providers.importDone}: ${name}`);
      reloadBoth();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setPendingID("");
    }
  };

  const submitManifest = async () => {
    setBusy(true);
    setError("");
    try {
      await providerApi.importManifest(manifest, source);
      setAdvancedOpen(false);
      setManifest("");
      onNotice(copy.providers.importDone);
      reloadBoth();
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
      reloadBoth();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setBusy(false);
    }
  };

  // A registry the host cannot reach is a reason, not a failure: the page names
  // the reason instead of showing an empty list as if nothing existed.
  const marketReason = (): string => {
    switch (market.data?.reason) {
      case "market_unreachable":
        return copy.market.reasons.unreachable;
      case "market_unreachable_timeout":
        return copy.market.reasons.timeout;
      case "market_registry_unreadable":
        return copy.market.reasons.unreadable;
      default:
        return copy.market.reasons.unavailable;
    }
  };

  return (
    <section className="v2-section">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="v2-section-title">{copy.market.title}</h2>
          <p className="v2-hint max-w-3xl">{copy.market.hint}</p>
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
          <Button size="sm" variant="ghost" onClick={() => setAdvancedOpen(true)}>
            {copy.market.advanced}
          </Button>
        </div>
      </div>
      <p className="text-xs text-gray-500">
        {bunAvailable ? copy.providers.hooksHint : copy.providers.hooksNeedsBun}
        {hooksOn ? "" : ` ${copy.providers.hooksOff}`}
      </p>
      {error ? <Notice tone="error">{error}</Notice> : null}

      {imported.length > 0 ? (
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
          {imported.map((entry) => {
            const id = String(entry.id ?? "");
            const notes = (entry.notes as string[] | undefined) ?? [];
            return (
              <Card key={id} className="flex h-full flex-col gap-2">
                <div className="flex items-start justify-between gap-2">
                  <CardTitle>{String(entry.name ?? id)}</CardTitle>
                  <Badge tone="amber">{String(entry.source ?? "manifest")}</Badge>
                </div>
                <p className="break-all text-xs text-gray-500">
                  {String(entry.package ?? "")} {entry.version ? `· ${String(entry.version)}` : ""}
                </p>
                <p className="text-xs text-gray-500">{notes[0]}</p>
                {entry.hook_error ? <p className="text-xs text-amber-700">{String(entry.hook_error)}</p> : null}
                {/* The sign-in is run by this plugin, so it belongs next to the
                    imported provider rather than behind the account page. */}
                <div className="mt-auto flex flex-wrap gap-2">
                  <Button
                    size="sm"
                    disabled={entry.sign_in_available !== true}
                    title={entry.sign_in_available === true ? undefined : copy.market.signInNeedsPackage}
                    onClick={() => onSignIn(id)}
                  >
                    {copy.market.signIn}
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => remove(id)} loading={busy}>
                    {copy.common.delete}
                  </Button>
                </div>
              </Card>
            );
          })}
        </div>
      ) : null}

      <Card className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <CardTitle>{copy.market.title}</CardTitle>
          <span className="text-xs text-gray-500">
            {market.data?.available
              ? `${filtered.length} / ${market.data.total} · ${copy.market.providersOnly}${
                  market.data.hidden > 0 ? ` · ${copy.market.hidden} ${market.data.hidden}` : ""
                }`
              : copy.common.notReported}
          </span>
        </div>
        <Input
          value={search}
          onChange={(event) => setSearch(event.target.value)}
          placeholder={copy.market.search}
          aria-label={copy.market.search}
        />
        {market.loading && !market.data ? (
          <div className="space-y-2">
            {[0, 1, 2].map((index) => (
              <Skeleton key={index} className="h-16" />
            ))}
          </div>
        ) : market.error ? (
          <ErrorState
            message={`${copy.market.unreachable}: ${market.error}`}
            retry={market.reload}
            retryLabel={copy.common.retry}
          />
        ) : market.data && !market.data.available ? (
          <ErrorState message={marketReason()} retry={market.reload} retryLabel={copy.common.retry} />
        ) : filtered.length === 0 ? (
          <EmptyState title={copy.market.empty} description={copy.market.hint} />
        ) : (
          <ul className="divide-y divide-gray-100">
            {filtered.map((plugin) => (
              <li key={plugin.id} className="flex flex-wrap items-start justify-between gap-3 py-3">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="truncate font-medium text-gray-900">{plugin.name}</span>
                    <Badge tone="gray">{plugin.version ? `v${plugin.version}` : copy.market.noVersion}</Badge>
                    {plugin.kind ? <Badge tone="blue">{plugin.kind}</Badge> : null}
                  </div>
                  <p className="mt-1 break-all text-xs text-gray-500">{plugin.id}</p>
                  {plugin.summary ? <p className="mt-1 text-xs text-gray-600">{plugin.summary}</p> : null}
                </div>
                <div className="shrink-0">
                  {plugin.imported ? (
                    <span className="text-xs text-gray-500">
                      {copy.market.imported}
                      {plugin.imported_version ? ` · v${plugin.imported_version}` : ""}
                    </span>
                  ) : (
                    <Button
                      size="sm"
                      onClick={() => importFromMarket(plugin.id, plugin.name)}
                      loading={pendingID === plugin.id}
                    >
                      {copy.market.import}
                    </Button>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
      </Card>

      <Dialog
        open={advancedOpen}
        title={copy.market.advanced}
        onClose={() => setAdvancedOpen(false)}
        footer={
          <>
            <Button variant="ghost" onClick={() => setAdvancedOpen(false)}>
              {copy.common.cancel}
            </Button>
            <Button onClick={submitManifest} loading={busy} disabled={!manifest.trim()}>
              {copy.providers.import}
            </Button>
          </>
        }
      >
        <div className="v2-section">
          <p className="text-xs text-gray-500">{copy.market.advancedHint}</p>
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
              className="mt-1 w-full rounded-lg border border-gray-300 bg-white p-3 font-mono text-xs text-gray-900 focus:border-blue-500 focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2"
              placeholder={'{"name":"@magpie-community/opencode-cline-auth","version":"0.1.14","main":"./opencode.mjs"}'}
            />
          </label>
          {error ? <Notice tone="error">{error}</Notice> : null}
        </div>
      </Dialog>
    </section>
  );
}

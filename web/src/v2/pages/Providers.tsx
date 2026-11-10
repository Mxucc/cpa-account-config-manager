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

// ChannelTest is the answer of a community channel test.
type ChannelTest = {
  provider: string;
  prefix?: string;
  callable?: string;
  upstream: { ok: boolean; status: number; latency_ms: number; model?: string; reason?: string };
  routed: { ok: boolean; status: number; latency_ms: number; model?: string; reason?: string };
  hint?: string;
};

// ChannelTestResult shows both legs, because they fail for different reasons and a
// single "failed" would send the operator after the wrong thing.
function ChannelTestResult({ result, copy }: { result: ChannelTest; copy: ReturnType<typeof useCopy> }) {
  const leg = (label: string, legResult: ChannelTest["upstream"]) => (
    <div className="flex flex-wrap items-center gap-2">
      <span className="text-xs text-gray-500">{label}</span>
      {legResult.ok ? <Badge tone="green">{copy.providers.legOk}</Badge> : <Badge tone="red">{copy.providers.legFailed}</Badge>}
      <span className="text-xs text-gray-600">
        HTTP {legResult.status || "-"} · {legResult.latency_ms} ms{legResult.model ? ` · ${legResult.model}` : ""}
      </span>
      {legResult.reason ? <span className="text-xs text-red-600">{legResult.reason}</span> : null}
    </div>
  );
  return (
    <Notice tone={result.upstream.ok && result.routed.ok ? "ok" : "warn"}>
      <div className="space-y-1.5">
        {result.callable ? (
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-xs text-gray-500">{copy.providers.callableName}</span>
            <span className="font-mono text-xs text-gray-900">{result.callable}</span>
          </div>
        ) : null}
        {leg(copy.providers.upstreamLeg, result.upstream)}
        {leg(copy.providers.routedLeg, result.routed)}
        {result.hint ? <p className="text-xs text-gray-700">{result.hint}</p> : null}
      </div>
    </Notice>
  );
}

// A community provider's models live in the channel this plugin published, so this
// panel is where they are read and chosen. Showing only a count left the operator
// with no way to see which models exist, no way to choose, and no way to change
// what CPA routes.
function ModelsPanel({ providerID }: { providerID: string }) {
  const copy = useCopy();
  const accounts = useAsync(() => providerApi.accounts(providerID), [providerID]);
  const pricing = useAsync(() => providerApi.pricing(providerID), [providerID]);
  const models = useAsync(() => providerApi.communityModels(providerID), [providerID]);
  const [selected, setSelected] = useState<string[] | null>(null);
  const [saving, setSaving] = useState(false);
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  // The channel test answers two independent questions: does the credential work
  // against the provider, and does the gateway route the callable model id here.
  const [testing, setTesting] = useState("");
  const [testResult, setTestResult] = useState<ChannelTest | null>(null);

  const runTest = async (model?: string) => {
    setTesting(model ?? "*");
    setError("");
    setNotice("");
    setTestResult(null);
    try {
      setTestResult(await providerApi.testCommunityChannel(providerID, model));
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setTesting("");
    }
  };
  const rows = accounts.data?.accounts ?? [];
  const catalog = models.data?.rows ?? [];
  const published = models.data?.published ?? [];

  // The selection follows the server until the operator touches a checkbox.
  const chosen = selected ?? published;
  const isOn = (id: string) => chosen.includes(id);

  const toggle = (id: string) => {
    setSelected(isOn(id) ? chosen.filter((entry) => entry !== id) : [...chosen, id]);
  };
  const selectAll = () => setSelected(catalog.map((row) => row.id));
  const selectNone = () => setSelected([]);

  const save = async () => {
    setSaving(true);
    setError("");
    setNotice("");
    try {
      const answer = await providerApi.saveCommunityModels(providerID, chosen);
      if (answer?.error) {
        setError(answer.reason ? `${answer.error}: ${answer.reason}` : answer.error);
        return;
      }
      setNotice(copy.providers.modelsSaved);
      setSelected(null);
      models.reload();
      accounts.reload();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setSaving(false);
    }
  };

  if (accounts.loading && !accounts.data) return <Skeleton className="h-64" />;

  return (
    <div className="v2-page">
      <Card className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <CardTitle>{copy.providers.modelsTitle}</CardTitle>
          <span className="text-xs text-gray-500">
            {copy.providers.modelsPublished}: {published.length} / {catalog.length} ·{" "}
            {models.data?.source === "account" ? copy.providers.modelSourceAccount : copy.providers.modelSourcePackage}
          </span>
        </div>
        <p className="v2-hint">{copy.providers.modelsHint}</p>
        {models.data?.warning ? <Notice tone="warn">{models.data.warning}</Notice> : null}
        {notice ? <Notice tone="ok">{notice}</Notice> : null}
        {error ? <Notice tone="error">{error}</Notice> : null}
        {testResult ? <ChannelTestResult result={testResult} copy={copy} /> : null}
        <div className="flex flex-wrap gap-2">
          <Button size="sm" variant="secondary" onClick={selectAll} disabled={catalog.length === 0}>
            {copy.providers.selectAll}
          </Button>
          <Button size="sm" variant="secondary" onClick={selectNone} disabled={chosen.length === 0}>
            {copy.providers.selectNone}
          </Button>
          <Button size="sm" loading={saving} disabled={chosen.length === 0} onClick={() => void save()}>
            {copy.providers.publishModels}
          </Button>
          <Button size="sm" variant="secondary" loading={testing === "*"} onClick={() => void runTest()}>
            {testing === "*" ? copy.providers.testing : copy.providers.testChannel}
          </Button>
        </div>
      </Card>

      {models.loading && !models.data ? (
        <Skeleton className="h-64" />
      ) : catalog.length === 0 ? (
        <EmptyState title={copy.common.empty} description={copy.providers.modelsHint} />
      ) : (
        <Card flush>
          <Table>
            <thead>
              <tr>
                <Th>{copy.providers.modelColumn}</Th>
                <Th>{copy.providers.modelState}</Th>
                <Th align="right">{copy.accounts.actions}</Th>
              </tr>
            </thead>
            <tbody>
              {catalog.map((row) => (
                <tr key={row.id}>
                  <Td>
                    <label className="flex cursor-pointer items-center gap-2">
                      <input
                        type="checkbox"
                        className="h-4 w-4 rounded border-gray-300 text-blue-600 focus:ring-2 focus:ring-blue-500"
                        checked={isOn(row.id)}
                        onChange={() => toggle(row.id)}
                        aria-label={row.id}
                      />
                      <span className="font-mono text-xs text-gray-900">{row.id}</span>
                    </label>
                  </Td>
                  <Td>
                    {isOn(row.id) ? (
                      <Badge tone="green">{copy.routing.published}</Badge>
                    ) : (
                      <Badge tone="gray">{copy.routing.unpublished}</Badge>
                    )}
                  </Td>
                  <Td className="v2-cell-actions">
                    <Button size="sm" variant="ghost" loading={testing === row.id} onClick={() => void runTest(row.id)}>
                      {copy.providers.testModel}
                    </Button>
                  </Td>
                </tr>
              ))}
            </tbody>
          </Table>
        </Card>
      )}

      {rows.length > 0 ? (
        <p className="v2-hint">
          {copy.providers.modelsAccountNote}
          {pricing.data && !pricing.data.available ? ` ${pricing.data.reason}` : ""}
        </p>
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

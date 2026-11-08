import { useCallback, useEffect, useMemo, useState } from "react";
import { useSettingsCopy } from "./copy";
import * as api from "../../../api/client";
import { providerApi } from "../../api";
import {
  Badge,
  Button,
  Card,
  CardTitle,
  Dialog,
  EmptyState,
  ErrorState,
  Input,
  LabeledField,
  Metric,
  Select,
  Skeleton,
  Toggle,
} from "../management/kit";
import { hasFields } from "../management/kit";
import type {
  ExperimentalSettingsSnapshot,
  GlobalPolicy,
  GlobalPolicySnapshot,
  OperationFilters,
  OperationListResponse,
  PolicySnapshot,
  RiskControlSnapshot,
  SelfUpdateSnapshot,
  UpdateSnapshot,
} from "../../../types";

// Settings, rebuilt on the v2 design system. The page owns six tabs; this module
// holds their bodies so the page itself stays a navigation shell.


export function GeneralTab({ onError, onNotice }: { onError: (message: string) => void; onNotice: (message: string) => void }) {
  const copy = useSettingsCopy();
  const [def, setDef] = useState<PolicySnapshot | null>(null);
  const [global, setGlobal] = useState<GlobalPolicySnapshot | null>(null);
  const [names, setNames] = useState<Awaited<ReturnType<typeof api.getAIProviderNames>> | null>(null);
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const report = useCallback(
    (caught: unknown) => {
      const message = caught instanceof Error ? caught.message : String(caught);
      setError(message);
      onError(message);
    },
    [onError],
  );

  useEffect(() => {
    let cancelled = false;
    Promise.all([api.getDefaultPolicy(), api.getGlobalPolicy(), api.getAIProviderNames()])
      .then(([nextDef, nextGlobal, nextNames]) => {
        if (cancelled) return;
        if (!hasFields(nextDef, ["policy"]) || !hasFields(nextGlobal, ["policy"]) || !hasFields(nextNames, ["names"])) {
          report(new Error(copy.invalidResponse));
          return;
        }
        setDef(nextDef);
        setGlobal(nextGlobal);
        setNames(nextNames);
      })
      .catch((caught) => {
        if (!cancelled) report(caught);
      });
    return () => {
      cancelled = true;
    };
  }, [report]);

  const save = async () => {
    if (!def || !global) return;
    setBusy(true);
    try {
      setDef(await api.saveDefaultPolicy(def.policy));
      setGlobal(await api.saveGlobalPolicy(global.policy));
      setDirty(false);
      onNotice(copy.saved);
    } catch (caught) {
      report(caught);
    } finally {
      setBusy(false);
    }
  };

  const renameChannel = async (kind: string, index: number, baseURL: string | undefined, name: string) => {
    try {
      await api.saveAIProviderName({ kind: kind as Parameters<typeof api.saveAIProviderName>[0]["kind"], index, base_url: baseURL, name });
      setNames(await api.getAIProviderNames());
      onNotice(copy.saved);
    } catch (caught) {
      report(caught);
    }
  };

  if (!def || !global || !names) {
    return (
      <div className="space-y-3">
        <Skeleton className="h-56" />
        <Skeleton className="h-40" />
      </div>
    );
  }

  const patchDefault = (patch: Partial<typeof def.policy>) => {
    setDef({ ...def, policy: { ...def.policy, ...patch } });
    setDirty(true);
  };
  const patchGlobal = (patch: Partial<GlobalPolicy>) => {
    setGlobal({ ...global, policy: { ...global.policy, ...patch } });
    setDirty(true);
  };

  return (
    <div className="space-y-4">
      {error ? <ErrorState message={error} /> : null}
      <Card className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <CardTitle>{copy.general}</CardTitle>
          <div className="flex items-center gap-2">
            {dirty ? <Badge tone="amber">{copy.dirty}</Badge> : null}
            <Button size="sm" onClick={save} loading={busy} disabled={!dirty}>
              {copy.save}
            </Button>
          </div>
        </div>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          <Toggle label={copy.enabled} checked={def.policy.enabled} onChange={(value) => patchDefault({ enabled: value })} />
          <LabeledField label={copy.scanInterval}>
            <Input
              type="number"
              value={def.policy.scan_interval_seconds}
              onChange={(event) => patchDefault({ scan_interval_seconds: Number(event.target.value) })}
            />
          </LabeledField>
          <LabeledField label={copy.concurrency}>
            <Input
              type="number"
              value={def.policy.concurrency_limit ?? ""}
              placeholder={copy.none}
              onChange={(event) =>
                patchDefault({ concurrency_limit: event.target.value === "" ? null : Number(event.target.value) })
              }
            />
          </LabeledField>
          <LabeledField label={copy.priority}>
            <Input
              type="number"
              value={def.policy.priority ?? ""}
              placeholder={copy.none}
              onChange={(event) => patchDefault({ priority: event.target.value === "" ? null : Number(event.target.value) })}
            />
          </LabeledField>
          <LabeledField label={copy.prefix}>
            <Input value={def.policy.prefix ?? ""} onChange={(event) => patchDefault({ prefix: event.target.value })} />
          </LabeledField>
          <LabeledField label={copy.note}>
            <Input value={def.policy.note ?? ""} onChange={(event) => patchDefault({ note: event.target.value })} />
          </LabeledField>
          <LabeledField label={copy.proxyURL} hint={copy.pluginFileHint}>
            <Input value={global.policy.proxy_url ?? ""} onChange={(event) => patchGlobal({ proxy_url: event.target.value })} />
          </LabeledField>
          <Toggle
            label={copy.websockets}
            checked={global.policy.websockets === true}
            onChange={(value) => patchGlobal({ websockets: value })}
          />
        </div>
      </Card>

      <Card className="space-y-3">
        <CardTitle>{copy.providerNames}</CardTitle>
        {names.names.length === 0 ? (
          <EmptyState title={copy.none} description={copy.providerNames} />
        ) : (
          <ul className="divide-y divide-gray-100">
            {names.names.map((entry) => (
              <li key={`${entry.kind}-${entry.index}`} className="flex flex-wrap items-center gap-3 py-3">
                <div className="min-w-0 flex-1">
                  <p className="text-sm text-gray-900">
                    {entry.kind} #{entry.index}
                  </p>
                  <p className="break-all text-xs text-gray-500">{entry.base_url ?? "-"}</p>
                </div>
                <Input
                  defaultValue={entry.name}
                  aria-label={copy.name}
                  onBlur={(event) => {
                    if (event.target.value.trim() !== entry.name) {
                      void renameChannel(entry.kind, entry.index, entry.base_url, event.target.value.trim());
                    }
                  }}
                  className="w-56"
                />
              </li>
            ))}
          </ul>
        )}
        {names.storage_error ? <p className="text-xs text-amber-700">{names.storage_error}</p> : null}
      </Card>
    </div>
  );
}

export function OperationsTab({ onError, onNotice }: { onError: (message: string) => void; onNotice: (message: string) => void }) {
  const copy = useSettingsCopy();
  const [list, setList] = useState<OperationListResponse | null>(null);
  const [filters, setFilters] = useState<OperationFilters>({});
  const [page, setPage] = useState(1);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [confirmClear, setConfirmClear] = useState(false);

  const report = useCallback(
    (caught: unknown) => {
      const message = caught instanceof Error ? caught.message : String(caught);
      setError(message);
      onError(message);
    },
    [onError],
  );

  const load = useCallback(async () => {
    try {
      const next = await api.listOperations(page, filters);
      if (!hasFields(next, ["operations", "summary", "total"])) throw new Error(copy.invalidResponse);
      setList(next);
    } catch (caught) {
      report(caught);
    }
  }, [page, filters, report]);

  useEffect(() => {
    void load();
  }, [load]);

  const setRetention = async (extended: boolean) => {
    setBusy(true);
    try {
      await api.saveOperationRetentionSettings(extended);
      await load();
      onNotice(copy.saved);
    } catch (caught) {
      report(caught);
    } finally {
      setBusy(false);
    }
  };

  const clear = async () => {
    setBusy(true);
    try {
      await api.clearOperations();
      setConfirmClear(false);
      await load();
      onNotice(copy.saved);
    } catch (caught) {
      report(caught);
    } finally {
      setBusy(false);
    }
  };

  const exportJournal = async (format: "json" | "csv" | "jsonl") => {
    setBusy(true);
    try {
      await api.downloadOperationExport(format, filters);
      onNotice(copy.saved);
    } catch (caught) {
      report(caught);
    } finally {
      setBusy(false);
    }
  };

  if (!list) return <Skeleton className="h-72" />;

  return (
    <div className="space-y-4">
      {error ? <ErrorState message={error} retry={() => void load()} /> : null}
      <Card className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <CardTitle>{copy.operations}</CardTitle>
          <div className="flex flex-wrap gap-2">
            <Button size="sm" variant="secondary" onClick={() => exportJournal("csv")} loading={busy}>
              {copy.export} csv
            </Button>
            <Button size="sm" variant="secondary" onClick={() => exportJournal("json")} loading={busy}>
              {copy.export} json
            </Button>
            <Button size="sm" variant="danger" onClick={() => setConfirmClear(true)}>
              {copy.clear}
            </Button>
          </div>
        </div>
        <div className="flex flex-wrap items-end gap-3">
          <LabeledField label={copy.filterCategory}>
            <Select
              value={filters.category ?? ""}
              onChange={(event) => {
                setPage(1);
                setFilters({ ...filters, category: event.target.value as OperationFilters["category"] });
              }}
            >
              <option value="">{copy.none}</option>
              {["inspection", "batch", "import", "export", "account", "policy", "settings", "update", "risk"].map((value) => (
                <option key={value} value={value}>
                  {value}
                </option>
              ))}
            </Select>
          </LabeledField>
          <LabeledField label={copy.filterStatus}>
            <Select
              value={filters.status ?? ""}
              onChange={(event) => {
                setPage(1);
                setFilters({ ...filters, status: event.target.value as OperationFilters["status"] });
              }}
            >
              <option value="">{copy.none}</option>
              {["succeeded", "partial", "failed", "running"].map((value) => (
                <option key={value} value={value}>
                  {value}
                </option>
              ))}
            </Select>
          </LabeledField>
          <LabeledField label={copy.search}>
            <Input
              value={filters.search ?? ""}
              onChange={(event) => {
                setPage(1);
                setFilters({ ...filters, search: event.target.value });
              }}
            />
          </LabeledField>
          <Toggle label={copy.retention} checked={list.extended_history} onChange={(value) => void setRetention(value)} />
        </div>
        <p className="text-xs text-gray-500">{copy.retentionHint}</p>
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
          <Metric label={copy.targets} value={list.total} />
          <Metric label="retained" value={list.retained} />
          <Metric label="segments" value={list.archived_segments} />
          <Metric label="page" value={`${list.page} / ${list.pages}`} />
        </div>
        {list.storage_error ? <p className="text-xs text-amber-700">{list.storage_error}</p> : null}
        {list.operations.length === 0 ? (
          <EmptyState title={copy.none} description={copy.operations} />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-sm">
              <thead>
                <tr className="whitespace-nowrap border-b border-gray-200 text-left text-xs font-medium text-gray-500">
                  <th className="px-3 py-2">{copy.category}</th>
                  <th className="px-3 py-2">{copy.action}</th>
                  <th className="px-3 py-2">{copy.status}</th>
                  <th className="px-3 py-2">{copy.targets}</th>
                  <th className="px-3 py-2">{copy.startedAt}</th>
                </tr>
              </thead>
              <tbody>
                {list.operations.map((entry) => (
                  <tr key={entry.id} className="border-b border-gray-100 last:border-0 hover:bg-gray-50">
                    <td className="px-3 py-2 text-gray-900">{entry.category}</td>
                    <td className="px-3 py-2 text-gray-600">{entry.action}</td>
                    <td className="px-3 py-2">
                      <Badge
                        tone={
                          entry.status === "succeeded" ? "green" : entry.status === "failed" ? "red" : entry.status === "running" ? "blue" : "amber"
                        }
                      >
                        {entry.status}
                      </Badge>
                    </td>
                    <td className="px-3 py-2 text-gray-600">
                      {entry.succeeded}/{entry.target_count}
                      {entry.failed > 0 ? ` · ${entry.failed} failed` : ""}
                    </td>
                    <td className="whitespace-nowrap px-3 py-2 text-gray-500">{entry.started_at}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {list.pages > 1 ? (
          <div className="flex justify-end gap-2">
            <Button size="sm" variant="ghost" disabled={list.page <= 1} onClick={() => setPage((value) => Math.max(1, value - 1))}>
              ←
            </Button>
            <Button size="sm" variant="ghost" disabled={list.page >= list.pages} onClick={() => setPage((value) => value + 1)}>
              →
            </Button>
          </div>
        ) : null}
      </Card>

      <Dialog
        open={confirmClear}
        title={copy.confirmClear}
        onClose={() => setConfirmClear(false)}
        footer={
          <>
            <Button variant="ghost" onClick={() => setConfirmClear(false)}>
              {copy.cancel}
            </Button>
            <Button variant="danger" onClick={clear} loading={busy}>
              {copy.confirm}
            </Button>
          </>
        }
      >
        <p className="text-sm text-gray-700">
          {copy.clear} · {list.retained}
        </p>
      </Dialog>
    </div>
  );
}

export function RiskTab({ onError, onNotice }: { onError: (message: string) => void; onNotice: (message: string) => void }) {
  const copy = useSettingsCopy();
  const [snapshot, setSnapshot] = useState<RiskControlSnapshot | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [pending, setPending] = useState<"events" | "hashes" | null>(null);

  const report = useCallback(
    (caught: unknown) => {
      const message = caught instanceof Error ? caught.message : String(caught);
      setError(message);
      onError(message);
    },
    [onError],
  );

  useEffect(() => {
    let cancelled = false;
    api
      .getRiskControl()
      .then((next) => {
        if (cancelled) return;
        if (!hasFields(next, ["config", "status"])) {
          report(new Error(copy.invalidResponse));
          return;
        }
        setSnapshot(next);
      })
      .catch((caught) => {
        if (!cancelled) report(caught);
      });
    return () => {
      cancelled = true;
    };
  }, [report, copy.invalidResponse]);

  const save = async () => {
    if (!snapshot) return;
    setBusy(true);
    try {
      setSnapshot(await api.saveRiskControl(snapshot.config));
      onNotice(copy.saved);
    } catch (caught) {
      report(caught);
    } finally {
      setBusy(false);
    }
  };

  const clear = async () => {
    if (!pending) return;
    setBusy(true);
    try {
      setSnapshot(pending === "events" ? await api.clearRiskControlEvents() : await api.clearRiskControlHashes());
      setPending(null);
      onNotice(copy.saved);
    } catch (caught) {
      report(caught);
    } finally {
      setBusy(false);
    }
  };

  if (!snapshot) return <Skeleton className="h-64" />;

  const patch = (next: Partial<RiskControlSnapshot["config"]>) =>
    setSnapshot({ ...snapshot, config: { ...snapshot.config, ...next } });

  return (
    <div className="space-y-4">
      {error ? <ErrorState message={error} /> : null}
      <Card className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <CardTitle>{copy.risk}</CardTitle>
          <Button size="sm" onClick={save} loading={busy}>
            {copy.save}
          </Button>
        </div>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          <Toggle label={copy.enabled} checked={snapshot.config.enabled} onChange={(value) => patch({ enabled: value })} />
          <LabeledField label={copy.retentionDays}>
            <Input
              type="number"
              value={snapshot.config.event_retention_days}
              onChange={(event) => patch({ event_retention_days: Number(event.target.value) })}
            />
          </LabeledField>
          <LabeledField label={copy.maxEvents}>
            <Input
              type="number"
              value={snapshot.config.max_events}
              onChange={(event) => patch({ max_events: Number(event.target.value) })}
            />
          </LabeledField>
          <LabeledField label={copy.blockStatus}>
            <Input
              type="number"
              value={snapshot.config.block_status}
              onChange={(event) => patch({ block_status: Number(event.target.value) })}
            />
          </LabeledField>
          <LabeledField label={copy.blockMessage}>
            <Input value={snapshot.config.block_message} onChange={(event) => patch({ block_message: event.target.value })} />
          </LabeledField>
          <LabeledField label={copy.blockedKeywords} hint={copy.blockedKeywordsHint}>
            <textarea
              value={snapshot.config.blocked_keywords.join("\n")}
              onChange={(event) =>
                patch({ blocked_keywords: event.target.value.split("\n").map((line) => line.trim()).filter(Boolean) })
              }
              rows={5}
              className="w-full rounded-lg border border-gray-300 bg-white p-2 text-xs text-gray-900 focus:border-blue-500 focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2"
            />
          </LabeledField>
        </div>
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
          <Metric label={copy.events} value={snapshot.status.total_events} />
          <Metric label="observed" value={snapshot.status.observed} />
          <Metric label="blocked" value={snapshot.status.blocked} />
          <Metric label={copy.hashes} value={snapshot.status.remembered_hashes} />
        </div>
        <div className="flex flex-wrap gap-2">
          <Button size="sm" variant="danger" onClick={() => setPending("events")}>
            {copy.clearEvents}
          </Button>
          <Button size="sm" variant="danger" onClick={() => setPending("hashes")}>
            {copy.clearHashes}
          </Button>
        </div>
        {snapshot.storage_error ? <p className="text-xs text-amber-700">{snapshot.storage_error}</p> : null}
      </Card>

      <Dialog
        open={pending !== null}
        title={pending === "events" ? copy.clearEvents : copy.clearHashes}
        onClose={() => setPending(null)}
        footer={
          <>
            <Button variant="ghost" onClick={() => setPending(null)}>
              {copy.cancel}
            </Button>
            <Button variant="danger" onClick={clear} loading={busy}>
              {copy.confirm}
            </Button>
          </>
        }
      >
        <p className="text-sm text-gray-700">
          {pending === "events" ? copy.events : copy.hashes}:{" "}
          {pending === "events" ? snapshot.status.total_events : snapshot.status.remembered_hashes}
        </p>
      </Dialog>
    </div>
  );
}

export function ExperimentsTab({ onError, onNotice }: { onError: (message: string) => void; onNotice: (message: string) => void }) {
  const copy = useSettingsCopy();
  const [snapshot, setSnapshot] = useState<ExperimentalSettingsSnapshot | null>(null);
  const [whitelist, setWhitelist] = useState<Awaited<ReturnType<typeof api.getAutoModelWhitelist>> | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const report = useCallback(
    (caught: unknown) => {
      const message = caught instanceof Error ? caught.message : String(caught);
      setError(message);
      onError(message);
    },
    [onError],
  );

  useEffect(() => {
    let cancelled = false;
    Promise.all([api.getExperimentalSettings(), api.getAutoModelWhitelist(undefined, 1, 20)])
      .then(([nextSnapshot, nextWhitelist]) => {
        if (cancelled) return;
        if (!hasFields(nextSnapshot, ["settings"])) {
          report(new Error(copy.invalidResponse));
          return;
        }
        setSnapshot(nextSnapshot);
        setWhitelist(hasFields(nextWhitelist, ["auto_model_whitelist"]) ? nextWhitelist : null);
      })
      .catch((caught) => {
        if (!cancelled) report(caught);
      });
    return () => {
      cancelled = true;
    };
  }, [report]);

  const save = async (patch: Partial<ExperimentalSettingsSnapshot["settings"]>) => {
    if (!snapshot) return;
    setBusy(true);
    try {
      setSnapshot(await api.saveExperimentalSettings(patch));
      onNotice(copy.saved);
    } catch (caught) {
      report(caught);
    } finally {
      setBusy(false);
    }
  };

  if (!snapshot) return <Skeleton className="h-56" />;

  return (
    <div className="space-y-4">
      {error ? <ErrorState message={error} /> : null}
      <Card className="space-y-3">
        <CardTitle>{copy.experiments}</CardTitle>
        <div className="grid gap-3 sm:grid-cols-2">
          <Toggle
            label={copy.weeklyOverdraft}
            checked={snapshot.settings.weekly_overdraft_enabled}
            onChange={(value) => void save({ weekly_overdraft_enabled: value })}
          />
          <Toggle
            label={copy.agentIdentity}
            checked={snapshot.settings.agent_identity_enabled}
            onChange={(value) => void save({ agent_identity_enabled: value })}
          />
          <Toggle
            label={copy.autoModelWhitelist}
            checked={snapshot.settings.auto_model_whitelist_enabled}
            onChange={(value) => void save({ auto_model_whitelist_enabled: value })}
          />
          <Toggle
            label={copy.sub2apiCredits}
            checked={snapshot.settings.sub2api_credit_usage_enabled}
            onChange={(value) => void save({ sub2api_credit_usage_enabled: value })}
          />
        </div>
        {snapshot.storage_error ? <p className="text-xs text-amber-700">{snapshot.storage_error}</p> : null}
      </Card>

      <Card className="space-y-3">
        <CardTitle>{copy.whitelist}</CardTitle>
        {!whitelist || whitelist.auto_model_whitelist.recent.length === 0 ? (
          <EmptyState title={copy.none} description={copy.whitelist} />
        ) : (
          <>
            <div className="grid grid-cols-3 gap-2">
              <Metric label={copy.enabled} value={whitelist.auto_model_whitelist.enabled ? "on" : "off"} />
              <Metric label={copy.accounts} value={whitelist.auto_model_whitelist.accounts} />
              <Metric label={copy.limited} value={whitelist.auto_model_whitelist.limited} />
            </div>
            <ul className="divide-y divide-gray-100 text-sm">
              {whitelist.auto_model_whitelist.recent.map((event) => (
                <li key={`${event.account_id}-${event.at ?? ""}`} className="flex items-center justify-between gap-3 py-2">
                  <span className="truncate text-gray-700">{event.label ?? event.account_id}</span>
                  <Badge tone={event.status === "applied" ? "green" : "gray"}>{event.status}</Badge>
                </li>
              ))}
            </ul>
          </>
        )}
      </Card>
    </div>
  );
}

export function PluginsTab({ onError, onNotice }: { onError: (message: string) => void; onNotice: (message: string) => void }) {
  const copy = useSettingsCopy();
  const [catalog, setCatalog] = useState<Awaited<ReturnType<typeof providerApi.catalog>> | null>(null);
  const [selfUpdate, setSelfUpdate] = useState<SelfUpdateSnapshot | null>(null);
  const [pluginFile, setPluginFile] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const report = useCallback(
    (caught: unknown) => {
      const message = caught instanceof Error ? caught.message : String(caught);
      setError(message);
      onError(message);
    },
    [onError],
  );

  useEffect(() => {
    let cancelled = false;
    Promise.all([providerApi.catalog(), api.getSelfUpdate()])
      .then(([nextCatalog, nextSelfUpdate]) => {
        if (cancelled) return;
        if (!hasFields(nextCatalog, ["imported", "hook_execution_enabled"]) || !hasFields(nextSelfUpdate, ["current_version"])) {
          report(new Error(copy.invalidResponse));
          return;
        }
        setCatalog(nextCatalog);
        setSelfUpdate(nextSelfUpdate);
        setPluginFile(nextSelfUpdate.plugin_file ?? "");
      })
      .catch((caught) => {
        if (!cancelled) report(caught);
      });
    return () => {
      cancelled = true;
    };
  }, [report]);

  const toggleHooks = async (value: boolean) => {
    setBusy(true);
    try {
      await providerApi.saveSettings({ magpie_hook_execution: value });
      setCatalog(await providerApi.catalog());
      onNotice(copy.saved);
    } catch (caught) {
      report(caught);
    } finally {
      setBusy(false);
    }
  };

  const savePluginFile = async () => {
    setBusy(true);
    try {
      setSelfUpdate(await api.saveSelfUpdateSettings(pluginFile.trim()));
      onNotice(copy.saved);
    } catch (caught) {
      report(caught);
    } finally {
      setBusy(false);
    }
  };

  if (!catalog || !selfUpdate) return <Skeleton className="h-56" />;

  return (
    <div className="space-y-4">
      {error ? <ErrorState message={error} /> : null}
      <Card className="space-y-3">
        <CardTitle>{copy.plugins}</CardTitle>
        <Toggle
          label={copy.hookExecution}
          checked={catalog.hook_execution_enabled}
          disabled={busy || (!catalog.bun_available && !catalog.hook_execution_enabled)}
          onChange={(value) => void toggleHooks(value)}
        />
        <p className="text-xs text-gray-500">{copy.hookExecutionHint}</p>
        <LabeledField label={copy.pluginFile} hint={copy.pluginFileHint}>
          <Input value={pluginFile} onChange={(event) => setPluginFile(event.target.value)} />
        </LabeledField>
        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" onClick={savePluginFile} loading={busy} disabled={pluginFile === (selfUpdate.plugin_file ?? "")}>
            {copy.save}
          </Button>
          <span className="text-xs text-gray-500">
            {selfUpdate.plugin_file_source ?? "-"} · {String(selfUpdate.plugin_file_exists)}
          </span>
        </div>
      </Card>

      <Card className="space-y-2">
        <CardTitle>{copy.registry}</CardTitle>
        <p className="text-xs text-gray-500">{copy.registryHint}</p>
        <p className="break-all text-xs text-gray-700">{catalog.plugins_dir ?? "-"}</p>
        <div className="grid grid-cols-2 gap-2">
          <Metric label={copy.imported} value={catalog.imported.length} />
          <Metric label="Bun" value={catalog.bun_available ? "ok" : "missing"} />
        </div>
        {catalog.imported.length > 0 ? (
          <ul className="space-y-1 text-xs text-gray-600">
            {catalog.imported.map((entry: Record<string, unknown>) => (
              <li key={String(entry.id)} className="break-all">
                {String(entry.name ?? entry.id)} · {String(entry.version ?? "-")}
              </li>
            ))}
          </ul>
        ) : null}
      </Card>
    </div>
  );
}

export function UpdatesTab({ onError, onNotice }: { onError: (message: string) => void; onNotice: (message: string) => void }) {
  const copy = useSettingsCopy();
  const [status, setStatus] = useState<UpdateSnapshot | null>(null);
  const [selfUpdate, setSelfUpdate] = useState<SelfUpdateSnapshot | null>(null);
  const [cpaVersion, setCpaVersion] = useState<Awaited<ReturnType<typeof api.getCPAServerVersionStatus>> | null>(null);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");

  const report = useCallback(
    (caught: unknown) => {
      const message = caught instanceof Error ? caught.message : String(caught);
      setError(message);
      onError(message);
    },
    [onError],
  );

  const refresh = useCallback(async () => {
    const [nextStatus, nextSelfUpdate, nextCPA] = await Promise.all([
      api.getEffectiveUpdateStatus(false),
      api.getSelfUpdate(),
      api.getCPAServerVersionStatus().catch(() => null),
    ]);
    if (!hasFields(nextStatus, ["current_version"]) || !hasFields(nextSelfUpdate, ["current_version"])) {
      throw new Error(copy.invalidResponse);
    }
    setStatus(nextStatus);
    setSelfUpdate(nextSelfUpdate);
    setCpaVersion(nextCPA);
  }, [copy.invalidResponse]);

  useEffect(() => {
    let cancelled = false;
    refresh().catch((caught) => {
      if (!cancelled) report(caught);
    });
    return () => {
      cancelled = true;
    };
  }, [refresh, report]);

  const check = async () => {
    setBusy("check");
    try {
      setStatus(await api.checkForUpdates());
      setSelfUpdate(await api.getSelfUpdate());
      onNotice(copy.saved);
    } catch (caught) {
      report(caught);
    } finally {
      setBusy("");
    }
  };

  const install = async () => {
    setBusy("install");
    try {
      // installPluginUpdate records the browser operation itself, so this path
      // must not record a second one.
      const version = status?.latest_version ?? selfUpdate?.latest_version;
      if (!version) throw new Error(copy.latestVersion);
      await api.installPluginUpdate(version);
      await refresh();
      onNotice(copy.saved);
    } catch (caught) {
      report(caught);
    } finally {
      setBusy("");
    }
  };

  const reload = async () => {
    setBusy("reload");
    try {
      const result = await api.reloadSelfUpdateThroughStore();
      setSelfUpdate(await api.getSelfUpdate());
      if (!result.reloaded && result.reason) {
        setError(result.reason);
        onError(result.reason);
      } else {
        onNotice(copy.saved);
      }
    } catch (caught) {
      report(caught);
    } finally {
      setBusy("");
    }
  };

  if (!status || !selfUpdate) return <Skeleton className="h-56" />;

  return (
    <div className="space-y-4">
      {error ? <ErrorState message={error} /> : null}
      <Card className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <CardTitle>{copy.updates}</CardTitle>
          <div className="flex flex-wrap gap-2">
            <Button size="sm" variant="secondary" onClick={check} loading={busy === "check"}>
              {copy.checkUpdates}
            </Button>
            <Button size="sm" onClick={install} loading={busy === "install"} disabled={!selfUpdate.can_install}>
              {copy.install}
            </Button>
            <Button size="sm" variant="ghost" onClick={reload} loading={busy === "reload"}>
              {copy.reloadThroughStore}
            </Button>
          </div>
        </div>
        <p className="text-xs text-gray-500">{copy.upgradeHint}</p>
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
          <Metric label={copy.currentVersion} value={selfUpdate.current_version || "-"} />
          <Metric label={copy.appliedVersion} value={selfUpdate.applied_version ?? "-"} />
          <Metric label={copy.latestVersion} value={status.latest_version ?? "-"} />
          <Metric label={copy.cpaVersion} value={cpaVersion?.current_version ?? "-"} />
        </div>
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <Badge tone={status.update_available ? "amber" : "green"}>
            {status.update_available ? copy.install : copy.canInstall}
          </Badge>
          {selfUpdate.pending_restart ? <Badge tone="amber">{copy.restartRequired}</Badge> : null}
          <span className="text-gray-500">{selfUpdate.plugin_file ?? "-"}</span>
        </div>
        {selfUpdate.error ? <p className="text-xs text-red-600">{selfUpdate.error}</p> : null}
        {status.error ? <p className="text-xs text-red-600">{status.error}</p> : null}
        {status.store_error ? <p className="text-xs text-amber-700">{status.store_error}</p> : null}
        {selfUpdate.reload?.reason ? <p className="text-xs text-amber-700">{selfUpdate.reload.reason}</p> : null}
      </Card>
    </div>
  );
}

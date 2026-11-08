import { useCallback, useEffect, useMemo, useState } from "react";
import { useAutomationCopy, type AutomationCopy } from "./copy";
import * as api from "../../../api/client";
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
  usePolling,
} from "../management/kit";
import { hasFields } from "../management/kit";
import { FieldGrid } from "../../../ui/kit";
import type {
  AutoRetrySnapshot,
  DefaultPolicy,
  GlobalPolicy,
  GlobalPolicySnapshot,
  InspectionNotificationEndpoint,
  InspectionPolicy,
  InspectionResult,
  InspectionSnapshot,
  PolicySnapshot,
  ProxyProfileView,
} from "../../../types";

// The host's own return types, read back from the client so a page can never
// drift from what the API layer validates.
type InspectionRows = Awaited<ReturnType<typeof api.listInspectionResults>>;
type NotificationPreview = Awaited<ReturnType<typeof api.previewInspectionNotification>>;

// The automation surfaces, rebuilt on the v2 design system. Each tab owns one
// host capability and keeps the same rules the old panels had: a destructive
// action is always confirmed, a running scan is polled and never assumed to have
// finished, and a failure is shown instead of being swallowed.


const healthTone = (health: string) => {
  switch (health) {
    case "healthy":
      return "green" as const;
    case "quota_limited":
      return "amber" as const;
    case "invalid_credentials":
    case "deactivated":
      return "red" as const;
    case "review":
      return "blue" as const;
    default:
      return "gray" as const;
  }
};

// probe_phase is a wire enum; the metric shows a word, and an unknown value is
// printed as-is instead of being hidden.
function phaseLabel(phase: string | undefined, copy: ReturnType<typeof useAutomationCopy>): string {
  switch (phase) {
    case "idle":
      return copy.phase.idle;
    case "running":
      return copy.phase.running;
    case "completed":
      return copy.phase.completed;
    case "failed":
      return copy.phase.failed;
    default:
      return phase && phase !== "" ? phase : "-";
  }
}

const HEALTH_VALUES = [
  "healthy",
  "quota_limited",
  "invalid_credentials",
  "deactivated",
  "review",
  "unavailable",
  "disabled",
  "unknown",
] as const;

export function InspectionTab({
  refreshRevision,
  onError,
  onNotice,
}: {
  refreshRevision: number;
  onError: (message: string) => void;
  onNotice: (message: string) => void;
}) {
  const copy = useAutomationCopy();
  const [snapshot, setSnapshot] = useState<InspectionSnapshot | null>(null);
  const [policy, setPolicy] = useState<InspectionPolicy | null>(null);
  const [list, setList] = useState<InspectionRows | null>(null);
  const [page, setPage] = useState(1);
  const [health, setHealth] = useState("");
  const [search, setSearch] = useState("");
  const [selected, setSelected] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [dirty, setDirty] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);

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
      const next = await api.getInspection();
      if (!hasFields(next, ["policy", "recent_runs"])) throw new Error(copy.invalidResponse);
      setSnapshot(next);
      setPolicy((current) => (dirty ? current : next.policy));
    } catch (caught) {
      report(caught);
    }
  }, [dirty, report, copy.invalidResponse]);

  const loadList = useCallback(async () => {
    try {
      const rows = await api.listInspectionResults(page, 50, health, search);
      if (!hasFields(rows, ["results", "summary", "total"])) throw new Error(copy.invalidResponse);
      setList(rows);
    } catch (caught) {
      report(caught);
    }
  }, [page, health, search, report, copy.invalidResponse]);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    Promise.all([api.getInspection(), api.listInspectionResults(page, 50, health, search)])
      .then(([next, rows]) => {
        if (cancelled) return;
        setSnapshot(next);
        if (!dirty) setPolicy(next.policy);
        setList(rows);
      })
      .catch((caught) => {
        if (!cancelled) report(caught);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
    // `dirty` is read above but deliberately not a dependency: a background
    // refresh must not overwrite the policy the operator is editing.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [page, health, search, refreshRevision]);

  // A running scan is polled; the loop stops as soon as the host says it is done.
  usePolling(
    Boolean(snapshot?.running),
    async () => {
      await load();
      await loadList();
    },
    3000,
  );

  const run = async (mode: "full" | "incremental") => {
    setBusy(true);
    setError("");
    try {
      setSnapshot(await api.startInspectionRun({ mode }));
      onNotice(copy.running);
    } catch (caught) {
      report(caught);
    } finally {
      setBusy(false);
    }
  };

  const stop = async () => {
    setBusy(true);
    try {
      setSnapshot(await api.stopInspectionRun());
    } catch (caught) {
      report(caught);
    } finally {
      setBusy(false);
    }
  };

  const scanNative = async () => {
    setBusy(true);
    try {
      setSnapshot(await api.scanNativeInspection());
      await loadList();
    } catch (caught) {
      report(caught);
    } finally {
      setBusy(false);
    }
  };

  const savePolicy = async () => {
    if (!policy) return;
    setBusy(true);
    try {
      setSnapshot(await api.saveInspectionPolicy(policy, policy.auto_delete, policy.auto_delete_invalid_credentials));
      setDirty(false);
      onNotice(copy.saved);
    } catch (caught) {
      report(caught);
    } finally {
      setBusy(false);
    }
  };

  const review = async (id: string, action: "resolve" | "ignore" | "reopen") => {
    setBusy(true);
    try {
      await api.updateInspectionReview(id, action);
      await loadList();
    } catch (caught) {
      report(caught);
    } finally {
      setBusy(false);
    }
  };

  const removeSelected = async () => {
    setBusy(true);
    try {
      await api.deleteInspectionRecommendations(selected);
      setSelected([]);
      setConfirmDelete(false);
      onNotice(copy.deleted);
      await loadList();
    } catch (caught) {
      report(caught);
    } finally {
      setBusy(false);
    }
  };

  const exportResults = async (format: "json" | "csv" | "jsonl") => {
    setBusy(true);
    try {
      await api.downloadInspectionExport(format, health, search);
      onNotice(copy.saved);
    } catch (caught) {
      report(caught);
    } finally {
      setBusy(false);
    }
  };

  const patchPolicy = (patch: Partial<InspectionPolicy>) => {
    setPolicy((current) => (current ? { ...current, ...patch } : current));
    setDirty(true);
  };

  if (loading && !snapshot) {
    return (
      <div className="v2-section">
        <Skeleton className="h-24" />
        <Skeleton className="h-72" />
      </div>
    );
  }

  const summary = list?.summary;

  return (
    <div className="v2-page">
      {error ? <ErrorState message={error} retry={() => void load()} /> : null}

      <Card className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <CardTitle>{copy.inspection}</CardTitle>
          <div className="flex flex-wrap gap-2">
            <Button size="sm" onClick={() => run("incremental")} loading={busy} disabled={Boolean(snapshot?.running)}>
              {snapshot?.running ? copy.running : copy.run}
            </Button>
            <Button size="sm" variant="secondary" onClick={scanNative} loading={busy}>
              {copy.nativeScan}
            </Button>
            <Button size="sm" variant="secondary" onClick={() => run("full")} loading={busy} disabled={Boolean(snapshot?.running)}>
              {copy.fullScan}
            </Button>
            <Button size="sm" variant="ghost" onClick={stop} disabled={!snapshot?.running}>
              {copy.stop}
            </Button>
          </div>
        </div>
        <div className="v2-metrics">
          <Metric label={copy.results} value={snapshot?.total ?? 0} />
          <Metric label={copy.actions} value={snapshot?.action_count ?? 0} />
          <Metric label={copy.scanNow} value={phaseLabel(snapshot?.probe_phase, copy)} />
          <Metric label={copy.anomaly} value={snapshot?.anomaly_count ?? 0} />
        </div>
      </Card>

      {policy ? (
        <Card className="space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <CardTitle>{copy.defaults}</CardTitle>
            <div className="flex items-center gap-2">
              {dirty ? <Badge tone="amber">{copy.dirty}</Badge> : null}
              <Button size="sm" onClick={savePolicy} loading={busy} disabled={!dirty}>
                {copy.save}
              </Button>
            </div>
          </div>
          <div className="flex flex-wrap gap-x-5 gap-y-2 rounded-lg border border-gray-200 bg-gray-50 px-3 py-2">
            <Toggle label={copy.enabled} checked={policy.enabled} onChange={(value) => patchPolicy({ enabled: value })} />
            <Toggle label={copy.autoDisable} checked={policy.auto_disable} onChange={(value) => patchPolicy({ auto_disable: value })} />
            <Toggle label={copy.autoEnable} checked={policy.auto_enable} onChange={(value) => patchPolicy({ auto_enable: value })} />
            <Toggle label={copy.autoDelete} checked={policy.auto_delete} onChange={(value) => patchPolicy({ auto_delete: value })} />
          </div>
          <FieldGrid>
            <LabeledField label={copy.interval}>
              <Input
                type="number"
                value={policy.scan_interval_minutes}
                onChange={(event) => patchPolicy({ scan_interval_minutes: Number(event.target.value) })}
              />
            </LabeledField>
            <LabeledField label={copy.failureThreshold}>
              <Input
                type="number"
                value={policy.failure_threshold}
                onChange={(event) => patchPolicy({ failure_threshold: Number(event.target.value) })}
              />
            </LabeledField>
            <LabeledField label={copy.recoveryThreshold}>
              <Input
                type="number"
                value={policy.recovery_threshold}
                onChange={(event) => patchPolicy({ recovery_threshold: Number(event.target.value) })}
              />
            </LabeledField>
            <LabeledField label={copy.deleteGrace}>
              <Input
                type="number"
                value={policy.delete_grace_hours}
                onChange={(event) => patchPolicy({ delete_grace_hours: Number(event.target.value) })}
              />
            </LabeledField>
            <LabeledField label={copy.deleteBatch}>
              <Input
                type="number"
                value={policy.delete_batch_size}
                onChange={(event) => patchPolicy({ delete_batch_size: Number(event.target.value) })}
              />
            </LabeledField>
          </FieldGrid>
        </Card>
      ) : null}

      <Card className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <CardTitle>{copy.results}</CardTitle>
          <div className="flex flex-wrap gap-2">
            <Select value={health} onChange={(event) => { setPage(1); setHealth(event.target.value); }} aria-label={copy.filterHealth}>
              <option value="">{copy.filterHealth}</option>
              {HEALTH_VALUES.map((value) => (
                <option key={value} value={value}>
                  {value}
                </option>
              ))}
            </Select>
            <div className="w-48">
              <Input
                value={search}
                onChange={(event) => { setPage(1); setSearch(event.target.value); }}
                placeholder={copy.search}
                aria-label={copy.search}
              />
            </div>
            <Button size="sm" variant="secondary" onClick={() => exportResults("csv")}>
              {copy.export} csv
            </Button>
            <Button size="sm" variant="secondary" onClick={() => exportResults("json")}>
              {copy.export} json
            </Button>
            {selected.length > 0 ? (
              <Button size="sm" variant="danger" onClick={() => setConfirmDelete(true)}>
                {copy.deleteSelected}: {selected.length}
              </Button>
            ) : null}
          </div>
        </div>
        {summary ? (
          <div className="v2-metrics">
            <Metric label={copy.summaryLabels.actionable} value={summary.actionable} />
            <Metric label={copy.summaryLabels.disable} value={summary.suggested_disable} />
            <Metric label={copy.summaryLabels.delete} value={summary.suggested_delete} />
            <Metric label={copy.summaryLabels.review} value={summary.review} />
            <Metric label={copy.summaryLabels.keep} value={summary.keep} />
            <Metric label={copy.summaryLabels.reauth} value={summary.reauth} />
          </div>
        ) : null}
        {!list || list.results.length === 0 ? (
          <EmptyState title={copy.none} description={copy.inspection} />
        ) : (
          <div className="overflow-x-auto">
            <table className="v2-table">
              <thead>
                <tr>
                  <th>
                    <input
                      type="checkbox"
                      aria-label={copy.actions}
                      checked={selected.length > 0 && selected.length === list.results.length}
                      onChange={(event) => setSelected(event.target.checked ? list.results.map((row) => row.id) : [])}
                    />
                  </th>
                  <th>{copy.account}</th>
                  <th>{copy.provider}</th>
                  <th>{copy.health}</th>
                  <th>{copy.reason}</th>
                  <th>{copy.recommendation}</th>
                  <th className="text-right">{copy.actions}</th>
                </tr>
              </thead>
              <tbody>
                {list.results.map((row: InspectionResult) => (
                  <tr key={row.id}>
                    <td>
                      <input
                        type="checkbox"
                        aria-label={row.name ?? row.id}
                        checked={selected.includes(row.id)}
                        onChange={(event) =>
                          setSelected((current) =>
                            event.target.checked ? [...current, row.id] : current.filter((id) => id !== row.id),
                          )
                        }
                      />
                    </td>
                    <td className="max-w-[16rem] truncate text-gray-900">{row.name ?? row.id}</td>
                    <td className="text-gray-600">{row.provider ?? "-"}</td>
                    <td>
                      <Badge tone={healthTone(row.health)}>{row.health}</Badge>
                    </td>
                    <td className="text-gray-600">{row.reason_code || "-"}</td>
                    <td className="text-gray-600">{row.recommendation}</td>
                    <td className="whitespace-nowrap text-right">
                      <div className="flex justify-end gap-1">
                        <Button size="sm" variant="ghost" onClick={() => review(row.id, "resolve")} disabled={busy}>
                          {copy.resolve}
                        </Button>
                        <Button size="sm" variant="ghost" onClick={() => review(row.id, "ignore")} disabled={busy}>
                          {copy.ignore}
                        </Button>
                        <Button size="sm" variant="ghost" onClick={() => review(row.id, "reopen")} disabled={busy}>
                          {copy.reopen}
                        </Button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {list && list.pages > 1 ? (
          <div className="flex items-center justify-between text-xs text-gray-500">
            <span>
              {copy.page} {list.page} / {list.pages}
            </span>
            <div className="flex gap-2">
              <Button size="sm" variant="ghost" disabled={list.page <= 1} onClick={() => setPage((value) => Math.max(1, value - 1))}>
                {copy.previous}
              </Button>
              <Button size="sm" variant="ghost" disabled={list.page >= list.pages} onClick={() => setPage((value) => value + 1)}>
                {copy.next}
              </Button>
            </div>
          </div>
        ) : null}
      </Card>

      {snapshot && (snapshot.recent_runs ?? []).length > 0 ? (
        <Card className="space-y-2">
          <CardTitle>{copy.recentActions}</CardTitle>
          <ul className="space-y-1 text-xs text-gray-600">
            {(snapshot.recent_runs ?? []).slice(0, 8).map((entry) => (
              <li key={entry.id} className="flex justify-between gap-3">
                <span className="truncate">
                  {entry.mode} · {entry.status}
                </span>
                <span className="shrink-0 text-gray-500">
                  {entry.summary.scanned} · {entry.summary.healthy} ok · {entry.summary.invalid_credentials} bad
                </span>
              </li>
            ))}
          </ul>
        </Card>
      ) : null}

      <Dialog
        open={confirmDelete}
        title={copy.confirmDelete}
        onClose={() => setConfirmDelete(false)}
        footer={
          <>
            <Button variant="ghost" onClick={() => setConfirmDelete(false)}>
              {copy.cancel}
            </Button>
            <Button variant="danger" onClick={removeSelected} loading={busy}>
              {copy.confirmDelete}
            </Button>
          </>
        }
      >
        <p className="text-sm text-gray-700">
          {copy.targetCount}: {selected.length}
        </p>
        <ul className="mt-2 max-h-48 space-y-1 overflow-y-auto text-xs text-gray-600">
          {selected.map((id) => (
            <li key={id} className="break-all">
              {id}
            </li>
          ))}
        </ul>
      </Dialog>
    </div>
  );
}

export function PoliciesTab({ onError, onNotice }: { onError: (message: string) => void; onNotice: (message: string) => void }) {
  const copy = useAutomationCopy();
  const [def, setDef] = useState<PolicySnapshot | null>(null);
  const [global, setGlobal] = useState<GlobalPolicySnapshot | null>(null);
  const [busy, setBusy] = useState(false);
  const [scanning, setScanning] = useState(false);
  const [error, setError] = useState("");
  const [defDirty, setDefDirty] = useState(false);
  const [globalDirty, setGlobalDirty] = useState(false);

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
    Promise.all([api.getDefaultPolicy(), api.getGlobalPolicy()])
      .then(([nextDef, nextGlobal]) => {
        if (cancelled) return;
        if (!hasFields(nextDef, ["policy"]) || !hasFields(nextGlobal, ["policy"])) {
          report(new Error(copy.invalidResponse));
          return;
        }
        setDef(nextDef);
        setGlobal(nextGlobal);
      })
      .catch((caught) => {
        if (!cancelled) report(caught);
      });
    return () => {
      cancelled = true;
    };
  }, [report, copy.invalidResponse]);

  const saveDefaults = async () => {
    if (!def) return;
    setBusy(true);
    try {
      setDef(await api.saveDefaultPolicy(def.policy));
      setDefDirty(false);
      onNotice(copy.saved);
    } catch (caught) {
      report(caught);
    } finally {
      setBusy(false);
    }
  };

  const saveGlobal = async () => {
    if (!global) return;
    setBusy(true);
    try {
      setGlobal(await api.saveGlobalPolicy(global.policy));
      setGlobalDirty(false);
      onNotice(copy.saved);
    } catch (caught) {
      report(caught);
    } finally {
      setBusy(false);
    }
  };

  const scan = async () => {
    setScanning(true);
    try {
      setDef(await api.scanDefaultPolicy());
      onNotice(copy.saved);
    } catch (caught) {
      report(caught);
    } finally {
      setScanning(false);
    }
  };

  if (!def || !global) {
    return (
      <div className="v2-section">
        <Skeleton className="h-56" />
        <Skeleton className="h-56" />
      </div>
    );
  }

  const patchDefault = (patch: Partial<DefaultPolicy>) => {
    setDef({ ...def, policy: { ...def.policy, ...patch } });
    setDefDirty(true);
  };
  const patchGlobal = (patch: Partial<GlobalPolicy>) => {
    setGlobal({ ...global, policy: { ...global.policy, ...patch } });
    setGlobalDirty(true);
  };

  return (
    <div className="v2-page">
      {error ? <ErrorState message={error} /> : null}
      <Card className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div>
            <CardTitle>{copy.defaults}</CardTitle>
            <p className="v2-hint">{copy.defaultsHint}</p>
          </div>
          <div className="flex items-center gap-2">
            {defDirty ? <Badge tone="amber">{copy.dirty}</Badge> : null}
            <Button size="sm" variant="secondary" onClick={scan} loading={scanning}>
              {scanning ? copy.scanning : copy.scanNow}
            </Button>
            <Button size="sm" onClick={saveDefaults} loading={busy} disabled={!defDirty}>
              {copy.save}
            </Button>
          </div>
        </div>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          <Toggle label={copy.enabled} checked={def.policy.enabled} onChange={(value) => patchDefault({ enabled: value })} />
          <Toggle
            label={copy.autoDisable}
            checked={def.policy.disabled === true}
            onChange={(value) => patchDefault({ disabled: value })}
          />
          <Toggle
            label={copy.websockets}
            checked={def.policy.websockets === true}
            onChange={(value) => patchDefault({ websockets: value })}
          />
          <LabeledField label={copy.priority}>
            <Input
              type="number"
              value={def.policy.priority ?? ""}
              placeholder={copy.none}
              onChange={(event) => patchDefault({ priority: event.target.value === "" ? null : Number(event.target.value) })}
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
          <LabeledField label={copy.interval}>
            <Input
              type="number"
              value={def.policy.scan_interval_seconds}
              onChange={(event) => patchDefault({ scan_interval_seconds: Number(event.target.value) })}
            />
          </LabeledField>
          <LabeledField label={copy.note}>
            <Input value={def.policy.note ?? ""} onChange={(event) => patchDefault({ note: event.target.value })} />
          </LabeledField>
          <LabeledField label={copy.prefix}>
            <Input value={def.policy.prefix ?? ""} onChange={(event) => patchDefault({ prefix: event.target.value })} />
          </LabeledField>
          <LabeledField label={copy.proxyURL}>
            <Input value={def.policy.proxy_url ?? ""} onChange={(event) => patchDefault({ proxy_url: event.target.value })} />
          </LabeledField>
        </div>
      </Card>

      <Card className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div>
            <CardTitle>{copy.globalPolicy}</CardTitle>
            <p className="v2-hint">{copy.globalPolicyHint}</p>
          </div>
          <div className="flex items-center gap-2">
            {globalDirty ? <Badge tone="amber">{copy.dirty}</Badge> : null}
            <Button size="sm" onClick={saveGlobal} loading={busy} disabled={!globalDirty}>
              {copy.save}
            </Button>
          </div>
        </div>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          <Toggle label={copy.enabled} checked={global.policy.enabled} onChange={(value) => patchGlobal({ enabled: value })} />
          <Toggle
            label={copy.autoDisable}
            checked={global.policy.disabled === true}
            onChange={(value) => patchGlobal({ disabled: value })}
          />
          <Toggle
            label={copy.websockets}
            checked={global.policy.websockets === true}
            onChange={(value) => patchGlobal({ websockets: value })}
          />
          <LabeledField label={copy.priority}>
            <Input
              type="number"
              value={global.policy.priority ?? ""}
              placeholder={copy.none}
              onChange={(event) => patchGlobal({ priority: event.target.value === "" ? null : Number(event.target.value) })}
            />
          </LabeledField>
          <LabeledField label={copy.concurrency}>
            <Input
              type="number"
              value={global.policy.concurrency_limit ?? ""}
              placeholder={copy.none}
              onChange={(event) =>
                patchGlobal({ concurrency_limit: event.target.value === "" ? null : Number(event.target.value) })
              }
            />
          </LabeledField>
          <LabeledField label={copy.note}>
            <Input value={global.policy.note ?? ""} onChange={(event) => patchGlobal({ note: event.target.value })} />
          </LabeledField>
          <LabeledField label={copy.prefix}>
            <Input value={global.policy.prefix ?? ""} onChange={(event) => patchGlobal({ prefix: event.target.value })} />
          </LabeledField>
          <LabeledField label={copy.proxyURL}>
            <Input value={global.policy.proxy_url ?? ""} onChange={(event) => patchGlobal({ proxy_url: event.target.value })} />
          </LabeledField>
        </div>
      </Card>
    </div>
  );
}

export function NotificationsTab({ onError, onNotice }: { onError: (message: string) => void; onNotice: (message: string) => void }) {
  const copy = useAutomationCopy();
  const [policy, setPolicy] = useState<InspectionPolicy | null>(null);
  const [draft, setDraft] = useState<InspectionNotificationEndpoint | null>(null);
  const [preview, setPreview] = useState<NotificationPreview | null>(null);
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
    api
      .getInspection()
      .then((snapshot) => {
        if (cancelled) return;
        if (!hasFields(snapshot, ["policy"])) {
          report(new Error(copy.invalidResponse));
          return;
        }
        setPolicy(snapshot.policy);
      })
      .catch((caught) => {
        if (!cancelled) report(caught);
      });
    return () => {
      cancelled = true;
    };
  }, [report]);

  const endpoints = policy?.notification_endpoints ?? [];

  const save = async (next: InspectionNotificationEndpoint[]) => {
    if (!policy) return;
    setBusy(true);
    try {
      const snapshot = await api.saveInspectionPolicy({ ...policy, notification_endpoints: next });
      setPolicy(snapshot.policy);
      onNotice(copy.saved);
    } catch (caught) {
      report(caught);
    } finally {
      setBusy(false);
    }
  };

  const requestFor = (endpoint: InspectionNotificationEndpoint) => ({
    endpoint_id: endpoint.id,
    endpoint_name: endpoint.name,
    url_template: endpoint.url,
    scenario: "manual_test" as const,
    threshold_percent: policy?.notification_availability_percent_threshold ?? 0,
    available_accounts_threshold: policy?.notification_available_accounts_threshold ?? 0,
    availability_percent_threshold: policy?.notification_availability_percent_threshold ?? 0,
    notification_policy_id: endpoint.notification_policy_id,
  });

  const runPreview = async (endpoint: InspectionNotificationEndpoint) => {
    setBusy(true);
    try {
      setPreview(await api.previewInspectionNotification(requestFor(endpoint)));
    } catch (caught) {
      report(caught);
    } finally {
      setBusy(false);
    }
  };

  const runTest = async (endpoint: InspectionNotificationEndpoint) => {
    setBusy(true);
    try {
      const result = await api.testInspectionNotification(requestFor(endpoint));
      onNotice(String(result.preview?.event ?? copy.saved));
    } catch (caught) {
      report(caught);
    } finally {
      setBusy(false);
    }
  };

  if (!policy) return <Skeleton className="h-56" />;

  return (
    <div className="v2-page">
      {error ? <ErrorState message={error} /> : null}
      <Card className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <CardTitle>{copy.endpoints}</CardTitle>
          <Button
            size="sm"
            onClick={() => setDraft({ id: "", name: "", url: "", enabled: true })}
          >
            {copy.addEndpoint}
          </Button>
        </div>
        {endpoints.length === 0 ? (
          <EmptyState title={copy.none} description={copy.notifications} />
        ) : (
          <ul className="divide-y divide-gray-100">
            {endpoints.map((endpoint) => (
              <li key={endpoint.id} className="flex flex-wrap items-center justify-between gap-3 py-3">
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className="truncate font-medium text-gray-900">{endpoint.name ?? endpoint.id}</span>
                    <Badge tone={endpoint.enabled ? "green" : "gray"}>{endpoint.enabled ? "on" : "off"}</Badge>
                  </div>
                  <p className="mt-1 break-all text-xs text-gray-500">{endpoint.url}</p>
                </div>
                <div className="flex flex-wrap gap-2">
                  <Button size="sm" variant="ghost" onClick={() => setDraft(endpoint)}>
                    {copy.preview}
                  </Button>
                  <Button size="sm" variant="secondary" onClick={() => runPreview(endpoint)} loading={busy}>
                    {copy.preview}
                  </Button>
                  <Button size="sm" variant="secondary" onClick={() => runTest(endpoint)} loading={busy}>
                    {copy.testSend}
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => save(endpoints.filter((row) => row.id !== endpoint.id))}
                    loading={busy}
                  >
                    {copy.remove}
                  </Button>
                </div>
              </li>
            ))}
          </ul>
        )}
        {preview ? (
          <div className="rounded-lg border border-gray-200 bg-gray-50 px-3 py-2">
            <p className="text-xs font-medium text-gray-500">{copy.expandedURL}</p>
            <p className="break-all text-xs text-gray-700">{preview.expanded_url}</p>
            <p className="mt-2 text-xs font-medium text-gray-500">{copy.variables}</p>
            <ul className="mt-1 space-y-0.5 text-xs text-gray-600">
              {Object.entries(preview.variables).map(([key, value]) => (
                <li key={key} className="break-all">
                  {key}: {value}
                </li>
              ))}
            </ul>
          </div>
        ) : null}
      </Card>

      <Dialog
        open={draft !== null}
        title={copy.addEndpoint}
        onClose={() => setDraft(null)}
        footer={
          <>
            <Button variant="ghost" onClick={() => setDraft(null)}>
              {copy.cancel}
            </Button>
            <Button
              onClick={() => {
                if (!draft) return;
                const entry: InspectionNotificationEndpoint = {
                  ...draft,
                  id: draft.id || `ep-${Date.now().toString(36)}`,
                };
                const next = endpoints.some((row) => row.id === entry.id)
                  ? endpoints.map((row) => (row.id === entry.id ? entry : row))
                  : [...endpoints, entry];
                setDraft(null);
                void save(next);
              }}
              loading={busy}
              disabled={!draft?.url.trim()}
            >
              {copy.save}
            </Button>
          </>
        }
      >
        {draft ? (
          <div className="v2-section">
            <LabeledField label={copy.endpointName}>
              <Input value={draft.name ?? ""} onChange={(event) => setDraft({ ...draft, name: event.target.value })} />
            </LabeledField>
            <LabeledField label={copy.endpointURL}>
              <Input value={draft.url} onChange={(event) => setDraft({ ...draft, url: event.target.value })} />
            </LabeledField>
            <Toggle label={copy.enabled} checked={draft.enabled} onChange={(value) => setDraft({ ...draft, enabled: value })} />
          </div>
        ) : null}
      </Dialog>
    </div>
  );
}

export function RetryTab({ onError, onNotice }: { onError: (message: string) => void; onNotice: (message: string) => void }) {
  const copy = useAutomationCopy();
  const [snapshot, setSnapshot] = useState<AutoRetrySnapshot | null>(null);
  const [attempts, setAttempts] = useState("0");
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
    api
      .getAutoRetry()
      .then((next) => {
        if (cancelled) return;
        if (!hasFields(next, ["attempts"])) {
          report(new Error(copy.invalidResponse));
          return;
        }
        setSnapshot(next);
        setAttempts(String(next.attempts));
      })
      .catch((caught) => {
        if (!cancelled) report(caught);
      });
    return () => {
      cancelled = true;
    };
  }, [report, copy.invalidResponse]);

  const save = async () => {
    setBusy(true);
    try {
      const next = await api.saveAutoRetry(Number(attempts));
      setSnapshot(next);
      onNotice(copy.saved);
    } catch (caught) {
      report(caught);
    } finally {
      setBusy(false);
    }
  };

  if (!snapshot) return <Skeleton className="h-40" />;

  return (
    <div className="v2-page">
      {error ? <ErrorState message={error} /> : null}
      <Card className="space-y-3">
        <CardTitle>{copy.retry}</CardTitle>
        <p className="v2-hint">{copy.retryHint}</p>
        <div className="grid gap-3 sm:grid-cols-3">
          <LabeledField label={`${copy.retryAttempts} (max ${snapshot.max_attempts ?? "-"})`}>
            <Input
              type="number"
              min={0}
              max={snapshot.max_attempts ?? 10}
              value={attempts}
              onChange={(event) => setAttempts(event.target.value)}
            />
          </LabeledField>
          <Metric label={copy.retryHost} value={snapshot.host?.request_retry ?? "-"} />
          <Metric label="codex accounts" value={snapshot.applied?.codex_accounts ?? 0} />
        </div>
        {snapshot.storage_error ? <p className="text-xs text-amber-700">{snapshot.storage_error}</p> : null}
        <Button size="sm" onClick={save} loading={busy}>
          {copy.save}
        </Button>
      </Card>
    </div>
  );
}

export function ProxyTab({ onError, onNotice }: { onError: (message: string) => void; onNotice: (message: string) => void }) {
  const copy = useAutomationCopy();
  const [profiles, setProfiles] = useState<ProxyProfileView[] | null>(null);
  const [draft, setDraft] = useState<{ id?: string; name: string; proxy_url: string; note: string; enabled: boolean } | null>(null);
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

  const load = useCallback(async () => {
    try {
      const next = await api.listProxyProfiles();
      if (!hasFields(next, ["profiles"])) throw new Error(copy.invalidResponse);
      setProfiles(next.profiles);
    } catch (caught) {
      report(caught);
    }
  }, [report]);

  useEffect(() => {
    void load();
  }, [load]);

  const submit = async () => {
    if (!draft) return;
    setBusy(true);
    try {
      if (draft.id) {
        await api.updateProxyProfile({ id: draft.id, name: draft.name, proxy_url: draft.proxy_url, note: draft.note, enabled: draft.enabled });
      } else {
        await api.createProxyProfile({ name: draft.name, proxy_url: draft.proxy_url, note: draft.note, enabled: draft.enabled });
      }
      setDraft(null);
      onNotice(copy.saved);
      await load();
    } catch (caught) {
      report(caught);
    } finally {
      setBusy(false);
    }
  };

  const [pendingDelete, setPendingDelete] = useState<ProxyProfileView | null>(null);

  const remove = async (force: boolean) => {
    if (!pendingDelete) return;
    setBusy(true);
    try {
      await api.deleteProxyProfile(pendingDelete.id, force);
      setPendingDelete(null);
      onNotice(copy.deleted);
      await load();
    } catch (caught) {
      report(caught);
    } finally {
      setBusy(false);
    }
  };

  if (!profiles) return <Skeleton className="h-40" />;

  return (
    <div className="v2-page">
      {error ? <ErrorState message={error} /> : null}
      <Card className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <CardTitle>{copy.profiles}</CardTitle>
          <Button size="sm" onClick={() => setDraft({ name: "", proxy_url: "", note: "", enabled: true })}>
            {copy.addProfile}
          </Button>
        </div>
        {profiles.length === 0 ? (
          <EmptyState title={copy.none} description={copy.proxy} />
        ) : (
          <div className="overflow-x-auto">
            <table className="v2-table">
              <thead>
                <tr>
                  <th>{copy.profileName}</th>
                  <th>URL</th>
                  <th>{copy.accounts}</th>
                  <th>{copy.enabledProfiles}</th>
                  <th className="text-right">{copy.actions}</th>
                </tr>
              </thead>
              <tbody>
                {profiles.map((profile) => (
                  <tr key={profile.id}>
                    <td className="text-gray-900">{profile.name}</td>
                    <td className="max-w-[18rem] truncate text-gray-600">{profile.proxy_url_masked}</td>
                    <td className="text-gray-600">{profile.account_count}</td>
                    <td>
                      <Badge tone={profile.enabled ? "green" : "gray"}>{profile.enabled ? "on" : "off"}</Badge>
                    </td>
                    <td className="whitespace-nowrap text-right">
                      <div className="flex justify-end gap-1">
                        <Button
                          size="sm"
                          variant="ghost"
                          onClick={() =>
                            setDraft({
                              id: profile.id,
                              name: profile.name,
                              proxy_url: "",
                              note: profile.note ?? "",
                              enabled: profile.enabled,
                            })
                          }
                        >
                          {copy.save}
                        </Button>
                        <Button size="sm" variant="ghost" onClick={() => setPendingDelete(profile)}>
                          {copy.remove}
                        </Button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Dialog
        open={draft !== null}
        title={draft?.id ? copy.profileName : copy.addProfile}
        onClose={() => setDraft(null)}
        footer={
          <>
            <Button variant="ghost" onClick={() => setDraft(null)}>
              {copy.cancel}
            </Button>
            <Button onClick={submit} loading={busy} disabled={!draft?.name.trim() || (draft.id ? false : !draft.proxy_url.trim())}>
              {copy.save}
            </Button>
          </>
        }
      >
        {draft ? (
          <div className="v2-section">
            <LabeledField label={copy.profileName}>
              <Input value={draft.name} onChange={(event) => setDraft({ ...draft, name: event.target.value })} />
            </LabeledField>
            <LabeledField label={copy.proxyURL}>
              <Input value={draft.proxy_url} onChange={(event) => setDraft({ ...draft, proxy_url: event.target.value })} />
            </LabeledField>
            <LabeledField label={copy.note}>
              <Input value={draft.note} onChange={(event) => setDraft({ ...draft, note: event.target.value })} />
            </LabeledField>
            <Toggle label={copy.enabled} checked={draft.enabled} onChange={(value) => setDraft({ ...draft, enabled: value })} />
          </div>
        ) : null}
      </Dialog>

      <Dialog
        open={pendingDelete !== null}
        title={copy.remove}
        onClose={() => setPendingDelete(null)}
        footer={
          <>
            <Button variant="ghost" onClick={() => setPendingDelete(null)}>
              {copy.cancel}
            </Button>
            <Button variant="danger" onClick={() => remove(true)} loading={busy}>
              {copy.confirm}
            </Button>
          </>
        }
      >
        <p className="text-sm text-gray-700">
          {pendingDelete?.name} · {pendingDelete?.account_count} {copy.accounts}
        </p>
      </Dialog>
    </div>
  );
}

// The page imports the tabs and the copy hook; the catalog module owns the strings.
export type { AutomationCopy };

import { useEffect, useMemo, useRef, useState } from "react";
import * as api from "../../../api/client";
import { Badge, Button, Dialog, ErrorState, Input, KeyValue, Notice, Progress, Select, Skeleton, useErrorMessage } from "../../../ui/kit";
import { useAccountManagementCopy, type ManagementCopy } from "./managementCopy";

// The page imports the hook from here; the strings themselves live in the catalog.
export { useAccountManagementCopy };

const useManagementCopy = useAccountManagementCopy;
import type {
  AccountDeletePreview,
  AccountDeduplicationPreview,
  AccountEditableConfig,
  AccountExportFormat,
  AccountModelCatalogResponse,
  BatchPatch,
  BatchPreview,
  ImportPreview,
  ImportResult,
  JobSnapshot,
	AccountLoginFlow,
  ModelTestResult,
	AccountLoginFlowKind,
	AccountLoginProvider,
	AccountLoginStatus,
  TargetScope,
} from "../../../types";

// Account management, rebuilt on the v2 design system. Every mutation goes
// through the host's own preview-then-apply flow, so nothing is changed before
// the operator sees what it will touch.



const rawAccountID = (providerID: string, id: string) =>
  id.startsWith(`provider:${providerID}:`) ? id.slice(`provider:${providerID}:`.length) : id;

// useJobPolling keeps a running job's progress visible and stops on a terminal
// state. The timer is cleared on unmount, so a closed dialog cannot keep polling.
function useJobPolling<T extends { running: boolean }>(
  active: boolean,
  load: () => Promise<T>,
  apply: (value: T) => void,
  intervalMs = 2000,
) {
  const loadRef = useRef(load);
  const applyRef = useRef(apply);
  loadRef.current = load;
  applyRef.current = apply;
  useEffect(() => {
    if (!active) return;
    let cancelled = false;
    const timer = window.setTimeout(async function tick() {
      try {
        const next = await loadRef.current();
        if (cancelled) return;
        applyRef.current(next);
        if (!next.running) return;
      } catch {
        // A failed poll leaves the last known state on screen; it must not clear it.
      }
      if (!cancelled) window.setTimeout(tick, intervalMs);
    }, intervalMs);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [active, intervalMs]);
}

// The dialogs use the shared metric tile; `tone` only tints the value, so a
// destructive count can read as such without inventing a second box.
function Metric({ label, value, tone }: { label: string; value: number | string; tone?: string }) {
  return (
    <div className="v2-metric">
      <p className="v2-metric-label">{label}</p>
      <p className={`v2-metric-value ${tone ?? ""}`}>{value}</p>
    </div>
  );
}

function PreviewSummary({ preview, copy }: { preview: BatchPreview; copy: ManagementCopy }) {
  return (
    <div className="space-y-3">
      <div className="v2-metrics">
        <Metric label={copy.targets} value={preview.total} />
        <Metric label={copy.eligible} value={preview.eligible} />
        <Metric label={copy.readOnly} value={preview.read_only} />
        <Metric label={copy.missing} value={preview.missing} />
        <Metric label={copy.physicalFiles} value={preview.physical_files} />
      </div>
      <div>
        <p className="text-xs font-medium text-gray-500">{copy.changedFields}</p>
        <div className="mt-1 flex flex-wrap gap-1">
          {preview.patch.fields.length === 0 ? (
            <span className="text-xs text-gray-400">{copy.none}</span>
          ) : (
            preview.patch.fields.map((field) => (
              <Badge key={field} tone="blue">
                {field}
              </Badge>
            ))
          )}
        </div>
      </div>
      {preview.warnings?.length ? (
        <ul className="space-y-1">
          {preview.warnings.map((warning) => (
            <li key={warning} className="text-xs text-amber-700">
              {warning}
            </li>
          ))}
        </ul>
      ) : null}
      <p className="text-xs text-gray-400">{copy.hookNote}</p>
    </div>
  );
}

function JobProgress({ job, copy }: { job: JobSnapshot | null; copy: ManagementCopy }) {
  if (!job) return <p className="text-xs text-gray-400">{copy.noJob}</p>;
  const total = Math.max(job.total, 1);
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between text-xs text-gray-600">
        <span>{copy.jobProgress}</span>
        <span className="font-medium text-gray-900">
          {job.done} / {job.total}
        </span>
      </div>
      <Progress value={Math.round((job.done / total) * 100)} tone={job.failed > 0 ? "amber" : "blue"} />
      <div className="v2-metrics">
        <Metric label={copy.eligible} value={job.eligible} />
        <Metric label={copy.metrics.ok} value={job.succeeded} />
        <Metric label={copy.metrics.failed} value={job.failed} />
        <Metric label={copy.metrics.skipped} value={job.skipped} />
      </div>
      {job.storage_error ? <p className="text-xs text-amber-700">{job.storage_error}</p> : null}
    </div>
  );
}

interface DialogProps {
  open: boolean;
  onClose: () => void;
  onError: (message: string) => void;
  onDone: () => void;
}

function LabeledInput({
  label,
  hint,
  ...rest
}: { label: string; hint?: string } & React.InputHTMLAttributes<HTMLInputElement>) {
  return (
    <label className="v2-label">
      {label}
      <Input className="mt-1 w-full" {...rest} />
      {hint ? <span className="mt-1 block text-xs text-gray-400">{hint}</span> : null}
    </label>
  );
}

// patchFromForm builds the patch from the fields the operator actually typed: an
// empty input means "leave unchanged", never "set to empty".
function patchFromForm(form: {
  note: string;
  prefix: string;
  priority: string;
  proxyURL: string;
  concurrency: string;
  concurrency15s: string;
  windowSeconds: string;
  disabled: "unchanged" | "enabled" | "disabled";
  websockets: "unchanged" | "on" | "off";
}): BatchPatch {
  const patch: BatchPatch = {};
  if (form.note.trim() !== "") patch.note = form.note.trim();
  if (form.prefix.trim() !== "") patch.prefix = form.prefix.trim();
  if (form.priority.trim() !== "") {
    const priority = Number(form.priority);
    if (Number.isFinite(priority)) patch.priority = priority;
  }
  if (form.proxyURL.trim() !== "") patch.proxy_url = form.proxyURL.trim();
  if (form.concurrency.trim() !== "") {
    const value = Number(form.concurrency);
    if (Number.isFinite(value)) patch.concurrency_limit = value;
  }
  if (form.concurrency15s.trim() !== "") {
    const value = Number(form.concurrency15s);
    if (Number.isFinite(value)) patch.concurrency_15s_limit = value;
  }
  if (form.windowSeconds.trim() !== "") {
    const value = Number(form.windowSeconds);
    if (Number.isFinite(value)) patch.concurrency_window_seconds = value;
  }
  if (form.disabled !== "unchanged") patch.disabled = form.disabled === "disabled";
  if (form.websockets !== "unchanged") patch.websockets = form.websockets === "on";
  return patch;
}

const emptyForm = {
  note: "",
  prefix: "",
  priority: "",
  proxyURL: "",
  concurrency: "",
  concurrency15s: "",
  windowSeconds: "",
  disabled: "unchanged" as const,
  websockets: "unchanged" as const,
};

// EditAccountsDialog edits one account or a whole scope through the host's
// preview/apply pair, so a single flow covers "edit this account" and "batch edit
// what I filtered to".
export function EditAccountsDialog({
  scope,
  account,
  open,
  onClose,
  onError,
  onDone,
}: DialogProps & { scope: TargetScope; account?: { id: string; display_name: string } }) {
  const copy = useManagementCopy();
  const [form, setForm] = useState({ ...emptyForm });
  const [preview, setPreview] = useState<BatchPreview | null>(null);
  const [job, setJob] = useState<JobSnapshot | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [loaded, setLoaded] = useState<AccountEditableConfig | null>(null);

  useEffect(() => {
    if (!open) {
      setForm({ ...emptyForm });
      setPreview(null);
      setJob(null);
      setError("");
      setLoaded(null);
    }
  }, [open]);

  // A single account opens with its current configuration, so the operator sees
  // the values they are about to override instead of guessing.
  useEffect(() => {
    if (!open || !account) return;
    let cancelled = false;
    api
      .loadAccountConfig(account.id)
      .then((config) => {
        if (!cancelled) setLoaded(config);
      })
      .catch(() => {
        // A read-only account has no editable configuration; the form still works
        // and the preview reports what the host will refuse.
        if (!cancelled) setLoaded(null);
      });
    return () => {
      cancelled = true;
    };
  }, [open, account]);

  useJobPolling(
    Boolean(job?.running),
    () => api.getJobStatus(true),
    setJob,
  );

  const createPreview = async () => {
    const patch = patchFromForm(form);
    if (Object.keys(patch).length === 0) {
      setError(copy.leaveEmpty);
      return;
    }
    setBusy(true);
    setError("");
    try {
      setPreview(await api.createPreview(scope, patch));
      setJob(null);
    } catch (caught) {
      const message = caught instanceof Error ? caught.message : String(caught);
      setError(message);
      onError(message);
    } finally {
      setBusy(false);
    }
  };

  const apply = async () => {
    if (!preview) return;
    setBusy(true);
    setError("");
    try {
      setJob(await api.startBatch(preview.id));
      onDone();
    } catch (caught) {
      const message = caught instanceof Error ? caught.message : String(caught);
      setError(message);
      onError(message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      title={account ? `${copy.edit} · ${account.display_name}` : copy.batchEdit}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {copy.close}
          </Button>
          {preview ? (
            <Button onClick={apply} loading={busy} disabled={preview.eligible === 0 || Boolean(job?.running)}>
              {busy ? copy.applying : copy.apply}
            </Button>
          ) : (
            <Button onClick={createPreview} loading={busy}>
              {copy.preview}
            </Button>
          )}
        </>
      }
    >
      <div className="space-y-4">
        <p className="v2-hint">{copy.editHint}</p>
        {loaded ? (
          <div className="rounded-lg border border-gray-200 bg-gray-50 px-3 py-2">
            <KeyValue label={copy.disabled}>{loaded.disabled ? "true" : "false"}</KeyValue>
            <KeyValue label={copy.note}>{loaded.note || "-"}</KeyValue>
            <KeyValue label={copy.prefix}>{loaded.prefix || "-"}</KeyValue>
            <KeyValue label={copy.priority}>{loaded.priority}</KeyValue>
          </div>
        ) : null}
        <div className="grid gap-3 sm:grid-cols-2">
          <LabeledInput
            label={copy.note}
            value={form.note}
            placeholder={copy.leaveEmpty}
            onChange={(event) => setForm({ ...form, note: event.target.value })}
          />
          <LabeledInput
            label={copy.prefix}
            value={form.prefix}
            placeholder={copy.leaveEmpty}
            onChange={(event) => setForm({ ...form, prefix: event.target.value })}
          />
          <LabeledInput
            label={copy.priority}
            value={form.priority}
            inputMode="numeric"
            placeholder={copy.leaveEmpty}
            onChange={(event) => setForm({ ...form, priority: event.target.value })}
          />
          <LabeledInput
            label={copy.proxyURL}
            value={form.proxyURL}
            placeholder={copy.leaveEmpty}
            onChange={(event) => setForm({ ...form, proxyURL: event.target.value })}
          />
          <LabeledInput
            label={copy.concurrency}
            value={form.concurrency}
            inputMode="numeric"
            placeholder={copy.leaveEmpty}
            onChange={(event) => setForm({ ...form, concurrency: event.target.value })}
          />
          <LabeledInput
            label={copy.concurrency15s}
            value={form.concurrency15s}
            inputMode="numeric"
            placeholder={copy.leaveEmpty}
            onChange={(event) => setForm({ ...form, concurrency15s: event.target.value })}
          />
          <LabeledInput
            label={copy.windowSeconds}
            value={form.windowSeconds}
            inputMode="numeric"
            placeholder={copy.leaveEmpty}
            onChange={(event) => setForm({ ...form, windowSeconds: event.target.value })}
          />
          <label className="v2-label">
            {copy.disabled}
            <Select
              className="mt-1 w-full"
              value={form.disabled}
              onChange={(event) => setForm({ ...form, disabled: event.target.value as typeof form.disabled })}
            >
              <option value="unchanged">{copy.leaveEmpty}</option>
              <option value="enabled">false</option>
              <option value="disabled">true</option>
            </Select>
          </label>
          <label className="v2-label">
            {copy.websockets}
            <Select
              className="mt-1 w-full"
              value={form.websockets}
              onChange={(event) => setForm({ ...form, websockets: event.target.value as typeof form.websockets })}
            >
              <option value="unchanged">{copy.leaveEmpty}</option>
              <option value="on">true</option>
              <option value="off">false</option>
            </Select>
          </label>
        </div>
        {error ? <Notice tone="error">{useErrorMessage(error)}</Notice> : null}
        {preview ? <PreviewSummary preview={preview} copy={copy} /> : null}
        {job ? <JobProgress job={job} copy={copy} /> : null}
      </div>
    </Dialog>
  );
}

export function ModelsDialog({
  account,
  open,
  onClose,
  onError,
}: Omit<DialogProps, "onDone"> & { account: { id: string; display_name: string } }) {
  const copy = useManagementCopy();
  const [catalog, setCatalog] = useState<AccountModelCatalogResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setLoading(true);
    setError("");
    api
      .loadAccountModels({ mode: "selected", ids: [account.id] })
      .then((next) => {
        if (!cancelled) setCatalog(next);
      })
      .catch((caught) => {
        const message = caught instanceof Error ? caught.message : String(caught);
        if (cancelled) return;
        setError(message);
        onError(message);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [open, account, onError]);

  return (
    <Dialog
      open={open}
      title={`${copy.models} · ${account.display_name}`}
      onClose={onClose}
      footer={
        <Button variant="ghost" onClick={onClose}>
          {copy.close}
        </Button>
      }
    >
      <div className="space-y-3">
        <p className="v2-hint">{copy.modelsHint}</p>
        {loading ? <Skeleton className="h-40" /> : null}
        {error ? <ErrorState message={error} /> : null}
        {catalog ? (
          <>
            <div className="v2-metrics">
              <Metric label={copy.targets} value={catalog.total} />
              <Metric label={copy.eligible} value={catalog.eligible} />
              <Metric label={copy.readOnly} value={catalog.read_only} />
              <Metric label={copy.missing} value={catalog.missing} />
            </div>
            {catalog.warnings?.map((warning) => (
              <p key={warning} className="text-xs text-amber-700">
                {warning}
              </p>
            ))}
            {catalog.models.length === 0 ? (
              <p className="text-sm text-gray-500">{copy.none}</p>
            ) : (
              <div className="max-h-72 overflow-y-auto rounded-lg border border-gray-200">
                <table className="v2-table">
                  <thead>
                    <tr>
                      <th>{copy.model}</th>
                      <th>type</th>
                      <th>owned_by</th>
                    </tr>
                  </thead>
                  <tbody>
                    {catalog.models.map((model) => (
                      <tr key={model.id} className="border-b border-gray-100 last:border-0">
                        <td className="break-all text-gray-900">{model.id}</td>
                        <td className="text-gray-600">{model.type ?? "-"}</td>
                        <td className="text-gray-600">{model.owned_by ?? "-"}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </>
        ) : null}
      </div>
    </Dialog>
  );
}

export function TestModelDialog({
  account,
  open,
  onClose,
  onError,
}: Omit<DialogProps, "onDone"> & { account: { id: string; display_name: string } }) {
  const copy = useManagementCopy();
  const [model, setModel] = useState("");
  const [result, setResult] = useState<ModelTestResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const run = async () => {
    if (model.trim() === "") return;
    setBusy(true);
    setError("");
    try {
      setResult(await api.testAccountModel(account.id, model.trim()));
    } catch (caught) {
      const message = caught instanceof Error ? caught.message : String(caught);
      setError(message);
      onError(message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      title={`${copy.test} · ${account.display_name}`}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {copy.close}
          </Button>
          <Button onClick={run} loading={busy} disabled={model.trim() === ""}>
            {copy.start}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <p className="v2-hint">{copy.testHint}</p>
        <LabeledInput label={copy.model} value={model} onChange={(event) => setModel(event.target.value)} placeholder="gpt-5.6-sol" />
        {error ? <Notice tone="error">{useErrorMessage(error)}</Notice> : null}
        {result ? (
          <div className="space-y-2">
            <div className="v2-metrics">
              <Metric label={copy.status} value={result.status} />
              <Metric label={copy.latency} value={`${result.latency_ms} ms`} />
              <Metric label={copy.metrics.http} value={result.status_code ?? "-"} />
              <Metric label={copy.model} value={result.selected_model ?? result.model} />
            </div>
            <KeyValue label={copy.reason}>{result.reason_code || "-"}</KeyValue>
            <KeyValue label={copy.testedAt}>{result.tested_at}</KeyValue>
          </div>
        ) : null}
      </div>
    </Dialog>
  );
}

export function DeleteAccountDialog({
  account,
  open,
  onClose,
  onError,
  onDone,
}: DialogProps & { account: { id: string; display_name: string } }) {
  const copy = useManagementCopy();
  const [preview, setPreview] = useState<AccountDeletePreview | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const [deleted, setDeleted] = useState(false);

  useEffect(() => {
    if (!open) {
      setPreview(null);
      setConfirmed(false);
      setDeleted(false);
      setError("");
    }
  }, [open]);

  const makePreview = async () => {
    setBusy(true);
    setError("");
    try {
      setPreview(await api.createAccountDeletePreview(account.id));
    } catch (caught) {
      const message = caught instanceof Error ? caught.message : String(caught);
      setError(message);
      onError(message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!preview) return;
    setBusy(true);
    setError("");
    try {
      await api.deleteAccount(preview.id);
      setDeleted(true);
      onDone();
    } catch (caught) {
      const message = caught instanceof Error ? caught.message : String(caught);
      setError(message);
      onError(message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      title={`${copy.delete} · ${account.display_name}`}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {copy.close}
          </Button>
          {preview ? (
            <Button variant="danger" onClick={remove} loading={busy} disabled={!confirmed || deleted}>
              {copy.confirmDelete}
            </Button>
          ) : (
            <Button onClick={makePreview} loading={busy}>
              {copy.preview}
            </Button>
          )}
        </>
      }
    >
      <div className="space-y-3">
        <p className="v2-hint">{copy.deleteHint}</p>
        {preview ? (
          <>
            <div className="rounded-lg border border-amber-200 bg-amber-50 px-3 py-2">
              <KeyValue label="name">{preview.account.name}</KeyValue>
              <KeyValue label={copy.status}>{preview.account.status ?? "-"}</KeyValue>
              <KeyValue label="source">{preview.account.source ?? "-"}</KeyValue>
            </div>
            <label className="flex items-center gap-2 text-sm text-gray-700">
              <input type="checkbox" checked={confirmed} onChange={(event) => setConfirmed(event.target.checked)} />
              {copy.confirmDelete}
            </label>
          </>
        ) : null}
        {deleted ? <p className="text-sm text-green-700">{copy.removed}</p> : null}
        {error ? <Notice tone="error">{useErrorMessage(error)}</Notice> : null}
      </div>
    </Dialog>
  );
}

export function BatchDeleteDialog({
  scope,
  open,
  onClose,
  onError,
  onDone,
}: DialogProps & { scope: TargetScope }) {
  const copy = useManagementCopy();
  const [preview, setPreview] = useState<BatchPreview | null>(null);
  const [job, setJob] = useState<JobSnapshot | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [confirmed, setConfirmed] = useState(false);

  useEffect(() => {
    if (!open) {
      setPreview(null);
      setJob(null);
      setConfirmed(false);
      setError("");
    }
  }, [open]);

  useJobPolling(
    Boolean(job?.running),
    () => api.getJobStatus(true),
    setJob,
  );

  const makePreview = async () => {
    setBusy(true);
    setError("");
    try {
      setPreview(await api.createBatchDeletePreview(scope));
      setJob(null);
    } catch (caught) {
      const message = caught instanceof Error ? caught.message : String(caught);
      setError(message);
      onError(message);
    } finally {
      setBusy(false);
    }
  };

  const apply = async () => {
    if (!preview) return;
    setBusy(true);
    setError("");
    try {
      setJob(await api.startBatchDelete(preview.id));
      onDone();
    } catch (caught) {
      const message = caught instanceof Error ? caught.message : String(caught);
      setError(message);
      onError(message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      title={copy.batchDelete}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {copy.close}
          </Button>
          {preview ? (
            <Button variant="danger" onClick={apply} loading={busy} disabled={preview.eligible === 0 || !confirmed}>
              {copy.confirmDelete}
            </Button>
          ) : (
            <Button onClick={makePreview} loading={busy}>
              {copy.preview}
            </Button>
          )}
        </>
      }
    >
      <div className="space-y-3">
        <p className="v2-hint">{copy.deleteHint}</p>
        {preview ? (
          <>
            <PreviewSummary preview={preview} copy={copy} />
            <label className="flex items-center gap-2 text-sm text-gray-700">
              <input type="checkbox" checked={confirmed} onChange={(event) => setConfirmed(event.target.checked)} />
              {copy.confirmDelete}
            </label>
          </>
        ) : null}
        {job ? <JobProgress job={job} copy={copy} /> : null}
        {error ? <Notice tone="error">{useErrorMessage(error)}</Notice> : null}
      </div>
    </Dialog>
  );
}

export function DedupeDialog({
  open,
  onClose,
  onError,
  onDone,
}: DialogProps) {
  const copy = useManagementCopy();
  const [options, setOptions] = useState({ ignore_account_id: false, exclude_team_accounts: true });
  const [preview, setPreview] = useState<AccountDeduplicationPreview | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!open) {
      setPreview(null);
      setError("");
    }
  }, [open]);

  const scan = async () => {
    setBusy(true);
    setError("");
    try {
      setPreview(await api.scanAccountDuplicates(options));
    } catch (caught) {
      const message = caught instanceof Error ? caught.message : String(caught);
      setError(message);
      onError(message);
    } finally {
      setBusy(false);
    }
  };

  // The deletion set is the recommendation, not a guess: only members the host
  // marked for deletion are offered, and the batch delete previews them again.
  const deleteIDs = useMemo(
    () =>
      (preview?.groups ?? []).flatMap((group) =>
        group.members.filter((member) => member.recommended_action === "delete").map((member) => member.id),
      ),
    [preview],
  );

  const [pendingDelete, setPendingDelete] = useState<TargetScope | null>(null);

  return (
    <>
      <Dialog
        open={open}
        title={copy.dedupe}
        onClose={onClose}
        footer={
          <>
            <Button variant="ghost" onClick={onClose}>
              {copy.close}
            </Button>
            {deleteIDs.length > 0 ? (
              <Button variant="danger" onClick={() => setPendingDelete({ mode: "selected", ids: deleteIDs })}>
                {copy.proposedDeletions}: {deleteIDs.length}
              </Button>
            ) : null}
            <Button onClick={scan} loading={busy}>
              {busy ? copy.scanning : copy.scan}
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          <div className="flex flex-wrap gap-4">
            <label className="flex items-center gap-2 text-sm text-gray-700">
              <input
                type="checkbox"
                checked={options.ignore_account_id}
                onChange={(event) => setOptions({ ...options, ignore_account_id: event.target.checked })}
              />
              {copy.ignoreAccountID}
            </label>
            <label className="flex items-center gap-2 text-sm text-gray-700">
              <input
                type="checkbox"
                checked={options.exclude_team_accounts}
                onChange={(event) => setOptions({ ...options, exclude_team_accounts: event.target.checked })}
              />
              {copy.excludeTeams}
            </label>
          </div>
          {preview ? (
            <>
              <div className="v2-metrics">
                <Metric label={copy.duplicateGroups} value={preview.duplicate_groups} />
                <Metric label={copy.proposedDeletions} value={preview.proposed_deletions} />
                <Metric label={copy.readOnly} value={preview.read_only_skipped} />
                <Metric label={copy.missing} value={preview.missing_identity} />
              </div>
              {preview.groups.length === 0 ? (
                <p className="text-sm text-gray-500">{copy.none}</p>
              ) : (
                <ul className="max-h-72 space-y-2 overflow-y-auto">
                  {preview.groups.map((group) => (
                    <li key={group.id} className="rounded-lg border border-gray-200 p-3">
                      <div className="flex flex-wrap items-center gap-2 text-sm">
                        <span className="font-medium text-gray-900">{group.identity_label}</span>
                        <Badge tone="gray">{group.provider}</Badge>
                        <span className="v2-hint">{group.matched_by}</span>
                      </div>
                      <ul className="mt-2 space-y-1">
                        {group.members.map((member) => (
                          <li key={member.id} className="flex items-center justify-between gap-2 text-xs">
                            <span className="break-all text-gray-600">{member.name ?? member.id}</span>
                            <Badge tone={member.recommended_action === "delete" ? "red" : "green"}>
                              {member.recommended_action === "delete" ? copy.removed : copy.keep}
                            </Badge>
                          </li>
                        ))}
                      </ul>
                    </li>
                  ))}
                </ul>
              )}
            </>
          ) : null}
          {error ? <Notice tone="error">{useErrorMessage(error)}</Notice> : null}
        </div>
      </Dialog>
      <BatchDeleteDialog
        scope={pendingDelete ?? { mode: "selected", ids: [] }}
        open={pendingDelete !== null}
        onClose={() => setPendingDelete(null)}
        onError={onError}
        onDone={() => {
          setPendingDelete(null);
          onDone();
        }}
      />
    </>
  );
}

export function ImportAccountsDialog({ open, onClose, onError, onDone }: DialogProps) {
  const copy = useManagementCopy();
  const [files, setFiles] = useState<File[]>([]);
  const [preview, setPreview] = useState<ImportPreview | null>(null);
  const [result, setResult] = useState<ImportResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!open) {
      setFiles([]);
      setPreview(null);
      setResult(null);
      setError("");
    }
  }, [open]);

  useJobPolling(
    Boolean(result?.running),
    () => api.getImportStatus(),
    setResult,
  );

  const makePreview = async () => {
    if (files.length === 0) return;
    setBusy(true);
    setError("");
    try {
      setPreview(await api.createImportPreview(files));
      setResult(null);
    } catch (caught) {
      const message = caught instanceof Error ? caught.message : String(caught);
      setError(message);
      onError(message);
    } finally {
      setBusy(false);
    }
  };

  const startImport = async () => {
    if (!preview) return;
    setBusy(true);
    setError("");
    try {
      setResult(await api.startImport(preview.id));
      onDone();
    } catch (caught) {
      const message = caught instanceof Error ? caught.message : String(caught);
      setError(message);
      onError(message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      title={copy.importAccounts}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {copy.close}
          </Button>
          {preview ? (
            <Button onClick={startImport} loading={busy} disabled={preview.total === 0}>
              {copy.start}
            </Button>
          ) : (
            <Button onClick={makePreview} loading={busy} disabled={files.length === 0}>
              {copy.preview}
            </Button>
          )}
        </>
      }
    >
      <div className="space-y-3">
        <label className="v2-label">
          {copy.pickFile}
          <input
            type="file"
            multiple
            accept=".json,.zip,.txt"
            className="mt-1 block w-full text-sm text-gray-600"
            onChange={(event) => setFiles(Array.from(event.target.files ?? []))}
          />
        </label>
        <p className="v2-hint">
          {copy.pickedFiles}: {files.length}
        </p>
        {preview ? (
          <>
            <div className="v2-metrics">
              <Metric label={copy.targets} value={preview.total} />
              <Metric label={copy.metrics.skipped} value={preview.skipped} />
              <Metric label={copy.metrics.files} value={preview.source_files} />
              <Metric label={copy.metrics.type} value={preview.input_type} />
            </div>
            {preview.warnings?.map((warning) => (
              <p key={warning} className="text-xs text-amber-700">
                {warning}
              </p>
            ))}
            <ul className="max-h-48 space-y-1 overflow-y-auto text-xs text-gray-600">
              {preview.items.slice(0, 50).map((item) => (
                <li key={item.index} className="break-all">
                  {item.target_name} {item.label ? `· ${item.label}` : ""}
                </li>
              ))}
            </ul>
          </>
        ) : null}
        {result ? (
          <div className="v2-metrics">
            <Metric label={copy.status} value={result.state} />
            <Metric label={copy.metrics.imported} value={result.imported} />
            <Metric label={copy.metrics.skipped} value={result.skipped} />
            <Metric label={copy.metrics.failed} value={result.failed} />
          </div>
        ) : null}
        {error ? <Notice tone="error">{useErrorMessage(error)}</Notice> : null}
      </div>
    </Dialog>
  );
}

export function ExportAccountsDialog({
  scope,
  open,
  onClose,
  onError,
  onNotice,
}: Omit<DialogProps, "onDone"> & { scope: TargetScope; onNotice: (message: string) => void }) {
  const copy = useManagementCopy();
  const [format, setFormat] = useState<AccountExportFormat>("cpa");
  const [mode, setMode] = useState<"selected" | "filtered">(scope.mode);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const download = async () => {
    setBusy(true);
    setError("");
    try {
      await api.downloadExport("accounts", format, { ...scope, mode });
      onNotice(copy.downloaded);
    } catch (caught) {
      const message = caught instanceof Error ? caught.message : String(caught);
      setError(message);
      onError(message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      title={copy.exportAccounts}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {copy.close}
          </Button>
          <Button onClick={download} loading={busy} disabled={mode === "selected" && (scope.ids?.length ?? 0) === 0}>
            {copy.download}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <label className="v2-label">
          {copy.format}
          <Select className="mt-1 w-full" value={format} onChange={(event) => setFormat(event.target.value as AccountExportFormat)}>
            {(["cpa", "sub2api", "cockpit", "9router", "codex", "axonhub", "codexmanager", "json", "jsonl", "csv"] as AccountExportFormat[]).map((value) => (
              <option key={value} value={value}>
                {value}
              </option>
            ))}
          </Select>
        </label>
        <label className="v2-label">
          {copy.scope}
          <Select className="mt-1 w-full" value={mode} onChange={(event) => setMode(event.target.value as "selected" | "filtered")}>
            <option value="filtered">{copy.scopeFiltered}</option>
            <option value="selected">{copy.scopeSelected}</option>
          </Select>
        </label>
        <p className="v2-hint">
          {copy.targets}: {mode === "selected" ? (scope.ids?.length ?? 0) : "-"}
        </p>
        {error ? <Notice tone="error">{useErrorMessage(error)}</Notice> : null}
      </div>
    </Dialog>
  );
}

// Adding an account. CPA owns every provider login, so this dialog is a provider
// picker plus a relay: it asks CPA to start a flow, shows the URL or device code
// CPA returned, and polls until CPA reports that it saved the credential. No
// provider endpoint is called from the browser and no second key is stored.
export function AddAccountDialog({ open, onClose, onError, onDone }: DialogProps) {
	const copy = useManagementCopy();
	const [providers, setProviders] = useState<AccountLoginProvider[]>([]);
	const [community, setCommunity] = useState<AccountLoginProvider[]>([]);
	const [loading, setLoading] = useState(false);
	const [busy, setBusy] = useState("");
	const [flow, setFlow] = useState<AccountLoginFlow | null>(null);
	const [status, setStatus] = useState<AccountLoginStatus | null>(null);
	const [error, setError] = useState("");
	// The JavaScript runtime a community sign-in needs. A host without one gets an
	// install button instead of a dead end.
	const [runtimeReady, setRuntimeReady] = useState<boolean | null>(null);
	const [installingRuntime, setInstallingRuntime] = useState(false);

	useEffect(() => {
		if (!open) {
			setFlow(null);
			setStatus(null);
			setError("");
			return;
		}
		let cancelled = false;
		setLoading(true);
		api
			.listAccountLoginProviders()
			.then((answer) => {
				if (cancelled) return;
				setProviders(answer.providers);
				setCommunity(answer.community);
				setError("");
			})
			.catch((caught: unknown) => {
				if (cancelled) return;
				const message = caught instanceof Error ? caught.message : String(caught);
				setError(message);
				onError(message);
			})
			.finally(() => {
				if (!cancelled) setLoading(false);
			});
		api
			.getProviderRuntime()
			.then((status) => {
				if (!cancelled) setRuntimeReady(status.available);
			})
			.catch(() => {
				if (!cancelled) setRuntimeReady(null);
			});
		return () => {
			cancelled = true;
		};
	}, [open, onError]);

	const poll = async (state: string) => {
		try {
			const next = await api.getAccountLoginStatus(state);
			setStatus(next);
			if (next.done) {
				onDone();
			}
		} catch (caught) {
			const message = caught instanceof Error ? caught.message : String(caught);
			setError(message);
		}
	};

	// While a flow is pending the dialog keeps asking CPA; the timer stops as soon
	// as CPA reports a terminal state or the dialog closes.
	useEffect(() => {
		if (!open || !flow || status?.done) return undefined;
		const timer = setInterval(() => {
			void poll(flow.state);
		}, 3000);
		return () => clearInterval(timer);
	}, [open, flow, status?.done]);

	const start = async (provider: AccountLoginProvider) => {
		setBusy(provider.id);
		setError("");
		setStatus(null);
		try {
			const answer = await api.startAccountLogin(provider.id);
			// A community provider answers with its own reason instead of an HTTP
			// failure: show it where the dialog already shows sign-in state.
			if (answer.error) {
				setError(answer.reason ? `${answer.error}: ${answer.reason}` : answer.error);
				setFlow(null);
				return;
			}
			if (answer.status === "failed") {
				setError(answer.message ?? copy.loginFailed);
				setFlow(null);
				return;
			}
			setFlow(answer);
		} catch (caught) {
			const message = caught instanceof Error ? caught.message : String(caught);
			setError(message);
			onError(message);
		} finally {
			setBusy("");
		}
	};

	const installRuntime = async () => {
		setInstallingRuntime(true);
		setError("");
		try {
			const status = await api.installProviderRuntime();
			if (status.installed || status.available) {
				setRuntimeReady(true);
			} else {
				setError(status.reason ?? copy.runtimeMissing);
			}
		} catch (caught) {
			setError(caught instanceof Error ? caught.message : String(caught));
		} finally {
			setInstallingRuntime(false);
		}
	};

	const flowLabel = (kind: AccountLoginFlowKind) => {
		switch (kind) {
			case "device":
				return copy.flowDevice;
			case "plugin":
				return copy.flowPlugin;
			case "community":
				return copy.flowCommunity;
			case "external":
				return copy.flowExternal;
			default:
				return copy.flowBrowser;
		}
	};

	return (
		<Dialog
			open={open}
			title={copy.addAccount}
			onClose={onClose}
			footer={
				<>
					{flow && !status?.done ? (
						<Button variant="secondary" onClick={() => void poll(flow.state)}>
							{copy.loginCheck}
						</Button>
					) : null}
					<Button variant="ghost" onClick={onClose}>
						{copy.close}
					</Button>
				</>
			}
		>
			<div className="space-y-4">
				<p className="text-sm text-gray-600">{copy.addAccountHint}</p>
				{loading ? <Skeleton className="h-20" /> : null}
				{!loading ? (
					<div className="space-y-2">
						<p className="text-xs font-medium text-gray-500">{copy.chooseProvider}</p>
						<ul className="grid gap-2 sm:grid-cols-2">
							{providers.map((provider) => (
								<li key={provider.id} className="flex items-center justify-between gap-2 rounded-lg border border-gray-200 px-3 py-2">
									<div className="min-w-0 flex-1">
										<p className="truncate text-sm font-medium text-gray-900">{provider.name}</p>
										<p className="v2-hint">{flowLabel(provider.flow)}</p>
									</div>
									<Button size="sm" loading={busy === provider.id} onClick={() => void start(provider)}>
										{copy.addAccount}
									</Button>
								</li>
							))}
						</ul>
					</div>
				) : null}
				{flow ? (
					<div className="space-y-2 rounded-lg bg-gray-50 p-3 text-sm">
						<p className="font-medium text-gray-900">{flow.provider}</p>
						{flow.url ? (
							<a className="break-all text-blue-600 underline" href={flow.url} target="_blank" rel="noreferrer">
								{copy.openLoginPage}
							</a>
						) : null}
						{flow.instructions ? <p className="text-gray-700">{flow.instructions}</p> : null}
						{flow.user_code ? (
							<p className="text-gray-700">
								{copy.userCode}: <span className="font-mono">{flow.user_code}</span>
							</p>
						) : null}
						<p className="v2-hint">
							{status?.done ? copy.loginDone : status && (status.status === "error" || status.status === "expired") ? `${copy.loginFailed}: ${status.message ?? status.status}` : copy.loginWaiting}
						</p>
					</div>
				) : null}
				{runtimeReady === false && community.some((entry) => entry.flow === "community") ? (
					<Notice tone="warn">
						<div className="flex flex-wrap items-center justify-between gap-2">
							<span>{copy.runtimeMissing}</span>
							<Button size="sm" loading={installingRuntime} onClick={() => void installRuntime()}>
								{installingRuntime ? copy.runtimeInstalling : copy.runtimeInstall}
							</Button>
						</div>
					</Notice>
				) : null}
				{community.length > 0 ? (
					<div className="space-y-2">
						<p className="text-xs font-medium text-gray-500">{copy.communityTitle}</p>
						<ul className="grid gap-2 sm:grid-cols-2">
							{community.map((provider) => (
								<li key={provider.id} className="flex items-center justify-between gap-2 rounded-lg border border-gray-200 px-3 py-2">
									<div className="min-w-0 flex-1">
										<p className="truncate text-sm font-medium text-gray-900">{provider.name}</p>
										<p className="v2-hint">{flowLabel(provider.flow)}</p>
									</div>
									<Button
										size="sm"
										loading={busy === provider.id}
										disabled={provider.flow === "external"}
										onClick={() => void start(provider)}
									>
										{copy.addAccount}
									</Button>
								</li>
							))}
						</ul>
						<p className="v2-hint">{copy.communityNote}</p>
					</div>
				) : null}
				{error ? <Notice tone="error">{useErrorMessage(error)}</Notice> : null}
			</div>
		</Dialog>
	);
}

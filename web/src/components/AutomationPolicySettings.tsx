import {
  AlertCircle,
  ArrowDown,
  ArrowUp,
  LoaderCircle,
  Plus,
  Save,
  ScanLine,
  ShieldAlert,
  Trash2,
  Workflow,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import * as api from "../api/client";
import { operatorMessage } from "../format/operatorMessage";
import { useI18n } from "../i18n";
import type { UIMessageKey } from "../i18n/uiText";
import type {
  ConditionalPolicyActions,
  ConditionalPolicyRule,
  AccountQuotaPolicy,
  DefaultPolicy,
  ExperimentalCodexIdentitySettings,
  GlobalPolicy,
  HeaderPatch,
  ModelPolicyPatch,
  ModelPolicyMode,
  PolicySnapshot,
  OperationFailureDetail,
  ProxyProfileView,
} from "../types";
import { IconButton } from "./IconButton";
import { Modal } from "./Modal";
import { PolicyConditionEditor } from "./PolicyConditionEditor";

interface AutomationPolicySettingsProps {
  refreshRevision: number;
  forceLoading: boolean;
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
  onForcePreview: () => void;
}

export function AutomationPolicySettings({ refreshRevision, forceLoading, onAPIError, onNotice, onForcePreview }: AutomationPolicySettingsProps) {
  const { locale, tx, formatDateTime } = useI18n();
  const [snapshot, setSnapshot] = useState<PolicySnapshot | null>(null);
  const [draft, setDraft] = useState<DefaultPolicy | null>(null);
  const [globalSnapshot, setGlobalSnapshot] = useState<{ policy: GlobalPolicy; storage_error?: string } | null>(null);
  const [globalDraft, setGlobalDraft] = useState<GlobalPolicy | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [scanning, setScanning] = useState(false);
  const [confirmRunAfterSave, setConfirmRunAfterSave] = useState(false);
  const [error, setError] = useState("");
  const [proxyProfiles, setProxyProfiles] = useState<ProxyProfileView[]>([]);
  const refreshRequest = useRef(0);

  const invalidateRefresh = () => {
    refreshRequest.current += 1;
  };

  const refresh = useCallback(async (signal?: AbortSignal) => {
    const requestID = ++refreshRequest.current;
    try {
      const [next, global] = await Promise.all([
        api.getDefaultPolicy(signal),
        api.getGlobalPolicy(signal).catch((caught) => {
          if (caught instanceof api.APIError && [404, 405, 501].includes(caught.status)) return { policy: emptyGlobalPolicy() };
          throw caught;
        }),
      ]);
      if (requestID !== refreshRequest.current) return;
      if (!next?.policy || !next.last_scan) throw new Error("ui.policy_unavailable");
      setSnapshot(next);
      setDraft((current) => current ?? clonePolicy(next.policy));
      setGlobalSnapshot(global);
      setGlobalDraft((current) => current ?? cloneGlobalPolicy(global.policy));
    } catch (caught) {
      if (signal?.aborted || (caught instanceof DOMException && caught.name === "AbortError")) return;
      if (requestID !== refreshRequest.current) return;
      if (caught instanceof api.APIError && caught.status === 401) onAPIError(caught);
      else setError(operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale));
    } finally {
      if (requestID === refreshRequest.current) setLoading(false);
    }
  }, [locale, onAPIError, tx]);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setDraft(null);
    setGlobalDraft(null);
    void refresh(controller.signal);
    return () => {
      controller.abort();
      invalidateRefresh();
    };
  }, [refresh, refreshRevision]);

  useEffect(() => {
    const controller = new AbortController();
    void api.listProxyProfiles(controller.signal).then((response) => {
      if (!controller.signal.aborted) setProxyProfiles(response.profiles.filter((profile) => profile.enabled !== false));
    }).catch(() => undefined);
    return () => controller.abort();
  }, [refreshRevision]);

  useEffect(() => {
    if (!snapshot?.running) return;
    const controller = new AbortController();
    let timer = 0;
    const poll = async () => {
      await refresh(controller.signal);
      if (!controller.signal.aborted) timer = window.setTimeout(() => void poll(), 1500);
    };
    timer = window.setTimeout(() => void poll(), 1500);
    return () => {
      controller.abort();
      window.clearTimeout(timer);
      invalidateRefresh();
    };
  }, [refresh, snapshot?.running]);

  const dirty = useMemo(() => Boolean(snapshot && draft && JSON.stringify(draft) !== JSON.stringify(clonePolicy(snapshot.policy))), [draft, snapshot]);
  const globalDirty = useMemo(() => Boolean(globalSnapshot && globalDraft && JSON.stringify(globalDraft) !== JSON.stringify(cloneGlobalPolicy(globalSnapshot.policy))), [globalDraft, globalSnapshot]);
  const rules = draft?.conditional_rules ?? [];
  const updateDraft = (patch: Partial<DefaultPolicy>) => setDraft((current) => current ? { ...current, ...patch } : current);
  const updateGlobalDraft = (patch: Partial<GlobalPolicy>) => setGlobalDraft((current) => current ? { ...current, ...patch } : current);
  const updateRules = (next: ConditionalPolicyRule[]) => updateDraft({ conditional_rules: next });

  const save = async () => {
    if (!draft) return;
    invalidateRefresh();
    setError("");
    if (draft.enabled && !draft.new_account_model_probe_enabled && draft.priority === null && draft.websockets === null && rules.length === 0) {
      setError(tx("ui.select_at_least_one_default_field_before_enabling_the_policy"));
      return;
    }
    if (!Number.isInteger(draft.scan_interval_seconds) || draft.scan_interval_seconds < 5 || draft.scan_interval_seconds > 300) {
      setError(tx("ui.scan_interval_must_be_between_5_and_300_seconds"));
      return;
    }
    setSaving(true);
    try {
      const next = await api.saveDefaultPolicy(draft);
      setSnapshot(next);
      setDraft(clonePolicy(next.policy));
      onNotice(tx("ui.automation_policy_saved"));
      setConfirmRunAfterSave(true);
    } catch (caught) {
      if (caught instanceof api.APIError && caught.status === 401) onAPIError(caught);
      else setError(operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale));
    } finally {
      setSaving(false);
    }
  };

  const saveGlobal = async () => {
    if (!globalDraft) return;
    setError("");
    setSaving(true);
    try {
      const next = await api.saveGlobalPolicy(globalDraft);
      setGlobalSnapshot(next);
      setGlobalDraft(cloneGlobalPolicy(next.policy));
      onNotice(tx("ui.automation_policy_saved"));
    } catch (caught) {
      if (caught instanceof api.APIError && caught.status === 401) onAPIError(caught);
      else setError(operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale));
    } finally {
      setSaving(false);
    }
  };

  const scan = async () => {
    invalidateRefresh();
    setScanning(true);
    setError("");
    try {
      const next = await api.scanDefaultPolicy();
      setSnapshot({ ...next, running: true });
      onNotice(tx("ui.automation_policy_scan_started"));
    } catch (caught) {
      if (caught instanceof api.APIError && caught.status === 401) onAPIError(caught);
      else setError(operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale));
    } finally {
      setScanning(false);
    }
  };

  if (loading || !snapshot || !draft || !globalDraft) {
    return <div className="automation-policy-loading" role="tabpanel" aria-label={tx("ui.automation_policy")}><LoaderCircle className="spin" size={22} /><span>{tx("ui.loading_policy")}</span></div>;
  }

  const lastScan = snapshot.last_scan;
  const persistedFields = snapshot.policy.priority !== null || snapshot.policy.websockets !== null || Boolean(snapshot.policy.proxy_profile_id) || Boolean(snapshot.policy.ai_provider_proxy_profile_id);
  const controlsLocked = saving || forceLoading;
  const policyError = error || (snapshot.new_account_model_probe_storage_error ? tx("ui.new_account_model_probe_storage_error") : "") || operatorMessage(lastScan.error, locale);

  return (
    <section className="automation-policy-workspace" role="tabpanel" aria-label={tx("ui.automation_policy")}>
      <div className="automation-policy-overview">
        <div className="policy-status-line">
          <span className={`policy-state ${draft.enabled || rules.some((rule) => rule.enabled) ? "is-enabled" : ""}`}><span />{tx(draft.enabled || rules.some((rule) => rule.enabled) ? "ui.auto_apply" : "ui.stopped")}</span>
          <span>{snapshot.running ? <LoaderCircle className="spin" size={14} /> : <Workflow size={14} />}{snapshot.running ? tx("ui.scanning") : tx("ui.last_scan_time", { time: formatDateTime(lastScan.finished_at) })}</span>
        </div>
        <div className="policy-metrics" aria-label={tx("ui.latest_scan_metrics")}>
          <PolicyMetric label={tx("ui.scanned")} value={lastScan.scanned} />
          <PolicyMetric label={tx("ui.updated_2")} value={lastScan.changed} tone="success" />
          <PolicyMetric label={tx("ui.skipped")} value={lastScan.skipped} />
          <PolicyMetric label={tx("ui.failed")} value={lastScan.failed} tone={lastScan.failed ? "danger" : ""} />
        </div>
        {lastScan.failure_details?.length ? <PolicyFailureDetails details={lastScan.failure_details} /> : null}
      </div>

      <GlobalPolicyEditor policy={globalDraft} profiles={proxyProfiles} disabled={controlsLocked} storageError={globalSnapshot?.storage_error} onChange={updateGlobalDraft} onSave={() => void saveGlobal()} />

      <section className="automation-policy-section default-automation-policy-section" aria-label={tx("ui.default_policy_heading")}>
        <header><div><strong>{tx("ui.default_policy_heading")}</strong><span>{tx("ui.default_policy_description")}</span></div><button className="button button-primary" type="button" disabled={controlsLocked || !dirty} onClick={() => void save()}><Save size={15} />{tx("ui.save_default_policy")}</button></header>
        <p className="policy-section-help">{tx("ui.default_policy_help")}</p>
        <div className="policy-form global-policy-form">
          <div className="global-policy-group global-policy-group-primary">
            <div className="global-policy-group-heading"><strong>{tx("ui.automation_runtime_group")}</strong><span>{tx("ui.automation_runtime_group_help")}</span></div>
            <div className="global-policy-fields">
              <label className={`policy-row policy-master ${draft.enabled ? "is-enabled" : ""}`}>
                <span><strong>{tx("ui.auto_apply")}</strong><small>{tx("ui.auth_files")}</small></span>
                <span className="switch-control"><input type="checkbox" checked={draft.enabled} disabled={controlsLocked} onChange={(event) => updateDraft({ enabled: event.target.checked })} aria-label={tx("ui.enable_default_policy")} /><b>{tx(draft.enabled ? "ui.on_2" : "ui.off_2")}</b></span>
              </label>
              <label className={`policy-row ${draft.new_account_model_probe_enabled ? "is-enabled" : ""}`}>
                <span><strong>{tx("ui.new_account_model_probe")}</strong><small>{tx("ui.new_account_model_probe_description")}</small></span>
                <span className="switch-control"><input type="checkbox" checked={draft.new_account_model_probe_enabled} disabled={controlsLocked} onChange={(event) => updateDraft({ new_account_model_probe_enabled: event.target.checked })} aria-label={tx("ui.enable_new_account_model_probe")} /><b>{tx(draft.new_account_model_probe_enabled ? "ui.on_2" : "ui.off_2")}</b></span>
              </label>
              <OptionalNumberRow label="Priority" ariaLabel={tx("ui.default_priority")} value={draft.priority} disabled={controlsLocked} onChange={(priority) => updateDraft({ priority })} />
              <OptionalNumberRow label={tx("ui.account_concurrency_active_limit_setting")} ariaLabel={tx("ui.account_concurrency_active_limit_setting")} value={draft.concurrency_limit ?? null} disabled={controlsLocked} onChange={(concurrency_limit) => updateDraft({ concurrency_limit })} />
              <OptionalNumberRow label={tx("ui.account_concurrency_request_limit")} ariaLabel={tx("ui.account_concurrency_request_limit")} value={draft.concurrency_15s_limit ?? null} disabled={controlsLocked} onChange={(concurrency_15s_limit) => updateDraft({ concurrency_15s_limit })} />
              <OptionalNumberRow label={tx("ui.account_concurrency_window_seconds")} ariaLabel={tx("ui.account_concurrency_window_seconds")} value={draft.concurrency_window_seconds ?? null} disabled={controlsLocked} onChange={(concurrency_window_seconds) => updateDraft({ concurrency_window_seconds })} />
              <label className="policy-row policy-interval"><span className="edit-optin">{tx("ui.scan_interval")}</span><span className="number-suffix"><input type="number" min="5" max="300" value={draft.scan_interval_seconds} disabled={controlsLocked} onChange={(event) => updateDraft({ scan_interval_seconds: Number(event.target.value) })} aria-label={tx("ui.scan_interval")} /><b>{tx("ui.seconds")}</b></span></label>
            </div>
          </div>
          <div className="global-policy-group">
            <div className="global-policy-group-heading"><strong>{tx("ui.routing_group")}</strong><span>{tx("ui.routing_group_help")}</span></div>
            <div className="global-policy-fields">
              <OptionalBooleanRow label="WebSockets" ariaLabel={tx("ui.default_websockets")} value={draft.websockets} disabled={controlsLocked} onChange={(websockets) => updateDraft({ websockets })} />
              <div className="policy-proxy-group"><div className="policy-subsection-heading"><strong>{tx("ui.proxy_profiles")}</strong><span>{tx("ui.global_proxy_profile_help")}</span></div><ProxyProfileRow label={tx("ui.default_account_proxy")} value={draft.proxy_profile_id ?? null} profiles={proxyProfiles} disabled={controlsLocked} onChange={(proxy_profile_id) => updateDraft({ proxy_profile_id })} /><ProxyProfileRow label={tx("ui.default_ai_provider_proxy")} value={draft.ai_provider_proxy_profile_id ?? null} profiles={proxyProfiles} disabled={controlsLocked} onChange={(ai_provider_proxy_profile_id) => updateDraft({ ai_provider_proxy_profile_id })} /></div>
            </div>
          </div>
        </div>
      </section>

      <section className="automation-policy-section conditional-policy-section" aria-label={tx("ui.conditional_policies")}>
        <header>
          <div><strong>{tx("ui.conditional_policies")}</strong><span>{tx("ui.conditional_policies_description")}</span></div>
          <button className="button button-primary" type="button" disabled={controlsLocked || rules.length >= 100} onClick={() => updateRules([...rules, newConditionalRule(rules.length)])}><Plus size={15} />{tx("ui.add_policy")}</button>
        </header>
        <div className="conditional-rule-list">
          {rules.length === 0 ? <div className="conditional-policy-empty"><Workflow size={24} /><strong>{tx("ui.no_conditional_policies")}</strong><span>{tx("ui.no_conditional_policies_description")}</span></div> : null}
          {rules.map((rule, index) => (
            <ConditionalRuleEditor
              key={rule.id}
              rule={rule}
              index={index}
              total={rules.length}
              disabled={controlsLocked}
              onChange={(next) => updateRules(rules.map((item, itemIndex) => itemIndex === index ? next : item))}
              onMove={(offset) => updateRules(moveRule(rules, index, index + offset))}
              profiles={proxyProfiles}
              onDelete={() => updateRules(rules.filter((_, itemIndex) => itemIndex !== index))}
            />
          ))}
        </div>
      </section>

      {policyError ? <div className="policy-error automation-policy-error" role="alert"><AlertCircle size={16} /><span>{policyError}</span></div> : null}
      <footer className="automation-policy-actions">
        <span>{tx("ui.higher_priority_policy_overrides_actions")}</span>
        <button className="button button-warning" type="button" disabled={forceLoading || saving || snapshot.running || dirty || !persistedFields} onClick={onForcePreview}>{forceLoading ? <LoaderCircle className="spin" size={15} /> : <ShieldAlert size={15} />}{tx("ui.force_sync")}</button>
        <button className="button" type="button" disabled={scanning || saving || snapshot.running || dirty} onClick={() => void scan()}>{scanning || snapshot.running ? <LoaderCircle className="spin" size={15} /> : <ScanLine size={15} />}{tx("ui.scan_now")}</button>
        <button className="button button-primary" type="button" disabled={saving || !dirty} onClick={() => void save()}>{saving ? <LoaderCircle className="spin" size={15} /> : <Save size={15} />}{tx("ui.save_policy")}</button>
      </footer>
      {confirmRunAfterSave ? (
        <Modal
          title={tx("ui.run_automation_policy_after_save")}
          onClose={() => setConfirmRunAfterSave(false)}
          footer={(
            <>
              <button className="button" type="button" onClick={() => setConfirmRunAfterSave(false)}>{tx("ui.save_only")}</button>
              <button className="button button-primary" type="button" onClick={() => { setConfirmRunAfterSave(false); void scan(); }}><ScanLine size={15} />{tx("ui.run_asynchronously")}</button>
            </>
          )}
        >
          <div className="policy-run-confirmation">
            <Workflow size={22} />
            <div><strong>{tx("ui.automation_policy_saved")}</strong><span>{tx("ui.run_automation_policy_after_save_description")}</span></div>
          </div>
        </Modal>
      ) : null}
    </section>
  );
}

function GlobalPolicyEditor({ policy, profiles, disabled, storageError, onChange, onSave }: { policy: GlobalPolicy; profiles: ProxyProfileView[]; disabled: boolean; storageError?: string; onChange: (patch: Partial<GlobalPolicy>) => void; onSave: () => void }) {
  const { tx } = useI18n();
  const identity = policy.codex_identity ?? emptyGlobalIdentity();
  const updateIdentity = (patch: Partial<ExperimentalCodexIdentitySettings>) => onChange({ codex_identity: { ...identity, ...patch } });
  const updateQuota = (window: "five_hour" | "seven_day", field: "total_tokens" | "limit_percent", value: string) => {
    const current = policy.quota_policy ?? { five_hour: {}, seven_day: {} };
    const nextWindow = { ...current[window] };
    if (value.trim() === "") delete nextWindow[field];
    else nextWindow[field] = Number(value);
    const next = { ...current, [window]: nextWindow };
    const empty = !next.five_hour.total_tokens && next.five_hour.limit_percent === undefined && !next.seven_day.total_tokens && next.seven_day.limit_percent === undefined;
    onChange({ quota_policy: empty ? null : next });
  };
  return <section className="automation-policy-section global-policy-section" aria-label={tx("ui.global_policy_heading")}>
    <header><div><strong>{tx("ui.global_policy_heading")}</strong><span>{tx("ui.global_policy_description")}</span></div><button className="button button-primary" type="button" disabled={disabled} onClick={onSave}><Save size={15} />{tx("ui.save_global_policy")}</button></header>
    <p className="policy-section-help">{tx("ui.global_policy_help")}</p>
    {storageError ? <div className="automation-error" role="alert"><AlertCircle size={16} /><span>{storageError}</span></div> : null}
    <div className="policy-form global-policy-form">
      <div className="global-policy-group global-policy-group-primary">
        <div className="global-policy-group-heading"><strong>{tx("ui.global_basics_group")}</strong><span>{tx("ui.global_basics_group_help")}</span></div>
        <div className="global-policy-fields">
          <label className={`policy-row policy-master ${policy.enabled ? "is-enabled" : ""}`}><span><strong>{tx("ui.enabled")}</strong><small>{tx("ui.global_policy_override_status")}</small></span><span className="switch-control"><input type="checkbox" checked={policy.enabled} disabled={disabled} onChange={(event) => onChange({ enabled: event.target.checked })} /><b>{tx(policy.enabled ? "ui.on_2" : "ui.off_2")}</b></span></label>
          <OptionalBooleanRow label={tx("ui.disabled")} ariaLabel={tx("ui.disabled")} value={policy.disabled ?? null} disabled={disabled} onChange={(value) => onChange({ disabled: value })} />
          <OptionalNumberRow label={tx("ui.policy_priority")} ariaLabel={tx("ui.policy_priority")} value={policy.priority ?? null} disabled={disabled} onChange={(value) => onChange({ priority: value })} />
          <OptionalNumberRow label={tx("ui.account_concurrency_active_limit_setting")} ariaLabel={tx("ui.account_concurrency_active_limit_setting")} value={policy.concurrency_limit ?? null} disabled={disabled} onChange={(value) => onChange({ concurrency_limit: value })} />
          <OptionalNumberRow label={tx("ui.account_concurrency_request_limit")} ariaLabel={tx("ui.account_concurrency_request_limit")} value={policy.concurrency_15s_limit ?? null} disabled={disabled} onChange={(value) => onChange({ concurrency_15s_limit: value })} />
          <OptionalNumberRow label={tx("ui.account_concurrency_window_seconds")} ariaLabel={tx("ui.account_concurrency_window_seconds")} value={policy.concurrency_window_seconds ?? null} disabled={disabled} onChange={(value) => onChange({ concurrency_window_seconds: value })} />
          <label className="policy-row"><span className="edit-optin">{tx("ui.note")}</span><input value={policy.note ?? ""} disabled={disabled} placeholder={tx("ui.not_set")} onChange={(event) => onChange({ note: event.target.value || null })} /></label>
          <label className="policy-row"><span className="edit-optin">{tx("ui.route_prefix")}</span><input value={policy.prefix ?? ""} disabled={disabled} placeholder={tx("ui.not_set")} onChange={(event) => onChange({ prefix: event.target.value || null })} /></label>
        </div>
      </div>
      <div className="global-policy-group">
        <div className="global-policy-group-heading"><strong>{tx("ui.routing_group")}</strong><span>{tx("ui.routing_group_help")}</span></div>
        <div className="global-policy-fields">
          <div className="policy-proxy-group"><div className="policy-subsection-heading"><strong>{tx("ui.proxy_profiles")}</strong><span>{tx("ui.global_proxy_profile_help")}</span></div><ProxyProfileRow label={tx("ui.default_account_proxy")} value={policy.proxy_profile_id ?? null} profiles={profiles} disabled={disabled} onChange={(value) => onChange({ proxy_profile_id: value })} /><ProxyProfileRow label={tx("ui.default_ai_provider_proxy")} value={policy.ai_provider_proxy_profile_id ?? null} profiles={profiles} disabled={disabled} onChange={(value) => onChange({ ai_provider_proxy_profile_id: value })} /></div>
          <OptionalBooleanRow label="WebSockets" ariaLabel="WebSockets" value={policy.websockets ?? null} disabled={disabled} onChange={(value) => onChange({ websockets: value })} />
        </div>
      </div>
      <div className="global-policy-group global-policy-wide">
        <div className="global-policy-group-heading"><strong>{tx("ui.quota_group")}</strong><span>{tx("ui.quota_group_help")}</span></div>
        <GlobalQuotaEditor policy={policy.quota_policy ?? null} disabled={disabled} onChange={updateQuota} />
      </div>
      <div className="global-policy-group global-policy-wide">
        <div className="global-policy-group-heading"><strong>{tx("ui.request_policy_group")}</strong><span>{tx("ui.request_policy_group_help")}</span></div>
        <GlobalHeadersEditor value={policy.headers ?? null} disabled={disabled} onChange={(headers) => onChange({ headers })} />
        <GlobalModelPolicyEditor value={policy.model_policy ?? null} disabled={disabled} onChange={(model_policy) => onChange({ model_policy })} />
      </div>
      <div className="global-policy-group global-policy-wide">
        <div className="global-policy-group-heading"><strong>{tx("ui.codex_identity_group")}</strong><span>{tx("ui.codex_identity_group_help")}</span></div>
        <GlobalCodexIdentityEditor value={identity} disabled={disabled} onChange={updateIdentity} />
      </div>
    </div>
  </section>;
}

function GlobalQuotaEditor({ policy, disabled, onChange }: { policy: AccountQuotaPolicy | null; disabled: boolean; onChange: (window: "five_hour" | "seven_day", field: "total_tokens" | "limit_percent", value: string) => void }) {
  const { tx } = useI18n();
  const value = policy ?? { five_hour: {}, seven_day: {} };
  return <div className="policy-subsection global-quota-editor"><div className="policy-subsection-heading"><strong>{tx("ui.account_quota_limit")}</strong><span>{tx("ui.account_quota_limit_description")}</span></div><div className="settings-inline-grid"><QuotaInput label={tx("ui.quota_window_five_hour")} value={value.five_hour.limit_percent} disabled={disabled} suffix="%" onChange={(next) => onChange("five_hour", "limit_percent", next)} /><QuotaInput label={tx("ui.quota_window_seven_day")} value={value.seven_day.limit_percent} disabled={disabled} suffix="%" onChange={(next) => onChange("seven_day", "limit_percent", next)} /></div></div>;
}

function QuotaInput({ label, value, disabled, suffix, onChange }: { label: string; value?: number; disabled: boolean; suffix: string; onChange: (value: string) => void }) {
  return <label className="filter-control"><span>{label} · {suffix}</span><input type="number" min="0" max="100" value={value ?? ""} disabled={disabled} placeholder="-" onChange={(event) => onChange(event.target.value)} /></label>;
}

function GlobalHeadersEditor({ value, disabled, onChange }: { value: HeaderPatch | null; disabled: boolean; onChange: (value: HeaderPatch | null) => void }) {
  const { tx } = useI18n();
  const [rows, setRows] = useState<Array<{ id: number; action: "set" | "remove"; name: string; value: string }>>(() => headersToRows(value));
  useEffect(() => setRows(headersToRows(value)), [value]);
  const update = (nextRows: typeof rows) => {
    setRows(nextRows);
    const set: Record<string, string> = {};
    const remove: string[] = [];
    nextRows.forEach((row) => { if (!row.name.trim()) return; if (row.action === "remove") remove.push(row.name.trim()); else if (row.value) set[row.name.trim()] = row.value; });
    onChange(Object.keys(set).length || remove.length ? { set, remove } : null);
  };
  return <div className="policy-subsection"><div className="policy-subsection-heading"><strong>{tx("ui.headers")}</strong><span>{tx("ui.set")}/{tx("ui.remove")}</span></div><div className="header-editor">{rows.map((row) => <div className="header-row" key={row.id}><select value={row.action} disabled={disabled} onChange={(event) => update(rows.map((item) => item.id === row.id ? { ...item, action: event.target.value as "set" | "remove" } : item))}><option value="set">{tx("ui.set")}</option><option value="remove">{tx("ui.remove")}</option></select><input value={row.name} disabled={disabled} placeholder={tx("ui.header_name")} onChange={(event) => update(rows.map((item) => item.id === row.id ? { ...item, name: event.target.value } : item))} /><input value={row.value} disabled={disabled || row.action === "remove"} placeholder={tx("ui.header_value")} onChange={(event) => update(rows.map((item) => item.id === row.id ? { ...item, value: event.target.value } : item))} /><IconButton label={tx("ui.delete_header_row")} disabled={disabled || rows.length === 1} onClick={() => update(rows.filter((item) => item.id !== row.id))}><Trash2 size={15} /></IconButton></div>)}<button className="button button-quiet header-add" type="button" disabled={disabled} onClick={() => setRows((current) => [...current, { id: Date.now(), action: "set", name: "", value: "" }])}><Plus size={15} />{tx("ui.header")}</button></div></div>;
}

function GlobalModelPolicyEditor({ value, disabled, onChange }: { value: ModelPolicyPatch | null; disabled: boolean; onChange: (value: ModelPolicyPatch | null) => void }) {
  const { tx } = useI18n();
  const mode = value?.mode ?? "all";
  return <div className="policy-subsection"><div className="policy-subsection-heading"><strong>{tx("ui.model_policy")}</strong><span>{tx("ui.model_policy_mode")}</span></div><div className="model-policy-modes">{(["all", "allow_only", "deny_only"] as ModelPolicyMode[]).map((item) => <button key={item} type="button" className={mode === item ? "active" : ""} disabled={disabled} onClick={() => onChange(item === "all" ? null : { mode: item, models: value?.models ?? [] })}>{tx(item === "all" ? "ui.all_models" : item === "allow_only" ? "ui.model_allowlist" : "ui.model_blocklist")}</button>)}</div>{mode !== "all" ? <textarea rows={2} disabled={disabled} value={value?.models?.join("\n") ?? ""} placeholder="gpt-5.5" onChange={(event) => onChange({ mode, models: parseModels(event.target.value) })} aria-label={tx("ui.model_ids")} /> : null}</div>;
}

function GlobalCodexIdentityEditor({ value, disabled, onChange }: { value: ExperimentalCodexIdentitySettings; disabled: boolean; onChange: (patch: Partial<ExperimentalCodexIdentitySettings>) => void }) {
  const { tx } = useI18n();
  return <div className="policy-subsection codex-identity-global-editor">
    <div className="policy-subsection-heading"><strong>{tx("ui.codex_identity_target_policy")}</strong><span>{tx("ui.codex_identity_convergence_description")}</span></div>
    <div className="codex-identity-toggle-grid">
      <label className="switch-control"><input type="checkbox" checked={value.outbound_convergence_enabled} disabled={disabled} onChange={(event) => onChange({ outbound_convergence_enabled: event.target.checked })} /><span><b>{tx(value.outbound_convergence_enabled ? "ui.on_2" : "ui.off_2")}</b><small>{tx("ui.codex_outbound_convergence")}</small></span></label>
      <label className="switch-control"><input type="checkbox" checked={value.ingress_gate_enabled} disabled={disabled} onChange={(event) => onChange({ ingress_gate_enabled: event.target.checked })} /><span><b>{tx(value.ingress_gate_enabled ? "ui.on_2" : "ui.off_2")}</b><small>{tx("ui.codex_ingress_gate")}</small></span></label>
      <label className="switch-control"><input type="checkbox" checked={value.allow_app_server_clients} disabled={disabled} onChange={(event) => onChange({ allow_app_server_clients: event.target.checked })} /><span><b>{tx(value.allow_app_server_clients ? "ui.on_2" : "ui.off_2")}</b><small>{tx("ui.codex_allow_app_server")}</small></span></label>
    </div>
    <div className="codex-identity-runtime-grid">
      <label className="filter-control"><span>{tx("ui.codex_convergence_mode")}</span><select value={value.convergence_mode ?? ""} disabled={disabled} onChange={(event) => onChange({ convergence_mode: event.target.value })}><option value="">{tx("ui.codex_convergence_legacy_full")}</option><option value="off">{tx("ui.codex_convergence_off")}</option><option value="device">{tx("ui.codex_convergence_device")}</option><option value="session">{tx("ui.codex_convergence_session")}</option><option value="full">{tx("ui.codex_convergence_full")}</option></select></label>
      <label className="filter-control"><span>{tx("ui.codex_min_version")}</span><input value={value.min_version ?? ""} disabled={disabled} onChange={(event) => onChange({ min_version: event.target.value })} /></label>
      <label className="filter-control"><span>{tx("ui.codex_max_version")}</span><input value={value.max_version ?? ""} disabled={disabled} onChange={(event) => onChange({ max_version: event.target.value })} /></label>
    </div>
    <div className="codex-identity-json-grid">
      <label className="codex-policy-field"><span>{tx("ui.codex_whitelist_json")}</span><textarea rows={2} disabled={disabled} value={value.whitelist ?? ""} onChange={(event) => onChange({ whitelist: event.target.value })} /></label>
      <label className="codex-policy-field"><span>{tx("ui.codex_blacklist_json")}</span><textarea rows={2} disabled={disabled} value={value.blacklist ?? ""} onChange={(event) => onChange({ blacklist: event.target.value })} /></label>
      <label className="codex-policy-field"><span>{tx("ui.codex_fingerprint_json")}</span><textarea rows={2} disabled={disabled} value={value.fingerprint_signals ?? ""} onChange={(event) => onChange({ fingerprint_signals: event.target.value })} /></label>
    </div>
  </div>;
}

function headersToRows(value: HeaderPatch | null): Array<{ id: number; action: "set" | "remove"; name: string; value: string }> {
  const rows: Array<{ id: number; action: "set" | "remove"; name: string; value: string }> = [];
  Object.entries(value?.set ?? {}).forEach(([name, headerValue], index) => rows.push({ id: index + 1, action: "set", name, value: headerValue }));
  (value?.remove ?? []).forEach((name, index) => rows.push({ id: rows.length + index + 1, action: "remove", name, value: "" }));
  return rows.length ? rows : [{ id: 1, action: "set", name: "", value: "" }];
}

function emptyGlobalIdentity(): ExperimentalCodexIdentitySettings {
  return { outbound_convergence_enabled: false, ingress_gate_enabled: false, allow_app_server_clients: false };
}

function ConditionalRuleEditor({ rule, index, total, disabled, profiles, onChange, onMove, onDelete }: { rule: ConditionalPolicyRule; index: number; total: number; disabled: boolean; profiles: ProxyProfileView[]; onChange: (rule: ConditionalPolicyRule) => void; onMove: (offset: number) => void; onDelete: () => void }) {
  const { tx } = useI18n();
  const updateActions = (actions: ConditionalPolicyActions) => onChange({ ...rule, actions });
  return (
    <article className={`conditional-rule ${rule.enabled ? "is-enabled" : ""}`}>
      <header className="conditional-rule-header">
        <label className="conditional-rule-enabled"><input type="checkbox" checked={rule.enabled} disabled={disabled} onChange={(event) => onChange({ ...rule, enabled: event.target.checked })} /><span>{tx(rule.enabled ? "ui.on_2" : "ui.off_2")}</span></label>
        <input className="conditional-rule-name" value={rule.name} maxLength={128} disabled={disabled} onChange={(event) => onChange({ ...rule, name: event.target.value })} aria-label={tx("ui.policy_name")} placeholder={tx("ui.policy_name")} />
        <label className="conditional-rule-priority"><span>{tx("ui.policy_priority")}</span><input type="number" min="-10000" max="10000" value={rule.priority} disabled={disabled} onChange={(event) => onChange({ ...rule, priority: Number(event.target.value) })} /></label>
        <div className="conditional-rule-tools">
          <IconButton label={tx("ui.move_policy_up")} disabled={disabled || index === 0} onClick={() => onMove(-1)}><ArrowUp size={15} /></IconButton>
          <IconButton label={tx("ui.move_policy_down")} disabled={disabled || index === total - 1} onClick={() => onMove(1)}><ArrowDown size={15} /></IconButton>
          <IconButton label={tx("ui.delete_policy")} disabled={disabled} onClick={onDelete}><Trash2 size={15} /></IconButton>
        </div>
      </header>
      <div className="conditional-rule-body">
        <section className="conditional-rule-conditions"><h4>{tx("ui.match_conditions")}</h4><PolicyConditionEditor group={rule.conditions} disabled={disabled} onChange={(conditions) => onChange({ ...rule, conditions })} /></section>
        <section className="conditional-rule-actions"><h4>{tx("ui.automation_actions")}</h4><p className="policy-action-help">{tx("ui.conditional_proxy_profile_help")}</p>
          <OptionalBooleanAction label={tx("ui.new_account_model_probe")} present={hasOwn(rule.actions, "new_account_model_probe")} value={rule.actions.new_account_model_probe ?? false} disabled={disabled} onChange={(present, value) => updateActions(updateOptionalAction(rule.actions, "new_account_model_probe", present, value))} />
          <OptionalNumberAction label="Priority" present={hasOwn(rule.actions, "priority")} value={rule.actions.priority ?? 0} disabled={disabled} onChange={(present, value) => updateActions(updateOptionalAction(rule.actions, "priority", present, value))} />
          <OptionalNumberAction label={tx("ui.account_concurrency_active_limit_setting")} present={hasOwn(rule.actions, "concurrency_limit")} value={rule.actions.concurrency_limit ?? 0} disabled={disabled} onChange={(present, value) => updateActions(updateOptionalAction(rule.actions, "concurrency_limit", present, value))} />
          <OptionalNumberAction label={tx("ui.account_concurrency_request_limit")} present={hasOwn(rule.actions, "concurrency_15s_limit")} value={rule.actions.concurrency_15s_limit ?? 0} disabled={disabled} onChange={(present, value) => updateActions(updateOptionalAction(rule.actions, "concurrency_15s_limit", present, value))} />
          <OptionalNumberAction label={tx("ui.account_concurrency_window_seconds")} present={hasOwn(rule.actions, "concurrency_window_seconds")} value={rule.actions.concurrency_window_seconds ?? 15} disabled={disabled} onChange={(present, value) => updateActions(updateOptionalAction(rule.actions, "concurrency_window_seconds", present, value))} />
          <OptionalBooleanAction label="WebSockets" present={hasOwn(rule.actions, "websockets")} value={rule.actions.websockets ?? false} disabled={disabled} onChange={(present, value) => updateActions(updateOptionalAction(rule.actions, "websockets", present, value))} />
          <OptionalProxyProfileAction label={tx("ui.account_proxy_profile")} value={rule.actions.proxy_profile_id ?? null} profiles={profiles} disabled={disabled} onChange={(value) => updateActions(updateOptionalAction(rule.actions, "proxy_profile_id", value !== null, value ?? undefined))} />
          <OptionalProxyProfileAction label={tx("ui.ai_provider_proxy_profile")} value={rule.actions.ai_provider_proxy_profile_id ?? null} profiles={profiles} disabled={disabled} onChange={(value) => updateActions(updateOptionalAction(rule.actions, "ai_provider_proxy_profile_id", value !== null, value ?? undefined))} />
          <ModelPolicyAction actions={rule.actions} disabled={disabled} onChange={updateActions} />
        </section>
      </div>
    </article>
  );
}

function ProxyProfileRow({ label, value, profiles, disabled, onChange }: { label: string; value: string | null; profiles: ProxyProfileView[]; disabled: boolean; onChange: (value: string | null) => void }) {
  const { tx } = useI18n();
  return <label className={`policy-row ${value ? "is-enabled" : ""}`}><span className="edit-optin">{label}</span><select value={value ?? ""} disabled={disabled} onChange={(event) => onChange(event.target.value || null)}><option value="">{tx("ui.proxy_profile_unset")}</option>{profiles.map((profile) => <option key={profile.id} value={profile.id}>{profile.name} · {profile.proxy_url_masked}</option>)}</select></label>;
}

function OptionalProxyProfileAction({ label, value, profiles, disabled, onChange }: { label: string; value: string | null; profiles: ProxyProfileView[]; disabled: boolean; onChange: (value: string | null) => void }) {
  const { tx } = useI18n();
  const present = value !== null;
  const hasProfiles = profiles.length > 0;
  return <div className={`conditional-action conditional-proxy-action ${present ? "is-managed" : ""} ${!hasProfiles ? "is-unavailable" : ""}`}>
    <label title={!hasProfiles ? tx("ui.proxy_profile_create_hint") : undefined}>
      <input type="checkbox" checked={present} disabled={disabled || (!hasProfiles && !present)} onChange={(event) => {
        if (!event.target.checked) onChange(null);
        else if (profiles[0]) onChange(profiles[0].id);
      }} />
      <span>{label}</span>
    </label>
    {present ? <select value={value ?? ""} disabled={disabled || !hasProfiles} onChange={(event) => onChange(event.target.value || null)}>
      <option value="">{tx("ui.select_proxy_profile")}</option>
      {profiles.map((profile) => <option key={profile.id} value={profile.id}>{profile.name} · {profile.proxy_url_masked}</option>)}
    </select> : <small className="conditional-action-hint">{tx(hasProfiles ? "ui.proxy_profile_select_hint" : "ui.proxy_profile_create_hint")}</small>}
  </div>;
}

function ModelPolicyAction({ actions, disabled, onChange }: { actions: ConditionalPolicyActions; disabled: boolean; onChange: (actions: ConditionalPolicyActions) => void }) {
  const { tx } = useI18n();
  const present = Boolean(actions.model_policy);
  const mode = actions.model_policy?.mode ?? "all";
  const models = actions.model_policy?.models ?? [];
  const [modelText, setModelText] = useState(models.join("\n"));
  const setMode = (nextMode: ModelPolicyMode) => {
    const nextModels = nextMode === "all" ? [] : models.length ? models : ["gpt-5.5"];
    setModelText(nextModels.join("\n"));
    onChange({ ...actions, model_policy: { mode: nextMode, models: nextModels } });
  };
  return <div className={`conditional-action conditional-model-action ${present ? "is-managed" : ""}`}><label><input type="checkbox" checked={present} disabled={disabled} onChange={(event) => { const next = { ...actions }; setModelText(""); if (event.target.checked) next.model_policy = { mode: "all", models: [] }; else delete next.model_policy; onChange(next); }} /><span>{tx("ui.model_routing")}</span></label>{present ? <div className="conditional-model-policy"><div className="model-policy-modes">{(["all", "allow_only", "deny_only"] as ModelPolicyMode[]).map((item) => <button key={item} type="button" className={mode === item ? "active" : ""} disabled={disabled} onClick={() => setMode(item)}>{tx(item === "all" ? "ui.all_models" : item === "allow_only" ? "ui.allow_list" : "ui.deny_list")}</button>)}</div>{mode !== "all" ? <textarea rows={2} disabled={disabled} value={modelText} placeholder="gpt-5.5" onChange={(event) => { setModelText(event.target.value); onChange({ ...actions, model_policy: { mode, models: parseModels(event.target.value) } }); }} aria-label={tx("ui.model_ids")} /> : null}</div> : null}</div>;
}

function OptionalNumberRow({ label, ariaLabel, value, disabled, onChange }: { label: string; ariaLabel: string; value: number | null; disabled: boolean; onChange: (value: number | null) => void }) {
  return <div className={`policy-row ${value !== null ? "is-enabled" : ""}`}><label className="edit-optin"><input type="checkbox" checked={value !== null} disabled={disabled} onChange={(event) => onChange(event.target.checked ? 0 : null)} />{label}</label><input type="number" value={value ?? 0} disabled={disabled || value === null} onChange={(event) => onChange(Number(event.target.value))} aria-label={ariaLabel} /></div>;
}

function OptionalBooleanRow({ label, ariaLabel, value, disabled, onChange }: { label: string; ariaLabel: string; value: boolean | null; disabled: boolean; onChange: (value: boolean | null) => void }) {
  const { tx } = useI18n();
  return <div className={`policy-row ${value !== null ? "is-enabled" : ""}`}><label className="edit-optin"><input type="checkbox" checked={value !== null} disabled={disabled} onChange={(event) => onChange(event.target.checked ? false : null)} />{label}</label><label className="switch-control"><input type="checkbox" checked={value ?? false} disabled={disabled || value === null} onChange={(event) => onChange(event.target.checked)} aria-label={ariaLabel} /><b>{tx(value ? "ui.on_2" : "ui.off_2")}</b></label></div>;
}

function OptionalBooleanAction({ label, present, value, disabled, onChange }: { label: string; present: boolean; value: boolean; disabled: boolean; onChange: (present: boolean, value: boolean) => void }) {
  const { tx } = useI18n();
  return <div className={`conditional-action ${present ? "is-managed" : ""}`}><label><input type="checkbox" checked={present} disabled={disabled} onChange={(event) => onChange(event.target.checked, value)} /><span>{label}</span></label><label className="switch-control"><input type="checkbox" checked={value} disabled={disabled || !present} onChange={(event) => onChange(true, event.target.checked)} /><b>{tx(value ? "ui.on_2" : "ui.off_2")}</b></label></div>;
}

function OptionalNumberAction({ label, present, value, disabled, onChange }: { label: string; present: boolean; value: number; disabled: boolean; onChange: (present: boolean, value: number) => void }) {
  return <div className={`conditional-action ${present ? "is-managed" : ""}`}><label><input type="checkbox" checked={present} disabled={disabled} onChange={(event) => onChange(event.target.checked, value)} /><span>{label}</span></label><input type="number" value={value} disabled={disabled || !present} onChange={(event) => onChange(true, Number(event.target.value))} /></div>;
}

const policyFailureReasonKeys: Record<string, UIMessageKey> = {
  policy_auth_scan_failed: "ui.policy_failure_auth_scan",
  policy_auth_read_failed: "ui.policy_failure_auth_read",
  policy_account_identity_changed: "ui.policy_failure_account_identity_changed",
  policy_auth_source_changed: "ui.policy_failure_auth_source_changed",
  policy_auth_filename_invalid: "ui.policy_failure_auth_filename_invalid",
  policy_auth_projection_failed: "ui.policy_failure_auth_projection",
  policy_auth_json_invalid: "ui.policy_failure_auth_json_invalid",
  policy_auth_update_failed: "ui.policy_failure_auth_update",
  policy_auth_save_failed: "ui.policy_failure_auth_save",
  policy_model_policy_unavailable: "ui.policy_failure_model_policy_unavailable",
  policy_model_policy_apply_failed: "ui.policy_failure_model_policy_apply",
  policy_quota_metadata_probe_failed: "ui.policy_failure_quota_metadata",
  policy_state_persist_failed: "ui.policy_failure_state_persist",
};

function PolicyFailureDetails({ details }: { details: OperationFailureDetail[] }) {
  const { tx } = useI18n();
  return <section className="policy-failure-details" aria-label={tx("ui.failure_basis")}>
    <div className="policy-failure-heading"><ShieldAlert size={15} /><strong>{tx("ui.failure_basis")}</strong></div>
    <ul>{details.map((detail, index) => <li key={`${detail.reason_code}-${index}`}><span>{tx(policyFailureReasonKeys[detail.reason_code] || "ui.policy_failure_unknown")}</span><b>{tx("ui.policy_failure_account_count", { count: detail.count })}</b>{detail.sample_account_ids?.length ? <code>{detail.sample_account_ids.join(", ")}</code> : null}</li>)}</ul>
  </section>;
}

function PolicyMetric({ label, value, tone = "" }: { label: string; value: number; tone?: string }) {
  return <div className={tone}><span>{label}</span><strong>{value}</strong></div>;
}

function clonePolicy(policy: DefaultPolicy): DefaultPolicy {
  return JSON.parse(JSON.stringify({ ...policy, codex_quota_metadata_probe_enabled: true, conditional_rules: policy.conditional_rules ?? [] })) as DefaultPolicy;
}

function emptyGlobalPolicy(): GlobalPolicy {
  return { enabled: false, disabled: null, priority: null, concurrency_limit: null, concurrency_15s_limit: null, concurrency_window_seconds: null, quota_policy: null, note: null, prefix: null, proxy_url: null, proxy_profile_id: null, ai_provider_proxy_profile_id: null, websockets: null, headers: null, model_policy: null, codex_identity: emptyGlobalIdentity() };
}

function cloneGlobalPolicy(policy: GlobalPolicy): GlobalPolicy {
  return JSON.parse(JSON.stringify({ ...emptyGlobalPolicy(), ...(policy ?? {}), codex_identity: { ...emptyGlobalIdentity(), ...(policy?.codex_identity ?? {}) } })) as GlobalPolicy;
}

function newConditionalRule(index: number): ConditionalPolicyRule {
  return { id: `rule-${Date.now().toString(36)}-${index + 1}`, name: "", enabled: true, priority: (index + 1) * 10, conditions: { operator: "all", conditions: [{ field: "provider", value: "codex" }], groups: [] }, actions: { websockets: true } };
}

function moveRule(rules: ConditionalPolicyRule[], from: number, to: number): ConditionalPolicyRule[] {
  if (to < 0 || to >= rules.length) return rules;
  const next = [...rules];
  const [rule] = next.splice(from, 1);
  next.splice(to, 0, rule);
  return next;
}

function updateOptionalAction<K extends keyof ConditionalPolicyActions>(actions: ConditionalPolicyActions, key: K, present: boolean, value: ConditionalPolicyActions[K]): ConditionalPolicyActions {
  const next = { ...actions };
  if (present) next[key] = value;
  else delete next[key];
  return next;
}

function hasOwn(object: object, key: PropertyKey): boolean {
  return Object.prototype.hasOwnProperty.call(object, key);
}

function parseModels(value: string): string[] {
  return Array.from(new Set(value.split(/[\s,]+/).map((model) => model.trim()).filter(Boolean)));
}

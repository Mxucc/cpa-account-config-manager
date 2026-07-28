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
import { useCallback, useEffect, useMemo, useState } from "react";
import * as api from "../api/client";
import { operatorMessage } from "../format/operatorMessage";
import { useI18n } from "../i18n";
import type {
  ConditionalPolicyActions,
  ConditionalPolicyRule,
  DefaultPolicy,
  ModelPolicyMode,
  PolicySnapshot,
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
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [scanning, setScanning] = useState(false);
  const [confirmRunAfterSave, setConfirmRunAfterSave] = useState(false);
  const [error, setError] = useState("");

  const refresh = useCallback(async () => {
    try {
      const next = await api.getDefaultPolicy();
      if (!next?.policy || !next.last_scan) throw new Error("ui.policy_unavailable");
      setSnapshot(next);
      setDraft((current) => current ?? clonePolicy(next.policy));
    } catch (caught) {
      if (caught instanceof api.APIError && caught.status === 401) onAPIError(caught);
      else setError(operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale));
    } finally {
      setLoading(false);
    }
  }, [locale, onAPIError, tx]);

  useEffect(() => {
    setLoading(true);
    setDraft(null);
    void refresh();
  }, [refresh, refreshRevision]);

  useEffect(() => {
    if (!snapshot?.running) return;
    const timer = window.setInterval(() => void refresh(), 1500);
    return () => window.clearInterval(timer);
  }, [refresh, snapshot?.running]);

  const dirty = useMemo(() => Boolean(snapshot && draft && JSON.stringify(draft) !== JSON.stringify(clonePolicy(snapshot.policy))), [draft, snapshot]);
  const rules = draft?.conditional_rules ?? [];
  const updateDraft = (patch: Partial<DefaultPolicy>) => setDraft((current) => current ? { ...current, ...patch } : current);
  const updateRules = (next: ConditionalPolicyRule[]) => updateDraft({ conditional_rules: next });

  const save = async () => {
    if (!draft) return;
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

  const scan = async () => {
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

  if (loading || !snapshot || !draft) {
    return <div className="automation-policy-loading" role="tabpanel" aria-label={tx("ui.automation_policy")}><LoaderCircle className="spin" size={22} /><span>{tx("ui.loading_policy")}</span></div>;
  }

  const lastScan = snapshot.last_scan;
  const persistedFields = snapshot.policy.priority !== null || snapshot.policy.websockets !== null;
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
      </div>

      <section className="automation-policy-section" aria-label={tx("ui.global_default_policy")}>
        <header><div><strong>{tx("ui.global_default_policy")}</strong><span>{tx("ui.global_default_policy_description")}</span></div></header>
        <div className="policy-form automation-global-form">
          <label className={`policy-row policy-master ${draft.enabled ? "is-enabled" : ""}`}>
            <span><strong>{tx("ui.auto_apply")}</strong><small>{tx("ui.auth_files")}</small></span>
            <span className="switch-control"><input type="checkbox" checked={draft.enabled} disabled={controlsLocked} onChange={(event) => updateDraft({ enabled: event.target.checked })} aria-label={tx("ui.enable_default_policy")} /><b>{tx(draft.enabled ? "ui.on_2" : "ui.off_2")}</b></span>
          </label>
          <label className={`policy-row ${draft.new_account_model_probe_enabled ? "is-enabled" : ""}`}>
            <span><strong>{tx("ui.new_account_model_probe")}</strong><small>{tx("ui.new_account_model_probe_description")}</small></span>
            <span className="switch-control"><input type="checkbox" checked={draft.new_account_model_probe_enabled} disabled={controlsLocked} onChange={(event) => updateDraft({ new_account_model_probe_enabled: event.target.checked })} aria-label={tx("ui.enable_new_account_model_probe")} /><b>{tx(draft.new_account_model_probe_enabled ? "ui.on_2" : "ui.off_2")}</b></span>
          </label>
          <OptionalNumberRow label="Priority" ariaLabel={tx("ui.default_priority")} value={draft.priority} disabled={controlsLocked} onChange={(priority) => updateDraft({ priority })} />
          <OptionalBooleanRow label="WebSockets" ariaLabel={tx("ui.default_websockets")} value={draft.websockets} disabled={controlsLocked} onChange={(websockets) => updateDraft({ websockets })} />
          <label className="policy-row policy-interval"><span className="edit-optin">{tx("ui.scan_interval")}</span><span className="number-suffix"><input type="number" min="5" max="300" value={draft.scan_interval_seconds} disabled={controlsLocked} onChange={(event) => updateDraft({ scan_interval_seconds: Number(event.target.value) })} aria-label={tx("ui.scan_interval")} /><b>{tx("ui.seconds")}</b></span></label>
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

function ConditionalRuleEditor({ rule, index, total, disabled, onChange, onMove, onDelete }: { rule: ConditionalPolicyRule; index: number; total: number; disabled: boolean; onChange: (rule: ConditionalPolicyRule) => void; onMove: (offset: number) => void; onDelete: () => void }) {
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
        <section className="conditional-rule-actions"><h4>{tx("ui.automation_actions")}</h4>
          <OptionalBooleanAction label={tx("ui.new_account_model_probe")} present={hasOwn(rule.actions, "new_account_model_probe")} value={rule.actions.new_account_model_probe ?? false} disabled={disabled} onChange={(present, value) => updateActions(updateOptionalAction(rule.actions, "new_account_model_probe", present, value))} />
          <OptionalNumberAction label="Priority" present={hasOwn(rule.actions, "priority")} value={rule.actions.priority ?? 0} disabled={disabled} onChange={(present, value) => updateActions(updateOptionalAction(rule.actions, "priority", present, value))} />
          <OptionalBooleanAction label="WebSockets" present={hasOwn(rule.actions, "websockets")} value={rule.actions.websockets ?? false} disabled={disabled} onChange={(present, value) => updateActions(updateOptionalAction(rule.actions, "websockets", present, value))} />
          <ModelPolicyAction actions={rule.actions} disabled={disabled} onChange={updateActions} />
        </section>
      </div>
    </article>
  );
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

function PolicyMetric({ label, value, tone = "" }: { label: string; value: number; tone?: string }) {
  return <div className={tone}><span>{label}</span><strong>{value}</strong></div>;
}

function clonePolicy(policy: DefaultPolicy): DefaultPolicy {
  return JSON.parse(JSON.stringify({ ...policy, codex_quota_metadata_probe_enabled: true, conditional_rules: policy.conditional_rules ?? [] })) as DefaultPolicy;
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

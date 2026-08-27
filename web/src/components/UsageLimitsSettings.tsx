import { AlertTriangle, LoaderCircle, Plus, Save, Trash2 } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import * as api from "../api/client";
import { operatorMessage } from "../format/operatorMessage";
import { useI18n } from "../i18n";
import type {
  UsageLimitRule,
  UsageLimitsConfig,
  UsageLimitsScope,
  UsageLimitsSnapshot,
  UsageModelLimit,
} from "../types";

const defaultRule = (allowAccountBasis: boolean): UsageLimitRule => ({
  enabled: false,
  basis: allowAccountBasis ? "account" : "credit",
  window: allowAccountBasis ? "five_hour" : undefined,
  percent: allowAccountBasis ? 80 : undefined,
  amount_usd: allowAccountBasis ? undefined : 0,
});
const emptyConfig = (allowAccountBasis: boolean): UsageLimitsConfig => ({ enabled: false, total: defaultRule(allowAccountBasis), models: [] });

function ruleFrom(rule: UsageLimitRule | undefined, allowAccountBasis: boolean): UsageLimitRule {
  const normalized = { ...defaultRule(allowAccountBasis), ...(rule ?? {}) };
  if (allowAccountBasis && normalized.basis === "account") {
    return { ...normalized, window: normalized.window ?? "five_hour", percent: normalized.percent ?? 80, amount_usd: undefined };
  }
  return { ...normalized, basis: "credit", window: undefined, percent: undefined, amount_usd: normalized.amount_usd ?? 0 };
}
function configFrom(config: UsageLimitsConfig | undefined, allowAccountBasis: boolean): UsageLimitsConfig {
  return {
    enabled: config?.enabled === true,
    total: ruleFrom(config?.total, allowAccountBasis),
    models: (config?.models ?? []).map((item) => ({ ...item, rule: ruleFrom(item.rule, allowAccountBasis) })),
  };
}

export interface UsageLimitsSettingsProps {
  scope?: UsageLimitsScope;
  value?: UsageLimitsConfig;
  onChange?: (config: UsageLimitsConfig) => void;
  compact?: boolean;
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
}

export function UsageLimitsSettings({
  scope = { kind: "provider", id: "default" },
  value,
  onChange,
  compact = false,
  onAPIError,
  onNotice,
}: UsageLimitsSettingsProps) {
  const { tx, locale } = useI18n();
  const controlled = value !== undefined && onChange !== undefined;
  const allowAccountBasis = scope.kind === "account";
  const [snapshot, setSnapshot] = useState<UsageLimitsSnapshot | null>(null);
  const [config, setConfig] = useState<UsageLimitsConfig>(() => configFrom(value ?? emptyConfig(allowAccountBasis), allowAccountBasis));
  const [loading, setLoading] = useState(!controlled);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  const applyConfig = useCallback((next: UsageLimitsConfig) => {
    const normalized = configFrom(next, allowAccountBasis);
    setConfig(normalized);
    onChange?.(normalized);
  }, [allowAccountBasis, onChange]);

  useEffect(() => {
    if (controlled) {
      setConfig(configFrom(value, allowAccountBasis));
      setLoading(false);
      return;
    }
    let active = true;
    setLoading(true);
    setError("");
    void api.getProviderUsageLimits(scope.id).then((next) => {
      if (!active) return;
      setSnapshot(next);
      setConfig(configFrom(next.config, allowAccountBasis));
    }).catch((caught) => {
      if (!active) return;
      if (caught instanceof api.APIError && caught.status === 401) onAPIError(caught);
      else setError(operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale));
    }).finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [allowAccountBasis, controlled, locale, onAPIError, scope.id, tx, value]);

  const update = (mutate: (current: UsageLimitsConfig) => UsageLimitsConfig) => {
    applyConfig(mutate(configFrom(config, allowAccountBasis)));
  };
  const updateTotal = (patch: Partial<UsageLimitRule>) => update((current) => ({ ...current, total: { ...ruleFrom(current.total, allowAccountBasis), ...patch } }));
  const updateModel = (index: number, patch: Partial<UsageModelLimit>) => update((current) => ({ ...current, models: (current.models ?? []).map((item, i) => i === index ? { ...item, ...patch } : item) }));
  const updateModelRule = (index: number, patch: Partial<UsageLimitRule>) => update((current) => ({ ...current, models: (current.models ?? []).map((item, i) => i === index ? { ...item, rule: { ...ruleFrom(item.rule, allowAccountBasis), ...patch } } : item) }));
  const addModel = () => update((current) => ({ ...current, models: [...(current.models ?? []), { model: "", within_total: true, rule: defaultRule(allowAccountBasis) }] }));
  const removeModel = (index: number) => update((current) => ({ ...current, models: (current.models ?? []).filter((_, i) => i !== index) }));

  const save = async () => {
    if (controlled) {
      onNotice(tx("ui.usage_limits_updated_in_preview"));
      return;
    }
    setSaving(true);
    setError("");
    try {
      const next = await api.saveProviderUsageLimits(scope.id, config);
      setSnapshot(next);
      setConfig(configFrom(next.config, allowAccountBasis));
      onNotice(tx("ui.usage_limits_saved"));
    } catch (caught) {
      if (caught instanceof api.APIError && caught.status === 401) onAPIError(caught);
      else setError(operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale));
    } finally { setSaving(false); }
  };

  const renderRule = (rule: UsageLimitRule, updateRule: (patch: Partial<UsageLimitRule>) => void, label: string) => (
    <div className="usage-limit-rule" aria-label={label}>
      <label className="switch-control"><input type="checkbox" checked={rule.enabled} onChange={(event) => updateRule({ enabled: event.target.checked })} /><b>{tx(rule.enabled ? "ui.on_2" : "ui.off_2")}</b></label>
      {allowAccountBasis ? <label className="filter-control"><span>{tx("ui.usage_limit_basis")}</span><select value={rule.basis} onChange={(event) => updateRule({ basis: event.target.value as UsageLimitRule["basis"] })}><option value="account">{tx("ui.usage_limit_account")}</option><option value="credit">{tx("ui.usage_limit_credit")}</option></select></label> : null}
      {allowAccountBasis && rule.basis === "account" ? <>
        <label className="filter-control"><span>{tx("ui.usage_limit_window")}</span><select value={rule.window ?? "five_hour"} onChange={(event) => updateRule({ window: event.target.value as UsageLimitRule["window"] })}><option value="five_hour">{tx("ui.usage_limit_five_hour")}</option><option value="seven_day">{tx("ui.usage_limit_seven_day")}</option></select></label>
        <label className="filter-control"><span>{tx("ui.usage_limit_percent")}</span><span className="number-suffix"><input type="number" min="1" max="100" step="1" value={rule.percent ?? 0} onChange={(event) => updateRule({ percent: Number(event.target.value) })} /><b>%</b></span></label>
      </> : <label className="filter-control"><span>{tx("ui.usage_limit_amount")}</span><span className="number-suffix"><input type="number" min="0.01" step="0.01" value={rule.amount_usd ?? 0} onChange={(event) => updateRule({ amount_usd: Number(event.target.value) })} /><b>USD</b></span></label>}
    </div>
  );

  return <section className={`settings-section usage-limits-settings${compact ? " compact" : ""}`} aria-label={tx("ui.usage_limits")}>
    {!compact ? <header><AlertTriangle size={18} /><div><strong>{tx("ui.usage_limits")}</strong><span>{tx(scope.kind === "account" ? "ui.account_usage_limits_description" : "ui.provider_usage_limits_description", { scope: scope.id })}</span></div></header> : <div className="usage-limits-inline-heading"><strong>{tx("ui.usage_limits")}</strong><span>{tx(scope.kind === "account" ? "ui.account_usage_limits_description" : "ui.provider_usage_limits_description", { scope: scope.id })}</span></div>}
    {loading ? <div className="empty-state"><LoaderCircle className="spin" size={18} />{tx("ui.loading")}</div> : <>
      {error ? <div className="automation-error" role="alert"><AlertTriangle size={16} /><span>{error}</span></div> : null}
      <label className="switch-control"><input type="checkbox" checked={config.enabled} onChange={(event) => update((current) => ({ ...current, enabled: event.target.checked }))} /><b>{tx(config.enabled ? "ui.usage_limits_enabled" : "ui.usage_limits_disabled")}</b></label>
      <div className="usage-limit-card"><div className="settings-section-heading"><div><strong>{tx("ui.usage_limit_total")}</strong><span>{tx("ui.usage_limit_total_description")}</span></div></div>{renderRule(ruleFrom(config.total, allowAccountBasis), updateTotal, tx("ui.usage_limit_total"))}</div>
      <div className="usage-limit-card"><div className="settings-section-heading"><div><strong>{tx("ui.usage_limit_models")}</strong><span>{tx("ui.usage_limit_models_description")}</span></div><button className="button button-quiet" type="button" onClick={addModel}><Plus size={15} />{tx("ui.usage_limit_add_model")}</button></div>
        {(config.models ?? []).map((item, index) => <div className="usage-limit-model-row" key={`${index}-${item.model}`}><label className="filter-control"><span>{tx("ui.usage_limit_model_name")}</span><input value={item.model} placeholder="gpt-5.4" onChange={(event) => updateModel(index, { model: event.target.value })} /></label>{renderRule(ruleFrom(item.rule, allowAccountBasis), (patch) => updateModelRule(index, patch), item.model || tx("ui.usage_limit_model_name"))}<label className="switch-control"><input type="checkbox" checked={item.within_total} onChange={(event) => updateModel(index, { within_total: event.target.checked })} /><b>{tx("ui.usage_limit_within_total")}</b></label><button className="icon-button" type="button" title={tx("ui.remove")} aria-label={tx("ui.remove")} onClick={() => removeModel(index)}><Trash2 size={16} /></button></div>)}
        {(config.models ?? []).length === 0 ? <div className="empty-state">{tx("ui.usage_limit_no_models")}</div> : null}
      </div>
      <div className="usage-limit-meter"><span>{tx("ui.usage_limit_credit_used")}</span><strong>${(snapshot?.credit_used_usd ?? 0).toFixed(4)}</strong><small>{tx("ui.usage_limit_credit_note")}</small></div>
      {!compact ? <div className="settings-section-actions"><button className="button button-primary" type="button" disabled={saving} onClick={() => void save()}>{saving ? <LoaderCircle className="spin" size={15} /> : <Save size={15} />}{tx("ui.save_settings")}</button></div> : null}
    </>}
  </section>;
}

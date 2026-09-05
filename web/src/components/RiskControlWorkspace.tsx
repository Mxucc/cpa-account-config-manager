import { AlertTriangle, CheckCircle2, Database, RefreshCw, Save, ShieldAlert, Trash2, XCircle } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import * as api from "../api/client";
import { useI18n } from "../i18n";
import type {
  RiskAuditFailurePolicy,
  RiskAuditModuleStatus,
  RiskControlConfig,
  RiskControlEvent,
  RiskControlMode,
  RiskControlSnapshot,
  RiskExternalAuditConfig,
  RiskCustomAuditConfig,
} from "../types";

interface RiskControlWorkspaceProps {
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
}

type RiskTab = "content" | "prompt" | "custom";

const defaultPromptAudit: RiskExternalAuditConfig = {
  enabled: false,
  mode: "off",
  endpoint: "",
  model: "",
  credential_env: "",
  scanners: [],
  latest_turn_only: true,
  store_pass_events: false,
  timeout_ms: 3000,
  input_limit: 32768,
  worker_count: 2,
  queue_capacity: 128,
  failure_policy: "fail_open",
  block_status: 403,
  block_message: "request blocked by prompt audit",
};

const defaultCustomAudit = {
  ...defaultPromptAudit,
  block_message: "request blocked by custom audit",
  confidence_threshold: 0.8,
  system_prompt: "",
};

const defaultConfig: RiskControlConfig = {
  enabled: false,
  mode: "off",
  blocked_keywords: [],
  model_filter: { mode: "all", models: [] },
  pre_hash_check_enabled: true,
  block_status: 403,
  block_message: "request blocked by the configured risk-control policy",
  event_retention_days: 30,
  max_events: 500,
  prompt_audit: defaultPromptAudit,
  custom_audit: defaultCustomAudit,
};

function lines(value: string): string[] {
  return [...new Set(value.split(/\r?\n|,/).map((item) => item.trim()).filter(Boolean))];
}

function formatTime(value: string, formatDateTime: (value: string) => string): string {
  if (!value) return "-";
  try { return formatDateTime(value); } catch { return value; }
}

function stringList(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value.filter((item): item is string => typeof item === "string");
}

function mergeConfig(next: RiskControlConfig | null | undefined): RiskControlConfig {
  const candidate = next ?? defaultConfig;
  const modelFilter = candidate.model_filter ?? defaultConfig.model_filter;
  const promptAudit = candidate.prompt_audit ?? defaultPromptAudit;
  const customAudit = candidate.custom_audit ?? defaultCustomAudit;
  return {
    ...defaultConfig,
    ...candidate,
    blocked_keywords: stringList(candidate.blocked_keywords),
    model_filter: {
      ...defaultConfig.model_filter,
      ...modelFilter,
      models: stringList(modelFilter.models),
    },
    prompt_audit: {
      ...defaultPromptAudit,
      ...promptAudit,
      scanners: stringList(promptAudit.scanners),
    },
    custom_audit: {
      ...defaultCustomAudit,
      ...customAudit,
      scanners: stringList(customAudit.scanners),
    },
  };
}

function mergeSnapshot(next: RiskControlSnapshot): RiskControlSnapshot {
  return {
    ...next,
    config: mergeConfig(next?.config),
    events: Array.isArray(next?.events) ? next.events : [],
  };
}

const actionKeys = {
  keyword_observe: "ui.risk_action_keyword_observe",
  keyword_block: "ui.risk_action_keyword_block",
  hash_observe: "ui.risk_action_hash_observe",
  hash_block: "ui.risk_action_hash_block",
} as const;

function AuditStatus({ status, formatNumber, tx }: { status?: RiskAuditModuleStatus; formatNumber: (value: number) => string; tx: (key: any, values?: any) => string }) {
  if (!status) return <div className="risk-audit-status muted">{tx("ui.risk_audit_status_unavailable")}</div>;
  return (
    <div className="risk-audit-status" role="status">
      <div><span>{tx("ui.risk_audit_active")}</span><strong>{status.active ? tx("ui.enabled") : tx("ui.disabled")}</strong></div>
      <div><span>{tx("ui.risk_audit_queue")}</span><strong>{formatNumber(status.queue_length)} / {formatNumber(status.queue_capacity)}</strong></div>
      <div><span>{tx("ui.risk_audit_processed")}</span><strong>{formatNumber(status.processed)}</strong></div>
      <div><span>{tx("ui.risk_audit_blocked")}</span><strong>{formatNumber(status.blocked)}</strong></div>
      <div><span>{tx("ui.risk_audit_errors")}</span><strong>{formatNumber(status.errors)}</strong></div>
      <div><span>{tx("ui.risk_audit_credential")}</span><strong>{status.credential_available ? tx("ui.risk_audit_credential_ready") : status.credential_configured ? tx("ui.risk_audit_credential_missing") : tx("ui.risk_audit_credential_unset")}</strong></div>
    </div>
  );
}

type AuditConfigFormValue = RiskExternalAuditConfig & Partial<Pick<RiskCustomAuditConfig, "confidence_threshold" | "system_prompt">>;

function AuditConfigForm({
  value,
  custom,
  status,
  onChange,
  tx,
  formatNumber,
}: {
  value: AuditConfigFormValue;
  custom: boolean;
  status?: RiskAuditModuleStatus;
  onChange: (next: AuditConfigFormValue) => void;
  tx: (key: any, values?: any) => string;
  formatNumber: (value: number) => string;
}) {
  const update = <K extends keyof AuditConfigFormValue>(key: K, next: AuditConfigFormValue[K]) => onChange({ ...value, [key]: next });
  return (
    <div className="risk-audit-panel">
      <div className="risk-audit-intro">
        <div>
          <h3>{tx(custom ? "ui.risk_custom_audit_title" : "ui.risk_prompt_audit_title")}</h3>
          <p>{tx(custom ? "ui.risk_custom_audit_description" : "ui.risk_prompt_audit_description")}</p>
        </div>
        <AuditStatus status={status} formatNumber={formatNumber} tx={tx} />
      </div>
      <div className="risk-form-grid">
        <label className="field-block risk-toggle-field"><span>{tx("ui.risk_audit_enabled")}</span><button type="button" className={`toggle-switch ${value.enabled ? "on" : ""}`} role="switch" aria-label={tx("ui.risk_audit_enabled")} aria-checked={value.enabled} onClick={() => update("enabled", !value.enabled)}><span /></button></label>
        <label className="field-block"><span>{tx("ui.risk_audit_mode")}</span><select aria-label={tx("ui.risk_audit_mode")} value={value.mode} onChange={(event) => update("mode", event.target.value as RiskControlMode)}><option value="off">{tx("ui.risk_mode_off")}</option><option value="observe">{tx("ui.risk_mode_observe")}</option><option value="pre_block">{tx("ui.risk_mode_pre_block")}</option></select></label>
        <label className="field-block risk-wide"><span>{tx("ui.risk_audit_endpoint")}</span><input aria-label={tx("ui.risk_audit_endpoint")} value={value.endpoint} onChange={(event) => update("endpoint", event.target.value)} placeholder="https://moderation.example/v1/chat/completions" /></label>
        <label className="field-block"><span>{tx("ui.risk_audit_model")}</span><input aria-label={tx("ui.risk_audit_model")} value={value.model} onChange={(event) => update("model", event.target.value)} placeholder="deepseek-v4-flash" /></label>
        <label className="field-block"><span>{tx("ui.risk_audit_credential_env")}</span><input aria-label={tx("ui.risk_audit_credential_env")} value={value.credential_env} onChange={(event) => update("credential_env", event.target.value)} placeholder="RISK_AUDIT_API_KEY" /><small>{tx("ui.risk_audit_credential_env_hint")}</small></label>
        <label className="field-block risk-wide"><span>{tx("ui.risk_audit_scanners")}</span><textarea aria-label={tx("ui.risk_audit_scanners")} rows={2} value={(value.scanners ?? []).join("\n")} onChange={(event) => update("scanners", lines(event.target.value))} placeholder={tx("ui.risk_audit_scanners_placeholder")} /></label>
        <label className="field-block risk-toggle-field"><span>{tx("ui.risk_audit_latest_turn_only")}</span><button type="button" className={`toggle-switch ${value.latest_turn_only ? "on" : ""}`} role="switch" aria-label={tx("ui.risk_audit_latest_turn_only")} aria-checked={value.latest_turn_only} onClick={() => update("latest_turn_only", !value.latest_turn_only)}><span /></button></label>
        <label className="field-block risk-toggle-field"><span>{tx("ui.risk_audit_store_pass_events")}</span><button type="button" className={`toggle-switch ${value.store_pass_events ? "on" : ""}`} role="switch" aria-label={tx("ui.risk_audit_store_pass_events")} aria-checked={value.store_pass_events} onClick={() => update("store_pass_events", !value.store_pass_events)}><span /></button></label>
        <label className="field-block"><span>{tx("ui.risk_audit_timeout")}</span><input type="number" min={250} max={30000} value={value.timeout_ms} onChange={(event) => update("timeout_ms", Number(event.target.value))} /></label>
        <label className="field-block"><span>{tx("ui.risk_audit_input_limit")}</span><input type="number" min={256} max={131072} value={value.input_limit} onChange={(event) => update("input_limit", Number(event.target.value))} /></label>
        <label className="field-block"><span>{tx("ui.risk_audit_workers")}</span><input type="number" min={1} max={16} value={value.worker_count} onChange={(event) => update("worker_count", Number(event.target.value))} /></label>
        <label className="field-block"><span>{tx("ui.risk_audit_queue_capacity")}</span><input type="number" min={1} max={1024} value={value.queue_capacity} onChange={(event) => update("queue_capacity", Number(event.target.value))} /></label>
        <label className="field-block"><span>{tx("ui.risk_audit_failure_policy")}</span><select value={value.failure_policy} onChange={(event) => update("failure_policy", event.target.value as RiskAuditFailurePolicy)}><option value="fail_open">{tx("ui.risk_audit_fail_open")}</option><option value="fail_closed">{tx("ui.risk_audit_fail_closed")}</option></select></label>
        <label className="field-block"><span>{tx("ui.risk_block_status")}</span><input type="number" min={400} max={499} value={value.block_status} onChange={(event) => update("block_status", Number(event.target.value))} /></label>
        <label className="field-block risk-wide"><span>{tx("ui.risk_block_message")}</span><input value={value.block_message} onChange={(event) => update("block_message", event.target.value)} maxLength={240} /></label>
        {custom ? <>
          <label className="field-block"><span>{tx("ui.risk_custom_confidence")}</span><input type="number" min={0} max={1} step={0.05} value={value.confidence_threshold ?? 0.8} onChange={(event) => update("confidence_threshold", Number(event.target.value))} /></label>
          <label className="field-block risk-wide"><span>{tx("ui.risk_custom_system_prompt")}</span><textarea aria-label={tx("ui.risk_custom_system_prompt")} rows={9} value={value.system_prompt ?? ""} onChange={(event) => update("system_prompt", event.target.value)} /></label>
        </> : null}
      </div>
    </div>
  );
}

export function RiskControlWorkspace({ onAPIError, onNotice }: RiskControlWorkspaceProps) {
  const { tx, formatDateTime, formatNumber } = useI18n();
  const [snapshot, setSnapshot] = useState<RiskControlSnapshot | null>(null);
  const [config, setConfig] = useState<RiskControlConfig>(defaultConfig);
  const [activeTab, setActiveTab] = useState<RiskTab>("content");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [busyAction, setBusyAction] = useState<"events" | "hashes" | "" >("");
  const [error, setError] = useState("");

  const load = useCallback(async (signal?: AbortSignal) => {
    setLoading(true);
    setError("");
    try {
      const next = mergeSnapshot(await api.getRiskControl(signal));
      setSnapshot(next);
      setConfig(next.config);
    } catch (caught) {
      if (!signal?.aborted) {
        setError(tx("ui.risk_control_load_failed"));
        onAPIError(caught);
      }
    } finally {
      if (!signal?.aborted) setLoading(false);
    }
  }, [onAPIError, tx]);

  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [load]);

  const update = <K extends keyof RiskControlConfig>(key: K, value: RiskControlConfig[K]) => setConfig((current) => ({ ...current, [key]: value }));
  const updateFilter = (mode: RiskControlConfig["model_filter"]["mode"], models = config.model_filter.models ?? []) => update("model_filter", { mode, models });

  const save = async () => {
    setSaving(true);
    setError("");
    try {
      const next = await api.saveRiskControl({
        ...config,
        blocked_keywords: lines((config.blocked_keywords ?? []).join("\n")),
        model_filter: { mode: config.model_filter.mode, models: lines((config.model_filter.models ?? []).join("\n")) },
        block_status: Math.round(Number(config.block_status) || 403),
        event_retention_days: Math.round(Number(config.event_retention_days) || 30),
        max_events: Math.round(Number(config.max_events) || 500),
        prompt_audit: { ...config.prompt_audit, scanners: lines((config.prompt_audit.scanners ?? []).join("\n")) },
        custom_audit: { ...config.custom_audit, scanners: lines((config.custom_audit.scanners ?? []).join("\n")) },
      });
      const normalized = mergeSnapshot(next);
      setSnapshot(normalized);
      setConfig(normalized.config);
      onNotice(tx("ui.risk_control_saved"));
    } catch (caught) {
      setError(tx("ui.risk_control_save_failed"));
      onAPIError(caught);
    } finally {
      setSaving(false);
    }
  };

  const clear = async (kind: "events" | "hashes") => {
    setBusyAction(kind);
    setError("");
    try {
      const next = kind === "events" ? await api.clearRiskControlEvents() : await api.clearRiskControlHashes();
      setSnapshot(mergeSnapshot(next));
      onNotice(tx(kind === "events" ? "ui.risk_control_events_cleared" : "ui.risk_control_hashes_cleared"));
    } catch (caught) {
      setError(tx("ui.risk_control_clear_failed"));
      onAPIError(caught);
    } finally {
      setBusyAction("");
    }
  };

  const statusLabel = useMemo(() => {
    if (!snapshot?.status) return snapshot ? tx("ui.risk_status_inactive") : tx("ui.loading");
    if (!snapshot.status.active) return tx("ui.risk_status_inactive");
    return snapshot.status.mode === "pre_block" ? tx("ui.risk_status_blocking") : tx("ui.risk_status_observing");
  }, [snapshot, tx]);
  const events: RiskControlEvent[] = Array.isArray(snapshot?.events) ? snapshot.events : [];
  const status = snapshot?.status;

  return (
    <section className="risk-control-workspace" aria-label={tx("ui.risk_control_center")}>
      <div className="workspace-heading risk-control-heading">
        <div><div className="risk-control-eyebrow"><ShieldAlert size={15} />{tx("ui.risk_control_center")}</div><h2>{tx("ui.risk_control_title")}</h2><p>{tx("ui.risk_control_description")}</p></div>
        <div className="workspace-heading-actions"><span className={`risk-status ${status?.active ? "active" : ""}`}><span className="risk-status-dot" />{statusLabel}</span><button className="button" type="button" disabled={loading} onClick={() => void load()}><RefreshCw className={loading ? "spin" : ""} size={16} />{tx("ui.refresh")}</button></div>
      </div>
      {error ? <div className="notice-bar warning" role="alert"><AlertTriangle size={16} />{error}</div> : null}
      {snapshot?.storage_error ? <div className="notice-bar warning" role="status"><Database size={16} />{tx("ui.risk_control_storage_warning")}</div> : null}
      <div className="risk-metrics"><article><span>{tx("ui.risk_total_events")}</span><strong>{formatNumber(status?.total_events ?? 0)}</strong><small>{tx("ui.risk_events_retained")}</small></article><article className="danger"><span>{tx("ui.risk_blocked")}</span><strong>{formatNumber(status?.blocked ?? 0)}</strong><small>{tx("ui.risk_prevented_requests")}</small></article><article className="observe"><span>{tx("ui.risk_observed")}</span><strong>{formatNumber(status?.observed ?? 0)}</strong><small>{tx("ui.risk_observe_only")}</small></article><article className="memory"><span>{tx("ui.risk_remembered_hashes")}</span><strong>{formatNumber(status?.remembered_hashes ?? 0)}</strong><small>{tx("ui.risk_hash_reuse")}</small></article></div>

      <div className="risk-card risk-config-card">
        <div className="risk-card-heading"><div><h3>{tx("ui.risk_modules")}</h3><p>{tx("ui.risk_modules_description")}</p></div><ShieldAlert size={20} /></div>
        <div className="risk-module-tabs" role="tablist" aria-label={tx("ui.risk_modules")}>
          <button type="button" role="tab" aria-selected={activeTab === "content"} className={activeTab === "content" ? "active" : ""} onClick={() => setActiveTab("content")}>{tx("ui.risk_content_moderation")}</button>
          <button type="button" role="tab" aria-selected={activeTab === "prompt"} className={activeTab === "prompt" ? "active" : ""} onClick={() => setActiveTab("prompt")}>{tx("ui.risk_prompt_audit")}</button>
          <button type="button" role="tab" aria-selected={activeTab === "custom"} className={activeTab === "custom" ? "active" : ""} onClick={() => setActiveTab("custom")}>{tx("ui.risk_custom_audit")}</button>
        </div>
        {activeTab === "content" ? <div role="tabpanel" aria-label={tx("ui.risk_content_moderation")}>
          <div className="risk-form-grid">
            <label className="field-block risk-toggle-field"><span>{tx("ui.risk_enabled")}</span><button type="button" className={`toggle-switch ${config.enabled ? "on" : ""}`} role="switch" aria-label={tx("ui.risk_enabled")} aria-checked={config.enabled} onClick={() => update("enabled", !config.enabled)}><span /></button></label>
            <label className="field-block"><span>{tx("ui.risk_mode")}</span><select aria-label={tx("ui.risk_mode")} value={config.mode} onChange={(event) => update("mode", event.target.value as RiskControlMode)}><option value="off">{tx("ui.risk_mode_off")}</option><option value="observe">{tx("ui.risk_mode_observe")}</option><option value="pre_block">{tx("ui.risk_mode_pre_block")}</option></select></label>
            <label className="field-block risk-wide"><span>{tx("ui.risk_blocked_keywords")}</span><textarea aria-label={tx("ui.risk_blocked_keywords")} rows={4} value={config.blocked_keywords.join("\n")} onChange={(event) => update("blocked_keywords", event.target.value.split(/\r?\n/))} placeholder={tx("ui.risk_keywords_placeholder")} /><small>{tx("ui.risk_keywords_hint")}</small></label>
            <label className="field-block"><span>{tx("ui.risk_model_filter")}</span><select value={config.model_filter.mode} onChange={(event) => updateFilter(event.target.value as RiskControlConfig["model_filter"]["mode"])}><option value="all">{tx("ui.risk_model_filter_all")}</option><option value="include">{tx("ui.risk_model_filter_include")}</option><option value="exclude">{tx("ui.risk_model_filter_exclude")}</option></select></label>
            <label className="field-block"><span>{tx("ui.risk_model_list")}</span><textarea aria-label={tx("ui.risk_model_list")} rows={2} value={(config.model_filter.models ?? []).join("\n")} onChange={(event) => updateFilter(config.model_filter.mode, event.target.value.split(/\r?\n/))} placeholder="gpt-5.5\ngpt-5.4-mini" disabled={config.model_filter.mode === "all"} /></label>
            <label className="field-block risk-toggle-field"><span>{tx("ui.risk_hash_check")}</span><button type="button" className={`toggle-switch ${config.pre_hash_check_enabled ? "on" : ""}`} role="switch" aria-label={tx("ui.risk_hash_check")} aria-checked={config.pre_hash_check_enabled} onClick={() => update("pre_hash_check_enabled", !config.pre_hash_check_enabled)}><span /></button></label>
            <label className="field-block"><span>{tx("ui.risk_block_status")}</span><input type="number" min={400} max={499} value={config.block_status} onChange={(event) => update("block_status", Number(event.target.value))} /></label>
            <label className="field-block risk-wide"><span>{tx("ui.risk_block_message")}</span><input value={config.block_message} onChange={(event) => update("block_message", event.target.value)} maxLength={240} /></label>
            <label className="field-block"><span>{tx("ui.risk_retention_days")}</span><input type="number" min={1} max={3650} value={config.event_retention_days} onChange={(event) => update("event_retention_days", Number(event.target.value))} /></label>
            <label className="field-block"><span>{tx("ui.risk_max_events")}</span><input type="number" min={1} max={2000} value={config.max_events} onChange={(event) => update("max_events", Number(event.target.value))} /></label>
          </div>
        </div> : activeTab === "prompt" ? <div role="tabpanel" aria-label={tx("ui.risk_prompt_audit")}><AuditConfigForm value={config.prompt_audit} custom={false} status={status?.prompt_audit} onChange={(next) => update("prompt_audit", next)} tx={tx} formatNumber={formatNumber} /></div> : <div role="tabpanel" aria-label={tx("ui.risk_custom_audit")}><AuditConfigForm value={config.custom_audit} custom status={status?.custom_audit} onChange={(next) => update("custom_audit", { ...config.custom_audit, ...next })} tx={tx} formatNumber={formatNumber} /></div>}
        <div className="risk-card-actions"><button className="button primary" type="button" disabled={saving || loading} onClick={() => void save()}>{saving ? <RefreshCw className="spin" size={16} /> : <Save size={16} />}{tx("ui.save")}</button></div>
      </div>

      <div className="risk-control-layout"><section className="risk-card risk-safety-card"><div className="risk-card-heading"><div><h3>{tx("ui.risk_safety_boundary")}</h3><p>{tx("ui.risk_safety_boundary_description")}</p></div><CheckCircle2 size={20} /></div><ul className="risk-safety-list"><li>{tx("ui.risk_safe_point_1")}</li><li>{tx("ui.risk_safe_point_2")}</li><li>{tx("ui.risk_safe_point_3")}</li></ul><div className="risk-danger-zone"><div><strong>{tx("ui.risk_memory_management")}</strong><span>{tx("ui.risk_memory_management_description")}</span></div><div className="risk-card-actions"><button className="button subtle-danger" type="button" disabled={busyAction !== ""} onClick={() => void clear("hashes")}><XCircle size={15} />{tx("ui.risk_clear_hashes")}</button><button className="button subtle-danger" type="button" disabled={busyAction !== ""} onClick={() => void clear("events")}><Trash2 size={15} />{tx("ui.risk_clear_events")}</button></div></div></section></div>
      <section className="risk-card risk-events-card"><div className="risk-card-heading"><div><h3>{tx("ui.risk_event_history")}</h3><p>{tx("ui.risk_event_history_description")}</p></div><span className="risk-event-count">{formatNumber(events.length)}</span></div><div className="risk-events-table-wrap"><table className="risk-events-table"><thead><tr><th>{tx("ui.risk_event_time")}</th><th>{tx("ui.risk_event_action")}</th><th>{tx("ui.risk_event_source")}</th><th>{tx("ui.risk_event_model")}</th><th>{tx("ui.risk_event_rule")}</th><th>{tx("ui.risk_event_hash")}</th><th>{tx("ui.risk_event_latency")}</th></tr></thead><tbody>{events.map((event) => <tr key={event.id}><td>{formatTime(event.time, formatDateTime)}</td><td><span className={`risk-action ${event.action.includes("block") ? "blocked" : "observed"}`}>{event.action.includes("block") ? <XCircle size={13} /> : <CheckCircle2 size={13} />}{tx(actionKeys[event.action])}</span></td><td><code>{event.account_ref || event.provider || "-"}</code></td><td>{event.model || "-"}<small>{event.format || ""}</small></td><td><code>{event.matched_rules?.join(", ") || "-"}</code></td><td><code>{event.input_hash ? `${event.input_hash.slice(0, 12)}…` : "-"}</code></td><td>{formatNumber(event.latency_ms)} ms</td></tr>)}</tbody></table>{!loading && events.length === 0 ? <div className="empty-state" role="status">{tx("ui.risk_no_events")}</div> : null}</div></section>
    </section>
  );
}

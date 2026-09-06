import { AlertTriangle, CheckCircle2, Database, Plus, RefreshCw, Save, ShieldAlert, Trash2, XCircle } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import * as api from "../api/client";
import { DEFAULT_RISK_SYSTEM_PROMPT } from "../constants/riskAuditPrompt";
import { useI18n } from "../i18n";
import type {
  Account,
  AccountModelOption,
  AIProviderChannelEntry,
  AIProviderChannelModel,
  RiskAuditConfig,
  RiskAuditFailurePolicy,
  RiskAuditModelSource,
  RiskAuditModuleStatus,
  RiskControlConfig,
  RiskControlEvent,
  RiskControlMode,
  RiskControlSnapshot,
  RiskSystemPrompt,
} from "../types";

interface RiskControlWorkspaceProps { onAPIError: (error: unknown) => void; onNotice: (message: string) => void; }
type RiskTab = "content" | "audit";
type AccountModelCatalogs = Record<string, AccountModelOption[]>;

const defaultPrompt: RiskSystemPrompt = { id: "default-security-audit", name: "Default security audit", system_prompt: DEFAULT_RISK_SYSTEM_PROMPT, builtin: true };
const defaultAudit: RiskAuditConfig = { enabled: false, mode: "off", endpoint: "", model: "", api_key: "", model_source: "external", account_id: "", provider_auth_index: "", provider_name: "", scanners: [], latest_turn_only: true, store_pass_events: false, timeout_ms: 3000, input_limit: 32768, worker_count: 2, queue_capacity: 128, failure_policy: "fail_open", block_status: 403, block_message: "request blocked by risk audit", confidence_threshold: 0.8, prompt_id: defaultPrompt.id };
const defaultConfig: RiskControlConfig = { enabled: false, mode: "off", blocked_keywords: [], model_filter: { mode: "all", models: [] }, pre_hash_check_enabled: true, block_status: 403, block_message: "request blocked by the configured risk-control policy", event_retention_days: 30, max_events: 500, audit: defaultAudit, system_prompts: [defaultPrompt] };

function lines(value: string): string[] { return [...new Set(value.split(/\r?\n|,/).map((item) => item.trim()).filter(Boolean))]; }
function stringList(value: unknown): string[] { return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : []; }
function formatTime(value: string, formatDateTime: (value: string) => string): string { if (!value) return "-"; try { return formatDateTime(value); } catch { return value; } }
function mergeConfig(next: RiskControlConfig | null | undefined): RiskControlConfig { const candidate = next ?? defaultConfig; const prompts = Array.isArray(candidate.system_prompts) && candidate.system_prompts.length ? candidate.system_prompts : [defaultPrompt]; return { ...defaultConfig, ...candidate, blocked_keywords: stringList(candidate.blocked_keywords), model_filter: { ...defaultConfig.model_filter, ...(candidate.model_filter ?? {}), models: stringList(candidate.model_filter?.models) }, audit: { ...defaultAudit, ...(candidate.audit ?? {}), scanners: stringList(candidate.audit?.scanners), api_key: "" }, system_prompts: prompts.map((prompt) => ({ ...prompt, id: prompt.id ?? "", name: prompt.name ?? "", system_prompt: prompt.system_prompt ?? "", builtin: Boolean(prompt.builtin) })) }; }
function mergeSnapshot(next: RiskControlSnapshot): RiskControlSnapshot { return { ...next, config: mergeConfig(next?.config), events: Array.isArray(next?.events) ? next.events : [] }; }
const actionKeys: Record<string, string> = { keyword_observe: "ui.risk_action_keyword_observe", keyword_block: "ui.risk_action_keyword_block", hash_observe: "ui.risk_action_hash_observe", hash_block: "ui.risk_action_hash_block", audit_observe: "ui.risk_action_audit_observe", audit_block: "ui.risk_action_audit_block", error_block: "ui.risk_action_error_block", pass: "ui.risk_action_audit_pass" };

function AuditStatus({ status, formatNumber, tx }: { status?: RiskAuditModuleStatus; formatNumber: (value: number) => string; tx: (key: any, values?: any) => string }) { if (!status) return <div className="risk-audit-status muted">{tx("ui.risk_audit_status_unavailable")}</div>; return <div className="risk-audit-status" role="status"><div><span>{tx("ui.risk_audit_active")}</span><strong>{status.active ? tx("ui.enabled") : tx("ui.disabled")}</strong></div><div><span>{tx("ui.risk_audit_queue")}</span><strong>{formatNumber(status.queue_length)} / {formatNumber(status.queue_capacity)}</strong></div><div><span>{tx("ui.risk_audit_processed")}</span><strong>{formatNumber(status.processed)}</strong></div><div><span>{tx("ui.risk_audit_blocked")}</span><strong>{formatNumber(status.blocked)}</strong></div><div><span>{tx("ui.risk_audit_errors")}</span><strong>{formatNumber(status.errors)}</strong></div><div><span>{tx("ui.risk_audit_api_key")}</span><strong>{status.api_key_available ? tx("ui.risk_audit_api_key_ready") : status.api_key_configured ? tx("ui.risk_audit_api_key_unavailable") : tx("ui.risk_audit_api_key_unset")}</strong></div></div>; }
function Toggle({ label, value, onChange }: { label: string; value: boolean; onChange: () => void }) { return <label className="field-block risk-toggle-field"><span>{label}</span><button type="button" className={`toggle-switch ${value ? "on" : ""}`} role="switch" aria-label={label} aria-checked={value} onClick={onChange}><span /></button></label>; }

function accountLabel(account: Account): string { return account.email || account.name || account.label || account.id; }
function providerLabel(kind: string, entry: AIProviderChannelEntry): string { return `${entry.name || entry.base_url || kind} #${entry.index + 1}`; }
function providerAuthIndex(entry: AIProviderChannelEntry): string { return (entry.auth_index || entry.account_id || "").trim(); }
function providerModels(entry: AIProviderChannelEntry): AIProviderChannelModel[] { return Array.isArray(entry.models) ? entry.models.filter((model) => typeof model?.name === "string" && model.name.trim()) : []; }

interface AuditFormProps {
  value: RiskAuditConfig & { prompt_options?: RiskSystemPrompt[] };
  status?: RiskAuditModuleStatus;
  accounts: Account[];
  providers: Array<{ kind: string; entry: AIProviderChannelEntry }>;
  accountModels: AccountModelCatalogs;
  accountModelsLoading: boolean;
  onChange: (value: RiskAuditConfig) => void;
  tx: (key: any, values?: any) => string;
  formatNumber: (value: number) => string;
}

function AuditForm({ value, status, accounts, providers, accountModels, accountModelsLoading, onChange, tx, formatNumber }: AuditFormProps) {
  const source = value.model_source ?? "external";
  const update = <K extends keyof RiskAuditConfig>(key: K, next: RiskAuditConfig[K]) => onChange({ ...value, [key]: next });
  const updateSource = (next: RiskAuditModelSource) => {
    const nextValue: RiskAuditConfig = { ...value, model_source: next, endpoint: next === "external" ? value.endpoint : "", api_key: next === "external" ? value.api_key : "", api_key_clear: false };
    if (next === "account") nextValue.provider_auth_index = "", nextValue.provider_name = "";
    if (next === "ai_provider") nextValue.account_id = "";
    onChange(nextValue);
  };
  const selectAccount = (accountID: string) => {
    const models = accountModels[accountID] ?? [];
    onChange({ ...value, account_id: accountID, model: models.some((model) => model.id === value.model) ? value.model : models[0]?.id ?? "", model_source: "account", endpoint: "", api_key: "", api_key_clear: false });
  };
  const selectProvider = (identity: string) => {
    const selected = providers.find(({ entry }) => providerAuthIndex(entry) === identity);
    const models = selected ? providerModels(selected.entry) : [];
    onChange({ ...value, provider_auth_index: identity, provider_name: selected?.kind ?? value.provider_name ?? "", model: models.some((model) => model.name === value.model) ? value.model : models[0]?.name ?? "", model_source: "ai_provider", endpoint: "", api_key: "", api_key_clear: false });
  };
  const selectedProvider = providers.find(({ entry }) => providerAuthIndex(entry) === value.provider_auth_index);
  const modelOptions = source === "account" ? accountModels[value.account_id ?? ""] ?? [] : selectedProvider ? providerModels(selectedProvider.entry) : [];
  const native = source !== "external";
  return <div className="risk-audit-panel">
    <div className="risk-audit-intro"><div><h3>{tx("ui.risk_audit_title")}</h3><p>{tx("ui.risk_audit_description")}</p></div><AuditStatus status={status} formatNumber={formatNumber} tx={tx} /></div>
    <div className="risk-form-grid">
      <Toggle label={tx("ui.risk_audit_enabled")} value={value.enabled} onChange={() => update("enabled", !value.enabled)} />
      <label className="field-block"><span>{tx("ui.risk_audit_mode")}</span><select aria-label={tx("ui.risk_audit_mode")} value={value.mode} onChange={(event) => update("mode", event.target.value as RiskControlMode)}><option value="off">{tx("ui.risk_mode_off")}</option><option value="observe">{tx("ui.risk_mode_observe")}</option><option value="pre_block">{tx("ui.risk_mode_pre_block")}</option></select></label>
      <label className="field-block"><span>{tx("ui.risk_audit_model_source")}</span><select aria-label={tx("ui.risk_audit_model_source")} value={source} onChange={(event) => updateSource(event.target.value as RiskAuditModelSource)}><option value="external">{tx("ui.risk_audit_source_external")}</option><option value="account">{tx("ui.risk_audit_source_account")}</option><option value="ai_provider">{tx("ui.risk_audit_source_provider")}</option></select></label>
      {source === "account" ? <label className="field-block"><span>{tx("ui.risk_audit_account")}</span><select aria-label={tx("ui.risk_audit_account")} value={value.account_id ?? ""} onChange={(event) => selectAccount(event.target.value)}><option value="">{tx("ui.risk_audit_select_account")}</option>{accounts.filter((account) => !account.disabled && !account.unavailable).map((account) => <option key={account.id} value={account.id}>{accountLabel(account)}</option>)}</select></label> : null}
      {source === "ai_provider" ? <label className="field-block"><span>{tx("ui.risk_audit_provider")}</span><select aria-label={tx("ui.risk_audit_provider")} value={value.provider_auth_index ?? ""} onChange={(event) => selectProvider(event.target.value)}><option value="">{tx("ui.risk_audit_select_provider")}</option>{providers.filter(({ entry }) => !entry.disabled && providerAuthIndex(entry)).map(({ kind, entry }) => <option key={`${kind}:${providerAuthIndex(entry)}`} value={providerAuthIndex(entry)}>{providerLabel(kind, entry)}</option>)}</select></label> : null}
      {native ? <label className="field-block risk-wide"><span>{tx("ui.risk_audit_model")}</span><select aria-label={tx("ui.risk_audit_model")} value={value.model} onChange={(event) => update("model", event.target.value)} disabled={source === "account" ? accountModelsLoading || !value.account_id : !selectedProvider}>{accountModelsLoading && source === "account" ? <option value="">{tx("ui.risk_audit_loading_models")}</option> : <option value="">{tx("ui.risk_audit_select_model")}</option>}{modelOptions.map((model) => <option key={source === "account" ? (model as AccountModelOption).id : (model as AIProviderChannelModel).name} value={source === "account" ? (model as AccountModelOption).id : (model as AIProviderChannelModel).name}>{source === "account" ? (model as AccountModelOption).display_name || (model as AccountModelOption).id : (model as AIProviderChannelModel).display_name || (model as AIProviderChannelModel).alias || (model as AIProviderChannelModel).name}</option>)}</select><small>{tx("ui.risk_audit_native_credential_hint")}</small></label> : null}
      {source === "external" ? <><label className="field-block risk-wide"><span>{tx("ui.risk_audit_endpoint")}</span><input aria-label={tx("ui.risk_audit_endpoint")} value={value.endpoint} onChange={(event) => update("endpoint", event.target.value)} placeholder="https://moderation.example/v1/chat/completions" /></label><label className="field-block"><span>{tx("ui.risk_audit_model")}</span><input aria-label={tx("ui.risk_audit_model")} value={value.model} onChange={(event) => update("model", event.target.value)} placeholder="deepseek-v4-flash" /></label><label className="field-block"><span>{tx("ui.risk_audit_api_key")}</span><input type="password" autoComplete="new-password" aria-label={tx("ui.risk_audit_api_key")} value={value.api_key} onChange={(event) => update("api_key", event.target.value)} placeholder={value.api_key_set ? tx("ui.risk_audit_api_key_placeholder") : "sk-..."} /><small>{value.api_key_set ? tx("ui.risk_audit_api_key_configured_hint") : tx("ui.risk_audit_api_key_hint")}</small>{value.api_key_set ? <button className="button subtle-danger" type="button" onClick={() => onChange({ ...value, api_key: "", api_key_clear: true })}><Trash2 size={14} />{tx("ui.risk_audit_api_key_clear")}</button> : null}</label></> : null}
      <label className="field-block"><span>{tx("ui.risk_custom_confidence")}</span><input type="number" min={0} max={1} step={0.05} value={value.confidence_threshold} onChange={(event) => update("confidence_threshold", Number(event.target.value))} /></label>
      <label className="field-block"><span>{tx("ui.risk_current_prompt")}</span><select aria-label={tx("ui.risk_current_prompt")} value={value.prompt_id} onChange={(event) => update("prompt_id", event.target.value)}>{(value.prompt_options ?? []).map((prompt: RiskSystemPrompt) => <option key={prompt.id} value={prompt.id}>{prompt.name}</option>)}</select></label>
      <label className="field-block risk-wide"><span>{tx("ui.risk_audit_scanners")}</span><textarea aria-label={tx("ui.risk_audit_scanners")} rows={2} value={(value.scanners ?? []).join("\n")} onChange={(event) => update("scanners", lines(event.target.value))} placeholder={tx("ui.risk_audit_scanners_placeholder")} /></label>
      <Toggle label={tx("ui.risk_audit_latest_turn_only")} value={value.latest_turn_only} onChange={() => update("latest_turn_only", !value.latest_turn_only)} /><Toggle label={tx("ui.risk_audit_store_pass_events")} value={value.store_pass_events} onChange={() => update("store_pass_events", !value.store_pass_events)} />
      <label className="field-block"><span>{tx("ui.risk_audit_timeout")}</span><input type="number" min={250} max={30000} value={value.timeout_ms} onChange={(event) => update("timeout_ms", Number(event.target.value))} /></label><label className="field-block"><span>{tx("ui.risk_audit_input_limit")}</span><input type="number" min={256} max={65536} value={value.input_limit} onChange={(event) => update("input_limit", Number(event.target.value))} /></label><label className="field-block"><span>{tx("ui.risk_audit_workers")}</span><input type="number" min={1} max={4} value={value.worker_count} onChange={(event) => update("worker_count", Number(event.target.value))} /></label><label className="field-block"><span>{tx("ui.risk_audit_queue_capacity")}</span><input type="number" min={1} max={256} value={value.queue_capacity} onChange={(event) => update("queue_capacity", Number(event.target.value))} /></label><label className="field-block"><span>{tx("ui.risk_audit_failure_policy")}</span><select value={value.failure_policy} onChange={(event) => update("failure_policy", event.target.value as RiskAuditFailurePolicy)}><option value="fail_open">{tx("ui.risk_audit_fail_open")}</option><option value="fail_closed">{tx("ui.risk_audit_fail_closed")}</option></select></label><label className="field-block"><span>{tx("ui.risk_block_status")}</span><input type="number" min={400} max={499} value={value.block_status} onChange={(event) => update("block_status", Number(event.target.value))} /></label><label className="field-block risk-wide"><span>{tx("ui.risk_block_message")}</span><input value={value.block_message} onChange={(event) => update("block_message", event.target.value)} maxLength={240} /></label>
    </div>
  </div>;
}

function PromptCatalog({ config, onChange, tx }: { config: RiskControlConfig; onChange: (prompts: RiskSystemPrompt[]) => void; tx: (key: any, values?: any) => string }) {
  const [selectedID, setSelectedID] = useState(config.system_prompts[0]?.id ?? "");
  const selected = config.system_prompts.find((prompt) => prompt.id === selectedID) ?? config.system_prompts[0];

  useEffect(() => {
    if (!config.system_prompts.some((prompt) => prompt.id === selectedID)) {
      setSelectedID(config.system_prompts[0]?.id ?? "");
    }
  }, [config.system_prompts, selectedID]);

  const edit = (patch: Partial<RiskSystemPrompt>) => {
    if (!selected || selected.builtin) return;
    onChange(config.system_prompts.map((prompt) => prompt.id === selected.id ? { ...prompt, ...patch } : prompt));
  };

  const add = () => {
    const id = "custom-" + Date.now();
    const next = [...config.system_prompts, { id, name: tx("ui.risk_new_prompt"), system_prompt: "", builtin: false }];
    onChange(next);
    setSelectedID(id);
  };

  const remove = () => {
    if (!selected || selected.builtin) return;
    onChange(config.system_prompts.filter((prompt) => prompt.id !== selected.id));
    setSelectedID(config.system_prompts.find((prompt) => prompt.id !== selected.id)?.id ?? "");
  };

  return <div className="risk-prompt-catalog">
    <div className="risk-catalog-toolbar">
      <label className="field-block">
        <span>{tx("ui.risk_prompt_list")}</span>
        <select aria-label={tx("ui.risk_prompt_list")} value={selected?.id ?? ""} onChange={(event) => setSelectedID(event.target.value)}>
          {config.system_prompts.map((prompt) => <option key={prompt.id} value={prompt.id}>{prompt.name}{prompt.builtin ? " (" + tx("ui.risk_prompt_default") + ")" : ""}</option>)}
        </select>
      </label>
      <button className="button" type="button" onClick={add}><Plus size={15} />{tx("ui.risk_prompt_add")}</button>
      <button className="button subtle-danger" type="button" disabled={!selected || selected.builtin} onClick={remove}><Trash2 size={15} />{tx("ui.risk_prompt_delete")}</button>
    </div>
    {selected ? <div className="risk-form-grid">
      <label className="field-block"><span>{tx("ui.risk_prompt_name")}</span><input value={selected.name} disabled={selected.builtin} onChange={(event) => edit({ name: event.target.value })} /></label>
      <label className="field-block risk-wide"><span>{tx("ui.risk_prompt_content")}</span><textarea rows={10} value={selected.system_prompt} disabled={selected.builtin} onChange={(event) => edit({ system_prompt: event.target.value })} /></label>
      {selected.builtin ? <small>{tx("ui.risk_prompt_default_locked")}</small> : null}
    </div> : null}
  </div>;
}

export function RiskControlWorkspace({ onAPIError, onNotice }: RiskControlWorkspaceProps) {
  const { tx, formatDateTime, formatNumber } = useI18n();
  const [snapshot, setSnapshot] = useState<RiskControlSnapshot | null>(null);
  const [config, setConfig] = useState<RiskControlConfig>(defaultConfig);
  const [activeTab, setActiveTab] = useState<RiskTab>("content");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [busyAction, setBusyAction] = useState<"events" | "hashes" | "">("");
  const [error, setError] = useState("");
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [providers, setProviders] = useState<Array<{ kind: string; entry: AIProviderChannelEntry }>>([]);
  const [accountModels, setAccountModels] = useState<AccountModelCatalogs>({});
  const [loadingAccountModelID, setLoadingAccountModelID] = useState("");

  const load = useCallback(async (signal?: AbortSignal) => {
    setLoading(true); setError("");
    try {
      const next = mergeSnapshot(await api.getRiskControl(signal)); setSnapshot(next); setConfig(next.config);
      const [accountResponse, providerSnapshots] = await Promise.all([
        api.listAccounts(1, 1000, {}, { field: "account", order: "asc" }, signal),
        api.listAIProviderChannels(signal),
      ]);
      if (signal?.aborted) return;
      setAccounts(accountResponse.accounts ?? []);
      setProviders(providerSnapshots.flatMap((snapshot) => (snapshot.entries ?? []).map((entry) => ({ kind: snapshot.kind, entry }))));
    } catch (caught) {
      if (!signal?.aborted) { setError(tx("ui.risk_control_load_failed")); onAPIError(caught); }
    } finally { if (!signal?.aborted) setLoading(false); }
  }, [onAPIError, tx]);

  useEffect(() => { const controller = new AbortController(); void load(controller.signal); return () => controller.abort(); }, [load]);
  useEffect(() => {
    const accountID = config.audit.account_id?.trim() ?? "";
    if ((config.audit.model_source ?? "external") !== "account" || !accountID || accountModels[accountID]) {
      setLoadingAccountModelID("");
      return;
    }
    const controller = new AbortController();
    setLoadingAccountModelID(accountID);
    void api.loadAccountModels({ mode: "selected", ids: [accountID] }).then((catalog) => {
      if (!controller.signal.aborted) setAccountModels((current) => ({ ...current, [accountID]: catalog.models ?? [] }));
    }).catch(() => undefined).finally(() => {
      if (!controller.signal.aborted) setLoadingAccountModelID("");
    });
    return () => controller.abort();
  }, [config.audit.account_id, config.audit.model_source]);

  const status = snapshot?.status; const events = snapshot?.events ?? [];
  const update = <K extends keyof RiskControlConfig>(key: K, value: RiskControlConfig[K]) => setConfig((current) => ({ ...current, [key]: value }));
  const save = async () => {
    setSaving(true); setError("");
    try {
      const next = await api.saveRiskControl({ ...config, blocked_keywords: lines((config.blocked_keywords ?? []).join("\n")), model_filter: { mode: config.model_filter.mode, models: lines((config.model_filter.models ?? []).join("\n")) }, block_status: Math.round(Number(config.block_status) || 403), event_retention_days: Math.round(Number(config.event_retention_days) || 30), max_events: Math.round(Number(config.max_events) || 500), audit: { ...config.audit, scanners: lines((config.audit.scanners ?? []).join("\n")), api_key: (config.audit.model_source ?? "external") === "external" ? config.audit.api_key : "", endpoint: (config.audit.model_source ?? "external") === "external" ? config.audit.endpoint : "" } });
      const normalized = mergeSnapshot(next); setSnapshot(normalized); setConfig(normalized.config); onNotice(tx("ui.risk_control_saved"));
    } catch (caught) { setError(tx("ui.risk_control_save_failed")); onAPIError(caught); } finally { setSaving(false); }
  };
  const clear = async (kind: "events" | "hashes") => { setBusyAction(kind); setError(""); try { const next = kind === "events" ? await api.clearRiskControlEvents() : await api.clearRiskControlHashes(); const normalized = mergeSnapshot(next); setSnapshot(normalized); setConfig(normalized.config); onNotice(tx(kind === "events" ? "ui.risk_control_events_cleared" : "ui.risk_control_hashes_cleared")); } catch (caught) { setError(tx("ui.risk_control_clear_failed")); onAPIError(caught); } finally { setBusyAction(""); } };
  const auditWithOptions = useMemo(() => ({ ...config.audit, prompt_options: config.system_prompts } as RiskAuditConfig & { prompt_options: RiskSystemPrompt[] }), [config.audit, config.system_prompts]);
  return <section className="risk-control-workspace" aria-label={tx("ui.risk_control")}><div className="risk-control-header"><div><div className="eyebrow"><ShieldAlert size={15} />{tx("ui.risk_control")}</div><h2>{tx("ui.risk_control_title")}</h2><p>{tx("ui.risk_control_description")}</p></div><button className="button" type="button" onClick={() => void load()} disabled={loading}><RefreshCw size={15} className={loading ? "spin" : ""} />{tx("ui.refresh")}</button></div>{error ? <div className="inline-error" role="alert"><AlertTriangle size={16} />{error}</div> : null}{snapshot?.storage_error ? <div className="inline-warning" role="status"><Database size={16} />{snapshot.storage_error}</div> : null}<div className="risk-card risk-config-card"><div className="risk-tabs" role="tablist"><button type="button" role="tab" aria-selected={activeTab === "content"} className={activeTab === "content" ? "active" : ""} onClick={() => setActiveTab("content")}>{tx("ui.risk_content_moderation")}</button><button type="button" role="tab" aria-selected={activeTab === "audit"} className={activeTab === "audit" ? "active" : ""} onClick={() => setActiveTab("audit")}>{tx("ui.risk_audit")}</button></div>{activeTab === "content" ? <div role="tabpanel" aria-label={tx("ui.risk_content_moderation")}><div className="risk-form-grid"><Toggle label={tx("ui.risk_enabled")} value={config.enabled} onChange={() => update("enabled", !config.enabled)} /><label className="field-block"><span>{tx("ui.risk_mode")}</span><select value={config.mode} onChange={(event) => update("mode", event.target.value as RiskControlMode)}><option value="off">{tx("ui.risk_mode_off")}</option><option value="observe">{tx("ui.risk_mode_observe")}</option><option value="pre_block">{tx("ui.risk_mode_pre_block")}</option></select></label><label className="field-block risk-wide"><span>{tx("ui.risk_blocked_keywords")}</span><textarea rows={3} value={(config.blocked_keywords ?? []).join("\n")} onChange={(event) => update("blocked_keywords", lines(event.target.value))} placeholder={tx("ui.risk_keywords_placeholder")} /></label><label className="field-block"><span>{tx("ui.risk_model_filter")}</span><select value={config.model_filter.mode} onChange={(event) => update("model_filter", { ...config.model_filter, mode: event.target.value as RiskControlConfig["model_filter"]["mode"] })}><option value="all">{tx("ui.risk_model_filter_all")}</option><option value="include">{tx("ui.risk_model_filter_include")}</option><option value="exclude">{tx("ui.risk_model_filter_exclude")}</option></select></label><label className="field-block"><span>{tx("ui.risk_model_list")}</span><textarea rows={2} value={(config.model_filter.models ?? []).join("\n")} onChange={(event) => update("model_filter", { ...config.model_filter, models: lines(event.target.value) })} disabled={config.model_filter.mode === "all"} /></label><Toggle label={tx("ui.risk_hash_check")} value={config.pre_hash_check_enabled} onChange={() => update("pre_hash_check_enabled", !config.pre_hash_check_enabled)} /><label className="field-block"><span>{tx("ui.risk_block_status")}</span><input type="number" min={400} max={499} value={config.block_status} onChange={(event) => update("block_status", Number(event.target.value))} /></label><label className="field-block risk-wide"><span>{tx("ui.risk_block_message")}</span><input value={config.block_message} onChange={(event) => update("block_message", event.target.value)} /></label><label className="field-block"><span>{tx("ui.risk_retention_days")}</span><input type="number" min={1} max={365} value={config.event_retention_days} onChange={(event) => update("event_retention_days", Number(event.target.value))} /></label><label className="field-block"><span>{tx("ui.risk_max_events")}</span><input type="number" min={1} max={2000} value={config.max_events} onChange={(event) => update("max_events", Number(event.target.value))} /></label></div></div> : <div role="tabpanel" aria-label={tx("ui.risk_audit")}><AuditForm value={auditWithOptions} status={status?.audit} accounts={accounts} providers={providers} accountModels={accountModels} accountModelsLoading={loadingAccountModelID === (config.audit.account_id?.trim() ?? "")} onChange={(next) => update("audit", next)} tx={tx} formatNumber={formatNumber} /><PromptCatalog config={config} onChange={(prompts) => update("system_prompts", prompts)} tx={tx} /></div>}<div className="risk-card-actions"><button className="button primary" type="button" disabled={saving || loading} onClick={() => void save()}>{saving ? <RefreshCw className="spin" size={16} /> : <Save size={16} />}{tx("ui.save")}</button></div></div><div className="risk-control-layout"><section className="risk-card risk-safety-card"><div className="risk-card-heading"><div><h3>{tx("ui.risk_safety_boundary")}</h3><p>{tx("ui.risk_safety_boundary_description")}</p></div><CheckCircle2 size={20} /></div><ul className="risk-safety-list"><li>{tx("ui.risk_safe_point_1")}</li><li>{tx("ui.risk_safe_point_2")}</li><li>{tx("ui.risk_safe_point_3")}</li></ul><div className="risk-danger-zone"><div><strong>{tx("ui.risk_memory_management")}</strong><span>{tx("ui.risk_memory_management_description")}</span></div><div className="risk-card-actions"><button className="button subtle-danger" type="button" disabled={busyAction !== ""} onClick={() => void clear("hashes")}><XCircle size={15} />{tx("ui.risk_clear_hashes")}</button><button className="button subtle-danger" type="button" disabled={busyAction !== ""} onClick={() => void clear("events")}><Trash2 size={15} />{tx("ui.risk_clear_events")}</button></div></div></section></div><section className="risk-card risk-events-card"><div className="risk-card-heading"><div><h3>{tx("ui.risk_event_history")}</h3><p>{tx("ui.risk_event_history_description")}</p></div><span className="risk-event-count">{formatNumber(events.length)}</span></div><div className="risk-events-table-wrap"><table className="risk-events-table"><thead><tr><th>{tx("ui.risk_event_time")}</th><th>{tx("ui.risk_event_action")}</th><th>{tx("ui.risk_event_source")}</th><th>{tx("ui.risk_event_model")}</th><th>{tx("ui.risk_event_rule")}</th><th>{tx("ui.risk_event_hash")}</th><th>{tx("ui.risk_event_latency")}</th></tr></thead><tbody>{events.map((event: RiskControlEvent) => <tr key={event.id}><td>{formatTime(event.time, formatDateTime)}</td><td><span className={`risk-action ${event.action.includes("block") ? "blocked" : "observed"}`}>{event.action.includes("block") ? <XCircle size={13} /> : <CheckCircle2 size={13} />}{tx((actionKeys[event.action] ?? event.action) as any)}</span></td><td><code>{event.account_ref || event.provider || "-"}</code></td><td>{event.model || "-"}<small>{event.format || ""}</small></td><td><code>{event.matched_rules?.join(", ") || event.reason_code || "-"}</code></td><td><code>{event.input_hash ? `${event.input_hash.slice(0, 12)}…` : "-"}</code></td><td>{formatNumber(event.latency_ms)} ms</td></tr>)}</tbody></table>{!loading && events.length === 0 ? <div className="empty-state" role="status">{tx("ui.risk_no_events")}</div> : null}</div></section></section>;
}

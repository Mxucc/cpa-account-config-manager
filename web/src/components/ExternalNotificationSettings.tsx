import { BellRing, Eye, LoaderCircle, Plus, Save, Send, Trash2 } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import * as api from "../api/client";
import { operatorMessage } from "../format/operatorMessage";
import { useI18n } from "../i18n";
import type { UIMessageKey } from "../i18n/uiText";
import type {
  InspectionNotificationEndpoint,
  InspectionNotificationPreview,
  InspectionNotificationRequest,
  InspectionNotificationTestResult,
  InspectionPolicy,
} from "../types";

interface ExternalNotificationSettingsProps {
  refreshRevision: number;
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
}

type NotificationResult = {
  preview: InspectionNotificationPreview;
  test?: InspectionNotificationTestResult;
};

const maxEndpoints = 20;
const detailsPreset = "__notification_details__";

const notificationVariables: Array<{ name: string; label: UIMessageKey }> = [
  { name: "event", label: "ui.notification_parameter_event" },
  { name: "total_accounts", label: "ui.notification_parameter_total_accounts" },
  { name: "eligible_accounts", label: "ui.notification_parameter_eligible_accounts" },
  { name: "available_accounts", label: "ui.notification_parameter_available_accounts" },
  { name: "available_percent", label: "ui.notification_parameter_available_percent" },
  { name: "abnormal_accounts", label: "ui.notification_parameter_abnormal_accounts" },
  { name: "abnormal_percent", label: "ui.notification_parameter_abnormal_percent" },
  { name: "quota_limited_accounts", label: "ui.notification_parameter_quota_limited_accounts" },
  { name: "invalid_credential_accounts", label: "ui.notification_parameter_invalid_credential_accounts" },
  { name: "deactivated_accounts", label: "ui.notification_parameter_deactivated_accounts" },
  { name: "unavailable_accounts", label: "ui.notification_parameter_unavailable_accounts" },
  { name: "disabled_accounts", label: "ui.notification_parameter_disabled_accounts" },
  { name: "threshold_percent", label: "ui.notification_parameter_threshold_percent" },
  { name: "available_accounts_threshold", label: "ui.notification_parameter_available_accounts_threshold" },
  { name: "availability_percent_threshold", label: "ui.notification_parameter_availability_percent_threshold" },
  { name: "triggered_at", label: "ui.notification_parameter_triggered_at" },
];

function normalizedEndpoints(policy: InspectionPolicy): InspectionNotificationEndpoint[] {
  if (Array.isArray(policy.notification_endpoints) && policy.notification_endpoints.length > 0) {
    return policy.notification_endpoints.map((endpoint) => ({ ...endpoint }));
  }
  const legacy = policy.anomaly_notification_url?.trim();
  return legacy ? [{ id: "legacy", name: "", url: legacy, enabled: true }] : [];
}

export function ExternalNotificationSettings({ refreshRevision, onAPIError, onNotice }: ExternalNotificationSettingsProps) {
  const { locale, tx, formatDateTime } = useI18n();
  const [policy, setPolicy] = useState<InspectionPolicy | null>(null);
  const [endpoints, setEndpoints] = useState<InspectionNotificationEndpoint[]>([]);
  const [anomalyEnabled, setAnomalyEnabled] = useState(false);
  const [notificationOnly, setNotificationOnly] = useState(false);
  const [availableEnabled, setAvailableEnabled] = useState(false);
  const [availableThreshold, setAvailableThreshold] = useState("10");
  const [percentEnabled, setPercentEnabled] = useState(false);
  const [percentThreshold, setPercentThreshold] = useState("20");
  const [cooldown, setCooldown] = useState("60");
  const [results, setResults] = useState<Record<string, NotificationResult>>({});
  const [action, setAction] = useState<{ endpointID: string; kind: "preview" | "test" } | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const endpointCounter = useRef(0);
  const inputRefs = useRef(new Map<string, HTMLInputElement>());

  const handleError = useCallback((caught: unknown) => {
    if (caught instanceof api.APIError && caught.status === 401) {
      onAPIError(caught);
      return;
    }
    setError(operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale));
  }, [locale, onAPIError, tx]);

  const applyPolicy = useCallback((next: InspectionPolicy) => {
    setPolicy(next);
    setEndpoints(normalizedEndpoints(next));
    setAnomalyEnabled(next.anomaly_notification_enabled === true);
    setNotificationOnly(next.anomaly_notification_only === true);
    setAvailableEnabled(next.notification_available_accounts_enabled === true);
    setAvailableThreshold(String(next.notification_available_accounts_threshold || 10));
    setPercentEnabled(next.notification_availability_percent_enabled === true);
    setPercentThreshold(String(next.notification_availability_percent_threshold || 20));
    setCooldown(String(next.notification_cooldown_minutes || 60));
    setResults({});
  }, []);

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const snapshot = await api.getInspection();
      if (!snapshot?.policy) throw new Error(tx("ui.policy_unavailable"));
      applyPolicy(snapshot.policy);
    } catch (caught) {
      handleError(caught);
    } finally {
      setLoading(false);
    }
  }, [applyPolicy, handleError]);

  useEffect(() => { void load(); }, [load, refreshRevision]);

  const updateEndpoint = (id: string, patch: Partial<InspectionNotificationEndpoint>) => {
    setEndpoints((current) => current.map((endpoint) => endpoint.id === id ? { ...endpoint, ...patch } : endpoint));
    setResults((current) => {
      const next = { ...current };
      delete next[id];
      return next;
    });
  };

  const addEndpoint = () => {
    if (endpoints.length >= maxEndpoints) {
      setError(tx("ui.notification_endpoint_limit_reached", { count: maxEndpoints }));
      return;
    }
    endpointCounter.current += 1;
    const id = `notification-${Date.now().toString(36)}-${endpointCounter.current}`;
    setEndpoints((current) => [...current, { id, name: "", url: "", enabled: true }]);
    setError("");
  };

  const removeEndpoint = (endpoint: InspectionNotificationEndpoint) => {
    if (!window.confirm(tx("ui.confirm_remove_notification_endpoint", { name: endpoint.name || endpoint.id }))) return;
    setEndpoints((current) => current.filter((candidate) => candidate.id !== endpoint.id));
    setResults((current) => {
      const next = { ...current };
      delete next[endpoint.id];
      return next;
    });
  };

  const insertVariable = (endpoint: InspectionNotificationEndpoint, name: string) => {
    if (!name) return;
    const insertion = name === detailsPreset ? tx("ui.notification_full_message_template") : `\${${name}}`;
    const input = inputRefs.current.get(endpoint.id);
    const start = input?.selectionStart ?? endpoint.url.length;
    const end = input?.selectionEnd ?? start;
    updateEndpoint(endpoint.id, { url: `${endpoint.url.slice(0, start)}${insertion}${endpoint.url.slice(end)}` });
    window.setTimeout(() => {
      const cursor = start + insertion.length;
      input?.focus();
      input?.setSelectionRange(cursor, cursor);
    }, 0);
  };

  const numericSettings = () => {
    const available = Number(availableThreshold);
    const percent = Number(percentThreshold);
    const cooldownMinutes = Number(cooldown);
    if (!Number.isInteger(available) || available < 1 || available > 10000) throw new Error(tx("ui.notification_available_accounts_must_be_between_1_and_10000"));
    if (!Number.isInteger(percent) || percent < 1 || percent > 100) throw new Error(tx("ui.notification_availability_percent_must_be_between_1_and_100"));
    if (!Number.isInteger(cooldownMinutes) || cooldownMinutes < 5 || cooldownMinutes > 1440) throw new Error(tx("ui.notification_cooldown_must_be_between_5_and_1440_minutes"));
    return { available, percent, cooldownMinutes };
  };

  const validateEndpoints = () => {
    if (endpoints.length > maxEndpoints) throw new Error(tx("ui.notification_endpoint_limit_reached", { count: maxEndpoints }));
    const urls = new Set<string>();
    for (const endpoint of endpoints) {
      const url = endpoint.url.trim();
      if (!url) throw new Error(tx("ui.notification_url_is_required"));
      if (!url.toLowerCase().startsWith("https://")) throw new Error(tx("ui.notification_url_must_use_https"));
      if (urls.has(url)) throw new Error(tx("ui.notification_duplicate_url"));
      urls.add(url);
    }
    if ((anomalyEnabled || availableEnabled || percentEnabled) && !endpoints.some((endpoint) => endpoint.enabled)) {
      throw new Error(tx("ui.notification_enabled_endpoint_required"));
    }
  };

  const buildRequest = (endpoint: InspectionNotificationEndpoint): InspectionNotificationRequest => {
    const values = numericSettings();
    if (!endpoint.url.trim()) throw new Error(tx("ui.notification_url_is_required"));
    if (!endpoint.url.trim().toLowerCase().startsWith("https://")) throw new Error(tx("ui.notification_url_must_use_https"));
    return {
      endpoint_id: endpoint.id,
      endpoint_name: endpoint.name?.trim(),
      url_template: endpoint.url.trim(),
      scenario: "manual_test",
      threshold_percent: policy?.anomaly_threshold_percent || 50,
      available_accounts_threshold: values.available,
      availability_percent_threshold: values.percent,
    };
  };

  const runEndpointAction = async (endpoint: InspectionNotificationEndpoint, kind: "preview" | "test") => {
    setError("");
    let request: InspectionNotificationRequest;
    try {
      request = buildRequest(endpoint);
    } catch (caught) {
      handleError(caught);
      return;
    }
    setAction({ endpointID: endpoint.id, kind });
    try {
      if (kind === "preview") {
        const preview = await api.previewInspectionNotification(request);
        setResults((current) => ({ ...current, [endpoint.id]: { preview } }));
      } else {
        const test = await api.testInspectionNotification(request);
        setResults((current) => ({ ...current, [endpoint.id]: { preview: test.preview, test } }));
      }
    } catch (caught) {
      handleError(caught);
    } finally {
      setAction(null);
    }
  };

  const save = async () => {
    setError("");
    let values: ReturnType<typeof numericSettings>;
    try {
      values = numericSettings();
      validateEndpoints();
    } catch (caught) {
      handleError(caught);
      return;
    }
    setSaving(true);
    try {
      const latest = (await api.getInspection()).policy;
      const notificationActive = anomalyEnabled || availableEnabled || percentEnabled;
      const next: InspectionPolicy = {
        ...latest,
        enabled: notificationActive ? true : latest.enabled,
        anomaly_trigger_enabled: anomalyEnabled ? true : latest.anomaly_trigger_enabled,
        anomaly_notification_enabled: anomalyEnabled,
        anomaly_notification_only: anomalyEnabled ? notificationOnly : false,
        anomaly_notification_url: "",
        notification_endpoints: endpoints.map((endpoint) => ({ ...endpoint, name: endpoint.name?.trim(), url: endpoint.url.trim() })),
        notification_available_accounts_enabled: availableEnabled,
        notification_available_accounts_threshold: values.available,
        notification_availability_percent_enabled: percentEnabled,
        notification_availability_percent_threshold: values.percent,
        notification_cooldown_minutes: values.cooldownMinutes,
      };
      const saved = await api.saveInspectionPolicy(next);
      applyPolicy(saved.policy);
      onNotice(tx("ui.notification_settings_saved"));
    } catch (caught) {
      handleError(caught);
    } finally {
      setSaving(false);
    }
  };

  const notificationActive = anomalyEnabled || availableEnabled || percentEnabled;
  return (
    <section className="external-notification-settings" role="tabpanel" aria-label={tx("ui.external_notifications")}>
      <header className="notification-settings-header">
        <BellRing size={18} />
        <div><strong>{tx("ui.external_notifications")}</strong><span>{tx("ui.external_notifications_description")}</span></div>
        <button className="button button-quiet" type="button" disabled={loading || endpoints.length >= maxEndpoints} onClick={addEndpoint}><Plus size={15} />{tx("ui.add_notification_endpoint")}</button>
      </header>

      <div className="notification-global-settings">
        <NotificationToggle label="ui.notify_on_anomaly_ratio" checked={anomalyEnabled} disabled={loading || saving} onChange={(checked) => { setAnomalyEnabled(checked); if (!checked) setNotificationOnly(false); }} />
        <NotificationToggle label="ui.notification_only_mode" checked={notificationOnly} disabled={!anomalyEnabled || loading || saving} onChange={setNotificationOnly} />
        <NotificationToggle label="ui.notify_when_available_accounts_low" checked={availableEnabled} disabled={loading || saving} onChange={setAvailableEnabled} />
        <NotificationNumber label="ui.available_accounts_threshold" suffix="ui.accounts_2" value={availableThreshold} min={1} max={10000} disabled={!availableEnabled || loading || saving} onChange={setAvailableThreshold} />
        <NotificationToggle label="ui.notify_when_availability_low" checked={percentEnabled} disabled={loading || saving} onChange={setPercentEnabled} />
        <NotificationNumber label="ui.availability_percent_threshold" suffix="ui.percent" value={percentThreshold} min={1} max={100} disabled={!percentEnabled || loading || saving} onChange={setPercentThreshold} />
        <NotificationNumber label="ui.notification_cooldown" suffix="ui.minutes" value={cooldown} min={5} max={1440} disabled={!notificationActive || loading || saving} onChange={setCooldown} />
      </div>

      {error ? <div className="automation-error notification-settings-error" role="alert"><span>{error}</span><button type="button" onClick={() => setError("")}>{tx("ui.close")}</button></div> : null}

      <div className="notification-endpoint-list" aria-label={tx("ui.notification_endpoints")}>
        {endpoints.map((endpoint, index) => {
          const result = results[endpoint.id];
          const busy = action?.endpointID === endpoint.id;
          return (
            <section className="notification-endpoint-row" key={endpoint.id} aria-label={tx("ui.notification_endpoint_number", { number: index + 1 })}>
              <div className="notification-endpoint-heading">
                <label className="switch-control"><input type="checkbox" checked={endpoint.enabled} disabled={saving} onChange={(event) => updateEndpoint(endpoint.id, { enabled: event.target.checked })} aria-label={tx("ui.notification_endpoint_enabled_number", { number: index + 1 })} /><b>{tx(endpoint.enabled ? "ui.on_2" : "ui.off_2")}</b></label>
                <label><span>{tx("ui.notification_endpoint_name")}</span><input type="text" maxLength={80} value={endpoint.name || ""} disabled={saving} onChange={(event) => updateEndpoint(endpoint.id, { name: event.target.value })} aria-label={tx("ui.notification_endpoint_name_number", { number: index + 1 })} /></label>
                <button className="icon-button" type="button" title={tx("ui.remove_notification_endpoint")} aria-label={tx("ui.remove_notification_endpoint_number", { number: index + 1 })} disabled={saving} onClick={() => removeEndpoint(endpoint)}><Trash2 size={15} /></button>
              </div>
              <div className="notification-template-editor">
                <label className="notification-template-field"><span>{tx("ui.notification_url_template")}</span><input ref={(node) => { if (node) inputRefs.current.set(endpoint.id, node); else inputRefs.current.delete(endpoint.id); }} type="text" maxLength={4096} value={endpoint.url} disabled={saving} onChange={(event) => updateEndpoint(endpoint.id, { url: event.target.value })} placeholder="https://notify.example/hook?available=${available_accounts}" aria-label={tx("ui.notification_endpoint_url_number", { number: index + 1 })} autoComplete="off" spellCheck={false} /></label>
                <label className="notification-variable-field"><span>{tx("ui.insert_notification_parameter")}</span><select value="" disabled={saving || !endpoint.url} onChange={(event) => insertVariable(endpoint, event.target.value)} aria-label={tx("ui.notification_endpoint_parameter_number", { number: index + 1 })}><option value="">{tx("ui.select_parameter")}</option><option value={detailsPreset}>{tx("ui.notification_parameter_full_details")}</option>{notificationVariables.map((variable) => <option key={variable.name} value={variable.name}>{tx(variable.label)} · ${"{"}{variable.name}{"}"}</option>)}</select></label>
              </div>
              <div className="notification-endpoint-actions">
                <button className="button button-quiet" type="button" disabled={saving || busy || !endpoint.url.trim()} onClick={() => void runEndpointAction(endpoint, "preview")}>{busy && action?.kind === "preview" ? <LoaderCircle className="spin" size={14} /> : <Eye size={14} />}{tx("ui.preview_notification")}</button>
                <button className="button button-primary" type="button" disabled={saving || busy || !endpoint.url.trim()} onClick={() => void runEndpointAction(endpoint, "test")}>{busy && action?.kind === "test" ? <LoaderCircle className="spin" size={14} /> : <Send size={14} />}{tx("ui.send_test_notification")}</button>
              </div>
              {result ? <NotificationResultView result={result} /> : null}
            </section>
          );
        })}
        {!loading && endpoints.length === 0 ? <div className="notification-endpoint-empty"><BellRing size={20} /><span>{tx("ui.no_notification_endpoints")}</span><button className="button button-primary" type="button" onClick={addEndpoint}><Plus size={15} />{tx("ui.add_notification_endpoint")}</button></div> : null}
        {loading ? <div className="notification-endpoint-empty"><LoaderCircle className="spin" size={20} /><span>{tx("ui.loading_policy")}</span></div> : null}
      </div>

      <div className="settings-section-actions notification-settings-save"><span>{tx("ui.notification_endpoint_count", { count: endpoints.length, max: maxEndpoints })}</span><button className="button button-primary" type="button" disabled={loading || saving || !policy} onClick={() => void save()}>{saving ? <LoaderCircle className="spin" size={15} /> : <Save size={15} />}{tx("ui.save_settings")}</button></div>
    </section>
  );

  function NotificationResultView({ result }: { result: NotificationResult }) {
    return <div className="notification-preview-result"><div className="notification-preview-meta"><span><b>{tx("ui.notification_event")}</b><code>{result.preview.event}</code></span><span><b>{tx("ui.generated_at")}</b><time>{formatDateTime(result.preview.triggered_at)}</time></span>{result.test ? <><span className={result.test.delivered ? "is-success" : "is-failed"}><b>{tx("ui.delivery_result")}</b>{tx(result.test.delivered ? "ui.notification_delivered" : "ui.notification_failed")}</span><span><b>{tx("ui.http_status")}</b><code>{result.test.status_code || "-"}</code></span><span><b>{tx("ui.attempts")}</b><code>{result.test.attempts}</code></span></> : null}</div><label className="notification-expanded-url"><span>{tx("ui.exact_get_url")}</span><code>{result.preview.expanded_url}</code></label><div className="notification-variable-values"><strong>{tx("ui.current_variable_values")}</strong><dl>{notificationVariables.map((variable) => <div key={variable.name}><dt>{tx(variable.label)}<code>${"{"}{variable.name}{"}"}</code></dt><dd>{result.preview.variables[variable.name] ?? "-"}</dd></div>)}</dl></div></div>;
  }
}

function NotificationToggle({ label, checked, disabled, onChange }: { label: UIMessageKey; checked: boolean; disabled: boolean; onChange: (checked: boolean) => void }) {
  const { tx } = useI18n();
  return <label className={`automation-setting ${checked ? "is-enabled" : ""}`}><span>{tx(label)}</span><span className="switch-control"><input type="checkbox" checked={checked} disabled={disabled} onChange={(event) => onChange(event.target.checked)} aria-label={tx(label)} /><b>{tx(checked ? "ui.on_2" : "ui.off_2")}</b></span></label>;
}

function NotificationNumber({ label, suffix, value, min, max, disabled, onChange }: { label: UIMessageKey; suffix: UIMessageKey; value: string; min: number; max: number; disabled: boolean; onChange: (value: string) => void }) {
  const { tx } = useI18n();
  return <label className="automation-setting automation-setting-number"><span>{tx(label)}</span><span className="number-suffix"><input type="number" min={min} max={max} step="1" value={value} disabled={disabled} onChange={(event) => onChange(event.target.value)} aria-label={tx(label)} /><b>{tx(suffix)}</b></span></label>;
}

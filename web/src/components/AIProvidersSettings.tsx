import { Activity, Eye, EyeOff, LoaderCircle, Plus, Power, PowerOff, RefreshCw, Save, Trash2 } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { AlertTriangle, CheckCircle2, ShieldQuestion, XCircle } from "lucide-react";
import * as api from "../api/client";
import { technicalLabel } from "../format/accountDisplay";
import { operatorMessage } from "../format/operatorMessage";
import { decodeHTMLCharacterReferences } from "../format/htmlCharacterReferences";
import { useI18n } from "../i18n";
import type { UIMessageKey } from "../i18n/uiText";
import type {
  AIProviderAPIKeyEntry,
  AIProviderChannelEntry,
  AIProviderChannelKind,
  AIProviderChannelModel,
  AIProviderChannelSnapshot,
  AIProviderRuntimeModelUsage,
  AIProviderRuntimeSnapshot,
  ProviderQuotaPolicy,
  QuotaPolicySnapshot,
} from "../types";
import { IconButton } from "./IconButton";
import { Modal } from "./Modal";

interface AIProvidersSettingsProps {
  refreshRevision: number;
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
}

type AddKind = AIProviderChannelKind;

const providerTestStatusLabels: Record<api.AIProviderProbeResult["status"] & NonNullable<api.AIProviderProbeResult["status"]>, UIMessageKey> = {
  available: "ui.model_available",
  unavailable: "ui.model_unavailable",
  unsupported: "ui.testing_unsupported",
  review: "ui.manual_confirmation_required",
};

const providerTestReasonLabels: Record<string, UIMessageKey> = {
  model_response_ok: "ui.received_the_expected_model_response",
  model_not_found: "ui.this_account_cannot_use_the_model_or_the_model_does_not_exist",
  account_unavailable: "ui.account_is_currently_unavailable",
  authentication_failed: "ui.authentication_failed_check_credential_status",
  quota_limited: "ui.upstream_quota_or_rate_limited",
  request_timeout: "ui.test_request_timed_out",
  request_failed: "ui.upstream_service_is_temporarily_unavailable",
  upstream_unavailable: "ui.upstream_service_is_temporarily_unavailable",
  invalid_response: "ui.the_upstream_response_cannot_confirm_model_availability",
  invalid_model: "ui.enter_model_id",
  unsupported_provider: "ui.this_provider_does_not_support_safe_model_testing_yet",
  transient_failure: "ui.upstream_service_is_temporarily_unavailable",
};

function ProviderTestResponse({ response }: { response: NonNullable<api.AIProviderProbeResult["response"]> }) {
  const { tx } = useI18n();
  const headers = Array.isArray(response.headers) ? response.headers : [];
  const body = response.body ? decodeHTMLCharacterReferences(response.body) : tx("ui.empty_response_body");
  return (
    <div className="model-test-response">
      <div className="model-test-response-heading">
        <div><strong>{tx("ui.upstream_response")}</strong><span>{tx("ui.sanitized_response")}</span></div>
        <span>{response.format.toUpperCase()}{response.truncated ? ` · ${tx("ui.truncated")}` : ""}</span>
      </div>
      {headers.length > 0 ? (
        <div className="model-test-response-headers" aria-label={tx("ui.response_headers")}>
          {headers.map((header) => <div key={`${header.name}:${header.value}`}><code>{header.name}</code><span>{header.value}</span></div>)}
        </div>
      ) : null}
      <pre aria-label={tx("ui.response_body")}><code>{body}</code></pre>
    </div>
  );
}

const addableKinds: Array<{ kind: AddKind; labelKey: UIMessageKey; descriptionKey: UIMessageKey }> = [
  { kind: "openai-compatibility", labelKey: "ui.ai_provider_channel_openai_compatibility", descriptionKey: "ui.ai_provider_channel_openai_compatibility_description" },
  { kind: "gemini-api-key", labelKey: "ui.ai_provider_channel_gemini", descriptionKey: "ui.ai_provider_channel_gemini_description" },
  { kind: "interactions-api-key", labelKey: "ui.ai_provider_channel_interactions", descriptionKey: "ui.ai_provider_channel_interactions_description" },
  { kind: "claude-api-key", labelKey: "ui.ai_provider_channel_claude", descriptionKey: "ui.ai_provider_channel_claude_description" },
  { kind: "codex-api-key", labelKey: "ui.ai_provider_channel_codex", descriptionKey: "ui.ai_provider_channel_codex_description" },
  { kind: "xai-api-key", labelKey: "ui.ai_provider_channel_xai", descriptionKey: "ui.ai_provider_channel_xai_description" },
  { kind: "vertex-api-key", labelKey: "ui.ai_provider_channel_vertex", descriptionKey: "ui.ai_provider_channel_vertex_description" },
  { kind: "api-keys", labelKey: "ui.ai_provider_channel_api_keys", descriptionKey: "ui.ai_provider_channel_api_keys_description" },
  { kind: "opencode-go", labelKey: "ui.ai_provider_channel_opencode", descriptionKey: "ui.opencode_login_description" },
  { kind: "opencode-zen", labelKey: "ui.ai_provider_channel_opencode_zen", descriptionKey: "ui.opencode_zen_login_description" },
];

const apiKeyChannelKinds: AddKind[] = [
  "gemini-api-key",
  "interactions-api-key",
  "claude-api-key",
  "codex-api-key",
  "xai-api-key",
  "vertex-api-key",
];

type EditingEntry = {
  kind: AIProviderChannelKind;
  index: number;
  quotaPolicyKey: string;
  name: string;
  baseURL: string;
  apiKey: string;
  disabled: boolean;
  prefix: string;
  priority: string;
  weight: string;
  proxyURL: string;
  headersText: string;
  excludedText: string;
  models: AIProviderChannelModel[];
  apiKeyEntries: Array<{ apiKey: string; weight: string; proxyURL: string; raw?: Record<string, unknown> }>;
  apiKeyEntriesDirty: boolean;
  supportPromptCacheKey: boolean;
  disableCooling: boolean;
  requestRetry: string;
  requestScopedErrorsText: string;
  alphaSearch: boolean;
  websockets: boolean;
  rebuildMidSystemMessage: boolean;
  fingerprintProfile: string;
  accountID?: string;
  workspaceID?: string;
  concurrencyLimit: string;
  fiveHourTotalTokens: string;
  fiveHourLimitPercent: string;
  sevenDayTotalTokens: string;
  sevenDayLimitPercent: string;
};

function maskSecret(value: string | undefined): string {
  const trimmed = (value ?? "").trim();
  if (!trimmed) return "";
  if (trimmed.length <= 8) return "••••••••";
  return `${trimmed.slice(0, 4)}••••${trimmed.slice(-4)}`;
}

function apiKeyEntriesForEditing(entry: AIProviderChannelEntry): Array<{ apiKey: string; weight: string; proxyURL: string; raw?: Record<string, unknown> }> {
  const entries = (entry.api_key_entries ?? [])
    .map((keyEntry) => ({
      apiKey: keyEntry.api_key ?? "",
      weight: keyEntry.weight !== undefined && keyEntry.weight !== null ? String(keyEntry.weight) : "",
      proxyURL: keyEntry.proxy_url ?? "",
      raw: { ...(keyEntry.raw ?? {}) },
    }))
    .filter((keyEntry) => keyEntry.apiKey.trim());
  if (entries.length > 0) return entries;

  // Older CPA versions persist an OpenAI-compatible key only in the legacy
  // top-level `api-key` field. Keep that key visible in the editor so saving
  // an unrelated field never silently clears credentials.
  const legacyAPIKey = (entry.api_key ?? "").trim();
  return legacyAPIKey ? [{ apiKey: legacyAPIKey, weight: "", proxyURL: "" }] : [];
}

function headersToMap(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split(/\r?\n/)) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const colon = trimmed.indexOf(":");
    const key = (colon >= 0 ? trimmed.slice(0, colon) : trimmed).trim();
    const value = (colon >= 0 ? trimmed.slice(colon + 1) : "").trim();
    if (key) out[key] = value;
  }
  return out;
}

function mapToHeadersText(headers: Record<string, string> | undefined): string {
  if (!headers) return "";
  return Object.entries(headers)
    .map(([key, value]) => key + ": " + value)
    .join("\n");
}

function requestScopedErrorsToText(errors: unknown[] | undefined): string {
  if (!errors?.length) return "";
  return errors.map((error) => JSON.stringify(error)).join("\n");
}

function parseRequestScopedErrors(text: string): unknown[] | undefined {
  const rows = text.split(/\r?\n/).map((row) => row.trim()).filter(Boolean);
  if (!rows.length) return [];
  return rows.map((row, index) => {
    const parsed = JSON.parse(row) as unknown;
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      throw new Error(`request scoped error #${index + 1} must be a JSON object`);
    }
    return parsed;
  });
}

function listToArray(text: string): string[] {
  return text
    .split(/[\r\n,]+/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function arrayToList(items: string[] | undefined): string {
  return (items ?? []).join("\n");
}

type ProviderIdentitySource = Pick<AIProviderChannelEntry, "index" | "name" | "base_url" | "auth_index" | "account_id" | "workspace_id" | "api_key_entries">;

function providerStableIdentity(entry: ProviderIdentitySource): string {
  const candidates = [
    entry.auth_index,
    entry.account_id,
    entry.workspace_id,
    entry.api_key_entries?.find((item) => item.auth_index)?.auth_index,
  ];
  const stable = candidates.find((value) => value && value.trim());
  if (stable) return stable.trim();
  const name = (entry.name ?? "").trim();
  const baseURL = (entry.base_url ?? "").trim();
  if (name || baseURL) return `channel:${name}|${baseURL}`;
  return `index:${entry.index}`;
}

function parseNonNegativeInteger(value: string): number | undefined {
  const trimmed = value.trim();
  if (!trimmed || !/^\d+$/.test(trimmed)) return undefined;
  const parsed = Number(trimmed);
  return Number.isSafeInteger(parsed) && parsed >= 0 ? parsed : undefined;
}

function validateProviderQuotaInputs(editing: EditingEntry, message: (key: UIMessageKey) => string): void {
  const integerFields: Array<[string, string, number | undefined]> = [
    [editing.concurrencyLimit, message("ui.ai_provider_limit"), 1000],
    [editing.fiveHourTotalTokens, `${message("ui.quota_window_five_hour")} · ${message("ui.ai_provider_budget_tokens")}`, undefined],
    [editing.sevenDayTotalTokens, `${message("ui.quota_window_seven_day")} · ${message("ui.ai_provider_budget_tokens")}`, undefined],
  ];
  for (const [value, label, maximum] of integerFields) {
    if (!value.trim()) continue;
    const parsed = parseNonNegativeInteger(value);
    if (parsed === undefined || (maximum !== undefined && parsed > maximum)) {
      throw new Error(`${label}: ${maximum === undefined ? "0+" : `0-${maximum}`}`);
    }
  }
  const percentFields: Array<[string, string]> = [
    [editing.fiveHourLimitPercent, `${message("ui.quota_window_five_hour")} · ${message("ui.ai_provider_limit_percent")}`],
    [editing.sevenDayLimitPercent, `${message("ui.quota_window_seven_day")} · ${message("ui.ai_provider_limit_percent")}`],
  ];
  for (const [value, label] of percentFields) {
    if (!value.trim()) continue;
    const parsed = parseNonNegativeInteger(value);
    if (parsed === undefined || parsed > 100) throw new Error(`${label}: 0-100`);
  }
}

function ProviderPolicyFields({
  entry,
  onEntry,
}: {
  entry: EditingEntry;
  onEntry: (patch: Partial<EditingEntry>) => void;
}) {
  const { tx } = useI18n();
  return (
    <section className="ai-provider-quota-editor" aria-label={tx("ui.ai_provider_quota_settings")}>
      <div className="ai-provider-models-title">{tx("ui.ai_provider_quota_settings")}</div>
      <p className="ai-provider-field-note">{tx("ui.ai_provider_quota_settings_description")}</p>
      <div className="ai-provider-form-grid">
        <label className="field-block">
          <span>{tx("ui.ai_provider_limit")}</span>
          <input type="number" min="0" max="1000" step="1" value={entry.concurrencyLimit} onChange={(event) => onEntry({ concurrencyLimit: event.target.value })} placeholder="0" />
          <small>{tx("ui.account_concurrency_zero_unlimited")}</small>
        </label>
        <label className="field-block">
          <span>{tx("ui.quota_window_five_hour")} · {tx("ui.ai_provider_budget_tokens")}</span>
          <input type="number" min="0" step="1" value={entry.fiveHourTotalTokens} onChange={(event) => onEntry({ fiveHourTotalTokens: event.target.value })} placeholder={tx("ui.not_set")} />
        </label>
        <label className="field-block">
          <span>{tx("ui.quota_window_five_hour")} · {tx("ui.ai_provider_limit_percent")}</span>
          <input type="number" min="0" max="100" step="1" value={entry.fiveHourLimitPercent} onChange={(event) => onEntry({ fiveHourLimitPercent: event.target.value })} placeholder={tx("ui.not_set")} />
        </label>
        <label className="field-block">
          <span>{tx("ui.quota_window_seven_day")} · {tx("ui.ai_provider_budget_tokens")}</span>
          <input type="number" min="0" step="1" value={entry.sevenDayTotalTokens} onChange={(event) => onEntry({ sevenDayTotalTokens: event.target.value })} placeholder={tx("ui.not_set")} />
        </label>
        <label className="field-block">
          <span>{tx("ui.quota_window_seven_day")} · {tx("ui.ai_provider_limit_percent")}</span>
          <input type="number" min="0" max="100" step="1" value={entry.sevenDayLimitPercent} onChange={(event) => onEntry({ sevenDayLimitPercent: event.target.value })} placeholder={tx("ui.not_set")} />
        </label>
      </div>
    </section>
  );
}

function RichChannelFields({
  entry,
  kind,
  onEntry,
  showSecret,
  onToggleSecret,
}: {
  entry: EditingEntry;
  kind: AIProviderChannelKind;
  onEntry: (patch: Partial<EditingEntry>) => void;
  showSecret: boolean;
  onToggleSecret: () => void;
}) {
  const { tx } = useI18n();
  const set = (patch: Partial<EditingEntry>) => onEntry(patch);

  const setModelsRow = (index: number, patch: Partial<AIProviderChannelModel>) => {
    const models = entry.models.map((model, i) => (i === index ? { ...model, ...patch } : model));
    set({ models });
  };
  const addModelsRow = () => set({ models: [...entry.models, { name: "" }] });
  const removeModelsRow = (index: number) => set({ models: entry.models.filter((_, i) => i !== index) });
  const setKeyRow = (index: number, patch: { apiKey?: string; weight?: string; proxyURL?: string }) => {
    const apiKeyEntries = entry.apiKeyEntries.map((keyEntry, i) => (i === index ? { ...keyEntry, ...patch } : keyEntry));
    set({ apiKeyEntries, apiKeyEntriesDirty: true });
  };
  const addKeyEntryRow = () => set({ apiKeyEntries: [...entry.apiKeyEntries, { apiKey: "", weight: "", proxyURL: "" }], apiKeyEntriesDirty: true });
  const removeKeyEntryRow = (index: number) => set({ apiKeyEntries: entry.apiKeyEntries.filter((_, i) => i !== index), apiKeyEntriesDirty: true });

  return (
    <>
      {kind === "openai-compatibility" ? (
        <label className="field-block">
          <span>{tx("ui.ai_provider_name")}</span>
          <input value={entry.name} onChange={(event) => set({ name: event.target.value })} autoComplete="off" />
        </label>
      ) : null}
      <label className="field-block">
        <span>{tx("ui.ai_provider_api_key")}</span>
        <div className="secret-input">
          <input
            value={entry.apiKey}
            onChange={(event) => set({ apiKey: event.target.value, ...(kind === "openai-compatibility" ? { apiKeyEntriesDirty: true } : {}) })}
            type={showSecret ? "text" : "password"}
            placeholder={entry.apiKeyEntries[0]?.apiKey ? tx("ui.ai_provider_key_keep_placeholder") : tx("ui.ai_provider_key_placeholder")}
            autoComplete="off"
          />
          <button type="button" aria-label={tx(showSecret ? "ui.hide_key" : "ui.show_key")} title={tx(showSecret ? "ui.hide_key" : "ui.show_key")} onClick={onToggleSecret}>
            {showSecret ? <EyeOff size={16} /> : <Eye size={16} />}
          </button>
        </div>
        <span className="ai-provider-field-note">
          {entry.apiKeyEntries[0]?.apiKey
            ? `${tx("ui.ai_provider_current_key")}: ${maskSecret(entry.apiKeyEntries[0].apiKey)}`
            : tx("ui.ai_provider_edit_key_note")}
        </span>
      </label>
      <label className="field-block">
        <span>{tx("ui.ai_provider_base_url")}</span>
        <input value={entry.baseURL} onChange={(event) => set({ baseURL: event.target.value })} placeholder="https://api.example.com/v1" autoComplete="off" />
      </label>
      {kind === "claude-api-key" ? (
        <label className="field-block">
          <span>{tx("ui.ai_provider_field_fingerprint_profile")}</span>
          <select value={entry.fingerprintProfile} onChange={(event) => set({ fingerprintProfile: event.target.value })}>
            <option value="">{tx("ui.ai_provider_field_fingerprint_default")}</option>
            <option value="claude-code-cli">{tx("ui.ai_provider_field_fingerprint_claude_code")}</option>
          </select>
        </label>
      ) : null}
      <div className="ai-provider-form-grid">
        <label className="field-block">
          <span>{tx("ui.ai_provider_field_prefix")}</span>
          <input value={entry.prefix} onChange={(event) => set({ prefix: event.target.value })} autoComplete="off" />
        </label>
        <label className="field-block">
          <span>{tx("ui.ai_provider_field_priority")}</span>
          <input value={entry.priority} onChange={(event) => set({ priority: event.target.value })} type="number" placeholder="0" autoComplete="off" />
        </label>
        <label className="field-block">
          <span>{tx("ui.ai_provider_field_weight")}</span>
          <input value={entry.weight} onChange={(event) => set({ weight: event.target.value })} type="number" placeholder="1" autoComplete="off" />
        </label>
        <label className="field-block">
          <span>{tx("ui.ai_provider_field_proxy_url")}</span>
          <input value={entry.proxyURL} onChange={(event) => set({ proxyURL: event.target.value })} placeholder="http://127.0.0.1:7890" autoComplete="off" />
        </label>
      </div>
      {hasModelEditor(kind) ? (
        <label className="field-block">
          <span>{tx("ui.ai_provider_field_excluded_models")}</span>
          <textarea value={entry.excludedText} onChange={(event) => set({ excludedText: event.target.value })} rows={3} placeholder={tx("ui.ai_provider_field_excluded_models_placeholder")} autoComplete="off" />
        </label>
      ) : null}
      <label className="field-block">
        <span>{tx("ui.ai_provider_field_headers")}</span>
        <textarea value={entry.headersText} onChange={(event) => set({ headersText: event.target.value })} rows={3} placeholder="X-Token: value" autoComplete="off" />
      </label>
      {hasModelEditor(kind) ? (
        <div className="ai-provider-models-editor">
          <span className="ai-provider-models-title">{tx("ui.ai_provider_field_models")}</span>
          {entry.models.map((model, index) => (
            <div className="ai-provider-model-row" key={index}>
              <input value={model.name} onChange={(event) => setModelsRow(index, { name: event.target.value })} placeholder={tx("ui.ai_provider_field_model_name")} autoComplete="off" />
              <input value={model.alias ?? ""} onChange={(event) => setModelsRow(index, { alias: event.target.value })} placeholder={tx("ui.ai_provider_field_alias")} autoComplete="off" />
              <input value={model.display_name ?? ""} onChange={(event) => setModelsRow(index, { display_name: event.target.value })} placeholder={tx("ui.ai_provider_field_display_name")} autoComplete="off" />
              <label className="ai-provider-model-flag" title={tx("ui.ai_provider_field_force_mapping")}>
                <input type="checkbox" checked={model.force_mapping === true} onChange={(event) => setModelsRow(index, { force_mapping: event.target.checked })} />
                <span>{tx("ui.ai_provider_field_force_mapping")}</span>
              </label>
              <label className="ai-provider-model-flag" title={tx("ui.ai_provider_field_is_compat")}>
                <input type="checkbox" checked={model.is_compat === true} onChange={(event) => setModelsRow(index, { is_compat: event.target.checked })} />
                <span>{tx("ui.ai_provider_field_is_compat")}</span>
              </label>
              {kind === "openai-compatibility" ? (
                <label className="ai-provider-model-flag" title={tx("ui.ai_provider_field_image")}>
                  <input type="checkbox" checked={model.image === true} onChange={(event) => setModelsRow(index, { image: event.target.checked })} />
                  <span>{tx("ui.ai_provider_field_image")}</span>
                </label>
              ) : null}
              <IconButton className="button-danger" label={tx("ui.remove")} onClick={() => removeModelsRow(index)}><Trash2 size={14} /></IconButton>
            </div>
          ))}
          <button className="button" type="button" onClick={addModelsRow}><Plus size={14} />{tx("ui.ai_provider_field_add_model")}</button>
        </div>
      ) : null}
      {kind === "openai-compatibility" ? (
        <div className="ai-provider-keys-editor">
          <span className="ai-provider-models-title">{tx("ui.ai_provider_field_api_key_entries")}</span>
          {entry.apiKeyEntries.map((keyEntry, index) => (
            <div className="ai-provider-model-row" key={index}>
              <input value={keyEntry.apiKey} onChange={(event) => setKeyRow(index, { apiKey: event.target.value })} type="password" placeholder="sk-..." autoComplete="off" />
              <input value={keyEntry.weight} onChange={(event) => setKeyRow(index, { weight: event.target.value })} type="number" placeholder={tx("ui.ai_provider_field_weight")} autoComplete="off" />
              <input value={keyEntry.proxyURL} onChange={(event) => setKeyRow(index, { proxyURL: event.target.value })} placeholder={tx("ui.ai_provider_field_proxy_url")} autoComplete="off" />
              <button className="button button-danger button-small" type="button" aria-label={tx("ui.remove")} onClick={() => removeKeyEntryRow(index)}><Trash2 size={14} /></button>
            </div>
          ))}
          <button className="button" type="button" onClick={addKeyEntryRow}><Plus size={14} />{tx("ui.ai_provider_field_add_api_key")}</button>
        </div>
      ) : null}
      <div className="ai-provider-switches">
        {kind === "openai-compatibility" ? (
          <label className="switch-control ai-provider-switch">
            <input type="checkbox" checked={entry.disabled} onChange={(event) => set({ disabled: event.target.checked })} />
            <span>{tx("ui.ai_provider_disabled")}</span>
          </label>
        ) : null}
        {kind === "openai-compatibility" ? (
          <label className="switch-control ai-provider-switch">
            <input type="checkbox" checked={entry.supportPromptCacheKey} onChange={(event) => set({ supportPromptCacheKey: event.target.checked })} />
            <span>{tx("ui.ai_provider_field_support_prompt_cache_key")}</span>
          </label>
        ) : null}
        {hasModelEditor(kind) ? (
          <label className="switch-control ai-provider-switch">
            <input type="checkbox" checked={entry.disableCooling} onChange={(event) => set({ disableCooling: event.target.checked })} />
            <span>{tx("ui.ai_provider_field_disable_cooling")}</span>
          </label>
        ) : null}
        <label className="field-block">
          <span>{tx("ui.ai_provider_field_request_retry")}</span>
          <input value={entry.requestRetry} onChange={(event) => set({ requestRetry: event.target.value })} type="number" min={0} placeholder="3" autoComplete="off" />
        </label>
        <label className="field-block">
          <span>{tx("ui.ai_provider_field_request_scoped_errors")}</span>
          <textarea value={entry.requestScopedErrorsText} onChange={(event) => set({ requestScopedErrorsText: event.target.value })} rows={3} placeholder='{"match":"rate limited"}' autoComplete="off" spellCheck={false} />
        </label>
        {kind === "codex-api-key" || kind === "xai-api-key" ? (
          <label className="switch-control ai-provider-switch">
            <input type="checkbox" checked={entry.alphaSearch} onChange={(event) => set({ alphaSearch: event.target.checked })} />
            <span>{tx("ui.ai_provider_field_alpha_search")}</span>
          </label>
        ) : null}
        {kind === "codex-api-key" ? (
          <label className="switch-control ai-provider-switch">
            <input type="checkbox" checked={entry.websockets} onChange={(event) => set({ websockets: event.target.checked })} />
            <span>{tx("ui.ai_provider_field_websockets")}</span>
          </label>
        ) : null}
        {kind === "claude-api-key" ? (
          <label className="switch-control ai-provider-switch">
            <input type="checkbox" checked={entry.rebuildMidSystemMessage} onChange={(event) => set({ rebuildMidSystemMessage: event.target.checked })} />
            <span>{tx("ui.ai_provider_field_rebuild_mid_system_message")}</span>
          </label>
        ) : null}
      </div>
    </>
  );
}


function hasModelEditor(kind: AIProviderChannelKind): boolean {
  return kind !== "opencode-go" && kind !== "opencode-zen" && kind !== "api-keys";
}

function channelLabelKey(kind: AIProviderChannelKind): UIMessageKey {
  return (api.AI_PROVIDER_CHANNELS.find((channel) => channel.kind === kind)?.labelKey ?? "ui.ai_provider_channel_openai_compatibility") as UIMessageKey;
}

export function AIProvidersSettings({ refreshRevision, onAPIError, onNotice }: AIProvidersSettingsProps) {
  const { locale, tx, formatDateTime } = useI18n();
  const [channels, setChannels] = useState<AIProviderChannelSnapshot[]>([]);
  const [runtimeSnapshots, setRuntimeSnapshots] = useState<AIProviderRuntimeSnapshot[]>([]);
  const [runtimeUpdatedAt, setRuntimeUpdatedAt] = useState("");
  const [runtimeError, setRuntimeError] = useState("");
  const [quotaPolicies, setQuotaPolicies] = useState<QuotaPolicySnapshot>({ accounts: {}, providers: [] });
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const [addKind, setAddKind] = useState<AddKind>("openai-compatibility");
  const [editing, setEditing] = useState<EditingEntry | null>(null);
  const [viewing, setViewing] = useState<{ kind: AIProviderChannelKind; entry: AIProviderChannelEntry } | null>(null);
  const [testing, setTesting] = useState<{ kind: AIProviderChannelKind; index: number; label: string } | null>(null);
  const [testResult, setTestResult] = useState<api.AIProviderProbeResult | null>(null);
  const [testModels, setTestModels] = useState<string[]>([]);
  const [testModel, setTestModel] = useState("");
  const [newName, setNewName] = useState("");
  const [newBaseURL, setNewBaseURL] = useState("");
  const [newAPIKey, setNewAPIKey] = useState("");
  const [newWorkspace, setNewWorkspace] = useState("");
  const [newCookie, setNewCookie] = useState("");
  const [showSecret, setShowSecret] = useState(false);
  const refreshRequest = useRef(0);
  const runtimeRequest = useRef(0);

  const handleError = useCallback((caught: unknown) => {
    if (caught instanceof api.APIError && caught.status === 401) {
      onAPIError(caught);
      return;
    }
    setError(operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale));
  }, [locale, onAPIError, tx]);

  const refresh = useCallback(async (signal?: AbortSignal) => {
    const requestID = refreshRequest.current + 1;
    refreshRequest.current = requestID;
    setLoading(true);
    setError("");
    try {
      const [nextChannels, nextQuotaPolicies] = await Promise.all([
        api.listAIProviderChannels(signal),
        api.getQuotaPolicies(signal),
      ]);
      if (requestID !== refreshRequest.current) return;
      setChannels(nextChannels);
      setQuotaPolicies(nextQuotaPolicies);
    } catch (caught) {
      if (signal?.aborted || (caught instanceof DOMException && caught.name === "AbortError")) return;
      if (requestID === refreshRequest.current) handleError(caught);
    } finally {
      if (requestID === refreshRequest.current) setLoading(false);
    }
  }, [handleError]);

  const providerPolicyKey = (kind: AIProviderChannelKind, entry: AIProviderChannelEntry) => `${kind}:${providerStableIdentity(entry)}`;
  const providerPolicyFor = (kind: AIProviderChannelKind, entry: AIProviderChannelEntry): ProviderQuotaPolicy | undefined => {
    const stableKey = providerPolicyKey(kind, entry);
    const legacyKey = `${kind}:${entry.index}`;
    return quotaPolicies.providers.find((policy) => policy.key === stableKey || policy.key === legacyKey);
  };
  const parseOptionalInteger = (value: string): number | undefined => {
    const trimmed = value.trim();
    if (!trimmed) return undefined;
    const parsed = Number(trimmed);
    return Number.isSafeInteger(parsed) && parsed >= 0 ? parsed : undefined;
  };
  const parseOptionalPercent = (value: string): number | undefined => {
    const parsed = parseOptionalInteger(value);
    return parsed !== undefined && parsed <= 100 ? parsed : undefined;
  };

  const refreshRuntime = useCallback(async (signal?: AbortSignal) => {
    const requestID = runtimeRequest.current + 1;
    runtimeRequest.current = requestID;
    try {
      const runtime = await api.getAIProviderRuntime(signal);
      if (requestID !== runtimeRequest.current) return;
      setRuntimeSnapshots(runtime.snapshots ?? []);
      setRuntimeUpdatedAt(runtime.updated_at ?? "");
      setRuntimeError("");
    } catch (caught) {
      if (signal?.aborted || (caught instanceof DOMException && caught.name === "AbortError")) return;
      // Runtime metrics are observability only; never block provider editing.
      if (requestID === runtimeRequest.current) {
        setRuntimeError(caught instanceof Error ? caught.message : tx("ui.ai_provider_runtime_unavailable"));
      }
    }
  }, [tx]);

  useEffect(() => {
    const controller = new AbortController();
    void refresh(controller.signal);
    return () => {
      controller.abort();
      refreshRequest.current += 1;
    };
  }, [refresh, refreshRevision]);
  useEffect(() => {
    const controller = new AbortController();
    let timer = 0;
    const poll = async () => {
      await refreshRuntime(controller.signal);
      if (!controller.signal.aborted) timer = window.setTimeout(() => void poll(), 5000);
    };
    void poll();
    return () => {
      controller.abort();
      window.clearTimeout(timer);
      runtimeRequest.current += 1;
    };
  }, [refreshRuntime, refreshRevision]);

  const runtimeForEntry = (entry: AIProviderChannelEntry): AIProviderRuntimeSnapshot | undefined => {
    // A provider may have one runtime identity per API key. CPA exposes those
    // identities as auth-index metadata on api-key-entries; aggregate all of
    // them for a truthful provider-level view instead of showing only the
    // first key (or no metrics at all).
    const authIndexes = new Set<string>();
    const topLevel = (entry.auth_index ?? "").trim();
    if (topLevel) authIndexes.add(topLevel);
    for (const keyEntry of entry.api_key_entries ?? []) {
      const authIndex = (keyEntry.auth_index ?? "").trim();
      if (authIndex) authIndexes.add(authIndex);
    }
    if (authIndexes.size === 0) return undefined;
    const matches = runtimeSnapshots.filter((snapshot) => {
      const authIndex = (snapshot.auth_index ?? "").trim();
      return authIndex !== "" && authIndexes.has(authIndex);
    });
    if (matches.length === 0) return undefined;
    if (matches.length === 1) return matches[0];
    const models = new Map<string, AIProviderRuntimeModelUsage>();
    for (const snapshot of matches) {
      for (const usage of snapshot.models ?? []) {
        const previous = models.get(usage.model);
        if (!previous) {
          models.set(usage.model, { ...usage });
          continue;
        }
        previous.input_tokens += usage.input_tokens;
        previous.output_tokens += usage.output_tokens;
        previous.reasoning_tokens += usage.reasoning_tokens;
        previous.cached_tokens += usage.cached_tokens;
        previous.total_tokens += usage.total_tokens;
        previous.amount_usd += usage.amount_usd;
        previous.rated_requests += usage.rated_requests;
        previous.unrated_requests += usage.unrated_requests;
        previous.rated = previous.rated || usage.rated;
      }
    }
    const limit = matches.some((snapshot) => !Number.isFinite(snapshot.limit) || snapshot.limit < 0)
      ? Number.POSITIVE_INFINITY
      : matches.reduce((sum, snapshot) => sum + Math.max(0, snapshot.limit), 0);
    const quota = {
      five_hour_used_tokens: matches.reduce((sum, snapshot) => sum + Math.max(0, snapshot.quota?.five_hour_used_tokens ?? 0), 0),
      seven_day_used_tokens: matches.reduce((sum, snapshot) => sum + Math.max(0, snapshot.quota?.seven_day_used_tokens ?? 0), 0),
      five_hour_percent: matches.reduce((sum, snapshot) => sum + Math.max(0, snapshot.quota?.five_hour_percent ?? 0), 0),
      seven_day_percent: matches.reduce((sum, snapshot) => sum + Math.max(0, snapshot.quota?.seven_day_percent ?? 0), 0),
    };
    const latest = matches.reduce((latestSnapshot, snapshot) =>
      snapshot.updated_at > latestSnapshot.updated_at ? snapshot : latestSnapshot,
    matches[0]);
    return {
      ...latest,
      auth_index: undefined,
      identity: `provider:${entry.name ?? entry.index}`,
      supported: matches.some((snapshot) => snapshot.supported),
      concurrency_configurable: matches.some((snapshot) => snapshot.concurrency_configurable === true),
      active: matches.reduce((sum, snapshot) => sum + Math.max(0, snapshot.active), 0),
      limit,
      input_tokens: matches.reduce((sum, snapshot) => sum + Math.max(0, snapshot.input_tokens), 0),
      output_tokens: matches.reduce((sum, snapshot) => sum + Math.max(0, snapshot.output_tokens), 0),
      reasoning_tokens: matches.reduce((sum, snapshot) => sum + Math.max(0, snapshot.reasoning_tokens), 0),
      cached_tokens: matches.reduce((sum, snapshot) => sum + Math.max(0, snapshot.cached_tokens), 0),
      total_tokens: matches.reduce((sum, snapshot) => sum + Math.max(0, snapshot.total_tokens), 0),
      amount_usd: matches.reduce((sum, snapshot) => sum + Math.max(0, snapshot.amount_usd), 0),
      rated_requests: matches.reduce((sum, snapshot) => sum + Math.max(0, snapshot.rated_requests), 0),
      unrated_requests: matches.reduce((sum, snapshot) => sum + Math.max(0, snapshot.unrated_requests), 0),
      quota,
      models: Array.from(models.values()),
    };
  };

  const formatTokens = (value: number | undefined) => new Intl.NumberFormat(locale).format(Math.max(0, value ?? 0));
  const formatAmount = (value: number | undefined) => {
    const amount = Math.max(0, value ?? 0);
    if (amount === 0) return "$0";
    if (amount < 0.000001) return "<$0.000001";
    return `$${amount.toFixed(6).replace(/0+$/, "").replace(/\.$/, "")}`;
  };

  const resetForm = () => {
    setAdding(false);
    setEditing(null);
    setViewing(null);
    setTesting(null);
    setTestResult(null);
    setTestModels([]);
    setTestModel("");
    setAddKind("openai-compatibility");
    setNewName("");
    setNewBaseURL("");
    setNewAPIKey("");
    setNewWorkspace("");
    setNewCookie("");
    setShowSecret(false);
  };

  const openAdd = () => {
    setError("");
    setAddKind("openai-compatibility");
    setAdding(true);
  };

  const submitNewProvider = async () => {
    if (busy) return;
    if (addKind === "opencode-go") {
      if (!newWorkspace.trim() || !newCookie.trim()) return;
    } else if (addKind === "opencode-zen") {
      if (!newAPIKey.trim()) return;
    } else if (addKind === "openai-compatibility") {
      if (!newName.trim() || !newBaseURL.trim() || !newAPIKey.trim()) return;
    } else if (!newAPIKey.trim()) {
      return;
    }
    setBusy(true);
    setError("");
    try {
      if (addKind === "opencode-go") {
        await api.addAIProviderChannel("opencode-go", { workspace_id: newWorkspace, auth_cookie: newCookie });
      } else if (addKind === "opencode-zen") {
        await api.addAIProviderChannel("opencode-zen", { name: newName, base_url: newBaseURL, api_key: newAPIKey });
      } else if (addKind === "openai-compatibility") {
        await api.addAIProviderChannel("openai-compatibility", { name: newName, base_url: newBaseURL, api_key: newAPIKey });
      } else {
        await api.addAIProviderChannel(addKind, { api_key: newAPIKey, base_url: newBaseURL || undefined });
      }
      onNotice(tx("ui.ai_provider_added"));
      resetForm();
      await refresh();
    } catch (caught) {
      handleError(caught);
    } finally {
      setBusy(false);
    }
  };

  const submitEdit = async () => {
    if (busy || !editing) return;
    setBusy(true);
    setError("");
    try {
      const policy: ProviderQuotaPolicy = {
        // Keep the original policy identity while editing. For channels that
        // do not expose a stable CPA auth index, the fallback identity may be
        // derived from name/base URL; both are editable fields and must not
        // silently orphan the saved quota/concurrency policy.
        key: editing.quotaPolicyKey || providerPolicyKey(editing.kind, {
          index: editing.index,
          name: editing.name,
          base_url: editing.baseURL,
          account_id: editing.accountID,
          workspace_id: editing.workspaceID,
        }),
        // The channel name is display metadata, not a policy setting. Keeping
        // it here would make an otherwise empty policy undeletable because
        // the backend correctly treats non-empty labels as persisted state.
        label: "",
        concurrency_limit: parseOptionalInteger(editing.concurrencyLimit),
        five_hour: {
          total_tokens: parseOptionalInteger(editing.fiveHourTotalTokens),
          limit_percent: parseOptionalPercent(editing.fiveHourLimitPercent),
        },
        seven_day: {
          total_tokens: parseOptionalInteger(editing.sevenDayTotalTokens),
          limit_percent: parseOptionalPercent(editing.sevenDayLimitPercent),
        },
      };
      validateProviderQuotaInputs(editing, tx);
      if (editing.kind === "opencode-go") {
        if (editing.apiKey.trim()) {
          await api.saveOpenCodeAccount(editing.workspaceID ?? editing.name, editing.apiKey.trim());
          onNotice(tx("ui.ai_provider_saved"));
        }
      } else if (editing.kind === "opencode-zen") {
        await api.saveOpenCodeZenAccount({
          account_id: editing.accountID,
          name: editing.name,
          base_url: editing.baseURL,
          zen_api_key: editing.apiKey.trim() || undefined,
        });
        onNotice(tx("ui.ai_provider_saved"));
      } else if (editing.kind === "api-keys") {
        // api-keys is a plain string list: PATCH { index, value } is the only safe touch.
        if (editing.apiKey.trim()) {
          await api.patchAIProviderChannelEntry(editing.kind, editing.index, editing.apiKey.trim());
        }
        onNotice(tx("ui.ai_provider_saved"));
      } else {
        const patch: api.AIProviderChannelEntryPatch = {
          name: editing.name.trim(),
          base_url: editing.baseURL.trim(),
          prefix: editing.prefix.trim(),
          proxy_url: editing.proxyURL.trim(),
          headers: headersToMap(editing.headersText),
          disabled: editing.disabled,
          priority: editing.priority.trim() ? editing.priority.trim() : null,
          ...(editing.kind === "openai-compatibility" ? {} : { weight: editing.weight.trim() ? editing.weight.trim() : null }),
        };
        if (editing.apiKey.trim()) patch.api_key = editing.apiKey.trim();
        if (editing.kind !== "openai-compatibility") {
          patch.excluded_models = listToArray(editing.excludedText);
          patch.disable_cooling = editing.disableCooling;
        }
        if (editing.requestRetry.trim()) {
          const requestRetry = Number(editing.requestRetry.trim());
          if (!Number.isInteger(requestRetry) || requestRetry < 0) {
            throw new Error("request retry must be a non-negative integer");
          }
          patch.request_retry = requestRetry;
        } else {
          patch.request_retry = null;
        }
        patch.request_scoped_errors = parseRequestScopedErrors(editing.requestScopedErrorsText);
        if (editing.kind === "openai-compatibility") {
          patch.support_prompt_cache_key = editing.supportPromptCacheKey;
          // The simple replacement field targets the first credential row. Keep
          // the remaining weighted keys untouched so a single-key edit cannot
          // delete the rest of the pool.
          const replacementAPIKey = editing.apiKey.trim();
          const nextFirstKey = replacementAPIKey || (editing.apiKeyEntries[0]?.apiKey ?? "").trim();
          const apiKeyEntries = editing.apiKeyEntries.length > 0
            ? editing.apiKeyEntries.map((keyEntry, index) => ({ ...keyEntry, ...(index === 0 && nextFirstKey ? { apiKey: nextFirstKey } : {}), raw: { ...(keyEntry.raw ?? {}) } }))
            : [{ apiKey: nextFirstKey, weight: "", proxyURL: "", raw: undefined }];
          if (editing.apiKeyEntriesDirty || replacementAPIKey) {
            patch.api_key_entries = apiKeyEntries
              .map((keyEntry) => ({
                api_key: keyEntry.apiKey.trim(),
                weight: keyEntry.weight.trim(),
                proxy_url: keyEntry.proxyURL.trim(),
                ...(keyEntry.raw ? { raw: { ...keyEntry.raw } } : {}),
              }))
              .filter((keyEntry) => keyEntry.api_key);
          }
        }
        if (editing.kind === "codex-api-key" || editing.kind === "xai-api-key") patch.alpha_search = editing.alphaSearch;
        if (editing.kind === "codex-api-key") patch.websockets = editing.websockets;
        if (editing.kind === "claude-api-key") patch.rebuild_mid_system_message = editing.rebuildMidSystemMessage;
        if (editing.kind === "claude-api-key") patch.fingerprint_profile = editing.fingerprintProfile;
        if (hasModelEditor(editing.kind)) {
          const models = editing.models
            .map((model) => ({
              // Keep the original model metadata while normalizing only the
              // fields exposed by this editor. The API serializer removes
              // intentionally cleared known fields but preserves CPA fields
              // this UI does not yet render.
              ...model,
              name: model.name.trim(),
              alias: typeof model.alias === "string" ? model.alias.trim() : model.alias,
              display_name: typeof model.display_name === "string" ? model.display_name.trim() : model.display_name,
            }))
            .filter((model) => model.name.trim());
          patch.models = models;
        }
        await api.saveAIProviderChannelEntry(editing.kind, editing.index, patch);
        onNotice(tx("ui.ai_provider_saved"));
      }
      await api.saveAIProviderQuotaPolicy(policy);
      resetForm();
      await refresh();
    } catch (caught) {
      handleError(caught);
    } finally {
      setBusy(false);
    }
  };

  const deleteEntry = async (entry: AIProviderChannelEntry, kind: AIProviderChannelKind) => {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      await api.deleteAIProviderChannelEntry(kind, entry.index, entry.account_id);
      onNotice(tx("ui.ai_provider_deleted"));
      await refresh();
    } catch (caught) {
      handleError(caught);
    } finally {
      setBusy(false);
    }
  };

  const toggleEnabled = async (entry: AIProviderChannelEntry, kind: AIProviderChannelKind, enabled: boolean) => {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      await api.setAIProviderChannelEnabled(kind, entry.index, enabled);
      onNotice(enabled ? tx("ui.ai_provider_enabled") : tx("ui.ai_provider_disabled_notice"));
      await refresh();
    } catch (caught) {
      handleError(caught);
    } finally {
      setBusy(false);
    }
  };

  const testChannel = async (entry: AIProviderChannelEntry, kind: AIProviderChannelKind) => {
    if (busy) return;
    if (kind === "opencode-go") return;
    const unsupported = kind === "vertex-api-key" || kind === "api-keys"
      ? tx("ui.ai_provider_model_catalog_unavailable")
      : "";
    if (unsupported) {
      setError(unsupported);
      setTesting({ kind, index: entry.index, label: entry.name || entry.base_url || `#${entry.index + 1}` });
      setTestResult({
        reachable: false,
        status: "unsupported",
        probe_kind: "model",
        reason_code: "unsupported_provider",
        detail: unsupported,
        tested_at: new Date().toISOString(),
      });
      return;
    }
    setBusy(true);
    setError("");
    setTestResult(null);
    setTestModels([]);
    setTestModel("");
    setTesting({ kind, index: entry.index, label: entry.name || entry.base_url || `#${entry.index + 1}` });
    try {
      if (kind === "opencode-zen") {
        setTestResult((await api.probeOpenCodeZenAccount(entry.account_id as string)).result);
      } else {
        const configured = (entry.models ?? []).map((model) => String(model.name ?? model.alias ?? "").trim()).filter(Boolean);
        let catalog: api.AIProviderProbeResult | null = null;
        try {
          catalog = await api.testAIProviderChannelForKind(kind, entry.base_url ?? "", entry.api_key ?? "", 15, entry.headers, entry.auth_index || entry.account_id);
        } catch {
          // Some compatible gateways intentionally do not expose /models.
          // Configured models are still real routing targets, so continue.
        }
        const discovered = (catalog?.models ?? []).map((model) => String(model.id ?? "").trim()).filter(Boolean);
        const models = [...new Set([...discovered, ...configured])];
        setTestModels(models);
        const selected = models[0] ?? "";
        setTestModel(selected);
        if (selected) {
          const result = await api.testAIProviderChannelForKind(kind, entry.base_url ?? "", entry.api_key ?? "", 15, entry.headers, entry.auth_index || entry.account_id, selected);
          if (!catalog?.reachable && configured.length > 0) {
            result.detail = result.detail ? `${result.detail} · model catalog unavailable` : "model catalog unavailable";
          }
          setTestResult(result);
        } else if (catalog) {
          setTestResult(catalog);
        } else {
          setTestResult({
            reachable: false,
            status: "unsupported",
            probe_kind: "model",
            reason_code: "no_models_available",
            detail: tx("ui.ai_provider_no_models_available"),
            tested_at: new Date().toISOString(),
          });
        }
      }
    } catch (caught) {
      handleError(caught);
    } finally {
      setBusy(false);
    }
  };

  const retestChannelModel = async () => {
    if (!testing || !testModel || busy) return;
    const channel = channels.find((item) => item.kind === testing.kind);
    const entry = channel?.entries.find((item) => item.index === testing.index);
    if (!entry || testing.kind === "opencode-zen") return;
    setBusy(true);
    try {
      setTestResult(await api.testAIProviderChannelForKind(testing.kind, entry.base_url ?? "", entry.api_key ?? "", 15, entry.headers, entry.auth_index || entry.account_id, testModel));
    } catch (caught) { handleError(caught); } finally { setBusy(false); }
  };

  const closeTest = () => {
    setTesting(null);
    setTestResult(null);
    setTestModels([]);
    setTestModel("");
  };

  const addFormValid = addKind === "opencode-go"
    ? Boolean(newWorkspace.trim() && newCookie.trim())
    : addKind === "opencode-zen"
      ? Boolean(newAPIKey.trim())
      : addKind === "openai-compatibility"
        ? Boolean(newName.trim() && newBaseURL.trim() && newAPIKey.trim())
        : Boolean(newAPIKey.trim());

  return (
    <section className="ai-providers-section" role="tabpanel" aria-label={tx("ui.ai_providers")}>
      <div className="settings-section-heading ai-providers-heading">
        <div>
          <h3>{tx("ui.ai_providers")}</h3>
          <p>{tx("ui.ai_providers_description")}</p>
        </div>
        <div className="settings-section-actions">
          <button className="button" type="button" onClick={() => void refresh()} disabled={loading}>
            {loading ? <LoaderCircle className="spin" size={16} /> : <RefreshCw size={16} />}
            {tx("ui.refresh")}
          </button>
          <button className="button button-primary" type="button" onClick={openAdd}>
            <Plus size={16} />{tx("ui.add_ai_provider")}
          </button>
        </div>
      </div>

      {error ? <div className="agent-login-error" role="alert">{error}</div> : null}

      {adding ? (
        <Modal title={tx("ui.add_ai_provider")} onClose={() => { if (!busy) resetForm(); }} footer={(
          <>
            <button className="button" type="button" disabled={busy} onClick={resetForm}>{tx("ui.cancel")}</button>
            <button className="button button-primary" type="button" disabled={busy || !addFormValid} onClick={() => void submitNewProvider()}>
              {busy ? <LoaderCircle className="spin" size={16} /> : <Save size={16} />}
              {tx("ui.add_ai_provider_confirm")}
            </button>
          </>
        )}>
          <div className="ai-provider-type-select">
            <span className="ai-provider-type-label">{tx("ui.ai_provider_type")}</span>
            <div className="ai-provider-type-options" role="radiogroup" aria-label={tx("ui.choose_ai_provider_type")}>
              {addableKinds.map((item) => (
                <label key={item.kind} className={`ai-provider-type-option ${addKind === item.kind ? "active" : ""}`}>
                  <input
                    type="radio"
                    name="ai-provider-kind"
                    value={item.kind}
                    checked={addKind === item.kind}
                    onChange={() => { setAddKind(item.kind); setError(""); }}
                  />
                  <span className="ai-provider-type-option-main">
                    <strong>{tx(item.labelKey)}</strong>
                    <small>{tx(item.descriptionKey)}</small>
                  </span>
                </label>
              ))}
            </div>
          </div>

          <div className="ai-provider-form">
            {addKind === "opencode-go" ? (
              <>
                <label className="field-block">
                  <span>{tx("ui.opencode_workspace_id")}</span>
                  <input value={newWorkspace} onChange={(event) => setNewWorkspace(event.target.value)} placeholder={tx("ui.opencode_workspace_placeholder")} autoFocus autoComplete="off" />
                </label>
                <label className="field-block">
                  <span>{tx("ui.opencode_auth_cookie")}</span>
                  <div className="secret-input">
                    <input
                      value={newCookie}
                      onChange={(event) => setNewCookie(event.target.value)}
                      type={showSecret ? "text" : "password"}
                      placeholder={tx("ui.opencode_auth_cookie_placeholder")}
                      autoComplete="off"
                    />
                    <button type="button" aria-label={tx(showSecret ? "ui.hide_key" : "ui.show_key")} title={tx(showSecret ? "ui.hide_key" : "ui.show_key")} onClick={() => setShowSecret((value) => !value)}>
                      {showSecret ? <EyeOff size={16} /> : <Eye size={16} />}
                    </button>
                  </div>
                </label>
              </>
            ) : addKind === "opencode-zen" ? (
              <>
                <label className="field-block">
                  <span>{tx("ui.ai_provider_name")}</span>
                  <input value={newName} onChange={(event) => setNewName(event.target.value)} placeholder="claude-code-bridge" autoFocus autoComplete="off" />
                </label>
                <label className="field-block">
                  <span>{tx("ui.opencode_zen_base_url")}</span>
                  <input value={newBaseURL} onChange={(event) => setNewBaseURL(event.target.value)} placeholder="https://opencode.ai/zen" autoComplete="off" />
                </label>
                <label className="field-block">
                  <span>{tx("ui.opencode_zen_api_key")}</span>
                  <div className="secret-input">
                    <input
                      value={newAPIKey}
                      onChange={(event) => setNewAPIKey(event.target.value)}
                      type={showSecret ? "text" : "password"}
                      placeholder="sk-..."
                      autoComplete="off"
                    />
                    <button type="button" aria-label={tx(showSecret ? "ui.hide_key" : "ui.show_key")} title={tx(showSecret ? "ui.hide_key" : "ui.show_key")} onClick={() => setShowSecret((value) => !value)}>
                      {showSecret ? <EyeOff size={16} /> : <Eye size={16} />}
                    </button>
                  </div>
                </label>
              </>
            ) : addKind === "openai-compatibility" ? (
              <>
                <label className="field-block">
                  <span>{tx("ui.ai_provider_name")}</span>
                  <input value={newName} onChange={(event) => setNewName(event.target.value)} autoFocus autoComplete="off" />
                </label>
                <label className="field-block">
                  <span>{tx("ui.ai_provider_base_url")}</span>
                  <input value={newBaseURL} onChange={(event) => setNewBaseURL(event.target.value)} placeholder="https://api.example.com/v1" autoComplete="off" />
                </label>
                <label className="field-block">
                  <span>{tx("ui.ai_provider_api_key")}</span>
                  <div className="secret-input">
                    <input
                      value={newAPIKey}
                      onChange={(event) => setNewAPIKey(event.target.value)}
                      type={showSecret ? "text" : "password"}
                      placeholder="sk-..."
                      autoComplete="off"
                    />
                    <button type="button" aria-label={tx(showSecret ? "ui.hide_key" : "ui.show_key")} title={tx(showSecret ? "ui.hide_key" : "ui.show_key")} onClick={() => setShowSecret((value) => !value)}>
                      {showSecret ? <EyeOff size={16} /> : <Eye size={16} />}
                    </button>
                  </div>
                </label>
              </>
            ) : (
              <>
                <label className="field-block">
                  <span>{tx("ui.ai_provider_api_key")}</span>
                  <div className="secret-input">
                    <input
                      value={newAPIKey}
                      onChange={(event) => setNewAPIKey(event.target.value)}
                      type={showSecret ? "text" : "password"}
                      placeholder="sk-..."
                      autoFocus
                      autoComplete="off"
                    />
                    <button type="button" aria-label={tx(showSecret ? "ui.hide_key" : "ui.show_key")} title={tx(showSecret ? "ui.hide_key" : "ui.show_key")} onClick={() => setShowSecret((value) => !value)}>
                      {showSecret ? <EyeOff size={16} /> : <Eye size={16} />}
                    </button>
                  </div>
                </label>
                {addKind === "api-keys" ? null : (
                  <label className="field-block">
                    <span>{tx("ui.ai_provider_base_url")}</span>
                    <input value={newBaseURL} onChange={(event) => setNewBaseURL(event.target.value)} placeholder="https://api.example.com/v1" autoComplete="off" />
                  </label>
                )}
              </>
            )}
          </div>
        </Modal>
      ) : null}

      {editing ? (
        <Modal title={tx("ui.edit_ai_provider")} onClose={() => { if (!busy) resetForm(); }} footer={(
          <>
            <button className="button" type="button" disabled={busy} onClick={resetForm}>{tx("ui.cancel")}</button>
            <button className="button button-primary" type="button" disabled={busy} onClick={() => void submitEdit()}>
              {busy ? <LoaderCircle className="spin" size={16} /> : <Save size={16} />}
              {tx("ui.save")}
            </button>
          </>
        )}>
          <div className="ai-provider-form">
            {editing.kind === "opencode-go" ? (
              <label className="field-block">
                <span>{tx("ui.opencode_auth_cookie")}</span>
                <div className="secret-input">
                  <input
                    value={editing.apiKey}
                    onChange={(event) => setEditing({ ...editing, apiKey: event.target.value })}
                    type={showSecret ? "text" : "password"}
                    placeholder={tx("ui.opencode_auth_cookie_placeholder")}
                    autoComplete="off"
                  />
                  <button type="button" aria-label={tx(showSecret ? "ui.hide_key" : "ui.show_key")} title={tx(showSecret ? "ui.hide_key" : "ui.show_key")} onClick={() => setShowSecret((value) => !value)}>
                    {showSecret ? <EyeOff size={16} /> : <Eye size={16} />}
                  </button>
                </div>
                <span className="ai-provider-field-note">{tx("ui.opencode_edit_cookie_note")}</span>
              </label>
            ) : editing.kind === "opencode-zen" ? (
              <>
                <label className="field-block">
                  <span>{tx("ui.ai_provider_name")}</span>
                  <input value={editing.name} onChange={(event) => setEditing({ ...editing, name: event.target.value })} placeholder="claude-code-bridge" autoComplete="off" />
                </label>
                <label className="field-block">
                  <span>{tx("ui.opencode_zen_base_url")}</span>
                  <input value={editing.baseURL} onChange={(event) => setEditing({ ...editing, baseURL: event.target.value })} placeholder="https://opencode.ai/zen" autoComplete="off" />
                </label>
                <label className="field-block">
                  <span>{tx("ui.opencode_zen_api_key")}</span>
                  <div className="secret-input">
                    <input
                      value={editing.apiKey}
                      onChange={(event) => setEditing({ ...editing, apiKey: event.target.value })}
                      type={showSecret ? "text" : "password"}
                      autoComplete="off"
                    />
                    <button type="button" aria-label={tx(showSecret ? "ui.hide_key" : "ui.show_key")} title={tx(showSecret ? "ui.hide_key" : "ui.show_key")} onClick={() => setShowSecret((value) => !value)}>
                      {showSecret ? <EyeOff size={16} /> : <Eye size={16} />}
                    </button>
                  </div>
                  <span className="ai-provider-field-note">{tx("ui.opencode_zen_edit_key_note")}</span>
                </label>
              </>
            ) : editing.kind === "api-keys" ? (
              <label className="field-block">
                <span>{tx("ui.ai_provider_api_key")}</span>
                <div className="secret-input">
                  <input
                    value={editing.apiKey}
                    onChange={(event) => setEditing({ ...editing, apiKey: event.target.value })}
                    type={showSecret ? "text" : "password"}
                    autoComplete="off"
                  />
                  <button type="button" aria-label={tx(showSecret ? "ui.hide_key" : "ui.show_key")} title={tx(showSecret ? "ui.hide_key" : "ui.show_key")} onClick={() => setShowSecret((value) => !value)}>
                    {showSecret ? <EyeOff size={16} /> : <Eye size={16} />}
                  </button>
                </div>
                <span className="ai-provider-field-note">{tx("ui.ai_provider_edit_key_note")}</span>
              </label>
            ) : (
              <RichChannelFields
                entry={editing}
                kind={editing.kind}
                onEntry={(patch) => setEditing((current) => (current ? { ...current, ...patch } : current))}
                showSecret={showSecret}
                onToggleSecret={() => setShowSecret((value) => !value)}
              />
            )}
            <ProviderPolicyFields
              entry={editing}
              onEntry={(patch) => setEditing((current) => (current ? { ...current, ...patch } : current))}
            />
          </div>
        </Modal>
      ) : null}

      {viewing ? (
        <Modal title={tx("ui.view_ai_provider", { name: viewing.entry.name || viewing.entry.workspace_id || `#${viewing.entry.index + 1}` })} onClose={() => setViewing(null)} footer={(
          <button className="button" type="button" onClick={() => setViewing(null)}>{tx("ui.close")}</button>
        )}>
          <div className="ai-provider-detail">
            <div className="ai-provider-detail-row"><span>{tx("ui.ai_provider_type")}</span><strong>{tx(channelLabelKey(viewing.kind))}</strong></div>
            <div className="ai-provider-detail-row"><span>{tx("ui.ai_provider_name")}</span><strong>{viewing.entry.name || viewing.entry.workspace_id || "-"}</strong></div>
            <div className="ai-provider-detail-row"><span>{tx("ui.ai_provider_base_url")}</span><code>{viewing.entry.base_url || viewing.entry.workspace_id || "-"}</code></div>
            <div className="ai-provider-detail-row"><span>{tx("ui.ai_provider_api_key")}</span><code className="ai-provider-table-secret">{maskSecret(viewing.entry.api_key) || "-"}</code></div>
            {viewing.entry.prefix ? <div className="ai-provider-detail-row"><span>{tx("ui.prefix")}</span><strong>{viewing.entry.prefix}</strong></div> : null}
            {viewing.entry.priority !== undefined ? <div className="ai-provider-detail-row"><span>{tx("ui.priority")}</span><strong>{viewing.entry.priority}</strong></div> : null}
            {viewing.entry.proxy_url ? <div className="ai-provider-detail-row"><span>{tx("ui.proxy_url")}</span><code>{viewing.entry.proxy_url}</code></div> : null}
            <div className="ai-provider-detail-row"><span>{tx("ui.status")}</span><strong>{viewing.entry.disabled ? tx("ui.disabled") : tx("ui.enabled")}</strong></div>
            {viewing.entry.models && viewing.entry.models.length > 0 ? (
              <div className="ai-provider-detail-row ai-provider-detail-models"><span>{tx("ui.models")}</span><div>{viewing.entry.models.map((model, index) => <code key={index}>{String(model.name ?? model.alias ?? "") || `#${index + 1}`}</code>)}</div></div>
            ) : null}
            {viewing.entry.excluded_models && viewing.entry.excluded_models.length > 0 ? (
              <div className="ai-provider-detail-row"><span>{tx("ui.excluded_models")}</span><strong>{viewing.entry.excluded_models.join(", ")}</strong></div>
            ) : null}
            {viewing.entry.headers && Object.keys(viewing.entry.headers).length > 0 ? (
              <div className="ai-provider-detail-row"><span>{tx("ui.headers")}</span><code>{Object.entries(viewing.entry.headers).map(([key, value]) => `${key}: ${maskSecret(value)}`).join("; ")}</code></div>
            ) : null}
            {(() => {
              const runtime = runtimeForEntry(viewing.entry);
              const policy = providerPolicyFor(viewing.kind, viewing.entry);
              const budget = (window: ProviderQuotaPolicy["five_hour"], usedTokens: number | undefined) => {
                if (!window.total_tokens || window.total_tokens <= 0) return window.limit_percent === undefined ? "-" : `— / ${window.limit_percent}%`;
                if (usedTokens === undefined) return `— / ${window.limit_percent ?? 100}%`;
                return `${Math.min(999, usedTokens / window.total_tokens * 100).toFixed(1)}% / ${window.limit_percent ?? 100}%`;
              };
              if (!runtime && !policy) return <div className="ai-provider-detail-row"><span>{tx("ui.ai_provider_usage")}</span><strong>{tx("ui.ai_provider_identity_unavailable")}</strong></div>;
              return (
                <div className="ai-provider-runtime-detail">
                  {(runtime?.supported || policy) ? <div className="ai-provider-detail-row"><span>{tx("ui.ai_provider_concurrency")}</span><strong>{runtime ? `${runtime.active}/${policy?.concurrency_limit === undefined || policy.concurrency_limit === 0 ? (runtime.limit > 0 ? runtime.limit : "∞") : policy.concurrency_limit}` : policy?.concurrency_limit === undefined || policy.concurrency_limit === 0 ? "-" : `0/${policy.concurrency_limit}`}</strong></div> : null}
                  {policy ? <>
                    <div className="ai-provider-detail-row"><span>{tx("ui.quota_window_five_hour")}</span><strong>{budget(policy.five_hour, runtime?.quota?.five_hour_used_tokens)}</strong></div>
                    <div className="ai-provider-detail-row"><span>{tx("ui.quota_window_seven_day")}</span><strong>{budget(policy.seven_day, runtime?.quota?.seven_day_used_tokens)}</strong></div>
                  </> : null}
                  {runtime ? <>
                    <div className="ai-provider-detail-row"><span>{tx("ui.ai_provider_total_tokens")}</span><strong>{formatTokens(runtime.total_tokens)}</strong></div>
                    <div className="ai-provider-detail-row"><span>{tx("ui.ai_provider_estimated_cost")}</span><strong>{formatAmount(runtime.amount_usd)}</strong></div>
                  </> : null}
                  {runtime?.models && runtime.models.length > 0 ? (
                    <table className="account-table ai-provider-runtime-models"><thead><tr><th>{tx("ui.ai_provider_model")}</th><th>{tx("ui.ai_provider_input_tokens")}</th><th>{tx("ui.ai_provider_output_tokens")}</th><th>{tx("ui.ai_provider_total")}</th><th>{tx("ui.ai_provider_cost")}</th></tr></thead><tbody>{runtime.models.map((model) => <tr key={model.model}><td>{model.model}</td><td>{formatTokens(model.input_tokens)}</td><td>{formatTokens(model.output_tokens)}</td><td>{formatTokens(model.total_tokens)}</td><td>{formatAmount(model.amount_usd)}</td></tr>)}</tbody></table>
                  ) : runtime ? <div className="ai-provider-field-note">{tx("ui.ai_provider_no_usage")}</div> : null}
                </div>
              );
            })()}
          </div>
        </Modal>
      ) : null}

      {testing ? (
        <Modal title={tx("ui.test_ai_provider_title", { name: testing.label })} onClose={closeTest} footer={(
          <>
            {testModels.length > 0 && testResult ? (
              <button className="button" type="button" disabled={!testModel || busy} onClick={() => void retestChannelModel()}>
                {busy ? <LoaderCircle className="spin" size={16} /> : null}{tx("ui.test_again")}
              </button>
            ) : null}
            <button className="button button-primary" type="button" onClick={closeTest}>{tx("ui.close")}</button>
          </>
        )}>
          <div className="model-test-dialog">
            <div className="model-test-account">
              <span className="model-test-account-icon"><Activity size={18} /></span>
              <div>
                <strong>{testing.label}</strong>
                <span>{technicalLabel(testing.kind, locale)} · {testing.kind === "openai-compatibility" ? "api_key" : testing.kind}</span>
              </div>
            </div>
            <label className="model-test-field">
              <span>{tx("ui.test_model")}</span>
              {testModels.length > 0 ? (
                <select aria-label={tx("ui.test_model")} value={testModel} disabled={busy} onChange={(event) => setTestModel(event.target.value)}>
                  {testModels.map((model) => <option key={model} value={model}>{model}</option>)}
                </select>
              ) : (
                <span className="model-test-field-note">{tx("ui.ai_provider_no_models_available")}</span>
              )}
            </label>
            {busy ? <div className="ai-provider-test-running" role="status"><LoaderCircle className="spin" size={20} /><span>{tx("ui.testing_ai_provider")}</span></div> : null}
            {!busy ? (
            (() => {
              const result = testResult;
              if (!result) return null;
              const status = result.status || (result.reachable ? "available" : "unavailable");
              const StatusIcon = status === "available" ? CheckCircle2 : status === "unavailable" ? XCircle : status === "unsupported" ? AlertTriangle : ShieldQuestion;
              const reasonKey = providerTestReasonLabels[result.reason_code ?? ""];
              const description = result.detail
                ? operatorMessage(result.detail, locale)
                : reasonKey
                  ? tx(reasonKey)
                  : result.status_code
                    ? tx("ui.the_test_result_requires_manual_confirmation")
                    : "";
              return (
                <section className={`model-test-outcome outcome-${status}`} aria-label={tx("ui.model_test_result")}>
                  <div className="model-test-outcome-heading"><StatusIcon size={21} /><div><strong>{tx(providerTestStatusLabels[status])}</strong><span>{description}</span></div></div>
                  <dl>
                    {result.model ? <div><dt>{tx("ui.model")}</dt><dd>{result.model}</dd></div> : null}
                    <div><dt>{tx("ui.http_status")}</dt><dd>{result.status_code || "-"}</dd></div>
                    {result.probe_kind ? <div><dt>{tx("ui.probe_type")}</dt><dd>{result.probe_kind === "credential" ? tx("ui.credential_probe") : tx("ui.model_probe")}</dd></div> : null}
                    {typeof result.latency_ms === "number" ? <div><dt>{tx("ui.latency")}</dt><dd>{result.latency_ms >= 0 ? `${result.latency_ms} ms` : "-"}</dd></div> : null}
                    {result.tested_at ? <div><dt>{tx("ui.tested_at")}</dt><dd>{formatDateTime(result.tested_at)}</dd></div> : null}
                  </dl>
                  {result.response ? <ProviderTestResponse response={result.response} /> : null}
                </section>
              );
              })()
            ) : null}
          </div>
        </Modal>
      ) : null}

      {loading ? (
        <div className="ai-provider-table-wrap ai-provider-loading" aria-label={tx("ui.loading")}>
          <LoaderCircle className="spin" size={24} />
        </div>
      ) : (
        <div className="ai-provider-table-wrap">
          <table className="account-table ai-provider-table">
            <thead><tr>
              <th>{tx("ui.ai_provider_type")}</th>
              <th>{tx("ui.ai_provider_name")}</th>
              <th>{tx("ui.ai_provider_base_url")}</th>
              <th>{tx("ui.ai_provider_api_key")}</th>
              <th>{tx("ui.status")}</th>
              <th>{tx("ui.ai_provider_model_count")}</th>
              <th>{tx("ui.ai_provider_concurrency")}</th>
              <th>{tx("ui.ai_provider_usage")}</th>
              <th>{tx("ui.actions")}</th>
            </tr></thead>
            <tbody>
              {channels.flatMap((channel) => [
                ...(channel.error || channel.storage_error ? [
                  <tr key={`${channel.kind}-issue`} className="ai-provider-channel-issue">
                    <td colSpan={9}>
                      <strong>{tx(channelLabelKey(channel.kind))}</strong>{" "}
                      <span>{tx(channel.storage_error ? "ui.ai_provider_storage_unavailable" : channel.error === "provider_channel_response_invalid" ? "ui.ai_provider_response_invalid" : "ui.ai_provider_channel_unavailable")}</span>
                    </td>
                  </tr>,
                ] : []),
                ...channel.entries.map((entry) => (
                <tr key={`${channel.kind}-${entry.index}`}>
                  <td><strong>{tx(channelLabelKey(channel.kind))}</strong></td>
                  <td>{entry.name || entry.workspace_id || maskSecret(entry.api_key) || `#${entry.index + 1}`}</td>
                  <td className="ai-provider-table-url">{entry.base_url || entry.workspace_id || "-"}</td>
                  <td className="ai-provider-table-secret">{maskSecret(entry.api_key) || (channel.kind === "opencode-go" ? tx("ui.opencode_auth_cookie") : channel.kind === "opencode-zen" ? (entry.key_set ? "••••" : "-") : "-")}</td>
                  <td>{entry.disabled ? <span className="ai-provider-entry-badge is-disabled">{tx("ui.disabled")}</span> : <span className="ai-provider-entry-badge">{tx("ui.enabled")}</span>}</td>
                  <td>{entry.models?.length ?? 0}</td>
                  {(() => {
                    const runtime = runtimeForEntry(entry);
                    return <>
                      {(() => {
                        const policy = providerPolicyFor(channel.kind, entry);
                        const configuredLimit = policy?.concurrency_limit;
                        // Prefer an explicit plugin policy (including 0 for
                        // unlimited); otherwise surface CPA's observed limit
                        // when the runtime reports one.
                        const effectiveLimit = configuredLimit !== undefined ? configuredLimit : runtime?.limit ?? 0;
                        const concurrency = runtime?.supported
                          ? `${runtime.active}/${effectiveLimit > 0 ? effectiveLimit : "∞"}`
                          : configuredLimit === undefined || configuredLimit === 0 ? "-" : `0/${configuredLimit}`;
                        const budget = (window: ProviderQuotaPolicy["five_hour"], usedTokens: number | undefined) => {
                          if (!window.total_tokens || window.total_tokens <= 0) return window.limit_percent === undefined ? "-" : `— / ${window.limit_percent}%`;
                          if (usedTokens === undefined) return `— / ${window.limit_percent ?? 100}%`;
                          return `${Math.min(999, usedTokens / window.total_tokens * 100).toFixed(1)}% / ${window.limit_percent ?? 100}%`;
                        };
                        return <>
                          <td title={runtime?.supported && runtime.concurrency_configurable === true ? (runtimeUpdatedAt ? `${tx("ui.ai_provider_updated_at")}: ${runtimeUpdatedAt}` : tx("ui.ai_provider_concurrency_observable_only")) : tx("ui.ai_provider_concurrency_observable_only")}>
                            <strong>{concurrency}</strong>{configuredLimit !== undefined ? <small> · {tx("ui.ai_provider_limit")}</small> : null}
                          </td>
                          <td title={tx("ui.ai_provider_quota_settings_description")}>
                            {runtime ? <><strong>{formatTokens(runtime.total_tokens)}</strong><br /><small>{formatAmount(runtime.amount_usd)} · {tx("ui.quota_window_five_hour")} {budget(policy?.five_hour ?? {}, runtime.quota?.five_hour_used_tokens)} · {tx("ui.quota_window_seven_day")} {budget(policy?.seven_day ?? {}, runtime.quota?.seven_day_used_tokens)}</small></> : <span title={runtimeError || tx("ui.ai_provider_identity_unavailable")}>{tx("ui.ai_provider_no_usage")}</span>}
                          </td>
                        </>;
                      })()}
                    </>;
                  })()}
                  <td className="ai-provider-table-actions">
                    <IconButton label={tx("ui.view_ai_provider", { name: entry.name || entry.workspace_id || `#${entry.index + 1}` })} onClick={() => { setError(""); setViewing({ kind: channel.kind, entry }); }}><Eye size={15} /></IconButton>
                    <IconButton label={tx("ui.test_ai_provider", { name: entry.name || entry.workspace_id || `#${entry.index + 1}` })} disabled={channel.kind === "opencode-go" || busy} onClick={() => void testChannel(entry, channel.kind)}><Activity size={15} /></IconButton>
                    <IconButton label={tx("ui.edit_ai_provider")} onClick={() => { const policy = providerPolicyFor(channel.kind, entry); setEditing({ kind: channel.kind, index: entry.index, quotaPolicyKey: policy?.key ?? providerPolicyKey(channel.kind, entry), name: entry.name ?? entry.workspace_id ?? "", baseURL: entry.base_url ?? "", apiKey: "", disabled: entry.disabled === true, prefix: entry.prefix ?? "", priority: entry.priority !== undefined ? String(entry.priority) : "", weight: entry.weight !== undefined && entry.weight !== null ? String(entry.weight) : "", proxyURL: entry.proxy_url ?? "", headersText: mapToHeadersText(entry.headers), excludedText: arrayToList(entry.excluded_models), models: (entry.models ?? []).map((model) => ({ ...model })), apiKeyEntries: apiKeyEntriesForEditing(entry), apiKeyEntriesDirty: false, supportPromptCacheKey: entry.support_prompt_cache_key === true, disableCooling: entry.disable_cooling === true, requestRetry: entry.request_retry !== undefined && entry.request_retry !== null ? String(entry.request_retry) : "", requestScopedErrorsText: requestScopedErrorsToText(entry.request_scoped_errors), alphaSearch: entry.alpha_search === true, websockets: entry.websockets === true, rebuildMidSystemMessage: entry.rebuild_mid_system_message === true, fingerprintProfile: entry.fingerprint_profile ?? "", accountID: entry.account_id, workspaceID: entry.workspace_id, concurrencyLimit: policy?.concurrency_limit === undefined ? "" : String(policy.concurrency_limit), fiveHourTotalTokens: policy?.five_hour.total_tokens === undefined ? "" : String(policy.five_hour.total_tokens), fiveHourLimitPercent: policy?.five_hour.limit_percent === undefined ? "" : String(policy.five_hour.limit_percent), sevenDayTotalTokens: policy?.seven_day.total_tokens === undefined ? "" : String(policy.seven_day.total_tokens), sevenDayLimitPercent: policy?.seven_day.limit_percent === undefined ? "" : String(policy.seven_day.limit_percent) }); }}><Save size={15} /></IconButton>
                    {channel.kind !== "opencode-go" && channel.kind !== "opencode-zen" ? (
                      <>
                        <IconButton label={tx("ui.enable_ai_provider")} disabled={busy || !entry.disabled} onClick={() => void toggleEnabled(entry, channel.kind, true)}><Power size={15} /></IconButton>
                        <IconButton label={tx("ui.disable_ai_provider")} disabled={busy || entry.disabled === true} onClick={() => void toggleEnabled(entry, channel.kind, false)}><PowerOff size={15} /></IconButton>
                      </>
                    ) : null}
                    <IconButton className="button-danger" label={tx("ui.delete_ai_provider")} onClick={() => void deleteEntry(entry, channel.kind)}><Trash2 size={15} /></IconButton>
                  </td>
                </tr>
                )),
              ])}
            </tbody>
          </table>
          {channels.every((channel) => channel.entries.length === 0 && !channel.error && !channel.storage_error) ? <div className="ai-provider-empty">{tx("ui.ai_provider_channel_empty")}</div> : null}
        </div>
      )}
    </section>
  );
}

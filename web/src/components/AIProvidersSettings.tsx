import { Activity, Eye, EyeOff, LoaderCircle, Plus, Power, PowerOff, RefreshCw, Save, Trash2 } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import * as api from "../api/client";
import { operatorMessage } from "../format/operatorMessage";
import { useI18n } from "../i18n";
import type { UIMessageKey } from "../i18n/uiText";
import type {
  AIProviderAPIKeyEntry,
  AIProviderChannelEntry,
  AIProviderChannelKind,
  AIProviderChannelModel,
  AIProviderChannelSnapshot,
  AIProviderRuntimeSnapshot,
} from "../types";
import { IconButton } from "./IconButton";
import { Modal } from "./Modal";

interface AIProvidersSettingsProps {
  refreshRevision: number;
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
}

type AddKind = AIProviderChannelKind;

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
  apiKeyEntries: Array<{ apiKey: string; weight: string; proxyURL: string }>;
  supportPromptCacheKey: boolean;
  disableCooling: boolean;
  alphaSearch: boolean;
  websockets: boolean;
  rebuildMidSystemMessage: boolean;
  accountID?: string;
  workspaceID?: string;
};

function maskSecret(value: string | undefined): string {
  const trimmed = (value ?? "").trim();
  if (!trimmed) return "";
  if (trimmed.length <= 8) return "••••••••";
  return `${trimmed.slice(0, 4)}••••${trimmed.slice(-4)}`;
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

function listToArray(text: string): string[] {
  return text
    .split(/[\r\n,]+/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function arrayToList(items: string[] | undefined): string {
  return (items ?? []).join("\n");
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
    set({ apiKeyEntries });
  };
  const addKeyEntryRow = () => set({ apiKeyEntries: [...entry.apiKeyEntries, { apiKey: "", weight: "", proxyURL: "" }] });
  const removeKeyEntryRow = (index: number) => set({ apiKeyEntries: entry.apiKeyEntries.filter((_, i) => i !== index) });

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
            onChange={(event) => set({ apiKey: event.target.value })}
            type={showSecret ? "text" : "password"}
            placeholder={tx("ui.ai_provider_key_placeholder")}
            autoComplete="off"
          />
          <button type="button" aria-label={tx(showSecret ? "ui.hide_key" : "ui.show_key")} title={tx(showSecret ? "ui.hide_key" : "ui.show_key")} onClick={onToggleSecret}>
            {showSecret ? <EyeOff size={16} /> : <Eye size={16} />}
          </button>
        </div>
        <span className="ai-provider-field-note">{tx("ui.ai_provider_edit_key_note")}</span>
      </label>
      <label className="field-block">
        <span>{tx("ui.ai_provider_base_url")}</span>
        <input value={entry.baseURL} onChange={(event) => set({ baseURL: event.target.value })} placeholder="https://api.example.com/v1" autoComplete="off" />
      </label>
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
              <button className="button-danger" type="button" aria-label={tx("ui.remove")} onClick={() => removeKeyEntryRow(index)}><Trash2 size={14} /></button>
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
  const { locale, tx } = useI18n();
  const [channels, setChannels] = useState<AIProviderChannelSnapshot[]>([]);
  const [runtimeSnapshots, setRuntimeSnapshots] = useState<AIProviderRuntimeSnapshot[]>([]);
  const [runtimeUpdatedAt, setRuntimeUpdatedAt] = useState("");
  const [runtimeError, setRuntimeError] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const [addKind, setAddKind] = useState<AddKind>("openai-compatibility");
  const [editing, setEditing] = useState<EditingEntry | null>(null);
  const [viewing, setViewing] = useState<{ kind: AIProviderChannelKind; entry: AIProviderChannelEntry } | null>(null);
  const [testing, setTesting] = useState<{ kind: AIProviderChannelKind; index: number; label: string } | null>(null);
  const [testResult, setTestResult] = useState<api.AIProviderProbeResult | null>(null);
  const [newName, setNewName] = useState("");
  const [newBaseURL, setNewBaseURL] = useState("");
  const [newAPIKey, setNewAPIKey] = useState("");
  const [newWorkspace, setNewWorkspace] = useState("");
  const [newCookie, setNewCookie] = useState("");
  const [showSecret, setShowSecret] = useState(false);

  const handleError = useCallback((caught: unknown) => {
    if (caught instanceof api.APIError && caught.status === 401) {
      onAPIError(caught);
      return;
    }
    setError(operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale));
  }, [locale, onAPIError, tx]);

  const refresh = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      setChannels(await api.listAIProviderChannels());
    } catch (caught) {
      handleError(caught);
    } finally {
      setLoading(false);
    }
  }, [handleError]);

  const refreshRuntime = useCallback(async () => {
    try {
      const runtime = await api.getAIProviderRuntime();
      setRuntimeSnapshots(runtime.snapshots ?? []);
      setRuntimeUpdatedAt(runtime.updated_at ?? "");
      setRuntimeError("");
    } catch (caught) {
      // Runtime metrics are observability only; never block provider editing.
      setRuntimeError(caught instanceof Error ? caught.message : tx("ui.ai_provider_runtime_unavailable"));
    }
  }, [tx]);

  useEffect(() => { void refresh(); }, [refresh, refreshRevision]);
  useEffect(() => {
    void refreshRuntime();
    const timer = window.setInterval(() => void refreshRuntime(), 5000);
    return () => window.clearInterval(timer);
  }, [refreshRuntime, refreshRevision]);

  const runtimeForEntry = (entry: AIProviderChannelEntry): AIProviderRuntimeSnapshot | undefined => {
    const authIndex = (entry.auth_index ?? "").trim();
    if (!authIndex) return undefined;
    return runtimeSnapshots.find((snapshot) => snapshot.auth_index === authIndex);
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
      if (!newBaseURL.trim() || !newAPIKey.trim()) return;
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
          await api.patchAIProviderChannelEntry(editing.kind, editing.index, { "api-key": editing.apiKey.trim() });
        }
        onNotice(tx("ui.ai_provider_saved"));
      } else {
        const patch: api.AIProviderChannelEntryPatch = {};
        if (editing.name.trim()) patch.name = editing.name.trim();
        if (editing.baseURL.trim()) patch.base_url = editing.baseURL.trim();
        if (editing.apiKey.trim()) patch.api_key = editing.apiKey.trim();
        if (editing.disabled) patch.disabled = editing.disabled;
        if (editing.prefix.trim()) patch.prefix = editing.prefix.trim();
        if (editing.priority.trim()) patch.priority = editing.priority.trim();
        if (editing.weight.trim()) patch.weight = editing.weight.trim();
        if (editing.proxyURL.trim()) patch.proxy_url = editing.proxyURL.trim();
        const headers = headersToMap(editing.headersText);
        if (headers) patch.headers = headers;
        if (editing.kind !== "openai-compatibility") {
          const excluded = listToArray(editing.excludedText);
          if (excluded.length > 0) patch.excluded_models = excluded;
          patch.disable_cooling = editing.disableCooling;
        }
        if (editing.kind === "openai-compatibility") {
          patch.support_prompt_cache_key = editing.supportPromptCacheKey;
          const apiKeyEntries = editing.apiKeyEntries
            .map((keyEntry) => ({ apiKey: keyEntry.apiKey.trim(), weight: keyEntry.weight.trim(), proxyURL: keyEntry.proxyURL.trim() }))
            .filter((keyEntry) => keyEntry.apiKey);
          if (apiKeyEntries.length > 0) patch.api_key_entries = apiKeyEntries;
        }
        if (editing.kind === "codex-api-key" || editing.kind === "xai-api-key") patch.alpha_search = editing.alphaSearch;
        if (editing.kind === "codex-api-key") patch.websockets = editing.websockets;
        if (editing.kind === "claude-api-key") patch.rebuild_mid_system_message = editing.rebuildMidSystemMessage;
        if (hasModelEditor(editing.kind)) {
          const models = editing.models
            .map((model) => ({
              name: model.name.trim(),
              ...(model.alias && model.alias.trim() ? { alias: model.alias.trim() } : {}),
              ...(model.display_name && model.display_name.trim() ? { display_name: model.display_name.trim() } : {}),
              ...(model.max_context_length !== undefined && model.max_context_length > 0 ? { max_context_length: model.max_context_length } : {}),
              ...(model.force_mapping ? { force_mapping: true } : {}),
              ...(model.is_compat ? { is_compat: true } : {}),
              ...(model.image ? { image: true } : {}),
              ...(model.input_modalities && model.input_modalities.length > 0 ? { input_modalities: model.input_modalities } : {}),
              ...(model.output_modalities && model.output_modalities.length > 0 ? { output_modalities: model.output_modalities } : {}),
            }))
            .filter((model) => model.name.trim());
          if (models.length > 0) patch.models = models;
        }
        await api.saveAIProviderChannelEntry(editing.kind, editing.index, patch);
        onNotice(tx("ui.ai_provider_saved"));
      }
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
    if (kind !== "opencode-zen" && !entry.base_url) return;
    if (kind === "opencode-zen" && !entry.account_id) return;
    setBusy(true);
    setError("");
    setTestResult(null);
    setTesting({ kind, index: entry.index, label: entry.name || entry.base_url || `#${entry.index + 1}` });
    try {
      const result = kind === "opencode-zen"
        ? (await api.probeOpenCodeZenAccount(entry.account_id as string)).result
        : await api.testAIProviderChannel(entry.base_url ?? "", entry.api_key ?? "");
      setTestResult(result);
    } catch (caught) {
      handleError(caught);
    } finally {
      setBusy(false);
    }
  };

  const closeTest = () => {
    setTesting(null);
    setTestResult(null);
  };

  const addFormValid = addKind === "opencode-go"
    ? Boolean(newWorkspace.trim() && newCookie.trim())
    : addKind === "opencode-zen"
      ? Boolean(newBaseURL.trim() && newAPIKey.trim())
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
              if (!runtime) return <div className="ai-provider-detail-row"><span>{tx("ui.ai_provider_usage")}</span><strong>{tx("ui.ai_provider_identity_unavailable")}</strong></div>;
              return (
                <div className="ai-provider-runtime-detail">
                  <div className="ai-provider-detail-row"><span>{tx("ui.ai_provider_concurrency")}</span><strong>{runtime.limit > 0 ? `${runtime.active}/${runtime.limit}` : `${runtime.active}/∞`}</strong></div>
                  <div className="ai-provider-detail-row"><span>{tx("ui.ai_provider_total_tokens")}</span><strong>{formatTokens(runtime.total_tokens)}</strong></div>
                  <div className="ai-provider-detail-row"><span>{tx("ui.ai_provider_estimated_cost")}</span><strong>{formatAmount(runtime.amount_usd)}</strong></div>
                  {runtime.models && runtime.models.length > 0 ? (
                    <table className="account-table ai-provider-runtime-models"><thead><tr><th>{tx("ui.ai_provider_model")}</th><th>{tx("ui.ai_provider_input_tokens")}</th><th>{tx("ui.ai_provider_output_tokens")}</th><th>{tx("ui.ai_provider_total")}</th><th>{tx("ui.ai_provider_cost")}</th></tr></thead><tbody>{runtime.models.map((model) => <tr key={model.model}><td>{model.model}</td><td>{formatTokens(model.input_tokens)}</td><td>{formatTokens(model.output_tokens)}</td><td>{formatTokens(model.total_tokens)}</td><td>{formatAmount(model.amount_usd)}</td></tr>)}</tbody></table>
                  ) : <div className="ai-provider-field-note">{tx("ui.ai_provider_no_usage")}</div>}
                </div>
              );
            })()}
          </div>
        </Modal>
      ) : null}

      {testing ? (
        <Modal title={tx("ui.test_ai_provider_title", { name: testing.label })} onClose={closeTest} footer={(
          <button className="button" type="button" onClick={closeTest}>{tx("ui.close")}</button>
        )}>
          {!testResult ? (
            <div className="ai-provider-test-running" role="status"><LoaderCircle className="spin" size={20} /><span>{tx("ui.testing_ai_provider")}</span></div>
          ) : (
            <div className={`ai-provider-test-result ${testResult.reachable ? "is-reachable" : "is-failed"}`} role="status">
              <strong>{testResult.reachable ? tx("ui.ai_provider_test_ok") : tx("ui.ai_provider_test_failed")}</strong>
              <span>{testResult.detail || (testResult.status_code ? `HTTP ${testResult.status_code}` : "")}</span>
            </div>
          )}
        </Modal>
      ) : null}

      {loading ? (
        <div className="ai-provider-table-wrap ai-provider-loading" aria-label={tx("ui.loading")}>
          <LoaderCircle className="spin" size={24} />
        </div>
      ) : (
        <div className="ai-provider-table-wrap">
          <table className="account-table ai-provider-table">
            <thead><tr><th>{tx("ui.ai_provider_type")}</th><th>{tx("ui.ai_provider_name")}</th><th>{tx("ui.ai_provider_base_url")}</th><th>{tx("ui.ai_provider_api_key")}</th><th>{tx("ui.status")}</th><th>{tx("ui.models")}</th><th>{tx("ui.ai_provider_concurrency")}</th><th>{tx("ui.ai_provider_usage")}</th><th>{tx("ui.actions")}</th></tr></thead>
            <tbody>
              {channels.flatMap((channel) => channel.entries.map((entry) => (
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
                      <td title={runtimeUpdatedAt ? `${tx("ui.ai_provider_updated_at")}: ${runtimeUpdatedAt}` : undefined}>{runtime ? (runtime.limit > 0 ? `${runtime.active}/${runtime.limit}` : `${runtime.active}/∞`) : "-"}</td>
                      <td>{runtime ? <><strong>{formatTokens(runtime.total_tokens)}</strong><br /><small>{formatAmount(runtime.amount_usd)}</small></> : <span title={runtimeError || tx("ui.ai_provider_identity_unavailable")}>{tx("ui.ai_provider_no_usage")}</span>}</td>
                    </>;
                  })()}
                  <td className="ai-provider-table-actions">
                    <IconButton label={tx("ui.view_ai_provider", { name: entry.name || entry.workspace_id || `#${entry.index + 1}` })} onClick={() => { setError(""); setViewing({ kind: channel.kind, entry }); }}><Eye size={15} /></IconButton>
                    <IconButton label={tx("ui.test_ai_provider", { name: entry.name || entry.workspace_id || `#${entry.index + 1}` })} disabled={!entry.base_url || busy} onClick={() => void testChannel(entry, channel.kind)}><Activity size={15} /></IconButton>
                    <IconButton label={tx("ui.edit_ai_provider")} onClick={() => setEditing({ kind: channel.kind, index: entry.index, name: entry.name ?? entry.workspace_id ?? "", baseURL: entry.base_url ?? "", apiKey: "", disabled: entry.disabled === true, prefix: entry.prefix ?? "", priority: entry.priority !== undefined ? String(entry.priority) : "", weight: entry.weight !== undefined && entry.weight !== null ? String(entry.weight) : "", proxyURL: entry.proxy_url ?? "", headersText: mapToHeadersText(entry.headers), excludedText: arrayToList(entry.excluded_models), models: (entry.models ?? []).map((model) => ({ ...model })), apiKeyEntries: (entry.api_key_entries ?? []).map((keyEntry) => ({ apiKey: keyEntry.api_key ?? "", weight: keyEntry.weight !== undefined && keyEntry.weight !== null ? String(keyEntry.weight) : "", proxyURL: keyEntry.proxy_url ?? "" })), supportPromptCacheKey: entry.support_prompt_cache_key === true, disableCooling: entry.disable_cooling === true, alphaSearch: entry.alpha_search === true, websockets: entry.websockets === true, rebuildMidSystemMessage: entry.rebuild_mid_system_message === true, accountID: entry.account_id, workspaceID: entry.workspace_id })}><Save size={15} /></IconButton>
                    {channel.kind !== "opencode-go" && channel.kind !== "opencode-zen" ? (
                      <>
                        <IconButton label={tx("ui.enable_ai_provider")} disabled={busy || !entry.disabled} onClick={() => void toggleEnabled(entry, channel.kind, true)}><Power size={15} /></IconButton>
                        <IconButton label={tx("ui.disable_ai_provider")} disabled={busy || entry.disabled === true} onClick={() => void toggleEnabled(entry, channel.kind, false)}><PowerOff size={15} /></IconButton>
                      </>
                    ) : null}
                    <IconButton className="button-danger" label={tx("ui.delete_ai_provider")} onClick={() => void deleteEntry(entry, channel.kind)}><Trash2 size={15} /></IconButton>
                  </td>
                </tr>
              )))}
            </tbody>
          </table>
          {channels.every((channel) => channel.entries.length === 0) ? <div className="ai-provider-empty">{tx("ui.ai_provider_channel_empty")}</div> : null}
        </div>
      )}
    </section>
  );
}

import { Eye, EyeOff, LoaderCircle, Plus, RefreshCw, Save, Trash2, X } from "lucide-react";
import { useCallback, useEffect, useState, type FormEvent } from "react";
import * as api from "../api/client";
import { operatorMessage } from "../format/operatorMessage";
import { useI18n } from "../i18n";
import type { UIMessageKey } from "../i18n/uiText";
import type { AIProviderChannelKind, AIProviderChannelSnapshot } from "../types";

interface AIProvidersSettingsProps {
  refreshRevision: number;
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
}

type EditingEntry = {
  kind: AIProviderChannelKind;
  index: number;
  name: string;
  baseURL: string;
  apiKey: string;
  disabled: boolean;
};

function maskSecret(value: string | undefined): string {
  const trimmed = (value ?? "").trim();
  if (!trimmed) return "";
  if (trimmed.length <= 8) return "••••••••";
  return `${trimmed.slice(0, 4)}••••${trimmed.slice(-4)}`;
}

function channelLabelKey(kind: AIProviderChannelKind): UIMessageKey {
  return (api.AI_PROVIDER_CHANNELS.find((channel) => channel.kind === kind)?.labelKey ?? "ui.ai_provider_channel_openai_compatibility") as UIMessageKey;
}

export function AIProvidersSettings({ refreshRevision, onAPIError, onNotice }: AIProvidersSettingsProps) {
  const { locale, tx } = useI18n();
  const [channels, setChannels] = useState<AIProviderChannelSnapshot[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<EditingEntry | null>(null);
  const [newName, setNewName] = useState("");
  const [newBaseURL, setNewBaseURL] = useState("");
  const [newAPIKey, setNewAPIKey] = useState("");
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

  useEffect(() => { void refresh(); }, [refresh, refreshRevision]);

  const resetForm = () => {
    setAdding(false);
    setEditing(null);
    setNewName("");
    setNewBaseURL("");
    setNewAPIKey("");
    setShowSecret(false);
  };

  const submitNewProvider = async (event: FormEvent) => {
    event.preventDefault();
    if (busy || !newName.trim() || !newBaseURL.trim() || !newAPIKey.trim()) return;
    setBusy(true);
    setError("");
    try {
      await api.addOpenAICompatibilityProvider({ name: newName, base_url: newBaseURL, api_key: newAPIKey });
      onNotice(tx("ui.ai_provider_added"));
      resetForm();
      await refresh();
    } catch (caught) {
      handleError(caught);
    } finally {
      setBusy(false);
    }
  };

  const submitEdit = async (event: FormEvent) => {
    event.preventDefault();
    if (busy || !editing) return;
    setBusy(true);
    setError("");
    try {
      const value: Record<string, unknown> = {};
      if (editing.kind === "openai-compatibility") value["disabled"] = editing.disabled;
      if (editing.name.trim()) value["name"] = editing.name.trim();
      if (editing.baseURL.trim()) value["base-url"] = editing.baseURL.trim();
      if (editing.apiKey.trim()) {
        value[editing.kind === "openai-compatibility" ? "api-key-entries" : "api-key"] =
          editing.kind === "openai-compatibility" ? [{ "api-key": editing.apiKey.trim() }] : editing.apiKey.trim();
      }
      await api.patchAIProviderChannelEntry(editing.kind, editing.index, value);
      onNotice(tx("ui.ai_provider_saved"));
      resetForm();
      await refresh();
    } catch (caught) {
      handleError(caught);
    } finally {
      setBusy(false);
    }
  };

  const deleteEntry = async (kind: AIProviderChannelKind, index: number, name: string) => {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      await api.deleteAIProviderChannelEntry(kind, index);
      onNotice(tx("ui.ai_provider_deleted"));
      await refresh();
    } catch (caught) {
      handleError(caught);
    } finally {
      setBusy(false);
    }
  };

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
          <button className="button button-primary" type="button" onClick={() => { setError(""); setAdding(true); }}>
            <Plus size={16} />{tx("ui.add_ai_provider")}
          </button>
        </div>
      </div>

      {error ? <div className="agent-login-error" role="alert">{error}</div> : null}

      {adding || editing ? (
        <form className="ai-provider-form" onSubmit={adding ? submitNewProvider : submitEdit}>
          <div className="ai-provider-form-title">
            <strong>{adding ? tx("ui.add_ai_provider") : tx("ui.edit_ai_provider")}</strong>
            <IconClose onClick={resetForm} label={tx("ui.cancel")} />
          </div>
          {adding ? (
            <label className="field-block">
              <span>{tx("ui.ai_provider_name")}</span>
              <input value={newName} onChange={(event) => setNewName(event.target.value)} autoFocus autoComplete="off" />
            </label>
          ) : null}
          <label className="field-block">
            <span>{tx("ui.ai_provider_base_url")}</span>
            <input
              value={adding ? newBaseURL : (editing?.baseURL ?? "")}
              onChange={(event) => (adding ? setNewBaseURL(event.target.value) : setEditing({ ...editing!, baseURL: event.target.value }))}
              placeholder="https://api.example.com/v1"
              autoComplete="off"
            />
          </label>
          <label className="field-block">
            <span>{tx("ui.ai_provider_api_key")}</span>
            <div className="secret-input">
              <input
                value={adding ? newAPIKey : (editing?.apiKey ?? "")}
                onChange={(event) => (adding ? setNewAPIKey(event.target.value) : setEditing({ ...editing!, apiKey: event.target.value }))}
                type={showSecret ? "text" : "password"}
                placeholder={editing ? maskSecret(undefined) : "sk-..."}
                autoComplete="off"
              />
              <button type="button" aria-label={tx(showSecret ? "ui.hide_key" : "ui.show_key")} title={tx(showSecret ? "ui.hide_key" : "ui.show_key")} onClick={() => setShowSecret((value) => !value)}>
                {showSecret ? <EyeOff size={16} /> : <Eye size={16} />}
              </button>
            </div>
            {editing ? <span className="ai-provider-field-note">{tx("ui.ai_provider_edit_key_note")}</span> : null}
          </label>
          {editing && editing.kind === "openai-compatibility" ? (
            <label className="switch-control ai-provider-switch">
              <input type="checkbox" checked={editing.disabled} onChange={(event) => setEditing({ ...editing, disabled: event.target.checked })} />
              <span>{tx("ui.ai_provider_disabled")}</span>
            </label>
          ) : null}
          <div className="settings-section-actions ai-provider-form-actions">
            <button className="button" type="button" onClick={resetForm} disabled={busy}><X size={16} />{tx("ui.cancel")}</button>
            <button className="button button-primary" type="submit" disabled={busy}>
              {busy ? <LoaderCircle className="spin" size={16} /> : <Save size={16} />}
              {tx(adding ? "ui.add_ai_provider_confirm" : "ui.save")}
            </button>
          </div>
        </form>
      ) : null}

      {loading ? (
        <div className="auth-loading" aria-label={tx("ui.loading")}><LoaderCircle className="spin" size={24} /></div>
      ) : (
        <div className="ai-providers-list">
          {channels.map((channel) => (
            <div className="ai-provider-channel" key={channel.kind}>
              <div className="ai-provider-channel-heading">
                <strong>{tx(channelLabelKey(channel.kind))}</strong>
                <span className="ai-provider-channel-count">{channel.count}</span>
              </div>
              {channel.entries.length === 0 ? (
                <div className="ai-provider-empty">{tx("ui.ai_provider_channel_empty")}</div>
              ) : (
                <div className="ai-provider-entries">
                  {channel.entries.map((entry) => (
                    <div className="ai-provider-entry" key={`${channel.kind}-${entry.index}`}>
                      <div className="ai-provider-entry-main">
                        <span className="ai-provider-entry-name">{entry.name || entry.base_url || maskSecret(entry.api_key) || `#${entry.index + 1}`}</span>
                        {entry.base_url ? <span className="ai-provider-entry-base">{entry.base_url}</span> : null}
                        <span className="ai-provider-entry-key">{maskSecret(entry.api_key)}</span>
                        {entry.disabled ? <span className="ai-provider-entry-badge is-disabled">{tx("ui.disabled")}</span> : null}
                        {entry.prefix ? <span className="ai-provider-entry-prefix">{entry.prefix}</span> : null}
                        {entry.models && entry.models.length > 0 ? <span className="ai-provider-entry-badge">{entry.models.length} {tx("ui.models")}</span> : null}
                      </div>
                      <div className="ai-provider-entry-actions">
                        <button
                          className="button button-small"
                          type="button"
                          disabled={busy}
                          onClick={() => setEditing({
                            kind: channel.kind,
                            index: entry.index,
                            name: entry.name ?? "",
                            baseURL: entry.base_url ?? "",
                            apiKey: "",
                            disabled: entry.disabled === true,
                          })}
                        >
                          <Save size={14} />{tx("ui.edit")}
                        </button>
                        <button
                          className="button button-small button-danger"
                          type="button"
                          disabled={busy}
                          onClick={() => void deleteEntry(channel.kind, entry.index, entry.name ?? entry.base_url ?? `#${entry.index + 1}`)}
                        >
                          <Trash2 size={14} />{tx("ui.delete")}
                        </button>
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

function IconClose({ onClick, label }: { onClick: () => void; label: string }) {
  return (
    <button className="button button-small" type="button" onClick={onClick} aria-label={label} title={label}>
      <X size={14} />
    </button>
  );
}

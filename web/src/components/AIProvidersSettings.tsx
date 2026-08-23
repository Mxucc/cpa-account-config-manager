import { Eye, EyeOff, LoaderCircle, Plus, RefreshCw, Save, Trash2 } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import * as api from "../api/client";
import { operatorMessage } from "../format/operatorMessage";
import { useI18n } from "../i18n";
import type { UIMessageKey } from "../i18n/uiText";
import type { AIProviderChannelEntry, AIProviderChannelKind, AIProviderChannelSnapshot } from "../types";
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
  accountID?: string;
  workspaceID?: string;
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
  const [addKind, setAddKind] = useState<AddKind>("openai-compatibility");
  const [editing, setEditing] = useState<EditingEntry | null>(null);
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

  useEffect(() => { void refresh(); }, [refresh, refreshRevision]);

  const resetForm = () => {
    setAdding(false);
    setEditing(null);
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
      } else {
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

  const addFormValid = addKind === "opencode-go"
    ? Boolean(newWorkspace.trim() && newCookie.trim())
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
            ) : (
              <>
                {editing.kind === "openai-compatibility" ? (
                  <label className="field-block">
                    <span>{tx("ui.ai_provider_name")}</span>
                    <input value={editing.name} onChange={(event) => setEditing({ ...editing, name: event.target.value })} autoComplete="off" />
                  </label>
                ) : null}
                <label className="field-block">
                  <span>{tx("ui.ai_provider_base_url")}</span>
                  <input value={editing.baseURL} onChange={(event) => setEditing({ ...editing, baseURL: event.target.value })} placeholder="https://api.example.com/v1" autoComplete="off" />
                </label>
                <label className="field-block">
                  <span>{tx("ui.ai_provider_api_key")}</span>
                  <div className="secret-input">
                    <input
                      value={editing.apiKey}
                      onChange={(event) => setEditing({ ...editing, apiKey: event.target.value })}
                      type={showSecret ? "text" : "password"}
                      placeholder={editing.apiKey ? undefined : maskSecret(undefined)}
                      autoComplete="off"
                    />
                    <button type="button" aria-label={tx(showSecret ? "ui.hide_key" : "ui.show_key")} title={tx(showSecret ? "ui.hide_key" : "ui.show_key")} onClick={() => setShowSecret((value) => !value)}>
                      {showSecret ? <EyeOff size={16} /> : <Eye size={16} />}
                    </button>
                  </div>
                  {editing.kind === "openai-compatibility" ? <span className="ai-provider-field-note">{tx("ui.ai_provider_edit_key_note")}</span> : null}
                </label>
                {editing.kind === "openai-compatibility" ? (
                  <label className="switch-control ai-provider-switch">
                    <input type="checkbox" checked={editing.disabled} onChange={(event) => setEditing({ ...editing, disabled: event.target.checked })} />
                    <span>{tx("ui.ai_provider_disabled")}</span>
                  </label>
                ) : null}
              </>
            )}
          </div>
        </Modal>
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
                        {entry.workspace_id && entry.name !== entry.workspace_id ? <span className="ai-provider-entry-base">{entry.workspace_id}</span> : null}
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
                            name: entry.name ?? entry.workspace_id ?? "",
                            baseURL: entry.base_url ?? "",
                            apiKey: "",
                            disabled: entry.disabled === true,
                            accountID: entry.account_id,
                            workspaceID: entry.workspace_id,
                          })}
                        >
                          <Save size={14} />{tx("ui.edit")}
                        </button>
                        <button
                          className="button button-small button-danger"
                          type="button"
                          disabled={busy}
                          onClick={() => void deleteEntry(entry, channel.kind)}
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

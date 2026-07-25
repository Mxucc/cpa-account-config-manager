import { Eye, EyeOff, LoaderCircle, Plus, RefreshCw, Search, Trash2 } from "lucide-react";
import { useMemo, useState, type FormEvent } from "react";
import type { AccountModelCatalogResponse, BatchPatch, ModelPolicyMode } from "../types";
import { IconButton } from "./IconButton";
import { Modal } from "./Modal";
import { useI18n } from "../i18n";
import type { UIMessageKey } from "../i18n/uiText";

type FieldName = "disabled" | "priority" | "note" | "prefix" | "proxy_url" | "websockets" | "headers" | "model_policy";

interface HeaderRow {
  id: number;
  action: "set" | "remove";
  name: string;
  value: string;
}

interface BatchEditorProps {
  title?: UIMessageKey;
  scopeLabel: string;
  onClose: () => void;
  onSubmit: (patch: BatchPatch) => void;
	loadModels: () => Promise<AccountModelCatalogResponse>;
}

const initialEnabled: Record<FieldName, boolean> = {
  disabled: false,
  priority: false,
  note: false,
  prefix: false,
  proxy_url: false,
  websockets: false,
  headers: false,
	model_policy: false,
};

export function BatchEditor({ title = "ui.batch_edit", scopeLabel, onClose, onSubmit, loadModels }: BatchEditorProps) {
  const { locale, tx } = useI18n();
  const [enabled, setEnabled] = useState(initialEnabled);
  const [disabled, setDisabled] = useState(false);
  const [priority, setPriority] = useState("0");
  const [note, setNote] = useState("");
  const [prefix, setPrefix] = useState("");
  const [proxyURL, setProxyURL] = useState("");
  const [showProxy, setShowProxy] = useState(false);
  const [websockets, setWebsockets] = useState(false);
  const [headers, setHeaders] = useState<HeaderRow[]>([{ id: 1, action: "set", name: "", value: "" }]);
  const [error, setError] = useState("");
	const [modelCatalog, setModelCatalog] = useState<AccountModelCatalogResponse | null>(null);
	const [modelCatalogLoading, setModelCatalogLoading] = useState(false);
	const [modelCatalogError, setModelCatalogError] = useState(false);
	const [modelMode, setModelMode] = useState<ModelPolicyMode>("all");
	const [selectedModels, setSelectedModels] = useState<Set<string>>(new Set());
	const [modelSearch, setModelSearch] = useState("");

  const anyEnabled = useMemo(() => Object.values(enabled).some(Boolean), [enabled]);
  const toggle = (field: FieldName) => setEnabled((current) => ({ ...current, [field]: !current[field] }));
	const visibleModels = useMemo(() => {
		const query = modelSearch.trim().toLowerCase();
		if (!query) return modelCatalog?.models ?? [];
		return (modelCatalog?.models ?? []).filter((model) => `${model.id}\n${model.display_name ?? ""}\n${model.owned_by ?? ""}`.toLowerCase().includes(query));
	}, [modelCatalog, modelSearch]);

	const fetchModels = async () => {
		setModelCatalogLoading(true);
		setModelCatalogError(false);
		try {
			const catalog = await loadModels();
			setModelCatalog(catalog);
			if (catalog.current_policy) {
				setModelMode(catalog.current_policy.mode);
				setSelectedModels(new Set(catalog.current_policy.models ?? []));
			}
		} catch {
			setModelCatalogError(true);
		} finally {
			setModelCatalogLoading(false);
		}
	};

	const toggleModelPolicy = () => {
		const nextEnabled = !enabled.model_policy;
		setEnabled((current) => ({ ...current, model_policy: nextEnabled }));
		if (nextEnabled && !modelCatalog && !modelCatalogLoading) void fetchModels();
	};

	const toggleModel = (modelID: string) => {
		setSelectedModels((current) => {
			const next = new Set(current);
			if (next.has(modelID)) next.delete(modelID);
			else next.add(modelID);
			return next;
		});
	};

  const updateHeader = (id: number, update: Partial<HeaderRow>) => {
    setHeaders((rows) => rows.map((row) => row.id === id ? { ...row, ...update } : row));
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const patch: BatchPatch = {};
    if (enabled.disabled) patch.disabled = disabled;
    if (enabled.priority) {
      if (!/^-?\d+$/.test(priority.trim())) {
        setError(tx("ui.priority_must_be_an_integer"));
        return;
      }
      patch.priority = Number(priority);
    }
    if (enabled.note) patch.note = note;
    if (enabled.prefix) patch.prefix = prefix;
    if (enabled.proxy_url) patch.proxy_url = proxyURL;
    if (enabled.websockets) patch.websockets = websockets;
    if (enabled.headers) {
      const set: Record<string, string> = {};
      const remove: string[] = [];
      const seen = new Set<string>();
      for (const row of headers) {
        const name = row.name.trim();
        if (name === "") continue;
        const key = name.toLowerCase();
        if (seen.has(key)) {
          setError(tx("ui.header_name_is_duplicated", { name }));
          return;
        }
        seen.add(key);
        if (row.action === "remove") remove.push(name);
        else if (row.value.trim() === "") {
          setError(tx("ui.header_name_has_no_value", { name }));
          return;
        } else set[name] = row.value;
      }
      if (Object.keys(set).length === 0 && remove.length === 0) {
        setError(tx("ui.add_at_least_one_header_operation"));
        return;
      }
      patch.headers = {
        ...(Object.keys(set).length > 0 ? { set } : {}),
        ...(remove.length > 0 ? { remove } : {}),
      };
    }
		if (enabled.model_policy) {
			if (!modelCatalog) {
				setError(tx("ui.load_models_before_submitting"));
				return;
			}
			const models = Array.from(selectedModels).sort((left, right) => left.localeCompare(right));
			if (modelMode !== "all" && models.length === 0) {
				setError(tx("ui.select_at_least_one_model"));
				return;
			}
			patch.model_policy = { mode: modelMode, ...(modelMode === "all" ? {} : { models }) };
		}
    setError("");
    onSubmit(patch);
  };

  return (
    <Modal
      title={tx(title)}
      wide
      onClose={onClose}
      footer={(
        <>
          <span className="modal-scope">{scopeLabel}</span>
          <button className="button" type="button" onClick={onClose}>{tx("ui.cancel")}</button>
          <button className="button button-primary" type="submit" form="batch-editor" disabled={!anyEnabled}>{tx("ui.generate_preview")}</button>
        </>
      )}
    >
      <form id="batch-editor" className="batch-editor" onSubmit={submit}>
        <EditRow checked={enabled.disabled} label={tx("ui.enabled_state")} onToggle={() => toggle("disabled")}>
          <select value={disabled ? "disabled" : "enabled"} onChange={(event) => setDisabled(event.target.value === "disabled")} disabled={!enabled.disabled} aria-label={tx("ui.enabled_state_value")}>
            <option value="enabled">{tx("ui.enable")}</option>
            <option value="disabled">{tx("ui.disable")}</option>
          </select>
        </EditRow>
        <EditRow checked={enabled.priority} label={tx("ui.priority")} onToggle={() => toggle("priority")}>
          <input value={priority} onChange={(event) => setPriority(event.target.value)} inputMode="numeric" disabled={!enabled.priority} aria-label={tx("ui.priority_value")} />
        </EditRow>
        <EditRow checked={enabled.note} label={tx("ui.note")} onToggle={() => toggle("note")}>
          <input value={note} onChange={(event) => setNote(event.target.value)} maxLength={2000} disabled={!enabled.note} aria-label={tx("ui.note_value")} />
        </EditRow>
        <EditRow checked={enabled.prefix} label={tx("ui.prefix")} onToggle={() => toggle("prefix")}>
          <input value={prefix} onChange={(event) => setPrefix(event.target.value)} maxLength={256} disabled={!enabled.prefix} aria-label={tx("ui.prefix_value")} />
        </EditRow>
        <EditRow checked={enabled.proxy_url} label={tx("ui.proxy_url")} onToggle={() => toggle("proxy_url")}>
          <div className="secret-input editor-secret">
            <input value={proxyURL} onChange={(event) => setProxyURL(event.target.value)} type={showProxy ? "text" : "password"} disabled={!enabled.proxy_url} aria-label={tx("ui.proxy_url_value")} />
            <button type="button" aria-label={tx(showProxy ? "ui.hide_proxy" : "ui.show_proxy")} title={tx(showProxy ? "ui.hide_proxy" : "ui.show_proxy")} onClick={() => setShowProxy((value) => !value)} disabled={!enabled.proxy_url}>
              {showProxy ? <EyeOff size={16} /> : <Eye size={16} />}
            </button>
          </div>
        </EditRow>
        <EditRow checked={enabled.websockets} label={tx("ui.websockets")} onToggle={() => toggle("websockets")}>
          <label className="switch-control">
            <input type="checkbox" checked={websockets} onChange={(event) => setWebsockets(event.target.checked)} disabled={!enabled.websockets} aria-label={tx("ui.websockets_value")} />
            <span>{tx(websockets ? "ui.on_2" : "ui.off_2")}</span>
          </label>
        </EditRow>
        <div className={`edit-row edit-row-headers ${enabled.headers ? "is-enabled" : ""}`}>
          <label className="edit-optin">
            <input type="checkbox" checked={enabled.headers} onChange={() => toggle("headers")} />
            <span>{tx("ui.headers")}</span>
          </label>
          <div className="header-editor">
            {headers.map((row) => (
              <div className="header-row" key={row.id}>
                <select value={row.action} onChange={(event) => updateHeader(row.id, { action: event.target.value as HeaderRow["action"] })} disabled={!enabled.headers} aria-label={tx("ui.header_action")}>
                  <option value="set">{tx("ui.set")}</option>
                  <option value="remove">{tx("ui.remove")}</option>
                </select>
                <input value={row.name} onChange={(event) => updateHeader(row.id, { name: event.target.value })} placeholder="Header-Name" disabled={!enabled.headers} aria-label={tx("ui.header_name")} />
                <input value={row.value} onChange={(event) => updateHeader(row.id, { value: event.target.value })} placeholder={row.action === "remove" ? "-" : "Value"} type="password" disabled={!enabled.headers || row.action === "remove"} aria-label={tx("ui.header_value")} />
                <IconButton label={tx("ui.delete_header_row")} disabled={!enabled.headers || headers.length === 1} onClick={() => setHeaders((items) => items.filter((item) => item.id !== row.id))}><Trash2 size={15} /></IconButton>
              </div>
            ))}
            <button className="button button-quiet header-add" type="button" disabled={!enabled.headers} onClick={() => setHeaders((rows) => [...rows, { id: Math.max(...rows.map((row) => row.id), 0) + 1, action: "set", name: "", value: "" }])}>
              <Plus size={15} /> {tx("ui.header")}
            </button>
          </div>
        </div>
		<div className={`edit-row edit-row-models ${enabled.model_policy ? "is-enabled" : ""}`}>
			<label className="edit-optin">
				<input type="checkbox" checked={enabled.model_policy} onChange={toggleModelPolicy} />
				<span>{tx("ui.model_policy")}</span>
			</label>
			<div className="model-policy-editor">
				<div className="model-policy-modes" role="group" aria-label={tx("ui.model_policy_mode")}>
					{(["all", "allow_only", "deny_only"] as ModelPolicyMode[]).map((mode) => (
						<button key={mode} type="button" className={modelMode === mode ? "active" : ""} disabled={!enabled.model_policy || modelCatalogLoading} onClick={() => setModelMode(mode)}>
							{tx(mode === "all" ? "ui.all_models" : mode === "allow_only" ? "ui.model_allowlist" : "ui.model_blocklist")}
						</button>
					))}
				</div>
				{modelCatalogLoading ? (
					<div className="model-catalog-state"><LoaderCircle className="spin" size={16} />{tx("ui.loading_models")}</div>
				) : modelCatalogError ? (
					<div className="model-catalog-state is-error"><span>{tx("ui.models_could_not_be_loaded")}</span><button className="button button-quiet" type="button" onClick={() => void fetchModels()}><RefreshCw size={14} />{tx("ui.retry")}</button></div>
				) : modelCatalog ? (
					<>
						<div className="model-catalog-summary">
							<span>{tx("ui.common_models_count", { count: modelCatalog.models.length })}</span>
							<span>{tx("ui.model_catalog_loaded_count", { loaded: modelCatalog.loaded, total: modelCatalog.eligible })}</span>
							{modelCatalog.failed > 0 ? <span className="is-warning">{tx("ui.model_catalog_failed_count", { count: modelCatalog.failed })}</span> : null}
						</div>
						{modelMode !== "all" ? (
							<>
								<div className="model-list-tools">
									<label className="model-search"><Search size={14} /><input value={modelSearch} onChange={(event) => setModelSearch(event.target.value)} placeholder={tx("ui.search_models")} aria-label={tx("ui.search_models")} /></label>
									<button className="button button-quiet" type="button" onClick={() => setSelectedModels(new Set(modelCatalog.models.map((model) => model.id)))}>{tx("ui.select_all")}</button>
									<button className="button button-quiet" type="button" onClick={() => setSelectedModels(new Set())}>{tx("ui.clear")}</button>
								</div>
								<div className="model-option-list" role="group" aria-label={tx("ui.available_models")}>
									{visibleModels.map((model) => (
										<label className="model-option" key={model.id}>
											<input type="checkbox" checked={selectedModels.has(model.id)} onChange={() => toggleModel(model.id)} />
											<span><strong>{model.display_name || model.id}</strong>{model.display_name && model.display_name !== model.id ? <code>{model.id}</code> : null}</span>
											{model.owned_by ? <small>{model.owned_by}</small> : null}
										</label>
									))}
									{visibleModels.length === 0 ? <div className="model-list-empty">{tx("ui.no_matching_models")}</div> : null}
								</div>
							</>
						) : <p className="model-policy-help">{tx("ui.all_models_policy_help")}</p>}
					</>
				) : <div className="model-catalog-state">{tx("ui.enable_model_policy_to_load")}</div>}
			</div>
		</div>
        {error ? <div className="form-error" role="alert">{error}</div> : null}
      </form>
    </Modal>
  );
}

function EditRow({ checked, label, onToggle, children }: { checked: boolean; label: string; onToggle: () => void; children: React.ReactNode }) {
  return (
    <div className={`edit-row ${checked ? "is-enabled" : ""}`}>
      <label className="edit-optin">
        <input type="checkbox" checked={checked} onChange={onToggle} />
        <span>{label}</span>
      </label>
      <div className="edit-control">{children}</div>
    </div>
  );
}

import { Eye, EyeOff, Plus, Trash2 } from "lucide-react";
import { useMemo, useState, type FormEvent } from "react";
import type { BatchPatch } from "../types";
import { IconButton } from "./IconButton";
import { Modal } from "./Modal";

type FieldName = "disabled" | "priority" | "note" | "prefix" | "proxy_url" | "websockets" | "headers";

interface HeaderRow {
  id: number;
  action: "set" | "remove";
  name: string;
  value: string;
}

interface BatchEditorProps {
  title?: string;
  scopeLabel: string;
  onClose: () => void;
  onSubmit: (patch: BatchPatch) => void;
}

const initialEnabled: Record<FieldName, boolean> = {
  disabled: false,
  priority: false,
  note: false,
  prefix: false,
  proxy_url: false,
  websockets: false,
  headers: false,
};

export function BatchEditor({ title = "批量编辑", scopeLabel, onClose, onSubmit }: BatchEditorProps) {
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

  const anyEnabled = useMemo(() => Object.values(enabled).some(Boolean), [enabled]);
  const toggle = (field: FieldName) => setEnabled((current) => ({ ...current, [field]: !current[field] }));

  const updateHeader = (id: number, update: Partial<HeaderRow>) => {
    setHeaders((rows) => rows.map((row) => row.id === id ? { ...row, ...update } : row));
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const patch: BatchPatch = {};
    if (enabled.disabled) patch.disabled = disabled;
    if (enabled.priority) {
      if (!/^-?\d+$/.test(priority.trim())) {
        setError("Priority 必须是整数");
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
          setError(`Header ${name} 重复`);
          return;
        }
        seen.add(key);
        if (row.action === "remove") remove.push(name);
        else if (row.value.trim() === "") {
          setError(`Header ${name} 缺少值`);
          return;
        } else set[name] = row.value;
      }
      if (Object.keys(set).length === 0 && remove.length === 0) {
        setError("至少添加一个 Header 操作");
        return;
      }
      patch.headers = {
        ...(Object.keys(set).length > 0 ? { set } : {}),
        ...(remove.length > 0 ? { remove } : {}),
      };
    }
    setError("");
    onSubmit(patch);
  };

  return (
    <Modal
      title={title}
      wide
      onClose={onClose}
      footer={(
        <>
          <span className="modal-scope">{scopeLabel}</span>
          <button className="button" type="button" onClick={onClose}>取消</button>
          <button className="button button-primary" type="submit" form="batch-editor" disabled={!anyEnabled}>生成预览</button>
        </>
      )}
    >
      <form id="batch-editor" className="batch-editor" onSubmit={submit}>
        <EditRow checked={enabled.disabled} label="启用状态" onToggle={() => toggle("disabled")}>
          <select value={disabled ? "disabled" : "enabled"} onChange={(event) => setDisabled(event.target.value === "disabled")} disabled={!enabled.disabled} aria-label="启用状态值">
            <option value="enabled">启用</option>
            <option value="disabled">禁用</option>
          </select>
        </EditRow>
        <EditRow checked={enabled.priority} label="Priority" onToggle={() => toggle("priority")}>
          <input value={priority} onChange={(event) => setPriority(event.target.value)} inputMode="numeric" disabled={!enabled.priority} aria-label="Priority 值" />
        </EditRow>
        <EditRow checked={enabled.note} label="Note" onToggle={() => toggle("note")}>
          <input value={note} onChange={(event) => setNote(event.target.value)} maxLength={2000} disabled={!enabled.note} aria-label="Note 值" />
        </EditRow>
        <EditRow checked={enabled.prefix} label="Prefix" onToggle={() => toggle("prefix")}>
          <input value={prefix} onChange={(event) => setPrefix(event.target.value)} maxLength={256} disabled={!enabled.prefix} aria-label="Prefix 值" />
        </EditRow>
        <EditRow checked={enabled.proxy_url} label="Proxy URL" onToggle={() => toggle("proxy_url")}>
          <div className="secret-input editor-secret">
            <input value={proxyURL} onChange={(event) => setProxyURL(event.target.value)} type={showProxy ? "text" : "password"} disabled={!enabled.proxy_url} aria-label="Proxy URL 值" />
            <button type="button" aria-label={showProxy ? "隐藏代理" : "显示代理"} title={showProxy ? "隐藏代理" : "显示代理"} onClick={() => setShowProxy((value) => !value)} disabled={!enabled.proxy_url}>
              {showProxy ? <EyeOff size={16} /> : <Eye size={16} />}
            </button>
          </div>
        </EditRow>
        <EditRow checked={enabled.websockets} label="WebSockets" onToggle={() => toggle("websockets")}>
          <label className="switch-control">
            <input type="checkbox" checked={websockets} onChange={(event) => setWebsockets(event.target.checked)} disabled={!enabled.websockets} aria-label="WebSockets 值" />
            <span>{websockets ? "开启" : "关闭"}</span>
          </label>
        </EditRow>
        <div className={`edit-row edit-row-headers ${enabled.headers ? "is-enabled" : ""}`}>
          <label className="edit-optin">
            <input type="checkbox" checked={enabled.headers} onChange={() => toggle("headers")} />
            <span>Headers</span>
          </label>
          <div className="header-editor">
            {headers.map((row) => (
              <div className="header-row" key={row.id}>
                <select value={row.action} onChange={(event) => updateHeader(row.id, { action: event.target.value as HeaderRow["action"] })} disabled={!enabled.headers} aria-label="Header 操作">
                  <option value="set">设置</option>
                  <option value="remove">移除</option>
                </select>
                <input value={row.name} onChange={(event) => updateHeader(row.id, { name: event.target.value })} placeholder="Header-Name" disabled={!enabled.headers} aria-label="Header 名称" />
                <input value={row.value} onChange={(event) => updateHeader(row.id, { value: event.target.value })} placeholder={row.action === "remove" ? "-" : "Value"} type="password" disabled={!enabled.headers || row.action === "remove"} aria-label="Header 值" />
                <IconButton label="删除 Header 行" disabled={!enabled.headers || headers.length === 1} onClick={() => setHeaders((items) => items.filter((item) => item.id !== row.id))}><Trash2 size={15} /></IconButton>
              </div>
            ))}
            <button className="button button-quiet header-add" type="button" disabled={!enabled.headers} onClick={() => setHeaders((rows) => [...rows, { id: Math.max(...rows.map((row) => row.id), 0) + 1, action: "set", name: "", value: "" }])}>
              <Plus size={15} /> Header
            </button>
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

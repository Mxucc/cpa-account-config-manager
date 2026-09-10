import { useI18n } from "../i18n";
import type { ExperimentalCodexIdentitySettings } from "../types";

interface CodexIdentityPolicyEditorProps {
  value: ExperimentalCodexIdentitySettings;
  disabled?: boolean;
  onChange: (patch: Partial<ExperimentalCodexIdentitySettings>) => void;
}

/**
 * The single editor for global Codex client identity policy. It lives under
 * Other settings so outbound convergence and the official-client ingress gate
 * have exactly one source; automation policies only inherit it, and individual
 * accounts or providers may still override it explicitly.
 */
export function CodexIdentityPolicyEditor({ value, disabled = false, onChange }: CodexIdentityPolicyEditorProps) {
  const { tx } = useI18n();
  return (
    <div className="codex-identity-global-editor">
      <div className="codex-identity-toggle-grid">
        <label className="switch-control">
          <input type="checkbox" checked={value.outbound_convergence_enabled} disabled={disabled} onChange={(event) => onChange({ outbound_convergence_enabled: event.target.checked })} />
          <span><b>{tx(value.outbound_convergence_enabled ? "ui.on_2" : "ui.off_2")}</b><small>{tx("ui.codex_outbound_convergence")}</small></span>
        </label>
        <label className="switch-control">
          <input type="checkbox" checked={value.ingress_gate_enabled} disabled={disabled} onChange={(event) => onChange({ ingress_gate_enabled: event.target.checked })} />
          <span><b>{tx(value.ingress_gate_enabled ? "ui.on_2" : "ui.off_2")}</b><small>{tx("ui.codex_ingress_gate")}</small></span>
        </label>
        <label className="switch-control">
          <input type="checkbox" checked={value.allow_app_server_clients} disabled={disabled} onChange={(event) => onChange({ allow_app_server_clients: event.target.checked })} />
          <span><b>{tx(value.allow_app_server_clients ? "ui.on_2" : "ui.off_2")}</b><small>{tx("ui.codex_allow_app_server")}</small></span>
        </label>
      </div>
      <div className="codex-identity-runtime-grid">
        <label className="filter-control">
          <span>{tx("ui.codex_convergence_mode")}</span>
          <select value={value.convergence_mode ?? ""} disabled={disabled} onChange={(event) => onChange({ convergence_mode: event.target.value })}>
            <option value="">{tx("ui.codex_convergence_legacy_full")}</option>
            <option value="off">{tx("ui.codex_convergence_off")}</option>
            <option value="device">{tx("ui.codex_convergence_device")}</option>
            <option value="session">{tx("ui.codex_convergence_session")}</option>
            <option value="full">{tx("ui.codex_convergence_full")}</option>
          </select>
        </label>
        <label className="filter-control">
          <span>{tx("ui.codex_min_version")}</span>
          <input value={value.min_version ?? ""} disabled={disabled} onChange={(event) => onChange({ min_version: event.target.value })} />
        </label>
        <label className="filter-control">
          <span>{tx("ui.codex_max_version")}</span>
          <input value={value.max_version ?? ""} disabled={disabled} onChange={(event) => onChange({ max_version: event.target.value })} />
        </label>
      </div>
      <div className="codex-identity-json-grid">
        <label className="codex-policy-field">
          <span>{tx("ui.codex_whitelist_json")}</span>
          <textarea rows={2} disabled={disabled} value={value.whitelist ?? ""} onChange={(event) => onChange({ whitelist: event.target.value })} />
        </label>
        <label className="codex-policy-field">
          <span>{tx("ui.codex_blacklist_json")}</span>
          <textarea rows={2} disabled={disabled} value={value.blacklist ?? ""} onChange={(event) => onChange({ blacklist: event.target.value })} />
        </label>
        <label className="codex-policy-field">
          <span>{tx("ui.codex_fingerprint_json")}</span>
          <textarea rows={2} disabled={disabled} value={value.fingerprint_signals ?? ""} onChange={(event) => onChange({ fingerprint_signals: event.target.value })} />
        </label>
      </div>
      <p className="ai-provider-field-note">{tx("ui.codex_identity_single_source_note")}</p>
    </div>
  );
}

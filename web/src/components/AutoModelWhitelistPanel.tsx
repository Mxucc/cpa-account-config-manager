import { AlertTriangle, LoaderCircle, RefreshCw, ShieldCheck } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import * as api from "../api/client";
import { operatorMessage } from "../format/operatorMessage";
import { useI18n } from "../i18n";
import type { AutoModelWhitelistSnapshot } from "../types";
import { modelPolicyReasonLabels } from "./ModelTestDialog";

interface AutoModelWhitelistPanelProps {
  /** Bumped by the settings card when it is reloaded or the allow-list toggle was saved. */
  refreshRevision: number;
  onAPIError: (error: unknown) => void;
}

/**
 * Read-only activity of the experimental automatic Codex model allow-list. The panel owns its
 * own loading, empty and error states so an unavailable endpoint never breaks the settings card.
 */
export function AutoModelWhitelistPanel({ refreshRevision, onAPIError }: AutoModelWhitelistPanelProps) {
  const { locale, tx, formatDateTime } = useI18n();
  const [snapshot, setSnapshot] = useState<AutoModelWhitelistSnapshot | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const loadRequest = useRef(0);

  const load = useCallback(async (signal?: AbortSignal) => {
    const requestID = ++loadRequest.current;
    setLoading(true);
    setError("");
    try {
      const response = await api.getAutoModelWhitelist(signal);
      if (signal?.aborted || requestID !== loadRequest.current) return;
      setSnapshot(response.auto_model_whitelist ?? null);
    } catch (caught) {
      if (signal?.aborted || requestID !== loadRequest.current) return;
      if (caught instanceof api.APIError && caught.status === 401) {
        onAPIError(caught);
        return;
      }
      const fallback = tx("ui.auto_model_whitelist_load_failed");
      setError((caught instanceof Error ? operatorMessage(caught.message, locale) : "") || fallback);
    } finally {
      if (!signal?.aborted && requestID === loadRequest.current) setLoading(false);
    }
  }, [locale, onAPIError, tx]);

  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => {
      loadRequest.current += 1;
      controller.abort();
    };
  }, [load, refreshRevision]);

  const recent = snapshot?.recent ?? [];
  return (
    <section className="settings-section auto-model-whitelist-panel" aria-label={tx("ui.codex_auto_model_whitelist")}>
      <header>
        <ShieldCheck size={18} />
        <div>
          <strong>{tx("ui.codex_auto_model_whitelist")}</strong>
          <span>{tx("ui.auto_model_whitelist_recent_detections")}</span>
        </div>
        <button className="button button-quiet" type="button" disabled={loading} onClick={() => void load()}>
          <RefreshCw className={loading ? "spin" : ""} size={15} />{tx("ui.refresh")}
        </button>
      </header>
      {error ? <div className="automation-error" role="alert"><AlertTriangle size={16} /><span>{error}</span><button type="button" onClick={() => setError("")}>{tx("ui.close")}</button></div> : null}
      {snapshot ? (
        <div className="experimental-behavior-list">
          <div><strong>{tx("ui.enabled")}</strong><span>{tx(snapshot.enabled ? "ui.enabled" : "ui.disabled")}</span></div>
          <div><strong>{tx("ui.status")}</strong><span>{tx("ui.auto_model_whitelist_panel_summary", { limited: snapshot.limited, accounts: snapshot.accounts })}</span></div>
          <div><strong>{tx("ui.auto_model_whitelist_last_detected")}</strong><span>{formatDateTime(snapshot.last_detected_at)}</span></div>
        </div>
      ) : null}
      {snapshot && recent.length > 0 ? (
        <ul className="auto-model-whitelist-list" aria-label={tx("ui.auto_model_whitelist_recent_detections")}>
          {recent.map((entry, index) => (
            <li key={`${entry.account_id || "account"}:${entry.at || ""}:${index}`}>
              <div className="auto-model-whitelist-row-heading">
                <span className={`attempt-status ${entry.status === "applied" ? "attempt-status-available" : "attempt-status-review"}`}>
                  {tx(entry.status === "applied" ? "ui.model_allow_list_applied" : "ui.model_allow_list_not_applied")}
                </span>
                <strong>{entry.label?.trim() || entry.account_id || tx("ui.unknown")}</strong>
              </div>
              <div className="auto-model-whitelist-row-detail">
                <span>{entry.reason_code ? tx(modelPolicyReasonLabels[entry.reason_code] ?? "ui.unknown") : tx("ui.unknown")}</span>
                <time>{formatDateTime(entry.at)}</time>
              </div>
            </li>
          ))}
        </ul>
      ) : null}
      {!snapshot || recent.length === 0 ? (
        <p className="auto-model-whitelist-empty" role="status">
          {loading ? <LoaderCircle className="spin" size={16} /> : null}
          {snapshot ? tx("ui.auto_model_whitelist_empty") : loading ? tx("ui.loading") : tx("ui.no_data")}
        </p>
      ) : null}
    </section>
  );
}

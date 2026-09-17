import { AlertTriangle, LoaderCircle, RotateCw, Save } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import * as api from "../api/client";
import { operatorMessage } from "../format/operatorMessage";
import { useI18n } from "../i18n";
import type { AutoRetryAppliedState, AutoRetryHostState, AutoRetrySnapshot } from "../types";

interface AutoRetryCardProps {
  /** Bumped by the surrounding settings surface so the card reloads with its neighbours. */
  refreshRevision: number;
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
}

const MIN_ATTEMPTS = 0;
const DEFAULT_ATTEMPTS = 5;
const DEFAULT_MAX_ATTEMPTS = 10;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function numberOr(value: unknown, fallback: number): number {
  return typeof value === "number" && Number.isFinite(value) ? value : fallback;
}

function normalizeHost(value: unknown): AutoRetryHostState | undefined {
  if (!isRecord(value)) return undefined;
  const host: AutoRetryHostState = {
    request_retry: numberOr(value.request_retry, 0),
    max_retry_interval: numberOr(value.max_retry_interval, 0),
    max_retry_credentials: numberOr(value.max_retry_credentials, 0),
    configured: value.configured === true,
  };
  // bootstrap_retries is the one host field older runtimes may not report at all.
  if (typeof value.bootstrap_retries === "number" && Number.isFinite(value.bootstrap_retries)) {
    host.bootstrap_retries = value.bootstrap_retries;
  }
  return host;
}

function normalizeApplied(value: unknown): AutoRetryAppliedState | undefined {
  if (!isRecord(value)) return undefined;
  return {
    codex_accounts: numberOr(value.codex_accounts, 0),
    codex_channels: numberOr(value.codex_channels, 0),
    opencode_channels: numberOr(value.opencode_channels, 0),
    cline_pass_channels: numberOr(value.cline_pass_channels, 0),
    skipped: numberOr(value.skipped, 0),
    host_request_retry_raised: value.host_request_retry_raised === true,
    host_interval_raised: value.host_interval_raised === true,
    ...(typeof value.updated_at === "string" && value.updated_at.trim() ? { updated_at: value.updated_at } : {}),
  };
}

/**
 * Normalizes the auto-retry payload. The backend omits `host`, `applied` and
 * single counters depending on runtime version, so an absent number reads as 0
 * and an absent object reads as "not reported yet" instead of as an error.
 */
function normalizeAutoRetry(value: unknown): AutoRetrySnapshot {
  const source = isRecord(value) ? value : {};
  const maxAttempts = Math.max(1, numberOr(source.max_attempts, DEFAULT_MAX_ATTEMPTS));
  return {
    attempts: Math.min(Math.max(numberOr(source.attempts, DEFAULT_ATTEMPTS), MIN_ATTEMPTS), maxAttempts),
    default_attempts: numberOr(source.default_attempts, DEFAULT_ATTEMPTS),
    max_attempts: maxAttempts,
    enabled: source.enabled === true,
    host: normalizeHost(source.host),
    applied: normalizeApplied(source.applied),
    storage_error: typeof source.storage_error === "string" ? source.storage_error : "",
  };
}

/**
 * Operator card for the transparent automatic retry budget. The plugin retries
 * failed upstream requests itself, so the card only reports host prerequisites
 * and the credentials that already carry the setting; nothing here is a request
 * failure, and a raised host switch is a neutral, expected note.
 */
export function AutoRetryCard({ refreshRevision, onAPIError, onNotice }: AutoRetryCardProps) {
  const { locale, tx } = useI18n();
  const [snapshot, setSnapshot] = useState<AutoRetrySnapshot | null>(null);
  const [draft, setDraft] = useState(String(DEFAULT_ATTEMPTS));
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const loadRequest = useRef(0);

  const load = useCallback(async (signal?: AbortSignal) => {
    const requestID = ++loadRequest.current;
    setLoading(true);
    setError("");
    try {
      const next = normalizeAutoRetry(await api.getAutoRetry(signal));
      if (signal?.aborted || requestID !== loadRequest.current) return;
      setSnapshot(next);
      setDraft(String(next.attempts));
    } catch (caught) {
      if (signal?.aborted || requestID !== loadRequest.current) return;
      if (caught instanceof api.APIError && caught.status === 401) {
        onAPIError(caught);
        return;
      }
      const fallback = tx("ui.auto_retry_load_failed");
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

  const maxAttempts = snapshot?.max_attempts ?? DEFAULT_MAX_ATTEMPTS;
  const draftValue = Number(draft);
  const draftValid = draft.trim() !== "" && Number.isInteger(draftValue) && draftValue >= MIN_ATTEMPTS && draftValue <= maxAttempts;
  const hostRaised = snapshot?.applied
    ? snapshot.applied.host_request_retry_raised === true || snapshot.applied.host_interval_raised === true
    : false;

  const save = async () => {
    if (!snapshot || !draftValid || saving) return;
    setSaving(true);
    setError("");
    try {
      // The PUT answer is the refreshed snapshot: the card never reloads the page.
      const next = normalizeAutoRetry(await api.saveAutoRetry(draftValue));
      setSnapshot(next);
      setDraft(String(next.attempts));
      onNotice(tx("ui.auto_retry_saved"));
    } catch (caught) {
      if (caught instanceof api.APIError && caught.status === 401) {
        onAPIError(caught);
        return;
      }
      const fallback = tx("ui.auto_retry_save_failed");
      setError((caught instanceof Error ? operatorMessage(caught.message, locale) : "") || fallback);
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="experimental-feature-block">
      <div className="experimental-feature-row">
        <div className="experimental-feature-copy">
          <span className="experimental-feature-icon"><RotateCw size={18} /></span>
          <div>
            <strong>{tx("ui.auto_retry_title")}</strong>
            <span>{tx("ui.auto_retry_description")}</span>
          </div>
        </div>
        <span className="number-suffix">
          <input
            type="number"
            min={MIN_ATTEMPTS}
            max={maxAttempts}
            step={1}
            value={draft}
            disabled={loading || saving || snapshot === null}
            onChange={(event) => setDraft(event.target.value)}
            aria-label={tx("ui.auto_retry_attempts")}
          />
          <b>{tx("ui.auto_retry_attempts_unit")}</b>
        </span>
      </div>

      {error ? (
        <div className="automation-error" role="alert">
          <AlertTriangle size={16} />
          <span>{error}</span>
          <button type="button" onClick={() => void load()}>{tx("ui.refresh")}</button>
        </div>
      ) : null}

      {snapshot ? (
        <div className="experimental-behavior-list">
          <div>
            <strong>{tx("ui.auto_retry_attempts")}</strong>
            <span>{tx("ui.auto_retry_attempts_hint", { max: maxAttempts })}</span>
            <span>
              {draftValid
                ? draftValue === MIN_ATTEMPTS
                  ? tx("ui.auto_retry_disabled_state")
                  : tx("ui.auto_retry_enabled_state", { value: draftValue })
                : tx("ui.auto_retry_attempts_invalid", { max: maxAttempts })}
            </span>
          </div>
          <div>
            <strong>{tx("ui.auto_retry_host_state")}</strong>
            {snapshot.host ? (
              <>
                <span>{tx("ui.auto_retry_host_request_retry", { value: snapshot.host.request_retry })}</span>
                <span>{tx("ui.auto_retry_host_max_interval", { value: snapshot.host.max_retry_interval })}</span>
                <span>{tx("ui.auto_retry_host_max_credentials", { value: snapshot.host.max_retry_credentials })}</span>
                {typeof snapshot.host.bootstrap_retries === "number" ? (
                  <span>{tx("ui.auto_retry_host_bootstrap_retries", { value: snapshot.host.bootstrap_retries })}</span>
                ) : null}
              </>
            ) : (
              <span>{tx("ui.auto_retry_not_reported")}</span>
            )}
            {hostRaised ? <span>{tx("ui.auto_retry_host_raised")}</span> : null}
          </div>
          <div>
            <strong>{tx("ui.auto_retry_applied")}</strong>
            {snapshot.applied ? (
              <>
                <span>{tx("ui.auto_retry_applied_codex", { value: (snapshot.applied.codex_accounts ?? 0) + (snapshot.applied.codex_channels ?? 0) })}</span>
                <span>{tx("ui.auto_retry_applied_opencode", { value: snapshot.applied.opencode_channels ?? 0 })}</span>
                <span>{tx("ui.auto_retry_applied_cline_pass", { value: snapshot.applied.cline_pass_channels ?? 0 })}</span>
                {(snapshot.applied.skipped ?? 0) > 0 ? (
                  <span>{tx("ui.auto_retry_applied_skipped", { value: snapshot.applied.skipped ?? 0 })}</span>
                ) : null}
              </>
            ) : (
              <span>{tx("ui.auto_retry_not_reported")}</span>
            )}
          </div>
        </div>
      ) : null}

      {!snapshot && !error ? (
        <p className="auto-model-whitelist-empty" role="status">
          {loading ? <LoaderCircle className="spin" size={16} /> : null}
          {loading ? tx("ui.loading") : tx("ui.no_data")}
        </p>
      ) : null}

      <div className="settings-section-actions experimental-actions">
        <button className="button button-primary" type="button" disabled={loading || saving || !snapshot || !draftValid} onClick={() => void save()}>
          {saving ? <LoaderCircle className="spin" size={15} /> : <Save size={15} />}
          {tx("ui.auto_retry_save")}
        </button>
      </div>
    </div>
  );
}

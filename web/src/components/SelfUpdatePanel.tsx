import { AlertTriangle, CheckCircle2, DownloadCloud, HardDrive, LoaderCircle, RefreshCw, RotateCcw, Save, ShieldCheck } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import * as api from "../api/client";
import { operatorMessage } from "../format/operatorMessage";
import { useI18n } from "../i18n";
import type { UIMessageKey } from "../i18n/uiText";
import type { SelfUpdateReloadResult, SelfUpdateSnapshot } from "../types";

/**
 * Direct GitHub self-update. The plugin resolves and applies its own release, so an
 * installation whose CPA plugin-store request never succeeds can still update. The
 * replacement only takes effect after CPA restarts, because the host already mapped
 * the previous library into the running process.
 */

interface SelfUpdatePanelProps {
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
}

// Reloading swaps the plugin in place, so give the host time to answer again. Exported so a test
// can shorten the window instead of waiting for the real one.
export const reloadPollTiming = { intervalMS: 3000, windowMS: 90_000 };

/** Explains a refused reload in operator terms. */
const reloadReasonKeys: Record<string, UIMessageKey> = {
  plugin_store_unavailable: "ui.self_update_reload_store_unavailable",
  plugin_store_disabled: "ui.self_update_reload_store_disabled",
  plugin_not_in_store: "ui.self_update_reload_not_in_store",
  plugin_store_version_unknown: "ui.self_update_reload_store_version_unknown",
  plugin_store_version_is_older: "ui.self_update_reload_store_older",
  plugin_store_install_failed: "ui.self_update_reload_install_failed",
  cpa_management_api_unavailable: "ui.self_update_reload_management_unavailable",
  host_still_requires_a_restart: "ui.self_update_reload_restart_still_required",
  reload_not_confirmed: "ui.self_update_reload_not_confirmed",
};

/** Maps the resolution channel onto its catalog key. */
function sourceKey(source: SelfUpdateSnapshot["source"]): UIMessageKey {
  switch (source) {
    case "github_api":
      return "ui.self_update_source_api";
    case "github_redirect":
      return "ui.self_update_source_redirect";
    case "github_atom":
      return "ui.self_update_source_atom";
    default:
      return "ui.self_update_source_unknown";
  }
}

/** Maps the plugin-file discovery channel onto its catalog key. */
function pluginFileSourceKey(source: SelfUpdateSnapshot["plugin_file_source"]): UIMessageKey {
  switch (source) {
    case "setting":
      return "ui.self_update_file_source_setting";
    case "proc":
      return "ui.self_update_file_source_process";
    case "search":
      return "ui.self_update_file_source_search";
    default:
      return "ui.self_update_file_source_unknown";
  }
}

/** Renders a byte count without leaking an out-of-range value into the UI. */
function formatBytes(value: number | undefined): string {
  if (!Number.isFinite(value) || !value || value <= 0) return "-";
  const units = ["B", "KiB", "MiB", "GiB"];
  let size = value;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit += 1;
  }
  return `${size >= 10 || unit === 0 ? Math.round(size) : size.toFixed(1)} ${units[unit]}`;
}

export function SelfUpdatePanel({ onAPIError, onNotice }: SelfUpdatePanelProps) {
  const { locale, tx, formatDateTime } = useI18n();
  const [snapshot, setSnapshot] = useState<SelfUpdateSnapshot | null>(null);
  const [pluginFile, setPluginFile] = useState("");
  const [loading, setLoading] = useState(true);
  const [checking, setChecking] = useState(false);
  const [installing, setInstalling] = useState(false);
  const [saving, setSaving] = useState(false);
  const [reloading, setReloading] = useState(false);
  const [reloadResult, setReloadResult] = useState<SelfUpdateReloadResult | null>(null);
  const [error, setError] = useState("");
  const sequence = useRef(0);

  const handleError = useCallback((caught: unknown) => {
    if (caught instanceof api.APIError && caught.status === 401) {
      onAPIError(caught);
      return;
    }
    setError(operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale));
  }, [locale, onAPIError, tx]);

  const apply = useCallback((next: SelfUpdateSnapshot) => {
    setSnapshot(next);
    setPluginFile((current) => (current === "" ? next.plugin_file ?? "" : current));
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    // The initial read joins the same sequence as the buttons, so a slow first response
    // can never overwrite a newer snapshot the operator already produced.
    const current = ++sequence.current;
    const bootstrap = async () => {
      setLoading(true);
      try {
        const next = await api.getSelfUpdate(controller.signal);
        if (!controller.signal.aborted && current === sequence.current) apply(next);
      } catch (caught) {
        if (!controller.signal.aborted && current === sequence.current) handleError(caught);
      } finally {
        if (!controller.signal.aborted && current === sequence.current) setLoading(false);
      }
    };
    void bootstrap().catch(() => undefined);
    return () => {
      controller.abort();
      sequence.current += 1;
    };
  }, [apply, handleError]);

  const checkNow = useCallback(async () => {
    const current = ++sequence.current;
    setChecking(true);
    setError("");
    try {
      const next = await api.checkSelfUpdate();
      if (current !== sequence.current) return;
      apply(next);
      onNotice(next.update_available
        ? tx("ui.self_update_found_version", { version: next.latest_version || "-" })
        : tx("ui.self_update_is_current"));
    } catch (caught) {
      if (current === sequence.current) handleError(caught);
    } finally {
      if (current === sequence.current) setChecking(false);
    }
  }, [apply, handleError, onNotice, tx]);

  const installNow = useCallback(async () => {
    const current = ++sequence.current;
    setInstalling(true);
    setError("");
    try {
      const next = await api.installSelfUpdate();
      if (current !== sequence.current) return;
      apply(next);
      onNotice(tx("ui.self_update_applied_restart_required", { version: next.applied_version || next.latest_version || "-" }));
    } catch (caught) {
      if (current === sequence.current) handleError(caught);
    } finally {
      if (current === sequence.current) setInstalling(false);
    }
  }, [apply, handleError, onNotice, tx]);

  /** Maps a reload refusal onto the operator-facing explanation. */
  const handleReloadRefusal = useCallback((result: SelfUpdateReloadResult) => {
    const key = reloadReasonKeys[result.reason ?? ""];
    setReloadResult(result);
    onNotice(tx(key ?? "ui.self_update_reload_failed"));
  }, [onNotice, tx]);

  const savePath = useCallback(async () => {
    const current = ++sequence.current;
    setSaving(true);
    setError("");
    try {
      const next = await api.saveSelfUpdateSettings(pluginFile.trim());
      if (current !== sequence.current) return;
      apply(next);
      onNotice(tx("ui.self_update_path_saved"));
    } catch (caught) {
      if (current === sequence.current) handleError(caught);
    } finally {
      if (current === sequence.current) setSaving(false);
    }
  }, [apply, handleError, onNotice, pluginFile, tx]);

  /**
   * The reload replaces the running plugin, so the host may unload this instance while the call
   * is still open: a gateway then reports an invalid response (Cloudflare 502/524) even though
   * the swap happened. Polling the version is the only reliable signal, because the reloaded
   * instance answers with its own version.
   */
  const waitForReload = useCallback(async (previousVersion: string): Promise<boolean> => {
    const deadline = Date.now() + reloadPollTiming.windowMS;
    while (Date.now() < deadline) {
      await new Promise((resolve) => { setTimeout(resolve, reloadPollTiming.intervalMS); });
      try {
        const next = await api.getSelfUpdate();
        if (next.current_version && next.current_version !== previousVersion) {
          setSnapshot(next);
          setReloadResult({ reloaded: true, restart_required: false, store_version: next.current_version });
          onNotice(tx("ui.self_update_reloaded_refresh_page"));
          return true;
        }
      } catch {
        // The host is still swapping the plugin; keep polling until the window closes.
      }
    }
    return false;
  }, [onNotice, tx]);

  const reloadNow = useCallback(async () => {
    const current = ++sequence.current;
    const previousVersion = snapshot?.current_version ?? "";
    setReloading(true);
    setError("");
    setReloadResult(null);
    try {
      const result = await api.reloadSelfUpdateThroughStore();
      if (current !== sequence.current) return;
      setReloadResult(result);
      if (result.reloaded) {
        onNotice(tx("ui.self_update_reloaded_refresh_page"));
        return;
      }
      handleReloadRefusal(result);
    } catch {
      if (current !== sequence.current) return;
      // A closed connection is expected while the plugin is replaced, so ask the host instead of
      // reporting a failure the operator cannot act on.
      const reloaded = await waitForReload(previousVersion);
      if (current !== sequence.current) return;
      if (!reloaded) {
        setReloadResult({ reloaded: false, restart_required: true, reason: "reload_not_confirmed" });
        onNotice(tx("ui.self_update_reload_not_confirmed"));
      }
    } finally {
      if (current === sequence.current) setReloading(false);
    }
  }, [handleReloadRefusal, onNotice, snapshot?.current_version, tx, waitForReload]);

  const busy = loading || checking || installing || saving || reloading;
  const statusLabel = snapshot?.update_available
    ? tx("ui.version_version_available", { version: snapshot.latest_version || "-" })
    : snapshot?.latest_version
      ? tx("ui.self_update_is_current")
      : tx("ui.self_update_source_unknown");

  return (
    <section className="settings-section self-update-section" aria-label={tx("ui.self_update_title")}>
      <header>
        <DownloadCloud size={18} />
        <div>
          <strong>{tx("ui.self_update_title")}</strong>
          <span>{tx("ui.self_update_description")}</span>
        </div>
      </header>
      <div className="settings-version-grid">
        <div><span>{tx("ui.current_version")}</span><code>{snapshot?.current_version || "-"}</code></div>
        <div><span>{tx("ui.latest_version")}</span><code>{snapshot?.latest_version || "-"}</code></div>
        <div><span>{tx("ui.self_update_source")}</span><strong>{tx(sourceKey(snapshot?.source))}</strong></div>
        <div><span>{tx("ui.last_checked")}</span><time>{formatDateTime(snapshot?.checked_at)}</time></div>
        <div><span>{tx("ui.self_update_asset")}</span><code>{snapshot?.asset_name || "-"}</code></div>
        <div><span>{tx("ui.self_update_asset_size")}</span><code>{formatBytes(snapshot?.asset_bytes)}</code></div>
        <div><span>{tx("ui.self_update_library_file")}</span><code>{snapshot?.plugin_file || tx("ui.self_update_library_not_located")}</code></div>
        <div><span>{tx("ui.check_status")}</span><strong className={snapshot?.update_available ? "status-warning" : ""}>{loading ? tx("ui.loading") : statusLabel}</strong></div>
      </div>
      {snapshot?.plugin_file ? (
        <p className="self-update-hint">
          {tx("ui.self_update_library_detected_via", { source: tx(pluginFileSourceKey(snapshot.plugin_file_source)) })}
        </p>
      ) : (
        <p className="self-update-hint">{tx("ui.self_update_library_hint")}</p>
      )}
      {snapshot?.plugin_file && !snapshot.plugin_file_exists ? (
        <div className="experimental-storage-warning" role="alert"><AlertTriangle size={16} /><span>{tx("ui.self_update_library_missing")}</span></div>
      ) : null}
      {snapshot?.checksum_ok ? (
        <div className="self-update-verified" role="status"><CheckCircle2 size={16} /><span>{tx("ui.self_update_checksum_verified")}</span><code>{snapshot.archive_sha256}</code></div>
      ) : null}
      {snapshot?.applied_version ? (
        <div className="self-update-verified" role="status"><HardDrive size={16} /><span>{tx("ui.self_update_installed_version", { version: snapshot.applied_version })}</span></div>
      ) : null}
      {snapshot?.ui_updated ? (
        <div className="self-update-verified" role="status"><CheckCircle2 size={16} /><span>{tx("ui.self_update_interface_updated")}</span></div>
      ) : null}
      {snapshot?.restart_required ? (
        <div className="settings-update-callout" role="status"><RotateCcw size={18} /><strong>{tx("ui.self_update_restart_required")}</strong></div>
      ) : null}
      {snapshot?.backup_path ? (
        <p className="self-update-hint">{tx("ui.self_update_backup_at", { path: snapshot.backup_path })}</p>
      ) : null}
      {snapshot?.storage_error ? (
        <div className="experimental-storage-error" role="alert"><AlertTriangle size={16} /><span>{tx("ui.self_update_storage_error")}</span></div>
      ) : null}
      {snapshot?.error ? (
        <div className="experimental-storage-error" role="alert"><AlertTriangle size={16} /><span>{snapshot.error}</span></div>
      ) : null}
      {error ? <div className="experimental-storage-error" role="alert"><AlertTriangle size={16} /><span>{error}</span></div> : null}
      <div className="self-update-path-control">
        <label className="filter-control">
          <span>{tx("ui.self_update_library_file")}</span>
          <input type="text" value={pluginFile} disabled={saving} placeholder="/opt/cpa/plugins/cpa-account-config-manager.so" onChange={(event) => setPluginFile(event.target.value)} aria-label={tx("ui.self_update_library_file")} />
        </label>
        <button className="button button-quiet" type="button" disabled={saving || pluginFile.trim() === (snapshot?.plugin_file ?? "")} onClick={() => void savePath()}>
          {saving ? <LoaderCircle className="spin" size={15} /> : <Save size={15} />}{tx("ui.self_update_save_path")}
        </button>
      </div>
      {snapshot?.restart_required ? (
        <p className="self-update-hint">{tx("ui.self_update_reload_hint")}</p>
      ) : null}
      {reloading ? (
        <p className="self-update-hint" role="status">{tx("ui.self_update_reload_waiting")}</p>
      ) : null}
      <p className="self-update-hint">{tx("ui.self_update_restart_hint")}</p>
      {reloadResult && !reloadResult.reloaded ? (
        <p className="self-update-hint" role="status">{tx("ui.self_update_reload_last_result", { reason: reloadResult.reason || "-" })}</p>
      ) : null}
      <div className="settings-section-actions">
        {snapshot?.restart_required ? (
          <button className="button button-primary" type="button" disabled={busy} onClick={() => void reloadNow()}>
            {reloading ? <LoaderCircle className="spin" size={15} /> : <RotateCcw size={15} />}{tx("ui.self_update_reload_without_restart")}
          </button>
        ) : null}
        <button className="button button-quiet" type="button" disabled={busy} onClick={() => void checkNow()}>
          {checking ? <LoaderCircle className="spin" size={15} /> : <RefreshCw size={15} />}{tx("ui.self_update_check")}
        </button>
        {snapshot?.asset_url ? <a className="button button-quiet" href={snapshot.asset_url} target="_blank" rel="noopener noreferrer">{tx("ui.self_update_download_manually")}</a> : null}
        <button className="button button-primary" type="button" disabled={busy || !snapshot?.update_available || !snapshot.can_install} onClick={() => void installNow()}>
          {installing ? <LoaderCircle className="spin" size={15} /> : <ShieldCheck size={15} />}{tx("ui.self_update_install")}
        </button>
      </div>
    </section>
  );
}

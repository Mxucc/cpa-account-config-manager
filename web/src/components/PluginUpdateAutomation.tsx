import { useCallback, useEffect, useRef, useState } from "react";
import * as api from "../api/client";
import { useI18n } from "../i18n";
import type { UpdateSnapshot } from "../types";

const updateStatusEvent = "cpa-account-config-manager:update-status";

export function announcePluginUpdateStatus(snapshot: UpdateSnapshot): void {
  window.dispatchEvent(new CustomEvent<UpdateSnapshot>(updateStatusEvent, { detail: snapshot }));
}

export function subscribePluginUpdateStatus(listener: (snapshot: UpdateSnapshot) => void): () => void {
  const receiveStatus = (event: Event) => {
    const snapshot = (event as CustomEvent<UpdateSnapshot>).detail;
    if (snapshot?.policy) listener(snapshot);
  };
  window.addEventListener(updateStatusEvent, receiveStatus);
  return () => window.removeEventListener(updateStatusEvent, receiveStatus);
}

interface PluginUpdateAutomationProps {
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
}

export function PluginUpdateAutomation({ onAPIError, onNotice }: PluginUpdateAutomationProps) {
  const { tx } = useI18n();
  const [updates, setUpdates] = useState<UpdateSnapshot | null>(null);
  const attemptedUpdate = useRef("");
  const refreshInFlight = useRef(false);

  const refresh = useCallback(async (checkNow = false) => {
    if (refreshInFlight.current) return null;
    refreshInFlight.current = true;
    try {
      const next = await api.getEffectiveUpdateStatus(checkNow);
      setUpdates(next);
      return next;
    } finally {
      refreshInFlight.current = false;
    }
  }, []);

  useEffect(() => {
    let cancelled = false;
    const bootstrap = async () => {
      try {
        let next = await api.getEffectiveUpdateStatus();
        if (next.policy?.check_enabled && !next.checked_at && !next.checking && !next.pending) {
          next = await api.getEffectiveUpdateStatus(true);
        }
        if (!cancelled) setUpdates(next);
      } catch (error) {
        if (!cancelled && error instanceof api.APIError && error.status === 401) onAPIError(error);
      }
    };
    const unsubscribe = subscribePluginUpdateStatus(setUpdates);
    void bootstrap();
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [onAPIError]);

  useEffect(() => {
    if (!updates?.policy?.auto_update) attemptedUpdate.current = "";
  }, [updates?.policy?.auto_update]);

  useEffect(() => {
    if (!updates?.checking && !updates?.pending) return;
    const timer = window.setInterval(() => {
      void refresh().catch((error) => {
        if (error instanceof api.APIError && error.status === 401) onAPIError(error);
      });
    }, 1_200);
    return () => window.clearInterval(timer);
  }, [onAPIError, refresh, updates?.checking, updates?.pending]);

  useEffect(() => {
    if (!updates?.policy?.check_enabled || !updates.checked_at || updates.checking || updates.pending) return;
    const checkedAt = Date.parse(updates.checked_at);
    if (!Number.isFinite(checkedAt)) return;
    const intervalHours = Math.min(168, Math.max(1, updates.policy.check_interval_hours || 24));
    const dueAt = checkedAt + intervalHours * 60 * 60 * 1_000;
    const timer = window.setTimeout(() => {
      void refresh(true).catch((error) => {
        if (error instanceof api.APIError && error.status === 401) onAPIError(error);
      });
    }, Math.max(1_000, dueAt - Date.now()));
    return () => window.clearTimeout(timer);
  }, [onAPIError, refresh, updates?.checked_at, updates?.checking, updates?.pending, updates?.policy?.check_enabled, updates?.policy?.check_interval_hours]);

  useEffect(() => {
    const version = updates?.latest_version;
    if (!updates?.policy?.auto_update || !updates.update_available || !version || attemptedUpdate.current === version) return;
    attemptedUpdate.current = version;
    let cancelled = false;
    const install = async () => {
      try {
        const result = await api.installPluginUpdate(version);
        const next = { ...updates, current_version: result.version, update_available: false };
        if (!cancelled) {
          setUpdates(next);
          announcePluginUpdateStatus(next);
          onNotice(tx(result.restart_required
            ? "ui.plugin_version_installed_restart_cpa_to_activate_it"
            : "ui.plugin_version_installed_refresh_to_use_the_new_version", { version: result.version }));
        }
      } catch (error) {
        if (!cancelled) onAPIError(error);
      }
    };
    void install();
    return () => { cancelled = true; };
  }, [onAPIError, onNotice, tx, updates]);

  return null;
}

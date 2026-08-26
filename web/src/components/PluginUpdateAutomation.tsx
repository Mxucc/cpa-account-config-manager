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
  const refreshSequence = useRef(0);

  const refresh = useCallback(async (checkNow = false, signal?: AbortSignal) => {
    if (refreshInFlight.current) return null;
    const sequence = ++refreshSequence.current;
    refreshInFlight.current = true;
    try {
      const next = await api.getEffectiveUpdateStatus(checkNow, signal);
      if (!signal?.aborted && sequence === refreshSequence.current) setUpdates(next);
      return next;
    } finally {
      refreshInFlight.current = false;
    }
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    const bootstrap = async () => {
      try {
        const next = await api.getEffectiveUpdateStatus(false, controller.signal);
        if (!controller.signal.aborted) setUpdates(next);
      } catch (error) {
        if (!controller.signal.aborted && error instanceof api.APIError && error.status === 401) onAPIError(error);
      }
    };
    const unsubscribe = subscribePluginUpdateStatus(setUpdates);
    void bootstrap().catch(() => undefined);
    return () => {
      controller.abort();
      refreshSequence.current += 1;
      unsubscribe();
    };
  }, [onAPIError]);

  useEffect(() => {
    if (!updates?.policy?.auto_update) attemptedUpdate.current = "";
  }, [updates?.policy?.auto_update]);

  useEffect(() => {
    if (!updates?.checking && !updates?.pending) return;
    const controller = new AbortController();
    let timer = 0;
    let active = true;
    const poll = async () => {
      try {
        const next = await refresh(false, controller.signal);
        if (!active || controller.signal.aborted) return;
        // Poll only after the previous request has completed. This prevents
        // slow update checks from piling up and racing each other.
        if (next === null || next.checking || next.pending) {
          timer = window.setTimeout(() => void poll(), 1_200);
        }
      } catch (error) {
        if (!active || controller.signal.aborted) return;
        if (error instanceof api.APIError && error.status === 401) onAPIError(error);
      }
    };
    timer = window.setTimeout(() => void poll(), 1_200);
    return () => {
      active = false;
      controller.abort();
      refreshSequence.current += 1;
      if (timer) window.clearTimeout(timer);
    };
  }, [onAPIError, refresh, updates?.checking, updates?.pending]);

  useEffect(() => {
    if (!updates?.policy?.check_enabled || !updates.checked_at || updates.checking || updates.pending) return;
    const checkedAt = Date.parse(updates.checked_at);
    if (!Number.isFinite(checkedAt)) return;
    const intervalHours = Math.min(168, Math.max(1, updates.policy.check_interval_hours || 24));
    const dueAt = checkedAt + intervalHours * 60 * 60 * 1_000;
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      void refresh(true, controller.signal).catch((error) => {
        if (!controller.signal.aborted && error instanceof api.APIError && error.status === 401) onAPIError(error);
      });
    }, Math.max(1_000, dueAt - Date.now()));
    return () => {
      controller.abort();
      refreshSequence.current += 1;
      window.clearTimeout(timer);
    };
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

import type { Session } from "../types";

let currentSession: Session | null = null;

function normalizeBaseURL(value: string): string {
  const trimmed = value.trim().replace(/\/+$/, "");
  if (trimmed === "" || trimmed === window.location.origin) return "";
  if (/^https?:\/\//i.test(trimmed)) return trimmed;
  return `http://${trimmed}`;
}

export function setSession(baseUrl: string, managementKey: string): Session {
  currentSession = {
    baseUrl: normalizeBaseURL(baseUrl),
    managementKey: managementKey.trim(),
  };
  return currentSession;
}

export function getSession(): Session | null {
  return currentSession;
}

export function clearSession(): void {
  currentSession = null;
}

export function _resetSessionForTest(): void {
  currentSession = null;
}

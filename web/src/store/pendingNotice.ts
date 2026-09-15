/**
 * A self-update reloads the page as soon as CPA swapped the plugin, and that reload also wipes
 * the notice explaining what just happened: the operator saw the result flash and then nothing.
 * The panel therefore stores the notice here before refreshing, and the reloaded page shows it
 * once, so the outcome of a hot reload survives the refresh it triggers.
 */

const PENDING_NOTICE_KEY = "cpa-account-config-manager:pending-notice";

/** pendingNoticeStorage returns session storage, or null when the browser withholds it. */
function pendingNoticeStorage(): Storage | null {
  if (typeof window === "undefined") return null;
  try {
    return window.sessionStorage ?? null;
  } catch {
    // Storage can be blocked (private mode, sandboxed frame). The notice is then simply not kept.
    return null;
  }
}

/** writePendingNotice records one notice for the page that loads next in this tab. */
export function writePendingNotice(message: string): void {
  const trimmed = message.trim();
  if (trimmed === "") return;
  try {
    pendingNoticeStorage()?.setItem(PENDING_NOTICE_KEY, trimmed);
  } catch {
    // Best effort: a missing notice must never break the refresh that follows.
  }
}

/** takePendingNotice returns the stored notice and clears it, so a refresh shows it exactly once. */
export function takePendingNotice(): string {
  const storage = pendingNoticeStorage();
  if (storage === null) return "";
  try {
    const stored = storage.getItem(PENDING_NOTICE_KEY) ?? "";
    if (stored !== "") storage.removeItem(PENDING_NOTICE_KEY);
    return stored;
  } catch {
    return "";
  }
}

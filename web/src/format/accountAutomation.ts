import { formatDateTimeForLocale, formatDaysHoursForLocale } from "../i18n";
import type { Locale } from "../i18n";
import { translate, type MessageKey } from "../i18n/messages";
import type { Account } from "../types";

export type AutomationTone = "info" | "warning" | "danger" | "neutral";

export interface AccountAutomationPresentation {
  badge: string;
  detail: string;
  reason: string;
  tone: AutomationTone;
}

const reasonKeys: Record<string, MessageKey> = {
  healthy_recent_success: "reason.healthy_recent_success",
  quota_exhausted: "reason.quota_exhausted",
  token_revoked: "reason.token_revoked",
  invalid_credentials: "reason.invalid_credentials",
  account_deactivated: "reason.account_deactivated",
  workspace_deactivated: "reason.workspace_deactivated",
  authentication_review: "reason.authentication_review",
  billing_review: "reason.billing_review",
  credential_permission_denied: "reason.credential_permission_denied",
  native_unavailable: "reason.native_unavailable",
  manual_disabled: "reason.manual_disabled",
  transient_failure: "reason.transient_failure",
  unconfirmed_upstream_response: "reason.unconfirmed_upstream_response",
  passive_circuit_open: "reason.passive_circuit_open",
  invalid_response: "reason.invalid_response",
  upstream_unavailable: "reason.upstream_unavailable",
  request_timeout: "reason.request_timeout",
  quota_limited: "reason.quota_limited",
  no_recent_evidence: "reason.no_recent_evidence",
};

function reasonLabel(locale: Locale, reasonCode: string): string {
  return translate(locale, reasonKeys[reasonCode] ?? "reason.unknown");
}

function validDate(value?: string): Date | null {
  if (!value) return null;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
}

function withReason(reason: string, detail: string): string {
  return detail ? `${reason} · ${detail}` : reason;
}

const recoveryClockBoundaryMs = 24 * 60 * 60 * 1000;

function automaticEnableDetail(locale: Locale, recoverAfter: Date, now: Date): string {
  const t = (key: MessageKey, values?: Record<string, string | number>) => translate(locale, key, values);
  const remainingMs = recoverAfter.getTime() - now.getTime();
  if (remainingMs <= 0) return t("automation.enable_due");
  if (remainingMs <= recoveryClockBoundaryMs) {
    return t("automation.expected_enable", { time: formatDateTimeForLocale(locale, recoverAfter) });
  }
  const totalHours = Math.ceil(remainingMs / (60 * 60 * 1000));
  return t("automation.enable_in", { duration: formatDaysHoursForLocale(locale, Math.floor(totalHours / 24), totalHours % 24) });
}

export function accountAutomationPresentation(
  account: Account,
  locale: Locale = "zh-CN",
  now: Date = new Date(),
): AccountAutomationPresentation | null {
  const automation = account.automation;
  if (!automation) return null;
  const t = (key: MessageKey, values?: Record<string, string | number>) => translate(locale, key, values);
  const reasonCode = automation.disable_reason || automation.reason_code;
  const reason = reasonLabel(locale, reasonCode);

  if (account.disabled && automation.owned_disable) {
    if (automation.circuit_open || reasonCode === "passive_circuit_open") {
      const recoverAfter = validDate(automation.recover_after);
      const triggerReason = reasonLabel(locale, automation.circuit_reason_code || automation.reason_code);
      const recoveryDetail = recoverAfter ? automaticEnableDetail(locale, recoverAfter, now) : t("automation.waiting_scan");
      return { badge: t("automation.passive_circuit"), detail: withReason(triggerReason, recoveryDetail), reason: triggerReason, tone: "warning" };
    }
    if (reasonCode === "account_deactivated" || reasonCode === "workspace_deactivated" || automation.recommendation === "delete") {
      if (!automation.auto_delete_enabled) {
        return { badge: t("automation.suggest_delete"), detail: withReason(reason, t("automation.delete_off")), reason, tone: "danger" };
      }
      const retryAfter = validDate(automation.delete_retry_after);
      if (retryAfter && retryAfter > now) {
        return {
          badge: t("automation.waiting_delete_retry"),
          detail: withReason(reason, t("automation.delete_retry", { time: formatDateTimeForLocale(locale, retryAfter) })),
          reason,
          tone: "danger",
        };
      }
      const eligibleAt = validDate(automation.delete_eligible_at);
      if (eligibleAt && eligibleAt > now) {
        return {
          badge: t("automation.delete_grace"),
          detail: withReason(reason, t("automation.delete_eligible", { time: formatDateTimeForLocale(locale, eligibleAt) })),
          reason,
          tone: "warning",
        };
      }
      return {
        badge: automation.auto_action_status === "failed" ? t("automation.waiting_delete_retry") : t("automation.waiting_delete"),
        detail: withReason(reason, t("automation.waiting_scan")),
        reason,
        tone: "danger",
      };
    }

    let recoveryDetail: string;
    if (!automation.auto_enable_enabled) {
      recoveryDetail = t("automation.enable_off");
    } else if (reasonCode === "quota_exhausted") {
      const recoverAfter = validDate(automation.recover_after);
      recoveryDetail = recoverAfter
        ? automaticEnableDetail(locale, recoverAfter, now)
        : t("automation.waiting_quota");
    } else if (["invalid_credentials", "token_revoked", "credential_permission_denied", "authentication_review"].includes(reasonCode)) {
      recoveryDetail = t("automation.waiting_credentials");
    } else {
      recoveryDetail = t("automation.waiting_success", {
        current: automation.healthy_streak,
        required: automation.recovery_threshold,
      });
    }
    return { badge: t("automation.auto_disabled"), detail: withReason(reason, recoveryDetail), reason, tone: "warning" };
  }

  if (account.disabled) {
    return { badge: t("automation.manual_disabled"), detail: reason, reason, tone: "neutral" };
  }

  if (automation.auto_disable_eligible && automation.recommendation === "disable") {
    const badge = automation.auto_disable_enabled ? t("automation.waiting_disable") : t("automation.suggest_disable");
    const evidence = t("automation.disable_evidence", {
      current: automation.failure_streak,
      required: automation.failure_threshold,
    });
    const policy = automation.auto_disable_enabled ? evidence : t("automation.disable_off");
    return { badge, detail: withReason(reason, policy), reason, tone: "warning" };
  }

  if (automation.recommendation === "delete" || automation.recommendation === "reauth" || automation.recommendation === "review") {
    return { badge: t("automation.review"), detail: reason, reason, tone: "neutral" };
  }
  return null;
}

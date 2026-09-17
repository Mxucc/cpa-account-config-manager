import type { UIMessageKey } from "../i18n/uiText";

// Single source of truth for how the inspection health classification and the
// inspection reason codes are labelled. The inspection table and the
// notification state-rule editor both read from here, so a new health value or
// reason code is labelled and selectable consistently everywhere.
export const inspectionHealthLabelKeys = {
  healthy: "ui.healthy",
  quota_limited: "ui.quota_limited",
  invalid_credentials: "ui.invalid_credentials",
  deactivated: "ui.deactivated",
  review: "ui.needs_review",
  unavailable: "ui.unavailable",
  disabled: "ui.disabled",
  unknown: "ui.insufficient_evidence",
} satisfies Record<string, UIMessageKey>;

export const inspectionReasonLabelKeys = {
  healthy_recent_success: "ui.recent_request_succeeded",
  quota_exhausted: "ui.quota_exhausted",
  token_revoked: "ui.token_revoked",
  invalid_credentials: "ui.credentials_invalid_or_expired",
  account_deactivated: "ui.account_deactivated",
  workspace_deactivated: "ui.workspace_deactivated",
  authentication_review: "ui.authentication_needs_review",
  billing_review: "ui.billing_or_quota_needs_review",
  credential_permission_denied: "ui.credential_permission_denied",
  native_unavailable: "ui.cpa_marked_unavailable",
  manual_disabled: "ui.manually_disabled_2",
  transient_failure: "ui.temporary_upstream_failure",
  no_recent_evidence: "ui.no_recent_evidence",
  model_response_ok: "ui.model_response_is_healthy",
  credential_response_ok: "ui.credential_usage_response_is_healthy",
  authentication_failed: "ui.authentication_failed",
  quota_limited: "ui.upstream_quota_or_rate_limited_2",
  model_not_found: "ui.model_unavailable_or_missing",
  request_timeout: "ui.model_test_timed_out",
  upstream_unavailable: "ui.upstream_service_unavailable",
  invalid_response: "ui.could_not_validate_upstream_response",
  unsupported_provider: "ui.provider_unsupported",
  unconfirmed_upstream_response: "ui.could_not_validate_upstream_response",
  passive_circuit_open: "ui.passive_temporary_circuit",
} satisfies Record<string, UIMessageKey>;

export const inspectionHealthValues = Object.keys(inspectionHealthLabelKeys) as Array<keyof typeof inspectionHealthLabelKeys>;
export const inspectionReasonValues = Object.keys(inspectionReasonLabelKeys) as Array<keyof typeof inspectionReasonLabelKeys>;

export function inspectionHealthLabelKey(value: string | undefined): UIMessageKey {
  const key = value ? (inspectionHealthLabelKeys as Record<string, UIMessageKey>)[value] : undefined;
  return key ?? "ui.insufficient_evidence";
}

export function inspectionReasonLabelKey(value: string): UIMessageKey | undefined {
  return (inspectionReasonLabelKeys as Record<string, UIMessageKey>)[value];
}

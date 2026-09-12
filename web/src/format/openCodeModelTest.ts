import type { UIMessageKey } from "../i18n/uiText";

/**
 * The OpenCode probes report a machine-readable reason code. Showing the code alone hides the
 * cause (a rejected API key and an unreachable upstream look equally opaque), so every code is
 * mapped to the localized text the account-level test already uses.
 */
const openCodeReasonKeys: Record<string, UIMessageKey> = {
  model_response_ok: "ui.received_the_expected_model_response",
  authentication_failed: "ui.authentication_failed_check_credential_status",
  model_not_found: "ui.this_account_cannot_use_the_model_or_the_model_does_not_exist",
  quota_limited: "ui.upstream_quota_or_rate_limited",
  upstream_unavailable: "ui.upstream_service_is_temporarily_unavailable",
  request_timeout: "ui.test_request_timed_out",
  invalid_response: "ui.the_upstream_response_cannot_confirm_model_availability",
  unconfirmed_upstream_response: "ui.the_test_result_requires_manual_confirmation",
  request_failed: "ui.request_failed",
  credential_incomplete: "ui.opencode_incomplete_credentials",
  invalid_model: "ui.model_id_is_invalid",
};

/** Returns the catalog key for a probe reason code, or "" when the code is unknown. */
export function openCodeReasonKey(reasonCode?: string): UIMessageKey | "" {
  const code = (reasonCode ?? "").trim();
  return openCodeReasonKeys[code] ?? "";
}

/**
 * A rejected Go probe is almost always a credential mismatch rather than a broken model, and the
 * two causes need different fixes, so they are named explicitly.
 */
export function openCodeProbeHintKey(kind: "go" | "zen", reasonCode?: string): UIMessageKey | "" {
  if ((reasonCode ?? "").trim() !== "authentication_failed") return "";
  return kind === "go" ? "ui.opencode_probe_go_auth_failed_hint" : "ui.opencode_probe_zen_auth_failed_hint";
}

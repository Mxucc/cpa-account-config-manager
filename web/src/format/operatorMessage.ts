import type { Locale } from "../i18n";
import { translateOperatorMessage } from "../i18n/operatorText";
import { translateUI, type UIMessageKey } from "../i18n/uiText";

const directMessages: Record<string, UIMessageKey> = {
  "ui.management_key_is_not_set": "ui.management_key_is_not_set",
  "ui.the_account_manager_plugin_was_not_found_in_the_plugin_store": "ui.the_account_manager_plugin_was_not_found_in_the_plugin_store",
  "ui.authentication_failed": "ui.authentication_failed",
  "ui.request_failed": "ui.request_failed",
	"ui.settings_persistence_failed": "ui.settings_persistence_failed",
};

export function operatorMessage(message?: string, locale: Locale = "zh-CN"): string {
  const normalized = message?.trim() ?? "";
  const importAccountLimit = normalized.match(/^import contains more than (\d+) accounts$/);
  if (importAccountLimit) {
    return translateUI(locale, "ui.a_single_import_supports_at_most_count_accounts", { count: importAccountLimit[1] });
  }
  const requestFailure = normalized.match(/^Request failed \((\d+)\)$/);
  if (requestFailure) return translateUI(locale, "ui.request_failed_code", { code: requestFailure[1] });
  const exportFailure = normalized.match(/^Export failed \((\d+)\)$/);
  if (exportFailure) return translateUI(locale, "ui.export_failed_code", { code: exportFailure[1] });
  const translated = translateOperatorMessage(locale, normalized);
  if (translated) return translated;
  const direct = directMessages[normalized];
  if (direct) return translateUI(locale, direct);
  return normalized;
}

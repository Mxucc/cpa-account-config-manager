import type { Account } from "../types";
import type { Locale } from "../i18n";
import { translateUI, type UIMessageKey } from "../i18n/uiText";

const stateLabels: Record<string, UIMessageKey> = {
  active: "ui.active",
  enabled: "ui.enabled",
  disabled: "ui.disabled",
  error: "ui.error",
  unavailable: "ui.temporarily_unavailable",
  unknown: "ui.unknown_status",
};

const sourceLabels: Record<string, UIMessageKey> = {
  file: "ui.auth_file",
  runtime: "ui.runtime",
  runtime_only: "ui.runtime_only",
  config: "ui.configuration_file",
};

export function accountState(account: Account): string {
  return account.disabled ? "disabled" : account.unavailable ? "unavailable" : account.status || "unknown";
}

export function accountStateLabel(account: Account, locale: Locale = "zh-CN"): string {
  return stateLabel(accountState(account), locale);
}

export function stateLabel(value?: string, locale: Locale = "zh-CN"): string {
  const normalized = value?.trim().toLowerCase() || "unknown";
  return translateUI(locale, stateLabels[normalized] || "ui.unknown_status");
}

export function sourceLabel(value?: string, locale: Locale = "zh-CN"): string {
  const normalized = value?.trim().toLowerCase() || "";
  const label = sourceLabels[normalized];
  return label ? translateUI(locale, label) : value?.trim() || translateUI(locale, "ui.unknown_source");
}

export function technicalLabel(value?: string, locale: Locale = "zh-CN"): string {
  return value?.trim() || translateUI(locale, "ui.unknown");
}

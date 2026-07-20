import { act, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { I18nProvider, useI18n } from "./I18nProvider";
import { CPA_LANGUAGE_STORAGE_KEY } from "./locale";

function LocaleProbe() {
  const { locale, t, tx } = useI18n();
  return <div><span>{locale}</span><strong>{t("automation.auto_disabled")}</strong><em>{tx("ui.account_management")}</em></div>;
}

describe("I18nProvider", () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.lang = "zh-CN";
  });

  it("initializes from CPA and switches live without writing an independent preference", () => {
    const initial = '{"state":{"language":"zh-CN"},"version":0}';
    const next = '{"state":{"language":"en"},"version":0}';
    localStorage.setItem(CPA_LANGUAGE_STORAGE_KEY, initial);
    render(<I18nProvider><LocaleProbe /></I18nProvider>);
    expect(screen.getByText("zh-CN")).toBeInTheDocument();
    expect(screen.getByText("自动禁用")).toBeInTheDocument();

    act(() => {
      localStorage.setItem(CPA_LANGUAGE_STORAGE_KEY, next);
      window.dispatchEvent(new StorageEvent("storage", { key: CPA_LANGUAGE_STORAGE_KEY, oldValue: initial, newValue: next }));
    });
    expect(screen.getByText("en")).toBeInTheDocument();
    expect(screen.getByText("Auto-disabled")).toBeInTheDocument();
    expect(localStorage.getItem(CPA_LANGUAGE_STORAGE_KEY)).toBe(next);
    expect(localStorage.length).toBe(1);
  });

  it.each([
    ["zh-CN", "自动禁用", "账号管理"],
    ["zh-TW", "自動停用", "帳號管理"],
    ["en", "Auto-disabled", "Account Management"],
    ["ru", "Отключено автоматически", "Управление учётными записями"],
  ])("renders the complete %s catalog", (language, label, heading) => {
    localStorage.setItem(CPA_LANGUAGE_STORAGE_KEY, JSON.stringify({ state: { language }, version: 0 }));
    render(<I18nProvider><LocaleProbe /></I18nProvider>);
    expect(screen.getByText(language)).toBeInTheDocument();
    expect(screen.getByText(label)).toBeInTheDocument();
    expect(screen.getByText(heading)).toBeInTheDocument();
  });
});

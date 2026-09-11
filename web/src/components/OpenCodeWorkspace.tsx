import { useCallback, useEffect, useRef, useState } from "react";
import { Activity, AlertTriangle, Coins, ExternalLink, KeyRound, Link2, LoaderCircle, Plus, Radio, RefreshCw, RotateCcw, Search, Trash2 } from "lucide-react";
import * as api from "../api/client";
import { operatorMessage } from "../format/operatorMessage";
import { useI18n } from "../i18n";
import type { OpenCodeAccountView, OpenCodeModelPrice, OpenCodeModelTestResult, OpenCodePricingSnapshot, OpenCodeQuotaResult, OpenCodeSessionSnapshot, OpenCodeZenAccountView } from "../types";
import { IconButton } from "./IconButton";

interface OpenCodeWorkspaceProps {
  refreshRevision: number;
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
}

type OpenCodeKind = "go" | "zen";

interface ModelTarget {
  kind: OpenCodeKind;
  accountID: string;
  label: string;
  models: string[];
}

/** Prices are USD per million tokens; tiny values keep four decimals. */
function formatPriceUSD(value: number | undefined): string {
  if (typeof value !== "number" || Number.isNaN(value)) return "-";
  if (value === 0) return "$0";
  if (value < 0.01) return `$${value.toFixed(4)}`;
  return `$${value.toFixed(2)}`;
}

/**
 * Allowances and subscription amounts are USD figures that are normally whole
 * dollars, so they reuse the locale number formatter instead of formatPriceUSD.
 */
function formatAllowanceUSD(value: number | undefined, formatNumber: (value: number) => string): string {
  if (typeof value !== "number" || Number.isNaN(value)) return "-";
  return `$${formatNumber(value)}`;
}

/** Compact 5-hour / weekly / monthly request estimate; undefined when the docs have none. */
function formatEstimatedRequests(requests: OpenCodeModelPrice["estimated_requests"], formatNumber: (value: number) => string): string | undefined {
  const windows = [requests?.five_hour, requests?.weekly, requests?.monthly];
  if (!windows.some((value) => typeof value === "number" && !Number.isNaN(value))) return undefined;
  return windows.map((value) => (typeof value === "number" && !Number.isNaN(value) ? formatNumber(value) : "-")).join(" / ");
}

/** Mirrors the backend lookup: vendor prefixes and "_-" drift are tolerated. */
function normalizePriceKey(model: string): string {
  const trimmed = model.trim().toLowerCase();
  const tail = trimmed.includes("/") ? trimmed.slice(trimmed.lastIndexOf("/") + 1) : trimmed;
  return tail.replace(/_/g, "-");
}

function priceFor(prices: OpenCodeModelPrice[] | undefined, model: string): OpenCodeModelPrice | undefined {
  const key = normalizePriceKey(model);
  if (!key) return undefined;
  return (prices ?? []).find((price) => normalizePriceKey(price.id) === key);
}

const PRICE_ROWS_LIMIT = 40;

function formatWindow(window: { usage_percent: number; reset_in_sec: number } | undefined, tx: ReturnType<typeof useI18n>["tx"]): string {
  if (!window) return tx("ui.no_data");
  const resets = window.reset_in_sec > 0 ? `${Math.ceil(window.reset_in_sec / 60)} ${tx("ui.minutes_short")}` : "-";
  return `${window.usage_percent.toFixed(1)}% · ${resets}`;
}

/**
 * OpenCode workspace: the single place that manages OpenCode Go workspaces and
 * Zen credentials, including the model catalog, a real model test, and the action
 * that makes the models routable through CPA.
 */
export function OpenCodeWorkspace({ refreshRevision, onAPIError, onNotice }: OpenCodeWorkspaceProps) {
  const { locale, tx, formatDateTime, formatNumber } = useI18n();
  const [goAccounts, setGoAccounts] = useState<OpenCodeAccountView[]>([]);
  const [zenAccounts, setZenAccounts] = useState<OpenCodeZenAccountView[]>([]);
  const [quota, setQuota] = useState<Record<string, OpenCodeQuotaResult>>({});
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [storageError, setStorageError] = useState("");
  const [adding, setAdding] = useState(false);
  const [newWorkspace, setNewWorkspace] = useState("");
  const [newCookie, setNewCookie] = useState("");
  const [newKey, setNewKey] = useState("");
  const [newZenName, setNewZenName] = useState("");
  const [newZenBase, setNewZenBase] = useState("");
  const [newZenKey, setNewZenKey] = useState("");
  const [keyDraft, setKeyDraft] = useState<Record<string, string>>({});
  const [target, setTarget] = useState<ModelTarget | null>(null);
  const [testModel, setTestModel] = useState("");
  const [testResult, setTestResult] = useState<OpenCodeModelTestResult | null>(null);
  const [pricing, setPricing] = useState<OpenCodePricingSnapshot>({});
  const [session, setSession] = useState<OpenCodeSessionSnapshot | null>(null);
  const [priceKind, setPriceKind] = useState<OpenCodeKind>("go");
  const [priceQuery, setPriceQuery] = useState("");
  const request = useRef(0);

  const handleError = useCallback((caught: unknown) => {
    if (caught instanceof api.APIError && caught.status === 401) {
      onAPIError(caught);
      return;
    }
    setError(operatorMessage(caught instanceof Error ? caught.message : tx("ui.request_failed"), locale));
  }, [locale, onAPIError, tx]);

  const refresh = useCallback(async (signal?: AbortSignal) => {
    const requestID = request.current + 1;
    request.current = requestID;
    setLoading(true);
    setError("");
    try {
      const [go, zen, quotaSnapshot, pricingSnapshot, sessionSnapshot] = await Promise.all([
        api.listOpenCodeAccounts(signal),
        api.listOpenCodeZenAccounts(signal),
        api.getOpenCodeQuota(signal),
        api.getOpenCodePricing(signal),
        api.getOpenCodeSession(signal),
      ]);
      if (requestID !== request.current) return;
      setGoAccounts(go.accounts);
      setZenAccounts(zen.accounts);
      setQuota(quotaSnapshot.results ?? {});
      setPricing(pricingSnapshot.pricing ?? {});
      setSession(sessionSnapshot.session ?? null);
      setStorageError(go.storage_error || zen.storage_error || quotaSnapshot.storage_error || pricingSnapshot.pricing?.storage_error || "");
    } catch (caught) {
      if (signal?.aborted || (caught instanceof DOMException && caught.name === "AbortError")) return;
      if (requestID === request.current) handleError(caught);
    } finally {
      if (requestID === request.current) setLoading(false);
    }
  }, [handleError]);

  useEffect(() => {
    const controller = new AbortController();
    void refresh(controller.signal);
    return () => {
      controller.abort();
      request.current += 1;
    };
  }, [refresh, refreshRevision]);

  const withBusy = async (key: string, action: () => Promise<void>) => {
    setBusy(key);
    setError("");
    try {
      await action();
    } catch (caught) {
      handleError(caught);
    } finally {
      setBusy("");
    }
  };

  const addGoAccount = () => void withBusy("add-go", async () => {
    if (!newWorkspace.trim() || !newCookie.trim()) return;
    await api.saveOpenCodeAccount(newWorkspace.trim(), newCookie.trim(), newKey.trim() || undefined);
    setNewWorkspace("");
    setNewCookie("");
    setNewKey("");
    setAdding(false);
    setGoAccounts((await api.listOpenCodeAccounts()).accounts);
    onNotice(tx("ui.opencode_account_saved"));
  });

  const addZenAccount = () => void withBusy("add-zen", async () => {
    if (!newZenKey.trim()) return;
    await api.saveOpenCodeZenAccount({ name: newZenName.trim(), base_url: newZenBase.trim(), zen_api_key: newZenKey.trim() });
    setNewZenName("");
    setNewZenBase("");
    setNewZenKey("");
    setZenAccounts((await api.listOpenCodeZenAccounts()).accounts);
    onNotice(tx("ui.opencode_account_saved"));
  });

  const syncPrices = () => void withBusy("pricing", async () => {
    const response = await api.refreshOpenCodePricing();
    setPricing(response.pricing ?? {});
    onNotice(tx(response.changed ? "ui.opencode_pricing_changed" : "ui.opencode_pricing_unchanged"));
  });

  const refreshCatalog = (kind: OpenCodeKind, accountID: string) => void withBusy(`models-${accountID}`, async () => {
    const response = await api.refreshOpenCodeModels(kind, accountID);
    if (kind === "go") {
      setGoAccounts((current) => current.map((account) => (account.id === accountID ? { ...account, ...(response.account as OpenCodeAccountView) } : account)));
    } else {
      setZenAccounts((current) => current.map((account) => (account.id === accountID ? { ...account, ...(response.account as OpenCodeZenAccountView) } : account)));
    }
    const models = response.account.models ?? [];
    onNotice(tx("ui.opencode_models_loaded", { count: String(models.length) }));
  });

  const refreshQuota = (accountID: string) => void withBusy(`quota-${accountID}`, async () => {
    const response = await api.refreshOpenCodeAccountQuota(accountID);
    setQuota((current) => ({ ...current, [accountID]: response.result }));
  });

  const bind = (kind: OpenCodeKind, accountID: string) => void withBusy(`bind-${accountID}`, async () => {
    const response = await api.bindOpenCodeChannel(kind, accountID);
    const binding = response.binding;
    onNotice(`${tx(binding.created ? "ui.opencode_channel_created" : "ui.opencode_channel_updated", { url: binding.base_url })} · ${tx("ui.opencode_channel_models", { count: String(binding.models ?? 0) })}`);
  });

  const saveKey = (accountID: string) => void withBusy(`key-${accountID}`, async () => {
    await api.saveOpenCodeAccountKey(accountID, (keyDraft[accountID] ?? "").trim());
    setKeyDraft((current) => ({ ...current, [accountID]: "" }));
    setGoAccounts((await api.listOpenCodeAccounts()).accounts);
    onNotice(tx("ui.opencode_key_saved"));
  });

  const openModels = (kind: OpenCodeKind, accountID: string, label: string, models: string[]) => {
    setTarget({ kind, accountID, label, models });
    setTestModel(models[0] ?? "");
    setTestResult(null);
  };

  const runModelTest = () => void withBusy("model-test", async () => {
    if (!target || !testModel.trim()) return;
    const response = await api.testOpenCodeModel(target.kind, target.accountID, testModel.trim());
    setTestResult(response.result);
  });

  const catalogPrices = (priceKind === "go" ? pricing.go : pricing.zen) ?? [];
  const filteredPrices = catalogPrices.filter((price) => {
    const query = priceQuery.trim().toLowerCase();
    if (!query) return true;
    return price.id.toLowerCase().includes(query) || (price.name ?? "").toLowerCase().includes(query);
  });
  const visiblePrices = filteredPrices.slice(0, PRICE_ROWS_LIMIT);
  const selectedPrice = target ? priceFor(target.kind === "go" ? pricing.go : pricing.zen, testModel) : undefined;
  const billingModes = pricing.billing ?? [];
  const goBilling = billingModes.find((mode) => mode.kind === "go");
  // Documented Go split: the 5-hour window is 20% and the week is 50% of the
  // monthly allowance. The API carries the fractions; the constants are the fallback.
  const goFiveHourFraction = goBilling?.five_hour_fraction ?? 0.2;
  const goWeeklyFraction = goBilling?.weekly_fraction ?? 0.5;

  const statusLabel = (status: OpenCodeModelTestResult["status"]): string => {
    switch (status) {
      case "available": return tx("ui.model_available");
      case "unavailable": return tx("ui.model_unavailable");
      case "unsupported": return tx("ui.testing_unsupported");
      default: return tx("ui.manual_confirmation_required");
    }
  };

  return (
    <section className="opencode-workspace" role="tabpanel" aria-label={tx("ui.opencode_menu")}>
      <header className="opencode-header">
        <div>
          <div className="eyebrow"><Link2 size={15} />{tx("ui.opencode")}</div>
          <h2>{tx("ui.opencode_title")}</h2>
          <p>{tx("ui.opencode_description")}</p>
        </div>
        <div className="opencode-header-actions">
          <a className="button button-quiet" href="https://opencode.ai/auth" target="_blank" rel="noopener noreferrer">
            <ExternalLink size={15} />{tx("ui.opencode_open_auth")}
          </a>
          <button className="button button-quiet" type="button" disabled={loading} onClick={() => void refresh()}>
            <RefreshCw className={loading ? "spin" : ""} size={15} />{tx("ui.refresh")}
          </button>
        </div>
      </header>

      {storageError ? <div className="notice-bar warning-notice" role="status"><AlertTriangle size={16} />{storageError}</div> : null}
      {error ? <div className="notice-bar" role="alert"><AlertTriangle size={16} />{error}</div> : null}

      <div className="opencode-links">
        <a href="https://opencode.ai/workspace" target="_blank" rel="noopener noreferrer">OpenCode Go · {tx("ui.opencode_open_workspace")}</a>
        <a href="https://opencode.ai/zen" target="_blank" rel="noopener noreferrer">OpenCode Zen · {tx("ui.opencode_open_zen")}</a>
        <a href="/v0/resource/plugins/cpa-account-config-manager/opencode-status" target="_blank" rel="noopener noreferrer">{tx("ui.opencode_open_status_page")}</a>
      </div>

      <section className="opencode-section" aria-label={tx("ui.opencode_go_accounts")}>
        <div className="opencode-section-heading">
          <div>
            <strong>{tx("ui.opencode_go_accounts")}</strong>
            <span>{tx("ui.opencode_go_accounts_description")}</span>
          </div>
          <button className="button button-quiet" type="button" onClick={() => setAdding((value) => !value)}>
            <Plus size={15} />{tx("ui.opencode_add_go")}
          </button>
        </div>
        {adding ? (
          <div className="opencode-form">
            <label className="field-block"><span>{tx("ui.opencode_workspace_id")}</span><input value={newWorkspace} onChange={(event) => setNewWorkspace(event.target.value)} autoComplete="off" /></label>
            <label className="field-block"><span>{tx("ui.opencode_auth_cookie")}</span><input type="password" value={newCookie} onChange={(event) => setNewCookie(event.target.value)} autoComplete="off" /></label>
            <label className="field-block"><span>{tx("ui.opencode_api_key")}</span><input type="password" value={newKey} onChange={(event) => setNewKey(event.target.value)} autoComplete="off" /></label>
            <div className="opencode-form-actions">
              <button className="button button-primary" type="button" disabled={busy === "add-go" || !newWorkspace.trim() || !newCookie.trim()} onClick={addGoAccount}>
                {busy === "add-go" ? <LoaderCircle className="spin" size={15} /> : <Plus size={15} />}{tx("ui.save")}
              </button>
            </div>
            <p className="opencode-note">{tx("ui.opencode_credentials_note")}</p>
          </div>
        ) : null}
        <div className="opencode-table-wrap">
          <table className="account-table opencode-table">
            <thead>
              <tr>
                <th>{tx("ui.opencode_workspace_id")}</th>
                <th>{tx("ui.opencode_api_key")}</th>
                <th>{tx("ui.opencode_quota")}</th>
                <th>{tx("ui.models")}</th>
                <th className="actions-header">{tx("ui.actions")}</th>
              </tr>
            </thead>
            <tbody>
              {goAccounts.map((account) => {
                const result = quota[account.id];
                return (
                  <tr key={account.id}>
                    <td><strong>{account.workspace_id}</strong></td>
                    <td>
                      <div className="opencode-key-cell">
                        <span>{account.key_set ? tx("ui.opencode_key_stored") : tx("ui.opencode_key_missing")}</span>
                        <input
                          type="password"
                          aria-label={tx("ui.opencode_api_key_for", { account: account.workspace_id })}
                          value={keyDraft[account.id] ?? ""}
                          placeholder={tx("ui.opencode_key_placeholder")}
                          autoComplete="off"
                          onChange={(event) => setKeyDraft((current) => ({ ...current, [account.id]: event.target.value }))}
                        />
                        <button className="button button-quiet button-small" type="button" disabled={busy === `key-${account.id}` || !(keyDraft[account.id] ?? "").trim()} onClick={() => saveKey(account.id)}>
                          {busy === `key-${account.id}` ? <LoaderCircle className="spin" size={14} /> : <KeyRound size={14} />}{tx("ui.save")}
                        </button>
                      </div>
                    </td>
                    <td>
                      <div className="opencode-quota-cell">
                        {result?.success ? (
                          <>
                            <small>{tx("ui.opencode_rolling")}: {formatWindow(result.rolling, tx)}</small>
                            <small>{tx("ui.opencode_weekly")}: {formatWindow(result.weekly, tx)}</small>
                            <small>{tx("ui.opencode_monthly")}: {formatWindow(result.monthly, tx)}</small>
                          </>
                        ) : (
                          <small>{result?.error ? operatorMessage(result.error, locale) : tx("ui.no_data")}</small>
                        )}
                      </div>
                    </td>
                    <td>
                      {account.models?.length ? (
                        <div className="opencode-models-cell">
                          <strong>{account.models.length}</strong>
                          <small>{account.models.slice(0, 3).join(", ")}{account.models.length > 3 ? " …" : ""}</small>
                          {account.models_error ? <small className="opencode-model-error">{account.models_error}</small> : null}
                        </div>
                      ) : (
                        <div className="opencode-models-cell">
                          <strong>-</strong>
                          <small>{account.models_error ? account.models_error : tx("ui.opencode_models_not_loaded")}</small>
                        </div>
                      )}
                    </td>
                    <td className="actions-cell">
                      <div className="row-actions">
                        <IconButton label={tx("ui.opencode_load_models_for", { account: account.workspace_id })} disabled={busy === `models-${account.id}`} onClick={() => refreshCatalog("go", account.id)}>
                          {busy === `models-${account.id}` ? <LoaderCircle className="spin" size={15} /> : <RefreshCw size={15} />}
                        </IconButton>
                        <IconButton label={tx("ui.opencode_refresh_action")} disabled={busy === `quota-${account.id}`} onClick={() => refreshQuota(account.id)}>
                          {busy === `quota-${account.id}` ? <LoaderCircle className="spin" size={15} /> : <RotateCcw size={15} />}
                        </IconButton>
                        <IconButton label={tx("ui.opencode_test_models_for", { account: account.workspace_id })} disabled={!account.models?.length} onClick={() => openModels("go", account.id, account.workspace_id, account.models ?? [])}>
                          <Activity size={15} />
                        </IconButton>
                        <IconButton label={tx("ui.opencode_bind_for", { account: account.workspace_id })} disabled={busy === `bind-${account.id}` || !account.key_set} onClick={() => bind("go", account.id)}>
                          {busy === `bind-${account.id}` ? <LoaderCircle className="spin" size={15} /> : <Link2 size={15} />}
                        </IconButton>
                        <IconButton className="button-danger" label={tx("ui.opencode_remove_for", { account: account.workspace_id })} onClick={() => void withBusy(`remove-${account.id}`, async () => {
                          await api.removeOpenCodeAccount(account.id);
                          setGoAccounts((await api.listOpenCodeAccounts()).accounts);
                          onNotice(tx("ui.opencode_account_removed"));
                        })}><Trash2 size={15} /></IconButton>
                      </div>
                    </td>
                  </tr>
                );
              })}
              {!loading && goAccounts.length === 0 ? <tr><td colSpan={5}>{tx("ui.opencode_no_accounts")}</td></tr> : null}
            </tbody>
          </table>
        </div>
      </section>

      <section className="opencode-section" aria-label={tx("ui.opencode_zen_accounts")}>
        <div className="opencode-section-heading">
          <div>
            <strong>{tx("ui.opencode_zen_accounts")}</strong>
            <span>{tx("ui.opencode_zen_accounts_description")}</span>
          </div>
        </div>
        <div className="opencode-form">
          <label className="field-block"><span>{tx("ui.name")}</span><input value={newZenName} onChange={(event) => setNewZenName(event.target.value)} autoComplete="off" /></label>
          <label className="field-block"><span>{tx("ui.ai_provider_base_url")}</span><input value={newZenBase} onChange={(event) => setNewZenBase(event.target.value)} placeholder="https://opencode.ai/zen" autoComplete="off" /></label>
          <label className="field-block"><span>{tx("ui.opencode_api_key")}</span><input type="password" value={newZenKey} onChange={(event) => setNewZenKey(event.target.value)} autoComplete="off" /></label>
          <div className="opencode-form-actions">
            <button className="button button-primary" type="button" disabled={busy === "add-zen" || !newZenKey.trim()} onClick={addZenAccount}>
              {busy === "add-zen" ? <LoaderCircle className="spin" size={15} /> : <Plus size={15} />}{tx("ui.save")}
            </button>
          </div>
        </div>
        <div className="opencode-table-wrap">
          <table className="account-table opencode-table">
            <thead>
              <tr>
                <th>{tx("ui.name")}</th>
                <th>{tx("ui.ai_provider_base_url")}</th>
                <th>{tx("ui.models")}</th>
                <th className="actions-header">{tx("ui.actions")}</th>
              </tr>
            </thead>
            <tbody>
              {zenAccounts.map((account) => (
                <tr key={account.id}>
                  <td><strong>{account.name || account.id}</strong></td>
                  <td className="opencode-table-url">{account.base_url}</td>
                  <td>
                    {account.models?.length ? (
                      <div className="opencode-models-cell">
                        <strong>{account.models.length}</strong>
                        <small>{account.models.slice(0, 3).join(", ")}{account.models.length > 3 ? " …" : ""}</small>
                        {account.models_error ? <small className="opencode-model-error">{account.models_error}</small> : null}
                      </div>
                    ) : (
                      <div className="opencode-models-cell">
                        <strong>-</strong>
                        <small>{account.models_error ? account.models_error : tx("ui.opencode_models_not_loaded")}</small>
                      </div>
                    )}
                  </td>
                  <td className="actions-cell">
                    <div className="row-actions">
                      <IconButton label={tx("ui.opencode_load_models_for", { account: account.name || account.id })} disabled={busy === `models-${account.id}`} onClick={() => refreshCatalog("zen", account.id)}>
                        {busy === `models-${account.id}` ? <LoaderCircle className="spin" size={15} /> : <RefreshCw size={15} />}
                      </IconButton>
                      <IconButton label={tx("ui.opencode_test_models_for", { account: account.name || account.id })} disabled={!account.models?.length} onClick={() => openModels("zen", account.id, account.name || account.id, account.models ?? [])}>
                        <Activity size={15} />
                      </IconButton>
                      <IconButton label={tx("ui.opencode_bind_for", { account: account.name || account.id })} disabled={busy === `bind-${account.id}` || !account.key_set} onClick={() => bind("zen", account.id)}>
                        {busy === `bind-${account.id}` ? <LoaderCircle className="spin" size={15} /> : <Link2 size={15} />}
                      </IconButton>
                      <IconButton className="button-danger" label={tx("ui.opencode_remove_for", { account: account.name || account.id })} onClick={() => void withBusy(`remove-${account.id}`, async () => {
                        await api.removeOpenCodeZenAccount(account.id);
                        setZenAccounts((await api.listOpenCodeZenAccounts()).accounts);
                        onNotice(tx("ui.opencode_account_removed"));
                      })}><Trash2 size={15} /></IconButton>
                    </div>
                  </td>
                </tr>
              ))}
              {!loading && zenAccounts.length === 0 ? <tr><td colSpan={4}>{tx("ui.opencode_no_accounts")}</td></tr> : null}
            </tbody>
          </table>
        </div>
      </section>

      <section className="opencode-section opencode-pricing" aria-label={tx("ui.opencode_pricing")}>
        <div className="opencode-section-heading">
          <div>
            <strong><Coins size={14} /> {tx("ui.opencode_pricing")}</strong>
            <span>{tx("ui.opencode_pricing_description")}</span>
          </div>
          <button className="button button-quiet" type="button" disabled={busy === "pricing"} onClick={syncPrices}>
            {busy === "pricing" ? <LoaderCircle className="spin" size={15} /> : <RefreshCw size={15} />}{tx("ui.opencode_pricing_sync")}
          </button>
        </div>
        <div className="opencode-price-meta">
          <span>{tx("ui.opencode_pricing_source")}: {pricing.source || "-"}</span>
          <span>{tx("ui.opencode_pricing_updated")}: {pricing.updated_at ? formatDateTime(pricing.updated_at) : "-"}</span>
          <span>{tx("ui.opencode_docs_synced")}: {pricing.docs_updated_at ? formatDateTime(pricing.docs_updated_at) : "-"}</span>
          <span>{tx("ui.opencode_price_per_million")}</span>
        </div>
        {billingModes.length ? (
          <div className="opencode-billing" role="group" aria-label={tx("ui.opencode_billing")}>
            {billingModes.map((mode) => {
              const label = mode.kind === "go"
                ? tx("ui.opencode_billing_go")
                : mode.kind === "zen"
                  ? tx("ui.opencode_billing_zen")
                  : mode.kind;
              const split = !mode.metered && (typeof mode.five_hour_fraction === "number" || typeof mode.weekly_fraction === "number")
                ? tx("ui.opencode_billing_split", {
                  five_hour: `${formatNumber((mode.five_hour_fraction ?? 0) * 100)}%`,
                  weekly: `${formatNumber((mode.weekly_fraction ?? 0) * 100)}%`,
                  monthly: "100%",
                })
                : "";
              return (
                <div className="opencode-billing-row" key={mode.kind}>
                  <span className="opencode-billing-kind">{label}</span>
                  {mode.metered ? <span className="opencode-billing-badge">{tx("ui.opencode_billing_metered")}</span> : null}
                  {!mode.metered && typeof mode.subscription_usd_per_month === "number" ? (
                    <span className="opencode-billing-amount">
                      {tx("ui.opencode_billing_subscription", { amount: formatAllowanceUSD(mode.subscription_usd_per_month, formatNumber) })}
                    </span>
                  ) : null}
                  {split ? <span className="opencode-billing-split">{split}</span> : null}
                  {mode.summary ? <span className="opencode-billing-summary">{mode.summary}</span> : null}
                  {mode.docs_url ? (
                    <a className="opencode-billing-docs" href={mode.docs_url} target="_blank" rel="noopener noreferrer">
                      <ExternalLink size={13} />{tx("ui.opencode_billing_docs")}
                    </a>
                  ) : null}
                </div>
              );
            })}
          </div>
        ) : null}
        <div className="opencode-price-controls">
          <div className="scope-segment" aria-label={tx("ui.opencode_pricing")}>
            <button type="button" className={priceKind === "go" ? "active" : ""} onClick={() => setPriceKind("go")}>{tx("ui.ai_provider_channel_opencode")}</button>
            <button type="button" className={priceKind === "zen" ? "active" : ""} onClick={() => setPriceKind("zen")}>{tx("ui.ai_provider_channel_opencode_zen")}</button>
          </div>
          <label className="opencode-price-search">
            <Search size={14} />
            <input value={priceQuery} onChange={(event) => setPriceQuery(event.target.value)} placeholder={tx("ui.opencode_prices_filter")} aria-label={tx("ui.opencode_prices_filter")} />
          </label>
          <span className="opencode-price-count">{tx("ui.opencode_prices_shown", { shown: String(visiblePrices.length), total: String(filteredPrices.length) })}</span>
        </div>
        <div className="opencode-table-wrap">
          <table className="account-table opencode-table opencode-price-table">
            <thead>
              <tr>
                <th>{tx("ui.model")}</th>
                <th>{tx("ui.opencode_price_input")}</th>
                <th>{tx("ui.opencode_price_output")}</th>
                <th>{tx("ui.opencode_price_cache_read")}</th>
                <th>{tx("ui.opencode_price_cache_write")}</th>
                {priceKind === "go" ? <th>{tx("ui.opencode_monthly_allowance")}</th> : null}
                {priceKind === "go" ? <th>{tx("ui.opencode_estimated_requests")}</th> : null}
                <th>{tx("ui.opencode_context")}</th>
              </tr>
            </thead>
            <tbody>
              {visiblePrices.map((price) => {
                const estimated = formatEstimatedRequests(price.estimated_requests, formatNumber);
                const estimatedLabel = `${tx("ui.opencode_estimated_requests")}: ${tx("ui.opencode_rolling")} / ${tx("ui.opencode_weekly")} / ${tx("ui.opencode_monthly")}`;
                const monthlyLimit = typeof price.monthly_limit_usd === "number" && !Number.isNaN(price.monthly_limit_usd) ? price.monthly_limit_usd : undefined;
                return (
                  <tr key={price.id}>
                    <td>
                      <div className="opencode-models-cell">
                        <strong>{price.name || price.id}</strong>
                        <small>{price.id}</small>
                        {price.tiers?.length ? <small className="opencode-model-error">{tx("ui.opencode_price_tier_note", { tokens: formatNumber(price.tiers[0].min_context_tokens) })}</small> : null}
                        {price.deprecated_at ? <small className="opencode-model-error">{tx("ui.opencode_deprecated_at", { date: price.deprecated_at })}</small> : null}
                      </div>
                    </td>
                    <td>{formatPriceUSD(price.input_usd_per_million)}</td>
                    <td>{formatPriceUSD(price.output_usd_per_million)}</td>
                    <td>{formatPriceUSD(price.cache_read_usd_per_million)}</td>
                    <td>{formatPriceUSD(price.cache_write_usd_per_million)}</td>
                    {priceKind === "go" ? (
                      <td>
                        <div className="opencode-allowance-cell">
                          <strong>{formatAllowanceUSD(monthlyLimit, formatNumber)}</strong>
                          {monthlyLimit === undefined ? null : (
                            <small>
                              {tx("ui.opencode_window_budget", {
                                five_hour: formatAllowanceUSD(monthlyLimit * goFiveHourFraction, formatNumber),
                                weekly: formatAllowanceUSD(monthlyLimit * goWeeklyFraction, formatNumber),
                              })}
                            </small>
                          )}
                        </div>
                      </td>
                    ) : null}
                    {priceKind === "go" ? (
                      <td>
                        {estimated ? <span className="opencode-estimates" title={estimatedLabel} aria-label={estimatedLabel}>{estimated}</span> : "-"}
                      </td>
                    ) : null}
                    <td>{price.context_tokens ? formatNumber(price.context_tokens) : "-"}</td>
                  </tr>
                );
              })}
              {!loading && visiblePrices.length === 0 ? <tr><td colSpan={priceKind === "go" ? 8 : 6}>{tx("ui.no_data")}</td></tr> : null}
            </tbody>
          </table>
        </div>
      </section>

      <section className="opencode-section opencode-session" aria-label={tx("ui.opencode_session")}>
        <div className="opencode-section-heading">
          <div>
            <strong><Radio size={14} /> {tx("ui.opencode_session")}</strong>
            <span>{tx("ui.opencode_session_description")}</span>
          </div>
          <a className="button button-quiet" href="https://opencode.ai/docs/go/#where-can-i-use-it" target="_blank" rel="noopener noreferrer">
            <ExternalLink size={15} />{tx("ui.opencode_session_docs")}
          </a>
        </div>
        <dl className="opencode-test-result">
          <div><dt>{tx("ui.status")}</dt><dd>{session?.enabled && session?.salt_ready ? tx("ui.opencode_session_active") : tx("ui.opencode_session_inactive")}</dd></div>
          <div><dt>{tx("ui.opencode_session_targets")}</dt><dd>{session?.target_models?.length ?? 0} · {session?.target_auth_indexes ?? 0} {tx("ui.opencode_session_channels")}</dd></div>
          <div><dt>{tx("ui.opencode_session_injected")}</dt><dd>{session?.injected_requests ?? 0}</dd></div>
          <div><dt>{tx("ui.opencode_session_distinct")}</dt><dd>{session?.distinct_sessions ?? 0}</dd></div>
        </dl>
      </section>

      {target ? (
        <section className="opencode-section opencode-model-tester" aria-label={tx("ui.opencode_model_test")}>
          <div className="opencode-section-heading">
            <div>
              <strong>{tx("ui.opencode_model_test")}</strong>
              <span>{tx("ui.opencode_model_test_description", { account: target.label })}</span>
            </div>
            <button className="button button-quiet" type="button" onClick={() => { setTarget(null); setTestResult(null); }}>{tx("ui.close")}</button>
          </div>
          <div className="opencode-form">
            <label className="field-block">
              <span>{tx("ui.model")}</span>
              <select value={testModel} onChange={(event) => setTestModel(event.target.value)}>
                {target.models.map((model) => <option key={model} value={model}>{model}</option>)}
              </select>
            </label>
            <div className="opencode-form-actions">
              <button className="button button-primary" type="button" disabled={busy === "model-test" || !testModel} onClick={runModelTest}>
                {busy === "model-test" ? <LoaderCircle className="spin" size={15} /> : <Activity size={15} />}{tx("ui.test")}
              </button>
            </div>
          </div>
          <p className="opencode-note">
            {tx("ui.opencode_model_price")}: {selectedPrice
              ? `${formatPriceUSD(selectedPrice.input_usd_per_million)} / ${formatPriceUSD(selectedPrice.output_usd_per_million)} · ${tx("ui.opencode_price_per_million")}`
              : tx("ui.opencode_no_price")}
          </p>
          {testResult ? (
            <dl className="opencode-test-result">
              <div><dt>{tx("ui.status")}</dt><dd>{statusLabel(testResult.status)}</dd></div>
              <div><dt>{tx("ui.reason")}</dt><dd>{testResult.reason_code || "-"}</dd></div>
              <div><dt>{tx("ui.http_status")}</dt><dd>{testResult.status_code || "-"}</dd></div>
              {typeof testResult.latency_ms === "number" ? <div><dt>{tx("ui.latency")}</dt><dd>{testResult.latency_ms} ms</dd></div> : null}
              {testResult.tested_at ? <div><dt>{tx("ui.tested_at")}</dt><dd>{formatDateTime(testResult.tested_at)}</dd></div> : null}
              {testResult.detail ? <div><dt>{tx("ui.detail")}</dt><dd>{operatorMessage(testResult.detail, locale)}</dd></div> : null}
            </dl>
          ) : null}
        </section>
      ) : null}
    </section>
  );
}

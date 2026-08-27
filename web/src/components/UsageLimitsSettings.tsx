import { AlertTriangle, LoaderCircle, Plus, Save, Trash2 } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import * as api from "../api/client";
import { operatorMessage } from "../format/operatorMessage";
import { useI18n } from "../i18n";
import type {
  UsageLimitRule,
  UsageLimitsConfig,
  UsageLimitsSnapshot,
  UsageModelLimit,
} from "../types";

const defaultRule = (): UsageLimitRule => ({
  enabled: false,
  basis: "account",
  window: "five_hour",
  percent: 80,
  amount_usd: 0,
});
const emptyConfig = (): UsageLimitsConfig => ({
  enabled: false,
  total: defaultRule(),
  models: [],
});

function ruleFrom(rule?: UsageLimitRule): UsageLimitRule {
  return { ...defaultRule(), ...(rule ?? {}) };
}

export function UsageLimitsSettings({
  onAPIError,
  onNotice,
}: {
  onAPIError: (error: unknown) => void;
  onNotice: (message: string) => void;
}) {
  const { tx, locale } = useI18n();
  const [snapshot, setSnapshot] = useState<UsageLimitsSnapshot | null>(null);
  const [config, setConfig] = useState<UsageLimitsConfig>(emptyConfig);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  const refresh = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const next = await api.getUsageLimits();
      setSnapshot(next);
      setConfig({
        enabled: next.config.enabled,
        total: ruleFrom(next.config.total),
        models: (next.config.models ?? []).map((item) => ({
          ...item,
          rule: ruleFrom(item.rule),
        })),
      });
    } catch (caught) {
      if (caught instanceof api.APIError && caught.status === 401)
        onAPIError(caught);
      else
        setError(
          operatorMessage(
            caught instanceof Error ? caught.message : tx("ui.request_failed"),
            locale,
          ),
        );
    } finally {
      setLoading(false);
    }
  }, [locale, onAPIError, tx]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const updateTotal = (patch: Partial<UsageLimitRule>) =>
    setConfig((current) => ({
      ...current,
      total: { ...ruleFrom(current.total), ...patch },
    }));
  const updateModel = (index: number, patch: Partial<UsageModelLimit>) =>
    setConfig((current) => ({
      ...current,
      models: (current.models ?? []).map((item, i) =>
        i === index ? { ...item, ...patch } : item,
      ),
    }));
  const updateModelRule = (index: number, patch: Partial<UsageLimitRule>) =>
    setConfig((current) => ({
      ...current,
      models: (current.models ?? []).map((item, i) =>
        i === index
          ? { ...item, rule: { ...ruleFrom(item.rule), ...patch } }
          : item,
      ),
    }));
  const addModel = () =>
    setConfig((current) => ({
      ...current,
      models: [
        ...(current.models ?? []),
        { model: "", within_total: true, rule: defaultRule() },
      ],
    }));
  const removeModel = (index: number) =>
    setConfig((current) => ({
      ...current,
      models: (current.models ?? []).filter((_, i) => i !== index),
    }));

  const save = async () => {
    setSaving(true);
    setError("");
    try {
      const next = await api.saveUsageLimits(config);
      setSnapshot(next);
      setConfig({
        enabled: next.config.enabled,
        total: ruleFrom(next.config.total),
        models: (next.config.models ?? []).map((item) => ({
          ...item,
          rule: ruleFrom(item.rule),
        })),
      });
      onNotice(tx("ui.usage_limits_saved"));
    } catch (caught) {
      if (caught instanceof api.APIError && caught.status === 401)
        onAPIError(caught);
      else
        setError(
          operatorMessage(
            caught instanceof Error ? caught.message : tx("ui.request_failed"),
            locale,
          ),
        );
    } finally {
      setSaving(false);
    }
  };

  const renderRule = (
    rule: UsageLimitRule,
    update: (patch: Partial<UsageLimitRule>) => void,
    label: string,
  ) => (
    <div className="usage-limit-rule" aria-label={label}>
      <label className="switch-control">
        <input
          type="checkbox"
          checked={rule.enabled}
          onChange={(event) => update({ enabled: event.target.checked })}
        />
        <b>{tx(rule.enabled ? "ui.on_2" : "ui.off_2")}</b>
      </label>
      <label className="filter-control">
        <span>{tx("ui.usage_limit_basis")}</span>
        <select
          value={rule.basis}
          onChange={(event) =>
            update({ basis: event.target.value as UsageLimitRule["basis"] })
          }
        >
          <option value="account">{tx("ui.usage_limit_account")}</option>
          <option value="credit">{tx("ui.usage_limit_credit")}</option>
        </select>
      </label>
      {rule.basis === "account" ? (
        <>
          <label className="filter-control">
            <span>{tx("ui.usage_limit_window")}</span>
            <select
              value={rule.window ?? "five_hour"}
              onChange={(event) =>
                update({
                  window: event.target.value as UsageLimitRule["window"],
                })
              }
            >
              <option value="five_hour">
                {tx("ui.usage_limit_five_hour")}
              </option>
              <option value="seven_day">
                {tx("ui.usage_limit_seven_day")}
              </option>
            </select>
          </label>
          <label className="filter-control">
            <span>{tx("ui.usage_limit_percent")}</span>
            <span className="number-suffix">
              <input
                type="number"
                min="1"
                max="100"
                step="1"
                value={rule.percent ?? 0}
                onChange={(event) =>
                  update({ percent: Number(event.target.value) })
                }
              />
              <b>%</b>
            </span>
          </label>
        </>
      ) : (
        <label className="filter-control">
          <span>{tx("ui.usage_limit_amount")}</span>
          <span className="number-suffix">
            <input
              type="number"
              min="0.01"
              step="0.01"
              value={rule.amount_usd ?? 0}
              onChange={(event) =>
                update({ amount_usd: Number(event.target.value) })
              }
            />
            <b>USD</b>
          </span>
        </label>
      )}
    </div>
  );

  return (
    <section
      className="settings-section usage-limits-settings"
      aria-label={tx("ui.usage_limits")}
    >
      <header>
        <AlertTriangle size={18} />
        <div>
          <strong>{tx("ui.usage_limits")}</strong>
          <span>{tx("ui.usage_limits_description")}</span>
        </div>
      </header>
      {loading ? (
        <div className="empty-state">
          <LoaderCircle className="spin" size={18} />
          {tx("ui.loading")}
        </div>
      ) : (
        <>
          {error ? (
            <div className="automation-error" role="alert">
              <AlertTriangle size={16} />
              <span>{error}</span>
            </div>
          ) : null}
          <label className="switch-control">
            <input
              type="checkbox"
              checked={config.enabled}
              onChange={(event) =>
                setConfig((current) => ({
                  ...current,
                  enabled: event.target.checked,
                }))
              }
            />
            <b>
              {tx(
                config.enabled
                  ? "ui.usage_limits_enabled"
                  : "ui.usage_limits_disabled",
              )}
            </b>
          </label>
          <div className="usage-limit-card">
            <div className="settings-section-heading">
              <div>
                <strong>{tx("ui.usage_limit_total")}</strong>
                <span>{tx("ui.usage_limit_total_description")}</span>
              </div>
            </div>
            {renderRule(
              ruleFrom(config.total),
              updateTotal,
              tx("ui.usage_limit_total"),
            )}
          </div>
          <div className="usage-limit-card">
            <div className="settings-section-heading">
              <div>
                <strong>{tx("ui.usage_limit_models")}</strong>
                <span>{tx("ui.usage_limit_models_description")}</span>
              </div>
              <button
                className="button button-quiet"
                type="button"
                onClick={addModel}
              >
                <Plus size={15} />
                {tx("ui.usage_limit_add_model")}
              </button>
            </div>
            {(config.models ?? []).map((item, index) => (
              <div
                className="usage-limit-model-row"
                key={`${index}-${item.model}`}
              >
                <label className="filter-control">
                  <span>{tx("ui.usage_limit_model_name")}</span>
                  <input
                    value={item.model}
                    placeholder="gpt-5.4"
                    onChange={(event) =>
                      updateModel(index, { model: event.target.value })
                    }
                  />
                </label>
                {renderRule(
                  ruleFrom(item.rule),
                  (patch) => updateModelRule(index, patch),
                  item.model || tx("ui.usage_limit_model_name"),
                )}
                <label className="switch-control">
                  <input
                    type="checkbox"
                    checked={item.within_total}
                    onChange={(event) =>
                      updateModel(index, { within_total: event.target.checked })
                    }
                  />
                  <b>{tx("ui.usage_limit_within_total")}</b>
                </label>
                <button
                  className="icon-button"
                  type="button"
                  title={tx("ui.remove")}
                  aria-label={tx("ui.remove")}
                  onClick={() => removeModel(index)}
                >
                  <Trash2 size={16} />
                </button>
              </div>
            ))}
            {(config.models ?? []).length === 0 ? (
              <div className="empty-state">
                {tx("ui.usage_limit_no_models")}
              </div>
            ) : null}
          </div>
          <div className="usage-limit-meter">
            <span>{tx("ui.usage_limit_credit_used")}</span>
            <strong>${(snapshot?.credit_used_usd ?? 0).toFixed(4)}</strong>
            <small>{tx("ui.usage_limit_credit_note")}</small>
          </div>
          <div className="settings-section-actions">
            <button
              className="button button-primary"
              type="button"
              disabled={saving}
              onClick={() => void save()}
            >
              {saving ? (
                <LoaderCircle className="spin" size={15} />
              ) : (
                <Save size={15} />
              )}
              {tx("ui.save_settings")}
            </button>
          </div>
        </>
      )}
    </section>
  );
}

import { Activity, AlertTriangle, CheckCircle2, FlaskConical, LoaderCircle, ShieldQuestion, XCircle } from "lucide-react";
import { useMemo, useState } from "react";
import type { Account, ModelTestResult, ModelTestStatus } from "../types";
import { technicalLabel } from "../format/accountDisplay";
import { Modal } from "./Modal";
import { useI18n } from "../i18n";
import type { UIMessageKey } from "../i18n/uiText";

interface ModelTestDialogProps {
  account: Account;
  result: ModelTestResult | null;
  error: string;
  testing: boolean;
  experimentalAvailable?: boolean;
  onClose: () => void;
  onTest: (model: string, experimentalWeeklyOverdraft?: boolean) => void;
}

const defaultOpenAIProbeModel = "gpt-5.6-sol";

const modelSuggestions: Record<string, string[]> = {
  codex: [defaultOpenAIProbeModel, "gpt-5.4", "gpt-5.3-codex", "gpt-5.4-mini"],
  openai: [defaultOpenAIProbeModel, "gpt-5.4", "gpt-5.4-mini"],
  claude: ["claude-sonnet-4-5-20250929", "claude-opus-4-5-20251101"],
  gemini: ["gemini-2.0-flash", "gemini-2.5-pro"],
  "gemini-cli": ["gemini-2.0-flash", "gemini-2.5-pro"],
  "gemini-interactions": ["gemini-2.0-flash", "gemini-2.5-pro"],
  aistudio: ["gemini-2.0-flash", "gemini-2.5-pro"],
  xai: ["grok-4", "grok-4-fast"],
};

const statusLabels: Record<ModelTestStatus, UIMessageKey> = {
  available: "ui.model_available",
  unavailable: "ui.model_unavailable",
  unsupported: "ui.testing_unsupported",
  review: "ui.manual_confirmation_required",
};

const reasonLabels: Record<string, UIMessageKey> = {
  model_response_ok: "ui.received_the_expected_model_response",
  model_not_found: "ui.this_account_cannot_use_the_model_or_the_model_does_not_exist",
  account_unavailable: "ui.account_is_currently_unavailable",
  authentication_failed: "ui.authentication_failed_check_credential_status",
  quota_limited: "ui.upstream_quota_or_rate_limited",
  request_timeout: "ui.test_request_timed_out",
  upstream_unavailable: "ui.upstream_service_is_temporarily_unavailable",
  invalid_response: "ui.the_upstream_response_cannot_confirm_model_availability",
  unsupported_provider: "ui.this_provider_does_not_support_safe_model_testing_yet",
};

const quotaWindowLabels: Record<NonNullable<ModelTestResult["quota_window"]>, UIMessageKey> = {
  five_hour: "ui.quota_window_five_hour",
  seven_day: "ui.quota_window_seven_day",
  multiple: "ui.quota_window_multiple",
  five_hour_fallback: "ui.quota_window_five_hour_fallback",
};

export function ModelTestDialog({ account, result, error, testing, experimentalAvailable = false, onClose, onTest }: ModelTestDialogProps) {
  const { locale, tx } = useI18n();
  const provider = (account.provider || account.type || "").trim().toLowerCase();
  const suggestions = useMemo(() => modelSuggestions[provider] || [], [provider]);
  const [model, setModel] = useState(suggestions[0] || "");
  const identity = account.label || account.email || account.name || account.id;
  const valid = model.trim().length > 0 && model.trim().length <= 128;

  return (
    <Modal
      title={tx("ui.model_availability_test")}
      onClose={onClose}
      footer={(
        <>
          <span className="modal-scope">{tx("ui.single_account_minimal_upstream_usage")}</span>
          <button className="button" type="button" disabled={testing} onClick={onClose}>{tx("ui.close")}</button>
          {experimentalAvailable && provider === "codex" ? (
            <button className="button experimental-model-test-button" type="button" disabled={!valid || testing} onClick={() => onTest(model.trim(), true)}>
              {testing ? <LoaderCircle className="spin" size={15} /> : <FlaskConical size={15} />}
              {tx("ui.load_experimental_feature")}
            </button>
          ) : null}
          <button className="button button-primary" type="button" disabled={!valid || testing} onClick={() => onTest(model.trim(), false)}>
            {testing ? <LoaderCircle className="spin" size={15} /> : <Activity size={15} />}
            {tx(testing ? "ui.testing" : result ? "ui.test_again" : "ui.start_test")}
          </button>
        </>
      )}
    >
      <div className="model-test-dialog">
        <div className="model-test-account">
          <span className="model-test-account-icon"><Activity size={18} /></span>
          <div><strong>{identity}</strong><span>{technicalLabel(account.provider || account.type, locale)} · {account.plan_type || (account.account_type === "personal_access_token" ? tx("ui.codex_personal_access_token") : account.account_type) || tx("ui.unknown_type")}</span></div>
        </div>

        <label className="model-test-field">
          <span>{tx("ui.test_model")}</span>
          <input
            aria-label={tx("ui.test_model")}
            list="model-test-suggestions"
            maxLength={128}
            value={model}
            onChange={(event) => setModel(event.target.value)}
            placeholder={tx("ui.enter_model_id")}
            autoComplete="off"
          />
        </label>
        <datalist id="model-test-suggestions">
          {suggestions.map((suggestion) => <option key={suggestion} value={suggestion} />)}
        </datalist>

        {experimentalAvailable && provider === "codex" ? (
          <div className="model-test-experimental-note" role="note"><FlaskConical size={17} /><span>{tx("ui.experimental_model_test_description")}</span></div>
        ) : null}

        {testing ? <div className="model-test-running" role="status"><LoaderCircle className="spin" size={20} /><div><strong>{tx("ui.connecting_to_model")}</strong><span>{model.trim()}</span></div></div> : null}
        {error ? <div className="model-test-error" role="alert"><AlertTriangle size={18} /><span>{error}</span></div> : null}
        {result && !testing ? <ModelTestOutcome result={result} /> : null}
      </div>
    </Modal>
  );
}

function ModelTestOutcome({ result }: { result: ModelTestResult }) {
  const { formatDateTime, tx } = useI18n();
  const Icon = result.status === "available" ? CheckCircle2 : result.status === "unavailable" ? XCircle : ShieldQuestion;
  const responseHeaders = Array.isArray(result.response?.headers) ? result.response.headers : [];
  return (
    <section className={`model-test-outcome outcome-${result.status}`} aria-label={tx("ui.model_test_result")}>
      <div className="model-test-outcome-heading"><Icon size={21} /><div><strong>{tx(statusLabels[result.status])}</strong><span>{tx(reasonLabels[result.reason_code] || "ui.the_test_result_requires_manual_confirmation")}</span></div></div>
      <dl>
        <div><dt>{tx("ui.model")}</dt><dd>{result.model}</dd></div>
        <div><dt>{tx("ui.http_status")}</dt><dd>{result.status_code || "-"}</dd></div>
        <div><dt>{tx("ui.probe_type")}</dt><dd>{result.probe_kind === "credential" ? tx("ui.credential_probe") : result.probe_kind === "model" ? tx("ui.model_probe") : "-"}</dd></div>
        <div><dt>{tx("ui.latency")}</dt><dd>{result.latency_ms >= 0 ? `${result.latency_ms} ms` : "-"}</dd></div>
        {result.quota_window ? <div><dt>{tx("ui.quota_window")}</dt><dd>{tx(quotaWindowLabels[result.quota_window])}</dd></div> : null}
        <div><dt>{tx("ui.tested_at")}</dt><dd>{formatDateTime(result.tested_at)}</dd></div>
      </dl>
      {result.experiment?.applied ? (
        <div className="model-test-experiment-result">
          <span><FlaskConical size={15} />{tx("ui.experimental_feature_loaded")}</span>
          <div><strong>{tx("ui.correlation_call_id")}</strong><code>{result.experiment.call_id || "-"}</code></div>
        </div>
      ) : null}
      {result.response ? (
        <div className="model-test-response">
          <div className="model-test-response-heading">
            <div><strong>{tx("ui.upstream_response")}</strong><span>{tx("ui.sanitized_response")}</span></div>
            <span>{result.response.format.toUpperCase()}{result.response.truncated ? ` · ${tx("ui.truncated")}` : ""}</span>
          </div>
          {responseHeaders.length > 0 ? (
            <div className="model-test-response-headers" aria-label={tx("ui.response_headers")}>
              {responseHeaders.map((header) => <div key={`${header.name}:${header.value}`}><code>{header.name}</code><span>{header.value}</span></div>)}
            </div>
          ) : null}
          <pre aria-label={tx("ui.response_body")}><code>{result.response.body || tx("ui.empty_response_body")}</code></pre>
        </div>
      ) : null}
    </section>
  );
}

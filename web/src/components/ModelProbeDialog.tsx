import { Activity, AlertTriangle, CheckCircle2, LoaderCircle, ShieldQuestion, XCircle } from "lucide-react";
import type { ReactNode } from "react";
import { decodeHTMLCharacterReferences } from "../format/htmlCharacterReferences";
import { openCodeReasonKey } from "../format/openCodeModelTest";
import { useI18n } from "../i18n";
import type { UIMessageKey } from "../i18n/uiText";
import { Modal } from "./Modal";

/**
 * The model pages test one model through a chosen credential. That is the same interaction the
 * accounts page offers, so it uses the same dialog instead of an inline block: an inline panel
 * had nowhere to go when no credential could be resolved and only left an empty band under the
 * table.
 */

export interface ModelProbeTarget {
  id: string;
  label: string;
}

interface ModelProbeDialogProps {
  model: string;
  targets: ModelProbeTarget[];
  targetID: string;
  onSelectTarget: (id: string) => void;
  onRun: () => void;
  onClose: () => void;
  testing: boolean;
  /** Set when the target list had to fall back to every credential of the family. */
  fallbackHint?: boolean;
  error?: string;
  /** Result rendering, supplied by the caller because the two families report differently. */
  children?: ReactNode;
}

export function ModelProbeDialog({
  model,
  targets,
  targetID,
  onSelectTarget,
  onRun,
  onClose,
  testing,
  fallbackHint = false,
  error = "",
  children,
}: ModelProbeDialogProps) {
  const { tx } = useI18n();
  const runnable = targets.length > 0 && targetID !== "" && !testing;
  return (
    <Modal
      title={tx("ui.model_availability_test")}
      onClose={onClose}
      footer={(
        <>
          <span className="modal-scope">{tx("ui.model_probe_scope")}</span>
          <button className="button" type="button" disabled={testing} onClick={onClose}>{tx("ui.close")}</button>
          <button className="button button-primary" type="button" disabled={!runnable} onClick={onRun}>
            {testing ? <LoaderCircle className="spin" size={15} /> : <Activity size={15} />}
            {tx(testing ? "ui.testing" : "ui.start_test")}
          </button>
        </>
      )}
    >
      <div className="model-test-dialog">
        <div className="model-test-account">
          <span className="model-test-account-icon"><Activity size={18} /></span>
          <div><strong>{model}</strong><span>{tx("ui.model_test_dialog_description")}</span></div>
        </div>

        <label className="model-test-field">
          <span>{tx("ui.model_test_target")}</span>
          {targets.length === 0 ? (
            <input value="" readOnly aria-label={tx("ui.model_test_target")} placeholder={tx("ui.model_test_target_placeholder")} />
          ) : targets.length === 1 ? (
            <input value={targets[0].label} readOnly aria-label={tx("ui.model_test_target")} />
          ) : (
            <select aria-label={tx("ui.model_test_target")} value={targetID} onChange={(event) => onSelectTarget(event.target.value)}>
              {targets.map((target) => <option key={target.id} value={target.id}>{target.label}</option>)}
            </select>
          )}
        </label>

        {targets.length === 0 ? (
          <div className="model-test-error" role="status"><AlertTriangle size={18} /><span>{tx("ui.model_test_no_credentials")}</span></div>
        ) : null}
        {fallbackHint && targets.length > 0 ? (
          <div className="model-test-experimental-note" role="note"><AlertTriangle size={17} /><span>{tx("ui.model_test_fallback_credentials")}</span></div>
        ) : null}

        {testing ? <div className="model-test-running" role="status"><LoaderCircle className="spin" size={20} /><div><strong>{tx("ui.connecting_to_model")}</strong><span>{model}</span></div></div> : null}
        {error && !testing ? <div className="model-test-error" role="alert"><AlertTriangle size={18} /><span>{error}</span></div> : null}
        {!testing ? children : null}
      </div>
    </Modal>
  );
}


/**
 * The probe outcome uses the same markup and classes as the accounts model test, so the two pages
 * look identical: outcome banner, definition list and the sanitized upstream response.
 */

const probeStatusKeys: Record<string, UIMessageKey> = {
  available: "ui.model_available",
  unavailable: "ui.model_unavailable",
  unsupported: "ui.testing_unsupported",
  review: "ui.manual_confirmation_required",
};

export interface ModelProbeOutcomeProps {
  status: string;
  model?: string;
  reasonCode?: string;
  statusCode?: number;
  latencyMs?: number;
  testedAt?: string;
  /** The protocol that answered, when the probe walked several. */
  endpoint?: string;
  triedEndpoints?: string[];
  /** The sanitized upstream body, shown as the response block. */
  detail?: string;
}

export function ModelProbeOutcome({ status, model, reasonCode, statusCode, latencyMs, testedAt, endpoint, triedEndpoints, detail }: ModelProbeOutcomeProps) {
  const { formatDateTime, tx } = useI18n();
  const Icon = status === "available" ? CheckCircle2 : status === "unavailable" ? XCircle : ShieldQuestion;
  const reasonKey = openCodeReasonKey(reasonCode);
  return (
    <section className={`model-test-outcome outcome-${status}`} aria-label={tx("ui.model_test_result")}>
      <div className="model-test-outcome-heading">
        <Icon size={21} />
        <div>
          <strong>{tx(probeStatusKeys[status] ?? "ui.the_test_result_requires_manual_confirmation")}</strong>
          <span>{reasonKey ? tx(reasonKey) : tx("ui.the_test_result_requires_manual_confirmation")}</span>
        </div>
      </div>
      <dl>
        <div><dt>{tx("ui.model")}</dt><dd>{model || "-"}</dd></div>
        <div><dt>{tx("ui.model_test_result_reason")}</dt><dd>{reasonCode || "-"}</dd></div>
        {endpoint ? (
          <div>
            <dt>{tx("ui.model_test_endpoint")}</dt>
            <dd>{endpoint}{triedEndpoints?.length ? ` · ${tx("ui.model_test_endpoint_tried", { list: triedEndpoints.join(", ") })}` : ""}</dd>
          </div>
        ) : null}
        <div><dt>{tx("ui.http_status")}</dt><dd>{statusCode || "-"}</dd></div>
        <div><dt>{tx("ui.latency")}</dt><dd>{typeof latencyMs === "number" ? `${latencyMs} ms` : "-"}</dd></div>
        {testedAt ? <div><dt>{tx("ui.tested_at")}</dt><dd>{formatDateTime(testedAt)}</dd></div> : null}
      </dl>
      {detail ? (
        <div className="model-test-response">
          <div className="model-test-response-heading">
            <div><strong>{tx("ui.upstream_response")}</strong><span>{tx("ui.sanitized_response")}</span></div>
          </div>
          <pre aria-label={tx("ui.response_body")}><code>{decodeHTMLCharacterReferences(detail)}</code></pre>
        </div>
      ) : null}
    </section>
  );
}

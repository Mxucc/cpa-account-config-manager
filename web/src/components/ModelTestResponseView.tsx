import { decodeHTMLCharacterReferences } from "../format/htmlCharacterReferences";
import { useI18n } from "../i18n";
import type { ModelTestResponsePreview } from "../types";

/**
 * The sanitized upstream response, rendered exactly like the accounts model test: response
 * headers, then the redacted JSON body. Shared so every test dialog shows the same thing.
 */
export function ModelTestResponseView({ response }: { response: ModelTestResponsePreview }) {
  const { tx } = useI18n();
  const responseHeaders = Array.isArray(response.headers) ? response.headers : [];
  const responseBody = response.body ? decodeHTMLCharacterReferences(response.body) : tx("ui.empty_response_body");
  return (
    <div className="model-test-response">
      <div className="model-test-response-heading">
        <div><strong>{tx("ui.upstream_response")}</strong><span>{tx("ui.sanitized_response")}</span></div>
        <span>{response.format.toUpperCase()}{response.truncated ? ` · ${tx("ui.truncated")}` : ""}</span>
      </div>
      {responseHeaders.length > 0 ? (
        <div className="model-test-response-headers" aria-label={tx("ui.response_headers")}>
          {responseHeaders.map((header) => <div key={`${header.name}:${header.value}`}><code>{header.name}</code><span>{header.value}</span></div>)}
        </div>
      ) : null}
      <pre aria-label={tx("ui.response_body")}><code>{responseBody}</code></pre>
    </div>
  );
}

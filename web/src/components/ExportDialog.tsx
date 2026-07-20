import {
  Archive,
  Boxes,
  Check,
  Download,
  FileJson2,
  FolderCog,
  Gauge,
  KeyRound,
  LoaderCircle,
  Network,
  Route,
  Rows3,
  ShieldCheck,
  SquareTerminal,
  Table2,
} from "lucide-react";
import { useState } from "react";
import type { AccountExportFormat, ExportFormat, ResultExportFormat } from "../types";
import { Modal } from "./Modal";
import { useI18n } from "../i18n";
import type { UIMessageKey } from "../i18n/uiText";

interface ExportDialogProps {
  kind: "accounts" | "results";
  count: number;
  scopeLabel?: string;
  exporting: boolean;
  error?: string;
  onClose: () => void;
  onExport: (format: ExportFormat) => void;
}

interface FormatOption {
  id: ExportFormat;
  label: string;
  extension: string;
  detail: UIMessageKey;
  icon: typeof FileJson2;
}

const accountFormats: Array<FormatOption & { id: AccountExportFormat }> = [
  { id: "cpa", label: "CPA", extension: ".json / .zip", detail: "ui.multi_account_zip", icon: Archive },
  { id: "sub2api", label: "sub2api", extension: ".json", detail: "ui.account_batch", icon: Boxes },
  { id: "cockpit", label: "Cockpit", extension: ".json", detail: "ui.codex_credentials", icon: Gauge },
  { id: "9router", label: "9router", extension: ".json", detail: "ui.oauth_accounts", icon: Route },
  { id: "codex", label: "Codex", extension: "auth.json", detail: "ui.native_format", icon: SquareTerminal },
  { id: "axonhub", label: "AxonHub", extension: ".json", detail: "ui.codex_auth", icon: Network },
  { id: "codexmanager", label: "Codex-Manager", extension: ".json", detail: "ui.batch_import", icon: FolderCog },
];

const resultFormats: Array<FormatOption & { id: ResultExportFormat }> = [
  { id: "json", label: "JSON", extension: ".json", detail: "ui.structured", icon: FileJson2 },
  { id: "csv", label: "CSV", extension: ".csv", detail: "ui.table", icon: Table2 },
  { id: "jsonl", label: "JSON Lines", extension: ".jsonl", detail: "ui.line_delimited", icon: Rows3 },
];

export function ExportDialog({ kind, count, scopeLabel, exporting, error = "", onClose, onExport }: ExportDialogProps) {
  const { tx } = useI18n();
  const formats: FormatOption[] = kind === "accounts" ? accountFormats : resultFormats;
  const [format, setFormat] = useState<ExportFormat>(kind === "accounts" ? "cpa" : "json");
  const selected = formats.find((option) => option.id === format) ?? formats[0];
  const title = tx(kind === "accounts" ? "ui.download_account_credentials" : "ui.export_results");
  return (
    <Modal
      title={title}
      onClose={onClose}
      footer={(
        <>
          <button className="button" type="button" disabled={exporting} onClick={onClose}>{tx("ui.cancel")}</button>
          <button className="button button-primary" type="button" disabled={exporting} onClick={() => onExport(format)}>
            {exporting ? <LoaderCircle className="spin" size={15} /> : <Download size={15} />}{tx("ui.download_format", { format: selected.label })}
          </button>
        </>
      )}
    >
      <div className={`export-dialog ${kind === "accounts" ? "credential-export" : "result-export"}`}>
        <div className="export-summary">
          <div><span>{tx("ui.scope")}</span><strong>{scopeLabel ?? tx(kind === "accounts" ? "ui.current_filters" : "ui.current_job")}</strong></div>
          <div><span>{tx("ui.accounts_slash_records")}</span><strong>{count}</strong></div>
          <div className={kind === "accounts" ? "export-sensitive" : "export-redacted"}>
            <span>{tx("ui.contents")}</span>
            <strong>{kind === "accounts" ? <><KeyRound size={14} />{tx("ui.includes_credentials")}</> : <><ShieldCheck size={14} />{tx("ui.redacted")}</>}</strong>
          </div>
        </div>
        <div className="export-format-options" role="radiogroup" aria-label={tx("ui.export_format")}>
          {formats.map((option) => {
            const FormatIcon = option.icon;
            const active = format === option.id;
            return (
              <label
                key={option.id}
                className={active ? "active" : ""}
              >
                <input
                  type="radio"
                  name={`export-format-${kind}`}
                  value={option.id}
                  checked={active}
                  disabled={exporting}
                  aria-label={`${option.label} ${tx(option.detail)} ${option.extension}`}
                  onChange={() => setFormat(option.id)}
                />
                <FormatIcon className="export-format-icon" size={19} aria-hidden="true" />
                <span><strong>{option.label}</strong><small>{tx(option.detail)}</small></span>
                <code>{option.extension}</code>
                {active ? <Check className="export-selected-check" size={15} aria-hidden="true" /> : <span className="export-selected-check" aria-hidden="true" />}
              </label>
            );
          })}
        </div>
        {error ? <div className="form-error" role="alert">{error}</div> : null}
      </div>
    </Modal>
  );
}

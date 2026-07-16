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
  detail: string;
  icon: typeof FileJson2;
}

const accountFormats: Array<FormatOption & { id: AccountExportFormat }> = [
  { id: "cpa", label: "CPA", extension: ".json / .zip", detail: "多账号 ZIP", icon: Archive },
  { id: "sub2api", label: "sub2api", extension: ".json", detail: "批量账号", icon: Boxes },
  { id: "cockpit", label: "Cockpit", extension: ".json", detail: "Codex 凭据", icon: Gauge },
  { id: "9router", label: "9router", extension: ".json", detail: "OAuth 账号", icon: Route },
  { id: "codex", label: "Codex", extension: "auth.json", detail: "原生格式", icon: SquareTerminal },
  { id: "axonhub", label: "AxonHub", extension: ".json", detail: "Codex Auth", icon: Network },
  { id: "codexmanager", label: "Codex-Manager", extension: ".json", detail: "批量导入", icon: FolderCog },
];

const resultFormats: Array<FormatOption & { id: ResultExportFormat }> = [
  { id: "json", label: "JSON", extension: ".json", detail: "结构化", icon: FileJson2 },
  { id: "csv", label: "CSV", extension: ".csv", detail: "表格", icon: Table2 },
  { id: "jsonl", label: "JSON Lines", extension: ".jsonl", detail: "逐行", icon: Rows3 },
];

export function ExportDialog({ kind, count, scopeLabel, exporting, error = "", onClose, onExport }: ExportDialogProps) {
  const formats: FormatOption[] = kind === "accounts" ? accountFormats : resultFormats;
  const [format, setFormat] = useState<ExportFormat>(kind === "accounts" ? "cpa" : "json");
  const selected = formats.find((option) => option.id === format) ?? formats[0];
  const title = kind === "accounts" ? "下载账号凭据" : "导出结果";
  return (
    <Modal
      title={title}
      onClose={onClose}
      footer={(
        <>
          <button className="button" type="button" disabled={exporting} onClick={onClose}>取消</button>
          <button className="button button-primary" type="button" disabled={exporting} onClick={() => onExport(format)}>
            {exporting ? <LoaderCircle className="spin" size={15} /> : <Download size={15} />}下载 {selected.label}
          </button>
        </>
      )}
    >
      <div className={`export-dialog ${kind === "accounts" ? "credential-export" : "result-export"}`}>
        <div className="export-summary">
          <div><span>范围</span><strong>{scopeLabel ?? (kind === "accounts" ? "当前筛选" : "当前任务")}</strong></div>
          <div><span>账号 / 记录</span><strong>{count}</strong></div>
          <div className={kind === "accounts" ? "export-sensitive" : "export-redacted"}>
            <span>内容</span>
            <strong>{kind === "accounts" ? <><KeyRound size={14} />包含凭据</> : <><ShieldCheck size={14} />脱敏</>}</strong>
          </div>
        </div>
        <div className="export-format-options" role="radiogroup" aria-label="导出格式">
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
                  aria-label={`${option.label} ${option.detail} ${option.extension}`}
                  onChange={() => setFormat(option.id)}
                />
                <FormatIcon className="export-format-icon" size={19} aria-hidden="true" />
                <span><strong>{option.label}</strong><small>{option.detail}</small></span>
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

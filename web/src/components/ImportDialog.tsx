import {
  AlertCircle,
  AlertTriangle,
  CheckCircle2,
  FileArchive,
  FileJson2,
  FileText,
  Files,
  LoaderCircle,
  RotateCcw,
  Trash2,
  Upload,
  XCircle,
} from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import type { ImportPreview, ImportResult } from "../types";
import { IconButton } from "./IconButton";
import { Modal } from "./Modal";
import { useI18n } from "../i18n";
import type { Locale } from "../i18n";
import { translateUI, type UIMessageKey } from "../i18n/uiText";

const MAX_UPLOAD_FILES = 64;
const MAX_INPUT_BYTES = 12 << 20;
const MAX_VISIBLE_IMPORT_ROWS = 250;

interface ImportDialogProps {
  preview: ImportPreview | null;
  result: ImportResult | null;
  previewing: boolean;
  importing: boolean;
  error?: string;
  onClose: () => void;
  onPreview: (files: File[]) => void;
  onImport: () => void;
  onReset: () => void;
}

export function ImportDialog({
  preview,
  result,
  previewing,
  importing,
  error = "",
  onClose,
  onPreview,
  onImport,
  onReset,
}: ImportDialogProps) {
  const { locale, tx } = useI18n();
  const [sourceMode, setSourceMode] = useState<"files" | "paste">("files");
  const [files, setFiles] = useState<File[]>([]);
  const [jsonText, setJSONText] = useState("");
  const [inputError, setInputError] = useState("");

  useEffect(() => {
    if (!preview) return;
    setFiles([]);
    setJSONText("");
  }, [preview?.id]);

  const totalBytes = useMemo(() => files.reduce((total, file) => total + file.size, 0), [files]);
  const canPreview = sourceMode === "files" ? files.length > 0 : jsonText.trim().length > 0;
  const visibleError = inputError || error;

  const selectFiles = (selected: FileList | null) => {
    if (!selected) return;
    const incoming = Array.from(selected);
    const invalid = incoming.find((file) => !isSupportedImportFile(file));
    if (invalid) {
      setInputError(tx("ui.file_is_not_a_json_text_json_or_zip_file", { file: invalid.name }));
      return;
    }
    const merged = [...files];
    const keys = new Set(merged.map(fileKey));
    incoming.forEach((file) => {
      const key = fileKey(file);
      if (!keys.has(key)) {
        keys.add(key);
        merged.push(file);
      }
    });
    if (merged.length > MAX_UPLOAD_FILES) {
      setInputError(tx("ui.select_at_most_count_files_at_once", { count: MAX_UPLOAD_FILES }));
      return;
    }
    if (merged.reduce((total, file) => total + file.size, 0) > MAX_INPUT_BYTES) {
      setInputError(tx("ui.selected_files_exceed_12_mib_in_total"));
      return;
    }
    setFiles(merged);
    setInputError("");
  };

  const removeFile = (target: File) => {
    setFiles((current) => current.filter((file) => file !== target));
    setInputError("");
  };

  const submitPreview = () => {
    setInputError("");
    if (sourceMode === "files") {
      if (!files.length) {
        setInputError(tx("ui.select_json_text_json_or_zip_files"));
        return;
      }
      onPreview(files);
      return;
    }
    const body = jsonText.trim();
    if (!body) {
      setInputError(tx("ui.enter_json_content"));
      return;
    }
    const pasted = new File([body], "pasted-import.txt", { type: "text/plain" });
    if (pasted.size > MAX_INPUT_BYTES) {
      setInputError(tx("ui.json_content_exceeds_12_mib"));
      return;
    }
    onPreview([pasted]);
  };

  const reset = () => {
    setFiles([]);
    setJSONText("");
    setInputError("");
    onReset();
  };

  return (
    <Modal
      title={tx("ui.add_accounts")}
      wide
      onClose={onClose}
      footer={result ? (
        <>
          <button className="button" type="button" onClick={reset}><RotateCcw size={15} />{tx("ui.add_more")}</button>
          <button className="button button-primary" type="button" onClick={onClose}>{tx("ui.close")}</button>
        </>
      ) : preview ? (
        <>
          <span className="modal-scope">{tx("ui.snapshot_id", { id: preview.id.slice(0, 8) })}</span>
          <button className="button" type="button" disabled={importing} onClick={reset}>{tx("ui.select_again")}</button>
          <button className="button button-primary" type="button" disabled={importing || preview.total === 0} onClick={onImport}>
            {importing ? <LoaderCircle className="spin" size={15} /> : <Upload size={15} />}{tx("ui.add_count_accounts", { count: preview.total })}
          </button>
        </>
      ) : (
        <>
          <button className="button" type="button" onClick={onClose}>{tx("ui.cancel")}</button>
          <button className="button button-primary" type="button" disabled={previewing || !canPreview} onClick={submitPreview}>
            {previewing ? <LoaderCircle className="spin" size={15} /> : <FileJson2 size={15} />}{tx("ui.generate_preview")}
          </button>
        </>
      )}
    >
      {result ? <ImportResultView result={result} /> : preview ? <ImportPreviewView preview={preview} /> : (
        <div className="import-input-stage">
          <div className="import-source-segment" role="group" aria-label={tx("ui.import_source")}>
            <button type="button" aria-pressed={sourceMode === "files"} className={sourceMode === "files" ? "active" : ""} onClick={() => { setSourceMode("files"); setInputError(""); }}><Files size={15} />{tx("ui.multiple_files")}</button>
            <button type="button" aria-pressed={sourceMode === "paste"} className={sourceMode === "paste" ? "active" : ""} onClick={() => { setSourceMode("paste"); setInputError(""); }}><FileText size={15} />{tx("ui.text_json")}</button>
          </div>

          {sourceMode === "files" ? (
            <div className="import-file-input">
              <div className="import-file-toolbar">
                <label className="button import-file-button">
                  <Upload size={15} />{tx("ui.select_json_slash_text_slash_zip")}
                  <input type="file" accept=".json,.jsonl,.ndjson,.txt,.zip,application/json,application/x-ndjson,text/plain,application/zip" multiple aria-label={tx("ui.select_json_text_json_or_zip_files_2")} onChange={(event) => { selectFiles(event.currentTarget.files); event.currentTarget.value = ""; }} />
                </label>
                <span>{files.length}/{MAX_UPLOAD_FILES}</span>
                <span>{formatBytes(totalBytes)}</span>
              </div>
              <div className="import-file-list" aria-label={tx("ui.selected_import_files")}>
                {files.length ? files.map((file) => (
                  <div className="import-file-row" key={fileKey(file)}>
                    {isZIPFile(file) ? <FileArchive size={17} /> : isTextJSONFile(file) ? <FileText size={17} /> : <FileJson2 size={17} />}
                    <div><strong>{file.name}</strong><span>{importFileType(file)} · {formatBytes(file.size)}</span></div>
                    <IconButton label={tx("ui.remove_file", { file: file.name })} onClick={() => removeFile(file)}><Trash2 size={15} /></IconButton>
                  </div>
                )) : <div className="import-file-empty"><Files size={24} /><span>JSON / TEXT / ZIP</span></div>}
              </div>
            </div>
          ) : (
            <label className="import-json-input">
              <span>{tx("ui.json_text")}</span>
              <textarea aria-label={tx("ui.json_text")} value={jsonText} spellCheck={false} onChange={(event) => { setJSONText(event.target.value); setInputError(""); }} placeholder='{"accounts":[...]}' />
              <b>{formatBytes(new Blob([jsonText]).size)}</b>
            </label>
          )}
        </div>
      )}
      {visibleError ? <div className="import-error" role="alert"><AlertCircle size={17} /><span>{visibleError}</span></div> : null}
    </Modal>
  );
}

function ImportPreviewView({ preview }: { preview: ImportPreview }) {
  const { locale, tx } = useI18n();
  const visibleItems = preview.items.slice(0, MAX_VISIBLE_IMPORT_ROWS);
  const hiddenItems = Math.max(0, preview.total - visibleItems.length);
  return (
    <div className="import-preview-stage">
      <div className="import-metrics">
        <ImportMetric label={tx("ui.accounts")} value={preview.total} tone="success" />
        <ImportMetric label={tx("ui.json_files")} value={preview.source_files} />
        <ImportMetric label={tx("ui.skipped")} value={preview.skipped} tone={preview.skipped ? "warning" : ""} />
        <ImportMetric label={tx("ui.input")} value={preview.input_type.toUpperCase()} />
      </div>
      {preview.warnings?.length ? <div className="warning-list">{preview.warnings.map((warning) => <div key={warning}><AlertTriangle size={15} />{importMessage(warning, locale)}</div>)}</div> : null}
      <div className="import-records">
        <div className="import-record-header"><span>{tx("ui.accounts")}</span><span>{tx("ui.source")}</span><span>{tx("ui.cpa_file")}</span><span>{tx("ui.status")}</span></div>
        <div className="import-record-list">
          {visibleItems.map((item) => (
            <div className="import-record" key={`${item.index}:${item.target_name}`}>
              <div className="import-record-identity"><strong>{item.label}</strong><span>{item.account_id || item.email || `#${item.index}`}</span></div>
              <div className="import-record-source"><strong>{item.source_name}</strong><span>{item.source_path || "$"}</span></div>
              <code>{item.target_name}</code>
              <span className={item.warnings?.length ? "import-record-state warning" : "import-record-state success"}>{item.warnings?.length ? tx("ui.count_warnings", { count: item.warnings.length }) : tx("ui.ready")}</span>
            </div>
          ))}
          {hiddenItems ? <div className="import-list-overflow" role="status">{tx("ui.count_more_accounts_not_shown", { count: hiddenItems })}</div> : null}
        </div>
      </div>
      {preview.skipped_items?.length ? (
        <div className="import-skipped-list">
          {preview.skipped_items.map((item, index) => <div key={`${item.source_name}:${item.source_path}:${index}`}><XCircle size={14} /><span>{item.source_name}</span><b>{importMessage(item.reason, locale)}</b></div>)}
        </div>
      ) : null}
    </div>
  );
}

function ImportResultView({ result }: { result: ImportResult }) {
  const { locale, tx } = useI18n();
  const complete = result.state === "completed";
  const visibleResults = result.results.slice(0, MAX_VISIBLE_IMPORT_ROWS);
  const hiddenResults = Math.max(0, result.total - visibleResults.length);
  return (
    <div className="import-result-stage">
      <div className={`import-result-banner state-${result.state}`}>
        {complete ? <CheckCircle2 size={22} /> : result.state === "partial" ? <AlertTriangle size={22} /> : <XCircle size={22} />}
        <div><strong>{complete ? tx("ui.import_complete") : result.state === "partial" ? tx("ui.import_partially_complete") : tx("ui.import_failed")}</strong><span>{tx("ui.imported_slash_total_written_to_cpa", { imported: result.imported, total: result.total })}</span></div>
      </div>
      <div className="import-metrics">
        <ImportMetric label={tx("ui.total")} value={result.total} />
        <ImportMetric label={tx("ui.imported")} value={result.imported} tone="success" />
        <ImportMetric label={tx("ui.skipped")} value={result.skipped} tone={result.skipped ? "warning" : ""} />
        <ImportMetric label={tx("ui.failed")} value={result.failed} tone={result.failed ? "danger" : ""} />
      </div>
      {result.usage_collection_started ? <div className="import-usage-collection" role="status"><LoaderCircle className="spin" size={15} /><span>{tx("ui.collecting_usage_for_count_imported_accounts", { count: result.usage_collection_targets ?? result.imported })}</span></div> : null}
      <div className="import-result-list">
        {visibleResults.map((item) => (
          <div className="import-result-row" key={`${item.index}:${item.target_name}`}>
            {item.status === "imported" ? <CheckCircle2 size={16} /> : item.status === "skipped" ? <AlertTriangle size={16} /> : <XCircle size={16} />}
            <div><strong>{item.label}</strong><span>{item.target_name}</span></div>
            <b className={`status-${item.status}`}>{importStatus(item.status, locale)}</b>
            <small>{item.error ? importMessage(item.error, locale) : item.source_name}</small>
          </div>
        ))}
        {hiddenResults ? <div className="import-list-overflow" role="status">{tx("ui.count_more_accounts_not_shown", { count: hiddenResults })}</div> : null}
      </div>
    </div>
  );
}

function ImportMetric({ label, value, tone = "" }: { label: string; value: number | string; tone?: string }) {
  return <div className={`import-metric ${tone}`}><span>{label}</span><strong>{value}</strong></div>;
}

function isSupportedImportFile(file: File): boolean {
  const name = file.name.toLowerCase();
  return [".json", ".jsonl", ".ndjson", ".txt", ".zip"].some((extension) => name.endsWith(extension));
}

function isZIPFile(file: File): boolean {
  return file.name.toLowerCase().endsWith(".zip");
}

function isTextJSONFile(file: File): boolean {
  const name = file.name.toLowerCase();
  return name.endsWith(".txt") || name.endsWith(".jsonl") || name.endsWith(".ndjson");
}

function importFileType(file: File): string {
  if (isZIPFile(file)) return "ZIP";
  if (file.name.toLowerCase().endsWith(".jsonl") || file.name.toLowerCase().endsWith(".ndjson")) return "JSONL";
  if (isTextJSONFile(file)) return "TEXT JSON";
  return "JSON";
}

function fileKey(file: File): string {
  return `${file.name}:${file.size}:${file.lastModified}`;
}

function formatBytes(size: number): string {
  if (size >= (1 << 20)) return `${(size / (1 << 20)).toFixed(size >= (10 << 20) ? 0 : 1)} MiB`;
  if (size >= 1 << 10) return `${(size / (1 << 10)).toFixed(1)} KiB`;
  return `${size} B`;
}

function importStatus(status: ImportResult["results"][number]["status"], locale: Locale): string {
  return translateUI(locale, ({ imported: "ui.imported", skipped: "ui.skipped_2", failed: "ui.failed" } as const)[status]);
}

function importMessage(message: string, locale: Locale): string {
  const exact: Record<string, UIMessageKey> = {
    "existing Auth files will not be overwritten": "ui.existing_auth_files_will_not_be_overwritten",
    "refresh token is missing": "ui.refresh_token_is_missing",
    "ID token was synthesized from account metadata": "ui.id_token_was_synthesized_from_account_metadata",
    "account ID is missing": "ui.account_id_is_missing",
    "filename was adjusted to avoid an existing Auth file": "ui.filename_was_adjusted_to_avoid_an_existing_auth_file",
    "entry is not a JSON or text JSON file": "ui.zip_entry_is_not_a_json_or_text_json_file",
    "entry does not contain valid JSON": "ui.zip_entry_does_not_contain_valid_json",
    "uploaded file does not contain valid JSON": "ui.uploaded_file_does_not_contain_valid_json",
    "uploaded file is empty": "ui.uploaded_file_is_empty",
    "duplicate credential record": "ui.duplicate_credential_record",
    "target Auth file already exists": "ui.target_auth_file_already_exists",
    "could not verify the target Auth filename": "ui.could_not_verify_the_target_auth_filename",
    "CPA rejected the converted Auth file": "ui.cpa_rejected_the_converted_auth_file",
    "import was cancelled": "ui.import_was_cancelled",
  };
  if (exact[message]) return translateUI(locale, exact[message]);
  const skipped = message.match(/^(\d+) unsupported or duplicate record\(s\) were skipped$/);
  if (skipped) return translateUI(locale, "ui.count_unsupported_or_duplicate_record_s_were_skipped", { count: skipped[1] });
  return message;
}

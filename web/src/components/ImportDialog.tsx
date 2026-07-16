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
      setInputError(`${invalid.name} 不是 JSON、文本 JSON 或 ZIP 文件`);
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
      setInputError(`一次最多选择 ${MAX_UPLOAD_FILES} 个文件`);
      return;
    }
    if (merged.reduce((total, file) => total + file.size, 0) > MAX_INPUT_BYTES) {
      setInputError("所选文件总大小超过 12 MiB");
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
        setInputError("请选择 JSON、文本 JSON 或 ZIP 文件");
        return;
      }
      onPreview(files);
      return;
    }
    const body = jsonText.trim();
    if (!body) {
      setInputError("请输入 JSON 内容");
      return;
    }
    const pasted = new File([body], "pasted-import.txt", { type: "text/plain" });
    if (pasted.size > MAX_INPUT_BYTES) {
      setInputError("JSON 内容超过 12 MiB");
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
      title="添加账号"
      wide
      onClose={onClose}
      footer={result ? (
        <>
          <button className="button" type="button" onClick={reset}><RotateCcw size={15} />继续添加</button>
          <button className="button button-primary" type="button" onClick={onClose}>关闭</button>
        </>
      ) : preview ? (
        <>
          <span className="modal-scope">快照 {preview.id.slice(0, 8)}</span>
          <button className="button" type="button" disabled={importing} onClick={reset}>重新选择</button>
          <button className="button button-primary" type="button" disabled={importing || preview.total === 0} onClick={onImport}>
            {importing ? <LoaderCircle className="spin" size={15} /> : <Upload size={15} />}添加 {preview.total} 个账号
          </button>
        </>
      ) : (
        <>
          <button className="button" type="button" onClick={onClose}>取消</button>
          <button className="button button-primary" type="button" disabled={previewing || !canPreview} onClick={submitPreview}>
            {previewing ? <LoaderCircle className="spin" size={15} /> : <FileJson2 size={15} />}生成预览
          </button>
        </>
      )}
    >
      {result ? <ImportResultView result={result} /> : preview ? <ImportPreviewView preview={preview} /> : (
        <div className="import-input-stage">
          <div className="import-source-segment" role="group" aria-label="导入来源">
            <button type="button" aria-pressed={sourceMode === "files"} className={sourceMode === "files" ? "active" : ""} onClick={() => { setSourceMode("files"); setInputError(""); }}><Files size={15} />多文件</button>
            <button type="button" aria-pressed={sourceMode === "paste"} className={sourceMode === "paste" ? "active" : ""} onClick={() => { setSourceMode("paste"); setInputError(""); }}><FileText size={15} />文本 JSON</button>
          </div>

          {sourceMode === "files" ? (
            <div className="import-file-input">
              <div className="import-file-toolbar">
                <label className="button import-file-button">
                  <Upload size={15} />选择 JSON / 文本 / ZIP
                  <input type="file" accept=".json,.jsonl,.ndjson,.txt,.zip,application/json,application/x-ndjson,text/plain,application/zip" multiple aria-label="选择 JSON、文本 JSON 或 ZIP 文件" onChange={(event) => { selectFiles(event.currentTarget.files); event.currentTarget.value = ""; }} />
                </label>
                <span>{files.length}/{MAX_UPLOAD_FILES}</span>
                <span>{formatBytes(totalBytes)}</span>
              </div>
              <div className="import-file-list" aria-label="已选导入文件">
                {files.length ? files.map((file) => (
                  <div className="import-file-row" key={fileKey(file)}>
                    {isZIPFile(file) ? <FileArchive size={17} /> : isTextJSONFile(file) ? <FileText size={17} /> : <FileJson2 size={17} />}
                    <div><strong>{file.name}</strong><span>{importFileType(file)} · {formatBytes(file.size)}</span></div>
                    <IconButton label={`移除 ${file.name}`} onClick={() => removeFile(file)}><Trash2 size={15} /></IconButton>
                  </div>
                )) : <div className="import-file-empty"><Files size={24} /><span>JSON / TEXT / ZIP</span></div>}
              </div>
            </div>
          ) : (
            <label className="import-json-input">
              <span>JSON 文本</span>
              <textarea aria-label="JSON 文本" value={jsonText} spellCheck={false} onChange={(event) => { setJSONText(event.target.value); setInputError(""); }} placeholder='{"accounts":[...]}' />
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
  const visibleItems = preview.items.slice(0, MAX_VISIBLE_IMPORT_ROWS);
  const hiddenItems = Math.max(0, preview.total - visibleItems.length);
  return (
    <div className="import-preview-stage">
      <div className="import-metrics">
        <ImportMetric label="账号" value={preview.total} tone="success" />
        <ImportMetric label="JSON 文件" value={preview.source_files} />
        <ImportMetric label="跳过" value={preview.skipped} tone={preview.skipped ? "warning" : ""} />
        <ImportMetric label="输入" value={preview.input_type.toUpperCase()} />
      </div>
      {preview.warnings?.length ? <div className="warning-list">{preview.warnings.map((warning) => <div key={warning}><AlertTriangle size={15} />{importMessage(warning)}</div>)}</div> : null}
      <div className="import-records">
        <div className="import-record-header"><span>账号</span><span>来源</span><span>CPA 文件</span><span>状态</span></div>
        <div className="import-record-list">
          {visibleItems.map((item) => (
            <div className="import-record" key={`${item.index}:${item.target_name}`}>
              <div className="import-record-identity"><strong>{item.label}</strong><span>{item.account_id || item.email || `#${item.index}`}</span></div>
              <div className="import-record-source"><strong>{item.source_name}</strong><span>{item.source_path || "$"}</span></div>
              <code>{item.target_name}</code>
              <span className={item.warnings?.length ? "import-record-state warning" : "import-record-state success"}>{item.warnings?.length ? `${item.warnings.length} 警告` : "就绪"}</span>
            </div>
          ))}
          {hiddenItems ? <div className="import-list-overflow" role="status">另有 {hiddenItems} 个账号未展开</div> : null}
        </div>
      </div>
      {preview.skipped_items?.length ? (
        <div className="import-skipped-list">
          {preview.skipped_items.map((item, index) => <div key={`${item.source_name}:${item.source_path}:${index}`}><XCircle size={14} /><span>{item.source_name}</span><b>{importMessage(item.reason)}</b></div>)}
        </div>
      ) : null}
    </div>
  );
}

function ImportResultView({ result }: { result: ImportResult }) {
  const complete = result.state === "completed";
  const visibleResults = result.results.slice(0, MAX_VISIBLE_IMPORT_ROWS);
  const hiddenResults = Math.max(0, result.total - visibleResults.length);
  return (
    <div className="import-result-stage">
      <div className={`import-result-banner state-${result.state}`}>
        {complete ? <CheckCircle2 size={22} /> : result.state === "partial" ? <AlertTriangle size={22} /> : <XCircle size={22} />}
        <div><strong>{complete ? "导入完成" : result.state === "partial" ? "部分导入完成" : "导入失败"}</strong><span>{result.imported}/{result.total} 已写入 CPA</span></div>
      </div>
      <div className="import-metrics">
        <ImportMetric label="总数" value={result.total} />
        <ImportMetric label="已导入" value={result.imported} tone="success" />
        <ImportMetric label="跳过" value={result.skipped} tone={result.skipped ? "warning" : ""} />
        <ImportMetric label="失败" value={result.failed} tone={result.failed ? "danger" : ""} />
      </div>
      <div className="import-result-list">
        {visibleResults.map((item) => (
          <div className="import-result-row" key={`${item.index}:${item.target_name}`}>
            {item.status === "imported" ? <CheckCircle2 size={16} /> : item.status === "skipped" ? <AlertTriangle size={16} /> : <XCircle size={16} />}
            <div><strong>{item.label}</strong><span>{item.target_name}</span></div>
            <b className={`status-${item.status}`}>{importStatus(item.status)}</b>
            <small>{item.error ? importMessage(item.error) : item.source_name}</small>
          </div>
        ))}
        {hiddenResults ? <div className="import-list-overflow" role="status">另有 {hiddenResults} 个账号未展开</div> : null}
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

function importStatus(status: ImportResult["results"][number]["status"]): string {
  if (status === "imported") return "已导入";
  if (status === "skipped") return "已跳过";
  return "失败";
}

function importMessage(message: string): string {
  const exact: Record<string, string> = {
    "existing Auth files will not be overwritten": "不会覆盖现有 Auth 文件",
    "refresh token is missing": "缺少 Refresh Token",
    "ID token was synthesized from account metadata": "已根据账号信息生成兼容 ID Token",
    "account ID is missing": "缺少 Account ID",
    "filename was adjusted to avoid an existing Auth file": "目标文件名已避让现有 Auth 文件",
    "entry is not a JSON or text JSON file": "ZIP 条目不是 JSON 或文本 JSON 文件",
    "entry does not contain valid JSON": "ZIP 条目不是有效 JSON",
    "uploaded file does not contain valid JSON": "文件不是有效 JSON",
    "uploaded file is empty": "文件为空",
    "duplicate credential record": "重复账号记录",
    "target Auth file already exists": "目标 Auth 文件已存在",
    "could not verify the target Auth filename": "无法确认目标 Auth 文件名",
    "CPA rejected the converted Auth file": "CPA 拒绝了转换后的 Auth 文件",
    "import was cancelled": "导入已取消",
  };
  if (exact[message]) return exact[message];
  const skipped = message.match(/^(\d+) unsupported or duplicate record\(s\) were skipped$/);
  if (skipped) return `${skipped[1]} 条不支持或重复的记录已跳过`;
  return message;
}

import { Download, RefreshCw, RotateCcw, X } from "lucide-react";
import { operatorMessage } from "../format/operatorMessage";
import type { JobResult, JobState } from "../types";
import { IconButton } from "./IconButton";
import { useI18n } from "../i18n";
import type { Locale } from "../i18n";
import { translateUI, type UIMessageKey } from "../i18n/uiText";

interface JobPanelProps {
  job: ResultJobSnapshot;
  title?: UIMessageKey;
  ariaLabel?: UIMessageKey;
  retrying?: boolean;
  fields?: string[];
  onClose: () => void;
  onRetry?: () => void;
  onExport?: () => void;
  onRefresh: () => void;
}

interface ResultJobSnapshot {
	id?: string;
	operation?: "patch" | "delete";
	state: JobState;
	running: boolean;
	total: number;
	done: number;
	succeeded: number;
	failed: number;
	conflicts: number;
	skipped: number;
	retry_available?: boolean;
	results?: JobResult[];
}

const resultLabels: Record<string, UIMessageKey> = {
  pending: "ui.pending",
  running: "ui.running",
  succeeded: "ui.succeeded",
  failed: "ui.failed",
  conflict: "ui.conflict",
  skipped: "ui.skipped",
  interrupted: "ui.interrupted",
};

export function JobPanel({ job, title = "ui.batch_job", ariaLabel = "ui.batch_job", retrying = false, fields = [], onClose, onRetry, onExport, onRefresh }: JobPanelProps) {
  const { locale, tx } = useI18n();
  const progress = job.total > 0 ? Math.min(100, Math.round((job.done / job.total) * 100)) : 0;
  return (
    <aside className="job-panel" aria-label={tx(ariaLabel)}>
      <header className="job-header">
        <div>
          <span className={`job-state state-${job.state}`}>{job.running ? tx("ui.running") : jobStateLabel(job.state, locale)}</span>
          <h2>{tx(title)}</h2>
          <code>{job.id?.slice(0, 12) || "-"}</code>
        </div>
      </header>
      <div className="job-progress" aria-label={tx("ui.job_progress_progress_percent", { progress })}>
        <div style={{ width: `${progress}%` }} />
      </div>
      <div className="job-counts">
        <Count label={tx("ui.completed")} value={`${job.done}/${job.total}`} />
        <Count label={tx("ui.succeeded")} value={job.succeeded} tone="success" />
        <Count label={tx("ui.failed")} value={job.failed} tone={job.failed ? "danger" : ""} />
        <Count label={tx("ui.conflict")} value={job.conflicts} tone={job.conflicts ? "warning" : ""} />
        <Count label={tx("ui.skipped")} value={job.skipped} />
      </div>
      <div className={`job-toolbar${onExport || onRetry ? "" : " force-job-toolbar"}`}>
        {onExport || onRetry ? (
          <>
            {onExport ? <button className="button button-quiet" type="button" onClick={onExport}><Download size={15} />{tx("ui.export_results")}</button> : null}
            {onRetry ? <button className="button button-primary" type="button" onClick={onRetry} disabled={!job.retry_available || job.running || retrying}><RotateCcw size={15} />{tx(retrying ? "ui.retrying" : "ui.retry_failed_items_only")}</button> : null}
          </>
        ) : (
          <><span>{tx("ui.managed_fields")}</span>{fields.map((field) => <code key={field}>{field}</code>)}</>
        )}
        <div className="job-toolbar-controls">
          <IconButton label={tx("ui.refresh_job")} onClick={onRefresh}><RefreshCw size={16} /></IconButton>
          <IconButton label={tx("ui.close_job_panel")} onClick={onClose}><X size={17} /></IconButton>
        </div>
      </div>
      <div className="job-results">
        {(job.results ?? []).map((result) => (
          <div className="job-result" key={result.id}>
            <span className={`result-status result-${result.status}`}>{tx(resultLabels[result.status] ?? "ui.unknown_status")}</span>
            <div className="job-result-main">
              <strong>{result.label || result.name || result.id}</strong>
              <span>{result.provider || tx("ui.unknown")}</span>
              {result.error ? <small>{operatorMessage(result.error, locale)}</small> : null}
            </div>
            <span className="job-result-fields">{job.operation === "delete" && result.status === "succeeded" ? tx("ui.deleted") : result.applied_fields?.join(", ") || "-"}</span>
          </div>
        ))}
        {!job.results?.length ? <div className="empty-state compact">{tx("ui.no_item_results_yet")}</div> : null}
      </div>
    </aside>
  );
}

function Count({ label, value, tone = "" }: { label: string; value: string | number; tone?: string }) {
  return <div className={tone}><span>{label}</span><strong>{value}</strong></div>;
}

export function jobStateLabel(state: JobState, locale: Locale = "zh-CN"): string {
  const source = ({ idle: "ui.idle", completed: "ui.completed_2", partial: "ui.partially_completed", failed: "ui.failed", interrupted: "ui.interrupted_2", running: "ui.running" } as const)[state];
  return translateUI(locale, source);
}

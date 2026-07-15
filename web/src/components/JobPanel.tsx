import { Download, RefreshCw, RotateCcw, X } from "lucide-react";
import { operatorMessage } from "../format/operatorMessage";
import type { JobResult, JobState } from "../types";
import { IconButton } from "./IconButton";

interface JobPanelProps {
	job: ResultJobSnapshot;
	title?: string;
	ariaLabel?: string;
	retrying?: boolean;
	fields?: string[];
  onClose: () => void;
	onRetry?: () => void;
	onExport?: () => void;
  onRefresh: () => void;
}

interface ResultJobSnapshot {
	id?: string;
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

const resultLabels: Record<string, string> = {
  pending: "等待",
  running: "执行中",
  succeeded: "成功",
  failed: "失败",
  conflict: "冲突",
  skipped: "跳过",
  interrupted: "中断",
};

export function JobPanel({ job, title = "批量任务", ariaLabel = "批量任务", retrying = false, fields = [], onClose, onRetry, onExport, onRefresh }: JobPanelProps) {
  const progress = job.total > 0 ? Math.min(100, Math.round((job.done / job.total) * 100)) : 0;
  return (
    <aside className="job-panel" aria-label={ariaLabel}>
      <header className="job-header">
        <div>
          <span className={`job-state state-${job.state}`}>{job.running ? "执行中" : jobStateLabel(job.state)}</span>
          <h2>{title}</h2>
          <code>{job.id?.slice(0, 12) || "-"}</code>
        </div>
        <div className="job-header-actions">
          <IconButton label="刷新任务" onClick={onRefresh}><RefreshCw size={16} /></IconButton>
          <IconButton label="关闭任务面板" onClick={onClose}><X size={17} /></IconButton>
        </div>
      </header>
      <div className="job-progress" aria-label={`任务进度 ${progress}%`}>
        <div style={{ width: `${progress}%` }} />
      </div>
      <div className="job-counts">
        <Count label="完成" value={`${job.done}/${job.total}`} />
        <Count label="成功" value={job.succeeded} tone="success" />
        <Count label="失败" value={job.failed} tone={job.failed ? "danger" : ""} />
        <Count label="冲突" value={job.conflicts} tone={job.conflicts ? "warning" : ""} />
        <Count label="跳过" value={job.skipped} />
      </div>
      {onExport || onRetry ? (
        <div className="job-toolbar">
          {onExport ? <button className="button button-quiet" type="button" onClick={onExport}><Download size={15} />导出结果</button> : null}
          {onRetry ? <button className="button button-primary" type="button" onClick={onRetry} disabled={!job.retry_available || job.running || retrying}><RotateCcw size={15} />{retrying ? "重试中" : "仅重试失败项"}</button> : null}
        </div>
      ) : (
        <div className="job-toolbar force-job-toolbar"><span>覆盖字段</span>{fields.map((field) => <code key={field}>{field}</code>)}</div>
      )}
      <div className="job-results">
        {(job.results ?? []).map((result) => (
          <div className="job-result" key={result.id}>
            <span className={`result-status result-${result.status}`}>{resultLabels[result.status] ?? result.status}</span>
            <div className="job-result-main">
              <strong>{result.label || result.name || result.id}</strong>
              <span>{result.provider || "unknown"}</span>
              {result.error ? <small>{operatorMessage(result.error)}</small> : null}
            </div>
            <span className="job-result-fields">{result.applied_fields?.join(", ") || "-"}</span>
          </div>
        ))}
        {!job.results?.length ? <div className="empty-state compact">暂无逐项结果</div> : null}
      </div>
    </aside>
  );
}

function Count({ label, value, tone = "" }: { label: string; value: string | number; tone?: string }) {
  return <div className={tone}><span>{label}</span><strong>{value}</strong></div>;
}

export function jobStateLabel(state: JobState): string {
  return ({ idle: "空闲", completed: "已完成", partial: "部分完成", failed: "失败", interrupted: "已中断", running: "执行中" } as const)[state];
}

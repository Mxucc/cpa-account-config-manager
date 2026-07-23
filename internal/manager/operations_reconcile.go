package manager

import (
	"fmt"
	"time"
)

func (a *App) reconcileOperationSources() {
	if a == nil || a.operations == nil {
		return
	}
	if snapshot := a.jobs.Snapshot(false); snapshot.ID != "" {
		a.operations.Upsert("batch:"+snapshot.ID, operationFromJob(snapshot))
	}
	if snapshot := a.force.Snapshot(false); snapshot.ID != "" {
		a.operations.Upsert("force:"+snapshot.ID, operationFromForceSync(snapshot))
	}
	policy := a.policies.Snapshot()
	if !policy.LastScan.StartedAt.IsZero() {
		entry := operationFromPolicyScan(policy.LastScan)
		a.operations.Upsert(operationTimestampEvent("policy-scan", policy.LastScan.StartedAt), entry)
	}
	inspection := a.inspection.Snapshot()
	if !inspection.LastRun.StartedAt.IsZero() {
		entry := operationFromInspectionScan(inspection.LastRun)
		a.operations.Upsert(operationTimestampEvent("inspection-scan", inspection.LastRun.StartedAt), entry)
	}
	for _, action := range a.inspection.Actions(maxInspectionActions) {
		entry, ok := operationFromInspectionAction(action)
		if ok {
			a.operations.Upsert("inspection-action:"+action.ID, entry)
		}
	}
	update := a.updates.Snapshot()
	if !update.CheckedAt.IsZero() {
		entry := operationFromUpdateCheck(update)
		a.operations.Upsert(operationTimestampEvent("update-check", update.CheckedAt), entry)
	}
}

func operationFromJob(snapshot JobSnapshot) OperationEntry {
	action := OperationActionBatchEdit
	if snapshot.Operation == BatchOperationDelete {
		action = OperationActionBatchDelete
	}
	if snapshot.ParentJobID != "" && snapshot.Operation == BatchOperationDelete {
		action = OperationActionBatchDeleteRetry
	} else if snapshot.ParentJobID != "" {
		action = OperationActionBatchRetry
	}
	return OperationEntry{
		Category:     OperationCategoryBatch,
		Action:       action,
		Status:       operationStatusFromJobState(snapshot.State),
		Source:       OperationSourceManual,
		TargetCount:  snapshot.Total,
		Succeeded:    snapshot.Succeeded,
		Failed:       snapshot.Failed + snapshot.Conflicts,
		Skipped:      snapshot.Skipped,
		StartedAt:    snapshot.StartedAt,
		FinishedAt:   snapshot.FinishedAt,
		ReasonCode:   operationReasonFromJobState(snapshot.State),
		RelatedJobID: snapshot.ID,
	}
}

func operationFromForceSync(snapshot ForceSyncJobSnapshot) OperationEntry {
	return OperationEntry{
		Category:     OperationCategoryDefaultPolicy,
		Action:       OperationActionForceSync,
		Status:       operationStatusFromJobState(snapshot.State),
		Source:       OperationSourceManual,
		Scope:        OperationScopeAll,
		TargetCount:  snapshot.Total,
		Succeeded:    snapshot.Succeeded,
		Failed:       snapshot.Failed + snapshot.Conflicts,
		Skipped:      snapshot.Skipped,
		StartedAt:    snapshot.StartedAt,
		FinishedAt:   snapshot.FinishedAt,
		ReasonCode:   operationReasonFromJobState(snapshot.State),
		RelatedJobID: snapshot.ID,
	}
}

func operationFromPolicyScan(summary PolicyScanSummary) OperationEntry {
	status := OperationStatusSucceeded
	reason := "completed"
	if summary.Failed > 0 || summary.Error != "" {
		status = OperationStatusFailed
		reason = "operation_failed"
		if summary.Changed > 0 {
			status = OperationStatusPartial
			reason = "partial_failure"
		}
	}
	return OperationEntry{
		Category:    OperationCategoryDefaultPolicy,
		Action:      OperationActionPolicyScan,
		Status:      status,
		Source:      OperationSourceDefaultPolicy,
		Scope:       OperationScopeScheduled,
		TargetCount: summary.Scanned,
		Succeeded:   summary.Changed,
		Failed:      summary.Failed,
		Skipped:     summary.Skipped,
		StartedAt:   summary.StartedAt,
		FinishedAt:  summary.FinishedAt,
		ReasonCode:  reason,
	}
}

func operationFromInspectionScan(summary InspectionRunSummary) OperationEntry {
	status := OperationStatusSucceeded
	reason := "completed"
	if summary.Failed > 0 || summary.Error != "" {
		status = OperationStatusFailed
		reason = "operation_failed"
		if summary.Scanned > summary.Failed {
			status = OperationStatusPartial
			reason = "partial_failure"
		}
	}
	succeeded := summary.Scanned - summary.Failed
	if succeeded < 0 {
		succeeded = 0
	}
	return OperationEntry{
		Category:    OperationCategoryInspection,
		Action:      OperationActionInspectionScan,
		Status:      status,
		Source:      OperationSourceInspection,
		Scope:       OperationScopeScheduled,
		TargetCount: summary.Scanned,
		Succeeded:   succeeded,
		Failed:      summary.Failed,
		Skipped:     summary.Truncated,
		StartedAt:   summary.StartedAt,
		FinishedAt:  summary.FinishedAt,
		ReasonCode:  reason,
	}
}

func operationFromInspectionAction(action InspectionAction) (OperationEntry, bool) {
	journalAction := ""
	source := normalizeOperationSource(action.Source)
	if source == "" {
		// Actions persisted before source tracking were produced by inspection automation.
		source = OperationSourceInspection
	}
	switch action.Action {
	case InspectionActionDisable:
		journalAction = OperationActionAutoDisable
	case InspectionActionEnable:
		journalAction = OperationActionAutoEnable
	case InspectionActionDeleteCandidate:
		journalAction = OperationActionDeleteCandidate
	case InspectionActionDelete:
		if source == OperationSourceManual {
			// The manual endpoint records one aggregate operation with the real batch counts.
			return OperationEntry{}, false
		}
		journalAction = OperationActionAutoDelete
	case InspectionActionReviewResolve:
		journalAction = OperationActionReviewResolve
	case InspectionActionReviewIgnore:
		journalAction = OperationActionReviewIgnore
	case InspectionActionReviewReopen:
		journalAction = OperationActionReviewReopen
	}
	status := ""
	switch action.Status {
	case InspectionActionPending:
		status = OperationStatusRunning
	case InspectionActionSucceeded:
		status = OperationStatusSucceeded
	case InspectionActionFailed:
		status = OperationStatusFailed
	case InspectionActionSkipped:
		status = OperationStatusSkipped
	}
	if journalAction == "" || status == "" {
		return OperationEntry{}, false
	}
	succeeded, failed, skipped := 0, 0, 0
	switch status {
	case OperationStatusSucceeded:
		succeeded = 1
	case OperationStatusFailed:
		failed = 1
	case OperationStatusSkipped:
		skipped = 1
	}
	return OperationEntry{
		Category:        OperationCategoryInspection,
		Action:          journalAction,
		Status:          status,
		Source:          source,
		Scope:           OperationScopeSingle,
		TargetID:        action.AccountID,
		TargetCount:     1,
		Succeeded:       succeeded,
		Failed:          failed,
		Skipped:         skipped,
		StartedAt:       action.CreatedAt,
		FinishedAt:      action.CreatedAt,
		ReasonCode:      action.ReasonCode,
		RelatedActionID: action.ID,
	}, true
}

func operationFromUpdateCheck(snapshot UpdateSnapshot) OperationEntry {
	status := OperationStatusSucceeded
	reason := "check_completed"
	if snapshot.Error != "" {
		status = OperationStatusFailed
		reason = "check_failed"
	}
	return OperationEntry{
		Category:   OperationCategoryUpdate,
		Action:     OperationActionUpdateCheck,
		Status:     status,
		Source:     OperationSourcePluginStore,
		Scope:      OperationScopeSystem,
		StartedAt:  snapshot.CheckedAt,
		FinishedAt: snapshot.CheckedAt,
		ReasonCode: reason,
	}
}

func operationStatusFromJobState(state string) string {
	switch state {
	case JobStateRunning:
		return OperationStatusRunning
	case JobStateCompleted:
		return OperationStatusSucceeded
	case JobStatePartial:
		return OperationStatusPartial
	case JobStateInterrupted:
		return OperationStatusInterrupted
	default:
		return OperationStatusFailed
	}
}

func operationReasonFromJobState(state string) string {
	switch state {
	case JobStateCompleted:
		return "completed"
	case JobStatePartial:
		return "partial_failure"
	case JobStateInterrupted:
		return "interrupted"
	case JobStateRunning:
		return ""
	default:
		return "operation_failed"
	}
}

func operationTimestampEvent(prefix string, value time.Time) string {
	return fmt.Sprintf("%s:%s", prefix, value.UTC().Format(time.RFC3339Nano))
}

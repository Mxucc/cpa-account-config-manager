package manager

import (
	"strings"
	"time"
)

func inspectionAnomalyCounts(accounts map[string]Account, records map[string]inspectionRecord) (eligible, abnormal int) {
	metrics := inspectionAnomalyNotificationMetrics(accounts, records)
	return metrics.EligibleAccounts, metrics.AbnormalAccounts
}

func inspectionAnomalyNotificationMetrics(accounts map[string]Account, records map[string]inspectionRecord) anomalyNotificationMetrics {
	metrics := anomalyNotificationMetrics{TotalAccounts: len(accounts)}
	for id, account := range accounts {
		if account.Disabled {
			metrics.DisabledAccounts++
		}
		record, exists := records[id]
		if !exists || (account.Disabled && !record.Result.OwnedDisable) {
			continue
		}
		switch record.Result.Health {
		case InspectionHealthHealthy:
			metrics.EligibleAccounts++
			if !account.Disabled && !account.Unavailable {
				metrics.AvailableAccounts++
			}
		case InspectionHealthQuotaLimited:
			metrics.EligibleAccounts++
			metrics.AbnormalAccounts++
			metrics.QuotaLimitedAccounts++
		case InspectionHealthInvalidCredentials:
			metrics.EligibleAccounts++
			metrics.AbnormalAccounts++
			metrics.InvalidCredentialAccounts++
		case InspectionHealthDeactivated:
			metrics.EligibleAccounts++
			metrics.AbnormalAccounts++
			metrics.DeactivatedAccounts++
		case InspectionHealthUnavailable:
			metrics.EligibleAccounts++
			metrics.AbnormalAccounts++
			metrics.UnavailableAccounts++
		}
	}
	if metrics.EligibleAccounts > 0 {
		metrics.AbnormalPercent = metrics.AbnormalAccounts * 100 / metrics.EligibleAccounts
	}
	if metrics.TotalAccounts > 0 {
		metrics.AvailablePercent = metrics.AvailableAccounts * 100 / metrics.TotalAccounts
	}
	return metrics
}

func inspectionAnomalyTriggered(eligible, abnormal, minimum, thresholdPercent int) bool {
	if eligible <= 0 || abnormal < 0 || abnormal > eligible || eligible < minimum {
		return false
	}
	return abnormal*100 >= eligible*thresholdPercent
}

func inspectionProbeSweepSize(accounts map[string]Account, records map[string]inspectionRecord, scanManuallyDisabled bool) int {
	count := 0
	for id, account := range accounts {
		if strings.TrimSpace(id) == "" {
			continue
		}
		if account.Disabled && !records[id].Result.OwnedDisable && !scanManuallyDisabled {
			continue
		}
		count++
	}
	return count
}

func (e *InspectionEngine) evaluateAnomalyTrigger(
	policy InspectionPolicy,
	accounts map[string]Account,
	records map[string]inspectionRecord,
	now time.Time,
	evaluate bool,
	armed bool,
) (bool, int) {
	if e == nil {
		return false, 0
	}
	eligible, abnormal := inspectionAnomalyCounts(accounts, records)
	percent := 0
	if eligible > 0 {
		percent = abnormal * 100 / eligible
	}
	above := evaluate && policy.AnomalyTriggerEnabled && inspectionAnomalyTriggered(
		eligible, abnormal, policy.AnomalyMinimumAccounts, policy.AnomalyThresholdPercent,
	)

	e.mu.Lock()
	e.anomalyEligible = eligible
	e.anomalyCount = abnormal
	e.anomalyPercent = percent
	if !above {
		if e.anomalyTriggerPending {
			e.probeSweepRemaining = 0
		}
		e.anomalyTriggerPending = false
		e.dirty = true
		e.generation++
		e.mu.Unlock()
		return false, 0
	}
	cooldown := time.Duration(policy.AnomalyCooldownMinutes) * time.Minute
	if !e.lastAnomalyTriggerAt.IsZero() && now.Before(e.lastAnomalyTriggerAt.Add(cooldown)) {
		e.dirty = true
		e.generation++
		e.mu.Unlock()
		return false, 0
	}
	e.lastAnomalyTriggerAt = now.UTC()
	e.anomalyTriggerPending = !armed && !policy.AnomalyNotificationOnly
	e.dirty = true
	e.generation++
	e.mu.Unlock()
	if policy.AnomalyNotificationOnly {
		return true, 0
	}

	return true, inspectionProbeSweepSize(accounts, records, policy.ScanManuallyDisabled)
}

type inspectionSweepProgress struct {
	Total     int
	Completed int
	Remaining int
	Source    string
	StartedAt time.Time
	Targets   []string
}

func (e *InspectionEngine) updateProbeSweep(progress inspectionSweepProgress, stalled bool) {
	if e == nil {
		return
	}
	progress.Total, progress.Completed, progress.Remaining = normalizeInspectionSweepCounts(progress.Total, progress.Completed, progress.Remaining)
	status := InspectionSweepStatusRunning
	if stalled {
		status = InspectionSweepStatusFailed
	} else if progress.Remaining == 0 {
		status = InspectionSweepStatusCompleted
	}
	e.mu.Lock()
	e.probeSweepTotal = progress.Total
	e.probeSweepCompleted = progress.Completed
	e.probeSweepRemaining = progress.Remaining
	e.probeSweepSource = normalizeInspectionSweepSource(progress.Source)
	e.probeSweepStatus = status
	e.probeSweepStartedAt = progress.StartedAt.UTC()
	e.probeSweepTargets = sanitizeInspectionSweepTargets(progress.Targets)
	e.updateRunHistoryLocked(status, func() string {
		if status == InspectionSweepStatusCompleted {
			return InspectionProbePhaseDone
		}
		return e.probePhase
	}(), e.currentTime())
	if progress.Remaining == 0 || stalled {
		e.pendingProbeSweep = false
	}
	if progress.Remaining == 0 {
		e.probeSweepTargets = nil
	}
	e.dirty = true
	e.generation++
	e.mu.Unlock()
	e.requestPersist()
}

func (e *InspectionEngine) requestProbeSweep() {
	if e == nil {
		return
	}
	e.mu.Lock()
	if e.closed || e.probeSweepRemaining <= 0 {
		e.mu.Unlock()
		return
	}
	if strings.TrimSpace(e.managementKey) == "" || e.modelTests == nil {
		if e.policy.AnomalyTriggerEnabled {
			e.anomalyTriggerPending = true
		}
		e.dirty = true
		e.probeSweepStatus = InspectionSweepStatusWaitingForAuth
		e.generation++
		e.mu.Unlock()
		e.requestPersist()
		return
	}
	e.pending = true
	e.pendingProbe = true
	e.pendingProbeSweep = true
	e.probeSweepStatus = InspectionSweepStatusRunning
	started := e.started
	e.mu.Unlock()
	if started {
		select {
		case e.scanWake <- struct{}{}:
		default:
		}
	}
}

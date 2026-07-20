package manager

import (
	"strings"
	"time"
)

func inspectionAnomalyCounts(accounts map[string]Account, records map[string]inspectionRecord) (eligible, abnormal int) {
	for id, account := range accounts {
		record, exists := records[id]
		if !exists || (account.Disabled && !record.Result.OwnedDisable) {
			continue
		}
		switch record.Result.Health {
		case InspectionHealthHealthy:
			eligible++
		case InspectionHealthQuotaLimited, InspectionHealthInvalidCredentials, InspectionHealthDeactivated, InspectionHealthUnavailable:
			eligible++
			abnormal++
		}
	}
	return eligible, abnormal
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
	if e == nil || !evaluate {
		return false, 0
	}
	eligible, abnormal := inspectionAnomalyCounts(accounts, records)
	percent := 0
	if eligible > 0 {
		percent = abnormal * 100 / eligible
	}
	above := policy.AnomalyTriggerEnabled && inspectionAnomalyTriggered(
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
	e.anomalyTriggerPending = !armed
	e.dirty = true
	e.generation++
	e.mu.Unlock()

	return true, inspectionProbeSweepSize(accounts, records, policy.ScanManuallyDisabled)
}

func (e *InspectionEngine) updateProbeSweep(remaining int, stalled bool) {
	if e == nil {
		return
	}
	if remaining < 0 || stalled {
		remaining = 0
	}
	e.mu.Lock()
	e.probeSweepRemaining = remaining
	if remaining == 0 {
		e.pendingProbeSweep = false
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
		e.generation++
		e.mu.Unlock()
		e.requestPersist()
		return
	}
	e.pending = true
	e.pendingProbe = true
	e.pendingProbeSweep = true
	started := e.started
	e.mu.Unlock()
	if started {
		select {
		case e.scanWake <- struct{}{}:
		default:
		}
	}
}

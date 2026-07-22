package manager

import (
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// AutomaticDisableGuard allows removable features to conservatively veto one
// automatic disable without coupling inspection to that feature.
type AutomaticDisableGuard interface {
	AllowUsageAutoDisable(cpaapi.UsageRecord, time.Time) bool
	AllowInspectionAutoDisable(InspectionResult) bool
}

func (e *InspectionEngine) RegisterAutomaticDisableGuard(guard AutomaticDisableGuard) {
	if e == nil || guard == nil {
		return
	}
	e.mu.Lock()
	e.autoDisableGuards = append(e.autoDisableGuards, guard)
	e.mu.Unlock()
}

func (e *InspectionEngine) usageAutoDisableAllowedLocked(usage cpaapi.UsageRecord, now time.Time) bool {
	for _, guard := range e.autoDisableGuards {
		if guard != nil && !guard.AllowUsageAutoDisable(usage, now) {
			return false
		}
	}
	return true
}

func (e *InspectionEngine) inspectionAutoDisableAllowed(result InspectionResult) bool {
	if e == nil {
		return true
	}
	e.mu.RLock()
	guards := append([]AutomaticDisableGuard(nil), e.autoDisableGuards...)
	e.mu.RUnlock()
	for _, guard := range guards {
		if guard != nil && !guard.AllowInspectionAutoDisable(result) {
			return false
		}
	}
	return true
}

package manager

import (
	"context"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// AutomaticDisableGuard allows removable features to conservatively veto one
// automatic disable without coupling inspection to that feature.
type AutomaticDisableGuard interface {
	AllowUsageAutoDisable(cpaapi.UsageRecord, time.Time) bool
	AllowInspectionAutoDisable(InspectionResult) bool
}

// AutomaticDisableProbePlanner lets an optional feature require bounded real
// probes before its conservative disable veto can be released.
type AutomaticDisableProbePlanner interface {
	AutomaticDisableProbePlan(Account, InspectionResult, string) (AutomaticDisableProbePlan, bool)
}

type AutomaticDisableProbePlan struct {
	Name         string
	AttemptLimit int
	Models       []string
	Request      ModelTestRequest
}

type automaticDisableProbeRunner func(context.Context, ModelTestRequest, string, string) (ModelTestResult, error)

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

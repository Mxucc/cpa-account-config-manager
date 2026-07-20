package manager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const inspectionStoreVersion = 1

type inspectionSignal struct {
	ReasonCode          string    `json:"reason_code,omitempty"`
	Confidence          string    `json:"confidence,omitempty"`
	StatusCode          int       `json:"status_code,omitempty"`
	AutoDisableEligible bool      `json:"auto_disable_eligible,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures,omitempty"`
	ConsecutiveSuccess  int       `json:"consecutive_success,omitempty"`
	LastFailureAt       time.Time `json:"last_failure_at,omitempty"`
	LastSuccessAt       time.Time `json:"last_success_at,omitempty"`
	RecoverAfter        time.Time `json:"recover_after,omitempty"`
}

type inspectionRecord struct {
	Result               InspectionResult `json:"result"`
	Signal               inspectionSignal `json:"signal"`
	DisableReason        string           `json:"disable_reason,omitempty"`
	DisabledAt           time.Time        `json:"disabled_at,omitempty"`
	DisabledName         string           `json:"disabled_name,omitempty"`
	DisabledPath         string           `json:"disabled_path,omitempty"`
	DisabledVersion      string           `json:"disabled_revision,omitempty"`
	DisabledRecoverAfter time.Time        `json:"disabled_recover_after,omitempty"`
	DeleteRetryAfter     time.Time        `json:"delete_retry_after,omitempty"`
}

type persistedInspectionState struct {
	Version int                         `json:"version"`
	Policy  InspectionPolicy            `json:"policy"`
	Records map[string]inspectionRecord `json:"records"`
	Actions []InspectionAction          `json:"actions,omitempty"`
	LastRun InspectionRunSummary        `json:"last_run"`
}

func inspectionStorePath(dataDir string) string {
	return filepath.Join(dataDir, "inspection-state.json")
}

func loadInspectionState(path string) (persistedInspectionState, error) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return persistedInspectionState{}, errRead
	}
	var state persistedInspectionState
	if errDecode := json.Unmarshal(raw, &state); errDecode != nil {
		return persistedInspectionState{}, fmt.Errorf("decode inspection state: %w", errDecode)
	}
	if state.Version != inspectionStoreVersion {
		return persistedInspectionState{}, fmt.Errorf("unsupported inspection store version %d", state.Version)
	}
	policy, errPolicy := validateInspectionPolicy(state.Policy)
	if errPolicy != nil {
		return persistedInspectionState{}, fmt.Errorf("validate inspection policy: %w", errPolicy)
	}
	state.Policy = policy
	state.Records = sanitizeInspectionRecords(state.Records)
	state.Actions = sanitizeInspectionActions(state.Actions)
	state.LastRun.Error = safeInspectionError(state.LastRun.Error)
	return state, nil
}

func saveInspectionState(path string, state persistedInspectionState) error {
	state.Version = inspectionStoreVersion
	state.Policy = normalizeInspectionPolicy(state.Policy)
	state.Records = cloneInspectionRecords(state.Records)
	state.Actions = append([]InspectionAction(nil), state.Actions...)
	return savePrivateJSON(path, state)
}

func sanitizeInspectionRecords(records map[string]inspectionRecord) map[string]inspectionRecord {
	if len(records) == 0 {
		return make(map[string]inspectionRecord)
	}
	keys := make([]string, 0, len(records))
	for key := range records {
		key = strings.TrimSpace(key)
		if key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) > maxInspectionAccounts {
		keys = keys[:maxInspectionAccounts]
	}
	out := make(map[string]inspectionRecord, len(keys))
	for _, key := range keys {
		record := records[key]
		record.Result.ID = key
		record.Result.Health = normalizeInspectionHealth(record.Result.Health)
		record.Result.ReasonCode = safeInspectionReason(record.Result.ReasonCode)
		record.Result.Confidence = normalizeInspectionConfidence(record.Result.Confidence)
		record.Result.Recommendation = normalizeInspectionRecommendation(record.Result.Recommendation)
		record.Result.AutoAction = normalizeInspectionAction(record.Result.AutoAction)
		record.Result.AutoActionStatus = normalizeInspectionActionStatus(record.Result.AutoActionStatus)
		record.Result.FailureStreak = boundedCounter(record.Result.FailureStreak)
		record.Result.HealthyStreak = boundedCounter(record.Result.HealthyStreak)
		record.Signal.ReasonCode = safeInspectionReason(record.Signal.ReasonCode)
		record.Signal.Confidence = normalizeInspectionConfidence(record.Signal.Confidence)
		record.Signal.StatusCode = boundedHTTPStatus(record.Signal.StatusCode)
		record.Signal.ConsecutiveFailures = boundedCounter(record.Signal.ConsecutiveFailures)
		record.Signal.ConsecutiveSuccess = boundedCounter(record.Signal.ConsecutiveSuccess)
		record.DisableReason = safeInspectionReason(record.DisableReason)
		out[key] = record
	}
	return out
}

func sanitizeInspectionActions(actions []InspectionAction) []InspectionAction {
	if len(actions) > maxInspectionActions {
		actions = actions[len(actions)-maxInspectionActions:]
	}
	out := make([]InspectionAction, 0, len(actions))
	for _, action := range actions {
		action.ID = strings.TrimSpace(action.ID)
		action.AccountID = strings.TrimSpace(action.AccountID)
		action.Action = normalizeInspectionAction(action.Action)
		action.Status = normalizeInspectionActionStatus(action.Status)
		action.ReasonCode = safeInspectionReason(action.ReasonCode)
		if action.ID == "" || action.AccountID == "" || action.Action == "" || action.Status == "" {
			continue
		}
		out = append(out, action)
	}
	return out
}

func cloneInspectionRecords(records map[string]inspectionRecord) map[string]inspectionRecord {
	cloned := make(map[string]inspectionRecord, len(records))
	for key, record := range records {
		record.Result = cloneInspectionResult(record.Result)
		cloned[key] = record
	}
	return cloned
}

func cloneInspectionResult(result InspectionResult) InspectionResult {
	clone := result
	clone.FirstUnhealthyAt = cloneTimePointer(result.FirstUnhealthyAt)
	clone.LastFailureAt = cloneTimePointer(result.LastFailureAt)
	clone.LastSuccessAt = cloneTimePointer(result.LastSuccessAt)
	clone.RecoverAfter = cloneTimePointer(result.RecoverAfter)
	clone.DeleteEligibleAt = cloneTimePointer(result.DeleteEligibleAt)
	return clone
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	clone := value.UTC()
	return &clone
}

func normalizeInspectionHealth(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case InspectionHealthHealthy, InspectionHealthQuotaLimited, InspectionHealthInvalidCredentials,
		InspectionHealthDeactivated, InspectionHealthReview, InspectionHealthUnavailable,
		InspectionHealthDisabled, InspectionHealthUnknown:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return InspectionHealthUnknown
	}
}

func normalizeInspectionConfidence(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case InspectionConfidenceHigh, InspectionConfidenceMedium, InspectionConfidenceLow:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return InspectionConfidenceLow
	}
}

func normalizeInspectionRecommendation(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case InspectionRecommendationKeep, InspectionRecommendationReauth, InspectionRecommendationReview,
		InspectionRecommendationDisable, InspectionRecommendationEnable, InspectionRecommendationDelete:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return InspectionRecommendationReview
	}
}

func normalizeInspectionAction(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case InspectionActionDisable, InspectionActionEnable, InspectionActionDelete, InspectionActionDeleteCandidate:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeInspectionActionStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case InspectionActionPending, InspectionActionSucceeded, InspectionActionFailed, InspectionActionSkipped:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func safeInspectionReason(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "healthy_recent_success", "quota_exhausted", "token_revoked", "invalid_credentials",
		"account_deactivated", "workspace_deactivated", "authentication_review",
		"billing_review", "credential_permission_denied", "native_unavailable", "manual_disabled",
		"transient_failure", "no_recent_evidence", "mutation_busy", "account_changed",
		"account_missing", "account_read_only", "management_unavailable", "delete_failed":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "no_recent_evidence"
	}
}

func safeInspectionError(value string) string {
	switch strings.TrimSpace(value) {
	case "", "account inspection failed", "inspection state could not be persisted", "another account mutation is running":
		return strings.TrimSpace(value)
	default:
		return "account inspection failed"
	}
}

func boundedCounter(value int) int {
	if value < 0 {
		return 0
	}
	if value > 1_000_000 {
		return 1_000_000
	}
	return value
}

func boundedHTTPStatus(value int) int {
	if value < 100 || value > 599 {
		return 0
	}
	return value
}

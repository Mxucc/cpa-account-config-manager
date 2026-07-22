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
	QuotaWindow         string    `json:"quota_window,omitempty"`
}

type inspectionProbeSignal struct {
	Status              string    `json:"status,omitempty"`
	Kind                string    `json:"kind,omitempty"`
	ReasonCode          string    `json:"reason_code,omitempty"`
	StatusCode          int       `json:"status_code,omitempty"`
	Model               string    `json:"model,omitempty"`
	TestedAt            time.Time `json:"tested_at,omitempty"`
	LatencyMS           int64     `json:"latency_ms,omitempty"`
	QuotaWindow         string    `json:"quota_window,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures,omitempty"`
	ConsecutiveSuccess  int       `json:"consecutive_success,omitempty"`
}

type inspectionRecord struct {
	Result               InspectionResult      `json:"result"`
	Signal               inspectionSignal      `json:"signal"`
	Probe                inspectionProbeSignal `json:"probe,omitempty"`
	DisableReason        string                `json:"disable_reason,omitempty"`
	DisabledAt           time.Time             `json:"disabled_at,omitempty"`
	DisabledName         string                `json:"disabled_name,omitempty"`
	DisabledPath         string                `json:"disabled_path,omitempty"`
	DisabledVersion      string                `json:"disabled_revision,omitempty"`
	DisabledRecoverAfter time.Time             `json:"disabled_recover_after,omitempty"`
	DeleteRetryAfter     time.Time             `json:"delete_retry_after,omitempty"`
}

type persistedInspectionState struct {
	Version               int                         `json:"version"`
	Policy                InspectionPolicy            `json:"policy"`
	Records               map[string]inspectionRecord `json:"records"`
	Actions               []InspectionAction          `json:"actions,omitempty"`
	LastRun               InspectionRunSummary        `json:"last_run"`
	ProbeCursor           int                         `json:"probe_cursor,omitempty"`
	LastNativeRunAt       time.Time                   `json:"last_native_run_at,omitempty"`
	LastProbeRunAt        time.Time                   `json:"last_probe_run_at,omitempty"`
	ProbeSweepRemaining   int                         `json:"probe_sweep_remaining,omitempty"`
	ProbeSweepTotal       int                         `json:"probe_sweep_total,omitempty"`
	ProbeSweepCompleted   int                         `json:"probe_sweep_completed,omitempty"`
	ProbeSweepSource      string                      `json:"probe_sweep_source,omitempty"`
	ProbeSweepStatus      string                      `json:"probe_sweep_status,omitempty"`
	ProbeSweepStartedAt   time.Time                   `json:"probe_sweep_started_at,omitempty"`
	ProbeSweepTargets     []string                    `json:"probe_sweep_targets,omitempty"`
	AnomalyTriggerPending bool                        `json:"anomaly_trigger_pending,omitempty"`
	LastAnomalyTriggerAt  time.Time                   `json:"last_anomaly_trigger_at,omitempty"`
	LastNotificationAt    time.Time                   `json:"last_notification_at,omitempty"`
	RunMode               string                      `json:"run_mode,omitempty"`
	RunHealth             []string                    `json:"run_health,omitempty"`
	RunSelected           []string                    `json:"run_selected,omitempty"`
	ProbePhase            string                      `json:"probe_phase,omitempty"`
	RetryTotal            int                         `json:"retry_total,omitempty"`
	RetryCompleted        int                         `json:"retry_completed,omitempty"`
	StopRequested         bool                        `json:"stop_requested,omitempty"`
	Runs                  []InspectionRunRecord       `json:"runs,omitempty"`
	ActiveRunID           string                      `json:"active_run_id,omitempty"`
}

func stopPendingAnomalySweep(state *persistedInspectionState, includeRunning bool) {
	if state == nil {
		return
	}
	state.AnomalyTriggerPending = false
	if normalizeInspectionSweepSource(state.ProbeSweepSource) != InspectionSweepSourceAnomaly ||
		(!includeRunning && normalizeInspectionSweepStatus(state.ProbeSweepStatus) == InspectionSweepStatusRunning) {
		return
	}
	state.ProbeSweepTotal = 0
	state.ProbeSweepCompleted = 0
	state.ProbeSweepRemaining = 0
	state.ProbeSweepStatus = InspectionSweepStatusStopped
	state.ProbeSweepTargets = nil
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
	if state.LastNotificationAt.IsZero() && state.Policy.AnomalyNotificationEnabled {
		state.LastNotificationAt = state.LastAnomalyTriggerAt
	}
	state.Records = sanitizeInspectionRecords(state.Records)
	state.Actions = sanitizeInspectionActions(state.Actions)
	state.LastRun.Error = safeInspectionError(state.LastRun.Error)
	if state.ProbeCursor < 0 || state.ProbeCursor >= maxInspectionAccounts {
		state.ProbeCursor = 0
	}
	if state.ProbeSweepRemaining < 0 || state.ProbeSweepRemaining > maxInspectionAccounts {
		state.ProbeSweepRemaining = 0
		state.AnomalyTriggerPending = false
	}
	state.ProbeSweepTotal, state.ProbeSweepCompleted, state.ProbeSweepRemaining = normalizeInspectionSweepCounts(
		state.ProbeSweepTotal, state.ProbeSweepCompleted, state.ProbeSweepRemaining,
	)
	state.ProbeSweepSource = normalizeInspectionSweepSource(state.ProbeSweepSource)
	state.ProbeSweepStatus = normalizeInspectionSweepStatus(state.ProbeSweepStatus)
	state.ProbeSweepTargets = sanitizeInspectionSweepTargets(state.ProbeSweepTargets)
	state.RunMode = normalizeInspectionRunMode(state.RunMode)
	state.RunHealth = normalizeInspectionRunHealth(state.RunHealth)
	state.RunSelected = sanitizeInspectionSweepTargets(state.RunSelected)
	state.ProbePhase = normalizeInspectionProbePhase(state.ProbePhase)
	state.Runs = sanitizeInspectionRuns(state.Runs)
	state.ActiveRunID = safeOperationIdentifier(state.ActiveRunID, 128)
	state.RetryTotal, state.RetryCompleted, _ = normalizeInspectionSweepCounts(state.RetryTotal, state.RetryCompleted, max(0, state.RetryTotal-state.RetryCompleted))
	if state.ProbeSweepTotal == 0 && state.ProbeSweepRemaining == 0 {
		state.ProbeSweepCompleted = 0
	}
	return state, nil
}

func saveInspectionState(path string, state persistedInspectionState) error {
	state.Version = inspectionStoreVersion
	state.Policy = normalizeInspectionPolicy(state.Policy)
	state.Records = sanitizeInspectionRecords(state.Records)
	state.Actions = append([]InspectionAction(nil), state.Actions...)
	state.ProbeSweepTargets = append([]string(nil), state.ProbeSweepTargets...)
	state.RunHealth = append([]string(nil), state.RunHealth...)
	state.RunSelected = append([]string(nil), state.RunSelected...)
	state.Runs = append([]InspectionRunRecord(nil), state.Runs...)
	return savePrivateJSON(path, state)
}

func sanitizeInspectionRuns(runs []InspectionRunRecord) []InspectionRunRecord {
	if len(runs) > maxInspectionRuns {
		runs = runs[len(runs)-maxInspectionRuns:]
	}
	out := make([]InspectionRunRecord, 0, len(runs))
	for _, run := range runs {
		run.ID = safeOperationIdentifier(run.ID, 128)
		run.Mode = normalizeInspectionRunMode(run.Mode)
		run.Source = normalizeInspectionSweepSource(run.Source)
		run.Status = normalizeInspectionSweepStatus(run.Status)
		run.Phase = normalizeInspectionProbePhase(run.Phase)
		run.PrimaryTotal, run.PrimaryDone, _ = normalizeInspectionSweepCounts(run.PrimaryTotal, run.PrimaryDone, max(0, run.PrimaryTotal-run.PrimaryDone))
		run.RetryTotal, run.RetryDone, _ = normalizeInspectionSweepCounts(run.RetryTotal, run.RetryDone, max(0, run.RetryTotal-run.RetryDone))
		run.Summary.Error = safeInspectionError(run.Summary.Error)
		if run.ID == "" || run.Mode == "" || run.Source == "" || run.Status == "" || run.StartedAt.IsZero() {
			continue
		}
		out = append(out, run)
	}
	return out
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
		record.Probe.Status = normalizeModelProbeStatus(record.Probe.Status)
		record.Probe.Kind = normalizeInspectionProbeKind(record.Probe.Kind)
		record.Probe.ReasonCode = safeModelProbeReason(record.Probe.ReasonCode)
		record.Probe.StatusCode = boundedHTTPStatus(record.Probe.StatusCode)
		record.Probe.Model = safeModelIdentifier(record.Probe.Model)
		record.Probe.QuotaWindow = normalizeInspectionQuotaWindow(record.Probe.QuotaWindow)
		record.Probe.ConsecutiveFailures = boundedCounter(record.Probe.ConsecutiveFailures)
		record.Probe.ConsecutiveSuccess = boundedCounter(record.Probe.ConsecutiveSuccess)
		if record.Probe.LatencyMS < 0 {
			record.Probe.LatencyMS = 0
		}
		record.Result.ProbeStatus = record.Probe.Status
		record.Result.ProbeKind = record.Probe.Kind
		record.Result.ProbeReasonCode = record.Probe.ReasonCode
		record.Result.ProbeModel = record.Probe.Model
		record.Result.ProbeTestedAt = cloneTimePointer(timePointerOrNil(record.Probe.TestedAt))
		record.Result.ProbeLatencyMS = record.Probe.LatencyMS
		record.DisableReason = safeInspectionReason(record.DisableReason)
		record.Result.SignalSource = normalizeInspectionSignalSource(record.Result.SignalSource)
		migrateLegacyModelAuthenticationResult(&record)
		record.Result.StatusCode = boundedHTTPStatus(record.Result.StatusCode)
		record.Result.ReviewStatus = normalizeInspectionReviewStatus(record.Result.ReviewStatus, record.Result.Health)
		if record.Result.ReviewStatus == "" {
			record.Result.ReviewedAt = nil
		}
		record.Result.CircuitReasonCode = safeOptionalInspectionReason(record.Result.CircuitReasonCode)
		record.Result.QuotaWindow = normalizeInspectionQuotaWindow(record.Result.QuotaWindow)
		record.Result.CodexUsage = cloneCodexUsage(record.Result.CodexUsage)
		record.Result.RunID = safeOperationIdentifier(record.Result.RunID, 128)
		record.Result.RunPhase = normalizeInspectionProbePhase(record.Result.RunPhase)
		record.Signal.QuotaWindow = normalizeInspectionQuotaWindow(record.Signal.QuotaWindow)
		if record.DisableReason != "passive_circuit_open" {
			record.Result.CircuitOpen = false
			record.Result.CircuitReasonCode = ""
		}
		out[key] = record
	}
	return out
}

func migrateLegacyModelAuthenticationResult(record *inspectionRecord) {
	if record == nil || record.Result.SignalSource != InspectionSignalActiveProbe ||
		record.Probe.ReasonCode != "authentication_failed" || record.Probe.Kind == InspectionProbeKindCredential ||
		record.Result.Recommendation != InspectionRecommendationReauth {
		return
	}
	record.Result.Health = InspectionHealthUnavailable
	record.Result.Confidence = InspectionConfidenceMedium
	record.Result.Recommendation = InspectionRecommendationDisable
	record.Result.AutoDisableEligible = true
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
		action.Source = normalizeOperationSource(action.Source)
		if action.Source == "" {
			action.Source = OperationSourceInspection
		}
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
	clone.ProbeTestedAt = cloneTimePointer(result.ProbeTestedAt)
	clone.ReviewedAt = cloneTimePointer(result.ReviewedAt)
	clone.UsageLastRequestAt = cloneTimePointer(result.UsageLastRequestAt)
	clone.CodexUsage = cloneCodexUsage(result.CodexUsage)
	clone.RunObservedAt = cloneTimePointer(result.RunObservedAt)
	return clone
}

func normalizeInspectionQuotaWindow(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case InspectionQuotaWindowFiveHour, InspectionQuotaWindowSevenDay, InspectionQuotaWindowMultiple, InspectionQuotaWindowFiveHourFallback:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func timePointerOrNil(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func normalizeModelProbeStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "available", "unavailable", "review", "unsupported":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeInspectionProbeKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case InspectionProbeKindModel, InspectionProbeKindCredential:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func safeModelProbeReason(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return safeInspectionReason(value)
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
	case InspectionActionDisable, InspectionActionEnable, InspectionActionDelete, InspectionActionDeleteCandidate,
		InspectionActionReviewResolve, InspectionActionReviewIgnore, InspectionActionReviewReopen:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeInspectionReviewStatus(value, health string) string {
	if normalizeInspectionHealth(health) != InspectionHealthReview {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case InspectionReviewResolved, InspectionReviewIgnored:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return InspectionReviewPending
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
		"account_missing", "account_read_only", "management_unavailable", "delete_failed",
		"model_response_ok", "credential_response_ok", "authentication_failed", "quota_limited", "model_not_found", "unsupported_provider",
		"request_timeout", "upstream_unavailable", "invalid_response", "unconfirmed_upstream_response",
		"passive_circuit_open":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "no_recent_evidence"
	}
}

func safeOptionalInspectionReason(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return safeInspectionReason(value)
}

func normalizeInspectionSignalSource(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case InspectionSignalNative, InspectionSignalPassive, InspectionSignalActiveProbe:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
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

func normalizeInspectionSweepCounts(total, completed, remaining int) (int, int, int) {
	if total < 0 || total > maxInspectionAccounts {
		total = 0
	}
	if completed < 0 || completed > maxInspectionAccounts {
		completed = 0
	}
	if remaining < 0 || remaining > maxInspectionAccounts {
		remaining = 0
	}
	if total == 0 && remaining > 0 {
		total = remaining
	}
	if completed > total {
		completed = total
	}
	if total > 0 && completed+remaining > total {
		remaining = total - completed
	}
	return total, completed, remaining
}

func normalizeInspectionSweepSource(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case InspectionSweepSourceManual, InspectionSweepSourceScheduled, InspectionSweepSourceAnomaly:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeInspectionSweepStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case InspectionSweepStatusRunning, InspectionSweepStatusCompleted, InspectionSweepStatusFailed, InspectionSweepStatusWaitingForAuth, InspectionSweepStatusStopped:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func sanitizeInspectionSweepTargets(values []string) []string {
	if len(values) > maxInspectionAccounts {
		values = values[:maxInspectionAccounts]
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = safeOperationIdentifier(value, 256)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
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

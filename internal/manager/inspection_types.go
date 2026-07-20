package manager

import (
	"fmt"
	"strings"
	"time"
)

const (
	maxInspectionAccounts       = 10_000
	maxInspectionActions        = 500
	defaultInspectionInterval   = 30
	defaultModelProbeInterval   = 60
	defaultModelProbeBatchSize  = 20
	defaultAnomalyThreshold     = 50
	defaultAnomalyMinimum       = 10
	defaultAnomalyCooldown      = 60
	minInspectionInterval       = 5
	maxInspectionInterval       = 24 * 60
	maxModelProbeBatchSize      = 200
	defaultFailureThreshold     = 3
	defaultRecoveryThreshold    = 2
	defaultPassiveThreshold     = 5
	defaultPassiveWindow        = 180
	defaultPassiveCircuit       = 15
	defaultDeleteGraceHours     = 7 * 24
	defaultDeleteBatchSize      = 10
	maxDeleteGraceHours         = 365 * 24
	maxDeleteBatchSize          = 100
	maxInspectionResultPageSize = 200

	InspectionHealthHealthy            = "healthy"
	InspectionHealthQuotaLimited       = "quota_limited"
	InspectionHealthInvalidCredentials = "invalid_credentials"
	InspectionHealthDeactivated        = "deactivated"
	InspectionHealthReview             = "review"
	InspectionHealthUnavailable        = "unavailable"
	InspectionHealthDisabled           = "disabled"
	InspectionHealthUnknown            = "unknown"

	InspectionRecommendationKeep    = "keep"
	InspectionRecommendationReauth  = "reauth"
	InspectionRecommendationReview  = "review"
	InspectionRecommendationDisable = "disable"
	InspectionRecommendationEnable  = "enable"
	InspectionRecommendationDelete  = "delete"

	InspectionConfidenceHigh   = "high"
	InspectionConfidenceMedium = "medium"
	InspectionConfidenceLow    = "low"

	InspectionActionDisable         = "disable"
	InspectionActionEnable          = "enable"
	InspectionActionDelete          = "delete"
	InspectionActionDeleteCandidate = "delete_candidate"

	InspectionActionPending   = "pending"
	InspectionActionSucceeded = "succeeded"
	InspectionActionFailed    = "failed"
	InspectionActionSkipped   = "skipped"

	InspectionSignalNative      = "native"
	InspectionSignalPassive     = "passive"
	InspectionSignalActiveProbe = "active_probe"

	InspectionSweepSourceManual    = "manual"
	InspectionSweepSourceScheduled = "scheduled"
	InspectionSweepSourceAnomaly   = "anomaly"

	InspectionSweepStatusRunning        = "running"
	InspectionSweepStatusCompleted      = "completed"
	InspectionSweepStatusFailed         = "failed"
	InspectionSweepStatusWaitingForAuth = "waiting_for_auth"
)

type InspectionPolicy struct {
	Enabled                      bool             `json:"enabled"`
	ScanIntervalMinutes          int              `json:"scan_interval_minutes"`
	ModelProbeEnabled            bool             `json:"model_probe_enabled"`
	ModelProbeFullSweep          bool             `json:"model_probe_full_sweep"`
	ScanManuallyDisabled         bool             `json:"scan_manually_disabled"`
	ModelProbeIntervalMinutes    int              `json:"model_probe_interval_minutes"`
	ModelProbeBatchSize          int              `json:"model_probe_batch_size"`
	ModelProbeModels             ModelProbeModels `json:"model_probe_models"`
	FailureThreshold             int              `json:"failure_threshold"`
	RecoveryThreshold            int              `json:"recovery_threshold"`
	PassiveCircuitEnabled        bool             `json:"passive_circuit_enabled"`
	PassiveFailureThreshold      int              `json:"passive_failure_threshold"`
	PassiveFailureWindowMinutes  int              `json:"passive_failure_window_minutes"`
	PassiveCircuitMinutes        int              `json:"passive_circuit_minutes"`
	AutoDisable                  bool             `json:"auto_disable"`
	AutoEnable                   bool             `json:"auto_enable"`
	AutoDelete                   bool             `json:"auto_delete"`
	AutoDeleteInvalidCredentials bool             `json:"auto_delete_invalid_credentials"`
	DeleteGraceHours             int              `json:"delete_grace_hours"`
	DeleteBatchSize              int              `json:"delete_batch_size"`
	AnomalyTriggerEnabled        bool             `json:"anomaly_trigger_enabled"`
	AnomalyThresholdPercent      int              `json:"anomaly_threshold_percent"`
	AnomalyMinimumAccounts       int              `json:"anomaly_minimum_accounts"`
	AnomalyCooldownMinutes       int              `json:"anomaly_cooldown_minutes"`
}

type ModelProbeModels struct {
	Codex  string `json:"codex"`
	OpenAI string `json:"openai"`
	Claude string `json:"claude"`
	Gemini string `json:"gemini"`
	XAI    string `json:"xai"`
}

type InspectionPolicyUpdateRequest struct {
	InspectionPolicy
	ConfirmAutoDelete               bool `json:"confirm_auto_delete"`
	ConfirmDeleteInvalidCredentials bool `json:"confirm_delete_invalid_credentials"`
}

type InspectionRunSummary struct {
	StartedAt          time.Time `json:"started_at,omitempty"`
	FinishedAt         time.Time `json:"finished_at,omitempty"`
	Scanned            int       `json:"scanned"`
	Healthy            int       `json:"healthy"`
	QuotaLimited       int       `json:"quota_limited"`
	InvalidCredentials int       `json:"invalid_credentials"`
	Deactivated        int       `json:"deactivated"`
	Review             int       `json:"review"`
	Unavailable        int       `json:"unavailable"`
	Disabled           int       `json:"disabled"`
	Unknown            int       `json:"unknown"`
	AutoDisabled       int       `json:"auto_disabled"`
	AutoEnabled        int       `json:"auto_enabled"`
	DeletePending      int       `json:"delete_pending"`
	Failed             int       `json:"failed"`
	Truncated          int       `json:"truncated"`
	Error              string    `json:"error,omitempty"`
}

type InspectionSnapshot struct {
	Policy                InspectionPolicy     `json:"policy"`
	Running               bool                 `json:"running"`
	Pending               bool                 `json:"pending"`
	ScanStartedAt         time.Time            `json:"scan_started_at,omitempty"`
	LastRun               InspectionRunSummary `json:"last_run"`
	Total                 int                  `json:"total"`
	ActionCount           int                  `json:"action_count"`
	ActiveProbeArmed      bool                 `json:"active_probe_armed"`
	LastNativeRunAt       time.Time            `json:"last_native_run_at,omitempty"`
	LastProbeRunAt        time.Time            `json:"last_probe_run_at,omitempty"`
	ProbeSweepRemaining   int                  `json:"probe_sweep_remaining"`
	ProbeSweepTotal       int                  `json:"probe_sweep_total"`
	ProbeSweepCompleted   int                  `json:"probe_sweep_completed"`
	ProbeSweepSource      string               `json:"probe_sweep_source,omitempty"`
	ProbeSweepStatus      string               `json:"probe_sweep_status,omitempty"`
	ProbeSweepStartedAt   time.Time            `json:"probe_sweep_started_at,omitempty"`
	AnomalyEligible       int                  `json:"anomaly_eligible"`
	AnomalyCount          int                  `json:"anomaly_count"`
	AnomalyPercent        int                  `json:"anomaly_percent"`
	AnomalyTriggerPending bool                 `json:"anomaly_trigger_pending"`
	LastAnomalyTriggerAt  time.Time            `json:"last_anomaly_trigger_at,omitempty"`
	StorageError          string               `json:"storage_error,omitempty"`
}

type InspectionResult struct {
	ID                  string     `json:"id"`
	Name                string     `json:"name,omitempty"`
	Provider            string     `json:"provider,omitempty"`
	Type                string     `json:"type,omitempty"`
	PlanType            string     `json:"plan_type,omitempty"`
	Health              string     `json:"health"`
	ReasonCode          string     `json:"reason_code"`
	Confidence          string     `json:"confidence"`
	Recommendation      string     `json:"recommendation"`
	Disabled            bool       `json:"disabled"`
	Editable            bool       `json:"editable"`
	AutoDisableEligible bool       `json:"auto_disable_eligible"`
	OwnedDisable        bool       `json:"owned_disable"`
	FailureStreak       int        `json:"failure_streak"`
	HealthyStreak       int        `json:"healthy_streak"`
	LastCheckedAt       time.Time  `json:"last_checked_at"`
	FirstUnhealthyAt    *time.Time `json:"first_unhealthy_at,omitempty"`
	LastFailureAt       *time.Time `json:"last_failure_at,omitempty"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
	RecoverAfter        *time.Time `json:"recover_after,omitempty"`
	DeleteEligibleAt    *time.Time `json:"delete_eligible_at,omitempty"`
	AutoAction          string     `json:"auto_action,omitempty"`
	AutoActionStatus    string     `json:"auto_action_status,omitempty"`
	ProbeStatus         string     `json:"probe_status,omitempty"`
	ProbeReasonCode     string     `json:"probe_reason_code,omitempty"`
	ProbeModel          string     `json:"probe_model,omitempty"`
	ProbeTestedAt       *time.Time `json:"probe_tested_at,omitempty"`
	ProbeLatencyMS      int64      `json:"probe_latency_ms,omitempty"`
	SignalSource        string     `json:"signal_source,omitempty"`
	CircuitOpen         bool       `json:"circuit_open"`
	CircuitReasonCode   string     `json:"circuit_reason_code,omitempty"`
}

// AccountAutomationSummary is the bounded inspection state exposed with an
// account row. It intentionally excludes raw signals and auth-source details.
type AccountAutomationSummary struct {
	Health                  string     `json:"health"`
	ReasonCode              string     `json:"reason_code"`
	Recommendation          string     `json:"recommendation"`
	LastCheckedAt           time.Time  `json:"last_checked_at"`
	OwnedDisable            bool       `json:"owned_disable"`
	DisableReason           string     `json:"disable_reason,omitempty"`
	DisabledAt              *time.Time `json:"disabled_at,omitempty"`
	RecoverAfter            *time.Time `json:"recover_after,omitempty"`
	DeleteEligibleAt        *time.Time `json:"delete_eligible_at,omitempty"`
	DeleteRetryAfter        *time.Time `json:"delete_retry_after,omitempty"`
	AutoAction              string     `json:"auto_action,omitempty"`
	AutoActionStatus        string     `json:"auto_action_status,omitempty"`
	AutoDisableEligible     bool       `json:"auto_disable_eligible"`
	InspectionEnabled       bool       `json:"inspection_enabled"`
	AutoDisableEnabled      bool       `json:"auto_disable_enabled"`
	AutoEnableEnabled       bool       `json:"auto_enable_enabled"`
	AutoDeleteEnabled       bool       `json:"auto_delete_enabled"`
	FailureThreshold        int        `json:"failure_threshold"`
	FailureStreak           int        `json:"failure_streak"`
	RecoveryThreshold       int        `json:"recovery_threshold"`
	HealthyStreak           int        `json:"healthy_streak"`
	PassiveCircuitEnabled   bool       `json:"passive_circuit_enabled"`
	PassiveFailureThreshold int        `json:"passive_failure_threshold"`
	PassiveFailureStreak    int        `json:"passive_failure_streak"`
	CircuitOpen             bool       `json:"circuit_open"`
	CircuitReasonCode       string     `json:"circuit_reason_code,omitempty"`
}

type InspectionAction struct {
	ID         string    `json:"id"`
	AccountID  string    `json:"account_id"`
	Name       string    `json:"name,omitempty"`
	Provider   string    `json:"provider,omitempty"`
	Action     string    `json:"action"`
	Status     string    `json:"status"`
	ReasonCode string    `json:"reason_code"`
	CreatedAt  time.Time `json:"created_at"`
}

type InspectionDeleteResult struct {
	AccountID string `json:"account_id"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
}

type InspectionDeleteRun struct {
	Attempted int                      `json:"attempted"`
	Succeeded int                      `json:"succeeded"`
	Failed    int                      `json:"failed"`
	Skipped   int                      `json:"skipped"`
	Results   []InspectionDeleteResult `json:"results,omitempty"`
}

type InspectionResultQuery struct {
	Page     int
	PageSize int
	Health   string
	Search   string
}

type InspectionResultList struct {
	Results  []InspectionResult `json:"results"`
	Total    int                `json:"total"`
	Page     int                `json:"page"`
	PageSize int                `json:"page_size"`
	Pages    int                `json:"pages"`
}

func defaultInspectionPolicy() InspectionPolicy {
	return InspectionPolicy{
		ScanIntervalMinutes:         defaultInspectionInterval,
		ModelProbeIntervalMinutes:   defaultModelProbeInterval,
		ModelProbeBatchSize:         defaultModelProbeBatchSize,
		ModelProbeModels:            defaultModelProbeModels(),
		FailureThreshold:            defaultFailureThreshold,
		RecoveryThreshold:           defaultRecoveryThreshold,
		PassiveFailureThreshold:     defaultPassiveThreshold,
		PassiveFailureWindowMinutes: defaultPassiveWindow,
		PassiveCircuitMinutes:       defaultPassiveCircuit,
		DeleteGraceHours:            defaultDeleteGraceHours,
		DeleteBatchSize:             defaultDeleteBatchSize,
		AnomalyThresholdPercent:     defaultAnomalyThreshold,
		AnomalyMinimumAccounts:      defaultAnomalyMinimum,
		AnomalyCooldownMinutes:      defaultAnomalyCooldown,
	}
}

func defaultModelProbeModels() ModelProbeModels {
	return ModelProbeModels{
		Codex:  "gpt-5.4",
		OpenAI: "gpt-5.4",
		Claude: "claude-sonnet-4-5-20250929",
		Gemini: "gemini-2.0-flash",
		XAI:    "grok-4",
	}
}

func normalizeInspectionPolicy(policy InspectionPolicy) InspectionPolicy {
	if policy.ScanIntervalMinutes == 0 {
		policy.ScanIntervalMinutes = defaultInspectionInterval
	}
	if policy.ModelProbeIntervalMinutes == 0 {
		policy.ModelProbeIntervalMinutes = defaultModelProbeInterval
	}
	if policy.ModelProbeBatchSize == 0 {
		policy.ModelProbeBatchSize = defaultModelProbeBatchSize
	}
	defaults := defaultModelProbeModels()
	if strings.TrimSpace(policy.ModelProbeModels.Codex) == "" {
		policy.ModelProbeModels.Codex = defaults.Codex
	}
	if strings.TrimSpace(policy.ModelProbeModels.OpenAI) == "" {
		policy.ModelProbeModels.OpenAI = defaults.OpenAI
	}
	if strings.TrimSpace(policy.ModelProbeModels.Claude) == "" {
		policy.ModelProbeModels.Claude = defaults.Claude
	}
	if strings.TrimSpace(policy.ModelProbeModels.Gemini) == "" {
		policy.ModelProbeModels.Gemini = defaults.Gemini
	}
	if strings.TrimSpace(policy.ModelProbeModels.XAI) == "" {
		policy.ModelProbeModels.XAI = defaults.XAI
	}
	if policy.FailureThreshold == 0 {
		policy.FailureThreshold = defaultFailureThreshold
	}
	if policy.RecoveryThreshold == 0 {
		policy.RecoveryThreshold = defaultRecoveryThreshold
	}
	if policy.PassiveFailureThreshold == 0 {
		policy.PassiveFailureThreshold = defaultPassiveThreshold
	}
	if policy.PassiveFailureWindowMinutes == 0 {
		policy.PassiveFailureWindowMinutes = defaultPassiveWindow
	}
	if policy.PassiveCircuitMinutes == 0 {
		policy.PassiveCircuitMinutes = defaultPassiveCircuit
	}
	if policy.DeleteGraceHours == 0 {
		policy.DeleteGraceHours = defaultDeleteGraceHours
	}
	if policy.DeleteBatchSize == 0 {
		policy.DeleteBatchSize = defaultDeleteBatchSize
	}
	if policy.AnomalyThresholdPercent == 0 {
		policy.AnomalyThresholdPercent = defaultAnomalyThreshold
	}
	if policy.AnomalyMinimumAccounts == 0 {
		policy.AnomalyMinimumAccounts = defaultAnomalyMinimum
	}
	if policy.AnomalyCooldownMinutes == 0 {
		policy.AnomalyCooldownMinutes = defaultAnomalyCooldown
	}
	return policy
}

func validateInspectionPolicy(policy InspectionPolicy) (InspectionPolicy, error) {
	policy = normalizeInspectionPolicy(policy)
	if policy.ScanIntervalMinutes < minInspectionInterval || policy.ScanIntervalMinutes > maxInspectionInterval {
		return InspectionPolicy{}, fmt.Errorf("scan_interval_minutes must be between %d and %d", minInspectionInterval, maxInspectionInterval)
	}
	if policy.ModelProbeIntervalMinutes < minInspectionInterval || policy.ModelProbeIntervalMinutes > maxInspectionInterval {
		return InspectionPolicy{}, fmt.Errorf("model_probe_interval_minutes must be between %d and %d", minInspectionInterval, maxInspectionInterval)
	}
	if policy.ModelProbeBatchSize < 1 || policy.ModelProbeBatchSize > maxModelProbeBatchSize {
		return InspectionPolicy{}, fmt.Errorf("model_probe_batch_size must be between 1 and %d", maxModelProbeBatchSize)
	}
	for provider, model := range map[string]string{
		"codex": policy.ModelProbeModels.Codex, "openai": policy.ModelProbeModels.OpenAI,
		"claude": policy.ModelProbeModels.Claude, "gemini": policy.ModelProbeModels.Gemini, "xai": policy.ModelProbeModels.XAI,
	} {
		if safeModelIdentifier(model) == "" {
			return InspectionPolicy{}, fmt.Errorf("model_probe_models.%s contains unsupported characters or exceeds 128 characters", provider)
		}
	}
	if policy.FailureThreshold < 2 || policy.FailureThreshold > 10 {
		return InspectionPolicy{}, fmt.Errorf("failure_threshold must be between 2 and 10")
	}
	if policy.RecoveryThreshold < 1 || policy.RecoveryThreshold > 10 {
		return InspectionPolicy{}, fmt.Errorf("recovery_threshold must be between 1 and 10")
	}
	if policy.PassiveFailureThreshold < 2 || policy.PassiveFailureThreshold > 100 {
		return InspectionPolicy{}, fmt.Errorf("passive_failure_threshold must be between 2 and 100")
	}
	if policy.PassiveFailureWindowMinutes < 1 || policy.PassiveFailureWindowMinutes > maxInspectionInterval {
		return InspectionPolicy{}, fmt.Errorf("passive_failure_window_minutes must be between 1 and %d", maxInspectionInterval)
	}
	if policy.PassiveCircuitMinutes < 1 || policy.PassiveCircuitMinutes > maxInspectionInterval {
		return InspectionPolicy{}, fmt.Errorf("passive_circuit_minutes must be between 1 and %d", maxInspectionInterval)
	}
	if policy.DeleteGraceHours < 24 || policy.DeleteGraceHours > maxDeleteGraceHours {
		return InspectionPolicy{}, fmt.Errorf("delete_grace_hours must be between 24 and %d", maxDeleteGraceHours)
	}
	if policy.DeleteBatchSize < 1 || policy.DeleteBatchSize > maxDeleteBatchSize {
		return InspectionPolicy{}, fmt.Errorf("delete_batch_size must be between 1 and %d", maxDeleteBatchSize)
	}
	if policy.AnomalyThresholdPercent < 1 || policy.AnomalyThresholdPercent > 100 {
		return InspectionPolicy{}, fmt.Errorf("anomaly_threshold_percent must be between 1 and 100")
	}
	if policy.AnomalyMinimumAccounts < 1 || policy.AnomalyMinimumAccounts > maxInspectionAccounts {
		return InspectionPolicy{}, fmt.Errorf("anomaly_minimum_accounts must be between 1 and %d", maxInspectionAccounts)
	}
	if policy.AnomalyCooldownMinutes < minInspectionInterval || policy.AnomalyCooldownMinutes > maxInspectionInterval {
		return InspectionPolicy{}, fmt.Errorf("anomaly_cooldown_minutes must be between %d and %d", minInspectionInterval, maxInspectionInterval)
	}
	if policy.AutoDelete && !policy.AutoDisable {
		return InspectionPolicy{}, fmt.Errorf("auto_delete requires auto_disable")
	}
	if policy.AutoDeleteInvalidCredentials && (!policy.AutoDelete || !policy.AutoDisable) {
		return InspectionPolicy{}, fmt.Errorf("auto_delete_invalid_credentials requires auto_delete and auto_disable")
	}
	if policy.PassiveCircuitEnabled && (!policy.AutoDisable || !policy.AutoEnable) {
		return InspectionPolicy{}, fmt.Errorf("passive_circuit_enabled requires auto_disable and auto_enable")
	}
	if policy.ModelProbeFullSweep && !policy.ModelProbeEnabled {
		return InspectionPolicy{}, fmt.Errorf("model_probe_full_sweep requires scheduled model probes")
	}
	if policy.AnomalyTriggerEnabled && !policy.Enabled {
		return InspectionPolicy{}, fmt.Errorf("anomaly_trigger_enabled requires scheduled native inspection")
	}
	return policy, nil
}

func normalizeInspectionResultQuery(query InspectionResultQuery) InspectionResultQuery {
	if query.Page < 1 {
		query.Page = 1
	}
	if query.PageSize < 1 {
		query.PageSize = 50
	}
	if query.PageSize > maxInspectionResultPageSize {
		query.PageSize = maxInspectionResultPageSize
	}
	query.Health = strings.ToLower(strings.TrimSpace(query.Health))
	query.Search = strings.ToLower(strings.TrimSpace(query.Search))
	return query
}

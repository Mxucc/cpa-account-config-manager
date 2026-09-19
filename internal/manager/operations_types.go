package manager

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	operationPageSize = 500

	OperationCategoryAccount       = "account"
	OperationCategoryBatch         = "batch"
	OperationCategoryImport        = "import"
	OperationCategoryExport        = "export"
	OperationCategoryDefaultPolicy = "default_policy"
	OperationCategoryInspection    = "inspection"
	OperationCategoryUpdate        = "update"
	OperationCategoryJournal       = "journal"
	OperationCategoryOpenCode      = "opencode"
	OperationCategoryPlugin        = "plugin"
	OperationCategoryModelError    = "model_error"

	OperationActionDelete                 = "delete"
	OperationActionTokenRefresh           = "token_refresh"
	OperationActionModelTest              = "model_test"
	OperationActionQuotaMetadataRefresh   = "quota_metadata_refresh"
	OperationActionActiveReset            = "active_reset"
	OperationActionAutoModelWhitelist     = "auto_model_whitelist"
	OperationActionAgentIdentityLogin     = "agent_identity_login"
	OperationActionBatchEdit              = "batch_edit"
	OperationActionBatchRetry             = "batch_retry"
	OperationActionBatchDelete            = "batch_delete"
	OperationActionBatchDeleteRetry       = "batch_delete_retry"
	OperationActionImport                 = "import"
	OperationActionExportAccounts         = "export_accounts"
	OperationActionExportResults          = "export_results"
	OperationActionPolicySave             = "policy_save"
	OperationActionPolicyScan             = "policy_scan"
	OperationActionForceSync              = "force_sync"
	OperationActionInspectionSave         = "inspection_policy_save"
	OperationActionInspectionScan         = "inspection_scan"
	OperationActionInspectionManualDelete = "inspection_manual_delete"
	OperationActionAnomalyNotification    = "anomaly_notification"
	OperationActionNotificationTest       = "notification_test"
	OperationActionAutoDisable            = "auto_disable"
	OperationActionAutoEnable             = "auto_enable"
	OperationActionDeleteCandidate        = "delete_candidate"
	OperationActionAutoDelete             = "auto_delete"
	OperationActionReviewResolve          = "review_resolve"
	OperationActionReviewIgnore           = "review_ignore"
	OperationActionReviewReopen           = "review_reopen"
	OperationActionUpdateSave             = "update_policy_save"
	OperationActionUpdateCheck            = "update_check"
	OperationActionUpdateInstall          = "update_install"
	OperationActionJournalClear           = "journal_clear"
	OperationActionOpenCodeSave           = "opencode_save"
	OperationActionOpenCodeRemove         = "opencode_remove"
	OperationActionOpenCodeRefresh        = "opencode_refresh"
	OperationActionPluginConfigure        = "plugin_configure"
	OperationActionModelFailure           = "model_failure"

	OperationStatusRunning     = "running"
	OperationStatusSucceeded   = "succeeded"
	OperationStatusPartial     = "partial"
	OperationStatusFailed      = "failed"
	OperationStatusInterrupted = "interrupted"
	OperationStatusWarning     = "warning"
	OperationStatusSkipped     = "skipped"

	OperationSourceManual        = "manual"
	OperationSourceBackground    = "background"
	OperationSourceDefaultPolicy = "default_policy"
	OperationSourceInspection    = "inspection"
	OperationSourceImport        = "import"
	OperationSourcePluginStore   = "plugin_store"

	OperationScopeSingle    = "single"
	OperationScopeSelected  = "selected"
	OperationScopeFiltered  = "filtered"
	OperationScopeAll       = "all"
	OperationScopeScheduled = "scheduled"
	OperationScopeSystem    = "system"

	OperationFailurePolicyAuthScan               = "policy_auth_scan_failed"
	OperationFailurePolicyAuthRead               = "policy_auth_read_failed"
	OperationFailurePolicyAccountIdentity        = "policy_account_identity_changed"
	OperationFailurePolicyAuthSource             = "policy_auth_source_changed"
	OperationFailurePolicyAuthFilename           = "policy_auth_filename_invalid"
	OperationFailurePolicyAuthProjection         = "policy_auth_projection_failed"
	OperationFailurePolicyAuthJSON               = "policy_auth_json_invalid"
	OperationFailurePolicyAuthUpdate             = "policy_auth_update_failed"
	OperationFailurePolicyAuthSave               = "policy_auth_save_failed"
	OperationFailurePolicyModelPolicyUnavailable = "policy_model_policy_unavailable"
	OperationFailurePolicyModelPolicyApply       = "policy_model_policy_apply_failed"
	OperationFailurePolicyQuotaMetadata          = "policy_quota_metadata_probe_failed"
	OperationFailurePolicyStatePersist           = "policy_state_persist_failed"
	OperationFailureInspectionAuthHost           = "inspection_auth_host_unavailable"
	OperationFailureInspectionAccountReadOnly    = "inspection_account_not_editable"
	OperationFailureInspectionOwnership          = "inspection_disable_ownership_changed"
	OperationFailureInspectionAuthRead           = "inspection_auth_read_failed"
	OperationFailureInspectionAuthIdentity       = "inspection_auth_identity_changed"
	OperationFailureInspectionAuthSource         = "inspection_auth_source_changed"
	OperationFailureInspectionAuthJSON           = "inspection_auth_json_invalid"
	OperationFailureInspectionAuthField          = "inspection_auth_field_invalid"
	OperationFailureInspectionAuthUpdate         = "inspection_auth_update_failed"
	OperationFailureInspectionAuthSave           = "inspection_auth_save_failed"
	OperationFailureInspectionMutation           = "inspection_mutation_failed"
	OperationFailureModelTestInspectionRecord    = "model_test_inspection_record_failed"
	// Model-error journal reasons: one plain upstream failure, one request that
	// consumed the configured retry budget completely, and one client cancel.
	OperationFailureModelUpstream       = "model_upstream_failed"
	OperationFailureModelRetryExhausted = "model_retry_exhausted"
	OperationFailureModelCanceled       = "model_request_canceled"
	// OperationFailureClinePassCredentialRejected is the Cline Pass credential the gateway refused:
	// CPA keeps routing through the key on the channel row, so the account is unroutable until the
	// automatic repair rewrites that row.
	OperationFailureClinePassCredentialRejected = "cline_pass_credential_rejected"
)

type OperationFailureDetail struct {
	ReasonCode       string   `json:"reason_code"`
	Count            int      `json:"count"`
	SampleAccountIDs []string `json:"sample_account_ids,omitempty"`
}

type OperationEntry struct {
	ID              string                   `json:"id"`
	EventID         string                   `json:"event_id,omitempty"`
	Category        string                   `json:"category"`
	Action          string                   `json:"action"`
	Status          string                   `json:"status"`
	Source          string                   `json:"source"`
	Scope           string                   `json:"scope,omitempty"`
	TargetID        string                   `json:"target_id,omitempty"`
	TargetCount     int                      `json:"target_count"`
	Succeeded       int                      `json:"succeeded"`
	Failed          int                      `json:"failed"`
	Skipped         int                      `json:"skipped"`
	StartedAt       time.Time                `json:"started_at"`
	FinishedAt      time.Time                `json:"finished_at,omitempty"`
	ReasonCode      string                   `json:"reason_code,omitempty"`
	RelatedJobID    string                   `json:"related_job_id,omitempty"`
	RelatedActionID string                   `json:"related_action_id,omitempty"`
	Version         string                   `json:"version,omitempty"`
	Format          string                   `json:"format,omitempty"`
	Model           string                   `json:"model,omitempty"`
	HTTPStatus      int                      `json:"http_status,omitempty"`
	Attempts        int                      `json:"attempts,omitempty"`
	Message         string                   `json:"message,omitempty"`
	FailureDetails  []OperationFailureDetail `json:"failure_details,omitempty"`
}

type OperationSummary struct {
	Total       int `json:"total"`
	Running     int `json:"running"`
	Succeeded   int `json:"succeeded"`
	Failed      int `json:"failed"`
	Attention   int `json:"attention"`
	Interrupted int `json:"interrupted"`
}

type OperationListResponse struct {
	Operations       []OperationEntry `json:"operations"`
	Summary          OperationSummary `json:"summary"`
	Total            int              `json:"total"`
	Page             int              `json:"page"`
	PageSize         int              `json:"page_size"`
	Pages            int              `json:"pages"`
	ExtendedHistory  bool             `json:"extended_history"`
	ArchivedSegments int              `json:"archived_segments"`
	RetentionLimit   int              `json:"retention_limit"`
	Retained         int              `json:"retained"`
	StorageError     string           `json:"storage_error,omitempty"`
}

type OperationQuery struct {
	Page     int
	PageSize int
	Category string
	Status   string
	Source   string
	Search   string
}

type OperationRecordRequest struct {
	Action  string `json:"action"`
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
}

type OperationRetentionSettings struct {
	ExtendedHistory  bool `json:"extended_history"`
	PageSize         int  `json:"page_size"`
	Retained         int  `json:"retained"`
	ArchivedSegments int  `json:"archived_segments"`
}

type OperationRetentionUpdateRequest struct {
	ExtendedHistory *bool `json:"extended_history"`
}

func normalizeOperationQuery(query OperationQuery) OperationQuery {
	if query.Page < 1 {
		query.Page = 1
	}
	query.PageSize = operationPageSize
	query.Category = normalizeOperationCategory(query.Category)
	query.Status = normalizeOperationStatus(query.Status)
	query.Source = normalizeOperationSource(query.Source)
	query.Search = strings.ToLower(strings.TrimSpace(query.Search))
	return query
}

func validateBrowserOperationRecord(request OperationRecordRequest) (OperationEntry, error) {
	action := strings.ToLower(strings.TrimSpace(request.Action))
	if action != OperationActionUpdateInstall {
		return OperationEntry{}, fmt.Errorf("unsupported operation action")
	}
	status := normalizeOperationStatus(request.Status)
	if status != OperationStatusSucceeded && status != OperationStatusFailed && status != OperationStatusWarning {
		return OperationEntry{}, fmt.Errorf("unsupported operation status")
	}
	version := safeOperationVersion(request.Version)
	if strings.TrimSpace(request.Version) != "" && version == "" {
		return OperationEntry{}, fmt.Errorf("version must be a semantic version")
	}
	reason := ""
	if status == OperationStatusWarning {
		reason = "restart_required"
	} else if status == OperationStatusFailed {
		reason = "install_failed"
	}
	return OperationEntry{
		Category:   OperationCategoryUpdate,
		Action:     action,
		Status:     status,
		Source:     OperationSourcePluginStore,
		Scope:      OperationScopeSystem,
		Version:    version,
		ReasonCode: reason,
	}, nil
}

func normalizeOperationCategory(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case OperationCategoryAccount, OperationCategoryBatch, OperationCategoryImport, OperationCategoryExport,
		OperationCategoryDefaultPolicy, OperationCategoryInspection, OperationCategoryUpdate, OperationCategoryJournal,
		OperationCategoryOpenCode, OperationCategoryPlugin, OperationCategoryModelError:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeOperationAction(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case OperationActionDelete, OperationActionTokenRefresh, OperationActionModelTest, OperationActionQuotaMetadataRefresh, OperationActionActiveReset, OperationActionAutoModelWhitelist, OperationActionAgentIdentityLogin, OperationActionBatchEdit, OperationActionBatchRetry,
		OperationActionBatchDelete, OperationActionBatchDeleteRetry, OperationActionImport,
		OperationActionExportAccounts, OperationActionExportResults, OperationActionPolicySave,
		OperationActionPolicyScan, OperationActionForceSync, OperationActionInspectionSave,
		OperationActionInspectionScan, OperationActionInspectionManualDelete, OperationActionAnomalyNotification, OperationActionNotificationTest, OperationActionAutoDisable, OperationActionAutoEnable,
		OperationActionDeleteCandidate, OperationActionAutoDelete, OperationActionUpdateSave,
		OperationActionReviewResolve, OperationActionReviewIgnore, OperationActionReviewReopen,
		OperationActionUpdateCheck, OperationActionUpdateInstall, OperationActionJournalClear,
		OperationActionOpenCodeSave, OperationActionOpenCodeRemove, OperationActionOpenCodeRefresh,
		OperationActionPluginConfigure, OperationActionModelFailure:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeOperationStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case OperationStatusRunning, OperationStatusSucceeded, OperationStatusPartial, OperationStatusFailed,
		OperationStatusInterrupted, OperationStatusWarning, OperationStatusSkipped:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeOperationSource(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case OperationSourceManual, OperationSourceBackground, OperationSourceDefaultPolicy,
		OperationSourceInspection, OperationSourceImport, OperationSourcePluginStore:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeOperationScope(value string) string {
	trimmed := strings.TrimSpace(value)
	switch strings.ToLower(trimmed) {
	case OperationScopeSingle, OperationScopeSelected, OperationScopeFiltered, OperationScopeAll,
		OperationScopeScheduled, OperationScopeSystem:
		return strings.ToLower(trimmed)
	default:
		// A model-error entry scopes to the credential CPA selected, so a bounded
		// safe identifier is preserved alongside the enum values.
		return safeOperationScopeIdentifier(trimmed)
	}
}

// safeOperationScopeIdentifier accepts the auth-id shape a model-error entry
// scopes to: one bounded token, no whitespace, no control characters and no
// credential-looking text.
func safeOperationScopeIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 160 {
		return ""
	}
	lower := strings.ToLower(value)
	for _, sensitive := range []string{"authorization", "bearer", "cookie", "password", "secret", "token"} {
		if strings.Contains(lower, sensitive) {
			return ""
		}
	}
	for _, character := range value {
		if !unicode.IsLetter(character) && !unicode.IsDigit(character) && !strings.ContainsRune("@._:+-", character) {
			return ""
		}
	}
	return value
}

const operationMessageLimit = 600

var (
	operationMessageBearer        = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]+`)
	operationMessageBasic         = regexp.MustCompile(`(?i)\bbasic\s+[A-Za-z0-9+/=]{6,}`)
	operationMessageAuthorization = regexp.MustCompile(`(?i)\bauthorization\s*[:=]\s*[^\s,;)]+`)
	operationMessageKeyValue      = regexp.MustCompile(`(?i)\b(api[_-]?key|apikey|x-api-key|access[_-]?token|refresh[_-]?token|client[_-]?secret|password|passwd)\s*[:=]\s*["']?[A-Za-z0-9._~+/=-]{4,}["']?`)
	operationMessageOpenAIKey     = regexp.MustCompile(`(?i)\bsk-[A-Za-z0-9_-]{6,}`)
	operationMessagePersonalToken = regexp.MustCompile(`(?i)\bat-[A-Za-z0-9_-]{6,}`)
	operationMessageJWT           = regexp.MustCompile(`\b[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)
	operationMessageLongRun       = regexp.MustCompile(`[A-Za-z0-9+/=_-]{40,}`)
)

// sanitizeOperationMessage bounds and flattens one upstream error message for the
// model-error journal. Unlike sanitizeAIProviderProbeError, the upstream text is
// kept: it is the point of the field. Every obvious credential is removed first,
// so no token can survive into the persisted entry, then the message is collapsed
// to a single line and bounded. An empty result stays empty and is omitted by the
// entry's JSON tag.
func sanitizeOperationMessage(value string) string {
	value = operationMessageBearer.ReplaceAllString(value, "Bearer [redacted]")
	value = operationMessageBasic.ReplaceAllString(value, "Basic [redacted]")
	value = operationMessageAuthorization.ReplaceAllString(value, "Authorization=[redacted]")
	value = operationMessageKeyValue.ReplaceAllString(value, "$1=[redacted]")
	value = operationMessageOpenAIKey.ReplaceAllString(value, "[redacted-api-key]")
	value = operationMessagePersonalToken.ReplaceAllString(value, "[redacted-token]")
	value = operationMessageJWT.ReplaceAllString(value, "[redacted]")
	value = operationMessageLongRun.ReplaceAllString(value, "[redacted]")
	value = strings.Join(strings.Fields(value), " ")
	if runes := []rune(value); len(runes) > operationMessageLimit {
		value = string(runes[:operationMessageLimit])
	}
	return value
}

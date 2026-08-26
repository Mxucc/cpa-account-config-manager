package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	inspectionMutationOwner    = "account-inspection"
	inspectionMutationRetry    = time.Second
	inspectionDeleteRetry      = 5 * time.Minute
	maxManualInspectionDeletes = 100
	quotaRecoveryPriorityFloor = 100
)

type inspectionAutomaticActionSummary struct {
	InspectionRunSummary
	Deferred bool
}

var (
	ErrInspectionDeleteConfirmation = errors.New("inspection deletion requires explicit confirmation")
	ErrInspectionDeleteIDsRequired  = errors.New("inspection deletion account_ids are required")
	ErrInspectionDeleteTooMany      = errors.New("inspection deletion supports at most 100 accounts per request")
)

type inspectionMutationResult struct {
	Changed  bool
	Name     string
	Path     string
	Revision string
}

type inspectionDeleteValidation struct {
	Eligible bool
	Retry    bool
	Reason   string
}

func (e *InspectionEngine) applyAutomaticActions(
	ctx context.Context,
	policy InspectionPolicy,
	accounts map[string]Account,
	records map[string]inspectionRecord,
	now time.Time,
	managementBaseURL string,
	managementKey string,
) (inspectionAutomaticActionSummary, []InspectionAction) {
	summary := inspectionAutomaticActionSummary{}
	e.mu.RLock()
	owner := e.backgroundOwner
	e.mu.RUnlock()
	if !backgroundWorkAllowed(owner) {
		return summary, nil
	}
	if !policy.AutoDisable && !policy.AutoEnable && !policy.AutoDelete {
		return summary, nil
	}
	e.applyAutomaticDisableProbeGates(ctx, policy, accounts, records, managementBaseURL, managementKey)
	if e.mutations == nil || !e.mutations.TryAcquire(inspectionMutationOwner) {
		summary.Deferred = true
		return summary, nil
	}
	defer e.mutations.Release(inspectionMutationOwner)
	var writer ManagementWriter
	if strings.TrimSpace(managementKey) != "" {
		client, errClient := newManagementClient(resolveManagementBaseURL(managementBaseURL), managementKey, nil)
		if errClient != nil {
			summary.Error = "CPA account status API is unavailable"
			return summary, nil
		}
		writer = client
		defer clearManagementWriterSecrets(writer)
	}

	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	sortInspectionIDs(ids)
	actions := make([]InspectionAction, 0)
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		account, exists := accounts[id]
		if !exists {
			continue
		}
		record := records[id]

		if record.Result.OwnedDisable && !account.Disabled {
			clearInspectionDisableOwnership(&record)
			record.Result.AutoAction = ""
			record.Result.AutoActionStatus = ""
			records[id] = record
			continue
		}

		if shouldAutoEnableInspection(policy, account, record, now) {
			outcome, errMutation := e.setInspectionDisabled(ctx, account, record, false, quotaRecoveryPriority(policy, account, record, now), writer)
			action := newInspectionAction(record.Result, InspectionActionEnable, inspectionAutoEnableReason(account, record, now), now)
			if errMutation != nil {
				action.Status = InspectionActionFailed
				action.FailureReason = inspectionMutationFailureReason(errMutation)
				if inspectionMutationFailureRetryable(action.FailureReason) {
					summary.Deferred = true
				}
				record.Result.AutoAction = InspectionActionEnable
				record.Result.AutoActionStatus = InspectionActionFailed
				summary.Failed++
			} else if !outcome.Changed {
				e.stopOverdraftCycle(id)
				action.Status = InspectionActionSkipped
				clearInspectionDisableOwnership(&record)
				record.Result.Disabled = false
				record.Result.AutoAction = InspectionActionEnable
				record.Result.AutoActionStatus = InspectionActionSkipped
			} else {
				e.stopOverdraftCycle(id)
				action.Status = InspectionActionSucceeded
				clearInspectionDisableOwnership(&record)
				record.Result.Disabled = false
				record.Result.AutoAction = InspectionActionEnable
				record.Result.AutoActionStatus = InspectionActionSucceeded
				summary.AutoEnabled++
			}
			actions = append(actions, action)
			records[id] = record
			continue
		}

		openCircuit, circuitReason, circuitFailures := shouldOpenPassiveCircuit(policy, account, record, now)
		if (shouldAutoDisableInspection(policy, account, record) || openCircuit) && e.inspectionAutoDisableAllowed(record.Result) {
			disableReason := record.Result.ReasonCode
			if openCircuit {
				disableReason = "passive_circuit_open"
			}
			outcome, errMutation := e.setInspectionDisabled(ctx, account, record, true, nil, writer)
			action := newInspectionAction(record.Result, InspectionActionDisable, disableReason, now)
			if errMutation != nil {
				action.Status = InspectionActionFailed
				action.FailureReason = inspectionMutationFailureReason(errMutation)
				if inspectionMutationFailureRetryable(action.FailureReason) {
					summary.Deferred = true
				}
				record.Result.AutoAction = InspectionActionDisable
				record.Result.AutoActionStatus = InspectionActionFailed
				summary.Failed++
			} else if !outcome.Changed {
				action.Status = InspectionActionSkipped
				record.Result.AutoAction = InspectionActionDisable
				record.Result.AutoActionStatus = InspectionActionSkipped
			} else {
				action.Status = InspectionActionSucceeded
				record.Result.Disabled = true
				record.Result.OwnedDisable = true
				record.Result.AutoAction = InspectionActionDisable
				record.Result.AutoActionStatus = InspectionActionSucceeded
				record.DisableReason = disableReason
				record.DisabledAt = now.UTC()
				record.DisabledName = outcome.Name
				record.DisabledPath = outcome.Path
				record.DisabledVersion = outcome.Revision
				if openCircuit {
					recoverAfter := now.Add(time.Duration(policy.PassiveCircuitMinutes) * time.Minute).UTC()
					record.Result.CircuitOpen = true
					record.Result.CircuitReasonCode = safeOptionalInspectionReason(circuitReason)
					record.Result.Recommendation = InspectionRecommendationDisable
					record.Result.FailureStreak = circuitFailures
					record.Result.RecoverAfter = timePointer(recoverAfter)
					record.DisabledRecoverAfter = recoverAfter
				} else if record.Result.RecoverAfter != nil {
					record.DisabledRecoverAfter = record.Result.RecoverAfter.UTC()
				}
				summary.AutoDisabled++
			}
			actions = append(actions, action)
			records[id] = record
		}

		record = records[id]
		if markInspectionDeleteCandidate(policy, &record, now) {
			summary.DeletePending++
			if record.Result.AutoAction != InspectionActionDeleteCandidate || record.Result.AutoActionStatus != InspectionActionPending {
				action := newInspectionAction(record.Result, InspectionActionDeleteCandidate, record.DisableReason, now)
				action.Status = InspectionActionPending
				actions = append(actions, action)
			}
			record.Result.AutoAction = InspectionActionDeleteCandidate
			record.Result.AutoActionStatus = InspectionActionPending
			records[id] = record
		} else if record.Result.AutoAction == InspectionActionDeleteCandidate {
			record.Result.AutoAction = ""
			record.Result.AutoActionStatus = ""
			record.Result.DeleteEligibleAt = nil
			record.DeleteRetryAfter = time.Time{}
			records[id] = record
		}
	}
	return summary, actions
}

func (e *InspectionEngine) stopOverdraftCycle(accountID string) {
	if e == nil {
		return
	}
	e.mu.RLock()
	tracker := e.overdraftCycles
	e.mu.RUnlock()
	if tracker != nil {
		tracker.StopOverdraftCycle(accountID)
	}
}

func (e *InspectionEngine) beginOverdraftCycle(accountID, quotaWindow string, exhaustedAt time.Time) {
	if e == nil {
		return
	}
	e.mu.RLock()
	tracker := e.overdraftCycles
	e.mu.RUnlock()
	if tracker != nil {
		tracker.BeginOverdraftCycle(accountID, quotaWindow, exhaustedAt)
	}
}

func (e *InspectionEngine) markOverdraftCycle(accountID, quotaWindow, status, reason string, testedAt time.Time) {
	if e == nil {
		return
	}
	e.mu.RLock()
	tracker := e.overdraftCycles
	e.mu.RUnlock()
	if tracker != nil {
		tracker.MarkOverdraftCycle(accountID, quotaWindow, status, reason, testedAt)
	}
}

func shouldOpenPassiveCircuit(policy InspectionPolicy, account Account, record inspectionRecord, now time.Time) (bool, string, int) {
	if !policy.PassiveCircuitEnabled || !policy.AutoDisable || !policy.AutoEnable || account.Disabled || !account.Editable || record.Result.OwnedDisable {
		return false, "", 0
	}
	count, reason := passiveCircuitFailureEvidence(policy, record, now)
	return count >= policy.PassiveFailureThreshold, reason, count
}

func passiveCircuitThresholdReached(policy InspectionPolicy, record inspectionRecord) bool {
	policy = normalizeInspectionPolicy(policy)
	return policy.PassiveCircuitEnabled && policy.AutoDisable && policy.AutoEnable &&
		record.Signal.ConsecutiveFailures == policy.PassiveFailureThreshold &&
		!record.Signal.AutoDisableEligible && passiveCircuitReasonAllowed(record.Signal.ReasonCode)
}

func passiveCircuitFailureEvidence(policy InspectionPolicy, record inspectionRecord, now time.Time) (int, string) {
	policy = normalizeInspectionPolicy(policy)
	window := time.Duration(policy.PassiveFailureWindowMinutes) * time.Minute
	bestCount := 0
	bestReason := ""
	if !record.Signal.LastFailureAt.IsZero() && !now.Before(record.Signal.LastFailureAt) && now.Sub(record.Signal.LastFailureAt) <= window &&
		passiveCircuitReasonAllowed(record.Signal.ReasonCode) && !record.Signal.AutoDisableEligible {
		bestCount = boundedCounter(record.Signal.ConsecutiveFailures)
		bestReason = record.Signal.ReasonCode
	}
	if !record.Probe.TestedAt.IsZero() && !now.Before(record.Probe.TestedAt) && now.Sub(record.Probe.TestedAt) <= window &&
		record.Probe.Source != InspectionProbeSourceManual && passiveCircuitReasonAllowed(record.Probe.ReasonCode) && record.Probe.ConsecutiveFailures > bestCount {
		bestCount = boundedCounter(record.Probe.ConsecutiveFailures)
		bestReason = record.Probe.ReasonCode
	}
	return bestCount, safeOptionalInspectionReason(bestReason)
}

func passiveCircuitReasonAllowed(reason string) bool {
	switch safeOptionalInspectionReason(reason) {
	case "authentication_review", "transient_failure", "unconfirmed_upstream_response", "quota_limited",
		"request_timeout", "upstream_unavailable", "invalid_response":
		return true
	default:
		return false
	}
}

func shouldAutoDisableInspection(policy InspectionPolicy, account Account, record inspectionRecord) bool {
	if !policy.AutoDisable || account.Disabled || !account.Editable || record.Result.OwnedDisable || !record.Result.AutoDisableEligible {
		return false
	}
	if record.Result.SignalSource == InspectionSignalActiveProbe {
		credentialEvidence := record.Probe.Kind == InspectionProbeKindCredential ||
			inspectionProbeConfirmsCredentialFailure(record.Probe.Kind, record.Probe.ReasonCode, record.Probe.StatusCode)
		return credentialEvidence && record.Probe.ReasonCode != "" &&
			record.Probe.ReasonCode != "credential_response_ok" && record.Probe.Status != "unsupported"
	}
	if record.Result.SignalSource == InspectionSignalNative && record.Result.Confidence == InspectionConfidenceHigh {
		return true
	}
	if !inspectionResultIsStrongFailure(record.Result) && record.Result.ReasonCode != "credential_permission_denied" {
		return false
	}
	return record.Result.FailureStreak >= policy.FailureThreshold || record.Signal.ConsecutiveFailures >= policy.FailureThreshold
}

func shouldAutoEnableInspection(policy InspectionPolicy, account Account, record inspectionRecord, now time.Time) bool {
	if !policy.AutoEnable || !record.Result.OwnedDisable || !account.Disabled || !account.Editable {
		return false
	}
	if _, recovered := accountQuotaRecoveredAfterDisable(account, record, now); recovered {
		return true
	}
	if record.DisableReason == "quota_exhausted" && !record.DisabledRecoverAfter.IsZero() && !record.DisabledRecoverAfter.After(now) &&
		!inspectionResultIsStrongFailure(record.Result) {
		return true
	}
	if record.DisableReason == "passive_circuit_open" && !record.DisabledRecoverAfter.IsZero() && !record.DisabledRecoverAfter.After(now) &&
		!inspectionResultIsStrongFailure(record.Result) {
		return true
	}
	if record.Result.Health == InspectionHealthHealthy && record.Result.HealthyStreak >= policy.RecoveryThreshold &&
		inspectionRecoveryEvidenceAfter(record, record.DisabledAt) {
		return true
	}
	return account.LastRefresh != nil && account.LastRefresh.After(record.DisabledAt) && !inspectionResultIsStrongFailure(record.Result)
}

func inspectionAutoEnableReason(account Account, record inspectionRecord, now time.Time) string {
	if _, recovered := accountQuotaRecoveredAfterDisable(account, record, now); recovered {
		return "quota_reset"
	}
	if record.DisableReason == "quota_exhausted" && !record.DisabledRecoverAfter.IsZero() && !record.DisabledRecoverAfter.After(now) {
		return "quota_reset"
	}
	if record.DisableReason == "passive_circuit_open" && !record.DisabledRecoverAfter.IsZero() && !record.DisabledRecoverAfter.After(now) {
		return "passive_circuit_recovered"
	}
	if record.Result.Health == InspectionHealthHealthy && inspectionRecoveryEvidenceAfter(record, record.DisabledAt) {
		return "health_recovered"
	}
	if account.LastRefresh != nil && account.LastRefresh.After(record.DisabledAt) {
		return "credential_refreshed"
	}
	return "health_recovered"
}

func quotaRecoveryPriority(policy InspectionPolicy, account Account, record inspectionRecord, now time.Time) *int {
	if !policy.QuotaRecoveryPriorityEnabled {
		return nil
	}
	if _, recovered := accountQuotaRecoveredAfterDisable(account, record, now); !recovered {
		return nil
	}
	if account.Priority != nil && *account.Priority >= quotaRecoveryPriorityFloor {
		return nil
	}
	priority := quotaRecoveryPriorityFloor
	return &priority
}

func inspectionRecoveryEvidenceAfter(record inspectionRecord, disabledAt time.Time) bool {
	if !record.Signal.LastSuccessAt.IsZero() && record.Signal.LastSuccessAt.After(disabledAt) {
		return true
	}
	switch record.Probe.ReasonCode {
	case "model_response_ok", "credential_response_ok":
		return record.Probe.TestedAt.After(disabledAt)
	default:
		return false
	}
}

func markInspectionDeleteCandidate(policy InspectionPolicy, record *inspectionRecord, now time.Time) bool {
	if record == nil || !policy.AutoDelete || !record.Result.OwnedDisable || !record.Result.Disabled || !record.Result.Editable {
		return false
	}
	if record.DisabledAt.IsZero() || !inspectionDeleteReasonAllowed(policy, *record) {
		return false
	}
	eligibleAt := record.DisabledAt.Add(time.Duration(policy.DeleteGraceHours) * time.Hour).UTC()
	record.Result.DeleteEligibleAt = timePointer(eligibleAt)
	return !eligibleAt.After(now)
}

func inspectionDeleteReasonAllowed(policy InspectionPolicy, record inspectionRecord) bool {
	if record.Result.ReasonCode != record.DisableReason || record.Result.Confidence != InspectionConfidenceHigh {
		return false
	}
	if record.Result.Health == InspectionHealthDeactivated {
		return record.DisableReason == "account_deactivated" || record.DisableReason == "workspace_deactivated"
	}
	if !policy.AutoDeleteInvalidCredentials || record.Result.Health != InspectionHealthInvalidCredentials ||
		record.Result.FailureStreak < policy.FailureThreshold {
		return false
	}
	// A model probe that receives a real HTTP 401 is also strong credential
	// evidence. Restricting deletion to the dedicated credential probe left
	// expired/invalid accounts stuck in review even though the active probe had
	// already confirmed authentication failure. Keep the confirmation helper as
	// the guard so model-unavailable, 403, and transient probes remain
	// conservative.
	if record.Result.SignalSource == InspectionSignalActiveProbe &&
		!inspectionProbeConfirmsCredentialFailure(record.Result.ProbeKind, record.Result.ReasonCode, record.Result.StatusCode) {
		return false
	}
	switch record.DisableReason {
	case "invalid_credentials", "token_revoked", "authentication_failed":
		return true
	default:
		return false
	}
}

func (e *InspectionEngine) setInspectionDisabled(ctx context.Context, account Account, record inspectionRecord, disabled bool, priority *int, writer ManagementWriter) (inspectionMutationResult, error) {
	if e == nil || e.host == nil {
		return inspectionMutationResult{}, fmt.Errorf("auth host is unavailable")
	}
	if !account.Editable || account.ID == "" || account.path == "" || !safeAuthJSONName(account.Name) {
		return inspectionMutationResult{}, fmt.Errorf("account is not safely editable")
	}
	if !disabled {
		if !record.Result.OwnedDisable || record.DisabledName != account.Name || normalizedPath(record.DisabledPath) != account.path {
			return inspectionMutationResult{}, fmt.Errorf("inspection disable ownership changed")
		}
	}
	detail, errGet := e.host.GetAuth(ctx, account.ID)
	if errGet != nil {
		return inspectionMutationResult{}, fmt.Errorf("get auth file: %w", errGet)
	}
	if returned := strings.TrimSpace(detail.AuthIndex); returned != "" && returned != account.ID {
		return inspectionMutationResult{}, fmt.Errorf("auth index changed")
	}
	name := strings.TrimSpace(firstNonEmpty(detail.Name, account.Name))
	path := normalizedPath(firstNonEmpty(detail.Path, account.path))
	if name != account.Name || !safeAuthJSONName(name) || path != account.path {
		return inspectionMutationResult{}, fmt.Errorf("auth source changed")
	}
	raw := bytes.TrimSpace(detail.JSON)
	if len(raw) == 0 || !json.Valid(raw) {
		return inspectionMutationResult{}, fmt.Errorf("auth json is invalid")
	}
	var document map[string]json.RawMessage
	if errDecode := json.Unmarshal(raw, &document); errDecode != nil || document == nil {
		return inspectionMutationResult{}, fmt.Errorf("auth json must be an object")
	}
	current := false
	if rawDisabled, exists := document["disabled"]; exists {
		if errDisabled := json.Unmarshal(rawDisabled, &current); errDisabled != nil {
			return inspectionMutationResult{}, fmt.Errorf("disabled field is invalid")
		}
	}
	disabledChanged := current != disabled || account.Disabled != disabled
	priorityChanged := false
	if priority != nil {
		currentPriority := 0
		priorityPresent := false
		if rawPriority, exists := document["priority"]; exists {
			var decoded any
			if errPriority := json.Unmarshal(rawPriority, &decoded); errPriority != nil {
				return inspectionMutationResult{}, fmt.Errorf("priority field is invalid")
			}
			var ok bool
			currentPriority, ok = intValue(decoded)
			if !ok {
				return inspectionMutationResult{}, fmt.Errorf("priority field is invalid")
			}
			priorityPresent = true
		}
		priorityChanged = !priorityPresent || currentPriority != *priority
	}
	if !disabledChanged && !priorityChanged {
		return inspectionMutationResult{Name: name, Path: path, Revision: revisionFor(raw)}, nil
	}
	if disabledChanged {
		encodedDisabled, _ := json.Marshal(disabled)
		document["disabled"] = encodedDisabled
	}
	if priorityChanged {
		encodedPriority, _ := json.Marshal(*priority)
		document["priority"] = encodedPriority
	}
	updated, errMarshal := json.Marshal(document)
	if errMarshal != nil {
		return inspectionMutationResult{}, fmt.Errorf("encode auth update: %w", errMarshal)
	}
	if writer != nil {
		if priorityChanged {
			if errPatch := writer.PatchFields(ctx, name, BatchPatch{Priority: priority}); errPatch != nil {
				return inspectionMutationResult{}, fmt.Errorf("update CPA account priority: %w", errPatch)
			}
		}
		if disabledChanged {
			if errPatch := writer.PatchDisabled(ctx, name, disabled); errPatch != nil {
				return inspectionMutationResult{}, fmt.Errorf("update CPA account status: %w", errPatch)
			}
		}
	} else if _, errSave := e.host.SaveAuth(ctx, name, updated); errSave != nil {
		return inspectionMutationResult{}, fmt.Errorf("save auth file: %w", errSave)
	}
	return inspectionMutationResult{
		Changed:  true,
		Name:     name,
		Path:     path,
		Revision: revisionFor(updated),
	}, nil
}

// inspectionMutationFailureReason converts a private mutation error into a
// bounded diagnostic code. The raw error can contain host paths or upstream
// details and must never be persisted in the operation journal.
func inspectionMutationFailureReason(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.HasPrefix(message, "auth host is unavailable"):
		return OperationFailureInspectionAuthHost
	case strings.HasPrefix(message, "account is not safely editable"):
		return OperationFailureInspectionAccountReadOnly
	case strings.HasPrefix(message, "inspection disable ownership changed"):
		return OperationFailureInspectionOwnership
	case strings.HasPrefix(message, "get auth file:"):
		return OperationFailureInspectionAuthRead
	case strings.HasPrefix(message, "auth index changed"):
		return OperationFailureInspectionAuthIdentity
	case strings.HasPrefix(message, "auth source changed"):
		return OperationFailureInspectionAuthSource
	case strings.HasPrefix(message, "auth json is invalid"), strings.HasPrefix(message, "auth json must be an object"):
		return OperationFailureInspectionAuthJSON
	case strings.HasPrefix(message, "disabled field is invalid"), strings.HasPrefix(message, "priority field is invalid"):
		return OperationFailureInspectionAuthField
	case strings.HasPrefix(message, "encode auth update:"), strings.HasPrefix(message, "update cpa account priority:"), strings.HasPrefix(message, "update cpa account status:"):
		return OperationFailureInspectionAuthUpdate
	case strings.HasPrefix(message, "save auth file:"):
		return OperationFailureInspectionAuthSave
	default:
		return OperationFailureInspectionMutation
	}
}

func inspectionMutationFailureRetryable(reason string) bool {
	switch safeOperationFailureReason(reason) {
	case OperationFailureInspectionAuthHost, OperationFailureInspectionAuthRead,
		OperationFailureInspectionAuthUpdate, OperationFailureInspectionAuthSave,
		OperationFailureInspectionMutation:
		return true
	default:
		return false
	}
}

func (e *InspectionEngine) ExecuteManualDeletes(
	ctx context.Context,
	deletions *AccountDeleteService,
	managementBaseURL string,
	managementKey string,
	request InspectionManualDeleteRequest,
) (InspectionDeleteRun, error) {
	run := InspectionDeleteRun{}
	if !request.Confirm {
		return run, ErrInspectionDeleteConfirmation
	}
	if e == nil || deletions == nil || strings.TrimSpace(managementKey) == "" {
		return run, fmt.Errorf("inspection deletion service is unavailable")
	}
	seen := make(map[string]struct{}, len(request.AccountIDs))
	ids := make([]string, 0, len(request.AccountIDs))
	for _, rawID := range request.AccountIDs {
		id := strings.TrimSpace(rawID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return run, ErrInspectionDeleteIDsRequired
	}
	if len(ids) > maxManualInspectionDeletes {
		return run, ErrInspectionDeleteTooMany
	}
	sortInspectionIDs(ids)
	now := e.currentTime()
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		run.Attempted++
		e.mu.RLock()
		record, exists := e.records[id]
		allowed := exists && inspectionManualDeleteAllowed(record.Result)
		e.mu.RUnlock()
		if !allowed {
			run.Skipped++
			run.Results = append(run.Results, InspectionDeleteResult{AccountID: id, Status: InspectionActionSkipped, Reason: "delete_not_recommended"})
			e.markManualDeleteAttempt(id, InspectionActionSkipped, "delete_not_recommended", now)
			continue
		}
		preview, errPreview := deletions.Preview(ctx, AccountDeletePreviewRequest{ID: id})
		if errPreview != nil {
			reason := inspectionDeleteFailureReason(errPreview)
			status := InspectionActionFailed
			if reason == "account_missing" || reason == "account_read_only" {
				status = InspectionActionSkipped
				run.Skipped++
			} else {
				run.Failed++
			}
			run.Results = append(run.Results, InspectionDeleteResult{AccountID: id, Status: status, Reason: reason})
			e.markManualDeleteAttempt(id, status, reason, now)
			continue
		}
		_, errDelete := deletions.Start(ctx, preview.ID, managementBaseURL, managementKey)
		if errDelete != nil {
			reason := inspectionDeleteFailureReason(errDelete)
			status := InspectionActionFailed
			if errors.Is(errDelete, ErrAccountDeleteBusy) {
				status = InspectionActionSkipped
				run.Skipped++
			} else {
				run.Failed++
			}
			run.Results = append(run.Results, InspectionDeleteResult{AccountID: id, Status: status, Reason: reason})
			e.markManualDeleteAttempt(id, status, reason, now)
			continue
		}
		run.Succeeded++
		run.Results = append(run.Results, InspectionDeleteResult{AccountID: id, Status: InspectionActionSucceeded})
		e.markManualDeleteAttempt(id, InspectionActionSucceeded, "", now)
	}
	managementKey = ""
	e.persist()
	return run, nil
}

func inspectionManualDeleteAllowed(result InspectionResult) bool {
	if !result.Editable || normalizeInspectionConfidence(result.Confidence) != InspectionConfidenceHigh {
		return false
	}
	health := normalizeInspectionHealth(result.Health)
	recommendation := normalizeInspectionRecommendation(result.Recommendation)
	reason := safeOptionalInspectionReason(result.ReasonCode)
	if recommendation == InspectionRecommendationDelete && health == InspectionHealthDeactivated {
		return reason == "account_deactivated" || reason == "workspace_deactivated"
	}
	if recommendation != InspectionRecommendationReauth || health != InspectionHealthInvalidCredentials ||
		(result.SignalSource == InspectionSignalActiveProbe &&
			!inspectionProbeConfirmsCredentialFailure(result.ProbeKind, result.ReasonCode, result.StatusCode)) {
		return false
	}
	return reason == "invalid_credentials" || reason == "token_revoked" || reason == "authentication_failed"
}

func (e *InspectionEngine) markManualDeleteAttempt(id, status, reason string, now time.Time) {
	e.mu.Lock()
	record, exists := e.records[id]
	if !exists {
		e.mu.Unlock()
		return
	}
	action := newInspectionAction(record.Result, InspectionActionDelete, record.Result.ReasonCode, now)
	action.Source = OperationSourceManual
	action.Status = status
	if reason != "" {
		action.ReasonCode = safeInspectionDeleteFailureReason(reason)
	}
	e.appendActionLocked(action)
	if status == InspectionActionSucceeded {
		delete(e.records, id)
	} else {
		record.Result.AutoAction = InspectionActionDelete
		record.Result.AutoActionStatus = status
		e.records[id] = record
	}
	e.dirty = true
	e.generation++
	e.mu.Unlock()
}

func (e *InspectionEngine) ExecutePendingDeletes(
	ctx context.Context,
	deletions *AccountDeleteService,
	managementBaseURL string,
	managementKey string,
) InspectionDeleteRun {
	run := InspectionDeleteRun{}
	if e == nil || deletions == nil || strings.TrimSpace(managementKey) == "" {
		return run
	}
	now := e.currentTime()
	e.mu.RLock()
	policy := e.policy
	candidates := make([]string, 0)
	if policy.AutoDelete {
		for id, record := range e.records {
			if !record.Result.OwnedDisable || record.Result.AutoAction != InspectionActionDeleteCandidate ||
				(record.Result.AutoActionStatus != InspectionActionPending && record.Result.AutoActionStatus != InspectionActionFailed) ||
				!record.Result.Disabled || !record.Result.Editable || !inspectionDeleteReasonAllowed(policy, record) ||
				record.Result.DeleteEligibleAt == nil || record.Result.DeleteEligibleAt.After(now) ||
				(!record.DeleteRetryAfter.IsZero() && record.DeleteRetryAfter.After(now)) {
				continue
			}
			candidates = append(candidates, id)
		}
	}
	e.mu.RUnlock()
	sortInspectionIDs(candidates)
	if len(candidates) > policy.DeleteBatchSize {
		candidates = candidates[:policy.DeleteBatchSize]
	}

	for _, id := range candidates {
		if ctx.Err() != nil {
			break
		}
		run.Attempted++
		preview, errPreview := deletions.Preview(ctx, AccountDeletePreviewRequest{ID: id})
		if errPreview != nil {
			reason := inspectionDeleteFailureReason(errPreview)
			if reason == "account_missing" || reason == "account_read_only" {
				run.Skipped++
				run.Results = append(run.Results, InspectionDeleteResult{AccountID: id, Status: InspectionActionSkipped, Reason: reason})
				e.revokePendingDelete(id, reason, now)
			} else {
				run.Failed++
				run.Results = append(run.Results, InspectionDeleteResult{AccountID: id, Status: InspectionActionFailed, Reason: reason})
				e.markDeleteAttempt(id, InspectionActionFailed, reason, now)
			}
			continue
		}
		validation := e.revalidatePendingDelete(ctx, id, now)
		if !validation.Eligible {
			if validation.Retry {
				run.Failed++
				run.Results = append(run.Results, InspectionDeleteResult{AccountID: id, Status: InspectionActionFailed, Reason: validation.Reason})
				e.markDeleteAttempt(id, InspectionActionFailed, validation.Reason, now)
			} else {
				run.Skipped++
				run.Results = append(run.Results, InspectionDeleteResult{AccountID: id, Status: InspectionActionSkipped, Reason: validation.Reason})
			}
			continue
		}
		_, errDelete := deletions.Start(ctx, preview.ID, managementBaseURL, managementKey)
		if errDelete != nil {
			reason := inspectionDeleteFailureReason(errDelete)
			if errors.Is(errDelete, ErrAccountDeleteBusy) {
				run.Skipped++
				run.Results = append(run.Results, InspectionDeleteResult{AccountID: id, Status: InspectionActionSkipped, Reason: reason})
				e.markDeleteAttempt(id, InspectionActionSkipped, reason, now)
			} else {
				run.Failed++
				run.Results = append(run.Results, InspectionDeleteResult{AccountID: id, Status: InspectionActionFailed, Reason: reason})
				e.markDeleteAttempt(id, InspectionActionFailed, reason, now)
			}
			continue
		}
		run.Succeeded++
		run.Results = append(run.Results, InspectionDeleteResult{AccountID: id, Status: InspectionActionSucceeded})
		e.markDeleteAttempt(id, InspectionActionSucceeded, "", now)
	}
	managementKey = ""
	e.persist()
	return run
}

func (e *InspectionEngine) revalidatePendingDelete(ctx context.Context, id string, now time.Time) inspectionDeleteValidation {
	if e == nil || e.accounts == nil {
		return inspectionDeleteValidation{Retry: true, Reason: "delete_failed"}
	}
	resolved, errResolve := e.accounts.ResolveTargets(ctx, TargetScope{Mode: "selected", IDs: []string{id}})
	if errResolve != nil {
		return inspectionDeleteValidation{Retry: true, Reason: "delete_failed"}
	}
	if len(resolved.MissingIDs) != 0 || len(resolved.Accounts) != 1 {
		e.revokePendingDelete(id, "account_missing", now)
		return inspectionDeleteValidation{Reason: "account_missing"}
	}
	account := resolved.Accounts[0]
	physicalDisabled, errPhysical := e.inspectionPhysicalDisabled(ctx, account)
	if errPhysical != nil {
		return inspectionDeleteValidation{Retry: true, Reason: "delete_failed"}
	}

	e.mu.Lock()
	record, exists := e.records[id]
	if !exists {
		e.mu.Unlock()
		return inspectionDeleteValidation{Reason: "account_missing"}
	}
	decision := decideInspection(account, record, now)
	updateInspectionRecord(&record, account, decision, now)
	sameSource := account.Editable && account.Name != "" && account.Name == record.DisabledName &&
		account.path != "" && account.path == normalizedPath(record.DisabledPath)
	eligible := e.policy.AutoDelete && record.Result.OwnedDisable && account.Disabled && physicalDisabled && sameSource &&
		inspectionDeleteReasonAllowed(e.policy, record) && record.Result.DeleteEligibleAt != nil && !record.Result.DeleteEligibleAt.After(now)
	if eligible {
		e.records[id] = record
		e.dirty = true
		e.generation++
		e.mu.Unlock()
		return inspectionDeleteValidation{Eligible: true}
	}

	reason := "account_changed"
	if !account.Editable || account.path == "" {
		reason = "account_read_only"
	}
	if record.Result.OwnedDisable && (!account.Disabled || !physicalDisabled) {
		clearInspectionDisableOwnership(&record)
		record.Result.AutoAction = ""
		record.Result.AutoActionStatus = ""
	} else {
		record.Result.AutoAction = ""
		record.Result.AutoActionStatus = ""
		record.Result.DeleteEligibleAt = nil
		record.DeleteRetryAfter = time.Time{}
	}
	e.records[id] = record
	action := newInspectionAction(record.Result, InspectionActionDelete, reason, now)
	action.Status = InspectionActionSkipped
	action.ReasonCode = safeInspectionDeleteFailureReason(reason)
	e.appendActionLocked(action)
	e.dirty = true
	e.generation++
	e.mu.Unlock()
	return inspectionDeleteValidation{Reason: reason}
}

func (e *InspectionEngine) inspectionPhysicalDisabled(ctx context.Context, account Account) (bool, error) {
	if e == nil || e.host == nil || account.ID == "" || account.Name == "" || account.path == "" {
		return false, fmt.Errorf("physical auth source is unavailable")
	}
	detail, errGet := e.host.GetAuth(ctx, account.ID)
	if errGet != nil {
		return false, fmt.Errorf("get physical auth file: %w", errGet)
	}
	if returned := strings.TrimSpace(detail.AuthIndex); returned != "" && returned != account.ID {
		return false, nil
	}
	name := strings.TrimSpace(firstNonEmpty(detail.Name, account.Name))
	path := normalizedPath(firstNonEmpty(detail.Path, account.path))
	if name != account.Name || !safeAuthJSONName(name) || path != account.path {
		return false, nil
	}
	raw := bytes.TrimSpace(detail.JSON)
	if len(raw) == 0 || !json.Valid(raw) {
		return false, fmt.Errorf("physical auth json is invalid")
	}
	var document map[string]json.RawMessage
	if errDecode := json.Unmarshal(raw, &document); errDecode != nil || document == nil {
		return false, fmt.Errorf("physical auth json must be an object")
	}
	rawDisabled, exists := document["disabled"]
	if !exists {
		return false, nil
	}
	var disabled bool
	if errDisabled := json.Unmarshal(rawDisabled, &disabled); errDisabled != nil {
		return false, fmt.Errorf("physical disabled field is invalid")
	}
	return disabled, nil
}

func (e *InspectionEngine) revokePendingDelete(id, reason string, now time.Time) {
	e.mu.Lock()
	record, exists := e.records[id]
	if !exists {
		e.mu.Unlock()
		return
	}
	record.Result.AutoAction = ""
	record.Result.AutoActionStatus = ""
	record.Result.DeleteEligibleAt = nil
	record.DeleteRetryAfter = time.Time{}
	e.records[id] = record
	action := newInspectionAction(record.Result, InspectionActionDelete, reason, now)
	action.Status = InspectionActionSkipped
	action.ReasonCode = safeInspectionDeleteFailureReason(reason)
	e.appendActionLocked(action)
	e.dirty = true
	e.generation++
	e.mu.Unlock()
}

func (e *InspectionEngine) markDeleteAttempt(id, status, reason string, now time.Time) {
	e.mu.Lock()
	record, exists := e.records[id]
	if !exists {
		e.mu.Unlock()
		return
	}
	disableReason := record.DisableReason
	if status == InspectionActionSucceeded {
		clearInspectionDisableOwnership(&record)
		record.Result.AutoAction = InspectionActionDelete
		record.Result.AutoActionStatus = InspectionActionSucceeded
	} else {
		record.Result.AutoAction = InspectionActionDeleteCandidate
		record.Result.AutoActionStatus = InspectionActionFailed
		record.DeleteRetryAfter = now.Add(inspectionDeleteRetry).UTC()
	}
	e.records[id] = record
	action := newInspectionAction(record.Result, InspectionActionDelete, disableReason, now)
	action.Status = status
	if reason != "" {
		action.ReasonCode = safeInspectionDeleteFailureReason(reason)
	}
	e.appendActionLocked(action)
	e.dirty = true
	e.generation++
	e.mu.Unlock()
}

func clearInspectionDisableOwnership(record *inspectionRecord) {
	if record == nil {
		return
	}
	record.Result.OwnedDisable = false
	record.Result.DeleteEligibleAt = nil
	record.Result.CircuitOpen = false
	record.Result.CircuitReasonCode = ""
	record.Result.RecoverAfter = nil
	clearAutomaticDisableProbeState(&record.Result)
	record.DisableReason = ""
	record.DisabledAt = time.Time{}
	record.DisabledName = ""
	record.DisabledPath = ""
	record.DisabledVersion = ""
	record.DisabledRecoverAfter = time.Time{}
	record.DeleteRetryAfter = time.Time{}
}

func newInspectionAction(result InspectionResult, action, reason string, now time.Time) InspectionAction {
	id, errID := randomIdentifier()
	if errID != nil {
		id = fmt.Sprintf("inspection-%d", now.UnixNano())
	}
	return InspectionAction{
		ID:         id,
		AccountID:  result.ID,
		Name:       result.Name,
		Provider:   result.Provider,
		Action:     action,
		Status:     InspectionActionPending,
		Source:     OperationSourceInspection,
		ReasonCode: safeInspectionReason(reason),
		CreatedAt:  now.UTC(),
	}
}

func (e *InspectionEngine) appendActionLocked(action InspectionAction) {
	e.actions = append(e.actions, action)
	if len(e.actions) > maxInspectionActions {
		e.actions = append([]InspectionAction(nil), e.actions[len(e.actions)-maxInspectionActions:]...)
	}
}

func sortInspectionIDs(ids []string) {
	sort.Strings(ids)
}

func inspectionDeleteFailureReason(err error) string {
	switch {
	case errors.Is(err, ErrAccountDeleteBusy):
		return "mutation_busy"
	case errors.Is(err, ErrAccountDeletePreviewStale):
		return "account_changed"
	case errors.Is(err, ErrAccountDeleteTargetNotFound):
		return "account_missing"
	case errors.Is(err, ErrAccountDeleteTargetReadOnly):
		return "account_read_only"
	case errors.Is(err, ErrManagementBaseURLInvalid):
		return "management_unavailable"
	default:
		return "delete_failed"
	}
}

func safeInspectionDeleteFailureReason(value string) string {
	switch value {
	case "mutation_busy", "account_changed", "account_missing", "account_read_only", "management_unavailable", "delete_failed":
		return value
	default:
		return "delete_failed"
	}
}

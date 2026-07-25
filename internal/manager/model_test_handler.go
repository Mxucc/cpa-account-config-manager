package manager

import (
	"context"
	"errors"
	"net/http"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func (a *App) handleAccountModelTest(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request ModelTestRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	if request.ExperimentalWeeklyOverdraft && !a.experiments.WeeklyOverdraftEnabled() {
		return jsonResponse(http.StatusConflict, map[string]any{"error": "weekly overdraft experiment is not enabled"})
	}
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	config := a.configSnapshot()
	request.DetectRestrictedModels = a.experiments.AutoModelWhitelistEnabled()
	result, errTest := a.modelTests.Run(ctx, request, config.ManagementBaseURL, managementKey, req.HostCallbackID)
	if errTest != nil {
		managementKey = ""
		switch {
		case errors.Is(errTest, ErrModelTestAccountNotFound):
			return jsonResponse(http.StatusNotFound, map[string]any{"error": ErrModelTestAccountNotFound.Error()})
		case errors.Is(errTest, ErrModelTestBusy):
			return jsonResponse(http.StatusTooManyRequests, map[string]any{"error": ErrModelTestBusy.Error()})
		case errors.Is(errTest, ErrManagementBaseURLInvalid):
			return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": ErrManagementBaseURLInvalid.Error()})
		default:
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": errTest.Error()})
		}
	}
	if request.DetectRestrictedModels && len(result.CompatibleModels) > 0 {
		result.ModelPolicy = a.applyDetectedModelWhitelist(ctx, result.AccountID, result.CompatibleModels, config, managementKey, OperationSourceManual)
	}
	managementKey = ""
	a.recordModelTest(result, OperationSourceManual)
	_ = a.inspection.RecordManualModelTest(ctx, result)
	return jsonResponse(http.StatusOK, result)
}

func (a *App) recordModelTest(result ModelTestResult, requestedSource ...string) {
	source := OperationSourceManual
	if len(requestedSource) > 0 && normalizeOperationSource(requestedSource[0]) != "" {
		source = normalizeOperationSource(requestedSource[0])
	}
	status := OperationStatusWarning
	succeeded := 0
	failed := 0
	skipped := 0
	switch result.Status {
	case "available":
		status = OperationStatusSucceeded
		succeeded = 1
	case "unavailable":
		status = OperationStatusFailed
		failed = 1
	case "unsupported":
		status = OperationStatusSkipped
		skipped = 1
	}
	finishedAt := result.TestedAt.Add(time.Duration(result.LatencyMS) * time.Millisecond)
	a.operations.Record(OperationEntry{
		Category: OperationCategoryAccount, Action: OperationActionModelTest, Status: status,
		Source: source, Scope: OperationScopeSingle, TargetID: result.AccountID, TargetCount: 1,
		Succeeded: succeeded, Failed: failed, Skipped: skipped, StartedAt: result.TestedAt, FinishedAt: finishedAt,
		ReasonCode: result.ReasonCode, Model: result.Model,
	})
}

func (a *App) runNewAccountModelProbe(ctx context.Context, account Account, managementKey string, hostCallbackID string) (ModelTestResult, error) {
	result, errRun := a.modelTests.Run(ctx, ModelTestRequest{
		AccountID: account.ID, Model: defaultOpenAIProbeModel, DetectRestrictedModels: true, SelectPolicyFallback: true,
	}, a.configSnapshot().ManagementBaseURL, managementKey, hostCallbackID)
	if errRun != nil {
		return result, errRun
	}
	if a.experiments.AutoModelWhitelistEnabled() && len(result.CompatibleModels) > 0 {
		result.ModelPolicy = a.applyDetectedModelWhitelist(ctx, result.AccountID, result.CompatibleModels, a.configSnapshot(), managementKey, OperationSourceBackground)
	}
	a.recordModelTest(result, OperationSourceBackground)
	_ = a.inspection.RecordModelTest(ctx, result, InspectionProbeSourceScan)
	return result, nil
}

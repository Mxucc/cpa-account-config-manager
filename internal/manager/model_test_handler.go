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
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	config := a.configSnapshot()
	result, errTest := a.modelTests.Run(ctx, request, config.ManagementBaseURL, managementKey)
	managementKey = ""
	if errTest != nil {
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
	a.recordModelTest(result)
	return jsonResponse(http.StatusOK, result)
}

func (a *App) recordModelTest(result ModelTestResult) {
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
		Source: OperationSourceManual, Scope: OperationScopeSingle, TargetID: result.AccountID, TargetCount: 1,
		Succeeded: succeeded, Failed: failed, Skipped: skipped, StartedAt: result.TestedAt, FinishedAt: finishedAt,
		ReasonCode: result.ReasonCode, Model: result.Model,
	})
}

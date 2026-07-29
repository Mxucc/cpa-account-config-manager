package manager

import (
	"context"
	"errors"
	"net/http"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func (a *App) handleAccountTokenRefresh(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	startedAt := time.Now().UTC()
	var request AccountTokenRefreshRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	result, errRefresh := a.tokenRefresh.Refresh(ctx, request)
	a.recordTokenRefreshOperation(request.AccountID, result, errRefresh, startedAt)
	if errRefresh != nil {
		switch {
		case errors.Is(errRefresh, ErrAccountTokenRefreshNotFound):
			return jsonResponse(http.StatusNotFound, map[string]any{"error": ErrAccountTokenRefreshNotFound.Error()})
		case errors.Is(errRefresh, ErrAccountTokenRefreshReadOnly):
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": ErrAccountTokenRefreshReadOnly.Error()})
		case errors.Is(errRefresh, ErrAccountTokenRefreshBusy):
			return jsonResponse(http.StatusConflict, map[string]any{"error": ErrAccountTokenRefreshBusy.Error()})
		case errors.Is(errRefresh, ErrAccountTokenRefreshUnsupported):
			return jsonResponse(http.StatusNotImplemented, map[string]any{"error": ErrAccountTokenRefreshUnsupported.Error()})
		case errors.Is(errRefresh, ErrAccountTokenRefreshProviderUnsupported):
			return jsonResponse(http.StatusUnprocessableEntity, map[string]any{"error": ErrAccountTokenRefreshProviderUnsupported.Error()})
		case errors.Is(errRefresh, ErrAccountTokenRefreshCredentialMissing):
			return jsonResponse(http.StatusUnprocessableEntity, map[string]any{"error": ErrAccountTokenRefreshCredentialMissing.Error()})
		case errors.Is(errRefresh, ErrAccountTokenRefreshRejected):
			return jsonResponse(http.StatusUnprocessableEntity, map[string]any{"error": ErrAccountTokenRefreshRejected.Error()})
		case errors.Is(errRefresh, ErrAccountTokenRefreshConflict):
			return jsonResponse(http.StatusConflict, map[string]any{"error": ErrAccountTokenRefreshConflict.Error()})
		case errors.Is(errRefresh, ErrAccountTokenRefreshVerification):
			return jsonResponse(http.StatusConflict, map[string]any{"error": ErrAccountTokenRefreshVerification.Error()})
		case errors.Is(errRefresh, ErrAccountTokenRefreshFailed):
			return jsonResponse(http.StatusBadGateway, map[string]any{"error": ErrAccountTokenRefreshFailed.Error()})
		default:
			return jsonResponse(http.StatusInternalServerError, map[string]any{"error": "failed to refresh account credential"})
		}
	}
	return jsonResponse(http.StatusOK, result)
}

func (a *App) recordTokenRefreshOperation(accountID string, result AccountTokenRefreshResult, operationError error, startedAt time.Time) {
	status := OperationStatusSucceeded
	succeeded := 1
	failed := 0
	reason := "token_refreshed_plugin"
	if result.RefreshSource == "cpa_native" {
		reason = "token_refreshed_native"
	}
	if operationError != nil {
		status = OperationStatusFailed
		succeeded = 0
		failed = 1
		switch {
		case errors.Is(operationError, ErrAccountTokenRefreshUnsupported):
			reason = "host_refresh_unsupported"
		case errors.Is(operationError, ErrAccountTokenRefreshCredentialMissing):
			reason = "refresh_credential_missing"
		case errors.Is(operationError, ErrAccountTokenRefreshProviderUnsupported):
			reason = "refresh_provider_unsupported"
		case errors.Is(operationError, ErrAccountTokenRefreshRejected):
			reason = "refresh_rejected"
		case errors.Is(operationError, ErrAccountTokenRefreshConflict):
			reason = "refresh_conflict"
		case errors.Is(operationError, ErrAccountTokenRefreshVerification):
			reason = "refresh_verification_failed"
		case errors.Is(operationError, ErrAccountTokenRefreshBusy):
			reason = "refresh_already_running"
		default:
			reason = "operation_failed"
		}
	}
	targetID := result.AccountID
	if targetID == "" {
		targetID = accountID
	}
	a.operations.Record(OperationEntry{
		Category: OperationCategoryAccount, Action: OperationActionTokenRefresh, Status: status,
		Source: OperationSourceManual, Scope: OperationScopeSingle, TargetID: safeOperationIdentifier(targetID, 256),
		TargetCount: 1, Succeeded: succeeded, Failed: failed, StartedAt: startedAt,
		FinishedAt: time.Now().UTC(), ReasonCode: reason,
	})
}

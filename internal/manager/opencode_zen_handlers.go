package manager

import (
	"context"
	"net/http"
	"strings"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

type openCodeZenAccountSaveRequest struct {
	AccountID      string `json:"account_id,omitempty"`
	Name           string `json:"name,omitempty"`
	BaseURL        string `json:"base_url"`
	ZenAPIKey      string `json:"zen_api_key,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type openCodeZenProbeRequest struct {
	BaseURL        string `json:"base_url"`
	ZenAPIKey      string `json:"zen_api_key,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type openCodeZenAccountsResponse struct {
	Accounts     []OpenCodeZenAccountView `json:"accounts"`
	StorageError string                   `json:"storage_error,omitempty"`
}

type openCodeZenProbeResponse struct {
	Result OpenCodeZenProbeResult `json:"result"`
}

func openCodeZenTimeout(timeoutSeconds int) time.Duration {
	timeout := time.Duration(openCodeZenDefaultTimeout) * time.Second
	if timeoutSeconds >= 1 && timeoutSeconds <= openCodeZenMaxTimeoutSeconds {
		timeout = time.Duration(timeoutSeconds) * time.Second
	}
	return timeout
}

func savedOpenCodeZenView(service *OpenCodeZenService, id string) OpenCodeZenAccountView {
	for _, account := range service.ListAccounts() {
		if account.ID == id {
			return account
		}
	}
	return OpenCodeZenAccountView{ID: id}
}

func (a *App) handleOpenCodeZenAccounts(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.opencodeZen == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "OpenCode Zen service is unavailable"})
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	switch method {
	case http.MethodGet:
		return jsonResponse(http.StatusOK, openCodeZenAccountsResponse{
			Accounts: a.opencodeZen.ListAccounts(), StorageError: a.opencodeZen.StorageError(),
		})
	case http.MethodPost, http.MethodDelete:
		if resolveManagementKey(req.Headers) == "" {
			return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
		}
	}
	switch method {
	case http.MethodPost:
		var request openCodeZenAccountSaveRequest
		if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid OpenCode Zen account request"})
		}
		startedAt := time.Now().UTC()
		accountID, errSave := a.opencodeZen.SaveAccount(request.AccountID, request.Name, request.BaseURL, request.ZenAPIKey)
		if errSave != nil {
			a.operations.Record(OperationEntry{
				Category: OperationCategoryOpenCode, Action: OperationActionOpenCodeSave,
				Status: OperationStatusFailed, Source: OperationSourceManual, Scope: OperationScopeSingle,
				TargetCount: 1, Failed: 1, StartedAt: startedAt, FinishedAt: time.Now().UTC(), ReasonCode: "invalid_credential",
			})
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": errSave.Error()})
		}
		view := savedOpenCodeZenView(a.opencodeZen, accountID)
		var result OpenCodeZenProbeResult
		if request.ZenAPIKey != "" || request.AccountID == "" {
			// Fresh credential: probe with the submitted key (mirrors the
			// opencode-go save flow which queries right after saving).
			result = a.opencodeZen.Probe(ctx, view.BaseURL, request.ZenAPIKey, openCodeZenTimeout(request.TimeoutSeconds))
		} else {
			// Update path without a new key: probe server-side with the stored one.
			view, result = a.opencodeZen.ProbeAccount(ctx, accountID, openCodeZenTimeout(request.TimeoutSeconds))
		}
		a.operations.Record(OperationEntry{
			Category: OperationCategoryOpenCode, Action: OperationActionOpenCodeSave,
			Status: OperationStatusSucceeded, Source: OperationSourceManual, Scope: OperationScopeSingle,
			TargetCount: 1, Succeeded: 1, StartedAt: startedAt, FinishedAt: time.Now().UTC(), ReasonCode: "account_saved",
		})
		return jsonResponse(http.StatusOK, map[string]any{
			"account": view,
			"result":  result,
		})
	case http.MethodDelete:
		accountID := strings.TrimSpace(firstQueryValue(req.Query, "account_id"))
		if accountID == "" {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": "account_id is required"})
		}
		startedAt := time.Now().UTC()
		if errRemove := a.opencodeZen.RemoveAccount(accountID); errRemove != nil {
			a.operations.Record(OperationEntry{
				Category: OperationCategoryOpenCode, Action: OperationActionOpenCodeRemove,
				Status: OperationStatusFailed, Source: OperationSourceManual, Scope: OperationScopeSingle,
				TargetCount: 1, Failed: 1, StartedAt: startedAt, FinishedAt: time.Now().UTC(), ReasonCode: "account_not_found",
			})
			return jsonResponse(http.StatusNotFound, map[string]any{"error": errRemove.Error()})
		}
		a.operations.Record(OperationEntry{
			Category: OperationCategoryOpenCode, Action: OperationActionOpenCodeRemove,
			Status: OperationStatusSucceeded, Source: OperationSourceManual, Scope: OperationScopeSingle,
			TargetCount: 1, Succeeded: 1, StartedAt: startedAt, FinishedAt: time.Now().UTC(), ReasonCode: "account_removed",
		})
		return jsonResponse(http.StatusOK, map[string]any{"removed": true})
	default:
		return jsonResponse(http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

// handleOpenCodeZenProbe tests a raw Zen/bridge endpoint without saving any
// credential. It exists so the add form can validate before persistence; the
// saved-account test endpoint below verifies the stored key server-side.
func (a *App) handleOpenCodeZenProbe(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.opencodeZen == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "OpenCode Zen service is unavailable"})
	}
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	var request openCodeZenProbeRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid OpenCode Zen probe request"})
	}
	if strings.TrimSpace(request.BaseURL) == "" || strings.TrimSpace(request.ZenAPIKey) == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "base_url and zen_api_key are both required"})
	}
	result := a.opencodeZen.Probe(ctx, request.BaseURL, request.ZenAPIKey, openCodeZenTimeout(request.TimeoutSeconds))
	status := http.StatusOK
	if !result.Reachable {
		status = http.StatusBadGateway
	}
	return jsonResponse(status, openCodeZenProbeResponse{Result: result})
}

// handleOpenCodeZenProbeAccount tests a saved account using its stored key so
// the UI can verify connectivity without returning the secret to the browser.
func (a *App) handleOpenCodeZenProbeAccount(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.opencodeZen == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "OpenCode Zen service is unavailable"})
	}
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	accountID := strings.TrimSpace(firstQueryValue(req.Query, "account_id"))
	if accountID == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "account_id is required"})
	}
	view, result := a.opencodeZen.ProbeAccount(ctx, accountID, openCodeZenTimeout(openCodeZenDefaultTimeout))
	if result.Detail == "OpenCode Zen account was not found" {
		return jsonResponse(http.StatusNotFound, map[string]any{"error": result.Detail})
	}
	status := http.StatusOK
	if !result.Reachable {
		status = http.StatusBadGateway
	}
	return jsonResponse(status, map[string]any{"account": view, "result": result})
}

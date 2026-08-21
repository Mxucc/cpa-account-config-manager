package manager

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

const opencodeStatusResourcePath = resourceRoutePrefix + "/opencode-status"

type openCodeAccountSaveRequest struct {
	WorkspaceID string `json:"workspace_id"`
	AuthCookie  string `json:"auth_cookie"`
}

type openCodeProbeRequest struct {
	WorkspaceID    string `json:"workspace_id"`
	AuthCookie     string `json:"auth_cookie"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type openCodeProbeResponse struct {
	Result OpenCodeQuotaResult `json:"result"`
}

type openCodeAccountsResponse struct {
	Accounts []OpenCodeAccountView `json:"accounts"`
}

func (a *App) handleOpenCodeAccounts(_ context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.opencode == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "OpenCode quota service is unavailable"})
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	switch method {
	case http.MethodGet:
		return jsonResponse(http.StatusOK, openCodeAccountsResponse{Accounts: a.opencode.ListAccounts()})
	case http.MethodPost, http.MethodDelete:
		if resolveManagementKey(req.Headers) == "" {
			return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
		}
	}
	switch method {
	case http.MethodPost:
		var request openCodeAccountSaveRequest
		if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid OpenCode account request"})
		}
		startedAt := time.Now().UTC()
		accountID, errSave := a.opencode.SaveAccount(request.WorkspaceID, request.AuthCookie)
		if errSave != nil {
			a.operations.Record(OperationEntry{
				Category: OperationCategoryOpenCode, Action: OperationActionOpenCodeSave,
				Status: OperationStatusFailed, Source: OperationSourceManual, Scope: OperationScopeSingle,
				TargetCount: 1, Failed: 1, StartedAt: startedAt, FinishedAt: time.Now().UTC(), ReasonCode: "invalid_credential",
			})
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": errSave.Error()})
		}
		result := a.opencode.RefreshAccount(accountID)
		a.operations.Record(OperationEntry{
			Category: OperationCategoryOpenCode, Action: OperationActionOpenCodeSave,
			Status: OperationStatusSucceeded, Source: OperationSourceManual, Scope: OperationScopeSingle,
			TargetCount: 1, Succeeded: 1, StartedAt: startedAt, FinishedAt: time.Now().UTC(), ReasonCode: "account_saved",
		})
		return jsonResponse(http.StatusOK, map[string]any{
			"account": OpenCodeAccountView{ID: accountID, WorkspaceID: request.WorkspaceID},
			"result":  result,
		})
	case http.MethodDelete:
		accountID := strings.TrimSpace(firstQueryValue(req.Query, "account_id"))
		if accountID == "" {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": "account_id is required"})
		}
		startedAt := time.Now().UTC()
		if errRemove := a.opencode.RemoveAccount(accountID); errRemove != nil {
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

func (a *App) handleOpenCodeQuota(_ context.Context, _ cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.opencode == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "OpenCode quota service is unavailable"})
	}
	return jsonResponse(http.StatusOK, a.opencode.Snapshot())
}

func (a *App) handleOpenCodeRefresh(_ context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.opencode == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "OpenCode quota service is unavailable"})
	}
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	force := strings.EqualFold(firstQueryValue(req.Query, "force"), "true") || strings.EqualFold(firstQueryValue(req.Query, "force"), "1")
	startedAt := time.Now().UTC()
	results := a.opencode.RefreshAll(force)
	status := http.StatusOK
	failed := 0
	for _, result := range results {
		if result != nil && !result.Success {
			failed++
		}
	}
	if failed > 0 {
		status = http.StatusBadGateway
	}
	if len(results) > 0 {
		a.operations.Record(OperationEntry{
			Category: OperationCategoryOpenCode, Action: OperationActionOpenCodeRefresh,
			Status: operationStatusForOpenCode(failed, len(results)), Source: OperationSourceManual, Scope: OperationScopeAll,
			TargetCount: len(results), Succeeded: len(results) - failed, Failed: failed,
			StartedAt: startedAt, FinishedAt: time.Now().UTC(), ReasonCode: reasonCodeForOpenCode(failed, len(results)),
		})
	}
	return jsonResponse(status, map[string]any{"results": results})
}

func (a *App) handleOpenCodeRefreshAccount(_ context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.opencode == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "OpenCode quota service is unavailable"})
	}
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	accountID := strings.TrimSpace(firstQueryValue(req.Query, "account_id"))
	result := a.opencode.RefreshAccount(accountID)
	if result == nil || !result.Success {
		status := http.StatusNotFound
		if result != nil && result.AccountID != "" {
			status = http.StatusBadGateway
		}
		return jsonResponse(status, map[string]any{"result": result})
	}
	return jsonResponse(http.StatusOK, map[string]any{"result": result})
}

func (a *App) handleOpenCodeProbe(_ context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.opencode == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "OpenCode quota service is unavailable"})
	}
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	var request openCodeProbeRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid OpenCode probe request"})
	}
	if strings.TrimSpace(request.WorkspaceID) == "" || strings.TrimSpace(request.AuthCookie) == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "workspace_id and auth_cookie are both required"})
	}
	result := a.opencode.Probe(request.WorkspaceID, request.AuthCookie, request.TimeoutSeconds)
	status := http.StatusOK
	if !result.Success {
		status = http.StatusBadGateway
	}
	return jsonResponse(status, openCodeProbeResponse{Result: result})
}

// handleOpenCodeStatusPage serves the standalone OpenCode Go status page. All
// form actions use GET so CPA renders the response as a full HTML page.
func (a *App) handleOpenCodeStatusPage(_ context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.opencode == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "OpenCode quota service is unavailable"})
	}
	var workspace, cookie, action, removeID string
	timeout := openCodeQuotaDefaultTimeout
	if value := strings.TrimSpace(firstQueryValue(req.Query, "workspace_id")); value != "" {
		workspace = value
	}
	if value := strings.TrimSpace(firstQueryValue(req.Query, "auth_cookie")); value != "" {
		cookie = value
	}
	if value := strings.TrimSpace(firstQueryValue(req.Query, "action")); value != "" {
		action = value
	}
	if value := strings.TrimSpace(firstQueryValue(req.Query, "account_id")); value != "" {
		removeID = value
	}
	if value := strings.TrimSpace(firstQueryValue(req.Query, "timeout_seconds")); value != "" {
		if parsed, errParse := strconv.Atoi(value); errParse == nil && parsed >= 1 && parsed <= openCodeQuotaMaxTimeoutSeconds {
			timeout = parsed
		}
	}

	var message string
	switch action {
	case "save":
		if workspace != "" && cookie != "" {
			if _, errSave := a.opencode.SaveAccount(workspace, cookie); errSave != nil {
				message = "Save failed: " + errSave.Error()
			} else {
				message = "Account saved and queried."
			}
		} else {
			message = "Save failed: workspace_id and auth_cookie are both required."
		}
	case "remove":
		if removeID != "" {
			if errRemove := a.opencode.RemoveAccount(removeID); errRemove != nil {
				message = "Remove failed: " + errRemove.Error()
			} else {
				message = "Account removed."
			}
		}
	case "refresh":
		a.opencode.RefreshAll(true)
		message = "All accounts refreshed."
	case "query":
		if workspace != "" && cookie != "" {
			_ = a.opencode.Probe(workspace, cookie, timeout)
		}
	}

	results := a.opencode.RefreshAll(false)
	snapshot := a.opencode.Snapshot()
	snapshot.Results = results
	body := renderOpenCodeStatusPage(snapshot, message)
	return cpaapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":  []string{"text/html; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
		},
		Body: []byte(body),
	}
}

func firstQueryValue(query map[string][]string, key string) string {
	values := query[key]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func operationStatusForOpenCode(failed, total int) string {
	if failed == 0 {
		return OperationStatusSucceeded
	}
	if failed < total {
		return OperationStatusWarning
	}
	return OperationStatusFailed
}

func reasonCodeForOpenCode(failed, total int) string {
	if failed == 0 {
		return "all_refreshed"
	}
	if failed < total {
		return "partial_refresh_failed"
	}
	return "refresh_failed"
}

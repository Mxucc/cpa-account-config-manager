package manager

import (
	"context"
	"net/http"
	"strings"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

type clinePassAccountRequest struct {
	AccountID      string `json:"account_id,omitempty"`
	Name           string `json:"name,omitempty"`
	BaseURL        string `json:"base_url,omitempty"`
	APIKey         string `json:"api_key,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	Rebind         bool   `json:"rebind,omitempty"`
}

type clinePassModelRequest struct {
	AccountID      string `json:"account_id"`
	Model          string `json:"model,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type clinePassLoginStartRequest struct {
	// Method is one of oauth (browser device flow, the default), cli (reuse an
	// existing Cline CLI sign-in on this host) or api_key.
	Method  string `json:"method,omitempty"`
	Name    string `json:"name,omitempty"`
	APIKey  string `json:"api_key,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
}

type clinePassLoginPollRequest struct {
	SessionID string `json:"session_id"`
}

type clinePassLoginCancelRequest struct {
	SessionID string `json:"session_id"`
}

type clinePassAccountsResponse struct {
	Accounts     []ClinePassAccountView `json:"accounts"`
	StorageError string                 `json:"storage_error,omitempty"`
}

type clinePassCatalogResponse struct {
	Models         []clinePassCatalogModel `json:"models"`
	DefaultBaseURL string                  `json:"default_base_url"`
}

// handleClinePassAccounts lists, saves and removes Cline Pass accounts.
func (a *App) handleClinePassAccounts(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.clinePass == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "Cline Pass service is unavailable"})
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	managementKey := resolveManagementKey(req.Headers)
	if method == http.MethodGet {
		accounts := a.clinePass.ListAccounts()
		views := make([]*ClinePassAccountView, 0, len(accounts))
		for index := range accounts {
			views = append(views, &accounts[index])
		}
		// The routing state comes from one channel-list read and never fails the
		// list: an unreadable channel list or a missing management key degrades
		// every account to the unbound state.
		a.annotateClinePassRouteState(ctx, managementKey, views...)
		return jsonResponse(http.StatusOK, clinePassAccountsResponse{
			Accounts: accounts, StorageError: a.clinePass.StorageError(),
		})
	}
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	switch method {
	case http.MethodPost:
		var request clinePassAccountRequest
		if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid Cline Pass account request"})
		}
		startedAt := time.Now().UTC()
		accountID, errSave := a.clinePass.SaveAPIKeyAccount(request.AccountID, request.Name, request.BaseURL, request.APIKey)
		if errSave != nil {
			a.operations.Record(OperationEntry{
				Category: OperationCategoryOpenCode, Action: OperationActionOpenCodeSave,
				Status: OperationStatusFailed, Source: OperationSourceManual, Scope: OperationScopeSingle,
				TargetCount: 1, Failed: 1, StartedAt: startedAt, FinishedAt: time.Now().UTC(), ReasonCode: "invalid_credential",
			})
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": errSave.Error()})
		}
		view, _ := a.clinePass.AccountView(accountID)
		var result ClinePassProbeResult
		if strings.TrimSpace(request.APIKey) != "" {
			result = a.clinePass.Probe(ctx, view.BaseURL, request.APIKey, clinePassTimeout(request.TimeoutSeconds))
		} else {
			view, result = a.clinePass.ProbeAccount(ctx, accountID, clinePassTimeout(request.TimeoutSeconds))
		}
		a.operations.Record(OperationEntry{
			Category: OperationCategoryOpenCode, Action: OperationActionOpenCodeSave,
			Status: OperationStatusSucceeded, Source: OperationSourceManual, Scope: OperationScopeSingle,
			TargetCount: 1, Succeeded: 1, StartedAt: startedAt, FinishedAt: time.Now().UTC(), ReasonCode: "account_saved",
		})
		// The account is saved either way; binding is best-effort and its outcome
		// is reported on the response instead of failing the save.
		outcome := a.bindClinePassAccountBestEffort(ctx, managementKey, accountID)
		applyClinePassBindOutcome(&view, outcome)
		response := map[string]any{"account": view, "result": result}
		for key, value := range outcome.fields() {
			response[key] = value
		}
		return jsonResponse(http.StatusOK, response)
	case http.MethodDelete:
		accountID := strings.TrimSpace(firstQueryValue(req.Query, "account_id"))
		if accountID == "" {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": "account_id is required"})
		}
		startedAt := time.Now().UTC()
		if errRemove := a.clinePass.RemoveAccount(accountID); errRemove != nil {
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
	}
	return jsonResponse(http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
}

// handleClinePassCatalog returns the curated, allow-listed model catalog.
func (a *App) handleClinePassCatalog(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.clinePass == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "Cline Pass service is unavailable"})
	}
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	models := make([]clinePassCatalogModel, 0, len(clinePassCatalog))
	models = append(models, clinePassCatalog...)
	return jsonResponse(http.StatusOK, clinePassCatalogResponse{Models: models, DefaultBaseURL: clinePassDefaultBaseURL})
}

// handleClinePassLoginStart begins a sign-in: the browser device flow, reuse of
// a Cline CLI sign-in, or a pasted API key.
func (a *App) handleClinePassLoginStart(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.clinePass == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "Cline Pass service is unavailable"})
	}
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	var request clinePassLoginStartRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid Cline Pass sign-in request"})
	}
	switch strings.ToLower(strings.TrimSpace(request.Method)) {
	case "api_key", "apikey":
		accountID, errSave := a.clinePass.SaveAPIKeyAccount("", request.Name, request.BaseURL, request.APIKey)
		if errSave != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": errSave.Error()})
		}
		view, _ := a.clinePass.AccountView(accountID)
		loginView := ClinePassLoginView{Method: clinePassAuthMethodAPIKey, Status: clinePassLoginCompleted, Account: &view}
		a.completeClinePassLogin(ctx, managementKey, &loginView)
		return jsonResponse(http.StatusOK, loginView)
	case "cli":
		view, errLogin := a.clinePass.CompleteClineCLILogin(ctx, request.Name)
		if errLogin != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": errLogin.Error()})
		}
		a.completeClinePassLogin(ctx, managementKey, &view)
		return jsonResponse(http.StatusOK, view)
	default:
		view, errStart := a.clinePass.StartDeviceLogin(ctx, request.Name)
		if errStart != nil {
			return jsonResponse(http.StatusBadGateway, map[string]any{"error": errStart.Error()})
		}
		return jsonResponse(http.StatusOK, view)
	}
}

// handleClinePassLoginPoll performs one non-blocking poll of a device sign-in.
func (a *App) handleClinePassLoginPoll(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.clinePass == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "Cline Pass service is unavailable"})
	}
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	var request clinePassLoginPollRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid Cline Pass sign-in request"})
	}
	view, errPoll := a.clinePass.PollDeviceLogin(ctx, request.SessionID)
	if errPoll != nil {
		return jsonResponse(http.StatusNotFound, map[string]any{"error": errPoll.Error()})
	}
	// A completed sign-in binds the account best-effort so the new credential is
	// routable immediately; a bind failure is reported on the view.
	if view.Status == clinePassLoginCompleted {
		a.completeClinePassLogin(ctx, managementKey, &view)
	}
	return jsonResponse(http.StatusOK, view)
}

// handleClinePassLoginCancel abandons a pending device sign-in.
func (a *App) handleClinePassLoginCancel(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.clinePass == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "Cline Pass service is unavailable"})
	}
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	var request clinePassLoginCancelRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid Cline Pass sign-in request"})
	}
	if !a.clinePass.CancelDeviceLogin(request.SessionID) {
		return jsonResponse(http.StatusNotFound, map[string]any{"error": "Cline Pass login session was not found"})
	}
	return jsonResponse(http.StatusOK, map[string]any{"cancelled": true})
}

// handleClinePassRefresh rotates the OAuth tokens of one account and, when the
// caller asked for it, republishes the CPA channel key so routed traffic keeps
// working after the rotation.
func (a *App) handleClinePassRefresh(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.clinePass == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "Cline Pass service is unavailable"})
	}
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	var request clinePassAccountRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid Cline Pass refresh request"})
	}
	accountID := strings.TrimSpace(request.AccountID)
	if accountID == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "account_id is required"})
	}
	startedAt := time.Now().UTC()
	view, errRefresh := a.clinePass.RefreshToken(ctx, accountID)
	if errRefresh != nil {
		a.operations.Record(OperationEntry{
			Category: OperationCategoryOpenCode, Action: OperationActionOpenCodeRefresh,
			Status: OperationStatusFailed, Source: OperationSourceManual, Scope: OperationScopeSingle,
			TargetCount: 1, Failed: 1, StartedAt: startedAt, FinishedAt: time.Now().UTC(), ReasonCode: "refresh_failed",
		})
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": errRefresh.Error()})
	}
	response := map[string]any{"account": view}
	if request.Rebind {
		credential, errCredential := a.clinePass.credential(ctx, accountID)
		if errCredential != nil {
			return jsonResponse(http.StatusBadGateway, map[string]any{"error": errCredential.Error()})
		}
		result, errBind := a.bindClinePassChannel(ctx, managementKey, credential.BaseURL, credential.APIKey, a.clinePassChannelLabel(credential.ID), credential.Models, a.clinePass.clinePassClientVersion(ctx))
		if errBind != nil {
			a.operations.Record(OperationEntry{
				Category: OperationCategoryOpenCode, Action: OperationActionOpenCodeRefresh,
				Status: OperationStatusFailed, Source: OperationSourceManual, Scope: OperationScopeSingle,
				TargetCount: 1, Failed: 1, StartedAt: startedAt, FinishedAt: time.Now().UTC(), ReasonCode: "bind_failed",
			})
			return jsonResponse(http.StatusBadGateway, map[string]any{"error": errBind.Error()})
		}
		response["binding"] = result
	}
	a.operations.Record(OperationEntry{
		Category: OperationCategoryOpenCode, Action: OperationActionOpenCodeRefresh,
		Status: OperationStatusSucceeded, Source: OperationSourceManual, Scope: OperationScopeSingle,
		TargetCount: 1, Succeeded: 1, StartedAt: startedAt, FinishedAt: time.Now().UTC(), ReasonCode: "token_refreshed",
	})
	return jsonResponse(http.StatusOK, response)
}

// handleClinePassModels validates a stored credential against the gateway catalog.
func (a *App) handleClinePassModels(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.clinePass == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "Cline Pass service is unavailable"})
	}
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	var request clinePassModelRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid Cline Pass model request"})
	}
	if strings.TrimSpace(request.AccountID) == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "account_id is required"})
	}
	view, errRefresh := a.clinePass.RefreshModels(ctx, request.AccountID, request.TimeoutSeconds)
	if errRefresh != nil {
		if view.ID == "" {
			return jsonResponse(http.StatusNotFound, map[string]any{"error": errRefresh.Error()})
		}
		// The account stays usable: the catalog error is reported on the view.
		return jsonResponse(http.StatusOK, map[string]any{"account": view})
	}
	return jsonResponse(http.StatusOK, map[string]any{"account": view})
}

// handleClinePassModelTest sends one real chat completion through a stored account.
func (a *App) handleClinePassModelTest(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.clinePass == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "Cline Pass service is unavailable"})
	}
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	var request clinePassModelRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid Cline Pass model request"})
	}
	if strings.TrimSpace(request.AccountID) == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "account_id is required"})
	}
	if strings.TrimSpace(request.Model) == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "model is required"})
	}
	result, errProbe := a.clinePass.ProbeModel(ctx, request.AccountID, request.Model, request.TimeoutSeconds)
	if errProbe != nil {
		return jsonResponse(http.StatusNotFound, map[string]any{"error": errProbe.Error()})
	}
	return jsonResponse(http.StatusOK, map[string]any{"result": result})
}

// handleClinePassBind publishes the account's models to CPA routing by writing
// one OpenAI-compatible channel that carries the Cline identity headers.
func (a *App) handleClinePassBind(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.clinePass == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "Cline Pass service is unavailable"})
	}
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	var request clinePassModelRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid Cline Pass bind request"})
	}
	credential, errCredential := a.clinePass.credential(ctx, request.AccountID)
	if errCredential != nil {
		return jsonResponse(http.StatusNotFound, map[string]any{"error": errCredential.Error()})
	}
	models := credential.Models
	if len(models) == 0 {
		models = clinePassCatalogIDs()
	}
	result, errBind := a.bindClinePassChannel(ctx, managementKey, credential.BaseURL, credential.APIKey, a.clinePassChannelLabel(credential.ID), models, a.clinePass.clinePassClientVersion(ctx))
	if errBind != nil {
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": errBind.Error()})
	}
	return jsonResponse(http.StatusOK, map[string]any{"binding": result})
}

// clinePassChannelLabel names the CPA channel for one account, preferring the
// operator-supplied name so several subscriptions stay distinguishable.
func (a *App) clinePassChannelLabel(accountID string) string {
	if a != nil && a.clinePass != nil {
		if view, found := a.clinePass.AccountView(accountID); found {
			if name := strings.TrimSpace(view.Name); name != "" {
				return clinePassBoundChannelName + " " + name
			}
		}
	}
	return clinePassBoundChannelName
}

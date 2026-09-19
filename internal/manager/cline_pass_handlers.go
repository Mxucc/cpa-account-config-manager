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

// clinePassSettings is the persisted publishing switch of the Cline Pass view.
type clinePassSettings struct {
	// StripModelPrefix publishes the client-facing model id without the literal
	// cline-pass/ prefix. It defaults to on.
	StripModelPrefix bool `json:"strip_model_prefix"`
	// DeepseekUpstreamConsistency pins the Cline Pass DeepSeek requests to
	// DeepSeek's own upstream so one conversation keeps its prompt cache. It
	// defaults to off.
	DeepseekUpstreamConsistency bool `json:"deepseek_upstream_consistency"`
}

type clinePassSettingsView struct {
	Settings clinePassSettings `json:"settings"`
}

type clinePassSettingsUpdateRequest struct {
	StripModelPrefix *bool `json:"strip_model_prefix"`
	// DeepseekUpstreamConsistency is additive: omitting it leaves the stored
	// switch alone, so one control can be saved without resending the other.
	DeepseekUpstreamConsistency *bool `json:"deepseek_upstream_consistency"`
}

type clinePassSettingsUpdateResponse struct {
	Settings     clinePassSettings `json:"settings"`
	Rebound      int               `json:"rebound"`
	RebindErrors int               `json:"rebind_errors"`
}

// clinePassModelView is one model row of the Cline Pass model page. The upstream
// id is what a probe must send; the client id is what a client calls. No
// credential is ever part of this shape. The price fields carry the documented
// reference rates (USD per 1M tokens) rather than a charge: Priced reports whether
// the documentation prices the model at all, and the cache-write field is omitted
// when the documentation publishes no cache-write rate.
type clinePassModelView struct {
	ID                      string  `json:"id"`
	Name                    string  `json:"name"`
	Free                    bool    `json:"free"`
	UpstreamID              string  `json:"upstream_id"`
	ClientID                string  `json:"client_id"`
	Published               bool    `json:"published"`
	Priced                  bool    `json:"priced"`
	InputUSDPerMillion      float64 `json:"input_usd_per_million,omitempty"`
	OutputUSDPerMillion     float64 `json:"output_usd_per_million,omitempty"`
	CacheReadUSDPerMillion  float64 `json:"cache_read_usd_per_million,omitempty"`
	CacheWriteUSDPerMillion float64 `json:"cache_write_usd_per_million,omitempty"`
}

type clinePassModelsResponse struct {
	Models           []clinePassModelView `json:"models"`
	StripModelPrefix bool                 `json:"strip_model_prefix"`
	// DeepseekUpstreamConsistency reports the stored upstream-consistency switch.
	DeepseekUpstreamConsistency bool `json:"deepseek_upstream_consistency"`
	Accounts                    int  `json:"accounts"`
	ChannelBound                bool `json:"channel_bound"`
	ChannelModels               int  `json:"channel_models"`
	// ChannelStateUnreadable marks that the live channel list could not be read, so
	// ChannelBound is unknown rather than false.
	ChannelStateUnreadable bool   `json:"channel_state_unreadable,omitempty"`
	DefaultBaseURL         string `json:"default_base_url"`
}

// handleClinePassAccounts lists, saves and removes Cline Pass accounts.
func (a *App) handleClinePassAccounts(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.clinePass == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "Cline Pass service is unavailable"})
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	managementKey := resolveManagementKey(req.Headers)
	if method == http.MethodGet {
		// A rotating token inside the refresh margin is rotated before the list is built, so the
		// page reports a usable credential and the republish below replaces the channel row that
		// carried the old one.
		a.refreshExpiringClinePassAccounts(ctx)
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
		outcome := a.bindClinePassAccountBestEffort(ctx, managementKey, accountID, true)
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
		result, errBind := a.bindClinePassChannel(ctx, managementKey, credential.ID, credential.BaseURL, credential.APIKey, a.clinePassChannelLabel(credential.ID), credential.Models, a.clinePass.clinePassClientVersion(ctx))
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
	result, errBind := a.bindClinePassChannel(ctx, managementKey, credential.ID, credential.BaseURL, credential.APIKey, a.clinePassChannelLabel(credential.ID), models, a.clinePass.clinePassClientVersion(ctx))
	if errBind != nil {
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": errBind.Error()})
	}
	return jsonResponse(http.StatusOK, map[string]any{"binding": result})
}

// clinePassChannelLabel names the CPA channel for one account, preferring the
// operator-supplied name so several subscriptions stay distinguishable. It shares
// clinePassChannelLabelForName with the routing checks, so the label a bind writes and the label a
// repair compares against can never drift apart.
func (a *App) clinePassChannelLabel(accountID string) string {
	if a != nil && a.clinePass != nil {
		if view, found := a.clinePass.AccountView(accountID); found {
			return clinePassChannelLabelForName(view.Name)
		}
	}
	return clinePassChannelLabelForName("")
}

// refreshExpiringClinePassAccounts rotates the tokens a Cline Pass read finds inside the refresh
// margin, under the same bound as an automatic bind so a slow token endpoint cannot hold a page
// read open. A rotation failure is not reported here: the read that follows shows the stored state,
// and the republish pass reports a row it could not repair.
func (a *App) refreshExpiringClinePassAccounts(ctx context.Context) {
	if a == nil || a.clinePass == nil {
		return
	}
	refreshCtx, cancel := context.WithTimeout(ctx, clinePassRoutingBindTimeout)
	defer cancel()
	a.clinePass.RefreshExpiringAccounts(refreshCtx)
}

// handleClinePassSettings reads and writes the Cline Pass publishing settings.
// A write is persisted first, then every stored account is re-bound best-effort
// so the mapping on the live channel follows the new setting; the per-account
// outcome is reported as counts instead of failing the request.
func (a *App) handleClinePassSettings(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.clinePass == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "Cline Pass service is unavailable"})
	}
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	switch strings.ToUpper(strings.TrimSpace(req.Method)) {
	case http.MethodGet:
		return jsonResponse(http.StatusOK, clinePassSettingsView{
			Settings: clinePassSettings{
				StripModelPrefix:            a.clinePass.StripModelPrefix(),
				DeepseekUpstreamConsistency: a.clinePass.DeepseekUpstreamConsistency(),
			},
		})
	case http.MethodPut:
		var request clinePassSettingsUpdateRequest
		if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil ||
			request.StripModelPrefix == nil && request.DeepseekUpstreamConsistency == nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid Cline Pass settings request"})
		}
		if request.DeepseekUpstreamConsistency != nil {
			if errSet := a.clinePass.SetDeepseekUpstreamConsistency(*request.DeepseekUpstreamConsistency); errSet != nil {
				return jsonResponse(http.StatusInternalServerError, map[string]any{"error": "Cline Pass settings could not be persisted"})
			}
		}
		// Only the published ids need a re-bind; the upstream pin is read from the
		// request path, so flipping it changes nothing about the channel.
		rebound, rebindErrors := 0, 0
		if request.StripModelPrefix != nil {
			if errSet := a.clinePass.SetStripModelPrefix(*request.StripModelPrefix); errSet != nil {
				return jsonResponse(http.StatusInternalServerError, map[string]any{"error": "Cline Pass settings could not be persisted"})
			}
			rebound, rebindErrors = a.rebindClinePassAccounts(ctx, managementKey)
		}
		return jsonResponse(http.StatusOK, clinePassSettingsUpdateResponse{
			Settings: clinePassSettings{
				StripModelPrefix:            a.clinePass.StripModelPrefix(),
				DeepseekUpstreamConsistency: a.clinePass.DeepseekUpstreamConsistency(),
			},
			Rebound:      rebound,
			RebindErrors: rebindErrors,
		})
	}
	return jsonResponse(http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
}

// handleClinePassModelPage lists the models the Cline Pass view shows: the
// allow-listed catalog, or the stored accounts' refreshed listings when any
// exists, with the client-facing id the current setting implies and whether the
// bound channel already publishes that alias. The channel list is read once and
// is shared by every row; an unreadable list degrades to "nothing published"
// instead of failing the response.
func (a *App) handleClinePassModelPage(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.clinePass == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "Cline Pass service is unavailable"})
	}
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	// Same as the account list: rotate an expiring token first, then read, so the routing decision
	// below is made on the credential the row will hold.
	a.refreshExpiringClinePassAccounts(ctx)
	accounts := a.clinePass.ListAccounts()
	stripPrefix := a.clinePass.StripModelPrefix()
	// Reading the page also repairs the channel: an account whose credential was rotated is
	// published again here, so the page never reports a state the plugin would fix on a click.
	routes, channelStateReadable := a.clinePassRoutesWithAutoBind(ctx, managementKey, accounts)
	published := map[string]struct{}{}
	channelBound := false
	channelModels := 0
	for _, account := range accounts {
		// Each account owns a row, so its own credential selects the row that publishes it; a
		// deployment that has not re-bound yet still resolves through the shared gateway row.
		route, bound := clinePassChannelRouteLookup(account, a.clinePass.accessToken(account.ID), routes)
		if !bound {
			continue
		}
		channelBound = true
		// Several accounts can point at different gateways; report the largest
		// bound channel so the scalar stays meaningful.
		if route.models > channelModels {
			channelModels = route.models
		}
		for alias := range route.aliases {
			published[alias] = struct{}{}
		}
	}
	models := make([]clinePassModelView, 0, len(clinePassCatalog))
	for _, id := range clinePassModelPageIDs(accounts) {
		clientID := clinePassModelAlias(id, stripPrefix)
		view := clinePassModelView{ID: id, Name: id, UpstreamID: id, ClientID: clientID}
		if known, ok := clinePassCatalogModelByID(id); ok {
			view.Name = known.Name
			view.Free = known.Free
		}
		_, isPublished := published[clientID]
		view.Published = isPublished
		applyClinePassModelPrice(&view, id)
		models = append(models, view)
	}
	return jsonResponse(http.StatusOK, clinePassModelsResponse{
		Models:                      models,
		StripModelPrefix:            stripPrefix,
		DeepseekUpstreamConsistency: a.clinePass.DeepseekUpstreamConsistency(),
		Accounts:                    len(accounts),
		ChannelBound:                channelBound,
		ChannelStateUnreadable:      !channelStateReadable,
		ChannelModels:               channelModels,
		DefaultBaseURL:              clinePassDefaultBaseURL,
	})
}

// clinePassModelPageIDs lists the ids the model page shows. The allow-listed
// catalog is the default; when at least one stored account carries a refreshed
// listing, the union of those listings is used instead, catalog order first and
// unknown ids after.
func clinePassModelPageIDs(accounts []ClinePassAccountView) []string {
	accountIDs := make([]string, 0, len(clinePassCatalog))
	seen := make(map[string]bool, len(clinePassCatalog))
	hasListings := false
	for _, account := range accounts {
		if len(account.Models) == 0 {
			continue
		}
		hasListings = true
		for _, model := range account.Models {
			trimmed := strings.TrimSpace(model)
			if trimmed == "" || seen[trimmed] {
				continue
			}
			seen[trimmed] = true
			accountIDs = append(accountIDs, trimmed)
		}
	}
	if !hasListings {
		return clinePassCatalogIDs()
	}
	known := make(map[string]bool, len(clinePassCatalog))
	ordered := make([]string, 0, len(accountIDs))
	for _, model := range clinePassCatalog {
		known[model.ID] = true
		if seen[model.ID] {
			ordered = append(ordered, model.ID)
		}
	}
	for _, id := range accountIDs {
		if !known[id] {
			ordered = append(ordered, id)
		}
	}
	return ordered
}

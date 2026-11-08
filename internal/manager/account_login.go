package manager

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"cpa-account-config-manager/internal/cpaapi"
)

// errHostLoginNoBody reports a host answer without a body at all, which differs
// from an empty body: an empty body means the login is still pending.
var errHostLoginNoBody = errors.New("the host login endpoint returned no response body")

// Adding an account.
//
// CPA owns the login flows: it answers /v8/management/oauth/auth-url with a
// state and a user-facing URL and reports progress on /v8/management/oauth/status.
// The plugin page therefore does not implement a provider login of its own; it
// offers the provider picker and relays those two calls, so a browser that can
// reach the plugin page never has to hold the management key for a second
// purpose and never talks to a provider directly.
//
// Older CPA releases only serve the per-provider v0 endpoints, so a v8 miss
// falls back to <provider>-auth-url and get-auth-status.

const accountLoginRoutePrefix = managementRoutePrefix + "/accounts/login"

// accountLoginProviders is the curated list the picker offers. It is curated
// rather than discovered because asking CPA to start a flow is the only way to
// learn whether a provider exists, and starting a flow has side effects: each
// call mints a state the host holds until it expires. The list is the set of
// providers the host serves today; a provider that disappears answers
// provider_not_found and the dialog says so instead of failing silently.
//
// Names are English here because Go sources are English-only; the page maps the
// id to a localized label and falls back to this name.
var accountLoginProviders = []accountLoginProvider{
	{ID: "codex", Name: "Codex", Flow: "browser"},
	{ID: "claude", Name: "Claude", Flow: "browser"},
	{ID: "antigravity", Name: "Antigravity", Flow: "browser"},
	{ID: "devin", Name: "Devin", Flow: "browser"},
	{ID: "xai", Name: "xAI", Flow: "device"},
	{ID: "kimi", Name: "Kimi", Flow: "device"},
	{ID: "meta", Name: "Muse", Flow: "device"},
	{ID: "codex-agent-identity", Name: "Codex Agent Identity", Flow: "plugin"},
	{ID: "cline-pass", Name: "Cline Pass", Flow: "plugin"},
}

type accountLoginProvider struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Flow string `json:"flow"`
}

// accountLoginProviderView is one row in the picker. Source separates a provider
// CPA itself logs into from one this plugin publishes, and from a community
// plugin that can only be signed into inside its own runtime.
type accountLoginProviderView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Flow   string `json:"flow"`
	Source string `json:"source"`
}

type accountLoginStartRequest struct {
	Provider string `json:"provider"`
}

// accountLoginFlow is what the page needs to drive a login: the URL to open, the
// state to poll, and the device code when the flow shows one.
type accountLoginFlow struct {
	Provider  string `json:"provider"`
	State     string `json:"state"`
	URL       string `json:"url,omitempty"`
	Flow      string `json:"flow,omitempty"`
	UserCode  string `json:"user_code,omitempty"`
	ExpiresIn int    `json:"expires_in,omitempty"`
	Status    string `json:"status,omitempty"`
}

// accountLoginStatus is the polled state. Done is true only when CPA reports a
// finished login; the page reloads the account list on that signal.
type accountLoginStatus struct {
	State   string `json:"state"`
	Status  string `json:"status"`
	Done    bool   `json:"done"`
	Message string `json:"message,omitempty"`
}

// handleAccountLogin routes the three account-login endpoints. They live under
// the plugin prefix so the page uses one base URL for everything.
func (a *App) handleAccountLogin(ctx context.Context, req cpaapi.ManagementRequest, method, path string) (cpaapi.ManagementResponse, bool) {
	switch {
	case method == http.MethodGet && path == "/v0/management"+accountLoginRoutePrefix:
		return a.accountLoginProviderList(), true
	case method == http.MethodPost && path == "/v0/management"+accountLoginRoutePrefix+"/start":
		return a.accountLoginStart(ctx, req), true
	case method == http.MethodGet && path == "/v0/management"+accountLoginRoutePrefix+"/status":
		return a.accountLoginPoll(ctx, req), true
	}
	return cpaapi.ManagementResponse{}, false
}

// accountLoginProviderList reports the providers the picker can start, plus the
// imported community plugins. A community plugin runs its own sign-in inside
// Magpie or OpenCode, so it is listed as unavailable here rather than offered as
// a button that cannot work.
func (a *App) accountLoginProviderList() cpaapi.ManagementResponse {
	providers := make([]accountLoginProviderView, 0, len(accountLoginProviders))
	for _, entry := range accountLoginProviders {
		source := "host"
		if entry.Flow == "plugin" {
			source = "plugin"
		}
		providers = append(providers, accountLoginProviderView{
			ID: entry.ID, Name: entry.Name, Flow: entry.Flow, Source: source,
		})
	}
	community := make([]accountLoginProviderView, 0)
	if store := a.providerImportStore(); store != nil {
		for _, imported := range store.List() {
			community = append(community, accountLoginProviderView{
				ID: imported.ID, Name: imported.Name, Flow: "external", Source: "community",
			})
		}
	}
	return jsonResponse(http.StatusOK, map[string]any{
		"providers": providers,
		"community": community,
	})
}

func (a *App) accountLoginStart(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request accountLoginStartRequest
	if errDecode := json.Unmarshal(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid login request"})
	}
	provider := strings.ToLower(strings.TrimSpace(request.Provider))
	if !validAccountLoginProviderID(provider) {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "provider is invalid"})
	}
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	query := url.Values{}
	query.Set("provider", provider)
	status, body, errCall := a.hostLoginCall(ctx, managementKey, "/v8/management/oauth/auth-url?"+query.Encode())
	if errCall != nil {
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": "the host login endpoint is unreachable", "reason": errCall.Error()})
	}
	if status == http.StatusNotFound || status == http.StatusBadRequest {
		// A v0 host serves one endpoint per provider instead of a query.
		status, body, errCall = a.hostLoginCall(ctx, managementKey, "/v0/management/"+provider+"-auth-url")
		if errCall != nil {
			return jsonResponse(http.StatusBadGateway, map[string]any{"error": "the host login endpoint is unreachable", "reason": errCall.Error()})
		}
	}
	if status != http.StatusOK {
		return jsonResponse(status, map[string]any{
			"error":    "the host refused to start this login",
			"provider": provider,
			"reason":   hostLoginReason(body),
		})
	}
	var answer accountLoginFlow
	if errDecode := json.Unmarshal(body, &answer); errDecode != nil {
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": "the host login answer is not readable"})
	}
	answer.Provider = provider
	if answer.Status == "" {
		answer.Status = "ok"
	}
	if answer.Flow == "" {
		answer.Flow = "browser"
	}
	return jsonResponse(http.StatusOK, answer)
}

func (a *App) accountLoginPoll(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	state := strings.TrimSpace(firstQueryValue(req.Query, "state"))
	if state == "" || len(state) > 200 {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "state is invalid"})
	}
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	query := url.Values{}
	query.Set("state", state)
	status, body, errCall := a.hostLoginCall(ctx, managementKey, "/v8/management/oauth/status?"+query.Encode())
	if errCall != nil {
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": "the host login endpoint is unreachable", "reason": errCall.Error()})
	}
	if status == http.StatusNotFound {
		status, body, errCall = a.hostLoginCall(ctx, managementKey, "/v0/management/get-auth-status?"+query.Encode())
		if errCall != nil {
			return jsonResponse(http.StatusBadGateway, map[string]any{"error": "the host login endpoint is unreachable", "reason": errCall.Error()})
		}
	}
	// A pending login answers with an empty body or a wait status; only an
	// explicit success finishes the flow.
	var answer struct {
		Status  string `json:"status"`
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if len(strings.TrimSpace(string(body))) > 0 {
		_ = json.Unmarshal(body, &answer)
	}
	result := accountLoginStatus{State: state, Status: strings.TrimSpace(answer.Status)}
	switch {
	case result.Status == "":
		result.Status = "wait"
	case answer.Error != "":
		result.Message = answer.Error
	}
	switch result.Status {
	case "ok", "success", "done", "completed", "authenticated":
		result.Done = true
		result.Message = strings.TrimSpace(answer.Message)
	case "error", "failed", "expired", "timeout":
		if result.Message == "" {
			result.Message = result.Status
		}
	}
	return jsonResponse(http.StatusOK, result)
}

// hostLoginCall performs one login request against the host with the operator's
// own management key. It returns the raw body because a pending poll answers
// with no content, which a JSON decoder would treat as an error.
func (a *App) hostLoginCall(ctx context.Context, managementKey, path string) (int, []byte, error) {
	client, errClient := newManagementClient(resolveManagementBaseURL(a.configSnapshot().ManagementBaseURL), managementKey, a.managementDoer)
	if errClient != nil {
		return 0, nil, errClient
	}
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+path, nil)
	if errRequest != nil {
		return 0, nil, errRequest
	}
	request.Header.Set("Authorization", "Bearer "+client.key)
	request.Header.Set("Accept", "application/json")
	response, errDo := client.doer.Do(request)
	if errDo != nil {
		return 0, nil, errDo
	}
	if response == nil || response.Body == nil {
		return 0, nil, errHostLoginNoBody
	}
	defer func() {
		_ = response.Body.Close()
	}()
	limited := io.LimitReader(response.Body, maxManagementResponse+1)
	body, errRead := io.ReadAll(limited)
	if errRead != nil {
		return 0, nil, errRead
	}
	if len(body) > maxManagementResponse {
		body = body[:maxManagementResponse]
	}
	return response.StatusCode, body, nil
}

// hostLoginReason extracts the host's own reason so the page can show
// provider_not_found instead of a generic failure.
func hostLoginReason(body []byte) string {
	var answer struct {
		Error  string `json:"error"`
		Reason string `json:"reason"`
	}
	if errDecode := json.Unmarshal(body, &answer); errDecode != nil {
		return ""
	}
	if reason := strings.TrimSpace(answer.Reason); reason != "" {
		return reason
	}
	return strings.TrimSpace(answer.Error)
}

// validAccountLoginProviderID keeps the provider inside the host's slug space so
// the value cannot escape the endpoint path.
func validAccountLoginProviderID(provider string) bool {
	if provider == "" || len(provider) > 64 {
		return false
	}
	for _, character := range provider {
		switch {
		case character >= 'a' && character <= 'z':
		case character >= '0' && character <= '9':
		case character == '-':
		default:
			return false
		}
	}
	return true
}

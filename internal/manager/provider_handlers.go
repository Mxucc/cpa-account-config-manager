package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"

	"cpa-account-config-manager/internal/cpaapi"
	"cpa-account-config-manager/internal/provider"
)

// Unified provider API.
//
// The routes are exact paths with query parameters, not path parameters:
// CLIProxyAPI's plugin host registers plugin Management routes by exact path and
// normalizes them, so a dynamic segment would never match. Query parameters also
// keep one route per action, which is what the convention in AGENTS.md asks for.
const (
	// providerRouteRelative is what ManagementRegistration publishes: the host
	// prefixes /v0/management itself.
	providerRouteRelative = managementRoutePrefix + "/providers"
	// providerRoutePrefix is what an incoming request carries.
	providerRoutePrefix = "/v0/management" + providerRouteRelative
)

// providerService is the read model shared by every provider route.
func (a *App) providerService() *provider.Service {
	if a == nil {
		return provider.NewService(nil)
	}
	a.mu.RLock()
	service := a.providers
	a.mu.RUnlock()
	if service == nil {
		return provider.NewService(nil)
	}
	return service
}

// providerManagementRoutes lists the exact routes this API registers.
func providerManagementRoutes() []cpaapi.ManagementRoute {
	return []cpaapi.ManagementRoute{
		{Method: http.MethodGet, Path: providerRouteRelative, Description: "List provider definitions with live account counts."},
		{Method: http.MethodGet, Path: providerRouteRelative + "/overview", Description: "Aggregate account counts, statuses and subscription spend."},
		{Method: http.MethodGet, Path: providerRouteRelative + "/accounts", Description: "List normalized accounts, optionally filtered by provider."},
		{Method: http.MethodGet, Path: providerRouteRelative + "/account", Description: "Read one normalized account by its canonical id."},
		{Method: http.MethodGet, Path: providerRouteRelative + "/plans", Description: "List provider plan and capability metadata."},
		{Method: http.MethodGet, Path: providerRouteRelative + "/pricing", Description: "Read the price catalog a provider's billing uses."},
		{Method: http.MethodPost, Path: providerRouteRelative + "/account/refresh", Description: "Refresh one account's quota, plan and models."},
		{Method: http.MethodPost, Path: providerRouteRelative + "/account/test", Description: "Probe one model through an account credential."},
		{Method: http.MethodPost, Path: providerRouteRelative + "/account/bind", Description: "Publish or update one account's CPA channel."},
		{Method: http.MethodGet, Path: providerRouteRelative + "/catalog", Description: "List provider presets and imported Magpie/OpenCode provider plugins."},
		{Method: http.MethodPost, Path: providerRouteRelative + "/import", Description: "Import one Magpie/OpenCode provider plugin manifest as a provider definition."},
		{Method: http.MethodDelete, Path: providerRouteRelative + "/import", Description: "Forget one imported provider definition."},
		{Method: http.MethodGet, Path: providerRouteRelative + "/settings", Description: "Read the provider-domain switches, including whether plugin hooks may run."},
		{Method: http.MethodPut, Path: providerRouteRelative + "/settings", Description: "Persist the provider-domain switches."},
	}
}

// HandleQuotaDescribe answers quota.describe.
func (a *App) HandleQuotaDescribe(_ context.Context, _ cpaapi.QuotaDescribeRequest) cpaapi.QuotaDescribeResponse {
	return cpaapi.QuotaDescribeResponse{
		SupportedProviders: provider.SupportedQuotaProviders(),
		DisplayName:        "CPA Account Config Manager",
		SupportsReset:      false,
	}
}

// HandleQuotaFetch answers quota.fetch for one credential CPA named by index.
func (a *App) HandleQuotaFetch(ctx context.Context, request cpaapi.QuotaFetchRequest) (cpaapi.QuotaFetchResponse, error) {
	response, handled, errFetch := a.providerService().QuotaForAuthIndex(ctx, request.AuthIndex)
	if errFetch != nil {
		return cpaapi.QuotaFetchResponse{}, errFetch
	}
	if handled {
		return response, nil
	}
	// A credential this plugin does not manage gets an explicit empty answer rather
	// than an error: the Management Center then keeps its built-in card.
	return cpaapi.QuotaFetchResponse{}, nil
}

// HandleQuotaReset reports that this plugin does not reset quotas. The plan's
// reset credits belong to CPA's own Codex metadata flow, which already exposes
// an explicit operator confirmation there.
func (a *App) HandleQuotaReset(_ context.Context, _ cpaapi.QuotaResetRequest) (cpaapi.QuotaResetResponse, error) {
	return cpaapi.QuotaResetResponse{Success: false, Message: "this plugin does not reset quotas"}, nil
}

// handleProviderRoutes dispatches one provider API request. It reports whether
// the path belonged to this API so the main Management switch stays untouched.
func (a *App) handleProviderRoutes(ctx context.Context, req cpaapi.ManagementRequest, method, path string) (cpaapi.ManagementResponse, bool) {
	if path != providerRoutePrefix && !strings.HasPrefix(path, providerRoutePrefix+"/") {
		return cpaapi.ManagementResponse{}, false
	}
	switch {
	case method == http.MethodGet && path == providerRoutePrefix:
		return jsonResponse(http.StatusOK, map[string]any{"providers": a.providerService().Providers(ctx)}), true
	case method == http.MethodGet && path == providerRoutePrefix+"/overview":
		return jsonResponse(http.StatusOK, a.providerService().Overview(ctx)), true
	case method == http.MethodGet && path == providerRoutePrefix+"/accounts":
		providerID := strings.TrimSpace(firstQueryValue(req.Query, "provider"))
		accounts := a.providerService().Accounts(ctx)
		if providerID != "" {
			accounts = a.providerService().AccountsForProvider(ctx, providerID)
		}
		return jsonResponse(http.StatusOK, map[string]any{"accounts": accounts, "total": len(accounts)}), true
	case method == http.MethodGet && path == providerRoutePrefix+"/account":
		id := strings.TrimSpace(firstQueryValue(req.Query, "id"))
		if id == "" {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": "id is required"}), true
		}
		account, found := a.providerService().Account(ctx, id)
		if !found {
			return jsonResponse(http.StatusNotFound, map[string]any{"error": "account not found"}), true
		}
		return jsonResponse(http.StatusOK, account), true
	case method == http.MethodGet && path == providerRoutePrefix+"/plans":
		return jsonResponse(http.StatusOK, map[string]any{"providers": a.providerService().Providers(ctx)}), true
	case method == http.MethodGet && path == providerRoutePrefix+"/pricing":
		return jsonResponse(http.StatusOK, a.providerPricingSnapshot(strings.TrimSpace(firstQueryValue(req.Query, "provider")))), true
	case method == http.MethodPost && path == providerRoutePrefix+"/account/refresh":
		return a.handleProviderAction(ctx, req, "refresh"), true
	case method == http.MethodPost && path == providerRoutePrefix+"/account/test":
		return a.handleProviderAction(ctx, req, "test"), true
	case method == http.MethodPost && path == providerRoutePrefix+"/account/bind":
		return a.handleProviderAction(ctx, req, "bind"), true
	case method == http.MethodGet && path == providerRoutePrefix+"/catalog":
		imported := a.providerImportStore().List()
		entries := make([]map[string]any, 0, len(imported))
		for _, entry := range imported {
			raw, errMarshal := json.Marshal(entry)
			if errMarshal != nil {
				continue
			}
			var view map[string]any
			if errUnmarshal := json.Unmarshal(raw, &view); errUnmarshal != nil {
				continue
			}
			if errHook := a.hookError(entry.ID); errHook != "" {
				view["hook_error"] = errHook
			}
			entries = append(entries, view)
		}
		return jsonResponse(http.StatusOK, map[string]any{
			"presets":                provider.Presets,
			"imported":               entries,
			"hook_execution_enabled": a.providerSettingsService().MagpieHookExecution(),
			"bun_available":          a.pluginRuntimeAvailable(),
			"plugins_dir":            a.providerImportStore().PluginsDir(),
		}), true
	case method == http.MethodGet && path == providerRoutePrefix+"/settings":
		return jsonResponse(http.StatusOK, a.providerSettingsService().Get()), true
	case method == http.MethodPut && path == providerRoutePrefix+"/settings":
		var request struct {
			MagpieHookExecution *bool `json:"magpie_hook_execution"`
		}
		if errDecode := json.Unmarshal(req.Body, &request); errDecode != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid provider settings"}), true
		}
		current := a.providerSettingsService().Get()
		if request.MagpieHookExecution != nil {
			current.MagpieHookExecution = *request.MagpieHookExecution
		}
		if errSave := a.providerSettingsService().Set(current); errSave != nil {
			return jsonResponse(http.StatusInternalServerError, map[string]any{"error": errSave.Error()}), true
		}
		return jsonResponse(http.StatusOK, a.providerSettingsService().Get()), true
	case method == http.MethodPost && path == providerRoutePrefix+"/import":
		return a.handleProviderImport(req), true
	case method == http.MethodDelete && path == providerRoutePrefix+"/import":
		id := strings.TrimSpace(firstQueryValue(req.Query, "id"))
		if errRemove := a.providerImportStore().Remove(id); errRemove != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": errRemove.Error()}), true
		}
		return jsonResponse(http.StatusOK, map[string]any{"status": "removed", "id": id}), true
	}
	return jsonResponse(http.StatusNotFound, map[string]any{"error": "unknown provider route"}), true
}

// providerPricingSnapshot returns the price source a provider's billing uses.
// Only catalogs the plugin already syncs are reported; an empty answer says the
// provider has no plugin-side price catalog instead of inventing one.
func (a *App) providerPricingSnapshot(providerID string) map[string]any {
	if a == nil {
		return map[string]any{"provider": providerID, "available": false}
	}
	switch providerID {
	case "opencode-go", "opencode-zen", "":
		if a.opencodePricing != nil {
			return map[string]any{"provider": providerID, "available": true, "snapshot": a.opencodePricing.Snapshot()}
		}
	}
	return map[string]any{"provider": providerID, "available": false, "reason": "no plugin-side price catalog"}
}

// handleProviderAction routes a unified action to the handler that already owns
// the provider's validation. The unified endpoint exists so a page does not have
// to know which provider-specific route to call; it does not reimplement any of
// them.
func (a *App) handleProviderAction(ctx context.Context, req cpaapi.ManagementRequest, action string) cpaapi.ManagementResponse {
	providerID := strings.TrimSpace(firstQueryValue(req.Query, "provider"))
	id := strings.TrimSpace(firstQueryValue(req.Query, "id"))
	if providerID == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "provider is required"})
	}
	switch action {
	case "refresh":
		switch providerID {
		case "opencode-go":
			return a.handleOpenCodeRefreshAccount(ctx, managementRequestWithQuery(req, map[string][]string{"account_id": {id}, "kind": {"go"}}))
		case "cline-pass":
			return a.handleClinePassRefresh(ctx, managementRequestWithBody(req, map[string]any{"account_id": id}))
		case "codex":
			return jsonResponse(http.StatusNotImplemented, map[string]any{
				"error":  "codex_refresh_unsupported",
				"reason": "a Codex credential is refreshed by CPA itself or by the account token-refresh route",
			})
		default:
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": "unknown provider"})
		}
	case "test":
		switch providerID {
		case "codex":
			return a.handleCodexModelTest(ctx, req)
		case "opencode-go", "opencode-zen":
			return a.handleOpenCodeModelTest(ctx, req)
		case "cline-pass":
			return a.handleClinePassModelTest(ctx, req)
		default:
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": "unknown provider"})
		}
	case "bind":
		switch providerID {
		case "opencode-go", "opencode-zen":
			return a.handleOpenCodeBind(ctx, req)
		case "cline-pass":
			return a.handleClinePassBind(ctx, req)
		default:
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": "provider has no channel binding"})
		}
	}
	return jsonResponse(http.StatusBadRequest, map[string]any{"error": "unknown action"})
}

func managementRequestWithQuery(req cpaapi.ManagementRequest, query map[string][]string) cpaapi.ManagementRequest {
	merged := make(map[string][]string, len(req.Query)+len(query))
	for key, values := range req.Query {
		merged[key] = values
	}
	for key, values := range query {
		merged[key] = values
	}
	req.Query = merged
	return req
}

func managementRequestWithBody(req cpaapi.ManagementRequest, body map[string]any) cpaapi.ManagementRequest {
	raw, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		return req
	}
	req.Body = raw
	if req.Headers == nil {
		req.Headers = http.Header{}
	}
	req.Headers.Set("Content-Type", "application/json")
	return req
}

// providerImportStore returns the import store for the configured data
// directory. A host without a writable directory still gets an in-memory store,
// so the catalog page works and an import fails with a clear reason.
func (a *App) providerImportStore() *provider.ImportStore {
	if a == nil {
		return provider.NewImportStore("")
	}
	dataDir := strings.TrimSpace(a.configSnapshot().DataDir)
	path := ""
	if dataDir != "" {
		path = filepath.Join(dataDir, "provider-imports.json")
	}
	a.providerImportsMu.Lock()
	defer a.providerImportsMu.Unlock()
	if a.providerImports != nil && a.providerImportsPath == path {
		return a.providerImports
	}
	a.providerImports = provider.NewImportStore(path)
	a.providerImportsPath = path
	return a.providerImports
}

// handleProviderImport accepts either {"manifest": "<package.json>", "source":
// "magpie"} or the package.json body itself. Nothing is executed: the import
// records the definition and says that hooks are not run.
func (a *App) handleProviderImport(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	source := strings.TrimSpace(firstQueryValue(req.Query, "source"))
	body := req.Body
	var wrapper struct {
		Manifest  string `json:"manifest"`
		Source    string `json:"source"`
		Directory string `json:"directory"`
	}
	if errUnmarshal := json.Unmarshal(req.Body, &wrapper); errUnmarshal == nil && strings.TrimSpace(wrapper.Manifest) != "" {
		body = []byte(wrapper.Manifest)
		if wrapper.Source != "" {
			source = wrapper.Source
		}
	}
	entry, errImport := a.providerImportStore().Import(body, source, wrapper.Directory)
	if errImport != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errImport.Error()})
	}
	return jsonResponse(http.StatusOK, map[string]any{"provider": entry})
}

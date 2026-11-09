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
		{Method: http.MethodDelete, Path: providerRouteRelative + "/account/channel", Description: "Remove the CPA channel this plugin published for a community provider."},
		{Method: http.MethodGet, Path: providerRouteRelative + "/catalog", Description: "List provider presets and imported Magpie/OpenCode provider plugins."},
		{Method: http.MethodGet, Path: providerRouteRelative + "/market", Description: "List the Magpie community plugin catalog this plugin can import from."},
		{Method: http.MethodPost, Path: providerRouteRelative + "/import", Description: "Import one Magpie/OpenCode provider plugin manifest, or one community plugin by id, as a provider definition."},
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
	case method == http.MethodDelete && path == providerRoutePrefix+"/account/channel":
		providerID := strings.TrimSpace(firstQueryValue(req.Query, "provider"))
		if providerID == "" {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": "provider is required"}), true
		}
		// A community account owns no CPA auth file, so the only object that can be
		// removed is the channel this plugin published for it. Refusing here is what
		// left an account an operator could not get rid of.
		if !a.isImportedProvider(providerID) {
			return jsonResponse(http.StatusBadRequest, map[string]any{
				"error":  "not_a_community_provider",
				"reason": "this account is managed by CPA; delete it through the account deletion route",
			}), true
		}
		a.rememberCommunityManagementKey(resolveManagementKey(req.Headers))
		removed, errRemove := a.removeCommunityChannel(providerID)
		if errRemove != nil {
			return jsonResponse(http.StatusOK, map[string]any{
				"error":    "community_channel_not_removed",
				"provider": providerID,
				"reason":   errRemove.Error(),
			}), true
		}
		return jsonResponse(http.StatusOK, map[string]any{
			"status":   "removed",
			"provider": providerID,
			"channels": removed,
		}), true
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
			// The stored notes describe the import that first recorded this
			// entry. An entry imported before community sign-in existed would
			// otherwise keep telling the operator to finish the login in another
			// application, so the page is given the current state instead.
			view["notes"] = provider.LiveNotes(entry)
			view["sign_in_available"] = strings.TrimSpace(entry.Directory) != ""
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
	case method == http.MethodGet && path == providerRoutePrefix+"/market":
		return a.handleProviderMarket(ctx, req), true
	case method == http.MethodPost && path == providerRoutePrefix+"/import":
		return a.handleProviderImport(ctx, req), true
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
		}
		// A community provider's channel is written by its own sign-in, and the
		// binding action republishes that channel's model list: CPA registers a
		// channel's models from that field, so a channel without one routes
		// nothing however healthy the credential is.
		if a.isImportedProvider(providerID) {
			a.rememberCommunityManagementKey(resolveManagementKey(req.Headers))
			updated, errRefresh := a.publishCommunityChannelModelRefresh(providerID)
			if errRefresh != nil {
				return jsonResponse(http.StatusOK, map[string]any{
					"error":    "community_channel_unroutable",
					"provider": providerID,
					"reason":   errRefresh.Error(),
				})
			}
			return jsonResponse(http.StatusOK, map[string]any{
				"provider": providerID,
				"channels": updated,
				"status":   "published",
			})
		}
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "provider has no channel binding"})
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
	// The import list is the operator's own registry of community providers. It is
	// kept in the plugin's stable home rather than the per-run data directory: a
	// reinstall of the plugin must not look like every import disappeared.
	path := ""
	if dataDir := strings.TrimSpace(a.configSnapshot().DataDir); dataDir != "" {
		path = filepath.Join(providerHomeDir(dataDir), "provider-imports.json")
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

// providerMarket returns the community plugin reader, creating it on first use.
// A test replaces it to exercise both the reachable and unreachable paths.
func (a *App) providerMarket() *provider.Market {
	if a == nil {
		return provider.NewMarket(nil)
	}
	a.marketMu.Lock()
	defer a.marketMu.Unlock()
	if a.market == nil {
		a.market = provider.NewMarket(nil)
	}
	return a.market
}

// SetProviderMarket replaces the market reader. Tests use it so no listing
// depends on GitHub or npm being reachable.
func (a *App) SetProviderMarket(market *provider.Market) {
	if a == nil {
		return
	}
	a.marketMu.Lock()
	a.market = market
	a.marketMu.Unlock()
}

// handleProviderMarket lists the Magpie community plugins an operator can import
// with one click. The answer is cached for a few minutes, and an unreachable
// registry is reported as a reason instead of an error, so the page can explain
// what happened rather than showing an empty list.
func (a *App) handleProviderMarket(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	locale := strings.TrimSpace(firstQueryValue(req.Query, "locale"))
	snapshot := a.providerMarket().Snapshot(ctx, locale)
	// Mark what is already registered: the page then offers an import only where the
	// import would change anything.
	imported := map[string]string{}
	for _, entry := range a.providerImportStore().List() {
		if key := strings.TrimSpace(entry.Package); key != "" {
			imported[key] = entry.Version
		}
	}
	for index := range snapshot.Plugins {
		version, found := imported[snapshot.Plugins[index].ID]
		if !found {
			continue
		}
		snapshot.Plugins[index].Imported = true
		snapshot.Plugins[index].ImportedVersion = version
	}
	return jsonResponse(http.StatusOK, snapshot)
}

// handleProviderImport accepts one of three shapes:
//
//	{"manifest": "<package.json>", "source": "magpie"}  a pasted manifest
//	{...package.json body...}                          the manifest itself
//	{"id": "@magpie-community/xxx"}                    a market entry by id
//
// The id form is what the market page uses: the plugin fetches the published
// manifest itself, so the operator never handles a package.json. Nothing is
// executed in any shape: the import records the definition and says hooks are
// not run.
func (a *App) handleProviderImport(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	source := strings.TrimSpace(firstQueryValue(req.Query, "source"))
	body := req.Body
	var wrapper struct {
		Manifest  string `json:"manifest"`
		Source    string `json:"source"`
		Directory string `json:"directory"`
		ID        string `json:"id"`
		Package   string `json:"package"`
	}
	decoded := json.Unmarshal(req.Body, &wrapper) == nil
	if decoded && strings.TrimSpace(wrapper.Source) != "" {
		source = wrapper.Source
	}
	if decoded && strings.TrimSpace(wrapper.Manifest) != "" {
		body = []byte(wrapper.Manifest)
	} else if decoded {
		id := firstNonEmptyText(wrapper.ID, wrapper.Package)
		if id != "" && strings.TrimSpace(wrapper.Manifest) == "" {
			manifest, errFetch := a.providerMarket().Manifest(ctx, id)
			if errFetch != nil {
				// A name that is not an npm package is the caller's mistake; a
				// fetch that failed is the network's.
				status := http.StatusBadGateway
				if strings.Contains(errFetch.Error(), "npm package name") || strings.Contains(errFetch.Error(), "is empty") {
					status = http.StatusBadRequest
				}
				return jsonResponse(status, map[string]any{
					"error":  "market_manifest_unavailable",
					"id":     id,
					"reason": errFetch.Error(),
				})
			}
			body = manifest
			// A market entry is a Magpie plugin, so it records the same origin a
			// pasted Magpie manifest does. The id itself stays out of the record:
			// it is the package name, which the manifest already carries.
			if source == "" {
				source = "magpie"
			}
		}
	}
	// A market import installs the package's code as well as its manifest: the
	// sign-in and the model hooks live in that code, so recording the manifest
	// alone is what left a community provider impossible to add an account for.
	directory := strings.TrimSpace(wrapper.Directory)
	installNote := ""
	if directory == "" {
		if installed, errInstall := a.installProviderPackage(ctx, firstNonEmptyText(wrapper.ID, wrapper.Package)); errInstall == nil && installed != "" {
			directory = installed
		} else if errInstall != nil {
			installNote = errInstall.Error()
		}
	}
	entry, errImport := a.providerImportStore().Import(body, source, directory)
	if errImport != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errImport.Error()})
	}
	response := map[string]any{"provider": entry}
	if installNote != "" {
		response["install_note"] = installNote
	}
	return jsonResponse(http.StatusOK, response)
}

// firstNonEmptyText returns the first trimmed non-empty argument.
func firstNonEmptyText(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

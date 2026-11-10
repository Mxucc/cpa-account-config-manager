package manager

import (
	"encoding/json"
	"net/http"
	"strings"

	"cpa-account-config-manager/internal/cpaapi"
)

// The two model routes a community provider needs: read what the package
// publishes and what the channel routes, and write a selection back.
//
// They are separate from the CPA account model route because a community account
// has no CPA auth file: its models live in the channel this plugin published, not
// in a credential file CPA owns.

// communityModelsRoutePath is the provider-scoped model route.
const communityModelsRoutePath = providerRoutePrefix + "/models"

// handleCommunityModels answers the model read and write for one community
// provider. It reports whether the path belonged to it.
func (a *App) handleCommunityModels(req cpaapi.ManagementRequest, method, path string) (cpaapi.ManagementResponse, bool) {
	if path != communityModelsRoutePath {
		return cpaapi.ManagementResponse{}, false
	}
	providerID := strings.TrimSpace(firstQueryValue(req.Query, "provider"))
	if providerID == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "provider is required"}), true
	}
	if !a.isImportedProvider(providerID) {
		return jsonResponse(http.StatusBadRequest, map[string]any{
			"error":  "not_a_community_provider",
			"reason": "this provider's models belong to CPA; read them through the account model route",
		}), true
	}
	a.rememberCommunityManagementKey(resolveManagementKey(req.Headers))
	switch method {
	case http.MethodGet:
		return a.buildCommunityModelsView(providerID), true
	case http.MethodPut, http.MethodPost:
		var request struct {
			Models []string `json:"models"`
		}
		if errDecode := json.Unmarshal(req.Body, &request); errDecode != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid model selection"}), true
		}
		channels, count, errWrite := a.writeCommunityChannelModels(providerID, request.Models)
		if errWrite != nil {
			return jsonResponse(http.StatusOK, map[string]any{
				"error":    "community_models_not_saved",
				"provider": providerID,
				"reason":   errWrite.Error(),
			}), true
		}
		view := a.buildCommunityModelsView(providerID)
		return jsonResponse(http.StatusOK, map[string]any{
			"provider":  providerID,
			"status":    "saved",
			"published": count,
			"channels":  channels,
			"view":      view,
		}), true
	}
	return jsonResponse(http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"}), true
}

// communityModelsView builds the read answer: the catalog, what is published, and
// which published ids the package no longer offers.
func (a *App) buildCommunityModelsView(providerID string) cpaapi.ManagementResponse {
	available := a.communityModelCatalog(providerID)
	published, errPublished := a.communityPublishedModels(providerID)
	view := communityModelsView{Provider: providerID, Available: available, Published: published}
	if errPublished != nil {
		view.Warning = errPublished.Error()
	}
	live := map[string]bool{}
	for _, id := range published {
		live[id] = true
	}
	known := map[string]bool{}
	for _, id := range available {
		known[id] = true
	}
	for _, id := range available {
		view.Rows = append(view.Rows, communityModelRow{ID: id, Published: live[id]})
	}
	// A published id the package stopped offering still routes until it is removed;
	// naming it is what lets the operator decide instead of wondering.
	for _, id := range published {
		if !known[id] {
			view.Stale = append(view.Stale, id)
			view.Rows = append(view.Rows, communityModelRow{ID: id, Published: true})
		}
	}
	if len(available) == 0 && view.Warning == "" {
		view.Warning = "this provider published no model catalog"
	}
	return jsonResponse(http.StatusOK, view)
}

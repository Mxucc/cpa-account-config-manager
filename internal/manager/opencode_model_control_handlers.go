package manager

import (
	"context"
	"net/http"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// OpenCode model-control routes. Disabling a model is privileged configuration,
// so both routes require the Management key. Responses carry only the model rows
// and the disabled ids: no credential, cookie, or request content.

type openCodeModelsUpdateRequest struct {
	Disabled []string `json:"disabled"`
}

// handleOpenCodeModelControl lists the known OpenCode models with the disabled
// state. The channel scan is refreshed here so the list reflects the live CPA
// channels.
func (a *App) handleOpenCodeModelControl(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	if a == nil || a.opencodeModelControl == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "OpenCode model control is unavailable"})
	}
	// A read waits for the scan, but only up to the refresh bound: an unreachable CPA
	// management API must degrade the row list, not hang the tab.
	scanContext, cancelScan := boundedModelControlContext(ctx)
	a.refreshOpenCodeChannelModels(scanContext, managementKey)
	cancelScan()
	return jsonResponse(http.StatusOK, a.openCodeModelControlPayload())
}

// handleOpenCodeModelControlUpdate replaces the globally disabled set. The change
// applies to every OpenCode account and AI-provider channel on the next request.
func (a *App) handleOpenCodeModelControlUpdate(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	if a == nil || a.opencodeModelControl == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "OpenCode model control is unavailable"})
	}
	var request openCodeModelsUpdateRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid OpenCode model request"})
	}
	if _, errUpdate := a.opencodeModelControl.Set(request.Disabled); errUpdate != nil {
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": errUpdate.Error()})
	}
	// The change is already applied and persisted, so the response comes from the cached
	// channel scan: waiting for the CPA management API here is what made a click look like
	// it did nothing. The cache is refreshed in the background, or synchronously when it was
	// never scanned and the row list would otherwise lose the channel-configured models.
	if len(a.cachedOpenCodeChannelModels()) == 0 {
		scanContext, cancelScan := boundedModelControlContext(ctx)
		a.refreshOpenCodeChannelModels(scanContext, managementKey)
		cancelScan()
	} else {
		refreshModelControlChannels(func(scan context.Context) { a.refreshOpenCodeChannelModels(scan, managementKey) })
	}
	return jsonResponse(http.StatusOK, a.openCodeModelControlPayload())
}

// openCodeModelControlPayload is the response body shared by the read and write
// routes, so both always report the same shape.
func (a *App) openCodeModelControlPayload() map[string]any {
	snapshot := a.opencodeModelControl.Snapshot()
	snapshot.Models = a.openCodeModelControlRows()
	snapshot.PricingUpdatedAt, snapshot.PricingSource = a.openCodeModelControlPricingProvenance()
	return map[string]any{
		"models":             snapshot.Models,
		"disabled":           snapshot.Disabled,
		"storage_error":      snapshot.StorageError,
		"pricing_source":     snapshot.PricingSource,
		"pricing_updated_at": snapshot.PricingUpdatedAt,
	}
}

// openCodeModelControlPricingProvenance reports the credit price table's source
// and last refresh. The Codex route reports the same table, so both workspaces
// label their displayed rates from one authority.
func (a *App) openCodeModelControlPricingProvenance() (time.Time, string) {
	if a == nil || a.creditUsage == nil {
		return time.Time{}, ""
	}
	return a.creditUsage.Provenance()
}

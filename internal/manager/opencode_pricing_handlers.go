package manager

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// openCodeImportRequest names the existing channel to import.
type openCodeImportRequest struct {
	BaseURL string `json:"base_url"`
}

// OpenCode price routes. Prices are public data, but the routes stay behind the
// Management key like every other OpenCode route so the UI has a single
// authenticated surface.

// handleOpenCodePricing reports the official OpenCode Zen and Go price catalog
// with its sync provenance.
func (a *App) handleOpenCodePricing(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	if a == nil || a.opencodePricing == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "OpenCode pricing service is unavailable"})
	}
	return jsonResponse(http.StatusOK, map[string]any{"pricing": a.opencodePricing.Snapshot()})
}

// handleOpenCodeSession reports the per-conversation session routing status. The
// call also self-heals the attribution map, because CPA assigns channel auth
// indexes itself and can regenerate them after a channel edit.
func (a *App) handleOpenCodeSession(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	if a == nil || a.opencodeSession == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "OpenCode session routing is unavailable"})
	}
	if a.opencodeSessionPrimedAt.Load() == 0 || time.Since(time.Unix(0, a.opencodeSessionPrimedAt.Load())) > openCodeSessionTargetTTL {
		a.opencodeSessionPrimedAt.Store(time.Now().UnixNano())
		if _, storageErr := a.resolveAIProviderChannelNames(ctx, managementKey); storageErr == "" {
			a.refreshOpenCodeSessionTargets()
		}
	}
	return jsonResponse(http.StatusOK, map[string]any{"session": a.opencodeSession.Snapshot()})
}

// handleOpenCodeChannels lists the CPA AI-provider channels that belong to
// OpenCode, so accounts configured on the AI providers page are visible and
// importable here.
func (a *App) handleOpenCodeChannels(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	if a == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "OpenCode channel service is unavailable"})
	}
	channels, errList := a.listOpenCodeChannels(ctx, managementKey)
	if errList != nil {
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": errList.Error()})
	}
	return jsonResponse(http.StatusOK, map[string]any{"channels": channels})
}

// handleOpenCodeImport copies one existing AI-provider channel credential into the
// OpenCode workspace. The credential is read and stored server-side and never
// travels to the browser.
func (a *App) handleOpenCodeImport(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	if a == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "OpenCode channel service is unavailable"})
	}
	var request openCodeImportRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid OpenCode import request"})
	}
	if strings.TrimSpace(request.BaseURL) == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "base_url is required"})
	}
	result, errImport := a.importOpenCodeChannel(ctx, managementKey, request.BaseURL)
	if errImport != nil {
		if errors.Is(errImport, ErrOpenCodeImportNeedsWorkspace) {
			return jsonResponse(http.StatusConflict, map[string]any{"error": errImport.Error(), "needs_workspace": true})
		}
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": errImport.Error()})
	}
	return jsonResponse(http.StatusOK, map[string]any{"import": result})
}

// handleOpenCodePricingRefresh revalidates the catalog on demand. A failed sync
// keeps the previous prices and reports the failure instead of clearing them.
func (a *App) handleOpenCodePricingRefresh(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	if a == nil || a.opencodePricing == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "OpenCode pricing service is unavailable"})
	}
	changed, errRefresh := a.opencodePricing.Refresh(ctx)
	if errRefresh != nil {
		return jsonResponse(http.StatusBadGateway, map[string]any{
			"error":   errRefresh.Error(),
			"changed": false,
			"pricing": a.opencodePricing.Snapshot(),
		})
	}
	return jsonResponse(http.StatusOK, map[string]any{
		"changed": changed,
		"pricing": a.opencodePricing.Snapshot(),
	})
}

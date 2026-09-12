package manager

import (
	"context"
	"net/http"

	"cpa-account-config-manager/internal/cpaapi"
)

// Self-update routes. The plugin updates itself from its own GitHub releases, so an
// installation whose host marketplace requests never succeed can still update. All
// routes require the Management key; the responses carry versions, checksums and
// file paths only.

type selfUpdateSettingsRequest struct {
	PluginFile string `json:"plugin_file"`
}

// handleSelfUpdate reports the direct-update state.
func (a *App) handleSelfUpdate(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	if a == nil || a.selfUpdate == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "self update is unavailable"})
	}
	return jsonResponse(http.StatusOK, map[string]any{"self_update": a.selfUpdate.Snapshot()})
}

// handleSelfUpdateCheck resolves the latest release now.
func (a *App) handleSelfUpdateCheck(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	if a == nil || a.selfUpdate == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "self update is unavailable"})
	}
	snapshot, errCheck := a.selfUpdate.Check(ctx)
	if errCheck != nil {
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": errCheck.Error(), "self_update": snapshot})
	}
	return jsonResponse(http.StatusOK, map[string]any{"self_update": snapshot})
}

// handleSelfUpdateInstall downloads and applies the selected release. The plugin
// library is already mapped into the running host, so the response always reports
// restart_required once the file was replaced.
func (a *App) handleSelfUpdateInstall(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	if a == nil || a.selfUpdate == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "self update is unavailable"})
	}
	snapshot, errInstall := a.selfUpdate.Install(ctx)
	if errInstall != nil {
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": errInstall.Error(), "self_update": snapshot})
	}
	return jsonResponse(http.StatusOK, map[string]any{"self_update": snapshot})
}

// handleSelfUpdateReload asks CPA to reinstall and reload this plugin. A store install is the
// only path the host watches for a native plugin reload, so it is the way to apply a replaced
// library without restarting CPA.
func (a *App) handleSelfUpdateReload(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	if a == nil || a.selfUpdate == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "self update is unavailable"})
	}
	result, errReload := a.selfUpdate.ReloadThroughStore(ctx, managementKey)
	payload := map[string]any{"reload": result}
	if errReload != nil {
		// The reason code lets the UI explain the refusal without echoing internal details.
		return jsonResponse(http.StatusBadGateway, payload)
	}
	return jsonResponse(http.StatusOK, payload)
}

// handleSelfUpdateSettings records the plugin library path when auto-detection is
// not possible on this host.
func (a *App) handleSelfUpdateSettings(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	if a == nil || a.selfUpdate == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "self update is unavailable"})
	}
	var request selfUpdateSettingsRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid self update settings request"})
	}
	snapshot, errSave := a.selfUpdate.SetPluginFile(request.PluginFile)
	if errSave != nil {
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": errSave.Error(), "self_update": snapshot})
	}
	return jsonResponse(http.StatusOK, map[string]any{"self_update": snapshot})
}

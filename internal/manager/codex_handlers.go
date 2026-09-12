package manager

import (
	"context"
	"net/http"

	"cpa-account-config-manager/internal/cpaapi"
)

// Codex routes. Fingerprints and the global model control are privileged
// configuration, so every route requires the Management key. Responses carry only
// the editable values and counts: no credential, cookie, or request content.

type codexFingerprintUpdateRequest struct {
	Values map[string]string `json:"values"`
}

type codexFingerprintResetRequest struct {
	Keys []string `json:"keys"`
}

type codexModelsUpdateRequest struct {
	Disabled []string `json:"disabled"`
}

// handleCodexOverview reports the workspace counts and the effective mode.
func (a *App) handleCodexOverview(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	if a == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "Codex services are unavailable"})
	}
	return jsonResponse(http.StatusOK, map[string]any{"overview": a.codexOverview()})
}

// handleCodexFingerprint lists every editable field with its default.
func (a *App) handleCodexFingerprint(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	if a == nil || a.codexFingerprints == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "Codex fingerprint service is unavailable"})
	}
	return jsonResponse(http.StatusOK, map[string]any{"profile": a.codexFingerprints.Snapshot()})
}

// handleCodexFingerprintUpdate applies the supplied field values. An empty value
// clears the override, which is how the UI restores a single field.
func (a *App) handleCodexFingerprintUpdate(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	if a == nil || a.codexFingerprints == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "Codex fingerprint service is unavailable"})
	}
	var request codexFingerprintUpdateRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid Codex fingerprint request"})
	}
	profile, errUpdate := a.codexFingerprints.Set(request.Values)
	if errUpdate != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errUpdate.Error(), "profile": profile})
	}
	return jsonResponse(http.StatusOK, map[string]any{"profile": profile})
}

// handleCodexFingerprintReset restores the listed fields, or every field when the
// list is empty.
func (a *App) handleCodexFingerprintReset(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	if a == nil || a.codexFingerprints == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "Codex fingerprint service is unavailable"})
	}
	var request codexFingerprintResetRequest
	if len(req.Body) > 0 {
		if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid Codex fingerprint reset request"})
		}
	}
	profile, errReset := a.codexFingerprints.Reset(request.Keys)
	if errReset != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errReset.Error(), "profile": profile})
	}
	return jsonResponse(http.StatusOK, map[string]any{"profile": profile})
}

// handleCodexModels lists the known Codex models with the disabled state. The
// channel scan is refreshed here so the list reflects the live CPA channels.
func (a *App) handleCodexModels(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	if a == nil || a.codexModelControl == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "Codex model control is unavailable"})
	}
	a.refreshCodexChannelModels(ctx, managementKey)
	snapshot := a.codexModelControl.Snapshot()
	snapshot.Models = a.codexModelControlRows()
	snapshot.PricingUpdatedAt, snapshot.PricingSource = a.codexPricingProvenance()
	return jsonResponse(http.StatusOK, map[string]any{"models": snapshot.Models, "disabled": snapshot.Disabled, "storage_error": snapshot.StorageError, "pricing_source": snapshot.PricingSource, "pricing_updated_at": snapshot.PricingUpdatedAt})
}

// handleCodexModelsUpdate replaces the globally disabled set. The change applies
// to every Codex account and AI-provider channel on the next request.
func (a *App) handleCodexModelsUpdate(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	if a == nil || a.codexModelControl == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "Codex model control is unavailable"})
	}
	var request codexModelsUpdateRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid Codex model request"})
	}
	snapshot, errUpdate := a.codexModelControl.Set(request.Disabled)
	if errUpdate != nil {
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": errUpdate.Error()})
	}
	a.refreshCodexChannelModels(ctx, managementKey)
	snapshot.Models = a.codexModelControlRows()
	snapshot.PricingUpdatedAt, snapshot.PricingSource = a.codexPricingProvenance()
	return jsonResponse(http.StatusOK, map[string]any{"models": snapshot.Models, "disabled": snapshot.Disabled, "storage_error": snapshot.StorageError, "pricing_source": snapshot.PricingSource, "pricing_updated_at": snapshot.PricingUpdatedAt})
}

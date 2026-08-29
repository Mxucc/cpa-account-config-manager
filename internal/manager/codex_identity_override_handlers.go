package manager

import (
	"errors"
	"net/http"
	"strings"

	"cpa-account-config-manager/internal/cpaapi"
)

type accountCodexIdentityOverrideRequest struct {
	AccountID string                `json:"account_id"`
	Override  CodexIdentityOverride `json:"override"`
}

type providerCodexIdentityOverrideRequest struct {
	ProviderKey string                `json:"provider_key"`
	Override    CodexIdentityOverride `json:"override"`
}

func (a *App) handleCodexIdentityOverrides(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.codexIdentityOverrides == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "Codex identity override service is unavailable"})
	}
	path := normalizedRequestPath(req.Path)
	switch {
	case req.Method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/codex-identity-overrides":
		return jsonResponse(http.StatusOK, a.codexIdentityOverrides.Snapshot())
	case req.Method == http.MethodPut && path == "/v0/management"+managementRoutePrefix+"/codex-identity-overrides/account":
		var request accountCodexIdentityOverrideRequest
		if err := decodeJSONRequest(req.Body, &request); err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": err.Error()})
		}
		if err := a.codexIdentityOverrides.SetAccount(request.AccountID, request.Override); err != nil {
			return codexIdentityOverrideErrorResponse(err)
		}
		return jsonResponse(http.StatusOK, map[string]any{"account_id": strings.TrimSpace(request.AccountID), "override": request.Override})
	case req.Method == http.MethodPut && path == "/v0/management"+managementRoutePrefix+"/codex-identity-overrides/provider":
		var request providerCodexIdentityOverrideRequest
		if err := decodeJSONRequest(req.Body, &request); err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": err.Error()})
		}
		if err := a.codexIdentityOverrides.SetProvider(request.ProviderKey, request.Override); err != nil {
			return codexIdentityOverrideErrorResponse(err)
		}
		return jsonResponse(http.StatusOK, map[string]any{"provider_key": strings.TrimSpace(request.ProviderKey), "override": request.Override})
	default:
		return jsonResponse(http.StatusNotFound, map[string]any{"error": "Codex identity override route not found"})
	}
}

func codexIdentityOverrideErrorResponse(err error) cpaapi.ManagementResponse {
	status := http.StatusBadRequest
	if errors.Is(err, ErrCodexIdentityOverrideStorageUnavailable) {
		status = http.StatusInternalServerError
	}
	return jsonResponse(status, map[string]any{"error": err.Error()})
}

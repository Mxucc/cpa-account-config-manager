package manager

import (
	"net/http"
	"strings"

	"cpa-account-config-manager/internal/cpaapi"
)

type usageResetRequest struct {
	Scope     string `json:"scope"`
	AccountID string `json:"account_id,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Identity  string `json:"identity,omitempty"`
	Confirm   bool   `json:"confirm"`
}

func (a *App) handleUsageReset(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request usageResetRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	if !request.Confirm {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "usage reset requires explicit confirmation"})
	}
	switch strings.ToLower(strings.TrimSpace(request.Scope)) {
	case "account":
		identifier := strings.TrimSpace(request.AccountID)
		if identifier == "" || a.usage == nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": "account_id is required"})
		}
		a.usage.Reset(identifier)
	case "provider":
		provider := strings.TrimSpace(request.Provider)
		identity := strings.TrimSpace(request.Identity)
		if provider == "" || identity == "" || a.providerRuntime == nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": "provider and identity are required"})
		}
		a.providerRuntime.Reset(provider, identity)
	default:
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "scope must be account or provider"})
	}
	return jsonResponse(http.StatusOK, map[string]any{"reset": true})
}

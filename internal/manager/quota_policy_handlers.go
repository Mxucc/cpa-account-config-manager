package manager

import (
	"errors"
	"net/http"
	"strings"

	"cpa-account-config-manager/internal/cpaapi"
)

type accountQuotaPolicyRequest struct {
	AccountID string             `json:"account_id"`
	Policy    AccountQuotaPolicy `json:"policy"`
}

type providerQuotaPolicyRequest struct {
	Policy ProviderQuotaPolicy `json:"policy"`
}

func (a *App) handleQuotaPolicies(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.quotaPolicies == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "quota policy service is unavailable"})
	}
	path := normalizedRequestPath(req.Path)
	switch {
	case req.Method == http.MethodGet && strings.HasSuffix(path, "/quota-policies"):
		return jsonResponse(http.StatusOK, a.quotaPolicies.Snapshot())
	case req.Method == http.MethodPut && strings.HasSuffix(path, "/quota-policies/account"):
		var request accountQuotaPolicyRequest
		if err := decodeJSONRequest(req.Body, &request); err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": err.Error()})
		}
		if err := a.quotaPolicies.SetAccountPolicy(request.AccountID, request.Policy); err != nil {
			return quotaPolicyErrorResponse(err)
		}
		return jsonResponse(http.StatusOK, map[string]any{"account_id": strings.TrimSpace(request.AccountID), "policy": request.Policy})
	case req.Method == http.MethodPut && strings.HasSuffix(path, "/quota-policies/provider"):
		var request providerQuotaPolicyRequest
		if err := decodeJSONRequest(req.Body, &request); err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": err.Error()})
		}
		if err := a.quotaPolicies.SetProviderPolicy(request.Policy); err != nil {
			return quotaPolicyErrorResponse(err)
		}
		return jsonResponse(http.StatusOK, request.Policy)
	default:
		return jsonResponse(http.StatusNotFound, map[string]any{"error": "quota policy route not found"})
	}
}

func quotaPolicyErrorResponse(err error) cpaapi.ManagementResponse {
	status := http.StatusBadRequest
	if errors.Is(err, ErrQuotaPolicyStorageUnavailable) {
		status = http.StatusInternalServerError
	}
	return jsonResponse(status, map[string]any{"error": err.Error()})
}

package manager

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"cpa-account-config-manager/internal/cpaapi"
)

var errAIProviderNameEntryStale = errors.New("AI provider channel entry changed; refresh and retry")

type aiProviderNameUpdateRequest struct {
	Kind    string `json:"kind"`
	Index   int    `json:"index"`
	BaseURL string `json:"base_url"`
	Name    string `json:"name"`
}

// handleAIProviderNames serves the plugin-stored AI provider labels. Every read
// revalidates the stored digest against CPA's live channel list, so a label is
// only reported while its base URL and credential still match.
func (a *App) handleAIProviderNames(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.aiProviderNames == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "AI provider name service is unavailable"})
	}
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	managementKey := resolveManagementKey(req.Headers)
	path := normalizedRequestPath(req.Path)
	switch {
	case req.Method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/ai-provider-names":
		assignments, storageError := a.resolveAIProviderChannelNames(ctx, managementKey)
		snapshot := AIProviderNameSnapshot{Names: assignments}
		if storageError != "" {
			snapshot.StorageError = storageError
		}
		return jsonResponse(http.StatusOK, snapshot)
	case req.Method == http.MethodPut && path == "/v0/management"+managementRoutePrefix+"/ai-provider-names":
		var request aiProviderNameUpdateRequest
		if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
		}
		if !supportedAIProviderProxyKind(strings.TrimSpace(request.Kind)) {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": "AI provider channel kind is not supported"})
		}
		if request.Index < 0 {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": "channel index is required"})
		}
		errAssign := a.assignAIProviderChannelName(ctx, managementKey, strings.TrimSpace(request.Kind), request.Index, request.BaseURL, request.Name)
		if errAssign != nil {
			return aiProviderNameErrorResponse(errAssign)
		}
		return jsonResponse(http.StatusOK, map[string]any{
			"kind":     strings.TrimSpace(request.Kind),
			"index":    request.Index,
			"name":     strings.TrimSpace(request.Name),
			"resolved": true,
		})
	default:
		return jsonResponse(http.StatusNotFound, map[string]any{"error": "AI provider name route not found"})
	}
}

func aiProviderNameErrorResponse(err error) cpaapi.ManagementResponse {
	switch {
	case errors.Is(err, ErrAIProviderNameStorageUnavailable):
		return jsonResponse(http.StatusInternalServerError, map[string]any{"error": "AI provider names could not be persisted"})
	case errors.Is(err, errAIProviderNameEntryStale):
		return jsonResponse(http.StatusConflict, map[string]any{"error": errAIProviderNameEntryStale.Error()})
	default:
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": err.Error()})
	}
}

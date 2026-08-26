package manager

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"cpa-account-config-manager/internal/cpaapi"
)

type proxyProfileRequest struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	ProxyURL  string   `json:"proxy_url"`
	Note      string   `json:"note"`
	Providers []string `json:"providers"`
	Enabled   *bool    `json:"enabled"`
	Force     bool     `json:"force"`
}

func (a *App) handleProxyProfilesList(_ context.Context) cpaapi.ManagementResponse {
	if a == nil || a.proxyProfiles == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "proxy profile service is unavailable"})
	}
	return jsonResponse(http.StatusOK, map[string]any{"profiles": a.proxyProfiles.List(context.Background()), "storage_error": a.proxyProfiles.StorageError()})
}

func (a *App) handleProxyProfileCreate(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	var in proxyProfileRequest
	if err := decodeJSONRequest(req.Body, &in); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid proxy profile request"})
	}
	view, err := a.proxyProfiles.Create(in.Name, in.ProxyURL, in.Note, in.Providers, in.Enabled)
	if err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": err.Error()})
	}
	return jsonResponse(http.StatusCreated, map[string]any{"profile": view})
}

func (a *App) handleProxyProfileUpdate(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	var in proxyProfileRequest
	if err := decodeJSONRequest(req.Body, &in); err != nil || strings.TrimSpace(in.ID) == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "id and valid profile request are required"})
	}
	view, err := a.proxyProfiles.Update(in.ID, in.Name, in.ProxyURL, in.Note, in.Providers, in.Enabled)
	if err != nil {
		if errors.Is(err, ErrProxyProfileNotFound) {
			return jsonResponse(http.StatusNotFound, map[string]any{"error": err.Error()})
		}
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": err.Error()})
	}
	return jsonResponse(http.StatusOK, map[string]any{"profile": view})
}

func (a *App) handleProxyProfileDelete(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	id := strings.TrimSpace(firstQueryValue(req.Query, "id"))
	force := strings.EqualFold(firstQueryValue(req.Query, "force"), "true") || strings.EqualFold(firstQueryValue(req.Query, "force"), "1")
	if id == "" {
		var in proxyProfileRequest
		if decodeJSONRequest(req.Body, &in) == nil {
			id = strings.TrimSpace(in.ID)
			force = force || in.Force
		}
	}
	if id == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "id is required"})
	}
	if err := a.proxyProfiles.Delete(id, force); err != nil {
		switch {
		case errors.Is(err, ErrProxyProfileNotFound):
			return jsonResponse(http.StatusNotFound, map[string]any{"error": err.Error()})
		case errors.Is(err, ErrProxyProfileInUse):
			return jsonResponse(http.StatusConflict, map[string]any{"error": "proxy profile is assigned to accounts"})
		default:
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": err.Error()})
		}
	}
	return jsonResponse(http.StatusOK, map[string]any{"deleted": true})
}

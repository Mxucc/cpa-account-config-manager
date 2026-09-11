package manager

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"cpa-account-config-manager/internal/cpaapi"
)

// OpenCode model routes cover both upstream families with one contract:
//
//	POST {prefix}/opencode/models       -> refresh and return the model catalog
//	POST {prefix}/opencode/model-test   -> probe one model through the credential
//	POST {prefix}/opencode/bind         -> create or update the CPA channel so the
//	                                       models become routable through CPA
//
// Every route requires the management key and never returns a stored credential.

type openCodeModelRequest struct {
	Kind string `json:"kind"`
	// AccountID selects one bound Go workspace or Zen credential.
	AccountID      string `json:"account_id"`
	Model          string `json:"model"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func (a *App) handleOpenCodeModels(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	var request openCodeModelRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid OpenCode model request"})
	}
	if errValidate := request.validate(); errValidate != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errValidate.Error()})
	}
	kind := request.normalizedKind()
	switch kind {
	case openCodeKindGo:
		if a == nil || a.opencode == nil {
			return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "OpenCode quota service is unavailable"})
		}
		view, errRefresh := a.opencode.RefreshModels(ctx, request.AccountID, request.TimeoutSeconds)
		if errRefresh != nil {
			if view.ID == "" {
				return jsonResponse(http.StatusNotFound, map[string]any{"error": errRefresh.Error()})
			}
			// The account stays usable: the catalog error is reported on the view.
			return jsonResponse(http.StatusOK, map[string]any{"account": view})
		}
		return jsonResponse(http.StatusOK, map[string]any{"account": view})
	case openCodeKindZen:
		if a == nil || a.opencodeZen == nil {
			return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "OpenCode Zen service is unavailable"})
		}
		view, errRefresh := a.opencodeZen.RefreshModels(ctx, request.AccountID, request.TimeoutSeconds)
		if errRefresh != nil {
			if view.ID == "" {
				return jsonResponse(http.StatusNotFound, map[string]any{"error": errRefresh.Error()})
			}
			return jsonResponse(http.StatusOK, map[string]any{"account": view})
		}
		return jsonResponse(http.StatusOK, map[string]any{"account": view})
	}
	return jsonResponse(http.StatusBadRequest, map[string]any{"error": "kind must be go or zen"})
}

func (a *App) handleOpenCodeModelTest(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	var request openCodeModelRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid OpenCode model request"})
	}
	if errValidate := request.validate(); errValidate != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errValidate.Error()})
	}
	if strings.TrimSpace(request.Model) == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "model is required"})
	}
	switch request.normalizedKind() {
	case openCodeKindGo:
		if a == nil || a.opencode == nil {
			return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "OpenCode quota service is unavailable"})
		}
		result, errProbe := a.opencode.ProbeModel(ctx, request.AccountID, request.Model, request.TimeoutSeconds)
		if errProbe != nil {
			return jsonResponse(http.StatusNotFound, map[string]any{"error": errProbe.Error()})
		}
		return jsonResponse(http.StatusOK, map[string]any{"result": result})
	case openCodeKindZen:
		if a == nil || a.opencodeZen == nil {
			return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "OpenCode Zen service is unavailable"})
		}
		result, errProbe := a.opencodeZen.ProbeModel(ctx, request.AccountID, request.Model, request.TimeoutSeconds)
		if errProbe != nil {
			return jsonResponse(http.StatusNotFound, map[string]any{"error": errProbe.Error()})
		}
		return jsonResponse(http.StatusOK, map[string]any{"result": result})
	}
	return jsonResponse(http.StatusBadRequest, map[string]any{"error": "kind must be go or zen"})
}

// handleOpenCodeBind makes an OpenCode credential routable through CPA by writing
// one OpenAI-compatible channel that points at the OpenCode gateway.
func (a *App) handleOpenCodeBind(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	var request openCodeModelRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid OpenCode bind request"})
	}
	if errValidate := request.validate(); errValidate != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errValidate.Error()})
	}
	credential := OpenCodeGoCredential{}
	label := openCodeBoundChannelName
	switch request.normalizedKind() {
	case openCodeKindGo:
		if a == nil || a.opencode == nil {
			return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "OpenCode quota service is unavailable"})
		}
		resolved, errCredential := a.opencode.accountCredential(request.AccountID)
		if errCredential != nil {
			return jsonResponse(http.StatusNotFound, map[string]any{"error": errCredential.Error()})
		}
		credential = resolved
		label = openCodeBoundChannelName + " Go"
		if workspace := strings.TrimSpace(resolved.WorkspaceID); workspace != "" {
			label = label + " " + workspace
		}
	case openCodeKindZen:
		if a == nil || a.opencodeZen == nil {
			return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "OpenCode Zen service is unavailable"})
		}
		resolved, errCredential := a.opencodeZen.credential(request.AccountID)
		if errCredential != nil {
			return jsonResponse(http.StatusNotFound, map[string]any{"error": errCredential.Error()})
		}
		credential = resolved
		label = openCodeBoundChannelName + " Zen"
	default:
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "kind must be go or zen"})
	}
	if strings.TrimSpace(credential.APIKey) == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "store the OpenCode API key before binding"})
	}
	models := a.openCodeCatalogFor(ctx, request.normalizedKind(), request.AccountID)
	result, errBind := a.bindOpenCodeChannel(ctx, managementKey, credential.BaseURL, credential.APIKey, label, models)
	if errBind != nil {
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": errBind.Error()})
	}
	return jsonResponse(http.StatusOK, map[string]any{"binding": result})
}

const (
	openCodeKindGo  = "go"
	openCodeKindZen = "zen"
)

func (r openCodeModelRequest) normalizedKind() string {
	switch strings.ToLower(strings.TrimSpace(r.Kind)) {
	case openCodeKindZen:
		return openCodeKindZen
	default:
		return openCodeKindGo
	}
}

func (r openCodeModelRequest) validate() error {
	if strings.TrimSpace(r.AccountID) == "" {
		return errors.New("account_id is required")
	}
	return nil
}

// openCodeBindCatalogTimeoutSeconds bounds the fallback catalog fetch that runs
// when an operator binds a credential before loading its models.
const openCodeBindCatalogTimeoutSeconds = 20

// openCodeCatalogFor returns the cached model catalog for one credential and
// fetches it once when the operator binds before loading models, so the written
// CPA channel is immediately routable. A failed fetch is not fatal: the bind
// still succeeds with whatever the cache already holds.
func (a *App) openCodeCatalogFor(ctx context.Context, kind, accountID string) []string {
	if a == nil {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(kind), openCodeKindZen) {
		if a.opencodeZen == nil {
			return nil
		}
		for _, account := range a.opencodeZen.ListAccounts() {
			if account.ID == accountID && len(account.Models) > 0 {
				return account.Models
			}
		}
		view, errRefresh := a.opencodeZen.RefreshModels(ctx, accountID, openCodeBindCatalogTimeoutSeconds)
		if errRefresh != nil {
			return nil
		}
		return view.Models
	}
	if a.opencode == nil {
		return nil
	}
	if view, errView := a.opencode.accountView(accountID); errView == nil && len(view.Models) > 0 {
		return view.Models
	}
	view, errRefresh := a.opencode.RefreshModels(ctx, accountID, openCodeBindCatalogTimeoutSeconds)
	if errRefresh != nil {
		return nil
	}
	return view.Models
}

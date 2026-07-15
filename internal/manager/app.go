package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

const (
	PluginID   = "cpa-account-config-manager"
	PluginName = "CPA Account Config Manager"

	managementRoutePrefix = "/plugins/" + PluginID
	resourceRoutePrefix   = "/v0/resource/plugins/" + PluginID
)

var (
	PluginVersion    = "0.1.0-dev"
	PluginRepository = ""
)

type Registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      cpaapi.Metadata          `json:"metadata"`
	Capabilities  RegistrationCapabilities `json:"capabilities"`
}

type RegistrationCapabilities struct {
	ManagementAPI bool `json:"management_api"`
}

type App struct {
	mu        sync.RWMutex
	config    Config
	accounts  *AccountService
	previews  *PreviewService
	jobs      *JobEngine
	indexHTML []byte
}

func NewApp(host AuthHost, indexHTML []byte) *App {
	accounts := NewAccountService(host)
	return &App{
		config:    normalizeConfig(Config{}),
		accounts:  accounts,
		previews:  NewPreviewService(accounts),
		jobs:      NewJobEngine(accounts),
		indexHTML: append([]byte(nil), indexHTML...),
	}
}

func (a *App) Configure(raw []byte) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.config = ParseConfig(raw)
	config := a.config
	a.mu.Unlock()
	a.jobs.Configure(config)
}

func (a *App) Close() {
	if a == nil || a.jobs == nil {
		return
	}
	a.jobs.Shutdown()
	a.previews.Clear()
}

func (a *App) Registration() Registration {
	return Registration{
		SchemaVersion: cpaapi.SchemaVersion,
		Metadata: cpaapi.Metadata{
			Name:             PluginName,
			Version:          PluginVersion,
			Author:           "cpa-account-config-manager contributors",
			GitHubRepository: PluginRepository,
			ConfigFields: []cpaapi.ConfigField{
				{Name: "workers", Type: cpaapi.ConfigFieldTypeInteger, Description: "Maximum concurrent account mutations (1-16)."},
				{Name: "data_dir", Type: cpaapi.ConfigFieldTypeString, Description: "Directory for sanitized terminal job state."},
				{Name: "management_base_url", Type: cpaapi.ConfigFieldTypeString, Description: "Optional loopback CLIProxyAPI Management API base URL."},
			},
		},
		Capabilities: RegistrationCapabilities{ManagementAPI: true},
	}
}

func (a *App) ManagementRegistration() cpaapi.ManagementRegistrationResponse {
	return cpaapi.ManagementRegistrationResponse{
		Routes: []cpaapi.ManagementRoute{
			{Method: http.MethodGet, Path: managementRoutePrefix + "/accounts", Description: "List redacted CLIProxyAPI accounts."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/batch/preview", Description: "Preview a batch account configuration patch."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/batch/start", Description: "Start an approved batch account configuration patch."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/batch/status", Description: "Read current or last batch progress."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/batch/retry", Description: "Retry the failed subset of the last in-memory batch."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/export/accounts", Description: "Export a redacted filtered account view."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/export/results", Description: "Export sanitized batch results."},
		},
		Resources: []cpaapi.ResourceRoute{{
			Path:        "/index.html",
			Menu:        "Account Config Manager",
			Description: "List, filter, and safely batch-edit CLIProxyAPI account configuration.",
		}},
	}
}

func (a *App) HandleManagement(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}
	path := normalizedRequestPath(req.Path)

	switch {
	case method == http.MethodGet && path == resourceRoutePrefix+"/index.html":
		return cpaapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       append([]byte(nil), a.indexHTML...),
		}
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/accounts":
		return a.handleListAccounts(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/batch/preview":
		return a.handlePreview(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/batch/start":
		return a.handleStart(req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/batch/status":
		return jsonResponse(http.StatusOK, a.jobs.Snapshot(statusWantsResults(req.Query)))
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/batch/retry":
		return a.handleRetry(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/export/accounts":
		return a.handleExportAccounts(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/export/results":
		return jsonDownloadResponse(http.StatusOK, "cpa-account-config-results.json", a.jobs.Snapshot(true))
	default:
		return jsonResponse(http.StatusNotFound, map[string]any{
			"error":  "not found",
			"method": method,
			"path":   path,
		})
	}
}

func (a *App) handleListAccounts(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	query, errQuery := listQueryFromValues(req.Query)
	if errQuery != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errQuery.Error()})
	}
	response, errList := a.accounts.List(ctx, query)
	if errList != nil {
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": "failed to load accounts"})
	}
	return jsonResponse(http.StatusOK, response)
}

func (a *App) handlePreview(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request PreviewRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	preview, errPreview := a.previews.Create(ctx, request)
	if errPreview != nil {
		if strings.Contains(errPreview.Error(), "resolve target accounts") || strings.Contains(errPreview.Error(), "account service is unavailable") {
			return jsonResponse(http.StatusBadGateway, map[string]any{"error": "failed to resolve target accounts"})
		}
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errPreview.Error()})
	}
	return jsonResponse(http.StatusOK, preview)
}

func (a *App) handleStart(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request StartRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	preview, errPreview := a.previews.Get(request.PreviewID)
	if errPreview != nil {
		switch {
		case errors.Is(errPreview, ErrPreviewExpired):
			return jsonResponse(http.StatusGone, map[string]any{"error": "preview expired; create a new preview"})
		default:
			return jsonResponse(http.StatusNotFound, map[string]any{"error": "preview not found"})
		}
	}
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	snapshot, errStart := a.jobs.Start(preview, managementKey, "")
	managementKey = ""
	if errStart != nil {
		status := jobHTTPStatus(errStart)
		message := errStart.Error()
		if status == http.StatusInternalServerError {
			message = "failed to start batch job"
		}
		return jsonResponse(status, map[string]any{"error": message})
	}
	a.previews.Delete(request.PreviewID)
	return jsonResponse(http.StatusAccepted, snapshot)
}

func (a *App) handleRetry(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	scope, patch, parentJobID, errIntent := a.jobs.RetryIntent()
	if errIntent != nil {
		return jsonResponse(jobHTTPStatus(errIntent), map[string]any{"error": errIntent.Error()})
	}
	preview, errPreview := a.previews.BuildTransient(ctx, scope, patch)
	if errPreview != nil {
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": "failed to refresh failed targets"})
	}
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	snapshot, errStart := a.jobs.Start(preview, managementKey, parentJobID)
	managementKey = ""
	if errStart != nil {
		status := jobHTTPStatus(errStart)
		message := errStart.Error()
		if status == http.StatusInternalServerError {
			message = "failed to start retry job"
		}
		return jsonResponse(status, map[string]any{"error": message})
	}
	return jsonResponse(http.StatusAccepted, snapshot)
}

func (a *App) handleExportAccounts(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	query, errQuery := listQueryFromValues(req.Query)
	if errQuery != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errQuery.Error()})
	}
	accounts, errExport := a.accounts.Export(ctx, query.Filters)
	if errExport != nil {
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": "failed to export accounts"})
	}
	payload := struct {
		ExportedAt time.Time      `json:"exported_at"`
		Filters    AccountFilters `json:"filters"`
		Total      int            `json:"total"`
		Accounts   []Account      `json:"accounts"`
	}{
		ExportedAt: time.Now().UTC(),
		Filters:    query.Filters,
		Total:      len(accounts),
		Accounts:   accounts,
	}
	return jsonDownloadResponse(http.StatusOK, "cpa-account-config-accounts.json", payload)
}

func listQueryFromValues(values map[string][]string) (ListQuery, error) {
	query := ListQuery{}
	query.Page = intQuery(values, "page", 1)
	query.PageSize = intQuery(values, "page_size", defaultPageSize)
	query.Filters.Provider = firstQuery(values, "provider")
	query.Filters.Type = firstQuery(values, "type")
	query.Filters.Status = firstQuery(values, "status")
	query.Filters.Editability = firstQuery(values, "editability")
	query.Filters.Source = firstQuery(values, "source")
	query.Filters.Search = firstQuery(values, "search")
	if rawDisabled := firstQuery(values, "disabled"); rawDisabled != "" {
		disabled, errParse := strconv.ParseBool(rawDisabled)
		if errParse != nil {
			return ListQuery{}, fmt.Errorf("disabled must be true or false")
		}
		query.Filters.Disabled = &disabled
	}
	return query, nil
}

func firstQuery(values map[string][]string, key string) string {
	items := values[key]
	if len(items) == 0 {
		return ""
	}
	return strings.TrimSpace(items[0])
}

func intQuery(values map[string][]string, key string, fallback int) int {
	raw := firstQuery(values, key)
	if raw == "" {
		return fallback
	}
	parsed, errParse := strconv.Atoi(raw)
	if errParse != nil {
		return fallback
	}
	return parsed
}

func normalizedRequestPath(path string) string {
	path = strings.TrimRight(strings.TrimSpace(path), "/")
	if index := strings.IndexByte(path, '?'); index >= 0 {
		path = path[:index]
	}
	if strings.HasPrefix(path, managementRoutePrefix+"/") {
		return "/v0/management" + path
	}
	return path
}

func jsonResponse(statusCode int, payload any) cpaapi.ManagementResponse {
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		statusCode = http.StatusInternalServerError
		raw = []byte(`{"error":"failed to encode response"}`)
	}
	return cpaapi.ManagementResponse{
		StatusCode: statusCode,
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       raw,
	}
}

func jsonDownloadResponse(statusCode int, filename string, payload any) cpaapi.ManagementResponse {
	response := jsonResponse(statusCode, payload)
	if response.Headers == nil {
		response.Headers = make(http.Header)
	}
	response.Headers.Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	return response
}

func decodeJSONRequest(raw []byte, destination any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return fmt.Errorf("request body is required")
	}
	if len(raw) > 1<<20 {
		return fmt.Errorf("request body exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(destination); errDecode != nil {
		return fmt.Errorf("invalid request body: %w", errDecode)
	}
	var trailing any
	if errTrailing := decoder.Decode(&trailing); !errors.Is(errTrailing, io.EOF) {
		return fmt.Errorf("request body must contain one JSON object")
	}
	return nil
}

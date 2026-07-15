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
	UsagePlugin   bool `json:"usage_plugin"`
}

type App struct {
	mu        sync.RWMutex
	config    Config
	accounts  *AccountService
	previews  *PreviewService
	jobs      *JobEngine
	policies  *PolicyEngine
	force     *ForceSyncEngine
	imports   *ImportService
	usage     *UsageTracker
	indexHTML []byte
}

func NewApp(host AuthHost, indexHTML []byte) *App {
	usage := NewUsageTracker()
	accounts := NewAccountService(host, usage)
	mutations := NewMutationCoordinator()
	jobs := NewJobEngineWithCoordinator(accounts, mutations)
	policies := NewPolicyEngineWithCoordinator(host, mutations)
	return &App{
		config:    normalizeConfig(Config{}),
		accounts:  accounts,
		previews:  NewPreviewService(accounts),
		jobs:      jobs,
		policies:  policies,
		force:     NewForceSyncEngine(accounts, host, policies, mutations),
		imports:   NewImportService(host, mutations),
		usage:     usage,
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
	a.policies.Configure(config)
	a.force.Configure(config)
	a.usage.Configure(config)
}

func (a *App) HandleUsage(record cpaapi.UsageRecord) {
	if a == nil || a.usage == nil {
		return
	}
	a.usage.Observe(record)
}

func (a *App) Close() {
	if a == nil {
		return
	}
	a.force.Shutdown()
	a.policies.Shutdown()
	a.jobs.Shutdown()
	a.previews.Clear()
	a.imports.Clear()
	a.usage.Close()
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
				{Name: "workers", Type: cpaapi.ConfigFieldTypeInteger, Description: "Optional maximum concurrent account mutations (default 6, range 1-16)."},
				{Name: "data_dir", Type: cpaapi.ConfigFieldTypeString, Description: "Optional writable directory for sanitized job, default-policy, and usage snapshot state."},
				{Name: "management_base_url", Type: cpaapi.ConfigFieldTypeString, Description: "Optional loopback CLIProxyAPI Management API base URL; defaults to http://127.0.0.1:8317."},
			},
		},
		Capabilities: RegistrationCapabilities{ManagementAPI: true, UsagePlugin: true},
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
			{Method: http.MethodGet, Path: managementRoutePrefix + "/export/accounts", Description: "Export filtered account credentials for an explicitly selected target format."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/export/results", Description: "Export sanitized batch results as JSON, CSV, or JSON Lines."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/import/preview", Description: "Preview JSON or ZIP conversion into CPA Auth files."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/import/start", Description: "Import a confirmed converted Auth-file preview."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/defaults", Description: "Read the default Auth-file policy and safe scan status."},
			{Method: http.MethodPut, Path: managementRoutePrefix + "/defaults", Description: "Validate and save the default Auth-file policy."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/defaults/scan", Description: "Request an immediate missing-only Auth-file scan."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/defaults/force/preview", Description: "Preview force-syncing managed policy fields."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/defaults/force/start", Description: "Start an approved default-policy force sync."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/defaults/force/status", Description: "Read current or last force-sync progress."},
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
		return a.handleExportResults(req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/import/preview":
		return a.handleImportPreview(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/import/start":
		return a.handleImportStart(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/defaults":
		return jsonResponse(http.StatusOK, a.policies.Snapshot())
	case method == http.MethodPut && path == "/v0/management"+managementRoutePrefix+"/defaults":
		return a.handlePutDefaultPolicy(req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/defaults/scan":
		return jsonResponse(http.StatusAccepted, a.policies.RequestScan())
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/defaults/force/preview":
		return a.handleForcePreview(ctx)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/defaults/force/start":
		return a.handleForceStart(req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/defaults/force/status":
		return jsonResponse(http.StatusOK, a.force.Snapshot(statusWantsResults(req.Query)))
	default:
		return jsonResponse(http.StatusNotFound, map[string]any{
			"error":  "not found",
			"method": method,
			"path":   path,
		})
	}
}

func (a *App) handleImportPreview(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	uploads, multipartUpload, errUploads := importUploadsFromRequest(req, a.imports.limits)
	if errUploads != nil {
		status := http.StatusBadRequest
		if strings.Contains(errUploads.Error(), "exceeds") || strings.Contains(errUploads.Error(), "more than") {
			status = http.StatusRequestEntityTooLarge
		}
		return jsonResponse(status, map[string]any{"error": errUploads.Error()})
	}
	var preview ImportPreview
	var errPreview error
	if multipartUpload {
		preview, errPreview = a.imports.PreviewMany(ctx, uploads)
	} else {
		preview, errPreview = a.imports.Preview(ctx, uploads[0])
	}
	if errPreview != nil {
		status := http.StatusBadRequest
		switch {
		case errors.Is(errPreview, ErrImportNoAccounts):
			status = http.StatusUnprocessableEntity
		case errors.Is(errPreview, ErrImportAuthUnavailable):
			status = http.StatusBadGateway
		case strings.Contains(errPreview.Error(), "exceeds") || strings.Contains(errPreview.Error(), "more than"):
			status = http.StatusRequestEntityTooLarge
		}
		return jsonResponse(status, map[string]any{"error": errPreview.Error()})
	}
	return jsonResponse(http.StatusOK, preview)
}

func (a *App) handleImportStart(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request ImportStartRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	result, errStart := a.imports.Start(ctx, request.PreviewID)
	if errStart != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(errStart, ErrImportPreviewExpired):
			status = http.StatusGone
		case errors.Is(errStart, ErrImportPreviewNotFound):
			status = http.StatusNotFound
		case errors.Is(errStart, ErrJobBusy):
			status = http.StatusConflict
		case errors.Is(errStart, ErrImportAuthUnavailable):
			status = http.StatusBadGateway
		}
		return jsonResponse(status, map[string]any{"error": errStart.Error()})
	}
	return jsonResponse(http.StatusOK, result)
}

func (a *App) handlePutDefaultPolicy(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var policy DefaultPolicy
	if errDecode := decodeJSONRequest(req.Body, &policy); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	saved, errSave := a.policies.SetPolicy(policy)
	if errSave != nil {
		if errors.Is(errSave, ErrPolicyStorageUnavailable) {
			return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": ErrPolicyStorageUnavailable.Error()})
		}
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errSave.Error()})
	}
	snapshot := a.policies.Snapshot()
	snapshot.Policy = saved
	return jsonResponse(http.StatusOK, snapshot)
}

func (a *App) handleForcePreview(ctx context.Context) cpaapi.ManagementResponse {
	preview, errPreview := a.force.Preview(ctx)
	if errPreview != nil {
		switch {
		case errors.Is(errPreview, ErrForcePolicyEmpty):
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": ErrForcePolicyEmpty.Error()})
		case strings.Contains(errPreview.Error(), "resolve force-sync targets"):
			return jsonResponse(http.StatusBadGateway, map[string]any{"error": "failed to resolve force-sync targets"})
		default:
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": "no auth files are available for force sync"})
		}
	}
	return jsonResponse(http.StatusOK, preview)
}

func (a *App) handleForceStart(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request StartRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	snapshot, errStart := a.force.Start(request.PreviewID)
	if errStart != nil {
		switch {
		case errors.Is(errStart, ErrForcePreviewExpired):
			return jsonResponse(http.StatusGone, map[string]any{"error": ErrForcePreviewExpired.Error()})
		case errors.Is(errStart, ErrForcePreviewNotFound):
			return jsonResponse(http.StatusNotFound, map[string]any{"error": ErrForcePreviewNotFound.Error()})
		case errors.Is(errStart, ErrForcePreviewStale), errors.Is(errStart, ErrJobBusy):
			return jsonResponse(http.StatusConflict, map[string]any{"error": errStart.Error()})
		case errors.Is(errStart, ErrForceNoEligible):
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": ErrForceNoEligible.Error()})
		default:
			return jsonResponse(http.StatusInternalServerError, map[string]any{"error": "failed to start force-sync job"})
		}
	}
	return jsonResponse(http.StatusAccepted, snapshot)
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
		return jsonResponse(status, map[string]any{"error": publicJobStartError(errStart, "failed to start batch job")})
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
		return jsonResponse(status, map[string]any{"error": publicJobStartError(errStart, "failed to start retry job")})
	}
	return jsonResponse(http.StatusAccepted, snapshot)
}

func (a *App) handleExportAccounts(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	format, errFormat := credentialExportFormatFromValues(req.Query)
	if errFormat != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errFormat.Error()})
	}
	query, errQuery := listQueryFromValues(req.Query)
	if errQuery != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errQuery.Error()})
	}
	collection, errExport := a.accounts.ExportCredentialSources(ctx, query.Filters)
	if errExport != nil {
		if errors.Is(errExport, ErrCredentialExportTooLarge) {
			return jsonResponse(http.StatusRequestEntityTooLarge, map[string]any{"error": ErrCredentialExportTooLarge.Error()})
		}
		if errors.Is(errExport, ErrCredentialExportNoAccounts) {
			return jsonResponse(http.StatusUnprocessableEntity, map[string]any{"error": ErrCredentialExportNoAccounts.Error()})
		}
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": "failed to export accounts"})
	}
	defer clearCredentialExportCollection(&collection)
	download, errRender := renderCredentialExport(format, collection, time.Now().UTC())
	if errRender != nil {
		if errors.Is(errRender, ErrCredentialExportTooLarge) {
			return jsonResponse(http.StatusRequestEntityTooLarge, map[string]any{"error": ErrCredentialExportTooLarge.Error()})
		}
		if errors.Is(errRender, ErrCredentialExportNoCompatible) {
			return jsonResponse(http.StatusUnprocessableEntity, map[string]any{"error": ErrCredentialExportNoCompatible.Error()})
		}
		return jsonResponse(http.StatusInternalServerError, map[string]any{"error": "failed to encode account export"})
	}
	return exportDownloadResponse(download)
}

func (a *App) handleExportResults(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	format, errFormat := resultExportFormatFromValues(req.Query)
	if errFormat != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errFormat.Error()})
	}
	download, errRender := renderResultExport(format, a.jobs.Snapshot(true))
	if errRender != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"error": "failed to encode result export"})
	}
	return exportDownloadResponse(download)
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

func publicJobStartError(err error, fallback string) string {
	switch {
	case errors.Is(err, ErrJobStorageUnavailable):
		return ErrJobStorageUnavailable.Error()
	case errors.Is(err, ErrManagementBaseURLInvalid):
		return ErrManagementBaseURLInvalid.Error()
	case jobHTTPStatus(err) != http.StatusInternalServerError:
		return err.Error()
	default:
		return fallback
	}
}

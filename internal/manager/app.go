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
	PluginID                = "cpa-account-config-manager"
	PluginName              = "CPA Account Config Manager"
	DefaultPluginRepository = "https://github.com/Mxucc/cpa-account-config-manager"

	managementRoutePrefix = "/plugins/" + PluginID
	resourceRoutePrefix   = "/v0/resource/plugins/" + PluginID
)

var (
	PluginVersion    = "0.2.9"
	PluginRepository = DefaultPluginRepository
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
	mu         sync.RWMutex
	config     Config
	accounts   *AccountService
	deletions  *AccountDeleteService
	previews   *PreviewService
	jobs       *JobEngine
	policies   *PolicyEngine
	inspection *InspectionEngine
	updates    *UpdateChecker
	force      *ForceSyncEngine
	imports    *ImportService
	usage      *UsageTracker
	operations *OperationJournal
	modelTests *ModelTestService
	indexHTML  []byte
}

func NewApp(host AuthHost, indexHTML []byte) *App {
	usage := NewUsageTracker()
	accounts := NewAccountService(host, usage)
	mutations := NewMutationCoordinator()
	jobs := NewJobEngineWithCoordinator(accounts, mutations)
	policies := NewPolicyEngineWithCoordinator(host, mutations)
	inspection := NewInspectionEngine(accounts, host, mutations)
	modelTests := NewModelTestService(accounts)
	deletions := NewAccountDeleteService(accounts, mutations)
	inspection.SetModelTestService(modelTests)
	inspection.SetDeleteService(deletions)
	return &App{
		config:     normalizeConfig(Config{}),
		accounts:   accounts,
		deletions:  deletions,
		previews:   NewPreviewService(accounts),
		jobs:       jobs,
		policies:   policies,
		inspection: inspection,
		updates:    NewUpdateChecker(PluginVersion),
		force:      NewForceSyncEngine(accounts, host, policies, mutations),
		imports:    NewImportService(host, mutations),
		usage:      usage,
		operations: NewOperationJournal(),
		modelTests: modelTests,
		indexHTML:  append([]byte(nil), indexHTML...),
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
	a.operations.Configure(config)
	a.jobs.Configure(config)
	a.policies.Configure(config)
	a.inspection.Configure(config)
	a.updates.Configure(config)
	a.force.Configure(config)
	a.usage.Configure(config)
	a.reconcileOperationSources()
}

func (a *App) configSnapshot() Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.config
}

func (a *App) HandleUsage(record cpaapi.UsageRecord) {
	if a == nil || a.usage == nil {
		return
	}
	a.usage.Observe(record)
	a.inspection.Observe(record)
}

func (a *App) Close() {
	if a == nil {
		return
	}
	a.force.Shutdown()
	a.inspection.Shutdown()
	a.updates.Shutdown()
	a.policies.Shutdown()
	a.jobs.Shutdown()
	a.deletions.Clear()
	a.previews.Clear()
	a.imports.Clear()
	a.usage.Close()
	a.reconcileOperationSources()
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
				{Name: "data_dir", Type: cpaapi.ConfigFieldTypeString, Description: "Optional writable directory for sanitized job, policy, usage, inspection, update, and operation-journal state."},
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
			{Method: http.MethodPost, Path: managementRoutePrefix + "/accounts/model-test", Description: "Run one bounded account-specific model availability probe through CLIProxyAPI."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/accounts/delete/preview", Description: "Preview deletion of one editable physical Auth file."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/accounts/delete/start", Description: "Delete one confirmed unchanged physical Auth file."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/batch/preview", Description: "Preview a batch account configuration patch."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/batch/start", Description: "Start an approved batch account configuration patch."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/batch/status", Description: "Read current or last batch progress."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/batch/retry", Description: "Retry the failed subset of the last in-memory batch."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/export/accounts", Description: "Export filtered account credentials for an explicitly selected target format."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/export/accounts", Description: "Export selected account credentials for an explicitly selected target format."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/export/results", Description: "Export sanitized batch results as JSON, CSV, or JSON Lines."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/import/preview", Description: "Preview JSON or ZIP conversion into CPA Auth files."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/import/start", Description: "Import a confirmed converted Auth-file preview."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/defaults", Description: "Read the default Auth-file policy and safe scan status."},
			{Method: http.MethodPut, Path: managementRoutePrefix + "/defaults", Description: "Validate and save the default Auth-file policy."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/defaults/scan", Description: "Request an immediate missing-only Auth-file scan."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/defaults/force/preview", Description: "Preview force-syncing managed policy fields."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/defaults/force/start", Description: "Start an approved default-policy force sync."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/defaults/force/status", Description: "Read current or last force-sync progress."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/inspection", Description: "Read the persistent account inspection policy and scan status."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/inspection/live", Description: "Read uncached live inspection progress and newly completed account results."},
			{Method: http.MethodPut, Path: managementRoutePrefix + "/inspection", Description: "Validate and save the account inspection policy."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/inspection/scan", Description: "Request an immediate account inspection scan."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/inspection/scan/native", Description: "Request an immediate full CPA-native account census without model probes."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/inspection/run", Description: "Start a full, incremental, retry, or scoped active inspection."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/inspection/stop", Description: "Stop the current manual active inspection."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/inspection/results", Description: "List redacted account inspection results."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/inspection/export", Description: "Export filtered sanitized inspection results as JSON, CSV, or JSON Lines."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/inspection/review", Description: "Resolve, ignore, or reopen one sanitized inspection review result."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/inspection/actions", Description: "List sanitized automatic action history."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/inspection/delete", Description: "Delete explicitly confirmed high-confidence inspection recommendations after physical revision revalidation."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/inspection/auto-delete", Description: "Execute due opt-in deletion candidates with the current Management credential."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/updates", Description: "Read plugin release and update-check status."},
			{Method: http.MethodPut, Path: managementRoutePrefix + "/updates", Description: "Validate and save plugin update-check settings."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/updates/check", Description: "Record an immediate CPA plugin-store update check."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/operations", Description: "List the persistent sanitized account-manager operation journal."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/operations/export", Description: "Export the sanitized operation journal as JSON, CSV, or JSON Lines."},
			{Method: http.MethodDelete, Path: managementRoutePrefix + "/operations", Description: "Clear the operation journal while retaining a clear audit event."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/operations/record", Description: "Record a strict browser-owned plugin-store update outcome."},
		},
		Resources: []cpaapi.ResourceRoute{{
			Path:        "/index.html",
			Menu:        "CPA-A Config",
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
	if strings.HasPrefix(path, "/v0/management"+managementRoutePrefix) {
		a.reconcileOperationSources()
	}

	switch {
	case method == http.MethodGet && path == resourceRoutePrefix+"/index.html":
		return cpaapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       append([]byte(nil), a.indexHTML...),
		}
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/accounts":
		return a.handleListAccounts(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/accounts/model-test":
		return a.handleAccountModelTest(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/accounts/delete/preview":
		return a.handleAccountDeletePreview(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/accounts/delete/start":
		return a.handleAccountDeleteStart(ctx, req)
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
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/export/accounts":
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
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/inspection":
		inspectionSnapshot := a.inspection.Snapshot()
		inspectionPolicy := inspectionSnapshot.Policy
		if inspectionPolicy.ModelProbeEnabled || inspectionPolicy.AnomalyTriggerEnabled || inspectionPolicy.AutoDisable || inspectionPolicy.AutoEnable || inspectionPolicy.AutoDelete || inspectionSnapshot.ProbeSweepRemaining > 0 {
			managementKey := resolveManagementKey(req.Headers)
			if managementKey != "" {
				a.inspection.ArmModelProbes(managementKey)
				managementKey = ""
			}
		}
		return jsonResponse(http.StatusOK, a.inspection.Snapshot())
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/inspection/live":
		managementKey := resolveManagementKey(req.Headers)
		if managementKey != "" {
			a.inspection.ArmModelProbes(managementKey)
			managementKey = ""
		}
		response := jsonResponse(http.StatusOK, a.inspection.Snapshot())
		response.Headers.Set("Cache-Control", "no-store")
		return response
	case method == http.MethodPut && path == "/v0/management"+managementRoutePrefix+"/inspection":
		return a.handlePutInspectionPolicy(req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/inspection/scan":
		managementKey := resolveManagementKey(req.Headers)
		snapshot := a.inspection.RequestScanWithModelProbes(managementKey)
		managementKey = ""
		return jsonResponse(http.StatusAccepted, snapshot)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/inspection/scan/native":
		managementKey := resolveManagementKey(req.Headers)
		if managementKey != "" {
			a.inspection.ArmModelProbes(managementKey)
			managementKey = ""
		}
		return jsonResponse(http.StatusAccepted, a.inspection.RequestScan())
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/inspection/run":
		return a.handleInspectionRun(req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/inspection/stop":
		return jsonResponse(http.StatusAccepted, a.inspection.StopRun())
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/inspection/results":
		return a.handleListInspectionResults(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/inspection/export":
		return a.handleExportInspection(req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/inspection/review":
		return a.handleInspectionReview(req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/inspection/actions":
		return a.handleListInspectionActions(req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/inspection/delete":
		return a.handleInspectionManualDelete(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/inspection/auto-delete":
		return a.handleInspectionAutoDelete(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/updates":
		return jsonResponse(http.StatusOK, a.updates.Snapshot())
	case method == http.MethodPut && path == "/v0/management"+managementRoutePrefix+"/updates":
		return a.handlePutUpdatePolicy(req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/updates/check":
		return jsonResponse(http.StatusAccepted, a.updates.RequestCheck())
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/operations":
		return a.handleListOperations(req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/operations/export":
		return a.handleExportOperations(req)
	case method == http.MethodDelete && path == "/v0/management"+managementRoutePrefix+"/operations":
		return a.handleClearOperations()
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/operations/record":
		return a.handleRecordOperation(req)
	default:
		return jsonResponse(http.StatusNotFound, map[string]any{
			"error":  "not found",
			"method": method,
			"path":   path,
		})
	}
}

func (a *App) handleExportInspection(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	format := firstQuery(req.Query, "format")
	if format == "" {
		format = "json"
	}
	query := InspectionResultQuery{Page: 1, PageSize: maxInspectionResultPageSize, Health: firstQuery(req.Query, "health"), Search: firstQuery(req.Query, "search")}
	response := a.inspection.ListResults(query)
	results := append([]InspectionResult(nil), response.Results...)
	for page := 2; page <= response.Pages && len(results) < maxInspectionAccounts; page++ {
		query.Page = page
		results = append(results, a.inspection.ListResults(query).Results...)
	}
	download, errRender := renderInspectionExport(format, results, time.Now().UTC())
	if errRender != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errRender.Error()})
	}
	return cpaapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":                  []string{download.ContentType},
			"Content-Disposition":           []string{fmt.Sprintf(`attachment; filename="%s"`, download.Filename)},
			"X-Exported-Inspection-Results": []string{strconv.Itoa(download.Count)},
		},
		Body: download.Body,
	}
}

func (a *App) handleListOperations(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	query := operationQueryFromRequest(req, 50)
	return jsonResponse(http.StatusOK, a.operations.List(query))
}

func (a *App) handleExportOperations(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	format := firstQuery(req.Query, "format")
	if format == "" {
		format = "json"
	}
	query := operationQueryFromRequest(req, maxOperationEntries)
	query.Page = 1
	query.PageSize = maxOperationPageSize
	response := a.operations.List(query)
	entries := append([]OperationEntry(nil), response.Operations...)
	for page := 2; page <= response.Pages; page++ {
		query.Page = page
		entries = append(entries, a.operations.List(query).Operations...)
	}
	download, errRender := renderOperationExport(format, entries, time.Now().UTC())
	if errRender != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errRender.Error()})
	}
	return cpaapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":          []string{download.ContentType},
			"Content-Disposition":   []string{fmt.Sprintf(`attachment; filename="%s"`, download.Filename)},
			"X-Exported-Operations": []string{strconv.Itoa(download.Count)},
		},
		Body: download.Body,
	}
}

func operationQueryFromRequest(req cpaapi.ManagementRequest, pageSize int) OperationQuery {
	return OperationQuery{
		Page:     intQuery(req.Query, "page", 1),
		PageSize: intQuery(req.Query, "page_size", pageSize),
		Category: firstQuery(req.Query, "category"),
		Status:   firstQuery(req.Query, "status"),
		Source:   firstQuery(req.Query, "source"),
		Search:   firstQuery(req.Query, "search"),
	}
}

func (a *App) handleClearOperations() cpaapi.ManagementResponse {
	entry := a.operations.Clear()
	return jsonResponse(http.StatusOK, map[string]any{"operation": entry, "retained": 1})
}

func (a *App) handleRecordOperation(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request OperationRecordRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	entry, errValidate := validateBrowserOperationRecord(request)
	if errValidate != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errValidate.Error()})
	}
	now := time.Now().UTC()
	entry.StartedAt = now
	entry.FinishedAt = now
	recorded := a.operations.Record(entry)
	return jsonResponse(http.StatusCreated, recorded)
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
	startedAt := time.Now().UTC()
	var request ImportStartRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	result, errStart := a.imports.Start(ctx, request.PreviewID)
	if errStart != nil {
		a.operations.Record(OperationEntry{
			Category: OperationCategoryImport, Action: OperationActionImport, Status: OperationStatusFailed,
			Source: OperationSourceImport, Scope: OperationScopeAll, Failed: 1, StartedAt: startedAt,
			FinishedAt: time.Now().UTC(), ReasonCode: "operation_failed",
		})
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
	a.operations.Record(OperationEntry{
		Category: OperationCategoryImport, Action: OperationActionImport, Status: operationStatusFromJobState(result.State),
		Source: OperationSourceImport, Scope: OperationScopeAll, TargetCount: result.Total, Succeeded: result.Imported,
		Failed: result.Failed, Skipped: result.Skipped, StartedAt: result.StartedAt, FinishedAt: result.FinishedAt,
		ReasonCode: operationReasonFromJobState(result.State),
	})
	return jsonResponse(http.StatusOK, result)
}

func (a *App) handlePutDefaultPolicy(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var policy DefaultPolicy
	if errDecode := decodeJSONRequest(req.Body, &policy); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	saved, errSave := a.policies.SetPolicy(policy)
	if errSave != nil {
		a.recordPolicyChange(OperationCategoryDefaultPolicy, OperationActionPolicySave, OperationSourceManual, OperationStatusFailed)
		if errors.Is(errSave, ErrPolicyStorageUnavailable) {
			return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": ErrPolicyStorageUnavailable.Error()})
		}
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errSave.Error()})
	}
	a.recordPolicyChange(OperationCategoryDefaultPolicy, OperationActionPolicySave, OperationSourceManual, OperationStatusSucceeded)
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
	a.operations.Upsert("force:"+snapshot.ID, operationFromForceSync(snapshot))
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
	summaries := a.inspection.AccountAutomationSummaries(response.Accounts)
	for index := range response.Accounts {
		if summary, exists := summaries[response.Accounts[index].ID]; exists {
			response.Accounts[index].Automation = &summary
		}
	}
	return jsonResponse(http.StatusOK, response)
}

func (a *App) handleAccountDeletePreview(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request AccountDeletePreviewRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	if strings.TrimSpace(request.ID) == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "account id is required"})
	}
	preview, errPreview := a.deletions.Preview(ctx, request)
	if errPreview != nil {
		switch {
		case errors.Is(errPreview, ErrAccountDeleteTargetNotFound):
			return jsonResponse(http.StatusNotFound, map[string]any{"error": ErrAccountDeleteTargetNotFound.Error()})
		case errors.Is(errPreview, ErrAccountDeleteTargetReadOnly):
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": ErrAccountDeleteTargetReadOnly.Error()})
		case strings.Contains(errPreview.Error(), "resolve account for deletion"):
			return jsonResponse(http.StatusBadGateway, map[string]any{"error": "failed to resolve account for deletion"})
		default:
			return jsonResponse(http.StatusInternalServerError, map[string]any{"error": "failed to create delete preview"})
		}
	}
	return jsonResponse(http.StatusOK, preview)
}

func (a *App) handleAccountDeleteStart(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request AccountDeleteStartRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	if strings.TrimSpace(request.PreviewID) == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "preview_id is required"})
	}
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	config := a.configSnapshot()
	result, errDelete := a.deletions.Start(ctx, request.PreviewID, config.ManagementBaseURL, managementKey)
	managementKey = ""
	if errDelete != nil {
		now := time.Now().UTC()
		a.operations.Record(OperationEntry{
			Category: OperationCategoryAccount, Action: OperationActionDelete, Status: OperationStatusFailed,
			Source: OperationSourceManual, Scope: OperationScopeSingle, TargetCount: 1, Failed: 1,
			StartedAt: now, FinishedAt: now, ReasonCode: "operation_failed",
		})
		switch {
		case errors.Is(errDelete, ErrAccountDeletePreviewExpired):
			return jsonResponse(http.StatusGone, map[string]any{"error": "delete preview expired; create a new preview"})
		case errors.Is(errDelete, ErrAccountDeletePreviewNotFound):
			return jsonResponse(http.StatusNotFound, map[string]any{"error": ErrAccountDeletePreviewNotFound.Error()})
		case errors.Is(errDelete, ErrAccountDeletePreviewStale), errors.Is(errDelete, ErrAccountDeleteBusy):
			return jsonResponse(http.StatusConflict, map[string]any{"error": errDelete.Error()})
		case errors.Is(errDelete, ErrManagementBaseURLInvalid):
			return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": ErrManagementBaseURLInvalid.Error()})
		case errors.Is(errDelete, ErrAccountDeleteFailed):
			return jsonResponse(http.StatusBadGateway, map[string]any{"error": ErrAccountDeleteFailed.Error()})
		default:
			return jsonResponse(http.StatusInternalServerError, map[string]any{"error": "failed to delete account"})
		}
	}
	a.operations.Record(OperationEntry{
		Category: OperationCategoryAccount, Action: OperationActionDelete, Status: OperationStatusSucceeded,
		Source: OperationSourceManual, Scope: OperationScopeSingle, TargetID: result.Account.ID, TargetCount: 1,
		Succeeded: 1, StartedAt: result.DeletedAt, FinishedAt: result.DeletedAt, ReasonCode: "completed",
	})
	return jsonResponse(http.StatusOK, result)
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
	entry := operationFromJob(snapshot)
	entry.Scope = normalizeOperationScope(preview.Public.ScopeMode)
	a.operations.Upsert("batch:"+snapshot.ID, entry)
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
	entry := operationFromJob(snapshot)
	entry.Scope = OperationScopeSelected
	a.operations.Upsert("batch:"+snapshot.ID, entry)
	return jsonResponse(http.StatusAccepted, snapshot)
}

func (a *App) handleExportAccounts(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	format, errFormat := credentialExportFormatFromValues(req.Query)
	if errFormat != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errFormat.Error()})
	}
	var collection credentialExportCollection
	var errExport error
	if strings.EqualFold(strings.TrimSpace(req.Method), http.MethodPost) {
		var request CredentialExportRequest
		if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
		}
		scope, errScope := request.Scope.Validate()
		if errScope != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": errScope.Error()})
		}
		if scope.Mode != "selected" {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": "credential export scope must be selected"})
		}
		collection, errExport = a.accounts.ExportSelectedCredentialSources(ctx, scope)
	} else {
		query, errQuery := listQueryFromValues(req.Query)
		if errQuery != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": errQuery.Error()})
		}
		collection, errExport = a.accounts.ExportCredentialSources(ctx, query.Filters)
	}
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
	now := time.Now().UTC()
	scope := OperationScopeFiltered
	if strings.EqualFold(strings.TrimSpace(req.Method), http.MethodPost) {
		scope = OperationScopeSelected
	}
	a.operations.Record(OperationEntry{
		Category: OperationCategoryExport, Action: OperationActionExportAccounts, Status: OperationStatusSucceeded,
		Source: OperationSourceManual, Scope: scope, TargetCount: download.Exported + download.Skipped,
		Succeeded: download.Exported, Skipped: download.Skipped, StartedAt: now, FinishedAt: now,
		ReasonCode: "completed", Format: format,
	})
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
	now := time.Now().UTC()
	a.operations.Record(OperationEntry{
		Category: OperationCategoryExport, Action: OperationActionExportResults, Status: OperationStatusSucceeded,
		Source: OperationSourceManual, Scope: OperationScopeSystem, TargetCount: download.Exported,
		Succeeded: download.Exported, Skipped: download.Skipped, StartedAt: now, FinishedAt: now,
		ReasonCode: "completed", Format: format,
	})
	return exportDownloadResponse(download)
}

func (a *App) handlePutInspectionPolicy(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request InspectionPolicyUpdateRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	current := a.inspection.Snapshot().Policy
	if request.AutoDelete && !current.AutoDelete && !request.ConfirmAutoDelete {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "enabling auto_delete requires explicit confirmation"})
	}
	if request.AutoDeleteInvalidCredentials && !current.AutoDeleteInvalidCredentials && !request.ConfirmDeleteInvalidCredentials {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "enabling auto_delete_invalid_credentials requires explicit confirmation"})
	}
	if request.ModelProbeEnabled || request.AnomalyTriggerEnabled || request.AutoDisable || request.AutoEnable || request.AutoDelete {
		managementKey := resolveManagementKey(req.Headers)
		if managementKey != "" {
			a.inspection.ArmModelProbes(managementKey)
			managementKey = ""
		}
	}
	snapshot, errSave := a.inspection.SetPolicy(request.InspectionPolicy)
	if errSave != nil {
		a.recordPolicyChange(OperationCategoryInspection, OperationActionInspectionSave, OperationSourceManual, OperationStatusFailed)
		if strings.Contains(errSave.Error(), "save inspection policy") || strings.Contains(errSave.Error(), "storage is unavailable") {
			return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "inspection policy could not be persisted"})
		}
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errSave.Error()})
	}
	a.recordPolicyChange(OperationCategoryInspection, OperationActionInspectionSave, OperationSourceManual, OperationStatusSucceeded)
	return jsonResponse(http.StatusOK, snapshot)
}

func (a *App) handleListInspectionResults(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	_ = a.inspection.ReconcileAccountStates(ctx)
	query := InspectionResultQuery{
		Page:     intQuery(req.Query, "page", 1),
		PageSize: intQuery(req.Query, "page_size", 50),
		Health:   firstQuery(req.Query, "health"),
		Search:   firstQuery(req.Query, "search"),
	}
	return jsonResponse(http.StatusOK, a.inspection.ListResults(query))
}

func (a *App) handleInspectionReview(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request InspectionReviewRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	result, errUpdate := a.inspection.UpdateReview(request)
	if errUpdate != nil {
		status := http.StatusBadRequest
		if strings.Contains(errUpdate.Error(), "not found") {
			status = http.StatusNotFound
		} else if strings.Contains(errUpdate.Error(), "does not require review") {
			status = http.StatusConflict
		}
		return jsonResponse(status, map[string]any{"error": errUpdate.Error()})
	}
	a.reconcileOperationSources()
	return jsonResponse(http.StatusOK, result)
}

func (a *App) handleInspectionRun(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request InspectionRunRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	snapshot, errRun := a.inspection.RequestRun(request, managementKey)
	managementKey = ""
	if errRun != nil {
		status := http.StatusBadRequest
		if strings.Contains(errRun.Error(), "already running") {
			status = http.StatusConflict
		}
		return jsonResponse(status, map[string]any{"error": errRun.Error()})
	}
	return jsonResponse(http.StatusAccepted, snapshot)
}

func (a *App) handleListInspectionActions(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	return jsonResponse(http.StatusOK, map[string]any{
		"actions": a.inspection.Actions(intQuery(req.Query, "limit", 50)),
	})
}

func (a *App) handleInspectionManualDelete(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request InspectionManualDeleteRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	config := a.configSnapshot()
	startedAt := time.Now().UTC()
	run, errDelete := a.inspection.ExecuteManualDeletes(ctx, a.deletions, config.ManagementBaseURL, managementKey, request)
	managementKey = ""
	if errDelete != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(errDelete, ErrInspectionDeleteConfirmation) || errors.Is(errDelete, ErrInspectionDeleteIDsRequired) || errors.Is(errDelete, ErrInspectionDeleteTooMany) {
			status = http.StatusBadRequest
		}
		return jsonResponse(status, map[string]any{"error": errDelete.Error()})
	}
	status := OperationStatusSucceeded
	reason := "completed"
	if run.Failed > 0 || run.Skipped > 0 {
		status = OperationStatusPartial
		reason = "partial_failure"
		if run.Succeeded == 0 && run.Failed > 0 {
			status = OperationStatusFailed
			reason = "operation_failed"
		}
	}
	finishedAt := time.Now().UTC()
	a.operations.Record(OperationEntry{
		Category: OperationCategoryInspection, Action: OperationActionDelete, Status: status,
		Source: OperationSourceManual, Scope: OperationScopeSelected, TargetCount: run.Attempted,
		Succeeded: run.Succeeded, Failed: run.Failed, Skipped: run.Skipped,
		StartedAt: startedAt, FinishedAt: finishedAt, ReasonCode: reason,
	})
	return jsonResponse(http.StatusOK, run)
}

func (a *App) handleInspectionAutoDelete(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if !a.inspection.Snapshot().Policy.AutoDelete {
		return jsonResponse(http.StatusConflict, map[string]any{"error": "auto_delete is disabled"})
	}
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	config := a.configSnapshot()
	run := a.inspection.ExecutePendingDeletes(ctx, a.deletions, config.ManagementBaseURL, managementKey)
	managementKey = ""
	now := time.Now().UTC()
	status := OperationStatusSucceeded
	reason := "completed"
	if run.Failed > 0 {
		status = OperationStatusFailed
		reason = "operation_failed"
		if run.Succeeded > 0 {
			status = OperationStatusPartial
			reason = "partial_failure"
		}
	}
	a.operations.Record(OperationEntry{
		Category: OperationCategoryInspection, Action: OperationActionAutoDelete, Status: status,
		Source: OperationSourceManual, Scope: OperationScopeSelected, TargetCount: run.Attempted,
		Succeeded: run.Succeeded, Failed: run.Failed, Skipped: run.Skipped, StartedAt: now,
		FinishedAt: now, ReasonCode: reason,
	})
	return jsonResponse(http.StatusOK, run)
}

func (a *App) handlePutUpdatePolicy(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request UpdatePolicyRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	current := a.updates.Snapshot().Policy
	if request.Policy.AutoUpdate && !current.AutoUpdate && !request.ConfirmAutoUpdate {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "enabling auto_update requires explicit confirmation"})
	}
	snapshot, errSave := a.updates.SetPolicy(request.Policy)
	if errSave != nil {
		a.recordPolicyChange(OperationCategoryUpdate, OperationActionUpdateSave, OperationSourceManual, OperationStatusFailed)
		if strings.Contains(errSave.Error(), "save update policy") || strings.Contains(errSave.Error(), "storage is unavailable") {
			return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "update policy could not be persisted"})
		}
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errSave.Error()})
	}
	a.recordPolicyChange(OperationCategoryUpdate, OperationActionUpdateSave, OperationSourceManual, OperationStatusSucceeded)
	return jsonResponse(http.StatusOK, snapshot)
}

func (a *App) recordPolicyChange(category, action, source, status string) {
	now := time.Now().UTC()
	reason := "completed"
	failed := 0
	succeeded := 1
	if status == OperationStatusFailed {
		reason = "operation_failed"
		failed = 1
		succeeded = 0
	}
	a.operations.Record(OperationEntry{
		Category: category, Action: action, Status: status, Source: source, Scope: OperationScopeSystem,
		TargetCount: 1, Succeeded: succeeded, Failed: failed, StartedAt: now, FinishedAt: now, ReasonCode: reason,
	})
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

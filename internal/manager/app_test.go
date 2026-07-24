package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestManagementRegistrationUsesExactFixedRoutes(t *testing.T) {
	app := NewApp(&fakeAuthHost{}, []byte("index"))
	defer app.Close()
	registration := app.ManagementRegistration()
	expected := map[string]struct{}{
		http.MethodGet + " /plugins/cpa-account-config-manager/accounts":                                  {},
		http.MethodPost + " /plugins/cpa-account-config-manager/accounts/model-test":                      {},
		http.MethodPost + " /plugins/cpa-account-config-manager/accounts/delete/preview":                  {},
		http.MethodPost + " /plugins/cpa-account-config-manager/accounts/delete/start":                    {},
		http.MethodPost + " /plugins/cpa-account-config-manager/batch/preview":                            {},
		http.MethodPost + " /plugins/cpa-account-config-manager/batch/start":                              {},
		http.MethodPost + " /plugins/cpa-account-config-manager/batch/delete/preview":                     {},
		http.MethodPost + " /plugins/cpa-account-config-manager/batch/delete/start":                       {},
		http.MethodGet + " /plugins/cpa-account-config-manager/batch/status":                              {},
		http.MethodPost + " /plugins/cpa-account-config-manager/batch/retry":                              {},
		http.MethodGet + " /plugins/cpa-account-config-manager/export/accounts":                           {},
		http.MethodPost + " /plugins/cpa-account-config-manager/export/accounts":                          {},
		http.MethodGet + " /plugins/cpa-account-config-manager/export/results":                            {},
		http.MethodPost + " /plugins/cpa-account-config-manager/import/preview":                           {},
		http.MethodPost + " /plugins/cpa-account-config-manager/import/start":                             {},
		http.MethodGet + " /plugins/cpa-account-config-manager/defaults":                                  {},
		http.MethodPut + " /plugins/cpa-account-config-manager/defaults":                                  {},
		http.MethodPost + " /plugins/cpa-account-config-manager/defaults/scan":                            {},
		http.MethodPost + " /plugins/cpa-account-config-manager/defaults/force/preview":                   {},
		http.MethodPost + " /plugins/cpa-account-config-manager/defaults/force/start":                     {},
		http.MethodGet + " /plugins/cpa-account-config-manager/defaults/force/status":                     {},
		http.MethodGet + " /plugins/cpa-account-config-manager/inspection":                                {},
		http.MethodGet + " /plugins/cpa-account-config-manager/inspection/live":                           {},
		http.MethodPut + " /plugins/cpa-account-config-manager/inspection":                                {},
		http.MethodPost + " /plugins/cpa-account-config-manager/inspection/scan":                          {},
		http.MethodPost + " /plugins/cpa-account-config-manager/inspection/scan/native":                   {},
		http.MethodPost + " /plugins/cpa-account-config-manager/inspection/notification/preview":          {},
		http.MethodPost + " /plugins/cpa-account-config-manager/inspection/notification/test":             {},
		http.MethodPost + " /plugins/cpa-account-config-manager/inspection/run":                           {},
		http.MethodPost + " /plugins/cpa-account-config-manager/inspection/stop":                          {},
		http.MethodPost + " /plugins/cpa-account-config-manager/inspection/review":                        {},
		http.MethodGet + " /plugins/cpa-account-config-manager/inspection/results":                        {},
		http.MethodGet + " /plugins/cpa-account-config-manager/inspection/export":                         {},
		http.MethodGet + " /plugins/cpa-account-config-manager/inspection/actions":                        {},
		http.MethodPost + " /plugins/cpa-account-config-manager/inspection/delete":                        {},
		http.MethodPost + " /plugins/cpa-account-config-manager/inspection/auto-delete":                   {},
		http.MethodGet + " /plugins/cpa-account-config-manager/updates":                                   {},
		http.MethodPut + " /plugins/cpa-account-config-manager/updates":                                   {},
		http.MethodPost + " /plugins/cpa-account-config-manager/updates/check":                            {},
		http.MethodGet + " /plugins/cpa-account-config-manager/experiments":                               {},
		http.MethodPut + " /plugins/cpa-account-config-manager/experiments":                               {},
		http.MethodPost + " /plugins/cpa-account-config-manager/experiments/agent-identity/session-login": {},
		http.MethodGet + " /plugins/cpa-account-config-manager/operations":                                {},
		http.MethodGet + " /plugins/cpa-account-config-manager/operations/export":                         {},
		http.MethodGet + " /plugins/cpa-account-config-manager/operations/settings":                       {},
		http.MethodPut + " /plugins/cpa-account-config-manager/operations/settings":                       {},
		http.MethodDelete + " /plugins/cpa-account-config-manager/operations":                             {},
		http.MethodPost + " /plugins/cpa-account-config-manager/operations/record":                        {},
	}
	if len(registration.Routes) != len(expected) {
		t.Fatalf("routes len = %d, want %d", len(registration.Routes), len(expected))
	}
	for _, route := range registration.Routes {
		key := route.Method + " " + route.Path
		if _, exists := expected[key]; !exists {
			t.Fatalf("unexpected route %q", key)
		}
		delete(expected, key)
		if route.Path == "" || route.Path[0] != '/' {
			t.Fatalf("invalid route path %q", route.Path)
		}
		for _, forbidden := range []string{"*", ":", "{"} {
			if strings.Contains(route.Path, forbidden) {
				t.Fatalf("route %q contains dynamic marker %q", route.Path, forbidden)
			}
		}
	}
	if len(expected) != 0 {
		t.Fatalf("missing routes = %#v", expected)
	}
	if len(registration.Resources) != 1 || registration.Resources[0].Path != "/index.html" || registration.Resources[0].Menu != "CPA-A Manager" {
		t.Fatalf("resources = %#v", registration.Resources)
	}
}

func TestRegistrationUsesInjectedReleaseMetadata(t *testing.T) {
	originalVersion := PluginVersion
	originalRepository := PluginRepository
	PluginVersion = "1.2.3"
	PluginRepository = "https://github.com/example/cpa-account-config-manager"
	defer func() {
		PluginVersion = originalVersion
		PluginRepository = originalRepository
	}()

	app := NewApp(&fakeAuthHost{}, []byte("index"))
	defer app.Close()
	registration := app.Registration()
	if registration.Metadata.Version != "1.2.3" || registration.Metadata.GitHubRepository != PluginRepository {
		t.Fatalf("metadata = %#v", registration.Metadata)
	}
	if !registration.Capabilities.ManagementAPI || !registration.Capabilities.UsagePlugin || !registration.Capabilities.RequestInterceptor {
		t.Fatalf("capabilities = %#v", registration.Capabilities)
	}
}

func TestInspectionLiveRouteDisablesCaching(t *testing.T) {
	app := NewApp(&fakeAuthHost{}, []byte("index"))
	defer app.Close()
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method:  http.MethodGet,
		Path:    "/v0/management/plugins/cpa-account-config-manager/inspection/live",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}},
	})
	if response.StatusCode != http.StatusOK || response.Headers.Get("Cache-Control") != "no-store" {
		t.Fatalf("live response = %d headers=%v body=%s", response.StatusCode, response.Headers, response.Body)
	}
}

func TestInspectionNotificationRoutesRequireManagementKey(t *testing.T) {
	app := NewApp(&fakeAuthHost{}, []byte("index"))
	defer app.Close()
	body := []byte(`{"url_template":"https://notify.example/publish?available=${available_accounts}","scenario":"manual_test","threshold_percent":50,"available_accounts_threshold":1,"availability_percent_threshold":20}`)
	for _, route := range []string{
		"/v0/management/plugins/cpa-account-config-manager/inspection/notification/preview",
		"/v0/management/plugins/cpa-account-config-manager/inspection/notification/test",
	} {
		response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
			Method: http.MethodPost, Path: route, Body: body,
		})
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("route %s status = %d body=%s", route, response.StatusCode, response.Body)
		}
	}
}

func TestHandleManagementListsRedactedAccounts(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "auth-1",
			Name:      "account.json",
			Provider:  "codex",
			Source:    "file",
			Path:      "/auths/account.json",
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"auth-1": {
				AuthIndex: "auth-1",
				Name:      "account.json",
				Path:      "/auths/account.json",
				JSON:      json.RawMessage(`{"type":"codex","access_token":"secret"}`),
			},
		},
	}
	app := NewApp(host, []byte("index"))
	defer app.Close()
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-account-config-manager/accounts",
		Query:  url.Values{"page_size": []string{"20"}},
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	if strings.Contains(string(response.Body), "secret") {
		t.Fatalf("response leaked secret: %s", response.Body)
	}
}

func TestAppReplacementRestoresPersistedUsageIntoAccountList(t *testing.T) {
	dataDir := t.TempDir()
	config := []byte(fmt.Sprintf("data_dir: %q\n", dataDir))
	newHost := func() *fakeAuthHost {
		return &fakeAuthHost{
			entries: []cpaapi.HostAuthFileEntry{{
				ID: "runtime-instance-id", AuthIndex: "stable-auth-index", Name: "persisted.json",
				Provider: "codex", Type: "oauth", Source: "file", Path: "/auths/persisted.json",
			}},
			details: map[string]cpaapi.HostAuthGetResponse{
				"stable-auth-index": {
					AuthIndex: "stable-auth-index", Name: "persisted.json", Path: "/auths/persisted.json",
					JSON: json.RawMessage(`{"type":"codex","access_token":"must-not-be-persisted"}`),
				},
			},
		}
	}

	observedAt := time.Now().UTC().Truncate(time.Second)
	beforeUpdate := NewApp(newHost(), []byte("old-index"))
	beforeUpdate.Configure(config)
	beforeUpdate.HandleUsage(cpaapi.UsageRecord{
		AuthIndex: "stable-auth-index", AuthID: "must-not-be-persisted", APIKey: "sk-must-not-be-persisted",
		RequestedAt: observedAt,
		Detail:      cpaapi.UsageDetail{InputTokens: 80, OutputTokens: 20, TotalTokens: 100},
		ResponseHeaders: http.Header{
			"Authorization":                         []string{"Bearer must-not-be-persisted"},
			"X-Codex-Primary-Used-Percent":          []string{"64"},
			"X-Codex-Primary-Reset-After-Seconds":   []string{"604800"},
			"X-Codex-Primary-Window-Minutes":        []string{"10080"},
			"X-Codex-Secondary-Used-Percent":        []string{"18"},
			"X-Codex-Secondary-Reset-After-Seconds": []string{"18000"},
			"X-Codex-Secondary-Window-Minutes":      []string{"300"},
		},
	})
	beforeUpdate.Close()

	afterUpdate := NewApp(newHost(), []byte("replacement-index"))
	afterUpdate.Configure(config)
	defer afterUpdate.Close()
	response := afterUpdate.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-account-config-manager/accounts",
		Query:  url.Values{"page_size": []string{"20"}},
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("replacement account list status = %d body=%s", response.StatusCode, response.Body)
	}
	var listed ListResponse
	if errDecode := json.Unmarshal(response.Body, &listed); errDecode != nil {
		t.Fatalf("decode replacement account list: %v", errDecode)
	}
	if len(listed.Accounts) != 1 {
		t.Fatalf("replacement account count = %d, want 1", len(listed.Accounts))
	}
	usage := listed.Accounts[0].Usage
	if usage == nil || usage.TotalTokens != 100 || usage.InputTokens != 80 || usage.OutputTokens != 20 {
		t.Fatalf("replacement account usage = %#v", usage)
	}
	if usage.Codex == nil || usage.Codex.FiveHour == nil || usage.Codex.SevenDay == nil ||
		usage.Codex.FiveHour.UsedPercent != 18 || usage.Codex.SevenDay.UsedPercent != 64 {
		t.Fatalf("replacement account quota = %#v", usage.Codex)
	}
	for _, secret := range []string{"must-not-be-persisted", "sk-must-not-be-persisted", "Authorization", "Bearer"} {
		if bytes.Contains(response.Body, []byte(secret)) {
			t.Fatalf("replacement response leaked %q: %s", secret, response.Body)
		}
	}
}

func TestHandleManagementMergesSanitizedAutomationSummary(t *testing.T) {
	now := time.Date(2026, time.July, 20, 18, 0, 0, 0, time.UTC)
	recoverAfter := now.Add(2 * time.Hour)
	host := inspectionEditableHost(true)
	app := NewApp(host, []byte("index"))
	defer app.Close()
	app.inspection.mu.Lock()
	app.inspection.policy = InspectionPolicy{AutoDisable: true, AutoEnable: true, RecoveryThreshold: 2}
	app.inspection.records["inspection-account"] = inspectionRecord{
		Result: InspectionResult{
			ID:               "inspection-account",
			Health:           InspectionHealthQuotaLimited,
			ReasonCode:       "quota_exhausted",
			Recommendation:   InspectionRecommendationEnable,
			OwnedDisable:     true,
			Disabled:         true,
			LastCheckedAt:    now,
			AutoAction:       InspectionActionDisable,
			AutoActionStatus: InspectionActionSucceeded,
		},
		DisableReason:        "quota_exhausted",
		DisabledAt:           now.Add(-time.Hour),
		DisabledRecoverAfter: recoverAfter,
		DisabledPath:         "/auths/list-summary-secret.json",
		DisabledVersion:      "list-summary-secret-revision",
	}
	app.inspection.mu.Unlock()

	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-account-config-manager/accounts",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	for _, secret := range []string{"account-secret", "list-summary-secret", "/auths/", "revision"} {
		if bytes.Contains(response.Body, []byte(secret)) {
			t.Fatalf("account response leaked %q: %s", secret, response.Body)
		}
	}
	var listed ListResponse
	if errDecode := json.Unmarshal(response.Body, &listed); errDecode != nil {
		t.Fatalf("decode account list: %v", errDecode)
	}
	if len(listed.Accounts) != 1 || listed.Accounts[0].Automation == nil {
		t.Fatalf("account list = %#v", listed)
	}
	summary := listed.Accounts[0].Automation
	if !summary.OwnedDisable || summary.DisableReason != "quota_exhausted" || summary.RecoverAfter == nil || !summary.RecoverAfter.Equal(recoverAfter) {
		t.Fatalf("automation summary = %#v", summary)
	}
}

func TestHandleManagementServesResourceOnlyAtResourcePath(t *testing.T) {
	app := NewApp(&fakeAuthHost{}, []byte("<!doctype html><title>manager</title>"))
	defer app.Close()
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/cpa-account-config-manager/index.html",
	})
	if response.StatusCode != http.StatusOK || string(response.Body) != "<!doctype html><title>manager</title>" {
		t.Fatalf("response = %d %q", response.StatusCode, response.Body)
	}
}

func TestHandleManagementRunsPreviewStartStatusAndRedactedExports(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "auth-1",
			Name:      "account.json",
			Provider:  "codex",
			Source:    "file",
			Path:      "/auths/account.json",
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"auth-1": {
				AuthIndex: "auth-1",
				Name:      "account.json",
				Path:      "/auths/account.json",
				JSON:      json.RawMessage(`{"access_token":"auth-secret","proxy_url":"http://user:old-proxy-secret@127.0.0.1:7890","headers":{"Authorization":"Bearer old-header-secret"}}`),
			},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer management-secret" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(writer, `{"status":"ok"}`)
	}))
	defer server.Close()

	app := NewApp(host, []byte("index"))
	app.jobs.doer = server.Client()
	app.Configure([]byte(fmt.Sprintf("workers: 1\ndata_dir: %q\nmanagement_base_url: %q\n", t.TempDir(), server.URL)))
	defer app.Close()
	proxyURL := "http://new-user:new-proxy-secret@127.0.0.1:7890"
	disabled := true
	previewBody, _ := json.Marshal(PreviewRequest{
		Scope: TargetScope{Mode: "selected", IDs: []string{"auth-1"}},
		Patch: BatchPatch{
			Disabled: &disabled,
			ProxyURL: &proxyURL,
			Headers:  &HeaderPatch{Set: map[string]string{"Authorization": "Bearer new-header-secret"}},
		},
	})
	previewResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-account-config-manager/batch/preview",
		Body:   previewBody,
	})
	if previewResponse.StatusCode != http.StatusOK {
		t.Fatalf("preview = %d %s", previewResponse.StatusCode, previewResponse.Body)
	}
	for _, secret := range []string{"auth-secret", "new-proxy-secret", "new-header-secret"} {
		if strings.Contains(string(previewResponse.Body), secret) {
			t.Fatalf("preview leaked %q: %s", secret, previewResponse.Body)
		}
	}
	var preview BatchPreview
	if errDecode := json.Unmarshal(previewResponse.Body, &preview); errDecode != nil {
		t.Fatalf("decode preview: %v", errDecode)
	}
	startBody, _ := json.Marshal(StartRequest{PreviewID: preview.ID})
	startResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method:  http.MethodPost,
		Path:    "/v0/management/plugins/cpa-account-config-manager/batch/start",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}},
		Body:    startBody,
	})
	if startResponse.StatusCode != http.StatusAccepted {
		t.Fatalf("start = %d %s", startResponse.StatusCode, startResponse.Body)
	}

	deadline := time.Now().Add(5 * time.Second)
	var statusResponse cpaapi.ManagementResponse
	completed := false
	for time.Now().Before(deadline) {
		statusResponse = app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
			Method: http.MethodGet,
			Path:   "/v0/management/plugins/cpa-account-config-manager/batch/status",
		})
		var snapshot JobSnapshot
		if errDecode := json.Unmarshal(statusResponse.Body, &snapshot); errDecode == nil && !snapshot.Running && snapshot.State == JobStateCompleted {
			completed = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if statusResponse.StatusCode != http.StatusOK || strings.Contains(string(statusResponse.Body), "management-secret") {
		t.Fatalf("status = %d %s", statusResponse.StatusCode, statusResponse.Body)
	}
	if !completed {
		t.Fatalf("job did not complete: %s", statusResponse.Body)
	}
	resultsExport := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-account-config-manager/export/results",
	})
	if resultsExport.StatusCode != http.StatusOK || resultsExport.Headers.Get("Content-Disposition") == "" {
		t.Fatalf("results export = %d %#v", resultsExport.StatusCode, resultsExport.Headers)
	}
	for _, secret := range []string{"management-secret", "new-proxy-secret", "new-header-secret"} {
		if strings.Contains(string(resultsExport.Body), secret) {
			t.Fatalf("results export leaked %q: %s", secret, resultsExport.Body)
		}
	}
	accountsExport := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-account-config-manager/export/accounts",
	})
	for _, secret := range []string{"auth-secret", "old-proxy-secret", "old-header-secret"} {
		if strings.Contains(string(accountsExport.Body), secret) {
			t.Fatalf("accounts export leaked %q: %s", secret, accountsExport.Body)
		}
	}
}

func TestHandleStartReturnsActionableStorageErrorAndKeepsPreview(t *testing.T) {
	app := NewApp(twoEditableAccountsHost(), []byte("index"))
	defer app.Close()

	blockingPath := filepath.Join(t.TempDir(), "not-a-directory")
	if errWrite := os.WriteFile(blockingPath, []byte("block"), 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	app.Configure([]byte(fmt.Sprintf("workers: 1\ndata_dir: %q\nmanagement_base_url: %q\n", filepath.Join(blockingPath, "jobs"), defaultManagementBaseURL)))
	writers := make([]*trackingWriter, 0, 2)
	app.jobs.newWriter = func(_ string, key string, _ HTTPDoer) (ManagementWriter, error) {
		writer := &trackingWriter{key: key}
		writers = append(writers, writer)
		return writer, nil
	}

	preview, errPreview := app.previews.Create(context.Background(), PreviewRequest{
		Scope: TargetScope{Mode: "selected", IDs: []string{"a"}},
		Patch: BatchPatch{Disabled: boolPointer(true)},
	})
	if errPreview != nil {
		t.Fatalf("Create() error = %v", errPreview)
	}
	startBody, _ := json.Marshal(StartRequest{PreviewID: preview.ID})
	startRequest := cpaapi.ManagementRequest{
		Method:  http.MethodPost,
		Path:    "/v0/management/plugins/cpa-account-config-manager/batch/start",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}},
		Body:    startBody,
	}
	response := app.HandleManagement(context.Background(), startRequest)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	if string(response.Body) != `{"error":"job result storage is unavailable; configure data_dir to a writable directory"}` {
		t.Fatalf("body = %s", response.Body)
	}
	if strings.Contains(string(response.Body), blockingPath) || len(writers) != 1 || writers[0].key != "" {
		t.Fatalf("failed start leaked a path or retained its key: body=%s writers=%#v", response.Body, writers)
	}

	app.Configure([]byte(fmt.Sprintf("workers: 1\ndata_dir: %q\nmanagement_base_url: %q\n", t.TempDir(), defaultManagementBaseURL)))
	retryResponse := app.HandleManagement(context.Background(), startRequest)
	if retryResponse.StatusCode != http.StatusAccepted {
		t.Fatalf("retry with the same preview = %d %s", retryResponse.StatusCode, retryResponse.Body)
	}
}

func TestHandleStartReturnsActionableManagementURLConfigurationError(t *testing.T) {
	app := NewApp(twoEditableAccountsHost(), []byte("index"))
	defer app.Close()
	unsafeURL := "https://public.example.com/private?token=deployment-secret"
	app.Configure([]byte(fmt.Sprintf("data_dir: %q\nmanagement_base_url: %q\n", t.TempDir(), unsafeURL)))

	preview, errPreview := app.previews.Create(context.Background(), PreviewRequest{
		Scope: TargetScope{Mode: "selected", IDs: []string{"a"}},
		Patch: BatchPatch{Disabled: boolPointer(true)},
	})
	if errPreview != nil {
		t.Fatalf("Create() error = %v", errPreview)
	}
	startBody, _ := json.Marshal(StartRequest{PreviewID: preview.ID})
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method:  http.MethodPost,
		Path:    "/v0/management/plugins/cpa-account-config-manager/batch/start",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}},
		Body:    startBody,
	})
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	if string(response.Body) != `{"error":"management_base_url is invalid; configure an HTTP(S) loopback URL"}` {
		t.Fatalf("body = %s", response.Body)
	}
	for _, forbidden := range []string{"public.example.com", "deployment-secret", "management-secret"} {
		if strings.Contains(string(response.Body), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, response.Body)
		}
	}
}

func TestHandleDefaultPolicyAndForceSyncRoutes(t *testing.T) {
	host := forceSyncHost()
	app := NewApp(host, []byte("index"))
	app.Configure([]byte(fmt.Sprintf("workers: 2\ndata_dir: %q\n", t.TempDir())))
	defer app.Close()

	policyBody := []byte(`{"enabled":true,"apply_mode":"missing","scan_interval_seconds":1,"priority":0,"websockets":false}`)
	putResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut,
		Path:   "/v0/management/plugins/cpa-account-config-manager/defaults",
		Body:   policyBody,
	})
	if putResponse.StatusCode != http.StatusOK {
		t.Fatalf("PUT defaults = %d %s", putResponse.StatusCode, putResponse.Body)
	}
	var policySnapshot PolicySnapshot
	if errDecode := json.Unmarshal(putResponse.Body, &policySnapshot); errDecode != nil {
		t.Fatalf("decode policy response: %v", errDecode)
	}
	if !policySnapshot.Policy.Enabled || policySnapshot.Policy.ScanIntervalSeconds != minPolicyScanIntervalSeconds ||
		policySnapshot.Policy.Priority == nil || *policySnapshot.Policy.Priority != 0 ||
		policySnapshot.Policy.Websockets == nil || *policySnapshot.Policy.Websockets {
		t.Fatalf("policy response = %#v", policySnapshot)
	}

	getResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-account-config-manager/defaults",
	})
	if getResponse.StatusCode != http.StatusOK || !bytes.Contains(getResponse.Body, []byte(`"apply_mode":"missing"`)) {
		t.Fatalf("GET defaults = %d %s", getResponse.StatusCode, getResponse.Body)
	}
	scanResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-account-config-manager/defaults/scan",
	})
	if scanResponse.StatusCode != http.StatusAccepted {
		t.Fatalf("POST scan = %d %s", scanResponse.StatusCode, scanResponse.Body)
	}

	previewResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-account-config-manager/defaults/force/preview",
	})
	if previewResponse.StatusCode != http.StatusOK {
		t.Fatalf("POST force preview = %d %s", previewResponse.StatusCode, previewResponse.Body)
	}
	var preview ForceSyncPreview
	if errDecode := json.Unmarshal(previewResponse.Body, &preview); errDecode != nil {
		t.Fatalf("decode force preview: %v", errDecode)
	}
	startBody, _ := json.Marshal(StartRequest{PreviewID: preview.ID})
	startResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-account-config-manager/defaults/force/start",
		Body:   startBody,
	})
	if startResponse.StatusCode != http.StatusAccepted {
		t.Fatalf("POST force start = %d %s", startResponse.StatusCode, startResponse.Body)
	}
	deadline := time.Now().Add(3 * time.Second)
	var forceStatus cpaapi.ManagementResponse
	var terminalForce ForceSyncJobSnapshot
	for time.Now().Before(deadline) {
		forceStatus = app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
			Method: http.MethodGet,
			Path:   "/v0/management/plugins/cpa-account-config-manager/defaults/force/status",
		})
		if errDecode := json.Unmarshal(forceStatus.Body, &terminalForce); errDecode == nil && !terminalForce.Running && terminalForce.State != JobStateIdle {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if forceStatus.StatusCode != http.StatusOK {
		t.Fatalf("GET force status = %d %s", forceStatus.StatusCode, forceStatus.Body)
	}
	if terminalForce.Running || terminalForce.State == JobStateIdle {
		t.Fatalf("force sync did not reach terminal state: %s", forceStatus.Body)
	}
	for _, secret := range []string{"access-secret", "header-secret", "proxy-secret", "/auths"} {
		if bytes.Contains(putResponse.Body, []byte(secret)) || bytes.Contains(previewResponse.Body, []byte(secret)) || bytes.Contains(forceStatus.Body, []byte(secret)) {
			t.Fatalf("policy API leaked %q", secret)
		}
	}
}

func TestHandleDefaultPolicyUsesSafeBestEffortLocalStorageAndValidationErrors(t *testing.T) {
	blockingPath := filepath.Join(t.TempDir(), "not-a-directory")
	if errWrite := os.WriteFile(blockingPath, []byte("block"), 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	dataDir := filepath.Join(blockingPath, "policy")
	app := NewApp(&fakeAuthHost{details: map[string]cpaapi.HostAuthGetResponse{}}, []byte("index"))
	app.Configure([]byte(fmt.Sprintf("data_dir: %q\n", dataDir)))
	defer app.Close()

	storageResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut,
		Path:   "/v0/management/plugins/cpa-account-config-manager/defaults",
		Body:   []byte(`{"enabled":false,"priority":0,"websockets":null}`),
	})
	if storageResponse.StatusCode != http.StatusOK || !bytes.Contains(storageResponse.Body, []byte(policyLocalStoreError)) {
		t.Fatalf("storage response = %d %s", storageResponse.StatusCode, storageResponse.Body)
	}
	if bytes.Contains(storageResponse.Body, []byte(dataDir)) {
		t.Fatalf("storage response leaked its path: %s", storageResponse.Body)
	}

	validationResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut,
		Path:   "/v0/management/plugins/cpa-account-config-manager/defaults",
		Body:   []byte(`{"enabled":true,"unknown":"secret-value"}`),
	})
	if validationResponse.StatusCode != http.StatusBadRequest || bytes.Contains(validationResponse.Body, []byte("secret-value")) {
		t.Fatalf("validation response = %d %s", validationResponse.StatusCode, validationResponse.Body)
	}
}

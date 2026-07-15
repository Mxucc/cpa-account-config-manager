package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestManagementRegistrationUsesExactFixedRoutes(t *testing.T) {
	app := NewApp(&fakeAuthHost{}, []byte("index"))
	registration := app.ManagementRegistration()
	if len(registration.Routes) != 7 {
		t.Fatalf("routes len = %d, want 7", len(registration.Routes))
	}
	for _, route := range registration.Routes {
		if route.Path == "" || route.Path[0] != '/' {
			t.Fatalf("invalid route path %q", route.Path)
		}
		for _, forbidden := range []string{"*", ":", "{"} {
			if strings.Contains(route.Path, forbidden) {
				t.Fatalf("route %q contains dynamic marker %q", route.Path, forbidden)
			}
		}
	}
	if len(registration.Resources) != 1 || registration.Resources[0].Path != "/index.html" {
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

	registration := NewApp(&fakeAuthHost{}, []byte("index")).Registration()
	if registration.Metadata.Version != "1.2.3" || registration.Metadata.GitHubRepository != PluginRepository {
		t.Fatalf("metadata = %#v", registration.Metadata)
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

func TestHandleManagementServesResourceOnlyAtResourcePath(t *testing.T) {
	app := NewApp(&fakeAuthHost{}, []byte("<!doctype html><title>manager</title>"))
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

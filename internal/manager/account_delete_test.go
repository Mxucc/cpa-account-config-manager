package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestAccountDeleteServicePreviewsAndDeletesOneAccount(t *testing.T) {
	host := editableAccountDeleteHost()
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		deleteCalls++
		if request.Method != http.MethodDelete {
			t.Errorf("method = %s", request.Method)
		}
		if request.URL.Path != "/v0/management/auth-files" || request.URL.Query().Get("name") != "operator.json" {
			t.Errorf("URL = %s", request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer management-secret" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(writer, `{"status":"ok","detail":"response-secret"}`)
	}))
	defer server.Close()

	service := NewAccountDeleteService(NewAccountService(host), NewMutationCoordinator())
	service.doer = server.Client()
	preview, errPreview := service.Preview(context.Background(), AccountDeletePreviewRequest{ID: "auth-1"})
	if errPreview != nil {
		t.Fatalf("Preview() error = %v", errPreview)
	}
	if preview.Account.ID != "auth-1" || preview.Account.Name != "operator.json" || preview.Account.Provider != "codex" {
		t.Fatalf("preview = %#v", preview)
	}
	assertDeletePayloadRedacted(t, preview)

	result, errStart := service.Start(context.Background(), preview.ID, server.URL, "management-secret")
	if errStart != nil {
		t.Fatalf("Start() error = %v", errStart)
	}
	if result.Status != "deleted" || result.Account.Name != "operator.json" || deleteCalls != 1 {
		t.Fatalf("result = %#v calls=%d", result, deleteCalls)
	}
	assertDeletePayloadRedacted(t, result)
	if _, errSecond := service.Start(context.Background(), preview.ID, server.URL, "management-secret"); !errors.Is(errSecond, ErrAccountDeletePreviewNotFound) {
		t.Fatalf("second Start() error = %v", errSecond)
	}
}

func TestAccountDeleteServiceRejectsReadOnlyMissingExpiredStaleAndBusyTargets(t *testing.T) {
	t.Run("read only", func(t *testing.T) {
		host := &fakeAuthHost{entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex:   "runtime-1",
			Name:        "runtime.json",
			Provider:    "codex",
			Source:      "runtime",
			RuntimeOnly: true,
		}}}
		service := NewAccountDeleteService(NewAccountService(host), NewMutationCoordinator())
		if _, errPreview := service.Preview(context.Background(), AccountDeletePreviewRequest{ID: "runtime-1"}); !errors.Is(errPreview, ErrAccountDeleteTargetReadOnly) {
			t.Fatalf("Preview() error = %v", errPreview)
		}
	})

	t.Run("missing", func(t *testing.T) {
		service := NewAccountDeleteService(NewAccountService(&fakeAuthHost{}), NewMutationCoordinator())
		if _, errPreview := service.Preview(context.Background(), AccountDeletePreviewRequest{ID: "missing"}); !errors.Is(errPreview, ErrAccountDeleteTargetNotFound) {
			t.Fatalf("Preview() error = %v", errPreview)
		}
	})

	t.Run("expired", func(t *testing.T) {
		service := NewAccountDeleteService(NewAccountService(editableAccountDeleteHost()), NewMutationCoordinator())
		now := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
		service.now = func() time.Time { return now }
		preview, errPreview := service.Preview(context.Background(), AccountDeletePreviewRequest{ID: "auth-1"})
		if errPreview != nil {
			t.Fatalf("Preview() error = %v", errPreview)
		}
		now = now.Add(defaultAccountDeletePreviewTTL)
		if _, errStart := service.Start(context.Background(), preview.ID, defaultManagementBaseURL, "management-secret"); !errors.Is(errStart, ErrAccountDeletePreviewExpired) {
			t.Fatalf("Start() error = %v", errStart)
		}
	})

	t.Run("stale revision", func(t *testing.T) {
		host := editableAccountDeleteHost()
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			calls++
			writer.WriteHeader(http.StatusOK)
		}))
		defer server.Close()
		service := NewAccountDeleteService(NewAccountService(host), NewMutationCoordinator())
		service.doer = server.Client()
		preview, errPreview := service.Preview(context.Background(), AccountDeletePreviewRequest{ID: "auth-1"})
		if errPreview != nil {
			t.Fatalf("Preview() error = %v", errPreview)
		}
		host.mu.Lock()
		detail := host.details["auth-1"]
		detail.JSON = json.RawMessage(`{"type":"codex","access_token":"changed-secret"}`)
		host.details["auth-1"] = detail
		host.mu.Unlock()
		if _, errStart := service.Start(context.Background(), preview.ID, server.URL, "management-secret"); !errors.Is(errStart, ErrAccountDeletePreviewStale) {
			t.Fatalf("Start() error = %v", errStart)
		}
		if calls != 0 {
			t.Fatalf("delete endpoint called %d times for stale preview", calls)
		}
	})

	t.Run("shared mutation busy", func(t *testing.T) {
		mutations := NewMutationCoordinator()
		service := NewAccountDeleteService(NewAccountService(editableAccountDeleteHost()), mutations)
		preview, errPreview := service.Preview(context.Background(), AccountDeletePreviewRequest{ID: "auth-1"})
		if errPreview != nil {
			t.Fatalf("Preview() error = %v", errPreview)
		}
		if !mutations.TryAcquire("other-writer") {
			t.Fatal("failed to acquire test mutation slot")
		}
		defer mutations.Release("other-writer")
		if _, errStart := service.Start(context.Background(), preview.ID, defaultManagementBaseURL, "management-secret"); !errors.Is(errStart, ErrAccountDeleteBusy) {
			t.Fatalf("Start() error = %v", errStart)
		}
	})
}

func TestHandleManagementAccountDeleteRoutesAreAuthenticatedAndRedacted(t *testing.T) {
	host := editableAccountDeleteHost()
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		deleteCalls++
		if request.Header.Get("Authorization") != "Bearer management-secret" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		if deleteCalls == 1 {
			writer.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(writer, `{"error":"Bearer response-secret"}`)
			return
		}
		_, _ = io.WriteString(writer, `{"status":"ok"}`)
	}))
	defer server.Close()

	app := NewApp(host, []byte("index"))
	app.deletions.doer = server.Client()
	app.Configure([]byte(fmt.Sprintf("management_base_url: %q\n", server.URL)))
	defer app.Close()

	previewResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-account-config-manager/accounts/delete/preview",
		Body:   []byte(`{"id":"auth-1"}`),
	})
	if previewResponse.StatusCode != http.StatusOK {
		t.Fatalf("preview status = %d body=%s", previewResponse.StatusCode, previewResponse.Body)
	}
	if bytes.Contains(previewResponse.Body, []byte("account-secret")) || bytes.Contains(previewResponse.Body, []byte("/auths")) {
		t.Fatalf("preview leaked private data: %s", previewResponse.Body)
	}
	var preview AccountDeletePreview
	if errDecode := json.Unmarshal(previewResponse.Body, &preview); errDecode != nil {
		t.Fatalf("decode preview: %v", errDecode)
	}
	startBody, _ := json.Marshal(AccountDeleteStartRequest{PreviewID: preview.ID})

	unauthorized := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-account-config-manager/accounts/delete/start",
		Body:   startBody,
	})
	if unauthorized.StatusCode != http.StatusUnauthorized || deleteCalls != 0 {
		t.Fatalf("unauthorized response = %d %s calls=%d", unauthorized.StatusCode, unauthorized.Body, deleteCalls)
	}

	failed := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method:  http.MethodPost,
		Path:    "/v0/management/plugins/cpa-account-config-manager/accounts/delete/start",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}},
		Body:    startBody,
	})
	if failed.StatusCode != http.StatusBadGateway || bytes.Contains(failed.Body, []byte("response-secret")) {
		t.Fatalf("failed response = %d %s", failed.StatusCode, failed.Body)
	}

	succeeded := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method:  http.MethodPost,
		Path:    "/v0/management/plugins/cpa-account-config-manager/accounts/delete/start",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}},
		Body:    startBody,
	})
	if succeeded.StatusCode != http.StatusOK || deleteCalls != 2 {
		t.Fatalf("success response = %d %s calls=%d", succeeded.StatusCode, succeeded.Body, deleteCalls)
	}
	for _, secret := range []string{"account-secret", "proxy-secret", "header-secret", "management-secret", "/auths"} {
		if bytes.Contains(succeeded.Body, []byte(secret)) {
			t.Fatalf("success response leaked %q: %s", secret, succeeded.Body)
		}
	}
}

func editableAccountDeleteHost() *fakeAuthHost {
	return &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "auth-1",
			Name:      "operator.json",
			Provider:  "codex",
			Type:      "codex",
			Label:     "operator@example.com",
			Email:     "operator@example.com",
			Status:    "ready",
			Source:    "file",
			Path:      "/auths/operator.json",
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"auth-1": {
				AuthIndex: "auth-1",
				Name:      "operator.json",
				Path:      "/auths/operator.json",
				JSON:      json.RawMessage(`{"type":"codex","access_token":"account-secret","proxy_url":"http://user:proxy-secret@127.0.0.1:7890","headers":{"Authorization":"Bearer header-secret"}}`),
			},
		},
	}
}

func assertDeletePayloadRedacted(t *testing.T, payload any) {
	t.Helper()
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		t.Fatalf("json.Marshal() error = %v", errMarshal)
	}
	for _, secret := range []string{"account-secret", "proxy-secret", "header-secret", "response-secret", "/auths"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("payload leaked %q: %s", secret, raw)
		}
	}
}

package manager

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManagementClientForwardsBearerAndCanonicalPatchBodies(t *testing.T) {
	requests := make(chan map[string]any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPatch {
			t.Errorf("method = %s", request.Method)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer management-secret" {
			t.Errorf("Authorization = %q", got)
		}
		var body map[string]any
		if errDecode := json.NewDecoder(request.Body).Decode(&body); errDecode != nil {
			t.Errorf("decode body: %v", errDecode)
		}
		body["path"] = request.URL.Path
		requests <- body
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"status":"ok"}`)
	}))
	defer server.Close()

	client, errClient := newManagementClient(server.URL, "management-secret", server.Client())
	if errClient != nil {
		t.Fatalf("newManagementClient() error = %v", errClient)
	}
	note := "team-a"
	patch, errPatch := (BatchPatch{
		Note:    &note,
		Headers: &HeaderPatch{Set: map[string]string{"Authorization": "Bearer upstream-secret"}, Remove: []string{"X-Old"}},
	}).Validate()
	if errPatch != nil {
		t.Fatalf("Validate() error = %v", errPatch)
	}
	if errFields := client.PatchFields(context.Background(), "account.json", patch); errFields != nil {
		t.Fatalf("PatchFields() error = %v", errFields)
	}
	if errDisabled := client.PatchDisabled(context.Background(), "account.json", true); errDisabled != nil {
		t.Fatalf("PatchDisabled() error = %v", errDisabled)
	}
	fields := <-requests
	status := <-requests
	if fields["path"] != "/v0/management/auth-files/fields" || fields["name"] != "account.json" || fields["note"] != "team-a" {
		t.Fatalf("fields body = %#v", fields)
	}
	headers, okHeaders := fields["headers"].(map[string]any)
	if !okHeaders || headers["Authorization"] != "Bearer upstream-secret" || headers["X-Old"] != "" {
		t.Fatalf("headers body = %#v", fields["headers"])
	}
	if status["path"] != "/v0/management/auth-files/status" || status["disabled"] != true {
		t.Fatalf("status body = %#v", status)
	}
}

func TestManagementClientDoesNotExposeErrorResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(writer, `{"error":"Bearer response-secret"}`)
	}))
	defer server.Close()
	client, errClient := newManagementClient(server.URL, "management-secret", server.Client())
	if errClient != nil {
		t.Fatalf("newManagementClient() error = %v", errClient)
	}
	errPatch := client.PatchDisabled(context.Background(), "account.json", true)
	if errPatch == nil || !strings.Contains(errPatch.Error(), "HTTP 502") {
		t.Fatalf("PatchDisabled() error = %v", errPatch)
	}
	if strings.Contains(errPatch.Error(), "response-secret") {
		t.Fatalf("error leaked response body: %v", errPatch)
	}
}

func TestManagementClientDeletesSafeAuthFile(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		called = true
		if request.Method != http.MethodDelete {
			t.Errorf("method = %s", request.Method)
		}
		if request.URL.Path != "/v0/management/auth-files" || request.URL.Query().Get("name") != "operator+codex.json" {
			t.Errorf("URL = %s", request.URL.String())
		}
		if got := request.Header.Get("Authorization"); got != "Bearer management-secret" {
			t.Errorf("Authorization = %q", got)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"status":"ok"}`)
	}))
	defer server.Close()

	client, errClient := newManagementClient(server.URL, "management-secret", server.Client())
	if errClient != nil {
		t.Fatalf("newManagementClient() error = %v", errClient)
	}
	if errDelete := client.DeleteAuthFile(context.Background(), "operator+codex.json"); errDelete != nil {
		t.Fatalf("DeleteAuthFile() error = %v", errDelete)
	}
	if !called {
		t.Fatal("management delete endpoint was not called")
	}
	if errDelete := client.DeleteAuthFile(context.Background(), "../operator.json"); errDelete == nil {
		t.Fatal("DeleteAuthFile() accepted an unsafe file name")
	}
}

func TestManagementBaseURLRejectsNonLoopbackDestinations(t *testing.T) {
	for _, value := range []string{
		"https://example.com",
		"http://user:password@127.0.0.1:8317",
		"http://127.0.0.1:8317/management",
	} {
		if _, errValidate := validateManagementBaseURL(value); !errors.Is(errValidate, ErrManagementBaseURLInvalid) {
			t.Fatalf("validateManagementBaseURL(%q) error = %v", value, errValidate)
		}
	}
	if got, errValidate := validateManagementBaseURL("http://[::1]:8317"); errValidate != nil || got != "http://[::1]:8317" {
		t.Fatalf("loopback URL = %q, %v", got, errValidate)
	}
}

func TestResolveManagementBaseURLIgnoresRemoteGenericBaseURL(t *testing.T) {
	t.Setenv("CPA_MANAGEMENT_BASE_URL", "")
	t.Setenv("CPA_BASE_URL", "https://public.example.com")
	t.Setenv("PORT", "9417")
	t.Setenv("CPA_PORT", "")

	if got := resolveManagementBaseURL(""); got != "http://127.0.0.1:9417" {
		t.Fatalf("resolveManagementBaseURL() = %q", got)
	}
}

func TestManagementClientClearsManagementKey(t *testing.T) {
	client := &managementClient{key: "test-management-key"}
	client.clearSecrets()
	if client.key != "" {
		t.Fatal("management key was not cleared")
	}
}

func TestResolveManagementKeyUsesRequestBeforeEnvironment(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "environment-key")
	t.Setenv("CPA_MANAGEMENT_KEY", "secondary-environment-key")

	if got := resolveManagementKey(http.Header{
		"Authorization":    []string{"Bearer request-key"},
		"X-Management-Key": []string{"header-key"},
	}); got != "request-key" {
		t.Fatalf("bearer key = %q", got)
	}
	if got := resolveManagementKey(http.Header{"X-Management-Key": []string{"header-key"}}); got != "header-key" {
		t.Fatalf("management header key = %q", got)
	}
	if got := resolveManagementKey(nil); got != "environment-key" {
		t.Fatalf("environment key = %q", got)
	}
}

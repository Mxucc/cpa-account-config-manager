package manager

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

// recordingManagementServer is a Management API fixture that records every call
// so a test can assert which version path the client chose and what it sent.
type recordingManagementServer struct {
	mu      sync.Mutex
	calls   []recordedManagementCall
	handler func(recordedManagementCall) (int, http.Header, string)
}

type recordedManagementCall struct {
	Method  string
	Path    string
	IfMatch string
	Body    string
}

func (s *recordingManagementServer) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	body, _ := io.ReadAll(request.Body)
	call := recordedManagementCall{
		Method:  request.Method,
		Path:    request.URL.Path,
		IfMatch: request.Header.Get("If-Match"),
		Body:    string(body),
	}
	s.mu.Lock()
	s.calls = append(s.calls, call)
	s.mu.Unlock()
	status, headers, payload := s.handler(call)
	for name, values := range headers {
		for _, value := range values {
			writer.Header().Add(name, value)
		}
	}
	if payload != "" {
		writer.Header().Set("Content-Type", "application/json")
	}
	writer.WriteHeader(status)
	_, _ = io.WriteString(writer, payload)
}

func (s *recordingManagementServer) recorded() []recordedManagementCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]recordedManagementCall, len(s.calls))
	copy(out, s.calls)
	return out
}

func newV8TestClient(t *testing.T, server *httptest.Server) *managementClient {
	t.Helper()
	resetManagementVersionCache()
	t.Cleanup(resetManagementVersionCache)
	client, errClient := newManagementClient(server.URL, "management-secret", server.Client())
	if errClient != nil {
		t.Fatalf("newManagementClient() error = %v", errClient)
	}
	client.probeV8 = true
	return client
}

func TestV8ChannelWriteReadsETagAndPatchesGroup(t *testing.T) {
	items := []map[string]any{{
		"name":            "opencode",
		"base-url":        "https://example.test",
		"api-key-entries": []map[string]any{{"api-key": "test-key"}},
	}}
	server := &recordingManagementServer{handler: func(call recordedManagementCall) (int, http.Header, string) {
		switch {
		case call.Method == http.MethodGet && call.Path == "/v8/management/config":
			return http.StatusOK, nil, `{}`
		case call.Method == http.MethodGet && call.Path == "/v8/management/config/api-keys/openai-compatibility":
			return http.StatusOK, http.Header{"Etag": []string{`"etag-1"`}}, `[]`
		case call.Method == http.MethodPatch && call.Path == "/v8/management/config/api-keys/openai-compatibility":
			return http.StatusOK, nil, `{}`
		default:
			t.Errorf("unexpected call %s %s", call.Method, call.Path)
			return http.StatusNotFound, nil, `{"error":"not_found"}`
		}
	}}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	client := newV8TestClient(t, httpServer)
	if errWrite := client.putAIProviderChannel(context.Background(), "openai-compatibility", items); errWrite != nil {
		t.Fatalf("putAIProviderChannel() error = %v", errWrite)
	}

	calls := server.recorded()
	var patch *recordedManagementCall
	for index := range calls {
		if calls[index].Method == http.MethodPatch {
			patch = &calls[index]
		}
	}
	if patch == nil {
		t.Fatalf("no PATCH was sent, calls = %#v", calls)
	}
	if patch.IfMatch != `"etag-1"` {
		t.Fatalf("If-Match = %q, want the ETag read before the write", patch.IfMatch)
	}
	var decoded []map[string]any
	if errDecode := json.Unmarshal([]byte(patch.Body), &decoded); errDecode != nil {
		t.Fatalf("decode PATCH body: %v", errDecode)
	}
	// The v8 tree stores one group per base URL with a keys list, not a v0 row.
	if len(decoded) != 1 || decoded[0]["base-url"] != "https://example.test" {
		t.Fatalf("PATCH body = %s", patch.Body)
	}
	keys, ok := decoded[0]["keys"].([]any)
	if !ok || len(keys) != 1 {
		t.Fatalf("PATCH body has no keys: %s", patch.Body)
	}
	if key, _ := keys[0].(map[string]any)["api-key"].(string); key != "test-key" {
		t.Fatalf("key = %q body = %s", key, patch.Body)
	}
	for _, call := range calls {
		if call.Method == http.MethodPut {
			t.Fatalf("v8 host must not receive a v0 PUT, calls = %#v", calls)
		}
	}
}

func TestV8ChannelWriteRetriesOnceAfterPreconditionFailed(t *testing.T) {
	patches := 0
	gets := 0
	server := &recordingManagementServer{handler: func(call recordedManagementCall) (int, http.Header, string) {
		switch {
		case call.Method == http.MethodGet && call.Path == "/v8/management/config":
			return http.StatusOK, nil, `{}`
		case call.Method == http.MethodGet:
			gets++
			etag := `"etag-1"`
			if gets > 1 {
				etag = `"etag-2"`
			}
			return http.StatusOK, http.Header{"Etag": []string{etag}}, `[]`
		case call.Method == http.MethodPatch:
			patches++
			if patches == 1 {
				return http.StatusPreconditionFailed, nil, `{"error":"precondition_failed"}`
			}
			if call.IfMatch != `"etag-2"` {
				t.Errorf("retry If-Match = %q, want the re-read ETag", call.IfMatch)
			}
			return http.StatusOK, nil, `{}`
		default:
			return http.StatusNotFound, nil, `{}`
		}
	}}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	client := newV8TestClient(t, httpServer)
	errWrite := client.putAIProviderChannel(context.Background(), "openai-compatibility", []map[string]any{{"name": "a"}})
	if errWrite != nil {
		t.Fatalf("putAIProviderChannel() error = %v", errWrite)
	}
	if patches != 2 {
		t.Fatalf("PATCH count = %d, want exactly one retry", patches)
	}
	if gets != 2 {
		t.Fatalf("group GET count = %d, want a re-read before the retry", gets)
	}
}

func TestV8ChannelWriteFallsBackToV0WhenProbeFails(t *testing.T) {
	server := &recordingManagementServer{handler: func(call recordedManagementCall) (int, http.Header, string) {
		switch {
		case call.Path == "/v8/management/config":
			return http.StatusNotFound, nil, `{"error":"not_found"}`
		case call.Method == http.MethodPut && call.Path == "/v0/management/openai-compatibility":
			return http.StatusOK, nil, `{}`
		default:
			t.Errorf("unexpected call %s %s", call.Method, call.Path)
			return http.StatusNotFound, nil, `{}`
		}
	}}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	client := newV8TestClient(t, httpServer)
	if errWrite := client.putAIProviderChannel(context.Background(), "openai-compatibility", []map[string]any{{"name": "a"}}); errWrite != nil {
		t.Fatalf("putAIProviderChannel() error = %v", errWrite)
	}
	calls := server.recorded()
	if len(calls) != 2 || calls[1].Method != http.MethodPut {
		t.Fatalf("calls = %#v, want a probe then one v0 PUT", calls)
	}
}

func TestOlderHostNeverProbesV8(t *testing.T) {
	server := &recordingManagementServer{handler: func(call recordedManagementCall) (int, http.Header, string) {
		if call.Path == "/v8/management/config" {
			t.Errorf("an older host must not be probed")
		}
		return http.StatusOK, nil, `{}`
	}}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	resetManagementVersionCache()
	t.Cleanup(resetManagementVersionCache)

	client, errClient := newManagementClient(httpServer.URL, "management-secret", httpServer.Client())
	if errClient != nil {
		t.Fatalf("newManagementClient() error = %v", errClient)
	}
	if errWrite := client.putAIProviderChannel(context.Background(), "openai-compatibility", []map[string]any{{"name": "a"}}); errWrite != nil {
		t.Fatalf("putAIProviderChannel() error = %v", errWrite)
	}
	calls := server.recorded()
	if len(calls) != 1 || calls[0].Path != "/v0/management/openai-compatibility" {
		t.Fatalf("calls = %#v, want only the v0 PUT", calls)
	}
}

func TestRegistrationNeverClaimsNewerSchemaThanHost(t *testing.T) {
	cases := []struct {
		hostSchema  uint32
		wantSchema  uint32
		wantQuota   bool
		wantMulti   bool
		wantEntries bool
		description string
	}{
		{hostSchema: 1, wantSchema: 1, wantQuota: true, description: "legacy host"},
		{hostSchema: 2, wantSchema: 2, wantQuota: true, description: "lifecycle host"},
		{hostSchema: 8, wantSchema: 8, wantQuota: true, description: "pre-multi host"},
		{hostSchema: 9, wantSchema: 9, wantQuota: true, wantMulti: true, description: "multi-model host"},
		{hostSchema: 10, wantSchema: 10, wantQuota: true, wantMulti: true, wantEntries: true, description: "v8.0.60 host"},
		{hostSchema: 11, wantSchema: 10, wantQuota: true, wantMulti: true, wantEntries: true, description: "future host is capped"},
	}
	for _, testCase := range cases {
		app := NewApp(&fakeAuthHost{}, nil)
		app.ConfigureHost([]byte("data_dir: "+t.TempDir()), testCase.hostSchema)
		registration := app.Registration()
		if registration.SchemaVersion != testCase.wantSchema {
			t.Errorf("%s: schema = %d, want %d", testCase.description, registration.SchemaVersion, testCase.wantSchema)
		}
		if registration.Capabilities.QuotaProvider != testCase.wantQuota {
			t.Errorf("%s: QuotaProvider = %v, want %v", testCase.description, registration.Capabilities.QuotaProvider, testCase.wantQuota)
		}
		if registration.Capabilities.MultiProviderModels != testCase.wantMulti {
			t.Errorf("%s: MultiProviderModels = %v, want %v", testCase.description, registration.Capabilities.MultiProviderModels, testCase.wantMulti)
		}
		identifier := app.AuthIdentifierResponse()
		if got := len(identifier.Entries) > 0; got != testCase.wantEntries {
			t.Errorf("%s: entries present = %v, want %v", testCase.description, got, testCase.wantEntries)
		}
		if identifier.Identifier != "codex-agent-identity" {
			t.Errorf("%s: primary identifier = %q", testCase.description, identifier.Identifier)
		}
		app.Close()
	}
}

func TestQuotaWireTypesMatchHostContract(t *testing.T) {
	response := cpaapi.QuotaFetchResponse{
		Subscription: &cpaapi.QuotaSubscription{Plan: "go", TierName: "Go"},
		Summary:      []cpaapi.QuotaMetric{{Key: "balance", Label: "Balance", Value: 12.5, Format: "currency", Currency: "USD"}},
		Groups: []cpaapi.QuotaGroup{{
			DisplayName: "Allowance",
			Buckets:     []cpaapi.QuotaBucket{{Window: "5h", RemainingFraction: 0.4, ResetTime: "2026-11-08T10:00:00Z"}},
		}},
	}
	raw, errMarshal := json.Marshal(response)
	if errMarshal != nil {
		t.Fatalf("marshal: %v", errMarshal)
	}
	var decoded map[string]any
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("unmarshal: %v", errUnmarshal)
	}
	// The Management Center reads exactly these keys; a rename would silently
	// empty the plugin quota card.
	subscription, ok := decoded["subscription"].(map[string]any)
	if !ok || subscription["tierName"] != "Go" {
		t.Fatalf("subscription = %#v", decoded["subscription"])
	}
	groups, ok := decoded["groups"].([]any)
	if !ok || len(groups) != 1 {
		t.Fatalf("groups = %#v", decoded["groups"])
	}
	group := groups[0].(map[string]any)
	if group["displayName"] != "Allowance" {
		t.Fatalf("group = %#v", group)
	}
	buckets := group["buckets"].([]any)
	bucket := buckets[0].(map[string]any)
	if bucket["window"] != "5h" || bucket["remainingFraction"] != 0.4 || bucket["resetTime"] == "" {
		t.Fatalf("bucket = %#v", bucket)
	}
	summary := decoded["summary"].([]any)
	metric := summary[0].(map[string]any)
	if metric["format"] != "currency" || metric["currency"] != "USD" {
		t.Fatalf("summary = %#v", metric)
	}
}

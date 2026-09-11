package manager

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

const (
	openCodeSessionTestModel     = "opencode-go/claude-sonnet-4"
	openCodeSessionTestAuthIndex = "auth-index-opencode-1"
	openCodeSessionTestBody      = `{"messages":[{"role":"user","content":"hello"}]}`
)

// newTestOpenCodeSessionRouter builds an active router: enabled, with a salt on
// disk and the OpenCode auth-index allow-list that attribution depends on.
func newTestOpenCodeSessionRouter(t *testing.T, dataDir string, targets ...string) *OpenCodeSessionRouter {
	t.Helper()
	if len(targets) == 0 {
		targets = []string{openCodeSessionTestModel}
	}
	router := NewOpenCodeSessionRouter()
	router.Configure(Config{DataDir: dataDir})
	router.SetAuthIndexes([]string{openCodeSessionTestAuthIndex})
	router.SetTargets(targets)
	router.SetEnabled(true)
	if !router.RequestInterceptionActive() {
		t.Fatal("router did not become active after Configure")
	}
	return router
}

func openCodeSessionTestAttribution() map[string]any {
	return map[string]any{"selected_auth_index": openCodeSessionTestAuthIndex}
}

func openCodeSessionTestRequest(requestID, model string, headers http.Header, body string) cpaapi.RequestInterceptRequest {
	return openCodeSessionTestRequestWithMetadata(requestID, model, openCodeSessionTestAttribution(), headers, body)
}

func openCodeSessionTestRequestWithMetadata(requestID, model string, metadata map[string]any, headers http.Header, body string) cpaapi.RequestInterceptRequest {
	return cpaapi.RequestInterceptRequest{
		RequestID: requestID,
		Model:     model,
		Headers:   headers,
		Body:      []byte(body),
		Metadata:  metadata,
	}
}

func assertOpenCodeSessionNotIntercepted(t *testing.T, router *OpenCodeSessionRouter, response cpaapi.RequestInterceptResponse, changed bool) {
	t.Helper()
	if changed {
		t.Fatal("request was intercepted")
	}
	if len(response.Headers) != 0 {
		t.Fatalf("response headers = %#v, want none", response.Headers)
	}
	if snapshot := router.Snapshot(); snapshot.InjectedRequests != 0 || snapshot.DistinctSessions != 0 {
		t.Fatalf("counters = injected %d distinct %d, want 0 and 0", snapshot.InjectedRequests, snapshot.DistinctSessions)
	}
}

func TestOpenCodeSessionRouterStableValueForSameConversation(t *testing.T) {
	router := newTestOpenCodeSessionRouter(t, t.TempDir(), openCodeSessionTestModel)
	body := `{"messages":[{"role":"system","content":"You are a careful assistant."},{"role":"user","content":"Count the primes below one hundred."}]}`

	first, changed := router.InterceptRequest(openCodeSessionTestRequest("req-1", openCodeSessionTestModel, nil, body))
	if !changed {
		t.Fatal("first attributed request was not intercepted")
	}
	second, changed := router.InterceptRequest(openCodeSessionTestRequest("req-2", openCodeSessionTestModel, nil, body))
	if !changed {
		t.Fatal("second attributed request was not intercepted")
	}
	firstValue := first.Headers.Get(openCodeSessionHeader)
	secondValue := second.Headers.Get(openCodeSessionHeader)
	if firstValue == "" || firstValue != secondValue {
		t.Fatalf("session values = %q and %q, want one stable value", firstValue, secondValue)
	}
	if !strings.HasPrefix(firstValue, openCodeSessionValuePrefix) {
		t.Fatalf("fallback session %q must start with %q", firstValue, openCodeSessionValuePrefix)
	}
	if got := first.Headers.Get(openCodeClientHeader); got != openCodeClientHeaderValue {
		t.Fatalf("%s = %q, want %q", openCodeClientHeader, got, openCodeClientHeaderValue)
	}

	snapshot := router.Snapshot()
	if !snapshot.Enabled || !snapshot.SaltReady {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.InjectedRequests != 2 || snapshot.DistinctSessions != 1 {
		t.Fatalf("counters = injected %d distinct %d, want 2 and 1", snapshot.InjectedRequests, snapshot.DistinctSessions)
	}
	if snapshot.LastInjectedAt.IsZero() {
		t.Fatal("last injected timestamp was not recorded")
	}
	if len(snapshot.TargetModels) != 1 || snapshot.TargetModels[0] != openCodeSessionTestModel {
		t.Fatalf("target models = %v", snapshot.TargetModels)
	}
	if snapshot.TargetAuthIndexes != 1 {
		t.Fatalf("target auth indexes = %d, want 1", snapshot.TargetAuthIndexes)
	}
}

func TestOpenCodeSessionRouterDifferentConversationsGetDifferentValues(t *testing.T) {
	router := newTestOpenCodeSessionRouter(t, t.TempDir(), openCodeSessionTestModel)

	first, changed := router.InterceptRequest(openCodeSessionTestRequest("req-1", openCodeSessionTestModel, nil,
		`{"messages":[{"role":"user","content":"Explain quicksort."}]}`))
	if !changed {
		t.Fatal("first conversation was not intercepted")
	}
	second, changed := router.InterceptRequest(openCodeSessionTestRequest("req-2", openCodeSessionTestModel, nil,
		`{"messages":[{"role":"user","content":"Explain mergesort."}]}`))
	if !changed {
		t.Fatal("second conversation was not intercepted")
	}
	firstValue := first.Headers.Get(openCodeSessionHeader)
	secondValue := second.Headers.Get(openCodeSessionHeader)
	if firstValue == "" || firstValue == secondValue {
		t.Fatalf("session values = %q and %q, want different values", firstValue, secondValue)
	}
	if snapshot := router.Snapshot(); snapshot.DistinctSessions != 2 {
		t.Fatalf("distinct sessions = %d, want 2", snapshot.DistinctSessions)
	}
}

func TestOpenCodeSessionRouterOnlyInterceptsAttributedRequests(t *testing.T) {
	t.Run("no metadata", func(t *testing.T) {
		router := newTestOpenCodeSessionRouter(t, t.TempDir(), openCodeSessionTestModel)
		response, changed := router.InterceptRequest(openCodeSessionTestRequestWithMetadata(
			"req-1", openCodeSessionTestModel, nil, nil, openCodeSessionTestBody))
		assertOpenCodeSessionNotIntercepted(t, router, response, changed)
	})

	t.Run("empty metadata", func(t *testing.T) {
		router := newTestOpenCodeSessionRouter(t, t.TempDir(), openCodeSessionTestModel)
		response, changed := router.InterceptRequest(openCodeSessionTestRequestWithMetadata(
			"req-1", openCodeSessionTestModel, map[string]any{}, nil, openCodeSessionTestBody))
		assertOpenCodeSessionNotIntercepted(t, router, response, changed)
	})

	t.Run("unknown auth index", func(t *testing.T) {
		router := newTestOpenCodeSessionRouter(t, t.TempDir(), openCodeSessionTestModel)
		metadata := map[string]any{"selected_auth_index": "auth-index-not-opencode"}
		response, changed := router.InterceptRequest(openCodeSessionTestRequestWithMetadata(
			"req-1", openCodeSessionTestModel, metadata, nil, openCodeSessionTestBody))
		assertOpenCodeSessionNotIntercepted(t, router, response, changed)
	})

	t.Run("known auth index outside model catalog", func(t *testing.T) {
		router := newTestOpenCodeSessionRouter(t, t.TempDir(), openCodeSessionTestModel)
		response, changed := router.InterceptRequest(openCodeSessionTestRequest(
			"req-1", "gpt-5.5-vendor-model-outside-opencode-catalog", nil, openCodeSessionTestBody))
		if !changed {
			t.Fatal("attributed request was not intercepted")
		}
		value := response.Headers.Get(openCodeSessionHeader)
		if !strings.HasPrefix(value, openCodeSessionValuePrefix) {
			t.Fatalf("session value = %q, want the fallback prefix", value)
		}
		snapshot := router.Snapshot()
		if len(snapshot.TargetModels) != 1 || snapshot.TargetModels[0] != openCodeSessionTestModel {
			t.Fatalf("target models = %v, want only %q", snapshot.TargetModels, openCodeSessionTestModel)
		}
		if snapshot.InjectedRequests != 1 {
			t.Fatalf("injected requests = %d, want 1: attribution, not the model id, decides", snapshot.InjectedRequests)
		}
	})
}

func TestOpenCodeSessionRouterAcceptsEveryAttributionMetadataKey(t *testing.T) {
	keys := []string{"selected_auth_index", "selected_auth_id", "auth_index", "auth_id"}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			router := newTestOpenCodeSessionRouter(t, t.TempDir(), openCodeSessionTestModel)
			metadata := map[string]any{key: openCodeSessionTestAuthIndex}
			response, changed := router.InterceptRequest(openCodeSessionTestRequestWithMetadata(
				"req-1", openCodeSessionTestModel, metadata, nil, openCodeSessionTestBody))
			if !changed {
				t.Fatalf("metadata key %q did not attribute the request", key)
			}
			if value := response.Headers.Get(openCodeSessionHeader); value == "" {
				t.Fatalf("%s was not set", openCodeSessionHeader)
			}
		})
	}
}

func TestOpenCodeSessionRouterPreservesClientSession(t *testing.T) {
	router := newTestOpenCodeSessionRouter(t, t.TempDir(), openCodeSessionTestModel)
	const clientSession = "client-Session.abc_DEF-123"
	headers := http.Header{}
	headers.Set(openCodeSessionHeader, clientSession)
	headers.Set("x-session-id", "native-ignored")

	response, changed := router.InterceptRequest(openCodeSessionTestRequest("req-1", openCodeSessionTestModel, headers,
		`{"prompt_cache_key":"body-ignored","messages":[{"role":"user","content":"hello"}]}`))
	if !changed {
		t.Fatal("attributed request was not intercepted")
	}
	if got := response.Headers.Get(openCodeSessionHeader); got != clientSession {
		t.Fatalf("%s = %q, want the client-provided value %q", openCodeSessionHeader, got, clientSession)
	}
}

func TestOpenCodeSessionRouterTranslatesNativeSessionHeaders(t *testing.T) {
	headers := []string{
		"x-session-id",
		"x-claude-session-id",
		"x-claude-code-session-id",
		"session-id",
		"x-codex-session-id",
		"conversation-id",
		"x-conversation-id",
	}
	for _, name := range headers {
		t.Run(name, func(t *testing.T) {
			router := newTestOpenCodeSessionRouter(t, t.TempDir(), openCodeSessionTestModel)
			requestHeaders := http.Header{}
			requestHeaders.Set(name, "native-42")

			response, changed := router.InterceptRequest(openCodeSessionTestRequest("req-1", openCodeSessionTestModel, requestHeaders,
				openCodeSessionTestBody))
			if !changed {
				t.Fatalf("native session header %q was not translated", name)
			}
			if got := response.Headers.Get(openCodeSessionHeader); got != "native-42" {
				t.Fatalf("%s = %q, want native-42", openCodeSessionHeader, got)
			}
		})
	}
}

func TestOpenCodeSessionRouterUsesBodySessionHints(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "prompt cache key",
			body: `{"prompt_cache_key":"cache-key-7","messages":[{"role":"user","content":"hello"}]}`,
			want: "cache-key-7",
		},
		{name: "session id", body: `{"session_id":"body-session-2"}`, want: "body-session-2"},
		{name: "conversation id", body: `{"conversation_id":"body-conversation-3"}`, want: "body-conversation-3"},
		{name: "metadata prompt cache key", body: `{"metadata":{"prompt_cache_key":"metadata-key-4"}}`, want: "metadata-key-4"},
		{name: "metadata session id", body: `{"metadata":{"session_id":"metadata-session-5"}}`, want: "metadata-session-5"},
		{name: "metadata conversation id", body: `{"metadata":{"conversation_id":"metadata-conversation-6"}}`, want: "metadata-conversation-6"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := newTestOpenCodeSessionRouter(t, t.TempDir(), openCodeSessionTestModel)
			response, changed := router.InterceptRequest(openCodeSessionTestRequest("req-1", openCodeSessionTestModel, nil, test.body))
			if !changed {
				t.Fatalf("body hint %q was not used", test.want)
			}
			if got := response.Headers.Get(openCodeSessionHeader); got != test.want {
				t.Fatalf("%s = %q, want %q", openCodeSessionHeader, got, test.want)
			}
		})
	}
}

func TestOpenCodeSessionRouterFallbackNeverEchoesMessageText(t *testing.T) {
	router := newTestOpenCodeSessionRouter(t, t.TempDir(), openCodeSessionTestModel)
	canary := "CANARY-7f3d9b1e5a2c4d6f-private-marker"
	body, errMarshal := json.Marshal(map[string]any{
		"system": "Keep secrets secret.",
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "Please remember " + canary + " forever."},
				map[string]any{"type": "image", "source": map[string]any{"data": canary}},
			}},
		},
	})
	if errMarshal != nil {
		t.Fatalf("marshal body: %v", errMarshal)
	}

	response, changed := router.InterceptRequest(openCodeSessionTestRequest("req-1", openCodeSessionTestModel, nil, string(body)))
	if !changed {
		t.Fatal("fallback was not injected")
	}
	value := response.Headers.Get(openCodeSessionHeader)
	if !strings.HasPrefix(value, openCodeSessionValuePrefix) {
		t.Fatalf("fallback session %q must start with %q", value, openCodeSessionValuePrefix)
	}
	if strings.Contains(value, canary) {
		t.Fatal("injected session leaked message text")
	}

	repeat, changed := router.InterceptRequest(openCodeSessionTestRequest("req-2", openCodeSessionTestModel, nil, string(body)))
	if !changed || repeat.Headers.Get(openCodeSessionHeader) != value {
		t.Fatalf("fallback was not stable: %q vs %q", value, repeat.Headers.Get(openCodeSessionHeader))
	}
}

func TestOpenCodeSessionRouterDisabledDoesNotInject(t *testing.T) {
	router := NewOpenCodeSessionRouter()
	router.Configure(Config{DataDir: t.TempDir()})
	router.SetAuthIndexes([]string{openCodeSessionTestAuthIndex})
	router.SetTargets([]string{openCodeSessionTestModel})
	router.SetEnabled(false)
	if router.RequestInterceptionActive() {
		t.Fatal("disabled router reported itself active")
	}

	response, changed := router.InterceptRequest(openCodeSessionTestRequest("req-1", openCodeSessionTestModel, nil,
		openCodeSessionTestBody))
	assertOpenCodeSessionNotIntercepted(t, router, response, changed)
}

func TestOpenCodeSessionRouterRequiresAuthIndexesToBeActive(t *testing.T) {
	router := NewOpenCodeSessionRouter()
	router.Configure(Config{DataDir: t.TempDir()})
	router.SetEnabled(true)
	router.SetTargets([]string{openCodeSessionTestModel})
	if router.RequestInterceptionActive() {
		t.Fatal("router without auth indexes reported itself active")
	}
	snapshot := router.Snapshot()
	if !snapshot.Enabled || !snapshot.SaltReady {
		t.Fatalf("snapshot = %+v, want enabled with a salt", snapshot)
	}
	if snapshot.TargetAuthIndexes != 0 {
		t.Fatalf("target auth indexes = %d, want 0", snapshot.TargetAuthIndexes)
	}

	response, changed := router.InterceptRequest(openCodeSessionTestRequest("req-1", openCodeSessionTestModel, nil,
		openCodeSessionTestBody))
	assertOpenCodeSessionNotIntercepted(t, router, response, changed)

	router.SetAuthIndexes([]string{openCodeSessionTestAuthIndex})
	if !router.RequestInterceptionActive() {
		t.Fatal("router did not become active after SetAuthIndexes")
	}
	if got := router.Snapshot().TargetAuthIndexes; got != 1 {
		t.Fatalf("target auth indexes = %d, want 1", got)
	}
	response, changed = router.InterceptRequest(openCodeSessionTestRequest("req-2", openCodeSessionTestModel, nil,
		openCodeSessionTestBody))
	if !changed {
		t.Fatal("attributed request was not intercepted after SetAuthIndexes")
	}
}

func TestOpenCodeSessionRouterConfigureCreatesAndReusesSalt(t *testing.T) {
	dataDir := t.TempDir()
	saltPath := filepath.Join(dataDir, openCodeSessionSaltFile)
	body := `{"messages":[{"role":"user","content":"Keep this conversation stable."}]}`

	first := newTestOpenCodeSessionRouter(t, dataDir, openCodeSessionTestModel)
	info, errStat := os.Stat(saltPath)
	if errStat != nil {
		t.Fatalf("salt file missing: %v", errStat)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("salt permissions = %#o, want 0600", info.Mode().Perm())
	}
	firstRaw, errRead := os.ReadFile(saltPath)
	if errRead != nil {
		t.Fatalf("read salt: %v", errRead)
	}
	if strings.TrimSpace(string(firstRaw)) == "" {
		t.Fatal("salt file is empty")
	}

	firstResponse, changed := first.InterceptRequest(openCodeSessionTestRequest("req-1", openCodeSessionTestModel, nil, body))
	if !changed {
		t.Fatal("first router did not inject")
	}
	firstValue := firstResponse.Headers.Get(openCodeSessionHeader)

	second := newTestOpenCodeSessionRouter(t, dataDir, openCodeSessionTestModel)
	secondResponse, changed := second.InterceptRequest(openCodeSessionTestRequest("req-2", openCodeSessionTestModel, nil, body))
	if !changed {
		t.Fatal("second router did not inject")
	}
	secondValue := secondResponse.Headers.Get(openCodeSessionHeader)
	if firstValue == "" || firstValue != secondValue {
		t.Fatalf("salt reuse changed the fallback session: %q vs %q", firstValue, secondValue)
	}
	secondRaw, errRead := os.ReadFile(saltPath)
	if errRead != nil {
		t.Fatalf("read salt after reuse: %v", errRead)
	}
	if string(secondRaw) != string(firstRaw) {
		t.Fatal("Configure replaced the persisted salt")
	}

	third := newTestOpenCodeSessionRouter(t, t.TempDir(), openCodeSessionTestModel)
	thirdResponse, changed := third.InterceptRequest(openCodeSessionTestRequest("req-3", openCodeSessionTestModel, nil, body))
	if !changed {
		t.Fatal("router with a fresh salt did not inject")
	}
	if thirdResponse.Headers.Get(openCodeSessionHeader) == firstValue {
		t.Fatal("a different data dir produced the same fallback session")
	}
}

func TestOpenCodeSessionRouterRejectsUnsafeClientValues(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "spaces", value: "client session with spaces"},
		{name: "tab", value: "session-\t-1"},
		{name: "newline injection", value: "session-1\r\nx-evil: 1"},
		{name: "control character", value: "session-\x00-1"},
		{name: "too long", value: strings.Repeat("a", openCodeSessionMaxValueBytes+1)},
		{name: "non ascii", value: "sessão-1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := newTestOpenCodeSessionRouter(t, t.TempDir(), openCodeSessionTestModel)
			headers := http.Header{}
			headers.Set(openCodeSessionHeader, test.value)

			response, changed := router.InterceptRequest(openCodeSessionTestRequest("req-1", openCodeSessionTestModel, headers,
				openCodeSessionTestBody))
			if !changed {
				t.Fatal("router did not fall back after an unsafe client session")
			}
			value := response.Headers.Get(openCodeSessionHeader)
			if value == test.value {
				t.Fatal("unsafe client value was preserved")
			}
			if !strings.HasPrefix(value, openCodeSessionValuePrefix) {
				t.Fatalf("value = %q, want the fallback prefix", value)
			}
			if strings.ContainsAny(value, " \r\n\t\x00") {
				t.Fatalf("injected value %q contains unsafe characters", value)
			}
		})
	}
}

package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

// aiProviderNameTestChannel returns a channel list that contains two providers
// sharing a base URL with different keys, and two providers sharing a key with
// different base URLs. Only an identity that binds both fields can keep them
// apart.
func aiProviderNameTestChannelJSON() string {
	return `{"codex-api-key":[
		{"base-url":"https://shared.example/v1","api-key":"sk-alpha"},
		{"base-url":"https://shared.example/v1","api-key":"sk-beta"},
		{"base-url":"https://other.example/v1","api-key":"sk-alpha"}
	]}`
}

func aiProviderNameTestApp(t *testing.T, channels string) *App {
	t.Helper()
	service := NewAIProviderNameService()
	service.Configure(Config{DataDir: t.TempDir()})
	app := &App{aiProviderNames: service}
	app.managementDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet {
			return jsonHTTPResponse(http.StatusMethodNotAllowed, `{}`), nil
		}
		if !strings.Contains(request.URL.Path, "/codex-api-key") {
			return jsonHTTPResponse(http.StatusNotFound, `{}`), nil
		}
		return jsonHTTPResponse(http.StatusOK, channels), nil
	})
	return app
}

func aiProviderNameTestHeaders() http.Header {
	return http.Header{"Authorization": []string{"Bearer test-management-key"}}
}

func aiProviderNameBasePath() string {
	return "/v0/management" + managementRoutePrefix + "/ai-provider-names"
}

func putAIProviderName(t *testing.T, app *App, payload string) cpaapi.ManagementResponse {
	t.Helper()
	return app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method:  http.MethodPut,
		Path:    aiProviderNameBasePath(),
		Headers: aiProviderNameTestHeaders(),
		Body:    []byte(payload),
	})
}

func getAIProviderNames(t *testing.T, app *App) AIProviderNameSnapshot {
	t.Helper()
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method:  http.MethodGet,
		Path:    aiProviderNameBasePath(),
		Headers: aiProviderNameTestHeaders(),
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, body=%s", response.StatusCode, response.Body)
	}
	var snapshot AIProviderNameSnapshot
	if errDecode := json.Unmarshal(response.Body, &snapshot); errDecode != nil {
		t.Fatalf("decode snapshot: %v", errDecode)
	}
	return snapshot
}

func aiProviderNameForIndex(snapshot AIProviderNameSnapshot, kind string, index int) string {
	for _, assignment := range snapshot.Names {
		if assignment.Kind == kind && assignment.Index == index {
			return assignment.Name
		}
	}
	return ""
}

func TestAIProviderNameServicePersistsPepperAndLabels(t *testing.T) {
	dir := t.TempDir()
	service := NewAIProviderNameService()
	service.Configure(Config{DataDir: dir})
	key := service.CredentialKey("codex-api-key", "https://shared.example/v1", "sk-alpha")
	// The digest binds the base URL and the credential together.
	if same := service.CredentialKey("codex-api-key", "https://shared.example/v1", "sk-beta"); same == key {
		t.Fatal("different credentials produced the same name key")
	}
	if same := service.CredentialKey("codex-api-key", "https://other.example/v1", "sk-alpha"); same == key {
		t.Fatal("different base URLs produced the same name key")
	}
	if normalized, errAssign := service.Assign([]string{key}, "  Lab   Gateway "); errAssign != nil || normalized != "Lab Gateway" {
		t.Fatalf("assign = %q, %v", normalized, errAssign)
	}

	info, errStat := os.Stat(filepath.Join(dir, aiProviderNameStoreFile))
	if errStat != nil {
		t.Fatalf("stat name store: %v", errStat)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("name store mode = %o, want 600", info.Mode().Perm())
	}
	raw, errRead := os.ReadFile(filepath.Join(dir, aiProviderNameStoreFile))
	if errRead != nil {
		t.Fatalf("read name store: %v", errRead)
	}
	if strings.Contains(string(raw), "sk-alpha") || strings.Contains(string(raw), "shared.example") {
		t.Fatal("name store leaked channel credentials or base URLs")
	}

	reloaded := NewAIProviderNameService()
	reloaded.Configure(Config{DataDir: dir})
	if name, ok := reloaded.Name(reloaded.CredentialKey("codex-api-key", "https://shared.example/v1", "sk-alpha")); !ok || name != "Lab Gateway" {
		t.Fatalf("reloaded name = %q, found=%t", name, ok)
	}
	if _, errClear := reloaded.Assign([]string{key}, ""); errClear != nil {
		t.Fatalf("clear name: %v", errClear)
	}
	if _, ok := reloaded.Name(key); ok {
		t.Fatal("cleared name survived")
	}
}

func TestAIProviderNameServiceRejectsInvalidInput(t *testing.T) {
	service := NewAIProviderNameService()
	service.Configure(Config{DataDir: t.TempDir()})
	if _, err := service.Assign(nil, "name"); err == nil {
		t.Fatal("empty key list was accepted")
	}
	if _, err := service.Assign([]string{"codex-api-key:cred:abc\ninjected"}, "name"); err == nil {
		t.Fatal("control character in key was accepted")
	}
	key := service.CredentialKey("codex-api-key", "https://shared.example/v1", "sk-alpha")
	if _, err := service.Assign([]string{key}, strings.Repeat("n", aiProviderNameMaxLength+1)); err == nil {
		t.Fatal("oversized name was accepted")
	}
	unconfigured := NewAIProviderNameService()
	if _, err := unconfigured.Assign([]string{key}, "name"); err == nil {
		t.Fatal("unconfigured service accepted a write")
	}
}

func TestAIProviderNameServicePrunesOnlyItsOwnKind(t *testing.T) {
	service := NewAIProviderNameService()
	service.Configure(Config{DataDir: t.TempDir()})
	keepKey := service.CredentialKey("codex-api-key", "https://shared.example/v1", "sk-alpha")
	dropKey := service.CredentialKey("codex-api-key", "https://gone.example/v1", "sk-gone")
	otherKey := service.CredentialKey("claude-api-key", "https://shared.example/v1", "sk-alpha")
	for _, key := range []string{keepKey, dropKey, otherKey} {
		if _, err := service.Assign([]string{key}, "name"); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}
	if errPrune := service.PruneKind("codex-api-key", map[string]struct{}{keepKey: {}}); errPrune != nil {
		t.Fatalf("prune: %v", errPrune)
	}
	if _, ok := service.Name(dropKey); ok {
		t.Fatal("orphaned codex label survived pruning")
	}
	if _, ok := service.Name(keepKey); !ok {
		t.Fatal("live codex label was pruned")
	}
	if _, ok := service.Name(otherKey); !ok {
		t.Fatal("unrelated provider kind was pruned")
	}
}

func TestAIProviderNameManagementAPIKeepsSimilarProvidersApart(t *testing.T) {
	app := aiProviderNameTestApp(t, aiProviderNameTestChannelJSON())

	if unauthorized := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{Method: http.MethodGet, Path: aiProviderNameBasePath()}); unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated get status = %d", unauthorized.StatusCode)
	}

	// Same base URL as index 1, different key.
	if response := putAIProviderName(t, app, `{"kind":"codex-api-key","index":0,"base_url":"https://shared.example/v1","name":"Alpha gateway"}`); response.StatusCode != http.StatusOK {
		t.Fatalf("put alpha status = %d, body=%s", response.StatusCode, response.Body)
	}
	if response := putAIProviderName(t, app, `{"kind":"codex-api-key","index":1,"base_url":"https://shared.example/v1","name":"Beta gateway"}`); response.StatusCode != http.StatusOK {
		t.Fatalf("put beta status = %d, body=%s", response.StatusCode, response.Body)
	}
	// Same key as index 0, different base URL.
	if response := putAIProviderName(t, app, `{"kind":"codex-api-key","index":2,"base_url":"https://other.example/v1","name":"Mirror gateway"}`); response.StatusCode != http.StatusOK {
		t.Fatalf("put mirror status = %d, body=%s", response.StatusCode, response.Body)
	}

	snapshot := getAIProviderNames(t, app)
	for index, want := range map[int]string{0: "Alpha gateway", 1: "Beta gateway", 2: "Mirror gateway"} {
		if got := aiProviderNameForIndex(snapshot, "codex-api-key", index); got != want {
			t.Fatalf("name for index %d = %q, want %q", index, got, want)
		}
	}
}

func TestAIProviderNameManagementAPIRevalidatesAfterReorderAndKeyChange(t *testing.T) {
	app := aiProviderNameTestApp(t, aiProviderNameTestChannelJSON())
	if response := putAIProviderName(t, app, `{"kind":"codex-api-key","index":0,"base_url":"https://shared.example/v1","name":"Alpha gateway"}`); response.StatusCode != http.StatusOK {
		t.Fatalf("put status = %d, body=%s", response.StatusCode, response.Body)
	}

	// CPA reorders the array and rewrites an unrelated credential. The label must
	// follow the base URL plus key pair instead of the old array position.
	app.managementDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		return jsonHTTPResponse(http.StatusOK, `{"codex-api-key":[
			{"base-url":"https://other.example/v1","api-key":"sk-rotated"},
			{"base-url":"https://shared.example/v1","api-key":"sk-alpha"}
		]}`), nil
	})
	snapshot := getAIProviderNames(t, app)
	if got := aiProviderNameForIndex(snapshot, "codex-api-key", 1); got != "Alpha gateway" {
		t.Fatalf("label after reorder = %q, want %q", got, "Alpha gateway")
	}
	if got := aiProviderNameForIndex(snapshot, "codex-api-key", 0); got != "" {
		t.Fatalf("label leaked onto a rotated credential: %q", got)
	}
	if len(snapshot.Names) != 1 {
		t.Fatalf("assignments = %#v, want exactly one", snapshot.Names)
	}
}

func TestAIProviderNameManagementAPIRejectsStaleAndUnsupportedWrites(t *testing.T) {
	app := aiProviderNameTestApp(t, aiProviderNameTestChannelJSON())

	stale := putAIProviderName(t, app, `{"kind":"codex-api-key","index":0,"base_url":"https://moved.example/v1","name":"Wrong"}`)
	if stale.StatusCode != http.StatusConflict {
		t.Fatalf("stale base URL status = %d, want 409", stale.StatusCode)
	}
	outOfRange := putAIProviderName(t, app, `{"kind":"codex-api-key","index":9,"base_url":"https://shared.example/v1","name":"Wrong"}`)
	if outOfRange.StatusCode != http.StatusConflict {
		t.Fatalf("out-of-range index status = %d, want 409", outOfRange.StatusCode)
	}
	unsupported := putAIProviderName(t, app, `{"kind":"opencode-go","index":0,"name":"Wrong"}`)
	if unsupported.StatusCode != http.StatusBadRequest {
		t.Fatalf("unsupported kind status = %d, want 400", unsupported.StatusCode)
	}
	if names := app.aiProviderNames.Snapshot(); len(names) != 0 {
		t.Fatalf("rejected writes persisted state: %#v", names)
	}
}

func TestAIProviderNameManagementAPIClearsLabelOnRequest(t *testing.T) {
	app := aiProviderNameTestApp(t, aiProviderNameTestChannelJSON())
	if response := putAIProviderName(t, app, `{"kind":"codex-api-key","index":0,"base_url":"https://shared.example/v1","name":"Alpha gateway"}`); response.StatusCode != http.StatusOK {
		t.Fatalf("put status = %d", response.StatusCode)
	}
	if response := putAIProviderName(t, app, `{"kind":"codex-api-key","index":0,"base_url":"https://shared.example/v1","name":""}`); response.StatusCode != http.StatusOK {
		t.Fatalf("clear status = %d, body=%s", response.StatusCode, response.Body)
	}
	if snapshot := getAIProviderNames(t, app); len(snapshot.Names) != 0 {
		t.Fatalf("cleared label still resolved: %#v", snapshot.Names)
	}
}

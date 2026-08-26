package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func zenTestServer(t *testing.T, secret string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret && r.Header.Get("x-api-key") != secret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"glm-5.1","object":"model"}]}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestProbeOpenCodeZenEndpointReachable(t *testing.T) {
	server := zenTestServer(t, "sk-zen-secret")
	service := NewOpenCodeZenService()
	result := service.Probe(context.Background(), server.URL, "sk-zen-secret", 5*time.Second)
	if !result.Reachable || result.StatusCode != http.StatusOK {
		t.Fatalf("result = %+v", result)
	}
	if result.Detail != "reachable" {
		t.Fatalf("detail = %q", result.Detail)
	}
}

func TestProbeOpenCodeEndpointRejectsBadCredential(t *testing.T) {
	server := zenTestServer(t, "sk-zen-secret")
	service := NewOpenCodeZenService()
	result := service.Probe(context.Background(), server.URL, "sk-wrong", 5*time.Second)
	if result.Reachable {
		t.Fatalf("expected unreachable, got %+v", result)
	}
	if !strings.Contains(result.Detail, "rejected the credential") {
		t.Fatalf("detail = %q", result.Detail)
	}
}

func TestProbeOpenCodeZenFallsBackToZenNativePath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/zen/v1/models" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	result := NewOpenCodeZenService().Probe(context.Background(), server.URL, "k", 5*time.Second)
	if !result.Reachable {
		t.Fatalf("expected /zen/v1/models fallback to succeed, got %+v", result)
	}
}

func TestOpenCodeZenServicePersistsRedactsAndRemoves(t *testing.T) {
	dataDir := t.TempDir()
	service := NewOpenCodeZenService()
	service.Configure(Config{DataDir: dataDir})
	id, errSave := service.SaveAccount("", "main bridge", "http://localhost:8787", "sk-zen-first")
	if errSave != nil || id == "" {
		t.Fatalf("save: id=%q err=%v", id, errSave)
	}
	_, errSecond := service.SaveAccount("", "direct zen", "https://opencode.ai/zen", "sk-zen-second")
	if errSecond != nil {
		t.Fatalf("second save: %v", errSecond)
	}
	rawStore := readOpenCodeFileStore(t, dataDir)
	if !strings.Contains(rawStore, "sk-zen-first") || strings.Contains(rawStore, "sk-zen-second") == false {
		t.Fatalf("store must persist secrets; raw = %s", rawStore)
	}

	views := service.ListAccounts()
	if len(views) != 2 {
		t.Fatalf("views = %+v", views)
	}
	for _, view := range views {
		if view.BaseURL == "" || !view.KeySet {
			t.Fatalf("view missing data: %+v", view)
		}
		if strings.Contains(view.BaseURL, "sk-zen") {
			t.Fatalf("view leaked a secret: %+v", view)
		}
	}

	// Reload from disk and verify the update path keeps the stored key.
	reloaded := NewOpenCodeZenService()
	reloaded.Configure(Config{DataDir: dataDir})
	updatedID, errUpdate := reloaded.SaveAccount(id, "renamed", "https://opencode.ai/zen", "")
	if errUpdate != nil || updatedID != id {
		t.Fatalf("update: id=%q err=%v", updatedID, errUpdate)
	}
	if got := reloaded.ListAccounts()[0].Name; got != "renamed" {
		t.Fatalf("name after update = %q", got)
	}
	rawAfter := readOpenCodeFileStore(t, dataDir)
	if !strings.Contains(rawAfter, "sk-zen-first") {
		t.Fatalf("update must keep the stored key, raw = %s", rawAfter)
	}
	if errRemove := reloaded.RemoveAccount(id); errRemove != nil {
		t.Fatalf("remove: %v", errRemove)
	}
	if got := len(reloaded.ListAccounts()); got != 1 {
		t.Fatalf("accounts after remove = %d", got)
	}
}

func readOpenCodeFileStore(t *testing.T, dataDir string) string {
	t.Helper()
	raw, errRead := os.ReadFile(filepath.Join(dataDir, "opencode-zen.json"))
	if errRead != nil {
		t.Fatalf("read store: %v", errRead)
	}
	return string(raw)
}

func TestOpenCodeZenManagementRoutes(t *testing.T) {
	apps := &App{
		opencodeZen: NewOpenCodeZenService(),
		operations:  NewOperationJournal(),
	}
	apps.opencodeZen.Configure(Config{DataDir: t.TempDir()})
	managementOK := cpaapi.ManagementRequest{
		Method:  http.MethodPost,
		Path:    "/v0/management/plugins/cpa-account-config-manager/opencode/zen/accounts",
		Headers: http.Header{"X-Management-Key": []string{"management-secret"}},
		Body:    []byte(`{"name":"bridge","base_url":"http://127.0.0.1:1","zen_api_key":"sk-zen-route"}`),
	}
	var savedResponse map[string]any
	response := apps.HandleManagement(context.Background(), managementOK)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("save status = %d, body = %s", response.StatusCode, response.Body)
	}
	if errJSON := json.Unmarshal(response.Body, &savedResponse); errJSON != nil {
		t.Fatalf("save body = %s", response.Body)
	}
	accountID, _ := savedResponse["account"].(map[string]any)["id"].(string)
	if accountID == "" {
		t.Fatalf("save response missing account id: %s", response.Body)
	}

	list := apps.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-account-config-manager/opencode/zen/accounts",
	})
	if list.StatusCode != http.StatusOK || strings.Contains(string(list.Body), "sk-zen-route") {
		t.Fatalf("list must be redacted: status=%d body=%s", list.StatusCode, list.Body)
	}

	probe := apps.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method:  http.MethodPost,
		Path:    "/v0/management/plugins/cpa-account-config-manager/opencode/zen/probe-account",
		Query:   map[string][]string{"account_id": {accountID}},
		Headers: http.Header{"X-Management-Key": []string{"management-secret"}},
	})
	if probe.StatusCode != http.StatusBadGateway {
		t.Fatalf("probe-account status = %d, body = %s", probe.StatusCode, probe.Body)
	}

	unauthorized := apps.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodDelete,
		Path:   "/v0/management/plugins/cpa-account-config-manager/opencode/zen/accounts",
	})
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("delete without key status = %d", unauthorized.StatusCode)
	}

	deleted := apps.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method:  http.MethodDelete,
		Path:    "/v0/management/plugins/cpa-account-config-manager/opencode/zen/accounts",
		Query:   map[string][]string{"account_id": {accountID}},
		Headers: http.Header{"X-Management-Key": []string{"management-secret"}},
	})
	if deleted.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", deleted.StatusCode, deleted.Body)
	}
}

func TestOpenCodeZenConfigureRetriesCorruptStoreWithoutDroppingAccounts(t *testing.T) {
	firstDir := t.TempDir()
	service := NewOpenCodeZenService()
	service.Configure(Config{DataDir: firstDir})
	if _, errSave := service.SaveAccount("", "existing", "https://opencode.ai/zen", "sk-existing"); errSave != nil {
		t.Fatalf("SaveAccount() error = %v", errSave)
	}

	secondDir := t.TempDir()
	storePath := openCodeZenStorePath(secondDir)
	if errWrite := os.WriteFile(storePath, []byte(`{"version":`), 0o600); errWrite != nil {
		t.Fatalf("write corrupt store: %v", errWrite)
	}
	service.Configure(Config{DataDir: secondDir})
	if got := service.ListAccounts(); len(got) != 1 || got[0].Name != "existing" {
		t.Fatalf("corrupt store replaced live accounts: %+v", got)
	}
	if got := service.StorageError(); got != "OpenCode Zen state could not be loaded" {
		t.Fatalf("StorageError = %q", got)
	}

	persisted := openCodeZenPersisted{
		Version:  openCodeZenStoreVersion,
		Accounts: []OpenCodeZenAccount{{ID: "restored", Name: "restored", BaseURL: "https://opencode.ai/zen", ZenAPIKey: "sk-restored"}},
	}
	if errSave := savePrivateJSON(storePath, persisted); errSave != nil {
		t.Fatalf("repair store: %v", errSave)
	}
	service.Configure(Config{DataDir: secondDir})
	if got := service.ListAccounts(); len(got) != 1 || got[0].Name != "restored" {
		t.Fatalf("recovered accounts = %+v", got)
	}
	if got := service.StorageError(); got != "" {
		t.Fatalf("StorageError after recovery = %q", got)
	}
}

func TestOpenCodeZenPersistenceFailureIsSanitized(t *testing.T) {
	blockingPath := filepath.Join(t.TempDir(), "not-a-directory")
	if errWrite := os.WriteFile(blockingPath, []byte("block"), 0o600); errWrite != nil {
		t.Fatalf("write blocker: %v", errWrite)
	}
	service := NewOpenCodeZenService()
	service.Configure(Config{DataDir: blockingPath})
	_, errSave := service.SaveAccount("", "secret", "https://opencode.ai/zen", "sk-super-secret")
	if errSave == nil {
		t.Fatal("SaveAccount() error = nil")
	}
	if got := service.StorageError(); got != "OpenCode Zen state could not be persisted" {
		t.Fatalf("StorageError = %q", got)
	}
	if strings.Contains(service.StorageError(), blockingPath) || strings.Contains(service.StorageError(), "sk-super-secret") {
		t.Fatalf("StorageError leaked sensitive details: %q", service.StorageError())
	}
}

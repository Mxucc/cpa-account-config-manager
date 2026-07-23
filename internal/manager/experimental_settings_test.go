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

func TestExperimentalSettingsDefaultDisabledAndPersistAcrossRestart(t *testing.T) {
	dataDir := t.TempDir()
	first := NewExperimentalSettingsService()
	first.Configure(Config{DataDir: dataDir})
	if snapshot := first.Snapshot(); snapshot.Settings.WeeklyOverdraftEnabled || snapshot.Settings.AgentIdentityEnabled || snapshot.StorageError != "" {
		t.Fatalf("default snapshot = %#v", snapshot)
	}
	if _, errSet := first.Set(ExperimentalSettings{WeeklyOverdraftEnabled: true, AgentIdentityEnabled: true}); errSet != nil {
		t.Fatalf("Set() error = %v", errSet)
	}

	restarted := NewExperimentalSettingsService()
	restarted.Configure(Config{DataDir: dataDir})
	if snapshot := restarted.Snapshot(); !snapshot.Settings.WeeklyOverdraftEnabled || !snapshot.Settings.AgentIdentityEnabled || snapshot.StorageError != "" {
		t.Fatalf("restarted snapshot = %#v", snapshot)
	}
}

func TestExperimentalSettingsConfigOverridePersists(t *testing.T) {
	dataDir := t.TempDir()
	settings := ExperimentalSettings{WeeklyOverdraftEnabled: true, AgentIdentityEnabled: true}
	service := NewExperimentalSettingsService()
	service.Configure(Config{DataDir: dataDir, ExperimentalSettings: &settings})
	if !service.WeeklyOverdraftEnabled() {
		t.Fatal("config override did not enable weekly overdraft")
	}
	if !service.AgentIdentityEnabled() {
		t.Fatal("config override did not enable Agent Identity")
	}

	reloaded, errLoad := loadExperimentalSettings(experimentalSettingsStorePath(dataDir))
	if errLoad != nil {
		t.Fatalf("loadExperimentalSettings() error = %v", errLoad)
	}
	if !reloaded.WeeklyOverdraftEnabled || !reloaded.AgentIdentityEnabled {
		t.Fatalf("persisted settings = %#v", reloaded)
	}
}

func TestExperimentalSettingsCorruptStateFailsClosed(t *testing.T) {
	dataDir := t.TempDir()
	if errMkdir := os.MkdirAll(dataDir, 0o700); errMkdir != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdir)
	}
	if errWrite := os.WriteFile(experimentalSettingsStorePath(dataDir), []byte(`{"version":1,"settings":`), 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}

	service := NewExperimentalSettingsService()
	service.Configure(Config{DataDir: dataDir})
	snapshot := service.Snapshot()
	if snapshot.Settings.WeeklyOverdraftEnabled || snapshot.Settings.AgentIdentityEnabled {
		t.Fatal("corrupt state enabled the experiment")
	}
	if snapshot.StorageError != "experimental settings could not be loaded" {
		t.Fatalf("storage_error = %q", snapshot.StorageError)
	}
}

func TestExperimentalSettingsManagementRoutesPersistAndValidate(t *testing.T) {
	dataDir := t.TempDir()
	app := NewApp(&fakeAuthHost{}, []byte("index"))
	defer app.Close()
	app.Configure([]byte("data_dir: " + dataDir + "\n"))
	path := "/v0/management/plugins/cpa-account-config-manager/experiments"

	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{Method: http.MethodGet, Path: path})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", response.StatusCode, response.Body)
	}
	var initial ExperimentalSettingsSnapshot
	if errDecode := json.Unmarshal(response.Body, &initial); errDecode != nil {
		t.Fatalf("decode GET response: %v", errDecode)
	}
	if initial.Settings.WeeklyOverdraftEnabled || initial.Settings.AgentIdentityEnabled {
		t.Fatal("GET returned an enabled experiment by default")
	}

	response = app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut,
		Path:   path,
		Body:   []byte(`{"weekly_overdraft_enabled":true,"agent_identity_enabled":true}`),
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", response.StatusCode, response.Body)
	}
	if !app.experiments.WeeklyOverdraftEnabled() {
		t.Fatal("PUT did not enable the experiment")
	}
	if !app.experiments.AgentIdentityEnabled() {
		t.Fatal("PUT did not enable Agent Identity")
	}

	for name, body := range map[string][]byte{
		"malformed": []byte(`{"weekly_overdraft_enabled":`),
		"unknown":   []byte(`{"weekly_overdraft_enabled":true,"unexpected":true}`),
	} {
		t.Run(name, func(t *testing.T) {
			invalid := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{Method: http.MethodPut, Path: path, Body: body})
			if invalid.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s", invalid.StatusCode, invalid.Body)
			}
		})
	}
}

func TestExperimentalSettingsEnableRequestHookAndSurviveAppRestart(t *testing.T) {
	dataDir := t.TempDir()
	config := []byte("data_dir: " + dataDir + "\n")
	path := "/v0/management/plugins/cpa-account-config-manager/experiments"
	request := cpaapi.RequestInterceptRequest{
		ToFormat: "codex",
		Body:     []byte(`{"input":[{"type":"message","role":"user","content":"continue"}]}`),
	}

	first := NewApp(&fakeAuthHost{}, []byte("index"))
	first.Configure(config)
	if response := first.HandleRequestAfter(request); len(response.Body) != 0 {
		first.Close()
		t.Fatal("disabled experiment unexpectedly transformed the request")
	}
	response := first.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut,
		Path:   path,
		Body:   []byte(`{"weekly_overdraft_enabled":true}`),
	})
	if response.StatusCode != http.StatusOK {
		first.Close()
		t.Fatalf("PUT status = %d body=%s", response.StatusCode, response.Body)
	}
	if transformed := first.HandleRequestAfter(request); !containsExperimentalToolPair(transformed.Body) {
		first.Close()
		t.Fatalf("enabled Hook body = %s", transformed.Body)
	}
	first.Close()

	restarted := NewApp(&fakeAuthHost{}, []byte("index"))
	defer restarted.Close()
	restarted.Configure(config)
	if transformed := restarted.HandleRequestAfter(request); !containsExperimentalToolPair(transformed.Body) {
		t.Fatalf("restarted Hook body = %s", transformed.Body)
	}
}

func TestExperimentalSettingsManagementStorageFailureIsSafe(t *testing.T) {
	dataDir := t.TempDir()
	blockingPath := filepath.Join(dataDir, "not-a-directory")
	if errWrite := os.WriteFile(blockingPath, []byte("block"), 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	app := NewApp(&fakeAuthHost{}, []byte("index"))
	defer app.Close()
	app.Configure([]byte("data_dir: " + blockingPath + "\n"))
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut,
		Path:   "/v0/management/plugins/cpa-account-config-manager/experiments",
		Body:   []byte(`{"weekly_overdraft_enabled":true}`),
	})
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	if string(response.Body) != `{"error":"experimental settings could not be persisted"}` {
		t.Fatalf("unsafe error body = %s", response.Body)
	}
}

func containsExperimentalToolPair(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var document struct {
		Input []struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
		} `json:"input"`
	}
	if errDecode := json.Unmarshal(body, &document); errDecode != nil || len(document.Input) < 2 {
		return false
	}
	call := document.Input[len(document.Input)-2]
	output := document.Input[len(document.Input)-1]
	return call.Type == "custom_tool_call" && output.Type == "custom_tool_call_output" &&
		strings.HasPrefix(call.CallID, "call_cpa_overdraft_") && call.CallID == output.CallID
}

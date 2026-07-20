package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestInspectionPolicySeparatesNativeAndActiveSchedules(t *testing.T) {
	policy, errValidate := validateInspectionPolicy(InspectionPolicy{
		Enabled: false, ScanIntervalMinutes: 30,
		ModelProbeEnabled: true, ModelProbeIntervalMinutes: 15, ModelProbeBatchSize: 7,
		ModelProbeModels: ModelProbeModels{Codex: "codex-test", OpenAI: "openai-test", Claude: "claude-test", Gemini: "gemini-test", XAI: "grok-test"},
		FailureThreshold: 3, RecoveryThreshold: 2, DeleteGraceHours: 168, DeleteBatchSize: 10,
	})
	if errValidate != nil {
		t.Fatalf("validate policy: %v", errValidate)
	}
	if policy.Enabled || !policy.ModelProbeEnabled || policy.ModelProbeIntervalMinutes != 15 || policy.ModelProbeBatchSize != 7 {
		t.Fatalf("independent schedule policy = %#v", policy)
	}
	if _, errInvalid := validateInspectionPolicy(InspectionPolicy{ModelProbeModels: ModelProbeModels{Codex: "https://invalid.example"}}); errInvalid == nil {
		t.Fatal("URL-shaped model identifier was accepted")
	}
}

func TestInspectionProbeEligibilityRespectsManualDisablePolicyAndOwnership(t *testing.T) {
	accounts := []Account{
		{ID: "active"},
		{ID: "manual-disabled", Disabled: true},
		{ID: "inspection-disabled", Disabled: true},
	}
	records := map[string]inspectionRecord{
		"inspection-disabled": {Result: InspectionResult{OwnedDisable: true}},
	}
	withoutManual := inspectionProbeEligibleAccounts(accounts, records, false)
	if len(withoutManual) != 2 || withoutManual[0].ID != "active" || withoutManual[1].ID != "inspection-disabled" {
		t.Fatalf("default eligibility = %#v", withoutManual)
	}
	withManual := inspectionProbeEligibleAccounts(accounts, records, true)
	if len(withManual) != 3 {
		t.Fatalf("opt-in eligibility = %#v", withManual)
	}

	now := time.Date(2026, time.July, 21, 8, 0, 0, 0, time.UTC)
	decision := decideInspection(accounts[1], inspectionRecord{Probe: inspectionProbeSignal{ReasonCode: "model_response_ok", TestedAt: now}}, now)
	if decision.Health != InspectionHealthDisabled || decision.ReasonCode != "manual_disabled" {
		t.Fatalf("manual-disabled decision was overwritten by probe evidence: %#v", decision)
	}
}

func TestInspectionProbeDecisionIsConservative(t *testing.T) {
	now := time.Date(2026, time.July, 21, 3, 0, 0, 0, time.UTC)
	tests := []struct {
		reason      string
		health      string
		autoDisable bool
	}{
		{reason: "model_response_ok", health: InspectionHealthHealthy},
		{reason: "authentication_failed", health: InspectionHealthInvalidCredentials, autoDisable: true},
		{reason: "quota_limited", health: InspectionHealthReview},
		{reason: "model_not_found", health: InspectionHealthReview},
		{reason: "upstream_unavailable", health: InspectionHealthReview},
	}
	for _, test := range tests {
		decision, ok := decisionFromModelProbe(inspectionProbeSignal{ReasonCode: test.reason, TestedAt: now}, now)
		if !ok || decision.Health != test.health || decision.AutoDisableEligible != test.autoDisable {
			t.Errorf("decision for %s = %#v, ok=%v", test.reason, decision, ok)
		}
	}
}

func TestInspectionModelProbeBatchUsesProviderModelsAndRotates(t *testing.T) {
	entries := make([]cpaapi.HostAuthFileEntry, 0, 3)
	details := make(map[string]cpaapi.HostAuthGetResponse, 3)
	for _, id := range []string{"account-a", "account-b", "account-c"} {
		entries = append(entries, cpaapi.HostAuthFileEntry{AuthIndex: id, Name: id + ".json", Provider: "codex", Type: "codex", Source: "file", Path: "/auths/" + id + ".json"})
		details[id] = cpaapi.HostAuthGetResponse{AuthIndex: id, Name: id + ".json", Path: "/auths/" + id + ".json", JSON: json.RawMessage(`{"type":"codex","access_token":"upstream-secret"}`)}
	}
	host := &fakeAuthHost{entries: entries, details: details}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		var call managementAPICallRequest
		_ = json.NewDecoder(request.Body).Decode(&call)
		if !bytes.Contains([]byte(call.Data), []byte(`"model":"codex-inspection-model"`)) {
			t.Errorf("probe payload = %s", call.Data)
		}
		_ = json.NewEncoder(writer).Encode(managementAPICallResponse{StatusCode: http.StatusOK, Body: "data: {\"type\":\"response.completed\"}\n\n"})
	}))
	defer server.Close()
	service := NewModelTestService(NewAccountService(host))
	service.doer = server.Client()
	accounts, errAccounts := service.accounts.baseAccounts(context.Background())
	if errAccounts != nil {
		t.Fatalf("list accounts: %v", errAccounts)
	}
	policy := defaultInspectionPolicy()
	policy.ModelProbeBatchSize = 2
	policy.ModelProbeModels.Codex = "codex-inspection-model"
	results, cursor := runInspectionModelProbes(context.Background(), service, accounts, nil, policy, 0, server.URL, "management-secret")
	if len(results) != 2 || calls.Load() != 2 || cursor != 2 {
		t.Fatalf("first batch results=%d calls=%d cursor=%d", len(results), calls.Load(), cursor)
	}
	second, nextCursor := runInspectionModelProbes(context.Background(), service, accounts, nil, policy, cursor, server.URL, "management-secret")
	if len(second) != 2 || calls.Load() != 4 || nextCursor != 1 {
		t.Fatalf("second batch results=%d calls=%d cursor=%d", len(second), calls.Load(), nextCursor)
	}
}

func TestInspectionProbeAuthorizationIsNeverPersistedAndMustBeRearmed(t *testing.T) {
	dataDir := t.TempDir()
	engine := NewInspectionEngine(NewAccountService(inspectionEditableHost(false)), inspectionEditableHost(false), NewMutationCoordinator())
	engine.SetModelTestService(NewModelTestService(engine.accounts))
	engine.Configure(Config{DataDir: dataDir})
	policy := defaultInspectionPolicy()
	policy.ModelProbeEnabled = true
	if _, errPolicy := engine.SetPolicy(policy); errPolicy != nil {
		t.Fatalf("set policy: %v", errPolicy)
	}
	engine.ArmModelProbes("management-secret")
	if !engine.Snapshot().ActiveProbeArmed {
		t.Fatal("active probes were not armed")
	}
	engine.Shutdown()
	raw, errRead := os.ReadFile(filepath.Join(dataDir, "inspection-state.json"))
	if errRead != nil {
		t.Fatalf("read inspection state: %v", errRead)
	}
	if bytes.Contains(raw, []byte("management-secret")) {
		t.Fatalf("inspection state leaked Management Key: %s", raw)
	}

	host := inspectionEditableHost(false)
	reloaded := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	reloaded.SetModelTestService(NewModelTestService(reloaded.accounts))
	reloaded.Configure(Config{DataDir: filepath.Clean(dataDir)})
	defer reloaded.Shutdown()
	snapshot := reloaded.Snapshot()
	if !snapshot.Policy.ModelProbeEnabled || snapshot.ActiveProbeArmed {
		t.Fatalf("reloaded active-probe state = %#v", snapshot)
	}
}

func TestManualInspectionRunsActiveModelProbeWithCurrentManagementCredential(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{AuthIndex: "manual-account", Name: "manual.json", Provider: "codex", Type: "codex", Source: "file", Path: "/auths/manual.json"}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"manual-account": {AuthIndex: "manual-account", Name: "manual.json", Path: "/auths/manual.json", JSON: json.RawMessage(`{"type":"codex","access_token":"upstream-secret"}`)},
		},
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Header.Get("Authorization") != "Bearer current-management-secret" {
			t.Errorf("Management authorization was not forwarded")
		}
		_ = json.NewEncoder(writer).Encode(managementAPICallResponse{StatusCode: http.StatusOK, Body: "data: {\"type\":\"response.completed\"}\n\n"})
	}))
	defer server.Close()
	app := NewApp(host, []byte("index"))
	app.modelTests.doer = server.Client()
	app.Configure([]byte("data_dir: " + t.TempDir() + "\nmanagement_base_url: " + server.URL + "\n"))
	defer app.Close()
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method:  http.MethodPost,
		Path:    "/v0/management/plugins/cpa-account-config-manager/inspection/scan",
		Headers: http.Header{"Authorization": []string{"Bearer current-management-secret"}},
	})
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("manual scan response = %d %s", response.StatusCode, response.Body)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := app.inspection.Snapshot()
		if !snapshot.Pending && !snapshot.Running && !snapshot.LastRun.FinishedAt.IsZero() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	results := app.inspection.ListResults(InspectionResultQuery{Page: 1, PageSize: 50})
	if calls.Load() != 1 || len(results.Results) != 1 || results.Results[0].ProbeReasonCode != "model_response_ok" {
		t.Fatalf("manual probe calls=%d results=%#v", calls.Load(), results)
	}
}

func TestInspectionFullProbeSweepUsesExactFinalBatch(t *testing.T) {
	entries := make([]cpaapi.HostAuthFileEntry, 0, 5)
	details := make(map[string]cpaapi.HostAuthGetResponse, 5)
	for index := 0; index < 5; index++ {
		id := fmt.Sprintf("sweep-%d", index)
		entries = append(entries, cpaapi.HostAuthFileEntry{AuthIndex: id, Name: id + ".json", Provider: "codex", Type: "codex", Source: "file", Path: "/auths/" + id + ".json"})
		details[id] = cpaapi.HostAuthGetResponse{AuthIndex: id, Name: id + ".json", Path: "/auths/" + id + ".json", JSON: json.RawMessage(`{"type":"codex","access_token":"upstream-secret"}`)}
	}
	host := &fakeAuthHost{entries: entries, details: details}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(writer).Encode(managementAPICallResponse{StatusCode: http.StatusOK, Body: "data: {\"type\":\"response.completed\"}\n\n"})
	}))
	defer server.Close()
	accounts := NewAccountService(host)
	service := NewModelTestService(accounts)
	service.doer = server.Client()
	engine := NewInspectionEngine(accounts, host, NewMutationCoordinator())
	engine.SetModelTestService(service)
	engine.store = ""
	engine.config = normalizeConfig(Config{ManagementBaseURL: server.URL})
	engine.policy = defaultInspectionPolicy()
	engine.policy.ModelProbeEnabled = true
	engine.policy.ModelProbeFullSweep = true
	engine.policy.ModelProbeBatchSize = 2
	engine.managementKey = "management-secret"

	engine.scanWithMode(context.Background(), true, false, false)
	if engine.Snapshot().ProbeSweepRemaining != 3 || calls.Load() != 2 {
		t.Fatalf("first sweep batch remaining=%d calls=%d", engine.Snapshot().ProbeSweepRemaining, calls.Load())
	}
	engine.scanWithMode(context.Background(), false, true, true)
	if engine.Snapshot().ProbeSweepRemaining != 1 || calls.Load() != 4 {
		t.Fatalf("second sweep batch remaining=%d calls=%d", engine.Snapshot().ProbeSweepRemaining, calls.Load())
	}
	engine.scanWithMode(context.Background(), false, true, true)
	if engine.Snapshot().ProbeSweepRemaining != 0 || calls.Load() != 5 {
		t.Fatalf("final sweep batch remaining=%d calls=%d", engine.Snapshot().ProbeSweepRemaining, calls.Load())
	}
}

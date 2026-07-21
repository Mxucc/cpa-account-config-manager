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
	"reflect"
	"sync"
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
	if decision.Health != InspectionHealthHealthy || decision.ReasonCode != "model_response_ok" {
		t.Fatalf("manual-disabled account ignored fresh successful probe evidence: %#v", decision)
	}
	record := inspectionRecord{}
	updateInspectionRecord(&record, accounts[1], decision, now)
	if record.Result.Recommendation != InspectionRecommendationEnable || record.Result.OwnedDisable {
		t.Fatalf("healthy manually disabled account did not become an explicit enable suggestion: %#v", record.Result)
	}
}

func TestInspectionRunTargetModesAndInvalidHealthBoundaries(t *testing.T) {
	accounts := []Account{{ID: "healthy"}, {ID: "review"}, {ID: "new"}, {ID: "manual", Disabled: true}}
	records := map[string]inspectionRecord{
		"healthy": {Result: InspectionResult{ID: "healthy", Health: InspectionHealthHealthy, LastCheckedAt: time.Now()}},
		"review":  {Result: InspectionResult{ID: "review", Health: InspectionHealthReview, LastCheckedAt: time.Now()}},
	}
	if targets := inspectionRunTargetIDs(InspectionRunModeFull, accounts, records, false); len(targets) != 3 {
		t.Fatalf("full targets = %#v", targets)
	}
	if targets := inspectionRunTargetIDs(InspectionRunModeIncremental, accounts, records, false); len(targets) != 1 || targets[0] != "new" {
		t.Fatalf("incremental targets = %#v", targets)
	}
	if health := normalizeInspectionRunHealth([]string{" review ", "not-a-health", "unknown", "review"}); len(health) != 2 || health[0] != "review" || health[1] != "unknown" {
		t.Fatalf("normalized health = %#v", health)
	}
}

func TestStoppedInspectionSweepDoesNotResumeAfterRestart(t *testing.T) {
	dataDir := t.TempDir()
	startedAt := time.Date(2026, time.July, 21, 10, 0, 0, 0, time.UTC)
	state := persistedInspectionState{
		Version: inspectionStoreVersion, Policy: defaultInspectionPolicy(), Records: map[string]inspectionRecord{},
		ProbeSweepTotal: 4, ProbeSweepCompleted: 1, ProbeSweepRemaining: 3,
		ProbeSweepSource: InspectionSweepSourceManual, ProbeSweepStatus: InspectionSweepStatusStopped,
		ProbeSweepStartedAt: startedAt, ProbeSweepTargets: []string{"a", "b", "c", "d"},
		RunMode: InspectionRunModeFull, ProbePhase: InspectionProbePhaseStopped, StopRequested: true,
	}
	if errSave := saveInspectionState(inspectionStorePath(dataDir), state); errSave != nil {
		t.Fatalf("save stopped inspection: %v", errSave)
	}
	engine := NewInspectionEngine(NewAccountService(&fakeAuthHost{}), &fakeAuthHost{}, NewMutationCoordinator())
	engine.SetModelTestService(NewModelTestService(engine.accounts))
	engine.Configure(Config{DataDir: dataDir})
	defer engine.Shutdown()
	snapshot := engine.Snapshot()
	if snapshot.ProbeSweepStatus != InspectionSweepStatusStopped || snapshot.ProbePhase != InspectionProbePhaseStopped ||
		!snapshot.StopRequested || snapshot.Pending || snapshot.Running || snapshot.ProbeSweepRemaining != 3 {
		t.Fatalf("reloaded stopped sweep = %#v", snapshot)
	}
	engine.ArmModelProbes("management-secret")
	afterArm := engine.Snapshot()
	if afterArm.Pending || afterArm.Running || afterArm.ProbeSweepStatus != InspectionSweepStatusStopped {
		t.Fatalf("arming resumed stopped sweep = %#v", afterArm)
	}
}

func TestInspectionProbeDecisionDisablesEveryCompletedAbnormalModelTest(t *testing.T) {
	now := time.Date(2026, time.July, 21, 3, 0, 0, 0, time.UTC)
	tests := []struct {
		reason      string
		health      string
		autoDisable bool
		recommend   string
	}{
		{reason: "model_response_ok", health: InspectionHealthHealthy, recommend: InspectionRecommendationKeep},
		{reason: "authentication_failed", health: InspectionHealthUnavailable, autoDisable: true, recommend: InspectionRecommendationDisable},
		{reason: "quota_limited", health: InspectionHealthQuotaLimited, autoDisable: true, recommend: InspectionRecommendationDisable},
		{reason: "model_not_found", health: InspectionHealthUnavailable, autoDisable: true, recommend: InspectionRecommendationDisable},
		{reason: "request_timeout", health: InspectionHealthUnavailable, autoDisable: true, recommend: InspectionRecommendationDisable},
		{reason: "upstream_unavailable", health: InspectionHealthUnavailable, autoDisable: true, recommend: InspectionRecommendationDisable},
		{reason: "invalid_response", health: InspectionHealthUnavailable, autoDisable: true, recommend: InspectionRecommendationDisable},
	}
	for _, test := range tests {
		decision, ok := decisionFromModelProbe(inspectionProbeSignal{Kind: InspectionProbeKindModel, ReasonCode: test.reason, TestedAt: now}, now)
		if !ok || decision.Health != test.health || decision.AutoDisableEligible != test.autoDisable || decision.Recommendation != test.recommend {
			t.Errorf("decision for %s = %#v, ok=%v", test.reason, decision, ok)
		}
	}
	if _, ok := decisionFromModelProbe(inspectionProbeSignal{Status: "unsupported", ReasonCode: "unsupported_provider", TestedAt: now}, now); ok {
		t.Fatal("unsupported provider was treated as a completed abnormal model test")
	}
}

func TestInspectionProbeAuthenticationFailureUsesProbeKindForActionability(t *testing.T) {
	now := time.Date(2026, time.July, 21, 3, 30, 0, 0, time.UTC)
	credential, okCredential := decisionFromModelProbe(inspectionProbeSignal{
		Kind: InspectionProbeKindCredential, ReasonCode: "authentication_failed", StatusCode: http.StatusUnauthorized, TestedAt: now,
	}, now)
	if !okCredential || credential.Health != InspectionHealthInvalidCredentials ||
		credential.Recommendation != InspectionRecommendationReauth || !credential.AutoDisableEligible {
		t.Fatalf("credential authentication decision = %#v, ok=%v", credential, okCredential)
	}

	model, okModel := decisionFromModelProbe(inspectionProbeSignal{
		Kind: InspectionProbeKindModel, ReasonCode: "authentication_failed", StatusCode: http.StatusUnauthorized, TestedAt: now,
	}, now)
	if !okModel || model.Health != InspectionHealthUnavailable ||
		model.Recommendation != InspectionRecommendationDisable || !model.AutoDisableEligible {
		t.Fatalf("model authentication decision = %#v, ok=%v", model, okModel)
	}
}

func TestCompletedAbnormalModelProbeBypassesOrdinaryFailureThreshold(t *testing.T) {
	policy := defaultInspectionPolicy()
	policy.AutoDisable = true
	policy.FailureThreshold = 3
	record := inspectionRecord{
		Result: InspectionResult{
			Health: InspectionHealthUnavailable, ReasonCode: "upstream_unavailable", Recommendation: InspectionRecommendationDisable,
			Editable: true, AutoDisableEligible: true, FailureStreak: 1, SignalSource: InspectionSignalActiveProbe,
		},
		Probe: inspectionProbeSignal{Status: "unavailable", ReasonCode: "upstream_unavailable", ConsecutiveFailures: 1},
	}
	if !shouldAutoDisableInspection(policy, Account{ID: "active-probe", Editable: true}, record) {
		t.Fatal("completed abnormal model probe did not request immediate disable")
	}
	record.Probe.Status = "unsupported"
	if shouldAutoDisableInspection(policy, Account{ID: "unsupported", Editable: true}, record) {
		t.Fatal("unsupported provider requested an automatic disable")
	}
}

func TestInspectionProbeOrderingPrioritizesUnavailableAndOwnedRecoveryAccounts(t *testing.T) {
	accounts := []Account{
		{ID: "healthy", Editable: true},
		{ID: "owned", Disabled: true, Editable: true},
		{ID: "unavailable", Unavailable: true, Editable: true},
	}
	records := map[string]inspectionRecord{
		"owned": {Result: InspectionResult{ID: "owned", OwnedDisable: true}},
	}
	eligible := inspectionProbeEligibleAccounts(accounts, records, false)
	sortInspectionProbeAccounts(eligible, records)
	got := []string{eligible[0].ID, eligible[1].ID, eligible[2].ID}
	want := []string{"unavailable", "owned", "healthy"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("probe priority = %v, want %v", got, want)
	}
}

func TestExplicitNativeQuotaDisablesWithoutOrdinaryFailureThreshold(t *testing.T) {
	policy := normalizeInspectionPolicy(InspectionPolicy{AutoDisable: true})
	record := inspectionRecord{Result: InspectionResult{
		Health: InspectionHealthQuotaLimited, ReasonCode: "quota_exhausted", Confidence: InspectionConfidenceHigh,
		Recommendation: InspectionRecommendationDisable, AutoDisableEligible: true, SignalSource: InspectionSignalNative, FailureStreak: 1,
	}}
	if !shouldAutoDisableInspection(policy, Account{ID: "quota", Editable: true}, record) {
		t.Fatal("explicit native quota exhaustion did not request immediate disable")
	}
}

func TestCompletedAbnormalModelProbeActuallyDisablesEditableAccount(t *testing.T) {
	now := time.Date(2026, time.July, 21, 13, 0, 0, 0, time.UTC)
	host := inspectionEditableHost(false)
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	engine.now = func() time.Time { return now }
	engine.Configure(Config{DataDir: t.TempDir()})
	defer engine.Shutdown()
	engine.mu.Lock()
	engine.policy = InspectionPolicy{
		ScanIntervalMinutes: 30, FailureThreshold: 3, RecoveryThreshold: 2,
		AutoDisable: true, DeleteGraceHours: 168, DeleteBatchSize: 10,
	}
	engine.records["inspection-account"] = inspectionRecord{Probe: inspectionProbeSignal{
		Status: "unavailable", ReasonCode: "upstream_unavailable", Model: "gpt-test",
		TestedAt: now, ConsecutiveFailures: 1,
	}}
	engine.mu.Unlock()

	engine.scan(context.Background())
	result := engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 20}).Results[0]
	if !result.Disabled || !result.OwnedDisable || result.AutoAction != InspectionActionDisable ||
		result.AutoActionStatus != InspectionActionSucceeded || result.ReasonCode != "upstream_unavailable" || result.FailureStreak != 1 {
		t.Fatalf("active-probe disable result = %#v", result)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.saves) != 1 || !bytes.Contains(host.saves[0].JSON, []byte(`"disabled":true`)) {
		t.Fatalf("active-probe disable writes = %#v", host.saves)
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
		var call managementAPICallRequest
		_ = json.NewDecoder(request.Body).Decode(&call)
		if call.Method == http.MethodGet {
			_ = json.NewEncoder(writer).Encode(managementAPICallResponse{StatusCode: http.StatusOK, Body: `{}`})
			return
		}
		calls.Add(1)
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

func TestInspectionSweepProgressPersistsWithoutManagementCredential(t *testing.T) {
	dataDir := t.TempDir()
	startedAt := time.Date(2026, time.July, 21, 13, 0, 0, 0, time.UTC)
	state := persistedInspectionState{
		Version: inspectionStoreVersion, Policy: defaultInspectionPolicy(), Records: map[string]inspectionRecord{},
		ProbeSweepTotal: 100, ProbeSweepCompleted: 40, ProbeSweepRemaining: 60,
		ProbeSweepSource: InspectionSweepSourceManual, ProbeSweepStatus: InspectionSweepStatusRunning,
		ProbeSweepStartedAt: startedAt,
	}
	if errSave := saveInspectionState(inspectionStorePath(dataDir), state); errSave != nil {
		t.Fatalf("save inspection state: %v", errSave)
	}
	host := inspectionEditableHost(false)
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	engine.SetModelTestService(NewModelTestService(engine.accounts))
	engine.Configure(Config{DataDir: dataDir})
	defer engine.Shutdown()

	snapshot := engine.Snapshot()
	if snapshot.ProbeSweepTotal != 100 || snapshot.ProbeSweepCompleted != 40 || snapshot.ProbeSweepRemaining != 60 ||
		snapshot.ProbeSweepSource != InspectionSweepSourceManual || snapshot.ProbeSweepStatus != InspectionSweepStatusWaitingForAuth ||
		!snapshot.ProbeSweepStartedAt.Equal(startedAt) || snapshot.ActiveProbeArmed {
		t.Fatalf("reloaded sweep snapshot = %#v", snapshot)
	}
	engine.scan(context.Background())
	afterNative := engine.Snapshot()
	if afterNative.ProbeSweepStatus != InspectionSweepStatusWaitingForAuth || afterNative.ProbeSweepRemaining != 60 || afterNative.ProbeSweepCompleted != 40 {
		t.Fatalf("native scan changed waiting sweep progress: %#v", afterNative)
	}
	raw, errRead := os.ReadFile(inspectionStorePath(dataDir))
	if errRead != nil {
		t.Fatalf("read inspection state: %v", errRead)
	}
	if bytes.Contains(raw, []byte("management-secret")) {
		t.Fatalf("sweep state leaked Management Key: %s", raw)
	}
}

func TestFailedManualSweepPreservesCheckpointForExplicitResume(t *testing.T) {
	engine := NewInspectionEngine(nil, nil, nil)
	engine.started = true
	engine.modelTests = &ModelTestService{}
	engine.probeSweepTotal = 5
	engine.probeSweepCompleted = 2
	engine.probeSweepRemaining = 3
	engine.probeSweepSource = InspectionSweepSourceManual
	engine.probeSweepStatus = InspectionSweepStatusRunning
	engine.probeSweepStartedAt = time.Date(2026, time.July, 21, 14, 0, 0, 0, time.UTC)
	engine.probeSweepTargets = []string{"a", "b", "c", "d", "e"}
	engine.updateProbeSweep(inspectionSweepProgress{
		Total: 5, Completed: 2, Remaining: 3, Source: InspectionSweepSourceManual,
		StartedAt: engine.probeSweepStartedAt, Targets: engine.probeSweepTargets,
	}, true)
	failed := engine.Snapshot()
	if failed.ProbeSweepStatus != InspectionSweepStatusFailed || failed.ProbeSweepRemaining != 3 || len(engine.probeSweepTargets) != 5 || engine.pendingProbeSweep {
		t.Fatalf("failed sweep checkpoint = %#v targets=%#v", failed, engine.probeSweepTargets)
	}

	resumed := engine.RequestScanWithModelProbes("current-management-secret")
	if resumed.ProbeSweepStatus != InspectionSweepStatusRunning || resumed.ProbeSweepCompleted != 2 || resumed.ProbeSweepRemaining != 3 ||
		!engine.pendingProbeSweep || engine.managementKey == "" {
		t.Fatalf("resumed sweep = %#v pending=%t", resumed, engine.pendingProbeSweep)
	}
}

func TestEmptyManualFullInspectionCompletesWithoutProbe(t *testing.T) {
	host := &fakeAuthHost{}
	accounts := NewAccountService(host)
	engine := NewInspectionEngine(accounts, host, NewMutationCoordinator())
	engine.SetModelTestService(NewModelTestService(accounts))
	engine.store = ""
	engine.config = normalizeConfig(Config{})
	engine.policy = defaultInspectionPolicy()
	engine.managementKey = "current-management-secret"
	engine.probeSweepSource = InspectionSweepSourceManual
	engine.probeSweepStatus = InspectionSweepStatusRunning
	engine.probeSweepStartedAt = time.Date(2026, time.July, 21, 15, 0, 0, 0, time.UTC)

	engine.scanWithMode(context.Background(), false, true, true)

	snapshot := engine.Snapshot()
	if snapshot.ProbeSweepTotal != 0 || snapshot.ProbeSweepCompleted != 0 || snapshot.ProbeSweepRemaining != 0 ||
		snapshot.ProbeSweepStatus != InspectionSweepStatusCompleted || snapshot.LastRun.Scanned != 0 {
		t.Fatalf("empty manual sweep = %#v", snapshot)
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
		if request.Header.Get("Authorization") != "Bearer current-management-secret" {
			t.Errorf("Management authorization was not forwarded")
		}
		var call managementAPICallRequest
		_ = json.NewDecoder(request.Body).Decode(&call)
		if call.Method == http.MethodGet {
			_ = json.NewEncoder(writer).Encode(managementAPICallResponse{StatusCode: http.StatusOK, Body: `{}`})
			return
		}
		calls.Add(1)
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

func TestManualFullInspectionProbesEveryEligibleAccountAndNativeInspectionDoesNotProbe(t *testing.T) {
	entries := make([]cpaapi.HostAuthFileEntry, 0, 5)
	details := make(map[string]cpaapi.HostAuthGetResponse, 5)
	for index := 0; index < 5; index++ {
		id := fmt.Sprintf("manual-full-%d", index)
		entries = append(entries, cpaapi.HostAuthFileEntry{
			AuthIndex: id, Name: id + ".json", Provider: "codex", Type: "codex", Source: "file", Path: "/auths/" + id + ".json",
		})
		details[id] = cpaapi.HostAuthGetResponse{
			AuthIndex: id, Name: id + ".json", Path: "/auths/" + id + ".json",
			JSON: json.RawMessage(`{"type":"codex","access_token":"upstream-secret"}`),
		}
	}
	host := &fakeAuthHost{entries: entries, details: details}
	var calls atomic.Int32
	var callsMu sync.Mutex
	seen := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var call managementAPICallRequest
		_ = json.NewDecoder(request.Body).Decode(&call)
		if call.Method == http.MethodGet {
			_ = json.NewEncoder(writer).Encode(managementAPICallResponse{StatusCode: http.StatusOK, Body: `{}`})
			return
		}
		calls.Add(1)
		callsMu.Lock()
		seen[call.AuthIndex]++
		callsMu.Unlock()
		_ = json.NewEncoder(writer).Encode(managementAPICallResponse{StatusCode: http.StatusOK, Body: "data: {\"type\":\"response.completed\"}\n\n"})
	}))
	defer server.Close()
	app := NewApp(host, []byte("index"))
	app.modelTests.doer = server.Client()
	app.Configure([]byte("data_dir: " + t.TempDir() + "\nmanagement_base_url: " + server.URL + "\n"))
	defer app.Close()
	app.inspection.mu.Lock()
	app.inspection.policy.ModelProbeBatchSize = 2
	app.inspection.mu.Unlock()

	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management/plugins/cpa-account-config-manager/inspection/scan",
		Headers: http.Header{"Authorization": []string{"Bearer current-management-secret"}},
	})
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("manual full response = %d %s", response.StatusCode, response.Body)
	}
	waitInspectionSweep(t, app.inspection, InspectionSweepStatusCompleted)
	snapshot := app.inspection.Snapshot()
	if calls.Load() != 5 || snapshot.ProbeSweepTotal != 5 || snapshot.ProbeSweepCompleted != 5 || snapshot.ProbeSweepRemaining != 0 ||
		snapshot.ProbeSweepSource != InspectionSweepSourceManual {
		t.Fatalf("manual full snapshot=%#v calls=%d", snapshot, calls.Load())
	}
	callsMu.Lock()
	for _, entry := range entries {
		if seen[entry.AuthIndex] != 1 {
			callsMu.Unlock()
			t.Fatalf("account %q probe count = %d, all=%#v", entry.AuthIndex, seen[entry.AuthIndex], seen)
		}
	}
	callsMu.Unlock()

	beforeNative := calls.Load()
	previousRun := snapshot.LastRun.StartedAt
	response = app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management/plugins/cpa-account-config-manager/inspection/scan/native",
		Headers: http.Header{"Authorization": []string{"Bearer current-management-secret"}},
	})
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("native response = %d %s", response.StatusCode, response.Body)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		next := app.inspection.Snapshot()
		if !next.Pending && !next.Running && next.LastRun.StartedAt.After(previousRun) {
			if next.LastRun.Scanned != 5 || calls.Load() != beforeNative {
				t.Fatalf("native snapshot=%#v calls before=%d after=%d", next, beforeNative, calls.Load())
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("native inspection did not complete")
}

func waitInspectionSweep(t *testing.T, engine *InspectionEngine, wantStatus string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := engine.Snapshot()
		if !snapshot.Pending && !snapshot.Running && snapshot.ProbeSweepStatus == wantStatus {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("inspection sweep did not reach %q: %#v", wantStatus, engine.Snapshot())
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
	var seenMu sync.Mutex
	seen := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var call managementAPICallRequest
		_ = json.NewDecoder(request.Body).Decode(&call)
		if call.Method == http.MethodGet {
			_ = json.NewEncoder(writer).Encode(managementAPICallResponse{StatusCode: http.StatusOK, Body: `{}`})
			return
		}
		calls.Add(1)
		seenMu.Lock()
		seen[call.AuthIndex]++
		seenMu.Unlock()
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
	if engine.Snapshot().ProbeSweepRemaining != 3 || engine.Snapshot().ProbeSweepTotal != 5 || engine.Snapshot().ProbeSweepCompleted != 2 || calls.Load() != 2 {
		t.Fatalf("first sweep batch remaining=%d calls=%d", engine.Snapshot().ProbeSweepRemaining, calls.Load())
	}
	host.mu.Lock()
	host.entries = append(host.entries, cpaapi.HostAuthFileEntry{AuthIndex: "sweep-new", Name: "sweep-new.json", Provider: "codex", Type: "codex", Source: "file", Path: "/auths/sweep-new.json"})
	host.details["sweep-new"] = cpaapi.HostAuthGetResponse{AuthIndex: "sweep-new", Name: "sweep-new.json", Path: "/auths/sweep-new.json", JSON: json.RawMessage(`{"type":"codex","access_token":"upstream-secret"}`)}
	host.mu.Unlock()
	engine.scanWithMode(context.Background(), false, true, true)
	if engine.Snapshot().ProbeSweepRemaining != 1 || engine.Snapshot().ProbeSweepCompleted != 4 || calls.Load() != 4 {
		t.Fatalf("second sweep batch remaining=%d calls=%d", engine.Snapshot().ProbeSweepRemaining, calls.Load())
	}
	engine.scanWithMode(context.Background(), false, true, true)
	if engine.Snapshot().ProbeSweepRemaining != 0 || engine.Snapshot().ProbeSweepCompleted != 5 || engine.Snapshot().ProbeSweepStatus != InspectionSweepStatusCompleted || calls.Load() != 5 {
		t.Fatalf("final sweep batch remaining=%d calls=%d", engine.Snapshot().ProbeSweepRemaining, calls.Load())
	}
	seenMu.Lock()
	for _, entry := range entries {
		if seen[entry.AuthIndex] != 1 {
			seenMu.Unlock()
			t.Fatalf("snapshotted target %q probe count=%d all=%#v", entry.AuthIndex, seen[entry.AuthIndex], seen)
		}
	}
	if seen["sweep-new"] != 0 {
		seenMu.Unlock()
		t.Fatalf("account added mid-sweep was probed: %#v", seen)
	}
	seenMu.Unlock()
	engine.mu.Lock()
	engine.probeSweepSource = InspectionSweepSourceManual
	engine.lastProbeRunAt = time.Time{}
	engine.mu.Unlock()
	engine.scanWithMode(context.Background(), true, false, false)
	nextSweep := engine.Snapshot()
	if nextSweep.ProbeSweepTotal != 6 || nextSweep.ProbeSweepCompleted != 2 || nextSweep.ProbeSweepRemaining != 4 ||
		nextSweep.ProbeSweepSource != InspectionSweepSourceScheduled || calls.Load() != 7 {
		t.Fatalf("next scheduled sweep total=%d completed=%d remaining=%d source=%q status=%q calls=%d",
			nextSweep.ProbeSweepTotal, nextSweep.ProbeSweepCompleted, nextSweep.ProbeSweepRemaining,
			nextSweep.ProbeSweepSource, nextSweep.ProbeSweepStatus, calls.Load())
	}
}

func TestInspectionPublishesEachProbeBeforeTheBatchCompletes(t *testing.T) {
	entries := []cpaapi.HostAuthFileEntry{
		{AuthIndex: "live-fast", Name: "live-fast.json", Provider: "codex", Type: "codex", Source: "file", Path: "/auths/live-fast.json"},
		{AuthIndex: "live-slow", Name: "live-slow.json", Provider: "codex", Type: "codex", Source: "file", Path: "/auths/live-slow.json"},
	}
	host := &fakeAuthHost{entries: entries, details: map[string]cpaapi.HostAuthGetResponse{
		"live-fast": {AuthIndex: "live-fast", Name: "live-fast.json", Path: "/auths/live-fast.json", JSON: json.RawMessage(`{"type":"codex","access_token":"fast-secret"}`)},
		"live-slow": {AuthIndex: "live-slow", Name: "live-slow.json", Path: "/auths/live-slow.json", JSON: json.RawMessage(`{"type":"codex","access_token":"slow-secret"}`)},
	}}
	slowStarted := make(chan struct{})
	releaseSlow := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var call managementAPICallRequest
		_ = json.NewDecoder(request.Body).Decode(&call)
		if call.Method == http.MethodGet {
			_ = json.NewEncoder(writer).Encode(managementAPICallResponse{StatusCode: http.StatusOK, Body: `{}`})
			return
		}
		if call.AuthIndex == "live-slow" {
			close(slowStarted)
			<-releaseSlow
		}
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
	engine.runMode = InspectionRunModeFull
	engine.probeSweepTotal = 2
	engine.probeSweepRemaining = 2
	engine.probeSweepSource = InspectionSweepSourceManual
	engine.probeSweepStatus = InspectionSweepStatusRunning
	engine.probeSweepStartedAt = time.Now().UTC()
	engine.probeSweepTargets = []string{"live-fast", "live-slow"}
	engine.startRunHistoryLocked(InspectionRunModeFull, InspectionSweepSourceManual, engine.probeSweepStartedAt)

	done := make(chan struct{})
	go func() {
		engine.scanWithMode(context.Background(), false, true, true)
		close(done)
	}()
	<-slowStarted
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := engine.Snapshot()
		if snapshot.ProbeSweepCompleted == 1 && snapshot.ActiveRun != nil && len(snapshot.LiveResults) == 1 {
			if snapshot.LiveResults[0].ID != "live-fast" || snapshot.LiveResults[0].RunPhase != InspectionProbePhasePrimary || snapshot.LiveResults[0].RunID != snapshot.ActiveRun.ID {
				t.Fatalf("live result = %#v active=%#v", snapshot.LiveResults, snapshot.ActiveRun)
			}
			select {
			case <-done:
				t.Fatal("inspection completed before the blocked account was released")
			default:
			}
			close(releaseSlow)
			<-done
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(releaseSlow)
	<-done
	t.Fatalf("live result was not published before batch completion: %#v", engine.Snapshot())
}

func TestInspectionReloadRestoresBoundedLiveCheckpointWithoutSecrets(t *testing.T) {
	dataDir := t.TempDir()
	runID := "inspection-persisted-live"
	now := time.Date(2026, time.July, 21, 11, 0, 0, 0, time.UTC)
	resetAt := now.Add(5 * time.Hour)
	state := persistedInspectionState{
		Version: inspectionStoreVersion,
		Policy:  defaultInspectionPolicy(),
		Records: map[string]inspectionRecord{"persisted-live": {Result: InspectionResult{
			ID: "persisted-live", Name: "persisted.json", Provider: "codex", Health: InspectionHealthQuotaLimited,
			ReasonCode: "quota_exhausted", Confidence: InspectionConfidenceHigh, Recommendation: InspectionRecommendationDisable,
			QuotaWindow: InspectionQuotaWindowFiveHour, RunID: runID, RunPhase: InspectionProbePhasePrimary, RunObservedAt: timePointer(now),
			LastCheckedAt: now, RecoverAfter: timePointer(resetAt), UsageTotalTokens: 123,
			CodexUsage: &CodexUsageSnapshot{ObservedAt: now, FiveHour: &UsageWindowSnapshot{UsedPercent: 100, ResetAt: timePointer(resetAt), WindowMinutes: 300}},
		}}},
		ProbeSweepTotal: 2, ProbeSweepCompleted: 1, ProbeSweepRemaining: 1,
		ProbeSweepSource: InspectionSweepSourceManual, ProbeSweepStatus: InspectionSweepStatusRunning, ProbeSweepStartedAt: now,
		ProbeSweepTargets: []string{"persisted-live", "pending-live"}, RunMode: InspectionRunModeFull, ProbePhase: InspectionProbePhasePrimary,
		Runs:        []InspectionRunRecord{{ID: runID, Mode: InspectionRunModeFull, Source: InspectionSweepSourceManual, Status: InspectionSweepStatusRunning, Phase: InspectionProbePhasePrimary, StartedAt: now, PrimaryTotal: 2, PrimaryDone: 1, Summary: InspectionRunSummary{StartedAt: now, Scanned: 1, QuotaLimited: 1}}},
		ActiveRunID: runID,
	}
	if errSave := saveInspectionState(inspectionStorePath(dataDir), state); errSave != nil {
		t.Fatalf("saveInspectionState() error = %v", errSave)
	}
	engine := NewInspectionEngine(NewAccountService(&fakeAuthHost{}), &fakeAuthHost{}, NewMutationCoordinator())
	engine.Configure(Config{DataDir: dataDir})
	defer engine.Shutdown()
	snapshot := engine.Snapshot()
	if snapshot.ActiveRun == nil || snapshot.ActiveRun.ID != runID || len(snapshot.LiveResults) != 1 || snapshot.LiveResults[0].QuotaWindow != InspectionQuotaWindowFiveHour || snapshot.LiveResults[0].UsageTotalTokens != 123 {
		t.Fatalf("reloaded live snapshot = %#v", snapshot)
	}
	raw, errRead := os.ReadFile(inspectionStorePath(dataDir))
	if errRead != nil {
		t.Fatalf("read inspection state: %v", errRead)
	}
	for _, secret := range []string{"management-secret", "access_token", "Authorization", "Set-Cookie", "raw upstream"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("persisted live state leaked %q: %s", secret, raw)
		}
	}
}

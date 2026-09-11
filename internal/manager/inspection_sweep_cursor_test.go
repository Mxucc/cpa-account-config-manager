package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// sweepTestEngine builds an inspection engine whose probe requests are answered
// by a local server, so a sweep pass reaches the batch slicing code without
// touching the network.
func sweepTestEngine(t *testing.T) *InspectionEngine {
	t.Helper()
	entries := []cpaapi.HostAuthFileEntry{
		{AuthIndex: "sweep-0", Name: "sweep-0.json", Provider: "codex", Type: "codex", Source: "file", Path: "/auths/sweep-0.json"},
		{AuthIndex: "sweep-1", Name: "sweep-1.json", Provider: "codex", Type: "codex", Source: "file", Path: "/auths/sweep-1.json"},
	}
	details := map[string]cpaapi.HostAuthGetResponse{}
	for _, entry := range entries {
		details[entry.AuthIndex] = cpaapi.HostAuthGetResponse{
			AuthIndex: entry.AuthIndex, Name: entry.Name, Path: entry.Path,
			JSON: json.RawMessage(`{"type":"codex","access_token":"upstream-secret"}`),
		}
	}
	host := &fakeAuthHost{entries: entries, details: details}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var call managementAPICallRequest
		_ = json.NewDecoder(request.Body).Decode(&call)
		if call.Method == http.MethodGet {
			_ = json.NewEncoder(writer).Encode(managementAPICallResponse{StatusCode: http.StatusOK, Body: `{}`})
			return
		}
		_ = json.NewEncoder(writer).Encode(managementAPICallResponse{StatusCode: http.StatusOK, Body: "data: {\"type\":\"response.completed\"}\n\n"})
	}))
	t.Cleanup(server.Close)

	accounts := NewAccountService(host)
	service := NewModelTestService(accounts)
	service.doer = server.Client()
	engine := NewInspectionEngine(accounts, host, NewMutationCoordinator())
	engine.SetModelTestService(service)
	engine.store = ""
	engine.config = normalizeConfig(Config{ManagementBaseURL: server.URL})
	engine.policy = defaultInspectionPolicy()
	engine.policy.ModelProbeEnabled = true
	engine.policy.ModelProbeBatchSize = 20
	engine.managementKey = "management-secret"
	return engine
}

// A stale completed cursor (target list replaced or trimmed by a newer release,
// or a hand-edited state file) must degrade into a short batch instead of slicing
// past the end of the target list, which would panic the background scan
// goroutine and take CPA down with it.
func TestInspectionSweepCursorBeyondTargetsDoesNotPanic(t *testing.T) {
	engine := sweepTestEngine(t)
	engine.mu.Lock()
	engine.probeSweepTargets = []string{"sweep-0", "sweep-1"}
	engine.probeSweepTotal = 5
	engine.probeSweepCompleted = 4
	engine.probeSweepRemaining = 1
	engine.probeSweepSource = InspectionSweepSourceManual
	engine.probeSweepStatus = InspectionSweepStatusRunning
	engine.mu.Unlock()

	engine.scanWithMode(context.Background(), false, true, true)

	snapshot := engine.Snapshot()
	if snapshot.ProbeSweepCompleted > 2 {
		t.Fatalf("completed cursor = %d, want it clamped into the two-element target list", snapshot.ProbeSweepCompleted)
	}
	if snapshot.ProbeSweepRemaining < 0 {
		t.Fatalf("remaining = %d, want a non-negative count", snapshot.ProbeSweepRemaining)
	}
}

// Loading a state file whose counters disagree with the sanitized target list
// (duplicates, over-long identifiers, or a hand-edited file) must restore the
// counter invariant instead of trusting the stored numbers.
func TestInspectionSweepCountersClampOnLoad(t *testing.T) {
	dataDir := t.TempDir()
	state := map[string]any{
		"version":                inspectionStoreVersion,
		"policy":                 defaultInspectionPolicy(),
		"records":                map[string]any{},
		"probe_sweep_targets":    []string{"sweep-0", "sweep-0", "sweep-1"},
		"probe_sweep_total":      9,
		"probe_sweep_completed":  7,
		"probe_sweep_remaining":  2,
		"probe_sweep_source":     InspectionSweepSourceManual,
		"probe_sweep_status":     InspectionSweepStatusRunning,
		"probe_sweep_started_at": "2026-07-21T13:00:00Z",
	}
	raw, errMarshal := json.Marshal(state)
	if errMarshal != nil {
		t.Fatalf("marshal inspection state: %v", errMarshal)
	}
	if errWrite := os.WriteFile(inspectionStorePath(dataDir), raw, 0o600); errWrite != nil {
		t.Fatalf("write inspection state: %v", errWrite)
	}

	loaded, errLoad := loadInspectionState(inspectionStorePath(dataDir))
	if errLoad != nil {
		t.Fatalf("load inspection state: %v", errLoad)
	}
	// The duplicate target is sanitized away, so the reachable target count is 2.
	if loaded.ProbeSweepTotal != 2 {
		t.Fatalf("total = %d, want the sanitized target count 2", loaded.ProbeSweepTotal)
	}
	if loaded.ProbeSweepCompleted > loaded.ProbeSweepTotal {
		t.Fatalf("completed = %d exceeds total = %d", loaded.ProbeSweepCompleted, loaded.ProbeSweepTotal)
	}
	if loaded.ProbeSweepRemaining != loaded.ProbeSweepTotal-loaded.ProbeSweepCompleted {
		t.Fatalf("remaining = %d is inconsistent with total/completed", loaded.ProbeSweepRemaining)
	}
}

// An interrupted sweep that stored counters without a target list keeps them, so
// the executor can still rebuild the list and resume.
func TestInspectionSweepCountersSurviveLoadWithoutTargets(t *testing.T) {
	dataDir := t.TempDir()
	state := map[string]any{
		"version":               inspectionStoreVersion,
		"policy":                defaultInspectionPolicy(),
		"records":               map[string]any{},
		"probe_sweep_total":     5,
		"probe_sweep_completed": 2,
		"probe_sweep_remaining": 3,
		"probe_sweep_source":    InspectionSweepSourceManual,
		"probe_sweep_status":    InspectionSweepStatusWaitingForAuth,
	}
	raw, errMarshal := json.Marshal(state)
	if errMarshal != nil {
		t.Fatalf("marshal inspection state: %v", errMarshal)
	}
	if errWrite := os.WriteFile(inspectionStorePath(dataDir), raw, 0o600); errWrite != nil {
		t.Fatalf("write inspection state: %v", errWrite)
	}
	loaded, errLoad := loadInspectionState(inspectionStorePath(dataDir))
	if errLoad != nil {
		t.Fatalf("load inspection state: %v", errLoad)
	}
	if loaded.ProbeSweepTotal != 5 || loaded.ProbeSweepCompleted != 2 || loaded.ProbeSweepRemaining != 3 {
		t.Fatalf("target-less sweep state = total %d completed %d remaining %d, want it preserved",
			loaded.ProbeSweepTotal, loaded.ProbeSweepCompleted, loaded.ProbeSweepRemaining)
	}
}

// A saturated local model-test service must not be reported as an upstream
// failure: that would inflate the account's failure streak and circuit state
// even though the probe never reached the provider.
func TestInspectionProbeFailureClassification(t *testing.T) {
	account := Account{ID: "account-a", Provider: "codex", Type: "codex"}
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

	busy := inspectionProbeFailureResult(account, "gpt-5.4", ErrModelTestBusy, now)
	if busy.ReasonCode != inspectionProbeReasonManagementUnavailable {
		t.Fatalf("busy probe reason = %q, want %q", busy.ReasonCode, inspectionProbeReasonManagementUnavailable)
	}
	wrapped := inspectionProbeFailureResult(account, "gpt-5.4", fmt.Errorf("run: %w", ErrModelTestBusy), now)
	if wrapped.ReasonCode != inspectionProbeReasonManagementUnavailable {
		t.Fatalf("wrapped busy probe reason = %q, want %q", wrapped.ReasonCode, inspectionProbeReasonManagementUnavailable)
	}
	upstream := inspectionProbeFailureResult(account, "gpt-5.4", errors.New("upstream exploded"), now)
	if upstream.ReasonCode != "upstream_unavailable" {
		t.Fatalf("upstream failure reason = %q, want upstream_unavailable", upstream.ReasonCode)
	}

	// The signal layer ignores the local-capacity code entirely.
	record := inspectionRecord{Signal: inspectionSignal{ConsecutiveFailures: 2, ReasonCode: "upstream_unavailable"}}
	applyModelProbeToInspection(&record, busy, defaultInspectionPolicy())
	if record.Probe.ConsecutiveFailures != 0 || record.Probe.ReasonCode != "" {
		t.Fatalf("busy probe touched the failure streak: %#v", record.Probe)
	}
	// A real upstream failure still counts.
	applyModelProbeToInspection(&record, upstream, defaultInspectionPolicy())
	if record.Probe.ConsecutiveFailures != 1 || record.Probe.ReasonCode != "upstream_unavailable" {
		t.Fatalf("upstream failure streak = %#v", record.Probe)
	}
}

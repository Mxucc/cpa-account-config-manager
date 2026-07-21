package manager

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestCodex429ClassifiesResetWindowsAndUsesBoundedFallback(t *testing.T) {
	now := time.Date(2026, time.July, 21, 9, 0, 0, 0, time.UTC)
	fiveHourReset := now.Add(2 * time.Hour)
	weeklyReset := now.Add(4 * 24 * time.Hour)
	evidence := classifyUsageFailure(cpaapi.UsageRecord{
		Provider: "codex", Failed: true, Failure: cpaapi.UsageFailure{StatusCode: http.StatusTooManyRequests},
		ResponseHeaders: http.Header{
			"X-Codex-Primary-Used-Percent":     []string{"100"},
			"X-Codex-Primary-Reset-At":         []string{strconv.FormatInt(fiveHourReset.Unix(), 10)},
			"X-Codex-Primary-Window-Minutes":   []string{"300"},
			"X-Codex-Secondary-Used-Percent":   []string{"100"},
			"X-Codex-Secondary-Reset-At":       []string{strconv.FormatInt(weeklyReset.Unix(), 10)},
			"X-Codex-Secondary-Window-Minutes": []string{"10080"},
		},
	}, now)
	if evidence.ReasonCode != "quota_exhausted" || evidence.QuotaWindow != InspectionQuotaWindowMultiple || !evidence.RecoverAfter.Equal(weeklyReset) {
		t.Fatalf("dual-window evidence = %#v", evidence)
	}

	fallback := classifyUsageFailure(cpaapi.UsageRecord{
		Provider: "codex", Failed: true, Failure: cpaapi.UsageFailure{StatusCode: http.StatusTooManyRequests},
	}, now)
	if fallback.QuotaWindow != InspectionQuotaWindowFiveHourFallback || !fallback.RecoverAfter.Equal(now.Add(5*time.Hour)) {
		t.Fatalf("fallback evidence = %#v", fallback)
	}
}

func passiveCircuitPolicy() InspectionPolicy {
	policy := defaultInspectionPolicy()
	policy.AutoDisable = true
	policy.AutoEnable = true
	policy.PassiveCircuitEnabled = true
	policy.PassiveFailureThreshold = 3
	policy.PassiveFailureWindowMinutes = 30
	policy.PassiveCircuitMinutes = 15
	return policy
}

func TestPassiveFailuresOpenOwnedCircuitAtExactThresholdAndRecoverAtBoundary(t *testing.T) {
	now := time.Date(2026, time.July, 21, 9, 0, 0, 0, time.UTC)
	host := inspectionEditableHost(false)
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	engine.now = func() time.Time { return now }
	engine.Configure(Config{DataDir: t.TempDir()})
	defer engine.Shutdown()
	engine.mu.Lock()
	engine.policy = passiveCircuitPolicy()
	engine.started = false
	engine.mu.Unlock()

	failure := cpaapi.UsageRecord{
		Provider: "codex", AuthIndex: "inspection-account", Failed: true,
		Failure: cpaapi.UsageFailure{StatusCode: http.StatusBadGateway, Body: `{"error":"upstream unavailable"}`},
	}
	for attempt := 1; attempt < 3; attempt++ {
		engine.Observe(failure)
		engine.scan(context.Background())
		result := engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 50}).Results[0]
		if result.Disabled || result.OwnedDisable || result.CircuitOpen {
			t.Fatalf("attempt %d opened circuit before threshold: %#v", attempt, result)
		}
	}
	engine.Observe(failure)
	engine.scan(context.Background())

	result := engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 50}).Results[0]
	wantRecovery := now.Add(15 * time.Minute)
	if !result.Disabled || !result.OwnedDisable || !result.CircuitOpen || result.CircuitReasonCode != "transient_failure" ||
		result.RecoverAfter == nil || !result.RecoverAfter.Equal(wantRecovery) || result.FailureStreak != 3 {
		t.Fatalf("threshold result = %#v", result)
	}
	record := engine.records["inspection-account"]
	if record.DisableReason != "passive_circuit_open" || inspectionDeleteReasonAllowed(InspectionPolicy{
		AutoDisable: true, AutoDelete: true, AutoDeleteInvalidCredentials: true, FailureThreshold: 2,
	}, record) {
		t.Fatalf("passive circuit became delete eligible: %#v", record)
	}

	host.mu.Lock()
	host.entries[0].Disabled = true
	host.mu.Unlock()
	now = wantRecovery.Add(-time.Nanosecond)
	engine.scan(context.Background())
	if got := engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 50}).Results[0]; !got.OwnedDisable {
		t.Fatalf("circuit recovered before boundary: %#v", got)
	}
	now = wantRecovery
	engine.scan(context.Background())
	result = engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 50}).Results[0]
	if result.OwnedDisable || result.Disabled || result.CircuitOpen || result.RecoverAfter != nil || result.AutoAction != InspectionActionEnable {
		t.Fatalf("circuit did not recover at boundary: %#v", result)
	}
}

func TestPassiveFailureWindowAndManualDisableAreConservative(t *testing.T) {
	policy := passiveCircuitPolicy()
	now := time.Date(2026, time.July, 21, 10, 0, 0, 0, time.UTC)
	record := inspectionRecord{}
	failure := cpaapi.UsageRecord{Failed: true, Failure: cpaapi.UsageFailure{StatusCode: http.StatusUnauthorized, Body: "request failed"}}
	applyUsageRecordToInspection(&record, failure, policy, now)
	applyUsageRecordToInspection(&record, failure, policy, now.Add(31*time.Minute))
	if record.Signal.ConsecutiveFailures != 1 || record.Signal.ReasonCode != "authentication_review" {
		t.Fatalf("failure window did not reset: %#v", record.Signal)
	}
	applyUsageRecordToInspection(&record, cpaapi.UsageRecord{
		Failed: true, Failure: cpaapi.UsageFailure{StatusCode: http.StatusUnauthorized, Body: `{"error":{"code":"invalid_token"}}`},
	}, policy, now.Add(32*time.Minute))
	if record.Signal.ConsecutiveFailures != 1 || record.Signal.ReasonCode != "invalid_credentials" || !record.Signal.AutoDisableEligible {
		t.Fatalf("strong evidence inherited ambiguous failures: %#v", record.Signal)
	}
	record.Signal.ConsecutiveFailures = policy.PassiveFailureThreshold
	account := Account{Disabled: true, Editable: true}
	if open, _, _ := shouldOpenPassiveCircuit(policy, account, record, now.Add(31*time.Minute)); open {
		t.Fatal("manual disable was claimed by passive circuit")
	}
}

func TestRepeatedProbeFailuresUseCircuitButModelNotFoundDoesNot(t *testing.T) {
	policy := passiveCircuitPolicy()
	now := time.Date(2026, time.July, 21, 11, 0, 0, 0, time.UTC)
	record := inspectionRecord{}
	for attempt := 0; attempt < policy.PassiveFailureThreshold; attempt++ {
		applyModelProbeToInspection(&record, ModelTestResult{
			AccountID: "probe-account", Status: "review", ReasonCode: "invalid_response", Model: "gpt-5.4",
			TestedAt: now.Add(time.Duration(attempt) * time.Minute),
		}, policy)
	}
	if open, reason, count := shouldOpenPassiveCircuit(policy, Account{Editable: true}, record, now.Add(2*time.Minute)); !open || reason != "invalid_response" || count != 3 {
		t.Fatalf("probe circuit decision = open %t reason %q count %d", open, reason, count)
	}
	record.Probe.ReasonCode = "model_not_found"
	if open, _, _ := shouldOpenPassiveCircuit(policy, Account{Editable: true}, record, now.Add(2*time.Minute)); open {
		t.Fatal("model-not-found probe opened an account circuit")
	}
}

func TestPassiveCircuitPolicyBoundsAndDependencies(t *testing.T) {
	for name, mutate := range map[string]func(*InspectionPolicy){
		"threshold low":  func(policy *InspectionPolicy) { policy.PassiveFailureThreshold = 1 },
		"threshold high": func(policy *InspectionPolicy) { policy.PassiveFailureThreshold = 101 },
		"window high":    func(policy *InspectionPolicy) { policy.PassiveFailureWindowMinutes = 1441 },
		"duration high":  func(policy *InspectionPolicy) { policy.PassiveCircuitMinutes = 1441 },
		"auto disable off": func(policy *InspectionPolicy) {
			policy.AutoDisable = false
		},
		"auto enable off": func(policy *InspectionPolicy) {
			policy.AutoEnable = false
		},
	} {
		t.Run(name, func(t *testing.T) {
			policy := passiveCircuitPolicy()
			mutate(&policy)
			if _, errValidate := validateInspectionPolicy(policy); errValidate == nil {
				t.Fatalf("invalid policy accepted: %#v", policy)
			}
		})
	}
}

func TestPassiveThresholdQueuesImmediateScanAndSchedulesRecovery(t *testing.T) {
	engine := NewInspectionEngine(nil, nil, nil)
	engine.started = true
	engine.policy = passiveCircuitPolicy()
	failure := cpaapi.UsageRecord{
		AuthIndex: "passive-account", Failed: true,
		Failure: cpaapi.UsageFailure{StatusCode: http.StatusBadGateway, Body: "gateway failure"},
	}
	for range engine.policy.PassiveFailureThreshold - 1 {
		engine.Observe(failure)
	}
	if engine.pending || len(engine.scanWake) != 0 {
		t.Fatal("passive scan queued before exact threshold")
	}
	engine.Observe(failure)
	if !engine.pending || len(engine.scanWake) != 1 {
		t.Fatalf("exact threshold did not queue one scan: pending=%t wake=%d", engine.pending, len(engine.scanWake))
	}
	if got := engine.scanInterval(); !engine.scheduledEnabled() || got != 15*time.Minute {
		t.Fatalf("passive recovery scheduler = enabled %t interval %s", engine.scheduledEnabled(), got)
	}

	strong := inspectionRecord{Signal: inspectionSignal{ReasonCode: "invalid_credentials", AutoDisableEligible: true, ConsecutiveFailures: engine.policy.PassiveFailureThreshold}}
	if passiveCircuitThresholdReached(engine.policy, strong) {
		t.Fatal("high-confidence credential failure entered the passive path")
	}
}

func TestPassiveCircuitPersistsAcrossRestartAndFreshSuccessRecovers(t *testing.T) {
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	dataDir := t.TempDir()
	host := inspectionEditableHost(false)
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	engine.now = func() time.Time { return now }
	engine.Configure(Config{DataDir: dataDir})
	engine.mu.Lock()
	engine.policy = passiveCircuitPolicy()
	engine.started = false
	engine.mu.Unlock()
	failure := cpaapi.UsageRecord{
		Provider: "codex", AuthIndex: "inspection-account", Failed: true,
		Failure: cpaapi.UsageFailure{StatusCode: http.StatusBadGateway, Body: "gateway failure"},
	}
	for range 3 {
		engine.Observe(failure)
	}
	engine.scan(context.Background())
	engine.Shutdown()

	host.mu.Lock()
	host.entries[0].Disabled = true
	host.mu.Unlock()
	restarted := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	restarted.now = func() time.Time { return now }
	restarted.Configure(Config{DataDir: dataDir})
	defer restarted.Shutdown()
	restarted.mu.Lock()
	restarted.started = false
	restarted.mu.Unlock()
	record := restarted.records["inspection-account"]
	if !record.Result.OwnedDisable || !record.Result.CircuitOpen || record.DisableReason != "passive_circuit_open" ||
		record.DisabledRecoverAfter.IsZero() {
		t.Fatalf("restarted circuit state = %#v", record)
	}

	now = now.Add(time.Minute)
	restarted.Observe(cpaapi.UsageRecord{Provider: "codex", AuthIndex: "inspection-account"})
	restarted.scan(context.Background())
	if got := restarted.ListResults(InspectionResultQuery{Page: 1, PageSize: 50}).Results[0]; !got.OwnedDisable || got.HealthyStreak != 1 {
		t.Fatalf("first success unexpectedly recovered circuit: %#v", got)
	}
	restarted.Observe(cpaapi.UsageRecord{Provider: "codex", AuthIndex: "inspection-account"})
	restarted.scan(context.Background())
	if got := restarted.ListResults(InspectionResultQuery{Page: 1, PageSize: 50}).Results[0]; got.OwnedDisable || got.Disabled || got.CircuitOpen || got.AutoAction != InspectionActionEnable {
		t.Fatalf("fresh success evidence did not recover circuit: %#v", got)
	}
}

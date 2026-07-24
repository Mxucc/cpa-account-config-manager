package manager

import (
	"context"
	"errors"
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

func codexUsageObservationHeaders(now time.Time, fiveHour, sevenDay float64) http.Header {
	return http.Header{
		"X-Codex-Primary-Used-Percent":     []string{strconv.FormatFloat(fiveHour, 'f', -1, 64)},
		"X-Codex-Primary-Window-Minutes":   []string{"300"},
		"X-Codex-Secondary-Used-Percent":   []string{strconv.FormatFloat(sevenDay, 'f', -1, 64)},
		"X-Codex-Secondary-Reset-At":       []string{strconv.FormatInt(now.Add(4*24*time.Hour).Unix(), 10)},
		"X-Codex-Secondary-Window-Minutes": []string{"10080"},
	}
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

func TestUsageObservationQueuesImmediateAutoDisableScan(t *testing.T) {
	now := time.Date(2026, time.July, 22, 6, 0, 0, 0, time.UTC)
	newEngine := func() *InspectionEngine {
		engine := NewInspectionEngine(nil, nil, nil)
		engine.started = true
		engine.now = func() time.Time { return now }
		engine.policy = defaultInspectionPolicy()
		engine.policy.Enabled = true
		engine.policy.AutoDisable = true
		return engine
	}

	t.Run("seven day quota exhaustion", func(t *testing.T) {
		engine := newEngine()
		engine.Observe(cpaapi.UsageRecord{
			Provider: "codex", AuthIndex: "weekly-exhausted", ResponseHeaders: codexUsageObservationHeaders(now, 15, 100),
		})
		if !engine.pending || len(engine.scanWake) != 1 {
			t.Fatalf("weekly exhaustion did not queue scan: pending=%t wake=%d", engine.pending, len(engine.scanWake))
		}
		engine.Observe(cpaapi.UsageRecord{
			Provider: "codex", AuthIndex: "weekly-exhausted", ResponseHeaders: codexUsageObservationHeaders(now, 16, 100),
		})
		if len(engine.scanWake) != 1 {
			t.Fatalf("repeated exhaustion queued unbounded scans: wake=%d", len(engine.scanWake))
		}
	})

	t.Run("five hour quota exhaustion only", func(t *testing.T) {
		engine := newEngine()
		engine.Observe(cpaapi.UsageRecord{
			Provider: "codex", AuthIndex: "short-window", ResponseHeaders: codexUsageObservationHeaders(now, 100, 30),
		})
		if !engine.pending || len(engine.scanWake) != 1 {
			t.Fatalf("short-window exhaustion did not queue disable scan: pending=%t wake=%d", engine.pending, len(engine.scanWake))
		}
	})

	t.Run("high confidence failure at threshold", func(t *testing.T) {
		engine := newEngine()
		failure := cpaapi.UsageRecord{
			Provider: "codex", AuthIndex: "revoked", Failed: true,
			Failure: cpaapi.UsageFailure{StatusCode: http.StatusUnauthorized, Body: `{"error":"token_revoked"}`},
		}
		for range engine.policy.FailureThreshold - 1 {
			engine.Observe(failure)
		}
		if engine.pending || len(engine.scanWake) != 0 {
			t.Fatalf("strong failure queued before threshold: pending=%t wake=%d", engine.pending, len(engine.scanWake))
		}
		engine.Observe(failure)
		if !engine.pending || len(engine.scanWake) != 1 {
			t.Fatalf("strong failure threshold did not queue scan: pending=%t wake=%d", engine.pending, len(engine.scanWake))
		}
	})

	for name, mutate := range map[string]func(*InspectionPolicy){
		"auto disable off": func(policy *InspectionPolicy) { policy.AutoDisable = false },
		"schedule off":     func(policy *InspectionPolicy) { policy.Enabled = false },
	} {
		t.Run(name, func(t *testing.T) {
			engine := newEngine()
			mutate(&engine.policy)
			engine.Observe(cpaapi.UsageRecord{
				Provider: "codex", AuthIndex: "disabled-policy", ResponseHeaders: codexUsageObservationHeaders(now, 10, 100),
			})
			if engine.pending || len(engine.scanWake) != 0 {
				t.Fatalf("disabled remediation queued scan: pending=%t wake=%d", engine.pending, len(engine.scanWake))
			}
		})
	}
}

func TestUsageObservationAutoDisableRetriesFailedMutation(t *testing.T) {
	now := time.Date(2026, time.July, 22, 7, 0, 0, 0, time.UTC)
	host := inspectionEditableHost(false)
	host.saveErrors = map[string]error{"inspection.json": errors.New("injected save failure")}
	dataDir := t.TempDir()
	usage := NewUsageTracker()
	usage.Configure(Config{DataDir: dataDir})
	defer usage.Close()
	engine := NewInspectionEngine(NewAccountService(host, usage), host, NewMutationCoordinator())
	policy := defaultInspectionPolicy()
	policy.Enabled = true
	policy.AutoDisable = true
	engine.now = func() time.Time { return now }
	engine.Configure(Config{DataDir: dataDir, InspectionPolicy: &policy})
	defer engine.Shutdown()

	record := cpaapi.UsageRecord{
		Provider: "codex", AuthIndex: "inspection-account", ResponseHeaders: codexUsageObservationHeaders(now, 100, 20),
	}
	usage.Observe(record)
	engine.Observe(record)
	waitForInspectionResult(t, engine, func(result InspectionResult) bool {
		return result.AutoAction == InspectionActionDisable && result.AutoActionStatus == InspectionActionFailed
	})

	host.mu.Lock()
	delete(host.saveErrors, "inspection.json")
	host.mu.Unlock()
	usage.Observe(record)
	engine.Observe(record)
	result := waitForInspectionResult(t, engine, func(result InspectionResult) bool {
		return result.Disabled && result.OwnedDisable && result.AutoAction == InspectionActionDisable &&
			result.AutoActionStatus == InspectionActionSucceeded
	})
	if result.ReasonCode != "quota_exhausted" || result.QuotaWindow != InspectionQuotaWindowFiveHour {
		t.Fatalf("automatic disable result = %#v", result)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.saves) != 1 || host.saveCalls["inspection.json"] != 2 {
		t.Fatalf("automatic disable save attempts=%d successful saves=%d", host.saveCalls["inspection.json"], len(host.saves))
	}
}

func waitForInspectionResult(t *testing.T, engine *InspectionEngine, condition func(InspectionResult) bool) InspectionResult {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		results := engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 50}).Results
		if len(results) == 1 && condition(results[0]) {
			return results[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("inspection result did not reach expected state: %#v", results)
		}
		time.Sleep(10 * time.Millisecond)
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

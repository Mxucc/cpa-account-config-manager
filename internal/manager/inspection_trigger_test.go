package manager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInspectionAnomalyThresholdBoundaries(t *testing.T) {
	tests := []struct {
		name                         string
		eligible, abnormal, min, pct int
		want                         bool
	}{
		{name: "zero denominator", eligible: 0, abnormal: 0, min: 1, pct: 50},
		{name: "below minimum", eligible: 9, abnormal: 9, min: 10, pct: 50},
		{name: "below threshold", eligible: 10, abnormal: 4, min: 10, pct: 50},
		{name: "exact threshold", eligible: 10, abnormal: 5, min: 10, pct: 50, want: true},
		{name: "one percent", eligible: 100, abnormal: 1, min: 1, pct: 1, want: true},
		{name: "one hundred percent", eligible: 10, abnormal: 10, min: 10, pct: 100, want: true},
		{name: "invalid count", eligible: 2, abnormal: 3, min: 1, pct: 50},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := inspectionAnomalyTriggered(test.eligible, test.abnormal, test.min, test.pct); got != test.want {
				t.Fatalf("triggered = %v, want %v", got, test.want)
			}
		})
	}
}

func TestInspectionAnomalyMetricsRefreshWithoutTriggeringManualRun(t *testing.T) {
	engine := &InspectionEngine{}
	policy := defaultInspectionPolicy()
	policy.AnomalyTriggerEnabled = true
	accounts := map[string]Account{
		"healthy":   {ID: "healthy", Editable: true},
		"unhealthy": {ID: "unhealthy", Editable: true},
	}
	records := map[string]inspectionRecord{
		"healthy":   {Result: InspectionResult{ID: "healthy", Health: InspectionHealthHealthy}},
		"unhealthy": {Result: InspectionResult{ID: "unhealthy", Health: InspectionHealthUnavailable}},
	}
	triggered, size := engine.evaluateAnomalyTrigger(policy, accounts, records, time.Now().UTC(), false, true)
	if triggered || size != 0 || engine.anomalyEligible != 2 || engine.anomalyCount != 1 || engine.anomalyPercent != 50 {
		t.Fatalf("manual anomaly metrics triggered=%t size=%d eligible=%d count=%d percent=%d", triggered, size, engine.anomalyEligible, engine.anomalyCount, engine.anomalyPercent)
	}
	if snapshot := engine.Snapshot(); snapshot.LastAnomalyTriggerAt != nil {
		t.Fatalf("zero anomaly trigger time was exposed: %#v", snapshot.LastAnomalyTriggerAt)
	}
}

func TestInspectionAnomalyPolicyBoundariesAndDependencies(t *testing.T) {
	valid := defaultInspectionPolicy()
	valid.Enabled = true
	valid.AnomalyTriggerEnabled = true

	for name, mutate := range map[string]func(*InspectionPolicy){
		"minimum values": func(policy *InspectionPolicy) {
			policy.AnomalyThresholdPercent = 1
			policy.AnomalyMinimumAccounts = 1
			policy.AnomalyCooldownMinutes = 5
		},
		"maximum values": func(policy *InspectionPolicy) {
			policy.AnomalyThresholdPercent = 100
			policy.AnomalyMinimumAccounts = maxInspectionAccounts
			policy.AnomalyCooldownMinutes = 1440
		},
	} {
		t.Run(name, func(t *testing.T) {
			policy := valid
			mutate(&policy)
			if _, errValidate := validateInspectionPolicy(policy); errValidate != nil {
				t.Fatalf("valid boundary rejected: %v", errValidate)
			}
		})
	}

	for name, test := range map[string]struct {
		mutate func(*InspectionPolicy)
		want   string
	}{
		"threshold below range": {func(policy *InspectionPolicy) { policy.AnomalyThresholdPercent = -1 }, "anomaly_threshold_percent"},
		"threshold above range": {func(policy *InspectionPolicy) { policy.AnomalyThresholdPercent = 101 }, "anomaly_threshold_percent"},
		"sample above range":    {func(policy *InspectionPolicy) { policy.AnomalyMinimumAccounts = maxInspectionAccounts + 1 }, "anomaly_minimum_accounts"},
		"cooldown below range":  {func(policy *InspectionPolicy) { policy.AnomalyCooldownMinutes = 4 }, "anomaly_cooldown_minutes"},
		"cooldown above range":  {func(policy *InspectionPolicy) { policy.AnomalyCooldownMinutes = 1441 }, "anomaly_cooldown_minutes"},
		"trigger without census": {func(policy *InspectionPolicy) {
			policy.Enabled = false
		}, "requires scheduled native inspection"},
		"full sweep without schedule": {func(policy *InspectionPolicy) {
			policy.AnomalyTriggerEnabled = false
			policy.ModelProbeFullSweep = true
		}, "requires scheduled model probes"},
		"credential delete without delete": {func(policy *InspectionPolicy) {
			policy.AnomalyTriggerEnabled = false
			policy.AutoDeleteInvalidCredentials = true
		}, "requires auto_delete and auto_disable"},
	} {
		t.Run(name, func(t *testing.T) {
			policy := valid
			test.mutate(&policy)
			_, errValidate := validateInspectionPolicy(policy)
			if errValidate == nil || !strings.Contains(errValidate.Error(), test.want) {
				t.Fatalf("validation error = %v, want %q", errValidate, test.want)
			}
		})
	}
}

func TestInspectionPendingAnomalySweepSurvivesRestartWithoutCredential(t *testing.T) {
	dataDir := t.TempDir()
	triggeredAt := time.Date(2026, time.July, 21, 7, 30, 0, 0, time.UTC)
	policy := defaultInspectionPolicy()
	policy.Enabled = true
	policy.AnomalyTriggerEnabled = true
	state := persistedInspectionState{
		Version:               inspectionStoreVersion,
		Policy:                policy,
		Records:               map[string]inspectionRecord{},
		ProbeSweepRemaining:   10_000,
		AnomalyTriggerPending: true,
		LastAnomalyTriggerAt:  triggeredAt,
	}
	if errSave := saveInspectionState(inspectionStorePath(dataDir), state); errSave != nil {
		t.Fatalf("save inspection state: %v", errSave)
	}

	host := inspectionEditableHost(false)
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	engine.SetModelTestService(NewModelTestService(engine.accounts))
	engine.Configure(Config{DataDir: filepath.Clean(dataDir)})
	defer engine.Shutdown()
	snapshot := engine.Snapshot()
	if snapshot.ProbeSweepRemaining != 10_000 || !snapshot.AnomalyTriggerPending || !snapshot.LastAnomalyTriggerAt.Equal(triggeredAt) {
		t.Fatalf("restarted sweep state = %#v", snapshot)
	}
	if snapshot.ActiveProbeArmed {
		t.Fatal("restarted inspection unexpectedly retained a Management credential")
	}
}

func TestInspectionScanExecutesDueInvalidCredentialDeleteServerSide(t *testing.T) {
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	host := inspectionEditableHost(true)
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		deleteCalls++
		if request.Method != http.MethodDelete || request.URL.Path != "/v0/management/auth-files" {
			t.Errorf("delete request = %s %s", request.Method, request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer current-management-secret" {
			t.Error("current Management credential was not forwarded")
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()
	accounts := NewAccountService(host)
	engine := NewInspectionEngine(accounts, host, NewMutationCoordinator())
	engine.Configure(Config{DataDir: t.TempDir(), ManagementBaseURL: server.URL})
	defer engine.Shutdown()
	engine.SetDeleteService(NewAccountDeleteService(accounts, engine.mutations))
	engine.now = func() time.Time { return now }
	eligibleAt := now.Add(-time.Hour)
	engine.mu.Lock()
	engine.policy = defaultInspectionPolicy()
	engine.policy.AutoDisable = true
	engine.policy.AutoDelete = true
	engine.policy.AutoDeleteInvalidCredentials = true
	engine.policy.DeleteGraceHours = 24
	engine.managementKey = "current-management-secret"
	engine.records["inspection-account"] = inspectionRecord{
		Result: InspectionResult{
			ID: "inspection-account", Name: "inspection.json", Provider: "codex",
			Health: InspectionHealthInvalidCredentials, ReasonCode: "authentication_failed",
			Confidence: InspectionConfidenceHigh, Disabled: true, Editable: true, OwnedDisable: true,
			FailureStreak: engine.policy.FailureThreshold, DeleteEligibleAt: &eligibleAt,
			AutoAction: InspectionActionDeleteCandidate, AutoActionStatus: InspectionActionPending,
		},
		Probe:         inspectionProbeSignal{Status: "unavailable", ReasonCode: "authentication_failed", Model: "gpt-5.4", TestedAt: now},
		DisableReason: "authentication_failed", DisabledAt: now.Add(-48 * time.Hour),
		DisabledName: "inspection.json", DisabledPath: "/auths/inspection.json",
	}
	engine.mu.Unlock()

	engine.scan(context.Background())
	if deleteCalls != 1 {
		t.Fatalf("server-side delete calls = %d, want 1; results=%#v actions=%#v", deleteCalls,
			engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 50}), engine.Actions(20))
	}
	actions := engine.Actions(20)
	found := false
	for _, action := range actions {
		if action.Action == InspectionActionDelete && action.Status == InspectionActionSucceeded {
			found = true
		}
	}
	if !found {
		t.Fatalf("successful server-side delete was not journaled: %#v", actions)
	}
}

func TestInspectionAnomalyCountsExcludeAmbiguousAndManualDisabledAccounts(t *testing.T) {
	accounts := map[string]Account{
		"healthy":         {ID: "healthy"},
		"quota":           {ID: "quota"},
		"review":          {ID: "review"},
		"unknown":         {ID: "unknown"},
		"manual-disabled": {ID: "manual-disabled", Disabled: true},
		"owned-disabled":  {ID: "owned-disabled", Disabled: true},
	}
	records := map[string]inspectionRecord{
		"healthy":         {Result: InspectionResult{Health: InspectionHealthHealthy}},
		"quota":           {Result: InspectionResult{Health: InspectionHealthQuotaLimited}},
		"review":          {Result: InspectionResult{Health: InspectionHealthReview}},
		"unknown":         {Result: InspectionResult{Health: InspectionHealthUnknown}},
		"manual-disabled": {Result: InspectionResult{Health: InspectionHealthInvalidCredentials}},
		"owned-disabled":  {Result: InspectionResult{Health: InspectionHealthInvalidCredentials, OwnedDisable: true}},
	}
	eligible, abnormal := inspectionAnomalyCounts(accounts, records)
	if eligible != 3 || abnormal != 2 {
		t.Fatalf("anomaly counts = %d/%d, want 2/3", abnormal, eligible)
	}
}

func TestInspectionAnomalyTriggerCooldownPendingAndRearm(t *testing.T) {
	now := time.Date(2026, time.July, 21, 9, 0, 0, 0, time.UTC)
	policy := defaultInspectionPolicy()
	policy.Enabled = true
	policy.AnomalyTriggerEnabled = true
	policy.AnomalyThresholdPercent = 50
	policy.AnomalyMinimumAccounts = 2
	policy.AnomalyCooldownMinutes = 60
	accounts := map[string]Account{"a": {ID: "a"}, "b": {ID: "b"}}
	records := map[string]inspectionRecord{
		"a": {Result: InspectionResult{Health: InspectionHealthHealthy}},
		"b": {Result: InspectionResult{Health: InspectionHealthInvalidCredentials}},
	}
	engine := NewInspectionEngine(nil, nil, nil)
	engine.policy = policy
	triggered, sweepSize := engine.evaluateAnomalyTrigger(policy, accounts, records, now, true, false)
	if !triggered || sweepSize != 2 || !engine.anomalyTriggerPending || !engine.lastAnomalyTriggerAt.Equal(now) {
		t.Fatalf("first trigger = %v size=%d engine=%#v", triggered, sweepSize, engine.Snapshot())
	}
	engine.updateProbeSweep(inspectionSweepProgress{Total: sweepSize, Remaining: sweepSize, Source: InspectionSweepSourceAnomaly, StartedAt: now}, false)
	engine.SetModelTestService(&ModelTestService{})
	engine.ArmModelProbes("current-management-secret")
	if engine.anomalyTriggerPending || !engine.pendingProbe || !engine.pendingProbeSweep || engine.managementKey == "" {
		t.Fatalf("rearmed engine = %#v", engine.Snapshot())
	}
	triggered, _ = engine.evaluateAnomalyTrigger(policy, accounts, records, now.Add(59*time.Minute), true, true)
	if triggered {
		t.Fatal("cooldown allowed an early retrigger")
	}
	triggered, _ = engine.evaluateAnomalyTrigger(policy, accounts, records, now.Add(60*time.Minute), true, true)
	if !triggered {
		t.Fatal("cooldown boundary did not retrigger")
	}
}

func TestInspectionActiveProbeStreaksAccumulateAndRecoveryRequiresCurrentEvidence(t *testing.T) {
	now := time.Date(2026, time.July, 21, 10, 0, 0, 0, time.UTC)
	account := Account{ID: "account", Editable: true}
	record := inspectionRecord{}
	for attempt := 1; attempt <= 3; attempt++ {
		record.Probe = inspectionProbeSignal{Status: "unavailable", ReasonCode: "authentication_failed", TestedAt: now.Add(time.Duration(attempt) * time.Minute)}
		decision := decideInspection(account, record, record.Probe.TestedAt)
		updateInspectionRecord(&record, account, decision, record.Probe.TestedAt)
		if record.Result.FailureStreak != attempt {
			t.Fatalf("failure streak after attempt %d = %d", attempt, record.Result.FailureStreak)
		}
	}
	disabledAt := now.Add(4 * time.Minute)
	record.Result.OwnedDisable = true
	record.DisableReason = "quota_exhausted"
	record.DisabledAt = disabledAt
	record.DisabledRecoverAfter = disabledAt.Add(time.Hour)
	account.Disabled = true
	record.Result.Health = InspectionHealthQuotaLimited
	policy := defaultInspectionPolicy()
	policy.AutoEnable = true
	if shouldAutoEnableInspection(policy, account, record, disabledAt.Add(2*time.Hour)) {
		t.Fatal("expired quota timer enabled an account that is still explicitly unhealthy")
	}
	for attempt := 1; attempt <= policy.RecoveryThreshold; attempt++ {
		testedAt := disabledAt.Add(time.Duration(attempt) * time.Minute)
		record.Probe = inspectionProbeSignal{Status: "available", ReasonCode: "model_response_ok", TestedAt: testedAt}
		decision := decideInspection(account, record, testedAt)
		updateInspectionRecord(&record, account, decision, testedAt)
	}
	if record.Result.HealthyStreak != policy.RecoveryThreshold || !shouldAutoEnableInspection(policy, account, record, record.Probe.TestedAt) {
		t.Fatalf("active recovery evidence did not enable: %#v", record.Result)
	}
}

func TestInspectionInvalidCredentialDeletionAllowList(t *testing.T) {
	policy := defaultInspectionPolicy()
	policy.AutoDisable = true
	policy.AutoDelete = true
	policy.AutoDeleteInvalidCredentials = true
	base := inspectionRecord{
		Result: InspectionResult{
			Health: InspectionHealthInvalidCredentials, ReasonCode: "invalid_credentials",
			Confidence: InspectionConfidenceHigh, FailureStreak: policy.FailureThreshold,
		},
		DisableReason: "invalid_credentials",
	}
	if !inspectionDeleteReasonAllowed(policy, base) {
		t.Fatal("persistent invalid credentials were not allowed")
	}
	for name, mutate := range map[string]func(*inspectionRecord){
		"opt out":         func(record *inspectionRecord) { policy.AutoDeleteInvalidCredentials = false },
		"low confidence":  func(record *inspectionRecord) { record.Result.Confidence = InspectionConfidenceLow },
		"below threshold": func(record *inspectionRecord) { record.Result.FailureStreak = policy.FailureThreshold - 1 },
		"reason changed":  func(record *inspectionRecord) { record.Result.ReasonCode = "model_not_found" },
		"generic review": func(record *inspectionRecord) {
			record.DisableReason = "authentication_review"
			record.Result.ReasonCode = "authentication_review"
		},
		"quota": func(record *inspectionRecord) {
			record.DisableReason = "quota_exhausted"
			record.Result.ReasonCode = "quota_exhausted"
			record.Result.Health = InspectionHealthQuotaLimited
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidatePolicy := policy
			candidatePolicy.AutoDeleteInvalidCredentials = true
			record := base
			mutate(&record)
			if name == "opt out" {
				candidatePolicy.AutoDeleteInvalidCredentials = false
			}
			if inspectionDeleteReasonAllowed(candidatePolicy, record) {
				t.Fatalf("unsafe candidate was allowed: %#v", record)
			}
		})
	}
}

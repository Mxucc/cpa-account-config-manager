package manager

import (
	"bytes"
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

func TestInspectionEmptyCollectionsUseJSONArrays(t *testing.T) {
	engine := &InspectionEngine{}
	results := engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 50})
	if results.Results == nil {
		t.Fatal("Results is nil, want an empty JSON array")
	}
	if actions := engine.Actions(50); actions == nil {
		t.Fatal("Actions is nil, want an empty JSON array")
	}
}

func TestInspectionReviewLifecyclePersistsSanitizedOperatorDecision(t *testing.T) {
	now := time.Date(2026, time.July, 21, 9, 0, 0, 0, time.UTC)
	dataDir := t.TempDir()
	engine := NewInspectionEngine(nil, nil, nil)
	engine.Configure(Config{DataDir: dataDir})
	engine.now = func() time.Time { return now }
	engine.mu.Lock()
	engine.records["review-account"] = inspectionRecord{Result: InspectionResult{
		ID: "review-account", Name: "review.json", Health: InspectionHealthReview,
		ReasonCode: "authentication_review", ReviewStatus: InspectionReviewPending, LastCheckedAt: now.Add(-time.Minute),
	}}
	engine.mu.Unlock()

	resolved, errResolve := engine.UpdateReview(InspectionReviewRequest{AccountID: "review-account", Action: "resolve"})
	if errResolve != nil {
		t.Fatalf("resolve review: %v", errResolve)
	}
	if resolved.ReviewStatus != InspectionReviewResolved || resolved.ReviewedAt == nil || !resolved.ReviewedAt.Equal(now) {
		t.Fatalf("resolved review = %#v", resolved)
	}
	engine.Shutdown()

	raw, errRead := os.ReadFile(inspectionStorePath(dataDir))
	if errRead != nil {
		t.Fatalf("read review state: %v", errRead)
	}
	for _, forbidden := range []string{"access_token", "management-secret", "authorization"} {
		if bytes.Contains(bytes.ToLower(raw), []byte(forbidden)) {
			t.Fatalf("review state leaked %q: %s", forbidden, raw)
		}
	}

	reloaded := NewInspectionEngine(nil, nil, nil)
	reloaded.Configure(Config{DataDir: dataDir})
	defer reloaded.Shutdown()
	result := reloaded.ListResults(InspectionResultQuery{Page: 1, PageSize: 20}).Results[0]
	if result.ReviewStatus != InspectionReviewResolved || result.ReviewedAt == nil || !result.ReviewedAt.Equal(now) {
		t.Fatalf("reloaded review = %#v", result)
	}
	if _, errIgnore := reloaded.UpdateReview(InspectionReviewRequest{AccountID: "review-account", Action: "ignore"}); errIgnore != nil {
		t.Fatalf("ignore review: %v", errIgnore)
	}
	reopened, errReopen := reloaded.UpdateReview(InspectionReviewRequest{AccountID: "review-account", Action: "reopen"})
	if errReopen != nil || reopened.ReviewStatus != InspectionReviewPending || reopened.ReviewedAt != nil {
		t.Fatalf("reopened review = %#v, error=%v", reopened, errReopen)
	}
}

func TestInspectionRunHistoryIsBoundedSanitizedAndPersistent(t *testing.T) {
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	engine := &InspectionEngine{}
	engine.startRunHistoryLocked(InspectionRunModeFull, InspectionSweepSourceManual, now)
	engine.probeSweepTotal = 12
	engine.probeSweepCompleted = 12
	engine.retryTotal = 2
	engine.retryCompleted = 2
	engine.updateRunHistorySummaryLocked(InspectionRunSummary{StartedAt: now, FinishedAt: now.Add(time.Minute), Scanned: 12, Healthy: 9})
	engine.updateRunHistoryLocked(InspectionSweepStatusCompleted, InspectionProbePhaseDone, now.Add(time.Minute))

	runs := recentInspectionRuns(engine.runs, 10)
	if len(runs) != 1 || runs[0].Mode != InspectionRunModeFull || runs[0].Status != InspectionSweepStatusCompleted ||
		runs[0].PrimaryDone != 12 || runs[0].RetryDone != 2 || runs[0].Summary.Healthy != 9 || engine.activeRunID != "" {
		t.Fatalf("completed run history = %#v active=%q", runs, engine.activeRunID)
	}

	path := inspectionStorePath(t.TempDir())
	state := persistedInspectionState{Version: inspectionStoreVersion, Policy: defaultInspectionPolicy(), Records: map[string]inspectionRecord{}, Runs: engine.runs}
	if errSave := saveInspectionState(path, state); errSave != nil {
		t.Fatalf("save run history: %v", errSave)
	}
	loaded, errLoad := loadInspectionState(path)
	if errLoad != nil || len(loaded.Runs) != 1 || loaded.Runs[0].Summary.Healthy != 9 {
		t.Fatalf("loaded run history = %#v, error=%v", loaded.Runs, errLoad)
	}
}

func TestClassifyUsageFailureRequiresCredentialSemantics(t *testing.T) {
	now := time.Date(2026, time.July, 20, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		record       cpaapi.UsageRecord
		wantReason   string
		wantEligible bool
	}{
		{
			name: "bare unauthorized remains review only",
			record: cpaapi.UsageRecord{
				Provider: "codex",
				Failed:   true,
				Failure:  cpaapi.UsageFailure{StatusCode: http.StatusUnauthorized, Body: `Bearer upstream-secret`},
			},
			wantReason: "authentication_review",
		},
		{
			name: "explicit invalid token is actionable",
			record: cpaapi.UsageRecord{
				Provider: "codex",
				Failed:   true,
				Failure: cpaapi.UsageFailure{
					StatusCode: http.StatusUnauthorized,
					Body:       `{"error":{"code":"invalid_token","message":"invalid token sk-secret-value"}}`,
				},
			},
			wantReason:   "invalid_credentials",
			wantEligible: true,
		},
		{
			name: "region permission remains review only",
			record: cpaapi.UsageRecord{
				Provider: "xai",
				Failed:   true,
				Failure:  cpaapi.UsageFailure{StatusCode: http.StatusForbidden, Body: `permission_denied in this region`},
			},
			wantReason: "authentication_review",
		},
		{
			name: "deactivated workspace is actionable",
			record: cpaapi.UsageRecord{
				Provider: "codex",
				Failed:   true,
				Failure:  cpaapi.UsageFailure{StatusCode: http.StatusPaymentRequired, Body: `deactivated_workspace`},
			},
			wantReason:   "workspace_deactivated",
			wantEligible: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := classifyUsageFailure(test.record, now)
			if evidence.ReasonCode != test.wantReason || evidence.AutoDisableEligible != test.wantEligible {
				t.Fatalf("evidence = %#v", evidence)
			}
			raw, errMarshal := json.Marshal(evidence)
			if errMarshal != nil {
				t.Fatalf("marshal evidence: %v", errMarshal)
			}
			for _, secret := range []string{"upstream-secret", "sk-secret-value", "Bearer"} {
				if bytes.Contains(raw, []byte(secret)) {
					t.Fatalf("evidence leaked %q: %s", secret, raw)
				}
			}
		})
	}
}

func TestNativeStatusRequiresExplicitFailureSemantics(t *testing.T) {
	now := time.Date(2026, time.July, 20, 8, 30, 0, 0, time.UTC)
	for _, test := range []struct {
		status     string
		wantHealth string
		wantReason string
		eligible   bool
	}{
		{status: "unauthorized", wantHealth: InspectionHealthReview, wantReason: "authentication_review"},
		{status: "payment_required", wantHealth: InspectionHealthReview, wantReason: "billing_review"},
		{status: "invalid_grant", wantHealth: InspectionHealthInvalidCredentials, wantReason: "invalid_credentials", eligible: true},
		{status: "quota exhausted", wantHealth: InspectionHealthQuotaLimited, wantReason: "quota_exhausted", eligible: true},
	} {
		decision := decideInspection(Account{StatusMessage: test.status}, inspectionRecord{}, now)
		if decision.Health != test.wantHealth || decision.ReasonCode != test.wantReason || decision.AutoDisableEligible != test.eligible {
			t.Fatalf("status %q decision = %#v", test.status, decision)
		}
	}
}

func TestInspectionEnginePersistsOnlySanitizedEvidence(t *testing.T) {
	now := time.Date(2026, time.July, 20, 8, 0, 0, 0, time.UTC)
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "codex-account",
			Name:      "codex-account.json",
			Provider:  "codex",
			Type:      "codex",
			Status:    "ready",
			Source:    "file",
			Path:      "/auths/codex-account.json",
		}},
		details: map[string]cpaapi.HostAuthGetResponse{},
	}
	accounts := NewAccountService(host)
	engine := NewInspectionEngine(accounts, host, NewMutationCoordinator())
	engine.now = func() time.Time { return now }
	dataDir := t.TempDir()
	engine.Configure(Config{DataDir: dataDir})
	engine.Observe(cpaapi.UsageRecord{
		Provider:  "codex",
		AuthIndex: "codex-account",
		Failed:    true,
		Failure: cpaapi.UsageFailure{
			StatusCode: http.StatusUnauthorized,
			Body:       `{"error":{"code":"invalid_token","message":"Bearer persisted-secret"}}`,
		},
	})
	engine.scan(context.Background())

	listed := engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 50})
	if listed.Total != 1 || len(listed.Results) != 1 {
		t.Fatalf("listed = %#v", listed)
	}
	result := listed.Results[0]
	if result.Health != InspectionHealthInvalidCredentials || result.ReasonCode != "invalid_credentials" || !result.AutoDisableEligible {
		t.Fatalf("result = %#v", result)
	}
	engine.Shutdown()

	stored, errRead := os.ReadFile(filepath.Join(dataDir, "inspection-state.json"))
	if errRead != nil {
		t.Fatalf("read persisted inspection state: %v", errRead)
	}
	for _, secret := range []string{"persisted-secret", "Bearer", "invalid token"} {
		if bytes.Contains(stored, []byte(secret)) {
			t.Fatalf("persisted inspection state leaked %q: %s", secret, stored)
		}
	}
	if !bytes.Contains(stored, []byte(`"reason_code":"invalid_credentials"`)) {
		t.Fatalf("persisted inspection state = %s", stored)
	}

	reloaded := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	reloaded.Configure(Config{DataDir: dataDir})
	defer reloaded.Shutdown()
	reloadedResult := reloaded.ListResults(InspectionResultQuery{Page: 1, PageSize: 50})
	if reloadedResult.Total != 1 || reloadedResult.Results[0].ReasonCode != "invalid_credentials" {
		t.Fatalf("reloaded result = %#v", reloadedResult)
	}
}

func TestInspectionCancelledDuringAccountLoopClearsRunningState(t *testing.T) {
	host := &fakeAuthHost{entries: []cpaapi.HostAuthFileEntry{{
		AuthIndex: "cancelled-account",
		Name:      "cancelled-account.json",
		Provider:  "codex",
		Type:      "codex",
		Status:    "ready",
		Source:    "file",
		Path:      "/auths/cancelled-account.json",
	}}}
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	engine.Configure(Config{DataDir: t.TempDir()})
	defer engine.Shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	now := time.Date(2026, time.July, 20, 9, 0, 0, 0, time.UTC)
	nowCalls := 0
	engine.now = func() time.Time {
		nowCalls++
		if nowCalls == 2 {
			cancel()
		}
		return now
	}
	engine.scan(ctx)

	snapshot := engine.Snapshot()
	if snapshot.Running || !snapshot.ScanStartedAt.IsZero() {
		t.Fatalf("cancelled scan retained running state: %#v", snapshot)
	}
}

func TestInspectionPassiveFailureCountsUsageEventsNotScans(t *testing.T) {
	now := time.Date(2026, time.July, 20, 9, 30, 0, 0, time.UTC)
	host := inspectionEditableHost(false)
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	engine.now = func() time.Time { return now }
	engine.Configure(Config{DataDir: t.TempDir()})
	defer engine.Shutdown()
	engine.mu.Lock()
	engine.policy = InspectionPolicy{
		ScanIntervalMinutes: 30,
		FailureThreshold:    2,
		RecoveryThreshold:   2,
		AutoDisable:         true,
		DeleteGraceHours:    168,
		DeleteBatchSize:     10,
	}
	engine.mu.Unlock()

	failure := cpaapi.UsageRecord{
		Provider:  "codex",
		AuthIndex: "inspection-account",
		Failed:    true,
		Failure:   cpaapi.UsageFailure{StatusCode: http.StatusUnauthorized, Body: `{"error":{"code":"invalid_token"}}`},
	}
	engine.Observe(failure)
	for range 3 {
		engine.scan(context.Background())
	}
	result := engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 50}).Results[0]
	if result.FailureStreak != 1 || result.Disabled || result.OwnedDisable {
		t.Fatalf("one passive failure was counted more than once: %#v", result)
	}
	host.mu.Lock()
	savesAfterOneFailure := len(host.saves)
	host.mu.Unlock()
	if savesAfterOneFailure != 0 {
		t.Fatalf("one passive failure triggered %d saves", savesAfterOneFailure)
	}

	engine.Observe(failure)
	engine.scan(context.Background())
	result = engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 50}).Results[0]
	if result.FailureStreak != 2 || !result.Disabled || !result.OwnedDisable {
		t.Fatalf("two passive failures did not trigger the configured action: %#v", result)
	}
}

func TestInspectionPolicyValidationKeepsDestructiveDefaultsOff(t *testing.T) {
	policy := defaultInspectionPolicy()
	if policy.Enabled || policy.AutoDisable || policy.AutoEnable || policy.AutoDelete {
		t.Fatalf("destructive inspection defaults are enabled: %#v", policy)
	}
	if _, errValidate := validateInspectionPolicy(InspectionPolicy{
		ScanIntervalMinutes: 30,
		FailureThreshold:    3,
		RecoveryThreshold:   2,
		AutoDelete:          true,
		DeleteGraceHours:    168,
		DeleteBatchSize:     10,
	}); errValidate == nil || !strings.Contains(errValidate.Error(), "requires auto_disable") {
		t.Fatalf("validate auto-delete error = %v", errValidate)
	}
}

func TestInspectionAutomationDisablesAndOnlyRecoversOwnedAccount(t *testing.T) {
	now := time.Date(2026, time.July, 20, 10, 0, 0, 0, time.UTC)
	host := inspectionEditableHost(false)
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	engine.now = func() time.Time { return now }
	engine.Configure(Config{DataDir: t.TempDir()})
	defer engine.Shutdown()

	engine.mu.Lock()
	engine.policy = InspectionPolicy{
		ScanIntervalMinutes: 30,
		FailureThreshold:    2,
		RecoveryThreshold:   2,
		AutoDisable:         true,
		AutoEnable:          true,
		DeleteGraceHours:    168,
		DeleteBatchSize:     10,
	}
	engine.mu.Unlock()
	for range 2 {
		engine.Observe(cpaapi.UsageRecord{
			Provider:  "codex",
			AuthIndex: "inspection-account",
			Failed:    true,
			Failure:   cpaapi.UsageFailure{StatusCode: http.StatusUnauthorized, Body: `{"error":{"code":"invalid_token","message":"Bearer action-secret"}}`},
		})
	}
	engine.scan(context.Background())

	result := engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 50}).Results[0]
	if !result.Disabled || !result.OwnedDisable || result.AutoAction != InspectionActionDisable || result.AutoActionStatus != InspectionActionSucceeded {
		t.Fatalf("disabled result = %#v", result)
	}
	host.mu.Lock()
	if len(host.saves) != 1 || !bytes.Contains(host.saves[0].JSON, []byte(`"disabled":true`)) || !bytes.Contains(host.saves[0].JSON, []byte(`"access_token":"account-secret"`)) {
		t.Fatalf("disable saves = %#v", host.saves)
	}
	host.entries[0].Disabled = true
	host.mu.Unlock()

	now = now.Add(time.Hour)
	engine.Observe(cpaapi.UsageRecord{Provider: "codex", AuthIndex: "inspection-account", Failed: false})
	engine.scan(context.Background())
	firstRecovery := engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 50}).Results[0]
	if !firstRecovery.Disabled || firstRecovery.HealthyStreak != 1 {
		t.Fatalf("first recovery result = %#v", firstRecovery)
	}
	engine.scan(context.Background())
	stillDisabled := engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 50}).Results[0]
	if !stillDisabled.Disabled || stillDisabled.HealthyStreak != 1 {
		t.Fatalf("one recovery event was counted more than once: %#v", stillDisabled)
	}
	engine.Observe(cpaapi.UsageRecord{Provider: "codex", AuthIndex: "inspection-account", Failed: false})
	engine.scan(context.Background())
	result = engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 50}).Results[0]
	if result.Disabled || result.OwnedDisable || result.AutoAction != InspectionActionEnable || result.AutoActionStatus != InspectionActionSucceeded {
		t.Fatalf("enabled result = %#v", result)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.saves) != 2 || !bytes.Contains(host.saves[1].JSON, []byte(`"disabled":false`)) {
		t.Fatalf("enable saves = %#v", host.saves)
	}
	for _, action := range engine.Actions(20) {
		raw, _ := json.Marshal(action)
		if bytes.Contains(raw, []byte("action-secret")) || bytes.Contains(raw, []byte("account-secret")) {
			t.Fatalf("action leaked a credential: %s", raw)
		}
	}
}

func TestInspectionAutoDeleteRequiresGraceOwnershipAndCurrentManagementKey(t *testing.T) {
	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	host := inspectionEditableHost(false)
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	engine.now = func() time.Time { return now }
	dataDir := t.TempDir()
	engine.Configure(Config{DataDir: dataDir})
	defer engine.Shutdown()
	engine.mu.Lock()
	engine.policy = InspectionPolicy{
		ScanIntervalMinutes: 30,
		FailureThreshold:    2,
		RecoveryThreshold:   2,
		AutoDisable:         true,
		AutoDelete:          true,
		DeleteGraceHours:    24,
		DeleteBatchSize:     10,
	}
	engine.mu.Unlock()
	for range 2 {
		engine.Observe(cpaapi.UsageRecord{
			Provider:  "codex",
			AuthIndex: "inspection-account",
			Failed:    true,
			Failure:   cpaapi.UsageFailure{StatusCode: http.StatusPaymentRequired, Body: `deactivated_workspace Bearer delete-secret`},
		})
	}
	engine.scan(context.Background())
	host.mu.Lock()
	host.entries[0].Disabled = true
	host.mu.Unlock()
	now = now.Add(23 * time.Hour)
	engine.scan(context.Background())
	beforeGrace := engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 50}).Results[0]
	if beforeGrace.AutoAction == InspectionActionDeleteCandidate {
		t.Fatalf("delete became eligible before grace: %#v", beforeGrace)
	}
	now = now.Add(2 * time.Hour)
	engine.scan(context.Background())
	due := engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 50}).Results[0]
	if due.AutoAction != InspectionActionDeleteCandidate || due.AutoActionStatus != InspectionActionPending || due.DeleteEligibleAt == nil {
		t.Fatalf("due result = %#v", due)
	}

	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		deleteCalls++
		if request.Method != http.MethodDelete || request.URL.Path != "/v0/management/auth-files" || request.URL.Query().Get("name") != "inspection.json" {
			t.Errorf("delete request = %s %s", request.Method, request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer current-management-secret" {
			t.Errorf("authorization was not forwarded")
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	deletions := NewAccountDeleteService(NewAccountService(host), engine.mutations)
	run := engine.ExecutePendingDeletes(context.Background(), deletions, server.URL, "current-management-secret")
	if run.Attempted != 1 || run.Succeeded != 1 || run.Failed != 0 || deleteCalls != 1 {
		t.Fatalf("delete run = %#v calls=%d", run, deleteCalls)
	}
	stored, errRead := os.ReadFile(filepath.Join(dataDir, "inspection-state.json"))
	if errRead != nil {
		t.Fatalf("read inspection state: %v", errRead)
	}
	for _, secret := range []string{"current-management-secret", "delete-secret", "account-secret"} {
		if bytes.Contains(stored, []byte(secret)) {
			t.Fatalf("delete state leaked %q: %s", secret, stored)
		}
	}
}

func TestInspectionRecoveredAccountRevokesPendingDeleteCandidate(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	host := inspectionEditableHost(true)
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	engine.now = func() time.Time { return now }
	engine.Configure(Config{DataDir: t.TempDir()})
	defer engine.Shutdown()
	eligibleAt := now.Add(-time.Hour)
	firstUnhealthy := now.Add(-48 * time.Hour)
	engine.mu.Lock()
	engine.policy = InspectionPolicy{
		ScanIntervalMinutes: 30,
		FailureThreshold:    2,
		RecoveryThreshold:   2,
		AutoDisable:         true,
		AutoDelete:          true,
		DeleteGraceHours:    24,
		DeleteBatchSize:     10,
	}
	engine.records["inspection-account"] = inspectionRecord{
		Result: InspectionResult{
			ID:               "inspection-account",
			Name:             "inspection.json",
			Health:           InspectionHealthDeactivated,
			Disabled:         true,
			Editable:         true,
			OwnedDisable:     true,
			FirstUnhealthyAt: &firstUnhealthy,
			DeleteEligibleAt: &eligibleAt,
			AutoAction:       InspectionActionDeleteCandidate,
			AutoActionStatus: InspectionActionPending,
		},
		Signal: inspectionSignal{
			ReasonCode:    "workspace_deactivated",
			LastFailureAt: now.Add(-2 * time.Hour),
			LastSuccessAt: now.Add(-time.Hour),
		},
		DisableReason: "workspace_deactivated",
		DisabledAt:    now.Add(-48 * time.Hour),
		DisabledName:  "inspection.json",
		DisabledPath:  "/auths/inspection.json",
	}
	engine.mu.Unlock()

	engine.scan(context.Background())
	result := engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 50}).Results[0]
	if result.Health != InspectionHealthHealthy || result.AutoAction != "" || result.DeleteEligibleAt != nil {
		t.Fatalf("recovered result retained delete candidate: %#v", result)
	}
	run := engine.ExecutePendingDeletes(context.Background(), NewAccountDeleteService(NewAccountService(host), engine.mutations), "http://127.0.0.1:8317", "management-secret")
	if run.Attempted != 0 {
		t.Fatalf("recovered candidate was executed: %#v", run)
	}
}

func TestInspectionAutoDeleteRevalidatesRecoveryBeforeMutation(t *testing.T) {
	now := time.Date(2026, time.July, 22, 14, 0, 0, 0, time.UTC)
	host := inspectionEditableHost(true)
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	engine.now = func() time.Time { return now }
	engine.Configure(Config{DataDir: t.TempDir()})
	defer engine.Shutdown()
	seedDueInspectionDeleteCandidate(engine, now)
	engine.Observe(cpaapi.UsageRecord{Provider: "codex", AuthIndex: "inspection-account", Failed: false})

	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		deleteCalls++
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	run := engine.ExecutePendingDeletes(context.Background(), NewAccountDeleteService(NewAccountService(host), engine.mutations), server.URL, "management-secret")
	if run.Attempted != 1 || run.Skipped != 1 || run.Succeeded != 0 || deleteCalls != 0 {
		t.Fatalf("revalidated delete run = %#v calls=%d", run, deleteCalls)
	}
	result := engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 50}).Results[0]
	if result.Health != InspectionHealthHealthy || result.AutoAction != "" || result.DeleteEligibleAt != nil {
		t.Fatalf("recovered candidate was not revoked: %#v", result)
	}
}

func TestInspectionAutoDeleteFailureRemainsEligibleAfterRetryDelay(t *testing.T) {
	now := time.Date(2026, time.July, 22, 15, 0, 0, 0, time.UTC)
	host := inspectionEditableHost(true)
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	engine.now = func() time.Time { return now }
	engine.Configure(Config{DataDir: t.TempDir()})
	defer engine.Shutdown()
	seedDueInspectionDeleteCandidate(engine, now)

	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		deleteCalls++
		if deleteCalls == 1 {
			writer.WriteHeader(http.StatusInternalServerError)
			_, _ = writer.Write([]byte(`{"error":"temporary"}`))
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()
	deletions := NewAccountDeleteService(NewAccountService(host), engine.mutations)
	first := engine.ExecutePendingDeletes(context.Background(), deletions, server.URL, "management-secret")
	if first.Attempted != 1 || first.Failed != 1 || deleteCalls != 1 {
		t.Fatalf("first delete run = %#v calls=%d", first, deleteCalls)
	}
	failed := engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 50}).Results[0]
	if failed.AutoAction != InspectionActionDeleteCandidate || failed.AutoActionStatus != InspectionActionFailed {
		t.Fatalf("failed candidate cannot be retried: %#v", failed)
	}

	now = now.Add(inspectionDeleteRetry + time.Minute)
	second := engine.ExecutePendingDeletes(context.Background(), deletions, server.URL, "management-secret")
	if second.Attempted != 1 || second.Succeeded != 1 || deleteCalls != 2 {
		t.Fatalf("second delete run = %#v calls=%d", second, deleteCalls)
	}
}

func TestInspectionAutoDeleteRequiresPhysicalDisabledState(t *testing.T) {
	now := time.Date(2026, time.July, 22, 16, 0, 0, 0, time.UTC)
	host := inspectionEditableHost(true)
	host.details["inspection-account"] = cpaapi.HostAuthGetResponse{
		AuthIndex: "inspection-account",
		Name:      "inspection.json",
		Path:      "/auths/inspection.json",
		JSON:      json.RawMessage(`{"type":"codex","disabled":false}`),
	}
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	engine.now = func() time.Time { return now }
	engine.Configure(Config{DataDir: t.TempDir()})
	defer engine.Shutdown()
	seedDueInspectionDeleteCandidate(engine, now)

	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		deleteCalls++
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	run := engine.ExecutePendingDeletes(context.Background(), NewAccountDeleteService(NewAccountService(host), engine.mutations), server.URL, "management-secret")
	if run.Attempted != 1 || run.Skipped != 1 || run.Succeeded != 0 || deleteCalls != 0 {
		t.Fatalf("physical-state delete run = %#v calls=%d", run, deleteCalls)
	}
	result := engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 50}).Results[0]
	if result.OwnedDisable || result.AutoAction != "" || result.DeleteEligibleAt != nil {
		t.Fatalf("physically enabled account retained deletion ownership: %#v", result)
	}
}

func TestInspectionPolicyRouteRequiresExplicitAutoDeleteConfirmation(t *testing.T) {
	app := NewApp(inspectionEditableHost(false), []byte("index"))
	app.Configure([]byte("data_dir: " + t.TempDir()))
	defer app.Close()
	body := []byte(`{"enabled":false,"scan_interval_minutes":30,"failure_threshold":3,"recovery_threshold":2,"auto_disable":true,"auto_enable":false,"auto_delete":true,"delete_grace_hours":168,"delete_batch_size":10}`)
	withoutConfirmation := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut,
		Path:   "/v0/management/plugins/cpa-account-config-manager/inspection",
		Body:   body,
	})
	if withoutConfirmation.StatusCode != http.StatusBadRequest || !bytes.Contains(withoutConfirmation.Body, []byte("explicit confirmation")) {
		t.Fatalf("without confirmation = %d %s", withoutConfirmation.StatusCode, withoutConfirmation.Body)
	}
	confirmedBody := bytes.TrimSuffix(body, []byte("}"))
	confirmedBody = append(confirmedBody, []byte(`,"confirm_auto_delete":true}`)...)
	confirmed := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut,
		Path:   "/v0/management/plugins/cpa-account-config-manager/inspection",
		Body:   confirmedBody,
	})
	if confirmed.StatusCode != http.StatusOK {
		t.Fatalf("confirmed = %d %s", confirmed.StatusCode, confirmed.Body)
	}
}

func TestInspectionPolicyRouteRequiresSeparateInvalidCredentialDeleteConfirmation(t *testing.T) {
	app := NewApp(inspectionEditableHost(false), []byte("index"))
	app.Configure([]byte("data_dir: " + t.TempDir()))
	defer app.Close()
	body := []byte(`{"enabled":false,"scan_interval_minutes":30,"failure_threshold":3,"recovery_threshold":2,"auto_disable":true,"auto_enable":false,"auto_delete":true,"auto_delete_invalid_credentials":true,"delete_grace_hours":168,"delete_batch_size":10,"confirm_auto_delete":true}`)
	withoutInvalidConfirmation := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut,
		Path:   "/v0/management/plugins/cpa-account-config-manager/inspection",
		Body:   body,
	})
	if withoutInvalidConfirmation.StatusCode != http.StatusBadRequest || !bytes.Contains(withoutInvalidConfirmation.Body, []byte("auto_delete_invalid_credentials requires explicit confirmation")) {
		t.Fatalf("without invalid-credential confirmation = %d %s", withoutInvalidConfirmation.StatusCode, withoutInvalidConfirmation.Body)
	}
	confirmedBody := bytes.TrimSuffix(body, []byte("}"))
	confirmedBody = append(confirmedBody, []byte(`,"confirm_delete_invalid_credentials":true}`)...)
	confirmed := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut,
		Path:   "/v0/management/plugins/cpa-account-config-manager/inspection",
		Body:   confirmedBody,
	})
	if confirmed.StatusCode != http.StatusOK {
		t.Fatalf("confirmed invalid-credential deletion = %d %s", confirmed.StatusCode, confirmed.Body)
	}
}

func TestAccountAutomationSummariesArePolicyAwareAndSanitized(t *testing.T) {
	now := time.Date(2026, time.July, 20, 14, 0, 0, 0, time.UTC)
	recoverAfter := now.Add(90 * time.Minute)
	disabledAt := now.Add(-2 * time.Hour)
	deleteEligibleAt := now.Add(22 * time.Hour)
	engine := &InspectionEngine{
		policy: InspectionPolicy{
			Enabled:           true,
			AutoDisable:       true,
			AutoEnable:        true,
			AutoDelete:        false,
			RecoveryThreshold: 2,
			DeleteGraceHours:  24,
		},
		records: map[string]inspectionRecord{
			"quota": {
				Result: InspectionResult{
					ID:                  "quota",
					Health:              InspectionHealthQuotaLimited,
					ReasonCode:          "quota_exhausted",
					Recommendation:      InspectionRecommendationEnable,
					OwnedDisable:        true,
					Disabled:            true,
					AutoDisableEligible: true,
					FailureStreak:       2,
					LastCheckedAt:       now,
					AutoAction:          InspectionActionDisable,
					AutoActionStatus:    InspectionActionSucceeded,
				},
				DisableReason:        "quota_exhausted",
				DisabledAt:           disabledAt,
				DisabledRecoverAfter: recoverAfter,
				DisabledName:         "Bearer summary-secret",
				DisabledPath:         "/auths/summary-secret.json",
				DisabledVersion:      "summary-secret-revision",
			},
			"credentials": {
				Result: InspectionResult{
					ID:             "credentials",
					Health:         InspectionHealthInvalidCredentials,
					ReasonCode:     "invalid_credentials",
					Recommendation: InspectionRecommendationReauth,
					OwnedDisable:   true,
					Disabled:       true,
					LastCheckedAt:  now,
					HealthyStreak:  1,
				},
				DisableReason: "invalid_credentials",
				DisabledAt:    disabledAt,
			},
			"delete-off": {
				Result: InspectionResult{
					ID:               "delete-off",
					Health:           InspectionHealthDeactivated,
					ReasonCode:       "workspace_deactivated",
					Recommendation:   InspectionRecommendationDelete,
					OwnedDisable:     true,
					Disabled:         true,
					LastCheckedAt:    now,
					DeleteEligibleAt: &deleteEligibleAt,
				},
				DisableReason: "workspace_deactivated",
				DisabledAt:    disabledAt,
			},
			"manual": {
				Result: InspectionResult{
					ID:             "manual",
					Health:         InspectionHealthDisabled,
					ReasonCode:     "manual_disabled",
					Recommendation: InspectionRecommendationReview,
					Disabled:       true,
					LastCheckedAt:  now,
				},
			},
			"stale-owned": {
				Result: InspectionResult{
					ID:             "stale-owned",
					Health:         InspectionHealthHealthy,
					ReasonCode:     "healthy_recent_success",
					Recommendation: InspectionRecommendationEnable,
					OwnedDisable:   true,
					Disabled:       true,
					LastCheckedAt:  now,
				},
				DisableReason: "quota_exhausted",
				DisabledAt:    disabledAt,
			},
			"not-inspected": {Result: InspectionResult{ID: "not-inspected"}},
		},
	}

	summaries := engine.AccountAutomationSummaries([]Account{
		{ID: "quota", Disabled: true},
		{ID: "credentials", Disabled: true},
		{ID: "delete-off", Disabled: true},
		{ID: "manual", Disabled: true},
		{ID: "stale-owned", Disabled: false},
		{ID: "not-inspected"},
		{ID: "missing"},
	})
	if len(summaries) != 5 {
		t.Fatalf("summaries len = %d, want 5: %#v", len(summaries), summaries)
	}
	quota := summaries["quota"]
	if !quota.OwnedDisable || quota.DisableReason != "quota_exhausted" || quota.RecoverAfter == nil || !quota.RecoverAfter.Equal(recoverAfter) || !quota.AutoEnableEnabled || !quota.AutoDisableEligible || quota.FailureStreak != 2 {
		t.Fatalf("quota summary = %#v", quota)
	}
	credentials := summaries["credentials"]
	if !credentials.OwnedDisable || credentials.RecoverAfter != nil || credentials.HealthyStreak != 1 || credentials.RecoveryThreshold != 2 {
		t.Fatalf("credential summary = %#v", credentials)
	}
	deleteOff := summaries["delete-off"]
	if deleteOff.Recommendation != InspectionRecommendationDelete || deleteOff.AutoDeleteEnabled || deleteOff.DeleteEligibleAt != nil {
		t.Fatalf("disabled auto-delete summary = %#v", deleteOff)
	}
	manual := summaries["manual"]
	if manual.OwnedDisable || manual.DisableReason != "" || manual.DisabledAt != nil {
		t.Fatalf("manual summary claimed inspection ownership: %#v", manual)
	}
	stale := summaries["stale-owned"]
	if stale.OwnedDisable || stale.DisableReason != "" || stale.DisabledAt != nil {
		t.Fatalf("enabled account retained stale ownership: %#v", stale)
	}
	encoded, errMarshal := json.Marshal(summaries)
	if errMarshal != nil {
		t.Fatalf("marshal summaries: %v", errMarshal)
	}
	for _, secret := range []string{"summary-secret", "Bearer", "revision", "/auths/"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("automation summary leaked %q: %s", secret, encoded)
		}
	}
}

func TestAccountAutomationSummaryIncludesPendingDeleteRetry(t *testing.T) {
	now := time.Date(2026, time.July, 20, 16, 0, 0, 0, time.UTC)
	eligibleAt := now.Add(-time.Hour)
	retryAfter := now.Add(5 * time.Minute)
	engine := &InspectionEngine{
		policy: InspectionPolicy{AutoDelete: true, RecoveryThreshold: 2},
		records: map[string]inspectionRecord{
			"delete-pending": {
				Result: InspectionResult{
					ID:               "delete-pending",
					Health:           InspectionHealthDeactivated,
					ReasonCode:       "account_deactivated",
					Recommendation:   InspectionRecommendationDelete,
					OwnedDisable:     true,
					Disabled:         true,
					LastCheckedAt:    now,
					DeleteEligibleAt: &eligibleAt,
					AutoAction:       InspectionActionDeleteCandidate,
					AutoActionStatus: InspectionActionFailed,
				},
				DisableReason:    "account_deactivated",
				DisabledAt:       now.Add(-48 * time.Hour),
				DeleteRetryAfter: retryAfter,
			},
		},
	}

	summary := engine.AccountAutomationSummaries([]Account{{ID: "delete-pending", Disabled: true}})["delete-pending"]
	if !summary.AutoDeleteEnabled || summary.DeleteEligibleAt == nil || !summary.DeleteEligibleAt.Equal(eligibleAt) ||
		summary.DeleteRetryAfter == nil || !summary.DeleteRetryAfter.Equal(retryAfter) ||
		summary.AutoAction != InspectionActionDeleteCandidate || summary.AutoActionStatus != InspectionActionFailed {
		t.Fatalf("pending delete summary = %#v", summary)
	}
}

func inspectionEditableHost(disabled bool) *fakeAuthHost {
	raw := json.RawMessage(`{"type":"codex","email":"inspection@example.com","access_token":"account-secret","disabled":false}`)
	if disabled {
		raw = json.RawMessage(`{"type":"codex","email":"inspection@example.com","access_token":"account-secret","disabled":true}`)
	}
	return &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "inspection-account",
			Name:      "inspection.json",
			Provider:  "codex",
			Type:      "codex",
			Status:    "ready",
			Disabled:  disabled,
			Source:    "file",
			Path:      "/auths/inspection.json",
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"inspection-account": {
				AuthIndex: "inspection-account",
				Name:      "inspection.json",
				Path:      "/auths/inspection.json",
				JSON:      raw,
			},
		},
	}
}

func seedDueInspectionDeleteCandidate(engine *InspectionEngine, now time.Time) {
	eligibleAt := now.Add(-time.Hour)
	firstUnhealthy := now.Add(-48 * time.Hour)
	engine.mu.Lock()
	engine.policy = InspectionPolicy{
		ScanIntervalMinutes: 30,
		FailureThreshold:    2,
		RecoveryThreshold:   2,
		AutoDisable:         true,
		AutoDelete:          true,
		DeleteGraceHours:    24,
		DeleteBatchSize:     10,
	}
	engine.records["inspection-account"] = inspectionRecord{
		Result: InspectionResult{
			ID:               "inspection-account",
			Name:             "inspection.json",
			Provider:         "codex",
			Health:           InspectionHealthDeactivated,
			ReasonCode:       "workspace_deactivated",
			Confidence:       InspectionConfidenceHigh,
			Recommendation:   InspectionRecommendationDelete,
			Disabled:         true,
			Editable:         true,
			OwnedDisable:     true,
			FailureStreak:    2,
			FirstUnhealthyAt: &firstUnhealthy,
			DeleteEligibleAt: &eligibleAt,
			AutoAction:       InspectionActionDeleteCandidate,
			AutoActionStatus: InspectionActionPending,
			LastCheckedAt:    now.Add(-time.Hour),
		},
		Signal: inspectionSignal{
			ReasonCode:          "workspace_deactivated",
			Confidence:          InspectionConfidenceHigh,
			AutoDisableEligible: true,
			ConsecutiveFailures: 2,
			LastFailureAt:       now.Add(-2 * time.Hour),
		},
		DisableReason: "workspace_deactivated",
		DisabledAt:    now.Add(-48 * time.Hour),
		DisabledName:  "inspection.json",
		DisabledPath:  "/auths/inspection.json",
	}
	engine.mu.Unlock()
}

package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestInspectionRemediationSummaryMatchesReferenceStyleDistribution(t *testing.T) {
	results := make([]InspectionResult, 0, 188)
	for index := 0; index < 85; index++ {
		results = append(results, InspectionResult{
			ID: fmt.Sprintf("delete-%03d", index), Health: InspectionHealthDeactivated,
			ReasonCode: "workspace_deactivated", Confidence: InspectionConfidenceHigh,
			Recommendation: InspectionRecommendationDelete, Editable: true,
			SignalSource: InspectionSignalActiveProbe, ProbeKind: InspectionProbeKindCredential,
		})
	}
	for index := 0; index < 25; index++ {
		results = append(results, InspectionResult{
			ID: fmt.Sprintf("enable-%03d", index), Health: InspectionHealthHealthy,
			ReasonCode: "model_response_ok", Confidence: InspectionConfidenceHigh,
			Recommendation: InspectionRecommendationEnable, Disabled: true, Editable: true,
		})
	}
	for index := 0; index < 3; index++ {
		results = append(results, InspectionResult{
			ID: fmt.Sprintf("reauth-%03d", index), Health: InspectionHealthInvalidCredentials,
			ReasonCode: "authentication_failed", Confidence: InspectionConfidenceHigh,
			Recommendation: InspectionRecommendationReauth, Disabled: true, Editable: true,
			SignalSource: InspectionSignalActiveProbe, ProbeKind: InspectionProbeKindCredential,
		})
	}
	for index := 0; index < 75; index++ {
		results = append(results, InspectionResult{
			ID: fmt.Sprintf("keep-%03d", index), Health: InspectionHealthHealthy,
			Recommendation: InspectionRecommendationKeep, Editable: true,
		})
	}

	summary := summarizeInspectionRemediation(results)
	if summary.Actionable != 113 || summary.SuggestedDelete != 85 || summary.SuggestedDisable != 0 ||
		summary.SuggestedEnable != 25 || summary.Reauth != 3 || summary.DeletableReauth != 3 ||
		summary.Keep != 75 || summary.Handled != 0 || summary.Review != 0 {
		t.Fatalf("reference-style remediation summary = %#v", summary)
	}
}

func TestInspectionRemediationSummaryDoesNotTreatMatchingStateAsHandled(t *testing.T) {
	results := []InspectionResult{
		{ID: "disabled", Recommendation: InspectionRecommendationDisable, Disabled: true, Editable: true},
		{ID: "enabled", Recommendation: InspectionRecommendationEnable, Editable: true},
		{ID: "readonly-disable", Recommendation: InspectionRecommendationDisable},
		{ID: "keep", Recommendation: InspectionRecommendationKeep, Editable: true},
	}
	summary := summarizeInspectionRemediation(results)
	if summary.Handled != 0 || summary.Review != 1 || summary.Keep != 3 || summary.Actionable != 0 {
		t.Fatalf("state-aware remediation summary = %#v", summary)
	}
}

func TestInspectionRemediationSummaryRequiresSuccessfulMatchingActionForHandled(t *testing.T) {
	summary := summarizeInspectionRemediation([]InspectionResult{{
		ID: "auto-disabled", Recommendation: InspectionRecommendationDisable,
		Disabled: true, Editable: true, OwnedDisable: true,
		AutoAction: InspectionActionDisable, AutoActionStatus: InspectionActionSucceeded,
	}})
	if summary.Handled != 1 || summary.Keep != 0 || summary.Actionable != 0 {
		t.Fatalf("successful matching action summary = %#v", summary)
	}
}

func TestInspectionAuthenticationFailuresHaveExecutableProbeKindSpecificRemediation(t *testing.T) {
	now := time.Date(2026, time.July, 21, 9, 0, 0, 0, time.UTC)
	accounts := []Account{
		{ID: "credential-401", Editable: true},
		{ID: "model-401", Editable: true},
	}
	probeResults := []ModelTestResult{
		{AccountID: "credential-401", Status: "unavailable", ProbeKind: InspectionProbeKindCredential, ReasonCode: "authentication_failed", StatusCode: http.StatusUnauthorized, TestedAt: now},
		{AccountID: "model-401", Status: "unavailable", ProbeKind: InspectionProbeKindModel, ReasonCode: "authentication_failed", StatusCode: http.StatusUnauthorized, TestedAt: now},
	}
	records := make([]inspectionRecord, len(accounts))
	results := make([]InspectionResult, 0, len(accounts))
	for index := range accounts {
		applyModelProbeToInspection(&records[index], probeResults[index], defaultInspectionPolicy())
		decision := decideInspection(accounts[index], records[index], now)
		updateInspectionRecord(&records[index], accounts[index], decision, now)
		results = append(results, records[index].Result)
	}

	summary := summarizeInspectionRemediation(results)
	if summary.Reauth != 1 || summary.DeletableReauth != 1 || summary.SuggestedDisable != 1 ||
		summary.Actionable != 2 || summary.Review != 0 || summary.Keep != 0 || summary.Handled != 0 {
		t.Fatalf("probe-kind remediation summary = %#v; results = %#v", summary, results)
	}
}

func TestInspectionReported194AccountDistributionRemainsCompleteAfterProbeKindFix(t *testing.T) {
	results := make([]InspectionResult, 0, 194)
	appendResults := func(count int, template InspectionResult) {
		for index := 0; index < count; index++ {
			result := template
			result.ID = fmt.Sprintf("%s-%03d", template.ID, index)
			results = append(results, result)
		}
	}
	appendResults(42, InspectionResult{
		ID: "delete", Health: InspectionHealthDeactivated, ReasonCode: "workspace_deactivated",
		Confidence: InspectionConfidenceHigh, Recommendation: InspectionRecommendationDelete,
		Editable: true, SignalSource: InspectionSignalActiveProbe, ProbeKind: InspectionProbeKindCredential,
	})
	appendResults(3, InspectionResult{
		ID: "model-auth", Health: InspectionHealthUnavailable, ReasonCode: "authentication_failed",
		Confidence: InspectionConfidenceMedium, Recommendation: InspectionRecommendationDisable,
		Editable: true, AutoDisableEligible: true, SignalSource: InspectionSignalActiveProbe, ProbeKind: InspectionProbeKindModel,
	})
	appendResults(2, InspectionResult{ID: "enable", Health: InspectionHealthHealthy, Recommendation: InspectionRecommendationEnable, Editable: true, Disabled: true})
	appendResults(5, InspectionResult{
		ID: "reauth", Health: InspectionHealthInvalidCredentials, ReasonCode: "authentication_failed",
		Confidence: InspectionConfidenceHigh, Recommendation: InspectionRecommendationReauth,
		Editable: true, SignalSource: InspectionSignalActiveProbe, ProbeKind: InspectionProbeKindCredential,
	})
	appendResults(4, InspectionResult{ID: "keep", Health: InspectionHealthHealthy, Recommendation: InspectionRecommendationKeep, Editable: true})
	appendResults(138, InspectionResult{ID: "handled", Health: InspectionHealthUnavailable, Recommendation: InspectionRecommendationDisable, Editable: true, Disabled: true})

	summary := summarizeInspectionRemediation(results)
	partitioned := summary.SuggestedDelete + summary.SuggestedDisable + summary.SuggestedEnable +
		summary.Reauth + summary.Review + summary.Keep + summary.Handled
	if len(results) != 194 || partitioned != len(results) || summary.SuggestedDelete != 42 ||
		summary.SuggestedDisable != 3 || summary.SuggestedEnable != 2 || summary.Reauth != 5 ||
		summary.DeletableReauth != 5 || summary.Keep != 142 || summary.Handled != 0 || summary.Review != 0 {
		t.Fatalf("194-account remediation partition = %#v; partitioned=%d", summary, partitioned)
	}
}

func TestCodexInspectionAcceptsObjectBodiesAndUsesCredentialHealthDirectly(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{
			{AuthIndex: "healthy", Name: "healthy.json", Provider: "codex", Type: "oauth", Source: "file", Path: "/auths/healthy.json"},
			{AuthIndex: "deactivated", Name: "deactivated.json", Provider: "codex", Type: "oauth", Source: "file", Path: "/auths/deactivated.json"},
		},
		details: map[string]cpaapi.HostAuthGetResponse{
			"healthy":     {AuthIndex: "healthy", Name: "healthy.json", Path: "/auths/healthy.json", JSON: json.RawMessage(`{"type":"codex","access_token":"secret","account_id":"healthy-workspace"}`)},
			"deactivated": {AuthIndex: "deactivated", Name: "deactivated.json", Path: "/auths/deactivated.json", JSON: json.RawMessage(`{"type":"codex","access_token":"secret","account_id":"deactivated-workspace"}`)},
		},
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		var call managementAPICallRequest
		_ = json.NewDecoder(request.Body).Decode(&call)
		writer.Header().Set("Content-Type", "application/json")
		if call.AuthIndex == "deactivated" {
			_, _ = writer.Write([]byte(`{"status_code":402,"body":{"detail":{"code":"deactivated_workspace"}}}`))
			return
		}
		_, _ = writer.Write([]byte(`{"status_code":200,"body":{"rate_limit":{"allowed":true,"primary_window":{"used_percent":12,"limit_window_seconds":18000,"reset_after_seconds":3600},"secondary_window":{"used_percent":30,"limit_window_seconds":604800,"reset_after_seconds":7200}}}}`))
	}))
	defer server.Close()

	usage := NewUsageTracker()
	defer usage.Close()
	service := NewModelTestService(NewAccountService(host), usage)
	service.doer = server.Client()
	healthy, errHealthy := service.Run(context.Background(), ModelTestRequest{AccountID: "healthy", Inspection: true}, server.URL, "management-secret")
	deactivated, errDeactivated := service.Run(context.Background(), ModelTestRequest{AccountID: "deactivated", Inspection: true}, server.URL, "management-secret")
	if errHealthy != nil || healthy.Status != "available" || healthy.ProbeKind != InspectionProbeKindCredential || healthy.ReasonCode != "credential_response_ok" {
		t.Fatalf("healthy credential result=%#v error=%v", healthy, errHealthy)
	}
	if errDeactivated != nil || deactivated.StatusCode != http.StatusPaymentRequired || deactivated.ProbeKind != InspectionProbeKindCredential || deactivated.ReasonCode != "workspace_deactivated" {
		t.Fatalf("deactivated credential result=%#v error=%v", deactivated, errDeactivated)
	}
	if calls.Load() != 2 {
		t.Fatalf("inspection made %d calls, want one credential call per account", calls.Load())
	}
	snapshot := usage.Snapshot("healthy")
	if snapshot == nil || snapshot.Codex == nil || snapshot.Codex.FiveHour == nil || snapshot.Codex.SevenDay == nil ||
		snapshot.Codex.FiveHour.UsedPercent != 12 || snapshot.Codex.SevenDay.UsedPercent != 30 {
		t.Fatalf("credential usage snapshot = %#v", snapshot)
	}
}

func TestInspectionReference151DistributionIsActionable(t *testing.T) {
	now := time.Date(2026, time.July, 21, 18, 0, 0, 0, time.UTC)
	results := make([]InspectionResult, 0, 151)
	appendProbeResults := func(count int, prefix string, account Account, probe inspectionProbeSignal) {
		for index := 0; index < count; index++ {
			nextAccount := account
			nextAccount.ID = fmt.Sprintf("%s-%03d", prefix, index)
			nextAccount.Name = nextAccount.ID + ".json"
			record := inspectionRecord{}
			applyModelProbeToInspection(&record, ModelTestResult{
				AccountID: nextAccount.ID, Status: probe.Status, ProbeKind: probe.Kind,
				ReasonCode: probe.ReasonCode, StatusCode: probe.StatusCode, QuotaWindow: probe.QuotaWindow,
				TestedAt: probe.TestedAt,
			}, defaultInspectionPolicy())
			decision := decideInspection(nextAccount, record, now)
			updateInspectionRecord(&record, nextAccount, decision, now)
			results = append(results, record.Result)
		}
	}
	appendProbeResults(43, "delete", Account{Provider: "codex", Disabled: true, Editable: true}, inspectionProbeSignal{
		Status: "unavailable", Kind: InspectionProbeKindCredential, ReasonCode: "workspace_deactivated",
		StatusCode: http.StatusPaymentRequired, TestedAt: now, ConsecutiveFailures: 1,
	})
	appendProbeResults(24, "enable", Account{Provider: "codex", Disabled: true, Editable: true}, inspectionProbeSignal{
		Status: "available", Kind: InspectionProbeKindCredential, ReasonCode: "credential_response_ok",
		StatusCode: http.StatusOK, TestedAt: now, ConsecutiveSuccess: 1,
	})
	appendProbeResults(1, "reauth", Account{Provider: "codex", Disabled: true, Editable: true}, inspectionProbeSignal{
		Status: "unavailable", Kind: InspectionProbeKindCredential, ReasonCode: "authentication_failed",
		StatusCode: http.StatusUnauthorized, TestedAt: now, ConsecutiveFailures: 1,
	})
	appendProbeResults(48, "quota", Account{Provider: "codex", Disabled: true, Editable: true}, inspectionProbeSignal{
		Status: "review", Kind: InspectionProbeKindCredential, ReasonCode: "quota_limited",
		StatusCode: http.StatusPaymentRequired, TestedAt: now, ConsecutiveFailures: 1, QuotaWindow: InspectionQuotaWindowSevenDay,
	})
	appendProbeResults(7, "healthy", Account{Provider: "codex", Editable: true}, inspectionProbeSignal{
		Status: "available", Kind: InspectionProbeKindCredential, ReasonCode: "credential_response_ok",
		StatusCode: http.StatusOK, TestedAt: now, ConsecutiveSuccess: 1,
	})
	appendProbeResults(28, "unavailable", Account{Provider: "codex", Disabled: true, Editable: true}, inspectionProbeSignal{
		Status: "review", Kind: InspectionProbeKindCredential, ReasonCode: "upstream_unavailable",
		StatusCode: http.StatusBadGateway, TestedAt: now, ConsecutiveFailures: 1,
	})

	summary := summarizeInspectionRemediation(results)
	if len(results) != 151 || summary.Actionable != 68 || summary.SuggestedDelete != 43 ||
		summary.SuggestedDisable != 0 || summary.SuggestedEnable != 24 || summary.Reauth != 1 ||
		summary.DeletableReauth != 1 || summary.Keep != 83 || summary.Handled != 0 || summary.Review != 0 {
		t.Fatalf("151-account remediation summary = %#v", summary)
	}
}

func TestInspectionReference141DistributionSurvivesCompatibleCPAResponseShapes(t *testing.T) {
	now := time.Date(2026, time.July, 22, 8, 0, 0, 0, time.UTC)
	results := make([]InspectionResult, 0, 141)
	appendResponses := func(count int, prefix string, disabled bool, responseJSON string) {
		for index := 0; index < count; index++ {
			var response managementAPICallResponse
			if errDecode := json.Unmarshal([]byte(responseJSON), &response); errDecode != nil {
				t.Fatalf("decode %s response: %v", prefix, errDecode)
			}
			status, reason, quotaWindow := classifyCredentialProbeDetails(response.StatusCode, []byte(response.Body))
			account := Account{
				ID: fmt.Sprintf("%s-%03d", prefix, index), Provider: "codex",
				Disabled: disabled, Editable: true,
			}
			record := inspectionRecord{}
			applyModelProbeToInspection(&record, ModelTestResult{
				AccountID: account.ID, Status: status, ProbeKind: InspectionProbeKindCredential,
				ReasonCode: reason, StatusCode: response.StatusCode, QuotaWindow: quotaWindow, TestedAt: now,
			}, defaultInspectionPolicy())
			updateInspectionRecord(&record, account, decideInspection(account, record, now), now)
			results = append(results, record.Result)
		}
	}

	appendResponses(43, "delete", true, `{"statusCode":"402","body":{"detail":{"code":"deactivated_workspace"}}}`)
	appendResponses(33, "enable", true, `{"status_code":200,"body":{"rate_limit":{"allowed":true,"primary_window":{"used_percent":12,"limit_window_seconds":18000},"secondary_window":{"used_percent":30,"limit_window_seconds":604800}}}}`)
	appendResponses(7, "reauth", true, `{"status_code":"401","body":{"error":{"code":"invalid_token"}}}`)
	appendResponses(28, "quota", true, `{"statusCode":200,"body":{"rateLimit":{"allowed":false,"secondaryWindow":{"usedPercent":100,"limitWindowSeconds":604800}}}}`)
	appendResponses(30, "healthy", false, `{"statusCode":"200","body":{"rateLimit":{"allowed":true,"secondaryWindow":{"usedPercent":40,"limitWindowSeconds":604800}}}}`)

	summary := summarizeInspectionRemediation(results)
	if len(results) != 141 || summary.Actionable != 83 || summary.SuggestedDelete != 43 ||
		summary.SuggestedDisable != 0 || summary.SuggestedEnable != 33 || summary.Reauth != 7 ||
		summary.DeletableReauth != 7 || summary.Keep != 58 || summary.Handled != 0 || summary.Review != 0 {
		t.Fatalf("141-account remediation summary = %#v", summary)
	}
}

func TestInspectionCredentialSuccessQualifiesAsAutomaticRecoveryEvidence(t *testing.T) {
	disabledAt := time.Date(2026, time.July, 22, 7, 0, 0, 0, time.UTC)
	record := inspectionRecord{
		DisabledAt: disabledAt,
		Probe: inspectionProbeSignal{
			ReasonCode: "credential_response_ok",
			TestedAt:   disabledAt.Add(time.Minute),
		},
	}
	if !inspectionRecoveryEvidenceAfter(record, disabledAt) {
		t.Fatal("a healthy credential probe after disable was not accepted as recovery evidence")
	}
	record.Probe.TestedAt = disabledAt
	if inspectionRecoveryEvidenceAfter(record, disabledAt) {
		t.Fatal("a credential probe at or before disable was accepted as recovery evidence")
	}
}

func TestScopedInspectionAcceptsNewImportedAccountWithoutPreviousRecords(t *testing.T) {
	engine := NewInspectionEngine(NewAccountService(&fakeAuthHost{}), &fakeAuthHost{}, NewMutationCoordinator())
	engine.SetModelTestService(NewModelTestService(engine.accounts))
	engine.mu.Lock()
	engine.started = true
	engine.mu.Unlock()
	snapshot, errRun := engine.RequestRun(InspectionRunRequest{
		Mode: InspectionRunModeScoped, Selected: []string{"new-imported-account"},
	}, "management-secret")
	if errRun != nil || !snapshot.Pending || snapshot.ProbeSweepTotal != 1 || snapshot.ProbeSweepRemaining != 1 {
		t.Fatalf("new imported scoped run = %#v error=%v", snapshot, errRun)
	}
}

func TestCodexCredentialQuotaWindowsPreserveFiveHourAndLongWindowActions(t *testing.T) {
	tests := []struct {
		name               string
		body               string
		disabled           bool
		wantWindow         string
		wantRecommendation string
		wantHealth         string
	}{
		{name: "healthy disabled account enables", body: `{"rate_limit":{"allowed":true,"primary_window":{"used_percent":12,"limit_window_seconds":18000},"secondary_window":{"used_percent":30,"limit_window_seconds":604800}}}`, disabled: true, wantRecommendation: InspectionRecommendationEnable, wantHealth: InspectionHealthHealthy},
		{name: "five hour exhaustion keeps enabled account", body: `{"rate_limit":{"allowed":true,"primary_window":{"used_percent":100,"limit_window_seconds":18000},"secondary_window":{"used_percent":30,"limit_window_seconds":604800}}}`, wantWindow: InspectionQuotaWindowFiveHour, wantRecommendation: InspectionRecommendationKeep, wantHealth: InspectionHealthQuotaLimited},
		{name: "aggregate denial does not override healthy long window", body: `{"rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100,"limit_window_seconds":18000},"secondary_window":{"used_percent":30,"limit_window_seconds":604800}}}`, wantWindow: InspectionQuotaWindowFiveHour, wantRecommendation: InspectionRecommendationKeep, wantHealth: InspectionHealthQuotaLimited},
		{name: "long exhaustion disables enabled account", body: `{"rate_limit":{"allowed":true,"primary_window":{"used_percent":10,"limit_window_seconds":18000},"secondary_window":{"used_percent":100,"limit_window_seconds":604800}}}`, wantWindow: InspectionQuotaWindowSevenDay, wantRecommendation: InspectionRecommendationDisable, wantHealth: InspectionHealthQuotaLimited},
		{name: "long exhaustion keeps disabled account", body: `{"rate_limit":{"allowed":true,"primary_window":{"used_percent":10,"limit_window_seconds":18000},"secondary_window":{"used_percent":100,"limit_window_seconds":604800}}}`, disabled: true, wantWindow: InspectionQuotaWindowSevenDay, wantRecommendation: InspectionRecommendationKeep, wantHealth: InspectionHealthQuotaLimited},
	}
	now := time.Date(2026, time.July, 21, 18, 0, 0, 0, time.UTC)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, reason, quotaWindow := classifyCredentialProbeDetails(http.StatusOK, []byte(test.body))
			record := inspectionRecord{}
			applyModelProbeToInspection(&record, ModelTestResult{
				AccountID: "quota-window", Status: status, ProbeKind: InspectionProbeKindCredential,
				ReasonCode: reason, StatusCode: http.StatusOK, QuotaWindow: quotaWindow, TestedAt: now,
			}, defaultInspectionPolicy())
			account := Account{ID: "quota-window", Provider: "codex", Editable: true, Disabled: test.disabled}
			updateInspectionRecord(&record, account, decideInspection(account, record, now), now)
			if record.Result.Health != test.wantHealth || record.Result.QuotaWindow != test.wantWindow || record.Result.Recommendation != test.wantRecommendation {
				t.Fatalf("window result=%#v", record.Result)
			}
		})
	}
}

func TestManualFullInspectionIncludesManuallyDisabledAccounts(t *testing.T) {
	if !inspectionRunScansManuallyDisabled(InspectionRunModeFull, InspectionSweepSourceManual, false) {
		t.Fatal("manual full inspection excluded manually disabled accounts")
	}
	if inspectionRunScansManuallyDisabled(InspectionRunModeIncremental, InspectionSweepSourceManual, false) {
		t.Fatal("manual incremental inspection ignored the disabled-account policy")
	}
	if !inspectionRunScansManuallyDisabled(InspectionRunModeFull, InspectionSweepSourceScheduled, true) {
		t.Fatal("scheduled full inspection ignored the enabled disabled-account policy")
	}
}

func TestInspectionRunSummaryAccumulatesActionsAcrossProbeBatches(t *testing.T) {
	startedAt := time.Date(2026, time.July, 21, 8, 0, 0, 0, time.UTC)
	previous := InspectionRunSummary{StartedAt: startedAt, Scanned: 188, Healthy: 2, AutoDisabled: 95, AutoEnabled: 1, Failed: 2}
	current := InspectionRunSummary{StartedAt: startedAt.Add(time.Minute), FinishedAt: startedAt.Add(2 * time.Minute), Scanned: 188, Healthy: 3, QuotaLimited: 30, AutoDisabled: 4, AutoEnabled: 2, Failed: 1}
	merged := mergeInspectionRunSummary(previous, current)
	if merged.StartedAt != startedAt || merged.Scanned != 188 || merged.Healthy != 3 || merged.QuotaLimited != 30 ||
		merged.AutoDisabled != 99 || merged.AutoEnabled != 3 || merged.Failed != 3 || merged.FinishedAt != current.FinishedAt {
		t.Fatalf("merged run summary = %#v", merged)
	}
}

func TestCodexCredentialProbeClassifiesReauthDeactivationAndQuota(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		status     string
		reason     string
	}{
		{name: "expired credential", statusCode: http.StatusUnauthorized, body: `{"message":"token expired"}`, status: "unavailable", reason: "authentication_failed"},
		{name: "deactivated account", statusCode: http.StatusUnauthorized, body: `{"error":{"code":"account_deactivated"}}`, status: "unavailable", reason: "account_deactivated"},
		{name: "deactivated workspace", statusCode: http.StatusPaymentRequired, body: `{"detail":{"code":"deactivated_workspace"}}`, status: "unavailable", reason: "workspace_deactivated"},
		{name: "payment quota", statusCode: http.StatusPaymentRequired, body: `{"detail":"quota exhausted"}`, status: "review", reason: "quota_limited"},
		{name: "successful response with reached limit", statusCode: http.StatusOK, body: `{"rate_limit":{"allowed":false,"limit_reached":true}}`, status: "review", reason: "quota_limited"},
		{name: "successful response with exhausted primary window", statusCode: http.StatusOK, body: `{"rateLimit":{"primaryWindow":{"usedPercent":100}}}`, status: "review", reason: "quota_limited"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, reason := classifyCredentialProbe(test.statusCode, []byte(test.body))
			if status != test.status || reason != test.reason {
				t.Fatalf("credential classification = %q/%q, want %q/%q", status, reason, test.status, test.reason)
			}
		})
	}
}

func TestCodexCredentialQuotaStopsBeforeModelProbe(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "credential-quota", Name: "credential-quota.json", Provider: "codex", Type: "oauth",
			Source: "file", Path: "/auths/credential-quota.json",
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"credential-quota": {
				AuthIndex: "credential-quota", Name: "credential-quota.json", Path: "/auths/credential-quota.json",
				JSON: json.RawMessage(`{"type":"codex","access_token":"upstream-secret","account_id":"workspace-quota"}`),
			},
		},
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(writer).Encode(managementAPICallResponse{
			StatusCode: http.StatusOK,
			Body:       `{"rate_limit":{"allowed":false,"primary_window":{"used_percent":100}}}`,
		})
	}))
	defer server.Close()

	service := NewModelTestService(NewAccountService(host))
	service.doer = server.Client()
	result, errRun := service.Run(context.Background(), ModelTestRequest{AccountID: "credential-quota", Model: "gpt-5.4"}, server.URL, "management-secret")
	if errRun != nil {
		t.Fatalf("credential quota inspection: %v", errRun)
	}
	if calls.Load() != 1 || result.ProbeKind != InspectionProbeKindCredential ||
		result.Status != "review" || result.ReasonCode != "quota_limited" {
		t.Fatalf("credential quota result=%#v calls=%d", result, calls.Load())
	}
}

func TestCodexCredentialAndModelProbeShareOneDeadline(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "credential-timeout", Name: "credential-timeout.json", Provider: "codex", Type: "oauth",
			Source: "file", Path: "/auths/credential-timeout.json",
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"credential-timeout": {
				AuthIndex: "credential-timeout", Name: "credential-timeout.json", Path: "/auths/credential-timeout.json",
				JSON: json.RawMessage(`{"type":"codex","access_token":"upstream-secret","account_id":"workspace-timeout"}`),
			},
		},
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		select {
		case <-request.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}))
	defer server.Close()

	service := NewModelTestService(NewAccountService(host))
	service.doer = server.Client()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	result, errRun := service.Run(ctx, ModelTestRequest{AccountID: "credential-timeout", Model: "gpt-5.4"}, server.URL, "management-secret")
	if errRun != nil {
		t.Fatalf("credential timeout inspection: %v", errRun)
	}
	if calls.Load() != 1 || result.ProbeKind != InspectionProbeKindCredential || result.ReasonCode != "request_timeout" {
		t.Fatalf("credential timeout result=%#v calls=%d", result, calls.Load())
	}
	if inspectionManualDeleteAllowed(InspectionResult{
		Editable: true, Health: InspectionHealthUnavailable, Confidence: InspectionConfidenceHigh,
		Recommendation: InspectionRecommendationDisable, ReasonCode: result.ReasonCode,
		SignalSource: InspectionSignalActiveProbe, ProbeKind: result.ProbeKind,
	}) {
		t.Fatal("credential timeout became deletion evidence")
	}
}

func TestCredentialProbeReauthCanBeConfirmedForDeletionButModelFailureCannot(t *testing.T) {
	base := InspectionResult{
		Editable: true, Health: InspectionHealthInvalidCredentials, ReasonCode: "authentication_failed",
		Confidence: InspectionConfidenceHigh, Recommendation: InspectionRecommendationReauth,
		SignalSource: InspectionSignalActiveProbe,
	}
	credential := base
	credential.ProbeKind = InspectionProbeKindCredential
	if !inspectionManualDeleteAllowed(credential) {
		t.Fatal("confirmed credential-endpoint 401 was not eligible for manual deletion")
	}
	model := base
	model.ProbeKind = InspectionProbeKindModel
	if inspectionManualDeleteAllowed(model) {
		t.Fatal("generic model failure became deletion evidence")
	}
}

func TestModelTestServiceUsesDefinitiveCodexCredentialProbeWithSanitizedManualResponse(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "credential-401", Name: "credential-401.json", Provider: "codex", Type: "oauth",
			Source: "file", Path: "/auths/credential-401.json",
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"credential-401": {
				AuthIndex: "credential-401", Name: "credential-401.json", Path: "/auths/credential-401.json",
				JSON: json.RawMessage(`{"type":"codex","access_token":"upstream-secret","account_id":"workspace-401"}`),
			},
		},
	}
	credentialCalls := 0
	modelCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var call managementAPICallRequest
		_ = json.NewDecoder(request.Body).Decode(&call)
		if call.Method == http.MethodGet && call.URL == "https://chatgpt.com/backend-api/wham/usage" {
			credentialCalls++
			_ = json.NewEncoder(writer).Encode(managementAPICallResponse{
				StatusCode: http.StatusUnauthorized,
				Body:       `{"message":"private credential failure"}`,
			})
			return
		}
		modelCalls++
		_ = json.NewEncoder(writer).Encode(managementAPICallResponse{StatusCode: http.StatusOK, Body: `{"ok":true}`})
	}))
	defer server.Close()

	service := NewModelTestService(NewAccountService(host))
	service.doer = server.Client()
	result, errRun := service.Run(context.Background(), ModelTestRequest{AccountID: "credential-401", Model: "gpt-5.4"}, server.URL, "management-secret")
	if errRun != nil {
		t.Fatalf("credential inspection: %v", errRun)
	}
	if credentialCalls != 1 || modelCalls != 0 || result.ProbeKind != InspectionProbeKindCredential ||
		result.StatusCode != http.StatusUnauthorized || result.ReasonCode != "authentication_failed" {
		t.Fatalf("credential result=%#v credential_calls=%d model_calls=%d", result, credentialCalls, modelCalls)
	}
	encoded, _ := json.Marshal(result)
	if result.Response == nil || !strings.Contains(result.Response.Body, "private credential failure") {
		t.Fatalf("credential response preview = %#v", result.Response)
	}
	for _, secret := range []string{"upstream-secret", "management-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("credential result leaked %q: %s", secret, encoded)
		}
	}
}

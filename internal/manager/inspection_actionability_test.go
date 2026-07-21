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

func TestInspectionRemediationSummarySeparatesHandledAndBlockedFromKeep(t *testing.T) {
	results := []InspectionResult{
		{ID: "disabled", Recommendation: InspectionRecommendationDisable, Disabled: true, Editable: true},
		{ID: "enabled", Recommendation: InspectionRecommendationEnable, Editable: true},
		{ID: "readonly-disable", Recommendation: InspectionRecommendationDisable},
		{ID: "keep", Recommendation: InspectionRecommendationKeep, Editable: true},
	}
	summary := summarizeInspectionRemediation(results)
	if summary.Handled != 2 || summary.Review != 1 || summary.Keep != 1 || summary.Actionable != 0 {
		t.Fatalf("state-aware remediation summary = %#v", summary)
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
		summary.DeletableReauth != 5 || summary.Keep != 4 || summary.Handled != 138 || summary.Review != 0 {
		t.Fatalf("194-account remediation partition = %#v; partitioned=%d", summary, partitioned)
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

func TestModelTestServiceUsesDefinitiveCodexCredentialProbeWithoutLeakingResponse(t *testing.T) {
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
	for _, secret := range []string{"private credential failure", "upstream-secret", "management-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("credential result leaked %q: %s", secret, encoded)
		}
	}
}

package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestWeeklyOverdraftExperimentInjectsOneBoundedToolPair(t *testing.T) {
	enabled := true
	experiment := NewWeeklyOverdraftExperiment(func() bool { return enabled })
	experiment.newCallID = func() (string, bool) { return "call_cpa_overdraft_test", true }
	original := []byte(`{"model":"gpt-5.4","store":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`)
	response, changed := experiment.InterceptRequest(cpaapi.RequestInterceptRequest{ToFormat: "codex", Body: original})
	if !changed || len(response.Body) == 0 {
		t.Fatal("eligible Codex request was not transformed")
	}
	if string(original) != `{"model":"gpt-5.4","store":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}` {
		t.Fatal("interceptor mutated the caller-owned request body")
	}
	var document struct {
		Model string            `json:"model"`
		Store bool              `json:"store"`
		Input []json.RawMessage `json:"input"`
	}
	if errDecode := json.Unmarshal(response.Body, &document); errDecode != nil {
		t.Fatalf("decode transformed body: %v", errDecode)
	}
	if document.Model != "gpt-5.4" || document.Store || len(document.Input) != 3 {
		t.Fatalf("transformed document = %#v", document)
	}
	var call struct {
		Type   string `json:"type"`
		Name   string `json:"name"`
		CallID string `json:"call_id"`
		Input  string `json:"input"`
	}
	var output struct {
		Type   string              `json:"type"`
		CallID string              `json:"call_id"`
		Output []map[string]string `json:"output"`
	}
	if errCall := json.Unmarshal(document.Input[1], &call); errCall != nil {
		t.Fatalf("decode injected call: %v", errCall)
	}
	if errOutput := json.Unmarshal(document.Input[2], &output); errOutput != nil {
		t.Fatalf("decode injected output: %v", errOutput)
	}
	if call.Type != "custom_tool_call" || call.Name != "exec" || call.CallID != "call_cpa_overdraft_test" || call.Input != experimentalExecInput {
		t.Fatalf("injected call = %#v", call)
	}
	if output.Type != "custom_tool_call_output" || output.CallID != call.CallID || len(output.Output) != 1 ||
		output.Output[0]["type"] != "input_text" || output.Output[0]["text"] == "" {
		t.Fatalf("injected output = %#v", output)
	}

	enabled = false
	if disabledResponse, disabledChanged := experiment.InterceptRequest(cpaapi.RequestInterceptRequest{ToFormat: "codex", Body: original}); disabledChanged || len(disabledResponse.Body) != 0 {
		t.Fatal("disabled experiment transformed a request")
	}
}

func TestReplaceTopLevelJSONFieldValuePreservesUnknownFieldsAndNestedInput(t *testing.T) {
	original := []byte(`{"note":"the string contains \"input\": false","meta":{"input":[1]},"input": [ {"type":"message","role":"user"} ],"tail":{"keep":true}}`)
	replacement := []byte(`[{"type":"message","role":"user"},{"type":"custom_tool_call"}]`)
	updated, replaced := replaceTopLevelJSONFieldValue(original, "input", []byte(` [ {"type":"message","role":"user"} ]`), replacement)
	if !replaced {
		t.Fatal("top-level input field was not replaced")
	}
	var document struct {
		Note  string            `json:"note"`
		Meta  map[string][]int  `json:"meta"`
		Input []json.RawMessage `json:"input"`
		Tail  map[string]bool   `json:"tail"`
	}
	if errDecode := json.Unmarshal(updated, &document); errDecode != nil {
		t.Fatalf("decode replaced document: %v", errDecode)
	}
	if len(document.Input) != 2 || document.Meta["input"][0] != 1 || !document.Tail["keep"] || document.Note == "" {
		t.Fatalf("replacement lost surrounding fields: %#v", document)
	}
}

func TestWeeklyOverdraftExperimentFailsOpenForUnsupportedRequests(t *testing.T) {
	valid := []byte(`{"input":[{"type":"message","role":"user","content":"continue"}]}`)
	tests := []struct {
		name    string
		format  string
		body    []byte
		prepare func(*WeeklyOverdraftExperiment)
	}{
		{name: "non codex format", format: "openai", body: valid},
		{name: "invalid json", format: "codex", body: []byte(`{"input":`)},
		{name: "missing input", format: "codex", body: []byte(`{"model":"gpt-5.4"}`)},
		{name: "assistant is last", format: "codex", body: []byte(`{"input":[{"type":"message","role":"assistant","content":"done"}]}`)},
		{name: "already injected", format: "codex", body: []byte(`{"input":[{"type":"message","role":"user"},{"type":"custom_tool_call_output","call_id":"existing"}]}`)},
		{name: "oversized", format: "codex", body: valid, prepare: func(experiment *WeeklyOverdraftExperiment) { experiment.maxBodyBytes = 8 }},
		{name: "call id unavailable", format: "codex", body: valid, prepare: func(experiment *WeeklyOverdraftExperiment) {
			experiment.newCallID = func() (string, bool) { return "", false }
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			experiment := NewWeeklyOverdraftExperiment(func() bool { return true })
			if test.prepare != nil {
				test.prepare(experiment)
			}
			response, changed := experiment.InterceptRequest(cpaapi.RequestInterceptRequest{ToFormat: test.format, Body: test.body})
			if changed || len(response.Body) != 0 || len(response.Headers) != 0 || len(response.ClearHeaders) != 0 {
				t.Fatalf("unsupported request changed: %#v", response)
			}
		})
	}
}

func TestWeeklyOverdraftExperimentProtectsEveryCodexQuotaWindowWithoutWeakeningOtherRemediation(t *testing.T) {
	now := time.Date(2026, time.July, 22, 10, 0, 0, 0, time.UTC)
	experiment := NewWeeklyOverdraftExperiment(func() bool { return true })
	weeklyUsage := cpaapi.UsageRecord{ResponseHeaders: codexUsageObservationHeaders(now, 20, 100)}
	if !experiment.AllowUsageAutoDisable(weeklyUsage, now) {
		t.Fatal("weekly exhaustion did not wake the verification scan")
	}
	if !experiment.AllowUsageAutoDisable(cpaapi.UsageRecord{ResponseHeaders: codexUsageObservationHeaders(now, 100, 20)}, now) {
		t.Fatal("five-hour exhaustion was incorrectly treated as weekly overdraft")
	}
	if !experiment.AllowUsageAutoDisable(cpaapi.UsageRecord{ResponseHeaders: codexUsageObservationHeaders(now, 100, 100)}, now) {
		t.Fatal("weekly exhaustion suppressed an actionable five-hour exhaustion")
	}
	for _, quotaWindow := range []string{
		InspectionQuotaWindowFiveHour,
		InspectionQuotaWindowFiveHourFallback,
		InspectionQuotaWindowSevenDay,
		InspectionQuotaWindowMultiple,
	} {
		result := InspectionResult{ReasonCode: "quota_exhausted", QuotaWindow: quotaWindow}
		if experiment.AllowInspectionAutoDisable(result) {
			t.Fatalf("quota window %q was allowed to auto-disable before probing", quotaWindow)
		}
		plan, planned := experiment.AutomaticDisableProbePlan(Account{Provider: "codex"}, result, defaultOpenAIProbeModel)
		if !planned || plan.AttemptLimit != weeklyOverdraftProbeAttempts || !plan.Request.ExperimentalWeeklyOverdraft {
			t.Fatalf("quota window %q plan = %#v, planned=%t", quotaWindow, plan, planned)
		}
		result.AutoDisableProbeStatus = InspectionAutoDisableProbeFailed
		result.AutoDisableProbeAttempts = weeklyOverdraftProbeAttempts
		if !experiment.AllowInspectionAutoDisable(result) {
			t.Fatalf("quota window %q remained blocked after all probes failed", quotaWindow)
		}
	}
	for _, result := range []InspectionResult{
		{ReasonCode: "invalid_credentials", QuotaWindow: InspectionQuotaWindowSevenDay},
		{ReasonCode: "quota_exhausted", QuotaWindow: ""},
		{ReasonCode: "account_deactivated"},
	} {
		if !experiment.AllowInspectionAutoDisable(result) {
			t.Fatalf("unrelated remediation was suppressed: %#v", result)
		}
	}

	engine := NewInspectionEngine(nil, nil, nil)
	engine.started = true
	engine.now = func() time.Time { return now }
	engine.policy = defaultInspectionPolicy()
	engine.policy.Enabled = true
	engine.policy.AutoDisable = true
	engine.RegisterAutomaticDisableGuard(experiment)
	engine.Observe(weeklyUsageWithAuth(weeklyUsage, "weekly"))
	if !engine.pending || len(engine.scanWake) != 1 {
		t.Fatalf("weekly experiment did not queue verification: pending=%t wake=%d", engine.pending, len(engine.scanWake))
	}
}

func TestWeeklyOverdraftExperimentVetoesAutomaticQuotaMutation(t *testing.T) {
	host := inspectionEditableHost(false)
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	engine.RegisterAutomaticDisableGuard(NewWeeklyOverdraftExperiment(func() bool { return true }))
	records := map[string]inspectionRecord{
		"inspection-account": {Result: InspectionResult{
			ID: "inspection-account", Name: "inspection.json", Provider: "codex", Health: InspectionHealthQuotaLimited,
			ReasonCode: "quota_exhausted", QuotaWindow: InspectionQuotaWindowSevenDay, Confidence: InspectionConfidenceHigh,
			Recommendation: InspectionRecommendationDisable, Editable: true, AutoDisableEligible: true, SignalSource: InspectionSignalNative,
		}},
	}
	accounts := map[string]Account{
		"inspection-account": {ID: "inspection-account", Name: "inspection.json", Provider: "codex", Editable: true, path: "/auths/inspection.json"},
	}
	policy := defaultInspectionPolicy()
	policy.AutoDisable = true
	summary, actions := engine.applyAutomaticActions(context.Background(), policy, accounts, records, time.Now().UTC(), "", "")
	if summary.AutoDisabled != 0 || summary.Failed != 0 || len(actions) != 0 || len(host.saves) != 0 {
		t.Fatalf("weekly quota guard result summary=%#v actions=%#v saves=%d", summary, actions, len(host.saves))
	}
}

func TestWeeklyOverdraftExperimentKeepsAccountEnabledWhenAnyGateProbeSucceeds(t *testing.T) {
	now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	resetAt := now.Add(6 * 24 * time.Hour)
	host := inspectionEditableHost(false)
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	engine.RegisterAutomaticDisableGuard(NewWeeklyOverdraftExperiment(func() bool { return true }))
	cycleTracker := &recordingOverdraftCycleStopper{}
	engine.SetOverdraftCycleTracker(cycleTracker)
	probeCalls := 0
	engine.automaticDisableProbe = func(_ context.Context, request ModelTestRequest, _, _ string) (ModelTestResult, error) {
		probeCalls++
		status := "review"
		reason := "quota_limited"
		statusCode := http.StatusTooManyRequests
		if probeCalls == 3 {
			status = "available"
			reason = "model_response_ok"
			statusCode = http.StatusOK
		}
		return ModelTestResult{
			AccountID: request.AccountID, Model: request.Model, Status: status, ReasonCode: reason,
			StatusCode: statusCode, TestedAt: now.Add(time.Duration(probeCalls) * time.Second),
			Experiment: &ModelTestExperiment{Name: "weekly_overdraft", Applied: true, CallID: "call_gate"},
		}, nil
	}
	records := weeklyOverdraftActionRecords(resetAt)
	accounts := weeklyOverdraftActionAccounts()
	policy := defaultInspectionPolicy()
	policy.AutoDisable = true

	summary, actions := engine.applyAutomaticActions(context.Background(), policy, accounts, records, now, "", "")
	record := records["inspection-account"]
	if probeCalls != 3 || summary.AutoDisabled != 0 || summary.Failed != 0 || len(actions) != 0 || len(host.saves) != 0 {
		t.Fatalf("successful gate summary=%#v actions=%#v calls=%d saves=%d", summary, actions, probeCalls, len(host.saves))
	}
	if record.Result.AutoDisableProbeStatus != InspectionAutoDisableProbePassed || record.Result.AutoDisableProbeAttempts != 3 ||
		record.Result.AutoDisableProbeLimit != weeklyOverdraftProbeAttempts || record.Result.AutoDisableProbeReasonCode != "model_response_ok" ||
		record.Result.AutoDisableProbeTestedAt == nil || len(cycleTracker.started) != 1 || cycleTracker.started[0] != "inspection-account" {
		t.Fatalf("successful gate state = %#v", record.Result)
	}
}

func TestWeeklyOverdraftExperimentDisablesAfterFiveFailedGateProbes(t *testing.T) {
	now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	resetAt := now.Add(6 * 24 * time.Hour)
	host := inspectionEditableHost(false)
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	engine.RegisterAutomaticDisableGuard(NewWeeklyOverdraftExperiment(func() bool { return true }))
	probeCalls := 0
	engine.automaticDisableProbe = func(_ context.Context, request ModelTestRequest, _, _ string) (ModelTestResult, error) {
		probeCalls++
		return ModelTestResult{
			AccountID: request.AccountID, Model: request.Model, Status: "review", ReasonCode: "quota_limited",
			StatusCode: http.StatusTooManyRequests, TestedAt: now.Add(time.Duration(probeCalls) * time.Second),
			Experiment: &ModelTestExperiment{Name: "weekly_overdraft", Applied: true, CallID: "call_gate"},
		}, nil
	}
	records := weeklyOverdraftActionRecords(resetAt)
	accounts := weeklyOverdraftActionAccounts()
	policy := defaultInspectionPolicy()
	policy.AutoDisable = true
	policy.AutoEnable = true

	summary, actions := engine.applyAutomaticActions(context.Background(), policy, accounts, records, now, "", "")
	record := records["inspection-account"]
	if probeCalls != weeklyOverdraftProbeAttempts || summary.AutoDisabled != 1 || summary.Failed != 0 || len(actions) != 1 || len(host.saves) != 1 {
		t.Fatalf("failed gate summary=%#v actions=%#v calls=%d saves=%d", summary, actions, probeCalls, len(host.saves))
	}
	if !record.Result.Disabled || !record.Result.OwnedDisable || record.Result.AutoDisableProbeStatus != InspectionAutoDisableProbeFailed ||
		record.Result.AutoDisableProbeAttempts != weeklyOverdraftProbeAttempts || record.Result.AutoDisableProbeReasonCode != "quota_limited" ||
		record.DisabledRecoverAfter.IsZero() || !record.DisabledRecoverAfter.Equal(resetAt) {
		t.Fatalf("failed gate state = %#v", record)
	}

	account := accounts["inspection-account"]
	account.Disabled = true
	accounts["inspection-account"] = account
	record.Result.Disabled = true
	record.Result.Health = InspectionHealthHealthy
	record.Result.ReasonCode = "healthy_recent_success"
	record.Result.Recommendation = InspectionRecommendationEnable
	record.Result.AutoDisableEligible = false
	records["inspection-account"] = record
	recovery, recoveryActions := engine.applyAutomaticActions(context.Background(), policy, accounts, records, resetAt, "", "")
	recovered := records["inspection-account"]
	if recovery.AutoEnabled != 1 || recovery.Failed != 0 || len(recoveryActions) != 1 || len(host.saves) != 2 ||
		recovered.Result.Disabled || recovered.Result.OwnedDisable || recovered.Result.AutoDisableProbeStatus != "" || !recovered.DisabledRecoverAfter.IsZero() {
		t.Fatalf("weekly reset recovery=%#v actions=%#v saves=%d record=%#v", recovery, recoveryActions, len(host.saves), recovered)
	}
}

func TestWeeklyOverdraftProbeDecisionPersistsWithoutDiagnosticPayloads(t *testing.T) {
	now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	path := inspectionStorePath(t.TempDir())
	state := persistedInspectionState{
		Version: inspectionStoreVersion,
		Policy:  defaultInspectionPolicy(),
		Records: map[string]inspectionRecord{"inspection-account": {Result: InspectionResult{
			ID: "inspection-account", Health: InspectionHealthQuotaLimited, ReasonCode: "quota_exhausted",
			Confidence: InspectionConfidenceHigh, Recommendation: InspectionRecommendationDisable,
			AutoDisableProbeName: "weekly_overdraft", AutoDisableProbeStatus: InspectionAutoDisableProbeFailed,
			AutoDisableProbeAttempts: weeklyOverdraftProbeAttempts, AutoDisableProbeLimit: weeklyOverdraftProbeAttempts,
			AutoDisableProbeReasonCode: "quota_limited", AutoDisableProbeModel: defaultCodexFallbackModel,
			AutoDisableProbeTestedAt: timePointer(now),
		}}},
	}
	if errSave := saveInspectionState(path, state); errSave != nil {
		t.Fatalf("save inspection state: %v", errSave)
	}
	loaded, errLoad := loadInspectionState(path)
	if errLoad != nil {
		t.Fatalf("load inspection state: %v", errLoad)
	}
	result := loaded.Records["inspection-account"].Result
	if result.AutoDisableProbeName != "weekly_overdraft" || result.AutoDisableProbeStatus != InspectionAutoDisableProbeFailed ||
		result.AutoDisableProbeAttempts != weeklyOverdraftProbeAttempts || result.AutoDisableProbeLimit != weeklyOverdraftProbeAttempts ||
		result.AutoDisableProbeReasonCode != "quota_limited" || result.AutoDisableProbeModel != defaultCodexFallbackModel ||
		result.AutoDisableProbeTestedAt == nil || !result.AutoDisableProbeTestedAt.Equal(now) {
		t.Fatalf("persisted probe decision = %#v", result)
	}
}

func weeklyOverdraftActionRecords(resetAt time.Time) map[string]inspectionRecord {
	return map[string]inspectionRecord{
		"inspection-account": {Result: InspectionResult{
			ID: "inspection-account", Name: "inspection.json", Provider: "codex", Health: InspectionHealthQuotaLimited,
			ReasonCode: "quota_exhausted", QuotaWindow: InspectionQuotaWindowSevenDay, Confidence: InspectionConfidenceHigh,
			Recommendation: InspectionRecommendationDisable, Editable: true, AutoDisableEligible: true,
			SignalSource: InspectionSignalNative, RecoverAfter: timePointer(resetAt),
		}},
	}
}

func weeklyOverdraftActionAccounts() map[string]Account {
	return map[string]Account{
		"inspection-account": {ID: "inspection-account", Name: "inspection.json", Provider: "codex", Editable: true, path: "/auths/inspection.json"},
	}
}

func TestWeeklyOverdraftExperimentKeepsFiveHourAccountEnabledWhenProbeSucceeds(t *testing.T) {
	now := time.Date(2026, time.July, 30, 7, 0, 0, 0, time.UTC)
	host := inspectionEditableHost(false)
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	engine.RegisterAutomaticDisableGuard(NewWeeklyOverdraftExperiment(func() bool { return true }))
	usage := NewUsageTracker()
	defer usage.Close()
	usage.now = func() time.Time { return now }
	usage.persistDelay = time.Hour
	usage.Configure(Config{DataDir: t.TempDir()})
	usage.Observe(cpaapi.UsageRecord{
		AuthIndex: "inspection-account", RequestedAt: now, Detail: cpaapi.UsageDetail{TotalTokens: 30_875_000},
		ResponseHeaders: http.Header{
			"X-Codex-Secondary-Used-Percent":        []string{"100"},
			"X-Codex-Secondary-Window-Minutes":      []string{"300"},
			"X-Codex-Secondary-Reset-After-Seconds": []string{"3600"},
			"X-Codex-Primary-Used-Percent":          []string{"16"},
			"X-Codex-Primary-Window-Minutes":        []string{"10080"},
		},
	})
	engine.SetOverdraftCycleTracker(usage)
	probeCalls := 0
	engine.automaticDisableProbe = func(_ context.Context, request ModelTestRequest, _, _ string) (ModelTestResult, error) {
		probeCalls++
		return ModelTestResult{
			AccountID: request.AccountID, Model: request.Model, Status: "available", ReasonCode: "model_response_ok",
			StatusCode: http.StatusOK, TestedAt: now.Add(time.Duration(probeCalls) * time.Second),
			Experiment: &ModelTestExperiment{Name: "weekly_overdraft", Applied: true, CallID: "call_five_hour_gate"},
		}, nil
	}
	records := map[string]inspectionRecord{
		"inspection-account": {Result: InspectionResult{
			ID: "inspection-account", Name: "inspection.json", Provider: "codex", Health: InspectionHealthQuotaLimited,
			ReasonCode: "quota_exhausted", QuotaWindow: InspectionQuotaWindowFiveHour, Confidence: InspectionConfidenceHigh,
			Recommendation: InspectionRecommendationDisable, Editable: true, AutoDisableEligible: true, SignalSource: InspectionSignalActiveProbe,
		}, Probe: inspectionProbeSignal{Status: "review", Kind: InspectionProbeKindCredential, ReasonCode: "quota_limited"}},
	}
	accounts := map[string]Account{
		"inspection-account": {ID: "inspection-account", Name: "inspection.json", Provider: "codex", Editable: true, path: "/auths/inspection.json"},
	}
	policy := defaultInspectionPolicy()
	policy.AutoDisable = true
	summary, actions := engine.applyAutomaticActions(context.Background(), policy, accounts, records, now, "", "")
	result := records["inspection-account"].Result
	if probeCalls != 1 || summary.AutoDisabled != 0 || summary.Failed != 0 || len(actions) != 0 || len(host.saves) != 0 || result.Disabled ||
		result.AutoDisableProbeStatus != InspectionAutoDisableProbePassed || result.AutoDisableProbeAttempts != 1 {
		t.Fatalf("five-hour quota result summary=%#v actions=%#v probes=%d saves=%d record=%#v", summary, actions, probeCalls, len(host.saves), records["inspection-account"])
	}
	started := usage.Snapshot("inspection-account").Codex.FiveHour
	wantRecoverAt := now.Add(time.Second).Add(5 * time.Hour)
	if !started.OverdraftActive || started.OverdraftStartedAt == nil || !started.OverdraftStartedAt.Equal(now.Add(time.Second)) ||
		started.OverdraftRecoverAt == nil || !started.OverdraftRecoverAt.Equal(wantRecoverAt) {
		t.Fatalf("successful five-hour continuation did not start a frozen cycle: %#v", started)
	}

	_, _ = engine.applyAutomaticActions(context.Background(), policy, accounts, records, now.Add(time.Minute), "", "")
	repeated := usage.Snapshot("inspection-account").Codex.FiveHour
	if probeCalls != 2 || repeated.OverdraftRecoverAt == nil || !repeated.OverdraftRecoverAt.Equal(wantRecoverAt) {
		t.Fatalf("repeated successful continuation moved the frozen cycle: probes=%d window=%#v", probeCalls, repeated)
	}
}

func TestWeeklyOverdraftExperimentDisablesFiveHourAccountOnlyAfterFiveFailedProbes(t *testing.T) {
	now := time.Date(2026, time.July, 29, 9, 0, 0, 0, time.UTC)
	resetAt := now.Add(5 * time.Hour)
	host := inspectionEditableHost(false)
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	engine.RegisterAutomaticDisableGuard(NewWeeklyOverdraftExperiment(func() bool { return true }))
	cycleTracker := &recordingOverdraftCycleStopper{}
	engine.SetOverdraftCycleTracker(cycleTracker)
	probeCalls := 0
	engine.automaticDisableProbe = func(_ context.Context, request ModelTestRequest, _, _ string) (ModelTestResult, error) {
		probeCalls++
		return ModelTestResult{
			AccountID: request.AccountID, Model: request.Model, Status: "review", ReasonCode: "quota_limited",
			StatusCode: http.StatusTooManyRequests, TestedAt: now.Add(time.Duration(probeCalls) * time.Second),
			Experiment: &ModelTestExperiment{Name: "weekly_overdraft", Applied: true, CallID: "call_five_hour_gate"},
		}, nil
	}
	records := weeklyOverdraftActionRecords(resetAt)
	record := records["inspection-account"]
	record.Result.QuotaWindow = InspectionQuotaWindowFiveHour
	records["inspection-account"] = record
	policy := defaultInspectionPolicy()
	policy.AutoDisable = true

	summary, actions := engine.applyAutomaticActions(context.Background(), policy, weeklyOverdraftActionAccounts(), records, now, "", "")
	result := records["inspection-account"]
	if probeCalls != weeklyOverdraftProbeAttempts || summary.AutoDisabled != 1 || summary.Failed != 0 || len(actions) != 1 || len(host.saves) != 1 ||
		!result.Result.Disabled || result.Result.AutoDisableProbeStatus != InspectionAutoDisableProbeFailed ||
		result.Result.AutoDisableProbeAttempts != weeklyOverdraftProbeAttempts || !result.DisabledRecoverAfter.Equal(resetAt) || len(cycleTracker.started) != 0 {
		t.Fatalf("failed five-hour gate summary=%#v actions=%#v probes=%d saves=%d record=%#v", summary, actions, probeCalls, len(host.saves), result)
	}
}

func TestFiveHourOverdraftBackgroundInspectionRunsGateBeforeDisable(t *testing.T) {
	now := time.Date(2026, time.July, 29, 9, 0, 0, 0, time.UTC)
	resetAt := now.Add(5 * time.Hour)
	tests := []struct {
		name          string
		managementKey string
		configure     func(*InspectionEngine, *AccountService)
		wantStatus    string
		wantAttempts  int
		wantDisabled  bool
		wantReason    string
	}{
		{
			name: "first successful overdraft probe keeps the account enabled", managementKey: "management-secret",
			configure: func(engine *InspectionEngine, _ *AccountService) {
				engine.automaticDisableProbe = func(_ context.Context, request ModelTestRequest, _, _ string) (ModelTestResult, error) {
					return ModelTestResult{
						AccountID: request.AccountID, Model: request.Model, Status: "available", ReasonCode: "model_response_ok",
						StatusCode: http.StatusOK, TestedAt: now,
						Experiment: &ModelTestExperiment{Name: "weekly_overdraft", Applied: true, CallID: "call_background_five_hour"},
					}, nil
				}
			},
			wantStatus: InspectionAutoDisableProbePassed, wantAttempts: 1, wantDisabled: false, wantReason: "model_response_ok",
		},
		{
			name: "five explicit failures permit disable", managementKey: "",
			configure: func(engine *InspectionEngine, _ *AccountService) {
				engine.automaticDisableProbe = func(_ context.Context, request ModelTestRequest, _, _ string) (ModelTestResult, error) {
					return ModelTestResult{
						AccountID: request.AccountID, Model: request.Model, Status: "review", ReasonCode: "quota_limited",
						StatusCode: http.StatusTooManyRequests, TestedAt: now,
						Experiment: &ModelTestExperiment{Name: "weekly_overdraft", Applied: true, CallID: "call_background_five_hour"},
					}, nil
				}
			},
			wantStatus: InspectionAutoDisableProbeFailed, wantAttempts: weeklyOverdraftProbeAttempts, wantDisabled: true, wantReason: "quota_limited",
		},
		{
			name: "missing authenticated probe path is inconclusive and never disables", managementKey: "",
			configure: func(engine *InspectionEngine, accounts *AccountService) {
				engine.SetModelTestService(NewModelTestService(accounts))
			},
			wantStatus: InspectionAutoDisableProbeInconclusive, wantAttempts: 0, wantDisabled: false, wantReason: "management_auth_unavailable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			host := inspectionEditableHost(false)
			host.entries[0].Unavailable = true
			host.entries[0].PlanType = "k12"
			usage := notificationUsageReader{
				"inspection-account": notificationCodexUsage(
					&UsageWindowSnapshot{UsedPercent: 100, WindowMinutes: 300, ResetAt: &resetAt},
					&UsageWindowSnapshot{UsedPercent: 17, WindowMinutes: 10_080},
				),
			}
			accounts := NewAccountService(host, usage)
			engine := NewInspectionEngine(accounts, host, NewMutationCoordinator())
			engine.now = func() time.Time { return now }
			engine.policy = defaultInspectionPolicy()
			engine.policy.AutoDisable = true
			engine.managementKey = test.managementKey
			engine.RegisterAutomaticDisableGuard(NewWeeklyOverdraftExperiment(func() bool { return true }))
			binding, ok := usageBindingForEntry(host.entries[0])
			if !ok {
				t.Fatal("five-hour fixture has no stable account identity")
			}
			engine.records["inspection-account"] = inspectionRecord{
				AccountIdentity: binding.Key,
				Result: InspectionResult{
					ID: "inspection-account", Provider: "codex", Health: InspectionHealthQuotaLimited,
					ReasonCode: "quota_exhausted", QuotaWindow: InspectionQuotaWindowFiveHour,
					Confidence: InspectionConfidenceHigh, Recommendation: InspectionRecommendationDisable,
					AutoDisableEligible: true, FailureStreak: 4, LastCheckedAt: now.Add(-time.Minute), SignalSource: InspectionSignalNative,
				},
			}
			test.configure(engine, accounts)

			engine.scanWithMode(context.Background(), false, false, false)
			results := engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 20})
			if len(results.Results) != 1 {
				t.Fatalf("background inspection result count = %d", len(results.Results))
			}
			result := results.Results[0]
			if result.QuotaWindow != InspectionQuotaWindowFiveHour || result.AutoDisableProbeStatus != test.wantStatus ||
				result.AutoDisableProbeAttempts != test.wantAttempts || result.Disabled != test.wantDisabled ||
				result.AutoDisableProbeReasonCode != test.wantReason {
				t.Fatalf("background five-hour result = %#v", result)
			}
		})
	}
}

func TestFiveHourOverdraftGatePublishesRunningStateBeforeProbeCompletes(t *testing.T) {
	now := time.Date(2026, time.July, 29, 10, 0, 0, 0, time.UTC)
	host := inspectionEditableHost(false)
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	engine.RegisterAutomaticDisableGuard(NewWeeklyOverdraftExperiment(func() bool { return true }))
	started := make(chan struct{})
	release := make(chan struct{})
	engine.automaticDisableProbe = func(_ context.Context, request ModelTestRequest, _, _ string) (ModelTestResult, error) {
		close(started)
		<-release
		return ModelTestResult{
			AccountID: request.AccountID, Model: request.Model, Status: "available", ReasonCode: "model_response_ok",
			StatusCode: http.StatusOK, TestedAt: now,
			Experiment: &ModelTestExperiment{Name: "weekly_overdraft", Applied: true, CallID: "call_pending_five_hour"},
		}, nil
	}
	records := weeklyOverdraftActionRecords(now.Add(5 * time.Hour))
	record := records["inspection-account"]
	record.Result.QuotaWindow = InspectionQuotaWindowFiveHour
	record.Result.LastCheckedAt = now
	records["inspection-account"] = record
	engine.records = cloneInspectionRecords(records)
	policy := defaultInspectionPolicy()
	policy.AutoDisable = true
	done := make(chan struct{})
	go func() {
		engine.applyAutomaticActions(context.Background(), policy, weeklyOverdraftActionAccounts(), records, now, "", "management-secret")
		close(done)
	}()
	<-started
	running := engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 20})
	if len(running.Results) != 1 || running.Results[0].AutoDisableProbeStatus != InspectionAutoDisableProbePending ||
		running.Results[0].AutoDisableProbeAttempts != 0 || running.Results[0].AutoDisableProbeLimit != weeklyOverdraftProbeAttempts {
		t.Fatalf("running five-hour gate = %#v", running.Results)
	}
	close(release)
	<-done
	completed := engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 20})
	if completed.Results[0].AutoDisableProbeStatus != InspectionAutoDisableProbePassed || completed.Results[0].AutoDisableProbeAttempts != 1 {
		t.Fatalf("completed five-hour gate = %#v", completed.Results[0])
	}
}

func weeklyUsageWithAuth(record cpaapi.UsageRecord, authIndex string) cpaapi.UsageRecord {
	record.Provider = "codex"
	record.AuthIndex = authIndex
	return record
}

func TestWeeklyOverdraftExperimentExpiredResetDoesNotSuppressRemediation(t *testing.T) {
	now := time.Date(2026, time.July, 22, 10, 0, 0, 0, time.UTC)
	headers := http.Header{
		"X-Codex-Secondary-Used-Percent":   []string{"100"},
		"X-Codex-Secondary-Window-Minutes": []string{"10080"},
		"X-Codex-Secondary-Reset-At":       []string{strconv.FormatInt(now.Add(-30*time.Second).Unix(), 10)},
	}
	experiment := NewWeeklyOverdraftExperiment(func() bool { return true })
	if !experiment.AllowUsageAutoDisable(cpaapi.UsageRecord{ResponseHeaders: headers}, now) {
		t.Fatal("expired weekly window suppressed remediation")
	}
}

type fakeOverdraftGate struct {
	state overdraftGateState
}

func (g fakeOverdraftGate) OverdraftGateState(string) overdraftGateState { return g.state }

func (g fakeOverdraftGate) NoteOverdraftInjection(string) {}

// recordingOverdraftGate records the identifier each gate query receives so
// tests can assert which metadata key family reached the usage state.
type recordingOverdraftGate struct {
	gate   overdraftGate
	record *string
}

func (g recordingOverdraftGate) OverdraftGateState(identifier string) overdraftGateState {
	if g.record != nil {
		*g.record = identifier
	}
	return g.gate.OverdraftGateState(identifier)
}

func (g recordingOverdraftGate) NoteOverdraftInjection(identifier string) {
	g.gate.NoteOverdraftInjection(identifier)
}

// notingOverdraftGate records only injection notes so tests can distinguish
// interceptor writes from eligibility reads on the gate.
type notingOverdraftGate struct {
	gate  overdraftGate
	noted *string
	count *int
}

func (g notingOverdraftGate) OverdraftGateState(identifier string) overdraftGateState {
	return g.gate.OverdraftGateState(identifier)
}

func (g notingOverdraftGate) NoteOverdraftInjection(identifier string) {
	if g.noted != nil {
		*g.noted = identifier
	}
	if g.count != nil {
		*g.count++
	}
	g.gate.NoteOverdraftInjection(identifier)
}

func overdraftInjectionRequest() cpaapi.RequestInterceptRequest {
	return cpaapi.RequestInterceptRequest{
		ToFormat: "codex",
		Metadata: map[string]any{"selected_auth_index": "overdraft-account"},
		Body:     []byte(`{"model":"gpt-5.4","store":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`),
	}
}

func TestWeeklyOverdraftExperimentPrearmsAt95Percent(t *testing.T) {
	experiment := NewWeeklyOverdraftExperiment(func() bool { return true })
	experiment.newCallID = func() (string, bool) { return "call_cpa_overdraft_test", true }
	now := time.Now().UTC()
	experiment.WithOverdraftGate(fakeOverdraftGate{state: overdraftGateState{
		Has:                 true,
		SevenDayUsedPercent: 95,
		SevenDayCycleStatus: overdraftStatusPending,
		SevenDayRecoverAt:   now.Add(time.Hour),
	}})
	response, changed := experiment.InterceptRequest(overdraftInjectionRequest())
	if !changed || len(response.Body) == 0 {
		t.Fatal("95% pre-arm window was not injected")
	}
	experiment.WithOverdraftGate(fakeOverdraftGate{state: overdraftGateState{
		Has:                 true,
		SevenDayUsedPercent: 95,
		SevenDayCycleStatus: overdraftStatusPassed,
		SevenDayRecoverAt:   now.Add(time.Hour),
	}})
	if _, changed := experiment.InterceptRequest(overdraftInjectionRequest()); !changed {
		t.Fatal("passed cycle at 95% stopped injecting")
	}
}

func TestWeeklyOverdraftExperimentDoesNotInjectFailedOrRecoveredCycle(t *testing.T) {
	experiment := NewWeeklyOverdraftExperiment(func() bool { return true })
	experiment.newCallID = func() (string, bool) { return "call_cpa_overdraft_test", true }
	for _, status := range []string{overdraftStatusFailed, overdraftStatusRecovered} {
		experiment.WithOverdraftGate(fakeOverdraftGate{state: overdraftGateState{
			Has:                 true,
			FiveHourUsedPercent: 100,
			FiveHourCycleStatus: status,
		}})
		if _, changed := experiment.InterceptRequest(overdraftInjectionRequest()); changed {
			t.Fatalf("cycle status %q still injected", status)
		}
	}
}

func TestWeeklyOverdraftExperimentBelow95WithoutCycleDoesNotInject(t *testing.T) {
	experiment := NewWeeklyOverdraftExperiment(func() bool { return true })
	experiment.newCallID = func() (string, bool) { return "call_cpa_overdraft_test", true }
	experiment.WithOverdraftGate(fakeOverdraftGate{state: overdraftGateState{
		Has:                 true,
		FiveHourUsedPercent: 80,
	}})
	if _, changed := experiment.InterceptRequest(overdraftInjectionRequest()); changed {
		t.Fatal("80% window without an open cycle was injected")
	}
	now := time.Now().UTC()
	experiment.WithOverdraftGate(fakeOverdraftGate{state: overdraftGateState{
		Has:                 true,
		FiveHourUsedPercent: 80,
		FiveHourCycleStatus: overdraftStatusInconclusive,
		FiveHourRecoverAt:   now.Add(time.Hour),
	}})
	if _, changed := experiment.InterceptRequest(overdraftInjectionRequest()); !changed {
		t.Fatal("open inconclusive cycle below 95% stopped injecting")
	}
}

func TestWeeklyOverdraftExperimentGateStateMissingIsConservative(t *testing.T) {
	experiment := NewWeeklyOverdraftExperiment(func() bool { return true })
	experiment.newCallID = func() (string, bool) { return "call_cpa_overdraft_test", true }
	experiment.WithOverdraftGate(fakeOverdraftGate{state: overdraftGateState{Has: false}})
	if _, changed := experiment.InterceptRequest(overdraftInjectionRequest()); changed {
		t.Fatal("missing gate state still injected")
	}
	if _, changed := experiment.InterceptRequest(cpaapi.RequestInterceptRequest{ToFormat: "codex", Body: []byte(`{"input":[]}`)}); changed {
		t.Fatal("request without an account identity was injected")
	}
}

func TestWeeklyOverdraftExperimentResolvesAuthIDMetadataKey(t *testing.T) {
	experiment := NewWeeklyOverdraftExperiment(func() bool { return true })
	experiment.newCallID = func() (string, bool) { return "call_cpa_overdraft_test", true }
	now := time.Now().UTC()
	experiment.WithOverdraftGate(fakeOverdraftGate{state: overdraftGateState{
		Has:                 true,
		SevenDayUsedPercent: 100,
		SevenDayCycleStatus: overdraftStatusPending,
		SevenDayRecoverAt:   now.Add(time.Hour),
	}})
	// The CPA request lifecycle populates selected_auth_id with the selected
	// account credential id (see AccountConcurrencyService, which keys
	// per-account concurrency by Account.AuthID). runtimeIdentityFromMetadata
	// treats that key as an opaque credential id, and UsageTracker resolves it
	// back to the auth index through the auth file bindings, so the raw
	// metadata value must reach the gate unchanged.
	for _, key := range []string{"selected_auth_id", "auth_id"} {
		request := overdraftInjectionRequest()
		request.Metadata = map[string]any{key: "overdraft-account"}
		if _, changed := experiment.InterceptRequest(request); !changed {
			t.Fatalf("metadata key %q did not reach the overdraft gate", key)
		}
	}
}

func TestWeeklyOverdraftExperimentRecordsInjectionOnSuccess(t *testing.T) {
	experiment := NewWeeklyOverdraftExperiment(func() bool { return true })
	experiment.newCallID = func() (string, bool) { return "call_cpa_overdraft_test", true }
	now := time.Now().UTC()
	var received string
	var notes int
	experiment.WithOverdraftGate(notingOverdraftGate{
		gate: fakeOverdraftGate{state: overdraftGateState{
			Has:                 true,
			SevenDayUsedPercent: 95,
			SevenDayCycleStatus: overdraftStatusPending,
			SevenDayRecoverAt:   now.Add(time.Hour),
		}},
		noted: &received,
		count: &notes,
	})
	if _, changed := experiment.InterceptRequest(overdraftInjectionRequest()); !changed {
		t.Fatal("95% pre-arm window was not injected")
	}
	if received != "overdraft-account" || notes != 1 {
		t.Fatalf("injection was recorded for %q notes:%d, want the resolved account with one note", received, notes)
	}
	// An unchanged request (already carrying the pair) must not record a new
	// injection: the interceptor did not append anything for this account.
	received = ""
	notes = 0
	already := overdraftInjectionRequest()
	already.Body = []byte(`{"input":[{"type":"custom_tool_call","name":"exec","call_id":"call_cpa_overdraft_existing","input":{}},{"type":"custom_tool_call_output","call_id":"call_cpa_overdraft_existing","output":[]},{"type":"message","role":"user","content":[]}]}`)
	if _, changed := experiment.InterceptRequest(already); changed {
		t.Fatal("already-injected request was changed")
	}
	if notes != 0 {
		t.Fatalf("no-op interception recorded %d injection(s)", notes)
	}
}

func TestWeeklyOverdraftExperimentAllowUsageAutoDisableVetoesActiveCycle(t *testing.T) {
	experiment := NewWeeklyOverdraftExperiment(func() bool { return true })
	now := time.Date(2026, time.August, 25, 6, 0, 0, 0, time.UTC)
	record := cpaapi.UsageRecord{AuthIndex: "overdraft-account"}
	for _, status := range []string{overdraftStatusPending, overdraftStatusPassed, overdraftStatusInconclusive} {
		experiment.WithOverdraftGate(fakeOverdraftGate{state: overdraftGateState{
			Has:                 true,
			SevenDayCycleStatus: status,
		}})
		if experiment.AllowUsageAutoDisable(record, now) {
			t.Fatalf("auto disable was allowed while cycle status %q is active", status)
		}
	}
	for _, status := range []string{overdraftStatusFailed, overdraftStatusRecovered} {
		experiment.WithOverdraftGate(fakeOverdraftGate{state: overdraftGateState{
			Has:                 true,
			SevenDayCycleStatus: status,
		}})
		if !experiment.AllowUsageAutoDisable(record, now) {
			t.Fatalf("auto disable was vetoed after cycle status %q", status)
		}
	}
	experiment.WithOverdraftGate(fakeOverdraftGate{state: overdraftGateState{Has: true}})
	if !experiment.AllowUsageAutoDisable(record, now) {
		t.Fatal("auto disable was vetoed without any open cycle")
	}
	// The veto must also fire when the host only reports the credential ID on
	// the usage record: the gate resolves it like the request metadata does.
	var received string
	experiment.WithOverdraftGate(recordingOverdraftGate{
		gate:   fakeOverdraftGate{state: overdraftGateState{Has: true, SevenDayCycleStatus: overdraftStatusPending}},
		record: &received,
	})
	authIDOnly := cpaapi.UsageRecord{AuthID: "cred-only"}
	if experiment.AllowUsageAutoDisable(authIDOnly, now) {
		t.Fatal("auto disable was allowed when the active cycle was reached via AuthID")
	}
	if received != "cred-only" {
		t.Fatalf("gate identifier = %q, want the credential ID", received)
	}
	experiment.WithOverdraftGate(nil)
	if !experiment.AllowUsageAutoDisable(record, now) {
		t.Fatal("auto disable was vetoed without a gate")
	}
}

func TestWeeklyOverdraftExperimentExpiredCycleRecoverAtFallsBackToPrearm(t *testing.T) {
	experiment := NewWeeklyOverdraftExperiment(func() bool { return true })
	experiment.newCallID = func() (string, bool) { return "call_cpa_overdraft_test", true }
	now := time.Now().UTC()
	past := now.Add(-5 * time.Minute)
	future := now.Add(5 * time.Hour)
	for _, status := range []string{overdraftStatusPending, overdraftStatusPassed, overdraftStatusInconclusive} {
		experiment.WithOverdraftGate(fakeOverdraftGate{state: overdraftGateState{
			Has:                 true,
			FiveHourUsedPercent: 80,
			FiveHourCycleStatus: status,
			FiveHourRecoverAt:   past,
		}})
		if _, changed := experiment.InterceptRequest(overdraftInjectionRequest()); changed {
			t.Fatalf("expired cycle status %q below 95%% still injected", status)
		}
		// Once the recovery deadline passes, the account falls back to the
		// plain 95% pre-arm rule: a still-fresh window at 100% injects again.
		experiment.WithOverdraftGate(fakeOverdraftGate{state: overdraftGateState{
			Has:                 true,
			FiveHourUsedPercent: 100,
			FiveHourCycleStatus: status,
			FiveHourRecoverAt:   past,
			FiveHourResetAt:     future,
		}})
		if _, changed := experiment.InterceptRequest(overdraftInjectionRequest()); !changed {
			t.Fatalf("expired cycle status %q at 100%% did not fall back to pre-arm", status)
		}
	}
}

func TestWeeklyOverdraftExperimentExpiredWindowResetAtDoesNotInject(t *testing.T) {
	experiment := NewWeeklyOverdraftExperiment(func() bool { return true })
	experiment.newCallID = func() (string, bool) { return "call_cpa_overdraft_test", true }
	now := time.Now().UTC()
	past := now.Add(-5 * time.Minute)
	future := now.Add(5 * time.Hour)
	experiment.WithOverdraftGate(fakeOverdraftGate{state: overdraftGateState{
		Has:                 true,
		FiveHourUsedPercent: 100,
		FiveHourResetAt:     past,
	}})
	if _, changed := experiment.InterceptRequest(overdraftInjectionRequest()); changed {
		t.Fatal("expired window reset at 100% was injected")
	}
	experiment.WithOverdraftGate(fakeOverdraftGate{state: overdraftGateState{
		Has:                 true,
		FiveHourUsedPercent: 100,
		FiveHourResetAt:     future,
	}})
	if _, changed := experiment.InterceptRequest(overdraftInjectionRequest()); !changed {
		t.Fatal("fresh window reset at 100% was not injected")
	}
	experiment.WithOverdraftGate(fakeOverdraftGate{state: overdraftGateState{
		Has:                 true,
		FiveHourUsedPercent: 100,
	}})
	if _, changed := experiment.InterceptRequest(overdraftInjectionRequest()); !changed {
		t.Fatal("window without a reset deadline at 100% was not injected")
	}
}

func TestWeeklyOverdraftExperimentDoesNotDoubleInject(t *testing.T) {
	experiment := NewWeeklyOverdraftExperiment(func() bool { return true })
	experiment.newCallID = func() (string, bool) { return "call_cpa_overdraft_test", true }
	now := time.Now().UTC()
	experiment.WithOverdraftGate(fakeOverdraftGate{state: overdraftGateState{
		Has:                 true,
		SevenDayUsedPercent: 100,
		SevenDayCycleStatus: overdraftStatusPending,
		SevenDayRecoverAt:   now.Add(time.Hour),
	}})
	for _, prefix := range []string{"call_cpa_overdraft_", "call_sub2api_overdraft_"} {
		alreadyInjected := fmt.Sprintf(`{"input":[{"type":"message","role":"user","content":"continue"},{"type":"custom_tool_call","name":"exec","call_id":%q,"input":"const r = await tools.exec_command(...);"},{"type":"message","role":"user","content":"after"}]}`, prefix+"deadbeef")
		response, changed := experiment.InterceptRequest(cpaapi.RequestInterceptRequest{
			ToFormat: "codex",
			Metadata: map[string]any{"selected_auth_index": "overdraft-account"},
			Body:     []byte(alreadyInjected),
		})
		if changed || len(response.Body) != 0 {
			t.Fatalf("request already carrying %s injection was modified again", prefix)
		}
	}
}

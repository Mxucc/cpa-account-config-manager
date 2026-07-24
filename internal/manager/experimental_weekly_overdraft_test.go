package manager

import (
	"context"
	"encoding/json"
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

func TestWeeklyOverdraftExperimentSuppressesOnlyWeeklyQuotaAutoDisable(t *testing.T) {
	now := time.Date(2026, time.July, 22, 10, 0, 0, 0, time.UTC)
	experiment := NewWeeklyOverdraftExperiment(func() bool { return true })
	weeklyUsage := cpaapi.UsageRecord{ResponseHeaders: codexUsageObservationHeaders(now, 20, 100)}
	if experiment.AllowUsageAutoDisable(weeklyUsage, now) {
		t.Fatal("weekly exhaustion was allowed to wake auto-disable")
	}
	if !experiment.AllowUsageAutoDisable(cpaapi.UsageRecord{ResponseHeaders: codexUsageObservationHeaders(now, 100, 20)}, now) {
		t.Fatal("five-hour exhaustion was incorrectly treated as weekly overdraft")
	}
	if !experiment.AllowUsageAutoDisable(cpaapi.UsageRecord{ResponseHeaders: codexUsageObservationHeaders(now, 100, 100)}, now) {
		t.Fatal("weekly exhaustion suppressed an actionable five-hour exhaustion")
	}
	if experiment.AllowInspectionAutoDisable(InspectionResult{ReasonCode: "quota_exhausted", QuotaWindow: InspectionQuotaWindowSevenDay}) {
		t.Fatal("weekly quota inspection was allowed to auto-disable")
	}
	if experiment.AllowInspectionAutoDisable(InspectionResult{ReasonCode: "quota_limited", QuotaWindow: InspectionQuotaWindowSevenDay}) {
		t.Fatal("weekly quota probe was allowed to bypass the overdraft guard")
	}
	for _, result := range []InspectionResult{
		{ReasonCode: "quota_exhausted", QuotaWindow: InspectionQuotaWindowFiveHour},
		{ReasonCode: "quota_exhausted", QuotaWindow: InspectionQuotaWindowMultiple},
		{ReasonCode: "invalid_credentials", QuotaWindow: InspectionQuotaWindowSevenDay},
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
	if engine.pending || len(engine.scanWake) != 0 {
		t.Fatalf("weekly experiment still queued automatic disable: pending=%t wake=%d", engine.pending, len(engine.scanWake))
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

func TestWeeklyOverdraftExperimentAllowsFiveHourQuotaMutation(t *testing.T) {
	host := inspectionEditableHost(false)
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	engine.RegisterAutomaticDisableGuard(NewWeeklyOverdraftExperiment(func() bool { return true }))
	records := map[string]inspectionRecord{
		"inspection-account": {Result: InspectionResult{
			ID: "inspection-account", Name: "inspection.json", Provider: "codex", Health: InspectionHealthQuotaLimited,
			ReasonCode: "quota_exhausted", QuotaWindow: InspectionQuotaWindowFiveHour, Confidence: InspectionConfidenceHigh,
			Recommendation: InspectionRecommendationDisable, Editable: true, AutoDisableEligible: true, SignalSource: InspectionSignalActiveProbe,
		}, Probe: inspectionProbeSignal{Status: "review", ReasonCode: "quota_limited"}},
	}
	accounts := map[string]Account{
		"inspection-account": {ID: "inspection-account", Name: "inspection.json", Provider: "codex", Editable: true, path: "/auths/inspection.json"},
	}
	policy := defaultInspectionPolicy()
	policy.AutoDisable = true
	summary, actions := engine.applyAutomaticActions(context.Background(), policy, accounts, records, time.Now().UTC(), "", "")
	if summary.AutoDisabled != 1 || summary.Failed != 0 || len(actions) != 1 || len(host.saves) != 1 || !records["inspection-account"].Result.Disabled {
		t.Fatalf("five-hour quota result summary=%#v actions=%#v saves=%d record=%#v", summary, actions, len(host.saves), records["inspection-account"])
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

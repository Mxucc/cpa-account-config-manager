package manager

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// waitForNotificationURL returns the next notification URL the engine tried to
// deliver, failing the test when delivery never happens.
func waitForNotificationURL(t *testing.T, requestURLs <-chan string) string {
	t.Helper()
	select {
	case requested := <-requestURLs:
		return requested
	case <-time.After(2 * time.Second):
		t.Fatal("notification request was not sent")
		return ""
	}
}

// stateRulePolicy builds a notification policy that is scoped to codex accounts
// and carries the given state rules. It deliberately leaves both availability
// thresholds disabled, which is the configuration the state trigger exists for.
func stateRulePolicy(rules ...InspectionNotificationStateRule) InspectionNotificationPolicy {
	return InspectionNotificationPolicy{
		ID:                "state-watch",
		Name:              "Account state watch",
		Enabled:           true,
		Conditions:        PolicyConditionGroup{Operator: PolicyConditionAll, Conditions: []PolicyCondition{{Field: PolicyConditionProvider, Value: "codex"}}},
		StateRules:        rules,
		ThresholdOperator: PolicyConditionAll,
	}
}

func TestInspectionNotificationStateRulesValidateTargetsAndScopes(t *testing.T) {
	valid := []InspectionNotificationStateRule{
		{Match: PolicyStateMatchHealth, Value: "invalid_credentials"},
		{Match: PolicyStateMatchReasonCode, Value: "token_revoked"},
		{Match: PolicyStateMatchStatusCode, Value: "429"},
		{Match: PolicyStateMatchDisabled, Value: "true"},
		{Match: PolicyStateMatchDisableReason, Value: "invalid_credentials"},
		{Match: "  HEALTH ", Value: " Deactivated ", Scope: PolicyStateScopeAll},
		{Match: PolicyStateMatchStatusCode, Value: "403", Scope: PolicyStateScopeAtLeast, MinimumCount: 3},
	}
	policy := stateRulePolicy(valid...)
	policy.AvailableAccountsEnabled = false
	policy.AvailabilityPercentEnabled = false
	validated, errValidate := validateInspectionNotificationPolicies([]InspectionNotificationPolicy{policy})
	if errValidate != nil {
		t.Fatalf("valid state rules were rejected: %v", errValidate)
	}
	rules := validated[0].StateRules
	if len(rules) != len(valid) {
		t.Fatalf("validated state rules = %d, want %d", len(rules), len(valid))
	}
	if rules[0].Scope != PolicyStateScopeAny {
		t.Fatalf("default scope = %q, want %q", rules[0].Scope, PolicyStateScopeAny)
	}
	if rules[1].MinimumCount != 0 {
		t.Fatalf("non-at_least scope kept minimum_count = %d", rules[1].MinimumCount)
	}
	if rules[5].Match != "health" || rules[5].Value != "deactivated" || rules[5].Scope != PolicyStateScopeAll {
		t.Fatalf("normalized rule = %#v", rules[5])
	}
	if rules[6].MinimumCount != 3 {
		t.Fatalf("at_least minimum_count = %d, want 3", rules[6].MinimumCount)
	}

	// A policy without thresholds is only acceptable while it has a state rule.
	withoutTrigger := stateRulePolicy()
	if _, errPolicy := validateInspectionNotificationPolicies([]InspectionNotificationPolicy{withoutTrigger}); errPolicy == nil {
		t.Fatal("a policy without any trigger was accepted")
	}

	for name, rule := range map[string]InspectionNotificationStateRule{
		"unknown target":   {Match: "email", Value: "codex"},
		"unknown health":   {Match: PolicyStateMatchHealth, Value: "expired"},
		"unknown reason":   {Match: PolicyStateMatchReasonCode, Value: "made_up_reason"},
		"status too low":   {Match: PolicyStateMatchStatusCode, Value: "99"},
		"status too high":  {Match: PolicyStateMatchStatusCode, Value: "600"},
		"status not a int": {Match: PolicyStateMatchStatusCode, Value: "4xx"},
		"disabled word":    {Match: PolicyStateMatchDisabled, Value: "yes"},
		"empty value":      {Match: PolicyStateMatchHealth, Value: ""},
		"unknown scope":    {Match: PolicyStateMatchHealth, Value: "healthy", Scope: "some"},
		"at_least zero":    {Match: PolicyStateMatchHealth, Value: "healthy", Scope: PolicyStateScopeAtLeast},
	} {
		t.Run(name, func(t *testing.T) {
			if _, errRule := validateInspectionNotificationPolicies([]InspectionNotificationPolicy{stateRulePolicy(rule)}); errRule == nil {
				t.Fatalf("invalid state rule was accepted: %#v", rule)
			}
		})
	}

	tooMany := make([]InspectionNotificationStateRule, maxInspectionNotificationStateRules+1)
	for index := range tooMany {
		tooMany[index] = InspectionNotificationStateRule{Match: PolicyStateMatchHealth, Value: "healthy"}
	}
	if _, errMany := validateInspectionNotificationPolicies([]InspectionNotificationPolicy{stateRulePolicy(tooMany...)}); errMany == nil {
		t.Fatal("state rule limit was not enforced")
	}
}

func TestInspectionNotificationStateRuleScopeMatching(t *testing.T) {
	accounts := map[string]Account{
		"a": {ID: "a", Provider: "codex", Disabled: true},
		"b": {ID: "b", Provider: "codex", Disabled: true},
		"c": {ID: "c", Provider: "codex"},
	}
	allDisabled := map[string]inspectionRecord{}
	for _, id := range []string{"a", "b", "c"} {
		allDisabled[id] = inspectionRecord{Result: InspectionResult{ID: id, Health: InspectionHealthDisabled}}
	}

	evaluation := inspectionNotificationStateEvaluationFor(
		InspectionNotificationPolicy{StateRules: []InspectionNotificationStateRule{{Match: PolicyStateMatchDisabled, Value: "true", Scope: PolicyStateScopeAny}}},
		accounts, allDisabled,
	)
	if len(evaluation.Reasons) != 1 || evaluation.Reasons[0] != "state_disabled_true" || evaluation.Accounts != 2 {
		t.Fatalf("any-scope evaluation = %#v", evaluation)
	}
	if evaluation.Rules[0] != "disabled=true" {
		t.Fatalf("rule label = %q", evaluation.Rules[0])
	}

	evaluation = inspectionNotificationStateEvaluationFor(
		InspectionNotificationPolicy{StateRules: []InspectionNotificationStateRule{{Match: PolicyStateMatchDisabled, Value: "true", Scope: PolicyStateScopeAll}}},
		accounts, allDisabled,
	)
	if !evaluation.empty() {
		t.Fatalf("all-scope matched while one account was still enabled: %#v", evaluation)
	}

	// "c" has no evidence at all, so an all-scope evidence rule must not claim
	// that every cohort account is disabled.
	partial := map[string]inspectionRecord{"a": allDisabled["a"], "b": allDisabled["b"]}
	evaluation = inspectionNotificationStateEvaluationFor(
		InspectionNotificationPolicy{StateRules: []InspectionNotificationStateRule{{Match: PolicyStateMatchHealth, Value: "disabled", Scope: PolicyStateScopeAll}}},
		accounts, partial,
	)
	if !evaluation.empty() {
		t.Fatalf("all-scope health matched without evidence for every cohort account: %#v", evaluation)
	}

	// Once every account in the cohort carries matching evidence it fires.
	evaluation = inspectionNotificationStateEvaluationFor(
		InspectionNotificationPolicy{StateRules: []InspectionNotificationStateRule{{Match: PolicyStateMatchHealth, Value: "disabled", Scope: PolicyStateScopeAll}}},
		map[string]Account{"a": accounts["a"], "b": accounts["b"]}, partial,
	)
	if evaluation.Accounts != 2 || len(evaluation.Reasons) != 1 {
		t.Fatalf("all-scope health evaluation = %#v", evaluation)
	}

	// An unknown persisted target must stay inert rather than fire.
	evaluation = inspectionNotificationStateEvaluationFor(
		InspectionNotificationPolicy{StateRules: []InspectionNotificationStateRule{{Match: "legacy_target", Value: "whatever"}}},
		accounts, allDisabled,
	)
	if !evaluation.empty() {
		t.Fatalf("unknown state target matched: %#v", evaluation)
	}

	// No state rules means the evaluation must not contribute a reason, which
	// keeps threshold-only policies byte-for-byte compatible.
	evaluation = inspectionNotificationStateEvaluationFor(InspectionNotificationPolicy{}, accounts, allDisabled)
	if !evaluation.empty() || evaluation.Accounts != 0 {
		t.Fatalf("policy without state rules produced %#v", evaluation)
	}
}

func TestInspectionNotificationStateRuleMatchesDisableReasonOnlyForOwnedDisables(t *testing.T) {
	rule := InspectionNotificationStateRule{Match: PolicyStateMatchDisableReason, Value: "invalid_credentials"}
	account := Account{ID: "a", Provider: "codex", Disabled: true}
	if !inspectionNotificationStateRuleMatches(rule, account, inspectionRecord{
		Result: InspectionResult{OwnedDisable: true}, DisableReason: "invalid_credentials",
	}, true) {
		t.Fatal("owned disable reason did not match")
	}
	if inspectionNotificationStateRuleMatches(rule, account, inspectionRecord{
		Result: InspectionResult{OwnedDisable: false}, DisableReason: "invalid_credentials",
	}, true) {
		t.Fatal("manually disabled account matched an owned disable reason")
	}
	if inspectionNotificationStateRuleMatches(rule, account, inspectionRecord{}, false) {
		t.Fatal("account without evidence matched a disable reason")
	}
}

// notificationStateTestEngine builds an engine that captures outgoing
// notification URLs instead of sending them.
func notificationStateTestEngine(t *testing.T, policy InspectionPolicy) (*InspectionEngine, chan string) {
	t.Helper()
	engine := NewInspectionEngine(nil, nil, nil)
	requestURLs := make(chan string, 4)
	engine.notificationDoer = anomalyNotificationDoerFunc(func(request *http.Request) (*http.Response, error) {
		requestURLs <- request.URL.String()
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     make(http.Header),
			Request:    request,
		}, nil
	})
	engine.Configure(Config{DataDir: t.TempDir(), InspectionPolicy: &policy})
	t.Cleanup(engine.Shutdown)
	return engine, requestURLs
}

func TestInspectionNotificationStateRuleNotifiesWhenEveryAccountIsDisabled(t *testing.T) {
	policy := defaultInspectionPolicy()
	policy.Enabled = true
	policy.NotificationCooldownMinutes = 60
	policy.NotificationPolicies = []InspectionNotificationPolicy{stateRulePolicy(
		InspectionNotificationStateRule{Match: PolicyStateMatchDisabled, Value: "true", Scope: PolicyStateScopeAll},
	)}
	policy.NotificationEndpoints = []InspectionNotificationEndpoint{{
		ID: "state-endpoint", URL: "https://notify.example/hook?event=${event}&rule=${state_rule}&matched=${matched_accounts}&disabled=${disabled_accounts}",
		Enabled: true, NotificationPolicyID: "state-watch",
	}}
	if _, errPolicy := validateInspectionPolicy(policy); errPolicy != nil {
		t.Fatalf("policy with a state trigger was rejected: %v", errPolicy)
	}
	engine, requestURLs := notificationStateTestEngine(t, policy)

	now := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)
	accounts := map[string]Account{
		"a": {ID: "a", Provider: "codex", Disabled: true},
		"b": {ID: "b", Provider: "codex", Disabled: true},
	}
	records := map[string]inspectionRecord{
		"a": {Result: InspectionResult{ID: "a", Health: InspectionHealthDisabled, ReasonCode: "manual_disabled"}},
		"b": {Result: InspectionResult{ID: "b", Health: InspectionHealthDisabled, ReasonCode: "manual_disabled"}},
	}
	if !engine.evaluateInspectionNotification(policy, accounts, records, now, true) {
		t.Fatal("every disabled account did not queue a state notification")
	}
	requested := waitForNotificationURL(t, requestURLs)
	parsed, errParse := url.Parse(requested)
	if errParse != nil {
		t.Fatalf("parse requested URL: %v", errParse)
	}
	for key, want := range map[string]string{
		"event": "state_disabled_true", "rule": "disabled=true", "matched": "2", "disabled": "2",
	} {
		if got := parsed.Query().Get(key); got != want {
			t.Errorf("query %s = %q, want %q", key, got, want)
		}
	}

	// Cooldown still applies to the state trigger.
	if engine.evaluateInspectionNotification(policy, accounts, records, now.Add(30*time.Minute), true) {
		t.Fatal("state notification ignored the cooldown")
	}
}

func TestInspectionNotificationStateRuleNotifiesOnRateLimitStatus(t *testing.T) {
	policy := defaultInspectionPolicy()
	policy.Enabled = true
	policy.NotificationPolicies = []InspectionNotificationPolicy{stateRulePolicy(
		InspectionNotificationStateRule{Match: PolicyStateMatchStatusCode, Value: "429"},
	)}
	policy.NotificationEndpoints = []InspectionNotificationEndpoint{{
		ID: "state-endpoint", URL: "https://notify.example/hook?event=${event}&rule=${state_rule}&matched=${matched_accounts}",
		Enabled: true, NotificationPolicyID: "state-watch",
	}}
	engine, requestURLs := notificationStateTestEngine(t, policy)

	now := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)
	accounts := map[string]Account{
		"limited": {ID: "limited", Provider: "codex"},
		"fine":    {ID: "fine", Provider: "codex"},
	}
	records := map[string]inspectionRecord{
		"limited": {Result: InspectionResult{ID: "limited", Health: InspectionHealthUnavailable, ReasonCode: "transient_failure", StatusCode: 429}},
		"fine":    {Result: InspectionResult{ID: "fine", Health: InspectionHealthHealthy, ReasonCode: "healthy_recent_success"}},
	}
	if !engine.evaluateInspectionNotification(policy, accounts, records, now, true) {
		t.Fatal("a 429 status did not queue a state notification")
	}
	parsed, errParse := url.Parse(waitForNotificationURL(t, requestURLs))
	if errParse != nil {
		t.Fatalf("parse requested URL: %v", errParse)
	}
	if got, want := parsed.Query().Get("event"), "state_status_code_429"; got != want {
		t.Errorf("event = %q, want %q", got, want)
	}
	if got, want := parsed.Query().Get("rule"), "status_code=429"; got != want {
		t.Errorf("rule = %q, want %q", got, want)
	}
	if got, want := parsed.Query().Get("matched"), "1"; got != want {
		t.Errorf("matched = %q, want %q", got, want)
	}

	// A cohort with no failing account must stay quiet.
	quiet := map[string]inspectionRecord{
		"limited": {Result: InspectionResult{ID: "limited", Health: InspectionHealthHealthy, ReasonCode: "healthy_recent_success"}},
		"fine":    {Result: InspectionResult{ID: "fine", Health: InspectionHealthHealthy, ReasonCode: "healthy_recent_success"}},
	}
	if engine.evaluateInspectionNotification(policy, accounts, quiet, now.Add(time.Hour), true) {
		t.Fatal("a healthy cohort queued a state notification")
	}
}

func TestInspectionNotificationStateRuleNotifiesWhenAuthorizationIsRevokedAndDisabled(t *testing.T) {
	policy := defaultInspectionPolicy()
	policy.Enabled = true
	policy.NotificationPolicies = []InspectionNotificationPolicy{stateRulePolicy(
		InspectionNotificationStateRule{Match: PolicyStateMatchDisableReason, Value: "invalid_credentials"},
	)}
	policy.NotificationEndpoints = []InspectionNotificationEndpoint{{
		ID: "state-endpoint", URL: "https://notify.example/hook?event=${event}&rule=${state_rule}",
		Enabled: true, NotificationPolicyID: "state-watch",
	}}
	engine, requestURLs := notificationStateTestEngine(t, policy)

	now := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)
	accounts := map[string]Account{"revoked": {ID: "revoked", Provider: "codex", Disabled: true}}
	records := map[string]inspectionRecord{
		"revoked": {
			Result:        InspectionResult{ID: "revoked", Health: InspectionHealthInvalidCredentials, OwnedDisable: true},
			DisableReason: "invalid_credentials",
		},
	}
	if !engine.evaluateInspectionNotification(policy, accounts, records, now, true) {
		t.Fatal("a revoked and disabled account did not queue a state notification")
	}
	parsed, errParse := url.Parse(waitForNotificationURL(t, requestURLs))
	if errParse != nil {
		t.Fatalf("parse requested URL: %v", errParse)
	}
	if got, want := parsed.Query().Get("rule"), "disable_reason=invalid_credentials"; got != want {
		t.Errorf("rule = %q, want %q", got, want)
	}

	// The same account disabled by hand must not report an auth revocation.
	manual := map[string]inspectionRecord{
		"revoked": {Result: InspectionResult{ID: "revoked", Health: InspectionHealthDisabled, ReasonCode: "manual_disabled"}},
	}
	if engine.evaluateInspectionNotification(policy, accounts, manual, now.Add(time.Hour), true) {
		t.Fatal("a manually disabled account was reported as a revoked credential")
	}
}

func TestInspectionNotificationStateRuleCombinesWithThresholds(t *testing.T) {
	policy := stateRulePolicy(
		InspectionNotificationStateRule{Match: PolicyStateMatchHealth, Value: "invalid_credentials"},
	)
	policy.AvailableAccountsEnabled = true
	policy.AvailableAccountsBelow = 1
	accounts := map[string]Account{"revoked": {ID: "revoked", Provider: "codex", Disabled: true}}
	records := map[string]inspectionRecord{
		"revoked": {Result: InspectionResult{ID: "revoked", Health: InspectionHealthInvalidCredentials, OwnedDisable: true}},
	}
	cohort := inspectionNotificationCohort(accounts, policy.Conditions)
	metrics := inspectionAnomalyNotificationMetrics(cohort, records)
	state := inspectionNotificationStateEvaluationFor(policy, cohort, records)

	reasons := inspectionNotificationPolicyReasons(policy, state, metrics)
	joined := strings.Join(reasons, ",")
	if !strings.Contains(joined, "state_health_invalid_credentials") {
		t.Fatalf("state reason missing from %q", joined)
	}
	if !strings.Contains(joined, "available_accounts_low") {
		t.Fatalf("threshold reason missing from %q", joined)
	}

	// Dropping the state rule must leave the previous threshold behaviour.
	thresholdOnly := policy
	thresholdOnly.StateRules = nil
	reasons = inspectionNotificationPolicyReasons(thresholdOnly, inspectionNotificationStateEvaluation{}, metrics)
	if strings.Join(reasons, ",") != "available_accounts_low" {
		t.Fatalf("threshold-only reasons = %#v", reasons)
	}
}

func TestInspectionNotificationPreviewReportsStateRuleVariables(t *testing.T) {
	policy := defaultInspectionPolicy()
	policy.Enabled = true
	policy.NotificationPolicies = []InspectionNotificationPolicy{stateRulePolicy(
		InspectionNotificationStateRule{Match: PolicyStateMatchReasonCode, Value: "token_revoked"},
	)}
	policy.NotificationEndpoints = []InspectionNotificationEndpoint{{
		ID: "state-endpoint", URL: "https://notify.example/hook?rule=${state_rule}&matched=${matched_accounts}",
		Enabled: true, NotificationPolicyID: "state-watch",
	}}

	host := &fakeAuthHost{entries: []cpaapi.HostAuthFileEntry{
		{AuthIndex: "revoked", Name: "revoked.json", Provider: "codex", Source: "file", Path: "/auths/revoked.json"},
		{AuthIndex: "revoked-too", Name: "revoked-too.json", Provider: "codex", Source: "file", Path: "/auths/revoked-too.json"},
	}}
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	engine.notificationDoer = anomalyNotificationDoerFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("preview must not send")
	})
	engine.Configure(Config{DataDir: t.TempDir(), InspectionPolicy: &policy})
	t.Cleanup(engine.Shutdown)
	engine.records = map[string]inspectionRecord{
		"revoked": {
			Result: InspectionResult{ID: "revoked", Health: InspectionHealthInvalidCredentials, ReasonCode: "token_revoked"},
		},
		"revoked-too": {
			Result: InspectionResult{ID: "revoked-too", Health: InspectionHealthInvalidCredentials, ReasonCode: "token_revoked"},
		},
	}

	preview, errPreview := engine.PreviewNotification(context.Background(), InspectionNotificationRequest{
		URLTemplate:                  "https://notify.example/hook?rule=${state_rule}&matched=${matched_accounts}",
		Scenario:                     InspectionNotificationScenarioManualTest,
		ThresholdPercent:             50,
		AvailableAccountsThreshold:   1,
		AvailabilityPercentThreshold: 1,
		NotificationPolicyID:         "state-watch",
	})
	if errPreview != nil {
		t.Fatalf("PreviewNotification() error = %v", errPreview)
	}
	if got, want := preview.Variables["state_rule"], "reason_code=token_revoked"; got != want {
		t.Errorf("state_rule = %q, want %q", got, want)
	}
	if got, want := preview.Variables["matched_accounts"], "2"; got != want {
		t.Errorf("matched_accounts = %q, want %q", got, want)
	}
	parsed, errParse := url.Parse(preview.ExpandedURL)
	if errParse != nil {
		t.Fatalf("parse preview URL: %v", errParse)
	}
	if got, want := parsed.Query().Get("rule"), "reason_code=token_revoked"; got != want {
		t.Errorf("expanded rule = %q, want %q", got, want)
	}
}

// A status code typed as "0429" or "+429" is numerically equal to 429 but not
// textually equal, so it must be canonicalized before it is stored or matched.
// Otherwise the rule saves successfully and then never fires.
func TestInspectionNotificationStateStatusCodeIsCanonicalizedBeforeMatching(t *testing.T) {
	policy := stateRulePolicy(InspectionNotificationStateRule{Match: PolicyStateMatchStatusCode, Value: " 0429 "})
	validated, errValidate := validateInspectionNotificationPolicies([]InspectionNotificationPolicy{policy})
	if errValidate != nil {
		t.Fatalf("a padded status code was rejected: %v", errValidate)
	}
	if got, want := validated[0].StateRules[0].Value, "429"; got != want {
		t.Fatalf("normalized status code = %q, want %q", got, want)
	}

	accounts := map[string]Account{"limited": {ID: "limited", Provider: "codex"}}
	records := map[string]inspectionRecord{
		"limited": {Result: InspectionResult{ID: "limited", Health: InspectionHealthUnavailable, StatusCode: 429}},
	}
	evaluation := inspectionNotificationStateEvaluationFor(validated[0], accounts, records)
	if evaluation.Accounts != 1 || len(evaluation.Reasons) != 1 {
		t.Fatalf("canonicalized status rule did not match: %#v", evaluation)
	}

	// A value that reaches the matcher without canonicalization still matches by
	// numeric value, so a hand-edited state file cannot silently stop firing.
	if !inspectionNotificationStateRuleMatches(
		InspectionNotificationStateRule{Match: PolicyStateMatchStatusCode, Value: "0429"},
		accounts["limited"], records["limited"], true,
	) {
		t.Fatal("a non-canonical status code did not match numerically")
	}
}

// An unusable persisted state rule must cost only that rule. Failing the load
// makes the engine fall back to a fresh default state and write it back over the
// file, which discards every record and cooldown.
func TestInspectionStateLoadDropsUnusableStateRulesWithoutLosingRecords(t *testing.T) {
	storePath := inspectionStorePath(t.TempDir())
	policy := defaultInspectionPolicy()
	policy.NotificationPolicies = []InspectionNotificationPolicy{stateRulePolicy(
		InspectionNotificationStateRule{Match: PolicyStateMatchHealth, Value: "invalid_credentials"},
		// A target a different build knew about, and a value this build rejects.
		InspectionNotificationStateRule{Match: "retired_target", Value: "whatever"},
		InspectionNotificationStateRule{Match: PolicyStateMatchStatusCode, Value: "nonsense"},
	)}
	policy.NotificationEndpoints = []InspectionNotificationEndpoint{{
		ID: "state-endpoint", URL: "https://notify.example/hook",
		Enabled: true, NotificationPolicyID: "state-watch",
	}}
	state := persistedInspectionState{
		Version: inspectionStoreVersion,
		Policy:  policy,
		Records: map[string]inspectionRecord{
			"kept": {Result: InspectionResult{ID: "kept", Health: InspectionHealthInvalidCredentials, ReasonCode: "invalid_credentials"}},
		},
	}
	if errSave := saveInspectionState(storePath, state); errSave != nil {
		t.Fatalf("save inspection state: %v", errSave)
	}

	loaded, errLoad := loadInspectionState(storePath)
	if errLoad != nil {
		t.Fatalf("an unusable persisted state rule failed the whole load: %v", errLoad)
	}
	if _, exists := loaded.Records["kept"]; !exists {
		t.Fatal("inspection records were discarded by the load")
	}
	rules := loaded.Policy.NotificationPolicies[0].StateRules
	if len(rules) != 1 || rules[0].Match != PolicyStateMatchHealth {
		t.Fatalf("unusable persisted state rules were not dropped: %#v", rules)
	}

	// The API path stays strict so an operator still sees a typo as an error.
	for name, rule := range map[string]InspectionNotificationStateRule{
		"unknown target": {Match: "retired_target", Value: "whatever"},
		"bad status":     {Match: PolicyStateMatchStatusCode, Value: "nonsense"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, errStrict := validateInspectionNotificationPolicies([]InspectionNotificationPolicy{stateRulePolicy(rule)}); errStrict == nil {
				t.Fatalf("the API validation path accepted %#v", rule)
			}
		})
	}
}

package manager

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestConditionalPolicyNormalizesNestedConditionsAndActions(t *testing.T) {
	websockets := false
	priority := 9
	probe := true
	policy, errValidate := validateDefaultPolicy(DefaultPolicy{
		ConditionalRules: []ConditionalPolicyRule{{
			ID: " free-codex ", Name: " Codex Free ", Enabled: true, Priority: 100,
			Conditions: PolicyConditionGroup{Operator: "ALL", Conditions: []PolicyCondition{
				{Field: "provider", Value: " CODEX_AGENT_IDENTITY "},
			}, Groups: []PolicyConditionGroup{{Operator: "any", Conditions: []PolicyCondition{
				{Field: "account_type", Value: " Free "},
				{Field: "email_suffix", Value: "@Example.COM"},
			}}}},
			Actions: ConditionalPolicyActions{
				NewAccountModelProbe: &probe, Priority: &priority, Websockets: &websockets,
				ModelPolicy: &ModelPolicyPatch{Mode: ModelPolicyModeAllowOnly, Models: []string{"gpt-5.5", "GPT-5.5"}},
			},
		}},
	})
	if errValidate != nil {
		t.Fatalf("validateDefaultPolicy() error = %v", errValidate)
	}
	rule := policy.ConditionalRules[0]
	if rule.ID != "free-codex" || rule.Name != "Codex Free" || rule.Conditions.Operator != PolicyConditionAll {
		t.Fatalf("normalized rule = %#v", rule)
	}
	if got := rule.Conditions.Conditions[0].Value; got != "codex" {
		t.Fatalf("provider = %q, want codex", got)
	}
	if got := rule.Conditions.Groups[0].Conditions[0].Value; got != "free" {
		t.Fatalf("account type = %q, want free", got)
	}
	if got := rule.Conditions.Groups[0].Conditions[1].Value; got != "example.com" {
		t.Fatalf("email suffix = %q, want example.com", got)
	}
	if got := rule.Actions.ModelPolicy.Models; len(got) != 1 || got[0] != "gpt-5.5" {
		t.Fatalf("models = %#v", got)
	}
}

func TestConditionalPolicyRejectsUnsafeOrUnboundedTrees(t *testing.T) {
	websockets := true
	tests := []struct {
		name string
		rule ConditionalPolicyRule
	}{
		{name: "missing condition", rule: ConditionalPolicyRule{ID: "empty", Enabled: true, Actions: ConditionalPolicyActions{Websockets: &websockets}}},
		{name: "missing action", rule: ConditionalPolicyRule{ID: "no-action", Enabled: true, Conditions: providerCondition("codex")}},
		{name: "unsafe suffix", rule: ConditionalPolicyRule{ID: "bad-domain", Enabled: true, Conditions: PolicyConditionGroup{Operator: PolicyConditionAll, Conditions: []PolicyCondition{{Field: PolicyConditionEmailSuffix, Value: "example.com/path"}}}, Actions: ConditionalPolicyActions{Websockets: &websockets}}},
		{name: "unknown field", rule: ConditionalPolicyRule{ID: "unknown", Enabled: true, Conditions: PolicyConditionGroup{Operator: PolicyConditionAll, Conditions: []PolicyCondition{{Field: "token", Value: "secret"}}}, Actions: ConditionalPolicyActions{Websockets: &websockets}}},
		{name: "too deep", rule: ConditionalPolicyRule{ID: "deep", Enabled: true, Conditions: nestedPolicyCondition(maxPolicyConditionDepth + 1), Actions: ConditionalPolicyActions{Websockets: &websockets}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, errValidate := validateDefaultPolicy(DefaultPolicy{ConditionalRules: []ConditionalPolicyRule{test.rule}}); errValidate == nil {
				t.Fatal("validateDefaultPolicy() unexpectedly succeeded")
			}
		})
	}
}

func TestResolveConditionalPolicyMergesMatchingRulesByPriority(t *testing.T) {
	globalWebsockets := false
	providerWebsockets := true
	freeWebsockets := false
	globalPriority := 1
	freePriority := 8
	probe := true
	policy, errValidate := validateDefaultPolicy(DefaultPolicy{
		Enabled: true, Priority: &globalPriority, Websockets: &globalWebsockets,
		ConditionalRules: []ConditionalPolicyRule{
			{ID: "codex", Enabled: true, Priority: 10, Conditions: providerCondition("codex"), Actions: ConditionalPolicyActions{Websockets: &providerWebsockets}},
			{ID: "free", Enabled: true, Priority: 20, Conditions: PolicyConditionGroup{Operator: PolicyConditionAll, Conditions: []PolicyCondition{{Field: PolicyConditionProvider, Value: "codex"}, {Field: PolicyConditionAccountType, Value: "free"}}}, Actions: ConditionalPolicyActions{Priority: &freePriority, Websockets: &freeWebsockets, NewAccountModelProbe: &probe, ModelPolicy: &ModelPolicyPatch{Mode: ModelPolicyModeAllowOnly, Models: []string{"gpt-5.5"}}}},
		},
	})
	if errValidate != nil {
		t.Fatalf("validateDefaultPolicy() error = %v", errValidate)
	}

	resolved := resolveConditionalPolicy(policy, Account{Provider: agentIdentityProvider, PlanType: "free", Email: "person@example.com"})
	if resolved.Priority == nil || *resolved.Priority != freePriority || resolved.Websockets == nil || *resolved.Websockets {
		t.Fatalf("resolved actions = %#v", resolved)
	}
	if resolved.NewAccountModelProbe == nil || !*resolved.NewAccountModelProbe || resolved.ModelPolicy == nil || resolved.ModelPolicy.Mode != ModelPolicyModeAllowOnly {
		t.Fatalf("resolved conditional actions = %#v", resolved)
	}

	plus := resolveConditionalPolicy(policy, Account{Provider: "codex", PlanType: "plus", Email: "person@elsewhere.test"})
	if plus.Priority == nil || *plus.Priority != globalPriority || plus.Websockets == nil || !*plus.Websockets || plus.ModelPolicy != nil {
		t.Fatalf("broad Codex actions = %#v", plus)
	}
}

func TestConditionalPolicyNestedAnyAndEmailSuffixMatching(t *testing.T) {
	websockets := true
	policy, errValidate := validateDefaultPolicy(DefaultPolicy{ConditionalRules: []ConditionalPolicyRule{{
		ID: "nested", Enabled: true, Conditions: PolicyConditionGroup{Operator: PolicyConditionAll,
			Conditions: []PolicyCondition{{Field: PolicyConditionProvider, Value: "codex"}},
			Groups: []PolicyConditionGroup{{Operator: PolicyConditionAny, Conditions: []PolicyCondition{
				{Field: PolicyConditionAccountType, Value: "team"},
				{Field: PolicyConditionEmailSuffix, Value: "school.edu"},
			}}},
		}, Actions: ConditionalPolicyActions{Websockets: &websockets},
	}}})
	if errValidate != nil {
		t.Fatalf("validateDefaultPolicy() error = %v", errValidate)
	}
	for _, account := range []Account{
		{Provider: "codex", PlanType: "team", Email: "user@example.com"},
		{Provider: "codex", PlanType: "free", Email: "user@dept.school.edu"},
	} {
		if !conditionalPolicyGroupMatches(policy.ConditionalRules[0].Conditions, account) {
			t.Fatalf("account did not match: %#v", account)
		}
	}
	if conditionalPolicyGroupMatches(policy.ConditionalRules[0].Conditions, Account{Provider: "codex", PlanType: "free", Email: "user@example.com"}) {
		t.Fatal("unexpected nested condition match")
	}
}

func TestPolicyEngineAppliesConditionalFieldsAndModelPolicy(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "free", Name: "free.json", Provider: "codex", Type: "oauth", Email: "user@school.edu",
			Source: "file", Path: "/auths/free.json", Size: 128, ModTime: time.Now().UTC(),
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"free": {AuthIndex: "free", Name: "free.json", Path: "/auths/free.json", JSON: json.RawMessage(`{"type":"codex","id_token":{"plan_type":"free"}}`)},
		},
	}
	engine := NewPolicyEngine(host)
	engine.Configure(Config{DataDir: t.TempDir()})
	defer engine.Shutdown()

	var mu sync.Mutex
	modelCalls := 0
	engine.SetModelPolicyApplier(func(_ context.Context, account Account, patch ModelPolicyPatch, key string) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		modelCalls++
		if account.ID != "free" || account.PlanType != "free" || key != "management-secret" || patch.Mode != ModelPolicyModeAllowOnly {
			t.Fatalf("conditional model application = account:%#v patch:%#v key:%q", account, patch, key)
		}
		return true, nil
	})
	engine.Arm("management-secret")
	websockets := false
	priority := 12
	if _, errSet := engine.SetPolicy(DefaultPolicy{ConditionalRules: []ConditionalPolicyRule{{
		ID: "free-codex", Enabled: true, Priority: 50,
		Conditions: PolicyConditionGroup{Operator: PolicyConditionAll, Conditions: []PolicyCondition{
			{Field: PolicyConditionProvider, Value: "codex"},
			{Field: PolicyConditionAccountType, Value: "free"},
			{Field: PolicyConditionEmailSuffix, Value: "school.edu"},
		}},
		Actions: ConditionalPolicyActions{Priority: &priority, Websockets: &websockets, ModelPolicy: &ModelPolicyPatch{Mode: ModelPolicyModeAllowOnly, Models: []string{"gpt-5.5"}}},
	}}}); errSet != nil {
		t.Fatalf("SetPolicy() error = %v", errSet)
	}
	engine.RequestScan()
	waitForPolicy(t, engine, func(snapshot PolicySnapshot) bool {
		return snapshot.LastScan.Changed == 1 && snapshot.LastScan.Failed == 0
	})
	host.mu.Lock()
	updated := append(json.RawMessage(nil), host.details["free"].JSON...)
	host.mu.Unlock()
	var document map[string]any
	if errDecode := json.Unmarshal(updated, &document); errDecode != nil {
		t.Fatalf("decode updated auth: %v", errDecode)
	}
	if document[policyFieldPriority] != float64(priority) || document[policyFieldWebsockets] != false {
		t.Fatalf("conditional fields = %#v", document)
	}
	mu.Lock()
	defer mu.Unlock()
	if modelCalls < 1 {
		t.Fatalf("model policy calls = %d, want at least 1", modelCalls)
	}
}

func providerCondition(provider string) PolicyConditionGroup {
	return PolicyConditionGroup{Operator: PolicyConditionAll, Conditions: []PolicyCondition{{Field: PolicyConditionProvider, Value: provider}}}
}

func nestedPolicyCondition(depth int) PolicyConditionGroup {
	if depth <= 1 {
		return providerCondition("codex")
	}
	return PolicyConditionGroup{Operator: PolicyConditionAll, Groups: []PolicyConditionGroup{nestedPolicyCondition(depth - 1)}}
}

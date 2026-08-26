package manager

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type aiProviderProxyTestResolver struct {
	profiles map[string]string
	scopes   map[string][]string
}

func (r aiProviderProxyTestResolver) ProxyURLByID(id string) (string, bool) {
	value, ok := r.profiles[id]
	return value, ok
}

func (r aiProviderProxyTestResolver) ProxyURLForProvider(id, provider string) (string, bool) {
	value, ok := r.profiles[id]
	if !ok {
		return "", false
	}
	allowed := r.scopes[id]
	if len(allowed) == 0 {
		return value, true
	}
	provider = aiProviderPolicyFamily(provider)
	for _, candidate := range allowed {
		if aiProviderPolicyFamily(candidate) == provider {
			return value, true
		}
	}
	return "", false
}

func TestResolveAIProviderProxyPolicyUsesProviderFamilyAndConditionalOverride(t *testing.T) {
	defaultID, conditionalID := "default", "codex-only"
	resolver := aiProviderProxyTestResolver{
		profiles: map[string]string{defaultID: "http://default.proxy", conditionalID: "socks5://conditional.proxy"},
		scopes:   map[string][]string{conditionalID: {"codex"}},
	}
	policy := normalizeDefaultPolicy(DefaultPolicy{
		Enabled:                  true,
		AIProviderProxyProfileID: &defaultID,
		ConditionalRules: []ConditionalPolicyRule{{
			ID: "codex", Enabled: true, Priority: 10,
			Conditions: PolicyConditionGroup{Operator: PolicyConditionAll, Conditions: []PolicyCondition{{Field: PolicyConditionProvider, Value: "codex"}}},
			Actions:    ConditionalPolicyActions{AIProviderProxyProfileID: &conditionalID},
		}},
	})

	proxyURL, mode, ok, errResolve := resolveAIProviderProxyPolicy(policy, resolver, "codex-api-key")
	if errResolve != nil || !ok || mode != applyForce || proxyURL != "socks5://conditional.proxy" {
		t.Fatalf("codex resolution = %q, %d, %v, %v", proxyURL, mode, ok, errResolve)
	}
	proxyURL, mode, ok, errResolve = resolveAIProviderProxyPolicy(policy, resolver, "claude-api-key")
	if errResolve != nil || !ok || mode != applyMissing || proxyURL != "http://default.proxy" {
		t.Fatalf("claude resolution = %q, %d, %v, %v", proxyURL, mode, ok, errResolve)
	}
}

func TestResolveAIProviderProxyPolicySkipsOutOfScopeProfile(t *testing.T) {
	profileID := "codex-only"
	resolver := aiProviderProxyTestResolver{
		profiles: map[string]string{profileID: "http://proxy.internal"},
		scopes:   map[string][]string{profileID: {"codex"}},
	}
	policy := normalizeDefaultPolicy(DefaultPolicy{Enabled: true, AIProviderProxyProfileID: &profileID})
	if proxyURL, _, ok, errResolve := resolveAIProviderProxyPolicy(policy, resolver, "claude-api-key"); errResolve != nil || ok || proxyURL != "" {
		t.Fatalf("out-of-scope resolution = %q, %v, %v", proxyURL, ok, errResolve)
	}
}

func TestManagementClientPatchesAIProviderProxyWithoutLeakingOrReplacingCredentials(t *testing.T) {
	requests := make([]map[string]any, 0, 2)
	doer := httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodGet {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"openai-compatibility":[{"name":"one","api-key-entries":[{"api-key":"secret-one","weight":75,"future-field":"keep"},{"api-key":"secret-two","proxy-url":"http://existing.proxy"}]}]}`))}, nil
		}
		var body map[string]any
		if errDecode := json.NewDecoder(request.Body).Decode(&body); errDecode != nil {
			t.Fatalf("decode patch: %v", errDecode)
		}
		body["path"] = request.URL.Path
		requests = append(requests, body)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"status":"ok"}`))}, nil
	})
	client, errClient := newManagementClient("http://127.0.0.1:8317", "management-secret", doer)
	if errClient != nil {
		t.Fatalf("newManagementClient() error = %v", errClient)
	}
	entries, supported, errList := client.getAIProviderChannel(t.Context(), "openai-compatibility")
	if errList != nil || !supported || len(entries) != 1 {
		t.Fatalf("getAIProviderChannel() = %#v, %v, %v", entries, supported, errList)
	}
	changed, errPatch := client.patchAIProviderProxy(t.Context(), "openai-compatibility", 0, entries[0], "socks5://new.proxy", applyMissing)
	if errPatch != nil || !changed || len(requests) != 1 {
		t.Fatalf("patchAIProviderProxy() changed=%v requests=%d err=%v", changed, len(requests), errPatch)
	}
	value := requests[0]["value"].(map[string]any)
	keyEntries := value["api-key-entries"].([]any)
	first := keyEntries[0].(map[string]any)
	second := keyEntries[1].(map[string]any)
	if first["api-key"] != "secret-one" || first["future-field"] != "keep" || first["proxy-url"] != "socks5://new.proxy" {
		t.Fatalf("first key entry was not preserved: %#v", first)
	}
	if second["api-key"] != "secret-two" || second["proxy-url"] != "http://existing.proxy" {
		t.Fatalf("existing proxy was overwritten in missing mode: %#v", second)
	}
}

func TestManagementClientTreatsEmptyProxyAsMissing(t *testing.T) {
	requests := make([]map[string]any, 0, 2)
	doer := httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if errDecode := json.NewDecoder(request.Body).Decode(&body); errDecode != nil {
			t.Fatalf("decode patch: %v", errDecode)
		}
		requests = append(requests, body)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"status":"ok"}`))}, nil
	})
	client, errClient := newManagementClient("http://127.0.0.1:8317", "management-secret", doer)
	if errClient != nil {
		t.Fatalf("newManagementClient() error = %v", errClient)
	}

	changed, errPatch := client.patchAIProviderProxy(t.Context(), "codex-api-key", 0, map[string]any{"proxy-url": ""}, "socks5://new.proxy", applyMissing)
	if errPatch != nil || !changed {
		t.Fatalf("empty generic proxy changed=%v err=%v", changed, errPatch)
	}
	changed, errPatch = client.patchAIProviderProxy(t.Context(), "openai-compatibility", 0, map[string]any{
		"api-key-entries": []any{map[string]any{"api-key": "secret", "proxy-url": ""}},
	}, "socks5://new.proxy", applyMissing)
	if errPatch != nil || !changed || len(requests) != 2 {
		t.Fatalf("empty compatibility proxy changed=%v requests=%d err=%v", changed, len(requests), errPatch)
	}
}

func TestManagementClientTreatsMissingAIProviderChannelAsUnsupported(t *testing.T) {
	client, errClient := newManagementClient("http://127.0.0.1:8317", "management-secret", httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`{"error":"not found"}`))}, nil
	}))
	if errClient != nil {
		t.Fatalf("newManagementClient() error = %v", errClient)
	}
	entries, supported, errList := client.getAIProviderChannel(t.Context(), "vertex-api-key")
	if errList != nil || supported || entries != nil {
		t.Fatalf("unsupported channel = %#v, %v, %v", entries, supported, errList)
	}
}

func TestDefaultPolicySeparatesAccountAndAIProviderActions(t *testing.T) {
	profileID := "provider-proxy"
	policy := normalizeDefaultPolicy(DefaultPolicy{Enabled: true, AIProviderProxyProfileID: &profileID})
	if policy.ManagesAccountFields() || !policy.ManagesAIProviderProxy() || !policy.ManagesFields() {
		t.Fatalf("policy action classification is incorrect: %#v", policy)
	}
}

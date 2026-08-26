package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

var aiProviderProxyPolicyKinds = []string{
	"openai-compatibility",
	"gemini-api-key",
	"interactions-api-key",
	"claude-api-key",
	"codex-api-key",
	"xai-api-key",
	"vertex-api-key",
}

func (a *App) applyAIProviderProxyPolicy(ctx context.Context, policy DefaultPolicy, resolver ProxyProfileResolver, managementKey string) (int, error) {
	if a == nil {
		return 0, fmt.Errorf("AI provider proxy service is unavailable")
	}
	client, errClient := newManagementClient(resolveManagementBaseURL(a.configSnapshot().ManagementBaseURL), managementKey, a.managementDoer)
	if errClient != nil {
		return 0, fmt.Errorf("AI provider proxy management client is unavailable")
	}
	defer client.clearSecrets()

	changed := 0
	failures := 0
	for _, kind := range aiProviderProxyPolicyKinds {
		if ctx.Err() != nil {
			return changed, ctx.Err()
		}
		entries, supported, errList := client.getAIProviderChannel(ctx, kind)
		if errList != nil {
			failures++
			continue
		}
		if !supported {
			continue
		}
		for index, entry := range entries {
			desired, mode, ok, errResolve := resolveAIProviderProxyPolicy(policy, resolver, kind)
			if errResolve != nil {
				failures++
				continue
			}
			if !ok {
				continue
			}
			entryChanged, errPatch := client.patchAIProviderProxy(ctx, kind, index, entry, desired, mode)
			if errPatch != nil {
				failures++
				continue
			}
			if entryChanged {
				changed++
			}
		}
	}
	if failures > 0 {
		return changed, fmt.Errorf("AI provider proxy policy failed for %d provider entries", failures)
	}
	return changed, nil
}

func resolveAIProviderProxyPolicy(policy DefaultPolicy, resolver ProxyProfileResolver, kind string) (string, policyApplyMode, bool, error) {
	if resolver == nil {
		return "", applyMissing, false, fmt.Errorf("proxy profile resolver is unavailable")
	}
	provider := aiProviderPolicyFamily(kind)
	desired := ""
	mode := applyMissing
	resolvedAny := false
	if policy.Enabled && policy.AIProviderProxyProfileID != nil {
		proxyURL, matches, errResolve := resolveAIProviderScopedProxy(resolver, *policy.AIProviderProxyProfileID, provider)
		if errResolve != nil {
			return "", mode, false, errResolve
		}
		if matches {
			desired, resolvedAny = proxyURL, true
		}
	}
	resolved := resolveConditionalPolicy(policy, Account{Provider: provider, Type: provider, AccountType: "api-key"})
	if resolved.AIProviderProxyFromRule && resolved.AIProviderProxyProfileID != nil {
		proxyURL, matches, errResolve := resolveAIProviderScopedProxy(resolver, *resolved.AIProviderProxyProfileID, provider)
		if errResolve != nil {
			return "", mode, false, errResolve
		}
		if matches {
			desired, resolvedAny, mode = proxyURL, true, applyForce
		}
	}
	return desired, mode, resolvedAny, nil
}

func resolveAIProviderScopedProxy(resolver ProxyProfileResolver, profileID, provider string) (string, bool, error) {
	if _, exists := resolver.ProxyURLByID(profileID); !exists {
		return "", false, fmt.Errorf("proxy profile is unavailable")
	}
	proxyURL, matches := resolveProxyProfileForProvider(resolver, profileID, provider)
	return proxyURL, matches, nil
}

func aiProviderPolicyFamily(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if strings.HasSuffix(kind, "-api-key") {
		return strings.TrimSuffix(kind, "-api-key")
	}
	return kind
}

func (c *managementClient) getAIProviderChannel(ctx context.Context, kind string) ([]map[string]any, bool, error) {
	if !supportedAIProviderProxyKind(kind) {
		return nil, false, nil
	}
	var response map[string]json.RawMessage
	if errRequest := c.requestJSON(ctx, http.MethodGet, "/v0/management/"+kind, nil, "", &response); errRequest != nil {
		var statusError *managementHTTPStatusError
		if errors.As(errRequest, &statusError) && statusError.StatusCode == http.StatusNotFound {
			return nil, false, nil
		}
		return nil, true, fmt.Errorf("AI provider channel %s could not be loaded", kind)
	}
	raw, ok := response[kind]
	if !ok {
		return nil, true, fmt.Errorf("AI provider channel %s returned an invalid response", kind)
	}
	var entries []map[string]any
	if errDecode := json.Unmarshal(raw, &entries); errDecode != nil {
		return nil, true, fmt.Errorf("AI provider channel %s returned an invalid response", kind)
	}
	return entries, true, nil
}

func (c *managementClient) patchAIProviderProxy(ctx context.Context, kind string, index int, entry map[string]any, proxyURL string, mode policyApplyMode) (bool, error) {
	if kind == "openai-compatibility" {
		return c.patchOpenAICompatibilityProxy(ctx, index, entry, proxyURL, mode)
	}
	current, _ := entry["proxy-url"].(string)
	if mode == applyMissing && strings.TrimSpace(current) != "" {
		return false, nil
	}
	if strings.TrimSpace(current) == strings.TrimSpace(proxyURL) {
		return false, nil
	}
	if errPatch := c.patch(ctx, "/v0/management/"+kind, map[string]any{
		"index": index,
		"value": map[string]any{"proxy-url": proxyURL},
	}); errPatch != nil {
		return false, fmt.Errorf("AI provider channel %s proxy update failed", kind)
	}
	return true, nil
}

func (c *managementClient) patchOpenAICompatibilityProxy(ctx context.Context, index int, entry map[string]any, proxyURL string, mode policyApplyMode) (bool, error) {
	rawEntries, ok := entry["api-key-entries"].([]any)
	if !ok || len(rawEntries) == 0 {
		return false, nil
	}
	changed := false
	updated := make([]any, len(rawEntries))
	for entryIndex, rawEntry := range rawEntries {
		keyEntry, okMap := rawEntry.(map[string]any)
		if !okMap {
			updated[entryIndex] = rawEntry
			continue
		}
		cloned := make(map[string]any, len(keyEntry)+1)
		for key, value := range keyEntry {
			cloned[key] = value
		}
		current, _ := cloned["proxy-url"].(string)
		if mode == applyMissing && strings.TrimSpace(current) != "" {
			updated[entryIndex] = cloned
			continue
		}
		if strings.TrimSpace(current) == strings.TrimSpace(proxyURL) {
			updated[entryIndex] = cloned
			continue
		}
		cloned["proxy-url"] = proxyURL
		updated[entryIndex] = cloned
		changed = true
	}
	if !changed {
		return false, nil
	}
	if errPatch := c.patch(ctx, "/v0/management/openai-compatibility", map[string]any{
		"index": index,
		"value": map[string]any{"api-key-entries": updated},
	}); errPatch != nil {
		return false, fmt.Errorf("AI provider channel openai-compatibility proxy update failed")
	}
	return true, nil
}

func supportedAIProviderProxyKind(kind string) bool {
	for _, supported := range aiProviderProxyPolicyKinds {
		if kind == supported {
			return true
		}
	}
	return false
}

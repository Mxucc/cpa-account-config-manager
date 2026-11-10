package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// A community provider's model test.
//
// Two facts decide whether a channel is usable, and they fail independently:
//
//   - the credential and the endpoint answer at all, and
//   - CPA itself routes a request for that model to this channel.
//
// The second one is not a formality. A model id such as "glm-5.3" is published by
// many channels, so CPA can send a request to somebody else's row and fail with a
// retry exhaustion that looks exactly like a broken credential. This file tests
// both, so the answer says which one is wrong.
//
// Neither test proxies user traffic: the plugin makes one bounded request of its own
// and reports what came back.

const (
	// communityTestTimeout bounds one test request. A reasoning model can take a
	// while to produce its first bytes, so this is generous but still bounded.
	communityTestTimeout = 60 * time.Second
	// communityTestReadBytes is how much of a streaming answer is read. One SSE
	// chunk is enough to prove the upstream answered.
	communityTestReadBytes = 16 << 10
)

// communityTestResult is one leg of the test.
type communityTestResult struct {
	// OK is true only when the request answered 2xx with a body.
	OK bool `json:"ok"`
	// Status is the HTTP status, 0 when the request never completed.
	Status int `json:"status"`
	// LatencyMS is the time to the first response byte.
	LatencyMS int64 `json:"latency_ms"`
	// Model is the model id that was asked for.
	Model string `json:"model,omitempty"`
	// Reason explains a failure in operator terms, never echoing a credential.
	Reason string `json:"reason,omitempty"`
}

// communityChannelTest is the whole answer.
type communityChannelTest struct {
	Provider string `json:"provider"`
	// Prefix is the namespace CPA publishes this provider's models under.
	Prefix string `json:"prefix,omitempty"`
	// Callable is the model id a client must ask CPA for.
	Callable string `json:"callable,omitempty"`
	// Upstream tests the credential against the provider's own endpoint.
	Upstream communityTestResult `json:"upstream"`
	// Routed tests CPA's own entry point for the callable model id.
	Routed communityTestResult `json:"routed"`
	// Hint is what to do about a failure, when there is one.
	Hint string `json:"hint,omitempty"`
}

// communityChannelPrefix is the namespace CPA publishes a provider's models under.
// It is the provider id, which is already a slug, so two providers can never share
// a model namespace and a colliding plain id cannot take the request.
func communityChannelPrefix(providerID string) string {
	provider := strings.ToLower(strings.TrimSpace(providerID))
	replacer := strings.NewReplacer(" ", "-", "_", "-", "/", "-", ":", "-")
	provider = replacer.Replace(provider)
	provider = strings.Trim(provider, "-")
	if len(provider) > 32 {
		provider = provider[:32]
	}
	return provider
}

// communityCallableModel is the id a client asks CPA for.
func communityCallableModel(providerID, model string) string {
	prefix := communityChannelPrefix(providerID)
	trimmed := strings.TrimSpace(model)
	if prefix == "" || trimmed == "" {
		return trimmed
	}
	return prefix + "/" + trimmed
}

// communityOwnChannelCredential reads the base URL and credential of the channel
// this plugin published for a provider.
func (a *App) communityOwnChannelCredential(providerID string) (string, string, error) {
	managementKey := strings.TrimSpace(a.communityManagementKey())
	if managementKey == "" {
		return "", "", fmt.Errorf("a management key is required to read the channel")
	}
	client, errClient := a.newWriteManagementClient(managementKey)
	if errClient != nil {
		return "", "", errClient
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	entries, supported, errRead := client.getAIProviderChannel(ctx, "openai-compatibility")
	if errRead != nil {
		return "", "", fmt.Errorf("read the channel list: %w", errRead)
	}
	if !supported {
		return "", "", fmt.Errorf("this host does not accept OpenAI-compatible channels")
	}
	own := a.communityOwnChannelRows(providerID, entries)
	for index, entry := range entries {
		if !own[index] {
			continue
		}
		baseURL := strings.TrimRight(strings.TrimSpace(aiProviderChannelBaseURL(entry)), "/")
		credential := aiProviderChannelCredential(entry)
		if baseURL != "" && credential != "" {
			return baseURL, credential, nil
		}
	}
	return "", "", fmt.Errorf("no channel of this provider was found; sign in once to create it")
}

// communityTestClient is the HTTP client the tests use. It is separate from the
// management client so a slow provider cannot hold a management call open.
func (a *App) communityTestClient() *http.Client {
	if a != nil && a.managementDoer != nil {
		if client, ok := a.managementDoer.(*http.Client); ok && client != nil {
			return client
		}
	}
	return &http.Client{Timeout: communityTestTimeout}
}

// testCommunityUpstream asks the provider's own endpoint for one short streamed
// answer. The request shape is the one these providers require: a system message
// first and a streaming body.
func (a *App) testCommunityUpstream(ctx context.Context, baseURL, credential, model string) communityTestResult {
	result := communityTestResult{Model: model}
	payload, errMarshal := json.Marshal(map[string]any{
		"model":  model,
		"stream": true,
		"messages": []map[string]any{
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "ping"},
		},
		"max_tokens": 1,
	})
	if errMarshal != nil {
		result.Reason = "the test request could not be encoded"
		return result
	}
	requestCtx, cancel := context.WithTimeout(ctx, communityTestTimeout)
	defer cancel()
	request, errRequest := http.NewRequestWithContext(requestCtx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(payload))
	if errRequest != nil {
		result.Reason = "the test request could not be created"
		return result
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Authorization", "Bearer "+credential)
	started := time.Now()
	response, errDo := a.communityTestClient().Do(request)
	if errDo != nil {
		result.Reason = classifyCommunityTestError(errDo)
		return result
	}
	if response == nil || response.Body == nil {
		result.Reason = "the provider answered with no body"
		return result
	}
	defer func() {
		_ = response.Body.Close()
	}()
	result.Status = response.StatusCode
	body, _ := io.ReadAll(io.LimitReader(response.Body, communityTestReadBytes))
	result.LatencyMS = time.Since(started).Milliseconds()
	result.OK = response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices && len(bytes.TrimSpace(body)) > 0
	if !result.OK {
		result.Reason = communityUpstreamReason(body, response.StatusCode)
	}
	return result
}

// testCommunityRouted asks CPA's own entry point for the callable model id, which is
// the leg that proves the channel is actually reached when a client calls.
func (a *App) testCommunityRouted(ctx context.Context, providerID, model string) communityTestResult {
	callable := communityCallableModel(providerID, model)
	result := communityTestResult{Model: callable}
	clientKey := a.hostClientAPIKey(ctx)
	if clientKey == "" {
		result.Reason = "this host has no client API key, so the routed leg cannot be tested"
		return result
	}
	baseURL := strings.TrimRight(strings.TrimSpace(resolveManagementBaseURL(a.configSnapshot().ManagementBaseURL)), "/")
	if baseURL == "" {
		result.Reason = "the management base URL is unavailable"
		return result
	}
	payload, errMarshal := json.Marshal(map[string]any{
		"model":  callable,
		"stream": true,
		"messages": []map[string]any{
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "ping"},
		},
		"max_tokens": 1,
	})
	if errMarshal != nil {
		result.Reason = "the test request could not be encoded"
		return result
	}
	requestCtx, cancel := context.WithTimeout(ctx, communityTestTimeout)
	defer cancel()
	request, errRequest := http.NewRequestWithContext(requestCtx, http.MethodPost, baseURL+"/v1/chat/completions", bytes.NewReader(payload))
	if errRequest != nil {
		result.Reason = "the test request could not be created"
		return result
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Authorization", "Bearer "+clientKey)
	started := time.Now()
	response, errDo := a.communityTestClient().Do(request)
	if errDo != nil {
		result.Reason = classifyCommunityTestError(errDo)
		return result
	}
	if response == nil || response.Body == nil {
		result.Reason = "the gateway answered with no body"
		return result
	}
	defer func() {
		_ = response.Body.Close()
	}()
	result.Status = response.StatusCode
	body, _ := io.ReadAll(io.LimitReader(response.Body, communityTestReadBytes))
	result.LatencyMS = time.Since(started).Milliseconds()
	result.OK = response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices && len(bytes.TrimSpace(body)) > 0
	if !result.OK {
		result.Reason = communityGatewayReason(body, response.StatusCode)
	}
	return result
}

// handleCommunityTest answers the channel test for one provider.
func (a *App) handleCommunityTest(req cpaapi.ManagementRequest, method, path string) (cpaapi.ManagementResponse, bool) {
	if path != communityModelsRoutePath+"/test" {
		return cpaapi.ManagementResponse{}, false
	}
	if method != http.MethodPost && method != http.MethodGet {
		return jsonResponse(http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"}), true
	}
	providerID := strings.TrimSpace(firstQueryValue(req.Query, "provider"))
	if providerID == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "provider is required"}), true
	}
	if !a.isImportedProvider(providerID) {
		return jsonResponse(http.StatusBadRequest, map[string]any{
			"error":  "not_a_community_provider",
			"reason": "this provider's channels belong to CPA; test it through the account model route",
		}), true
	}
	a.rememberCommunityManagementKey(resolveManagementKey(req.Headers))
	model := strings.TrimSpace(firstQueryValue(req.Query, "model"))
	catalog := a.communityModelCatalog(providerID)
	if model == "" {
		if len(catalog) == 0 {
			return jsonResponse(http.StatusOK, communityChannelTest{
				Provider: providerID,
				Hint:     "this provider published no model catalog, so nothing can be tested",
			}), true
		}
		model = catalog[0]
	}
	ctx := context.Background()
	answer := communityChannelTest{
		Provider: providerID,
		Prefix:   communityChannelPrefix(providerID),
		Callable: communityCallableModel(providerID, model),
	}
	baseURL, credential, errChannel := a.communityOwnChannelCredential(providerID)
	if errChannel != nil {
		answer.Upstream = communityTestResult{Model: model, Reason: errChannel.Error()}
		answer.Routed = communityTestResult{Model: answer.Callable, Reason: errChannel.Error()}
		answer.Hint = "sign in once so this plugin creates the channel, then test again"
		return jsonResponse(http.StatusOK, answer), true
	}
	answer.Upstream = a.testCommunityUpstream(ctx, baseURL, credential, model)
	answer.Routed = a.testCommunityRouted(ctx, providerID, model)
	answer.Hint = communityTestHint(answer)
	return jsonResponse(http.StatusOK, answer), true
}

// communityTestHint turns the two results into the next action.
func communityTestHint(test communityChannelTest) string {
	switch {
	case test.Upstream.OK && test.Routed.OK:
		return ""
	case !test.Upstream.OK && !test.Routed.OK:
		return "the provider refused the credential itself: sign in again to refresh it"
	case !test.Upstream.OK:
		return "the credential works but the provider refused this model; pick another model"
	case !test.Routed.OK:
		return "the credential works, but the gateway did not reach this channel: another channel publishes the same model id, so call the prefixed name shown here"
	}
	return ""
}

// hostClientAPIKey reads one client API key this host accepts, which is what the
// routed leg authenticates with.
func (a *App) hostClientAPIKey(ctx context.Context) string {
	managementKey := strings.TrimSpace(a.communityManagementKey())
	if managementKey == "" || a == nil {
		return ""
	}
	client, errClient := newManagementClient(resolveManagementBaseURL(a.configSnapshot().ManagementBaseURL), managementKey, a.managementDoer)
	if errClient != nil {
		return ""
	}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var document map[string]json.RawMessage
	if errRead := client.requestJSON(requestCtx, http.MethodGet, "/v8/management/config", nil, "", &document); errRead != nil {
		return ""
	}
	raw, found := document["access"]
	if !found {
		return ""
	}
	var access struct {
		APIKeys []string `json:"api-keys"`
	}
	if errDecode := json.Unmarshal(raw, &access); errDecode != nil {
		return ""
	}
	for _, key := range access.APIKeys {
		if trimmed := strings.TrimSpace(key); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// classifyCommunityTestError keeps a transport failure readable.
func classifyCommunityTestError(err error) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "context deadline exceeded"), strings.Contains(message, "Client.Timeout"):
		return "the provider did not answer before the test timed out"
	case strings.Contains(message, "connection refused"):
		return "the provider refused the connection"
	case strings.Contains(message, "no such host"):
		return "the provider host could not be resolved"
	default:
		return "the request could not be completed"
	}
}

// communityUpstreamReason reads a provider's own error text without echoing a
// credential.
func communityUpstreamReason(body []byte, status int) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return fmt.Sprintf("the provider answered HTTP %d with no body", status)
	}
	var answer struct {
		Code    any    `json:"code"`
		Msg     string `json:"msg"`
		Message string `json:"message"`
		Error   any    `json:"error"`
	}
	if errDecode := json.Unmarshal(trimmed, &answer); errDecode == nil {
		text := firstNonEmptyText(answer.Msg, answer.Message)
		if text == "" {
			switch value := answer.Error.(type) {
			case string:
				text = value
			case map[string]any:
				if inner, ok := value["message"].(string); ok {
					text = inner
				}
			}
		}
		if text != "" {
			return truncateReason(fmt.Sprintf("the provider answered HTTP %d: %s", status, text))
		}
	}
	return fmt.Sprintf("the provider answered HTTP %d", status)
}

// communityGatewayReason reads the gateway's error envelope.
func communityGatewayReason(body []byte, status int) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return fmt.Sprintf("the gateway answered HTTP %d with no body", status)
	}
	var answer struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if errDecode := json.Unmarshal(trimmed, &answer); errDecode == nil && answer.Error.Message != "" {
		return truncateReason(fmt.Sprintf("the gateway answered HTTP %d: %s", status, answer.Error.Message))
	}
	return fmt.Sprintf("the gateway answered HTTP %d", status)
}

// truncateReason keeps an upstream sentence short enough for a table cell.
func truncateReason(text string) string {
	cleaned := strings.Join(strings.Fields(text), " ")
	if len(cleaned) > 220 {
		return cleaned[:220] + "…"
	}
	return cleaned
}

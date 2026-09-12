package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// OpenCode gateways are OpenAI-compatible, so CPA can route their models
// natively. Binding writes (or updates) one CPA OpenAI-compatible channel that
// points at the OpenCode base URL with the account credential and the OpenCode
// client headers, which is what makes the models reachable through CPA without a
// separate proxy process.

const (
	openCodeBoundChannelName = "OpenCode"
)

// OpenCodeBindingResult reports the CPA channel this account was bound to.
type OpenCodeBindingResult struct {
	Kind       string `json:"kind"`
	BaseURL    string `json:"base_url"`
	Index      int    `json:"index"`
	Created    bool   `json:"created"`
	ChannelKey string `json:"channel_key"`
	Models     int    `json:"models"`
}

// openCodeChannelHeaders are the headers the upstream expects from a client. CPA
// forwards configured channel headers, so the same identity used by the model
// probe is applied to routed traffic.
//
// The session header is included as a static baseline: OpenCode Go rejects a
// request that carries no x-opencode-session at all, and the plugin's request
// interceptor (which replaces this value with a per-conversation id whenever it
// runs) is only attached on hosts that support request interception. The baseline
// therefore keeps an older host routable instead of failing every request.
func openCodeChannelHeaders() map[string]string {
	return map[string]string{
		"x-opencode-client":  "cli",
		"x-opencode-session": openCodeChannelSessionBaseline,
		"User-Agent":         openCodeClientUserAgent(),
	}
}

// openCodeChannelSessionBaseline is the channel-level session id used until the
// request interceptor substitutes a per-conversation one.
const openCodeChannelSessionBaseline = "oc-cli-baseline"

// bindOpenCodeChannel writes one OpenAI-compatible CPA channel for an OpenCode
// credential. An existing channel with the same normalized base URL is updated in
// place so repeated binds are idempotent and keep unrelated fields untouched.
func (a *App) bindOpenCodeChannel(ctx context.Context, managementKey, baseURL, apiKey, label string, models []string) (OpenCodeBindingResult, error) {
	result := OpenCodeBindingResult{Kind: "openai-compatibility", BaseURL: openCodeAPIBase(baseURL) + "/v1", Index: -1}
	if a == nil {
		return result, fmt.Errorf("AI provider channel service is unavailable")
	}
	if strings.TrimSpace(managementKey) == "" {
		return result, fmt.Errorf("management key is unavailable")
	}
	if strings.TrimSpace(result.BaseURL) == "" || strings.TrimSpace(apiKey) == "" {
		return result, fmt.Errorf("an OpenCode base URL and API key are both required")
	}
	listKind := "openai-compatibility"
	entries, errRead := a.aiProviderChannelEntries(ctx, managementKey, listKind)
	if errRead != nil {
		return result, fmt.Errorf("CPA channels could not be read")
	}
	items := make([]map[string]any, 0, len(entries)+1)
	target := -1
	for index, entry := range entries {
		cloned := make(map[string]any, len(entry)+1)
		for key, value := range entry {
			cloned[key] = value
		}
		if target < 0 && canonicalProviderBaseURL(aiProviderChannelBaseURL(cloned)) == canonicalProviderBaseURL(result.BaseURL) {
			target = index
		}
		items = append(items, cloned)
	}
	label = strings.TrimSpace(label)
	if label == "" {
		label = openCodeBoundChannelName
	}
	if target < 0 {
		items = append(items, map[string]any{})
		target = len(items) - 1
		result.Created = true
	}
	entry := items[target]
	entry["base-url"] = result.BaseURL
	entry["name"] = label
	// The credential lives in the weighted key list; the legacy top-level field is
	// accepted by CPA's JSON decoder but ignored for OpenAI-compatible channels.
	entry["api-key-entries"] = mergeOpenCodeChannelKeyEntries(entry["api-key-entries"], apiKey)
	delete(entry, "api-key")
	headers, _ := entry["headers"].(map[string]any)
	if headers == nil {
		headers = map[string]any{}
	}
	for name, value := range openCodeChannelHeaders() {
		headers[name] = value
	}
	entry["headers"] = headers
	// Publish the verified catalog on the channel: CPA matches routed requests
	// against this list, so a channel without it cannot serve the models the
	// operator just tested.
	channelModels := mergeOpenCodeChannelModels(entry["models"], models)
	entry["models"] = channelModels
	result.Models = len(channelModels)
	items[target] = entry

	writer, errWriter := a.newWriteManagementClient(managementKey)
	if errWriter != nil {
		return result, errWriter
	}
	defer clearManagementWriterSecrets(writer)
	if errWrite := writer.putAIProviderChannel(ctx, listKind, items); errWrite != nil {
		return result, fmt.Errorf("CPA channel could not be saved")
	}
	// Re-read the channel list so the newly written channel's CPA auth index is
	// recorded for session attribution immediately after binding.
	if entries, errEntries := a.aiProviderChannelEntries(ctx, managementKey, listKind); errEntries == nil {
		_ = a.syncAIProviderChannelBindings(listKind, entries)
	}
	result.Index = target
	result.ChannelKey = fmt.Sprintf("%s:%d", listKind, target)
	return result, nil
}

// mergeOpenCodeChannelKeyEntries updates the first credential row in place and
// keeps any additional weighted rows, so binding does not discard a key pool the
// operator already configured for that channel.
func mergeOpenCodeChannelKeyEntries(existing any, apiKey string) []map[string]any {
	rows := make([]map[string]any, 0, 2)
	if list, ok := existing.([]any); ok {
		for _, item := range list {
			record, isRecord := item.(map[string]any)
			if !isRecord {
				continue
			}
			cloned := make(map[string]any, len(record)+1)
			for key, value := range record {
				cloned[key] = value
			}
			rows = append(rows, cloned)
		}
	}
	if len(rows) == 0 {
		return []map[string]any{{"api-key": strings.TrimSpace(apiKey)}}
	}
	rows[0]["api-key"] = strings.TrimSpace(apiKey)
	return rows
}

// mergeOpenCodeChannelModels keeps existing rows (including operator aliases) and
// appends the upstream catalog so the channel is immediately routable.
func mergeOpenCodeChannelModels(existing any, models []string) []map[string]any {
	rows := make([]map[string]any, 0, len(models)+2)
	seen := make(map[string]bool, len(models))
	appendRow := func(row map[string]any) {
		rows = append(rows, row)
		for _, field := range []string{"name", "alias"} {
			if value, ok := row[field].(string); ok && strings.TrimSpace(value) != "" {
				seen[strings.TrimSpace(value)] = true
			}
		}
	}
	switch list := existing.(type) {
	case []any:
		for _, item := range list {
			if record, isRecord := item.(map[string]any); isRecord {
				cloned := make(map[string]any, len(record)+1)
				for key, value := range record {
					cloned[key] = value
				}
				appendRow(cloned)
				continue
			}
			// A legacy or hand-written string row still names a routable model,
			// so it is preserved instead of being silently erased.
			if text, isText := item.(string); isText && strings.TrimSpace(text) != "" {
				appendRow(map[string]any{"name": strings.TrimSpace(text), "alias": strings.TrimSpace(text)})
			}
		}
	case []map[string]any:
		for _, record := range list {
			cloned := make(map[string]any, len(record)+1)
			for key, value := range record {
				cloned[key] = value
			}
			appendRow(cloned)
		}
	}
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] {
			continue
		}
		seen[model] = true
		rows = append(rows, map[string]any{"name": model, "alias": model})
	}
	return rows
}

// newWriteManagementClient builds the management client used for channel writes.
func (a *App) newWriteManagementClient(managementKey string) (*managementClient, error) {
	if a == nil {
		return nil, fmt.Errorf("management client is unavailable")
	}
	client, errClient := newManagementClient(resolveManagementBaseURL(a.configSnapshot().ManagementBaseURL), managementKey, a.managementDoer)
	if errClient != nil {
		return nil, fmt.Errorf("management client is unavailable")
	}
	return client, nil
}

// putAIProviderChannel rewrites one CPA channel list.
func (c *managementClient) putAIProviderChannel(ctx context.Context, kind string, items []map[string]any) error {
	if !supportedAIProviderProxyKind(kind) {
		return fmt.Errorf("AI provider channel %s is not supported", kind)
	}
	encoded, errEncode := json.Marshal(items)
	if errEncode != nil {
		return fmt.Errorf("AI provider channel payload could not be encoded")
	}
	return c.requestJSON(ctx, http.MethodPut, "/v0/management/"+kind, strings.NewReader(string(encoded)), "application/json", nil)
}

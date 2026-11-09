package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Management API versioning.
//
// CLIProxyAPI v8.0.60 keeps the v0 Management API and adds /v8/management with a
// configuration tree and optimistic concurrency: a write carries If-Match with
// the ETag of the config document it read, and a concurrent writer makes the
// write fail with 412 instead of silently overwriting it. The plugin used to
// write v0 lists only, so a v8 host's channel rows could be replaced from a
// stale read or lose fields another writer added in between.
//
// Writes prefer v8 when the host answers it and fall back to v0 otherwise, so
// older CPA releases keep working. Detection is cached per base URL: probing on
// every bind would add a round trip to each credential rotation, and the answer
// only changes when the host is restarted with a different release.

type managementAPIVersion int

const (
	managementAPIV0 managementAPIVersion = iota
	managementAPIV8
)

const (
	managementV8ConfigPath = "/v8/management/config"
	managementV8APICall    = "/v8/management/requests/api-call"
	managementV0APICall    = "/v0/management/api-call"
	// A host upgrade is detected within this window after a restart.
	managementVersionTTL = 5 * time.Minute
)

type managementVersionEntry struct {
	version   managementAPIVersion
	checkedAt time.Time
}

var managementVersionCache = struct {
	mu      sync.Mutex
	entries map[string]managementVersionEntry
}{entries: map[string]managementVersionEntry{}}

func resetManagementVersionCache() {
	managementVersionCache.mu.Lock()
	managementVersionCache.entries = map[string]managementVersionEntry{}
	managementVersionCache.mu.Unlock()
}

// managementResponse is one Management API reply with its headers, which the
// versioned client needs for the ETag that guards a conditional write.
type managementResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func (c *managementClient) doJSON(ctx context.Context, method, path string, payload []byte, contentType string, headers http.Header) (*managementResponse, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	request, errRequest := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if errRequest != nil {
		return nil, fmt.Errorf("create management request: %w", errRequest)
	}
	request.Header.Set("Authorization", "Bearer "+c.key)
	request.Header.Set("Accept", "application/json")
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	response, errDo := c.doer.Do(request)
	if errDo != nil {
		return nil, fmt.Errorf("management API request failed: %w", errDo)
	}
	if response == nil || response.Body == nil {
		return nil, fmt.Errorf("management API returned an empty response")
	}
	defer func() {
		_ = response.Body.Close()
	}()
	limited := io.LimitReader(response.Body, maxManagementResponse+1)
	responseBody, errRead := io.ReadAll(limited)
	if errRead != nil {
		return nil, fmt.Errorf("read management API response: %w", errRead)
	}
	if len(responseBody) > maxManagementResponse {
		return nil, fmt.Errorf("management API response exceeded the size limit")
	}
	return &managementResponse{StatusCode: response.StatusCode, Header: response.Header, Body: responseBody}, nil
}

// managementAPIVersionFor reports which API this host serves, probing at most
// once per TTL. A failed probe is cached as v0: the fallback path is exactly the
// behaviour the plugin had before v8 support, and an unreachable v8 endpoint
// must not make a write fail where v0 would have worked.
func (c *managementClient) managementAPIVersionFor(ctx context.Context) managementAPIVersion {
	if c == nil || !c.probeV8 {
		return managementAPIV0
	}
	managementVersionCache.mu.Lock()
	entry, known := managementVersionCache.entries[c.baseURL]
	managementVersionCache.mu.Unlock()
	if known && time.Since(entry.checkedAt) < managementVersionTTL {
		return entry.version
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	version := managementAPIV0
	response, errProbe := c.doJSON(probeCtx, http.MethodGet, managementV8ConfigPath, nil, "", nil)
	if errProbe == nil && response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		version = managementAPIV8
	}
	managementVersionCache.mu.Lock()
	managementVersionCache.entries[c.baseURL] = managementVersionEntry{version: version, checkedAt: time.Now()}
	managementVersionCache.mu.Unlock()
	return version
}

// readAPIKeyGroupV8 reads one api-keys group and the document ETag that a
// conditional write needs.
func (c *managementClient) readAPIKeyGroupV8(ctx context.Context, kind string) ([]map[string]any, string, error) {
	if !supportedAIProviderProxyKind(kind) {
		return nil, "", fmt.Errorf("AI provider channel %s is not supported", kind)
	}
	path := managementV8ConfigPath + "/api-keys/" + kind
	response, errGet := c.doJSON(ctx, http.MethodGet, path, nil, "", nil)
	if errGet != nil {
		return nil, "", errGet
	}
	if response.StatusCode == http.StatusNotFound {
		// A group that has no rows yet reads as an empty list; the write creates it.
		return nil, response.Header.Get("ETag"), nil
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, "", &managementHTTPStatusError{StatusCode: response.StatusCode}
	}
	items, errDecode := decodeAPIKeyGroup(response.Body)
	if errDecode != nil {
		return nil, "", errDecode
	}
	return items, response.Header.Get("ETag"), nil
}

// v8APIKeyGroups converts the row shape this plugin builds into the group shape the
// v8 tree stores: one group per base URL, carrying a keys list. Sending the v0 row
// straight to v8 is rejected with 422, which is how a finished community sign-in
// failed at the last step.
func v8APIKeyGroups(items []map[string]any) []map[string]any {
	groups := make([]map[string]any, 0, len(items))
	for _, item := range items {
		baseURL, _ := item["base-url"].(string)
		if strings.TrimSpace(baseURL) == "" {
			continue
		}
		group := map[string]any{"base-url": baseURL}
		if headers, found := item["headers"]; found {
			group["headers"] = headers
		}
		// The model list is what makes a channel routable: CPA registers a compat
		// group's models from this field and unregisters them when it is empty, so
		// dropping it here would leave a written channel serving nothing.
		if models, found := v8ChannelModels(item["models"]); found {
			group["models"] = models
		}
		if prefix, ok := item["prefix"].(string); ok && strings.TrimSpace(prefix) != "" {
			group["prefix"] = prefix
		}
		keys := make([]map[string]any, 0, 1)
		switch entries := item["api-key-entries"].(type) {
		case []map[string]any:
			for _, entry := range entries {
				if key, ok := entry["api-key"].(string); ok && strings.TrimSpace(key) != "" {
					keys = append(keys, map[string]any{"api-key": key})
				}
			}
		case []any:
			for _, raw := range entries {
				if entry, ok := raw.(map[string]any); ok {
					if key, okKey := entry["api-key"].(string); okKey && strings.TrimSpace(key) != "" {
						keys = append(keys, map[string]any{"api-key": key})
					}
				}
			}
		}
		if key, ok := item["api-key"].(string); ok && strings.TrimSpace(key) != "" && len(keys) == 0 {
			keys = append(keys, map[string]any{"api-key": key})
		}
		if len(keys) == 0 {
			continue
		}
		group["keys"] = keys
		groups = append(groups, group)
	}
	return groups
}

// v8ChannelModels converts a channel row's model list into what the v8 tree
// stores. A v0 row carries {name, alias} already; a bare list of ids becomes the
// same pair, so each id is both what CPA calls upstream and what a client asks
// for.
func v8ChannelModels(raw any) ([]map[string]any, bool) {
	rows := make([]any, 0)
	switch list := raw.(type) {
	case []map[string]any:
		for _, entry := range list {
			rows = append(rows, entry)
		}
	case []any:
		rows = list
	case []string:
		for _, entry := range list {
			rows = append(rows, entry)
		}
	default:
		return nil, false
	}
	out := make([]map[string]any, 0, len(rows))
	for _, entry := range rows {
		switch value := entry.(type) {
		case string:
			if id := strings.TrimSpace(value); id != "" {
				out = append(out, map[string]any{"name": id, "alias": id})
			}
		case map[string]any:
			name, _ := value["name"].(string)
			alias, _ := value["alias"].(string)
			name = strings.TrimSpace(name)
			alias = strings.TrimSpace(alias)
			if name == "" && alias == "" {
				continue
			}
			if name == "" {
				name = alias
			}
			if alias == "" {
				alias = name
			}
			model := map[string]any{"name": name, "alias": alias}
			// Carry the optional per-model settings a bind may have written.
			for _, key := range []string{"display-name", "max-context-length", "force-mapping", "image", "is-compat"} {
				if field, found := value[key]; found {
					model[key] = field
				}
			}
			out = append(out, model)
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

func decodeAPIKeyGroup(raw []byte) ([]map[string]any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var items []map[string]any
	if errUnmarshal := json.Unmarshal(trimmed, &items); errUnmarshal == nil {
		return items, nil
	}
	// A single object is accepted as a one-row group by the v8 tree.
	var single map[string]any
	if errUnmarshal := json.Unmarshal(trimmed, &single); errUnmarshal != nil {
		return nil, fmt.Errorf("management API returned an invalid api-keys group")
	}
	return []map[string]any{single}, nil
}

// patchAPIKeyGroupV8 replaces one group with If-Match. 412 means another writer
// saved between our read and our write; the caller re-reads and retries once.
func (c *managementClient) patchAPIKeyGroupV8(ctx context.Context, kind string, items []map[string]any, ifMatch string) error {
	payload, errMarshal := json.Marshal(v8APIKeyGroups(items))
	if errMarshal != nil {
		return fmt.Errorf("AI provider channel payload could not be encoded")
	}
	headers := http.Header{}
	if strings.TrimSpace(ifMatch) != "" {
		headers.Set("If-Match", ifMatch)
	}
	response, errPatch := c.doJSON(ctx, http.MethodPatch, managementV8ConfigPath+"/api-keys/"+kind, payload, "application/json", headers)
	if errPatch != nil {
		return errPatch
	}
	if response.StatusCode == http.StatusPreconditionFailed {
		return &managementHTTPStatusError{StatusCode: response.StatusCode}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return &managementHTTPStatusError{StatusCode: response.StatusCode}
	}
	return nil
}

// writeAPIKeyGroupV8 reads, merges nothing (the caller already owns the list),
// and writes conditionally, retrying once after a lost race so a concurrent
// writer costs one extra round trip instead of silently dropping its change.
func (c *managementClient) writeAPIKeyGroupV8(ctx context.Context, kind string, items []map[string]any) error {
	_, etag, errRead := c.readAPIKeyGroupV8(ctx, kind)
	if errRead != nil {
		return errRead
	}
	errWrite := c.patchAPIKeyGroupV8(ctx, kind, items, etag)
	var statusError *managementHTTPStatusError
	if errWrite != nil && errors.As(errWrite, &statusError) && statusError.StatusCode == http.StatusPreconditionFailed {
		_, retryETag, errRetryRead := c.readAPIKeyGroupV8(ctx, kind)
		if errRetryRead != nil {
			return errRetryRead
		}
		errWrite = c.patchAPIKeyGroupV8(ctx, kind, items, retryETag)
	}
	return errWrite
}

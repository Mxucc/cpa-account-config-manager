package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// resetOpenCodeChannelModelsCache clears the package-level channel scan, which
// otherwise leaks between tests in the same process.
func resetOpenCodeChannelModelsCache() {
	openCodeChannelModelsMu.Lock()
	openCodeChannelModelsState = nil
	openCodeChannelModelsMu.Unlock()
}

func TestOpenCodeModelControlStoreNormalizesDedupesAndCaps(t *testing.T) {
	dataDir := t.TempDir()
	service := NewOpenCodeModelControlService()
	service.Configure(Config{DataDir: dataDir})
	models := []string{"GPT-5.5", "gpt-5.5 ", "opencode-go/Kimi_K2", ""}
	for index := 0; index < openCodeModelControlMaxModels+8; index++ {
		models = append(models, fmt.Sprintf("model-%d", index))
	}
	if _, errSet := service.Set(models); errSet != nil {
		t.Fatalf("set: %v", errSet)
	}
	// Deduplicated and capped at the documented maximum.
	if service.Count() != openCodeModelControlMaxModels {
		t.Fatalf("disabled count = %d, want %d", service.Count(), openCodeModelControlMaxModels)
	}
	if !service.Disabled("GPT-5.5") {
		t.Fatal("lookup is not case-insensitive")
	}
	// The same prefix- and separator-insensitive fold the session router uses, so
	// a channel-prefixed or underscore-drifted id still matches.
	if !service.Disabled("OpenCode-Go/kimi-k2") {
		t.Fatal("prefix- or separator-insensitive lookup failed")
	}
	info, errStat := os.Stat(openCodeModelControlStorePath(dataDir))
	if errStat != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("store permissions = %v err=%v", info, errStat)
	}

	restored := NewOpenCodeModelControlService()
	restored.Configure(Config{DataDir: dataDir})
	if restored.Count() != openCodeModelControlMaxModels || !restored.Disabled("gpt-5.5") {
		t.Fatalf("the disabled list did not persist: %#v", restored.Snapshot())
	}
	if _, errSet := restored.Set(nil); errSet != nil {
		t.Fatalf("clear: %v", errSet)
	}
	if restored.Count() != 0 || restored.Disabled("gpt-5.5") {
		t.Fatalf("clearing the list left state behind: %#v", restored.Snapshot())
	}
}

func TestOpenCodeModelControlGateBlocksDisabledModelsOnlyForOpenCode(t *testing.T) {
	service := NewOpenCodeModelControlService()
	service.Configure(Config{DataDir: t.TempDir()})
	if _, errSet := service.Set([]string{"GPT-5.5"}); errSet != nil {
		t.Fatalf("set: %v", errSet)
	}
	gate := NewOpenCodeModelControl(service)
	gate.SetAuthIndexes([]string{"opencode-index"})
	gate.SetTargets([]string{"gpt-5.5", "kimi-k2"})
	if !gate.RequestInterceptionActive() {
		t.Fatal("the gate is inactive while a model is disabled")
	}

	openCodeMetadata := map[string]any{"selected_auth_index": "opencode-index"}
	tests := []struct {
		name        string
		request     cpaapi.RequestInterceptRequest
		wantBlocked bool
	}{
		{
			name:        "disabled model on an OpenCode credential",
			request:     cpaapi.RequestInterceptRequest{RequestID: "req-1", ToFormat: "openai", Model: "GPT-5.5", Metadata: openCodeMetadata},
			wantBlocked: true,
		},
		{
			name:        "disabled model behind a channel prefix without an auth index",
			request:     cpaapi.RequestInterceptRequest{RequestID: "req-2", ToFormat: "openai", Model: "opencode-go/gpt-5.5"},
			wantBlocked: true,
		},
		{
			name:        "enabled model on an OpenCode credential",
			request:     cpaapi.RequestInterceptRequest{RequestID: "req-3", ToFormat: "openai", Model: "kimi-k2", Metadata: openCodeMetadata},
			wantBlocked: false,
		},
		{
			name:        "codex request with a shared model id",
			request:     cpaapi.RequestInterceptRequest{RequestID: "req-4", ToFormat: "codex", Model: "gpt-5.5"},
			wantBlocked: false,
		},
		{
			name:        "codex provider metadata with a shared model id",
			request:     cpaapi.RequestInterceptRequest{RequestID: "req-5", ToFormat: "openai", Model: "gpt-5.5", Metadata: map[string]any{"provider": "codex"}},
			wantBlocked: false,
		},
		{
			name:        "auth index of a different channel",
			request:     cpaapi.RequestInterceptRequest{RequestID: "req-6", ToFormat: "openai", Model: "gpt-5.5", Metadata: map[string]any{"selected_auth_index": "codex-index"}},
			wantBlocked: false,
		},
		{
			name:        "model OpenCode does not publish without an auth index",
			request:     cpaapi.RequestInterceptRequest{RequestID: "req-7", ToFormat: "openai", Model: "gpt-5.5-vendor-only"},
			wantBlocked: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, changed := gate.InterceptRequest(test.request)
			if changed != test.wantBlocked {
				t.Fatalf("InterceptRequest() changed = %v, want %v", changed, test.wantBlocked)
			}
			if !test.wantBlocked {
				if response.Terminate || len(response.ResponseBody) != 0 {
					t.Fatalf("an untouched request produced a response: %#v", response)
				}
				return
			}
			if !response.Terminate || response.StatusCode != http.StatusForbidden {
				t.Fatalf("rejection response = %#v", response)
			}
			if got := response.ResponseHeaders.Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q", got)
			}
			var payload struct {
				Error struct {
					Message string `json:"message"`
					Code    string `json:"code"`
					Source  string `json:"source"`
					Model   string `json:"model"`
				} `json:"error"`
			}
			if errDecode := json.Unmarshal(response.ResponseBody, &payload); errDecode != nil {
				t.Fatalf("decode rejection: %v", errDecode)
			}
			if payload.Error.Code != openCodeModelDisabledCode || payload.Error.Source != openCodeModelDisabledSource {
				t.Fatalf("rejection payload = %#v", payload.Error)
			}
			if payload.Error.Message != openCodeModelDisabledMessage || payload.Error.Model != "gpt-5.5" {
				t.Fatalf("rejection payload = %#v", payload.Error)
			}
		})
	}

	// The requested model is honoured when Model is empty.
	if _, changed := gate.InterceptRequest(cpaapi.RequestInterceptRequest{
		RequestID: "req-8", ToFormat: "openai", RequestedModel: "gpt-5.5",
	}); !changed {
		t.Fatal("the requested model did not block")
	}
	// Clearing the list deactivates the gate entirely.
	if _, errSet := service.Set(nil); errSet != nil {
		t.Fatalf("clear: %v", errSet)
	}
	if gate.RequestInterceptionActive() {
		t.Fatal("the gate stayed active with nothing disabled")
	}
}

// The models list merges the disabled set with the official catalogs, the cached
// account catalogs, and the cached channel scan.
func TestOpenCodeModelControlRowsMergeCatalogAccountsAndChannels(t *testing.T) {
	resetOpenCodeChannelModelsCache()
	app := NewApp(&fakeAuthHost{}, nil)
	app.Configure([]byte("data_dir: " + t.TempDir()))
	if _, errSet := app.opencodeModelControl.Set([]string{"GPT-5.5"}); errSet != nil {
		t.Fatalf("set: %v", errSet)
	}
	// One Go workspace and one Zen credential cache the disabled model; the Go
	// catalog repeats a model with separator drift, which must count once.
	app.opencode.mu.Lock()
	app.opencode.accounts = []OpenCodeAccount{{
		ID: "go-1", WorkspaceID: "wrk_1", Models: []string{"GPT-5.5", "kimi-k2", "kimi_k2"},
	}}
	app.opencode.mu.Unlock()
	app.opencodeZen.mu.Lock()
	app.opencodeZen.accounts = []OpenCodeZenAccount{{ID: "zen-1", Models: []string{"gpt-5.5"}}}
	app.opencodeZen.mu.Unlock()
	// Only the official catalogs publish the third model.
	app.opencodePricing.table.Store(&openCodePricingTable{
		Go:  map[string]OpenCodeModelPrice{"claude-sonnet-4": {ID: "claude-sonnet-4"}},
		Zen: map[string]OpenCodeModelPrice{"claude-sonnet-4": {ID: "claude-sonnet-4"}},
	})
	// One OpenCode channel lists the disabled model; an unrelated channel that
	// happens to list the same id must not count.
	app.managementDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
		body, _ := json.Marshal(map[string]any{"openai-compatibility": []any{
			map[string]any{"name": "OpenCode Go wrk_1", "base-url": "https://opencode.ai/zen/go/v1", "models": []any{
				map[string]any{"name": "gpt-5.5", "alias": "gpt-5.5"},
			}},
			map[string]any{"name": "OpenRouter", "base-url": "https://openrouter.ai/api/v1", "models": []any{
				map[string]any{"name": "gpt-5.5", "alias": "gpt-5.5"},
			}},
		}})
		return jsonHTTPResponse(http.StatusOK, string(body)), nil
	})
	app.refreshOpenCodeChannelModels(context.Background(), "management-secret")

	byID := map[string]OpenCodeModelControlRow{}
	for _, row := range app.openCodeModelControlRows() {
		byID[row.ID] = row
	}
	disabledRow, ok := byID["gpt-5.5"]
	if !ok || !disabledRow.Disabled || disabledRow.Accounts != 2 || disabledRow.Channels != 1 {
		t.Fatalf("disabled row = %#v (present=%v)", disabledRow, ok)
	}
	accountRow, ok := byID["kimi-k2"]
	if !ok || accountRow.Disabled || accountRow.Accounts != 1 || accountRow.Channels != 0 {
		t.Fatalf("account-cached row = %#v (present=%v)", accountRow, ok)
	}
	catalogRow, ok := byID["claude-sonnet-4"]
	if !ok || catalogRow.Disabled || catalogRow.Accounts != 0 || catalogRow.Channels != 0 {
		t.Fatalf("catalog row = %#v (present=%v)", catalogRow, ok)
	}
}

func TestOpenCodeModelControlRoutesRequireKeyAndApply(t *testing.T) {
	resetOpenCodeChannelModelsCache()
	app := NewApp(&fakeAuthHost{}, nil)
	app.Configure([]byte("data_dir: " + t.TempDir()))
	app.managementDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return jsonHTTPResponse(http.StatusOK, `{"openai-compatibility":[]}`), nil
	})
	// The route labels the credit table the rows are priced against.
	app.creditUsage.table.Store(&creditPricingTable{
		Models:    map[string]creditModelPricing{"gpt-5.5": {Input: 1e-06}},
		UpdatedAt: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC),
		Source:    "test-credit-table",
	})

	const route = "/v0/management" + managementRoutePrefix + "/opencode/model-control"
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
			Method: method, Path: route, Body: []byte(`{"disabled":["gpt-5.5"]}`),
		})
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s status without key = %d", method, response.StatusCode)
		}
	}

	headers := http.Header{"Authorization": []string{"Bearer management-secret"}}
	write := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut, Path: route, Headers: headers,
		Body: []byte(`{"disabled":["GPT-5.5"]}`),
	})
	if write.StatusCode != http.StatusOK {
		t.Fatalf("write status = %d body=%s", write.StatusCode, write.Body)
	}
	if !app.opencodeModelControl.Disabled("gpt-5.5") {
		t.Fatal("the route did not apply the disabled list")
	}
	var writePayload struct {
		Models        []OpenCodeModelControlRow `json:"models"`
		Disabled      []string                  `json:"disabled"`
		StorageError  string                    `json:"storage_error"`
		PricingSource string                    `json:"pricing_source"`
	}
	if errDecode := json.Unmarshal(write.Body, &writePayload); errDecode != nil {
		t.Fatalf("decode write response: %v", errDecode)
	}
	if writePayload.PricingSource != "test-credit-table" || writePayload.StorageError != "" {
		t.Fatalf("write payload = %#v", writePayload)
	}
	if len(writePayload.Disabled) != 1 || writePayload.Disabled[0] != "gpt-5.5" {
		t.Fatalf("disabled list = %#v", writePayload.Disabled)
	}

	read := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: route, Headers: headers,
	})
	if read.StatusCode != http.StatusOK {
		t.Fatalf("read status = %d body=%s", read.StatusCode, read.Body)
	}
	var readPayload struct {
		Models        []OpenCodeModelControlRow `json:"models"`
		Disabled      []string                  `json:"disabled"`
		PricingSource string                    `json:"pricing_source"`
	}
	if errDecode := json.Unmarshal(read.Body, &readPayload); errDecode != nil {
		t.Fatalf("decode read response: %v", errDecode)
	}
	if readPayload.PricingSource != "test-credit-table" {
		t.Fatalf("read pricing_source = %q", readPayload.PricingSource)
	}
	found := false
	for _, row := range readPayload.Models {
		if row.ID == "gpt-5.5" && row.Disabled {
			found = true
		}
	}
	if !found {
		t.Fatalf("the disabled row is missing: %#v", readPayload.Models)
	}
}

// The gate must run before the session router, so a blocked request never gets
// an x-opencode-session header and the router's attribution counters stay clean.
func TestOpenCodeModelControlGateRunsBeforeSessionRouter(t *testing.T) {
	resetOpenCodeChannelModelsCache()
	app := NewApp(&fakeAuthHost{}, nil)
	app.Configure([]byte("data_dir: " + t.TempDir()))
	if _, errSet := app.opencodeModelControl.Set([]string{"gpt-5.5"}); errSet != nil {
		t.Fatalf("set: %v", errSet)
	}
	// The refresh inside HandleRequestAfter publishes this catalog to both the
	// router and the gate, so the model fallback attributes the request to
	// OpenCode even without an auth index.
	app.opencodePricing.table.Store(&openCodePricingTable{
		Go:  map[string]OpenCodeModelPrice{"gpt-5.5": {ID: "gpt-5.5"}},
		Zen: map[string]OpenCodeModelPrice{"gpt-5.5": {ID: "gpt-5.5"}},
	})
	request := cpaapi.RequestInterceptRequest{RequestID: "req-1", ToFormat: "openai", Model: "gpt-5.5"}
	response := app.HandleRequestAfter(request)
	if !response.Terminate || response.StatusCode != http.StatusForbidden {
		t.Fatalf("response = %#v", response)
	}
	if len(response.Headers) != 0 || response.Headers.Get(openCodeSessionHeader) != "" {
		t.Fatalf("a blocked request carried headers: %#v", response.Headers)
	}
	if snapshot := app.opencodeSession.Snapshot(); snapshot.InjectedRequests != 0 || snapshot.DistinctSessions != 0 {
		t.Fatalf("the session router ran for a blocked request: %#v", snapshot)
	}

	// Clearing the list lets the router inject again, proving the previous
	// assertion was about ordering rather than an inactive router.
	if _, errSet := app.opencodeModelControl.Set(nil); errSet != nil {
		t.Fatalf("clear: %v", errSet)
	}
	response = app.HandleRequestAfter(request)
	if response.Terminate {
		t.Fatalf("a request was blocked with nothing disabled: %#v", response)
	}
	if response.Headers.Get(openCodeSessionHeader) == "" {
		t.Fatal("the session router did not inject after the control was cleared")
	}
}

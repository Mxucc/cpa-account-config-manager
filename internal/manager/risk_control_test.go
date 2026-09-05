package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

func configuredRiskControl(t *testing.T, config RiskControlConfig) (*RiskControlService, string) {
	t.Helper()
	dataDir := t.TempDir()
	service := NewRiskControlService()
	service.Configure(Config{DataDir: dataDir})
	if _, err := service.UpdateConfig(config); err != nil {
		t.Fatalf("UpdateConfig() error = %v", err)
	}
	return service, dataDir
}

func TestRiskControlSnapshotSerializesEmptyCollectionsAsArrays(t *testing.T) {
	service := NewRiskControlService()
	service.Configure(Config{DataDir: t.TempDir()})
	raw, err := json.Marshal(service.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"blocked_keywords":[]`, `"events":[]`} {
		if !bytes.Contains(raw, []byte(field)) {
			t.Fatalf("snapshot missing non-null collection %s: %s", field, raw)
		}
	}
	for _, field := range []string{`"blocked_keywords":null`, `"models":null`, `"scanners":null`, `"events":null`} {
		if bytes.Contains(raw, []byte(field)) {
			t.Fatalf("snapshot contains nullable collection %s: %s", field, raw)
		}
	}
}

func TestRiskControlPreBlockRecordsOnlyRedactedMetadata(t *testing.T) {
	service, dataDir := configuredRiskControl(t, RiskControlConfig{
		Enabled:             true,
		Mode:                RiskControlModePreBlock,
		BlockedKeywords:     []string{"blocked phrase"},
		PreHashCheckEnabled: true,
	})
	request := cpaapi.RequestInterceptRequest{
		Body:     []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"blocked phrase private-prompt-suffix"}]}`),
		Model:    "gpt-5",
		ToFormat: "openai",
		Headers:  http.Header{"Authorization": {"Bearer private-token"}},
		Metadata: map[string]any{"selected_auth_id": "person@example.com", "provider": "codex"},
	}
	response, changed := service.InterceptRequest(request)
	if !changed || !response.Terminate || response.StatusCode != http.StatusForbidden {
		t.Fatalf("response = %#v changed=%t", response, changed)
	}
	snapshot := service.Snapshot()
	if len(snapshot.Events) != 1 || snapshot.Events[0].Action != "keyword_block" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.Events[0].AccountRef == "" || snapshot.Events[0].AccountRef == "person@example.com" {
		t.Fatalf("account ref was not pseudonymized: %#v", snapshot.Events[0])
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, riskControlStoreName))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range [][]byte{[]byte("private-prompt-suffix"), []byte("person@example.com"), []byte("private-token")} {
		if bytes.Contains(raw, secret) {
			t.Fatalf("risk-control store leaked %q: %s", secret, raw)
		}
	}
}

func TestRiskControlConfigureBoundsPersistedHashes(t *testing.T) {
	dataDir := t.TempDir()
	hashes := make([]string, 0, riskControlMaxHashes+32)
	for i := 0; i < riskControlMaxHashes+32; i++ {
		hashes = append(hashes, fmt.Sprintf("%064x", i+1))
	}
	raw, err := json.Marshal(persistedRiskControl{
		Version: riskControlStoreVersion,
		Config:  defaultRiskControlConfig(),
		Hashes:  hashes,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, riskControlStoreName), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	service := NewRiskControlService()
	service.Configure(Config{DataDir: dataDir})
	if got := service.Snapshot().Status.RememberedHashes; got != riskControlMaxHashes {
		t.Fatalf("remembered hashes = %d, want %d", got, riskControlMaxHashes)
	}
}

func TestRiskControlObserveAndModelFilter(t *testing.T) {
	service, _ := configuredRiskControl(t, RiskControlConfig{
		Enabled:         true,
		Mode:            RiskControlModeObserve,
		BlockedKeywords: []string{"danger"},
		ModelFilter:     RiskControlModelFilter{Mode: RiskControlModelFilterInclude, Models: []string{"gpt-5"}},
	})
	skipped, changed := service.InterceptRequest(cpaapi.RequestInterceptRequest{Body: []byte(`{"messages":[{"content":"danger"}]}`), Model: "other"})
	if changed || skipped.Terminate || len(service.Snapshot().Events) != 0 {
		t.Fatalf("excluded model was inspected: %#v", service.Snapshot())
	}
	allowed, changed := service.InterceptRequest(cpaapi.RequestInterceptRequest{Body: []byte(`{"messages":[{"content":"danger"}]}`), Model: "gpt-5"})
	if changed || allowed.Terminate {
		t.Fatalf("observe mode changed request: %#v changed=%t", allowed, changed)
	}
	snapshot := service.Snapshot()
	if snapshot.Status.Observed != 1 || snapshot.Status.Blocked != 0 || snapshot.Events[0].Action != "keyword_observe" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestRiskControlRememberedHashBlocksAfterKeywordRemoval(t *testing.T) {
	service, _ := configuredRiskControl(t, RiskControlConfig{
		Enabled:             true,
		Mode:                RiskControlModePreBlock,
		BlockedKeywords:     []string{"repeat-risk"},
		PreHashCheckEnabled: true,
	})
	request := cpaapi.RequestInterceptRequest{Body: []byte(`{"input":"repeat-risk"}`), ToFormat: "codex"}
	if response, changed := service.InterceptRequest(request); !changed || !response.Terminate {
		t.Fatalf("first response = %#v changed=%t", response, changed)
	}
	config := service.Snapshot().Config
	config.BlockedKeywords = nil
	if _, err := service.UpdateConfig(config); err != nil {
		t.Fatal(err)
	}
	if response, changed := service.InterceptRequest(request); !changed || !response.Terminate {
		t.Fatalf("hash response = %#v changed=%t", response, changed)
	}
	snapshot := service.Snapshot()
	if snapshot.Events[0].Action != "hash_block" || snapshot.Status.HashHits != 1 || snapshot.Status.RememberedHashes != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestExtractRiskControlTextSupportsCommonPayloads(t *testing.T) {
	cases := []string{
		`{"instructions":"system risk","input":[{"content":[{"type":"input_text","text":"response risk"}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"text","text":"chat risk"}]}]}`,
		`{"system":"anthropic system","messages":[{"content":[{"type":"text","text":"anthropic risk"}]}]}`,
		`{"contents":[{"parts":[{"text":"gemini risk"}]}]}`,
	}
	for _, body := range cases {
		text, _, ok := extractRiskControlText([]byte(body))
		if !ok || !stringsContainsFold(text, "risk") {
			t.Fatalf("extractRiskControlText(%s) = %q, %t", body, text, ok)
		}
	}
	if _, _, ok := extractRiskControlText([]byte(`{"tools":[{"description":"risk"}]}`)); ok {
		t.Fatal("tool schema must not be scanned")
	}
}

func stringsContainsFold(value, substring string) bool {
	return bytes.Contains(bytes.ToLower([]byte(value)), bytes.ToLower([]byte(substring)))
}

func TestRiskControlManagementRoutesUpdateReadAndClear(t *testing.T) {
	app := NewApp(&fakeAuthHost{}, []byte("index"))
	app.Configure([]byte("data_dir: " + t.TempDir() + "\n"))
	defer app.Close()
	config := RiskControlConfig{Enabled: true, Mode: RiskControlModeObserve, BlockedKeywords: []string{"risk"}}
	body, _ := json.Marshal(config)
	updated := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut,
		Path:   "/v0/management/plugins/cpa-account-config-manager/risk-control",
		Body:   body,
	})
	if updated.StatusCode != http.StatusOK {
		t.Fatalf("update = %d %s", updated.StatusCode, updated.Body)
	}
	app.riskControl.InterceptRequest(cpaapi.RequestInterceptRequest{Body: []byte(`{"input":"risk"}`)})
	read := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{Method: http.MethodGet, Path: "/plugins/cpa-account-config-manager/risk-control"})
	if read.StatusCode != http.StatusOK || !bytes.Contains(read.Body, []byte(`"total_events":1`)) {
		t.Fatalf("read = %d %s", read.StatusCode, read.Body)
	}
	cleared := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{Method: http.MethodDelete, Path: "/v0/management/plugins/cpa-account-config-manager/risk-control/events"})
	if cleared.StatusCode != http.StatusOK || !bytes.Contains(cleared.Body, []byte(`"total_events":0`)) {
		t.Fatalf("clear = %d %s", cleared.StatusCode, cleared.Body)
	}
}

func TestRiskControlConfigValidation(t *testing.T) {
	service := NewRiskControlService()
	service.Configure(Config{DataDir: t.TempDir()})
	for _, config := range []RiskControlConfig{
		{Mode: "invalid"},
		{Mode: RiskControlModeObserve, BlockStatus: 500},
		{Mode: RiskControlModeObserve, ModelFilter: RiskControlModelFilter{Mode: "invalid"}},
	} {
		if _, err := service.UpdateConfig(config); err == nil {
			t.Fatalf("UpdateConfig(%#v) succeeded", config)
		}
	}
}

func TestRiskAuditUsesDirectAPIKeyAndDoesNotPersistOrExposeIt(t *testing.T) {
	const credential = "private-risk-audit-token"
	transport := &fakeAgentIdentityTransport{do: func(_ string, request cpaapi.HostHTTPRequest) (cpaapi.HostHTTPResponse, error) {
		if got := request.Headers.Get("Authorization"); got != "Bearer "+credential {
			t.Fatalf("authorization = %q", got)
		}
		var payload map[string]any
		if err := json.Unmarshal(request.Body, &payload); err != nil {
			t.Fatal(err)
		}
		messages, _ := payload["messages"].([]any)
		if len(messages) != 2 {
			t.Fatalf("messages = %#v", messages)
		}
		userMessage, _ := messages[1].(map[string]any)
		content, _ := userMessage["content"].(string)
		if !strings.Contains(content, "<user_input>") || !strings.Contains(content, "attack a target") {
			t.Fatalf("audit payload was not wrapped: %s", request.Body)
		}
		return cpaapi.HostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"choices":[{"message":{"content":"{\"flagged\":true,\"reason\":\"private reason\"}"}}]}`)}, nil
	}}
	service := NewRiskControlService()
	service.SetAuditTransport(transport)
	dataDir := t.TempDir()
	service.Configure(Config{DataDir: dataDir})
	config := defaultRiskControlConfig()
	config.Audit.Enabled = true
	config.Audit.Mode = RiskControlModePreBlock
	config.Audit.Endpoint = "https://guard.example.test/v1/chat/completions"
	config.Audit.Model = "guard-model"
	config.Audit.APIKey = credential
	if _, err := service.UpdateConfig(config); err != nil {
		t.Fatal(err)
	}
	response, changed := service.InterceptRequest(cpaapi.RequestInterceptRequest{Body: []byte("{\"model\":\"gpt-5\",\"messages\":[{\"role\":\"user\",\"content\":\"attack a target\"}]}")})
	if !changed || !response.Terminate || response.StatusCode != http.StatusForbidden {
		t.Fatalf("response = %#v changed=%t", response, changed)
	}
	snapshot := service.Snapshot()
	if snapshot.Status.Audit.Blocked != 1 || len(snapshot.Events) != 1 || snapshot.Events[0].Module != "audit" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, riskControlStoreName))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{credential, "attack a target", "private reason"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("persisted state leaked %q: %s", secret, raw)
		}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(credential)) || bytes.Contains(encoded, []byte("private reason")) {
		t.Fatalf("snapshot leaked secret: %s", encoded)
	}
	if snapshot.Config.Audit.APIKey != "" || !snapshot.Config.Audit.APIKeySet {
		t.Fatalf("snapshot api key state = %#v", snapshot.Config.Audit)
	}
}

func TestRiskAuditEmptyAPIKeyRetainsExistingAndClearRemovesIt(t *testing.T) {
	service := NewRiskControlService()
	service.Configure(Config{DataDir: t.TempDir()})
	config := defaultRiskControlConfig()
	config.Audit.APIKey = "first-key"
	updated, err := service.UpdateConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Config.Audit.APIKeySet || updated.Config.Audit.APIKey != "" {
		t.Fatalf("configured snapshot = %#v", updated.Config.Audit)
	}
	config = updated.Config
	config.Audit.APIKey = ""
	updated, err = service.UpdateConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Config.Audit.APIKeySet {
		t.Fatal("empty api key unexpectedly cleared the key")
	}
	config = updated.Config
	config.Audit.APIKeyClear = true
	updated, err = service.UpdateConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Config.Audit.APIKeySet {
		t.Fatal("api key clear did not remove the key")
	}
}

func TestPromptAuditUsesSelectedPromptAndSupportsCustomCatalogEntries(t *testing.T) {
	transport := &fakeAgentIdentityTransport{do: func(_ string, request cpaapi.HostHTTPRequest) (cpaapi.HostHTTPResponse, error) {
		var payload map[string]any
		if err := json.Unmarshal(request.Body, &payload); err != nil {
			t.Fatal(err)
		}
		messages, _ := payload["messages"].([]any)
		systemMessage, _ := messages[0].(map[string]any)
		if systemMessage["content"] != "custom system prompt" {
			t.Fatalf("selected prompt = %#v", systemMessage["content"])
		}
		return cpaapi.HostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"flagged":false,"confidence":0.1}`)}, nil
	}}
	service := NewRiskControlService()
	service.SetAuditTransport(transport)
	service.Configure(Config{DataDir: t.TempDir()})
	config := defaultRiskControlConfig()
	config.Audit.Enabled = true
	config.Audit.Mode = RiskControlModePreBlock
	config.Audit.Endpoint = "https://guard.example.test/v1/chat/completions"
	config.Audit.Model = "guard-model"
	config.SystemPrompts = append(config.SystemPrompts, RiskSystemPrompt{ID: "custom", Name: "Custom", SystemPrompt: "custom system prompt"})
	config.Audit.PromptID = "custom"
	updated, err := service.UpdateConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Config.Audit.PromptID != "custom" || len(updated.Config.SystemPrompts) != 2 {
		t.Fatalf("updated config = %#v", updated.Config)
	}
	if _, changed := service.InterceptRequest(cpaapi.RequestInterceptRequest{Body: []byte("{\"messages\":[{\"role\":\"user\",\"content\":\"safe\"}]}")}); changed {
		t.Fatal("safe audit unexpectedly changed request")
	}
}

func TestRiskAuditRejectsInvalidPromptCatalogAndUnknownSelection(t *testing.T) {
	service := NewRiskControlService()
	service.Configure(Config{DataDir: t.TempDir()})
	cases := []RiskControlConfig{}
	for _, prompts := range [][]RiskSystemPrompt{
		{{ID: "", Name: "name", SystemPrompt: "prompt"}},
		{{ID: "same", Name: "one", SystemPrompt: "prompt"}, {ID: "same", Name: "two", SystemPrompt: "prompt"}},
		{{ID: "custom", Name: "", SystemPrompt: "prompt"}},
		{{ID: "custom", Name: "name", SystemPrompt: ""}},
		{{ID: defaultRiskSystemPromptID, Name: "tampered", SystemPrompt: "tampered"}},
		{{ID: "custom", Name: "name", SystemPrompt: "prompt", BuiltIn: true}},
	} {
		config := defaultRiskControlConfig()
		config.SystemPrompts = prompts
		cases = append(cases, config)
	}
	unknown := defaultRiskControlConfig()
	unknown.Audit.PromptID = "does-not-exist"
	cases = append(cases, unknown)
	for _, config := range cases {
		if _, err := service.UpdateConfig(config); err == nil {
			t.Fatalf("UpdateConfig(%#v) succeeded", config)
		}
	}
}

func TestRiskControlV1MigrationDoesNotReadCredentialEnvironment(t *testing.T) {
	const envKey = "CPA_RISK_AUDIT_KEY"
	const secret = "must-not-be-used"
	t.Setenv(envKey, secret)
	dataDir := t.TempDir()
	legacy := map[string]any{
		"version": 1,
		"config": map[string]any{
			"prompt_audit": map[string]any{"enabled": true, "mode": "observe", "endpoint": "https://guard.example.test/v1/chat/completions", "model": "guard", "credential_env": envKey},
			"custom_audit": map[string]any{},
		},
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, riskControlStoreName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewRiskControlService()
	service.Configure(Config{DataDir: dataDir})
	snapshot := service.Snapshot()
	if snapshot.Config.Audit.APIKey != "" || snapshot.Config.Audit.APIKeySet {
		t.Fatalf("migration imported environment credential: %#v", snapshot.Config.Audit)
	}
}

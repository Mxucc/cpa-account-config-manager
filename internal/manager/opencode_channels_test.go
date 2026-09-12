package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

// openCodeChannelEntry builds one CPA OpenAI-compatible channel entry.
func openCodeChannelEntry(name, baseURL, apiKey string, models ...string) map[string]any {
	rows := make([]any, 0, len(models))
	for _, model := range models {
		rows = append(rows, map[string]any{"name": model, "alias": model})
	}
	entry := map[string]any{"name": name, "base-url": baseURL, "models": rows}
	if apiKey != "" {
		entry["api-key-entries"] = []any{map[string]any{"api-key": apiKey}}
	}
	return entry
}

// openCodeChannelTestApp wires an app whose CPA channel list returns entries and
// records every channel write.
func openCodeChannelTestApp(t *testing.T, entries []map[string]any) (*App, *[][]map[string]any) {
	t.Helper()
	app := NewApp(&fakeAuthHost{}, nil)
	app.Configure([]byte("data_dir: " + t.TempDir()))
	writes := make([][]map[string]any, 0, 1)
	app.managementDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		payload, errEncode := json.Marshal(map[string]any{"openai-compatibility": entries})
		if errEncode != nil {
			return nil, errEncode
		}
		if request.Method == http.MethodPut {
			var items []map[string]any
			_ = json.NewDecoder(request.Body).Decode(&items)
			writes = append(writes, items)
			return jsonHTTPResponse(http.StatusOK, `{}`), nil
		}
		return jsonHTTPResponse(http.StatusOK, string(payload)), nil
	})
	return app, &writes
}

func TestOpenCodeChannelDetection(t *testing.T) {
	cases := []struct {
		name    string
		entry   map[string]any
		want    string
		matched bool
	}{
		{"go gateway", openCodeChannelEntry("", "https://opencode.ai/zen/go/v1", "sk-1"), "go", true},
		{"zen gateway", openCodeChannelEntry("", "https://opencode.ai/zen/v1", "sk-2"), "zen", true},
		{"self hosted bridge named opencode", openCodeChannelEntry("opencode-cc", "http://localhost:8787/v1", "sk-3"), "zen", true},
		{"bound channel label", openCodeChannelEntry("OpenCode Zen", "https://bridge.example/v1", "sk-4"), "zen", true},
		{"unrelated provider", openCodeChannelEntry("OpenRouter", "https://openrouter.ai/api/v1", "sk-5"), "", false},
		{"lookalike host", openCodeChannelEntry("evil", "https://evil-opencode.ai/zen", "sk-6"), "", false},
	}
	for _, testCase := range cases {
		kind, matched := openCodeChannelKindForEntry(testCase.entry)
		if matched != testCase.matched || kind != testCase.want {
			t.Fatalf("%s: kind=%q matched=%v, want %q/%v", testCase.name, kind, matched, testCase.want, testCase.matched)
		}
	}
}

func TestOpenCodeChannelListMarksImportedAndHidesKeys(t *testing.T) {
	entries := []map[string]any{
		openCodeChannelEntry("OpenCode Go wrk_alpha", "https://opencode.ai/zen/go/v1", "sk-go-secret", "glm-5.3", "kimi-k3"),
		openCodeChannelEntry("opencode-cc", "http://localhost:8787/v1", "sk-bridge-secret"),
		openCodeChannelEntry("OpenRouter", "https://openrouter.ai/api/v1", "sk-other"),
	}
	app, _ := openCodeChannelTestApp(t, entries)

	// An existing Go account for wrk_alpha makes that channel "already imported".
	accountID, errSave := app.opencode.SaveAccount("wrk_alpha", "cookie-alpha", "")
	if errSave != nil {
		t.Fatalf("save account: %v", errSave)
	}
	channels, errList := app.listOpenCodeChannels(context.Background(), "management-secret")
	if errList != nil {
		t.Fatalf("list channels: %v", errList)
	}
	if len(channels) != 2 {
		t.Fatalf("channels = %#v, want only the two OpenCode channels", channels)
	}
	goChannel, zenChannel := channels[0], channels[1]
	if goChannel.Kind != "go" || goChannel.WorkspaceID != "wrk_alpha" || !goChannel.Imported || goChannel.Models != 2 {
		t.Fatalf("Go channel view = %#v", goChannel)
	}
	if !goChannel.KeySet {
		t.Fatalf("Go channel key state was not reported: %#v", goChannel)
	}
	if zenChannel.Kind != "zen" || zenChannel.BaseURL != "http://localhost:8787/v1" || zenChannel.Imported {
		t.Fatalf("bridge channel view = %#v", zenChannel)
	}
	// The credential itself must never leave the plugin.
	encoded, errEncode := json.Marshal(channels)
	if errEncode != nil {
		t.Fatalf("encode channels: %v", errEncode)
	}
	for _, secret := range []string{"sk-go-secret", "sk-bridge-secret", "cookie-alpha"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("the channel list leaked %q: %s", secret, encoded)
		}
	}
	_ = accountID
}

func TestOpenCodeImportZenChannelCreatesAccount(t *testing.T) {
	entries := []map[string]any{openCodeChannelEntry("opencode-cc", "http://localhost:8787/v1", "sk-bridge-secret")}
	app, writes := openCodeChannelTestApp(t, entries)

	result, errImport := app.importOpenCodeChannel(context.Background(), "management-secret", "http://localhost:8787/v1")
	if errImport != nil {
		t.Fatalf("import: %v", errImport)
	}
	if result.Kind != "zen" || result.Action != openCodeImportActionCreateZen || result.AccountID == "" {
		t.Fatalf("import result = %#v", result)
	}
	accounts := app.opencodeZen.ListAccounts()
	if len(accounts) != 1 || accounts[0].BaseURL != "http://localhost:8787" || !accounts[0].KeySet {
		t.Fatalf("imported accounts = %#v", accounts)
	}
	if !accounts[0].ModelsFetchedAt.IsZero() {
		t.Fatalf("import must not fabricate a catalog")
	}
	// The "/v1" suffix is stripped once, so the plugin appends it itself instead of
	// requesting "/v1/v1/models" from the bridge.
	if strings.HasSuffix(accounts[0].BaseURL, "/v1") {
		t.Fatalf("import kept the OpenAI-compatible suffix: %q", accounts[0].BaseURL)
	}
	if len(*writes) != 0 {
		t.Fatalf("import must not write CPA channels: %#v", *writes)
	}
	// The imported account is now visible as imported.
	channels, errList := app.listOpenCodeChannels(context.Background(), "management-secret")
	if errList != nil || len(channels) != 1 || !channels[0].Imported {
		t.Fatalf("channel list after import = %#v err=%v", channels, errList)
	}
}

func TestOpenCodeImportGoChannelAttachesKeyToWorkspace(t *testing.T) {
	entries := []map[string]any{openCodeChannelEntry("OpenCode Go wrk_beta", "https://opencode.ai/zen/go/v1", "sk-go-secret")}
	app, _ := openCodeChannelTestApp(t, entries)
	if _, errSave := app.opencode.SaveAccount("wrk_beta", "cookie-beta", ""); errSave != nil {
		t.Fatalf("save account: %v", errSave)
	}

	result, errImport := app.importOpenCodeChannel(context.Background(), "management-secret", "https://opencode.ai/zen/go/v1")
	if errImport != nil {
		t.Fatalf("import: %v", errImport)
	}
	if result.Kind != "go" || result.Action != openCodeImportActionAttachKey || result.Name != "wrk_beta" {
		t.Fatalf("import result = %#v", result)
	}
	accounts := app.opencode.ListAccounts()
	if len(accounts) != 1 || !accounts[0].KeySet {
		t.Fatalf("the Go account did not receive the channel key: %#v", accounts)
	}
	// The raw key must not appear in the import response.
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "sk-go-secret") {
		t.Fatalf("the import result leaked the key: %s", encoded)
	}
}

func TestOpenCodeImportGoChannelWithoutWorkspaceAsksForCredentials(t *testing.T) {
	entries := []map[string]any{openCodeChannelEntry("OpenCode Go", "https://opencode.ai/zen/go/v1", "sk-go-secret")}
	app, _ := openCodeChannelTestApp(t, entries)

	if _, errImport := app.importOpenCodeChannel(context.Background(), "management-secret", "https://opencode.ai/zen/go/v1"); errImport != ErrOpenCodeImportNeedsWorkspace {
		t.Fatalf("import error = %v, want ErrOpenCodeImportNeedsWorkspace", errImport)
	}
	// A single keyless account is an unambiguous target.
	if _, errSave := app.opencode.SaveAccount("wrk_only", "cookie-only", ""); errSave != nil {
		t.Fatalf("save account: %v", errSave)
	}
	result, errImport := app.importOpenCodeChannel(context.Background(), "management-secret", "https://opencode.ai/zen/go/v1")
	if errImport != nil || result.Name != "wrk_only" {
		t.Fatalf("import with one keyless account = %#v err=%v", result, errImport)
	}
	// Two keyless accounts are ambiguous and must be refused.
	if _, errSave := app.opencode.SaveAccount("wrk_second", "cookie-second", ""); errSave != nil {
		t.Fatalf("save second account: %v", errSave)
	}
	app.opencode.mu.Lock()
	app.opencode.accounts[0].APIKey = ""
	app.opencode.accounts[1].APIKey = ""
	app.opencode.mu.Unlock()
	if _, errImport := app.importOpenCodeChannel(context.Background(), "management-secret", "https://opencode.ai/zen/go/v1"); errImport != ErrOpenCodeImportNeedsWorkspace {
		t.Fatalf("ambiguous import error = %v, want ErrOpenCodeImportNeedsWorkspace", errImport)
	}
}

func TestOpenCodeImportRejectsUnknownOrKeylessChannels(t *testing.T) {
	entries := []map[string]any{
		openCodeChannelEntry("OpenRouter", "https://openrouter.ai/api/v1", "sk-other"),
		openCodeChannelEntry("OpenCode Zen", "https://opencode.ai/zen/v1", ""),
	}
	app, _ := openCodeChannelTestApp(t, entries)

	if _, errImport := app.importOpenCodeChannel(context.Background(), "management-secret", "https://missing.example/v1"); errImport == nil {
		t.Fatalf("an unknown channel must not import")
	}
	if _, errImport := app.importOpenCodeChannel(context.Background(), "management-secret", "https://openrouter.ai/api/v1"); errImport == nil {
		t.Fatalf("a non-OpenCode channel must not import")
	}
	if _, errImport := app.importOpenCodeChannel(context.Background(), "management-secret", "https://opencode.ai/zen/v1"); errImport == nil ||
		!strings.Contains(errImport.Error(), "API key") {
		t.Fatalf("a keyless channel error = %v", errImport)
	}
	if len(app.opencodeZen.ListAccounts()) != 0 {
		t.Fatalf("a failed import must not create an account")
	}
}

// The routes stay behind the Management key, like every other OpenCode route.
func TestOpenCodeChannelRoutesRequireManagementKey(t *testing.T) {
	app, _ := openCodeChannelTestApp(t, nil)
	for _, route := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/opencode/channels", ""},
		{http.MethodPost, "/opencode/import", `{"base_url":"https://opencode.ai/zen/v1"}`},
	} {
		response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
			Method: route.method, Path: "/v0/management" + managementRoutePrefix + route.path,
			Body: []byte(route.body),
		})
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s status without key = %d", route.method, route.path, response.StatusCode)
		}
	}
}

// A Go channel import that needs the workspace credentials answers with 409 so the
// UI can send the operator to the account form instead of showing a server error.
func TestOpenCodeImportRouteReportsNeedsWorkspace(t *testing.T) {
	entries := []map[string]any{openCodeChannelEntry("OpenCode Go", "https://opencode.ai/zen/go/v1", "sk-go-secret")}
	app, _ := openCodeChannelTestApp(t, entries)
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + "/opencode/import",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}},
		Body:    []byte(`{"base_url":"https://opencode.ai/zen/go/v1"}`),
	})
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("import status = %d, body=%s", response.StatusCode, response.Body)
	}
	if !strings.Contains(string(response.Body), "needs_workspace") {
		t.Fatalf("import body = %s", response.Body)
	}
}

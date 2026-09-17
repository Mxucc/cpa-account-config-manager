package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

// autoRetryHostStub stands in for the CPA Management API during one apply pass:
// it serves the channel list, records every patch, and answers the two host retry
// knobs plus the configuration document.
type autoRetryHostStub struct {
	mu            sync.Mutex
	channels      []map[string]any
	codexChannels []map[string]any
	requestRetry  int
	retryInterval int
	cooldown      int
	patches       []map[string]any
	hostWrites    []string
}

func (s *autoRetryHostStub) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	switch request.URL.Path {
	case "/v0/management/codex-api-key":
		if request.Method == http.MethodPatch {
			s.patchRow(writer, request, s.codexChannels)
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"codex-api-key": s.codexChannels})
	case "/v0/management/openai-compatibility":
		if request.Method == http.MethodPatch {
			s.patchRow(writer, request, s.channels)
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"openai-compatibility": s.channels})
	case "/v0/management/max-retry-interval":
		s.serveInt(writer, request, &s.retryInterval, "max-retry-interval")
	case "/v0/management/request-retry":
		s.serveInt(writer, request, &s.requestRetry, "request-retry")
	case "/v0/management/config":
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"max-retry-credentials":            0,
			"transient-error-cooldown-seconds": s.cooldown,
			"streaming":                        map[string]any{"bootstrap-retries": 0},
		})
	default:
		writer.WriteHeader(http.StatusNotFound)
		_, _ = writer.Write([]byte(`{"error":"not found"}`))
	}
}

func (s *autoRetryHostStub) serveInt(writer http.ResponseWriter, request *http.Request, field *int, name string) {
	if request.Method == http.MethodPut {
		var body map[string]int
		_ = json.NewDecoder(request.Body).Decode(&body)
		if value, ok := body[name]; ok {
			*field = value
		}
		s.hostWrites = append(s.hostWrites, name)
	}
	_ = json.NewEncoder(writer).Encode(map[string]int{name: *field})
}

func (s *autoRetryHostStub) patchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.patches)
}

// patchRow records one patch and applies it to the row the index names, so a second
// pass can prove it finds the value already correct.
func (s *autoRetryHostStub) patchRow(writer http.ResponseWriter, request *http.Request, rows []map[string]any) {
	var body struct {
		Index *int           `json:"index"`
		Value map[string]any `json:"value"`
	}
	_ = json.NewDecoder(request.Body).Decode(&body)
	s.patches = append(s.patches, map[string]any{"index": body.Index, "value": body.Value})
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(rows) {
		for key, value := range body.Value {
			rows[*body.Index][key] = value
		}
	}
	_, _ = writer.Write([]byte(`{}`))
}

// newAutoRetryTestApp wires the smallest App an apply pass needs: the automatic
// retry service, an account service over the supplied host, and a Management API
// pointing at the stub.
func newAutoRetryTestApp(t *testing.T, stub *autoRetryHostStub, host AuthHost) (*App, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(stub.serveHTTP))
	t.Cleanup(server.Close)
	dataDir := t.TempDir()
	config := normalizeConfig(Config{DataDir: dataDir, ManagementBaseURL: server.URL})
	app := NewApp(host, nil)
	app.mu.Lock()
	app.config = config
	app.mu.Unlock()
	app.managementDoer = server.Client()
	app.autoRetry.Configure(config)
	return app, server
}

func TestAutoRetrySettingsDefaultBoundsAndPersistence(t *testing.T) {
	dataDir := t.TempDir()
	service := NewAutoRetryService()
	service.Configure(Config{DataDir: dataDir})
	if attempts := service.Attempts(); attempts != autoRetryDefaultAttempts {
		t.Fatalf("default attempts = %d, want %d", attempts, autoRetryDefaultAttempts)
	}
	if state := service.State(); !state.Enabled || state.MaxAttempts != autoRetryMaxAttempts {
		t.Fatalf("default state = %#v", state)
	}
	for _, rejected := range []int{-1, autoRetryMaxAttempts + 1} {
		if errSet := service.SetAttempts(rejected); errSet == nil {
			t.Fatalf("SetAttempts(%d) was accepted", rejected)
		}
	}
	for _, accepted := range []int{0, autoRetryMaxAttempts} {
		if errSet := service.SetAttempts(accepted); errSet != nil {
			t.Fatalf("SetAttempts(%d) error = %v", accepted, errSet)
		}
		if attempts := service.Attempts(); attempts != accepted {
			t.Fatalf("attempts = %d, want %d", attempts, accepted)
		}
	}
	if errSet := service.SetAttempts(0); errSet != nil {
		t.Fatalf("SetAttempts(0) error = %v", errSet)
	}
	if state := service.State(); state.Enabled {
		t.Fatalf("zero attempts still reported as enabled: %#v", state)
	}

	// The setting survives a restart, and the documented default replaces a value a
	// hand-edited file pushed out of range.
	restored := NewAutoRetryService()
	restored.Configure(Config{DataDir: dataDir})
	if attempts := restored.Attempts(); attempts != 0 {
		t.Fatalf("restored attempts = %d, want 0", attempts)
	}
	if errWrite := saveAutoRetrySettings(autoRetryStorePath(dataDir), 99); errWrite != nil {
		t.Fatalf("write out-of-range settings: %v", errWrite)
	}
	reloaded := NewAutoRetryService()
	reloaded.Configure(Config{DataDir: dataDir})
	if attempts := reloaded.Attempts(); attempts != autoRetryDefaultAttempts {
		t.Fatalf("out-of-range stored attempts = %d, want the default", attempts)
	}
}

// The three product families must all receive the budget, an unrelated channel must
// never be written, and the host interval must be raised far enough that a
// transient failure (which cools its credential for the host cooldown) can actually
// be retried - otherwise the client sees the error the setting promises to hide.
func TestAutoRetryApplyCoversEveryManagedProduct(t *testing.T) {
	host := &fakeAuthHost{}
	stub := &autoRetryHostStub{
		channels: []map[string]any{
			{"name": "OpenCode Zen", "base-url": "https://opencode.ai/zen/v1"},
			{"name": "Cline Pass", "base-url": clinePassDefaultBaseURL},
			{"name": "Some other relay", "base-url": "https://relay.example.com/v1"},
		},
		codexChannels: []map[string]any{
			{"api-key": "sk-codex-one", "base-url": "https://codex.example.com/v1"},
			{"api-key": "sk-codex-two", "base-url": "https://codex.example.com/v1"},
		},
		requestRetry:  3,
		retryInterval: 30,
	}
	app, _ := newAutoRetryTestApp(t, stub, host)
	if errSet := app.autoRetry.SetAttempts(5); errSet != nil {
		t.Fatalf("SetAttempts(5) error = %v", errSet)
	}

	result := app.runAutoRetryApply(context.Background(), "management-secret")
	if result.OpenCodeChannels != 1 || result.ClinePassChannels != 1 || result.CodexChannels != 2 || result.Skipped != 1 {
		t.Fatalf("apply result = %#v", result)
	}
	if result.Host.MaxRetryInterval != autoRetryTransientCooldownFallbackSeconds || !result.HostIntervalRaised {
		t.Fatalf("host interval was not raised over the transient cooldown: %#v", result.Host)
	}
	if result.Host.RequestRetry != 3 || result.HostRequestRetryRaised {
		t.Fatalf("a non-zero global request-retry was changed: %#v", result.Host)
	}
	if !result.Host.Configured {
		t.Fatalf("host state was not reported as configured: %#v", result.Host)
	}
	stub.mu.Lock()
	patches := append([]map[string]any(nil), stub.patches...)
	channels := append([]map[string]any(nil), stub.channels...)
	hostWrites := append([]string(nil), stub.hostWrites...)
	stub.mu.Unlock()
	// Two managed OpenAI-compatible rows plus the two Codex provider-channel rows.
	if len(patches) != 4 {
		t.Fatalf("patched %d rows, want the managed products and both Codex rows: %#v", len(patches), patches)
	}
	for _, patch := range patches {
		value, _ := patch["value"].(map[string]any)
		// The stub decodes the JSON body, so the number arrives as a float64.
		// Five operator retries are published as six attempts: the extra attempt is
		// the one the exhausted-retry interceptor terminates with a 503.
		if value["request-retry"] != float64(6) {
			t.Fatalf("patch payload = %#v", patch)
		}
	}
	if _, written := channels[2]["request-retry"]; written {
		t.Fatalf("an unrelated channel was written: %#v", channels[2])
	}
	if len(hostWrites) != 1 || hostWrites[0] != "max-retry-interval" {
		t.Fatalf("host writes = %#v, want only the retry interval", hostWrites)
	}

	// A second pass finds every row correct and writes nothing.
	before := stub.patchCount()
	if again := app.runAutoRetryApply(context.Background(), "management-secret"); again.OpenCodeChannels != 0 || again.ClinePassChannels != 0 {
		t.Fatalf("second pass rewrote rows: %#v", again)
	}
	if stub.patchCount() != before {
		t.Fatalf("second pass issued %d extra patches", stub.patchCount()-before)
	}
}

// Zero means "no retry" for the credentials the plugin manages, and it must not
// change host policy: an operator who disables the feature keeps the host exactly
// as configured.
func TestAutoRetryZeroDisablesWithoutTouchingHostPolicy(t *testing.T) {
	host := &fakeAuthHost{}
	stub := &autoRetryHostStub{
		channels:      []map[string]any{{"name": "Cline Pass", "base-url": clinePassDefaultBaseURL}},
		requestRetry:  3,
		retryInterval: 30,
	}
	app, _ := newAutoRetryTestApp(t, stub, host)
	if errSet := app.autoRetry.SetAttempts(0); errSet != nil {
		t.Fatalf("SetAttempts(0) error = %v", errSet)
	}
	result := app.runAutoRetryApply(context.Background(), "management-secret")
	if result.ClinePassChannels != 1 || result.HostIntervalRaised || result.HostRequestRetryRaised {
		t.Fatalf("disabling the feature changed the host: %#v", result)
	}
	stub.mu.Lock()
	patches := append([]map[string]any(nil), stub.patches...)
	hostWrites := append([]string(nil), stub.hostWrites...)
	stub.mu.Unlock()
	if len(patches) != 1 {
		t.Fatalf("patches = %#v", patches)
	}
	value, _ := patches[0]["value"].(map[string]any)
	if value["request-retry"] != float64(0) {
		t.Fatalf("disabled budget payload = %#v", patches[0])
	}
	if len(hostWrites) != 0 {
		t.Fatalf("host knobs were written while disabled: %#v", hostWrites)
	}
}

// A Codex account carries the budget in its auth file, because CPA hands the parsed
// file to the scheduler as-is. The file is rewritten only when the value differs,
// and every other key survives byte for byte.
func TestAutoRetryWritesCodexAuthFileOnlyWhenItChanges(t *testing.T) {
	host := &fakeAuthHost{
		details: map[string]cpaapi.HostAuthGetResponse{
			"auth-codex-1": {
				AuthIndex: "auth-codex-1",
				Name:      "codex-one.json",
				JSON:      json.RawMessage(`{"type":"codex","access_token":"token-secret","email":"a@example.com","tokens":{"id_token":"id-secret"}}`),
			},
		},
	}
	stub := &autoRetryHostStub{requestRetry: 3, retryInterval: 30}
	app, _ := newAutoRetryTestApp(t, stub, host)

	if changed := app.applyAutoRetryToCodexAuthFile(context.Background(), "auth-codex-1", "codex-one.json", 5); !changed {
		t.Fatal("the auth file was not updated")
	}
	host.mu.Lock()
	saves := append([]cpaapi.HostAuthSaveRequest(nil), host.saves...)
	host.mu.Unlock()
	if len(saves) != 1 {
		t.Fatalf("saves = %#v", saves)
	}
	var saved map[string]any
	if errDecode := json.Unmarshal(saves[0].JSON, &saved); errDecode != nil {
		t.Fatalf("decode the saved auth file: %v", errDecode)
	}
	if saved["request_retry"] != float64(5) {
		t.Fatalf("saved request_retry = %#v", saved["request_retry"])
	}
	for _, key := range []string{"type", "access_token", "email", "tokens"} {
		if _, kept := saved[key]; !kept {
			t.Fatalf("the rewrite dropped %q: %#v", key, saved)
		}
	}
	if got := string(saves[0].JSON); !strings.Contains(got, "a@example.com") || !strings.Contains(got, "token-secret") {
		t.Fatalf("the rewrite mangled a value: %s", got)
	}

	// The value is already correct, so a second pass must not touch the file again.
	if changed := app.applyAutoRetryToCodexAuthFile(context.Background(), "auth-codex-1", "codex-one.json", 5); changed {
		t.Fatal("an already-correct auth file was rewritten")
	}
	host.mu.Lock()
	saveCount := len(host.saves)
	host.mu.Unlock()
	if saveCount != 1 {
		t.Fatalf("saves after the second pass = %d, want 1", saveCount)
	}

	// A turned-off policy replaces the value instead of leaving retries enabled.
	if changed := app.applyAutoRetryToCodexAuthFile(context.Background(), "auth-codex-1", "codex-one.json", 0); !changed {
		t.Fatal("disabling the setting did not rewrite the auth file")
	}
	host.mu.Lock()
	raw := append(json.RawMessage(nil), host.details["auth-codex-1"].JSON...)
	host.mu.Unlock()
	if !strings.Contains(string(raw), `"request_retry":0`) {
		t.Fatalf("disabled auth file = %s", raw)
	}
}

// The host floor follows the reported transient cooldown, so a host that publishes
// its own cooldown gets an interval that covers it instead of a fixed constant.
func TestAutoRetryRequiredIntervalFollowsTheHostCooldown(t *testing.T) {
	cases := []struct {
		cooldown int
		want     int
	}{
		{cooldown: 0, want: autoRetryTransientCooldownFallbackSeconds},
		{cooldown: 5, want: autoRetryMinRetryIntervalSeconds},
		{cooldown: 120, want: 120},
	}
	for _, testCase := range cases {
		if got := autoRetryRequiredRetryInterval(testCase.cooldown); got != testCase.want {
			t.Fatalf("autoRetryRequiredRetryInterval(%d) = %d, want %d", testCase.cooldown, got, testCase.want)
		}
	}
}

// A positive operator budget is published as one extra attempt so the
// exhausted-retry interceptor has an attempt to terminate; zero stays zero
// because the feature is off and nothing may be intercepted.
func TestAutoRetryPublishedAttemptsKeepsZeroOffAndAddsTheInterceptedAttempt(t *testing.T) {
	cases := []struct {
		operator  int
		published int
	}{
		{operator: 0, published: 0},
		{operator: 1, published: 2},
		{operator: 5, published: 6},
		{operator: autoRetryMaxAttempts, published: autoRetryMaxAttempts + 1},
	}
	for _, testCase := range cases {
		if got := autoRetryPublishedAttempts(testCase.operator); got != testCase.published {
			t.Fatalf("autoRetryPublishedAttempts(%d) = %d, want %d", testCase.operator, got, testCase.published)
		}
	}
}

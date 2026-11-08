package manager

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

// stubLoginDoer answers the host's login endpoints from a table keyed by path,
// so the route tests never reach a real CPA.
type stubLoginDoer struct {
	answer map[string]stubLoginAnswer
	seen   []string
}

type stubLoginAnswer struct {
	status int
	body   string
}

func (s *stubLoginDoer) Do(req *http.Request) (*http.Response, error) {
	s.seen = append(s.seen, req.URL.RequestURI())
	for path, answer := range s.answer {
		if !strings.Contains(req.URL.RequestURI(), path) {
			continue
		}
		return &http.Response{
			StatusCode: answer.status,
			Body:       io.NopCloser(strings.NewReader(answer.body)),
			Header:     http.Header{},
			Request:    req,
		}, nil
	}
	return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}, Request: req}, nil
}

func newAccountLoginTestApp(t *testing.T, doer HTTPDoer) *App {
	t.Helper()
	app := NewApp(&fakeAuthHost{}, nil)
	app.Configure([]byte("data_dir: " + t.TempDir() + "\nmanagement_base_url: http://127.0.0.1:8317\n"))
	app.managementDoer = doer
	t.Cleanup(app.Close)
	return app
}

func accountLoginRequest(method, path string, body string, query map[string][]string) cpaapi.ManagementRequest {
	req := cpaapi.ManagementRequest{
		Method:  method,
		Path:    path,
		Query:   query,
		Headers: http.Header{"Authorization": []string{"Bearer test-key"}},
	}
	if body != "" {
		req.Body = []byte(body)
	}
	return req
}

// The page needs one call that says which providers it may offer, and it must
// separate the community plugins it cannot sign into from here.
func TestAccountLoginListsHostProvidersAndCommunityPlugins(t *testing.T) {
	app := newAccountLoginTestApp(t, &stubLoginDoer{})
	if _, errImport := app.providerImportStore().Import([]byte(`{"name":"@magpie-community/opencode-cline-auth","version":"0.1.14","main":"./opencode.mjs","providers":["cline"]}`), "magpie", ""); errImport != nil {
		t.Fatalf("ImportManifest: %v", errImport)
	}

	response := app.HandleManagement(context.Background(), accountLoginRequest(
		http.MethodGet, "/v0/management"+managementRoutePrefix+"/accounts/login", "", nil,
	))
	decoded := decodeProviderResponse(t, response)
	providers, ok := decoded["providers"].([]any)
	if !ok || len(providers) == 0 {
		t.Fatalf("providers = %#v", decoded["providers"])
	}
	found := map[string]map[string]any{}
	for _, entry := range providers {
		provider := entry.(map[string]any)
		found[provider["id"].(string)] = provider
	}
	if found["codex"]["flow"] != "browser" || found["codex"]["source"] != "host" {
		t.Fatalf("codex entry = %#v", found["codex"])
	}
	if found["xai"]["flow"] != "device" {
		t.Fatalf("xai entry = %#v", found["xai"])
	}
	if found["cline-pass"]["source"] != "plugin" {
		t.Fatalf("cline-pass entry = %#v", found["cline-pass"])
	}
	// An imported community provider now signs in through its own package, which
	// this plugin installs and runs, so it is offered with the community flow
	// instead of being sent to another app.
	community, ok := decoded["community"].([]any)
	if !ok || len(community) != 1 || community[0].(map[string]any)["flow"] != "community" {
		t.Fatalf("community = %#v", decoded["community"])
	}
}

// Starting a login returns CPA's own URL and state so the page can show the link
// and poll it.
func TestAccountLoginStartRelaysTheHostFlow(t *testing.T) {
	doer := &stubLoginDoer{answer: map[string]stubLoginAnswer{
		"/v8/management/oauth/auth-url": {status: http.StatusOK, body: `{"state":"xai-1","status":"ok","flow":"device","url":"https://accounts.x.ai/oauth2/device?user_code=AB12","user_code":"AB12","expires_in":1800}`},
	}}
	app := newAccountLoginTestApp(t, doer)

	response := app.HandleManagement(context.Background(), accountLoginRequest(
		http.MethodPost, "/v0/management"+managementRoutePrefix+"/accounts/login/start", `{"provider":"xai"}`, nil,
	))
	decoded := decodeProviderResponse(t, response)
	if decoded["provider"] != "xai" || decoded["state"] != "xai-1" || decoded["user_code"] != "AB12" {
		t.Fatalf("start = %#v", decoded)
	}
	if !strings.Contains(decoded["url"].(string), "accounts.x.ai") {
		t.Fatalf("url = %#v", decoded["url"])
	}
	if len(doer.seen) != 1 || !strings.Contains(doer.seen[0], "provider=xai") {
		t.Fatalf("host calls = %#v", doer.seen)
	}
}

// A provider the host does not serve is reported with the host's own reason, and
// the legacy per-provider endpoint is tried first for an older host.
func TestAccountLoginStartFallsBackToTheLegacyEndpoint(t *testing.T) {
	doer := &stubLoginDoer{answer: map[string]stubLoginAnswer{
		"/v0/management/codex-auth-url": {status: http.StatusOK, body: `{"state":"legacy-1","status":"ok","url":"https://auth.openai.com/oauth/authorize?x=1"}`},
	}}
	app := newAccountLoginTestApp(t, doer)

	response := app.HandleManagement(context.Background(), accountLoginRequest(
		http.MethodPost, "/v0/management"+managementRoutePrefix+"/accounts/login/start", `{"provider":"codex"}`, nil,
	))
	decoded := decodeProviderResponse(t, response)
	if decoded["state"] != "legacy-1" || decoded["flow"] != "browser" {
		t.Fatalf("legacy start = %#v", decoded)
	}
	if len(doer.seen) != 2 || !strings.Contains(doer.seen[1], "codex-auth-url") {
		t.Fatalf("host calls = %#v", doer.seen)
	}
}

func TestAccountLoginStartRejectsAnUnknownProviderSlug(t *testing.T) {
	app := newAccountLoginTestApp(t, &stubLoginDoer{})
	response := app.HandleManagement(context.Background(), accountLoginRequest(
		http.MethodPost, "/v0/management"+managementRoutePrefix+"/accounts/login/start", `{"provider":"../etc/passwd"}`, nil,
	))
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", response.StatusCode, response.Body)
	}
}

// A pending login answers with no body; that is a wait, not a failure.
func TestAccountLoginStatusTreatsAnEmptyAnswerAsPending(t *testing.T) {
	doer := &stubLoginDoer{answer: map[string]stubLoginAnswer{
		"/v8/management/oauth/status": {status: http.StatusOK, body: ""},
	}}
	app := newAccountLoginTestApp(t, doer)

	response := app.HandleManagement(context.Background(), accountLoginRequest(
		http.MethodGet, "/v0/management"+managementRoutePrefix+"/accounts/login/status", "", map[string][]string{"state": {"abc"}},
	))
	decoded := decodeProviderResponse(t, response)
	if decoded["done"] != false || decoded["status"] != "wait" || decoded["state"] != "abc" {
		t.Fatalf("pending = %#v", decoded)
	}
}

func TestAccountLoginStatusReportsCompletion(t *testing.T) {
	doer := &stubLoginDoer{answer: map[string]stubLoginAnswer{
		"/v8/management/oauth/status": {status: http.StatusOK, body: `{"status":"ok","message":"saved"}`},
	}}
	app := newAccountLoginTestApp(t, doer)

	response := app.HandleManagement(context.Background(), accountLoginRequest(
		http.MethodGet, "/v0/management"+managementRoutePrefix+"/accounts/login/status", "", map[string][]string{"state": {"abc"}},
	))
	decoded := decodeProviderResponse(t, response)
	if decoded["done"] != true || decoded["message"] != "saved" {
		t.Fatalf("done = %#v", decoded)
	}
}

// An unknown state is reported by the host, and the plugin must not turn it into
// a completed login.
func TestAccountLoginStatusReportsAHostError(t *testing.T) {
	doer := &stubLoginDoer{answer: map[string]stubLoginAnswer{
		"/v8/management/oauth/status": {status: http.StatusOK, body: `{"error":"unknown or expired state","status":"error"}`},
	}}
	app := newAccountLoginTestApp(t, doer)

	var decoded map[string]any
	body := app.HandleManagement(context.Background(), accountLoginRequest(
		http.MethodGet, "/v0/management"+managementRoutePrefix+"/accounts/login/status", "", map[string][]string{"state": {"abc"}},
	)).Body
	if errDecode := json.Unmarshal(body, &decoded); errDecode != nil {
		t.Fatalf("decode: %v", errDecode)
	}
	if decoded["done"] != false || decoded["status"] != "error" || decoded["message"] != "unknown or expired state" {
		t.Fatalf("error = %#v", decoded)
	}
}

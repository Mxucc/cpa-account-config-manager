package manager

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// clinePassFakeGateway answers every outbound Cline Pass call the service makes:
// the WorkOS device endpoints, the Cline token endpoints, the model catalog, the
// chat probe and the npm version lookup. Nothing in these tests touches the
// network.
type clinePassFakeGateway struct {
	mu               sync.Mutex
	deviceCalls      int
	authenticateCall int
	registerCalls    int
	refreshCalls     int
	modelsCalls      int
	chatCalls        int
	forcePending     bool
	forceSlowDown    bool
	authenticateFail bool
	deviceFail       bool
	modelsStatus     int
	chatStatus       int
	registerBody     string
	refreshBody      string
	modelsBody       string
	// chatPayload records the last chat completion request the probe sent, so a
	// test can pin the output budget it asks for.
	chatPayload map[string]any
}

func newClinePassFakeGateway() *clinePassFakeGateway {
	return &clinePassFakeGateway{
		registerBody: `{"success":true,"data":{"accessToken":"registered-access","refreshToken":"registered-refresh","expiresAt":"` +
			time.Now().UTC().Add(time.Hour).Format(time.RFC3339) + `"}}`,
		refreshBody: `{"data":{"accessToken":"rotated-access","refreshToken":"rotated-refresh"}}`,
		modelsBody:  `{"data":[{"id":"cline-pass/glm-5.3"},{"id":"not/allow-listed"}]}`,
	}
}

func (g *clinePassFakeGateway) Do(request *http.Request) (*http.Response, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	host := request.URL.Host
	path := request.URL.Path
	switch {
	case strings.HasSuffix(host, "workos.com") && strings.HasSuffix(path, "/authorize/device"):
		g.deviceCalls++
		if g.deviceFail {
			return jsonHTTPResponse(http.StatusBadRequest, `{"error":"invalid_client","error_description":"device disabled"}`), nil
		}
		return jsonHTTPResponse(http.StatusOK, `{"device_code":"device-1","user_code":"USER-CODE","verification_uri":"https://cline.bot/device","verification_uri_complete":"https://cline.bot/device?code=USER-CODE","expires_in":600,"interval":5}`), nil
	case strings.HasSuffix(host, "workos.com") && strings.HasSuffix(path, "/authenticate"):
		g.authenticateCall++
		if g.authenticateFail {
			return jsonHTTPResponse(http.StatusBadRequest, `{"error":"expired_token","error_description":"expired"}`), nil
		}
		if g.forceSlowDown {
			return jsonHTTPResponse(http.StatusBadRequest, `{"error":"slow_down"}`), nil
		}
		if g.forcePending {
			return jsonHTTPResponse(http.StatusBadRequest, `{"error":"authorization_pending"}`), nil
		}
		return jsonHTTPResponse(http.StatusOK, `{"access_token":"access-1","refresh_token":"refresh-1"}`), nil
	case strings.HasSuffix(host, "cline.bot") && strings.HasSuffix(path, "/auth/register"):
		g.registerCalls++
		return jsonHTTPResponse(http.StatusOK, g.registerBody), nil
	case strings.HasSuffix(host, "cline.bot") && strings.HasSuffix(path, "/auth/refresh"):
		g.refreshCalls++
		return jsonHTTPResponse(http.StatusOK, g.refreshBody), nil
	case strings.HasSuffix(path, "/models"):
		g.modelsCalls++
		if g.modelsStatus >= 400 {
			return jsonHTTPResponse(g.modelsStatus, `{"error":{"message":"unauthorized"}}`), nil
		}
		return jsonHTTPResponse(http.StatusOK, g.modelsBody), nil
	case strings.HasSuffix(path, "/chat/completions"):
		g.chatCalls++
		var payload map[string]any
		if request.Body != nil {
			if raw, errRead := io.ReadAll(io.LimitReader(request.Body, 1<<16)); errRead == nil {
				_ = json.Unmarshal(raw, &payload)
			}
		}
		g.chatPayload = payload
		if g.chatStatus >= 400 {
			if g.chatStatus == http.StatusNotFound {
				return jsonHTTPResponse(g.chatStatus, `{"error":{"message":"ModelError: not supported"}}`), nil
			}
			return jsonHTTPResponse(g.chatStatus, `{"error":{"message":"unauthorized"}}`), nil
		}
		return jsonHTTPResponse(http.StatusOK, `{"choices":[{"message":{"role":"assistant","content":"pong"}}]}`), nil
	case strings.Contains(host, "npmjs.org"):
		return jsonHTTPResponse(http.StatusOK, `{"version":"3.0.99"}`), nil
	}
	return jsonHTTPResponse(http.StatusNotFound, `{}`), nil
}

func (g *clinePassFakeGateway) counters() (device, authenticate, register, refresh, models, chat int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.deviceCalls, g.authenticateCall, g.registerCalls, g.refreshCalls, g.modelsCalls, g.chatCalls
}

func newConfiguredClinePassService(t *testing.T, dataDir string) (*ClinePassService, *clinePassFakeGateway) {
	t.Helper()
	gateway := newClinePassFakeGateway()
	service := NewClinePassService()
	service.Configure(Config{DataDir: dataDir})
	service.SetHTTPDoer(gateway)
	return service, gateway
}

func TestClinePassBaseURLNormalization(t *testing.T) {
	cases := map[string]string{
		"":                                 clinePassDefaultBaseURL,
		"https://api.cline.bot":            clinePassDefaultBaseURL,
		"https://api.cline.bot/":           clinePassDefaultBaseURL,
		"https://api.cline.bot/api/v1/":    clinePassDefaultBaseURL,
		"  https://api.cline.bot/api/v1  ": clinePassDefaultBaseURL,
		"http://127.0.0.1:8080/llm/v1":     "http://127.0.0.1:8080/llm/v1",
	}
	for input, want := range cases {
		if got := normalizeClinePassBaseURL(input); got != want {
			t.Fatalf("normalizeClinePassBaseURL(%q) = %q, want %q", input, got, want)
		}
	}
	if validClinePassBaseURL("not-a-url") {
		t.Fatal("validClinePassBaseURL accepted a relative value")
	}
	if !validClinePassBaseURL(clinePassDefaultBaseURL) {
		t.Fatal("validClinePassBaseURL rejected the default base URL")
	}
}

// The published catalog is an explicit allow-list: an unknown upstream id can
// never reach the channel or the UI.
func TestClinePassCatalogIsAnAllowList(t *testing.T) {
	if len(clinePassCatalog) == 0 {
		t.Fatal("the Cline Pass catalog is empty")
	}
	if got := normalizeClinePassModels([]string{"cline-pass/glm-5.3", "not/allow-listed", "", "cline-pass/glm-5.3"}); len(got) != 1 || got[0] != "cline-pass/glm-5.3" {
		t.Fatalf("normalizeClinePassModels() = %#v", got)
	}
	for _, id := range clinePassCatalogIDs() {
		if !clinePassAllowsModel(id) {
			t.Fatalf("catalog id %q is not allow-listed by its own catalog", id)
		}
	}
	if len(clinePassCatalogIDs()) != len(clinePassCatalog) {
		t.Fatal("clinePassCatalogIDs lost entries")
	}
}

func TestClinePassSavePersistsRedactsAndRemoves(t *testing.T) {
	dataDir := t.TempDir()
	service, _ := newConfiguredClinePassService(t, dataDir)

	id, errSave := service.SaveAPIKeyAccount("", "work", "", "sk-cline-super-secret")
	if errSave != nil || id == "" {
		t.Fatalf("SaveAPIKeyAccount() id=%q err=%v", id, errSave)
	}
	raw, errRead := os.ReadFile(filepath.Join(dataDir, clinePassStoreFileName))
	if errRead != nil {
		t.Fatalf("read store: %v", errRead)
	}
	if !strings.Contains(string(raw), "sk-cline-super-secret") {
		t.Fatal("the private store does not hold the credential")
	}
	views := service.ListAccounts()
	if len(views) != 1 {
		t.Fatalf("ListAccounts() = %+v", views)
	}
	if views[0].BaseURL != clinePassDefaultBaseURL || !views[0].AccessTokenSet || views[0].AuthMethod != clinePassAuthMethodAPIKey {
		t.Fatalf("view = %#v", views[0])
	}
	if views[0].ExpiresAt != nil || views[0].Expired {
		t.Fatalf("a static API key must not expire: %#v", views[0])
	}
	if len(views[0].Models) != len(clinePassCatalog) {
		t.Fatalf("view models = %d, want the catalog", len(views[0].Models))
	}
	encoded, _ := json.Marshal(views)
	if strings.Contains(string(encoded), "sk-cline-super-secret") {
		t.Fatal("the redacted view leaked the credential")
	}

	// Reload from disk: the credential survives a restart.
	reloaded := NewClinePassService()
	reloaded.Configure(Config{DataDir: dataDir})
	if got := reloaded.ListAccounts(); len(got) != 1 || got[0].ID != id {
		t.Fatalf("reloaded accounts = %+v", got)
	}

	// Renaming an account without a new key keeps the stored credential.
	if _, errUpdate := service.SaveAPIKeyAccount(id, "renamed", "", ""); errUpdate != nil {
		t.Fatalf("rename: %v", errUpdate)
	}
	service.mu.RLock()
	kept := service.accounts[0].AccessToken
	service.mu.RUnlock()
	if kept != "sk-cline-super-secret" {
		t.Fatalf("empty api_key replaced the stored credential: %q", kept)
	}

	if errRemove := service.RemoveAccount(id); errRemove != nil {
		t.Fatalf("RemoveAccount() error = %v", errRemove)
	}
	if got := service.ListAccounts(); len(got) != 0 {
		t.Fatalf("accounts after remove = %+v", got)
	}
}

func TestClinePassDeviceLoginCompletesAndStoresAccount(t *testing.T) {
	dataDir := t.TempDir()
	service, gateway := newConfiguredClinePassService(t, dataDir)

	started, errStart := service.StartDeviceLogin(context.Background(), "work laptop")
	if errStart != nil {
		t.Fatalf("StartDeviceLogin() error = %v", errStart)
	}
	if started.Status != clinePassLoginPending || started.SessionID == "" {
		t.Fatalf("start view = %#v", started)
	}
	if started.UserCode != "USER-CODE" || started.VerificationURI != "https://cline.bot/device" {
		t.Fatalf("start view = %#v", started)
	}
	if started.VerificationURIComplete == "" || started.ExpiresInSeconds <= 0 || started.IntervalSeconds != 5 {
		t.Fatalf("start view = %#v", started)
	}

	polled, errPoll := service.PollDeviceLogin(context.Background(), started.SessionID)
	if errPoll != nil {
		t.Fatalf("PollDeviceLogin() error = %v", errPoll)
	}
	if polled.Status != clinePassLoginCompleted || polled.Account == nil {
		t.Fatalf("poll view = %#v", polled)
	}
	if !polled.Account.AccessTokenSet || !polled.Account.RefreshTokenSet {
		t.Fatalf("poll account = %#v", polled.Account)
	}
	if polled.Account.Name != "work laptop" {
		t.Fatalf("poll account name = %q", polled.Account.Name)
	}

	accounts := service.ListAccounts()
	if len(accounts) != 1 || accounts[0].AuthMethod != clinePassAuthMethodOAuth {
		t.Fatalf("accounts = %+v", accounts)
	}
	service.mu.RLock()
	stored := service.accounts[0]
	service.mu.RUnlock()
	if !strings.HasPrefix(stored.AccessToken, clinePassWorkOSTokenPrefix) {
		t.Fatalf("stored access token = %q, want the workos: prefix", stored.AccessToken)
	}
	// The registered expiry is authoritative.
	if stored.ExpiresAt.IsZero() || !stored.ExpiresAt.After(time.Now()) {
		t.Fatalf("stored expiry = %v", stored.ExpiresAt)
	}
	encoded, _ := json.Marshal(accounts)
	if strings.Contains(string(encoded), "registered-access") || strings.Contains(string(encoded), "registered-refresh") {
		t.Fatal("the redacted view leaked a token")
	}
	if raw, errRead := os.ReadFile(filepath.Join(dataDir, clinePassStoreFileName)); errRead != nil || !strings.Contains(string(raw), "registered-access") {
		t.Fatalf("store does not hold the registered token: err=%v", errRead)
	}
	if _, _, register, _, _, _ := gateway.counters(); register != 1 {
		t.Fatalf("register calls = %d", register)
	}
}

func TestClinePassDeviceLoginPollingIsBounded(t *testing.T) {
	service, gateway := newConfiguredClinePassService(t, t.TempDir())
	gateway.forcePending = true

	started, errStart := service.StartDeviceLogin(context.Background(), "")
	if errStart != nil {
		t.Fatalf("StartDeviceLogin() error = %v", errStart)
	}
	first, errFirst := service.PollDeviceLogin(context.Background(), started.SessionID)
	if errFirst != nil || first.Status != clinePassLoginPending {
		t.Fatalf("first poll = %#v err=%v", first, errFirst)
	}
	// A second immediate poll is rate-limited: it must not reach the identity provider.
	second, errSecond := service.PollDeviceLogin(context.Background(), started.SessionID)
	if errSecond != nil || second.Status != clinePassLoginPending {
		t.Fatalf("second poll = %#v err=%v", second, errSecond)
	}
	if _, authenticate, _, _, _, _ := gateway.counters(); authenticate != 1 {
		t.Fatalf("authenticate calls = %d, want exactly one while rate-limited", authenticate)
	}

	// RFC 8628: slow_down raises the interval by five seconds.
	gateway.mu.Lock()
	gateway.forcePending = false
	gateway.forceSlowDown = true
	gateway.mu.Unlock()
	service.mu.Lock()
	for _, session := range service.logins {
		session.NextPollAt = time.Time{}
	}
	service.mu.Unlock()
	slowed, errSlow := service.PollDeviceLogin(context.Background(), started.SessionID)
	if errSlow != nil || slowed.Status != clinePassLoginPending {
		t.Fatalf("slow_down poll = %#v err=%v", slowed, errSlow)
	}
	if slowed.IntervalSeconds != started.IntervalSeconds+5 {
		t.Fatalf("interval after slow_down = %d, want %d", slowed.IntervalSeconds, started.IntervalSeconds+5)
	}
}

func TestClinePassDeviceLoginCancelAndFailure(t *testing.T) {
	service, gateway := newConfiguredClinePassService(t, t.TempDir())
	started, errStart := service.StartDeviceLogin(context.Background(), "")
	if errStart != nil {
		t.Fatalf("StartDeviceLogin() error = %v", errStart)
	}
	if !service.CancelDeviceLogin(started.SessionID) {
		t.Fatal("CancelDeviceLogin() = false")
	}
	cancelled, errCancelled := service.PollDeviceLogin(context.Background(), started.SessionID)
	if errCancelled != nil || cancelled.Status != clinePassLoginCancelled {
		t.Fatalf("cancelled poll = %#v err=%v", cancelled, errCancelled)
	}
	if service.CancelDeviceLogin("clinelogin_missing") {
		t.Fatal("CancelDeviceLogin() accepted an unknown session")
	}
	if _, errUnknown := service.PollDeviceLogin(context.Background(), "clinelogin_missing"); errUnknown == nil {
		t.Fatal("PollDeviceLogin() accepted an unknown session")
	}

	// An upstream rejection is reported on the view, not as a transport error.
	gateway.mu.Lock()
	gateway.authenticateFail = true
	gateway.forcePending = false
	gateway.forceSlowDown = false
	gateway.mu.Unlock()
	second, errSecond := service.StartDeviceLogin(context.Background(), "")
	if errSecond != nil {
		t.Fatalf("second StartDeviceLogin() error = %v", errSecond)
	}
	failed, errFailed := service.PollDeviceLogin(context.Background(), second.SessionID)
	if errFailed != nil || failed.Status != clinePassLoginFailed || failed.Error == "" {
		t.Fatalf("failed poll = %#v err=%v", failed, errFailed)
	}
	if strings.Contains(failed.Error, "sk-") {
		t.Fatalf("failure detail looks like a credential: %q", failed.Error)
	}

	// A device-authorization failure is an error: there is no session to poll.
	gateway.mu.Lock()
	gateway.deviceFail = true
	gateway.mu.Unlock()
	if _, errDevice := service.StartDeviceLogin(context.Background(), ""); errDevice == nil {
		t.Fatal("StartDeviceLogin() ignored a device authorization failure")
	}
}

func TestClinePassRefreshRotatesTokensAndPersists(t *testing.T) {
	dataDir := t.TempDir()
	service, gateway := newConfiguredClinePassService(t, dataDir)
	started, errStart := service.StartDeviceLogin(context.Background(), "rotating")
	if errStart != nil {
		t.Fatalf("StartDeviceLogin() error = %v", errStart)
	}
	completed, errPoll := service.PollDeviceLogin(context.Background(), started.SessionID)
	if errPoll != nil || completed.Account == nil {
		t.Fatalf("poll = %#v err=%v", completed, errPoll)
	}
	accountID := completed.Account.ID

	view, errRefresh := service.RefreshToken(context.Background(), accountID)
	if errRefresh != nil {
		t.Fatalf("RefreshToken() error = %v", errRefresh)
	}
	if view.Expired || view.ExpiresAt == nil || !view.ExpiresAt.After(time.Now()) {
		t.Fatalf("refreshed view = %#v", view)
	}
	service.mu.RLock()
	stored := service.accounts[0]
	service.mu.RUnlock()
	if stored.AccessToken != clinePassWorkOSTokenPrefix+"rotated-access" {
		t.Fatalf("stored access token = %q", stored.AccessToken)
	}
	if stored.RefreshToken != "rotated-refresh" {
		t.Fatalf("stored refresh token = %q, want the rotated value", stored.RefreshToken)
	}
	if raw, errRead := os.ReadFile(filepath.Join(dataDir, clinePassStoreFileName)); errRead != nil || !strings.Contains(string(raw), "rotated-refresh") {
		t.Fatalf("rotated refresh token was not persisted: err=%v", errRead)
	}
	if _, _, _, refresh, _, _ := gateway.counters(); refresh != 1 {
		t.Fatalf("refresh calls = %d", refresh)
	}

	// A static API key is never rotated.
	keyID, errSave := service.SaveAPIKeyAccount("", "static", "", "sk-static")
	if errSave != nil {
		t.Fatalf("SaveAPIKeyAccount() error = %v", errSave)
	}
	if _, errKey := service.RefreshToken(context.Background(), keyID); errKey != nil {
		t.Fatalf("RefreshToken() on a static key: %v", errKey)
	}
	if _, _, _, refresh, _, _ := gateway.counters(); refresh != 1 {
		t.Fatalf("a static key triggered a refresh call (total=%d)", refresh)
	}
}

// An expired rotating token is refreshed before the credential is used.
func TestClinePassCredentialRefreshesExpiredToken(t *testing.T) {
	service, gateway := newConfiguredClinePassService(t, t.TempDir())
	started, errStart := service.StartDeviceLogin(context.Background(), "expiring")
	if errStart != nil {
		t.Fatalf("StartDeviceLogin() error = %v", errStart)
	}
	completed, errPoll := service.PollDeviceLogin(context.Background(), started.SessionID)
	if errPoll != nil || completed.Account == nil {
		t.Fatalf("poll = %#v err=%v", completed, errPoll)
	}
	service.mu.Lock()
	service.accounts[0].ExpiresAt = time.Now().Add(-time.Hour)
	service.mu.Unlock()

	credential, errCredential := service.credential(context.Background(), completed.Account.ID)
	if errCredential != nil {
		t.Fatalf("credential() error = %v", errCredential)
	}
	if credential.APIKey != clinePassWorkOSTokenPrefix+"rotated-access" {
		t.Fatalf("credential key = %q", credential.APIKey)
	}
	if _, _, _, refresh, _, _ := gateway.counters(); refresh != 1 {
		t.Fatalf("refresh calls = %d, want one proactive refresh", refresh)
	}
}

func TestClinePassManagementRoutes(t *testing.T) {
	dataDir := t.TempDir()
	service, gateway := newConfiguredClinePassService(t, dataDir)

	channelWrites := make([][]map[string]any, 0, 1)
	app := NewApp(&fakeAuthHost{}, nil)
	app.clinePass = service
	app.managementDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodGet {
			return jsonHTTPResponse(http.StatusOK, `{"openai-compatibility":[]}`), nil
		}
		var items []map[string]any
		_ = json.NewDecoder(request.Body).Decode(&items)
		channelWrites = append(channelWrites, items)
		return jsonHTTPResponse(http.StatusOK, `{}`), nil
	})
	headers := http.Header{"Authorization": []string{"Bearer management-secret"}}

	// Every new route requires the management key.
	for _, route := range []string{"/opencode/cline-pass/accounts", "/opencode/cline-pass/models", "/opencode/cline-pass/model-test", "/opencode/cline-pass/bind", "/opencode/cline-pass/refresh", "/opencode/cline-pass/login/start", "/opencode/cline-pass/login/poll", "/opencode/cline-pass/login/cancel"} {
		response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
			Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + route, Body: []byte(`{}`),
		})
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s without a management key = %d", route, response.StatusCode)
		}
	}
	if response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/catalog",
	}); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("catalog without a management key = %d", response.StatusCode)
	}

	// The catalog is the allow-list and names the default base URL.
	catalogResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/catalog", Headers: headers,
	})
	if catalogResponse.StatusCode != http.StatusOK {
		t.Fatalf("catalog status = %d body=%s", catalogResponse.StatusCode, catalogResponse.Body)
	}
	var catalog clinePassCatalogResponse
	if errDecode := json.Unmarshal(catalogResponse.Body, &catalog); errDecode != nil {
		t.Fatalf("decode catalog: %v", errDecode)
	}
	if len(catalog.Models) != len(clinePassCatalog) || catalog.DefaultBaseURL != clinePassDefaultBaseURL {
		t.Fatalf("catalog = %#v", catalog)
	}

	// Saving a pasted API key stores it and probes the gateway.
	saveResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/accounts", Headers: headers,
		Body: []byte(`{"name":"pasted","api_key":"sk-cline-secret"}`),
	})
	if saveResponse.StatusCode != http.StatusOK {
		t.Fatalf("save status = %d body=%s", saveResponse.StatusCode, saveResponse.Body)
	}
	if strings.Contains(string(saveResponse.Body), "sk-cline-secret") {
		t.Fatal("the save response leaked the credential")
	}
	var savePayload struct {
		Account ClinePassAccountView `json:"account"`
		Result  ClinePassProbeResult `json:"result"`
	}
	if errDecode := json.Unmarshal(saveResponse.Body, &savePayload); errDecode != nil {
		t.Fatalf("decode save response: %v", errDecode)
	}
	accountID := savePayload.Account.ID
	if accountID == "" || !savePayload.Account.AccessTokenSet || !savePayload.Result.Reachable {
		t.Fatalf("save payload = %#v", savePayload)
	}

	listResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/accounts", Headers: headers,
	})
	if listResponse.StatusCode != http.StatusOK || strings.Contains(string(listResponse.Body), "sk-cline-secret") {
		t.Fatalf("list status = %d body=%s", listResponse.StatusCode, listResponse.Body)
	}
	var listPayload clinePassAccountsResponse
	if errDecode := json.Unmarshal(listResponse.Body, &listPayload); errDecode != nil || len(listPayload.Accounts) != 1 {
		t.Fatalf("list payload = %+v err=%v", listPayload, errDecode)
	}

	// Catalog refresh validates the credential and keeps the allow-list.
	modelsResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/models", Headers: headers,
		Body: []byte(`{"account_id":"` + accountID + `"}`),
	})
	if modelsResponse.StatusCode != http.StatusOK {
		t.Fatalf("models status = %d body=%s", modelsResponse.StatusCode, modelsResponse.Body)
	}
	if strings.Contains(string(modelsResponse.Body), "not/allow-listed") {
		t.Fatal("an upstream model id expanded the published allow-list")
	}

	// A real model probe reports the gateway answer.
	testResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/model-test", Headers: headers,
		Body: []byte(`{"account_id":"` + accountID + `","model":"cline-pass/glm-5.3"}`),
	})
	if testResponse.StatusCode != http.StatusOK {
		t.Fatalf("model-test status = %d body=%s", testResponse.StatusCode, testResponse.Body)
	}
	var testPayload struct {
		Result OpenCodeModelTestResult `json:"result"`
	}
	if errDecode := json.Unmarshal(testResponse.Body, &testPayload); errDecode != nil {
		t.Fatalf("decode model-test: %v", errDecode)
	}
	if testPayload.Result.Status != "available" || testPayload.Result.Endpoint != "chat" {
		t.Fatalf("model-test result = %#v", testPayload.Result)
	}
	if _, _, _, _, _, chat := gateway.counters(); chat == 0 {
		t.Fatal("model-test did not call the chat endpoint")
	}

	// Binding publishes the channel with the Cline identity headers.
	bindResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/bind", Headers: headers,
		Body: []byte(`{"account_id":"` + accountID + `"}`),
	})
	if bindResponse.StatusCode != http.StatusOK {
		t.Fatalf("bind status = %d body=%s", bindResponse.StatusCode, bindResponse.Body)
	}
	// The save above already bound the account once, so the explicit bind is the
	// second channel write.
	if len(channelWrites) != 2 || len(channelWrites[1]) != 1 {
		t.Fatalf("channel writes = %#v", channelWrites)
	}
	entry := channelWrites[1][0]
	if entry["base-url"] != clinePassDefaultBaseURL {
		t.Fatalf("bound base-url = %#v", entry["base-url"])
	}
	boundHeaders, _ := entry["headers"].(map[string]any)
	if boundHeaders["x-client-type"] != "cli" || !strings.HasPrefix(boundHeaders["User-Agent"].(string), "Cline/") {
		t.Fatalf("bound headers = %#v", entry["headers"])
	}
	rows, _ := entry["api-key-entries"].([]any)
	if len(rows) != 1 {
		t.Fatalf("bound key rows = %#v", entry["api-key-entries"])
	}
	firstRow, _ := rows[0].(map[string]any)
	if firstRow["api-key"] != "sk-cline-secret" {
		t.Fatalf("bound key row = %#v", firstRow)
	}
	channelModels, _ := entry["models"].([]any)
	if len(channelModels) != len(clinePassCatalog) {
		t.Fatalf("bound channel models = %d, want the allow-list", len(channelModels))
	}
	if name, _ := entry["name"].(string); !strings.HasPrefix(name, clinePassBoundChannelName) {
		t.Fatalf("bound channel name = %#v", entry["name"])
	}

	// Refreshing with rebind republishes the channel key.
	refreshResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/refresh", Headers: headers,
		Body: []byte(`{"account_id":"` + accountID + `","rebind":true}`),
	})
	if refreshResponse.StatusCode != http.StatusOK {
		t.Fatalf("refresh status = %d body=%s", refreshResponse.StatusCode, refreshResponse.Body)
	}
	if len(channelWrites) != 3 || len(channelWrites[2]) != 1 {
		t.Fatalf("rebind did not update the channel in place: %#v", channelWrites)
	}

	// Removing the account empties the list.
	removeResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodDelete, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/accounts", Headers: headers,
		Query: map[string][]string{"account_id": {accountID}},
	})
	if removeResponse.StatusCode != http.StatusOK {
		t.Fatalf("remove status = %d body=%s", removeResponse.StatusCode, removeResponse.Body)
	}
	afterRemove := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/accounts", Headers: headers,
	})
	var afterPayload clinePassAccountsResponse
	if errDecode := json.Unmarshal(afterRemove.Body, &afterPayload); errDecode != nil || len(afterPayload.Accounts) != 0 {
		t.Fatalf("accounts after remove = %+v err=%v", afterPayload, errDecode)
	}
}

func TestClinePassPersistenceFailureIsSanitized(t *testing.T) {
	blockingPath := filepath.Join(t.TempDir(), "not-a-directory")
	if errWrite := os.WriteFile(blockingPath, []byte("block"), 0o600); errWrite != nil {
		t.Fatalf("write blocker: %v", errWrite)
	}
	service := NewClinePassService()
	service.Configure(Config{DataDir: blockingPath})
	_, errSave := service.SaveAPIKeyAccount("", "secret", "", "sk-super-secret")
	if errSave == nil {
		t.Fatal("SaveAPIKeyAccount() error = nil")
	}
	if got := service.StorageError(); got != "Cline Pass state could not be persisted" {
		t.Fatalf("StorageError = %q", got)
	}
	if strings.Contains(service.StorageError(), blockingPath) || strings.Contains(service.StorageError(), "sk-super-secret") {
		t.Fatalf("StorageError leaked sensitive details: %q", service.StorageError())
	}
}

func TestClinePassConfigureRetriesCorruptStoreWithoutDroppingAccounts(t *testing.T) {
	firstDir := t.TempDir()
	service, _ := newConfiguredClinePassService(t, firstDir)
	if _, errSave := service.SaveAPIKeyAccount("", "existing", "", "sk-existing"); errSave != nil {
		t.Fatalf("SaveAPIKeyAccount() error = %v", errSave)
	}

	secondDir := t.TempDir()
	storePath := clinePassStorePath(secondDir)
	if errWrite := os.WriteFile(storePath, []byte(`{"version":`), 0o600); errWrite != nil {
		t.Fatalf("write corrupt store: %v", errWrite)
	}
	service.Configure(Config{DataDir: secondDir})
	if got := service.ListAccounts(); len(got) != 1 || got[0].Name != "existing" {
		t.Fatalf("corrupt store replaced live accounts: %+v", got)
	}
	if got := service.StorageError(); got != "Cline Pass state could not be loaded" {
		t.Fatalf("StorageError = %q", got)
	}

	if errSave := savePrivateJSON(storePath, clinePassPersisted{
		Version:  clinePassStoreVersion,
		Accounts: []ClinePassAccount{{ID: "restored", Name: "restored", BaseURL: clinePassDefaultBaseURL, AccessToken: "workos:restored"}},
	}); errSave != nil {
		t.Fatalf("repair store: %v", errSave)
	}
	service.Configure(Config{DataDir: secondDir})
	if got := service.ListAccounts(); len(got) != 1 || got[0].Name != "restored" {
		t.Fatalf("recovered accounts = %+v", got)
	}
	if got := service.StorageError(); got != "" {
		t.Fatalf("StorageError after recovery = %q", got)
	}
}

func TestClinePassModelProbeClassifiesGatewayFailures(t *testing.T) {
	service, gateway := newConfiguredClinePassService(t, t.TempDir())
	if _, errSave := service.SaveAPIKeyAccount("", "classified", "http://127.0.0.1:9/v1", "sk-classified"); errSave != nil {
		t.Fatalf("SaveAPIKeyAccount() error = %v", errSave)
	}
	accountID := service.ListAccounts()[0].ID
	service.mu.Lock()
	service.accounts[0].BaseURL = "https://api.cline.bot/api/v1"
	service.mu.Unlock()

	gateway.mu.Lock()
	gateway.chatStatus = http.StatusUnauthorized
	gateway.mu.Unlock()
	result, errProbe := service.ProbeModel(context.Background(), accountID, "cline-pass/glm-5.3", 5)
	if errProbe != nil {
		t.Fatalf("ProbeModel() error = %v", errProbe)
	}
	if result.Status != "unavailable" || result.ReasonCode != "authentication_failed" {
		t.Fatalf("unauthorized probe = %#v", result)
	}

	gateway.mu.Lock()
	gateway.chatStatus = http.StatusNotFound
	gateway.mu.Unlock()
	missing, errMissing := service.ProbeModel(context.Background(), accountID, "cline-pass/glm-5.3", 5)
	if errMissing != nil {
		t.Fatalf("ProbeModel() error = %v", errMissing)
	}
	if missing.ReasonCode != "model_not_supported" {
		t.Fatalf("model-not-supported probe = %#v", missing)
	}

	// The catalog refresh surfaces a rejected credential on the account view.
	gateway.mu.Lock()
	gateway.modelsStatus = http.StatusUnauthorized
	gateway.mu.Unlock()
	view, errRefresh := service.RefreshModels(context.Background(), accountID, 5)
	if errRefresh == nil {
		t.Fatal("RefreshModels() accepted a rejected credential")
	}
	if view.ID != accountID || view.ModelsError == "" || len(view.Models) != len(clinePassCatalog) {
		t.Fatalf("catalog failure view = %#v", view)
	}
	if strings.Contains(view.ModelsError, "sk-classified") {
		t.Fatalf("catalog error leaked the credential: %q", view.ModelsError)
	}
}

func TestClinePassProbeRejectsBadCredential(t *testing.T) {
	service, gateway := newConfiguredClinePassService(t, t.TempDir())
	gateway.mu.Lock()
	gateway.modelsStatus = http.StatusUnauthorized
	gateway.mu.Unlock()
	result := service.Probe(context.Background(), clinePassDefaultBaseURL, "sk-wrong", 5*time.Second)
	if result.Reachable || result.StatusCode != http.StatusUnauthorized {
		t.Fatalf("probe = %#v", result)
	}
	if result.Detail != "the gateway rejected the credential" {
		t.Fatalf("probe detail = %q", result.Detail)
	}
	if reachable := service.Probe(context.Background(), "not-a-url", "sk-wrong", 5*time.Second); reachable.Reachable {
		t.Fatalf("probe accepted an invalid base URL: %#v", reachable)
	}
}

func TestClinePassChannelHeadersCarryClineIdentity(t *testing.T) {
	headers := clinePassChannelHeaders("3.0.99")
	for name, want := range map[string]string{
		"x-client-type":    "cli",
		"x-client-version": "3.0.99",
		"x-core-version":   "3.0.99",
		"User-Agent":       "Cline/3.0.99",
	} {
		if headers[name] != want {
			t.Fatalf("header %s = %q, want %q", name, headers[name], want)
		}
	}
	// An empty version falls back to the bundled one instead of emitting a broken header.
	if got := clinePassChannelHeaders("")["x-client-version"]; got != clinePassClineVersionFallback {
		t.Fatalf("fallback version = %q", got)
	}
}

func TestClinePassClientVersionUsesRegistryAndFallsBack(t *testing.T) {
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	if got := service.clinePassClientVersion(context.Background()); got != "3.0.99" {
		t.Fatalf("client version = %q, want the registry value", got)
	}
	if got := service.clinePassClientVersionCached(); got != "3.0.99" {
		t.Fatalf("cached client version = %q", got)
	}

	// A gateway without a registry answer keeps the bundled fallback.
	broken := NewClinePassService()
	broken.Configure(Config{DataDir: t.TempDir()})
	broken.SetHTTPDoer(httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return jsonHTTPResponse(http.StatusInternalServerError, `{}`), nil
	}))
	if got := broken.clinePassClientVersion(context.Background()); got != clinePassClineVersionFallback {
		t.Fatalf("fallback client version = %q", got)
	}
}

// A reasoning model can spend a tiny output budget entirely on reasoning and then answer with no
// content at all, which the upstream reports as an error such as "empty response content". The
// probe must therefore ask for a budget that still leaves room for an actual answer.
func TestClinePassModelProbeAsksForAReasoningTolerantOutputBudget(t *testing.T) {
	service, gateway := newConfiguredClinePassService(t, t.TempDir())
	if _, errSave := service.SaveAPIKeyAccount("", "budget", "http://127.0.0.1:9/v1", "sk-budget"); errSave != nil {
		t.Fatalf("SaveAPIKeyAccount() error = %v", errSave)
	}
	accountID := service.ListAccounts()[0].ID
	service.mu.Lock()
	service.accounts[0].BaseURL = "https://api.cline.bot/api/v1"
	service.mu.Unlock()

	result, errProbe := service.ProbeModel(context.Background(), accountID, "cline-pass/deepseek-v4.1-flash", 5)
	if errProbe != nil {
		t.Fatalf("ProbeModel() error = %v", errProbe)
	}
	if result.Status != "available" || result.ReasonCode != "model_response_ok" {
		t.Fatalf("probe result = %#v", result)
	}
	gateway.mu.Lock()
	payload := gateway.chatPayload
	gateway.mu.Unlock()
	if payload == nil {
		t.Fatal("the probe did not send a chat completion")
	}
	budget, ok := payload["max_tokens"].(float64)
	if !ok || int(budget) != openCodeModelProbeMaxOutputTokens {
		t.Fatalf("probe max_tokens = %#v, want %d", payload["max_tokens"], openCodeModelProbeMaxOutputTokens)
	}
	if openCodeModelProbeMaxOutputTokens < 128 {
		t.Fatalf("a %d-token probe budget leaves no room for a reasoning model to answer", openCodeModelProbeMaxOutputTokens)
	}
	if payload["stream"] != false {
		t.Fatalf("probe stream = %#v, want false", payload["stream"])
	}
}

// The strip_model_prefix setting persists in the Cline Pass store as an additive
// field, defaults to on when an existing store file has no field, requires the
// management key, and survives a service reconfigure.
func TestClinePassSettingsPersistAndSurviveReconfigure(t *testing.T) {
	dataDir := t.TempDir()
	service, _ := newConfiguredClinePassService(t, dataDir)
	if !service.StripModelPrefix() {
		t.Fatal("strip_model_prefix must default to on")
	}
	if _, errSave := service.SaveAPIKeyAccount("", "settings", "", "sk-settings-secret"); errSave != nil {
		t.Fatalf("SaveAPIKeyAccount() error = %v", errSave)
	}
	store := &clinePassChannelStore{}
	app := NewApp(&fakeAuthHost{}, nil)
	app.clinePass = service
	app.managementDoer = store.doer()
	headers := http.Header{"Authorization": []string{"Bearer management-secret"}}
	settingsPath := "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/settings"

	// Both methods require the management key.
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
			Method: method, Path: settingsPath, Body: []byte(`{"strip_model_prefix":false}`),
		})
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s settings without a management key = %d", method, response.StatusCode)
		}
	}
	if response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut, Path: settingsPath, Headers: headers, Body: []byte(`{}`),
	}); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("settings without the field = %d body=%s", response.StatusCode, response.Body)
	}

	update := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut, Path: settingsPath, Headers: headers,
		Body: []byte(`{"strip_model_prefix":false}`),
	})
	if update.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d body=%s", update.StatusCode, update.Body)
	}
	var payload clinePassSettingsUpdateResponse
	if errDecode := json.Unmarshal(update.Body, &payload); errDecode != nil {
		t.Fatalf("decode update: %v", errDecode)
	}
	if payload.Settings.StripModelPrefix || payload.Rebound != 1 || payload.RebindErrors != 0 {
		t.Fatalf("update payload = %+v", payload)
	}
	if got := string(update.Body); got != `{"settings":{"strip_model_prefix":false},"rebound":1,"rebind_errors":0}` {
		t.Fatalf("update body = %s", got)
	}
	if _, writes := store.snapshot(); writes != 1 {
		t.Fatalf("settings update did not rebind the stored account: writes=%d", writes)
	}

	// The field is persisted and a second service reading the same store sees it.
	raw, errRead := os.ReadFile(filepath.Join(dataDir, clinePassStoreFileName))
	if errRead != nil || !strings.Contains(string(raw), `"strip_model_prefix":false`) {
		t.Fatalf("store does not persist the setting: err=%v raw=%s", errRead, raw)
	}
	reloaded := NewClinePassService()
	reloaded.Configure(Config{DataDir: dataDir})
	if reloaded.StripModelPrefix() {
		t.Fatal("strip_model_prefix did not survive a reconfigure")
	}
	read := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: settingsPath, Headers: headers,
	})
	if read.StatusCode != http.StatusOK {
		t.Fatalf("read status = %d body=%s", read.StatusCode, read.Body)
	}
	var view clinePassSettingsView
	if errDecode := json.Unmarshal(read.Body, &view); errDecode != nil || view.Settings.StripModelPrefix {
		t.Fatalf("read payload = %+v err=%v", view, errDecode)
	}
	if got := string(read.Body); got != `{"settings":{"strip_model_prefix":false}}` {
		t.Fatalf("read body = %s", got)
	}

	// A store file written before the setting existed reads as the default.
	legacyDir := t.TempDir()
	legacy := `{"version":1,"accounts":[{"id":"legacy","base_url":"https://api.cline.bot/api/v1","access_token":"sk-legacy","refresh_token":"sk-legacy","auth_method":"api_key"}]}`
	if errWrite := os.WriteFile(clinePassStorePath(legacyDir), []byte(legacy), 0o600); errWrite != nil {
		t.Fatalf("write legacy store: %v", errWrite)
	}
	legacyService := NewClinePassService()
	legacyService.Configure(Config{DataDir: legacyDir})
	if !legacyService.StripModelPrefix() {
		t.Fatal("a store file without the field must read as the default (on)")
	}
	if accounts := legacyService.ListAccounts(); len(accounts) != 1 {
		t.Fatalf("legacy accounts = %+v", accounts)
	}
}

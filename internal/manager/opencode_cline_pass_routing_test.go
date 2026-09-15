package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

// clinePassChannelStore is a mutable in-memory CPA channel list for the routing
// tests. GET returns the stored openai-compatibility entries; PUT replaces them
// and counts the write. Read or write failures can be forced so the degraded
// paths stay exercised.
type clinePassChannelStore struct {
	mu         sync.Mutex
	entries    []map[string]any
	writes     int
	failReads  bool
	failWrites bool
}

func (s *clinePassChannelStore) doer() HTTPDoer {
	return httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch request.Method {
		case http.MethodGet:
			if s.failReads {
				return jsonHTTPResponse(http.StatusInternalServerError, `{"error":"boom"}`), nil
			}
			payload, errEncode := json.Marshal(map[string]any{"openai-compatibility": s.entries})
			if errEncode != nil {
				return jsonHTTPResponse(http.StatusInternalServerError, `{"error":"encode"}`), nil
			}
			return jsonHTTPResponse(http.StatusOK, string(payload)), nil
		case http.MethodPut:
			if s.failWrites {
				return jsonHTTPResponse(http.StatusInternalServerError, `{"error":"boom"}`), nil
			}
			var items []map[string]any
			if errDecode := json.NewDecoder(request.Body).Decode(&items); errDecode != nil {
				return jsonHTTPResponse(http.StatusBadRequest, `{"error":"bad"}`), nil
			}
			s.entries = items
			s.writes++
			return jsonHTTPResponse(http.StatusOK, `{}`), nil
		default:
			return jsonHTTPResponse(http.StatusNotFound, `{}`), nil
		}
	})
}

func (s *clinePassChannelStore) snapshot() ([]map[string]any, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.entries...), s.writes
}

func (s *clinePassChannelStore) setEntries(entries []map[string]any) {
	s.mu.Lock()
	s.entries = entries
	s.mu.Unlock()
}

func getClinePassAccounts(t *testing.T, app *App, headers http.Header) clinePassAccountsResponse {
	t.Helper()
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/accounts", Headers: headers,
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("accounts status = %d body=%s", response.StatusCode, response.Body)
	}
	var payload clinePassAccountsResponse
	if errDecode := json.Unmarshal(response.Body, &payload); errDecode != nil {
		t.Fatalf("decode accounts: %v", errDecode)
	}
	return payload
}

// An account only becomes routable once a CPA channel publishes its base URL.
// The view must say so: unbound reports zero channel models and a full gap.
func TestClinePassAccountsReportRoutingState(t *testing.T) {
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	accountID, errSave := service.SaveAPIKeyAccount("", "routing", "", "sk-routing-secret")
	if errSave != nil {
		t.Fatalf("SaveAPIKeyAccount() error = %v", errSave)
	}
	store := &clinePassChannelStore{}
	app := NewApp(&fakeAuthHost{}, nil)
	app.clinePass = service
	app.managementDoer = store.doer()
	headers := http.Header{"Authorization": []string{"Bearer management-secret"}}

	unbound := getClinePassAccounts(t, app, headers)
	if len(unbound.Accounts) != 1 {
		t.Fatalf("accounts = %+v", unbound.Accounts)
	}
	account := unbound.Accounts[0]
	if account.ChannelBound || account.ChannelModels != 0 {
		t.Fatalf("unbound routing state = %+v", account)
	}
	if account.ChannelModelGaps != len(account.Models) || account.ChannelModelGaps == 0 {
		t.Fatalf("unbound gap = %d, want %d", account.ChannelModelGaps, len(account.Models))
	}

	// The explicit bind route does not change: it still publishes the channel.
	bindResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/bind", Headers: headers,
		Body: []byte(`{"account_id":"` + accountID + `"}`),
	})
	if bindResponse.StatusCode != http.StatusOK {
		t.Fatalf("bind status = %d body=%s", bindResponse.StatusCode, bindResponse.Body)
	}
	entries, writes := store.snapshot()
	if writes != 1 || len(entries) != 1 {
		t.Fatalf("channel writes = %d entries = %#v", writes, entries)
	}

	bound := getClinePassAccounts(t, app, headers)
	account = bound.Accounts[0]
	if !account.ChannelBound || account.ChannelModels != len(clinePassCatalog) || account.ChannelModelGaps != 0 {
		t.Fatalf("bound routing state = %+v", account)
	}

	// A partially bound channel reports exactly the models it does not publish.
	partialModels := make([]any, 0, 3)
	for _, model := range clinePassCatalog[:3] {
		partialModels = append(partialModels, map[string]any{"name": model.ID, "alias": model.ID})
	}
	store.setEntries([]map[string]any{{"base-url": clinePassDefaultBaseURL, "models": partialModels}})

	partial := getClinePassAccounts(t, app, headers)
	account = partial.Accounts[0]
	wantGaps := len(account.Models) - len(partialModels)
	if !account.ChannelBound || account.ChannelModels != len(partialModels) || account.ChannelModelGaps != wantGaps {
		t.Fatalf("partial routing state = %+v, want gaps=%d", account, wantGaps)
	}
}

// A channel-list read failure must degrade every account to "unbound" without
// failing the route, and an unauthenticated request must do the same.
func TestClinePassAccountsRoutingStateDegradesWhenChannelReadFails(t *testing.T) {
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	if _, errSave := service.SaveAPIKeyAccount("", "degraded", "", "sk-degraded-secret"); errSave != nil {
		t.Fatalf("SaveAPIKeyAccount() error = %v", errSave)
	}
	store := &clinePassChannelStore{failReads: true}
	app := NewApp(&fakeAuthHost{}, nil)
	app.clinePass = service
	app.managementDoer = store.doer()

	payload := getClinePassAccounts(t, app, http.Header{"Authorization": []string{"Bearer management-secret"}})
	if len(payload.Accounts) != 1 {
		t.Fatalf("accounts = %+v", payload.Accounts)
	}
	account := payload.Accounts[0]
	if account.ChannelBound || account.ChannelModels != 0 || account.ChannelModelGaps != len(account.Models) {
		t.Fatalf("degraded routing state = %+v", account)
	}
	if strings.Contains(string(mustEncodeAccounts(t, payload)), "management-secret") {
		t.Fatal("the accounts response leaked the management key")
	}

	// Without a management key the list still answers, reporting unbound.
	anonymous := getClinePassAccounts(t, app, nil)
	if len(anonymous.Accounts) != 1 {
		t.Fatalf("anonymous accounts = %+v", anonymous.Accounts)
	}
	if anonymous.Accounts[0].ChannelBound || anonymous.Accounts[0].ChannelModelGaps != len(anonymous.Accounts[0].Models) {
		t.Fatalf("anonymous routing state = %+v", anonymous.Accounts[0])
	}
}

// Saving an account and completing a device sign-in must publish the channel so
// the common path produces a routable account straight away.
func TestClinePassSaveAndLoginBindAccount(t *testing.T) {
	service, gateway := newConfiguredClinePassService(t, t.TempDir())
	store := &clinePassChannelStore{}
	app := NewApp(&fakeAuthHost{}, nil)
	app.clinePass = service
	app.managementDoer = store.doer()
	headers := http.Header{"Authorization": []string{"Bearer management-secret"}}

	saveResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/accounts", Headers: headers,
		Body: []byte(`{"name":"auto","api_key":"sk-auto-secret"}`),
	})
	if saveResponse.StatusCode != http.StatusOK {
		t.Fatalf("save status = %d body=%s", saveResponse.StatusCode, saveResponse.Body)
	}
	if strings.Contains(string(saveResponse.Body), "sk-auto-secret") {
		t.Fatal("the save response leaked the credential")
	}
	var savePayload struct {
		Account      ClinePassAccountView   `json:"account"`
		Binding      *OpenCodeBindingResult `json:"binding"`
		BindingError string                 `json:"binding_error"`
	}
	if errDecode := json.Unmarshal(saveResponse.Body, &savePayload); errDecode != nil {
		t.Fatalf("decode save: %v", errDecode)
	}
	if savePayload.Binding == nil || savePayload.BindingError != "" {
		t.Fatalf("save binding = %+v error=%q", savePayload.Binding, savePayload.BindingError)
	}
	if !savePayload.Account.ChannelBound || savePayload.Account.ChannelModelGaps != 0 {
		t.Fatalf("saved account routing state = %+v", savePayload.Account)
	}
	if _, writes := store.snapshot(); writes != 1 {
		t.Fatalf("channel writes after save = %d, want 1", writes)
	}

	started, errStart := service.StartDeviceLogin(context.Background(), "device")
	if errStart != nil {
		t.Fatalf("StartDeviceLogin() error = %v", errStart)
	}
	pollResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/login/poll", Headers: headers,
		Body: []byte(`{"session_id":"` + started.SessionID + `"}`),
	})
	if pollResponse.StatusCode != http.StatusOK {
		t.Fatalf("poll status = %d body=%s", pollResponse.StatusCode, pollResponse.Body)
	}
	var pollView ClinePassLoginView
	if errDecode := json.Unmarshal(pollResponse.Body, &pollView); errDecode != nil {
		t.Fatalf("decode poll: %v", errDecode)
	}
	if pollView.Status != clinePassLoginCompleted || pollView.Binding == nil || pollView.BindingError != "" {
		t.Fatalf("poll binding = %+v", pollView)
	}
	if _, writes := store.snapshot(); writes != 2 {
		t.Fatalf("channel writes after login = %d, want 2", writes)
	}
	if _, _, register, _, _, _ := gateway.counters(); register != 1 {
		t.Fatalf("register calls = %d", register)
	}
}

// A channel write failure must never fail a save or a sign-in: the account stays
// stored and the failure is reported through the response field instead.
func TestClinePassSaveAndLoginReportBindFailureWithoutFailing(t *testing.T) {
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	store := &clinePassChannelStore{failWrites: true}
	app := NewApp(&fakeAuthHost{}, nil)
	app.clinePass = service
	app.managementDoer = store.doer()
	headers := http.Header{"Authorization": []string{"Bearer management-secret"}}

	saveResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/accounts", Headers: headers,
		Body: []byte(`{"name":"broken","api_key":"sk-broken-secret"}`),
	})
	if saveResponse.StatusCode != http.StatusOK {
		t.Fatalf("save status = %d body=%s", saveResponse.StatusCode, saveResponse.Body)
	}
	if strings.Contains(string(saveResponse.Body), "sk-broken-secret") {
		t.Fatal("the save response leaked the credential")
	}
	var savePayload struct {
		Account      ClinePassAccountView   `json:"account"`
		Binding      *OpenCodeBindingResult `json:"binding"`
		BindingError string                 `json:"binding_error"`
	}
	if errDecode := json.Unmarshal(saveResponse.Body, &savePayload); errDecode != nil {
		t.Fatalf("decode save: %v", errDecode)
	}
	if savePayload.Account.ID == "" {
		t.Fatalf("the account was not saved: %+v", savePayload)
	}
	if savePayload.Binding != nil || savePayload.BindingError == "" {
		t.Fatalf("save binding = %+v error=%q, want a reported failure", savePayload.Binding, savePayload.BindingError)
	}
	if accounts := service.ListAccounts(); len(accounts) != 1 {
		t.Fatalf("accounts after a failed bind = %+v", accounts)
	}

	started, errStart := service.StartDeviceLogin(context.Background(), "device")
	if errStart != nil {
		t.Fatalf("StartDeviceLogin() error = %v", errStart)
	}
	pollResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/login/poll", Headers: headers,
		Body: []byte(`{"session_id":"` + started.SessionID + `"}`),
	})
	if pollResponse.StatusCode != http.StatusOK {
		t.Fatalf("poll status = %d body=%s", pollResponse.StatusCode, pollResponse.Body)
	}
	var pollView ClinePassLoginView
	if errDecode := json.Unmarshal(pollResponse.Body, &pollView); errDecode != nil {
		t.Fatalf("decode poll: %v", errDecode)
	}
	if pollView.Status != clinePassLoginCompleted || pollView.Account == nil {
		t.Fatalf("poll view = %+v", pollView)
	}
	if pollView.Binding != nil || pollView.BindingError == "" {
		t.Fatalf("poll binding = %+v error=%q, want a reported failure", pollView.Binding, pollView.BindingError)
	}
	if accounts := service.ListAccounts(); len(accounts) != 2 {
		t.Fatalf("accounts after a failed login bind = %+v", accounts)
	}
}

func mustEncodeAccounts(t *testing.T, payload clinePassAccountsResponse) []byte {
	t.Helper()
	encoded, errEncode := json.Marshal(payload)
	if errEncode != nil {
		t.Fatalf("encode accounts: %v", errEncode)
	}
	return encoded
}

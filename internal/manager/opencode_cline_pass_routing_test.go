package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

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
	// assignAuthIndex mimics CPA's own write: it stamps an auth index onto the
	// entries of write number n, so the plugin's re-read sees the index CPA would
	// have assigned.
	assignAuthIndex func(entries []map[string]any, write int)
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
			s.writes++
			// CPA assigns the auth-index of a channel row when the row is written,
			// so a fake that stores the payload verbatim would never exercise the
			// re-read that records it.
			if s.assignAuthIndex != nil {
				s.assignAuthIndex(items, s.writes)
			}
			s.entries = items
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

// clinePassChannelAliasSets indexes the model rows of one channel entry by
// upstream name. One name can carry several client-facing aliases, so the value
// is the alias set and a duplicated (name, alias) pair is a failure.
func clinePassChannelAliasSets(t *testing.T, entry map[string]any) map[string]map[string]bool {
	t.Helper()
	rows, _ := entry["models"].([]any)
	aliases := make(map[string]map[string]bool, len(rows))
	for _, item := range rows {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := row["name"].(string)
		alias, _ := row["alias"].(string)
		if aliases[name] == nil {
			aliases[name] = map[string]bool{}
		}
		if aliases[name][alias] {
			t.Fatalf("duplicate channel row for (%q, %q)", name, alias)
		}
		aliases[name][alias] = true
	}
	return aliases
}

// clinePassChannelRows flattens one channel entry's model rows into sorted
// (name, alias) keys so a test can compare the whole row set.
func clinePassChannelRows(t *testing.T, entry map[string]any) []string {
	t.Helper()
	rows, _ := entry["models"].([]any)
	pairs := make([]string, 0, len(rows))
	for _, item := range rows {
		row, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("channel row is not an object: %#v", item)
		}
		name, _ := row["name"].(string)
		alias, _ := row["alias"].(string)
		pairs = append(pairs, name+"\x00"+alias)
	}
	sort.Strings(pairs)
	return pairs
}

func getClinePassModelPage(t *testing.T, app *App, headers http.Header) clinePassModelsResponse {
	t.Helper()
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/models", Headers: headers,
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("model page status = %d body=%s", response.StatusCode, response.Body)
	}
	var payload clinePassModelsResponse
	if errDecode := json.Unmarshal(response.Body, &payload); errDecode != nil {
		t.Fatalf("decode model page: %v", errDecode)
	}
	return payload
}

func clinePassModelRow(t *testing.T, payload clinePassModelsResponse, id string) clinePassModelView {
	t.Helper()
	for _, row := range payload.Models {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("model %q is missing from the page", id)
	return clinePassModelView{}
}

// The strip_model_prefix setting decides the client-facing id on the bound channel: a prefixed
// model is published as its stripped id while the switch is on and as its full, prefixed id when
// it is off. Either way the row keeps the full id as its upstream name, so the prefixed form
// stays routable without being advertised.
func TestClinePassBindingAliasesFollowStripSetting(t *testing.T) {
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	store := &clinePassChannelStore{}
	app := NewApp(&fakeAuthHost{}, nil)
	app.clinePass = service
	app.managementDoer = store.doer()
	headers := http.Header{"Authorization": []string{"Bearer management-secret"}}

	// Saving an account binds automatically with the default setting (on).
	saveResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/accounts", Headers: headers,
		Body: []byte(`{"name":"aliases","api_key":"sk-alias-secret"}`),
	})
	if saveResponse.StatusCode != http.StatusOK {
		t.Fatalf("save status = %d body=%s", saveResponse.StatusCode, saveResponse.Body)
	}
	entries, writes := store.snapshot()
	if writes != 1 || len(entries) != 1 {
		t.Fatalf("channel writes = %d entries = %#v", writes, entries)
	}
	aliases := clinePassChannelAliasSets(t, entries[0])
	if len(aliases) != len(clinePassCatalog) {
		t.Fatalf("published models = %d, want %d", len(aliases), len(clinePassCatalog))
	}
	if len(aliases["cline-pass/glm-5.3"]) != 1 || !aliases["cline-pass/glm-5.3"]["glm-5.3"] {
		t.Fatalf("cline-pass/glm-5.3 aliases = %#v, want the stripped client id alone", aliases["cline-pass/glm-5.3"])
	}
	for _, id := range []string{"cline-free/longcat-2.0", "deepseek/deepseek-v4-flash", "z-ai/glm-5.3-flash", "poolside/laguna-s-2.1:free"} {
		if len(aliases[id]) != 1 || !aliases[id][id] {
			t.Fatalf("aliases for %q = %#v, want the identity only", id, aliases[id])
		}
	}
	rows, _ := entries[0]["models"].([]any)
	if len(rows) != clinePassExpectedChannelRows() {
		t.Fatalf("channel rows = %d, want %d", len(rows), clinePassExpectedChannelRows())
	}

	settingsPath := "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/settings"
	// Turning the switch off drops the stripped rows and keeps every full id.
	update := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut, Path: settingsPath, Headers: headers, Body: []byte(`{"strip_model_prefix":false}`),
	})
	if update.StatusCode != http.StatusOK {
		t.Fatalf("settings off status = %d body=%s", update.StatusCode, update.Body)
	}
	entries, writes = store.snapshot()
	if writes != 2 || len(entries) != 1 {
		t.Fatalf("channel writes = %d entries = %#v", writes, entries)
	}
	aliases = clinePassChannelAliasSets(t, entries[0])
	if len(aliases) != len(clinePassCatalog) {
		t.Fatalf("published models after disabling = %d, want %d", len(aliases), len(clinePassCatalog))
	}
	for id, set := range aliases {
		if len(set) != 1 || !set[id] {
			t.Fatalf("identity alias expected for %q, got %#v", id, set)
		}
	}
	rows, _ = entries[0]["models"].([]any)
	if len(rows) != clinePassExpectedChannelRows() {
		t.Fatalf("channel rows after disabling = %d, want %d", len(rows), clinePassExpectedChannelRows())
	}

	// Turning it on again restores the stripped alias for prefixed ids only.
	update = app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut, Path: settingsPath, Headers: headers, Body: []byte(`{"strip_model_prefix":true}`),
	})
	if update.StatusCode != http.StatusOK {
		t.Fatalf("settings on status = %d body=%s", update.StatusCode, update.Body)
	}
	entries, writes = store.snapshot()
	if writes != 3 || len(entries) != 1 {
		t.Fatalf("channel writes = %d entries = %#v", writes, entries)
	}
	aliases = clinePassChannelAliasSets(t, entries[0])
	if len(aliases) != len(clinePassCatalog) {
		t.Fatalf("published models after re-enabling = %d, want %d", len(aliases), len(clinePassCatalog))
	}
	if len(aliases["cline-pass/kimi-k3"]) != 1 || !aliases["cline-pass/kimi-k3"]["kimi-k3"] {
		t.Fatalf("cline-pass/kimi-k3 aliases = %#v, want the stripped client id alone", aliases["cline-pass/kimi-k3"])
	}
	if len(aliases["cline-free/muse-spark-1.3-contributor"]) != 1 || !aliases["cline-free/muse-spark-1.3-contributor"]["cline-free/muse-spark-1.3-contributor"] {
		t.Fatalf("free model aliases = %#v", aliases["cline-free/muse-spark-1.3-contributor"])
	}
}

// The published list is what a client reads from /v1/models, and CPA advertises
// each channel row's alias there. While the prefix switch is on no row may keep a
// cline-pass/-prefixed alias: the prefixed id stays the row's upstream name, which
// keeps it routable without being advertised.
func TestClinePassChannelAdvertisesNoPrefixedAliasWhenStripping(t *testing.T) {
	models := []string{"cline-pass/glm-5.3", "cline-pass/kimi-k3", "cline-free/longcat-2.0"}
	app, store, accountID, headers := newClinePassPublicationApp(t, models)
	bindClinePassPublicationAccount(t, app, headers, accountID)

	entries, _ := store.snapshot()
	rows, _ := entries[0]["models"].([]any)
	if len(rows) != len(models) {
		t.Fatalf("channel rows = %d, want %d (one per published model)", len(rows), len(models))
	}
	advertised := make(map[string]bool, len(rows))
	for _, row := range rows {
		record, _ := row.(map[string]any)
		name, _ := record["name"].(string)
		alias, _ := record["alias"].(string)
		if strings.HasPrefix(alias, clinePassModelPrefix) {
			t.Fatalf("alias %q keeps the prefix while stripping is on", alias)
		}
		if strings.HasPrefix(name, clinePassModelPrefix) && alias != strings.TrimPrefix(name, clinePassModelPrefix) {
			t.Fatalf("row (name %q, alias %q) does not publish the stripped id", name, alias)
		}
		advertised[alias] = true
	}
	for _, want := range []string{"glm-5.3", "kimi-k3", "cline-free/longcat-2.0"} {
		if !advertised[want] {
			t.Fatalf("advertised ids = %#v, want %q among them", advertised, want)
		}
	}
}

// The model page reports the client-facing id the setting implies and whether the
// bound channel already publishes that alias. A stale row that still carries the
// full id does not publish the stripped client id.
func TestClinePassModelPageReportsClientIDsAndPublication(t *testing.T) {
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	accountID, errSave := service.SaveAPIKeyAccount("", "page", "", "sk-page-secret")
	if errSave != nil {
		t.Fatalf("SaveAPIKeyAccount() error = %v", errSave)
	}
	store := &clinePassChannelStore{}
	app := NewApp(&fakeAuthHost{}, nil)
	app.clinePass = service
	app.managementDoer = store.doer()
	headers := http.Header{"Authorization": []string{"Bearer management-secret"}}

	bindResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/bind", Headers: headers,
		Body: []byte(`{"account_id":"` + accountID + `"}`),
	})
	if bindResponse.StatusCode != http.StatusOK {
		t.Fatalf("bind status = %d body=%s", bindResponse.StatusCode, bindResponse.Body)
	}

	page := getClinePassModelPage(t, app, headers)
	if !page.StripModelPrefix || page.Accounts != 1 || !page.ChannelBound || page.ChannelModels != len(clinePassCatalog) {
		t.Fatalf("page summary = %+v", page)
	}
	if page.DefaultBaseURL != clinePassDefaultBaseURL || len(page.Models) != len(clinePassCatalog) {
		t.Fatalf("page summary = %+v", page)
	}
	row := clinePassModelRow(t, page, "cline-pass/deepseek-v4.1-flash")
	if row.Name != "DeepSeek V4.1 Flash" || row.Free || row.UpstreamID != "cline-pass/deepseek-v4.1-flash" || row.ClientID != "deepseek-v4.1-flash" || !row.Published {
		t.Fatalf("prefixed row = %+v", row)
	}
	free := clinePassModelRow(t, page, "cline-free/longcat-2.0")
	if free.Name != "LongCat 2.0" || !free.Free || free.UpstreamID != "cline-free/longcat-2.0" || free.ClientID != "cline-free/longcat-2.0" || !free.Published {
		t.Fatalf("free row = %+v", free)
	}

	// A channel that still carries only the full id does not publish the
	// stripped client id.
	stale := make([]any, 0, len(clinePassCatalog))
	for _, model := range clinePassCatalog {
		stale = append(stale, map[string]any{"name": model.ID, "alias": model.ID})
	}
	store.setEntries([]map[string]any{{"base-url": clinePassDefaultBaseURL, "models": stale}})
	staleRow := clinePassModelRow(t, getClinePassModelPage(t, app, headers), "cline-pass/deepseek-v4.1-flash")
	if staleRow.Published || staleRow.ClientID != "deepseek-v4.1-flash" {
		t.Fatalf("stale row = %+v", staleRow)
	}

	// Disabling the switch republishes the full ids, so the page reports them as
	// published and no longer strips the client id.
	update := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/settings", Headers: headers,
		Body: []byte(`{"strip_model_prefix":false}`),
	})
	if update.StatusCode != http.StatusOK {
		t.Fatalf("settings off status = %d body=%s", update.StatusCode, update.Body)
	}
	off := getClinePassModelPage(t, app, headers)
	if off.StripModelPrefix {
		t.Fatalf("page strip flag = %+v", off)
	}
	row = clinePassModelRow(t, off, "cline-pass/deepseek-v4.1-flash")
	if row.ClientID != row.ID || !row.Published {
		t.Fatalf("row with the switch off = %+v", row)
	}
}

// An unreadable channel list degrades the model page to "nothing published"
// instead of failing it, and the route still requires the management key.
func TestClinePassModelPageDegradesWhenChannelReadFails(t *testing.T) {
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	if _, errSave := service.SaveAPIKeyAccount("", "page", "", "sk-page-degraded"); errSave != nil {
		t.Fatalf("SaveAPIKeyAccount() error = %v", errSave)
	}
	store := &clinePassChannelStore{failReads: true}
	app := NewApp(&fakeAuthHost{}, nil)
	app.clinePass = service
	app.managementDoer = store.doer()

	page := getClinePassModelPage(t, app, http.Header{"Authorization": []string{"Bearer management-secret"}})
	if page.ChannelBound || page.ChannelModels != 0 || page.Accounts != 1 {
		t.Fatalf("degraded page = %+v", page)
	}
	if len(page.Models) != len(clinePassCatalog) {
		t.Fatalf("degraded rows = %d", len(page.Models))
	}
	for _, row := range page.Models {
		if row.Published {
			t.Fatalf("degraded row reports published: %+v", row)
		}
	}

	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/models",
	})
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("model page without a management key = %d", response.StatusCode)
	}
}

// A model probe always sends the full upstream id, even when the publishing
// switch strips it on the channel: the gateway only accepts the full id.
func TestClinePassModelProbeKeepsUpstreamIDWhenStripping(t *testing.T) {
	service, gateway := newConfiguredClinePassService(t, t.TempDir())
	accountID, errSave := service.SaveAPIKeyAccount("", "probe", "", "sk-probe-secret")
	if errSave != nil {
		t.Fatalf("SaveAPIKeyAccount() error = %v", errSave)
	}
	if !service.StripModelPrefix() {
		t.Fatal("the probe test expects the default setting (on)")
	}
	store := &clinePassChannelStore{}
	app := NewApp(&fakeAuthHost{}, nil)
	app.clinePass = service
	app.managementDoer = store.doer()
	headers := http.Header{"Authorization": []string{"Bearer management-secret"}}

	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/model-test", Headers: headers,
		Body: []byte(`{"account_id":"` + accountID + `","model":"cline-pass/glm-5.3"}`),
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("model-test status = %d body=%s", response.StatusCode, response.Body)
	}
	gateway.mu.Lock()
	sent := gateway.chatPayload["model"]
	gateway.mu.Unlock()
	if sent != "cline-pass/glm-5.3" {
		t.Fatalf("probe sent model %#v, want the full upstream id", sent)
	}
}

// clinePassExpectedChannelRows returns the number of rows a channel publishes for
// the fixture catalog: one per model, because the prefix switch changes which
// single client-facing id a row carries, not how many rows exist.
func clinePassExpectedChannelRows() int {
	return len(clinePassCatalog)
}

// newClinePassPublicationApp binds one app to an in-memory channel store and a
// stored account whose catalog is exactly the given allow-listed ids.
func newClinePassPublicationApp(t *testing.T, models []string) (*App, *clinePassChannelStore, string, http.Header) {
	t.Helper()
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	accountID, errSave := service.SaveAPIKeyAccount("", "publication", "", "sk-publication-secret")
	if errSave != nil {
		t.Fatalf("SaveAPIKeyAccount() error = %v", errSave)
	}
	service.mu.Lock()
	for index := range service.accounts {
		if service.accounts[index].ID == accountID {
			service.accounts[index].Models = append([]string(nil), models...)
		}
	}
	service.mu.Unlock()
	store := &clinePassChannelStore{}
	app := NewApp(&fakeAuthHost{}, nil)
	app.clinePass = service
	app.managementDoer = store.doer()
	return app, store, accountID, http.Header{"Authorization": []string{"Bearer management-secret"}}
}

func bindClinePassPublicationAccount(t *testing.T, app *App, headers http.Header, accountID string) OpenCodeBindingResult {
	t.Helper()
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/bind", Headers: headers,
		Body: []byte(`{"account_id":"` + accountID + `"}`),
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("bind status = %d body=%s", response.StatusCode, response.Body)
	}
	var payload struct {
		Binding OpenCodeBindingResult `json:"binding"`
	}
	if errDecode := json.Unmarshal(response.Body, &payload); errDecode != nil {
		t.Fatalf("decode bind: %v", errDecode)
	}
	return payload.Binding
}

func setClinePassStripPrefix(t *testing.T, app *App, headers http.Header, strip bool) {
	t.Helper()
	body := `{"strip_model_prefix":false}`
	if strip {
		body = `{"strip_model_prefix":true}`
	}
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/settings", Headers: headers,
		Body: []byte(body),
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("settings status = %d body=%s", response.StatusCode, response.Body)
	}
}

// With the prefix switch on, a cline-pass/ model is published under its stripped
// client id alone: CPA advertises each row's alias in /v1/models, so the full id
// must not keep an identity row of its own. Every other model keeps exactly one
// identity row, and no (name, alias) pair is duplicated.
func TestClinePassBindingPublishesStrippedClientID(t *testing.T) {
	app, store, accountID, headers := newClinePassPublicationApp(t, []string{"cline-pass/glm-5.3", "cline-free/longcat-2.0"})

	result := bindClinePassPublicationAccount(t, app, headers, accountID)
	if result.Models != 2 {
		t.Fatalf("binding models = %d, want the 2 distinct ids", result.Models)
	}
	entries, writes := store.snapshot()
	if writes != 1 || len(entries) != 1 {
		t.Fatalf("channel writes = %d entries = %#v", writes, entries)
	}
	want := []string{
		"cline-free/longcat-2.0\x00cline-free/longcat-2.0",
		"cline-pass/glm-5.3\x00glm-5.3",
	}
	if got := clinePassChannelRows(t, entries[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("channel rows = %#v, want %#v", got, want)
	}

	// Binding twice in a row converges on the same row set.
	second := bindClinePassPublicationAccount(t, app, headers, accountID)
	if second.Models != 2 {
		t.Fatalf("second binding models = %d", second.Models)
	}
	entries, writes = store.snapshot()
	if writes != 2 {
		t.Fatalf("channel writes = %d", writes)
	}
	if got := clinePassChannelRows(t, entries[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows after a second bind = %#v, want %#v", got, want)
	}
}

// A channel written by the previous release carries the identity row only. The
// rebind must republish that model under its stripped client id, replacing the
// prefixed row CPA would otherwise keep advertising, and a further rebind must
// change nothing.
func TestClinePassBindingUpgradesIdentityOnlyChannel(t *testing.T) {
	app, store, accountID, headers := newClinePassPublicationApp(t, []string{"cline-pass/glm-5.3"})
	store.setEntries([]map[string]any{{
		"base-url": clinePassDefaultBaseURL,
		"name":     clinePassBoundChannelName,
		"models":   []any{map[string]any{"name": "cline-pass/glm-5.3", "alias": "cline-pass/glm-5.3"}},
	}})

	if result := bindClinePassPublicationAccount(t, app, headers, accountID); result.Models != 1 {
		t.Fatalf("binding models = %d, want 1", result.Models)
	}
	entries, _ := store.snapshot()
	want := []string{
		"cline-pass/glm-5.3\x00glm-5.3",
	}
	if got := clinePassChannelRows(t, entries[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("upgraded rows = %#v, want %#v", got, want)
	}

	bindClinePassPublicationAccount(t, app, headers, accountID)
	entries, _ = store.snapshot()
	if got := clinePassChannelRows(t, entries[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows after a further bind = %#v, want %#v", got, want)
	}
}

// Turning the switch off after a switch-on bind drops exactly the stripped alias
// rows and keeps the identity rows, and a further bind changes nothing.
func TestClinePassBindingDropsStrippedAliasesWhenSwitchOff(t *testing.T) {
	app, store, accountID, headers := newClinePassPublicationApp(t, []string{"cline-pass/glm-5.3", "cline-free/longcat-2.0"})
	bindClinePassPublicationAccount(t, app, headers, accountID)

	setClinePassStripPrefix(t, app, headers, false)
	want := []string{
		"cline-free/longcat-2.0\x00cline-free/longcat-2.0",
		"cline-pass/glm-5.3\x00cline-pass/glm-5.3",
	}
	entries, _ := store.snapshot()
	if got := clinePassChannelRows(t, entries[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows with the switch off = %#v, want %#v", got, want)
	}

	if result := bindClinePassPublicationAccount(t, app, headers, accountID); result.Models != 2 {
		t.Fatalf("binding models with the switch off = %d, want 2", result.Models)
	}
	entries, _ = store.snapshot()
	if got := clinePassChannelRows(t, entries[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows after a further bind = %#v, want %#v", got, want)
	}
}

// An operator-added row for an id this plugin does not publish survives every
// rebind, while a stale alias row for a published id is removed.
func TestClinePassBindingPreservesOperatorRows(t *testing.T) {
	app, store, accountID, headers := newClinePassPublicationApp(t, []string{"cline-pass/glm-5.3"})
	store.setEntries([]map[string]any{{
		"base-url": clinePassDefaultBaseURL,
		"name":     clinePassBoundChannelName,
		"models": []any{
			map[string]any{"name": "operator/private", "alias": "private-alias"},
			map[string]any{"name": "cline-pass/glm-5.3", "alias": "stale-operator-alias"},
		},
	}})

	bindClinePassPublicationAccount(t, app, headers, accountID)
	entries, _ := store.snapshot()
	aliases := clinePassChannelAliasSets(t, entries[0])
	if len(aliases["operator/private"]) != 1 || !aliases["operator/private"]["private-alias"] {
		t.Fatalf("operator row was not preserved: %#v", aliases)
	}
	if aliases["cline-pass/glm-5.3"]["stale-operator-alias"] {
		t.Fatalf("a stale alias survived the merge: %#v", aliases["cline-pass/glm-5.3"])
	}
	if len(aliases["cline-pass/glm-5.3"]) != 1 || !aliases["cline-pass/glm-5.3"]["glm-5.3"] {
		t.Fatalf("published aliases = %#v, want the stripped client id alone", aliases["cline-pass/glm-5.3"])
	}

	// The operator row also survives the switch-off rebind and a manual rebind.
	setClinePassStripPrefix(t, app, headers, false)
	entries, _ = store.snapshot()
	aliases = clinePassChannelAliasSets(t, entries[0])
	if len(aliases["operator/private"]) != 1 || !aliases["operator/private"]["private-alias"] {
		t.Fatalf("operator row was lost by the switch-off rebind: %#v", aliases)
	}
	if len(aliases["cline-pass/glm-5.3"]) != 1 || !aliases["cline-pass/glm-5.3"]["cline-pass/glm-5.3"] {
		t.Fatalf("rows with the switch off = %#v", aliases["cline-pass/glm-5.3"])
	}
	bindClinePassPublicationAccount(t, app, headers, accountID)
	entries, _ = store.snapshot()
	aliases = clinePassChannelAliasSets(t, entries[0])
	if len(aliases["operator/private"]) != 1 || !aliases["operator/private"]["private-alias"] {
		t.Fatalf("operator row was lost by a manual rebind: %#v", aliases)
	}
}

// The binding result counts distinct upstream ids (two models with both aliases
// report 2, not 3 or 4), and the model page reports "published" by the alias the
// current setting says a client should call.
func TestClinePassBindingModelCountAndModelPagePublication(t *testing.T) {
	app, _, accountID, headers := newClinePassPublicationApp(t, []string{"cline-pass/glm-5.3", "cline-free/longcat-2.0"})
	result := bindClinePassPublicationAccount(t, app, headers, accountID)
	if result.Models != 2 {
		t.Fatalf("binding models = %d, want 2", result.Models)
	}

	page := getClinePassModelPage(t, app, headers)
	if !page.ChannelBound || page.ChannelModels != 2 || len(page.Models) != 2 {
		t.Fatalf("page summary with the switch on = %+v", page)
	}
	if row := clinePassModelRow(t, page, "cline-pass/glm-5.3"); row.ClientID != "glm-5.3" || !row.Published {
		t.Fatalf("switch-on row = %+v", row)
	}

	setClinePassStripPrefix(t, app, headers, false)
	off := getClinePassModelPage(t, app, headers)
	if off.StripModelPrefix || !off.ChannelBound || off.ChannelModels != 2 {
		t.Fatalf("page summary with the switch off = %+v", off)
	}
	if row := clinePassModelRow(t, off, "cline-pass/glm-5.3"); row.ClientID != row.ID || !row.Published {
		t.Fatalf("switch-off row = %+v", row)
	}
}

// clinePassAuthIndexStamp mimics CPA's channel write: it stamps an auth index on
// the row and on each weighted key entry of the payload it receives.
func clinePassAuthIndexStamp(entries []map[string]any, write int) {
	for _, entry := range entries {
		entry["auth-index"] = fmt.Sprintf("row-index-%d", write)
		rows, _ := entry["api-key-entries"].([]any)
		for _, item := range rows {
			row, isRow := item.(map[string]any)
			if !isRow {
				continue
			}
			row["auth-index"] = fmt.Sprintf("key-index-%d", write)
		}
	}
}

// Binding records the auth indexes CPA assigned to the account's own channel row,
// including the per-key-row index, so a usage callback that names one of them is
// attributed to the account whose windows the operator reads. Re-binding replaces
// the recorded set, because CPA can change the index on every write.
func TestClinePassBindRecordsChannelAuthIndexes(t *testing.T) {
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	accountID, errSave := service.SaveAPIKeyAccount("", "auth indexes", "", "sk-auth-index-bind")
	if errSave != nil {
		t.Fatalf("SaveAPIKeyAccount() error = %v", errSave)
	}
	store := &clinePassChannelStore{assignAuthIndex: clinePassAuthIndexStamp}
	app := NewApp(&fakeAuthHost{}, nil)
	app.clinePass = service
	app.managementDoer = store.doer()
	headers := http.Header{"Authorization": []string{"Bearer management-secret"}}
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	bindClinePassPublicationAccount(t, app, headers, accountID)
	want := []string{"key-index-1", "row-index-1"}
	if got := recordedClinePassRouteIndexes(service, accountID); !reflect.DeepEqual(got, want) {
		t.Fatalf("recorded auth indexes = %#v, want %#v", got, want)
	}
	// Both the row index and the per-key-row index attribute traffic to the account.
	for _, authIndex := range want {
		service.ObserveUsage(cpaapi.UsageRecord{
			Provider: "openai-compatible-cline pass", AuthType: "apikey", AuthIndex: authIndex,
			Model: "cline-pass/glm-5.3", RequestedAt: now.Add(-time.Minute), Detail: cpaapi.UsageDetail{InputTokens: 1_000_000},
		})
	}
	view, _ := service.AccountView(accountID)
	if view.QuotaUsage.Monthly.Requests != 2 || view.QuotaUsage.Monthly.InputTokens != 2_000_000 {
		t.Fatalf("binding recorded indexes did not attribute traffic: %+v", view.QuotaUsage.Monthly)
	}
	if math.Abs(view.QuotaUsage.Monthly.USD-2.80) > 1e-6 {
		t.Fatalf("monthly USD = %v, want 2.80", view.QuotaUsage.Monthly.USD)
	}

	// A rebind refreshes the recorded set: the indexes of the previous write are
	// gone, so traffic that still names one is no longer attributed to this account.
	bindClinePassPublicationAccount(t, app, headers, accountID)
	want = []string{"key-index-2", "row-index-2"}
	if got := recordedClinePassRouteIndexes(service, accountID); !reflect.DeepEqual(got, want) {
		t.Fatalf("recorded auth indexes after a rebind = %#v, want %#v", got, want)
	}
	service.ObserveUsage(cpaapi.UsageRecord{
		Provider: "openai-compatible-cline pass", AuthType: "apikey", AuthIndex: "row-index-1",
		Model: "cline-pass/glm-5.3", RequestedAt: now.Add(-time.Minute), Detail: cpaapi.UsageDetail{InputTokens: 1_000_000},
	})
	after, _ := service.AccountView(accountID)
	if after.QuotaUsage.Monthly.InputTokens != 2_000_000 {
		t.Fatalf("a stale auth index still attributed traffic: %+v", after.QuotaUsage.Monthly)
	}
}

// Two accounts on one gateway are two channel rows, so each bind records only its
// own row's indexes: a record that names one account's index never reaches the
// other account's windows.
func TestClinePassBindRecordsAuthIndexesPerAccount(t *testing.T) {
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	firstID, errFirst := service.SaveAPIKeyAccount("", "first", "", "sk-first-secret")
	if errFirst != nil {
		t.Fatalf("SaveAPIKeyAccount(first) error = %v", errFirst)
	}
	secondID, errSecond := service.SaveAPIKeyAccount("", "second", "", "sk-second-secret")
	if errSecond != nil {
		t.Fatalf("SaveAPIKeyAccount(second) error = %v", errSecond)
	}
	if firstID == secondID {
		t.Fatal("the two accounts must be distinct")
	}
	store := &clinePassChannelStore{assignAuthIndex: clinePassAuthIndexStamp}
	app := NewApp(&fakeAuthHost{}, nil)
	app.clinePass = service
	app.managementDoer = store.doer()
	headers := http.Header{"Authorization": []string{"Bearer management-secret"}}
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	bindClinePassPublicationAccount(t, app, headers, firstID)
	bindClinePassPublicationAccount(t, app, headers, secondID)
	if got, want := recordedClinePassRouteIndexes(service, firstID), []string{"key-index-1", "row-index-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first account indexes = %#v, want %#v", got, want)
	}
	if got, want := recordedClinePassRouteIndexes(service, secondID), []string{"key-index-2", "row-index-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second account indexes = %#v, want %#v", got, want)
	}

	service.ObserveUsage(cpaapi.UsageRecord{
		Provider: "openai-compatible-cline pass", AuthType: "apikey", AuthIndex: "row-index-2",
		Model: "cline-pass/glm-5.3", RequestedAt: now.Add(-time.Minute), Detail: cpaapi.UsageDetail{InputTokens: 1_000_000},
	})
	first, _ := service.AccountView(firstID)
	if first.QuotaUsage.Monthly.Requests != 0 {
		t.Fatalf("the first account took the second account's traffic: %+v", first.QuotaUsage.Monthly)
	}
	second, _ := service.AccountView(secondID)
	if second.QuotaUsage.Monthly.Requests != 1 || second.QuotaUsage.Monthly.InputTokens != 1_000_000 {
		t.Fatalf("the second account did not take its own traffic: %+v", second.QuotaUsage.Monthly)
	}
}

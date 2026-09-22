package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// channelCredentialKey reads one channel row's first credential.
func channelCredentialKey(t *testing.T, entry map[string]any) string {
	t.Helper()
	rows, _ := entry["api-key-entries"].([]any)
	for _, item := range rows {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if key, isText := record["api-key"].(string); isText {
			return key
		}
	}
	return ""
}

func channelName(t *testing.T, entry map[string]any) string {
	t.Helper()
	name, _ := entry["name"].(string)
	return name
}

// Every account of one gateway shares the same base URL, so before this fix they all collapsed into
// ONE channel row: the last bind overwrote its name and its key, which renamed every account at once
// and made the earlier accounts unroutable. Each account must own a row.
func TestClinePassAccountsWithOneGatewayGetTheirOwnChannelRows(t *testing.T) {
	app, store, firstID, headers := newClinePassPublicationApp(t, []string{"cline-pass/glm-5.3"})
	service := app.clinePass
	secondID, errSave := service.SaveAPIKeyAccount("", "second account", "", "sk-second-secret")
	if errSave != nil {
		t.Fatalf("SaveAPIKeyAccount() error = %v", errSave)
	}
	service.mu.Lock()
	for index := range service.accounts {
		if service.accounts[index].ID == secondID {
			service.accounts[index].Models = []string{"cline-pass/glm-5.3"}
		}
	}
	service.mu.Unlock()

	first := bindClinePassPublicationAccount(t, app, headers, firstID)
	if !first.Created {
		t.Fatalf("the first bind did not create a row: %#v", first)
	}
	entries, _ := store.snapshot()
	if len(entries) != 1 {
		t.Fatalf("rows after the first bind = %d, want 1", len(entries))
	}
	firstName := channelName(t, entries[0])
	firstKey := channelCredentialKey(t, entries[0])
	if firstKey != "sk-publication-secret" {
		t.Fatalf("first row credential = %q", firstKey)
	}

	second := bindClinePassPublicationAccount(t, app, headers, secondID)
	if !second.Created {
		t.Fatalf("the second account reused another account's row: %#v", second)
	}
	entries, _ = store.snapshot()
	if len(entries) != 2 {
		t.Fatalf("two accounts sharing a gateway must own two rows, got %d", len(entries))
	}
	// Neither row may have borrowed the other account's credential or name.
	keys := map[string]string{}
	for _, entry := range entries {
		keys[channelCredentialKey(t, entry)] = channelName(t, entry)
	}
	if keys["sk-publication-secret"] != firstName {
		t.Fatalf("the first account's row changed: %#v", keys)
	}
	if keys["sk-second-secret"] == "" {
		t.Fatalf("the second account has no row: %#v", keys)
	}
	if keys["sk-second-secret"] == firstName {
		t.Fatalf("both accounts report the same name: %#v", keys)
	}

	// A further bind of the first account updates its own row only.
	again := bindClinePassPublicationAccount(t, app, headers, firstID)
	if again.Created {
		t.Fatal("re-binding an existing account created another row")
	}
	entries, _ = store.snapshot()
	if len(entries) != 2 {
		t.Fatalf("rows after a re-bind = %d, want 2", len(entries))
	}
	for _, entry := range entries {
		if channelCredentialKey(t, entry) == "sk-publication-secret" && channelName(t, entry) != firstName {
			t.Fatalf("a re-bind renamed the account's own row: %#v", entries)
		}
	}
}

// A row an older release left without a credential is adopted instead of duplicated, so an upgrade
// does not leave an orphan row behind.
func TestClinePassBindingAdoptsACredentiallessLegacyRow(t *testing.T) {
	app, store, accountID, headers := newClinePassPublicationApp(t, []string{"cline-pass/glm-5.3"})
	store.setEntries([]map[string]any{{
		"base-url": clinePassDefaultBaseURL,
		"name":     clinePassBoundChannelName,
		"models":   []any{map[string]any{"name": "cline-pass/glm-5.3", "alias": "cline-pass/glm-5.3"}},
	}})

	result := bindClinePassPublicationAccount(t, app, headers, accountID)
	if result.Created {
		t.Fatalf("an unclaimed legacy row was not adopted: %#v", result)
	}
	entries, _ := store.snapshot()
	if len(entries) != 1 {
		t.Fatalf("an adopted legacy row must not create a duplicate, got %d rows", len(entries))
	}
	if got := channelCredentialKey(t, entries[0]); got != "sk-publication-secret" {
		t.Fatalf("adopted row credential = %q", got)
	}
}

// A row that already belongs to another credential is never adopted: the new account gets its own.
func TestClinePassBindingNeverAdoptsAnotherAccountsRow(t *testing.T) {
	app, store, accountID, headers := newClinePassPublicationApp(t, []string{"cline-pass/glm-5.3"})
	store.setEntries([]map[string]any{{
		"base-url":        clinePassDefaultBaseURL,
		"name":            "someone else",
		"api-key-entries": []any{map[string]any{"api-key": "sk-someone-else"}},
		"models":          []any{map[string]any{"name": "cline-pass/glm-5.3", "alias": "cline-pass/glm-5.3"}},
	}})

	if result := bindClinePassPublicationAccount(t, app, headers, accountID); !result.Created {
		t.Fatalf("another account's row was adopted: %#v", result)
	}
	entries, _ := store.snapshot()
	if len(entries) != 2 {
		t.Fatalf("rows = %d, want the other account's row plus a new one", len(entries))
	}
	for _, entry := range entries {
		if channelCredentialKey(t, entry) == "sk-someone-else" && channelName(t, entry) != "someone else" {
			t.Fatalf("the other account's row was renamed: %#v", entries)
		}
	}
}

// The accounts list reports each account's routing state from its OWN channel row, which is what
// makes per-account quota and publication state meaningful once several accounts share a gateway.
func TestClinePassRoutingStateIsPerAccount(t *testing.T) {
	app, store, firstID, headers := newClinePassPublicationApp(t, []string{"cline-pass/glm-5.3"})
	service := app.clinePass
	secondID, errSave := service.SaveAPIKeyAccount("", "second account", "", "sk-second-secret")
	if errSave != nil {
		t.Fatalf("SaveAPIKeyAccount() error = %v", errSave)
	}
	thirdID, errThird := service.SaveAPIKeyAccount("", "unbound account", "", "sk-third-secret")
	if errThird != nil {
		t.Fatalf("SaveAPIKeyAccount() error = %v", errThird)
	}
	// Two rows for the same gateway: the first account's row publishes one model, the second
	// account's row publishes two. Reading the wrong row would report the same count twice.
	store.setEntries([]map[string]any{
		{
			"base-url":        clinePassDefaultBaseURL,
			"name":            "Cline Pass first",
			"api-key-entries": []any{map[string]any{"api-key": "sk-publication-secret"}},
			"models":          []any{map[string]any{"name": "cline-pass/glm-5.3", "alias": "cline-pass/glm-5.3"}},
		},
		{
			"base-url":        clinePassDefaultBaseURL,
			"name":            "Cline Pass second",
			"api-key-entries": []any{map[string]any{"api-key": "sk-second-secret"}},
			"models": []any{
				map[string]any{"name": "cline-pass/glm-5.3", "alias": "cline-pass/glm-5.3"},
				map[string]any{"name": "cline-pass/kimi-k2.6", "alias": "cline-pass/kimi-k2.6"},
			},
		},
	})

	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/accounts", Headers: headers,
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("accounts status = %d body=%s", response.StatusCode, response.Body)
	}
	var payload struct {
		Accounts []ClinePassAccountView `json:"accounts"`
	}
	if errDecode := json.Unmarshal(response.Body, &payload); errDecode != nil {
		t.Fatalf("decode accounts: %v", errDecode)
	}
	views := map[string]ClinePassAccountView{}
	for _, view := range payload.Accounts {
		views[view.ID] = view
	}
	if got := views[firstID]; !got.ChannelBound || got.ChannelModels != 1 {
		t.Fatalf("the first account's routing state = %#v, want its own row with 1 model", got)
	}
	if got := views[secondID]; !got.ChannelBound || got.ChannelModels != 2 {
		t.Fatalf("the second account's routing state = %#v, want its own row with 2 models", got)
	}
	// An account that owns no row of its own is published by the same read: a row belonging
	// to a sibling says nothing about this account - CPA routes through the credential inside
	// a row - so reporting it "bound" through that row is what used to leave the second
	// subscription idle while the page said it was fine.
	if got := views[thirdID]; !got.ChannelBound {
		t.Fatalf("an account without its own row was not published: %#v", got)
	}
	entries, _ := store.snapshot()
	owners := map[string]bool{}
	for _, entry := range entries {
		owners[channelCredentialKey(t, entry)] = true
	}
	if !owners["sk-third-secret"] {
		t.Fatalf("the read did not publish the account that owned no row: %#v", owners)
	}
}

// A rotated credential must not add a second row for the same account: the row is matched by its
// per-account label when the token it holds is no longer the current one.
func TestClinePassBindingReusesItsRowAfterACredentialRotation(t *testing.T) {
	app, store, accountID, headers := newClinePassPublicationApp(t, []string{"cline-pass/glm-5.3"})
	// The row an earlier bind wrote for this same account, still holding the superseded token.
	store.setEntries([]map[string]any{{
		"base-url":        clinePassDefaultBaseURL,
		"name":            "Cline Pass publication",
		"api-key-entries": []any{map[string]any{"api-key": "sk-superseded-secret"}},
		"models":          []any{map[string]any{"name": "cline-pass/glm-5.3", "alias": "cline-pass/glm-5.3"}},
	}})

	result := bindClinePassPublicationAccount(t, app, headers, accountID)
	if result.Created {
		t.Fatalf("a rotated credential created a second row: %#v", result)
	}
	entries, _ := store.snapshot()
	if len(entries) != 1 {
		t.Fatalf("rows after a credential rotation = %d, want the account's single row", len(entries))
	}
	if got := channelCredentialKey(t, entries[0]); got != "sk-publication-secret" {
		t.Fatalf("the rotated row credential = %q", got)
	}
	if got := channelName(t, entries[0]); got != "Cline Pass publication" {
		t.Fatalf("the rotated row lost its name: %q", got)
	}
}

// The models page reports publication from each account's own row, so the alias a client calls is
// marked published even while an older sibling row for the same gateway publishes only the full id.
func TestClinePassModelPageReadsTheAccountsOwnRow(t *testing.T) {
	app, store, _, headers := newClinePassPublicationApp(t, []string{"cline-pass/glm-5.3"})
	store.setEntries([]map[string]any{
		{
			"base-url":        clinePassDefaultBaseURL,
			"name":            "Cline Pass legacy",
			"api-key-entries": []any{map[string]any{"api-key": "sk-legacy-secret"}},
			"models":          []any{map[string]any{"name": "cline-pass/glm-5.3", "alias": "cline-pass/glm-5.3"}},
		},
		{
			"base-url":        clinePassDefaultBaseURL,
			"name":            "Cline Pass publication",
			"api-key-entries": []any{map[string]any{"api-key": "sk-publication-secret"}},
			"models": []any{
				map[string]any{"name": "cline-pass/glm-5.3", "alias": "cline-pass/glm-5.3"},
				map[string]any{"name": "cline-pass/glm-5.3", "alias": "glm-5.3"},
			},
		},
	})

	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/models", Headers: headers,
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("models status = %d body=%s", response.StatusCode, response.Body)
	}
	var payload struct {
		Models []clinePassModelView `json:"models"`
	}
	if errDecode := json.Unmarshal(response.Body, &payload); errDecode != nil {
		t.Fatalf("decode models: %v", errDecode)
	}
	for _, row := range payload.Models {
		if row.ID != "cline-pass/glm-5.3" {
			continue
		}
		if !row.Published || row.ClientID != "glm-5.3" {
			t.Fatalf("the account's own published alias was not reported: %#v", row)
		}
		return
	}
	t.Fatalf("the model row is missing from the page: %#v", payload.Models)
}

// rotateClinePassStoredToken replaces one account's stored token the way a refresh does, leaving the
// channel row it published untouched. That is the state an expired or rotated Cline Pass credential
// leaves behind: CPA keeps routing through the token inside the row, so the account answers an
// authorization error while every page still reports it as bound.
func rotateClinePassStoredToken(t *testing.T, app *App, accountID, accessToken string) {
	t.Helper()
	service := app.clinePass
	service.mu.Lock()
	defer service.mu.Unlock()
	for index := range service.accounts {
		if service.accounts[index].ID == accountID {
			// A static credential keeps the token verbatim: no prefix and no refresh call, so the
			// test asserts on the exact value the row must end up carrying.
			service.accounts[index].AccessToken = accessToken
			service.accounts[index].RefreshToken = ""
			service.accounts[index].AuthMethod = clinePassAuthMethodAPIKey
			return
		}
	}
	t.Fatalf("account %q is not stored", accountID)
}

// setClinePassOAuthCredential stores a rotating credential with an explicit expiry, which is the
// shape a Cline Pass sign-in leaves behind.
func setClinePassOAuthCredential(t *testing.T, app *App, accountID, access, refresh string, expiresAt time.Time) {
	t.Helper()
	service := app.clinePass
	service.mu.Lock()
	defer service.mu.Unlock()
	for index := range service.accounts {
		if service.accounts[index].ID == accountID {
			service.accounts[index].AccessToken = access
			service.accounts[index].RefreshToken = refresh
			service.accounts[index].ExpiresAt = expiresAt
			service.accounts[index].AuthMethod = clinePassAuthMethodOAuth
			return
		}
	}
	t.Fatalf("account %q is not stored", accountID)
}

// An expired rotating token is the other half of the same failure: nobody edited anything, the token
// simply ran out. A page read must rotate it and republish the row, so the operator no longer has to
// press refresh and repair the channel by hand.
func TestClinePassPageReadRefreshesAnExpiredTokenAndRepairsTheRow(t *testing.T) {
	app, store, accountID, headers := newClinePassPublicationApp(t, []string{"cline-pass/glm-5.3"})
	// A credential that is still valid publishes its own token into the row.
	setClinePassOAuthCredential(t, app, accountID, "workos:live-access", "workos:live-refresh", time.Now().Add(time.Hour))
	bindClinePassPublicationAccount(t, app, headers, accountID)
	entries, _ := store.snapshot()
	if got := channelCredentialKey(t, entries[0]); got != "workos:live-access" {
		t.Fatalf("the row did not publish the live token: %q", got)
	}

	// The token runs out. Nothing else changes, which is exactly the reported situation.
	setClinePassOAuthCredential(t, app, accountID, "workos:live-access", "workos:live-refresh", time.Now().Add(-time.Hour))
	payload := getClinePassAccounts(t, app, headers)
	entries, _ = store.snapshot()
	if got := channelCredentialKey(t, entries[0]); got != "workos:rotated-access" {
		t.Fatalf("the row credential after the read = %q, want the rotated token", got)
	}
	var view ClinePassAccountView
	for _, account := range payload.Accounts {
		if account.ID == accountID {
			view = account
		}
	}
	if view.Expired {
		t.Fatalf("the page still reports the refreshed account as expired: %#v", view)
	}
	if !view.ChannelBound {
		t.Fatalf("the refreshed account is not routable: %#v", view)
	}
}

// A page read must republish a row whose credential is no longer the stored one, which is exactly
// the manual delete-and-re-add an operator had to perform before this repair existed.
func TestClinePassPageReadRepairsASupersededRowCredential(t *testing.T) {
	app, store, accountID, headers := newClinePassPublicationApp(t, []string{"cline-pass/glm-5.3"})
	bindClinePassPublicationAccount(t, app, headers, accountID)
	entries, writesAfterBind := store.snapshot()
	if len(entries) != 1 || channelCredentialKey(t, entries[0]) != "sk-publication-secret" {
		t.Fatalf("the bind did not publish the current credential: %#v", entries)
	}

	// The token rotates; the row keeps the superseded one.
	rotateClinePassStoredToken(t, app, accountID, "sk-rotated-secret")
	payload := getClinePassAccounts(t, app, headers)
	entries, writes := store.snapshot()
	if writes <= writesAfterBind {
		t.Fatal("the page read did not repair the superseded row credential")
	}
	if got := channelCredentialKey(t, entries[0]); got != "sk-rotated-secret" {
		t.Fatalf("the row credential after the repair = %q, want the stored one", got)
	}
	// The repair rewrites the account's own row; it must not add a second one.
	if len(entries) != 1 {
		t.Fatalf("the repair left %d rows for one account", len(entries))
	}
	if got := channelName(t, entries[0]); got != "Cline Pass publication" {
		t.Fatalf("the repaired row lost its label: %q", got)
	}
	var view ClinePassAccountView
	for _, account := range payload.Accounts {
		if account.ID == accountID {
			view = account
		}
	}
	if !view.ChannelBound {
		t.Fatalf("the repaired account still reports unbound: %#v", view)
	}
}

// While the repair cannot run (here: the automatic-bind cooldown is still open), the page must not
// claim the account is bound: CPA would answer an authorization error for it.
func TestClinePassStaleRowReportsUnboundWhileTheRepairIsThrottled(t *testing.T) {
	app, store, accountID, headers := newClinePassPublicationApp(t, []string{"cline-pass/glm-5.3"})
	bindClinePassPublicationAccount(t, app, headers, accountID)
	rotateClinePassStoredToken(t, app, accountID, "sk-rotated-secret")
	// Pin the cooldown so the read below cannot repair the row.
	app.clinePassAutoBindMu.Lock()
	app.clinePassAutoBindAt = map[string]time.Time{accountID: time.Now()}
	app.clinePassAutoBindMu.Unlock()

	payload := getClinePassAccounts(t, app, headers)
	var view ClinePassAccountView
	for _, account := range payload.Accounts {
		if account.ID == accountID {
			view = account
		}
	}
	if view.ChannelBound {
		t.Fatalf("a superseded row was reported as bound: %#v", view)
	}
	if view.ChannelModelGaps == 0 {
		t.Fatalf("an unroutable account reported no model gap: %#v", view)
	}
	entries, _ := store.snapshot()
	if got := channelCredentialKey(t, entries[0]); got != "sk-publication-secret" {
		t.Fatalf("the throttled read rewrote the row anyway: %q", got)
	}
}

// Two rows can carry the same label (an unnamed account publishes the bare channel name), and a
// label that does not identify one row must never be treated as a superseded credential: guessing
// there would rewrite a sibling account's working row.
func TestClinePassAmbiguousLabelIsNotTreatedAsStale(t *testing.T) {
	app, store, accountID, headers := newClinePassPublicationApp(t, []string{"cline-pass/glm-5.3"})
	store.setEntries([]map[string]any{
		{
			"base-url":        clinePassDefaultBaseURL,
			"name":            "Cline Pass publication",
			"api-key-entries": []any{map[string]any{"api-key": "sk-publication-secret"}},
			"models":          []any{map[string]any{"name": "cline-pass/glm-5.3", "alias": "cline-pass/glm-5.3"}},
		},
		{
			"base-url":        clinePassDefaultBaseURL,
			"name":            "Cline Pass publication",
			"api-key-entries": []any{map[string]any{"api-key": "sk-sibling-secret"}},
			"models":          []any{map[string]any{"name": "cline-pass/glm-5.3", "alias": "cline-pass/glm-5.3"}},
		},
	})

	payload := getClinePassAccounts(t, app, headers)
	var view ClinePassAccountView
	for _, account := range payload.Accounts {
		if account.ID == accountID {
			view = account
		}
	}
	if !view.ChannelBound {
		t.Fatalf("an ambiguous label made a bound account look unbound: %#v", view)
	}
	entries, writes := store.snapshot()
	if writes != 0 {
		t.Fatalf("an ambiguous label triggered %d channel writes", writes)
	}
	if len(entries) != 2 {
		t.Fatalf("the read changed the row count: %d", len(entries))
	}
}

// newClinePassUnnamedPublicationApp binds one app to an in-memory channel store and
// the given number of accounts the operator never renamed, which is what a
// device-code sign-in leaves behind: the name is optional there. The credentials are
// numbered so a test can tell the accounts apart.
func newClinePassUnnamedPublicationApp(t *testing.T, accounts int, models []string) (*App, *clinePassChannelStore, []string, http.Header) {
	t.Helper()
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	ids := make([]string, 0, accounts)
	for index := 0; index < accounts; index++ {
		accountID, errSave := service.SaveAPIKeyAccount("", "", "", fmt.Sprintf("sk-secret-%d", index))
		if errSave != nil {
			t.Fatalf("SaveAPIKeyAccount() error = %v", errSave)
		}
		service.mu.Lock()
		for position := range service.accounts {
			if service.accounts[position].ID == accountID {
				service.accounts[position].Models = append([]string(nil), models...)
			}
		}
		service.mu.Unlock()
		ids = append(ids, accountID)
	}
	store := &clinePassChannelStore{}
	app := NewApp(&fakeAuthHost{}, nil)
	app.clinePass = service
	app.managementDoer = store.doer()
	return app, store, ids, http.Header{"Authorization": []string{"Bearer management-secret"}}
}

// Two accounts of one gateway that were never renamed must each own a channel row.
// The label they publish is per account (the id stands in for a missing name), so a
// bind can tell its own row from a sibling's instead of adopting - and rewriting -
// it. Before that, both accounts computed the same label: the second bind replaced
// the first account's credential, so exactly one of them was routable at a time and
// the other was reported as "not bound" no matter how often the page was reloaded.
func TestClinePassUnnamedAccountsWithOneGatewayGetTheirOwnChannelRows(t *testing.T) {
	app, store, ids, headers := newClinePassUnnamedPublicationApp(t, 2, []string{"cline-pass/glm-5.3"})

	first := bindClinePassPublicationAccount(t, app, headers, ids[0])
	if !first.Created {
		t.Fatalf("the first bind did not create a row: %#v", first)
	}
	second := bindClinePassPublicationAccount(t, app, headers, ids[1])
	if !second.Created {
		t.Fatalf("the second unnamed account reused the first account's row: %#v", second)
	}
	entries, _ := store.snapshot()
	if len(entries) != 2 {
		t.Fatalf("rows after binding two unnamed accounts = %d, want 2", len(entries))
	}
	names := map[string]string{}
	for _, entry := range entries {
		names[channelCredentialKey(t, entry)] = channelName(t, entry)
	}
	for _, key := range []string{"sk-secret-0", "sk-secret-1"} {
		if names[key] == "" {
			t.Fatalf("account %q owns no row of its own: %#v", key, names)
		}
	}
	if names["sk-secret-0"] == names["sk-secret-1"] {
		t.Fatalf("both accounts publish the same label again: %#v", names)
	}

	// The point of the fix: one read must report every account of the gateway as
	// bound, which is what the operator sees as a published account.
	for _, account := range getClinePassAccounts(t, app, headers).Accounts {
		if !account.ChannelBound {
			t.Fatalf("account %s is not bound: %#v", account.ID, account)
		}
	}
}

// Two accounts can still be renamed to the same name, and then one label describes
// both of them. Such a label must never authorize an adoption: the second bind
// publishes its own row instead of replacing the credential the first row routes
// through.
func TestClinePassAccountsWithTheSameNameDoNotStealEachOthersRow(t *testing.T) {
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	save := func(key string) string {
		accountID, errSave := service.SaveAPIKeyAccount("", "shared laptop", "", key)
		if errSave != nil {
			t.Fatalf("SaveAPIKeyAccount(%s) error = %v", key, errSave)
		}
		service.mu.Lock()
		for position := range service.accounts {
			if service.accounts[position].ID == accountID {
				service.accounts[position].Models = []string{"cline-pass/glm-5.3"}
			}
		}
		service.mu.Unlock()
		return accountID
	}
	firstID, secondID := save("sk-first-secret"), save("sk-second-secret")
	store := &clinePassChannelStore{}
	app := NewApp(&fakeAuthHost{}, nil)
	app.clinePass = service
	app.managementDoer = store.doer()
	headers := http.Header{"Authorization": []string{"Bearer management-secret"}}

	bindClinePassPublicationAccount(t, app, headers, firstID)
	second := bindClinePassPublicationAccount(t, app, headers, secondID)
	if !second.Created {
		t.Fatalf("a shared label let the second account adopt the first account's row: %#v", second)
	}
	entries, _ := store.snapshot()
	if len(entries) != 2 {
		t.Fatalf("rows = %d, want one row per account", len(entries))
	}
	keys := map[string]bool{}
	for _, entry := range entries {
		keys[channelCredentialKey(t, entry)] = true
	}
	if !keys["sk-first-secret"] || !keys["sk-second-secret"] {
		t.Fatalf("an account lost its credential: %#v", keys)
	}
}

// A row that still carries the shared default name was written before the label held
// an account identity. A bind of its own account renames that row instead of adding a
// second one, so the two accounts of a gateway end up distinguishable without
// leaving an orphan row behind.
func TestClinePassBindRenamesTheSharedDefaultRow(t *testing.T) {
	app, store, accountID, headers := newClinePassPublicationApp(t, []string{"cline-pass/glm-5.3"})
	store.setEntries([]map[string]any{{
		"base-url":        clinePassDefaultBaseURL,
		"name":            clinePassBoundChannelName,
		"api-key-entries": []any{map[string]any{"api-key": "sk-publication-secret"}},
		"models":          []any{map[string]any{"name": "cline-pass/glm-5.3", "alias": "cline-pass/glm-5.3"}},
	}})

	result := bindClinePassPublicationAccount(t, app, headers, accountID)
	if result.Created {
		t.Fatalf("the account's own row was duplicated: %#v", result)
	}
	entries, _ := store.snapshot()
	if len(entries) != 1 {
		t.Fatalf("rows = %d, want the renamed row alone", len(entries))
	}
	if got := channelName(t, entries[0]); got != "Cline Pass publication" {
		t.Fatalf("row name = %q, want the account's own label", got)
	}
	if got := channelCredentialKey(t, entries[0]); got != "sk-publication-secret" {
		t.Fatalf("the rename changed the credential: %q", got)
	}
}

// Two accounts renamed to the same name cannot be told apart by their label, so the digest the
// account's last bind recorded is what keeps a rotation on its own row - including after a
// restart, because the digest is persisted with the account. A second row for the same account
// would leave CPA routing half the traffic through a credential the gateway already revoked.
func TestClinePassSameNamedRotationStaysOnOneRow(t *testing.T) {
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	save := func(key string) string {
		accountID, errSave := service.SaveAPIKeyAccount("", "shared laptop", "", key)
		if errSave != nil {
			t.Fatalf("SaveAPIKeyAccount(%s) error = %v", key, errSave)
		}
		service.mu.Lock()
		for position := range service.accounts {
			if service.accounts[position].ID == accountID {
				service.accounts[position].Models = []string{"cline-pass/glm-5.3"}
			}
		}
		service.mu.Unlock()
		return accountID
	}
	firstID, secondID := save("sk-first-secret"), save("sk-second-secret")
	store := &clinePassChannelStore{}
	app := NewApp(&fakeAuthHost{}, nil)
	app.clinePass = service
	app.managementDoer = store.doer()
	headers := http.Header{"Authorization": []string{"Bearer management-secret"}}
	bindClinePassPublicationAccount(t, app, headers, firstID)
	bindClinePassPublicationAccount(t, app, headers, secondID)

	rotateClinePassStoredToken(t, app, firstID, "sk-first-rotated")
	if result := bindClinePassPublicationAccount(t, app, headers, firstID); result.Created {
		t.Fatalf("a rotation after the same-name bind added a second row: %#v", result)
	}
	entries, _ := store.snapshot()
	if len(entries) != 2 {
		t.Fatalf("rows = %d, want one per account", len(entries))
	}
	keys := map[string]bool{}
	for _, entry := range entries {
		keys[channelCredentialKey(t, entry)] = true
	}
	if !keys["sk-first-rotated"] || !keys["sk-second-secret"] {
		t.Fatalf("the rotation did not stay on the account's own row: %#v", keys)
	}
}

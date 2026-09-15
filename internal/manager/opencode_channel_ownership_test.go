package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

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

// The plugin's own label store must not rename siblings: the URL record is shared by every channel
// with that base URL, so it may only carry a label while a single channel owns it.
func TestAIProviderChannelBaseURLOwnersCountsSiblings(t *testing.T) {
	first := map[string]any{"base-url": "https://api.cline.bot/api/v1"}
	entries := []map[string]any{
		first,
		{"base-url": "https://api.cline.bot/api/v1/"},
		{"base-url": "https://opencode.ai/zen/v1"},
	}
	if got := aiProviderChannelBaseURLOwners(entries, first); got != 2 {
		t.Fatalf("owners = %d, want 2 for the shared gateway", got)
	}
	zen := entries[2]
	if got := aiProviderChannelBaseURLOwners(entries, zen); got != 1 {
		t.Fatalf("owners = %d, want 1 for the unshared gateway", got)
	}
	if got := aiProviderChannelBaseURLOwners(entries, map[string]any{}); got != 0 {
		t.Fatalf("owners = %d, want 0 for an entry without a base URL", got)
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
	// An account with no row of its own still resolves through the gateway row it shares, so a
	// deployment that has not re-bound yet keeps reporting a bound account instead of a broken one.
	if got := views[thirdID]; !got.ChannelBound {
		t.Fatalf("an account without its own row lost the shared-gateway fallback: %#v", got)
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

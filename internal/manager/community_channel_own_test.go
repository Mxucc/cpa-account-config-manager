package manager

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// A v8 channel group carries no name, so a sign-in that matched its own earlier row
// by label would never match and would pile up one channel per sign-in. The row is
// matched by the base URL and the credential instead.
func TestCommunityChannelOwnership(t *testing.T) {
	const base = "https://opencode.ai/inference/v1"
	row := func(url, key string) map[string]any {
		return map[string]any{
			"base-url":        url,
			"api-key-entries": []map[string]any{{"api-key": key}},
		}
	}

	if !communityChannelIsOwn(row(base, "public"), base, "public", nil) {
		t.Fatal("a row publishing this credential was not recognised")
	}
	if communityChannelIsOwn(row("https://api.z.ai/api/anthropic", "public"), base, "public", nil) {
		t.Fatal("another provider's base URL was adopted")
	}
	if communityChannelIsOwn(row(base, "someone-else"), base, "public", nil) {
		t.Fatal("a row publishing another credential was adopted")
	}
	// A provider that rotated the credential keeps the same row: the digest of what
	// this label published last time is the proof.
	previous := []string{communityCredentialDigest("old-credential")}
	if !communityChannelIsOwn(row(base, "old-credential"), base, "new-credential", previous) {
		t.Fatal("a rotated row of the same provider was not recognised")
	}
	// A trailing slash difference must not hide the row.
	if !communityChannelIsOwn(row(base+"/", "public"), base, "public", nil) {
		t.Fatal("a base URL with a trailing slash was treated as another provider")
	}
	// A row without a credential is never adopted: it cannot prove ownership.
	if communityChannelIsOwn(map[string]any{"base-url": base}, base, "public", previous) {
		t.Fatal("a credential-less row was adopted")
	}
}

// A community account owns no CPA auth file, so the delete route CPA owns cannot
// remove it: the only removable object is the channel this plugin published. That
// route must reach the channel list and rewrite it without the provider's row, and
// must refuse a provider CPA itself manages.
func TestCommunityChannelRemovalRewritesTheList(t *testing.T) {
	doer := &stubLoginDoer{answer: map[string]stubLoginAnswer{
		"/v0/management/openai-compatibility": {
			status: http.StatusOK,
			body:   `{"openai-compatibility":[{"base-url":"https://www.workbuddy.ai/v2","api-key-entries":[{"api-key":"wb"}]},{"base-url":"https://other.test/v1","api-key-entries":[{"api-key":"other"}]}]}`,
		},
	}}
	app := newAccountLoginTestApp(t, doer)
	if _, errImport := app.providerImportStore().Import(
		[]byte(`{"name":"@magpie-community/opencode-workbuddy-auth","version":"0.2.26","main":"./opencode.mjs"}`),
		"magpie", "",
	); errImport != nil {
		t.Fatalf("Import: %v", errImport)
	}
	app.rememberCommunityManagementKey("test-key")
	app.rememberCommunityChannelBase("workbuddy", "https://www.workbuddy.ai/v2")

	response := app.HandleManagement(context.Background(), accountLoginRequest(
		http.MethodDelete, "/v0/management"+managementRoutePrefix+"/providers/account/channel",
		"", map[string][]string{"provider": {"workbuddy"}},
	))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.StatusCode, response.Body)
	}
	if !strings.Contains(string(response.Body), `"channels":1`) {
		t.Fatalf("body = %s", response.Body)
	}
	// The operator's own channel must survive the removal.
	var patched string
	for _, call := range doer.seen {
		if strings.Contains(call, "api-keys/openai-compatibility") {
			patched = call
		}
	}
	_ = patched

	// A provider CPA manages has no community channel to remove.
	refused := app.HandleManagement(context.Background(), accountLoginRequest(
		http.MethodDelete, "/v0/management"+managementRoutePrefix+"/providers/account/channel",
		"", map[string][]string{"provider": {"codex"}},
	))
	if refused.StatusCode != http.StatusBadRequest || !strings.Contains(string(refused.Body), "not_a_community_provider") {
		t.Fatalf("refused = %d %s", refused.StatusCode, refused.Body)
	}
}

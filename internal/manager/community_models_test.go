package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// A community provider's model list is what CPA routes, and until now the page could
// only show a count: no ids, no choice, no way to change what is published. The model
// routes must answer with the catalog and what the channel currently lists, and a
// write must replace exactly that list on the channel this plugin owns.
func TestCommunityModelsReadAndWrite(t *testing.T) {
	channelBody := `{"openai-compatibility":[{"base-url":"https://www.workbuddy.ai/v2","api-key-entries":[{"api-key":"wb"}],"models":[{"name":"glm-5.3","alias":"glm-5.3"}]},{"base-url":"https://other.test/v1","api-key-entries":[{"api-key":"other"}]}]}`
	doer := &stubLoginDoer{answer: map[string]stubLoginAnswer{
		"/v0/management/openai-compatibility": {status: http.StatusOK, body: channelBody},
	}}
	app := newAccountLoginTestApp(t, doer)
	app.rememberCommunityManagementKey("test-key")
	app.rememberCommunityChannelBase("workbuddy", "https://www.workbuddy.ai/v2")
	if _, errImport := app.providerImportStore().Import(
		[]byte(`{"name":"@magpie-community/opencode-workbuddy-auth","version":"0.2.26","main":"./opencode.mjs"}`),
		"magpie", "",
	); errImport != nil {
		t.Fatalf("Import: %v", errImport)
	}

	read := app.HandleManagement(context.Background(), accountLoginRequest(
		http.MethodGet, "/v0/management"+managementRoutePrefix+"/providers/models",
		"", map[string][]string{"provider": {"workbuddy"}},
	))
	if read.StatusCode != http.StatusOK {
		t.Fatalf("read status = %d body = %s", read.StatusCode, read.Body)
	}
	var view struct {
		Available []string `json:"available"`
		Published []string `json:"published"`
		Rows      []struct {
			ID        string `json:"id"`
			Published bool   `json:"published"`
		} `json:"rows"`
	}
	if errDecode := json.Unmarshal(read.Body, &view); errDecode != nil {
		t.Fatalf("decode read: %v", errDecode)
	}
	// The channel lists glm-5.3; the answer must say so rather than only counting.
	if len(view.Published) != 1 || view.Published[0] != "glm-5.3" {
		t.Fatalf("published = %v", view.Published)
	}
	found := false
	for _, row := range view.Rows {
		if row.ID == "glm-5.3" && row.Published {
			found = true
		}
	}
	if !found && len(view.Rows) > 0 {
		t.Fatalf("rows = %#v", view.Rows)
	}

	write := app.HandleManagement(context.Background(), accountLoginRequest(
		http.MethodPut, "/v0/management"+managementRoutePrefix+"/providers/models",
		`{"models":["hy3","glm-5.3"]}`, map[string][]string{"provider": {"workbuddy"}},
	))
	if write.StatusCode != http.StatusOK || !strings.Contains(string(write.Body), `"published":2`) {
		t.Fatalf("write = %d %s", write.StatusCode, write.Body)
	}

	// A provider CPA manages has no channel this plugin owns.
	refused := app.HandleManagement(context.Background(), accountLoginRequest(
		http.MethodGet, "/v0/management"+managementRoutePrefix+"/providers/models",
		"", map[string][]string{"provider": {"codex"}},
	))
	if refused.StatusCode != http.StatusBadRequest || !strings.Contains(string(refused.Body), "not_a_community_provider") {
		t.Fatalf("refused = %d %s", refused.StatusCode, refused.Body)
	}
}

// An empty selection would publish a channel that routes nothing; the route says so
// instead of silently writing an unroutable row.
func TestCommunityModelsRejectAnEmptySelection(t *testing.T) {
	app := newAccountLoginTestApp(t, &stubLoginDoer{answer: map[string]stubLoginAnswer{}})
	app.rememberCommunityManagementKey("test-key")
	if _, errImport := app.providerImportStore().Import(
		[]byte(`{"name":"@magpie-community/opencode-workbuddy-auth","version":"0.2.26","main":"./opencode.mjs"}`),
		"magpie", "",
	); errImport != nil {
		t.Fatalf("Import: %v", errImport)
	}
	write := app.HandleManagement(context.Background(), accountLoginRequest(
		http.MethodPut, "/v0/management"+managementRoutePrefix+"/providers/models",
		`{"models":[]}`, map[string][]string{"provider": {"workbuddy"}},
	))
	if write.StatusCode != http.StatusOK || !strings.Contains(string(write.Body), "select at least one model") {
		t.Fatalf("write = %d %s", write.StatusCode, write.Body)
	}
}

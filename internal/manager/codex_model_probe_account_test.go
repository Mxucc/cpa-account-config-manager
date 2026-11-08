package manager

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The unified account page sends {account_id, model}. The Codex branch used to
// require channel_index and answered 400 to the shape the page actually sends,
// which is why "test model" never ran for a Codex account.
func TestCodexModelTestAcceptsAnAccountID(t *testing.T) {
	app := newAccountLoginTestApp(t, &stubLoginDoer{})
	body := `{"account_id":"acct-1","model":"gpt-5.3-codex-spark"}`
	response := app.HandleManagement(t.Context(), accountLoginRequest(
		http.MethodPost, providerRoutePrefix+"/account/test", body, map[string][]string{"provider": {"codex"}},
	))
	// The request is routed to CPA's account model-test path, so the answer is
	// no longer the 400 "invalid Codex model test request"; the account itself
	// does not exist in this fixture, which is reported as 404.
	if response.StatusCode == http.StatusBadRequest && strings.Contains(string(response.Body), "invalid Codex model test request") {
		t.Fatalf("the account_id shape was still rejected: %s", response.Body)
	}
	var decoded map[string]any
	_ = json.Unmarshal(response.Body, &decoded)
	if reason, _ := decoded["error"].(string); reason == "" {
		t.Fatalf("expected an error for a missing account, got %s", response.Body)
	}
}

func TestCodexModelTestStillRequiresAModel(t *testing.T) {
	app := newAccountLoginTestApp(t, &stubLoginDoer{})
	response := app.HandleManagement(t.Context(), accountLoginRequest(
		http.MethodPost, providerRoutePrefix+"/account/test", `{"account_id":"acct-1"}`, map[string][]string{"provider": {"codex"}},
	))
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(string(response.Body), "model is required") {
		t.Fatalf("response = %d %s", response.StatusCode, response.Body)
	}
}

// The account model-test request rejects unknown fields, so the Codex branch must
// forward only the two it declares. Forwarding timeout_seconds made the probe
// answer "json: unknown field" instead of running.
func TestCodexModelTestForwardsOnlyDeclaredFields(t *testing.T) {
	app := newAccountLoginTestApp(t, &stubLoginDoer{})
	response := app.HandleManagement(t.Context(), accountLoginRequest(
		http.MethodPost, providerRoutePrefix+"/account/test",
		`{"account_id":"acct-1","model":"gpt-5.3-codex-spark","timeout_seconds":30}`,
		map[string][]string{"provider": {"codex"}},
	))
	if strings.Contains(string(response.Body), "unknown field") {
		t.Fatalf("the forward carried an undeclared field: %s", response.Body)
	}
}

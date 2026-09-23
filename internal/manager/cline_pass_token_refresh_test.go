package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// Cline Pass token rotation and the channel row.
//
// A rotation replaces the access token the gateway accepts, and CPA routes through the token
// published on the channel row. A rotation that is not published therefore turns every routed
// request into an authorization error while the account keeps looking bound - which is what a
// manual model test, a model-list reload and every page read can trigger.

// newClinePassRotatingQuotaApp publishes one OAuth account whose token is inside the refresh
// margin, so the next request path that reads the credential rotates it.
func newClinePassRotatingQuotaApp(t *testing.T, models []string) (*App, *clinePassChannelStore, *clinePassFakeGateway, string, http.Header) {
	t.Helper()
	app, store, gateway, ids, headers := newClinePassQuotaApp(t, 1, models)
	// The bind itself reads the credential, so it is published while the token is comfortably
	// valid; only then is the token put inside the refresh margin, which is the state the next
	// read rotates.
	setClinePassOAuthCredential(t, app, ids[0], "workos:live-access", "workos:live-refresh", time.Now().UTC().Add(time.Hour))
	if result := bindClinePassPublicationAccount(t, app, headers, ids[0]); !result.Created {
		t.Fatalf("the first bind did not create the account's row: %#v", result)
	}
	if key := rowCredentialByAccount(t, store, "workos:live-access"); key == "" {
		t.Fatalf("the fixture row does not carry the live token: %#v", allRowCredentials(t, store))
	}
	setClinePassOAuthCredential(t, app, ids[0], "workos:live-access", "workos:live-refresh", time.Now().UTC().Add(time.Minute))
	return app, store, gateway, ids[0], headers
}

// rowCredentialByAccount reports the credential published by the row that carries the given
// credential, or "" when no such row exists.
func rowCredentialByAccount(t *testing.T, store *clinePassChannelStore, credential string) string {
	t.Helper()
	entries, _ := store.snapshot()
	for _, entry := range entries {
		if channelCredentialKey(t, entry) == credential {
			return credential
		}
	}
	return ""
}

// allRowCredentials reports the credential of every published row.
func allRowCredentials(t *testing.T, store *clinePassChannelStore) []string {
	t.Helper()
	entries, _ := store.snapshot()
	credentials := make([]string, 0, len(entries))
	for _, entry := range entries {
		credentials = append(credentials, channelCredentialKey(t, entry))
	}
	return credentials
}

// A model test rotates an expiring token before it probes. The rotation invalidates the token CPA
// still routes through, so the row must be published again by the same request: testing one model
// must never be what breaks every other call.
func TestClinePassModelTestRepublishesTheRowAfterARotation(t *testing.T) {
	app, store, gateway, accountID, headers := newClinePassRotatingQuotaApp(t, []string{"cline-pass/glm-5.3"})

	if result := runClinePassModelTest(t, app, headers, accountID, "cline-pass/glm-5.3"); result.Status != "available" {
		t.Fatalf("probe status = %q", result.Status)
	}
	if _, _, _, refreshCalls, _, _ := gateway.counters(); refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want one rotation", refreshCalls)
	}
	if live := app.clinePass.accessToken(accountID); live != "workos:rotated-access" {
		t.Fatalf("stored token = %q, want the rotated one", live)
	}
	if credentials := allRowCredentials(t, store); len(credentials) != 1 || credentials[0] != "workos:rotated-access" {
		t.Fatalf("published rows = %#v, want the row republished with the rotated token", credentials)
	}
}

// Reloading the model list reads the credential too, so the same rule applies: the row follows the
// rotation in the same request.
func TestClinePassModelReloadRepublishesTheRowAfterARotation(t *testing.T) {
	app, store, _, accountID, headers := newClinePassRotatingQuotaApp(t, []string{"cline-pass/glm-5.3"})

	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/models", Headers: headers,
		Body: []byte(`{"account_id":"` + accountID + `"}`),
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("load-models status = %d body=%s", response.StatusCode, response.Body)
	}
	if live := app.clinePass.accessToken(accountID); live != "workos:rotated-access" {
		t.Fatalf("stored token = %q, want the rotated one", live)
	}
	if credentials := allRowCredentials(t, store); len(credentials) != 1 || credentials[0] != "workos:rotated-access" {
		t.Fatalf("published rows = %#v, want the row republished with the rotated token", credentials)
	}
}

// A rotation the gateway refuses with a spent refresh token is the failure that needs a new
// sign-in. It has to reach the operator - the journal and the account's routing state - instead of
// showing up only as an account that quietly turned "unbound".
func TestClinePassRefusedRotationIsJournaledAndReported(t *testing.T) {
	app, _, gateway, accountID, headers := newClinePassRotatingQuotaApp(t, []string{"cline-pass/glm-5.3"})
	gateway.mu.Lock()
	gateway.refreshStatus = http.StatusBadRequest
	gateway.mu.Unlock()

	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/model-test", Headers: headers,
		Body: []byte(`{"account_id":"` + accountID + `","model":"cline-pass/glm-5.3"}`),
	})
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("model-test status = %d body=%s", response.StatusCode, response.Body)
	}
	if !app.clinePass.AuthFailurePending(accountID) {
		t.Fatal("a refused rotation did not mark the account as needing a sign-in")
	}
	journal := app.operations.List(OperationQuery{Page: 1, PageSize: operationPageSize})
	found := false
	for _, operation := range journal.Operations {
		if operation.ReasonCode == OperationFailureClinePassTokenRefresh {
			found = true
			if operation.TargetID != accountID {
				t.Fatalf("the journal entry names %q, want the account", operation.TargetID)
			}
		}
	}
	if !found {
		t.Fatalf("the refused rotation was not journaled: %#v", journal.Operations)
	}
	// The row is not republished with a token that does not exist, and the account page says why
	// instead of leaving the operator with a bare "unbound".
	payload := getClinePassAccounts(t, app, headers)
	var view ClinePassAccountView
	for _, account := range payload.Accounts {
		if account.ID == accountID {
			view = account
		}
	}
	if !view.ChannelCredentialRejected || view.ChannelBound {
		t.Fatalf("the account page does not report the refused credential: %#v", view)
	}
	if view.ChannelBindingError == "" {
		t.Fatalf("the account page reports no reason for the failed publish: %#v", view)
	}
}

// A transient rotation failure is not a reason to tell the operator to sign in again: the stored
// token still works until it expires, so the account keeps its state and the next pass retries.
func TestClinePassTransientRotationFailureKeepsTheAccount(t *testing.T) {
	app, _, gateway, accountID, headers := newClinePassRotatingQuotaApp(t, []string{"cline-pass/glm-5.3"})
	gateway.mu.Lock()
	gateway.refreshStatus = http.StatusBadGateway
	gateway.refreshErrorBody = `{"error":"upstream unavailable"}`
	gateway.mu.Unlock()

	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/model-test", Headers: headers,
		Body: []byte(`{"account_id":"` + accountID + `","model":"cline-pass/glm-5.3"}`),
	})
	var payload map[string]any
	if errDecode := json.Unmarshal(response.Body, &payload); errDecode != nil {
		t.Fatalf("decode model-test: %v", errDecode)
	}
	if app.clinePass.AuthFailurePending(accountID) {
		t.Fatal("a transient rotation failure asked the operator for a new sign-in")
	}
	if live := app.clinePass.accessToken(accountID); live != "workos:live-access" {
		t.Fatalf("stored token = %q, want the previous one kept", live)
	}
}

// A rotated token's expiry is what the gateway says it is. The plugin used to assume a fixed
// lifetime, so a gateway handing out shorter-lived tokens left the stored credential looking valid
// long after the gateway had stopped accepting it.
func TestClinePassRotationUsesTheGatewayExpiry(t *testing.T) {
	app, _, gateway, accountID, headers := newClinePassRotatingQuotaApp(t, []string{"cline-pass/glm-5.3"})
	gateway.mu.Lock()
	gateway.refreshBody = `{"data":{"accessToken":"rotated-access","refreshToken":"rotated-refresh","expiresIn":600}}`
	gateway.mu.Unlock()

	runClinePassModelTest(t, app, headers, accountID, "cline-pass/glm-5.3")

	view, found := app.clinePass.AccountView(accountID)
	if !found || view.ExpiresAt == nil {
		t.Fatalf("the rotated account has no expiry: %#v", view)
	}
	remaining := time.Until(*view.ExpiresAt)
	if remaining < 8*time.Minute || remaining > 11*time.Minute {
		t.Fatalf("recorded expiry = %v from now, want the 600s the gateway named", remaining)
	}
}

// A nonsense lifetime is bounded instead of trusted, so a stray number in the answer cannot park an
// account behind an expiry far in the future.
func TestClinePassRotationBoundsANonsenseExpiry(t *testing.T) {
	app, _, gateway, accountID, headers := newClinePassRotatingQuotaApp(t, []string{"cline-pass/glm-5.3"})
	gateway.mu.Lock()
	gateway.refreshBody = `{"data":{"accessToken":"rotated-access","refreshToken":"rotated-refresh","expiresIn":999999999}}`
	gateway.mu.Unlock()

	runClinePassModelTest(t, app, headers, accountID, "cline-pass/glm-5.3")

	view, found := app.clinePass.AccountView(accountID)
	if !found || view.ExpiresAt == nil {
		t.Fatalf("the rotated account has no expiry: %#v", view)
	}
	if remaining := time.Until(*view.ExpiresAt); remaining > clinePassMaxTokenLifetime+time.Minute {
		t.Fatalf("recorded expiry = %v from now, want it bounded by %v", remaining, clinePassMaxTokenLifetime)
	}
}

// The AI-provider channel test the operator runs when calls fail exercises the credential CPA
// routes through. A refusal there has to start the same repair a failing request starts, otherwise
// the row stays exactly as it was and the next call fails the same way.
func TestClinePassChannelTestRejectionStartsTheRepair(t *testing.T) {
	app, store, accountID, headers := publishedOAuthClinePassApp(t)
	app.operations.Configure(Config{DataDir: t.TempDir()})

	// The operator tests the published row and the gateway refuses that credential.
	app.noteClinePassChannelTestRejection("openai-compatibility", "workos:live-access", AIProviderProbeResult{
		Reachable: true, StatusCode: http.StatusUnauthorized, ReasonCode: "authentication_failed",
	})
	if !app.clinePass.AuthFailurePending(accountID) {
		t.Fatal("a refused channel test did not mark the account as rejected")
	}
	journal := app.operations.List(OperationQuery{Page: 1, PageSize: operationPageSize})
	found := false
	for _, operation := range journal.Operations {
		if operation.ReasonCode == OperationFailureClinePassCredentialRejected {
			found = true
		}
	}
	if !found {
		t.Fatalf("the refused channel test was not journaled: %#v", journal.Operations)
	}

	// The maintenance pass rotates the token and republishes the row it routes through.
	app.repairRejectedClinePassAccounts(context.Background(), resolveManagementKey(headers))
	if app.clinePass.AuthFailurePending(accountID) {
		t.Fatal("the repair did not spend the rejection record")
	}
	if credentials := allRowCredentials(t, store); len(credentials) != 1 || credentials[0] != "workos:rotated-access" {
		t.Fatalf("published rows = %#v, want the row republished with the rotated token", credentials)
	}
}

// The plugin's own model test is the other place the operator looks, and a 401 there means the same
// thing: the account is recorded and its row is rotated and republished. The automatic repair may
// already have run by the time the response is read (the request itself hands the plugin a management
// key), so the test asserts the outcome the operator depends on - a recorded rejection and a row that
// ends up carrying the rotated token - instead of a race-sensitive intermediate flag.
func TestClinePassModelTestRejectionStartsTheRepair(t *testing.T) {
	app, store, gateway, accountID, headers := newClinePassRotatingQuotaApp(t, []string{"cline-pass/glm-5.3"})
	app.operations.Configure(Config{DataDir: t.TempDir()})
	gateway.mu.Lock()
	gateway.chatStatus = http.StatusUnauthorized
	gateway.chatBody = `{"error":"unauthorized: Please make sure you're using the latest version of Cline and re-authenticate your Cline account."}`
	gateway.mu.Unlock()

	result := runClinePassModelTest(t, app, headers, accountID, "cline-pass/glm-5.3")
	if result.ReasonCode != "authentication_failed" {
		t.Fatalf("probe reason = %q, want the gateway's authentication failure", result.ReasonCode)
	}
	journal := app.operations.List(OperationQuery{Page: 1, PageSize: operationPageSize})
	rejected := false
	for _, operation := range journal.Operations {
		if operation.ReasonCode == OperationFailureClinePassCredentialRejected {
			rejected = true
		}
	}
	if !rejected {
		t.Fatalf("the refused model test was not journaled: %#v", journal.Operations)
	}

	// Whether the automatic repair ran on its own or not, the row must end up carrying a working
	// token: the test is what the operator ran because calls were failing.
	app.repairRejectedClinePassAccounts(context.Background(), resolveManagementKey(headers))
	if credentials := allRowCredentials(t, store); len(credentials) != 1 || credentials[0] != "workos:rotated-access" {
		t.Fatalf("published rows = %#v, want the row republished with the rotated token", credentials)
	}
	if app.clinePass.AuthFailurePending(accountID) {
		t.Fatal("a repaired account is still reported as rejected")
	}
}

// A probe that failed for another reason says nothing about the stored credential, so it must not
// put the account into the repair loop.
func TestClinePassUnrelatedProbeFailureIsNotACredentialRejection(t *testing.T) {
	app, _, _, accountID, _ := newClinePassRotatingQuotaApp(t, []string{"cline-pass/glm-5.3"})
	app.noteClinePassChannelTestRejection("openai-compatibility", "workos:live-access", AIProviderProbeResult{
		Reachable: true, StatusCode: http.StatusNotFound, ReasonCode: "model_not_found",
	})
	if app.clinePass.AuthFailurePending(accountID) {
		t.Fatal("a probe that only failed to find a model was treated as a credential rejection")
	}
}

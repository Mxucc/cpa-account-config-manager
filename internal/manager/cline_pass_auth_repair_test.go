package manager

import (
	"context"
	"net/http"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// A Cline Pass token can be rejected before its recorded expiry, and CPA routes only through the key
// stored on its channel row. The completion callback is where that rejection is observed, so these
// tests drive the whole chain: learn from the finished request, rotate the token, rewrite the row,
// and report the state honestly in between.

const (
	clinePassRejectedUpstreamError = "Unauthorized: Please make sure you're using the latest version of Cline and re-authenticate your Cline account."
	clinePassColdCredentialError   = `auth_unavailable: no auth available (providers=openai-compatible-cline pass, model=deepseek-v4.1-flash; last upstream error: Unauthorized: Please make sure you're using the latest version of Cline and re-authenticate your Cline account.)`
)

// publishedOAuthClinePassApp publishes an account whose token the gateway will rotate, so a repair
// writes a visibly different credential than the one the row started with.
func publishedOAuthClinePassApp(t *testing.T) (*App, *clinePassChannelStore, string, http.Header) {
	t.Helper()
	app, store, accountID, headers := newClinePassPublicationApp(t, []string{"cline-pass/glm-5.3"})
	setClinePassOAuthCredential(t, app, accountID, "workos:live-access", "workos:live-refresh", time.Now().Add(time.Hour))
	if result := bindClinePassPublicationAccount(t, app, headers, accountID); result.Created == false {
		t.Fatalf("the first bind did not create the account's row: %#v", result)
	}
	entries, _ := store.snapshot()
	if len(entries) != 1 || channelCredentialKey(t, entries[0]) != "workos:live-access" {
		t.Fatalf("the published row does not carry the live token: %#v", entries)
	}
	// The bind itself is a management request, so it hands the app a management key. A test that
	// wants the automatic repair resets this, because the repair runs on its own goroutine and a test
	// asserts on state it can wait for.
	forgetClinePassRepairKey(app)
	return app, store, accountID, headers
}

// forgetClinePassRepairKey models an app that has not seen a management request yet.
func forgetClinePassRepairKey(app *App) {
	app.clinePassRepairMu.Lock()
	defer app.clinePassRepairMu.Unlock()
	app.clinePassRepairKey = ""
	app.clinePassRepairAt = time.Time{}
	app.clinePassRepairRunning = false
}

func rejectedClinePassCompletion(accountID string, status int, message string) cpaapi.RequestCompletion {
	return cpaapi.RequestCompletion{
		RequestID:  "req-rejected-1",
		Outcome:    requestCompletionFailed,
		StatusCode: status,
		Error:      message,
		Model:      "deepseek-v4.1-flash",
		Metadata:   map[string]any{"selected_auth_id": accountID},
	}
}

func TestClinePassRejectedCompletionRotatesAndRepublishesTheRow(t *testing.T) {
	app, store, accountID, _ := publishedOAuthClinePassApp(t)
	if !app.clinePass.AuthFailurePending(accountID) == false {
		t.Fatal("an account that has not failed is already recorded as rejected")
	}

	app.noteClinePassRequestOutcome(rejectedClinePassCompletion(accountID, http.StatusUnauthorized, clinePassRejectedUpstreamError))
	if !app.clinePass.AuthFailurePending(accountID) {
		t.Fatal("the rejected credential was not recorded")
	}
	// The operator sees why the account stopped working.
	journal := app.operations.List(OperationQuery{Page: 1, PageSize: operationPageSize})
	foundRejection := false
	for _, operation := range journal.Operations {
		if operation.ReasonCode == "cline_pass_credential_rejected" {
			foundRejection = true
		}
	}
	if !foundRejection {
		t.Fatalf("the rejection is not journalled: %#v", journal.Operations)
	}

	// The repair rotates the token and rewrites the row CPA routes through.
	app.repairRejectedClinePassAccounts(context.Background(), "management-secret")
	entries, _ := store.snapshot()
	if len(entries) != 1 {
		t.Fatalf("the repair left %d rows for one account", len(entries))
	}
	if got := channelCredentialKey(t, entries[0]); got != "workos:rotated-access" {
		t.Fatalf("the repaired row credential = %q, want the rotated token", got)
	}
	if app.clinePass.AuthFailurePending(accountID) {
		t.Fatal("the account still asks for a repair after a successful rotation")
	}
}

func TestClinePassColdCredentialAnswerAlsoTriggersTheRepair(t *testing.T) {
	// The answer the operator actually saw: CPA had already cooled the credential down, so the
	// status is 503 and only the message names the rejected token.
	app, _, accountID, _ := publishedOAuthClinePassApp(t)
	app.noteClinePassRequestOutcome(rejectedClinePassCompletion(accountID, http.StatusServiceUnavailable, clinePassColdCredentialError))
	if !app.clinePass.AuthFailurePending(accountID) {
		t.Fatal("the cold-credential answer was not treated as a rejection")
	}
}

func TestClinePassOtherFailuresDoNotRotate(t *testing.T) {
	cases := map[string]cpaapi.RequestCompletion{
		"quota limited": {RequestID: "r1", Outcome: requestCompletionFailed, StatusCode: 429, Error: "rate limit exceeded", Model: "deepseek-v4.1-flash"},
		"upstream error": {RequestID: "r2", Outcome: requestCompletionFailed, StatusCode: 500,
			Error: "internal server error", Model: "deepseek-v4.1-flash"},
		"client canceled": {RequestID: "r3", Outcome: requestCompletionCanceled, StatusCode: 503,
			Error: clinePassColdCredentialError, Model: "deepseek-v4.1-flash"},
	}
	for name, completion := range cases {
		t.Run(name, func(t *testing.T) {
			app, store, accountID, _ := publishedOAuthClinePassApp(t)
			completion.Metadata = map[string]any{"selected_auth_id": accountID}
			app.noteClinePassRequestOutcome(completion)
			if app.clinePass.AuthFailurePending(accountID) {
				t.Fatal("a failure that needs no rotation was recorded as a rejected credential")
			}
			app.repairRejectedClinePassAccounts(context.Background(), "management-secret")
			entries, _ := store.snapshot()
			if got := channelCredentialKey(t, entries[0]); got != "workos:live-access" {
				t.Fatalf("the row was rewritten anyway: %q", got)
			}
		})
	}
}

func TestClinePassSuccessfulCompletionClearsTheRecord(t *testing.T) {
	app, _, accountID, _ := publishedOAuthClinePassApp(t)
	app.noteClinePassRequestOutcome(rejectedClinePassCompletion(accountID, http.StatusUnauthorized, clinePassRejectedUpstreamError))
	if !app.clinePass.AuthFailurePending(accountID) {
		t.Fatal("the rejected credential was not recorded")
	}
	app.noteClinePassRequestOutcome(cpaapi.RequestCompletion{
		RequestID: "req-ok", Outcome: requestCompletionSucceeded, StatusCode: 200,
		Model: "deepseek-v4.1-flash", Metadata: map[string]any{"selected_auth_id": accountID},
	})
	if app.clinePass.AuthFailurePending(accountID) {
		t.Fatal("a working credential kept its recorded rejection")
	}
}

func TestClinePassAccountPageReportsARejectedCredential(t *testing.T) {
	app, store, accountID, headers := publishedOAuthClinePassApp(t)
	app.noteClinePassRequestOutcome(rejectedClinePassCompletion(accountID, http.StatusUnauthorized, clinePassRejectedUpstreamError))
	// The page read repairs what it can, so the rejected flag is what an operator sees while that
	// repair cannot complete: here CPA refuses the channel write.
	store.failWrites = true

	var view ClinePassAccountView
	for _, account := range getClinePassAccounts(t, app, headers).Accounts {
		if account.ID == accountID {
			view = account
		}
	}
	if !view.ChannelCredentialRejected {
		t.Fatalf("the page does not flag the rejected credential: %#v", view)
	}
	if view.ChannelBound {
		t.Fatalf("a rejected credential was still reported as routable: %#v", view)
	}
	if view.ChannelModelGaps == 0 {
		t.Fatalf("an unroutable account reported no model gap: %#v", view)
	}
}

func TestClinePassRepairNeedsAManagementKeyAndCoalesces(t *testing.T) {
	app, store, accountID, headers := publishedOAuthClinePassApp(t)
	// A page read is what hands the plugin a management key, so this models an app that has not seen
	// one yet: a repair must not claim it could write a channel row.
	app.clinePassRepairMu.Lock()
	app.clinePassRepairKey = ""
	app.clinePassRepairAt = time.Time{}
	app.clinePassRepairRunning = false
	app.clinePassRepairMu.Unlock()
	app.noteClinePassRequestOutcome(rejectedClinePassCompletion(accountID, http.StatusUnauthorized, clinePassRejectedUpstreamError))
	// Any management request hands the plugin one, which is what arms the repair.
	app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/accounts", Headers: headers,
	})
	if _, ok := app.clinePassRepairCredentials(); !ok {
		t.Fatal("the management key was not remembered")
	}
	// The backoff coalesces a burst of rejected requests into one repair.
	if _, ok := app.clinePassRepairCredentials(); ok {
		t.Fatal("a second repair was claimed inside the backoff")
	}
	// A repair the page read already performed is visible: the row now carries a rotated token.
	entries, _ := store.snapshot()
	if len(entries) != 1 {
		t.Fatalf("the repair left %d rows for one account", len(entries))
	}
	if got := channelCredentialKey(t, entries[0]); got == "" {
		t.Fatal("the repaired row lost its credential")
	}
}

func TestClinePassRejectionIsNotAttributedWithoutEvidence(t *testing.T) {
	// Two accounts share the gateway, so a rejected request that names neither of them must not be
	// charged to either one.
	app, _, firstID, _ := publishedOAuthClinePassApp(t)
	// No management key, so a recorded rejection cannot spawn a background repair inside the test.
	app.clinePassRepairMu.Lock()
	app.clinePassRepairKey = ""
	app.clinePassRepairMu.Unlock()
	secondID, errSave := app.clinePass.SaveAPIKeyAccount("", "second", "", "sk-second-secret")
	if errSave != nil {
		t.Fatalf("SaveAPIKeyAccount() error = %v", errSave)
	}
	completion := rejectedClinePassCompletion("", http.StatusUnauthorized, clinePassRejectedUpstreamError)
	completion.Metadata = map[string]any{}
	app.noteClinePassRequestOutcome(completion)
	if app.clinePass.AuthFailurePending(firstID) || app.clinePass.AuthFailurePending(secondID) {
		t.Fatal("an unattributable rejection was charged to a stored account")
	}
	// With exactly one stored account there is no ambiguity, so the same request is attributed.
	if errRemove := app.clinePass.RemoveAccount(secondID); errRemove != nil {
		t.Fatalf("RemoveAccount() error = %v", errRemove)
	}
	app.noteClinePassRequestOutcome(completion)
	if !app.clinePass.AuthFailurePending(firstID) {
		t.Fatal("the only stored account was not attributed the rejection")
	}
}

package manager

import (
	"context"
	"net/http"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// Cline Pass credential upkeep between page loads.
//
// Everything that republished a rotated token used to hang off a management request: a page
// read, a model test, a model-list reload. An operator who simply used the gateway therefore
// kept CPA routing through a token nobody renewed, and the first request after the token lapsed
// was answered with "no auth available ... re-authenticate your Cline account" until a page was
// opened. The upkeep loop closes that window, and the usage callback gives the plugin a second
// way to learn that a credential was refused.

// The loop rotates an expiring token and republishes the row on its own, without any management
// request in between; before it holds a management key it must not touch anything.
func TestClinePassMaintenanceRotatesAndRepublishesWithoutAPageRead(t *testing.T) {
	app, store, gateway, accountID, headers := newClinePassRotatingQuotaApp(t, []string{"cline-pass/glm-5.3"})

	// A plugin that has not seen a management request holds no key and must not touch anything:
	// every row write goes through CPA's management API.
	app.clinePassRepairMu.Lock()
	app.clinePassRepairKey = ""
	app.clinePassRepairMu.Unlock()
	if app.runClinePassMaintenance(context.Background()) {
		t.Fatal("the upkeep pass acted without a management key")
	}
	if _, _, _, refreshCalls, _, _ := gateway.counters(); refreshCalls != 0 {
		t.Fatalf("refresh calls = %d, want none before the pass can write the row", refreshCalls)
	}

	// The first management request hands the loop its key.
	app.rememberClinePassManagementKey(resolveManagementKey(headers))
	if !app.runClinePassMaintenance(context.Background()) {
		t.Fatal("the upkeep pass did nothing with a key and an expiring token")
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

	// A token that is comfortably valid needs no rotation, so a later pass writes nothing.
	app.clinePass.mu.Lock()
	for index := range app.clinePass.accounts {
		app.clinePass.accounts[index].ExpiresAt = time.Now().UTC().Add(time.Hour)
	}
	app.clinePass.mu.Unlock()
	if _, _, _, refreshCalls, _, _ := gateway.counters(); refreshCalls != 1 {
		t.Fatalf("refresh calls = %d before the settled pass", refreshCalls)
	}
	app.runClinePassMaintenance(context.Background())
	if _, _, _, refreshCalls, _, _ := gateway.counters(); refreshCalls != 1 {
		t.Fatalf("a settled pass rotated the token again: %d refresh calls", refreshCalls)
	}
}

// A refused credential reported on the usage callback is learned there, because that callback
// reaches the plugin even when the request-completion callback does not. This is what keeps a
// row CPA has already cooled down from staying in the routing pool.
func TestClinePassUsageFailureStartsTheRepair(t *testing.T) {
	app, _, _, ids, headers := newClinePassQuotaApp(t, 1, []string{"cline-pass/glm-5.3"})
	bindClinePassPublicationAccount(t, app, headers, ids[0])
	app.clinePass.SetRouteAuthIndexes(ids[0], []string{"index-a"})

	app.HandleUsage(cpaapi.UsageRecord{
		Provider: "openai-compatibility", AuthType: "api_key", AuthIndex: "index-a",
		Model: "cline-pass/glm-5.3", Failed: true,
		Failure: cpaapi.UsageFailure{
			StatusCode: http.StatusServiceUnavailable,
			Body:       `{"error":{"message":"auth_unavailable: no auth available (providers=openai-compatible-cline pass, model=deepseek-v4.1-flash; last upstream error: Unauthorized: Please make sure you're using the latest version of Cline and re-authenticate your Cline account.)"}}`,
		},
	})

	if !app.clinePass.AuthFailurePending(ids[0]) {
		t.Fatal("a refused credential reported on the usage callback was not recorded")
	}
	journaled := false
	for _, operation := range app.operations.List(OperationQuery{Page: 1, PageSize: operationPageSize}).Operations {
		if operation.ReasonCode == OperationFailureClinePassCredentialRejected {
			journaled = true
		}
	}
	if !journaled {
		t.Fatal("the refusal was not journalled")
	}
}

// An ordinary upstream failure on the usage callback is not a credential rejection: a 500 (or a
// transient body) must not rotate the account or mark it as needing a sign-in.
func TestClinePassUsageFailureIgnoresOrdinaryErrors(t *testing.T) {
	app, _, _, ids, headers := newClinePassQuotaApp(t, 1, []string{"cline-pass/glm-5.3"})
	bindClinePassPublicationAccount(t, app, headers, ids[0])
	app.clinePass.SetRouteAuthIndexes(ids[0], []string{"index-a"})

	for _, failure := range []cpaapi.UsageFailure{
		{StatusCode: http.StatusInternalServerError, Body: `{"error":{"message":"upstream exploded"}}`},
		{StatusCode: http.StatusTooManyRequests, Body: `{"error":{"message":"rate limit exceeded"}}`},
		{StatusCode: http.StatusBadGateway, Body: ""},
	} {
		app.HandleUsage(cpaapi.UsageRecord{
			Provider: "openai-compatibility", AuthType: "api_key", AuthIndex: "index-a",
			Model: "cline-pass/glm-5.3", Failed: true, Failure: failure,
		})
	}

	if app.clinePass.AuthFailurePending(ids[0]) {
		t.Fatal("an ordinary upstream failure was recorded as a credential rejection")
	}
}

// The upkeep loop is idempotent and shuts down cleanly, so a second management request never
// starts a second loop and shutdown never races a row write.
func TestClinePassMaintenanceLoopStartsOnceAndStops(t *testing.T) {
	app := NewApp(&fakeAuthHost{}, nil)
	app.startClinePassMaintenance()
	app.startClinePassMaintenance()
	app.clinePassRepairMu.Lock()
	first, firstDone := app.clinePassMaintenanceStop, app.clinePassMaintenanceDone
	app.clinePassRepairMu.Unlock()
	if first == nil || firstDone == nil {
		t.Fatal("the upkeep loop did not start")
	}
	app.stopClinePassMaintenance()
	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the upkeep loop did not stop")
	}
	// A loop that was stopped is never started again, and stopping twice is harmless.
	app.startClinePassMaintenance()
	app.clinePassRepairMu.Lock()
	restarted := app.clinePassMaintenanceStop
	app.clinePassRepairMu.Unlock()
	if restarted != nil {
		t.Fatal("a stopped instance started its upkeep loop again")
	}
	app.stopClinePassMaintenance()
}

package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// The gateway names its own reset window in the quota answer, in a compact form
// ("3d 13h"). The hold the plugin records has to follow that window: a hold that is too
// long keeps a recovered account idle, and one that is too short sends the next request
// into the same rejection.
func TestClinePassQuotaResetDelayReadsTheGatewayWindow(t *testing.T) {
	cases := []struct {
		text string
		want time.Duration
	}{
		{"Error 429: You have reached your weekly Clinepass limit. The limit resets in 3d 13h, please try again later.", 3*24*time.Hour + 13*time.Hour},
		{"rate limited, resets in 2 hours 30 minutes", 2*time.Hour + 30*time.Minute},
		{"resets in 45m", 45 * time.Minute},
		// A window shorter than one row write can act on, and one that is absurdly long,
		// are clamped instead of being trusted.
		{"resets in 20s", clinePassQuotaHoldMin},
		{"resets in 400d", clinePassQuotaHoldMax},
		// A status code or a token count must not be mistaken for a window.
		{"Error 429 from the gateway", 0},
		{"", 0},
	}
	for _, testCase := range cases {
		if got := clinePassQuotaResetDelay(testCase.text); got != testCase.want {
			t.Fatalf("clinePassQuotaResetDelay(%q) = %v, want %v", testCase.text, got, testCase.want)
		}
	}
	now := time.Date(2026, 9, 22, 18, 51, 0, 0, time.UTC)
	if got := clinePassQuotaResetHold("no window here", now); !got.Equal(now.Add(clinePassQuotaHoldDefault)) {
		t.Fatalf("a windowless answer held the account until %v, want the short default hold", got)
	}
	if got := clinePassQuotaResetHold("resets in 3d", now); !got.Equal(now.Add(72 * time.Hour)) {
		t.Fatalf("hold = %v, want the parsed window", got)
	}
}

// newClinePassQuotaApp binds one app to an in-memory channel store and a fake gateway, so a
// test can make the gateway answer a model test with its own quota message.
func newClinePassQuotaApp(t *testing.T, accounts int, models []string) (*App, *clinePassChannelStore, *clinePassFakeGateway, []string, http.Header) {
	t.Helper()
	service, gateway := newConfiguredClinePassService(t, t.TempDir())
	ids := make([]string, 0, accounts)
	for index := 0; index < accounts; index++ {
		accountID, errSave := service.SaveAPIKeyAccount("", "", "", "sk-quota-"+string(rune('a'+index)))
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
	return app, store, gateway, ids, http.Header{"Authorization": []string{"Bearer management-secret"}}
}

func runClinePassModelTest(t *testing.T, app *App, headers http.Header, accountID, model string) OpenCodeModelTestResult {
	t.Helper()
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/model-test", Headers: headers,
		Body: []byte(`{"account_id":"` + accountID + `","model":"` + model + `"}`),
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("model-test status = %d body=%s", response.StatusCode, response.Body)
	}
	var payload struct {
		Result OpenCodeModelTestResult `json:"result"`
	}
	if errDecode := json.Unmarshal(response.Body, &payload); errDecode != nil {
		t.Fatalf("decode model-test: %v", errDecode)
	}
	return payload.Result
}

func clinePassRowDisabled(t *testing.T, entry map[string]any) bool {
	t.Helper()
	disabled, _ := entry["disabled"].(bool)
	return disabled
}

// A gateway quota answer for one account must take that account out of routing and leave
// its siblings alone: with one row per account, the healthy subscription keeps serving the
// same models instead of the whole gateway failing.
func TestClinePassQuotaAnswerDisablesOnlyTheLimitedAccountsRow(t *testing.T) {
	app, store, gateway, ids, headers := newClinePassQuotaApp(t, 2, []string{"cline-pass/glm-5.3"})
	bindClinePassPublicationAccount(t, app, headers, ids[0])
	bindClinePassPublicationAccount(t, app, headers, ids[1])

	gateway.mu.Lock()
	gateway.chatStatus = http.StatusTooManyRequests
	gateway.chatBody = `{"error":{"code":"INFERENCE_CAP_ERROR","message":"Error 429: You have reached your weekly Clinepass limit. The limit resets in 3d 13h, please try again later."}}`
	gateway.mu.Unlock()

	result := runClinePassModelTest(t, app, headers, ids[0], "cline-pass/glm-5.3")
	if result.ReasonCode != "quota_limited" {
		t.Fatalf("probe reason = %q, want the gateway's quota classification", result.ReasonCode)
	}

	entries, writes := store.snapshot()
	if len(entries) != 2 {
		t.Fatalf("rows = %d, want one per account", len(entries))
	}
	for _, entry := range entries {
		key := channelCredentialKey(t, entry)
		if key == "sk-quota-a" && !clinePassRowDisabled(t, entry) {
			t.Fatalf("the limited account's row is still enabled: %#v", entry)
		}
		if key == "sk-quota-b" && clinePassRowDisabled(t, entry) {
			t.Fatalf("the sibling account's row was disabled: %#v", entry)
		}
	}

	// The account page reports the hold and the window the gateway named, and a further
	// read does not rewrite the row: the state is already what it must be.
	payload := getClinePassAccounts(t, app, headers)
	var limited ClinePassAccountView
	for _, account := range payload.Accounts {
		if account.ID == ids[0] {
			limited = account
		}
		if account.ID == ids[1] && account.QuotaLimited {
			t.Fatalf("the sibling account reports a quota hold: %#v", account)
		}
	}
	if !limited.QuotaLimited || limited.QuotaLimitedUntil == nil {
		t.Fatalf("the limited account does not report its hold: %#v", limited)
	}
	if remaining := time.Until(*limited.QuotaLimitedUntil); remaining < 3*24*time.Hour {
		t.Fatalf("the reported window = %v, want the 3d 13h the gateway named", remaining)
	}
	if _, writesAfter := store.snapshot(); writesAfter != writes {
		t.Fatalf("a second read rewrote the channel list: %d writes, want %d", writesAfter, writes)
	}
}

// A hold that has run out must put the account back in the routing pool: the row is enabled
// again and the record is dropped only once that write succeeded.
func TestClinePassQuotaHoldEnablesTheRowWhenTheWindowEnds(t *testing.T) {
	app, store, _, ids, headers := newClinePassQuotaApp(t, 2, []string{"cline-pass/glm-5.3"})
	bindClinePassPublicationAccount(t, app, headers, ids[0])
	bindClinePassPublicationAccount(t, app, headers, ids[1])
	if _, errMark := app.clinePass.MarkQuotaLimited(ids[0], time.Now().UTC().Add(time.Hour)); errMark != nil {
		t.Fatalf("MarkQuotaLimited() error = %v", errMark)
	}
	applyClinePassQuotaRowStatesForTest(t, app, headers)
	if !rowDisabledByCredential(t, store, "sk-quota-a") {
		t.Fatal("the held account's row was not disabled")
	}

	// The window the gateway named passes.
	if _, errMark := app.clinePass.MarkQuotaLimited(ids[0], time.Now().UTC().Add(-time.Second)); errMark != nil {
		t.Fatalf("MarkQuotaLimited() error = %v", errMark)
	}
	applyClinePassQuotaRowStatesForTest(t, app, headers)

	if rowDisabledByCredential(t, store, "sk-quota-a") {
		t.Fatal("the account stayed disabled after its window ended")
	}
	if !app.clinePass.QuotaLimitedUntil(ids[0]).IsZero() {
		t.Fatal("the released hold was not dropped once the row was enabled again")
	}
}

// A probe that answers proves the account serves traffic again, so a recorded hold is
// released and the row is enabled in the same pass.
func TestClinePassQuotaProbeReleasesTheHold(t *testing.T) {
	app, store, gateway, ids, headers := newClinePassQuotaApp(t, 1, []string{"cline-pass/glm-5.3"})
	bindClinePassPublicationAccount(t, app, headers, ids[0])
	if _, errMark := app.clinePass.MarkQuotaLimited(ids[0], time.Now().UTC().Add(3*time.Hour)); errMark != nil {
		t.Fatalf("MarkQuotaLimited() error = %v", errMark)
	}
	applyClinePassQuotaRowStatesForTest(t, app, headers)
	if !rowDisabledByCredential(t, store, "sk-quota-a") {
		t.Fatal("the held account's row was not disabled")
	}

	gateway.mu.Lock()
	gateway.chatStatus = http.StatusOK
	gateway.chatBody = ""
	gateway.mu.Unlock()

	if result := runClinePassModelTest(t, app, headers, ids[0], "cline-pass/glm-5.3"); result.Status != "available" {
		t.Fatalf("probe status = %q, want the gateway's answer", result.Status)
	}
	if rowDisabledByCredential(t, store, "sk-quota-a") {
		t.Fatal("a probe that answered left the account out of routing")
	}
	if !app.clinePass.QuotaLimitedUntil(ids[0]).IsZero() {
		t.Fatal("the released hold was not dropped")
	}
}

// The completion path learns the hold from the gateway's own answer, which is what keeps a
// long-running deployment correct without the operator pressing test.
func TestClinePassCompletionRecognisesTheQuotaAnswer(t *testing.T) {
	quotaAnswer := cpaapi.RequestCompletion{
		StatusCode: http.StatusTooManyRequests,
		Error:      "Error 429: You have reached your weekly Clinepass limit. The limit resets in 2h, please try again later.",
		Metadata:   map[string]any{"selected_auth_index": "index-a"},
	}
	if !clinePassCompletionQuotaLimited(quotaAnswer) {
		t.Fatal("the gateway's cap message was not recognised as a quota answer")
	}
	transient := cpaapi.RequestCompletion{StatusCode: http.StatusTooManyRequests, Error: "rate limit exceeded"}
	if clinePassCompletionQuotaLimited(transient) {
		t.Fatal("a transient rate limit was treated as an account-level cap")
	}
	authorization := cpaapi.RequestCompletion{StatusCode: http.StatusUnauthorized, Error: "unauthorized: re-authenticate"}
	if clinePassCompletionQuotaLimited(authorization) {
		t.Fatal("an authorization failure was treated as a quota answer")
	}
}

// applyClinePassQuotaRowStatesForTest runs the maintenance pass the way a page read does,
// through the management route that owns it.
func applyClinePassQuotaRowStatesForTest(t *testing.T, app *App, headers http.Header) {
	t.Helper()
	getClinePassAccounts(t, app, headers)
}

func rowDisabledByCredential(t *testing.T, store *clinePassChannelStore, credential string) bool {
	t.Helper()
	entries, _ := store.snapshot()
	for _, entry := range entries {
		if channelCredentialKey(t, entry) == credential {
			return clinePassRowDisabled(t, entry)
		}
	}
	t.Fatalf("no channel row carries %q", credential)
	return false
}

// The deployment this fix was reported from: one legacy Cline Pass row written by a release
// that had no per-account label, holding the first account's token, and a second account that
// was never renamed. One page read must leave BOTH accounts published - the first keeps the
// row it owns, the second gets its own - instead of the second account staying idle behind a
// row that only ever routes to the first.
func TestClinePassLegacySingleRowIsSplitPerAccount(t *testing.T) {
	app, store, _, _, headers := newClinePassQuotaApp(t, 2, []string{"cline-pass/glm-5.3"})
	store.setEntries([]map[string]any{{
		"base-url":        clinePassDefaultBaseURL,
		"name":            clinePassBoundChannelName,
		"api-key-entries": []any{map[string]any{"api-key": "sk-quota-a"}},
		"models":          []any{map[string]any{"name": "cline-pass/glm-5.3", "alias": "cline-pass/glm-5.3"}},
	}})

	for _, account := range getClinePassAccounts(t, app, headers).Accounts {
		if !account.ChannelBound {
			t.Fatalf("account %s stayed unbound on a shared legacy row: %#v", account.ID, account)
		}
	}
	entries, _ := store.snapshot()
	if len(entries) != 2 {
		t.Fatalf("rows = %d, want one row per account", len(entries))
	}
	keys := map[string]string{}
	for _, entry := range entries {
		keys[channelCredentialKey(t, entry)] = channelName(t, entry)
	}
	if keys["sk-quota-a"] == "" || keys["sk-quota-b"] == "" {
		t.Fatalf("an account lost its own row: %#v", keys)
	}
	if keys["sk-quota-a"] == keys["sk-quota-b"] {
		t.Fatalf("both accounts still publish one label: %#v", keys)
	}
	// The account whose credential the legacy row carries keeps it: the migration must not
	// take a working account's routing away while it publishes the sibling.
	if !strings.HasPrefix(keys["sk-quota-a"], clinePassBoundChannelName) {
		t.Fatalf("the legacy row was not renamed to its owner's label: %#v", keys)
	}
}

// A quota hold survives a restart: the store keeps the window, so a process that comes back
// while the account is still out of allowance does not route to it again.
func TestClinePassQuotaHoldSurvivesARestart(t *testing.T) {
	dataDir := t.TempDir()
	service, gateway := newConfiguredClinePassService(t, dataDir)
	accountID, errSave := service.SaveAPIKeyAccount("", "", "", "sk-quota-a")
	if errSave != nil {
		t.Fatalf("SaveAPIKeyAccount() error = %v", errSave)
	}
	window := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	if _, errMark := service.MarkQuotaLimited(accountID, window); errMark != nil {
		t.Fatalf("MarkQuotaLimited() error = %v", errMark)
	}

	restarted := NewClinePassService()
	restarted.Configure(Config{DataDir: dataDir})
	restarted.SetHTTPDoer(gateway)
	if got := restarted.QuotaLimitedUntil(accountID); !got.Equal(window) {
		t.Fatalf("the hold after a restart = %v, want %v", got, window)
	}
	if !restarted.QuotaLimited(accountID) {
		t.Fatal("a restarted service routed to an account whose window has not ended")
	}
}

// The request path records the hold from the gateway's own answer and asks the maintenance
// pass to take the account's row out of routing.
func TestClinePassCompletionOpensAQuotaHold(t *testing.T) {
	app, store, _, ids, headers := newClinePassQuotaApp(t, 1, []string{"cline-pass/glm-5.3"})
	bindClinePassPublicationAccount(t, app, headers, ids[0])
	// The completion callback attributes the answer through the auth index a bind recorded.
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/accounts", Headers: headers,
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("accounts status = %d body=%s", response.StatusCode, response.Body)
	}
	app.rememberClinePassManagementKey(resolveManagementKey(headers))

	app.HandleRequestComplete(cpaapi.RequestCompletion{
		RequestID: "quota-completion", Outcome: "failed", StatusCode: http.StatusTooManyRequests,
		Error: "Error 429: You have reached your weekly Clinepass limit. The limit resets in 2h, please try again later.",
		Model: "cline-pass/glm-5.3",
	})

	if !app.clinePass.QuotaLimited(ids[0]) {
		t.Fatal("the gateway's quota answer did not record a hold")
	}
	if writes := waitForClinePassRowDisabled(t, store, "sk-quota-a"); !writes {
		t.Fatal("the maintenance pass never took the limited account out of routing")
	}
}

// waitForClinePassRowDisabled waits for the background maintenance pass to disable the row.
func waitForClinePassRowDisabled(t *testing.T, store *clinePassChannelStore, credential string) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rowDisabledByCredential(t, store, credential) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// runClinePassModelTestResponse runs one model test and hands back both the probe result and
// the whole response body, so a test can assert on the quota outcome the dialog reads.
func runClinePassModelTestResponse(t *testing.T, app *App, headers http.Header, accountID, model string) (OpenCodeModelTestResult, map[string]any) {
	t.Helper()
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/model-test", Headers: headers,
		Body: []byte(`{"account_id":"` + accountID + `","model":"` + model + `"}`),
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("model-test status = %d body=%s", response.StatusCode, response.Body)
	}
	var payload map[string]any
	if errDecode := json.Unmarshal(response.Body, &payload); errDecode != nil {
		t.Fatalf("decode model-test: %v", errDecode)
	}
	encoded, errMarshal := json.Marshal(payload["result"])
	if errMarshal != nil {
		t.Fatalf("re-encode result: %v", errMarshal)
	}
	var result OpenCodeModelTestResult
	if errDecode := json.Unmarshal(encoded, &result); errDecode != nil {
		t.Fatalf("decode result: %v", errDecode)
	}
	return result, payload
}

// setClinePassRowDisabledByCredential edits the stored row of one credential directly, which is
// how a test acts as the operator who toggles the switch on the AI provider page.
func setClinePassRowDisabledByCredential(t *testing.T, store *clinePassChannelStore, credential string, disabled bool) {
	t.Helper()
	entries, _ := store.snapshot()
	found := false
	for _, entry := range entries {
		if channelCredentialKey(t, entry) != credential {
			continue
		}
		entry["disabled"] = disabled
		found = true
	}
	if !found {
		t.Fatalf("no channel row carries %q", credential)
	}
	store.setEntries(entries)
}

const clinePassQuotaWeeklyBody = `{"error":{"code":"INFERENCE_CAP_ERROR","message":"Error 429: You have reached your weekly Clinepass limit. The limit resets in 2d 22h, please try again later."}}`

// The gateway keeps refusing an account for the whole window, so its rejection repeats the same
// message. A repeated rejection changes nothing about the recorded window, and it must still
// take the account out of routing: the row may have been enabled again in between (by an
// operator, by a failed write, or by a lost state), and the record not changing says nothing
// about the row. This is the case that left a rate-limited subscription routable and, because
// both accounts of one gateway publish the same models, took the whole gateway down with it.
func TestClinePassRepeatedQuotaAnswerStillDisablesTheRow(t *testing.T) {
	app, store, gateway, ids, headers := newClinePassQuotaApp(t, 1, []string{"cline-pass/glm-5.3"})
	bindClinePassPublicationAccount(t, app, headers, ids[0])
	gateway.mu.Lock()
	gateway.chatStatus = http.StatusTooManyRequests
	gateway.chatBody = clinePassQuotaWeeklyBody
	gateway.mu.Unlock()

	if _, payload := runClinePassModelTestResponse(t, app, headers, ids[0], "cline-pass/glm-5.3"); payload["quota"] == nil {
		t.Fatalf("the model test reported no quota outcome: %#v", payload)
	}
	if !rowDisabledByCredential(t, store, "sk-quota-a") {
		t.Fatal("the first rejection did not take the account out of routing")
	}

	// The row is enabled again while the gateway still refuses the credential.
	setClinePassRowDisabledByCredential(t, store, "sk-quota-a", false)
	if _, errMark := app.clinePass.MarkQuotaLimited(ids[0], time.Now().UTC().Add(2*time.Hour)); errMark != nil {
		t.Fatalf("MarkQuotaLimited() error = %v", errMark)
	}

	result, payload := runClinePassModelTestResponse(t, app, headers, ids[0], "cline-pass/glm-5.3")
	if result.ReasonCode != "quota_limited" {
		t.Fatalf("probe reason = %q", result.ReasonCode)
	}
	if !rowDisabledByCredential(t, store, "sk-quota-a") {
		t.Fatal("a repeated rejection left the rate-limited account in the routing pool")
	}
	outcome, _ := payload["quota"].(map[string]any)
	if state, _ := outcome["row"].(string); state != providerChannelRowDisabled {
		t.Fatalf("the reported row state = %#v, want %q", outcome["row"], providerChannelRowDisabled)
	}
}

// A row the operator disabled by hand is not this plugin's to enable: a spent hold (or a hold
// whose record was lost) must leave that row exactly as the operator left it, and still clear
// the record so the pages stop reporting a hold that is over.
func TestClinePassSpentHoldNeverEnablesAnOperatorsRow(t *testing.T) {
	app, store, _, ids, headers := newClinePassQuotaApp(t, 1, []string{"cline-pass/glm-5.3"})
	bindClinePassPublicationAccount(t, app, headers, ids[0])
	// The operator turned the account off, and a hold of ours ran out at the same time.
	setClinePassRowDisabledByCredential(t, store, "sk-quota-a", true)
	if _, errMark := app.clinePass.MarkQuotaLimited(ids[0], time.Now().UTC().Add(-time.Minute)); errMark != nil {
		t.Fatalf("MarkQuotaLimited() error = %v", errMark)
	}

	getClinePassAccounts(t, app, headers)

	entries, _ := store.snapshot()
	if !clinePassRowDisabled(t, entries[0]) {
		t.Fatal("a spent hold enabled a row the operator had disabled")
	}
	if !app.clinePass.QuotaLimitedUntil(ids[0]).IsZero() {
		t.Fatal("the spent hold was not dropped")
	}
}

// A request CPA attributes to an account whose row this plugin took out of routing cannot have
// reached the gateway through that account's credential, so it must not clear the hold and put
// a rate-limited credential back in the pool.
func TestClinePassMisattributedSuccessDoesNotReleaseAQuotaHold(t *testing.T) {
	app, store, _, ids, headers := newClinePassQuotaApp(t, 2, []string{"cline-pass/glm-5.3"})
	bindClinePassPublicationAccount(t, app, headers, ids[0])
	bindClinePassPublicationAccount(t, app, headers, ids[1])
	if _, errMark := app.clinePass.MarkQuotaLimited(ids[1], time.Now().UTC().Add(2*time.Hour)); errMark != nil {
		t.Fatalf("MarkQuotaLimited() error = %v", errMark)
	}
	getClinePassAccounts(t, app, headers)
	if !rowDisabledByCredential(t, store, "sk-quota-b") {
		t.Fatal("the held account's row was not disabled")
	}
	app.clinePass.SetRouteAuthIndexes(ids[1], []string{"index-b"})

	app.HandleRequestComplete(cpaapi.RequestCompletion{
		RequestID: "misattributed", Outcome: "succeeded", StatusCode: http.StatusOK,
		Model: "cline-pass/glm-5.3", Metadata: map[string]any{"selected_auth_index": "index-b"},
	})

	if !app.clinePass.QuotaLimited(ids[1]) {
		t.Fatal("a success attributed to an unroutable account cleared its hold")
	}
	if !rowDisabledByCredential(t, store, "sk-quota-b") {
		t.Fatal("a success attributed to an unroutable account put its row back in the pool")
	}
}

// A probe that answers is the one signal that proves the credential serves traffic again, so it
// releases the hold and enables the row this plugin disabled.
func TestClinePassAnsweringProbeEnablesTheRowAgain(t *testing.T) {
	app, store, gateway, ids, headers := newClinePassQuotaApp(t, 1, []string{"cline-pass/glm-5.3"})
	bindClinePassPublicationAccount(t, app, headers, ids[0])
	gateway.mu.Lock()
	gateway.chatStatus = http.StatusTooManyRequests
	gateway.chatBody = clinePassQuotaWeeklyBody
	gateway.mu.Unlock()
	runClinePassModelTestResponse(t, app, headers, ids[0], "cline-pass/glm-5.3")
	if !rowDisabledByCredential(t, store, "sk-quota-a") {
		t.Fatal("the limited account's row was not disabled")
	}

	gateway.mu.Lock()
	gateway.chatStatus = http.StatusOK
	gateway.chatBody = ""
	gateway.mu.Unlock()
	if result := runClinePassModelTest(t, app, headers, ids[0], "cline-pass/glm-5.3"); result.Status != "available" {
		t.Fatalf("probe status = %q", result.Status)
	}

	if rowDisabledByCredential(t, store, "sk-quota-a") {
		t.Fatal("an answering probe left the account out of routing")
	}
	if !app.clinePass.QuotaLimitedUntil(ids[0]).IsZero() || app.clinePass.QuotaRowDisabled(ids[0]) {
		t.Fatalf("the released hold left state behind: until=%v rowDisabled=%v", app.clinePass.QuotaLimitedUntil(ids[0]), app.clinePass.QuotaRowDisabled(ids[0]))
	}
}

package manager

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// clinePassQuotaRecord builds the usage callback CPA sends for traffic routed
// through a Cline Pass channel: the channel credential, the model the client
// called and the token breakdown of one request.
func clinePassQuotaRecord(token, model string, at time.Time, detail cpaapi.UsageDetail) cpaapi.UsageRecord {
	return cpaapi.UsageRecord{
		Provider:    "openai",
		AuthType:    "apikey",
		APIKey:      token,
		Model:       model,
		RequestedAt: at,
		Detail:      detail,
	}
}

// The documented windows are measured with an injectable clock. The fixed instant
// is Thursday 2026-03-12 12:00 UTC, so the calendar week starts Monday 2026-03-09
// and the calendar month starts 2026-03-01; the rolling window starts five hours
// before the instant, at 07:00.
//
// Events: 1M tokens one hour ago (all three windows), 1M tokens exactly at the
// rolling boundary 07:00 (all three), 2M tokens at 06:00 (week and month only),
// 4M tokens on Sunday 2026-03-08 23:00 (month only) and 8M tokens on
// 2026-02-27 (no window, and pruned). MiMo-V2.5 is 0.14 USD per 1M input tokens,
// so the windows report 0.28 / 0.56 / 1.12 USD.
func TestClinePassQuotaWindowsBucketByBoundary(t *testing.T) {
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	accountID, errSave := service.SaveAPIKeyAccount("", "windows", "", "sk-window-secret")
	if errSave != nil {
		t.Fatalf("SaveAPIKeyAccount() error = %v", errSave)
	}

	events := []struct {
		at     time.Time
		tokens int64
	}{
		{at: now.Add(-time.Hour), tokens: 1_000_000},
		{at: now.Add(-clinePassRollingWindow), tokens: 1_000_000},
		{at: now.Add(-6 * time.Hour), tokens: 2_000_000},
		{at: time.Date(2026, 3, 8, 23, 0, 0, 0, time.UTC), tokens: 4_000_000},
		{at: time.Date(2026, 2, 27, 23, 0, 0, 0, time.UTC), tokens: 8_000_000},
	}
	for _, event := range events {
		service.ObserveUsage(clinePassQuotaRecord("sk-window-secret", "cline-pass/mimo-v2.5", event.at, cpaapi.UsageDetail{InputTokens: event.tokens}))
	}

	view, ok := service.AccountView(accountID)
	if !ok {
		t.Fatalf("account %q is missing", accountID)
	}
	usage := view.QuotaUsage
	if !usage.Reference || usage.MonthlySubscriptionUSD != 9.99 {
		t.Fatalf("quota block = %+v, want reference-priced usage with the documented subscription", usage)
	}
	checks := []struct {
		name     string
		window   ClinePassQuotaWindowUsage
		tokens   int64
		requests int64
		usd      float64
	}{
		{name: clinePassQuotaWindowFiveHour, window: usage.FiveHour, tokens: 2_000_000, requests: 2, usd: 0.28},
		{name: clinePassQuotaWindowWeekly, window: usage.Weekly, tokens: 4_000_000, requests: 3, usd: 0.56},
		{name: clinePassQuotaWindowMonthly, window: usage.Monthly, tokens: 8_000_000, requests: 4, usd: 1.12},
	}
	for _, check := range checks {
		if check.window.InputTokens != check.tokens || check.window.Requests != check.requests {
			t.Fatalf("%s window = %+v, want %d tokens over %d requests", check.name, check.window, check.tokens, check.requests)
		}
		if math.Abs(check.window.USD-check.usd) > 1e-6 || check.window.UnpricedRequests != 0 {
			t.Fatalf("%s window = %+v, want %v USD", check.name, check.window, check.usd)
		}
		if check.window.OutputTokens != 0 || check.window.CacheReadTokens != 0 || check.window.CacheWriteTokens != 0 {
			t.Fatalf("%s window invented token counts: %+v", check.name, check.window)
		}
	}
}

// A request that is priced and a request of an unpriced free model are both
// counted, but only the priced one adds reference value. A record that is not
// Cline Pass traffic is ignored entirely.
func TestClinePassQuotaUsageSeparatesPricedAndUnpricedTraffic(t *testing.T) {
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	accountID, errSave := service.SaveAPIKeyAccount("", "mixed", "", "sk-mixed-secret")
	if errSave != nil {
		t.Fatalf("SaveAPIKeyAccount() error = %v", errSave)
	}

	// A documented model with a cached read and a cache write, plus an unpriced
	// free model of the same account.
	service.ObserveUsage(clinePassQuotaRecord("sk-mixed-secret", "cline-pass/qwen3.7-max", now.Add(-time.Minute), cpaapi.UsageDetail{
		InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 100_000, CacheCreationTokens: 100_000,
	}))
	service.ObserveUsage(clinePassQuotaRecord("sk-mixed-secret", "cline-free/longcat-2.0", now.Add(-time.Minute), cpaapi.UsageDetail{InputTokens: 500_000}))
	// Traffic of another provider that happens to be an API-key channel is not
	// Cline Pass traffic: the credential matches no stored account and the model
	// is not published for Cline Pass.
	service.ObserveUsage(clinePassQuotaRecord("sk-other-secret", "gpt-5.3-codex", now.Add(-time.Minute), cpaapi.UsageDetail{InputTokens: 9_000_000}))

	view, _ := service.AccountView(accountID)
	window := view.QuotaUsage.Monthly
	if window.Requests != 2 || window.UnpricedRequests != 1 {
		t.Fatalf("window = %+v, want two requests of which one is unpriced", window)
	}
	// Uncached input is 1M - 100K cache read - 100K cache write = 800K, so the
	// priced request is 0.8*2.50 + 1.0*7.50 + 0.1*0.50 + 0.1*3.125 = 9.8625 USD.
	// The unpriced request still adds its 500K input tokens without adding value.
	if window.InputTokens != 1_300_000 || window.CacheReadTokens != 100_000 || window.CacheWriteTokens != 100_000 || window.OutputTokens != 1_000_000 {
		t.Fatalf("window tokens = %+v", window)
	}
	if math.Abs(window.USD-9.8625) > 1e-6 {
		t.Fatalf("window USD = %v, want 9.8625", window.USD)
	}
}

// A record observed before the account row exists is kept under the bound
// channel's credential identity, which is the fallback identity, and shows up on
// the account as soon as it is stored.
func TestClinePassQuotaUsageFallsBackToChannelCredentialIdentity(t *testing.T) {
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	service.ObserveUsage(clinePassQuotaRecord("sk-late-secret", "cline-pass/glm-5.2", now.Add(-time.Minute), cpaapi.UsageDetail{InputTokens: 1_000_000}))
	if got := service.usage.usage(now, clinePassChannelCredentialIdentity("sk-late-secret")); got.Monthly.Requests != 1 {
		t.Fatalf("the fallback identity recorded nothing: %+v", got)
	}

	accountID, errSave := service.SaveAPIKeyAccount("", "late", "", "sk-late-secret")
	if errSave != nil {
		t.Fatalf("SaveAPIKeyAccount() error = %v", errSave)
	}
	view, _ := service.AccountView(accountID)
	if view.QuotaUsage.Monthly.InputTokens != 1_000_000 || view.QuotaUsage.Monthly.Requests != 1 {
		t.Fatalf("the stored account did not adopt the channel identity usage: %+v", view.QuotaUsage.Monthly)
	}
	if math.Abs(view.QuotaUsage.Monthly.USD-1.40) > 1e-6 {
		t.Fatalf("adopted USD = %v, want 1.40", view.QuotaUsage.Monthly.USD)
	}
}

// The accounts payload carries the additive quota block with the three documented
// windows, the documented subscription and no credential material.
func TestClinePassAccountsPayloadCarriesReferenceQuotaUsage(t *testing.T) {
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	if _, errSave := service.SaveAPIKeyAccount("", "quota payload", "", "sk-payload-secret"); errSave != nil {
		t.Fatalf("SaveAPIKeyAccount() error = %v", errSave)
	}
	store := &clinePassChannelStore{}
	app := NewApp(&fakeAuthHost{}, nil)
	app.clinePass = service
	app.managementDoer = store.doer()
	headers := http.Header{"Authorization": []string{"Bearer management-secret"}}

	// Drive the existing CPA usage callback the host calls, so the wiring itself
	// is under test rather than the ledger alone.
	app.HandleUsage(clinePassQuotaRecord("sk-payload-secret", "cline-pass/glm-5.3", now.Add(-time.Hour), cpaapi.UsageDetail{
		InputTokens: 1_000_000, OutputTokens: 500_000, CacheReadTokens: 200_000,
	}))

	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/accounts", Headers: headers,
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("accounts status = %d body=%s", response.StatusCode, response.Body)
	}
	if strings.Contains(string(response.Body), "sk-payload-secret") {
		t.Fatal("the accounts payload leaked the credential")
	}
	var payload struct {
		Accounts []struct {
			QuotaUsage map[string]any `json:"quota_usage"`
		} `json:"accounts"`
	}
	if errDecode := json.Unmarshal(response.Body, &payload); errDecode != nil || len(payload.Accounts) != 1 {
		t.Fatalf("decode accounts payload: %v %s", errDecode, response.Body)
	}
	quota := payload.Accounts[0].QuotaUsage
	for _, key := range []string{"five_hour", "weekly", "monthly"} {
		window, ok := quota[key].(map[string]any)
		if !ok {
			t.Fatalf("quota_usage[%q] = %#v, want the three documented windows", key, quota[key])
		}
		for _, field := range []string{"usd", "input_tokens", "output_tokens"} {
			if _, ok := window[field]; !ok {
				t.Fatalf("quota_usage[%q] is missing %q: %#v", key, field, window)
			}
		}
	}
	if quota["monthly_subscription_usd"] != 9.99 {
		t.Fatalf("monthly_subscription_usd = %#v, want 9.99", quota["monthly_subscription_usd"])
	}
	if quota["reference"] != true {
		t.Fatalf("reference = %#v, want true", quota["reference"])
	}
	// GLM-5.3 with 800K uncached input, 500K output and 200K cache reads:
	// 0.8*1.40 + 0.5*4.40 + 0.2*0.26 = 3.372 USD.
	monthly, _ := quota["monthly"].(map[string]any)
	if usd, ok := monthly["usd"].(float64); !ok || math.Abs(usd-3.372) > 1e-6 {
		t.Fatalf("monthly usd = %#v, want 3.372", monthly["usd"])
	}
	if input, ok := monthly["input_tokens"].(float64); !ok || input != 800_000 {
		t.Fatalf("monthly input_tokens = %#v, want the uncached input", monthly["input_tokens"])
	}
}

// The models payload carries the reference price of each documented model and no
// price field at all for a model the documentation does not price.
func TestClinePassModelsPayloadCarriesReferencePrices(t *testing.T) {
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	store := &clinePassChannelStore{}
	app := NewApp(&fakeAuthHost{}, nil)
	app.clinePass = service
	app.managementDoer = store.doer()
	headers := http.Header{"Authorization": []string{"Bearer management-secret"}}

	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: "/v0/management" + managementRoutePrefix + "/opencode/cline-pass/models", Headers: headers,
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("model page status = %d body=%s", response.StatusCode, response.Body)
	}
	var payload struct {
		Models []map[string]any `json:"models"`
	}
	if errDecode := json.Unmarshal(response.Body, &payload); errDecode != nil {
		t.Fatalf("decode model page: %v", errDecode)
	}
	rows := map[string]map[string]any{}
	for _, row := range payload.Models {
		id, _ := row["id"].(string)
		rows[id] = row
	}

	priced, ok := rows["cline-pass/qwen3.7-max"]
	if !ok {
		t.Fatalf("qwen3.7-max is missing from the page: %#v", rows)
	}
	if priced["priced"] != true || priced["input_usd_per_million"] != 2.5 || priced["output_usd_per_million"] != 7.5 ||
		priced["cache_read_usd_per_million"] != 0.5 || priced["cache_write_usd_per_million"] != 3.125 {
		t.Fatalf("qwen3.7-max row = %#v", priced)
	}

	// The documentation publishes no cache-write rate for most models, so the
	// field is absent rather than zero.
	noCacheWrite, ok := rows["cline-pass/kimi-k3"]
	if !ok {
		t.Fatalf("kimi-k3 is missing from the page: %#v", rows)
	}
	if noCacheWrite["priced"] != true || noCacheWrite["input_usd_per_million"] != 3.0 {
		t.Fatalf("kimi-k3 row = %#v", noCacheWrite)
	}
	if _, exists := noCacheWrite["cache_write_usd_per_million"]; exists {
		t.Fatalf("kimi-k3 published an undocumented cache-write rate: %#v", noCacheWrite)
	}

	// An unpriced free model reports priced=false and no price field at all.
	free, ok := rows["cline-free/longcat-2.0"]
	if !ok {
		t.Fatalf("longcat-2.0 is missing from the page: %#v", rows)
	}
	if free["priced"] != false {
		t.Fatalf("free row = %#v", free)
	}
	for _, field := range []string{"input_usd_per_million", "output_usd_per_million", "cache_read_usd_per_million", "cache_write_usd_per_million"} {
		if _, exists := free[field]; exists {
			t.Fatalf("unpriced row published %q: %#v", field, free)
		}
	}
}

// recordedClinePassRouteIndexes reports the auth indexes one account currently
// claims, as the matcher sees them.
func recordedClinePassRouteIndexes(service *ClinePassService, accountID string) []string {
	service.mu.RLock()
	defer service.mu.RUnlock()
	indexes := make([]string, 0, 2)
	for index, owner := range service.routeAuthIndexes {
		if owner == accountID {
			indexes = append(indexes, index)
		}
	}
	sort.Strings(indexes)
	return indexes
}

// A usage callback that carries only the auth index CPA assigned to the account's
// own channel row is attributed to that account: every documented window reports
// the tokens, the request and the reference-priced USD. The record names the
// provider string CPA reports live, which is not the one this plugin computes for
// the channel kind, and carries no credential at all.
func TestClinePassQuotaUsageAttributesByChannelAuthIndex(t *testing.T) {
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	accountID, errSave := service.SaveAPIKeyAccount("", "auth index", "", "sk-auth-index-secret")
	if errSave != nil {
		t.Fatalf("SaveAPIKeyAccount() error = %v", errSave)
	}
	// Empty values and duplicates are ignored, and the recorded value is trimmed.
	service.SetRouteAuthIndexes(accountID, []string{" b0b53977f3d9925b ", "b0b53977f3d9925b", ""})

	service.ObserveUsage(cpaapi.UsageRecord{
		Provider:    "openai-compatible-cline pass",
		AuthType:    "apikey",
		AuthIndex:   "b0b53977f3d9925b",
		Model:       "glm-5.3",
		RequestedAt: now.Add(-time.Minute),
		Detail:      cpaapi.UsageDetail{InputTokens: 1_000_000, OutputTokens: 500_000},
	})

	view, ok := service.AccountView(accountID)
	if !ok {
		t.Fatalf("account %q is missing", accountID)
	}
	// glm-5.3 with 1M uncached input and 500K output: 1*1.40 + 0.5*4.40 = 3.60 USD.
	windows := map[string]ClinePassQuotaWindowUsage{
		"five_hour": view.QuotaUsage.FiveHour,
		"weekly":    view.QuotaUsage.Weekly,
		"monthly":   view.QuotaUsage.Monthly,
	}
	for name, window := range windows {
		if window.InputTokens != 1_000_000 || window.OutputTokens != 500_000 || window.Requests != 1 {
			t.Fatalf("%s window = %+v, want the record's tokens over one request", name, window)
		}
		if math.Abs(window.USD-3.60) > 1e-6 || window.UnpricedRequests != 0 {
			t.Fatalf("%s window USD = %v, want 3.60", name, window.USD)
		}
	}
}

// The other identity a callback can carry is the host's auth id: it names the
// account by its id or by the credential identity this plugin computes.
func TestClinePassQuotaUsageAttributesByAuthID(t *testing.T) {
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	accountID, errSave := service.SaveAPIKeyAccount("", "auth id", "", "sk-auth-id-secret")
	if errSave != nil {
		t.Fatalf("SaveAPIKeyAccount() error = %v", errSave)
	}

	byID := clinePassQuotaRecord("", "cline-pass/glm-5.2", now.Add(-time.Minute), cpaapi.UsageDetail{InputTokens: 1_000_000})
	byID.Provider = "openai-compatible-cline pass"
	byID.AuthID = accountID
	service.ObserveUsage(byID)

	byIdentity := clinePassQuotaRecord("", "cline-pass/glm-5.2", now.Add(-time.Minute), cpaapi.UsageDetail{InputTokens: 1_000_000})
	byIdentity.Provider = "openai-compatible-cline pass"
	byIdentity.AuthID = clinePassChannelCredentialIdentity("sk-auth-id-secret")
	service.ObserveUsage(byIdentity)

	view, _ := service.AccountView(accountID)
	monthly := view.QuotaUsage.Monthly
	if monthly.InputTokens != 2_000_000 || monthly.Requests != 2 {
		t.Fatalf("the auth id did not attribute both records: %+v", monthly)
	}
	if math.Abs(monthly.USD-2.80) > 1e-6 {
		t.Fatalf("monthly USD = %v, want 2.80", monthly.USD)
	}
}

// Two accounts on the same gateway carry different auth indexes, which is what
// tells their traffic apart: each record lands on the account whose channel row
// it names, and the per-account channel rows are the reason the index is recorded
// per account rather than per gateway.
func TestClinePassQuotaUsageSeparatesAccountsByAuthIndex(t *testing.T) {
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	// The accounts are saved before the clock is frozen: an account id is derived
	// from the wall clock, so two accounts created at the same instant would share
	// one id.
	firstID, errFirst := service.SaveAPIKeyAccount("", "first", "", "sk-first-secret")
	if errFirst != nil {
		t.Fatalf("SaveAPIKeyAccount(first) error = %v", errFirst)
	}
	secondID, errSecond := service.SaveAPIKeyAccount("", "second", "", "sk-second-secret")
	if errSecond != nil {
		t.Fatalf("SaveAPIKeyAccount(second) error = %v", errSecond)
	}
	service.now = func() time.Time { return now }
	if firstID == secondID {
		t.Fatal("the two accounts must be distinct")
	}
	service.SetRouteAuthIndexes(firstID, []string{"index-first"})
	service.SetRouteAuthIndexes(secondID, []string{"index-second"})

	service.ObserveUsage(cpaapi.UsageRecord{
		Provider: "openai-compatible-cline pass", AuthType: "apikey", AuthIndex: "index-first",
		Model: "cline-pass/glm-5.3", RequestedAt: now.Add(-time.Minute), Detail: cpaapi.UsageDetail{InputTokens: 1_000_000},
	})
	service.ObserveUsage(cpaapi.UsageRecord{
		Provider: "openai-compatible-cline pass", AuthType: "apikey", AuthIndex: "index-second",
		Model: "cline-pass/glm-5.2", RequestedAt: now.Add(-time.Minute), Detail: cpaapi.UsageDetail{InputTokens: 2_000_000},
	})

	first, _ := service.AccountView(firstID)
	if first.QuotaUsage.Monthly.InputTokens != 1_000_000 || first.QuotaUsage.Monthly.Requests != 1 {
		t.Fatalf("first account took the wrong traffic: %+v", first.QuotaUsage.Monthly)
	}
	if math.Abs(first.QuotaUsage.Monthly.USD-1.40) > 1e-6 {
		t.Fatalf("first account USD = %v, want 1.40", first.QuotaUsage.Monthly.USD)
	}
	second, _ := service.AccountView(secondID)
	if second.QuotaUsage.Monthly.InputTokens != 2_000_000 || second.QuotaUsage.Monthly.Requests != 1 {
		t.Fatalf("second account took the wrong traffic: %+v", second.QuotaUsage.Monthly)
	}
	if math.Abs(second.QuotaUsage.Monthly.USD-2.80) > 1e-6 {
		t.Fatalf("second account USD = %v, want 2.80", second.QuotaUsage.Monthly.USD)
	}
}

// An auth index no stored account claims is not attributed to an arbitrary
// account. Two accounts are stored, so the last-resort identity keeps the event
// where the callback reported it instead of mixing it into one of them, and a
// record without any credential is dropped entirely.
func TestClinePassQuotaUsageIgnoresUnknownAuthIndex(t *testing.T) {
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	firstID, errFirst := service.SaveAPIKeyAccount("", "first", "", "sk-first-secret")
	if errFirst != nil {
		t.Fatalf("SaveAPIKeyAccount(first) error = %v", errFirst)
	}
	secondID, errSecond := service.SaveAPIKeyAccount("", "second", "", "sk-second-secret")
	if errSecond != nil {
		t.Fatalf("SaveAPIKeyAccount(second) error = %v", errSecond)
	}
	service.now = func() time.Time { return now }
	if firstID == secondID {
		t.Fatal("the two accounts must be distinct")
	}
	service.SetRouteAuthIndexes(firstID, []string{"index-first"})
	service.SetRouteAuthIndexes(secondID, []string{"index-second"})

	foreign := cpaapi.UsageRecord{
		Provider: "openai-compatible-cline pass", AuthType: "apikey", APIKey: "sk-foreign-secret",
		AuthIndex: "index-foreign", Model: "glm-5.3",
		RequestedAt: now.Add(-time.Minute), Detail: cpaapi.UsageDetail{InputTokens: 1_000_000},
	}
	service.ObserveUsage(foreign)
	// The same unknown index without any credential cannot be attributed either.
	service.ObserveUsage(cpaapi.UsageRecord{
		Provider: "openai-compatible-cline pass", AuthType: "apikey",
		AuthIndex: "index-foreign", Model: "glm-5.3",
		RequestedAt: now.Add(-time.Minute), Detail: cpaapi.UsageDetail{InputTokens: 4_000_000},
	})

	for name, id := range map[string]string{"first": firstID, "second": secondID} {
		view, _ := service.AccountView(id)
		if view.QuotaUsage.Monthly.Requests != 0 || view.QuotaUsage.Monthly.InputTokens != 0 {
			t.Fatalf("%s account took a record of a foreign auth index: %+v", name, view.QuotaUsage.Monthly)
		}
		if view.QuotaUsage.Weekly.Requests != 0 || view.QuotaUsage.FiveHour.Requests != 0 {
			t.Fatalf("%s account took a foreign record in another window: %+v", name, view.QuotaUsage)
		}
	}
	// Nothing is lost either: the credentialled record stays under the identity the
	// callback reported.
	kept := service.usage.usage(now, runtimeCredentialIdentity(foreign))
	if kept.Monthly.Requests != 1 || kept.Monthly.InputTokens != 1_000_000 {
		t.Fatalf("the foreign record was not kept under its own identity: %+v", kept.Monthly)
	}
}

// A record two stored accounts claim is dropped rather than counted twice.
func TestClinePassQuotaUsageDropsRecordTwoAccountsClaim(t *testing.T) {
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	firstID, errFirst := service.SaveAPIKeyAccount("", "shared one", "", "sk-shared-secret")
	if errFirst != nil {
		t.Fatalf("SaveAPIKeyAccount(first) error = %v", errFirst)
	}
	secondID, errSecond := service.SaveAPIKeyAccount("", "shared two", "", "sk-shared-secret")
	if errSecond != nil {
		t.Fatalf("SaveAPIKeyAccount(second) error = %v", errSecond)
	}
	service.now = func() time.Time { return now }
	if firstID == secondID {
		t.Fatal("the two accounts must be distinct")
	}

	service.ObserveUsage(clinePassQuotaRecord("sk-shared-secret", "cline-pass/glm-5.3", now.Add(-time.Minute), cpaapi.UsageDetail{InputTokens: 1_000_000}))

	for name, id := range map[string]string{"first": firstID, "second": secondID} {
		view, _ := service.AccountView(id)
		if view.QuotaUsage.Monthly.Requests != 0 {
			t.Fatalf("%s account counted an ambiguous record: %+v", name, view.QuotaUsage.Monthly)
		}
	}
	if kept := service.usage.usage(now, clinePassChannelCredentialIdentity("sk-shared-secret")); kept.Monthly.Requests != 0 {
		t.Fatalf("the ambiguous record was stashed under the credential identity: %+v", kept.Monthly)
	}
}

// The client-facing stripped alias of a catalog model counts as a published
// Cline Pass model, which is what lets the last-resort match recognise traffic
// named by the id a client actually calls.
func TestClinePassPublishedModelAcceptsClientAlias(t *testing.T) {
	for _, model := range []string{"cline-pass/deepseek-v4.1-flash", "deepseek-v4.1-flash", "cline-pass/glm-5.3", "glm-5.3"} {
		if !clinePassIsPublishedModel(model) {
			t.Fatalf("model %q must count as published", model)
		}
	}
	for _, model := range []string{"gpt-5.3-codex", ""} {
		if clinePassIsPublishedModel(model) {
			t.Fatalf("model %q must not count as published", model)
		}
	}
}

// The reported defect: CPA's callback for the channel names only the auth index CPA assigned,
// so the Cline Pass ledger resolved no credential and attributed nothing - the documented windows
// stayed at $0 while the traffic kept arriving. The record's credential is now resolved before any
// consumer reads it, which is what this pins.
func TestClinePassQuotaUsageAttributesAnAuthIndexOnlyCallback(t *testing.T) {
	const token = "sk-ledger-channel-secret"
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	accountID, errSave := service.SaveAPIKeyAccount("", "ledger", "", token)
	if errSave != nil {
		t.Fatalf("SaveAPIKeyAccount() error = %v", errSave)
	}

	app := NewApp(&fakeAuthHost{}, nil)
	app.Configure([]byte("data_dir: " + t.TempDir()))
	t.Cleanup(app.Close)
	app.clinePass = service
	app.managementDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		kind := strings.TrimPrefix(request.URL.Path, "/v0/management/")
		var list []map[string]any
		if kind == "openai-compatibility" {
			list = []map[string]any{{
				"name":            "Cline Pass",
				"base-url":        clinePassDefaultBaseURL,
				"api-key-entries": []any{map[string]any{"api-key": token, "auth-index": "row-index-a"}},
			}}
		}
		payload, errEncode := json.Marshal(map[string]any{kind: list})
		if errEncode != nil {
			return nil, errEncode
		}
		return jsonHTTPResponse(http.StatusOK, string(payload)), nil
	})
	// One channel read is what teaches the plugin which credential the auth index belongs to.
	if _, storageErr := app.resolveAIProviderChannelNames(context.Background(), "management-secret"); storageErr != "" {
		t.Fatalf("channel read failed: %s", storageErr)
	}

	// The callback carries the auth index and nothing else: no API key, no provider name.
	app.HandleUsage(cpaapi.UsageRecord{
		AuthType:    "api_key",
		AuthIndex:   "row-index-a",
		Model:       "cline-pass/glm-5.3",
		RequestedAt: now.Add(-time.Minute),
		Detail:      cpaapi.UsageDetail{InputTokens: 1_000_000, OutputTokens: 0},
	})

	view, ok := service.AccountView(accountID)
	if !ok {
		t.Fatal("the stored account disappeared")
	}
	monthly := view.QuotaUsage.Monthly
	if monthly.Requests != 1 || monthly.InputTokens != 1_000_000 {
		t.Fatalf("an auth-index-only callback was not attributed: %+v", monthly)
	}
	if math.Abs(monthly.USD-1.40) > 1e-6 {
		t.Fatalf("reference-priced USD = %v, want 1.40 for one GLM-5.3 input million", monthly.USD)
	}
}

package manager

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
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

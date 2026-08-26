package manager

import (
	"context"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

type creditPricingRoundTripper func(*http.Request) (*http.Response, error)

func (f creditPricingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func creditPricingResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    request,
	}
}

func TestEmbeddedCreditPricingTableCalculatesCachedAndUncachedTokens(t *testing.T) {
	service := NewSub2APICreditUsage()
	defer service.Close()
	service.SetEnabled(true)

	charge := service.Calculate(cpaapi.UsageRecord{
		Model: "gpt-5.4",
		Detail: cpaapi.UsageDetail{
			InputTokens:         1000,
			OutputTokens:        100,
			ReasoningTokens:     40,
			CacheReadTokens:     200,
			CacheCreationTokens: 100,
		},
	})
	want := 700*0.0000025 + 200*0.00000025 + 100*0 + 100*0.000015
	if !charge.Enabled || !charge.Rated || math.Abs(float64(charge.AmountNanos)/creditNanosPerUSD-want) > 1e-9 {
		t.Fatalf("charge = %#v, want USD %.9f", charge, want)
	}
}

func TestEmbeddedCreditPricingPreservesTinyLunaChargesAtNanoUSDPrecision(t *testing.T) {
	service := NewSub2APICreditUsage()
	defer service.Close()
	service.SetEnabled(true)

	charge := service.Calculate(cpaapi.UsageRecord{
		Model:  "gpt-5.6-luna",
		Detail: cpaapi.UsageDetail{InputTokens: 1, CacheReadTokens: 1, TotalTokens: 1},
	})
	// One cached Luna input token costs USD 0.00000002 in the current
	// upstream rate card. The assertion still proves sub-cent nano-USD
	// precision survives rounding.
	wantNanos := int64(20)
	if !charge.Enabled || !charge.Rated || charge.AmountNanos != wantNanos {
		t.Fatalf("tiny Luna charge = %#v, want %d nano-USD", charge, wantNanos)
	}
}

func TestCreditPricingAppliesServiceTierAndLongContextRules(t *testing.T) {
	table, err := parseCreditPricingTable([]byte(`{
		"priced-model": {
			"input_cost_per_token": 0.000001,
			"output_cost_per_token": 0.000002,
			"input_cost_per_token_priority": 0.000003,
			"output_cost_per_token_priority": 0.000004,
			"long_context_input_token_threshold": 10,
			"long_context_input_cost_multiplier": 2,
			"long_context_output_cost_multiplier": 1.5
		},
		"fallback-tier-model": {
			"input_cost_per_token": 0.000001,
			"output_cost_per_token": 0.000002
		}
	}`), time.Now(), "test")
	if err != nil {
		t.Fatalf("parseCreditPricingTable() error = %v", err)
	}
	service := NewSub2APICreditUsage()
	defer service.Close()
	service.table.Store(table)
	service.SetEnabled(true)

	priority := service.Calculate(cpaapi.UsageRecord{
		Model: "priced-model", ServiceTier: "priority",
		Detail: cpaapi.UsageDetail{InputTokens: 20, OutputTokens: 10},
	})
	wantPriority := 20*0.000003*2 + 10*0.000004*1.5
	if got := float64(priority.AmountNanos) / creditNanosPerUSD; math.Abs(got-wantPriority) > 1e-9 {
		t.Fatalf("priority USD = %.9f, want %.9f", got, wantPriority)
	}

	flex := service.Calculate(cpaapi.UsageRecord{
		Model: "fallback-tier-model", ServiceTier: "flex",
		Detail: cpaapi.UsageDetail{InputTokens: 20, OutputTokens: 10},
	})
	wantFlex := (20*0.000001 + 10*0.000002) * 0.5
	if got := float64(flex.AmountNanos) / creditNanosPerUSD; math.Abs(got-wantFlex) > 1e-9 {
		t.Fatalf("flex USD = %.9f, want %.9f", got, wantFlex)
	}
}

func TestCreditPricingUnknownAndFailedRequestsAreNotCharged(t *testing.T) {
	service := NewSub2APICreditUsage()
	defer service.Close()
	service.SetEnabled(true)

	unknown := service.Calculate(cpaapi.UsageRecord{Model: "unknown-model", Detail: cpaapi.UsageDetail{TotalTokens: 10}})
	if !unknown.Enabled || unknown.Rated || unknown.AmountNanos != 0 {
		t.Fatalf("unknown charge = %#v", unknown)
	}
	failed := service.Calculate(cpaapi.UsageRecord{Model: "gpt-5.4", Failed: true, Detail: cpaapi.UsageDetail{TotalTokens: 10}})
	if failed.Enabled || failed.Rated || failed.AmountNanos != 0 {
		t.Fatalf("failed charge = %#v", failed)
	}
}

func TestCreditPricingCoversRequestedNonGPTModelFamilies(t *testing.T) {
	service := NewSub2APICreditUsage()
	defer service.Close()
	service.SetEnabled(true)
	tests := []struct {
		model string
		want  bool
	}{
		{model: "claude-sonnet-4-6", want: true},
		{model: "claude-opus-5-thinking", want: true},
		{model: "gemini-2.5-flash", want: true},
		{model: "deepseek-chat", want: true},
		{model: "deepseek-reasoner", want: true},
		{model: "claude-sonnet-4-5", want: true},
		{model: "claude-opus-4-6-thinking", want: true},
		{model: "gemini-3-pro-preview", want: true},
		{model: "gemini-3.1-pro-high", want: true},
		{model: "unknown-model", want: false},
	}
	for _, test := range tests {
		charge := service.Calculate(cpaapi.UsageRecord{
			Model:  test.model,
			Detail: cpaapi.UsageDetail{InputTokens: 100, OutputTokens: 10},
		})
		if charge.Rated != test.want {
			t.Fatalf("Calculate(%q) rated = %t, want %t", test.model, charge.Rated, test.want)
		}
	}
}

func TestCreditPricingRemoteSyncRejectsHashMismatchWithoutReplacingLastGoodTable(t *testing.T) {
	service := NewSub2APICreditUsage()
	defer service.Close()
	previous := service.table.Load()
	if previous == nil {
		t.Fatal("embedded pricing table was not loaded")
	}
	service.client = &http.Client{
		Transport: creditPricingRoundTripper(func(request *http.Request) (*http.Response, error) {
			switch request.URL.String() {
			case creditPricingHashURL:
				return creditPricingResponse(request, http.StatusOK, strings.Repeat("0", 64)), nil
			case creditPricingJSONURL:
				return creditPricingResponse(request, http.StatusOK, `{"replacement":{"input_cost_per_token":1}}`), nil
			default:
				t.Fatalf("unexpected pricing URL %q", request.URL.String())
				return nil, nil
			}
		}),
		Timeout: time.Second,
	}

	if err := service.syncRemote(context.Background()); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("syncRemote() error = %v, want hash mismatch", err)
	}
	if current := service.table.Load(); current != previous {
		t.Fatal("hash mismatch replaced the last-good pricing table")
	}
}

func TestCreditPricingFetchRejectsOversizedAndRedirectResponses(t *testing.T) {
	service := NewSub2APICreditUsage()
	defer service.Close()
	service.client = &http.Client{
		Transport: creditPricingRoundTripper(func(request *http.Request) (*http.Response, error) {
			if request.URL.Path == "/redirect" {
				response := creditPricingResponse(request, http.StatusFound, "")
				response.Header.Set("Location", "https://example.invalid/pricing.json")
				return response, nil
			}
			return creditPricingResponse(request, http.StatusOK, "12345"), nil
		}),
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       time.Second,
	}

	if _, err := service.fetchBounded(context.Background(), "https://pricing.invalid/large", 4); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized fetch error = %v", err)
	}
	if _, err := service.fetchBounded(context.Background(), "https://pricing.invalid/redirect", 128); err == nil || !strings.Contains(err.Error(), "HTTP status 302") {
		t.Fatalf("redirect fetch error = %v", err)
	}
}

func TestUsageTrackerCreditModePreservesTokensAndPersistsCredit(t *testing.T) {
	dataDir := t.TempDir()
	service := NewSub2APICreditUsage()
	service.SetEnabled(true)
	tracker := NewUsageTracker()
	tracker.persistDelay = time.Hour
	tracker.SetCreditCalculator(service)
	tracker.Configure(Config{DataDir: dataDir})
	tracker.Observe(cpaapi.UsageRecord{
		AuthIndex: "credit-index", Model: "gpt-5.4",
		Detail: cpaapi.UsageDetail{InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100},
	})
	tracker.Observe(cpaapi.UsageRecord{
		AuthIndex: "credit-index", Model: "unknown-model",
		Detail: cpaapi.UsageDetail{InputTokens: 10, TotalTokens: 10},
	})
	snapshot := tracker.Snapshot("credit-index")
	if snapshot == nil || snapshot.TotalTokens != 1110 || snapshot.Credit == nil || snapshot.Credit.RatedRequests != 1 || snapshot.Credit.UnratedRequests != 1 || snapshot.Credit.AmountUSD <= 0 {
		t.Fatalf("credit snapshot = %#v", snapshot)
	}
	tracker.Close()
	service.Close()

	restored := NewUsageTracker()
	defer restored.Close()
	restored.persistDelay = time.Hour
	restored.Configure(Config{DataDir: dataDir})
	restoredSnapshot := restored.Snapshot("credit-index")
	if restoredSnapshot == nil || restoredSnapshot.TotalTokens != 1110 || restoredSnapshot.Credit == nil || restoredSnapshot.Credit.RatedRequests != 1 || restoredSnapshot.Credit.UnratedRequests != 1 {
		t.Fatalf("restored credit snapshot = %#v", restoredSnapshot)
	}
}

func TestUsageTrackerCreditModeMeasuresAndPersistsOverdraftCredit(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Date(2026, time.August, 12, 8, 0, 0, 0, time.UTC)
	service := NewSub2APICreditUsage()
	service.SetEnabled(true)
	tracker := NewUsageTracker()
	tracker.now = func() time.Time { return now }
	tracker.persistDelay = time.Hour
	tracker.SetCreditCalculator(service)
	tracker.Configure(Config{DataDir: dataDir})
	headers := http.Header{
		"X-Codex-Secondary-Used-Percent":        []string{"100"},
		"X-Codex-Secondary-Reset-After-Seconds": []string{"3600"},
		"X-Codex-Secondary-Window-Minutes":      []string{"300"},
		"X-Codex-Primary-Used-Percent":          []string{"100"},
		"X-Codex-Primary-Reset-After-Seconds":   []string{"86400"},
		"X-Codex-Primary-Window-Minutes":        []string{"10080"},
	}
	tracker.Observe(cpaapi.UsageRecord{
		AuthIndex: "credit-overdraft", Model: "gpt-5.4", RequestedAt: now,
		Detail: cpaapi.UsageDetail{InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100}, ResponseHeaders: headers,
	})
	now = now.Add(time.Minute)
	tracker.BeginOverdraftCycle("credit-overdraft", InspectionQuotaWindowMultiple, now)
	tracker.Observe(cpaapi.UsageRecord{
		AuthIndex: "credit-overdraft", Model: "gpt-5.4", RequestedAt: now,
		Detail: cpaapi.UsageDetail{InputTokens: 2000, OutputTokens: 200, TotalTokens: 2200},
	})
	tracker.Observe(cpaapi.UsageRecord{
		AuthIndex: "credit-overdraft", Model: "unknown-overdraft-model", RequestedAt: now,
		Detail: cpaapi.UsageDetail{InputTokens: 20, TotalTokens: 20},
	})
	tracker.Observe(cpaapi.UsageRecord{
		AuthIndex: "credit-overdraft", Model: "gpt-5.4", RequestedAt: now, Failed: true,
		Detail: cpaapi.UsageDetail{InputTokens: 5000, OutputTokens: 500, TotalTokens: 5500},
	})

	snapshot := tracker.Snapshot("credit-overdraft")
	if snapshot == nil || snapshot.Credit == nil || snapshot.Credit.RatedRequests != 2 || snapshot.Credit.UnratedRequests != 1 {
		t.Fatalf("credit snapshot = %#v", snapshot)
	}
	for label, window := range map[string]*UsageWindowSnapshot{
		"5h": snapshot.Codex.FiveHour,
		"7d": snapshot.Codex.SevenDay,
	} {
		if window == nil || !window.OverdraftActive || window.OverdraftAmountUSD <= 0 || window.OverdraftRated != 1 || window.OverdraftUnrated != 1 {
			t.Fatalf("%s overdraft credit = %#v", label, window)
		}
		if window.OverdraftAmountUSD >= snapshot.Credit.AmountUSD {
			t.Fatalf("%s overdraft credit %f must remain a subset of total %f", label, window.OverdraftAmountUSD, snapshot.Credit.AmountUSD)
		}
	}
	wantOverdraftUSD := snapshot.Codex.FiveHour.OverdraftAmountUSD
	tracker.Close()
	service.Close()

	restored := NewUsageTracker()
	defer restored.Close()
	restored.now = func() time.Time { return now.Add(time.Minute) }
	restored.Configure(Config{DataDir: dataDir})
	restoredSnapshot := restored.Snapshot("credit-overdraft")
	if restoredSnapshot == nil || restoredSnapshot.Codex == nil || restoredSnapshot.Codex.FiveHour == nil ||
		math.Abs(restoredSnapshot.Codex.FiveHour.OverdraftAmountUSD-wantOverdraftUSD) > 1e-12 ||
		restoredSnapshot.Codex.FiveHour.OverdraftRated != 1 || restoredSnapshot.Codex.FiveHour.OverdraftUnrated != 1 {
		t.Fatalf("restored overdraft credit = %#v", restoredSnapshot)
	}
}

func TestUsageStoreVersionFiveFreezesExistingCreditAtActiveOverdraftBaseline(t *testing.T) {
	dataDir := t.TempDir()
	storePath := usageStorePath(dataDir)
	legacy := []byte(`{"version":5,"accounts":{"auth-index:legacy-credit":{"total_tokens":1100,"successful_tokens":1100,"successful_requests":1,"credit_amount_nanos":2750000,"credit_rated_requests":1,"credit_started_at":"2026-08-12T08:00:00Z","updated_at":"2026-08-12T08:00:00Z","five_hour_overdraft":{"active":true,"baseline_tokens":1100,"baseline_requests":1,"started_at":"2026-08-12T08:00:00Z","recover_at":"2026-08-12T13:00:00Z","window_minutes":300,"changed_at":"2026-08-12T08:00:00Z"},"codex":{"five_hour":{"used_percent":100,"window_minutes":300},"observed_at":"2026-08-12T08:00:00Z"}}}}`)
	if errWrite := os.WriteFile(storePath, legacy, 0o600); errWrite != nil {
		t.Fatalf("write version-five credit usage state: %v", errWrite)
	}

	tracker := NewUsageTracker()
	defer tracker.Close()
	tracker.now = func() time.Time { return time.Date(2026, time.August, 12, 8, 5, 0, 0, time.UTC) }
	tracker.Configure(Config{DataDir: dataDir})
	snapshot := tracker.Snapshot("legacy-credit")
	if snapshot == nil || snapshot.Codex == nil || snapshot.Codex.FiveHour == nil || !snapshot.Codex.FiveHour.OverdraftActive {
		t.Fatalf("migrated version-five snapshot = %#v", snapshot)
	}
	if snapshot.Codex.FiveHour.OverdraftAmountUSD != 0 || snapshot.Codex.FiveHour.OverdraftRated != 0 || snapshot.Codex.FiveHour.OverdraftUnrated != 0 {
		t.Fatalf("historical credit was relabeled as overdraft credit: %#v", snapshot.Codex.FiveHour)
	}
}

func TestUsageTrackerCreditModeDisabledDoesNotCreateCreditSnapshot(t *testing.T) {
	service := NewSub2APICreditUsage()
	defer service.Close()
	tracker := NewUsageTracker()
	defer tracker.Close()
	tracker.SetCreditCalculator(service)
	tracker.Observe(cpaapi.UsageRecord{
		AuthIndex: "disabled-credit-index", Model: "gpt-5.4",
		Detail: cpaapi.UsageDetail{InputTokens: 10, TotalTokens: 10},
	})
	snapshot := tracker.Snapshot("disabled-credit-index")
	if snapshot == nil || snapshot.TotalTokens != 10 || snapshot.Credit != nil {
		t.Fatalf("disabled credit snapshot = %#v", snapshot)
	}
}

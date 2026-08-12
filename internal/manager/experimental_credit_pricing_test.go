package manager

import (
	"context"
	"io"
	"math"
	"net/http"
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

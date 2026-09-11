package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// models.dev-shaped fixture: two OpenCode providers plus an unrelated provider
// that must never be parsed into the OpenCode catalog.
const openCodePricingFixture = `{
  "openai": {"name": "OpenAI", "models": {"gpt-5.5": {"cost": {"input": 1.25, "output": 10}}}},
  "opencode": {"name": "OpenCode Zen", "api": "https://opencode.ai/zen/v1", "doc": "https://opencode.ai/docs/zen",
    "models": {
      "claude-sonnet-4-6": {"name": "Claude Sonnet 4.6", "cost": {"input": 3, "output": 15, "cache_read": 0.3, "cache_write": 3.75}, "limit": {"context": 1000000, "output": 64000}},
      "gemini-3.1-pro": {"name": "Gemini 3.1 Pro", "cost": {"input": 2, "output": 12, "cache_read": 0.2, "tiers": [{"input": 4, "output": 18, "cache_read": 0.4, "tier": {"type": "context", "size": 200000}}]}, "limit": {"context": 1048576, "output": 65536}},
      "hidden-free": {"name": "Free", "cost": {}}
    }},
  "opencode-go": {"name": "OpenCode Go", "api": "https://opencode.ai/zen/go/v1", "doc": "https://opencode.ai/docs/zen",
    "models": {
      "qwen3.7-max": {"name": "Qwen3.7 Max", "cost": {"input": 2.5, "output": 7.5, "cache_read": 0.5}, "limit": {"context": 1000000, "output": 65536}},
      "longcat-2.0": {"name": "LongCat-2.0", "cost": {"input": 0.3, "output": 1.2}, "limit": {"context": 1000000, "output": 131072}}
    }}
}`

func TestOpenCodePricingParseKeepsOnlyOpenCodeProviders(t *testing.T) {
	table, errParse := parseOpenCodePricing([]byte(openCodePricingFixture), time.Time{}, "test")
	if errParse != nil {
		t.Fatalf("parse pricing: %v", errParse)
	}
	if len(table.Zen) != 3 || len(table.Go) != 2 {
		t.Fatalf("parsed models: zen=%d go=%d", len(table.Zen), len(table.Go))
	}
	if _, exists := table.Zen["gpt-5.5"]; exists {
		t.Fatalf("a non-OpenCode provider leaked into the catalog")
	}
	zen, ok := table.modelsFor("zen")["claude-sonnet-4-6"]
	if !ok || zen.InputUSDPerMillion != 3 || zen.OutputUSDPerMillion != 15 || zen.CacheReadUSDPerMillion != 0.3 || zen.CacheWriteUSDPerMillion != 3.75 {
		t.Fatalf("claude-sonnet-4-6 price = %#v", zen)
	}
	if zen.ContextTokens != 1000000 || zen.OutputTokens != 64000 {
		t.Fatalf("claude-sonnet-4-6 limit = %#v", zen)
	}
	tiered := table.modelsFor("zen")["gemini-3.1-pro"]
	if len(tiered.Tiers) != 1 || tiered.Tiers[0].MinContextTokens != 200000 || tiered.Tiers[0].InputUSDPerMillion != 4 {
		t.Fatalf("gemini-3.1-pro tiers = %#v", tiered.Tiers)
	}
	if empty := table.modelsFor("zen")["hidden-free"]; empty.InputUSDPerMillion != 0 || empty.OutputUSDPerMillion != 0 {
		t.Fatalf("free model priced: %#v", empty)
	}
	if _, errEmpty := parseOpenCodePricing([]byte(`{"openai":{"models":{}}}`), time.Time{}, "test"); errEmpty == nil {
		t.Fatalf("a payload without OpenCode providers must fail")
	}
}

func TestOpenCodeEmbeddedPricingSnapshotCoversBothGateways(t *testing.T) {
	service := NewOpenCodePricingService()
	defer service.Close()
	snapshot := service.Snapshot()
	if len(snapshot.Zen) == 0 || len(snapshot.Go) == 0 {
		t.Fatalf("embedded snapshot: zen=%d go=%d", len(snapshot.Zen), len(snapshot.Go))
	}
	if !strings.Contains(snapshot.Source, "embedded") {
		t.Fatalf("embedded provenance = %q", snapshot.Source)
	}
	if _, ok := service.Price("go", "qwen3.7-max"); !ok {
		t.Fatalf("Go catalog did not carry a documented Go model")
	}
	if _, ok := service.Price("zen", "claude-sonnet-4-6"); !ok {
		t.Fatalf("Zen catalog did not carry a documented Zen model")
	}
	// Lookup tolerates vendor prefixes and separator drift from routed requests.
	if _, ok := service.Price("zen", "opencode/claude-sonnet-4-6"); !ok {
		t.Fatalf("vendor-prefixed lookup failed")
	}
	if _, ok := service.Price("go", "Qwen3.7_Max"); !ok {
		t.Fatalf("normalized lookup failed")
	}
	if _, ok := service.Price("go", "definitely-not-a-model"); ok {
		t.Fatalf("unknown model resolved a price")
	}
}

func TestOpenCodePricingEstimateUsesOfficialRatesAndTiers(t *testing.T) {
	service := NewOpenCodePricingService()
	defer service.Close()
	service.table.Store(parsedOpenCodePricingForTest(t, openCodePricingFixture))

	// 1M uncached input at $3 plus 1M output at $15 is exactly $18.
	amount, priced := service.Estimate("zen", "claude-sonnet-4-6", OpenCodeTokenUsage{UncachedInputTokens: 1_000_000, OutputTokens: 1_000_000})
	if !priced || amount != 18*creditNanosPerUSD {
		t.Fatalf("estimate = %d, priced=%v", amount, priced)
	}
	// Cache reads and writes bill at their own rates: 1M read at $0.3 plus 1M write at $3.75 = $4.05.
	amount, priced = service.Estimate("zen", "claude-sonnet-4-6", OpenCodeTokenUsage{CacheReadTokens: 1_000_000, CacheWriteTokens: 1_000_000})
	if !priced || amount != int64(4.05*float64(creditNanosPerUSD)) {
		t.Fatalf("cache estimate = %d, priced=%v", amount, priced)
	}
	// Above the tier threshold Zen charges the tier rate: 1M input at $4 = $4.
	amount, priced = service.Estimate("zen", "gemini-3.1-pro", OpenCodeTokenUsage{UncachedInputTokens: 1_000_000, ContextTokens: 300_000})
	if !priced || amount != 4*creditNanosPerUSD {
		t.Fatalf("tiered estimate = %d, priced=%v", amount, priced)
	}
	// Below the threshold the base rate applies: 1M input at $2 = $2.
	amount, priced = service.Estimate("zen", "gemini-3.1-pro", OpenCodeTokenUsage{UncachedInputTokens: 1_000_000, ContextTokens: 100_000})
	if !priced || amount != 2*creditNanosPerUSD {
		t.Fatalf("base estimate = %d, priced=%v", amount, priced)
	}
	if amount, priced := service.Estimate("zen", "model-that-does-not-exist", OpenCodeTokenUsage{UncachedInputTokens: 1_000_000}); priced || amount != 0 {
		t.Fatalf("unpriced model reported priced=%v amount=%d", priced, amount)
	}
}

func TestOpenCodePricingRefreshRevalidatesAndCaches(t *testing.T) {
	dataDir := t.TempDir()
	responses := []*http.Response{
		jsonHTTPResponse(http.StatusOK, openCodePricingFixture),
		jsonHTTPResponse(http.StatusNotModified, ``),
		jsonHTTPResponse(http.StatusInternalServerError, `{}`),
	}
	requests := 0
	service := &OpenCodePricingService{
		client: &http.Client{Transport: creditPricingRoundTripper(func(request *http.Request) (*http.Response, error) {
			// Documentation pages are served alongside the mirror so the test
			// exercises both sync paths.
			if strings.HasPrefix(request.URL.String(), "https://opencode.ai/docs/") {
				if strings.Contains(request.URL.Path, "/go") {
					return jsonHTTPResponse(http.StatusOK, openCodeDocsFixture), nil
				}
				return jsonHTTPResponse(http.StatusOK, openCodeZenDocsFixture), nil
			}
			if requests == 1 && request.Header.Get("If-None-Match") == "" {
				t.Fatalf("revalidation did not send an ETag")
			}
			response := responses[requests]
			requests++
			response.Header.Set("ETag", `"etag-1"`)
			return response, nil
		})},
		now: func() time.Time { return time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC) },
	}
	service.Configure(Config{DataDir: dataDir})

	changed, errRefresh := service.Refresh(context.Background())
	if errRefresh != nil || !changed {
		t.Fatalf("first refresh: changed=%v err=%v", changed, errRefresh)
	}
	// The mirror rows survive and the documentation tables extend the catalog
	// with the official ids they publish.
	snapshot := service.Snapshot()
	if len(snapshot.Zen) < 3 || len(snapshot.Go) < 2 {
		t.Fatalf("refreshed snapshot lost mirror models: zen=%d go=%d", len(snapshot.Zen), len(snapshot.Go))
	}
	if _, ok := service.Price("go", "glm-5.3-flash"); !ok {
		t.Fatalf("the documentation models were not merged: %#v", service.ModelIDs("go"))
	}
	if _, ok := service.Price("zen", "claude-opus-5"); !ok {
		t.Fatalf("the Zen documentation models were not merged: %#v", service.ModelIDs("zen"))
	}
	if docs := snapshot.DocsUpdatedAt; docs.IsZero() {
		t.Fatalf("the documentation sync did not record its time")
	}
	cached, errRead := os.ReadFile(filepath.Join(dataDir, openCodePricingStoreFile))
	if errRead != nil {
		t.Fatalf("price cache was not written: %v", errRead)
	}
	if info, errStat := os.Stat(filepath.Join(dataDir, openCodePricingStoreFile)); errStat != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("price cache permissions = %v err=%v", info.Mode().Perm(), errStat)
	}
	var persisted persistedOpenCodePricing
	if errDecode := json.Unmarshal(cached, &persisted); errDecode != nil || persisted.ETag != `"etag-1"` || persisted.Version != openCodePricingStoreVersion {
		t.Fatalf("persisted cache = %s err=%v", cached, errDecode)
	}

	// A 304 keeps the catalog and is not reported as a change.
	changed, errRefresh = service.Refresh(context.Background())
	if errRefresh != nil || changed {
		t.Fatalf("revalidation: changed=%v err=%v", changed, errRefresh)
	}
	if _, ok := service.Price("go", "longcat-2.0"); !ok {
		t.Fatalf("revalidation dropped the catalog")
	}

	// A failed sync keeps the previous prices.
	changed, errRefresh = service.Refresh(context.Background())
	if errRefresh == nil || changed {
		t.Fatalf("failed refresh: changed=%v err=%v", changed, errRefresh)
	}
	if _, ok := service.Price("go", "longcat-2.0"); !ok {
		t.Fatalf("a failed refresh cleared the catalog")
	}

	// A new instance adopts the persisted catalog instead of the embedded baseline.
	restored := &OpenCodePricingService{client: &http.Client{Transport: creditPricingRoundTripper(func(*http.Request) (*http.Response, error) {
		return jsonHTTPResponse(http.StatusOK, `{}`), nil
	})}}
	restored.Configure(Config{DataDir: dataDir})
	if !strings.Contains(restored.Snapshot().Source, "cached") {
		t.Fatalf("restored provenance = %q", restored.Snapshot().Source)
	}
	if _, ok := restored.Price("zen", "hidden-free"); !ok {
		t.Fatalf("restored catalog is missing a model")
	}
}

func TestOpenCodeGatewayClassification(t *testing.T) {
	cases := []struct {
		baseURL string
		gateway bool
		kind    string
	}{
		{"https://opencode.ai/zen/go", true, "go"},
		{"https://opencode.ai/zen/go/v1", true, "go"},
		{"https://opencode.ai/zen", true, "zen"},
		{"https://opencode.ai/zen/v1", true, "zen"},
		{"https://opencode.ai/zen?x=1", true, "zen"},
		{"https://evil-opencode.ai/zen/go", false, ""},
		{"https://opencode.ai.evil.example/zen", false, ""},
		{"https://openrouter.ai/api/v1", false, ""},
		{"", false, ""},
	}
	for _, testCase := range cases {
		if got := isOpenCodeGatewayBaseURL(testCase.baseURL); got != testCase.gateway {
			t.Fatalf("isOpenCodeGatewayBaseURL(%q) = %v", testCase.baseURL, got)
		}
		if got := openCodeGatewayKind(testCase.baseURL); got != testCase.kind {
			t.Fatalf("openCodeGatewayKind(%q) = %q", testCase.baseURL, got)
		}
	}
}

// OpenCode channels must be valued with OpenCode prices even when a generic
// vendor price exists for the same model id, and non-OpenCode channels must keep
// the generic behavior.
func TestCreditPricingPrefersOpenCodePricesForOpenCodeChannels(t *testing.T) {
	pricing := NewOpenCodePricingService()
	defer pricing.Close()
	pricing.table.Store(parsedOpenCodePricingForTest(t, openCodePricingFixture))

	service := NewSub2APICreditUsage()
	service.SetEnabled(true)
	// Generic vendor prices are per token: $1 per million for every dimension.
	service.table.Store(parsedCreditPricingForTest(t, map[string]creditModelPricing{
		"claude-sonnet-4-6": {Input: 1e-6, Output: 1e-6},
		"longcat-2.0":       {Input: 1e-6, Output: 1e-6},
		"generic-only":      {Input: 1e-6, Output: 1e-6},
	}))

	zen := cpaapi.UsageRecord{Provider: "openai-compatibility", Model: "claude-sonnet-4-6", APIKey: "zen-key", Detail: cpaapi.UsageDetail{InputTokens: 1_000_000, OutputTokens: 1_000_000}}
	goUsage := cpaapi.UsageRecord{Provider: "openai-compatibility", Model: "longcat-2.0", APIKey: "go-key", Detail: cpaapi.UsageDetail{InputTokens: 1_000_000, OutputTokens: 1_000_000}}
	other := cpaapi.UsageRecord{Provider: "openai-compatibility", Model: "claude-sonnet-4-6", APIKey: "other-key", Detail: cpaapi.UsageDetail{InputTokens: 1_000_000, OutputTokens: 1_000_000}}
	unmapped := cpaapi.UsageRecord{Provider: "openai-compatibility", Model: "generic-only", APIKey: "unmapped-key", Detail: cpaapi.UsageDetail{InputTokens: 1_000_000, OutputTokens: 1_000_000}}
	service.SetOpenCodePricing(pricing, func(identity string) (string, bool) {
		switch identity {
		case runtimeCredentialIdentity(zen):
			return "https://opencode.ai/zen/v1", true
		case runtimeCredentialIdentity(goUsage):
			return "https://opencode.ai/zen/go/v1", true
		case runtimeCredentialIdentity(other):
			return "https://openrouter.ai/api/v1", true
		}
		return "", false
	})

	// Zen gateway: OpenCode's own price ($3 in + $15 out) wins over the vendor price.
	charge := service.Calculate(zen)
	if !charge.Rated || charge.AmountNanos != 18*creditNanosPerUSD {
		t.Fatalf("Zen charge = %#v", charge)
	}
	if !strings.Contains(charge.PricingSource, "models.dev") {
		t.Fatalf("Zen charge provenance = %q", charge.PricingSource)
	}
	// Go gateway: the Go catalog prices the Go model at $0.3 in + $1.2 out.
	charge = service.Calculate(goUsage)
	if !charge.Rated || charge.AmountNanos != int64(1.5*float64(creditNanosPerUSD)) {
		t.Fatalf("Go charge = %#v", charge)
	}
	// A non-OpenCode channel keeps the generic price ($1 in + $1 out).
	charge = service.Calculate(other)
	if !charge.Rated || charge.AmountNanos != 2*creditNanosPerUSD {
		t.Fatalf("generic charge = %#v", charge)
	}
	if !strings.Contains(charge.PricingSource, "generic") {
		t.Fatalf("generic charge provenance = %q", charge.PricingSource)
	}
	// An unmapped identity cannot be attributed to OpenCode.
	charge = service.Calculate(unmapped)
	if !charge.Rated || charge.AmountNanos != 2*creditNanosPerUSD {
		t.Fatalf("unmapped charge = %#v", charge)
	}
	// A model the OpenCode catalog does not list falls back to the generic table.
	unpriced := zen
	unpriced.Model = "generic-only"
	charge = service.Calculate(unpriced)
	if !charge.Rated || charge.AmountNanos != 2*creditNanosPerUSD {
		t.Fatalf("fallback charge = %#v", charge)
	}
}

func TestProviderNameServiceResolvesUniqueBaseURLOnly(t *testing.T) {
	service := NewAIProviderNameService()
	service.bindings = map[string]aiProviderNameBinding{
		"openai-compatibility:cred:alpha": {BaseURL: "https://opencode.ai/zen/go", Identities: []string{"credential:alpha"}},
		"openai-compatibility:cred:beta":  {BaseURL: "https://opencode.ai/zen", Identities: []string{"credential:beta"}},
		"openai-compatibility:cred:dup-a": {BaseURL: "https://a.example/v1", Identities: []string{"credential:dup"}},
		"openai-compatibility:cred:dup-b": {BaseURL: "https://b.example/v1", Identities: []string{"credential:dup"}},
	}
	if baseURL, ok := service.BaseURLForRuntimeIdentity("credential:alpha"); !ok || baseURL != "https://opencode.ai/zen/go" {
		t.Fatalf("alpha = %q ok=%v", baseURL, ok)
	}
	if _, ok := service.BaseURLForRuntimeIdentity("credential:dup"); ok {
		t.Fatalf("an ambiguous identity resolved a base URL")
	}
	if _, ok := service.BaseURLForRuntimeIdentity("credential:missing"); ok {
		t.Fatalf("an unknown identity resolved a base URL")
	}
	if _, ok := service.BaseURLForRuntimeIdentity(""); ok {
		t.Fatalf("an empty identity resolved a base URL")
	}
}

// Session attribution is credential-based: a request is only given an
// x-opencode-session when its CPA auth index belongs to an OpenCode channel, so
// vendor models resold by Zen (gpt-5.5) never leak OpenCode headers into other
// providers' traffic.
func TestAppInjectsOpenCodeSessionForAttributedRequestsOnly(t *testing.T) {
	app := NewApp(&fakeAuthHost{}, nil)
	app.opencodeSession.Configure(Config{DataDir: t.TempDir()})
	app.aiProviderNames = NewAIProviderNameService()
	app.aiProviderNames.bindings = map[string]aiProviderNameBinding{
		"openai-compatibility:cred:opencode": {
			BaseURL:    "https://opencode.ai/zen/go/v1",
			Provider:   "openai",
			Identities: []string{"auth-index:cpa-opencode-1"},
		},
	}
	app.refreshOpenCodeSessionTargets()
	if !app.RequestInterceptionActive() {
		t.Fatalf("the app does not report an active request interceptor")
	}

	body := []byte(`{"messages":[{"role":"user","content":"partition this conversation"}]}`)
	openCodeRequest := cpaapi.RequestInterceptRequest{
		RequestID: "req-1", Model: "qwen3.7-max", Headers: http.Header{}, Body: body,
		Metadata: map[string]any{"selected_auth_index": "cpa-opencode-1"},
	}
	response := app.HandleRequestAfter(openCodeRequest)
	first := response.Headers.Get(openCodeSessionHeader)
	if first == "" || !strings.HasPrefix(first, "oc-") {
		t.Fatalf("attributed request did not receive a session header: %#v", response.Headers)
	}
	if response.Headers.Get(openCodeClientHeader) != "cli" {
		t.Fatalf("OpenCode client header = %q", response.Headers.Get(openCodeClientHeader))
	}
	// The same conversation keeps its session across requests.
	second := app.HandleRequestAfter(cpaapi.RequestInterceptRequest{
		RequestID: "req-2", Model: "qwen3.7-max", Headers: http.Header{}, Body: body,
		Metadata: map[string]any{"selected_auth_index": "cpa-opencode-1"},
	})
	if second.Headers.Get(openCodeSessionHeader) != first {
		t.Fatalf("conversation session changed between requests: %q then %q", first, second.Headers.Get(openCodeSessionHeader))
	}
	// A different conversation is partitioned separately.
	other := app.HandleRequestAfter(cpaapi.RequestInterceptRequest{
		RequestID: "req-3", Model: "qwen3.7-max", Headers: http.Header{},
		Body:     []byte(`{"messages":[{"role":"user","content":"a different conversation"}]}`),
		Metadata: map[string]any{"selected_auth_index": "cpa-opencode-1"},
	})
	if other.Headers.Get(openCodeSessionHeader) == first {
		t.Fatalf("two conversations shared one session id")
	}
	// Another provider's channel is never touched, even for a vendor model id
	// that Zen also resells, and unattributed requests are left alone.
	for name, request := range map[string]cpaapi.RequestInterceptRequest{
		"other provider": {
			RequestID: "req-4", Model: "gpt-5.5", Headers: http.Header{}, Body: body,
			Metadata: map[string]any{"selected_auth_index": "cpa-codex-9"},
		},
		"unattributed": {
			RequestID: "req-5", Model: "gpt-5.5", Headers: http.Header{}, Body: body,
		},
	} {
		response := app.HandleRequestAfter(request)
		if response.Headers.Get(openCodeSessionHeader) != "" {
			t.Fatalf("%s request received a session header: %#v", name, response.Headers)
		}
	}
	// The OpenCode catalog still drives the reported coverage.
	snapshot := app.opencodeSession.Snapshot()
	if snapshot.InjectedRequests == 0 || len(snapshot.TargetModels) == 0 || snapshot.TargetAuthIndexes != 1 {
		t.Fatalf("session snapshot = %#v", snapshot)
	}
}

func parsedOpenCodePricingForTest(t *testing.T, raw string) *openCodePricingTable {
	t.Helper()
	table, errParse := parseOpenCodePricing([]byte(raw), time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC), "models.dev (test)")
	if errParse != nil {
		t.Fatalf("parse pricing fixture: %v", errParse)
	}
	return table
}

// parsedCreditPricingForTest builds a generic credit table through the production
// parser so precedence tests compare real parses.
func parsedCreditPricingForTest(t *testing.T, models map[string]creditModelPricing) *creditPricingTable {
	t.Helper()
	table := &creditPricingTable{Models: models, UpdatedAt: time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC), Source: "generic (test)"}
	return table
}

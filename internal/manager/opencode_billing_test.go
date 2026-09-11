package manager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// openCodeDocsFixture mirrors the official documentation table shapes: a model-id
// table, a Go price table carrying a monthly USD allowance, a request-estimate
// table, a Zen price table without allowances, and a deprecation table. The
// parser must join them on the display name and keep them apart from unrelated
// documentation tables.
const openCodeDocsFixture = `<!doctype html><html><body>
<table>
  <tr><th>Client</th><th>Session support</th></tr>
  <tr><td>Claude Code</td><td>Go recognizes its native session header.</td></tr>
</table>
<table>
  <tr><th>Model</th><th>Input</th><th>Output</th><th>Cached Read</th><th>Cached Write</th><th>Monthly limit</th></tr>
  <tr><td>GLM-5.3-Flash</td><td>$0.15</td><td>$0.50</td><td>$0.03</td><td>-</td><td>$60</td></tr>
  <tr><td>Kimi K3</td><td>$3.00</td><td>$15.00</td><td>$0.30</td><td>-</td><td>$15</td></tr>
  <tr><td>Not a model row</td><td>see above</td><td>see above</td><td>see above</td><td>see above</td><td>see above</td></tr>
</table>
<table>
  <tr><th>Model</th><th>requests per 5 hour</th><th>requests per week</th><th>requests per month</th></tr>
  <tr><td>GLM-5.3-Flash</td><td>1,830</td><td>4,580</td><td>9,150</td></tr>
  <tr><td>Kimi K3</td><td>220</td><td>540</td><td>1,080</td></tr>
</table>
<table>
  <tr><th>Model</th><th>Model ID</th><th>Endpoint</th><th>AI SDK Package</th></tr>
  <tr><td>GLM-5.3-Flash</td><td>glm-5.3-flash</td><td>https://opencode.ai/zen/go/v1/chat/completions</td><td>@ai-sdk/openai-compatible</td></tr>
  <tr><td>Kimi K3</td><td>kimi-k3</td><td>https://opencode.ai/zen/go/v1/chat/completions</td><td>@ai-sdk/openai-compatible</td></tr>
</table>
</body></html>`

const openCodeZenDocsFixture = `<!doctype html><html><body>
<table>
  <tr><th>Model</th><th>Model ID</th><th>Endpoint</th><th>AI SDK Package</th></tr>
  <tr><td>MiniMax M3</td><td>minimax-m3</td><td>https://opencode.ai/zen/v1/chat/completions</td><td>@ai-sdk/openai-compatible</td></tr>
  <tr><td>Claude Opus 5</td><td>claude-opus-5</td><td>https://opencode.ai/zen/v1/messages</td><td>@ai-sdk/anthropic</td></tr>
</table>
<table>
  <tr><th>Model</th><th>Input</th><th>Output</th><th>Cached Read</th><th>Cached Write</th></tr>
  <tr><td>MiniMax M3</td><td>$0.30</td><td>$1.20</td><td>$0.06</td><td>-</td></tr>
  <tr><td>Big Pickle</td><td>Free</td><td>Free</td><td>Free</td><td>-</td></tr>
  <tr><td>Claude Opus 5</td><td>$5.00</td><td>$25.00</td><td>$0.50</td><td>$6.25</td></tr>
</table>
<table>
  <tr><th>Model</th><th>Deprecation date</th></tr>
  <tr><td>Claude Sonnet 4</td><td>June 15, 2026</td></tr>
</table>
</body></html>`

func TestOpenCodeDocsParserReadsOfficialTables(t *testing.T) {
	goCatalog := parseOpenCodeDocsCatalog([]byte(openCodeDocsFixture))
	if len(goCatalog.Models) != 2 {
		t.Fatalf("parsed Go docs models = %d, want only the two real model rows", len(goCatalog.Models))
	}
	glm, ok := goCatalog.Models["glm-5.3-flash"]
	if !ok {
		t.Fatalf("the model-id table did not key the price row by the official id: %#v", goCatalog.openCodeDocsModelIDs())
	}
	if !glm.HasMonthly || glm.MonthlyUSD != 60 {
		t.Fatalf("GLM-5.3-Flash monthly allowance = %v (present=%v)", glm.MonthlyUSD, glm.HasMonthly)
	}
	if glm.Prices["input"] != 0.15 || glm.Prices["output"] != 0.5 || glm.Prices["cache_read"] != 0.03 {
		t.Fatalf("GLM-5.3-Flash prices = %#v", glm.Prices)
	}
	if _, billed := glm.Prices["cache_write"]; billed {
		t.Fatalf("an unlisted cache-write price must stay absent: %#v", glm.Prices)
	}
	if glm.Endpoint != "https://opencode.ai/zen/go/v1/chat/completions" {
		t.Fatalf("GLM-5.3-Flash endpoint = %q", glm.Endpoint)
	}
	if glm.Estimates.FiveHour != 1830 || glm.Estimates.Weekly != 4580 || glm.Estimates.Monthly != 9150 {
		t.Fatalf("GLM-5.3-Flash estimates = %#v", glm.Estimates)
	}
	kimi := goCatalog.Models["kimi-k3"]
	if kimi.MonthlyUSD != 15 || kimi.Estimates.Monthly != 1080 {
		t.Fatalf("Kimi K3 row = %#v", kimi)
	}
	// A documentation table that is not a pricing table must not become a model.
	for _, id := range goCatalog.openCodeDocsModelIDs() {
		if strings.Contains(id, "not-a-model") || strings.Contains(id, "client") {
			t.Fatalf("an unrelated table was parsed as a model: %q", id)
		}
	}

	zenCatalog := parseOpenCodeDocsCatalog([]byte(openCodeZenDocsFixture))
	// Three priced rows plus one deprecation-only row: a model that only appears
	// in the deprecation table must still be recorded, because the UI warns about it.
	if len(zenCatalog.Models) != 4 {
		t.Fatalf("parsed Zen docs models = %d", len(zenCatalog.Models))
	}
	miniMax := zenCatalog.Models["minimax-m3"]
	if miniMax.Prices["input"] != 0.3 || miniMax.Prices["output"] != 1.2 || miniMax.Prices["cache_read"] != 0.06 {
		t.Fatalf("MiniMax M3 prices = %#v", miniMax.Prices)
	}
	if miniMax.HasMonthly {
		t.Fatalf("Zen has no per-model monthly allowance: %#v", miniMax)
	}
	// "Free" is a real zero price, not a missing value.
	free, ok := zenCatalog.Models["big-pickle"]
	if !ok || free.Prices["input"] != 0 || free.Prices["output"] != 0 {
		t.Fatalf("free model prices = %#v (present=%v)", free.Prices, ok)
	}
	if claude := zenCatalog.Models["claude-opus-5"]; claude.Prices["cache_write"] != 6.25 {
		t.Fatalf("Claude Opus 5 cache-write price = %#v", claude.Prices)
	}
	if zenCatalog.Models["claude-sonnet-4"].Deprecated != "June 15, 2026" {
		t.Fatalf("deprecation date was not read: %#v", zenCatalog.Models["claude-sonnet-4"])
	}
}

// Official documentation prices, the Go monthly allowance, the request estimates
// and the endpoint must all reach the catalog, and a documented model the mirror
// has not published yet must still be priced.
func TestOpenCodeDocsPricingOverridesAndExtendsCatalog(t *testing.T) {
	catalog := map[string]OpenCodeModelPrice{
		"glm-5.3-flash": {ID: "glm-5.3-flash", Name: "GLM 5.3 Flash", InputUSDPerMillion: 9, OutputUSDPerMillion: 9},
	}
	applyOpenCodeDocsPricing(catalog, parseOpenCodeDocsCatalog([]byte(openCodeDocsFixture)))

	flash := catalog["glm-5.3-flash"]
	if !flash.OfficialPrices || flash.InputUSDPerMillion != 0.15 || flash.OutputUSDPerMillion != 0.5 {
		t.Fatalf("official prices did not win: %#v", flash)
	}
	if flash.MonthlyLimitUSD != 60 || flash.EstimatedRequests == nil || flash.EstimatedRequests.Monthly != 9150 {
		t.Fatalf("Go allowance or estimates missing: %#v", flash)
	}
	kimi, ok := catalog["kimi-k3"]
	if !ok {
		t.Fatalf("a documented model missing from the mirror was not added")
	}
	if kimi.MonthlyLimitUSD != 15 || !kimi.OfficialPrices {
		t.Fatalf("added model row = %#v", kimi)
	}
}

// The billing contract is read from the documentation prose, so a change on the
// OpenCode side is picked up by the next sync instead of staying hard coded.
func TestOpenCodeDocsBillingProseIsParsed(t *testing.T) {
	// The decoy sentence repeats the word "monthly" before the monthly value, which
	// a naive per-window regex reads as the monthly share.
	goProse := `<p>Usage limits are defined as monthly dollar amounts. The table below shows the monthly limit and token costs for each model.</p>` +
		`<p>OpenCode Go is a low cost $10/month subscription.</p>` +
		`<p>Each model has the following usage limits: 5-hour — 20% of the monthly limit; weekly — 50%; and monthly — 100%.</p>`
	goMode, parsed := parseOpenCodeDocsBilling(openCodeDocsText([]byte(goProse)), openCodeKindGoValue)
	if !parsed {
		t.Fatalf("Go billing prose was not parsed: %#v", goMode)
	}
	if goMode.SubscriptionUSD != 10 || goMode.FiveHourFraction != 0.2 || goMode.WeeklyFraction != 0.5 {
		t.Fatalf("Go billing = %#v", goMode)
	}

	// A changed contract must be adopted, not averaged with the defaults.
	changed := `<p>OpenCode Go is a low cost $12/month subscription.</p>` +
		`<p>Each model has the following usage limits: 5-hour — 25% of the monthly limit; weekly — 60%; and monthly — 100%.</p>`
	changedMode, parsed := parseOpenCodeDocsBilling(openCodeDocsText([]byte(changed)), openCodeKindGoValue)
	if !parsed || changedMode.SubscriptionUSD != 12 || changedMode.FiveHourFraction != 0.25 || changedMode.WeeklyFraction != 0.6 {
		t.Fatalf("a changed Go contract was not adopted: %#v", changedMode)
	}

	zenProse := `<p>Auto-reload: If your balance goes below $5, Zen will automatically reload $20.</p>`
	zenMode, parsed := parseOpenCodeDocsBilling(openCodeDocsText([]byte(zenProse)), openCodeKindZenValue)
	if !parsed || !zenMode.Metered || zenMode.AutoReloadBelowUSD != 5 || zenMode.AutoReloadUSD != 20 {
		t.Fatalf("Zen billing = %#v", zenMode)
	}

	// Missing or implausible prose keeps the documented defaults.
	empty, parsed := parseOpenCodeDocsBilling("", openCodeKindGoValue)
	if parsed {
		t.Fatalf("empty prose reported a parse: %#v", empty)
	}
	defaults := openCodeDocsBillingOrDefaults(openCodeDocsCatalog{}, openCodeDocsCatalog{})
	if len(defaults) != 2 || defaults[1].SubscriptionUSD != 10 || defaults[1].FiveHourFraction != 0.2 {
		t.Fatalf("defaults = %#v", defaults)
	}
	// A parsed gateway overrides its default while the other keeps its own.
	merged := openCodeDocsBillingOrDefaults(
		openCodeDocsCatalog{Billing: changedMode, BillingParsed: true},
		openCodeDocsCatalog{},
	)
	if merged[1].SubscriptionUSD != 12 || merged[1].FiveHourFraction != 0.25 || merged[0].AutoReloadUSD != 20 {
		t.Fatalf("merged billing = %#v", merged)
	}
}

// The embedded snapshot is generated from the live documentation and must keep
// both gateways usable offline, including the Go allowance.
func TestEmbeddedOpenCodeDocsSnapshot(t *testing.T) {
	goDocs, zenDocs, errDocs := parseEmbeddedOpenCodeDocs()
	if errDocs != nil {
		t.Fatalf("embedded docs snapshot: %v", errDocs)
	}
	if len(goDocs.Models) == 0 || len(zenDocs.Models) == 0 {
		t.Fatalf("embedded docs snapshot: go=%d zen=%d", len(goDocs.Models), len(zenDocs.Models))
	}
	withAllowance := 0
	for _, model := range goDocs.Models {
		if model.HasMonthly {
			withAllowance++
		}
	}
	if withAllowance == 0 {
		t.Fatalf("embedded docs snapshot carried no Go monthly allowance")
	}
	service := NewOpenCodePricingService()
	defer service.Close()
	snapshot := service.Snapshot()
	if snapshot.DocsUpdatedAt.IsZero() {
		t.Fatalf("embedded docs snapshot did not record its parse time")
	}
	modes := snapshot.Billing
	if len(modes) != 2 {
		t.Fatalf("billing modes = %#v", modes)
	}
	var zenMode, goMode OpenCodeBillingMode
	for _, mode := range modes {
		switch mode.Kind {
		case openCodeKindZenValue:
			zenMode = mode
		case openCodeKindGoValue:
			goMode = mode
		}
	}
	if !zenMode.Metered || goMode.Metered {
		t.Fatalf("billing modes are wrong: zen=%#v go=%#v", zenMode, goMode)
	}
	if goMode.SubscriptionUSD != 10 || goMode.FiveHourFraction != 0.2 || goMode.WeeklyFraction != 0.5 {
		t.Fatalf("Go billing descriptor = %#v", goMode)
	}
	priced := 0
	for _, price := range snapshot.Go {
		if price.OfficialPrices && price.MonthlyLimitUSD > 0 {
			priced++
		}
	}
	if priced == 0 {
		t.Fatalf("no Go model carried both an official price and an allowance")
	}
}

// TestGenerateOpenCodeDocsSnapshot regenerates the embedded snapshot from live
// documentation saved as HTML. It is skipped unless OPENCODE_DOCS_DIR points at a
// directory containing go.html and zen.html, so the committed snapshot is always
// produced by the same parser the runtime uses.
func TestGenerateOpenCodeDocsSnapshot(t *testing.T) {
	dir := strings.TrimSpace(os.Getenv("OPENCODE_DOCS_DIR"))
	if dir == "" {
		t.Skip("OPENCODE_DOCS_DIR is not set")
	}
	read := func(name string) []byte {
		raw, errRead := os.ReadFile(filepath.Join(dir, name))
		if errRead != nil {
			t.Fatalf("read %s: %v", name, errRead)
		}
		return raw
	}
	goCatalog := parseOpenCodeDocsCatalogFor(read("go.html"), openCodeKindGoValue)
	zenCatalog := parseOpenCodeDocsCatalogFor(read("zen.html"), openCodeKindZenValue)
	if len(goCatalog.Models) == 0 || len(zenCatalog.Models) == 0 {
		t.Fatalf("parsed docs are empty: go=%d zen=%d", len(goCatalog.Models), len(zenCatalog.Models))
	}
	snapshot := openCodeDocsSnapshot{
		Source: openCodeDocsSource + " (embedded)",
		Go:     goCatalog.snapshot(),
		Zen:    zenCatalog.snapshot(),
	}
	if goCatalog.BillingParsed {
		billing := goCatalog.Billing
		snapshot.GoBilling = &billing
	}
	if zenCatalog.BillingParsed {
		billing := zenCatalog.Billing
		snapshot.ZenBilling = &billing
	}
	encoded, errEncode := json.MarshalIndent(snapshot, "", " ")
	if errEncode != nil {
		t.Fatalf("encode snapshot: %v", errEncode)
	}
	target := filepath.Join("opencode_docs_snapshot.json")
	if errWrite := os.WriteFile(target, append(encoded, '\n'), 0o644); errWrite != nil {
		t.Fatalf("write snapshot: %v", errWrite)
	}
	t.Logf("wrote %s: go=%d zen=%d", target, len(snapshot.Go), len(snapshot.Zen))
}

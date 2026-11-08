package manager

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The Go dashboard is login-gated, so the credential-backed usage endpoint is the
// source the quota reader must prefer; a scrape-only reader reported every
// workspace as unparseable once the console moved behind a login.
type usageAPIDoer struct {
	status int
	body   string
	seen   []string
}

func (d *usageAPIDoer) Do(req *http.Request) (*http.Response, error) {
	d.seen = append(d.seen, req.URL.String())
	return &http.Response{
		StatusCode: d.status,
		Body:       io.NopCloser(strings.NewReader(d.body)),
		Header:     http.Header{},
		Request:    req,
	}, nil
}

func newUsageAPIService(t *testing.T, doer HTTPDoer, apiKey string) *OpenCodeQuotaService {
	t.Helper()
	service := NewOpenCodeQuotaService()
	service.SetDoer(doer)
	service.mu.Lock()
	service.accounts = []OpenCodeAccount{{ID: "acct-1", WorkspaceID: "wrk-1", APIKey: apiKey}}
	service.loaded = true
	service.mu.Unlock()
	return service
}

func TestOpenCodeQuotaPrefersTheUsageAPIOverTheDashboardScrape(t *testing.T) {
	doer := &usageAPIDoer{status: http.StatusOK, body: `{
      "rolling": {"usage_percent": 25.5, "reset_in_sec": 3600},
      "weekly":  {"usage_percent": 40, "reset_in_sec": 7200},
      "monthly": {"used": 5, "limit": 10}
    }`}
	service := newUsageAPIService(t, doer, "test-key")

	result, ok := service.queryOpenCodeGoUsageAPI("wrk-1", 5*time.Second)
	if !ok || !result.Success {
		t.Fatalf("usage API was not used: ok=%v result=%+v", ok, result)
	}
	if result.Source != "usage_api" {
		t.Fatalf("source = %q, want usage_api", result.Source)
	}
	if result.Rolling == nil || result.Rolling.UsagePercent != 25.5 {
		t.Fatalf("rolling = %+v", result.Rolling)
	}
	if result.Weekly == nil || result.Weekly.UsagePercent != 40 {
		t.Fatalf("weekly = %+v", result.Weekly)
	}
	// A used/limit window becomes a percentage, never a raw amount.
	if result.Monthly == nil || result.Monthly.UsagePercent != 50 {
		t.Fatalf("monthly = %+v", result.Monthly)
	}
	// The default base already ends in /zen/go: the usage path must be appended
	// once, not doubled.
	if len(doer.seen) != 1 || !strings.HasSuffix(doer.seen[0], "/zen/go/v1/usage") {
		t.Fatalf("requested %v, want one /zen/go/v1/usage call", doer.seen)
	}
	if strings.Contains(doer.seen[0], "/zen/go/zen/go") {
		t.Fatalf("the gateway path was doubled: %s", doer.seen[0])
	}
}

// Without a stored key the API cannot answer, so the reader declines and the
// caller keeps its dashboard fallback instead of reporting a false zero.
func TestOpenCodeQuotaUsageAPIDeclinesWithoutAKey(t *testing.T) {
	doer := &usageAPIDoer{status: http.StatusOK, body: `{"rolling":{"usage_percent":10}}`}
	service := newUsageAPIService(t, doer, "")
	if _, ok := service.queryOpenCodeGoUsageAPI("wrk-1", time.Second); ok {
		t.Fatal("the usage API reported a result without a credential")
	}
	if len(doer.seen) != 0 {
		t.Fatalf("a keyless account still made a request: %v", doer.seen)
	}
}

func TestOpenCodeQuotaUsageAPIDeclinesOnAnUnreadableAnswer(t *testing.T) {
	doer := &usageAPIDoer{status: http.StatusOK, body: `<html>login</html>`}
	service := newUsageAPIService(t, doer, "test-key")
	if _, ok := service.queryOpenCodeGoUsageAPI("wrk-1", time.Second); ok {
		t.Fatal("an unreadable answer was accepted")
	}
}

// A skipped API read explains itself, so the operator is not sent looking at the
// dashboard parser when the credential is the actual problem.
func TestOpenCodeQuotaUsageAPIExplainsWhyItWasSkipped(t *testing.T) {
	doer := &usageAPIDoer{status: http.StatusUnauthorized, body: `{"error":"Unauthorized"}`}
	service := newUsageAPIService(t, doer, "test-key")
	if _, ok, reason := service.queryOpenCodeGoUsageAPIWithStatus("wrk-1", time.Second); ok || reason == "" {
		t.Fatalf("ok=%v reason=%q, want a reason", ok, reason)
	}
	keyless := newUsageAPIService(t, doer, "")
	if _, ok, reason := keyless.queryOpenCodeGoUsageAPIWithStatus("wrk-1", time.Second); ok || !strings.Contains(reason, "API key") {
		t.Fatalf("keyless reason = %q", reason)
	}
}

// The endpoint has answered a list of windows with their own names as well as a
// flat object; both must read, otherwise a working credential still looks empty.
func TestOpenCodeUsageAPIParsesListAndNestedShapes(t *testing.T) {
	now := time.Now().UTC()
	cases := map[string]string{
		"list with names": `{"windows":[
			{"name":"rolling","usage_percent":12.5,"reset_in_sec":600},
			{"name":"weekly","usage_percent":30,"reset_in_sec":1200},
			{"name":"monthly","used":2,"limit":8}]}`,
		"nested object": `{"usage":{"five_hour":{"percent":12.5},"week":{"percent":30},"month":{"used":2,"limit":8}}}`,
		"snake keys":    `{"rolling_usage":{"usage_percent":12.5},"weekly_usage":{"usage_percent":30},"monthly_usage":{"used":2,"limit":8}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rolling, weekly, monthly := parseOpenCodeUsageAPI([]byte(body), now)
			if rolling == nil || weekly == nil || monthly == nil {
				t.Fatalf("rolling=%+v weekly=%+v monthly=%+v", rolling, weekly, monthly)
			}
			if rolling.UsagePercent != 12.5 || weekly.UsagePercent != 30 || monthly.UsagePercent != 25 {
				t.Fatalf("values = %v %v %v", rolling.UsagePercent, weekly.UsagePercent, monthly.UsagePercent)
			}
		})
	}
}

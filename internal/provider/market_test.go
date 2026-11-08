package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMarketDoer answers the three market requests from a fixed table and counts
// them, so a test never reaches GitHub or npm.
type fakeMarketDoer struct {
	mu     sync.Mutex
	body   map[string]string
	status map[string]int
	fail   map[string]error
	calls  []string
}

func (f *fakeMarketDoer) Do(req *http.Request) (*http.Response, error) {
	url := req.URL.String()
	f.mu.Lock()
	f.calls = append(f.calls, url)
	errInject := f.fail[url]
	if errInject == nil {
		for key, inject := range f.fail {
			if strings.Contains(url, key) {
				errInject = inject
				break
			}
		}
	}
	body, found := f.body[url]
	if !found {
		for key, value := range f.body {
			if strings.Contains(url, key) {
				body, found = value, true
				break
			}
		}
	}
	status := f.status[url]
	f.mu.Unlock()
	if errInject != nil {
		return nil, errInject
	}
	if status == 0 {
		status = http.StatusOK
	}
	if !found {
		status = http.StatusNotFound
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
		Request:    req,
	}, nil
}

func (f *fakeMarketDoer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

const testRegistry = `{
  "version": 1,
  "plugins": [
    {"package": "@magpie-community/opencode-zcode-auth", "name": "ZCode", "icon": "https://example.test/i.png",
     "providers": ["zcode"], "community": true,
     "summary": {"en": "ZCode sign-in", "zh": "ZCode 登录"}},
    {"package": "@magpie-community/gateway-respire-memory", "name": "respire-memory", "kind": "middleware", "community": true,
     "summary": {"en": "Memory", "zh": "记忆"}},
    {"package": "not a package name!", "name": "broken", "summary": {"en": "x"}},
    {"package": "@magpie-community/no-name", "summary": {"en": "x"}}
  ]
}`

const testSearch = `{"objects":[
  {"package":{"name":"@magpie-community/opencode-zcode-auth","version":"0.1.57"}},
  {"package":{"name":"@magpie-community/gateway-respire-memory","version":"0.3.0"}},
  {"package":{"name":"unrelated","version":"9.9.9"}}
]}`

const testPackument = `{
  "dist-tags": {"latest": "0.1.57"},
  "versions": {
    "0.1.55": {"name": "@magpie-community/opencode-zcode-auth", "version": "0.1.55", "main": "./old.mjs"},
    "0.1.57": {"name": "@magpie-community/opencode-zcode-auth", "version": "0.1.57", "main": "./opencode.mjs",
               "exports": {".": "./opencode2.mjs"}, "magpie": {"maxAccounts": 2}}
  }
}`

func newTestMarket(doer MarketDoer, now time.Time) *Market {
	market := NewMarket(doer)
	market.SetNow(func() time.Time { return now })
	return market
}

func TestMarketSnapshotJoinsTheRegistryWithPublishedVersions(t *testing.T) {
	doer := &fakeMarketDoer{body: map[string]string{
		marketRegistryURL:                "\n" + testRegistry,
		"registry.npmjs.org/-/v1/search": testSearch,
	}}
	market := newTestMarket(doer, time.Date(2026, time.November, 8, 10, 0, 0, 0, time.UTC))

	snapshot := market.Snapshot(context.Background(), "zh-CN")
	if !snapshot.Available {
		t.Fatalf("snapshot is unavailable: %+v", snapshot)
	}
	if snapshot.Source != marketSourceLabel {
		t.Fatalf("source = %q, want %q", snapshot.Source, marketSourceLabel)
	}
	// The two malformed entries (a bad package name, a missing name) are skipped,
	// and the middleware entry is not a provider plugin, so only the provider
	// plugin is listed; the count of everything left out is reported instead.
	if snapshot.Total != 1 || len(snapshot.Plugins) != 1 {
		t.Fatalf("plugins = %+v, want the one provider plugin", snapshot.Plugins)
	}
	if snapshot.Hidden != 1 {
		t.Fatalf("hidden = %d, want the middleware entry", snapshot.Hidden)
	}
	first := snapshot.Plugins[0]
	if first.ID != "@magpie-community/opencode-zcode-auth" || first.Name != "ZCode" {
		t.Fatalf("first entry = %+v", first)
	}
	if first.Version != "0.1.57" {
		t.Fatalf("version = %q, want the published version", first.Version)
	}
	if first.Summary != "ZCode 登录" {
		t.Fatalf("summary = %q, want the Chinese summary", first.Summary)
	}
	if snapshot.Versioned != 1 {
		t.Fatalf("versioned = %d, want 1", snapshot.Versioned)
	}
	if snapshot.FetchedAt.IsZero() {
		t.Fatal("fetched_at is zero")
	}
}

func TestMarketSnapshotPrefersEnglishForANonChineseLocale(t *testing.T) {
	doer := &fakeMarketDoer{body: map[string]string{marketRegistryURL: testRegistry, "v1/search": testSearch}}
	market := newTestMarket(doer, time.Now())
	snapshot := market.Snapshot(context.Background(), "en")
	if len(snapshot.Plugins) == 0 || snapshot.Plugins[0].Summary != "ZCode sign-in" {
		t.Fatalf("summary = %q, want the English summary", snapshot.Plugins[0].Summary)
	}
}

// A page that lists plugins repeatedly must not refetch the registry every time.
func TestMarketSnapshotServesTheCacheInsideItsWindow(t *testing.T) {
	doer := &fakeMarketDoer{body: map[string]string{marketRegistryURL: testRegistry, "v1/search": testSearch}}
	now := time.Date(2026, time.November, 8, 10, 0, 0, 0, time.UTC)
	market := newTestMarket(doer, now)

	market.Snapshot(context.Background(), "zh-CN")
	firstCalls := doer.callCount()
	market.Snapshot(context.Background(), "zh-CN")
	if doer.callCount() != firstCalls {
		t.Fatalf("calls = %d after a cached read, want %d", doer.callCount(), firstCalls)
	}

	// Past the window the registry is read again.
	market.SetNow(func() time.Time { return now.Add(marketCacheTTL + time.Minute) })
	market.Snapshot(context.Background(), "zh-CN")
	if doer.callCount() == firstCalls {
		t.Fatal("the cache did not expire")
	}
}

// An unreachable registry is a reason, not an error: the page explains it.
func TestMarketSnapshotReportsAnUnreachableRegistry(t *testing.T) {
	doer := &fakeMarketDoer{fail: map[string]error{marketRegistryURL: fmt.Errorf("dial tcp: no such host")}}
	market := newTestMarket(doer, time.Now())

	snapshot := market.Snapshot(context.Background(), "zh-CN")
	if snapshot.Available {
		t.Fatal("snapshot is available without a registry")
	}
	if snapshot.Reason != "market_unreachable" {
		t.Fatalf("reason = %q", snapshot.Reason)
	}
	if snapshot.Plugins == nil {
		t.Fatal("plugins is nil, want an empty list so JSON sends []")
	}
}

func TestMarketSnapshotReportsATimeoutSeparately(t *testing.T) {
	doer := &fakeMarketDoer{fail: map[string]error{marketRegistryURL: fmt.Errorf("context deadline exceeded")}}
	market := newTestMarket(doer, time.Now())
	if reason := market.Snapshot(context.Background(), "zh-CN").Reason; reason != "market_unreachable_timeout" {
		t.Fatalf("reason = %q", reason)
	}
}

// A registry that cannot be read at all is reported, not rendered as empty.
func TestMarketSnapshotReportsAnUnreadableRegistry(t *testing.T) {
	doer := &fakeMarketDoer{body: map[string]string{marketRegistryURL: "{not json"}}
	market := newTestMarket(doer, time.Now())
	snapshot := market.Snapshot(context.Background(), "zh-CN")
	if snapshot.Available || snapshot.Reason != "market_registry_unreadable" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

// The version lookup is an enhancement: when npm cannot answer, the entries stay
// and simply report no version.
func TestMarketSnapshotKeepsEntriesWhenVersionsCannotBeRead(t *testing.T) {
	doer := &fakeMarketDoer{
		body: map[string]string{marketRegistryURL: testRegistry},
		fail: map[string]error{"v1/search": fmt.Errorf("connection refused")},
	}
	market := newTestMarket(doer, time.Now())
	snapshot := market.Snapshot(context.Background(), "zh-CN")
	if !snapshot.Available || snapshot.Total != 1 || snapshot.Versioned != 0 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.Plugins[0].Version != "" {
		t.Fatalf("version = %q, want none", snapshot.Plugins[0].Version)
	}
}

func TestMarketManifestReadsTheLatestPublishedManifest(t *testing.T) {
	doer := &fakeMarketDoer{body: map[string]string{
		"registry.npmjs.org/@magpie-community%2Fopencode-zcode-auth": testPackument,
	}}
	market := newTestMarket(doer, time.Now())

	manifest, errManifest := market.Manifest(context.Background(), "@magpie-community/opencode-zcode-auth")
	if errManifest != nil {
		t.Fatalf("Manifest: %v", errManifest)
	}
	var published struct {
		Name    string         `json:"name"`
		Version string         `json:"version"`
		Main    string         `json:"main"`
		Magpie  map[string]any `json:"magpie"`
	}
	if errUnmarshal := json.Unmarshal(manifest, &published); errUnmarshal != nil {
		t.Fatalf("the returned manifest is not JSON: %v", errUnmarshal)
	}
	if published.Version != "0.1.57" || published.Main != "./opencode.mjs" {
		t.Fatalf("manifest = %+v, want the latest published version", published)
	}
	if _, found := published.Magpie["maxAccounts"]; !found {
		t.Fatalf("manifest lost the magpie metadata: %s", manifest)
	}
}

func TestMarketManifestRejectsAnIDThatIsNotAPackageName(t *testing.T) {
	doer := &fakeMarketDoer{}
	market := newTestMarket(doer, time.Now())
	for _, id := range []string{"", "  ", "../../etc/passwd", "@scope/../x", "UPPER CASE", "a/b/c"} {
		if _, errManifest := market.Manifest(context.Background(), id); errManifest == nil {
			t.Fatalf("id %q was accepted", id)
		}
	}
	if doer.callCount() != 0 {
		t.Fatalf("a rejected id still made %d request(s)", doer.callCount())
	}
}

func TestMarketManifestReportsAMissingPublishedVersion(t *testing.T) {
	doer := &fakeMarketDoer{body: map[string]string{"registry.npmjs.org/": `{"dist-tags":{},"versions":{}}`}}
	market := newTestMarket(doer, time.Now())
	if _, errManifest := market.Manifest(context.Background(), "some-package"); errManifest == nil {
		t.Fatal("a package with no published version was accepted")
	}
}

func TestMarketSnapshotOnANilMarketIsUnavailable(t *testing.T) {
	var market *Market
	snapshot := market.Snapshot(context.Background(), "zh-CN")
	if snapshot.Available || snapshot.Reason != "market_unavailable" || snapshot.Plugins == nil {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

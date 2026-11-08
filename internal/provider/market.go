package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The Magpie community market: one JSON list names every community plugin, and
// npm carries the published manifest each entry installs. Both live behind fixed
// HTTPS hosts, and the plugin asks them itself so the browser never has to reach
// the public internet from the operator's machine.
//
// The list is read-only. Importing a market entry records the same provider
// definition a pasted package.json would, and never downloads or runs code.
const (
	marketRegistryURL   = "https://raw.githubusercontent.com/magpie-community/plugins/main/registry.json"
	marketNpmSearchURL  = "https://registry.npmjs.org/-/v1/search"
	marketNpmPackageURL = "https://registry.npmjs.org/"
	marketCacheTTL      = 5 * time.Minute
	marketRequestTime   = 10 * time.Second
	// The registry file is a few tens of kilobytes; the cap keeps a hostile or
	// broken answer from filling memory.
	marketMaxRegistryBytes  = 4 << 20
	marketMaxPackumentBytes = 8 << 20
	// npm answers a page of search results at most this large.
	marketSearchPageSize = 250
	// marketSourceLabel is what an imported entry records as its origin.
	marketSourceLabel = "magpie-community"
)

// marketPackageName is the npm name shape magpie itself accepts. It is checked
// before a name is put in a URL or looked up, so a crafted id cannot escape the
// fixed hosts above.
var marketPackageName = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._~-]*/)?[a-z0-9][a-z0-9._~-]*$`)

// MarketPlugin is one importable community plugin.
type MarketPlugin struct {
	// ID is the npm package name, which is both the import key and the id the
	// registry uses.
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Version   string   `json:"version,omitempty"`
	Kind      string   `json:"kind,omitempty"`
	Summary   string   `json:"summary,omitempty"`
	Icon      string   `json:"icon,omitempty"`
	Providers []string `json:"providers,omitempty"`
	Replaces  string   `json:"replaces,omitempty"`
	Source    string   `json:"source"`
	// Imported reports that this package is already registered locally, and
	// ImportedVersion says which version was recorded.
	Imported        bool   `json:"imported"`
	ImportedVersion string `json:"imported_version,omitempty"`
	PackageURL      string `json:"package_url,omitempty"`
}

// MarketSnapshot is the read-only market listing. Available is false with a
// reason when the host could not reach the registry; the page then explains why
// instead of showing an empty list.
type MarketSnapshot struct {
	Available bool           `json:"available"`
	Reason    string         `json:"reason,omitempty"`
	Source    string         `json:"source"`
	Registry  string         `json:"registry,omitempty"`
	Versioned int            `json:"versioned"`
	Total     int            `json:"total"`
	FetchedAt time.Time      `json:"fetched_at,omitempty"`
	Plugins   []MarketPlugin `json:"plugins"`
}

// MarketDoer performs the HTTP requests. It is injectable so tests never reach
// the network.
type MarketDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

type marketCache struct {
	snapshot  MarketSnapshot
	expiresAt time.Time
}

// Market reads the community plugin registry with a short cache, so a page that
// lists plugins does not hammer GitHub or npm.
type Market struct {
	doer MarketDoer
	now  func() time.Time

	mu    sync.Mutex
	cache marketCache
}

// NewMarket builds the market reader. A nil doer falls back to a client that
// refuses to follow a redirect off the fixed hosts.
func NewMarket(doer MarketDoer) *Market {
	if doer == nil {
		doer = &http.Client{
			Timeout: marketRequestTime,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 3 {
					return fmt.Errorf("too many redirects")
				}
				if req.URL.Scheme != "https" {
					return fmt.Errorf("redirect left https")
				}
				return nil
			},
		}
	}
	return &Market{doer: doer, now: time.Now}
}

// SetNow lets a test control the cache clock.
func (m *Market) SetNow(now func() time.Time) {
	if m == nil || now == nil {
		return
	}
	m.mu.Lock()
	m.now = now
	m.mu.Unlock()
}

// Snapshot returns the community plugin list, serving the cached copy while it
// is fresh. locale picks the registry's summary language; anything other than a
// Chinese locale reads English.
func (m *Market) Snapshot(ctx context.Context, locale string) MarketSnapshot {
	if m == nil {
		return MarketSnapshot{Available: false, Reason: "market_unavailable", Plugins: []MarketPlugin{}, Source: marketSourceLabel}
	}
	m.mu.Lock()
	cached := m.cache
	now := m.clock()
	m.mu.Unlock()
	if cached.snapshot.Available && now.Before(cached.expiresAt) {
		return cached.snapshot
	}

	registry, errRegistry := m.fetch(ctx, marketRegistryURL, marketMaxRegistryBytes)
	if errRegistry != nil {
		return MarketSnapshot{
			Available: false,
			Reason:    m.reasonFor(errRegistry),
			Source:    marketSourceLabel,
			Registry:  marketRegistryURL,
			Plugins:   []MarketPlugin{},
		}
	}
	entries, errParse := parseMarketRegistry(registry, locale)
	if errParse != nil {
		return MarketSnapshot{
			Available: false,
			Reason:    "market_registry_unreadable",
			Source:    marketSourceLabel,
			Registry:  marketRegistryURL,
			Plugins:   []MarketPlugin{},
		}
	}

	// The registry names no version, so the published versions come from npm. A
	// missing npm answer still leaves a usable list: the entries simply report no
	// version instead of disappearing.
	versions := m.fetchVersions(ctx)
	versioned := 0
	for index := range entries {
		if version, found := versions[entries[index].ID]; found {
			entries[index].Version = version
			versioned++
		}
	}

	snapshot := MarketSnapshot{
		Available: true,
		Source:    marketSourceLabel,
		Registry:  marketRegistryURL,
		Versioned: versioned,
		Total:     len(entries),
		FetchedAt: now.UTC(),
		Plugins:   entries,
	}
	m.mu.Lock()
	m.cache = marketCache{snapshot: snapshot, expiresAt: now.Add(marketCacheTTL)}
	m.mu.Unlock()
	return snapshot
}

// Manifest returns one plugin's published package.json, fetched from npm. The
// caller imports the bytes; nothing is executed here.
func (m *Market) Manifest(ctx context.Context, id string) ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("market is unavailable")
	}
	name, errName := marketPackageID(id)
	if errName != nil {
		return nil, errName
	}
	if m.doer == nil {
		return nil, fmt.Errorf("market has no HTTP client")
	}
	raw, errFetch := m.fetch(ctx, marketNpmPackageURL+url.PathEscape(name), marketMaxPackumentBytes)
	if errFetch != nil {
		return nil, errFetch
	}
	return marketLatestManifest(raw)
}

// marketLatestManifest extracts the latest published manifest from an npm
// packument, which is the package.json npm would install.
func marketLatestManifest(raw []byte) ([]byte, error) {
	var packument struct {
		DistTags map[string]string          `json:"dist-tags"`
		Versions map[string]json.RawMessage `json:"versions"`
	}
	if errUnmarshal := json.Unmarshal(raw, &packument); errUnmarshal != nil {
		return nil, fmt.Errorf("npm did not answer with a package document")
	}
	latest := strings.TrimSpace(packument.DistTags["latest"])
	if latest == "" {
		return nil, fmt.Errorf("the package has no published version")
	}
	manifest, found := packument.Versions[latest]
	if !found || len(manifest) == 0 {
		return nil, fmt.Errorf("the package does not carry version %s", latest)
	}
	return manifest, nil
}

// marketPackageID validates one npm package name and returns it unchanged.
func marketPackageID(id string) (string, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return "", fmt.Errorf("the plugin id is empty")
	}
	if len(trimmed) > 214 || !marketPackageName.MatchString(trimmed) {
		return "", fmt.Errorf("the plugin id is not an npm package name")
	}
	return trimmed, nil
}

// parseMarketRegistry reads the community registry. A malformed entry is skipped
// rather than failing the whole list: one bad row must not hide every plugin.
func parseMarketRegistry(raw []byte, locale string) ([]MarketPlugin, error) {
	var document struct {
		Version int `json:"version"`
		Plugins []struct {
			Package   string            `json:"package"`
			Name      string            `json:"name"`
			Icon      string            `json:"icon"`
			Providers []string          `json:"providers"`
			Community bool              `json:"community"`
			Replaces  string            `json:"replaces"`
			Kind      string            `json:"kind"`
			Summary   map[string]string `json:"summary"`
		} `json:"plugins"`
	}
	if errUnmarshal := json.Unmarshal(raw, &document); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	out := make([]MarketPlugin, 0, len(document.Plugins))
	for _, entry := range document.Plugins {
		id, errID := marketPackageID(entry.Package)
		if errID != nil || strings.TrimSpace(entry.Name) == "" {
			continue
		}
		out = append(out, MarketPlugin{
			ID:         id,
			Name:       strings.TrimSpace(entry.Name),
			Kind:       strings.TrimSpace(entry.Kind),
			Summary:    marketSummary(entry.Summary, locale),
			Icon:       strings.TrimSpace(entry.Icon),
			Providers:  entry.Providers,
			Replaces:   strings.TrimSpace(entry.Replaces),
			Source:     marketSourceLabel,
			PackageURL: "https://www.npmjs.com/package/" + id,
		})
	}
	return out, nil
}

// marketSummary picks the registry's summary for the operator's language and
// falls back to English so an entry is never left without a description.
func marketSummary(summary map[string]string, locale string) string {
	if len(summary) == 0 {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(locale)), "zh") {
		if text := strings.TrimSpace(summary["zh"]); text != "" {
			return text
		}
	}
	if text := strings.TrimSpace(summary["en"]); text != "" {
		return text
	}
	for _, text := range summary {
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// fetchVersions reads the published version of every community package in one
// paginated npm search, so the listing carries versions without one request per
// plugin.
func (m *Market) fetchVersions(ctx context.Context) map[string]string {
	versions := map[string]string{}
	for from := 0; from <= marketSearchPageSize; from += marketSearchPageSize {
		query := url.Values{}
		query.Set("text", "@magpie-community")
		query.Set("size", fmt.Sprint(marketSearchPageSize))
		query.Set("from", fmt.Sprint(from))
		// A search page is a few hundred kilobytes.
		raw, errFetch := m.fetch(ctx, marketNpmSearchURL+"?"+query.Encode(), marketMaxRegistryBytes)
		if errFetch != nil {
			break
		}
		var page struct {
			Objects []struct {
				Package struct {
					Name    string `json:"name"`
					Version string `json:"version"`
				} `json:"package"`
			} `json:"objects"`
		}
		if errUnmarshal := json.Unmarshal(raw, &page); errUnmarshal != nil {
			break
		}
		for _, object := range page.Objects {
			name := strings.TrimSpace(object.Package.Name)
			if !strings.HasPrefix(name, "@magpie-community/") || !marketPackageName.MatchString(name) {
				continue
			}
			if version := strings.TrimSpace(object.Package.Version); version != "" {
				versions[name] = version
			}
		}
		if len(page.Objects) < marketSearchPageSize {
			break
		}
	}
	return versions
}

// fetch performs one bounded GET and returns the body. The context decides when
// to give up; the size cap decides how much of an answer is read.
func (m *Market) fetch(ctx context.Context, rawURL string, maxBytes int64) ([]byte, error) {
	if m == nil || m.doer == nil {
		return nil, fmt.Errorf("market has no HTTP client")
	}
	requestCtx, cancel := context.WithTimeout(ctx, marketRequestTime)
	defer cancel()
	request, errRequest := http.NewRequestWithContext(requestCtx, http.MethodGet, rawURL, nil)
	if errRequest != nil {
		return nil, errRequest
	}
	request.Header.Set("Accept", "application/json, text/plain;q=0.9")
	request.Header.Set("User-Agent", "cpa-account-config-manager")
	response, errDo := m.doer.Do(request)
	if errDo != nil {
		return nil, errDo
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream answered HTTP %d", response.StatusCode)
	}
	body, errRead := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if errRead != nil {
		return nil, errRead
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("the answer is larger than %d bytes", maxBytes)
	}
	return body, nil
}

// reasonFor turns a transport failure into one short code the page can explain.
// Only the shape of the failure is reported, never a URL, host or credential.
func (m *Market) reasonFor(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if strings.Contains(message, "context deadline exceeded") || strings.Contains(message, "Timeout") {
		return "market_unreachable_timeout"
	}
	return "market_unreachable"
}

// clock reports the current time, honoring a test override.
func (m *Market) clock() time.Time {
	if m == nil || m.now == nil {
		return time.Now()
	}
	return m.now()
}

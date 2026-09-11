package manager

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// OpenCode publishes its own prices for every Zen and Go model, and they are not
// the vendor prices: Zen resells Claude, GPT, Gemini, Qwen and the open coding
// models at its own rates, while Go is a flat $10/month subscription whose usage
// is still worth pricing at list value. This service keeps an official catalog
// for both providers so the plugin can show real prices and rate OpenCode usage
// with the OpenCode rates instead of a generic vendor table.
//
// Source of truth: models.dev, the model database maintained for OpenCode, which
// carries the "opencode" (Zen) and "opencode-go" (Go) providers with per-model
// cost in USD per million tokens. The catalog is refreshed on a timer with an
// ETag revalidation and can be refreshed on demand from the UI.

const (
	openCodePricingURL            = "https://models.dev/api.json"
	openCodePricingSource         = "models.dev (OpenCode Zen and OpenCode Go)"
	openCodePricingSyncInterval   = 24 * time.Hour
	openCodePricingRequestTimeout = 45 * time.Second
	openCodePricingMaxBytes       = 24 << 20
	openCodePricingStoreFile      = "opencode-pricing.json"
	openCodePricingStoreVersion   = 1
	openCodePricingMaxModels      = 4096

	openCodePricingProviderZen = "opencode"
	openCodePricingProviderGo  = "opencode-go"

	openCodeKindGoValue  = "go"
	openCodeKindZenValue = "zen"
)

//go:embed opencode_pricing_snapshot.json
var embeddedOpenCodePricingJSON []byte

// OpenCodePriceTier is a context-length price tier. Zen charges more once a
// request exceeds the tier threshold, so the tier is part of the price.
type OpenCodePriceTier struct {
	MinContextTokens        int64   `json:"min_context_tokens"`
	InputUSDPerMillion      float64 `json:"input_usd_per_million,omitempty"`
	OutputUSDPerMillion     float64 `json:"output_usd_per_million,omitempty"`
	CacheReadUSDPerMillion  float64 `json:"cache_read_usd_per_million,omitempty"`
	CacheWriteUSDPerMillion float64 `json:"cache_write_usd_per_million,omitempty"`
}

// OpenCodeModelPrice is one official price row in USD per million tokens.
type OpenCodeModelPrice struct {
	ID                      string              `json:"id"`
	Name                    string              `json:"name,omitempty"`
	InputUSDPerMillion      float64             `json:"input_usd_per_million,omitempty"`
	OutputUSDPerMillion     float64             `json:"output_usd_per_million,omitempty"`
	CacheReadUSDPerMillion  float64             `json:"cache_read_usd_per_million,omitempty"`
	CacheWriteUSDPerMillion float64             `json:"cache_write_usd_per_million,omitempty"`
	ContextTokens           int64               `json:"context_tokens,omitempty"`
	OutputTokens            int64               `json:"output_tokens,omitempty"`
	Tiers                   []OpenCodePriceTier `json:"tiers,omitempty"`
	// Fields below come from the official pricing docs rather than the mirror.
	MonthlyLimitUSD   float64                    `json:"monthly_limit_usd,omitempty"`
	EstimatedRequests *openCodeEstimatedRequests `json:"estimated_requests,omitempty"`
	Endpoint          string                     `json:"endpoint,omitempty"`
	DeprecatedAt      string                     `json:"deprecated_at,omitempty"`
	OfficialPrices    bool                       `json:"official_prices,omitempty"`
}

// OpenCodePricingSnapshot is the redacted pricing state exposed to the UI.
type OpenCodePricingSnapshot struct {
	UpdatedAt     time.Time             `json:"updated_at,omitempty"`
	DocsUpdatedAt time.Time             `json:"docs_updated_at,omitempty"`
	Source        string                `json:"source,omitempty"`
	Billing       []OpenCodeBillingMode `json:"billing,omitempty"`
	Zen           []OpenCodeModelPrice  `json:"zen,omitempty"`
	Go            []OpenCodeModelPrice  `json:"go,omitempty"`
	StorageError  string                `json:"storage_error,omitempty"`
}

// openCodePricingTable is the parsed catalog plus the revalidation state.
type openCodePricingTable struct {
	UpdatedAt time.Time
	Source    string
	ETag      string
	Zen       map[string]OpenCodeModelPrice
	Go        map[string]OpenCodeModelPrice
	// Docs is the official documentation catalog per kind, used for the Go
	// allowance and request estimates and as the authoritative price source.
	Docs map[string]openCodeDocsCatalog
	// DocsUpdatedAt records when the documentation tables were last parsed.
	DocsUpdatedAt time.Time
}

func (t *openCodePricingTable) modelsFor(kind string) map[string]OpenCodeModelPrice {
	if t == nil {
		return nil
	}
	if openCodePricingProviderForKind(kind) == openCodePricingProviderGo {
		return t.Go
	}
	return t.Zen
}

func openCodePricingProviderForKind(kind string) string {
	if strings.EqualFold(strings.TrimSpace(kind), openCodeKindZenValue) {
		return openCodePricingProviderZen
	}
	return openCodePricingProviderGo
}

type persistedOpenCodePricing struct {
	Version   int                                         `json:"version"`
	UpdatedAt time.Time                                   `json:"updated_at,omitempty"`
	Source    string                                      `json:"source,omitempty"`
	ETag      string                                      `json:"etag,omitempty"`
	Providers map[string]persistedOpenCodePricingProvider `json:"providers,omitempty"`
}

// persistedOpenCodePricingProvider stores the parsed prices directly, so a cached
// catalog round-trips without depending on the upstream models.dev shape.
type persistedOpenCodePricingProvider struct {
	Models map[string]OpenCodeModelPrice `json:"models,omitempty"`
}

// openCodePricingProviderPayload mirrors one models.dev provider entry.
type openCodePricingProviderPayload struct {
	Name   string                        `json:"name,omitempty"`
	API    string                        `json:"api,omitempty"`
	Doc    string                        `json:"doc,omitempty"`
	Models map[string]openCodePricingRow `json:"models,omitempty"`
}

// openCodePricingRow is one models.dev model entry. Cost is a typed struct because
// models.dev carries a "tiers" array inside cost, which a float map cannot decode.
type openCodePricingRow struct {
	Name  string                 `json:"name,omitempty"`
	Cost  openCodePricingCost    `json:"cost,omitempty"`
	Limit map[string]json.Number `json:"limit,omitempty"`
}

type openCodePricingCost struct {
	Input           *float64                 `json:"input,omitempty"`
	Output          *float64                 `json:"output,omitempty"`
	CacheRead       *float64                 `json:"cache_read,omitempty"`
	CacheWrite      *float64                 `json:"cache_write,omitempty"`
	Tiers           []openCodePricingRawTier `json:"tiers,omitempty"`
	ContextOver200k *openCodePricingCost     `json:"context_over_200k,omitempty"`
}

type openCodePricingRawTier struct {
	Input      *float64 `json:"input,omitempty"`
	Output     *float64 `json:"output,omitempty"`
	CacheRead  *float64 `json:"cache_read,omitempty"`
	CacheWrite *float64 `json:"cache_write,omitempty"`
	Tier       struct {
		Type string      `json:"type"`
		Size json.Number `json:"size"`
	} `json:"tier"`
}

// OpenCodePricingService owns the official OpenCode price catalog.
type OpenCodePricingService struct {
	mu         sync.RWMutex
	table      atomic.Pointer[openCodePricingTable]
	client     *http.Client
	storePath  string
	configured bool
	storageErr string
	activity   func() bool
	refreshing sync.Mutex
	stop       chan struct{}
	done       chan struct{}
	cancel     context.CancelFunc
	closeOnce  sync.Once
	now        func() time.Time
}

// NewOpenCodePricingService seeds the catalog from the embedded official
// snapshot so prices are available before the first successful sync.
func NewOpenCodePricingService() *OpenCodePricingService {
	service := &OpenCodePricingService{
		client: &http.Client{
			Timeout: openCodePricingRequestTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		stop: make(chan struct{}),
		done: make(chan struct{}),
		now:  func() time.Time { return time.Now().UTC() },
	}
	if table, errParse := parseOpenCodePricing(embeddedOpenCodePricingJSON, time.Time{}, openCodePricingSource+" (embedded)"); errParse == nil {
		docsGo, docsZen, errDocs := parseEmbeddedOpenCodeDocs()
		if errDocs == nil {
			table.Docs = map[string]openCodeDocsCatalog{openCodeKindGoValue: docsGo, openCodeKindZenValue: docsZen}
			table.DocsUpdatedAt = service.now()
			applyOpenCodeDocsPricing(table.Go, docsGo)
			applyOpenCodeDocsPricing(table.Zen, docsZen)
		}
		service.table.Store(table)
	}
	ctx, cancel := context.WithCancel(context.Background())
	service.cancel = cancel
	go service.run(ctx)
	return service
}

// Configure points the service at the plugin data directory and adopts a
// previously persisted catalog. A missing or unreadable cache is not fatal: the
// embedded snapshot keeps prices available.
func (s *OpenCodePricingService) Configure(config Config) {
	if s == nil {
		return
	}
	config = normalizeConfig(config)
	storePath := filepath.Join(config.DataDir, openCodePricingStoreFile)
	s.mu.Lock()
	changed := !s.configured || s.storePath != storePath
	s.storePath = storePath
	s.configured = true
	s.mu.Unlock()
	if !changed {
		return
	}
	raw, errRead := os.ReadFile(storePath)
	if errRead != nil {
		return
	}
	persisted, errDecode := decodePersistedOpenCodePricing(raw)
	if errDecode != nil {
		s.mu.Lock()
		s.storageErr = "OpenCode price cache could not be read"
		s.mu.Unlock()
		return
	}
	if table := persisted.table(); table != nil {
		table.Source = openCodePricingSource + " (cached)"
		s.table.Store(table)
	}
}

// SetActivityCheck limits the periodic sync to installations that actually use
// OpenCode. A nil check means "always sync".
func (s *OpenCodePricingService) SetActivityCheck(check func() bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.activity = check
	s.mu.Unlock()
}

func (s *OpenCodePricingService) activeNow() bool {
	s.mu.RLock()
	check := s.activity
	s.mu.RUnlock()
	return check == nil || check()
}

// Close stops the background sync loop.
func (s *OpenCodePricingService) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		close(s.stop)
		select {
		case <-s.done:
		case <-time.After(2 * time.Second):
		}
	})
}

func (s *OpenCodePricingService) run(ctx context.Context) {
	defer close(s.done)
	timer := time.NewTimer(openCodePricingSyncInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-timer.C:
		}
		if !s.activeNow() {
			timer.Reset(openCodePricingSyncInterval)
			continue
		}
		refreshCtx, cancel := context.WithTimeout(ctx, openCodePricingRequestTimeout+5*time.Second)
		_, _ = s.Refresh(refreshCtx)
		cancel()
		timer.Reset(openCodePricingSyncInterval)
	}
}

// Refresh revalidates the catalog against models.dev. A 304 keeps the current
// table and is reported as "not changed".
// refreshOpenCodeDocs re-parses the official pricing documentation for both
// gateways. A documentation failure never clears the previous tables, because a
// transient docs outage must not stop usage from being priced.
func (s *OpenCodePricingService) refreshOpenCodeDocs(ctx context.Context) (map[string]openCodeDocsCatalog, time.Time, bool, error) {
	fetcher := newOpenCodeDocsFetcher(s.client)
	catalogs := map[string]openCodeDocsCatalog{}
	changed := false
	var firstErr error
	for _, target := range []struct {
		kind string
		url  string
	}{
		{openCodeKindGoValue, openCodeGoDocsURL},
		{openCodeKindZenValue, openCodeZenDocsURL},
	} {
		body, errFetch := fetcher.fetch(ctx, target.url)
		if errFetch != nil {
			if firstErr == nil {
				firstErr = errFetch
			}
			continue
		}
		catalog := parseOpenCodeDocsCatalogFor(body, target.kind)
		if len(catalog.Models) == 0 {
			if firstErr == nil {
				firstErr = fmt.Errorf("OpenCode pricing docs carried no %s models", target.kind)
			}
			continue
		}
		catalogs[target.kind] = catalog
		changed = true
	}
	if len(catalogs) == 0 {
		if firstErr == nil {
			firstErr = fmt.Errorf("OpenCode pricing docs are unavailable")
		}
		return nil, time.Time{}, false, firstErr
	}
	return catalogs, s.now(), changed, nil
}

func (s *OpenCodePricingService) Refresh(ctx context.Context) (bool, error) {
	if s == nil {
		return false, errors.New("OpenCode pricing service is unavailable")
	}
	s.refreshing.Lock()
	defer s.refreshing.Unlock()

	docs, docsUpdatedAt, docsChanged, docsErr := s.refreshOpenCodeDocs(ctx)

	current := s.table.Load()
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, openCodePricingURL, nil)
	if errRequest != nil {
		return false, errRequest
	}
	request.Header.Set("Accept", "application/json")
	if current != nil && strings.TrimSpace(current.ETag) != "" {
		request.Header.Set("If-None-Match", current.ETag)
	}
	response, errDo := s.client.Do(request)
	if errDo != nil {
		return false, fmt.Errorf("OpenCode pricing could not be fetched")
	}
	if response == nil || response.Body == nil {
		return false, fmt.Errorf("OpenCode pricing returned an empty response")
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusNotModified:
		s.mu.Lock()
		s.storageErr = ""
		s.mu.Unlock()
		if current != nil {
			current.UpdatedAt = s.now()
			// A documentation re-parse only counts as a change when the tables
			// actually differ, so a daily revalidation stays quiet.
			changed := false
			if len(docs) > 0 {
				if current.Docs == nil {
					current.Docs = map[string]openCodeDocsCatalog{}
				}
				for kind, catalog := range docs {
					if !openCodeDocsEqual(current.Docs[kind], catalog) {
						changed = true
					}
					current.Docs[kind] = catalog
				}
				if changed {
					current.DocsUpdatedAt = docsUpdatedAt
				}
				applyOpenCodeDocsPricing(current.Go, current.Docs[openCodeKindGoValue])
				applyOpenCodeDocsPricing(current.Zen, current.Docs[openCodeKindZenValue])
			}
			s.persistOpenCodePricing(current)
			if docsErr != nil {
				return changed, docsErr
			}
			return changed, nil
		}
		return docsChanged, docsErr
	case http.StatusOK:
	default:
		return false, fmt.Errorf("OpenCode pricing returned HTTP status %d", response.StatusCode)
	}
	body, errRead := io.ReadAll(io.LimitReader(response.Body, openCodePricingMaxBytes+1))
	if errRead != nil {
		return false, fmt.Errorf("OpenCode pricing could not be read")
	}
	if int64(len(body)) > openCodePricingMaxBytes {
		return false, fmt.Errorf("OpenCode pricing response is too large")
	}
	table, errParse := parseOpenCodePricing(body, s.now(), openCodePricingSource)
	if errParse != nil {
		return false, errParse
	}
	table.ETag = strings.TrimSpace(response.Header.Get("ETag"))
	if current != nil {
		// Carry the official documentation state across a mirror refresh: the
		// allowance and estimates only exist in the docs.
		table.Docs = current.Docs
		table.DocsUpdatedAt = current.DocsUpdatedAt
	}
	if len(docs) > 0 {
		if table.Docs == nil {
			table.Docs = map[string]openCodeDocsCatalog{}
		}
		for kind, catalog := range docs {
			table.Docs[kind] = catalog
		}
		table.DocsUpdatedAt = docsUpdatedAt
	}
	if docsCatalog, ok := table.Docs[openCodeKindGoValue]; ok {
		applyOpenCodeDocsPricing(table.Go, docsCatalog)
	}
	if docsCatalog, ok := table.Docs[openCodeKindZenValue]; ok {
		applyOpenCodeDocsPricing(table.Zen, docsCatalog)
	}
	s.table.Store(table)

	s.persistOpenCodePricing(table)
	if docsErr != nil {
		return true, docsErr
	}
	return true, nil
}

// persistOpenCodePricing writes the catalog to the plugin data directory. A
// failed write reports a storage error but never fails the sync: the in-memory
// catalog is already updated and usable.
func (s *OpenCodePricingService) persistOpenCodePricing(table *openCodePricingTable) {
	if s == nil || table == nil {
		return
	}
	s.mu.Lock()
	storePath := s.storePath
	configured := s.configured
	s.mu.Unlock()
	if !configured || strings.TrimSpace(storePath) == "" {
		return
	}
	persisted := persistedOpenCodePricing{
		Version:   openCodePricingStoreVersion,
		UpdatedAt: table.UpdatedAt,
		Source:    table.Source,
		ETag:      table.ETag,
		Providers: map[string]persistedOpenCodePricingProvider{
			openCodePricingProviderZen: {Models: table.Zen},
			openCodePricingProviderGo:  {Models: table.Go},
		},
	}
	encoded, errEncode := json.Marshal(persisted)
	if errEncode != nil {
		return
	}
	if errMkdir := os.MkdirAll(filepath.Dir(storePath), 0o700); errMkdir != nil {
		s.mu.Lock()
		s.storageErr = "OpenCode price cache could not be written"
		s.mu.Unlock()
		return
	}
	if errWrite := writePrivateFileAtomically(storePath, encoded); errWrite != nil {
		s.mu.Lock()
		s.storageErr = "OpenCode price cache could not be written"
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.storageErr = ""
	s.mu.Unlock()
}

// Snapshot reports the catalog plus its provenance.
func (s *OpenCodePricingService) Snapshot() OpenCodePricingSnapshot {
	if s == nil {
		return OpenCodePricingSnapshot{}
	}
	s.mu.RLock()
	storageErr := s.storageErr
	s.mu.RUnlock()
	table := s.table.Load()
	if table == nil {
		return OpenCodePricingSnapshot{StorageError: storageErr}
	}
	return OpenCodePricingSnapshot{
		UpdatedAt:     table.UpdatedAt,
		DocsUpdatedAt: table.DocsUpdatedAt,
		Source:        table.Source,
		Billing:       openCodeDocsBillingOrDefaults(table.Docs[openCodeKindGoValue], table.Docs[openCodeKindZenValue]),
		Zen:           sortedOpenCodePrices(table.Zen),
		Go:            sortedOpenCodePrices(table.Go),
		StorageError:  storageErr,
	}
}

// ModelIDs lists the catalog model ids for one kind, sorted.
func (s *OpenCodePricingService) ModelIDs(kind string) []string {
	table := s.tableSnapshot()
	models := table.modelsFor(kind)
	if len(models) == 0 {
		return nil
	}
	ids := make([]string, 0, len(models))
	for id := range models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Price resolves the official price for one model. Lookup tolerates the vendor
// prefixes and separators seen in routed requests.
func (s *OpenCodePricingService) Price(kind, model string) (OpenCodeModelPrice, bool) {
	models := s.tableSnapshot().modelsFor(kind)
	if len(models) == 0 {
		return OpenCodeModelPrice{}, false
	}
	for _, candidate := range openCodePricingLookupKeys(model) {
		if price, ok := models[candidate]; ok {
			return price, true
		}
	}
	return OpenCodeModelPrice{}, false
}

// OpenCodeTokenUsage is the billable token breakdown of one request.
type OpenCodeTokenUsage struct {
	UncachedInputTokens int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheWriteTokens    int64
	ContextTokens       int64
}

// Estimate returns the list-price cost in credit nanos (1e9 nanos = 1 USD) for
// one request. Unpriced models report ok=false instead of a silent zero, so the
// caller can keep such usage unrated rather than under-reporting cost.
func (s *OpenCodePricingService) Estimate(kind, model string, usage OpenCodeTokenUsage) (int64, bool) {
	price, ok := s.Price(kind, model)
	if !ok {
		return 0, false
	}
	inputRate := price.InputUSDPerMillion
	outputRate := price.OutputUSDPerMillion
	cacheReadRate := price.CacheReadUSDPerMillion
	cacheWriteRate := price.CacheWriteUSDPerMillion
	for _, tier := range price.Tiers {
		if tier.MinContextTokens > 0 && usage.ContextTokens >= tier.MinContextTokens {
			if tier.InputUSDPerMillion > 0 {
				inputRate = tier.InputUSDPerMillion
			}
			if tier.OutputUSDPerMillion > 0 {
				outputRate = tier.OutputUSDPerMillion
			}
			if tier.CacheReadUSDPerMillion > 0 {
				cacheReadRate = tier.CacheReadUSDPerMillion
			}
			if tier.CacheWriteUSDPerMillion > 0 {
				cacheWriteRate = tier.CacheWriteUSDPerMillion
			}
		}
	}
	usd := (float64(nonNegative(usage.UncachedInputTokens))*inputRate +
		float64(nonNegative(usage.OutputTokens))*outputRate +
		float64(nonNegative(usage.CacheReadTokens))*cacheReadRate +
		float64(nonNegative(usage.CacheWriteTokens))*cacheWriteRate) / 1_000_000
	if usd <= 0 || math.IsNaN(usd) || math.IsInf(usd, 0) {
		return 0, true
	}
	return int64(math.Round(usd * creditNanosPerUSD)), true
}

// applyOpenCodeDocsPricing merges the official documentation tables into a price
// catalog. Documentation values win because they are the contractual prices, and
// a model the mirror has not picked up yet is added so a newly published model is
// priced immediately. The Go monthly allowance, the per-window request estimates
// and the endpoint only exist in the documentation.
func applyOpenCodeDocsPricing(models map[string]OpenCodeModelPrice, docs openCodeDocsCatalog) {
	if models == nil || len(docs.Models) == 0 {
		return
	}
	for id, doc := range docs.Models {
		key := strings.ToLower(strings.TrimSpace(id))
		if key == "" {
			continue
		}
		price, exists := models[key]
		if !exists {
			price = OpenCodeModelPrice{ID: strings.TrimSpace(id), Name: firstNonEmpty(doc.Name, id)}
		}
		if len(doc.Prices) > 0 {
			price.InputUSDPerMillion = doc.Prices["input"]
			price.OutputUSDPerMillion = doc.Prices["output"]
			price.CacheReadUSDPerMillion = doc.Prices["cache_read"]
			price.CacheWriteUSDPerMillion = doc.Prices["cache_write"]
			price.OfficialPrices = true
		}
		if doc.HasMonthly {
			price.MonthlyLimitUSD = doc.MonthlyUSD
		}
		if doc.HasEstimate {
			estimates := doc.Estimates
			price.EstimatedRequests = &estimates
		}
		if doc.Endpoint != "" {
			price.Endpoint = doc.Endpoint
		}
		if doc.Deprecated != "" {
			price.DeprecatedAt = doc.Deprecated
		}
		if doc.Name != "" && (price.Name == "" || price.Name == price.ID) {
			price.Name = doc.Name
		}
		models[key] = price
	}
}

// openCodeDocsCatalogFor returns the parsed official documentation catalog for a
// gateway, or an empty catalog when it was never loaded.
func (t *openCodePricingTable) openCodeDocsCatalogFor(kind string) openCodeDocsCatalog {
	if t == nil || len(t.Docs) == 0 {
		return openCodeDocsCatalog{}
	}
	return t.Docs[openCodePricingProviderForKindKind(kind)]
}

// openCodePricingProviderForKindKind maps a billing kind to the docs catalog key.
func openCodePricingProviderForKindKind(kind string) string {
	if strings.EqualFold(strings.TrimSpace(kind), openCodeKindZenValue) {
		return openCodeKindZenValue
	}
	return openCodeKindGoValue
}

// Provenance reports where the current catalog came from without copying it,
// so a hot rating path can label a charge cheaply.
func (s *OpenCodePricingService) Provenance() (time.Time, string) {
	table := s.tableSnapshot()
	if table == nil {
		return time.Time{}, ""
	}
	return table.UpdatedAt, table.Source
}

// isOpenCodeGatewayBaseURL reports whether a channel base URL points at an
// OpenCode gateway. Only the two documented gateways qualify, so a lookalike
// host never captures the OpenCode price table.
func isOpenCodeGatewayBaseURL(baseURL string) bool {
	parsed, errParse := url.Parse(strings.TrimSpace(baseURL))
	if errParse != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "opencode.ai" || strings.HasSuffix(host, ".opencode.ai")
}

// openCodeGatewayKind classifies an OpenCode gateway URL. The Go subscription
// lives under /zen/go; everything else on the gateway is Zen.
func openCodeGatewayKind(baseURL string) string {
	if !isOpenCodeGatewayBaseURL(baseURL) {
		return ""
	}
	parsed, errParse := url.Parse(strings.TrimSpace(baseURL))
	if errParse != nil {
		return ""
	}
	path := strings.ToLower(strings.TrimSuffix(parsed.Path, "/"))
	if path == "/zen/go" || strings.HasPrefix(path, "/zen/go/") || strings.Contains(path, "/go/") {
		return openCodeKindGoValue
	}
	return openCodeKindZenValue
}

func (s *OpenCodePricingService) tableSnapshot() *openCodePricingTable {
	if s == nil {
		return nil
	}
	return s.table.Load()
}

func sortedOpenCodePrices(models map[string]OpenCodeModelPrice) []OpenCodeModelPrice {
	if len(models) == 0 {
		return nil
	}
	rows := make([]OpenCodeModelPrice, 0, len(models))
	for _, price := range models {
		rows = append(rows, price)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

// openCodePricingLookupKeys lists the candidate catalog keys for a routed model
// name, most specific first.
func openCodePricingLookupKeys(model string) []string {
	trimmed := strings.ToLower(strings.TrimSpace(model))
	if trimmed == "" {
		return nil
	}
	keys := []string{trimmed}
	if index := strings.LastIndex(trimmed, "/"); index >= 0 && index+1 < len(trimmed) {
		keys = append(keys, trimmed[index+1:])
	}
	keys = append(keys, strings.ReplaceAll(trimmed, "_", "-"))
	seen := make(map[string]struct{}, len(keys))
	unique := keys[:0]
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, key)
	}
	return unique
}

// parseOpenCodePricing decodes the models.dev catalog, keeping only the OpenCode
// Zen and OpenCode Go providers. Providers other than the two targets are
// skipped without materializing their model maps.
func parseOpenCodePricing(raw []byte, updatedAt time.Time, source string) (*openCodePricingTable, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, errToken := decoder.Token()
	if errToken != nil {
		return nil, fmt.Errorf("OpenCode pricing payload is invalid")
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, fmt.Errorf("OpenCode pricing payload is not an object")
	}
	table := &openCodePricingTable{UpdatedAt: updatedAt, Source: source, Zen: map[string]OpenCodeModelPrice{}, Go: map[string]OpenCodeModelPrice{}}
	for decoder.More() {
		keyToken, errKey := decoder.Token()
		if errKey != nil {
			return nil, fmt.Errorf("OpenCode pricing payload is invalid")
		}
		key, _ := keyToken.(string)
		switch key {
		case openCodePricingProviderZen, openCodePricingProviderGo:
			var payload openCodePricingProviderPayload
			if errDecode := decoder.Decode(&payload); errDecode != nil {
				return nil, fmt.Errorf("OpenCode pricing provider %s is invalid", key)
			}
			target := table.Zen
			if key == openCodePricingProviderGo {
				target = table.Go
			}
			for id, row := range payload.Models {
				if len(target) >= openCodePricingMaxModels {
					break
				}
				trimmed := strings.ToLower(strings.TrimSpace(id))
				if trimmed == "" {
					continue
				}
				target[trimmed] = row.modelPrice(id)
			}
		default:
			var skipped json.RawMessage
			if errSkip := decoder.Decode(&skipped); errSkip != nil {
				return nil, fmt.Errorf("OpenCode pricing payload is invalid")
			}
		}
	}
	if len(table.Zen) == 0 && len(table.Go) == 0 {
		return nil, fmt.Errorf("OpenCode pricing payload carried no OpenCode models")
	}
	return table, nil
}

func (r openCodePricingRow) modelPrice(id string) OpenCodeModelPrice {
	price := OpenCodeModelPrice{
		ID:            strings.TrimSpace(id),
		Name:          firstNonEmpty(strings.TrimSpace(r.Name), strings.TrimSpace(id)),
		ContextTokens: openCodePricingLimitValue(r.Limit["context"]),
		OutputTokens:  openCodePricingLimitValue(r.Limit["output"]),
	}
	price.InputUSDPerMillion = safePrice(derefFloat(r.Cost.Input))
	price.OutputUSDPerMillion = safePrice(derefFloat(r.Cost.Output))
	price.CacheReadUSDPerMillion = safePrice(derefFloat(r.Cost.CacheRead))
	price.CacheWriteUSDPerMillion = safePrice(derefFloat(r.Cost.CacheWrite))
	for _, tier := range r.Cost.Tiers {
		size := openCodePricingLimitValue(tier.Tier.Size)
		if size <= 0 || !strings.EqualFold(strings.TrimSpace(tier.Tier.Type), "context") {
			continue
		}
		price.Tiers = append(price.Tiers, OpenCodePriceTier{
			MinContextTokens:        size,
			InputUSDPerMillion:      safePrice(derefFloat(tier.Input)),
			OutputUSDPerMillion:     safePrice(derefFloat(tier.Output)),
			CacheReadUSDPerMillion:  safePrice(derefFloat(tier.CacheRead)),
			CacheWriteUSDPerMillion: safePrice(derefFloat(tier.CacheWrite)),
		})
	}
	// Some entries only carry the legacy long-context block. Treat it as a
	// 200k-context tier so an over-threshold request is never under-priced.
	if len(price.Tiers) == 0 && r.Cost.ContextOver200k != nil {
		long := r.Cost.ContextOver200k
		price.Tiers = append(price.Tiers, OpenCodePriceTier{
			MinContextTokens:        200_000,
			InputUSDPerMillion:      safePrice(derefFloat(long.Input)),
			OutputUSDPerMillion:     safePrice(derefFloat(long.Output)),
			CacheReadUSDPerMillion:  safePrice(derefFloat(long.CacheRead)),
			CacheWriteUSDPerMillion: safePrice(derefFloat(long.CacheWrite)),
		})
	}
	sort.Slice(price.Tiers, func(i, j int) bool { return price.Tiers[i].MinContextTokens < price.Tiers[j].MinContextTokens })
	if price.Tiers == nil {
		price.Tiers = []OpenCodePriceTier{}
	}
	return price
}

func derefFloat(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func openCodePricingLimitValue(value json.Number) int64 {
	if strings.TrimSpace(value.String()) == "" {
		return 0
	}
	parsed, errParse := value.Int64()
	if errParse != nil {
		return 0
	}
	return nonNegative(parsed)
}

func decodePersistedOpenCodePricing(raw []byte) (persistedOpenCodePricing, error) {
	var persisted persistedOpenCodePricing
	if errDecode := json.Unmarshal(raw, &persisted); errDecode != nil {
		return persistedOpenCodePricing{}, errDecode
	}
	if persisted.Version != openCodePricingStoreVersion {
		return persistedOpenCodePricing{}, fmt.Errorf("unsupported OpenCode price cache version")
	}
	return persisted, nil
}

func (p persistedOpenCodePricing) table() *openCodePricingTable {
	zen := modelsFromPersistedPrices(p.Providers[openCodePricingProviderZen].Models)
	goModels := modelsFromPersistedPrices(p.Providers[openCodePricingProviderGo].Models)
	if len(zen) == 0 && len(goModels) == 0 {
		return nil
	}
	return &openCodePricingTable{UpdatedAt: p.UpdatedAt, Source: p.Source, ETag: p.ETag, Zen: zen, Go: goModels}
}

func modelsFromPersistedPrices(rows map[string]OpenCodeModelPrice) map[string]OpenCodeModelPrice {
	models := make(map[string]OpenCodeModelPrice, len(rows))
	for id, price := range rows {
		trimmed := strings.ToLower(strings.TrimSpace(id))
		if trimmed == "" {
			continue
		}
		if len(models) >= openCodePricingMaxModels {
			break
		}
		price.ID = firstNonEmpty(strings.TrimSpace(price.ID), strings.TrimSpace(id))
		price.Name = firstNonEmpty(strings.TrimSpace(price.Name), price.ID)
		models[trimmed] = price
	}
	return models
}

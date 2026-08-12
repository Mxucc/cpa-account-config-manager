package manager

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

const (
	creditPricingJSONURL        = "https://raw.githubusercontent.com/Wei-Shaw/model-price-repo/main/model_prices_and_context_window.json"
	creditPricingHashURL        = "https://raw.githubusercontent.com/Wei-Shaw/model-price-repo/main/model_prices_and_context_window.sha256"
	creditPricingSource         = "Sub2API / Wei-Shaw model-price-repo"
	creditPricingSyncInterval   = 24 * time.Hour
	creditPricingRequestTimeout = 15 * time.Second
	creditPricingMaxBytes       = 4 << 20
	creditPricingHashMaxBytes   = 1024
	creditNanosPerUSD           = 1_000_000_000
)

//go:embed model_prices_and_context_window.json
var embeddedCreditPricingJSON []byte

var creditModelDateSuffix = regexp.MustCompile(`-(?:\d{8}|\d{4}-\d{2}-\d{2})$`)

type CreditCharge struct {
	Enabled          bool
	AmountNanos      int64
	Rated            bool
	ObservedAt       time.Time
	PricingUpdatedAt time.Time
	PricingSource    string
}

type CreditPricingSnapshot struct {
	UpdatedAt time.Time
	Source    string
}

type UsageCreditCalculator interface {
	Enabled() bool
	Calculate(cpaapi.UsageRecord) CreditCharge
	Snapshot() CreditPricingSnapshot
}

type creditModelPricing struct {
	Input                       float64
	InputPriority               float64
	Output                      float64
	OutputPriority              float64
	CacheCreation               float64
	CacheCreationPriority       float64
	CacheRead                   float64
	CacheReadPriority           float64
	LongContextThreshold        int64
	LongContextInputMultiplier  float64
	LongContextOutputMultiplier float64
}

type creditPricingTable struct {
	Models    map[string]creditModelPricing
	UpdatedAt time.Time
	Source    string
}

type creditPricingRaw struct {
	Input                       *float64 `json:"input_cost_per_token"`
	InputPriority               *float64 `json:"input_cost_per_token_priority"`
	InputAbove                  *float64 `json:"input_cost_per_token_above_272k_tokens"`
	Output                      *float64 `json:"output_cost_per_token"`
	OutputPriority              *float64 `json:"output_cost_per_token_priority"`
	OutputAbove                 *float64 `json:"output_cost_per_token_above_272k_tokens"`
	CacheCreation               *float64 `json:"cache_creation_input_token_cost"`
	CacheCreationPriority       *float64 `json:"cache_creation_input_token_cost_priority"`
	CacheRead                   *float64 `json:"cache_read_input_token_cost"`
	CacheReadPriority           *float64 `json:"cache_read_input_token_cost_priority"`
	LongContextThreshold        *int64   `json:"long_context_input_token_threshold"`
	LongContextInputMultiplier  *float64 `json:"long_context_input_cost_multiplier"`
	LongContextOutputMultiplier *float64 `json:"long_context_output_cost_multiplier"`
}

type Sub2APICreditUsage struct {
	enabled    atomic.Bool
	table      atomic.Pointer[creditPricingTable]
	client     *http.Client
	mu         sync.Mutex
	cachePath  string
	configured bool
	wake       chan struct{}
	stop       chan struct{}
	done       chan struct{}
	closeOnce  sync.Once
}

func NewSub2APICreditUsage() *Sub2APICreditUsage {
	service := &Sub2APICreditUsage{
		client: &http.Client{
			Timeout: creditPricingRequestTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		wake: make(chan struct{}, 1),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	if table, err := parseCreditPricingTable(embeddedCreditPricingJSON, time.Time{}, creditPricingSource+" (embedded)"); err == nil {
		service.table.Store(table)
	}
	go service.run()
	return service
}

func (s *Sub2APICreditUsage) Configure(config Config, enabled bool) {
	if s == nil {
		return
	}
	config = normalizeConfig(config)
	cachePath := filepath.Join(config.DataDir, "model-pricing-cache.json")
	s.mu.Lock()
	changed := !s.configured || s.cachePath != cachePath
	s.cachePath = cachePath
	s.configured = true
	s.mu.Unlock()
	if changed {
		if raw, err := os.ReadFile(cachePath); err == nil {
			if info, errStat := os.Stat(cachePath); errStat == nil {
				if table, errParse := parseCreditPricingTable(raw, info.ModTime().UTC(), creditPricingSource+" (cached)"); errParse == nil {
					s.table.Store(table)
				}
			}
		}
	}
	s.SetEnabled(enabled)
}

func (s *Sub2APICreditUsage) SetEnabled(enabled bool) {
	if s == nil {
		return
	}
	s.enabled.Store(enabled)
	if enabled {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

func (s *Sub2APICreditUsage) Enabled() bool {
	return s != nil && s.enabled.Load()
}

func (s *Sub2APICreditUsage) Snapshot() CreditPricingSnapshot {
	if s == nil {
		return CreditPricingSnapshot{}
	}
	table := s.table.Load()
	if table == nil {
		return CreditPricingSnapshot{}
	}
	return CreditPricingSnapshot{UpdatedAt: table.UpdatedAt, Source: table.Source}
}

func (s *Sub2APICreditUsage) Calculate(record cpaapi.UsageRecord) CreditCharge {
	charge := CreditCharge{ObservedAt: record.RequestedAt.UTC()}
	if s == nil || !s.enabled.Load() || record.Failed {
		return charge
	}
	charge.Enabled = true
	table := s.table.Load()
	if table == nil {
		return charge
	}
	charge.PricingUpdatedAt = table.UpdatedAt
	charge.PricingSource = table.Source
	pricing, ok := resolveCreditModelPricing(table.Models, record.Model)
	if !ok {
		charge.Rated = false
		return charge
	}
	charge.Rated = true
	input := nonNegative(record.Detail.InputTokens)
	cacheRead := nonNegative(record.Detail.CacheReadTokens)
	if cacheRead == 0 {
		cacheRead = nonNegative(record.Detail.CachedTokens)
	}
	cacheCreation := nonNegative(record.Detail.CacheCreationTokens)
	uncachedInput := input - cacheRead - cacheCreation
	if uncachedInput < 0 {
		uncachedInput = 0
	}
	output := nonNegative(record.Detail.OutputTokens)

	inputPrice, outputPrice := pricing.Input, pricing.Output
	cacheReadPrice, cacheCreationPrice := pricing.CacheRead, pricing.CacheCreation
	tierMultiplier := 1.0
	if strings.EqualFold(strings.TrimSpace(record.ServiceTier), "priority") && (pricing.InputPriority > 0 || pricing.OutputPriority > 0 || pricing.CacheReadPriority > 0 || pricing.CacheCreationPriority > 0) {
		if pricing.InputPriority > 0 {
			inputPrice = pricing.InputPriority
		}
		if pricing.OutputPriority > 0 {
			outputPrice = pricing.OutputPriority
		}
		if pricing.CacheReadPriority > 0 {
			cacheReadPrice = pricing.CacheReadPriority
		}
		if pricing.CacheCreationPriority > 0 {
			cacheCreationPrice = pricing.CacheCreationPriority
		}
	} else {
		switch strings.ToLower(strings.TrimSpace(record.ServiceTier)) {
		case "priority":
			tierMultiplier = 2
		case "flex":
			tierMultiplier = 0.5
		}
	}
	inputMultiplier, outputMultiplier := 1.0, 1.0
	if pricing.LongContextThreshold > 0 && input > pricing.LongContextThreshold {
		if pricing.LongContextInputMultiplier > 1 {
			inputMultiplier = pricing.LongContextInputMultiplier
		}
		if pricing.LongContextOutputMultiplier > 1 {
			outputMultiplier = pricing.LongContextOutputMultiplier
		}
	}
	cost := (float64(uncachedInput)*inputPrice+float64(cacheRead)*cacheReadPrice+float64(cacheCreation)*cacheCreationPrice)*inputMultiplier + float64(output)*outputPrice*outputMultiplier
	cost *= tierMultiplier
	if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 {
		return CreditCharge{Enabled: true, Rated: false, ObservedAt: charge.ObservedAt, PricingUpdatedAt: charge.PricingUpdatedAt, PricingSource: charge.PricingSource}
	}
	if cost > float64(math.MaxInt64)/creditNanosPerUSD {
		charge.AmountNanos = math.MaxInt64
	} else {
		charge.AmountNanos = int64(math.Round(cost * creditNanosPerUSD))
	}
	return charge
}

func (s *Sub2APICreditUsage) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() { close(s.stop) })
	<-s.done
}

func (s *Sub2APICreditUsage) run() {
	defer close(s.done)
	ticker := time.NewTicker(creditPricingSyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.wake:
			if s.enabled.Load() {
				_ = s.syncRemote(context.Background())
			}
		case <-ticker.C:
			if s.enabled.Load() {
				_ = s.syncRemote(context.Background())
			}
		case <-s.stop:
			return
		}
	}
}

func (s *Sub2APICreditUsage) syncRemote(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, creditPricingRequestTimeout)
	defer cancel()
	hashRaw, err := s.fetchBounded(ctx, creditPricingHashURL, creditPricingHashMaxBytes)
	if err != nil {
		return fmt.Errorf("fetch pricing hash: %w", err)
	}
	expected := firstSHA256(string(hashRaw))
	if expected == "" {
		return errors.New("pricing hash is invalid")
	}
	data, err := s.fetchBounded(ctx, creditPricingJSONURL, creditPricingMaxBytes)
	if err != nil {
		return fmt.Errorf("fetch pricing data: %w", err)
	}
	actualBytes := sha256.Sum256(data)
	actual := hex.EncodeToString(actualBytes[:])
	if !strings.EqualFold(expected, actual) {
		return errors.New("pricing hash mismatch")
	}
	now := time.Now().UTC()
	table, err := parseCreditPricingTable(data, now, creditPricingSource)
	if err != nil {
		return err
	}
	s.table.Store(table)
	s.mu.Lock()
	cachePath := s.cachePath
	configured := s.configured
	s.mu.Unlock()
	if configured && strings.TrimSpace(cachePath) != "" {
		if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err == nil {
			_ = writePrivateFileAtomically(cachePath, data)
		}
	}
	return nil
}

func (s *Sub2APICreditUsage) fetchBounded(ctx context.Context, rawURL string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, maxBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, errors.New("pricing response is too large")
	}
	return body, nil
}

func writePrivateFileAtomically(path string, data []byte) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func firstSHA256(value string) string {
	for _, field := range strings.Fields(value) {
		if len(field) != sha256.Size*2 {
			continue
		}
		if _, err := hex.DecodeString(field); err == nil {
			return strings.ToLower(field)
		}
	}
	return ""
}

func parseCreditPricingTable(raw []byte, updatedAt time.Time, source string) (*creditPricingTable, error) {
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("decode model pricing: %w", err)
	}
	models := make(map[string]creditModelPricing, len(entries))
	for name, encoded := range entries {
		var item creditPricingRaw
		if json.Unmarshal(encoded, &item) != nil || (item.Input == nil && item.Output == nil) {
			continue
		}
		pricing := creditModelPricing{}
		if item.Input != nil {
			pricing.Input = safePrice(*item.Input)
		}
		if item.InputPriority != nil {
			pricing.InputPriority = safePrice(*item.InputPriority)
		}
		if item.Output != nil {
			pricing.Output = safePrice(*item.Output)
		}
		if item.OutputPriority != nil {
			pricing.OutputPriority = safePrice(*item.OutputPriority)
		}
		if item.CacheCreation != nil {
			pricing.CacheCreation = safePrice(*item.CacheCreation)
		}
		if item.CacheCreationPriority != nil {
			pricing.CacheCreationPriority = safePrice(*item.CacheCreationPriority)
		}
		if item.CacheRead != nil {
			pricing.CacheRead = safePrice(*item.CacheRead)
		}
		if item.CacheReadPriority != nil {
			pricing.CacheReadPriority = safePrice(*item.CacheReadPriority)
		}
		if item.LongContextThreshold != nil {
			pricing.LongContextThreshold = *item.LongContextThreshold
		}
		if item.LongContextInputMultiplier != nil {
			pricing.LongContextInputMultiplier = safeMultiplier(*item.LongContextInputMultiplier)
		}
		if item.LongContextOutputMultiplier != nil {
			pricing.LongContextOutputMultiplier = safeMultiplier(*item.LongContextOutputMultiplier)
		}
		if pricing.LongContextThreshold == 0 && strings.HasPrefix(strings.ToLower(name), "gpt-5.") && (item.InputAbove != nil || item.OutputAbove != nil) {
			pricing.LongContextThreshold = 272000
		}
		if pricing.LongContextInputMultiplier <= 1 && item.InputAbove != nil && pricing.Input > 0 {
			pricing.LongContextInputMultiplier = safeMultiplier(*item.InputAbove / pricing.Input)
		}
		if pricing.LongContextOutputMultiplier <= 1 && item.OutputAbove != nil && pricing.Output > 0 {
			pricing.LongContextOutputMultiplier = safeMultiplier(*item.OutputAbove / pricing.Output)
		}
		models[strings.ToLower(strings.TrimSpace(name))] = pricing
	}
	if len(models) == 0 {
		return nil, errors.New("model pricing table is empty")
	}
	return &creditPricingTable{Models: models, UpdatedAt: updatedAt.UTC(), Source: strings.TrimSpace(source)}, nil
}

func safePrice(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	return value
}

func safeMultiplier(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 1
	}
	return value
}

func resolveCreditModelPricing(models map[string]creditModelPricing, model string) (creditModelPricing, bool) {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if slash := strings.LastIndex(normalized, "/"); slash >= 0 {
		normalized = normalized[slash+1:]
	}
	normalized = strings.ReplaceAll(normalized, "_", "-")
	candidates := []string{normalized, creditModelDateSuffix.ReplaceAllString(normalized, "")}
	switch {
	case strings.Contains(normalized, "gpt-5.6-sol") || normalized == "gpt-5.6" || strings.Contains(normalized, "gpt-5.6-max"):
		candidates = append(candidates, "gpt-5.6-sol")
	case strings.Contains(normalized, "gpt-5.6-terra"):
		candidates = append(candidates, "gpt-5.6-terra")
	case strings.Contains(normalized, "gpt-5.6-luna"):
		candidates = append(candidates, "gpt-5.6-luna")
	case strings.Contains(normalized, "gpt-5.5-pro"):
		candidates = append(candidates, "gpt-5.5-pro")
	case strings.Contains(normalized, "gpt-5.5"):
		candidates = append(candidates, "gpt-5.5")
	case strings.Contains(normalized, "gpt-5.4-mini"):
		candidates = append(candidates, "gpt-5.4-mini")
	case strings.Contains(normalized, "gpt-5.4-nano"):
		candidates = append(candidates, "gpt-5.4-nano")
	case strings.Contains(normalized, "gpt-5.4"):
		candidates = append(candidates, "gpt-5.4")
	case strings.Contains(normalized, "gpt-5.3-codex-spark"):
		candidates = append(candidates, "gpt-5.3-codex-spark")
	case strings.Contains(normalized, "gpt-5.3") || strings.Contains(normalized, "codex"):
		candidates = append(candidates, "gpt-5.3-codex")
	}
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, exists := seen[candidate]; exists {
			continue
		}
		seen[candidate] = struct{}{}
		if pricing, exists := models[candidate]; exists {
			return pricing, true
		}
	}
	return creditModelPricing{}, false
}

package manager

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

const (
	providerRuntimeMaxIdentities = 10000
	providerRuntimeMaxModels     = 512
)

// ProviderRuntimeSnapshot is intentionally redacted. It contains no API key,
// token, cookie, header, or provider credential material.
type ProviderRuntimeSnapshot struct {
	Provider        string               `json:"provider"`
	AuthIndex       string               `json:"auth_index,omitempty"`
	Identity        string               `json:"identity"`
	Supported       bool                 `json:"supported"`
	Reason          string               `json:"reason,omitempty"`
	Active          int                  `json:"active"`
	Limit           int                  `json:"limit"`
	InputTokens     int64                `json:"input_tokens"`
	OutputTokens    int64                `json:"output_tokens"`
	ReasoningTokens int64                `json:"reasoning_tokens"`
	CachedTokens    int64                `json:"cached_tokens"`
	TotalTokens     int64                `json:"total_tokens"`
	AmountUSD       float64              `json:"amount_usd"`
	RatedRequests   int64                `json:"rated_requests"`
	UnratedRequests int64                `json:"unrated_requests"`
	Models          []ProviderModelUsage `json:"models,omitempty"`
	UpdatedAt       time.Time            `json:"updated_at"`
}

type ProviderModelUsage struct {
	Model           string  `json:"model"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	CachedTokens    int64   `json:"cached_tokens"`
	TotalTokens     int64   `json:"total_tokens"`
	AmountUSD       float64 `json:"amount_usd"`
	Rated           bool    `json:"rated"`
	RatedRequests   int64   `json:"rated_requests"`
	UnratedRequests int64   `json:"unrated_requests"`
}

type providerRuntimeRequest struct {
	AggregateKey string
}

type providerRuntimeModel struct {
	ProviderModelUsage
}

type providerRuntimeAggregate struct {
	Provider        string
	AuthIndex       string
	Identity        string
	Active          int
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	CachedTokens    int64
	TotalTokens     int64
	AmountNanos     int64
	RatedRequests   int64
	UnratedRequests int64
	Models          map[string]*providerRuntimeModel
	UpdatedAt       time.Time
}

// ProviderRuntimeTracker observes request lifecycle and usage callbacks without
// participating in routing or admission. Missing CPA identities are exposed as
// unsupported rather than being guessed into a configured channel.
type ProviderRuntimeTracker struct {
	mu         sync.RWMutex
	requests   map[string]providerRuntimeRequest
	aggregates map[string]*providerRuntimeAggregate
	calculator UsageCreditCalculator
	now        func() time.Time
}

func NewProviderRuntimeTracker(calculator UsageCreditCalculator) *ProviderRuntimeTracker {
	return &ProviderRuntimeTracker{
		requests:   make(map[string]providerRuntimeRequest),
		aggregates: make(map[string]*providerRuntimeAggregate),
		calculator: calculator,
		now:        time.Now,
	}
}

// RequestInterceptionActive keeps the lifecycle observer attached even when no
// mutating request experiment is enabled. It never changes request bodies.
func (t *ProviderRuntimeTracker) RequestInterceptionActive() bool              { return t != nil }
func (t *ProviderRuntimeTracker) RequestInterceptionAcceptsFormat(string) bool { return t != nil }
func (t *ProviderRuntimeTracker) InterceptRequest(request cpaapi.RequestInterceptRequest) (cpaapi.RequestInterceptResponse, bool) {
	t.ObserveRequest(request)
	return cpaapi.RequestInterceptResponse{}, false
}

func (t *ProviderRuntimeTracker) ObserveRequest(request cpaapi.RequestInterceptRequest) {
	if t == nil || strings.TrimSpace(request.RequestID) == "" {
		return
	}
	identity, authIndex := runtimeIdentityFromMetadata(request.Metadata)
	if identity == "" {
		return
	}
	provider := runtimeProviderFromMetadata(request.Metadata)
	if provider == "" {
		provider = strings.TrimSpace(request.ToFormat)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.requests[request.RequestID]; exists {
		return
	}
	aggregateKey := runtimeAggregateKey(provider, identity)
	t.requests[request.RequestID] = providerRuntimeRequest{AggregateKey: aggregateKey}
	aggregate := t.ensureAggregateLocked(aggregateKey, identity, provider, authIndex)
	aggregate.Active++
	aggregate.UpdatedAt = t.now().UTC()
}

func (t *ProviderRuntimeTracker) Complete(completion cpaapi.RequestCompletion) {
	if t == nil || strings.TrimSpace(completion.RequestID) == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	admission, exists := t.requests[completion.RequestID]
	if !exists {
		return
	}
	delete(t.requests, completion.RequestID)
	if aggregate := t.aggregates[admission.AggregateKey]; aggregate != nil {
		if aggregate.Active > 0 {
			aggregate.Active--
		}
		aggregate.UpdatedAt = t.now().UTC()
	}
}

func (t *ProviderRuntimeTracker) ObserveUsage(record cpaapi.UsageRecord) {
	if t == nil {
		return
	}
	identity, authIndex := runtimeIdentityFromUsage(record)
	if identity == "" {
		return
	}
	now := t.now().UTC()
	charge := CreditCharge{}
	if t.calculator != nil {
		charge = t.calculator.Calculate(record)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	aggregateKey := runtimeAggregateKey(strings.TrimSpace(record.Provider), identity)
	if len(t.aggregates) >= providerRuntimeMaxIdentities {
		if _, exists := t.aggregates[aggregateKey]; !exists {
			return
		}
	}
	provider := strings.TrimSpace(record.Provider)
	aggregate := t.ensureAggregateLocked(aggregateKey, identity, provider, authIndex)
	input := nonNegative(record.Detail.InputTokens)
	output := nonNegative(record.Detail.OutputTokens)
	reasoning := nonNegative(record.Detail.ReasoningTokens)
	cached := nonNegative(record.Detail.CachedTokens)
	total := nonNegative(record.Detail.TotalTokens)
	if total == 0 {
		total = saturatingAdd(saturatingAdd(input, output), reasoning)
	}
	aggregate.InputTokens = saturatingAdd(aggregate.InputTokens, input)
	aggregate.OutputTokens = saturatingAdd(aggregate.OutputTokens, output)
	aggregate.ReasoningTokens = saturatingAdd(aggregate.ReasoningTokens, reasoning)
	aggregate.CachedTokens = saturatingAdd(aggregate.CachedTokens, cached)
	aggregate.TotalTokens = saturatingAdd(aggregate.TotalTokens, total)
	model := strings.TrimSpace(record.Model)
	if model != "" {
		if aggregate.Models == nil {
			aggregate.Models = make(map[string]*providerRuntimeModel)
		}
		if entry := aggregate.Models[model]; entry != nil || len(aggregate.Models) < providerRuntimeMaxModels {
			if entry == nil {
				entry = &providerRuntimeModel{ProviderModelUsage: ProviderModelUsage{Model: model}}
				aggregate.Models[model] = entry
			}
			entry.InputTokens = saturatingAdd(entry.InputTokens, input)
			entry.OutputTokens = saturatingAdd(entry.OutputTokens, output)
			entry.ReasoningTokens = saturatingAdd(entry.ReasoningTokens, reasoning)
			entry.CachedTokens = saturatingAdd(entry.CachedTokens, cached)
			entry.TotalTokens = saturatingAdd(entry.TotalTokens, total)
			if !record.Failed {
				if charge.Rated {
					entry.Rated = true
					entry.RatedRequests++
				} else if charge.Enabled {
					entry.UnratedRequests++
				}
			}
			if charge.Rated {
				entry.AmountUSD += float64(charge.AmountNanos) / creditNanosPerUSD
			}
		}
	}
	if !record.Failed {
		if charge.Rated {
			aggregate.RatedRequests++
			aggregate.AmountNanos = saturatingAdd(aggregate.AmountNanos, charge.AmountNanos)
		} else if charge.Enabled {
			aggregate.UnratedRequests++
		}
	}
	aggregate.UpdatedAt = now
}

func (t *ProviderRuntimeTracker) Snapshot() []ProviderRuntimeSnapshot {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]ProviderRuntimeSnapshot, 0, len(t.aggregates))
	for _, aggregate := range t.aggregates {
		models := make([]ProviderModelUsage, 0, len(aggregate.Models))
		for _, model := range aggregate.Models {
			models = append(models, model.ProviderModelUsage)
		}
		sort.Slice(models, func(i, j int) bool { return models[i].Model < models[j].Model })
		supported := aggregate.Identity != ""
		reason := ""
		if !supported {
			reason = "provider_runtime_identity_unavailable"
		}
		out = append(out, ProviderRuntimeSnapshot{Provider: aggregate.Provider, AuthIndex: aggregate.AuthIndex, Identity: aggregate.Identity, Supported: supported, Reason: reason, Active: aggregate.Active, Limit: 0, InputTokens: aggregate.InputTokens, OutputTokens: aggregate.OutputTokens, ReasoningTokens: aggregate.ReasoningTokens, CachedTokens: aggregate.CachedTokens, TotalTokens: aggregate.TotalTokens, AmountUSD: float64(aggregate.AmountNanos) / creditNanosPerUSD, RatedRequests: aggregate.RatedRequests, UnratedRequests: aggregate.UnratedRequests, Models: models, UpdatedAt: aggregate.UpdatedAt})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider == out[j].Provider {
			return out[i].AuthIndex < out[j].AuthIndex
		}
		return out[i].Provider < out[j].Provider
	})
	return out
}

func (t *ProviderRuntimeTracker) Shutdown() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.requests = make(map[string]providerRuntimeRequest)
	t.aggregates = make(map[string]*providerRuntimeAggregate)
	t.mu.Unlock()
}

func runtimeAggregateKey(provider, identity string) string {
	return strings.TrimSpace(provider) + "\x00" + identity
}

func (t *ProviderRuntimeTracker) ensureAggregateLocked(key, identity, provider, authIndex string) *providerRuntimeAggregate {
	aggregate := t.aggregates[key]
	if aggregate == nil {
		aggregate = &providerRuntimeAggregate{Provider: provider, AuthIndex: authIndex, Identity: identity, Models: make(map[string]*providerRuntimeModel)}
		t.aggregates[key] = aggregate
	}
	if aggregate.Provider == "" {
		aggregate.Provider = provider
	}
	if aggregate.AuthIndex == "" {
		aggregate.AuthIndex = authIndex
	}
	return aggregate
}

func runtimeProviderFromMetadata(metadata map[string]any) string {
	for _, key := range []string{"provider", "selected_provider", "provider_name", "auth_provider"} {
		if value, ok := metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func runtimeIdentityFromMetadata(metadata map[string]any) (string, string) {
	for _, key := range []string{"selected_auth_index", "selected_auth_id", "auth_index", "auth_id"} {
		if value, ok := metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			value = strings.TrimSpace(value)
			if strings.Contains(key, "index") {
				return "auth-index:" + value, value
			}
			return "auth-id:" + value, ""
		}
	}
	return "", ""
}

func runtimeIdentityFromUsage(record cpaapi.UsageRecord) (string, string) {
	if authIndex := strings.TrimSpace(record.AuthIndex); authIndex != "" {
		return "auth-index:" + authIndex, authIndex
	}
	if authID := strings.TrimSpace(record.AuthID); authID != "" {
		return "auth-id:" + authID, ""
	}
	provider, key := strings.TrimSpace(record.Provider), strings.TrimSpace(record.APIKey)
	if provider == "" || key == "" {
		return "", ""
	}
	digest := sha256.Sum256([]byte(provider + "\x00" + key))
	return "credential:" + hex.EncodeToString(digest[:]), ""
}

func (a *App) handleAIProviderRuntime() cpaapi.ManagementResponse {
	if a == nil || a.providerRuntime == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "provider runtime metrics are unavailable"})
	}
	return jsonResponse(http.StatusOK, map[string]any{"snapshots": a.providerRuntime.Snapshot(), "updated_at": time.Now().UTC()})
}

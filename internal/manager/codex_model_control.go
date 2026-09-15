package manager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// Codex model control is a plugin-side, request-time switch that disables one or
// more Codex models for every Codex account and every Codex AI-provider channel
// at once. The host's own model policy is config-time and per account, so it
// cannot express "this model is unavailable everywhere, right now"; this gate
// runs in the request path and answers a disabled model with a deterministic
// rejection instead of forwarding it upstream.

const (
	codexModelControlStoreFile = "codex-model-control.json"
	codexModelControlVersion   = 1
	codexModelControlMaxModels = 512

	codexModelDisabledCode    = "codex_model_disabled"
	codexModelDisabledSource  = "plugin_model_control"
	codexModelDisabledMessage = "this model is disabled for Codex by the account config manager"
)

// CodexModelControlRow is one model id known to the Codex family, with the price
// the plugin's own accounting uses for it (USD per million tokens).
type CodexModelControlRow struct {
	ID       string `json:"id"`
	Disabled bool   `json:"disabled"`
	// Accounts counts the Codex provider identities that reported traffic for the
	// model; Channels counts the Codex AI-provider channels that list it.
	Accounts                   int     `json:"accounts"`
	Channels                   int     `json:"channels"`
	Priced                     bool    `json:"priced"`
	InputUSDPerMillion         float64 `json:"input_usd_per_million,omitempty"`
	OutputUSDPerMillion        float64 `json:"output_usd_per_million,omitempty"`
	CacheReadUSDPerMillion     float64 `json:"cache_read_usd_per_million,omitempty"`
	CacheCreationUSDPerMillion float64 `json:"cache_creation_usd_per_million,omitempty"`
	// LongContextThresholdTokens and the multipliers are part of the same table and
	// explain why a long request costs more.
	LongContextThresholdTokens  int64   `json:"long_context_threshold_tokens,omitempty"`
	LongContextInputMultiplier  float64 `json:"long_context_input_multiplier,omitempty"`
	LongContextOutputMultiplier float64 `json:"long_context_output_multiplier,omitempty"`
}

// CodexModelControlSnapshot is the redacted state exposed to the UI.
type CodexModelControlSnapshot struct {
	Models       []CodexModelControlRow `json:"models"`
	Disabled     []string               `json:"disabled"`
	StorageError string                 `json:"storage_error,omitempty"`
	// Pricing provenance lets the UI label where the displayed prices come from.
	PricingSource    string    `json:"pricing_source,omitempty"`
	PricingUpdatedAt time.Time `json:"pricing_updated_at,omitempty"`
}

// CodexModelControlService stores the disabled model list.
type CodexModelControlService struct {
	mu         sync.RWMutex
	store      string
	loaded     bool
	disabled   map[string]struct{}
	updatedAt  time.Time
	storageErr string
}

func NewCodexModelControlService() *CodexModelControlService {
	return &CodexModelControlService{disabled: map[string]struct{}{}}
}

func codexModelControlStorePath(dataDir string) string {
	return filepath.Join(dataDir, codexModelControlStoreFile)
}

func (s *CodexModelControlService) Configure(config Config) {
	if s == nil {
		return
	}
	path := codexModelControlStorePath(normalizeConfig(config).DataDir)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded && s.store == path {
		return
	}
	s.store, s.loaded = path, true
	raw, errRead := os.ReadFile(path)
	switch {
	case errRead == nil:
		var persisted persistedCodexModelControl
		if errDecode := json.Unmarshal(raw, &persisted); errDecode != nil || persisted.Version != codexModelControlVersion {
			s.storageErr = "Codex model control state could not be read"
			s.disabled = map[string]struct{}{}
			return
		}
		s.disabled = normalizeCodexModelIDs(persisted.Disabled)
		s.updatedAt = persisted.UpdatedAt
		s.storageErr = ""
	case errors.Is(errRead, os.ErrNotExist):
		s.disabled = map[string]struct{}{}
		s.storageErr = ""
	default:
		s.storageErr = "Codex model control state could not be read"
	}
}

// Set replaces the disabled list. Models are matched case-insensitively.
func (s *CodexModelControlService) Set(disabled []string) (CodexModelControlSnapshot, error) {
	if s == nil {
		return CodexModelControlSnapshot{}, ErrCodexModelControlStorageUnavailable
	}
	s.mu.Lock()
	if strings.TrimSpace(s.store) == "" {
		s.mu.Unlock()
		return CodexModelControlSnapshot{}, ErrCodexModelControlStorageUnavailable
	}
	s.disabled = normalizeCodexModelIDs(disabled)
	s.updatedAt = time.Now().UTC()
	errPersist := s.persistLocked()
	s.mu.Unlock()
	snapshot := s.Snapshot()
	if errPersist != nil {
		return snapshot, errPersist
	}
	return snapshot, nil
}

// Disabled reports whether one model id is blocked. It is called per request, so
// it takes only a read lock and does no allocation beyond the lookup key.
func (s *CodexModelControlService) Disabled(model string) bool {
	if s == nil {
		return false
	}
	key := normalizeCodexModelID(model)
	if key == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, disabled := s.disabled[key]
	return disabled
}

func (s *CodexModelControlService) Count() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.disabled)
}

// Snapshot reports the disabled ids. The known-model rows are merged by the app,
// which is the only place that can see accounts, channels and observed traffic.
func (s *CodexModelControlService) Snapshot() CodexModelControlSnapshot {
	snapshot := CodexModelControlSnapshot{Models: []CodexModelControlRow{}, Disabled: []string{}}
	if s == nil {
		return snapshot
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot.StorageError = s.storageErr
	for id := range s.disabled {
		snapshot.Disabled = append(snapshot.Disabled, id)
	}
	sort.Strings(snapshot.Disabled)
	return snapshot
}

// DisabledSet returns a copy of the disabled ids for merging with known models.
func (s *CodexModelControlService) DisabledSet() map[string]struct{} {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	set := make(map[string]struct{}, len(s.disabled))
	for id := range s.disabled {
		set[id] = struct{}{}
	}
	return set
}

type persistedCodexModelControl struct {
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	Disabled  []string  `json:"disabled,omitempty"`
}

func (s *CodexModelControlService) persistLocked() error {
	persisted := persistedCodexModelControl{Version: codexModelControlVersion, UpdatedAt: s.updatedAt}
	for id := range s.disabled {
		persisted.Disabled = append(persisted.Disabled, id)
	}
	sort.Strings(persisted.Disabled)
	encoded, errEncode := json.Marshal(persisted)
	if errEncode != nil {
		return errEncode
	}
	if errMkdir := os.MkdirAll(filepath.Dir(s.store), 0o700); errMkdir != nil {
		s.storageErr = "Codex model control state could not be saved"
		return errMkdir
	}
	if errWrite := writePrivateFileAtomically(s.store, encoded); errWrite != nil {
		s.storageErr = "Codex model control state could not be saved"
		return errWrite
	}
	s.storageErr = ""
	return nil
}

// ErrCodexModelControlStorageUnavailable reports that the service has no store.
var ErrCodexModelControlStorageUnavailable = errors.New("Codex model control storage is unavailable")

func normalizeCodexModelID(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

func normalizeCodexModelIDs(models []string) map[string]struct{} {
	normalized := make(map[string]struct{}, len(models))
	for _, model := range models {
		key := normalizeCodexModelID(model)
		if key == "" || len(key) > maxAccountConfigIDLength {
			continue
		}
		if len(normalized) >= codexModelControlMaxModels {
			break
		}
		normalized[key] = struct{}{}
	}
	return normalized
}

// CodexModelControl is the request-time gate. It only inspects Codex requests, so
// a model id shared with another provider family is unaffected there.
type CodexModelControl struct {
	service *CodexModelControlService
}

func NewCodexModelControl(service *CodexModelControlService) *CodexModelControl {
	return &CodexModelControl{service: service}
}

// RequestInterceptionActive keeps the gate attached only while something is
// actually disabled, so an untouched installation pays nothing.
func (c *CodexModelControl) RequestInterceptionActive() bool {
	return c != nil && c.service != nil && c.service.Count() > 0
}

// RequestInterceptionAcceptsFormat accepts every format: whether a request is a
// Codex request is decided per request from the formats CPA reports.
func (c *CodexModelControl) RequestInterceptionAcceptsFormat(string) bool { return c != nil }

func (c *CodexModelControl) InterceptRequest(request cpaapi.RequestInterceptRequest) (cpaapi.RequestInterceptResponse, bool) {
	if c == nil || c.service == nil || c.service.Count() == 0 {
		return cpaapi.RequestInterceptResponse{}, false
	}
	if !isCodexFamilyRequest(request) {
		return cpaapi.RequestInterceptResponse{}, false
	}
	model := strings.TrimSpace(firstNonEmpty(request.Model, request.RequestedModel))
	if model == "" || !c.service.Disabled(model) {
		return cpaapi.RequestInterceptResponse{}, false
	}
	body, errMarshal := json.Marshal(map[string]any{"error": map[string]any{
		"message": codexModelDisabledMessage,
		"code":    codexModelDisabledCode,
		"source":  codexModelDisabledSource,
		"model":   model,
	}})
	if errMarshal != nil {
		return cpaapi.RequestInterceptResponse{}, false
	}
	return cpaapi.RequestInterceptResponse{
		Terminate:       true,
		StatusCode:      http.StatusForbidden,
		ResponseHeaders: http.Header{"Content-Type": []string{"application/json"}},
		ResponseBody:    body,
	}, true
}

// isCodexFamilyRequest reports whether CPA routed this request to Codex. The
// format is authoritative; the metadata providers are a fallback for hosts that
// report the provider explicitly.
func isCodexFamilyRequest(request cpaapi.RequestInterceptRequest) bool {
	if strings.EqualFold(strings.TrimSpace(request.ToFormat), "codex") ||
		strings.EqualFold(strings.TrimSpace(request.SourceFormat), "codex") {
		return true
	}
	provider := runtimeProviderFromMetadata(request.Metadata)
	return provider == "codex"
}

// codexModelControlRows merges the disabled list with every Codex model the
// plugin can see, so the operator can disable a model that has not been used yet.
func (a *App) codexModelControlRows() []CodexModelControlRow {
	rows := map[string]*CodexModelControlRow{}
	ensure := func(id string) *CodexModelControlRow {
		key := normalizeCodexModelID(id)
		if key == "" {
			return nil
		}
		row, ok := rows[key]
		if !ok {
			row = &CodexModelControlRow{ID: key}
			rows[key] = row
		}
		return row
	}
	if a == nil {
		return nil
	}
	if a.codexModelControl != nil {
		for id := range a.codexModelControl.DisabledSet() {
			if row := ensure(id); row != nil {
				row.Disabled = true
			}
		}
	}
	// Models with observed Codex traffic, grouped by provider identity.
	if a.providerRuntime != nil {
		for _, snapshot := range a.providerRuntime.Snapshot() {
			if !providerIsCodexFamily(snapshot.Provider) {
				continue
			}
			for _, usage := range snapshot.Models {
				row := ensure(usage.Model)
				if row == nil {
					continue
				}
				row.Accounts++
			}
		}
	}
	// Models configured on Codex AI-provider channels.
	if channels := a.cachedCodexChannelModels(); len(channels) > 0 {
		for id, count := range channels {
			if row := ensure(id); row != nil {
				row.Channels += count
			}
		}
	}
	// Models the Codex accounts actually list in their effective catalogs. The
	// catalog count and the observed-identity count are both per account, so the
	// larger one is reported instead of double counting a shared account.
	for id, count := range a.cachedCodexAccountModels() {
		row := ensure(id)
		if row == nil {
			continue
		}
		if count > row.Accounts {
			row.Accounts = count
		}
	}
	list := make([]CodexModelControlRow, 0, len(rows))
	for _, row := range rows {
		pricing, priced := a.codexModelPriceFor(row.ID)
		applyCodexModelPrice(row, pricing, priced)
		list = append(list, *row)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}

// codexModelPriceFor resolves one model against the plugin's price table using the
// same lookup the credit accounting uses.
func (a *App) codexModelPriceFor(model string) (creditModelPricing, bool) {
	if a == nil || a.creditUsage == nil {
		return creditModelPricing{}, false
	}
	return a.creditUsage.PriceForModel(model)
}

// applyCodexModelPrice converts the per-token rates the billing path uses into the
// per-million figures an operator reads, so the displayed price always matches the
// rate the plugin charges for the same model.
func applyCodexModelPrice(row *CodexModelControlRow, pricing creditModelPricing, priced bool) {
	if row == nil || !priced {
		return
	}
	row.Priced = true
	row.InputUSDPerMillion = pricing.Input * 1_000_000
	row.OutputUSDPerMillion = pricing.Output * 1_000_000
	row.CacheReadUSDPerMillion = pricing.CacheRead * 1_000_000
	row.CacheCreationUSDPerMillion = pricing.CacheCreation * 1_000_000
	row.LongContextThresholdTokens = pricing.LongContextThreshold
	if pricing.LongContextInputMultiplier > 1 {
		row.LongContextInputMultiplier = pricing.LongContextInputMultiplier
	}
	if pricing.LongContextOutputMultiplier > 1 {
		row.LongContextOutputMultiplier = pricing.LongContextOutputMultiplier
	}
}

// codexPricingProvenance reports the price table's source and last refresh.
func (a *App) codexPricingProvenance() (time.Time, string) {
	if a == nil || a.creditUsage == nil {
		return time.Time{}, ""
	}
	return a.creditUsage.Provenance()
}

func providerIsCodexFamily(provider string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(provider)), "codex")
}

// codexChannelModelsCache holds the last Codex inventory scan so the models tab
// does not call CPA on every refresh. One scan records both the codex-api-key
// channels and the effective model catalogs of the Codex accounts.
var codexChannelModelsMu sync.Mutex

type codexChannelModelsCache struct {
	models map[string]int
	// channels counts the codex-api-key channel entries of the last successful scan.
	channels int
	// accountModels maps a model id to the number of Codex accounts whose
	// effective catalog lists it. It keeps the last successful scan, so a failed
	// rescan never empties the model table.
	accountModels map[string]int
	// fetchedAt stamps the channel half and accountScannedAt the account half:
	// the two halves refresh on independent TTLs.
	fetchedAt        time.Time
	accountScannedAt time.Time
}

var codexChannelModelsState codexChannelModelsCache

const codexChannelModelsTTL = 30 * time.Second

// cachedCodexChannelModels returns model id -> channel count for Codex channels.
func (a *App) cachedCodexChannelModels() map[string]int {
	if a == nil {
		return nil
	}
	codexChannelModelsMu.Lock()
	defer codexChannelModelsMu.Unlock()
	// A stale scan is still better than blocking the UI on a CPA call.
	return codexChannelModelsState.models
}

// cachedCodexChannelEntryCount returns the codex-api-key channel count of the last
// inventory scan. It stays zero until the first successful scan.
func (a *App) cachedCodexChannelEntryCount() int {
	if a == nil {
		return 0
	}
	codexChannelModelsMu.Lock()
	defer codexChannelModelsMu.Unlock()
	return codexChannelModelsState.channels
}

// cachedCodexAccountModels returns model id -> Codex account count from the last
// inventory scan.
func (a *App) cachedCodexAccountModels() map[string]int {
	if a == nil {
		return nil
	}
	codexChannelModelsMu.Lock()
	defer codexChannelModelsMu.Unlock()
	return codexChannelModelsState.accountModels
}

// codexInventoryNeedsAccountScan reports whether the catalog half of the inventory
// has never run or is older than the TTL. It is keyed on its own timestamp, so a
// tab that keeps re-reading the channel half cannot starve the catalog half.
func codexInventoryNeedsAccountScan() bool {
	codexChannelModelsMu.Lock()
	defer codexChannelModelsMu.Unlock()
	return codexChannelModelsState.accountScannedAt.IsZero() ||
		time.Since(codexChannelModelsState.accountScannedAt) >= codexChannelModelsTTL
}

// cachedCodexAccountScan returns the last account catalog scan with its timestamp.
func (a *App) cachedCodexAccountScan() (map[string]int, time.Time) {
	if a == nil {
		return nil, time.Time{}
	}
	codexChannelModelsMu.Lock()
	defer codexChannelModelsMu.Unlock()
	return codexChannelModelsState.accountModels, codexChannelModelsState.accountScannedAt
}

// codexInventoryAccounts lists the Codex-family host credentials with distinct
// ids. A failed host listing degrades to no accounts instead of failing the
// caller, because the workspace must still render its other counts.
func (a *App) codexInventoryAccounts(ctx context.Context) []Account {
	if a == nil || a.accounts == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	accounts, errList := a.accounts.baseAccounts(ctx)
	if errList != nil {
		return nil
	}
	seen := make(map[string]struct{}, len(accounts))
	codex := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		if !providerIsCodexFamily(account.Provider) && !providerIsCodexFamily(account.Type) {
			continue
		}
		// The projection id is the host auth index; fall back to the other identity
		// fields only when a host omits it, and never count one credential twice.
		identity := firstNonEmpty(strings.TrimSpace(account.ID), strings.TrimSpace(account.AuthID), strings.TrimSpace(account.Name))
		if identity == "" {
			continue
		}
		if _, exists := seen[identity]; exists {
			continue
		}
		seen[identity] = struct{}{}
		codex = append(codex, account)
	}
	return codex
}

// codexAccountModelCounts loads the effective model catalog of every Codex
// account and returns model id -> account count. It runs inside the caller's
// bounded context and never fails the caller: an incomplete scan degrades to the
// catalogs it could read (nil when it could read none).
func (a *App) codexAccountModelCounts(ctx context.Context, managementKey string) map[string]int {
	if a == nil || a.accounts == nil {
		return nil
	}
	targets := a.codexInventoryAccounts(ctx)
	if len(targets) == 0 {
		return nil
	}
	// Cap the fan-out at the same target limit the catalog route enforces, so a
	// huge host listing cannot start unbounded per-account work.
	if len(targets) > maxModelCatalogTargets {
		targets = targets[:maxModelCatalogTargets]
	}
	config := a.configSnapshot()
	client, errClient := newManagementClient(resolveManagementBaseURL(config.ManagementBaseURL), managementKey, a.managementDoer)
	if errClient != nil {
		return nil
	}
	defer client.clearSecrets()
	catalogs, _ := loadCommonAccountModels(ctx, a.accounts, targets, client, config.Workers)
	return countCodexAccountModels(catalogs)
}

// countCodexAccountModels turns per-account catalogs into a model id -> account
// count map, counting a model once per account even when a catalog repeats it.
func countCodexAccountModels(catalogs [][]AccountModelOption) map[string]int {
	counts := map[string]int{}
	for _, catalog := range catalogs {
		seen := make(map[string]struct{}, len(catalog))
		for _, option := range catalog {
			key := normalizeCodexModelID(option.ID)
			if key == "" {
				continue
			}
			seen[key] = struct{}{}
		}
		for key := range seen {
			counts[key]++
		}
	}
	if len(counts) == 0 {
		return nil
	}
	return counts
}

// refreshCodexChannelModels runs one Codex inventory scan: it reads the
// codex-api-key channels and the effective model catalog of every Codex account,
// then stores both counts behind one shared TTL. A failed channel read keeps the
// previous scan, and a failed or partial account read degrades to the models it
// could read.
func (a *App) refreshCodexChannelModels(ctx context.Context, managementKey string) map[string]int {
	if a == nil {
		return nil
	}
	entries, errRead := a.aiProviderChannelEntries(ctx, managementKey, "codex-api-key")
	if errRead != nil {
		return a.cachedCodexChannelModels()
	}
	models := map[string]int{}
	for _, entry := range entries {
		seen := map[string]struct{}{}
		if list, ok := entry["models"].([]any); ok {
			for _, item := range list {
				record, isRecord := item.(map[string]any)
				if !isRecord {
					continue
				}
				for _, field := range []string{"name", "alias"} {
					if value, isText := record[field].(string); isText {
						key := normalizeCodexModelID(value)
						if key != "" {
							seen[key] = struct{}{}
						}
					}
				}
			}
		}
		if list, ok := entry["excluded_models"].([]any); ok {
			for _, item := range list {
				if value, isText := item.(string); isText {
					if key := normalizeCodexModelID(value); key != "" {
						seen[key] = struct{}{}
					}
				}
			}
		}
		for id := range seen {
			models[id]++
		}
	}
	// The per-account catalog scan is the expensive half of the inventory, so it
	// runs at most once per TTL. A failed scan keeps the previous rows and still
	// stamps accountScannedAt, which bounds the next retry to the TTL.
	accountModels, accountScannedAt := a.cachedCodexAccountScan()
	if codexInventoryNeedsAccountScan() {
		if scanned := a.codexAccountModelCounts(ctx, managementKey); scanned != nil {
			accountModels = scanned
		}
		accountScannedAt = time.Now()
	}
	codexChannelModelsMu.Lock()
	codexChannelModelsState = codexChannelModelsCache{models: models, channels: len(entries), accountModels: accountModels, fetchedAt: time.Now(), accountScannedAt: accountScannedAt}
	codexChannelModelsMu.Unlock()
	return models
}

// codexOverview reports the counts the Codex workspace shows. The account count
// comes from the host auth listing and the channel count from the shared channel
// scan cache, because the provider-runtime tracker deliberately excludes native
// OAuth accounts and their traffic.
func (a *App) codexOverview(ctx context.Context) map[string]any {
	overview := map[string]any{
		"accounts":                      0,
		"channels":                      0,
		"disabled_models":               0,
		"convergence_mode":              "",
		"account_overrides":             0,
		"provider_overrides":            0,
		"fingerprint_overridden_fields": 0,
		"model_control_active":          false,
	}
	if a == nil {
		return overview
	}
	overview["accounts"] = len(a.codexInventoryAccounts(ctx))
	overview["channels"] = a.cachedCodexChannelEntryCount()
	if a.codexModelControl != nil {
		overview["disabled_models"] = a.codexModelControl.Count()
		overview["model_control_active"] = a.codexModelControl.Count() > 0
	}
	if a.codexFingerprints != nil {
		overview["fingerprint_overridden_fields"] = a.codexFingerprints.OverriddenFields()
		overview["convergence_mode"] = a.codexFingerprints.EffectiveValues().mode
	}
	if a.codexIdentityOverrides != nil {
		snapshot := a.codexIdentityOverrides.Snapshot()
		overview["account_overrides"] = len(snapshot.Accounts)
		overview["provider_overrides"] = len(snapshot.Providers)
	}
	return overview
}

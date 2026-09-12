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

// OpenCode model control is a plugin-side, request-time switch that disables one
// or more OpenCode models for every OpenCode account and every OpenCode
// AI-provider channel at once. The host's own model policy is config-time and
// per account, so it cannot express "this model is unavailable everywhere, right
// now"; this gate runs in the request path and answers a disabled model with a
// deterministic rejection instead of forwarding it upstream. The intended use is
// stopping GPT-class models from consuming OpenCode Go resources, but the control
// itself is model-id agnostic.
const (
	openCodeModelControlStoreFile = "opencode-model-control.json"
	openCodeModelControlVersion   = 1
	openCodeModelControlMaxModels = 512

	openCodeModelDisabledCode    = "opencode_model_disabled"
	openCodeModelDisabledSource  = "plugin_model_control"
	openCodeModelDisabledMessage = "this model is disabled for OpenCode by the account config manager"
)

// OpenCodeModelControlRow is one model id known to the OpenCode family.
type OpenCodeModelControlRow struct {
	ID       string `json:"id"`
	Disabled bool   `json:"disabled"`
	// Accounts counts the OpenCode credentials whose cached catalog publishes the
	// model; Channels counts the OpenCode AI-provider channels that list it.
	Accounts int `json:"accounts"`
	Channels int `json:"channels"`
}

// OpenCodeModelControlSnapshot is the redacted state exposed to the UI.
type OpenCodeModelControlSnapshot struct {
	Models       []OpenCodeModelControlRow `json:"models"`
	Disabled     []string                  `json:"disabled"`
	StorageError string                    `json:"storage_error,omitempty"`
	// Pricing provenance lets the UI label the price table behind the rows.
	PricingSource    string    `json:"pricing_source,omitempty"`
	PricingUpdatedAt time.Time `json:"pricing_updated_at,omitempty"`
}

// OpenCodeModelControlService stores the disabled model list.
type OpenCodeModelControlService struct {
	mu         sync.RWMutex
	store      string
	loaded     bool
	disabled   map[string]struct{}
	updatedAt  time.Time
	storageErr string
}

func NewOpenCodeModelControlService() *OpenCodeModelControlService {
	return &OpenCodeModelControlService{disabled: map[string]struct{}{}}
}

func openCodeModelControlStorePath(dataDir string) string {
	return filepath.Join(dataDir, openCodeModelControlStoreFile)
}

func (s *OpenCodeModelControlService) Configure(config Config) {
	if s == nil {
		return
	}
	path := openCodeModelControlStorePath(normalizeConfig(config).DataDir)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded && s.store == path {
		return
	}
	s.store, s.loaded = path, true
	raw, errRead := os.ReadFile(path)
	switch {
	case errRead == nil:
		var persisted persistedOpenCodeModelControl
		if errDecode := json.Unmarshal(raw, &persisted); errDecode != nil || persisted.Version != openCodeModelControlVersion {
			s.storageErr = "OpenCode model control state could not be read"
			s.disabled = map[string]struct{}{}
			return
		}
		s.disabled = normalizeOpenCodeModelIDs(persisted.Disabled)
		s.updatedAt = persisted.UpdatedAt
		s.storageErr = ""
	case errors.Is(errRead, os.ErrNotExist):
		s.disabled = map[string]struct{}{}
		s.storageErr = ""
	default:
		s.storageErr = "OpenCode model control state could not be read"
	}
}

// Set replaces the disabled list. Models are matched prefix- and
// separator-insensitively, exactly like the session router folds a routed model.
func (s *OpenCodeModelControlService) Set(disabled []string) (OpenCodeModelControlSnapshot, error) {
	if s == nil {
		return OpenCodeModelControlSnapshot{}, ErrOpenCodeModelControlStorageUnavailable
	}
	s.mu.Lock()
	if strings.TrimSpace(s.store) == "" {
		s.mu.Unlock()
		return OpenCodeModelControlSnapshot{}, ErrOpenCodeModelControlStorageUnavailable
	}
	s.disabled = normalizeOpenCodeModelIDs(disabled)
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
// it takes only a read lock and no allocation beyond the lookup key.
func (s *OpenCodeModelControlService) Disabled(model string) bool {
	if s == nil {
		return false
	}
	key := normalizeOpenCodeSessionModel(model)
	if key == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, disabled := s.disabled[key]
	return disabled
}

func (s *OpenCodeModelControlService) Count() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.disabled)
}

// Snapshot reports the disabled ids. The known-model rows are merged by the app,
// which is the only place that can see accounts and channels.
func (s *OpenCodeModelControlService) Snapshot() OpenCodeModelControlSnapshot {
	snapshot := OpenCodeModelControlSnapshot{Models: []OpenCodeModelControlRow{}, Disabled: []string{}}
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
func (s *OpenCodeModelControlService) DisabledSet() map[string]struct{} {
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

type persistedOpenCodeModelControl struct {
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	Disabled  []string  `json:"disabled,omitempty"`
}

func (s *OpenCodeModelControlService) persistLocked() error {
	persisted := persistedOpenCodeModelControl{Version: openCodeModelControlVersion, UpdatedAt: s.updatedAt}
	for id := range s.disabled {
		persisted.Disabled = append(persisted.Disabled, id)
	}
	sort.Strings(persisted.Disabled)
	encoded, errEncode := json.Marshal(persisted)
	if errEncode != nil {
		return errEncode
	}
	if errMkdir := os.MkdirAll(filepath.Dir(s.store), 0o700); errMkdir != nil {
		s.storageErr = "OpenCode model control state could not be saved"
		return errMkdir
	}
	if errWrite := writePrivateFileAtomically(s.store, encoded); errWrite != nil {
		s.storageErr = "OpenCode model control state could not be saved"
		return errWrite
	}
	s.storageErr = ""
	return nil
}

// ErrOpenCodeModelControlStorageUnavailable reports that the service has no store.
var ErrOpenCodeModelControlStorageUnavailable = errors.New("OpenCode model control storage is unavailable")

func normalizeOpenCodeModelIDs(models []string) map[string]struct{} {
	normalized := make(map[string]struct{}, len(models))
	for _, model := range models {
		key := normalizeOpenCodeSessionModel(model)
		if key == "" || len(key) > maxAccountConfigIDLength {
			continue
		}
		if len(normalized) >= openCodeModelControlMaxModels {
			break
		}
		normalized[key] = struct{}{}
	}
	return normalized
}

// OpenCodeModelControl is the request-time gate. It blocks only requests the
// plugin can attribute to OpenCode, using the same layered attribution as the
// session router, so a model id OpenCode resells (for example a GPT-class id) is
// never blocked on another provider's traffic.
type OpenCodeModelControl struct {
	service *OpenCodeModelControlService

	mu sync.RWMutex
	// authIndexes is the channel allow-list copied from the app's refresh, and
	// targets is the OpenCode model catalog. Both are replaced wholesale.
	authIndexes map[string]struct{}
	targets     map[string]struct{}
}

func NewOpenCodeModelControl(service *OpenCodeModelControlService) *OpenCodeModelControl {
	return &OpenCodeModelControl{
		service:     service,
		authIndexes: map[string]struct{}{},
		targets:     map[string]struct{}{},
	}
}

// SetAuthIndexes replaces the authoritative allow-list of CPA auth indexes that
// belong to OpenCode channels. Indexes are assigned per channel key entry by CPA
// and can be regenerated when a channel is edited, so the app refreshes them.
func (c *OpenCodeModelControl) SetAuthIndexes(indexes []string) {
	if c == nil {
		return
	}
	allowed := make(map[string]struct{}, len(indexes))
	for _, index := range indexes {
		normalized := normalizeOpenCodeSessionAuthIndex(index)
		if normalized == "" {
			continue
		}
		if len(allowed) >= openCodeSessionMaxTargets {
			break
		}
		allowed[normalized] = struct{}{}
	}
	c.mu.Lock()
	c.authIndexes = allowed
	c.mu.Unlock()
}

// SetTargets replaces the set of models published by OpenCode with normalized,
// deduplicated ids. The set is bounded so a bad catalog cannot grow memory
// without limit.
func (c *OpenCodeModelControl) SetTargets(models []string) {
	if c == nil {
		return
	}
	targets := make(map[string]struct{}, len(models))
	for _, model := range models {
		normalized := normalizeOpenCodeSessionModel(model)
		if normalized == "" {
			continue
		}
		if _, exists := targets[normalized]; exists {
			continue
		}
		if len(targets) >= openCodeSessionMaxTargets {
			break
		}
		targets[normalized] = struct{}{}
	}
	c.mu.Lock()
	c.targets = targets
	c.mu.Unlock()
}

// RequestInterceptionActive keeps the gate attached only while something is
// actually disabled, so an untouched installation pays nothing.
func (c *OpenCodeModelControl) RequestInterceptionActive() bool {
	return c != nil && c.service != nil && c.service.Count() > 0
}

// RequestInterceptionAcceptsFormat accepts every format: whether a request is an
// OpenCode request is decided per request from the auth index and the catalog.
func (c *OpenCodeModelControl) RequestInterceptionAcceptsFormat(string) bool { return c != nil }

// InterceptRequest rejects a disabled model when the request is attributed to
// OpenCode and the gate is active.
func (c *OpenCodeModelControl) InterceptRequest(request cpaapi.RequestInterceptRequest) (cpaapi.RequestInterceptResponse, bool) {
	if c == nil || c.service == nil || c.service.Count() == 0 {
		return cpaapi.RequestInterceptResponse{}, false
	}
	model := normalizeOpenCodeSessionModel(firstNonEmpty(request.Model, request.RequestedModel))
	if model == "" || !c.service.Disabled(model) {
		return cpaapi.RequestInterceptResponse{}, false
	}
	if !c.attributedToOpenCode(request, model) {
		return cpaapi.RequestInterceptResponse{}, false
	}
	body, errMarshal := json.Marshal(map[string]any{"error": map[string]any{
		"message": openCodeModelDisabledMessage,
		"code":    openCodeModelDisabledCode,
		"source":  openCodeModelDisabledSource,
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

// attributedToOpenCode applies the session router's layered rule: a reported auth
// index that is in the OpenCode channel allow-list is authoritative. Otherwise
// the model must be published by OpenCode and the request must not be Codex
// traffic, which is the only family whose model ids overlap with the catalog.
func (c *OpenCodeModelControl) attributedToOpenCode(request cpaapi.RequestInterceptRequest, model string) bool {
	authIndex := normalizeOpenCodeSessionAuthIndex(openCodeSessionAuthIndexFromMetadata(request.Metadata))
	c.mu.RLock()
	_, allowedIndex := c.authIndexes[authIndex]
	channelListKnown := len(c.authIndexes) > 0
	_, catalogModel := c.targets[model]
	c.mu.RUnlock()
	switch {
	case authIndex != "" && channelListKnown:
		// The channel allow-list is authoritative: the request provably belongs to
		// an OpenCode channel, or to a different one.
		return allowedIndex
	case isCodexFamilyRequest(request):
		return false
	default:
		return catalogModel
	}
}

// openCodeModelControlRows merges the disabled list with every OpenCode model the
// plugin can see, so the operator can disable a model that has not been used yet.
func (a *App) openCodeModelControlRows() []OpenCodeModelControlRow {
	rows := map[string]*OpenCodeModelControlRow{}
	ensure := func(id string) *OpenCodeModelControlRow {
		key := normalizeOpenCodeSessionModel(id)
		if key == "" {
			return nil
		}
		row, ok := rows[key]
		if !ok {
			row = &OpenCodeModelControlRow{ID: key}
			rows[key] = row
		}
		return row
	}
	if a == nil {
		return nil
	}
	if a.opencodeModelControl != nil {
		for id := range a.opencodeModelControl.DisabledSet() {
			if row := ensure(id); row != nil {
				row.Disabled = true
			}
		}
	}
	// The official catalogs are the contract: a model published there is routable
	// even before any credential has cached it.
	if a.opencodePricing != nil {
		for _, id := range a.opencodePricing.ModelIDs(openCodeKindGoValue) {
			ensure(id)
		}
		for _, id := range a.opencodePricing.ModelIDs(openCodeKindZenValue) {
			ensure(id)
		}
	}
	// Credentials whose cached catalog references the model. One account counts
	// once even when its catalog repeats a model with separator drift.
	countAccounts := func(models []string) {
		seen := map[string]struct{}{}
		for _, id := range models {
			key := normalizeOpenCodeSessionModel(id)
			if key == "" {
				continue
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			if row := ensure(key); row != nil {
				row.Accounts++
			}
		}
	}
	if a.opencode != nil {
		for _, account := range a.opencode.ListAccounts() {
			countAccounts(account.Models)
		}
	}
	if a.opencodeZen != nil {
		for _, account := range a.opencodeZen.ListAccounts() {
			countAccounts(account.Models)
		}
	}
	// Channels whose live configuration lists the model. The scan is cached by the
	// management handlers, so the row builder never calls CPA by itself.
	for id, count := range a.cachedOpenCodeChannelModels() {
		if row := ensure(id); row != nil {
			row.Channels += count
		}
	}
	list := make([]OpenCodeModelControlRow, 0, len(rows))
	for _, row := range rows {
		list = append(list, *row)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}

// openCodeChannelModelsState holds the last OpenCode channel model scan so the
// management handlers do not make the row builder call CPA on every refresh.
var openCodeChannelModelsMu sync.Mutex

var openCodeChannelModelsState map[string]int

// cachedOpenCodeChannelModels returns model id -> channel count for OpenCode
// channels, or nil when no scan has succeeded yet.
func (a *App) cachedOpenCodeChannelModels() map[string]int {
	if a == nil {
		return nil
	}
	openCodeChannelModelsMu.Lock()
	defer openCodeChannelModelsMu.Unlock()
	return openCodeChannelModelsState
}

// refreshOpenCodeChannelModels scans the CPA OpenCode AI-provider channels and
// stores the model ids they list. A failed read keeps the previous scan.
func (a *App) refreshOpenCodeChannelModels(ctx context.Context, managementKey string) map[string]int {
	if a == nil {
		return nil
	}
	entries, errRead := a.aiProviderChannelEntries(ctx, managementKey, "openai-compatibility")
	if errRead != nil {
		return a.cachedOpenCodeChannelModels()
	}
	models := map[string]int{}
	for _, entry := range entries {
		if _, ok := openCodeChannelKindForEntry(entry); !ok {
			continue
		}
		seen := map[string]struct{}{}
		if list, ok := entry["models"].([]any); ok {
			for _, item := range list {
				record, isRecord := item.(map[string]any)
				if !isRecord {
					continue
				}
				for _, field := range []string{"name", "alias"} {
					if value, isText := record[field].(string); isText {
						key := normalizeOpenCodeSessionModel(value)
						if key != "" {
							seen[key] = struct{}{}
						}
					}
				}
			}
		}
		for id := range seen {
			models[id]++
		}
	}
	openCodeChannelModelsMu.Lock()
	openCodeChannelModelsState = models
	openCodeChannelModelsMu.Unlock()
	return models
}

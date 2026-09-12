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

// CodexModelControlRow is one model id known to the Codex family.
type CodexModelControlRow struct {
	ID       string `json:"id"`
	Disabled bool   `json:"disabled"`
	// Accounts counts the Codex provider identities that reported traffic for the
	// model; Channels counts the Codex AI-provider channels that list it.
	Accounts int `json:"accounts"`
	Channels int `json:"channels"`
}

// CodexModelControlSnapshot is the redacted state exposed to the UI.
type CodexModelControlSnapshot struct {
	Models       []CodexModelControlRow `json:"models"`
	Disabled     []string               `json:"disabled"`
	StorageError string                 `json:"storage_error,omitempty"`
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
	list := make([]CodexModelControlRow, 0, len(rows))
	for _, row := range rows {
		list = append(list, *row)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}

func providerIsCodexFamily(provider string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(provider)), "codex")
}

// codexChannelModelsCache holds the last channel model scan so the models tab does
// not call CPA on every refresh.
var codexChannelModelsMu sync.Mutex

type codexChannelModelsCache struct {
	models    map[string]int
	fetchedAt time.Time
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
	if codexChannelModelsState.models == nil {
		return nil
	}
	if time.Since(codexChannelModelsState.fetchedAt) >= codexChannelModelsTTL {
		// A stale scan is still better than blocking the UI on a CPA call.
		return codexChannelModelsState.models
	}
	return codexChannelModelsState.models
}

// refreshCodexChannelModels scans the CPA Codex channels and stores the model
// ids they list. A failed read keeps the previous scan.
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
	codexChannelModelsMu.Lock()
	codexChannelModelsState = codexChannelModelsCache{models: models, fetchedAt: time.Now()}
	codexChannelModelsMu.Unlock()
	return models
}

// codexOverview reports the counts the Codex workspace shows.
func (a *App) codexOverview() map[string]any {
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
	if a.providerRuntime != nil {
		counted := map[string]struct{}{}
		channels := map[string]struct{}{}
		for _, snapshot := range a.providerRuntime.Snapshot() {
			if !providerIsCodexFamily(snapshot.Provider) {
				continue
			}
			identity := strings.TrimSpace(snapshot.Identity)
			if identity == "" {
				identity = snapshot.AuthIndex
			}
			counted[snapshot.Provider+"\x00"+identity] = struct{}{}
			if snapshot.CredentialBacked {
				channels[snapshot.Provider+"\x00"+identity] = struct{}{}
			}
		}
		overview["accounts"] = len(counted)
		overview["channels"] = len(channels)
	}
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

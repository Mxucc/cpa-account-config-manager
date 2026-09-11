package manager

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	aiProviderNameStoreVersion       = 2
	aiProviderNameStoreLegacyVersion = 1
	aiProviderNameStoreFile          = "ai-provider-names.json"
	aiProviderNameMaxLength          = 120
	aiProviderNameMaxEntries         = 512
	aiProviderNameMaxIdentities      = 8
	aiProviderNameDigestBytes        = 16
	aiProviderNamePepperBytes        = 32
)

var ErrAIProviderNameStorageUnavailable = errors.New("AI provider name storage is unavailable")

// AIProviderNameSnapshot reports the resolved channel bindings for the live
// channel list plus a sanitized storage error when the state could not persist.
type AIProviderNameSnapshot struct {
	Names        []AIProviderNameAssignment `json:"names"`
	StorageError string                     `json:"storage_error,omitempty"`
}

// aiProviderNameBinding is the plugin-managed record for one channel. CPA has no
// name field for channel entries, so the operator label and the usage identities
// observed for the channel live here, keyed by an irreversible digest of the
// channel base URL and credential.
type aiProviderNameBinding struct {
	Name       string    `json:"name,omitempty"`
	BaseURL    string    `json:"base_url,omitempty"`
	Provider   string    `json:"provider,omitempty"`
	Identities []string  `json:"identities,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// aiProviderNameEntry is the version 1 layout, kept so an existing store can be
// migrated into bindings without losing labels.
type aiProviderNameEntry struct {
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updated_at"`
}

type persistedAIProviderNames struct {
	Version  int                              `json:"version"`
	Pepper   string                           `json:"pepper,omitempty"`
	Channels map[string]aiProviderNameBinding `json:"channels,omitempty"`
	Names    map[string]aiProviderNameEntry   `json:"names,omitempty"`
}

// AIProviderNameService stores plugin-managed per-channel data (the operator
// label and the usage identities bound to the channel) for AI provider channels.
// Keys are an irreversible keyed digest of the channel base URL and credential:
// a changed base URL or a changed key produces a different key, which keeps two
// similar providers apart, while a unique base URL can adopt the previous record
// so history follows a rotated credential.
type AIProviderNameService struct {
	mu         sync.RWMutex
	store      string
	loaded     bool
	storageErr string
	pepper     []byte
	bindings   map[string]aiProviderNameBinding
}

func NewAIProviderNameService() *AIProviderNameService {
	return &AIProviderNameService{bindings: make(map[string]aiProviderNameBinding)}
}

func aiProviderNameStorePath(dataDir string) string {
	return filepath.Join(dataDir, aiProviderNameStoreFile)
}

func (s *AIProviderNameService) Configure(config Config) {
	if s == nil {
		return
	}
	path := aiProviderNameStorePath(normalizeConfig(config).DataDir)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded && s.store == path {
		return
	}
	s.store, s.loaded = path, true
	persisted, errLoad := loadAIProviderNames(path)
	if errLoad != nil {
		s.bindings, s.pepper = make(map[string]aiProviderNameBinding), nil
		if errors.Is(errLoad, os.ErrNotExist) {
			s.storageErr = ""
		} else {
			s.storageErr = "AI provider name state could not be loaded"
		}
	} else {
		s.bindings, s.storageErr = persisted.Channels, ""
		s.pepper = decodeAIProviderNamePepper(persisted.Pepper)
	}
	if len(s.pepper) == 0 {
		// The keyed digest needs a per-installation secret. Generate and persist
		// it before the first read so keys stay comparable across restarts.
		pepper, errPepper := generateAIProviderNamePepper()
		if errPepper != nil {
			return
		}
		s.pepper = pepper
		if errPersist := s.persistLocked(); errPersist != nil {
			s.storageErr = "AI provider name state could not be persisted"
		}
	}
}

// Snapshot exposes the stored bindings for diagnostics and tests. Keys are
// digests, so the map carries no credential material.
func (s *AIProviderNameService) Snapshot() map[string]aiProviderNameBinding {
	if s == nil {
		return map[string]aiProviderNameBinding{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneAIProviderNameBindings(s.bindings)
}

func (s *AIProviderNameService) StorageError() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.storageErr
}

// noteStorageError records a sanitized persistence failure under the store lock.
func (s *AIProviderNameService) noteStorageError(message string) {
	if s == nil || strings.TrimSpace(message) == "" {
		return
	}
	s.mu.Lock()
	s.storageErr = message
	s.mu.Unlock()
}

// CredentialKey derives the primary binding key for one channel entry. The digest
// covers the provider kind, the base URL, and the credential together.
func (s *AIProviderNameService) CredentialKey(kind, baseURL, credential string) string {
	return aiProviderCredentialNameKey(s.pepperSnapshot(), kind, baseURL, credential)
}

// URLKey derives the fallback binding key used for entries without a credential
// and as the rotation fallback when a base URL is unique.
func (s *AIProviderNameService) URLKey(kind, baseURL string) string {
	return aiProviderURLNameKey(s.pepperSnapshot(), kind, baseURL)
}

func (s *AIProviderNameService) pepperSnapshot() []byte {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pepper
}

// Binding returns a copy of the stored binding for one key.
func (s *AIProviderNameService) Binding(key string) (aiProviderNameBinding, bool) {
	if s == nil {
		return aiProviderNameBinding{}, false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return aiProviderNameBinding{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	binding, exists := s.bindings[key]
	return cloneAIProviderNameBinding(binding), exists
}

func (s *AIProviderNameService) Name(key string) (string, bool) {
	binding, exists := s.Binding(key)
	if !exists || strings.TrimSpace(binding.Name) == "" {
		return "", false
	}
	return binding.Name, true
}

// Names resolves the first matching label for the supplied candidate keys.
func (s *AIProviderNameService) Names(keys []string) (string, bool) {
	for _, key := range keys {
		if name, ok := s.Name(key); ok {
			return name, true
		}
	}
	return "", false
}

// UpsertBinding records the live characteristics of one channel: its canonical
// base URL, provider family, and the usage identity currently observed for it.
// The operator label is preserved unless Assign changes it, and identities are
// accumulated (bounded) so usage history survives a credential rotation.
func (s *AIProviderNameService) UpsertBinding(key, baseURL, provider, identity string) error {
	if s == nil {
		return ErrAIProviderNameStorageUnavailable
	}
	key = strings.TrimSpace(key)
	if !validAIProviderNameKey(key) {
		return fmt.Errorf("AI provider channel key is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(s.store) == "" || len(s.pepper) == 0 {
		return ErrAIProviderNameStorageUnavailable
	}
	binding := s.bindings[key]
	applyAIProviderBindingFields(&binding, baseURL, provider, identity)
	s.bindings[key] = binding
	evicted := s.evictOldestLocked()
	if errSave := s.persistLocked(); errSave != nil {
		for droppedKey, dropped := range evicted {
			s.bindings[droppedKey] = dropped
		}
		s.storageErr = "AI provider name state could not be persisted"
		return fmt.Errorf("%w: %v", ErrAIProviderNameStorageUnavailable, errSave)
	}
	s.storageErr = ""
	return nil
}

// AdoptBinding merges the record stored under fromKey into toKey and removes
// fromKey. It is used when a credential changed but the base URL still uniquely
// identifies the channel, so the label and usage history follow the new key.
func (s *AIProviderNameService) AdoptBinding(fromKey, toKey string) error {
	if s == nil {
		return ErrAIProviderNameStorageUnavailable
	}
	fromKey, toKey = strings.TrimSpace(fromKey), strings.TrimSpace(toKey)
	if fromKey == "" || toKey == "" || fromKey == toKey {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(s.store) == "" || len(s.pepper) == 0 {
		return ErrAIProviderNameStorageUnavailable
	}
	source, exists := s.bindings[fromKey]
	if !exists {
		return nil
	}
	target := s.bindings[toKey]
	if strings.TrimSpace(target.Name) == "" && strings.TrimSpace(source.Name) != "" {
		target.Name = source.Name
	}
	if strings.TrimSpace(target.BaseURL) == "" {
		target.BaseURL = source.BaseURL
	}
	if strings.TrimSpace(target.Provider) == "" {
		target.Provider = source.Provider
	}
	for _, identity := range source.Identities {
		target.Identities = appendAIProviderIdentity(target.Identities, identity)
	}
	if target.UpdatedAt.IsZero() || source.UpdatedAt.After(target.UpdatedAt) {
		target.UpdatedAt = source.UpdatedAt
	}
	target.UpdatedAt = time.Now().UTC()
	s.bindings[toKey] = target
	delete(s.bindings, fromKey)
	if errSave := s.persistLocked(); errSave != nil {
		delete(s.bindings, toKey)
		s.bindings[fromKey] = source
		s.storageErr = "AI provider name state could not be persisted"
		return fmt.Errorf("%w: %v", ErrAIProviderNameStorageUnavailable, errSave)
	}
	s.storageErr = ""
	return nil
}

// Assign stores one label under every supplied key, or clears them when the name
// is empty. Assignments are transactional: a failed write restores the previous
// state instead of leaving the store half-updated.
func (s *AIProviderNameService) Assign(keys []string, name string) (string, error) {
	if s == nil {
		return "", ErrAIProviderNameStorageUnavailable
	}
	normalizedKeys := normalizeAIProviderNameKeys(keys)
	if len(normalizedKeys) == 0 {
		return "", fmt.Errorf("at least one AI provider name key is required")
	}
	normalizedName, errName := normalizeAIProviderName(name)
	if errName != nil {
		return "", errName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(s.store) == "" || len(s.pepper) == 0 {
		return "", ErrAIProviderNameStorageUnavailable
	}
	previous := make(map[string]*aiProviderNameBinding, len(normalizedKeys))
	for _, key := range normalizedKeys {
		if binding, exists := s.bindings[key]; exists {
			cloned := binding
			previous[key] = &cloned
			continue
		}
		previous[key] = nil
	}
	now := time.Now().UTC()
	for _, key := range normalizedKeys {
		binding := s.bindings[key]
		if normalizedName == "" {
			binding.Name = ""
			binding.UpdatedAt = now
			if aiProviderBindingEmpty(binding) {
				delete(s.bindings, key)
				continue
			}
			s.bindings[key] = binding
			continue
		}
		binding.Name = normalizedName
		binding.UpdatedAt = now
		s.bindings[key] = binding
	}
	evicted := s.evictOldestLocked()
	if errSave := s.persistLocked(); errSave != nil {
		// Restore both this assignment and anything the cap evicted: a failed write
		// must not drop records that were already stored.
		for key, binding := range previous {
			if binding == nil {
				delete(s.bindings, key)
				continue
			}
			s.bindings[key] = *binding
		}
		for key, binding := range evicted {
			s.bindings[key] = binding
		}
		s.storageErr = "AI provider name state could not be persisted"
		return "", fmt.Errorf("%w: %v", ErrAIProviderNameStorageUnavailable, errSave)
	}
	s.storageErr = ""
	return normalizedName, nil
}

// PruneKind drops stored bindings for one provider kind whose keys no longer
// match any live channel entry, so a deleted channel cannot hand its label or
// usage history to a future entry. Keys outside the kind are never touched.
func (s *AIProviderNameService) PruneKind(kind string, keep map[string]struct{}) error {
	if s == nil {
		return nil
	}
	prefix := strings.TrimSpace(kind) + ":"
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := make(map[string]aiProviderNameBinding)
	for key, binding := range s.bindings {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if _, exists := keep[key]; exists {
			continue
		}
		removed[key] = binding
		delete(s.bindings, key)
	}
	if len(removed) == 0 {
		return nil
	}
	if errSave := s.persistLocked(); errSave != nil {
		// The prune is best effort: keep the records in memory when the state could
		// not be persisted, so a failed write cannot silently discard them.
		for key, binding := range removed {
			s.bindings[key] = binding
		}
		s.storageErr = "AI provider name state could not be persisted"
		return fmt.Errorf("%w: %v", ErrAIProviderNameStorageUnavailable, errSave)
	}
	s.storageErr = ""
	return nil
}

// evictOldestLocked trims the store back to its cap and returns the dropped
// records so the caller can restore them when persistence fails.
func (s *AIProviderNameService) evictOldestLocked() map[string]aiProviderNameBinding {
	evicted := make(map[string]aiProviderNameBinding)
	if len(s.bindings) <= aiProviderNameMaxEntries {
		return evicted
	}
	type keyedBinding struct {
		key string
		at  time.Time
	}
	ordered := make([]keyedBinding, 0, len(s.bindings))
	for key, binding := range s.bindings {
		ordered = append(ordered, keyedBinding{key: key, at: binding.UpdatedAt})
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].at.Equal(ordered[j].at) {
			return ordered[i].key < ordered[j].key
		}
		return ordered[i].at.Before(ordered[j].at)
	})
	for _, candidate := range ordered[:len(ordered)-aiProviderNameMaxEntries] {
		if current, exists := s.bindings[candidate.key]; exists {
			evicted[candidate.key] = current
		}
		delete(s.bindings, candidate.key)
	}
	return evicted
}

func (s *AIProviderNameService) persistLocked() error {
	return savePrivateJSON(s.store, persistedAIProviderNames{
		Version:  aiProviderNameStoreVersion,
		Pepper:   hex.EncodeToString(s.pepper),
		Channels: cloneAIProviderNameBindings(s.bindings),
	})
}

func loadAIProviderNames(path string) (persistedAIProviderNames, error) {
	var persisted persistedAIProviderNames
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return persisted, errRead
	}
	if errDecode := json.Unmarshal(raw, &persisted); errDecode != nil {
		return persisted, errDecode
	}
	switch persisted.Version {
	case aiProviderNameStoreLegacyVersion:
		// Version 1 stored bare labels; keep them as bindings without the channel
		// characteristics that only a live read can supply.
		channels := make(map[string]aiProviderNameBinding, len(persisted.Names))
		for key, entry := range persisted.Names {
			if !validAIProviderNameKey(key) {
				continue
			}
			normalizedName, errName := normalizeAIProviderName(entry.Name)
			if errName != nil || normalizedName == "" {
				continue
			}
			channels[strings.TrimSpace(key)] = aiProviderNameBinding{Name: normalizedName, UpdatedAt: entry.UpdatedAt}
		}
		persisted.Version = aiProviderNameStoreVersion
		persisted.Channels = channels
		persisted.Names = nil
		return persisted, nil
	case aiProviderNameStoreVersion:
	default:
		return persisted, fmt.Errorf("unsupported AI provider name store version %d", persisted.Version)
	}
	channels := make(map[string]aiProviderNameBinding, len(persisted.Channels))
	for key, binding := range persisted.Channels {
		if !validAIProviderNameKey(key) {
			continue
		}
		if normalizedName, errName := normalizeAIProviderName(binding.Name); errName == nil {
			binding.Name = normalizedName
		} else {
			binding.Name = ""
		}
		binding.BaseURL = canonicalProviderBaseURL(binding.BaseURL)
		binding.Provider = strings.TrimSpace(binding.Provider)
		binding.Identities = normalizeAIProviderIdentities(binding.Identities)
		if aiProviderBindingEmpty(binding) {
			continue
		}
		channels[strings.TrimSpace(key)] = binding
	}
	persisted.Channels = channels
	persisted.Names = nil
	return persisted, nil
}

func applyAIProviderBindingFields(binding *aiProviderNameBinding, baseURL, provider, identity string) {
	if binding == nil {
		return
	}
	if canonical := canonicalProviderBaseURL(baseURL); canonical != "" {
		binding.BaseURL = canonical
	}
	if trimmed := strings.TrimSpace(provider); trimmed != "" {
		binding.Provider = trimmed
	}
	binding.Identities = appendAIProviderIdentity(binding.Identities, identity)
	binding.UpdatedAt = time.Now().UTC()
}

// appendAIProviderIdentity adds one usage identity while keeping the list bounded
// and duplicate free; the newest entry is kept last so the cap drops the oldest.
func appendAIProviderIdentity(values []string, identity string) []string {
	identity = strings.TrimSpace(identity)
	if identity == "" || len(identity) > maxAccountConfigIDLength {
		return normalizeAIProviderIdentities(values)
	}
	normalized := make([]string, 0, len(values)+1)
	for _, value := range normalizeAIProviderIdentities(values) {
		if value != identity {
			normalized = append(normalized, value)
		}
	}
	normalized = append(normalized, identity)
	if len(normalized) > aiProviderNameMaxIdentities {
		normalized = normalized[len(normalized)-aiProviderNameMaxIdentities:]
	}
	return normalized
}

func normalizeAIProviderIdentities(values []string) []string {
	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || len(trimmed) > maxAccountConfigIDLength {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	if len(normalized) > aiProviderNameMaxIdentities {
		normalized = normalized[len(normalized)-aiProviderNameMaxIdentities:]
	}
	return normalized
}

func aiProviderBindingEmpty(binding aiProviderNameBinding) bool {
	return strings.TrimSpace(binding.Name) == "" && strings.TrimSpace(binding.BaseURL) == "" &&
		strings.TrimSpace(binding.Provider) == "" && len(normalizeAIProviderIdentities(binding.Identities)) == 0
}

func generateAIProviderNamePepper() ([]byte, error) {
	pepper := make([]byte, aiProviderNamePepperBytes)
	if _, errRead := rand.Read(pepper); errRead != nil {
		return nil, fmt.Errorf("generate AI provider name pepper: %w", errRead)
	}
	return pepper, nil
}

func decodeAIProviderNamePepper(encoded string) []byte {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil
	}
	decoded, errDecode := hex.DecodeString(encoded)
	if errDecode != nil || len(decoded) != aiProviderNamePepperBytes {
		return nil
	}
	return decoded
}

// aiProviderCredentialNameKey binds the provider kind, base URL, and credential
// into one irreversible digest. A changed base URL or a changed key therefore
// produces a different key, which is what keeps two similar providers apart.
func aiProviderCredentialNameKey(pepper []byte, kind, baseURL, credential string) string {
	return strings.TrimSpace(kind) + ":cred:" + aiProviderNameDigest(pepper, "credential", kind, canonicalProviderBaseURL(baseURL), credential)
}

// aiProviderURLNameKey is the fallback identity for entries without any
// credential, and the rotation fallback that is only honoured when it matches
// exactly one live entry.
func aiProviderURLNameKey(pepper []byte, kind, baseURL string) string {
	return strings.TrimSpace(kind) + ":url:" + aiProviderNameDigest(pepper, "base-url", kind, canonicalProviderBaseURL(baseURL))
}

func aiProviderNameDigest(pepper []byte, parts ...string) string {
	mac := hmac.New(sha256.New, pepper)
	for _, part := range parts {
		_, _ = mac.Write([]byte(part))
		_, _ = mac.Write([]byte{0})
	}
	sum := mac.Sum(nil)
	return hex.EncodeToString(sum[:aiProviderNameDigestBytes])
}

func canonicalProviderBaseURL(value string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(value)), "/")
}

func normalizeAIProviderNameKeys(keys []string) []string {
	normalized := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		trimmed := strings.TrimSpace(key)
		if !validAIProviderNameKey(trimmed) {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	return normalized
}

func validAIProviderNameKey(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > maxAccountConfigIDLength {
		return false
	}
	for _, char := range key {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

// normalizeAIProviderName trims, strips control characters, and collapses
// whitespace so a pasted label cannot inject markup or line breaks.
func normalizeAIProviderName(name string) (string, error) {
	if !utf8.ValidString(name) {
		return "", fmt.Errorf("AI provider name must be valid UTF-8")
	}
	cleaned := strings.Map(func(char rune) rune {
		if unicode.IsControl(char) {
			return ' '
		}
		return char
	}, name)
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	if utf8.RuneCountInString(cleaned) > aiProviderNameMaxLength {
		return "", fmt.Errorf("AI provider name must be at most %d characters", aiProviderNameMaxLength)
	}
	return cleaned, nil
}

func cloneAIProviderNameBindings(values map[string]aiProviderNameBinding) map[string]aiProviderNameBinding {
	clone := make(map[string]aiProviderNameBinding, len(values))
	for key, binding := range values {
		clone[key] = cloneAIProviderNameBinding(binding)
	}
	return clone
}

func cloneAIProviderNameBinding(binding aiProviderNameBinding) aiProviderNameBinding {
	if len(binding.Identities) > 0 {
		binding.Identities = append([]string(nil), binding.Identities...)
	}
	return binding
}

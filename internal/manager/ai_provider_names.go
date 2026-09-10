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
	aiProviderNameStoreVersion = 1
	aiProviderNameStoreFile    = "ai-provider-names.json"
	aiProviderNameMaxLength    = 120
	aiProviderNameMaxEntries   = 512
	aiProviderNameDigestBytes  = 16
	aiProviderNamePepperBytes  = 32
)

var ErrAIProviderNameStorageUnavailable = errors.New("AI provider name storage is unavailable")

// AIProviderNameSnapshot reports the resolved operator labels for the live
// channel list plus a sanitized storage error when the state could not persist.
type AIProviderNameSnapshot struct {
	Names        []AIProviderNameAssignment `json:"names"`
	StorageError string                     `json:"storage_error,omitempty"`
}

type aiProviderNameEntry struct {
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updated_at"`
}

type persistedAIProviderNames struct {
	Version int                            `json:"version"`
	Pepper  string                         `json:"pepper,omitempty"`
	Names   map[string]aiProviderNameEntry `json:"names,omitempty"`
}

// AIProviderNameService stores operator labels for AI provider channels. CPA
// returns every channel kind as one positional array and drops an unknown name
// field, so a label is keyed by an irreversible keyed digest of the channel's
// base URL and credential. Both must match, which prevents two providers that
// share only a base URL or only a key from being confused with each other.
type AIProviderNameService struct {
	mu         sync.RWMutex
	store      string
	loaded     bool
	storageErr string
	pepper     []byte
	names      map[string]aiProviderNameEntry
}

func NewAIProviderNameService() *AIProviderNameService {
	return &AIProviderNameService{names: make(map[string]aiProviderNameEntry)}
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
		s.names, s.pepper = make(map[string]aiProviderNameEntry), nil
		if errors.Is(errLoad, os.ErrNotExist) {
			s.storageErr = ""
		} else {
			s.storageErr = "AI provider name state could not be loaded"
		}
	} else {
		s.names, s.storageErr = persisted.Names, ""
		s.pepper = decodeAIProviderNamePepper(persisted.Pepper)
	}
	if len(s.pepper) == 0 {
		// The keyed digest needs a per-installation secret. Generate and persist
		// it before the first read so names stay comparable across restarts.
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

// Snapshot exposes the stored labels for diagnostics and tests. Keys are
// digests, so the map carries no credential material.
func (s *AIProviderNameService) Snapshot() map[string]string {
	if s == nil {
		return map[string]string{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.names))
	for key, entry := range s.names {
		out[key] = entry.Name
	}
	return out
}

func (s *AIProviderNameService) StorageError() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.storageErr
}

// CredentialKey derives the primary label key for one channel entry. The digest
// covers the provider kind, the base URL, and the credential together.
func (s *AIProviderNameService) CredentialKey(kind, baseURL, credential string) string {
	return aiProviderCredentialNameKey(s.pepperSnapshot(), kind, baseURL, credential)
}

// URLKey derives the fallback label key used only by entries that expose no
// credential at all.
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

func (s *AIProviderNameService) Name(key string) (string, bool) {
	if s == nil {
		return "", false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, exists := s.names[key]
	if !exists || entry.Name == "" {
		return "", false
	}
	return entry.Name, true
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

// Assign stores one label under every supplied key, or clears them when the
// name is empty. Assignments are transactional: a failed write restores the
// previous state instead of leaving the store half-updated.
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
	previous := make(map[string]*aiProviderNameEntry, len(normalizedKeys))
	for _, key := range normalizedKeys {
		if entry, exists := s.names[key]; exists {
			cloned := entry
			previous[key] = &cloned
			continue
		}
		previous[key] = nil
	}
	now := time.Now().UTC()
	for _, key := range normalizedKeys {
		if normalizedName == "" {
			delete(s.names, key)
			continue
		}
		s.names[key] = aiProviderNameEntry{Name: normalizedName, UpdatedAt: now}
	}
	s.evictOldestLocked()
	if errSave := s.persistLocked(); errSave != nil {
		for key, entry := range previous {
			if entry == nil {
				delete(s.names, key)
				continue
			}
			s.names[key] = *entry
		}
		s.storageErr = "AI provider name state could not be persisted"
		return "", fmt.Errorf("%w: %v", ErrAIProviderNameStorageUnavailable, errSave)
	}
	s.storageErr = ""
	return normalizedName, nil
}

// PruneKind drops stored labels for one provider kind whose keys no longer
// match any live channel entry, so a deleted or rotated provider cannot hand
// its label to a future entry. Keys outside the kind are never touched.
func (s *AIProviderNameService) PruneKind(kind string, keep map[string]struct{}) error {
	if s == nil {
		return nil
	}
	prefix := strings.TrimSpace(kind) + ":"
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := false
	for key := range s.names {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if _, exists := keep[key]; exists {
			continue
		}
		delete(s.names, key)
		removed = true
	}
	if !removed {
		return nil
	}
	if errSave := s.persistLocked(); errSave != nil {
		s.storageErr = "AI provider name state could not be persisted"
		return fmt.Errorf("%w: %v", ErrAIProviderNameStorageUnavailable, errSave)
	}
	s.storageErr = ""
	return nil
}

func (s *AIProviderNameService) evictOldestLocked() {
	if len(s.names) <= aiProviderNameMaxEntries {
		return
	}
	type keyedEntry struct {
		key string
		at  time.Time
	}
	ordered := make([]keyedEntry, 0, len(s.names))
	for key, entry := range s.names {
		ordered = append(ordered, keyedEntry{key: key, at: entry.UpdatedAt})
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].at.Equal(ordered[j].at) {
			return ordered[i].key < ordered[j].key
		}
		return ordered[i].at.Before(ordered[j].at)
	})
	for _, entry := range ordered[:len(ordered)-aiProviderNameMaxEntries] {
		delete(s.names, entry.key)
	}
}

func (s *AIProviderNameService) persistLocked() error {
	return savePrivateJSON(s.store, persistedAIProviderNames{
		Version: aiProviderNameStoreVersion,
		Pepper:  hex.EncodeToString(s.pepper),
		Names:   cloneAIProviderNameEntries(s.names),
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
	if persisted.Version != aiProviderNameStoreVersion {
		return persisted, fmt.Errorf("unsupported AI provider name store version %d", persisted.Version)
	}
	names := make(map[string]aiProviderNameEntry, len(persisted.Names))
	for key, entry := range persisted.Names {
		if !validAIProviderNameKey(key) {
			continue
		}
		normalizedName, errName := normalizeAIProviderName(entry.Name)
		if errName != nil || normalizedName == "" {
			continue
		}
		names[strings.TrimSpace(key)] = aiProviderNameEntry{Name: normalizedName, UpdatedAt: entry.UpdatedAt}
	}
	persisted.Names = names
	return persisted, nil
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
// credential. It is only honoured when it matches exactly one live entry.
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

func cloneAIProviderNameEntries(names map[string]aiProviderNameEntry) map[string]aiProviderNameEntry {
	clone := make(map[string]aiProviderNameEntry, len(names))
	for key, entry := range names {
		clone[key] = entry
	}
	return clone
}

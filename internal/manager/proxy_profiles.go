package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	proxyProfilesStoreVersion = 1
	maxProxyProfileNameLength = 128
	maxProxyProfileNoteLength = 2000
	maxProxyProfileProviders  = 64
)

var (
	ErrProxyProfileNotFound = errors.New("proxy profile was not found")
	ErrProxyProfileInUse    = errors.New("proxy profile is still assigned to accounts")
	errPrivateFileMissing   = errors.New("private file is missing")
)

type ProxyProfile struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	ProxyURL  string    `json:"proxy_url"`
	Note      string    `json:"note,omitempty"`
	Providers []string  `json:"providers,omitempty"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ProxyProfileView struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	ProxyURLMasked string    `json:"proxy_url_masked"`
	Note           string    `json:"note,omitempty"`
	Providers      []string  `json:"providers,omitempty"`
	Enabled        bool      `json:"enabled"`
	AccountCount   int       `json:"account_count"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type proxyProfileBinding struct {
	AccountID string `json:"account_id"`
	ProfileID string `json:"profile_id"`
}
type persistedProxyProfiles struct {
	Version  int                   `json:"version"`
	Profiles []ProxyProfile        `json:"profiles"`
	Bindings []proxyProfileBinding `json:"bindings,omitempty"`
}

// ProxyProfileResolver keeps profile consumers decoupled from storage and
// prevents raw proxy credentials from crossing service boundaries.
type ProxyProfileResolver interface {
	ProxyURLByID(id string) (string, bool)
}

// ProxyProfileScopedResolver is implemented by resolvers that can enforce a
// profile's optional provider allow-list without exposing the stored URL.
type ProxyProfileScopedResolver interface {
	ProxyURLForProvider(id, provider string) (string, bool)
}

type ProxyProfileService struct {
	mu             sync.RWMutex
	dataDir        string
	bindingApplier func(ctx context.Context, accounts []Account, proxyURL, managementKey string) error
	profiles       map[string]ProxyProfile
	bindings       map[string]string
	loaded         bool
	storageErr     string
	now            func() time.Time
}

func NewProxyProfileService() *ProxyProfileService {
	return &ProxyProfileService{profiles: map[string]ProxyProfile{}, bindings: map[string]string{}, now: time.Now}
}

func (s *ProxyProfileService) SetBindingApplier(applier func(context.Context, []Account, string, string) error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.bindingApplier = applier
	s.mu.Unlock()
}

func proxyProfilesStorePath(dataDir string) string {
	return joinDataPath(dataDir, "proxy-profiles.json")
}
func joinDataPath(dataDir, name string) string {
	if strings.TrimSpace(dataDir) == "" {
		return name
	}
	return filepath.Join(strings.TrimSpace(dataDir), name)
}

func (s *ProxyProfileService) Configure(config Config) {
	if s == nil {
		return
	}
	path := proxyProfilesStorePath(config.DataDir)
	s.mu.Lock()
	if s.loaded && s.dataDir == config.DataDir {
		s.mu.Unlock()
		return
	}
	s.dataDir = config.DataDir
	s.loaded = true
	s.storageErr = ""
	s.mu.Unlock()
	persisted, err := loadPrivateJSON(path)
	if err != nil {
		if !errors.Is(err, errPrivateFileMissing) {
			s.mu.Lock()
			s.storageErr = err.Error()
			s.mu.Unlock()
		}
		return
	}
	if persisted.Version != 0 && persisted.Version != proxyProfilesStoreVersion {
		s.mu.Lock()
		s.storageErr = fmt.Sprintf("unsupported proxy profile store version %d", persisted.Version)
		s.mu.Unlock()
		return
	}
	profiles := make(map[string]ProxyProfile, len(persisted.Profiles))
	for _, p := range persisted.Profiles {
		if p.ID != "" {
			profiles[p.ID] = p
		}
	}
	bindings := make(map[string]string, len(persisted.Bindings))
	for _, b := range persisted.Bindings {
		if b.AccountID != "" && b.ProfileID != "" {
			bindings[b.AccountID] = b.ProfileID
		}
	}
	s.mu.Lock()
	s.profiles = profiles
	s.bindings = bindings
	s.mu.Unlock()
}

// loadPrivateJSON is intentionally tiny and only used by this service.
func loadPrivateJSON(path string) (persistedProxyProfiles, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return persistedProxyProfiles{}, errPrivateFileMissing
		}
		return persistedProxyProfiles{}, err
	}
	var out persistedProxyProfiles
	if err := json.Unmarshal(b, &out); err != nil {
		return persistedProxyProfiles{}, err
	}
	return out, nil
}

func (s *ProxyProfileService) StorageError() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.storageErr
}

// ProxyURLByID returns the concrete URL only to trusted in-process consumers.
func (s *ProxyProfileService) ProxyURLByID(id string) (string, bool) {
	p, err := s.get(id)
	if err != nil || !p.Enabled {
		return "", false
	}
	return p.ProxyURL, true
}

func (s *ProxyProfileService) ProxyURLForProvider(id, provider string) (string, bool) {
	p, err := s.get(id)
	if err != nil || !p.Enabled {
		return "", false
	}
	if len(p.Providers) == 0 {
		return p.ProxyURL, true
	}
	provider = normalizeProxyProfileProvider(provider)
	for _, allowed := range p.Providers {
		if normalizeProxyProfileProvider(allowed) == provider {
			return p.ProxyURL, true
		}
	}
	return "", false
}

func resolveProxyProfileForProvider(resolver ProxyProfileResolver, id, provider string) (string, bool) {
	if scoped, ok := resolver.(ProxyProfileScopedResolver); ok {
		return scoped.ProxyURLForProvider(id, provider)
	}
	return resolver.ProxyURLByID(id)
}

func (s *ProxyProfileService) List(ctx context.Context) []ProxyProfileView {
	_ = ctx
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ProxyProfileView, 0, len(s.profiles))
	counts := map[string]int{}
	for _, id := range s.bindings {
		counts[id]++
	}
	for _, p := range s.profiles {
		out = append(out, s.viewLocked(p, counts[p.ID]))
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}
func (s *ProxyProfileService) viewLocked(p ProxyProfile, count int) ProxyProfileView {
	return ProxyProfileView{ID: p.ID, Name: p.Name, ProxyURLMasked: redactProxyURL(p.ProxyURL), Note: p.Note, Providers: append([]string(nil), p.Providers...), Enabled: p.Enabled, AccountCount: count, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}
func (s *ProxyProfileService) get(id string) (ProxyProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.profiles[strings.TrimSpace(id)]
	if !ok {
		return ProxyProfile{}, ErrProxyProfileNotFound
	}
	return p, nil
}
func (s *ProxyProfileService) Create(name, rawURL, note string, providers []string, enabled *bool) (ProxyProfileView, error) {
	p, err := normalizeProxyProfile("", name, rawURL, note, providers, enabled)
	if err != nil {
		return ProxyProfileView{}, err
	}
	id, err := randomIdentifier()
	if err != nil {
		return ProxyProfileView{}, err
	}
	p.ID = "proxy-" + id
	now := s.now().UTC()
	p.CreatedAt = now
	p.UpdatedAt = now
	s.mu.Lock()
	defer s.mu.Unlock()
	s.profiles[p.ID] = p
	if err := s.persistLocked(); err != nil {
		return ProxyProfileView{}, err
	}
	return s.viewLocked(p, 0), nil
}
func (s *ProxyProfileService) Update(id, name, rawURL, note string, providers []string, enabled *bool) (ProxyProfileView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.profiles[strings.TrimSpace(id)]
	if !ok {
		return ProxyProfileView{}, ErrProxyProfileNotFound
	}
	if strings.TrimSpace(rawURL) == "" {
		rawURL = old.ProxyURL
	}
	p, err := normalizeProxyProfile(old.ID, name, rawURL, note, providers, enabled)
	if err != nil {
		return ProxyProfileView{}, err
	}
	p.CreatedAt = old.CreatedAt
	p.UpdatedAt = s.now().UTC()
	s.profiles[p.ID] = p
	if err := s.persistLocked(); err != nil {
		return ProxyProfileView{}, err
	}
	return s.viewLocked(p, s.bindingCountLocked(p.ID)), nil
}
func (s *ProxyProfileService) Delete(id string, force bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id = strings.TrimSpace(id)
	if _, ok := s.profiles[id]; !ok {
		return ErrProxyProfileNotFound
	}
	count := s.bindingCountLocked(id)
	if count > 0 && !force {
		return ErrProxyProfileInUse
	}
	delete(s.profiles, id)
	for a, p := range s.bindings {
		if p == id {
			delete(s.bindings, a)
		}
	}
	return s.persistLocked()
}
func (s *ProxyProfileService) bindingCountLocked(id string) int {
	n := 0
	for _, p := range s.bindings {
		if p == id {
			n++
		}
	}
	return n
}
func (s *ProxyProfileService) Bind(accounts []Account, profileID string) error {
	return s.BindWithKey(accounts, profileID, "")
}

func (s *ProxyProfileService) BindWithKey(accounts []Account, profileID, managementKey string) error {
	s.mu.Lock()
	if _, ok := s.profiles[profileID]; !ok {
		s.mu.Unlock()
		return ErrProxyProfileNotFound
	}
	for _, account := range accounts {
		if account.Name != "" && !safeAuthJSONName(account.Name) {
			s.mu.Unlock()
			return fmt.Errorf("account auth file is invalid")
		}
	}
	url := s.profiles[profileID].ProxyURL
	applier := s.bindingApplier
	s.mu.Unlock()
	if applier != nil {
		if err := applier(context.Background(), accounts, url, managementKey); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range accounts {
		if a.ID != "" {
			s.bindings[a.ID] = profileID
		}
	}
	return s.persistLocked()
}
func (s *ProxyProfileService) Unbind(accounts []Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range accounts {
		delete(s.bindings, a.ID)
	}
	return s.persistLocked()
}
func (s *ProxyProfileService) persistLocked() error {
	if strings.TrimSpace(s.dataDir) == "" {
		return nil
	}
	profiles := make([]ProxyProfile, 0, len(s.profiles))
	for _, p := range s.profiles {
		profiles = append(profiles, p)
	}
	bindings := make([]proxyProfileBinding, 0, len(s.bindings))
	for a, p := range s.bindings {
		bindings = append(bindings, proxyProfileBinding{a, p})
	}
	if err := savePrivateJSON(proxyProfilesStorePath(s.dataDir), persistedProxyProfiles{Version: proxyProfilesStoreVersion, Profiles: profiles, Bindings: bindings}); err != nil {
		s.storageErr = err.Error()
		return err
	}
	s.storageErr = ""
	return nil
}

func normalizeProxyProfile(id, name, rawURL, note string, providers []string, enabled *bool) (ProxyProfile, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxProxyProfileNameLength || hasUnsafeControl(name, false) {
		return ProxyProfile{}, fmt.Errorf("profile name is invalid")
	}
	rawURL = strings.TrimSpace(rawURL)
	if err := validateProxyProfileURL(rawURL); err != nil {
		return ProxyProfile{}, err
	}
	note = strings.TrimSpace(note)
	if len(note) > maxProxyProfileNoteLength || hasUnsafeControl(note, true) {
		return ProxyProfile{}, fmt.Errorf("profile note is invalid")
	}
	seen := map[string]bool{}
	clean := make([]string, 0, len(providers))
	for _, v := range providers {
		v = normalizeProxyProfileProvider(v)
		if v == "" || seen[v] {
			continue
		}
		if len(v) > 64 {
			return ProxyProfile{}, fmt.Errorf("provider is invalid")
		}
		seen[v] = true
		clean = append(clean, v)
		if len(clean) > maxProxyProfileProviders {
			return ProxyProfile{}, fmt.Errorf("too many providers")
		}
	}
	en := true
	if enabled != nil {
		en = *enabled
	}
	return ProxyProfile{ID: id, Name: name, ProxyURL: rawURL, Note: note, Providers: clean, Enabled: en}, nil
}

func normalizeProxyProfileProvider(value string) string {
	return deduplicationProviderFamily(strings.ToLower(strings.TrimSpace(value)))
}
func validateProxyProfileURL(raw string) error {
	if len(raw) > maxProxyURLLength {
		return fmt.Errorf("proxy URL exceeds %d bytes", maxProxyURLLength)
	}
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "direct" || v == "none" || v == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return fmt.Errorf("proxy URL must be direct, none, or a valid URL")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return fmt.Errorf("proxy URL scheme must be http, https, socks5, or socks5h")
	}
	if u.Host == "" || u.Fragment != "" {
		return fmt.Errorf("proxy URL host or fragment is invalid")
	}
	return nil
}

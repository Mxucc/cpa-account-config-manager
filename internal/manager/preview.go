package manager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultPreviewTTL = 5 * time.Minute
	maxPreviewEntries = 32
)

var (
	ErrPreviewNotFound = errors.New("preview not found")
	ErrPreviewExpired  = errors.New("preview expired")
)

type PreviewRequest struct {
	Scope TargetScope `json:"scope"`
	Patch BatchPatch  `json:"patch"`
}

type PreviewTarget struct {
	ID             string `json:"id"`
	Name           string `json:"name,omitempty"`
	Provider       string `json:"provider,omitempty"`
	Label          string `json:"label,omitempty"`
	Eligible       bool   `json:"eligible"`
	ReadOnlyReason string `json:"read_only_reason,omitempty"`
}

type BatchPreview struct {
	ID            string          `json:"id,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	ExpiresAt     time.Time       `json:"expires_at,omitempty"`
	ScopeMode     string          `json:"scope_mode"`
	Total         int             `json:"total"`
	Eligible      int             `json:"eligible"`
	ReadOnly      int             `json:"read_only"`
	Missing       int             `json:"missing"`
	PhysicalFiles int             `json:"physical_files"`
	Providers     map[string]int  `json:"providers"`
	Patch         PatchSummary    `json:"patch"`
	Warnings      []string        `json:"warnings,omitempty"`
	Targets       []PreviewTarget `json:"targets"`
}

type previewSnapshot struct {
	Public  BatchPreview
	Scope   TargetScope
	Patch   BatchPatch
	Targets []Account
}

type previewStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]previewSnapshot
}

type PreviewService struct {
	accounts *AccountService
	store    *previewStore
	now      func() time.Time
}

func NewPreviewService(accounts *AccountService) *PreviewService {
	return &PreviewService{
		accounts: accounts,
		store: &previewStore{
			ttl:     defaultPreviewTTL,
			entries: make(map[string]previewSnapshot),
		},
		now: time.Now,
	}
}

func (s *PreviewService) Create(ctx context.Context, request PreviewRequest) (BatchPreview, error) {
	snapshot, errBuild := s.build(ctx, request.Scope, request.Patch)
	if errBuild != nil {
		return BatchPreview{}, errBuild
	}
	now := s.now().UTC()
	id, errID := randomIdentifier()
	if errID != nil {
		return BatchPreview{}, fmt.Errorf("create preview id: %w", errID)
	}
	snapshot.Public.ID = id
	snapshot.Public.CreatedAt = now
	snapshot.Public.ExpiresAt = now.Add(s.store.ttl)
	s.store.put(snapshot)
	return snapshot.Public, nil
}

func (s *PreviewService) BuildTransient(ctx context.Context, scope TargetScope, patch BatchPatch) (previewSnapshot, error) {
	return s.build(ctx, scope, patch)
}

func (s *PreviewService) Get(id string) (previewSnapshot, error) {
	return s.store.get(strings.TrimSpace(id), s.now().UTC())
}

func (s *PreviewService) Delete(id string) {
	s.store.delete(strings.TrimSpace(id))
}

func (s *PreviewService) Clear() {
	if s == nil || s.store == nil {
		return
	}
	s.store.clear()
}

func (s *PreviewService) build(ctx context.Context, rawScope TargetScope, rawPatch BatchPatch) (previewSnapshot, error) {
	if s == nil || s.accounts == nil {
		return previewSnapshot{}, fmt.Errorf("account service is unavailable")
	}
	scope, errScope := rawScope.Validate()
	if errScope != nil {
		return previewSnapshot{}, errScope
	}
	patch, errPatch := rawPatch.Validate()
	if errPatch != nil {
		return previewSnapshot{}, errPatch
	}
	resolved, errResolve := s.accounts.ResolveTargets(ctx, scope)
	if errResolve != nil {
		return previewSnapshot{}, fmt.Errorf("resolve target accounts: %w", errResolve)
	}
	if len(resolved.Accounts)+len(resolved.MissingIDs) == 0 {
		return previewSnapshot{}, fmt.Errorf("scope matched no accounts")
	}

	providers := make(map[string]int)
	targets := make([]PreviewTarget, 0, len(resolved.Accounts)+len(resolved.MissingIDs))
	eligibleTargets := make([]Account, 0, len(resolved.Accounts))
	readOnly := 0
	for _, account := range resolved.Accounts {
		provider := strings.TrimSpace(account.Provider)
		if provider == "" {
			provider = "unknown"
		}
		providers[provider]++
		target := PreviewTarget{
			ID:             account.ID,
			Name:           account.Name,
			Provider:       account.Provider,
			Label:          firstNonEmpty(account.Label, account.Email),
			Eligible:       account.Editable,
			ReadOnlyReason: account.ReadOnlyReason,
		}
		if account.Editable {
			eligibleTargets = append(eligibleTargets, account)
		} else {
			readOnly++
		}
		targets = append(targets, target)
	}
	for _, id := range resolved.MissingIDs {
		targets = append(targets, PreviewTarget{
			ID:             id,
			ReadOnlyReason: "account no longer exists",
		})
	}

	warnings := make([]string, 0, 3)
	if readOnly > 0 {
		warnings = append(warnings, fmt.Sprintf("%d target(s) are read-only and will be skipped", readOnly))
	}
	if len(resolved.MissingIDs) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d selected target(s) are missing and will be skipped", len(resolved.MissingIDs)))
	}
	if len(providers) > 1 {
		warnings = append(warnings, "the target snapshot contains multiple providers")
	}
	return previewSnapshot{
		Public: BatchPreview{
			CreatedAt:     s.now().UTC(),
			ScopeMode:     scope.Mode,
			Total:         len(targets),
			Eligible:      len(eligibleTargets),
			ReadOnly:      readOnly,
			Missing:       len(resolved.MissingIDs),
			PhysicalFiles: resolved.PhysicalFiles,
			Providers:     providers,
			Patch:         patch.Summary(),
			Warnings:      warnings,
			Targets:       targets,
		},
		Scope:   scope,
		Patch:   cloneBatchPatch(patch),
		Targets: append([]Account(nil), eligibleTargets...),
	}, nil
}

func (s *previewStore) put(snapshot previewSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := snapshot.Public.CreatedAt
	s.purgeExpiredLocked(now)
	if len(s.entries) >= maxPreviewEntries {
		type candidate struct {
			id        string
			createdAt time.Time
		}
		candidates := make([]candidate, 0, len(s.entries))
		for id, entry := range s.entries {
			candidates = append(candidates, candidate{id: id, createdAt: entry.Public.CreatedAt})
		}
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].createdAt.Before(candidates[j].createdAt)
		})
		if len(candidates) > 0 {
			delete(s.entries, candidates[0].id)
		}
	}
	s.entries[snapshot.Public.ID] = clonePreviewSnapshot(snapshot)
}

func (s *previewStore) get(id string, now time.Time) (previewSnapshot, error) {
	if id == "" {
		return previewSnapshot{}, ErrPreviewNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.entries[id]
	if !exists {
		return previewSnapshot{}, ErrPreviewNotFound
	}
	if !entry.Public.ExpiresAt.IsZero() && !now.Before(entry.Public.ExpiresAt) {
		delete(s.entries, id)
		return previewSnapshot{}, ErrPreviewExpired
	}
	return clonePreviewSnapshot(entry), nil
}

func (s *previewStore) delete(id string) {
	s.mu.Lock()
	delete(s.entries, id)
	s.mu.Unlock()
}

func (s *previewStore) clear() {
	s.mu.Lock()
	clear(s.entries)
	s.mu.Unlock()
}

func (s *previewStore) purgeExpiredLocked(now time.Time) {
	for id, entry := range s.entries {
		if !entry.Public.ExpiresAt.IsZero() && !now.Before(entry.Public.ExpiresAt) {
			delete(s.entries, id)
		}
	}
}

func clonePreviewSnapshot(snapshot previewSnapshot) previewSnapshot {
	clone := snapshot
	clone.Scope.IDs = append([]string(nil), snapshot.Scope.IDs...)
	clone.Patch = cloneBatchPatch(snapshot.Patch)
	clone.Targets = append([]Account(nil), snapshot.Targets...)
	clone.Public.Providers = make(map[string]int, len(snapshot.Public.Providers))
	for provider, count := range snapshot.Public.Providers {
		clone.Public.Providers[provider] = count
	}
	clone.Public.Patch.Fields = append([]string(nil), snapshot.Public.Patch.Fields...)
	clone.Public.Patch.HeaderSet = append([]string(nil), snapshot.Public.Patch.HeaderSet...)
	clone.Public.Patch.HeaderRemove = append([]string(nil), snapshot.Public.Patch.HeaderRemove...)
	clone.Public.Warnings = append([]string(nil), snapshot.Public.Warnings...)
	clone.Public.Targets = append([]PreviewTarget(nil), snapshot.Public.Targets...)
	return clone
}

func randomIdentifier() (string, error) {
	buffer := make([]byte, 16)
	if _, errRead := rand.Read(buffer); errRead != nil {
		return "", errRead
	}
	return hex.EncodeToString(buffer), nil
}

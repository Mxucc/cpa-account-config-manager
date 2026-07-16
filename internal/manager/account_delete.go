package manager

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultAccountDeletePreviewTTL = 5 * time.Minute
	maxAccountDeletePreviews       = 32
)

var (
	ErrAccountDeletePreviewNotFound = errors.New("delete preview not found")
	ErrAccountDeletePreviewExpired  = errors.New("delete preview expired")
	ErrAccountDeleteTargetNotFound  = errors.New("account was not found")
	ErrAccountDeleteTargetReadOnly  = errors.New("account is read-only and cannot be deleted")
	ErrAccountDeletePreviewStale    = errors.New("account changed after delete preview")
	ErrAccountDeleteBusy            = errors.New("another account mutation is already running")
	ErrAccountDeleteFailed          = errors.New("CPA failed to delete the account")
)

type AccountDeletePreviewRequest struct {
	ID string `json:"id"`
}

type AccountDeleteStartRequest struct {
	PreviewID string `json:"preview_id"`
}

type AccountDeleteTarget struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider,omitempty"`
	Type     string `json:"type,omitempty"`
	PlanType string `json:"plan_type,omitempty"`
	Label    string `json:"label,omitempty"`
	Email    string `json:"email,omitempty"`
	Status   string `json:"status,omitempty"`
	Source   string `json:"source,omitempty"`
}

type AccountDeletePreview struct {
	ID        string              `json:"id"`
	CreatedAt time.Time           `json:"created_at"`
	ExpiresAt time.Time           `json:"expires_at"`
	Account   AccountDeleteTarget `json:"account"`
}

type AccountDeleteResult struct {
	Status    string              `json:"status"`
	DeletedAt time.Time           `json:"deleted_at"`
	Account   AccountDeleteTarget `json:"account"`
}

type accountDeleteSnapshot struct {
	Public  AccountDeletePreview
	Account Account
}

type accountDeleteStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]accountDeleteSnapshot
}

type accountDeleteClientFactory func(string, string, HTTPDoer) (ManagementAuthFileDeleter, error)

type AccountDeleteService struct {
	accounts   *AccountService
	mutations  *MutationCoordinator
	store      *accountDeleteStore
	now        func() time.Time
	doer       HTTPDoer
	newDeleter accountDeleteClientFactory
}

func NewAccountDeleteService(accounts *AccountService, mutations *MutationCoordinator) *AccountDeleteService {
	if mutations == nil {
		mutations = NewMutationCoordinator()
	}
	return &AccountDeleteService{
		accounts:  accounts,
		mutations: mutations,
		store: &accountDeleteStore{
			ttl:     defaultAccountDeletePreviewTTL,
			entries: make(map[string]accountDeleteSnapshot),
		},
		now: time.Now,
		newDeleter: func(baseURL, key string, doer HTTPDoer) (ManagementAuthFileDeleter, error) {
			return newManagementClient(baseURL, key, doer)
		},
	}
}

func (s *AccountDeleteService) Preview(ctx context.Context, request AccountDeletePreviewRequest) (AccountDeletePreview, error) {
	if s == nil || s.accounts == nil || s.store == nil {
		return AccountDeletePreview{}, fmt.Errorf("account delete service is unavailable")
	}
	id := strings.TrimSpace(request.ID)
	if id == "" {
		return AccountDeletePreview{}, fmt.Errorf("account id is required")
	}
	resolved, errResolve := s.accounts.ResolveTargets(ctx, TargetScope{Mode: "selected", IDs: []string{id}})
	if errResolve != nil {
		return AccountDeletePreview{}, fmt.Errorf("resolve account for deletion: %w", errResolve)
	}
	if len(resolved.MissingIDs) != 0 || len(resolved.Accounts) != 1 {
		return AccountDeletePreview{}, ErrAccountDeleteTargetNotFound
	}
	account := resolved.Accounts[0]
	if !account.Editable || account.path == "" || account.revision == "" || !safeAuthJSONName(account.Name) {
		return AccountDeletePreview{}, ErrAccountDeleteTargetReadOnly
	}

	now := s.now().UTC()
	previewID, errID := randomIdentifier()
	if errID != nil {
		return AccountDeletePreview{}, fmt.Errorf("create delete preview id: %w", errID)
	}
	preview := AccountDeletePreview{
		ID:        previewID,
		CreatedAt: now,
		ExpiresAt: now.Add(s.store.ttl),
		Account:   publicAccountDeleteTarget(account),
	}
	s.store.put(accountDeleteSnapshot{Public: preview, Account: cloneDeleteAccount(account)})
	return preview, nil
}

func (s *AccountDeleteService) Start(ctx context.Context, previewID, managementBaseURL, managementKey string) (AccountDeleteResult, error) {
	if s == nil || s.accounts == nil || s.mutations == nil || s.store == nil {
		return AccountDeleteResult{}, fmt.Errorf("account delete service is unavailable")
	}
	previewID = strings.TrimSpace(previewID)
	snapshot, errPreview := s.store.get(previewID, s.now().UTC())
	if errPreview != nil {
		return AccountDeleteResult{}, errPreview
	}
	if !safeAuthJSONName(snapshot.Account.Name) || snapshot.Account.path == "" || snapshot.Account.revision == "" {
		return AccountDeleteResult{}, ErrAccountDeletePreviewStale
	}

	clientFactory := s.newDeleter
	if clientFactory == nil {
		clientFactory = func(baseURL, key string, doer HTTPDoer) (ManagementAuthFileDeleter, error) {
			return newManagementClient(baseURL, key, doer)
		}
	}
	deleter, errClient := clientFactory(resolveManagementBaseURL(managementBaseURL), managementKey, s.doer)
	if errClient != nil {
		return AccountDeleteResult{}, errClient
	}
	defer clearManagementAuthFileDeleterSecrets(deleter)

	owner := "account-delete:" + previewID
	if !s.mutations.TryAcquire(owner) {
		return AccountDeleteResult{}, ErrAccountDeleteBusy
	}
	defer s.mutations.Release(owner)

	currentRevision, errRevision := s.accounts.CurrentRevision(ctx, snapshot.Account)
	if errRevision != nil || currentRevision != snapshot.Account.revision {
		return AccountDeleteResult{}, ErrAccountDeletePreviewStale
	}
	if errDelete := deleter.DeleteAuthFile(ctx, snapshot.Account.Name); errDelete != nil {
		return AccountDeleteResult{}, fmt.Errorf("%w: %v", ErrAccountDeleteFailed, errDelete)
	}

	s.store.delete(previewID)
	return AccountDeleteResult{
		Status:    "deleted",
		DeletedAt: s.now().UTC(),
		Account:   snapshot.Public.Account,
	}, nil
}

func (s *AccountDeleteService) Clear() {
	if s == nil || s.store == nil {
		return
	}
	s.store.clear()
}

func publicAccountDeleteTarget(account Account) AccountDeleteTarget {
	return AccountDeleteTarget{
		ID:       account.ID,
		Name:     account.Name,
		Provider: account.Provider,
		Type:     account.Type,
		PlanType: account.PlanType,
		Label:    account.Label,
		Email:    account.Email,
		Status:   account.Status,
		Source:   account.Source,
	}
}

func cloneDeleteAccount(account Account) Account {
	clone := account
	clone.HeaderNames = append([]string(nil), account.HeaderNames...)
	clone.RecentRequests = append([]RecentRequestEntry(nil), account.RecentRequests...)
	return clone
}

func clearManagementAuthFileDeleterSecrets(deleter ManagementAuthFileDeleter) {
	if cleaner, ok := deleter.(interface{ clearSecrets() }); ok {
		cleaner.clearSecrets()
	}
}

func (s *accountDeleteStore) put(snapshot accountDeleteSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := snapshot.Public.CreatedAt
	s.purgeExpiredLocked(now)
	if len(s.entries) >= maxAccountDeletePreviews {
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
	s.entries[snapshot.Public.ID] = cloneAccountDeleteSnapshot(snapshot)
}

func (s *accountDeleteStore) get(id string, now time.Time) (accountDeleteSnapshot, error) {
	if id == "" {
		return accountDeleteSnapshot{}, ErrAccountDeletePreviewNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.entries[id]
	if !exists {
		return accountDeleteSnapshot{}, ErrAccountDeletePreviewNotFound
	}
	if !now.Before(entry.Public.ExpiresAt) {
		delete(s.entries, id)
		return accountDeleteSnapshot{}, ErrAccountDeletePreviewExpired
	}
	return cloneAccountDeleteSnapshot(entry), nil
}

func (s *accountDeleteStore) delete(id string) {
	s.mu.Lock()
	delete(s.entries, id)
	s.mu.Unlock()
}

func (s *accountDeleteStore) clear() {
	s.mu.Lock()
	clear(s.entries)
	s.mu.Unlock()
}

func (s *accountDeleteStore) purgeExpiredLocked(now time.Time) {
	for id, entry := range s.entries {
		if !now.Before(entry.Public.ExpiresAt) {
			delete(s.entries, id)
		}
	}
}

func cloneAccountDeleteSnapshot(snapshot accountDeleteSnapshot) accountDeleteSnapshot {
	clone := snapshot
	clone.Account = cloneDeleteAccount(snapshot.Account)
	return clone
}

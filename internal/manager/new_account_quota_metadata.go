package manager

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	quotaMetadataBootstrapWorkers     = 4
	quotaMetadataBootstrapMaxAttempts = 8
	quotaMetadataBootstrapMaxBackoff  = 30 * time.Minute
)

type quotaMetadataBootstrapRetry struct {
	Attempts   int
	RetryAfter time.Time
}

type quotaMetadataBootstrapHandler func(context.Context, Account, string) error

type accountQuotaMetadataBootstrap struct {
	mu              sync.Mutex
	wait            sync.WaitGroup
	backgroundOwner BackgroundWorkOwner
	latest          map[string]Account
	pending         map[string]quotaMetadataBootstrapRetry
	completed       map[string]struct{}
	managementKey   string
	handler         quotaMetadataBootstrapHandler
	wake            chan struct{}
	cancel          context.CancelFunc
	started         bool
	closed          bool
	now             func() time.Time
}

func NewAccountQuotaMetadataBootstrap() *accountQuotaMetadataBootstrap {
	return &accountQuotaMetadataBootstrap{
		latest: make(map[string]Account), pending: make(map[string]quotaMetadataBootstrapRetry),
		completed: make(map[string]struct{}), wake: make(chan struct{}, 1), now: time.Now,
	}
}

func (e *accountQuotaMetadataBootstrap) SetBackgroundWorkOwner(owner BackgroundWorkOwner) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.backgroundOwner = owner
	e.mu.Unlock()
}

func (e *accountQuotaMetadataBootstrap) SetHandler(handler quotaMetadataBootstrapHandler) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.handler = handler
	e.mu.Unlock()
}

func (e *accountQuotaMetadataBootstrap) Start() {
	if e == nil {
		return
	}
	e.mu.Lock()
	start := !e.started && !e.closed
	if start {
		ctx, cancel := context.WithCancel(context.Background())
		e.cancel = cancel
		e.started = true
		e.wait.Add(1)
		go e.run(ctx)
	}
	e.mu.Unlock()
	if !start {
		e.requestRun()
	}
}

func (e *accountQuotaMetadataBootstrap) Arm(managementKey string) {
	if e == nil {
		return
	}
	managementKey = strings.TrimSpace(managementKey)
	if managementKey == "" {
		return
	}
	e.mu.Lock()
	if !e.closed {
		e.managementKey = managementKey
	}
	e.mu.Unlock()
	managementKey = ""
	e.requestRun()
}

func (e *accountQuotaMetadataBootstrap) ObserveAccounts(accounts []Account) {
	if e == nil {
		return
	}
	latest := make(map[string]Account, min(len(accounts), maxInspectionAccounts))
	for _, account := range accounts {
		if !quotaMetadataBootstrapEligible(account) {
			continue
		}
		identity := newAccountModelProbeIdentity(account)
		if identity == "" {
			continue
		}
		if previous, exists := latest[identity]; !exists || account.ID < previous.ID {
			latest[identity] = quotaMetadataBootstrapAccount(account)
		}
	}
	if len(latest) > maxInspectionAccounts {
		identities := mapKeys(latest)
		sort.Strings(identities)
		for _, identity := range identities[maxInspectionAccounts:] {
			delete(latest, identity)
		}
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.latest = latest
	for identity := range e.pending {
		if _, exists := latest[identity]; !exists {
			delete(e.pending, identity)
		}
	}
	for identity := range e.completed {
		if _, exists := latest[identity]; !exists {
			delete(e.completed, identity)
		}
	}
	for identity, account := range latest {
		if quotaMetadataAlreadyObserved(account) {
			delete(e.pending, identity)
			delete(e.completed, identity)
			continue
		}
		if _, complete := e.completed[identity]; complete {
			continue
		}
		if _, queued := e.pending[identity]; !queued {
			e.pending[identity] = quotaMetadataBootstrapRetry{}
		}
	}
	e.mu.Unlock()
	e.requestRun()
}

func (e *accountQuotaMetadataBootstrap) Shutdown() {
	if e == nil {
		return
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.closed = true
	e.managementKey = ""
	cancel := e.cancel
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	e.wait.Wait()
}

func (e *accountQuotaMetadataBootstrap) requestRun() {
	if e == nil {
		return
	}
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *accountQuotaMetadataBootstrap) run(ctx context.Context) {
	defer e.wait.Done()
	for {
		delay := e.reconcile(ctx)
		if delay <= 0 {
			delay = time.Hour
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-e.wake:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
}

func (e *accountQuotaMetadataBootstrap) reconcile(ctx context.Context) time.Duration {
	if e == nil || ctx.Err() != nil {
		return time.Hour
	}
	now := e.currentTime()
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return time.Hour
	}
	latest := cloneQuotaMetadataBootstrapAccounts(e.latest)
	owner, key, handler := e.backgroundOwner, e.managementKey, e.handler
	due := make([]string, 0, len(e.pending))
	nextDelay := time.Hour
	for identity, retry := range e.pending {
		if retry.RetryAfter.IsZero() || !retry.RetryAfter.After(now) {
			due = append(due, identity)
			continue
		}
		if delay := retry.RetryAfter.Sub(now); delay < nextDelay {
			nextDelay = delay
		}
	}
	e.mu.Unlock()
	if strings.TrimSpace(key) == "" || handler == nil || !backgroundWorkAllowed(owner) || len(due) == 0 {
		key = ""
		return nextDelay
	}

	sort.Strings(due)
	ownedCtx, cancelOwnership := contextWithBackgroundOwnership(ctx, owner)
	defer cancelOwnership()
	type outcome struct {
		identity string
		err      error
	}
	jobs := make(chan string)
	results := make(chan outcome, len(due))
	workers := min(quotaMetadataBootstrapWorkers, len(due))
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for identity := range jobs {
				errRun := handler(ownedCtx, latest[identity], key)
				select {
				case results <- outcome{identity: identity, err: errRun}:
				case <-ownedCtx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, identity := range due {
			select {
			case jobs <- identity:
			case <-ownedCtx.Done():
				return
			}
		}
	}()
	go func() {
		wait.Wait()
		close(results)
	}()
	outcomes := make([]outcome, 0, len(due))
	for result := range results {
		outcomes = append(outcomes, result)
	}
	key = ""

	completedAt := e.currentTime()
	e.mu.Lock()
	for _, result := range outcomes {
		retry, exists := e.pending[result.identity]
		if !exists {
			continue
		}
		if result.err == nil {
			delete(e.pending, result.identity)
			e.completed[result.identity] = struct{}{}
			continue
		}
		retry.Attempts = min(retry.Attempts+1, quotaMetadataBootstrapMaxAttempts)
		retry.RetryAfter = completedAt.Add(quotaMetadataBootstrapBackoff(retry.Attempts))
		e.pending[result.identity] = retry
	}
	nextDelay = e.nextRetryDelayLocked(completedAt)
	e.mu.Unlock()
	return nextDelay
}

func (e *accountQuotaMetadataBootstrap) nextRetryDelayLocked(now time.Time) time.Duration {
	delay := time.Hour
	for _, retry := range e.pending {
		if retry.RetryAfter.IsZero() || !retry.RetryAfter.After(now) {
			return time.Second
		}
		if candidate := retry.RetryAfter.Sub(now); candidate < delay {
			delay = candidate
		}
	}
	return delay
}

func (e *accountQuotaMetadataBootstrap) currentTime() time.Time {
	now := time.Now
	if e != nil && e.now != nil {
		now = e.now
	}
	return now().UTC()
}

func quotaMetadataBootstrapEligible(account Account) bool {
	provider := strings.ToLower(strings.TrimSpace(firstNonEmpty(account.Provider, account.Type)))
	return strings.TrimSpace(account.ID) != "" && !account.RuntimeOnly &&
		(provider == "codex" || provider == agentIdentityProvider)
}

func quotaMetadataAlreadyObserved(account Account) bool {
	return account.Usage != nil && account.Usage.Codex != nil && !account.Usage.Codex.MetadataObservedAt.IsZero()
}

func quotaMetadataBootstrapAccount(account Account) Account {
	return Account{
		ID: account.ID, AuthID: account.AuthID, Name: account.Name, Provider: account.Provider,
		Type: account.Type, Email: account.Email, RuntimeOnly: account.RuntimeOnly, Usage: account.Usage,
	}
}

func cloneQuotaMetadataBootstrapAccounts(values map[string]Account) map[string]Account {
	out := make(map[string]Account, len(values))
	for identity, account := range values {
		out[identity] = quotaMetadataBootstrapAccount(account)
	}
	return out
}

func quotaMetadataBootstrapBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Minute << min(attempt-1, 5)
	return min(delay, quotaMetadataBootstrapMaxBackoff)
}

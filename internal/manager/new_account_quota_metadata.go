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
	exhausted       map[string]struct{}
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
		completed: make(map[string]struct{}), exhausted: make(map[string]struct{}), wake: make(chan struct{}, 1), now: time.Now,
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
}

func (e *accountQuotaMetadataBootstrap) Arm(managementKey string) {
	if e == nil {
		return
	}
	if strings.TrimSpace(managementKey) == "" {
		return
	}
	e.SetManagementKey(managementKey)
	e.mu.Lock()
	if !e.closed {
		// Arm is an explicit retry request. Clear terminal failures and
		// requeue the currently observed accounts so an operator can retry
		// after fixing credentials or upstream state.
		e.exhausted = make(map[string]struct{})
		for identity, account := range e.latest {
			if !quotaMetadataAlreadyObserved(account) {
				if _, complete := e.completed[identity]; !complete {
					e.pending[identity] = quotaMetadataBootstrapRetry{}
				}
			}
		}
	}
	e.mu.Unlock()
	e.requestRun()
}

// SetManagementKey refreshes credentials for future metadata collection
// without waking the worker. Account-list reads use this path so refreshing
// credentials alone cannot repeatedly wake the worker; account observation
// still schedules work when the observed account set actually changes.
func (e *accountQuotaMetadataBootstrap) SetManagementKey(managementKey string) {
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
		managementKey = ""
	}
	e.mu.Unlock()
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
	previous := e.latest
	pendingBefore := cloneQuotaMetadataRetryMap(e.pending)
	completedBefore := cloneQuotaMetadataIdentitySet(e.completed)
	exhaustedBefore := cloneQuotaMetadataIdentitySet(e.exhausted)
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
	for identity := range e.exhausted {
		if _, exists := latest[identity]; !exists {
			delete(e.exhausted, identity)
		}
	}
	for identity, account := range latest {
		previousAccount, hadPrevious := previous[identity]
		identityChanged := hadPrevious && quotaMetadataAccountChanged(previousAccount, account)
		if identityChanged {
			// A re-import/re-authentication can keep the same logical identity
			// while changing the auth file or plan. Do not inherit the old
			// completion marker (or stale metadata) in that case. A plan change
			// accompanied by a newer metadata timestamp is the normal result of
			// our own successful probe, so it must not immediately enqueue the
			// same account again.
			if quotaMetadataProbeUpdated(previousAccount, account) {
				identityChanged = false
			} else {
				delete(e.completed, identity)
				delete(e.exhausted, identity)
				e.pending[identity] = quotaMetadataBootstrapRetry{}
			}
		}
		if quotaMetadataAlreadyObserved(account) && !identityChanged {
			// The account already has a usable metadata snapshot. Keep a stable
			// completed marker so a probe-written plan/timestamp transition does
			// not itself create another wake-up on the next account-list read.
			delete(e.pending, identity)
			delete(e.exhausted, identity)
			e.completed[identity] = struct{}{}
			continue
		}
		if _, terminal := e.exhausted[identity]; terminal {
			continue
		}
		if _, complete := e.completed[identity]; complete {
			continue
		}
		if _, queued := e.pending[identity]; !queued {
			e.pending[identity] = quotaMetadataBootstrapRetry{}
		}
	}
	wake := !sameQuotaMetadataAccountSet(previous, latest) || !sameQuotaMetadataRetryMap(pendingBefore, e.pending) || !sameQuotaMetadataIdentitySet(completedBefore, e.completed) || !sameQuotaMetadataIdentitySet(exhaustedBefore, e.exhausted)
	e.mu.Unlock()
	if wake {
		e.requestRun()
	}
}

func sameQuotaMetadataAccountSet(left, right map[string]Account) bool {
	if len(left) != len(right) {
		return false
	}
	for identity, account := range left {
		other, exists := right[identity]
		if !exists || (quotaMetadataAccountChanged(account, other) && !quotaMetadataProbeUpdated(account, other)) {
			return false
		}
	}
	return true
}

func quotaMetadataPlanChanged(left, right string) bool {
	left = strings.ToLower(strings.TrimSpace(left))
	right = strings.ToLower(strings.TrimSpace(right))
	// The first successful metadata probe fills an initially empty plan. That
	// transition is expected and must not immediately schedule the same probe
	// again. A change between two known plans, however, indicates a real plan
	// transition and should invalidate the cached quota metadata.
	return left != "" && right != "" && left != right
}

func quotaMetadataCredentialChanged(left, right Account) bool {
	return strings.TrimSpace(left.ID) != strings.TrimSpace(right.ID) ||
		strings.TrimSpace(left.AuthID) != strings.TrimSpace(right.AuthID) ||
		strings.TrimSpace(left.Provider) != strings.TrimSpace(right.Provider) ||
		strings.TrimSpace(left.Type) != strings.TrimSpace(right.Type) ||
		strings.TrimSpace(left.Email) != strings.TrimSpace(right.Email)
}

func quotaMetadataAccountChanged(left, right Account) bool {
	return quotaMetadataCredentialChanged(left, right) ||
		quotaMetadataPlanChanged(left.PlanType, right.PlanType) ||
		quotaMetadataAlreadyObserved(left) != quotaMetadataAlreadyObserved(right)
}

// quotaMetadataProbeUpdated reports the one state transition that a successful
// metadata probe is expected to cause: the account's metadata timestamp becomes
// newer while the plan/credential fields are refreshed. Treating that as an
// external account change would make the account-list observer enqueue a second
// identical probe immediately after the first one.
func quotaMetadataProbeUpdated(left, right Account) bool {
	if quotaMetadataCredentialChanged(left, right) {
		return false
	}
	if !quotaMetadataPlanChanged(left.PlanType, right.PlanType) &&
		quotaMetadataAlreadyObserved(left) == quotaMetadataAlreadyObserved(right) {
		return false
	}
	leftObservedAt := quotaMetadataObservedAt(left)
	rightObservedAt := quotaMetadataObservedAt(right)
	return !rightObservedAt.IsZero() && (leftObservedAt.IsZero() || rightObservedAt.After(leftObservedAt))
}

func quotaMetadataObservedAt(account Account) time.Time {
	if account.Usage == nil || account.Usage.Codex == nil {
		return time.Time{}
	}
	return account.Usage.Codex.MetadataObservedAt
}

func cloneQuotaMetadataRetryMap(input map[string]quotaMetadataBootstrapRetry) map[string]quotaMetadataBootstrapRetry {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]quotaMetadataBootstrapRetry, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneQuotaMetadataIdentitySet(input map[string]struct{}) map[string]struct{} {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]struct{}, len(input))
	for key := range input {
		output[key] = struct{}{}
	}
	return output
}

func sameQuotaMetadataRetryMap(left, right map[string]quotaMetadataBootstrapRetry) bool {
	if len(left) != len(right) {
		return false
	}
	for identity, retry := range left {
		other, exists := right[identity]
		if !exists || retry != other {
			return false
		}
	}
	return true
}

func sameQuotaMetadataIdentitySet(left, right map[string]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for identity := range left {
		if _, exists := right[identity]; !exists {
			return false
		}
	}
	return true
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
		// The account may have been re-imported or re-authenticated while the
		// request was in flight. Do not apply a stale result to the replacement
		// account; its ObserveAccounts call has already queued a fresh probe.
		current, stillCurrent := e.latest[result.identity]
		if !stillCurrent || quotaMetadataCredentialChanged(latest[result.identity], current) {
			continue
		}
		if result.err == nil {
			delete(e.pending, result.identity)
			e.completed[result.identity] = struct{}{}
			continue
		}
		retry.Attempts++
		if retry.Attempts >= quotaMetadataBootstrapMaxAttempts {
			delete(e.pending, result.identity)
			e.exhausted[result.identity] = struct{}{}
			continue
		}
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
		Type: account.Type, PlanType: account.PlanType, Email: account.Email, RuntimeOnly: account.RuntimeOnly, Usage: account.Usage,
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

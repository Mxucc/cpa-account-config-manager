package manager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	newAccountModelProbeStoreVersion = 1
	newAccountModelProbeWorkers      = 4
	newAccountModelProbeMaxAttempts  = 8
	newAccountModelProbeMaxBackoff   = 30 * time.Minute
)

var newAccountModelProbePersistRetryDelay = 30 * time.Second

type newAccountModelProbeRetry struct {
	Attempts   int       `json:"attempts"`
	RetryAfter time.Time `json:"retry_after,omitempty"`
}

type persistedNewAccountModelProbeState struct {
	Version     int                                  `json:"version"`
	Initialized bool                                 `json:"initialized"`
	Known       []string                             `json:"known,omitempty"`
	Pending     map[string]newAccountModelProbeRetry `json:"pending,omitempty"`
}

type newAccountModelProbeHandler func(context.Context, Account, string, string) (ModelTestResult, error)

type newAccountModelProbeEngine struct {
	mu              sync.Mutex
	storeMu         sync.Mutex
	wait            sync.WaitGroup
	backgroundOwner BackgroundWorkOwner
	config          Config
	store           string
	initialized     bool
	observed        bool
	known           map[string]struct{}
	pending         map[string]newAccountModelProbeRetry
	latest          map[string]Account
	managementKey   string
	hostCallbackID  string
	storageErr      string
	loadFailed      bool
	retryTimer      *time.Timer
	retryScheduled  bool
	enabled         func() bool
	eligible        func(Account) bool
	handler         newAccountModelProbeHandler
	wake            chan struct{}
	cancel          context.CancelFunc
	started         bool
	closed          bool
	now             func() time.Time
}

func NewAccountModelProbeEngine(enabled func() bool) *newAccountModelProbeEngine {
	config := normalizeConfig(Config{})
	return &newAccountModelProbeEngine{
		config: config, store: newAccountModelProbeStorePath(config.DataDir),
		known: make(map[string]struct{}), pending: make(map[string]newAccountModelProbeRetry),
		latest: make(map[string]Account), enabled: enabled, wake: make(chan struct{}, 1), now: time.Now,
	}
}

func (e *newAccountModelProbeEngine) SetBackgroundWorkOwner(owner BackgroundWorkOwner) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.backgroundOwner = owner
	e.mu.Unlock()
}

func (e *newAccountModelProbeEngine) SetHandler(handler newAccountModelProbeHandler) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.handler = handler
	e.mu.Unlock()
}

func (e *newAccountModelProbeEngine) SetEligibility(eligible func(Account) bool) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.eligible = eligible
	e.mu.Unlock()
	e.requestRun()
}

func (e *newAccountModelProbeEngine) Configure(config Config) {
	if e == nil {
		return
	}
	config = normalizeConfig(config)
	storePath := newAccountModelProbeStorePath(config.DataDir)

	e.mu.Lock()
	if e.started && e.store == storePath && !e.loadFailed {
		e.config = config
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()

	state := persistedNewAccountModelProbeState{Version: newAccountModelProbeStoreVersion, Pending: make(map[string]newAccountModelProbeRetry)}
	storageErr := ""
	if loaded, errLoad := loadNewAccountModelProbeState(storePath); errLoad == nil {
		state = loaded
	} else if !errors.Is(errLoad, os.ErrNotExist) {
		storageErr = "new-account model-probe state could not be loaded"
	}

	e.mu.Lock()
	e.config = config
	e.store = storePath
	e.initialized = state.Initialized
	e.known = stringSet(state.Known)
	e.pending = cloneNewAccountModelProbePending(state.Pending)
	e.storageErr = storageErr
	e.loadFailed = storageErr == "new-account model-probe state could not be loaded"
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

func (e *newAccountModelProbeEngine) StorageError() string {
	if e == nil {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.storageErr
}

func (e *newAccountModelProbeEngine) Arm(managementKey string, requestedCallbackID ...string) {
	if e == nil {
		return
	}
	if strings.TrimSpace(managementKey) == "" {
		return
	}
	e.SetManagementKey(managementKey, requestedCallbackID...)
	e.requestRun()
}

// SetManagementKey updates the credentials used by a future probe without
// scheduling a probe run. Management GET requests use this method so merely
// opening or polling the UI cannot wake the new-account worker repeatedly.
// Explicit actions (imports, policy changes, or a manual scan) should use
// Arm, which also schedules pending work.
func (e *newAccountModelProbeEngine) SetManagementKey(managementKey string, requestedCallbackID ...string) {
	if e == nil {
		return
	}
	managementKey = strings.TrimSpace(managementKey)
	if managementKey == "" {
		return
	}
	callbackID := ""
	if len(requestedCallbackID) > 0 {
		callbackID = strings.TrimSpace(requestedCallbackID[0])
	}
	e.mu.Lock()
	if !e.closed {
		e.managementKey = managementKey
		e.hostCallbackID = callbackID
	}
	e.mu.Unlock()
	managementKey = ""
	callbackID = ""
}

func (e *newAccountModelProbeEngine) ObserveAccounts(accounts []Account) {
	if e == nil {
		return
	}
	latest := make(map[string]Account, len(accounts))
	for _, account := range accounts {
		identity := newAccountModelProbeIdentity(account)
		if identity == "" || !newAccountModelProbeEligible(account) {
			continue
		}
		if previous, exists := latest[identity]; !exists || account.ID < previous.ID {
			latest[identity] = newAccountModelProbeSummary(account)
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
	wake := false
	if !e.closed {
		wake = !e.observed || !sameAccountIdentitySet(e.latest, latest)
		e.latest = latest
		e.observed = true
	}
	e.mu.Unlock()
	if wake {
		e.requestRun()
	}
}

func sameAccountIdentitySet(left, right map[string]Account) bool {
	if len(left) != len(right) {
		return false
	}
	for identity, account := range left {
		other, exists := right[identity]
		if !exists || !sameNewAccountProbeAccount(account, other) {
			return false
		}
	}
	return true
}

// sameNewAccountProbeAccount deliberately compares only the metadata that
// changes how a newly discovered account should be probed. Usage, timestamps
// and request counters are intentionally excluded so routine quota refreshes
// do not retrigger model tests.
func sameNewAccountProbeAccount(left, right Account) bool {
	return left.ID == right.ID &&
		left.AuthID == right.AuthID &&
		left.Name == right.Name &&
		left.Provider == right.Provider &&
		left.Type == right.Type &&
		left.Email == right.Email &&
		left.AccountType == right.AccountType &&
		left.PlanType == right.PlanType &&
		left.Disabled == right.Disabled &&
		left.RuntimeOnly == right.RuntimeOnly &&
		reflect.DeepEqual(left.ModelPolicy, right.ModelPolicy)
}

func (e *newAccountModelProbeEngine) Shutdown() {
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
	e.hostCallbackID = ""
	cancel := e.cancel
	retryTimer := e.retryTimer
	e.retryTimer = nil
	e.retryScheduled = false
	e.mu.Unlock()
	if retryTimer != nil {
		retryTimer.Stop()
	}
	if cancel != nil {
		cancel()
	}
	e.wait.Wait()
}

func (e *newAccountModelProbeEngine) requestRun() {
	if e == nil {
		return
	}
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *newAccountModelProbeEngine) run(ctx context.Context) {
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

func (e *newAccountModelProbeEngine) reconcile(ctx context.Context) time.Duration {
	if ctx.Err() != nil {
		return time.Hour
	}
	e.mu.Lock()
	if e.closed || !e.observed {
		e.mu.Unlock()
		return time.Hour
	}
	latest := cloneNewAccountModelProbeAccounts(e.latest)
	now := e.currentTime()
	if !e.initialized {
		e.initialized = true
		e.known = accountIdentitySet(latest)
		e.pending = make(map[string]newAccountModelProbeRetry)
		state, storePath := e.persistedStateLocked(), e.store
		e.mu.Unlock()
		e.persist(storePath, state)
		return time.Hour
	}
	current := accountIdentitySet(latest)
	for identity := range e.pending {
		if _, exists := current[identity]; !exists {
			delete(e.pending, identity)
		}
	}
	for identity := range current {
		if _, exists := e.known[identity]; !exists {
			e.pending[identity] = newAccountModelProbeRetry{}
		}
	}
	e.known = current
	state, storePath := e.persistedStateLocked(), e.store
	owner, key, callbackID, handler, eligible := e.backgroundOwner, e.managementKey, e.hostCallbackID, e.handler, e.eligible
	enabled := e.enabled != nil && e.enabled()
	due := make([]string, 0, len(e.pending))
	nextDelay := time.Hour
	for identity, retry := range e.pending {
		if retry.Attempts >= newAccountModelProbeMaxAttempts {
			delete(e.pending, identity)
			continue
		}
		if retry.RetryAfter.IsZero() || !retry.RetryAfter.After(now) {
			if eligible != nil && !eligible(latest[identity]) {
				continue
			}
			due = append(due, identity)
			continue
		}
		if delay := retry.RetryAfter.Sub(now); delay < nextDelay {
			nextDelay = delay
		}
	}
	state = e.persistedStateLocked()
	e.mu.Unlock()
	e.persist(storePath, state)
	if !enabled || strings.TrimSpace(key) == "" || handler == nil || !backgroundWorkAllowed(owner) || len(due) == 0 {
		key = ""
		callbackID = ""
		return nextDelay
	}
	sort.Strings(due)
	type outcome struct {
		identity string
		result   ModelTestResult
		err      error
	}
	jobs := make(chan string)
	results := make(chan outcome, len(due))
	workers := min(newAccountModelProbeWorkers, len(due))
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for identity := range jobs {
				result, errRun := handler(ctx, latest[identity], key, callbackID)
				select {
				case results <- outcome{identity: identity, result: result, err: errRun}:
				case <-ctx.Done():
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
			case <-ctx.Done():
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
	callbackID = ""
	completedAt := e.currentTime()
	e.mu.Lock()
	for _, result := range outcomes {
		retry, exists := e.pending[result.identity]
		if !exists {
			continue
		}
		if !newAccountModelProbeShouldRetry(result.result, result.err) {
			delete(e.pending, result.identity)
			continue
		}
		retry.Attempts++
		if retry.Attempts >= newAccountModelProbeMaxAttempts {
			delete(e.pending, result.identity)
			continue
		}
		retry.RetryAfter = completedAt.Add(newAccountModelProbeBackoff(retry.Attempts))
		e.pending[result.identity] = retry
	}
	state, storePath = e.persistedStateLocked(), e.store
	nextDelay = e.nextRetryDelayLocked(completedAt)
	e.mu.Unlock()
	e.persist(storePath, state)
	return nextDelay
}

func (e *newAccountModelProbeEngine) persistedStateLocked() persistedNewAccountModelProbeState {
	known := make([]string, 0, len(e.known))
	for identity := range e.known {
		known = append(known, identity)
	}
	sort.Strings(known)
	return persistedNewAccountModelProbeState{
		Version: newAccountModelProbeStoreVersion, Initialized: e.initialized,
		Known: known, Pending: cloneNewAccountModelProbePending(e.pending),
	}
}

func (e *newAccountModelProbeEngine) nextRetryDelayLocked(now time.Time) time.Duration {
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

func (e *newAccountModelProbeEngine) persist(path string, state persistedNewAccountModelProbeState) {
	if strings.TrimSpace(path) == "" {
		return
	}
	e.storeMu.Lock()
	errSave := saveNewAccountModelProbeState(path, state)
	e.storeMu.Unlock()
	e.mu.Lock()
	if e.store == path {
		if errSave != nil {
			e.storageErr = "new-account model-probe state could not be persisted"
			e.schedulePersistRetryLocked()
		} else {
			e.storageErr = ""
			e.loadFailed = false
			if e.retryTimer != nil {
				e.retryTimer.Stop()
				e.retryTimer = nil
			}
			e.retryScheduled = false
		}
	}
	e.mu.Unlock()
}

func (e *newAccountModelProbeEngine) schedulePersistRetryLocked() {
	if e == nil || e.closed || e.retryScheduled {
		return
	}
	e.retryScheduled = true
	e.retryTimer = time.AfterFunc(newAccountModelProbePersistRetryDelay, func() {
		e.mu.Lock()
		if e.closed {
			e.retryScheduled = false
			e.retryTimer = nil
			e.mu.Unlock()
			return
		}
		e.retryScheduled = false
		e.retryTimer = nil
		e.mu.Unlock()
		e.requestRun()
	})
}

func (e *newAccountModelProbeEngine) currentTime() time.Time {
	now := time.Now
	if e != nil && e.now != nil {
		now = e.now
	}
	return now().UTC()
}

func newAccountModelProbeStorePath(dataDir string) string {
	return filepath.Join(dataDir, "new-account-model-probe.json")
}

func loadNewAccountModelProbeState(path string) (persistedNewAccountModelProbeState, error) {
	var state persistedNewAccountModelProbeState
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return state, errRead
	}
	if errDecode := decodeNewAccountModelProbeState(raw, &state); errDecode != nil {
		return state, errDecode
	}
	if state.Version != newAccountModelProbeStoreVersion {
		return state, errors.New("unsupported new-account model-probe state version")
	}
	state.Known = sanitizeNewAccountModelProbeHashes(state.Known)
	state.Pending = sanitizeNewAccountModelProbePending(state.Pending)
	return state, nil
}

func decodeNewAccountModelProbeState(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(target); errDecode != nil {
		return errDecode
	}
	if errTrailing := decoder.Decode(&struct{}{}); !errors.Is(errTrailing, io.EOF) {
		if errTrailing == nil {
			return errors.New("new-account model-probe state contains trailing data")
		}
		return errTrailing
	}
	return nil
}

func saveNewAccountModelProbeState(path string, state persistedNewAccountModelProbeState) error {
	state.Version = newAccountModelProbeStoreVersion
	state.Known = sanitizeNewAccountModelProbeHashes(state.Known)
	state.Pending = sanitizeNewAccountModelProbePending(state.Pending)
	return savePrivateJSON(path, state)
}

func newAccountModelProbeIdentity(account Account) string {
	provider := deduplicationProviderFamily(firstNonEmpty(account.Provider, account.Type))
	if provider == "" {
		return ""
	}
	value := strings.ToLower(strings.TrimSpace(account.AuthID))
	kind := "auth"
	if value == "" {
		value, kind = normalizeDeduplicationEmail(account.Email), "email"
	}
	if value == "" {
		value, kind = strings.TrimSpace(account.ID), "index"
	}
	if value == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(provider + "\x00" + kind + ":" + value))
	return hex.EncodeToString(digest[:])
}

func newAccountModelProbeEligible(account Account) bool {
	provider := strings.ToLower(strings.TrimSpace(firstNonEmpty(account.Provider, account.Type)))
	return strings.TrimSpace(account.ID) != "" && !account.RuntimeOnly && !account.Disabled &&
		(provider == "codex" || provider == agentIdentityProvider)
}

func newAccountModelProbeSummary(account Account) Account {
	return Account{
		ID: account.ID, AuthID: account.AuthID, Name: account.Name, Provider: account.Provider, Type: account.Type,
		Email: account.Email, AccountType: account.AccountType, PlanType: account.PlanType, Disabled: account.Disabled, RuntimeOnly: account.RuntimeOnly,
		ModelPolicy: cloneAccountModelPolicySummary(account.ModelPolicy),
	}
}

func cloneAccountModelPolicySummary(policy *AccountModelPolicySummary) *AccountModelPolicySummary {
	if policy == nil {
		return nil
	}
	clone := *policy
	clone.Models = append([]string(nil), policy.Models...)
	return &clone
}

func newAccountModelProbeShouldRetry(result ModelTestResult, errRun error) bool {
	if errRun != nil {
		return true
	}
	if result.ModelPolicy != nil && result.ModelPolicy.Status == "failed" {
		return true
	}
	if result.Status != "review" {
		return false
	}
	switch result.ReasonCode {
	case "request_timeout", "upstream_unavailable", "invalid_response":
		return true
	default:
		return false
	}
}

func newAccountModelProbeBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Minute << min(attempt-1, 5)
	return min(delay, newAccountModelProbeMaxBackoff)
}

func sanitizeNewAccountModelProbeHashes(values []string) []string {
	set := make(map[string]struct{}, min(len(values), maxInspectionAccounts))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if len(value) == sha256.Size*2 {
			if _, errDecode := hex.DecodeString(value); errDecode == nil {
				set[value] = struct{}{}
			}
		}
		if len(set) >= maxInspectionAccounts {
			break
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sanitizeNewAccountModelProbePending(values map[string]newAccountModelProbeRetry) map[string]newAccountModelProbeRetry {
	out := make(map[string]newAccountModelProbeRetry, min(len(values), maxInspectionAccounts))
	for _, identity := range sanitizeNewAccountModelProbeHashes(mapKeys(values)) {
		retry := values[identity]
		retry.Attempts = max(0, min(retry.Attempts, newAccountModelProbeMaxAttempts))
		retry.RetryAfter = retry.RetryAfter.UTC()
		out[identity] = retry
	}
	return out
}

func cloneNewAccountModelProbePending(values map[string]newAccountModelProbeRetry) map[string]newAccountModelProbeRetry {
	out := make(map[string]newAccountModelProbeRetry, len(values))
	for identity, retry := range values {
		out[identity] = retry
	}
	return out
}

func cloneNewAccountModelProbeAccounts(values map[string]Account) map[string]Account {
	out := make(map[string]Account, len(values))
	for identity, account := range values {
		out[identity] = newAccountModelProbeSummary(account)
	}
	return out
}

func accountIdentitySet(accounts map[string]Account) map[string]struct{} {
	out := make(map[string]struct{}, len(accounts))
	for identity := range accounts {
		out[identity] = struct{}{}
	}
	return out
}

func stringSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}

func mapKeys[T any](values map[string]T) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	return out
}

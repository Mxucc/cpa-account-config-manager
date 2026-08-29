package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

const (
	defaultPolicyScanIntervalSeconds = 15
	minPolicyScanIntervalSeconds     = 5
	maxPolicyScanIntervalSeconds     = 300
	policyFailureRetryInterval       = 5 * time.Minute
	aiProviderProxyReconcileInterval = 5 * time.Minute
	policyApplyModeMissing           = "missing"

	policyFieldPriority      = "priority"
	policyFieldWebsockets    = "websockets"
	policyFieldProxyURL      = "proxy_url"
	policyFieldNote          = "note"
	policyFieldPrefix        = "prefix"
	policyFieldHeaders       = "headers"
	policyMutationOwner      = "default-policy-scan"
	policyQuotaWorkers       = 4
	policyFailureSampleLimit = 5
	policyLocalStoreError    = "default policy scan status could not be persisted locally"
	configuredPolicyError    = "configured default policy could not be loaded"
)

var policyPersistRetryDelay = 30 * time.Second

var ErrPolicyStorageUnavailable = errors.New("default policy storage is unavailable; configure data_dir to a writable directory")

type DefaultPolicy struct {
	Enabled                        bool                    `json:"enabled" yaml:"enabled"`
	NewAccountModelProbeEnabled    bool                    `json:"new_account_model_probe_enabled" yaml:"new_account_model_probe_enabled"`
	CodexQuotaMetadataProbeEnabled bool                    `json:"codex_quota_metadata_probe_enabled" yaml:"codex_quota_metadata_probe_enabled"`
	ApplyMode                      string                  `json:"apply_mode" yaml:"apply_mode"`
	ScanIntervalSeconds            int                     `json:"scan_interval_seconds" yaml:"scan_interval_seconds"`
	Priority                       *int                    `json:"priority" yaml:"priority"`
	Disabled                       *bool                   `json:"disabled,omitempty" yaml:"disabled,omitempty"`
	ConcurrencyLimit               *int                    `json:"concurrency_limit,omitempty" yaml:"concurrency_limit,omitempty"`
	QuotaPolicy                    *AccountQuotaPolicy     `json:"quota_policy,omitempty" yaml:"quota_policy,omitempty"`
	Note                           *string                 `json:"note,omitempty" yaml:"note,omitempty"`
	Prefix                         *string                 `json:"prefix,omitempty" yaml:"prefix,omitempty"`
	ProxyURL                       *string                 `json:"proxy_url,omitempty" yaml:"proxy_url,omitempty"`
	Websockets                     *bool                   `json:"websockets" yaml:"websockets"`
	Headers                        *HeaderPatch            `json:"headers,omitempty" yaml:"headers,omitempty"`
	ModelPolicy                    *ModelPolicyPatch       `json:"model_policy,omitempty" yaml:"model_policy,omitempty"`
	CodexIdentity                  *CodexIdentityOverride  `json:"codex_identity,omitempty" yaml:"codex_identity,omitempty"`
	ProxyProfileID                 *string                 `json:"proxy_profile_id,omitempty" yaml:"proxy_profile_id,omitempty"`
	AIProviderProxyProfileID       *string                 `json:"ai_provider_proxy_profile_id,omitempty" yaml:"ai_provider_proxy_profile_id,omitempty"`
	ConditionalRules               []ConditionalPolicyRule `json:"conditional_rules,omitempty" yaml:"conditional_rules,omitempty"`
	proxyURL                       *string
}

type PolicyScanSummary struct {
	StartedAt            time.Time                `json:"started_at,omitempty"`
	FinishedAt           time.Time                `json:"finished_at,omitempty"`
	Scanned              int                      `json:"scanned"`
	Eligible             int                      `json:"eligible"`
	Changed              int                      `json:"changed"`
	Skipped              int                      `json:"skipped"`
	Failed               int                      `json:"failed"`
	QuotaMetadataProbed  int                      `json:"quota_metadata_probed"`
	QuotaMetadataUpdated int                      `json:"quota_metadata_updated"`
	QuotaMetadataFailed  int                      `json:"quota_metadata_failed"`
	Error                string                   `json:"error,omitempty"`
	FailureDetails       []OperationFailureDetail `json:"failure_details,omitempty"`
}

type PolicySnapshot struct {
	Policy                           DefaultPolicy     `json:"policy"`
	Running                          bool              `json:"running"`
	ScanStartedAt                    time.Time         `json:"scan_started_at,omitempty"`
	LastScan                         PolicyScanSummary `json:"last_scan"`
	NewAccountModelProbeStorageError string            `json:"new_account_model_probe_storage_error,omitempty"`
}

type policyApplyMode uint8

const (
	applyMissing policyApplyMode = iota
	applyForce
)

type authFingerprint struct {
	Name       string
	Path       string
	Size       int64
	ModTimeNS  int64
	ModTimeSet bool
	PlanType   string
}

type policyFailureBackoff struct {
	Fingerprint authFingerprint
	RetryAt     time.Time
}

type policyQuotaMetadataProbe func(context.Context, Account, string) (string, error)
type policyAIProviderProxyApplier func(context.Context, DefaultPolicy, ProxyProfileResolver, string) (int, error)

type policyQuotaMetadataProbeSummary struct {
	planTypes map[string]string
	failedIDs map[string]struct{}
	failures  []OperationFailureDetail
	attempted int
	updated   int
	failed    int
	ready     bool
}

type PolicyEngine struct {
	mu                       sync.RWMutex
	operationMu              sync.Mutex
	wait                     sync.WaitGroup
	host                     AuthHost
	mutations                *MutationCoordinator
	observer                 interface{ ObserveAccounts([]Account) }
	modelPolicyApplier       func(context.Context, Account, ModelPolicyPatch, string) (bool, error)
	proxyProfiles            ProxyProfileResolver
	globalPolicy             *GlobalPolicyService
	concurrency              *AccountConcurrencyService
	quotaPolicies            *QuotaPolicyService
	codexIdentityOverrides   *CodexIdentityOverrideService
	aiProviderProxyApplier   policyAIProviderProxyApplier
	aiProviderProxyAppliedAt time.Time
	quotaMetadataProbe       policyQuotaMetadataProbe
	managementKey            string
	backgroundOwner          BackgroundWorkOwner
	config                   Config
	store                    string
	policy                   DefaultPolicy
	lastScan                 PolicyScanSummary
	running                  bool
	scanStarted              time.Time
	fingerprints             map[string]authFingerprint
	failures                 map[string]policyFailureBackoff
	wake                     chan struct{}
	cancel                   context.CancelFunc
	started                  bool
	closed                   bool
	loadFailed               bool
	dirty                    bool
	retryTimer               *time.Timer
	retryScheduled           bool
	// initialScanPending coalesces a manual wake received while the engine is
	// performing its first startup reconciliation.  Configure starts the
	// worker before callers can finish applying a policy; without this guard a
	// queued RequestScan could make the same account run twice back-to-back and
	// overwrite the useful Changed=1 result with a no-op scan.
	initialScanPending bool
	now                func() time.Time
}

func (e *PolicyEngine) SetGlobalPolicy(service *GlobalPolicyService) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.globalPolicy = service
	e.fingerprints = make(map[string]authFingerprint)
	e.failures = make(map[string]policyFailureBackoff)
	e.mu.Unlock()
	e.requestScan()
}

func (e *PolicyEngine) SetAccountConcurrency(service *AccountConcurrencyService) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.concurrency = service
	e.mu.Unlock()
}

func (e *PolicyEngine) SetQuotaPolicies(service *QuotaPolicyService) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.quotaPolicies = service
	e.mu.Unlock()
}

func (e *PolicyEngine) SetCodexIdentityOverrides(service *CodexIdentityOverrideService) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.codexIdentityOverrides = service
	e.mu.Unlock()
}

func (e *PolicyEngine) SetQuotaMetadataProbe(probe policyQuotaMetadataProbe) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.quotaMetadataProbe = probe
	e.mu.Unlock()
}

func (e *PolicyEngine) SetModelPolicyApplier(applier func(context.Context, Account, ModelPolicyPatch, string) (bool, error)) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.modelPolicyApplier = applier
	e.mu.Unlock()
}

func (e *PolicyEngine) SetProxyProfiles(resolver ProxyProfileResolver) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.proxyProfiles = resolver
	e.mu.Unlock()
}

func (e *PolicyEngine) SetAIProviderProxyApplier(applier policyAIProviderProxyApplier) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.aiProviderProxyApplier = applier
	e.aiProviderProxyAppliedAt = time.Time{}
	e.mu.Unlock()
}

func (e *PolicyEngine) Arm(managementKey string) {
	if e == nil {
		return
	}
	managementKey = strings.TrimSpace(managementKey)
	if managementKey == "" {
		return
	}
	e.mu.Lock()
	if !e.closed {
		if e.managementKey != managementKey {
			e.aiProviderProxyAppliedAt = time.Time{}
		}
		e.managementKey = managementKey
	}
	e.mu.Unlock()
	managementKey = ""
}

func (e *PolicyEngine) ProxyProfilesUpdated() {
	if e == nil {
		return
	}
	e.operationMu.Lock()
	e.mu.Lock()
	e.fingerprints = make(map[string]authFingerprint)
	e.failures = make(map[string]policyFailureBackoff)
	e.aiProviderProxyAppliedAt = time.Time{}
	e.mu.Unlock()
	e.operationMu.Unlock()
	e.requestScan()
}

func (e *PolicyEngine) SetObserver(observer interface{ ObserveAccounts([]Account) }) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.observer = observer
	e.mu.Unlock()
}

func NewPolicyEngine(host AuthHost) *PolicyEngine {
	return NewPolicyEngineWithCoordinator(host, NewMutationCoordinator())
}

func NewPolicyEngineWithCoordinator(host AuthHost, mutations *MutationCoordinator) *PolicyEngine {
	if mutations == nil {
		mutations = NewMutationCoordinator()
	}
	config := normalizeConfig(Config{})
	return &PolicyEngine{
		host:         host,
		mutations:    mutations,
		config:       config,
		store:        policyStorePath(config.DataDir),
		policy:       normalizeDefaultPolicy(DefaultPolicy{}),
		fingerprints: make(map[string]authFingerprint),
		failures:     make(map[string]policyFailureBackoff),
		wake:         make(chan struct{}, 1),
		now:          time.Now,
	}
}

func (e *PolicyEngine) SetBackgroundWorkOwner(owner BackgroundWorkOwner) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.backgroundOwner = owner
	e.mu.Unlock()
}

func (e *PolicyEngine) Configure(config Config) {
	if e == nil {
		return
	}
	config = normalizeConfig(config)
	storePath := policyStorePath(config.DataDir)
	configuredPolicy, hasConfiguredPolicy, errConfiguredPolicy := policyFromConfig(config)

	e.operationMu.Lock()
	e.mu.RLock()
	sameStore := e.started && e.store == storePath && !e.loadFailed
	e.mu.RUnlock()

	if sameStore {
		e.mu.Lock()
		e.config = config
		if e.dirty {
			e.mu.Unlock()
			e.persistRuntimeStateLocked()
			e.mu.RLock()
			stillDirty := e.dirty
			e.mu.RUnlock()
			if stillDirty {
				e.operationMu.Unlock()
				return
			}
			e.mu.Lock()
		}
		currentPolicy := cloneDefaultPolicy(e.policy)
		lastScan := e.lastScan
		e.mu.Unlock()
		if hasConfiguredPolicy {
			if errConfiguredPolicy != nil {
				// Keep the last known-good policy active when a live config
				// reload contains an invalid policy.  Falling back to an empty
				// policy here silently disables automation until the next reload
				// and makes the UI look as if the save succeeded.
				e.mu.Lock()
				e.lastScan.Error = configuredPolicyError
				e.mu.Unlock()
			} else if !defaultPolicyEqual(currentPolicy, configuredPolicy) {
				if errSave := savePolicyRuntimeState(storePath, configuredPolicy, lastScan, nil); errSave != nil {
					e.mu.Lock()
					e.lastScan.Error = policyLocalStoreError
					e.mu.Unlock()
				} else {
					e.mu.Lock()
					e.policy = configuredPolicy
					e.fingerprints = make(map[string]authFingerprint)
					e.failures = make(map[string]policyFailureBackoff)
					if e.lastScan.Error == configuredPolicyError || e.lastScan.Error == policyLocalStoreError {
						e.lastScan.Error = ""
					}
					e.loadFailed = false
					e.mu.Unlock()
				}
			} else {
				e.mu.Lock()
				if e.lastScan.Error == configuredPolicyError || e.lastScan.Error == policyLocalStoreError {
					e.lastScan.Error = ""
				}
				e.mu.Unlock()
			}
		}
		e.operationMu.Unlock()
		return
	}

	// Do not abandon a newer in-memory scan snapshot when data_dir changes.
	// Keeping the old store active is safer than silently losing fingerprints
	// and causing every account to be processed again after a transient mount
	// failure.
	e.mu.RLock()
	needsFlush := e.started && e.store != storePath && e.dirty
	e.mu.RUnlock()
	if needsFlush {
		e.persistRuntimeStateLocked()
		e.mu.RLock()
		stillDirty := e.dirty
		e.mu.RUnlock()
		if stillDirty {
			e.operationMu.Unlock()
			return
		}
	}

	policy := normalizeDefaultPolicy(DefaultPolicy{})
	lastScan := PolicyScanSummary{}
	fingerprints := make(map[string]authFingerprint)
	loadFailed := false
	if hasConfiguredPolicy {
		if errConfiguredPolicy != nil {
			lastScan.Error = configuredPolicyError
		} else {
			policy = configuredPolicy
			if loadedPolicy, loadedScan, loadedFingerprints, errLoad := loadPolicyRuntimeState(storePath); errLoad == nil {
				lastScan = loadedScan
				if defaultPolicyEqual(loadedPolicy, policy) {
					fingerprints = loadedFingerprints
				}
			}
			if errSave := savePolicyRuntimeState(storePath, policy, lastScan, fingerprints); errSave != nil {
				lastScan.Error = policyLocalStoreError
			}
		}
	} else {
		loadedPolicy, loadedScan, loadedFingerprints, errLoad := loadPolicyRuntimeState(storePath)
		if errLoad == nil {
			policy = loadedPolicy
			lastScan = loadedScan
			fingerprints = loadedFingerprints
		} else if !errors.Is(errLoad, os.ErrNotExist) {
			lastScan.Error = "stored default policy could not be loaded"
			loadFailed = true
		}
	}
	// A repeated Configure on a store that failed to load must be recoverable,
	// but another failed read must not replace the last known-good live policy.
	e.mu.RLock()
	startedSameStore := e.started && e.store == storePath
	e.mu.RUnlock()
	if loadFailed && startedSameStore {
		e.mu.Lock()
		e.config = config
		e.loadFailed = true
		e.lastScan.Error = "stored default policy could not be loaded"
		e.mu.Unlock()
		e.operationMu.Unlock()
		return
	}

	e.mu.Lock()
	e.config = config
	e.store = storePath
	e.policy = policy
	e.lastScan = lastScan
	e.fingerprints = fingerprints
	e.failures = make(map[string]policyFailureBackoff)
	e.loadFailed = loadFailed
	e.dirty = false
	start := !e.started && !e.closed
	if start {
		e.initialScanPending = true
		ctx, cancel := context.WithCancel(context.Background())
		e.cancel = cancel
		e.started = true
		e.wait.Add(1)
		go e.run(ctx)
	}
	e.mu.Unlock()
	e.operationMu.Unlock()

	if !start {
		e.requestScan()
	}
}

func (e *PolicyEngine) AccountMetadataUpdated(authIndex string) {
	if e == nil {
		return
	}
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return
	}
	e.mu.Lock()
	delete(e.fingerprints, authIndex)
	delete(e.failures, authIndex)
	e.mu.Unlock()
	e.requestScan()
}

func policyFromConfig(config Config) (DefaultPolicy, bool, error) {
	if config.DefaultPolicy == nil {
		return DefaultPolicy{}, false, nil
	}
	policy, errValidate := validateDefaultPolicy(*config.DefaultPolicy)
	if errValidate != nil {
		return DefaultPolicy{}, true, errValidate
	}
	return policy, true, nil
}

func (e *PolicyEngine) Snapshot() PolicySnapshot {
	if e == nil {
		return PolicySnapshot{Policy: normalizeDefaultPolicy(DefaultPolicy{})}
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return PolicySnapshot{
		Policy:        cloneDefaultPolicy(e.policy),
		Running:       e.running,
		ScanStartedAt: e.scanStarted,
		LastScan:      e.lastScan,
	}
}

func (e *PolicyEngine) SetPolicy(policy DefaultPolicy) (DefaultPolicy, error) {
	if e == nil {
		return DefaultPolicy{}, ErrPolicyStorageUnavailable
	}
	normalized, errValidate := validateDefaultPolicy(policy)
	if errValidate != nil {
		return DefaultPolicy{}, errValidate
	}

	e.operationMu.Lock()
	e.mu.RLock()
	storePath := e.store
	lastScan := e.lastScan
	closed := e.closed
	changed := !defaultPolicyEqual(e.policy, normalized)
	fingerprints := clonePolicyFingerprints(e.fingerprints)
	e.mu.RUnlock()
	if closed || strings.TrimSpace(storePath) == "" {
		e.operationMu.Unlock()
		return DefaultPolicy{}, ErrPolicyStorageUnavailable
	}
	if changed {
		fingerprints = nil
	}
	errSave := savePolicyRuntimeState(storePath, normalized, lastScan, fingerprints)
	if errSave != nil {
		// Do not publish a policy that was not durably saved.  Updating the
		// in-memory copy first would make the UI report success while a restart
		// silently restores the previous policy (and could also discard the
		// previous fingerprint/backoff state).
		e.mu.Lock()
		e.lastScan.Error = policyLocalStoreError
		e.mu.Unlock()
		e.operationMu.Unlock()
		return DefaultPolicy{}, fmt.Errorf("save default policy: %w", errSave)
	}
	e.mu.Lock()
	e.policy = normalized
	if changed {
		e.fingerprints = make(map[string]authFingerprint)
		e.failures = make(map[string]policyFailureBackoff)
		e.aiProviderProxyAppliedAt = time.Time{}
	}
	if e.lastScan.Error == policyLocalStoreError {
		e.lastScan.Error = ""
	}
	e.mu.Unlock()
	e.operationMu.Unlock()

	return cloneDefaultPolicy(normalized), nil
}

func (e *PolicyEngine) RequestScan() PolicySnapshot {
	if e == nil {
		return PolicySnapshot{Policy: normalizeDefaultPolicy(DefaultPolicy{})}
	}
	e.operationMu.Lock()
	e.mu.Lock()
	// The first startup scan is already implicit.  A wake sent before the
	// goroutine starts, or while that first scan is running, is redundant and
	// otherwise causes an immediate second full scan.  Policy changes made
	// during the scan are still detected by the normal fingerprint pass on the
	// next scheduler iteration.
	if e.initialScanPending {
		e.mu.Unlock()
		e.operationMu.Unlock()
		return e.Snapshot()
	}
	e.failures = make(map[string]policyFailureBackoff)
	e.aiProviderProxyAppliedAt = time.Time{}
	e.mu.Unlock()
	e.operationMu.Unlock()
	e.requestScan()
	return e.Snapshot()
}

func (e *PolicyEngine) Shutdown() {
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
	e.operationMu.Lock()
	e.persistRuntimeStateLocked()
	e.operationMu.Unlock()
	e.mu.Lock()
	if e.retryTimer != nil {
		e.retryTimer.Stop()
		e.retryTimer = nil
	}
	e.retryScheduled = false
	e.mu.Unlock()
}

func (e *PolicyEngine) run(ctx context.Context) {
	defer e.wait.Done()
	for {
		if ctx.Err() != nil {
			return
		}
		retrySoon := e.reconcile(ctx)
		e.mu.Lock()
		e.initialScanPending = false
		e.mu.Unlock()

		// A policy pass deferred by another writer is intentionally retried on
		// the normal scheduler interval.  Retrying every second made imports,
		// batch jobs, and inspections generate a hot loop (and repeated stale
		// operation entries) while the mutation coordinator was occupied.
		interval := e.scanInterval()
		_ = retrySoon
		timer := time.NewTimer(interval)
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

func (e *PolicyEngine) requestScan() {
	if e == nil {
		return
	}
	e.mu.RLock()
	started := e.started && !e.closed
	e.mu.RUnlock()
	if !started {
		return
	}
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *PolicyEngine) scanInterval() time.Duration {
	e.mu.RLock()
	seconds := e.policy.ScanIntervalSeconds
	e.mu.RUnlock()
	seconds = clampPolicyScanInterval(seconds)
	return time.Duration(seconds) * time.Second
}

func (e *PolicyEngine) reconcile(ctx context.Context) bool {
	e.mu.RLock()
	owner := e.backgroundOwner
	e.mu.RUnlock()
	if !backgroundWorkAllowed(owner) {
		return false
	}
	ownedCtx, cancelOwnership := contextWithBackgroundOwnership(ctx, owner)
	defer cancelOwnership()
	ctx = ownedCtx
	e.operationMu.Lock()
	defer e.operationMu.Unlock()

	e.mu.RLock()
	policy := cloneDefaultPolicy(e.policy)
	global := e.globalPolicy
	e.mu.RUnlock()
	policy = mergeGlobalPolicyIntoDefault(policy, global)
	applyAccountDefaults := policy.ManagesAccountFields()
	applyAIProviderDefaults := policy.ManagesAIProviderProxy()
	applyDefaults := applyAccountDefaults || applyAIProviderDefaults
	if !applyDefaults && !policy.ManagesNewAccountProbe() {
		return false
	}
	if applyDefaults {
		// Do not even enumerate/probe accounts while another mutation is in
		// progress.  This is a deferred pass, not a failed scan; the normal
		// scheduler interval will retry after the writer has had a chance to
		// finish.
		if !e.mutations.TryAcquire(policyMutationOwner) {
			return false
		}
		e.mutations.Release(policyMutationOwner)
	}

	startedAt := e.now().UTC()
	e.mu.Lock()
	e.running = true
	e.scanStarted = startedAt
	e.mu.Unlock()

	summary := PolicyScanSummary{StartedAt: startedAt}
	if applyAIProviderDefaults {
		changed, errApply := e.reconcileAIProviderProxies(ctx, policy, startedAt)
		if errApply != nil {
			summary.Failed++
			addPolicyFailureDetail(&summary.FailureDetails, classifyPolicyFailure(errApply), "ai-providers")
		} else {
			summary.Changed += changed
		}
	}
	var fingerprints map[string]authFingerprint
	var failures map[string]policyFailureBackoff
	var observedAccounts []Account
	if applyAccountDefaults || policy.ManagesNewAccountProbe() {
		accountSummary, accountFingerprints, accountFailures, accounts := e.scanWithState(ctx, policy, startedAt)
		summary.Scanned += accountSummary.Scanned
		summary.Eligible += accountSummary.Eligible
		summary.Changed += accountSummary.Changed
		summary.Skipped += accountSummary.Skipped
		summary.Failed += accountSummary.Failed
		summary.QuotaMetadataProbed += accountSummary.QuotaMetadataProbed
		summary.QuotaMetadataUpdated += accountSummary.QuotaMetadataUpdated
		summary.QuotaMetadataFailed += accountSummary.QuotaMetadataFailed
		summary.FailureDetails = mergePolicyFailureDetails(summary.FailureDetails, accountSummary.FailureDetails)
		fingerprints, failures, observedAccounts = accountFingerprints, accountFailures, accounts
	} else {
		e.mu.RLock()
		fingerprints = clonePolicyFingerprints(e.fingerprints)
		failures = clonePolicyFailures(e.failures)
		e.mu.RUnlock()
	}
	summary.FinishedAt = e.now().UTC()
	e.mu.Lock()
	e.running = false
	e.scanStarted = time.Time{}
	if ctx.Err() == nil {
		e.lastScan = summary
		e.fingerprints = fingerprints
		e.failures = failures
		e.dirty = true
	}
	e.mu.Unlock()
	if ctx.Err() != nil {
		return false
	}
	e.mu.RLock()
	observer := e.observer
	e.mu.RUnlock()
	if observer != nil && observedAccounts != nil {
		observer.ObserveAccounts(observedAccounts)
	}
	e.persistRuntimeStateLocked()
	return false
}

// mergeGlobalPolicyIntoDefault applies the permanent global baseline only to
// fields that the automatic/default policy does not explicitly own. This
// keeps object and conditional overrides authoritative while allowing a
// single global configuration to seed all newly discovered accounts.
func mergeGlobalPolicyIntoDefault(policy DefaultPolicy, service *GlobalPolicyService) DefaultPolicy {
	if service == nil {
		return policy
	}
	global := service.Snapshot().Policy
	if !global.Enabled {
		return policy
	}
	if policy.Disabled == nil {
		policy.Disabled = cloneBoolPointer(global.Disabled)
	}
	if policy.Priority == nil {
		policy.Priority = cloneIntPointer(global.Priority)
	}
	if policy.ConcurrencyLimit == nil {
		policy.ConcurrencyLimit = cloneIntPointer(global.ConcurrencyLimit)
	}
	if policy.QuotaPolicy == nil && global.QuotaPolicy != nil {
		value := *global.QuotaPolicy
		policy.QuotaPolicy = &value
	}
	if policy.Note == nil {
		policy.Note = cloneStringPointer(global.Note)
	}
	if policy.Prefix == nil {
		policy.Prefix = cloneStringPointer(global.Prefix)
	}
	if policy.ProxyURL == nil {
		policy.ProxyURL = cloneStringPointer(global.ProxyURL)
	}
	if policy.ProxyProfileID == nil {
		policy.ProxyProfileID = cloneStringPointer(global.ProxyProfileID)
	}
	if policy.AIProviderProxyProfileID == nil {
		policy.AIProviderProxyProfileID = cloneStringPointer(global.AIProviderProxyProfileID)
	}
	if policy.Websockets == nil {
		policy.Websockets = cloneBoolPointer(global.Websockets)
	}
	if policy.Headers == nil && global.Headers != nil {
		value := cloneHeaderPatch(*global.Headers)
		policy.Headers = &value
	}
	if policy.ModelPolicy == nil && global.ModelPolicy != nil {
		value := cloneModelPolicyPatch(*global.ModelPolicy)
		policy.ModelPolicy = &value
	}
	if policy.CodexIdentity == nil {
		policy.CodexIdentity = codexIdentityOverrideFromGlobal(global.CodexIdentity)
	}
	return policy
}

func (e *PolicyEngine) reconcileAIProviderProxies(ctx context.Context, policy DefaultPolicy, startedAt time.Time) (int, error) {
	e.mu.RLock()
	applier := e.aiProviderProxyApplier
	resolver := e.proxyProfiles
	managementKey := e.managementKey
	lastAppliedAt := e.aiProviderProxyAppliedAt
	e.mu.RUnlock()
	if applier == nil || resolver == nil || strings.TrimSpace(managementKey) == "" {
		return 0, fmt.Errorf("AI provider proxy policy is not armed")
	}
	if !lastAppliedAt.IsZero() && startedAt.Sub(lastAppliedAt) < aiProviderProxyReconcileInterval {
		return 0, nil
	}
	changed, errApply := applier(ctx, policy, resolver, managementKey)
	managementKey = ""
	e.mu.Lock()
	e.aiProviderProxyAppliedAt = startedAt
	e.mu.Unlock()
	if errApply != nil {
		return 0, errApply
	}
	return changed, nil
}

func clonePolicyFailures(failures map[string]policyFailureBackoff) map[string]policyFailureBackoff {
	cloned := make(map[string]policyFailureBackoff, len(failures))
	for key, failure := range failures {
		cloned[key] = failure
	}
	return cloned
}

// persistRuntimeStateLocked saves the newest policy scan snapshot. The caller
// must hold operationMu so a delayed retry cannot overwrite a newer policy or
// fingerprint set.
func (e *PolicyEngine) persistRuntimeStateLocked() {
	if e == nil {
		return
	}
	e.mu.RLock()
	if !e.dirty || strings.TrimSpace(e.store) == "" || !backgroundWorkAllowed(e.backgroundOwner) {
		e.mu.RUnlock()
		return
	}
	storePath := e.store
	policy := cloneDefaultPolicy(e.policy)
	lastScan := policySummaryForPersistence(e.lastScan)
	fingerprints := clonePolicyFingerprints(e.fingerprints)
	e.mu.RUnlock()

	if errSave := savePolicyRuntimeState(storePath, policy, lastScan, fingerprints); errSave != nil {
		e.mu.Lock()
		e.lastScan.Error = policyLocalStoreError
		addPolicyFailureDetail(&e.lastScan.FailureDetails, OperationFailurePolicyStatePersist, "")
		e.schedulePersistRetryLocked()
		e.mu.Unlock()
		return
	}
	e.mu.Lock()
	if e.store == storePath {
		e.dirty = false
		e.loadFailed = false
		clearPolicyPersistenceFailureLocked(&e.lastScan)
		if e.retryTimer != nil {
			e.retryTimer.Stop()
			e.retryTimer = nil
		}
		e.retryScheduled = false
	}
	e.mu.Unlock()
}

func (e *PolicyEngine) schedulePersistRetryLocked() {
	if e == nil || e.closed || e.retryScheduled || !e.dirty {
		return
	}
	e.retryScheduled = true
	e.retryTimer = time.AfterFunc(policyPersistRetryDelay, func() {
		e.operationMu.Lock()
		e.mu.Lock()
		e.retryScheduled = false
		e.retryTimer = nil
		closed := e.closed
		e.mu.Unlock()
		if !closed {
			e.persistRuntimeStateLocked()
		}
		e.operationMu.Unlock()
	})
}

func policySummaryForPersistence(summary PolicyScanSummary) PolicyScanSummary {
	clean := summary
	clearPolicyPersistenceFailureLocked(&clean)
	return clean
}

func clearPolicyPersistenceFailureLocked(summary *PolicyScanSummary) {
	if summary == nil {
		return
	}
	if summary.Error == policyLocalStoreError {
		summary.Error = ""
	}
	details := summary.FailureDetails[:0]
	for _, detail := range summary.FailureDetails {
		if detail.ReasonCode != OperationFailurePolicyStatePersist {
			details = append(details, detail)
		}
	}
	summary.FailureDetails = details
}

func (e *PolicyEngine) scan(ctx context.Context, policy DefaultPolicy, startedAt time.Time) (PolicyScanSummary, map[string]authFingerprint, []Account) {
	summary, fingerprints, _, observedAccounts := e.scanWithState(ctx, policy, startedAt)
	return summary, fingerprints, observedAccounts
}

func (e *PolicyEngine) scanWithState(ctx context.Context, policy DefaultPolicy, startedAt time.Time) (PolicyScanSummary, map[string]authFingerprint, map[string]policyFailureBackoff, []Account) {
	summary := PolicyScanSummary{StartedAt: startedAt}
	nextFingerprints := make(map[string]authFingerprint)
	nextFailures := make(map[string]policyFailureBackoff)
	if e.host == nil {
		summary.Failed = 1
		summary.Error = "auth file scan failed"
		addPolicyFailureDetail(&summary.FailureDetails, OperationFailurePolicyAuthScan, "")
		summary.FinishedAt = e.now().UTC()
		return summary, nextFingerprints, nextFailures, nil
	}
	entries, errList := e.host.ListAuth(ctx)
	if errList != nil {
		summary.Failed = 1
		summary.Error = "auth file scan failed"
		addPolicyFailureDetail(&summary.FailureDetails, OperationFailurePolicyAuthScan, "")
		summary.FinishedAt = e.now().UTC()
		return summary, nextFingerprints, nextFailures, nil
	}
	summary.Scanned = len(entries)

	pathCounts := make(map[string]int, len(entries))
	indexCounts := make(map[string]int, len(entries))
	for _, entry := range entries {
		path := normalizedPath(entry.Path)
		if path != "" {
			pathCounts[path]++
		}
		if authIndex := strings.TrimSpace(entry.AuthIndex); authIndex != "" {
			indexCounts[authIndex]++
		}
	}
	observedAccounts := make([]Account, 0, len(entries))
	accountsByID := make(map[string]*Account, len(entries))
	for _, entry := range entries {
		observedAccounts = append(observedAccounts, projectHostEntry(entry, pathCounts, indexCounts, nil))
	}
	for index := range observedAccounts {
		accountsByID[observedAccounts[index].ID] = &observedAccounts[index]
	}

	e.mu.RLock()
	previousFingerprints := clonePolicyFingerprints(e.fingerprints)
	previousFailures := make(map[string]policyFailureBackoff, len(e.failures))
	for authIndex, failure := range e.failures {
		previousFailures[authIndex] = failure
	}
	// Quota metadata is an optional integration. Only keep an account
	// pending for the bootstrap probe when a probe is actually configured.
	// A standalone engine (or an older CPA host without this hook) must still
	// be able to apply ordinary policy fields and persist its fingerprint;
	// otherwise every scheduler tick would rediscover the same account.
	quotaProbeConfigured := e.quotaMetadataProbe != nil
	e.mu.RUnlock()

	type policyCandidate struct {
		entry       cpaapi.HostAuthFileEntry
		authIndex   string
		fingerprint authFingerprint
	}
	candidates := make([]policyCandidate, 0)
	for _, entry := range entries {
		if !eligiblePolicyEntry(entry, pathCounts, indexCounts) {
			summary.Skipped++
			continue
		}
		summary.Eligible++
		authIndex := strings.TrimSpace(entry.AuthIndex)
		planType := ""
		if account := accountsByID[authIndex]; account != nil {
			planType = account.PlanType
		}
		fingerprint := fingerprintForEntry(entry, planType)
		metadataPending := false
		if quotaProbeConfigured {
			if account := accountsByID[authIndex]; account != nil {
				metadataPending = quotaMetadataBootstrapEligible(*account) && !quotaMetadataAlreadyObserved(*account)
			}
		}
		if previous, exists := previousFingerprints[authIndex]; exists && samePolicyAccount(previous, fingerprint) && !metadataPending {
			nextFingerprints[authIndex] = fingerprint
			summary.Skipped++
			continue
		}
		if failure, exists := previousFailures[authIndex]; exists && failure.Fingerprint == fingerprint && startedAt.Before(failure.RetryAt) {
			nextFailures[authIndex] = failure
			summary.Skipped++
			continue
		}
		candidates = append(candidates, policyCandidate{entry: entry, authIndex: authIndex, fingerprint: fingerprint})
	}

	quotaCandidates := make([]Account, 0, len(candidates))
	for _, candidate := range candidates {
		if account := accountsByID[candidate.authIndex]; account != nil && quotaMetadataBootstrapEligible(*account) {
			quotaCandidates = append(quotaCandidates, *account)
		}
	}
	quotaSummary := e.probeQuotaMetadata(ctx, quotaCandidates)
	summary.QuotaMetadataProbed = quotaSummary.attempted
	summary.QuotaMetadataUpdated = quotaSummary.updated
	summary.QuotaMetadataFailed = quotaSummary.failed
	summary.FailureDetails = mergePolicyFailureDetails(summary.FailureDetails, quotaSummary.failures)
	for authIndex, planType := range quotaSummary.planTypes {
		if account := accountsByID[authIndex]; account != nil && planType != "" {
			account.PlanType = planType
		}
	}
	for _, candidate := range candidates {
		if _, failed := quotaSummary.failedIDs[candidate.authIndex]; failed {
			nextFailures[candidate.authIndex] = policyFailureBackoff{Fingerprint: candidate.fingerprint, RetryAt: startedAt.Add(policyFailureRetryInterval)}
			continue
		}
		// A configured quota probe without a management credential is not an
		// account failure. Defer it with the normal policy backoff, however, so
		// the scheduler does not run the same full candidate set every 15s while
		// CPA credentials are unavailable. An explicit RequestScan clears this
		// backoff after credentials are repaired.
		if !quotaSummary.ready {
			if account := accountsByID[candidate.authIndex]; account != nil && quotaMetadataBootstrapEligible(*account) {
				nextFailures[candidate.authIndex] = policyFailureBackoff{Fingerprint: candidate.fingerprint, RetryAt: startedAt.Add(policyFailureRetryInterval)}
			}
		}
	}

	for _, candidate := range candidates {
		if ctx.Err() != nil {
			break
		}
		account := accountsByID[candidate.authIndex]
		_, quotaProbeFailed := quotaSummary.failedIDs[candidate.authIndex]
		quotaDeferred := account != nil && quotaMetadataBootstrapEligible(*account) && (!quotaSummary.ready || quotaProbeFailed)
		if quotaDeferred {
			summary.Skipped++
			continue
		}
		if account != nil {
			candidate.fingerprint.PlanType = safeAccountPlanType(account.PlanType)
		}
		if !policy.ManagesFields() {
			if !quotaDeferred {
				nextFingerprints[candidate.authIndex] = candidate.fingerprint
			}
			summary.Skipped++
			continue
		}
		if !e.mutations.TryAcquire(policyMutationOwner) {
			summary.Skipped++
			continue
		}

		changed, errApply := func() (bool, error) {
			defer e.mutations.Release(policyMutationOwner)
			return e.reconcileEntry(ctx, candidate.entry, policy, candidate.fingerprint.PlanType)
		}()
		if errApply != nil {
			summary.Failed++
			addPolicyFailureDetail(&summary.FailureDetails, classifyPolicyFailure(errApply), candidate.authIndex)
			nextFailures[candidate.authIndex] = policyFailureBackoff{Fingerprint: candidate.fingerprint, RetryAt: startedAt.Add(policyFailureRetryInterval)}
			continue
		}
		if !quotaDeferred {
			nextFingerprints[candidate.authIndex] = candidate.fingerprint
		}
		if changed {
			summary.Changed++
		} else {
			summary.Skipped++
		}
	}
	summary.FinishedAt = e.now().UTC()
	return summary, nextFingerprints, nextFailures, observedAccounts
}

func (e *PolicyEngine) probeQuotaMetadata(ctx context.Context, accounts []Account) policyQuotaMetadataProbeSummary {
	result := policyQuotaMetadataProbeSummary{
		planTypes: make(map[string]string),
		failedIDs: make(map[string]struct{}),
		ready:     true,
	}
	eligible := make(map[string]Account, min(len(accounts), maxInspectionAccounts))
	for _, account := range accounts {
		if !quotaMetadataBootstrapEligible(account) || len(eligible) >= maxInspectionAccounts {
			continue
		}
		if _, exists := eligible[account.ID]; !exists {
			eligible[account.ID] = quotaMetadataBootstrapAccount(account)
		}
	}
	if len(eligible) == 0 {
		return result
	}

	e.mu.RLock()
	probe := e.quotaMetadataProbe
	managementKey := e.managementKey
	e.mu.RUnlock()
	if probe == nil {
		// Standalone policy engines may not have a host quota probe. Do not block
		// ordinary policy reconciliation when that optional integration is absent.
		return result
	}
	if strings.TrimSpace(managementKey) == "" {
		result.ready = false
		managementKey = ""
		return result
	}
	result.attempted = len(eligible)

	ids := mapKeys(eligible)
	sort.Strings(ids)
	type outcome struct {
		id       string
		planType string
		err      error
	}
	jobs := make(chan string)
	outcomes := make(chan outcome, len(ids))
	workers := min(policyQuotaWorkers, len(ids))
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for id := range jobs {
				planType, errProbe := probe(ctx, eligible[id], managementKey)
				select {
				case outcomes <- outcome{id: id, planType: safeAccountPlanType(planType), err: errProbe}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, id := range ids {
			select {
			case jobs <- id:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wait.Wait()
		close(outcomes)
	}()
	for item := range outcomes {
		if item.err != nil {
			result.failed++
			result.failedIDs[item.id] = struct{}{}
			addPolicyFailureDetail(&result.failures, OperationFailurePolicyQuotaMetadata, item.id)
			continue
		}
		result.updated++
		if item.planType != "" {
			result.planTypes[item.id] = item.planType
		}
	}
	managementKey = ""
	return result
}

func classifyPolicyFailure(err error) string {
	message := ""
	if err != nil {
		message = strings.ToLower(strings.TrimSpace(err.Error()))
	}
	switch {
	case strings.HasPrefix(message, "get auth file:"):
		return OperationFailurePolicyAuthRead
	case message == "auth index changed":
		return OperationFailurePolicyAccountIdentity
	case message == "auth source changed" || message == "auth filename changed":
		return OperationFailurePolicyAuthSource
	case message == "auth filename is invalid":
		return OperationFailurePolicyAuthFilename
	case strings.HasPrefix(message, "project auth account:"):
		return OperationFailurePolicyAuthProjection
	case message == "auth json is invalid" || message == "auth json must be an object":
		return OperationFailurePolicyAuthJSON
	case strings.HasPrefix(message, "encode policy field:") || strings.HasPrefix(message, "encode updated auth json:"):
		return OperationFailurePolicyAuthUpdate
	case strings.HasPrefix(message, "save auth file:"):
		return OperationFailurePolicyAuthSave
	case message == "conditional model policy is not armed":
		return OperationFailurePolicyModelPolicyUnavailable
	case strings.HasPrefix(message, "apply conditional model policy:"):
		return OperationFailurePolicyModelPolicyApply
	default:
		return OperationFailurePolicyAuthUpdate
	}
}

func addPolicyFailureDetail(details *[]OperationFailureDetail, reasonCode, accountID string) {
	if details == nil {
		return
	}
	reasonCode = safeOperationFailureReason(reasonCode)
	if reasonCode == "" {
		return
	}
	accountID = safeOperationFailureAccountID(accountID)
	for index := range *details {
		if (*details)[index].ReasonCode != reasonCode {
			continue
		}
		(*details)[index].Count = boundedCounter((*details)[index].Count + 1)
		if accountID != "" && len((*details)[index].SampleAccountIDs) < policyFailureSampleLimit && !containsString((*details)[index].SampleAccountIDs, accountID) {
			(*details)[index].SampleAccountIDs = append((*details)[index].SampleAccountIDs, accountID)
		}
		return
	}
	detail := OperationFailureDetail{ReasonCode: reasonCode, Count: 1}
	if accountID != "" {
		detail.SampleAccountIDs = []string{accountID}
	}
	*details = append(*details, detail)
}

func mergePolicyFailureDetails(left, right []OperationFailureDetail) []OperationFailureDetail {
	merged := normalizeOperationFailureDetails(left)
	for _, incoming := range normalizeOperationFailureDetails(right) {
		matched := false
		for index := range merged {
			if merged[index].ReasonCode != incoming.ReasonCode {
				continue
			}
			merged[index].Count = boundedCounter(merged[index].Count + incoming.Count)
			for _, accountID := range incoming.SampleAccountIDs {
				if len(merged[index].SampleAccountIDs) >= policyFailureSampleLimit {
					break
				}
				if !containsString(merged[index].SampleAccountIDs, accountID) {
					merged[index].SampleAccountIDs = append(merged[index].SampleAccountIDs, accountID)
				}
			}
			matched = true
			break
		}
		if !matched {
			merged = append(merged, incoming)
		}
	}
	return normalizeOperationFailureDetails(merged)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func samePolicyAccount(left, right authFingerprint) bool {
	// A plan transition (for example free -> plus/k12/team) changes the
	// inputs used by conditional policies. Treat it as a new policy state
	// while intentionally ignoring file size/mtime churn from CPA rewrites.
	return left.Name == right.Name &&
		left.Path == right.Path &&
		left.PlanType == right.PlanType
}

func (e *PolicyEngine) reconcileEntry(ctx context.Context, entry cpaapi.HostAuthFileEntry, policy DefaultPolicy, refreshedPlanType string) (bool, error) {
	detail, errGet := e.host.GetAuth(ctx, strings.TrimSpace(entry.AuthIndex))
	if errGet != nil {
		return false, fmt.Errorf("get auth file: %w", errGet)
	}
	if detail.AuthIndex != "" && strings.TrimSpace(detail.AuthIndex) != strings.TrimSpace(entry.AuthIndex) {
		return false, fmt.Errorf("auth index changed")
	}
	entryPath := normalizedPath(entry.Path)
	detailPath := normalizedPath(detail.Path)
	if entryPath != "" && detailPath != "" && entryPath != detailPath {
		return false, fmt.Errorf("auth source changed")
	}
	name := strings.TrimSpace(firstNonEmpty(detail.Name, entry.Name))
	if !safeAuthJSONName(name) {
		return false, fmt.Errorf("auth filename is invalid")
	}
	if entryName := strings.TrimSpace(entry.Name); entryName != "" && entryName != name {
		return false, fmt.Errorf("auth filename changed")
	}

	account := projectHostEntry(entry, map[string]int{normalizedPath(entry.Path): 1}, map[string]int{strings.TrimSpace(entry.AuthIndex): 1}, nil)
	if errEnrich := enrichAccount(&account, detail); errEnrich != nil {
		return false, fmt.Errorf("project auth account: %w", errEnrich)
	}
	if planType := safeAccountPlanType(refreshedPlanType); planType != "" {
		account.PlanType = planType
	}
	basePolicy := policy
	basePolicy.ConditionalRules = nil
	if !policy.Enabled {
		basePolicy.Priority = nil
		basePolicy.Websockets = nil
		basePolicy.ProxyProfileID = nil
		basePolicy.AIProviderProxyProfileID = nil
	}
	if basePolicy.ProxyProfileID != nil {
		e.mu.RLock()
		resolver := e.proxyProfiles
		e.mu.RUnlock()
		if resolver == nil {
			return false, fmt.Errorf("proxy profile resolver is unavailable")
		}
		proxyURL, ok := resolveProxyProfileForProvider(resolver, *basePolicy.ProxyProfileID, firstNonEmpty(account.Provider, account.Type))
		if !ok {
			return false, fmt.Errorf("proxy profile is unavailable")
		}
		basePolicy.proxyURL = &proxyURL
	}
	updated, _, changed, errApply := applyDefaultPolicy(detail.JSON, basePolicy, applyMissing)
	if errApply != nil {
		return false, errApply
	}
	resolved := resolveConditionalPolicy(policy, account)
	if resolved.PriorityFromRule || resolved.DisabledFromRule || resolved.ConcurrencyFromRule || resolved.QuotaPolicyFromRule || resolved.NoteFromRule || resolved.PrefixFromRule || resolved.ProxyURLFromRule || resolved.WebsocketsFromRule || resolved.HeadersFromRule || resolved.CodexIdentityFromRule || resolved.ProxyProfileFromRule {
		override := DefaultPolicy{}
		if resolved.DisabledFromRule {
			override.Disabled = resolved.Disabled
		}
		if resolved.PriorityFromRule {
			override.Priority = resolved.Priority
		}
		if resolved.ConcurrencyFromRule {
			override.ConcurrencyLimit = resolved.ConcurrencyLimit
		}
		if resolved.QuotaPolicyFromRule {
			override.QuotaPolicy = resolved.QuotaPolicy
		}
		if resolved.NoteFromRule {
			override.Note = resolved.Note
		}
		if resolved.PrefixFromRule {
			override.Prefix = resolved.Prefix
		}
		if resolved.ProxyURLFromRule {
			override.ProxyURL = resolved.ProxyURL
		}
		if resolved.WebsocketsFromRule {
			override.Websockets = resolved.Websockets
		}
		if resolved.HeadersFromRule {
			override.Headers = resolved.Headers
		}
		if resolved.CodexIdentityFromRule {
			override.CodexIdentity = resolved.CodexIdentity
		}
		if resolved.ProxyProfileFromRule && resolved.ProxyProfileID != nil {
			e.mu.RLock()
			resolver := e.proxyProfiles
			e.mu.RUnlock()
			if resolver == nil {
				return false, fmt.Errorf("proxy profile resolver is unavailable")
			}
			proxyURL, ok := resolveProxyProfileForProvider(resolver, *resolved.ProxyProfileID, firstNonEmpty(account.Provider, account.Type))
			if !ok {
				return false, fmt.Errorf("proxy profile is unavailable")
			}
			override.proxyURL = &proxyURL
		}
		var conditionalChanged bool
		updated, _, conditionalChanged, errApply = applyDefaultPolicy(updated, override, applyForce)
		if errApply != nil {
			return false, errApply
		}
		changed = changed || conditionalChanged
	}
	pluginChanged, errPlugin := e.applyPluginPolicy(ctx, account, name, basePolicy, applyMissing)
	if errPlugin != nil {
		return false, errPlugin
	}
	changed = changed || pluginChanged
	if resolved.DisabledFromRule || resolved.ConcurrencyFromRule || resolved.QuotaPolicyFromRule || resolved.CodexIdentityFromRule {
		override := DefaultPolicy{}
		override.Disabled = resolved.Disabled
		override.ConcurrencyLimit = resolved.ConcurrencyLimit
		override.QuotaPolicy = resolved.QuotaPolicy
		override.CodexIdentity = resolved.CodexIdentity
		conditionalPluginChanged, errConditionalPlugin := e.applyPluginPolicy(ctx, account, name, override, applyForce)
		if errConditionalPlugin != nil {
			return false, errConditionalPlugin
		}
		changed = changed || conditionalPluginChanged
	}
	if !changed {
		if resolved.ModelPolicy == nil {
			return false, nil
		}
	} else if _, errSave := e.host.SaveAuth(ctx, name, updated); errSave != nil {
		return false, fmt.Errorf("save auth file: %w", errSave)
	}
	if resolved.ModelPolicy != nil {
		e.mu.RLock()
		applier := e.modelPolicyApplier
		managementKey := e.managementKey
		e.mu.RUnlock()
		if applier == nil || strings.TrimSpace(managementKey) == "" {
			return changed, fmt.Errorf("conditional model policy is not armed")
		}
		modelChanged, errModelPolicy := applier(ctx, account, *resolved.ModelPolicy, managementKey)
		managementKey = ""
		if errModelPolicy != nil {
			return changed, fmt.Errorf("apply conditional model policy: %w", errModelPolicy)
		}
		changed = changed || modelChanged
	}
	return changed, nil
}

func (e *PolicyEngine) applyPluginPolicy(ctx context.Context, account Account, name string, policy DefaultPolicy, mode policyApplyMode) (bool, error) {
	if e == nil {
		return false, nil
	}
	changed := false
	e.mu.RLock()
	concurrency := e.concurrency
	quotaPolicies := e.quotaPolicies
	identityOverrides := e.codexIdentityOverrides
	managementKey := e.managementKey
	config := e.config
	e.mu.RUnlock()
	if policy.ConcurrencyLimit != nil && concurrency != nil {
		current := concurrency.Summary(account.AuthID)
		if mode == applyForce || current.Limit == 0 {
			if current.Limit != *policy.ConcurrencyLimit {
				if err := concurrency.SetLimit(account, *policy.ConcurrencyLimit); err != nil {
					return changed, fmt.Errorf("apply account concurrency policy: %w", err)
				}
				changed = true
			}
		}
	}
	if policy.QuotaPolicy != nil && quotaPolicies != nil {
		current := quotaPolicies.AccountPolicy(account.ID)
		if mode == applyForce || quotaPolicyEmpty(current) {
			if !reflect.DeepEqual(current, *policy.QuotaPolicy) {
				if err := quotaPolicies.SetAccountPolicy(account.ID, *policy.QuotaPolicy); err != nil {
					return changed, fmt.Errorf("apply account quota policy: %w", err)
				}
				changed = true
			}
		}
	}
	if policy.CodexIdentity != nil && identityOverrides != nil {
		current, exists := identityOverrides.Account(account.ID)
		if mode == applyForce || !exists {
			if !reflect.DeepEqual(current, *policy.CodexIdentity) {
				if err := identityOverrides.SetAccount(account.ID, *policy.CodexIdentity); err != nil {
					return changed, fmt.Errorf("apply Codex identity policy: %w", err)
				}
				changed = true
			}
		}
	}
	if policy.Disabled != nil && account.Disabled != *policy.Disabled {
		if strings.TrimSpace(managementKey) == "" {
			return changed, fmt.Errorf("account disabled policy is not armed")
		}
		client, errClient := newManagementClient(resolveManagementBaseURL(config.ManagementBaseURL), managementKey, nil)
		managementKey = ""
		if errClient != nil {
			return changed, fmt.Errorf("create management client for disabled policy: %w", errClient)
		}
		if err := client.PatchDisabled(ctx, name, *policy.Disabled); err != nil {
			return changed, fmt.Errorf("apply account disabled policy: %w", err)
		}
		changed = true
	}
	return changed, nil
}

func normalizeDefaultPolicy(policy DefaultPolicy) DefaultPolicy {
	policy.CodexQuotaMetadataProbeEnabled = true
	policy.ApplyMode = policyApplyModeMissing
	policy.ScanIntervalSeconds = clampPolicyScanInterval(policy.ScanIntervalSeconds)
	if policy.ProxyProfileID != nil {
		id := strings.ToLower(strings.TrimSpace(*policy.ProxyProfileID))
		policy.ProxyProfileID = &id
	}
	if policy.AIProviderProxyProfileID != nil {
		id := strings.ToLower(strings.TrimSpace(*policy.AIProviderProxyProfileID))
		policy.AIProviderProxyProfileID = &id
	}
	if policy.ProxyURL != nil {
		value := strings.TrimSpace(*policy.ProxyURL)
		policy.ProxyURL = &value
	}
	if policy.Note != nil {
		value := strings.TrimSpace(*policy.Note)
		policy.Note = &value
	}
	if policy.Prefix != nil {
		value := strings.TrimSpace(*policy.Prefix)
		policy.Prefix = &value
	}
	return cloneDefaultPolicy(policy)
}

func validateDefaultPolicy(policy DefaultPolicy) (DefaultPolicy, error) {
	mode := strings.ToLower(strings.TrimSpace(policy.ApplyMode))
	if mode != "" && mode != policyApplyModeMissing {
		return DefaultPolicy{}, fmt.Errorf("apply_mode must be missing")
	}
	rules, errRules := validateConditionalPolicyRules(policy.ConditionalRules)
	if errRules != nil {
		return DefaultPolicy{}, errRules
	}
	policy.ConditionalRules = rules
	patch := BatchPatch{Disabled: policy.Disabled, Priority: policy.Priority, Note: policy.Note, Prefix: policy.Prefix, ProxyURL: policy.ProxyURL, ProxyProfileID: policy.ProxyProfileID, Websockets: policy.Websockets, Headers: policy.Headers, ModelPolicy: policy.ModelPolicy, ConcurrencyLimit: policy.ConcurrencyLimit, QuotaPolicy: policy.QuotaPolicy, CodexIdentity: policy.CodexIdentity}
	if !patch.Empty() {
		validated, errValidate := patch.Validate()
		if errValidate != nil {
			return DefaultPolicy{}, errValidate
		}
		policy.Disabled, policy.Priority, policy.Note, policy.Prefix, policy.ProxyURL = validated.Disabled, validated.Priority, validated.Note, validated.Prefix, validated.ProxyURL
		policy.ProxyProfileID, policy.Websockets, policy.Headers, policy.ModelPolicy = validated.ProxyProfileID, validated.Websockets, validated.Headers, validated.ModelPolicy
		policy.ConcurrencyLimit, policy.QuotaPolicy, policy.CodexIdentity = validated.ConcurrencyLimit, validated.QuotaPolicy, validated.CodexIdentity
	}
	if policy.Enabled && !policy.ManagesFields() && !policy.ManagesNewAccountProbe() {
		return DefaultPolicy{}, fmt.Errorf("enabled policy requires at least one automation action")
	}
	policy = normalizeDefaultPolicy(policy)
	return policy, nil
}

func (policy DefaultPolicy) ManagesFields() bool {
	return policy.ManagesAccountFields() || policy.ManagesAIProviderProxy()
}

func (policy DefaultPolicy) ManagesAccountFields() bool {
	if policy.Enabled && policy.hasAccountActions() {
		return true
	}
	for _, rule := range policy.ConditionalRules {
		if rule.Enabled && conditionalActionsManageAccount(rule.Actions) {
			return true
		}
	}
	return false
}

func (policy DefaultPolicy) hasAccountActions() bool {
	return policy.Disabled != nil || policy.Priority != nil || policy.Note != nil || policy.Prefix != nil || policy.ProxyURL != nil || policy.ProxyProfileID != nil || policy.Websockets != nil || policy.Headers != nil || policy.ModelPolicy != nil || policy.ConcurrencyLimit != nil || policy.QuotaPolicy != nil || policy.CodexIdentity != nil
}

func conditionalActionsManageAccount(actions ConditionalPolicyActions) bool {
	return actions.Disabled != nil || actions.Priority != nil || actions.Note != nil || actions.Prefix != nil || actions.ProxyURL != nil || actions.ProxyProfileID != nil || actions.Websockets != nil || actions.Headers != nil || actions.ModelPolicy != nil || actions.ConcurrencyLimit != nil || actions.QuotaPolicy != nil || actions.CodexIdentity != nil
}

func (policy DefaultPolicy) ManagesAIProviderProxy() bool {
	if policy.Enabled && policy.AIProviderProxyProfileID != nil {
		return true
	}
	for _, rule := range policy.ConditionalRules {
		if rule.Enabled && rule.Actions.AIProviderProxyProfileID != nil {
			return true
		}
	}
	return false
}

func (policy DefaultPolicy) ManagesNewAccountProbe() bool {
	if policy.NewAccountModelProbeEnabled {
		return true
	}
	for _, rule := range policy.ConditionalRules {
		if rule.Enabled && rule.Actions.NewAccountModelProbe != nil && *rule.Actions.NewAccountModelProbe {
			return true
		}
	}
	return false
}

func (policy DefaultPolicy) Fields() []string {
	fields := make([]string, 0, 12)
	if policy.Disabled != nil {
		fields = append(fields, "disabled")
	}
	if policy.Priority != nil {
		fields = append(fields, policyFieldPriority)
	}
	if policy.Note != nil {
		fields = append(fields, "note")
	}
	if policy.Prefix != nil {
		fields = append(fields, "prefix")
	}
	if policy.ProxyURL != nil {
		fields = append(fields, policyFieldProxyURL)
	}
	if policy.Websockets != nil {
		fields = append(fields, policyFieldWebsockets)
	}
	if policy.ProxyProfileID != nil {
		fields = append(fields, policyFieldProxyURL)
	}
	if policy.Headers != nil {
		fields = append(fields, "headers")
	}
	if policy.ModelPolicy != nil {
		fields = append(fields, "model_policy")
	}
	if policy.ConcurrencyLimit != nil {
		fields = append(fields, "concurrency_limit")
	}
	if policy.QuotaPolicy != nil {
		fields = append(fields, "quota_policy")
	}
	if policy.CodexIdentity != nil {
		fields = append(fields, "codex_identity")
	}
	return fields
}

func cloneDefaultPolicy(policy DefaultPolicy) DefaultPolicy {
	clone := policy
	clone.Priority = cloneIntPointer(policy.Priority)
	clone.Disabled = cloneBoolPointer(policy.Disabled)
	clone.ConcurrencyLimit = cloneIntPointer(policy.ConcurrencyLimit)
	clone.Note = cloneStringPointer(policy.Note)
	clone.Prefix = cloneStringPointer(policy.Prefix)
	clone.ProxyURL = cloneStringPointer(policy.ProxyURL)
	clone.Websockets = cloneBoolPointer(policy.Websockets)
	if policy.Headers != nil {
		value := cloneHeaderPatch(*policy.Headers)
		clone.Headers = &value
	}
	if policy.ModelPolicy != nil {
		value := cloneModelPolicyPatch(*policy.ModelPolicy)
		clone.ModelPolicy = &value
	}
	if policy.QuotaPolicy != nil {
		value := *policy.QuotaPolicy
		clone.QuotaPolicy = &value
	}
	if policy.CodexIdentity != nil {
		value := cloneCodexIdentityOverride(*policy.CodexIdentity)
		clone.CodexIdentity = &value
	}
	clone.ProxyProfileID = cloneStringPointer(policy.ProxyProfileID)
	clone.AIProviderProxyProfileID = cloneStringPointer(policy.AIProviderProxyProfileID)
	clone.ConditionalRules = cloneConditionalPolicyRules(policy.ConditionalRules)
	return clone
}

func defaultPolicyEqual(left, right DefaultPolicy) bool {
	left = normalizeDefaultPolicy(left)
	right = normalizeDefaultPolicy(right)
	return left.Enabled == right.Enabled && left.ApplyMode == right.ApplyMode &&
		left.NewAccountModelProbeEnabled == right.NewAccountModelProbeEnabled &&
		left.CodexQuotaMetadataProbeEnabled == right.CodexQuotaMetadataProbeEnabled &&
		left.ScanIntervalSeconds == right.ScanIntervalSeconds && managedPolicyEqual(left, right) && optionalBoolEqual(left.Disabled, right.Disabled) && optionalIntEqual(left.ConcurrencyLimit, right.ConcurrencyLimit) && optionalStringEqual(left.Note, right.Note) && optionalStringEqual(left.Prefix, right.Prefix) && optionalStringEqual(left.ProxyURL, right.ProxyURL) && reflect.DeepEqual(left.Headers, right.Headers) && reflect.DeepEqual(left.ModelPolicy, right.ModelPolicy) && reflect.DeepEqual(left.QuotaPolicy, right.QuotaPolicy) && reflect.DeepEqual(left.CodexIdentity, right.CodexIdentity) && optionalStringEqual(left.ProxyProfileID, right.ProxyProfileID) && optionalStringEqual(left.AIProviderProxyProfileID, right.AIProviderProxyProfileID) &&
		reflect.DeepEqual(left.ConditionalRules, right.ConditionalRules)
}

func clampPolicyScanInterval(seconds int) int {
	if seconds == 0 {
		return defaultPolicyScanIntervalSeconds
	}
	if seconds < minPolicyScanIntervalSeconds {
		return minPolicyScanIntervalSeconds
	}
	if seconds > maxPolicyScanIntervalSeconds {
		return maxPolicyScanIntervalSeconds
	}
	return seconds
}

func eligiblePolicyEntry(entry cpaapi.HostAuthFileEntry, pathCounts, indexCounts map[string]int) bool {
	authIndex := strings.TrimSpace(entry.AuthIndex)
	path := normalizedPath(entry.Path)
	name := strings.TrimSpace(entry.Name)
	return authIndex != "" && indexCounts[authIndex] == 1 &&
		!entry.RuntimeOnly && strings.EqualFold(strings.TrimSpace(entry.Source), "file") &&
		path != "" && pathCounts[path] == 1 && safeAuthJSONName(name)
}

func fingerprintForEntry(entry cpaapi.HostAuthFileEntry, planType string) authFingerprint {
	fingerprint := authFingerprint{
		Name:     strings.TrimSpace(entry.Name),
		Path:     normalizedPath(entry.Path),
		Size:     entry.Size,
		PlanType: safeAccountPlanType(planType),
	}
	if !entry.ModTime.IsZero() {
		fingerprint.ModTimeSet = true
		fingerprint.ModTimeNS = entry.ModTime.UnixNano()
	}
	return fingerprint
}

func applyDefaultPolicy(raw json.RawMessage, policy DefaultPolicy, mode policyApplyMode) (json.RawMessage, []string, bool, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || !json.Valid(raw) {
		return nil, nil, false, fmt.Errorf("auth json is invalid")
	}
	var document map[string]json.RawMessage
	if errDecode := json.Unmarshal(raw, &document); errDecode != nil || document == nil {
		return nil, nil, false, fmt.Errorf("auth json must be an object")
	}

	applied := make([]string, 0, 2)
	apply := func(field string, value any) error {
		desired, errMarshal := json.Marshal(value)
		if errMarshal != nil {
			return fmt.Errorf("encode policy field: %w", errMarshal)
		}
		current, exists := document[field]
		if mode == applyMissing && exists {
			return nil
		}
		if exists && bytes.Equal(bytes.TrimSpace(current), desired) {
			return nil
		}
		document[field] = desired
		applied = append(applied, field)
		return nil
	}
	if policy.Priority != nil {
		if errApply := apply(policyFieldPriority, *policy.Priority); errApply != nil {
			return nil, nil, false, errApply
		}
	}
	if policy.Websockets != nil {
		if errApply := apply(policyFieldWebsockets, *policy.Websockets); errApply != nil {
			return nil, nil, false, errApply
		}
	}
	if policy.proxyURL != nil {
		if errApply := apply(policyFieldProxyURL, *policy.proxyURL); errApply != nil {
			return nil, nil, false, errApply
		}
	}
	if policy.Note != nil {
		if errApply := apply(policyFieldNote, *policy.Note); errApply != nil {
			return nil, nil, false, errApply
		}
	}
	if policy.Prefix != nil {
		if errApply := apply(policyFieldPrefix, *policy.Prefix); errApply != nil {
			return nil, nil, false, errApply
		}
	}
	if policy.Headers != nil {
		if errApply := applyHeaders(document, *policy.Headers, mode, &applied); errApply != nil {
			return nil, nil, false, errApply
		}
	}
	if len(applied) == 0 {
		return append(json.RawMessage(nil), raw...), nil, false, nil
	}
	updated, errMarshal := json.Marshal(document)
	if errMarshal != nil {
		return nil, nil, false, fmt.Errorf("encode updated auth json: %w", errMarshal)
	}
	return updated, applied, true, nil
}

func applyHeaders(document map[string]json.RawMessage, patch HeaderPatch, mode policyApplyMode, applied *[]string) error {
	if document == nil || applied == nil {
		return nil
	}
	currentRaw, exists := document[policyFieldHeaders]
	if mode == applyMissing && exists {
		return nil
	}
	current := make(map[string]any)
	if exists && len(bytes.TrimSpace(currentRaw)) > 0 {
		if err := json.Unmarshal(currentRaw, &current); err != nil {
			return fmt.Errorf("auth headers are invalid: %w", err)
		}
	}
	changed := false
	for name, value := range patch.Set {
		if existing, ok := current[name]; !ok || existing != value {
			current[name] = value
			changed = true
		}
	}
	for _, name := range patch.Remove {
		if _, ok := current[name]; ok {
			delete(current, name)
			changed = true
		}
	}
	if !changed && exists {
		return nil
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		return fmt.Errorf("encode policy headers: %w", err)
	}
	document[policyFieldHeaders] = encoded
	*applied = append(*applied, policyFieldHeaders)
	return nil
}

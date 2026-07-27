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
	policyMutationRetryInterval      = time.Second
	policyApplyModeMissing           = "missing"

	policyFieldPriority   = "priority"
	policyFieldWebsockets = "websockets"
	policyMutationOwner   = "default-policy-scan"
	policyQuotaWorkers    = 4
	policyLocalStoreError = "default policy scan status could not be persisted locally"
	configuredPolicyError = "configured default policy could not be loaded"
)

var ErrPolicyStorageUnavailable = errors.New("default policy storage is unavailable; configure data_dir to a writable directory")

type DefaultPolicy struct {
	Enabled                        bool                    `json:"enabled" yaml:"enabled"`
	NewAccountModelProbeEnabled    bool                    `json:"new_account_model_probe_enabled" yaml:"new_account_model_probe_enabled"`
	CodexQuotaMetadataProbeEnabled bool                    `json:"codex_quota_metadata_probe_enabled" yaml:"codex_quota_metadata_probe_enabled"`
	ApplyMode                      string                  `json:"apply_mode" yaml:"apply_mode"`
	ScanIntervalSeconds            int                     `json:"scan_interval_seconds" yaml:"scan_interval_seconds"`
	Priority                       *int                    `json:"priority" yaml:"priority"`
	Websockets                     *bool                   `json:"websockets" yaml:"websockets"`
	ConditionalRules               []ConditionalPolicyRule `json:"conditional_rules,omitempty" yaml:"conditional_rules,omitempty"`
}

type PolicyScanSummary struct {
	StartedAt            time.Time `json:"started_at,omitempty"`
	FinishedAt           time.Time `json:"finished_at,omitempty"`
	Scanned              int       `json:"scanned"`
	Eligible             int       `json:"eligible"`
	Changed              int       `json:"changed"`
	Skipped              int       `json:"skipped"`
	Failed               int       `json:"failed"`
	QuotaMetadataProbed  int       `json:"quota_metadata_probed"`
	QuotaMetadataUpdated int       `json:"quota_metadata_updated"`
	QuotaMetadataFailed  int       `json:"quota_metadata_failed"`
	Error                string    `json:"error,omitempty"`
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

type policyQuotaMetadataProbe func(context.Context, Account, string) (string, error)

type policyQuotaMetadataProbeSummary struct {
	planTypes map[string]string
	attempted int
	updated   int
	failed    int
}

type PolicyEngine struct {
	mu                 sync.RWMutex
	operationMu        sync.Mutex
	wait               sync.WaitGroup
	host               AuthHost
	mutations          *MutationCoordinator
	observer           interface{ ObserveAccounts([]Account) }
	modelPolicyApplier func(context.Context, Account, ModelPolicyPatch, string) (bool, error)
	quotaMetadataProbe policyQuotaMetadataProbe
	managementKey      string
	backgroundOwner    BackgroundWorkOwner
	config             Config
	store              string
	policy             DefaultPolicy
	lastScan           PolicyScanSummary
	running            bool
	scanStarted        time.Time
	fingerprints       map[string]authFingerprint
	wake               chan struct{}
	cancel             context.CancelFunc
	started            bool
	closed             bool
	now                func() time.Time
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
		e.managementKey = managementKey
	}
	e.mu.Unlock()
	managementKey = ""
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
	sameStore := e.started && e.store == storePath
	e.mu.RUnlock()

	if sameStore {
		e.mu.Lock()
		e.config = config
		if hasConfiguredPolicy {
			if errConfiguredPolicy != nil {
				e.policy = normalizeDefaultPolicy(DefaultPolicy{})
				e.lastScan.Error = configuredPolicyError
				e.fingerprints = make(map[string]authFingerprint)
			} else {
				if e.lastScan.Error == configuredPolicyError {
					e.lastScan.Error = ""
				}
				if !defaultPolicyEqual(e.policy, configuredPolicy) {
					e.policy = configuredPolicy
					e.fingerprints = make(map[string]authFingerprint)
				}
			}
		}
		e.mu.Unlock()
		e.operationMu.Unlock()
		e.requestScan()
		return
	}

	policy := normalizeDefaultPolicy(DefaultPolicy{})
	lastScan := PolicyScanSummary{}
	if hasConfiguredPolicy {
		if errConfiguredPolicy != nil {
			lastScan.Error = configuredPolicyError
		} else {
			policy = configuredPolicy
			if _, loadedScan, errLoad := loadPolicyState(storePath); errLoad == nil {
				lastScan = loadedScan
			}
		}
	} else {
		loadedPolicy, loadedScan, errLoad := loadPolicyState(storePath)
		if errLoad == nil {
			policy = loadedPolicy
			lastScan = loadedScan
		} else if !errors.Is(errLoad, os.ErrNotExist) {
			lastScan.Error = "stored default policy could not be loaded"
		}
	}

	e.mu.Lock()
	e.config = config
	e.store = storePath
	e.policy = policy
	e.lastScan = lastScan
	e.fingerprints = make(map[string]authFingerprint)
	start := !e.started && !e.closed
	if start {
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
	e.mu.RUnlock()
	if closed || strings.TrimSpace(storePath) == "" {
		e.operationMu.Unlock()
		return DefaultPolicy{}, ErrPolicyStorageUnavailable
	}
	errSave := savePolicyState(storePath, normalized, lastScan)
	e.mu.Lock()
	e.policy = normalized
	e.fingerprints = make(map[string]authFingerprint)
	if errSave != nil {
		e.lastScan.Error = policyLocalStoreError
	} else if e.lastScan.Error == policyLocalStoreError {
		e.lastScan.Error = ""
	}
	e.mu.Unlock()
	e.operationMu.Unlock()

	e.requestScan()
	return cloneDefaultPolicy(normalized), nil
}

func (e *PolicyEngine) RequestScan() PolicySnapshot {
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
}

func (e *PolicyEngine) run(ctx context.Context) {
	defer e.wait.Done()
	for {
		if ctx.Err() != nil {
			return
		}
		retrySoon := e.reconcile(ctx)

		interval := e.scanInterval()
		if retrySoon {
			interval = policyMutationRetryInterval
		}
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
	e.mu.RUnlock()
	applyDefaults := policy.ManagesFields()
	if !applyDefaults && !policy.ManagesNewAccountProbe() && !policy.CodexQuotaMetadataProbeEnabled {
		return false
	}
	if applyDefaults {
		if !e.mutations.TryAcquire(policyMutationOwner) {
			return true
		}
	}

	startedAt := e.now().UTC()
	e.mu.Lock()
	e.running = true
	e.scanStarted = startedAt
	e.mu.Unlock()

	summary, fingerprints, observedAccounts := e.scan(ctx, policy, startedAt)
	e.mu.Lock()
	e.running = false
	e.scanStarted = time.Time{}
	if ctx.Err() == nil {
		e.lastScan = summary
		e.fingerprints = fingerprints
	}
	storePath := e.store
	currentPolicy := cloneDefaultPolicy(e.policy)
	lastScan := e.lastScan
	e.mu.Unlock()
	if applyDefaults {
		e.mutations.Release(policyMutationOwner)
	}
	if ctx.Err() != nil {
		return false
	}
	e.mu.RLock()
	observer := e.observer
	e.mu.RUnlock()
	if observer != nil && observedAccounts != nil {
		observer.ObserveAccounts(observedAccounts)
	}
	if errSave := savePolicyState(storePath, currentPolicy, lastScan); errSave != nil {
		e.mu.Lock()
		e.lastScan.Error = policyLocalStoreError
		e.mu.Unlock()
	}
	return false
}

func (e *PolicyEngine) scan(ctx context.Context, policy DefaultPolicy, startedAt time.Time) (PolicyScanSummary, map[string]authFingerprint, []Account) {
	summary := PolicyScanSummary{StartedAt: startedAt}
	nextFingerprints := make(map[string]authFingerprint)
	if e.host == nil {
		summary.Failed = 1
		summary.Error = "auth file scan failed"
		summary.FinishedAt = e.now().UTC()
		return summary, nextFingerprints, nil
	}
	entries, errList := e.host.ListAuth(ctx)
	if errList != nil {
		summary.Failed = 1
		summary.Error = "auth file scan failed"
		summary.FinishedAt = e.now().UTC()
		return summary, nextFingerprints, nil
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
	for _, entry := range entries {
		observedAccounts = append(observedAccounts, projectHostEntry(entry, pathCounts, indexCounts, nil))
	}
	if policy.CodexQuotaMetadataProbeEnabled {
		quotaSummary := e.probeQuotaMetadata(ctx, observedAccounts)
		summary.QuotaMetadataProbed = quotaSummary.attempted
		summary.QuotaMetadataUpdated = quotaSummary.updated
		summary.QuotaMetadataFailed = quotaSummary.failed
		summary.Failed += quotaSummary.failed
		for index := range observedAccounts {
			if planType := quotaSummary.planTypes[observedAccounts[index].ID]; planType != "" {
				observedAccounts[index].PlanType = planType
			}
		}
	}
	planTypes := make(map[string]string, len(observedAccounts))
	for _, account := range observedAccounts {
		planTypes[account.ID] = account.PlanType
	}
	if !policy.ManagesFields() {
		summary.Skipped = len(entries)
		summary.FinishedAt = e.now().UTC()
		return summary, nextFingerprints, observedAccounts
	}

	e.mu.RLock()
	previousFingerprints := make(map[string]authFingerprint, len(e.fingerprints))
	for authIndex, fingerprint := range e.fingerprints {
		previousFingerprints[authIndex] = fingerprint
	}
	e.mu.RUnlock()

	for _, entry := range entries {
		if ctx.Err() != nil {
			break
		}
		if !eligiblePolicyEntry(entry, pathCounts, indexCounts) {
			summary.Skipped++
			continue
		}
		summary.Eligible++
		authIndex := strings.TrimSpace(entry.AuthIndex)
		fingerprint := fingerprintForEntry(entry, planTypes[authIndex])
		if previous, exists := previousFingerprints[authIndex]; exists && previous == fingerprint {
			nextFingerprints[authIndex] = fingerprint
			summary.Skipped++
			continue
		}

		changed, errApply := e.reconcileEntry(ctx, entry, policy, planTypes[authIndex])
		if errApply != nil {
			summary.Failed++
			continue
		}
		nextFingerprints[authIndex] = fingerprint
		if changed {
			summary.Changed++
		} else {
			summary.Skipped++
		}
	}
	summary.FinishedAt = e.now().UTC()
	return summary, nextFingerprints, observedAccounts
}

func (e *PolicyEngine) probeQuotaMetadata(ctx context.Context, accounts []Account) policyQuotaMetadataProbeSummary {
	result := policyQuotaMetadataProbeSummary{planTypes: make(map[string]string)}
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
	result.attempted = len(eligible)
	if probe == nil || strings.TrimSpace(managementKey) == "" {
		result.failed = len(eligible)
		managementKey = ""
		return result
	}

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
	}
	updated, _, changed, errApply := applyDefaultPolicy(detail.JSON, basePolicy, applyMissing)
	if errApply != nil {
		return false, errApply
	}
	resolved := resolveConditionalPolicy(policy, account)
	if resolved.PriorityFromRule || resolved.WebsocketsFromRule {
		override := DefaultPolicy{}
		if resolved.PriorityFromRule {
			override.Priority = resolved.Priority
		}
		if resolved.WebsocketsFromRule {
			override.Websockets = resolved.Websockets
		}
		var conditionalChanged bool
		updated, _, conditionalChanged, errApply = applyDefaultPolicy(updated, override, applyForce)
		if errApply != nil {
			return false, errApply
		}
		changed = changed || conditionalChanged
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

func normalizeDefaultPolicy(policy DefaultPolicy) DefaultPolicy {
	policy.ApplyMode = policyApplyModeMissing
	policy.ScanIntervalSeconds = clampPolicyScanInterval(policy.ScanIntervalSeconds)
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
	policy = normalizeDefaultPolicy(policy)
	if policy.Enabled && !policy.ManagesFields() && !policy.ManagesNewAccountProbe() && !policy.CodexQuotaMetadataProbeEnabled {
		return DefaultPolicy{}, fmt.Errorf("enabled policy requires at least one automation action")
	}
	return policy, nil
}

func (policy DefaultPolicy) ManagesFields() bool {
	if policy.Enabled && (policy.Priority != nil || policy.Websockets != nil) {
		return true
	}
	for _, rule := range policy.ConditionalRules {
		if rule.Enabled && (rule.Actions.Priority != nil || rule.Actions.Websockets != nil || rule.Actions.ModelPolicy != nil) {
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
	fields := make([]string, 0, 2)
	if policy.Priority != nil {
		fields = append(fields, policyFieldPriority)
	}
	if policy.Websockets != nil {
		fields = append(fields, policyFieldWebsockets)
	}
	return fields
}

func cloneDefaultPolicy(policy DefaultPolicy) DefaultPolicy {
	clone := policy
	clone.Priority = cloneIntPointer(policy.Priority)
	clone.Websockets = cloneBoolPointer(policy.Websockets)
	clone.ConditionalRules = cloneConditionalPolicyRules(policy.ConditionalRules)
	return clone
}

func defaultPolicyEqual(left, right DefaultPolicy) bool {
	left = normalizeDefaultPolicy(left)
	right = normalizeDefaultPolicy(right)
	return left.Enabled == right.Enabled && left.ApplyMode == right.ApplyMode &&
		left.NewAccountModelProbeEnabled == right.NewAccountModelProbeEnabled &&
		left.CodexQuotaMetadataProbeEnabled == right.CodexQuotaMetadataProbeEnabled &&
		left.ScanIntervalSeconds == right.ScanIntervalSeconds && managedPolicyEqual(left, right) &&
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
	if len(applied) == 0 {
		return append(json.RawMessage(nil), raw...), nil, false, nil
	}
	updated, errMarshal := json.Marshal(document)
	if errMarshal != nil {
		return nil, nil, false, fmt.Errorf("encode updated auth json: %w", errMarshal)
	}
	return updated, applied, true, nil
}

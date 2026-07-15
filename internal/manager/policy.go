package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
)

var ErrPolicyStorageUnavailable = errors.New("default policy storage is unavailable; configure data_dir to a writable directory")

type DefaultPolicy struct {
	Enabled             bool   `json:"enabled"`
	ApplyMode           string `json:"apply_mode"`
	ScanIntervalSeconds int    `json:"scan_interval_seconds"`
	Priority            *int   `json:"priority"`
	Websockets          *bool  `json:"websockets"`
}

type PolicyScanSummary struct {
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Scanned    int       `json:"scanned"`
	Eligible   int       `json:"eligible"`
	Changed    int       `json:"changed"`
	Skipped    int       `json:"skipped"`
	Failed     int       `json:"failed"`
	Error      string    `json:"error,omitempty"`
}

type PolicySnapshot struct {
	Policy        DefaultPolicy     `json:"policy"`
	Running       bool              `json:"running"`
	ScanStartedAt time.Time         `json:"scan_started_at,omitempty"`
	LastScan      PolicyScanSummary `json:"last_scan"`
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
}

type PolicyEngine struct {
	mu           sync.RWMutex
	operationMu  sync.Mutex
	wait         sync.WaitGroup
	host         AuthHost
	mutations    *MutationCoordinator
	config       Config
	store        string
	policy       DefaultPolicy
	lastScan     PolicyScanSummary
	running      bool
	scanStarted  time.Time
	fingerprints map[string]authFingerprint
	wake         chan struct{}
	cancel       context.CancelFunc
	started      bool
	closed       bool
	now          func() time.Time
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

func (e *PolicyEngine) Configure(config Config) {
	if e == nil {
		return
	}
	config = normalizeConfig(config)
	storePath := policyStorePath(config.DataDir)

	e.operationMu.Lock()
	e.mu.RLock()
	sameStore := e.started && e.store == storePath
	e.mu.RUnlock()

	if sameStore {
		e.mu.Lock()
		e.config = config
		e.mu.Unlock()
		e.operationMu.Unlock()
		e.requestScan()
		return
	}

	policy := normalizeDefaultPolicy(DefaultPolicy{})
	lastScan := PolicyScanSummary{}
	loadedPolicy, loadedScan, errLoad := loadPolicyState(storePath)
	if errLoad == nil {
		policy = loadedPolicy
		lastScan = loadedScan
	} else if !errors.Is(errLoad, os.ErrNotExist) {
		lastScan.Error = "stored default policy could not be loaded"
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
	if errSave := savePolicyState(storePath, normalized, lastScan); errSave != nil {
		e.operationMu.Unlock()
		return DefaultPolicy{}, ErrPolicyStorageUnavailable
	}
	e.mu.Lock()
	e.policy = normalized
	e.fingerprints = make(map[string]authFingerprint)
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
	e.operationMu.Lock()
	defer e.operationMu.Unlock()

	e.mu.RLock()
	policy := cloneDefaultPolicy(e.policy)
	e.mu.RUnlock()
	if !policy.Enabled || !policy.ManagesFields() {
		return false
	}
	if !e.mutations.TryAcquire(policyMutationOwner) {
		return true
	}
	defer e.mutations.Release(policyMutationOwner)

	startedAt := e.now().UTC()
	e.mu.Lock()
	e.running = true
	e.scanStarted = startedAt
	e.mu.Unlock()

	summary, fingerprints := e.scan(ctx, policy, startedAt)
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
	if ctx.Err() != nil {
		return false
	}
	if errSave := savePolicyState(storePath, currentPolicy, lastScan); errSave != nil {
		e.mu.Lock()
		e.lastScan.Error = "default policy status could not be persisted"
		e.mu.Unlock()
	}
	return false
}

func (e *PolicyEngine) scan(ctx context.Context, policy DefaultPolicy, startedAt time.Time) (PolicyScanSummary, map[string]authFingerprint) {
	summary := PolicyScanSummary{StartedAt: startedAt}
	nextFingerprints := make(map[string]authFingerprint)
	if e.host == nil {
		summary.Failed = 1
		summary.Error = "auth file scan failed"
		summary.FinishedAt = e.now().UTC()
		return summary, nextFingerprints
	}
	entries, errList := e.host.ListAuth(ctx)
	if errList != nil {
		summary.Failed = 1
		summary.Error = "auth file scan failed"
		summary.FinishedAt = e.now().UTC()
		return summary, nextFingerprints
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
		fingerprint := fingerprintForEntry(entry)
		if previous, exists := previousFingerprints[authIndex]; exists && previous == fingerprint {
			nextFingerprints[authIndex] = fingerprint
			summary.Skipped++
			continue
		}

		changed, errApply := e.reconcileEntry(ctx, entry, policy)
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
	return summary, nextFingerprints
}

func (e *PolicyEngine) reconcileEntry(ctx context.Context, entry cpaapi.HostAuthFileEntry, policy DefaultPolicy) (bool, error) {
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

	updated, _, changed, errApply := applyDefaultPolicy(detail.JSON, policy, applyMissing)
	if errApply != nil {
		return false, errApply
	}
	if !changed {
		return false, nil
	}
	if _, errSave := e.host.SaveAuth(ctx, name, updated); errSave != nil {
		return false, fmt.Errorf("save auth file: %w", errSave)
	}
	return true, nil
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
	policy = normalizeDefaultPolicy(policy)
	if policy.Enabled && !policy.ManagesFields() {
		return DefaultPolicy{}, fmt.Errorf("enabled policy requires priority or websockets")
	}
	return policy, nil
}

func (policy DefaultPolicy) ManagesFields() bool {
	return policy.Priority != nil || policy.Websockets != nil
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
	if policy.Priority != nil {
		value := *policy.Priority
		clone.Priority = &value
	}
	if policy.Websockets != nil {
		value := *policy.Websockets
		clone.Websockets = &value
	}
	return clone
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

func fingerprintForEntry(entry cpaapi.HostAuthFileEntry) authFingerprint {
	fingerprint := authFingerprint{
		Name: strings.TrimSpace(entry.Name),
		Path: normalizedPath(entry.Path),
		Size: entry.Size,
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

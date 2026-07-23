package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

const inspectionPersistDelay = 2 * time.Second

type InspectionEngine struct {
	mu                     sync.RWMutex
	scanMu                 sync.Mutex
	storeMu                sync.Mutex
	wait                   sync.WaitGroup
	accounts               *AccountService
	host                   AuthHost
	mutations              *MutationCoordinator
	modelTests             *ModelTestService
	deletions              *AccountDeleteService
	operations             *OperationJournal
	notificationDoer       HTTPDoer
	notificationRetryDelay func(int) time.Duration
	config                 Config
	store                  string
	policy                 InspectionPolicy
	records                map[string]inspectionRecord
	actions                []InspectionAction
	autoDisableGuards      []AutomaticDisableGuard
	runs                   []InspectionRunRecord
	activeRunID            string
	activeRunHealth        map[string]string
	activeRunHealthID      string
	lastRun                InspectionRunSummary
	probeCursor            int
	lastNativeRunAt        time.Time
	lastProbeRunAt         time.Time
	managementKey          string
	probeSweepRemaining    int
	probeSweepTotal        int
	probeSweepCompleted    int
	probeSweepSource       string
	probeSweepStatus       string
	probeSweepStartedAt    time.Time
	probeSweepTargets      []string
	anomalyTriggerPending  bool
	lastAnomalyTriggerAt   time.Time
	lastNotificationAt     time.Time
	anomalyEligible        int
	anomalyCount           int
	anomalyPercent         int
	running                bool
	pending                bool
	pendingProbe           bool
	pendingProbeSweep      bool
	runMode                string
	runHealth              []string
	runSelected            []string
	probePhase             string
	retryTotal             int
	retryCompleted         int
	stopRequested          bool
	activeCancel           context.CancelFunc
	scanStarted            time.Time
	storageErr             string
	dirty                  bool
	generation             uint64
	scanWake               chan struct{}
	persistWake            chan struct{}
	notificationWake       chan anomalyNotificationEvent
	cancel                 context.CancelFunc
	started                bool
	closed                 bool
	now                    func() time.Time
}

func NewInspectionEngine(accounts *AccountService, host AuthHost, mutations *MutationCoordinator) *InspectionEngine {
	if mutations == nil {
		mutations = NewMutationCoordinator()
	}
	config := normalizeConfig(Config{})
	return &InspectionEngine{
		accounts:         accounts,
		host:             host,
		mutations:        mutations,
		config:           config,
		store:            inspectionStorePath(config.DataDir),
		policy:           defaultInspectionPolicy(),
		records:          make(map[string]inspectionRecord),
		scanWake:         make(chan struct{}, 1),
		persistWake:      make(chan struct{}, 1),
		notificationWake: make(chan anomalyNotificationEvent, 4),
		now:              time.Now,
	}
}

func (e *InspectionEngine) SetModelTestService(service *ModelTestService) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.modelTests = service
	e.mu.Unlock()
}

func (e *InspectionEngine) SetDeleteService(service *AccountDeleteService) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.deletions = service
	e.mu.Unlock()
}

func (e *InspectionEngine) SetOperationJournal(journal *OperationJournal) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.operations = journal
	e.mu.Unlock()
}

func (e *InspectionEngine) Configure(config Config) {
	if e == nil {
		return
	}
	config = normalizeConfig(config)
	storePath := inspectionStorePath(config.DataDir)
	configuredPolicy, hasConfiguredPolicy, errConfiguredPolicy := inspectionPolicyFromConfig(config)

	e.scanMu.Lock()
	defer e.scanMu.Unlock()
	e.mu.RLock()
	sameStore := e.started && e.store == storePath
	e.mu.RUnlock()
	if sameStore {
		e.mu.Lock()
		e.config = config
		currentPolicy := e.policy
		if hasConfiguredPolicy && errConfiguredPolicy != nil {
			e.storageErr = "inspection state could not be loaded"
		} else if hasConfiguredPolicy && e.storageErr == "inspection state could not be loaded" {
			e.storageErr = ""
		}
		e.mu.Unlock()
		if hasConfiguredPolicy && errConfiguredPolicy == nil && currentPolicy != configuredPolicy {
			if _, errSave := e.SetPolicy(configuredPolicy); errSave != nil {
				e.mu.Lock()
				e.storageErr = "inspection state could not be persisted"
				e.mu.Unlock()
			}
		}
		return
	}

	state := persistedInspectionState{
		Version: inspectionStoreVersion,
		Policy:  defaultInspectionPolicy(),
		Records: make(map[string]inspectionRecord),
	}
	storageErr := ""
	loaded, errLoad := loadInspectionState(storePath)
	if errLoad == nil {
		state = loaded
	} else if !errors.Is(errLoad, os.ErrNotExist) {
		storageErr = "inspection state could not be loaded"
	}
	if hasConfiguredPolicy {
		if errConfiguredPolicy != nil {
			storageErr = "inspection state could not be loaded"
		} else {
			state.Policy = configuredPolicy
			if configuredPolicy.AnomalyNotificationOnly {
				stopPendingAnomalySweep(&state, true)
			}
			e.storeMu.Lock()
			if errSave := saveInspectionState(storePath, state); errSave != nil {
				storageErr = "inspection state could not be persisted"
			}
			e.storeMu.Unlock()
		}
	}

	e.mu.Lock()
	e.config = config
	e.store = storePath
	e.policy = state.Policy
	e.records = state.Records
	e.actions = state.Actions
	e.runs = state.Runs
	e.activeRunID = state.ActiveRunID
	e.activeRunHealthID = e.activeRunID
	e.activeRunHealth = make(map[string]string)
	for id, record := range e.records {
		if record.Result.RunID == e.activeRunID && record.Result.RunObservedAt != nil {
			e.activeRunHealth[id] = normalizeInspectionHealth(record.Result.Health)
		}
	}
	e.lastRun = state.LastRun
	e.probeCursor = state.ProbeCursor
	e.lastNativeRunAt = state.LastNativeRunAt
	e.lastProbeRunAt = state.LastProbeRunAt
	e.probeSweepRemaining = state.ProbeSweepRemaining
	e.probeSweepTotal = state.ProbeSweepTotal
	e.probeSweepCompleted = state.ProbeSweepCompleted
	e.probeSweepSource = state.ProbeSweepSource
	e.probeSweepStatus = state.ProbeSweepStatus
	e.probeSweepStartedAt = state.ProbeSweepStartedAt
	e.probeSweepTargets = append([]string(nil), state.ProbeSweepTargets...)
	if e.probeSweepRemaining > 0 && e.probeSweepStatus != InspectionSweepStatusStopped {
		e.probeSweepStatus = InspectionSweepStatusWaitingForAuth
	}
	e.anomalyTriggerPending = state.AnomalyTriggerPending
	e.lastAnomalyTriggerAt = state.LastAnomalyTriggerAt
	e.lastNotificationAt = state.LastNotificationAt
	e.runMode = state.RunMode
	e.runHealth = append([]string(nil), state.RunHealth...)
	e.runSelected = append([]string(nil), state.RunSelected...)
	e.probePhase = state.ProbePhase
	e.retryTotal = state.RetryTotal
	e.retryCompleted = state.RetryCompleted
	e.stopRequested = state.StopRequested
	e.managementKey = ""
	e.storageErr = storageErr
	e.dirty = false
	e.generation++
	start := !e.started && !e.closed
	if start {
		ctx, cancel := context.WithCancel(context.Background())
		e.cancel = cancel
		e.started = true
		e.wait.Add(3)
		go e.scanLoop(ctx)
		go e.persistLoop(ctx)
		go e.notificationLoop(ctx)
	}
	e.mu.Unlock()
}

func inspectionPolicyFromConfig(config Config) (InspectionPolicy, bool, error) {
	if config.InspectionPolicy == nil {
		return InspectionPolicy{}, false, nil
	}
	policy, errValidate := validateInspectionPolicy(*config.InspectionPolicy)
	if errValidate != nil {
		return InspectionPolicy{}, true, errValidate
	}
	return policy, true, nil
}

func (e *InspectionEngine) Snapshot() InspectionSnapshot {
	if e == nil {
		return InspectionSnapshot{Policy: defaultInspectionPolicy()}
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	activeRun := activeInspectionRun(e.runs, e.activeRunID)
	liveResults := liveInspectionResults(e.records, e.activeRunID, maxInspectionLiveResults)
	return InspectionSnapshot{
		Policy:                e.policy,
		Running:               e.running,
		Pending:               e.pending,
		ScanStartedAt:         e.scanStarted,
		LastRun:               e.lastRun,
		Total:                 len(e.records),
		ActionCount:           len(e.actions),
		ActiveProbeArmed:      strings.TrimSpace(e.managementKey) != "" && e.modelTests != nil,
		LastNativeRunAt:       e.lastNativeRunAt,
		LastProbeRunAt:        e.lastProbeRunAt,
		ProbeSweepRemaining:   e.probeSweepRemaining,
		ProbeSweepTotal:       e.probeSweepTotal,
		ProbeSweepCompleted:   e.probeSweepCompleted,
		ProbeSweepSource:      e.probeSweepSource,
		ProbeSweepStatus:      e.probeSweepStatus,
		ProbeSweepStartedAt:   e.probeSweepStartedAt,
		AnomalyEligible:       e.anomalyEligible,
		AnomalyCount:          e.anomalyCount,
		AnomalyPercent:        e.anomalyPercent,
		AnomalyTriggerPending: e.anomalyTriggerPending,
		LastAnomalyTriggerAt:  timePointer(e.lastAnomalyTriggerAt),
		LastNotificationAt:    timePointer(e.lastNotificationAt),
		StorageError:          e.storageErr,
		RunMode:               e.runMode,
		ProbePhase:            e.probePhase,
		RetryTotal:            e.retryTotal,
		RetryCompleted:        e.retryCompleted,
		StopRequested:         e.stopRequested,
		RecentRuns:            recentInspectionRuns(e.runs, 10),
		Revision:              e.generation,
		ActiveRun:             activeRun,
		LiveResults:           liveResults,
	}
}

func (e *InspectionEngine) RequestRun(request InspectionRunRequest, managementKey string) (InspectionSnapshot, error) {
	if e == nil {
		return InspectionSnapshot{}, fmt.Errorf("inspection engine is unavailable")
	}
	mode := strings.ToLower(strings.TrimSpace(request.Mode))
	if mode == "" {
		mode = InspectionRunModeFull
	}
	if mode != InspectionRunModeFull && mode != InspectionRunModeIncremental && mode != InspectionRunModeScoped && mode != InspectionRunModeRetry {
		return InspectionSnapshot{}, fmt.Errorf("unsupported inspection run mode")
	}
	health := normalizeInspectionRunHealth(request.Health)
	selected := sanitizeInspectionSweepTargets(request.Selected)
	if mode == InspectionRunModeScoped && len(health) == 0 && len(selected) == 0 {
		return InspectionSnapshot{}, fmt.Errorf("scoped inspection requires health or selected account ids")
	}
	e.mu.Lock()
	if e.closed || !e.started {
		e.mu.Unlock()
		return InspectionSnapshot{}, fmt.Errorf("inspection engine is unavailable")
	}
	if e.running || e.pending || e.pendingProbeSweep {
		e.mu.Unlock()
		return InspectionSnapshot{}, fmt.Errorf("inspection is already running")
	}
	if (mode == InspectionRunModeRetry || mode == InspectionRunModeScoped && len(selected) == 0) && len(e.records) == 0 {
		e.mu.Unlock()
		return InspectionSnapshot{}, fmt.Errorf("inspection mode requires existing results")
	}
	armed := strings.TrimSpace(managementKey) != "" && e.modelTests != nil
	if !armed {
		e.mu.Unlock()
		return InspectionSnapshot{}, fmt.Errorf("management key is required for active inspection")
	}
	targets := e.runTargetsLocked(mode, health, selected)
	if mode == InspectionRunModeFull || mode == InspectionRunModeIncremental {
		targets = nil
	}
	if len(targets) == 0 && mode != InspectionRunModeFull && mode != InspectionRunModeIncremental {
		e.mu.Unlock()
		return InspectionSnapshot{}, fmt.Errorf("inspection scope matched no accounts")
	}
	e.managementKey = strings.TrimSpace(managementKey)
	e.pending = true
	e.pendingProbe = true
	e.pendingProbeSweep = true
	e.runMode = mode
	e.runHealth = append([]string(nil), health...)
	e.runSelected = append([]string(nil), selected...)
	e.probePhase = InspectionProbePhaseListing
	e.retryTotal = 0
	e.retryCompleted = 0
	e.stopRequested = false
	e.probeSweepTotal = len(targets)
	e.probeSweepCompleted = 0
	e.probeSweepRemaining = len(targets)
	e.probeSweepSource = InspectionSweepSourceManual
	e.probeSweepStatus = InspectionSweepStatusRunning
	e.probeSweepStartedAt = e.currentTime()
	e.probeSweepTargets = append([]string(nil), targets...)
	e.startRunHistoryLocked(mode, InspectionSweepSourceManual, e.probeSweepStartedAt)
	e.dirty = true
	e.generation++
	e.mu.Unlock()
	managementKey = ""
	e.requestPersist()
	select {
	case e.scanWake <- struct{}{}:
	default:
	}
	return e.Snapshot(), nil
}

func (e *InspectionEngine) StopRun() InspectionSnapshot {
	if e == nil {
		return InspectionSnapshot{Policy: defaultInspectionPolicy()}
	}
	e.mu.Lock()
	cancel := e.activeCancel
	if e.running || e.pendingProbeSweep || e.probeSweepRemaining > 0 {
		e.stopRequested = true
		e.pending = false
		e.pendingProbe = false
		e.pendingProbeSweep = false
		e.probeSweepStatus = InspectionSweepStatusStopped
		e.probePhase = InspectionProbePhaseStopped
		e.managementKey = ""
		e.updateRunHistoryLocked(InspectionSweepStatusStopped, InspectionProbePhaseStopped, e.currentTime())
		e.dirty = true
		e.generation++
	}
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	e.requestPersist()
	return e.Snapshot()
}

func normalizeInspectionRunHealth(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		health := strings.ToLower(strings.TrimSpace(value))
		switch health {
		case InspectionHealthHealthy, InspectionHealthQuotaLimited, InspectionHealthInvalidCredentials,
			InspectionHealthDeactivated, InspectionHealthReview, InspectionHealthUnavailable,
			InspectionHealthDisabled, InspectionHealthUnknown:
		default:
			continue
		}
		if _, exists := seen[health]; exists {
			continue
		}
		seen[health] = struct{}{}
		out = append(out, health)
	}
	sort.Strings(out)
	return out
}

func normalizeInspectionRunMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case InspectionRunModeNative, InspectionRunModeFull, InspectionRunModeIncremental, InspectionRunModeScoped, InspectionRunModeRetry:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeInspectionProbePhase(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case InspectionProbePhaseListing, InspectionProbePhasePrimary, InspectionProbePhaseRetry, InspectionProbePhaseStopped, InspectionProbePhaseDone:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func (e *InspectionEngine) runTargetsLocked(mode string, health, selected []string) []string {
	if len(selected) > 0 {
		return append([]string(nil), selected...)
	}
	ids := make([]string, 0, len(e.records))
	healthSet := make(map[string]struct{}, len(health))
	for _, value := range health {
		healthSet[value] = struct{}{}
	}
	for id, record := range e.records {
		include := false
		switch mode {
		case InspectionRunModeRetry:
			include = record.Result.Health == InspectionHealthReview || record.Result.Health == InspectionHealthUnavailable || record.Result.Health == InspectionHealthUnknown ||
				record.Probe.Status == "review" || record.Probe.Status == "unavailable"
		case InspectionRunModeScoped:
			_, include = healthSet[record.Result.Health]
		case InspectionRunModeIncremental:
			include = false
		default:
			include = true
		}
		if include {
			ids = append(ids, id)
		}
	}
	sortInspectionIDs(ids)
	return sanitizeInspectionSweepTargets(ids)
}

func (e *InspectionEngine) SetPolicy(policy InspectionPolicy) (InspectionSnapshot, error) {
	if e == nil {
		return InspectionSnapshot{}, fmt.Errorf("inspection engine is unavailable")
	}
	normalized, errValidate := validateInspectionPolicy(policy)
	if errValidate != nil {
		return InspectionSnapshot{}, errValidate
	}

	e.mu.RLock()
	storePath := e.store
	state := e.persistedStateLocked()
	closed := e.closed
	e.mu.RUnlock()
	if closed || strings.TrimSpace(storePath) == "" {
		return InspectionSnapshot{}, fmt.Errorf("inspection storage is unavailable")
	}
	state.Policy = normalized
	if !normalized.AnomalyTriggerEnabled {
		state.AnomalyTriggerPending = false
	}
	if normalized.AnomalyNotificationOnly {
		stopPendingAnomalySweep(&state, false)
	}
	if !normalized.ModelProbeFullSweep && !normalized.AnomalyTriggerEnabled {
		state.ProbeSweepRemaining = 0
	}
	e.storeMu.Lock()
	errSave := saveInspectionState(storePath, state)
	e.storeMu.Unlock()
	if errSave != nil {
		return InspectionSnapshot{}, fmt.Errorf("save inspection policy: %w", errSave)
	}

	e.mu.Lock()
	e.policy = normalized
	if !normalized.AnomalyTriggerEnabled {
		e.anomalyTriggerPending = false
	}
	if !normalized.ModelProbeFullSweep && !normalized.AnomalyTriggerEnabled {
		e.probeSweepRemaining = 0
		e.pendingProbeSweep = false
	}
	if normalized.AnomalyNotificationOnly {
		e.anomalyTriggerPending = false
		if normalizeInspectionSweepSource(e.probeSweepSource) == InspectionSweepSourceAnomaly &&
			normalizeInspectionSweepStatus(e.probeSweepStatus) != InspectionSweepStatusRunning {
			e.probeSweepTotal = 0
			e.probeSweepCompleted = 0
			e.probeSweepRemaining = 0
			e.probeSweepStatus = InspectionSweepStatusStopped
			e.probeSweepTargets = nil
			e.pendingProbeSweep = false
		}
	}
	e.storageErr = ""
	e.generation++
	e.mu.Unlock()
	e.RequestScan()
	return e.Snapshot(), nil
}

func (e *InspectionEngine) RequestScan() InspectionSnapshot {
	if e == nil {
		return InspectionSnapshot{Policy: defaultInspectionPolicy()}
	}
	e.mu.Lock()
	started := e.started && !e.closed
	if started {
		e.pending = true
		e.stopRequested = false
		e.startRunHistoryLocked(InspectionRunModeNative, InspectionSweepSourceManual, e.currentTime())
	}
	e.mu.Unlock()
	if started {
		select {
		case e.scanWake <- struct{}{}:
		default:
		}
	}
	return e.Snapshot()
}

func (e *InspectionEngine) RequestScanWithModelProbes(managementKey string) InspectionSnapshot {
	if e == nil {
		return InspectionSnapshot{Policy: defaultInspectionPolicy()}
	}
	requestedAt := e.currentTime()
	e.mu.Lock()
	started := e.started && !e.closed
	if started {
		e.pending = true
		e.stopRequested = false
		armed := strings.TrimSpace(managementKey) != "" && e.modelTests != nil
		e.pendingProbe = e.pendingProbe || armed
		if e.pendingProbe {
			e.managementKey = strings.TrimSpace(managementKey)
		}
		if armed && e.probeSweepRemaining > 0 {
			e.pendingProbeSweep = true
			e.probeSweepStatus = InspectionSweepStatusRunning
			if e.runMode == "" {
				e.runMode = InspectionRunModeFull
			}
			e.startRunHistoryLocked(e.runMode, InspectionSweepSourceManual, requestedAt)
		} else if armed && e.probeSweepRemaining == 0 && !e.pendingProbeSweep && e.probeSweepStatus != InspectionSweepStatusRunning {
			e.probeSweepTotal = 0
			e.probeSweepCompleted = 0
			e.probeSweepRemaining = 0
			e.probeSweepSource = InspectionSweepSourceManual
			e.probeSweepStatus = InspectionSweepStatusRunning
			e.probeSweepStartedAt = requestedAt
			e.probeSweepTargets = nil
			e.pendingProbeSweep = true
			e.runMode = InspectionRunModeFull
			e.startRunHistoryLocked(InspectionRunModeFull, InspectionSweepSourceManual, requestedAt)
		} else if !armed {
			e.probeSweepSource = InspectionSweepSourceManual
			e.probeSweepStatus = InspectionSweepStatusWaitingForAuth
			e.probeSweepStartedAt = requestedAt
		}
	}
	e.mu.Unlock()
	managementKey = ""
	if started {
		select {
		case e.scanWake <- struct{}{}:
		default:
		}
	}
	return e.Snapshot()
}

func (e *InspectionEngine) ArmModelProbes(managementKey string) InspectionSnapshot {
	if e == nil || strings.TrimSpace(managementKey) == "" {
		return e.Snapshot()
	}
	e.mu.Lock()
	wake := false
	if !e.closed && e.modelTests != nil {
		e.managementKey = strings.TrimSpace(managementKey)
		if e.anomalyTriggerPending || (e.probeSweepRemaining > 0 && e.probeSweepStatus != InspectionSweepStatusStopped) {
			e.anomalyTriggerPending = false
			e.pending = true
			e.pendingProbe = true
			e.pendingProbeSweep = true
			e.probeSweepStatus = InspectionSweepStatusRunning
			e.dirty = true
			e.generation++
			wake = e.started
		}
	}
	e.mu.Unlock()
	managementKey = ""
	if wake {
		select {
		case e.scanWake <- struct{}{}:
		default:
		}
	}
	return e.Snapshot()
}

func (e *InspectionEngine) Observe(record cpaapi.UsageRecord) {
	if e == nil {
		return
	}
	authIndex := strings.TrimSpace(record.AuthIndex)
	if authIndex == "" {
		return
	}
	now := e.currentTime()
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	if e.records == nil {
		e.records = make(map[string]inspectionRecord)
	}
	if _, exists := e.records[authIndex]; !exists {
		e.ensureInspectionRecordCapacityLocked(authIndex)
	}
	inspection := e.records[authIndex]
	inspection.Result.ID = authIndex
	applyUsageRecordToInspection(&inspection, record, e.policy, now)
	e.records[authIndex] = inspection
	wake := e.started && (passiveCircuitThresholdReached(e.policy, inspection) ||
		usageObservationRequiresImmediateScan(e.policy, record, inspection, now) && e.usageAutoDisableAllowedLocked(record, now))
	if wake {
		e.pending = true
	}
	e.dirty = true
	e.generation++
	e.mu.Unlock()
	e.requestPersist()
	if wake {
		select {
		case e.scanWake <- struct{}{}:
		default:
		}
	}
}

func usageObservationRequiresImmediateScan(policy InspectionPolicy, usage cpaapi.UsageRecord, inspection inspectionRecord, now time.Time) bool {
	policy = normalizeInspectionPolicy(policy)
	if !policy.Enabled || !policy.AutoDisable {
		return false
	}
	if codex := parseCodexUsageHeaders(usage.ResponseHeaders, now); codex != nil && codex.SevenDay != nil &&
		codex.SevenDay.UsedPercent >= 100 && (codex.SevenDay.ResetAt == nil || codex.SevenDay.ResetAt.After(now)) {
		return true
	}
	return usage.Failed && inspection.Signal.AutoDisableEligible &&
		inspection.Signal.ConsecutiveFailures >= policy.FailureThreshold
}

func (e *InspectionEngine) ListResults(query InspectionResultQuery) InspectionResultList {
	query = normalizeInspectionResultQuery(query)
	e.mu.RLock()
	results := make([]InspectionResult, 0, len(e.records))
	for _, record := range e.records {
		result := cloneInspectionResult(record.Result)
		result.ManualDeleteEligible = inspectionManualDeleteAllowed(result)
		if query.Health != "" && result.Health != query.Health {
			continue
		}
		if query.Search != "" {
			haystack := strings.ToLower(strings.Join([]string{result.ID, result.Name, result.Provider, result.Type, result.PlanType, result.ReasonCode}, "\n"))
			if !strings.Contains(haystack, query.Search) {
				continue
			}
		}
		results = append(results, result)
	}
	e.mu.RUnlock()
	sort.Slice(results, func(i, j int) bool {
		leftRank := inspectionHealthRank(results[i].Health)
		rightRank := inspectionHealthRank(results[j].Health)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		left := strings.ToLower(firstNonEmpty(results[i].Name, results[i].ID))
		right := strings.ToLower(firstNonEmpty(results[j].Name, results[j].ID))
		if left == right {
			return results[i].ID < results[j].ID
		}
		return left < right
	})

	total := len(results)
	remediation := summarizeInspectionRemediation(results)
	start := (query.Page - 1) * query.PageSize
	if start > total {
		start = total
	}
	end := start + query.PageSize
	if end > total {
		end = total
	}
	pages := 0
	if total > 0 {
		pages = (total + query.PageSize - 1) / query.PageSize
	}
	return InspectionResultList{
		Results:  append([]InspectionResult{}, results[start:end]...),
		Summary:  remediation,
		Total:    total,
		Page:     query.Page,
		PageSize: query.PageSize,
		Pages:    pages,
	}
}

func (e *InspectionEngine) ReconcileAccountStates(ctx context.Context) error {
	if e == nil || e.accounts == nil {
		return fmt.Errorf("account inspection is unavailable")
	}
	accounts, errAccounts := e.accounts.baseAccounts(ctx)
	if errAccounts != nil {
		return fmt.Errorf("list CPA accounts: %w", errAccounts)
	}
	current := make(map[string]Account, len(accounts))
	for _, account := range accounts {
		if id := strings.TrimSpace(account.ID); id != "" {
			current[id] = account
		}
	}
	e.mu.Lock()
	changed := false
	for id, record := range e.records {
		account, exists := current[id]
		if !exists {
			delete(e.records, id)
			changed = true
			continue
		}
		if record.Result.Disabled != account.Disabled {
			record.Result.Disabled = account.Disabled
			changed = true
		}
		if record.Result.OwnedDisable && !account.Disabled {
			clearInspectionDisableOwnership(&record)
			record.Result.AutoAction = ""
			record.Result.AutoActionStatus = ""
			changed = true
		}
		e.records[id] = record
	}
	if changed {
		e.dirty = true
		e.generation++
	}
	e.mu.Unlock()
	if changed {
		e.requestPersist()
	}
	return nil
}

func summarizeInspectionRemediation(results []InspectionResult) InspectionRemediationSummary {
	summary := InspectionRemediationSummary{}
	for _, result := range results {
		if result.Editable {
			if result.Disabled {
				summary.EditableDisabled++
			} else {
				summary.EditableEnabled++
			}
		}
		switch normalizeInspectionRecommendation(result.Recommendation) {
		case InspectionRecommendationDelete:
			if inspectionManualDeleteAllowed(result) {
				summary.SuggestedDelete++
			} else {
				summary.Review++
			}
		case InspectionRecommendationDisable:
			if result.Editable && !result.Disabled {
				summary.SuggestedDisable++
			} else if result.Disabled {
				if inspectionRecommendationHandled(result, InspectionActionDisable) {
					summary.Handled++
				} else {
					summary.Keep++
				}
			} else {
				summary.Review++
			}
		case InspectionRecommendationEnable:
			if result.Editable && result.Disabled {
				summary.SuggestedEnable++
			} else if !result.Disabled {
				if inspectionRecommendationHandled(result, InspectionActionEnable) {
					summary.Handled++
				} else {
					summary.Keep++
				}
			} else {
				summary.Review++
			}
		case InspectionRecommendationReauth:
			summary.Reauth++
			if inspectionManualDeleteAllowed(result) {
				summary.DeletableReauth++
			}
		case InspectionRecommendationReview:
			summary.Review++
		case InspectionRecommendationKeep:
			summary.Keep++
		default:
			summary.Review++
		}
	}
	summary.Actionable = summary.SuggestedDelete + summary.SuggestedDisable + summary.SuggestedEnable + summary.Reauth
	return summary
}

func inspectionRecommendationHandled(result InspectionResult, action string) bool {
	return normalizeInspectionAction(result.AutoAction) == action &&
		normalizeInspectionActionStatus(result.AutoActionStatus) == InspectionActionSucceeded
}

func (e *InspectionEngine) AccountAutomationSummaries(accounts []Account) map[string]AccountAutomationSummary {
	summaries := make(map[string]AccountAutomationSummary)
	if e == nil || len(accounts) == 0 {
		return summaries
	}

	e.mu.RLock()
	policy := e.policy
	records := make(map[string]inspectionRecord, len(accounts))
	for _, account := range accounts {
		if record, exists := e.records[account.ID]; exists {
			records[account.ID] = record
		}
	}
	e.mu.RUnlock()

	for _, account := range accounts {
		record, exists := records[account.ID]
		if !exists || record.Result.LastCheckedAt.IsZero() {
			continue
		}
		record = refreshUnsupportedAgentIdentityNativeResult(account, record, e.currentTime())
		ownedDisable := record.Result.OwnedDisable && account.Disabled
		summary := AccountAutomationSummary{
			Health:                  normalizeInspectionHealth(record.Result.Health),
			ReasonCode:              safeInspectionReason(record.Result.ReasonCode),
			Recommendation:          normalizeInspectionRecommendation(record.Result.Recommendation),
			LastCheckedAt:           record.Result.LastCheckedAt.UTC(),
			OwnedDisable:            ownedDisable,
			AutoAction:              normalizeInspectionAction(record.Result.AutoAction),
			AutoActionStatus:        normalizeInspectionActionStatus(record.Result.AutoActionStatus),
			AutoDisableEligible:     record.Result.AutoDisableEligible,
			InspectionEnabled:       policy.Enabled,
			AutoDisableEnabled:      policy.AutoDisable,
			AutoEnableEnabled:       policy.AutoEnable,
			AutoDeleteEnabled:       policy.AutoDelete,
			FailureThreshold:        policy.FailureThreshold,
			FailureStreak:           boundedCounter(record.Result.FailureStreak),
			RecoveryThreshold:       policy.RecoveryThreshold,
			HealthyStreak:           boundedCounter(record.Result.HealthyStreak),
			PassiveCircuitEnabled:   policy.PassiveCircuitEnabled,
			PassiveFailureThreshold: policy.PassiveFailureThreshold,
			PassiveFailureStreak:    max(record.Signal.ConsecutiveFailures, record.Probe.ConsecutiveFailures),
			CircuitOpen:             ownedDisable && record.DisableReason == "passive_circuit_open",
			CircuitReasonCode:       safeOptionalInspectionReason(record.Result.CircuitReasonCode),
		}
		if ownedDisable {
			if strings.TrimSpace(record.DisableReason) != "" {
				summary.DisableReason = safeInspectionReason(record.DisableReason)
			}
			summary.DisabledAt = timePointer(record.DisabledAt)
			summary.RecoverAfter = timePointer(record.DisabledRecoverAfter)
			if policy.AutoDelete {
				summary.DeleteEligibleAt = cloneTimePointer(record.Result.DeleteEligibleAt)
				summary.DeleteRetryAfter = timePointer(record.DeleteRetryAfter)
			}
		}
		summaries[account.ID] = summary
	}
	return summaries
}

func refreshUnsupportedAgentIdentityNativeResult(account Account, record inspectionRecord, now time.Time) inspectionRecord {
	if account.Disabled || !isAgentIdentityProvider(account.Provider) ||
		record.Result.ReasonCode != "native_unavailable" ||
		(record.Result.SignalSource != "" && record.Result.SignalSource != InspectionSignalNative) {
		return record
	}
	lastCheckedAt := record.Result.LastCheckedAt
	decision := decideInspection(account, record, now)
	if decision.ReasonCode == "native_unavailable" {
		return record
	}
	updateInspectionRecord(&record, account, decision, lastCheckedAt)
	record.Result.LastCheckedAt = lastCheckedAt
	if record.Result.AutoAction == InspectionActionDisable {
		record.Result.AutoAction = ""
		record.Result.AutoActionStatus = ""
	}
	return record
}

func (e *InspectionEngine) Actions(limit int) []InspectionAction {
	if e == nil {
		return []InspectionAction{}
	}
	if limit <= 0 || limit > maxInspectionActions {
		limit = 50
	}
	e.mu.RLock()
	start := len(e.actions) - limit
	if start < 0 {
		start = 0
	}
	actions := append([]InspectionAction{}, e.actions[start:]...)
	e.mu.RUnlock()
	for left, right := 0, len(actions)-1; left < right; left, right = left+1, right-1 {
		actions[left], actions[right] = actions[right], actions[left]
	}
	return actions
}

func (e *InspectionEngine) UpdateReview(request InspectionReviewRequest) (InspectionResult, error) {
	if e == nil {
		return InspectionResult{}, fmt.Errorf("inspection engine is unavailable")
	}
	accountID := strings.TrimSpace(request.AccountID)
	if accountID == "" || len(accountID) > 256 {
		return InspectionResult{}, fmt.Errorf("account_id is required and must be at most 256 characters")
	}
	action := strings.ToLower(strings.TrimSpace(request.Action))
	status := ""
	inspectionAction := ""
	switch action {
	case "resolve":
		status = InspectionReviewResolved
		inspectionAction = InspectionActionReviewResolve
	case "ignore":
		status = InspectionReviewIgnored
		inspectionAction = InspectionActionReviewIgnore
	case "reopen":
		status = InspectionReviewPending
		inspectionAction = InspectionActionReviewReopen
	default:
		return InspectionResult{}, fmt.Errorf("unsupported inspection review action")
	}
	now := e.currentTime()
	e.mu.Lock()
	record, exists := e.records[accountID]
	if !exists || record.Result.LastCheckedAt.IsZero() {
		e.mu.Unlock()
		return InspectionResult{}, fmt.Errorf("inspection result was not found")
	}
	if record.Result.Health != InspectionHealthReview {
		e.mu.Unlock()
		return InspectionResult{}, fmt.Errorf("inspection result does not require review")
	}
	current := normalizeInspectionReviewStatus(record.Result.ReviewStatus, record.Result.Health)
	if current == status {
		result := record.Result
		e.mu.Unlock()
		return result, nil
	}
	record.Result.ReviewStatus = status
	record.Result.ReviewedAt = timePointer(now)
	if status == InspectionReviewPending {
		record.Result.ReviewedAt = nil
	}
	e.records[accountID] = record
	actionRecord := newInspectionAction(record.Result, inspectionAction, record.Result.ReasonCode, now)
	actionRecord.Source = OperationSourceManual
	actionRecord.Status = InspectionActionSucceeded
	e.appendActionLocked(actionRecord)
	e.dirty = true
	e.generation++
	result := record.Result
	e.mu.Unlock()
	e.persist()
	return result, nil
}

func (e *InspectionEngine) RecordManualModelTest(ctx context.Context, result ModelTestResult) error {
	if e == nil || e.accounts == nil {
		return fmt.Errorf("inspection engine is unavailable")
	}
	accountID := strings.TrimSpace(result.AccountID)
	if accountID == "" {
		return fmt.Errorf("account_id is required")
	}
	resolved, errResolve := e.accounts.ResolveTargets(ctx, TargetScope{Mode: "selected", IDs: []string{accountID}})
	if errResolve != nil || len(resolved.Accounts) != 1 {
		return fmt.Errorf("inspection account was not found")
	}
	account := resolved.Accounts[0]
	e.scanMu.Lock()
	defer e.scanMu.Unlock()
	e.mu.Lock()
	record := e.records[accountID]
	policy := e.policy
	previousHealth := record.Result.Health
	previousReason := record.Result.ReasonCode
	applyModelProbeToInspection(&record, result, policy)
	decision := decideInspection(account, record, e.currentTime())
	updateInspectionRecord(&record, account, decision, e.currentTime())
	if record.Result.Health == InspectionHealthReview || record.Result.Health != previousHealth || record.Result.ReasonCode != previousReason {
		record.Result.ReviewStatus = normalizeInspectionReviewStatus("", record.Result.Health)
		record.Result.ReviewedAt = nil
	}
	e.records[accountID] = record
	requestDisable := policy.AutoDisable && decision.AutoDisableEligible && account.Editable && !account.Disabled
	e.dirty = true
	e.generation++
	e.mu.Unlock()
	e.persist()
	if requestDisable {
		e.RequestScan()
	}
	return nil
}

func (e *InspectionEngine) Shutdown() {
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

func (e *InspectionEngine) scanLoop(ctx context.Context) {
	defer e.wait.Done()
	timer := time.NewTimer(e.scanInterval())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.scanWake:
			e.mu.Lock()
			probe := e.pendingProbe
			sweep := e.pendingProbeSweep
			e.pendingProbe = false
			e.pendingProbeSweep = false
			e.mu.Unlock()
			e.scanWithMode(ctx, false, probe, sweep)
		case <-timer.C:
			if e.scheduledEnabled() {
				e.scanWithMode(ctx, true, false, false)
			}
		}
		resetInspectionTimer(timer, e.scanInterval())
	}
}

func (e *InspectionEngine) persistLoop(ctx context.Context) {
	defer e.wait.Done()
	for {
		select {
		case <-ctx.Done():
			e.persist()
			return
		case <-e.persistWake:
			timer := time.NewTimer(inspectionPersistDelay)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				e.persist()
				return
			case <-timer.C:
				e.persist()
			}
		}
	}
}

func (e *InspectionEngine) scan(ctx context.Context) {
	e.scanWithMode(ctx, false, false, false)
}

func (e *InspectionEngine) scanWithMode(ctx context.Context, scheduled, manualProbe, requestedSweep bool) {
	e.scanMu.Lock()
	defer e.scanMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	startedAt := e.currentTime()
	batchCtx, batchCancel := context.WithCancel(ctx)
	e.mu.Lock()
	e.running = true
	e.pending = false
	e.scanStarted = startedAt
	previous := cloneInspectionRecords(e.records)
	policy := e.policy
	managementKey := e.managementKey
	lastNativeRunAt := e.lastNativeRunAt
	lastProbeRunAt := e.lastProbeRunAt
	probeCursor := e.probeCursor
	modelTests := e.modelTests
	deletions := e.deletions
	probeSweepRemaining := e.probeSweepRemaining
	probeSweepTotal := e.probeSweepTotal
	probeSweepCompleted := e.probeSweepCompleted
	probeSweepSource := e.probeSweepSource
	probeSweepStatus := e.probeSweepStatus
	probeSweepStartedAt := e.probeSweepStartedAt
	probeSweepTargets := append([]string(nil), e.probeSweepTargets...)
	runMode := e.runMode
	activeRunID := e.activeRunID
	retryCompletedBase := e.retryCompleted
	stopRequested := e.stopRequested
	config := e.config
	e.activeCancel = batchCancel
	e.mu.Unlock()
	defer func() {
		batchCancel()
		e.mu.Lock()
		e.activeCancel = nil
		e.mu.Unlock()
	}()
	ctx = batchCtx
	defer e.clearScanRunning()
	manualSweepStart := !scheduled && manualProbe && requestedSweep && probeSweepSource == InspectionSweepSourceManual && probeSweepCompleted == 0
	runNative := (!scheduled && !requestedSweep) || manualSweepStart || ((policy.Enabled || policy.PassiveCircuitEnabled) && inspectionRunDue(startedAt, lastNativeRunAt, nativeInspectionInterval(policy)))
	runProbe := manualProbe || (scheduled && policy.ModelProbeEnabled && inspectionRunDue(startedAt, lastProbeRunAt, policy.ModelProbeIntervalMinutes))
	runProbe = runProbe && strings.TrimSpace(managementKey) != "" && modelTests != nil
	probeSweep := requestedSweep || (scheduled && runProbe && policy.ModelProbeFullSweep)
	if scheduled && runProbe && policy.ModelProbeFullSweep && (probeSweepStatus != InspectionSweepStatusRunning || probeSweepRemaining == 0) {
		probeSweepTotal = 0
		probeSweepCompleted = 0
		probeSweepRemaining = 0
		probeSweepSource = InspectionSweepSourceScheduled
		probeSweepStartedAt = startedAt
		probeSweepTargets = nil
	}
	if probeSweep && probeSweepSource == "" {
		probeSweepSource = InspectionSweepSourceScheduled
		probeSweepStartedAt = startedAt
	}
	if !runNative && !runProbe {
		return
	}
	if stopRequested {
		return
	}

	summary := InspectionRunSummary{StartedAt: startedAt}
	accounts, errAccounts := e.accounts.baseAccounts(ctx)
	if errAccounts != nil {
		if ctx.Err() != nil {
			return
		}
		summary.Failed = 1
		summary.Error = "account inspection failed"
		summary.FinishedAt = e.currentTime()
		e.finishScan(summary, previous, nil, runNative, false, probeCursor)
		if requestedSweep {
			e.updateProbeSweep(inspectionSweepProgress{
				Total: probeSweepTotal, Completed: probeSweepCompleted, Remaining: probeSweepRemaining,
				Source: probeSweepSource, StartedAt: probeSweepStartedAt, Targets: probeSweepTargets,
			}, true)
		}
		return
	}
	if len(accounts) > maxInspectionAccounts {
		summary.Truncated = len(accounts) - maxInspectionAccounts
		accounts = accounts[:maxInspectionAccounts]
	}
	accountsByID := make(map[string]Account, len(accounts))
	for _, account := range accounts {
		if strings.TrimSpace(account.ID) != "" {
			accountsByID[account.ID] = account
		}
	}
	now := e.currentTime()
	probeProcessed := 0
	if runProbe {
		e.mu.Lock()
		e.probePhase = InspectionProbePhasePrimary
		e.dirty = true
		e.generation++
		e.mu.Unlock()
		if probeSweep && len(probeSweepTargets) == 0 {
			scanManuallyDisabled := inspectionRunScansManuallyDisabled(runMode, probeSweepSource, policy.ScanManuallyDisabled)
			probeSweepTargets = inspectionRunTargetIDs(runMode, accounts, previous, scanManuallyDisabled)
			probeSweepTotal = len(probeSweepTargets)
			probeSweepCompleted = 0
			probeSweepRemaining = max(0, probeSweepTotal-probeSweepCompleted)
		}
		probePolicy := policy
		probeAccounts := accounts
		probeCursorForRun := probeCursor
		if probeSweep {
			batchEnd := min(probeSweepCompleted+probePolicy.ModelProbeBatchSize, len(probeSweepTargets))
			batchTargets := probeSweepTargets[probeSweepCompleted:batchEnd]
			probeProcessed = len(batchTargets)
			probeAccounts = inspectionProbeAccountsForTargets(accounts, batchTargets)
			probePolicy.ModelProbeBatchSize = len(probeAccounts)
			// Sweep targets already passed the run-mode and disabled-account policy
			// checks. Do not silently filter explicitly selected or manual-full
			// disabled accounts a second time while executing the batch.
			probePolicy.ScanManuallyDisabled = true
			probeCursorForRun = 0
		}
		primaryObserved := 0
		observePrimary := func(result ModelTestResult) {
			account, exists := accountsByID[result.AccountID]
			if !exists {
				return
			}
			primaryObserved++
			liveRecord := previous[result.AccountID]
			applyModelProbeToInspection(&liveRecord, result, policy)
			observedAt := e.currentTime()
			decision := decideInspection(account, liveRecord, observedAt)
			updateInspectionRecord(&liveRecord, account, decision, observedAt)
			liveRecord.Result.RunID = activeRunID
			liveRecord.Result.RunPhase = InspectionProbePhasePrimary
			liveRecord.Result.RunObservedAt = timePointer(observedAt)
			e.publishLiveProbeRecord(liveRecord, activeRunID, InspectionProbePhasePrimary, probeSweepCompleted+primaryObserved, probeSweepTotal, -1)
		}
		probeResults, nextCursor := runInspectionModelProbesObserved(ctx, modelTests, probeAccounts, previous, probePolicy, probeCursorForRun, config.ManagementBaseURL, managementKey, observePrimary)
		retryCount := inspectionProbeRetryCount(probeResults)
		if retryCount > 0 {
			e.mu.Lock()
			e.probePhase = InspectionProbePhaseRetry
			e.retryTotal += retryCount
			e.updateRunHistoryProgressLocked()
			e.dirty = true
			e.generation++
			e.mu.Unlock()
		}
		retryObserved := 0
		observeRetry := func(result ModelTestResult) {
			account, exists := accountsByID[result.AccountID]
			if !exists {
				return
			}
			retryObserved++
			liveRecord := previous[result.AccountID]
			applyModelProbeToInspection(&liveRecord, result, policy)
			observedAt := e.currentTime()
			decision := decideInspection(account, liveRecord, observedAt)
			updateInspectionRecord(&liveRecord, account, decision, observedAt)
			liveRecord.Result.RunID = activeRunID
			liveRecord.Result.RunPhase = InspectionProbePhaseRetry
			liveRecord.Result.RunObservedAt = timePointer(observedAt)
			e.publishLiveProbeRecord(liveRecord, activeRunID, InspectionProbePhaseRetry, -1, probeSweepTotal, retryCompletedBase+retryObserved)
		}
		retryResults, _ := retryInspectionProbeResultsObserved(ctx, modelTests, probeAccounts, probeResults, probePolicy, config.ManagementBaseURL, managementKey, observeRetry)
		if len(retryResults) > 0 {
			probeResults = mergeInspectionProbeResults(probeResults, retryResults)
		}
		if !probeSweep {
			probeCursor = nextCursor
		}
		retried := make(map[string]struct{}, len(retryResults))
		for _, result := range retryResults {
			retried[result.AccountID] = struct{}{}
		}
		for _, result := range probeResults {
			record := previous[result.AccountID]
			applyModelProbeToInspection(&record, result, policy)
			phase := InspectionProbePhasePrimary
			if _, exists := retried[result.AccountID]; exists {
				phase = InspectionProbePhaseRetry
			}
			observedAt := result.TestedAt.UTC()
			if observedAt.IsZero() {
				observedAt = e.currentTime()
			}
			record.Result.RunID = activeRunID
			record.Result.RunPhase = phase
			record.Result.RunObservedAt = timePointer(observedAt)
			previous[result.AccountID] = record
		}
	}
	// Probe timestamps are captured while the batch runs. Refresh the decision
	// clock after the batch so freshly observed evidence is not rejected as a
	// future timestamp and replaced by stale native account state.
	now = e.currentTime()
	next := make(map[string]inspectionRecord, len(accounts))
	for _, account := range accounts {
		if ctx.Err() != nil {
			return
		}
		id := strings.TrimSpace(account.ID)
		if id == "" {
			summary.Failed++
			continue
		}
		record := previous[id]
		decision := decideInspection(account, record, now)
		updateInspectionRecord(&record, account, decision, now)
		next[id] = record
		accountsByID[id] = account
		incrementInspectionSummary(&summary, record.Result.Health)
	}
	armed := strings.TrimSpace(managementKey) != "" && modelTests != nil
	triggered, anomalySweepSize := e.evaluateAnomalyTrigger(policy, accountsByID, next, now, scheduled && runNative, armed)
	e.evaluateInspectionNotification(policy, accountsByID, next, now, scheduled && runNative)
	if triggered && !policy.AnomalyNotificationOnly {
		probeSweep = true
		probeSweepRemaining = anomalySweepSize
		probeSweepTotal = anomalySweepSize
		probeSweepCompleted = 0
		probeSweepSource = InspectionSweepSourceAnomaly
		probeSweepStartedAt = now
		probeSweepTargets = inspectionProbeEligibleAccountIDs(accounts, next, policy.ScanManuallyDisabled)
		probeSweepTotal = len(probeSweepTargets)
		probeSweepRemaining = probeSweepTotal
	}
	if probeSweep {
		probeSweepCompleted += probeProcessed
		if probeSweepCompleted > probeSweepTotal {
			probeSweepCompleted = probeSweepTotal
		}
		probeSweepRemaining = max(0, probeSweepTotal-probeSweepCompleted)
	}
	e.mu.RLock()
	stopped := e.stopRequested
	e.mu.RUnlock()
	if stopped {
		e.mu.Lock()
		e.probeSweepStatus = InspectionSweepStatusStopped
		e.probePhase = InspectionProbePhaseStopped
		e.running = false
		e.pending = false
		e.mu.Unlock()
		e.requestPersist()
		return
	}
	actionSummary, actions := e.applyAutomaticActions(ctx, policy, accountsByID, next, now, config.ManagementBaseURL, managementKey)
	summary.AutoDisabled += actionSummary.AutoDisabled
	summary.AutoEnabled += actionSummary.AutoEnabled
	summary.DeletePending += actionSummary.DeletePending
	summary.Failed += actionSummary.Failed
	if actionSummary.Error != "" {
		summary.Error = actionSummary.Error
	}
	summary.Scanned = len(next)
	summary.FinishedAt = e.currentTime()
	e.finishScan(summary, next, actions, runNative, runProbe, probeCursor)
	if probeSweep {
		stalled := runProbe && probeProcessed == 0 && probeSweepRemaining > 0
		e.updateProbeSweep(inspectionSweepProgress{
			Total: probeSweepTotal, Completed: probeSweepCompleted, Remaining: probeSweepRemaining,
			Source: probeSweepSource, StartedAt: probeSweepStartedAt, Targets: probeSweepTargets,
		}, stalled)
		if probeSweepRemaining > 0 && !stalled {
			e.requestProbeSweep()
		} else if probeSweepRemaining == 0 {
			e.mu.Lock()
			e.probePhase = InspectionProbePhaseDone
			e.runMode = ""
			e.runHealth = nil
			e.runSelected = nil
			e.mu.Unlock()
		}
	}
	if policy.AutoDelete && deletions != nil && strings.TrimSpace(managementKey) != "" {
		e.ExecutePendingDeletes(ctx, deletions, config.ManagementBaseURL, managementKey)
	}
	managementKey = ""
}

func (e *InspectionEngine) clearScanRunning() {
	e.mu.Lock()
	e.running = false
	e.scanStarted = time.Time{}
	e.mu.Unlock()
}

func (e *InspectionEngine) finishScan(summary InspectionRunSummary, records map[string]inspectionRecord, actions []InspectionAction, ranNative, ranProbe bool, probeCursor int) {
	e.mu.Lock()
	e.running = false
	e.scanStarted = time.Time{}
	e.lastRun = summary
	if ranNative {
		e.lastNativeRunAt = summary.FinishedAt
	}
	if ranProbe {
		e.lastProbeRunAt = summary.FinishedAt
		e.probeCursor = probeCursor
	}
	e.records = records
	if e.activeRunID != "" {
		if e.activeRunModeLocked() == InspectionRunModeNative || !ranProbe {
			e.updateRunHistorySummaryLocked(summary)
		} else {
			for index := len(e.runs) - 1; index >= 0; index-- {
				if e.runs[index].ID != e.activeRunID {
					continue
				}
				accumulated := mergeInspectionRunSummary(e.runs[index].Summary, summary)
				e.runs[index].Summary = accumulated
				e.lastRun = accumulated
				break
			}
		}
		if !ranProbe {
			status := InspectionSweepStatusCompleted
			if summary.Error != "" {
				status = InspectionSweepStatusFailed
			}
			e.updateRunHistoryLocked(status, InspectionProbePhaseDone, summary.FinishedAt)
		}
	}
	for _, action := range actions {
		e.appendActionLocked(action)
	}
	e.dirty = true
	e.generation++
	e.mu.Unlock()
	e.persist()
}

func mergeInspectionRunSummary(previous, current InspectionRunSummary) InspectionRunSummary {
	merged := current
	if !previous.StartedAt.IsZero() {
		merged.StartedAt = previous.StartedAt.UTC()
	}
	merged.AutoDisabled = previous.AutoDisabled + current.AutoDisabled
	merged.AutoEnabled = previous.AutoEnabled + current.AutoEnabled
	merged.Failed = previous.Failed + current.Failed
	if merged.Error == "" {
		merged.Error = previous.Error
	}
	return merged
}

func (e *InspectionEngine) publishLiveProbeRecord(record inspectionRecord, runID, phase string, primaryCompleted, primaryTotal, retryCompleted int) {
	if e == nil || strings.TrimSpace(record.Result.ID) == "" {
		return
	}
	e.mu.Lock()
	e.records[record.Result.ID] = record
	if runID != "" && e.activeRunID == runID {
		e.probePhase = normalizeInspectionProbePhase(phase)
		if primaryTotal >= 0 {
			e.probeSweepTotal = min(max(0, primaryTotal), maxInspectionAccounts)
		}
		if primaryCompleted >= 0 {
			e.probeSweepCompleted = min(max(0, primaryCompleted), e.probeSweepTotal)
			e.probeSweepRemaining = max(0, e.probeSweepTotal-e.probeSweepCompleted)
		}
		if retryCompleted >= 0 {
			e.retryCompleted = min(max(0, retryCompleted), e.retryTotal)
		}
		e.updateLiveRunSummaryLocked(record.Result)
		e.updateRunHistoryProgressLocked()
	}
	e.dirty = true
	e.generation++
	e.mu.Unlock()
	e.requestPersist()
}

func (e *InspectionEngine) updateLiveRunSummaryLocked(result InspectionResult) {
	if e.activeRunID == "" || result.RunID != e.activeRunID {
		return
	}
	if e.activeRunHealthID != e.activeRunID || e.activeRunHealth == nil {
		e.activeRunHealthID = e.activeRunID
		e.activeRunHealth = make(map[string]string)
	}
	previousHealth, existed := e.activeRunHealth[result.ID]
	e.activeRunHealth[result.ID] = normalizeInspectionHealth(result.Health)
	for index := len(e.runs) - 1; index >= 0; index-- {
		if e.runs[index].ID != e.activeRunID {
			continue
		}
		summary := &e.runs[index].Summary
		if summary.StartedAt.IsZero() {
			summary.StartedAt = e.runs[index].StartedAt
		}
		if existed {
			decrementInspectionSummary(summary, previousHealth)
		}
		incrementInspectionSummary(summary, result.Health)
		summary.Scanned = len(e.activeRunHealth)
		summary.FinishedAt = time.Time{}
		return
	}
}

func (e *InspectionEngine) updateRunHistoryProgressLocked() {
	for index := len(e.runs) - 1; index >= 0; index-- {
		if e.runs[index].ID != e.activeRunID {
			continue
		}
		e.runs[index].Status = InspectionSweepStatusRunning
		e.runs[index].Phase = normalizeInspectionProbePhase(e.probePhase)
		e.runs[index].PrimaryTotal = e.probeSweepTotal
		e.runs[index].PrimaryDone = e.probeSweepCompleted
		e.runs[index].RetryTotal = e.retryTotal
		e.runs[index].RetryDone = e.retryCompleted
		return
	}
}

func (e *InspectionEngine) activeRunModeLocked() string {
	for index := len(e.runs) - 1; index >= 0; index-- {
		if e.runs[index].ID == e.activeRunID {
			return e.runs[index].Mode
		}
	}
	return ""
}

func (e *InspectionEngine) persist() {
	if e == nil {
		return
	}
	e.mu.RLock()
	if !e.dirty || strings.TrimSpace(e.store) == "" {
		e.mu.RUnlock()
		return
	}
	storePath := e.store
	generation := e.generation
	state := e.persistedStateLocked()
	e.mu.RUnlock()

	e.storeMu.Lock()
	errSave := saveInspectionState(storePath, state)
	e.storeMu.Unlock()
	e.mu.Lock()
	if errSave != nil {
		e.storageErr = "inspection state could not be persisted"
	} else if e.store == storePath && e.generation == generation {
		e.dirty = false
		e.storageErr = ""
	}
	e.mu.Unlock()
}

func (e *InspectionEngine) persistedStateLocked() persistedInspectionState {
	return persistedInspectionState{
		Version:               inspectionStoreVersion,
		Policy:                e.policy,
		Records:               cloneInspectionRecords(e.records),
		Actions:               append([]InspectionAction(nil), e.actions...),
		LastRun:               e.lastRun,
		ProbeCursor:           e.probeCursor,
		LastNativeRunAt:       e.lastNativeRunAt,
		LastProbeRunAt:        e.lastProbeRunAt,
		ProbeSweepRemaining:   e.probeSweepRemaining,
		ProbeSweepTotal:       e.probeSweepTotal,
		ProbeSweepCompleted:   e.probeSweepCompleted,
		ProbeSweepSource:      e.probeSweepSource,
		ProbeSweepStatus:      e.probeSweepStatus,
		ProbeSweepStartedAt:   e.probeSweepStartedAt,
		ProbeSweepTargets:     append([]string(nil), e.probeSweepTargets...),
		AnomalyTriggerPending: e.anomalyTriggerPending,
		LastAnomalyTriggerAt:  e.lastAnomalyTriggerAt,
		LastNotificationAt:    e.lastNotificationAt,
		RunMode:               e.runMode,
		RunHealth:             append([]string(nil), e.runHealth...),
		RunSelected:           append([]string(nil), e.runSelected...),
		ProbePhase:            e.probePhase,
		RetryTotal:            e.retryTotal,
		RetryCompleted:        e.retryCompleted,
		StopRequested:         e.stopRequested,
		Runs:                  append([]InspectionRunRecord(nil), e.runs...),
		ActiveRunID:           e.activeRunID,
	}
}

func (e *InspectionEngine) ensureInspectionRecordCapacityLocked(incomingID string) {
	if incomingID == "" {
		return
	}
	for len(e.records) >= maxInspectionAccounts {
		candidate := oldestEvictableInspectionRecord(e.records)
		if candidate == "" {
			return
		}
		delete(e.records, candidate)
	}
}

func oldestEvictableInspectionRecord(records map[string]inspectionRecord) string {
	candidate := ""
	protectedCandidate := ""
	for id, record := range records {
		if inspectionRecordProtected(record) {
			if olderInspectionRecord(id, record, protectedCandidate, records[protectedCandidate]) {
				protectedCandidate = id
			}
			continue
		}
		if olderInspectionRecord(id, record, candidate, records[candidate]) {
			candidate = id
		}
	}
	if candidate != "" {
		return candidate
	}
	return protectedCandidate
}

func inspectionRecordProtected(record inspectionRecord) bool {
	return record.Result.OwnedDisable || record.Result.DeleteEligibleAt != nil ||
		record.Result.Recommendation == InspectionRecommendationDelete ||
		record.Result.AutoActionStatus == InspectionActionPending ||
		!record.DeleteRetryAfter.IsZero()
}

func olderInspectionRecord(id string, record inspectionRecord, candidateID string, candidate inspectionRecord) bool {
	if candidateID == "" {
		return true
	}
	activity := inspectionRecordActivityTime(record)
	candidateActivity := inspectionRecordActivityTime(candidate)
	if activity.Equal(candidateActivity) {
		return id < candidateID
	}
	return activity.Before(candidateActivity)
}

func inspectionRecordActivityTime(record inspectionRecord) time.Time {
	activity := record.Result.LastCheckedAt
	for _, observedAt := range []time.Time{
		record.Signal.LastFailureAt,
		record.Signal.LastSuccessAt,
		record.Probe.TestedAt,
		record.DisabledAt,
		record.DeleteRetryAfter,
	} {
		if observedAt.After(activity) {
			activity = observedAt
		}
	}
	for _, observedAt := range []*time.Time{
		record.Result.LastFailureAt,
		record.Result.LastSuccessAt,
		record.Result.ProbeTestedAt,
		record.Result.RunObservedAt,
		record.Result.ReviewedAt,
	} {
		if observedAt != nil && observedAt.After(activity) {
			activity = *observedAt
		}
	}
	return activity
}

func (e *InspectionEngine) startRunHistoryLocked(mode, source string, startedAt time.Time) {
	mode = normalizeInspectionRunMode(mode)
	source = normalizeInspectionSweepSource(source)
	if mode == "" || source == "" {
		return
	}
	if e.activeRunID != "" {
		return
	}
	startedAt = startedAt.UTC()
	id := fmt.Sprintf("inspection-%d", startedAt.UnixNano())
	e.activeRunID = id
	e.activeRunHealthID = id
	e.activeRunHealth = make(map[string]string)
	e.runs = append(e.runs, InspectionRunRecord{ID: id, Mode: mode, Source: source, Status: InspectionSweepStatusRunning, Phase: InspectionProbePhaseListing, StartedAt: startedAt, Summary: InspectionRunSummary{StartedAt: startedAt}})
	if len(e.runs) > maxInspectionRuns {
		e.runs = e.runs[len(e.runs)-maxInspectionRuns:]
	}
}

func (e *InspectionEngine) updateRunHistorySummaryLocked(summary InspectionRunSummary) {
	for index := len(e.runs) - 1; index >= 0; index-- {
		if e.runs[index].ID == e.activeRunID {
			e.runs[index].Summary = summary
			if e.runs[index].Mode == InspectionRunModeNative {
				e.runs[index].PrimaryTotal = summary.Scanned
				e.runs[index].PrimaryDone = summary.Scanned
			}
			return
		}
	}
}

func (e *InspectionEngine) updateRunHistoryLocked(status, phase string, finishedAt time.Time) {
	for index := len(e.runs) - 1; index >= 0; index-- {
		if e.runs[index].ID != e.activeRunID {
			continue
		}
		e.runs[index].Status = normalizeInspectionSweepStatus(status)
		e.runs[index].Phase = normalizeInspectionProbePhase(phase)
		if e.runs[index].Mode != InspectionRunModeNative {
			e.runs[index].PrimaryTotal = e.probeSweepTotal
			e.runs[index].PrimaryDone = e.probeSweepCompleted
			e.runs[index].RetryTotal = e.retryTotal
			e.runs[index].RetryDone = e.retryCompleted
		}
		if status == InspectionSweepStatusCompleted || status == InspectionSweepStatusFailed || status == InspectionSweepStatusStopped {
			e.runs[index].FinishedAt = finishedAt.UTC()
			e.activeRunID = ""
		}
		return
	}
}

func recentInspectionRuns(runs []InspectionRunRecord, limit int) []InspectionRunRecord {
	if limit <= 0 || limit > maxInspectionRuns {
		limit = 10
	}
	start := len(runs) - limit
	if start < 0 {
		start = 0
	}
	out := append([]InspectionRunRecord{}, runs[start:]...)
	for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
		out[left], out[right] = out[right], out[left]
	}
	return out
}

func activeInspectionRun(runs []InspectionRunRecord, activeID string) *InspectionRunRecord {
	if activeID == "" {
		return nil
	}
	for index := len(runs) - 1; index >= 0; index-- {
		if runs[index].ID != activeID {
			continue
		}
		clone := runs[index]
		return &clone
	}
	return nil
}

func liveInspectionResults(records map[string]inspectionRecord, activeID string, limit int) []InspectionResult {
	if activeID == "" || limit <= 0 {
		return []InspectionResult{}
	}
	results := make([]InspectionResult, 0, min(limit, len(records)))
	for _, record := range records {
		if record.Result.RunID != activeID || record.Result.RunObservedAt == nil {
			continue
		}
		results = append(results, cloneInspectionResult(record.Result))
	}
	sort.Slice(results, func(left, right int) bool {
		leftAt := results[left].RunObservedAt
		rightAt := results[right].RunObservedAt
		if leftAt != nil && rightAt != nil && !leftAt.Equal(*rightAt) {
			return leftAt.After(*rightAt)
		}
		return results[left].ID < results[right].ID
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

func (e *InspectionEngine) requestPersist() {
	select {
	case e.persistWake <- struct{}{}:
	default:
	}
}

func (e *InspectionEngine) scheduledEnabled() bool {
	e.mu.RLock()
	enabled := (e.policy.Enabled || e.policy.PassiveCircuitEnabled || (e.policy.ModelProbeEnabled && strings.TrimSpace(e.managementKey) != "")) && !e.closed
	e.mu.RUnlock()
	return enabled
}

func (e *InspectionEngine) scanInterval() time.Duration {
	e.mu.RLock()
	policy := e.policy
	e.mu.RUnlock()
	policy = normalizeInspectionPolicy(policy)
	minutes := nativeInspectionInterval(policy)
	if policy.ModelProbeEnabled && (!policy.Enabled || policy.ModelProbeIntervalMinutes < minutes) {
		minutes = policy.ModelProbeIntervalMinutes
	}
	return time.Duration(minutes) * time.Minute
}

func nativeInspectionInterval(policy InspectionPolicy) int {
	policy = normalizeInspectionPolicy(policy)
	minutes := policy.ScanIntervalMinutes
	if policy.PassiveCircuitEnabled && policy.PassiveCircuitMinutes < minutes {
		minutes = policy.PassiveCircuitMinutes
	}
	return minutes
}

func (e *InspectionEngine) currentTime() time.Time {
	now := time.Now
	if e != nil && e.now != nil {
		now = e.now
	}
	return now().UTC()
}

func updateInspectionRecord(record *inspectionRecord, account Account, decision inspectionDecision, now time.Time) {
	previousHealth := record.Result.Health
	previousReason := record.Result.ReasonCode
	result := record.Result
	result.ID = account.ID
	result.Name = account.Name
	result.Provider = account.Provider
	result.Type = account.Type
	result.PlanType = account.PlanType
	result.Health = decision.Health
	result.ReasonCode = decision.ReasonCode
	result.Confidence = decision.Confidence
	result.Recommendation = decision.Recommendation
	result.Disabled = account.Disabled
	result.Editable = account.Editable
	result.AutoDisableEligible = decision.AutoDisableEligible
	result.LastCheckedAt = now.UTC()
	result.OwnedDisable = record.Result.OwnedDisable
	result.LastFailureAt = timePointer(record.Signal.LastFailureAt)
	result.LastSuccessAt = timePointer(record.Signal.LastSuccessAt)
	result.RecoverAfter = timePointer(decision.RecoverAfter)
	result.SignalSource = normalizeInspectionSignalSource(decision.SignalSource)
	result.StatusCode = boundedHTTPStatus(record.Signal.StatusCode)
	if result.SignalSource == InspectionSignalActiveProbe {
		result.StatusCode = boundedHTTPStatus(record.Probe.StatusCode)
	}
	result.QuotaWindow = normalizeInspectionQuotaWindow(decision.QuotaWindow)
	result.UsageTotalTokens = 0
	result.UsageLastRequestAt = nil
	result.CodexUsage = nil
	if account.Usage != nil {
		result.UsageTotalTokens = nonNegative(account.Usage.TotalTokens)
		result.UsageLastRequestAt = cloneTimePointer(account.Usage.LastRequestAt)
		result.CodexUsage = cloneCodexUsage(account.Usage.Codex)
	}
	if decision.Health == InspectionHealthReview {
		if previousHealth != decision.Health || previousReason != decision.ReasonCode ||
			(result.ReviewedAt != nil && record.Signal.LastFailureAt.After(result.ReviewedAt.UTC())) {
			result.ReviewStatus = InspectionReviewPending
			result.ReviewedAt = nil
		} else {
			result.ReviewStatus = normalizeInspectionReviewStatus(result.ReviewStatus, result.Health)
		}
	} else {
		result.ReviewStatus = ""
		result.ReviewedAt = nil
	}
	if result.OwnedDisable && !record.DisabledRecoverAfter.IsZero() {
		result.RecoverAfter = timePointer(record.DisabledRecoverAfter)
	}

	if inspectionDecisionIsStrongFailure(decision) {
		if decision.FailureCount > 0 {
			result.FailureStreak = boundedCounter(decision.FailureCount)
		} else if previousHealth == decision.Health {
			result.FailureStreak = boundedCounter(result.FailureStreak + 1)
		} else {
			result.FailureStreak = 1
		}
		result.HealthyStreak = 0
		if result.FirstUnhealthyAt == nil || previousHealth != decision.Health {
			result.FirstUnhealthyAt = timePointer(now)
		}
	} else if decision.Health == InspectionHealthHealthy {
		if decision.HealthyCount > 0 {
			result.HealthyStreak = boundedCounter(decision.HealthyCount)
		} else {
			result.HealthyStreak = boundedCounter(result.HealthyStreak + 1)
		}
		result.FailureStreak = 0
		result.FirstUnhealthyAt = nil
	} else {
		result.FailureStreak = 0
		result.HealthyStreak = 0
		result.FirstUnhealthyAt = nil
	}
	if result.Disabled && decision.Health == InspectionHealthHealthy {
		result.Recommendation = InspectionRecommendationEnable
	} else if result.Disabled && result.Recommendation == InspectionRecommendationDisable {
		result.Recommendation = InspectionRecommendationKeep
	} else if !result.Disabled && result.Recommendation == InspectionRecommendationEnable {
		result.Recommendation = InspectionRecommendationKeep
	}
	result.CircuitOpen = result.OwnedDisable && record.DisableReason == "passive_circuit_open"
	if result.CircuitOpen {
		result.FailureStreak = max(result.FailureStreak, record.Signal.ConsecutiveFailures, record.Probe.ConsecutiveFailures)
	} else {
		result.CircuitReasonCode = ""
	}
	if result.OwnedDisable && result.Disabled && decision.Health == InspectionHealthDeactivated &&
		decision.Recommendation == InspectionRecommendationDelete && decision.Confidence == InspectionConfidenceHigh &&
		(record.DisableReason != decision.ReasonCode) {
		switch decision.ReasonCode {
		case "account_deactivated", "workspace_deactivated":
			record.DisableReason = decision.ReasonCode
			record.DisabledAt = now.UTC()
			record.DisabledRecoverAfter = time.Time{}
			record.DeleteRetryAfter = time.Time{}
			result.RecoverAfter = nil
			result.DeleteEligibleAt = nil
			result.AutoAction = ""
			result.AutoActionStatus = ""
			result.CircuitOpen = false
			result.CircuitReasonCode = ""
		}
	}
	record.Result = result
}

func incrementInspectionSummary(summary *InspectionRunSummary, health string) {
	if summary == nil {
		return
	}
	switch health {
	case InspectionHealthHealthy:
		summary.Healthy++
	case InspectionHealthQuotaLimited:
		summary.QuotaLimited++
	case InspectionHealthInvalidCredentials:
		summary.InvalidCredentials++
	case InspectionHealthDeactivated:
		summary.Deactivated++
	case InspectionHealthReview:
		summary.Review++
	case InspectionHealthUnavailable:
		summary.Unavailable++
	case InspectionHealthDisabled:
		summary.Disabled++
	default:
		summary.Unknown++
	}
}

func decrementInspectionSummary(summary *InspectionRunSummary, health string) {
	if summary == nil {
		return
	}
	switch health {
	case InspectionHealthHealthy:
		summary.Healthy = max(0, summary.Healthy-1)
	case InspectionHealthQuotaLimited:
		summary.QuotaLimited = max(0, summary.QuotaLimited-1)
	case InspectionHealthInvalidCredentials:
		summary.InvalidCredentials = max(0, summary.InvalidCredentials-1)
	case InspectionHealthDeactivated:
		summary.Deactivated = max(0, summary.Deactivated-1)
	case InspectionHealthReview:
		summary.Review = max(0, summary.Review-1)
	case InspectionHealthUnavailable:
		summary.Unavailable = max(0, summary.Unavailable-1)
	case InspectionHealthDisabled:
		summary.Disabled = max(0, summary.Disabled-1)
	default:
		summary.Unknown = max(0, summary.Unknown-1)
	}
}

func inspectionHealthIsStrongFailure(health string) bool {
	return health == InspectionHealthQuotaLimited || health == InspectionHealthInvalidCredentials || health == InspectionHealthDeactivated
}

func inspectionDecisionIsStrongFailure(decision inspectionDecision) bool {
	return inspectionHealthIsStrongFailure(decision.Health) ||
		(decision.SignalSource == InspectionSignalActiveProbe && decision.AutoDisableEligible) ||
		(decision.ReasonCode == "native_unavailable" && decision.AutoDisableEligible)
}

func inspectionResultIsStrongFailure(result InspectionResult) bool {
	return inspectionHealthIsStrongFailure(result.Health) ||
		(result.SignalSource == InspectionSignalActiveProbe && result.AutoDisableEligible) ||
		(result.ReasonCode == "native_unavailable" && result.AutoDisableEligible)
}

func inspectionHealthRank(health string) int {
	switch health {
	case InspectionHealthDeactivated:
		return 0
	case InspectionHealthInvalidCredentials:
		return 1
	case InspectionHealthQuotaLimited:
		return 2
	case InspectionHealthReview:
		return 3
	case InspectionHealthUnavailable:
		return 4
	case InspectionHealthDisabled:
		return 5
	case InspectionHealthUnknown:
		return 6
	case InspectionHealthHealthy:
		return 7
	default:
		return 8
	}
}

func timePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	value = value.UTC()
	return &value
}

func resetInspectionTimer(timer *time.Timer, duration time.Duration) {
	if duration <= 0 {
		duration = time.Duration(defaultInspectionInterval) * time.Minute
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

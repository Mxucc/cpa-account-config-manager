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
	mu                    sync.RWMutex
	scanMu                sync.Mutex
	storeMu               sync.Mutex
	wait                  sync.WaitGroup
	accounts              *AccountService
	host                  AuthHost
	mutations             *MutationCoordinator
	modelTests            *ModelTestService
	deletions             *AccountDeleteService
	config                Config
	store                 string
	policy                InspectionPolicy
	records               map[string]inspectionRecord
	actions               []InspectionAction
	lastRun               InspectionRunSummary
	probeCursor           int
	lastNativeRunAt       time.Time
	lastProbeRunAt        time.Time
	managementKey         string
	probeSweepRemaining   int
	probeSweepTotal       int
	probeSweepCompleted   int
	probeSweepSource      string
	probeSweepStatus      string
	probeSweepStartedAt   time.Time
	probeSweepTargets     []string
	anomalyTriggerPending bool
	lastAnomalyTriggerAt  time.Time
	anomalyEligible       int
	anomalyCount          int
	anomalyPercent        int
	running               bool
	pending               bool
	pendingProbe          bool
	pendingProbeSweep     bool
	scanStarted           time.Time
	storageErr            string
	dirty                 bool
	generation            uint64
	scanWake              chan struct{}
	persistWake           chan struct{}
	cancel                context.CancelFunc
	started               bool
	closed                bool
	now                   func() time.Time
}

func NewInspectionEngine(accounts *AccountService, host AuthHost, mutations *MutationCoordinator) *InspectionEngine {
	if mutations == nil {
		mutations = NewMutationCoordinator()
	}
	config := normalizeConfig(Config{})
	return &InspectionEngine{
		accounts:    accounts,
		host:        host,
		mutations:   mutations,
		config:      config,
		store:       inspectionStorePath(config.DataDir),
		policy:      defaultInspectionPolicy(),
		records:     make(map[string]inspectionRecord),
		scanWake:    make(chan struct{}, 1),
		persistWake: make(chan struct{}, 1),
		now:         time.Now,
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

func (e *InspectionEngine) Configure(config Config) {
	if e == nil {
		return
	}
	config = normalizeConfig(config)
	storePath := inspectionStorePath(config.DataDir)

	e.scanMu.Lock()
	defer e.scanMu.Unlock()
	e.mu.RLock()
	sameStore := e.started && e.store == storePath
	e.mu.RUnlock()
	if sameStore {
		e.mu.Lock()
		e.config = config
		e.mu.Unlock()
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

	e.mu.Lock()
	e.config = config
	e.store = storePath
	e.policy = state.Policy
	e.records = state.Records
	e.actions = state.Actions
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
	if e.probeSweepRemaining > 0 {
		e.probeSweepStatus = InspectionSweepStatusWaitingForAuth
	}
	e.anomalyTriggerPending = state.AnomalyTriggerPending
	e.lastAnomalyTriggerAt = state.LastAnomalyTriggerAt
	e.managementKey = ""
	e.storageErr = storageErr
	e.dirty = false
	e.generation++
	start := !e.started && !e.closed
	if start {
		ctx, cancel := context.WithCancel(context.Background())
		e.cancel = cancel
		e.started = true
		e.wait.Add(2)
		go e.scanLoop(ctx)
		go e.persistLoop(ctx)
	}
	e.mu.Unlock()
}

func (e *InspectionEngine) Snapshot() InspectionSnapshot {
	if e == nil {
		return InspectionSnapshot{Policy: defaultInspectionPolicy()}
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
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
		LastAnomalyTriggerAt:  e.lastAnomalyTriggerAt,
		StorageError:          e.storageErr,
	}
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
		armed := strings.TrimSpace(managementKey) != "" && e.modelTests != nil
		e.pendingProbe = e.pendingProbe || armed
		if e.pendingProbe {
			e.managementKey = strings.TrimSpace(managementKey)
		}
		if armed && e.probeSweepRemaining > 0 {
			e.pendingProbeSweep = true
			e.probeSweepStatus = InspectionSweepStatusRunning
		} else if armed && e.probeSweepRemaining == 0 && !e.pendingProbeSweep && e.probeSweepStatus != InspectionSweepStatusRunning {
			e.probeSweepTotal = 0
			e.probeSweepCompleted = 0
			e.probeSweepRemaining = 0
			e.probeSweepSource = InspectionSweepSourceManual
			e.probeSweepStatus = InspectionSweepStatusRunning
			e.probeSweepStartedAt = requestedAt
			e.probeSweepTargets = nil
			e.pendingProbeSweep = true
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
		if e.anomalyTriggerPending || e.probeSweepRemaining > 0 {
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
	inspection := e.records[authIndex]
	inspection.Result.ID = authIndex
	applyUsageRecordToInspection(&inspection, record, e.policy, now)
	e.records[authIndex] = inspection
	wake := e.started && passiveCircuitThresholdReached(e.policy, inspection)
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

func (e *InspectionEngine) ListResults(query InspectionResultQuery) InspectionResultList {
	query = normalizeInspectionResultQuery(query)
	e.mu.RLock()
	results := make([]InspectionResult, 0, len(e.records))
	for _, record := range e.records {
		result := cloneInspectionResult(record.Result)
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
		Total:    total,
		Page:     query.Page,
		PageSize: query.PageSize,
		Pages:    pages,
	}
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
	config := e.config
	e.mu.Unlock()
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
	now := e.currentTime()
	probeProcessed := 0
	if runProbe {
		if probeSweep && len(probeSweepTargets) == 0 {
			probeSweepTargets = inspectionProbeEligibleAccountIDs(accounts, previous, policy.ScanManuallyDisabled)
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
			probeCursorForRun = 0
		}
		probeResults, nextCursor := runInspectionModelProbes(ctx, modelTests, probeAccounts, previous, probePolicy, probeCursorForRun, config.ManagementBaseURL, managementKey)
		if !probeSweep {
			probeCursor = nextCursor
		}
		for _, result := range probeResults {
			record := previous[result.AccountID]
			applyModelProbeToInspection(&record, result, policy)
			previous[result.AccountID] = record
		}
	}
	next := make(map[string]inspectionRecord, len(accounts))
	accountsByID := make(map[string]Account, len(accounts))
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
	if triggered {
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
	actionSummary, actions := e.applyAutomaticActions(ctx, policy, accountsByID, next, now)
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
	for _, action := range actions {
		e.appendActionLocked(action)
	}
	e.dirty = true
	e.generation++
	e.mu.Unlock()
	e.persist()
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
	}
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
	if result.OwnedDisable && !record.DisabledRecoverAfter.IsZero() {
		result.RecoverAfter = timePointer(record.DisabledRecoverAfter)
	}

	if inspectionHealthIsStrongFailure(decision.Health) {
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
	if result.OwnedDisable && decision.Health == InspectionHealthHealthy {
		result.Recommendation = InspectionRecommendationEnable
	}
	result.CircuitOpen = result.OwnedDisable && record.DisableReason == "passive_circuit_open"
	if result.CircuitOpen {
		result.FailureStreak = max(result.FailureStreak, record.Signal.ConsecutiveFailures, record.Probe.ConsecutiveFailures)
	} else {
		result.CircuitReasonCode = ""
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

func inspectionHealthIsStrongFailure(health string) bool {
	return health == InspectionHealthQuotaLimited || health == InspectionHealthInvalidCredentials || health == InspectionHealthDeactivated
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

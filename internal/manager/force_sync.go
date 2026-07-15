package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrForcePreviewNotFound = errors.New("force-sync preview not found")
	ErrForcePreviewExpired  = errors.New("force-sync preview expired")
	ErrForcePreviewStale    = errors.New("default policy changed; create a new force-sync preview")
	ErrForcePolicyEmpty     = errors.New("default policy does not manage any fields")
	ErrForceNoEligible      = errors.New("force-sync preview contains no eligible auth files")
)

type ForcePolicySummary struct {
	Fields     []string `json:"fields"`
	Priority   *int     `json:"priority"`
	Websockets *bool    `json:"websockets"`
}

type ForceSyncPreview struct {
	ID            string             `json:"id"`
	CreatedAt     time.Time          `json:"created_at"`
	ExpiresAt     time.Time          `json:"expires_at"`
	Total         int                `json:"total"`
	Eligible      int                `json:"eligible"`
	ReadOnly      int                `json:"read_only"`
	PhysicalFiles int                `json:"physical_files"`
	Policy        ForcePolicySummary `json:"policy"`
	Warnings      []string           `json:"warnings,omitempty"`
	Targets       []PreviewTarget    `json:"targets"`
}

type ForceSyncJobSnapshot struct {
	ID         string             `json:"id,omitempty"`
	State      string             `json:"state"`
	Running    bool               `json:"running"`
	Total      int                `json:"total"`
	Eligible   int                `json:"eligible"`
	Done       int                `json:"done"`
	Succeeded  int                `json:"succeeded"`
	Failed     int                `json:"failed"`
	Conflicts  int                `json:"conflicts"`
	Skipped    int                `json:"skipped"`
	Workers    int                `json:"workers"`
	Policy     ForcePolicySummary `json:"policy"`
	StartedAt  time.Time          `json:"started_at,omitempty"`
	FinishedAt time.Time          `json:"finished_at,omitempty"`
	Results    []JobResult        `json:"results,omitempty"`
}

type forceSyncPreviewSnapshot struct {
	public  ForceSyncPreview
	policy  DefaultPolicy
	targets []Account
}

type forceSyncRun struct {
	jobID   string
	owner   string
	policy  DefaultPolicy
	targets []Account
}

type ForceSyncEngine struct {
	mu        sync.Mutex
	wait      sync.WaitGroup
	accounts  *AccountService
	host      AuthHost
	policies  *PolicyEngine
	mutations *MutationCoordinator
	config    Config
	now       func() time.Time
	previews  map[string]forceSyncPreviewSnapshot
	running   bool
	cancel    context.CancelFunc
	snapshot  ForceSyncJobSnapshot
}

func NewForceSyncEngine(accounts *AccountService, host AuthHost, policies *PolicyEngine, mutations *MutationCoordinator) *ForceSyncEngine {
	if mutations == nil {
		mutations = NewMutationCoordinator()
	}
	return &ForceSyncEngine{
		accounts:  accounts,
		host:      host,
		policies:  policies,
		mutations: mutations,
		config:    normalizeConfig(Config{}),
		now:       time.Now,
		previews:  make(map[string]forceSyncPreviewSnapshot),
		snapshot:  ForceSyncJobSnapshot{State: JobStateIdle},
	}
}

func (e *ForceSyncEngine) Configure(config Config) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.config = normalizeConfig(config)
	e.mu.Unlock()
}

func (e *ForceSyncEngine) Preview(ctx context.Context) (ForceSyncPreview, error) {
	if e == nil || e.accounts == nil || e.policies == nil {
		return ForceSyncPreview{}, fmt.Errorf("force-sync service is unavailable")
	}
	policy := e.policies.Snapshot().Policy
	if !policy.ManagesFields() {
		return ForceSyncPreview{}, ErrForcePolicyEmpty
	}
	resolved, errResolve := e.accounts.ResolveTargets(ctx, TargetScope{Mode: "filtered"})
	if errResolve != nil {
		return ForceSyncPreview{}, fmt.Errorf("resolve force-sync targets: %w", errResolve)
	}
	if len(resolved.Accounts) == 0 {
		return ForceSyncPreview{}, fmt.Errorf("no auth files are available")
	}

	publicTargets := make([]PreviewTarget, 0, len(resolved.Accounts))
	eligibleTargets := make([]Account, 0, len(resolved.Accounts))
	readOnly := 0
	for _, account := range resolved.Accounts {
		publicTargets = append(publicTargets, PreviewTarget{
			ID:             account.ID,
			Name:           account.Name,
			Provider:       account.Provider,
			Label:          firstNonEmpty(account.Label, account.Email),
			Eligible:       account.Editable,
			ReadOnlyReason: account.ReadOnlyReason,
		})
		if account.Editable {
			eligibleTargets = append(eligibleTargets, account)
		} else {
			readOnly++
		}
	}
	now := e.now().UTC()
	id, errID := randomIdentifier()
	if errID != nil {
		return ForceSyncPreview{}, fmt.Errorf("create force-sync preview id: %w", errID)
	}
	warnings := make([]string, 0, 1)
	if readOnly > 0 {
		warnings = append(warnings, fmt.Sprintf("%d target(s) are read-only and will be skipped", readOnly))
	}
	snapshot := forceSyncPreviewSnapshot{
		public: ForceSyncPreview{
			ID:            id,
			CreatedAt:     now,
			ExpiresAt:     now.Add(defaultPreviewTTL),
			Total:         len(publicTargets),
			Eligible:      len(eligibleTargets),
			ReadOnly:      readOnly,
			PhysicalFiles: resolved.PhysicalFiles,
			Policy:        forcePolicySummary(policy),
			Warnings:      warnings,
			Targets:       publicTargets,
		},
		policy:  cloneDefaultPolicy(policy),
		targets: append([]Account(nil), eligibleTargets...),
	}
	e.mu.Lock()
	e.purgePreviewsLocked(now)
	if len(e.previews) >= maxPreviewEntries {
		e.removeOldestPreviewLocked()
	}
	e.previews[id] = cloneForcePreviewSnapshot(snapshot)
	e.mu.Unlock()
	return cloneForcePreview(snapshot.public), nil
}

func (e *ForceSyncEngine) Start(previewID string) (ForceSyncJobSnapshot, error) {
	if e == nil || e.policies == nil || e.host == nil {
		return ForceSyncJobSnapshot{}, fmt.Errorf("force-sync service is unavailable")
	}
	e.mu.Lock()
	alreadyRunning := e.running
	e.mu.Unlock()
	if alreadyRunning {
		return ForceSyncJobSnapshot{}, ErrJobBusy
	}
	preview, errPreview := e.getPreview(strings.TrimSpace(previewID))
	if errPreview != nil {
		return ForceSyncJobSnapshot{}, errPreview
	}
	if len(preview.targets) == 0 {
		return ForceSyncJobSnapshot{}, ErrForceNoEligible
	}

	e.policies.operationMu.Lock()
	policyLockTransferred := false
	defer func() {
		if !policyLockTransferred {
			e.policies.operationMu.Unlock()
		}
	}()
	e.policies.mu.RLock()
	currentPolicy := cloneDefaultPolicy(e.policies.policy)
	e.policies.mu.RUnlock()
	if !managedPolicyEqual(currentPolicy, preview.policy) {
		return ForceSyncJobSnapshot{}, ErrForcePreviewStale
	}

	jobID, errID := randomIdentifier()
	if errID != nil {
		return ForceSyncJobSnapshot{}, fmt.Errorf("create force-sync job id: %w", errID)
	}
	owner := "force:" + jobID
	e.mu.Lock()
	if e.running || !e.mutations.TryAcquire(owner) {
		e.mu.Unlock()
		return ForceSyncJobSnapshot{}, ErrJobBusy
	}
	mutationTransferred := false
	defer func() {
		if !mutationTransferred {
			e.mutations.Release(owner)
		}
	}()
	workers := e.config.Workers
	if workers > len(preview.targets) {
		workers = len(preview.targets)
	}
	if workers < 1 {
		workers = 1
	}
	results := make([]JobResult, 0, len(preview.public.Targets))
	for _, target := range preview.public.Targets {
		status := ResultPending
		errorMessage := ""
		if !target.Eligible {
			status = ResultSkipped
			errorMessage = target.ReadOnlyReason
		}
		results = append(results, JobResult{
			ID:       target.ID,
			Name:     target.Name,
			Provider: target.Provider,
			Label:    target.Label,
			Status:   status,
			Error:    errorMessage,
		})
	}
	e.snapshot = ForceSyncJobSnapshot{
		ID:        jobID,
		State:     JobStateRunning,
		Running:   true,
		Total:     len(results),
		Eligible:  len(preview.targets),
		Workers:   workers,
		Policy:    forcePolicySummary(preview.policy),
		StartedAt: e.now().UTC(),
		Results:   results,
	}
	e.recountLocked()
	e.running = true
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	delete(e.previews, preview.public.ID)
	run := forceSyncRun{
		jobID:   jobID,
		owner:   owner,
		policy:  cloneDefaultPolicy(preview.policy),
		targets: append([]Account(nil), preview.targets...),
	}
	snapshot := cloneForceJobSnapshot(e.snapshot, true)
	e.wait.Add(1)
	policyLockTransferred = true
	mutationTransferred = true
	e.mu.Unlock()
	go e.run(ctx, run, workers)
	return snapshot, nil
}

func (e *ForceSyncEngine) Snapshot(includeResults bool) ForceSyncJobSnapshot {
	if e == nil {
		return ForceSyncJobSnapshot{State: JobStateIdle}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return cloneForceJobSnapshot(e.snapshot, includeResults)
}

func (e *ForceSyncEngine) Shutdown() {
	if e == nil {
		return
	}
	e.mu.Lock()
	cancel := e.cancel
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	e.wait.Wait()
	e.mu.Lock()
	clear(e.previews)
	e.mu.Unlock()
}

func (e *ForceSyncEngine) run(ctx context.Context, run forceSyncRun, workers int) {
	defer e.wait.Done()
	defer e.mutations.Release(run.owner)
	defer e.policies.operationMu.Unlock()

	jobs := make(chan Account)
	var workerGroup sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		workerGroup.Add(1)
		go func() {
			defer workerGroup.Done()
			for account := range jobs {
				if ctx.Err() != nil {
					e.completeResult(run.jobID, account.ID, JobResult{
						Status: ResultInterrupted, Error: "force-sync job was interrupted before this target was updated", Retryable: false,
					})
					continue
				}
				e.setResultRunning(run.jobID, account.ID)
				result := e.applyAccountSafely(ctx, account, run.policy)
				e.completeResult(run.jobID, account.ID, result)
			}
		}()
	}
	for _, account := range run.targets {
		jobs <- account
	}
	close(jobs)
	workerGroup.Wait()
	e.finish(run.jobID, ctx.Err() != nil)
}

func (e *ForceSyncEngine) applyAccountSafely(ctx context.Context, account Account, policy DefaultPolicy) (result JobResult) {
	defer func() {
		if recover() != nil {
			result = JobResult{Status: ResultFailed, Error: "unexpected force-sync worker failure", Retryable: false}
		}
	}()
	return e.applyAccount(ctx, account, policy)
}

func (e *ForceSyncEngine) applyAccount(ctx context.Context, account Account, policy DefaultPolicy) JobResult {
	if ctx.Err() != nil {
		return JobResult{Status: ResultInterrupted, Error: "force-sync job was interrupted before this target was updated"}
	}
	detail, errGet := e.host.GetAuth(ctx, account.ID)
	if errGet != nil {
		return JobResult{Status: ResultFailed, Error: "physical auth file could not be re-read"}
	}
	raw := bytes.TrimSpace(detail.JSON)
	if len(raw) == 0 || !json.Valid(raw) {
		return JobResult{Status: ResultFailed, Error: "physical auth file is invalid"}
	}
	if detail.AuthIndex != "" && strings.TrimSpace(detail.AuthIndex) != account.ID {
		return JobResult{Status: ResultConflict, Error: "physical auth source changed after preview"}
	}
	if detailPath := normalizedPath(detail.Path); account.path != "" && detailPath != "" && detailPath != account.path {
		return JobResult{Status: ResultConflict, Error: "physical auth source changed after preview"}
	}
	if revisionFor(raw) != account.revision {
		return JobResult{Status: ResultConflict, Error: "physical auth file changed after preview"}
	}
	name := strings.TrimSpace(firstNonEmpty(detail.Name, account.Name))
	if !safeAuthJSONName(name) || account.Name != "" && name != account.Name {
		return JobResult{Status: ResultConflict, Error: "physical auth source changed after preview"}
	}
	updated, applied, changed, errApply := applyDefaultPolicy(raw, policy, applyForce)
	if errApply != nil {
		return JobResult{Status: ResultFailed, Error: "physical auth file is invalid"}
	}
	if !changed {
		return JobResult{Status: ResultSucceeded}
	}
	if _, errSave := e.host.SaveAuth(ctx, name, updated); errSave != nil {
		if ctx.Err() != nil {
			return JobResult{Status: ResultInterrupted, Error: "force-sync job was interrupted during the update"}
		}
		return JobResult{Status: ResultFailed, Error: "default policy update failed"}
	}
	return JobResult{Status: ResultSucceeded, AppliedFields: applied}
}

func (e *ForceSyncEngine) setResultRunning(jobID, accountID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running || e.snapshot.ID != jobID {
		return
	}
	for index := range e.snapshot.Results {
		if e.snapshot.Results[index].ID == accountID && e.snapshot.Results[index].Status == ResultPending {
			e.snapshot.Results[index].Status = ResultRunning
			return
		}
	}
}

func (e *ForceSyncEngine) completeResult(jobID, accountID string, completion JobResult) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running || e.snapshot.ID != jobID {
		return
	}
	for index := range e.snapshot.Results {
		result := &e.snapshot.Results[index]
		if result.ID != accountID || result.Status == ResultSkipped {
			continue
		}
		result.Status = completion.Status
		result.Error = completion.Error
		result.AppliedFields = append([]string(nil), completion.AppliedFields...)
		result.Retryable = false
		break
	}
	e.recountLocked()
}

func (e *ForceSyncEngine) finish(jobID string, interrupted bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.snapshot.ID != jobID {
		return
	}
	e.running = false
	e.cancel = nil
	e.snapshot.Running = false
	e.snapshot.FinishedAt = e.now().UTC()
	e.recountLocked()
	switch {
	case interrupted:
		e.snapshot.State = JobStateInterrupted
	case e.snapshot.Failed > 0 || e.snapshot.Conflicts > 0:
		if e.snapshot.Succeeded > 0 {
			e.snapshot.State = JobStatePartial
		} else {
			e.snapshot.State = JobStateFailed
		}
	case e.snapshot.Skipped > 0:
		e.snapshot.State = JobStatePartial
	default:
		e.snapshot.State = JobStateCompleted
	}
}

func (e *ForceSyncEngine) recountLocked() {
	e.snapshot.Done = 0
	e.snapshot.Succeeded = 0
	e.snapshot.Failed = 0
	e.snapshot.Conflicts = 0
	e.snapshot.Skipped = 0
	for _, result := range e.snapshot.Results {
		switch result.Status {
		case ResultSucceeded:
			e.snapshot.Done++
			e.snapshot.Succeeded++
		case ResultFailed, ResultInterrupted:
			e.snapshot.Done++
			e.snapshot.Failed++
		case ResultConflict:
			e.snapshot.Done++
			e.snapshot.Conflicts++
		case ResultSkipped:
			e.snapshot.Done++
			e.snapshot.Skipped++
		}
	}
}

func (e *ForceSyncEngine) getPreview(id string) (forceSyncPreviewSnapshot, error) {
	if id == "" {
		return forceSyncPreviewSnapshot{}, ErrForcePreviewNotFound
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	preview, exists := e.previews[id]
	if !exists {
		return forceSyncPreviewSnapshot{}, ErrForcePreviewNotFound
	}
	if !e.now().UTC().Before(preview.public.ExpiresAt) {
		delete(e.previews, id)
		return forceSyncPreviewSnapshot{}, ErrForcePreviewExpired
	}
	return cloneForcePreviewSnapshot(preview), nil
}

func (e *ForceSyncEngine) purgePreviewsLocked(now time.Time) {
	for id, preview := range e.previews {
		if !now.Before(preview.public.ExpiresAt) {
			delete(e.previews, id)
		}
	}
}

func (e *ForceSyncEngine) removeOldestPreviewLocked() {
	ids := make([]string, 0, len(e.previews))
	for id := range e.previews {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return e.previews[ids[i]].public.CreatedAt.Before(e.previews[ids[j]].public.CreatedAt)
	})
	if len(ids) > 0 {
		delete(e.previews, ids[0])
	}
}

func forcePolicySummary(policy DefaultPolicy) ForcePolicySummary {
	policy = cloneDefaultPolicy(policy)
	return ForcePolicySummary{
		Fields:     append([]string(nil), policy.Fields()...),
		Priority:   policy.Priority,
		Websockets: policy.Websockets,
	}
}

func managedPolicyEqual(left, right DefaultPolicy) bool {
	return optionalIntEqual(left.Priority, right.Priority) && optionalBoolEqual(left.Websockets, right.Websockets)
}

func optionalIntEqual(left, right *int) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func optionalBoolEqual(left, right *bool) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func cloneForcePreview(preview ForceSyncPreview) ForceSyncPreview {
	clone := preview
	clone.Policy = forcePolicySummary(DefaultPolicy{Priority: preview.Policy.Priority, Websockets: preview.Policy.Websockets})
	clone.Warnings = append([]string(nil), preview.Warnings...)
	clone.Targets = append([]PreviewTarget(nil), preview.Targets...)
	return clone
}

func cloneForcePreviewSnapshot(snapshot forceSyncPreviewSnapshot) forceSyncPreviewSnapshot {
	clone := snapshot
	clone.public = cloneForcePreview(snapshot.public)
	clone.policy = cloneDefaultPolicy(snapshot.policy)
	clone.targets = append([]Account(nil), snapshot.targets...)
	return clone
}

func cloneForceJobSnapshot(snapshot ForceSyncJobSnapshot, includeResults bool) ForceSyncJobSnapshot {
	clone := snapshot
	clone.Policy = forcePolicySummary(DefaultPolicy{Priority: snapshot.Policy.Priority, Websockets: snapshot.Policy.Websockets})
	if !includeResults {
		clone.Results = nil
		return clone
	}
	clone.Results = make([]JobResult, len(snapshot.Results))
	for index, result := range snapshot.Results {
		clone.Results[index] = result
		clone.Results[index].AppliedFields = append([]string(nil), result.AppliedFields...)
	}
	return clone
}

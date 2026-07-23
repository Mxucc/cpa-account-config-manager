package manager

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	JobStateIdle        = "idle"
	JobStateRunning     = "running"
	JobStateCompleted   = "completed"
	JobStatePartial     = "partial"
	JobStateFailed      = "failed"
	JobStateInterrupted = "interrupted"

	ResultPending     = "pending"
	ResultRunning     = "running"
	ResultSucceeded   = "succeeded"
	ResultFailed      = "failed"
	ResultConflict    = "conflict"
	ResultSkipped     = "skipped"
	ResultInterrupted = "interrupted"
)

var (
	ErrJobBusy               = errors.New("a batch job is already running")
	ErrRetryMissing          = errors.New("no failed targets are available to retry")
	ErrNoEligibleJob         = errors.New("preview contains no eligible targets")
	ErrJobStorageUnavailable = errors.New("job result storage is unavailable; configure data_dir to a writable directory")
)

type StartRequest struct {
	PreviewID string `json:"preview_id"`
}

type JobResult struct {
	ID            string   `json:"id"`
	Name          string   `json:"name,omitempty"`
	Provider      string   `json:"provider,omitempty"`
	Label         string   `json:"label,omitempty"`
	Status        string   `json:"status"`
	Error         string   `json:"error,omitempty"`
	AppliedFields []string `json:"applied_fields,omitempty"`
	Retryable     bool     `json:"retryable"`
}

type JobSnapshot struct {
	ID             string       `json:"id,omitempty"`
	ParentJobID    string       `json:"parent_job_id,omitempty"`
	Operation      string       `json:"operation"`
	State          string       `json:"state"`
	Running        bool         `json:"running"`
	Total          int          `json:"total"`
	Eligible       int          `json:"eligible"`
	Done           int          `json:"done"`
	Succeeded      int          `json:"succeeded"`
	Failed         int          `json:"failed"`
	Conflicts      int          `json:"conflicts"`
	Skipped        int          `json:"skipped"`
	Workers        int          `json:"workers"`
	Patch          PatchSummary `json:"patch"`
	StartedAt      time.Time    `json:"started_at,omitempty"`
	FinishedAt     time.Time    `json:"finished_at,omitempty"`
	RetryAvailable bool         `json:"retry_available"`
	Persisted      bool         `json:"persisted"`
	Results        []JobResult  `json:"results,omitempty"`
}

type retryIntent struct {
	parentJobID string
	operation   string
	patch       BatchPatch
	ids         []string
}

type jobRun struct {
	jobID     string
	operation string
	patch     BatchPatch
	targets   []Account
	writer    ManagementWriter
}

type JobEngine struct {
	mu        sync.Mutex
	wait      sync.WaitGroup
	accounts  *AccountService
	mutations *MutationCoordinator
	config    Config
	doer      HTTPDoer
	newWriter func(string, string, HTTPDoer) (ManagementWriter, error)
	now       func() time.Time
	cancel    context.CancelFunc
	running   bool
	snapshot  JobSnapshot
	retry     *retryIntent
	store     string
	loaded    bool
}

func NewJobEngine(accounts *AccountService) *JobEngine {
	return NewJobEngineWithCoordinator(accounts, NewMutationCoordinator())
}

func NewJobEngineWithCoordinator(accounts *AccountService, mutations *MutationCoordinator) *JobEngine {
	if mutations == nil {
		mutations = NewMutationCoordinator()
	}
	engine := &JobEngine{
		accounts:  accounts,
		mutations: mutations,
		config:    normalizeConfig(Config{}),
		newWriter: func(baseURL, key string, doer HTTPDoer) (ManagementWriter, error) {
			return newManagementClient(baseURL, key, doer)
		},
		now:      time.Now,
		snapshot: JobSnapshot{State: JobStateIdle},
	}
	engine.Configure(engine.config)
	return engine
}

func (e *JobEngine) Configure(config Config) {
	if e == nil {
		return
	}
	config = normalizeConfig(config)
	storePath := jobStorePath(config.DataDir)
	e.mu.Lock()
	e.config = config
	if e.running || e.loaded && e.store == storePath {
		e.mu.Unlock()
		return
	}
	e.store = storePath
	e.loaded = true
	snapshot, errLoad := loadJobSnapshot(storePath)
	if errLoad == nil {
		e.snapshot = snapshot
		if e.snapshot.ID != "" && e.snapshot.Operation == "" {
			e.snapshot.Operation = BatchOperationPatch
		}
		e.snapshot.RetryAvailable = false
		e.retry = nil
		if e.snapshot.State == JobStateRunning || e.snapshot.Running {
			e.markLoadedJobInterruptedLocked()
		}
		_ = e.persistLocked()
	} else {
		e.snapshot = JobSnapshot{State: JobStateIdle}
		e.retry = nil
	}
	e.mu.Unlock()
}

func (e *JobEngine) Start(preview previewSnapshot, managementKey, parentJobID string) (JobSnapshot, error) {
	if e == nil || e.accounts == nil {
		return JobSnapshot{}, fmt.Errorf("job engine is unavailable")
	}
	if len(preview.Targets) == 0 {
		return JobSnapshot{}, ErrNoEligibleJob
	}
	operation := preview.Operation
	if operation == "" {
		operation = BatchOperationPatch
	}
	if operation != BatchOperationPatch && operation != BatchOperationDelete {
		return JobSnapshot{}, fmt.Errorf("unsupported batch operation")
	}
	config := e.configSnapshot()
	writerFactory := e.newWriter
	if writerFactory == nil {
		writerFactory = func(baseURL, key string, doer HTTPDoer) (ManagementWriter, error) {
			return newManagementClient(baseURL, key, doer)
		}
	}
	writer, errWriter := writerFactory(resolveManagementBaseURL(config.ManagementBaseURL), managementKey, e.doer)
	if errWriter != nil {
		return JobSnapshot{}, errWriter
	}
	writerTransferred := false
	defer func() {
		if !writerTransferred {
			clearManagementWriterSecrets(writer)
		}
	}()
	jobID, errID := randomIdentifier()
	if errID != nil {
		return JobSnapshot{}, fmt.Errorf("create job id: %w", errID)
	}

	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return JobSnapshot{}, ErrJobBusy
	}
	if !e.mutations.TryAcquire(jobID) {
		e.mu.Unlock()
		return JobSnapshot{}, ErrJobBusy
	}
	mutationAcquired := true
	defer func() {
		if mutationAcquired {
			e.mutations.Release(jobID)
		}
	}()
	workers := config.Workers
	if workers > len(preview.Targets) {
		workers = len(preview.Targets)
	}
	if workers < 1 {
		workers = 1
	}
	results := make([]JobResult, 0, len(preview.Public.Targets))
	for _, target := range preview.Public.Targets {
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
	now := e.now().UTC()
	e.snapshot = JobSnapshot{
		ID:          jobID,
		ParentJobID: strings.TrimSpace(parentJobID),
		Operation:   operation,
		State:       JobStateRunning,
		Running:     true,
		Total:       len(results),
		Eligible:    len(preview.Targets),
		Workers:     workers,
		Patch:       preview.Patch.Summary(),
		StartedAt:   now,
		Results:     results,
	}
	e.recountLocked()
	e.running = true
	e.retry = nil
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	if errPersist := e.persistLocked(); errPersist != nil {
		e.running = false
		e.cancel = nil
		cancel()
		e.snapshot = JobSnapshot{State: JobStateIdle}
		e.mu.Unlock()
		return JobSnapshot{}, ErrJobStorageUnavailable
	}
	run := jobRun{
		jobID:     jobID,
		operation: operation,
		patch:     cloneBatchPatch(preview.Patch),
		targets:   append([]Account(nil), preview.Targets...),
		writer:    writer,
	}
	snapshot := cloneJobSnapshot(e.snapshot, true)
	e.wait.Add(1)
	writerTransferred = true
	mutationAcquired = false
	e.mu.Unlock()
	go e.run(ctx, run, workers)
	return snapshot, nil
}

func (e *JobEngine) Snapshot(includeResults bool) JobSnapshot {
	if e == nil {
		return JobSnapshot{State: JobStateIdle}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return cloneJobSnapshot(e.snapshot, includeResults)
}

func (e *JobEngine) RetryIntent() (TargetScope, BatchPatch, string, string, error) {
	if e == nil {
		return TargetScope{}, BatchPatch{}, "", "", ErrRetryMissing
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running {
		return TargetScope{}, BatchPatch{}, "", "", ErrJobBusy
	}
	if e.retry == nil || len(e.retry.ids) == 0 || e.retry.operation != BatchOperationDelete && e.retry.patch.Empty() {
		return TargetScope{}, BatchPatch{}, "", "", ErrRetryMissing
	}
	return TargetScope{Mode: "selected", IDs: append([]string(nil), e.retry.ids...)},
		cloneBatchPatch(e.retry.patch), e.retry.operation, e.retry.parentJobID, nil
}

func (e *JobEngine) Shutdown() {
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
	e.retry = nil
	e.snapshot.RetryAvailable = false
	if e.snapshot.ID != "" {
		_ = e.persistLocked()
	}
	e.mu.Unlock()
}

func (e *JobEngine) configSnapshot() Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.config
}

func (e *JobEngine) run(ctx context.Context, run jobRun, workers int) {
	defer e.wait.Done()
	defer clearManagementWriterSecrets(run.writer)
	defer e.mutations.Release(run.jobID)
	jobs := make(chan Account)
	var workerGroup sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		workerGroup.Add(1)
		go func() {
			defer workerGroup.Done()
			for account := range jobs {
				if ctx.Err() != nil {
					e.completeResult(run.jobID, account.ID, JobResult{
						Status:    ResultInterrupted,
						Error:     "job was interrupted before this target was updated",
						Retryable: true,
					})
					continue
				}
				e.setResultRunning(run.jobID, account.ID)
				result := e.applyAccountSafely(ctx, account, run.operation, run.patch, run.writer)
				e.completeResult(run.jobID, account.ID, result)
			}
		}()
	}
	for _, account := range run.targets {
		jobs <- account
	}
	close(jobs)
	workerGroup.Wait()
	e.finish(run, ctx.Err() != nil)
}

func clearManagementWriterSecrets(writer ManagementWriter) {
	if cleaner, ok := writer.(interface{ clearSecrets() }); ok {
		cleaner.clearSecrets()
	}
}

func (e *JobEngine) applyAccountSafely(ctx context.Context, account Account, operation string, patch BatchPatch, writer ManagementWriter) (result JobResult) {
	defer func() {
		if recover() != nil {
			result = JobResult{
				Status:    ResultFailed,
				Error:     "unexpected worker failure",
				Retryable: true,
			}
		}
	}()
	return e.applyAccount(ctx, account, operation, patch, writer)
}

func (e *JobEngine) applyAccount(ctx context.Context, account Account, operation string, patch BatchPatch, writer ManagementWriter) JobResult {
	if ctx.Err() != nil {
		return JobResult{Status: ResultInterrupted, Error: "job was interrupted before this target was updated", Retryable: true}
	}
	revision, errRevision := e.accounts.CurrentRevision(ctx, account)
	if errRevision != nil {
		return JobResult{Status: ResultFailed, Error: "physical auth file could not be re-read", Retryable: true}
	}
	if revision != account.revision {
		return JobResult{Status: ResultConflict, Error: "physical auth file changed after preview", Retryable: true}
	}
	if operation == BatchOperationDelete {
		if errDelete := writer.DeleteAuthFile(ctx, account.Name); errDelete != nil {
			if ctx.Err() != nil {
				return JobResult{Status: ResultInterrupted, Error: "job was interrupted during account deletion", Retryable: true}
			}
			return JobResult{Status: ResultFailed, Error: "account deletion failed", Retryable: true}
		}
		return JobResult{Status: ResultSucceeded}
	}

	applied := make([]string, 0, len(patch.Summary().Fields))
	if patch.HasFieldUpdates() {
		if errFields := writer.PatchFields(ctx, account.Name, patch); errFields != nil {
			if ctx.Err() != nil {
				return JobResult{Status: ResultInterrupted, Error: "job was interrupted during the update", Retryable: true}
			}
			return JobResult{Status: ResultFailed, Error: "account field update failed", Retryable: true}
		}
		for _, field := range patch.Summary().Fields {
			if field != "disabled" {
				applied = append(applied, field)
			}
		}
	}
	if patch.Disabled != nil {
		if errDisabled := writer.PatchDisabled(ctx, account.Name, *patch.Disabled); errDisabled != nil {
			if ctx.Err() != nil {
				return JobResult{Status: ResultInterrupted, Error: "job was interrupted during the update", AppliedFields: applied, Retryable: true}
			}
			message := "account disabled-state update failed"
			if len(applied) > 0 {
				message = "account disabled-state update failed after other fields were applied"
			}
			return JobResult{Status: ResultFailed, Error: message, AppliedFields: applied, Retryable: true}
		}
		applied = append(applied, "disabled")
	}
	return JobResult{Status: ResultSucceeded, AppliedFields: applied}
}

func (e *JobEngine) setResultRunning(jobID, accountID string) {
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

func (e *JobEngine) completeResult(jobID, accountID string, completion JobResult) {
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
		result.Retryable = completion.Retryable
		break
	}
	e.recountLocked()
}

func (e *JobEngine) finish(run jobRun, interrupted bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.snapshot.ID != run.jobID {
		return
	}
	// Release the shared write slot before publishing a terminal snapshot so an
	// immediate failed-only retry cannot observe an idle job with a busy slot.
	e.mutations.Release(run.jobID)
	e.running = false
	e.cancel = nil
	e.snapshot.Running = false
	e.snapshot.FinishedAt = e.now().UTC()
	e.recountLocked()
	if interrupted {
		e.snapshot.State = JobStateInterrupted
	} else {
		switch {
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

	retryIDs := make([]string, 0)
	for _, result := range e.snapshot.Results {
		if result.Retryable && result.ID != "" {
			retryIDs = append(retryIDs, result.ID)
		}
	}
	sort.Strings(retryIDs)
	if len(retryIDs) > 0 {
		e.retry = &retryIntent{
			parentJobID: run.jobID,
			operation:   run.operation,
			patch:       cloneBatchPatch(run.patch),
			ids:         retryIDs,
		}
		e.snapshot.RetryAvailable = true
	} else {
		e.retry = nil
		e.snapshot.RetryAvailable = false
	}
	_ = e.persistLocked()
}

func (e *JobEngine) recountLocked() {
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

func (e *JobEngine) markLoadedJobInterruptedLocked() {
	e.snapshot.State = JobStateInterrupted
	e.snapshot.Running = false
	e.snapshot.FinishedAt = e.now().UTC()
	e.snapshot.RetryAvailable = false
	for index := range e.snapshot.Results {
		result := &e.snapshot.Results[index]
		if result.Status != ResultPending && result.Status != ResultRunning {
			continue
		}
		result.Status = ResultInterrupted
		result.Error = "plugin stopped before this target completed"
		result.Retryable = false
	}
	e.recountLocked()
}

func (e *JobEngine) persistLocked() error {
	if e.store == "" {
		return fmt.Errorf("job store path is unavailable")
	}
	snapshot := cloneJobSnapshot(e.snapshot, true)
	snapshot.Persisted = true
	if errSave := saveJobSnapshot(e.store, snapshot); errSave != nil {
		e.snapshot.Persisted = false
		return errSave
	}
	e.snapshot.Persisted = true
	return nil
}

func cloneJobSnapshot(snapshot JobSnapshot, includeResults bool) JobSnapshot {
	clone := snapshot
	clone.Patch.Fields = append([]string(nil), snapshot.Patch.Fields...)
	clone.Patch.HeaderSet = append([]string(nil), snapshot.Patch.HeaderSet...)
	clone.Patch.HeaderRemove = append([]string(nil), snapshot.Patch.HeaderRemove...)
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

func statusWantsResults(query map[string][]string) bool {
	for _, key := range []string{"include_results", "light"} {
		value := strings.ToLower(strings.TrimSpace(firstQuery(query, key)))
		if key == "include_results" && (value == "0" || value == "false" || value == "no") {
			return false
		}
		if key == "light" && (value == "1" || value == "true" || value == "yes") {
			return false
		}
	}
	return true
}

func jobHTTPStatus(err error) int {
	switch {
	case errors.Is(err, ErrJobBusy):
		return http.StatusConflict
	case errors.Is(err, ErrRetryMissing), errors.Is(err, ErrNoEligibleJob):
		return http.StatusBadRequest
	case errors.Is(err, ErrJobStorageUnavailable), errors.Is(err, ErrManagementBaseURLInvalid):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

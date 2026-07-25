package manager

import (
	"context"
	"strings"
	"sync"
	"time"
)

const maxAutomaticDisableProbeAttempts = 10

type automaticDisableProbeTask struct {
	id      string
	account Account
	record  inspectionRecord
	plan    AutomaticDisableProbePlan
}

type automaticDisableProbeOutcome struct {
	id     string
	result InspectionResult
}

func (e *InspectionEngine) applyAutomaticDisableProbeGates(
	ctx context.Context,
	policy InspectionPolicy,
	accounts map[string]Account,
	records map[string]inspectionRecord,
	managementBaseURL string,
	managementKey string,
) {
	if e == nil || !policy.AutoDisable || len(records) == 0 {
		return
	}
	e.mu.RLock()
	guards := append([]AutomaticDisableGuard(nil), e.autoDisableGuards...)
	runner := e.automaticDisableProbe
	e.mu.RUnlock()

	tasks := make([]automaticDisableProbeTask, 0)
	for id, record := range records {
		account, exists := accounts[id]
		if !exists || !shouldAutoDisableInspection(policy, account, record) {
			continue
		}
		preferredModel := inspectionProbeModel(account, policy.ModelProbeModels)
		plan, planned := automaticDisableProbePlanFor(guards, account, record.Result, preferredModel)
		if !planned {
			clearAutomaticDisableProbeState(&record.Result)
			records[id] = record
			continue
		}
		tasks = append(tasks, automaticDisableProbeTask{id: id, account: account, record: record, plan: plan})
	}
	if len(tasks) == 0 {
		return
	}

	jobs := make(chan automaticDisableProbeTask)
	outcomes := make(chan automaticDisableProbeOutcome, len(tasks))
	workers := min(inspectionProbeWorkers, len(tasks))
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for task := range jobs {
				result := executeAutomaticDisableProbePlan(ctx, runner, task.account, task.record.Result, task.plan, managementBaseURL, managementKey, e.currentTime)
				select {
				case outcomes <- automaticDisableProbeOutcome{id: task.id, result: result}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, task := range tasks {
			select {
			case jobs <- task:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wait.Wait()
		close(outcomes)
	}()
	for outcome := range outcomes {
		record := records[outcome.id]
		record.Result = outcome.result
		records[outcome.id] = record
	}
}

func automaticDisableProbePlanFor(guards []AutomaticDisableGuard, account Account, result InspectionResult, preferredModel string) (AutomaticDisableProbePlan, bool) {
	for _, guard := range guards {
		planner, ok := guard.(AutomaticDisableProbePlanner)
		if !ok || planner == nil {
			continue
		}
		plan, planned := planner.AutomaticDisableProbePlan(account, result, preferredModel)
		if !planned {
			continue
		}
		plan.Name = safeOperationIdentifier(plan.Name, 64)
		plan.AttemptLimit = boundedAutoDisableProbeCount(plan.AttemptLimit)
		plan.Models = safeAutomaticDisableProbeModels(plan.Models)
		if plan.Name == "" || plan.AttemptLimit == 0 || len(plan.Models) == 0 {
			continue
		}
		return plan, true
	}
	return AutomaticDisableProbePlan{}, false
}

func executeAutomaticDisableProbePlan(
	ctx context.Context,
	runner automaticDisableProbeRunner,
	account Account,
	result InspectionResult,
	plan AutomaticDisableProbePlan,
	managementBaseURL string,
	managementKey string,
	now func() time.Time,
) InspectionResult {
	result.AutoDisableProbeName = plan.Name
	result.AutoDisableProbeStatus = InspectionAutoDisableProbePending
	result.AutoDisableProbeAttempts = 0
	result.AutoDisableProbeLimit = plan.AttemptLimit
	result.AutoDisableProbeReasonCode = ""
	result.AutoDisableProbeModel = ""
	result.AutoDisableProbeTestedAt = nil
	if runner == nil {
		result.AutoDisableProbeStatus = InspectionAutoDisableProbeInconclusive
		result.AutoDisableProbeReasonCode = "upstream_unavailable"
		return result
	}
	for attempt := 0; attempt < plan.AttemptLimit; attempt++ {
		if ctx.Err() != nil {
			result.AutoDisableProbeStatus = InspectionAutoDisableProbeInconclusive
			result.AutoDisableProbeReasonCode = "request_timeout"
			return result
		}
		request := plan.Request
		request.AccountID = account.ID
		request.Model = plan.Models[attempt%len(plan.Models)]
		probe, errRun := runner(ctx, request, managementBaseURL, managementKey)
		if errRun != nil || probe.Experiment == nil || !probe.Experiment.Applied || probe.Experiment.Name != plan.Name {
			result.AutoDisableProbeStatus = InspectionAutoDisableProbeInconclusive
			result.AutoDisableProbeReasonCode = "upstream_unavailable"
			return result
		}
		result.AutoDisableProbeAttempts++
		result.AutoDisableProbeReasonCode = safeOptionalInspectionReason(probe.ReasonCode)
		result.AutoDisableProbeModel = safeModelIdentifier(probe.Model)
		testedAt := probe.TestedAt.UTC()
		if testedAt.IsZero() && now != nil {
			testedAt = now().UTC()
		}
		result.AutoDisableProbeTestedAt = timePointer(testedAt)
		if probe.Status == "available" {
			result.AutoDisableProbeStatus = InspectionAutoDisableProbePassed
			return result
		}
	}
	result.AutoDisableProbeStatus = InspectionAutoDisableProbeFailed
	return result
}

func safeAutomaticDisableProbeModels(models []string) []string {
	out := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = safeModelIdentifier(strings.TrimSpace(model))
		if model == "" {
			continue
		}
		key := strings.ToLower(model)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, model)
	}
	return out
}

func boundedAutoDisableProbeCount(value int) int {
	if value < 0 {
		return 0
	}
	return min(value, maxAutomaticDisableProbeAttempts)
}

func normalizeInspectionAutoDisableProbeStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case InspectionAutoDisableProbePending, InspectionAutoDisableProbePassed, InspectionAutoDisableProbeFailed, InspectionAutoDisableProbeInconclusive:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func clearAutomaticDisableProbeState(result *InspectionResult) {
	if result == nil {
		return
	}
	result.AutoDisableProbeName = ""
	result.AutoDisableProbeStatus = ""
	result.AutoDisableProbeAttempts = 0
	result.AutoDisableProbeLimit = 0
	result.AutoDisableProbeReasonCode = ""
	result.AutoDisableProbeModel = ""
	result.AutoDisableProbeTestedAt = nil
}

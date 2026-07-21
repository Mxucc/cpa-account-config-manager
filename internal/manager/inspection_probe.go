package manager

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

const inspectionProbeWorkers = 4

func inspectionRunDue(now, lastRun time.Time, intervalMinutes int) bool {
	if lastRun.IsZero() {
		return true
	}
	return !now.Before(lastRun.Add(time.Duration(intervalMinutes) * time.Minute))
}

func runInspectionModelProbes(
	ctx context.Context,
	service *ModelTestService,
	accounts []Account,
	records map[string]inspectionRecord,
	policy InspectionPolicy,
	cursor int,
	managementBaseURL string,
	managementKey string,
) ([]ModelTestResult, int) {
	return runInspectionModelProbesObserved(ctx, service, accounts, records, policy, cursor, managementBaseURL, managementKey, nil)
}

func runInspectionModelProbesObserved(
	ctx context.Context,
	service *ModelTestService,
	accounts []Account,
	records map[string]inspectionRecord,
	policy InspectionPolicy,
	cursor int,
	managementBaseURL string,
	managementKey string,
	observe func(ModelTestResult),
) ([]ModelTestResult, int) {
	if service == nil || len(accounts) == 0 || strings.TrimSpace(managementKey) == "" {
		return nil, cursor
	}
	eligible := inspectionProbeEligibleAccounts(accounts, records, policy.ScanManuallyDisabled)
	if len(eligible) == 0 {
		return nil, 0
	}
	sortInspectionProbeAccounts(eligible, records)
	if cursor < 0 || cursor >= len(eligible) {
		cursor = 0
	}
	batchSize := policy.ModelProbeBatchSize
	if batchSize > len(eligible) {
		batchSize = len(eligible)
	}
	selected := make([]Account, 0, batchSize)
	for offset := 0; offset < batchSize; offset++ {
		selected = append(selected, eligible[(cursor+offset)%len(eligible)])
	}
	nextCursor := (cursor + batchSize) % len(eligible)

	jobs := make(chan Account)
	results := make(chan ModelTestResult, len(selected))
	var wait sync.WaitGroup
	workers := inspectionProbeWorkers
	if len(selected) < workers {
		workers = len(selected)
	}
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for account := range jobs {
				model := inspectionProbeModel(account, policy.ModelProbeModels)
				result, errRun := service.Run(ctx, ModelTestRequest{AccountID: account.ID, Model: model}, managementBaseURL, managementKey)
				if errRun != nil {
					result = ModelTestResult{
						AccountID: account.ID, Provider: inspectionProbeProvider(account), Model: model,
						Status: "review", ProbeKind: InspectionProbeKindModel,
						ReasonCode: "upstream_unavailable", TestedAt: service.currentTime(),
					}
				}
				select {
				case results <- result:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, account := range selected {
			select {
			case jobs <- account:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wait.Wait()
		close(results)
	}()
	out := make([]ModelTestResult, 0, len(selected))
	for result := range results {
		out = append(out, result)
		if observe != nil {
			observe(result)
		}
	}
	sort.Slice(out, func(left, right int) bool { return out[left].AccountID < out[right].AccountID })
	return out, nextCursor
}

func inspectionProbeEligibleAccounts(accounts []Account, records map[string]inspectionRecord, scanManuallyDisabled bool) []Account {
	eligible := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		id := strings.TrimSpace(account.ID)
		if id == "" {
			continue
		}
		if account.Disabled && !records[id].Result.OwnedDisable && !scanManuallyDisabled {
			continue
		}
		eligible = append(eligible, account)
	}
	return eligible
}

func sortInspectionProbeAccounts(accounts []Account, records map[string]inspectionRecord) {
	sort.SliceStable(accounts, func(left, right int) bool {
		leftPriority := inspectionProbePriority(accounts[left], records[accounts[left].ID])
		rightPriority := inspectionProbePriority(accounts[right], records[accounts[right].ID])
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		return accounts[left].ID < accounts[right].ID
	})
}

func inspectionProbePriority(account Account, record inspectionRecord) int {
	if !account.Disabled && (account.Unavailable || record.Result.Health == InspectionHealthUnavailable || record.Result.Recommendation == InspectionRecommendationDisable) {
		return 0
	}
	if account.Disabled && record.Result.OwnedDisable {
		return 1
	}
	return 2
}

func inspectionProbeEligibleAccountIDs(accounts []Account, records map[string]inspectionRecord, scanManuallyDisabled bool) []string {
	eligible := inspectionProbeEligibleAccounts(accounts, records, scanManuallyDisabled)
	sortInspectionProbeAccounts(eligible, records)
	ids := make([]string, 0, len(eligible))
	for _, account := range eligible {
		ids = append(ids, account.ID)
	}
	return ids
}

func inspectionProbeAccountsForTargets(accounts []Account, targets []string) []Account {
	byID := make(map[string]Account, len(accounts))
	for _, account := range accounts {
		byID[account.ID] = account
	}
	out := make([]Account, 0, len(targets))
	for _, id := range targets {
		if account, exists := byID[id]; exists {
			out = append(out, account)
		}
	}
	return out
}

func inspectionRunTargetIDs(mode string, accounts []Account, records map[string]inspectionRecord, scanManuallyDisabled bool) []string {
	eligible := inspectionProbeEligibleAccountIDs(accounts, records, scanManuallyDisabled)
	if mode != InspectionRunModeIncremental {
		return eligible
	}
	out := make([]string, 0, len(eligible))
	for _, id := range eligible {
		if record, exists := records[id]; !exists || record.Result.LastCheckedAt.IsZero() {
			out = append(out, id)
		}
	}
	return out
}

func inspectionRunScansManuallyDisabled(mode, source string, configured bool) bool {
	return configured || (normalizeInspectionRunMode(mode) == InspectionRunModeFull && normalizeInspectionSweepSource(source) == InspectionSweepSourceManual)
}

func retryInspectionProbeResults(ctx context.Context, service *ModelTestService, accounts []Account, results []ModelTestResult, policy InspectionPolicy, managementBaseURL, managementKey string) ([]ModelTestResult, int) {
	return retryInspectionProbeResultsObserved(ctx, service, accounts, results, policy, managementBaseURL, managementKey, nil)
}

func retryInspectionProbeResultsObserved(ctx context.Context, service *ModelTestService, accounts []Account, results []ModelTestResult, policy InspectionPolicy, managementBaseURL, managementKey string, observe func(ModelTestResult)) ([]ModelTestResult, int) {
	if service == nil || strings.TrimSpace(managementKey) == "" {
		return nil, 0
	}
	byID := make(map[string]Account, len(accounts))
	for _, account := range accounts {
		byID[account.ID] = account
	}
	retry := make([]ModelTestResult, 0)
	completed := 0
	for _, result := range results {
		if result.ReasonCode != "request_timeout" && result.ReasonCode != "upstream_unavailable" && result.ReasonCode != "invalid_response" {
			continue
		}
		account, exists := byID[result.AccountID]
		if !exists || ctx.Err() != nil {
			continue
		}
		model := inspectionProbeModel(account, policy.ModelProbeModels)
		completed++
		next, errRun := service.Run(ctx, ModelTestRequest{AccountID: account.ID, Model: model}, managementBaseURL, managementKey)
		if errRun != nil {
			next = ModelTestResult{
				AccountID: account.ID, Provider: inspectionProbeProvider(account), Model: model,
				Status: "review", ProbeKind: InspectionProbeKindModel,
				ReasonCode: "upstream_unavailable", TestedAt: service.currentTime(),
			}
		}
		if observe != nil {
			observe(next)
		}
		if errRun != nil {
			continue
		}
		retry = append(retry, next)
	}
	return retry, completed
}

func inspectionProbeRetryCount(results []ModelTestResult) int {
	count := 0
	for _, result := range results {
		if result.ReasonCode == "request_timeout" || result.ReasonCode == "upstream_unavailable" || result.ReasonCode == "invalid_response" {
			count++
		}
	}
	return count
}

func mergeInspectionProbeResults(primary, retry []ModelTestResult) []ModelTestResult {
	byID := make(map[string]ModelTestResult, len(primary)+len(retry))
	for _, result := range primary {
		byID[result.AccountID] = result
	}
	for _, result := range retry {
		byID[result.AccountID] = result
	}
	out := make([]ModelTestResult, 0, len(byID))
	for _, result := range byID {
		out = append(out, result)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out
}

func inspectionProbeProvider(account Account) string {
	provider := strings.ToLower(strings.TrimSpace(firstNonEmpty(account.Provider, account.Type)))
	switch provider {
	case "gemini-cli", "gemini-interactions", "aistudio":
		return "gemini"
	case "grok":
		return "xai"
	default:
		return provider
	}
}

func inspectionProbeModel(account Account, models ModelProbeModels) string {
	switch inspectionProbeProvider(account) {
	case "codex":
		return models.Codex
	case "openai":
		return models.OpenAI
	case "claude", "anthropic":
		return models.Claude
	case "gemini":
		return models.Gemini
	case "xai":
		return models.XAI
	default:
		return ""
	}
}

func applyModelProbeToInspection(record *inspectionRecord, result ModelTestResult, policy InspectionPolicy) {
	if record == nil || strings.TrimSpace(result.AccountID) == "" {
		return
	}
	previous := record.Probe
	next := inspectionProbeSignal{
		Status: normalizeModelProbeStatus(result.Status), Kind: normalizeInspectionProbeKind(result.ProbeKind),
		ReasonCode: safeModelProbeReason(result.ReasonCode), StatusCode: boundedHTTPStatus(result.StatusCode),
		Model: safeModelIdentifier(result.Model), TestedAt: result.TestedAt.UTC(), LatencyMS: maxInt64(0, result.LatencyMS),
	}
	window := time.Duration(normalizeInspectionPolicy(policy).PassiveFailureWindowMinutes) * time.Minute
	if next.ReasonCode == "model_response_ok" || next.Status == "available" {
		next.ConsecutiveSuccess = boundedCounter(previous.ConsecutiveSuccess + 1)
	} else if previous.TestedAt.IsZero() || next.TestedAt.Before(previous.TestedAt) || next.TestedAt.Sub(previous.TestedAt) > window ||
		previous.ReasonCode == "model_response_ok" || previous.ReasonCode != next.ReasonCode {
		next.ConsecutiveFailures = 1
	} else {
		next.ConsecutiveFailures = boundedCounter(previous.ConsecutiveFailures + 1)
	}
	record.Probe = next
	record.Result.ProbeStatus = record.Probe.Status
	record.Result.ProbeKind = record.Probe.Kind
	record.Result.ProbeReasonCode = record.Probe.ReasonCode
	record.Result.ProbeModel = record.Probe.Model
	record.Result.ProbeTestedAt = cloneTimePointer(timePointerOrNil(record.Probe.TestedAt))
	record.Result.ProbeLatencyMS = record.Probe.LatencyMS
}

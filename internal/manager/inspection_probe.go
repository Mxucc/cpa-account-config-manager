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
	if service == nil || len(accounts) == 0 || strings.TrimSpace(managementKey) == "" {
		return nil, cursor
	}
	eligible := inspectionProbeEligibleAccounts(accounts, records, policy.ScanManuallyDisabled)
	if len(eligible) == 0 {
		return nil, 0
	}
	sort.SliceStable(eligible, func(left, right int) bool { return eligible[left].ID < eligible[right].ID })
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
						Status: "review", ReasonCode: "upstream_unavailable", TestedAt: service.currentTime(),
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
	wait.Wait()
	close(results)
	out := make([]ModelTestResult, 0, len(selected))
	for result := range results {
		out = append(out, result)
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

func applyModelProbeToInspection(record *inspectionRecord, result ModelTestResult) {
	if record == nil || strings.TrimSpace(result.AccountID) == "" {
		return
	}
	record.Probe = inspectionProbeSignal{
		Status: normalizeModelProbeStatus(result.Status), ReasonCode: safeModelProbeReason(result.ReasonCode),
		Model: safeModelIdentifier(result.Model), TestedAt: result.TestedAt.UTC(), LatencyMS: maxInt64(0, result.LatencyMS),
	}
	record.Result.ProbeStatus = record.Probe.Status
	record.Result.ProbeReasonCode = record.Probe.ReasonCode
	record.Result.ProbeModel = record.Probe.Model
	record.Result.ProbeTestedAt = cloneTimePointer(timePointerOrNil(record.Probe.TestedAt))
	record.Result.ProbeLatencyMS = record.Probe.LatencyMS
}

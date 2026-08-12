package manager

import (
	"cpa-account-config-manager/internal/cpaapi"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	usageStoreVersion         = 6
	baselineUsageStoreVersion = 3
	counterUsageStoreVersion  = 2
	legacyUsageStoreVersion   = 1
	usageStoreLockTimeout     = 2 * time.Second
	usageStoreLockStale       = 30 * time.Second
	usageStoreLockRetry       = 10 * time.Millisecond
	usageDurableDirName       = ".cpa-account-config-manager"
	usageDurableFileName      = "usage-snapshots.state"
)

type persistedUsageState struct {
	Version  int                       `json:"version"`
	Accounts map[string]usageAggregate `json:"accounts"`
}

func usageStorePath(dataDir string) string {
	return filepath.Join(dataDir, "usage-snapshots.json")
}

func durableUsageStorePath(authDir string) string {
	return filepath.Join(authDir, usageDurableDirName, usageDurableFileName)
}

func discoverUsageAuthDir(entries []cpaapi.HostAuthFileEntry) string {
	authDir := ""
	for _, entry := range entries {
		path := strings.TrimSpace(entry.Path)
		name := strings.TrimSpace(entry.Name)
		if entry.RuntimeOnly || !strings.EqualFold(strings.TrimSpace(entry.Source), "file") ||
			!filepath.IsAbs(path) || !safeAuthJSONName(name) || !strings.EqualFold(filepath.Base(path), name) {
			continue
		}
		info, errStat := os.Stat(path)
		if errStat != nil || !info.Mode().IsRegular() {
			continue
		}
		candidate, errResolve := filepath.EvalSymlinks(filepath.Dir(path))
		if errResolve != nil {
			continue
		}
		candidate = filepath.Clean(candidate)
		directoryInfo, errDirectory := os.Stat(candidate)
		if errDirectory != nil || !directoryInfo.IsDir() {
			continue
		}
		if authDir == "" {
			authDir = candidate
			continue
		}
		if !sameFilePath(authDir, candidate) {
			return ""
		}
	}
	return authDir
}

func sameFilePath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func usageStoreBackupPath(path string) string {
	return path + ".bak"
}

func usageStoreLockPath(path string) string {
	return path + ".lock"
}

func loadUsageState(path string) (map[string]usageAggregate, error) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return nil, errRead
	}
	var persisted persistedUsageState
	if errDecode := json.Unmarshal(raw, &persisted); errDecode != nil {
		return nil, fmt.Errorf("decode usage state: %w", errDecode)
	}
	if persisted.Version == legacyUsageStoreVersion {
		migrated := make(map[string]usageAggregate, len(persisted.Accounts))
		for authIndex, aggregate := range persisted.Accounts {
			if key := usagePendingKey(authIndex); key != "" {
				aggregate = migrateUsageAggregateToBaselineState(aggregate)
				migrated[key] = aggregate
			}
		}
		return normalizeUsageAccounts(migrated), nil
	}
	if persisted.Version == counterUsageStoreVersion {
		migrated := make(map[string]usageAggregate, len(persisted.Accounts))
		for storageKey, aggregate := range persisted.Accounts {
			migrated[storageKey] = migrateUsageAggregateToBaselineState(aggregate)
		}
		return normalizeUsageAccounts(migrated), nil
	}
	if persisted.Version == baselineUsageStoreVersion {
		return normalizeUsageAccounts(persisted.Accounts), nil
	}
	if persisted.Version == 4 {
		return normalizeUsageAccounts(persisted.Accounts), nil
	}
	if persisted.Version == 5 {
		return normalizeUsageAccounts(migrateVersionFiveCreditBaselines(persisted.Accounts)), nil
	}
	if persisted.Version != usageStoreVersion {
		return nil, fmt.Errorf("unsupported usage store version %d", persisted.Version)
	}
	return normalizeUsageAccounts(persisted.Accounts), nil
}

func migrateVersionFiveCreditBaselines(accounts map[string]usageAggregate) map[string]usageAggregate {
	migrated := make(map[string]usageAggregate, len(accounts))
	for storageKey, aggregate := range accounts {
		for _, cycle := range []*overdraftCycleState{aggregate.FiveHourOverdraft, aggregate.SevenDayOverdraft} {
			if cycle == nil || !cycle.Active {
				continue
			}
			// Version 5 persisted total credit usage but had no per-cycle credit
			// baseline. Freeze the loaded totals so an upgrade cannot relabel all
			// historical account cost as overdraft cost.
			cycle.BaselineCreditAmountNanos = aggregate.CreditAmountNanos
			cycle.BaselineCreditRated = aggregate.CreditRatedRequests
			cycle.BaselineCreditUnrated = aggregate.CreditUnratedRequests
		}
		migrated[storageKey] = aggregate
	}
	return migrated
}

func migrateUsageAggregateToBaselineState(aggregate usageAggregate) usageAggregate {
	aggregate.SuccessfulTokens = maxInt64(aggregate.SuccessfulTokens, aggregate.TotalTokens)
	aggregate.FiveHourOverdraft = nil
	aggregate.SevenDayOverdraft = nil
	if aggregate.Codex != nil {
		for _, window := range []*UsageWindowSnapshot{aggregate.Codex.FiveHour, aggregate.Codex.SevenDay} {
			clearPublicOverdraftState(window)
		}
	}
	return aggregate
}

func loadUsageStateWithBackup(path string) (map[string]usageAggregate, bool, error) {
	accounts, errPrimary := loadUsageState(path)
	if errPrimary == nil {
		return accounts, false, nil
	}
	backup, errBackup := loadUsageState(usageStoreBackupPath(path))
	if errBackup == nil {
		return backup, true, nil
	}
	if errors.Is(errPrimary, os.ErrNotExist) && !errors.Is(errBackup, os.ErrNotExist) {
		return nil, false, errBackup
	}
	return nil, false, errPrimary
}

func normalizeUsageAccounts(values map[string]usageAggregate) map[string]usageAggregate {
	type entry struct {
		authIndex string
		aggregate usageAggregate
	}
	entries := make([]entry, 0, len(values))
	for storageKey, aggregate := range values {
		storageKey = strings.TrimSpace(storageKey)
		if !validUsageStorageKey(storageKey) {
			continue
		}
		aggregate = sanitizeUsageAggregate(aggregate)
		entries = append(entries, entry{authIndex: storageKey, aggregate: aggregate})
	}
	sort.Slice(entries, func(i, j int) bool {
		left := entries[i].aggregate.UpdatedAt
		right := entries[j].aggregate.UpdatedAt
		if left.Equal(right) {
			return entries[i].authIndex < entries[j].authIndex
		}
		return left.After(right)
	})
	if len(entries) > maxUsageAccounts {
		entries = entries[:maxUsageAccounts]
	}
	accounts := make(map[string]usageAggregate, len(entries))
	for _, item := range entries {
		accounts[item.authIndex] = item.aggregate
	}
	return accounts
}

func saveUsageState(path string, accounts map[string]usageAggregate) error {
	state := persistedUsageState{
		Version:  usageStoreVersion,
		Accounts: normalizeUsageAccounts(accounts),
	}
	if errSave := savePrivateJSON(path, state); errSave != nil {
		return errSave
	}
	if errBackup := savePrivateJSON(usageStoreBackupPath(path), state); errBackup != nil {
		return fmt.Errorf("save usage backup: %w", errBackup)
	}
	return nil
}

func persistUsageState(path string, accounts map[string]usageAggregate) (map[string]usageAggregate, error) {
	release, errLock := acquireUsageStoreLock(path)
	if errLock != nil {
		return nil, errLock
	}
	defer release()
	merged := normalizeUsageAccounts(accounts)
	stored, _, errLoad := loadUsageStateWithBackup(path)
	if errLoad == nil {
		merged = mergeUsageAggregates(merged, stored)
	} else if !errors.Is(errLoad, os.ErrNotExist) {
		return nil, errLoad
	}
	if errSave := saveUsageState(path, merged); errSave != nil {
		return nil, errSave
	}
	return merged, nil
}

func acquireUsageStoreLock(path string) (func(), error) {
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		return nil, fmt.Errorf("create usage data directory: %w", errMkdir)
	}
	lockPath := usageStoreLockPath(path)
	deadline := time.Now().Add(usageStoreLockTimeout)
	for {
		file, errOpen := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errOpen == nil {
			if errClose := file.Close(); errClose != nil {
				_ = os.Remove(lockPath)
				return nil, fmt.Errorf("close usage storage lock: %w", errClose)
			}
			return func() { _ = os.Remove(lockPath) }, nil
		}
		if !errors.Is(errOpen, os.ErrExist) {
			return nil, fmt.Errorf("acquire usage storage lock: %w", errOpen)
		}
		if info, errStat := os.Stat(lockPath); errStat == nil && time.Since(info.ModTime()) > usageStoreLockStale {
			_ = os.Remove(lockPath)
			continue
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("acquire usage storage lock: timed out")
		}
		time.Sleep(usageStoreLockRetry)
	}
}

func mergeUsageAggregates(current, stored map[string]usageAggregate) map[string]usageAggregate {
	merged := cloneUsageAggregates(stored)
	for authIndex, aggregate := range current {
		merged[authIndex] = mergeUsageAggregate(aggregate, merged[authIndex])
	}
	return normalizeUsageAccounts(merged)
}

func mergeUsageAggregate(current, stored usageAggregate) usageAggregate {
	if usageIdentitiesConflict(current.Identity, stored.Identity) {
		return sanitizeUsageAggregate(current)
	}
	current.Identity = mergeUsageIdentity(current.Identity, stored.Identity)
	current.InputTokens = maxInt64(current.InputTokens, stored.InputTokens)
	current.OutputTokens = maxInt64(current.OutputTokens, stored.OutputTokens)
	current.ReasoningTokens = maxInt64(current.ReasoningTokens, stored.ReasoningTokens)
	current.CachedTokens = maxInt64(current.CachedTokens, stored.CachedTokens)
	current.CacheReadTokens = maxInt64(current.CacheReadTokens, stored.CacheReadTokens)
	current.CacheCreationTokens = maxInt64(current.CacheCreationTokens, stored.CacheCreationTokens)
	current.TotalTokens = maxInt64(current.TotalTokens, stored.TotalTokens)
	current.SuccessfulTokens = maxInt64(current.SuccessfulTokens, stored.SuccessfulTokens)
	current.SuccessfulRequests = maxInt64(current.SuccessfulRequests, stored.SuccessfulRequests)
	current.CreditAmountNanos = maxInt64(current.CreditAmountNanos, stored.CreditAmountNanos)
	current.CreditRatedRequests = maxInt64(current.CreditRatedRequests, stored.CreditRatedRequests)
	current.CreditUnratedRequests = maxInt64(current.CreditUnratedRequests, stored.CreditUnratedRequests)
	if current.CreditStartedAt.IsZero() || (!stored.CreditStartedAt.IsZero() && stored.CreditStartedAt.Before(current.CreditStartedAt)) {
		current.CreditStartedAt = stored.CreditStartedAt
	}
	if stored.CreditPricingUpdatedAt.After(current.CreditPricingUpdatedAt) {
		current.CreditPricingUpdatedAt = stored.CreditPricingUpdatedAt
		current.CreditPricingSource = stored.CreditPricingSource
	}
	current.FiveHourOverdraft = mergeOverdraftCycle(current.FiveHourOverdraft, stored.FiveHourOverdraft)
	current.SevenDayOverdraft = mergeOverdraftCycle(current.SevenDayOverdraft, stored.SevenDayOverdraft)
	current.Lifecycle = mergeAccountLifecycle(current.Lifecycle, stored.Lifecycle)
	if stored.LastRequestAt.After(current.LastRequestAt) {
		current.LastRequestAt = stored.LastRequestAt
	}
	if stored.UpdatedAt.After(current.UpdatedAt) {
		current.UpdatedAt = stored.UpdatedAt
	}
	current.Codex = mergeCodexUsage(current.Codex, stored.Codex)
	return sanitizeUsageAggregate(current)
}

func mergeCodexUsage(current, stored *CodexUsageSnapshot) *CodexUsageSnapshot {
	if current == nil {
		return cloneCodexUsage(stored)
	}
	if stored == nil {
		return cloneCodexUsage(current)
	}
	merged := cloneCodexUsage(current)
	if stored.ObservedAt.After(merged.ObservedAt) {
		merged.FiveHour = cloneUsageWindow(stored.FiveHour)
		merged.SevenDay = cloneUsageWindow(stored.SevenDay)
		merged.ObservedAt = stored.ObservedAt
	} else if merged.FiveHour == nil {
		merged.FiveHour = cloneUsageWindow(stored.FiveHour)
	}
	if merged.SevenDay == nil {
		merged.SevenDay = cloneUsageWindow(stored.SevenDay)
	}
	if stored.MetadataObservedAt.After(merged.MetadataObservedAt) {
		merged.PlanType = stored.PlanType
		merged.ActiveResetCount = cloneIntPointer(stored.ActiveResetCount)
		merged.MetadataObservedAt = stored.MetadataObservedAt
	} else if merged.MetadataObservedAt.IsZero() {
		if merged.PlanType == "" {
			merged.PlanType = stored.PlanType
		}
		if merged.ActiveResetCount == nil && stored.ActiveResetCount != nil {
			count := *stored.ActiveResetCount
			merged.ActiveResetCount = &count
		}
	}
	return merged
}

func mergeOverdraftCycle(current, stored *overdraftCycleState) *overdraftCycleState {
	if current == nil {
		return cloneOverdraftCycle(stored)
	}
	if stored == nil {
		return cloneOverdraftCycle(current)
	}
	if stored.ChangedAt.After(current.ChangedAt) || stored.ChangedAt.Equal(current.ChangedAt) && !stored.Active {
		return cloneOverdraftCycle(stored)
	}
	return cloneOverdraftCycle(current)
}

func mergeAccountLifecycle(current, stored *accountLifecycleState) *accountLifecycleState {
	current = sanitizeAccountLifecycle(current)
	stored = sanitizeAccountLifecycle(stored)
	if current == nil {
		return cloneAccountLifecycle(stored)
	}
	if stored == nil {
		return cloneAccountLifecycle(current)
	}
	createdAt := current.CreatedAt
	if stored.CreatedAt.Before(createdAt) {
		createdAt = stored.CreatedAt
	}
	winner := current
	if stored.StateChangedAt.After(current.StateChangedAt) ||
		stored.StateChangedAt.Equal(current.StateChangedAt) && !stored.Disabled {
		winner = stored
	}
	merged := cloneAccountLifecycle(winner)
	merged.CreatedAt = createdAt
	return merged
}

func sanitizeUsageAggregate(aggregate usageAggregate) usageAggregate {
	aggregate.InputTokens = nonNegative(aggregate.InputTokens)
	aggregate.OutputTokens = nonNegative(aggregate.OutputTokens)
	aggregate.ReasoningTokens = nonNegative(aggregate.ReasoningTokens)
	aggregate.CachedTokens = nonNegative(aggregate.CachedTokens)
	aggregate.CacheReadTokens = nonNegative(aggregate.CacheReadTokens)
	aggregate.CacheCreationTokens = nonNegative(aggregate.CacheCreationTokens)
	aggregate.TotalTokens = nonNegative(aggregate.TotalTokens)
	aggregate.SuccessfulTokens = nonNegative(aggregate.SuccessfulTokens)
	aggregate.SuccessfulRequests = nonNegative(aggregate.SuccessfulRequests)
	aggregate.CreditAmountNanos = nonNegative(aggregate.CreditAmountNanos)
	aggregate.CreditRatedRequests = nonNegative(aggregate.CreditRatedRequests)
	aggregate.CreditUnratedRequests = nonNegative(aggregate.CreditUnratedRequests)
	aggregate.CreditStartedAt = aggregate.CreditStartedAt.UTC()
	aggregate.CreditPricingUpdatedAt = aggregate.CreditPricingUpdatedAt.UTC()
	aggregate.CreditPricingSource = strings.TrimSpace(aggregate.CreditPricingSource)
	aggregate.FiveHourOverdraft = sanitizeOverdraftCycle(aggregate.FiveHourOverdraft)
	aggregate.SevenDayOverdraft = sanitizeOverdraftCycle(aggregate.SevenDayOverdraft)
	aggregate.Lifecycle = sanitizeAccountLifecycle(aggregate.Lifecycle)
	aggregate.LastRequestAt = aggregate.LastRequestAt.UTC()
	aggregate.UpdatedAt = aggregate.UpdatedAt.UTC()
	aggregate.Codex = sanitizeCodexUsage(aggregate.Codex)
	return aggregate
}

func sanitizeCodexUsage(snapshot *CodexUsageSnapshot) *CodexUsageSnapshot {
	if snapshot == nil {
		return nil
	}
	snapshot = cloneCodexUsage(snapshot)
	snapshot.ObservedAt = snapshot.ObservedAt.UTC()
	snapshot.MetadataObservedAt = snapshot.MetadataObservedAt.UTC()
	snapshot.PlanType = safeAccountPlanType(snapshot.PlanType)
	if snapshot.ActiveResetCount != nil && (*snapshot.ActiveResetCount < 0 || *snapshot.ActiveResetCount > maxActiveResetCount) {
		snapshot.ActiveResetCount = nil
	}
	snapshot.FiveHour = sanitizeUsageWindow(snapshot.FiveHour)
	snapshot.SevenDay = sanitizeUsageWindow(snapshot.SevenDay)
	if !hasCodexUsageData(snapshot) {
		return nil
	}
	return snapshot
}

func sanitizeUsageWindow(window *UsageWindowSnapshot) *UsageWindowSnapshot {
	if window == nil || mathInvalidUsagePercent(window.UsedPercent) || window.WindowMinutes < 0 || window.WindowMinutes > maxUsageWindowMinutes {
		return nil
	}
	window = cloneUsageWindow(window)
	clearPublicOverdraftState(window)
	return window
}

func clearPublicOverdraftState(window *UsageWindowSnapshot) {
	if window == nil {
		return
	}
	window.OverdraftActive = false
	window.OverdraftTokens = 0
	window.OverdraftRequests = 0
	window.OverdraftAmountUSD = 0
	window.OverdraftRated = 0
	window.OverdraftUnrated = 0
	window.OverdraftStartedAt = nil
	window.OverdraftRecoverAt = nil
}

func sanitizeOverdraftCycle(cycle *overdraftCycleState) *overdraftCycleState {
	if cycle == nil {
		return nil
	}
	cycle = cloneOverdraftCycle(cycle)
	cycle.BaselineTokens = nonNegative(cycle.BaselineTokens)
	cycle.BaselineRequests = nonNegative(cycle.BaselineRequests)
	cycle.BaselineCreditAmountNanos = nonNegative(cycle.BaselineCreditAmountNanos)
	cycle.BaselineCreditRated = nonNegative(cycle.BaselineCreditRated)
	cycle.BaselineCreditUnrated = nonNegative(cycle.BaselineCreditUnrated)
	cycle.StartedAt = cycle.StartedAt.UTC()
	cycle.RecoverAt = cycle.RecoverAt.UTC()
	cycle.ChangedAt = cycle.ChangedAt.UTC()
	if cycle.ChangedAt.IsZero() || cycle.WindowMinutes < 0 || cycle.WindowMinutes > maxUsageWindowMinutes {
		return nil
	}
	if cycle.Active && (cycle.StartedAt.IsZero() || cycle.RecoverAt.IsZero() || !cycle.RecoverAt.After(cycle.StartedAt)) {
		return nil
	}
	if !cycle.Active {
		cycle.BaselineTokens = 0
		cycle.BaselineRequests = 0
		cycle.BaselineCreditAmountNanos = 0
		cycle.BaselineCreditRated = 0
		cycle.BaselineCreditUnrated = 0
		cycle.StartedAt = time.Time{}
		cycle.RecoverAt = time.Time{}
		cycle.WindowMinutes = 0
	}
	return cycle
}

func sanitizeAccountLifecycle(state *accountLifecycleState) *accountLifecycleState {
	if state == nil {
		return nil
	}
	state = cloneAccountLifecycle(state)
	state.CreatedAt = state.CreatedAt.UTC()
	state.DisabledAt = state.DisabledAt.UTC()
	state.StateChangedAt = state.StateChangedAt.UTC()
	minimum := time.Unix(0, 0).UTC()
	if state.CreatedAt.IsZero() || state.CreatedAt.Before(minimum) || state.StateChangedAt.IsZero() || state.StateChangedAt.Before(state.CreatedAt) {
		return nil
	}
	if state.Disabled {
		if state.DisabledAt.IsZero() || state.DisabledAt.Before(state.CreatedAt) || !state.DisabledAt.Equal(state.StateChangedAt) {
			return nil
		}
	} else {
		state.DisabledAt = time.Time{}
	}
	return state
}

func mathInvalidUsagePercent(value float64) bool {
	return value < 0 || value > 10_000
}

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
	usageStoreVersion     = 1
	usageStoreLockTimeout = 2 * time.Second
	usageStoreLockStale   = 30 * time.Second
	usageStoreLockRetry   = 10 * time.Millisecond
	usageDurableDirName   = ".cpa-account-config-manager"
	usageDurableFileName  = "usage-snapshots.state"
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
	if persisted.Version != usageStoreVersion {
		return nil, fmt.Errorf("unsupported usage store version %d", persisted.Version)
	}
	return normalizeUsageAccounts(persisted.Accounts), nil
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
	for authIndex, aggregate := range values {
		authIndex = strings.TrimSpace(authIndex)
		if authIndex == "" {
			continue
		}
		aggregate = sanitizeUsageAggregate(aggregate)
		entries = append(entries, entry{authIndex: authIndex, aggregate: aggregate})
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
	current.InputTokens = maxInt64(current.InputTokens, stored.InputTokens)
	current.OutputTokens = maxInt64(current.OutputTokens, stored.OutputTokens)
	current.ReasoningTokens = maxInt64(current.ReasoningTokens, stored.ReasoningTokens)
	current.CachedTokens = maxInt64(current.CachedTokens, stored.CachedTokens)
	current.CacheReadTokens = maxInt64(current.CacheReadTokens, stored.CacheReadTokens)
	current.CacheCreationTokens = maxInt64(current.CacheCreationTokens, stored.CacheCreationTokens)
	current.TotalTokens = maxInt64(current.TotalTokens, stored.TotalTokens)
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
	if stored.ObservedAt.After(current.ObservedAt) {
		return cloneCodexUsage(stored)
	}
	merged := cloneCodexUsage(current)
	if merged.FiveHour == nil {
		merged.FiveHour = cloneUsageWindow(stored.FiveHour)
	}
	if merged.SevenDay == nil {
		merged.SevenDay = cloneUsageWindow(stored.SevenDay)
	}
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
	snapshot.FiveHour = sanitizeUsageWindow(snapshot.FiveHour)
	snapshot.SevenDay = sanitizeUsageWindow(snapshot.SevenDay)
	if snapshot.FiveHour == nil && snapshot.SevenDay == nil {
		return nil
	}
	return snapshot
}

func sanitizeUsageWindow(window *UsageWindowSnapshot) *UsageWindowSnapshot {
	if window == nil || mathInvalidUsagePercent(window.UsedPercent) || window.WindowMinutes < 0 || window.WindowMinutes > maxUsageWindowMinutes {
		return nil
	}
	window = cloneUsageWindow(window)
	return window
}

func mathInvalidUsagePercent(value float64) bool {
	return value < 0 || value > 10_000
}

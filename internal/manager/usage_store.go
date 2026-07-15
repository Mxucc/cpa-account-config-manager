package manager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const usageStoreVersion = 1

type persistedUsageState struct {
	Version  int                       `json:"version"`
	Accounts map[string]usageAggregate `json:"accounts"`
}

func usageStorePath(dataDir string) string {
	return filepath.Join(dataDir, "usage-snapshots.json")
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
	type entry struct {
		authIndex string
		aggregate usageAggregate
	}
	entries := make([]entry, 0, len(persisted.Accounts))
	for authIndex, aggregate := range persisted.Accounts {
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
	return accounts, nil
}

func saveUsageState(path string, accounts map[string]usageAggregate) error {
	return savePrivateJSON(path, persistedUsageState{
		Version:  usageStoreVersion,
		Accounts: cloneUsageAggregates(accounts),
	})
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

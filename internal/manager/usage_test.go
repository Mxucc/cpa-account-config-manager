package manager

import (
	"bytes"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestUsageTrackerAggregatesSanitizedUsageAndCodexWindows(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	tracker := NewUsageTracker()
	tracker.now = func() time.Time { return now }
	tracker.persistDelay = time.Hour
	tracker.Configure(Config{DataDir: dataDir})

	tracker.Observe(cpaapi.UsageRecord{
		AuthIndex:   " auth-index-1 ",
		AuthID:      "runtime-secret-id",
		APIKey:      "sk-client-secret",
		RequestedAt: now.Add(-time.Minute),
		Failure:     cpaapi.UsageFailure{StatusCode: 429, Body: "Bearer upstream-secret"},
		Detail: cpaapi.UsageDetail{
			InputTokens: 10, OutputTokens: 4, ReasoningTokens: 2,
			CachedTokens: 3, CacheReadTokens: 2, CacheCreationTokens: 1,
		},
		ResponseHeaders: http.Header{
			"Authorization":                         []string{"Bearer header-secret"},
			"Set-Cookie":                            []string{"session=secret"},
			"X-Codex-Primary-Used-Percent":          []string{"34"},
			"X-Codex-Primary-Reset-After-Seconds":   []string{"604800"},
			"X-Codex-Primary-Window-Minutes":        []string{"10080"},
			"X-Codex-Secondary-Used-Percent":        []string{"12.5"},
			"X-Codex-Secondary-Reset-After-Seconds": []string{"1800"},
			"X-Codex-Secondary-Window-Minutes":      []string{"300"},
		},
	})
	tracker.Observe(cpaapi.UsageRecord{
		AuthIndex:   "auth-index-1",
		RequestedAt: now,
		Detail:      cpaapi.UsageDetail{InputTokens: 6, OutputTokens: 3, TotalTokens: 9},
	})

	snapshot := tracker.Snapshot("auth-index-1")
	if snapshot == nil {
		t.Fatal("usage snapshot is nil")
	}
	if snapshot.InputTokens != 16 || snapshot.OutputTokens != 7 || snapshot.ReasoningTokens != 2 || snapshot.TotalTokens != 25 {
		t.Fatalf("token totals = in:%d out:%d reasoning:%d total:%d", snapshot.InputTokens, snapshot.OutputTokens, snapshot.ReasoningTokens, snapshot.TotalTokens)
	}
	if snapshot.CachedTokens != 3 || snapshot.CacheReadTokens != 2 || snapshot.CacheCreationTokens != 1 {
		t.Fatalf("cache totals = cached:%d read:%d creation:%d", snapshot.CachedTokens, snapshot.CacheReadTokens, snapshot.CacheCreationTokens)
	}
	if snapshot.LastRequestAt == nil || !snapshot.LastRequestAt.Equal(now) {
		t.Fatalf("last request = %v, want %v", snapshot.LastRequestAt, now)
	}
	if snapshot.Codex == nil || snapshot.Codex.FiveHour == nil || snapshot.Codex.SevenDay == nil {
		t.Fatalf("codex snapshot = %#v", snapshot.Codex)
	}
	if snapshot.Codex.FiveHour.UsedPercent != 12.5 || snapshot.Codex.FiveHour.WindowMinutes != 300 {
		t.Fatalf("5h window = %#v", snapshot.Codex.FiveHour)
	}
	if snapshot.Codex.SevenDay.UsedPercent != 34 || snapshot.Codex.SevenDay.WindowMinutes != 10080 {
		t.Fatalf("7d window = %#v", snapshot.Codex.SevenDay)
	}
	if snapshot.Codex.FiveHour.ResetAt == nil || !snapshot.Codex.FiveHour.ResetAt.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("5h reset = %v", snapshot.Codex.FiveHour.ResetAt)
	}

	tracker.Close()
	raw, errRead := os.ReadFile(usageStorePath(dataDir))
	if errRead != nil {
		t.Fatalf("read usage state: %v", errRead)
	}
	for _, secret := range []string{"sk-client-secret", "runtime-secret-id", "upstream-secret", "header-secret", "session=secret", "Authorization", "Set-Cookie"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("persisted usage leaked %q: %s", secret, raw)
		}
	}
}

func TestUsageTrackerLoadsPersistedSnapshotAndExpiresQuotaWindows(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	first := NewUsageTracker()
	first.now = func() time.Time { return now }
	first.persistDelay = time.Hour
	first.Configure(Config{DataDir: dataDir})
	first.Observe(cpaapi.UsageRecord{
		AuthIndex: "persisted",
		Detail:    cpaapi.UsageDetail{TotalTokens: 42},
		ResponseHeaders: http.Header{
			"X-Codex-Secondary-Used-Percent":        []string{"80"},
			"X-Codex-Secondary-Reset-After-Seconds": []string{"10"},
			"X-Codex-Secondary-Window-Minutes":      []string{"300"},
		},
	})
	first.Close()

	second := NewUsageTracker()
	defer second.Close()
	second.now = func() time.Time { return now.Add(5 * time.Second) }
	second.Configure(Config{DataDir: dataDir})
	loaded := second.Snapshot("persisted")
	if loaded == nil || loaded.TotalTokens != 42 || loaded.Codex == nil || loaded.Codex.FiveHour == nil {
		t.Fatalf("loaded usage = %#v", loaded)
	}
	second.now = func() time.Time { return now.Add(11 * time.Second) }
	expired := second.Snapshot("persisted")
	if expired == nil || expired.TotalTokens != 42 {
		t.Fatalf("expired usage lost token totals: %#v", expired)
	}
	if expired.Codex != nil {
		t.Fatalf("expired codex window = %#v, want nil", expired.Codex)
	}
}

func TestUsageCodexWindowNormalizationHandlesReversedAndLegacyHeaders(t *testing.T) {
	now := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		headers      http.Header
		wantFive     float64
		wantSeven    float64
		wantFiveMin  int
		wantSevenMin int
	}{
		{
			name: "reversed explicit windows",
			headers: http.Header{
				"X-Codex-Primary-Used-Percent":     []string{"7"},
				"X-Codex-Primary-Window-Minutes":   []string{"300"},
				"X-Codex-Secondary-Used-Percent":   []string{"70"},
				"X-Codex-Secondary-Window-Minutes": []string{"10080"},
			},
			wantFive: 7, wantSeven: 70, wantFiveMin: 300, wantSevenMin: 10080,
		},
		{
			name: "legacy primary weekly secondary short",
			headers: http.Header{
				"X-Codex-Primary-Used-Percent":   []string{"71"},
				"X-Codex-Secondary-Used-Percent": []string{"8"},
			},
			wantFive: 8, wantSeven: 71,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := parseCodexUsageHeaders(test.headers, now)
			if snapshot == nil || snapshot.FiveHour == nil || snapshot.SevenDay == nil {
				t.Fatalf("snapshot = %#v", snapshot)
			}
			if snapshot.FiveHour.UsedPercent != test.wantFive || snapshot.SevenDay.UsedPercent != test.wantSeven {
				t.Fatalf("usage windows = 5h:%#v 7d:%#v", snapshot.FiveHour, snapshot.SevenDay)
			}
			if snapshot.FiveHour.WindowMinutes != test.wantFiveMin || snapshot.SevenDay.WindowMinutes != test.wantSevenMin {
				t.Fatalf("window minutes = 5h:%d 7d:%d", snapshot.FiveHour.WindowMinutes, snapshot.SevenDay.WindowMinutes)
			}
		})
	}
}

func TestUsageTrackerBoundsAccountsAndIgnoresMissingAuthIndex(t *testing.T) {
	tracker := NewUsageTracker()
	defer tracker.Close()
	now := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	tracker.now = func() time.Time { return now }
	tracker.Observe(cpaapi.UsageRecord{Detail: cpaapi.UsageDetail{TotalTokens: 99}})
	for index := 0; index < maxUsageAccounts+1; index++ {
		tracker.now = func() time.Time { return now.Add(time.Duration(index) * time.Second) }
		tracker.Observe(cpaapi.UsageRecord{
			AuthIndex: "auth-" + strconv.Itoa(index),
			Detail:    cpaapi.UsageDetail{TotalTokens: 1},
		})
	}
	tracker.mu.RLock()
	accountCount := len(tracker.accounts)
	tracker.mu.RUnlock()
	if accountCount != maxUsageAccounts {
		t.Fatalf("usage accounts = %d, want %d", accountCount, maxUsageAccounts)
	}
	if tracker.Snapshot("auth-0") != nil {
		t.Fatal("oldest usage account was not evicted")
	}
	if tracker.Snapshot("auth-10000") == nil {
		t.Fatal("newest usage account is missing")
	}
}

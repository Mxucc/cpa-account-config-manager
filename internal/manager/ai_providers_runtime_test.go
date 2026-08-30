package manager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestProviderRuntimePersistsAggregatesAcrossRestart(t *testing.T) {
	dataDir := t.TempDir()
	first := NewProviderRuntimeTracker(nil)
	first.Configure(Config{DataDir: dataDir})
	first.ObserveUsage(cpaapi.UsageRecord{
		Provider:  "openai",
		AuthIndex: "auth-a",
		Model:     "gpt-5.5",
		Detail:    cpaapi.UsageDetail{InputTokens: 12, OutputTokens: 8, TotalTokens: 20},
	})
	first.ObserveRequest(cpaapi.RequestInterceptRequest{
		RequestID: "in-flight",
		ToFormat:  "openai",
		Metadata:  map[string]any{"selected_auth_index": "auth-a"},
	})
	first.Shutdown()

	if _, errStat := os.Stat(filepath.Join(dataDir, providerRuntimeStoreFileName)); errStat != nil {
		t.Fatalf("provider runtime state was not written: %v", errStat)
	}

	second := NewProviderRuntimeTracker(nil)
	second.Configure(Config{DataDir: dataDir})
	snapshots := second.Snapshot()
	if len(snapshots) != 1 {
		t.Fatalf("restored snapshots = %#v", snapshots)
	}
	snapshot := snapshots[0]
	if snapshot.TotalTokens != 20 || snapshot.InputTokens != 12 || snapshot.OutputTokens != 8 {
		t.Fatalf("restored token totals = %+v", snapshot)
	}
	if len(snapshot.Models) != 1 || snapshot.Models[0].TotalTokens != 20 {
		t.Fatalf("restored model usage = %+v", snapshot.Models)
	}
	if snapshot.Active != 0 {
		t.Fatalf("in-flight request was restored as active: %+v", snapshot)
	}
	second.Shutdown()
}

func TestProviderRuntimeIgnoresCorruptState(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, providerRuntimeStoreFileName)
	if errWrite := os.WriteFile(path, []byte("not-json"), 0o600); errWrite != nil {
		t.Fatalf("write corrupt state: %v", errWrite)
	}
	tracker := NewProviderRuntimeTracker(nil)
	tracker.Configure(Config{DataDir: dataDir})
	if snapshots := tracker.Snapshot(); len(snapshots) != 0 {
		t.Fatalf("corrupt state produced snapshots: %+v", snapshots)
	}
	tracker.Shutdown()
}

func TestProviderRuntimeReloadsAfterShutdownOnSameTracker(t *testing.T) {
	dataDir := t.TempDir()
	tracker := NewProviderRuntimeTracker(nil)
	tracker.Configure(Config{DataDir: dataDir})
	tracker.ObserveUsage(cpaapi.UsageRecord{
		Provider:  "openai",
		AuthIndex: "auth-a",
		Model:     "gpt-5.5",
		Detail:    cpaapi.UsageDetail{TotalTokens: 9},
	})
	tracker.Shutdown()

	tracker.Configure(Config{DataDir: dataDir})
	snapshots := tracker.Snapshot()
	if len(snapshots) != 1 || snapshots[0].TotalTokens != 9 {
		t.Fatalf("reloaded snapshots = %+v", snapshots)
	}
	tracker.Shutdown()
}

func TestProviderRuntimeAggregatesByProviderAndModel(t *testing.T) {
	tracker := NewProviderRuntimeTracker(nil)
	tracker.now = func() time.Time { return time.Unix(10, 0) }
	tracker.ObserveUsage(cpaapi.UsageRecord{Provider: "openai", AuthIndex: "a", Model: "gpt-5.5", Detail: cpaapi.UsageDetail{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}})
	tracker.ObserveUsage(cpaapi.UsageRecord{Provider: "openai", AuthIndex: "a", Model: "gpt-5.5", Detail: cpaapi.UsageDetail{InputTokens: 1, OutputTokens: 4, TotalTokens: 5}})
	tracker.ObserveUsage(cpaapi.UsageRecord{Provider: "claude", AuthIndex: "a", Model: "claude-3", Detail: cpaapi.UsageDetail{TotalTokens: 9}})

	snapshots := tracker.Snapshot()
	if len(snapshots) != 2 {
		t.Fatalf("snapshots = %d, want 2", len(snapshots))
	}
	var found bool
	for _, snapshot := range snapshots {
		if snapshot.Provider == "openai" {
			found = true
			if snapshot.TotalTokens != 10 || len(snapshot.Models) != 1 || snapshot.Models[0].TotalTokens != 10 {
				t.Fatalf("unexpected openai aggregate: %+v", snapshot)
			}
		}
	}
	if !found {
		t.Fatal("openai snapshot missing")
	}
}

func TestProviderRuntimeActiveIsIdempotent(t *testing.T) {
	tracker := NewProviderRuntimeTracker(nil)
	request := cpaapi.RequestInterceptRequest{RequestID: "r1", ToFormat: "openai", Metadata: map[string]any{"selected_auth_index": "auth-a"}}
	tracker.ObserveRequest(request)
	tracker.ObserveRequest(request)
	tracker.Complete(cpaapi.RequestCompletion{RequestID: "r1"})
	tracker.Complete(cpaapi.RequestCompletion{RequestID: "r1"})
	snapshots := tracker.Snapshot()
	if len(snapshots) != 1 || snapshots[0].Active != 0 {
		t.Fatalf("unexpected active count: %+v", snapshots)
	}
}

func TestProviderRuntimeDoesNotExposeAPIKey(t *testing.T) {
	tracker := NewProviderRuntimeTracker(nil)
	secret := "sk-secret-provider-key"
	tracker.ObserveUsage(cpaapi.UsageRecord{Provider: "openai", APIKey: secret, Model: "gpt", Detail: cpaapi.UsageDetail{TotalTokens: 1}})
	encoded := ""
	for _, snapshot := range tracker.Snapshot() {
		encoded += snapshot.Identity + snapshot.Provider
	}
	if strings.Contains(encoded, secret) {
		t.Fatalf("secret leaked in snapshot: %s", encoded)
	}
}

func TestProviderRuntimePrunesRequestsWithoutCompletion(t *testing.T) {
	tracker := NewProviderRuntimeTracker(nil)
	now := time.Unix(10_000, 0).UTC()
	tracker.now = func() time.Time { return now }
	tracker.ObserveRequest(cpaapi.RequestInterceptRequest{RequestID: "stale", ToFormat: "openai", Metadata: map[string]any{"selected_auth_index": "auth-a"}})
	if snapshots := tracker.Snapshot(); len(snapshots) != 1 || snapshots[0].Active != 1 {
		t.Fatalf("initial snapshots = %+v", snapshots)
	}
	now = now.Add(providerRuntimeRequestLease + time.Minute)
	// A later lifecycle event triggers the bounded cleanup even when CPA never
	// delivered a completion callback for the stale request.
	tracker.ObserveRequest(cpaapi.RequestInterceptRequest{RequestID: "fresh", ToFormat: "openai", Metadata: map[string]any{"selected_auth_index": "auth-a"}})
	snapshots := tracker.Snapshot()
	if len(snapshots) != 1 || snapshots[0].Active != 1 {
		t.Fatalf("stale request was not pruned: %+v", snapshots)
	}
}

func TestProviderRuntimeNormalizesProviderNames(t *testing.T) {
	tracker := NewProviderRuntimeTracker(nil)
	tracker.ObserveUsage(cpaapi.UsageRecord{Provider: " OpenAI ", AuthIndex: "same", Model: "gpt", Detail: cpaapi.UsageDetail{TotalTokens: 2}})
	tracker.ObserveUsage(cpaapi.UsageRecord{Provider: "openai", AuthIndex: "same", Model: "gpt", Detail: cpaapi.UsageDetail{TotalTokens: 3}})
	snapshots := tracker.Snapshot()
	if len(snapshots) != 1 || snapshots[0].Provider != "openai" || snapshots[0].TotalTokens != 5 {
		t.Fatalf("provider normalization failed: %+v", snapshots)
	}
}

func TestProviderRuntimeEvictsOldestIdleAggregate(t *testing.T) {
	tracker := NewProviderRuntimeTracker(nil)
	tracker.mu.Lock()
	tracker.aggregates["old"] = &providerRuntimeAggregate{Provider: "old", Identity: "old", UpdatedAt: time.Unix(1, 0)}
	tracker.aggregates["new"] = &providerRuntimeAggregate{Provider: "new", Identity: "new", UpdatedAt: time.Unix(2, 0)}
	if !tracker.evictIdleAggregateLocked() {
		t.Fatal("evictIdleAggregateLocked() returned false")
	}
	if _, exists := tracker.aggregates["old"]; exists {
		t.Fatal("oldest idle aggregate was not evicted")
	}
	if _, exists := tracker.aggregates["new"]; !exists {
		t.Fatal("newer aggregate was evicted")
	}
	tracker.mu.Unlock()
}

func TestProviderRuntimeIncludesConfiguredConcurrencyLimit(t *testing.T) {
	concurrency := NewAccountConcurrencyService()
	concurrency.Configure(Config{DataDir: t.TempDir()}, cpaapi.SchemaVersion)
	if errSet := concurrency.SetLimit(Account{AuthID: "auth-a", ID: "account-a"}, 7); errSet != nil {
		t.Fatalf("SetLimit() error = %v", errSet)
	}
	tracker := NewProviderRuntimeTracker(nil)
	tracker.SetAccountConcurrency(concurrency)
	tracker.ObserveRequest(cpaapi.RequestInterceptRequest{
		RequestID: "request-a",
		ToFormat:  "openai",
		Metadata:  map[string]any{"selected_auth_index": "auth-a"},
	})
	snapshots := tracker.Snapshot()
	if len(snapshots) != 1 {
		t.Fatalf("snapshots = %#v", snapshots)
	}
	if snapshots[0].Active != 1 || snapshots[0].Limit != 7 {
		t.Fatalf("runtime concurrency = active=%d limit=%d, want 1/7", snapshots[0].Active, snapshots[0].Limit)
	}
}

func TestProviderRuntimeCalculatesRollingFiveHourAndSevenDayWindows(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	tracker := NewProviderRuntimeTracker(nil)
	tracker.now = func() time.Time { return now }
	tracker.ObserveUsage(cpaapi.UsageRecord{Provider: "openai", AuthIndex: "auth-a", Model: "gpt", Detail: cpaapi.UsageDetail{TotalTokens: 10}})
	now = now.Add(4 * time.Hour)
	tracker.ObserveUsage(cpaapi.UsageRecord{Provider: "openai", AuthIndex: "auth-a", Model: "gpt", Detail: cpaapi.UsageDetail{TotalTokens: 20}})
	now = now.Add(2 * time.Hour)
	tracker.ObserveUsage(cpaapi.UsageRecord{Provider: "openai", AuthIndex: "auth-a", Model: "gpt", Detail: cpaapi.UsageDetail{TotalTokens: 30}})

	snapshots := tracker.Snapshot()
	if len(snapshots) != 1 {
		t.Fatalf("snapshots = %#v", snapshots)
	}
	quota := snapshots[0].Quota
	if quota.FiveHourUsedTokens != 50 || quota.SevenDayUsedTokens != 60 {
		t.Fatalf("rolling quota = %#v, want 50/60", quota)
	}
}

func TestProviderRuntimeExcludesEventsOutsideSevenDayWindow(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	tracker := NewProviderRuntimeTracker(nil)
	tracker.now = func() time.Time { return now }
	tracker.ObserveUsage(cpaapi.UsageRecord{Provider: "openai", AuthIndex: "auth-a", Model: "gpt", Detail: cpaapi.UsageDetail{TotalTokens: 7}})
	now = now.Add(7*24*time.Hour + time.Second)
	if quota := tracker.Snapshot()[0].Quota; quota.FiveHourUsedTokens != 0 || quota.SevenDayUsedTokens != 0 {
		t.Fatalf("expired quota events retained = %#v", quota)
	}
}

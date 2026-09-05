package manager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

type scaledUsageCreditCalculator struct{}

func (scaledUsageCreditCalculator) Enabled() bool { return true }
func (scaledUsageCreditCalculator) Calculate(record cpaapi.UsageRecord) CreditCharge {
	return CreditCharge{Enabled: true, Rated: true, AmountNanos: nonNegative(record.Detail.TotalTokens) * 1_000_000}
}
func (scaledUsageCreditCalculator) Snapshot() CreditPricingSnapshot { return CreditPricingSnapshot{} }

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
	if snapshot.Used60s != 1 || snapshot.Used15s != 1 {
		t.Fatalf("rolling request events were not restored across restart: %+v", snapshot)
	}
	second.Shutdown()
}

func TestProviderRuntimeIgnoresCorruptState(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, providerRuntimeStoreFileName)
	backupPath := providerRuntimeBackupPath(path)
	if errWrite := os.WriteFile(path, []byte("not-json"), 0o600); errWrite != nil {
		t.Fatalf("write corrupt state: %v", errWrite)
	}
	if errWrite := os.WriteFile(backupPath, []byte("also-not-json"), 0o600); errWrite != nil {
		t.Fatalf("write corrupt backup: %v", errWrite)
	}
	tracker := NewProviderRuntimeTracker(nil)
	tracker.Configure(Config{DataDir: dataDir})
	if snapshots := tracker.Snapshot(); len(snapshots) != 0 {
		t.Fatalf("corrupt state produced snapshots: %+v", snapshots)
	}
	if got := tracker.StorageError(); got != "provider runtime state could not be loaded" {
		t.Fatalf("StorageError() = %q", got)
	}
	tracker.ObserveUsage(cpaapi.UsageRecord{Provider: "openai", AuthIndex: "auth-a", Model: "gpt", Detail: cpaapi.UsageDetail{TotalTokens: 3}})
	tracker.Shutdown()
	for _, corruptPath := range []string{path, backupPath} {
		raw, errRead := os.ReadFile(corruptPath)
		if errRead != nil {
			t.Fatalf("read %s: %v", corruptPath, errRead)
		}
		if string(raw) != map[string]string{path: "not-json", backupPath: "also-not-json"}[corruptPath] {
			t.Fatalf("corrupt state %s was overwritten: %q", corruptPath, raw)
		}
	}
}

func TestProviderRuntimePrunesPersistedRollingEventsOnLoad(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(dataDir, providerRuntimeStoreFileName)
	key := runtimeAggregateKey("openai", "auth-index:auth-a")
	state := persistedProviderRuntimeState{
		Version: providerRuntimeStoreVersion,
		Aggregates: map[string]providerRuntimeAggregate{
			key: {
				Provider: "openai", AuthIndex: "auth-a", Identity: "auth-index:auth-a",
				Events: []providerRuntimeEvent{
					{At: now.Add(-8 * 24 * time.Hour), AmountNanos: 1},
					{At: now.Add(-time.Hour), AmountNanos: 2},
				},
				RequestEvents: []time.Time{now.Add(-2 * time.Hour), now.Add(-10 * time.Second)},
				Models:        map[string]*providerRuntimeModel{},
			},
		},
	}
	if errSave := savePrivateJSON(path, state); errSave != nil {
		t.Fatalf("write provider state: %v", errSave)
	}
	tracker := NewProviderRuntimeTracker(nil)
	tracker.now = func() time.Time { return now }
	tracker.Configure(Config{DataDir: dataDir})
	snapshots := tracker.Snapshot()
	if len(snapshots) != 1 || snapshots[0].Used60s != 1 {
		t.Fatalf("loaded rolling events = %+v", snapshots)
	}
	tracker.Shutdown()
	loaded, errLoad := loadProviderRuntimeState(path)
	if errLoad != nil {
		t.Fatalf("reload provider state: %v", errLoad)
	}
	if got := len(loaded.Aggregates[key].RequestEvents); got != 1 {
		t.Fatalf("persisted request events = %d, want 1", got)
	}
	if got := len(loaded.Aggregates[key].Events); got != 1 {
		t.Fatalf("persisted cost events = %d, want 1", got)
	}
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
	policies := NewQuotaPolicyService()
	policies.Configure(Config{DataDir: t.TempDir()})
	if errSet := policies.SetProviderPolicy(ProviderQuotaPolicy{Key: "openai:auth-a", Concurrency: intPointer(7), Concurrency15s: intPointer(3)}); errSet != nil {
		t.Fatalf("SetProviderPolicy() error = %v", errSet)
	}
	tracker := NewProviderRuntimeTracker(nil)
	tracker.SetQuotaPolicies(policies)
	tracker.ObserveRequest(cpaapi.RequestInterceptRequest{
		RequestID: "request-a",
		ToFormat:  "openai",
		Metadata:  map[string]any{"selected_auth_index": "auth-a"},
	})
	snapshots := tracker.Snapshot()
	if len(snapshots) != 1 {
		t.Fatalf("snapshots = %#v", snapshots)
	}
	if snapshots[0].Active != 1 || snapshots[0].Limit != 7 || snapshots[0].Limit15s != 3 || snapshots[0].Used60s != 1 || snapshots[0].Used15s != 1 || !snapshots[0].ConcurrencyConfigurable {
		t.Fatalf("runtime concurrency = %+v", snapshots[0])
	}
}

func TestProviderRuntimeObservesConfiguredRollingWindowsWithoutRejecting(t *testing.T) {
	policies := NewQuotaPolicyService()
	policies.Configure(Config{DataDir: t.TempDir()})
	if errSet := policies.SetProviderPolicy(ProviderQuotaPolicy{Key: "openai:auth-a", Concurrency: intPointer(3), Concurrency15s: intPointer(2)}); errSet != nil {
		t.Fatal(errSet)
	}
	tracker := NewProviderRuntimeTracker(nil)
	tracker.SetQuotaPolicies(policies)
	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	tracker.now = func() time.Time { return now }
	request := func(id string) cpaapi.RequestInterceptRequest {
		return cpaapi.RequestInterceptRequest{RequestID: id, ToFormat: "openai", Metadata: map[string]any{"selected_auth_index": "auth-a"}}
	}
	for _, id := range []string{"request-1", "request-2", "request-3"} {
		if response, changed := tracker.InterceptRequest(request(id)); changed || response.Terminate || response.StatusCode != 0 {
			t.Fatalf("configured provider window rejected %s: %#v changed=%v", id, response, changed)
		}
		tracker.Complete(cpaapi.RequestCompletion{RequestID: id})
	}
	if snapshot := tracker.Snapshot()[0]; snapshot.Used15s != 3 || snapshot.Used60s != 3 {
		t.Fatalf("observed rolling windows = %+v", snapshot)
	}
}

func TestProviderRuntimeDuplicateRequestDoesNotConsumeWindowTwice(t *testing.T) {
	policies := NewQuotaPolicyService()
	policies.Configure(Config{DataDir: t.TempDir()})
	if errSet := policies.SetProviderPolicy(ProviderQuotaPolicy{Key: "openai:auth-a", Concurrency15s: intPointer(2)}); errSet != nil {
		t.Fatal(errSet)
	}
	tracker := NewProviderRuntimeTracker(nil)
	tracker.SetQuotaPolicies(policies)
	request := cpaapi.RequestInterceptRequest{RequestID: "same", ToFormat: "openai", Metadata: map[string]any{"selected_auth_index": "auth-a"}}
	tracker.InterceptRequest(request)
	tracker.InterceptRequest(request)
	snapshot := tracker.Snapshot()[0]
	if snapshot.Active != 1 || snapshot.Used60s != 1 || snapshot.Used15s != 1 {
		t.Fatalf("duplicate request consumed the window twice: %+v", snapshot)
	}
}

func TestProviderRuntimeCalculatesRollingFiveHourAndSevenDayWindows(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	policies := NewQuotaPolicyService()
	policies.Configure(Config{DataDir: t.TempDir()})
	fiveHourBudget, sevenDayBudget := 0.10, 0.20
	if err := policies.SetProviderPolicy(ProviderQuotaPolicy{Key: "openai:auth-a", FiveHour: QuotaWindowPolicy{BudgetAmountUSD: &fiveHourBudget}, SevenDay: QuotaWindowPolicy{BudgetAmountUSD: &sevenDayBudget}}); err != nil {
		t.Fatal(err)
	}
	tracker := NewProviderRuntimeTracker(scaledUsageCreditCalculator{})
	tracker.SetQuotaPolicies(policies)
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
	if quota.FiveHourAmountUSD != 0.05 || quota.SevenDayAmountUSD != 0.06 {
		t.Fatalf("rolling quota amounts = %#v, want 0.05/0.06 USD", quota)
	}
	if quota.FiveHourPercent != 50 || quota.SevenDayPercent != 30 {
		t.Fatalf("rolling quota percentages = %#v, want 50/30", quota)
	}
}

func TestProviderRuntimeExcludesEventsOutsideSevenDayWindow(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	tracker := NewProviderRuntimeTracker(scaledUsageCreditCalculator{})
	tracker.now = func() time.Time { return now }
	tracker.ObserveUsage(cpaapi.UsageRecord{Provider: "openai", AuthIndex: "auth-a", Model: "gpt", Detail: cpaapi.UsageDetail{TotalTokens: 7}})
	now = now.Add(7*24*time.Hour + time.Second)
	if quota := tracker.Snapshot()[0].Quota; quota.FiveHourAmountUSD != 0 || quota.SevenDayAmountUSD != 0 {
		t.Fatalf("expired quota events retained = %#v", quota)
	}
}

func TestProviderRuntimePreservesStableCredentialAcrossAuthIndexChange(t *testing.T) {
	dataDir := t.TempDir()
	tracker := NewProviderRuntimeTracker(nil)
	tracker.Configure(Config{DataDir: dataDir})
	tracker.ObserveUsage(cpaapi.UsageRecord{
		Provider: "openai", AuthIndex: "auth-old", APIKey: "sk-same", Model: "gpt-5.5",
		Detail: cpaapi.UsageDetail{TotalTokens: 10},
	})
	tracker.ObserveUsage(cpaapi.UsageRecord{
		Provider: "openai", AuthIndex: "auth-new", APIKey: "sk-same", Model: "gpt-5.5",
		Detail: cpaapi.UsageDetail{TotalTokens: 7},
	})
	tracker.Shutdown()
	for _, statePath := range []string{filepath.Join(dataDir, providerRuntimeStoreFileName), providerRuntimeBackupPath(filepath.Join(dataDir, providerRuntimeStoreFileName))} {
		raw, errRead := os.ReadFile(statePath)
		if errRead != nil {
			t.Fatalf("read persisted state: %v", errRead)
		}
		if strings.Contains(string(raw), "sk-same") {
			t.Fatalf("API key leaked into persisted state %s", statePath)
		}
	}

	restored := NewProviderRuntimeTracker(nil)
	restored.Configure(Config{DataDir: dataDir})
	snapshots := restored.Snapshot()
	if len(snapshots) != 1 {
		t.Fatalf("credential rotation created duplicate aggregates: %+v", snapshots)
	}
	if snapshots[0].TotalTokens != 17 || snapshots[0].AuthIndex != "auth-new" {
		t.Fatalf("credential rotation lost usage: %+v", snapshots[0])
	}
	if strings.Contains(snapshots[0].Identity, "sk-same") {
		t.Fatalf("credential leaked into identity: %q", snapshots[0].Identity)
	}
	restored.Shutdown()
}

func TestProviderRuntimePersistsDurableAuthAdjacentStore(t *testing.T) {
	authDir := t.TempDir()
	authPath := filepath.Join(authDir, "codex-auth.json")
	if err := os.WriteFile(authPath, []byte(`{"type":"codex"}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
	entry := cpaapi.HostAuthFileEntry{
		AuthIndex: "auth-a", Name: filepath.Base(authPath), Source: "file", Path: authPath,
	}
	fallbackDir := t.TempDir()
	first := NewProviderRuntimeTracker(nil)
	first.Configure(Config{DataDir: fallbackDir, implicitDataDir: true})
	first.ObserveUsage(cpaapi.UsageRecord{
		Provider: "openai", AuthIndex: "auth-a", Model: "gpt-5.5",
		Detail: cpaapi.UsageDetail{TotalTokens: 23},
	})
	first.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{entry})
	first.Shutdown()

	durablePath := providerRuntimeStorePath(filepath.Join(authDir, usageDurableDirName))
	if _, err := os.Stat(durablePath); err != nil {
		t.Fatalf("durable provider store was not written: %v", err)
	}
	second := NewProviderRuntimeTracker(nil)
	second.Configure(Config{DataDir: t.TempDir(), implicitDataDir: true})
	second.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{entry})
	defer second.Shutdown()
	snapshots := second.Snapshot()
	if len(snapshots) != 1 || snapshots[0].TotalTokens != 23 {
		t.Fatalf("durable provider usage was not restored: %+v", snapshots)
	}
}

func TestProviderRuntimeRecoversFromBackup(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, providerRuntimeStoreFileName)
	state := persistedProviderRuntimeState{
		Version: providerRuntimeStoreVersion,
		Aggregates: map[string]providerRuntimeAggregate{
			runtimeAggregateKey("openai", "auth-index:auth-a"): {
				Provider: "openai", AuthIndex: "auth-a", Identity: "auth-index:auth-a",
				TotalTokens: 31, Models: map[string]*providerRuntimeModel{},
			},
		},
	}
	if err := savePrivateJSON(providerRuntimeBackupPath(path), state); err != nil {
		t.Fatalf("write provider backup: %v", err)
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0o600); err != nil {
		t.Fatalf("write corrupt provider state: %v", err)
	}
	tracker := NewProviderRuntimeTracker(nil)
	tracker.Configure(Config{DataDir: dataDir})
	defer tracker.Shutdown()
	snapshots := tracker.Snapshot()
	if len(snapshots) != 1 || snapshots[0].TotalTokens != 31 {
		t.Fatalf("backup state was not recovered: %+v", snapshots)
	}
	if tracker.StorageError() != "" {
		t.Fatalf("backup recovery left storage error: %q", tracker.StorageError())
	}
}

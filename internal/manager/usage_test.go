package manager

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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
	for _, path := range []string{usageStorePath(dataDir), usageStoreBackupPath(usageStorePath(dataDir))} {
		raw, errRead := os.ReadFile(path)
		if errRead != nil {
			t.Fatalf("read usage state %q: %v", filepath.Base(path), errRead)
		}
		for _, secret := range []string{"sk-client-secret", "runtime-secret-id", "upstream-secret", "header-secret", "session=secret", "Authorization", "Set-Cookie"} {
			if bytes.Contains(raw, []byte(secret)) {
				t.Fatalf("persisted usage %q leaked %q: %s", filepath.Base(path), secret, raw)
			}
		}
	}
}

func TestUsageTrackerMeasuresOverdraftFromFrozenNormalProbeFailureBaseline(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Date(2026, time.July, 30, 0, 0, 0, 0, time.UTC)
	tracker := NewUsageTracker()
	tracker.now = func() time.Time { return now }
	tracker.persistDelay = time.Hour
	tracker.Configure(Config{DataDir: dataDir})

	exhaustedHeaders := http.Header{
		"X-Codex-Secondary-Used-Percent":        []string{"100"},
		"X-Codex-Secondary-Reset-After-Seconds": []string{"3600"},
		"X-Codex-Secondary-Window-Minutes":      []string{"300"},
		"X-Codex-Primary-Used-Percent":          []string{"17"},
		"X-Codex-Primary-Reset-After-Seconds":   []string{"86400"},
		"X-Codex-Primary-Window-Minutes":        []string{"10080"},
	}
	tracker.Observe(cpaapi.UsageRecord{
		AuthIndex: "overdraft", RequestedAt: now, Detail: cpaapi.UsageDetail{TotalTokens: 100},
		ResponseHeaders: exhaustedHeaders,
	})
	boundary := tracker.Snapshot("overdraft")
	if boundary == nil || boundary.Codex == nil || boundary.Codex.FiveHour == nil {
		t.Fatalf("boundary snapshot = %#v", boundary)
	}
	if boundary.Codex.FiveHour.OverdraftActive || boundary.Codex.FiveHour.OverdraftTokens != 0 || boundary.Codex.FiveHour.OverdraftRequests != 0 {
		t.Fatalf("reaching 100%% started overdraft without a failed normal probe: %#v", boundary.Codex.FiveHour)
	}

	now = now.Add(time.Minute)
	tracker.BeginOverdraftCycle("overdraft", InspectionQuotaWindowFiveHour, now)
	started := tracker.Snapshot("overdraft").Codex.FiveHour
	wantRecoverAt := now.Add(5 * time.Hour)
	if !started.OverdraftActive || started.OverdraftStartedAt == nil || !started.OverdraftStartedAt.Equal(now) ||
		started.OverdraftRecoverAt == nil || !started.OverdraftRecoverAt.Equal(wantRecoverAt) ||
		started.OverdraftTokens != 0 || started.OverdraftRequests != 0 {
		t.Fatalf("started overdraft cycle = %#v, want frozen zero delta through %v", started, wantRecoverAt)
	}
	tracker.BeginOverdraftCycle("overdraft", InspectionQuotaWindowFiveHour, now.Add(time.Hour))
	if repeated := tracker.Snapshot("overdraft").Codex.FiveHour; repeated.OverdraftRecoverAt == nil || !repeated.OverdraftRecoverAt.Equal(wantRecoverAt) {
		t.Fatalf("repeated normal failure moved recovery from %v: %#v", wantRecoverAt, repeated)
	}
	tracker.Observe(cpaapi.UsageRecord{
		AuthIndex: "overdraft", RequestedAt: now, Detail: cpaapi.UsageDetail{InputTokens: 70, OutputTokens: 30},
	})
	tracker.Observe(cpaapi.UsageRecord{
		AuthIndex: "overdraft", RequestedAt: now, Failed: true, Detail: cpaapi.UsageDetail{TotalTokens: 999},
	})
	afterSuccess := tracker.Snapshot("overdraft")
	window := afterSuccess.Codex.FiveHour
	if window.OverdraftTokens != 100 || window.OverdraftRequests != 1 {
		t.Fatalf("measured overdraft = tokens:%d requests:%d, want 100/1", window.OverdraftTokens, window.OverdraftRequests)
	}
	if afterSuccess.Codex.SevenDay.OverdraftActive || afterSuccess.Codex.SevenDay.OverdraftTokens != 0 || afterSuccess.Codex.SevenDay.OverdraftRequests != 0 {
		t.Fatalf("non-exhausted 7d window counted overdraft: %#v", afterSuccess.Codex.SevenDay)
	}

	now = now.Add(time.Minute)
	sameWindowHeaders := exhaustedHeaders.Clone()
	sameWindowHeaders.Set("X-Codex-Secondary-Reset-After-Seconds", "3480")
	tracker.Observe(cpaapi.UsageRecord{
		AuthIndex: "overdraft", RequestedAt: now, Detail: cpaapi.UsageDetail{TotalTokens: 25},
		ResponseHeaders: sameWindowHeaders,
	})
	tracker.Observe(cpaapi.UsageRecord{AuthIndex: "overdraft", RequestedAt: now})
	continued := tracker.Snapshot("overdraft")
	if continued.Codex.FiveHour.OverdraftTokens != 125 || continued.Codex.FiveHour.OverdraftRequests != 3 {
		t.Fatalf("same-window overdraft was not preserved: %#v", continued.Codex.FiveHour)
	}
	if continued.Codex.FiveHour.OverdraftRecoverAt == nil || !continued.Codex.FiveHour.OverdraftRecoverAt.Equal(wantRecoverAt) {
		t.Fatalf("later usage response moved frozen recovery time: %#v", continued.Codex.FiveHour)
	}

	now = now.Add(time.Minute)
	recoveredHeaders := exhaustedHeaders.Clone()
	recoveredHeaders.Set("X-Codex-Secondary-Used-Percent", "0")
	recoveredHeaders.Set("X-Codex-Secondary-Reset-After-Seconds", "18000")
	tracker.Observe(cpaapi.UsageRecord{
		AuthIndex: "overdraft", RequestedAt: now, Detail: cpaapi.UsageDetail{TotalTokens: 10},
		ResponseHeaders: recoveredHeaders,
	})
	recovered := tracker.Snapshot("overdraft")
	if recovered.Codex.FiveHour.UsedPercent != 0 || recovered.Codex.FiveHour.OverdraftActive || recovered.Codex.FiveHour.OverdraftTokens != 0 || recovered.Codex.FiveHour.OverdraftRequests != 0 {
		t.Fatalf("recovered quota retained prior overdraft: %#v", recovered.Codex.FiveHour)
	}
	tracker.Close()

	restored := NewUsageTracker()
	defer restored.Close()
	restored.now = func() time.Time { return now.Add(time.Minute) }
	restored.Configure(Config{DataDir: dataDir})
	reloaded := restored.Snapshot("overdraft")
	if reloaded == nil || reloaded.Codex == nil || reloaded.Codex.FiveHour == nil ||
		reloaded.Codex.FiveHour.OverdraftTokens != 0 || reloaded.Codex.FiveHour.OverdraftRequests != 0 {
		t.Fatalf("persisted recovered quota resurrected prior overdraft: %#v", reloaded)
	}
}

func TestUsageTrackerPersistsFrozenOverdraftBaseline(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Date(2026, time.July, 30, 1, 0, 0, 0, time.UTC)
	first := NewUsageTracker()
	first.now = func() time.Time { return now }
	first.persistDelay = time.Hour
	first.Configure(Config{DataDir: dataDir})
	headers := http.Header{
		"X-Codex-Secondary-Used-Percent":        []string{"100"},
		"X-Codex-Secondary-Reset-After-Seconds": []string{"3600"},
		"X-Codex-Secondary-Window-Minutes":      []string{"300"},
	}
	first.Observe(cpaapi.UsageRecord{AuthIndex: "persist-overdraft", RequestedAt: now, ResponseHeaders: headers})
	now = now.Add(time.Minute)
	first.BeginOverdraftCycle("persist-overdraft", InspectionQuotaWindowFiveHour, now)
	first.Observe(cpaapi.UsageRecord{AuthIndex: "persist-overdraft", RequestedAt: now, Detail: cpaapi.UsageDetail{TotalTokens: 321}})
	first.Close()

	second := NewUsageTracker()
	defer second.Close()
	second.now = func() time.Time { return now.Add(time.Minute) }
	second.Configure(Config{DataDir: dataDir})
	snapshot := second.Snapshot("persist-overdraft")
	if snapshot == nil || snapshot.Codex == nil || snapshot.Codex.FiveHour == nil ||
		!snapshot.Codex.FiveHour.OverdraftActive || snapshot.Codex.FiveHour.OverdraftTokens != 321 || snapshot.Codex.FiveHour.OverdraftRequests != 1 ||
		snapshot.Codex.FiveHour.OverdraftRecoverAt == nil || !snapshot.Codex.FiveHour.OverdraftRecoverAt.Equal(now.Add(5*time.Hour)) {
		t.Fatalf("persisted overdraft snapshot = %#v", snapshot)
	}
}

func TestUsageTrackerMigratesCounterOnlyOverdraftStateAsInactive(t *testing.T) {
	dataDir := t.TempDir()
	storePath := usageStorePath(dataDir)
	legacy := []byte(`{"version":2,"accounts":{"auth-index:legacy-overdraft":{"total_tokens":900,"codex":{"five_hour":{"used_percent":100,"window_minutes":300,"overdraft_tokens":450,"overdraft_requests":9},"observed_at":"2026-07-30T00:00:00Z"},"updated_at":"2026-07-30T00:00:00Z"}}}`)
	if errWrite := os.WriteFile(storePath, legacy, 0o600); errWrite != nil {
		t.Fatalf("write version-two usage state: %v", errWrite)
	}
	tracker := NewUsageTracker()
	tracker.now = func() time.Time { return time.Date(2026, time.July, 30, 0, 1, 0, 0, time.UTC) }
	tracker.persistDelay = time.Hour
	tracker.Configure(Config{DataDir: dataDir})
	snapshot := tracker.Snapshot("legacy-overdraft")
	if snapshot == nil || snapshot.Codex == nil || snapshot.Codex.FiveHour == nil {
		t.Fatalf("migrated version-two snapshot = %#v", snapshot)
	}
	if snapshot.Codex.FiveHour.OverdraftActive || snapshot.Codex.FiveHour.OverdraftTokens != 0 || snapshot.Codex.FiveHour.OverdraftRequests != 0 {
		t.Fatalf("counter-only state became an active baseline cycle: %#v", snapshot.Codex.FiveHour)
	}
	tracker.Close()
}

func TestStoppedOverdraftCycleWinsPersistenceMerge(t *testing.T) {
	startedAt := time.Date(2026, time.July, 30, 3, 0, 0, 0, time.UTC)
	active := &overdraftCycleState{
		Active: true, BaselineTokens: 100, BaselineRequests: 2, StartedAt: startedAt,
		RecoverAt: startedAt.Add(5 * time.Hour), WindowMinutes: 300, ChangedAt: startedAt,
	}
	stopped := &overdraftCycleState{Active: false, ChangedAt: startedAt.Add(time.Minute)}
	for _, merged := range []*overdraftCycleState{mergeOverdraftCycle(active, stopped), mergeOverdraftCycle(stopped, active)} {
		if merged == nil || merged.Active || !merged.ChangedAt.Equal(stopped.ChangedAt) {
			t.Fatalf("stopped cycle was resurrected during merge: %#v", merged)
		}
	}
}

func TestAccountQuotaLimitedUsesFrozenOverdraftRecoveryTime(t *testing.T) {
	now := time.Date(2026, time.July, 30, 4, 0, 0, 0, time.UTC)
	mutableResetAt := now.Add(6 * time.Hour)
	frozenRecoverAt := now.Add(5 * time.Hour)
	limited, recoverAt, quotaWindow := accountQuotaLimited(Account{Usage: &AccountUsageSnapshot{Codex: &CodexUsageSnapshot{
		FiveHour: &UsageWindowSnapshot{
			UsedPercent: 100, ResetAt: &mutableResetAt, WindowMinutes: 300,
			OverdraftActive: true, OverdraftRecoverAt: &frozenRecoverAt,
		},
	}}}, now)
	if !limited || quotaWindow != InspectionQuotaWindowFiveHour || !recoverAt.Equal(frozenRecoverAt) {
		t.Fatalf("quota recovery = limited:%t window:%q at:%v, want frozen %v", limited, quotaWindow, recoverAt, frozenRecoverAt)
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

func TestUsageTrackerUsesEmailIdentityAcrossAuthIndexChanges(t *testing.T) {
	tracker := NewUsageTracker()
	defer tracker.Close()
	tracker.persistDelay = time.Hour
	tracker.Configure(Config{DataDir: t.TempDir()})
	tracker.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{{
		AuthIndex: "old-index", Provider: "codex", Type: "codex", Email: " Person@Example.com ",
	}})
	tracker.Observe(cpaapi.UsageRecord{AuthIndex: "old-index", Detail: cpaapi.UsageDetail{TotalTokens: 41}})

	tracker.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{{
		AuthIndex: "new-index", Provider: "codex", Type: "codex", Email: "person@example.com",
	}})
	if snapshot := tracker.Snapshot("new-index"); snapshot == nil || snapshot.TotalTokens != 41 {
		t.Fatalf("usage did not follow normalized email identity: %#v", snapshot)
	}
	if snapshot := tracker.Snapshot("old-index"); snapshot != nil {
		t.Fatalf("retired AuthIndex still resolved usage: %#v", snapshot)
	}
}

func TestUsageTrackerPersistsAccountLifecycleTransitions(t *testing.T) {
	dataDir := t.TempDir()
	createdAt := time.Date(2026, time.July, 1, 8, 0, 0, 0, time.UTC)
	now := time.Date(2026, time.July, 31, 8, 0, 0, 0, time.UTC)
	tracker := NewUsageTracker()
	tracker.now = func() time.Time { return now }
	tracker.persistDelay = time.Hour
	tracker.Configure(Config{DataDir: dataDir})
	entry := usageIdentityTestEntry(t, "lifecycle-enabled", "lifecycle@example.com", "lifecycle-account")
	entry.CreatedAt = createdAt
	entry.UpdatedAt = createdAt.Add(time.Hour)
	tracker.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{entry})
	assertAccountLifecycle(t, tracker.AccountLifecycle("lifecycle-enabled"), createdAt, nil)

	now = now.Add(time.Hour)
	disabledAt := now.Add(-time.Minute)
	entry.AuthIndex = "lifecycle-disabled"
	entry.Disabled = true
	entry.UpdatedAt = disabledAt.Add(-time.Hour)
	entry.ModTime = disabledAt
	tracker.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{entry})
	assertAccountLifecycle(t, tracker.AccountLifecycle("lifecycle-disabled"), createdAt, &disabledAt)

	now = now.Add(time.Hour)
	entry.AuthIndex = "lifecycle-reenabled"
	entry.Disabled = false
	entry.UpdatedAt = now
	entry.ModTime = now
	tracker.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{entry})
	assertAccountLifecycle(t, tracker.AccountLifecycle("lifecycle-reenabled"), createdAt, nil)

	now = now.Add(time.Hour)
	secondDisabledAt := now
	entry.AuthIndex = "lifecycle-disabled-again"
	entry.Disabled = true
	entry.UpdatedAt = secondDisabledAt
	entry.ModTime = secondDisabledAt
	tracker.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{entry})
	assertAccountLifecycle(t, tracker.AccountLifecycle("lifecycle-disabled-again"), createdAt, &secondDisabledAt)
	tracker.Close()

	restored := NewUsageTracker()
	defer restored.Close()
	restored.now = func() time.Time { return now.Add(time.Minute) }
	restored.Configure(Config{DataDir: dataDir})
	restored.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{entry})
	assertAccountLifecycle(t, restored.AccountLifecycle("lifecycle-disabled-again"), createdAt, &secondDisabledAt)
}

func TestUsageTrackerUsesFirstObservationForMissingLifecycleTimes(t *testing.T) {
	now := time.Date(2026, time.July, 31, 10, 0, 0, 0, time.UTC)
	tracker := NewUsageTracker()
	defer tracker.Close()
	tracker.now = func() time.Time { return now }
	tracker.persistDelay = time.Hour
	tracker.Configure(Config{DataDir: t.TempDir()})
	entry := usageIdentityTestEntry(t, "missing-times", "missing-times@example.com", "missing-times-account")
	entry.Disabled = true
	tracker.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{entry})
	assertAccountLifecycle(t, tracker.AccountLifecycle("missing-times"), now, &now)

	now = now.Add(24 * time.Hour)
	tracker.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{entry})
	assertAccountLifecycle(t, tracker.AccountLifecycle("missing-times"), now.Add(-24*time.Hour), timePointer(now.Add(-24*time.Hour)))
}

func assertAccountLifecycle(t *testing.T, snapshot AccountLifecycleSnapshot, createdAt time.Time, disabledAt *time.Time) {
	t.Helper()
	if snapshot.CreatedAt == nil || !snapshot.CreatedAt.Equal(createdAt) {
		t.Fatalf("created_at = %v, want %v", snapshot.CreatedAt, createdAt)
	}
	if disabledAt == nil {
		if snapshot.DisabledAt != nil {
			t.Fatalf("disabled_at = %v, want nil", snapshot.DisabledAt)
		}
		return
	}
	if snapshot.DisabledAt == nil || !snapshot.DisabledAt.Equal(*disabledAt) {
		t.Fatalf("disabled_at = %v, want %v", snapshot.DisabledAt, *disabledAt)
	}
}

func TestUsageTrackerPreservesActiveOverdraftAcrossDisabledTeamIdentityRebind(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Date(2026, time.July, 30, 6, 0, 0, 0, time.UTC)
	tracker := NewUsageTracker()
	tracker.now = func() time.Time { return now }
	tracker.persistDelay = time.Hour
	tracker.Configure(Config{DataDir: dataDir})
	tracker.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{
		usageIdentityTestEntry(t, "enabled-index", "member@example.com", "team-workspace-before-disable"),
	})
	tracker.Observe(cpaapi.UsageRecord{
		AuthIndex: "enabled-index", RequestedAt: now, Detail: cpaapi.UsageDetail{TotalTokens: 1_000},
		ResponseHeaders: http.Header{
			"X-Codex-Primary-Used-Percent":        []string{"100"},
			"X-Codex-Primary-Window-Minutes":      []string{"10080"},
			"X-Codex-Primary-Reset-After-Seconds": []string{"3600"},
		},
	})
	now = now.Add(time.Minute)
	tracker.BeginOverdraftCycle("enabled-index", InspectionQuotaWindowSevenDay, now)
	tracker.Observe(cpaapi.UsageRecord{
		AuthIndex: "enabled-index", RequestedAt: now, Detail: cpaapi.UsageDetail{TotalTokens: 381_080},
	})

	disabledEntry := usageIdentityTestEntry(t, "disabled-index", "MEMBER@example.com", "team-workspace-after-disable")
	disabledEntry.Disabled = true
	tracker.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{disabledEntry})

	snapshot := tracker.Snapshot("disabled-index")
	if snapshot == nil || snapshot.TotalTokens != 382_080 || snapshot.Codex == nil || snapshot.Codex.SevenDay == nil ||
		!snapshot.Codex.SevenDay.OverdraftActive || snapshot.Codex.SevenDay.OverdraftTokens != 381_080 ||
		snapshot.Codex.SevenDay.OverdraftRequests != 1 {
		t.Fatalf("disabled Team account lost active overdraft after identity rebind: %#v", snapshot)
	}
	tracker.Close()

	restored := NewUsageTracker()
	defer restored.Close()
	restored.now = func() time.Time { return now.Add(time.Minute) }
	restored.Configure(Config{DataDir: dataDir})
	restored.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{disabledEntry})
	reloaded := restored.Snapshot("disabled-index")
	if reloaded == nil || reloaded.Codex == nil || reloaded.Codex.SevenDay == nil ||
		!reloaded.Codex.SevenDay.OverdraftActive || reloaded.Codex.SevenDay.OverdraftTokens != 381_080 {
		t.Fatalf("disabled Team account lost persisted overdraft after reload: %#v", reloaded)
	}
}

func TestUsageTrackerDoesNotCarryUsageAcrossEmailReplacement(t *testing.T) {
	tracker := NewUsageTracker()
	defer tracker.Close()
	tracker.persistDelay = time.Hour
	tracker.Configure(Config{DataDir: t.TempDir()})
	tracker.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{{
		AuthIndex: "stable-index", Provider: "codex", Type: "codex", Email: "old@example.com",
	}})
	tracker.Observe(cpaapi.UsageRecord{AuthIndex: "stable-index", Detail: cpaapi.UsageDetail{TotalTokens: 90}})

	tracker.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{{
		AuthIndex: "stable-index", Provider: "codex", Type: "codex", Email: "new@example.com",
	}})
	if snapshot := tracker.Snapshot("stable-index"); snapshot != nil {
		t.Fatalf("replacement account inherited old usage: %#v", snapshot)
	}
	tracker.Observe(cpaapi.UsageRecord{AuthIndex: "stable-index", Detail: cpaapi.UsageDetail{TotalTokens: 7}})
	if snapshot := tracker.Snapshot("stable-index"); snapshot == nil || snapshot.TotalTokens != 7 {
		t.Fatalf("replacement account usage = %#v, want 7 tokens", snapshot)
	}
}

func TestUsageTrackerUsesHashedAccountIDToRejectSameEmailCollision(t *testing.T) {
	dataDir := t.TempDir()
	tracker := NewUsageTracker()
	tracker.persistDelay = time.Hour
	tracker.Configure(Config{DataDir: dataDir})
	tracker.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{usageIdentityTestEntry(t, "shared-index", "same@example.com", "account-old")})
	tracker.Observe(cpaapi.UsageRecord{AuthIndex: "shared-index", Detail: cpaapi.UsageDetail{TotalTokens: 55}})

	tracker.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{usageIdentityTestEntry(t, "shared-index", "same@example.com", "account-new")})
	if snapshot := tracker.Snapshot("shared-index"); snapshot != nil {
		t.Fatalf("different upstream account ID inherited usage: %#v", snapshot)
	}
	tracker.Close()

	raw, errRead := os.ReadFile(usageStorePath(dataDir))
	if errRead != nil {
		t.Fatalf("read identity-aware usage state: %v", errRead)
	}
	for _, private := range []string{"same@example.com", "account-old", "account-new"} {
		if bytes.Contains(raw, []byte(private)) {
			t.Fatalf("usage state persisted raw identity %q: %s", private, raw)
		}
	}
}

func TestUsageTrackerSuppressesAmbiguousAuthIndexUsage(t *testing.T) {
	tracker := NewUsageTracker()
	defer tracker.Close()
	tracker.persistDelay = time.Hour
	tracker.Configure(Config{DataDir: t.TempDir()})
	tracker.Observe(cpaapi.UsageRecord{AuthIndex: "duplicate", Detail: cpaapi.UsageDetail{TotalTokens: 77}})
	tracker.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{
		{AuthIndex: "duplicate", Provider: "codex", Type: "codex", Email: "first@example.com"},
		{AuthIndex: "duplicate", Provider: "codex", Type: "codex", Email: "second@example.com"},
	})
	if snapshot := tracker.Snapshot("duplicate"); snapshot != nil {
		t.Fatalf("ambiguous AuthIndex exposed usage: %#v", snapshot)
	}
}

func TestUsageTrackerSuppressesSameEmailWithConflictingAccountIDs(t *testing.T) {
	tracker := NewUsageTracker()
	defer tracker.Close()
	tracker.persistDelay = time.Hour
	tracker.Configure(Config{DataDir: t.TempDir()})
	tracker.Observe(cpaapi.UsageRecord{AuthIndex: "first-index", Detail: cpaapi.UsageDetail{TotalTokens: 44}})
	tracker.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{
		{AuthIndex: "missing-id-index", Provider: "codex", Type: "codex", Email: "shared@example.com"},
		usageIdentityTestEntry(t, "first-index", "shared@example.com", "account-first"),
		usageIdentityTestEntry(t, "second-index", "shared@example.com", "account-second"),
	})
	for _, authIndex := range []string{"missing-id-index", "first-index", "second-index"} {
		if identity := tracker.UsageIdentity(authIndex); identity != "" {
			t.Fatalf("conflicting email identity %s unexpectedly bound to %q", authIndex, identity)
		}
		if snapshot := tracker.Snapshot(authIndex); snapshot != nil {
			t.Fatalf("conflicting email identity %s exposed usage: %#v", authIndex, snapshot)
		}
	}
}

func TestUsageTrackerMigratesVersionOneStateIntoCurrentEmailIdentity(t *testing.T) {
	dataDir := t.TempDir()
	storePath := usageStorePath(dataDir)
	legacy := []byte(`{"version":1,"accounts":{"legacy-index":{"total_tokens":63,"updated_at":"2026-07-26T00:00:00Z"}}}`)
	if errWrite := os.WriteFile(storePath, legacy, 0o600); errWrite != nil {
		t.Fatalf("write legacy usage state: %v", errWrite)
	}
	tracker := NewUsageTracker()
	tracker.persistDelay = time.Hour
	tracker.Configure(Config{DataDir: dataDir})
	tracker.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{{
		AuthIndex: "legacy-index", Provider: "codex", Type: "codex", Email: "legacy@example.com",
	}})
	if snapshot := tracker.Snapshot("legacy-index"); snapshot == nil || snapshot.TotalTokens != 63 {
		t.Fatalf("migrated usage snapshot = %#v, want 63 tokens", snapshot)
	}
	tracker.Close()

	raw, errRead := os.ReadFile(storePath)
	if errRead != nil {
		t.Fatalf("read migrated usage state: %v", errRead)
	}
	if !bytes.Contains(raw, []byte(`"version":4`)) || bytes.Contains(raw, []byte("legacy@example.com")) {
		t.Fatalf("legacy state was not safely migrated: %s", raw)
	}
}

func TestUsageTrackerMigratesVersionThreeStateWithoutLosingUsage(t *testing.T) {
	dataDir := t.TempDir()
	storePath := usageStorePath(dataDir)
	baseline := []byte(`{"version":3,"accounts":{"auth-index:baseline-index":{"total_tokens":91,"successful_tokens":73,"updated_at":"2026-07-30T00:00:00Z","five_hour_overdraft":{"active":true,"baseline_tokens":73,"started_at":"2026-07-30T00:00:00Z","recover_at":"2026-07-30T05:00:00Z","window_minutes":300,"changed_at":"2026-07-30T00:00:00Z"}}}}`)
	if errWrite := os.WriteFile(storePath, baseline, 0o600); errWrite != nil {
		t.Fatalf("write version-three usage state: %v", errWrite)
	}
	tracker := NewUsageTracker()
	tracker.persistDelay = time.Hour
	tracker.Configure(Config{DataDir: dataDir})
	tracker.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{{
		AuthIndex: "baseline-index", Provider: "codex", Type: "codex", Email: "baseline@example.com",
	}})
	snapshot := tracker.Snapshot("baseline-index")
	if snapshot == nil || snapshot.TotalTokens != 91 {
		t.Fatalf("version-three usage snapshot = %#v, want existing counters", snapshot)
	}
	tracker.Close()

	raw, errRead := os.ReadFile(storePath)
	if errRead != nil {
		t.Fatalf("read migrated version-three usage state: %v", errRead)
	}
	if !bytes.Contains(raw, []byte(`"version":4`)) || !bytes.Contains(raw, []byte(`"total_tokens":91`)) ||
		!bytes.Contains(raw, []byte(`"successful_tokens":73`)) || !bytes.Contains(raw, []byte(`"five_hour_overdraft"`)) {
		t.Fatalf("version-three state was not preserved during migration: %s", raw)
	}
}

func TestUsagePersistenceMergesLifecycleByEarliestCreationAndLatestTransition(t *testing.T) {
	createdEarly := time.Date(2026, time.July, 1, 8, 0, 0, 0, time.UTC)
	createdLate := createdEarly.Add(24 * time.Hour)
	disabledAt := time.Date(2026, time.July, 30, 8, 0, 0, 0, time.UTC)
	reenabledAt := disabledAt.Add(time.Hour)
	disabled := &accountLifecycleState{
		CreatedAt: createdLate, Disabled: true, DisabledAt: disabledAt, StateChangedAt: disabledAt,
	}
	enabled := &accountLifecycleState{
		CreatedAt: createdEarly, Disabled: false, StateChangedAt: reenabledAt,
	}
	merged := mergeAccountLifecycle(disabled, enabled)
	if merged == nil || !merged.CreatedAt.Equal(createdEarly) || merged.Disabled || !merged.StateChangedAt.Equal(reenabledAt) || !merged.DisabledAt.IsZero() {
		t.Fatalf("merged lifecycle = %#v, want earliest creation and latest enabled transition", merged)
	}

	// Equal transition timestamps favor enabled state so a stale instance cannot
	// resurrect a disabled period after another instance has enabled the account.
	enabled.StateChangedAt = disabledAt
	merged = mergeAccountLifecycle(disabled, enabled)
	if merged == nil || merged.Disabled || !merged.CreatedAt.Equal(createdEarly) || !merged.DisabledAt.IsZero() {
		t.Fatalf("equal-timestamp lifecycle merge = %#v, want enabled state", merged)
	}
}

func usageIdentityTestEntry(t *testing.T, authIndex, email, accountID string) cpaapi.HostAuthFileEntry {
	t.Helper()
	var listed cpaapi.HostAuthListResponse
	raw := fmt.Sprintf(`{"files":[{"auth_index":%q,"provider":"codex","type":"codex","email":%q,"id_token":{"chatgpt_account_id":%q}}]}`,
		authIndex, email, accountID)
	if errDecode := json.Unmarshal([]byte(raw), &listed); errDecode != nil || len(listed.Files) != 1 {
		t.Fatalf("decode usage identity fixture: files=%#v err=%v", listed.Files, errDecode)
	}
	return listed.Files[0]
}

func TestUsagePersistenceMergesOverlappingPluginInstances(t *testing.T) {
	dataDir := t.TempDir()
	seed := NewUsageTracker()
	seed.persistDelay = time.Hour
	seed.Configure(Config{DataDir: dataDir})
	seed.Observe(cpaapi.UsageRecord{AuthIndex: "shared", Detail: cpaapi.UsageDetail{TotalTokens: 10}})
	seed.Close()

	oldInstance := NewUsageTracker()
	oldInstance.persistDelay = time.Hour
	oldInstance.Configure(Config{DataDir: dataDir})
	replacement := NewUsageTracker()
	replacement.persistDelay = time.Hour
	replacement.Configure(Config{DataDir: dataDir})

	oldInstance.Observe(cpaapi.UsageRecord{AuthIndex: "shared", Detail: cpaapi.UsageDetail{TotalTokens: 5}})
	replacement.Observe(cpaapi.UsageRecord{AuthIndex: "replacement-only", Detail: cpaapi.UsageDetail{TotalTokens: 7}})
	oldInstance.Close()
	replacement.Close()

	restored := NewUsageTracker()
	defer restored.Close()
	restored.Configure(Config{DataDir: dataDir})
	shared := restored.Snapshot("shared")
	replacementOnly := restored.Snapshot("replacement-only")
	if shared == nil || shared.TotalTokens != 15 {
		t.Fatalf("shared usage after overlapping replacement = %#v, want 15 tokens", shared)
	}
	if replacementOnly == nil || replacementOnly.TotalTokens != 7 {
		t.Fatalf("replacement usage after overlapping replacement = %#v, want 7 tokens", replacementOnly)
	}
}

func TestUsagePersistenceRecoversLastGoodBackup(t *testing.T) {
	dataDir := t.TempDir()
	first := NewUsageTracker()
	first.persistDelay = time.Hour
	first.Configure(Config{DataDir: dataDir})
	first.Observe(cpaapi.UsageRecord{AuthIndex: "recoverable", Detail: cpaapi.UsageDetail{TotalTokens: 42}})
	first.Close()

	storePath := usageStorePath(dataDir)
	if errWrite := os.WriteFile(storePath, []byte(`{"version":1,"accounts":`), 0o600); errWrite != nil {
		t.Fatalf("corrupt primary usage state: %v", errWrite)
	}

	restored := NewUsageTracker()
	defer restored.Close()
	restored.Configure(Config{DataDir: dataDir})
	snapshot := restored.Snapshot("recoverable")
	if snapshot == nil || snapshot.TotalTokens != 42 {
		t.Fatalf("backup-restored usage = %#v, want 42 tokens", snapshot)
	}
}

func TestUsagePersistenceRestoresFromAuthStorageAcrossCPAUpgrade(t *testing.T) {
	authDir := t.TempDir()
	authName := "upgrade-account.json"
	authPath := filepath.Join(authDir, authName)
	if errWrite := os.WriteFile(authPath, []byte(`{"type":"codex","email":"upgrade@example.com"}`), 0o600); errWrite != nil {
		t.Fatalf("write auth fixture: %v", errWrite)
	}
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "upgrade-index", Name: authName, Provider: "codex", Type: "codex",
			Source: "file", Path: authPath, Email: "upgrade@example.com",
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"upgrade-index": {AuthIndex: "upgrade-index", Name: authName, Path: authPath, JSON: json.RawMessage(`{"type":"codex","email":"upgrade@example.com"}`)},
		},
	}
	now := time.Date(2026, time.July, 24, 8, 0, 0, 0, time.UTC)
	oldCoreData := t.TempDir()
	oldInstance := NewUsageTracker()
	oldInstance.now = func() time.Time { return now }
	oldInstance.persistDelay = time.Hour
	oldInstance.Configure(Config{DataDir: oldCoreData, implicitDataDir: true})
	oldInstance.Observe(cpaapi.UsageRecord{
		AuthIndex: "upgrade-index", AuthID: "secret-auth-id", APIKey: "sk-secret-key",
		Failure: cpaapi.UsageFailure{Body: "Bearer secret-response"},
		Detail:  cpaapi.UsageDetail{TotalTokens: 73},
		ResponseHeaders: http.Header{
			"Authorization":                       []string{"Bearer secret-header"},
			"X-Codex-Primary-Used-Percent":        []string{"64"},
			"X-Codex-Primary-Window-Minutes":      []string{"10080"},
			"X-Codex-Primary-Reset-After-Seconds": []string{"3600"},
		},
	})
	if _, errList := NewAccountService(host, oldInstance).List(t.Context(), ListQuery{Page: 1, PageSize: 20}); errList != nil {
		t.Fatalf("prime durable storage from account list: %v", errList)
	}
	oldInstance.Close()

	resolvedAuthDir, errResolve := filepath.EvalSymlinks(authDir)
	if errResolve != nil {
		t.Fatalf("resolve auth directory: %v", errResolve)
	}
	durablePath := durableUsageStorePath(resolvedAuthDir)
	if filepath.Ext(durablePath) == ".json" {
		t.Fatalf("durable usage path %q must not look like an auth JSON file", durablePath)
	}
	for _, path := range []string{durablePath, usageStoreBackupPath(durablePath)} {
		raw, errRead := os.ReadFile(path)
		if errRead != nil {
			t.Fatalf("read durable usage state %q: %v", filepath.Base(path), errRead)
		}
		for _, secret := range []string{authPath, "secret-auth-id", "sk-secret-key", "secret-response", "secret-header", "Authorization"} {
			if bytes.Contains(raw, []byte(secret)) {
				t.Fatalf("durable usage state leaked %q: %s", secret, raw)
			}
		}
	}

	newCoreData := t.TempDir()
	upgradedInstance := NewUsageTracker()
	defer upgradedInstance.Close()
	upgradedInstance.now = func() time.Time { return now.Add(time.Minute) }
	upgradedInstance.Configure(Config{DataDir: newCoreData, implicitDataDir: true})
	response, errList := NewAccountService(host, upgradedInstance).List(t.Context(), ListQuery{Page: 1, PageSize: 20})
	if errList != nil {
		t.Fatalf("list accounts after CPA upgrade: %v", errList)
	}
	if len(response.Accounts) != 1 || response.Accounts[0].Usage == nil || response.Accounts[0].Usage.TotalTokens != 73 {
		t.Fatalf("first account list after CPA upgrade = %#v", response.Accounts)
	}
	if response.Accounts[0].Usage.Codex == nil || response.Accounts[0].Usage.Codex.SevenDay == nil || response.Accounts[0].Usage.Codex.SevenDay.UsedPercent != 64 {
		t.Fatalf("restored Codex usage = %#v", response.Accounts[0].Usage)
	}
	upgradedInstance.Configure(Config{DataDir: newCoreData, implicitDataDir: true})
	upgradedInstance.Observe(cpaapi.UsageRecord{AuthIndex: "upgrade-index", Detail: cpaapi.UsageDetail{TotalTokens: 2}})
	upgradedInstance.mu.RLock()
	storePath := upgradedInstance.store
	upgradedInstance.mu.RUnlock()
	if storePath != durablePath {
		t.Fatalf("default reconfiguration changed durable usage store to %q", storePath)
	}
	if _, errStat := os.Stat(usageStorePath(newCoreData)); !errors.Is(errStat, os.ErrNotExist) {
		t.Fatalf("new CPA working data unexpectedly became the usage authority: %v", errStat)
	}
}

func TestUsagePersistenceKeepsExplicitDataDirAuthoritative(t *testing.T) {
	authDir := t.TempDir()
	authName := "explicit-account.json"
	authPath := filepath.Join(authDir, authName)
	if errWrite := os.WriteFile(authPath, []byte(`{"type":"codex"}`), 0o600); errWrite != nil {
		t.Fatalf("write auth fixture: %v", errWrite)
	}
	explicitDataDir := t.TempDir()
	tracker := NewUsageTracker()
	tracker.persistDelay = time.Hour
	tracker.Configure(Config{DataDir: explicitDataDir})
	tracker.Observe(cpaapi.UsageRecord{AuthIndex: "explicit-index", Detail: cpaapi.UsageDetail{TotalTokens: 19}})
	tracker.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{{
		AuthIndex: "explicit-index", Name: authName, Source: "file", Path: authPath,
	}})
	tracker.Close()

	if _, errStat := os.Stat(usageStorePath(explicitDataDir)); errStat != nil {
		t.Fatalf("explicit usage store was not written: %v", errStat)
	}
	if _, errStat := os.Stat(durableUsageStorePath(authDir)); !errors.Is(errStat, os.ErrNotExist) {
		t.Fatalf("explicit data_dir unexpectedly wrote an auth-directory mirror: %v", errStat)
	}
}

func TestUsagePersistenceRejectsAmbiguousAuthDirectories(t *testing.T) {
	leftDir := t.TempDir()
	rightDir := t.TempDir()
	leftPath := filepath.Join(leftDir, "left.json")
	rightPath := filepath.Join(rightDir, "right.json")
	for path, raw := range map[string]string{leftPath: `{"type":"codex"}`, rightPath: `{"type":"codex"}`} {
		if errWrite := os.WriteFile(path, []byte(raw), 0o600); errWrite != nil {
			t.Fatalf("write auth fixture: %v", errWrite)
		}
	}
	dataDir := t.TempDir()
	tracker := NewUsageTracker()
	defer tracker.Close()
	tracker.Configure(Config{DataDir: dataDir, implicitDataDir: true})
	tracker.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{
		{Name: filepath.Base(leftPath), Source: "file", Path: leftPath},
		{Name: filepath.Base(rightPath), Source: "file", Path: rightPath},
	})
	tracker.mu.RLock()
	storePath := tracker.store
	tracker.mu.RUnlock()
	if storePath != usageStorePath(dataDir) {
		t.Fatalf("ambiguous auth roots selected usage store %q", storePath)
	}
}

func TestConfigTracksImplicitAndExplicitDataDirectories(t *testing.T) {
	t.Setenv("CPA_ACCOUNT_CONFIG_MANAGER_DATA_DIR", "")
	implicit := normalizeConfig(Config{})
	if !implicit.implicitDataDir || implicit.DataDir != "data/cpa-account-config-manager" {
		t.Fatalf("implicit data directory = %#v", implicit)
	}
	if normalizedAgain := normalizeConfig(implicit); !normalizedAgain.implicitDataDir || normalizedAgain.DataDir != implicit.DataDir {
		t.Fatalf("renormalized implicit data directory = %#v", normalizedAgain)
	}
	explicit := normalizeConfig(Config{DataDir: "operator-data"})
	if explicit.implicitDataDir || explicit.DataDir != "operator-data" {
		t.Fatalf("explicit data directory = %#v", explicit)
	}
	t.Setenv("CPA_ACCOUNT_CONFIG_MANAGER_DATA_DIR", "environment-data")
	environment := normalizeConfig(Config{})
	if environment.implicitDataDir || environment.DataDir != "environment-data" {
		t.Fatalf("environment data directory = %#v", environment)
	}
}

func TestUsageTrackerAcceptsAbsoluteCodexResetAt(t *testing.T) {
	now := time.Date(2026, time.July, 21, 10, 0, 0, 0, time.UTC)
	resetAt := now.Add(5 * time.Hour)
	snapshot := parseCodexUsageHeaders(http.Header{
		"X-Codex-Primary-Used-Percent":   []string{"100"},
		"X-Codex-Primary-Reset-At":       []string{strconv.FormatInt(resetAt.Unix(), 10)},
		"X-Codex-Primary-Window-Minutes": []string{"300"},
	}, now)
	if snapshot == nil || snapshot.FiveHour == nil || snapshot.FiveHour.ResetAt == nil || !snapshot.FiveHour.ResetAt.Equal(resetAt) {
		t.Fatalf("absolute reset snapshot = %#v", snapshot)
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

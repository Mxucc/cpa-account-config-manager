package manager

import (
	"bytes"
	"encoding/json"
	"errors"
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

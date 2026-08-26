package manager

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestReleaseVersionComparisonIgnoresDevelopmentSuffix(t *testing.T) {
	tests := []struct {
		current string
		latest  string
		want    bool
		ok      bool
	}{
		{current: "0.2.0-dev", latest: "v0.3.0", want: true, ok: true},
		{current: "0.2.0-dev", latest: "v0.2.0", want: false, ok: true},
		{current: "1.2.3", latest: "v1.2.2", want: false, ok: true},
		{current: "development", latest: "v1.0.0", want: false, ok: false},
	}
	for _, test := range tests {
		got, _, ok := releaseVersionNewer(test.current, test.latest)
		if got != test.want || ok != test.ok {
			t.Fatalf("releaseVersionNewer(%q, %q) = %v, _, %v", test.current, test.latest, got, ok)
		}
	}
}

func TestUpdateCheckerRecordsPluginStoreCheckWithoutReleaseMetadata(t *testing.T) {
	now := time.Date(2026, time.July, 20, 14, 0, 0, 0, time.UTC)
	checker := NewUpdateChecker("0.2.0-dev")
	checker.now = func() time.Time { return now }
	checker.store = filepath.Join(t.TempDir(), "update-state.json")
	snapshot := checker.RequestCheck()
	if snapshot.LatestVersion != "" || snapshot.UpdateAvailable || snapshot.ReleaseURL != "" || snapshot.Error != "" || !snapshot.CheckedAt.Equal(now) {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	stored, errRead := os.ReadFile(checker.store)
	if errRead != nil {
		t.Fatalf("read update state: %v", errRead)
	}
	if bytes.Contains(stored, []byte("latest_version")) || bytes.Contains(stored, []byte("release metadata")) || bytes.Contains(stored, []byte("github")) {
		t.Fatalf("update state retained direct release metadata: %s", stored)
	}
}

func TestUpdateCheckerClearsLegacyGitHubFailureOnLoad(t *testing.T) {
	dataDir := t.TempDir()
	store := filepath.Join(dataDir, "update-state.json")
	legacy := []byte(`{"version":1,"policy":{"check_enabled":true,"check_interval_hours":24,"auto_update":false},"latest_version":"0.2.9","checked_at":"2026-07-20T14:00:00Z","error":"release metadata request failed"}`)
	if errWrite := os.WriteFile(store, legacy, 0o600); errWrite != nil {
		t.Fatalf("write legacy update state: %v", errWrite)
	}
	checker := NewUpdateChecker("0.2.9")
	checker.Configure(Config{DataDir: dataDir})
	snapshot := checker.Snapshot()
	if snapshot.Error != "" || snapshot.LatestVersion != "" || snapshot.UpdateAvailable {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	checker.RequestCheck()
	raw, errRead := os.ReadFile(store)
	if errRead != nil {
		t.Fatalf("read migrated update state: %v", errRead)
	}
	if bytes.Contains(raw, []byte("latest_version")) || bytes.Contains(raw, []byte("release metadata")) {
		t.Fatalf("legacy GitHub state was not cleared: %s", raw)
	}
}

func TestUpdateCheckerLoadsPolicyOnFirstConfigureWhenStorePathAlreadyMatches(t *testing.T) {
	dataDir := t.TempDir()
	store := filepath.Join(dataDir, "update-state.json")
	stored := []byte(`{"version":1,"policy":{"check_enabled":true,"check_interval_hours":72,"auto_update":false},"checked_at":"2026-07-20T14:00:00Z"}`)
	if errWrite := os.WriteFile(store, stored, 0o600); errWrite != nil {
		t.Fatalf("write update state: %v", errWrite)
	}
	checker := NewUpdateChecker("0.2.9")
	checker.store = store
	checker.Configure(Config{DataDir: dataDir})
	if checker.Snapshot().Policy.CheckIntervalHours != 72 {
		t.Fatalf("snapshot = %#v", checker.Snapshot())
	}
}

func TestUpdatePolicyRouteRequiresExplicitAutoUpdateConfirmation(t *testing.T) {
	app := NewApp(&fakeAuthHost{}, []byte("index"))
	app.Configure([]byte("data_dir: " + t.TempDir()))
	defer app.Close()
	body := []byte(`{"policy":{"check_enabled":true,"check_interval_hours":24,"auto_update":true}}`)
	withoutConfirmation := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut,
		Path:   "/v0/management/plugins/cpa-account-config-manager/updates",
		Body:   body,
	})
	if withoutConfirmation.StatusCode != http.StatusBadRequest || !bytes.Contains(withoutConfirmation.Body, []byte("explicit confirmation")) {
		t.Fatalf("without confirmation = %d %s", withoutConfirmation.StatusCode, withoutConfirmation.Body)
	}
	confirmed := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut,
		Path:   "/v0/management/plugins/cpa-account-config-manager/updates",
		Body:   []byte(`{"policy":{"check_enabled":true,"check_interval_hours":24,"auto_update":true},"confirm_auto_update":true}`),
	})
	if confirmed.StatusCode != http.StatusOK {
		t.Fatalf("confirmed = %d %s", confirmed.StatusCode, confirmed.Body)
	}
}

func TestUpdateCheckerConcurrentStateMutationsAreSerialized(t *testing.T) {
	dataDir := t.TempDir()
	checker := NewUpdateChecker("0.3.0")
	checker.Configure(Config{DataDir: dataDir})
	checker.now = func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }

	const workers = 24
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			if index%2 == 0 {
				policy := defaultUpdatePolicy()
				policy.CheckIntervalHours = 24 + index
				if _, errSet := checker.SetPolicy(policy); errSet != nil {
					t.Errorf("SetPolicy() error = %v", errSet)
				}
				return
			}
			checker.RequestCheck()
		}(i)
	}
	wg.Wait()

	stored, errRead := loadUpdateState(updateStorePath(dataDir))
	if errRead != nil {
		t.Fatalf("loadUpdateState() error = %v", errRead)
	}
	if stored.CheckedAt.IsZero() {
		t.Fatal("concurrent RequestCheck calls lost checked_at")
	}
	if stored.Policy.CheckIntervalHours < 24 || stored.Policy.CheckIntervalHours > 46 {
		t.Fatalf("unexpected persisted policy: %#v", stored.Policy)
	}
	if snapshot := checker.Snapshot(); snapshot.Error != "" {
		t.Fatalf("snapshot error = %q", snapshot.Error)
	}
}

func TestUpdateCheckerSuccessfulPolicySaveClearsPersistedError(t *testing.T) {
	dataDir := t.TempDir()
	store := updateStorePath(dataDir)
	state := persistedUpdateState{Version: updateStoreVersion, Policy: defaultUpdatePolicy(), Error: "update state could not be persisted"}
	if errSave := saveUpdateState(store, state); errSave != nil {
		t.Fatalf("seed update state: %v", errSave)
	}
	checker := NewUpdateChecker("0.3.0")
	checker.Configure(Config{DataDir: dataDir})
	policy := defaultUpdatePolicy()
	policy.CheckIntervalHours = 48
	if _, errSet := checker.SetPolicy(policy); errSet != nil {
		t.Fatalf("SetPolicy() error = %v", errSet)
	}
	stored, errLoad := loadUpdateState(store)
	if errLoad != nil {
		t.Fatalf("loadUpdateState() error = %v", errLoad)
	}
	if stored.Error != "" {
		t.Fatalf("persisted error = %q, want empty", stored.Error)
	}
}

func TestUpdateCheckerConfigureRetriesCorruptSameStore(t *testing.T) {
	dataDir := t.TempDir()
	storePath := updateStorePath(dataDir)
	if errWrite := os.WriteFile(storePath, []byte("{"), 0o600); errWrite != nil {
		t.Fatalf("write corrupt update state: %v", errWrite)
	}
	checker := NewUpdateChecker("0.3.0")
	checker.Configure(Config{DataDir: dataDir})
	defer checker.Shutdown()
	if got := checker.Snapshot().Error; got != "update state could not be loaded" {
		t.Fatalf("load error = %q", got)
	}
	want := persistedUpdateState{Version: updateStoreVersion, Policy: defaultUpdatePolicy(), CheckedAt: time.Unix(1_800_000_000, 0).UTC()}
	want.Policy.CheckIntervalHours = 72
	if errSave := saveUpdateState(storePath, want); errSave != nil {
		t.Fatalf("save recovered update state: %v", errSave)
	}
	checker.Configure(Config{DataDir: dataDir})
	snapshot := checker.Snapshot()
	if snapshot.Error != "" || snapshot.Policy.CheckIntervalHours != 72 || !snapshot.CheckedAt.Equal(want.CheckedAt) {
		t.Fatalf("recovered snapshot = %#v", snapshot)
	}
}

func TestUpdateCheckerRetriesFailedRequestCheckPersistence(t *testing.T) {
	previousDelay := updatePersistRetryDelay
	updatePersistRetryDelay = 10 * time.Millisecond
	t.Cleanup(func() { updatePersistRetryDelay = previousDelay })

	dataDir := t.TempDir()
	checker := NewUpdateChecker("0.3.0")
	checker.Configure(Config{DataDir: dataDir})
	defer checker.Shutdown()
	blockingPath := filepath.Join(dataDir, "blocked")
	if errWrite := os.WriteFile(blockingPath, []byte("block"), 0o600); errWrite != nil {
		t.Fatalf("write blocking path: %v", errWrite)
	}
	checker.mu.Lock()
	checker.store = filepath.Join(blockingPath, "update-state.json")
	checker.mu.Unlock()
	checker.RequestCheck()
	if got := checker.Snapshot().Error; got != "update state could not be persisted" {
		t.Fatalf("persistence error = %q", got)
	}
	if errRemove := os.Remove(blockingPath); errRemove != nil {
		t.Fatalf("remove blocking path: %v", errRemove)
	}
	if errMkdir := os.MkdirAll(blockingPath, 0o700); errMkdir != nil {
		t.Fatalf("restore blocking path: %v", errMkdir)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && checker.Snapshot().Error != "" {
		time.Sleep(5 * time.Millisecond)
	}
	if got := checker.Snapshot().Error; got != "" {
		t.Fatalf("persistence error after retry = %q", got)
	}
	loaded, errLoad := loadUpdateState(filepath.Join(blockingPath, "update-state.json"))
	if errLoad != nil {
		t.Fatalf("load retried update state: %v", errLoad)
	}
	if loaded.CheckedAt.IsZero() || loaded.Error != "" {
		t.Fatalf("retried state = %#v", loaded)
	}
}

func TestUpdateCheckerDoesNotSwitchStoreWhenDirtyFlushFails(t *testing.T) {
	oldDir := t.TempDir()
	newDir := t.TempDir()
	checker := NewUpdateChecker("0.3.0")
	checker.Configure(Config{DataDir: oldDir})
	defer checker.Shutdown()
	checker.mu.Lock()
	checker.checkedAt = time.Unix(1_800_000_000, 0).UTC()
	checker.dirty = true
	checker.mu.Unlock()
	if errRemove := os.RemoveAll(oldDir); errRemove != nil {
		t.Fatalf("remove old data dir: %v", errRemove)
	}
	if errWrite := os.WriteFile(oldDir, []byte("block"), 0o600); errWrite != nil {
		t.Fatalf("replace old data dir with file: %v", errWrite)
	}
	checker.Configure(Config{DataDir: newDir})
	checker.mu.RLock()
	store, dirty, checkedAt := checker.store, checker.dirty, checker.checkedAt
	checker.mu.RUnlock()
	if store != updateStorePath(oldDir) || !dirty || checkedAt.IsZero() {
		t.Fatalf("dirty state switched or was lost: store=%q dirty=%v checked_at=%v", store, dirty, checkedAt)
	}
	if got := checker.Snapshot().Error; got != "update state could not be persisted" {
		t.Fatalf("persistence error = %q", got)
	}
}

func TestUpdateCheckerShutdownStopsPersistenceRetry(t *testing.T) {
	previousDelay := updatePersistRetryDelay
	updatePersistRetryDelay = time.Hour
	t.Cleanup(func() { updatePersistRetryDelay = previousDelay })

	dataDir := t.TempDir()
	checker := NewUpdateChecker("0.3.0")
	checker.Configure(Config{DataDir: dataDir})
	blockingPath := filepath.Join(dataDir, "blocked")
	if errWrite := os.WriteFile(blockingPath, []byte("block"), 0o600); errWrite != nil {
		t.Fatalf("write blocking path: %v", errWrite)
	}
	checker.mu.Lock()
	checker.store = filepath.Join(blockingPath, "update-state.json")
	checker.mu.Unlock()
	checker.RequestCheck()
	checker.mu.RLock()
	scheduled := checker.retryScheduled && checker.retryTimer != nil
	checker.mu.RUnlock()
	if !scheduled {
		t.Fatal("persistence retry was not scheduled")
	}
	checker.Shutdown()
	checker.mu.RLock()
	scheduled = checker.retryScheduled || checker.retryTimer != nil
	checker.mu.RUnlock()
	if scheduled {
		t.Fatal("persistence retry survived shutdown")
	}
}

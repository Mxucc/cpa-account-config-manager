package manager

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type backgroundWorkOwnerFunc func() bool

func (fn backgroundWorkOwnerFunc) AllowsBackgroundWork() bool { return fn() }

func TestRuntimeOwnershipStartsImmediatelyWithoutCompetitor(t *testing.T) {
	owner := newTestRuntimeOwnership("0.3.1202", "instance-a", "scope-a", time.Now().UTC())
	owner.Configure(Config{DataDir: t.TempDir()})
	t.Cleanup(owner.Shutdown)

	snapshot := owner.Snapshot()
	if !snapshot.Active || snapshot.Superseded || snapshot.RestartRecommended || snapshot.OwnerVersion != "0.3.1202" || snapshot.StorageError != "" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestRuntimeOwnershipRequiresOneRestartWhenProtocolIsFirstInstalled(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Now().UTC()
	first := NewRuntimeOwnership(runtimeProtocolVersion)
	first.instanceID = "instance-first"
	first.scope = "scope-before-restart"
	first.now = func() time.Time { return now }
	first.heartbeat = time.Hour
	first.Configure(Config{DataDir: dataDir})
	if first.AllowsBackgroundWork() || !first.Snapshot().RestartRequired {
		t.Fatalf("first process snapshot = %#v", first.Snapshot())
	}
	first.Shutdown()

	afterRestart := NewRuntimeOwnership(runtimeProtocolVersion)
	afterRestart.instanceID = "instance-after-restart"
	afterRestart.scope = "scope-after-restart"
	afterRestart.now = func() time.Time { return now.Add(time.Minute) }
	afterRestart.heartbeat = time.Hour
	afterRestart.Configure(Config{DataDir: dataDir})
	t.Cleanup(afterRestart.Shutdown)
	if !afterRestart.AllowsBackgroundWork() || afterRestart.Snapshot().RestartRequired {
		t.Fatalf("restarted process snapshot = %#v", afterRestart.Snapshot())
	}
}

func TestRuntimeBootstrapMigratesDockerPIDScopeAfterAnObservedRestart(t *testing.T) {
	dataDir := t.TempDir()
	updatedAt := time.Date(2026, 7, 25, 8, 0, 0, 0, time.UTC)
	legacy := persistedRuntimeBootstrap{
		Version: runtimeBootstrapVersion - 1, PendingProcessScope: "legacy-docker-pid-1", UpdatedAt: updatedAt,
	}
	if errSave := savePrivateJSON(filepath.Join(dataDir, runtimeBootstrapStoreName), legacy); errSave != nil {
		t.Fatalf("save legacy bootstrap: %v", errSave)
	}
	restartRequired, storageErr := runtimeBootstrapStatus(
		dataDir,
		"process-start-v2",
		"legacy-docker-pid-1",
		updatedAt.Add(time.Second),
		updatedAt.Add(2*time.Second),
		true,
	)
	if storageErr != "" || restartRequired {
		t.Fatalf("migration result restart=%t error=%q", restartRequired, storageErr)
	}
	state := readPersistedRuntimeBootstrap(t, dataDir)
	if state.Version != runtimeBootstrapVersion || !state.Ready || state.PendingProcessScope != "" {
		t.Fatalf("migrated state = %#v", state)
	}
}

func TestRuntimeBootstrapMigrationDoesNotMistakeHotReloadForRestart(t *testing.T) {
	dataDir := t.TempDir()
	processStartedAt := time.Date(2026, 7, 25, 8, 0, 0, 0, time.UTC)
	updatedAt := processStartedAt.Add(time.Minute)
	legacy := persistedRuntimeBootstrap{
		Version: runtimeBootstrapVersion - 1, PendingProcessScope: "legacy-same-process", UpdatedAt: updatedAt,
	}
	if errSave := savePrivateJSON(filepath.Join(dataDir, runtimeBootstrapStoreName), legacy); errSave != nil {
		t.Fatalf("save legacy bootstrap: %v", errSave)
	}
	restartRequired, storageErr := runtimeBootstrapStatus(
		dataDir,
		"process-start-v2",
		"legacy-same-process",
		processStartedAt,
		updatedAt.Add(time.Second),
		true,
	)
	if storageErr != "" || !restartRequired {
		t.Fatalf("migration result restart=%t error=%q", restartRequired, storageErr)
	}
	state := readPersistedRuntimeBootstrap(t, dataDir)
	if state.Version != runtimeBootstrapVersion || state.Ready || state.PendingProcessScope != "process-start-v2" {
		t.Fatalf("migrated state = %#v", state)
	}
}

func TestParseLinuxProcStatStartTimeHandlesSpacesAndParentheses(t *testing.T) {
	raw := "1 (cpa worker (main)) S 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23"
	if startTime := parseLinuxProcStatStartTime(raw); startTime != "22" {
		t.Fatalf("start time = %q, want 22", startTime)
	}
	for _, malformed := range []string{"", "1 cpa S 1 2", "1 (cpa) S 1 2", "1 (cpa) S 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 nope"} {
		if startTime := parseLinuxProcStatStartTime(malformed); startTime != "" {
			t.Fatalf("malformed stat %q returned %q", malformed, startTime)
		}
	}
}

func TestRuntimeOwnershipNewerVersionSupersedesOlderAfterTakeoverDelay(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Date(2026, 7, 25, 8, 0, 0, 0, time.UTC)
	older := newTestRuntimeOwnership("0.3.1202", "instance-old", "scope-shared", now)
	quiesced := make(chan struct{}, 1)
	older.SetOnSuperseded(func() { quiesced <- struct{}{} })
	older.Configure(Config{DataDir: dataDir})
	t.Cleanup(older.Shutdown)
	if !older.AllowsBackgroundWork() {
		t.Fatal("older instance did not own a clean runtime")
	}

	newer := newTestRuntimeOwnership("0.3.1203", "instance-new", "scope-shared", now.Add(time.Second))
	newer.Configure(Config{DataDir: dataDir})
	t.Cleanup(newer.Shutdown)
	older.now = func() time.Time { return now.Add(time.Second) }
	older.refresh()
	if older.AllowsBackgroundWork() || newer.AllowsBackgroundWork() {
		t.Fatalf("takeover overlap: older=%#v newer=%#v", older.Snapshot(), newer.Snapshot())
	}
	select {
	case <-quiesced:
	case <-time.After(time.Second):
		t.Fatal("superseded instance did not quiesce")
	}
	if _, errStat := os.Stat(older.claimPath); !os.IsNotExist(errStat) {
		t.Fatalf("superseded claim still exists: %v", errStat)
	}

	takeoverTime := now.Add(time.Second + runtimeTakeoverDelay)
	newer.now = func() time.Time { return takeoverTime }
	newer.refresh()
	if !newer.AllowsBackgroundWork() || !older.Snapshot().Superseded {
		t.Fatalf("takeover failed: older=%#v newer=%#v", older.Snapshot(), newer.Snapshot())
	}
	if !newer.Snapshot().RestartRecommended {
		t.Fatalf("hot-reloaded owner did not recommend a process restart: %#v", newer.Snapshot())
	}
}

func TestRuntimeOwnershipSameVersionUsesLaterInstance(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Date(2026, 7, 25, 8, 0, 0, 0, time.UTC)
	first := newTestRuntimeOwnership("0.3.1202", "instance-first", "scope-shared", now)
	first.Configure(Config{DataDir: dataDir})
	t.Cleanup(first.Shutdown)
	second := newTestRuntimeOwnership("0.3.1202", "instance-second", "scope-shared", now.Add(time.Second))
	second.Configure(Config{DataDir: dataDir})
	t.Cleanup(second.Shutdown)

	first.now = func() time.Time { return now.Add(time.Second) }
	first.refresh()
	second.now = func() time.Time { return now.Add(time.Second + runtimeTakeoverDelay) }
	second.refresh()
	if first.AllowsBackgroundWork() || !second.AllowsBackgroundWork() {
		t.Fatalf("same-version winner: first=%#v second=%#v", first.Snapshot(), second.Snapshot())
	}
}

func TestRuntimeOwnershipIgnoresExpiredNewerClaim(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Date(2026, 7, 25, 8, 0, 0, 0, time.UTC)
	directory := filepath.Join(dataDir, runtimeOwnershipStoreName, "scope-shared")
	stale := runtimeClaim{
		Version: runtimeClaimVersion, InstanceID: "instance-stale", PluginVersion: "9.0.0",
		ProcessScope: "scope-shared", StartedAt: now.Add(-time.Hour), HeartbeatAt: now.Add(-runtimeClaimTimeout - time.Second),
	}
	if errSave := savePrivateJSON(filepath.Join(directory, stale.InstanceID+".json"), stale); errSave != nil {
		t.Fatalf("save stale claim: %v", errSave)
	}
	older := newTestRuntimeOwnership("0.3.1202", "instance-old", "scope-shared", now)
	older.Configure(Config{DataDir: dataDir})
	t.Cleanup(older.Shutdown)
	if !older.AllowsBackgroundWork() || older.Snapshot().OwnerVersion != "0.3.1202" {
		t.Fatalf("stale claim blocked active instance = %#v", older.Snapshot())
	}
}

func TestRuntimeOwnershipScopesDoNotCompete(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Now().UTC()
	left := newTestRuntimeOwnership("0.3.1202", "instance-left", "scope-left", now)
	right := newTestRuntimeOwnership("9.0.0", "instance-right", "scope-right", now)
	left.Configure(Config{DataDir: dataDir})
	right.Configure(Config{DataDir: dataDir})
	t.Cleanup(left.Shutdown)
	t.Cleanup(right.Shutdown)
	if !left.AllowsBackgroundWork() || !right.AllowsBackgroundWork() {
		t.Fatalf("separate scopes competed: left=%#v right=%#v", left.Snapshot(), right.Snapshot())
	}
}

func TestRuntimeOwnershipFailsClosedWhenStorageIsUnavailable(t *testing.T) {
	blockingPath := filepath.Join(t.TempDir(), "not-a-directory")
	if errWrite := os.WriteFile(blockingPath, []byte("blocked"), 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	owner := newTestRuntimeOwnership("0.3.1202", "instance-a", "scope-a", time.Now().UTC())
	owner.Configure(Config{DataDir: filepath.Join(blockingPath, "data")})
	t.Cleanup(owner.Shutdown)
	if owner.AllowsBackgroundWork() || owner.Snapshot().StorageError == "" {
		t.Fatalf("unavailable storage snapshot = %#v", owner.Snapshot())
	}
}

func TestRuntimeOwnershipShutdownRemovesOwnClaim(t *testing.T) {
	owner := newTestRuntimeOwnership("0.3.1202", "instance-a", "scope-a", time.Now().UTC())
	owner.Configure(Config{DataDir: t.TempDir()})
	claimPath := owner.claimPath
	if _, errStat := os.Stat(claimPath); errStat != nil {
		t.Fatalf("claim before shutdown: %v", errStat)
	}
	owner.Shutdown()
	if _, errStat := os.Stat(claimPath); !os.IsNotExist(errStat) {
		t.Fatalf("claim after shutdown error = %v", errStat)
	}
}

func TestBackgroundOwnershipContextCancelsWhenOwnershipIsLost(t *testing.T) {
	var allowed atomic.Bool
	allowed.Store(true)
	ctx, cancel := contextWithBackgroundOwnership(context.Background(), backgroundWorkOwnerFunc(allowed.Load))
	defer cancel()
	allowed.Store(false)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("ownership context was not cancelled")
	}
}

func newTestRuntimeOwnership(version, instanceID, scope string, now time.Time) *RuntimeOwnership {
	owner := NewRuntimeOwnership(version)
	owner.instanceID = instanceID
	owner.scope = scope
	owner.legacyScope = scope
	owner.processStartedAt = now.Add(-time.Minute)
	owner.now = func() time.Time { return now }
	owner.heartbeat = time.Hour
	owner.bootstrapEnabled = false
	return owner
}

func readPersistedRuntimeBootstrap(t *testing.T, dataDir string) persistedRuntimeBootstrap {
	t.Helper()
	raw, errRead := os.ReadFile(filepath.Join(dataDir, runtimeBootstrapStoreName))
	if errRead != nil {
		t.Fatalf("read bootstrap state: %v", errRead)
	}
	var state persistedRuntimeBootstrap
	if errDecode := json.Unmarshal(raw, &state); errDecode != nil {
		t.Fatalf("decode bootstrap state: %v", errDecode)
	}
	return state
}

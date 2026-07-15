package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestApplyDefaultPolicyMissingFillsAbsentZeroAndFalseWithoutTouchingSecrets(t *testing.T) {
	priority := 0
	websockets := false
	raw := json.RawMessage(`{
		"type":"codex",
		"priority":7,
		"access_token":"token-secret",
		"headers":{"Authorization":"Bearer header-secret"},
		"unknown":{"nested":true}
	}`)

	updated, applied, changed, errApply := applyDefaultPolicy(raw, DefaultPolicy{
		Priority:   &priority,
		Websockets: &websockets,
	}, applyMissing)
	if errApply != nil {
		t.Fatalf("applyDefaultPolicy() error = %v", errApply)
	}
	if !changed || len(applied) != 1 || applied[0] != policyFieldWebsockets {
		t.Fatalf("changed=%t applied=%#v", changed, applied)
	}

	var document map[string]any
	if errDecode := json.Unmarshal(updated, &document); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v", errDecode)
	}
	if document[policyFieldPriority] != float64(7) || document[policyFieldWebsockets] != false {
		t.Fatalf("managed fields = priority:%#v websockets:%#v", document[policyFieldPriority], document[policyFieldWebsockets])
	}
	for _, secret := range []string{"token-secret", "Bearer header-secret", `"nested":true`} {
		if !bytes.Contains(updated, []byte(secret)) {
			t.Fatalf("updated document did not preserve %q: %s", secret, updated)
		}
	}
}

func TestApplyDefaultPolicyForceOverwritesOnlyManagedFields(t *testing.T) {
	priority := 0
	websockets := false
	raw := json.RawMessage(`{"priority":9,"websockets":true,"disabled":true,"proxy_url":"http://user:proxy-secret@127.0.0.1:7890","api_key":"api-secret"}`)

	updated, applied, changed, errApply := applyDefaultPolicy(raw, DefaultPolicy{
		Priority:   &priority,
		Websockets: &websockets,
	}, applyForce)
	if errApply != nil {
		t.Fatalf("applyDefaultPolicy() error = %v", errApply)
	}
	if !changed || strings.Join(applied, ",") != "priority,websockets" {
		t.Fatalf("changed=%t applied=%#v", changed, applied)
	}
	var document map[string]any
	if errDecode := json.Unmarshal(updated, &document); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v", errDecode)
	}
	if document[policyFieldPriority] != float64(0) || document[policyFieldWebsockets] != false || document["disabled"] != true {
		t.Fatalf("document = %#v", document)
	}
	for _, secret := range []string{"proxy-secret", "api-secret"} {
		if !bytes.Contains(updated, []byte(secret)) {
			t.Fatalf("updated document did not preserve %q", secret)
		}
	}
}

func TestPolicyStatePersistsReloadsAndUsesPrivatePermissions(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "private")
	path := policyStorePath(dataDir)
	priority := 0
	websockets := false
	policy := normalizeDefaultPolicy(DefaultPolicy{
		Enabled:             true,
		ScanIntervalSeconds: 1,
		Priority:            &priority,
		Websockets:          &websockets,
	})
	lastScan := PolicyScanSummary{Scanned: 3, Changed: 2, FinishedAt: time.Now().UTC()}
	if errSave := savePolicyState(path, policy, lastScan); errSave != nil {
		t.Fatalf("savePolicyState() error = %v", errSave)
	}

	loaded, loadedScan, errLoad := loadPolicyState(path)
	if errLoad != nil {
		t.Fatalf("loadPolicyState() error = %v", errLoad)
	}
	if !loaded.Enabled || loaded.Priority == nil || *loaded.Priority != 0 || loaded.Websockets == nil || *loaded.Websockets || loaded.ScanIntervalSeconds != minPolicyScanIntervalSeconds {
		t.Fatalf("loaded policy = %#v", loaded)
	}
	if loadedScan.Scanned != 3 || loadedScan.Changed != 2 {
		t.Fatalf("loaded scan = %#v", loadedScan)
	}
	if runtime.GOOS != "windows" {
		fileInfo, errStat := os.Stat(path)
		if errStat != nil {
			t.Fatalf("Stat(file) error = %v", errStat)
		}
		dirInfo, errDirStat := os.Stat(dataDir)
		if errDirStat != nil {
			t.Fatalf("Stat(dir) error = %v", errDirStat)
		}
		if fileInfo.Mode().Perm() != 0o600 || dirInfo.Mode().Perm()&0o077 != 0 {
			t.Fatalf("permissions = file:%#o dir:%#o", fileInfo.Mode().Perm(), dirInfo.Mode().Perm())
		}
	}
}

func TestPolicyEngineReconcilesMissingFieldsAndDetectsNewFiles(t *testing.T) {
	modTime := time.Now().UTC().Add(-time.Minute)
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "a", Name: "a.json", Source: "file", Path: "/auths/a.json", Size: 48, ModTime: modTime,
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"a": {AuthIndex: "a", Name: "a.json", Path: "/auths/a.json", JSON: json.RawMessage(`{"type":"codex","access_token":"first-secret"}`)},
		},
	}
	engine := NewPolicyEngine(host)
	engine.Configure(Config{DataDir: t.TempDir()})
	defer engine.Shutdown()
	priority := 0
	websockets := false
	if _, errSet := engine.SetPolicy(DefaultPolicy{Enabled: true, Priority: &priority, Websockets: &websockets}); errSet != nil {
		t.Fatalf("SetPolicy() error = %v", errSet)
	}
	waitForPolicy(t, engine, func(snapshot PolicySnapshot) bool {
		host.mu.Lock()
		defer host.mu.Unlock()
		return !snapshot.Running && len(host.saves) == 1
	})

	host.mu.Lock()
	firstJSON := append(json.RawMessage(nil), host.details["a"].JSON...)
	firstSaveCount := len(host.saves)
	host.mu.Unlock()
	if firstSaveCount != 1 || !bytes.Contains(firstJSON, []byte("first-secret")) {
		t.Fatalf("first reconciliation saves=%d json=%s", firstSaveCount, firstJSON)
	}
	assertManagedPolicyValues(t, firstJSON, 0, false)

	firstFinished := engine.Snapshot().LastScan.FinishedAt
	engine.RequestScan()
	waitForPolicy(t, engine, func(snapshot PolicySnapshot) bool {
		return snapshot.LastScan.FinishedAt.After(firstFinished)
	})
	secondFinished := engine.Snapshot().LastScan.FinishedAt
	engine.RequestScan()
	waitForPolicy(t, engine, func(snapshot PolicySnapshot) bool {
		return snapshot.LastScan.FinishedAt.After(secondFinished)
	})
	host.mu.Lock()
	if len(host.saves) != 1 {
		host.mu.Unlock()
		t.Fatalf("unchanged auth file was saved repeatedly: %d", len(host.saves))
	}
	host.entries = append(host.entries, cpaapi.HostAuthFileEntry{
		AuthIndex: "b", Name: "b.json", Source: "file", Path: "/auths/b.json", Size: 42, ModTime: modTime,
	})
	host.details["b"] = cpaapi.HostAuthGetResponse{
		AuthIndex: "b", Name: "b.json", Path: "/auths/b.json", JSON: json.RawMessage(`{"type":"codex","api_key":"second-secret"}`),
	}
	host.mu.Unlock()

	engine.RequestScan()
	waitForPolicy(t, engine, func(PolicySnapshot) bool {
		host.mu.Lock()
		defer host.mu.Unlock()
		return len(host.saves) == 2
	})
	host.mu.Lock()
	secondJSON := append(json.RawMessage(nil), host.details["b"].JSON...)
	host.mu.Unlock()
	assertManagedPolicyValues(t, secondJSON, 0, false)
	if !bytes.Contains(secondJSON, []byte("second-secret")) {
		t.Fatalf("second reconciliation lost an unknown secret field: %s", secondJSON)
	}
}

func TestPolicyEngineSkipsUnsupportedAndDuplicateEntries(t *testing.T) {
	now := time.Now().UTC()
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{
			{AuthIndex: "runtime", Name: "runtime.json", Source: "runtime", RuntimeOnly: true},
			{AuthIndex: "text", Name: "notes.txt", Source: "file", Path: "/auths/notes.txt", ModTime: now},
			{AuthIndex: "duplicate-a", Name: "shared.json", Source: "file", Path: "/auths/shared.json", ModTime: now},
			{AuthIndex: "duplicate-b", Name: "shared.json", Source: "file", Path: "/auths/shared.json", ModTime: now},
		},
		details: map[string]cpaapi.HostAuthGetResponse{},
	}
	engine := NewPolicyEngine(host)
	engine.Configure(Config{DataDir: t.TempDir()})
	defer engine.Shutdown()
	priority := 1
	if _, errSet := engine.SetPolicy(DefaultPolicy{Enabled: true, Priority: &priority}); errSet != nil {
		t.Fatalf("SetPolicy() error = %v", errSet)
	}
	snapshot := waitForPolicy(t, engine, func(snapshot PolicySnapshot) bool {
		return !snapshot.LastScan.FinishedAt.IsZero()
	})
	if snapshot.LastScan.Scanned != 4 || snapshot.LastScan.Eligible != 0 || snapshot.LastScan.Skipped != 4 || snapshot.LastScan.Failed != 0 {
		t.Fatalf("last scan = %#v", snapshot.LastScan)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.saves) != 0 {
		t.Fatalf("unsupported entries were saved: %#v", host.saves)
	}
}

func TestPolicyEngineSharesMutationCoordinatorWithExplicitJobs(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "a", Name: "a.json", Source: "file", Path: "/auths/a.json", Size: 10, ModTime: time.Now().UTC(),
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"a": {AuthIndex: "a", Name: "a.json", Path: "/auths/a.json", JSON: json.RawMessage(`{"type":"codex"}`)},
		},
	}
	coordinator := NewMutationCoordinator()
	engine := NewPolicyEngineWithCoordinator(host, coordinator)
	priority := 1
	engine.mu.Lock()
	engine.policy = normalizeDefaultPolicy(DefaultPolicy{Enabled: true, Priority: &priority})
	engine.store = policyStorePath(t.TempDir())
	engine.mu.Unlock()

	if !coordinator.TryAcquire("batch-job") {
		t.Fatal("failed to reserve mutation coordinator")
	}
	if retrySoon := engine.reconcile(context.Background()); !retrySoon {
		t.Fatal("busy background policy scan did not request a short retry")
	}
	host.mu.Lock()
	blockedSaves := len(host.saves)
	host.mu.Unlock()
	if blockedSaves != 0 {
		t.Fatalf("background policy scan wrote during an explicit job: %d saves", blockedSaves)
	}

	coordinator.Release("batch-job")
	if retrySoon := engine.reconcile(context.Background()); retrySoon {
		t.Fatal("policy scan requested another retry after the writer slot was released")
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.saves) != 1 {
		t.Fatalf("policy scan after release saves = %d, want 1", len(host.saves))
	}
}

func TestPolicyEngineReloadsPersistedPolicyAndPerformsFullScan(t *testing.T) {
	dataDir := t.TempDir()
	priority := 3
	firstEngine := NewPolicyEngine(&fakeAuthHost{details: map[string]cpaapi.HostAuthGetResponse{}})
	firstEngine.Configure(Config{DataDir: dataDir})
	if _, errSet := firstEngine.SetPolicy(DefaultPolicy{Enabled: true, Priority: &priority}); errSet != nil {
		t.Fatalf("SetPolicy() error = %v", errSet)
	}
	firstEngine.Shutdown()

	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "after-restart", Name: "after-restart.json", Source: "file", Path: "/auths/after-restart.json", Size: 8, ModTime: time.Now().UTC(),
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"after-restart": {
				AuthIndex: "after-restart",
				Name:      "after-restart.json",
				Path:      "/auths/after-restart.json",
				JSON:      json.RawMessage(`{"type":"codex","refresh_token":"restart-secret"}`),
			},
		},
	}
	secondEngine := NewPolicyEngine(host)
	secondEngine.Configure(Config{DataDir: dataDir})
	defer secondEngine.Shutdown()
	waitForPolicy(t, secondEngine, func(snapshot PolicySnapshot) bool {
		host.mu.Lock()
		defer host.mu.Unlock()
		return snapshot.Policy.Enabled && snapshot.Policy.Priority != nil &&
			*snapshot.Policy.Priority == 3 && len(host.saves) == 1
	})
	host.mu.Lock()
	updated := append(json.RawMessage(nil), host.details["after-restart"].JSON...)
	host.mu.Unlock()
	if !bytes.Contains(updated, []byte("restart-secret")) {
		t.Fatalf("restarted policy lost an unknown field: %s", updated)
	}
	var document map[string]any
	if errDecode := json.Unmarshal(updated, &document); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v", errDecode)
	}
	if document[policyFieldPriority] != float64(3) {
		t.Fatalf("priority = %#v, want 3", document[policyFieldPriority])
	}
}

func TestPolicyEngineSanitizesSaveFailuresAndRetries(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "a", Name: "a.json", Source: "file", Path: "/auths/a.json", Size: 10, ModTime: time.Now().UTC(),
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"a": {AuthIndex: "a", Name: "a.json", Path: "/auths/a.json", JSON: json.RawMessage(`{"access_token":"document-secret"}`)},
		},
		saveErrors: map[string]error{"a.json": errors.New("callback failed: token=callback-secret")},
	}
	engine := NewPolicyEngine(host)
	engine.Configure(Config{DataDir: t.TempDir()})
	defer engine.Shutdown()
	websockets := true
	if _, errSet := engine.SetPolicy(DefaultPolicy{Enabled: true, Websockets: &websockets}); errSet != nil {
		t.Fatalf("SetPolicy() error = %v", errSet)
	}
	first := waitForPolicy(t, engine, func(snapshot PolicySnapshot) bool {
		return snapshot.LastScan.Failed == 1
	})
	encoded, errMarshal := json.Marshal(first)
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	for _, secret := range []string{"callback-secret", "document-secret", "a.json", "/auths"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("policy status leaked %q: %s", secret, encoded)
		}
	}
	firstFinished := first.LastScan.FinishedAt
	engine.RequestScan()
	waitForPolicy(t, engine, func(snapshot PolicySnapshot) bool {
		return snapshot.LastScan.FinishedAt.After(firstFinished) && snapshot.LastScan.Failed == 1
	})
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.saveCalls["a.json"] < 2 {
		t.Fatalf("failed save was not retried: %#v", host.saveCalls)
	}
}

func TestPolicyEngineShutdownCancelsBackgroundScan(t *testing.T) {
	host := &blockingPolicyHost{started: make(chan struct{})}
	dataDir := t.TempDir()
	priority := 1
	if errSave := savePolicyState(policyStorePath(dataDir), normalizeDefaultPolicy(DefaultPolicy{
		Enabled:  true,
		Priority: &priority,
	}), PolicyScanSummary{}); errSave != nil {
		t.Fatalf("savePolicyState() error = %v", errSave)
	}
	engine := NewPolicyEngine(host)
	engine.Configure(Config{DataDir: dataDir})
	select {
	case <-host.started:
	case <-time.After(2 * time.Second):
		t.Fatal("background scan did not start")
	}
	done := make(chan struct{})
	go func() {
		engine.Shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown() did not wait for the cancelled reconciler")
	}
	if !host.cancelled() {
		t.Fatal("host callback did not observe cancellation")
	}
}

type blockingPolicyHost struct {
	once      sync.Once
	started   chan struct{}
	mu        sync.Mutex
	wasCancel bool
}

func (h *blockingPolicyHost) ListAuth(ctx context.Context) ([]cpaapi.HostAuthFileEntry, error) {
	h.once.Do(func() { close(h.started) })
	<-ctx.Done()
	h.mu.Lock()
	h.wasCancel = true
	h.mu.Unlock()
	return nil, ctx.Err()
}

func (*blockingPolicyHost) GetAuth(context.Context, string) (cpaapi.HostAuthGetResponse, error) {
	return cpaapi.HostAuthGetResponse{}, errors.New("unexpected get")
}

func (*blockingPolicyHost) SaveAuth(context.Context, string, json.RawMessage) (cpaapi.HostAuthSaveResponse, error) {
	return cpaapi.HostAuthSaveResponse{}, errors.New("unexpected save")
}

func (h *blockingPolicyHost) cancelled() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.wasCancel
}

func waitForPolicy(t *testing.T, engine *PolicyEngine, predicate func(PolicySnapshot) bool) PolicySnapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := engine.Snapshot()
		if predicate(snapshot) {
			return snapshot
		}
		time.Sleep(5 * time.Millisecond)
	}
	snapshot := engine.Snapshot()
	t.Fatalf("policy condition was not met; snapshot=%#v", snapshot)
	return PolicySnapshot{}
}

func assertManagedPolicyValues(t *testing.T, raw json.RawMessage, priority int, websockets bool) {
	t.Helper()
	var document map[string]any
	if errDecode := json.Unmarshal(raw, &document); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v", errDecode)
	}
	if document[policyFieldPriority] != float64(priority) || document[policyFieldWebsockets] != websockets {
		t.Fatalf("managed values = priority:%#v websockets:%#v", document[policyFieldPriority], document[policyFieldWebsockets])
	}
}

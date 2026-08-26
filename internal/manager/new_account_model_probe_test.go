package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewAccountModelProbeBaselinesExistingAccounts(t *testing.T) {
	engine := newTestAccountModelProbeEngine(t)
	calls := 0
	engine.SetHandler(func(context.Context, Account, string, string) (ModelTestResult, error) {
		calls++
		return ModelTestResult{Status: "available"}, nil
	})
	engine.Arm("management-secret", "private-host-callback")
	engine.ObserveAccounts([]Account{testProbeAccount("existing", "existing@example.com")})

	engine.reconcile(context.Background())

	if calls != 0 {
		t.Fatalf("baseline handler calls = %d, want 0", calls)
	}
	if !engine.initialized || len(engine.known) != 1 || len(engine.pending) != 0 {
		t.Fatalf("baseline state = initialized %v, known %d, pending %d", engine.initialized, len(engine.known), len(engine.pending))
	}
}

func TestNewAccountModelProbeSetManagementKeyDoesNotScheduleWork(t *testing.T) {
	engine := newTestAccountModelProbeEngine(t)
	engine.SetManagementKey("management-secret", "host-callback")

	select {
	case <-engine.wake:
		t.Fatal("setting management credentials unexpectedly scheduled a probe")
	default:
	}

	engine.mu.Lock()
	key, callbackID := engine.managementKey, engine.hostCallbackID
	engine.mu.Unlock()
	if key != "management-secret" || callbackID != "host-callback" {
		t.Fatalf("stored credentials = %q/%q", key, callbackID)
	}
}

func TestNewAccountModelProbeRepeatedAccountObservationDoesNotScheduleWork(t *testing.T) {
	engine := newTestAccountModelProbeEngine(t)
	account := testProbeAccount("stable-account", "stable@example.com")
	engine.ObserveAccounts([]Account{account})
	select {
	case <-engine.wake:
	default:
		t.Fatal("initial account observation did not schedule reconciliation")
	}

	engine.ObserveAccounts([]Account{account})
	select {
	case <-engine.wake:
		t.Fatal("repeated account observation scheduled reconciliation")
	default:
	}
}

func TestNewAccountModelProbeDetectsFirstAccountAfterEmptyBaseline(t *testing.T) {
	engine := newTestAccountModelProbeEngine(t)
	var mu sync.Mutex
	calls := make([]Account, 0, 1)
	engine.SetHandler(func(_ context.Context, account Account, key string, callbackID string) (ModelTestResult, error) {
		if key != "management-secret" {
			t.Errorf("management key = %q", key)
		}
		if callbackID != "host-callback" {
			t.Errorf("host callback id = %q", callbackID)
		}
		mu.Lock()
		calls = append(calls, account)
		mu.Unlock()
		return ModelTestResult{AccountID: account.ID, Status: "available"}, nil
	})
	engine.Arm("management-secret", "host-callback")
	engine.ObserveAccounts(nil)
	engine.reconcile(context.Background())

	account := testProbeAccount("new-account", "new-account@example.com")
	engine.ObserveAccounts([]Account{account})
	engine.reconcile(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0].ID != account.ID {
		t.Fatalf("handler accounts = %#v, want only %q", calls, account.ID)
	}
	if len(engine.pending) != 0 {
		t.Fatalf("pending after successful probe = %d, want 0", len(engine.pending))
	}
}

func TestNewAccountModelProbeDoesNotRepeatKnownAccount(t *testing.T) {
	engine := newTestAccountModelProbeEngine(t)
	calls := 0
	engine.SetHandler(func(_ context.Context, account Account, _ string, _ string) (ModelTestResult, error) {
		calls++
		return ModelTestResult{AccountID: account.ID, Status: "available"}, nil
	})
	engine.Arm("management-secret")
	engine.ObserveAccounts(nil)
	engine.reconcile(context.Background())
	account := testProbeAccount("new-account", "same@example.com")
	engine.ObserveAccounts([]Account{account})
	engine.reconcile(context.Background())
	engine.ObserveAccounts([]Account{account})
	engine.reconcile(context.Background())

	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1", calls)
	}
}

func TestNewAccountModelProbeWaitsForPolicyEnablement(t *testing.T) {
	enabled := false
	engine := NewAccountModelProbeEngine(func() bool { return enabled })
	engine.store = filepath.Join(t.TempDir(), "new-account-model-probe.json")
	calls := 0
	engine.SetHandler(func(_ context.Context, account Account, _ string, _ string) (ModelTestResult, error) {
		calls++
		return ModelTestResult{AccountID: account.ID, Status: "available"}, nil
	})
	engine.Arm("management-secret")
	engine.ObserveAccounts(nil)
	engine.reconcile(context.Background())
	engine.ObserveAccounts([]Account{testProbeAccount("new-account", "policy@example.com")})
	engine.reconcile(context.Background())
	if calls != 0 || len(engine.pending) != 1 {
		t.Fatalf("disabled policy calls = %d, pending = %d", calls, len(engine.pending))
	}

	enabled = true
	engine.reconcile(context.Background())
	if calls != 1 || len(engine.pending) != 0 {
		t.Fatalf("enabled policy calls = %d, pending = %d", calls, len(engine.pending))
	}
}

func TestNewAccountModelProbeUsesConditionalEligibility(t *testing.T) {
	engine := newTestAccountModelProbeEngine(t)
	engine.SetEligibility(func(account Account) bool { return account.PlanType == "plus" })
	var mu sync.Mutex
	calls := make([]string, 0, 1)
	engine.SetHandler(func(_ context.Context, account Account, _ string, _ string) (ModelTestResult, error) {
		mu.Lock()
		calls = append(calls, account.ID)
		mu.Unlock()
		return ModelTestResult{AccountID: account.ID, Status: "available"}, nil
	})
	engine.Arm("management-secret")
	engine.ObserveAccounts(nil)
	engine.reconcile(context.Background())
	free := testProbeAccount("free-account", "free@example.com")
	free.PlanType = "free"
	plus := testProbeAccount("plus-account", "plus@example.com")
	plus.PlanType = "plus"
	engine.ObserveAccounts([]Account{free, plus})
	engine.reconcile(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0] != plus.ID {
		t.Fatalf("conditional probe calls = %#v, want only %q", calls, plus.ID)
	}
	if len(engine.pending) != 1 {
		t.Fatalf("pending conditional accounts = %d, want 1", len(engine.pending))
	}
}

func TestNewAccountModelProbePersistsHashesWithoutSecrets(t *testing.T) {
	engine := newTestAccountModelProbeEngine(t)
	engine.SetHandler(func(_ context.Context, account Account, _ string, _ string) (ModelTestResult, error) {
		return ModelTestResult{AccountID: account.ID, Status: "available"}, nil
	})
	engine.Arm("management-secret", "private-host-callback")
	engine.ObserveAccounts(nil)
	engine.reconcile(context.Background())
	engine.ObserveAccounts([]Account{testProbeAccount("new-account", "private@example.com")})
	engine.reconcile(context.Background())

	raw, errRead := os.ReadFile(engine.store)
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	contents := string(raw)
	for _, secret := range []string{"management-secret", "private-host-callback", "private@example.com", "new-account"} {
		if strings.Contains(contents, secret) {
			t.Fatalf("persisted state contains secret %q: %s", secret, contents)
		}
	}
	state, errLoad := loadNewAccountModelProbeState(engine.store)
	if errLoad != nil {
		t.Fatalf("loadNewAccountModelProbeState() error = %v", errLoad)
	}
	if !state.Initialized || len(state.Known) != 1 || len(state.Pending) != 0 {
		t.Fatalf("persisted state = %#v", state)
	}
}

func TestNewAccountModelProbeReportsSanitizedStorageFailure(t *testing.T) {
	blockingPath := filepath.Join(t.TempDir(), "not-a-directory")
	if errWrite := os.WriteFile(blockingPath, []byte("blocked"), 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	engine := NewAccountModelProbeEngine(func() bool { return true })
	engine.store = filepath.Join(blockingPath, "new-account-model-probe.json")
	engine.ObserveAccounts(nil)
	engine.reconcile(context.Background())

	if got := engine.StorageError(); got != "new-account model-probe state could not be persisted" {
		t.Fatalf("StorageError() = %q", got)
	}
	if strings.Contains(engine.StorageError(), blockingPath) {
		t.Fatal("storage error exposed its filesystem path")
	}
}

func TestNewAccountModelProbeRetriesTransientResultWithBackoff(t *testing.T) {
	engine := newTestAccountModelProbeEngine(t)
	now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	engine.now = func() time.Time { return now }
	engine.SetHandler(func(_ context.Context, account Account, _ string, _ string) (ModelTestResult, error) {
		return ModelTestResult{AccountID: account.ID, Status: "review", ReasonCode: "upstream_unavailable"}, nil
	})
	engine.Arm("management-secret")
	engine.ObserveAccounts(nil)
	engine.reconcile(context.Background())
	account := testProbeAccount("new-account", "retry@example.com")
	engine.ObserveAccounts([]Account{account})
	delay := engine.reconcile(context.Background())

	identity := newAccountModelProbeIdentity(account)
	retry, exists := engine.pending[identity]
	if !exists {
		t.Fatal("transient probe result was not retained for retry")
	}
	if retry.Attempts != 1 || !retry.RetryAfter.Equal(now.Add(time.Minute)) {
		t.Fatalf("retry = %#v, want one attempt at %s", retry, now.Add(time.Minute))
	}
	if delay != time.Minute {
		t.Fatalf("retry delay = %s, want %s", delay, time.Minute)
	}
}

func TestNewAccountModelProbeSnapshotIsBounded(t *testing.T) {
	engine := newTestAccountModelProbeEngine(t)
	accounts := make([]Account, maxInspectionAccounts+1)
	for index := range accounts {
		accounts[index] = testProbeAccount(strings.Repeat("a", index%8)+time.Unix(int64(index), 0).UTC().Format("150405.000000000"), "")
		accounts[index].AuthID = time.Unix(int64(index), 0).UTC().Format(time.RFC3339Nano)
	}
	engine.ObserveAccounts(accounts)

	if len(engine.latest) != maxInspectionAccounts {
		t.Fatalf("latest accounts = %d, want %d", len(engine.latest), maxInspectionAccounts)
	}
}

func newTestAccountModelProbeEngine(t *testing.T) *newAccountModelProbeEngine {
	t.Helper()
	engine := NewAccountModelProbeEngine(func() bool { return true })
	engine.store = filepath.Join(t.TempDir(), "new-account-model-probe.json")
	return engine
}

func testProbeAccount(id string, email string) Account {
	return Account{
		ID: id, AuthID: id, Name: id + ".json", Provider: "codex", Type: "codex", Email: email,
	}
}

func TestNewAccountModelProbeConfigureSameStoreDoesNotScheduleWork(t *testing.T) {
	engine := newTestAccountModelProbeEngine(t)
	defer engine.Shutdown()
	config := Config{DataDir: filepath.Dir(engine.store)}
	engine.Configure(config)
	select {
	case <-engine.wake:
		t.Fatal("initial Configure unexpectedly scheduled a probe")
	default:
	}
	engine.Configure(config)
	select {
	case <-engine.wake:
		t.Fatal("repeated Configure for the same store scheduled a probe")
	default:
	}
}

func TestNewAccountModelProbeWakesWhenProbeMetadataChanges(t *testing.T) {
	engine := newTestAccountModelProbeEngine(t)
	account := testProbeAccount("stable-account", "stable@example.com")
	engine.ObserveAccounts([]Account{account})
	select {
	case <-engine.wake:
	default:
		t.Fatal("initial account observation did not schedule a probe")
	}
	engine.ObserveAccounts([]Account{account})
	select {
	case <-engine.wake:
		t.Fatal("unchanged account metadata scheduled a probe")
	default:
	}
	changedPlan := account
	changedPlan.PlanType = "plus"
	engine.ObserveAccounts([]Account{changedPlan})
	select {
	case <-engine.wake:
	default:
		t.Fatal("plan change did not schedule a probe")
	}
	// Usage and timestamps are intentionally not part of the probe summary.
	select {
	case <-engine.wake:
	default:
	}
	usageOnly := changedPlan
	usageOnly.UpdatedAt = timePtr(time.Now())
	usageOnly.Usage = &AccountUsageSnapshot{Codex: &CodexUsageSnapshot{MetadataObservedAt: time.Now()}}
	engine.ObserveAccounts([]Account{usageOnly})
	select {
	case <-engine.wake:
		t.Fatal("usage-only change scheduled a probe")
	default:
	}
}

func timePtr(value time.Time) *time.Time { return &value }

func TestNewAccountModelProbeConfigureRetriesFailedLoadForSameStore(t *testing.T) {
	dataDir := t.TempDir()
	storePath := newAccountModelProbeStorePath(dataDir)
	if errWrite := os.WriteFile(storePath, []byte("{"), 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	engine := NewAccountModelProbeEngine(func() bool { return true })
	engine.Configure(Config{DataDir: dataDir})
	defer engine.Shutdown()
	if got := engine.StorageError(); got != "new-account model-probe state could not be loaded" {
		t.Fatalf("StorageError() = %q", got)
	}
	knownIdentity := strings.Repeat("a", 64)
	want := persistedNewAccountModelProbeState{Version: newAccountModelProbeStoreVersion, Initialized: true, Known: []string{knownIdentity}, Pending: map[string]newAccountModelProbeRetry{}}
	if errSave := saveNewAccountModelProbeState(storePath, want); errSave != nil {
		t.Fatalf("saveNewAccountModelProbeState() error = %v", errSave)
	}
	engine.Configure(Config{DataDir: dataDir})
	if got := engine.StorageError(); got != "" {
		t.Fatalf("StorageError() after recovery = %q", got)
	}
	engine.mu.Lock()
	_, exists := engine.known[knownIdentity]
	engine.mu.Unlock()
	if !exists {
		t.Fatal("recovered known identity is missing")
	}
}

func TestNewAccountModelProbeRetriesFailedPersistence(t *testing.T) {
	previousDelay := newAccountModelProbePersistRetryDelay
	newAccountModelProbePersistRetryDelay = 10 * time.Millisecond
	t.Cleanup(func() { newAccountModelProbePersistRetryDelay = previousDelay })

	dataDir := t.TempDir()
	engine := NewAccountModelProbeEngine(func() bool { return true })
	engine.Configure(Config{DataDir: dataDir})
	defer engine.Shutdown()
	blockingPath := filepath.Join(dataDir, "blocked")
	if errWrite := os.WriteFile(blockingPath, []byte("block"), 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	engine.mu.Lock()
	engine.store = filepath.Join(blockingPath, "state.json")
	engine.observed = true
	engine.initialized = true
	engine.mu.Unlock()
	engine.requestRun()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && engine.StorageError() == "" {
		time.Sleep(5 * time.Millisecond)
	}
	if got := engine.StorageError(); got != "new-account model-probe state could not be persisted" {
		t.Fatalf("StorageError() = %q", got)
	}
	if errRemove := os.Remove(blockingPath); errRemove != nil {
		t.Fatalf("Remove() error = %v", errRemove)
	}
	if errMkdir := os.MkdirAll(blockingPath, 0o700); errMkdir != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdir)
	}
	for time.Now().Before(deadline) && engine.StorageError() != "" {
		time.Sleep(5 * time.Millisecond)
	}
	if got := engine.StorageError(); got != "" {
		t.Fatalf("StorageError() after retry = %q", got)
	}
	if _, errStat := os.Stat(filepath.Join(blockingPath, "state.json")); errStat != nil {
		t.Fatalf("persisted state after retry: %v", errStat)
	}
}

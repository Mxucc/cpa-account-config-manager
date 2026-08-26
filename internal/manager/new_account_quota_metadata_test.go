package manager

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestQuotaMetadataBootstrapCollectsOnlyMissingCodexAccountsOnce(t *testing.T) {
	engine := NewAccountQuotaMetadataBootstrap()
	var mu sync.Mutex
	calls := make([]string, 0, 1)
	engine.SetHandler(func(_ context.Context, account Account, key string) error {
		if key != "management-secret" {
			t.Errorf("management key = %q", key)
		}
		mu.Lock()
		calls = append(calls, account.ID)
		mu.Unlock()
		return nil
	})
	engine.Arm("management-secret")
	observedAt := time.Date(2026, time.July, 27, 5, 0, 0, 0, time.UTC)
	missing := testQuotaBootstrapAccount("missing", "codex", time.Time{})
	observed := testQuotaBootstrapAccount("observed", "codex", observedAt)
	unsupported := testQuotaBootstrapAccount("unsupported", "gemini", time.Time{})

	engine.ObserveAccounts([]Account{missing, observed, unsupported})
	engine.reconcile(context.Background())
	engine.ObserveAccounts([]Account{missing, observed, unsupported})
	engine.reconcile(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0] != missing.ID {
		t.Fatalf("handler calls = %#v, want only %q", calls, missing.ID)
	}
}

func TestQuotaMetadataBootstrapIncludesAgentIdentityAndBoundsAccounts(t *testing.T) {
	engine := NewAccountQuotaMetadataBootstrap()
	accounts := make([]Account, 0, maxInspectionAccounts+2)
	accounts = append(accounts, testQuotaBootstrapAccount("agent", agentIdentityProvider, time.Time{}))
	for index := 0; index <= maxInspectionAccounts; index++ {
		accounts = append(accounts, testQuotaBootstrapAccount(time.Unix(int64(index), 0).UTC().Format(time.RFC3339Nano), "codex", time.Time{}))
	}
	engine.ObserveAccounts(accounts)

	if len(engine.latest) != maxInspectionAccounts {
		t.Fatalf("latest accounts = %d, want %d", len(engine.latest), maxInspectionAccounts)
	}
	if _, exists := engine.latest[newAccountModelProbeIdentity(accounts[0])]; !exists {
		t.Fatal("Agent Identity account was not eligible for quota bootstrap")
	}
}

func TestQuotaMetadataBootstrapRetriesWithBoundedBackoff(t *testing.T) {
	engine := NewAccountQuotaMetadataBootstrap()
	now := time.Date(2026, time.July, 27, 6, 0, 0, 0, time.UTC)
	engine.now = func() time.Time { return now }
	calls := 0
	engine.SetHandler(func(context.Context, Account, string) error {
		calls++
		return ErrQuotaMetadataUnavailable
	})
	engine.Arm("management-secret")
	account := testQuotaBootstrapAccount("retry", "codex", time.Time{})
	engine.ObserveAccounts([]Account{account})

	delay := engine.reconcile(context.Background())
	identity := newAccountModelProbeIdentity(account)
	retry, exists := engine.pending[identity]
	if calls != 1 || !exists || retry.Attempts != 1 || !retry.RetryAfter.Equal(now.Add(time.Minute)) {
		t.Fatalf("retry after first failure = calls %d retry %#v exists %v", calls, retry, exists)
	}
	if delay != time.Minute {
		t.Fatalf("retry delay = %s, want %s", delay, time.Minute)
	}

	engine.reconcile(context.Background())
	if calls != 1 {
		t.Fatalf("handler calls before retry due = %d, want 1", calls)
	}
	now = now.Add(time.Minute)
	engine.reconcile(context.Background())
	if calls != 2 {
		t.Fatalf("handler calls after retry due = %d, want 2", calls)
	}
}

func TestQuotaMetadataBootstrapDoesNotRunWithoutManagementKey(t *testing.T) {
	engine := NewAccountQuotaMetadataBootstrap()
	calls := 0
	engine.SetHandler(func(context.Context, Account, string) error {
		calls++
		return nil
	})
	engine.ObserveAccounts([]Account{testQuotaBootstrapAccount("missing", "codex", time.Time{})})
	engine.reconcile(context.Background())
	if calls != 0 {
		t.Fatalf("handler calls without management key = %d", calls)
	}
}

func TestQuotaMetadataBootstrapShutdownCancelsWorkAndClearsManagementKey(t *testing.T) {
	engine := NewAccountQuotaMetadataBootstrap()
	started := make(chan struct{})
	finished := make(chan struct{})
	engine.SetHandler(func(ctx context.Context, _ Account, _ string) error {
		close(started)
		<-ctx.Done()
		close(finished)
		return ctx.Err()
	})
	engine.Start()
	engine.Arm("management-secret")
	engine.ObserveAccounts([]Account{testQuotaBootstrapAccount("shutdown", "codex", time.Time{})})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("quota metadata bootstrap did not start")
	}
	engine.Shutdown()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("quota metadata bootstrap did not stop")
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.managementKey != "" {
		t.Fatal("quota metadata bootstrap retained the Management Key after shutdown")
	}
}

func testQuotaBootstrapAccount(id, provider string, observedAt time.Time) Account {
	account := Account{ID: id, AuthID: id, Name: id + ".json", Provider: provider, Type: provider, Email: id + "@example.com"}
	if !observedAt.IsZero() {
		account.Usage = &AccountUsageSnapshot{Codex: &CodexUsageSnapshot{MetadataObservedAt: observedAt}}
	}
	return account
}

func TestQuotaMetadataRepeatedAccountObservationDoesNotScheduleWork(t *testing.T) {
	engine := NewAccountQuotaMetadataBootstrap()
	account := Account{ID: "stable-account", AuthID: "stable-account", Provider: "codex", Type: "codex"}
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

func TestQuotaMetadataBootstrapStartIsIdempotent(t *testing.T) {
	engine := NewAccountQuotaMetadataBootstrap()
	defer engine.Shutdown()
	engine.Start()
	select {
	case <-engine.wake:
		t.Fatal("initial Start unexpectedly scheduled metadata collection")
	default:
	}
	engine.Start()
	select {
	case <-engine.wake:
		t.Fatal("repeated Start scheduled metadata collection")
	default:
	}
}

func TestQuotaMetadataBootstrapStopsAfterMaximumFailures(t *testing.T) {
	engine := NewAccountQuotaMetadataBootstrap()
	now := time.Date(2026, time.July, 27, 6, 0, 0, 0, time.UTC)
	engine.now = func() time.Time { return now }
	calls := 0
	engine.SetHandler(func(context.Context, Account, string) error {
		calls++
		return ErrQuotaMetadataUnavailable
	})
	engine.Arm("management-secret")
	account := testQuotaBootstrapAccount("exhausted", "codex", time.Time{})
	engine.ObserveAccounts([]Account{account})
	identity := newAccountModelProbeIdentity(account)
	for attempt := 0; attempt < quotaMetadataBootstrapMaxAttempts; attempt++ {
		engine.reconcile(context.Background())
		engine.mu.Lock()
		retry, pending := engine.pending[identity]
		engine.mu.Unlock()
		if attempt+1 < quotaMetadataBootstrapMaxAttempts {
			if !pending {
				t.Fatalf("attempt %d removed pending before exhaustion", attempt+1)
			}
			now = retry.RetryAfter
		}
	}
	if calls != quotaMetadataBootstrapMaxAttempts {
		t.Fatalf("handler calls = %d, want %d", calls, quotaMetadataBootstrapMaxAttempts)
	}
	engine.mu.Lock()
	_, pending := engine.pending[identity]
	_, exhausted := engine.exhausted[identity]
	engine.mu.Unlock()
	if pending || !exhausted {
		t.Fatalf("terminal retry state pending=%v exhausted=%v", pending, exhausted)
	}
	// Re-observing the same account must not requeue a terminal failure.
	engine.ObserveAccounts([]Account{account})
	engine.reconcile(context.Background())
	if calls != quotaMetadataBootstrapMaxAttempts {
		t.Fatalf("terminal account was retried: calls=%d", calls)
	}
	// An explicit Arm is the documented operator retry path.
	engine.Arm("management-secret")
	engine.reconcile(context.Background())
	if calls != quotaMetadataBootstrapMaxAttempts+1 {
		t.Fatalf("explicit Arm did not retry exhausted account: calls=%d", calls)
	}
}

func TestQuotaMetadataBootstrapRequeuesWhenAccountMetadataChanges(t *testing.T) {
	engine := NewAccountQuotaMetadataBootstrap()
	account := testQuotaBootstrapAccount("stable", "codex", time.Date(2026, time.July, 27, 5, 0, 0, 0, time.UTC))
	account.PlanType = "free"
	engine.ObserveAccounts([]Account{account})
	select {
	case <-engine.wake:
	default:
	}
	engine.mu.Lock()
	identity := newAccountModelProbeIdentity(account)
	engine.completed[identity] = struct{}{}
	engine.mu.Unlock()

	changed := account
	changed.PlanType = "plus"
	engine.ObserveAccounts([]Account{changed})
	if _, pending := engine.pending[identity]; !pending {
		t.Fatal("metadata change did not requeue quota collection")
	}
}

func TestQuotaMetadataBootstrapDoesNotRequeueAfterProbeRefresh(t *testing.T) {
	engine := NewAccountQuotaMetadataBootstrap()
	before := testQuotaBootstrapAccount("probe-refresh", "codex", time.Time{})
	before.PlanType = "k12"
	engine.ObserveAccounts([]Account{before})
	select {
	case <-engine.wake:
	default:
		t.Fatal("initial account observation did not schedule reconciliation")
	}
	identity := newAccountModelProbeIdentity(before)
	engine.mu.Lock()
	delete(engine.pending, identity)
	engine.completed[identity] = struct{}{}
	engine.mu.Unlock()

	after := before
	after.PlanType = "free"
	after.Usage = &AccountUsageSnapshot{Codex: &CodexUsageSnapshot{
		PlanType:           "free",
		MetadataObservedAt: time.Date(2026, time.July, 27, 6, 0, 0, 0, time.UTC),
	}}
	engine.ObserveAccounts([]Account{after})
	if _, pending := engine.pending[identity]; pending {
		t.Fatal("successful probe refresh was requeued")
	}
	select {
	case <-engine.wake:
		t.Fatal("successful probe refresh woke the worker again")
	default:
	}
}

func TestQuotaMetadataBootstrapIgnoresStaleResultAfterReauthentication(t *testing.T) {
	engine := NewAccountQuotaMetadataBootstrap()
	started := make(chan struct{})
	release := make(chan struct{})
	engine.SetHandler(func(ctx context.Context, account Account, _ string) error {
		if account.AuthID != "old-auth" {
			t.Errorf("stale test handler received auth %q", account.AuthID)
		}
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	engine.Start()
	engine.Arm("management-secret")
	oldAccount := testQuotaBootstrapAccount("auth-index", "codex", time.Time{})
	oldAccount.AuthID = "old-auth"
	engine.ObserveAccounts([]Account{oldAccount})
	select {
	case <-started:
	case <-time.After(time.Second):
		engine.Shutdown()
		t.Fatal("quota metadata probe did not start")
	}

	newAccount := oldAccount
	newAccount.AuthID = "new-auth"
	engine.ObserveAccounts([]Account{newAccount})
	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		engine.mu.Lock()
		_, oldCompleted := engine.completed[newAccountModelProbeIdentity(oldAccount)]
		newIdentity := newAccountModelProbeIdentity(newAccount)
		_, newPending := engine.pending[newIdentity]
		engine.mu.Unlock()
		if !oldCompleted && newPending {
			break
		}
		if time.Now().After(deadline) {
			engine.Shutdown()
			t.Fatalf("stale result state old_completed=%v new_pending=%v", oldCompleted, newPending)
		}
		time.Sleep(10 * time.Millisecond)
	}
	engine.Shutdown()
}

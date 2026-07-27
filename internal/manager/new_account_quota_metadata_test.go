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

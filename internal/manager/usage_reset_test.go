package manager

import (
	"fmt"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// Reset must clear the overdraft injection evidence as well as the counters: the
// evidence is stored under the resolved storage key, so clearing only the
// identifier the caller passed left a pre-reset proof in place.
func TestUsageResetClearsInjectionEvidence(t *testing.T) {
	tracker := NewUsageTracker()
	tracker.Configure(Config{DataDir: t.TempDir()})
	defer tracker.Close()

	const authIndex = "auth-reset"
	tracker.Observe(cpaapi.UsageRecord{AuthIndex: authIndex, Detail: cpaapi.UsageDetail{TotalTokens: 120}})
	tracker.NoteOverdraftInjection(authIndex)
	if !tracker.overdraftInjectedRecently(authIndex, time.Now()) {
		t.Fatal("test setup did not record injection evidence")
	}

	if !tracker.Reset(authIndex) {
		t.Fatal("reset reported nothing to clear for an observed account")
	}
	if snapshot := tracker.Snapshot(authIndex); snapshot != nil {
		t.Fatalf("usage survived reset: %#v", snapshot)
	}
	if tracker.overdraftInjectedRecently(authIndex, time.Now()) {
		t.Fatal("injection evidence survived reset")
	}
}

// Resetting an identifier that was never observed must not allocate state, so a
// management client cannot grow the tracker map without bound.
func TestUsageResetDoesNotCreateUnknownEntries(t *testing.T) {
	tracker := NewUsageTracker()
	tracker.Configure(Config{DataDir: t.TempDir()})
	defer tracker.Close()

	tracker.Observe(cpaapi.UsageRecord{AuthIndex: "auth-known", Detail: cpaapi.UsageDetail{TotalTokens: 10}})
	tracker.mu.RLock()
	before := len(tracker.accounts)
	tracker.mu.RUnlock()

	for index := 0; index < 64; index++ {
		if tracker.Reset(fmt.Sprintf("auth-unknown-%d", index)) {
			t.Fatalf("unknown identifier %d reported a reset", index)
		}
	}

	tracker.mu.RLock()
	after := len(tracker.accounts)
	tracker.mu.RUnlock()
	if after != before {
		t.Fatalf("unknown resets grew the tracker: before=%d after=%d", before, after)
	}
	// The known account keeps working after the unknown resets.
	if snapshot := tracker.Snapshot("auth-known"); snapshot == nil || snapshot.TotalTokens != 10 {
		t.Fatalf("known account state changed: %#v", snapshot)
	}
}

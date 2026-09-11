package manager

import (
	"sync"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// The usage tracker publishes aggregates to request-path readers (overdraft
// gating) and to management readers (account snapshots) while usage callbacks
// update the same pointers in place. Readers must therefore clone under the
// tracker lock; this test fails under -race when a read path returns a live
// pointer to another goroutine.
func TestUsageTrackerConcurrentObserveAndReadAreRaceFree(t *testing.T) {
	tracker := NewUsageTracker()
	tracker.Configure(Config{DataDir: t.TempDir()})
	defer tracker.Close()

	const authIndex = "auth-race"
	stop := make(chan struct{})
	var wait sync.WaitGroup

	wait.Add(1)
	go func() {
		defer wait.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			tracker.Observe(cpaapi.UsageRecord{
				Provider:    "codex",
				AuthIndex:   authIndex,
				RequestedAt: time.Now(),
				Detail:      cpaapi.UsageDetail{TotalTokens: 32},
			})
		}
	}()

	// A second writer keeps the Codex windows and the overdraft cycles moving,
	// which is what the management and gating readers used to observe unlocked.
	wait.Add(1)
	go func() {
		defer wait.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			now := time.Now()
			reset := now.Add(time.Hour)
			tracker.ObserveCredentialUsage(authIndex, &CodexUsageSnapshot{
				FiveHour:           &UsageWindowSnapshot{UsedPercent: 42, ResetAt: &reset},
				SevenDay:           &UsageWindowSnapshot{UsedPercent: 63, ResetAt: &reset},
				ObservedAt:         now,
				MetadataObservedAt: now,
			})
			tracker.BeginOverdraftCycle(authIndex, "five_hour", now)
			tracker.StopOverdraftCycle(authIndex)
		}
	}()

	wait.Add(3)
	for reader := 0; reader < 3; reader++ {
		go func() {
			defer wait.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = tracker.Snapshot(authIndex)
				_ = tracker.AccountLifecycle(authIndex)
				_ = tracker.OverdraftGateState(authIndex)
				_ = tracker.UsageIdentity(authIndex)
			}
		}()
	}

	time.Sleep(300 * time.Millisecond)
	close(stop)
	wait.Wait()

	// The mutation must still be visible: cloning read state must not break the
	// published snapshot contract.
	snapshot := tracker.Snapshot(authIndex)
	if snapshot == nil || snapshot.TotalTokens == 0 {
		t.Fatalf("snapshot after concurrent updates = %#v, want observed tokens", snapshot)
	}
	if state := tracker.OverdraftGateState(authIndex); !state.Has {
		t.Fatal("overdraft gate state lost the observed account")
	}
}

package manager

import (
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestProviderRuntimeAggregatesByProviderAndModel(t *testing.T) {
	tracker := NewProviderRuntimeTracker(nil)
	tracker.now = func() time.Time { return time.Unix(10, 0) }
	tracker.ObserveUsage(cpaapi.UsageRecord{Provider: "openai", AuthIndex: "a", Model: "gpt-5.5", Detail: cpaapi.UsageDetail{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}})
	tracker.ObserveUsage(cpaapi.UsageRecord{Provider: "openai", AuthIndex: "a", Model: "gpt-5.5", Detail: cpaapi.UsageDetail{InputTokens: 1, OutputTokens: 4, TotalTokens: 5}})
	tracker.ObserveUsage(cpaapi.UsageRecord{Provider: "claude", AuthIndex: "a", Model: "claude-3", Detail: cpaapi.UsageDetail{TotalTokens: 9}})

	snapshots := tracker.Snapshot()
	if len(snapshots) != 2 {
		t.Fatalf("snapshots = %d, want 2", len(snapshots))
	}
	var found bool
	for _, snapshot := range snapshots {
		if snapshot.Provider == "openai" {
			found = true
			if snapshot.TotalTokens != 10 || len(snapshot.Models) != 1 || snapshot.Models[0].TotalTokens != 10 {
				t.Fatalf("unexpected openai aggregate: %+v", snapshot)
			}
		}
	}
	if !found {
		t.Fatal("openai snapshot missing")
	}
}

func TestProviderRuntimeActiveIsIdempotent(t *testing.T) {
	tracker := NewProviderRuntimeTracker(nil)
	request := cpaapi.RequestInterceptRequest{RequestID: "r1", ToFormat: "openai", Metadata: map[string]any{"selected_auth_index": "auth-a"}}
	tracker.ObserveRequest(request)
	tracker.ObserveRequest(request)
	tracker.Complete(cpaapi.RequestCompletion{RequestID: "r1"})
	tracker.Complete(cpaapi.RequestCompletion{RequestID: "r1"})
	snapshots := tracker.Snapshot()
	if len(snapshots) != 1 || snapshots[0].Active != 0 {
		t.Fatalf("unexpected active count: %+v", snapshots)
	}
}

func TestProviderRuntimeDoesNotExposeAPIKey(t *testing.T) {
	tracker := NewProviderRuntimeTracker(nil)
	secret := "sk-secret-provider-key"
	tracker.ObserveUsage(cpaapi.UsageRecord{Provider: "openai", APIKey: secret, Model: "gpt", Detail: cpaapi.UsageDetail{TotalTokens: 1}})
	encoded := ""
	for _, snapshot := range tracker.Snapshot() {
		encoded += snapshot.Identity + snapshot.Provider
	}
	if strings.Contains(encoded, secret) {
		t.Fatalf("secret leaked in snapshot: %s", encoded)
	}
}

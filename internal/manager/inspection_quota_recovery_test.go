package manager

import (
	"bytes"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestInspectionColdStartRunsAutoEnableWithoutManagementPage(t *testing.T) {
	now := time.Date(2026, time.August, 7, 8, 0, 0, 0, time.UTC)
	dataDir := t.TempDir()
	policy := defaultInspectionPolicy()
	policy.AutoEnable = true
	state := persistedInspectionState{
		Version: inspectionStoreVersion,
		Policy:  policy,
		Records: map[string]inspectionRecord{
			"inspection-account": {
				Result: InspectionResult{
					ID: "inspection-account", Name: "inspection.json", Provider: "codex",
					Health: InspectionHealthQuotaLimited, ReasonCode: "quota_exhausted",
					Disabled: true, OwnedDisable: true, Editable: true,
				},
				DisableReason: "quota_exhausted", DisabledAt: now.Add(-6 * time.Hour),
				DisabledName: "inspection.json", DisabledPath: "/auths/inspection.json",
				DisabledRecoverAfter: now.Add(-time.Minute),
			},
		},
		LastNativeRunAt: now,
	}
	if errSave := saveInspectionState(inspectionStorePath(dataDir), state); errSave != nil {
		t.Fatalf("save cold-start inspection state: %v", errSave)
	}

	host := inspectionEditableHost(true)
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	engine.now = func() time.Time { return now }
	engine.SetModelTestService(NewModelTestService(engine.accounts))
	var ownershipReady atomic.Bool
	engine.SetBackgroundWorkOwner(backgroundWorkOwnerFunc(ownershipReady.Load))
	engine.Configure(Config{DataDir: dataDir})
	defer engine.Shutdown()
	time.Sleep(250 * time.Millisecond)
	ownershipReady.Store(true)

	deadline := time.Now().Add(3 * time.Second)
	for {
		host.mu.Lock()
		saves := append([]cpaapi.HostAuthSaveRequest(nil), host.saves...)
		host.mu.Unlock()
		if len(saves) > 0 {
			if len(saves) != 1 || !bytes.Contains(saves[0].JSON, []byte(`"disabled":false`)) {
				t.Fatalf("cold-start auto-enable saves = %#v", saves)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cold-start auto-enable waited for a management page or the full scan interval")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if snapshot := engine.Snapshot(); snapshot.ActiveProbeArmed || snapshot.LastRun.AutoEnabled != 1 {
		t.Fatalf("cold-start inspection snapshot = %#v", snapshot)
	}
	if !engine.scheduledEnabled() {
		t.Fatal("auto-enable-only policy did not keep native scheduling active")
	}

	engine.Configure(Config{DataDir: dataDir})
	time.Sleep(50 * time.Millisecond)
	host.mu.Lock()
	saveCount := len(host.saves)
	host.mu.Unlock()
	if saveCount != 1 {
		t.Fatalf("same-store reconfigure queued %d cold-start mutations", saveCount)
	}
}

func TestAccountQuotaRecoveryAfterDisableUsesFreshRelevantCodexWindow(t *testing.T) {
	now := time.Date(2026, time.July, 28, 5, 30, 0, 0, time.UTC)
	disabledAt := now.Add(-30 * time.Minute)
	baseRecord := inspectionRecord{
		Result: InspectionResult{
			OwnedDisable: true, Disabled: true, Health: InspectionHealthQuotaLimited,
			ReasonCode: "quota_exhausted", QuotaWindow: InspectionQuotaWindowSevenDay,
		},
		DisableReason: "quota_exhausted",
		DisabledAt:    disabledAt,
	}
	baseAccount := Account{ID: "codex-account", Provider: "codex", Disabled: true, Editable: true}
	snapshot := func(observedAt time.Time, fiveHour, sevenDay *UsageWindowSnapshot) *AccountUsageSnapshot {
		return &AccountUsageSnapshot{Codex: &CodexUsageSnapshot{ObservedAt: observedAt, FiveHour: fiveHour, SevenDay: sevenDay}}
	}
	zero := &UsageWindowSnapshot{UsedPercent: 0, WindowMinutes: 10080}
	full := &UsageWindowSnapshot{UsedPercent: 100, WindowMinutes: 300}

	tests := []struct {
		name    string
		account Account
		record  inspectionRecord
		want    bool
	}{
		{name: "fresh seven day reset", account: func() Account {
			value := baseAccount
			value.Usage = snapshot(now.Add(-time.Minute), nil, zero)
			return value
		}(), record: baseRecord, want: true},
		{name: "fresh five hour reset", account: func() Account {
			value := baseAccount
			value.Usage = snapshot(now.Add(-time.Minute), zero, nil)
			return value
		}(), record: func() inspectionRecord {
			value := baseRecord
			value.Result.QuotaWindow = InspectionQuotaWindowFiveHour
			return value
		}(), want: true},
		{name: "five hour remains exhausted", account: func() Account {
			value := baseAccount
			value.Usage = snapshot(now.Add(-time.Minute), full, zero)
			return value
		}(), record: func() inspectionRecord {
			value := baseRecord
			value.Result.QuotaWindow = InspectionQuotaWindowFiveHour
			return value
		}()},
		{name: "multiple requires every window", account: func() Account {
			value := baseAccount
			value.Usage = snapshot(now.Add(-time.Minute), zero, full)
			return value
		}(), record: func() inspectionRecord {
			value := baseRecord
			value.Result.QuotaWindow = InspectionQuotaWindowMultiple
			return value
		}()},
		{name: "snapshot predates disable", account: func() Account {
			value := baseAccount
			value.Usage = snapshot(disabledAt.Add(-time.Minute), nil, zero)
			return value
		}(), record: baseRecord},
		{name: "stale snapshot", account: func() Account {
			value := baseAccount
			value.Usage = snapshot(now.Add(-quotaRecoveryEvidenceTTL-time.Second), nil, zero)
			return value
		}(), record: baseRecord},
		{name: "manual disable", account: func() Account {
			value := baseAccount
			value.Usage = snapshot(now.Add(-time.Minute), nil, zero)
			return value
		}(), record: func() inspectionRecord { value := baseRecord; value.Result.OwnedDisable = false; return value }()},
		{name: "fresh credential failure blocks reset", account: func() Account {
			value := baseAccount
			value.Usage = snapshot(now.Add(-time.Minute), nil, zero)
			return value
		}(), record: func() inspectionRecord {
			value := baseRecord
			value.Probe = inspectionProbeSignal{Kind: InspectionProbeKindCredential, ReasonCode: "authentication_failed", StatusCode: http.StatusUnauthorized, TestedAt: now.Add(-2 * time.Minute)}
			return value
		}()},
		{name: "other provider", account: func() Account {
			value := baseAccount
			value.Provider = "claude"
			value.Usage = snapshot(now.Add(-time.Minute), nil, zero)
			return value
		}(), record: baseRecord},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			window, recovered := accountQuotaRecoveredAfterDisable(test.account, test.record, now)
			if recovered != test.want {
				t.Fatalf("recovered = %t window=%q, want %t", recovered, window, test.want)
			}
			decision := decideInspection(test.account, test.record, now)
			if test.want && (decision.Health != InspectionHealthHealthy || decision.ReasonCode != "quota_reset" || decision.Recommendation != InspectionRecommendationEnable) {
				t.Fatalf("quota recovery decision = %#v", decision)
			}
		})
	}
}

func TestQuotaRecoveryObservationQueuesImmediateInspection(t *testing.T) {
	now := time.Date(2026, time.July, 28, 6, 0, 0, 0, time.UTC)
	disabledAt := now.Add(-time.Hour)
	policy := defaultInspectionPolicy()
	policy.Enabled = true
	policy.AutoEnable = true
	record := inspectionRecord{
		Result:        InspectionResult{OwnedDisable: true, Disabled: true, QuotaWindow: InspectionQuotaWindowFiveHour},
		DisableReason: "quota_exhausted", DisabledAt: disabledAt,
	}
	snapshot := &CodexUsageSnapshot{ObservedAt: now, FiveHour: &UsageWindowSnapshot{UsedPercent: 0, WindowMinutes: 300}}
	if !quotaSnapshotRequiresImmediateScan(policy, snapshot, record, now) {
		t.Fatal("fresh recovered quota snapshot did not request an immediate inspection")
	}
	policy.AutoEnable = false
	if quotaSnapshotRequiresImmediateScan(policy, snapshot, record, now) {
		t.Fatal("quota recovery queued inspection while automatic enable was disabled")
	}

	policy.AutoEnable = true
	usage := cpaapi.UsageRecord{AuthIndex: "codex-account", Provider: "codex", ResponseHeaders: http.Header{
		"X-Codex-Primary-Used-Percent":   []string{"0"},
		"X-Codex-Primary-Window-Minutes": []string{"300"},
	}}
	if !quotaUsageObservationRequiresImmediateScan(policy, usage, record, now) {
		t.Fatal("recovered passive quota headers did not request an immediate inspection")
	}
	record.Signal = inspectionSignal{ReasonCode: "invalid_credentials", Confidence: InspectionConfidenceHigh, LastFailureAt: now.Add(-time.Minute)}
	if quotaSnapshotRequiresImmediateScan(policy, snapshot, record, now) {
		t.Fatal("fresh invalid credentials did not block quota recovery inspection")
	}
}

func TestFreshQuotaRecoveryAutoEnablesBeforeStoredResetTime(t *testing.T) {
	now := time.Date(2026, time.July, 28, 7, 0, 0, 0, time.UTC)
	disabledAt := now.Add(-time.Hour)
	record := inspectionRecord{
		Result:        InspectionResult{OwnedDisable: true, Disabled: true, Health: InspectionHealthHealthy, QuotaWindow: InspectionQuotaWindowSevenDay},
		DisableReason: "quota_exhausted", DisabledAt: disabledAt, DisabledRecoverAfter: now.Add(6 * 24 * time.Hour),
	}
	account := Account{
		ID: "codex-account", Provider: "codex", Disabled: true, Editable: true,
		Usage: &AccountUsageSnapshot{Codex: &CodexUsageSnapshot{ObservedAt: now.Add(-time.Minute), SevenDay: &UsageWindowSnapshot{UsedPercent: 0, WindowMinutes: 10080}}},
	}
	policy := defaultInspectionPolicy()
	policy.AutoEnable = true
	if !shouldAutoEnableInspection(policy, account, record, now) {
		t.Fatal("fresh quota reset waited for the stale stored recovery time")
	}
	if reason := inspectionAutoEnableReason(account, record, now); reason != "quota_reset" {
		t.Fatalf("auto-enable reason = %q, want quota_reset", reason)
	}
}

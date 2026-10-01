package manager

import (
	"context"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// One probe pass touches the same credential three times (probe, allow-list
// write, inspection record). The follow-up steps must reuse the account the pass
// already resolved instead of walking the whole account list again.
func TestModelProbeTargetReusesTheResolvedAccount(t *testing.T) {
	host := pagedAuthHostFixture(1)
	app := &App{accounts: NewAccountService(host)}
	resolved := Account{ID: "auth-0000", AuthID: "account-0000.json", Name: "account-0000.json"}

	account, errTarget := app.modelProbeTarget(context.Background(), resolved, resolved.ID)
	if errTarget != nil || account.ID != resolved.ID {
		t.Fatalf("modelProbeTarget = %#v, %v", account, errTarget)
	}
	if host.listCalls != 0 {
		t.Fatalf("reusing the resolved account listed the host %d time(s)", host.listCalls)
	}

	// A caller without a resolved account still gets one.
	if _, errTarget := app.modelProbeTarget(context.Background(), Account{}, resolved.ID); errTarget != nil {
		t.Fatalf("modelProbeTarget without a hint: %v", errTarget)
	}
	if host.listCalls == 0 {
		t.Fatal("a caller without a resolved account must still resolve one")
	}
}

func TestInspectionRecordReusesTheProbeResolvedAccount(t *testing.T) {
	host := pagedAuthHostFixture(1)
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	const accountID = "auth-0000"
	resolved := Account{
		ID: accountID, AuthID: "account-0000.json", Name: "account-0000.json",
		Provider: "codex", Type: "codex", AccountType: "oauth", Source: "file",
		path: "/auths/account-0000.json",
	}

	errRecord := engine.RecordModelTest(context.Background(), ModelTestResult{
		AccountID: accountID, Provider: "codex", Model: "gpt-5.6-sol", Status: "available",
		ProbeKind: InspectionProbeKindModel, ReasonCode: "model_response_ok", StatusCode: 200,
		TestedAt: time.Now().UTC(), resolvedAccount: resolved,
	}, InspectionProbeSourceScan)
	if errRecord != nil {
		t.Fatalf("record model test: %v", errRecord)
	}
	host.mu.Lock()
	reads := host.getCalls[accountID]
	listCalls := host.listCalls
	host.mu.Unlock()
	if listCalls != 0 {
		t.Fatalf("the inspection record listed the host %d time(s) although the probe already resolved the account", listCalls)
	}
	// The snapshot is confirmed with one physical read, not a re-resolution.
	if reads != 1 {
		t.Fatalf("the inspection record read the credential %d time(s), want 1", reads)
	}
	if _, exists := engine.records[accountID]; !exists {
		t.Fatal("the model test was not recorded")
	}
}

// A probe runs for a while, so a credential deleted during it must not still be
// recorded from the probe-time snapshot.
func TestInspectionRecordRejectsACredentialDeletedDuringTheProbe(t *testing.T) {
	host := pagedAuthHostFixture(1)
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	const accountID = "auth-0000"
	resolved := Account{
		ID: accountID, AuthID: "account-0000.json", Name: "account-0000.json",
		Provider: "codex", Type: "codex", AccountType: "oauth", Source: "file",
		path: "/auths/account-0000.json",
	}
	host.mu.Lock()
	host.entries = nil
	host.details = map[string]cpaapi.HostAuthGetResponse{}
	host.mu.Unlock()

	errRecord := engine.RecordModelTest(context.Background(), ModelTestResult{
		AccountID: accountID, Provider: "codex", Model: "gpt-5.6-sol", Status: "available",
		ProbeKind: InspectionProbeKindModel, ReasonCode: "model_response_ok", StatusCode: 200,
		TestedAt: time.Now().UTC(), resolvedAccount: resolved,
	}, InspectionProbeSourceScan)
	if errRecord == nil {
		t.Fatal("a model test for a deleted credential was recorded")
	}
	if _, exists := engine.records[accountID]; exists {
		t.Fatal("a record was written for a credential that no longer exists")
	}
}

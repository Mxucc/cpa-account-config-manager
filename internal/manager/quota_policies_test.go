package manager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestQuotaPolicyServiceTreatsMissingStoreAsEmpty(t *testing.T) {
	service := NewQuotaPolicyService()
	service.Configure(Config{DataDir: t.TempDir()})
	snapshot := service.Snapshot()
	if snapshot.StorageError != "" || len(snapshot.Accounts) != 0 || len(snapshot.Providers) != 0 {
		t.Fatalf("missing store snapshot = %#v", snapshot)
	}
}

func TestQuotaPolicyServicePersistsAndReloadsPolicies(t *testing.T) {
	dir := t.TempDir()
	service := NewQuotaPolicyService()
	service.Configure(Config{DataDir: dir})
	accountLimit := 85
	providerBudget := int64(123456)
	providerLimit := 90
	provider15sLimit := 2
	if err := service.SetAccountPolicy("auth-a", AccountQuotaPolicy{FiveHour: QuotaWindowPolicy{LimitPercent: &accountLimit}}); err != nil {
		t.Fatalf("SetAccountPolicy() error = %v", err)
	}
	if err := service.SetProviderPolicy(ProviderQuotaPolicy{Key: "openai:channel-a", Concurrency: intPointer(4), Concurrency15s: &provider15sLimit, FiveHour: QuotaWindowPolicy{TotalTokens: &providerBudget, LimitPercent: &providerLimit}}); err != nil {
		t.Fatalf("SetProviderPolicy() error = %v", err)
	}

	reloaded := NewQuotaPolicyService()
	reloaded.Configure(Config{DataDir: dir})
	account := reloaded.AccountPolicy("auth-a")
	if account.FiveHour.LimitPercent == nil || *account.FiveHour.LimitPercent != 85 {
		t.Fatalf("reloaded account policy = %#v", account)
	}
	provider := reloaded.ProviderPolicy("openai:channel-a")
	if provider.Concurrency == nil || *provider.Concurrency != 4 || provider.Concurrency15s == nil || *provider.Concurrency15s != 2 || provider.FiveHour.TotalTokens == nil || *provider.FiveHour.TotalTokens != providerBudget {
		t.Fatalf("reloaded provider policy = %#v", provider)
	}
}

func TestQuotaPolicyServiceDeletesEmptyPolicies(t *testing.T) {
	dir := t.TempDir()
	service := NewQuotaPolicyService()
	service.Configure(Config{DataDir: dir})
	limit := 50
	if err := service.SetAccountPolicy("auth-a", AccountQuotaPolicy{SevenDay: QuotaWindowPolicy{LimitPercent: &limit}}); err != nil {
		t.Fatal(err)
	}
	if err := service.SetAccountPolicy("auth-a", AccountQuotaPolicy{}); err != nil {
		t.Fatal(err)
	}
	if got := service.Snapshot(); len(got.Accounts) != 0 {
		t.Fatalf("empty account policy was not deleted: %#v", got)
	}

	provider := ProviderQuotaPolicy{Key: "openai:channel-a", Label: "demo", Concurrency: intPointer(2)}
	if err := service.SetProviderPolicy(provider); err != nil {
		t.Fatal(err)
	}
	provider = ProviderQuotaPolicy{Key: provider.Key}
	if err := service.SetProviderPolicy(provider); err != nil {
		t.Fatal(err)
	}
	if len(service.Snapshot().Providers) != 0 {
		t.Fatalf("empty provider policy was not deleted: %#v", service.Snapshot())
	}
}

func TestQuotaPolicyServiceValidatesRangesAndStorage(t *testing.T) {
	service := NewQuotaPolicyService()
	service.Configure(Config{DataDir: t.TempDir()})
	tooHigh := 101
	if err := service.SetAccountPolicy("auth-a", AccountQuotaPolicy{FiveHour: QuotaWindowPolicy{LimitPercent: &tooHigh}}); err == nil {
		t.Fatal("expected percent validation error")
	}
	negative := int64(-1)
	if err := service.SetProviderPolicy(ProviderQuotaPolicy{Key: "x", SevenDay: QuotaWindowPolicy{TotalTokens: &negative}}); err == nil {
		t.Fatal("expected token budget validation error")
	}
	if err := service.SetProviderPolicy(ProviderQuotaPolicy{Key: "x", Concurrency: intPointer(1001)}); err == nil {
		t.Fatal("expected concurrency validation error")
	}
	if err := service.SetProviderPolicy(ProviderQuotaPolicy{Key: "x", Concurrency15s: intPointer(1001)}); err == nil {
		t.Fatal("expected 15-second concurrency validation error")
	}

	path := filepath.Join(t.TempDir(), "quota-policies.json")
	if err := os.WriteFile(path, []byte(`{"version":999}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadQuotaPolicies(path)
	if err == nil || loaded != nil {
		t.Fatalf("invalid version load = %#v, %v", loaded, err)
	}
}

func TestQuotaPolicySnapshotJSONIsStable(t *testing.T) {
	limit := 75
	snapshot := QuotaPolicySnapshot{Accounts: map[string]AccountQuotaPolicy{"auth-a": {FiveHour: QuotaWindowPolicy{LimitPercent: &limit}}}, Providers: []ProviderQuotaPolicy{}}
	if _, err := json.Marshal(snapshot); err != nil {
		t.Fatalf("snapshot JSON error = %v", err)
	}
}

func TestQuotaPolicyServiceResolvesProviderPolicyUnambiguously(t *testing.T) {
	service := NewQuotaPolicyService()
	service.Configure(Config{DataDir: t.TempDir()})
	if err := service.SetProviderPolicy(ProviderQuotaPolicy{Key: "openai:auth-a", Concurrency: intPointer(4)}); err != nil {
		t.Fatal(err)
	}
	if policy, ok := service.ResolveProviderPolicy("openai", "auth-a", "auth-index:auth-a"); !ok || policy.Concurrency == nil || *policy.Concurrency != 4 {
		t.Fatalf("exact provider policy was not resolved: %#v ok=%v", policy, ok)
	}
	if err := service.SetProviderPolicy(ProviderQuotaPolicy{Key: "openai:auth-b", Concurrency: intPointer(5)}); err != nil {
		t.Fatal(err)
	}
	if policy, ok := service.ResolveProviderPolicy("openai", "", ""); ok {
		t.Fatalf("ambiguous provider policy was resolved: %#v", policy)
	}
}

func intPointer(value int) *int { return &value }

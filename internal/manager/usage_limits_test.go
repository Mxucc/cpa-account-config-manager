package manager

import (
	"math"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

type usageLimitTestReader struct {
	snapshots map[string]*AccountUsageSnapshot
}

func (r *usageLimitTestReader) Snapshot(identity string) *AccountUsageSnapshot {
	if r == nil {
		return nil
	}
	return r.snapshots[identity]
}

type usageLimitTestCalculator struct {
	amountByModel map[string]int64
}

func (c usageLimitTestCalculator) Enabled() bool { return true }
func (c usageLimitTestCalculator) Snapshot() CreditPricingSnapshot {
	return CreditPricingSnapshot{Source: "test"}
}
func (c usageLimitTestCalculator) Calculate(record cpaapi.UsageRecord) CreditCharge {
	amount := c.amountByModel[record.Model]
	if amount == 0 {
		amount = c.amountByModel["*"]
	}
	return CreditCharge{Enabled: amount > 0, Rated: amount > 0, AmountNanos: amount, ObservedAt: time.Now().UTC()}
}

func usageLimitRequest(model, identity, provider string) cpaapi.RequestInterceptRequest {
	return cpaapi.RequestInterceptRequest{
		Model: model, RequestedModel: model,
		Metadata: map[string]any{"selected_auth_id": identity, "provider": provider},
	}
}

func usageLimitAccountSnapshot(fiveHour, sevenDay float64) *AccountUsageSnapshot {
	return &AccountUsageSnapshot{Codex: &CodexUsageSnapshot{
		FiveHour: &UsageWindowSnapshot{UsedPercent: fiveHour},
		SevenDay: &UsageWindowSnapshot{UsedPercent: sevenDay},
	}}
}

func configureUsageLimitsTestService(t *testing.T, reader UsageSnapshotReader, calculator UsageCreditCalculator) *UsageLimitService {
	t.Helper()
	service := NewUsageLimitService(reader, calculator)
	service.Configure(Config{DataDir: t.TempDir()})
	return service
}

func mustSetUsageLimit(t *testing.T, service *UsageLimitService, scope UsageLimitsScope, config UsageLimitsConfig) {
	t.Helper()
	if _, err := service.Set(scope, config); err != nil {
		t.Fatalf("Set(%s/%s) error = %v", scope.Kind, scope.ID, err)
	}
}

func TestUsageLimitAccountFiveHourAndSevenDayBlockRequests(t *testing.T) {
	reader := &usageLimitTestReader{snapshots: map[string]*AccountUsageSnapshot{
		"five-hour": usageLimitAccountSnapshot(80, 10),
		"seven-day": usageLimitAccountSnapshot(10, 90),
	}}
	service := configureUsageLimitsTestService(t, reader, nil)
	mustSetUsageLimit(t, service, AccountUsageLimitsScope("five-hour"), UsageLimitsConfig{Enabled: true, Total: &UsageLimitRule{Enabled: true, Basis: UsageLimitBasisAccount, Window: UsageLimitWindowFiveHour, Percent: 80}})
	response, changed := service.InterceptRequest(usageLimitRequest("gpt-5.4", "five-hour", "openai"))
	if !changed || !response.Terminate || response.StatusCode != 429 || !strings.Contains(string(response.ResponseBody), `"basis":"total_account"`) {
		t.Fatalf("five-hour interception = changed:%v response:%#v", changed, response)
	}
	mustSetUsageLimit(t, service, AccountUsageLimitsScope("seven-day"), UsageLimitsConfig{Enabled: true, Total: &UsageLimitRule{Enabled: true, Basis: UsageLimitBasisAccount, Window: UsageLimitWindowSevenDay, Percent: 90}})
	response, changed = service.InterceptRequest(usageLimitRequest("gpt-5.4", "seven-day", "openai"))
	if !changed || !response.Terminate || !strings.Contains(string(response.ResponseBody), `"basis":"total_account"`) {
		t.Fatalf("seven-day interception = changed:%v response:%#v", changed, response)
	}
}

func TestUsageLimitAccountScopeIsolated(t *testing.T) {
	reader := &usageLimitTestReader{snapshots: map[string]*AccountUsageSnapshot{
		"account-a": usageLimitAccountSnapshot(90, 0),
		"account-b": usageLimitAccountSnapshot(10, 0),
	}}
	service := configureUsageLimitsTestService(t, reader, nil)
	config := UsageLimitsConfig{Enabled: true, Total: &UsageLimitRule{Enabled: true, Basis: UsageLimitBasisAccount, Window: UsageLimitWindowFiveHour, Percent: 80}}
	mustSetUsageLimit(t, service, AccountUsageLimitsScope("account-a"), config)
	if response, changed := service.InterceptRequest(usageLimitRequest("gpt-5.4", "account-a", "openai")); !changed || !response.Terminate {
		t.Fatalf("account A should be blocked: changed:%v response:%#v", changed, response)
	}
	if response, changed := service.InterceptRequest(usageLimitRequest("gpt-5.4", "account-b", "openai")); changed || response.Terminate {
		t.Fatalf("account B was affected by account A: changed:%v response:%#v", changed, response)
	}
}

func TestUsageLimitProviderScopeIsolated(t *testing.T) {
	calculator := usageLimitTestCalculator{amountByModel: map[string]int64{"gpt-5.4": 600_000_000}}
	service := configureUsageLimitsTestService(t, nil, calculator)
	mustSetUsageLimit(t, service, ProviderUsageLimitsScope("OpenAI"), UsageLimitsConfig{Enabled: true, Total: &UsageLimitRule{Enabled: true, Basis: UsageLimitBasisCredit, AmountUSD: 0.5}})
	service.ObserveUsage(cpaapi.UsageRecord{Provider: "openai", Model: "gpt-5.4", AuthID: "provider-a"})
	if response, changed := service.InterceptRequest(usageLimitRequest("gpt-5.4", "provider-a", "openai")); !changed || !response.Terminate {
		t.Fatalf("provider openai should be blocked: changed:%v response:%#v", changed, response)
	}
	if response, changed := service.InterceptRequest(usageLimitRequest("gpt-5.4", "provider-b", "anthropic")); changed || response.Terminate {
		t.Fatalf("provider anthropic was affected by provider openai: changed:%v response:%#v", changed, response)
	}
}

func TestUsageLimitCreditTotalAndModelWithinTotal(t *testing.T) {
	calculator := usageLimitTestCalculator{amountByModel: map[string]int64{"gpt-5.4": 600_000_000}}
	service := configureUsageLimitsTestService(t, nil, calculator)
	mustSetUsageLimit(t, service, AccountUsageLimitsScope("account-1"), UsageLimitsConfig{Enabled: true,
		Total:  &UsageLimitRule{Enabled: true, Basis: UsageLimitBasisCredit, AmountUSD: 1},
		Models: []UsageModelLimit{{Model: "gpt-5.4", WithinTotal: true, Rule: UsageLimitRule{Enabled: true, Basis: UsageLimitBasisCredit, AmountUSD: 10}}},
	})
	service.ObserveUsage(cpaapi.UsageRecord{Model: "gpt-5.4", AuthID: "account-1"})
	if got := service.Get(AccountUsageLimitsScope("account-1")).CreditUsedUSD; math.Abs(got-0.6) > 1e-9 {
		t.Fatalf("CreditUsedUSD = %f, want 0.6", got)
	}
	if response, changed := service.InterceptRequest(usageLimitRequest("gpt-5.4", "account-1", "openai")); changed || response.Terminate {
		t.Fatalf("within-total request blocked before total was reached: changed:%v response:%#v", changed, response)
	}
	calculator.amountByModel["gpt-5.4"] = 500_000_000
	service.ObserveUsage(cpaapi.UsageRecord{Model: "gpt-5.4", AuthID: "account-1"})
	response, changed := service.InterceptRequest(usageLimitRequest("gpt-5.4", "account-1", "openai"))
	if !changed || !response.Terminate || !strings.Contains(string(response.ResponseBody), `"basis":"total_credit"`) {
		t.Fatalf("total credit interception = changed:%v response:%#v", changed, response)
	}
}

func TestUsageLimitModelOutsideTotalStillEnforcesModelLimit(t *testing.T) {
	calculator := usageLimitTestCalculator{amountByModel: map[string]int64{"gpt-5.4": 600_000_000}}
	service := configureUsageLimitsTestService(t, nil, calculator)
	mustSetUsageLimit(t, service, AccountUsageLimitsScope("account-1"), UsageLimitsConfig{Enabled: true,
		Total:  &UsageLimitRule{Enabled: true, Basis: UsageLimitBasisCredit, AmountUSD: 5},
		Models: []UsageModelLimit{{Model: "gpt-5.4", WithinTotal: false, Rule: UsageLimitRule{Enabled: true, Basis: UsageLimitBasisCredit, AmountUSD: 0.5}}},
	})
	service.ObserveUsage(cpaapi.UsageRecord{Model: "gpt-5.4", AuthID: "account-1"})
	response, changed := service.InterceptRequest(usageLimitRequest("gpt-5.4", "account-1", "openai"))
	if !changed || !response.Terminate || !strings.Contains(string(response.ResponseBody), `"basis":"model_credit"`) {
		t.Fatalf("outside-total model interception = changed:%v response:%#v", changed, response)
	}
}

func TestUsageLimitCreditCountersPersistAndDirectorySwitchResetsMissingStore(t *testing.T) {
	dataDir := t.TempDir()
	calculator := usageLimitTestCalculator{amountByModel: map[string]int64{"gpt-5.4": 250_000_000}}
	service := NewUsageLimitService(nil, calculator)
	service.Configure(Config{DataDir: dataDir})
	mustSetUsageLimit(t, service, AccountUsageLimitsScope("account-1"), UsageLimitsConfig{Enabled: true, Total: &UsageLimitRule{Enabled: true, Basis: UsageLimitBasisCredit, AmountUSD: 5}})
	service.ObserveUsage(cpaapi.UsageRecord{Model: "gpt-5.4", AuthID: "account-1"})
	reloaded := NewUsageLimitService(nil, calculator)
	reloaded.Configure(Config{DataDir: dataDir})
	if got := reloaded.Get(AccountUsageLimitsScope("account-1")).CreditUsedUSD; math.Abs(got-0.25) > 1e-9 {
		t.Fatalf("reloaded CreditUsedUSD = %f, want 0.25", got)
	}
	reloaded.Configure(Config{DataDir: t.TempDir()})
	if got := reloaded.Get(AccountUsageLimitsScope("account-1")).CreditUsedUSD; got != 0 {
		t.Fatalf("directory switch CreditUsedUSD = %f, want 0", got)
	}
}

func TestUsageLimitProviderRejectsAccountPercentageRules(t *testing.T) {
	service := configureUsageLimitsTestService(t, nil, nil)
	provider := ProviderUsageLimitsScope("openai")
	if _, err := service.Set(provider, UsageLimitsConfig{Enabled: true, Total: &UsageLimitRule{
		Enabled: true, Basis: UsageLimitBasisAccount, Window: UsageLimitWindowFiveHour, Percent: 80,
	}}); err == nil || !strings.Contains(err.Error(), "provider total usage limit only supports credit amount") {
		t.Fatalf("provider account total rule error = %v", err)
	}
	if _, err := service.Set(provider, UsageLimitsConfig{Enabled: true, Models: []UsageModelLimit{{
		Model: "gpt-5.4", WithinTotal: true, Rule: UsageLimitRule{
			Enabled: true, Basis: UsageLimitBasisAccount, Window: UsageLimitWindowSevenDay, Percent: 90,
		},
	}}}); err == nil || !strings.Contains(err.Error(), "provider model \"gpt-5.4\" usage limit only supports credit amount") {
		t.Fatalf("provider account model rule error = %v", err)
	}
}

func TestUsageLimitProviderAcceptsCreditRules(t *testing.T) {
	service := configureUsageLimitsTestService(t, nil, nil)
	provider := ProviderUsageLimitsScope("openai")
	if _, err := service.Set(provider, UsageLimitsConfig{Enabled: true,
		Total: &UsageLimitRule{Enabled: true, Basis: UsageLimitBasisCredit, AmountUSD: 10},
		Models: []UsageModelLimit{{Model: "gpt-5.4", WithinTotal: false, Rule: UsageLimitRule{
			Enabled: true, Basis: UsageLimitBasisCredit, AmountUSD: 2,
		}}},
	}); err != nil {
		t.Fatalf("provider credit rules error = %v", err)
	}
	config := service.Get(provider).Config
	if config.Total == nil || config.Total.Basis != UsageLimitBasisCredit || config.Models[0].Rule.Basis != UsageLimitBasisCredit {
		t.Fatalf("provider credit config = %#v", config)
	}
}

func TestUsageLimitProviderStoredAccountRulesAreNotEnforced(t *testing.T) {
	service := configureUsageLimitsTestService(t, &usageLimitTestReader{snapshots: map[string]*AccountUsageSnapshot{
		"provider-account": usageLimitAccountSnapshot(100, 100),
	}}, nil)
	key, err := usageLimitScopeKey(ProviderUsageLimitsScope("openai"))
	if err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	service.configs = cloneUsageLimitConfigs(map[string]UsageLimitsConfig{key: {Enabled: true, Total: &UsageLimitRule{
		Enabled: true, Basis: UsageLimitBasisAccount, Window: UsageLimitWindowFiveHour, Percent: 1,
	}}})
	service.mu.Unlock()
	if response, changed := service.InterceptRequest(usageLimitRequest("gpt-5.4", "provider-account", "openai")); changed || response.Terminate {
		t.Fatalf("stored provider account rule was enforced: changed:%v response:%#v", changed, response)
	}
	if got := service.Get(ProviderUsageLimitsScope("openai")).Config.Total; got == nil || got.Basis != UsageLimitBasisCredit || got.Enabled {
		t.Fatalf("stored provider account rule was not sanitized: %#v", got)
	}
}

func TestNormalizeUsageLimitsProtectsInvalidNumbers(t *testing.T) {
	config := normalizeUsageLimitsConfig(UsageLimitsConfig{Enabled: true,
		Total:  &UsageLimitRule{Enabled: true, Basis: UsageLimitBasisCredit, AmountUSD: math.NaN()},
		Models: []UsageModelLimit{{Model: " GPT-5.4 ", Rule: UsageLimitRule{Basis: UsageLimitBasisAccount, Percent: math.Inf(1)}}, {Model: "gpt-5.4", Rule: UsageLimitRule{Basis: UsageLimitBasisCredit, AmountUSD: 1}}},
	})
	if config.Total == nil || config.Total.AmountUSD != 0 || len(config.Models) != 1 || config.Models[0].Model != "GPT-5.4" || config.Models[0].Rule.Percent != 0 {
		t.Fatalf("normalized config = %#v", config)
	}
	if err := validateUsageLimitsConfig(config); err == nil {
		t.Fatal("invalid normalized config unexpectedly validated")
	}
}

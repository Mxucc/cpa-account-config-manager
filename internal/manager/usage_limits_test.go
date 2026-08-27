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

func usageLimitRequest(model, identity string) cpaapi.RequestInterceptRequest {
	return cpaapi.RequestInterceptRequest{
		Model:          model,
		RequestedModel: model,
		Metadata:       map[string]any{"selected_auth_id": identity},
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

func TestUsageLimitAccountFiveHourAndSevenDayBlockRequests(t *testing.T) {
	reader := &usageLimitTestReader{snapshots: map[string]*AccountUsageSnapshot{
		"five-hour": usageLimitAccountSnapshot(80, 10),
		"seven-day": usageLimitAccountSnapshot(10, 90),
	}}
	service := configureUsageLimitsTestService(t, reader, nil)

	fiveHour := 80.0
	_, err := service.Set(UsageLimitsConfig{Enabled: true, Total: &UsageLimitRule{
		Enabled: true, Basis: UsageLimitBasisAccount, Window: UsageLimitWindowFiveHour, Percent: fiveHour,
	}})
	if err != nil {
		t.Fatalf("Set(five-hour) error = %v", err)
	}
	response, changed := service.InterceptRequest(usageLimitRequest("gpt-5.4", "five-hour"))
	if !changed || !response.Terminate || response.StatusCode != 429 || !strings.Contains(string(response.ResponseBody), `"basis":"total_account"`) {
		t.Fatalf("five-hour interception = changed:%v response:%#v", changed, response)
	}

	_, err = service.Set(UsageLimitsConfig{Enabled: true, Total: &UsageLimitRule{
		Enabled: true, Basis: UsageLimitBasisAccount, Window: UsageLimitWindowSevenDay, Percent: 90,
	}})
	if err != nil {
		t.Fatalf("Set(seven-day) error = %v", err)
	}
	response, changed = service.InterceptRequest(usageLimitRequest("gpt-5.4", "seven-day"))
	if !changed || !response.Terminate || !strings.Contains(string(response.ResponseBody), `"basis":"total_account"`) {
		t.Fatalf("seven-day interception = changed:%v response:%#v", changed, response)
	}
}

func TestUsageLimitMissingAccountUsageDoesNotBlock(t *testing.T) {
	service := configureUsageLimitsTestService(t, &usageLimitTestReader{}, nil)
	_, err := service.Set(UsageLimitsConfig{Enabled: true, Total: &UsageLimitRule{
		Enabled: true, Basis: UsageLimitBasisAccount, Window: UsageLimitWindowFiveHour, Percent: 1,
	}})
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if response, changed := service.InterceptRequest(usageLimitRequest("gpt-5.4", "missing")); changed || response.Terminate {
		t.Fatalf("missing usage unexpectedly blocked request: changed:%v response:%#v", changed, response)
	}
}

func TestUsageLimitCreditTotalAndModelWithinTotal(t *testing.T) {
	calculator := usageLimitTestCalculator{amountByModel: map[string]int64{"gpt-5.4": 600_000_000}}
	service := configureUsageLimitsTestService(t, nil, calculator)
	_, err := service.Set(UsageLimitsConfig{Enabled: true,
		Total: &UsageLimitRule{Enabled: true, Basis: UsageLimitBasisCredit, AmountUSD: 1},
		Models: []UsageModelLimit{{Model: "gpt-5.4", WithinTotal: true, Rule: UsageLimitRule{
			Enabled: true, Basis: UsageLimitBasisCredit, AmountUSD: 10,
		}}},
	})
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	service.ObserveUsage(cpaapi.UsageRecord{Model: "gpt-5.4", AuthIndex: "account-1"})
	if got := service.Snapshot().CreditUsedUSD; math.Abs(got-0.6) > 1e-9 {
		t.Fatalf("CreditUsedUSD = %f, want 0.6", got)
	}
	if response, changed := service.InterceptRequest(usageLimitRequest("gpt-5.4", "account-1")); changed || response.Terminate {
		t.Fatalf("within-total request blocked before total was reached: changed:%v response:%#v", changed, response)
	}

	calculator.amountByModel["gpt-5.4"] = 500_000_000
	service.ObserveUsage(cpaapi.UsageRecord{Model: "gpt-5.4", AuthIndex: "account-1"})
	response, changed := service.InterceptRequest(usageLimitRequest("gpt-5.4", "account-1"))
	if !changed || !response.Terminate || !strings.Contains(string(response.ResponseBody), `"basis":"total_credit"`) {
		t.Fatalf("total credit interception = changed:%v response:%#v", changed, response)
	}
}

func TestUsageLimitModelOutsideTotalStillEnforcesModelLimit(t *testing.T) {
	calculator := usageLimitTestCalculator{amountByModel: map[string]int64{"gpt-5.4": 600_000_000}}
	service := configureUsageLimitsTestService(t, nil, calculator)
	_, err := service.Set(UsageLimitsConfig{Enabled: true,
		Total: &UsageLimitRule{Enabled: true, Basis: UsageLimitBasisCredit, AmountUSD: 0.5},
		Models: []UsageModelLimit{{Model: "gpt-5.4", WithinTotal: false, Rule: UsageLimitRule{
			Enabled: true, Basis: UsageLimitBasisCredit, AmountUSD: 0.5,
		}}},
	})
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	service.ObserveUsage(cpaapi.UsageRecord{Model: "gpt-5.4", AuthIndex: "account-1"})
	response, changed := service.InterceptRequest(usageLimitRequest("gpt-5.4", "account-1"))
	if !changed || !response.Terminate || !strings.Contains(string(response.ResponseBody), `"basis":"model_credit"`) {
		t.Fatalf("outside-total model interception = changed:%v response:%#v", changed, response)
	}
}

func TestUsageLimitCreditCountersPersistAndDirectorySwitchResetsMissingStore(t *testing.T) {
	dataDir := t.TempDir()
	calculator := usageLimitTestCalculator{amountByModel: map[string]int64{"gpt-5.4": 250_000_000}}
	service := NewUsageLimitService(nil, calculator)
	service.Configure(Config{DataDir: dataDir})
	_, err := service.Set(UsageLimitsConfig{Enabled: true, Total: &UsageLimitRule{
		Enabled: true, Basis: UsageLimitBasisCredit, AmountUSD: 5,
	}})
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	service.ObserveUsage(cpaapi.UsageRecord{Model: "gpt-5.4", AuthIndex: "account-1"})

	reloaded := NewUsageLimitService(nil, calculator)
	reloaded.Configure(Config{DataDir: dataDir})
	if got := reloaded.Snapshot().CreditUsedUSD; math.Abs(got-0.25) > 1e-9 {
		t.Fatalf("reloaded CreditUsedUSD = %f, want 0.25", got)
	}
	reloaded.Configure(Config{DataDir: t.TempDir()})
	if got := reloaded.Snapshot().CreditUsedUSD; got != 0 {
		t.Fatalf("directory switch CreditUsedUSD = %f, want 0", got)
	}
}

func TestNormalizeUsageLimitsProtectsInvalidNumbers(t *testing.T) {
	config := normalizeUsageLimitsConfig(UsageLimitsConfig{
		Enabled: true,
		Total:   &UsageLimitRule{Enabled: true, Basis: UsageLimitBasisCredit, AmountUSD: math.NaN()},
		Models: []UsageModelLimit{
			{Model: " GPT-5.4 ", Rule: UsageLimitRule{Basis: UsageLimitBasisAccount, Percent: math.Inf(1)}},
			{Model: "gpt-5.4", Rule: UsageLimitRule{Basis: UsageLimitBasisCredit, AmountUSD: 1}},
		},
	})
	if config.Total == nil || config.Total.AmountUSD != 0 || len(config.Models) != 1 || config.Models[0].Model != "GPT-5.4" || config.Models[0].Rule.Percent != 0 {
		t.Fatalf("normalized config = %#v", config)
	}
	if err := validateUsageLimitsConfig(config); err == nil {
		t.Fatal("invalid normalized config unexpectedly validated")
	}
}

package provider

import (
	"fmt"

	"cpa-account-config-manager/internal/cpaapi"
)

// QuotaResponse maps a normalized account onto CPA's quota contract, which the
// new Management Center renders as a quota card. The mapping is deliberately
// lossless in one direction only: a window the provider did not report is left
// out, and an account with no windows still answers with its plan and summary so
// the card shows something true instead of an empty 0%.
func QuotaResponse(account Account) cpaapi.QuotaFetchResponse {
	response := cpaapi.QuotaFetchResponse{}
	if account.Plan.Cycle.Kind != CycleNone || account.Plan.Name != "" || account.Plan.Tier != "" {
		response.Subscription = &cpaapi.QuotaSubscription{
			Plan:     firstNonEmpty(account.Plan.ID, account.Plan.Tier),
			TierName: firstNonEmpty(account.Plan.Name, account.Plan.Tier),
			TierID:   account.Plan.Tier,
		}
	}
	group := cpaapi.QuotaGroup{DisplayName: "Allowance"}
	for _, window := range account.Quota {
		if window.UsedPercent == nil && window.RemainingPercent == nil {
			// A wallet window carries a balance and no share; it belongs in the
			// summary, not in a meter.
			continue
		}
		remaining := 0.0
		if window.RemainingPercent != nil {
			remaining = *window.RemainingPercent / 100
		} else if window.UsedPercent != nil {
			remaining = 1 - *window.UsedPercent/100
		}
		if remaining < 0 {
			remaining = 0
		}
		if remaining > 1 {
			remaining = 1
		}
		bucket := cpaapi.QuotaBucket{
			Window:            firstNonEmpty(window.Label, window.SourceWindow, window.ID),
			RemainingFraction: remaining,
			Description:       window.Note,
		}
		if window.ResetAt != nil {
			bucket.ResetTime = window.ResetAt.UTC().Format("2006-01-02T15:04:05Z07:00")
		}
		if window.Limit != nil {
			bucket.Description = joinDescription(bucket.Description, fmt.Sprintf("Limit %s %.2f", firstNonEmpty(window.Unit, "USD"), *window.Limit))
		}
		group.Buckets = append(group.Buckets, bucket)
	}
	if len(group.Buckets) > 0 {
		response.Groups = []cpaapi.QuotaGroup{group}
	}
	for _, metric := range account.Metrics {
		response.Summary = append(response.Summary, cpaapi.QuotaMetric{
			Key:      metric.Key,
			Label:    metric.Label,
			Value:    metric.Value,
			Unit:     metric.Unit,
			Format:   metric.Format,
			Currency: metric.Currency,
		})
	}
	if account.Billing.Remaining != nil {
		response.Summary = append(response.Summary, cpaapi.QuotaMetric{
			Key:      "remaining",
			Label:    "Remaining",
			Value:    *account.Billing.Remaining,
			Unit:     firstNonEmpty(account.Billing.Currency, "USD"),
			Format:   "currency",
			Currency: firstNonEmpty(account.Billing.Currency, "USD"),
		})
	}
	if account.Billing.Used != nil {
		response.Summary = append(response.Summary, cpaapi.QuotaMetric{
			Key:      "used",
			Label:    "Used",
			Value:    *account.Billing.Used,
			Unit:     firstNonEmpty(account.Billing.Currency, "USD"),
			Format:   "currency",
			Currency: firstNonEmpty(account.Billing.Currency, "USD"),
		})
	}
	return response
}

func joinDescription(existing, extra string) string {
	if existing == "" {
		return extra
	}
	return existing + " · " + extra
}

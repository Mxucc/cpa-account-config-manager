package manager

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// Cline Pass quota holds.
//
// The gateway answers an account that is out of allowance with HTTP 429 and names its
// own window: "Error 429: You have reached your weekly Clinepass limit. The limit resets
// in 3d 13h, please try again later." CPA keeps selecting the channel row that carries
// that credential, so every routed request fails while a sibling account of the same
// gateway - which publishes the same models - stays idle, and the operator sees an
// account that looks bound but cannot answer.
//
// The plugin therefore records the window and temporarily disables the limited
// account's own channel row. Disabling the row instead of unpublishing it keeps the
// published model list and the usage attribution intact, and it is the same switch the
// AI provider page offers by hand; the row is enabled again once the window has passed,
// or as soon as a probe or a routed request proves the credential serves traffic again.
// Every account of the gateway keeps its own row, so its siblings stay routable.
const (
	// clinePassQuotaHoldDefault is the hold used when the gateway does not name a reset
	// window. It is deliberately short: the next rejection refreshes the hold, so
	// guessing low costs one failed attempt while guessing high would keep a recovered
	// account out of the pool.
	clinePassQuotaHoldDefault = 15 * time.Minute
	// clinePassQuotaHoldMax bounds a parsed window so a malformed or hostile message
	// cannot park an account for ever. The documented windows are weekly.
	clinePassQuotaHoldMax = 30 * 24 * time.Hour
	// clinePassQuotaHoldMin drops a parsed window that would expire before the row write
	// that acts on it.
	clinePassQuotaHoldMin = time.Minute
	// clinePassQuotaHoldExtendThreshold is how much longer a repeat rejection has to push
	// the recorded window before the store is written again. The window the gateway names
	// does not move, so recording it on every rejected request would write the state file
	// per request for no new information.
	clinePassQuotaHoldExtendThreshold = 10 * time.Minute
	// clinePassQuotaMaxComponent bounds one number of a parsed window, so a nonsense value
	// cannot wrap the duration it is summed into.
	clinePassQuotaMaxComponent = 10000
)

// clinePassQuotaLimitedMarkers identify the gateway's account-level quota answer in the
// error text CPA reports for a finished request, where only a string is available. A
// plain rate limit is deliberately not covered on its own: a transient 429 must not take
// a working account out of the pool.
var clinePassQuotaLimitedMarkers = []string{
	"inference_cap_error",
	"clinepass limit",
	"weekly limit",
	"quota exceeded",
	"quota_exceeded",
	"quota_limited",
	"insufficient quota",
	"out of quota",
}

// clinePassProbeQuotaLimited reports whether one model probe was answered with the
// gateway's quota rejection instead of a model answer.
func clinePassProbeQuotaLimited(result OpenCodeModelTestResult) bool {
	if strings.EqualFold(strings.TrimSpace(result.ReasonCode), "quota_limited") {
		return true
	}
	return result.StatusCode == http.StatusTooManyRequests
}

// clinePassProbeQuotaText joins the sanitized texts a probe carries, so the reset window
// the gateway named can be parsed from whichever one holds it.
func clinePassProbeQuotaText(result OpenCodeModelTestResult) string {
	texts := []string{result.Detail}
	if result.Response != nil {
		texts = append(texts, result.Response.Body)
	}
	return strings.Join(texts, " ")
}

// clinePassCompletionQuotaLimited reports whether a finished request failed because the
// gateway says the account is out of allowance. The status is checked first so an
// authorization failure is never mistaken for one, and the cap message (or the reset
// window it names) is required: a bare 429 is a transient rate limit.
func clinePassCompletionQuotaLimited(completion cpaapi.RequestCompletion) bool {
	switch completion.StatusCode {
	case 0, http.StatusTooManyRequests, http.StatusServiceUnavailable:
	default:
		return false
	}
	message := strings.ToLower(strings.TrimSpace(completion.Error))
	if message == "" {
		return false
	}
	if strings.Contains(message, "reset") && clinePassQuotaResetDelay(message) > 0 {
		return true
	}
	for _, marker := range clinePassQuotaLimitedMarkers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// clinePassQuotaResetHold resolves how long an account must stay out of routing. The
// window the gateway names is authoritative when it parses; anything else falls back to
// the short default hold.
func clinePassQuotaResetHold(text string, now time.Time) time.Time {
	delay := clinePassQuotaResetDelay(text)
	if delay <= 0 {
		delay = clinePassQuotaHoldDefault
	}
	return now.UTC().Add(delay)
}

// clinePassQuotaResetDelay reads the "resets in ..." window out of a gateway message.
// Accepted units are days, hours, minutes and seconds, in any order and in both the
// compact ("3d 13h") and the spelled-out ("2 hours 30 minutes") form, which is what the
// gateway answers with. The window is read from the LAST "reset" in the text, so an
// earlier mention ("quota was reset 2 days ago") cannot inflate it; a text without a
// window reports 0 so the caller can use its default.
func clinePassQuotaResetDelay(text string) time.Duration {
	lowered := strings.ToLower(text)
	if index := strings.LastIndex(lowered, "reset"); index >= 0 {
		lowered = lowered[index:]
	}
	total := time.Duration(0)
	for index := 0; index < len(lowered); {
		if lowered[index] < '0' || lowered[index] > '9' {
			index++
			continue
		}
		start := index
		for index < len(lowered) && lowered[index] >= '0' && lowered[index] <= '9' {
			index++
		}
		value, errParse := strconv.Atoi(lowered[start:index])
		if errParse != nil || value <= 0 || value > clinePassQuotaMaxComponent {
			continue
		}
		unit := clinePassQuotaUnit(lowered[index:])
		if unit <= 0 {
			continue
		}
		total += time.Duration(value) * unit
	}
	if total <= 0 {
		return 0
	}
	if total < clinePassQuotaHoldMin {
		total = clinePassQuotaHoldMin
	}
	if total > clinePassQuotaHoldMax {
		total = clinePassQuotaHoldMax
	}
	return total
}

// clinePassQuotaUnit reads the time unit that follows a number. Only a whole word counts,
// so an unrelated word that happens to start with h/d/m/s (for example "1 month") is not
// read as a unit at all.
func clinePassQuotaUnit(rest string) time.Duration {
	word := strings.TrimSpace(rest)
	end := 0
	for end < len(word) && word[end] >= 'a' && word[end] <= 'z' {
		end++
	}
	switch word[:end] {
	case "d", "day", "days":
		return 24 * time.Hour
	case "h", "hr", "hrs", "hour", "hours":
		return time.Hour
	case "m", "min", "mins", "minute", "minutes":
		return time.Minute
	case "s", "sec", "secs", "second", "seconds":
		return time.Second
	}
	return 0
}

// noteClinePassQuotaLimited records the hold one account just hit and journals a change, so
// the operator can see why an account stopped serving requests. Recording needs no
// management key; the row write that acts on the hold does, and runs on the caller's path
// or in the background maintenance pass. A rejection that only repeats a window already
// recorded reports false and writes nothing.
func (a *App) noteClinePassQuotaLimited(accountID, message string, now time.Time) (time.Time, bool) {
	if a == nil || a.clinePass == nil {
		return time.Time{}, false
	}
	id := strings.TrimSpace(accountID)
	if id == "" {
		return time.Time{}, false
	}
	until := clinePassQuotaResetHold(message, now)
	changed, errMark := a.clinePass.MarkQuotaLimited(id, until)
	if errMark != nil || !changed {
		return until, false
	}
	a.recordClinePassQuotaLimited(id, until)
	return until, true
}

// recordClinePassQuotaLimited journals one recorded hold. The reason code is allow-listed
// and the message carries the window only, never upstream text or a credential.
func (a *App) recordClinePassQuotaLimited(accountID string, until time.Time) {
	if a == nil || a.operations == nil {
		return
	}
	now := time.Now().UTC()
	a.operations.Record(OperationEntry{
		Category:    OperationCategoryOpenCode,
		Action:      OperationActionOpenCodeRefresh,
		Status:      OperationStatusFailed,
		Source:      OperationSourceBackground,
		Scope:       OperationScopeSingle,
		TargetID:    strings.TrimSpace(accountID),
		TargetCount: 1,
		Failed:      1,
		ReasonCode:  OperationFailureClinePassQuotaLimited,
		HTTPStatus:  http.StatusTooManyRequests,
		Message:     "quota limited until " + until.UTC().Format(time.RFC3339),
		StartedAt:   now,
		FinishedAt:  now,
	})
}

// noteClinePassProbeQuota turns one finished model probe into routing state: the gateway's
// quota answer records a hold and takes the account's row out of routing, and a probe that
// answered releases a recorded hold. The manual model test in the UI is the most direct
// signal the plugin has, because it names the account and shows the gateway's own message.
func (a *App) noteClinePassProbeQuota(ctx context.Context, managementKey, accountID string, result OpenCodeModelTestResult) {
	if a == nil || a.clinePass == nil {
		return
	}
	switch {
	case clinePassProbeQuotaLimited(result):
		if _, marked := a.noteClinePassQuotaLimited(accountID, clinePassProbeQuotaText(result), time.Now().UTC()); !marked {
			return
		}
		a.applyClinePassQuotaRowStates(ctx, managementKey)
	case strings.EqualFold(strings.TrimSpace(result.Status), "available"):
		a.releaseClinePassQuotaHold(ctx, managementKey, accountID)
	}
}

// clinePassQuotaRowTargets builds the enabled state every stored account's own channel row
// must have from its recorded hold, together with the accounts whose spent record may be
// dropped once the rows were written.
func (a *App) clinePassQuotaRowTargets(now time.Time) ([]providerChannelRowTarget, []string) {
	targets := make([]providerChannelRowTarget, 0, 2)
	spent := make([]string, 0, 2)
	for _, account := range a.clinePass.ListAccounts() {
		recorded := a.clinePass.QuotaLimitedUntil(account.ID)
		if recorded.IsZero() {
			continue
		}
		targets = append(targets, providerChannelRowTarget{
			BaseURL:  clinePassChannelBaseURL(account.BaseURL),
			APIKey:   a.clinePass.accessToken(account.ID),
			Label:    a.clinePassChannelLabel(account.ID),
			Digest:   a.clinePass.RoutePublishedCredential(account.ID),
			Disabled: recorded.After(now),
		})
		if !recorded.After(now) {
			spent = append(spent, account.ID)
		}
	}
	return targets, spent
}

// applyClinePassQuotaRowStates aligns the live channel rows with the recorded holds: an
// account inside its window gets its own row disabled, so CPA stops selecting a credential
// the gateway is refusing, and an account whose window has passed gets it enabled again.
// The whole list is read once and written once, and only when a row actually differs.
//
// A spent record is dropped only after that write, so a failure is retried by the next pass
// instead of leaving a disabled row behind with nothing left to enable it.
func (a *App) applyClinePassQuotaRowStates(ctx context.Context, managementKey string) bool {
	if a == nil || a.clinePass == nil || strings.TrimSpace(managementKey) == "" {
		return false
	}
	targets, spent := a.clinePassQuotaRowTargets(time.Now().UTC())
	if len(targets) == 0 {
		return false
	}
	wrote, errWrite := a.setProviderChannelRowsDisabled(ctx, managementKey, targets)
	if errWrite != nil {
		return false
	}
	for _, accountID := range spent {
		if _, errClear := a.clinePass.ClearQuotaLimited(accountID); errClear != nil {
			break
		}
	}
	return wrote
}

// releaseClinePassQuotaHold records that the account serves traffic again - a probe or a
// routed request proved it - and applies that to the live channel list. Without a management
// key the record is still released, so the row is enabled by the pass that holds one.
func (a *App) releaseClinePassQuotaHold(ctx context.Context, managementKey, accountID string) bool {
	if a == nil || a.clinePass == nil {
		return false
	}
	released, errRelease := a.clinePass.ReleaseQuotaLimited(accountID)
	if errRelease != nil || !released {
		return false
	}
	if strings.TrimSpace(managementKey) == "" {
		return false
	}
	// Applying here enables the row inline: the operator just watched the probe answer, so
	// the account belongs back in the pool without waiting for the background pass.
	return a.applyClinePassQuotaRowStates(ctx, managementKey)
}

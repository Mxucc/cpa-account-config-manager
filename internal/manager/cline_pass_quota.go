package manager

import (
	"context"
	"errors"
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
// in 2d 22h, please try again later." CPA keeps selecting the channel row that carries
// that credential, so every routed request fails while a sibling account of the same
// gateway - which publishes the same models - stays idle, and the operator sees an
// account that looks bound but cannot answer.
//
// The plugin therefore records the window and temporarily disables the limited account's
// own channel row. Disabling the row instead of unpublishing it keeps the published model
// list and the usage attribution intact, and it is the same switch the AI provider page
// offers by hand; the row is enabled again once the window has passed, or as soon as a
// probe or a routed request proves the credential serves traffic again. Every account of
// the gateway keeps its own row, so its siblings stay routable.
//
// Two rules keep this from doing harm:
//
//   - A row is only ever ENABLED again when this plugin is what disabled it (the per
//     account QuotaRowDisabled record). A row the operator disabled stays disabled,
//     whatever this plugin records about the account.
//   - Taking a row out of routing never depends on the hold being persisted or on the
//     record having changed: the store write is best effort, and a rejection that repeats
//     the window it already recorded still re-applies the row state. A hold that stays in
//     memory only costs the state across a restart, while a rate-limited credential that
//     stays routable takes the whole gateway down.
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

// ClinePassQuotaOutcome reports what one observed quota answer did to the account's
// routing state. The paths that see the gateway's own answer - the model test above all -
// hand it back to the operator, because the failure that matters is not "the account is
// limited" but "the limited account was still routable".
type ClinePassQuotaOutcome struct {
	// Limited reports that the gateway's quota answer was recognised for this account.
	Limited bool `json:"limited"`
	// Until is the moment the plugin will route to the account again.
	Until time.Time `json:"until,omitempty"`
	// Row is what happened to the account's own channel row: "disabled" (taken out of
	// routing by this call), "unchanged" (it already had the wanted state, which includes a
	// row this plugin disabled earlier), "enabled" or "missing" (the account publishes no
	// row of its own).
	Row string `json:"row,omitempty"`
	// RowError is why the row could not be reached. It never carries a credential.
	RowError string `json:"row_error,omitempty"`
	// Persisted reports whether the hold survived to the state store. A hold that could not
	// be written still takes the account out of routing now, but a restart forgets it.
	Persisted bool `json:"persisted"`
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

// noteClinePassQuotaLimited records the hold one account just hit and journals a new or
// changed window, so the operator can see why an account stopped serving requests.
// Observing the rejection never requires a management key; the row write that acts on the
// hold does, and runs on the caller's path or in the background maintenance pass.
//
// A store that cannot be written does not stop the hold: it is journaled, kept in memory
// and reported, because leaving a credential the gateway refuses in the routing pool is
// worse than losing the record across a restart.
func (a *App) noteClinePassQuotaLimited(accountID, message string, now time.Time) (time.Time, bool, bool) {
	if a == nil || a.clinePass == nil {
		return time.Time{}, false, false
	}
	id := strings.TrimSpace(accountID)
	if id == "" {
		return time.Time{}, false, false
	}
	until := clinePassQuotaResetHold(message, now)
	changed, errMark := a.clinePass.MarkQuotaLimited(id, until)
	persisted := errMark == nil
	if errMark != nil {
		a.recordClinePassQuotaFailure(id, "the hold could not be stored: "+errMark.Error())
	}
	if changed {
		a.recordClinePassQuotaLimited(id, until)
	}
	return until, changed, persisted
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

// recordClinePassQuotaFailure journals a hold that could not reach the account's channel
// row, so "the account was rate-limited but stayed routable" is never silent. The message
// is sanitized and carries no credential or upstream text.
func (a *App) recordClinePassQuotaFailure(accountID, reason string) {
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
		Message:     sanitizeClinePassError(reason),
		StartedAt:   now,
		FinishedAt:  now,
	})
}

// noteClinePassProbeQuota turns one finished model probe into routing state: the gateway's
// quota answer records a hold and takes the account's row out of routing, and a probe that
// answered releases a recorded hold. The manual model test in the UI is the most direct
// signal the plugin has, because it names the account and shows the gateway's own message,
// so its outcome is reported back to the caller.
func (a *App) noteClinePassProbeQuota(ctx context.Context, managementKey, accountID string, result OpenCodeModelTestResult) ClinePassQuotaOutcome {
	if a == nil || a.clinePass == nil {
		return ClinePassQuotaOutcome{}
	}
	switch {
	case clinePassProbeQuotaLimited(result):
		until, _, persisted := a.noteClinePassQuotaLimited(accountID, clinePassProbeQuotaText(result), time.Now().UTC())
		outcome := ClinePassQuotaOutcome{Limited: true, Until: until, Persisted: persisted}
		// Applying is deliberately not conditional on the record having changed: a row may
		// still be enabled because an earlier write failed, because the operator enabled it
		// again, or because the state was lost, and a credential the gateway refuses must not
		// stay in the routing pool.
		report, errApply := a.applyClinePassQuotaRowStatesLocked(ctx, managementKey)
		if state, ok := report.States[strings.TrimSpace(accountID)]; ok {
			outcome.Row = state
		}
		if errApply != nil {
			outcome.RowError = sanitizeClinePassError(errApply.Error())
			a.recordClinePassQuotaFailure(accountID, errApply.Error())
		} else if outcome.Row == providerChannelRowMissing {
			a.recordClinePassQuotaFailure(accountID, "the account owns no channel row to take out of routing")
		}
		return outcome
	case strings.EqualFold(strings.TrimSpace(result.Status), "available"):
		a.releaseClinePassQuotaHold(ctx, managementKey, accountID)
	}
	return ClinePassQuotaOutcome{}
}

// clinePassQuotaRowTargets builds the enabled state every stored account's own channel row
// must have, and the accounts whose spent record only needs dropping.
//
// A row is only ever a target when this plugin has a reason to touch it: an account with a
// recorded hold (disable, or enable once the window has passed) or an account whose
// plugin-side disable flag outlived its record (enable, so a lost record cannot leave a row
// out of routing for ever). An account the operator disabled by hand carries neither, and is
// left exactly as it is - including when a record of ours has run out, which must not turn the
// operator's own disable into an enable.
func (a *App) clinePassQuotaRowTargets(now time.Time) ([]providerChannelRowTarget, []string) {
	targets := make([]providerChannelRowTarget, 0, 2)
	spent := make([]string, 0, 2)
	for _, account := range a.clinePass.ListAccounts() {
		recorded := a.clinePass.QuotaLimitedUntil(account.ID)
		ours := a.clinePass.QuotaRowDisabled(account.ID)
		if recorded.IsZero() && !ours {
			continue
		}
		if !recorded.After(now) && !ours {
			// The window has passed and this plugin never took the row out of routing, so
			// there is no row write to make: only the record is dropped.
			spent = append(spent, account.ID)
			continue
		}
		targets = append(targets, providerChannelRowTarget{
			AccountID: account.ID,
			BaseURL:   clinePassChannelBaseURL(account.BaseURL),
			APIKey:    a.clinePass.accessToken(account.ID),
			Label:     a.clinePassChannelLabel(account.ID),
			Digest:    a.clinePass.RoutePublishedCredential(account.ID),
			Disabled:  recorded.After(now),
		})
	}
	return targets, spent
}

// clinePassQuotaRowReport is the per-account outcome of one row write.
type clinePassQuotaRowReport struct {
	// Wrote reports whether the channel list was actually rewritten.
	Wrote bool
	// States maps an account id to what its own row became; see setProviderChannelRowsDisabled.
	States map[string]string
}

// applyClinePassQuotaRowStates aligns the live channel rows with the recorded holds: an
// account inside its window gets its own row disabled, so CPA stops selecting a credential
// the gateway is refusing, and a row this plugin disabled whose window has passed (or whose
// record was lost) is enabled again. The whole list is read once and written once, and only
// when a row actually differs.
//
// The bookkeeping follows what the row write actually did: the plugin-side disable flag is
// set for a row that ended up disabled and cleared for a row that ended up enabled, and a
// spent record is cleared once its account needs no row write any more. A write failure keeps
// both, so the next pass retries instead of leaving a disabled row behind with nothing left
// to enable it.
func (a *App) applyClinePassQuotaRowStates(ctx context.Context, managementKey string) (clinePassQuotaRowReport, error) {
	report := clinePassQuotaRowReport{States: map[string]string{}}
	if a == nil || a.clinePass == nil {
		return report, nil
	}
	if strings.TrimSpace(managementKey) == "" {
		return report, errors.New("management key is unavailable")
	}
	targets, spent := a.clinePassQuotaRowTargets(time.Now().UTC())
	if len(targets) == 0 {
		// Nothing to write, but a spent record still has to go, or every later read would
		// keep offering the same no-op.
		for _, accountID := range spent {
			if _, errClear := a.clinePass.ClearQuotaLimited(accountID); errClear != nil {
				return report, errClear
			}
		}
		return report, nil
	}
	wrote, states, errWrite := a.setProviderChannelRowsDisabled(ctx, managementKey, targets)
	report.Wrote = wrote
	if len(states) > 0 {
		report.States = states
	}
	if errWrite != nil {
		return report, errWrite
	}
	for _, target := range targets {
		switch report.States[target.AccountID] {
		case providerChannelRowDisabled:
			// From here on this plugin owns the disabled state of that row, so only this
			// plugin may enable it again.
			_ = a.clinePass.SetQuotaRowDisabled(target.AccountID, true)
		case providerChannelRowEnabled, providerChannelRowMissing:
			// The account is routable again (or publishes no row at all), so the record and
			// the flag are both spent.
			_ = a.clinePass.SetQuotaRowDisabled(target.AccountID, false)
			if !target.Disabled {
				_, _ = a.clinePass.ClearQuotaLimited(target.AccountID)
			}
		case providerChannelRowUnchanged:
			// A row that already carried the wanted state keeps its bookkeeping: a disabled
			// row this plugin disabled keeps its flag, and an enabled row whose record has
			// just run out still needs the record cleared.
			if target.Disabled {
				continue
			}
			if a.clinePass.QuotaRowDisabled(target.AccountID) {
				_ = a.clinePass.SetQuotaRowDisabled(target.AccountID, false)
			}
			if !a.clinePass.QuotaLimitedUntil(target.AccountID).IsZero() {
				_, _ = a.clinePass.ClearQuotaLimited(target.AccountID)
			}
		}
	}
	for _, accountID := range spent {
		if _, errClear := a.clinePass.ClearQuotaLimited(accountID); errClear != nil {
			return report, errClear
		}
	}
	return report, nil
}

// releaseClinePassQuotaHold records that the account serves traffic again - a probe or a
// routed request proved it - and applies that to the live channel list. Only a row this
// plugin disabled is enabled here; a row the operator disabled stays as it is. Without a
// management key the record is still released, so the row is enabled by the pass that holds
// one.
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
	report, _ := a.applyClinePassQuotaRowStates(ctx, managementKey)
	return report.Wrote
}

// clinePassReleaseQuotaHoldOnSuccess releases a recorded hold after a routed request went
// through. A request can only have reached the gateway through the credential on the
// account's row, so a hold is released only while that row is in the routing pool: a success
// attributed to an account whose row this plugin disabled cannot have come from it, and
// releasing the hold there would put a credential the gateway refuses back in the pool.
func (a *App) clinePassReleaseQuotaHoldOnSuccess(accountID string) bool {
	if a == nil || a.clinePass == nil {
		return false
	}
	if a.clinePass.QuotaRowDisabled(accountID) {
		return false
	}
	released, errRelease := a.clinePass.ReleaseQuotaLimited(accountID)
	return errRelease == nil && released
}

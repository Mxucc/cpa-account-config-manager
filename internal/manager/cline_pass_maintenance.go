package manager

import (
	"context"
	"net/http"
	"strings"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// Cline Pass credential upkeep.
//
// CPA routes a Cline Pass model through the token stored on the account's own
// openai-compatibility row, and that token expires on its own - Cline issues an access token
// with a short lifetime and the plugin refreshes it. Everything that touches the row used to
// happen while the operator was looking at a Cline Pass page, which left a hole: a token that
// lapsed between two page loads kept CPA routing with a credential the gateway had stopped
// accepting, so every request was answered "no auth available (providers=openai-compatible-cline
// pass ..., last upstream error: Unauthorized: ... re-authenticate your Cline account)" until
// somebody opened the page or pressed refresh.
//
// This loop closes the hole: on a fixed interval it rotates the tokens that are about to expire
// (or that the gateway already rejected) and republishes the rows that no longer carry the live
// token - the same work a page read does, without needing the page. A rejected credential can
// also be learned from the usage callback (see noteClinePassUsageOutcome), which reaches the
// plugin even when the request-completion callback does not.
const (
	// clinePassMaintenanceInterval is how often the row is re-checked. Access tokens live for
	// roughly an hour, so a few minutes keeps the row valid without touching the management
	// API more than once per interval.
	clinePassMaintenanceInterval = 3 * time.Minute
	// clinePassMaintenanceInitialDelay lets the instance settle after the first management
	// request before it starts writing channel rows.
	clinePassMaintenanceInitialDelay = 20 * time.Second
	// clinePassMaintenanceTimeout bounds one pass: token rotations plus the channel writes.
	clinePassMaintenanceTimeout = clinePassAuthRepairTimeout
)

// startClinePassMaintenance starts the upkeep loop. It is idempotent, and it does nothing once
// the instance has been shut down.
func (a *App) startClinePassMaintenance() {
	if a == nil {
		return
	}
	a.clinePassMaintenanceOnce.Do(func() {
		a.clinePassRepairMu.Lock()
		if a.clinePassMaintenanceStopped {
			a.clinePassRepairMu.Unlock()
			return
		}
		stop := make(chan struct{})
		done := make(chan struct{})
		a.clinePassMaintenanceStop = stop
		a.clinePassMaintenanceDone = done
		a.clinePassRepairMu.Unlock()
		go func() {
			defer close(done)
			a.runClinePassMaintenanceLoop(stop)
		}()
	})
}

// stopClinePassMaintenance stops the loop and waits briefly for a running pass to finish, so
// shutdown never races a channel write.
func (a *App) stopClinePassMaintenance() {
	if a == nil {
		return
	}
	a.clinePassRepairMu.Lock()
	a.clinePassMaintenanceStopped = true
	stop, done := a.clinePassMaintenanceStop, a.clinePassMaintenanceDone
	a.clinePassMaintenanceStop, a.clinePassMaintenanceDone = nil, nil
	a.clinePassRepairMu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

func (a *App) runClinePassMaintenanceLoop(stop <-chan struct{}) {
	timer := time.NewTimer(clinePassMaintenanceInitialDelay)
	defer timer.Stop()
	for {
		select {
		case <-stop:
			return
		case <-timer.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), clinePassMaintenanceTimeout)
		a.runClinePassMaintenance(ctx)
		cancel()
		timer.Reset(clinePassMaintenanceInterval)
	}
}

// runClinePassMaintenance keeps every stored Cline Pass account routable: it rotates the tokens
// that are about to expire (and those the gateway rejected), applies the recorded quota holds,
// and republishes every row that no longer carries the live token.
//
// It needs a management key, which the plugin only ever sees in a management request, so the
// pass reports false while no key is known; the first page load starts it. A superseded instance
// does nothing at all: the row writes have one owner.
func (a *App) runClinePassMaintenance(ctx context.Context) bool {
	if a == nil || a.clinePass == nil || a.runtimeSuperseded() {
		return false
	}
	managementKey := a.clinePassManagementKey()
	if managementKey == "" {
		return false
	}
	changed := a.refreshExpiringClinePassAccounts(ctx) > 0
	if a.applyClinePassQuotaRows(ctx, managementKey) {
		changed = true
	}
	if len(a.clinePass.ListAccounts()) == 0 {
		return changed
	}
	// The same repair a page read runs: an account whose row no longer carries the live token,
	// one whose credential the gateway rejected, and one that owns no row yet are published
	// again. This is what makes a rotation performed by the pass itself harmless: the row CPA
	// routes through is rewritten in the same tick.
	a.republishClinePassRows(ctx, managementKey, true)
	return true
}

// clinePassManagementKey reports the management key the plugin last saw, without touching the
// repair throttle: the upkeep loop writes channel rows on its own timetable and must not spend
// the repair budget of a rejected credential.
func (a *App) clinePassManagementKey() string {
	if a == nil {
		return ""
	}
	a.clinePassRepairMu.Lock()
	defer a.clinePassRepairMu.Unlock()
	return a.clinePassRepairKey
}

// applyClinePassQuotaRows runs the quota pass under the channel-write lock, so it can never
// interleave with a bind that writes the same list. It reports whether the list was rewritten.
func (a *App) applyClinePassQuotaRows(ctx context.Context, managementKey string) bool {
	report, _ := a.applyClinePassQuotaRowStatesLocked(ctx, managementKey)
	return report.Wrote
}

// applyClinePassQuotaRowStatesLocked is applyClinePassQuotaRows for a caller that needs the
// per-account report as well.
func (a *App) applyClinePassQuotaRowStatesLocked(ctx context.Context, managementKey string) (clinePassQuotaRowReport, error) {
	a.clinePassWriteMu.Lock()
	defer a.clinePassWriteMu.Unlock()
	return a.applyClinePassQuotaRowStates(ctx, managementKey)
}

// noteClinePassUsageOutcome learns a rejected Cline Pass credential from a usage callback. CPA
// reports a failed request on this callback - with the upstream status and body - even when the
// request-completion callback is not delivered to the plugin, so it is the second channel that
// keeps a refused credential from staying in the routing pool. A successful record is ignored:
// releasing a hold or a rejection is the completion path's job, which knows whether the account
// was actually routable.
func (a *App) noteClinePassUsageOutcome(record cpaapi.UsageRecord) {
	if a == nil || a.clinePass == nil {
		return
	}
	defer func() {
		// Learning from a usage callback must never reach CPA's usage path as a failure.
		_ = recover()
	}()
	if !record.Failed || !clinePassUsageLooksRejected(record) {
		return
	}
	accountID := a.clinePassAccountForUsage(record)
	if accountID == "" {
		return
	}
	if !a.clinePass.NoteAuthFailure(accountID) {
		return
	}
	a.recordClinePassAuthFailure(accountID, cpaapi.RequestCompletion{
		StatusCode: record.Failure.StatusCode,
		Model:      firstNonEmpty(record.Model, record.Alias),
	})
	a.requestClinePassAuthRepair()
}

// clinePassUsageLooksRejected reports whether one failed usage record is a credential rejection
// rather than an ordinary upstream failure. The status decides first, and a 5xx (or a record
// without a status) additionally needs one of the authorization markers in the body, because
// CPA reports its own cooled credential as 503.
func clinePassUsageLooksRejected(record cpaapi.UsageRecord) bool {
	status := record.Failure.StatusCode
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return true
	case 0, http.StatusServiceUnavailable:
	default:
		return false
	}
	body := strings.ToLower(strings.TrimSpace(record.Failure.Body))
	if body == "" {
		return false
	}
	for _, marker := range clinePassAuthErrorMarkers {
		if strings.Contains(body, marker) {
			return true
		}
	}
	return false
}

// clinePassAccountForUsage resolves the stored account one usage record belongs to, using the
// same identities the ledger uses. Unlike the ledger key this never falls back to a digest: a
// rejection is only ever recorded for an account the plugin can name, so nothing is guessed
// while several accounts could have served the request.
func (a *App) clinePassAccountForUsage(record cpaapi.UsageRecord) string {
	if a == nil || a.clinePass == nil {
		return ""
	}
	if accountID := a.clinePass.AccountIDForAuthIndex(record.AuthIndex); accountID != "" {
		return accountID
	}
	if accountID := a.clinePass.AccountIDForAuthIdentity(record.AuthID); accountID != "" {
		return accountID
	}
	if credential := strings.TrimSpace(record.APIKey); credential != "" {
		if accountID := a.clinePass.AccountIDForAuthIdentity(credential); accountID != "" {
			return accountID
		}
		if accountID := a.clinePass.AccountIDForPublishedCredential(credential); accountID != "" {
			return accountID
		}
	}
	if clinePassIsPublishedModel(firstNonEmpty(record.Model, record.Alias)) {
		return a.clinePass.SoleAccountID()
	}
	return ""
}

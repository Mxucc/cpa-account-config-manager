package manager

import (
	"context"
	"strings"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// Cline Pass authorization repair.
//
// A Cline Pass access token can be rejected before its recorded expiry, and CPA routes only through
// the key stored on its channel row. Once the gateway answers "unauthorized", CPA cools that
// credential down and later requests fail with "no auth available", while every plugin page still
// reports the account as bound. Recovering used to mean refreshing the token and deleting and
// re-adding the AI-provider account by hand; the plugin now notices the rejection, rotates the
// token and republishes the account's own row.
//
// The repair is best effort and off the request path: a rejection only records the account, and the
// repair runs when the plugin holds a management key, under a backoff so a burst of failing requests
// coalesces into one rotation and one channel write. A repair that cannot run leaves the record in
// place, and the next account page read performs the same repair inline.
const (
	// clinePassAuthRepairTimeout bounds one repair: a token rotation plus a channel write.
	clinePassAuthRepairTimeout = 2 * clinePassRoutingBindTimeout
	// clinePassAuthRepairBackoff throttles the repairs a burst of rejected requests asks for.
	clinePassAuthRepairBackoff = 15 * time.Second
)

// clinePassAuthErrorMarkers are the lowercase fragments that identify an authorization failure in
// the sanitized error text CPA reports. They cover the gateway's own answer ("Unauthorized: Please
// make sure you're using the latest version of Cline and re-authenticate your Cline account"), the
// OAuth exchange failures, and CPA's own "no auth available" once it has cooled the credential down.
// A quota or rate-limit answer is deliberately absent: it needs no rotation.
var clinePassAuthErrorMarkers = []string{
	"unauthorized",
	"unauthenticated",
	"re-authenticate",
	"reauthenticate",
	"invalid_grant",
	"invalid_token",
	"authentication failed",
	"auth_unavailable",
	"no auth available",
}

// noteClinePassRequestOutcome learns from one finished request: a successful Cline Pass request
// clears a recorded rejection, and an authorization failure records one and asks for a repair.
func (a *App) noteClinePassRequestOutcome(completion cpaapi.RequestCompletion) {
	if a == nil || a.clinePass == nil {
		return
	}
	defer func() {
		// Learning from a completion must never reach CPA's completion callback as a failure.
		_ = recover()
	}()
	outcome := strings.ToLower(strings.TrimSpace(completion.Outcome))
	accountID := a.clinePassAccountForCompletion(completion)
	// The gateway's quota answer needs no rotation: the credential is fine, the account
	// is simply out of allowance for its window. The hold takes it out of routing until
	// that window ends, which is what lets a sibling account of the same gateway serve
	// the models in the meantime.
	if clinePassCompletionQuotaLimited(completion) {
		if accountID == "" {
			return
		}
		if _, changed, _ := a.noteClinePassQuotaLimited(accountID, completion.Error, time.Now().UTC()); changed {
			a.requestClinePassAuthRepair()
		}
		return
	}
	if outcome != requestCompletionSucceeded && !clinePassCompletionLooksAuthorizationFailure(completion, outcome) {
		return
	}
	if accountID == "" {
		return
	}
	if outcome == requestCompletionSucceeded {
		// The credential works, so a recorded rejection is spent and the account stops asking.
		a.clinePass.ClearAuthFailure(accountID)
		// A request that went through proves the account serves traffic again, so a
		// recorded quota hold is released as well; the row is enabled by the maintenance
		// pass, which holds the management key this path does not have.
		if a.clinePassReleaseQuotaHoldOnSuccess(accountID) {
			a.requestClinePassAuthRepair()
		}
		return
	}
	if !a.clinePass.NoteAuthFailure(accountID) {
		return
	}
	a.recordClinePassAuthFailure(accountID, completion)
	a.requestClinePassAuthRepair()
}

// clinePassCompletionLooksAuthorizationFailure reports whether a finished request failed because the
// credential was rejected. The status code is the first signal, because CPA reports its own cold
// credential as 503 while the upstream answer that caused it is only in the error text.
func clinePassCompletionLooksAuthorizationFailure(completion cpaapi.RequestCompletion, outcome string) bool {
	if outcome == requestCompletionCanceled {
		return false
	}
	if completion.StatusCode == 401 || completion.StatusCode == 403 {
		return true
	}
	if completion.StatusCode != 0 && completion.StatusCode != 503 {
		// A quota, rate-limit or upstream failure with its own status needs no rotation.
		return false
	}
	message := strings.ToLower(strings.TrimSpace(completion.Error))
	if message == "" {
		return false
	}
	for _, marker := range clinePassAuthErrorMarkers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// clinePassAccountForCompletion resolves the stored Cline Pass account one finished request belongs
// to: the auth index a bind recorded for the account's own channel row first, then the auth identity,
// and only then the sole stored account when the request names a published Cline Pass model. Nothing
// is guessed while several accounts could have served the request.
func (a *App) clinePassAccountForCompletion(completion cpaapi.RequestCompletion) string {
	if a == nil || a.clinePass == nil {
		return ""
	}
	for _, key := range []string{"selected_auth_index", "auth_index"} {
		if value, ok := completion.Metadata[key].(string); ok {
			if accountID := a.clinePass.AccountIDForAuthIndex(value); accountID != "" {
				return accountID
			}
		}
	}
	for _, key := range []string{"selected_auth_id", "auth_id"} {
		if value, ok := completion.Metadata[key].(string); ok {
			if accountID := a.clinePass.AccountIDForAuthIdentity(value); accountID != "" {
				return accountID
			}
		}
	}
	if !clinePassCompletionNamesPublishedModel(completion) {
		return ""
	}
	return a.clinePass.SoleAccountID()
}

// clinePassCompletionNamesPublishedModel reports whether a finished request named a model this
// plugin publishes for Cline Pass, accepting both the prefixed and the stripped client id.
func clinePassCompletionNamesPublishedModel(completion cpaapi.RequestCompletion) bool {
	for _, value := range []string{completion.Model, completion.RequestedModel} {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if clinePassAllowsModel(trimmed) || clinePassAllowsModel(clinePassModelPrefix+trimmed) {
			return true
		}
		if clinePassIsPublishedModel(trimmed) || clinePassIsPublishedModel(clinePassModelPrefix+trimmed) {
			return true
		}
	}
	return false
}

// recordClinePassAuthFailure journals one rejected credential so the operator can see why a Cline
// Pass account stopped working, whether or not the automatic repair could run.
func (a *App) recordClinePassAuthFailure(accountID string, completion cpaapi.RequestCompletion) {
	if a == nil || a.operations == nil {
		return
	}
	a.operations.Record(OperationEntry{
		Category:    OperationCategoryOpenCode,
		Action:      OperationActionOpenCodeRefresh,
		Status:      OperationStatusFailed,
		Source:      OperationSourceBackground,
		Scope:       OperationScopeSingle,
		TargetID:    strings.TrimSpace(accountID),
		Model:       completion.Model,
		HTTPStatus:  completion.StatusCode,
		TargetCount: 1,
		Failed:      1,
		ReasonCode:  OperationFailureClinePassCredentialRejected,
		StartedAt:   time.Now().UTC(),
		FinishedAt:  time.Now().UTC(),
	})
}

// rememberClinePassManagementKey keeps the last management key the plugin saw. The automatic repair
// writes a channel row, which needs that key, and no other path provides one while the operator is
// simply using the gateway. The key is held in memory only and is never logged.
func (a *App) rememberClinePassManagementKey(managementKey string) {
	if a == nil {
		return
	}
	trimmed := strings.TrimSpace(managementKey)
	if trimmed == "" {
		return
	}
	a.clinePassRepairMu.Lock()
	a.clinePassRepairKey = trimmed
	a.clinePassRepairMu.Unlock()
}

// clinePassRepairCredentials returns the held management key and whether a repair may run now. The
// backoff is claimed here, so a burst of failing requests starts one repair instead of one each.
func (a *App) clinePassRepairCredentials() (string, bool) {
	if a == nil {
		return "", false
	}
	a.clinePassRepairMu.Lock()
	defer a.clinePassRepairMu.Unlock()
	if a.clinePassRepairKey == "" || a.clinePassRepairRunning {
		return "", false
	}
	now := time.Now()
	if !a.clinePassRepairAt.IsZero() && now.Sub(a.clinePassRepairAt) < clinePassAuthRepairBackoff {
		return "", false
	}
	a.clinePassRepairAt = now
	a.clinePassRepairRunning = true
	return a.clinePassRepairKey, true
}

// requestClinePassAuthRepair repairs the accounts whose credential the gateway rejected, off the
// request path. It needs a management key the plugin has seen; without one the record stays and the
// next account page read performs the same repair.
func (a *App) requestClinePassAuthRepair() {
	if a == nil || a.clinePass == nil {
		return
	}
	managementKey, ok := a.clinePassRepairCredentials()
	if !ok {
		return
	}
	go func() {
		defer func() {
			// A repair runs on its own goroutine, so a panic must be contained here rather than
			// reaching the host process.
			_ = recover()
			a.clinePassRepairMu.Lock()
			a.clinePassRepairRunning = false
			a.clinePassRepairMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), clinePassAuthRepairTimeout)
		defer cancel()
		a.repairRejectedClinePassAccounts(ctx, managementKey)
	}()
}

// repairRejectedClinePassAccounts rotates the tokens that are due, republishes every channel row
// that no longer matches the token it publishes, and then applies the recorded quota holds to the
// same list. Rotation happens first, so the republish writes the new key. One maintenance pass owns
// both writers of the channel list, which is why the quota hold of a request that arrived off the
// page path is applied here instead of by a second background task: two passes writing the whole
// list would lose each other's update.
func (a *App) repairRejectedClinePassAccounts(ctx context.Context, managementKey string) {
	if a == nil || a.clinePass == nil || strings.TrimSpace(managementKey) == "" {
		return
	}
	a.refreshExpiringClinePassAccounts(ctx)
	a.republishClinePassRows(ctx, managementKey, true)
	_, _ = a.applyClinePassQuotaRowStates(ctx, managementKey)
}

// republishClinePassRows republishes the accounts whose channel row is not the row they route
// through any more. With publishMissing it also publishes the accounts that own no row at all,
// which is what the maintenance pass does and what makes one page load enough to recover a
// deployment; the request paths ask for the narrow form, because a manual probe or model list
// should repair what it broke and not silently publish channels of its own.
//
// A rotation is the case this exists for: refreshing invalidates the token the row was published
// with, so a row left behind routes every request into an authorization error - the account keeps
// looking bound while nothing can be served through it.
//
// A successful republish is what spends a rejection record; a failed one keeps it, and the next
// pass tries again under the same throttles.
func (a *App) republishClinePassRows(ctx context.Context, managementKey string, publishMissing bool) {
	if a == nil || a.clinePass == nil || strings.TrimSpace(managementKey) == "" {
		return
	}
	routes, readable := a.clinePassChannelRoutes(ctx, managementKey)
	if !readable {
		// An unreadable list is not an empty one: rebinding on top of it could publish a
		// duplicate row, and every account would be rewritten for the wrong reason.
		return
	}
	for _, account := range a.clinePass.ListAccounts() {
		if ctx.Err() != nil {
			return
		}
		credential := a.clinePass.accessToken(account.ID)
		_, bound := clinePassChannelRouteLookup(account, credential, routes)
		stale := clinePassRouteRowStale(account, a.clinePassChannelLabel(account.ID), credential, routes)
		rejected := a.clinePass.AuthFailurePending(account.ID)
		if !stale && !rejected && !(publishMissing && !bound) {
			continue
		}
		if outcome := a.bindClinePassAccountBestEffort(ctx, managementKey, account.ID, true); outcome.Bound {
			a.clinePass.ClearAuthFailure(account.ID)
		}
	}
}

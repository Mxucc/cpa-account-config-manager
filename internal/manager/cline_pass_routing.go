package manager

import (
	"context"
	"strings"
	"time"
)

// clinePassRoutingBindTimeout bounds one automatic bind: the channel list read,
// the channel write and the Cline version lookup all share it so a slow CPA
// management API can never hold a save or sign-in request open indefinitely.
const clinePassRoutingBindTimeout = 20 * time.Second

// clinePassChannelRoute is the live CPA channel state that publishes one Cline
// Pass base URL: the model ids it advertises and how many it holds.
type clinePassChannelRoute struct {
	published map[string]struct{}
	// aliases holds only the client-facing alias of each row, which is the id a
	// client calls. It is what the model page compares a client id against.
	aliases map[string]struct{}
	models  int
}

// clinePassChannelRouteKey indexes one channel row by its base URL and, when it has one, by its
// credential. Several accounts of one kind share the gateway base URL, so the credential is what
// tells their rows apart.
func clinePassChannelRouteKey(baseURL, apiKey string) string {
	key := canonicalProviderBaseURL(baseURL)
	if key == "" {
		return ""
	}
	if trimmed := strings.TrimSpace(apiKey); trimmed != "" {
		return key + "\x00" + trimmed
	}
	return key
}

// clinePassChannelRoutes reads the OpenAI-compatible channel list once and indexes it twice: by
// canonical base URL (first row wins, which is what a summary over every account needs) and by
// base URL plus credential (which is what one account needs, now that each account has its own
// row). The management key is never logged or returned.
//
// readable reports whether the list could actually be read. Without it every caller would show the
// same "not bound" state for two very different situations - a channel that is genuinely missing
// and a management API this plugin could not reach - and an operator whose channel IS published
// would be told on every refresh to go and publish it. The second return value lets the pages say
// which of the two happened.
func (a *App) clinePassChannelRoutes(ctx context.Context, managementKey string) (map[string]clinePassChannelRoute, bool) {
	routes := map[string]clinePassChannelRoute{}
	if a == nil || strings.TrimSpace(managementKey) == "" {
		return routes, false
	}
	entries, errRead := a.aiProviderChannelEntries(ctx, managementKey, "openai-compatibility")
	if errRead != nil {
		return routes, false
	}
	for _, entry := range entries {
		baseKey := canonicalProviderBaseURL(aiProviderChannelBaseURL(entry))
		if baseKey == "" {
			continue
		}
		route := clinePassChannelRoute{
			published: aiProviderChannelPublishedModels(entry),
			aliases:   aiProviderChannelModelAliases(entry),
			models:    clinePassChannelModelCount(entry),
		}
		if _, exists := routes[baseKey]; !exists {
			routes[baseKey] = route
		}
		if credentialKey := clinePassChannelRouteKey(aiProviderChannelBaseURL(entry), aiProviderChannelCredential(entry)); credentialKey != "" {
			routes[credentialKey] = route
		}
	}
	return routes, true
}

// clinePassAutoBindCooldown throttles the automatic binds a page load triggers.
const clinePassAutoBindCooldown = 30 * time.Second

// clinePassAutoBindDue reports whether an automatic bind may run for one account now, and
// records the attempt. A read that keeps finding an account unbound must not rewrite the
// channel on every request, so the cooldown holds the retries back; an account that binds
// successfully never asks again because it stops being unbound.
func (a *App) clinePassAutoBindDue(accountID string) bool {
	if a == nil || strings.TrimSpace(accountID) == "" {
		return false
	}
	a.clinePassAutoBindMu.Lock()
	defer a.clinePassAutoBindMu.Unlock()
	if a.clinePassAutoBindAt == nil {
		a.clinePassAutoBindAt = map[string]time.Time{}
	}
	if attemptedAt, ok := a.clinePassAutoBindAt[accountID]; ok && time.Since(attemptedAt) < clinePassAutoBindCooldown {
		return false
	}
	a.clinePassAutoBindAt[accountID] = time.Now()
	return true
}

// clinePassRoutesWithAutoBind reads the channel index and repairs the accounts it does not
// route. Binding is not an action the operator should have to remember: a credential
// rotation rewrites an account's token while the channel keeps the old one, so the page a
// load renders is where the missing row is noticed and where it is published again. Every
// unbound account may attempt one bind under the shared cooldown, and the index is re-read
// only after one of them succeeded.
func (a *App) clinePassRoutesWithAutoBind(ctx context.Context, managementKey string, accounts []ClinePassAccountView) (map[string]clinePassChannelRoute, bool) {
	routes, readable := a.clinePassChannelRoutes(ctx, managementKey)
	if a == nil || a.clinePass == nil || len(accounts) == 0 || !readable {
		// An unreadable list is not an empty one: binding on top of it could publish a
		// duplicate row, and every account would be reported unbound for the wrong reason.
		return routes, readable
	}
	unbound := make([]ClinePassAccountView, 0, len(accounts))
	for _, account := range accounts {
		if _, bound := clinePassChannelRouteLookup(account, a.clinePass.accessToken(account.ID), routes); !bound {
			unbound = append(unbound, account)
		}
	}
	if len(unbound) == 0 {
		return routes, readable
	}
	bound := false
	for _, account := range unbound {
		if !a.clinePassAutoBindDue(account.ID) {
			continue
		}
		if outcome := a.bindClinePassAccountBestEffort(ctx, managementKey, account.ID, false); outcome.Bound {
			bound = true
		}
	}
	if !bound {
		return routes, readable
	}
	routes, readable = a.clinePassChannelRoutes(ctx, managementKey)
	return routes, readable
}

// clinePassChannelRouteLookup prefers the account's own row (base URL plus its credential) and
// falls back to the base-URL row, which is what a deployment that has not re-bound yet still has.
func clinePassChannelRouteLookup(view ClinePassAccountView, apiKey string, routes map[string]clinePassChannelRoute) (clinePassChannelRoute, bool) {
	baseURL := clinePassChannelBaseURL(view.BaseURL)
	if credentialKey := clinePassChannelRouteKey(baseURL, apiKey); credentialKey != "" {
		if route, ok := routes[credentialKey]; ok {
			return route, true
		}
	}
	baseKey := canonicalProviderBaseURL(baseURL)
	if baseKey == "" {
		return clinePassChannelRoute{}, false
	}
	route, ok := routes[baseKey]
	return route, ok
}

// aiProviderChannelPublishedModels collects every model id one live channel
// entry advertises. Both a row name and its alias name a routable model.
func aiProviderChannelPublishedModels(entry map[string]any) map[string]struct{} {
	published := map[string]struct{}{}
	list, ok := entry["models"].([]any)
	if !ok {
		return published
	}
	for _, item := range list {
		switch row := item.(type) {
		case string:
			if id := strings.TrimSpace(row); id != "" {
				published[id] = struct{}{}
			}
		case map[string]any:
			for _, field := range []string{"name", "alias"} {
				id, ok := row[field].(string)
				if !ok {
					continue
				}
				if trimmed := strings.TrimSpace(id); trimmed != "" {
					published[trimmed] = struct{}{}
				}
			}
		}
	}
	return published
}

// aiProviderChannelModelAliases collects the client-facing ids one live channel
// advertises: the alias field only, because that is what CPA exposes to
// clients. A legacy string row is its own alias.
func aiProviderChannelModelAliases(entry map[string]any) map[string]struct{} {
	aliases := map[string]struct{}{}
	list, ok := entry["models"].([]any)
	if !ok {
		return aliases
	}
	for _, item := range list {
		switch row := item.(type) {
		case string:
			if id := strings.TrimSpace(row); id != "" {
				aliases[id] = struct{}{}
			}
		case map[string]any:
			value, ok := row["alias"].(string)
			if !ok {
				continue
			}
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				aliases[trimmed] = struct{}{}
			}
		}
	}
	return aliases
}

// clinePassChannelModelCount reports how many distinct upstream model ids one
// live channel entry publishes. The row count is not that number: operator
// additions can outnumber the catalog the binding owns.
func clinePassChannelModelCount(entry map[string]any) int {
	ids := map[string]struct{}{}
	list, ok := entry["models"].([]any)
	if !ok {
		return 0
	}
	for _, item := range list {
		switch row := item.(type) {
		case string:
			if id := strings.TrimSpace(row); id != "" {
				ids[id] = struct{}{}
			}
		case map[string]any:
			id, _ := row["name"].(string)
			if strings.TrimSpace(id) == "" {
				// A hand-written row may only carry the client-facing alias.
				id, _ = row["alias"].(string)
			}
			if trimmed := strings.TrimSpace(id); trimmed != "" {
				ids[trimmed] = struct{}{}
			}
		}
	}
	return len(ids)
}

// clinePassAccountModelIDs returns the models an account publishes, falling back
// to the allow-listed catalog so an account whose listing refreshed to nothing
// still reports a meaningful routing gap.
func clinePassAccountModelIDs(view ClinePassAccountView) []string {
	if len(view.Models) > 0 {
		return view.Models
	}
	return clinePassCatalogIDs()
}

// applyClinePassRouteState fills the routing fields of one account view from the channel index.
// The account's own credential selects its own row when the deployment already gave every account
// one; without it the base-URL row is used. An unbound account reports channel_models=0 and a gap
// equal to its model count, so "not bound" can never be mistaken for "all models routed".
func applyClinePassRouteState(view ClinePassAccountView, apiKey string, routes map[string]clinePassChannelRoute) ClinePassAccountView {
	models := clinePassAccountModelIDs(view)
	route, bound := clinePassChannelRouteLookup(view, apiKey, routes)
	if !bound {
		view.ChannelBound = false
		view.ChannelModels = 0
		view.ChannelModelGaps = len(models)
		return view
	}
	gaps := 0
	for _, model := range models {
		if _, ok := route.published[strings.TrimSpace(model)]; !ok {
			gaps++
		}
	}
	view.ChannelBound = true
	view.ChannelModels = route.models
	view.ChannelModelGaps = gaps
	return view
}

// annotateClinePassRouteState fills the routing fields of the account views a
// response carries. The channel list is read once and shared by every view; nil
// views are skipped. An account that is not routed yet is published here, so a
// page load repairs the channel a credential rotation left behind.
func (a *App) annotateClinePassRouteState(ctx context.Context, managementKey string, views ...*ClinePassAccountView) {
	targets := make([]*ClinePassAccountView, 0, len(views))
	for _, view := range views {
		if view != nil {
			targets = append(targets, view)
		}
	}
	if len(targets) == 0 {
		return
	}
	accounts := make([]ClinePassAccountView, 0, len(targets))
	for _, view := range targets {
		accounts = append(accounts, *view)
	}
	routes, readable := a.clinePassRoutesWithAutoBind(ctx, managementKey, accounts)
	for _, view := range targets {
		// Reporting "not bound" for a list this plugin could not read would send the operator
		// looking for a missing channel that may well be published; the pages say which it is.
		view.ChannelStateUnreadable = !readable
		// The stored credential is what identifies this account's own channel row; it is read
		// without any refresh or network call and never leaves this function.
		credential := ""
		if a.clinePass != nil {
			credential = a.clinePass.accessToken(view.ID)
		}
		*view = applyClinePassRouteState(*view, credential, routes)
	}
}

// clinePassBindOutcome reports one automatic bind attempt. Exactly one of Bound
// and ErrorText is meaningful. ErrorText is sanitized and never carries a
// credential, a header or the base URL.
type clinePassBindOutcome struct {
	Bound     bool
	Result    ProviderChannelBindingResult
	ErrorText string
}

// fields renders the outcome as the optional response fields the UI reads:
// "binding" on success, "binding_error" on failure.
func (o clinePassBindOutcome) fields() map[string]any {
	if o.Bound {
		return map[string]any{"binding": o.Result}
	}
	if o.ErrorText != "" {
		return map[string]any{"binding_error": o.ErrorText}
	}
	return nil
}

// bindClinePassAccountBestEffort publishes a saved Cline Pass account to CPA
// routing. It never fails the caller: the account is already stored, and a
// failure is reported through the outcome instead. The write uses a bounded
// context and runs on the request goroutine.
//
// journalFailures records a failed attempt in the operation history. An attempt
// the operator triggered is always worth recording; the repair a page read runs
// retries on its own throttle, so its failures stay out of the history instead of
// filling it with one entry per page load while a credential is broken.
func (a *App) bindClinePassAccountBestEffort(ctx context.Context, managementKey, accountID string, journalFailures bool) clinePassBindOutcome {
	if a == nil || a.clinePass == nil || strings.TrimSpace(managementKey) == "" {
		return clinePassBindOutcome{ErrorText: "management key is unavailable"}
	}
	bindCtx, cancel := context.WithTimeout(ctx, clinePassRoutingBindTimeout)
	defer cancel()
	startedAt := time.Now().UTC()
	credential, errCredential := a.clinePass.credential(bindCtx, accountID)
	if errCredential != nil {
		if journalFailures {
			a.recordClinePassBinding(startedAt, false)
		}
		return clinePassBindOutcome{ErrorText: sanitizeClinePassError(errCredential.Error())}
	}
	models := credential.Models
	if len(models) == 0 {
		models = clinePassCatalogIDs()
	}
	result, errBind := a.bindClinePassChannel(bindCtx, managementKey, credential.ID, credential.BaseURL, credential.APIKey, a.clinePassChannelLabel(credential.ID), models, a.clinePass.clinePassClientVersion(bindCtx))
	if errBind != nil {
		if journalFailures {
			a.recordClinePassBinding(startedAt, false)
		}
		return clinePassBindOutcome{ErrorText: sanitizeClinePassError(errBind.Error())}
	}
	a.recordClinePassBinding(startedAt, true)
	return clinePassBindOutcome{Bound: true, Result: result}
}

// recordClinePassBinding journals one automatic bind so a routing regression is
// visible in the operation history. The reason codes are allow-listed and carry
// no credential, header or base URL.
func (a *App) recordClinePassBinding(startedAt time.Time, bound bool) {
	if a == nil || a.operations == nil {
		return
	}
	entry := OperationEntry{
		Category: OperationCategoryOpenCode, Action: OperationActionOpenCodeSave,
		Status: OperationStatusFailed, Source: OperationSourceManual, Scope: OperationScopeSingle,
		TargetCount: 1, Failed: 1, StartedAt: startedAt, FinishedAt: time.Now().UTC(), ReasonCode: "channel_bind_failed",
	}
	if bound {
		entry.Status = OperationStatusSucceeded
		entry.Failed = 0
		entry.Succeeded = 1
		entry.ReasonCode = "channel_bound"
	}
	a.operations.Record(entry)
}

// applyClinePassBindOutcome fills an account view from one bind attempt without
// a second channel read: a successful bind published every one of the account's
// models, and a failed or skipped bind leaves the account unbound.
func applyClinePassBindOutcome(view *ClinePassAccountView, outcome clinePassBindOutcome) {
	if view == nil {
		return
	}
	if outcome.Bound {
		view.ChannelBound = true
		view.ChannelModels = outcome.Result.Models
		view.ChannelModelGaps = 0
		return
	}
	view.ChannelBound = false
	view.ChannelModels = 0
	view.ChannelModelGaps = len(clinePassAccountModelIDs(*view))
}

// completeClinePassLogin binds the account a finished sign-in returned so the
// common path produces a routable account, then records the routing state on the
// view. It never fails the sign-in: the credential is already saved and a bind
// failure is reported through the login view instead.
func (a *App) completeClinePassLogin(ctx context.Context, managementKey string, view *ClinePassLoginView) {
	if a == nil || view == nil || view.Account == nil || strings.TrimSpace(managementKey) == "" {
		return
	}
	outcome := a.bindClinePassAccountBestEffort(ctx, managementKey, view.Account.ID, true)
	applyClinePassBindOutcome(view.Account, outcome)
	if outcome.Bound {
		view.Binding = &outcome.Result
	} else if outcome.ErrorText != "" {
		view.BindingError = outcome.ErrorText
	}
}

// clinePassStripModelPrefix reports the current publishing setting; an app
// without a Cline Pass service keeps the documented default (on).
func (a *App) clinePassStripModelPrefix() bool {
	if a == nil || a.clinePass == nil {
		return true
	}
	return a.clinePass.StripModelPrefix()
}

// rebindClinePassAccounts republishes every stored account after a settings
// change so the live channel follows the new alias mapping. The binds run
// sequentially on the request goroutine under the per-bind timeout and are
// best-effort: the counts let the caller report a partial failure instead of
// failing the settings write.
func (a *App) rebindClinePassAccounts(ctx context.Context, managementKey string) (int, int) {
	if a == nil || a.clinePass == nil || strings.TrimSpace(managementKey) == "" {
		return 0, 0
	}
	rebound := 0
	failed := 0
	for _, account := range a.clinePass.ListAccounts() {
		if a.bindClinePassAccountBestEffort(ctx, managementKey, account.ID, true).Bound {
			rebound++
			continue
		}
		failed++
	}
	return rebound, failed
}

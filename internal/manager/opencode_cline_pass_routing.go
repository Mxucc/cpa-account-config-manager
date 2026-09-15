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

// clinePassChannelRoutes reads the OpenAI-compatible channel list once and
// indexes it by canonical base URL. A read failure or a missing management key
// returns an empty index so every caller degrades to the unbound state instead
// of failing the request. The management key is never logged or returned.
func (a *App) clinePassChannelRoutes(ctx context.Context, managementKey string) map[string]clinePassChannelRoute {
	routes := map[string]clinePassChannelRoute{}
	if a == nil || strings.TrimSpace(managementKey) == "" {
		return routes
	}
	entries, errRead := a.aiProviderChannelEntries(ctx, managementKey, "openai-compatibility")
	if errRead != nil {
		return routes
	}
	for _, entry := range entries {
		key := canonicalProviderBaseURL(aiProviderChannelBaseURL(entry))
		if key == "" {
			continue
		}
		if _, exists := routes[key]; exists {
			continue
		}
		routes[key] = clinePassChannelRoute{
			published: aiProviderChannelPublishedModels(entry),
			aliases:   aiProviderChannelModelAliases(entry),
			models:    clinePassChannelModelCount(entry),
		}
	}
	return routes
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
// live channel entry publishes. A Cline Pass model can carry two rows (the
// identity alias and the stripped alias), so the raw row count would overstate
// the catalog the channel exposes.
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

// applyClinePassRouteState fills the routing fields of one account view from the
// channel index. An unbound account reports channel_models=0 and a gap equal to
// its model count, so "not bound" can never be mistaken for "all models routed".
func applyClinePassRouteState(view ClinePassAccountView, routes map[string]clinePassChannelRoute) ClinePassAccountView {
	models := clinePassAccountModelIDs(view)
	route, bound := routes[canonicalProviderBaseURL(clinePassChannelBaseURL(view.BaseURL))]
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
// views are skipped.
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
	routes := a.clinePassChannelRoutes(ctx, managementKey)
	for _, view := range targets {
		*view = applyClinePassRouteState(*view, routes)
	}
}

// clinePassBindOutcome reports one automatic bind attempt. Exactly one of Bound
// and ErrorText is meaningful. ErrorText is sanitized and never carries a
// credential, a header or the base URL.
type clinePassBindOutcome struct {
	Bound     bool
	Result    OpenCodeBindingResult
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
// routing so the common sign-in and save paths produce a routable account. It
// never fails the caller: the account is already stored, and a failure is
// reported through the outcome and the operation journal instead. The write
// uses a bounded context and runs on the request goroutine.
func (a *App) bindClinePassAccountBestEffort(ctx context.Context, managementKey, accountID string) clinePassBindOutcome {
	if a == nil || a.clinePass == nil || strings.TrimSpace(managementKey) == "" {
		return clinePassBindOutcome{ErrorText: "management key is unavailable"}
	}
	bindCtx, cancel := context.WithTimeout(ctx, clinePassRoutingBindTimeout)
	defer cancel()
	startedAt := time.Now().UTC()
	credential, errCredential := a.clinePass.credential(bindCtx, accountID)
	if errCredential != nil {
		a.recordClinePassBinding(startedAt, false)
		return clinePassBindOutcome{ErrorText: sanitizeClinePassError(errCredential.Error())}
	}
	models := credential.Models
	if len(models) == 0 {
		models = clinePassCatalogIDs()
	}
	result, errBind := a.bindClinePassChannel(bindCtx, managementKey, credential.BaseURL, credential.APIKey, a.clinePassChannelLabel(credential.ID), models, a.clinePass.clinePassClientVersion(bindCtx))
	if errBind != nil {
		a.recordClinePassBinding(startedAt, false)
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
	outcome := a.bindClinePassAccountBestEffort(ctx, managementKey, view.Account.ID)
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
		if a.bindClinePassAccountBestEffort(ctx, managementKey, account.ID).Bound {
			rebound++
			continue
		}
		failed++
	}
	return rebound, failed
}

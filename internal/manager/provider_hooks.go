package manager

import (
	"context"
	"errors"
	"strings"
	"time"

	"cpa-account-config-manager/internal/magpiecompat"
	"cpa-account-config-manager/internal/provider"
)

// Provider hooks and settings accessors.

// providerSettingsService returns the settings store for the configured data
// directory, rebuilding it when the directory changes.
func (a *App) providerSettingsService() *ProviderSettingsService {
	if a == nil {
		return NewProviderSettingsService("")
	}
	path := providerSettingsPath(strings.TrimSpace(a.configSnapshot().DataDir))
	a.providerImportsMu.Lock()
	defer a.providerImportsMu.Unlock()
	if a.providerSettings != nil && a.providerSettings.path == path {
		return a.providerSettings
	}
	a.providerSettings = NewProviderSettingsService(path)
	return a.providerSettings
}

// pluginHooks returns the runner used for imported plugin probes.
func (a *App) pluginHooks() pluginHookRunner {
	if a == nil {
		return defaultPluginHookRunner{}
	}
	a.hookRunnerMu.Lock()
	defer a.hookRunnerMu.Unlock()
	if a.hookRunner != nil {
		return a.hookRunner
	}
	return defaultPluginHookRunner{}
}

// setPluginHookRunner is the test seam: it lets a test describe a probe result
// (or a failure) without running third-party code.
func (a *App) setPluginHookRunner(runner pluginHookRunner) {
	if a == nil {
		return
	}
	a.hookRunnerMu.Lock()
	a.hookRunner = runner
	a.hookRunnerMu.Unlock()
}

// pluginRuntimeAvailable reports whether a sidecar runtime exists.
func (a *App) pluginRuntimeAvailable() bool {
	if a == nil {
		return false
	}
	a.hookRunnerMu.Lock()
	override := a.hookRuntimeAvailable
	a.hookRunnerMu.Unlock()
	if override != nil {
		return override()
	}
	return magpiecompat.BunAvailable()
}

func (a *App) recordHookError(providerID, message string) {
	if a == nil || providerID == "" {
		return
	}
	a.hookErrorsMu.Lock()
	if a.hookErrors == nil {
		a.hookErrors = map[string]string{}
	}
	if message == "" {
		delete(a.hookErrors, providerID)
	} else {
		a.hookErrors[providerID] = message
	}
	a.hookErrorsMu.Unlock()
}

// hookError returns the last probe failure for one provider, if any.
func (a *App) hookError(providerID string) string {
	if a == nil {
		return ""
	}
	a.hookErrorsMu.Lock()
	defer a.hookErrorsMu.Unlock()
	return a.hookErrors[providerID]
}

// importedPluginAccounts turns the imported plugins into normalized accounts when
// hook execution is enabled. Everything about the mapping lives in
// provider.FromMagpiePlugin; this only decides whether to run a probe and records
// why one failed.
func (a *App) importedPluginAccounts(ctx context.Context) []provider.Account {
	if a == nil || !a.providerSettingsService().MagpieHookExecution() {
		return nil
	}
	imported := a.providerImportStore().List()
	if len(imported) == 0 {
		return nil
	}
	// The channel list is read once for every provider, so an account can say
	// whether the channel this plugin publishes for it is routable at all.
	states := a.communityChannelStates(ctx)
	out := make([]provider.Account, 0, len(imported))
	for _, entry := range imported {
		if strings.TrimSpace(entry.Directory) == "" {
			// Manifest-only entry: nothing to execute, and the catalog says so.
			continue
		}
		if !a.pluginRuntimeAvailable() {
			a.recordHookError(entry.ID, magpiecompat.ErrBunUnavailable.Error())
			continue
		}
		input, errProbe := a.pluginHooks().Probe(entry.Directory)
		if errProbe != nil {
			a.recordHookError(entry.ID, sanitizedHookError(errProbe))
			continue
		}
		a.recordHookError(entry.ID, "")
		input.ProviderID = entry.ID
		if strings.TrimSpace(input.DisplayName) == "" {
			input.DisplayName = entry.Name
		}
		account := provider.FromMagpiePlugin(input)
		// With a signed-in account the plugin can say who it is, what plan it is on
		// and what allowance is left. Without that the row was only a vendor name
		// with no usage, which is exactly what made a signed-in account look like an
		// unidentified placeholder.
		if status, known := a.communityAccountStatus(ctx, entry.ID); known {
			applyCommunityStatus(&account, status)
		}
		// The credential names the account when the provider put an identity in it,
		// which is what makes two accounts of one vendor tellable apart.
		if a.communityChannelHasCredential(entry.ID) {
			account.Credential.Configured = true
		}
		if label := a.communityAccountLabel(entry.ID); label != "" {
			account.DisplayName = label
		}
		if len(account.Quota) == 0 && account.Credential.Configured {
			// Saying nothing would read as "no limit"; the provider answered with no
			// allowance window, and that is the honest sentence.
			account.StatusNote = "the provider reported no allowance window for this account"
		}
		// A provider whose channel CPA does not route has to say so on the account
		// itself: the routing page reads this field, so an account without one is an
		// account that page cannot describe.
		if state, found := states[strings.ToLower(strings.TrimSpace(entry.ID))]; found {
			bound := state
			account.Binding = &bound
		}
		out = append(out, account)
	}
	return out
}

// applyCommunityStatus puts the signed-in account's identity, plan and allowance on
// the row the page renders. The window mapping is provider.FromMagpiePlugin's, so a
// plugin window follows the same used/limit/balance rules as every other provider.
// Only what the provider reported is filled: an unknown field stays unknown.
func applyCommunityStatus(account *provider.Account, status magpiecompat.AccountModels) {
	if account == nil {
		return
	}
	windows := make([]provider.MagpieWindow, 0, len(status.Windows))
	for _, window := range status.Windows {
		windows = append(windows, provider.MagpieWindow{
			Name: window.Name, Used: window.Used, Limit: window.Limit, Balance: window.Balance,
			Unit: window.Unit, Reset: window.Reset, Aside: window.Aside,
		})
	}
	mapped := provider.FromMagpiePlugin(provider.MagpiePluginInput{
		ProviderID:  account.Provider.ID,
		DisplayName: account.DisplayName,
		Models:      status.Models,
		Windows:     windows,
		UpdatedAt:   time.Now().UTC(),
	})
	if name := strings.TrimSpace(status.Account); name != "" {
		account.DisplayName = name
		account.Credential.Configured = true
	}
	if plan := strings.TrimSpace(status.Plan); plan != "" {
		account.Plan.Name = plan
	}
	if len(mapped.Quota) > 0 {
		account.Quota = mapped.Quota
		if account.Billing.Mode == provider.BillingUnknown {
			account.Billing = mapped.Billing
		}
	}
	if mapped.Models.Count > 0 {
		account.Models = mapped.Models
	}
}

// communityChannelStates maps a community provider id onto the state of the CPA
// channel this plugin published for it. A provider with no channel is absent from
// the map, which differs from a channel that exists but cannot route.
func (a *App) communityChannelStates(ctx context.Context) map[string]provider.Binding {
	out := map[string]provider.Binding{}
	if a == nil {
		return out
	}
	managementKey := strings.TrimSpace(a.communityManagementKey())
	if managementKey == "" {
		return out
	}
	entries, errList := a.aiProviderChannelEntries(ctx, managementKey, "openai-compatibility")
	if errList != nil {
		return out
	}
	const kind = "openai-compatibility"
	imported := a.providerImportStore().List()
	for _, entry := range entries {
		baseURL := strings.TrimRight(strings.TrimSpace(aiProviderChannelBaseURL(entry)), "/")
		if baseURL == "" {
			continue
		}
		models := aiProviderChannelModelCount(entry)
		for _, candidate := range imported {
			id := strings.ToLower(strings.TrimSpace(candidate.ID))
			if id == "" {
				continue
			}
			// Where this provider's channel points is learned from its own package,
			// with the base a previous sign-in recorded as the fallback; the static
			// table only covers providers that publish neither.
			known := a.communityChannelBase(id)
			if known == "" {
				known = a.communityProviderDeclaredBase(id)
			}
			if known == "" {
				known = communityProviderBaseURL(id)
			}
			if strings.TrimRight(strings.TrimSpace(known), "/") != baseURL {
				continue
			}
			out[id] = provider.Binding{
				Kind:      kind,
				Channel:   baseURL,
				Published: true,
				Models:    models,
				Readable:  true,
			}
		}
	}
	return out
}

// sanitizedHookError keeps a probe failure actionable without echoing plugin
// output, which may contain credentials.
func sanitizedHookError(err error) string {
	switch {
	case errors.Is(err, magpiecompat.ErrBunUnavailable):
		return magpiecompat.ErrBunUnavailable.Error()
	case errors.Is(err, magpiecompat.ErrTimeout):
		return magpiecompat.ErrTimeout.Error()
	case errors.Is(err, magpiecompat.ErrOutputTooLarge):
		return magpiecompat.ErrOutputTooLarge.Error()
	case errors.Is(err, magpiecompat.ErrPluginInvalid):
		return "plugin did not answer the probe"
	default:
		return "plugin probe failed"
	}
}
